// The progress window of one download (DESKTOP-SPEC §7): what it shows, as pure functions
// of the download. The window itself is ../ProgressWindow.svelte. A web page's video
// (SITE-MEDIA-SPEC §5.4) says "Page", "Getting video info…", "Part N" and "Merging…".

import type { ConnectionInfo, Download } from './api';
import { displayName } from './categories';
import { DASH, etaSeconds, formatBytes, formatClock, formatEta, formatPercent, formatSize, formatSpeed, progressFraction } from './format';
import { cannotRetry, chosenTracks, codecSummary, containerLabel, formatLabel, isSiteDownload, siteVideoLabel } from './media';

/** Window sizes in CSS pixels; the host opens it collapsed (ui/progress.go). */
export const PROGRESS_WINDOW = {
  width: 520,
  collapsedHeight: 300,
  expandedHeight: 560,
  /** The host's minimum height (ui/progress.go). */
  minHeight: 260,
  /** The completion view when its content cannot be measured. */
  completeHeight: 420,
  /** The completion view never grows the window beyond this; its content scrolls. */
  maxHeight: 720,
  /** Title bar and frame, when the host cannot tell the window's outer size. */
  frame: 40,
  /**
   * CSS px added to a fitted height: the host truncates window sizes to device pixels both
   * ways (Wails' SetSize and Size), up to about 1 CSS px each at 125 % or 150 % scaling, and
   * a page 1 px short shows a full scrollbar. Invisible in the page's 12 px bottom padding.
   */
  fitSlack: 3,
} as const;

/** What a window fitted to its content shows: the completion view, or the progress view without its details. */
export type FitView = 'complete' | 'collapsed';

/**
 * The title bar and frame of the window: its outer height (the host's getSize) less the
 * page's; the usual one when the host cannot tell or the difference cannot be a frame.
 */
export function windowFrame(outerPx: number | null | undefined, innerPx: number): number {
  const frame = typeof outerPx === 'number' ? outerPx - innerPx : Number.NaN;
  return Number.isFinite(frame) && frame >= 0 && frame <= 200 ? frame : PROGRESS_WINDOW.frame;
}

/**
 * The window height that shows `contentPx` (the page's height with nothing in it scrolling,
 * fractional) and the frame, rounded up with fitSlack, within the view's limits: the
 * completion view from minHeight, the collapsed progress view never below its usual
 * collapsedHeight; both at most maxHeight (beyond it, the view scrolls). Content that cannot
 * be measured (0, no layout) gets the view's usual height.
 */
export function fitWindowHeight(view: FitView, contentPx: number, framePx: number): number {
  const floor = view === 'complete' ? PROGRESS_WINDOW.minHeight : PROGRESS_WINDOW.collapsedHeight;
  if (!(contentPx > 0)) return view === 'complete' ? PROGRESS_WINDOW.completeHeight : PROGRESS_WINDOW.collapsedHeight;
  const frame = Number.isFinite(framePx) && framePx >= 0 && framePx <= 200 ? framePx : PROGRESS_WINDOW.frame;
  return Math.min(PROGRESS_WINDOW.maxHeight, Math.max(floor, Math.ceil(contentPx + frame + PROGRESS_WINDOW.fitSlack)));
}

/** Where a viewer's choice to show the details is kept (shared by all progress windows). */
export const DETAILS_STORAGE_KEY = 'downex.progress.details';

/** A media download whose fragments are all there: the daemon is assembling the file. */
export function isAssembling(d: Pick<Download, 'state' | 'kind' | 'media'>): boolean {
  const m = d.media;
  return d.state === 'downloading' && d.kind === 'media' && m !== null && m.fragmentsTotal > 0 && m.fragmentsDone >= m.fragmentsTotal;
}

/** The window title: "<percent>% <file name>", just the name while the size is unknown. */
export function progressTitle(d: Download): string {
  const name = displayName(d);
  const fraction = progressFraction(d);
  return Number.isFinite(fraction) ? `${formatPercent(fraction)} ${name}` : name;
}

/** The label of a download's address: a web page's "Page", a stream's "Manifest", else "Address". */
export function addressLabel(d: Pick<Download, 'kind' | 'media'>): string {
  if (d.kind !== 'media') return 'Address';
  return isSiteDownload(d) ? 'Page' : 'Manifest';
}

/**
 * What a media download's pieces are called: a web page's direct formats come in parts
 * (byte ranges of one file, format "http"), streams in fragments.
 */
export function pieceNames(d: Pick<Download, 'kind' | 'media'>): { readonly one: string; readonly many: string } {
  return d.kind === 'media' && d.media?.format === 'http' ? { one: 'Part', many: 'Parts' } : { one: 'Fragment', many: 'Fragments' };
}

