// Human-readable labels derived from download state, shared by the table and details.

import type { ConnectionInfo, Download, MediaInfo } from './api';
import { displayName, kindOf, type FileKind } from './categories';
import {
  etaSeconds,
  formatBytes,
  formatClock,
  formatDate,
  formatEta,
  formatPercent,
  formatSpeed,
  progressFraction,
} from './format';
import { cannotRetry, formatLabel, isSiteDownload, siteVideoLabel } from './media';
import { pieceNames } from './progress';
import type { SortKey } from './query';
import { HISTORY_MS, type SpeedSample } from './stores/downloads.svelte';
import { escapeBidi } from './text';

/** A download's kind and media info, which name some states (a web page's video). */
type WithKind = { readonly kind?: Download['kind']; readonly media?: MediaInfo | null };

export function statusText(d: Pick<Download, 'state' | 'queuePosition' | 'errorCode'> & WithKind): string {
  switch (d.state) {
    case 'downloading':
      return 'Downloading';
    case 'probing':
      // A web page's video: yt-dlp reads the page (SITE-MEDIA-SPEC §5.4).
      return d.kind === 'media' && d.media?.origin === 'site' ? 'Getting video info' : 'Connecting';
    case 'queued':
      return d.queuePosition > 0 ? `Queued #${d.queuePosition}` : 'Queued';
    case 'paused':
      return 'Paused';
    case 'completed':
      return 'Completed';
    case 'failed':
      return d.errorCode === 'validator_changed' ? 'Needs restart' : 'Failed';
    case 'canceled':
      return 'Canceled';
    case 'awaiting_confirmation':
      return 'Waiting for confirmation';
    case 'unspecified':
      return 'Unknown';
  }
}

/** The size to show before a download is probed: its size, else the requester's hint. */
export function sizeHint(d: Pick<Download, 'size' | 'expectedSize'>): number {
  if (d.size > 0) return d.size;
  return d.expectedSize > 0 ? d.expectedSize : -1;
}

const SOURCE_LABELS: Record<Download['source'], string> = {
  unspecified: '',
  cli: 'Command line',
  ui: 'Downex window',
  browser: 'Browser',
  clipboard: 'Clipboard',
};

/** Who added a download, for display ("" when not recorded). */
export function sourceText(source: Download['source']): string {
  return SOURCE_LABELS[source];
}

const ERROR_HINTS: Record<string, string> = {
  validator_changed:
    'The file on the server changed, or the saved progress no longer matches the partial file. It can only continue from the beginning.',
  disk_full: 'The destination disk is full. Free some space, then resume.',
  tls: 'The secure connection could not be verified. Downex never skips certificate checks.',
  http_status: 'The server refused the request.',
  network: 'The connection failed. Resuming keeps the data received so far.',
  invalid_request: 'The daemon rejected the request.',
  stalled: 'No data arrived for too long. Resuming keeps the data received so far.',
  internal: 'An internal error occurred in the daemon.',
  encrypted: 'The stream is encrypted or DRM-protected. Downex does not download protected content.',
  live: 'The stream is live. Downex downloads only complete (on-demand) streams.',
  auth_required: 'The server asks for a user name and password. Add a site login for it, then retry.',
};

export function errorHint(code: string): string {
  return ERROR_HINTS[code] ?? '';
}

/**
 * Joins a directory and file name with the separator the directory already uses, for
 * display: bidi control characters are made visible (see escapeBidi).
 */
export function joinPath(dir: string, filename: string): string {
  return escapeBidi(rawJoinPath(dir, filename));
}

/** joinPath without the escaping: the path itself, for copying it. */
export function rawJoinPath(dir: string, filename: string): string {
  if (!filename) return dir;
  if (!dir) return filename;
  const sep = dir.includes('\\') && !dir.includes('/') ? '\\' : '/';
  return dir.endsWith(sep) ? `${dir}${filename}` : `${dir}${sep}${filename}`;
}

/**
 * Spoken summary of a download's progress (the table's "I" shortcut). Live values are
 * left out when the list is stale (the daemon is unreachable).
 */
