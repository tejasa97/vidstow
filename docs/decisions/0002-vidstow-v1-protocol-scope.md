# Decision 0002: VidStow V1 protocol scope

Status: Accepted

Date: 2026-08-11

Applies to: First reliable concurrency/lifecycle release

## Context

The engine contains and continues to develop several media transport paths.
VidStow V1 needs a bounded, testable set whose Pause/Resume, finalization,
publication, and recovery behavior can be proven on every supported platform.
Experimental engine capability is not automatically a supported desktop
product workflow.

## Decision

VidStow V1 includes these resumable engine paths:

1. direct HTTP media;
2. multi-track download plus FFmpeg merge/processing;
3. finite HLS VOD; and
4. static DASH.

SABR/UMP remains experimental engine work. It is excluded from VidStow V1,
must not be selectable through a VidStow output plan, and is not an engine E3,
E5, combined-test, or VidStow release gate.

Live HLS, dynamic DASH, and other live workflows also remain outside VidStow
V1. Their within-run engine behavior must not be presented as durable desktop
resume support.

## Consequences

- Engine SABR/UMP work may continue independently without blocking VidStow.
- VidStow output-plan filtering must fail closed if an analyzed plan would
  require an excluded protocol.
- Release evidence and fixtures cover direct, multi-track/FFmpeg, finite HLS
  VOD, and static DASH only.
- Documentation must distinguish engine experimentation from supported VidStow
  behavior.

## Reconsideration

A later decision may add SABR/UMP or live workflows only after they have:

- stable public engine contracts;
- durable identity and credential-free checkpoint semantics;
- staged no-replace publication and crash reconciliation;
- native Windows, macOS, and Linux evidence; and
- an explicit VidStow UX and output-plan acceptance decision.

## Related material

- [Decision 0001: concurrency and lifecycle v1](0001-concurrency-lifecycle-v1.md)
- [Confirmed product plan](../CONCURRENCY_LIFECYCLE_PLAN.md)
- [Technical implementation plan](../CONCURRENCY_LIFECYCLE_TECHNICAL_PLAN.md)
