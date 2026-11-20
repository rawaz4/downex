// What a frame's Download panel and the popup list (VIDEO-DETECT-SPEC §2.6, §5.3), from the
// background's memory: the frame's own direct files and streams, an element file for an MP4 the
// extension did not see, the keys a frame may use (bound to its own frame and document), and the
// badge's count of a tab. The full URLs and headers never reach a web page: panels get keys, host
// and last path segment. A closed shadow root keeps the page's scripts from the panel's DOM, not
// from its text (window.find and Selection reach it in Chromium), so a panel shows only what its
// own frame may know: its own items, and the tab's title and host only in the top frame. The
// popup, an extension page, lists the rest. Pure.

import { fileName } from './file-handoff';
import { type FrameReport, SHORT_CLIP_MS, allLive, allProtected, isMp4Path, protectedOrLive } from './frame-media';
import { directoryOf, type FrameRef, type MediaItem, isStream, itemKey, toMediaEntry } from './media';
import type { MediaSendState } from './media-handoff';
import type { VideoTaskState } from './messages';
import {
  type FileEntry,
  type FileOffer,
  type FrameOffers,
  type FrameVideoMeta,
  OFFER_KEY_RE,
  type OfferKey,
  type PlayedOffer,
  type SiteOffer,
  type StreamEntry,
  type StreamOffer,
} from './offer-types';
import { type AddedResult, daemonCode, type VideoInfoResult, failure } from './protocol';

/** The frame (and, on Chromium, document) that asks. */
export interface FrameSender {
  frameId: number;
  documentId?: string;
}

/** Whether two documents may be the same (a missing id, Firefox's, matches any). */
function sameDocument(a: string | undefined, b: string | undefined): boolean {
  return a === undefined || b === undefined || a === b;
}

/**
 * Whether a frame may name an item (§3.7): the top frame every item of its tab, another frame
 * only the items of its own document.
 */
export function itemVisibleTo(item: FrameRef, sender: FrameSender): boolean {
  if (sender.frameId === 0) return true;
  return item.frameId === sender.frameId && sameDocument(item.documentId, sender.documentId);
}

function normalized(url: string): string | undefined {
  try {
    const u = new URL(url);
    u.hash = '';
    return u.href;
  } catch {
    return undefined;
  }
}

/** Whether two addresses are the same, fragments ignored. */
export function sameUrl(a: string | undefined, b: string | undefined): boolean {
  if (!a || !b) return false;
  const x = normalized(a);
  return x !== undefined && x === normalized(b);
}

/** Whether an element whose `currentSrc` is `src` plays this file (its first or final URL, §2.6). */
export function fileMatches(item: Pick<MediaItem, 'url' | 'requestUrl'>, src: string): boolean {
  return src !== '' && (sameUrl(item.url, src) || sameUrl(item.requestUrl, src));
}

/** The reported element of the item's frame that plays it. */
export function elementOf(item: MediaItem, report: FrameReport | undefined): FrameVideoMeta | undefined {
  return report?.videos.find((v) => fileMatches(item, v.src));
}

/**
 * Whether a file is a stream's segment (§2.4): marked so by the store, and no reported element
 * of its frame plays it (a stream's segments are never an element's `currentSrc`).
 */
export function segmentOf(item: MediaItem, report: FrameReport | undefined): boolean {
  return item.segment && elementOf(item, report) === undefined;
}

/** Short by the element that plays it: at most SHORT_CLIP_MS (an ad, a preview). */
function shortClip(v: FrameVideoMeta | undefined): boolean {
  return v !== undefined && v.durationMs >= 0 && v.durationMs < SHORT_CLIP_MS;
}

/**
 * The group of a stream's playlist (StreamOffer.group): a hash of its origin, directory and file name
 * up to the first "_" or "." ("" for an address that does not parse). Playlists of one video named alike
 * (`<name>.m3u8`, `<name>_720w.m3u8`, `<name>_audio.m3u8`) share it; one directory per video too.
 */
