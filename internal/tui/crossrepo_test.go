package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"silly-review/internal/gitx"
)

func enter() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyEnter} }
func runeKey(r string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(r)}
}

// pickAnchor sets up two repos with the given backend branches, picks the
// frontend's "feat/x" as the anchor, and returns the backend pick — leaving the
// model on the cross-repo match screen for backend.
func twoRepoAtMatch(t *testing.T, backendBranches []gitx.Branch) (*Model, *repoPick) {
	t.Helper()
	m := testModel(t) // multi: repos[0]=frontend, repos[1]=backend
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	fe := &repoPick{repo: m.repos[0], base: "origin/main", defBranch: "origin/main",
		branches: []gitx.Branch{{Name: "feat/x", Ref: "origin/feat/x", Author: "ada", Subject: "x"}}}
	be := &repoPick{repo: m.repos[1], defBranch: "origin/main", branches: backendBranches}
	m.picks = []*repoPick{fe, be}

	m.cur = 0
	m.loadPickIntoView(fe)
	m.brCur = 0 // feat/x
	m.keyBranchSelect(enter())

	if m.anchorBranchName != "feat/x" {
		t.Fatalf("anchor not recorded, got %q", m.anchorBranchName)
	}
	if m.screen != scMatch {
		t.Fatalf("expected scMatch for backend, got screen %d", m.screen)
	}
	return m, be
}

func TestCrossRepoMatchYes(t *testing.T) {
	m, be := twoRepoAtMatch(t, []gitx.Branch{
		{Name: "feat/x", Ref: "origin/feat/x", Author: "ada", Subject: "x"},
		{Name: "main", Ref: "origin/main"},
	})
	if m.matched == nil || m.matched.Name != "feat/x" {
		t.Fatalf("expected a feat/x match, got %+v", m.matched)
	}
	m.keyMatch(runeKey("y"))
	if be.dropped || !be.decided || be.branch.Name != "feat/x" {
		t.Fatalf("backend should be auto-selected to feat/x: %+v", be)
	}
	if be.base != "origin/main" {
		t.Fatalf("backend base should resolve to its default, got %q", be.base)
	}
	if m.screen != scStyle {
		t.Fatalf("expected scStyle once all repos decided, got %d", m.screen)
	}
}

func TestCrossRepoNoMatchSkip(t *testing.T) {
	m, be := twoRepoAtMatch(t, []gitx.Branch{{Name: "main", Ref: "origin/main"}})
	if m.matched != nil {
		t.Fatalf("expected no match, got %+v", m.matched)
	}
	// Case C default cursor is Skip; enter skips.
	m.keyMatch(enter())
	if !be.dropped {
		t.Fatalf("backend should be dropped when skipped")
	}
	if m.screen != scStyle {
		t.Fatalf("expected scStyle (review the anchor alone), got %d", m.screen)
	}
}

// TestCrossRepoYesBranchEqualsBase: pressing Yes on a matched branch that equals
// the repo's base must route to manual *and keep* the explanatory warning.
func TestCrossRepoYesBranchEqualsBase(t *testing.T) {
	m, be := twoRepoAtMatch(t, []gitx.Branch{
		{Name: "feat/x", Ref: "origin/main", Author: "ada", Subject: "x"}, // Ref == backend's default base
		{Name: "main", Ref: "origin/main"},
	})
	m.keyMatch(runeKey("y"))
	if m.screen != scBranchSelect {
		t.Fatalf("expected routing to manual branch select, got screen %d", m.screen)
	}
	if !strings.Contains(m.statusMsg, "nothing to diff") {
		t.Fatalf("base-collision warning should survive, got %q", m.statusMsg)
	}
	if be.decided || be.dropped {
		t.Fatalf("backend should await a manual pick, got %+v", be)
	}
}

func TestViewMatchRendersAllCases(t *testing.T) {
	m := testModel(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.picks = []*repoPick{{repo: m.repos[0], base: "origin/main"}, {repo: m.repos[1], base: "origin/main"}}
	m.cur = 1
	m.screen = scMatch
	m.anchorBranchName = "feat/x"
	m.anchorAuthor = "ada"

	m.matched = &gitx.Branch{Name: "feat/x", Ref: "origin/feat/x", Author: "ada", DateRel: "2h ago", Subject: "x"}
	if a := m.viewMatch(); !strings.Contains(a, "also has") || !strings.Contains(a, "[y] Yes") {
		t.Errorf("case A render wrong:\n%s", a)
	}
	m.matched = &gitx.Branch{Name: "feat/x", Ref: "origin/feat/x", Author: "bob", DateRel: "2h ago", Subject: "x"}
	if b := m.viewMatch(); !strings.Contains(b, "different author") {
		t.Errorf("case B render wrong:\n%s", b)
	}
	m.matched = nil
	if c := m.viewMatch(); !strings.Contains(c, "no 'feat/x' branch") || strings.Contains(c, "[y] Yes") {
		t.Errorf("case C render wrong:\n%s", c)
	}
}
