// Package discover decides whether silly-review was launched inside a single
// repo or in a parent folder containing several child repos.
package discover

import (
	"context"
	"os"
	"path/filepath"
	"sort"

	"silly-review/internal/gitx"
)

// Mode is the launch context.
type Mode int

const (
	// Single means cwd is itself a git repo.
	Single Mode = iota
	// Multi means cwd holds one or more child git repos.
	Multi
)

// Result describes what was found at the launch location.
type Result struct {
	Mode  Mode
	Root  string
	Repos []*gitx.Repo
}

// Discover inspects cwd. If cwd is a repo, returns Single mode with that repo.
// Otherwise it scans immediate children for repos and returns Multi mode.
func Discover(ctx context.Context, cwd string) (*Result, error) {
	if gitx.IsRepo(ctx, cwd) {
		repo, err := repoAt(ctx, cwd)
		if err != nil {
			return nil, err
		}
		return &Result{Mode: Single, Root: repo.Path, Repos: []*gitx.Repo{repo}}, nil
	}

	entries, err := os.ReadDir(cwd)
	if err != nil {
		return nil, err
	}
	var repos []*gitx.Repo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		child := filepath.Join(cwd, e.Name())
		if !gitx.IsRepo(ctx, child) {
			continue
		}
		repo, err := repoAt(ctx, child)
		if err != nil {
			continue
		}
		repos = append(repos, repo)
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].Name < repos[j].Name })
	return &Result{Mode: Multi, Root: cwd, Repos: repos}, nil
}

func repoAt(ctx context.Context, dir string) (*gitx.Repo, error) {
	top, err := gitx.Toplevel(ctx, dir)
	if err != nil {
		return nil, err
	}
	remote, _ := gitx.PickRemote(ctx, top) // may be "" if no remotes; surfaced later
	return &gitx.Repo{Name: filepath.Base(top), Path: top, Remote: remote}, nil
}
