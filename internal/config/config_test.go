package config

import (
	"testing"
)

func TestRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Defaults.Style != DefaultStyle || c.Defaults.Model != DefaultModel {
		t.Fatalf("bad defaults: %+v", c.Defaults)
	}

	c.SetRepoBase("/abs/backend", "origin/dev")
	c.SetFolder("/abs/parent", FolderConfig{Style: "security", Model: "sonnet", LastRepos: []string{"a", "b"}})
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}

	c2, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if base, ok := c2.RepoBase("/abs/backend"); !ok || base != "origin/dev" {
		t.Fatalf("repo base not persisted: %q %v", base, ok)
	}
	fc := c2.Folder("/abs/parent")
	if fc.Style != "security" || fc.Model != "sonnet" || len(fc.LastRepos) != 2 {
		t.Fatalf("folder config not persisted: %+v", fc)
	}
	// A folder with no entry falls back to defaults.
	if def := c2.Folder("/unknown"); def.Style != DefaultStyle || def.Model != DefaultModel {
		t.Fatalf("fallback defaults wrong: %+v", def)
	}
}
