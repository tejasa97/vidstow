import { describe, expect, test } from 'vitest';

import { formatEngineVersion, formatViewCount, youtubeUrlFromText } from '../src/lib/format.js';

describe('formatViewCount', () => {
  test('promotes 1000 of a unit to the next unit', () => {
    expect(formatViewCount(999_999)).toBe('1M');
    expect(formatViewCount(999_999_999)).toBe('1B');
  });

  test('keeps ordinary compact values', () => {
    expect(formatViewCount(405_000_000)).toBe('405M');
    expect(formatViewCount(12_000_000)).toBe('12M');
    expect(formatViewCount(1500)).toBe('1.5K');
    expect(formatViewCount(999)).toBe('999');
  });
});

describe('youtubeUrlFromText', () => {
  test('extracts Shorts URLs from pasted text', () => {
    expect(youtubeUrlFromText('check this https://www.youtube.com/shorts/dQw4w9WgXcQ out')).toBe(
      'https://www.youtube.com/shorts/dQw4w9WgXcQ',
    );
  });

  test('extracts channel URLs from pasted text', () => {
    expect(youtubeUrlFromText('see https://www.youtube.com/@veritasium/videos later')).toBe(
      'https://www.youtube.com/@veritasium/videos',
    );
  });
});

describe('formatEngineVersion', () => {
  test('uses the commit hash from a Go pseudo-version, not the timestamp', () => {
    expect(formatEngineVersion('v0.1.1-0.20260807091708-09a8354be2be')).toBe('v0.1.1 (09a8354)');
  });

  test('keeps stable and prerelease tags without inventing a hash', () => {
    expect(formatEngineVersion('v0.2.1')).toBe('v0.2.1');
    expect(formatEngineVersion('v0.2.1-beta.1')).toBe('v0.2.1-beta.1');
  });
});
