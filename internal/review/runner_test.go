package review

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBuildArgsReadOnly(t *testing.T) {
	args := BuildArgs(Options{Model: "opus", System: "sys", Prompt: "p", OtherWorktrees: []string{"/tmp/a"}})
	joined := strings.Join(args, " ")

	for _, want := range []string{
		"--permission-mode dontAsk",
		"--output-format stream-json",
		"--json-schema",
		"--add-dir /tmp/a",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q\ngot: %s", want, joined)
		}
	}
	// Edit/Write must be denied (bare names), never allowed.
	if !contains(args, "Edit") || !contains(args, "Write") {
		t.Errorf("Edit/Write not in disallowed set: %v", disallowedTools)
	}
	di := indexOf(args, "--disallowedTools")
	if di < 0 {
		t.Fatal("no --disallowedTools")
	}
	for _, tool := range []string{"Edit", "Write", "NotebookEdit"} {
		if pos := indexOf(args, tool); pos < di {
			t.Errorf("%s appears before --disallowedTools (would be allowed): pos=%d di=%d", tool, pos, di)
		}
	}
}

// TestRunParsesStructuredOutput drives the full Run path with a stub claude that
// emits canned stream-json, including the read-only-denial signal.
func TestRunParsesStructuredOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stub uses a shell script")
	}
	stub := writeStub(t, stubStream)
	res, err := Run(context.Background(), Options{Model: "opus", BinPath: stub, PrimaryWorktree: t.TempDir()}, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if res.Review == nil {
		t.Fatalf("no structured output parsed; raw=%q", res.RawText)
	}
	if res.Review.Verdict != "request_changes" || len(res.Review.Findings) != 1 {
		t.Fatalf("unexpected review: %+v", res.Review)
	}
	f := res.Review.Findings[0]
	if f.File != "app.go" || f.StartLine != 3 || f.Severity != "major" {
		t.Fatalf("unexpected finding: %+v", f)
	}
	if len(res.PermissionDenials) != 1 || res.PermissionDenials[0] != "Write" {
		t.Fatalf("expected a Write permission denial, got %v", res.PermissionDenials)
	}
}

const stubStream = `{"type":"system","subtype":"init","tools":["Read","Grep"],"model":"opus"}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/x/app.go"}}]}}
{"type":"system","subtype":"api_retry","attempt":1,"error":"overloaded"}
{"type":"assistant","message":{"content":[{"type":"text","text":"Found an issue."}]}}
{"type":"result","subtype":"success","is_error":false,"result":"done","total_cost_usd":0.012,"permission_denials":[{"tool_name":"Write"}],"structured_output":{"summary":"ok","verdict":"request_changes","findings":[{"repo":"work","file":"app.go","start_line":3,"severity":"major","title":"No overflow guard","comment":"Add returns int; consider overflow.","code_snippet":"func Add(a, b int) int { return a + b }"}]}}`

func writeStub(t *testing.T, stream string) string {
	t.Helper()
	dir := t.TempDir()
	// The stub ignores its args and prints the canned stream to stdout.
	data := filepath.Join(dir, "stream.json")
	if err := os.WriteFile(data, []byte(stream), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncat " + data + "\n"
	path := filepath.Join(dir, "testclaude")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func contains(ss []string, v string) bool {
	for _, s := range ss {
		if s == v {
			return true
		}
	}
	return false
}

func indexOf(ss []string, v string) int {
	for i, s := range ss {
		if s == v {
			return i
		}
	}
	return -1
}
