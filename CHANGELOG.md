# Changelog

All notable user-visible changes to VidStow are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and versions follow semantic versioning where practical for a desktop
application.

No public VidStow release has been published yet. `v0.1.0-beta.1` is the first
packaged macOS Apple Silicon candidate; publication remains a separate
maintainer step after the draft prerelease is reviewed.

## [Unreleased]

### Added

- No entries yet.

### Changed

- No entries yet.

### Fixed

- No entries yet.

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

- The packaged application version is `0.1.0-beta.1`; no stable `0.1.0` release
  exists.
- Candidate packaging currently targets macOS on Apple Silicon only.
- Release automation creates a draft prerelease and cannot publish it.
- The desktop engine pin is `github.com/tejasa97/youtube_dlp v0.2.1`.

[Unreleased]: https://github.com/tejasa97/vidstow/compare/v0.1.0-beta.1...HEAD
[0.1.0-beta.1]: https://github.com/tejasa97/vidstow/releases/tag/v0.1.0-beta.1
