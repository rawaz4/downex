// Display formatting. Byte quantities use decimal units (1 KB = 1,000 B, 1 MB =
// 1,000,000 B), as other download managers and file managers show them. Every function
// is pure; the locale and time zone are module settings so tests can pin them.

import type { Download, MediaInfo } from './api';

let currentLocale = 'en-US';
let currentTimeZone: string | undefined;

export function setFormatLocale(locale: string, timeZone?: string): void {
  currentLocale = locale;
  currentTimeZone = timeZone;
  numberFormats.clear();
  dateFormats.clear();
}

export const DASH = '—';

const BYTE_UNITS = ['B', 'KB', 'MB', 'GB', 'TB', 'PB', 'EB'] as const;

/** One step of BYTE_UNITS: 1 KB = 1,000 B. */
const UNIT_STEP = 1000;

const numberFormats = new Map<string, Intl.NumberFormat>();

function numberFormat(digits: number): Intl.NumberFormat {
  const key = `${currentLocale}|${digits}`;
  let nf = numberFormats.get(key);
  if (!nf) {
    nf = new Intl.NumberFormat(currentLocale, {
      minimumFractionDigits: digits,
      maximumFractionDigits: digits,
    });
    numberFormats.set(key, nf);
  }
  return nf;
}

export function formatCount(n: number): string {
  return numberFormat(0).format(n);
}

/**
 * 1500 → "1.5 KB"; values below 100 units keep one decimal. A value switches to the
 * next unit from 1000, and one that would round to 1000 shows in the next unit
 * (999,999 → "1.0 MB"), so columns never show four integer digits.
 * Negative = unknown → "—".
 */
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return DASH;
  if (bytes < 1000) return `${Math.round(bytes)} B`;
  let unit = 0;
  let value = bytes;
  while (value >= 1000 && unit < BYTE_UNITS.length - 1) {
    value /= UNIT_STEP;
    unit++;
  }
  let digits = value < 100 ? 1 : 0;
  // 99.95 KB rounds to 100: no decimal from 100, as for any other value from 100
  if (digits === 1 && roundTo(value, 1) >= 100) digits = 0;
  // 999.96 KB would print as "1000 KB": promote to the next unit instead.
  if (roundTo(value, digits) >= 1000 && unit < BYTE_UNITS.length - 1) {
    value /= UNIT_STEP;
    unit++;
    digits = 1;
  }
  return `${numberFormat(digits).format(roundTo(value, digits))} ${BYTE_UNITS[unit]}`;
}

function roundTo(value: number, digits: number): number {
  const f = 10 ** digits;
  return Math.round(value * f) / f;
}

export function formatSpeed(bytesPerSecond: number): string {
  if (!Number.isFinite(bytesPerSecond) || bytesPerSecond <= 0) return '0 B/s';
  return `${formatBytes(bytesPerSecond)}/s`;
}

/** Seconds → "45s", "3m 05s", "2h 07m", "3d 4h". NaN/negative → "—", Infinity → "∞". */
export function formatEta(seconds: number): string {
  if (Number.isNaN(seconds) || seconds < 0) return DASH;
  if (!Number.isFinite(seconds)) return '∞';
  const s = Math.ceil(seconds);
  if (s < 1) return '<1s';
  if (s < 60) return `${s}s`;
  if (s < 3600) {
    const m = Math.floor(s / 60);
    return `${m}m ${pad2(s % 60)}s`;
  }
  if (s < 86400) {
    const h = Math.floor(s / 3600);
    return `${h}h ${pad2(Math.floor((s % 3600) / 60))}m`;
  }
  const d = Math.floor(s / 86400);
  if (d > 99) return '>99d';
  return `${d}d ${Math.floor((s % 86400) / 3600)}h`;
}

function pad2(n: number): string {
  return n < 10 ? `0${n}` : String(n);
}

/**
 * A download's size for display: "2.1 GB", "~2.1 GB" for downexd's estimate (a stream
 * that is not completed), "—" when unknown.
 */
export function formatSize(d: Pick<Download, 'size' | 'sizeApprox'>): string {
  if (!(d.size >= 0)) return DASH;
  return `${d.sizeApprox ? '~' : ''}${formatBytes(d.size)}`;
}

/** The kind and media info of a download (a media download's fragments measure its progress). */
type WithMedia = { readonly kind?: Download['kind']; readonly media?: MediaInfo | null };

/** Whether a download's size is downexd's estimate (older hosts never say so). */
type WithEstimate = { readonly sizeApprox?: boolean };

/**
 * Fragments done of a media download as 0..1; NaN when it has no fragment count, and for
 * a web page's video saved from its direct formats (format "http"): its parts are chunks
 * of uneven sizes (a 10 MiB video chunk, a 2 MiB audio one), so its bytes measure it
 * against Download.size like a file, indeterminate while the size is unknown
 * (SITE-MEDIA-SPEC §2.5, §5.4). A stream's estimated size never measures its progress:
 * the fragments do.
 */
