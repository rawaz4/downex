// Messages between extension contexts (content script / popup / options → background, and
// background → content script: FrameMessage).

import type { AddedResult, ConnectionStatus, Outcome, StatusResult, VideoInfoResult } from './protocol';
import type { HandoffRecord } from './history';
import type { LinksBatch, LinksSummary } from './page-links';
import type { SiteVideoKind } from './site-video';
import {
  OFFER_KEY_RE,
  type FileEntry,
  type FrameOffers,
  type FrameVideoMeta,
  type OfferKey,
  type StreamEntry,
} from './offer-types';
import { isTabId } from './tab-pause';

/** Content script → background: the last click's modifiers (Alt bypass, BROWSER-SPEC §3). */
export interface ClickMessage {
  type: 'downex:click';
  alt: boolean;
  ctrl: boolean;
  shift: boolean;
  meta: boolean;
  /** Epoch ms of the click. */
  ts: number;
}

/** Popup → background: connection state and recent hand-offs. */
export interface GetStateMessage {
  type: 'downex:get-state';
}

/** Popup → background: asks the relay for daemon status. */
export interface QueryStatusMessage {
  type: 'downex:query-status';
}

/** Confirmation page → background: the links waiting for confirmation. */
export interface LinksBatchMessage {
  type: 'downex:links-batch';
  batchId: string;
}

/** Confirmation page → background: hand off (accept) or drop the batch. */
export interface LinksConfirmMessage {
  type: 'downex:links-confirm';
  batchId: string;
  accept: boolean;
}

/** Popup → background: whether capture is paused in a tab. */
export interface TabStateMessage {
  type: 'downex:tab-state';
  tabId: number;
}

/** Popup → background: pauses or resumes capture in a tab until it closes. */
export interface SetTabPausedMessage {
  type: 'downex:set-tab-paused';
  tabId: number;
  paused: boolean;
}

/** Popup → background: the HLS/DASH streams seen in a tab (MEDIA-SPEC §6.1). */
export interface TabMediaMessage {
  type: 'downex:tab-media';
  tabId: number;
}

/**
 * Popup → background: hands one listed stream of a tab to Downex (`addMedia`). Missing or ""
 * tracks: Downex's default pick; they are sent only to a relay that lists `streams`.
 */
export interface AddMediaMessage {
  type: 'downex:add-media';
  tabId: number;
  /** Manifest URL as listed by `downex:tab-media`. */
  url: string;
  videoTrack?: string;
  audioTrack?: string;
}

/**
 * Popup → background: the answer to the stream's `addMedia` once it is known; waits for one
 * still running (sent from a popup that closed since).
 */
export interface MediaResultMessage {
  type: 'downex:media-result';
  tabId: number;
  url: string;
}

/**
 * YouTube overlay (content script) → background: the qualities of the frame's video
 * (SITE-MEDIA-SPEC §3.6). `url` is the frame's address; the background checks it.
 */
export interface PageVideoProbeMessage {
  type: 'downex:page-video-probe';
  url: string;
}

/**
 * YouTube content script (top frame) → background: the user stayed on this watch or Shorts video
 * in a visible tab (BROWSER-SPEC §4): its qualities may be got ahead. Not answered.
 */
export interface PageVideoPrefetchMessage {
  type: 'downex:page-video-prefetch';
  url: string;
}

/** YouTube overlay → background: adds the frame's video with the chosen tracks. */
export interface PageVideoAddMessage {
  type: 'downex:page-video-add';
  url: string;
  videoTrack: string;
  audioTrack: string;
}

/** Popup → background: the qualities of a page video listed by `downex:tab-media`. */
export interface TabVideoProbeMessage {
  type: 'downex:tab-video-probe';
  tabId: number;
  key: string;
}

/** Popup → background: adds a listed page video ("" tracks: Downex's default pick). */
export interface TabVideoAddMessage {
  type: 'downex:tab-video-add';
  tabId: number;
  key: string;
  videoTrack: string;
  audioTrack: string;
}

/** Popup → background: "Find the video on this page" of any http(s) tab (user-initiated). */
export interface FindPageVideoMessage {
  type: 'downex:find-page-video';
  tabId: number;
}

