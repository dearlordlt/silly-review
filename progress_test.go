package main

import (
	"bytes"
	"strings"
	"testing"

	"silly-review/internal/review"
)

func TestProgressNonTTYPrintsLines(t *testing.T) {
	var buf bytes.Buffer // not an *os.File → treated as non-tty
	p := newProgress(&buf)
	if p.tty {
		t.Fatal("a bytes.Buffer should not be detected as a terminal")
	}
	p.start() // no-op when non-tty
	p.event(review.Event{Text: "reading auth.go"})
	p.event(review.Event{Text: "running: git diff"})
	p.stop()

	out := buf.String()
	if !strings.Contains(out, "reading auth.go") || !strings.Contains(out, "running: git diff") {
		t.Fatalf("expected per-event activity lines, got %q", out)
	}
}

func TestClip(t *testing.T) {
	if got := clip("short", 72); got != "short" {
		t.Fatalf("short string changed: %q", got)
	}
	if got := clip("abcdef", 4); got != "abc…" {
		t.Fatalf("clip = %q, want abc…", got)
	}
}
