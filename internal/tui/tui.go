// Package tui implements silly-review's interactive terminal UI: repo select →
// branch select → base config → style/model → live progress → results.
package tui

import (
	"context"
	"fmt"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"silly-review/internal/config"
	"silly-review/internal/discover"
	"silly-review/internal/gitx"
	"silly-review/internal/render"
	"silly-review/internal/review"
)

type screen int

const (
	scRepoSelect screen = iota
	scLoading
	scBranchSelect
	scBaseConfig
	scStyle
	scModel
	scProgress
	scResults
	scError
)

// repoPick is the accumulating choice for one repo.
type repoPick struct {
	repo   *gitx.Repo
	base   string // resolved base ref, e.g. "origin/main"
	branch gitx.Branch
}

type modelOpt struct{ key, desc string }

var modelChoices = []modelOpt{
	{"opus", "Most capable — best for thorough, nuanced reviews"},
	{"sonnet", "Fast and strong — a great default for most reviews"},
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
}

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

	baseCands  []string
	baseCur    int
	baseReturn screen

	styleCur int
	modelCur int

	spin     spinner.Model
	logLines []string
	costUSD  float64

	reviews   []render.RepoReview
	flat      []review.Finding
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
	err           error
}

type logMsg struct{ repo, text string }
type retryMsg struct{ text string }
type allDoneMsg struct {
	reviews []render.RepoReview
	cost    float64
}
type reviewErrMsg struct{ err error }

// New builds the root model.
func New(p Params) *Model {
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
	if m.disc.Mode == discover.Multi {
		m.screen = scRepoSelect
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
		return nil
	}
	m.picks = []*repoPick{{repo: m.repos[0]}}
	m.cur = 0
	return m.startLoadingRepo()
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
	case allDoneMsg:
		return m.onAllDone(msg)
	case reviewErrMsg:
		m.err = msg.err
		m.screen = scError
		return m, nil
	}
	return m, nil
}

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.screen {
	case scRepoSelect:
		return m.keyRepoSelect(msg)
	case scBranchSelect:
		return m.keyBranchSelect(msg)
	case scBaseConfig:
		return m.keyBaseConfig(msg)
	case scStyle:
		return m.keyStyle(msg)
	case scModel:
		return m.keyModel(msg)
	case scProgress:
		if k := msg.String(); k == "esc" || k == "q" {
			m.cancel()
			return m, tea.Quit
		}
		return m, nil
	case scResults:
		return m.keyResults(msg)
	case scError:
		return m, tea.Quit
	}
	return m, nil
}

// ---- repo select ----

func (m *Model) keyRepoSelect(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc":
		return m, tea.Quit
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
			m.statusMsg = "select at least one repo (space to toggle)"
			return m, nil
		}
		m.picks = nil
		var names []string
		for _, i := range sel {
			m.picks = append(m.picks, &repoPick{repo: m.repos[i]})
			names = append(names, m.repos[i].Name)
		}
		fc := m.cfg.Folder(m.folderKey)
		fc.LastRepos = names
		m.cfg.SetFolder(m.folderKey, fc)
		_ = m.cfg.Save()
		m.cur = 0
		return m, m.startLoadingRepo()
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
	return tea.Batch(loadRepoCmd(m.ctx, r, m.fetch, m.cur), m.spin.Tick)
}

func loadRepoCmd(ctx context.Context, repo *gitx.Repo, fetch bool, idx int) tea.Cmd {
	return func() tea.Msg {
		if repo.Remote == "" {
			return repoLoadedMsg{idx: idx, err: fmt.Errorf("%s has no remote (origin) to review", repo.Name)}
		}
		if fetch {
			_ = gitx.Fetch(ctx, repo.Path, repo.Remote)
		}
		branches, err := gitx.RemoteBranches(ctx, repo.Path, repo.Remote)
		if err != nil {
			return repoLoadedMsg{idx: idx, err: err}
		}
		if len(branches) == 0 {
			return repoLoadedMsg{idx: idx, err: fmt.Errorf("%s has no remote branches", repo.Name)}
		}
		def, _ := gitx.DefaultBranch(ctx, repo.Path, repo.Remote)
		return repoLoadedMsg{idx: idx, branches: branches, defaultBranch: def, candidates: baseCandidates(def, branches)}
	}
}

func (m *Model) onRepoLoaded(msg repoLoadedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.err = msg.err
		m.screen = scError
		return m, nil
	}
	m.branches = msg.branches
	m.brCur = 0
	m.baseCands = msg.candidates
	m.baseCur = 0
	if base, ok := m.cfg.RepoBase(m.picks[m.cur].repo.Path); ok {
		m.picks[m.cur].base = base
		m.screen = scBranchSelect
		return m, nil
	}
	m.baseReturn = scBranchSelect
	m.screen = scBaseConfig
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
	case "q", "esc":
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
		m.statusMsg = ""
		m.baseReturn = scBranchSelect
		m.baseCur = 0
		m.screen = scBaseConfig
	case "enter":
		sel := m.branches[m.brCur]
		// Reviewing the base against itself yields an empty diff — catch it here
		// rather than spinning up a worktree and a no-op review.
		if sel.Ref == m.picks[m.cur].base {
			m.statusMsg = fmt.Sprintf("%s is the base branch — pick the branch you want to review, or press c to change the base", sel.Name)
			return m, nil
		}
		m.statusMsg = ""
		m.picks[m.cur].branch = sel
		m.cur++
		if m.cur < len(m.picks) {
			return m, m.startLoadingRepo()
		}
		m.screen = scStyle
		m.styleCur = indexOfStyle(m.cfg.Folder(m.folderKey).Style)
		return m, nil
	}
	return m, nil
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
		m.screen = m.baseReturn
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
		m.screen = scModel
		m.modelCur = indexOfModel(m.cfg.Folder(m.folderKey).Model)
	}
	return m, nil
}

// ---- model ----

func (m *Model) keyModel(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q":
		return m, tea.Quit
	case "esc":
		m.screen = scStyle
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
		fc.Style = review.Styles[m.styleCur].Key
		fc.Model = modelChoices[m.modelCur].key
		m.cfg.SetFolder(m.folderKey, fc)
		_ = m.cfg.Save()
		return m, m.startReview()
	}
	return m, nil
}

// ---- progress / review orchestration ----

func (m *Model) startReview() tea.Cmd {
	m.screen = scProgress
	m.logLines = nil
	m.events = make(chan tea.Msg, 256)
	style := review.Styles[m.styleCur]
	model := modelChoices[m.modelCur].key
	return tea.Batch(
		launchReview(m.ctx, m.ws, m.picks, style, model, m.binPath, m.events),
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

func indexOfStyle(key string) int {
	for i, s := range review.Styles {
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