// --- Video detection on every site (VIDEO-DETECT-SPEC §3.6) ----------------------------

/**
 * Video tracker (content script) → background: the frame's tracked `<video>` elements, at most
 * once a second and only when something changed. Not answered. At most MAX_FRAME_VIDEOS
 * entries with unique ids and MAX_FRAME_VIDEOS_BYTES of JSON.
 */
export interface FrameVideosMessage {
  type: 'downex:frame-videos';
  videos: FrameVideoMeta[];
}

/** Video tracker → background: inject the overlay UI (VIDEO_UI_SCRIPT) into the sender's frame. */
export interface VideoUiMessage {
  type: 'downex:video-ui';
}

/** Video tracker or overlay UI → background: whether buttons may show for the sender's tab. */
export interface OverlayStateMessage {
  type: 'downex:overlay-state';
}

/**
 * Overlay UI → background: what the clicked element's panel lists. `src` is the element's
 * `currentSrc` when http(s) (no fragment, ≤ 32 KiB), else ""; it is used only to match items.
 */
export interface FrameOffersMessage {
  type: 'downex:frame-offers';
  src: string;
  /** The element plays a `blob:` URL (Media Source Extensions). */
  mse: boolean;
}

/** Overlay UI → background: the MP4 qualities of one of the frame's streams (`probeStream`). */
export interface StreamProbeMessage {
  type: 'downex:stream-probe';
  /** A stream key (`s<seq>`) from `downex:frame-offers`. */
  key: OfferKey;
}

/**
 * Overlay UI → background: hands an offer to Downex. A file or element-file key → `add`; a
 * stream key → `addMedia` (tracks only to a relay that lists `streams`; missing or "" = the
 * default pick).
 */
export interface OfferAddMessage {
  type: 'downex:offer-add';
  key: OfferKey;
  videoTrack?: string;
  audioTrack?: string;
}

/**
 * Overlay UI or popup → background: hides ("Don't show on <host>") or shows the Download buttons
 * on the exact host of the tab's page: the sender's tab for a content script, `tabId` for the
 * popup.
 */
export interface SetSiteHiddenMessage {
  type: 'downex:set-site-hidden';
  /** The popup's tab; absent from a content script. */
  tabId?: number;
  hidden: boolean;
}

/** Popup → background: the MP4 qualities of a stream listed by `downex:tab-media`. */
export interface TabStreamProbeMessage {
  type: 'downex:tab-stream-probe';
  tabId: number;
  /** Manifest URL as listed; looked up among the tab's streams, never probed as given. */
  url: string;
}

/** Popup → background: hands one listed direct file of a tab to Downex (`add`). */
export interface AddFileMessage {
  type: 'downex:add-file';
  tabId: number;
  /** File URL as listed (`FileEntry.url`); looked up among the tab's files, never fetched as given. */
  url: string;
}

export type ExtensionMessage =
  | ClickMessage
  | GetStateMessage
  | QueryStatusMessage
  | LinksBatchMessage
  | LinksConfirmMessage
  | TabStateMessage
  | SetTabPausedMessage
  | TabMediaMessage
  | AddMediaMessage
  | MediaResultMessage
  | PageVideoProbeMessage
  | PageVideoPrefetchMessage
  | PageVideoAddMessage
  | TabVideoProbeMessage
  | TabVideoAddMessage
  | FindPageVideoMessage
  | FrameVideosMessage
  | VideoUiMessage
  | OverlayStateMessage
  | FrameOffersMessage
  | StreamProbeMessage
  | OfferAddMessage
  | SetSiteHiddenMessage
  | TabStreamProbeMessage
  | AddFileMessage;

/**
 * Background → a frame's content script (`tabs.sendMessage(tabId, m, {frameId})`): media or
 * manifest traffic was seen for a frame that reported no video, so the tracker scans its shadow
 * roots (VIDEO-DETECT-SPEC §2.5). Not answered.
 */
export interface FrameMediaSeenMessage {
  type: 'downex:frame-media-seen';
}

/** Messages the background sends to content scripts (validated there with isFrameMessage). */
export type FrameMessage = FrameMediaSeenMessage;

export interface StateResponse {
  connection: ConnectionStatus;
  history: HandoffRecord[];
}

