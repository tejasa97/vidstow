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
  const AnalyzeBatchURLs = vi.fn();
  const StartBatchDownload = vi.fn();
  (window as any).go = { main: { App: { ValidateURL, AnalyzeURL, AnalyzeBatchURLs, StartBatchDownload } } };
  return { ValidateURL, AnalyzeURL, AnalyzeBatchURLs, StartBatchDownload };
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

    const input = screen.getByLabelText('YouTube video, Short, or playlist URL');
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

    const input = screen.getByLabelText('YouTube video, Short, or playlist URL');
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

    const input = screen.getByLabelText('YouTube video, Short, or playlist URL');
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

    await user.type(screen.getByLabelText('YouTube video, Short, or playlist URL'), playlistURL);
    await user.click(screen.getByRole('button', { name: 'Analyze' }));

    expect(await screen.findByText('Fixture playlist')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Add 1 Video to Queue' })).toBeDisabled();
  });

  test('badges a Short from extracted media type, not the submitted URL', async () => {
    const user = userEvent.setup();
    const { AnalyzeURL } = installBindings();
    AnalyzeURL.mockImplementation(async (raw: string) => ({ ...videoSummary(raw), mediaType: 'short' }));
    render(Home);

    await user.type(screen.getByLabelText('YouTube video, Short, or playlist URL'), firstURL);
    await user.click(screen.getByRole('button', { name: 'Analyze' }));

    expect(await screen.findByText('Fixture video')).toBeInTheDocument();
    expect(screen.getByText((content, el) => el?.tagName === 'EM' && content === 'Short')).toBeInTheDocument();
  });

  test('does not badge a Shorts URL without extracted media type', async () => {
    const user = userEvent.setup();
    const shortsURL = 'https://www.youtube.com/shorts/fixture0001';
    const watchURL = 'https://www.youtube.com/watch?v=fixture0001';
    const { ValidateURL } = installBindings();
    ValidateURL.mockImplementation(async () => ({
      kind: 'single_video', url: watchURL, videoUrl: watchURL, playlistUrl: '', videoId: 'fixture0001', playlistId: '',
    }));
    render(Home);

    await user.type(screen.getByLabelText('YouTube video, Short, or playlist URL'), shortsURL);
    await user.click(screen.getByRole('button', { name: 'Analyze' }));

    expect(await screen.findByText('Fixture video')).toBeInTheDocument();
    expect(screen.queryByText('Short')).not.toBeInTheDocument();
  });

  test('requires at least two non-empty lines before batch review', async () => {
    const user = userEvent.setup();
    render(Home);
    await user.click(screen.getByRole('button', { name: 'Batch URLs' }));
    const input = screen.getByLabelText('YouTube video or Short URLs');
    const review = screen.getByRole('button', { name: 'Review URLs' });
    expect(review).toBeDisabled();
    await user.type(input, 'one');
    expect(review).toBeDisabled();
    await user.type(input, '\n\ntwo');
    expect(review).toBeEnabled();
  });

  test('does not publish an in-flight batch review after the lines change', async () => {
    const user = userEvent.setup();
    let finishReview!: (result: any) => void;
    const { AnalyzeBatchURLs } = installBindings();
    AnalyzeBatchURLs.mockImplementation(() => new Promise((resolve) => { finishReview = resolve; }));
    render(Home);
    await user.click(screen.getByRole('button', { name: 'Batch URLs' }));
    const input = screen.getByLabelText('YouTube video or Short URLs');
    await user.type(input, 'one\ntwo');
    await user.click(screen.getByRole('button', { name: 'Review URLs' }));
    await waitFor(() => expect(AnalyzeBatchURLs).toHaveBeenCalledOnce());
    await user.type(input, '\nthree');
    finishReview({
      token: 'stale-token', expiresAt: '2099-01-01T00:00:00Z',
      counts: { pasted: 2, ready: 2, duplicate: 0, invalid: 0, analysisFailed: 0 }, items: [],
    });
    await waitFor(() => expect(screen.queryByText('Review URLs', { selector: 'h2' })).not.toBeInTheDocument());
    expect(screen.getByRole('button', { name: 'Review URLs' })).toBeEnabled();
  });

  test('reviews mixed batch lines on Home and starts every ready item', async () => {
    const user = userEvent.setup();
    const { AnalyzeBatchURLs, StartBatchDownload } = installBindings();
    AnalyzeBatchURLs.mockResolvedValue({
      token: 'batch-token', expiresAt: '2099-08-22T12:00:00Z',
      counts: { pasted: 5, ready: 3, duplicate: 1, invalid: 1, analysisFailed: 0 },
      items: [
        { lineNumber: 1, input: 'https://youtu.be/fixture0001', status: 'ready', messageKey: 'batch.ready', message: 'Ready', title: 'First' },
        { lineNumber: 2, input: 'https://youtu.be/fixture0002', status: 'ready', messageKey: 'batch.ready', message: 'Ready', title: 'Second' },
        { lineNumber: 3, input: 'https://www.youtube.com/watch?v=fixture0001', status: 'duplicate', messageKey: 'batch.duplicate', message: 'Duplicate of line 1', duplicateOfLine: 1 },
        { lineNumber: 4, input: 'not-a-url', status: 'invalid', messageKey: 'batch.invalid_url', message: 'Only YouTube links are supported.' },
        { lineNumber: 5, input: 'https://youtu.be/fixture0003', status: 'ready', messageKey: 'batch.ready', message: 'Ready', title: 'Third' },
      ],
    });
    StartBatchDownload.mockResolvedValue({ collectionId: 'batch-1', admitted: 3 });
    render(Home);

    await user.click(screen.getByRole('button', { name: 'Batch URLs' }));
    await user.type(screen.getByLabelText('YouTube video or Short URLs'), 'one\ntwo\nthree\nfour\nfive');
    await user.click(screen.getByRole('button', { name: 'Review URLs' }));

    expect(await screen.findByText('Duplicate of line 1')).toBeInTheDocument();
    expect(screen.getByText('Only YouTube links are supported.')).toBeInTheDocument();
    const start = screen.getByRole('button', { name: 'Start 3 downloads' });
    expect(start).toBeEnabled();
    await user.click(start);
    await waitFor(() => expect(StartBatchDownload).toHaveBeenCalledWith({ token: 'batch-token', quality: '1080p', audioBitrate: 0 }));
  });

  test('invalidates a batch review when the user edits the lines', async () => {
    const user = userEvent.setup();
    const { AnalyzeBatchURLs } = installBindings();
    AnalyzeBatchURLs.mockResolvedValue({
      token: 'batch-token', expiresAt: '2099-08-22T12:00:00Z', counts: { pasted: 2, ready: 2, duplicate: 0, invalid: 0, analysisFailed: 0 },
      items: [
        { lineNumber: 1, input: 'one', status: 'ready', messageKey: 'batch.ready', message: 'Ready', title: 'First' },
        { lineNumber: 2, input: 'two', status: 'ready', messageKey: 'batch.ready', message: 'Ready', title: 'Second' },
      ],
    });
    render(Home);

    await user.click(screen.getByRole('button', { name: 'Batch URLs' }));
    const input = screen.getByLabelText('YouTube video or Short URLs');
    await user.type(input, 'one\ntwo');
    await user.click(screen.getByRole('button', { name: 'Review URLs' }));
    expect(await screen.findByRole('button', { name: 'Start 2 downloads' })).toBeEnabled();
    await user.click(screen.getByRole('button', { name: 'Edit lines' }));
    expect(screen.queryByRole('button', { name: 'Start 2 downloads' })).not.toBeInTheDocument();
    expect(screen.getByLabelText('YouTube video or Short URLs')).toHaveValue('one\ntwo');
  });

  test('disables admission when the backend batch review is expired', async () => {
    const user = userEvent.setup();
    const { AnalyzeBatchURLs } = installBindings();
    AnalyzeBatchURLs.mockResolvedValue({
      token: 'expired-token', expiresAt: '2000-01-01T00:00:00Z',
      counts: { pasted: 2, ready: 2, duplicate: 0, invalid: 0, analysisFailed: 0 },
      items: [
        { lineNumber: 1, input: 'one', status: 'ready', messageKey: 'batch.ready', message: 'Ready', title: 'First' },
        { lineNumber: 2, input: 'two', status: 'ready', messageKey: 'batch.ready', message: 'Ready', title: 'Second' },
      ],
    });
    render(Home);
    await user.click(screen.getByRole('button', { name: 'Batch URLs' }));
    await user.type(screen.getByLabelText('YouTube video or Short URLs'), 'one\ntwo');
    await user.click(screen.getByRole('button', { name: 'Review URLs' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('This review expired');
    expect(screen.getByRole('button', { name: 'Start 2 downloads' })).toBeDisabled();
  });

  test('switching output types selects a visible compatible plan', async () => {
    const user = userEvent.setup();
    render(Home);

    await user.type(screen.getByLabelText('YouTube video, Short, or playlist URL'), firstURL);
    await user.click(screen.getByRole('button', { name: 'Analyze' }));
    expect(await screen.findByText('Video plan')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Audio' }));
    expect(screen.getByText('Audio plan')).toBeInTheDocument();
    expect(screen.queryByText('Video plan')).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Add to Queue' })).toBeEnabled();
  });
});
