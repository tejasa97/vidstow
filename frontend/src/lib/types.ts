// Shared TypeScript shapes mirror the JSON returned by the Go bindings.
// Keeping them in one place makes it easy to spot drift between the
// app contract and the UI.

export type Quality =
  | 'best'
  | '4k'
  | '1440p'
  | '1080p'
  | '720p'
  | 'audio';

export const QUALITIES: Quality[] = ['best', '4k', '1440p', '1080p', '720p', 'audio'];

export type JobStatus =
  | 'pending'
  | 'active'
  | 'pausing'
  | 'paused'
  | 'canceling'
  | 'complete'
  | 'failed'
  | 'canceled'
  | 'action-required';

export interface OutputPlan {
  id: string;
  kind: 'video' | 'audio';
  label: string;
  resolution?: string;
  container: string;
  videoCodec?: string;
  audioCodec?: string;
  width?: number;
  height?: number;
  approxBytes?: number;
  sizeIsApproximate?: boolean;
  requiresFfmpeg?: boolean;
  audioBitrateKbps?: number;
  recommended?: boolean;
  available: boolean;
}

export interface JobSnapshot {
  id: string;
  url: string;
  videoID: string;
  title: string;
  channel: string;
  quality: Quality;
  qualityLabel: string;
  planId?: string;
  outputKind?: 'video' | 'audio';
  container?: string;
  videoCodec?: string;
  audioCodec?: string;
  approxBytes?: number;
  sizeApproximate?: boolean;
  requiresFfmpeg?: boolean;
  canPause?: boolean;
  processing?: boolean;
  outputDir: string;
  durationLabel: string;
  thumbnail: string;
  status: JobStatus;
  createdAt: string;
  startedAt?: string;
  completedAt?: string;
  bytes: number;
  total: number;
  progress: number;
  speedBps: number;
  etaSeconds: number;
  filename: string;
  absolutePath: string;
  message: string;
  errorReason?: string;
  optionsNote?: string;
}

export interface HistoryEntry {
  id: string;
  videoId: string;
  title: string;
  channel: string;
  quality: string;
  container?: string;
  videoCodec?: string;
  audioCodec?: string;
  filename: string;
  absolutePath: string;
  sizeBytes: number;
  completedAt: string;
  durationLabel: string;
  thumbnail: string;
  fileMissing?: boolean;
}

export interface FFmpegStatus {
  available: boolean;
  path: string;
  version: string;
  ffprobePath: string;
  message: string;
}

export interface Settings {
  downloadFolder: string;
  ffmpegPath: string;
  windowWidth: number;
  windowHeight: number;
  downloadConcurrency: number;
  perVideoSubfolder: boolean;
  confirmBeforeDownload: boolean;
  outputOptions: OutputOptions;
  automaticDiagnostics: '' | 'enabled' | 'disabled';
}

// One caption track reported by analysis; auto marks auto-generated tracks.
export interface SubtitleLanguage {
  code: string;
  name?: string;
  auto?: boolean;
}

// Per-download subtitle and embedding choices. Mirrors jobmodel.OutputOptions;
// the zero value means VidStow's plain media-only output.
export interface OutputOptions {
  subtitleMode?: '' | 'sidecar' | 'embed';
  subtitleLanguages?: string[];
  subtitleAutoCaptions?: boolean;
  subtitleFormat?: '' | 'srt' | 'vtt';
  embedMetadata?: boolean;
  embedThumbnail?: boolean;
  embedChapters?: boolean;
}

export interface UrlCheckResult {
  url: string;
  videoId?: string;
  playlistId?: string;
  kind: 'single_video' | 'playlist' | 'video_playlist';
  videoUrl?: string;
  playlistUrl?: string;
}

export interface PlaylistEntrySummary {
  index: number;
  videoId: string;
  url: string;
  title: string;
  duration?: string;
  thumbnail?: string;
  available: boolean;
}

export interface PlaylistSummary {
  id: string;
  url: string;
  title: string;
  channel: string;
  thumbnail: string;
  entryCount: number;
  available: number;
  unavailable: number;
  entries: PlaylistEntrySummary[];
}

export interface InfoSummary {
  title: string;
  channel: string;
  duration: string;
  thumbnail: string;
  videoId: string;
  url: string;
  durationSeconds: number;
  viewCount: number;
  uploadDate: string;
  description: string;
  mediaType?: string;
  access: AccessSummary;
  subtitles?: SubtitleLanguage[];
  plans: OutputPlan[];
}

export interface AccessSummary {
  code: 'public' | 'unlisted' | 'restricted' | 'unknown';
  label: string;
}

export type BatchLineStatus = 'ready' | 'duplicate' | 'invalid' | 'analysis_failed';

export interface BatchAnalysisCounts {
  pasted: number;
  ready: number;
  duplicate: number;
  invalid: number;
  analysisFailed: number;
}

export interface BatchAnalysisItem {
  lineNumber: number;
  input: string;
  status: BatchLineStatus;
  messageKey: string;
  message: string;
  duplicateOfLine?: number;
  title?: string;
  channel?: string;
  duration?: string;
  thumbnail?: string;
}

export interface BatchAnalysisView {
  token?: string;
  expiresAt?: string;
  counts: BatchAnalysisCounts;
  items: BatchAnalysisItem[];
}

export interface BatchStartResult {
  collectionId: string;
  admitted: number;
}

export interface PersistenceStatus {
  available: boolean;
  healthy: boolean;
  message?: string;
}

export interface StartupStatus {
  mode: 'starting' | 'healthy' | 'recovery-required';
  reason?: string;
  warning?: string;
}

export interface QuitSummary {
  activeDownloads: number;
  waitingOrPausedDownloads: number;
}

export interface BuildInfo {
  version: string;
  engineVersion: string;
  os: string;
  architecture: string;
  goVersion: string;
}

export interface QueueEvent {
  name: 'job:update' | 'queue:update' | 'persistence:update';
  job?: JobSnapshot;
  queue?: JobSnapshot[];
  persistence?: PersistenceStatus;
}

export interface AppError {
  reason: string;
  message: string;
}