function fragmentFraction(d: WithMedia): number {
  const media = d.media;
  if (!media || !(media.fragmentsTotal > 0)) return Number.NaN;
  if (d.kind === 'media' && media.origin === 'site' && media.format === 'http') return Number.NaN;
  return Math.min(1, Math.max(0, media.fragmentsDone / media.fragmentsTotal));
}

/**
 * Remaining seconds for a running download; NaN when not computable. A stream's comes
 * from downexd's estimate of its size while that is ahead of the bytes received, else
 * from the bytes its finished fragments took.
 */
export function etaSeconds(d: Pick<Download, 'state' | 'size' | 'received' | 'speedBps'> & WithMedia & WithEstimate): number {
  if (d.state !== 'downloading' || !(d.speedBps > 0)) return Number.NaN;
  if (d.sizeApprox && d.size > d.received) return (d.size - d.received) / d.speedBps;
  const fragments = fragmentFraction(d);
  if (Number.isFinite(fragments)) {
    if (!(fragments > 0) || !(d.received > 0)) return Number.NaN;
    return Math.max(0, d.received / fragments - d.received) / d.speedBps;
  }
  if (d.size < 0) return Number.NaN;
  return Math.max(0, d.size - d.received) / d.speedBps;
}

/**
 * 0..1, or NaN when the size is unknown (indeterminate). Media downloads count fragments,
 * except a web page's video from its direct formats, always measured by its bytes
 * (indeterminate while its size is unknown).
 */
export function progressFraction(d: Pick<Download, 'state' | 'size' | 'received'> & WithMedia & WithEstimate): number {
  if (d.state === 'completed') return 1;
  const fragments = fragmentFraction(d);
  if (Number.isFinite(fragments)) return fragments;
  if (d.size <= 0) return d.size === 0 ? 0 : Number.NaN;
  return Math.min(1, Math.max(0, d.received / d.size));
}

/** Bits per second → "800 kbit/s", "5.2 Mbit/s" (decimal units, as bitrates are quoted); "" when unknown. */
export function formatBitrate(bitsPerSecond: number): string {
  if (!Number.isFinite(bitsPerSecond) || bitsPerSecond <= 0) return '';
  if (bitsPerSecond < 1000) return `${Math.round(bitsPerSecond)} bit/s`;
  if (bitsPerSecond < 999_500) return `${numberFormat(0).format(Math.round(bitsPerSecond / 1000))} kbit/s`;
  const mbit = bitsPerSecond / 1_000_000;
  return `${numberFormat(mbit < 100 ? 1 : 0).format(roundTo(mbit, mbit < 100 ? 1 : 0))} Mbit/s`;
}

/** Milliseconds → "45:07" or "1:02:03" (playback length); "—" when unknown. */
export function formatDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return DASH;
  const s = Math.round(ms / 1000);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const rest = pad2(s % 60);
  return h > 0 ? `${h}:${pad2(m)}:${rest}` : `${m}:${rest}`;
}

/** Floors so a download never shows 100% before it has completed. */
export function formatPercent(fraction: number): string {
  if (!Number.isFinite(fraction)) return '';
  const pct = Math.floor(Math.min(1, Math.max(0, fraction)) * 100);
  return `${pct}%`;
}

const dateFormats = new Map<string, Intl.DateTimeFormat>();

