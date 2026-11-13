// What a tab played, from the browser's own traffic (MEDIA-SPEC §6.1, VIDEO-DETECT-SPEC §2.4):
// HLS and DASH manifests (streams) and direct MP4 files (files, classified in frame-media.ts), per
// tab, frame and document, at most MAX_MEDIA_PER_TAB, cleared when their frame navigates or the
// tab closes. Kept in storage.session so a restarted service worker still lists them.
// Observe-only; pure: browser APIs and storage are injected.

import { type WebRequestHeader, headerValue, toHeaderPairs } from './context';
import type { OfferKey } from './offer-types';
import { mimeEssence } from './rules';
import { type AddedResult, type HeaderPair, type Outcome, utf8Length } from './protocol';
import { isTabId } from './tab-pause';

export type MediaFormat = 'hls' | 'dash';
/** A stream's format, or `file`: a direct MP4 file. */
export type ItemFormat = MediaFormat | 'file';
/** webRequest resource types an item's response may have. */
export type RequestKind = 'media' | 'main_frame' | 'sub_frame' | 'object' | 'xmlhttprequest' | 'other';

/** Response Content-Types of HLS playlists (RFC 8216 §4) and DASH MPDs (ISO/IEC 23009-1 Annex C). */
export const HLS_CONTENT_TYPES: ReadonlySet<string> = new Set([
  'application/vnd.apple.mpegurl',
  'application/x-mpegurl',
  'audio/mpegurl',
]);
export const DASH_CONTENT_TYPE = 'application/dash+xml';

/** webRequest resource types a player loads manifests with (fetch/XHR, native playback). */
export const MEDIA_REQUEST_TYPES = ['xmlhttprequest', 'media', 'other'] as const;
const MEDIA_REQUEST_TYPE_SET: ReadonlySet<string> = new Set(MEDIA_REQUEST_TYPES);
const REQUEST_KINDS: ReadonlySet<string> = new Set(['media', 'main_frame', 'sub_frame', 'object', 'xmlhttprequest', 'other']);

/** Items of all formats a tab keeps. */
export const MAX_MEDIA_PER_TAB = 50;
/** Files one frame keeps; segment-like ones go first beyond it (VIDEO-DETECT-SPEC §2.4). */
export const MAX_FILES_PER_FRAME = 16;
/** Tabs whose items are kept; the least recently updated tab is dropped beyond it. */
export const MAX_MEDIA_TABS = 100;
/**
 * Upper bound for what the items take in storage.session, which history, paused tabs and link
 * batches share: past it the oldest items lose their headers, then the oldest go.
 */
export const MAX_STORED_MEDIA_BYTES = 2 * 1024 * 1024;
/** Longest manifest or page URL the daemon accepts (DAEMON-SPEC §4). */
export const MAX_MEDIA_URL_BYTES = 32 * 1024;
/** Largest file size believed (1 TiB); anything else is unknown. */
export const MAX_FILE_SIZE = 2 ** 40;
/** A file smaller than this is short: listed last and not counted (VIDEO-DETECT-SPEC §2.4). */
export const SHORT_FILE_BYTES = 2 * 1024 * 1024;
/** This many files of one frame in one directory within SEGMENT_WINDOW_MS are a stream's segments … */
export const SEGMENT_RUN = 4;
export const SEGMENT_WINDOW_MS = 60_000;
/** … unless larger than this: a gallery's clips, not a stream's segments of a few seconds. */
export const SEGMENT_MAX_BYTES = 16 * 1024 * 1024;
/** Largest sequence number of a key (OFFER_KEY_RE: at most 12 digits). */
const MAX_SEQ = 999_999_999_999;
const MAX_CONTENT_TYPE_BYTES = 255;
const MAX_STORED_TITLE_CHARS = 1024;
const MAX_FILENAME_CHARS = 1024;
const MAX_DOCUMENT_ID_CHARS = 128;
/** storage.session key of a tab's items: `media:<tabId>`. */
export const MEDIA_STORAGE_PREFIX = 'media:';

/** Format of a response: its Content-Type, else a `.m3u8` / `.mpd` URL path. */
export function mediaFormat(url: string, contentType: string | undefined): MediaFormat | undefined {
  let u: URL;
  try {
    u = new URL(url);
  } catch {
    return undefined;
  }
  if (u.protocol !== 'http:' && u.protocol !== 'https:') return undefined;
  const essence = mimeEssence(contentType);
  if (essence && HLS_CONTENT_TYPES.has(essence)) return 'hls';
  if (essence === DASH_CONTENT_TYPE) return 'dash';
  // An HTML answer at a manifest path is an error or login page, not a manifest.
  if (essence === 'text/html') return undefined;
  const path = u.pathname.toLowerCase();
  if (path.endsWith('.m3u8')) return 'hls';
  if (path.endsWith('.mpd')) return 'dash';
  return undefined;
}