export function progressSummary(d: Download, stale: boolean): string {
  const parts = [statusText(d)];
  const fraction = progressFraction(d);
  if (Number.isFinite(fraction)) parts.push(formatPercent(fraction));
  const media = d.media;
  if (media && media.fragmentsTotal > 0 && !(isSiteDownload(d) && d.size > 0 && !d.sizeApprox)) {
    const pieces = pieceNames(d).many.toLowerCase();
    const of = d.sizeApprox && d.size > 0 ? ` of ${spokenSize(d)}` : '';
    parts.push(`${media.fragmentsDone} of ${media.fragmentsTotal} ${pieces}`, `${formatBytes(d.received)}${of}`);
  } else {
    parts.push(`${formatBytes(d.received)} of ${d.size >= 0 ? spokenSize(d) : 'unknown size'}`);
  }
  if (d.state === 'downloading' && !stale) {
    parts.push(formatSpeed(d.speedBps));
    const eta = etaSeconds(d);
    if (Number.isFinite(eta)) parts.push(`${formatEta(eta)} left`);
  }
  if (stale) parts.push('last known state, downexd is not connected');
  return `${displayName(d)}: ${parts.join(', ')}`;
}

/** A known size as assistive technology reads it: "about 2.1 GB" for downexd's estimate. */
function spokenSize(d: Pick<Download, 'size' | 'sizeApprox'>): string {
  return `${d.sizeApprox ? 'about ' : ''}${formatBytes(d.size)}`;
}

/** Stable secondary facts for a row's accessible description (no per-second values). */
export function rowDescription(d: Download): string {
  const size = d.size >= 0 ? spokenSize(d) : 'unknown';
  const category = d.category ? `, category ${escapeBidi(d.category)}` : '';
  let stream = '';
  if (d.kind === 'media') stream = d.media && isSiteDownload(d) ? `, ${siteVideoLabel(d.media)}` : `, ${mediaBadge(d)} stream`;
  return `Size ${size}${category}, added ${d.createdAt > 0 ? formatDate(d.createdAt) : 'unknown'}${stream}`;
}

/**
 * The short label of a media download's badge: a web page's site ("YouTube", else
 * "Video"), a stream's format ("HLS", "DASH"), else "Media".
 */
export function mediaBadge(d: Pick<Download, 'media'>): string {
  if (d.media?.origin === 'site') return d.media.site.trim() || 'Video';
  return formatLabel(d.media?.format ?? '') || 'Media';
}

// ---- the Studio list and details pane (DESIGN-SYSTEM.md §3.11, §3.15) -----------------------

const KIND_LABELS: Record<FileKind, string> = {
  video: 'Video',
  audio: 'Audio',
  archives: 'Archives',
  documents: 'Documents',
  programs: 'Programs',
  other: 'Other',
};

/** The label of a file kind ("Video"), for a download without a category. */
export function kindLabel(kind: FileKind): string {
  return KIND_LABELS[kind];
}

/**
 * The host a row names: the hostname without "www." of a web page's video (its page), else of
 * the final address (else the first). "" when the address does not parse.
 */
export function rowHost(d: Pick<Download, 'url' | 'finalUrl' | 'kind' | 'media'>): string {
  const page = d.kind === 'media' && d.media?.origin === 'site' ? d.media.pageUrl : '';
  try {
    return escapeBidi(new URL(page || d.finalUrl || d.url).hostname.replace(/^www\./i, ''));
  } catch {
    return '';
  }
}

/** A row's category: the one downexd chose, else the label of the file's kind. */
export function rowCategory(d: Pick<Download, 'category' | 'filename' | 'url' | 'finalUrl'>): string {
  return d.category ? escapeBidi(d.category) : kindLabel(kindOf(d));
}

/** A row's second line: "youtube.com · Video" (the host is left out when there is none). */
export function rowSubline(d: Download): string {
  return [rowHost(d), rowCategory(d)].filter((s) => s !== '').join(' · ');
}

const SOURCE_PHRASES: Record<Download['source'], string> = {
  unspecified: '',
  cli: 'from the command line',
  ui: 'from the Downex window',
  browser: 'from the browser',
  clipboard: 'from the clipboard',
};

/** Who added a download as a phrase ("from the browser"); "" when not recorded. */
export function sourcePhrase(source: Download['source']): string {
  return SOURCE_PHRASES[source];
}

