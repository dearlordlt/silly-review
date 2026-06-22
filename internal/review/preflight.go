package review

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
)

// Preflight checks that the claude CLI is present and authenticated. It returns
// a user-actionable error otherwise. bin is the claude binary name or path.
func Preflight(ctx context.Context, bin string) error {
	if bin == "" {
		bin = "claude"
	}
	bin, err := exec.LookPath(bin)
	if err != nil {
		return fmt.Errorf("the `claude` CLI was not found on PATH — install Claude Code and sign in first")
	}
	out, err := exec.CommandContext(ctx, bin, "auth", "status").Output()
	if err != nil {
		// Non-JSON or failure: don't hard-block here; a real auth problem will
		// surface as an api_retry/result error during the review with a clear message.
		return nil
	}
	var st struct {
		LoggedIn *bool `json:"loggedIn"`
	}
	if json.Unmarshal(out, &st) != nil {
		return nil // unrecognized format; let the review surface any issue
	}
	// Only block on an explicit loggedIn:false; an absent field means the output
	// shape changed, so stay lenient and let the review surface real auth errors.
	if st.LoggedIn != nil && !*st.LoggedIn {
		return fmt.Errorf("the `claude` CLI is not authenticated — run `claude` once and sign in, then retry")
	}
	return nil
}
