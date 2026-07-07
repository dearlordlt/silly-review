// Command silly-review is a TUI that produces senior-engineer code reviews of
// git branches and whole-codebase health checks (security, tech debt,
// performance, …) by driving the `claude` CLI, without ever touching the
// user's working tree.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"silly-review/internal/checks"
	"silly-review/internal/config"
	"silly-review/internal/discover"
	"silly-review/internal/gitx"
	"silly-review/internal/history"
	"silly-review/internal/render"
	"silly-review/internal/review"
	"silly-review/internal/tui"
)

var (
	flagModel   string
	flagStyle   string
	flagBase    string
	flagBranch  string
	flagNoTUI   bool
	flagNoFetch bool
	flagJSON    bool
	flagOut     string
	flagFresh   bool
)

func main() {
	root := &cobra.Command{
		Use:           "silly-review",
		Short:         "Senior-engineer code reviews of git branches and codebase health checks, powered by Claude — read-only, never touches your working tree.",
		Version:       buildVersion(),
		Args:          cobra.NoArgs,
		RunE:          runReview,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.Flags().StringVar(&flagModel, "model", "", "model alias (opus|sonnet|haiku|fable); overrides remembered choice")
	root.Flags().StringVar(&flagStyle, "style", "", "review style (thorough|blocking|architecture|security)")
	root.Flags().StringVar(&flagBase, "base", "", "base ref to diff against (e.g. origin/dev); overrides remembered base")
	root.Flags().StringVar(&flagBranch, "branch", "", "branch to review (required with --no-tui)")
	root.Flags().BoolVar(&flagNoTUI, "no-tui", false, "headless: run a single review and print it to stdout")
	root.Flags().BoolVar(&flagNoFetch, "no-fetch", false, "do not fetch from the remote before listing branches")
	root.Flags().BoolVar(&flagJSON, "json", false, "with --no-tui, print the structured review as JSON")
	root.Flags().StringVar(&flagOut, "out", "", "with --no-tui, also write the markdown report to this file")
	root.Flags().BoolVar(&flagFresh, "fresh", false, "ignore any saved prior review for this branch and review from scratch")

	root.AddCommand(configCmd())
	root.AddCommand(updateCmd())
	root.AddCommand(checkCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "silly-review: "+err.Error())
		os.Exit(1)
	}
}

func runReview(cmd *cobra.Command, _ []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	disc, err := discover.Discover(ctx, cwd)
	if err != nil {
		return err
	}
	if len(disc.Repos) == 0 {
		return fmt.Errorf("no git repository found here or in immediate subdirectories")
	}
	bin := claudeBin()
	if err := review.Preflight(ctx, bin); err != nil {
		return err
	}

	ws, err := gitx.NewWorkspace()
	if err != nil {
		return err
	}
	defer ws.Cleanup()
	// Cleanup also on signal, before the process exits.
	go func() {
		<-ctx.Done()
		ws.Cleanup()
	}()

	if flagNoTUI {
		return runHeadless(ctx, cfg, disc, ws, bin)
	}

	cancelCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	return tui.Run(tui.Params{
		Ctx:       cancelCtx,
		Cancel:    cancel,
		Cfg:       cfg,
		Workspace: ws,
		Disc:      disc,
		FolderKey: cwd,
		Fetch:     !flagNoFetch,
		BinPath:   bin,
		Version:   buildVersion(),
	})
}

// buildVersion reports the VCS revision Go embeds at build time, so
// `silly-review --version` makes a stale binary obvious.
func buildVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	var rev, mod string
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			if s.Value == "true" {
				mod = "+dirty"
			}
		}
	}
	if rev == "" {
		return "dev"
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	return rev + mod
}

// claudeBin returns the claude binary to invoke. SILLY_REVIEW_CLAUDE overrides
// the default ("claude") — handy for testing with a stub.
func claudeBin() string {
	if b := os.Getenv("SILLY_REVIEW_CLAUDE"); b != "" {
		return b
	}
	return "claude"
}

