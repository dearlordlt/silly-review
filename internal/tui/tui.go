// Package tui implements silly-review's interactive terminal UI: repo select →
// branch select → base config → style/model → live progress → results.
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"silly-review/internal/checks"
	"silly-review/internal/config"
	"silly-review/internal/discover"
	"silly-review/internal/gitx"
	"silly-review/internal/history"
	"silly-review/internal/render"
	"silly-review/internal/review"
)

type screen int

const (
	scMode screen = iota
	scRepoSelect
	scLoading
	scBranchSelect
	scBaseConfig
	scMatch
	scContinue
	scStyle
	scCategory
	scScope
	scModel
	scProgress
	scResults
	scError
)

// appMode is what the user chose on the first screen: a PR-style branch review
// or a whole-codebase health check.
type appMode int

const (
	modeReview appMode = iota
	modeCheck
)

// repoPick is the accumulating choice for one repo.
type repoPick struct {
	repo   *gitx.Repo
	base   string // resolved base ref, e.g. "origin/main"
	branch gitx.Branch

	// Eager-load cache (multi-repo mode loads every selected repo up front so the
	// cross-repo match step can compare branch names without extra git calls).
	branches  []gitx.Branch
	baseCands []string
	defBranch string
	loadErr   error

	decided bool // a branch was chosen, or the repo was skipped
	dropped bool // excluded from the review (skipped, or failed to load)
}

type modelOpt struct{ key, desc string }

var modelChoices = []modelOpt{
	{"opus", "Most capable — best for thorough, nuanced work"},
	{"sonnet", "Fast and strong — a great default"},
	{"haiku", "Fastest, lightest — quick passes"},
	{"fable", "Latest Fable model"},
}

// Params configures a TUI run.
type Params struct {
	Ctx       context.Context
	Cancel    context.CancelFunc
	Cfg       *config.Config
	Workspace *gitx.Workspace
	Disc      *discover.Result
	FolderKey string
	Fetch     bool
	BinPath   string
	Version   string
}

// appVersion is shown in the TUI header so the running build is always visible
// (set by New). One program runs at a time, so a package var is fine.
var appVersion string

// Model is the root Bubble Tea model.
type Model struct {
	ctx       context.Context
	cancel    context.CancelFunc
	cfg       *config.Config
	ws        *gitx.Workspace
	disc      *discover.Result
	folderKey string
	fetch     bool
	binPath   string

	width, height int
	screen        screen
	err           error

	repos   []*gitx.Repo
	repoSel map[int]bool
	repoCur int

	picks []*repoPick
	cur   int

	branches   []gitx.Branch
	brCur      int
	loadingMsg string

	baseCands   []string
	baseCur     int
	baseReturn  screen
	baseAdvance bool // when set, base-config enter advances the cross-repo flow

	// cross-repo matching
	loadRemain       int          // eager-load fan-in counter
	anchorBranchName string       // name of the first actively-picked branch ("" = none yet)
	anchorAuthor     string       // its author (used only to word the match prompt)
	matched          *gitx.Branch // matched branch in the current repo, or nil (no match)
	matchCur         int          // cursor over the match-screen options

	// continue-from-last-review
	continueFromPrior bool
	continueCur       int

	// mode + health-check selection
	mode          appMode
	modeCur       int
	catCur        int
	scopeCur      int
	currentBranch string // checked-out branch of the repo being checked ("" = detached)

	styleCur int
	modelCur int

	spin        spinner.Model
	logLines    []string
	curActivity string
	reviewStart time.Time
	costUSD     float64

	reviews   []render.RepoReview
	flat      []review.Finding
	checkRes  render.CheckResult
	flatCheck []checks.Finding
	resCur    int
	sevFilter string
	statusMsg string

	events chan tea.Msg
}

// ---- messages ----

type repoLoadedMsg struct {
	idx           int
	branches      []gitx.Branch
	defaultBranch string
	candidates    []string
	current       string // checked-out branch (check mode)
	err           error
}

type logMsg struct{ repo, text string }
type retryMsg struct{ text string }
type thinkMsg struct{ text string }
type allDoneMsg struct {
	reviews []render.RepoReview
	cost    float64
}
type checkDoneMsg struct {
	res  render.CheckResult
	cost float64
}
type reviewErrMsg struct{ err error }

// New builds the root model.
func New(p Params) *Model {
	appVersion = p.Version
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("63"))
	return &Model{
		ctx:       p.Ctx,
		cancel:    p.Cancel,
		cfg:       p.Cfg,
		ws:        p.Workspace,
		disc:      p.Disc,
		folderKey: p.FolderKey,
		fetch:     p.Fetch,
		binPath:   p.BinPath,
		repos:     p.Disc.Repos,
		repoSel:   map[int]bool{},
		spin:      s,
	}
}

