// Save what's playing (SITE-MEDIA-SPEC §2.9): the pure rules of the extension's part. Which
// responses of a tab are played media files (their whole-file key, with a byte range in the
// query folded away), which of them are the clicked video's candidates, what the clicked
// `<video>` says of itself (its trace), and the name of a played video. Only generic standards
// are read: HTTP (RFC 9110), the element's public state, the browser's own resource timing, link
// addresses and visible text near the element; never a site's own data fields or scripts.

import { headerValue, toHeaderPairs } from './context';
import { contentLengthSize, contentRangeTotal } from './frame-media';
import { mimeEssence } from './rules';

/** Files a tab remembers, newest kept, and for how long (§2.9 "The flow" 1). */
export const PLAYED_PER_TAB = 64;
export const PLAYED_TTL_MS = 30 * 60_000;
/** Longest address recorded at all, and longest written to storage.session. */
export const MAX_PLAYED_URL = 8 * 1024;
export const MAX_STORED_PLAYED_URL = 4 * 1024;
/** Candidates of one click, and how recent a file must be (§2.9 "The flow" 3). */
export const MAX_CANDIDATES = 16;
export const CANDIDATE_WINDOW_MS = 10 * 60_000;
/** A file known to be smaller is an ad's or a preview's piece, never a candidate. */
export const MIN_CANDIDATE_BYTES = 64 * 1024;
/** Resource-timing addresses a click sends at most. */
export const MAX_RECENT = 24;
/** Other elements' durations a click sends at most. */
export const MAX_OTHERS = 8;
/** Segment directories a click sends at most, and how close to the newest segment they were fetched. */
export const MAX_SEGMENT_DIRS = 8;
export const PLAYING_SEGMENTS_WINDOW_MS = 10_000;
/** Values of a byte range in the query are below this. */
const MAX_RANGE_VALUE = 2 ** 40;

/** One media file a tab's player fetched, by its whole-file key. */
export interface PlayedResource {
  key: string;
  /** Exact total, 0 = unknown. */
  total: number;
  kind: 'video' | 'audio' | '';
  frameId: number;
  firstAt: number;
  lastAt: number;
  /** The browser got it from a public address (the request log's server address); absent: unknown. */
  direct?: boolean;
}

/** The webRequest.onResponseStarted fields read here. */
export interface PlayedResponse {
  url: string;
  method: string;
  tabId: number;
  frameId?: number;
  statusCode?: number;
  incognito?: boolean;
  /** The server's address the browser connected to (absent when answered from its cache). */
  ip?: string;
  responseHeaders?: readonly { name: string; value?: string }[];
}

/** File extensions of whole media files whose server sends no media type. */
const MEDIA_PATH = /\.(mp4|m4v|m4a|mov)$/i;
/** Segments of a stream (Stream rows, never whole files). */
const SEGMENT_PATH = /\.(ts|m4s|m4f|aac|cmfv|cmfa|vtt|webvtt)$/i;

function pathOf(url: URL): string {
  return url.pathname;
}

/**
 * The whole-file key of a piece whose byte range is in its query (§2.9): one parameter whose value
 * is `a-b`, or two parameters whose values are a ≤ b, all decimal, with b − a + 1 = `length`; the
 * address without exactly those parameters, every other kept in order. Undefined when the query
 * names no such range.
 */
