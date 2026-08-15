<script lang="ts">
  import { history, modal, showBanner, showError } from '../lib/stores.js';
  import { api } from '../lib/api.js';
  import { formatBytes, formatRelative, qualityLabel } from '../lib/format.js';
  import { Tabs, EmptyState } from '../lib/components/ui/index.js';
  import type { HistoryEntry } from '../lib/types.js';

  const RECENT_LIMIT = 10;

  let query = '';
  let view: 'recent' | 'all' = 'recent';
  let selected: HistoryEntry | null = null;

  $: recentCount = Math.min(RECENT_LIMIT, $history.length);
  $: showRange = $history.length > RECENT_LIMIT;
  $: source = view === 'recent' && showRange ? $history.slice(0, RECENT_LIMIT) : $history;
  $: needle = query.trim().toLowerCase();
  $: filtered = source.filter((entry) =>
    [entry.title, entry.channel, entry.filename, entry.quality, entry.container || '']
      .some((value) => value.toLowerCase().includes(needle)),
  );
  $: if (selected && !filtered.some((entry) => entry.id === selected?.id)) selected = null;

  $: rangeTabs = [
    { value: 'recent', label: 'Recent', count: recentCount },
    { value: 'all', label: 'All', count: $history.length },
  ];

  function formatLabel(entry: HistoryEntry): string {
    const quality = qualityLabel(entry.quality);
    return entry.container ? `${quality} · ${entry.container}` : quality;
  }

  function codecSummary(entry: HistoryEntry): string {
    return [entry.videoCodec, entry.audioCodec].filter(Boolean).join(' · ');
  }

  function thumbnailFor(entry: HistoryEntry): string {
    if (entry.thumbnail) return entry.thumbnail;
    return entry.videoId
      ? `https://i.ytimg.com/vi/${encodeURIComponent(entry.videoId)}/hqdefault.jpg`
      : '';
  }

  function toggle(entry: HistoryEntry) {
    selected = selected?.id === entry.id ? null : entry;
  }

  const open = async (entry: HistoryEntry) => {
    if (entry.fileMissing) {
      showBanner('warning', 'That downloaded file is no longer on disk.');
      return;
    }
    try {
      await api.fs.open(entry.absolutePath);
    } catch (err) {
      showError(err, 'Could not open the downloaded file');
    }
  };

  const reveal = async (entry: HistoryEntry) => {
    if (entry.fileMissing) {
      showBanner('warning', 'That downloaded file is no longer on disk.');
      return;
    }
    try {
      await api.fs.reveal(entry.absolutePath);
    } catch (err) {
      showError(err, 'Could not show the downloaded file');
    }
  };

  function confirmRemoveHistory(entry: HistoryEntry) {
    modal.set({
      kind: 'confirm',
      title: 'Remove from history?',
      message: `Remove “${entry.title}” from Downloads. The media file on disk stays untouched.`,
      actions: [
        {
          label: 'Remove from history',
          primary: true,
          action: async () => {
            try {
              await api.downloads.remove(entry.id);
              showBanner('info', 'Removed from history');
            } catch (err) {
              showError(err, 'Could not remove the history entry');
            }
          },
        },
      ],
    });
  }

  function confirmDeleteFile(entry: HistoryEntry) {
    modal.set({
      kind: 'confirm',
      title: 'Delete downloaded file?',
      message: `Permanently delete “${entry.filename || entry.title}” from disk and remove it from history.`,
      actions: [
        {
          label: 'Delete file',
          primary: true,
          action: async () => {
            try {
              await api.downloads.deleteFile(entry.id);
              showBanner('info', 'Deleted downloaded file');
            } catch (err) {
              showError(err, 'Could not delete the downloaded file');
            }
          },
        },
      ],
    });
  }
</script>

