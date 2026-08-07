#!/usr/bin/env bash
# Create or update a GitHub Release from packaged archives and SHA256SUMS.
#
# Usage:
#   scripts/create-github-release.sh <tag> <archives-dir>
#
# Example:
#   scripts/create-github-release.sh v0.1.0 dist/archives
#
# Requires: gh authenticated for tejasa97/vidstow, and the archives already built.

set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 <tag> <archives-dir>" >&2
  exit 2
fi

tag=$1
archives_dir=$2

if [[ ! -d "$archives_dir" ]]; then
  echo "create-github-release: missing archives dir: $archives_dir" >&2
  exit 1
fi

if [[ ! -f "$archives_dir/SHA256SUMS" ]]; then
  echo "create-github-release: missing SHA256SUMS in $archives_dir" >&2
  exit 1
fi

shopt -s nullglob
assets=("$archives_dir"/*)
if [[ ${#assets[@]} -eq 0 ]]; then
  echo "create-github-release: no assets found in $archives_dir" >&2
  exit 1
fi

notes_file=$(mktemp)
trap 'rm -f "$notes_file"' EXIT

cat >"$notes_file" <<EOF
## VidStow ${tag}

Focused desktop downloader for public single-video YouTube URLs.

### Downloads

- **macOS**: \`.zip\` containing \`VidStow.app\` (Apple Silicon and Intel builds are separate)
- **Windows**: portable \`.zip\` and optional NSIS \`.exe\` installer
- **Linux**: \`.tar.gz\` containing the binary and JS helper

### Requirements

- [FFmpeg](https://ffmpeg.org/download.html) on \`PATH\` (or configured in Settings)
- Internet access for analyzing and downloading public YouTube videos

### Unsigned builds

This early release ships **unsigned** artifacts so people can try VidStow without waiting for platform signing certificates.

- **macOS**: right-click → Open the first time (Gatekeeper)
- **Windows**: SmartScreen may warn; choose More info → Run anyway
- **Linux**: extract and run \`./vidstow\`

See [docs/RELEASE.md](docs/RELEASE.md) for packaging details and signing roadmap.

Verify downloads against \`SHA256SUMS\`.
EOF

if gh release view "$tag" >/dev/null 2>&1; then
  echo "create-github-release: updating existing release $tag"
  gh release upload "$tag" "${assets[@]}" --clobber
else
  echo "create-github-release: creating release $tag"
  gh release create "$tag" "${assets[@]}" \
    --title "VidStow ${tag}" \
    --notes-file "$notes_file"
fi

echo "create-github-release: done ($tag)"
