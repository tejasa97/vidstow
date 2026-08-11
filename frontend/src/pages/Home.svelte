<script lang="ts">
  import { createEventDispatcher } from 'svelte';
  import { api } from '../lib/api.js';
  import { errorMessage, ffmpeg, modal, settings, showBanner } from '../lib/stores.js';
  import { formatBytes } from '../lib/format.js';
  import type { InfoSummary, OutputPlan } from '../lib/types.js';

  const dispatch = createEventDispatcher<{ goto: 'home' | 'queue' | 'downloads' | 'settings' | 'about' }>();
  let url = '';
  let busy = false;
  let preview: InfoSummary | null = null;
  let selectedPlanId = '';
  let tab: 'video' | 'audio' | 'info' = 'video';
  let folder = '';

  $: folder = $settings.downloadFolder || folder;
  $: plans = preview?.plans ?? [];
  $: visiblePlans = tab === 'info' ? [] : plans.filter((plan) => plan.kind === tab);
  $: selectedPlan = plans.find((plan) => plan.id === selectedPlanId) ?? null;

  async function analyze() {
    if (!url.trim()) return;
    busy = true;
    preview = null;
    selectedPlanId = '';
    try {
      const accepted = await api.validation.url(url.trim());
      url = accepted.url;
      preview = await api.analyse.url(accepted.url);
      const recommended = preview.plans.find((plan) => plan.recommended) ?? preview.plans[0];
      selectedPlanId = recommended?.id ?? '';
      tab = recommended?.kind ?? 'video';
    } catch (err) {
      modal.set({
        kind: 'error',
        title: 'Unsupported URL',
        message: errorMessage(err, 'VidStow could not extract information from this URL. Make sure it is a valid, publicly accessible YouTube video.'),
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

  async function enqueue() {
    if (!preview || !selectedPlan || !folder) return;
    if (selectedPlan.requiresFfmpeg && !$ffmpeg.available) {
      modal.set({
        kind: 'ffmpeg-missing', title: 'FFmpeg Required',
        message: 'This output needs FFmpeg for merging or conversion. Install FFmpeg, set its path in Settings, or choose an original audio option.',
        actions: [{ label: 'Open Settings', primary: true, action: () => dispatch('goto', 'settings') }],
      });
      return;
    }
    const start = async () => {
      try {
        await api.jobs.start({
          url: preview!.url, videoId: preview!.videoId, title: preview!.title,
          channel: preview!.channel, planId: selectedPlan!.id, outputDir: folder,
          duration: preview!.duration, thumbnail: preview!.thumbnail,
        });
        showBanner('success', 'Added to queue');
        dispatch('goto', 'queue');
      } catch (err) {
        modal.set({ kind: 'error', title: 'Download could not start', message: errorMessage(err, 'Could not start this download.') });
      }
    };
    if ($settings.confirmBeforeDownload) {
      modal.set({
        kind: 'error', title: 'Add this download?',
        message: `${selectedPlan.label} · ${selectedPlan.container}${selectedPlan.approxBytes ? ` · about ${formatBytes(selectedPlan.approxBytes)}` : ''}`,
        actions: [{ label: 'Add to Queue', primary: true, action: start }],
      });
      return;
    }
    await start();
  }

  function choose(plan: OutputPlan) {
    selectedPlanId = plan.id;
  }
</script>

<section class="page" aria-labelledby="home-title">
  <header class="page-header">
    <h1 id="home-title">Download from YouTube</h1>
    <p>Paste a public video URL to analyze it and choose your download.</p>
  </header>

  <form class="analyze-bar" on:submit|preventDefault={analyze}>
    <label class="visually-hidden" for="video-url">YouTube video URL</label>
    <input id="video-url" type="url" bind:value={url} placeholder="https://www.youtube.com/watch?v=…" autocomplete="off" />
    <button class="primary" type="submit" disabled={busy || !url.trim()}>{busy ? 'Analyzing…' : 'Analyze'}</button>
  </form>

  {#if preview}
    <section class="video-info" aria-labelledby="video-info-title">
      <h2 id="video-info-title">Video Info</h2>
      <div class="video-card">
        <div class="thumbnail">
          {#if preview.thumbnail}<img src={preview.thumbnail} alt="" referrerpolicy="no-referrer" />{/if}
          {#if preview.duration}<span>{preview.duration}</span>{/if}
        </div>
        <div class="video-copy">
          <strong title={preview.title}>{preview.title}</strong>
          <span>{preview.channel || 'YouTube'}</span>
          <small>{preview.duration || 'Duration unavailable'}{preview.viewCount ? ` · ${preview.viewCount.toLocaleString()} views` : ''}</small>
        </div>
      </div>
    </section>

    <section class="download-options" aria-labelledby="options-title">
      <h2 id="options-title">Choose Download</h2>
      <p>Select a complete output file. VidStow has already paired compatible video and audio streams.</p>
      <div class="tabs" role="tablist" aria-label="Output type">
        <button type="button" class:active={tab === 'video'} on:click={() => tab = 'video'}>Video</button>
        <button type="button" class:active={tab === 'audio'} on:click={() => tab = 'audio'}>Audio</button>
        <button type="button" class:active={tab === 'info'} on:click={() => tab = 'info'}>Info</button>
      </div>

      {#if tab === 'info'}
        <div class="info-grid">
          <div><span>Video ID</span><strong>{preview.videoId}</strong></div>
          <div><span>Uploaded</span><strong>{preview.uploadDate || 'Unavailable'}</strong></div>
          <div><span>Channel</span><strong>{preview.channel || 'Unavailable'}</strong></div>
          <div><span>Duration</span><strong>{preview.duration || 'Unavailable'}</strong></div>
          <div><span>Access</span><strong>{preview.access?.label || 'Access status not reported'}</strong></div>
        </div>
      {:else if visiblePlans.length}
        <div class="plan-table" role="radiogroup" aria-label={`${tab} output options`}>
          <div class="plan-head"><span></span><span>Quality</span><span>Format</span><span>Video</span><span>Audio</span><span>Size</span></div>
          {#each visiblePlans as plan (plan.id)}
            <button type="button" class="plan-row" class:selected={selectedPlanId === plan.id} role="radio" aria-checked={selectedPlanId === plan.id} on:click={() => choose(plan)}>
              <span class="radio"></span>
              <span><strong>{plan.label}</strong>{#if plan.recommended}<small>Recommended</small>{/if}</span>
              <span>{plan.container}</span>
              <span>{plan.videoCodec || '—'}</span>
              <span>{plan.audioCodec || '—'}</span>
              <span>{plan.approxBytes ? `${plan.sizeIsApproximate ? '~' : ''}${formatBytes(plan.approxBytes)}` : '—'}</span>
            </button>
          {/each}
        </div>
      {:else}
        <div class="empty">No {tab} outputs were reported for this video.</div>
      {/if}
    </section>

    <footer class="save-bar">
      <div class="destination"><span>Save to:</span><strong title={folder}>{folder}</strong></div>
      <button type="button" class="secondary" on:click={pickFolder}>Change…</button>
      <button type="button" class="primary queue" on:click={enqueue} disabled={!selectedPlan}>Add to Queue</button>
    </footer>
  {:else}
    <section class="welcome">
      <div class="download-mark" aria-hidden="true">↓</div>
      <h2>Ready when you are</h2>
      <p>Paste a supported YouTube link above. VidStow will show the exact video and audio files available before anything downloads.</p>
    </section>
  {/if}
</section>

<style>
  .page{width:min(100%,960px);margin:0 auto;padding:34px 42px 48px;color:var(--text-primary)}
  .page-header h1{margin:0;font-size:26px;letter-spacing:-.025em}.page-header p{margin:7px 0 20px;color:var(--text-secondary)}
  .analyze-bar{display:grid;grid-template-columns:minmax(0,1fr) 112px;gap:10px}.analyze-bar input{height:42px}.primary,.secondary{min-height:38px;padding:0 16px;border-radius:7px;font-weight:600}.primary{color:#fff;background:var(--accent-600,#2563eb)}.primary:disabled{opacity:.45}.secondary{border:1px solid var(--border-default);background:var(--surface-raised);color:var(--text-primary)}
  h2{margin:0 0 10px;font-size:14px}.video-info,.download-options{margin-top:24px}.video-card{display:grid;grid-template-columns:150px minmax(0,1fr);gap:16px;padding:12px;border:1px solid var(--border-default);border-radius:9px;background:var(--surface-raised)}
  .thumbnail{position:relative;aspect-ratio:16/9;overflow:hidden;border-radius:6px;background:var(--surface-sunken)}.thumbnail img{width:100%;height:100%;object-fit:cover}.thumbnail span{position:absolute;right:5px;bottom:5px;padding:2px 5px;border-radius:3px;background:#111d;color:#fff;font-size:11px}
  .video-copy{display:flex;min-width:0;flex-direction:column;justify-content:center;gap:6px}.video-copy strong{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.video-copy span{font-size:13px;color:var(--text-secondary)}.video-copy small{color:var(--text-muted)}
  .download-options>p{margin:-4px 0 12px;color:var(--text-muted);font-size:12px}.tabs{display:flex;border-bottom:1px solid var(--border-default)}.tabs button{min-width:92px;padding:10px 14px;color:var(--text-secondary);border-bottom:2px solid transparent}.tabs button.active{color:var(--accent-600);border-color:var(--accent-600)}
  .plan-table{border:1px solid var(--border-default);border-top:0;border-radius:0 0 8px 8px;overflow:hidden}.plan-head,.plan-row{display:grid;grid-template-columns:28px 1.2fr .8fr .9fr .8fr .8fr;gap:10px;align-items:center;text-align:left}.plan-head{padding:9px 12px;color:var(--text-muted);background:var(--surface-subtle);font-size:11px}.plan-row{width:100%;min-height:48px;padding:7px 12px;border-top:1px solid var(--border-subtle);color:var(--text-secondary)}.plan-row:hover,.plan-row.selected{background:var(--accent-soft,rgba(37,99,235,.07))}.plan-row strong,.plan-row small{display:block}.plan-row strong{color:var(--text-primary)}.plan-row small{margin-top:2px;color:var(--accent-600);font-size:10px}.radio{width:14px;height:14px;border:1px solid var(--border-strong);border-radius:50%}.selected .radio{border:4px solid var(--accent-600)}
  .info-grid{display:grid;grid-template-columns:1fr 1fr;border:1px solid var(--border-default);border-top:0}.info-grid div{padding:14px;border-top:1px solid var(--border-subtle)}.info-grid div:nth-child(even){border-left:1px solid var(--border-subtle)}.info-grid span,.info-grid strong{display:block}.info-grid span{color:var(--text-muted);font-size:11px}.info-grid strong{margin-top:4px}.empty{padding:36px;text-align:center;border:1px solid var(--border-default);border-top:0;color:var(--text-muted)}
  .save-bar{display:grid;grid-template-columns:minmax(0,1fr) auto auto;gap:10px;align-items:center;margin-top:22px;padding-top:16px;border-top:1px solid var(--border-default)}.destination{display:flex;min-width:0;gap:8px}.destination span{color:var(--text-secondary)}.destination strong{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.queue{min-width:128px}
  .welcome{min-height:360px;display:grid;place-content:center;text-align:center;color:var(--text-secondary)}.welcome h2{margin:18px 0 6px;font-size:20px;color:var(--text-primary)}.welcome p{max-width:500px;margin:0;line-height:1.6}.download-mark{width:58px;height:58px;display:grid;place-items:center;margin:auto;border-radius:15px;background:var(--accent-soft,rgba(37,99,235,.1));color:var(--accent-600);font-size:30px}
  @media(max-width:720px){.page{padding:24px 18px}.video-card{grid-template-columns:120px 1fr}.plan-head,.plan-row{grid-template-columns:24px 1fr .7fr .8fr}.plan-head span:nth-child(4),.plan-head span:nth-child(5),.plan-row span:nth-child(4),.plan-row span:nth-child(5){display:none}.save-bar{grid-template-columns:1fr auto}.destination{grid-column:1/-1}}
</style>
