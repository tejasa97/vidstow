# Changelog

All notable user-visible changes to VidStow are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and versions follow semantic versioning where practical for a desktop
application.

`v0.1.0-beta.1` is the first public VidStow prerelease. The current packaged
preview is `v0.1.0-beta.4` for macOS Apple Silicon; a later stable `0.1.0`
does not exist yet.

## [Unreleased]

### Added

- No entries yet.

### Changed

- Public engine naming now uses `ytdlp-go` (`https://github.com/tejasa97/ytdlp-go`).
  The Go module pin remains `github.com/tejasa97/youtube_dlp v0.2.3`.

### Fixed

- Startup waits for the saved queue check instead of briefly reporting recovery,
  ignores stale workspace identities for terminal jobs, and clears a leftover
  recovery marker only when the committed state is provably intact.

## [0.1.0-beta.4] - 2026-08-18

### Changed

- The packaged application version is `0.1.0-beta.4`.
- Action-required Review can try recovery again, retry with a fresh link, or
  discard saved data. Uncertain session evidence is not reused.
- Queue rows with pending or quarantined cleanup stay visible until that
  cleanup settles. Retry cleanup is available, and Remove appears only after
  cleanup is done.

### Fixed

- Deleting a file from Downloads also removes the matching completed item from
  the queue.
- Canceled rows that still carried a leftover cleanup phase show as Canceled
  and can be removed once cleanup has settled.

## [0.1.0-beta.3] - 2026-08-18

### Changed

- The packaged application version is `0.1.0-beta.3`.
- The desktop engine pin is `github.com/tejasa97/youtube_dlp v0.2.3`.
- Repeated downloads using the same YouTube player can reuse completed EJS
  preprocessing while each job retains its isolated JavaScript helper.

### Fixed

- Action-required queue rows provide a bounded review flow and can start over
  through normal analysis without deleting or reusing uncertain saved data.

## [0.1.0-beta.2] - 2026-08-17

### Changed

- README and release packaging recommend the
  [`vidstow/tap`](https://github.com/vidstow/homebrew-tap) Homebrew cask as the
  supported install path. The GitHub ZIP remains a checksum-pinned technical
  artifact.
- The packaged application version is `0.1.0-beta.2`.
- The desktop engine pin is `github.com/tejasa97/youtube_dlp v0.2.2`.

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

[Unreleased]: https://github.com/vidstow/vidstow/compare/v0.1.0-beta.4...HEAD
[0.1.0-beta.4]: https://github.com/vidstow/vidstow/releases/tag/v0.1.0-beta.4
[0.1.0-beta.3]: https://github.com/vidstow/vidstow/releases/tag/v0.1.0-beta.3
[0.1.0-beta.2]: https://github.com/vidstow/vidstow/releases/tag/v0.1.0-beta.2
[0.1.0-beta.1]: https://github.com/vidstow/vidstow/releases/tag/v0.1.0-beta.1
