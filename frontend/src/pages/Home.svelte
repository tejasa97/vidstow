<script lang="ts">
  import { createEventDispatcher, onDestroy } from 'svelte';
  import { api } from '../lib/api.js';
  import { errorMessage, ffmpeg, modal, pendingUrl, settings, showBanner } from '../lib/stores.js';
  import { formatBytes, formatViewCount, shortTitle } from '../lib/format.js';
  import type { BatchAnalysisView, InfoSummary, OutputPlan, PlaylistSummary, Quality, UrlCheckResult } from '../lib/types.js';

  const dispatch = createEventDispatcher<{ goto: 'home' | 'queue' | 'downloads' | 'settings' | 'about' }>();

  let inputMode: 'single' | 'batch' = 'single';
  let url = '';
  let analysisGeneration = 0;
  let busy = false;
  let batchText = '';
  let batchGeneration = 0;
  let batchBusy = false;
  let batchReview: BatchAnalysisView | null = null;
  let batchNow = Date.now();
  let batchTab: 'video' | 'audio' = 'video';
  let batchQuality: Quality = '1080p';
  let batchAudioChoice = 'original';
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
  let linkedPlaylist: UrlCheckResult | null = null;
  const PLAYLIST_ADMIT_CAP = 500;
  const batchExpiryTimer = setInterval(() => batchNow = Date.now(), 1000);
  onDestroy(() => clearInterval(batchExpiryTimer));

  $: folder = $settings.downloadFolder || folder;
  $: plans = preview?.plans ?? [];
  $: visiblePlans = tab === 'info' ? [] : plans.filter((plan) => plan.kind === tab);
  $: selectedPlan = plans.find((plan) => plan.id === selectedPlanId) ?? null;
  $: query = search.trim().toLowerCase();
  $: filteredEntries = playlist?.entries.filter((entry) => !query || entry.title.toLowerCase().includes(query)) ?? [];
  $: availableCount = playlist?.available ?? 0;
  $: playlistFirstIndex = playlist?.entries[0]?.index ?? 1;
  $: playlistLastIndex = playlist?.entries.at(-1)?.index ?? playlist?.entryCount ?? 1;
  $: allAvailableSelected = availableCount > 0 && selectedItems.size === availableCount;
  $: playlistAtCap = (playlist?.entries.length ?? 0) >= PLAYLIST_ADMIT_CAP;
  $: batchInputLineCount = batchText.split(/\r?\n/).filter((line) => line.trim()).length;
  $: batchReadyCount = batchReview?.counts.ready ?? 0;
  $: batchExpiry = batchReview?.expiresAt ? Date.parse(batchReview.expiresAt) : Number.NaN;
  $: batchTokenValid = !!batchReview?.token && Number.isFinite(batchExpiry) && batchExpiry > batchNow;
  $: batchCanStart = batchTokenValid && batchReadyCount >= 2 && !!folder && !batchBusy;
  $: if (selectAllBox && playlist) {
    selectAllBox.indeterminate = selectedItems.size > 0 && selectedItems.size < availableCount;
  }

  $: if ($pendingUrl) {
    const droppedURL = $pendingUrl;
    pendingUrl.set('');
    inputMode = 'single';
    url = droppedURL;
    analyze();
  }

  const fallbackThumbnail = (videoId: string) => (videoId ? `https://i.ytimg.com/vi/${videoId}/hqdefault.jpg` : '');
  const batchReviewSummary = (review: BatchAnalysisView) => {
    const { pasted, ready, duplicate, invalid, analysisFailed } = review.counts;
    if (ready === pasted && duplicate === 0 && invalid === 0 && analysisFailed === 0) {
      return `${ready} ${ready === 1 ? 'video' : 'videos'} ready to download`;
    }
    return [
      ready ? `${ready} ready` : '',
      duplicate ? `${duplicate} duplicate` : '',
      invalid ? `${invalid} invalid` : '',
      analysisFailed ? `${analysisFailed} could not be analyzed` : '',
    ].filter(Boolean).join(' · ');
  };
  const hideBrokenImage = (event: Event) => {
    (event.currentTarget as HTMLImageElement).style.display = 'none';
  };

  function clearAnalysis() {
    preview = null;
    playlist = null;
    selectedItems = new Set();
    selectedPlanId = '';
    search = '';
  }

  function updateURL(event: Event) {
    const nextURL = (event.currentTarget as HTMLInputElement).value;
    if (nextURL === url) return;
    url = nextURL;
    analysisGeneration += 1;
    busy = false;
    linkedPlaylist = null;
    if (preview || playlist) clearAnalysis();
  }

  function setInputMode(mode: 'single' | 'batch') {
    inputMode = mode;
  }

  function updateBatchText(event: Event) {
    const nextText = (event.currentTarget as HTMLTextAreaElement).value;
    if (nextText === batchText) return;
    batchText = nextText;
    batchGeneration += 1;
    batchBusy = false;
    batchReview = null;
  }

  async function analyzeBatch() {
    if (!batchText.trim() || batchBusy) return;
    const requestGeneration = ++batchGeneration;
    batchBusy = true;
    try {
      const review = await api.analyse.batch(batchText);
      if (requestGeneration !== batchGeneration) return;
      batchReview = review;
    } catch (err) {
      if (requestGeneration !== batchGeneration) return;
      modal.set({
        kind: 'error',
        title: 'Batch could not be reviewed',
        message: errorMessage(err, 'Paste between 2 and 20 individual public YouTube video or Short URLs.'),
      });
    } finally {
      if (requestGeneration === batchGeneration) batchBusy = false;
    }
  }

  function editBatchURLs() {
    batchGeneration += 1;
    batchBusy = false;
    batchReview = null;
  }

  async function enqueueBatch() {
    if (!batchReview?.token || !batchCanStart) return;
    const quality: Quality = batchTab === 'audio' ? 'audio' : batchQuality;
    const audioBitrate = batchTab === 'audio' && batchAudioChoice !== 'original' ? Number(batchAudioChoice) : 0;
    if (audioBitrate && !$ffmpeg.available) {
      requireFFmpeg('MP3 conversion needs FFmpeg. Choose original audio or configure FFmpeg.');
      return;
    }
    batchBusy = true;
    try {
      const result = await api.jobs.startBatch({ token: batchReview.token, quality, audioBitrate });
      showBanner('success', `Added ${result.admitted} downloads to the queue`);
      batchReview = null;
      batchText = '';
      dispatch('goto', 'queue');
    } catch (err) {
      modal.set({ kind: 'error', title: 'Batch could not start', message: errorMessage(err, 'Could not add this batch to the queue.') });
    } finally {
      batchBusy = false;
    }
  }

  async function analyzeTarget(target: UrlCheckResult, requestGeneration: number) {
    if (requestGeneration !== analysisGeneration) return;
    clearAnalysis();
    if (target.kind === 'playlist') {
      const canonicalURL = target.playlistUrl!;
      const summary = await api.analyse.playlist(canonicalURL);
      if (requestGeneration !== analysisGeneration) return;
      url = canonicalURL;
      playlist = summary;
      selectedItems = new Set(summary.entries.filter((entry) => entry.available).map((entry) => entry.index));
      rangeStart = summary.entries[0]?.index ? String(summary.entries[0].index) : '1';
      rangeEnd = summary.entries.at(-1)?.index ? String(summary.entries.at(-1)!.index) : String(summary.entryCount);
    } else {
      const canonicalURL = target.videoUrl!;
      const summary = await api.analyse.url(canonicalURL);
      if (requestGeneration !== analysisGeneration) return;
      url = canonicalURL;
      preview = summary;
      const recommended = summary.plans.find((plan) => plan.recommended) ?? summary.plans[0];
      selectedPlanId = recommended?.id ?? '';
      tab = recommended?.kind ?? 'video';
    }
  }

  async function analyze() {
    const submittedURL = url.trim();
    if (!submittedURL) return;
    const requestGeneration = ++analysisGeneration;
    busy = true;
    linkedPlaylist = null;
    try {
      const accepted = await api.validation.url(submittedURL);
      if (requestGeneration !== analysisGeneration) return;
      if (accepted.kind === 'video_playlist') {
        linkedPlaylist = accepted;
        modal.set({
          kind: 'confirm',
          title: 'This link includes a playlist',
          message: 'Choose what you want to review.',
          actions: [
            { label: 'This video only', action: () => withBusy(() => analyzeTarget({ ...accepted, kind: 'single_video', url: accepted.videoUrl! }, requestGeneration), requestGeneration) },
            { label: 'Full playlist', primary: true, action: () => withBusy(() => analyzeTarget({ ...accepted, kind: 'playlist', url: accepted.playlistUrl! }, requestGeneration), requestGeneration) },
          ],
        });
      } else {
        await analyzeTarget(accepted, requestGeneration);
      }
    } catch (err) {
      if (requestGeneration !== analysisGeneration) return;
      modal.set({
        kind: 'error',
        title: 'Unsupported URL',
        message: errorMessage(err, 'VidStow could not extract information from this URL. Make sure it is a valid, publicly accessible YouTube video, Short, or playlist.'),
      });
    } finally {
      if (requestGeneration === analysisGeneration) busy = false;
    }
  }

  async function withBusy(action: () => Promise<void>, requestGeneration: number) {
    if (requestGeneration !== analysisGeneration) return;
    busy = true;
    try {
      await action();
    } catch (err) {
      if (requestGeneration !== analysisGeneration) return;
      modal.set({
        kind: 'error',
        title: 'Could not analyze link',
        message: errorMessage(err, 'Could not analyze this link.'),
      });
    } finally {
      if (requestGeneration === analysisGeneration) busy = false;
    }
  }

  async function pickFolder() {
    try {
      const path = await api.folder.pick();
      if (!path) return;
      const updated = await api.settings.update({ ...$settings, downloadFolder: path });
      settings.set(updated);
      folder = path;
    } catch (err) {
      modal.set({ kind: 'error', title: 'Folder could not be changed', message: errorMessage(err, 'Could not update the download folder.') });
    }
  }

  function choose(plan: OutputPlan) {
    selectedPlanId = plan.id;
  }

  function setTab(kind: 'video' | 'audio') {
    tab = kind;
    const compatible = plans.find((plan) => plan.kind === kind && plan.recommended) ?? plans.find((plan) => plan.kind === kind);
    selectedPlanId = compatible?.id ?? '';
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
    if (!Number.isInteger(start) || !Number.isInteger(end) || start < playlistFirstIndex || end < playlistFirstIndex || start > playlistLastIndex || end > playlistLastIndex) {
      showBanner('warning', `Enter whole-number positions from ${playlistFirstIndex} to ${playlistLastIndex}.`);
      return;
    }
    const low = Math.min(start, end);
    const high = Math.max(start, end);
    selectedItems = new Set(
      playlist.entries
        .filter((entry) => entry.available && entry.index >= low && entry.index <= high)
        .map((entry) => entry.index),
    );
  }

  function reviewLinkedPlaylist() {
    if (!linkedPlaylist?.playlistUrl) return;
    const requestGeneration = ++analysisGeneration;
    withBusy(
      () => analyzeTarget({ ...linkedPlaylist!, kind: 'playlist', url: linkedPlaylist!.playlistUrl! }, requestGeneration),
      requestGeneration,
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
    if (!folder) {
      showBanner('warning', 'Choose a download folder before adding this playlist.');
      return;
    }
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
          quality,
          audioBitrate,
          selectedItems: [...selectedItems].sort((a, b) => a - b),
        });
        showBanner('success', `Added ${selectedItems.size} videos to queue`);
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

