# Release packaging

VidStow ships native desktop builds for macOS, Windows, and Linux. Wails cannot
cross-compile webview apps, so each platform must be built on a matching runner
or machine.

## Artifacts for v0.1.0

| Platform | Artifact | Contents |
| --- | --- | --- |
| macOS Apple Silicon | `VidStow-0.1.0-darwin-arm64.zip` | `VidStow.app` with embedded `ytdlp-js-helper` |
| macOS Intel | `VidStow-0.1.0-darwin-amd64.zip` | `VidStow.app` with embedded `ytdlp-js-helper` |
| Windows x64 | `VidStow-0.1.0-windows-amd64.zip` | portable `vidstow.exe` + `ytdlp-js-helper.exe` |
| Windows x64 installer | `VidStow-0.1.0-windows-amd64-installer.exe` | NSIS installer that also installs the helper |
| Linux x64 | `VidStow-0.1.0-linux-amd64.tar.gz` | `vidstow` + `ytdlp-js-helper` |

Every GitHub Release also includes `SHA256SUMS`.

## Local packaging

```sh
# Install Wails once
go install github.com/wailsapp/wails/v2/cmd/wails@v2.13.0

# macOS
wails build -platform darwin/arm64 -clean
./scripts/package-release-archives.sh 0.1.0 darwin arm64 build/bin/VidStow.app

# Windows (on Windows)
wails build -platform windows/amd64 -nsis -clean
./scripts/package-release-archives.sh 0.1.0 windows amd64 build/bin/vidstow.exe

# Linux
wails build -platform linux/amd64 -clean
./scripts/package-release-archives.sh 0.1.0 linux amd64 build/bin/vidstow
```

Post-build hooks package and verify `ytdlp-js-helper` beside the executable on
Windows and Linux. On macOS the helper is placed inside the app bundle and the
bundle is ad-hoc re-signed.

## Current signing status

Current locally produced artifacts do not carry an endorsed distribution
signature:

- macOS bundles are ad-hoc signed, not Developer ID signed or notarized.
- Windows binaries do not carry an Authenticode signature.
- Linux archives do not carry a separate desktop-distribution signature.

Release notes must describe the signature on the exact artifact and must not
instruct users to disable or bypass operating-system security controls.

## Signing and notarization requirements

A Gatekeeper-accepted macOS artifact requires a Developer ID Application
signature, Apple notarization, and stapling. CI must obtain signing and
notarization credentials from protected secrets and must verify the resulting
artifact before release.

If a Windows artifact is described as signed, the exact binary and installer
must carry a verifiable Authenticode signature. The installer signing hooks are
in `build/windows/installer/project.nsi`.

## FFmpeg

FFmpeg is **not** bundled in v0.1 because redistribution raises LGPL/GPL notice
and update-maintenance questions. The app detects FFmpeg/FFprobe on `PATH` and
lets the user pick a custom binary. Release notes and the README tell users how
to install it.

## GitHub Release automation

`.github/workflows/release.yml` defines the repository's tagged-build
automation. It builds on matching platform runners and assembles archives plus
`SHA256SUMS`. Any published release must identify the exact source revision,
supported platforms, dependency versions, and signing status of its artifacts.

Manual archive creation after local builds:

```sh
./scripts/create-github-release.sh v0.1.0 dist
```
