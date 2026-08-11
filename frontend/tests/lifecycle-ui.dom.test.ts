import { fireEvent, render, screen } from '@testing-library/svelte';
import '@testing-library/jest-dom/vitest';
import userEvent from '@testing-library/user-event';
import { describe, expect, test, vi } from 'vitest';

import DestinationConflictDialog from '../src/lib/lifecycle-ui/DestinationConflictDialog.svelte';
import LifecycleJobRow from '../src/lib/lifecycle-ui/LifecycleJobRow.svelte';
import QueueOverview from '../src/lib/lifecycle-ui/QueueOverview.svelte';
import QuitConfirmationDialog from '../src/lib/lifecycle-ui/QuitConfirmationDialog.svelte';
import type {
  DestinationConflictEventDetail,
  DestinationConflictViewModel,
  LifecycleJobEventDetail,
  LifecycleJobViewModel,
  QueueOverviewViewModel,
} from '../src/lib/lifecycle-ui/types.js';

function queueModel(overrides: Partial<QueueOverviewViewModel> = {}): QueueOverviewViewModel {
  return {
    summary: {
      totalJobs: 0,
      runningJobs: 0,
      occupiedSlots: 0,
      slotLimit: 2,
      waitingJobs: 0,
      pausedJobs: 0,
    },
    jobs: [],
    canPauseAll: false,
    canClearCompleted: false,
    ...overrides,
  };
}

function conflictModel(overrides: Partial<DestinationConflictViewModel> = {}): DestinationConflictViewModel {
  return {
    conflictToken: 'conflict-token-7',
    unavailableName: 'Video.mp4',
    proposedName: 'Video (2).mp4',
    proposedNameAvailable: true,
    ...overrides,
  };
}

describe('backend-authored capabilities', () => {
  test('queue-wide actions fail closed when capabilities are absent at runtime', async () => {
    const onPauseAll = vi.fn();
    const onClearCompleted = vi.fn();
    const user = userEvent.setup();
    const model = queueModel() as Partial<QueueOverviewViewModel>;
    delete model.canPauseAll;
    delete model.canClearCompleted;

    render(QueueOverview, {
      props: { model: model as QueueOverviewViewModel },
      events: { 'pause-all': onPauseAll, 'clear-completed': onClearCompleted },
    });

    const pauseAll = screen.getByRole('button', { name: 'Pause All' });
    const clearCompleted = screen.getByRole('button', { name: 'Clear Completed' });
    expect(pauseAll).toBeDisabled();
    expect(clearCompleted).toBeDisabled();

    await user.click(pauseAll);
    await user.click(clearCompleted);
    expect(onPauseAll).not.toHaveBeenCalled();
    expect(onClearCompleted).not.toHaveBeenCalled();
  });

  test('queue-wide actions emit only when explicitly enabled', async () => {
    const onPauseAll = vi.fn();
    const onClearCompleted = vi.fn();
    const user = userEvent.setup();

    render(QueueOverview, {
      props: { model: queueModel({ canPauseAll: true, canClearCompleted: true }) },
      events: { 'pause-all': onPauseAll, 'clear-completed': onClearCompleted },
    });

    await user.click(screen.getByRole('button', { name: 'Pause All' }));
    await user.click(screen.getByRole('button', { name: 'Clear Completed' }));
    expect(onPauseAll).toHaveBeenCalledOnce();
    expect(onClearCompleted).toHaveBeenCalledOnce();
  });

  test('disabled row capabilities do not emit actions', async () => {
    const onPause = vi.fn<(event: CustomEvent<LifecycleJobEventDetail>) => void>();
    const onCancel = vi.fn<(event: CustomEvent<LifecycleJobEventDetail>) => void>();
    const job: LifecycleJobViewModel = {
      id: 'job-1',
      title: 'Example video',
      lifecycle: 'active',
      phase: 'downloading',
      occupiesSlot: true,
      capabilities: { pause: false, cancel: false },
    };
    const user = userEvent.setup();

    render(LifecycleJobRow, {
      props: { job },
      events: { pause: onPause, cancel: onCancel },
    });

    const pause = screen.getByRole('button', { name: 'Pause download' });
    const cancel = screen.getByRole('button', { name: 'Cancel download' });
    expect(pause).toBeDisabled();
    expect(cancel).toBeDisabled();
    await user.click(pause);
    await user.click(cancel);
    expect(onPause).not.toHaveBeenCalled();
    expect(onCancel).not.toHaveBeenCalled();
    expect(screen.getByLabelText('Downloading, occupies an active slot')).toBeInTheDocument();
  });
});

