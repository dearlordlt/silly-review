# silly-review

Senior-engineer **code reviews** of git branches and whole-codebase **health checks** (security, tech debt, performance, …), in your terminal — powered by the `claude` CLI under the hood (your Claude subscription, no API key).

It never touches your working tree. You can be mid-work on your own branch and review a colleague's branch without a single checkout, stash, or branch switch: each branch is materialized in a disposable detached `git worktree` in a temp dir, Claude works on it read-only, and everything is cleaned up afterward.

> **Status:** early but working, and dogfooding itself. Expect rough edges in the TUI polish; the core (safety, review quality, multi-repo) is solid and tested.

## Why

- **Safe by construction.** No branch switching, no stashing, nothing written into your repos (config lives in `~/.config/silly-review/`). Claude runs in a read-only sandbox (`--permission-mode dontAsk`, `Edit`/`Write` removed entirely) inside a throwaway worktree.
- **Real reviews.** Claude reads your `AGENTS.md` / `CLAUDE.md` / `.claude/`, the surrounding code, and the deps — so comments match your conventions and read like a human senior engineer wrote them, not LLM filler.
- **Multi-repo.** Launch in a folder of repos, select the frontend *and* backend a feature spans, and each review gets the others mounted for cross-repo context.
- **Copy-paste straight into the PR.** Every finding has a `repo/file:line` locator and a verbatim code snippet so you can Ctrl-F to the spot in GitHub/GitLab, plus one-key clipboard copy of a PR-ready comment.
- **Health checks, not just reviews.** Audit the codebase as it stands — security, tech debt, performance, tests, resilience, dependencies, observability — and every finding ships with a **self-contained fix prompt** you can paste straight into Claude Code or Cursor to get it fixed.

## Install

**macOS / Linux:**

```sh
curl -fsSL https://raw.githubusercontent.com/dearlordlt/silly-review/main/setup.sh | sh
```

**Windows (PowerShell):**

```powershell
irm https://raw.githubusercontent.com/dearlordlt/silly-review/main/setup.ps1 | iex
```

The installer clones, builds, and installs `silly-review` (to `~/.local/bin`, or `%LOCALAPPDATA%\silly-review\bin` on Windows — override with `INSTALL_DIR`). Re-run it any time to **update**, or run `silly-review update`.

It needs **git**. If a new-enough **Go** (1.24+) isn't found it downloads the official toolchain to a private dir (`~/.local/share/silly-review/go`) — no `sudo`/admin, exact version, removed by deleting that dir. You also need the **`claude`** CLI signed in ([Claude Code](https://claude.com/claude-code)).

Build from source instead:

```sh
git clone https://github.com/dearlordlt/silly-review && cd silly-review
go build -o bin/silly-review . && install bin/silly-review ~/.local/bin/
```

Optional: `alias sr=silly-review`.

## Use

Interactive (the normal way):

```sh
cd ~/code/my-service      # a single repo
silly-review

cd ~/code/acme            # a folder of repos (frontend, backend, deploy…)
silly-review              # multi-select the repos a feature spans
```

The first screen asks what to do: **review a branch** or **check the codebase**.

**Review flow:** pick repo(s) → pick the branch to review → (first time per repo) set the base branch it’s diffed against → pick a style + model → watch progress → browse findings.

In multi-repo mode, after you pick the first repo's branch silly-review checks the other selected repos for a branch with the same name and offers to **review it too**, **skip that repo**, or **pick a branch manually** — so a frontend-only change doesn't make you slog through (or restart) the backend picker.

**Results keys:** `↑/↓` navigate · `y` copy the selected comment · `Y` copy the whole review · `f` filter by severity · `q` quit.

The base branch (e.g. `origin/dev` vs `origin/main`) is asked once per repo and remembered; change it later with `c` on the branch screen. Style/model are remembered per folder.

### Health checks

A health check audits the **whole codebase at a ref** (your current branch by default — unpushed work included — or main/master/anything) instead of a diff. Pick a lens and how broad to go:

| Lens | Narrower scopes |
|---|---|
| **Security** | auth & access control · injection & input · secrets & config |
| **Tech debt** | dead code & duplication · complexity hotspots · outdated patterns · TODO/FIXME audit |
| **Performance** | database & queries · memory · network & I/O · frontend runtime |
| **Tests & coverage** | critical-path gaps · test quality |
| **Error handling & resilience** | swallowed errors · timeouts & retries · cleanup & shutdown |
| **Dependency health** | vulnerable & abandoned · bloat & duplication |
| **Observability** | logging · metrics & tracing |

Every finding comes with the problem, its concrete impact, a suggested fix, **and a self-contained fix prompt**: press `y` and paste it into Claude Code / Cursor — the prompt carries all the context the agent needs, so it works in a fresh session.

**Check results keys:** `y` copy fix prompt · `c` copy the whole finding · `Y` copy the full report · `f` filter by severity.

Re-running the same check later offers to **continue from the previous one** — confirming what you've fixed and only re-raising what still stands.

### Headless / CI

```sh
cd ~/code/my-service
silly-review --no-tui --branch feat/login --model sonnet            # markdown to stdout
silly-review --no-tui --branch feat/login --json                    # structured JSON
silly-review --no-tui --branch feat/login --out review.md           # also write a file
silly-review --no-tui --branch feat/login --base origin/release-3   # explicit base

silly-review check --list                                           # all lenses + scopes
silly-review check --category security                              # audit the current branch
silly-review check --category performance --scope db --branch main  # narrowed, explicit ref
silly-review check --category debt --json --out debt.md             # JSON to stdout + file
```

### Other

```sh
silly-review update        # update this install in place to the latest version
silly-review config        # show saved per-repo base + per-folder style/model
silly-review --no-fetch    # skip the `git fetch` before listing branches
```

## How it works

1. **Discover** — is cwd a repo, or a parent of repos?
2. **Worktree** — `git worktree add --detach <tmp> <ref>` (your HEAD/index untouched).
3. **Scope** — reviews diff three-dot `base...branch` (PR semantics); health checks take the whole tree at the ref.
4. **Run** — `claude -p` with `--output-format stream-json --json-schema …` in a read-only tool sandbox, cwd set to the worktree so project conventions are auto-discovered; other selected repos are mounted via `--add-dir`.
5. **Render** — the validated `structured_output` becomes the scrollable findings list and the clipboard/markdown output.
6. **Cleanup** — every worktree is removed and pruned on exit, including Ctrl-C.

`SILLY_REVIEW_CLAUDE=/path/to/claude` overrides the binary (used by the test stub).

## Development

```sh
go build -o bin/silly-review .
go test ./...        # fast, offline — uses a stub claude, never the real API
go vet ./...
```

See [AGENTS.md](AGENTS.md) for project conventions and the one invariant that must never break (read-only with respect to your repos).

## License

[MIT](LICENSE) © Šarūnas ([@dearlordlt](https://github.com/dearlordlt))

The name is a joke; the tool is serious. 🙂