// Run launches the program.
func Run(p Params) error {
	m := New(p)
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

func (m *Model) Init() tea.Cmd {
	m.screen = scMode
	return nil
}

// enterMode routes past the mode screen once a mode is chosen.
func (m *Model) enterMode(mode appMode) (tea.Model, tea.Cmd) {
	m.mode = mode
	if m.disc.Mode == discover.Multi {
		m.screen = scRepoSelect
		if mode == modeReview {
			fc := m.cfg.Folder(m.folderKey)
			last := map[string]bool{}
			for _, n := range fc.LastRepos {
				last[n] = true
			}
			for i, r := range m.repos {
				if last[r.Name] {
					m.repoSel[i] = true
				}
			}
		}
		return m, nil
	}
	m.picks = []*repoPick{{repo: m.repos[0]}}
	m.cur = 0
	return m, m.startLoadingRepo()
}

// keyMode handles the first screen: review a branch, or check the codebase.
func (m *Model) keyMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc":
		return m, tea.Quit
	case "up", "k":
		m.modeCur = 0
	case "down", "j":
		m.modeCur = 1
	case "r":
		return m.enterMode(modeReview)
	case "c":
		return m.enterMode(modeCheck)
	case "enter":
		if m.modeCur == 1 {
			return m.enterMode(modeCheck)
		}
		return m.enterMode(modeReview)
	}
	return m, nil
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			m.cancel()
			return m, tea.Quit
		}
		return m.handleKey(msg)
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	case repoLoadedMsg:
		return m.onRepoLoaded(msg)
	case logMsg:
		m.appendLog(msg.repo, msg.text)
		return m, m.waitForEvent()
	case retryMsg:
		m.appendLog("", msg.text)
		return m, m.waitForEvent()
	case thinkMsg:
		// Status-line only — don't pollute the activity log with token spam.
		m.curActivity = msg.text
		return m, m.waitForEvent()
	case allDoneMsg:
		return m.onAllDone(msg)
	case checkDoneMsg:
		return m.onCheckDone(msg)
	case reviewErrMsg:
		m.err = msg.err
		m.screen = scError
		return m, nil
	}
	return m, nil
}

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.screen {
	case scMode:
		return m.keyMode(msg)
	case scRepoSelect:
		return m.keyRepoSelect(msg)
	case scBranchSelect:
		return m.keyBranchSelect(msg)
	case scBaseConfig:
		return m.keyBaseConfig(msg)
	case scMatch:
		return m.keyMatch(msg)
	case scContinue:
		return m.keyContinue(msg)
	case scStyle:
		return m.keyStyle(msg)
	case scCategory:
		return m.keyCategory(msg)
	case scScope:
		return m.keyScope(msg)
	case scModel:
		return m.keyModel(msg)
	case scProgress:
		if k := msg.String(); k == "esc" || k == "q" {
			m.cancel()
			return m, tea.Quit
		}
		return m, nil
	case scResults:
		if m.mode == modeCheck {
			return m.keyCheckResults(msg)
		}
		return m.keyResults(msg)
	case scError:
		return m, tea.Quit
	}
	return m, nil
}

// ---- repo select ----

func (m *Model) keyRepoSelect(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Check mode audits exactly one repo: enter picks the highlighted row.
	if m.mode == modeCheck {
		switch msg.String() {
		case "q":
			return m, tea.Quit
		case "esc":
			m.screen = scMode
		case "up", "k":
			if m.repoCur > 0 {
				m.repoCur--
			}
		case "down", "j":
			if m.repoCur < len(m.repos)-1 {
				m.repoCur++
			}
		case "enter", " ":
			m.picks = []*repoPick{{repo: m.repos[m.repoCur]}}
			m.cur = 0
			return m, m.startLoadingRepo()
		}
		return m, nil
	}
	switch msg.String() {
	case "q":
		return m, tea.Quit
	case "esc":
		m.screen = scMode
	case "up", "k":
		if m.repoCur > 0 {
			m.repoCur--
		}
	case "down", "j":
		if m.repoCur < len(m.repos)-1 {
			m.repoCur++
		}
	case " ":
		m.repoSel[m.repoCur] = !m.repoSel[m.repoCur]
	case "a":
		all := len(m.selectedRepoIdx()) == len(m.repos)
		for i := range m.repos {
			m.repoSel[i] = !all
		}
	case "enter":
		sel := m.selectedRepoIdx()
		if len(sel) == 0 {
			m.statusMsg = "Check at least one repo first — press space to toggle the highlighted row."
			return m, nil
		}
		m.statusMsg = ""
		m.picks = nil
		var names []string
		for _, i := range sel {
			m.picks = append(m.picks, &repoPick{repo: m.repos[i]})
			names = append(names, m.repos[i].Name)
		}
		// Remember the full selection (before any later skip) for next time.
		fc := m.cfg.Folder(m.folderKey)
		fc.LastRepos = names
		m.cfg.SetFolder(m.folderKey, fc)
		_ = m.cfg.Save()
		return m, m.startEagerLoad()
	}
	return m, nil
}