func runHeadless(ctx context.Context, cfg *config.Config, disc *discover.Result, ws *gitx.Workspace, bin string) error {
	if disc.Mode == discover.Multi {
		return fmt.Errorf("headless mode supports a single repo; cd into one repo, or drop --no-tui for the multi-repo picker")
	}
	if flagBranch == "" {
		return fmt.Errorf("--branch is required with --no-tui")
	}
	repo := disc.Repos[0]
	if repo.Remote == "" {
		return fmt.Errorf("%s has no remote to review against", repo.Name)
	}
	if !flagNoFetch {
		_ = gitx.Fetch(ctx, repo.Path, repo.Remote)
	}

	head, err := resolveBranchRef(ctx, repo, flagBranch)
	if err != nil {
		return err
	}
	base, err := resolveBase(ctx, cfg, repo)
	if err != nil {
		return err
	}

	if head == base {
		return fmt.Errorf("%s is the base branch — pick a different --branch or change --base", head)
	}
	wt, err := ws.Add(ctx, repo, head)
	if err != nil {
		return fmt.Errorf("creating worktree: %w", err)
	}
	mb, _ := gitx.MergeBase(ctx, repo.Path, base, head)
	stat, statErr := gitx.DiffStat(ctx, repo.Path, base, head, mb)
	if statErr != nil {
		return fmt.Errorf("computing diff vs %s: %w", base, statErr)
	}
	if stat.Files == 0 {
		fmt.Fprintf(os.Stderr, "no changes between %s and %s — nothing to review\n", head, base)
		return nil
	}
	files, _ := gitx.DiffNameStatus(ctx, repo.Path, base, head, mb)
	rc := review.RepoContext{
		Name: repo.Name, WorktreePath: wt.Path, BranchRef: head, BaseRef: base,
		MergeBase: mb, Stat: stat, Files: files,
	}

	style := review.StyleByKey(firstNonEmpty(flagStyle, cfg.Folder(repo.Path).Style))
	model := firstNonEmpty(flagModel, cfg.Folder(repo.Path).Model, config.DefaultModel)

	var prior *review.Review
	if !flagFresh {
		if e, ok := history.Load(repo.Path, head); ok {
			prior = &e.Review
			fmt.Fprintln(os.Stderr, "continuing from the previous review (pass --fresh to start over)")
		}
	}

	fmt.Fprintf(os.Stderr, "reviewing %s vs %s (%s, %s)…\n", head, base, model, style.Key)
	prog := newProgress(os.Stderr)
	prog.start()
	res, err := review.RunWithResume(ctx, review.Options{
		Model:           model,
		System:          review.SystemPrompt(style),
		Prompt:          review.BuildPrompt(rc, nil, prior),
		PrimaryWorktree: wt.Path,
		BinPath:         bin,
	}, prog.event)
	prog.stop()
	if err != nil {
		return err
	}
	// Only save a clean review — an error result can still carry partial
	// structured output, and saving it would clobber a good prior. (The TUI path
	// guards this the same way, in its switch default case.)
	if !res.IsError && res.Review != nil {
		_ = history.Save(repo.Path, head, history.Entry{Repo: repo.Name, Branch: head, Base: base, When: time.Now(), Review: *res.Review})
	}

	rr := render.RepoReview{Repo: repo.Name, Branch: head, Base: base}
	if res.IsError {
		rr.Err = res.ErrMsg
	} else {
		rr.Review = res.Review
		rr.RawText = res.RawText
	}
	report := render.FullReport([]render.RepoReview{rr})

	if flagJSON {
		if res.Review == nil {
			return fmt.Errorf("no structured review: %s", firstNonEmpty(res.ErrMsg, res.RawText, res.Stderr, "claude returned no structured output"))
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(res.Review); err != nil {
			return err
		}
	} else {
		fmt.Print(report)
	}
	// --out always gets the markdown report, --json included.
	if flagOut != "" {
		if err := os.WriteFile(flagOut, []byte(report), 0o644); err != nil {
			return fmt.Errorf("writing report: %w", err)
		}
		fmt.Fprintf(os.Stderr, "wrote %s\n", flagOut)
	}
	// Reflect a failed review in the exit code — in JSON mode too, since an
	// is_error result can still carry partial structured output — so CI can tell
	// a transient/auth/overload failure apart from a clean pass.
	if res.IsError {
		return fmt.Errorf("review failed: %s", res.ErrMsg)
	}
	return nil
}

func resolveBranchRef(ctx context.Context, repo *gitx.Repo, branch string) (string, error) {
	// Prefer the remote branch over a same-named local ref.
	candidates := []string{repo.Remote + "/" + branch, branch}
	for _, c := range candidates {
		if gitx.RefExists(ctx, repo.Path, c) {
			return c, nil
		}
	}
	return "", fmt.Errorf("branch %q not found on %s", branch, repo.Remote)
}

func resolveBase(ctx context.Context, cfg *config.Config, repo *gitx.Repo) (string, error) {
	if flagBase != "" {
		return flagBase, nil
	}
	if b, ok := cfg.RepoBase(repo.Path); ok {
		return b, nil
	}
	def, err := gitx.DefaultBranch(ctx, repo.Path, repo.Remote)
	if err != nil {
		return "", fmt.Errorf("could not determine base branch; pass --base")
	}
	return def, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// checkCmd is the headless health check: audit the codebase at a ref through a
// chosen lens and print the report (the TUI offers the same via the first
// screen's "Check the codebase").
func checkCmd() *cobra.Command {
	var (
		catFlag    string
		scopeFlag  string
		branchFlag string
		modelFlag  string
		jsonFlag   bool
		outFlag    string
		freshFlag  bool
		noFetch    bool
		listFlag   bool
	)
	c := &cobra.Command{
		Use:   "check",
		Short: "Headless codebase health check (security, tech debt, performance, …) with paste-ready fix prompts",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if listFlag {
				for _, cat := range checks.Categories {
					fmt.Printf("%-14s %s\n", cat.Key, cat.Desc)
					for _, s := range cat.Scopes {
						fmt.Printf("    --scope %-12s %s\n", s.Key, s.Desc)
					}
				}
				return nil
			}
			if catFlag == "" {
				return fmt.Errorf("--category is required (run `silly-review check --list` to see them)")
			}
			cat, ok := checks.CategoryByKey(catFlag)
			if !ok {
				return fmt.Errorf("unknown category %q — run `silly-review check --list`", catFlag)
			}
			scope := checks.ScopeByKey(cat, scopeFlag)
			if scopeFlag != "" && scope.Key != scopeFlag {
				return fmt.Errorf("category %s has no scope %q — run `silly-review check --list`", cat.Key, scopeFlag)
			}
			return runHeadlessCheck(cmd.Context(), cat, scope, branchFlag, modelFlag, jsonFlag, outFlag, freshFlag, noFetch)
		},
	}
	c.Flags().StringVar(&catFlag, "category", "", "audit lens (security|debt|performance|tests|resilience|deps|observability)")
	c.Flags().StringVar(&scopeFlag, "scope", "", "narrower scope within the category (default: general)")
	c.Flags().StringVar(&branchFlag, "branch", "", "branch to audit (default: the currently checked-out branch)")
	c.Flags().StringVar(&modelFlag, "model", "", "model alias (opus|sonnet|haiku|fable); overrides remembered choice")
	c.Flags().BoolVar(&jsonFlag, "json", false, "print the structured report as JSON")
	c.Flags().StringVar(&outFlag, "out", "", "also write the markdown report to this file")
	c.Flags().BoolVar(&freshFlag, "fresh", false, "ignore any saved prior check and audit from scratch")
	c.Flags().BoolVar(&noFetch, "no-fetch", false, "do not fetch from the remote first")
	c.Flags().BoolVar(&listFlag, "list", false, "list categories and scopes, then exit")
	return c
}

func runHeadlessCheck(ctx context.Context, cat checks.Category, scope checks.Scope, branch, modelFlag string, jsonOut bool, outFile string, fresh, noFetch bool) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	disc, err := discover.Discover(ctx, cwd)
	if err != nil {
		return err
	}
	if disc.Mode == discover.Multi {
		return fmt.Errorf("cd into the repo you want to check (found %d repos here)", len(disc.Repos))
	}
	if len(disc.Repos) == 0 {
		return fmt.Errorf("no git repository found here")
	}
	repo := disc.Repos[0]
	bin := claudeBin()
	if err := review.Preflight(ctx, bin); err != nil {
		return err
	}

	if !noFetch && repo.Remote != "" {
		_ = gitx.Fetch(ctx, repo.Path, repo.Remote)
	}

	// Resolve the ref to audit: explicit branch (local first — a check audits
	// what's on this machine), else the checked-out branch, else detached HEAD.
	ref := ""
	switch {
	case branch != "":
		cands := []string{branch}
		if repo.Remote != "" {
			cands = append(cands, repo.Remote+"/"+branch)
		}
		for _, c := range cands {
			if gitx.RefExists(ctx, repo.Path, c) {
				ref = c
				break
			}
		}
		if ref == "" {
			return fmt.Errorf("branch %q not found (locally or on the remote)", branch)
		}
	default:
		if cur := gitx.CurrentBranch(ctx, repo.Path); cur != "" {
			ref = cur
		} else {
			ref = "HEAD" // detached — audit whatever is checked out
		}
	}

	ws, err := gitx.NewWorkspace()
	if err != nil {
		return err
	}
	defer ws.Cleanup()
	go func() {
		<-ctx.Done()
		ws.Cleanup()
	}()

	wt, err := ws.Add(ctx, repo, ref)
	if err != nil {
		return fmt.Errorf("creating worktree: %w", err)
	}

	var prior *checks.Report
	if !fresh {
		if e, ok := history.LoadCheck(repo.Path, ref, cat.Key, scope.Key); ok {
			prior = &e.Report
			fmt.Fprintln(os.Stderr, "continuing from the previous check (pass --fresh to start over)")
		}
	}

	model := firstNonEmpty(modelFlag, cfg.Folder(repo.Path).Model, config.DefaultModel)
	fmt.Fprintf(os.Stderr, "checking %s at %s — %s (%s), %s…\n", repo.Name, ref, cat.Name, scope.Name, model)
	prog := newProgress(os.Stderr)
	prog.start()
	res, err := review.RunWithResume(ctx, review.Options{
		Model:           model,
		System:          checks.SystemPrompt(cat, scope),
		Prompt:          checks.BuildPrompt(checks.Context{RepoName: repo.Name, WorktreePath: wt.Path, Ref: ref}, cat, scope, prior),
		Schema:          checks.SchemaJSON,
		PrimaryWorktree: wt.Path,
		BinPath:         bin,
	}, prog.event)
	prog.stop()
	if err != nil {
		return err
	}

	cr := render.CheckResult{Repo: repo.Name, Ref: ref, Category: cat.Name, Scope: scope.Name}
	var report *checks.Report
	if len(res.Structured) > 0 {
		var rep checks.Report
		if json.Unmarshal(res.Structured, &rep) == nil {
			report = &rep
		}
	}
	// Only save a clean check — an error result can still carry partial
	// structured output, and saving it would clobber a good prior.
	if !res.IsError && report != nil {
		_ = history.SaveCheck(repo.Path, ref, cat.Key, scope.Key, history.CheckEntry{
			Repo: repo.Name, Ref: ref, Category: cat.Key, Scope: scope.Key, When: time.Now(), Report: *report,
		})
	}

	if res.IsError {
		cr.Err = res.ErrMsg
	} else {
		cr.Report = report
		cr.RawText = res.RawText
	}
	out := render.CheckReportMarkdown(cr)

	if jsonOut {
		if report == nil {
			return fmt.Errorf("no structured report: %s", firstNonEmpty(res.ErrMsg, res.RawText, res.Stderr, "claude returned no structured output"))
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			return err
		}
	} else {
		fmt.Print(out)
	}
	// --out always gets the markdown report, --json included (JSON to stdout +
	// file, as the README promises).
	if outFile != "" {
		if err := os.WriteFile(outFile, []byte(out), 0o644); err != nil {
			return fmt.Errorf("writing report: %w", err)
		}
		fmt.Fprintf(os.Stderr, "wrote %s\n", outFile)
	}
	// A failed run must exit non-zero even in JSON mode — an is_error result can
	// still carry partial structured output, and CI must not mistake it for a pass.
	if res.IsError {
		return fmt.Errorf("check failed: %s", res.ErrMsg)
	}
	return nil
}

