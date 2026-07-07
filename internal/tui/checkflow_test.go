package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"silly-review/internal/checks"
	"silly-review/internal/gitx"
	"silly-review/internal/history"
	"silly-review/internal/render"
)

// TestCheckFlow drives the health-check path: mode → repo (single-pick) →
// branch → category → scope → model → results, without invoking git or claude.
func TestCheckFlow(t *testing.T) {
	m := testModel(t)
	m.Init()
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

	if m.screen != scMode {
		t.Fatalf("startup should show the mode screen, got %d", m.screen)
	}
	if s := m.viewMode(); !strings.Contains(s, "Check the codebase") {
		t.Fatalf("mode screen missing the check option:\n%s", s)
	}

	// 'c' enters check mode; multi-repo root → single-pick repo list.
	m.keyMode(runeKey("c"))
	if m.mode != modeCheck || m.screen != scRepoSelect {
		t.Fatalf("check mode should go to repo select, got mode=%d screen=%d", m.mode, m.screen)
	}
	if s := m.viewRepoSelect(); !strings.Contains(s, "pick the repo to check") {
		t.Fatalf("check repo select should be single-choice:\n%s", s)
	}

	// esc on the repo picker returns to the mode screen.
	m.keyRepoSelect(tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen != scMode {
		t.Fatalf("esc from repo select should return to the mode screen, got %d", m.screen)
	}
	m.keyMode(runeKey("c"))

	// Simulate the load completing (skip loadRepoCmd: it shells out to git).
	m.picks = []*repoPick{{repo: m.repos[0]}}
	m.cur = 0
	branches := []gitx.Branch{
		{Name: "krea2", Ref: "krea2", Local: true, Unpushed: true, Author: "Me", DateRel: "1h ago", Subject: "wip"},
		{Name: "main", Ref: "main", Local: true, Author: "Me", DateRel: "2d ago", Subject: "release"},
	}
	m.onRepoLoaded(repoLoadedMsg{idx: 0, branches: branches, current: "krea2"})
	if m.screen != scBranchSelect {
		t.Fatalf("check load should go straight to branch select (no base config), got %d", m.screen)
	}
	if s := m.viewBranchSelect(); !strings.Contains(s, "(current)") {
		t.Fatalf("current branch should be tagged:\n%s", s)
	}
	// A pushed local branch (main) must NOT be tagged unpushed in check mode.
	if s := m.viewBranchSelect(); strings.Contains(s, "unpushed") {
		t.Fatalf("pushed local branch mislabeled as unpushed:\n%s", s)
	}

	// esc on the branch picker goes back to the repo picker (multi-repo root).
	m.keyBranchSelect(tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen != scRepoSelect {
		t.Fatalf("esc from branch select should return to repo select, got %d", m.screen)
	}
	m.screen = scBranchSelect

	// Enter on the current branch → category picker (no prior → no continue).
	m.keyBranchSelect(enter())
	if m.screen != scCategory {
		t.Fatalf("expected category screen, got %d", m.screen)
	}
	if s := m.viewCategory(); !strings.Contains(s, "Security") || !strings.Contains(s, "Tech debt") {
		t.Fatalf("category screen missing presets:\n%s", s)
	}

	// Down to Tech debt, enter → scope picker.
	m.keyCategory(runeKey("j"))
	m.keyCategory(enter())
	if m.screen != scScope || checks.Categories[m.catCur].Key != "debt" {
		t.Fatalf("expected debt scopes, got screen=%d cat=%s", m.screen, checks.Categories[m.catCur].Key)
	}
	if s := m.viewScope(); !strings.Contains(s, "General") {
		t.Fatalf("scope screen missing general:\n%s", s)
	}

	// Enter on General → model picker (no prior check saved).
	m.keyScope(enter())
	if m.screen != scModel {
		t.Fatalf("expected model screen, got %d", m.screen)
	}

	// esc from model goes back to scope in check mode.
	m.keyModel(tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen != scScope {
		t.Fatalf("esc should return to scope, got %d", m.screen)
	}

	// A saved prior check for this exact repo+ref+category+scope → continue screen.
	if err := history.SaveCheck(m.repos[0].Path, "krea2", "debt", "general", history.CheckEntry{
		Repo: "frontend", Ref: "krea2", Category: "debt", Scope: "general",
		Report: checks.Report{Health: "good", Summary: "fine", Findings: []checks.Finding{{Title: "x"}}},
	}); err != nil {
		t.Fatal(err)
	}
	m.keyScope(enter())
	if m.screen != scContinue {
		t.Fatalf("expected continue screen with a prior check, got %d", m.screen)
	}
	if s := m.viewContinue(); !strings.Contains(s, "previous check") {
		t.Fatalf("continue screen should speak about checks:\n%s", s)
	}
	// esc goes back to the scope picker instead of quitting the app.
	m.keyContinue(tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen != scScope {
		t.Fatalf("esc from continue should return to scope, got %d", m.screen)
	}
	m.keyScope(enter())
	m.keyContinue(runeKey("c"))
	if !m.continueFromPrior || m.screen != scModel {
		t.Fatalf("continue choice failed: continue=%v screen=%d", m.continueFromPrior, m.screen)
	}

	// Results: a done message renders the check results view with fix-prompt help.
	rep := &checks.Report{
		Health:  "needs_attention",
		Summary: "One hole.",
		Findings: []checks.Finding{
			{File: "a.go", StartLine: 3, Severity: "high", Title: "hole", Problem: "p", Impact: "i", Solution: "s", FixPrompt: "fix", Effort: "quick"},
			{File: "b.go", StartLine: 1, Severity: "low", Title: "meh", Problem: "p", Impact: "i", Solution: "s", FixPrompt: "fix2"},
		},
	}
	m.onCheckDone(checkDoneMsg{res: render.CheckResult{Repo: "frontend", Ref: "krea2", Category: "Tech debt", Scope: "General", Report: rep}, cost: 1.5})
	if m.screen != scResults || len(m.flatCheck) != 2 {
		t.Fatalf("check results not loaded: screen=%d findings=%d", m.screen, len(m.flatCheck))
	}
	out := m.View()
	for _, want := range []string{"HIGH", "hole", "copy fix prompt", "needs attention"} {
		if !strings.Contains(out, want) {
			t.Errorf("check results missing %q:\n%s", want, out)
		}
	}

	// Severity filter cycles the check severities, not the review ones.
	m.keyCheckResults(runeKey("f"))
	if m.sevFilter != "critical" {
		t.Fatalf("first filter should be critical, got %q", m.sevFilter)
	}
	m.sevFilter = "high"
	if fs := m.filteredChecks(); len(fs) != 1 || fs[0].Title != "hole" {
		t.Fatalf("high filter wrong: %+v", fs)
	}
}

// TestCheckEmptyStates: a failed check explains itself; a clean one shows the
// health verdict and summary instead of a bare "no findings".
func TestCheckEmptyStates(t *testing.T) {
	m := testModel(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.mode = modeCheck
	m.screen = scResults

	m.checkRes = render.CheckResult{Repo: "backend", Ref: "main", Category: "Security", Scope: "General", Err: "claude failed: 401 authentication"}
	out := m.View()
	if !strings.Contains(out, "check failed") || !strings.Contains(out, "sign in") {
		t.Fatalf("failed check should show the error and auth hints:\n%s", out)
	}

	m.checkRes = render.CheckResult{Repo: "backend", Ref: "main", Category: "Security", Scope: "General",
		Report: &checks.Report{Health: "good", Summary: "Auth is enforced everywhere; nothing exploitable found."}}
	out = m.View()
	for _, want := range []string{"health: good", "nothing exploitable"} {
		if !strings.Contains(out, want) {
			t.Errorf("clean check missing %q:\n%s", want, out)
		}
	}
}

// TestReviewFlowUnaffected: choosing review on the mode screen preserves the
// old multi-repo behavior (checkbox select with remembered picks).
func TestReviewFlowUnaffected(t *testing.T) {
	m := testModel(t)
	m.Init()
	if m.screen != scMode {
		t.Fatalf("expected mode screen first, got %d", m.screen)
	}
	m.keyMode(enter()) // default option = review
	if m.mode != modeReview || m.screen != scRepoSelect {
		t.Fatalf("enter should pick review mode, got mode=%d screen=%d", m.mode, m.screen)
	}
	if s := m.viewRepoSelect(); !strings.Contains(s, "space check/uncheck") {
		t.Fatalf("review repo select should be multi-choice:\n%s", s)
	}
}