func (m *Model) selectedRepoIdx() []int {
	var out []int
	for i := range m.repos {
		if m.repoSel[i] {
			out = append(out, i)
		}
	}
	return out
}

// ---- loading / repo loaded ----

func (m *Model) startLoadingRepo() tea.Cmd {
	m.screen = scLoading
	r := m.picks[m.cur].repo
	m.loadingMsg = fmt.Sprintf("Loading branches for %s…", r.Name)
	return tea.Batch(loadRepoCmd(m.ctx, r, m.fetch, m.cur, m.mode == modeCheck), m.spin.Tick)
}

// startEagerLoad (multi-repo) fetches every selected repo's branches up front so
// the cross-repo match step can compare names without stalling.
func (m *Model) startEagerLoad() tea.Cmd {
	m.screen = scLoading
	m.loadRemain = len(m.picks)
	m.loadingMsg = fmt.Sprintf("Loaded 0 of %d repos…", len(m.picks))
	cmds := make([]tea.Cmd, 0, len(m.picks)+1)
	for i, p := range m.picks {
		cmds = append(cmds, loadRepoCmd(m.ctx, p.repo, m.fetch, i, false))
	}
	cmds = append(cmds, m.spin.Tick)
	return tea.Batch(cmds...)
}

func loadRepoCmd(ctx context.Context, repo *gitx.Repo, fetch bool, idx int, forCheck bool) tea.Cmd {
	return func() tea.Msg {
		// A health check has no base/diff, so it works even without a remote:
		// local branches (current first) win over their same-name remote copy.
		if forCheck {
			var remoteB []gitx.Branch
			if repo.Remote != "" {
				if fetch {
					_ = gitx.Fetch(ctx, repo.Path, repo.Remote)
				}
				remoteB, _ = gitx.RemoteBranches(ctx, repo.Path, repo.Remote) // best-effort
			}
			localB, _ := gitx.LocalBranches(ctx, repo.Path)
			cur := gitx.CurrentBranch(ctx, repo.Path)
			branches := gitx.CheckBranchLists(localB, remoteB, cur)
			// Detached HEAD has no branch name but is still what's checked out —
			// offer it as the default target instead of dropping it silently.
			if cur == "" && gitx.RefExists(ctx, repo.Path, "HEAD") {
				cur = "HEAD"
				branches = append([]gitx.Branch{{Name: "HEAD", Ref: "HEAD", Local: true, Subject: "detached — audit exactly what's checked out"}}, branches...)
			}
			if len(branches) == 0 {
				return repoLoadedMsg{idx: idx, err: fmt.Errorf("%s has no branches to check (no commits yet?)", repo.Name)}
			}
			return repoLoadedMsg{idx: idx, branches: branches, current: cur}
		}

		if repo.Remote == "" {
			return repoLoadedMsg{idx: idx, err: fmt.Errorf("%s has no remote (origin) to review", repo.Name)}
		}
		if fetch {
			_ = gitx.Fetch(ctx, repo.Path, repo.Remote)
		}
		remoteB, err := gitx.RemoteBranches(ctx, repo.Path, repo.Remote)
		if err != nil {
			return repoLoadedMsg{idx: idx, err: err}
		}
		localB, _ := gitx.LocalBranches(ctx, repo.Path) // best-effort: also offer unpushed local work
		branches := gitx.MergeBranchLists(localB, remoteB)
		if len(branches) == 0 {
			return repoLoadedMsg{idx: idx, err: fmt.Errorf("%s has no branches to review", repo.Name)}
		}
		def, _ := gitx.DefaultBranch(ctx, repo.Path, repo.Remote)
		// Base candidates stay remote-only — you diff against an integration branch.
		return repoLoadedMsg{idx: idx, branches: branches, defaultBranch: def, candidates: baseCandidates(def, remoteB)}
	}
}

