# VidStow diagnostic telemetry v1

## Purpose

Opt-in diagnostics answer one question: **why could VidStow not complete a
user-requested download?** They also record app integrity failures that can
prevent safe operation. They are not usage analytics.

## Consent and local storage

Automatic transmission is off until the user explicitly selects **Send
diagnostics**. Neither consent choice is preselected. Dismissal leaves sending
off. Disabling it immediately deletes the automatic outbox.

A separate owner-only local history supports **Copy diagnostics** and **Clear
diagnostic history**. It is capped at 200 events, 1 MiB, and seven days.

The automatic outbox is a separate owner-only queue. It is capped at 100
events, 512 KiB, and seven days. It is best effort: failure, corruption, or
transport errors must never affect a download and must not create telemetry.

## Eligible events

Send one event only after a terminal outcome prevents a valid requested
download from completing. Valid categories are closed by
[`event-v1.schema.json`](event-v1.schema.json):

- extraction/access failures, including typed 403, 429, authentication,
  restriction, unavailable, network, and extractor failures;
- media transfer, resume, remote-content, and incomplete-transfer failures;
- helper/EJS, FFmpeg, output filesystem, persistence, and recovery failures;
- unhandled frontend faults, panics, and typed unexpected internal failures.

Do **not** send malformed or unsupported input before admission; cancellation,
pause, removal, retry, dismissal, progress, success, or an error that later
recovers. Do not send a separate event for telemetry failure.

## Payload and privacy

Every event contains only:

- random UUIDv4 event and per-launch session IDs;
- app and engine version;
- coarse OS major version and architecture;
- typed stage, category, terminal outcome, retry bucket, and optional duration
  bucket.

There is no installation or device ID. `operation_id` is used locally for
deduplication and is never serialized. Automatic events must never include
URLs, resource IDs, paths, filenames, titles, channels, thumbnails, cookies,
headers, tokens, raw errors, stack traces or fingerprints, player data, helper
input/output, or arbitrary metadata.

Classification is constructed from typed component boundaries. Producers must
not inspect or transmit `err.Error()` text.

## Limits

- one event per local operation and `(stage, category)`;
- only terminal failures, including startup/recovery failures that prevent safe operation;
- at most 10 events per app launch;
- at most 3 events per `(stage, category)` per launch;
- upload batches contain at most 20 events or 64 KiB.

No health summaries are part of v1. Successful operations, cache statistics,
and timing distributions are not uploaded.

## Transport and service

The uploader uses HTTPS, bounded requests, exponential backoff with jitter,
and `event_id` acknowledgement. It starts only after the app is responsive,
removes only acknowledged events, and does not synchronously flush during
shutdown. The server must separately enforce payload validation, retention,
and abuse limits; client session IDs are never a security boundary.
