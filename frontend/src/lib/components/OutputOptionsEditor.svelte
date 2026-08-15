<script lang="ts">
  // Collapsed "advanced" disclosure for per-download subtitle and embedding
  // choices. Mirrors the backend gate: FFmpeg-dependent choices stay disabled
  // (and are clamped away) while FFmpeg is unavailable, so the user is never
  // left with checked-but-stuck controls.
  import { createEventDispatcher } from 'svelte';
  import type { OutputOptions, SubtitleLanguage } from '../types.js';

  export let value: OutputOptions = {};
  // Languages reported by analysis; empty for collections (playlists), where
  // the engine's English-first default picks per video.
  export let languages: SubtitleLanguage[] = [];
  export let allowSubtitles = true;
  export let ffmpegAvailable = true;
  export let collectionMode = false;

  const dispatch = createEventDispatcher<{ 'goto-settings': void }>();

  let open = false;

  $: mode = value.subtitleMode ?? '';
  $: selectedLanguages = new Set(value.subtitleLanguages ?? []);
  $: manualLanguages = languages.filter((language) => !language.auto);
  $: autoLanguages = languages.filter((language) => language.auto);
  $: noLanguagesReported = !collectionMode && languages.length === 0;
  $: subtitleChoiceBlocked = !allowSubtitles || noLanguagesReported;
  $: embedBlocked = !ffmpegAvailable;
  $: summary = [
    mode === 'sidecar' ? 'subtitle file' : mode === 'embed' ? 'embedded subtitles' : '',
    value.embedMetadata ? 'details' : '',
    value.embedThumbnail ? 'thumbnail' : '',
    value.embedChapters ? 'chapters' : '',
  ]
    .filter(Boolean)
    .join(' · ');

  $: if (!ffmpegAvailable && requiresFFmpeg(value)) {
    value = withoutFFmpegChoices(value);
  }

  function requiresFFmpeg(options: OutputOptions): boolean {
    return (
      options.subtitleMode === 'embed' ||
      !!options.embedMetadata ||
      !!options.embedThumbnail ||
      !!options.embedChapters ||
      (options.subtitleMode === 'sidecar' && !!options.subtitleFormat)
    );
  }

  function withoutFFmpegChoices(options: OutputOptions): OutputOptions {
    return {
      ...options,
      subtitleMode: options.subtitleMode === 'embed' ? '' : options.subtitleMode,
      subtitleFormat: '',
      embedMetadata: false,
      embedThumbnail: false,
      embedChapters: false,
    };
  }

  function setMode(next: '' | 'sidecar' | 'embed') {
    if (subtitleChoiceBlocked && next !== '') return;
    if (next === 'embed' && embedBlocked) return;
    const nextValue: OutputOptions = { ...value, subtitleMode: next };
    if (next === 'sidecar' && !nextValue.subtitleFormat && ffmpegAvailable) {
      nextValue.subtitleFormat = 'srt';
    }
    value = nextValue;
  }

  function toggleLanguage(code: string) {
    const next = new Set(value.subtitleLanguages ?? []);
    if (next.has(code)) next.delete(code);
    else next.add(code);
    value = { ...value, subtitleLanguages: [...next] };
  }

  function setFlag(flag: 'subtitleAutoCaptions' | 'embedMetadata' | 'embedThumbnail' | 'embedChapters', checked: boolean) {
    const next = { ...value };
    if (flag === 'subtitleAutoCaptions') next.subtitleAutoCaptions = checked;
    else if (flag === 'embedMetadata') next.embedMetadata = checked;
    else if (flag === 'embedThumbnail') next.embedThumbnail = checked;
    else next.embedChapters = checked;
    value = next;
  }

  function languageLabel(language: SubtitleLanguage): string {
    return language.name || language.code;
  }
</script>