func (m *Model) onRepoLoaded(msg repoLoadedMsg) (tea.Model, tea.Cmd) {
	// Check mode: always one repo, no base to configure — straight to the target picker.
	if m.mode == modeCheck {
		if msg.err != nil {
			m.err = msg.err
			m.screen = scError
			return m, nil
		}
		p := m.picks[m.cur]
		p.branches = msg.branches
		m.currentBranch = msg.current
		m.loadPickIntoView(p)
		m.screen = scBranchSelect
		return m, nil
	}

	// Single-repo (lazy) mode: one repo, hard-fail on error — nothing to fall back to.
	if m.disc.Mode != discover.Multi {
		if msg.err != nil {
			m.err = msg.err
			m.screen = scError
			return m, nil
		}
		p := m.picks[m.cur]
		p.branches, p.baseCands, p.defBranch = msg.branches, msg.candidates, msg.defaultBranch
		m.loadPickIntoView(p)
		if base, ok := m.cfg.RepoBase(p.repo.Path); ok {
			p.base = base
			m.screen = scBranchSelect
			return m, nil
		}
		m.baseReturn = scBranchSelect
		m.baseAdvance = false
		m.screen = scBaseConfig
		return m, nil
	}

	// Multi-repo (eager) mode: fan-in. A repo that fails to load is auto-dropped
	// rather than aborting the whole run; only all-failed ends in an error.
	p := m.picks[msg.idx]
	if msg.err != nil {
		p.loadErr = msg.err
		p.dropped = true
		p.decided = true
	} else {
		p.branches, p.baseCands, p.defBranch = msg.branches, msg.candidates, msg.defaultBranch
	}
	if m.loadRemain--; m.loadRemain > 0 {
		m.loadingMsg = fmt.Sprintf("Loaded %d of %d repos…", len(m.picks)-m.loadRemain, len(m.picks))
		return m, nil
	}
	return m.beginFirstRepo()
}

// loadPickIntoView copies a pick's cached branch/base data into the live
// selection state used by the branch/base screens.
func (m *Model) loadPickIntoView(p *repoPick) {
	m.branches = p.branches
	m.brCur = 0
	m.baseCands = p.baseCands
	m.baseCur = 0
}

// beginFirstRepo runs once all repos are loaded: it drives the first surviving
// repo to its base/branch picker (that repo becomes the anchor when its branch
// is chosen).
func (m *Model) beginFirstRepo() (tea.Model, tea.Cmd) {
	for i, p := range m.picks {
		if p.dropped {
			continue
		}
		m.cur = i
		m.loadPickIntoView(p)
		if base, ok := m.cfg.RepoBase(p.repo.Path); ok {
			p.base = base
			m.screen = scBranchSelect
		} else {
			m.baseReturn = scBranchSelect
			m.baseAdvance = false
			m.screen = scBaseConfig
		}
		return m, nil
	}
	m.err = fmt.Errorf("every selected repo failed to load branches — nothing to review")
	m.screen = scError
	return m, nil
}

func baseCandidates(def string, branches []gitx.Branch) []string {
	var out []string
	seen := map[string]bool{}
	if def != "" {
		out = append(out, def)
		seen[def] = true
	}
	for _, b := range branches {
		if !seen[b.Ref] {
			out = append(out, b.Ref)
			seen[b.Ref] = true
		}
	}
	return out
}

// ---- branch select ----

func (m *Model) keyBranchSelect(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q":
		return m, tea.Quit
	case "esc":
		// Check mode has a clean back path; the review flow keeps esc = quit
		// (backing out mid multi-repo matching is ambiguous).
		if m.mode == modeCheck {
			if m.disc.Mode == discover.Multi {
				m.screen = scRepoSelect
			} else {
				m.screen = scMode
			}
			return m, nil
		}
		return m, tea.Quit
	case "up", "k":
		if m.brCur > 0 {
			m.brCur--
		}
		m.statusMsg = ""
	case "down", "j":
		if m.brCur < len(m.branches)-1 {
			m.brCur++
		}
		m.statusMsg = ""
	case "c":
		if m.mode == modeCheck {
			return m, nil // no base branch in check mode
		}
		m.statusMsg = ""
		m.baseReturn = scBranchSelect
		m.baseAdvance = false
		m.baseCur = 0
		m.screen = scBaseConfig
	case "enter":
		sel := m.branches[m.brCur]
		if m.mode == modeCheck {
			m.statusMsg = ""
			p := m.picks[m.cur]
			p.branch = sel
			p.decided = true
			return m.gotoCategory()
		}
		// Reviewing the base against itself yields an empty diff — catch it here
		// rather than spinning up a worktree and a no-op review.
		if sel.Ref == m.picks[m.cur].base {
			m.statusMsg = fmt.Sprintf("%s is the base branch itself — there'd be nothing to diff. Pick the branch you want reviewed, or press c to change the base.", sel.Name)
			return m, nil
		}
		m.statusMsg = ""
		p := m.picks[m.cur]
		p.branch = sel
		p.decided = true
		// The first branch the user actively picks is the anchor; its name is
		// matched against the other repos.
		if m.anchorBranchName == "" {
			m.anchorBranchName = sel.Name
			m.anchorAuthor = sel.Author
		}
		return m.advanceToNextUndecided()
	}
	return m, nil
}

