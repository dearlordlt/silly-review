#!/bin/sh
# silly-review installer / updater — builds from source, fetching Go if needed.
#
#   curl -fsSL https://raw.githubusercontent.com/dearlordlt/silly-review/main/setup.sh | sh
#
# Needs git. If a new-enough Go isn't found it downloads the official toolchain
# to a private dir (no sudo). silly-review also needs the `claude` CLI at runtime.
#
# Env overrides:
#   INSTALL_DIR               where to put the binary    (default: ~/.local/bin)
#   BRANCH                    branch/tag to install      (default: main)
#   REPO_URL                  repo to clone              (default: dearlordlt/silly-review)
#   SILLY_REVIEW_NO_GO_INSTALL=1   never auto-download Go; just print guidance
set -eu

REPO_URL="${REPO_URL:-https://github.com/dearlordlt/silly-review}"
BRANCH="${BRANCH:-main}"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"
DATADIR="${XDG_DATA_HOME:-$HOME/.local/share}/silly-review"
BIN="silly-review"

info() { printf '\033[36m==>\033[0m %s\n' "$1"; }
warn() { printf '\033[33mwarning:\033[0m %s\n' "$1" >&2; }
die()  { printf '\033[31merror:\033[0m %s\n' "$1" >&2; exit 1; }

have() { command -v "$1" >/dev/null 2>&1; }

# dl_to URL OUTFILE — download to a file (curl or wget).
dl_to() {
	if have curl; then curl -fsSL "$1" -o "$2"
	elif have wget; then wget -qO "$2" "$1"
	else return 1
	fi
}
# dl_cat URL — download to stdout.
dl_cat() {
	if have curl; then curl -fsSL "$1"
	elif have wget; then wget -qO- "$1"
	else return 1
	fi
}

have git || die "git is required — install it and re-run."

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM

info "Cloning $REPO_URL ($BRANCH)…"
git clone --quiet --depth 1 --branch "$BRANCH" "$REPO_URL" "$tmp/src" \
	|| die "clone failed — check the repo URL and branch."

# Required Go floor comes from the cloned go.mod (e.g. "go 1.24.2" -> 1 / 24).
mingo="$(awk '/^go /{print $2; exit}' "$tmp/src/go.mod")"
MIN_MAJOR="${mingo%%.*}"
rest="${mingo#*.}"
MIN_MINOR="${rest%%.*}"
[ -n "$MIN_MAJOR" ] && [ -n "$MIN_MINOR" ] || { MIN_MAJOR=1; MIN_MINOR=24; }

# go_ok GOBIN — true if that Go is >= the required floor.
go_ok() {
	v="$("$1" version 2>/dev/null | awk '{print $3}')" # e.g. go1.24.5
	v="${v#go}"
	[ -n "$v" ] || return 1
	maj="${v%%.*}"
	r="${v#*.}"
	min="${r%%.*}"
	case "$maj$min" in *[!0-9]*) return 1 ;; esac
	[ "$maj" -gt "$MIN_MAJOR" ] && return 0
	[ "$maj" -eq "$MIN_MAJOR" ] && [ "$min" -ge "$MIN_MINOR" ] && return 0
	return 1
}

distro_hint() {
	if [ "$(uname -s)" = "Darwin" ]; then
		echo "  brew install go    (or download from https://go.dev/dl/)"
		return
	fi
	id=""
	[ -r /etc/os-release ] && id="$(. /etc/os-release 2>/dev/null; echo "${ID:-} ${ID_LIKE:-}")"
	case "$id" in
		*arch* | *cachyos* | *manjaro*) echo "  sudo pacman -S go" ;;
		*debian* | *ubuntu* | *mint*)   echo "  sudo apt install golang-go    (may be older than $MIN_MAJOR.$MIN_MINOR — see https://go.dev/dl/)" ;;
		*fedora* | *rhel* | *centos*)   echo "  sudo dnf install golang" ;;
		*suse* | *opensuse*)            echo "  sudo zypper install go" ;;
		*alpine*)                       echo "  sudo apk add go" ;;
		*)                              echo "  see https://go.dev/dl/" ;;
	esac
}

guide_go() {
	warn "Go $MIN_MAJOR.$MIN_MINOR+ is required to build silly-review. Install it, then re-run:"
	distro_hint >&2
	echo "  https://go.dev/dl/" >&2
}

