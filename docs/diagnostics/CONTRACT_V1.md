# Diagnostic telemetry contract v1

This document is the normative privacy, event, transport, retention, and abuse
contract for VidStow diagnostic telemetry v1. It describes approved future
behavior; shipping code must not claim conformance until the corresponding
client and service controls are implemented and tested.

Normative terms such as **must**, **must not**, and **may** are intentional.

## 1. Goals

The system exists to answer operational questions:

- Which application stage failed, retried, degraded, or recovered?
- Does a problem correlate with an app, engine, operating-system, or
  architecture version?
- Are repeated failures associated with the same YouTube resource?
- What proportion of operations complete?
- What are EJS cache hit, miss, invalidation, and timing distributions?
- Do retries and recovery paths prevent terminal failures?

It is not a behavioral analytics system. It must not measure viewing habits,
retention, engagement, feature funnels, or advertising attribution.

## 2. The three data paths

### 2.1 Local diagnostic history

The backend diagnostic recorder receives only structured, sanitized events.
It keeps no more than 200 events, 1 MiB, or seven days of history, whichever
limit is reached first. Storage must be owner-only where the operating system
supports permissions, bounded before decoding or writing, and replaced
atomically. Corruption is quarantined or discarded; it must not prevent app
startup or an operation.

Local history is available regardless of automatic-transmission consent so
that **Copy diagnostics** and manual reports remain useful. It must not contain
an original YouTube page URL. A manual report obtains a selected operation's
URL from normal application state only while constructing its preview.

Settings must provide **Clear diagnostic history**. Clearing is distinct from
disabling automatic transmission.

### 2.2 Automatic telemetry

Automatic transmission is disabled until the user makes an explicit choice.
When enabled, eligible events are copied into a separate bounded outbox. The
uploader batches and sends them without blocking startup, analysis, transfer,
postprocessing, persistence, shutdown, or user interaction.

Disabling automatic telemetry must immediately delete the pending outbox. It
does not delete local diagnostic history; the UI must explain the distinction.

### 2.3 Manual problem reports

Manual submission is available whether or not automatic telemetry is enabled.
The report builder lets the user:

1. select an affected recent operation;
2. provide an optional description;
3. optionally provide contact information, blank by default;
4. include or remove the original YouTube page URL;
5. include or remove recent sanitized diagnostic events;
6. inspect the complete payload in a readable preview; and
7. submit or copy it.

The URL is visibly selected by default but must be removable. Submission
returns a human-readable report reference. A failed submission preserves the
preview and copy-to-clipboard fallback; it must not silently upload the user's
free-form description later.

## 3. Consent copy and behavior

The first-run choice must communicate at least:

> Help improve VidStow by sending operational diagnostics when downloads or
> app operations encounter problems. Reports may include the affected YouTube
> video identifier. They never include cookies, media download links, tokens,
> signatures, or downloaded filenames.

The choices are **Send diagnostics** and **Don't send**. Neither is preselected.
Dismissing the choice is equivalent to automatic transmission remaining off.
The setting is changeable later.

A privacy notice must be linked from the choice and Settings before a release
enables transmission. It must identify Cloudflare as an infrastructure
processor, state the retention periods, distinguish automatic telemetry from
manual reports, explain resource identifiers, and provide a privacy contact or
manual-report deletion route.

## 4. Identity and correlation

The client must not create, read, infer, or transmit a stable installation or
device identifier for diagnostics. In particular it must not use a hardware
serial, advertising identifier, hostname, username, home path, MAC address,
keychain identity, account identity, or fingerprint derived from machine
properties.

The permitted random identifiers are:

- `session_id`: generated at each application launch;
- `operation_id`: generated for one analysis/download operation; and
- `event_id`: generated for one diagnostic event.

These identifiers must be UUIDv4 values and must not embed time, device, user,
or resource information. The service reports affected sessions, not affected
users or installations.

## 5. Event eligibility

Automatic telemetry may include:

- terminal operation failures;
- retries caused by a typed operational problem;
- degraded startup, persistence, extraction, helper, transfer,
  postprocessing, or filesystem states;
