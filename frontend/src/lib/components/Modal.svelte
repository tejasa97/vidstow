<script lang="ts">
  import { modal } from '../stores.js';
  function close() { modal.set(null); }
  $: current = $modal;
</script>

<svelte:window on:keydown={(event) => event.key === 'Escape' && close()} />

{#if current}
  <div class="overlay" role="presentation" on:click|self={close}>
    <div class:ffmpeg={current.kind === 'ffmpeg-missing'} class:confirm={current.kind === 'confirm-remove-history' || current.kind === 'confirm-delete-file'} class="dialog" role="dialog" aria-modal="true" aria-labelledby="modal-title">
      <header><div class="small-icon" aria-hidden="true">{current.kind === 'ffmpeg-missing' || current.kind.startsWith('confirm-') ? '△' : '×'}</div><h2 id="modal-title">{current.title}</h2><button class="close" type="button" on:click={close} aria-label="Close">×</button></header>
      <p class="lead">{current.message}</p>
      {#if current.kind === 'ffmpeg-missing'}<p class="fine-print">VidStow will continue to offer outputs that do not need FFmpeg.</p>{/if}
      {#if current.detail}<pre>{current.detail}</pre>{/if}
      <footer><button type="button" on:click={close}>Close</button>{#each current.actions || [] as action}<button class:primary={action.primary} type="button" on:click={() => { action.action(); close(); }}>{action.label}</button>{/each}</footer>
    </div>
  </div>
{/if}

<style>
  .overlay{position:fixed;inset:0;display:grid;place-items:center;background:rgba(17,24,39,.32);z-index:100;padding:20px}
  .dialog{width:min(440px,94vw);padding:20px;background:#fff;border:1px solid var(--border-default);border-radius:8px;box-shadow:var(--shadow-modal)}
  h2{margin:0;font-size:17px;letter-spacing:-.01em}.lead{margin:14px 0;color:var(--text-secondary);font-size:13px;line-height:1.55}.fine-print{margin:10px 0;color:var(--text-muted);font-size:11px}
  footer{display:flex;justify-content:flex-end;gap:8px;margin-top:20px}footer button{min-height:34px;padding:0 13px;color:var(--text-primary);background:#fff;border:1px solid var(--border-default);border-radius:6px;font-size:12px}footer button.primary{color:#fff;background:#1f2937;border-color:#1f2937}
  header{display:grid;grid-template-columns:auto 1fr auto;align-items:center;gap:12px}.small-icon{width:30px;height:30px;display:grid;place-items:center;color:var(--status-danger);border:1.5px solid currentColor;border-radius:50%;font-weight:700}.ffmpeg .small-icon,.confirm .small-icon{color:var(--status-warning)}.close{color:var(--text-muted);font-size:20px}.dialog pre{max-height:160px;overflow:auto;white-space:pre-wrap;padding:12px;background:var(--surface-sunken);border-radius:6px;color:var(--text-secondary)}
</style>