function dateFormat(kind: 'time' | 'clock' | 'monthDay' | 'date' | 'full' | 'ymd'): Intl.DateTimeFormat {
  const key = `${currentLocale}|${currentTimeZone ?? ''}|${kind}`;
  let df = dateFormats.get(key);
  if (!df) {
    const tz = currentTimeZone ? { timeZone: currentTimeZone } : {};
    const options: Intl.DateTimeFormatOptions =
      kind === 'time'
        ? { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' }
        : kind === 'clock'
          ? // the locale's own clock: 24 h ("11:59") or 12 h ("11:59 AM")
            { hour: 'numeric', minute: '2-digit' }
          : kind === 'monthDay'
            ? { month: 'short', day: 'numeric' }
            : kind === 'date'
              ? { year: 'numeric', month: 'short', day: 'numeric' }
              : kind === 'ymd'
                ? { year: 'numeric', month: '2-digit', day: '2-digit' }
                : {
                    year: 'numeric',
                    month: 'short',
                    day: 'numeric',
                    hour: '2-digit',
                    minute: '2-digit',
                    second: '2-digit',
                    hourCycle: 'h23',
                  };
    df = new Intl.DateTimeFormat(currentLocale, { ...options, ...tz });
    dateFormats.set(key, df);
  }
  return df;
}

/**
 * Compact date for list columns: time of day for today, "Oct 3" this year,
 * otherwise "Oct 3, 2024". 0/negative → "—".
 */
export function formatDate(ms: number, now: number = Date.now()): string {
  if (!(ms > 0)) return DASH;
  const ymd = dateFormat('ymd');
  const day = ymd.format(ms);
  if (day === ymd.format(now)) return dateFormat('time').format(ms);
  const year = (t: number) =>
    ymd.formatToParts(t).find((p) => p.type === 'year')?.value ?? '';
  if (year(ms) === year(now)) return dateFormat('monthDay').format(ms);
  return dateFormat('date').format(ms);
}

export function formatDateTime(ms: number): string {
  if (!(ms > 0)) return DASH;
  return dateFormat('full').format(ms);
}

/**
 * A time of day on the locale's clock, for "Done ≈ 11:59": "11:59" where the locale counts 24
 * hours, "11:59 AM" where it counts 12. 0, negative or not a number → "—".
 */
export function formatClock(epochMs: number): string {
  if (!(epochMs > 0) || !Number.isFinite(epochMs)) return DASH;
  return dateFormat('clock').format(epochMs);
}

/**
 * The volume of a download folder, for the status bar's free space ("D: 230 GB free"):
 * "D:" for a Windows drive, "Network folder" for a UNC path (\\server\share), else the
 * folder's last component ("Downloads"; "/" for the root). "" when there is no folder.
 */
export function formatDriveLabel(dir: string): string {
  const d = dir.trim();
  if (d === '') return '';
  const drive = /^([A-Za-z]):(?:[\\/]|$)/.exec(d);
  if (drive) return `${(drive[1] ?? '').toUpperCase()}:`;
  if (/^[\\/]{2}[^\\/]/.test(d)) return 'Network folder';
  const parts = d.split(/[\\/]+/).filter((p) => p !== '');
  return parts.at(-1) ?? '/';
}

export type RateUnit = 'KB/s' | 'MB/s';

/** The units of the speed limit pickers (decimal: 1 KB/s = 1,000 bytes per second). */
export const RATE_UNITS: readonly RateUnit[] = ['KB/s', 'MB/s'];

const RATE_FACTOR: Record<RateUnit, number> = { 'KB/s': 1000, 'MB/s': 1000 * 1000 };

/** A typed unit prefix: K, M, G (KB, MB, GB) are decimal; KiB, MiB, GiB stay binary for old habits. */
const DECIMAL_PREFIX: Record<string, number> = { k: 1e3, m: 1e6, g: 1e9 };
const BINARY_PREFIX: Record<string, number> = { k: 1024, m: 1024 ** 2, g: 1024 ** 3 };

/** A number with an optional unit of its own: "500", "1.5 MB", "2M", "1 MiB/s", "300 B/s". */
const RATE_TEXT = /^(\d+(?:\.\d+)?)\s*(?:([kmg])(i)?)?(b)?(?:\s*\/\s*s)?$/i;

/**
 * Parses a user-entered rate: a number in `unit`, or with a unit typed after it, which wins
 * ("2 MB", "500K", "1.5 MiB/s"; K, M, G = 1,000, 1,000,000, 1,000,000,000 bytes per second,
 * KiB, MiB, GiB = 1024, 1024², 1024³). Empty = 0 (unlimited). Returns null when invalid.
 * `loaded` is the limit the fields were filled from: while their text is still what
 * splitRate showed for it, it is kept exactly (a limit set in MiB/s before shows rounded in
 * MB/s and a save does not change it).
 */
export function parseRate(value: string, unit: RateUnit, loaded = 0): number | null {
  const text = value.trim().replace(',', '.');
  if (text === '') return 0;
  if (loaded > 0) {
    const shown = splitRate(loaded);
    if (shown.unit === unit && shown.value === text) return loaded;
  }
  const m = RATE_TEXT.exec(text);
  if (!m) return null;
  // "5Mi" (a binary prefix without its B) is not a unit
  if (m[3] && !m[4]) return null;
  const prefix = m[2]?.toLowerCase();
  const factor = prefix ? (m[3] ? BINARY_PREFIX : DECIMAL_PREFIX)[prefix] : m[4] ? 1 : RATE_FACTOR[unit];
  const typed = Number(m[1]);
  const bytes = Math.round(typed * (factor ?? RATE_FACTOR[unit]));
  if (!Number.isSafeInteger(bytes)) return null;
  // "0.4 B/s" is not 0 (no limit): a speed above 0 that rounds to no bytes is invalid
  if (typed > 0 && bytes === 0) return null;
  return bytes;
}

/**
 * Splits bytes/s into an editable value and unit for the dialogs: KB/s with at most two
 * decimals, MB/s once that would read 1000 or more (from 999,995 B/s, never "1000" KB/s).
 * 0 → "" (unlimited).
 */
export function splitRate(bytesPerSecond: number): { value: string; unit: RateUnit } {
  if (!(bytesPerSecond > 0)) return { value: '', unit: 'MB/s' };
  const kb = trimNumber(bytesPerSecond / RATE_FACTOR['KB/s']);
  if (Number(kb) < 1000) return { value: kb, unit: 'KB/s' };
  return { value: trimNumber(bytesPerSecond / RATE_FACTOR['MB/s']), unit: 'MB/s' };
}

function trimNumber(n: number): string {
  return String(Math.round(n * 100) / 100);
}

/** Hostname of a URL for compact display; the input when it does not parse. */
export function hostOf(url: string): string {
  try {
    return new URL(url).host;
  } catch {
    return url;
  }
}