- recovery attempts and outcomes;
- unexpected internal failures and sanitized panic fingerprints; and
- bounded health summaries needed to calculate rates.

The following are not automatic failures:

- user cancellation;
- a user dismissing a dialog;
- ordinary queue operations;
- high-frequency progress, byte, speed, or ETA updates;
- expected local input validation before an operation is admitted; or
- telemetry transport failure itself.

A recovered problem is still eligible because it measures recovery behavior,
but it must state `recovered: true` and must not also be counted as a terminal
failure.

## 6. Automatic event envelope

The machine-readable definition is
[`event-v1.schema.json`](event-v1.schema.json). The same closed event shape is
used for eligible local records included in a manual report. Every automatic
event has:

- schema version `1`;
- event, session, and optional operation UUIDs;
- client occurrence time;
- app and engine versions;
- coarse platform information;
- exactly one typed event payload; and
- an optional canonical YouTube resource only where the schema permits it.

The service uses receipt time for retention and ordering authority. Client time
is diagnostic context only. Normative-shape examples are available for a
[`problem_observed`](examples/problem-event.json) event, a
[`health_summary`](examples/health-summary.json), and a
[manual report](examples/manual-report.json).

### 6.1 Problem events

`problem_observed` records one typed non-happy path:

- `stage` identifies where it happened;
- `category` is a stable safe code;
- `outcome` is `recovered`, `terminal`, or `degraded`;
- `retry_bucket` is `none`, `one`, `two`, or `three_plus`;
- `duration_bucket` is optional and coarse; and
- `panic_fingerprint` is optional only for internal panic categories.

A panic fingerprint is a non-reversible digest of normalized package/function
frames. Raw frames, arguments, source lines, and paths are forbidden.

### 6.2 Health summaries

`health_summary` contains bounded delta counters for one non-overlapping
session interval. It has no resource or operation identifier. Each summary
states its interval start and end; a later summary in the same session starts
at or after the preceding interval's end and does not repeat acknowledged
counts. Event-ID idempotency protects a retry after a lost acknowledgement.
Permitted counters are:

- operations started, completed, and terminally failed;
- retries and successful recoveries;
- EJS memory-cache hits and misses;
- EJS persistent-cache hits, misses, invalid, expired, read failures, and write
  failures; and
- coarse preprocessing and solve duration histograms.

A summary must not contain per-video success records. Counters saturate at the
schema maximum rather than overflowing. Implementations may emit summaries
periodically and at graceful shutdown, but telemetry must not add synchronous
shutdown work; a pending summary can be delivered on the next launch with its
original session ID.

### 6.3 Duration buckets

Durations use only these values:

- `lt_100ms`
- `100_499ms`
- `500_1999ms`
- `2_9s`
- `10_29s`
- `30_59s`
- `gte_60s`

Exact timestamps may appear only in the envelope. Exact operation durations are
not transmitted.

## 7. Stages and category ownership

Stages are:

- `startup`
- `persistence`
- `extraction`
- `ejs_preprocess`
- `ejs_solve`
- `helper`
- `media_transfer`
- `postprocessing`
- `filesystem`
- `frontend`
- `internal`

Initial categories are:

| Area | Categories |
| --- | --- |
| Network | `http_403`, `http_429`, `network_timeout`, `network_offline`, `dns_failure`, `tls_failure` |
| Extraction | `resource_unavailable`, `resource_restricted`, `authentication_required`, `unsupported_resource`, `extractor_failed` |
| EJS/helper | `helper_start_failed`, `helper_timeout`, `helper_crashed`, `helper_security_limit`, `preprocess_failed`, `solve_failed`, `invalid_solver_result` |
| Transfer | `range_rejected`, `resume_invalid`, `remote_content_changed`, `incomplete_transfer`, `transfer_failed` |
| Postprocessing | `ffmpeg_missing`, `ffmpeg_start_failed`, `ffmpeg_failed` |
| Filesystem | `permission_denied`, `disk_full`, `path_unavailable`, `destination_conflict`, `unsafe_path` |
| Persistence | `state_unavailable`, `state_corrupt`, `state_unsupported`, `state_contended`, `state_indeterminate` |
| Frontend/internal | `frontend_unhandled`, `backend_panic`, `unexpected_internal` |

