// Package render turns review findings into copy-paste-ready text: per-comment
// clipboard blocks, a full markdown report, and a plain-stdout dump.
package render

import (
	"fmt"
	"sort"
	"strings"

	"silly-review/internal/review"
)

// RepoReview pairs a repo name with its review outcome. Exactly one outcome
// holds: Err != "" (failed), NoChanges (empty diff vs base), or Review set
// (reviewed — possibly with zero findings).
type RepoReview struct {
	Repo      string
	Branch    string // head ref reviewed, e.g. origin/feat/x
	Base      string // base ref it was diffed against
	Review    *review.Review
	RawText   string // fallback text when structured output is missing
	Err       string
	NoChanges bool
}

// Locator is the "repo/file:line" string a reviewer searches for in the PR.
func Locator(f review.Finding) string {
	loc := fmt.Sprintf("%s:%d", f.File, f.StartLine)
	if f.EndLine > f.StartLine {
		loc = fmt.Sprintf("%s:%d-%d", f.File, f.StartLine, f.EndLine)
	}
	if f.Repo != "" {
		loc = f.Repo + "/" + loc
	}
	return loc
}

// SeverityTag renders e.g. "[MAJOR · correctness]".
func SeverityTag(f review.Finding) string {
	sev := strings.ToUpper(f.Severity)
	if f.Category != "" {
		return fmt.Sprintf("[%s · %s]", sev, f.Category)
	}
	return fmt.Sprintf("[%s]", sev)
}

// CommentBlock is the PR-ready markdown for a single finding (what `y` copies).
func CommentBlock(f review.Finding) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**%s** %s\n\n", f.Title, SeverityTag(f))
	b.WriteString(strings.TrimSpace(f.Comment))
	b.WriteString("\n")
	if s := strings.TrimSpace(f.CodeSnippet); s != "" {
		fmt.Fprintf(&b, "\n> `%s`\n>\n", Locator(f))
		for _, line := range strings.Split(s, "\n") {
			fmt.Fprintf(&b, "> %s\n", line)
		}
	}
	if s := strings.TrimSpace(f.Suggestion); s != "" {
		fmt.Fprintf(&b, "\n```suggestion\n%s\n```\n", s)
	}
	return b.String()
}

// SortFindings orders findings by repo, then severity, then file/line.
func SortFindings(fs []review.Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		if fs[i].Repo != fs[j].Repo {
			return fs[i].Repo < fs[j].Repo
		}
		if review.SeverityRank(fs[i].Severity) != review.SeverityRank(fs[j].Severity) {
			return review.SeverityRank(fs[i].Severity) < review.SeverityRank(fs[j].Severity)
		}
		if fs[i].File != fs[j].File {
			return fs[i].File < fs[j].File
		}
		return fs[i].StartLine < fs[j].StartLine
	})
}

// FullReport renders the entire review set as markdown (what `Y` copies / `--out` writes).
func FullReport(reviews []RepoReview) string {
	var b strings.Builder
	for _, rr := range reviews {
		fmt.Fprintf(&b, "# Review: %s\n\n", rr.Repo)
		if rr.Err != "" {
			fmt.Fprintf(&b, "_Review failed: %s_\n\n", rr.Err)
			continue
		}
		if rr.NoChanges {
			fmt.Fprintf(&b, "_No changes between %s and %s — nothing to review._\n\n", rr.Branch, rr.Base)
			continue
		}
		if rr.Review == nil {
			if rr.RawText != "" {
				b.WriteString(rr.RawText + "\n\n")
			} else {
				b.WriteString("_No findings returned._\n\n")
			}
			continue
		}
		if v := rr.Review.Verdict; v != "" {
			fmt.Fprintf(&b, "**Verdict:** %s\n\n", v)
		}
		if s := strings.TrimSpace(rr.Review.Summary); s != "" {
			b.WriteString(s + "\n\n")
		}
		if n := ProseNotes(rr); n != "" {
			b.WriteString("**Reviewer's notes**\n\n" + n + "\n\n")
		}
		findings := append([]review.Finding(nil), rr.Review.Findings...)
		for i := range findings {
			if findings[i].Repo == "" {
				findings[i].Repo = rr.Repo
			}
		}
		SortFindings(findings)
		if len(findings) == 0 {
			b.WriteString("_No findings._\n\n")
			continue
		}
		for _, f := range findings {
			fmt.Fprintf(&b, "### %s — `%s`\n\n", f.Title, Locator(f))
			b.WriteString(SeverityTag(f) + "\n\n")
			b.WriteString(strings.TrimSpace(f.Comment) + "\n\n")
			if s := strings.TrimSpace(f.CodeSnippet); s != "" {
				fmt.Fprintf(&b, "```\n%s\n```\n\n", s)
			}
			if s := strings.TrimSpace(f.Suggestion); s != "" {
				fmt.Fprintf(&b, "```suggestion\n%s\n```\n\n", s)
			}
		}
	}
	return b.String()
}

// ProseNotes returns the model's free-form narrative when it's materially richer
// than the structured summary. With --json-schema the summary field is short, so
// the detailed assessment often lands in the result text — which is exactly the
// "proof of work" worth showing on a clean review.
func ProseNotes(rr RepoReview) string {
	notes := strings.TrimSpace(rr.RawText)
	if notes == "" || rr.Review == nil {
		return ""
	}
	sum := strings.TrimSpace(rr.Review.Summary)
	if notes == sum || len(notes) < len(sum)+120 {
		return ""
	}
	return notes
}
