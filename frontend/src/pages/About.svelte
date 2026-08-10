<script lang="ts">
  import { onMount } from 'svelte';
  import { Card, Button } from '../lib/components/ui/index.js';
  import { api } from '../lib/api.js';
  import { showBanner, showError } from '../lib/stores.js';
  import type { BuildInfo } from '../lib/types.js';

  const APP = {
    name: 'VidStow',
    tagline: 'A tidy, single-video YouTube downloader for the desktop.',
    description:
      'VidStow is an open-source desktop app that downloads public YouTube videos and audio. ' +
      'Analyze a URL, pick a complete output, and let the queue run — no account or tracking required.',
    license: 'Apache-2.0',
    source: 'https://github.com/tejasa97/vidstow',
    docs: 'https://github.com/tejasa97/vidstow#readme',
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
    <p>About VidStow, its license, and the open-source tools that power it.</p>
  </header>

  <div class="grid">
    <Card title={APP.name} description={`Version ${build.version} · ${APP.license}`}>
      <div class="intro">
        <p class="tagline">{APP.tagline}</p>
        <p class="description">{APP.description}</p>
      </div>
      <dl class="build-info">
        <div><dt>Engine</dt><dd>youtube_dlp {build.engineVersion}</dd></div>
        <div><dt>Platform</dt><dd>{build.os && build.architecture ? `${build.os}/${build.architecture}` : 'Loading…'}</dd></div>
      </dl>
      <div class="actions">
        <Button variant="primary" label="View source" onclick={() => open(APP.source)}>View source</Button>
        <Button variant="secondary" label="Read the docs" onclick={() => open(APP.docs)}>Read the docs</Button>
        <Button variant="secondary" label="Copy Diagnostics" onclick={copyDiagnostics}>Copy Diagnostics</Button>
      </div>
    </Card>

    <Card title="Built with open source" description="VidStow depends on these projects.">
      <ul class="stack">
        {#each builtWith as tool}
          <li>
            <button type="button" class="link-button" onclick={() => open(tool.url)}>
              <span class="stack-name">{tool.name}</span>
              <svg viewBox="0 0 24 24" width="14" height="14" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M14 4h6v6M20 4l-9 9M18 13v7H4V6h7"/></svg>
            </button>
          </li>
        {/each}
      </ul>
    </Card>
  </div>

  <Card title="Legal">
    <p class="legal">
      VidStow is distributed under the Apache License 2.0. It is not affiliated with, or endorsed by,
      YouTube or Google. Video and audio content is downloaded only where you have the right to do so —
      please respect each creator's terms and local laws. FFmpeg, yt-dlp, Go, Wails, and Svelte are
      independent open-source projects with their own licenses.
    </p>
  </Card>
</section>

<style>
  .page {
    width: min(100%, 840px);
    margin: 0 auto;
    padding: var(--sp-6) var(--sp-5) var(--sp-8);
    display: flex;
    flex-direction: column;
    gap: var(--sp-4);
  }
  .page-header h1 { margin: 0; font-size: var(--fs-2xl); letter-spacing: -0.02em; }
  .page-header p { margin: var(--sp-2) 0 0; color: var(--text-secondary); font-size: var(--fs-md); }

  .grid {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: var(--sp-4);
  }

  .intro { display: flex; flex-direction: column; gap: var(--sp-2); }
  .tagline { margin: 0; font-size: var(--fs-md); font-weight: 600; color: var(--text-primary); }
  .description { margin: 0; color: var(--text-secondary); font-size: var(--fs-sm); line-height: 1.55; }
  .build-info { margin: var(--sp-3) 0 0; display: grid; gap: var(--sp-1); font-size: var(--fs-xs); color: var(--text-secondary); }
  .build-info div { display: flex; gap: var(--sp-2); }
  .build-info dt { color: var(--text-muted); }
  .build-info dd { margin: 0; overflow-wrap: anywhere; }
  .actions { display: flex; gap: var(--sp-2); margin-top: var(--sp-4); flex-wrap: wrap; }

  .stack { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; }
  .stack li { border-top: 1px solid var(--border-subtle); }
  .stack li:first-child { border-top: 0; }
  .link-button {
    width: 100%;
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--sp-2);
    padding: var(--sp-3) var(--sp-2);
    border-radius: var(--r-sm);
    color: var(--text-secondary);
    font-size: var(--fs-sm);
  }
  .link-button:hover { background: var(--surface-hover); color: var(--accent-600); }

  .legal { margin: 0; color: var(--text-secondary); font-size: var(--fs-sm); line-height: 1.6; }

  @media (max-width: 760px) {
    .grid { grid-template-columns: 1fr; }
  }
</style>
