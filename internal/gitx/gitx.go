// Package gitx wraps the read-only git plumbing silly-review relies on.
//
// Every command here is read-only with respect to the user's working tree: we
// never check out branches, stash, commit, or otherwise mutate the repo the
// user is sitting in. Branch contents are materialized through disposable
// detached worktrees in a temp dir (see Workspace), which share the object
// store but leave the user's HEAD and index untouched.
package gitx

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

// Repo identifies a single git repository.
type Repo struct {
	Name   string // base name of the top-level dir
	Path   string // absolute top-level path
	Remote string // remote to review against, normally "origin"
}

// Branch is a branch with the metadata shown in the picker.
type Branch struct {
	Name     string    // short name without remote prefix, e.g. "feat/login"
	Ref      string    // ref to review: "origin/feat/login" (remote) or "feat/login" (local)
	SHA      string    // abbreviated commit hash
	Date     time.Time // committer date
	DateRel  string    // git's relative date, e.g. "3 hours ago"
	Author   string
	Subject  string
	Local    bool // a local branch, not a remote one
	Unpushed bool // a local branch with no same-name remote counterpart
}

// FileChange is one entry from `git diff --name-status`.
type FileChange struct {
	Status string // A, M, D, R...
	Path   string
}

// Stat summarizes a diff range.
type Stat struct {
	Files     int
	Additions int
	Deletions int
}

// run executes `git -C dir args...` and returns trimmed stdout.
func run(ctx context.Context, dir string, args ...string) (string, error) {
	full := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return strings.TrimSpace(out.String()), nil
}

// IsRepo reports whether dir is inside a git working tree.
func IsRepo(ctx context.Context, dir string) bool {
	out, err := run(ctx, dir, "rev-parse", "--is-inside-work-tree")
	return err == nil && out == "true"
}

// Toplevel returns the absolute top-level path of the repo containing dir.
func Toplevel(ctx context.Context, dir string) (string, error) {
	return run(ctx, dir, "rev-parse", "--show-toplevel")
}

// Remotes lists configured remotes.
func Remotes(ctx context.Context, repoPath string) ([]string, error) {
	out, err := run(ctx, repoPath, "remote")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// PickRemote returns "origin" if present, otherwise the first remote, or an error.
func PickRemote(ctx context.Context, repoPath string) (string, error) {
	rs, err := Remotes(ctx, repoPath)
	if err != nil {
		return "", err
	}
	for _, r := range rs {
		if r == "origin" {
			return "origin", nil
		}
	}
	if len(rs) > 0 {
		return rs[0], nil
	}
	return "", fmt.Errorf("no remotes configured")
}

// Fetch updates remote-tracking refs only. This touches neither the working
// tree nor any local branch, so it's safe under our no-mutation contract.
func Fetch(ctx context.Context, repoPath, remote string) error {
	_, err := run(ctx, repoPath, "fetch", "--prune", "--quiet", remote)
	return err
}

// RemoteBranches lists branches under refs/remotes/<remote>, newest commit first.
func RemoteBranches(ctx context.Context, repoPath, remote string) ([]Branch, error) {
	const fmtStr = "%(refname:short)%09%(objectname:short)%09%(committerdate:iso8601-strict)%09%(committerdate:relative)%09%(authorname)%09%(contents:subject)"
	out, err := run(ctx, repoPath, "for-each-ref", "--sort=-committerdate",
		"--format="+fmtStr, "refs/remotes/"+remote)
	if err != nil {
		return nil, err
	}
	var branches []Branch
	headRef := remote + "/HEAD"
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 6)
		if len(parts) < 6 {
			continue
		}
		short := parts[0]
		// Filter the HEAD pointer: it appears both as "<remote>/HEAD" and as a
		// bare "<remote>" entry.
		if short == headRef || short == remote {
			continue
		}
		t, _ := time.Parse(time.RFC3339, parts[2])
		branches = append(branches, Branch{
			Name:    strings.TrimPrefix(short, remote+"/"),
			Ref:     short,
			SHA:     parts[1],
			Date:    t,
			DateRel: parts[3],
			Author:  parts[4],
			Subject: parts[5],
		})
	}
	return branches, nil
}