export type QueryStatusResponse = Outcome<StatusResult>;

/** null when the batch expired or was already answered. */
export type LinksBatchResponse = LinksBatch | null;

export type LinksConfirmResponse =
  | { status: 'expired' }
  | { status: 'cancelled' }
  | ({ status: 'done' } & LinksSummary);

/** The tab's pause state after the request (a failed write still pauses until the worker restarts). */
export interface TabStateResponse {
  paused: boolean;
}

/** A probe or add of a page video the background runs, or its kept answer. */
export type VideoTaskState<T> = { running: true } | { result: Outcome<T> };

/** A video of the tab's page (SITE-MEDIA-SPEC §5.2): its address, never cookies. */
export interface PageVideoEntry {
  /** `youtube:<id>`, or `page:<address>` for "Find the video on this page". */
  key: string;
  /** "YouTube", or the page's host for a found video. */
  site: string;
  kind: SiteVideoKind | 'page';
  /** The tab's page, or an embedded player in one of its frames. */
  frame: 'top' | 'embed';
  /** The address Downex resolves. */
  pageUrl: string;
  title?: string;
  probe?: VideoTaskState<VideoInfoResult>;
  add?: VideoTaskState<AddedResult>;
}

/**
 * Newest first; never the recorded headers. `videos`: the page's videos (curated sites, YouTube,
 * embeds), listed before the streams and files of each frame.
 */
export interface TabMediaResponse {
  /** The tab's HLS/DASH streams. */
  items: StreamEntry[];
  /** The tab's direct MP4 files, segment-like ones never included. */
  files: FileEntry[];
  videos: PageVideoEntry[];
  /** A hidden-sites entry hides the Download buttons on the tab's host. */
  hiddenHere: boolean;
  /** The tab's host ("" when it is not an http(s) page). */
  host: string;
}

/** Error code `expired`: the stream is no longer listed (the tab navigated or closed). */
export type AddMediaResponse = Outcome<AddedResult>;

/** No `result`: the stream's hand-off is neither running nor recent. */
export interface MediaResultResponse {
  result?: AddMediaResponse;
}

/**
 * Local codes: `unsupported_page` (not a video page, or not one the sender may ask about),
 * `outdated_downex` (the relay does not serve videos), `expired` (no longer on the page).
 */
export type VideoProbeResponse = Outcome<VideoInfoResult>;
export type VideoAddResponse = Outcome<AddedResult>;
/** The entry of the tab's page; `unsupported_page` for a tab that is not an http(s) page. */
export type FindPageVideoResponse = Outcome<PageVideoEntry>;

/** Whether the overlay UI was injected into the sender's frame (or already was). */
export interface VideoUiResponse {
  ok: boolean;
}

/** Whether Download buttons may show in the sender's tab, and the host that decided it. */
export interface OverlayStateResponse {
  button: boolean;
  /** The host of the tab's page ("" when unknown). */
  host: string;
}

/** Local code `expired`: the frame's items are gone (the frame navigated). */
export type FrameOffersResponse = Outcome<FrameOffers>;

/**
 * `downex:stream-probe` and `downex:tab-stream-probe`. Local codes: `outdated_downex` (the relay
 * does not list `streams`), `expired` (the stream is no longer listed).
 */
export type StreamProbeResponse = Outcome<VideoInfoResult>;

/** `downex:offer-add` and `downex:add-file`. Local code `expired`: the item is no longer listed. */
export type OfferAddResponse = Outcome<AddedResult>;

/** Whether the buttons are hidden on the host after the request (a wildcard entry may keep them hidden). */
export interface SiteHiddenResponse {
  hidden: boolean;
}

