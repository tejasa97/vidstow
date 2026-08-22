# Durable collections in State v2

Playlists and pasted URL batches are durable collections of ordinary download jobs, not
separate schedulers. Each child remains an independent State v2 job and therefore retains
the existing lifecycle, attempt, session, reservation, recovery, and history rules.

## Durable ownership

`DurableCollection` owns aggregate identity and presentation metadata:

- a stable collection ID and explicit `playlist` or `batch` kind;
- playlist identity and canonical source URL only for playlist collections;
- title, optional channel/thumbnail, and a server-authored output-policy label;
- ordered child job IDs;
- revision and timestamps.

Batch collections never persist the pasted text or a collection-level source URL. Their
ordinary child jobs retain only the canonical source data already required by the job
model. State v2 load normalization treats collections written before the kind field was
introduced as playlists.

Each child stores its collection ID and original one-based selection/input index. Indexes
are not renumbered when terminal children are removed. The collection is removed after
its final child is removed. Queue totals and batch titles are derived from the same current
ordered child set so their progress text cannot disagree.

## Atomic admission

`admission.Coordinator.AdmitCollection` accepts only backend-prepared child requests.
Before mutating State it:

1. validates the collection kind, identity, and child count;
2. validates every backend-resolved child plan;
3. renders each engine artifact declaration;
4. validates each child against its exact open output root; and
5. allocates all job, attempt, session, and collection identities.

Playlist children normally share one root. Batch children may use distinct roots when the
per-video-subfolder setting is enabled. Each root is opened and validated before the
transaction, without accepting renderer-authored paths or engine metadata.

One State v2 transaction then selects every reservation against both existing durable
claims and reservations selected earlier in the same collection. It commits the parent,
all pending children, queue ordinals, private plans, and reservations together.

Only after that transaction commits are children handed to the live FIFO manager. A
manager failure cannot produce an ephemeral-only job: the complete pending collection
remains durable for startup reconciliation. Conversely, any validation, planning,
rendering, reservation, or transaction failure reaches no live queue.

## Trust boundary

The coordinator does not accept generic format selectors or analysis metadata from the
renderer. The application layer must resolve selected playlist entries or an opaque batch
analysis token, choose a curated plan for each canonical child according to the requested
product policy, and enforce FFmpeg requirements before calling collection admission.

Batch analysis tokens are in-memory, bounded, single-claim authorities with a 30-minute
expiry. Successful durable admission consumes the token. Expiry, replay, or an app restart
requires analysis again.