// ---- health-check category & scope ----

func (m *Model) gotoCategory() (tea.Model, tea.Cmd) {
	m.screen = scCategory
	m.catCur = indexOfCategory(m.cfg.Folder(m.folderKey).CheckCategory)
	return m, nil
}

func (m *Model) keyCategory(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q":
		return m, tea.Quit
	case "esc":
		m.screen = scBranchSelect
	case "up", "k":
		if m.catCur > 0 {
			m.catCur--
		}
	case "down", "j":
		if m.catCur < len(checks.Categories)-1 {
			m.catCur++
		}
	case "enter":
		cat := checks.Categories[m.catCur]
		m.scopeCur = 0
		if fc := m.cfg.Folder(m.folderKey); fc.CheckCategory == cat.Key {
			m.scopeCur = indexOfScope(cat, fc.CheckScope)
		}
		m.screen = scScope
	}
	return m, nil
}

func (m *Model) keyScope(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	scopes := checks.Categories[m.catCur].Scopes
	switch msg.String() {
	case "q":
		return m, tea.Quit
	case "esc":
		m.screen = scCategory
	case "up", "k":
		if m.scopeCur > 0 {
			m.scopeCur--
		}
	case "down", "j":
		if m.scopeCur < len(scopes)-1 {
			m.scopeCur++
		}
	case "enter":
		return m.toCheckContinueOrModel()
	}
	return m, nil
}

// toCheckContinueOrModel offers to continue from a saved prior check for this
// exact repo+ref+category+scope, else goes straight to the model picker.
func (m *Model) toCheckContinueOrModel() (tea.Model, tea.Cmd) {
	p := m.picks[m.cur]
	cat := checks.Categories[m.catCur]
	scope := cat.Scopes[m.scopeCur]
	m.continueFromPrior = false
	if history.HasCheck(p.repo.Path, p.branch.Ref, cat.Key, scope.Key) {
		m.continueCur = 0 // default: continue from last
		m.screen = scContinue
		return m, nil
	}
	return m.gotoModel()
}

// ---- base config ----

func (m *Model) keyBaseConfig(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		// Cancel: only allowed if a base is already set (re-entered via 'c').
		if m.picks[m.cur].base != "" {
			m.screen = m.baseReturn
		}
		return m, nil
	case "up", "k":
		if m.baseCur > 0 {
			m.baseCur--
		}
	case "down", "j":
		if m.baseCur < len(m.baseCands)-1 {
			m.baseCur++
		}
	case "enter":
		if len(m.baseCands) == 0 {
			return m, nil
		}
		base := m.baseCands[m.baseCur]
		m.cfg.SetRepoBase(m.picks[m.cur].repo.Path, base)
		_ = m.cfg.Save()
		m.picks[m.cur].base = base
		if m.baseAdvance {
			m.baseAdvance = false
			return m.advanceToNextUndecided()
		}
		m.screen = m.baseReturn
	}
	return m, nil
}

// ---- cross-repo matching ----

// advanceToNextUndecided drives the next undecided repo to its match screen, or
// moves on to the style screen when every repo has been decided.
func (m *Model) advanceToNextUndecided() (tea.Model, tea.Cmd) {
	for i, p := range m.picks {
		if p.decided || p.dropped {
			continue
		}
		return m.presentMatch(i)
	}
	return m.toStyleOrError()
}

// presentMatch computes whether repo i has a branch matching the anchor and
// shows the match screen.
func (m *Model) presentMatch(i int) (tea.Model, tea.Cmd) {
	m.cur = i
	p := m.picks[i]
	m.loadPickIntoView(p)
	m.matchCur = 0
	m.statusMsg = ""
	if b, ok := findBranchByName(p.branches, m.anchorBranchName); ok {
		m.matched = &b
	} else {
		m.matched = nil
	}
	m.screen = scMatch
	return m, nil
}

func findBranchByName(branches []gitx.Branch, name string) (gitx.Branch, bool) {
	for _, b := range branches {
		if b.Name == name {
			return b, true
		}
	}
	return gitx.Branch{}, false
}

