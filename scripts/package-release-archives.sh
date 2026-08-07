#!/bin/sh
# Create versioned launch archives and SHA-256 checksums from Wails build output.
# Usage: package-release-archives.sh <version> <os> <arch> <artifact-path>
# Examples:
#   package-release-archives.sh 0.1.0 darwin arm64 build/bin/VidStow.app
#   package-release-archives.sh 0.1.0 windows amd64 build/bin/vidstow.exe
#   package-release-archives.sh 0.1.0 linux amd64 build/bin/vidstow
set -eu

version=${1:-}
os_name=${2:-}
arch=${3:-}
artifact=${4:-}

if [ -z "$version" ] || [ -z "$os_name" ] || [ -z "$arch" ] || [ -z "$artifact" ]; then
  echo "usage: package-release-archives.sh <version> <os> <arch> <artifact-path>" >&2
  exit 2
fi

if [ ! -e "$artifact" ]; then
  echo "package-release-archives: missing artifact: $artifact" >&2
  exit 1
fi

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_dir=$(CDPATH= cd -- "$script_dir/.." && pwd)
dist_dir="$repo_dir/dist"
staging_root=$(mktemp -d "${TMPDIR:-/tmp}/vidstow-release.XXXXXX")
trap 'rm -rf "$staging_root"' EXIT HUP INT TERM

mkdir -p "$dist_dir"
stage_dir="$staging_root/VidStow"
mkdir -p "$stage_dir"

artifact_abs=$(CDPATH= cd -- "$(dirname -- "$artifact")" && pwd)/$(basename -- "$artifact")
artifact_dir=$(CDPATH= cd -- "$(dirname -- "$artifact_abs")" && pwd)

copy_sibling_helper() {
  helper_name=$1
  if [ -f "$artifact_dir/$helper_name" ]; then
    cp -f "$artifact_dir/$helper_name" "$stage_dir/$helper_name"
    chmod 755 "$stage_dir/$helper_name" 2>/dev/null || true
  fi
}

case "$os_name" in
  darwin)
    if [ ! -d "$artifact_abs" ]; then
      echo "package-release-archives: darwin artifact must be an .app bundle" >&2
      exit 1
    fi
    cp -R "$artifact_abs" "$stage_dir/VidStow.app"
    copy_sibling_helper ytdlp-js-helper
    # Prefer the helper already placed inside the app bundle when present.
    if [ -f "$stage_dir/VidStow.app/Contents/MacOS/ytdlp-js-helper" ]; then
      rm -f "$stage_dir/ytdlp-js-helper"
    fi
    archive_name="VidStow-${version}-darwin-${arch}.zip"
    (
      cd "$staging_root"
      if command -v ditto >/dev/null 2>&1; then
        ditto -c -k --sequesterRsrc --keepParent VidStow "$dist_dir/$archive_name"
      else
        zip -qry "$dist_dir/$archive_name" VidStow
      fi
    )
    ;;
  windows)
    if [ ! -f "$artifact_abs" ]; then
      echo "package-release-archives: windows artifact must be an executable" >&2
      exit 1
    fi
    cp -f "$artifact_abs" "$stage_dir/VidStow.exe"
    copy_sibling_helper ytdlp-js-helper.exe
    if [ ! -f "$stage_dir/ytdlp-js-helper.exe" ]; then
      echo "package-release-archives: missing ytdlp-js-helper.exe beside $artifact_abs" >&2
      exit 1
    fi
    # Prefer a portable zip for first public launch; NSIS installers are optional extras.
    archive_name="VidStow-${version}-windows-${arch}.zip"
    (
      cd "$staging_root"
      if command -v zip >/dev/null 2>&1; then
        zip -qry "$dist_dir/$archive_name" VidStow
      else
        # Fallback for Windows runners without zip.
        powershell -NoProfile -Command "Compress-Archive -Path 'VidStow' -DestinationPath '$dist_dir/$archive_name' -Force"
      fi
    )
    installer_candidate="$artifact_dir/VidStow-${arch}-installer.exe"
    if [ -f "$installer_candidate" ]; then
      cp -f "$installer_candidate" "$dist_dir/VidStow-${version}-windows-${arch}-installer.exe"
    fi
    ;;
  linux)
    if [ ! -f "$artifact_abs" ]; then
      echo "package-release-archives: linux artifact must be an executable" >&2
      exit 1
    fi
    cp -f "$artifact_abs" "$stage_dir/vidstow"
    chmod 755 "$stage_dir/vidstow"
    copy_sibling_helper ytdlp-js-helper
    if [ ! -f "$stage_dir/ytdlp-js-helper" ]; then
      echo "package-release-archives: missing ytdlp-js-helper beside $artifact_abs" >&2
      exit 1
    fi
    archive_name="VidStow-${version}-linux-${arch}.tar.gz"
    (
      cd "$staging_root"
      tar -czf "$dist_dir/$archive_name" VidStow
    )
    ;;
  *)
    echo "package-release-archives: unsupported os: $os_name" >&2
    exit 2
    ;;
esac

# Refresh checksums for every file currently in dist/.
(
  cd "$dist_dir"
  rm -f SHA256SUMS
  if command -v shasum >/dev/null 2>&1; then
    for file in *; do
      [ -f "$file" ] || continue
      LC_ALL=C shasum -a 256 "$file" >> SHA256SUMS
    done
  elif command -v sha256sum >/dev/null 2>&1; then
    for file in *; do
      [ -f "$file" ] || continue
      LC_ALL=C sha256sum "$file" >> SHA256SUMS
    done
  else
    echo "package-release-archives: no sha256 tool available" >&2
    exit 1
  fi
)

printf '%s\n' "package-release-archives: wrote $dist_dir/$archive_name"
