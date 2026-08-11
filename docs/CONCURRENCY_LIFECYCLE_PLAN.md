# VidStow concurrency and lifecycle plan

Status: Confirmed on 2026-08-11

Scope: VidStow queue, lifecycle, persistence, shutdown, recovery, and output publication

Implementation: Not authorized by this document

The first-release product decisions are recorded in
[Decision 0001](decisions/0001-concurrency-lifecycle-v1.md). Later releases
must add a superseding decision record rather than silently rewriting this
baseline.

The first-release protocol boundary is recorded in
[Decision 0002](decisions/0002-vidstow-v1-protocol-scope.md): direct,
multi-track/FFmpeg, finite HLS VOD, and static DASH are in scope; experimental
SABR/UMP and live workflows are not VidStow V1 dependencies or release gates.

The cross-repository implementation is specified separately in the draft
[technical implementation plan](CONCURRENCY_LIFECYCLE_TECHNICAL_PLAN.md).

## Product goal

VidStow already has a configurable multi-worker FIFO queue. This plan makes
that queue trustworthy when several downloads are paused, resumed, canceled,
retried, finalized, restored, or published at the same time.

This is not a new scheduler project. The existing queue manager remains the
single scheduling authority.

## Product invariants

- Download concurrency remains 1–10, with a default of 2.
- Scheduling remains FIFO and fills every available worker slot.
- Lowering the limit never interrupts active jobs. Occupancy drains to the new
  limit before more work starts.
- A job occupies its slot until its runner has exited and released its session
  lease, including while it is pausing, canceling, or finalizing.
- Pause preserves validated resumable work.
- Cancel discards resumable work.
- The UI reports the publication winner. A job that completes publication
  while Cancel is being requested is Completed, not Canceled.
- Failed jobs expose Retry. A canceled job exposes Download again and starts
  with fresh execution and resume identities.
- Ordinary quit pauses active work and preserves recoverable state.
- Two jobs must not publish to the same destination, and VidStow must never
  silently replace an unrelated file.
- Durable persistence tracks lifecycle intent and recovery evidence. Speed,
  ETA, and high-frequency progress are telemetry, not lifecycle revisions.

## State model

Do not encode every visible phrase in one status field. Each job has three
independent dimensions.

| Dimension | Purpose | Examples |
| --- | --- | --- |
| Durable lifecycle | Recovery and allowed actions | pending, active, pausing, paused, canceling, failed, canceled, completed, action-required |
| Presentation phase | Current user-facing activity | preparing, downloading, waiting-for-processing, finalizing, cleaning-up |
| Scheduler occupancy | Manager-owned live fact | occupies a worker slot: true or false |

Rules:

- `occupiesSlot` is derived from the live manager and is never restored from
  disk as authoritative state.
- Pausing and canceling remain occupied until the runner exits.
- Waiting and paused jobs do not occupy slots.
- Finalization and waiting for the existing FFmpeg processing semaphore remain
  part of the active job lifetime and occupy a worker slot.
- Tombstone cleanup may continue after slot release when it no longer owns
  runner or publication resources.
- After a crash, persisted active or transitional jobs are reconciled against
  engine evidence before becoming paused, completed, canceled, or
  action-required.

## Identity model

Use separate identities internally even if the UI presents one queue row.

| Identity | Lifetime | Use |
| --- | --- | --- |
| Job ID | Logical queue/history record | UI actions, ordering, history, idempotency |
| Attempt ID | One execution attempt | Diagnostics, event deduplication, stale-event rejection |
| Session ID | One resumable workspace | Checkpoints, leases, cleanup, recovery |

- Pause and Resume retain the job and session and start a new attempt.
- Failed Retry retains the job and may retain the session only after complete
  resume validation. It always starts a new attempt.
- Download again after Cancel creates a new job, attempt, and session. It may
  keep a non-authoritative link to the prior job for history.
- Transport URLs, tokens, cookies, and other expiring credentials are never
  identity.

## Destination reservation and publication

Proposed policy for confirmation:

1. Resolve the output template and metadata-derived filename.
2. Canonicalize the output root and destination using platform-aware path and
   case rules.
3. Reserve the exact destination before writing persistent output.
4. Persist the reservation with the job and session.
5. Resolve in-app collisions deterministically with ` (2)`, ` (3)`, and so on.
6. Publish with an atomic no-replace operation.
7. If an external file appears after reservation, do not overwrite it. Move
   the job to action-required so the user can accept a new reserved name or
   cancel.

The reservation registry prevents VidStow jobs from colliding. Atomic
no-replace publication protects against files created or changed outside
VidStow. A per-job publication arbiter still decides the Cancel-versus-publish
winner for that job.

## Queue and action behavior

### Scheduling

- New jobs enter the FIFO tail.
- Resume and Retry re-enter the FIFO tail; neither jumps ahead of existing
  waiting work.
- Download again is a fresh job at the FIFO tail.
- With limit 2, canceling or pausing one active job does not start the next job
  until the prior runner releases its slot.

### Pause

