package review

import (
	"strings"
	"testing"
)

// TestReviewEnvStripsSessionVars guards the fix for nested-session 401s: the
// claude subprocess must not inherit the parent Claude Code session's vars.
func TestReviewEnvStripsSessionVars(t *testing.T) {
	// Session/delegation markers that must be stripped.
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("CLAUDE_CODE_CHILD_SESSION", "1")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "abc")
	t.Setenv("CLAUDE_CODE_OAUTH_SCOPES", "user:inference")
	// Things that must survive: normal env, API config, and 3P provider selection.
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("ANTHROPIC_BASE_URL", "https://api.anthropic.com")
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "1")
	// A stale inherited copy of the var we set ourselves must be de-duplicated.
	t.Setenv("CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD", "0")

	env := reviewEnv()
	var sawPath, sawBase, sawBedrock, addDirCount int
	for _, kv := range env {
		for stripped := range strippedEnvVars {
			if strings.HasPrefix(kv, stripped+"=") {
				t.Errorf("session var leaked into review env: %s", kv)
			}
		}
		switch {
		case kv == "PATH=/usr/bin":
			sawPath++
		case kv == "ANTHROPIC_BASE_URL=https://api.anthropic.com":
			sawBase++
		case kv == "CLAUDE_CODE_USE_BEDROCK=1":
			sawBedrock++
		case strings.HasPrefix(kv, "CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD="):
			addDirCount++
			if kv != "CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD=1" {
				t.Errorf("expected our value, got %s", kv)
			}
		}
	}
	if sawPath == 0 || sawBase == 0 {
		t.Errorf("non-session vars must survive (PATH=%d ANTHROPIC_BASE_URL=%d)", sawPath, sawBase)
	}
	if sawBedrock == 0 {
		t.Error("provider-selection var CLAUDE_CODE_USE_BEDROCK must survive")
	}
	if addDirCount != 1 {
		t.Errorf("CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD must appear exactly once, got %d", addDirCount)
	}
}