<section class="page" class:fill={inputMode === 'single' && (!!playlist || !!preview)} aria-labelledby="home-title">
  <header class="page-header">
    <h1 id="home-title">{inputMode === 'batch' ? 'Batch URLs' : 'Download from YouTube'}</h1>
    {#if inputMode === 'batch'}
      <p>Review 2–20 individual public YouTube video or Short URLs before adding them to the queue.</p>
    {:else if !playlist && !preview}
      <p>Paste a public YouTube video, Short, or playlist URL to analyze it and choose your download.</p>
    {/if}
  </header>

  <div class="input-mode" aria-label="Download input mode">
    <button type="button" aria-pressed={inputMode === 'single'} class:active={inputMode === 'single'} on:click={() => setInputMode('single')}>Single URL</button>
    <button type="button" aria-pressed={inputMode === 'batch'} class:active={inputMode === 'batch'} on:click={() => setInputMode('batch')}>Batch URLs</button>
  </div>

  {#if inputMode === 'batch'}
    {#if batchReview}
      <section class="batch-review" aria-labelledby="batch-review-title">
        <header class="batch-review-header">
          <div>
            <h2 id="batch-review-title">Review URLs</h2>
            <p aria-live="polite">{batchReviewSummary(batchReview)}</p>
            {#if !batchTokenValid}<p class="batch-expired" role="alert">This review expired. Edit the lines and review them again.</p>{/if}
          </div>
          <button type="button" class="app-btn" on:click={editBatchURLs} disabled={batchBusy}>Edit URLs</button>
        </header>

        <div class="batch-lines" role="list" aria-label="Reviewed batch URLs">
          {#each batchReview.items as item (item.lineNumber)}
            <article class="batch-line" data-status={item.status} role="listitem">
              <span class="batch-line-number" aria-label={`Line ${item.lineNumber}`}>{item.lineNumber}</span>
              <div class="batch-thumbnail" aria-hidden="true">
                <svg viewBox="0 0 24 24" aria-hidden="true">
                  <rect x="3" y="5" width="18" height="14" rx="2"></rect>
                  <path d="m10 9 5 3-5 3Z"></path>
                </svg>
                {#if item.status === 'ready' && item.thumbnail}
                  <img src={item.thumbnail} alt="" referrerpolicy="no-referrer" on:error={hideBrokenImage} />
                {/if}
              </div>
              <div class="batch-line-copy">
                <strong title={item.title || item.input}>{item.title || item.input}</strong>
                <span title={item.input}>{item.input}</span>
                {#if item.title && (item.channel || item.duration)}<small>{[item.channel, item.duration].filter(Boolean).join(' · ')}</small>{/if}
              </div>
              <div class="batch-line-state">
                <span class="batch-state">{item.status === 'analysis_failed' ? 'Analysis failed' : item.status === 'duplicate' ? 'Duplicate' : item.status === 'invalid' ? 'Invalid URL' : 'Ready'}</span>
                {#if item.status !== 'ready'}<small>{item.message}</small>{/if}
              </div>
            </article>
          {/each}
        </div>

        <div class="batch-policy">
          <div>
            <strong>Format</strong>
            <small>Every ready video uses this format.</small>
          </div>
          <div class="segment" aria-label="Batch output type">
            <button type="button" aria-pressed={batchTab === 'video'} class:active={batchTab === 'video'} on:click={() => batchTab = 'video'}>Video</button>
            <button type="button" aria-pressed={batchTab === 'audio'} class:active={batchTab === 'audio'} on:click={() => batchTab = 'audio'}>Audio</button>
          </div>
          {#if batchTab === 'video'}
            <label class="visually-hidden" for="batch-quality">Batch video quality</label>
            <select id="batch-quality" bind:value={batchQuality}>
              <option value="best">Best available</option>
              <option value="4k">Up to 4K</option>
              <option value="1440p">Up to 1440p</option>
              <option value="1080p">Up to 1080p</option>
              <option value="720p">Up to 720p</option>
            </select>
          {:else}
            <label class="visually-hidden" for="batch-audio">Batch audio format</label>
            <select id="batch-audio" bind:value={batchAudioChoice}>
              <option value="original">Original audio</option>
              <option value="128">MP3 · 128 kbps</option>
              <option value="192">MP3 · 192 kbps</option>
              <option value="256">MP3 · 256 kbps</option>
            </select>
          {/if}
        </div>

        <footer class="batch-save-bar">
          <div class="destination">
            <span>Save to</span>
            <strong title={folder}>{folder || 'Choose a download folder'}</strong>
          </div>
          <button type="button" class="app-btn" on:click={pickFolder} disabled={batchBusy}>Change…</button>
          <button type="button" class="app-btn primary" on:click={enqueueBatch} disabled={!batchCanStart}>
            {batchBusy ? 'Starting…' : `Start ${batchReadyCount} downloads`}
          </button>
        </footer>
      </section>
    {:else}
      <form class="batch-composer" on:submit|preventDefault={analyzeBatch}>
        <label for="batch-urls">YouTube video or Short URLs</label>
        <textarea id="batch-urls" value={batchText} on:input={updateBatchText} rows="9" placeholder={'https://www.youtube.com/watch?v=…\nhttps://youtu.be/…\nhttps://www.youtube.com/shorts/…'} autocomplete="off"></textarea>
        <div class="batch-composer-footer">
          <p>One URL per line. Blank lines are ignored; duplicates are identified before anything downloads.</p>
          <button class="app-btn primary" type="submit" disabled={batchBusy || batchInputLineCount < 2}>{batchBusy ? 'Analyzing…' : 'Review URLs'}</button>
        </div>
      </form>
    {/if}
  {:else}
    <form class="analyze-bar" on:submit|preventDefault={analyze}>
      <label class="visually-hidden" for="video-url">YouTube video, Short, or playlist URL</label>
      <input id="video-url" type="url" value={url} on:input={updateURL} placeholder="https://www.youtube.com/watch?v=…, shorts/…, or playlist?list=…" autocomplete="off" />
      <button class="app-btn primary" type="submit" disabled={busy || !url.trim()}>{busy ? 'Analyzing…' : 'Analyze'}</button>
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
          {#if playlistAtCap}
            <small class="cap-note">VidStow can review up to {PLAYLIST_ADMIT_CAP} videos from a playlist.</small>
          {/if}
        </div>
        <div class="policy">
          <h2>Format</h2>
          <p class="policy-note">Every selected video uses this format.</p>
          <div class="segment" aria-label="Output type">
            <button type="button" aria-pressed={playlistTab === 'video'} class:active={playlistTab === 'video'} on:click={() => playlistTab = 'video'}>Video</button>
            <button type="button" aria-pressed={playlistTab === 'audio'} class:active={playlistTab === 'audio'} on:click={() => playlistTab = 'audio'}>Audio</button>
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
          <input type="number" min={playlistFirstIndex} max={playlistLastIndex} step="1" inputmode="numeric" bind:value={rangeStart} aria-label="Range start" />
          <span class="dash">–</span>
          <input type="number" min={playlistFirstIndex} max={playlistLastIndex} step="1" inputmode="numeric" bind:value={rangeEnd} aria-label="Range end" />
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
        <button type="button" class="app-btn" on:click={pickFolder}>Change…</button>
        <button type="button" class="app-btn primary queue" on:click={enqueuePlaylist} disabled={!selectedItems.size || !folder}>
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
          <span>{preview.channel || 'YouTube'}{#if preview.mediaType === 'short'} · <em>Short</em>{/if}</span>
          <small>{preview.duration || 'Duration unavailable'}{preview.viewCount ? ` · ${formatViewCount(preview.viewCount)} views` : ''}</small>
          {#if linkedPlaylist?.playlistUrl}
            <button type="button" class="ghost review-playlist" on:click={reviewLinkedPlaylist}>Review the playlist instead</button>
          {/if}
        </div>
        <div class="policy">
          <h2>Choose Download</h2>
          <div class="segment" aria-label="Output type">
            <button type="button" aria-pressed={tab === 'video'} class:active={tab === 'video'} on:click={() => setTab('video')}>Video</button>
            <button type="button" aria-pressed={tab === 'audio'} class:active={tab === 'audio'} on:click={() => setTab('audio')}>Audio</button>
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
        <button type="button" class="app-btn" on:click={pickFolder}>Change…</button>
        <button type="button" class="app-btn primary queue" on:click={enqueueVideo} disabled={!selectedPlan}>Add to Queue</button>
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
      <p>VidStow reviews a public video, Short, or playlist, then shows the files you’ll get before anything downloads.</p>
    </section>
    {/if}
  {/if}
</section>

<style>
  .page.fill {
    height: 100%;
    min-height: 0;
    overflow: hidden;
    padding-bottom: 20px;
  }
  .input-mode {
    display: inline-flex;
    align-self: flex-start;
    padding: 3px;
    border: 1px solid var(--border-default);
    border-radius: var(--r-md);
    background: var(--surface-sunken);
  }
  .input-mode button,
  .segment button {
    min-height: 36px;
  }
  .input-mode button {
    padding: 0 var(--sp-4);
    border: 0;
    border-radius: calc(var(--r-md) - 2px);
    background: transparent;
    color: var(--text-secondary);
    font-weight: 650;
  }
  .input-mode button.active { background: var(--surface-base); color: var(--text-primary); box-shadow: var(--shadow-card); }

  .batch-composer,
  .batch-review {
    display: flex;
    min-height: 0;
    flex-direction: column;
    border: 1px solid var(--border-default);
    border-radius: var(--r-lg);
    background: var(--surface-raised);
    box-shadow: var(--shadow-card);
  }
  .batch-composer { padding: var(--sp-5); gap: var(--sp-3); }
  .batch-composer > label { font-weight: 700; }
  .batch-composer textarea {
    width: 100%;
    min-height: 210px;
    resize: vertical;
    padding: var(--sp-4);
    border: 1px solid var(--border-default);
    border-radius: var(--r-md);
    background: var(--surface-base);
    color: var(--text-primary);
    font: inherit;
    line-height: 1.6;
  }
  .batch-composer-footer,
  .batch-review-header,
  .batch-save-bar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--sp-4);
  }
  .batch-composer-footer p,
  .batch-review-header p { margin: 0; color: var(--text-muted); font-size: var(--fs-sm); }
  .batch-review { overflow: hidden; }
  .batch-review-header { padding: var(--sp-4); }
  .batch-review-header h2 { margin: 0 0 3px; font-size: var(--fs-lg); }
  .batch-review-header .batch-expired { margin-top: var(--sp-2); color: var(--status-danger); }
  .batch-lines { display: flex; min-height: 0; flex-direction: column; border-top: 1px solid var(--border-subtle); }
  .batch-line {
    display: grid;
    grid-template-columns: 34px 112px minmax(0, 1fr) minmax(150px, 230px);
    align-items: center;
    gap: var(--sp-3);
    padding: var(--sp-3) var(--sp-4);
    border-bottom: 1px solid var(--border-subtle);
    background: var(--surface-base);
  }
  .batch-line-number { color: var(--text-muted); font-variant-numeric: tabular-nums; text-align: center; }
  .batch-thumbnail {
    width: 112px;
    aspect-ratio: 16 / 9;
    position: relative;
    display: grid;
    place-items: center;
    overflow: hidden;
    border-radius: var(--r-sm);
    background: var(--surface-sunken);
    color: var(--text-muted);
  }
  .batch-thumbnail img { position: absolute; inset: 0; width: 100%; height: 100%; object-fit: cover; }
  .batch-thumbnail svg { width: 28px; height: 28px; fill: none; stroke: currentColor; stroke-width: 1.5; }
  .batch-thumbnail svg path { fill: currentColor; stroke: none; }
  .batch-line-copy,
  .batch-line-state { display: flex; min-width: 0; flex-direction: column; gap: 2px; }
  .batch-line-copy strong,
  .batch-line-copy span,
  .batch-line-state small { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .batch-line-copy span,
  .batch-line-copy small,
  .batch-line-state small { color: var(--text-muted); font-size: var(--fs-xs); }
  .batch-line-state { align-items: flex-end; text-align: right; }
  .batch-state { display: inline-flex; padding: 3px 8px; border-radius: var(--r-full); font-size: var(--fs-xs); font-weight: 700; }
  .batch-line[data-status='ready'] .batch-state { color: var(--status-success); background: var(--status-success-soft); }
  .batch-line[data-status='duplicate'] .batch-state { color: var(--status-warning); background: var(--status-warning-soft); }
  .batch-line[data-status='invalid'] .batch-state,
  .batch-line[data-status='analysis_failed'] .batch-state { color: var(--status-danger); background: var(--status-danger-soft); }
  .batch-policy {
    display: grid;
    grid-template-columns: minmax(180px, 1fr) auto minmax(170px, 220px);
    align-items: center;
    gap: var(--sp-4);
    padding: var(--sp-4);
    border-bottom: 1px solid var(--border-subtle);
    background: var(--surface-sunken);
  }
  .batch-policy > div:first-child { display: flex; flex-direction: column; }
  .batch-policy small { color: var(--text-muted); }
  .batch-policy select { height: 40px; }
  .batch-save-bar { padding: var(--sp-4); }
  .batch-save-bar .destination { flex: 1; min-width: 0; }

  .analyze-bar {
    display: grid;
    grid-template-columns: minmax(0, 1fr) 118px;
    gap: 10px;
    flex-shrink: 0;
  }
  .analyze-bar input { height: 40px; }
  .analyze-bar .app-btn { min-height: 40px; }
  .ghost {
    min-height: 32px;
    padding: 0 10px;
    border: 1px solid var(--border-default);
    border-radius: var(--r-md);
    background: var(--surface-base);
    color: var(--text-primary);
    font-size: var(--fs-sm);
    font-weight: 600;
  }
  .ghost:hover:not(:disabled) { background: var(--surface-hover); }

  .workspace {
    flex: 1;
    min-height: 0;
    margin-top: 0;
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
  .policy-note {
    grid-column: 1 / -1;
    margin: -4px 0 0;
    color: var(--text-muted);
    font-size: 11px;
    font-weight: 500;
  }
  .cap-note, .review-playlist {
    color: var(--accent-600);
    font-weight: 600;
  }
  .review-playlist { justify-self: start; margin-top: 4px; min-height: 28px; padding: 0 8px; }
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
    .batch-policy { grid-template-columns: 1fr auto; }
    .batch-policy select { grid-column: 1 / -1; width: 100%; }
    .identity { grid-template-columns: 72px 1fr; }
    .policy { grid-column: 1 / -1; }
    .toolbar input[type='search'] { margin-left: 0; width: 100%; flex-basis: 100%; }
  }
  @media (max-width: 720px) {
    .batch-composer-footer,
    .batch-review-header,
    .batch-save-bar { align-items: stretch; flex-direction: column; }
    .batch-composer-footer .app-btn,
    .batch-review-header .app-btn,
    .batch-save-bar .app-btn { width: 100%; }
    .batch-line { grid-template-columns: 28px 72px minmax(0, 1fr); }
    .batch-thumbnail { width: 72px; }
    .batch-line-state { grid-column: 3; align-items: flex-start; text-align: left; }
    .save-bar { grid-template-columns: 1fr auto; }
    .destination { grid-column: 1 / -1; }
    .queue { grid-column: 1 / -1; }
    .entry { grid-template-columns: 16px 28px minmax(0, 1fr) auto; }
    .mini { display: none; }
  }
</style>
