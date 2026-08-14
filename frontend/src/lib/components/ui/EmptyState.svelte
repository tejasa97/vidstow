<script lang="ts">
  import type { Snippet } from 'svelte';

  interface Props {
    title?: string;
    message?: string;
    icon?: 'inbox' | 'search' | 'muted';
    action?: Snippet;
  }

  let { title, message, icon = 'inbox', action }: Props = $props();
</script>

<div class="empty">
  <div class="art" aria-hidden="true">
    {#if icon === 'search'}
      <svg viewBox="0 0 24 24" width="28" height="28" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"><circle cx="11" cy="11" r="7"/><path d="M21 21l-4.3-4.3"/></svg>
    {:else}
      <svg viewBox="0 0 24 24" width="28" height="28" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M4 4h16l-6 8v6l-4 2v-8z"/></svg>
    {/if}
  </div>
  {#if title}<h3 class="title">{title}</h3>{/if}
  {#if message}<p class="message">{message}</p>{/if}
  {#if action}<div class="action">{@render action()}</div>{/if}
</div>

<style>
  .empty {
    display: flex;
    flex-direction: column;
    align-items: center;
    text-align: center;
    gap: var(--sp-2);
    padding: var(--sp-8) var(--sp-4);
    border: 1px dashed var(--border-default);
    border-radius: var(--r-md);
    background: var(--surface-sunken);
  }
  .art {
    display: grid;
    place-items: center;
    width: 48px;
    height: 48px;
    border-radius: var(--r-md);
    background: var(--surface-base);
    border: 1px solid var(--border-subtle);
    color: var(--text-secondary);
    margin-bottom: var(--sp-2);
  }
  .title { margin: 0; font-size: var(--fs-md); font-weight: 600; }
  .message { margin: 0; color: var(--text-secondary); font-size: var(--fs-sm); max-width: 34ch; }
  .action { margin-top: var(--sp-3); }
</style>
