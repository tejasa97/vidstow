# Changelog

All notable user-visible changes to VidStow are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and versions follow semantic versioning where practical for a desktop
application.

`v0.1.0-beta.1` is the first public VidStow prerelease. It packages a macOS
Apple Silicon preview; a later stable `0.1.0` does not exist yet.

## [Unreleased]

### Added

- No entries yet.

### Changed

- README and release packaging now describe the published `v0.1.0-beta.1`
  macOS Apple Silicon DMG and zip.

### Fixed

- Retrying a download that failed mid-transfer because its saved YouTube link
  expired no longer reuses the dead link forever: the first retry still
  resumes, and once a resume makes no progress (or the server rejects the link
  with HTTP 403) the next retry discards the saved data and restarts with a
  fresh link. After two such restarts the row explains that the item must be
  removed and downloaded again from Home.
- Mid-transfer failures now explain what Retry will do instead of surfacing
  the raw engine error.
- The macOS app icon uses Apple's 824/1024 icon grid with transparent
  padding so the Dock draws it at the same size as neighboring icons.

## [0.1.0-beta.1] - 2026-08-15

### Added

- Analysis and downloading of public, on-demand YouTube Shorts URLs.
- Automatic discovery of Homebrew FFmpeg and FFprobe when they are not on the
  application PATH.
- Initial desktop workflow for public single-video YouTube URLs.
- Analyze, quality presets, download queue, history, and FFmpeg diagnostics.
- Durable State v2 queue lifecycle, destination reservation, startup recovery,
  and bounded shutdown foundations.
- Backend-authored lifecycle queue controls and restored thumbnails.
- Packaged `ytdlp-js-helper` runtime for the macOS application bundle.
- Manual Apple-Silicon beta-candidate workflow with checksums, release
  metadata, and build-provenance attestation.

### Changed

- App icon, sidebar mark, and docs logos now use the simpler arrow-into-tray
  download glyph.
- The packaged application version is `0.1.0-beta.1`; no stable `0.1.0` release
  exists.
- Candidate packaging currently targets macOS on Apple Silicon only.
- Release automation creates a draft prerelease and cannot publish it.
- The desktop engine pin is `github.com/tejasa97/youtube_dlp v0.2.1`.

[Unreleased]: https://github.com/vidstow/vidstow/compare/v0.1.0-beta.1...HEAD
[0.1.0-beta.1]: https://github.com/vidstow/vidstow/releases/tag/v0.1.0-beta.1
