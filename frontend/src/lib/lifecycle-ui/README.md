# Lifecycle UI foundation

These components define presentation-safe lifecycle contracts and executable
interaction behavior. They are intentionally not wired into VidStow's live
Queue, Settings, or dialog routes yet; that integration depends on the final
State v2 and backend-authored `QueueView` contracts.

This package alone does not satisfy technical-plan phase V4 or its visual
release gate. V4 remains incomplete until the live routes use these contracts,
the full lifecycle/phase/occupancy matrix is exercised against backend data,
and native 1000×640 screenshots are reviewed against the approved mockups.

Destination-conflict actions echo only a backend-issued opaque token. Runtime
payloads fail closed unless that token is a non-empty Unicode scalar string of
at most 256 UTF-8 bytes with no C0/C1 control characters. The UI never treats
the displayed proposed filename as authority.
