package history

import (
	"testing"
	"time"

	"silly-review/internal/review"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	if _, ok := Load("/repo", "origin/x"); ok {
		t.Fatal("expected no entry before save")
	}
	e := Entry{
		Repo: "r", Branch: "origin/x", Base: "origin/main", When: time.Now(),
		Review: review.Review{
			Verdict:  "request_changes",
			Summary:  "s",
			Findings: []review.Finding{{File: "a.go", StartLine: 3, Severity: "major", Title: "bug X"}},
		},
	}
	if err := Save("/repo", "origin/x", e); err != nil {
		t.Fatal(err)
	}
	got, ok := Load("/repo", "origin/x")
	if !ok {
		t.Fatal("expected entry after save")
	}
	if got.Review.Verdict != "request_changes" || len(got.Review.Findings) != 1 || got.Review.Findings[0].Title != "bug X" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if !Has("/repo", "origin/x") {
		t.Fatal("Has should be true")
	}
	// Keyed by repo+branch — a different branch (or repo) must not collide.
	if Has("/repo", "origin/y") || Has("/other", "origin/x") {
		t.Fatal("different repo/branch should not match")
	}
}
