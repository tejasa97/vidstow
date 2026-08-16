# Release packaging

The current packaged release scope is a macOS Apple Silicon beta. VidStow's
Apache-2.0 source and self-build instructions remain available for users who
prefer to build it themselves.

## Artifact for v0.1.0-beta.1

| Platform | Artifact | Contents |
| --- | --- | --- |
| macOS Apple Silicon | `VidStow-0.1.0-beta.1-darwin-arm64.dmg` | `VidStow.app` at volume root plus an Applications symlink |
| macOS Apple Silicon | `VidStow-0.1.0-beta.1-darwin-arm64.zip` | `VidStow.app` with embedded `ytdlp-js-helper` |

The published [`v0.1.0-beta.1`](https://github.com/vidstow/vidstow/releases/tag/v0.1.0-beta.1)
prerelease includes both artifacts. Later candidates emit the zip, DMG,
`SHA256SUMS`, and build metadata from the same workflow. The `v0.1.0-beta.1`
DMG was attached after the zip-only candidate run.

The release also includes `SHA256SUMS` and build metadata identifying the exact
source revision and `youtube_dlp` module version. A checksum establishes file
identity only; it does not establish publisher identity, notarization, safety,
or reproducibility.

Windows, Linux, and macOS Intel packages are outside the current supported
release scope.

## Local packaging

Install the documented Go, Node.js, Wails, and Xcode command-line tool versions,
then build on an Apple Silicon Mac:

```sh
go install github.com/wailsapp/wails/v2/cmd/wails@v2.13.0
wails build -platform darwin/arm64 -clean
./scripts/package-release-archives.sh \
  0.1.0-beta.1 darwin arm64 build/bin/VidStow.app
```

The macOS post-build hook places `ytdlp-js-helper` inside the app bundle and
ad-hoc re-signs the bundle after modification. Packaging writes both the zip
and a drag-to-Applications DMG (`VidStow.app` at the volume root plus an
Applications symlink). Verify the bundle, helper, archive layout, disk image
layout, architecture, version, and checksum before creating a release
candidate. DMG creation requires macOS `hdiutil`.

## Artifact signature

The current macOS bundle is ad-hoc signed. An ad-hoc signature can detect some
post-signing modifications, but it does not establish publisher identity or
Apple notarization. The zip and DMG containers are unsigned; only the app
bundle inside them carries the ad-hoc signature.

Release notes must describe the exact artifact accurately and must not claim
Apple verification or instruct users to disable or bypass operating-system
security controls.

## FFmpeg

FFmpeg and FFprobe are not bundled. The app detects them on `PATH` and lets the
user select custom binaries. The README and release notes must state this
external requirement.

## Updates

The beta does not include an automatic updater. Users obtain updates manually
from the project's GitHub Releases page or build a newer source revision.

## GitHub release automation

The release-candidate workflow must:

1. run only by explicit manual dispatch from `main`;
2. accept only the exact beta version configured in the source;
3. build and validate macOS Apple Silicon only;
4. record the source commit and dependency versions;
5. produce the zip, DMG, `SHA256SUMS`, and build metadata; and
6. create or update a draft prerelease without publishing it.

A maintainer reviews the draft and its validation evidence before publication.
Tag creation, release approval, and publication remain manual authority.
