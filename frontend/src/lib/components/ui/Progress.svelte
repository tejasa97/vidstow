<script lang="ts">
  interface Props {
    /** 0..1 fractional progress */
    value?: number;
    /** Show a textual percentage */
    showLabel?: boolean;
    label?: string;
    tone?: 'accent' | 'success';
    indeterminate?: boolean;
  }

  let {
    value = 0,
    showLabel = false,
    label,
    tone = 'accent',
    indeterminate = false,
  }: Props = $props();

  const pct = $derived(Math.max(0, Math.min(100, Math.round(value * 100))));
  const displayLabel = $derived(label ?? `${pct}%`);
</script>

<div class="progress" role="progressbar" aria-valuemin="0" aria-valuemax="100" aria-valuenow={indeterminate ? undefined : pct}>
  <div class="track">
    <div
      class="fill {tone}"
      class:indeterminate
      style:width={indeterminate ? undefined : `${pct}%`}
    ></div>
  </div>
  {#if showLabel}<span class="label">{displayLabel}</span>{/if}
</div>

<style>
  .progress { display: flex; align-items: center; gap: var(--sp-2); }
  .track {
    flex: 1;
    height: 6px;
    border-radius: var(--r-full);
    background: var(--border-subtle);
    overflow: hidden;
  }
  .fill {
    height: 100%;
    border-radius: var(--r-full);
    background: var(--accent-500);
    transition: width 180ms ease;
  }
  .fill.success { background: var(--status-success); }
  .fill.indeterminate {
    width: 40% !important;
    animation: slide 1.2s infinite ease-in-out;
  }
  .label {
    font-size: var(--fs-xs);
    color: var(--text-secondary);
    min-width: 36px;
    text-align: right;
    font-variant-numeric: tabular-nums;
  }
  @keyframes slide {
    0% { margin-left: -40%; }
    100% { margin-left: 100%; }
  }
</style>
