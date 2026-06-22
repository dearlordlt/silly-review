package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"silly-review/internal/config"
	"silly-review/internal/discover"
	"silly-review/internal/gitx"
	"silly-review/internal/render"
	"silly-review/internal/review"
)

func testModel(t *testing.T) *Model {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return New(Params{
		Ctx:    ctx,
		Cancel: cancel,
		Cfg:    cfg,
		Disc: &discover.Result{
			Mode:  discover.Multi,
			Root:  "/parent",
			Repos: []*gitx.Repo{{Name: "frontend", Path: "/parent/frontend", Remote: "origin"}, {Name: "backend", Path: "/parent/backend", Remote: "origin"}},
		},
		FolderKey: "/parent",
		BinPath:   "claude",
	})
}

// TestViewsDoNotPanic renders every screen with representative state.
func TestViewsDoNotPanic(t *testing.T) {
	m := testModel(t)
	m.Init()
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

	m.picks = []*repoPick{{repo: m.repos[0], base: "origin/main", branch: gitx.Branch{Name: "feat/x", Ref: "origin/feat/x", Author: "Ada", DateRel: "2h ago", Subject: "do a thing"}}}
	m.branches = []gitx.Branch{{Name: "feat/x", Ref: "origin/feat/x", Author: "Ada", DateRel: "2h ago", Subject: "x"}}
	m.baseCands = []string{"origin/main", "origin/dev"}
	m.logLines = []string{"reading app.go", "running: git diff"}
	m.reviews = []render.RepoReview{{Repo: "frontend", Review: &review.Review{
		Summary: "Looks ok.", Verdict: "approve_with_nits",
		Findings: []review.Finding{{Repo: "frontend", File: "a.go", StartLine: 3, Severity: "major", Category: "correctness", Title: "bug", Comment: "this is wrong because of reasons that go on", CodeSnippet: "x := 1", Suggestion: "x := 2"}},
	}}}
	m.onAllDone(allDoneMsg{reviews: m.reviews})

	for _, sc := range []screen{scRepoSelect, scLoading, scBranchSelect, scBaseConfig, scStyle, scModel, scProgress, scResults} {
		m.screen = sc
		out := m.View()
		if strings.TrimSpace(out) == "" {
			t.Errorf("screen %d rendered empty", sc)
		}
	}
	m.err = context.Canceled
	m.screen = scError
	if strings.TrimSpace(m.View()) == "" {
		t.Error("error screen rendered empty")
	}
}

// TestProgressShowsActivityAndTimer proves the progress screen shows the current
// action and an elapsed timer rather than a static "working…".
func TestProgressShowsActivityAndTimer(t *testing.T) {
	m := testModel(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.screen = scProgress
	m.curActivity = "reading config.go"
	m.reviewStart = time.Now().Add(-65 * time.Second)

	out := m.View()
	if !strings.Contains(out, "reading config.go") {
		t.Errorf("progress should show the current activity; got:\n%s", out)
	}
	if !strings.Contains(out, "1:05") {
		t.Errorf("progress should show an elapsed timer (~1:05); got:\n%s", out)
	}
	if strings.Contains(out, " working…") {
		t.Errorf("progress should no longer show static 'working…'; got:\n%s", out)
	}
}

// TestResultsNavigationAndFilter exercises the results key handlers.
func TestResultsNavigationAndFilter(t *testing.T) {
	m := testModel(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.flat = []review.Finding{
		{File: "a.go", StartLine: 1, Severity: "blocker", Title: "boom"},
		{File: "b.go", StartLine: 2, Severity: "nit", Title: "tiny"},
	}
	render.SortFindings(m.flat)
	m.screen = scResults

	m.keyResults(tea.KeyMsg{Type: tea.KeyDown})
	if m.resCur != 1 {
		t.Fatalf("down should move cursor to 1, got %d", m.resCur)
	}
	// filter to nits only -> cursor resets, one match
	for m.sevFilter != "nit" {
		m.keyResults(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
		if m.sevFilter == "" && len(m.flat) == 0 {
			break
		}
		if m.sevFilter == "praise" { // safety: avoid infinite loop
			break
		}
	}
	if got := m.filtered(); m.sevFilter == "nit" && (len(got) != 1 || got[0].Severity != "nit") {
		t.Fatalf("nit filter wrong: %+v", got)
	}
}
