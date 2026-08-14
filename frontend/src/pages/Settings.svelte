<script lang="ts">
  import { onMount } from 'svelte';
  import { api } from '../lib/api.js';
  import { settings, ffmpeg, showBanner, showError } from '../lib/stores.js';
  import type { Settings } from '../lib/types.js';
  import QueueSettingsCard from '../lib/lifecycle-ui/QueueSettingsCard.svelte';

  let folder = '';
  let ffmpegPath = '';
  let saving = false;

  onMount(() => {
    folder = $settings.downloadFolder || '';
    ffmpegPath = $settings.ffmpegPath || $ffmpeg.path || '';
  });
  $: displayedFFmpegPath = ffmpegPath || $ffmpeg.path || '';
  $: concurrency = $settings.downloadConcurrency;
  $: ffmpegVersion = ($ffmpeg.version || '').replace(/^ffmpeg version /i, '').split(/\s+/)[0] || '';

  async function update(next: Settings, message = 'Settings updated') {
    saving = true;
    try {
      const saved = await api.settings.update(next);
      settings.set(saved);
      showBanner('success', message);
    } catch (err) { showError(err, 'Could not save settings'); }
    finally { saving = false; }
  }

  async function pickFolder() {
    try {
      const path = await api.folder.pick();
      if (!path) return;
      folder = path;
      await update({ ...$settings, downloadFolder: path }, 'Download folder updated');
    } catch (err) { showError(err, 'Could not choose folder'); }
  }

  async function showFolder() {
    if (!folder) return;
    try { await api.fs.reveal(folder); }
    catch (err) { showError(err, 'Could not show the folder in Finder'); }
  }

  async function locateFFmpeg() {
    try {
      const path = await api.ffmpeg.pickPath();
      if (!path) return;
      const status = await api.ffmpeg.configure(path);
      ffmpeg.set(status);
      ffmpegPath = status.path;
      showBanner('success', 'FFmpeg configured');
    } catch (err) { showError(err, 'Could not configure FFmpeg'); }
  }

  async function recheck() {
    try { ffmpeg.set(await api.ffmpeg.probe()); showBanner('info', $ffmpeg.available ? 'FFmpeg is ready' : 'FFmpeg was not found'); }
    catch (err) { showError(err, 'Could not check FFmpeg'); }
  }

  async function copyDiagnostics() {
    try { await api.diagnostics.copy(); showBanner('info', 'Diagnostics copied'); }
    catch (err) { showError(err, 'Could not copy diagnostics'); }
  }

  async function changeConcurrency(value: number) {
    await update({ ...$settings, downloadConcurrency: value });
  }
</script>

