package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"silly-review/internal/render"
	"silly-review/internal/review"
)

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63"))
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	cursorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("212")).Bold(true)
	selStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	helpStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
	okStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Bold(true)
	boxStyle    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("238")).Padding(0, 1)
)

func sevColor(sev string) lipgloss.Color {
	switch sev {
	case "blocker":
		return lipgloss.Color("196")
	case "major":
		return lipgloss.Color("208")
	case "minor":
		return lipgloss.Color("220")
	case "nit":
		return lipgloss.Color("245")
	case "question":
		return lipgloss.Color("39")
	case "praise":
		return lipgloss.Color("42")
	default:
		return lipgloss.Color("245")
	}
}

func (m *Model) dims() (int, int) {
	w, h := m.width, m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	return w, h
}

func (m *Model) View() string {
	switch m.screen {
	case scRepoSelect:
		return m.viewRepoSelect()
	case scLoading:
		return m.viewLoading()
	case scBranchSelect:
		return m.viewBranchSelect()
	case scBaseConfig:
		return m.viewBaseConfig()
	case scStyle:
		return m.viewStyle()
	case scModel:
		return m.viewModel()
	case scProgress:
		return m.viewProgress()
	case scResults:
		return m.viewResults()
	case scError:
		return m.viewError()
	}
	return ""
}

func header(title, sub string) string {
	s := titleStyle.Render("⬡ silly-review") + dimStyle.Render("  ·  "+title)
	if sub != "" {
		s += "\n" + dimStyle.Render(sub)
	}
	return s + "\n\n"
}

func (m *Model) statusLine() string {
	if m.statusMsg == "" {
		return ""
	}
	return "\n" + okStyle.Render(m.statusMsg)
}

func (m *Model) viewRepoSelect() string {
	var b strings.Builder
	b.WriteString(header("select repositories", "a feature can span several repos — pick all that apply"))
	for i, r := range m.repos {
		cursor := "  "
		if i == m.repoCur {
			cursor = cursorStyle.Render("▸ ")
		}
		check := "[ ]"
		if m.repoSel[i] {
			check = selStyle.Render("[✓]")
		}
		b.WriteString(fmt.Sprintf("%s%s %s\n", cursor, check, r.Name))
	}
	if m.statusMsg != "" {
		b.WriteString("\n" + dimStyle.Render(m.statusMsg))
	}
	b.WriteString("\n" + helpStyle.Render("↑/↓ move · space toggle · a all · enter confirm · q quit"))
	return b.String()
}

func (m *Model) viewLoading() string {
	return header("loading", "") + m.spin.View() + " " + m.loadingMsg
}

