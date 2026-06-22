package tui

import (
	"strings"
	"testing"

	"silly-review/internal/render"
	"silly-review/internal/review"
)

// TestEmptyStateTellsTheTruth verifies the results screen distinguishes failed,
// no-changes, and genuinely-clean reviews instead of always saying "No findings".
func TestEmptyStateTellsTheTruth(t *testing.T) {
	cases := []struct {
		name   string
		rr     render.RepoReview
		want   string
		reject string
	}{
		{
			name: "auth failure",
			rr:   render.RepoReview{Repo: "svc", Branch: "origin/main", Base: "origin/main", Err: "claude failed: Failed to authenticate. API Error: 401"},
			want: "review failed",
		},
		{
			name: "no changes",
			rr:   render.RepoReview{Repo: "svc", Branch: "origin/feat", Base: "origin/main", NoChanges: true},
			want: "no changes between origin/feat and origin/main",
		},
		{
			name: "genuinely clean",
			rr:   render.RepoReview{Repo: "svc", Review: &review.Review{Verdict: "approve"}},
			want: "no findings",
		},
		{
			name:   "prose only (no structured output)",
			rr:     render.RepoReview{Repo: "svc", RawText: "Looks fine overall, a few thoughts..."},
			want:   "no structured findings",
			reject: "🎉",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &Model{reviews: []render.RepoReview{tc.rr}}
			body := strings.ToLower(m.emptyStateBody())
			if !strings.Contains(body, tc.want) {
				t.Errorf("body missing %q\ngot: %s", tc.want, body)
			}
			if tc.reject != "" && strings.Contains(body, strings.ToLower(tc.reject)) {
				t.Errorf("body should not contain %q\ngot: %s", tc.reject, body)
			}
			// A failed review must never be reported as a clean pass.
			if tc.rr.Err != "" && strings.Contains(body, "🎉") {
				t.Errorf("failed review shown as clean: %s", body)
			}
		})
	}

	// The auth case must surface the troubleshooting hint.
	m := &Model{reviews: []render.RepoReview{{Repo: "svc", Err: "401 authentication failed"}}}
	if !strings.Contains(m.emptyStateBody(), "claude -p hi") {
		t.Error("auth failure should include a troubleshooting hint")
	}
}
