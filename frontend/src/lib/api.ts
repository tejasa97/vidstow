// Thin wrappers around the Wails runtime. We intentionally avoid the
// generated `wailsjs/` directory in source — rolldown has trouble
// resolving the generated `.js` files in this environment, and writing
// to the Wails bindings directory at build time is fragile. The runtime
// contract (window.go.* and window.runtime.*) is stable, so we call it
// directly here. Wails injects these globals at app startup.

import type {
  BuildInfo,
  FFmpegStatus,
  HistoryEntry,
  InfoSummary,
  JobSnapshot,
  PlaylistSummary,
  PersistenceStatus,
  QuitSummary,
  QueueEvent,
  Settings,
  StartupStatus,
  UrlCheckResult,
} from './types';
import type { ActionRequiredReviewViewModel, QueueView } from './lifecycle-ui/types.js';

declare global {
  interface Window {
    go: {
      main: {
        App: Record<string, (...args: any[]) => Promise<any>>;
      };
    };
    runtime: {
      EventsOn: (event: string, cb: (...args: any[]) => void) => () => void;
      EventsOff: (event: string, ...rest: string[]) => void;
      EventsEmit: (event: string, ...args: any[]) => void;
      OpenDirectoryDialog: (opts: any) => Promise<string>;
      OpenFileDialog: (opts: any) => Promise<string>;
      ClipboardSetText: (text: string) => Promise<void>;
      BrowserOpenURL: (url: string) => void;
      WindowSetTitle?: (title: string) => void;
      LogError: (msg: string) => void;
    };
  }
}

function call<T>(method: string, ...args: any[]): Promise<T> {
  const fn = window.go?.main?.App?.[method];
  if (typeof fn !== 'function') {
    return Promise.reject(new Error(`Go binding "${method}" is not available yet`));
  }
  return fn(...args);
}

export interface StartPlaylistRequest {
  url: string;
  playlistId: string;
  quality: JobSnapshot['quality'];
  audioBitrate?: number;
  selectedItems: number[];
}

export interface StartRequest {
  url: string;
  videoId: string;
  title: string;
  channel: string;
  quality?: JobSnapshot['quality'];
  planId?: string;
  outputDir: string;
  duration: string;
  thumbnail: string;
}