// matchOptionCount is 3 when there's a match (Yes/Skip/Manual), else 2 (Skip/Manual).
func (m *Model) matchOptionCount() int {
	if m.matched != nil {
		return 3
	}
	return 2
}

func (m *Model) keyMatch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	n := m.matchOptionCount()
	switch msg.String() {
	case "q", "esc":
		return m, tea.Quit
	case "up", "k":
		if m.matchCur > 0 {
			m.matchCur--
		}
	case "down", "j":
		if m.matchCur < n-1 {
			m.matchCur++
		}
	case "y":
		if m.matched != nil {
			return m.matchYes()
		}
	case "s":
		return m.matchSkip()
	case "m":
		return m.matchManual()
	case "enter":
		if m.matched != nil {
			switch m.matchCur {
			case 0:
				return m.matchYes()
			case 1:
				return m.matchSkip()
			case 2:
				return m.matchManual()
			}
		}
		switch m.matchCur {
		case 0:
			return m.matchSkip()
		case 1:
			return m.matchManual()
		}
	}
	return m, nil
}

func (m *Model) matchYes() (tea.Model, tea.Cmd) {
	p := m.picks[m.cur]
	mb := *m.matched

	// Resolve a base: a remembered one, else the detected default (persist it so
	// matched repos don't re-prompt). Only fall through to the base picker if
	// neither exists.
	base, ok := m.cfg.RepoBase(p.repo.Path)
	if !ok && p.defBranch != "" {
		base = p.defBranch
		m.cfg.SetRepoBase(p.repo.Path, base)
		_ = m.cfg.Save()
		ok = true
	}
	if ok {
		p.base = base
		if mb.Ref == p.base {
			m.statusMsg = fmt.Sprintf("That branch is %s's base — nothing to diff. Pick another.", p.repo.Name)
			return m.matchManual()
		}
		p.branch = mb
		p.decided = true
		return m.advanceToNextUndecided()
	}

	// No base known: set the branch now, ask for a base, then continue.
	p.branch = mb
	p.decided = true
	m.baseAdvance = true
	m.baseCur = 0
	m.screen = scBaseConfig
	return m, nil
}

func (m *Model) matchSkip() (tea.Model, tea.Cmd) {
	p := m.picks[m.cur]
	p.dropped = true
	p.decided = true
	m.statusMsg = ""
	return m.advanceToNextUndecided()
}

func (m *Model) matchManual() (tea.Model, tea.Cmd) {
	p := m.picks[m.cur]
	m.loadPickIntoView(p)
	// Note: don't clear statusMsg here — presentMatch already cleared it on entry
	// to the match screen, and matchYes sets an explanatory warning before routing
	// here that must survive to the branch picker.
	if base, ok := m.cfg.RepoBase(p.repo.Path); ok {
		p.base = base
		m.screen = scBranchSelect
	} else {
		m.baseReturn = scBranchSelect
		m.baseAdvance = false
		m.screen = scBaseConfig
	}
	return m, nil
}

// toStyleOrError proceeds to the style screen (via the continue screen when a
// prior review exists), unless every repo was skipped.
func (m *Model) toStyleOrError() (tea.Model, tea.Cmd) {
	active := 0
	for _, p := range m.picks {
		if !p.dropped {
			active++
		}
	}
	if active == 0 {
		m.err = fmt.Errorf("every selected repo was skipped or had no reviewable branch — nothing to review")
		m.screen = scError
		return m, nil
	}
	if m.anyPriorReview() {
		m.continueCur = 0 // default: continue from last
		m.screen = scContinue
		return m, nil
	}
	return m.gotoStyle()
}

// anyPriorReview reports whether any non-dropped pick has a saved prior review.
func (m *Model) anyPriorReview() bool {
	for _, p := range m.picks {
		if !p.dropped && p.branch.Ref != "" && history.Has(p.repo.Path, p.branch.Ref) {
			return true
		}
	}
	return false
}

func (m *Model) gotoStyle() (tea.Model, tea.Cmd) {
	m.screen = scStyle
	m.styleCur = indexOfStyle(m.cfg.Folder(m.folderKey).Style)
	return m, nil
}

// keyContinue: choose between continuing from the last review/check or a fresh
// one. The next screen depends on the mode (review → style, check → model).
func (m *Model) keyContinue(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	next := m.gotoStyle
	if m.mode == modeCheck {
		next = m.gotoModel
	}
	switch msg.String() {
	case "q":
		return m, tea.Quit
	case "esc":
		if m.mode == modeCheck {
			m.screen = scScope
			return m, nil
		}
		return m, tea.Quit
	case "up", "k":
		m.continueCur = 0
	case "down", "j":
		m.continueCur = 1
	case "c":
		m.continueFromPrior = true
		return next()
	case "f":
		m.continueFromPrior = false
		return next()
	case "enter":
		m.continueFromPrior = m.continueCur == 0
		return next()
	}
	return m, nil
}

