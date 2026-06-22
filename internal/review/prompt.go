package review

import (
	"fmt"
	"strings"

	"silly-review/internal/gitx"
)

// Style is a selectable review preset.
type Style struct {
	Key      string
	Name     string
	Desc     string
	Addendum string // appended to the base persona
}

// Styles are the presets shown in the picker. Order matters (first = default-ish).
var Styles = []Style{
	{
		Key:  "thorough",
		Name: "Thorough PR review",
		Desc: "Correctness, design, edge cases, naming, tests. The default high-signal review.",
		Addendum: "Do a complete review: correctness, edge cases, design, readability, naming, error handling, and test coverage. " +
			"Include a few genuine praise notes where the code is notably good, but keep them rare and specific.",
	},
	{
		Key:  "blocking",
		Name: "Blocking-only / quick",
		Desc: "Only what should block merge — bugs, regressions, security. Skips nitpicks.",
		Addendum: "Only report issues that should block the merge: real bugs, regressions, data loss, security holes, broken contracts. " +
			"Do NOT report style, naming, or nitpicks. If nothing blocks, say so plainly and return an empty findings list.",
	},
	{
		Key:  "architecture",
		Name: "Architecture & design",
		Desc: "Structure, boundaries, abstractions, coupling — less line-level nit.",
		Addendum: "Focus on architecture and design: module boundaries, coupling, abstractions, data flow, API shape, and whether this change " +
			"fits the existing structure. Skip line-level style nits unless they signal a design problem.",
	},
	{
		Key:  "security",
		Name: "Security-focused",
		Desc: "Auth, input validation, injection, secrets, data exposure.",
		Addendum: "Review through a security lens: authentication/authorization, input validation, injection (SQL/command/path), SSRF, " +
			"secrets handling, unsafe deserialization, and data exposure. Flag anything that widens the attack surface, with the concrete risk.",
	},
}

// StyleByKey returns the style with the given key, falling back to the first.
func StyleByKey(key string) Style {
	for _, s := range Styles {
		if s.Key == key {
			return s
		}
	}
	return Styles[0]
}

const basePersona = `You are a staff-level software engineer doing a focused review of a colleague's pull request.

Write the way a sharp human reviewer writes:
- Direct and specific. No preamble, no "Overall this looks great, but...", no AI throat-clearing, no flattery.
- Every comment must earn its place. A handful of high-signal comments beats a wall of nitpicks. If something is fine, say nothing about it.
- Comment only on the changed lines and their direct consequences — not pre-existing code unless the change makes it newly wrong.
- Respect the project's own conventions. Read AGENTS.md, CLAUDE.md, and .claude/ if present, and match the surrounding code's style; do not impose your own preferences over an established pattern.
- For each finding give: the exact file and 1-based line number on the NEW side of the diff, a verbatim code_snippet of that line (so the reviewer can Ctrl-F to it in the web PR), a clear comment, an honest severity, and a concrete suggestion when the fix is obvious.
- Comments must be copy-paste ready for a GitHub/GitLab review. Write them as you'd post them, in plain markdown.

You have read-only tools: use git log/show/diff/blame, plus Read/Grep/Glob, to inspect the change and the code around it. You cannot and must not edit anything.`

// SystemPrompt builds the --append-system-prompt text for the chosen style.
func SystemPrompt(style Style) string {
	return basePersona + "\n\nReview emphasis for this pass:\n" + style.Addendum
}

// RepoContext describes one repo being reviewed for prompt construction.
type RepoContext struct {
	Name         string
	WorktreePath string
	BranchRef    string // e.g. origin/feat/x
	BaseRef      string // e.g. origin/main
	MergeBase    string // sha, or "" if unrelated histories
	Stat         gitx.Stat
	Files        []gitx.FileChange
}

// BuildPrompt produces the -p prompt. We hand claude the scope and let it pull
// the actual diff/files itself via its read-only tools (keeps the prompt small
// and lets it read full context, not just the patch).
func BuildPrompt(primary RepoContext, others []RepoContext) string {
	var b strings.Builder
	b.WriteString("Review the changes on the following branch(es). Produce findings that conform to the provided JSON schema.\n\n")

	writeRepo := func(rc RepoContext, isPrimary bool) {
		role := "ADDITIONAL repo (available for cross-repo context)"
		if isPrimary {
			role = "PRIMARY repo to review"
		}
		fmt.Fprintf(&b, "## %s: %s\n", role, rc.Name)
		fmt.Fprintf(&b, "- worktree (checked out at the branch, read-only): %s\n", rc.WorktreePath)
		fmt.Fprintf(&b, "- branch: %s\n- base: %s\n", rc.BranchRef, rc.BaseRef)
		diffRange := rc.BaseRef + "..." + rc.BranchRef
		if rc.MergeBase == "" {
			diffRange = rc.BaseRef + ".." + rc.BranchRef
			b.WriteString("- NOTE: unrelated histories; using two-dot diff range.\n")
		}
		fmt.Fprintf(&b, "- diff range: %s (%d files, +%d/-%d)\n", diffRange, rc.Stat.Files, rc.Stat.Additions, rc.Stat.Deletions)
		fmt.Fprintf(&b, "- inspect with: `cd %s && git diff %s` (also git show/log/blame, Read, Grep)\n", rc.WorktreePath, diffRange)
		if len(rc.Files) > 0 {
			b.WriteString("- changed files:\n")
			limit := 60
			for i, f := range rc.Files {
				if i >= limit {
					fmt.Fprintf(&b, "    ... and %d more\n", len(rc.Files)-limit)
					break
				}
				fmt.Fprintf(&b, "    %s\t%s\n", f.Status, f.Path)
			}
		}
		b.WriteString("\n")
	}

	writeRepo(primary, true)
	for _, o := range others {
		writeRepo(o, false)
	}

	b.WriteString("Instructions:\n")
	b.WriteString("- Read the worktree's AGENTS.md / CLAUDE.md / .claude/ and match its conventions.\n")
	fmt.Fprintf(&b, "- Set each finding's `repo` to the repo name it belongs to (primary is %q).\n", primary.Name)
	b.WriteString("- `start_line` is the 1-based line in the NEW version of the file; `code_snippet` is that exact line verbatim.\n")
	b.WriteString("- Prefer reading the full files in the worktree over trusting the diff in isolation.\n")
	b.WriteString("- For very large changes, prioritize the highest-risk files first.\n")
	return b.String()
}
