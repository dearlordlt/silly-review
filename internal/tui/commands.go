package tui

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"silly-review/internal/gitx"
	"silly-review/internal/render"
	"silly-review/internal/review"
)

// launchReview starts the review goroutine and returns immediately. Progress
// flows back through events; the model drains it via waitForEvent.
func launchReview(ctx context.Context, ws *gitx.Workspace, picks []*repoPick, style review.Style, model, binPath string, events chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		go runReviews(ctx, ws, picks, style, model, binPath, events)
		return nil
	}
}

func runReviews(ctx context.Context, ws *gitx.Workspace, picks []*repoPick, style review.Style, model, binPath string, events chan tea.Msg) {
	send := func(msg tea.Msg) {
		select {
		case events <- msg:
		case <-ctx.Done():
		}
	}

	// 1. Materialize each branch in a disposable worktree and gather its diff scope.
	type prepared struct {
		pick    *repoPick
		wt      *gitx.Worktree
		rc      review.RepoContext
		statErr error
	}
	var preps []*prepared
	for _, p := range picks {
		send(logMsg{repo: p.repo.Name, text: "preparing read-only worktree…"})
		wt, err := ws.Add(ctx, p.repo, p.branch.Ref)
		if err != nil {
			send(reviewErrMsg{err: fmt.Errorf("%s: creating worktree: %w", p.repo.Name, err)})
			return
		}
		mb, _ := gitx.MergeBase(ctx, p.repo.Path, p.base, p.branch.Ref)
		stat, statErr := gitx.DiffStat(ctx, p.repo.Path, p.base, p.branch.Ref, mb)
		files, _ := gitx.DiffNameStatus(ctx, p.repo.Path, p.base, p.branch.Ref, mb)
		preps = append(preps, &prepared{
			pick:    p,
			wt:      wt,
			statErr: statErr,
			rc: review.RepoContext{
				Name:         p.repo.Name,
				WorktreePath: wt.Path,
				BranchRef:    p.branch.Ref,
				BaseRef:      p.base,
				MergeBase:    mb,
				Stat:         stat,
				Files:        files,
			},
		})
	}

	// 2. Review each repo as primary, with the others mounted for cross-repo context.
	sys := review.SystemPrompt(style)
	var reviews []render.RepoReview
	var totalCost float64
	for i, pr := range preps {
		name := pr.pick.repo.Name
		base := pr.pick.base
		head := pr.pick.branch.Ref

		// Skip a pointless claude call when there's nothing (or no valid diff)
		// to review — base==head, an already-merged branch, or a bad base ref.
		if pr.statErr != nil {
			send(logMsg{repo: name, text: "could not compute diff — skipping"})
			reviews = append(reviews, render.RepoReview{Repo: name, Branch: head, Base: base, Err: "could not compute diff vs " + base + ": " + pr.statErr.Error()})
			continue
		}
		if pr.rc.Stat.Files == 0 {
			send(logMsg{repo: name, text: fmt.Sprintf("no changes vs %s — nothing to review", base)})
			reviews = append(reviews, render.RepoReview{Repo: name, Branch: head, Base: base, NoChanges: true})
			continue
		}

		send(logMsg{repo: name, text: fmt.Sprintf("reviewing %s vs %s…", head, base)})
		var others []review.RepoContext
		var otherPaths []string
		for j, o := range preps {
			if j == i {
				continue
			}
			others = append(others, o.rc)
			otherPaths = append(otherPaths, o.wt.Path)
		}
		opts := review.Options{
			Model:           model,
			System:          sys,
			Prompt:          review.BuildPrompt(pr.rc, others),
			PrimaryWorktree: pr.wt.Path,
			OtherWorktrees:  otherPaths,
			BinPath:         binPath,
		}
		res, err := review.Run(ctx, opts, func(e review.Event) {
			if e.Kind == review.EvtRetry {
				send(retryMsg{text: e.Text})
				return
			}
			send(logMsg{repo: pr.pick.repo.Name, text: e.Text})
		})
		if ctx.Err() != nil {
			return
		}
		rr := render.RepoReview{Repo: name, Branch: head, Base: base}
		switch {
		case err != nil:
			rr.Err = err.Error()
		case res.IsError:
			rr.Err = res.ErrMsg // guaranteed non-empty by review.Run
		default:
			totalCost += res.CostUSD
			rr.Review = res.Review
			rr.RawText = res.RawText
		}
		reviews = append(reviews, rr)
	}

	send(allDoneMsg{reviews: reviews, cost: totalCost})
}