<section class="page" aria-labelledby="settings-title">
  <header class="page-header">
    <h1 id="settings-title">Settings</h1>
    <p>Configure downloads, queue behavior, and external tools.</p>
  </header>

  <section class="group" aria-labelledby="general-title">
    <header class="group-head">
      <h2 id="general-title">Downloads</h2>
      <p>Where files go, and how the queue runs.</p>
    </header>

    <div class="setting">
      <div class="copy">
        <strong>Default download folder</strong>
        <span>New videos and playlists are saved here.</span>
      </div>
      <div class="path">
        <code class="chip" title={folder}>{folder || 'Not set'}</code>
        <button type="button" disabled={!folder} on:click={showFolder}>Show in Finder</button>
        <button type="button" on:click={pickFolder}>Change…</button>
      </div>
    </div>

    <label class="setting">
      <span class="copy">
        <strong>Create a subfolder for each download</strong>
        <small>Places all files for one video together.</small>
      </span>
      <input type="checkbox" checked={$settings.perVideoSubfolder} on:change={(e) => update({ ...$settings, perVideoSubfolder: e.currentTarget.checked })} />
    </label>

    <label class="setting">
      <span class="copy">
        <strong>Confirm before starting downloads</strong>
        <small>Shows the selected output before adding it to the queue.</small>
      </span>
      <input type="checkbox" checked={$settings.confirmBeforeDownload} on:change={(e) => update({ ...$settings, confirmBeforeDownload: e.currentTarget.checked })} />
    </label>

    <QueueSettingsCard
      model={{ concurrency, minimum: 1, maximum: 10, defaultValue: 2, disabled: saving }}
      onConcurrencyChange={changeConcurrency}
    />
    {#if concurrency > 4}
      <p class="warning">More than 4 simultaneous downloads may reduce stability or trigger rate limits.</p>
    {/if}
  </section>

  <section class="group" aria-labelledby="ffmpeg-title">
    <header class="group-head">
      <h2 id="ffmpeg-title">FFmpeg</h2>
      <p>Needed to merge video and audio, and to convert to MP3.</p>
    </header>

    <div class="setting">
      <div class="copy">
        <strong>FFmpeg status</strong>
        <span>
          {#if $ffmpeg.available && ffmpegVersion}
            Version {ffmpegVersion} · ready for merging and MP3 conversion
          {:else}
            Used for stream merging and audio conversion.
          {/if}
        </span>
      </div>
      <div class="status-actions">
        <em class="badge" class:ok={$ffmpeg.available}>{$ffmpeg.available ? 'Ready' : 'Not found'}</em>
        <button type="button" on:click={recheck}>Recheck</button>
      </div>
    </div>

    <div class="setting">
      <div class="copy">
        <strong>FFmpeg path</strong>
        <span>Choose an installed FFmpeg executable.</span>
      </div>
      <div class="path">
        <code class="chip" class:empty={!displayedFFmpegPath} title={displayedFFmpegPath}>{displayedFFmpegPath || 'Not configured'}</code>
        <button type="button" on:click={locateFFmpeg}>Change…</button>
      </div>
    </div>

    {#if !$ffmpeg.available}
      <div class="setting hint">
        <p>Install FFmpeg, then Recheck or choose the binary with Change…</p>
        <button type="button" on:click={() => window.runtime.BrowserOpenURL('https://ffmpeg.org/download.html')}>Installation guide ↗</button>
      </div>
    {/if}
  </section>

  <section class="group" aria-labelledby="diagnostics-title">
    <header class="group-head">
      <h2 id="diagnostics-title">Diagnostics</h2>
      <p>Copy a privacy-safe summary for troubleshooting.</p>
    </header>
    <div class="setting">
      <div class="copy">
        <strong>Support report</strong>
        <span>Includes app version and FFmpeg status. Paths stay private.</span>
      </div>
      <button type="button" on:click={copyDiagnostics}>Copy Diagnostics</button>
    </div>
  </section>
</section>

<style>
  .group {
    margin-top: 0;
    padding: 4px 20px 8px;
    border: 1px solid var(--border-default);
    border-radius: var(--r-lg);
    background: var(--surface-raised);
    box-shadow: var(--shadow-card);
  }
  .group + .group { margin-top: 16px; }
  .group-head {
    padding: 16px 0 10px;
    border-bottom: 1px solid var(--border-subtle);
  }
  .group-head h2 {
    margin: 0;
    font-size: var(--fs-xs);
    font-weight: 650;
    letter-spacing: 0.04em;
    text-transform: uppercase;
    color: var(--text-secondary);
  }
  .group-head p {
    margin: 4px 0 0;
    color: var(--text-secondary);
    font-size: var(--fs-sm);
  }

  .setting {
    min-height: 64px;
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 24px;
    padding: 14px 0;
    border-top: 1px solid var(--border-subtle);
  }
  .group-head + .setting { border-top: 0; }
  .copy {
    min-width: 0;
    flex: 1;
  }
  .copy strong, .copy span, .copy small { display: block; }
  .copy strong { font-size: var(--fs-sm); color: var(--text-primary); }
  .copy span, .copy small {
    margin-top: 4px;
    color: var(--text-secondary);
    font-size: var(--fs-xs);
    line-height: 1.45;
  }
  label.setting { cursor: pointer; }
  label.setting input { margin-left: 8px; }

  .path, .status-actions {
    display: flex;
    align-items: center;
    justify-content: flex-end;
    gap: 8px;
    flex-shrink: 0;
    max-width: 58%;
  }
  .chip {
    min-width: 0;
    max-width: 280px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    padding: 7px 10px;
    border: 1px solid var(--border-default);
    border-radius: var(--r-md);
    background: var(--surface-subtle);
    color: var(--text-primary);
    font-family: var(--font-mono);
    font-size: 11px;
  }
  .chip.empty { color: var(--text-secondary); font-style: italic; font-family: var(--font-sans); }

  .setting button, .hint button {
    min-height: 34px;
    padding: 0 12px;
    border: 1px solid var(--border-default);
    border-radius: var(--r-md);
    background: var(--surface-base);
    color: var(--text-primary);
    font-size: var(--fs-xs);
    font-weight: 600;
    white-space: nowrap;
  }
  .setting button:hover:not(:disabled), .hint button:hover { background: var(--surface-hover); }

  .badge {
    padding: 4px 10px;
    border-radius: var(--r-full);
    background: var(--status-danger-soft);
    color: var(--status-danger);
    font-style: normal;
    font-size: 11px;
    font-weight: 650;
  }
  .badge.ok {
    background: var(--status-success-soft);
    color: var(--status-success);
  }

  .warning, .hint {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    margin: 0 0 12px;
    padding: 10px 12px;
    border-radius: var(--r-md);
    font-size: var(--fs-sm);
  }
  .warning {
    background: var(--status-warning-soft);
    color: var(--status-warning);
  }
  .hint {
    margin-top: 0;
    background: var(--status-info-soft);
    color: var(--text-primary);
    border-top: 0;
  }
  .hint p { margin: 0; color: var(--text-secondary); font-size: var(--fs-xs); }

  @media (max-width: 720px) {
    .setting, .hint { flex-direction: column; align-items: flex-start; }
    .path, .status-actions { max-width: 100%; width: 100%; justify-content: flex-start; flex-wrap: wrap; }
    .chip { max-width: 100%; }
  }
</style>
