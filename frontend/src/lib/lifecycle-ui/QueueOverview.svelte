<script lang="ts">
  import { createEventDispatcher } from 'svelte';
  import LifecycleJobRow, { type LifecycleJobActionEvent } from './LifecycleJobRow.svelte';
  import CollectionRow from './CollectionRow.svelte';
  import QueueSummary from './QueueSummary.svelte';
  import { isValidCommandToken } from './types.js';
  import type { LifecycleJobEventDetail, LifecycleJobViewModel, QueueCollectionActionEvent, QueueOverviewViewModel } from './types.js';

  export interface QueueOverviewEvents {
    'pause-all': void;
    'clear-completed': void;
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
    'collection-action': QueueCollectionActionEvent;
  }

  interface Props {
    model: QueueOverviewViewModel;
    title?: string;
    onPauseAll?: () => void;
    onClearCompleted?: () => void;
    onAction?: (event: LifecycleJobActionEvent) => void;
    onCollectionAction?: (event: QueueCollectionActionEvent) => void;
  }

  let { model, title = 'Queue', onPauseAll, onClearCompleted, onAction, onCollectionAction }: Props = $props();
  const dispatch = createEventDispatcher<QueueOverviewEvents>();
  const hasQueueAuthority = $derived(isValidCommandToken(model.commandToken));
  const collections = $derived(model.collections ?? []);
  const collectionIds = $derived(new Set(collections.map((collection) => collection.id)));
  const jobsById = $derived(new Map(model.jobs.map((job) => [job.id, job])));
  const standaloneJobs = $derived(model.jobs.filter((job) => !job.collectionId || !collectionIds.has(job.collectionId)));

  function forward(event: LifecycleJobActionEvent): void {
    dispatch(event.action, { jobId: event.jobId, commandToken: event.commandToken });
    onAction?.(event);
  }

  function forwardCollection(event: QueueCollectionActionEvent): void {
    dispatch('collection-action', event);
    onCollectionAction?.(event);
  }

  function collectionChildren(childJobIds: string[]) {
    return childJobIds.map((id) => jobsById.get(id)).filter((job): job is LifecycleJobViewModel => job !== undefined);
  }

  function pauseAll(): void {
    if (!(model.canPauseAll === true && hasQueueAuthority)) return;
    dispatch('pause-all');
    onPauseAll?.();
  }

  function clearCompleted(): void {
    if (!(model.canClearCompleted === true && hasQueueAuthority)) return;
    dispatch('clear-completed');
    onClearCompleted?.();
  }
</script>

<section class="page queue-page" aria-labelledby="lifecycle-queue-title">
  <header class="page-header queue-header">
    <div>
      <h1 id="lifecycle-queue-title">{title}</h1>
      <p>{model.summary.totalJobs} {model.summary.totalJobs === 1 ? 'job' : 'jobs'} · {model.summary.runningJobs} running</p>
    </div>
    <div class="header-actions">
      <button
        type="button"
        class="app-btn"
        disabled={!(model.canPauseAll === true && hasQueueAuthority)}
        onclick={pauseAll}
      >Pause All</button>
      <button
        type="button"
        class="app-btn"
        disabled={!(model.canClearCompleted === true && hasQueueAuthority)}
        onclick={clearCompleted}
      >Clear Completed</button>
    </div>
  </header>

  <QueueSummary summary={model.summary} />

  {#if model.notice}
    <p class="notice" data-tone={model.noticeTone ?? 'info'} role="status" aria-live="polite">
      <span class="notice-icon" aria-hidden="true">i</span>
      <span>{model.notice}</span>
    </p>
  {/if}

  <section class="job-section" aria-labelledby="lifecycle-job-section-title">
    <h2 id="lifecycle-job-section-title">
      {model.sectionTitle ?? 'Active & queued'}
      <span class="count" aria-label={`${model.jobs.length} jobs`}>{model.jobs.length}</span>
    </h2>

    {#if model.jobs.length || collections.length}
      <div class="job-list">
        {#each collections as collection (collection.id)}
          <CollectionRow
            {collection}
            children={collectionChildren(collection.childJobIds)}
            onAction={forward}
            onCollectionAction={forwardCollection}
          />
        {/each}
        {#each standaloneJobs as job, index (job.id)}
          <LifecycleJobRow
            {job}
            index={index + 1}
            onAction={forward}
          />
        {/each}
      </div>
    {:else}
      <div class="empty" role="status">Nothing in the queue. Add a video, Short, or playlist from Home to get started.</div>
    {/if}
  </section>

  <footer>{model.footerText ?? 'Jobs are saved automatically.'}</footer>
</section>

<style>
  .queue-header {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: var(--sp-6);
  }
  .header-actions { display: flex; gap: var(--sp-2); padding-top: 2px; }



  .notice {
    display: flex;
    align-items: center;
    gap: var(--sp-3);
    margin: 0;
    padding: var(--sp-3) var(--sp-4);
    border: 1px solid var(--accent-400);
    border-radius: var(--r-md);
    color: var(--accent-600);
    background: var(--accent-soft);
    font-size: var(--fs-sm);
  }

  .notice[data-tone='warning'] {
    border-color: rgba(176, 118, 7, 0.4);
    color: var(--status-warning);
    background: var(--status-warning-soft);
  }

  .notice-icon {
    display: grid;
    place-items: center;
    width: 18px;
    height: 18px;
    flex: 0 0 auto;
    border: 1.5px solid currentColor;
    border-radius: 50%;
    font-size: var(--fs-xs);
    font-weight: 700;
  }

  .job-section { margin-top: 0; }
  h2 { display: flex; align-items: center; gap: var(--sp-2); margin: 0 0 var(--sp-3); font-size: var(--fs-lg); }
  .count { display: inline-grid; place-items: center; min-width: 22px; height: 22px; padding: 0 6px; border-radius: var(--r-full); background: var(--surface-active); color: var(--text-secondary); font-size: var(--fs-xs); font-weight: 600; }

  .job-list { overflow: hidden; border: 1px solid var(--border-default); border-radius: var(--r-md); box-shadow: var(--shadow-card); }
  .empty { display: grid; min-height: 150px; place-items: center; border: 1px dashed var(--border-default); border-radius: var(--r-md); color: var(--text-muted); font-size: var(--fs-sm); }
  footer { margin-top: var(--sp-5); color: var(--text-muted); font-size: var(--fs-xs); text-align: center; }

  @media (max-width: 700px) {
    .queue-header { flex-direction: column; }
    .header-actions { width: 100%; }
    .header-actions .app-btn { flex: 1; }
  }
</style>