describe('destination conflict authority', () => {
  test('emits only the opaque backend token for both choices', async () => {
    const onUseNewName = vi.fn<(event: CustomEvent<DestinationConflictEventDetail>) => void>();
    const onCancelDownload = vi.fn<(event: CustomEvent<DestinationConflictEventDetail>) => void>();
    const user = userEvent.setup();

    render(DestinationConflictDialog, {
      props: {
        open: true,
        conflict: conflictModel(),
      },
      events: { 'use-new-name': onUseNewName, 'cancel-download': onCancelDownload },
    });

    expect(screen.getByRole('dialog', { name: 'Choose a new filename' })).toBeInTheDocument();
    expect(screen.getByDisplayValue('Video (2).mp4')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Use new name' }));
    await user.click(screen.getByRole('button', { name: 'Cancel download' }));

    expect(onUseNewName).toHaveBeenCalledOnce();
    expect(onUseNewName.mock.calls[0][0].detail).toEqual({ conflictToken: 'conflict-token-7' });
    expect(onUseNewName.mock.calls[0][0].detail).not.toHaveProperty('name');
    expect(onCancelDownload.mock.calls[0][0].detail).toEqual({ conflictToken: 'conflict-token-7' });
  });

  test('disables authoritative choices when the backend token is absent', () => {
    const conflict = conflictModel() as Partial<DestinationConflictViewModel>;
    delete conflict.conflictToken;
    render(DestinationConflictDialog, {
      props: { open: true, conflict: conflict as DestinationConflictViewModel },
    });

    expect(screen.getByRole('button', { name: 'Use new name' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Cancel download' })).toBeDisabled();
  });

  test.each([
    ['null', null],
    ['non-string', 7],
    ['empty', ''],
    ['oversized UTF-8', 'é'.repeat(129)],
    ['C0 control', 'token\u0000value'],
    ['C1 control', 'token\u0085value'],
    ['lone surrogate', 'token\ud800value'],
  ])('fails closed for %s conflict authority', async (_name, token) => {
    const onUseNewName = vi.fn();
    const onCancelDownload = vi.fn();
    const conflict = { ...conflictModel(), conflictToken: token } as unknown as DestinationConflictViewModel;
    const user = userEvent.setup();
    render(DestinationConflictDialog, {
      props: { open: true, conflict },
      events: { 'use-new-name': onUseNewName, 'cancel-download': onCancelDownload },
    });

    const useNewName = screen.getByRole('button', { name: 'Use new name' });
    const cancelDownload = screen.getByRole('button', { name: 'Cancel download' });
    expect(useNewName).toBeDisabled();
    expect(cancelDownload).toBeDisabled();
    await user.click(useNewName);
    await user.click(cancelDownload);
    expect(onUseNewName).not.toHaveBeenCalled();
    expect(onCancelDownload).not.toHaveBeenCalled();
  });

  test('Escape and overlay clicks emit close while dialog clicks do not', async () => {
    const onClose = vi.fn();
    const user = userEvent.setup();
    const { container } = render(DestinationConflictDialog, {
      props: { open: true, conflict: conflictModel() },
      events: { close: onClose },
    });

    await user.keyboard('{Escape}');
    expect(onClose).toHaveBeenCalledTimes(1);

    const dialog = screen.getByRole('dialog');
    await fireEvent.click(dialog);
    expect(onClose).toHaveBeenCalledTimes(1);

    const overlay = container.querySelector('.overlay');
    expect(overlay).not.toBeNull();
    await fireEvent.click(overlay!);
    expect(onClose).toHaveBeenCalledTimes(2);
  });
});

describe('modal keyboard focus', () => {
  test('contains focus and restores the previously focused control on unmount', async () => {
    const outside = document.createElement('button');
    outside.textContent = 'Outside';
    document.body.append(outside);
    outside.focus();
    const user = userEvent.setup();

    const rendered = render(QuitConfirmationDialog, {
      props: {
        open: true,
        model: { activeDownloads: 2, waitingOrPausedDownloads: 1 },
      },
    });
    await Promise.resolve();

    const keepWorking = screen.getByRole('button', { name: 'Keep working' });
    const close = screen.getByRole('button', { name: 'Close' });
    const quit = screen.getByRole('button', { name: 'Pause downloads and quit' });
    expect(keepWorking).toHaveFocus();

    outside.focus();
    expect(keepWorking).toHaveFocus();

    close.focus();
    await user.tab({ shift: true });
    expect(quit).toHaveFocus();
    await user.tab();
    expect(close).toHaveFocus();

    rendered.unmount();
    expect(outside).toHaveFocus();
    outside.remove();
  });
});
