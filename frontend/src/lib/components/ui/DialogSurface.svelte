<script lang="ts">
  import type { Snippet } from 'svelte';
  import Button from './Button.svelte';

  interface Props {
    open: boolean;
    title?: string;
    onClose?: () => void;
    closable?: boolean;
    children: Snippet;
    /* Footer button label + callback; uses the accent primary by default */
    primaryLabel?: string;
    onPrimary?: () => void;
    primaryVariant?: 'primary' | 'danger';
  }

  let {
    open,
    title,
    onClose,
    closable = true,
    children,
    primaryLabel,
    onPrimary,
    primaryVariant = 'primary',
  }: Props = $props();

  function onKey(event: KeyboardEvent) {
    if (event.key === 'Escape') onClose?.();
  }
  function onOverlayClick(event: MouseEvent) {
    if (event.target === event.currentTarget) onClose?.();
  }
</script>

<svelte:window on:keydown={onKey} />

{#if open}
  <div class="overlay" role="presentation" onclick={onOverlayClick}>
    <div class="dialog" role="dialog" aria-modal="true" aria-labelledby="dialog-title">
      <header class="header">
        {#if title}<h2 id="dialog-title">{title}</h2>{/if}
        {#if closable}
          <Button variant="ghost" size="sm" label="Close" onclick={onClose}>×</Button>
        {/if}
      </header>
      <div class="body">{@render children()}</div>
      {#if primaryLabel}
        <footer class="footer">
          <Button variant={primaryVariant} label={primaryLabel} onclick={() => { onPrimary?.(); onClose?.(); }} />
        </footer>
      {/if}
    </div>
  </div>
{/if}

<style>
  .overlay {
    position: fixed;
    inset: 0;
    display: grid;
    place-items: center;
    padding: var(--sp-5);
    background: rgba(24, 25, 28, 0.45);
    z-index: 100;
    backdrop-filter: blur(2px);
  }
  .dialog {
    width: min(460px, 94vw);
    background: var(--surface-base);
    border: 1px solid var(--border-default);
    border-radius: 10px;
    box-shadow: var(--shadow-modal);
    overflow: hidden;
  }
  .header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--sp-3);
    padding: var(--sp-4);
    border-bottom: 1px solid var(--border-subtle);
  }
  .header h2 { margin: 0; font-size: var(--fs-lg); font-weight: 600; }
  .body { padding: var(--sp-4); color: var(--text-secondary); font-size: var(--fs-sm); }
  .footer {
    display: flex;
    justify-content: flex-end;
    gap: var(--sp-2);
    padding: var(--sp-3) var(--sp-4);
    border-top: 1px solid var(--border-subtle);
  }
</style>