/** The fields of webRequest.onResponseStarted details the detectors read. */
export interface ResponseDetailsLike {
  url: string;
  method: string;
  tabId: number;
  /** Resource type (`media`, `main_frame`, …); missing: an older caller, read as a script's request. */
  type?: string;
  requestId?: string;
  statusCode?: number;
  responseHeaders?: readonly WebRequestHeader[];
  /** The frame that sent the request (0 = top; < 0: the browser names none). */
  frameId?: number;
  parentFrameId?: number;
  /** Chromium: the document that sent the request (absent for navigations and on Firefox). */
  documentId?: string;
  /** Chromium: `outermost_frame`, `fenced_frame` or `sub_frame`. */
  frameType?: string;
  /** Firefox. */
  incognito?: boolean;
  /** Firefox container of the request. */
  cookieStoreId?: string;
  /** Firefox: URL of the document that sent the request (an embedded player's frame). */
  documentUrl?: string;
  /** Chromium: `active`, `prerender`, `cached` or `pending_deletion`. */
  documentLifecycle?: string;
  /** Firefox: the request's tracking classification (`tracking_ad`, …). */
  urlClassification?: { firstParty?: readonly string[]; thirdParty?: readonly string[] };
}

/** Where an item was seen: its frame (0 = top) and, on Chromium, its document. */
export interface FrameRef {
  frameId: number;
  /** -1 for the top frame. */
  parentFrameId: number;
  documentId?: string;
}

/** The frame of a request; a request the browser names no frame for (a worker's) is the top frame's. */
export function frameOf(d: Pick<ResponseDetailsLike, 'frameId' | 'parentFrameId' | 'documentId'>): FrameRef {
  const frameId = typeof d.frameId === 'number' && Number.isSafeInteger(d.frameId) && d.frameId > 0 ? d.frameId : 0;
  const parent = frameId === 0 ? -1 : d.parentFrameId;
  const ref: FrameRef = { frameId, parentFrameId: typeof parent === 'number' && Number.isSafeInteger(parent) && parent >= 0 ? parent : -1 };
  if (typeof d.documentId === 'string' && d.documentId !== '' && d.documentId.length <= MAX_DOCUMENT_ID_CHARS) ref.documentId = d.documentId;
  return ref;
}

/** What both detectors recognise of a response. */
export interface Detection extends FrameRef {
  /** URL without fragment: the dedupe key within a frame and document. */
  url: string;
  format: ItemFormat;
  tabId: number;
  reqType: RequestKind;
  /** Content-Type essence of the response. */
  contentType?: string;
  storeId?: string;
  /** http(s) URL of the requesting document (Referer fallback when no headers were recorded). */
  frameUrl?: string;
  /** Files: total bytes when the response says (0 = unknown). */
  size?: number;
  /** Files: the Content-Disposition file name. */
  filename?: string;
  /** Files: the browser classified the request as an ad (Firefox `tracking_ad`). */
  ad?: boolean;
  /** Files: a 304, which refreshes an item already listed and adds none. */
  refreshOnly?: boolean;
}

export function withoutFragment(url: string): string | undefined {
  try {
    const u = new URL(url);
    u.hash = '';
    return u.href;
  } catch {
    return undefined;
  }
}

export function httpUrl(v: string | undefined): string | undefined {
  if (!v || utf8Length(v) > MAX_MEDIA_URL_BYTES) return undefined;
  try {
    const p = new URL(v).protocol;
    return p === 'http:' || p === 'https:' ? v : undefined;
  } catch {
    return undefined;
  }
}

/**
 * The gates both detectors share (VIDEO-DETECT-SPEC §2.4): a GET of a tab of the active page
 * (no private window, no prerendered or unloading document, no fenced frame), answered 2xx or
 * 304, at an http(s) URL of at most 32 KiB. Returns that URL without fragment, else undefined.
 */
export function passesGates(d: ResponseDetailsLike): string | undefined {
  if (!isTabId(d.tabId) || d.incognito) return undefined;
  if (d.method.toUpperCase() !== 'GET') return undefined;
  if (d.documentLifecycle !== undefined && d.documentLifecycle !== 'active') return undefined;
  if (d.frameType === 'fenced_frame') return undefined;
  const s = d.statusCode;
  if (s !== undefined && !((s >= 200 && s < 300) || s === 304)) return undefined;
  const url = httpUrl(withoutFragment(d.url));
  return url;
}

/** The detection fields every item kind shares. */
export function baseDetection(d: ResponseDetailsLike, url: string, format: ItemFormat, reqType: RequestKind): Detection {
  const det: Detection = { url, format, tabId: d.tabId, reqType, ...frameOf(d) };
  if (d.cookieStoreId) det.storeId = d.cookieStoreId;
  const frame = httpUrl(d.documentUrl);
  if (frame) det.frameUrl = frame;
  return det;
}

/**
 * An HLS or DASH manifest a player of a tab loaded (fetch/XHR, native playback); undefined for
 * everything else (no tab, private window, prerendered or unloading documents, errors).
 */
