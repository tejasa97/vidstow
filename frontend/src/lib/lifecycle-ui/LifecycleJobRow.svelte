<script lang="ts">
  import { createEventDispatcher } from 'svelte';
  import LifecycleBadge from './LifecycleBadge.svelte';
  import {
    lifecycleMessage,
    queuePositionLabel,
    type LifecycleJobAction,
    type LifecycleJobEventDetail,
    type LifecycleJobEventName,
    type LifecycleJobViewModel,
    isValidCommandToken,
  } from './types.js';

  export interface LifecycleJobRowEvents {
    pause: LifecycleJobEventDetail;
    cancel: LifecycleJobEventDetail;
    resume: LifecycleJobEventDetail;
    retry: LifecycleJobEventDetail;
    'download-again': LifecycleJobEventDetail;
    'start-again': LifecycleJobEventDetail;
    'open-source': LifecycleJobEventDetail;
    'copy-link': LifecycleJobEventDetail;
    review: LifecycleJobEventDetail;
    open: LifecycleJobEventDetail;
    remove: LifecycleJobEventDetail;
  }

  export interface LifecycleJobActionEvent extends LifecycleJobEventDetail {
    action: LifecycleJobEventName;
  }

  interface Props {
    job: LifecycleJobViewModel;
    index?: number;
    onAction?: (event: LifecycleJobActionEvent) => void;
  }

  let { job, index = 1, onAction }: Props = $props();
  const dispatch = createEventDispatcher<LifecycleJobRowEvents>();

  const progress = $derived(
    job.progress === undefined ? undefined : Math.max(0, Math.min(100, Math.round(job.progress * 100))),
  );
  const displayPhase = $derived(job.phase === 'cleaning-up' && enabled('remove') ? undefined : job.phase);
  const message = $derived(lifecycleMessage({ ...job, phase: displayPhase }));
  const queueLabel = $derived(job.queueLabel ?? queuePositionLabel(job.queuePosition));
  const hasProgress = $derived(
    progress !== undefined && job.phase !== 'cleaning-up' && !['failed', 'canceled', 'action-required'].includes(job.lifecycle),
  );
  const hasTransitionControls = $derived(
    job.phase !== 'cleaning-up' && ['pending', 'active', 'pausing', 'canceling', 'paused'].includes(job.lifecycle),
  );
  const isTerminal = $derived(
    ['failed', 'canceled', 'completed', 'action-required'].includes(job.lifecycle),
  );

  function enabled(action: LifecycleJobAction): boolean {
    if (!isValidCommandToken(job.commandToken)) return false;
    const capabilities = job.capabilities;
    if (!capabilities) return false;
    switch (action) {
      case 'download-again':
        return capabilities.downloadAgain === true;
      case 'start-again':
        return capabilities.startAgain === true;
      case 'open-source':
        return capabilities.openSource === true;
      case 'copy-link':
        return capabilities.copyLink === true;
      default:
        return capabilities[action] === true;
    }
  }

  function trigger(action: LifecycleJobEventName): void {
    if (!isValidCommandToken(job.commandToken)) return;
    const detail = { jobId: job.id, commandToken: job.commandToken };
    dispatch(action, detail);
    onAction?.({ ...detail, action });
  }
</script>

<article
  class="job-row"
  data-occupies-slot={job.occupiesSlot}
  aria-label={job.title || `Queue item ${index}`}
