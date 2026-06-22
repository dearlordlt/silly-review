# AGENTS.md — conventions for silly-review

silly-review is a Go TUI that drives the `claude` CLI to produce senior-engineer
code reviews of remote git branches. This file is the contract for anyone (human
or agent) changing the code. silly-review reads it when reviewing itself.

## The one invariant that must never break

**silly-review is read-only with respect to the user's repositories.** It must
never check out, switch, stash, commit, reset, or otherwise mutate the working
tree of a repo the user is sitting in. Branch contents are only ever materialized
through *disposable detached worktrees* in a temp dir (`internal/gitx` →
`Workspace`), which are removed and pruned on exit, including Ctrl-C.

Concretely, when you touch git or the claude invocation:
- Only use read-only git plumbing (`internal/gitx`). Never add a mutating git
  command to a code path that runs against the user's repo.
- The `claude` subprocess runs sandboxed: `--permission-mode dontAsk`, with
  `Edit`/`Write`/`NotebookEdit` and mutating git denied (`internal/review/runner.go`).
  Do not widen `allowedTools` to anything that can write.
- Config and state live under `~/.config/silly-review/` only — never write inside
  a user repo.

If a change could violate this, it needs a test proving the working tree and HEAD
are byte-identical before/after (see `internal/gitx/gitx_test.go`).

## Layout

```
main.go                  cobra entry, flags, headless mode, signal-based cleanup
internal/config          per-repo base + per-folder style/model (XDG json)
internal/gitx            read-only git plumbing + disposable worktree Workspace
internal/discover        single-repo vs multi-repo-root detection
internal/review          claude argv, stream-json parsing, prompt/persona, schema, preflight
internal/render          findings → clipboard block / markdown report
internal/tui             Bubble Tea state machine + per-screen views
```

Keep `gitx`, `review`, `config`, `render` free of TUI imports so they stay
unit-testable and reusable by the `--no-tui` path.

## Style & deps

- Standard library first. Current third-party deps are intentionally minimal:
  Bubble Tea / Bubbles / Lipgloss (TUI), Cobra (CLI), atotto/clipboard. Don't add
  a dependency where stdlib will do.
- `gofmt` clean; `go vet ./...` clean. Match the surrounding code.
- Errors are surfaced to the user with actionable messages, never panics.

## Testing

- `go test ./...` must pass and stay fast/offline.
- Do **not** call the real `claude` in tests. Point `SILLY_REVIEW_CLAUDE` at a
  stub that emits canned stream-json (`internal/review/runner_test.go`).
- `gitx` tests build throwaway repos in `t.TempDir()` (a bare "origin" + a clone).
- Prefer table-driven tests.

## claude invocation notes

- Reviews use `--output-format stream-json --json-schema` — structured findings
  land in the final `result` event's `structured_output`.
- Diff range is merge-base-aware: three-dot `base...head` normally, two-dot
  `base..head` for unrelated histories (orphan/re-rooted branches). The stat path
  and the prompt must agree on this.
- The subprocess env strips the parent Claude Code session's delegated-session
  vars (so a nested invocation authenticates normally) but **keeps** 3P provider
  vars like `CLAUDE_CODE_USE_BEDROCK`. See `reviewEnv` in `internal/review/runner.go`.
