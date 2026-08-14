import { render, screen, waitFor } from '@testing-library/svelte';
import '@testing-library/jest-dom/vitest';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, test, vi } from 'vitest';

import Home from '../src/pages/Home.svelte';
import { settings } from '../src/lib/stores.js';

const firstURL = 'https://www.youtube.com/watch?v=fixture0001';

function installBindings() {
  const ValidateURL = vi.fn(async (raw: string) => ({
    kind: 'single_video', url: raw, videoUrl: raw, playlistUrl: '', videoId: 'fixture0001', playlistId: '',
  }));
  const AnalyzeURL = vi.fn(async (raw: string) => ({
    title: 'Fixture video', channel: 'Fixture channel', duration: '1:00', thumbnail: '', videoId: 'fixture0001', url: raw,
    durationSeconds: 60, viewCount: 1, uploadDate: '', description: '', access: { code: 'public', label: 'Public' },
    plans: [
      { id: 'video', kind: 'video', label: 'Video plan', container: 'mp4', available: true, recommended: true },
      { id: 'audio', kind: 'audio', label: 'Audio plan', container: 'm4a', available: true },
    ],
  }));
  (window as any).go = { main: { App: { ValidateURL, AnalyzeURL } } };
  return { ValidateURL, AnalyzeURL };
}

describe('Home analysis authority', () => {
  beforeEach(() => {
    settings.update((current) => ({ ...current, downloadFolder: '/tmp/downloads', confirmBeforeDownload: false }));
    installBindings();
  });

  test('invalidates an analyzed result when the URL input changes', async () => {
    const user = userEvent.setup();
    render(Home);

    const input = screen.getByLabelText('YouTube video or playlist URL');
    await user.type(input, firstURL);
    await user.click(screen.getByRole('button', { name: 'Analyze' }));
    expect(await screen.findByText('Fixture video')).toBeInTheDocument();

    await user.clear(input);
    await user.type(input, 'https://www.youtube.com/watch?v=fixture0002');
    await waitFor(() => expect(screen.queryByText('Fixture video')).not.toBeInTheDocument());
    expect(screen.getByText('Add a YouTube link')).toBeInTheDocument();
  });

  test('switching output types selects a visible compatible plan', async () => {
    const user = userEvent.setup();
    render(Home);

    await user.type(screen.getByLabelText('YouTube video or playlist URL'), firstURL);
    await user.click(screen.getByRole('button', { name: 'Analyze' }));
    expect(await screen.findByText('Video plan')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Audio' }));
    expect(screen.getByText('Audio plan')).toBeInTheDocument();
    expect(screen.queryByText('Video plan')).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Add to Queue' })).toBeEnabled();
  });
});