export function detectMedia(d: ResponseDetailsLike): Detection | undefined {
  const reqType = d.type ?? 'xmlhttprequest';
  if (!MEDIA_REQUEST_TYPE_SET.has(reqType)) return undefined;
  const url = passesGates(d);
  if (!url) return undefined;
  const contentType = headerValue(toHeaderPairs(d.responseHeaders), 'content-type');
  const format = mediaFormat(url, contentType);
  if (!format) return undefined;
  const det = baseDetection(d, url, format, reqType as RequestKind);
  const essence = mimeEssence(contentType);
  if (essence && essence.length <= MAX_CONTENT_TYPE_BYTES) det.contentType = essence;
  return det;
}

// ---------------------------------------------------------------------------------------
// Per-tab store

export interface MediaItem extends FrameRef {
  /** Final URL without fragment. */
  url: string;
  format: ItemFormat;
  /** Epoch ms first and last seen in this frame. */
  firstSeen: number;
  lastSeen: number;
  contentType?: string;
  /** Cookie store (Firefox container) of the request. */
  storeId?: string;
  frameUrl?: string;
  /** Top-level page and its title when the item was first seen. */
  pageUrl?: string;
  pageTitle?: string;
  /**
   * Request headers the page sent (filtered for replay; of the first URL of a redirect chain).
   * Missing when not recorded or dropped for space.
   */
  headers?: HeaderPair[];
  /** Files: the first URL of the redirect chain that ended at `url`. */
  requestUrl?: string;
  /** Files: total bytes (0 = unknown). */
  size: number;
  /** Files: the Content-Disposition file name. */
  filename?: string;
  reqType: RequestKind;
  /** Files: the browser classified the request as an ad. */
  ad?: true;
  /** Files: de-prioritised (under SHORT_FILE_BYTES, or an ad): listed last, not counted. */
  short: boolean;
  /** Files: one of SEGMENT_RUN files of a directory: a stream's segment, never listed or counted. */
  segment: boolean;
  /** Number of the item's key (`f<seq>` / `s<seq>`), unique among the worker's items. */
  seq: number;
}

/** What the popup is told about a stream: never its headers. */
export interface MediaEntry {
  url: string;
  format: MediaFormat;
  firstSeen: number;
  lastSeen: number;
  pageUrl?: string;
  pageTitle?: string;
  /** An `addMedia` of the stream is still running (sent from a popup that may have closed). */
  sending?: true;
  /** The answer to the stream's last `addMedia`, kept for a while (MEDIA_RESULT_KEEP_MS). */
  result?: Outcome<AddedResult>;
}

/** The popup entry of a stream item. */
export function toMediaEntry(item: MediaItem): MediaEntry {
  const format: MediaFormat = item.format === 'dash' ? 'dash' : 'hls';
  const e: MediaEntry = { url: item.url, format, firstSeen: item.firstSeen, lastSeen: item.lastSeen };
  if (item.pageUrl) e.pageUrl = item.pageUrl;
  if (item.pageTitle) e.pageTitle = item.pageTitle;
  return e;
}

/** A stream (manifest) item rather than a file. */
export function isStream(item: Pick<MediaItem, 'format'>): boolean {
  return item.format !== 'file';
}

/** The key a panel and the popup name an item by: `f<seq>` a file, `s<seq>` a stream. */
export function itemKey(item: Pick<MediaItem, 'format' | 'seq'>): OfferKey {
  return `${item.format === 'file' ? 'f' : 's'}${item.seq}`;
}

/** Origin and directory of a URL (`https://cdn.example/v/1/`), for the segment rule. */
export function directoryOf(url: string): string | undefined {
  try {
    const u = new URL(url);
    return u.origin + u.pathname.slice(0, u.pathname.lastIndexOf('/') + 1);
  } catch {
    return undefined;
  }
}

function originAndPath(url: string): string | undefined {
  try {
    const u = new URL(url);
    return u.origin + u.pathname;
  } catch {
    return undefined;
  }
}

/** Whether a file is short by what the network said (its size or an ad classification). */
export function shortBySize(item: Pick<MediaItem, 'size' | 'ad'>): boolean {
  return item.ad === true || (item.size > 0 && item.size < SHORT_FILE_BYTES);
}

/**
 * The frames whose items go with `frameIds`: those frames and every frame below them, as the
 * items' parent frames say.
 */
export function frameFamily(items: readonly FrameRef[], frameIds: Iterable<number>): Set<number> {
  const gone = new Set(frameIds);
  for (let grew = true; grew; ) {
    grew = false;
    for (const x of items) {
      if (gone.has(x.parentFrameId) && !gone.has(x.frameId)) {
        gone.add(x.frameId);
        grew = true;
      }
    }
  }
  return gone;
}

/** The storage.StorageArea subset the store uses (storage.session in the background). */
export interface MediaStorageArea {
  get(keys: null): Promise<Record<string, unknown>>;
  set(items: Record<string, unknown>): Promise<void>;
  remove(keys: string[]): Promise<void>;
}

export function mediaStorageKey(tabId: number): string {
  return `${MEDIA_STORAGE_PREFIX}${tabId}`;
}

