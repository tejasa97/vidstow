# Decision 0001: VidStow concurrency and lifecycle v1

Status: Accepted

Date: 2026-08-11

## Context

Reliable Pause, Resume, Cancel, Retry, shutdown, recovery, and output
publication require stable ownership and lifecycle rules across the desktop
application and engine.

## Decision

| Area | Decision | Rationale |
| --- | --- | --- |
| Destination ownership | Persist exact reservations and publish with no-replace semantics | Concurrent jobs and external files must not be silently overwritten |
| Late destination collision | Move the job to Action required | A filename change or destructive action requires explicit authority |
| Identity | Keep job, attempt, and resumable session identities separate | Retry history, stale-event rejection, and cleanup have different lifetimes |
| Queue order | Resume and Retry re-enter the FIFO queue | The manager retains one visible scheduling rule |
| Slot lifetime | Processing and finalization remain part of the worker lifetime | Occupancy reflects manager-owned work until the runner settles |
| Persistence cadence | Lifecycle revisions exclude speed, ETA, byte counters, and repeated progress events | Telemetry must not invalidate action authority or amplify writes |
| Restoration | Restore interrupted durable rows without treating prior occupancy as live | Only a worker in the current process can occupy a slot |
| Window close | Closing the application uses bounded shutdown | Queue admission and runner settlement have one shutdown authority |
| Pause All | Permit mixed Paused and Completed outcomes | Publication may already have won for finalizing work |
| Recovery failure | Block destructive queue and session actions and preserve evidence | Unknown state cannot safely authorize reuse, cleanup, or deletion |

## Consequences

- The queue manager is the only in-process scheduler.
- Application lifecycle state and engine resume manifests remain separate
  authorities.
- Publication may stop for user action instead of overwriting or silently
  renaming an occupied destination.
- A runner retains occupancy through transitional work until settlement.
- Recovery reports uncertainty instead of inventing a successful pause,
  cancellation, cleanup, or publication result.

## Related material

- [Current architecture](../ARCHITECTURE.md)
- [Decision 0002: VidStow protocol scope](0002-vidstow-v1-protocol-scope.md)
