<script lang="ts">
  import { createEventDispatcher } from 'svelte';
  import { api } from '../lib/api.js';
  import { errorMessage, ffmpeg, modal, settings, showBanner } from '../lib/stores.js';
  import { formatBytes, shortTitle } from '../lib/format.js';
  import type { InfoSummary, OutputPlan, PlaylistSummary, Quality, UrlCheckResult } from '../lib/types.js';

  const dispatch = createEventDispatcher<{ goto: 'home' | 'queue' | 'downloads' | 'settings' | 'about' }>();

  let url = '';
  let busy = false;
  let folder = '';
  let selectedPlanId = '';
  let search = '';
  let preview: InfoSummary | null = null;
  let playlist: PlaylistSummary | null = null;
  let selectedItems = new Set<number>();
  let tab: 'video' | 'audio' | 'info' = 'video';
  let playlistTab: 'video' | 'audio' = 'video';
  let playlistQuality: Quality = '1080p';
  let audioChoice = 'original';
  let rangeStart = '';
  let rangeEnd = '';
  let selectAllBox: HTMLInputElement | undefined;

  $: folder = $settings.downloadFolder || folder;
  $: plans = preview?.plans ?? [];
  $: visiblePlans = tab === 'info' ? [] : plans.filter((plan) => plan.kind === tab);
  $: selectedPlan = plans.find((plan) => plan.id === selectedPlanId) ?? null;
  $: query = search.trim().toLowerCase();
  $: filteredEntries = playlist?.entries.filter((entry) => !query || entry.title.toLowerCase().includes(query)) ?? [];
  $: availableCount = playlist?.available ?? 0;
  $: allAvailableSelected = availableCount > 0 && selectedItems.size === availableCount;
  $: if (selectAllBox && playlist) {
    selectAllBox.indeterminate = selectedItems.size > 0 && selectedItems.size < availableCount;
  }

  const fallbackThumbnail = (videoId: string) => (videoId ? `https://i.ytimg.com/vi/${videoId}/hqdefault.jpg` : '');
  const hideBrokenImage = (event: Event) => {
    (event.currentTarget as HTMLImageElement).style.display = 'none';
  };

  async function analyzeTarget(target: UrlCheckResult) {
    preview = null;
    playlist = null;
    selectedItems = new Set();
    selectedPlanId = '';
    search = '';
    if (target.kind === 'playlist') {
      url = target.playlistUrl!;
      playlist = await api.analyse.playlist(target.playlistUrl!);
      selectedItems = new Set(playlist.entries.filter((entry) => entry.available).map((entry) => entry.index));
      rangeStart = playlist.entries[0]?.index ? String(playlist.entries[0].index) : '1';
      rangeEnd = playlist.entries.at(-1)?.index ? String(playlist.entries.at(-1)!.index) : String(playlist.entryCount);
    } else {
      url = target.videoUrl!;
      preview = await api.analyse.url(target.videoUrl!);
      const recommended = preview.plans.find((plan) => plan.recommended) ?? preview.plans[0];
      selectedPlanId = recommended?.id ?? '';
      tab = recommended?.kind ?? 'video';
    }
  }

  async function analyze() {
    if (!url.trim()) return;
    busy = true;
    try {
      const accepted = await api.validation.url(url.trim());
      if (accepted.kind === 'video_playlist') {
        modal.set({
          kind: 'confirm',
          title: 'This link includes a playlist',
          message: 'Choose what you want to review.',
          actions: [
            { label: 'This video only', action: () => withBusy(() => analyzeTarget({ ...accepted, kind: 'single_video', url: accepted.videoUrl! })) },
            { label: 'Full playlist', primary: true, action: () => withBusy(() => analyzeTarget({ ...accepted, kind: 'playlist', url: accepted.playlistUrl! })) },
          ],
        });
      } else {
        await analyzeTarget(accepted);
      }
    } catch (err) {
      modal.set({
        kind: 'error',
        title: 'Unsupported URL',
        message: errorMessage(err, 'VidStow could not extract information from this URL. Make sure it is a valid, publicly accessible YouTube video or playlist.'),
      });
    } finally {
      busy = false;
    }
  }

  async function withBusy(action: () => Promise<void>) {
    busy = true;
    try {
      await action();
    } catch (err) {
      modal.set({
        kind: 'error',
        title: 'Could not analyze link',
        message: errorMessage(err, 'Could not analyze this link.'),
      });
    } finally {
      busy = false;
    }
  }

  async function pickFolder() {
    const path = await api.folder.pick();
    if (!path) return;
    const updated = await api.settings.update({ ...$settings, downloadFolder: path });
    settings.set(updated);
    folder = path;
  }

  function choose(plan: OutputPlan) {
    selectedPlanId = plan.id;
  }

  function planDetail(plan: OutputPlan) {
    return [plan.container, plan.kind === 'video' ? plan.videoCodec : '', plan.audioCodec].filter(Boolean).join(' · ');
  }

  function toggle(index: number) {
    const next = new Set(selectedItems);
    if (next.has(index)) next.delete(index);
    else next.add(index);
    selectedItems = next;
  }

  function selectAll() {
    selectedItems = new Set(playlist?.entries.filter((entry) => entry.available).map((entry) => entry.index) ?? []);
  }

  function clearSelection() {
    selectedItems = new Set();
  }

  function toggleSelectAll() {
    if (allAvailableSelected) clearSelection();
    else selectAll();
  }

  function applyRange() {
    if (!playlist) return;
    const start = Number(rangeStart);
    const end = Number(rangeEnd);
    if (!Number.isInteger(start) || !Number.isInteger(end) || start < 1 || end < 1) return;
    const low = Math.min(start, end);
    const high = Math.max(start, end);
    selectedItems = new Set(
      playlist.entries
        .filter((entry) => entry.available && entry.index >= low && entry.index <= high)
        .map((entry) => entry.index),
    );
  }

  function requireFFmpeg(message: string) {
    modal.set({
      kind: 'ffmpeg-missing',
      title: 'FFmpeg Required',
      message,
      actions: [{ label: 'Open Settings', primary: true, action: () => dispatch('goto', 'settings') }],
    });
  }

  async function enqueueVideo() {
    if (!preview || !selectedPlan || !folder) return;
    if (selectedPlan.requiresFfmpeg && !$ffmpeg.available) {
      requireFFmpeg('This output needs FFmpeg for merging or conversion. Install FFmpeg, set its path in Settings, or choose an original audio option.');
      return;
    }
    const start = async () => {
      try {
        await api.jobs.start({
          url: preview!.url,
          videoId: preview!.videoId,
          title: preview!.title,
          channel: preview!.channel,
          planId: selectedPlan!.id,
          outputDir: folder,
          duration: preview!.duration,
          thumbnail: preview!.thumbnail,
        });
        showBanner('success', 'Added to queue');
        dispatch('goto', 'queue');
      } catch (err) {
        modal.set({ kind: 'error', title: 'Download could not start', message: errorMessage(err, 'Could not start this download.') });
      }
    };
    if ($settings.confirmBeforeDownload) {
      modal.set({
        kind: 'confirm',
        title: 'Add this download?',
        message: `${selectedPlan.label} · ${selectedPlan.container}${selectedPlan.approxBytes ? ` · about ${formatBytes(selectedPlan.approxBytes)}` : ''}`,
        actions: [{ label: 'Add to Queue', primary: true, action: start }],
      });
      return;
    }
    await start();
  }

  async function enqueuePlaylist() {
    if (!playlist || !selectedItems.size) return;
    const quality: Quality = playlistTab === 'audio' ? 'audio' : playlistQuality;
    const audioBitrate = playlistTab === 'audio' && audioChoice !== 'original' ? Number(audioChoice) : 0;
    if (audioBitrate && !$ffmpeg.available) {
      requireFFmpeg('MP3 conversion needs FFmpeg. Choose original audio or configure FFmpeg.');
      return;
    }
    const start = async () => {
      try {
        await api.jobs.startPlaylist({
          url: playlist!.url,
          playlistId: playlist!.id,
          title: playlist!.title,
          channel: playlist!.channel,
          thumbnail: playlist!.thumbnail,
          quality,
          audioBitrate,
          outputDir: folder,
          selectedItems: [...selectedItems].sort((a, b) => a - b),
        });
        showBanner('success', `Added ${selectedItems.size} videos to queue`);
        dispatch('goto', 'queue');
      } catch (err) {
        modal.set({ kind: 'error', title: 'Playlist could not start', message: errorMessage(err, 'Could not add this playlist to the queue.') });
      }
    };
    if (selectedItems.size > 100 || $settings.confirmBeforeDownload) {
      modal.set({
        kind: 'confirm',
        title: 'Add this playlist?',
        message: `${selectedItems.size} videos will be added to the queue.`,
        actions: [{ label: 'Add to Queue', primary: true, action: start }],
      });
      return;
    }
    await start();
  }
