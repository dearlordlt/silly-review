package render

import (
	"fmt"
	"sort"
	"strings"

	"silly-review/internal/checks"
)

// CheckResult pairs a repo with its health-check outcome. Exactly one outcome
// holds: Err != "" (failed) or Report set (possibly with zero findings).
type CheckResult struct {
	Repo     string
	Ref      string // the ref that was audited, e.g. "feat/x" or "origin/main"
	Category string // display name, e.g. "Security"
	Scope    string // display name, e.g. "Auth & access control"
	Report   *checks.Report
	RawText  string // fallback / narrative text from the model
	Err      string
}

// CheckLocator is the "repo/file:line" string for a check finding.
func CheckLocator(f checks.Finding) string {
	loc := fmt.Sprintf("%s:%d", f.File, f.StartLine)
	if f.EndLine > f.StartLine {
		loc = fmt.Sprintf("%s:%d-%d", f.File, f.StartLine, f.EndLine)
	}
	if f.Repo != "" {
		loc = f.Repo + "/" + loc
	}
	return loc
}

// CheckSeverityTag renders e.g. "[HIGH · quick fix]".
func CheckSeverityTag(f checks.Finding) string {
	sev := strings.ToUpper(f.Severity)
	if f.Effort != "" {
		return fmt.Sprintf("[%s · %s fix]", sev, f.Effort)
	}
	return fmt.Sprintf("[%s]", sev)
}

// SortCheckFindings orders findings by severity, then file/line.
func SortCheckFindings(fs []checks.Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		if checks.SeverityRank(fs[i].Severity) != checks.SeverityRank(fs[j].Severity) {
			return checks.SeverityRank(fs[i].Severity) < checks.SeverityRank(fs[j].Severity)
		}
		if fs[i].File != fs[j].File {
			return fs[i].File < fs[j].File
		}
		return fs[i].StartLine < fs[j].StartLine
	})
}

// fence returns a code fence long enough that s can't close it early (a fix
// prompt may itself contain ``` blocks).
func fence(s string) string {
	f := "```"
	for strings.Contains(s, f) {
		f += "`"
	}
	return f
}

// CheckFindingBlock is the markdown for a single check finding (what `c`
// copies) — suitable for a ticket or a chat message.
func CheckFindingBlock(f checks.Finding) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**%s** %s\n\n", f.Title, CheckSeverityTag(f))
	if s := strings.TrimSpace(f.Problem); s != "" {
		b.WriteString(s + "\n")
	}
	if s := strings.TrimSpace(f.Impact); s != "" {
		b.WriteString("\n**Impact:** " + s + "\n")
	}
	if s := strings.TrimSpace(f.Solution); s != "" {
		b.WriteString("\n**Fix:** " + s + "\n")
	}
	if s := strings.TrimSpace(f.CodeSnippet); s != "" {
		fmt.Fprintf(&b, "\n> `%s`\n>\n", CheckLocator(f))
		for _, line := range strings.Split(s, "\n") {
			fmt.Fprintf(&b, "> %s\n", line)
		}
	}
	if s := strings.TrimSpace(f.FixPrompt); s != "" {
		fn := fence(s)
		fmt.Fprintf(&b, "\n**Fix prompt** (paste into Claude Code / Cursor):\n\n%s\n%s\n%s\n", fn, s, fn)
	}
	return b.String()
}

// CheckReportMarkdown renders the entire check as markdown (what `Y` copies /
// headless mode prints / `--out` writes).
func CheckReportMarkdown(cr CheckResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Health check: %s — %s (%s)\n\n", cr.Repo, cr.Category, cr.Scope)
	fmt.Fprintf(&b, "_Audited `%s`_\n\n", cr.Ref)
	if cr.Err != "" {
		fmt.Fprintf(&b, "_Check failed: %s_\n\n", cr.Err)
		return b.String()
	}
	if cr.Report == nil {
		if cr.RawText != "" {
			b.WriteString(cr.RawText + "\n\n")
		} else {
			b.WriteString("_No report returned._\n\n")
		}
		return b.String()
	}
	if h := cr.Report.Health; h != "" {
		fmt.Fprintf(&b, "**Health:** %s\n\n", checks.HealthLabel(h))
	}
	if s := strings.TrimSpace(cr.Report.Summary); s != "" {
		b.WriteString(s + "\n\n")
	}
	if n := checkProseNotes(cr); n != "" {
		b.WriteString("**Auditor's notes**\n\n" + n + "\n\n")
	}
	findings := append([]checks.Finding(nil), cr.Report.Findings...)
	for i := range findings {
		if findings[i].Repo == "" {
			findings[i].Repo = cr.Repo
		}
	}
	SortCheckFindings(findings)
	if len(findings) == 0 {
		b.WriteString("_No findings._\n\n")
		return b.String()
	}
	for _, f := range findings {
		fmt.Fprintf(&b, "### %s — `%s`\n\n", f.Title, CheckLocator(f))
		b.WriteString(CheckSeverityTag(f) + "\n\n")
		if s := strings.TrimSpace(f.Problem); s != "" {
			b.WriteString(s + "\n\n")
		}
		if s := strings.TrimSpace(f.Impact); s != "" {
			b.WriteString("**Impact:** " + s + "\n\n")
		}
		if s := strings.TrimSpace(f.Solution); s != "" {
			b.WriteString("**Fix:** " + s + "\n\n")
		}
		if s := strings.TrimSpace(f.CodeSnippet); s != "" {
			fmt.Fprintf(&b, "```\n%s\n```\n\n", s)
		}
		if s := strings.TrimSpace(f.FixPrompt); s != "" {
			fn := fence(s)
			fmt.Fprintf(&b, "**Fix prompt** (paste into Claude Code / Cursor):\n\n%s\n%s\n%s\n\n", fn, s, fn)
		}
	}
	return b.String()
}

// checkProseNotes mirrors ProseNotes for checks: surface the model's free-form
// narrative when it's materially richer than the structured summary.
func checkProseNotes(cr CheckResult) string {
	notes := strings.TrimSpace(cr.RawText)
	if notes == "" || cr.Report == nil {
		return ""
	}
	sum := strings.TrimSpace(cr.Report.Summary)
	if notes == sum || len(notes) < len(sum)+120 {
		return ""
	}
	return notes
}