func (m *Model) viewBranchSelect() string {
	w, _ := m.dims()
	p := m.picks[m.cur]
	sub := fmt.Sprintf("%s  ·  base: %s", p.repo.Name, p.base)
	if len(m.picks) > 1 {
		sub = fmt.Sprintf("repo %d/%d — %s", m.cur+1, len(m.picks), sub)
	}
	var b strings.Builder
	b.WriteString(header("select branch to review", sub))
	for i, br := range m.branches {
		cursor := "  "
		name := br.Name
		if i == m.brCur {
			cursor = cursorStyle.Render("▸ ")
			name = cursorStyle.Render(name)
		}
		meta := dimStyle.Render(fmt.Sprintf("%s · %s", br.Author, br.DateRel))
		line := fmt.Sprintf("%s%s  %s  %s", cursor, name, truncate(br.Subject, w/2), meta)
		b.WriteString(truncate(line, w) + "\n")
	}
	if m.statusMsg != "" {
		b.WriteString("\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("208")).Render("! "+m.statusMsg) + "\n")
	}
	b.WriteString("\n" + helpStyle.Render("↑/↓ move · enter select · c change base · q quit"))
	return b.String()
}

func (m *Model) viewBaseConfig() string {
	p := m.picks[m.cur]
	var b strings.Builder
	b.WriteString(header("set base branch", fmt.Sprintf("what should %s be diffed against? (remembered for this repo)", p.repo.Name)))
	for i, c := range m.baseCands {
		cursor := "  "
		label := c
		if i == m.baseCur {
			cursor = cursorStyle.Render("▸ ")
			label = cursorStyle.Render(label)
		}
		tag := ""
		if i == 0 {
			tag = dimStyle.Render("  (detected default)")
		}
		b.WriteString(fmt.Sprintf("%s%s%s\n", cursor, label, tag))
	}
	help := "↑/↓ move · enter save"
	if p.base != "" {
		help += " · esc cancel"
	}
	b.WriteString("\n" + helpStyle.Render(help))
	return b.String()
}

func (m *Model) viewStyle() string {
	var b strings.Builder
	b.WriteString(header("review style", "remembered for this folder; change anytime"))
	for i, s := range review.Styles {
		cursor := "  "
		name := s.Name
		if i == m.styleCur {
			cursor = cursorStyle.Render("▸ ")
			name = cursorStyle.Render(name)
		}
		b.WriteString(fmt.Sprintf("%s%s\n      %s\n", cursor, name, dimStyle.Render(s.Desc)))
	}
	b.WriteString("\n" + helpStyle.Render("↑/↓ move · enter next · q quit"))
	return b.String()
}

func (m *Model) viewModel() string {
	var b strings.Builder
	b.WriteString(header("model", "uses your Claude subscription"))
	for i, mo := range modelChoices {
		cursor := "  "
		name := mo.key
		if i == m.modelCur {
			cursor = cursorStyle.Render("▸ ")
			name = cursorStyle.Render(name)
		}
		b.WriteString(fmt.Sprintf("%s%-8s %s\n", cursor, name, dimStyle.Render(mo.desc)))
	}
	b.WriteString("\n" + helpStyle.Render("↑/↓ move · enter start review · esc back"))
	return b.String()
}

func (m *Model) viewProgress() string {
	_, h := m.dims()
	var b strings.Builder
	b.WriteString(header("reviewing", "this can take a minute — Claude is reading the code"))
	b.WriteString(m.spin.View() + " working…\n\n")
	// show the tail of the activity log
	maxLog := h - 7
	if maxLog < 3 {
		maxLog = 3
	}
	lines := m.logLines
	if len(lines) > maxLog {
		lines = lines[len(lines)-maxLog:]
	}
	for _, l := range lines {
		b.WriteString(dimStyle.Render("· ") + l + "\n")
	}
	b.WriteString("\n" + helpStyle.Render("esc cancel (nothing is written to your repos)"))
	return b.String()
}

func (m *Model) viewResults() string {
	w, h := m.dims()
	fs := m.filtered()
	if m.resCur >= len(fs) {
		m.resCur = max(0, len(fs)-1)
	}

	var b strings.Builder
	b.WriteString(header("review", m.resultSummary()))

	if len(fs) == 0 {
		// Only the *filter* emptied the list when there are findings overall;
		// if there are none at all, always show the per-repo diagnostics.
		if len(m.flat) > 0 && m.sevFilter != "" {
			b.WriteString(dimStyle.Render(fmt.Sprintf("No %s findings. Press f to change filter.\n", m.sevFilter)))
		} else {
			b.WriteString(m.emptyStateBody())
		}
		b.WriteString(m.statusLine())
		b.WriteString("\n\n" + helpStyle.Render("Y copy full review · f filter · q quit"))
		return b.String()
	}

	// Split remaining space between the list and the detail pane.
	avail := h - 6
	if avail < 6 {
		avail = 6
	}
	listH := avail / 2
	if listH < 3 {
		listH = 3
	}
	detailH := avail - listH

	// windowed list
	start := m.resCur - listH/2
	if start < 0 {
		start = 0
	}
	if start > len(fs)-listH {
		start = max(0, len(fs)-listH)
	}
	end := min(len(fs), start+listH)
	for i := start; i < end; i++ {
		f := fs[i]
		cursor := "  "
		if i == m.resCur {
			cursor = cursorStyle.Render("▸ ")
		}
		badge := lipgloss.NewStyle().Foreground(sevColor(f.Severity)).Bold(true).Render(strings.ToUpper(f.Severity))
		loc := dimStyle.Render(render.Locator(f))
		row := fmt.Sprintf("%s%-8s %s  %s", cursor, badge, f.Title, loc)
		b.WriteString(truncate(row, w) + "\n")
	}

	// detail pane for the selected finding
	b.WriteString("\n")
	b.WriteString(m.renderDetail(fs[m.resCur], w, detailH))

	b.WriteString(m.statusLine())
	b.WriteString("\n" + helpStyle.Render("↑/↓ move · y copy comment · Y copy all · f filter · q quit"))
	return b.String()
}

// emptyStateBody explains *why* there are no findings: per repo it was a
// failure, a no-op (empty diff), or a genuinely clean review.
func (m *Model) emptyStateBody() string {
	if len(m.reviews) == 0 {
		return okStyle.Render("No findings. 🎉\n")
	}
	var b strings.Builder
	authHint := false
	for _, rr := range m.reviews {
		switch {
		case rr.Err != "":
			b.WriteString(errStyle.Render("✗ "+rr.Repo+" — review failed") + "\n")
			b.WriteString(dimStyle.Render("  "+firstLineOf(rr.Err)) + "\n")
			low := strings.ToLower(rr.Err)
			if strings.Contains(low, "authenticat") || strings.Contains(low, "401") {
				authHint = true
			}
		case rr.NoChanges:
			b.WriteString(dimStyle.Render(fmt.Sprintf("• %s — no changes between %s and %s (nothing to review)", rr.Repo, rr.Branch, rr.Base)) + "\n")
		case rr.Review == nil && strings.TrimSpace(rr.RawText) != "":
			b.WriteString(dimStyle.Render(fmt.Sprintf("• %s — review returned prose, no structured findings (press Y to copy)", rr.Repo)) + "\n")
		default:
			b.WriteString(okStyle.Render(fmt.Sprintf("✓ %s — no findings 🎉", rr.Repo)) + "\n")
		}
	}
	if authHint {
		b.WriteString("\n" + dimStyle.Render("Auth failed. Try `claude -p hi` in THIS terminal:") + "\n")
		b.WriteString(dimStyle.Render("  · if that also fails, run `claude` and sign in.") + "\n")
		b.WriteString(dimStyle.Render("  · if it works there but not here, you launched silly-review from inside a Claude Code session — open a plain terminal.") + "\n")
	}
	return b.String()
}

func firstLineOf(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:199] + "…"
	}
	return s
}

