import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import ts from 'typescript';

const read = (path) => readFile(new URL(path, import.meta.url), 'utf8');

async function loadLifecycleTypes() {
  const source = await read('../src/lib/lifecycle-ui/types.ts');
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 },
  });
  return import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`);
}

test('lifecycle UI exports typed presentation components and view models', async () => {
  const index = await read('../src/lib/lifecycle-ui/index.ts');
  for (const name of [
    'DestinationConflictDialog',
    'LifecycleBadge',
    'LifecycleJobRow',
    'QueueOverview',
    'QueueSettingsCard',
    'QueueSummary',
    'QuitConfirmationDialog',
    'RecoveryRequiredShell',
  ]) {
    assert.match(index, new RegExp(`export \{ default as ${name} \}`));
  }
  for (const name of [
    'DurableLifecycle',
    'PresentationPhase',
    'LifecycleJobViewModel',
    'QueueSummaryViewModel',
    'QueueSettingsViewModel',
    'QuitConfirmationViewModel',
    'RecoveryRequiredViewModel',
    'DestinationConflictViewModel',
  ]) {
    assert.match(index, new RegExp(`export type[\\s\\S]*${name}`));
  }
});

test('lifecycle view model keeps lifecycle, phase, and occupancy independent', async () => {
  const types = await read('../src/lib/lifecycle-ui/types.ts');
  assert.match(types, /lifecycle:\s*DurableLifecycle/);
  assert.match(types, /phase\?:\s*PresentationPhase/);
  assert.match(types, /occupiesSlot:\s*boolean/);
  assert.match(types, /'pausing'/);
  assert.match(types, /'canceling'/);
  assert.match(types, /'action-required'/);
  assert.match(types, /'cleaning-up'/);
  assert.match(types, /queuePositionLabel/);
});

test('badge occupancy indicator follows manager occupancy and supports publication phases', async () => {
  const [badge, row, types] = await Promise.all([
    read('../src/lib/lifecycle-ui/LifecycleBadge.svelte'),
    read('../src/lib/lifecycle-ui/LifecycleJobRow.svelte'),
    read('../src/lib/lifecycle-ui/types.ts'),
  ]);
  assert.match(badge, /occupiesSlot:\s*boolean/);
  assert.match(badge, /showsOccupiedIndicator = \$derived\(occupiesSlot\)/);
  assert.match(row, /occupiesSlot=\{job\.occupiesSlot\}/);
  assert.doesNotMatch(badge, /lifecycle === ['"]active['"]|phase === ['"]finalizing['"]/);
  for (const phase of ['ready-to-publish', 'publishing']) {
    assert.match(types, new RegExp(`['"]${phase}['"]`));
  }
  assert.match(types, /Ready to publish/);
  assert.match(types, /Publishing/);
});

