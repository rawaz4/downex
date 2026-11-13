import { beforeAll, describe, expect, it } from 'vitest';
import {
  etaSeconds,
  formatBytes,
  formatClock,
  formatCount,
  formatDate,
  formatDateTime,
  formatDriveLabel,
  formatEta,
  formatPercent,
  formatSize,
  formatSpeed,
  hostOf,
  parseRate,
  progressFraction,
  RATE_UNITS,
  setFormatLocale,
  splitRate,
} from './format';
import { KiB, MiB, makeDownload, makeMediaDownload, makeMediaInfo } from './testing';

const MB = 1000 * 1000;
const GB = 1000 * MB;

beforeAll(() => setFormatLocale('en-US', 'UTC'));

describe('formatBytes', () => {
  it.each([
    [0, '0 B'],
    [1, '1 B'],
    [999, '999 B'],
    [1000, '1.0 KB'],
    [1024, '1.0 KB'],
    [1500, '1.5 KB'],
    [10 * MB, '10.0 MB'],
    [10 * MiB, '10.5 MB'],
    [99.94 * MB, '99.9 MB'],
    [150 * MB, '150 MB'],
    // 630 MiB
    [660_602_880, '661 MB'],
    [1_429_658_896, '1.4 GB'],
    [5 * GB, '5.0 GB'],
    [2 * 1000 * GB, '2.0 TB'],
  ])('%d → %s', (bytes, text) => {
    expect(formatBytes(bytes)).toBe(text);
  });

  it('steps at 1000 (decimal units) and promotes instead of printing 1000 of a unit', () => {
    expect(formatBytes(999_999)).toBe('1.0 MB');
    expect(formatBytes(999_499)).toBe('999 KB');
    expect(formatBytes(999_500)).toBe('1.0 MB');
    expect(formatBytes(999_999_999)).toBe('1.0 GB');
    expect(formatBytes(1_000_000)).toBe('1.0 MB');
  });

  it('drops the decimal for a value that rounds up to 100 of a unit', () => {
    expect(formatBytes(99_950)).toBe('100 KB');
    expect(formatBytes(99_949)).toBe('99.9 KB');
    expect(formatBytes(99_950_000)).toBe('100 MB');
    expect(formatSpeed(99_950)).toBe('100 KB/s');
  });

  it('never shows a binary unit', () => {
    for (const bytes of [999, 1000, 1024, MiB, 630 * MiB, 3 * 1024 * MiB, 1e13]) {
      expect(formatBytes(bytes)).not.toMatch(/iB/);
      expect(formatSpeed(bytes)).not.toMatch(/iB/);
    }
  });

  it('shows a dash for unknown sizes', () => {
    expect(formatBytes(-1)).toBe('—');
    expect(formatBytes(Number.NaN)).toBe('—');
  });
});

describe('formatSpeed', () => {
  it('formats bytes per second', () => {
    expect(formatSpeed(0)).toBe('0 B/s');
    expect(formatSpeed(-5)).toBe('0 B/s');
    expect(formatSpeed(999)).toBe('999 B/s');
    expect(formatSpeed(1000)).toBe('1.0 KB/s');
    expect(formatSpeed(2048)).toBe('2.0 KB/s');
    expect(formatSpeed(12.34 * MB)).toBe('12.3 MB/s');
    // 9.67 MiB/s
    expect(formatSpeed(10_139_730)).toBe('10.1 MB/s');
    expect(formatSpeed(999_999)).toBe('1.0 MB/s');
  });
});

describe('formatEta', () => {
  it.each([
    [Number.NaN, '—'],
    [-1, '—'],
    [Number.POSITIVE_INFINITY, '∞'],
    [0, '<1s'],
    [0.4, '1s'],
    [45, '45s'],
    [185, '3m 05s'],
    [7620, '2h 07m'],
    [3 * 86400 + 4 * 3600, '3d 4h'],
    [200 * 86400, '>99d'],
  ])('%d s → %s', (s, text) => {
    expect(formatEta(s)).toBe(text);
  });
});