<section class="output-options" aria-label="Subtitles and details">
  <button type="button" class="disclosure" aria-expanded={open} on:click={() => (open = !open)}>
    <span class="chevron" aria-hidden="true">{open ? '▾' : '▸'}</span>
    <span class="label">Subtitles &amp; details</span>
    {#if summary}<span class="summary">{summary}</span>{/if}
  </button>

  {#if open}
    <div class="body">
      <div class="group" class:disabled={subtitleChoiceBlocked}>
        <h3>Subtitles</h3>
        {#if !allowSubtitles}
          <p class="note">Subtitles need a video output.</p>
        {:else if noLanguagesReported}
          <p class="note">No subtitles were reported for this video.</p>
        {:else}
          {#if collectionMode}
            <p class="note">Every video uses English or its first available language.</p>
          {/if}
          <div class="segment" role="group" aria-label="Subtitle mode">
            <button type="button" class:active={mode === ''} aria-pressed={mode === ''} on:click={() => setMode('')} disabled={subtitleChoiceBlocked}>Off</button>
            <button type="button" class:active={mode === 'sidecar'} aria-pressed={mode === 'sidecar'} on:click={() => setMode('sidecar')} disabled={subtitleChoiceBlocked}>Subtitle file</button>
            <button
              type="button"
              class:active={mode === 'embed'}
              aria-pressed={mode === 'embed'}
              on:click={() => setMode('embed')}
              disabled={subtitleChoiceBlocked || embedBlocked}
              title={embedBlocked ? 'Embedding needs FFmpeg' : ''}
            >
              Embed in video
            </button>
          </div>

          {#if mode !== ''}
            {#if !collectionMode && languages.length}
              <div class="languages" role="group" aria-label="Subtitle languages">
                {#each manualLanguages as language (language.code)}
                  <label class="lang">
                    <input type="checkbox" checked={selectedLanguages.has(language.code)} on:change={() => toggleLanguage(language.code)} />
                    {languageLabel(language)}
                  </label>
                {/each}
                {#if autoLanguages.length}
                  <span class="lang-group">Auto-generated</span>
                  {#each autoLanguages as language (`${language.code}:auto`)}
                    <label class="lang">
                      <input type="checkbox" checked={selectedLanguages.has(language.code)} on:change={() => toggleLanguage(language.code)} />
                      {languageLabel(language)}
                    </label>
                  {/each}
                {/if}
              </div>
              <p class="hint">Leave languages unchecked to use English or the first available language.</p>
            {/if}
            {#if autoLanguages.length || collectionMode}
              <label class="check">
                <input type="checkbox" checked={!!value.subtitleAutoCaptions} on:change={(event) => setFlag('subtitleAutoCaptions', event.currentTarget.checked)} />
                Include auto-generated captions
              </label>
            {/if}
            {#if mode === 'sidecar'}
              <label class="inline-select">
                Format
                <select
                  disabled={!ffmpegAvailable}
                  value={value.subtitleFormat ?? ''}
                  on:change={(event) => (value = { ...value, subtitleFormat: (event.currentTarget as HTMLSelectElement).value as 'srt' | 'vtt' })}
                >
                  <option value="">Original</option>
                  <option value="srt">SRT</option>
                  <option value="vtt">VTT</option>
                </select>
              </label>
              {#if !ffmpegAvailable}
                <p class="hint">FFmpeg converts subtitle files; the original format is kept without it.</p>
              {/if}
            {/if}
          {/if}
        {/if}
      </div>

      <div class="group" class:disabled={embedBlocked}>
        <h3>Include in file</h3>
        <label class="check">
          <input type="checkbox" disabled={embedBlocked} checked={!!value.embedMetadata} on:change={(event) => setFlag('embedMetadata', event.currentTarget.checked)} />
          Title &amp; channel details
        </label>
        <label class="check">
          <input type="checkbox" disabled={embedBlocked} checked={!!value.embedThumbnail} on:change={(event) => setFlag('embedThumbnail', event.currentTarget.checked)} />
          Thumbnail artwork
        </label>
        <label class="check">
          <input type="checkbox" disabled={embedBlocked} checked={!!value.embedChapters} on:change={(event) => setFlag('embedChapters', event.currentTarget.checked)} />
          Chapter markers
        </label>
        {#if embedBlocked}
          <p class="hint">
            Embedding needs FFmpeg.
            <button type="button" class="link" on:click={() => dispatch('goto-settings')}>Open Settings</button>
          </p>
        {/if}
      </div>
    </div>
  {/if}
</section>

<style>
  .output-options {
    border-top: 1px solid var(--border-subtle);
    background: var(--surface-subtle);
    flex-shrink: 0;
  }
  .disclosure {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    min-height: 38px;
    padding: 4px 16px;
    color: var(--text-secondary);
    font-size: var(--fs-xs);
    font-weight: 650;
    text-align: left;
  }
  .disclosure:hover { background: var(--surface-hover); }
  .chevron { width: 12px; color: var(--text-muted); }
  .summary {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--text-muted);
    font-weight: 550;
  }
  .body {
    display: flex;
    flex-wrap: wrap;
    gap: 12px 28px;
    padding: 2px 16px 14px 36px;
  }
  .group {
    min-width: 220px;
    display: flex;
    flex-direction: column;
    gap: 7px;
  }
  .group.disabled { opacity: 0.7; }
  .group h3 {
    margin: 0;
    color: var(--text-muted);
    font-size: 10px;
    font-weight: 650;
    letter-spacing: 0.04em;
    text-transform: uppercase;
  }
  .note, .hint {
    margin: 0;
    color: var(--text-muted);
    font-size: 11px;
    line-height: 1.45;
  }
  .segment {
    display: inline-flex;
    width: fit-content;
    padding: 3px;
    background: var(--surface-sunken);
    border: 1px solid var(--border-default);
    border-radius: var(--r-md);
  }
  .segment button {
    min-width: 76px;
    min-height: 26px;
    padding: 0 10px;
    border-radius: 6px;
    color: var(--text-secondary);
    font-size: var(--fs-xs);
    font-weight: 600;
  }
  .segment button.active {
    color: var(--text-primary);
    background: var(--surface-base);
    box-shadow: var(--shadow-card);
  }
  .segment button:disabled { opacity: 0.55; }
  .languages {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(130px, 1fr));
    gap: 2px 10px;
    max-height: 148px;
    overflow: auto;
    padding: 8px 10px;
    border: 1px solid var(--border-subtle);
    border-radius: var(--r-md);
    background: var(--surface-base);
  }
  .lang {
    display: flex;
    align-items: center;
    gap: 7px;
    font-size: 11px;
    color: var(--text-primary);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .lang-group {
    grid-column: 1 / -1;
    margin-top: 4px;
    color: var(--text-muted);
    font-size: 10px;
    font-weight: 650;
    text-transform: uppercase;
  }
  .check {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: var(--fs-xs);
    font-weight: 550;
    color: var(--text-primary);
    cursor: pointer;
  }
  .check input { flex-shrink: 0; }
  .check input:disabled { cursor: default; }
  .inline-select {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    width: fit-content;
    color: var(--text-secondary);
    font-size: var(--fs-xs);
    font-weight: 600;
  }
  .inline-select select { height: 28px; padding: 0 8px; font-size: var(--fs-xs); }
  .link {
    padding: 0;
    border: 0;
    background: none;
    color: var(--accent-600);
    font-size: 11px;
    font-weight: 650;
    text-decoration: underline;
  }
</style>