// LocalBranches lists local branches (refs/heads), newest commit first, with
// Local=true and Ref set to the bare branch name. Callers filter out ones that
// are already on the remote (see MergeBranchLists).
func LocalBranches(ctx context.Context, repoPath string) ([]Branch, error) {
	const fmtStr = "%(refname:short)%09%(objectname:short)%09%(committerdate:iso8601-strict)%09%(committerdate:relative)%09%(authorname)%09%(contents:subject)"
	out, err := run(ctx, repoPath, "for-each-ref", "--sort=-committerdate", "--format="+fmtStr, "refs/heads")
	if err != nil {
		return nil, err
	}
	var branches []Branch
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 6)
		if len(parts) < 6 {
			continue
		}
		t, _ := time.Parse(time.RFC3339, parts[2])
		branches = append(branches, Branch{
			Name:    parts[0],
			Ref:     parts[0], // local branches are reviewed by bare name
			SHA:     parts[1],
			Date:    t,
			DateRel: parts[3],
			Author:  parts[4],
			Subject: parts[5],
			Local:   true,
		})
	}
	return branches, nil
}

// MergeBranchLists returns the unpushed local branches (those without a
// same-named remote branch) followed by the remote branches, newest commit
// first — so reviewing your own work before pushing is a first-class option.
func MergeBranchLists(local, remote []Branch) []Branch {
	rset := make(map[string]bool, len(remote))
	for _, b := range remote {
		rset[b.Name] = true
	}
	out := make([]Branch, 0, len(local)+len(remote))
	for _, b := range local {
		if !rset[b.Name] { // skip local branches already represented on the remote
			b.Unpushed = true
			out = append(out, b)
		}
	}
	out = append(out, remote...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Date.After(out[j].Date) })
	return out
}

// CurrentBranch returns the short name of the branch checked out in repoPath,
// or "" for a detached HEAD.
func CurrentBranch(ctx context.Context, repoPath string) string {
	out, err := run(ctx, repoPath, "symbolic-ref", "--short", "-q", "HEAD")
	if err != nil {
		return ""
	}
	return out
}

// CheckBranchLists orders branches for a health check, which audits code as it
// sits on the machine: local branches win over their same-name remote (the
// local copy is what the user means, unpushed commits included), everything is
// newest-commit first, and the currently checked-out branch is pinned to the
// top as the default target.
func CheckBranchLists(local, remote []Branch, current string) []Branch {
	lset := make(map[string]bool, len(local))
	for _, b := range local {
		lset[b.Name] = true
	}
	rset := make(map[string]bool, len(remote))
	for _, b := range remote {
		rset[b.Name] = true
	}
	out := make([]Branch, 0, len(local)+len(remote))
	for _, b := range local {
		b.Unpushed = !rset[b.Name]
		out = append(out, b)
	}
	for _, b := range remote {
		if !lset[b.Name] {
			out = append(out, b)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Date.After(out[j].Date) })
	for i, b := range out {
		if b.Local && b.Name == current {
			cur := out[i]
			copy(out[1:i+1], out[:i])
			out[0] = cur
			break
		}
	}
	return out
}

// DefaultBranch returns the remote's default branch ref (e.g. "origin/main").
// Falls back to probing common names if the symbolic ref isn't set.
func DefaultBranch(ctx context.Context, repoPath, remote string) (string, error) {
	if out, err := run(ctx, repoPath, "symbolic-ref", "--short", "refs/remotes/"+remote+"/HEAD"); err == nil && out != "" {
		return out, nil
	}
	for _, name := range []string{"main", "master", "develop", "dev"} {
		ref := remote + "/" + name
		if _, err := run(ctx, repoPath, "rev-parse", "--verify", "--quiet", ref); err == nil {
			return ref, nil
		}
	}
	return "", fmt.Errorf("could not determine default branch for %s", remote)
}

// RefExists reports whether a ref resolves.
func RefExists(ctx context.Context, repoPath, ref string) bool {
	_, err := run(ctx, repoPath, "rev-parse", "--verify", "--quiet", ref)
	return err == nil
}

// MergeBase returns the merge base of two refs, or "" if histories are unrelated.
func MergeBase(ctx context.Context, repoPath, a, b string) (string, error) {
	out, err := run(ctx, repoPath, "merge-base", a, b)
	if err != nil {
		return "", nil // unrelated histories: caller falls back to two-dot
	}
	return out, nil
}

