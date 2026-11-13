// Direct MP4 files and the frames that play them (VIDEO-DETECT-SPEC §2.4, §2.5): the file
// classifier and its sizes, the redirect map, files of documents waiting for their commit, the
// tracker's element reports and the rate of `downex:frame-media-seen`. Observe-only: response
// headers and element state are read, never bodies. Pure: clocks are injected.

import { parseContentDisposition } from './capture-firefox';
import { headerValue, toHeaderPairs } from './context';
import {
  type Detection,
  MAX_FILE_SIZE,
  type RequestKind,
  type ResponseDetailsLike,
  baseDetection,
  httpUrl,
  passesGates,
  withoutFragment,
} from './media';
import { MAX_FRAME_VIDEOS } from './messages';
import type { FrameVideoMeta } from './offer-types';
import { mimeEssence } from './rules';

/** webRequest resource types a direct file is played or shown with (§2.4); scripts' requests never. */
export const FILE_REQUEST_TYPES = ['media', 'main_frame', 'sub_frame', 'object'] as const;
const FILE_REQUEST_TYPE_SET: ReadonlySet<string> = new Set(FILE_REQUEST_TYPES);
const DOCUMENT_TYPES: ReadonlySet<string> = new Set(['main_frame', 'sub_frame']);
/** Content-Types of a direct MP4 file. */
const MP4_TYPES: ReadonlySet<string> = new Set(['video/mp4', 'video/x-m4v']);
/** Content-Types that say nothing: the path or the Content-Disposition name decides. */
const UNTYPED: ReadonlySet<string> = new Set(['application/octet-stream', 'binary/octet-stream']);
/** An element of at most this many milliseconds plays a short clip (an ad, a preview). */
export const SHORT_CLIP_MS = 30_000;

/** Whether a name or path ends in `.mp4` or `.m4v`. */
function mp4Name(name: string | undefined): boolean {
  return name !== undefined && /\.(mp4|m4v)$/i.test(name.trim());
}

/** An http(s) URL whose path ends in `.mp4` or `.m4v` (an element's own `currentSrc`, §2.6). */
export function isMp4Path(url: string | undefined): boolean {
  if (!httpUrl(url)) return false;
  try {
    return mp4Name(new URL(url as string).pathname);
  } catch {
    return false;
  }
}

function sizeValue(digits: string | undefined): number | undefined {
  if (!digits || !/^[0-9]{1,15}$/.test(digits)) return undefined;
  const n = Number(digits);
  return n >= 1 && n <= MAX_FILE_SIZE ? n : undefined;
}

/** The total of `Content-Range: bytes a-b/TOTAL`; undefined for `*`, a bad header or out of 1–2⁴⁰. */
export function contentRangeTotal(header: string | undefined): number | undefined {
  if (!header || header.length > 128) return undefined;
  const m = /^\s*bytes\s+(?:[0-9]+-[0-9]+|\*)\s*\/\s*([0-9]+|\*)\s*$/i.exec(header);
  return m?.[1] === '*' ? undefined : sizeValue(m?.[1]);
}

/** A Content-Length of 1–2⁴⁰; undefined otherwise. */
export function contentLengthSize(header: string | undefined): number | undefined {
  return sizeValue(header?.trim());
}

/** A response's total size: from a 206's Content-Range, from a 200's Content-Length; 0 unknown. */
export function responseSize(status: number | undefined, headers: readonly [string, string][]): number {
  if (status === 206) return contentRangeTotal(headerValue(headers, 'content-range')) ?? 0;
  if (status === 200) return contentLengthSize(headerValue(headers, 'content-length')) ?? 0;
  return 0;
}

/** Firefox classified the request as an ad (`urlClassification`). */
function isAd(d: ResponseDetailsLike): boolean {
  const c = d.urlClassification;
  return [...(c?.firstParty ?? []), ...(c?.thirdParty ?? [])].includes('tracking_ad');
}

