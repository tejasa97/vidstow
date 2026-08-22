# Release packaging

The current packaged release scope is a macOS Apple Silicon beta. VidStow's
Apache-2.0 source and self-build instructions remain available for users who
prefer to build it themselves.

## Homebrew installation

The recommended beta installation uses the project-owned
[`vidstow/tap`](https://github.com/vidstow/homebrew-tap) cask:

```sh
brew tap vidstow/tap
brew install --cask vidstow
```

The cask pins and verifies the published ZIP checksum, installs FFmpeg as a
Homebrew dependency, places `VidStow.app` in Applications, and explicitly
removes its quarantine attribute. This bypass is disclosed in both the cask
output and tap README. It is temporary until releases can be Developer ID signed
and Apple-notarized.

## Artifact for v0.1.0-beta.4

| Platform | Artifact | Purpose |
| --- | --- | --- |
| macOS Apple Silicon | `VidStow-0.1.0-beta.4-darwin-arm64.zip` | Checksum-pinned payload consumed by the Homebrew cask |

The published [`v0.1.0-beta.4`](https://github.com/vidstow/vidstow/releases/tag/v0.1.0-beta.4)
prerelease includes the ZIP, `SHA256SUMS`, and build metadata. These are
technical release inputs and verification evidence; the supported user
installation path is the Homebrew cask.

The release also includes `SHA256SUMS` and build metadata identifying the exact
source revision and `ytdlp-go` module version (`github.com/tejasa97/youtube_dlp`).
A checksum establishes file identity only; it does not establish publisher identity,
notarization, safety, or reproducibility.

Windows, Linux, and macOS Intel packages are outside the current supported
release scope.

## Local packaging

Install the documented Go, Node.js, Wails, and Xcode command-line tool versions,
then build on an Apple Silicon Mac:

```sh
go install github.com/wailsapp/wails/v2/cmd/wails@v2.13.0
wails build -platform darwin/arm64 -clean
./scripts/package-release-archives.sh \
  0.1.0-beta.4 darwin arm64 build/bin/VidStow.app
```

The macOS post-build hook places `ytdlp-js-helper` inside the app bundle and
ad-hoc re-signs the bundle after modification. Verify the bundle, helper,
archive layout, architecture, version, and checksum before creating a release
candidate.

## Artifact signature

The current macOS bundle is ad-hoc signed. An ad-hoc signature can detect some
post-signing modifications, but it does not establish publisher identity or
Apple notarization. The ZIP container is unsigned; only the app bundle inside
it carries the ad-hoc signature.

Release notes must describe the exact artifact accurately and must not claim
Apple verification. Any quarantine bypass must be explicit, checksum-pinned,
and accompanied by a warning that normal Gatekeeper verification is being
bypassed.

## FFmpeg

FFmpeg and FFprobe are not bundled. The Homebrew cask installs FFmpeg as a
formula dependency. Source builds detect the binaries on `PATH`, in Homebrew's
default prefixes, or at paths selected by the user. The README and release
notes must state this external requirement.

## Updates

The beta does not include an automatic updater. Users obtain updates manually
from the project's GitHub Releases page or build a newer source revision.

## GitHub release automation

The release-candidate workflow must:

1. run only by explicit manual dispatch from `main`;
2. accept only the exact beta version configured in the source;
3. build and validate macOS Apple Silicon only;
4. record the source commit and dependency versions;
5. produce the ZIP, `SHA256SUMS`, and build metadata; and
6. create or update a draft prerelease without publishing it.

A maintainer reviews the draft and its validation evidence before publication.
Tag creation, release approval, and publication remain manual authority.
