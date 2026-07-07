package render

import (
	"strings"
	"testing"

	"silly-review/internal/checks"
)

func TestCheckReportMarkdown(t *testing.T) {
	cr := CheckResult{
		Repo: "backend", Ref: "main", Category: "Security", Scope: "Auth & access control",
		Report: &checks.Report{
			Summary: "Looked at auth end to end.",
			Health:  "needs_attention",
			Findings: []checks.Finding{{
				Repo: "backend", File: "auth.go", StartLine: 42, Severity: "high", Effort: "quick",
				Title: "Missing permission check", Problem: "Endpoint skips the role check.",
				Impact: "Any signed-in user can delete projects.", Solution: "Guard with RequireRole.",
				FixPrompt:   "In auth.go, add a RequireRole(admin) guard to DeleteProject…",
				CodeSnippet: "func DeleteProject(w http.ResponseWriter, r *http.Request) {",
			}},
		},
	}
	out := CheckReportMarkdown(cr)
	for _, want := range []string{
		"# Health check: backend — Security (Auth & access control)",
		"needs attention",
		"Missing permission check", "backend/auth.go:42",
		"[HIGH · quick fix]",
		"**Impact:**", "**Fix:**",
		"Fix prompt", "RequireRole(admin)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q\n%s", want, out)
		}
	}
}

func TestCheckReportMarkdownFailure(t *testing.T) {
	out := CheckReportMarkdown(CheckResult{Repo: "r", Ref: "main", Category: "Security", Scope: "General", Err: "boom"})
	if !strings.Contains(out, "Check failed: boom") {
		t.Fatalf("failed check must say so:\n%s", out)
	}
}

// TestFixPromptFencing: a fix prompt containing ``` must not break out of its
// own code fence in the markdown report.
func TestFixPromptFencing(t *testing.T) {
	f := checks.Finding{
		File: "a.go", StartLine: 1, Severity: "low", Title: "x", Problem: "p", Impact: "i", Solution: "s",
		FixPrompt: "Change the code:\n```go\nfoo()\n```\nthen run tests.",
	}
	block := CheckFindingBlock(f)
	// The wrapping fence must be longer than any fence inside the prompt.
	if !strings.Contains(block, "````") {
		t.Fatalf("expected an extended fence around a prompt containing ```:\n%s", block)
	}
	if strings.Count(block, "````") != 2 {
		t.Fatalf("extended fence should open and close exactly once:\n%s", block)
	}
}

// TestProseNotesSkipsRawJSON: with --json-schema the model's final text is
// often the JSON blob itself — it must never be echoed as auditor's notes.
func TestProseNotesSkipsRawJSON(t *testing.T) {
	rawJSON := `{"findings":[{"file":"a.go","title":"x","problem":"a very long problem description that easily exceeds the prose threshold over the summary field by far more than one hundred and twenty characters in total length"}]}`
	cr := CheckResult{
		Repo: "r", Ref: "main", Category: "Security", Scope: "General",
		Report:  &checks.Report{Summary: "short", Health: "good"},
		RawText: rawJSON,
	}
	if out := CheckReportMarkdown(cr); strings.Contains(out, "Auditor's notes") {
		t.Fatalf("raw JSON must not be rendered as auditor's notes:\n%s", out)
	}
	// Real prose of the same length still shows.
	cr.RawText = strings.ReplaceAll(rawJSON, "{", "(")
	if out := CheckReportMarkdown(cr); !strings.Contains(out, "Auditor's notes") {
		t.Fatal("genuine narrative notes should still be rendered")
	}
}

func TestSortCheckFindings(t *testing.T) {
	fs := []checks.Finding{
		{File: "b.go", StartLine: 2, Severity: "low"},
		{File: "a.go", StartLine: 9, Severity: "critical"},
		{File: "a.go", StartLine: 1, Severity: "critical"},
	}
	SortCheckFindings(fs)
	if fs[0].StartLine != 1 || fs[1].StartLine != 9 || fs[2].Severity != "low" {
		t.Fatalf("bad order: %+v", fs)
	}
}