export function streamGroup(url: string): string {
  try {
    const u = new URL(url);
    const slash = u.pathname.lastIndexOf('/');
    const name = u.pathname.slice(slash + 1);
    const stem = /^[^_.]*/.exec(name)?.[0] ?? name;
    // A hash: the panel compares groups, it never needs (or shows) the directory.
    const text = `${u.origin}${u.pathname.slice(0, slash + 1)}${stem}`;
    let h = 0x811c9dc5;
    for (let i = 0; i < text.length; i++) h = Math.imul(h ^ text.charCodeAt(i), 0x01000193) >>> 0;
    return h.toString(36);
  } catch {
    return '';
  }
}

/** What a page's panel shows of an address: host and last path segment, never the query. */
export function displayOf(url: string): string {
  try {
    const u = new URL(url);
    const seg = u.pathname.split('/').filter((s) => s !== '').pop();
    if (!seg) return u.host;
    let name = seg;
    try {
      name = decodeURIComponent(seg);
    } catch {
      // keep it encoded
    }
    return `${u.host}/${name}`;
  } catch {
    return '';
  }
}

/** The send key of an element file: element ids are only unique within their frame. */
export function elementSendKey(key: OfferKey, frameId: number): string {
  return `${key}@${frameId}`;
}

export interface FrameOffersInput {
  sender: FrameSender;
  /** The clicked element's `currentSrc` when http(s), else "". */
  src: string;
  /** The clicked element plays a `blob:` URL. */
  mse: boolean;
  /** The tab's title. */
  title: string;
  /** The host of the tab's page ("" when unknown). */
  topHost: string;
  /** The tab's items. */
  items: readonly MediaItem[];
  /** The tab's element reports. */
  reports: readonly FrameReport[];
  site?: SiteOffer;
  /** The clicked `blob:` video's Played source (SITE-MEDIA-SPEC §2.9). */
  played?: PlayedOffer;
  /**
   * The directories of the segments the frame's page fetched last (its resource timing, read at the
   * click): the stream playing now is there. Without them, the frame's recorded segment files say.
   */
  playingDirs?: readonly string[];
  features: { streams: boolean; video: boolean; clips?: boolean };
  /** The stream probe cache's state of a stream. */
  streamProbe: (item: MediaItem) => VideoTaskState<VideoInfoResult> | undefined;
  /** The stream was refused: it is not offered. */
  refused: (item: MediaItem) => boolean;
  /** Streams a page video hides (googlevideo manifests of a YouTube page). */
  hideStream?: (item: MediaItem) => boolean;
  /** The hand-off state of a send key (an item key, or elementSendKey). */
  addState: (sendKey: string) => VideoTaskState<AddedResult> | undefined;
  /** The sender frame saw a response of this address and it was not an MP4: no element file. */
  rejected?: (url: string) => boolean;
}

/** Whether an item is the sender frame's own: of its frame and (on Chromium) its document. */
function ownItem(item: FrameRef, sender: FrameSender): boolean {
  return item.frameId === sender.frameId && sameDocument(item.documentId, sender.documentId);
}

function fileOffer(item: MediaItem, input: FrameOffersInput, reports: ReadonlyMap<number, FrameReport>): FileOffer {
  const el = elementOf(item, reports.get(item.frameId));
  const same = item.frameId === input.sender.frameId;
  const offer: FileOffer = {
    key: itemKey(item),
    display: displayOf(item.url),
    size: item.size,
    sizeKnown: item.size > 0,
    filename: fileName({ filename: item.filename, url: item.url, requestUrl: item.requestUrl, pageTitle: input.title }),
    frame: same ? 'this' : 'other',
    matched: same && fileMatches(item, input.src),
    short: item.short || shortClip(el),
    width: el?.videoWidth ?? 0,
    height: el?.videoHeight ?? 0,
    durationMs: el ? el.durationMs : -1,
  };
  const add = input.addState(offer.key);
  if (add) offer.add = add;
  return offer;
}

/** Segments fetched within this long of the frame's newest one are of the video playing now. */
export const PLAYING_SEGMENTS_MS = 10_000;

/**
 * The directories of the segments a frame fetched last (within PLAYING_SEGMENTS_MS of its newest):
 * those of the stream the video playing now plays (VIDEO-DETECT-SPEC §2.6). Empty without segments.
 */