/**
 * A direct MP4 file a tab played or showed (VIDEO-DETECT-SPEC §2.4): a `media`, `main_frame`,
 * `sub_frame` or `object` GET answered 200 or 206 with `video/mp4` / `video/x-m4v`, or untyped
 * (`application/octet-stream`, `binary/octet-stream`, none) with a `.mp4`/`.m4v` path or
 * Content-Disposition name. A document's attachment is a download, never a file here. A 304 is a
 * refresh of a listed file only. Undefined for everything else: script requests (MSE segments),
 * other containers, errors, private windows, prerendered and fenced frames.
 */
export function classifyFile(d: ResponseDetailsLike): Detection | undefined {
  const type = d.type;
  if (type === undefined || !FILE_REQUEST_TYPE_SET.has(type)) return undefined;
  const status = d.statusCode;
  if (status !== 200 && status !== 206 && status !== 304) return undefined;
  const url = passesGates(d);
  if (!url) return undefined;
  const headers = toHeaderPairs(d.responseHeaders);
  const essence = mimeEssence(headerValue(headers, 'content-type'));
  const cd = parseContentDisposition(headerValue(headers, 'content-disposition'));
  if (DOCUMENT_TYPES.has(type) && cd?.type === 'attachment') return undefined;
  const typed = essence !== undefined && MP4_TYPES.has(essence);
  const untyped = essence === undefined || UNTYPED.has(essence);
  const refresh = status === 304;
  if (refresh) {
    // A 304 may carry no Content-Type; it only refreshes what is listed.
    if (!typed && !untyped) return undefined;
  } else if (!typed && !(untyped && (mp4Name(new URL(url).pathname) || mp4Name(cd?.filename)))) {
    return undefined;
  }
  const det = baseDetection(d, url, 'file', type as RequestKind);
  det.size = responseSize(status, headers);
  if (essence && essence.length <= 255) det.contentType = essence;
  if (cd?.filename) det.filename = cd.filename;
  if (isAd(d)) det.ad = true;
  if (refresh) det.refreshOnly = true;
  return det;
}

/**
 * The address (without fragment) of a file request (`media`, a document, an `object`) answered
 * 200 or 206 whose path ends in .mp4 or .m4v, for a response classifyFile refused (a WebM or MOV
 * named .mp4, an error page): the panel offers no element file for it (§2.6).
 */
export function refusedMp4Address(d: ResponseDetailsLike): string | undefined {
  if (d.type === undefined || !FILE_REQUEST_TYPE_SET.has(d.type)) return undefined;
  if (d.statusCode !== 200 && d.statusCode !== 206) return undefined;
  const url = passesGates(d);
  return url && isMp4Path(url) ? url : undefined;
}

/** How long a refused .mp4 address is remembered, and how many at most. */
export const REFUSED_FILE_TTL_MS = 10 * 60_000;
const MAX_REFUSED_FILES = 256;

/** The .mp4 addresses whose responses were seen and refused, per tab and frame. */
export class RefusedFiles {
  /** Map order: oldest first. */
  readonly #seen = new Map<string, number>();
  readonly #now: () => number;

  constructor(opts: { now?: () => number } = {}) {
    this.#now = opts.now ?? (() => Date.now());
  }

