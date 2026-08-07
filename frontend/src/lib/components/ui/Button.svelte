<script lang="ts">
  import type { Snippet } from 'svelte';

  type Variant = 'primary' | 'secondary' | 'ghost' | 'danger';
  type Size = 'sm' | 'md' | 'lg';

  interface Props {
    variant?: Variant;
    size?: Size;
    disabled?: boolean;
    loading?: boolean;
    fullWidth?: boolean;
    label: string;
    children?: Snippet;
  }

  let {
    variant = 'secondary',
    size = 'md',
    disabled = false,
    loading = false,
    fullWidth = false,
    label,
    children,
    ...rest
  }: Props & Record<string, unknown> = $props();

  const classes = $derived(['btn', `variant-${variant}`, `size-${size}`].filter(Boolean).join(' '));
</script>

<button
  type="button"
  class={classes}
  class:disabled={disabled || loading}
  class:full={fullWidth}
  {disabled}
  aria-busy={loading || undefined}
  aria-label={loading ? `${label}…` : undefined}
  {...rest}
>
  {#if loading}
    <span class="spinner" aria-hidden="true"></span>
  {/if}
  {#if children}{@render children()}{:else}{label}{/if}
</button>

<style>
  .btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: var(--sp-2);
    border-radius: var(--r-md);
    font-family: inherit;
    font-weight: 550;
    line-height: 1;
    white-space: nowrap;
    cursor: pointer;
    transition: background 120ms ease, border-color 120ms ease, color 120ms ease, box-shadow 120ms ease;
  }
  .btn:focus-visible {
    outline: 2px solid var(--accent-400);
    outline-offset: 2px;
  }
  .btn:disabled { cursor: not-allowed; opacity: 0.55; }

  /* Sizes */
  .size-sm { min-height: 28px; padding: 0 var(--sp-3); font-size: var(--fs-xs); }
  .size-md { min-height: 36px; padding: 0 var(--sp-4); font-size: var(--fs-sm); }
  .size-lg { min-height: 42px; padding: 0 var(--sp-5); font-size: var(--fs-md); }

  .full { width: 100%; }

  /* Variants */
  .variant-primary {
    color: var(--text-on-accent);
    background: #15161A;
    border: 1px solid #15161A;
  }
  .variant-primary:hover:not(:disabled) { background: #2A2C31; border-color: #2A2C31; }

  .variant-secondary {
    color: var(--text-primary);
    background: var(--surface-base);
    border: 1px solid var(--border-default);
  }
  .variant-secondary:hover:not(:disabled) { background: var(--surface-hover); border-color: var(--border-strong); }

  .variant-ghost {
    color: var(--accent-600);
    background: transparent;
    border: 1px solid transparent;
  }
  .variant-ghost:hover:not(:disabled) { background: var(--accent-soft); }

  .variant-danger {
    color: var(--status-danger);
    background: var(--surface-base);
    border: 1px solid var(--border-default);
  }
  .variant-danger:hover:not(:disabled) {
    background: var(--status-danger-soft);
    border-color: rgba(214, 69, 61, 0.4);
  }

  .spinner {
    width: 12px;
    height: 12px;
    border-radius: 50%;
    border: 2px solid currentColor;
    border-top-color: transparent;
    animation: spin 700ms linear infinite;
  }
  @keyframes spin { to { transform: rotate(360deg); } }
</style>