/** The Status row. A failure carries the daemon's reason (shown bidi-escaped by the caller). */
export function progressStatus(d: Download): string {
  switch (d.state) {
    case 'probing':
      // yt-dlp reads the page (and is set up first, the first time).
      return isSiteDownload(d) ? 'Getting video info…' : 'Connecting…';
    case 'downloading': {
      if (isAssembling(d)) return isSiteDownload(d) ? 'Merging…' : 'Assembling…';
      const conns = d.connectionInfo;
      const waiting = conns.length > 0 && conns.every((c) => c.state === 'connecting' || c.state === 'requesting');
      return waiting ? 'Connecting…' : 'Receiving data…';
    }
    case 'paused':
      return 'Paused';
    case 'queued':
      return d.queuePosition > 0 ? `Waiting in queue (#${d.queuePosition})` : 'Waiting in queue';
    case 'completed':
      return 'Completed';
    case 'failed':
      return `Failed: ${d.error || 'the download stopped with an error'}`;
    case 'canceled':
      return 'Canceled';
    case 'awaiting_confirmation':
      return 'Waiting for confirmation';
    case 'unspecified':
      return 'Unknown';
  }
}

/** "Resume capability": ranged transfers continue where they stopped; a stream keeps its fragments. */
export function resumeSupport(d: Pick<Download, 'mode' | 'kind'>): string {
  if (d.kind === 'media') return 'Yes';
  switch (d.mode) {
    case 'ranged':
      return 'Yes';
    case 'single':
      return 'No';
    default:
      return 'Unknown';
  }
}

/** "1.2 GB (45%)"; the percentage is left out while the size is unknown. */
export function downloadedText(d: Download): string {
  const fraction = progressFraction(d);
  const bytes = formatBytes(d.received);
  return Number.isFinite(fraction) && d.state !== 'completed' ? `${bytes} (${formatPercent(fraction)})` : bytes;
}

/** "1.2 GB"; "~2.1 GB" while downexd estimates a stream's size; "Unknown" without one. */
export function sizeText(d: Download): string {
  return d.size >= 0 && (d.size > 0 || d.state === 'completed') ? formatSize(d) : 'Unknown';
}

/** The transfer rate while data arrives, else a dash. */
export function rateText(d: Download): string {
  return d.state === 'downloading' && !isAssembling(d) ? formatSpeed(d.speedBps) : DASH;
}

/**
 * The running time before the progress view shows an average speed: a download's first
 * seconds are mostly connecting, and their figure jumps with every update.
 */
export const AVERAGE_MIN_ACTIVE_MS = 3000;

/**
 * The average speed of the whole download in bytes per second: the bytes received over the
 * time all its runs took (pauses excluded, as downexd counts it in activeMs). NaN while that
 * time or the bytes are unknown.
 */
export function averageBps(d: Pick<Download, 'received' | 'activeMs'>): number {
  if (!(d.activeMs > 0) || !(d.received >= 0)) return Number.NaN;
  return d.received / (d.activeMs / 1000);
}

/**
 * The average shown beside the live rate ("7.6 MB/s"): only while data arrives, once the
 * download has run AVERAGE_MIN_ACTIVE_MS in all; "" otherwise.
 */
export function averageRateText(d: Download): string {
  if (d.state !== 'downloading' || isAssembling(d) || !(d.activeMs >= AVERAGE_MIN_ACTIVE_MS)) return '';
  const bps = averageBps(d);
  return bps > 0 ? formatSpeed(bps) : '';
}

export function timeLeftText(d: Download): string {
  if (d.state !== 'downloading' || isAssembling(d)) return DASH;
  const eta = etaSeconds(d);
  return Number.isFinite(eta) ? formatEta(eta) : DASH;
}

/**
 * When a running download should be done, on the locale's clock ("11:59", "11:59 AM"), from its
 * time left; "" when that is unknown (DESIGN-SYSTEM.md §4.8: "Done ≈ 11:59").
 */
export function doneAtText(d: Download, now: number = Date.now()): string {
  if (d.state !== 'downloading' || isAssembling(d)) return '';
  const eta = etaSeconds(d);
  if (!Number.isFinite(eta) || eta < 0) return '';
  return formatClock(now + eta * 1000);
}

/**
 * The header's big percentage: "42%", "~42%" while the size is an estimate, "" while the size is
 * unknown (DESIGN-SYSTEM.md §4.8).
 */
export function headerPercent(d: Download): string {
  const fraction = progressFraction(d);
  if (!Number.isFinite(fraction)) return '';
  return `${d.sizeApprox && d.state !== 'completed' ? '~' : ''}${formatPercent(fraction)}`;
}

/** "450 / 1800" for a media download (fragments done of all), else "". */
export function fragmentsText(d: Download): string {
  const m = d.media;
  if (d.kind !== 'media' || !m || !(m.fragmentsTotal > 0)) return '';
  return `${m.fragmentsDone} / ${m.fragmentsTotal}`;
}

/**
 * The chosen quality of a stream or a web page's video in plain words: "1080p · H.264 · AAC 128
 * kbit/s" (DESIGN-SYSTEM.md §4.8); "" for a file or when nothing is known.
 */
export function qualityText(d: Pick<Download, 'kind' | 'media'>): string {
  if (d.kind !== 'media' || !d.media) return '';
  return codecSummary(d.media);
}

