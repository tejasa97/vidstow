<script lang="ts">
  import { createEventDispatcher } from 'svelte';
  import {
    concurrencyValues,
    DEFAULT_CONCURRENCY,
    MAX_CONCURRENCY,
    MIN_CONCURRENCY,
    type QueueSettingsViewModel,
  } from './types.js';

  export interface QueueSettingsEvents {
    'concurrency-change': { value: number };
  }

  interface Props {
    model: QueueSettingsViewModel;
    heading?: string;
    onConcurrencyChange?: (value: number) => void;
  }

  let { model, heading = 'Queue & recovery', onConcurrencyChange }: Props = $props();
  const dispatch = createEventDispatcher<QueueSettingsEvents>();

  const minimum = $derived(model.minimum ?? MIN_CONCURRENCY);
  const maximum = $derived(model.maximum ?? MAX_CONCURRENCY);
  const defaultValue = $derived(model.defaultValue ?? DEFAULT_CONCURRENCY);
  const values = $derived(concurrencyValues(minimum, maximum));

  function changeConcurrency(event: Event): void {
    const select = event.currentTarget as HTMLSelectElement;
    const value = Number(select.value);
    if (Number.isInteger(value) && value >= minimum && value <= maximum) {
      dispatch('concurrency-change', { value });
      onConcurrencyChange?.(value);
    }
  }
</script>

<section class="queue-settings" aria-labelledby="lifecycle-queue-settings-title">
  <h2 id="lifecycle-queue-settings-title">{heading}</h2>

  <div class="setting interrupted-setting">
    <div>
      <strong>Interrupted jobs</strong>
      <p>Saved jobs are restored as paused. Nothing starts automatically when VidStow opens.</p>
    </div>
    <span class="fixed-value">Restored as paused</span>
  </div>

  <div class="setting concurrency-setting">
    <div>
      <label for="lifecycle-concurrency">Concurrent downloads</label>
      <p>Choose from {minimum} to {maximum}. Reducing the limit waits for active jobs; it does not pause them. Default: {defaultValue} · FIFO order</p>
    </div>
    <select
      id="lifecycle-concurrency"
      aria-label="Concurrent downloads"
      value={model.concurrency}
      disabled={model.disabled}
      onchange={changeConcurrency}
    >
      {#each values as value (value)}
        <option value={value}>{value}</option>
      {/each}
    </select>
  </div>
</section>

<style>
  .queue-settings { display: contents; }
  h2 {
    margin: 0;
    padding: 10px 0 0;
    border-top: 1px solid var(--border-subtle);
    font-size: var(--fs-xs);
    font-weight: 650;
    letter-spacing: 0.04em;
    text-transform: uppercase;
    color: var(--text-muted);
  }
  .setting {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--sp-4);
    min-height: 48px;
    padding: 8px 0;
    border-top: 1px solid var(--border-subtle);
  }
  .setting > div { min-width: 0; }
  strong, label {
    display: block;
    color: var(--text-primary);
    font-size: var(--fs-sm);
    font-weight: 600;
  }
  p {
    margin: 4px 0 0;
    color: var(--text-secondary);
    font-size: var(--fs-xs);
    line-height: 1.45;
  }

  .fixed-value {
    flex: 0 0 auto;
    color: var(--text-muted);
    font-size: var(--fs-sm);
    font-weight: 600;
    white-space: nowrap;
  }

  select {
    width: 84px;
    flex: 0 0 84px;
    height: 36px;
    padding: 0 28px 0 12px;
    border: 1px solid var(--border-default);
    border-radius: var(--r-md);
    background-color: var(--surface-base);
    background-image: url("data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' width='12' height='12' viewBox='0 0 12 12' fill='none'><path d='M2.5 4.5L6 8l3.5-3.5' stroke='%235C544C' stroke-width='1.6' stroke-linecap='round' stroke-linejoin='round'/></svg>");
    background-repeat: no-repeat;
    background-position: right 10px center;
    color: var(--text-primary);
    font-size: var(--fs-sm);
    font-weight: 600;
    box-shadow: none;
  }
  select:hover:not(:disabled) {
    background-color: var(--surface-hover);
    border-color: var(--border-strong);
  }

  @media (max-width: 620px) {
    .setting { align-items: flex-start; flex-direction: column; gap: var(--sp-2); }
    .fixed-value, select { align-self: flex-start; }
  }
</style>
