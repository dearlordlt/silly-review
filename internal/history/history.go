// Package history persists the last review per repo+branch (and the last
// health check per repo+ref+category+scope) so a re-run can continue from it
// (confirm fixes, only re-raise what still stands) instead of starting from
// scratch. Stored under ~/.config/silly-review/reviews/, one file per key —
// never inside the user's repo.
package history

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"silly-review/internal/checks"
	"silly-review/internal/review"
)

// Entry is a saved review for one repo+branch.
type Entry struct {
	Repo   string        `json:"repo"`
	Branch string        `json:"branch"`
	Base   string        `json:"base"`
	When   time.Time     `json:"when"`
	Review review.Review `json:"review"`
}

func reviewsDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "silly-review", "reviews"), nil
}

func keyFile(dir, repoPath, branchRef string) string {
	h := sha1.Sum([]byte(repoPath + "\x00" + branchRef))
	return filepath.Join(dir, hex.EncodeToString(h[:])+".json")
}

// Save records the review for repoPath@branchRef (atomic write).
func Save(repoPath, branchRef string, e Entry) error {
	dir, err := reviewsDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	f := keyFile(dir, repoPath, branchRef)
	tmp := f + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, f)
}

// Load returns the saved review for repoPath@branchRef, if any.
func Load(repoPath, branchRef string) (*Entry, bool) {
	dir, err := reviewsDir()
	if err != nil {
		return nil, false
	}
	data, err := os.ReadFile(keyFile(dir, repoPath, branchRef))
	if err != nil {
		return nil, false
	}
	var e Entry
	if json.Unmarshal(data, &e) != nil {
		return nil, false
	}
	return &e, true
}

// Has reports whether a prior review exists for repoPath@branchRef.
func Has(repoPath, branchRef string) bool {
	_, ok := Load(repoPath, branchRef)
	return ok
}

// ---- health checks ----

// CheckEntry is a saved health-check report for one repo+ref+category+scope.
type CheckEntry struct {
	Repo     string        `json:"repo"`
	Ref      string        `json:"ref"`
	Category string        `json:"category"`
	Scope    string        `json:"scope"`
	When     time.Time     `json:"when"`
	Report   checks.Report `json:"report"`
}

// checkKey composes the key so checks never collide with reviews of the same
// ref (refs can't contain NUL) and each category+scope has its own slot.
func checkKey(ref, category, scope string) string {
	return ref + "\x00check\x00" + category + "\x00" + scope
}

// SaveCheck records the check report for repoPath@ref under category+scope.
func SaveCheck(repoPath, ref, category, scope string, e CheckEntry) error {
	dir, err := reviewsDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	f := keyFile(dir, repoPath, checkKey(ref, category, scope))
	tmp := f + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, f)
}

// LoadCheck returns the saved check for repoPath@ref under category+scope, if any.
func LoadCheck(repoPath, ref, category, scope string) (*CheckEntry, bool) {
	dir, err := reviewsDir()
	if err != nil {
		return nil, false
	}
	data, err := os.ReadFile(keyFile(dir, repoPath, checkKey(ref, category, scope)))
	if err != nil {
		return nil, false
	}
	var e CheckEntry
	if json.Unmarshal(data, &e) != nil {
		return nil, false
	}
	return &e, true
}

// HasCheck reports whether a prior check exists for repoPath@ref+category+scope.
func HasCheck(repoPath, ref, category, scope string) bool {
	_, ok := LoadCheck(repoPath, ref, category, scope)
	return ok
}
