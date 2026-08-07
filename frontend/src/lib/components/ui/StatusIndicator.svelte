<script lang="ts">
  export type StatusTone = 'neutral' | 'info' | 'success' | 'warning' | 'danger' | 'active';

  interface Props {
    tone?: StatusTone;
    dot?: boolean;
    label: string;
    pulse?: boolean;
  }

  let { tone = 'neutral', dot = true, label, pulse = false }: Props = $props();
</script>

<span class="container" data-tone={tone}>
  {#if dot}<span class="dot {tone}" class:pulse aria-hidden="true"></span>{/if}
  <span class="label">{label}</span>
</span>

<style>
  .container {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: var(--fs-sm);
    font-weight: 500;
  }
  .dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--text-muted);
    flex-shrink: 0;
  }
  .dot.neutral { background: var(--text-muted); }
  .dot.info { background: var(--status-info); }
  .dot.success { background: var(--status-success); }
  .dot.warning { background: var(--status-warning); }
  .dot.danger { background: var(--status-danger); }
  .dot.active {
    background: var(--status-info);
    animation: pulse 1.6s infinite ease-out;
  }
  .container[data-tone='neutral'] { color: var(--text-secondary); }
  .container[data-tone='info'] { color: var(--status-info); }
  .container[data-tone='success'] { color: var(--status-success); }
  .container[data-tone='warning'] { color: var(--status-warning); }
  .container[data-tone='danger'] { color: var(--status-danger); }
  .container[data-tone='active'] { color: var(--status-info); }
  .pulse { animation: pulse 1.6s infinite ease-out; }
  @keyframes pulse {
    0% { box-shadow: 0 0 0 0 rgba(47,111,237,0.5); }
    70% { box-shadow: 0 0 0 6px rgba(47,111,237,0); }
    100% { box-shadow: 0 0 0 0 rgba(47,111,237,0); }
  }
</style>
