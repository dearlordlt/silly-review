package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"silly-review/internal/gitx"
	"silly-review/internal/history"
	"silly-review/internal/review"
)

func TestContinueFlow(t *testing.T) {
	m := testModel(t) // sets XDG_CONFIG_HOME to a temp dir
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.picks = []*repoPick{{repo: m.repos[0], base: "origin/main", branch: gitx.Branch{Name: "x", Ref: "origin/x"}}}

	// No prior review yet → straight to style, no continue screen.
	if m.anyPriorReview() {
		t.Fatal("no prior expected before any review")
	}
	m.toStyleOrError()
	if m.screen != scStyle {
		t.Fatalf("expected scStyle with no prior, got %d", m.screen)
	}

	// Save a prior review for this repo+branch.
	if err := history.Save(m.repos[0].Path, "origin/x", history.Entry{
		Repo: m.repos[0].Name, Branch: "origin/x", Review: review.Review{Verdict: "approve", Findings: []review.Finding{{Title: "x"}}},
	}); err != nil {
		t.Fatal(err)
	}
	if !m.anyPriorReview() {
		t.Fatal("prior should be detected after save")
	}

	// Now the transition offers the continue screen.
	m.toStyleOrError()
	if m.screen != scContinue {
		t.Fatalf("expected scContinue when a prior exists, got %d", m.screen)
	}
	if s := m.viewContinue(); s == "" {
		t.Fatal("continue screen rendered empty")
	}

	// Enter on the default option = continue.
	m.keyContinue(enter())
	if !m.continueFromPrior || m.screen != scStyle {
		t.Fatalf("continue choice failed: continue=%v screen=%d", m.continueFromPrior, m.screen)
	}

	// 'f' = fresh.
	m.screen = scContinue
	m.keyContinue(runeKey("f"))
	if m.continueFromPrior || m.screen != scStyle {
		t.Fatalf("fresh choice failed: continue=%v screen=%d", m.continueFromPrior, m.screen)
	}
}
