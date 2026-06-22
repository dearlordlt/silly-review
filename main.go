// Command silly-review is a TUI that produces senior-engineer code reviews of
// remote branches by driving the `claude` CLI, without ever touching the user's
// working tree.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"silly-review/internal/config"
	"silly-review/internal/discover"
	"silly-review/internal/gitx"
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
)

func main() {
	root := &cobra.Command{
		Use:           "silly-review",
		Short:         "Senior-engineer code reviews of remote branches, powered by Claude — read-only, never touches your working tree.",
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

	root.AddCommand(configCmd())
	root.AddCommand(updateCmd())

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

	fmt.Fprintf(os.Stderr, "reviewing %s vs %s (%s, %s)…\n", head, base, model, style.Key)
	prog := newProgress(os.Stderr)
	prog.start()
	res, err := review.RunWithResume(ctx, review.Options{
		Model:           model,
		System:          review.SystemPrompt(style),
		Prompt:          review.BuildPrompt(rc, nil),
		PrimaryWorktree: wt.Path,
		BinPath:         bin,
	}, prog.event)
	prog.stop()
	if err != nil {
		return err
	}

	if flagJSON {
		if res.Review == nil {
			return fmt.Errorf("no structured review: %s", firstNonEmpty(res.ErrMsg, res.RawText, res.Stderr, "claude returned no structured output"))
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res.Review)
	}

	rr := render.RepoReview{Repo: repo.Name, Branch: head, Base: base}
	if res.IsError {
		rr.Err = res.ErrMsg
	} else {
		rr.Review = res.Review
		rr.RawText = res.RawText
	}
	report := render.FullReport([]render.RepoReview{rr})
	fmt.Print(report)
	if flagOut != "" {
		if err := os.WriteFile(flagOut, []byte(report), 0o644); err != nil {
			return fmt.Errorf("writing report: %w", err)
		}
		fmt.Fprintf(os.Stderr, "wrote %s\n", flagOut)
	}
	// Reflect a failed review in the exit code so CI/scripting can tell a
	// transient/auth/overload failure apart from a clean pass (the report still
	// printed above for the human).
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

// setupURL is the canonical installer, reused by `silly-review update`.
const setupURL = "https://raw.githubusercontent.com/dearlordlt/silly-review/main/setup.sh"

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

			fmt.Fprintf(os.Stderr, "updating %s (currently %s)…\n", exe, buildVersion())
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