function tabIdOfKey(key: string): number | undefined {
  if (!key.startsWith(MEDIA_STORAGE_PREFIX)) return undefined;
  const rest = key.slice(MEDIA_STORAGE_PREFIX.length);
  if (!/^\d{1,15}$/.test(rest)) return undefined;
  const id = Number(rest);
  return isTabId(id) ? id : undefined;
}

function isHeaderPairs(v: unknown): v is HeaderPair[] {
  return (
    Array.isArray(v) &&
    v.every((h) => Array.isArray(h) && h.length === 2 && typeof h[0] === 'string' && typeof h[1] === 'string')
  );
}

function optionalString(v: unknown, max: number): string | undefined {
  return typeof v === 'string' && v !== '' && v.length <= max ? v : undefined;
}

function frameNumber(v: unknown, fallback: number): number {
  return typeof v === 'number' && Number.isSafeInteger(v) && v >= 0 ? v : fallback;
}

/** A stored item; items stored before files and frames existed read as streams of the top frame. */
function parseItem(v: unknown): MediaItem | undefined {
  if (typeof v !== 'object' || v === null) return undefined;
  const r = v as Record<string, unknown>;
  const url = typeof r.url === 'string' ? httpUrl(r.url) : undefined;
  if (!url || (r.format !== 'hls' && r.format !== 'dash' && r.format !== 'file')) return undefined;
  if (typeof r.firstSeen !== 'number' || !Number.isFinite(r.firstSeen)) return undefined;
  if (typeof r.lastSeen !== 'number' || !Number.isFinite(r.lastSeen)) return undefined;
  const frameId = frameNumber(r.frameId, 0);
  const file = r.format === 'file';
  const reqType = typeof r.reqType === 'string' && REQUEST_KINDS.has(r.reqType) ? (r.reqType as RequestKind) : file ? 'media' : 'xmlhttprequest';
  const size = typeof r.size === 'number' && Number.isSafeInteger(r.size) && r.size > 0 && r.size <= MAX_FILE_SIZE ? r.size : 0;
  const seq = typeof r.seq === 'number' && Number.isSafeInteger(r.seq) && r.seq >= 1 && r.seq <= MAX_SEQ ? r.seq : 0;
  const item: MediaItem = {
    url,
    format: r.format,
    firstSeen: r.firstSeen,
    lastSeen: r.lastSeen,
    frameId,
    parentFrameId: frameId === 0 ? -1 : frameNumber(r.parentFrameId, -1),
    size: file ? size : 0,
    reqType,
    short: false,
    segment: file && r.segment === true,
    seq,
  };
  const documentId = optionalString(r.documentId, MAX_DOCUMENT_ID_CHARS);
  if (documentId) item.documentId = documentId;
  const contentType = optionalString(r.contentType, MAX_CONTENT_TYPE_BYTES);
  if (contentType) item.contentType = contentType;
  const storeId = optionalString(r.storeId, 256);
  if (storeId) item.storeId = storeId;
  const frameUrl = typeof r.frameUrl === 'string' ? httpUrl(r.frameUrl) : undefined;
  if (frameUrl) item.frameUrl = frameUrl;
  const pageUrl = typeof r.pageUrl === 'string' ? httpUrl(r.pageUrl) : undefined;
  if (pageUrl) item.pageUrl = pageUrl;
  const pageTitle = optionalString(r.pageTitle, MAX_STORED_TITLE_CHARS);
  if (pageTitle) item.pageTitle = pageTitle;
  if (isHeaderPairs(r.headers)) item.headers = r.headers;
  if (file) {
    const requestUrl = typeof r.requestUrl === 'string' ? httpUrl(r.requestUrl) : undefined;
    if (requestUrl && requestUrl !== url) item.requestUrl = requestUrl;
    const filename = optionalString(r.filename, MAX_FILENAME_CHARS);
    if (filename) item.filename = filename;
    if (r.ad === true) item.ad = true;
    item.short = shortBySize(item);
  }
  return item;
}

/** Whether two sightings are of the same document of the same frame (a missing document id matches). */
function sameFrame(a: FrameRef, b: FrameRef): boolean {
  return a.frameId === b.frameId && (a.documentId === undefined || b.documentId === undefined || a.documentId === b.documentId);
}

function sameItem(a: MediaItem, b: Pick<MediaItem, 'url' | 'format'> & FrameRef): boolean {
  return a.url === b.url && isStream(a) === isStream(b) && sameFrame(a, b);
}

/** Stored items of one tab: valid ones, newest first, unique per frame and document, at most `max`. */
export function parseMediaItems(raw: unknown, max: number = MAX_MEDIA_PER_TAB): MediaItem[] {
  if (!Array.isArray(raw)) return [];
  const items = raw.map(parseItem).filter((x): x is MediaItem => x !== undefined);
  items.sort((a, b) => b.lastSeen - a.lastSeen);
  const out: MediaItem[] = [];
  for (const it of items) {
    if (out.some((x) => sameItem(x, it))) continue;
    out.push(it);
    if (out.length >= max) break;
  }
  return out;
}