func (m *Model) renderDetail(f review.Finding, w, h int) string {
	inner := w - 4
	if inner < 20 {
		inner = 20
	}
	wrap := lipgloss.NewStyle().Width(inner)
	var b strings.Builder
	head := lipgloss.NewStyle().Foreground(sevColor(f.Severity)).Bold(true).Render(render.SeverityTag(f))
	b.WriteString(head + "  " + dimStyle.Render(render.Locator(f)) + "\n")
	b.WriteString(wrap.Render(strings.TrimSpace(f.Comment)) + "\n")
	if s := strings.TrimSpace(f.CodeSnippet); s != "" {
		b.WriteString(dimStyle.Render(truncate(s, inner)) + "\n")
	}
	if s := strings.TrimSpace(f.Suggestion); s != "" {
		b.WriteString(selStyle.Render("suggestion: ") + wrap.Render(s) + "\n")
	}
	content := truncateToLines(b.String(), h-2)
	return boxStyle.Width(w - 2).Render(content)
}

func (m *Model) resultSummary() string {
	n := len(m.flat)
	parts := []string{fmt.Sprintf("%d findings", n)}
	for _, rr := range m.reviews {
		switch {
		case rr.Err != "":
			parts = append(parts, fmt.Sprintf("%s: failed", rr.Repo))
		case rr.NoChanges:
			parts = append(parts, fmt.Sprintf("%s: no changes", rr.Repo))
		case rr.Review == nil && strings.TrimSpace(rr.RawText) != "":
			parts = append(parts, fmt.Sprintf("%s: prose review", rr.Repo))
		case rr.Review != nil && rr.Review.Verdict != "":
			parts = append(parts, fmt.Sprintf("%s: %s", rr.Repo, rr.Review.Verdict))
		}
	}
	if m.sevFilter != "" {
		parts = append(parts, "filter="+m.sevFilter)
	}
	if m.costUSD > 0 {
		parts = append(parts, fmt.Sprintf("$%.3f", m.costUSD))
	}
	return strings.Join(parts, "  ·  ")
}

func (m *Model) viewError() string {
	var b strings.Builder
	b.WriteString(header("error", ""))
	b.WriteString(errStyle.Render("✗ ") + m.err.Error() + "\n")
	b.WriteString("\n" + helpStyle.Render("press any key to quit"))
	return b.String()
}

// ---- helpers ----

func truncate(s string, w int) string {
	if w <= 1 {
		return s
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	// Trim by runes until it fits (accounts for styled width roughly).
	r := []rune(s)
	for len(r) > 0 && lipgloss.Width(string(r)) > w-1 {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}

func truncateToLines(s string, n int) string {
	if n < 1 {
		n = 1
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= n {
		return strings.Join(lines, "\n")
	}
	lines = lines[:n]
	lines[n-1] = dimStyle.Render("… (press y to copy the full comment)")
	return strings.Join(lines, "\n")
}