Implementations should map typed errors at the component boundary. They must not
classify by uploading the error text. Adding a category requires a schema
revision or a backward-compatible schema release and corresponding server
support; clients must not improvise values.

## 8. YouTube resource handling

An automatic problem event may contain only:

```json
{
  "provider": "youtube",
  "resource_type": "video",
  "resource_id": "canonical identifier"
}
```

`resource_type` is `video`, `playlist`, or `channel`. The client derives the
identifier from a recognized YouTube page URL and validates it against bounded
provider-specific syntax: a video ID is exactly 11 URL-safe identifier
characters, a playlist ID is 2–128 such characters, and a canonical channel ID
is `UC` followed by exactly 22 such characters. It must not send the original
URL, query string, fragment, redirect target, embed parameters, search terms,
or an identifier extracted from a media delivery URL.

Resources are forbidden on health summaries. The service stores a keyed
fingerprint for short-term grouping and may store an authenticated-encrypted
recoverable identifier for authorized reproduction. Recoverable identifiers
expire after 30 days. Encryption and fingerprint keys live in server secret
storage, never in the desktop client or database.

A manual report may contain one original recognized YouTube page URL when the
user has seen it in the preview. It must still reject `googlevideo.com`, signed
media delivery URLs, and non-HTTP(S) schemes.

## 9. Forbidden fields and values

All three data paths must reject or omit:

- request or response headers and bodies;
- cookies, credentials, authorization values, session tokens, OAuth material,
  API keys, signatures, challenges, deciphered values, and encryption keys;
- player source, preprocessed player representations, helper input/output, or
  arbitrary JavaScript;
- media delivery URLs, signed query parameters, redirect chains, CDN hosts,
  byte-range URLs, or thumbnail URLs;
- arbitrary error strings or unbounded metadata maps;
- absolute paths, home-directory components, usernames, configured FFmpeg
  paths, destination paths, and downloaded filenames;
- title, channel name, description, captions, queue contents, history contents,
  clipboard contents, screenshots, or raw application state;
- raw stack traces, crash dumps, process environment, command lines, or memory
  contents;
- IP addresses supplied by the client; and
- installation or device identifiers.

Sanitizing arbitrary logs after collection is not a valid implementation. The
producer must construct an allowlisted value from typed data.

## 10. Local outbox and uploader

The automatic outbox is limited to 100 events, 512 KiB, and seven days. When a
new event would exceed a bound, deterministic oldest-first eviction applies.
The outbox uses owner-only permissions, bounded decoding, atomic replacement,
and one process-safe writer. Corruption or write failure drops telemetry rather
than affecting application state.

The uploader:

- sends no more than 20 events or 64 KiB per batch;
- uses HTTPS and a short request timeout;
- applies exponential backoff with jitter;
- attempts work only after the application is responsive;
- uses `event_id` for idempotent acknowledgement;
- removes only acknowledged events;
- does not extend an event's seven-day lifetime when retrying;
- does not flush synchronously during normal shutdown; and
- emits no recursive telemetry about telemetry failures.

Equivalent rapid events should be coalesced into a bounded count where doing so
does not erase a terminal outcome. A retry loop must not create an event storm.

## 11. Service API

The proposed v1 endpoints are:

### `POST /v1/events`

Request:

```json
{
  "schema_version": 1,
  "events": []
}
```

The service accepts at most 20 events and 64 KiB of decoded JSON. It validates
content type, nesting depth, lengths, UUIDs, enums, resource syntax, and unknown
fields before storage. A successful response has this bounded shape:

```json
{
  "accepted_event_ids": ["UUID"],
  "rejected": [{"event_id": "UUID", "code": "invalid_event"}]
}
```

Stable permanent rejection codes are `invalid_event`, `unsupported_schema`,
`invalid_resource`, and `expired_event`. The client removes accepted and
permanently rejected events. HTTP `429` and `5xx` responses retry with backoff;
other `4xx` responses are permanent for the whole batch. Duplicate event IDs
with the same canonical payload are acknowledged as accepted; a conflicting
payload for an existing ID is permanently rejected.

