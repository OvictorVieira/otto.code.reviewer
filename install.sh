#!/bin/sh
# Otto installer.
#
#   curl -fsSL https://raw.githubusercontent.com/OvictorVieira/otto.code.reviewer/main/install.sh | sh
#
# Options (after `sh -s --`):
#   --version vX.Y.Z   install a specific release (default: latest)
#   --no-setup         do not start `otto setup` at the end
#   --uninstall        remove the binary, the background service and the cache
#   --purge            with --uninstall, also remove ~/.config/otto
#
# Environment: OTTO_INSTALL_DIR (default ~/.local/bin)
set -eu

REPO="OvictorVieira/otto.code.reviewer"
INSTALL_DIR="${OTTO_INSTALL_DIR:-$HOME/.local/bin}"
VERSION="latest"
RUN_SETUP=1
UNINSTALL=0
PURGE=0
LABEL="dev.otto.reviewer"

say() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
ok() { printf '\033[1;32m ✓\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m !\033[0m %s\n' "$*" >&2; }
die() {
	printf '\033[1;31m ✗\033[0m %s\n' "$*" >&2
	exit 1
}
has() { command -v "$1" >/dev/null 2>&1; }
interactive() { [ -t 1 ] && [ -r /dev/tty ] && (: </dev/tty) 2>/dev/null; }

while [ $# -gt 0 ]; do
	case "$1" in
	--version)
		[ $# -ge 2 ] || die "--version needs a value, e.g. --version v0.1.0"
		VERSION="$2"
		shift 2
		;;
	--no-setup) RUN_SETUP=0 && shift ;;
	--uninstall) UNINSTALL=1 && shift ;;
	--purge) PURGE=1 && shift ;;
	*) die "unknown option: $1" ;;
	esac
done

OS="$(uname -s)"
case "$OS" in
Darwin) OS=darwin CACHE_DIR="$HOME/Library/Caches/otto" ;;
Linux) OS=linux CACHE_DIR="${XDG_CACHE_HOME:-$HOME/.cache}/otto" ;;
*) die "unsupported OS: $OS (macOS and Linux are supported)" ;;
esac

uninstall() {
	say "Uninstalling Otto"
	plist="$HOME/Library/LaunchAgents/$LABEL.plist"
	monitor_plist="$HOME/Library/LaunchAgents/$LABEL.monitor.plist"
	if [ "$OS" = darwin ] && { [ -f "$plist" ] || [ -f "$monitor_plist" ]; }; then
		launchctl bootout "gui/$(id -u)" "$plist" 2>/dev/null || true
		launchctl bootout "gui/$(id -u)" "$monitor_plist" 2>/dev/null || true
		rm -f "$plist" "$monitor_plist"
		ok "background service removed"
	fi
	for bin in "$INSTALL_DIR/otto" "$(command -v otto 2>/dev/null || true)"; do
		if [ -n "$bin" ] && [ -f "$bin" ]; then
			rm -f "$bin"
			ok "removed $bin"
		fi
	done
	rm -rf "$CACHE_DIR" && ok "removed cache $CACHE_DIR"
	if [ "$PURGE" = 1 ]; then
		rm -rf "$HOME/.config/otto" && ok "removed config ~/.config/otto"
	else
		say "Kept your config in ~/.config/otto (use --purge to remove it)"
	fi
}

if [ "$UNINSTALL" = 1 ]; then
	uninstall
	exit 0
fi

ARCH="$(uname -m)"
case "$ARCH" in
x86_64 | amd64) ARCH=amd64 ;;
arm64 | aarch64) ARCH=arm64 ;;
*) die "unsupported architecture: $ARCH" ;;
esac

has curl || die "curl is required"
has tar || die "tar is required"
if ! has git; then
	[ "$OS" = darwin ] && die "git is required: run \`xcode-select --install\` and try again"
	die "git is required: install it with your package manager and try again"
fi

