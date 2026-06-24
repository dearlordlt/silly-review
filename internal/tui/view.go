package tui

import (
	"fmt"
	"strings"
	"time"

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
	case scMatch:
		return m.viewMatch()
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
	s := titleStyle.Render("⬡ silly-review")
	if appVersion != "" {
		s += dimStyle.Render(" " + appVersion)
	}
	s += dimStyle.Render("  ·  " + title)
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
	b.WriteString(header("pick the repos this change touches",
		"A feature can span several repos (frontend, backend, deploy). Check every repo involved — you'll choose one branch per repo next. Last run's picks are pre-checked."))
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
	b.WriteString("\n" + helpStyle.Render("↑/↓ move · space check/uncheck · a check all · enter continue · q quit"))
	return b.String()
}

func (m *Model) viewLoading() string {
	return header("loading branches", "Fetching the remote branch list — read-only, nothing is checked out.") +
		m.spin.View() + " " + m.loadingMsg
}

func (m *Model) viewBranchSelect() string {
	w, _ := m.dims()
	p := m.picks[m.cur]
	var sub string
	switch {
	case len(m.picks) <= 1:
		sub = fmt.Sprintf("%s  ·  diffed against %s  ·  your unpushed branches + remote, newest first", p.repo.Name, p.base)
	case m.anchorBranchName == "":
		// The anchor repo — its branch name drives matching in the others.
		sub = fmt.Sprintf("repo 1/%d · %s  ·  base %s  ·  this branch's name is matched against the other repos next", len(m.picks), p.repo.Name, p.base)
	default:
		sub = fmt.Sprintf("%s  ·  base %s  ·  newest first", p.repo.Name, p.base)
	}
	var b strings.Builder
	b.WriteString(header(fmt.Sprintf("pick the branch to review in %s", p.repo.Name), sub))
	for i, br := range m.branches {
		cursor := "  "
		name := br.Name
		if i == m.brCur {
			cursor = cursorStyle.Render("▸ ")
			name = cursorStyle.Render(name)
		}
		tag := ""
		if br.Local {
			tag = selStyle.Render(" (local, unpushed)")
		}
		meta := dimStyle.Render(fmt.Sprintf("%s · %s", br.Author, br.DateRel))
		line := fmt.Sprintf("%s%s%s  %s  %s", cursor, name, tag, truncate(br.Subject, w/2), meta)
		b.WriteString(truncate(line, w) + "\n")
	}
	if m.statusMsg != "" {
		b.WriteString("\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("208")).Render("! "+m.statusMsg) + "\n")
	}
	b.WriteString("\n" + helpStyle.Render("↑/↓ move · enter review this branch · c change base · q quit"))
	return b.String()
}

func (m *Model) viewBaseConfig() string {
	p := m.picks[m.cur]
	var b strings.Builder
	b.WriteString(header(fmt.Sprintf("set the base branch for %s", p.repo.Name),
		fmt.Sprintf("%s's branch is diffed against this — only changes since this branch get reviewed. Usually your trunk (origin/main, origin/master) or integration branch (origin/dev). Asked once per repo, then remembered.", p.repo.Name)))
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
	help := "↑/↓ move · enter use as base · q quit"
	if p.base != "" {
		help = "↑/↓ move · enter use as base · esc keep current · q quit"
	}
	b.WriteString("\n" + helpStyle.Render(help))
	return b.String()
}

// viewMatch is the cross-repo step: after the anchor branch is picked, each
// other repo gets this screen — auto-match (same name), warn (same name, other
// author), or no-match (drop it here without restarting).
func (m *Model) viewMatch() string {
	w, _ := m.dims()
	p := m.picks[m.cur]
	bn := m.anchorBranchName
	warn := lipgloss.NewStyle().Foreground(lipgloss.Color("208"))

	var title, sub string
	var options []string
	switch {
	case m.matched == nil:
		title = fmt.Sprintf("%s has no '%s' branch", p.repo.Name, bn)
		sub = fmt.Sprintf("This change may not touch %s. You can drop it here instead of quitting and starting over.", p.repo.Name)
		options = []string{
			fmt.Sprintf("[s] Skip %s — leave it out of this review", p.repo.Name),
			fmt.Sprintf("[m] Pick a branch in %s manually", p.repo.Name),
		}
	case m.matched.Author == m.anchorAuthor:
		title = fmt.Sprintf("%s also has '%s'", p.repo.Name, bn)
		sub = fmt.Sprintf("Same branch name, same author (%s) — looks like the same change spans %s too.", m.matched.Author, p.repo.Name)
		options = []string{
			fmt.Sprintf("[y] Yes — review '%s' in %s too", bn, p.repo.Name),
			fmt.Sprintf("[s] Skip %s (drop it from this review)", p.repo.Name),
			fmt.Sprintf("[m] Pick a different branch in %s manually", p.repo.Name),
		}
	default:
		title = fmt.Sprintf("%s has a '%s' branch", p.repo.Name, bn)
		sub = fmt.Sprintf("Same name, but a different author (%s, not %s) — could be the same change or unrelated. Check before including it.", m.matched.Author, m.anchorAuthor)
		options = []string{
			fmt.Sprintf("[y] Yes — review '%s' in %s too", bn, p.repo.Name),
			fmt.Sprintf("[s] Skip %s (drop it from this review)", p.repo.Name),
			fmt.Sprintf("[m] Pick a different branch in %s manually", p.repo.Name),
		}
	}

	var b strings.Builder
	b.WriteString(header(title, sub))
	if m.matched != nil {
		author := m.matched.Author
		if m.matched.Author != m.anchorAuthor {
			author = warn.Render(author)
		}
		ltag := ""
		if m.matched.Local {
			ltag = selStyle.Render(" (local, unpushed)")
		}
		meta := dimStyle.Render(author + " · " + m.matched.DateRel)
		row := fmt.Sprintf("  %s%s  %s  %s", m.matched.Name, ltag, truncate(m.matched.Subject, w/2), meta)
		b.WriteString(truncate(row, w) + "\n")
		if p.base != "" {
			b.WriteString(dimStyle.Render("  will be diffed against "+p.base) + "\n")
		}
		b.WriteString("\n")
	}
	for i, opt := range options {
		cursor := "  "
		label := opt
		if i == m.matchCur {
			cursor = cursorStyle.Render("▸ ")
			label = cursorStyle.Render(label)
		}
		b.WriteString(cursor + label + "\n")
	}
	if m.statusMsg != "" {
		b.WriteString("\n" + warn.Render("! "+m.statusMsg) + "\n")
	}
	help := "↑/↓ move · enter choose · s skip · m manual · q quit"
	if m.matched != nil {
		help = "↑/↓ move · enter choose · y yes · s skip · m manual · q quit"
	}
	b.WriteString("\n" + helpStyle.Render(help))
	return b.String()
}

func (m *Model) viewStyle() string {
	var b strings.Builder
	b.WriteString(header("choose a review style", "Sets how hard Claude pushes — what it flags and how deep it goes. Remembered for this folder; change anytime."))
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
	b.WriteString(header("choose the model", "Which Claude model runs the review. Bigger reads more carefully but takes longer. Runs on your Claude subscription — no API key, no per-token charge."))
	for i, mo := range modelChoices {
		cursor := "  "
		name := mo.key
		if i == m.modelCur {
			cursor = cursorStyle.Render("▸ ")
			name = cursorStyle.Render(name)
		}
		b.WriteString(fmt.Sprintf("%s%-8s %s\n", cursor, name, dimStyle.Render(mo.desc)))
	}
	b.WriteString("\n" + helpStyle.Render("↑/↓ move · enter start review · esc back · q quit"))
	return b.String()
}

func (m *Model) viewProgress() string {
	w, h := m.dims()
	n := 0
	for _, p := range m.picks {
		if !p.dropped {
			n++
		}
	}
	if n == 0 {
		n = 1
	}
	noun := "branch"
	if n > 1 {
		noun = "branches"
	}
	var b strings.Builder
	b.WriteString(header(fmt.Sprintf("reviewing %d %s", n, noun),
		"Claude is reading each branch's diff and the surrounding code. A minute or two is normal. Read-only — nothing is written to your repos."))
	// Live status line: spinner + the current action + elapsed time, so it never
	// looks frozen even during Claude's long, tool-less generation phase.
	activity := m.curActivity
	if activity == "" {
		activity = "working…"
	}
	elapsed := dimStyle.Render(" · " + fmtElapsed(time.Since(m.reviewStart)))
	b.WriteString(truncate(m.spin.View()+" "+activity, w-12) + elapsed + "\n\n")
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
		// if there are none at all, always show the per-repo assessment.
		if len(m.flat) > 0 && m.sevFilter != "" {
			b.WriteString(dimStyle.Render(fmt.Sprintf("No %s findings. Press f to change filter.\n", m.sevFilter)))
		} else {
			b.WriteString(truncateToLines(m.emptyStateBody(), h-6, "(Y copies the full write-up)"))
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

// emptyStateBody renders what happened when there are no line-level findings:
// a failure, a no-op (empty diff), or — crucially — the reviewer's actual
// assessment (verdict + summary + narrative) so a clean review proves its work
// instead of just celebrating emptiness.
func (m *Model) emptyStateBody() string {
	if len(m.reviews) == 0 {
		return okStyle.Render("No findings. 🎉\n")
	}
	w, _ := m.dims()
	wrap := lipgloss.NewStyle().Width(min(w-2, 100))
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
		case rr.Review != nil:
			head := okStyle.Render("✓ " + rr.Repo)
			if v := rr.Review.Verdict; v != "" {
				head += dimStyle.Render(" — " + v + ", no blocking findings")
			}
			b.WriteString(head + "\n\n")
			if s := strings.TrimSpace(rr.Review.Summary); s != "" {
				b.WriteString(wrap.Render(s) + "\n")
			}
			if n := render.ProseNotes(rr); n != "" {
				b.WriteString("\n" + wrap.Render(n) + "\n")
			}
			b.WriteString("\n" + dimStyle.Render("(Y copies the full write-up. For line-level notes, re-run with the Thorough style.)") + "\n")
		case strings.TrimSpace(rr.RawText) != "":
			b.WriteString(dimStyle.Render(fmt.Sprintf("• %s — assessment (no structured findings):", rr.Repo)) + "\n\n")
			b.WriteString(wrap.Render(strings.TrimSpace(rr.RawText)) + "\n")
		default:
			b.WriteString(okStyle.Render(fmt.Sprintf("✓ %s — no findings", rr.Repo)) + "\n")
		}
	}
	if authHint {
		b.WriteString("\n" + dimStyle.Render("Auth failed. Try `claude -p hi` in THIS terminal:") + "\n")
		b.WriteString(dimStyle.Render("  · if that also fails, run `claude` and sign in.") + "\n")
		b.WriteString(dimStyle.Render("  · if it works there but not here, you launched silly-review from inside a Claude Code session — open a plain terminal.") + "\n")
	}
	return b.String()
}

func fmtElapsed(d time.Duration) string {
	s := int(d.Seconds())
	if s < 0 {
		s = 0
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
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
	content := truncateToLines(b.String(), h-2, "(press y to copy this comment)")
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
	// Account for repos that were skipped or failed to load.
	for _, p := range m.picks {
		if p.dropped {
			reason := "skipped"
			if p.loadErr != nil {
				reason = "skipped (couldn't load branches)"
			}
			parts = append(parts, fmt.Sprintf("%s: %s", p.repo.Name, reason))
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

func truncateToLines(s string, n int, hint string) string {
	if n < 1 {
		n = 1
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= n {
		return strings.Join(lines, "\n")
	}
	lines = lines[:n]
	lines[n-1] = dimStyle.Render("… " + hint)
	return strings.Join(lines, "\n")
}
