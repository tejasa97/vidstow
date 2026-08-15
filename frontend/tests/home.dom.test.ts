import { render, screen, waitFor } from '@testing-library/svelte';
import '@testing-library/jest-dom/vitest';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, test, vi } from 'vitest';
import { get } from 'svelte/store';

import Home from '../src/pages/Home.svelte';
import { pendingUrl, settings } from '../src/lib/stores.js';

const firstURL = 'https://www.youtube.com/watch?v=fixture0001';

function videoSummary(raw: string) {
  return {
    title: 'Fixture video', channel: 'Fixture channel', duration: '1:00', thumbnail: '', videoId: 'fixture0001', url: raw,
    durationSeconds: 60, viewCount: 1, uploadDate: '', description: '', access: { code: 'public', label: 'Public' },
    plans: [
      { id: 'video', kind: 'video', label: 'Video plan', container: 'mp4', available: true, recommended: true },
      { id: 'audio', kind: 'audio', label: 'Audio plan', container: 'm4a', available: true },
    ],
  };
}

function installBindings() {
  const ValidateURL = vi.fn(async (raw: string) => ({
    kind: 'single_video', url: raw, videoUrl: raw, playlistUrl: '', videoId: 'fixture0001', playlistId: '',
  }));
  const AnalyzeURL = vi.fn(async (raw: string) => videoSummary(raw));
  (window as any).go = { main: { App: { ValidateURL, AnalyzeURL } } };
  return { ValidateURL, AnalyzeURL };
}

describe('Home analysis authority', () => {
  beforeEach(() => {
    pendingUrl.set('');
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

  test('re-analyzes a URL dropped while that URL is already in the field', async () => {
    const { AnalyzeURL } = installBindings();
    render(Home);

    const input = screen.getByLabelText('YouTube video or playlist URL');
    await userEvent.setup().type(input, firstURL);
    pendingUrl.set(firstURL);

    await waitFor(() => expect(AnalyzeURL).toHaveBeenCalledWith(firstURL));
    expect(input).toHaveValue(firstURL);
    expect(get(pendingUrl)).toBe('');
  });

  test('does not publish an in-flight result after the URL changes', async () => {
    const user = userEvent.setup();
    let finishAnalysis!: (result: ReturnType<typeof videoSummary>) => void;
    const AnalyzeURL = vi.fn(() => new Promise<ReturnType<typeof videoSummary>>((resolve) => { finishAnalysis = resolve; }));
    (window as any).go.main.App.AnalyzeURL = AnalyzeURL;
    render(Home);

    const input = screen.getByLabelText('YouTube video or playlist URL');
    await user.type(input, firstURL);
    await user.click(screen.getByRole('button', { name: 'Analyze' }));
    await waitFor(() => expect(AnalyzeURL).toHaveBeenCalledOnce());

    const secondURL = 'https://www.youtube.com/watch?v=fixture0002';
    await user.clear(input);
    await user.type(input, secondURL);
    finishAnalysis(videoSummary(firstURL));

    await waitFor(() => expect(screen.queryByText('Fixture video')).not.toBeInTheDocument());
    expect(input).toHaveValue(secondURL);
    expect(screen.getByRole('button', { name: 'Analyze' })).toBeEnabled();
  });

  test('does not enable playlist admission without a destination', async () => {
    const user = userEvent.setup();
    const playlistURL = 'https://www.youtube.com/playlist?list=PLfixture';
    settings.update((current) => ({ ...current, downloadFolder: '' }));
    (window as any).go.main.App.ValidateURL = vi.fn(async () => ({
      kind: 'playlist', url: playlistURL, playlistUrl: playlistURL, playlistId: 'PLfixture',
    }));
    (window as any).go.main.App.AnalyzePlaylist = vi.fn(async () => ({
      id: 'PLfixture', url: playlistURL, title: 'Fixture playlist', channel: 'Fixture channel', thumbnail: '',
      entryCount: 1, available: 1, unavailable: 0,
      entries: [{ index: 1, videoId: 'fixture0001', url: firstURL, title: 'First video', available: true }],
    }));
    render(Home);

    await user.type(screen.getByLabelText('YouTube video or playlist URL'), playlistURL);
    await user.click(screen.getByRole('button', { name: 'Analyze' }));

    expect(await screen.findByText('Fixture playlist')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Add 1 Video to Queue' })).toBeDisabled();
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
