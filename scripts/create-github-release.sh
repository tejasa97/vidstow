#!/usr/bin/env bash
# Create or update a draft GitHub prerelease from a verified candidate bundle.
# This script deliberately cannot publish a release.
#
# Usage:
#   scripts/create-github-release.sh <tag> <archives-dir> [target-commit]

set -euo pipefail

if [[ $# -lt 2 || $# -gt 3 ]]; then
  echo "usage: $0 <tag> <archives-dir> [target-commit]" >&2
  exit 2
fi

tag=$1
archives_dir=$2
target=${3:-}

if [[ ! "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+-beta\.[0-9]+$ ]]; then
  echo "create-github-release: expected a beta tag, got: $tag" >&2
  exit 2
fi
if [[ ! -d "$archives_dir" ]]; then
  echo "create-github-release: missing archives dir: $archives_dir" >&2
  exit 1
fi
if [[ ! -f "$archives_dir/SHA256SUMS" ]]; then
  echo "create-github-release: missing SHA256SUMS in $archives_dir" >&2
  exit 1
fi
if [[ ! -f "$archives_dir/RELEASE-METADATA.txt" ]]; then
  echo "create-github-release: missing RELEASE-METADATA.txt in $archives_dir" >&2
  exit 1
fi
version=${tag#v}
archive_name="VidStow-${version}-darwin-arm64.zip"
if [[ ! -f "$archives_dir/$archive_name" ]]; then
  echo "create-github-release: missing candidate archive: $archive_name" >&2
  exit 1
fi

shopt -s nullglob
assets=("$archives_dir"/*)
if [[ ${#assets[@]} -ne 3 ]]; then
  echo "create-github-release: expected exactly archive, metadata, and checksums" >&2
  exit 1
fi
for asset in "${assets[@]}"; do
  case "$(basename "$asset")" in
    "$archive_name"|RELEASE-METADATA.txt|SHA256SUMS) ;;
    *)
      echo "create-github-release: unexpected candidate asset: $(basename "$asset")" >&2
      exit 1
      ;;
  esac
done
checksum_names=$(awk '{print $2}' "$archives_dir/SHA256SUMS" | LC_ALL=C sort)
expected_names=$(printf '%s\n' "$archive_name" RELEASE-METADATA.txt | LC_ALL=C sort)
if [[ "$checksum_names" != "$expected_names" ]]; then
  echo "create-github-release: checksum manifest does not name the exact candidate files" >&2
  exit 1
fi
if ! (cd "$archives_dir" && sha256sum -c SHA256SUMS); then
  echo "create-github-release: candidate checksum verification failed" >&2
  exit 1
fi

if [[ -z "$target" ]]; then
  echo "create-github-release: target commit is required" >&2
  exit 1
fi
remote_tag=$(git ls-remote --tags origin "refs/tags/$tag^{}" | awk 'NR == 1 {print $1}')
if [[ -z "$remote_tag" ]]; then
  remote_tag=$(git ls-remote --tags origin "refs/tags/$tag" | awk 'NR == 1 {print $1}')
fi
if [[ "$remote_tag" != "$target" ]]; then
  echo "create-github-release: remote tag $tag does not point to target $target" >&2
  exit 1
fi

notes_file=$(mktemp)
trap 'rm -f "$notes_file"' EXIT

cat >"$notes_file" <<EOF
## VidStow ${tag}

This beta preview targets **macOS on Apple Silicon only**.

### Install

The recommended installation uses the project Homebrew cask, which also
installs FFmpeg:

\`\`\`sh
brew tap vidstow/tap
brew install --cask vidstow
\`\`\`

The technical release also includes
\`VidStow-${tag#v}-darwin-arm64.zip\`. External FFmpeg and FFprobe are required
for direct-ZIP and source installations. Updates are installed manually.
\`SHA256SUMS\` verifies the candidate files, and \`RELEASE-METADATA.txt\`
records the exact source commit, engine module, toolchain, workflow, and
artifact metadata.

### Signing and installation warning

The application is ad-hoc signed and is **not notarized by Apple**. The Homebrew
cask verifies the pinned archive checksum and then explicitly removes macOS
quarantine so the app can launch. This bypasses normal Gatekeeper quarantine
enforcement. Review the source, release metadata, and cask before installing.

Technical users can instead build this Apache-2.0 project from the source
attached to this release.

### Scope

VidStow accepts public, on-demand YouTube video, Short, playlist, and bounded
2–20 URL batch workflows exposed by its UI. It does not support channels,
search, live streams, authentication, cookies, private media, DRM, or
access-control circumvention. Pause/Resume does not guarantee universal byte
reuse; saved bytes are reused only when the engine can validate their identity.

See the repository's release packaging guide and documented limitations before
testing.
EOF

if gh release view "$tag" >/dev/null 2>&1; then
  is_draft=$(gh release view "$tag" --json isDraft --jq .isDraft)
  if [[ "$is_draft" != "true" ]]; then
    echo "create-github-release: refusing to overwrite published release $tag" >&2
    exit 1
  fi
  echo "create-github-release: updating draft prerelease $tag"
  gh release upload "$tag" "${assets[@]}" --clobber
  gh release edit "$tag" \
    --draft \
    --prerelease \
    --title "VidStow ${tag}" \
    --notes-file "$notes_file"
else
  echo "create-github-release: creating draft prerelease $tag"
  create_args=(
    "$tag" "${assets[@]}"
    --draft
    --prerelease
    --title "VidStow ${tag}"
    --notes-file "$notes_file"
  )
  create_args+=(--target "$target" --verify-tag)
  gh release create "${create_args[@]}"
fi

echo "create-github-release: draft ready; publication remains a separate maintainer action ($tag)"