/** `ms` falls on the same calendar day as `day` (in the format time zone). */
function sameDay(ms: number, day: number): boolean {
  // formatDate shows a time of day only for the day it is given as "now".
  return formatDate(ms, day) === formatDate(ms, ms);
}

/**
 * When and from where a download was added, for the details pane: "Today, 14:02 · from the
 * browser", "Yesterday, 09:15", "Oct 21, 18:40". "—" when the time is not known.
 */
export function addedText(d: Pick<Download, 'createdAt' | 'source'>, now: number = Date.now()): string {
  const ms = d.createdAt;
  if (!(ms > 0)) return formatDate(ms);
  const from = sourcePhrase(d.source);
  return `${dayTimeText(ms, now)}${from ? ` · ${from}` : ''}`;
}

/** A moment as "Today, 14:02", "Yesterday, 09:15" or "25 Oct, 18:31" (no time without a moment: "—"). */
export function dayTimeText(ms: number, now: number = Date.now()): string {
  if (!(ms > 0)) return formatDate(ms);
  const day = sameDay(ms, now) ? 'Today' : sameDay(ms, now - 86_400_000) ? 'Yesterday' : formatDate(ms, now);
  return `${day}, ${formatClock(ms)}`;
}

/** The tone of a state pill (`.pill.<tone>` in app.css). */
export type PillTone = 'ok' | 'info' | 'warn' | 'fail' | 'done';

/** The details pane's state pill: "Downloading · 42%", "Paused · 18%", "Queued #2", "Failed". */
export function statePill(d: Download): { readonly tone: PillTone; readonly text: string } {
  const status = statusText(d);
  const pct = formatPercent(progressFraction(d));
  switch (d.state) {
    case 'downloading':
      return { tone: 'ok', text: pct ? `${status} · ${pct}` : status };
    case 'probing':
      return { tone: 'ok', text: status };
    case 'paused':
      return { tone: 'warn', text: pct ? `${status} · ${pct}` : status };
    case 'failed':
      return { tone: 'fail', text: status };
    case 'awaiting_confirmation':
      return { tone: 'info', text: status };
    default:
      return { tone: 'done', text: status };
  }
}

/**
 * Bytes received of a total with the unit said once where both share it: "1.4 of 3.4 GB",
 * "900 MB of 3.4 GB", "~" before an estimated total; only the bytes when the total is unknown.
 */
export function receivedOf(received: number, size: number, approx = false): string {
  const got = formatBytes(Math.max(0, received));
  if (!(size >= 0)) return got;
  const total = `${approx ? '~' : ''}${formatBytes(size)}`;
  const unit = (s: string) => s.slice(s.lastIndexOf(' ') + 1);
  if (unit(got) === unit(total)) return `${got.slice(0, got.lastIndexOf(' '))} of ${total}`;
  return `${got} of ${total}`;
}

/** The multi-select strip: "3 downloads selected · 4.2 GB" (sizes that are known; "~" when some are estimates). */
export function selectionSummary(ds: readonly Pick<Download, 'size' | 'sizeApprox'>[]): string {
  const head = `${ds.length} download${ds.length === 1 ? '' : 's'} selected`;
  let total = 0;
  let approx = false;
  for (const d of ds) {
    if (d.size > 0) total += d.size;
    if (d.sizeApprox || d.size < 0) approx = true;
  }
  return total > 0 ? `${head} · ${approx ? '~' : ''}${formatBytes(total)}` : head;
}

/** 1 MB = 1,000,000 B (sizes and speeds are shown in decimal units, lib/format.ts). */
const MB = 1000 ** 2;

// ---- the list (DESIGN-SYSTEM.md §3.11) ----

export interface Column {
  readonly key: SortKey;
  readonly label: string;
  /** Full name when the label is abbreviated. */
  readonly title?: string;
  readonly end?: boolean;
  /** Default width in rem; 0 = the flexible column (Name) that takes the rest. */
  readonly width: number;
  /** Smallest width in rem (resizing and fitting stop here). */
  readonly min: number;
  /** When space runs out, columns are hidden from the highest `drop` down; 0 = always shown. */
  readonly drop: number;
}

