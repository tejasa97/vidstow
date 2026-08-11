<script lang="ts">
  import {
    lifecycleLabel,
    lifecycleTone,
    type DurableLifecycle,
    type PresentationPhase,
  } from './types.js';

  interface Props {
    lifecycle: DurableLifecycle;
    phase?: PresentationPhase;
    occupiesSlot: boolean;
    compact?: boolean;
  }

  let { lifecycle, phase, occupiesSlot, compact = false }: Props = $props();

  const label = $derived(lifecycleLabel(lifecycle, phase));
  const tone = $derived(lifecycleTone(lifecycle, phase));
  const showsOccupiedIndicator = $derived(occupiesSlot);
  const accessibleLabel = $derived(occupiesSlot ? `${label}, occupies an active slot` : label);
</script>

<span class="badge" class:compact data-tone={tone} aria-label={accessibleLabel}>
  {#if showsOccupiedIndicator}<span class="dot" aria-hidden="true"></span>{/if}
  <span>{label}</span>
</span>

<style>
  .badge {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    min-height: 24px;
    padding: 3px 9px;
    border: 1px solid transparent;
    border-radius: var(--r-full);
    font-size: var(--fs-xs);
    font-weight: 550;
    letter-spacing: 0.01em;
    white-space: nowrap;
  }

  .badge.compact {
    min-height: 22px;
    padding: 2px 8px;
  }

  .badge[data-tone='neutral'] {
    color: var(--text-secondary);
    background: var(--surface-hover);
    border-color: var(--border-default);
  }

  .badge[data-tone='info'] {
    color: var(--accent-600);
    background: var(--accent-soft);
    border-color: rgba(47, 111, 237, 0.24);
  }

  .badge[data-tone='warning'] {
    color: var(--status-warning);
    background: var(--status-warning-soft);
    border-color: rgba(176, 118, 7, 0.22);
  }

  .badge[data-tone='danger'] {
    color: var(--status-danger);
    background: var(--status-danger-soft);
    border-color: rgba(214, 69, 61, 0.22);
  }

  .badge[data-tone='success'] {
    color: var(--status-success);
    background: var(--status-success-soft);
    border-color: rgba(30, 142, 76, 0.22);
  }

  .dot {
    width: 7px;
    height: 7px;
    flex: 0 0 auto;
    border-radius: 50%;
    background: currentColor;
  }

  @media (prefers-reduced-motion: no-preference) {
    .dot { animation: pulse 1.6s infinite ease-out; }
  }

  @keyframes pulse {
    0% { box-shadow: 0 0 0 0 var(--accent-ring); }
    70% { box-shadow: 0 0 0 6px transparent; }
    100% { box-shadow: 0 0 0 0 transparent; }
  }
</style>