test('queue summary and row expose truthful slot occupancy and semantic actions', async () => {
  const [summary, row, overview] = await Promise.all([
    read('../src/lib/lifecycle-ui/QueueSummary.svelte'),
    read('../src/lib/lifecycle-ui/LifecycleJobRow.svelte'),
    read('../src/lib/lifecycle-ui/QueueOverview.svelte'),
  ]);
  assert.match(summary, /active slots/);
  assert.match(summary, /waiting/);
  assert.match(summary, /paused/);
  assert.match(await read('../src/lib/lifecycle-ui/types.ts'), /occupiesSlot:\s*boolean/);
  assert.match(row, /createEventDispatcher<LifecycleJobRowEvents>/);
  for (const action of ['pause', 'cancel', 'resume', 'retry', 'download-again', 'review', 'remove']) {
    assert.match(row, new RegExp(`['"]?${action}['"]?`));
  }
  assert.match(await read('../src/lib/lifecycle-ui/types.ts'), /Pause requested\. Jobs will settle individually; finalizing work may still complete\./);
  assert.doesNotMatch(row, /from ['"].*api/);
  assert.doesNotMatch(overview, /from ['"].*(api|stores)/);
});

test('transitional and terminal row copy stays distinct', async () => {
  const types = await read('../src/lib/lifecycle-ui/types.ts');
  const row = await read('../src/lib/lifecycle-ui/LifecycleJobRow.svelte');
  for (const copy of [
    'Saving resume state...',
    'Discarding resumable data...',
    'Merging video and audio...',
    'Removing temporary files...',
    'Canceled. Resumable data was removed.',
    'The reserved filename is no longer available.',
  ]) {
    assert.match(types + row, new RegExp(copy.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')));
  }
  assert.match(row, />Retry<\/button>/);
  assert.match(row, />Download again<\/button>/);
  assert.match(row, />Review<\/button>/);
});

test('durable outcomes cannot be masked by retained engine phases', async () => {
  const { lifecycleLabel, lifecycleMessage, lifecycleTone } = await loadLifecycleTypes();
  const base = { id: 'job-1', title: 'Example', occupiesSlot: false };

  assert.equal(lifecycleLabel('action-required', 'ready-to-publish'), 'Action required');
  assert.equal(lifecycleMessage({ ...base, lifecycle: 'action-required', phase: 'ready-to-publish' }), 'The reserved filename is no longer available.');
  assert.equal(lifecycleLabel('failed', 'finalizing'), 'Failed');
  assert.equal(lifecycleMessage({ ...base, lifecycle: 'failed', phase: 'finalizing' }), 'Download failed');
  assert.equal(lifecycleLabel('completed', 'publishing'), 'Completed');
  assert.equal(lifecycleLabel('active', 'publishing'), 'Publishing');
  assert.equal(lifecycleLabel('canceling', 'cleaning-up'), 'Cleaning up');
  assert.equal(lifecycleTone('canceling', 'cleaning-up'), 'neutral');
});

test('modal dialogs trap focus and only claim a destination preview when confirmed', async () => {
  const [conflict, quit, modal] = await Promise.all([
    read('../src/lib/lifecycle-ui/DestinationConflictDialog.svelte'),
    read('../src/lib/lifecycle-ui/QuitConfirmationDialog.svelte'),
    read('../src/lib/lifecycle-ui/modal.ts'),
  ]);
  assert.match(conflict, /proposedNameAvailable === true/);
  assert.match(conflict, /isValidConflictToken\(conflict\.conflictToken\)/);
  assert.match(conflict, /conflictToken: conflict\.conflictToken/);
  assert.doesNotMatch(conflict, /\{ name: conflict\.proposedName \}/);
  assert.match(conflict, /use:trapModalFocus/);
  assert.match(quit, /use:trapModalFocus/);
  assert.match(modal, /event\.key !== 'Tab'/);
  assert.match(modal, /previouslyFocused\?\.isConnected/);
});

test('queue-wide actions require positive backend capabilities', async () => {
  const [overview, types] = await Promise.all([
    read('../src/lib/lifecycle-ui/QueueOverview.svelte'),
    read('../src/lib/lifecycle-ui/types.ts'),
  ]);
  assert.match(types, /canPauseAll:\s*boolean/);
  assert.match(types, /canClearCompleted:\s*boolean/);
  assert.match(overview, /disabled=\{!model\.canPauseAll\}/);
  assert.match(overview, /disabled=\{!model\.canClearCompleted\}/);
  assert.doesNotMatch(types, /pauseAllDisabled\?|clearCompletedDisabled\?/);
});

test('queue settings communicates 1–10/default 2 and drain-on-lower with no toggle', async () => {
  const [settings, types] = await Promise.all([
    read('../src/lib/lifecycle-ui/QueueSettingsCard.svelte'),
    read('../src/lib/lifecycle-ui/types.ts'),
  ]);
  assert.match(settings, /Queue & recovery/);
  assert.match(settings, /Choose from \{minimum\} to \{maximum\}/);
  assert.match(settings, /Reducing the limit waits for active jobs; it does not pause them\./);
  assert.match(settings, /Restored as paused/);
  assert.match(settings, /Nothing starts automatically when VidStow opens\./);
  assert.match(settings, /Default: \{defaultValue\} · FIFO order/);
  assert.doesNotMatch(settings, /type=["']checkbox/);
  assert.match(types, /MIN_CONCURRENCY = 1/);
  assert.match(types, /MAX_CONCURRENCY = 10/);
  assert.match(types, /DEFAULT_CONCURRENCY = 2/);
});

test('ordinary quit dialog only offers safe first-release actions', async () => {
  const quit = await read('../src/lib/lifecycle-ui/QuitConfirmationDialog.svelte');
  assert.match(quit, /Quit VidStow\?/);
  assert.match(quit, /active download\$\{model\.activeDownloads === 1 \? '' : 's'\}/);
  assert.match(quit, /Waiting or paused/);
  assert.match(quit, /Already safe/);
  assert.match(quit, /Saved progress will be restored as paused the next time VidStow opens\./);
  assert.match(quit, />Keep working<\/button>/);
  assert.match(quit, />Pause downloads and quit<\/button>/);
  assert.doesNotMatch(quit, /Cancel downloads and quit/);
  assert.doesNotMatch(quit, /background operation|tray operation/i);
});

test('recovery-required shell fails closed and exposes diagnostics only', async () => {
  const recovery = await read('../src/lib/lifecycle-ui/RecoveryRequiredShell.svelte');
  assert.match(recovery, /Download state needs recovery/);
  assert.match(recovery, /Your media and recovery files were preserved\./);
  assert.match(recovery, /will not resume, retry, cancel, or clean up saved work\./);
  assert.match(recovery, /State file/);
  assert.match(recovery, /Automatic cleanup/);
  assert.match(recovery, /Saved media/);
  assert.match(recovery, />Copy diagnostics<\/button>/);
  assert.match(recovery, />Open data folder<\/button>/);
  assert.doesNotMatch(recovery, />Resume<\/button>|>Retry<\/button>|>Cancel<\/button>|>Reset<\/button>/);
});

test('destination conflict keeps the existing file and emits safe choices', async () => {
  const conflict = await read('../src/lib/lifecycle-ui/DestinationConflictDialog.svelte');
  assert.match(conflict, /Choose a new filename/);
  assert.match(conflict, /The reserved filename is no longer available\. VidStow will not replace the existing file\./);
  assert.match(conflict, /Unavailable name/);
  assert.match(conflict, /New reserved name/);
  assert.match(conflict, /Available/);
  assert.match(conflict, /The existing file will remain unchanged\./);
  assert.match(conflict, />Cancel download<\/button>/);
  assert.match(conflict, />Use new name<\/button>/);
  assert.doesNotMatch(conflict, />Replace<\/button>/);
  assert.doesNotMatch(conflict, /from ['"].*(api|stores)/);
});

test('all lifecycle UI components remain presentation-only', async () => {
  const files = [
    'DestinationConflictDialog.svelte',
    'LifecycleBadge.svelte',
    'LifecycleJobRow.svelte',
    'modal.ts',
    'QueueOverview.svelte',
    'QueueSettingsCard.svelte',
    'QueueSummary.svelte',
    'QuitConfirmationDialog.svelte',
    'RecoveryRequiredShell.svelte',
    'types.ts',
  ];
  const contents = await Promise.all(files.map((file) => read(`../src/lib/lifecycle-ui/${file}`)));
  for (const content of contents) {
    assert.doesNotMatch(content, /\bapi\b|\bstores?\b|window\.runtime|Wails/i);
  }
});
