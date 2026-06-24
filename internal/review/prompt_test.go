package review

import (
	"strings"
	"testing"
)

func TestBuildPromptPrior(t *testing.T) {
	rc := RepoContext{Name: "r", WorktreePath: "/wt", BranchRef: "origin/x", BaseRef: "origin/main", MergeBase: "abc"}

	if out := BuildPrompt(rc, nil, nil); strings.Contains(out, "PREVIOUS REVIEW") {
		t.Fatal("no prior should mean no previous-review section")
	}

	prior := &Review{
		Verdict:  "request_changes",
		Summary:  "earlier pass",
		Findings: []Finding{{File: "a.go", StartLine: 3, Severity: "major", Title: "missing guard"}},
	}
	out := BuildPrompt(rc, nil, prior)
	for _, want := range []string{"PREVIOUS REVIEW", "missing guard", "a.go:3", "request_changes"} {
		if !strings.Contains(out, want) {
			t.Errorf("prior section missing %q\n%s", want, out)
		}
	}
}