/** Approximate stored size of an item (JSON characters). */
export function itemWeight(item: Pick<MediaItem, 'url'> & Partial<MediaItem>): number {
  let n = 160 + item.url.length;
  for (const s of [item.contentType, item.storeId, item.frameUrl, item.pageUrl, item.pageTitle, item.requestUrl, item.filename, item.documentId]) {
    n += s?.length ?? 0;
  }
  for (const [k, v] of item.headers ?? []) n += k.length + v.length + 8;
  return n;
}

export interface MediaStoreOptions {
  /**
   * Whether headers (cookies) are written to storage. Only for storage.session, which is held
   * in memory; with another area they stay in memory and are lost when the worker stops.
   */
  persistHeaders?: boolean;
  maxPerTab?: number;
  maxFilesPerFrame?: number;
  maxTabs?: number;
  maxBytes?: number;
  /**
   * What a tab lists changed: an item added, removed or turned into a segment, a size learned
   * (also called for every tab once the store was read). `count`: the tab's items.
   */
  onChange?: (tabId: number, count: number) => void;
}

export interface RecordInput extends Detection {
  /** Epoch ms of the response. */
  seenAt: number;
  /** Headers the request was sent with; undefined when not recorded. */
  headers?: HeaderPair[];
  /** Files: the first URL of the redirect chain (when it differs from `url`). */
  requestUrl?: string;
}

/** What one sighting changed. */
export interface RecordResult {
  /** The item's key. */
  key: OfferKey;
  /** A new item (a new document or a new URL in it). */
  added: boolean;
  /** Something the popup or a panel shows changed; only then was storage written. */
  shown: boolean;
}

export interface PageInfo {
  url?: string;
  title?: string;
}

/** A change applied to the store: the tabs it changed, and whether that is worth a write now. */
interface Change<T> {
  tabs?: Iterable<number>;
  /** Write now; otherwise the tabs are written with the next change that is (a sighting again). */
  flush?: boolean;
  value?: T;
}

/**
 * Items per tab, most recently seen first. Changes apply in call order once the stored items
 * were read (also those made before). Storage is written, coalesced, one tab per key, only when
 * something shown changes: the next range request of a playing file updates the item in memory
 * only. Promises resolve once written and reject when writing failed (the memory state stays and
 * is written next time).
 */
export class MediaStore {
  /** Map order: least recently updated tab first. */
  readonly #tabs = new Map<number, MediaItem[]>();
  /** Tabs to write now. */
  readonly #dirty = new Set<number>();
  /** Tabs changed in memory only (a sighting again): written with the next write that is due. */
  readonly #stale = new Set<number>();
  readonly #persistHeaders: boolean;
  readonly #maxPerTab: number;
  readonly #maxFilesPerFrame: number;
  readonly #maxTabs: number;
  readonly #maxBytes: number;
  readonly #onChange: ((tabId: number, count: number) => void) | undefined;
  #flushing: Promise<void> | undefined;
  #loaded = false;
  #seq = 1;
  /** Resolves once the stored items were read (an unreadable store counts as empty). */
  readonly ready: Promise<void>;