describe('etaSeconds and progress', () => {
  it('computes remaining time only while downloading with a known size', () => {
    const d = makeDownload({ id: 1, state: 'downloading', size: 1000, received: 400, speedBps: 100 });
    expect(etaSeconds(d)).toBe(6);
    expect(etaSeconds({ ...d, state: 'paused' })).toBeNaN();
    expect(etaSeconds({ ...d, size: -1 })).toBeNaN();
    expect(etaSeconds({ ...d, speedBps: 0 })).toBeNaN();
  });

  it('estimates the time left of a stream from its estimated size, else from its fragments', () => {
    const d = makeMediaDownload({ id: 1, state: 'downloading', size: 300 * MiB, sizeApprox: true, received: 100 * MiB, speedBps: 10 * MiB,
      media: makeMediaInfo({ fragmentsDone: 100 }) });
    expect(etaSeconds(d)).toBe(20);
    // 100 MiB were half the fragments.
    expect(etaSeconds({ ...d, size: -1, sizeApprox: false })).toBe(10);
    // An estimate already passed: the fragments decide.
    expect(etaSeconds({ ...d, size: 90 * MiB })).toBe(10);
  });

  it('measures a web page’s video by its bytes, never by its uneven parts (SITE-MEDIA-SPEC §2.5)', () => {
    // Paused at 4 of 37 parts (10 MiB video chunks, 2 MiB audio ones): 10.8% by parts,
    // 16.5% by the bytes kept.
    const site = makeMediaDownload({
      id: 1, state: 'paused', size: 316_000_000, sizeApprox: false, received: 52_000_000,
      media: makeMediaInfo({ origin: 'site', format: 'http', manifestUrl: '', fragmentsTotal: 37, fragmentsDone: 4 }),
    });
    const bytes = 52_000_000 / 316_000_000;
    expect(progressFraction(site)).toBeCloseTo(bytes, 9);
    expect(progressFraction({ ...site, state: 'downloading' })).toBeCloseTo(bytes, 9);
    // Also when the size is the site's estimate, and indeterminate while it is unknown.
    expect(progressFraction({ ...site, sizeApprox: true })).toBeCloseTo(bytes, 9);
    expect(progressFraction({ ...site, size: -1 })).toBeNaN();
    expect(etaSeconds({ ...site, state: 'downloading', speedBps: 1_000_000, size: -1 })).toBeNaN();
    expect(etaSeconds({ ...site, state: 'downloading', speedBps: 1_000_000 })).toBeCloseTo(264, 6);
    // A site's stream (HLS or DASH through yt-dlp) is measured like any stream.
    const stream = { ...site, sizeApprox: true, media: makeMediaInfo({ origin: 'site', format: 'hls', fragmentsTotal: 40, fragmentsDone: 10 }) };
    expect(progressFraction(stream)).toBe(0.25);
  });

  it('formatSize marks downexd’s estimates with "~"', () => {
    expect(formatSize(makeDownload({ id: 1, size: 1500 }))).toBe('1.5 KB');
    expect(formatSize(makeDownload({ id: 1, size: 2100 * MB, sizeApprox: true }))).toBe('~2.1 GB');
    expect(formatSize(makeDownload({ id: 1, size: 1_429_658_896, sizeApprox: true }))).toBe('~1.4 GB');
    expect(formatSize(makeDownload({ id: 1, size: -1 }))).toBe('—');
    expect(formatSize(makeDownload({ id: 1, size: -1, sizeApprox: true }))).toBe('—');
  });

  it('progressFraction handles completed, unknown and empty sizes', () => {
    expect(progressFraction(makeDownload({ id: 1, state: 'completed', size: -1 }))).toBe(1);
    expect(progressFraction(makeDownload({ id: 1, size: -1, received: 50 }))).toBeNaN();
    expect(progressFraction(makeDownload({ id: 1, size: 0 }))).toBe(0);
    expect(progressFraction(makeDownload({ id: 1, size: 200, received: 50 }))).toBe(0.25);
    expect(progressFraction(makeDownload({ id: 1, size: 200, received: 500 }))).toBe(1);
  });

  it('formatPercent floors so 100% means done', () => {
    expect(formatPercent(0.999)).toBe('99%');
    expect(formatPercent(1)).toBe('100%');
    expect(formatPercent(0)).toBe('0%');
    expect(formatPercent(Number.NaN)).toBe('');
  });
});

