# Playlist collections in State v2

A playlist or public channel tab is a durable collection of ordinary download jobs, not a second scheduler.
Each selected video remains an independent State v2 job and therefore retains the
existing lifecycle, attempt, session, reservation, recovery, and history rules.

Public channel Videos and Shorts URLs use this same collection path. Live
streams, channel search, and other channel tabs remain outside the product
boundary.

## Durable ownership

`DurableCollection` owns only aggregate identity and presentation metadata:

- stable collection and playlist identities
- canonical playlist source URL
- title, channel, thumbnail, and server-authored output-policy label
- ordered child job IDs
- revision and timestamps

Each child stores its collection ID and original one-based playlist selection index.
Indexes are not renumbered when terminal children are removed. The collection is
removed after its final child is removed.

## Atomic admission

`admission.Coordinator.AdmitCollection` accepts only backend-prepared child requests.
Before mutating State it:

1. validates the collection and child count (1–500),
2. resolves every child plan through the private server-side plan resolver,
3. renders each engine artifact declaration,
4. validates that every child uses the exact open output root, and
5. allocates all job, attempt, session, and collection identities.

One State v2 transaction then selects every reservation against both existing durable
claims and reservations selected earlier in the same collection. It commits the parent,
all pending children, queue ordinals, private plans, and reservations together.

Only after that transaction commits are children handed to the live FIFO manager. A
manager failure cannot produce an ephemeral-only job: the complete pending collection
remains durable for reconciliation. Conversely, any validation, planning, rendering,
reservation, or transaction failure reaches no live queue.

## Trust boundary

The coordinator does not accept a generic playlist quality selector from the renderer.
The application layer must resolve selected indices against its bounded trusted preview,
analyze each canonical child URL, choose a curated plan according to the requested
product policy, and enforce FFmpeg requirements before calling collection admission.
That orchestration and collection-aware QueueView presentation are intentionally separate
follow-up layers.