</script>

<section class="page" class:fill={!!playlist || !!preview} aria-labelledby="home-title">
  <header class="page-header">
    <h1 id="home-title">Download from YouTube</h1>
    {#if !playlist && !preview}
      <p>Paste a public YouTube video or playlist URL to analyze it and choose your download.</p>
    {/if}
  </header>

  <form class="analyze-bar" on:submit|preventDefault={analyze}>
    <label class="visually-hidden" for="video-url">YouTube video or playlist URL</label>
    <input id="video-url" type="url" bind:value={url} placeholder="https://www.youtube.com/watch?v=…" autocomplete="off" />
    <button class="primary" type="submit" disabled={busy || !url.trim()}>{busy ? 'Analyzing…' : 'Analyze'}</button>
  </form>

  {#if playlist}
    <section class="workspace" aria-label="Playlist">
      <header class="identity">
        <div class="thumb">
          {#if playlist.thumbnail}<img src={playlist.thumbnail} alt="" referrerpolicy="no-referrer" on:error={hideBrokenImage} />{/if}
        </div>
        <div class="identity-copy">
          <strong title={playlist.title}>{playlist.title}</strong>
          <span>
            <em>Playlist</em>
            {#if playlist.channel} · {playlist.channel}{/if}
            · {playlist.entryCount} videos
            {#if playlist.unavailable} · {playlist.unavailable} unavailable{/if}
          </span>
          <small aria-live="polite">{selectedItems.size} of {availableCount} selected</small>
        </div>
        <div class="policy">
          <h2>Format</h2>
          <div class="segment" role="tablist" aria-label="Output type">
            <button type="button" class:active={playlistTab === 'video'} on:click={() => playlistTab = 'video'}>Video</button>
            <button type="button" class:active={playlistTab === 'audio'} on:click={() => playlistTab = 'audio'}>Audio</button>
          </div>
          {#if playlistTab === 'video'}
            <label class="visually-hidden" for="playlist-quality">Video quality</label>
            <select id="playlist-quality" bind:value={playlistQuality}>
              <option value="best">Best available</option>
              <option value="4k">Up to 4K</option>
              <option value="1440p">Up to 1440p</option>
              <option value="1080p">Up to 1080p</option>
              <option value="720p">Up to 720p</option>
            </select>
          {:else}
            <label class="visually-hidden" for="playlist-audio">Audio format</label>
            <select id="playlist-audio" bind:value={audioChoice}>
              <option value="original">Original audio</option>
              <option value="128">MP3 · 128 kbps</option>
              <option value="192">MP3 · 192 kbps</option>
              <option value="256">MP3 · 256 kbps</option>
            </select>
          {/if}
        </div>
      </header>

      <div class="toolbar">
        <label class="check">
          <input type="checkbox" bind:this={selectAllBox} checked={allAvailableSelected} on:change={toggleSelectAll} />
          All available
        </label>
        <button type="button" class="ghost" on:click={clearSelection} disabled={!selectedItems.size}>Clear</button>
        <form class="range" on:submit|preventDefault={applyRange}>
          <span>Range</span>
          <input type="number" min="1" inputmode="numeric" bind:value={rangeStart} aria-label="Range start" />
          <span class="dash">–</span>
          <input type="number" min="1" inputmode="numeric" bind:value={rangeEnd} aria-label="Range end" />
          <button type="submit" class="ghost">Apply</button>
        </form>
        <input type="search" bind:value={search} placeholder="Search playlist…" aria-label="Search playlist" />
      </div>

      <div class="entry-list" role="list">
        {#each filteredEntries as entry (entry.index)}
          <label class="entry" class:unavailable={!entry.available} class:selected={selectedItems.has(entry.index)} role="listitem">
            <input type="checkbox" checked={selectedItems.has(entry.index)} disabled={!entry.available} on:change={() => toggle(entry.index)} />
            <span class="number">{entry.index}</span>
            <span class="mini">
              {#if entry.thumbnail || entry.videoId}
                <img src={entry.thumbnail || fallbackThumbnail(entry.videoId)} alt="" referrerpolicy="no-referrer" on:error={hideBrokenImage} />
              {/if}
            </span>
            <strong title={entry.title}>{entry.title}</strong>
            {#if !entry.available}
              <span class="meta">Unavailable</span>
            {:else if entry.duration}
              <span class="meta">{entry.duration}</span>
            {/if}
          </label>
        {:else}
          <div class="empty-list">{query ? 'No videos match that search.' : 'No videos in this playlist.'}</div>
        {/each}
      </div>

      <footer class="save-bar">
        <div class="destination">
          <span>Save to</span>
          <strong title={folder}>{folder}</strong>
          <small title={`${playlist.title} [${playlist.id}]`}>Playlist folder · {shortTitle(playlist.title, 48)}</small>
        </div>
        <button type="button" class="secondary" on:click={pickFolder}>Change…</button>
        <button type="button" class="primary queue" on:click={enqueuePlaylist} disabled={!selectedItems.size}>
          Add {selectedItems.size} {selectedItems.size === 1 ? 'Video' : 'Videos'} to Queue
        </button>
      </footer>
    </section>
  {:else if preview}
    <section class="workspace video" aria-label="Video">
      <header class="identity">
        <div class="thumb thumbnail">
          {#if preview.thumbnail}<img src={preview.thumbnail} alt="" referrerpolicy="no-referrer" />{/if}
          {#if preview.duration}<span>{preview.duration}</span>{/if}
        </div>
        <div class="identity-copy">
          <strong title={preview.title}>{preview.title}</strong>
          <span>{preview.channel || 'YouTube'}</span>
          <small>{preview.duration || 'Duration unavailable'}{preview.viewCount ? ` · ${preview.viewCount.toLocaleString()} views` : ''}</small>
        </div>
        <div class="policy">
          <h2>Choose Download</h2>
          <div class="segment" role="tablist" aria-label="Output type">
            <button type="button" class:active={tab === 'video'} on:click={() => tab = 'video'}>Video</button>
            <button type="button" class:active={tab === 'audio'} on:click={() => tab = 'audio'}>Audio</button>
          </div>
        </div>
      </header>

      {#if visiblePlans.length}
        <div class="plan-list pane" role="radiogroup" aria-label={`${tab} output options`}>
          {#each visiblePlans as plan (plan.id)}
            <button type="button" class="plan-row" class:selected={selectedPlanId === plan.id} role="radio" aria-checked={selectedPlanId === plan.id} on:click={() => choose(plan)}>
              <span class="radio"></span>
              <span class="plan-copy">
                <strong>
                  {plan.label}
                  {#if plan.recommended}<em>Recommended</em>{/if}
                </strong>
                <small>{planDetail(plan) || plan.container}</small>
              </span>
              <span class="plan-size">{plan.approxBytes ? `${plan.sizeIsApproximate ? '~' : ''}${formatBytes(plan.approxBytes)}` : '—'}</span>
            </button>
          {/each}
        </div>
      {:else}
        <div class="empty pane">No {tab} outputs were reported for this video.</div>
      {/if}

      <footer class="save-bar">
        <div class="destination">
          <span>Save to</span>
          <strong title={folder}>{folder}</strong>
          {#if selectedPlan}
            <small>{selectedPlan.label} · {selectedPlan.container}{selectedPlan.approxBytes ? ` · ${selectedPlan.sizeIsApproximate ? '~' : ''}${formatBytes(selectedPlan.approxBytes)}` : ''}</small>
          {/if}
        </div>
        <button type="button" class="secondary" on:click={pickFolder}>Change…</button>
        <button type="button" class="primary queue" on:click={enqueueVideo} disabled={!selectedPlan}>Add to Queue</button>
      </footer>
    </section>
  {:else}
    <section class="welcome">
      <div class="download-mark" aria-hidden="true">
        <svg viewBox="0 0 24 24" width="26" height="26" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">
          <path d="M12 4v12m0 0l-4-4m4 4l4-4M5 20h14" />
        </svg>
      </div>
      <h2>Add a YouTube link</h2>
      <p>VidStow will show the available video and audio outputs before anything downloads.</p>
    </section>
  {/if}
</section>

<style>
  .page.fill {
    height: 100%;
    min-height: 0;
    overflow: hidden;
    padding-bottom: 20px;
  }
  .page.fill .page-header {
    margin-bottom: 12px;
  }
  .analyze-bar {
    display: grid;
    grid-template-columns: minmax(0, 1fr) 118px;
    gap: 10px;
    flex-shrink: 0;
  }
  .analyze-bar input { height: 42px; }
  .primary, .secondary, .ghost {
    min-height: 36px;
    padding: 0 14px;
    border-radius: var(--r-md);
    font-weight: 600;
    font-size: var(--fs-sm);
  }
  .primary {
    color: #fff;
    background: var(--accent-600);
    border: 1px solid var(--accent-600);
  }
  .primary:hover:not(:disabled) { background: var(--accent-500); border-color: var(--accent-500); }
  .primary:disabled { opacity: 0.45; }
  .secondary, .ghost {
    border: 1px solid var(--border-default);
    background: var(--surface-raised);
    color: var(--text-primary);
  }
  .ghost { min-height: 32px; padding: 0 10px; font-weight: 550; }
  .secondary:hover:not(:disabled), .ghost:hover:not(:disabled) { background: var(--surface-hover); }

  .workspace {
    flex: 1;
    min-height: 0;
    margin-top: 16px;
    display: flex;
    flex-direction: column;
    border: 1px solid var(--border-default);
    border-radius: var(--r-lg);
    background: var(--surface-raised);
    box-shadow: var(--shadow-card);
    overflow: hidden;
  }
  .identity {
    display: grid;
    grid-template-columns: 88px minmax(0, 1fr) auto;
    gap: 14px;
    align-items: center;
    padding: 14px 16px;
    border-bottom: 1px solid var(--border-subtle);
    flex-shrink: 0;
  }
  .thumb, .mini, .thumbnail {
    overflow: hidden;
    border-radius: var(--r-sm);
    background: var(--surface-sunken);
  }
  .thumb {
    width: 88px;
    aspect-ratio: 16 / 9;
    position: relative;
  }
  .thumb span {
    position: absolute;
    right: 4px;
    bottom: 4px;
    padding: 1px 5px;
    border-radius: 3px;
    background: #111d;
    color: #fff;
    font-size: 10px;
  }
  .thumb img, .mini img, .thumbnail img {
    width: 100%;
    height: 100%;
    object-fit: cover;
    display: block;
  }
  .identity-copy {
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 3px;
  }
  .identity-copy strong, .entry strong {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .identity-copy strong { font-size: var(--fs-md); letter-spacing: -0.015em; }
  .identity-copy span, .identity-copy small { color: var(--text-secondary); font-size: var(--fs-xs); }
  .identity-copy em {
    font-style: normal;
    font-weight: 650;
    color: var(--text-secondary);
  }
  .identity-copy small { color: var(--text-secondary); font-weight: 550; }

  .policy {
    display: grid;
    grid-template-columns: auto minmax(168px, 200px);
    gap: 8px 10px;
    align-items: center;
  }
  .policy h2 {
    grid-column: 1 / -1;
    margin: 0;
    color: var(--text-secondary);
    font-size: 11px;
    font-weight: 600;
  }
  .segment {
    display: inline-flex;
    padding: 3px;
    background: var(--surface-sunken);
    border: 1px solid var(--border-default);
    border-radius: var(--r-md);
  }
  .segment button {
    min-width: 68px;
    min-height: 28px;
    padding: 0 10px;
    border-radius: 6px;
    color: var(--text-secondary);
    font-size: var(--fs-xs);
    font-weight: 600;
  }
  .segment button.active {
    color: var(--text-primary);
    background: var(--surface-base);
    box-shadow: var(--shadow-card);
  }
  .policy select { height: 34px; padding: 0 10px; font-size: var(--fs-xs); }

  .toolbar {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px 12px;
    padding: 10px 16px;
    border-bottom: 1px solid var(--border-subtle);
    background: var(--surface-subtle);
    flex-shrink: 0;
  }
  .check {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    font-size: var(--fs-xs);
    font-weight: 600;
    color: var(--text-secondary);
    white-space: nowrap;
  }
  .range {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    color: var(--text-secondary);
    font-size: var(--fs-xs);
    font-weight: 600;
  }
  .range input {
    width: 56px;
    height: 32px;
    padding: 0 8px;
    text-align: center;
  }
  .toolbar input[type='search'] {
    margin-left: auto;
    width: min(240px, 100%);
    height: 32px;
    padding: 0 10px;
    font-size: var(--fs-xs);
  }

  .entry-list {
    flex: 1;
    min-height: 220px;
    overflow: auto;
  }
  .entry {
    display: grid;
    grid-template-columns: 16px 36px 64px minmax(0, 1fr) auto;
    gap: 10px;
    align-items: center;
    padding: 8px 16px;
    border-bottom: 1px solid var(--border-subtle);
    cursor: pointer;
  }
  .entry:hover { background: var(--surface-hover); }
  .entry.unavailable {
    opacity: 0.55;
    cursor: default;
  }
  .entry .number, .entry .meta {
    font-size: var(--fs-xs);
    color: var(--text-muted);
    font-variant-numeric: tabular-nums;
  }
  .entry strong { font-size: var(--fs-sm); font-weight: 550; }
  .mini {
    width: 64px;
    aspect-ratio: 16 / 9;
  }
  .empty-list {
    min-height: 160px;
    display: grid;
    place-items: center;
    color: var(--text-muted);
    font-size: var(--fs-sm);
  }

  .save-bar {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto auto;
    gap: 10px;
    align-items: center;
    padding: 12px 16px;
    border-top: 1px solid var(--border-subtle);
    background: var(--surface-base);
    flex-shrink: 0;
  }
  .destination {
    display: flex;
    min-width: 0;
    flex-wrap: wrap;
    gap: 6px 8px;
    align-items: baseline;
  }
  .destination span { color: var(--text-secondary); font-size: var(--fs-sm); }
  .destination strong {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: var(--fs-sm);
  }
  .destination small {
    flex-basis: 100%;
    color: var(--text-muted);
    font-size: 11px;
  }
  .queue { min-width: 168px; min-height: 40px; }

  .pane {
    flex: 1;
    min-height: 0;
    overflow: auto;
  }
  .plan-list {
    display: flex;
    flex-direction: column;
  }
  .plan-row {
    display: grid;
    grid-template-columns: 20px minmax(0, 1fr) auto;
    gap: 12px;
    align-items: center;
    width: 100%;
    min-height: 52px;
    padding: 10px 18px;
    border-top: 1px solid var(--border-subtle);
    text-align: left;
    color: var(--text-secondary);
  }
  .plan-row:first-child { border-top: 0; }
  .plan-row:hover { background: var(--surface-hover); }
  .plan-row.selected { background: var(--accent-soft); }
  .plan-copy { min-width: 0; }
  .plan-copy strong {
    display: flex;
    align-items: center;
    gap: 8px;
    color: var(--text-primary);
    font-size: var(--fs-sm);
  }
  .plan-copy em {
    font-style: normal;
    font-size: 10px;
    font-weight: 650;
    letter-spacing: 0.02em;
    text-transform: uppercase;
    color: var(--accent-600);
  }
  .plan-copy small {
    display: block;
    margin-top: 3px;
    color: var(--text-secondary);
    font-size: 11px;
  }
  .plan-size {
    color: var(--text-primary);
    font-size: var(--fs-sm);
    font-weight: 600;
    font-variant-numeric: tabular-nums;
    white-space: nowrap;
  }
  .radio {
    width: 14px;
    height: 14px;
    border: 1.5px solid var(--border-strong);
    border-radius: 50%;
  }
  .selected .radio { border: 4px solid var(--accent-600); }
  .empty {
    display: grid;
    place-items: center;
    color: var(--text-secondary);
    font-size: var(--fs-sm);
  }

  .welcome {
    flex: 1;
    min-height: 280px;
    display: grid;
    place-content: center;
    justify-items: center;
    text-align: center;
    color: var(--text-secondary);
  }
  .welcome h2 { margin: 16px 0 6px; font-size: var(--fs-lg); color: var(--text-primary); }
  .welcome p { max-width: 440px; margin: 0; line-height: 1.55; font-size: var(--fs-sm); }
  .download-mark {
    width: 52px;
    height: 52px;
    display: grid;
    place-items: center;
    border-radius: var(--r-lg);
    background: var(--accent-soft);
    color: var(--accent-600);
  }

  @media (max-width: 860px) {
    .identity { grid-template-columns: 72px 1fr; }
    .policy { grid-column: 1 / -1; }
    .toolbar input[type='search'] { margin-left: 0; width: 100%; flex-basis: 100%; }
  }
  @media (max-width: 720px) {
    .save-bar { grid-template-columns: 1fr auto; }
    .destination { grid-column: 1 / -1; }
    .queue { grid-column: 1 / -1; }
    .entry { grid-template-columns: 16px 28px minmax(0, 1fr) auto; }
    .mini { display: none; }
  }
</style>
