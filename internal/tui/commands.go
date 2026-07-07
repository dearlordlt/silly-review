package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"silly-review/internal/checks"
	"silly-review/internal/gitx"
	"silly-review/internal/history"
	"silly-review/internal/render"
	"silly-review/internal/review"
)

// launchReview starts the review goroutine and returns immediately. Progress
// flows back through events; the model drains it via waitForEvent.
func launchReview(ctx context.Context, ws *gitx.Workspace, picks []*repoPick, style review.Style, model, binPath string, continuePrior bool, events chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		go runReviews(ctx, ws, picks, style, model, binPath, continuePrior, events)
		return nil
	}
}

func runReviews(ctx context.Context, ws *gitx.Workspace, picks []*repoPick, style review.Style, model, binPath string, continuePrior bool, events chan tea.Msg) {
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

		var prior *review.Review
		if continuePrior {
			if e, ok := history.Load(pr.pick.repo.Path, head); ok {
				prior = &e.Review
				send(logMsg{repo: name, text: "continuing from the previous review…"})
			}
		}
		if prior == nil {
			send(logMsg{repo: name, text: fmt.Sprintf("reviewing %s vs %s…", head, base)})
		}
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
			Prompt:          review.BuildPrompt(pr.rc, others, prior),
			PrimaryWorktree: pr.wt.Path,
			OtherWorktrees:  otherPaths,
			BinPath:         binPath,
		}
		res, err := review.RunWithResume(ctx, opts, func(e review.Event) {
			switch e.Kind {
			case review.EvtRetry:
				send(retryMsg{text: e.Text})
			case review.EvtThinking:
				send(thinkMsg{text: e.Text})
			default:
				send(logMsg{repo: pr.pick.repo.Name, text: e.Text})
			}
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
			// Save the review so the next pass can continue from it.
			if res.Review != nil {
				_ = history.Save(pr.pick.repo.Path, head, history.Entry{
					Repo: name, Branch: head, Base: base, When: time.Now(), Review: *res.Review,
				})
			}
		}
		reviews = append(reviews, rr)
	}

	send(allDoneMsg{reviews: reviews, cost: totalCost})
}

// launchCheck starts the health-check goroutine and returns immediately.
func launchCheck(ctx context.Context, ws *gitx.Workspace, pick *repoPick, cat checks.Category, scope checks.Scope, model, binPath string, continuePrior bool, events chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		go runCheck(ctx, ws, pick, cat, scope, model, binPath, continuePrior, events)
		return nil
	}
}

func runCheck(ctx context.Context, ws *gitx.Workspace, pick *repoPick, cat checks.Category, scope checks.Scope, model, binPath string, continuePrior bool, events chan tea.Msg) {
	send := func(msg tea.Msg) {
		select {
		case events <- msg:
		case <-ctx.Done():
		}
	}
	name := pick.repo.Name
	ref := pick.branch.Ref

	send(logMsg{repo: name, text: "preparing read-only worktree…"})
	wt, err := ws.Add(ctx, pick.repo, ref)
	if err != nil {
		send(reviewErrMsg{err: fmt.Errorf("%s: creating worktree: %w", name, err)})
		return
	}

	var prior *checks.Report
	if continuePrior {
		if e, ok := history.LoadCheck(pick.repo.Path, ref, cat.Key, scope.Key); ok {
			prior = &e.Report
			send(logMsg{repo: name, text: "continuing from the previous check…"})
		}
	}
	if prior == nil {
		send(logMsg{repo: name, text: fmt.Sprintf("checking %s — %s (%s)…", ref, cat.Name, scope.Name)})
	}

	opts := review.Options{
		Model:           model,
		System:          checks.SystemPrompt(cat, scope),
		Prompt:          checks.BuildPrompt(checks.Context{RepoName: name, WorktreePath: wt.Path, Ref: ref}, cat, scope, prior),
		Schema:          checks.SchemaJSON,
		PrimaryWorktree: wt.Path,
		BinPath:         binPath,
	}
	res, err := review.RunWithResume(ctx, opts, func(e review.Event) {
		switch e.Kind {
		case review.EvtRetry:
			send(retryMsg{text: e.Text})
		case review.EvtThinking:
			send(thinkMsg{text: e.Text})
		default:
			send(logMsg{repo: name, text: e.Text})
		}
	})
	if ctx.Err() != nil {
		return
	}

	cr := render.CheckResult{Repo: name, Ref: ref, Category: cat.Name, Scope: scope.Name}
	var cost float64
	switch {
	case err != nil:
		cr.Err = err.Error()
	case res.IsError:
		cr.Err = res.ErrMsg // guaranteed non-empty by review.Run
	default:
		cost = res.CostUSD
		cr.RawText = res.RawText
		if len(res.Structured) > 0 {
			var rep checks.Report
			if json.Unmarshal(res.Structured, &rep) == nil {
				cr.Report = &rep
				// Save so the next pass can continue from it (clean results only —
				// the error branches above never reach here).
				_ = history.SaveCheck(pick.repo.Path, ref, cat.Key, scope.Key, history.CheckEntry{
					Repo: name, Ref: ref, Category: cat.Key, Scope: scope.Key, When: time.Now(), Report: rep,
				})
			}
		}
	}
	send(checkDoneMsg{res: cr, cost: cost})
}
