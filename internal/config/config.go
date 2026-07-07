// Package config persists silly-review's per-repo and per-folder preferences.
//
// The store lives entirely under the user's XDG config dir
// (~/.config/silly-review/config.json) — we never write anything inside the
// user's repositories, so a review can never dirty their working tree.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// RepoConfig holds settings remembered for a single git repository, keyed by
// the repo's absolute top-level path.
type RepoConfig struct {
	// Base is the ref a branch is diffed against, e.g. "origin/dev".
	Base string `json:"base"`
}

// FolderConfig holds settings remembered for a launch folder (the directory the
// user ran silly-review from), keyed by its absolute path.
type FolderConfig struct {
	Style     string   `json:"style,omitempty"`
	Model     string   `json:"model,omitempty"`
	LastRepos []string `json:"lastRepos,omitempty"`
	// Last health-check lens used from this folder (category + scope keys).
	CheckCategory string `json:"checkCategory,omitempty"`
	CheckScope    string `json:"checkScope,omitempty"`
}

// Defaults are the global fallbacks used when a folder has no remembered choice.
type Defaults struct {
	Style string `json:"style"`
	Model string `json:"model"`
}

// Config is the whole on-disk document.
type Config struct {
	Defaults Defaults                `json:"defaults"`
	Repos    map[string]RepoConfig   `json:"repos"`
	Folders  map[string]FolderConfig `json:"folders"`

	mu   sync.Mutex
	path string
}

// DefaultStyle and DefaultModel seed a fresh config.
const (
	DefaultStyle = "thorough"
	DefaultModel = "opus"
)

func configPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "silly-review", "config.json"), nil
}

// Load reads the config, returning an initialized empty config if none exists.
func Load() (*Config, error) {
	path, err := configPath()
	if err != nil {
		return nil, err
	}
	c := &Config{
		Defaults: Defaults{Style: DefaultStyle, Model: DefaultModel},
		Repos:    map[string]RepoConfig{},
		Folders:  map[string]FolderConfig{},
		path:     path,
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, c); err != nil {
		// A corrupt config shouldn't brick the tool; start fresh but keep the path.
		return c, nil
	}
	if c.Repos == nil {
		c.Repos = map[string]RepoConfig{}
	}
	if c.Folders == nil {
		c.Folders = map[string]FolderConfig{}
	}
	if c.Defaults.Style == "" {
		c.Defaults.Style = DefaultStyle
	}
	if c.Defaults.Model == "" {
		c.Defaults.Model = DefaultModel
	}
	return c, nil
}

// Save writes the config atomically (write-temp-then-rename).
func (c *Config) Save() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

// RepoBase returns the configured base ref for a repo path, if any.
func (c *Config) RepoBase(repoPath string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	rc, ok := c.Repos[repoPath]
	if !ok || rc.Base == "" {
		return "", false
	}
	return rc.Base, true
}

// SetRepoBase records the base ref for a repo path.
func (c *Config) SetRepoBase(repoPath, base string) {
	c.mu.Lock()
	c.Repos[repoPath] = RepoConfig{Base: base}
	c.mu.Unlock()
}

// Folder returns the remembered settings for a launch folder.
func (c *Config) Folder(folderPath string) FolderConfig {
	c.mu.Lock()
	defer c.mu.Unlock()
	fc := c.Folders[folderPath]
	if fc.Style == "" {
		fc.Style = c.Defaults.Style
	}
	if fc.Model == "" {
		fc.Model = c.Defaults.Model
	}
	return fc
}

// SetFolder records settings for a launch folder.
func (c *Config) SetFolder(folderPath string, fc FolderConfig) {
	c.mu.Lock()
	c.Folders[folderPath] = fc
	c.mu.Unlock()
}
