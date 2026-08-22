<div align="center">

<img src="docs/assets/vidstow-logo.png" alt="VidStow logo" width="168">

# VidStow

### Download public YouTube videos with a focused desktop queue.

A local desktop application built with Go, Wails, and Svelte.

[![Status: beta preview](https://img.shields.io/badge/status-beta_preview-f59e0b.svg)](#project-status)
[![License: Apache-2.0](https://img.shields.io/badge/license-Apache--2.0-4c7cf3.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white)](go.mod)
[![Wails](https://img.shields.io/badge/Wails-v2-CB2D3E)](https://wails.io/)
[![Svelte](https://img.shields.io/badge/Svelte-5-FF3E00?logo=svelte&logoColor=white)](frontend/package.json)
[![Engine](https://img.shields.io/badge/youtube__dlp-pinned-20232A)](go.mod)

[Download](#download) · [Features](#features) · [Screenshots](#screenshots) · [Project status](#project-status) · [Run locally](#run-locally) · [How it works](#how-it-works) · [Contributing](CONTRIBUTING.md)

<video src="docs/assets/demo-walkthrough.mp4" width="800" autoplay muted loop playsinline controls>
  VidStow playlist walkthrough: paste a link, review entries, and follow the local queue.
</video>

</div>

> [!IMPORTANT]
> VidStow is beta software for public, on-demand YouTube video, Short, and playlist URLs.
> Channels, search, live streams, authenticated downloads, and other
> sites are outside the supported application scope.

## Download

[`v0.1.0-beta.5`](https://github.com/vidstow/vidstow/releases/tag/v0.1.0-beta.5)
is the current macOS Apple Silicon preview. The recommended installation
uses VidStow's Homebrew tap:

```sh
brew tap vidstow/tap
brew install --cask vidstow
```

The cask installs FFmpeg and FFprobe automatically. Upgrade later with
`brew update && brew upgrade --cask vidstow`.

> [!WARNING]
> This beta is ad-hoc signed and is not notarized by Apple. After verifying the
> release archive against its pinned SHA-256 checksum, the cask explicitly
> removes macOS quarantine from VidStow so it can launch. Review the
> [tap](https://github.com/vidstow/homebrew-tap), source, and published release
> metadata before installing.

VidStow can alternatively be built from the Apache-2.0 source using the
[local build instructions](#run-locally).

Windows, Linux, and macOS Intel packages are outside the supported release
scope. See the [release guide](docs/RELEASE.md) for artifact packaging and
verification details.

## Features

- **Analyze before downloading** — inspect the title, thumbnail, duration,
  channel, and available output choices.
- **Flexible link input** — paste, type, or drag and drop a public YouTube video,
  Short, or playlist URL. Links containing both a video and a playlist can be
  reviewed as either.
- **Focused output choices** — choose best available video, capped resolutions,
  original audio, or MP3 when the analyzed media supports those choices.
- **Playlist review** — select up to 500 available entries, apply a bounded
  range, and admit the collection as one expandable parent with individual jobs.
- **Reliable batch downloads** — review 2–20 individual video or Short URLs at
  once, identify invalid and duplicate lines, then atomically admit every ready
  item under one durable expandable queue parent.
- **FIFO queue** — configure 1–10 concurrent downloads; the default is 2.
- **Explicit lifecycle controls** — each queue row presents only the Pause,
  Resume, Cancel, Retry, Start again, source, Open, or removal actions authorized
  by the application; Pause All is available for eligible queued work.
- **Durable application state** — State v2 stores queue lifecycle, settings,
  reservations, history, and pending cleanup obligations.
- **Persistent download history** — completed downloads remain available across
  app launches, with search, file actions, expandable details, and removal
  controls.
- **Conservative recovery** — corrupt, unsafe, contended, unavailable, or
  indeterminate evidence does not authorize automatic destructive mutation.
- **Destination reservations** — related artifacts for a job receive one
  collision decision, and an unrelated destination is not silently replaced.
- **External FFmpeg support** — VidStow detects FFmpeg and FFprobe on `PATH`,
  in Homebrew's default prefixes, or via a user-selected FFmpeg/FFprobe pair.
- **Focused engine composition** — the desktop application supports only its
  explicitly exposed YouTube workflow, not the engine's wider extractor catalog.

### Lifecycle boundaries

VidStow persists logical job, execution-attempt, and engine-session identities
separately. Pause requests are cooperative: a transfer may settle as paused,
while work already finalizing may complete. Resume and Retry enqueue work; reuse
of retained bytes depends on the exact engine release, selected protocol, and
valid session evidence. VidStow does not promise universal continuation or byte
reuse.

Cancel requests engine-session discard. If cleanup cannot be proved complete,
the application retains a cleanup obligation or reports that user action is
required instead of claiming success.

## Screenshots

<table>
  <tr>
    <td width="50%">
      <strong>Queue</strong><br>
      Review queue occupancy, lifecycle status, progress, and available actions.<br><br>
      <img src="docs/assets/screenshots/queue-lifecycle.png" alt="VidStow queue showing a completed video and its available actions">
    </td>
    <td width="50%">
      <strong>Playlist review</strong><br>
      Search a playlist, select entries, apply a range, and choose one output policy.<br><br>
      <img src="docs/assets/screenshots/playlist-review.png" alt="VidStow reviewing selected entries from the Blender Open Movies playlist">
    </td>
  </tr>
  <tr>
    <td width="50%">
      <strong>Download history</strong><br>
      Search completed downloads and open files or reveal them in the system file manager.<br><br>
      <img src="docs/assets/screenshots/downloads.png" alt="VidStow Downloads page showing search and file actions for a completed download">
    </td>
    <td width="50%">
      <strong>Settings</strong><br>
      Configure output behavior, queue concurrency, recovery policy, and FFmpeg.<br><br>
      <img src="docs/assets/screenshots/settings-lifecycle.png" alt="VidStow settings showing download, queue recovery, FFmpeg, and diagnostics controls">
    </td>
  </tr>
  <tr>
    <td colspan="2">
      <strong>Recovery-required state</strong><br>
      Unsafe or unreadable application state disables ordinary queue mutation
      and preserves available recovery evidence for review.<br><br>
      <img src="docs/assets/screenshots/recovery-required.png" alt="VidStow recovery-required screen preserving saved media and disabling automatic cleanup">
    </td>
  </tr>
</table>

## Project status

VidStow is beta software. Supported behavior is bounded by the exact
application and engine versions and by artifact-specific validation.

| Area | Supported boundary |
| --- | --- |
| Product | Beta; focused public YouTube video, Short, playlist, and 2–20 URL batch workflow |
| Package | [`v0.1.0-beta.5`](https://github.com/vidstow/vidstow/releases/tag/v0.1.0-beta.5) installs on Apple Silicon through the [`vidstow/tap`](https://github.com/vidstow/homebrew-tap) Homebrew cask |
| Source | Apache-2.0 source and self-build instructions |
| Queue | State v2 persistence, revision-checked lifecycle transitions, FIFO admission, and startup reconciliation |
| Engine | [ytdlp-go](https://github.com/tejasa97/ytdlp-go); `go.mod` pins `github.com/tejasa97/ytdlp-go v0.3.0` |
| Resume | Session reuse is evidence-dependent; no universal transfer continuation or guaranteed byte reuse |
| Updates | Manual downloads from GitHub Releases |

The underlying [`ytdlp-go`](https://github.com/tejasa97/ytdlp-go)
project has broader extractor and CLI capabilities. VidStow supports only the
workflow documented here and exposed by its desktop UI. The Go module path is
`github.com/tejasa97/ytdlp-go`.

## Prerequisites

- [Go 1.25.12](https://go.dev/dl/)
- [Node.js 22](https://nodejs.org/) with npm
- [FFmpeg and FFprobe](https://ffmpeg.org/download.html) on `PATH`, in
  Homebrew's default prefixes (`/opt/homebrew/bin` or `/usr/local/bin`), or a
  user-selected FFmpeg executable with matching FFprobe beside it
- [Wails CLI v2.13.0](https://wails.io/docs/gettingstarted/installation/)

Install the pinned Wails CLI:

```sh
go install github.com/wailsapp/wails/v2/cmd/wails@v2.13.0
```

## Run locally

```sh
git clone https://github.com/vidstow/vidstow.git
cd vidstow

cd frontend
npm ci
cd ..

wails dev
```

## Build the desktop app

```sh
wails build
```

The native artifact is written beneath `build/bin/`. A JavaScript helper used
by the pinned engine is built and verified by the repository's packaging hooks;
it must remain in the location expected by the resulting application bundle or
binary layout.

## How it works

```mermaid
flowchart LR
    UI["Svelte UI"] --> Bridge["Wails bridge"]
    Bridge --> Admission["Admission and output planning"]
    Bridge --> Manager["Queue manager"]
    Admission --> State["State v2 store"]
    Manager --> State
    Manager --> Engine["ytdlp-go engine"]
    Engine --> Session["Engine session workspace"]
    Engine --> FFmpeg["FFmpeg / FFprobe"]
    Session --> Output["Reserved output"]
    FFmpeg --> Output
```

VidStow owns the desktop workflow, queue scheduling, State v2 application
lifecycle, destination reservations, recovery presentation, and UI. The engine
owns extraction, transport, protocol-specific session evidence, media
processing, and output-publication primitives. State v2 and an engine session
manifest are separate authorities; neither substitutes for the other.

The manager produces the queue view and action capabilities consumed by the
frontend. The frontend presents those capabilities rather than inferring
lifecycle authority from status text.

## Local data and privacy

VidStow stores application state in the operating system's per-user
configuration directory. State v2 can include:

- settings, including the selected download folder and an optional FFmpeg path;
- canonical public YouTube watch URLs and video IDs;
- display metadata such as title, channel, duration, and selected quality;
- job, attempt, session, queue, lifecycle, reservation, and cleanup records;
- completed-download history, including output paths and media metadata; and
- a separate owner-only diagnostic history, bounded to seven days, 200 typed
  events, and 1 MiB. It contains sanitized failure categories and no YouTube
  URLs, media URLs, arbitrary error text, or absolute paths.

The local diagnostic history itself stays on the device, can be cleared from
Settings, and is included in **Copy Diagnostics** only as a bounded list of
sanitized problem events. Automatic diagnostics remain off unless the user
explicitly selects **Send diagnostics**. When enabled, a separate bounded
outbox sends newly observed, allowlisted terminal-failure reports in the
background; disabling automatic diagnostics deletes anything waiting to be
sent.

Engine session work is stored beneath an engine-owned hidden directory in the
selected output root.

State v2 must not persist media delivery URLs, request headers, cookies,
credentials, signed query parameters, or media encryption keys. VidStow does
not require a hosted account and does not provide cloud sync. Normal operation
contacts YouTube and its media or thumbnail hosts. If automatic diagnostics are
explicitly enabled, VidStow also contacts `diagnostics.vidstow.workers.dev` to
submit the bounded reports described above. VidStow may start the locally
installed FFmpeg/FFprobe tools when the selected output requires them.

Errors shown by lifecycle and recovery views are bounded for presentation, but
users should review diagnostic material before sharing it.

## Development

Run the validation suite before submitting a change:

```sh
cd frontend
npm ci
npm run check
npm run test:ui
npm run build
cd ..

go mod tidy -diff
go vet ./...
go test -count=1 ./...
go test -race -count=1 ./...
go build ./...
```

See [Architecture](docs/ARCHITECTURE.md) for ownership and trust boundaries,
and [CONTRIBUTING.md](CONTRIBUTING.md) for project conventions and
dependency boundaries. Generated Wails bindings under `frontend/wailsjs/` are
intentionally not tracked.

## Scope and limitations

VidStow does not support:

- channels, search, live streams, or authenticated workflows;
- sites other than YouTube;
- DRM decryption or access-control circumvention;
- universal resumability or byte reuse when media equivalence is unproved;
- treating cleanup, publication, or recovery as successful when evidence is
  uncertain;
- automatic updates or packages outside macOS Apple Silicon; or
- feature parity with yt-dlp or the broader `ytdlp-go` CLI.

## Responsible use

Use VidStow only for content you own or are authorized to download. You are
responsible for following applicable laws, rights-holder permissions, and
platform terms.

VidStow is an independent open-source project. It is not affiliated with,
endorsed by, or sponsored by YouTube, Google, yt-dlp, or any supported service.

## License

Licensed under the [Apache License 2.0](LICENSE). See [NOTICE](NOTICE) for
attribution details.