### `POST /v1/reports`

The service accepts one user-reviewed report, at most 256 KiB, matching
[`report-v1.schema.json`](report-v1.schema.json), with:

- build and coarse platform information;
- optional description, at most 4,000 Unicode scalar values;
- optional contact value, at most 320 values and blank by default;
- optional recognized YouTube page URL, at most 2,048 values;
- at most 50 sanitized local events; and
- optional automatic event IDs for correlation.

On creation it returns HTTP `201` with an opaque human-readable reference:

```json
{"report_reference": "VS-20260818-A7F3"}
```

Reports are idempotent using a client-generated report UUID. Repeating the same
UUID and canonical payload returns the original reference; a conflicting
payload is rejected. A free-form report is uploaded only after an explicit
submit action.

### Public-client security

A secret embedded in a desktop binary is not an authentication boundary. The
service therefore relies on strict schemas, payload limits, per-edge abuse
controls, idempotency, bounded database work, and operational rate limiting.
It must not place a browser challenge in the normal desktop path.

Cloudflare necessarily processes connection metadata such as source IP while
serving the request. Application code must not write source IP, forwarded IP
headers, or user-agent strings to D1. Cloudflare logging and retention settings
must be reviewed before production rollout and accurately reflected in the
privacy notice.

## 12. Central storage model

The initial service is a Cloudflare Worker in front of D1. Its logical v1
schema is:

| Table | Required data |
| --- | --- |
| `events` | event ID primary key; session and optional operation IDs; type; client and receipt times; app/engine versions; OS major and architecture; expiry |
| `problem_events` | event ID foreign key; stage; category; outcome; retry bucket; optional duration bucket and panic fingerprint |
| `event_resources` | event ID foreign key; keyed resource fingerprint; authenticated ciphertext; key version; 30-day expiry |
| `health_summaries` | event ID foreign key; interval bounds; every bounded counter and duration-histogram bucket defined by the schema |
| `reports` | report UUID primary key; unique human reference; client and receipt times; build/platform context; authenticated-encrypted private payload; keyed payload digest; key version; expiry |
| `report_automatic_events` | report UUID and explicitly correlated automatic event UUID, with a composite primary key |
| `daily_problem_aggregates` | day, build/platform dimensions, stage, category, outcome, and count; no session, operation, event, or resource identity |
| `daily_health_aggregates` | day, build/platform dimensions, summed counters and histograms; no session, operation, event, or resource identity |
| `schema_versions` | accepted schema version, deployment revision, activation time, and retirement time |

The manual report's optional description, contact, page URL, and validated
copied-event array form one bounded private payload. The Worker validates its
individual fields first and only then authenticated-encrypts the canonical
serialization. D1 stores no plaintext copy of those fields. The keyed digest
supports integrity and idempotency checks, not public lookup or correlation.

Foreign keys cascade from `events` to problem, resource, and health details.
Required indexes cover event/report expiry, event receipt time, problem
stage/category/version, resource fingerprint, report reference, and aggregate
day/version. No index or table represents an installation or user.

Database constraints repeat application validation, including enums, counter
bounds, lengths, and valid nullability for each event type. Unknown JSON
payloads are not retained as a fallback. Administrative access uses Cloudflare
Access or an equivalent independent identity layer and is not exposed through
the desktop client.

Routine dashboards use aggregates and resource fingerprints. Exact automatic
resource identifiers and the free-form/contact/URL portion of manual reports
are authenticated-encrypted with keys held outside D1. Decryption is limited
to an explicit diagnostic-detail path. Administrative access and decryption
attempts should be auditable.

## 13. Retention and deletion

| Record | Retention rule |
| --- | --- |
| Local diagnostic history | 7 days, 200 events, 1 MiB |
| Automatic outbox | 7 days, 100 events, 512 KiB |
| Recoverable automatic resource identifier | 30 days |
| Detailed automatic event | 90 days |
| Identifier-free daily aggregate | 12 months |
| Manual report | 180 days |