- Pending job: move directly to paused and remove it from FIFO eligibility.
- Active job: durably accept Pause, show Pausing, signal the typed pause cause,
  then become paused only after runner exit and checkpoint settlement.
- Finalizing job: request interruption at the engine-defined safe boundary. If
  publication already won, report Completed.

### Pause All

- Pending jobs become paused immediately.
- Active jobs transition to Pausing and retain their slots until runner exit.
- Finalizing jobs may pause safely or complete if publication wins.
- The command reports accepted, settled, and exceptional outcomes; it does not
  claim that every job paused synchronously.

### Cancel

- Durably accept Cancel before signaling a local runner.
- Show Canceling while the runner exits and resumable state is discarded.
- If publication wins first, report Completed.
- Cleanup failure leaves the job canceled but creates an observable cleanup
  tombstone and warning.

### Retry and Download again

- Failed + validated session: Retry using the same logical job and session,
  with a new attempt.
- Failed + invalid session: discard or quarantine invalid resume data and
  restart safely under a new session, while retaining the logical job.
- Canceled: expose Download again, which creates fresh identities and performs
  normal destination reservation.

## Persistence model

State v2 is a versioned, copy-on-write document protected by a stable sibling
lock. Its successful atomic replacement is the commit point.

Durably commit:

- accepted lifecycle commands and expected revisions;
- FIFO order and concurrency setting;
- job, attempt, and session references;
- canonical output root and destination reservation;
- runner start and terminal settlement;
- pause/cancel/publication winner outcomes;
- retryability and action-required reasons;
- cleanup and quarantine tombstones; and
- completion plus history transition.

Do not advance the lifecycle revision for:

- byte counters;
- speed or ETA;
- animation or presentation-only messages; or
- repeated progress events.

Progress remains in memory. An optional coarse snapshot may be persisted on a
separate cadence for display continuity, but it must not invalidate user-action
preconditions or claim resumable authority. Engine manifests and checkpoints
remain authoritative for resumable bytes.

## Startup and recovery

1. Open and validate State v2 before starting resumable work.
2. If State is corrupt, unsupported, or migration fails, preserve the original
   bytes and enter blocking recovery-required mode. Do not default and rewrite.
3. Reconcile jobs against session manifests, leases, publication evidence,
   reservations, and tombstones.
4. Resolve publication-winner evidence before applying stale cancel intent.
5. Restore waiting, paused, and retryable failed jobs as non-running work.
6. Never restore occupied slots; only a newly started live runner occupies one.
7. Run safe cleanup and orphan collection only after State and live references
   are known to be healthy.

Restoration is always enabled for the first implementation. Do not expose a
toggle whose disabled state could hide or abandon preserved work. Interrupted
jobs are restored as paused and never start automatically.

## Shutdown policy

Proposed first-release behavior:

- Closing the only window follows the ordinary quit flow; background/tray
  operation is out of scope.
- Ordinary quit stops admission, pauses active jobs using one shared bounded
  deadline, persists settled outcomes, and then exits.
- Jobs that cannot settle by the deadline remain recoverable. The UI must not
  claim they were safely paused without evidence.
- The quit dialog offers Keep working and Pause downloads and quit.
- Destructive Cancel downloads and quit is deferred beyond the first release.
- Waiting and already-paused jobs remain preserved.

## User interface plan

The visual language remains the current VidStow design system: 160 px dark
graphite sidebar, light neutral canvas, restrained blue accent, Inter/system
type, 8 px spacing rhythm, 6–8 px radii, fine gray borders, and minimal shadow.

### UI-01 — Queue overview and truthful occupancy

- Keep `N jobs · M running` only if Running means lifecycle-active jobs.
- Add a separate `X of Y active slots` summary based on manager occupancy.
- Show counts for waiting, paused, and transitional work without overloading
  the slot count.
- Show FIFO labels: Next, Position 2, Position 3.
- Show independent progress, speed, ETA, phase, and actions per job.

### UI-02 — Transitional rows and Pause All

- Add Pausing, Canceling, Finalizing, and Cleaning up presentation.
- Disable actions that conflict with an accepted transition.
- Keep the row and slot visibly occupied until runner exit.
- Pause All reports that requests were accepted, then rows settle
  independently. Finalizing work may complete.

### UI-03 — Failed, canceled, and action-required outcomes

- Failed rows expose Retry.
- Canceled rows expose Download again; do not claim resumable data was kept.
- Destination collisions and indeterminate recovery use Action required with a
  clear explanation and safe actions.

### UI-04 — Queue settings

- Keep Concurrent downloads with values 1–10 and default 2.
- Explain that reducing the value does not interrupt current downloads.
- Show Interrupted jobs as a fixed product behavior: Restored as paused.
- Explain that jobs never restart automatically when VidStow opens.
- Do not expose a restoration toggle in the first release.

### UI-05 — Quit confirmation

- State how many active jobs will be paused and how many waiting/paused jobs
  are already safe.
- Primary action: Pause downloads and quit.
- Secondary action: Keep working.
- Do not expose destructive Cancel downloads and quit in the first release.
- Do not mention background or tray behavior.

### UI-06 — Recovery-required state

