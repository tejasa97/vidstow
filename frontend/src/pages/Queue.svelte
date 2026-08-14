<script lang="ts">
  import { api } from '../lib/api.js';
  import { queueView, showBanner, showError } from '../lib/stores.js';
  import QueueOverview from '../lib/lifecycle-ui/QueueOverview.svelte';
  import type { LifecycleJobEventDetail, QueueCollectionActionEvent, QueueOverviewViewModel, QueueView } from '../lib/lifecycle-ui/types.js';
  import { newestQueueView } from '../lib/queue-view.js';

  function modelFrom(view: QueueView | null): QueueOverviewViewModel {
    const persistence = view?.persistence;
    const writable = persistence?.available === true && persistence.healthy === true;
    return {
      summary: view?.summary ?? { totalJobs: 0, runningJobs: 0, occupiedSlots: 0, slotLimit: 2, processingOccupied: 0, processingLimit: 3, waitingJobs: 0, pausedJobs: 0 },
      jobs: writable ? (view?.rows ?? []) : (view?.rows ?? []).map((row) => ({ ...row, capabilities: {}, commandToken: undefined })),
      collections: writable ? (view?.collections ?? []) : (view?.collections ?? []).map((collection) => ({ ...collection, capabilities: {}, commandToken: undefined })),
      canPauseAll: writable && view?.capabilities?.pauseAll === true,
      canClearCompleted: writable && view?.capabilities?.clearCompleted === true,
      commandToken: writable ? view?.capabilities?.commandToken : undefined,
      notice: !persistence?.available
        ? (persistence?.message || 'Download state is unavailable. Queue actions are disabled.')
        : !persistence.healthy
          ? (persistence.message || 'VidStow could not save the download queue.')
          : undefined,
      noticeTone: 'warning',
      footerText: persistence?.available && persistence.healthy ? 'Jobs are saved automatically.' : 'Queue changes require healthy saved state.',
    };
  }

  $: model = modelFrom($queueView);

  async function refresh(): Promise<void> {
    const next = await api.queue.get();
    queueView.update((current) => newestQueueView(current, next));
  }

  async function action(detail: LifecycleJobEventDetail, operation: (id: string, token: string) => Promise<unknown>, fallback: string, success?: string) {
    try {
      await operation(detail.jobId, detail.commandToken);
      await refresh();
      if (success) showBanner('info', success);
    } catch (err) { showError(err, fallback); }
  }

  async function collectionAction(detail: QueueCollectionActionEvent) {
    const operations = {
      pause: api.queue.pauseCollection,
      cancel: api.queue.cancelCollection,
      resume: api.queue.resumeCollection,
      retry: api.queue.retryCollection,
      remove: api.queue.removeCollection,
    };
    try {
      const count = await operations[detail.action](detail.collectionId, detail.commandToken);
      await refresh();
      showBanner('info', `${detail.action === 'remove' ? 'Removed' : 'Updated'} ${count} playlist item${count === 1 ? '' : 's'}.`);
    } catch (err) { showError(err, `Could not ${detail.action} the playlist`); }
  }

  async function pauseAll() {
    try {
      const count = await api.queue.pauseAll(model.commandToken ?? '');
      await refresh();
      showBanner('info', count ? `Pause requested for ${count} job${count === 1 ? '' : 's'}.` : 'No jobs can be paused right now.');
    } catch (err) { showError(err, 'Could not pause the queue'); }
  }

  async function clearCompleted() {
    try { await api.queue.clearCompleted(model.commandToken ?? ''); await refresh(); }
    catch (err) { showError(err, 'Could not clear completed downloads'); }
  }
</script>

<QueueOverview
  {model}
  onPauseAll={pauseAll}
  onClearCompleted={clearCompleted}
  onCollectionAction={collectionAction}
  onAction={(event) => {
    if (event.action === 'pause') action(event, api.queue.pause, 'Could not pause the download');
    else if (event.action === 'cancel') action(event, api.queue.cancel, 'Could not cancel the download');
    else if (event.action === 'resume') action(event, api.queue.resume, 'Could not resume the download', 'Download resumed.');
    else if (event.action === 'retry') action(event, api.queue.retry, 'Could not retry the download', 'Retry added to the queue.');
    else if (event.action === 'open') action(event, api.queue.open, 'Could not open the downloaded file');
    else if (event.action === 'remove') action(event, api.queue.remove, 'Could not remove the download');
  }}
/>