// diffRange picks the diff range: three-dot (PR/merge-base semantics) when a
// merge base exists, two-dot for unrelated histories (mergeBase == ""), where
// three-dot would abort with "no merge base".
func diffRange(base, head, mergeBase string) string {
	if mergeBase == "" {
		return base + ".." + head
	}
	return base + "..." + head
}

// DiffStat returns the summary for the base→head range. Pass the merge base
// (or "" if histories are unrelated) so the range matches what the prompt uses.
func DiffStat(ctx context.Context, repoPath, base, head, mergeBase string) (Stat, error) {
	out, err := run(ctx, repoPath, "diff", "--numstat", diffRange(base, head, mergeBase))
	if err != nil {
		return Stat{}, err
	}
	var s Stat
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		s.Files++
		// "-" marks binary files; skip their line counts.
		if fields[0] != "-" {
			s.Additions += atoi(fields[0])
		}
		if fields[1] != "-" {
			s.Deletions += atoi(fields[1])
		}
	}
	return s, nil
}

// DiffNameStatus returns the changed files for the base→head range, using the
// same range selection as DiffStat.
func DiffNameStatus(ctx context.Context, repoPath, base, head, mergeBase string) ([]FileChange, error) {
	out, err := run(ctx, repoPath, "diff", "--name-status", diffRange(base, head, mergeBase))
	if err != nil {
		return nil, err
	}
	var changes []FileChange
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 2)
		if len(fields) < 2 {
			continue
		}
		changes = append(changes, FileChange{Status: fields[0], Path: fields[1]})
	}
	return changes, nil
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return n
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// ---- Disposable worktrees ----

// Worktree is a detached checkout of a ref in a temp dir.
type Worktree struct {
	Path string
	Repo *Repo
	Ref  string
}

// Workspace owns all worktrees created during a run and guarantees cleanup.
type Workspace struct {
	root  string
	mu    sync.Mutex
	trees []*Worktree
}

// NewWorkspace creates a temp root to hold worktrees.
func NewWorkspace() (*Workspace, error) {
	root, err := os.MkdirTemp("", "silly-review-")
	if err != nil {
		return nil, err
	}
	return &Workspace{root: root}, nil
}

// Add creates a detached worktree of ref for repo. The checkout lives outside
// the user's repo so it never appears as untracked there.
func (ws *Workspace) Add(ctx context.Context, repo *Repo, ref string) (*Worktree, error) {
	name := sanitize(repo.Name) + "-" + sanitize(ref)
	dir, err := os.MkdirTemp(ws.root, name+"-")
	if err != nil {
		return nil, err
	}
	// os.MkdirTemp creates the dir, but `git worktree add` wants to create it.
	_ = os.Remove(dir)
	if _, err := run(ctx, repo.Path, "worktree", "add", "--detach", "--quiet", dir, ref); err != nil {
		// A stale worktree table can block adds; prune once and retry.
		_, _ = run(ctx, repo.Path, "worktree", "prune")
		if _, err2 := run(ctx, repo.Path, "worktree", "add", "--detach", "--quiet", dir, ref); err2 != nil {
			return nil, err
		}
	}
	wt := &Worktree{Path: dir, Repo: repo, Ref: ref}
	ws.mu.Lock()
	ws.trees = append(ws.trees, wt)
	ws.mu.Unlock()
	return wt, nil
}

// Cleanup removes every worktree created in this workspace and the temp root.
// It is idempotent and safe to call from a signal handler or defer.
func (ws *Workspace) Cleanup() {
	ws.mu.Lock()
	trees := ws.trees
	ws.trees = nil
	ws.mu.Unlock()

	pruned := map[string]bool{}
	ctx := context.Background()
	for _, wt := range trees {
		_, _ = run(ctx, wt.Repo.Path, "worktree", "remove", "--force", wt.Path)
		if !pruned[wt.Repo.Path] {
			_, _ = run(ctx, wt.Repo.Path, "worktree", "prune")
			pruned[wt.Repo.Path] = true
		}
		_ = os.RemoveAll(wt.Path)
	}
	if ws.root != "" {
		_ = os.RemoveAll(ws.root)
	}
}

func sanitize(s string) string {
	r := strings.NewReplacer("/", "-", " ", "-", ":", "-", "\\", "-")
	return strings.Trim(r.Replace(s), "-")
}