  add(tabId: number, frameId: number, url: string): void {
    const key = `${tabId}\n${frameId}\n${url}`;
    this.#seen.delete(key);
    this.#seen.set(key, this.#now());
    while (this.#seen.size > MAX_REFUSED_FILES) this.#seen.delete(this.#seen.keys().next().value as string);
  }

  has(tabId: number, frameId: number, url: string): boolean {
    const at = this.#seen.get(`${tabId}\n${frameId}\n${url}`);
    return at !== undefined && this.#now() - at <= REFUSED_FILE_TTL_MS;
  }

  /** The frames of a tab committed other documents (frame 0: the whole tab). */
  clearFrames(tabId: number, frames: Iterable<number>): void {
    const prefixes = [...frames].map((f) => `${tabId}\n${f}\n`);
    for (const key of [...this.#seen.keys()]) if (prefixes.some((p) => key.startsWith(p))) this.#seen.delete(key);
  }

  clearTab(tabId: number): void {
    for (const key of [...this.#seen.keys()]) if (key.startsWith(`${tabId}\n`)) this.#seen.delete(key);
  }
}

/**
 * Video traffic of a frame (a `media` request that is not audio, or a script's video answer),
 * after which a frame that reported no video is asked to look into its shadow roots (§2.5).
 * Audio alone (an `<audio>` element, a podcast site) is no reason to look for a video.
 */
export function isMediaTraffic(d: ResponseDetailsLike): boolean {
  if (!passesGates(d)) return false;
  const essence = mimeEssence(headerValue(toHeaderPairs(d.responseHeaders), 'content-type')) ?? '';
  if (essence.startsWith('audio/')) return false;
  return d.type === 'media' || essence.startsWith('video/');
}

// ---------------------------------------------------------------------------------------
// Redirects

export interface ClockOptions {
  ttlMs?: number;
  max?: number;
  now?: () => number;
}

/** How long the first URL of a redirect chain waits for the chain's response. */
export const REDIRECT_TTL_MS = 60_000;
export const MAX_REDIRECTS = 512;

/**
 * The first URL of each redirect chain by request id (observe-only `onBeforeRedirect`): at most
 * MAX_REDIRECTS for REDIRECT_TTL_MS, forgotten at `onCompleted`/`onErrorOccurred`.
 */
export class RedirectMap {
  /** Map order: oldest first. */
  readonly #entries = new Map<string, { url: string; at: number }>();
  readonly #ttlMs: number;
  readonly #max: number;
  readonly #now: () => number;

  constructor(opts: ClockOptions = {}) {
    this.#ttlMs = opts.ttlMs ?? REDIRECT_TTL_MS;
    this.#max = opts.max ?? MAX_REDIRECTS;
    this.#now = opts.now ?? (() => Date.now());
  }

  get size(): number {
    return this.#entries.size;
  }

  /** A request at `url` was redirected; the chain's first URL (http(s), no fragment) is kept. */
  redirected(requestId: string, url: string): void {
    if (!requestId || requestId.length > 64) return;
    this.#prune();
    if (this.#entries.has(requestId)) return;
    const first = httpUrl(withoutFragment(url));
    if (!first) return;
    this.#entries.set(requestId, { url: first, at: this.#now() });
    while (this.#entries.size > this.#max) this.#entries.delete(this.#entries.keys().next().value as string);
  }

  /** The first URL of the chain of `requestId`; undefined when it was not redirected. */
  first(requestId: string | undefined): string | undefined {
    if (!requestId) return undefined;
    const e = this.#entries.get(requestId);
    if (!e) return undefined;
    if (this.#now() - e.at > this.#ttlMs) {
      this.#entries.delete(requestId);
      return undefined;
    }
    return e.url;
  }

  /** The request ended (`onCompleted`, `onErrorOccurred`). */
  done(requestId: string | undefined): void {
    if (requestId) this.#entries.delete(requestId);
  }

  #prune(): void {
    const now = this.#now();
    for (const [id, e] of this.#entries) {
      if (now - e.at <= this.#ttlMs) break;
      this.#entries.delete(id);
    }
  }
}

// ---------------------------------------------------------------------------------------
// Documents that are the file

export const PENDING_DOCUMENT_TTL_MS = 30_000;
export const MAX_PENDING_DOCUMENTS = 32;

/**
 * Files of `main_frame`/`sub_frame` responses (a document that is the file), held until their
 * frame commits that URL (§2.4): the commit clears the frame first, then the file is recorded.
 * A response that never commits (a download, a blocked navigation) is dropped: at the frame's
 * next commit, after PENDING_DOCUMENT_TTL_MS, or beyond MAX_PENDING_DOCUMENTS.
 */
export class PendingDocuments<T extends { tabId: number; frameId: number; url: string }> {
  /** Map order: oldest first. */
  readonly #entries = new Map<string, { value: T; at: number }>();
  readonly #ttlMs: number;
  readonly #max: number;
  readonly #now: () => number;

  constructor(opts: ClockOptions = {}) {
    this.#ttlMs = opts.ttlMs ?? PENDING_DOCUMENT_TTL_MS;
    this.#max = opts.max ?? MAX_PENDING_DOCUMENTS;
    this.#now = opts.now ?? (() => Date.now());
  }

  get size(): number {
    return this.#entries.size;
  }

  add(value: T): void {
    const key = `${value.tabId}\n${value.frameId}`;
    this.#entries.delete(key);
    this.#entries.set(key, { value, at: this.#now() });
    while (this.#entries.size > this.#max) this.#entries.delete(this.#entries.keys().next().value as string);
  }

  /** The frame committed `url`: its pending file when it is that document, else undefined. */
  take(tabId: number, frameId: number, url: string): T | undefined {
    const key = `${tabId}\n${frameId}`;
    const e = this.#entries.get(key);
    this.#entries.delete(key);
    if (!e || this.#now() - e.at > this.#ttlMs) return undefined;
    return e.value.url === withoutFragment(url) ? e.value : undefined;
  }

  clearTab(tabId: number): void {
    for (const key of [...this.#entries.keys()]) if (key.startsWith(`${tabId}\n`)) this.#entries.delete(key);
  }
}

// ---------------------------------------------------------------------------------------
// Element reports (`downex:frame-videos`)

/** Most frames of a tab whose reports are kept; the least recently reporting frame goes first. */
export const MAX_REPORT_FRAMES = 32;

export interface FrameReport {
  frameId: number;
  /** Chromium: the reporting document. */
  documentId?: string;
  videos: readonly FrameVideoMeta[];
  /** Epoch ms of the report. */
  at: number;
}

/** A video a look-ahead or the badge may count: meaningful, not protected and not live. */
export function playable(v: FrameVideoMeta): boolean {
  return v.meaningful && !v.drm && v.durationMs !== -2;
}

/** A frame whose reported videos are all protected (DRM): the popup shows its streams as such (§5.3). */
export function allProtected(report: FrameReport | undefined): boolean {
  const videos = report?.videos ?? [];
  return videos.length > 0 && videos.every((v) => v.drm);
}

/** A frame whose reported videos are all live: the popup leaves its items out (§5.3). */
export function allLive(report: FrameReport | undefined): boolean {
  const videos = report?.videos ?? [];
  return videos.length > 0 && videos.every((v) => v.durationMs === -2);
}

/** A frame whose reported videos are all protected, or all live: its items are not counted (§5.3). */
export function protectedOrLive(report: FrameReport | undefined): boolean {
  return allProtected(report) || allLive(report);
}

/**
 * The trackers' element reports, in memory only, per tab and frame (the latest document's):
 * at most MAX_FRAME_VIDEOS videos per frame and MAX_REPORT_FRAMES frames per tab. A frame's
 * commit clears its report and those of the frames below it (as earlier commits named them).
 */
export class FrameReports {
  /** Per tab; map order: least recently reporting frame first. */
  readonly #tabs = new Map<number, Map<number, FrameReport>>();
  /** Per tab: the parent of each frame, from commits. */
  readonly #parents = new Map<number, Map<number, number>>();
  readonly #maxFrames: number;

  constructor(opts: { maxFrames?: number } = {}) {
    this.#maxFrames = opts.maxFrames ?? MAX_REPORT_FRAMES;
  }

  /** A frame's report; returns whether it differs from the one kept. */
  set(tabId: number, frameId: number, documentId: string | undefined, videos: readonly FrameVideoMeta[], now: number): boolean {
    let frames = this.#tabs.get(tabId);
    if (!frames) {
      frames = new Map();
      this.#tabs.set(tabId, frames);
    }
    const prev = frames.get(frameId);
    const report: FrameReport = { frameId, videos: videos.slice(0, MAX_FRAME_VIDEOS).map((v) => ({ ...v })), at: now };
    if (documentId) report.documentId = documentId;
    frames.delete(frameId);
    frames.set(frameId, report);
    while (frames.size > this.#maxFrames) frames.delete(frames.keys().next().value as number);
    return !prev || prev.documentId !== report.documentId || JSON.stringify(prev.videos) !== JSON.stringify(report.videos);
  }

  frame(tabId: number, frameId: number): FrameReport | undefined {
    return this.#tabs.get(tabId)?.get(frameId);
  }

  /** The tab's reports. */
  tab(tabId: number): FrameReport[] {
    return [...(this.#tabs.get(tabId)?.values() ?? [])];
  }

  /** Whether some frame of the tab reported a meaningful video that is neither protected nor live. */
  playable(tabId: number): boolean {
    return this.tab(tabId).some((r) => r.videos.some(playable));
  }

  /**
   * A frame of the tab committed another document: its report and those of the frames below it
   * are gone (the top frame: every report of the tab). Returns the frames cleared.
   */
  committed(tabId: number, frameId: number, parentFrameId?: number): Set<number> {
    if (frameId === 0) {
      const all = new Set([0, ...(this.#tabs.get(tabId)?.keys() ?? []), ...(this.#parents.get(tabId)?.keys() ?? [])]);
      this.clearTab(tabId);
      return all;
    }
    const parents = this.#parents.get(tabId) ?? new Map<number, number>();
    const gone = new Set([frameId]);
    for (let grew = true; grew; ) {
      grew = false;
      for (const [child, parent] of parents) {
        if (gone.has(parent) && !gone.has(child)) {
          gone.add(child);
          grew = true;
        }
      }
    }
    this.clearFrames(tabId, gone);
    if (parentFrameId !== undefined && parentFrameId >= 0) {
      parents.set(frameId, parentFrameId);
      this.#parents.set(tabId, parents);
    }
    return gone;
  }

  /** Forgets the reports (and parents) of these frames. */
  clearFrames(tabId: number, frameIds: Iterable<number>): void {
    const frames = this.#tabs.get(tabId);
    const parents = this.#parents.get(tabId);
    for (const f of frameIds) {
      frames?.delete(f);
      parents?.delete(f);
    }
    if (frames?.size === 0) this.#tabs.delete(tabId);
    if (parents?.size === 0) this.#parents.delete(tabId);
  }

  clearTab(tabId: number): void {
    this.#tabs.delete(tabId);
    this.#parents.delete(tabId);
  }

  replace(removedTabId: number, addedTabId: number): void {
    const frames = this.#tabs.get(removedTabId);
    const parents = this.#parents.get(removedTabId);
    this.clearTab(removedTabId);
    if (frames) this.#tabs.set(addedTabId, frames);
    if (parents) this.#parents.set(addedTabId, parents);
  }

  clearAll(): void {
    this.#tabs.clear();
    this.#parents.clear();
  }
}

// ---------------------------------------------------------------------------------------
// `downex:frame-media-seen`

/** At most one `downex:frame-media-seen` per frame per this long (§2.5) … */
export const FRAME_MEDIA_SEEN_MS = 5000;
/** … twice as long after each time (the frame's deep scans found nothing), up to this long. */
export const FRAME_MEDIA_SEEN_MAX_MS = 5 * 60_000;
const MAX_SEEN_FRAMES = 512;

/**
 * When a frame may next be told about its media traffic: FRAME_MEDIA_SEEN_MS after the first
 * time, then twice as long each time, up to FRAME_MEDIA_SEEN_MAX_MS, until the tab is cleared.
 * A frame keeps being told only while it reports no video, and each time costs it a deep scan.
 */
export class FrameMediaSeen {
  /** Map order: least recently told first. */
  readonly #last = new Map<string, { at: number; wait: number }>();
  readonly #intervalMs: number;
  readonly #now: () => number;

  constructor(opts: { intervalMs?: number; now?: () => number } = {}) {
    this.#intervalMs = opts.intervalMs ?? FRAME_MEDIA_SEEN_MS;
    this.#now = opts.now ?? (() => Date.now());
  }

  /** Whether the frame may be told now (and records that it is). */
  due(tabId: number, frameId: number): boolean {
    const key = `${tabId}\n${frameId}`;
    const now = this.#now();
    const last = this.#last.get(key);
    if (last !== undefined && now - last.at < last.wait) return false;
    this.#last.delete(key);
    this.#last.set(key, { at: now, wait: last === undefined ? this.#intervalMs : Math.min(2 * last.wait, FRAME_MEDIA_SEEN_MAX_MS) });
    while (this.#last.size > MAX_SEEN_FRAMES) this.#last.delete(this.#last.keys().next().value as string);
    return true;
  }

  clearTab(tabId: number): void {
    for (const key of [...this.#last.keys()]) if (key.startsWith(`${tabId}\n`)) this.#last.delete(key);
  }
}