// setup script URLs, reused by `silly-review update`.
const (
	setupURL    = "https://raw.githubusercontent.com/dearlordlt/silly-review/main/setup.sh"
	setupPS1URL = "https://raw.githubusercontent.com/dearlordlt/silly-review/main/setup.ps1"
)

// updateCmd re-runs the installer, targeting the directory of the currently
// running binary so it updates this copy in place.
func updateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "Update silly-review in place to the latest version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			if resolved, e := filepath.EvalSymlinks(exe); e == nil {
				exe = resolved
			}
			dir := filepath.Dir(exe)
			ctx := cmd.Context()
			fmt.Fprintf(os.Stderr, "updating %s (currently %s)…\n", exe, buildVersion())

			if runtime.GOOS == "windows" {
				// Download to a temp file (Stop on failure) then run it; INSTALL_DIR
				// is passed via env so the running binary's dir is updated in place.
				ps := "$ErrorActionPreference='Stop'; $f=Join-Path $env:TEMP ('sr-setup-'+[guid]::NewGuid().ToString('N')+'.ps1'); " +
					"Invoke-WebRequest -UseBasicParsing '" + setupPS1URL + "' -OutFile $f; & $f; Remove-Item $f -Force -ErrorAction SilentlyContinue"
				run := exec.CommandContext(ctx, "powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", ps)
				run.Env = append(os.Environ(), "INSTALL_DIR="+dir)
				run.Stdout, run.Stderr = os.Stdout, os.Stderr
				return run.Run()
			}

			// Download the installer to a temp file and run it only if the
			// download succeeded. A `curl | sh` pipe hides a download failure —
			// sh exits 0 on empty stdin — so update could no-op yet report success.
			script, err := os.CreateTemp("", "silly-review-setup-*.sh")
			if err != nil {
				return err
			}
			defer os.Remove(script.Name())
			script.Close()

			var dl *exec.Cmd
			switch {
			case haveCmd("curl"):
				dl = exec.CommandContext(ctx, "curl", "-fsSL", "-o", script.Name(), setupURL)
			case haveCmd("wget"):
				dl = exec.CommandContext(ctx, "wget", "-qO", script.Name(), setupURL)
			default:
				return fmt.Errorf("need curl or wget to self-update; re-run the install command from the README")
			}
			dl.Stderr = os.Stderr
			if err := dl.Run(); err != nil {
				return fmt.Errorf("downloading installer from %s: %w", setupURL, err)
			}
			if fi, err := os.Stat(script.Name()); err != nil || fi.Size() == 0 {
				return fmt.Errorf("downloaded installer from %s was empty", setupURL)
			}

			run := exec.CommandContext(ctx, "sh", script.Name())
			run.Env = append(os.Environ(), "INSTALL_DIR="+dir)
			run.Stdout, run.Stderr = os.Stdout, os.Stderr
			return run.Run()
		},
	}
}

func haveCmd(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func configCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "config",
		Short: "Show or change silly-review's saved configuration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			data, _ := json.MarshalIndent(cfg, "", "  ")
			fmt.Println(strings.TrimSpace(string(data)))
			return nil
		},
	}
	c.AddCommand(configBaseCmd())
	return c
}

// configBaseCmd shows or sets the base branch the current repo is diffed
// against — the non-TUI equivalent of pressing `c` on the branch screen.
func configBaseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "base [ref]",
		Short: "Show or set the base branch this repo is reviewed against (e.g. origin/main)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			top, err := gitx.Toplevel(ctx, cwd)
			if err != nil {
				return fmt.Errorf("not inside a git repository")
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if len(args) == 0 {
				if b, ok := cfg.RepoBase(top); ok {
					fmt.Println(b)
				} else {
					fmt.Println("(no base set — defaults to origin's default branch)")
				}
				return nil
			}
			cfg.SetRepoBase(top, args[0])
			if err := cfg.Save(); err != nil {
				return err
			}
			fmt.Printf("base for %s set to %s\n", top, args[0])
			return nil
		},
	}
}
