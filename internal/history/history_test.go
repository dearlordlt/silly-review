package history

import (
	"testing"
	"time"

	"silly-review/internal/checks"
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

func TestCheckSaveLoadRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	if HasCheck("/repo", "main", "security", "auth") {
		t.Fatal("expected no check entry before save")
	}
	e := CheckEntry{
		Repo: "r", Ref: "main", Category: "security", Scope: "auth", When: time.Now(),
		Report: checks.Report{
			Health:   "at_risk",
			Summary:  "s",
			Findings: []checks.Finding{{File: "a.go", StartLine: 3, Severity: "high", Title: "hole", FixPrompt: "fix it"}},
		},
	}
	if err := SaveCheck("/repo", "main", "security", "auth", e); err != nil {
		t.Fatal(err)
	}
	got, ok := LoadCheck("/repo", "main", "security", "auth")
	if !ok {
		t.Fatal("expected check entry after save")
	}
	if got.Report.Health != "at_risk" || len(got.Report.Findings) != 1 || got.Report.Findings[0].FixPrompt != "fix it" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}

	// A check must not collide with a review of the same ref, nor with another
	// category/scope of the same check.
	if Has("/repo", "main") {
		t.Fatal("check entry must not be visible as a review")
	}
	if err := Save("/repo", "main", Entry{Repo: "r", Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	got2, _ := LoadCheck("/repo", "main", "security", "auth")
	if got2 == nil || got2.Report.Health != "at_risk" {
		t.Fatal("saving a review clobbered the check entry")
	}
	if HasCheck("/repo", "main", "security", "general") || HasCheck("/repo", "main", "debt", "auth") {
		t.Fatal("different category/scope should not match")
	}
}
