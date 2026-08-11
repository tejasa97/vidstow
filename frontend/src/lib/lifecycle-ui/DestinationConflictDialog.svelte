<script lang="ts">
  import { createEventDispatcher } from 'svelte';
  import { trapModalFocus } from './modal.js';
  import {
    isValidConflictToken,
    type DestinationConflictEventDetail,
    type DestinationConflictViewModel,
  } from './types.js';

  export interface DestinationConflictEvents {
    close: void;
    'cancel-download': DestinationConflictEventDetail;
    'use-new-name': DestinationConflictEventDetail;
  }

  interface Props {
    open: boolean;
    conflict: DestinationConflictViewModel;
  }

  let { open, conflict }: Props = $props();
  const dispatch = createEventDispatcher<DestinationConflictEvents>();
  const proposedNameAvailable = $derived(conflict.proposedNameAvailable === true);
  const hasConflictAuthority = $derived(isValidConflictToken(conflict.conflictToken));

  function onKeydown(event: KeyboardEvent): void {
    if (open && event.key === 'Escape') dispatch('close');
  }

  function onOverlayClick(event: MouseEvent): void {
    if (event.target === event.currentTarget) dispatch('close');
  }

  function useNewName(): void {
    if (proposedNameAvailable && hasConflictAuthority) {
      dispatch('use-new-name', { conflictToken: conflict.conflictToken });
    }
  }

  function cancelDownload(): void {
    if (hasConflictAuthority) {
      dispatch('cancel-download', { conflictToken: conflict.conflictToken });
    }
  }
</script>

<svelte:window onkeydown={onKeydown} />

{#if open}
  <div class="overlay" role="presentation" onclick={onOverlayClick}>
    <div class="dialog" role="dialog" aria-modal="true" aria-labelledby="lifecycle-conflict-title" tabindex="-1" use:trapModalFocus>
      <header class="dialog-header">
        <div>
          <h2 id="lifecycle-conflict-title">Choose a new filename</h2>
          <p>The reserved filename is no longer available. VidStow will not replace the existing file.</p>
        </div>
        <button type="button" class="close-button" aria-label="Close" onclick={() => dispatch('close')}>×</button>
      </header>

      <div class="dialog-body">
        <div class="field">
          <label for="lifecycle-unavailable-name">Unavailable name</label>
          <input id="lifecycle-unavailable-name" value={conflict.unavailableName} readonly aria-readonly="true" />
        </div>

        <div class="field">
          <div class="field-label">
            <label for="lifecycle-proposed-name">New reserved name</label>
            {#if proposedNameAvailable}<span class="available"><span aria-hidden="true">✓</span> Available</span>{/if}
          </div>
          <input id="lifecycle-proposed-name" value={conflict.proposedName} readonly aria-readonly="true" />
        </div>

        <p class="unchanged-copy">The existing file will remain unchanged.</p>
      </div>

      <footer class="dialog-footer">
        <button type="button" class="secondary-button" disabled={!hasConflictAuthority} onclick={cancelDownload}>Cancel download</button>
        <button type="button" class="primary-button" data-autofocus disabled={!proposedNameAvailable || !hasConflictAuthority} onclick={useNewName}>Use new name</button>
      </footer>
    </div>
  </div>
{/if}

<style>
  .overlay { position: fixed; inset: 0; z-index: 100; display: grid; place-items: center; padding: var(--sp-5); background: rgba(24, 25, 28, 0.58); backdrop-filter: blur(2px); }
  .dialog { width: min(580px, 94vw); overflow: hidden; border: 1px solid var(--border-default); border-radius: var(--r-lg); background: var(--surface-base); box-shadow: var(--shadow-modal); }
  .dialog-header { display: flex; align-items: flex-start; justify-content: space-between; gap: var(--sp-4); padding: var(--sp-5) var(--sp-6) var(--sp-3); }
  h2 { margin: 0; font-size: var(--fs-xl); font-weight: 700; letter-spacing: -0.02em; }
  .dialog-header p { max-width: 56ch; margin: var(--sp-1) 0 0; color: var(--text-secondary); font-size: var(--fs-sm); }
  .close-button { width: 32px; height: 32px; flex: 0 0 auto; border-radius: var(--r-sm); color: var(--text-secondary); font-size: var(--fs-2xl); line-height: 1; }
  .close-button:hover { background: var(--surface-hover); }
  .dialog-body { display: flex; flex-direction: column; gap: var(--sp-4); padding: var(--sp-3) var(--sp-6) var(--sp-4); }
  .field { display: flex; flex-direction: column; gap: var(--sp-2); }
  .field-label { display: flex; align-items: center; justify-content: space-between; gap: var(--sp-3); }
  label { color: var(--text-secondary); font-size: var(--fs-sm); font-weight: 600; }
  input { height: 42px; background: var(--surface-sunken); color: var(--text-secondary); }
  .available { display: inline-flex; align-items: center; gap: 5px; color: var(--status-success); font-size: var(--fs-sm); font-weight: 600; }
  .available span { display: inline-grid; place-items: center; width: 18px; height: 18px; border: 1.5px solid currentColor; border-radius: 50%; }
  .unchanged-copy { margin: 0; color: var(--text-secondary); font-size: var(--fs-sm); }
  .dialog-footer { display: flex; justify-content: flex-end; gap: var(--sp-2); padding: var(--sp-3) var(--sp-6) var(--sp-5); }
  .secondary-button, .primary-button { min-height: 36px; padding: 0 var(--sp-4); border-radius: var(--r-md); font-size: var(--fs-sm); font-weight: 600; white-space: nowrap; }
  .secondary-button { border: 1px solid var(--accent-500); background: var(--surface-base); color: var(--accent-600); }
  .secondary-button:hover { background: var(--accent-soft); }
  .primary-button { border: 1px solid var(--accent-500); background: var(--accent-500); color: var(--text-on-accent); }
  .primary-button:hover:not(:disabled) { background: var(--accent-600); border-color: var(--accent-600); }
  .primary-button:disabled { opacity: 0.5; }

  @media (max-width: 560px) {
    .dialog-header, .dialog-body { padding-left: var(--sp-4); padding-right: var(--sp-4); }
    .dialog-footer { flex-direction: column-reverse; padding-left: var(--sp-4); padding-right: var(--sp-4); }
    .secondary-button, .primary-button { width: 100%; }
  }
</style>
