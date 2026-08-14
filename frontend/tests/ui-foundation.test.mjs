import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

const read = (path) => readFile(new URL(path, import.meta.url), 'utf8');

test('design tokens match the approved paper-and-ink contract', async () => {
  const css = await read('../src/styles/global.css');
  assert.match(css, /--surface-bg:\s+#EDE6D8/);
  assert.match(css, /--surface-base:\s+#FFFBF5/);
  assert.match(css, /--surface-sidebar:\s+#1A1714/);
  assert.match(css, /--text-secondary:\s+#3F3933/);
  assert.match(css, /--text-muted:\s+#5C544C/);
  assert.match(css, /--sidebar-w:\s+160px/);
  assert.match(css, /--page-pad-x:\s+32px/);
  assert.match(css, /--page-pad-y:\s+28px/);
  assert.match(css, /\.page \{/);
  assert.match(css, /\.page-header h1 \{/);
  assert.match(css, /--r-sm:\s+6px/);
  assert.match(css, /--r-md:\s+8px/);
  assert.match(css, /--font-sans: 'Inter'/);
  assert.match(css, /--shadow-card: 0 1px 2px/);
  assert.match(css, /prefers-reduced-motion: reduce/);
  assert.match(css, /:focus-visible/);
});

test('branded sidebar includes FFmpeg footer and About navigation', async () => {
  const sidebar = await read('../src/lib/components/Sidebar.svelte');
  assert.match(sidebar, /class="brand"/);
  assert.match(sidebar, /import brandMark from '\.\.\/\.\.\/assets\/images\/brand-mark\.png'/);
  assert.match(sidebar, /<img src=\{brandMark\} alt="" width="26" height="26" \/>/);
  assert.match(sidebar, />VidStow</);
  assert.match(sidebar, /label: 'About'/);
  assert.match(sidebar, /class="ffmpeg"/);
  assert.match(sidebar, /\$ffmpeg\.available \? 'Ready' : 'Required'/);
  assert.match(sidebar, /BrowserOpenURL\?\.\('https:\/\/ffmpeg\.org\/download\.html'\)/);
});

test('app shell routes to the About page', async () => {
  const [app, stores] = await Promise.all([
    read('../src/App.svelte'),
    read('../src/lib/stores.ts'),
  ]);
  assert.match(app, /import About from '\.\/pages\/About\.svelte'/);
  assert.match(app, /\$route === 'about'/);
  assert.match(app, /navigate\(target: 'home' \| 'queue' \| 'downloads' \| 'settings' \| 'about'\)/);
  assert.match(stores, /writable<'home' \| 'queue' \| 'downloads' \| 'settings' \| 'about'>/);
});

test('About page uses backend build info with legal copy and external links', async () => {
  const about = await read('../src/pages/About.svelte');
  assert.match(about, /<h1 id="about-title">About<\/h1>/);
  assert.match(about, /name: 'VidStow'/);
  assert.match(about, /api\.app\.buildInfo\(\)/);
  assert.match(about, /engineVersion/);
  assert.match(about, /Copy Diagnostics/);
  assert.match(about, /license: 'Apache-2\.0'/);
  assert.match(about, /BrowserOpenURL\?\.\(url\)/);
  assert.match(about, /github\.com\/tejasa97\/vidstow/);
  assert.match(about, /yt-dlp/);
  assert.match(about, /not affiliated with, or endorsed by,/);
});

test('ui primitives exist and expose the expected API surface', async () => {
  const [button, card, tabs, indicator, progress, dialog, empty, index] = await Promise.all([
    read('../src/lib/components/ui/Button.svelte'),
    read('../src/lib/components/ui/Card.svelte'),
    read('../src/lib/components/ui/Tabs.svelte'),
    read('../src/lib/components/ui/StatusIndicator.svelte'),
    read('../src/lib/components/ui/Progress.svelte'),
    read('../src/lib/components/ui/DialogSurface.svelte'),
    read('../src/lib/components/ui/EmptyState.svelte'),
    read('../src/lib/components/ui/index.ts'),
  ]);
  assert.match(button, /type Variant = 'primary' \| 'secondary' \| 'ghost' \| 'danger'/);
  assert.match(button, /\['btn', `variant-\$\{variant\}`, `size-\$\{size\}`\]/);
  assert.match(button, /aria-busy/);
  assert.match(card, /<section class="card"/);
  assert.match(card, /{@render children\(\)}/);
  assert.match(tabs, /role="tablist"/);
  assert.match(tabs, /aria-selected/);
  assert.match(indicator, /data-tone={tone}/);
  assert.match(progress, /role="progressbar"/);
  assert.match(dialog, /role="dialog"/);
  assert.match(dialog, /aria-modal="true"/);
  assert.match(empty, /class="empty"/);
  for (const name of ['Button', 'Card', 'Tabs', 'StatusIndicator', 'Progress', 'DialogSurface', 'EmptyState']) {
    assert.match(index, new RegExp(`export \\{ default as ${name} \\}`));
  }
});