// DESIGN-SYSTEM.md §3.11: Name flexible (min 12), Size 5, Progress 11, Speed 5.5, Time left 5,
// Conn. 3.25, Status 11.25, Added 4.25 (rem). Status is wide enough for its longest text,
// "Waiting for confirmation", on one line beside its mark; Added holds "27 Oct" or "14:02".
export const COLUMNS: readonly Column[] = [
  { key: 'name', label: 'Name', width: 0, min: 12, drop: 0 },
  { key: 'size', label: 'Size', end: true, width: 5, min: 4, drop: 2 },
  { key: 'progress', label: 'Progress', width: 11, min: 7, drop: 0 },
  { key: 'speed', label: 'Speed', end: true, width: 5.5, min: 4.5, drop: 1 },
  { key: 'eta', label: 'Time left', end: true, width: 5, min: 4, drop: 3 },
  { key: 'connections', label: 'Conn.', title: 'Connections', end: true, width: 3.25, min: 2.75, drop: 4 },
  { key: 'status', label: 'Status', width: 11.25, min: 6, drop: 0 },
  { key: 'added', label: 'Added', end: true, width: 4.25, min: 4, drop: 5 },
];

/** The rows' and header's inline padding (px): the name starts 20 px in, with its cell's 6 px. */
export const ROW_PAD_START = 14;
export const ROW_PAD_END = 10;

/** Row height (px) for a root font size: Comfortable two lines (46 at 16 px), Compact one (24). */
export function rowHeightFor(rem: number, compact: boolean): number {
  return compact ? Math.max(24, Math.round(rem * 1.5)) : Math.max(44, Math.round(rem * 2.875));
}

/**
 * The columns that fit `available` px: low-priority columns are hidden first
 * (Added, Conn., Time left, Size, Speed). Name, Progress and Status always stay.
 */
export function fitColumns(
  available: number,
  rem: number,
  widthOf: (c: Column) => number,
): Column[] {
  let cols = [...COLUMNS];
  if (!(available > 0)) return cols;
  const need = () => cols.reduce((sum, c) => sum + (c.width === 0 ? c.min * rem : widthOf(c)), 0);
  const droppable = COLUMNS.filter((c) => c.drop > 0).sort((a, b) => b.drop - a.drop);
  for (const c of droppable) {
    if (need() <= available) break;
    cols = cols.filter((x) => x !== c);
  }
  return cols;
}

// ---- row cells by state (DESIGN-SYSTEM.md §3.12) ----

/** What the progress cell shows for a state (DESIGN-SYSTEM.md §3.12). */
export type ProgressCell = 'bar' | 'probe' | 'review' | 'none';

export function progressCell(d: Pick<Download, 'state' | 'received'>): ProgressCell {
  switch (d.state) {
    case 'downloading':
    case 'paused':
      return 'bar';
    case 'probing':
      return 'probe';
    case 'queued':
    case 'failed':
      // with progress kept from an earlier run
      return d.received > 0 ? 'bar' : 'none';
    case 'awaiting_confirmation':
      return 'review';
    default:
      // completed, canceled: the status says it, the Added column has the date
      return 'none';
  }
}

/** The inline action of a failed row: Retry, Restart… (the file changed), or none when retrying cannot help. */
export function inlineAction(d: Pick<Download, 'state' | 'errorCode'>): { id: 'resume' | 'restart'; label: string } | null {
  if (d.state !== 'failed' || cannotRetry(d.errorCode)) return null;
  return d.errorCode === 'validator_changed' ? { id: 'restart', label: 'Restart…' } : { id: 'resume', label: 'Retry' };
}

// ---- the speed chart (DESIGN-SYSTEM.md §3.15) ----

/** The y-axis of the speed chart: its top in bytes per second and the labels top, half and 0. */
export interface SpeedAxis {
  readonly max: number;
  readonly labels: readonly [top: string, half: string, zero: string];
}

const UNITS: readonly (readonly [number, string])[] = [
  [1000, 'KB/s'],
  [1000 ** 2, 'MB/s'],
  [1000 ** 3, 'GB/s'],
];