export function playingSegmentDirs(items: readonly MediaItem[]): Set<string> {
  const segs = items.filter((i) => !isStream(i) && i.segment);
  const newest = Math.max(...segs.map((i) => i.lastSeen));
  const dirs = new Set<string>();
  for (const s of segs) {
    if (s.lastSeen < newest - PLAYING_SEGMENTS_MS) continue;
    const d = directoryOf(s.url);
    if (d) dirs.add(d);
  }
  return dirs;
}

/**
 * Whether a stream's playlist is of the segments in `dirs`: in the same directory, or a directory
 * above them at least two levels deep (a master playlist above its variants' folders).
 */
export function streamOfSegments(url: string, dirs: ReadonlySet<string>): boolean {
  const own = directoryOf(url);
  if (!own) return false;
  if (dirs.has(own)) return true;
  let deep = false;
  try {
    deep = new URL(own).pathname.split('/').filter((s) => s !== '').length >= 2;
  } catch {
    deep = false;
  }
  return deep && [...dirs].some((d) => d.startsWith(own));
}

function streamOffer(item: MediaItem, input: FrameOffersInput, playing?: ReadonlySet<string>): StreamOffer {
  const same = item.frameId === input.sender.frameId;
  const offer: StreamOffer = {
    key: itemKey(item),
    format: item.format === 'dash' ? 'dash' : 'hls',
    display: displayOf(item.url),
    group: streamGroup(item.url),
    frame: same ? 'this' : 'other',
    // Several streams in the frame (a grid of previews): only the one whose segments are being
    // fetched is the clicked video's (measured 2026-11-18: 48 playlists of a grid all marked).
    matched: same && input.mse && (playing === undefined || streamOfSegments(item.url, playing)),
  };
  const probe = input.streamProbe(item);
  if (probe) offer.probe = probe;
  const add = input.addState(offer.key);
  if (add) offer.add = add;
  return offer;
}

/**
 * The panel of the frame `input.sender` (§2.6): its own files and streams, without segments and
 * refused streams; an element file when the clicked element plays an MP4 address no recorded file
 * has; the frame's site source; and whether the clicked element is protected or live (an MSE
 * element also when Downex refused every stream of its frame as live). A sub-frame
 * gets no title and no top host (the panel says "Video" and "Don't show on this site"). Files:
 * the clicked element's first, then full ones before short ones, largest first. Streams: newest
 * first.
 */
