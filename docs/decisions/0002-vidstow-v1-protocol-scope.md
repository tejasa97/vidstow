# Decision 0002: VidStow protocol scope

Status: Accepted

Date: 2026-08-11

## Context

The engine contains more transport behavior than the focused desktop product
exposes. Engine capability is not automatically supported VidStow behavior.
The desktop boundary must remain explicit and testable.

## Decision

VidStow's durable desktop session boundary covers:

1. direct HTTP media;
2. separate-track download plus FFmpeg processing;
3. finite HLS VOD; and
4. static DASH.

Experimental SABR/UMP behavior and live workflows are outside the current
VidStow product scope. Within-run engine behavior for an excluded protocol must
not be presented as durable desktop resume support.

## Consequences

- Output planning must fail closed when a selection requires an excluded
  protocol.
- User documentation distinguishes engine capability from behavior exposed and
  supported by VidStow.
- Validation claims for VidStow are limited to the protocols listed above.

## Related material

- [Current architecture](../ARCHITECTURE.md)
- [Decision 0001: concurrency and lifecycle v1](0001-concurrency-lifecycle-v1.md)
