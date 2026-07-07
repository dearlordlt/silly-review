package checks

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSchemaIsValidJSON(t *testing.T) {
	var v map[string]any
	if err := json.Unmarshal([]byte(SchemaJSON), &v); err != nil {
		t.Fatalf("SchemaJSON is not valid JSON: %v", err)
	}
	req, _ := v["required"].([]any)
	if len(req) != 3 {
		t.Fatalf("expected 3 required top-level fields, got %v", req)
	}
}

func TestCategoriesWellFormed(t *testing.T) {
	if len(Categories) < 3 {
		t.Fatalf("expected at least the 3 core categories, got %d", len(Categories))
	}
	seen := map[string]bool{}
	for _, c := range Categories {
		if c.Key == "" || c.Name == "" || c.Focus == "" {
			t.Fatalf("category %+v missing key/name/focus", c)
		}
		if seen[c.Key] {
			t.Fatalf("duplicate category key %q", c.Key)
		}
		seen[c.Key] = true
		if len(c.Scopes) < 1 || c.Scopes[0].Key != "general" {
			t.Fatalf("category %s must have a leading general scope, got %+v", c.Key, c.Scopes)
		}
		sseen := map[string]bool{}
		for _, s := range c.Scopes {
			if s.Key == "" || s.Name == "" || s.Addendum == "" {
				t.Fatalf("scope %+v of %s missing key/name/addendum", s, c.Key)
			}
			if sseen[s.Key] {
				t.Fatalf("duplicate scope key %q in %s", s.Key, c.Key)
			}
			sseen[s.Key] = true
		}
	}
	// The three the user asked for by name must exist.
	for _, key := range []string{"security", "debt", "performance"} {
		if _, ok := CategoryByKey(key); !ok {
			t.Fatalf("core category %q missing", key)
		}
	}
}

func TestLookups(t *testing.T) {
	if _, ok := CategoryByKey("nope"); ok {
		t.Fatal("unknown category should not resolve")
	}
	sec, _ := CategoryByKey("security")
	if s := ScopeByKey(sec, "auth"); s.Key != "auth" {
		t.Fatalf("ScopeByKey(auth) = %+v", s)
	}
	if s := ScopeByKey(sec, "bogus"); s.Key != "general" {
		t.Fatalf("unknown scope should fall back to general, got %+v", s)
	}
}

func TestSystemPromptCombinesLensAndScope(t *testing.T) {
	sec, _ := CategoryByKey("security")
	scope := ScopeByKey(sec, "auth")
	sys := SystemPrompt(sec, scope)
	for _, want := range []string{"fix_prompt", "read-only", sec.Focus, scope.Addendum} {
		if !strings.Contains(sys, want) {
			t.Errorf("system prompt missing %q", want)
		}
	}
	// No target count: the persona must demand every real issue, like reviews do.
	if !strings.Contains(sys, "EVERY real issue") {
		t.Error("persona must demand every real issue, no curated handful")
	}
}

func TestBuildPromptPrior(t *testing.T) {
	cx := Context{RepoName: "r", WorktreePath: "/wt", Ref: "feat/x"}
	cat, _ := CategoryByKey("performance")
	scope := ScopeByKey(cat, "db")

	out := BuildPrompt(cx, cat, scope, nil)
	for _, want := range []string{"/wt", "feat/x", "Performance", "Database & queries", "fix_prompt", `"r"`} {
		if !strings.Contains(out, want) {
			t.Errorf("prompt missing %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "PREVIOUS CHECK") {
		t.Fatal("no prior should mean no previous-check section")
	}

	prior := &Report{
		Health:  "at_risk",
		Summary: "earlier pass",
		Findings: []Finding{
			{File: "db.go", StartLine: 12, Severity: "high", Title: "N+1 in ListUsers"},
		},
	}
	out = BuildPrompt(cx, cat, scope, prior)
	for _, want := range []string{"PREVIOUS CHECK", "N+1 in ListUsers", "db.go:12", "at_risk", "earlier pass"} {
		if !strings.Contains(out, want) {
			t.Errorf("prior section missing %q\n%s", want, out)
		}
	}
}

func TestSeverityRankAndHealthLabel(t *testing.T) {
	order := []string{"critical", "high", "medium", "low", "info"}
	for i := 1; i < len(order); i++ {
		if SeverityRank(order[i-1]) >= SeverityRank(order[i]) {
			t.Fatalf("%s should rank above %s", order[i-1], order[i])
		}
	}
	if SeverityRank("junk") <= SeverityRank("info") {
		t.Fatal("unknown severity should rank last")
	}
	if HealthLabel("needs_attention") != "needs attention" {
		t.Fatalf("HealthLabel wrong: %q", HealthLabel("needs_attention"))
	}
}