export function buildFrameOffers(input: FrameOffersInput): FrameOffers {
  const { sender, src } = input;
  if (sender.frameId !== 0) input = { ...input, title: '', topHost: '' };
  const reports = new Map(input.reports.map((r) => [r.frameId, r]));
  const own = reports.get(sender.frameId);
  const visible = input.items.filter((i) => ownItem(i, sender));
  const fileItems = visible.filter((i) => !isStream(i) && !segmentOf(i, reports.get(i.frameId)));
  const files = fileItems.map((i) => fileOffer(i, input, reports));
  if (src !== '' && isMp4Path(src) && !fileItems.some((i) => fileMatches(i, src)) && !input.rejected?.(src)) {
    const el = own && sameDocument(own.documentId, sender.documentId) ? own.videos.find((v) => sameUrl(v.src, src)) : undefined;
    if (el) {
      const key = `e${el.id}`;
      const offer: FileOffer = {
        key,
        display: displayOf(src),
        size: 0,
        sizeKnown: false,
        filename: fileName({ url: src, pageTitle: input.title }),
        frame: 'this',
        matched: true,
        short: shortClip(el),
        width: el.videoWidth,
        height: el.videoHeight,
        durationMs: el.durationMs,
      };
      const add = input.addState(elementSendKey(key, sender.frameId));
      if (add) offer.add = add;
      files.push(offer);
    }
  }
  files.sort(
    (a, b) =>
      Number(b.matched) - Number(a.matched) ||
      Number(a.short) - Number(b.short) ||
      Number(a.frame === 'other') - Number(b.frame === 'other') ||
      b.size - a.size,
  );
  const frameStreams = visible.filter((i) => isStream(i) && !input.hideStream?.(i));
  // Downex refused the stream as live (hls.js gives a live stream a finite, growing duration, so
  // the element alone does not tell).
  const refusedLive = (i: MediaItem) => {
    const p = input.streamProbe(i);
    return p !== undefined && 'result' in p && !p.result.ok && p.result.error.code === 'rejected' && daemonCode(p.result.error) === 'live';
  };
  const offered = frameStreams.filter((i) => !input.refused(i));
  // The streams of the segments fetched last, when that names some of several (else every stream
  // of the frame is the clicked video's, as before).
  const ownStreams = offered.filter((i) => i.frameId === sender.frameId);
  const dirs =
    input.playingDirs && input.playingDirs.length > 0
      ? new Set(input.playingDirs)
      : playingSegmentDirs(input.items.filter((i) => i.frameId === sender.frameId && sameDocument(i.documentId, sender.documentId)));
  const playing = ownStreams.length > 1 && dirs.size > 0 && ownStreams.some((i) => streamOfSegments(i.url, dirs)) ? dirs : undefined;
  const streams = offered
    .sort((a, b) => Number(b.frameId === sender.frameId) - Number(a.frameId === sender.frameId) || b.lastSeen - a.lastSeen)
    .map((i) => streamOffer(i, input, playing));
  const clicked = own?.videos.filter((v) => (src !== '' ? sameUrl(v.src, src) : input.mse && v.mse)) ?? [];
  const offers: FrameOffers = {
    title: input.title,
    topHost: input.topHost,
    files,
    streams,
    features: { ...input.features },
    drm: clicked.length > 0 && clicked.every((v) => v.drm),
    live:
      (clicked.length > 0 && clicked.every((v) => v.durationMs === -2)) ||
      (src === '' && input.mse && frameStreams.length > 0 && frameStreams.every(refusedLive)),
  };
  if (input.site) offers.site = input.site;
  if (input.played) offers.played = input.played;
  return offers;
}

/** What an offer key names for the frame that sent it. */
export type ResolvedOffer =
  | { kind: 'file'; item: MediaItem }
  | { kind: 'stream'; item: MediaItem }
  | { kind: 'element'; video: FrameVideoMeta; sendKey: string };

/**
 * The item a frame's key names (§3.7, §6.2), looked up in the tab's own store: a file or stream
 * of the frame (any of the tab for the top frame), or the frame's reported element playing an
 * MP4 address (a recorded file playing it now wins). Undefined when it is not, or no longer, one.
 */
export function resolveOfferKey(
  key: string,
  items: readonly MediaItem[],
  reports: readonly FrameReport[],
  sender: FrameSender,
  rejected?: (url: string) => boolean,
): ResolvedOffer | undefined {
  if (!OFFER_KEY_RE.test(key)) return undefined;
  if (key.startsWith('e')) {
    const id = Number(key.slice(1));
    const report = reports.find((r) => r.frameId === sender.frameId && sameDocument(r.documentId, sender.documentId));
    const video = report?.videos.find((v) => v.id === id);
    if (!video || !isMp4Path(video.src)) return undefined;
    const file = items.find((i) => !isStream(i) && itemVisibleTo(i, sender) && fileMatches(i, video.src));
    if (file) return { kind: 'file', item: file };
    return rejected?.(video.src) ? undefined : { kind: 'element', video, sendKey: elementSendKey(key, sender.frameId) };
  }
  const item = items.find((i) => itemKey(i) === key);
  if (!item || !itemVisibleTo(item, sender)) return undefined;
  if (key.startsWith('f')) return segmentOf(item, reports.find((r) => r.frameId === item.frameId)) ? undefined : { kind: 'file', item };
  return { kind: 'stream', item };
}

// ---------------------------------------------------------------------------------------
// Popup ("Videos on this page", §5.3)

/**
 * The popup's files of a tab, newest first, with their hand-off state: never segments, nor the
 * files of frames whose videos are all live.
 */