<section class="page" aria-labelledby="downloads-title">
  <header class="page-header">
    <h1 id="downloads-title">Downloads</h1>
    <p>View your recently downloaded items.</p>
  </header>
  <div class="toolbar">
    <label class="search">
      <svg viewBox="0 0 24 24" width="16" height="16" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round">
        <circle cx="11" cy="11" r="6.5" /><path d="M16 16l4 4" />
      </svg>
      <input type="search" bind:value={query} placeholder="Search downloads…" aria-label="Search downloads" />
    </label>
    {#if showRange}
      <Tabs options={rangeTabs} value={view} onChange={(value) => (view = value === 'all' ? 'all' : 'recent')} ariaLabel="Download history range">
        <span class="visually-hidden">Showing {view === 'recent' ? 'recent' : 'all'} downloads</span>
      </Tabs>
    {/if}
    {#if filtered.length}
        <ul class="library" aria-label="Downloaded videos">
          {#each filtered as entry (entry.id)}
            <li class="item" class:selected={selected?.id === entry.id} class:missing={entry.fileMissing}>
              <button
                class="item-main"
                type="button"
                aria-label={`${selected?.id === entry.id ? 'Hide' : 'Show'} details for ${entry.title}`}
                aria-expanded={selected?.id === entry.id}
                on:click={() => toggle(entry)}
              >
                <span class="thumb">
                  {#if thumbnailFor(entry)}
                    <img src={thumbnailFor(entry)} alt="" referrerpolicy="no-referrer" />
                  {/if}
                  {#if entry.durationLabel}
                    <span class="duration">{entry.durationLabel}</span>
                  {/if}
                </span>
                <span class="copy">
                  <strong title={entry.title}>{entry.title}</strong>
                  <span class="meta">
                    <span class="channel">{entry.channel || 'YouTube'}</span>
                    <span aria-hidden="true">·</span>
                    <span>{formatLabel(entry)}</span>
                    {#if entry.sizeBytes}
                      <span aria-hidden="true">·</span>
                      <span>{formatBytes(entry.sizeBytes)}</span>
                    {/if}
                    {#if entry.completedAt}
                      <span aria-hidden="true">·</span>
                      <span>{formatRelative(entry.completedAt)}</span>
                    {/if}
                    {#if entry.fileMissing}
                      <span class="missing-flag">File missing</span>
                    {/if}
                  </span>
                </span>
              </button>

              <div class="item-actions">
                <button
                  type="button"
                  class="app-btn primary"
                  aria-label="Open downloaded file"
                  disabled={entry.fileMissing}
                  on:click={() => open(entry)}
                >Open</button>
                <button
                  type="button"
                  class="app-btn"
                  aria-label="Show in Finder"
                  disabled={entry.fileMissing}
                  on:click={() => reveal(entry)}
                >Show in Finder</button>
              </div>

              {#if selected?.id === entry.id}
                <div class="item-detail">
                  {#if entry.fileMissing}
                    <p class="missing-note">This file is no longer on disk. You can still remove the history entry.</p>
                  {/if}
                  <p class="path-full" title={entry.absolutePath}>
                    {#if codecSummary(entry)}{codecSummary(entry)} · {/if}{entry.absolutePath}
                  </p>
                  <div class="detail-actions">
                    <button type="button" class="app-btn" aria-label="Remove from history" on:click={() => confirmRemoveHistory(entry)}>
                      Remove
                    </button>
                    <button
                      type="button"
                      class="app-btn danger"
                      aria-label="Delete downloaded file"
                      disabled={entry.fileMissing}
                      on:click={() => confirmDeleteFile(entry)}
                    >Delete file</button>
                  </div>
                </div>
              {/if}
            </li>
          {/each}
        </ul>
        {#if view === 'recent' && $history.length > RECENT_LIMIT && !needle}
          <button type="button" class="more" on:click={() => (view = 'all')}>
            Show all {$history.length} downloads
          </button>
        {/if}
    {:else}
      <EmptyState
        icon={needle ? 'search' : 'inbox'}
        title={needle ? 'No downloads match your search.' : 'No downloads yet.'}
        message={needle ? 'Try a title, channel, or format.' : 'Finished downloads will show up here.'}
      />
    {/if}
  </div>
</section>

<style>
  .toolbar {
    display: flex;
    flex-direction: column;
    align-items: stretch;
    gap: var(--page-section-gap);
  }

  .search {
    position: relative;
    display: block;
    width: 100%;
  }
  .search svg {
    position: absolute;
    left: 12px;
    top: 50%;
    transform: translateY(-50%);
    color: var(--text-muted);
    pointer-events: none;
  }
  .search input {
    height: 40px;
    padding-left: 36px;
    font-size: var(--fs-md);
    background: var(--surface-base);
  }

  .toolbar :global(.tabs) {
    align-self: flex-start;
  }

  .library {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: var(--sp-2);
  }

  .item {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    gap: var(--sp-3) var(--sp-4);
    padding: var(--sp-3);
    background: var(--surface-base);
    border: 1px solid var(--border-default);
    border-radius: var(--r-md);
    box-shadow: var(--shadow-card);
  }
  .item:hover { border-color: var(--border-strong); }
  .item.selected { border-color: var(--accent-400); box-shadow: 0 0 0 3px var(--accent-ring); }
  .item.missing { opacity: 0.92; }

  .item-main {
    display: grid;
    grid-template-columns: 128px minmax(0, 1fr);
    align-items: center;
    gap: var(--sp-4);
    min-width: 0;
    text-align: left;
  }

  .thumb {
    position: relative;
    width: 128px;
    aspect-ratio: 16 / 9;
    border-radius: var(--r-sm);
    overflow: hidden;
    background: var(--surface-sunken);
    flex-shrink: 0;
  }
  .thumb img { width: 100%; height: 100%; object-fit: cover; display: block; }
  .duration {
    position: absolute;
    right: 5px;
    bottom: 5px;
    padding: 1px 5px;
    border-radius: 4px;
    background: rgba(17, 18, 21, 0.82);
    color: #fff;
    font-size: 10px;
    font-weight: 600;
    letter-spacing: 0.01em;
  }

  .copy { min-width: 0; }
  .copy strong {
    display: -webkit-box;
    -webkit-line-clamp: 2;
    line-clamp: 2;
    -webkit-box-orient: vertical;
    overflow: hidden;
    color: var(--text-primary);
    font-size: var(--fs-md);
    font-weight: 600;
    line-height: 1.35;
  }
  .meta {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px;
    margin-top: 6px;
    color: var(--text-secondary);
    font-size: var(--fs-sm);
  }
  .channel { color: var(--text-muted); }
  .missing-flag {
    color: var(--status-warning);
    background: var(--status-warning-soft);
    border-radius: var(--r-full);
    padding: 1px 7px;
    font-weight: 600;
    font-size: var(--fs-xs);
  }

  .item-actions {
    display: flex;
    align-items: center;
    gap: var(--sp-2);
    align-self: center;
  }

  .item-detail {
    grid-column: 1 / -1;
    display: flex;
    flex-direction: column;
    gap: var(--sp-3);
    padding: var(--sp-3) 4px 4px;
    border-top: 1px solid var(--border-subtle);
  }
  .missing-note { margin: 0; color: var(--status-warning); font-size: var(--fs-sm); }
  .path-full {
    margin: 0;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--text-muted);
    font-size: var(--fs-xs);
  }
  .detail-actions { display: flex; flex-wrap: wrap; gap: var(--sp-2); }

  .more {
    display: block;
    width: 100%;
    margin-top: var(--sp-3);
    min-height: 36px;
    color: var(--accent-600);
    font-size: var(--fs-sm);
    font-weight: 600;
  }
  .more:hover { background: var(--accent-soft); border-radius: var(--r-sm); }

  @media (max-width: 800px) {
    .item { grid-template-columns: 1fr; }
    .item-main { grid-template-columns: 96px minmax(0, 1fr); gap: var(--sp-3); }
    .thumb { width: 96px; }
    .item-actions { justify-content: flex-start; }
  }
</style>
