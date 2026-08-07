<script lang="ts">
  import type { JobSnapshot } from '../types.js';
  import { formatEta, formatProgress, formatSpeed, progressOf } from '../format.js';
  import { api } from '../api.js';
  import { showBanner, showError } from '../stores.js';
  import StatusBadge from './StatusBadge.svelte';

  export let job: JobSnapshot;
  export let index = 1;
  $: progress = progressOf(job);
  $: progressLabel = job.status === 'complete' ? '100%' : job.total > 0 ? formatProgress(progress) : '';

  async function act(action: () => Promise<unknown>, fallback: string, success = '') {
    try { await action(); if (success) showBanner('info', success); }
    catch (err) { showError(err, fallback); }
  }
</script>

<article class="job-card" aria-label={job.title || `Queue item ${index}`}>
  <div class="thumb">{#if job.thumbnail}<img src={job.thumbnail} alt="" referrerpolicy="no-referrer" />{/if}</div>
  <div class="body">
    <div class="topline">
      <div class="title"><strong title={job.title || job.filename}>{job.title || job.filename || 'Untitled video'}</strong><span>{job.qualityLabel || job.quality}{job.container ? ` · ${job.container}` : ''}</span></div>
      <StatusBadge status={job.status} compact />
      <div class="actions">
        {#if job.status === 'active'}
          {#if job.canPause && !job.processing}<button type="button" aria-label="Pause download" on:click={() => act(() => api.jobs.pause(job.id), 'Could not pause the download')}>Ⅱ</button>{/if}
          <button type="button" aria-label="Cancel download" on:click={() => act(() => api.jobs.cancel(job.id), 'Could not cancel the download')}>×</button>
        {:else if job.status === 'pending'}
          <button type="button" aria-label="Pause download" on:click={() => act(() => api.jobs.pause(job.id), 'Could not pause the download')}>Ⅱ</button>
          <button type="button" aria-label="Cancel download" on:click={() => act(() => api.jobs.cancel(job.id), 'Could not cancel the download')}>×</button>
        {:else if job.status === 'paused'}
          <button class="label" type="button" aria-label="Resume download" on:click={() => act(() => api.jobs.resume(job.id), 'Could not resume the download', 'Download resumed')}>Resume</button>
          <button type="button" aria-label="Cancel download" on:click={() => act(() => api.jobs.cancel(job.id), 'Could not cancel the download')}>×</button>
        {:else if job.status === 'complete'}
          <button class="label" type="button" aria-label="Open downloaded file" on:click={() => act(() => api.fs.open(job.absolutePath), 'Could not open the file')}>Open</button>
          <button type="button" aria-label="Remove completed download" on:click={() => act(() => api.jobs.remove(job.id), 'Could not remove the download')}>×</button>
        {:else}
          <button class="label" type="button" aria-label="Retry download" on:click={() => act(() => api.jobs.retry(job.id), 'Could not retry the download', 'Retry added')}>Retry</button>
          <button type="button" aria-label="Remove download" on:click={() => act(() => api.jobs.remove(job.id), 'Could not remove the download')}>×</button>
        {/if}
      </div>
    </div>
    {#if job.status === 'active' || job.status === 'paused' || job.status === 'pending'}
      <div class="progress-line"><div class="track"><span style={`width:${Math.round(progress * 100)}%`}></span></div><small>{progressLabel || job.message || 'Queued'}{job.status === 'active' && job.speedBps > 0 ? ` · ${formatSpeed(job.speedBps)} · ETA ${formatEta(job.etaSeconds)}` : ''}</small></div>
    {:else if job.status === 'failed'}
      <p class="error">{job.message || 'Download failed'}</p>
    {:else if job.status === 'complete'}
      <p class="complete">Completed{job.filename ? ` · ${job.filename}` : ''}</p>
    {/if}
  </div>
</article>

<style>
  .job-card{display:grid;grid-template-columns:72px minmax(0,1fr);gap:12px;min-height:76px;padding:10px;border:1px solid var(--border-default);border-radius:7px;background:var(--surface-raised)}
  .thumb{width:72px;aspect-ratio:16/10;align-self:center;overflow:hidden;border-radius:5px;background:var(--surface-sunken)}.thumb img{width:100%;height:100%;object-fit:cover}
  .body{min-width:0;align-self:center}.topline{display:grid;grid-template-columns:minmax(0,1fr) auto auto;gap:12px;align-items:center}.title{min-width:0}.title strong,.title span{display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.title strong{font-size:12.5px}.title span{margin-top:3px;color:var(--text-muted);font-size:10.5px}
  .actions{display:flex;gap:5px}.actions button{min-width:28px;height:28px;border:1px solid var(--border-default);border-radius:5px;background:#fff;color:var(--text-secondary)}.actions button:hover{background:var(--surface-hover)}.actions .label{min-width:48px;padding:0 8px;color:var(--text-primary);font-size:11px}
  .progress-line{display:grid;grid-template-columns:minmax(120px,1fr) auto;gap:10px;align-items:center;margin-top:9px}.track{height:4px;overflow:hidden;border-radius:99px;background:var(--surface-active)}.track span{display:block;height:100%;border-radius:inherit;background:var(--accent-500)}.progress-line small{color:var(--text-muted);font-size:10px;white-space:nowrap}.error,.complete{margin:7px 0 0;font-size:10.5px}.error{color:var(--status-danger)}.complete{color:var(--status-success)}
  @media(max-width:650px){.job-card{grid-template-columns:60px 1fr}.thumb{width:60px}.topline{grid-template-columns:1fr auto}.topline :global(.badge){display:none}.progress-line{grid-template-columns:1fr}.progress-line small{white-space:normal}}
</style>
