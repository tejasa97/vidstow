<script lang="ts">
  import { api } from '../lib/api.js';
  import { modal, pendingUrl, queueView, route, showBanner, showError } from '../lib/stores.js';
  import ActionRequiredReviewDialog from '../lib/lifecycle-ui/ActionRequiredReviewDialog.svelte';
  import QueueOverview from '../lib/lifecycle-ui/QueueOverview.svelte';
  import type { ActionRequiredReviewViewModel, LifecycleJobEventDetail, QueueCollectionActionEvent, QueueOverviewViewModel, QueueView } from '../lib/lifecycle-ui/types.js';
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
  let actionRequiredReview: ActionRequiredReviewViewModel | null = null;
  let actionRequiredAuthority: LifecycleJobEventDetail | null = null;
  let startingOver = false;

  async function refresh(): Promise<QueueView> {
    const next = await api.queue.get();
    queueView.update((current) => newestQueueView(current, next));
    return next;
  }

  async function reloadActionRequiredReview(jobId: string): Promise<void> {
    try {
      const next = await refresh();
      const row = next.rows.find((candidate) => candidate.id === jobId);
      const commandToken = row?.commandToken;
      if (row?.capabilities?.review !== true || typeof commandToken !== 'string' || commandToken.length === 0) {
        throw new Error('review authority is no longer available');
      }
      const authority = { jobId, commandToken };
      const review = await api.queue.reviewActionRequired(jobId, commandToken);
      actionRequiredAuthority = authority;
      actionRequiredReview = review;
    } catch {
      actionRequiredReview = null;
      actionRequiredAuthority = null;
    }
  }

  async function action(detail: LifecycleJobEventDetail, operation: (id: string, token: string) => Promise<unknown>, fallback: string, success?: string) {
    try {
      await operation(detail.jobId, detail.commandToken);
      await refresh();
      if (success) showBanner('info', success);
    } catch (err) {
      await refresh().catch(() => undefined);
      showError(err, fallback);
    }
  }

  async function startAgain(detail: LifecycleJobEventDetail) {
    try {
      const url = await api.queue.startAgain(detail.jobId, detail.commandToken);
      pendingUrl.set(url);
      route.set('home');
      showBanner('info', 'The old failed item was removed. Check the default folder, then confirm this download again.');
    } catch (err) {
      await refresh().catch(() => undefined);
      showError(err, 'Could not start this item again');
    }
  }

  async function reviewActionRequired(detail: LifecycleJobEventDetail) {
    try {
      const review = await api.queue.reviewActionRequired(detail.jobId, detail.commandToken);
      actionRequiredAuthority = detail;
      actionRequiredReview = review;
    } catch (err) {
      await refresh().catch(() => undefined);
      showError(err, 'Could not review this download');
    }
  }

  function closeActionRequiredReview() {
    if (startingOver) return;
    actionRequiredReview = null;
    actionRequiredAuthority = null;
  }

  async function startOverActionRequired() {
    if (!actionRequiredAuthority || startingOver) return;
    startingOver = true;
    try {
      const url = await api.queue.startOverActionRequired(actionRequiredAuthority.jobId, actionRequiredAuthority.commandToken);
      actionRequiredReview = null;
      actionRequiredAuthority = null;
      pendingUrl.set(url);
      route.set('home');
      showBanner('info', 'Analyze the video again to start a fresh download. The original saved data and destination reservation were preserved.');
    } catch (err) {
      actionRequiredReview = null;
      actionRequiredAuthority = null;
      await refresh().catch(() => undefined);
      showError(err, 'Could not start over from this download');
    } finally {
      startingOver = false;
    }
  }

  async function removeActionRequired() {
    if (!actionRequiredAuthority || startingOver) return;
    const authority = actionRequiredAuthority;
    startingOver = true;
    try {
      await api.queue.remove(authority.jobId, authority.commandToken);
      actionRequiredReview = null;
      actionRequiredAuthority = null;
      await refresh();
      showBanner('info', 'Removed from the queue. Saved temporary data was left on disk.');
    } catch (err) {
      await reloadActionRequiredReview(authority.jobId);
      showError(err, 'Could not remove this download');
    } finally {
      startingOver = false;
    }
  }

  async function retryActionRequiredRecovery() {
    if (!actionRequiredAuthority || startingOver) return;
    startingOver = true;
    try {
      await api.queue.retryActionRequired(actionRequiredAuthority.jobId, actionRequiredAuthority.commandToken);
      actionRequiredReview = null;
      actionRequiredAuthority = null;
      await refresh();
      showBanner('info', 'The saved session is safe again and has been returned to the queue.');
    } catch (err) {
      actionRequiredReview = null;
      actionRequiredAuthority = null;
      await refresh().catch(() => undefined);
      showError(err, 'The saved session still could not be recovered safely');
    } finally {
      startingOver = false;
    }
  }

  async function retryActionRequiredFreshLink() {
    if (!actionRequiredAuthority || startingOver) return;
    startingOver = true;
    try {
      await api.queue.retryActionRequiredFreshLink(actionRequiredAuthority.jobId, actionRequiredAuthority.commandToken);
      actionRequiredReview = null;
      actionRequiredAuthority = null;
      await refresh();
      showBanner('info', 'Retrying in place with a freshly resolved media link. The uncertain session was retained for safe cleanup.');
    } catch (err) {
      actionRequiredReview = null;
      actionRequiredAuthority = null;
      await refresh().catch(() => undefined);
      showError(err, 'Could not retry with a fresh link');
    } finally {
      startingOver = false;
    }
  }

  async function retryCleanup() {
    if (!actionRequiredAuthority || startingOver) return;
    startingOver = true;
    try {
      await api.queue.retryCleanup(actionRequiredAuthority.jobId, actionRequiredAuthority.commandToken);
      actionRequiredReview = null;
      actionRequiredAuthority = null;
      await refresh();
      showBanner('info', 'Cleanup will be tried again automatically.');
    } catch (err) {
      actionRequiredReview = null;
      actionRequiredAuthority = null;
      await refresh().catch(() => undefined);
      showError(err, 'Could not retry cleanup');
    } finally {
      startingOver = false;
    }
  }

  function discardActionRequired() {
    if (!actionRequiredAuthority || startingOver) return;
    const authority = actionRequiredAuthority;
    actionRequiredReview = null;
    actionRequiredAuthority = null;
    modal.set({
      kind: 'confirm',
      title: 'Discard saved temporary data?',
      message: 'VidStow will only remove data the download engine can identify safely. If cleanup cannot be completed, this queue item will remain visible.',
      actions: [{
        label: 'Discard saved data',
        primary: true,
        action: async () => {
          try {
            await api.queue.discardActionRequired(authority.jobId, authority.commandToken);
            await refresh();
            showBanner('info', 'Saved temporary data was discarded or scheduled for safe cleanup.');
          } catch (err) {
            await refresh().catch(() => undefined);
            showError(err, 'Saved data could not be discarded safely');
          }
        },
      }],
    });
  }

  async function collectionAction(detail: QueueCollectionActionEvent) {
    const collection = model.collections?.find((candidate) => candidate.id === detail.collectionId);
    const collectionLabel = collection?.kind === 'batch' ? 'batch' : 'playlist';
    const collectionTitle = collection?.title ?? `This ${collectionLabel}`;
    const operations = {
      pause: api.queue.pauseCollection,
      cancel: api.queue.cancelCollection,
      resume: api.queue.resumeCollection,
      retry: api.queue.retryCollection,
      remove: api.queue.removeCollection,
    };
    const execute = async () => {
      try {
        const count = await operations[detail.action](detail.collectionId, detail.commandToken);
        await refresh();
        showBanner('info', `${detail.action === 'remove' ? 'Removed' : 'Updated'} ${count} ${collectionLabel} item${count === 1 ? '' : 's'}.`);
      } catch (err) { showError(err, `Could not ${detail.action} the ${collectionLabel}`); }
    };
    if (detail.action === 'cancel' || detail.action === 'remove') {
      modal.set({
        kind: 'confirm',
        title: detail.action === 'cancel' ? `Cancel this ${collectionLabel}?` : `Remove this ${collectionLabel} from the queue?`,
        message: `${collectionTitle} contains ${collection?.total ?? 0} queue item${collection?.total === 1 ? '' : 's'}. Completed files remain on disk.`,
        actions: [{ label: detail.action === 'cancel' ? `Cancel ${collectionLabel === 'batch' ? 'Batch' : 'Playlist'}` : `Remove ${collectionLabel === 'batch' ? 'Batch' : 'Playlist'}`, primary: true, action: execute }],
      });
      return;
    }
    await execute();
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
    else if (event.action === 'start-again') startAgain(event);
    else if (event.action === 'open-source') action(event, api.queue.openSource, 'Could not open the source');
    else if (event.action === 'copy-link') action(event, api.queue.copyLink, 'Could not copy the source link', 'Source link copied.');
    else if (event.action === 'review') reviewActionRequired(event);
    else if (event.action === 'open') action(event, api.queue.open, 'Could not open the downloaded file');
    else if (event.action === 'remove') action(event, api.queue.remove, 'Could not remove the download');
  }}
/>

<ActionRequiredReviewDialog
  open={actionRequiredReview !== null}
  review={actionRequiredReview}
  busy={startingOver}
  onClose={closeActionRequiredReview}
  onRemove={removeActionRequired}
  onStartOver={startOverActionRequired}
  onRetryRecovery={retryActionRequiredRecovery}
  onRetryFreshLink={retryActionRequiredFreshLink}
  onDiscard={discardActionRequired}
  onRetryCleanup={retryCleanup}
/>