export function rangeInQuery(url: string, length: number): string | undefined {
  if (!(length > 0)) return undefined;
  let u: URL;
  try {
    u = new URL(url);
  } catch {
    return undefined;
  }
  const params = [...u.searchParams.entries()];
  const num = (v: string) => (/^[0-9]{1,13}$/.test(v) ? Number(v) : undefined);
  const strip = (drop: ReadonlySet<number>) => {
    const kept = new URLSearchParams();
    params.forEach(([k, v], i) => {
      if (!drop.has(i)) kept.append(k, v);
    });
    const q = kept.toString();
    u.search = q ? `?${q}` : '';
    return u.href;
  };
  for (let i = 0; i < params.length; i++) {
    const m = /^([0-9]{1,13})-([0-9]{1,13})$/.exec(params[i]?.[1] ?? '');
    if (!m) continue;
    const a = Number(m[1]);
    const b = Number(m[2]);
    if (a <= b && b < MAX_RANGE_VALUE && b - a + 1 === length) return strip(new Set([i]));
  }
  // Two parameters: neighbours in order first (`start=a&end=b`), then any pair in order, then any
  // pair, so that another number of the query that happens to fit never wins.
  const pairs: [number, number, number][] = [];
  for (let i = 0; i < params.length; i++) {
    const a = num(params[i]?.[1] ?? '');
    if (a === undefined) continue;
    for (let j = i + 1; j < params.length; j++) {
      const b = num(params[j]?.[1] ?? '');
      if (b === undefined) continue;
      const [lo, hi] = a <= b ? [a, b] : [b, a];
      if (!(hi < MAX_RANGE_VALUE && hi - lo + 1 === length)) continue;
      // Names of one pair often share their start ("…start"/"…end" of one word).
      const ni = params[i]?.[0] ?? '';
      const nj = params[j]?.[0] ?? '';
      const shared = ni.length >= 2 && nj.slice(0, 2) === ni.slice(0, 2);
      const order = j === i + 1 && a <= b ? 0 : a <= b ? 1 : 2;
      pairs.push([i, j, order * 2 + (shared ? 0 : 1) - j / 1e3]);
    }
  }
  pairs.sort((x, y) => x[2] - y[2]);
  const best = pairs[0];
  return best ? strip(new Set([best[0], best[1]])) : undefined;
}

/**
 * A response that is a played media file (§2.9 "The flow" 1): a GET of a tab (not private)
 * answered 200 or 206, http(s), at most MAX_PLAYED_URL, `video/*` or `audio/*` (a transport
 * stream never: its files are a stream's segments) or an octet stream whose path names a media
 * file, not a segment's path. Its key folds a range in the query away (rangeInQuery); its total is
 * Content-Range's total, a 200's Content-Length when no range was folded, else unknown.
 */
export function playedResponse(d: PlayedResponse): (Pick<PlayedResource, 'key' | 'total' | 'kind'> & { direct: boolean }) | undefined {
  if (d.tabId < 0 || d.incognito || d.method.toUpperCase() !== 'GET') return undefined;
  if (d.statusCode !== 200 && d.statusCode !== 206) return undefined;
  if (d.url.length > MAX_PLAYED_URL || d.url.includes('#')) return undefined;
  let u: URL;
  try {
    u = new URL(d.url);
  } catch {
    return undefined;
  }
  if (u.protocol !== 'https:' && u.protocol !== 'http:') return undefined;
  if (!playedAddressOk(d.url)) return undefined;
  // A name that led the browser to this computer or a local network is never recorded (§2.9 "Limits").
  const ip = typeof d.ip === 'string' && d.ip !== '' ? d.ip : undefined;
  if (ip && !playedAddressOk(`http://${ip.includes(':') ? `[${ip}]` : ip}/`)) return undefined;
  const direct = ip !== undefined;
  const headers = toHeaderPairs(d.responseHeaders);
  const type = mimeEssence(headerValue(headers, 'content-type')) ?? '';
  const path = pathOf(u);
  if (SEGMENT_PATH.test(path)) return undefined;
  let kind: PlayedResource['kind'];
  if (type.startsWith('video/')) {
    if (type === 'video/mp2t' || type === 'video/webm') return undefined;
    kind = 'video';
  } else if (type.startsWith('audio/')) {
    if (type === 'audio/webm' || type === 'audio/mpegurl' || type === 'audio/x-mpegurl') return undefined;
    kind = 'audio';
  } else if ((type === '' || type === 'application/octet-stream' || type === 'binary/octet-stream') && MEDIA_PATH.test(path)) {
    kind = /\.m4a$/i.test(path) ? 'audio' : '';
  } else {
    return undefined;
  }
  if (d.statusCode === 206) {
    return { key: d.url, total: contentRangeTotal(headerValue(headers, 'content-range')) ?? 0, kind, direct };
  }
  const length = contentLengthSize(headerValue(headers, 'content-length')) ?? 0;
  const whole = rangeInQuery(d.url, length);
  if (whole) return { key: whole, total: 0, kind, direct };
  return { key: d.url, total: length, kind, direct };
}

