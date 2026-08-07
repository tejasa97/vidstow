<script lang="ts">
  import { jobs, showBanner, showError } from '../lib/stores.js';
  import { api } from '../lib/api.js';
  import ProgressRow from '../lib/components/ProgressRow.svelte';

  $: active = $jobs.filter((job) => ['active', 'pending', 'paused'].includes(job.status));
  $: completed = $jobs.filter((job) => ['complete', 'failed', 'canceled'].includes(job.status));
  $: running = $jobs.filter((job) => job.status === 'active').length;
  $: pausable = $jobs.some((job) => job.status === 'pending' || (job.status === 'active' && job.canPause && !job.processing));

  async function pauseAll() {
    try { const count = await api.jobs.pauseAll(); showBanner('info', count ? `${count} job${count === 1 ? '' : 's'} paused` : 'No jobs can be paused right now'); }
    catch (err) { showError(err, 'Could not pause the queue'); }
  }
  async function clearCompleted() { try { await api.jobs.clearCompleted(); } catch (err) { showError(err); } }
</script>

<section class="page" aria-labelledby="queue-title">
  <header class="page-header">
    <div><h1 id="queue-title">Queue</h1><p>{active.length} job{active.length === 1 ? '' : 's'} · {running} running</p></div>
    <div class="header-actions"><button type="button" on:click={pauseAll} disabled={!pausable}>Pause All</button><button type="button" on:click={clearCompleted} disabled={!completed.length}>Clear Completed</button></div>
  </header>

  <section class="section" aria-labelledby="active-title">
    <h2 id="active-title">Active &amp; queued <span>{active.length}</span></h2>
    {#if active.length}<div class="job-list">{#each active as job, index (job.id)}<ProgressRow {job} index={index + 1} />{/each}</div>
    {:else}<div class="empty">No active jobs. Add a video from Home to get started.</div>{/if}
  </section>

  {#if completed.length}
    <section class="section" aria-labelledby="completed-title"><h2 id="completed-title">Completed <span>{completed.length}</span></h2><div class="job-list">{#each completed as job, index (job.id)}<ProgressRow {job} index={active.length + index + 1} />{/each}</div></section>
  {/if}
  <footer>Jobs are saved automatically.</footer>
</section>

<style>
  .page{width:min(100%,900px);margin:0 auto;padding:34px 42px 48px}.page-header{display:flex;align-items:flex-start;justify-content:space-between;gap:24px}.page-header h1{margin:0;font-size:26px}.page-header p{margin:5px 0 0;color:var(--text-muted);font-size:12px}.header-actions{display:flex;gap:8px}.header-actions button{min-height:34px;padding:0 12px;border:1px solid var(--border-default);border-radius:6px;background:#fff;font-size:11px}
  .section{margin-top:24px}.section h2{display:flex;align-items:center;gap:7px;margin:0 0 9px;font-size:12px}.section h2 span{min-width:18px;height:18px;display:grid;place-items:center;border-radius:99px;background:var(--surface-active);color:var(--text-secondary);font-size:10px}.job-list{display:flex;flex-direction:column;gap:8px}.empty{min-height:150px;display:grid;place-items:center;border:1px dashed var(--border-default);border-radius:7px;color:var(--text-muted);font-size:12px}footer{margin-top:22px;text-align:center;color:var(--text-muted);font-size:10px}
  @media(max-width:650px){.page{padding:24px 18px}.page-header{flex-direction:column}.header-actions{width:100%}.header-actions button{flex:1}}
</style>