A scheduled server task deletes expired rows. Aggregation must not copy a
recoverable resource identifier into long-term storage. A manual report can be
deleted early using its report reference through the published privacy contact
or future self-service deletion route.

Because v1 has no account or installation identifier, the service cannot
reliably identify all automatic events originating from one person. The
privacy notice must say this rather than promise an unavailable account-wide
deletion function.

## 14. Manual preview requirements

The preview is generated from the exact submission object, not a parallel
human-readable approximation. It displays:

- app and engine versions;
- coarse platform information;
- description and contact information;
- the complete page URL if included;
- every included diagnostic event in readable form; and
- a concise list of categories that are always excluded.

Removing a field from the preview must remove it from the serialized request.
Tests must compare the preview model with the submitted model. The UI must not
claim that a report is anonymous when it contains a URL, resource identifier,
contact value, or free-form description.

## 15. Dashboard questions

The private v1 dashboard should answer:

- operation completion and terminal-failure rates by app and engine version;
- problem counts by stage, category, outcome, OS major, and architecture;
- retry and recovery rates;
- EJS memory and persistent-cache hit/miss/error rates;
- EJS preprocessing and solve duration distributions;
- resource fingerprints associated with repeated problems; and
- manual reports and their explicitly linked events.

It must say **sessions**, not **users** or **installations**. It must not expose a
feed of successful resources because health summaries contain no resource.

## 16. Threat model and required controls

| Threat | Required control |
| --- | --- |
| Accidental secret leakage | Typed producers, closed schema, forbidden-field tests, no arbitrary strings |
| Database disclosure | Minimize fields, encrypt recoverable IDs, short retention, authenticated admin access |
| Forged/spam events | Edge rate limits, strict validation, bounded writes, anomaly monitoring |
| Replay | Event/report UUID uniqueness and idempotent inserts |
| Event amplification | Client coalescing, bounded outbox, batch and server rate limits |
| Oversized/deep JSON | Limit raw and decoded bytes, nesting, arrays, strings, and counters before storage |
| Service outage | Best-effort background delivery; never affect application behavior |
| Malicious local file | Owner permissions, bounded decoding, schema validation, atomic replacement |
| Cross-session tracking | No installation ID; rotating session ID; limited retention |
| Misleading privacy UI | Preview exact payload; publish collection and processor notice |
| Telemetry recursion | Never generate telemetry because telemetry transport failed |

## 17. Conformance tests required before rollout

Client tests must prove:

- every allowed event serializes and every unknown field/category is rejected;
- forbidden sample secrets, URLs, paths, headers, and raw errors cannot enter an
  event;
- session IDs rotate and no installation identifier exists;
- disabling transmission deletes the outbox;
- local history remains independently clearable;
- bounds, expiry, eviction, corruption, atomicity, and concurrent access behave
  safely;
- uploader acknowledgement, backoff, cancellation, deduplication, and outage
  behavior cannot affect an operation;
- automatic resource parsing strips the original URL and rejects media URLs;
- health summaries contain no resource;
- manual preview and submitted payload are the same model; and
- manual submission never occurs without the submit action.

Service tests must prove:

- schema, size, nesting, enum, UUID, and resource validation;
- unknown fields fail closed;
- duplicate event/report IDs are idempotent;
- forbidden values are not retained;
- database constraints match request validation;
- retention removes recoverable identifiers before detailed events and removes
  aggregates and reports at their configured limits;
- aggregation cannot retain recoverable identifiers; and
- untrusted clients cannot cause unbounded CPU, memory, or database work.

Before beta rollout, an integration test must capture and inspect the exact
HTTPS requests for representative startup, extraction, EJS, transfer,
postprocessing, filesystem, panic, health-summary, opt-out, and manual-report
flows.

## 18. Deployment details still to assign

The approved product contract does not depend on these operational names:

- service repository and ownership;
- ingestion and dashboard hostnames;
- Cloudflare account/project identifiers;
- privacy contact route;
- alert thresholds; and
- key rotation procedure.

They must be assigned and documented before production rollout, but may not
weaken this contract without a recorded decision and schema/privacy review.
