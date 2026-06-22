# silly-review

Senior-engineer code reviews of **remote branches**, in your terminal — powered by the `claude` CLI under the hood (your Claude subscription, no API key).

It never touches your working tree. You can be mid-work on your own branch and review a colleague's branch without a single checkout, stash, or branch switch: each branch is materialized in a disposable detached `git worktree` in a temp dir, Claude reviews it read-only, and everything is cleaned up afterward.

> **Status:** early but working, and dogfooding itself. Expect rough edges in the TUI polish; the core (safety, review quality, multi-repo) is solid and tested.

## Why

- **Safe by construction.** No branch switching, no stashing, nothing written into your repos (config lives in `~/.config/silly-review/`). Claude runs in a read-only sandbox (`--permission-mode dontAsk`, `Edit`/`Write` removed entirely) inside a throwaway worktree.
- **Real reviews.** Claude reads your `AGENTS.md` / `CLAUDE.md` / `.claude/`, the surrounding code, and the deps — so comments match your conventions and read like a human senior engineer wrote them, not LLM filler.
- **Multi-repo.** Launch in a folder of repos, select the frontend *and* backend a feature spans, and each review gets the others mounted for cross-repo context.
- **Copy-paste straight into the PR.** Every finding has a `repo/file:line` locator and a verbatim code snippet so you can Ctrl-F to the spot in GitHub/GitLab, plus one-key clipboard copy of a PR-ready comment.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/dearlordlt/silly-review/main/setup.sh | sh
```

This clones, builds, and installs `silly-review` to `~/.local/bin` (override with `INSTALL_DIR=…`, pick a ref with `BRANCH=…`). Re-run the same command any time to **update** — or just run `silly-review update`. Or build from source:

```sh
git clone https://github.com/dearlordlt/silly-review && cd silly-review
go build -o bin/silly-review . && install bin/silly-review ~/.local/bin/
```

Requires Go 1.24+, git, and an authenticated `claude` CLI (install [Claude Code](https://claude.com/claude-code) and run `claude` once to sign in). Optional: `alias sr=silly-review`.

## Use

Interactive (the normal way):

```sh
cd ~/code/my-service      # a single repo
silly-review

cd ~/code/acme            # a folder of repos (frontend, backend, deploy…)
silly-review              # multi-select the repos a feature spans
```

Flow: pick repo(s) → pick the remote branch to review → (first time per repo) set the base branch it’s diffed against → pick a style + model → watch progress → browse findings.

**Results keys:** `↑/↓` navigate · `y` copy the selected comment · `Y` copy the whole review · `f` filter by severity · `q` quit.

The base branch (e.g. `origin/dev` vs `origin/main`) is asked once per repo and remembered; change it later with `c` on the branch screen. Style/model are remembered per folder.

### Headless / CI

```sh
cd ~/code/my-service
silly-review --no-tui --branch feat/login --model sonnet            # markdown to stdout
silly-review --no-tui --branch feat/login --json                    # structured JSON
silly-review --no-tui --branch feat/login --out review.md           # also write a file
silly-review --no-tui --branch feat/login --base origin/release-3   # explicit base
```

### Other

```sh
silly-review update        # update this install in place to the latest version
silly-review config        # show saved per-repo base + per-folder style/model
silly-review --no-fetch    # skip the `git fetch` before listing branches
```

## How it works

1. **Discover** — is cwd a repo, or a parent of repos?
2. **Worktree** — `git worktree add --detach <tmp> origin/<branch>` (your HEAD/index untouched).
3. **Diff scope** — three-dot `base...branch` (PR semantics) for stats + changed files.
4. **Review** — `claude -p` with `--output-format stream-json --json-schema …` in a read-only tool sandbox, cwd set to the worktree so project conventions are auto-discovered; other selected repos are mounted via `--add-dir`.
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