// ---- style ----

func (m *Model) keyStyle(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc":
		return m, tea.Quit
	case "up", "k":
		if m.styleCur > 0 {
			m.styleCur--
		}
	case "down", "j":
		if m.styleCur < len(review.Styles)-1 {
			m.styleCur++
		}
	case "enter":
		return m.gotoModel()
	}
	return m, nil
}

func (m *Model) gotoModel() (tea.Model, tea.Cmd) {
	m.screen = scModel
	m.modelCur = indexOfModel(m.cfg.Folder(m.folderKey).Model)
	return m, nil
}

// ---- model ----

func (m *Model) keyModel(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q":
		return m, tea.Quit
	case "esc":
		if m.mode == modeCheck {
			m.screen = scScope
		} else {
			m.screen = scStyle
		}
	case "up", "k":
		if m.modelCur > 0 {
			m.modelCur--
		}
	case "down", "j":
		if m.modelCur < len(modelChoices)-1 {
			m.modelCur++
		}
	case "enter":
		fc := m.cfg.Folder(m.folderKey)
		fc.Model = modelChoices[m.modelCur].key
		if m.mode == modeCheck {
			cat := checks.Categories[m.catCur]
			fc.CheckCategory = cat.Key
			fc.CheckScope = cat.Scopes[m.scopeCur].Key
		} else {
			fc.Style = review.Styles[m.styleCur].Key
		}
		m.cfg.SetFolder(m.folderKey, fc)
		_ = m.cfg.Save()
		if m.mode == modeCheck {
			return m, m.startCheck()
		}
		return m, m.startReview()
	}
	return m, nil
}

// ---- progress / review orchestration ----

func (m *Model) startReview() tea.Cmd {
	m.screen = scProgress
	m.logLines = nil
	m.curActivity = "starting…"
	m.reviewStart = time.Now()
	m.events = make(chan tea.Msg, 256)
	style := review.Styles[m.styleCur]
	model := modelChoices[m.modelCur].key
	// Only review repos that weren't skipped or failed to load — dropped repos
	// must not get a worktree nor leak into another repo's cross-repo context.
	var active []*repoPick
	for _, p := range m.picks {
		if !p.dropped {
			active = append(active, p)
		}
	}
	return tea.Batch(
		launchReview(m.ctx, m.ws, active, style, model, m.binPath, m.continueFromPrior, m.events),
		m.waitForEvent(),
		m.spin.Tick,
	)
}

func (m *Model) startCheck() tea.Cmd {
	m.screen = scProgress
	m.logLines = nil
	m.curActivity = "starting…"
	m.reviewStart = time.Now()
	m.events = make(chan tea.Msg, 256)
	cat := checks.Categories[m.catCur]
	scope := cat.Scopes[m.scopeCur]
	model := modelChoices[m.modelCur].key
	return tea.Batch(
		launchCheck(m.ctx, m.ws, m.picks[m.cur], cat, scope, model, m.binPath, m.continueFromPrior, m.events),
		m.waitForEvent(),
		m.spin.Tick,
	)
}

