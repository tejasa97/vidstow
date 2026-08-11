<script lang="ts">
  import { createEventDispatcher } from 'svelte';
  import { trapModalFocus } from './modal.js';
  import type { QuitConfirmationViewModel } from './types.js';

  export interface QuitConfirmationEvents {
    close: void;
    'keep-working': void;
    'pause-and-quit': void;
  }

  interface Props {
    open: boolean;
    model: QuitConfirmationViewModel;
    onClose?: () => void;
    onKeepWorking?: () => void;
    onPauseAndQuit?: () => void;
  }

  let { open, model, onClose, onKeepWorking, onPauseAndQuit }: Props = $props();
  const dispatch = createEventDispatcher<QuitConfirmationEvents>();

  function close(): void {
    dispatch('close');
    onClose?.();
  }

  function keepWorking(): void {
    dispatch('keep-working');
    onKeepWorking?.();
  }

  function pauseAndQuit(): void {
    dispatch('pause-and-quit');
    onPauseAndQuit?.();
  }

  const activeDownloadsLabel = $derived(
    `${model.activeDownloads} active download${model.activeDownloads === 1 ? '' : 's'}`,
  );
  const safeDownloadsLabel = $derived(
    `${model.waitingOrPausedDownloads} · Already safe`,
  );

  function onKeydown(event: KeyboardEvent): void {
    if (open && event.key === 'Escape') close();
  }

  function onOverlayClick(event: MouseEvent): void {
    if (event.target === event.currentTarget) dispatch('close');
  }
</script>

<svelte:window onkeydown={onKeydown} />

{#if open}
  <div class="overlay" role="presentation" onclick={onOverlayClick}>
    <div
      class="dialog"
      role="dialog"
      aria-modal="true"
      aria-labelledby="lifecycle-quit-title"
      aria-describedby="lifecycle-quit-description"
      tabindex="-1"
      use:trapModalFocus
    >
      <header class="dialog-header">
        <h2 id="lifecycle-quit-title">Quit VidStow?</h2>
    <button type="button" class="close-button" aria-label="Close" onclick={close}>×</button>
      </header>

      <div class="dialog-body">
        <p id="lifecycle-quit-description" class="lead">
          {activeDownloadsLabel} will be paused before VidStow quits.
        </p>

        <dl class="summary-list">
          <div>
            <dt>Active downloads</dt>
            <dd>{model.activeDownloads} · Will be paused</dd>
          </div>
          <div>
            <dt>Waiting or paused</dt>
            <dd>{safeDownloadsLabel}</dd>
          </div>
        </dl>

        <p class="preservation-copy">Saved progress will be restored as paused the next time VidStow opens.</p>
      </div>

      <footer class="dialog-footer">
        <button type="button" class="secondary-button" data-autofocus onclick={keepWorking}>Keep working</button>
        <button type="button" class="primary-button" onclick={pauseAndQuit}>Pause downloads and quit</button>
      </footer>
    </div>
  </div>
{/if}

<style>
  .overlay { position: fixed; inset: 0; z-index: 100; display: grid; place-items: center; padding: var(--sp-5); background: rgba(24, 25, 28, 0.45); backdrop-filter: blur(2px); }
  .dialog { width: min(548px, 94vw); overflow: hidden; border: 1px solid var(--border-default); border-radius: var(--r-lg); background: var(--surface-base); box-shadow: var(--shadow-modal); }
  .dialog-header { display: flex; align-items: center; justify-content: space-between; gap: var(--sp-3); padding: var(--sp-5) var(--sp-6) var(--sp-3); }
  h2 { margin: 0; font-size: var(--fs-xl); font-weight: 700; letter-spacing: -0.02em; }
  .close-button { width: 32px; height: 32px; border-radius: var(--r-sm); color: var(--text-secondary); font-size: var(--fs-2xl); line-height: 1; }
  .close-button:hover { background: var(--surface-hover); }
  .dialog-body { padding: 0 var(--sp-6) var(--sp-4); }
  .lead, .preservation-copy { margin: 0; color: var(--text-secondary); font-size: var(--fs-sm); }
  .summary-list { margin: var(--sp-4) 0; border: 1px solid var(--border-subtle); border-radius: var(--r-md); }
  .summary-list > div { display: flex; align-items: center; justify-content: space-between; gap: var(--sp-4); padding: var(--sp-3); }
  .summary-list > div + div { border-top: 1px solid var(--border-subtle); }
  dt { color: var(--text-primary); font-size: var(--fs-sm); }
  dd { margin: 0; color: var(--text-muted); font-size: var(--fs-sm); text-align: right; }
  .dialog-footer { display: flex; justify-content: flex-end; gap: var(--sp-2); padding: var(--sp-3) var(--sp-6) var(--sp-5); }
  .secondary-button, .primary-button { min-height: 36px; padding: 0 var(--sp-4); border-radius: var(--r-md); font-size: var(--fs-sm); font-weight: 600; white-space: nowrap; }
  .secondary-button { border: 1px solid var(--border-default); background: var(--surface-base); color: var(--text-primary); }
  .secondary-button:hover { background: var(--surface-hover); }
  .primary-button { border: 1px solid var(--accent-500); background: var(--accent-500); color: var(--text-on-accent); }
  .primary-button:hover { background: var(--accent-600); border-color: var(--accent-600); }

  @media (max-width: 560px) {
    .dialog-header, .dialog-body { padding-left: var(--sp-4); padding-right: var(--sp-4); }
    .dialog-footer { flex-direction: column-reverse; padding-left: var(--sp-4); padding-right: var(--sp-4); }
    .secondary-button, .primary-button { width: 100%; }
  }
</style>
