package gitx

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fixture builds a "remote" bare repo and a clone with a main branch and a
// feature branch, then returns the clone's path with origin tracking refs set.
func fixture(t *testing.T) (ctx context.Context, repo *Repo) {
	t.Helper()
	ctx = context.Background()
	root := t.TempDir()
	bare := filepath.Join(root, "origin.git")
	work := filepath.Join(root, "work")

	mustGit(t, root, "init", "--bare", "-b", "main", bare)
	mustGit(t, root, "clone", "-q", bare, work)
	gitEnv(t, work)

	writeFile(t, work, "README.md", "# project\n")
	mustGit(t, work, "add", ".")
	mustGit(t, work, "commit", "-qm", "initial")
	mustGit(t, work, "push", "-q", "origin", "main")

	mustGit(t, work, "checkout", "-q", "-b", "feature")
	writeFile(t, work, "app.go", "package app\n\nfunc Add(a, b int) int { return a + b }\n")
	mustGit(t, work, "add", ".")
	mustGit(t, work, "commit", "-qm", "add app")
	mustGit(t, work, "push", "-q", "origin", "feature")

	// Park the clone on main so feature is reviewed without a checkout.
	mustGit(t, work, "checkout", "-q", "main")
	mustGit(t, work, "fetch", "-q", "origin")

	return ctx, &Repo{Name: "work", Path: work, Remote: "origin"}
}

func TestRemoteBranchesFiltersHead(t *testing.T) {
	ctx, repo := fixture(t)
	branches, err := RemoteBranches(ctx, repo.Path, repo.Remote)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, b := range branches {
		if b.Name == "HEAD" || b.Ref == "origin" {
			t.Fatalf("HEAD pointer leaked into branch list: %+v", b)
		}
		names[b.Name] = true
	}
	if !names["main"] || !names["feature"] {
		t.Fatalf("expected main and feature, got %v", names)
	}
}

func TestDefaultBranchAndDiff(t *testing.T) {
	ctx, repo := fixture(t)
	def, err := DefaultBranch(ctx, repo.Path, repo.Remote)
	if err != nil {
		t.Fatal(err)
	}
	if def != "origin/main" {
		t.Fatalf("default branch = %q, want origin/main", def)
	}
	mb, _ := MergeBase(ctx, repo.Path, "origin/main", "origin/feature")
	if mb == "" {
		t.Fatal("expected a merge base")
	}
	stat, err := DiffStat(ctx, repo.Path, "origin/main", "origin/feature", mb)
	if err != nil {
		t.Fatal(err)
	}
	if stat.Files != 1 || stat.Additions == 0 {
		t.Fatalf("unexpected stat %+v", stat)
	}
}

// TestDiffUnrelatedHistories: an orphan branch (no merge base) must still
// produce a real diff via the two-dot fallback rather than erroring.
func TestDiffUnrelatedHistories(t *testing.T) {
	ctx, repo := fixture(t)
	work := repo.Path
	mustGit(t, work, "checkout", "-q", "--orphan", "ortho")
	mustGit(t, work, "rm", "-rfq", "--cached", ".")
	writeFile(t, work, "ortho.txt", "orphan content\n")
	mustGit(t, work, "add", "ortho.txt")
	mustGit(t, work, "commit", "-qm", "orphan root")
	mustGit(t, work, "push", "-q", "origin", "ortho") // updates local refs/remotes/origin/ortho

	mb, _ := MergeBase(ctx, work, "origin/main", "origin/ortho")
	if mb != "" {
		t.Fatalf("expected no merge base for unrelated histories, got %q", mb)
	}
	stat, err := DiffStat(ctx, work, "origin/main", "origin/ortho", mb)
	if err != nil {
		t.Fatalf("two-dot fallback should not error on unrelated histories: %v", err)
	}
	if stat.Files == 0 {
		t.Fatal("expected a non-empty diff for an orphan branch")
	}
}

// TestLocalBranchesAndMerge covers reviewing unpushed local work: a local branch
// with no remote counterpart shows up (flagged Local), and merging puts it ahead
// of remotes while dropping local branches already on the remote.
func TestLocalBranchesAndMerge(t *testing.T) {
	ctx, repo := fixture(t)
	// On a fresh feature branch that is NOT pushed.
	mustGit(t, repo.Path, "checkout", "-q", "-b", "krea2")
	writeFile(t, repo.Path, "k.go", "package k\n")
	mustGit(t, repo.Path, "add", ".")
	mustGit(t, repo.Path, "commit", "-qm", "local work")

	local, err := LocalBranches(ctx, repo.Path)
	if err != nil {
		t.Fatal(err)
	}
	var krea2 *Branch
	for i := range local {
		if local[i].Name == "krea2" {
			krea2 = &local[i]
		}
	}
	if krea2 == nil || !krea2.Local || krea2.Ref != "krea2" {
		t.Fatalf("expected local krea2 with Ref=krea2, got %+v", local)
	}

	remote, _ := RemoteBranches(ctx, repo.Path, repo.Remote) // main, feature
	merged := MergeBranchLists(local, remote)
	names := map[string]bool{}
	localKept := false
	for _, b := range merged {
		names[b.Name] = true
		if b.Name == "krea2" && b.Local {
			localKept = true
		}
		// local "main"/"feature" (already on the remote) must be dropped, leaving
		// only the remote copies — so no merged entry is a local main/feature.
		if (b.Name == "main" || b.Name == "feature") && b.Local {
			t.Fatalf("local %s should have been dropped (it's on the remote)", b.Name)
		}
	}
	if !localKept || !names["main"] || !names["feature"] {
		t.Fatalf("merged set wrong: %v", names)
	}
}

