<script lang="ts">
  import { createEventDispatcher } from 'svelte';
  import { DEFAULT_RECOVERY_REQUIRED, type RecoveryRequiredViewModel } from './types.js';

  export interface RecoveryRequiredEvents {
    'copy-diagnostics': void;
    'open-data-folder': void;
  }

  interface Props {
    model?: RecoveryRequiredViewModel;
    title?: string;
    subtitle?: string;
  }

  let {
    model = DEFAULT_RECOVERY_REQUIRED,
    title = 'Queue',
    subtitle = 'Saved queue unavailable',
  }: Props = $props();
  const dispatch = createEventDispatcher<RecoveryRequiredEvents>();
</script>

<section class="recovery-shell" aria-labelledby="lifecycle-recovery-title">
  <header class="page-header">
    <h1>{title}</h1>
    <p>{subtitle}</p>
  </header>

  <section class="recovery-card" aria-label="Recovery required">
    <div class="warning-icon" aria-hidden="true">
      <svg viewBox="0 0 48 48" fill="none" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round" stroke-width="2.4">
        <path d="m21.2 7.5-17 29.3a3.4 3.4 0 0 0 3 5.1h33.6a3.4 3.4 0 0 0 3-5.1l-17-29.3a3.4 3.4 0 0 0-5.6 0Z" />
        <path d="M24 17v10" />
        <path d="M24 34h.02" stroke-width="3.8" />
      </svg>
    </div>
    <h2 id="lifecycle-recovery-title">Download state needs recovery</h2>
    <p class="intro">VidStow could not safely read its saved queue. Your media and recovery files were preserved.</p>
    <p class="intro">Downloads are paused until the saved state is reviewed. VidStow will not resume, retry, cancel, or clean up saved work.</p>

    <dl class="status-list">
      <div><dt>State file</dt><dd>{model.stateFileStatus ?? DEFAULT_RECOVERY_REQUIRED.stateFileStatus}</dd></div>
      <div><dt>Automatic cleanup</dt><dd>{model.automaticCleanupStatus ?? DEFAULT_RECOVERY_REQUIRED.automaticCleanupStatus}</dd></div>
      <div><dt>Saved media</dt><dd>{model.savedMediaStatus ?? DEFAULT_RECOVERY_REQUIRED.savedMediaStatus}</dd></div>
    </dl>

    <div class="actions">
      <button type="button" class="outline-button" onclick={() => dispatch('copy-diagnostics')}>Copy diagnostics</button>
      <button type="button" class="outline-button" onclick={() => dispatch('open-data-folder')}>Open data folder</button>
    </div>

    <p class="footer-message">
      <span class="small-warning" aria-hidden="true">!</span>
      {model.footerMessage ?? DEFAULT_RECOVERY_REQUIRED.footerMessage}
    </p>
  </section>
</section>

<style>
  .recovery-shell { width: min(100%, 900px); margin: 0 auto; padding: var(--sp-8) var(--sp-9) var(--sp-9); }
  .page-header h1 { margin: 0; font-size: var(--fs-3xl); letter-spacing: -0.03em; }
  .page-header p { margin: var(--sp-1) 0 0; color: var(--text-muted); font-size: var(--fs-md); }
  .recovery-card { display: flex; flex-direction: column; align-items: center; margin: var(--sp-7) auto 0; padding: var(--sp-7) var(--sp-8); border: 1px solid var(--border-default); border-radius: var(--r-md); background: var(--surface-base); box-shadow: var(--shadow-card); text-align: center; }
  .warning-icon { width: 68px; height: 68px; color: var(--status-warning); }
  .warning-icon svg { width: 100%; height: 100%; }
  h2 { margin: var(--sp-4) 0 0; font-size: var(--fs-2xl); letter-spacing: -0.025em; }
  .intro { max-width: 62ch; margin: var(--sp-3) 0 0; color: var(--text-secondary); font-size: var(--fs-lg); line-height: 1.55; }
  .status-list { width: min(100%, 560px); margin: var(--sp-6) 0 0; border: 1px solid var(--border-subtle); border-radius: var(--r-md); text-align: left; }
  .status-list > div { display: flex; align-items: center; justify-content: space-between; gap: var(--sp-4); padding: var(--sp-3) var(--sp-4); }
  .status-list > div + div { border-top: 1px solid var(--border-subtle); }
  dt { color: var(--text-primary); font-size: var(--fs-md); }
  dd { margin: 0; color: var(--text-muted); font-size: var(--fs-md); text-align: right; }
  .actions { display: flex; gap: var(--sp-3); width: min(100%, 560px); margin-top: var(--sp-5); }
  .outline-button { flex: 1; min-height: 42px; border: 1px solid var(--accent-500); border-radius: var(--r-md); color: var(--accent-600); background: var(--surface-base); font-size: var(--fs-md); font-weight: 600; }
  .outline-button:hover { background: var(--accent-soft); }
  .footer-message { display: flex; align-items: center; gap: var(--sp-3); margin: var(--sp-5) 0 0; color: var(--text-secondary); font-size: var(--fs-md); }
  .small-warning { display: inline-grid; place-items: center; width: 20px; height: 20px; border: 1.5px solid var(--status-warning); border-radius: 5px; color: var(--status-warning); font-size: var(--fs-sm); font-weight: 700; }

  @media (max-width: 700px) {
    .recovery-shell { padding: var(--sp-6) var(--sp-4) var(--sp-7); }
    .recovery-card { padding: var(--sp-6) var(--sp-4); }
    .intro { font-size: var(--fs-md); }
    .actions { flex-direction: column; width: 100%; }
    .outline-button { width: 100%; }
    .status-list { width: 100%; }
    .status-list > div { align-items: flex-start; flex-direction: column; gap: var(--sp-1); }
    dd { text-align: left; }
  }
</style>