func (m *Model) waitForEvent() tea.Cmd {
	ch := m.events
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

func (m *Model) appendLog(repo, text string) {
	m.curActivity = text
	line := text
	if repo != "" {
		line = lipgloss.NewStyle().Foreground(lipgloss.Color("63")).Render(repo) + "  " + text
	}
	m.logLines = append(m.logLines, line)
	const maxLines = 200
	if len(m.logLines) > maxLines {
		m.logLines = m.logLines[len(m.logLines)-maxLines:]
	}
}

func (m *Model) onAllDone(msg allDoneMsg) (tea.Model, tea.Cmd) {
	m.reviews = msg.reviews
	m.costUSD = msg.cost
	m.flat = nil
	for _, rr := range m.reviews {
		if rr.Review == nil {
			continue
		}
		for _, f := range rr.Review.Findings {
			if f.Repo == "" {
				f.Repo = rr.Repo
			}
			m.flat = append(m.flat, f)
		}
	}
	render.SortFindings(m.flat)
	m.resCur = 0
	m.screen = scResults
	return m, nil
}

func (m *Model) onCheckDone(msg checkDoneMsg) (tea.Model, tea.Cmd) {
	m.checkRes = msg.res
	m.costUSD = msg.cost
	m.flatCheck = nil
	if msg.res.Report != nil {
		for _, f := range msg.res.Report.Findings {
			if f.Repo == "" {
				f.Repo = msg.res.Repo
			}
			m.flatCheck = append(m.flatCheck, f)
		}
	}
	render.SortCheckFindings(m.flatCheck)
	m.resCur = 0
	m.screen = scResults
	return m, nil
}

// ---- results ----

var sevFilters = []string{"", "blocker", "major", "minor", "nit", "question", "praise"}

func (m *Model) filtered() []review.Finding {
	if m.sevFilter == "" {
		return m.flat
	}
	var out []review.Finding
	for _, f := range m.flat {
		if f.Severity == m.sevFilter {
			out = append(out, f)
		}
	}
	return out
}

func (m *Model) keyResults(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	fs := m.filtered()
	switch msg.String() {
	case "q", "esc":
		return m, tea.Quit
	case "up", "k":
		if m.resCur > 0 {
			m.resCur--
		}
		m.statusMsg = ""
	case "down", "j":
		if m.resCur < len(fs)-1 {
			m.resCur++
		}
		m.statusMsg = ""
	case "f":
		// cycle filter
		cur := 0
		for i, s := range sevFilters {
			if s == m.sevFilter {
				cur = i
				break
			}
		}
		m.sevFilter = sevFilters[(cur+1)%len(sevFilters)]
		m.resCur = 0
	case "y":
		if len(fs) > 0 {
			if err := clipboard.WriteAll(render.CommentBlock(fs[m.resCur])); err != nil {
				m.statusMsg = "clipboard unavailable (install wl-clipboard or xclip)"
			} else {
				m.statusMsg = "✓ comment copied to clipboard"
			}
		}
	case "Y":
		if err := clipboard.WriteAll(render.FullReport(m.reviews)); err != nil {
			m.statusMsg = "clipboard unavailable (install wl-clipboard or xclip)"
		} else {
			m.statusMsg = "✓ full review copied to clipboard"
		}
	}
	return m, nil
}

// ---- check results ----

var checkSevFilters = []string{"", "critical", "high", "medium", "low", "info"}

func (m *Model) filteredChecks() []checks.Finding {
	if m.sevFilter == "" {
		return m.flatCheck
	}
	var out []checks.Finding
	for _, f := range m.flatCheck {
		if f.Severity == m.sevFilter {
			out = append(out, f)
		}
	}
	return out
}

func (m *Model) keyCheckResults(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	fs := m.filteredChecks()
	switch msg.String() {
	case "q", "esc":
		return m, tea.Quit
	case "up", "k":
		if m.resCur > 0 {
			m.resCur--
		}
		m.statusMsg = ""
	case "down", "j":
		if m.resCur < len(fs)-1 {
			m.resCur++
		}
		m.statusMsg = ""
	case "f":
		cur := 0
		for i, s := range checkSevFilters {
			if s == m.sevFilter {
				cur = i
				break
			}
		}
		m.sevFilter = checkSevFilters[(cur+1)%len(checkSevFilters)]
		m.resCur = 0
	case "y":
		if len(fs) > 0 {
			if err := clipboard.WriteAll(strings.TrimSpace(fs[m.resCur].FixPrompt)); err != nil {
				m.statusMsg = "clipboard unavailable (install wl-clipboard or xclip)"
			} else {
				m.statusMsg = "✓ fix prompt copied — paste it into Claude Code / Cursor"
			}
		}
	case "c":
		if len(fs) > 0 {
			if err := clipboard.WriteAll(render.CheckFindingBlock(fs[m.resCur])); err != nil {
				m.statusMsg = "clipboard unavailable (install wl-clipboard or xclip)"
			} else {
				m.statusMsg = "✓ finding copied (problem + impact + fix + prompt)"
			}
		}
	case "Y":
		if err := clipboard.WriteAll(render.CheckReportMarkdown(m.checkRes)); err != nil {
			m.statusMsg = "clipboard unavailable (install wl-clipboard or xclip)"
		} else {
			m.statusMsg = "✓ full check report copied to clipboard"
		}
	}
	return m, nil
}

func indexOfStyle(key string) int {
	for i, s := range review.Styles {
		if s.Key == key {
			return i
		}
	}
	return 0
}

func indexOfCategory(key string) int {
	for i, c := range checks.Categories {
		if c.Key == key {
			return i
		}
	}
	return 0
}

func indexOfScope(c checks.Category, key string) int {
	for i, s := range c.Scopes {
		if s.Key == key {
			return i
		}
	}
	return 0
}

func indexOfModel(key string) int {
	for i, mo := range modelChoices {
		if mo.key == key {
			return i
		}
	}
	return 0
}