- Block resumable queue mutation when State cannot be trusted.
- Explain that media and recovery evidence were preserved.
- Offer Copy diagnostics and Open data folder.
- Do not offer Resume, Retry, Cancel, cleanup, or reset as though they were
  safe.

### UI-07 — Destination conflict

- Explain that the reserved filename is no longer available.
- Show the existing name and proposed collision-free name.
- Primary action: Use new name.
- Secondary action: Cancel download.
- Never offer silent Replace as the default recovery action.

Mockups live under `docs/mockups/concurrency-lifecycle/` and map one-to-one to
the UI identifiers above. UI-01 and UI-02 may share one queue overview image
only if all states and slot semantics remain readable.

| UI | Mockup |
| --- | --- |
| UI-01 | [Queue overview and occupancy](mockups/concurrency-lifecycle/ui-01-queue-overview.png) |
| UI-02 | [Transitional states and Pause All](mockups/concurrency-lifecycle/ui-02-transitional-states.png) |
| UI-03 | [Failed, canceled, and action-required outcomes](mockups/concurrency-lifecycle/ui-03-outcomes.png) |
| UI-04 | [Queue and recovery settings](mockups/concurrency-lifecycle/ui-04-queue-settings-v2.png) |
| UI-05 | [Ordinary quit confirmation](mockups/concurrency-lifecycle/ui-05-quit-confirmation-v3.png) |
| UI-06 | [Blocking recovery-required state](mockups/concurrency-lifecycle/ui-06-recovery-required.png) |
| UI-07 | [Destination conflict resolution](mockups/concurrency-lifecycle/ui-07-destination-conflict.png) |

## Engine and application boundaries

The `youtube_dlp` engine owns:

- typed interruption causes;
- resumable session workspaces, manifests, checkpoints, and leases;
- protocol-specific resume validation;
- staged output construction;
- atomic publication primitives and publication arbitration; and
- safe session cleanup operations.

VidStow owns:

- the FIFO worker pool and concurrency setting;
- job, attempt, and session references;
- destination reservations across jobs;
- durable queue/history/tombstone transactions;
- lifecycle commands, revisions, and user-visible outcomes;
- shutdown orchestration; and
- all queue, settings, recovery, and conflict UI.

No VidStow code may import an engine `internal` package. Shared behavior must be
provided through a public engine contract or implemented locally against
documented semantics and shared test vectors.

## Implementation sequence

This sequence is intentionally ticket-independent.

1. Confirm this product contract and every decision listed below.
2. Finalize public engine interruption, session, staged-output, no-replace
   publication, and cleanup contracts.
3. Implement VidStow State v2, migration, revisions, reservations, and recovery
   mode without changing queue behavior.
4. Integrate typed lifecycle commands into the existing manager while
   preserving FIFO and concurrency behavior.
5. Add the UI states and flows defined in UI-01 through UI-07.
6. Validate combined multi-job behavior, crash recovery, shutdown, publication
   races, and platform filesystem behavior.
7. Update user documentation only after the behavior and UI are proven.

## Acceptance scenarios

At minimum, prove:

1. default 2 and every accepted limit from 1 through 10;
2. FIFO ordering with more jobs than slots;
3. lowering 4 to 1 without interrupting four active jobs;
4. Pause releasing a slot only after runner and lease exit;
5. Cancel at transfer and publication boundaries;
6. Pause All across running, waiting, and finalizing jobs;
7. ordinary shutdown with several active jobs and one shared deadline;
8. failed Retry at the FIFO tail with validated and invalid session state;
9. canceled Download again receiving fresh identities;
10. two jobs resolving the same filename;
11. an external file appearing after destination reservation;
12. restart with paused, failed, pausing, canceling, and publication-winner
    evidence;
13. independent sessions sharing an output root without global serialization;
14. State v2 under concurrent progress streams without revision churn;
15. frontend counts matching backend lifecycle and occupancy; and
16. native macOS, Windows, and Linux path, lock, and atomic publication tests.

## Confirmed decisions

- [x] Destination reservations are durable and publication is no-replace.
- [x] Late destination collisions become action-required and never overwrite.
- [x] Job, attempt, and session identities are separate.
- [x] Resume, Retry, and Download again join the FIFO tail without priority.
- [x] Waiting for FFmpeg and finalizing continue to occupy a worker slot.
- [x] Lifecycle revisions exclude high-frequency progress telemetry.
- [x] Interrupted work is always restored as paused and never auto-started.
- [x] The first release exposes no restoration toggle.
- [x] Closing the only window uses ordinary pause-and-quit behavior.
- [x] Destructive Cancel downloads and quit is deferred beyond the first release.
- [x] Download again creates a fresh job while the canceled row remains until cleared.
- [x] Pause All may produce mixed Paused and Completed outcomes.
- [x] Recovery-required mode blocks all destructive queue/session actions.
- [x] The seven UI surfaces and their exact actions/copy are approved, subject
  to the confirmed UI-04 and UI-05 revisions.

This confirmation authorizes implementation planning, but not code changes by
itself. Implementation begins only through a separately authorized assignment.