export function tabFileEntries(
  items: readonly MediaItem[],
  reports: readonly FrameReport[],
  title: string,
  addState: (sendKey: string) => VideoTaskState<AddedResult> | undefined,
): FileEntry[] {
  const byFrame = new Map(reports.map((r) => [r.frameId, r]));
  return items
    .filter((i) => !isStream(i) && !segmentOf(i, byFrame.get(i.frameId)) && !allLive(byFrame.get(i.frameId)))
    .map((i) => {
      const el = elementOf(i, byFrame.get(i.frameId));
      const e: FileEntry = {
        key: itemKey(i),
        display: displayOf(i.url),
        size: i.size,
        sizeKnown: i.size > 0,
        filename: fileName({ filename: i.filename, url: i.url, requestUrl: i.requestUrl, pageTitle: title }),
        matched: el !== undefined,
        short: i.short || shortClip(el),
        width: el?.videoWidth ?? 0,
        height: el?.videoHeight ?? 0,
        durationMs: el ? el.durationMs : -1,
        url: i.url,
        frame: i.frameId > 0 ? 'embed' : 'top',
      };
      const add = addState(e.key);
      if (add) e.add = add;
      return e;
    });
}

/** The popup entry of a stream, with its hand-off and probe state. */
export function streamEntry(item: MediaItem, send: MediaSendState, probe: VideoTaskState<VideoInfoResult> | undefined): StreamEntry {
  const e: StreamEntry = { ...toMediaEntry(item), ...send, key: itemKey(item), frame: item.frameId > 0 ? 'embed' : 'top' };
  if (probe) e.probe = probe;
  return e;
}

/** What the popup shows for a stream of a frame whose videos are all protected: "Protected". */
export const PROTECTED_FRAME_PROBE: VideoTaskState<VideoInfoResult> = {
  result: failure('encrypted', 'The page plays this video with DRM protection.', 'encrypted'),
};

/**
 * The popup's streams of a tab (§5.3), newest first: the streams of frames whose videos are all
 * live are left out, and those of frames whose videos are all protected read as refused
 * (`encrypted`) until Downex itself says otherwise.
 */
export function tabStreamEntries(
  streams: readonly MediaItem[],
  reports: readonly FrameReport[],
  send: (item: MediaItem) => MediaSendState,
  probe: (item: MediaItem) => VideoTaskState<VideoInfoResult> | undefined,
): StreamEntry[] {
  const byFrame = new Map(reports.map((r) => [r.frameId, r]));
  return streams
    .filter((i) => isStream(i) && !allLive(byFrame.get(i.frameId)))
    .map((i) => streamEntry(i, send(i), probe(i) ?? (allProtected(byFrame.get(i.frameId)) ? PROTECTED_FRAME_PROBE : undefined)));
}

// ---------------------------------------------------------------------------------------
// Badge (§5.3)

export interface TabGroupsInput {
  items: readonly MediaItem[];
  /** The page video (curated or YouTube) each frame shows (0: the top frame). */
  pageVideos: ReadonlyMap<number, string>;
  reports: readonly FrameReport[];
  /** The stream was refused (protected, live, no MP4 video). */
  refused: (item: MediaItem) => boolean;
}

/**
 * The badge's count of a tab: each page video (which takes in its frame's network items), each
 * frame with a stream that was not refused, and each file that is neither short nor a segment;
 * nothing of frames whose reported videos are all protected or all live.
 */
export function tabGroupCount(input: TabGroupsInput): number {
  const reports = new Map(input.reports.map((r) => [r.frameId, r]));
  const excluded = (frameId: number) => protectedOrLive(reports.get(frameId));
  const videos = new Set<string>();
  for (const [frameId, key] of input.pageVideos) if (!excluded(frameId)) videos.add(key);
  const streamFrames = new Set<number>();
  let files = 0;
  for (const it of input.items) {
    if (input.pageVideos.has(it.frameId) || excluded(it.frameId)) continue;
    if (isStream(it)) {
      if (!input.refused(it)) streamFrames.add(it.frameId);
    } else if (!segmentOf(it, reports.get(it.frameId)) && !it.short && !shortClip(elementOf(it, reports.get(it.frameId)))) {
      files++;
    }
  }
  return videos.size + streamFrames.size + files;
}
