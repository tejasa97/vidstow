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
  }

  let { model, heading = 'Queue & recovery' }: Props = $props();
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
    }
  }
</script>

<section class="settings-card" aria-labelledby="lifecycle-queue-settings-title">
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
      <p>Choose from {minimum} to {maximum}. Reducing the limit waits for active jobs; it does not pause them.</p>
      <p class="info"><span class="info-icon" aria-hidden="true">i</span>Default: {defaultValue} · FIFO order</p>
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
  .settings-card {
    padding: var(--sp-5) var(--sp-6);
    border: 1px solid var(--border-default);
    border-radius: var(--r-md);
    background: var(--surface-base);
    box-shadow: var(--shadow-card);
  }

  h2 { margin: 0 0 var(--sp-3); font-size: var(--fs-lg); font-weight: 650; }
  .setting { display: flex; align-items: center; justify-content: space-between; gap: var(--sp-6); padding: var(--sp-4) 0; border-top: 1px solid var(--border-subtle); }
  .setting > div { min-width: 0; }
  strong, label { display: block; color: var(--text-primary); font-size: var(--fs-md); font-weight: 650; }
  p { max-width: 68ch; margin: var(--sp-1) 0 0; color: var(--text-muted); font-size: var(--fs-sm); }

  .fixed-value {
    flex: 0 0 auto;
    padding: 6px 12px;
    border-radius: var(--r-full);
    background: var(--surface-hover);
    color: var(--text-secondary);
    font-size: var(--fs-sm);
    font-weight: 550;
    white-space: nowrap;
  }

  select {
    width: 84px;
    flex: 0 0 84px;
    background: var(--surface-base);
  }

  .info { display: flex; align-items: center; gap: var(--sp-2); margin-top: var(--sp-2); color: var(--accent-500); }
  .info-icon { display: inline-grid; place-items: center; width: 18px; height: 18px; border-radius: 50%; background: var(--accent-500); color: var(--text-on-accent); font-size: var(--fs-xs); font-weight: 700; }

  @media (max-width: 620px) {
    .settings-card { padding: var(--sp-4); }
    .setting { align-items: flex-start; flex-direction: column; gap: var(--sp-3); }
    .fixed-value, select { align-self: flex-start; }
  }
</style>
