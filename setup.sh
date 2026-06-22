#!/bin/sh
# silly-review installer — builds from source and installs the binary.
#
#   curl -fsSL https://raw.githubusercontent.com/dearlordlt/silly-review/main/setup.sh | sh
#
# Requires git and Go 1.24+. silly-review also needs the `claude` CLI at runtime.
#
# Env overrides:
#   INSTALL_DIR  where to put the binary   (default: ~/.local/bin)
#   BRANCH       branch/tag to install     (default: main)
#   REPO_URL     repo to clone             (default: dearlordlt/silly-review)
set -eu

REPO_URL="${REPO_URL:-https://github.com/dearlordlt/silly-review}"
BRANCH="${BRANCH:-main}"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"
BIN="silly-review"

info() { printf '\033[36m==>\033[0m %s\n' "$1"; }
warn() { printf '\033[33mwarning:\033[0m %s\n' "$1" >&2; }
die()  { printf '\033[31merror:\033[0m %s\n' "$1" >&2; exit 1; }

command -v git >/dev/null 2>&1 || die "git is required — install it and re-run."
command -v go  >/dev/null 2>&1 || die "Go 1.24+ is required to build silly-review — install it from https://go.dev/dl/ and re-run."

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM

info "Cloning $REPO_URL ($BRANCH)…"
git clone --quiet --depth 1 --branch "$BRANCH" "$REPO_URL" "$tmp/src" \
  || die "clone failed — check the repo URL and branch."

info "Building $BIN (this fetches Go deps the first time)…"
( cd "$tmp/src" && go build -o "$tmp/$BIN" . ) || die "build failed."

# Note the currently-installed version (if any) so we can report install vs update.
old=""
if [ -x "$INSTALL_DIR/$BIN" ]; then
  old="$("$INSTALL_DIR/$BIN" --version 2>/dev/null | awk 'NF{print $NF; exit}')"
fi
new="$("$tmp/$BIN" --version 2>/dev/null | awk 'NF{print $NF; exit}')"
[ -n "$new" ] || new="(unknown)"

mkdir -p "$INSTALL_DIR" || die "cannot create $INSTALL_DIR"
# Install via temp file + atomic rename: replacing a *running* binary by
# truncating it fails with ETXTBSY on Linux, but renaming over it is fine.
dst="$INSTALL_DIR/$BIN"
tmpdst="$INSTALL_DIR/.$BIN.new.$$"
( cp "$tmp/$BIN" "$tmpdst" && chmod 0755 "$tmpdst" && mv -f "$tmpdst" "$dst" ) \
  || { rm -f "$tmpdst" 2>/dev/null || true; die "cannot write to $INSTALL_DIR"; }

if [ -z "$old" ]; then
  info "Installed silly-review $new → $dst"
elif [ "$old" = "$new" ]; then
  info "Already up to date (silly-review $new)."
else
  info "Updated silly-review $old → $new"
fi

# PATH guidance
case ":${PATH:-}:" in
  *":$INSTALL_DIR:"*) : ;;
  *)
    warn "$INSTALL_DIR is not on your PATH. Add this to your shell profile (~/.zshrc, ~/.bashrc):"
    printf '    export PATH="%s:$PATH"\n' "$INSTALL_DIR" >&2
    ;;
esac

# Runtime prerequisite
if ! command -v claude >/dev/null 2>&1; then
  warn "the 'claude' CLI was not found. silly-review needs it (and you must be signed in)."
  warn "Install Claude Code from https://claude.com/claude-code, then run 'claude' once to log in."
fi

info "Done. cd into a git repo (or a folder of repos) and run: $BIN"