checksum() {
	if has sha256sum; then
		sha256sum "$1" | cut -d' ' -f1
	else
		shasum -a 256 "$1" | cut -d' ' -f1
	fi
}

install_binary() {
	asset="otto_${OS}_${ARCH}.tar.gz"
	if [ "$VERSION" = latest ]; then
		base="https://github.com/$REPO/releases/latest/download"
	else
		base="https://github.com/$REPO/releases/download/$VERSION"
	fi
	tmp="$(mktemp -d)"
	trap 'rm -rf "$tmp"' EXIT

	say "Downloading Otto ($VERSION, $OS/$ARCH)"
	if curl -fsSL "$base/$asset" -o "$tmp/$asset" && curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt"; then
		want="$(grep " $asset\$" "$tmp/checksums.txt" | cut -d' ' -f1)"
		[ -n "$want" ] || die "no checksum for $asset in the release"
		[ "$(checksum "$tmp/$asset")" = "$want" ] || die "checksum mismatch for $asset: download is corrupted or tampered with"
		ok "checksum verified"
		tar -xzf "$tmp/$asset" -C "$tmp" otto
		mkdir -p "$INSTALL_DIR"
		mv "$tmp/otto" "$INSTALL_DIR/otto"
		chmod +x "$INSTALL_DIR/otto"
	elif has go; then
		warn "no prebuilt release found; building from source with Go"
		ref="$VERSION"
		[ "$ref" = latest ] && ref=main
		mkdir -p "$INSTALL_DIR"
		GOBIN="$INSTALL_DIR" go install "github.com/$REPO/cmd/otto@$ref"
	else
		die "could not download Otto and Go is not installed to build it"
	fi
	ok "installed $INSTALL_DIR/otto ($("$INSTALL_DIR/otto" version))"
}

ensure_gh() {
	if ! has gh; then
		if has brew; then
			say "Installing GitHub CLI with Homebrew"
			brew install gh
		else
			die "GitHub CLI (gh) is required: https://cli.github.com, then run this installer again"
		fi
	fi
	ok "GitHub CLI: $(gh --version | head -1)"
	if ! gh auth status >/dev/null 2>&1; then
		if interactive; then
			say "Log in to GitHub (Otto posts reviews as you)"
			gh auth login </dev/tty
		else
			warn "GitHub CLI is not logged in: run \`gh auth login\`"
		fi
	fi
}

report_engines() {
	say "AI engines (Otto uses the ones you already have)"
	found=0
	for pair in "claude:Claude Code:https://docs.anthropic.com/claude-code" \
		"codex:Codex:https://github.com/openai/codex" \
		"cursor-agent:Cursor:https://cursor.com/cli" \
		"opencode:OpenCode:https://opencode.ai" \
		"copilot:GitHub Copilot CLI:https://docs.github.com/en/copilot/how-tos/copilot-cli/set-up-copilot-cli/install-copilot-cli"; do
		bin="${pair%%:*}"
		rest="${pair#*:}"
		name="${rest%%:*}"
		url="${rest#*:}"
		if has "$bin"; then
			ok "$name ($bin)"
			found=1
		else
			printf '   - %s not installed: %s\n' "$name" "$url"
		fi
	done
	[ "$found" = 1 ] || warn "install and log in to at least one engine before reviewing"
}

install_binary
ensure_gh
report_engines

case ":$PATH:" in
*":$INSTALL_DIR:"*) ;;
*)
	warn "$INSTALL_DIR is not in your PATH. Add this to your shell profile:"
	# shellcheck disable=SC2016 # print a literal $PATH for the user to copy
	printf '     export PATH="%s:$PATH"\n' "$INSTALL_DIR"
	;;
esac

if [ "$RUN_SETUP" = 1 ] && interactive; then
	say "Starting setup"
	"$INSTALL_DIR/otto" setup </dev/tty
else
	say "Next: run \`otto setup\`"
fi