  constructor(
    private readonly area: MediaStorageArea,
    opts: MediaStoreOptions = {},
  ) {
    this.#persistHeaders = opts.persistHeaders ?? true;
    this.#maxPerTab = opts.maxPerTab ?? MAX_MEDIA_PER_TAB;
    this.#maxFilesPerFrame = opts.maxFilesPerFrame ?? MAX_FILES_PER_FRAME;
    this.#maxTabs = opts.maxTabs ?? MAX_MEDIA_TABS;
    this.#maxBytes = opts.maxBytes ?? MAX_STORED_MEDIA_BYTES;
    this.#onChange = opts.onChange;
    this.ready = area
      .get(null)
      .then(
        (got) => {
          const loaded: [number, MediaItem[]][] = [];
          for (const [key, value] of Object.entries(got)) {
            const tabId = tabIdOfKey(key);
            if (tabId === undefined) continue;
            const items = parseMediaItems(value, this.#maxPerTab);
            if (items.length > 0) loaded.push([tabId, items]);
          }
          loaded.sort((a, b) => (a[1][0]?.lastSeen ?? 0) - (b[1][0]?.lastSeen ?? 0));
          // Keys stay unique: a missing or repeated number gets a new one above all others.
          const used = new Set<number>();
          for (const [, items] of loaded) for (const it of items) this.#seq = Math.max(this.#seq, it.seq + 1);
          for (const [tabId, items] of loaded) {
            for (const it of items) {
              if (it.seq === 0 || used.has(it.seq)) it.seq = this.#nextSeq();
              used.add(it.seq);
            }
            this.#tabs.set(tabId, items);
          }
          this.#enforceLimits();
        },
        () => undefined,
      )
      .finally(() => {
        this.#loaded = true;
        for (const [tabId, items] of this.#tabs) this.#notify(tabId, items.length);
      });
  }

  get loaded(): boolean {
    return this.#loaded;
  }

  /** Items of a tab right now (0 until the store was read), without those `hidden` leaves out. */
  count(tabId: number, hidden?: (url: string) => boolean): number {
    const items = this.#tabs.get(tabId) ?? [];
    return hidden ? items.filter((it) => !hidden(it.url)).length : items.length;
  }

  /** [tabId, count] of every tab with items. */
  counts(): [number, number][] {
    return [...this.#tabs].map(([tabId, items]) => [tabId, items.length]);
  }

  /** Copies of a tab's items right now (none until the store was read), most recently seen first. */
  items(tabId: number): MediaItem[] {
    return (this.#tabs.get(tabId) ?? []).map((it) => ({ ...it }));
  }

  /** Copies of a tab's items, most recently seen first. */
  async list(tabId: number): Promise<MediaItem[]> {
    await this.ready;
    return this.items(tabId);
  }

  /** The most recently seen stream (or file) of a tab at `url`. */
  async get(tabId: number, url: string, kind: 'stream' | 'file' = 'stream'): Promise<MediaItem | undefined> {
    await this.ready;
    const it = this.#tabs.get(tabId)?.find((x) => x.url === url && isStream(x) === (kind === 'stream'));
    return it ? { ...it } : undefined;
  }

  /** The item of a tab with key `key`. */
  async byKey(tabId: number, key: string): Promise<MediaItem | undefined> {
    await this.ready;
    const it = this.#tabs.get(tabId)?.find((x) => itemKey(x) === key);
    return it ? { ...it } : undefined;
  }

  /**
   * A response: a new item, or the item it belongs to seen again (its time, headers and size
   * updated in memory; written only when its size became known or larger). A file whose query
   * changed while its origin, path and known size stayed (per-request tokens) replaces the older
   * sighting under the same key. Undefined when nothing was recorded (a 304 of an unknown file).
   */
  record(input: RecordInput): Promise<RecordResult | undefined> {
    return this.#apply((): Change<RecordResult> => {
      const { tabId } = input;
      if (!isTabId(tabId)) return {};
      const list = this.#tabs.get(tabId) ?? [];
      const before = list.length;
      const file = input.format === 'file';
      const ref: FrameRef = { frameId: input.frameId > 0 ? input.frameId : 0, parentFrameId: input.parentFrameId };
      if (ref.frameId === 0) ref.parentFrameId = -1;
      if (input.documentId) ref.documentId = input.documentId;
      const size = file && input.size !== undefined && input.size > 0 && input.size <= MAX_FILE_SIZE ? input.size : 0;
      let i = list.findIndex((x) => sameItem(x, { url: input.url, format: input.format, ...ref }));
      let shown = false;
      let replaced = false;
      if (i < 0 && file && size > 0 && !input.refreshOnly) {
        const path = originAndPath(input.url);
        i = list.findIndex((x) => x.format === 'file' && sameFrame(x, ref) && x.size === size && originAndPath(x.url) === path);
        replaced = i >= 0;
      }
      let item: MediaItem;
      let added = false;
      if (i >= 0) {
        item = list.splice(i, 1)[0] as MediaItem;
        if (replaced) {
          item.url = input.url;
          delete item.requestUrl;
          shown = true;
        }
        item.lastSeen = Math.max(item.lastSeen, input.seenAt);
        if (!file) item.format = input.format;
        if (ref.documentId && !item.documentId) item.documentId = ref.documentId;
      } else {
        if (input.refreshOnly) return {};
        item = {
          url: input.url,
          format: input.format,
          firstSeen: input.seenAt,
          lastSeen: input.seenAt,
          ...ref,
          size: 0,
          reqType: input.reqType,
          short: false,
          segment: false,
          seq: this.#nextSeq(),
        };
        added = shown = true;
      }
      if (input.contentType) item.contentType = input.contentType;
      if (input.storeId) item.storeId = input.storeId;
      if (input.frameUrl) item.frameUrl = input.frameUrl;
      if (input.headers) item.headers = input.headers;
      if (file) {
        if (input.requestUrl && input.requestUrl !== item.url) item.requestUrl = input.requestUrl;
        if (size > item.size) {
          item.size = size;
          shown = true;
        }
        if (input.filename && !item.filename) {
          item.filename = input.filename.slice(0, MAX_FILENAME_CHARS);
          shown = true;
        }
        if (input.ad && !item.ad) item.ad = true;
        const short = shortBySize(item);
        if (short !== item.short) {
          item.short = short;
          shown = true;
        }
      }
      list.unshift(item);
      if (file && added && this.#markSegments(list, item)) shown = true;
      if (file && added) this.#capFrame(list, item);
      this.#capTab(list);
      this.#tabs.delete(tabId);
      this.#tabs.set(tabId, list);
      const others = this.#enforceLimits();
      if (shown || list.length !== before) this.#notify(tabId, list.length);
      return { tabs: [tabId, ...others], flush: shown || list.length !== before || others.length > 0, value: { key: itemKey(item), added, shown } };
    });
  }

  /**
   * The page an item was seen on (looked up after `record`), by the item's key: never applied to
   * an item of a later document, which has another key.
   */
  annotate(tabId: number, key: string, page: PageInfo): Promise<void> {
    return this.#apply((): Change<void> => {
      const item = this.#tabs.get(tabId)?.find((x) => itemKey(x) === key);
      if (!item) return {};
      const pageUrl = httpUrl(page.url);
      const title = page.title?.trim().slice(0, MAX_STORED_TITLE_CHARS);
      let changed = false;
      if (pageUrl && pageUrl !== item.pageUrl) {
        item.pageUrl = pageUrl;
        changed = true;
      }
      if (title && title !== item.pageTitle) {
        item.pageTitle = title;
        changed = true;
      }
      return changed ? { tabs: [tabId, ...this.#enforceLimits()], flush: true } : {};
    });
  }

  /**
   * Frames of a tab navigated to other documents: their items, and those of the frames below
   * them, are gone. Resolves with every frame cleared.
   */
  clearFrames(tabId: number, frameIds: Iterable<number>): Promise<Set<number>> {
    return this.#apply((): Change<Set<number>> => {
      const list = this.#tabs.get(tabId) ?? [];
      const gone = frameFamily(list, frameIds);
      const kept = list.filter((x) => !gone.has(x.frameId));
      if (kept.length === list.length) return { value: gone };
      if (kept.length === 0) this.#tabs.delete(tabId);
      else this.#tabs.set(tabId, kept);
      this.#notify(tabId, kept.length);
      return { tabs: [tabId], flush: true, value: gone };
    }).then((v) => v ?? new Set());
  }

  /** The tab navigated to another document or closed: its items are gone. */
  clearTab(tabId: number): Promise<void> {
    return this.#apply((): Change<void> => {
      if (!this.#tabs.delete(tabId)) return {};
      this.#notify(tabId, 0);
      return { tabs: [tabId], flush: true };
    });
  }