// TestCurrentBranchAndCheckLists covers the health-check target picker: the
// checked-out branch is detected and pinned first, locals shadow their
// same-name remotes, and remote-only branches still appear.
func TestCurrentBranchAndCheckLists(t *testing.T) {
	ctx, repo := fixture(t)
	mustGit(t, repo.Path, "checkout", "-q", "-b", "krea2")
	writeFile(t, repo.Path, "k.go", "package k\n")
	mustGit(t, repo.Path, "add", ".")
	mustGit(t, repo.Path, "commit", "-qm", "local work")

	if cur := CurrentBranch(ctx, repo.Path); cur != "krea2" {
		t.Fatalf("CurrentBranch = %q, want krea2", cur)
	}

	// Drop the local feature branch so origin/feature is remote-only.
	mustGit(t, repo.Path, "branch", "-q", "-D", "feature")

	local, _ := LocalBranches(ctx, repo.Path)                // main, krea2
	remote, _ := RemoteBranches(ctx, repo.Path, repo.Remote) // main, feature
	out := CheckBranchLists(local, remote, "krea2")

	if len(out) == 0 || out[0].Name != "krea2" || !out[0].Local {
		t.Fatalf("current branch must be pinned first, got %+v", out)
	}
	seen := map[string]int{}
	for _, b := range out {
		seen[b.Name]++
		if b.Name == "main" && !b.Local {
			t.Fatal("local main must shadow origin/main in check mode")
		}
		if b.Name == "feature" && b.Local {
			t.Fatal("feature only exists on the remote here")
		}
	}
	for name, n := range seen {
		if n != 1 {
			t.Fatalf("branch %s listed %d times", name, n)
		}
	}
	if seen["feature"] != 1 || seen["main"] != 1 {
		t.Fatalf("expected main (local) and feature (remote-only): %v", seen)
	}

	// Detached HEAD → no current branch.
	mustGit(t, repo.Path, "checkout", "-q", "--detach")
	if cur := CurrentBranch(ctx, repo.Path); cur != "" {
		t.Fatalf("detached HEAD should give empty current branch, got %q", cur)
	}
	mustGit(t, repo.Path, "checkout", "-q", "krea2")
}

// TestWorktreeOfCheckedOutLocalBranch: reviewing the branch you're currently on
// (unpushed) must work and leave your working tree untouched.
func TestWorktreeOfCheckedOutLocalBranch(t *testing.T) {
	ctx, repo := fixture(t)
	mustGit(t, repo.Path, "checkout", "-q", "-b", "krea2")
	writeFile(t, repo.Path, "k.go", "package k\n")
	mustGit(t, repo.Path, "add", ".")
	mustGit(t, repo.Path, "commit", "-qm", "local work")

	headBefore := gitOut(t, repo.Path, "rev-parse", "HEAD")
	branchBefore := gitOut(t, repo.Path, "rev-parse", "--abbrev-ref", "HEAD") // krea2

	ws, err := NewWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	wt, err := ws.Add(ctx, repo, "krea2") // bare local-branch ref, currently checked out
	if err != nil {
		t.Fatalf("worktree of checked-out local branch failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt.Path, "k.go")); err != nil {
		t.Fatalf("worktree missing local-branch content: %v", err)
	}
	ws.Cleanup()

	if got := gitOut(t, repo.Path, "rev-parse", "--abbrev-ref", "HEAD"); got != branchBefore {
		t.Fatalf("current branch changed: %s -> %s", branchBefore, got)
	}
	if got := gitOut(t, repo.Path, "rev-parse", "HEAD"); got != headBefore {
		t.Fatalf("HEAD changed: %s -> %s", headBefore, got)
	}
}

// TestWorktreeLeavesTreeUntouched is the central safety invariant: creating and
// removing a disposable worktree must not change the user's HEAD or status.
func TestWorktreeLeavesTreeUntouched(t *testing.T) {
	ctx, repo := fixture(t)

	headBefore := gitOut(t, repo.Path, "rev-parse", "HEAD")
	branchBefore := gitOut(t, repo.Path, "rev-parse", "--abbrev-ref", "HEAD")
	statusBefore := gitOut(t, repo.Path, "status", "--porcelain")

	ws, err := NewWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	wt, err := ws.Add(ctx, repo, "origin/feature")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(wt.Path, "app.go")); err != nil {
		t.Fatalf("worktree missing branch content: %v", err)
	}
	ws.Cleanup()

	if got := gitOut(t, repo.Path, "rev-parse", "HEAD"); got != headBefore {
		t.Fatalf("HEAD changed: %s -> %s", headBefore, got)
	}
	if got := gitOut(t, repo.Path, "rev-parse", "--abbrev-ref", "HEAD"); got != branchBefore {
		t.Fatalf("branch changed: %s -> %s", branchBefore, got)
	}
	if got := gitOut(t, repo.Path, "status", "--porcelain"); got != statusBefore {
		t.Fatalf("status changed: %q -> %q", statusBefore, got)
	}
	if list := gitOut(t, repo.Path, "worktree", "list"); strings.Contains(list, wt.Path) {
		t.Fatalf("worktree not cleaned up: %s", list)
	}
}

// ---- helpers ----

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := run(context.Background(), dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return out
}

func gitEnv(t *testing.T, dir string) {
	t.Helper()
	mustGit(t, dir, "config", "user.email", "test@example.com")
	mustGit(t, dir, "config", "user.name", "Test")
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