/**
 * A candidate of a click: `logged` when the tab's PlayedLog saw the browser answer it as media,
 * `direct` when that answer came from a public address (only then may the background read it itself).
 */
export interface PlayedCandidate extends Pick<PlayedResource, 'key' | 'total' | 'kind'> {
  logged: boolean;
  direct: boolean;
}

/**
 * The candidates of a click (§2.9 "The flow" 3): the tab's files requested in the last
 * CANDIDATE_WINDOW_MS, newest first, without those known to be smaller than MIN_CANDIDATE_BYTES,
 * then the resource-timing addresses not among them; at most MAX_CANDIDATES. An address on a host
 * Downex never saves from (playedAddressOk) is never a candidate.
 */
export function candidates(resources: readonly PlayedResource[], recent: readonly string[], now: number): PlayedCandidate[] {
  const out: PlayedCandidate[] = [];
  const seen = new Set<string>();
  const live = resources
    .filter((r) => now - r.lastAt <= CANDIDATE_WINDOW_MS && !(r.total > 0 && r.total < MIN_CANDIDATE_BYTES))
    .sort((a, b) => b.lastAt - a.lastAt);
  for (const r of live) {
    if (out.length >= MAX_CANDIDATES) break;
    if (seen.has(r.key) || !playedAddressOk(r.key)) continue;
    seen.add(r.key);
    out.push({ key: r.key, total: r.total, kind: r.kind, logged: true, direct: r.direct === true });
  }
  for (const url of recent) {
    if (out.length >= MAX_CANDIDATES) break;
    if (seen.has(url) || !playedAddressOk(url)) continue;
    seen.add(url);
    out.push({ key: url, total: 0, kind: '', logged: false, direct: false });
  }
  return out;
}

/** An IPv4 literal's four numbers, undefined for anything else. */
function ipv4(host: string): number[] | undefined {
  const m = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(host);
  if (!m) return undefined;
  const n = m.slice(1).map(Number);
  return n.every((x) => x <= 255) ? n : undefined;
}

/**
 * Whether an IPv4 address is this computer's, a local network's (private, shared/CGNAT, IETF,
 * benchmarking), link-local, unspecified, multicast or reserved (internal/transport.LocalAddress).
 */
function localIPv4([a, b, c]: number[]): boolean {
  if (a === undefined || b === undefined) return true;
  return (
    a === 0 ||
    a === 10 ||
    a === 127 ||
    (a === 100 && b >= 64 && b <= 127) ||
    (a === 169 && b === 254) ||
    (a === 172 && b >= 16 && b <= 31) ||
    (a === 192 && b === 168) ||
    (a === 192 && b === 0 && c === 0) ||
    (a === 198 && (b === 18 || b === 19)) ||
    a >= 224
  );
}

/** The IPv4 address in two IPv6 groups (`c0a8:101` is 192.168.1.1). */
function groupsIPv4(hi: string, lo: string): number[] {
  const h = parseInt(hi, 16);
  const l = parseInt(lo, 16);
  return [h >> 8, h & 255, l >> 8, l & 255];
}

/**
 * Whether a played file's address is one Downex may fetch (internal/played.CheckURL, §2.9 "Limits"):
 * http(s) with a host, not on a YouTube host, not `localhost` (or a name under it), and not a
 * local-network literal address (internal/transport.LocalAddress). Nothing else is ever
 * recorded, fetched or sent for a Played source.
 */