  /** The browser replaced a tab (e.g. a prerendered page): the items move to the new ID. */
  replace(removedTabId: number, addedTabId: number): Promise<void> {
    return this.#apply((): Change<void> => {
      const items = this.#tabs.get(removedTabId);
      if (!items) return {};
      this.#tabs.delete(removedTabId);
      this.#notify(removedTabId, 0);
      if (!isTabId(addedTabId)) return { tabs: [removedTabId], flush: true };
      this.#tabs.set(addedTabId, items);
      this.#notify(addedTabId, items.length);
      return { tabs: [removedTabId, addedTabId], flush: true };
    });
  }

  /** Forgets every tab (a new browser session reuses tab IDs). */
  clearAll(): Promise<void> {
    return this.#apply((): Change<void> => {
      const all = [...this.#tabs.keys()];
      this.#tabs.clear();
      for (const t of all) this.#notify(t, 0);
      return { tabs: all, flush: true };
    });
  }

  #nextSeq(): number {
    const n = this.#seq;
    this.#seq = n >= MAX_SEQ ? 1 : n + 1;
    return n;
  }

  /**
   * The segment rule (VIDEO-DETECT-SPEC §2.4): SEGMENT_RUN files of the new file's frame in its
   * directory within SEGMENT_WINDOW_MS, or a file within SEGMENT_WINDOW_MS of such a segment, are
   * a stream's segments; never a file larger than SEGMENT_MAX_BYTES. Returns whether a listed file
   * became one.
   */
  #markSegments(list: MediaItem[], item: MediaItem): boolean {
    if (item.size > SEGMENT_MAX_BYTES) return false;
    const dir = directoryOf(item.url);
    const recent = list.filter(
      (x) =>
        x.format === 'file' &&
        x.frameId === item.frameId &&
        x.size <= SEGMENT_MAX_BYTES &&
        directoryOf(x.url) === dir &&
        item.lastSeen - x.lastSeen <= SEGMENT_WINDOW_MS,
    );
    if (recent.length < SEGMENT_RUN && !recent.some((x) => x.segment)) return false;
    let shown = false;
    for (const x of recent) {
      if (x.segment) continue;
      x.segment = true;
      if (x !== item) shown = true;
    }
    item.segment = true;
    return shown;
  }

  /** At most maxFilesPerFrame files in the new file's frame: segments go first, then the oldest. */
  #capFrame(list: MediaItem[], item: MediaItem): void {
    const files = () => list.filter((x) => x.format === 'file' && x.frameId === item.frameId);
    while (files().length > this.#maxFilesPerFrame) {
      const candidates = files().filter((x) => x !== item);
      const victim = candidates.filter((x) => x.segment).at(-1) ?? candidates.at(-1);
      if (!victim) break;
      list.splice(list.indexOf(victim), 1);
    }
  }

  /** At most maxPerTab items in a tab: segments go first, then the least recently seen. */
  #capTab(list: MediaItem[]): void {
    while (list.length > this.#maxPerTab) {
      const segment = list.findLastIndex((x) => x.segment);
      list.splice(segment >= 1 ? segment : list.length - 1, 1);
    }
  }

  #notify(tabId: number, count: number): void {
    try {
      this.#onChange?.(tabId, count);
    } catch {
      // a listener must not break the store
    }
  }

  /** Applies `change` after the store was read, then writes the tabs it changed when it says so. */
  #apply<T>(change: () => Change<T>): Promise<T | undefined> {
    // Callbacks of one promise run in the order they were added: changes keep call order.
    return this.ready.then(async () => {
      const c = change();
      if (!c.flush) {
        for (const t of c.tabs ?? []) this.#stale.add(t);
        return c.value;
      }
      for (const t of [...this.#stale, ...(c.tabs ?? [])]) this.#dirty.add(t);
      this.#stale.clear();
      await this.#flush();
      return c.value;
    });
  }

  /** Tab cap, then the size budget; returns the tabs it changed. */
  #enforceLimits(): number[] {
    const changed = new Set<number>();
    while (this.#tabs.size > this.#maxTabs) {
      const oldest = this.#tabs.keys().next().value as number;
      this.#tabs.delete(oldest);
      this.#notify(oldest, 0);
      changed.add(oldest);
    }
    let total = 0;
    for (const items of this.#tabs.values()) for (const it of items) total += itemWeight(it);
    if (total <= this.#maxBytes) return [...changed];
    const all: [number, MediaItem][] = [];
    for (const [tabId, items] of this.#tabs) for (const it of items) all.push([tabId, it]);
    all.sort((a, b) => a[1].lastSeen - b[1].lastSeen);
    const drop = (tabId: number, it: MediaItem) => {
      const items = this.#tabs.get(tabId);
      const i = items?.indexOf(it) ?? -1;
      if (!items || i < 0) return;
      items.splice(i, 1);
      total -= itemWeight(it);
      changed.add(tabId);
      if (items.length === 0) this.#tabs.delete(tabId);
      this.#notify(tabId, items.length);
    };
    // First segments, which nothing lists …
    for (const [tabId, it] of all) {
      if (total <= this.#maxBytes) break;
      if (it.segment) drop(tabId, it);
    }
    // … then the oldest items' headers (a stream's hand-off falls back to the page's cookies) …
    for (const [tabId, it] of all) {
      if (total <= this.#maxBytes) break;
      if (!it.headers) continue;
      const w = itemWeight(it);
      delete it.headers;
      total -= w - itemWeight(it);
      changed.add(tabId);
    }
    // … then the oldest items.
    for (const [tabId, it] of all) {
      if (total <= this.#maxBytes) break;
      drop(tabId, it);
    }
    return [...changed];
  }

  #flush(): Promise<void> {
    if (this.#dirty.size === 0) return this.#flushing ?? Promise.resolve();
    this.#flushing ??= this.#writeLoop();
    return this.#flushing;
  }

  /** Writes dirty tabs until none are left; started only with dirty tabs (its first step awaits). */
  async #writeLoop(): Promise<void> {
    try {
      while (this.#dirty.size > 0) {
        const tabs = [...this.#dirty];
        this.#dirty.clear();
        try {
          await this.#write(tabs);
        } catch (e) {
          for (const t of tabs) this.#dirty.add(t);
          throw e;
        }
      }
    } finally {
      // Cleared in the same step that saw no dirty tab: a later change starts a new loop.
      this.#flushing = undefined;
    }
  }

  async #write(tabs: readonly number[]): Promise<void> {
    const set: Record<string, unknown> = {};
    const remove: string[] = [];
    for (const t of tabs) {
      const items = this.#tabs.get(t);
      if (!items || items.length === 0) remove.push(mediaStorageKey(t));
      else set[mediaStorageKey(t)] = this.#persistHeaders ? items : items.map(({ headers: _h, ...rest }) => rest);
    }
    await Promise.all([
      Object.keys(set).length > 0 ? this.area.set(set) : undefined,
      remove.length > 0 ? this.area.remove(remove) : undefined,
    ]);
  }
}

// ---------------------------------------------------------------------------------------
// Toolbar badge

/** Badge colours: the relay error (BROWSER-SPEC §4) and the video count. */
export const RELAY_BADGE_COLOR = '#d93025';
export const MEDIA_BADGE_COLOR = '#0a5cc2';

export interface TabBadge {
  /** null: the tab shows the global badge (`!` while Downex is unavailable, else nothing). */
  text: string | null;
  color: string;
}

/**
 * A manifest of the video host behind a page video (`*.googlevideo.com`): live or SABR streams
 * Downex would refuse. A tab with a page video does not list or count them (SITE-MEDIA-SPEC §5.2).
 */
export function isPageVideoStream(url: string): boolean {
  try {
    const host = new URL(url).hostname.toLowerCase();
    return host === 'googlevideo.com' || host.endsWith('.googlevideo.com');
  } catch {
    return false;
  }
}

/**
 * A tab's badge, from the number of its video groups. Precedence: the relay error `!` (global,
 * actionable) over the tab's count (informational), so an unavailable Downex is never hidden by a
 * count.
 */
export function tabBadge(count: number, relayDown: boolean): TabBadge {
  if (relayDown || count <= 0) return { text: null, color: RELAY_BADGE_COLOR };
  return { text: count > 99 ? '99+' : String(count), color: MEDIA_BADGE_COLOR };
}
