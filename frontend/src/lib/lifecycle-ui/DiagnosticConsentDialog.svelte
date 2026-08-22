<script lang="ts">
  import { trapModalFocus } from './modal.js';

  interface Props {
    open: boolean;
    busy?: boolean;
    onClose?: () => void;
    onEnable?: () => void;
    onDisable?: () => void;
    onPrivacy?: () => void;
  }

  let { open, busy = false, onClose, onEnable, onDisable, onPrivacy }: Props = $props();

  function onKeydown(event: KeyboardEvent): void {
    if (open && event.key === 'Escape') onClose?.();
  }
</script>

<svelte:window onkeydown={onKeydown} />

{#if open}
  <div class="overlay" role="presentation" onclick={(event) => event.target === event.currentTarget && onClose?.()}>
    <div class="dialog" role="dialog" aria-modal="true" aria-labelledby="diagnostic-consent-title" aria-describedby="diagnostic-consent-description" tabindex="-1" use:trapModalFocus>
      <header>
        <h2 id="diagnostic-consent-title">Help improve VidStow?</h2>
        <button type="button" class="close" aria-label="Close" onclick={() => onClose?.()}>×</button>
      </header>
      <p id="diagnostic-consent-description">
        Send a small, sanitized report only when VidStow cannot complete a requested download or encounters an app failure. Reports never include video IDs, links, paths, filenames, cookies, tokens, or error text.
      </p>
      <button type="button" class="privacy" onclick={() => onPrivacy?.()}>Read the diagnostics privacy notice ↗</button>
      <footer>
        <button type="button" class="app-btn" disabled={busy} onclick={() => onDisable?.()}>Don’t send</button>
        <button type="button" class="app-btn primary" disabled={busy} onclick={() => onEnable?.()}>Send diagnostics</button>
      </footer>
    </div>
  </div>
{/if}

<style>
  .overlay { position: fixed; inset: 0; z-index: 120; display: grid; place-items: center; padding: 20px; background: rgba(17,24,39,.34); }
  .dialog { width: min(500px, 94vw); padding: 22px; border: 1px solid var(--border-default); border-radius: var(--r-lg); background: var(--surface-base); box-shadow: var(--shadow-modal); }
  header { display: grid; grid-template-columns: 1fr auto; align-items: center; gap: 12px; }
  h2 { margin: 0; font-size: 18px; }
  .close { color: var(--text-muted); font-size: 20px; }
  p { margin: 16px 0 10px; color: var(--text-secondary); font-size: 13px; line-height: 1.6; }
  .privacy { padding: 0; color: var(--accent-primary); font-size: 12px; text-decoration: underline; text-underline-offset: 3px; }
  footer { display: flex; justify-content: flex-end; gap: 8px; margin-top: 22px; }
</style>