/** The smallest 1, 2 or 5 × 10^k that is at least `v` (> 0). */
function niceCeil(v: number): number {
  const p = 10 ** Math.floor(Math.log10(v));
  for (const m of [1, 2, 5, 10]) if (m * p >= v * (1 - 1e-9)) return m * p;
  return 10 * p;
}

function num(n: number): string {
  return String(Math.round(n * 100) / 100);
}

/**
 * The axis for a peak speed: the top is the peak rounded up to 1, 2 or 5 of the unit that
 * keeps it under 1000 ("10 MB/s" for 5.9 MB/s, "1 MB/s" for 931 KB/s); the middle label is
 * half of it. No speed yet: up to 1 MB/s.
 */
export function speedAxis(peak: number): SpeedAxis {
  if (!(peak > 0) || !Number.isFinite(peak)) return { max: MB, labels: ['1 MB/s', '0.5 MB/s', '0'] };
  for (let i = 0; i < UNITS.length; i++) {
    const [size, unit] = UNITS[i] as readonly [number, string];
    const top = niceCeil(peak / size);
    if (top < 1000 || i === UNITS.length - 1) {
      return { max: top * size, labels: [`${num(top)} ${unit}`, `${num(top / 2)} ${unit}`, '0'] };
    }
  }
  return { max: peak, labels: ['', '', '0'] };
}

/** The samples of the last HISTORY_MS before the newest one, with their average and peak. */
export function speedStats(samples: readonly SpeedSample[]): { readonly avg: number; readonly peak: number; readonly shown: readonly SpeedSample[] } {
  const latest = samples.at(-1);
  if (!latest) return { avg: 0, peak: 0, shown: [] };
  const from = latest.t - HISTORY_MS;
  const shown = samples.filter((s) => s.t >= from);
  let sum = 0;
  let peak = 0;
  for (const s of shown) {
    sum += s.v;
    peak = Math.max(peak, s.v);
  }
  return { avg: shown.length > 0 ? sum / shown.length : 0, peak, shown };
}

// ---- connections (DESIGN-SYSTEM.md §3.15, from B) ----

/** The most boxes the panel draws (two rows of 8); more connections end in a "+N" box. */
export const MAX_BOXES = 16;

/** How a connection's box is drawn. */
export type BoxTone = 'live' | 'wait' | 'retry' | 'error' | 'idle' | 'done';

export interface ConnBox {
  readonly key: string;
  /** 0..1 of its current range received. */
  readonly fraction: number;
  readonly tone: BoxTone;
  readonly speed: number;
  /** Under the box: the fragment number of a stream's connection ("#13"), else its speed. */
  readonly label: string;
  readonly title: string;
  /** The "+N" box: N connections not drawn one by one. */
  readonly more?: number;
}

export interface ConnSummary {
  readonly boxes: readonly ConnBox[];
  /** The sum of the boxes' speeds: every connection's, so the boxes add up to it. */
  readonly together: number;
  /** The unit of the speeds under the boxes. */
  readonly unit: string;
}

function toneOf(c: ConnectionInfo): BoxTone {
  switch (c.state) {
    case 'receiving':
      return 'live';
    case 'connecting':
    case 'requesting':
      return 'wait';
    case 'retrying':
      return 'retry';
    case 'error':
      return 'error';
    case 'done':
      return 'done';
    default:
      return 'idle';
  }
}

const STATE_TEXT: Record<BoxTone, string> = {
  live: 'receiving',
  wait: 'connecting',
  retry: 'retrying',
  error: 'failed',
  idle: 'idle',
  done: 'done',
};

/** A connection's share of its current range; a finished one is full, one without a range empty. */
function rangeFraction(c: ConnectionInfo): number {
  if (c.state === 'done') return 1;
  if (!(c.start >= 0) || !(c.end >= c.start)) return 0;
  return Math.min(1, Math.max(0, c.received / (c.end - c.start + 1)));
}

/**
 * Per-connection speeds under the boxes share one unit, said once by "Together": MB/s with
 * one decimal when any label (a connection, or the +N box's sum) reaches 1 MB/s (or would show
 * as 1000 KB/s), else KB/s in whole numbers.
 */
export function speedUnit(speeds: readonly number[]): 'MB/s' | 'KB/s' {
  return Math.max(0, ...speeds) >= MB - 500 ? 'MB/s' : 'KB/s';
}

