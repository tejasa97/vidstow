import { describe, expect, test } from 'vitest';
import { newestQueueView } from '../src/lib/queue-view.js';
import type { QueueView } from '../src/lib/lifecycle-ui/types.js';

function view(revision: number): QueueView {
  return {
    revision,
    rows: [],
    summary: { totalJobs: 0, runningJobs: 0, occupiedSlots: 0, slotLimit: 2, waitingJobs: 0, pausedJobs: 0 },
    capabilities: {},
    persistence: { available: true, healthy: true },
  };
}

describe('live QueueView ordering', () => {
  test('does not regress progress/authority for delayed or duplicate bridge events', () => {
    const current = view(4);
    expect(newestQueueView(current, view(3))).toBe(current);
    expect(newestQueueView(current, view(4))?.revision).toBe(4);
    expect(newestQueueView(current, view(5))?.revision).toBe(5);
  });

  test('keeps an event-newer view when an older imperative refresh resolves afterwards', () => {
    const eventView = view(12);
    const staleRefresh = view(11);
    expect(newestQueueView(eventView, staleRefresh)).toBe(eventView);
  });
});
