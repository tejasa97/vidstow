#!/bin/sh
# Build the current VidStow checkout and install VidStow.app on this Mac.
# Usage: ./scripts/install-macos.sh
#
# Requires Apple Silicon macOS, Go 1.25, Node.js 22 or newer, npm, and Xcode
# Command Line Tools. Installs the pinned Wails CLI if it is missing.
# Does not download a GitHub release and does not remove quarantine attributes.
set -eu

WAILS_VERSION=v2.13.0
BUNDLE_ID=app.vidstow.desktop

die() {
  printf '%s\n' "install-macos: $*" >&2
  exit 1
}

log() {
  printf '%s\n' "install-macos: $*"
}

if [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
  cat <<'EOF'
Usage: ./scripts/install-macos.sh

Build VidStow.app from this checkout and copy it to /Applications.

Environment:
  VIDSTOW_INSTALL_DIR  Directory that should receive VidStow.app
                       (default: /Applications)
EOF
  exit 0
fi

if [ "$#" -ne 0 ]; then
  die "unexpected arguments; see --help"
fi

[ "$(uname -s)" = Darwin ] || die "this script only supports macOS"
[ "$(uname -m)" = arm64 ] || die "this script only supports Apple Silicon (found $(uname -m))"

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_dir=$(CDPATH= cd -- "$script_dir/.." && pwd)
cd "$repo_dir"

[ -f "$repo_dir/wails.json" ] && [ -f "$repo_dir/buildinfo.go" ] ||
  die "run this script from a VidStow checkout"

version=$(awk -F'"' '/^[[:space:]]*appVersion[[:space:]]*= / {print $2; exit}' buildinfo.go)
[ -n "$version" ] || die "could not read appVersion from buildinfo.go"

install_root=${VIDSTOW_INSTALL_DIR:-/Applications}
case "$install_root" in
  /*) ;;
  *) die "VIDSTOW_INSTALL_DIR must be an absolute path" ;;
esac
dest="$install_root/VidStow.app"

if ! xcode-select -p >/dev/null 2>&1 || ! xcrun --find clang >/dev/null 2>&1; then
  die "Xcode Command Line Tools are required. Run: xcode-select --install"
fi

command -v go >/dev/null 2>&1 || die "Go 1.25 is required: https://go.dev/dl/"
go_ver=$(go version | awk '{print $3}')
case "$go_ver" in
  go1.25.*) ;;
  *) die "Go 1.25.x is required (found $go_ver)" ;;
esac

command -v node >/dev/null 2>&1 || die "Node.js 22 or newer is required: https://nodejs.org/"
command -v npm >/dev/null 2>&1 || die "npm is required (install Node.js 22 or newer)"
node_major=$(node -v | sed 's/^v//' | cut -d. -f1)
case "$node_major" in
  ''|*[!0-9]*) die "could not parse Node.js version ($(node -v))" ;;
esac
if [ "$node_major" -lt 22 ]; then
  die "Node.js 22 or newer is required (found $(node -v))"
fi

go_bin=$(go env GOPATH)/bin
PATH="$go_bin:$PATH"
export PATH

wails_ok=0
if command -v wails >/dev/null 2>&1; then
  wails_ver=$(wails version 2>/dev/null | tr '\n' ' ')
  case "$wails_ver" in
    *"$WAILS_VERSION"*) wails_ok=1 ;;
  esac
fi
if [ "$wails_ok" -eq 0 ]; then
  log "installing Wails CLI $WAILS_VERSION into $go_bin"
  go install "github.com/wailsapp/wails/v2/cmd/wails@$WAILS_VERSION"
  command -v wails >/dev/null 2>&1 ||
    die "wails is not on PATH after install; add $go_bin to PATH"
fi

if [ -e "$dest" ]; then
  [ -d "$dest/Contents" ] || die "refusing to replace $dest (not an app bundle)"
  existing_id=$(/usr/libexec/PlistBuddy -c 'Print :CFBundleIdentifier' "$dest/Contents/Info.plist" 2>/dev/null || true)
  if [ -n "$existing_id" ] && [ "$existing_id" != "$BUNDLE_ID" ]; then
    die "refusing to replace $dest (bundle id $existing_id)"
  fi
  if ps -axo command= | grep -F "$dest/Contents/MacOS/" | grep -vq grep; then
    die "quit VidStow before replacing $dest"
  fi
fi

log "building VidStow $version from $repo_dir"
log "install destination: $dest"
if [ -e "$dest" ]; then
  log "existing $dest will be replaced"
fi

(
  cd "$repo_dir/frontend"
  npm ci
)

wails build -clean -platform darwin/arm64

app="$repo_dir/build/bin/VidStow.app"
[ -d "$app" ] || die "build did not produce $app"
executable=$(/usr/libexec/PlistBuddy -c 'Print :CFBundleExecutable' "$app/Contents/Info.plist")
[ -x "$app/Contents/MacOS/$executable" ] || die "missing executable in $app"
[ -x "$app/Contents/MacOS/ytdlp-js-helper" ] || die "missing ytdlp-js-helper in $app"

mkdir -p "$install_root"
if [ -e "$dest" ]; then
  rm -rf "$dest"
fi
ditto "$app" "$dest"
[ -x "$dest/Contents/MacOS/$executable" ] || die "install failed: $dest is incomplete"

log "installed $dest"
log "FFmpeg and FFprobe are still required for some outputs"
log "open it from Finder or run: open \"$dest\""
