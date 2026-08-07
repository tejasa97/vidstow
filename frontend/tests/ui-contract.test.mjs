import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

const read = (path) => readFile(new URL(path, import.meta.url), 'utf8');

test('approved navigation and window branding are used', async () => {
  const [sidebar, main] = await Promise.all([
    read('../src/lib/components/Sidebar.svelte'),
    read('../../main.go'),
  ]);
  for (const label of ['Home', 'Queue', 'Downloads', 'Settings']) assert.match(sidebar, new RegExp(`label: '${label}'`));
  for (const rejected of ['v0 · single video', 'Single public YouTube videos only', 'brand-name', 'class="logo"']) assert.doesNotMatch(sidebar, new RegExp(rejected));
  assert.match(main, /Title:\s+"VidStow"/);
  assert.match(await read('../index.html'), /<title>VidStow<\/title>/);
});

test('page titles and controls match the approved redesign', async () => {
  const [home, queue, downloads, settings] = await Promise.all([
    read('../src/pages/Home.svelte'), read('../src/pages/Queue.svelte'),
    read('../src/pages/Downloads.svelte'), read('../src/pages/Settings.svelte'),
  ]);
  assert.match(home, /<h1 id="home-title">Download from YouTube<\/h1>/);
  assert.match(home, /Paste a public video URL to analyze it and choose your download\./);
  assert.match(home, />Choose Download</);
  assert.match(home, />Add to Queue</);
  assert.match(home, /'Analyze'/);
  assert.match(queue, />Pause All</);
  assert.match(queue, /Jobs are saved automatically\./);
  assert.match(queue, /Clear Completed/);
  assert.match(downloads, /View your recently downloaded items\./);
  assert.match(downloads, /placeholder="Search downloads…"/);
  assert.match(settings, /Configure downloads, queue behavior, and external tools\./);
  assert.match(settings, />Default download folder</);
  assert.match(settings, />FFmpeg path</);
  assert.match(settings, />Diagnostics</);
  assert.match(settings, />Copy Diagnostics</);
  assert.match(settings, /await api\.diagnostics\.copy\(\)/);
});

test('analysis failures use the redesigned error modal', async () => {
  const home = await read('../src/pages/Home.svelte');
  assert.match(home, /title: 'Unsupported URL'/);
  assert.match(home, /kind: 'error'/);
  assert.match(home, /message: errorMessage\(err,/);
  assert.doesNotMatch(home, /catch \(err\) \{\s*unsupported = \{\s*url: result\.url/);
});

test('unsupported and FFmpeg-required states retain accurate product copy', async () => {
  const [home, modal] = await Promise.all([
    read('../src/pages/Home.svelte'), read('../src/lib/components/Modal.svelte'),
  ]);
  assert.match(home, /valid, publicly accessible YouTube video/);
  assert.doesNotMatch(home, /supports YouTube videos and playlists/);
  assert.match(home, /title: 'FFmpeg Required'/);
  assert.match(home, /choose an original audio option/);
  assert.match(modal, /VidStow will continue to offer outputs that do not need FFmpeg\./);
  assert.match(modal, /class:primary=\{action\.primary\}/);
});

test('startup performs one FFmpeg fetch and normalizes binding errors', async () => {
  const [app, stores] = await Promise.all([
    read('../src/App.svelte'),
    read('../src/lib/stores.ts'),
  ]);
  assert.equal((app.match(/api\.ffmpeg\.status\(\)/g) || []).length, 1);
  assert.match(app, /title: 'The app could not finish starting'/);
  assert.match(app, /api\.events\.onJobUpdate\(updateJobInList\)/);
  assert.match(app, /if \(idx === -1\) return \[updated, \.\.\.list\]/);
  assert.match(stores, /'message' in err/);
  assert.match(stores, /return fallback/);
});

test('terminal queue rows expose recovery and removal actions', async () => {
  const row = await read('../src/lib/components/ProgressRow.svelte');
  assert.match(row, /<article class="job-card"/);
  assert.doesNotMatch(row, /role="cell"/);
  assert.doesNotMatch(row, /<t[rd][^>]*>/);
  for (const action of ['Cancel download', 'Open downloaded file', 'Retry download', 'Remove download']) {
    assert.match(row, new RegExp(`<button[^>]+type="button"[^>]+aria-label="${action}"`));
  }
  assert.match(row, /aria-label="Pause download"/);
  assert.match(row, /aria-label="Resume download"/);
  for (const method of ['cancel', 'retry', 'remove', 'pause', 'resume']) assert.match(row, new RegExp(`api\\.jobs\\.${method}\\(job\\.id\\)`));
  assert.match(row, />Retry</);
  assert.match(row, /job\.status === 'failed'/);
});

test('download history actions remain native accessible buttons', async () => {
  const downloads = await read('../src/pages/Downloads.svelte');
  assert.doesNotMatch(downloads, /role="(?:table|row|cell)"/);
  assert.match(downloads, /<button[^>]+aria-label="Open downloaded file"/);
  assert.match(downloads, /<button[^>]+aria-label="Show in Finder"/);
  assert.match(downloads, /await api\.fs\.open\(entry\.absolutePath\)/);
  assert.match(downloads, /await api\.fs\.reveal\(entry\.absolutePath\)/);
});