export function playedAddressOk(url: string): boolean {
  let u: URL;
  try {
    u = new URL(url);
  } catch {
    return false;
  }
  if (u.protocol !== 'https:' && u.protocol !== 'http:') return false;
  const host = u.hostname.toLowerCase().replace(/\.$/, '');
  if (!host || isYouTubeHost(host) || host === 'localhost' || host.endsWith('.localhost')) return false;
  const v4 = ipv4(host);
  if (v4) return !localIPv4(v4);
  if (host.startsWith('[')) {
    const v6 = host.slice(1, -1);
    // ::, ::1, fc00::/7 (private), fe80::/10 (link-local), fec0::/10 (site-local), ff00::/8
    // (multicast), Teredo (2001::/32), documentation (2001:db8::/32), and IPv6 addresses carrying
    // an IPv4 address that may reach a local one: NAT64 (64:ff9b:), 6to4 (2002:), IPv4-compatible
    // and IPv4-mapped (the URL parser writes their IPv4 part as two hex groups).
    if (v6 === '::' || v6 === '::1' || /^f[c-f]/.test(v6) || /^2001:(0{0,4}:|db8:)/.test(v6)) return false;
    if (v6.startsWith('64:ff9b:')) return false;
    const sixToFour = /^2002:([0-9a-f]{1,4}):([0-9a-f]{1,4})(:|$)/.exec(v6);
    if (sixToFour) return !localIPv4(groupsIPv4(sixToFour[1] ?? '0', sixToFour[2] ?? '0'));
    if (v6.startsWith('2002:')) return false;
    const embedded = /^::(?:ffff:)?([0-9a-f]{1,4}):([0-9a-f]{1,4})$/.exec(v6);
    if (embedded) return !localIPv4(groupsIPv4(embedded[1] ?? '0', embedded[2] ?? '0'));
  }
  return true;
}

// ---------------------------------------------------------------------------------------------
// The clicked element (content script)

/** What the clicked `<video>` says of itself, read at the click and on each panel poll. */
export interface PlayedTrace {
  /**
   * The clicked element's identity in its frame (its button's id and a hash of what it plays now and
   * of its post's link): a new element, or a feed's player that loads another post, is another video.
   */
  element: string;
  /** 0 = unknown (not loaded yet). A live element has no trace. */
  durationMs: number;
  /**
   * The durations (ms) of the frame's other `<video>` elements that know theirs, at most MAX_OTHERS:
   * a file nearer one of them than this element's length is theirs (a feed's preloaded neighbour).
   */
  others: number[];
  width: number;
  height: number;
  /** 1 it has sound, -1 it has none, 0 unknown. */
  audio: -1 | 0 | 1;
  /** The page's own resource-timing addresses of media (files fetched before the extension watched). */
  recent: string[];
  /**
   * The directories of the stream segments the page fetched last (playingSegments, at most
   * MAX_SEGMENT_DIRS): the HLS or DASH stream playing now is the one there (VIDEO-DETECT-SPEC §2.6).
   */
  segments: string[];
  /** The name hint (playedTitle), at most 200 characters. */
  name: string;
  /** The frame's address now (`location.href`): a post viewer opened with pushState. */
  href: string;
  /** The element has `mediaKeys` now (EME, set while its panel was open): never saved (§2.9 "Never"). */
  drm?: boolean;
}

