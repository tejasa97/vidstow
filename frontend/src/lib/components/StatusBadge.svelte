<script lang="ts">
  import type { JobStatus } from '../types.js';

  export let status: JobStatus;
  export let compact = false;

  const labels: Record<string, string> = {
    pending: 'Queued',
    active: 'Downloading',
    pausing: 'Pausing',
    paused: 'Paused',
    canceling: 'Canceling',
    complete: 'Done',
    failed: 'Failed',
    canceled: 'Canceled',
    'action-required': 'Action required',
  };
</script>

<span class="badge {status}" class:compact>
  {#if status === 'active'}<span class="dot" aria-hidden="true"></span>{/if}
  <span>{labels[status] ?? status}</span>
</span>

<style>
  .badge {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 4px 10px;
    font-size: var(--fs-xs);
    font-weight: 500;
    border-radius: var(--r-full);
    border: 1px solid transparent;
    letter-spacing: 0.01em;
  }
  .badge.compact {
    padding: 2px 8px;
    font-size: 11px;
  }
  .pending {
    background: var(--surface-hover);
    color: var(--text-secondary);
    border-color: var(--border-default);
  }
  .active {
    background: var(--accent-soft);
    color: var(--accent-400);
    border-color: rgba(59, 130, 246, 0.32);
  }
  .paused, .pausing { background: var(--status-warning-soft); color: var(--status-warning); border-color: rgba(154, 100, 8, 0.28); }
  .canceling, .action-required { background: var(--status-danger-soft); color: var(--status-danger); border-color: rgba(196, 59, 52, 0.28); }
  .complete {
    background: var(--status-success-soft);
    color: var(--status-success);
    border-color: rgba(52, 211, 153, 0.32);
  }
  .failed {
    background: var(--status-danger-soft);
    color: var(--status-danger);
    border-color: rgba(248, 113, 113, 0.32);
  }
  .canceled {
    background: var(--surface-hover);
    color: var(--text-muted);
    border-color: var(--border-default);
  }
  .dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--accent-400);
    box-shadow: 0 0 0 0 var(--accent-ring);
    animation: pulse 1.6s infinite ease-out;
  }
  @keyframes pulse {
    0%   { box-shadow: 0 0 0 0 rgba(59,130,246,0.55); }
    70%  { box-shadow: 0 0 0 8px rgba(59,130,246,0); }
    100% { box-shadow: 0 0 0 0 rgba(59,130,246,0); }
  }
</style>
