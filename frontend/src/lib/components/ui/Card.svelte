<script lang="ts">
  import type { Snippet } from 'svelte';

  interface Props {
    title?: string;
    description?: string;
    actions?: Snippet;
    children: Snippet;
    padded?: boolean;
  }

  let { title, description, actions, children, padded = true }: Props = $props();
</script>

<section class="card" class:padded>
  {#if title || actions}
    <header class="card-header">
      <div>
        {#if title}<h2 class="card-title">{title}</h2>{/if}
        {#if description}<p class="card-desc">{description}</p>{/if}
      </div>
      {#if actions}<div class="card-actions">{@render actions()}</div>{/if}
    </header>
  {/if}
  <div class="card-body">{@render children()}</div>
</section>

<style>
  .card {
    background: var(--surface-base);
    border: 1px solid var(--border-default);
    border-radius: var(--r-md);
    box-shadow: var(--shadow-card);
    overflow: hidden;
    min-width: 0;
  }
  .card-header {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: var(--sp-3);
    padding: var(--sp-4);
    padding-bottom: 0;
  }
  .card-header + .card-body { padding-top: var(--sp-3); }
  .card-title { margin: 0; font-size: var(--fs-lg); font-weight: 600; letter-spacing: -0.01em; }
  .card-desc { margin: 4px 0 0; color: var(--text-secondary); font-size: var(--fs-sm); }
  .card-actions { display: flex; align-items: center; flex-shrink: 0; }
  .card-body { padding: var(--sp-4); }
  .card:not(.padded) .card-body { padding: 0; }
  .card:not(.padded) .card-header { padding-left: var(--sp-4); padding-right: var(--sp-4); }
</style>