/**
 * The details behind qualityText, for its tooltip: each chosen track's name, codecs, rate and
 * picture ("1080p (Full HD) · H.264 · 5.0 Mbit/s · 1920×1080 · 29.97 fps · English · 128 kbit/s
 * · mp4a.40.2"); "" for a file.
 */
export function qualityDetail(d: Pick<Download, 'kind' | 'media'>): string {
  if (d.kind !== 'media' || !d.media) return '';
  const t = chosenTracks(d.media);
  return [t.video, t.audio].filter((x) => x !== '').join(' · ');
}

/**
 * The Info column of a connection (from its state and the daemon's detail); a media
 * download's names its piece ("Fragment 3 · …", "Part 3 · …" for a web page's video).
 */
export function connectionText(c: ConnectionInfo, media: boolean, piece = 'Fragment'): string {
  const text = connectionStateText(c);
  return media && c.fragment >= 0 ? `${piece} ${c.fragment + 1} · ${text}` : text;
}

function connectionStateText(c: ConnectionInfo): string {
  switch (c.state) {
    case 'connecting':
      return 'Connecting…';
    case 'requesting':
      return 'Sending request…';
    case 'receiving':
      return 'Receiving data…';
    case 'retrying': {
      if (!c.detail) return 'Retrying…';
      // downexd says "<reason>, retrying in <wait>" (e.g. "HTTP 503, retrying in 4 s").
      const m = /^(.+?),\s*retrying in (.+)$/i.exec(c.detail);
      if (m) return `Retrying in ${m[2]} (${m[1]})`;
      return /retry/i.test(c.detail) ? c.detail : `Retrying (${c.detail})`;
    }
    case 'idle':
      return 'Idle';
    case 'done':
      return 'Finished';
    case 'error':
      return c.detail ? `Error: ${c.detail}` : 'Error';
    case 'unspecified':
      return c.detail || DASH;
  }
}

/** The Downloaded column: what the connection received of its current range. */
export function connectionReceived(c: ConnectionInfo): string {
  return formatBytes(Math.max(0, c.received));
}

// ---- the download complete dialog (DESKTOP-SPEC §8) ---------------------------------

/** A running time: "<1s", "45s", "3m 05s", "2h 07m 12s", "3d 04h 10m"; "—" when unknown. */
export function formatElapsed(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) return DASH;
  const s = Math.round(ms / 1000);
  if (s < 1) return '<1s';
  if (s < 60) return `${s}s`;
  const pad = (n: number) => String(n).padStart(2, '0');
  if (s < 3600) return `${Math.floor(s / 60)}m ${pad(s % 60)}s`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ${pad(Math.floor((s % 3600) / 60))}m ${pad(s % 60)}s`;
  return `${Math.floor(s / 86400)}d ${pad(Math.floor((s % 86400) / 3600))}h ${pad(Math.floor((s % 3600) / 60))}m`;
}

/**
 * "Time taken": the time the download spent running (pauses excluded) as downexd counts
 * it; without that, the time from its first start to its completion; else unknown.
 */
export function timeTakenText(d: Pick<Download, 'activeMs' | 'startedAt' | 'completedAt'>): string {
  if (d.activeMs > 0) return formatElapsed(d.activeMs);
  if (d.startedAt > 0 && d.completedAt >= d.startedAt) return formatElapsed(d.completedAt - d.startedAt);
  return 'Unknown';
}

/**
 * The completion view's "Time taken" with the average speed of the whole download over it
 * ("3m 00s · average 7.6 MB/s"); just timeTakenText when downexd did not count the running
 * time (the time from the first start includes pauses) or no bytes are known.
 */
export function timeTakenSummary(d: Pick<Download, 'activeMs' | 'startedAt' | 'completedAt' | 'received'>): string {
  const taken = timeTakenText(d);
  const bps = averageBps(d);
  return bps > 0 ? `${taken} · average ${formatSpeed(bps)}` : taken;
}

/**
 * A stream's format and the file it became ("HLS stream, saved as MP4"); a web page's video
 * and its file ("YouTube video, saved as MP4"); "" for files. The quality is qualityText.
 */
export function mediaFormatText(d: Pick<Download, 'kind' | 'media'>): string {
  if (d.kind !== 'media' || !d.media) return '';
  if (isSiteDownload(d)) {
    const container = containerLabel(d.media.container);
    return container ? `${siteVideoLabel(d.media)}, saved as ${container}` : siteVideoLabel(d.media);
  }
  const format = formatLabel(d.media.format);
  const container = containerLabel(d.media.container);
  if (format && container) return `${format} stream, saved as ${container}`;
  if (format) return `${format} stream`;
  return container ? `Saved as ${container}` : '';
}

/**
 * A failure that resuming cannot help with: Downex never downloads protected or live
 * streams, and a page without an MP4 video (or the chosen quality) stays so.
 */
export function isRefused(d: Pick<Download, 'errorCode'>): boolean {
  return cannotRetry(d.errorCode);
}
