/**
 * Presentation-safe lifecycle values.
 *
 * Durable lifecycle, presentation phase, and worker-slot occupancy are kept
 * as separate fields on purpose. A row can therefore say “Pausing” while it
 * still occupies a slot, or “Cleaning up” after that slot has been released.
 */
export type DurableLifecycle =
  | 'pending'
  | 'active'
  | 'pausing'
  | 'paused'
  | 'canceling'
  | 'failed'
  | 'canceled'
  | 'completed'
  | 'action-required';

export type PresentationPhase =
  | 'preparing'
  | 'downloading'
  | 'waiting-for-processing'
  | 'finalizing'
  | 'ready-to-publish'
  | 'publishing'
  | 'cleaning-up';

export type LifecycleBadgeTone = 'neutral' | 'info' | 'warning' | 'danger' | 'success';

export type LifecycleJobAction =
  | 'pause'
  | 'cancel'
  | 'resume'
  | 'retry'
  | 'download-again'
  | 'review'
  | 'open'
  | 'remove';

export interface LifecycleJobCapabilities {
  pause?: boolean;
  cancel?: boolean;
  resume?: boolean;
  retry?: boolean;
  downloadAgain?: boolean;
  review?: boolean;
  open?: boolean;
  remove?: boolean;
}

/**
 * Safe data needed to render one queue row. This is intentionally not the
 * backend Job type: no runtime handles, credentials, paths, or engine objects
 * belong in a presentation view model.
 */
export interface LifecycleJobViewModel {
  id: string;
  title: string;
  metadata?: string;
  thumbnailUrl?: string;
  lifecycle: DurableLifecycle;
  phase?: PresentationPhase;
  occupiesSlot: boolean;
  progress?: number;
  progressLabel?: string;
  speedLabel?: string;
  etaLabel?: string;
  message?: string;
  queuePosition?: number;
  queueLabel?: string;
  capabilities?: LifecycleJobCapabilities;
}

export interface QueueSummaryViewModel {
  totalJobs: number;
  runningJobs: number;
  occupiedSlots: number;
  slotLimit: number;
  waitingJobs: number;
  pausedJobs: number;
}

export type QueueNoticeTone = 'info' | 'warning';

export interface QueueOverviewViewModel {
  summary: QueueSummaryViewModel;
  jobs: LifecycleJobViewModel[];
  sectionTitle?: string;
  notice?: string;
  noticeTone?: QueueNoticeTone;
  pauseAllDisabled?: boolean;
  clearCompletedDisabled?: boolean;
  footerText?: string;
}

export interface QueueSettingsViewModel {
  concurrency: number;
  minimum?: number;
  maximum?: number;
  defaultValue?: number;
  disabled?: boolean;
}

export interface QuitConfirmationViewModel {
  activeDownloads: number;
  waitingOrPausedDownloads: number;
}

export interface RecoveryRequiredViewModel {
  stateFileStatus?: string;
  automaticCleanupStatus?: string;
  savedMediaStatus?: string;
  footerMessage?: string;
}

export interface DestinationConflictViewModel {
  unavailableName: string;
  proposedName: string;
  proposedNameAvailable: boolean;
}

export interface LifecycleJobEventDetail {
  jobId: string;
}

export type LifecycleJobEventName =
  | 'pause'
  | 'cancel'
  | 'resume'
  | 'retry'
  | 'download-again'
  | 'review'
  | 'open'
  | 'remove';

export const MIN_CONCURRENCY = 1;
export const MAX_CONCURRENCY = 10;
export const DEFAULT_CONCURRENCY = 2;
export const PAUSE_ALL_NOTICE = 'Pause requested. Jobs will settle individually; finalizing work may still complete.';

export const DEFAULT_RECOVERY_REQUIRED: Required<RecoveryRequiredViewModel> = {
  stateFileStatus: 'Could not validate State v2',
  automaticCleanupStatus: 'Disabled',
  savedMediaStatus: 'Preserved',
  footerMessage: 'No recovery files have been changed.',
};

export function queuePositionLabel(position?: number): string | undefined {
  if (!position || position < 1) return undefined;
  return position === 1 ? 'Next' : `Position ${position}`;
}

export function lifecycleLabel(
  lifecycle: DurableLifecycle,
  phase?: PresentationPhase,
): string {
  if (phase === 'cleaning-up') return 'Cleaning up';

  switch (lifecycle) {
    case 'pausing':
      return 'Pausing';
    case 'canceling':
      return 'Canceling';
    case 'pending':
      return 'Queued';
    case 'paused':
      return 'Paused';
    case 'failed':
      return 'Failed';
    case 'canceled':
      return 'Canceled';
    case 'completed':
      return 'Completed';
    case 'action-required':
      return 'Action required';
    case 'active':
      switch (phase) {
        case 'preparing':
          return 'Preparing';
        case 'waiting-for-processing':
          return 'Waiting for processing';
        case 'finalizing':
          return 'Finalizing';
        case 'ready-to-publish':
          return 'Ready to publish';
        case 'publishing':
          return 'Publishing';
        default:
          return 'Downloading';
      }
  }
}

export function lifecycleTone(
  lifecycle: DurableLifecycle,
  phase?: PresentationPhase,
): LifecycleBadgeTone {
  if (phase === 'cleaning-up') return 'neutral';
  if (lifecycle === 'failed') return 'danger';
  if (lifecycle === 'action-required' || lifecycle === 'pausing' || lifecycle === 'canceling') return 'warning';
  if (lifecycle === 'completed') return 'success';
  if (lifecycle === 'canceled' || lifecycle === 'paused' || lifecycle === 'pending') return 'neutral';
  return 'info';
}

export function lifecycleMessage(job: LifecycleJobViewModel): string | undefined {
  if (job.message) return job.message;
  if (job.phase === 'cleaning-up') return 'Removing temporary files...';
  if (job.lifecycle === 'pausing') return 'Saving resume state...';
  if (job.lifecycle === 'canceling') return 'Discarding resumable data...';
  if (job.lifecycle === 'failed') return 'Download failed';
  if (job.lifecycle === 'canceled') return 'Canceled. Resumable data was removed.';
  if (job.lifecycle === 'action-required') return 'The reserved filename is no longer available.';
  if (job.phase === 'finalizing') return 'Merging video and audio...';
  if (job.phase === 'ready-to-publish') return 'Ready to publish.';
  if (job.phase === 'publishing') return 'Publishing...';
  if (job.phase === 'waiting-for-processing') return 'Waiting for processing...';
  return job.queueLabel ?? queuePositionLabel(job.queuePosition);
}

export function concurrencyValues(
  minimum = MIN_CONCURRENCY,
  maximum = MAX_CONCURRENCY,
): number[] {
  const lower = Math.max(MIN_CONCURRENCY, Math.min(minimum, MAX_CONCURRENCY));
  const upper = Math.max(lower, Math.min(maximum, MAX_CONCURRENCY));
  return Array.from({ length: upper - lower + 1 }, (_, index) => lower + index);
}