describe('dates', () => {
  const now = Date.UTC(2026, 9, 20, 15, 0, 0);

  it('shows the time for today, month and day this year, the full date otherwise', () => {
    expect(formatDate(Date.UTC(2026, 9, 20, 9, 5), now)).toBe('09:05');
    expect(formatDate(Date.UTC(2026, 9, 3, 23, 59), now)).toBe('Oct 3');
    expect(formatDate(Date.UTC(2024, 2, 1, 12, 0), now)).toBe('Mar 1, 2024');
    expect(formatDate(0, now)).toBe('—');
  });

  it('formats full timestamps', () => {
    expect(formatDateTime(Date.UTC(2026, 9, 20, 9, 5, 7))).toBe('Oct 20, 2026, 09:05:07');
    expect(formatDateTime(0)).toBe('—');
  });
});

describe('rates', () => {
  it('parses user input into bytes per second', () => {
    expect(RATE_UNITS).toEqual(['KB/s', 'MB/s']);
    expect(parseRate('', 'MB/s')).toBe(0);
    expect(parseRate(' 2 ', 'KB/s')).toBe(2000);
    expect(parseRate('1.5', 'MB/s')).toBe(1_500_000);
    expect(parseRate('1,5', 'MB/s')).toBe(1_500_000);
    expect(parseRate('abc', 'MB/s')).toBeNull();
    expect(parseRate('-1', 'MB/s')).toBeNull();
    expect(parseRate('1e3', 'MB/s')).toBeNull();
  });

  it('reads a typed unit: K, M, G decimal, KiB, MiB, GiB binary', () => {
    expect(parseRate('500K', 'MB/s')).toBe(500_000);
    expect(parseRate('500 KB', 'MB/s')).toBe(500_000);
    expect(parseRate('500 kb/s', 'MB/s')).toBe(500_000);
    expect(parseRate('2M', 'KB/s')).toBe(2_000_000);
    expect(parseRate('2 MB/s', 'KB/s')).toBe(2_000_000);
    expect(parseRate('1G', 'KB/s')).toBe(1_000_000_000);
    expect(parseRate('1 GB', 'KB/s')).toBe(1_000_000_000);
    expect(parseRate('500 KiB', 'MB/s')).toBe(500 * KiB);
    expect(parseRate('1.5 MiB/s', 'KB/s')).toBe(1.5 * MiB);
    expect(parseRate('1 GiB', 'KB/s')).toBe(1024 * MiB);
    expect(parseRate('300 B/s', 'MB/s')).toBe(300);
    expect(parseRate('5 Mi', 'MB/s')).toBeNull();
    expect(parseRate('5 X', 'MB/s')).toBeNull();
    expect(parseRate('MB', 'MB/s')).toBeNull();
  });

  it('refuses a speed above 0 that rounds to 0 bytes per second (0 is no limit)', () => {
    expect(parseRate('0.4 B/s', 'MB/s')).toBeNull();
    expect(parseRate('0.0001', 'KB/s')).toBeNull();
    expect(parseRate('0', 'KB/s')).toBe(0);
    expect(parseRate('0.5 B/s', 'MB/s')).toBe(1);
  });

  it('splits bytes per second into an editable value and unit', () => {
    expect(splitRate(0)).toEqual({ value: '', unit: 'MB/s' });
    expect(splitRate(2 * MB)).toEqual({ value: '2', unit: 'MB/s' });
    expect(splitRate(1.5 * MB)).toEqual({ value: '1.5', unit: 'MB/s' });
    expect(splitRate(1500)).toEqual({ value: '1.5', unit: 'KB/s' });
    // never "1000" KB/s: MB/s once the KB/s text would round to 1000
    expect(splitRate(999_999)).toEqual({ value: '1', unit: 'MB/s' });
    expect(splitRate(999_995)).toEqual({ value: '1', unit: 'MB/s' });
    expect(splitRate(999_994)).toEqual({ value: '999.99', unit: 'KB/s' });
    // a limit set in MiB/s before shows rounded
    expect(splitRate(10 * MiB)).toEqual({ value: '10.49', unit: 'MB/s' });
    expect(splitRate(512 * KiB)).toEqual({ value: '524.29', unit: 'KB/s' });
  });

  it('round-trips through the dialogs', () => {
    for (const bytes of [512 * 1000, 3 * MB, 2.25 * MB, 100 * 1000, 1230]) {
      const { value, unit } = splitRate(bytes);
      expect(parseRate(value, unit)).toBe(bytes);
    }
  });

  it('keeps a loaded limit exactly while its text is unchanged', () => {
    for (const bytes of [10 * MiB, 512 * KiB, MiB + 1, 999_999]) {
      const { value, unit } = splitRate(bytes);
      expect(parseRate(value, unit)).not.toBe(bytes);
      expect(parseRate(value, unit, bytes)).toBe(bytes);
      // edited: what is typed
      expect(parseRate('10', unit, bytes)).toBe(10 * (unit === 'MB/s' ? MB : 1000));
    }
    // the same number in the other unit is an edit
    expect(parseRate('10.49', 'KB/s', 10 * MiB)).toBe(10_490);
  });
});