export function speedFormatter(unit: 'MB/s' | 'KB/s'): (bps: number) => string {
  if (unit === 'MB/s') return (bps) => (bps / MB).toFixed(1);
  return (bps) => String(Math.round(bps / 1000));
}

/**
 * One box per connection, in slot order, at most `max`: beyond that the last box is "+N" with
 * the speed of the connections it stands for. `together` is the sum of every connection's
 * speed, which is also the sum of the boxes'.
 */
export function connectionBoxes(
  infos: readonly ConnectionInfo[],
  opts: { media?: boolean; piece?: string; max?: number; format?: (bps: number) => string } = {},
): ConnSummary {
  const max = Math.max(2, opts.max ?? MAX_BOXES);
  const piece = opts.piece ?? 'Fragment';
  const sorted = [...infos].sort((a, b) => a.index - b.index);
  const speedOf = (c: ConnectionInfo) => (c.state === 'receiving' && c.speedBps > 0 ? c.speedBps : 0);
  const drawn = sorted.length > max ? sorted.slice(0, max - 1) : sorted;
  const rest = sorted.slice(drawn.length);
  const restSpeed = rest.reduce((sum, c) => sum + speedOf(c), 0);
  // the unit covers every label drawn, the +N box's sum too (never "5100" KB/s)
  const unit = speedUnit([...drawn.map(speedOf), restSpeed]);
  const fmt = opts.format ?? speedFormatter(unit);
  const boxes: ConnBox[] = drawn.map((c) => {
    const speed = speedOf(c);
    const tone = toneOf(c);
    const fragment = opts.media && c.fragment >= 0 ? c.fragment + 1 : 0;
    const what = fragment > 0 ? ` · ${piece.toLowerCase()} ${fragment}` : '';
    return {
      key: `c${c.index}`,
      fraction: rangeFraction(c),
      tone,
      speed,
      // a stream's boxes show their speeds as well (they add up to Together); the fragment
      // each connection is on is in the tooltip
      label: fmt(speed),
      title: `Connection ${c.index}${what}: ${STATE_TEXT[tone]}${c.detail ? ` (${c.detail})` : ''}`,
    };
  });
  if (rest.length > 0) {
    boxes.push({
      key: 'more',
      fraction: 0,
      tone: 'idle',
      speed: restSpeed,
      label: fmt(restSpeed),
      title: `${rest.length} more connections`,
      more: rest.length,
    });
  }
  return { boxes, together: boxes.reduce((sum, b) => sum + b.speed, 0), unit };
}

/** Whether the panel can show live connections (else the static segment overview). */
export function showsConnections(d: Pick<Download, 'state' | 'connectionInfo'>, stale: boolean): boolean {
  return d.state === 'downloading' && !stale && d.connectionInfo.length > 0;
}

// ---- the details pane's speed limit and figures (DESIGN-SYSTEM.md §3.15) ----

/** "Custom…" in the speed limit menu: the progress window's Speed limit tab. */
export const CUSTOM_LIMIT = -1;
/** The download speed limits offered in the pane (bytes per second; 0 = unlimited). */
export const LIMIT_PRESETS: readonly number[] = [0, 20 * MB, 10 * MB, 5 * MB, MB];

/** The speed limit menu for a download: the presets, its own limit when it is another, and Custom…. */
export function limitOptions(current: number): { value: number; label: string }[] {
  const values = [...LIMIT_PRESETS];
  if (current > 0 && !values.includes(current)) values.push(current);
  const sorted = [0, ...values.filter((v) => v > 0).sort((a, b) => b - a)];
  return [
    ...sorted.map((v) => ({ value: v, label: v === 0 ? 'Unlimited' : LIMIT_PRESETS.includes(v) ? `${v / MB} MB/s` : formatSpeed(v) })),
    { value: CUSTOM_LIMIT, label: 'Custom…' },
  ];
}

/** A speed split for the big figure: "5.8" and "MB/s". */
export function speedParts(bps: number): { n: string; unit: string } {
  const s = formatSpeed(bps);
  const at = s.lastIndexOf(' ');
  return at > 0 ? { n: s.slice(0, at), unit: s.slice(at + 1) } : { n: s, unit: '' };
}
