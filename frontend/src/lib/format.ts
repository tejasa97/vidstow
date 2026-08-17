// Pure formatting helpers. Pure functions keep the pages simple and
// make the helpers trivial to test without rendering anything.

import type { JobSnapshot } from './types.js';

const QUALITY_LABELS: Record<string, string> = {
  best: 'Best',
  '4k': '4K',
  '1440p': '1440p',
  '1080p': '1080p',
  '720p': '720p',
  audio: 'Audio only',
};

export function qualityLabel(q: string): string {
  return QUALITY_LABELS[q] ?? q;
}

export function formatBytes(bytes: number): string {
  if (!bytes || bytes < 0) return '—';
  const kb = 1024;
  const mb = kb * 1024;
  const gb = mb * 1024;
  if (bytes >= gb) return `${(bytes / gb).toFixed(1)} GB`;
  if (bytes >= mb) return `${(bytes / mb).toFixed(1)} MB`;
  if (bytes >= kb) return `${Math.round(bytes / kb)} KB`;
  return `${bytes} B`;
}

export function formatSpeed(bps: number): string {
  if (!bps || bps <= 0) return '';
  return `${formatBytes(bps)}/s`;
}

export function formatEta(seconds: number): string {
  if (!seconds || seconds <= 0 || !Number.isFinite(seconds)) return '';
  const s = Math.round(seconds);
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`;
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  return `${h}h ${m}m`;
}

export function formatProgress(p: number): string {
  if (!Number.isFinite(p)) return '0%';
  return `${Math.max(0, Math.min(100, Math.round(p * 100)))}%`;
}

export function formatDate(iso: string): string {
  if (!iso) return '';
  try {
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return iso;
    return d.toLocaleString(undefined, {
      year: 'numeric',
      month: 'short',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
    });
  } catch {
    return iso;
  }
}

export function formatRelative(iso: string): string {
  if (!iso) return '';
  try {
    const d = new Date(iso).getTime();
    const diff = Date.now() - d;
    if (diff < 60_000) return 'just now';
    if (diff < 3_600_000) return `${Math.floor(diff / 60_000)}m ago`;
    if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)}h ago`;
    if (diff < 7 * 86_400_000) return `${Math.floor(diff / 86_400_000)}d ago`;
    return formatDate(iso);
  } catch {
    return iso;
  }
}

export function shortTitle(title: string, max = 42): string {
  const trimmed = (title || '').trim();
  if (trimmed.length <= max) return trimmed;
  return `${trimmed.slice(0, Math.max(1, max - 1)).trimEnd()}…`;
}

export function formatViewCount(count: number): string {
  if (!Number.isFinite(count) || count < 0) return '';
  const abs = Math.round(count);
  if (abs < 1000) return abs.toLocaleString('en-US');

  const label = (divisor: number) => {
    const scaled = abs / divisor;
    return scaled >= 100
      ? String(Math.round(scaled))
      : scaled.toFixed(scaled >= 10 ? 0 : 1).replace(/\.0$/, '');
  };

  if (abs >= 1_000_000_000 || label(1_000_000) === '1000') return `${label(1_000_000_000)}B`;
  if (abs >= 1_000_000 || label(1_000) === '1000') return `${label(1_000_000)}M`;
  return `${label(1_000)}K`;
}

export function formatEngineVersion(version: string): string {
  if (!version || version === 'Loading…') return version;
  // Go pseudo-versions use a timestamp and commit after the stable base.
  const pseudo = version.match(/^(v?\d+\.\d+\.\d+)-(?:0\.)?\d{14}-([0-9a-f]{7,40})/i);
  if (pseudo) return `${pseudo[1]} (${pseudo[2].slice(0, 7)})`;

  const tag = version.match(/^(v?\d+\.\d+\.\d+(?:-[0-9A-Za-z][0-9A-Za-z.-]*)?(?:\+[0-9A-Za-z.-]+)?)/)?.[1];
  const hash = [...version.matchAll(/([0-9a-f]*[a-f][0-9a-f]{6,39})/gi)].at(-1)?.[1]?.slice(0, 7);
  if (tag) return tag;
  return hash ?? version;
}

export function youtubeUrlFromText(text: string): string {
  if (!text) return '';
  const match = text.match(
    /https?:\/\/(?:www\.|m\.)?(?:youtube\.com\/(?:watch\?[^\s]*v=[\w-]{6,}|playlist\?list=[\w-]+|shorts\/[\w-]{6,}|@[^\s/]+(?:\/(?:videos|shorts))?|channel\/UC[\w-]{22}(?:\/(?:videos|shorts))?|(?:c|user)\/[^\s/]+(?:\/(?:videos|shorts))?)|youtu\.be\/[\w-]{6,})[^\s]*/i,
  );
  return match?.[0] ?? '';
}

export function progressOf(job: JobSnapshot): number {
  if (job.total > 0 && job.bytes > 0) {
    return Math.max(0, Math.min(1, job.bytes / job.total));
  }
  return Math.max(0, Math.min(1, job.progress || 0));
}