export const api = {
  settings: {
    get: () => call<Settings>('GetSettings'),
    update: (next: Settings) => call<Settings>('UpdateSettings', next),
  },
  ffmpeg: {
    status: () => call<FFmpegStatus>('GetFFmpegStatus'),
    probe: () => call<FFmpegStatus>('ProbeFFmpeg'),
    configure: (path: string) => call<FFmpegStatus>('ConfigureFFmpeg', path),
    clear: () => call<FFmpegStatus>('ClearFFmpegPath'),
    pickPath: () => call<string>('PickFFmpegPath'),
  },
  app: {
    buildInfo: () => call<BuildInfo>('GetBuildInfo'),
    persistenceStatus: () => call<PersistenceStatus>('GetPersistenceStatus'),
    startupStatus: () => call<StartupStatus>('GetStartupStatus'),
    keepWorking: () => call<void>('KeepWorking'),
    pauseAndQuit: () => call<void>('PauseDownloadsAndQuit'),
    openDataFolder: () => call<void>('OpenDataFolder'),
  },
  folder: {
    pick: () => call<string>('PickDownloadFolder'),
  },
  validation: {
    url: (raw: string) => call<UrlCheckResult>('ValidateURL', raw),
  },
  analyse: {
    url: (raw: string) => call<InfoSummary>('AnalyzeURL', raw),
    playlist: (raw: string) => call<PlaylistSummary>('AnalyzePlaylist', raw),
  },
  jobs: {
    start: (req: StartRequest) => call<string>('StartDownload', req),
    startPlaylist: (req: StartPlaylistRequest) => call<string>('StartPlaylistDownload', req),
    list: () => call<JobSnapshot[]>('ListJobs'),
    cancel: (id: string) => call<void>('CancelJob', id),
    pause: (id: string) => call<void>('PauseJob', id),
    pauseAll: () => call<number>('PauseAllJobs'),
    resume: (id: string) => call<void>('ResumeJob', id),
    retry: (id: string) => call<void>('RetryJob', id),
    remove: (id: string) => call<void>('RemoveJob', id),
    clearCompleted: () => call<void>('ClearCompletedJobs'),
  },
  queue: {
    get: () => call<QueueView>('GetQueueView'),
    pause: (id: string, token: string) => call<void>('PauseQueueJob', id, token),
    cancel: (id: string, token: string) => call<void>('CancelQueueJob', id, token),
    resume: (id: string, token: string) => call<void>('ResumeQueueJob', id, token),
    retry: (id: string, token: string) => call<void>('RetryQueueJob', id, token),
    reviewActionRequired: (id: string, token: string) => call<ActionRequiredReviewViewModel>('ReviewActionRequiredQueueJob', id, token),
    startOverActionRequired: (id: string, token: string) => call<string>('StartOverActionRequiredQueueJob', id, token),
    retryActionRequired: (id: string, token: string) => call<void>('RetryActionRequiredQueueJob', id, token),
    retryActionRequiredFreshLink: (id: string, token: string) => call<void>('RetryActionRequiredWithFreshLink', id, token),
    discardActionRequired: (id: string, token: string) => call<void>('DiscardActionRequiredQueueJob', id, token),
    retryCleanup: (id: string, token: string) => call<void>('RetryQueueJobCleanup', id, token),
    open: (id: string, token: string) => call<void>('OpenQueueJob', id, token),
    remove: (id: string, token: string) => call<void>('RemoveQueueJob', id, token),
    pauseCollection: (id: string, token: string) => call<number>('PauseQueueCollection', id, token),
    cancelCollection: (id: string, token: string) => call<number>('CancelQueueCollection', id, token),
    resumeCollection: (id: string, token: string) => call<number>('ResumeQueueCollection', id, token),
    retryCollection: (id: string, token: string) => call<number>('RetryQueueCollection', id, token),
    removeCollection: (id: string, token: string) => call<number>('RemoveQueueCollection', id, token),
    pauseAll: (token: string) => call<number>('PauseAllQueueJobs', token),
    clearCompleted: (token: string) => call<void>('ClearCompletedQueueJobs', token),
  },
  downloads: {
    list: () => call<HistoryEntry[]>('ListDownloads'),
    remove: (id: string) => call<void>('RemoveDownload', id),
    deleteFile: (id: string) => call<void>('DeleteDownloadFile', id),
    clear: () => call<void>('ClearDownloads'),
  },
  fs: {
    open: (path: string) => call<void>('OpenFile', path),
    reveal: (path: string) => call<void>('RevealInFinder', path),
  },
  diagnostics: {
    copy: () => call<string>('CopyDiagnostics'),
    clear: () => call<void>('ClearDiagnostics'),
  },
  events: {
    onJobUpdate: (cb: (job: JobSnapshot) => void) =>
      window.runtime?.EventsOn?.('job:update', (event: QueueEvent) => {
        if (event?.job) cb(event.job);
      }),
    onQueue: (cb: (jobs: JobSnapshot[]) => void) =>
      window.runtime?.EventsOn?.('queue:update', (event: QueueEvent) => {
        if (event?.queue) cb(event.queue);
      }),
    onQueueView: (cb: (view: QueueView) => void) =>
      window.runtime?.EventsOn?.('queue:update', (event: QueueEvent & { queueView?: QueueView }) => {
        if (event?.queueView) cb(event.queueView);
      }),
    onJobQueueView: (cb: (view: QueueView) => void) =>
      window.runtime?.EventsOn?.('job:update', (event: QueueEvent & { queueView?: QueueView }) => {
        if (event?.queueView) cb(event.queueView);
      }),
    onHistory: (cb: (entries: HistoryEntry[]) => void) =>
      window.runtime?.EventsOn?.('history:update', (entries: HistoryEntry[]) => cb(entries ?? [])),
    onSettings: (cb: (settings: Settings) => void) =>
      window.runtime?.EventsOn?.('settings:update', (settings: Settings) => cb(settings)),
    onFFmpeg: (cb: (status: FFmpegStatus) => void) =>
      window.runtime?.EventsOn?.('ffmpeg:update', (status: FFmpegStatus) => cb(status)),
    onPersistence: (cb: (status: PersistenceStatus) => void) =>
      window.runtime?.EventsOn?.('persistence:update', (event: QueueEvent) => {
        if (event?.persistence) cb(event.persistence);
      }),
    onQuitRequest: (cb: (summary: QuitSummary) => void) =>
      window.runtime?.EventsOn?.('quit:request', (summary: QuitSummary) => cb(summary)),
    off: (event: string) => window.runtime?.EventsOff?.(event),
  },
};

export {};
