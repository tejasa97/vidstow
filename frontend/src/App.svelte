<script lang="ts">
  import { onDestroy, onMount } from 'svelte';
  import { get } from 'svelte/store';
  import { api } from './lib/api.js';
  import { errorMessage, ffmpeg, history, jobs, queueView, route, settings, modal, persistence, pendingUrl, showBanner } from './lib/stores.js';
  import { progressOf, youtubeUrlFromText } from './lib/format.js';
  import type { QueueView } from './lib/lifecycle-ui/types.js';
  import { newestQueueView } from './lib/queue-view.js';
  import type { JobSnapshot, QuitSummary, StartupStatus } from './lib/types.js';
  import { DEFAULT_RECOVERY_REQUIRED, type RecoveryRequiredViewModel } from './lib/lifecycle-ui/types.js';
  import QuitConfirmationDialog from './lib/lifecycle-ui/QuitConfirmationDialog.svelte';
  import DiagnosticConsentDialog from './lib/lifecycle-ui/DiagnosticConsentDialog.svelte';
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
  let diagnosticChoiceOpen = false;
  let diagnosticChoiceSaving = false;
  let showFFmpegAfterDiagnosticChoice = false;

  onMount(async () => {
    unsubAll = [
      api.events.onJobUpdate(updateJobInList),
      api.events.onQueue((list) => jobs.set(list ?? [])),
      api.events.onQueueView(applyQueueView),
      api.events.onJobQueueView(applyQueueView),
      api.events.onHistory((entries) => history.set(entries ?? [])),
      api.events.onSettings((value) => settings.set(value)),
      api.events.onFFmpeg((status) => {
        ffmpeg.set(status);
        if (status?.available && get(modal)?.kind === 'ffmpeg-missing') modal.set(null);
      }),
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
      startupStatus = await waitForStartupStatus();
      if (startupStatus.mode === 'recovery-required') {
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
      if (!savedSettings.automaticDiagnostics) {
        diagnosticChoiceOpen = true;
        showFFmpegAfterDiagnosticChoice = !ffmpegStatus.available;
      } else if (!ffmpegStatus.available) {
        showFFmpegRequired();
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

  // Never pass browser error/rejection data across the backend boundary: it
  // can contain a URL, user input, or stack trace. The backend records only a
  // fixed frontend_unhandled category and applies the session caps.
  function reportFrontendFailure(): void {
    void api.diagnostics.frontendFailure().catch(() => {});
  }

  async function waitForStartupStatus(): Promise<StartupStatus> {
    // Wails can bind frontend calls before dispatching App.startup. Polling the
    // explicit non-terminal status leaves that callback free to run.
    for (;;) {
      const status = await api.app.startupStatus();
      if (status.mode !== 'starting') return status;
      await new Promise((resolve) => window.setTimeout(resolve, 50));
    }
  }

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

  function showFFmpegRequired() {
    modal.set({
      kind: 'ffmpeg-missing',
      title: 'FFmpeg is required for most downloads',
      message: 'Install FFmpeg or point VidStow at it in Settings. You can still queue original audio that does not need merging.',
      actions: [{ label: 'Open Settings', primary: true, action: () => navigate('settings') }],
    });
  }

  function closeDiagnosticChoice() {
    diagnosticChoiceOpen = false;
    if (showFFmpegAfterDiagnosticChoice) showFFmpegRequired();
    showFFmpegAfterDiagnosticChoice = false;
  }

  async function chooseAutomaticDiagnostics(value: 'enabled' | 'disabled') {
    if (diagnosticChoiceSaving) return;
    diagnosticChoiceSaving = true;
    try {
      const saved = await api.settings.setAutomaticDiagnostics(value);
      settings.set(saved);
      closeDiagnosticChoice();
    } catch (err) {
      try { settings.set(await api.settings.get()); } catch { /* retain the last known value */ }
      showBanner('danger', errorMessage(err, 'Could not save the diagnostics preference. Automatic sending remains off.'));
    } finally {
      diagnosticChoiceSaving = false;
    }
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

<svelte:window on:dragover={acceptDrop} on:drop={handleDrop} on:error={reportFrontendFailure} on:unhandledrejection={reportFrontendFailure} />

{#if startupStatus === null}
  <main class="main startup-shell" aria-busy="true" aria-label="Starting VidStow">
    <div class="startup-indicator">
      <span class="startup-spinner" aria-hidden="true"></span>
      <p>Restoring saved downloads…</p>
    </div>
  </main>
{:else if startupStatus.mode === 'recovery-required'}
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
<DiagnosticConsentDialog
  open={diagnosticChoiceOpen}
  busy={diagnosticChoiceSaving}
  onClose={closeDiagnosticChoice}
  onEnable={() => chooseAutomaticDiagnostics('enabled')}
  onDisable={() => chooseAutomaticDiagnostics('disabled')}
  onPrivacy={() => window.runtime.BrowserOpenURL('https://diagnostics.vidstow.workers.dev/privacy')}
/>
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
  .startup-shell {
    align-items: center;
    justify-content: center;
  }
  .startup-indicator {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: var(--sp-3);
    color: var(--text-muted);
  }
  .startup-indicator p { margin: 0; }
  .startup-spinner {
    width: 28px;
    height: 28px;
    border: 2px solid var(--border-default);
    border-top-color: var(--accent-400);
    border-radius: 50%;
    animation: startup-spin 0.8s linear infinite;
  }
  @keyframes startup-spin { to { transform: rotate(360deg); } }
  @media (prefers-reduced-motion: reduce) {
    .startup-spinner { animation: none; }
  }
</style>
