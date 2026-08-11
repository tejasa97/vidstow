# Decision 0001: VidStow concurrency and lifecycle v1

Status: Accepted

Date: 2026-08-11

Applies to: First reliable concurrency/lifecycle release

## Context

VidStow already supports a configurable 1–10 worker FIFO queue with a default
of 2. Reliable Pause, Resume, Cancel, Retry, shutdown, recovery, and output
publication introduce product choices that must remain stable while the first
combined implementation is built.

This record freezes those choices for the first release. It is intentionally
separate from implementation tickets so future planning changes do not erase
the original product contract.

## Accepted decisions

| Area | First-release decision | Why |
| --- | --- | --- |
| Destination ownership | Persist exact reservations and publish with no-replace semantics | Prevents concurrent jobs or external files from being silently overwritten |
| Late destination collision | Move the job to Action required and offer a deterministic ` (2)` name | Keeps the filename change visible and preserves the existing file |
| Identity | Keep job, attempt, and resumable session identities separate | Avoids making retry history, event deduplication, and cleanup depend on one overloaded ID |
| Queue order | Resume, Retry, and Download again join the FIFO tail | Preserves the scheduler users already have and avoids hidden priority rules |
| Slot lifetime | Waiting for FFmpeg and finalization continue to occupy the job's worker slot | Keeps occupancy truthful and prevents a second phase scheduler from appearing implicitly |
| Persistence cadence | Lifecycle revisions exclude speed, ETA, byte counters, and repeated progress events | Avoids write amplification and stale user-action revisions |
| Restoration | Always restore interrupted work as paused; never auto-start it | Makes ordinary quit reliable without hiding or abandoning saved work |
| Restoration setting | No restoration toggle in the first release | A disabled state has no safe, obvious meaning while preserved session data exists |
| Window close | Closing the only window runs ordinary pause-and-quit | Provides one understandable shutdown model; background/tray behavior is out of scope |
| Destructive quit | Do not ship Cancel downloads and quit in the first release | Avoids accidental loss while the safe lifecycle is being established |
| Download again | Create a fresh job, attempt, and session; keep the canceled row until cleared | Preserves truthful history and enforces Cancel's promise to discard the old session |
| Pause All | Permit mixed Paused and Completed outcomes | A finalizing job may complete when publication has already won |
| Recovery failure | Block destructive queue/session actions and preserve evidence | Unknown or corrupt State cannot safely authorize resume, cleanup, or deletion |

## Consequences

- The existing FIFO scheduler remains the only job scheduler.
- Ordinary quit and crash restoration are conservative: work returns paused.
- Publication may stop for user input rather than replacing or silently
  renaming an externally occupied destination.
- Finalization can temporarily reduce transfer throughput because it keeps the
  worker slot. Correctness and simple accounting take priority in v1.
- Canceled and replacement jobs may coexist visibly until completed rows are
  cleared.
- The first-release Settings and Quit interfaces are intentionally simpler
  than the earlier exploratory mockups.

## Candidates for later releases

These are not commitments. Each requires evidence, an explicit product
decision, updated acceptance tests, and a new decision record that supersedes
the relevant part of this one.

1. Destructive Cancel downloads and quit with a second confirmation and clear
   cleanup outcomes.
2. A restoration preference, but only after the disabled behavior has a safe,
   discoverable definition that cannot hide retained work.
3. Background or tray operation with a distinct close-versus-quit contract.
4. Separate transfer and processing capacity if profiling proves that holding
   worker slots through FFmpeg materially harms real workloads.
5. Explicit Retry priority or fair scheduling if VidStow deliberately moves
   away from strict FIFO.
6. Automatic late filename re-reservation if user research shows that the
   Action required step adds friction without improving trust.
7. More detailed recovery and repair tooling after the fail-closed path has
   proven safe across macOS, Windows, and Linux.

## Change policy

Do not edit this record to make a later release appear to have always used a
new policy. Add a numbered decision record that identifies the superseded
decision, the evidence for changing it, migration behavior, UI changes, and
new acceptance coverage.

## Related material

- [Confirmed concurrency and lifecycle plan](../CONCURRENCY_LIFECYCLE_PLAN.md)
- [Decision 0002: VidStow V1 protocol scope](0002-vidstow-v1-protocol-scope.md)
- [Technical implementation plan](../CONCURRENCY_LIFECYCLE_TECHNICAL_PLAN.md)
- [Concurrency and lifecycle mockups](../mockups/concurrency-lifecycle/)
