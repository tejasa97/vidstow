<script lang="ts">
  import { onDestroy, onMount } from 'svelte';
  import { api } from './lib/api.js';
  import { errorMessage, ffmpeg, history, jobs, queueView, route, settings, modal, persistence, pendingUrl, showBanner } from './lib/stores.js';
  import { progressOf, youtubeUrlFromText } from './lib/format.js';
  import type { QueueView } from './lib/lifecycle-ui/types.js';
  import { newestQueueView } from './lib/queue-view.js';
  import type { JobSnapshot, QuitSummary, StartupStatus } from './lib/types.js';
  import { DEFAULT_RECOVERY_REQUIRED, type RecoveryRequiredViewModel } from './lib/lifecycle-ui/types.js';
  import QuitConfirmationDialog from './lib/lifecycle-ui/QuitConfirmationDialog.svelte';
  import RecoveryRequiredShell from './lib/lifecycle-ui/RecoveryRequiredShell.svelte';
  import Sidebar from './lib/components/Sidebar.svelte';
  import Modal from './lib/components/Modal.svelte';
  import Banner from './lib/components/Banner.svelte';
  import Home from './pages/Home.svelte';
  import Queue from './pages/Queue.svelte';
  import Downloads from './pages/Downloads.svelte';
  import Settings from './pages/Settings.svelte';
  import About from './pages/About.svelte';

  let unsubAll: Array<() => void> = [];
  let startupStatus: StartupStatus | null = null;
  let quitOpen = false;
  let quitModel: QuitSummary = { activeDownloads: 0, waitingOrPausedDownloads: 0 };
  let recoveryModel: RecoveryRequiredViewModel = DEFAULT_RECOVERY_REQUIRED;

  onMount(async () => {
    unsubAll = [
      api.events.onJobUpdate(updateJobInList),
      api.events.onQueue((list) => jobs.set(list ?? [])),
      api.events.onQueueView(applyQueueView),
      api.events.onJobQueueView(applyQueueView),
      api.events.onHistory((entries) => history.set(entries ?? [])),
      api.events.onSettings((value) => settings.set(value)),
      api.events.onFFmpeg((status) => ffmpeg.set(status)),
      api.events.onPersistence((status) => {
        persistence.set(status);
        if (status.available && !status.healthy && status.message) showBanner('warning', status.message, 10_000);
      }),
      api.events.onQuitRequest((summary) => {
        quitModel = summary ?? { activeDownloads: 0, waitingOrPausedDownloads: 0 };
        quitOpen = true;
      }),
    ].filter(Boolean) as Array<() => void>;

    try {
      startupStatus = await api.app.startupStatus();
      if (startupStatus?.mode === 'recovery-required') {
        recoveryModel = {
          ...DEFAULT_RECOVERY_REQUIRED,
          stateFileStatus: startupStatus.reason ? `State v2: ${startupStatus.reason}` : DEFAULT_RECOVERY_REQUIRED.stateFileStatus,
        };
        return;
      }
      const [savedSettings, initialJobs, initialQueueView, savedHistory, ffmpegStatus, persistenceStatus] = await Promise.all([
        api.settings.get(),
        api.jobs.list(),
        api.queue.get(),
        api.downloads.list(),
        api.ffmpeg.status(),
        api.app.persistenceStatus(),
      ]);
      settings.set(savedSettings);
      jobs.set(initialJobs ?? []);
      applyQueueView(initialQueueView);
      history.set(savedHistory ?? []);
      ffmpeg.set(ffmpegStatus);
      persistence.set(persistenceStatus);
      if (!ffmpegStatus.available) {
        modal.set({
          kind: 'ffmpeg-missing',
          title: 'FFmpeg is required for most downloads',
          message: 'Install FFmpeg or point VidStow at it in Settings. You can still queue original audio that does not need merging.',
          actions: [{ label: 'Open Settings', primary: true, action: () => navigate('settings') }],
        });
      }
    } catch (err) {
      modal.set({
        kind: 'error',
        title: 'The app could not finish starting',
        message: errorMessage(err, 'Close and reopen the app. If the problem continues, copy the diagnostic details.'),
      });
    }
  });

  onDestroy(() => {
    for (const off of unsubAll) off();
  });

  function updateJobInList(updated: JobSnapshot) {
    jobs.update((list) => {
      const idx = list.findIndex((j) => j.id === updated.id);
      if (idx === -1) return [updated, ...list];
      const copy = list.slice();
      copy[idx] = { ...copy[idx], ...updated };
      return copy;
    });
  }

  function applyQueueView(next: QueueView) {
    queueView.update((current) => {
      return newestQueueView(current, next);
    });
    if (next?.persistence) persistence.set(next.persistence);
  }

  function navigate(target: 'home' | 'queue' | 'downloads' | 'settings' | 'about') {
    route.set(target);
  }

  async function copyRecoveryDiagnostics() {
    try {
      await api.diagnostics.copy();
      showBanner('success', 'Diagnostics copied.', 5000);
    } catch (err) {
      modal.set({ kind: 'error', title: 'Could not copy diagnostics', message: errorMessage(err, 'Try again or open the data folder.') });
    }
  }

  async function openRecoveryDataFolder() {
    try {
      await api.app.openDataFolder();
    } catch (err) {
      modal.set({ kind: 'error', title: 'Could not open the data folder', message: errorMessage(err, 'The saved data folder is not available.') });
    }
  }

  async function keepWorking() {
    try {
      await api.app.keepWorking();
      quitOpen = false;
    } catch (err) {
      modal.set({ kind: 'error', title: 'Could not dismiss quit confirmation', message: errorMessage(err, 'Try again.') });
    }
  }

  $: {
    const running = $jobs.find((job) => job.status === 'active');
    const title = running
      ? `Downloading “${(running.title || 'video').slice(0, 42)}” · ${Math.round(progressOf(running) * 100)}%`
      : 'VidStow';
    document.title = title;
    window.runtime?.WindowSetTitle?.(title);
  }

  function acceptDrop(event: DragEvent) {
    event.preventDefault();
  }

  function handleDrop(event: DragEvent) {
    event.preventDefault();
    const text = event.dataTransfer?.getData('text/uri-list') || event.dataTransfer?.getData('text/plain') || '';
    const found = youtubeUrlFromText(text);
    if (!found) {
      showBanner('warning', 'Drop a public YouTube video, Short, or playlist link to analyze it.');
      return;
    }
    pendingUrl.set(found);
    route.set('home');
  }

  async function pauseAndQuit() {
    try {
      await api.app.pauseAndQuit();
      quitOpen = false;
    } catch (err) {
      modal.set({ kind: 'error', title: 'Could not pause downloads', message: errorMessage(err, 'Keep VidStow open and try again.') });
    }
  }
