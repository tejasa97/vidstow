<script lang="ts">
  import LifecycleJobRow, { type LifecycleJobActionEvent } from './LifecycleJobRow.svelte';
  import { isValidCommandToken } from './types.js';
  import type {
    LifecycleJobViewModel,
    QueueCollectionAction,
    QueueCollectionActionEvent,
    QueueCollectionViewModel,
  } from './types.js';

  interface Props {
    collection: QueueCollectionViewModel;
    children: LifecycleJobViewModel[];
    onAction?: (event: LifecycleJobActionEvent) => void;
    onCollectionAction?: (event: QueueCollectionActionEvent) => void;
  }

  let { collection, children, onAction, onCollectionAction }: Props = $props();
  let expanded = $state(true);
  const panelId = $derived(`playlist-children-${collection.id.replace(/[^A-Za-z0-9_-]/g, '-')}`);
  const progress = $derived(Math.max(0, Math.min(100, Math.round(collection.progress * 100))));

  function enabled(action: QueueCollectionAction): boolean {
    return isValidCommandToken(collection.commandToken) && collection.capabilities?.[action] === true;
  }

  function trigger(action: QueueCollectionAction): void {
    if (!enabled(action) || !collection.commandToken) return;
    onCollectionAction?.({ collectionId: collection.id, commandToken: collection.commandToken, action });
  }
</script>

<section class="collection" aria-labelledby={`${panelId}-title`}>
  <div class="parent-row">
    <button
      type="button"
      class="toggle"
      aria-expanded={expanded}
      aria-controls={panelId}
      aria-label={`${expanded ? 'Collapse' : 'Expand'} ${collection.title}`}
      onclick={() => expanded = !expanded}
    >
      <span aria-hidden="true">{expanded ? '▾' : '▸'}</span>
    </button>

    {#if collection.thumbnailUrl}
      <img class="thumbnail" src={collection.thumbnailUrl} alt="" referrerpolicy="no-referrer" />
    {:else}
      <div class="thumbnail placeholder" aria-hidden="true">{collection.title.slice(0, 1).toUpperCase()}</div>
    {/if}

    <div class="identity">
      <h3 id={`${panelId}-title`}>{collection.title}</h3>
      <p>{collection.metadata ? `${collection.metadata} · ` : ''}{collection.policy} · {collection.total} videos</p>
      <div class="progress-line">
        <div class="track" role="progressbar" aria-label={`${collection.title} progress`} aria-valuemin="0" aria-valuemax="100" aria-valuenow={progress}>
          <span style={`width: ${progress}%`}></span>
        </div>
        <span>{collection.progressLabel}</span>
        {#if collection.failed}<strong class="failed">{collection.failed} failed</strong>{/if}
        {#if collection.canceled}<strong>{collection.canceled} canceled</strong>{/if}
      </div>
    </div>

    <div class="actions" aria-label="Playlist actions">
      {#if collection.capabilities?.resume}
        <button type="button" class="app-btn primary" disabled={!enabled('resume')} onclick={() => trigger('resume')}>Resume</button>
      {:else if collection.capabilities?.pause}
        <button type="button" class="app-btn" disabled={!enabled('pause')} onclick={() => trigger('pause')}>Pause</button>
      {/if}
      {#if collection.capabilities?.retry}
        <button type="button" class="app-btn primary" disabled={!enabled('retry')} onclick={() => trigger('retry')}>Retry failed</button>
      {/if}
      {#if collection.capabilities?.cancel}
        <button type="button" class="app-btn" disabled={!enabled('cancel')} onclick={() => trigger('cancel')}>Cancel</button>
      {/if}
      {#if collection.capabilities?.remove}
        <button type="button" class="app-btn" disabled={!enabled('remove')} onclick={() => trigger('remove')}>Remove</button>
      {/if}
    </div>
  </div>

  {#if expanded}
    <div class="children" id={panelId}>
      {#each children as child, index (child.id)}
        <LifecycleJobRow job={child} index={index + 1} {onAction} />
      {/each}
    </div>
  {/if}
</section>

<style>
  .collection { border-bottom: 1px solid var(--border-default); background: var(--surface-base); }
  .collection:last-child { border-bottom: 0; }
  .parent-row { display: grid; grid-template-columns: 36px 128px minmax(0, 1fr) auto; align-items: center; gap: var(--sp-3); padding: var(--sp-4); background: var(--surface-sunken); }
  .toggle { display: grid; width: 36px; height: 36px; place-items: center; border: 1px solid var(--border-default); border-radius: var(--r-md); background: var(--surface-base); color: var(--text-primary); font-size: var(--fs-lg); }
  .toggle:hover { background: var(--surface-hover); }
  .thumbnail { width: 128px; aspect-ratio: 16 / 9; object-fit: cover; border-radius: var(--r-sm); background: var(--surface-base); }
  .placeholder { display: grid; place-items: center; color: var(--text-muted); font-weight: 650; }
  .identity { min-width: 0; }
  h3 { margin: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: var(--fs-md); }
  p { margin: 3px 0 var(--sp-2); color: var(--text-muted); font-size: var(--fs-sm); }
  .progress-line { display: flex; align-items: center; gap: var(--sp-2); color: var(--text-muted); font-size: var(--fs-xs); }
  .track { width: min(220px, 35vw); height: 6px; overflow: hidden; border-radius: var(--r-full); background: var(--surface-active); }
  .track span { display: block; height: 100%; border-radius: inherit; background: var(--accent-500); }
  .failed { color: var(--status-danger); }
  .actions { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: var(--sp-2); }
  .children { margin-left: 48px; border-left: 3px solid var(--accent-soft); }
  .children :global(.job-row) { border-radius: 0; }

  @media (max-width: 760px) {
    .parent-row { grid-template-columns: 36px 72px minmax(0, 1fr); }
    .thumbnail { width: 72px; height: 44px; }
    .actions { grid-column: 2 / -1; max-width: none; justify-content: flex-start; }
    .children { margin-left: 20px; }
  }
</style>