>
  {#if job.thumbnailUrl}
    <img class="thumbnail" src={job.thumbnailUrl} alt="" referrerpolicy="no-referrer" />
  {:else}
    <div class="thumbnail placeholder" aria-hidden="true">
      <span>{job.title.slice(0, 1).toUpperCase()}</span>
    </div>
  {/if}

  <div class="content">
    <div class="topline">
      <div class="title-block">
        <h3>{job.title}</h3>
        {#if job.metadata}<p>{job.metadata}</p>{/if}
      </div>

      <LifecycleBadge lifecycle={job.lifecycle} phase={displayPhase} occupiesSlot={job.occupiesSlot} compact />

      <div class="actions" aria-label="Job actions">
        {#if hasTransitionControls}
          {#if job.lifecycle === 'paused'}
            <button
              type="button"
              class="app-btn primary"
              aria-label="Resume download"
              disabled={!enabled('resume')}
              onclick={() => trigger('resume')}
            >Resume</button>
          {:else}
            <button
              type="button"
              class="app-btn"
              aria-label="Pause download"
              disabled={!enabled('pause')}
              onclick={() => trigger('pause')}
            >Pause</button>
          {/if}
          <button
            type="button"
            class="app-btn"
            aria-label="Cancel download"
            disabled={!enabled('cancel')}
            onclick={() => trigger('cancel')}
          >Cancel</button>
        {:else if isTerminal}
          {#if enabled('retry')}
            <button type="button" class="app-btn primary" onclick={() => trigger('retry')}>Retry</button>
          {:else if enabled('start-again')}
            <button type="button" class="app-btn primary" onclick={() => trigger('start-again')}>Start again</button>
          {:else if enabled('download-again')}
            <button type="button" class="app-btn primary" onclick={() => trigger('download-again')}>Download again</button>
          {:else if enabled('review')}
            <button type="button" class="app-btn primary" onclick={() => trigger('review')}>Review</button>
          {:else if enabled('open')}
            <button type="button" class="app-btn primary" onclick={() => trigger('open')}>Open</button>
          {/if}
          {#if enabled('open-source')}
            <button type="button" class="app-btn" onclick={() => trigger('open-source')}>Open source</button>
          {/if}
          {#if enabled('copy-link')}
            <button type="button" class="app-btn" onclick={() => trigger('copy-link')}>Copy link</button>
          {/if}
          {#if enabled('remove')}
            <button
              type="button"
              class="app-btn"
              aria-label="Remove download"
              onclick={() => trigger('remove')}
            >Remove</button>
          {/if}
        {:else if enabled('review')}
          <button type="button" class="app-btn primary" onclick={() => trigger('review')}>Review</button>
        {/if}
      </div>
    </div>

    {#if hasProgress}
      <div class="progress-line">
        <div
          class="track"
          role="progressbar"
          aria-label={`${job.title} progress`}
          aria-valuemin="0"
          aria-valuemax="100"
          aria-valuenow={progress}
        >
          <span style={`width: ${progress}%`}></span>
        </div>
        <span class="progress-label">{job.progressLabel ?? `${progress}%`}</span>
        <span class="progress-detail">
          {#if job.speedLabel}{job.speedLabel}{/if}
          {#if job.etaLabel}{job.speedLabel ? ' · ' : ''}ETA {job.etaLabel}{/if}
          {#if !job.speedLabel && !job.etaLabel && message}{message}{/if}
        </span>
      </div>
    {:else if job.failure}
      <div class="failure" role="alert" data-category={job.failure.category}>
        <strong>{job.failure.heading}</strong>
        <span>{job.failure.message}</span>
        <span class="recommended">{job.failure.recommendedAction}</span>
        {#if job.failure.partialOutput}<small>Partial download data may remain until this item is removed or retried.</small>{/if}
      </div>
    {:else if message || queueLabel}
      <p class:terminal-message={isTerminal} class="message">{message ?? queueLabel}</p>
    {/if}
  </div>
</article>

<style>
  .job-row {
    display: grid;
    grid-template-columns: 128px minmax(0, 1fr);
    gap: var(--sp-4);
    min-height: 116px;
    padding: var(--sp-4);
    border-bottom: 1px solid var(--border-subtle);
    background: var(--surface-base);
  }

  .job-row:first-child { border-radius: var(--r-md) var(--r-md) 0 0; }
  .job-row:last-child { border-bottom: 0; border-radius: 0 0 var(--r-md) var(--r-md); }

  .thumbnail {
    width: 128px;
    aspect-ratio: 16 / 9;
    align-self: center;
    object-fit: cover;
    border-radius: var(--r-sm);
    background: var(--surface-sunken);
  }

  .thumbnail.placeholder {
    display: grid;
    place-items: center;
    color: var(--text-muted);
    font-size: var(--fs-xl);
    font-weight: 650;
  }

  .content {
    display: flex;
    min-width: 0;
    flex-direction: column;
    justify-content: center;
    gap: var(--sp-4);
  }

  .topline {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto auto;
    align-items: start;
    gap: var(--sp-3);
  }

  .title-block { min-width: 0; }
  h3 { margin: 0; overflow: hidden; font-size: var(--fs-md); font-weight: 650; text-overflow: ellipsis; white-space: nowrap; }
  .title-block p { margin: 3px 0 0; color: var(--text-muted); font-size: var(--fs-sm); }

  .actions {
    display: flex;
    align-items: center;
    justify-content: flex-end;
    gap: var(--sp-2);
    min-width: 90px;
  }

  .progress-line {
    display: grid;
    grid-template-columns: minmax(120px, 1fr) auto minmax(180px, auto);
    align-items: center;
    gap: var(--sp-3);
    min-width: 0;
  }

  .track {
    height: 6px;
    overflow: hidden;
    border-radius: var(--r-full);
    background: var(--surface-active);
  }

  .track span {
    display: block;
    height: 100%;
    border-radius: inherit;
    background: var(--accent-500);
    transition: width 180ms ease;
  }

  .progress-label,
  .progress-detail,
  .message {
    color: var(--text-secondary);
    font-size: var(--fs-sm);
  }

  .progress-label { min-width: 36px; font-variant-numeric: tabular-nums; }
  .progress-detail { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .message { margin: 0; min-height: 20px; }
  .terminal-message { color: var(--text-secondary); }

  .failure {
    display: grid;
    gap: var(--sp-1);
    padding-left: var(--sp-3);
    border-left: 3px solid var(--status-danger);
    color: var(--text-secondary);
    font-size: var(--fs-sm);
  }
  .failure strong { color: var(--text-primary); font-size: var(--fs-md); }
  .failure .recommended { color: var(--text-primary); }
  .failure small { color: var(--text-muted); font-size: var(--fs-xs); }

  @media (max-width: 780px) {
    .job-row { grid-template-columns: 96px minmax(0, 1fr); }
    .thumbnail { width: 96px; height: 60px; }
    .topline { grid-template-columns: minmax(0, 1fr) auto; }
    .topline :global(.badge) { grid-column: 2; grid-row: 1; }
    .actions { grid-column: 1 / -1; grid-row: 2; justify-content: flex-start; }
    .progress-line { grid-template-columns: minmax(80px, 1fr) auto; }
    .progress-detail { grid-column: 1 / -1; }
  }

  @media (max-width: 480px) {
    .job-row { grid-template-columns: 72px minmax(0, 1fr); gap: var(--sp-3); padding: var(--sp-3); }
    .thumbnail { width: 72px; height: 48px; }
    .topline { gap: var(--sp-2); }
    h3 { font-size: var(--fs-sm); }
  }
</style>
