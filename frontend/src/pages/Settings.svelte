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
    try { ffmpeg.set(await api.ffmpeg.probe()); }
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
  <header><h1 id="settings-title">Settings</h1><p>Configure downloads, queue behavior, and external tools.</p></header>

  <section class="group" aria-labelledby="general-title">
    <h2 id="general-title">General</h2>
    <div class="setting path-setting">
      <div><strong>Default download folder</strong><span>New downloads are saved here.</span></div>
      <div class="path"><input readonly value={folder} title={folder} /><button type="button" on:click={pickFolder}>Change…</button></div>
    </div>
    <label class="setting check"><span><strong>Create a subfolder for each download</strong><small>Places all files for one video together.</small></span><input type="checkbox" checked={$settings.perVideoSubfolder} on:change={(e) => update({ ...$settings, perVideoSubfolder: e.currentTarget.checked })} /></label>
    <label class="setting check"><span><strong>Confirm before starting downloads</strong><small>Shows the selected output before adding it to the queue.</small></span><input type="checkbox" checked={$settings.confirmBeforeDownload} on:change={(e) => update({ ...$settings, confirmBeforeDownload: e.currentTarget.checked })} /></label>
    <QueueSettingsCard
      model={{ concurrency: $settings.downloadConcurrency, minimum: 1, maximum: 10, defaultValue: 2, disabled: saving }}
      onConcurrencyChange={changeConcurrency}
    />
    {#if $settings.downloadConcurrency > 4}<p class="warning">More than 4 simultaneous downloads may reduce stability or trigger rate limits.</p>{/if}
  </section>

  <section class="group" aria-labelledby="ffmpeg-title">
    <h2 id="ffmpeg-title">FFmpeg</h2>
    <div class="setting status"><div><strong>FFmpeg status</strong><span>Used for stream merging and audio conversion.</span></div><em class:detected={$ffmpeg.available}>{$ffmpeg.available ? 'Detected' : 'Not found'}</em></div>
    <div class="setting path-setting"><div><strong>FFmpeg path</strong><span>Choose an installed FFmpeg executable.</span></div><div class="path"><input readonly value={displayedFFmpegPath} placeholder="Not configured" /><button type="button" on:click={locateFFmpeg}>Change…</button></div></div>
    <div class="tool-actions"><button type="button" on:click={recheck}>Recheck</button><button type="button" on:click={() => window.runtime.BrowserOpenURL('https://ffmpeg.org/download.html')}>Installation guide ↗</button></div>
  </section>

  <section class="group diagnostics"><div><h2>Diagnostics</h2><p>Copy a privacy-safe summary for troubleshooting.</p></div><button type="button" on:click={copyDiagnostics}>Copy Diagnostics</button></section>
</section>

<style>
  .page{width:min(100%,900px);margin:0 auto;padding:34px 42px 50px}.page>header h1{margin:0;font-size:26px}.page>header p{margin:7px 0 26px;color:var(--text-secondary)}
  .group{margin-top:20px;padding:18px 20px;border:1px solid var(--border-default);border-radius:8px;background:var(--surface-raised)}.group>h2,.diagnostics h2{margin:0 0 14px;font-size:14px}
  .setting{min-height:58px;display:flex;align-items:center;justify-content:space-between;gap:28px;padding:12px 0;border-top:1px solid var(--border-subtle)}.group h2+.setting{border-top:0}.setting strong,.setting span,.setting small{display:block}.setting strong{font-size:13px;color:var(--text-primary)}.setting span,.setting small{margin-top:4px;color:var(--text-muted);font-size:11px}.setting>div:first-child,.setting>span{min-width:240px}
  .path-setting{display:grid;grid-template-columns:260px minmax(0,1fr)}.path{display:grid;grid-template-columns:minmax(0,1fr) auto;gap:8px}.path input{height:36px;min-width:0}.path button,.tool-actions button,.diagnostics button{min-height:34px;padding:0 12px;border:1px solid var(--border-default);border-radius:6px;background:var(--surface-raised);color:var(--text-primary)}
  .check{cursor:pointer}.check input{width:16px;height:16px;order:-1;flex:0 0 auto}.check>span{flex:1}.warning{margin:10px 0 0;padding:10px 12px;border-radius:6px;background:#fff7ed;color:#9a3412;font-size:12px}
  .status em{padding:4px 8px;border-radius:999px;background:#fef2f2;color:#b91c1c;font-style:normal;font-size:11px}.status em.detected{background:#ecfdf5;color:#047857}.tool-actions{display:flex;justify-content:flex-end;gap:8px;padding-top:12px;border-top:1px solid var(--border-subtle)}
  .diagnostics{display:flex;align-items:center;justify-content:space-between}.diagnostics h2{margin-bottom:5px}.diagnostics p{margin:0;color:var(--text-muted);font-size:12px}
  @media(max-width:700px){.page{padding:24px 18px}.path-setting{grid-template-columns:1fr}.setting{align-items:flex-start}.diagnostics{align-items:flex-start;flex-direction:column}}
</style>
