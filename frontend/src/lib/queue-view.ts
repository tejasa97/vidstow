import type { QueueView } from './lifecycle-ui/types.js';

// Queue events are ordered by the backend, but bridge delivery can still
// duplicate or delay a payload during startup. Never let an older snapshot
// overwrite newer progress or authority in the live UI.
export function newestQueueView(current: QueueView | null, next: QueueView): QueueView | null {
  if (current && Number.isFinite(current.revision) && Number.isFinite(next?.revision) && next.revision < current.revision) {
    return current;
  }
  return next;
}