/** Whether a value is a PlayedTrace within bounds (messages from content scripts). */
export function isPlayedTrace(v: unknown): v is PlayedTrace {
  if (typeof v !== 'object' || v === null) return false;
  const t = v as Record<string, unknown>;
  const int = (x: unknown, lo: number, hi: number) => Number.isSafeInteger(x) && (x as number) >= lo && (x as number) <= hi;
  return (
    typeof t.element === 'string' &&
    /^[\w:.-]{1,64}$/.test(t.element) &&
    int(t.durationMs, 0, 86_400_000) &&
    Array.isArray(t.others) &&
    t.others.length <= MAX_OTHERS &&
    t.others.every((d) => int(d, 1, 86_400_000)) &&
    int(t.width, 0, 16384) &&
    int(t.height, 0, 16384) &&
    (t.audio === -1 || t.audio === 0 || t.audio === 1) &&
    Array.isArray(t.recent) &&
    t.recent.length <= MAX_RECENT &&
    t.recent.every((u) => typeof u === 'string' && /^https?:\/\//i.test(u) && u.length <= MAX_PLAYED_URL && !u.includes('#')) &&
    Array.isArray(t.segments) &&
    t.segments.length <= MAX_SEGMENT_DIRS &&
    t.segments.every((u) => typeof u === 'string' && /^https?:\/\//i.test(u) && u.length <= MAX_PLAYED_URL && !u.includes('#')) &&
    typeof t.name === 'string' &&
    t.name.length <= 200 &&
    typeof t.href === 'string' &&
    t.href.length <= MAX_PLAYED_URL &&
    (t.drm === undefined || typeof t.drm === 'boolean')
  );
}

/** The element's sound as the browser's own counters say it (Chromium, Firefox); 0 unknown. */
export function elementAudio(v: HTMLVideoElement): -1 | 0 | 1 {
  const x = v as HTMLVideoElement & { webkitAudioDecodedByteCount?: number; mozHasAudio?: boolean };
  if (typeof x.mozHasAudio === 'boolean') return x.mozHasAudio ? 1 : -1;
  if (typeof x.webkitAudioDecodedByteCount === 'number') {
    if (x.webkitAudioDecodedByteCount > 0) return 1;
    if (v.currentTime >= 2 && !v.paused) return -1;
  }
  return 0;
}

/** A resource-timing entry's fields read here. */
export interface TimingEntry {
  name: string;
  initiatorType?: string;
}

/**
 * The page's own resource-timing addresses of media files (§2.9 "The flow" 2): fetched by a media
 * element, or by the page's scripts at an address whose path names a media file; http(s), on a host
 * Downex may fetch (playedAddressOk), never a stream segment; the newest MAX_RECENT. A word in an
 * address ("video" in an API's path) never makes it media: such addresses are the site's own API
 * and logging calls, never requested again.
 */
export function recentMedia(entries: readonly TimingEntry[]): string[] {
  const out: string[] = [];
  for (let i = entries.length - 1; i >= 0 && out.length < MAX_RECENT; i--) {
    const e = entries[i] as TimingEntry;
    const type = e.initiatorType ?? '';
    if (!['fetch', 'xmlhttprequest', 'video', 'audio', 'other', ''].includes(type)) continue;
    const url = e.name;
    if (!/^https?:\/\//i.test(url) || url.length > MAX_PLAYED_URL) continue;
    let path: string;
    try {
      const u = new URL(url);
      u.hash = '';
      path = u.pathname;
      if (SEGMENT_PATH.test(path)) continue;
      if (!(MEDIA_PATH.test(path) || type === 'video' || type === 'audio')) continue;
      if (!playedAddressOk(u.href)) continue;
      if (!out.includes(u.href)) out.push(u.href);
    } catch {
      // not an address
    }
  }
  return out;
}

/**
 * The directories (origin and path up to its last "/") of the stream segments the page fetched
 * last: those started within PLAYING_SEGMENTS_WINDOW_MS of the newest segment, newest first, at most
 * MAX_SEGMENT_DIRS. A segment is an address whose path names one (`.ts`, `.m4s`, …). Empty without
 * segments.
 */
export function playingSegments(entries: readonly (TimingEntry & { startTime?: number })[]): string[] {
  const segs: { dir: string; at: number }[] = [];
  for (const e of entries) {
    let u: URL;
    try {
      u = new URL(e.name);
    } catch {
      continue;
    }
    if ((u.protocol !== 'https:' && u.protocol !== 'http:') || !SEGMENT_PATH.test(u.pathname)) continue;
    const dir = u.origin + u.pathname.slice(0, u.pathname.lastIndexOf('/') + 1);
    if (dir.length <= MAX_PLAYED_URL) segs.push({ dir, at: e.startTime ?? 0 });
  }
  if (segs.length === 0) return [];
  const newest = Math.max(...segs.map((s) => s.at));
  const out: string[] = [];
  for (const s of segs.sort((a, b) => b.at - a.at)) {
    if (s.at < newest - PLAYING_SEGMENTS_WINDOW_MS || out.length >= MAX_SEGMENT_DIRS) break;
    if (!out.includes(s.dir)) out.push(s.dir);
  }
  return out;
}

// ---------------------------------------------------------------------------------------------
// The name (§2.9 "The name")

/** Text this long is a caption; the name is cut at a word boundary to NAME_MAX. */
const CAPTION_MIN = 12;
const NAME_MAX = 120;
/** Nodes the caption walk visits at most. */
const MAX_NAME_NODES = 2000;

function cut(text: string, max: number): string {
  const t = text.replace(/\s+/g, ' ').trim();
  if (t.length <= max) return t;
  const head = t.slice(0, max);
  const space = head.lastIndexOf(' ');
  return (space > max / 2 ? head.slice(0, space) : head).trim();
}

/**
 * An author link's text: its text pieces joined with spaces (a display name and a handle are two
 * pieces: never "Name@handle"), without a trailing "@handle" after a display name, and without a
 * leading "@".
 */
export function authorText(a: Element): string {
  const parts: string[] = [];
  const walker = a.ownerDocument.createTreeWalker(a, 4 /* NodeFilter.SHOW_TEXT */);
  for (let n = 0, node = walker.nextNode(); node && n < 64; n++, node = walker.nextNode()) {
    const t = (node.textContent ?? '').replace(/\s+/g, ' ').trim();
    if (t) parts.push(t);
  }
  let text = parts.join(' ').trim();
  const handle = /^(.*\p{L}.*?)\s*@[\w.-]+$/u.exec(text);
  if (handle && (handle[1] ?? '').trim() !== '') text = (handle[1] ?? '').trim();
  return text.replace(/^@/, '');
}

/**
 * The name of a played video (§2.9 "The name"): in `container` (the lowest ancestor of the video
 * that holds no other shown video), the text of the first link that `isProfile` takes for an
 * author, and the first text of at least CAPTION_MIN characters outside buttons, `time`, and that
 * link; "author - caption", or the one that exists, else "". `accept` (playedNameOver) limits both
 * to the elements it takes.
 */
export function playedName(
  container: Element | undefined,
  isProfile: (href: string) => boolean,
  opts: { authorOnly?: boolean; accept?: (el: Element) => boolean } = {},
): string {
  if (!container) return '';
  const accept = opts.accept ?? (() => true);
  let author = '';
  let authorEl: Element | undefined;
  for (const a of Array.from(container.querySelectorAll('a[href]')).slice(0, 64)) {
    const href = (a as HTMLAnchorElement).href;
    const text = authorText(a);
    if (text && text.length <= 64 && isProfile(href) && !a.closest(PAGE_CHROME) && accept(a)) {
      author = text;
      authorEl = a;
      break;
    }
  }
  let caption = '';
  const doc = container.ownerDocument;
  const walker = doc.createTreeWalker(container, 4 /* NodeFilter.SHOW_TEXT */);
  for (let n = 0, node = walker.nextNode(); node && n < MAX_NAME_NODES; n++, node = walker.nextNode()) {
    const text = (node.textContent ?? '').replace(/\s+/g, ' ').trim();
    // A caption has words: never a player's time ("00:04 / 00:05") or a count.
    if (text.length < CAPTION_MIN || !/\p{L}{3}/u.test(text) || /^[\d\s:./|-]+$/.test(text)) continue;
    const el = node.parentElement;
    if (!el || el.closest(`button, time, [role="button"], script, style, ${PAGE_CHROME}`) || (authorEl && authorEl.contains(el))) continue;
    // A site's dialog over the post (a sign-up nudge) never names the video.
    const dialog = el.closest('[role="dialog"], [aria-modal="true"], dialog');
    if (dialog && dialog !== container && container.contains(dialog)) continue;
    if (author && text === author) continue;
    if (!accept(el)) continue;
    caption = text;
    break;
  }
  if (opts.authorOnly) caption = '';
  author = cut(author, 40);
  caption = cut(caption, NAME_MAX);
  if (author && caption) return `${author} - ${caption}`;
  return author || caption;
}

/**
 * The title of a played video (§2.9 "The name"): the name read in its post, else, on the video's own
 * page, the tab's title without its site part (a leading notification count "(3) ", a trailing
 * " | Site", " • Site" or " - Site") and without a run of counts before " | " ("4.6K views · 38K
 * reactions | "); "" when nothing but the site's name is left, so Downex names it "<Site> video".
 */
export function playedTitle(name: string, tabTitle: string | undefined, ownPage: string | undefined): string {
  if (name.trim() !== '') return name.trim();
  if (ownPage === undefined || !tabTitle) return '';
  const site = ownPage.toLowerCase();
  let t = tabTitle.replace(/\s+/g, ' ').trim().replace(/^\(\d+\+?\)\s*/, '');
  const suffix = /^(.*\S)\s+[|•·–-]\s+([^|•·–]+)$/.exec(t);
  if (suffix && (suffix[2] ?? '').trim().toLowerCase() === site) t = suffix[1] ?? '';
  t = t.replace(/^\d[\d.,]*\s?[KMBkmb]?\s+\p{L}+(?:\s+·\s+\d[\d.,]*\s?[KMBkmb]?\s+\p{L}+)*\s+\|\s+/u, '');
  if (t.toLowerCase() === site || t === '') return '';
  return cut(t, 200);
}

/** Whether two http(s) addresses have the same origin (a frame's address read at the click, §3.6). */
export function sameOrigin(a: string | undefined, b: string | undefined): boolean {
  if (!a || !b) return false;
  try {
    const x = new URL(a);
    const y = new URL(b);
    return (x.protocol === 'https:' || x.protocol === 'http:') && x.origin === y.origin;
  } catch {
    return false;
  }
}

/** Whether a head starts like an ISO-BMFF file or segment (its first box type). */
export function looksLikeMp4(head: Uint8Array): boolean {
  if (head.length < 8) return false;
  const t = String.fromCharCode(head[4] as number, head[5] as number, head[6] as number, head[7] as number);
  return ['ftyp', 'styp', 'moov', 'sidx', 'free', 'skip', 'wide', 'pdin', 'moof'].includes(t);
}

/** Reads at most `max` bytes of a response's body, then cancels the rest. */
export async function readUpTo(res: Response, max: number): Promise<Uint8Array> {
  const reader = res.body?.getReader();
  if (!reader) return new Uint8Array(0);
  const chunks: Uint8Array[] = [];
  let got = 0;
  try {
    while (got < max) {
      const r = await reader.read();
      if (r.done || !r.value) break;
      chunks.push(r.value);
      got += r.value.length;
    }
  } finally {
    await reader.cancel().catch(() => undefined);
  }
  const out = new Uint8Array(Math.min(got, max));
  let off = 0;
  for (const c of chunks) {
    if (off >= out.length) break;
    const part = c.subarray(0, out.length - off);
    out.set(part, off);
    off += part.length;
  }
  return out;
}

/** YouTube's hosts and its video servers': never a Played source (YouTube keeps its own path, §2.9). */
export function isYouTubeHost(host: string): boolean {
  const h = host.toLowerCase();
  return ['youtube.com', 'youtu.be', 'youtube-nocookie.com', 'googlevideo.com', 'ytimg.com'].some((d) => h === d || h.endsWith(`.${d}`));
}

/** The page's own menus and footers: never a post's author or caption. */
const PAGE_CHROME = 'nav, aside, footer, [role="navigation"], [role="contentinfo"], [role="complementary"]';

/** The ids of a single-page app's root element (TikTok's `div#app`). */
const APP_ROOTS = new Set(['app', 'root', '__next', '__nuxt', 'main-app', 'mount']);

/**
 * Whether an ancestor is past a post: the page's app root (a child of `body` named like one,
 * TikTok's `div#app` on a video's own page) or a section that holds the page's menus or footer.
 */
function pastPost(el: Element): boolean {
  if (el.parentElement === el.ownerDocument.body && APP_ROOTS.has(el.id.toLowerCase())) return true;
  return el.querySelector(PAGE_CHROME) !== null;
}

/** Levels the name's container walk climbs at most (the feed walk's bound, VIDEO-DETECT-SPEC §5.1). */
const MAX_CONTAINER_LEVELS = 16;

/**
 * The post container of a video (§2.9 "The name"): the highest of its ancestors, at most
 * MAX_CONTAINER_LEVELS up (from a shadow root to its host), that holds no other `<video>`.
 */
export function postContainer(video: Element): Element | undefined {
  let best: Element | undefined;
  let node: Element | null = video;
  for (let level = 0; node && level < MAX_CONTAINER_LEVELS; level++) {
    const root = node.getRootNode();
    const parent: Element | null = node.parentElement ?? (root instanceof ShadowRoot ? root.host : null);
    if (!parent || parent === video.ownerDocument.body || parent === video.ownerDocument.documentElement) break;
    const others = Array.from(parent.querySelectorAll('video')).some((v) => v !== video);
    // A page section (more than 64 links: a grid, a menu) is never one post (the feed walk's rule).
    if (others || parent.querySelectorAll('a[href]').length > 64 || pastPost(parent)) break;
    best = parent;
    node = parent;
  }
  return best;
}

/**
 * The name of a played video whose post container names nothing (§2.9 "The name"; review
 * 2026-11-18, TikTok For You with its comments closed: the video's own box holds no link and no
 * text, and its author and caption lie in a layer beside it, over the video, under an ancestor that
 * also holds the next item's `<video>`): from the video's post container upward (at most
 * MAX_CONTAINER_LEVELS, never past an ancestor with more than 64 links), the author link and caption
 * that lie over the clicked video on screen (their box's centre inside the video's) and inside no
 * other video's post container. "" when nothing is found.
 */
export function playedNameOver(video: Element, isProfile: (href: string) => boolean): string {
  const box = video.getBoundingClientRect();
  if (!(box.width > 0 && box.height > 0)) return '';
  const others = Array.from(video.ownerDocument.querySelectorAll('video')).filter((v) => v !== video);
  const theirs = others.map((v) => postContainer(v) ?? v).filter((c) => !c.contains(video));
  const accept = (el: Element) => {
    if (theirs.some((c) => c.contains(el))) return false;
    const r = el.getBoundingClientRect();
    if (!(r.width > 0 && r.height > 0)) return false;
    const x = r.left + r.width / 2;
    const y = r.top + r.height / 2;
    return x >= box.left && x <= box.right && y >= box.top && y <= box.bottom;
  };
  let node: Element | null = postContainer(video) ?? video.parentElement;
  for (let level = 0; node && level < MAX_CONTAINER_LEVELS; level++) {
    const parent: Element | null = node.parentElement;
    if (!parent || parent === video.ownerDocument.body || parent === video.ownerDocument.documentElement) break;
    if (parent.querySelectorAll('a[href]').length > 64 || pastPost(parent)) break;
    const name = playedName(parent, isProfile, { accept });
    if (name) return name;
    node = parent;
  }
  return '';
}

/** One-segment paths that are a site's own sections, never an author (TikTok's "/explore"). */
const SITE_SECTIONS = new Set([
  'explore', 'following', 'foryou', 'friends', 'live', 'messages', 'inbox', 'upload', 'search', 'feed', 'home',
  'reels', 'reel', 'watch', 'shorts', 'trending', 'discover', 'notifications', 'settings', 'login', 'signup',
  'legal', 'terms', 'privacy', 'about', 'help', 'tag', 'tags', 'music', 'gaming', 'marketplace', 'groups',
]);

/**
 * Whether a link is an author's profile on the frame's own site (§2.9 "The name"): the same
 * origin, and a path of one name (`/@name`, `/name`) or a profile section (`/user/name`,
 * `/profile/name`, `/in/name`, `/u/name`, `/c/name`).
 */
export function profileLink(href: string, frameUrl: string): boolean {
  if (!sameOrigin(href, frameUrl)) return false;
  let path: string;
  try {
    path = new URL(href).pathname;
  } catch {
    return false;
  }
  const one = /^\/(@[\w.-]{1,40}|[\w.-]{2,40})\/?$/.exec(path);
  if (one) return !SITE_SECTIONS.has((one[1] ?? '').toLowerCase());
  return /^\/(user|profile|in|u|c)\/[\w.-]{1,60}\/?$/.test(path);
}
