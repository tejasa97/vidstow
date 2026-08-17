<script lang="ts">
  import { onMount } from 'svelte';
  import { api } from '../lib/api.js';
  import { showBanner, showError } from '../lib/stores.js';
  import { formatEngineVersion } from '../lib/format.js';
  import type { BuildInfo } from '../lib/types.js';

  const APP = {
    name: 'VidStow',
    tagline: 'A tidy YouTube video, Short, playlist, and channel downloader for the desktop.',
    description:
      'VidStow is an open-source desktop app that downloads public YouTube videos, Shorts, playlists, channels, and audio. ' +
      'Analyze a URL, choose the items and output, and let the queue run — no account or tracking required.',
    license: 'Apache-2.0',
    source: 'https://github.com/vidstow/vidstow',
    docs: 'https://github.com/vidstow/vidstow#readme',
  };

  const builtWith: Array<{ name: string; url: string }> = [
    { name: 'Go', url: 'https://go.dev' },
    { name: 'Wails', url: 'https://wails.io' },
    { name: 'Svelte', url: 'https://svelte.dev' },
    { name: 'yt-dlp', url: 'https://github.com/yt-dlp/yt-dlp' },
    { name: 'FFmpeg', url: 'https://ffmpeg.org' },
  ];

  let build: BuildInfo = {
    version: 'Loading…', engineVersion: 'Loading…', os: '', architecture: '', goVersion: '',
  };

  onMount(async () => {
    try { build = await api.app.buildInfo(); }
    catch (err) { showError(err, 'Could not read build information'); }
  });

  function open(url: string) {
    if (url) window.runtime?.BrowserOpenURL?.(url);
  }

  async function copyDiagnostics() {
    try { await api.diagnostics.copy(); showBanner('info', 'Diagnostics copied'); }
    catch (err) { showError(err, 'Could not copy diagnostics'); }
  }
</script>

<section class="page" aria-labelledby="about-title">
  <header class="page-header">
    <h1 id="about-title">About</h1>
    <p>{APP.tagline}</p>
  </header>

  <section class="group" aria-labelledby="app-title">
    <h2 id="app-title">{APP.name}</h2>
    <p class="lede">{APP.description}</p>

    <dl class="facts">
      <div>
        <dt>Version</dt>
        <dd>{build.version} · {APP.license}</dd>
      </div>
      <div>
        <dt>Engine</dt>
        <dd>youtube_dlp {formatEngineVersion(build.engineVersion)}</dd>
      </div>
      <div>
        <dt>Platform</dt>
        <dd>{build.os && build.architecture ? `${build.os}/${build.architecture}` : 'Loading…'}</dd>
      </div>
    </dl>

    <div class="setting">
      <div class="copy">
        <strong>Source</strong>
        <span>Open source on GitHub, with setup notes in the readme.</span>
      </div>
      <div class="actions">
        <button type="button" class="app-btn" on:click={() => open(APP.source)}>View source</button>
        <button type="button" class="app-btn primary" on:click={() => open(APP.docs)}>Read the docs</button>
      </div>
    </div>
    <div class="setting">
      <div class="copy">
        <strong>Support report</strong>
        <span>Includes app version and FFmpeg status. Paths stay private.</span>
      </div>
      <div class="actions">
        <button type="button" class="app-btn primary" on:click={copyDiagnostics}>Copy Diagnostics</button>
      </div>
    </div>
  </section>

  <section class="group" aria-labelledby="stack-title">
    <h2 id="stack-title">Built with open source</h2>
    <div class="setting">
      <div class="copy">
        <strong>Dependencies</strong>
        <span>VidStow depends on these projects.</span>
      </div>
      <ul class="stack">
        {#each builtWith as tool}
          <li>
            <button type="button" class="app-btn" on:click={() => open(tool.url)}>{tool.name}</button>
          </li>
        {/each}
      </ul>
    </div>
  </section>

  <section class="group" aria-labelledby="legal-title">
    <h2 id="legal-title">Legal</h2>
    <p class="legal">
      VidStow is distributed under the Apache License 2.0. It is not affiliated with, or endorsed by,
      YouTube or Google. Video and audio content is downloaded only where you have the right to do so —
      please respect each creator's terms and local laws. FFmpeg, yt-dlp, Go, Wails, and Svelte are
      independent open-source projects with their own licenses.
    </p>
  </section>

</section>

<style>
  .group {
    padding: 2px 20px 8px;
    border: 1px solid var(--border-default);
    border-radius: var(--r-lg);
    background: var(--surface-raised);
    box-shadow: var(--shadow-card);
  }
  .group h2 {
    margin: 0;
    padding: 12px 0 2px;
    font-size: var(--fs-xs);
    font-weight: 650;
    letter-spacing: 0.04em;
    text-transform: uppercase;
    color: var(--text-muted);
  }
  .lede, .legal {
    margin: 8px 0 10px;
    color: var(--text-secondary);
    font-size: var(--fs-sm);
    line-height: 1.5;
  }
  .facts {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: var(--sp-4);
    margin: 8px 0 4px;
    padding: 10px 0 12px;
    border-top: 1px solid var(--border-subtle);
  }
  .facts dt {
    color: var(--text-muted);
    font-size: var(--fs-xs);
    font-weight: 650;
    letter-spacing: 0.02em;
  }
  .facts dd {
    margin: 4px 0 0;
    color: var(--text-primary);
    font-size: var(--fs-sm);
    overflow-wrap: anywhere;
  }

  .setting {
    min-height: 48px;
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--sp-4);
    padding: 8px 0;
    border-top: 1px solid var(--border-subtle);
  }
  .group > h2 + .setting, .group > h2 + .lede + .setting { border-top: 0; }
  .copy { min-width: 0; flex: 1; }
  .copy strong, .copy span { display: block; }
  .copy strong { font-size: var(--fs-sm); color: var(--text-primary); font-weight: 600; }
  .copy span {
    margin-top: 4px;
    color: var(--text-secondary);
    font-size: var(--fs-xs);
    line-height: 1.45;
  }

  .actions {
    display: flex;
    align-items: center;
    justify-content: flex-end;
    gap: var(--sp-2);
    flex-shrink: 0;
  }
  .stack {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-wrap: wrap;
    justify-content: flex-end;
    gap: var(--sp-2);
  }
  @media (max-width: 720px) {
    .setting { flex-direction: column; align-items: flex-start; }
    .facts { grid-template-columns: 1fr; gap: var(--sp-3); }
    .actions, .stack { width: 100%; justify-content: flex-start; }
  }
</style>