</script>

<svelte:window on:dragover={acceptDrop} on:drop={handleDrop} />

{#if startupStatus?.mode === 'recovery-required'}
  <main class="main">
    <div class="scroll">
      <RecoveryRequiredShell
        model={recoveryModel}
        onCopyDiagnostics={copyRecoveryDiagnostics}
        onOpenDataFolder={openRecoveryDataFolder}
      />
    </div>
  </main>
{:else}
  <Sidebar />

  <main class="main">
    <div class="scroll">
      {#if $route === 'home'}
        <Home on:goto={(e) => navigate(e.detail)} />
      {:else if $route === 'queue'}
        <Queue />
      {:else if $route === 'downloads'}
        <Downloads />
      {:else if $route === 'settings'}
        <Settings />
      {:else if $route === 'about'}
        <About />
      {/if}
    </div>
  </main>
{/if}

<Modal />
<Banner />
<QuitConfirmationDialog
  open={quitOpen}
  model={quitModel}
  onClose={keepWorking}
  onKeepWorking={keepWorking}
  onPauseAndQuit={pauseAndQuit}
/>

<style>
  .main {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    background:
      radial-gradient(900px 420px at 0% 0%, rgba(47,111,237,0.045), transparent 60%),
      var(--surface-bg);
  }
  .scroll {
    flex: 1;
    overflow-y: auto;
  }
</style>