confirm_go_install() {
	if [ -r /dev/tty ]; then
		printf 'Go %s.%s+ not found. Download a private copy to %s (no sudo, ~150MB)? [Y/n] ' \
			"$MIN_MAJOR" "$MIN_MINOR" "$DATADIR/go" >/dev/tty
		ans=""
		read -r ans </dev/tty 2>/dev/null || ans=""
		case "$ans" in [Nn]*) return 1 ;; esac
	fi
	return 0 # no tty (piped/CI): proceed
}

install_go() {
	os="$(uname -s | tr '[:upper:]' '[:lower:]')"
	case "$os" in linux | darwin) ;; *) warn "no automatic Go install for OS '$os'."; return 1 ;; esac
	case "$(uname -m)" in
		x86_64 | amd64) arch=amd64 ;;
		aarch64 | arm64) arch=arm64 ;;
		armv6l | armv7l) arch=armv6l ;;
		i386 | i686) arch=386 ;;
		*) warn "no automatic Go install for CPU '$(uname -m)'."; return 1 ;;
	esac
	ver="$(dl_cat 'https://go.dev/VERSION?m=text' 2>/dev/null | head -1)" || return 1
	case "$ver" in go*) ;; *) warn "couldn't determine the latest Go version."; return 1 ;; esac
	url="https://go.dev/dl/${ver}.${os}-${arch}.tar.gz"
	info "Downloading $ver for ${os}-${arch} → $DATADIR/go …"
	tgz="$tmp/go.tar.gz"
	dl_to "$url" "$tgz" || { warn "Go download failed ($url)."; return 1; }
	[ -s "$tgz" ] || { warn "Go download was empty."; return 1; }
	rm -rf "$DATADIR/go"
	mkdir -p "$DATADIR/go" || return 1
	tar -C "$DATADIR/go" --strip-components=1 -xzf "$tgz" || { warn "extracting Go failed."; return 1; }
	[ -x "$DATADIR/go/bin/go" ] || { warn "Go install looks incomplete."; return 1; }
	GO="$DATADIR/go/bin/go"
	go_ok "$GO" || { warn "downloaded Go is older than required."; return 1; }
	return 0
}

# Resolve a usable Go into $GO: PATH, then our private copy, then download.
GO=""
if have go && go_ok go; then
	GO="go"
elif [ -x "$DATADIR/go/bin/go" ] && go_ok "$DATADIR/go/bin/go"; then
	GO="$DATADIR/go/bin/go"
	info "Using the Go previously installed at $DATADIR/go."
elif [ "${SILLY_REVIEW_NO_GO_INSTALL:-}" = "1" ]; then
	guide_go
	die "Go not available and auto-install disabled."
elif confirm_go_install && install_go; then
	info "Go ready at $DATADIR/go (used only to build silly-review)."
else
	guide_go
	die "could not obtain Go."
fi

info "Building $BIN…"
( cd "$tmp/src" && GOTOOLCHAIN=local "$GO" build -o "$tmp/$BIN" . ) || die "build failed."

# Note the currently-installed version (if any) for install-vs-update reporting.
old=""
if [ -x "$INSTALL_DIR/$BIN" ]; then
	old="$("$INSTALL_DIR/$BIN" --version 2>/dev/null | awk 'NF{print $NF; exit}')"
fi
new="$("$tmp/$BIN" --version 2>/dev/null | awk 'NF{print $NF; exit}')"
[ -n "$new" ] || new="(unknown)"

mkdir -p "$INSTALL_DIR" || die "cannot create $INSTALL_DIR"
# Install via temp file + atomic rename so replacing a running binary doesn't
# hit ETXTBSY on Linux.
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

case ":${PATH:-}:" in
	*":$INSTALL_DIR:"*) : ;;
	*)
		warn "$INSTALL_DIR is not on your PATH. Add this to your shell profile (~/.zshrc, ~/.bashrc):"
		printf '    export PATH="%s:$PATH"\n' "$INSTALL_DIR" >&2
		;;
esac

if ! have claude; then
	warn "the 'claude' CLI was not found. silly-review needs it (and you must be signed in)."
	warn "Install Claude Code from https://claude.com/claude-code, then run 'claude' once to log in."
fi

info "Done. cd into a git repo (or a folder of repos) and run: $BIN"