describe('misc', () => {
  it('formats counts and hosts', () => {
    expect(formatCount(10000)).toBe('10,000');
    expect(hostOf('https://cdn.example.net:8443/a')).toBe('cdn.example.net:8443');
    expect(hostOf('not a url')).toBe('not a url');
  });
});

describe('formatClock', () => {
  const at = Date.UTC(2026, 9, 28, 11, 59, 30);

  it("shows the time of day on the locale's clock", () => {
    expect(formatClock(at)).toMatch(/^11:59\sAM$/u);
    expect(formatClock(Date.UTC(2026, 9, 28, 23, 5))).toMatch(/^11:05\sPM$/u);
    setFormatLocale('en-GB', 'UTC');
    try {
      expect(formatClock(at)).toBe('11:59');
      expect(formatClock(Date.UTC(2026, 9, 28, 23, 5))).toBe('23:05');
    } finally {
      setFormatLocale('en-US', 'UTC');
    }
  });

  it('follows the format time zone and shows a dash for no time', () => {
    setFormatLocale('de-DE', 'Europe/Berlin');
    try {
      expect(formatClock(at)).toBe('12:59');
    } finally {
      setFormatLocale('en-US', 'UTC');
    }
    expect(formatClock(0)).toBe('—');
    expect(formatClock(-5)).toBe('—');
    expect(formatClock(Number.NaN)).toBe('—');
    expect(formatClock(Number.POSITIVE_INFINITY)).toBe('—');
  });
});

describe('formatDriveLabel', () => {
  it.each([
    ['D:\\Downloads\\Video', 'D:'],
    ['c:\\Users\\you\\Downloads', 'C:'],
    ['E:/media', 'E:'],
    ['D:', 'D:'],
    ['\\\\nas\\share\\downloads', 'Network folder'],
    ['//nas/share', 'Network folder'],
    ['/home/you/Downloads', 'Downloads'],
    ['/home/you/Downloads/', 'Downloads'],
    ['/', '/'],
    ['', ''],
    ['   ', ''],
  ])('%j → %j', (dir, label) => {
    expect(formatDriveLabel(dir)).toBe(label);
  });
});
