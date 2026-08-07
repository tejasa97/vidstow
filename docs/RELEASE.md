# Release packaging

VidStow ships native desktop builds for macOS, Windows, and Linux. Wails cannot
cross-compile webview apps, so each platform must be built on a matching runner
or machine.

## Artifacts for v0.1.0

| Platform | Artifact | Contents |
| --- | --- | --- |
| macOS Apple Silicon | `VidStow-0.1.0-darwin-arm64.zip` | `VidStow.app` + sibling `ytdlp-js-helper` |
| macOS Intel | `VidStow-0.1.0-darwin-amd64.zip` | `VidStow.app` + sibling `ytdlp-js-helper` |
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

Post-build hooks package and verify `ytdlp-js-helper` beside the app on all
three platforms. On macOS the helper is placed inside the app bundle and the
bundle is ad-hoc re-signed.

## Unsigned v0.1 policy

The first public release is intentionally **unsigned / un-notarized**:

- macOS: ad-hoc signature only. Users must right-click → Open the first time,
  or clear the quarantine flag:
  `xattr -dr com.apple.quarantine /path/to/VidStow.app`
- Windows: no Authenticode signature. SmartScreen may warn on first launch.
- Linux: ordinary user-owned binaries; no desktop-entry signing.

This keeps the Product Hunt / Reddit launch unblocked while Apple Developer and
Windows code-signing credentials are obtained.

## Signing and notarization (follow-up)

To ship a Gatekeeper-clean macOS build later, you need:

1. Apple Developer Program membership
2. Developer ID Application certificate installed in the CI keychain
3. App Store Connect API key (or Apple ID + app-specific password) for
   `notarytool`
4. GitHub Actions secrets such as:
   - `APPLE_CERTIFICATE_P12` / `APPLE_CERTIFICATE_PASSWORD`
   - `APPLE_API_KEY` / `APPLE_API_KEY_ID` / `APPLE_API_ISSUER`
   - or `APPLE_ID` / `APPLE_APP_SPECIFIC_PASSWORD` / `APPLE_TEAM_ID`

Windows Authenticode is optional for v0.1, but SmartScreen reputation improves
with a standard code-signing certificate. Store the cert as CI secrets and
uncomment the `#finalize` / `#uninstfinalize` lines in
`build/windows/installer/project.nsi` when ready.

## FFmpeg

FFmpeg is **not** bundled in v0.1 because redistribution raises LGPL/GPL notice
and update-maintenance questions. The app detects FFmpeg/FFprobe on `PATH` and
lets the user pick a custom binary. Release notes and the README tell users how
to install it.

## GitHub Release flow

1. Merge launch changes to `main`
2. `git tag -a v0.1.0 -m "VidStow 0.1.0"`
3. `git push origin v0.1.0`
4. `.github/workflows/release.yml` builds all platforms and publishes the draft
   or final release with archives + `SHA256SUMS`

Manual fallback after local builds:

```sh
./scripts/create-github-release.sh v0.1.0 dist
```