export interface ResponseFor {
  'downex:click': undefined;
  'downex:get-state': StateResponse;
  'downex:query-status': QueryStatusResponse;
  'downex:links-batch': LinksBatchResponse;
  'downex:links-confirm': LinksConfirmResponse;
  'downex:tab-state': TabStateResponse;
  'downex:set-tab-paused': TabStateResponse;
  'downex:tab-media': TabMediaResponse;
  'downex:add-media': AddMediaResponse;
  'downex:media-result': MediaResultResponse;
  'downex:page-video-probe': VideoProbeResponse;
  'downex:page-video-prefetch': undefined;
  'downex:page-video-add': VideoAddResponse;
  'downex:tab-video-probe': VideoProbeResponse;
  'downex:tab-video-add': VideoAddResponse;
  'downex:find-page-video': FindPageVideoResponse;
  'downex:frame-videos': undefined;
  'downex:video-ui': VideoUiResponse;
  'downex:overlay-state': OverlayStateResponse;
  'downex:frame-offers': FrameOffersResponse;
  'downex:stream-probe': StreamProbeResponse;
  'downex:offer-add': OfferAddResponse;
  'downex:set-site-hidden': SiteHiddenResponse;
  'downex:tab-stream-probe': StreamProbeResponse;
  'downex:add-file': OfferAddResponse;
}

const TYPES: ReadonlySet<string> = new Set([
  'downex:click',
  'downex:get-state',
  'downex:query-status',
  'downex:links-batch',
  'downex:links-confirm',
  'downex:tab-state',
  'downex:set-tab-paused',
  'downex:tab-media',
  'downex:add-media',
  'downex:media-result',
  'downex:page-video-probe',
  'downex:page-video-prefetch',
  'downex:page-video-add',
  'downex:tab-video-probe',
  'downex:tab-video-add',
  'downex:find-page-video',
  'downex:frame-videos',
  'downex:video-ui',
  'downex:overlay-state',
  'downex:frame-offers',
  'downex:stream-probe',
  'downex:offer-add',
  'downex:set-site-hidden',
  'downex:tab-stream-probe',
  'downex:add-file',
]);

/** Most videos one `downex:frame-videos` may report. */
export const MAX_FRAME_VIDEOS = 16;
/** Most JSON characters the videos of one `downex:frame-videos` may take. */
export const MAX_FRAME_VIDEOS_BYTES = 64 * 1024;
/** Largest tracker-local video id. */
const MAX_VIDEO_ID = 2 ** 31 - 1;
/** Largest rendered size reported, in CSS px. */
const MAX_RENDERED_PX = 1_000_000;
/** Largest intrinsic size (the MP4 sample entry's 16-bit fields). */
const MAX_VIDEO_PX = 65_535;

/** Longest manifest URL a message may name (DAEMON-SPEC §4 URL limit). */
const MAX_MESSAGE_URL_LENGTH = 32 * 1024;
/** Longest page video key: `page:` and an address. */
const MAX_VIDEO_KEY_LENGTH = MAX_MESSAGE_URL_LENGTH + 16;
/** Longest track id (DAEMON-SPEC §4 `validate.TrackID`). */
const MAX_TRACK_ID_LENGTH = 1024;

function isText(v: unknown, max: number): v is string {
  return typeof v === 'string' && v !== '' && v.length <= max;
}

function isTrack(v: unknown): v is string {
  return typeof v === 'string' && v.length <= MAX_TRACK_ID_LENGTH;
}

function isOptionalTrack(v: unknown): v is string | undefined {
  return v === undefined || isTrack(v);
}

/** "" or an absolute http(s) URL without fragment of at most 32 KiB. */
function isElementSrc(v: unknown): v is string {
  if (typeof v !== 'string') return false;
  if (v === '') return true;
  return v.length <= MAX_MESSAGE_URL_LENGTH && /^https?:\/\/[^#]+$/i.test(v);
}

function isCountUpTo(v: unknown, max: number): v is number {
  return typeof v === 'number' && Number.isSafeInteger(v) && v >= 0 && v <= max;
}

/** A well-formed FrameVideoMeta; extra fields are ignored. */
export function isFrameVideoMeta(v: unknown): v is FrameVideoMeta {
  if (typeof v !== 'object' || v === null) return false;
  const m = v as Record<string, unknown>;
  return (
    typeof m.id === 'number' &&
    Number.isSafeInteger(m.id) &&
    m.id >= 1 &&
    m.id <= MAX_VIDEO_ID &&
    isElementSrc(m.src) &&
    isCountUpTo(m.width, MAX_RENDERED_PX) &&
    isCountUpTo(m.height, MAX_RENDERED_PX) &&
    isCountUpTo(m.videoWidth, MAX_VIDEO_PX) &&
    isCountUpTo(m.videoHeight, MAX_VIDEO_PX) &&
    typeof m.durationMs === 'number' &&
    Number.isSafeInteger(m.durationMs) &&
    m.durationMs >= -2 &&
    typeof m.mse === 'boolean' &&
    typeof m.drm === 'boolean' &&
    typeof m.visible === 'boolean' &&
    typeof m.decorative === 'boolean' &&
    typeof m.meaningful === 'boolean'
  );
}

function isFrameVideos(v: unknown): v is FrameVideoMeta[] {
  if (!Array.isArray(v) || v.length > MAX_FRAME_VIDEOS || !v.every(isFrameVideoMeta)) return false;
  if (new Set(v.map((m) => m.id)).size !== v.length) return false;
  return JSON.stringify(v).length <= MAX_FRAME_VIDEOS_BYTES;
}

function isOfferKey(v: unknown): v is OfferKey {
  return typeof v === 'string' && OFFER_KEY_RE.test(v);
}

export function isExtensionMessage(v: unknown): v is ExtensionMessage {
  if (typeof v !== 'object' || v === null) return false;
  const t = (v as { type?: unknown }).type;
  if (typeof t !== 'string' || !TYPES.has(t)) return false;
  const m = v as Record<string, unknown>;
  if (t === 'downex:links-batch') return typeof m.batchId === 'string';
  if (t === 'downex:links-confirm') return typeof m.batchId === 'string' && typeof m.accept === 'boolean';
  if (t === 'downex:tab-state' || t === 'downex:tab-media') return isTabId(m.tabId);
  if (t === 'downex:set-tab-paused') return isTabId(m.tabId) && typeof m.paused === 'boolean';
  if (t === 'downex:add-media') {
    return (
      isTabId(m.tabId) && isText(m.url, MAX_MESSAGE_URL_LENGTH) && isOptionalTrack(m.videoTrack) && isOptionalTrack(m.audioTrack)
    );
  }
  if (t === 'downex:media-result' || t === 'downex:tab-stream-probe' || t === 'downex:add-file') {
    return isTabId(m.tabId) && isText(m.url, MAX_MESSAGE_URL_LENGTH);
  }
  if (t === 'downex:frame-videos') return isFrameVideos(m.videos);
  if (t === 'downex:frame-offers') return isElementSrc(m.src) && typeof m.mse === 'boolean';
  if (t === 'downex:stream-probe') return isOfferKey(m.key) && m.key.startsWith('s');
  if (t === 'downex:offer-add') return isOfferKey(m.key) && isOptionalTrack(m.videoTrack) && isOptionalTrack(m.audioTrack);
  if (t === 'downex:set-site-hidden') return typeof m.hidden === 'boolean' && (m.tabId === undefined || isTabId(m.tabId));
  if (t === 'downex:page-video-probe' || t === 'downex:page-video-prefetch') return isText(m.url, MAX_MESSAGE_URL_LENGTH);
  if (t === 'downex:page-video-add') {
    return isText(m.url, MAX_MESSAGE_URL_LENGTH) && isTrack(m.videoTrack) && isTrack(m.audioTrack);
  }
  if (t === 'downex:tab-video-probe') return isTabId(m.tabId) && isText(m.key, MAX_VIDEO_KEY_LENGTH);
  if (t === 'downex:tab-video-add') {
    return isTabId(m.tabId) && isText(m.key, MAX_VIDEO_KEY_LENGTH) && isTrack(m.videoTrack) && isTrack(m.audioTrack);
  }
  if (t === 'downex:find-page-video') return isTabId(m.tabId);
  if (t !== 'downex:click') return true;
  return (
    typeof m.alt === 'boolean' &&
    typeof m.ctrl === 'boolean' &&
    typeof m.shift === 'boolean' &&
    typeof m.meta === 'boolean' &&
    typeof m.ts === 'number'
  );
}

const FRAME_TYPES: ReadonlySet<string> = new Set(['downex:frame-media-seen']);

/** A message of the background to a content script (FrameMessage). */
export function isFrameMessage(v: unknown): v is FrameMessage {
  if (typeof v !== 'object' || v === null) return false;
  const t = (v as { type?: unknown }).type;
  return typeof t === 'string' && FRAME_TYPES.has(t);
}
