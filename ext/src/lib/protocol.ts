// Native-messaging protocol between the extension and downex-nmhost (BROWSER-SPEC §2).
// Pure module: the browser port is injected, so everything here runs under vitest.

import { CLIP_TEXTS, isClipMs, type VideoChapter } from './clip';

export type { VideoChapter } from './clip';

export const NATIVE_HOST_NAME = 'com.downex.host';
export const PROTOCOL_VERSION = 1;
/** The relay rejects browser → host messages larger than this (UTF-8 JSON bytes). */
export const MAX_OUTBOUND_MESSAGE_BYTES = 2 * 1024 * 1024;
/** Largest decoded POST body the relay accepts (DAEMON-SPEC §4). */
export const MAX_BODY_BYTES = 1024 * 1024;
/** How long a timed-out request's answer is still reported (the relay gives an add ≤ 15 s). */
export const LATE_RESPONSE_MS = 30_000;

export type BrowserName = 'chrome' | 'edge' | 'firefox' | 'other';
export type HttpMethod = 'GET' | 'POST';
export type HeaderPair = [name: string, value: string];

export interface HelloParams {
  type: 'hello';
  protocol: typeof PROTOCOL_VERSION;
  browser: BrowserName;
  extVersion: string;
}

export interface AddFields {
  url: string;
  method: HttpMethod;
  headers: HeaderPair[];
  /** Base64 of at most MAX_BODY_BYTES decoded bytes; POST only. */
  body?: string;
  filename?: string;
  referrer?: string;
  mime?: string;
  size?: number;
  startPaused?: boolean;
  /**
   * The page the link was found on (http or https, no user name): Downex offers to open it
   * when the link expires (DESKTOP-SPEC §10). A relay that does not list `renew` ignores it
   * (unknown fields are ignored).
   */
  pageUrl?: string;
  /**
   * Milliseconds the relay has to commit the download. After that the extension has given
   * up and the browser kept the download, so the relay must drop the request.
   */
  deadlineMs?: number;
}

export interface AddParams extends AddFields {
  type: 'add';
}

/** Longest page title an `addMedia` may carry (BROWSER-SPEC §2). */
export const MAX_MEDIA_TITLE_CHARS = 512;

/** A streaming-media (HLS/DASH) hand-off (BROWSER-SPEC §2 `addMedia`, MEDIA-SPEC §6). */
export interface AddMediaFields {
  /** The .m3u8 / .mpd manifest the page loaded. */
  manifestUrl: string;
  /** Headers the page sent for the manifest (Cookie/Referer/User-Agent …). */
  headers: HeaderPair[];
  pageUrl?: string;
  /** Page title, at most MAX_MEDIA_TITLE_CHARS. */
  title?: string;
  /** As for `add`: how long the extension waits; the relay drops what it cannot commit by then. */
  deadlineMs?: number;
  /**
   * Track ids of a `probeStream` answer; missing or "" = Downex's default pick. Sent only to a
   * relay that lists FEATURE_STREAMS: an older one would ignore them.
   */
  videoTrack?: string;
  audioTrack?: string;
  /**
   * A part of the video (MEDIA-SPEC §8): its start and end in ms (end 0 or missing = to the end).
   * Sent only to a relay that lists FEATURE_CLIPS: NativeClient refuses it to an older one, which
   * would drop it and save the whole video.
   */
  clipStartMs?: number;
  clipEndMs?: number;
}

export interface AddMediaParams extends AddMediaFields {
  type: 'addMedia';
}

/**
 * Lists the MP4 qualities of a manifest a page loaded (VIDEO-DETECT-SPEC §3.5): the daemon's
 * MANIFEST probe, answered with a `videoInfo` (`site` "", `container` "mp4"). No side effects.
 * Sent only to a relay that lists FEATURE_STREAMS.
 */
export interface ProbeStreamFields {
  /** The .m3u8 / .mpd manifest the page loaded. */
  manifestUrl: string;
  /** Headers of the stream hand-off (the M5 rules of `addMedia`). */
  headers: HeaderPair[];
  pageUrl?: string;
  deadlineMs?: number;
}

export interface ProbeStreamParams extends ProbeStreamFields {
  type: 'probeStream';
}

/** The relay feature that serves `probeVideo` and `addVideo` (SITE-MEDIA-SPEC §3.5). */
export const FEATURE_VIDEO = 'video';
/**
 * The relay feature that forwards a look-ahead `probeVideo` (`prefetch`, BROWSER-SPEC §4): an
 * older relay drops the mark, and Downex would take the look-ahead for a user's probe.
 */
export const FEATURE_PREFETCH = 'prefetch';
/** The relay feature that serves `probeStream` and the tracks of `addMedia` (VIDEO-DETECT-SPEC §3.5). */
export const FEATURE_STREAMS = 'streams';
/** The relay serves `rescue` and `resume` (BROWSER-SPEC §6). */
export const FEATURE_RESCUE = 'rescue';
/** The relay takes a part of a video (`clipStartMs`/`clipEndMs`) and answers chapters (MEDIA-SPEC §8). */
export const FEATURE_CLIPS = 'clips';
/**
 * The relay takes `entryId`, one video of a page with several, and answers the page's items
 * (SITE-MEDIA-SPEC §3.12): an older relay would drop the item and Downex would take the page's
 * default one.
 */
export const FEATURE_ITEMS = 'items';
/**
 * The relay takes `cookieDomain`, the domain a Cookie header's sign-in is for (SITE-MEDIA-SPEC
 * §6.3). To an older relay the Cookie goes without it, and Downex keeps it to the page's own host.
 */
export const FEATURE_SIGN_IN = 'signIn';
/**
 * The relay takes `addVideo.browserFetch`, answers `added.fetchTicket` and `videoInfo.browserFetch`,
 * and serves `fetchTake` and `fetchPut` (browser-fetched saves, SITE-MEDIA-SPEC §2.8). An older relay
 * gets no `browserFetch` and is never asked to take or put.
 */
export const FEATURE_BROWSER_FETCH = 'browserFetch';
/** The relay feature that serves `probePlayed` and `addPlayed` (SITE-MEDIA-SPEC §2.9). */
export const FEATURE_PLAYED = 'played';
/** What a played request to a relay without FEATURE_PLAYED is answered with (code `outdated_downex`). */
export const OUTDATED_PLAYED_TEXT = 'Update Downex to save this video.';
/** What a request for an item to a relay without FEATURE_ITEMS is answered with (code `outdated_downex`). */
export const OUTDATED_ITEMS_TEXT = 'Update Downex to choose a video of this page.';
/** What a `rescue` or `resume` to a relay without FEATURE_RESCUE is answered with (code `outdated_downex`). */
export const OUTDATED_RESCUE_TEXT = 'Update Downex to finish downloads that failed in the browser.';
/** At most this many welcome features of at most MAX_FEATURE_CHARS each are read. */
export const MAX_FEATURES = 16;
export const MAX_FEATURE_CHARS = 32;
/** What a relay without FEATURE_VIDEO is answered with (code `outdated_downex`). */
export const OUTDATED_DOWNEX_TEXT = 'Update Downex to download videos.';
/** What a `probeStream` to a relay without FEATURE_STREAMS is answered with (code `outdated_downex`). */
export const OUTDATED_STREAMS_TEXT = 'Update Downex to choose a stream quality.';

/** What both video requests carry: the page, its headers and the deadline. */
export interface VideoPageFields {
  pageUrl: string;
  /** Only `Cookie` (the user's sign-in, SITE-MEDIA-SPEC §6.3) and `Referer` (the embedding page) mean something. */
  headers?: HeaderPair[];
  deadlineMs?: number;
  /**
   * The registrable domain the Cookie's sign-in is for (a curated site's, §6.3): Downex gives the
   * cookies to it and its subdomains. Missing: the page's own host. Dropped for a relay without
   * FEATURE_SIGN_IN.
   */
  cookieDomain?: string;
  /**
   * One video of a page with several, by the site's id of it (SITE-MEDIA-SPEC §3.12; missing: the
   * page's default). Sent only to a relay that lists FEATURE_ITEMS.
   */
  entryId?: string;
}

/**
 * Resolves a web page with yt-dlp in Downex and lists its MP4 qualities (SITE-MEDIA-SPEC §3.5).
 * No side effects; `tools_installing` and `site_resolving` refusals mean "ask again".
 */
export interface ProbeVideoFields extends VideoPageFields {
  /**
   * A look-ahead while the user watches (BROWSER-SPEC §4): Downex never installs tools for it
   * (`tools_unavailable` while they are not ready) and never lets it delay a user's probe.
   * Sent only to a relay that lists FEATURE_PREFETCH.
   */
  prefetch?: boolean;
}

export interface ProbeVideoParams extends ProbeVideoFields {
  type: 'probeVideo';
}

/** Adds a page's video; empty tracks ask for Downex's default pick. */
export interface AddVideoFields extends VideoPageFields {
  videoTrack?: string;
  audioTrack?: string;
  /** Page title, at most MAX_MEDIA_TITLE_CHARS. */
  title?: string;
  /** A part of the video, as for `addMedia` (FEATURE_CLIPS only). */
  clipStartMs?: number;
  clipEndMs?: number;
  /**
   * This browser can fetch the video's bytes when its servers answer only browsers (SITE-MEDIA-SPEC
   * §2.8); the answer then carries a fetch ticket. Dropped for a relay without FEATURE_BROWSER_FETCH.
   */
  browserFetch?: boolean;
}

export interface AddVideoParams extends AddVideoFields {
  type: 'addVideo';
}

/** One file the tab's player fetched for the clicked video (SITE-MEDIA-SPEC §2.9). */
export interface PlayedFile {
  /** "r1".."r16". */
  id: string;
  /** The whole file's address (range-in-query removed). */
  url: string;
  /** Exact total, 0 = unknown. */
  size: number;
  kind: 'video' | 'audio' | '';
  /** Base64 of the file's first bytes (at most 256 KiB). */
  head: string;
  /** The head needed the tab: Downex fetches the file through this browser from the start. */
  browser: boolean;
}

/** The clicked `<video>` and the files its tab played (SITE-MEDIA-SPEC §2.9). */
export interface PlayedSourceFields {
  resources: PlayedFile[];
  /** 0 = unknown. */
  durationMs: number;
  videoWidth: number;
  videoHeight: number;
  /** 1 yes, 0 unknown, -1 no. */
  hasAudio: number;
  /** The frame's other `<video>` elements' durations (ms), at most 8. */
  otherDurationsMs?: number[];
}

/** Describes the clicked video from its played files (no request is made by Downex). */
export interface ProbePlayedFields {
  /** The frame's address, a hint. */
  pageUrl: string;
  played: PlayedSourceFields;
  deadlineMs?: number;
}

export interface ProbePlayedParams extends ProbePlayedFields {
  type: 'probePlayed';
}

/** Adds the clicked video from its played files; empty tracks ask for Downex's default pick. */
export interface AddPlayedFields extends ProbePlayedFields {
  videoTrack?: string;
  audioTrack?: string;
  title?: string;
}

export interface AddPlayedParams extends AddPlayedFields {
  type: 'addPlayed';
}

/**
 * A download that failed in the browser, for Downex to finish from the browser's partial file
 * (BROWSER-SPEC §6). Sent only to a relay that lists FEATURE_RESCUE. downexd finds the partial
 * next to `target` (`<target>.crdownload`, `<target>.part`, or a provisional
 * "Unconfirmed N.crdownload"), proves that the server still has the same file, and moves it
 * into its own part file.
 */
export interface RescueFields {
  /** The download's address (the original one; downexd follows redirects). */
  url: string;
  headers: HeaderPair[];
  /** The browser's final path of the file (DownloadItem.filename), absolute. */
  target: string;
  /** The browser's count of good bytes. */
  bytesReceived: number;
  /** The browser's total; -1 = unknown. */
  totalBytes?: number;
  /** When the download started, Unix ms. */
  startTime?: number;
  /** The original response's validators, when the extension saw them. */
  etag?: string;
  lastModified?: string;
  /** The page the download was started from (http or https, no user name). */
  pageUrl?: string;
  /** Stored paused: the extension erases its own entry first, then sends `resume`. */
  startPaused: boolean;
  /** Milliseconds the relay has; it drops a rescue it cannot finish by then. */
  deadlineMs?: number;
}

export interface RescueParams extends RescueFields {
  type: 'rescue';
}

/**
 * The first start of a download a `rescue` stored paused (BROWSER-SPEC §6). downexd refuses any
 * other download, and this one after its first start or 10 minutes.
 */
export interface ResumeParams {
  type: 'resume';
  downloadId: number | string;
}

/** Asks Downex for a request its downloads' runs want the browser to make (SITE-MEDIA-SPEC §2.8). */
export interface FetchTakeParams {
  type: 'fetchTake';
  /** The fetch tickets this browser holds: 1 to MAX_FETCH_TICKETS. */
  tickets: string[];
  /** How long Downex may wait for one, 0 to 25,000 ms. */
  waitMs?: number;
}

/** The head of the browser's answer to a fetch. */
export interface FetchHead {
  status: number;
  contentType?: string;
  contentRange?: string;
  /** -1 or missing = unknown. */
  contentLength?: number;
}

/** What the browser received for a fetch, in order: the head with seq 0, then the body. */
export interface FetchPutParams {
  type: 'fetchPut';
  ticket: string;
  fetchId: string;
  seq: number;
  head?: FetchHead;
  /** Base64 of at most MAX_FETCH_PIECE bytes. */
  data?: string;
  end?: boolean;
  /** The fetch failed in the browser (`no_tab`: no tab of the site is open), at most 256 bytes. */
  error?: string;
}

export interface StatusParams {
  type: 'status';
}

export interface PingParams {
  type: 'ping';
}

export type RequestParams =
  | HelloParams
  | AddParams
  | AddMediaParams
  | ProbeStreamParams
  | ProbeVideoParams
  | AddVideoParams
  | ProbePlayedParams
  | AddPlayedParams
  | RescueParams
  | ResumeParams
  | FetchTakeParams
  | FetchPutParams
  | StatusParams
  | PingParams;
export type RequestType = RequestParams['type'];
export type ParamsFor<T extends RequestType> = Extract<RequestParams, { type: T }>;
export type WireRequest = RequestParams & { id: string };

export interface WelcomeResult {
  type: 'welcome';
  protocol: number;
  daemonVersion: string;
  /** What the relay serves beyond the first protocol-1 requests (`video`); [] for older relays. */
  features: string[];
}

export interface AddedResult {
  type: 'added';
  /** Daemon download ID as sent by the relay (int64 may arrive as number or string). */
  downloadId: number | string;
  /**
   * The download waits for the user's confirmation in Downex (`add`, `addMedia`, `addVideo`);
   * missing from an older relay: unknown.
   */
  awaiting?: boolean;
  /**
   * Downex checks first whether this link continues one of its downloads whose link expired
   * (`add`; DESKTOP-SPEC §10); missing from an older relay: unknown.
   */
  renewCheck?: boolean;
  /**
   * The download's fetch ticket (`addVideo` to a relay that lists FEATURE_BROWSER_FETCH,
   * SITE-MEDIA-SPEC §2.8): 32 lowercase hex characters; missing when Downex made none.
   */
  fetchTicket?: string;
  /**
   * Set by the extension, never by the relay: the domain whose sign-in this add carried, after its
   * anonymous request was answered `auth_required` (SITE-MEDIA-SPEC §6.3).
   */
  signedIn?: string;
}

/** Most tracks a videoInfo may list (SITE-MEDIA-SPEC §3.5). */
export const MAX_VIDEO_TRACKS = 200;

export type VideoTrackKind = 'video' | 'audio' | 'muxed';

/** One offered track of a page's video; `size` 0 means unknown. */
export interface VideoTrack {
  id: string;
  kind: VideoTrackKind;
  /** The site's file type: "mp4" or "m4a" ("" = unknown). */
  ext: string;
  codecs: string;
  width: number;
  height: number;
  frameRate: number;
  /** bits/s. */
  bandwidth: number;
  /** Bytes as the site declares them; 0 = unknown. */
  size: number;
  /** `size` is the site's estimate. */
  sizeApprox: boolean;
  language: string;
  /**
   * The standard quality name ("4K (2160p)", "1080p (Full HD) 60fps", "480p"; an older downexd:
   * "1080p60", "1920x1080"), or an audio track's note ("English original").
   */
  name: string;
}

/** Answer to probeVideo: one MP4 quality per class best first (standard names), then the MP4 audio tracks. */
export interface VideoInfoResult {
  type: 'videoInfo';
  /** Display name ("YouTube"). */
  site: string;
  title: string;
  durationMs: number;
  /** "http", or "hls"/"dash" for a page offered through a manifest. */
  format: string;
  /** Always "mp4": every video is saved as one .mp4. */
  container: string;
  /** Downex's default pick ("" = none). */
  selectedVideo: string;
  selectedAudio: string;
  tracks: VideoTrack[];
  /** The video's chapters (a relay that lists FEATURE_CLIPS; never for streams), start order. */
  chapters?: VideoChapter[];
  /**
   * A page with several videos (a relay that lists FEATURE_ITEMS, SITE-MEDIA-SPEC §3.12): the item
   * this answer describes, the page's videos in its order, and how many other items it has
   * (photos, links, videos past 20), which are not downloaded.
   */
  entryId?: string;
  items?: VideoItem[];
  itemsLeftOut?: number;
  /**
   * Set by the extension, never by the relay: the domain whose sign-in this answer needed (its
   * anonymous request was answered `auth_required`, SITE-MEDIA-SPEC §6.3).
   */
  signedIn?: string;
  /**
   * The page's video servers answer only web browsers: this browser fetches the video for Downex
   * (SITE-MEDIA-SPEC §2.8; a relay that lists FEATURE_BROWSER_FETCH).
   */
  browserFetch?: boolean;
}

/** One video of a page with several (SITE-MEDIA-SPEC §3.12). */
export interface VideoItem {
  /** The site's id of it: its identity, never its position. */
  id: string;
  title: string;
  /** 0 = unknown. */
  durationMs: number;
  /** An https image of the page's ("" = none); shown only in the panel of that page's frame. */
  thumbnail: string;
}

/** Most items a videoInfo may list (SITE-MEDIA-SPEC §3.12). */
export const MAX_VIDEO_ITEMS = 20;

/** At most this many chapters of a videoInfo are read (MEDIA-SPEC §8 C14). */
export const MAX_VIDEO_CHAPTERS = 512;

/** Answer to rescue: the new download and the bytes it kept from the browser's partial file. */
export interface RescuedResult {
  type: 'rescued';
  downloadId: number | string;
  keptBytes: number;
}

/** Answer to resume. */
export interface ResumedResult {
  type: 'resumed';
  downloadId: number | string;
}

/** Polite mode's state as the relay names it (docs/POLITE-SPEC.md §8). */
export type PoliteState = 'off' | 'idle' | 'watching' | 'slowed' | 'measuring' | 'no_signal' | 'unavailable';

const POLITE_STATES: readonly PoliteState[] = ['off', 'idle', 'watching', 'slowed', 'measuring', 'no_signal', 'unavailable'];

export interface StatusResult {
  type: 'status';
  running: boolean;
  active: number;
  queued: number;
  /** Polite mode's state; absent from an older relay or daemon. */
  polite?: PoliteState;
  /** Polite mode's cap in bytes per second, only while it slows downloads. */
  politeRate?: number;
}

export interface PongResult {
  type: 'pong';
}

/** One request a download's run wants the browser to make (SITE-MEDIA-SPEC §2.8). */
export interface BrowserFetchTask {
  ticket: string;
  fetchId: string;
  /** The format's address (the extension may use its player's address of the same resource). */
  url: string;
  start: number;
  /** -1 = to the end. */
  end: number;
  /** The resource's exact size, 0 = unknown. */
  total: number;
}

/** Answer to fetchTake: a request to make (none came within the wait) and the tickets Downex no longer knows. */
export interface FetchTaskResult {
  type: 'fetchTask';
  fetch?: BrowserFetchTask;
  ended: string[];
}

/** Answer to fetchPut: `stop` = abort the fetch. */
export interface FetchAckResult {
  type: 'fetchAck';
  stop: boolean;
}

/** At most this many tickets go in a fetchTake, and a fetchPut carries at most this many bytes. */
export const MAX_FETCH_TICKETS = 64;
export const MAX_FETCH_PIECE = 256 * 1024;

/** A fetch ticket's syntax: 32 lowercase hex characters. */
export function isFetchTicket(v: unknown): v is string {
  return typeof v === 'string' && /^[0-9a-f]{32}$/.test(v);
}

export interface ResultFor {
  hello: WelcomeResult;
  add: AddedResult;
  addMedia: AddedResult;
  probeStream: VideoInfoResult;
  probeVideo: VideoInfoResult;
  addVideo: AddedResult;
  probePlayed: VideoInfoResult;
  addPlayed: AddedResult;
  rescue: RescuedResult;
  resume: ResumedResult;
  fetchTake: FetchTaskResult;
  fetchPut: FetchAckResult;
  status: StatusResult;
  ping: PongResult;
}

export type SuccessResult = ResultFor[RequestType];

const EXPECTED_RESULT: { [K in RequestType]: ResultFor[K]['type'] } = {
  hello: 'welcome',
  add: 'added',
  addMedia: 'added',
  probeStream: 'videoInfo',
  probeVideo: 'videoInfo',
  addVideo: 'added',
  probePlayed: 'videoInfo',
  addPlayed: 'added',
  rescue: 'rescued',
  resume: 'resumed',
  fetchTake: 'fetchTask',
  fetchPut: 'fetchAck',
  status: 'status',
  ping: 'pong',
};

/** Codes sent by the relay. */
export type WireErrorCode = 'invalid_request' | 'daemon_unavailable' | 'rejected' | 'internal';
/**
 * Codes produced locally by the client. `incompatible`: the relay speaks another protocol;
 * `outdated_downex`: it does not serve video requests; `unsupported_page`: not a video page.
 */
export type LocalErrorCode =
  | 'timeout'
  | 'disconnected'
  | 'too_large'
  | 'protocol'
  | 'unsupported'
  | 'incompatible'
  | 'outdated_downex'
  | 'unsupported_page';

/** Why a relay whose welcome names another protocol version is not used. */
export function incompatibleRelayMessage(relayProtocol: number): string {
  return `the Downex relay speaks protocol ${relayProtocol}, this extension speaks protocol ${PROTOCOL_VERSION}`;
}

export interface ProtocolError {
  /** Wire or local code; unknown relay codes are preserved verbatim. */
  code: WireErrorCode | LocalErrorCode | (string & {});
  message: string;
  /** The daemon's stable error code (`tools_installing`, `auth_required`, …), when it sent one. */
  reason?: string;
  /** Set by the extension on a page video's `auth_required`: what happened with the sign-in (§6.3). */
  signIn?: SignInNote;
}

/**
 * What became of the user's sign-in for a page video Downex answered `auth_required` (SITE-MEDIA-SPEC
 * §6.3): `used` (sent, and the site still refused), `none` (the browser holds no cookie of the site),
 * `off` (the option "Use my sign-in for videos that need it" is off), `too-large` (more than 16 KiB).
 */
export interface SignInNote {
  domain: string;
  state: 'used' | 'none' | 'off' | 'too-large';
}

export type Outcome<T> = { ok: true; value: T } | { ok: false; error: ProtocolError };

export function failure(
  code: ProtocolError['code'],
  message: string,
  reason?: string,
): { ok: false; error: ProtocolError } {
  return { ok: false, error: reason === undefined ? { code, message } : { code, message, reason } };
}

/** The relay forwards the daemon's code as `reason` only in this form (at most 64 of [a-z_]). */
export const REASON_RE = /^[a-z_]{1,64}$/;

/**
 * The daemon's code of a refusal (SITE-MEDIA-SPEC §3.2): the relay's `reason`, else the
 * "<code>: " a `rejected` message starts with (an older relay); else the error's own code.
 */
export function daemonCode(e: ProtocolError): string {
  if (e.reason) return e.reason;
  if (e.code === 'rejected') {
    const lead = /^([a-z_]{1,64}): /.exec(e.message)?.[1];
    if (lead) return lead;
  }
  return e.code;
}

/** A message without the "<code>: " prefix the daemon puts before it. */
export function daemonDetail(e: ProtocolError): string {
  const code = daemonCode(e);
  return e.message.startsWith(`${code}: `) ? e.message.slice(code.length + 2) : e.message;
}

/**
 * A `daemon_unavailable` the relay sent because its call to a running downexd passed the
 * request's deadline ("downexd is not reachable: … deadline exceeded", "downexd did not answer
 * in time"), not because downexd could not be reached or started. An `addMedia` answers after
 * the daemon read the manifests, so this is usually a slow stream server.
 */
export function isRelayDeadline(e: ProtocolError): boolean {
  if (e.code !== 'daemon_unavailable') return false;
  if (e.message === 'downexd did not answer in time') return true;
  return e.message.startsWith('downexd is not reachable: ') && /deadline exceeded/i.test(e.message);
}

export interface ParsedResponse {
  id: string;
  outcome: Outcome<SuccessResult>;
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

/**
 * Polite mode's optional status fields: a state this extension knows (anything else is dropped,
 * as from an older relay), and the cap only while slowed.
 */
function parsePolite(m: Record<string, unknown>): Pick<StatusResult, 'polite' | 'politeRate'> {
  const state = POLITE_STATES.find((s) => s === m.polite);
  if (state === undefined) return {};
  return state === 'slowed' && isCount(m.politeRate) ? { polite: state, politeRate: m.politeRate } : { polite: state };
}

function isCount(v: unknown): v is number {
  return typeof v === 'number' && Number.isSafeInteger(v) && v >= 0;
}

function isText(v: unknown, max: number): v is string {
  return typeof v === 'string' && v.length <= max;
}

/** The welcome's features: at most MAX_FEATURES strings of at most MAX_FEATURE_CHARS. */
export function parseFeatures(v: unknown): string[] {
  if (!Array.isArray(v)) return [];
  return v
    .slice(0, MAX_FEATURES)
    .filter((f): f is string => typeof f === 'string' && f !== '' && f.length <= MAX_FEATURE_CHARS);
}

const TRACK_KINDS: ReadonlySet<string> = new Set(['video', 'audio', 'muxed']);

/**
 * A track of a videoInfo; undefined when a field has the wrong type or is out of bounds, null
 * for a well-formed track of another kind (left out, e.g. subtitles of a manifest).
 */
function parseVideoTrack(v: unknown): VideoTrack | null | undefined {
  if (!isRecord(v)) return undefined;
  const { id, kind, ext, codecs, width, height, frameRate, bandwidth, size, sizeApprox, language, name } = v;
  if (!isText(id, 1024) || id === '' || !isText(kind, 32) || kind === '') return undefined;
  if (!isText(ext, 16) || !isText(codecs, 256) || !isText(language, 64) || !isText(name, 256)) return undefined;
  if (!isCount(width) || !isCount(height) || !isCount(bandwidth) || !isCount(size)) return undefined;
  if (width > 65_535 || height > 65_535) return undefined;
  if (typeof frameRate !== 'number' || !Number.isFinite(frameRate) || frameRate < 0 || frameRate > 1000) return undefined;
  if (typeof sizeApprox !== 'boolean') return undefined;
  if (!TRACK_KINDS.has(kind)) return null;
  return {
    id,
    kind: kind as VideoTrackKind,
    ext,
    codecs,
    width,
    height,
    frameRate,
    bandwidth,
    size,
    sizeApprox,
    language,
    name,
  };
}

/** A videoInfo with every field of the right type and within bounds; undefined otherwise. */
export function parseVideoInfo(m: Record<string, unknown>): VideoInfoResult | undefined {
  const { site, title, durationMs, format, container, selectedVideo, selectedAudio, tracks } = m;
  if (!isText(site, 64) || !isText(title, 2048) || !isCount(durationMs)) return undefined;
  if (!isText(format, 16) || !isText(container, 16)) return undefined;
  if (!isText(selectedVideo, 1024) || !isText(selectedAudio, 1024)) return undefined;
  if (!Array.isArray(tracks) || tracks.length > MAX_VIDEO_TRACKS) return undefined;
  const out: VideoTrack[] = [];
  const ids = new Set<string>();
  for (const raw of tracks) {
    const t = parseVideoTrack(raw);
    if (t === undefined) return undefined;
    if (t === null) continue;
    if (ids.has(t.id)) return undefined;
    ids.add(t.id);
    out.push(t);
  }
  const info: VideoInfoResult = { type: 'videoInfo', site, title, durationMs, format, container, selectedVideo, selectedAudio, tracks: out };
  const chapters = parseChapters(m.chapters);
  if (chapters) info.chapters = chapters;
  const items = parseItems(m);
  if (items) {
    info.entryId = items.entryId;
    info.items = items.items;
    info.itemsLeftOut = items.leftOut;
  }
  if (m.browserFetch === true) info.browserFetch = true;
  return info;
}

/** An item's thumbnail as Downex checked it (SITE-MEDIA-SPEC §3.12): an https URL without user information; else "". */
function httpsImage(s: string): string {
  if (s === '') return '';
  try {
    const u = new URL(s);
    return u.protocol === 'https:' && u.username === '' && u.password === '' && u.hash === '' ? s : '';
  } catch {
    return '';
  }
}

/**
 * The items of a videoInfo (SITE-MEDIA-SPEC §3.12): undefined unless the answer has them and every
 * one is well formed (a bad list means no items, never a refused answer): ids unique, the entry id
 * one of them (or "" when there is none), thumbnails https or "".
 */
function parseItems(m: Record<string, unknown>): { entryId: string; items: VideoItem[]; leftOut: number } | undefined {
  const { entryId, items, itemsLeftOut } = m;
  if (!isText(entryId, 256) || !Array.isArray(items) || items.length > MAX_VIDEO_ITEMS || !isCount(itemsLeftOut)) return undefined;
  const out: VideoItem[] = [];
  const ids = new Set<string>();
  for (const it of items) {
    if (!isRecord(it)) return undefined;
    const { id, title, durationMs, thumbnail } = it;
    if (!isText(id, 256) || id === '' || ids.has(id) || !isText(title, 2048) || !isCount(durationMs) || !isText(thumbnail, 2048)) {
      return undefined;
    }
    ids.add(id);
    out.push({ id, title, durationMs, thumbnail: httpsImage(thumbnail) });
  }
  if (out.length > 0 && !ids.has(entryId)) return undefined;
  return { entryId, items: out, leftOut: itemsLeftOut };
}

/**
 * The chapters of a videoInfo: undefined unless every entry is well formed and they follow each
 * other in start order (a bad list means no chapters, never a refused answer).
 */
function parseChapters(v: unknown): VideoChapter[] | undefined {
  if (!Array.isArray(v) || v.length === 0 || v.length > MAX_VIDEO_CHAPTERS) return undefined;
  const out: VideoChapter[] = [];
  for (const c of v) {
    if (!isRecord(c)) return undefined;
    const { startMs, endMs, title } = c;
    if (!isClipMs(startMs) || !isClipMs(endMs) || endMs <= startMs || !isText(title, 256)) return undefined;
    const prev = out[out.length - 1];
    if (prev && startMs < prev.endMs) return undefined;
    out.push({ startMs, endMs, title });
  }
  return out;
}

/** A daemon download ID as the relay sends it (int64 may arrive as number or string). */
function isDownloadId(d: unknown): d is number | string {
  return (typeof d === 'number' && Number.isFinite(d)) || (typeof d === 'string' && d !== '');
}

function parseSuccess(m: Record<string, unknown>): SuccessResult | undefined {
  switch (m.type) {
    case 'welcome':
      return typeof m.protocol === 'number' && typeof m.daemonVersion === 'string'
        ? { type: 'welcome', protocol: m.protocol, daemonVersion: m.daemonVersion, features: parseFeatures(m.features) }
        : undefined;
    case 'added': {
      const d = m.downloadId;
      if (!((typeof d === 'number' && Number.isFinite(d)) || (typeof d === 'string' && d !== ''))) return undefined;
      if (m.awaiting !== undefined && typeof m.awaiting !== 'boolean') return undefined;
      if (m.renewCheck !== undefined && typeof m.renewCheck !== 'boolean') return undefined;
      const added: AddedResult = { type: 'added', downloadId: d };
      if (typeof m.awaiting === 'boolean') added.awaiting = m.awaiting;
      if (typeof m.renewCheck === 'boolean') added.renewCheck = m.renewCheck;
      if (m.fetchTicket !== undefined && !isFetchTicket(m.fetchTicket)) return undefined;
      if (isFetchTicket(m.fetchTicket)) added.fetchTicket = m.fetchTicket;
      return added;
    }
    case 'fetchTask': {
      if (!Array.isArray(m.ended) || m.ended.length > MAX_FETCH_TICKETS || !m.ended.every(isFetchTicket)) return undefined;
      const task: FetchTaskResult = { type: 'fetchTask', ended: m.ended };
      if (m.fetch !== undefined) {
        const f = m.fetch;
        if (!isRecord(f) || !isFetchTicket(f.ticket) || !isText(f.fetchId, 32) || f.fetchId === '') return undefined;
        if (typeof f.url !== 'string' || !/^https?:\/\//i.test(f.url) || f.url.length > 32 * 1024) return undefined;
        if (!isCount(f.start) || !isCount(f.total) || !(f.end === -1 || isCount(f.end))) return undefined;
        task.fetch = { ticket: f.ticket, fetchId: f.fetchId, url: f.url, start: f.start, end: f.end as number, total: f.total };
      }
      return task;
    }
    case 'fetchAck':
      return typeof m.stop === 'boolean' ? { type: 'fetchAck', stop: m.stop } : undefined;
    case 'videoInfo':
      return parseVideoInfo(m);
    case 'status':
      return typeof m.running === 'boolean' && isCount(m.active) && isCount(m.queued)
        ? { type: 'status', running: m.running, active: m.active, queued: m.queued, ...parsePolite(m) }
        : undefined;
    case 'rescued': {
      const d = m.downloadId;
      if (!isDownloadId(d) || !isCount(m.keptBytes)) return undefined;
      return { type: 'rescued', downloadId: d, keptBytes: m.keptBytes };
    }
    case 'resumed':
      return isDownloadId(m.downloadId) ? { type: 'resumed', downloadId: m.downloadId } : undefined;
    case 'pong':
      return { type: 'pong' };
    default:
      return undefined;
  }
}

/**
 * Parses one host → browser message. Returns undefined when the message has no usable
 * `id` (it cannot be correlated); a correlated but malformed message yields a `protocol` error.
 */
export function parseResponse(raw: unknown): ParsedResponse | undefined {
  if (!isRecord(raw) || typeof raw.id !== 'string' || raw.id === '') return undefined;
  const id = raw.id;
  if (raw.ok === false) {
    const e = raw.error;
    if (isRecord(e) && typeof e.code === 'string' && e.code !== '') {
      const message = typeof e.message === 'string' ? e.message : e.code;
      const reason = typeof e.reason === 'string' && REASON_RE.test(e.reason) ? e.reason : undefined;
      return { id, outcome: failure(e.code, message, reason) };
    }
    return { id, outcome: failure('protocol', 'error response without an error code') };
  }
  if (raw.ok === true) {
    const value = parseSuccess(raw);
    if (value) return { id, outcome: { ok: true, value } };
    return { id, outcome: failure('protocol', `malformed ${String(raw.type)} response`) };
  }
  return { id, outcome: failure('protocol', 'response without ok flag') };
}

/** Returns a generator of request IDs unique across service-worker restarts. */
export function createIdGenerator(random: () => string = randomHex): () => string {
  const session = random();
  let n = 0;
  return () => `${session}-${(++n).toString(36)}`;
}

function randomHex(): string {
  const bytes = new Uint8Array(6);
  crypto.getRandomValues(bytes);
  return Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
}

export function utf8Length(s: string): number {
  return new TextEncoder().encode(s).length;
}

// ---------------------------------------------------------------------------------------
// NativeClient

export interface PortEvent<L> {
  addListener(listener: L): void;
  removeListener(listener: L): void;
}

/** The subset of runtime.Port the client uses. */
export interface NativePort {
  postMessage(message: unknown): void;
  disconnect(): void;
  onMessage: PortEvent<(message: unknown) => void>;
  onDisconnect: PortEvent<(port: never) => void>;
}

export interface Timers {
  setTimeout(callback: () => void, ms: number): unknown;
  clearTimeout(handle: unknown): void;
  now(): number;
}

/** Defers to the globals at call time so test fake timers apply. */
export const systemTimers: Timers = {
  setTimeout: (cb, ms) => globalThis.setTimeout(cb, ms),
  clearTimeout: (h) => globalThis.clearTimeout(h as ReturnType<typeof globalThis.setTimeout>),
  now: () => Date.now(),
};

export type ConnectionState = 'idle' | 'connecting' | 'connected' | 'disconnected';

export interface ConnectionStatus {
  state: ConnectionState;
  /** From the last welcome; kept while connected. */
  daemonVersion?: string;
  /** The relay's features from the last welcome; kept while connected. */
  features?: string[];
  /**
   * While connected: whether downexd is known to run (welcome with a daemonVersion, a
   * status with running, a successful add).
   */
  daemonRunning?: boolean;
  /** Why the last connection attempt or connection ended. */
  lastError?: string;
  /**
   * Set while disconnected because the relay's welcome named this protocol version instead
   * of PROTOCOL_VERSION: an incompatible relay is never sent anything but the hello.
   */
  incompatibleProtocol?: number;
  /** Epoch ms of the next reconnect attempt while disconnected. */
  nextRetryAt?: number;
  /** Epoch ms of the last state change. */
  since: number;
}

export interface NativeClientOptions {
  /** Opens the port (e.g. `() => browser.runtime.connectNative(NATIVE_HOST_NAME)`); may throw. */
  connect: () => NativePort;
  /** Hello sent on every new connection. */
  hello: () => Omit<HelloParams, 'type'>;
  /** Reads the disconnect reason (runtime.lastError / port.error) after onDisconnect. */
  disconnectReason?: (port: NativePort) => string | undefined;
  onStatus?: (status: ConnectionStatus) => void;
  timers?: Timers;
  newId?: () => string;
  requestTimeoutMs?: number;
  helloTimeoutMs?: number;
  minBackoffMs?: number;
  maxBackoffMs?: number;
}

export interface RequestOptions<R = SuccessResult> {
  timeoutMs?: number;
  /**
   * Receives an answer that arrives after the request timed out (within LATE_RESPONSE_MS),
   * e.g. an `added` for a download the browser already kept.
   */
  onLate?: (outcome: Outcome<R>) => void;
}

type LateCallback = (outcome: Outcome<SuccessResult>) => void;

/**
 * Requests sent only to a relay whose welcome lists FEATURE_VIDEO (`probeStream` also needs
 * FEATURE_STREAMS). The daemon resolves the page or reads the manifest before it answers them.
 */
const VIDEO_REQUESTS: ReadonlySet<RequestType> = new Set(['probeVideo', 'addVideo', 'probeStream']);
/** Requests whose `deadlineMs` is timed from sending: the wait for the welcome is taken off it. */
const DEADLINE_REQUESTS: ReadonlySet<RequestType> = new Set([
  'add',
  'addMedia',
  'probeVideo',
  'addVideo',
  'probeStream',
  'rescue',
  'probePlayed',
  'addPlayed',
]);
/** Requests sent only to a relay whose welcome lists FEATURE_PLAYED (SITE-MEDIA-SPEC §2.9). */
const PLAYED_REQUESTS: ReadonlySet<RequestType> = new Set(['probePlayed', 'addPlayed']);
/** Requests sent only to a relay whose welcome lists FEATURE_RESCUE. */
const RESCUE_REQUESTS: ReadonlySet<RequestType> = new Set(['rescue', 'resume']);

interface Pending {
  type: RequestType;
  message: WireRequest;
  timer: unknown;
  resolve: (outcome: Outcome<SuccessResult>) => void;
  onLate: LateCallback | undefined;
  /** Epoch ms the request was made. */
  createdAt: number;
  /** Posted to the relay; until then it waits for the connection's welcome. */
  sent: boolean;
}

interface Late {
  type: RequestType;
  timer: unknown;
  onLate: LateCallback;
}

/**
 * One native-messaging connection with request/response correlation by `id`, per-request
 * timeouts, a hello on every connect, and reconnects with exponential backoff (1 s → 60 s).
 * Requests made before the relay's welcome was verified wait for it (within their timeout),
 * so a relay that speaks another protocol is sent nothing but the hello.
 * Requests never reject: they resolve to an Outcome.
 */
export class NativeClient {
  readonly #opts: Required<
    Pick<NativeClientOptions, 'requestTimeoutMs' | 'helloTimeoutMs' | 'minBackoffMs' | 'maxBackoffMs'>
  > &
    NativeClientOptions;
  readonly #timers: Timers;
  readonly #newId: () => string;
  /** Requests waiting for an answer or, unsent, for the welcome; in the order they were made. */
  readonly #pending = new Map<string, Pending>();
  readonly #late = new Map<string, Late>();
  /** A status request asked because an answer left it open whether downexd runs. */
  #recheck = false;
  #port: NativePort | undefined;
  #status: ConnectionStatus;
  #backoffMs: number;
  #retryTimer: unknown;
  #stopped = true;

  constructor(options: NativeClientOptions) {
    this.#opts = {
      requestTimeoutMs: 5000,
      helloTimeoutMs: 5000,
      minBackoffMs: 1000,
      maxBackoffMs: 60_000,
      ...options,
    };
    this.#timers = options.timers ?? systemTimers;
    this.#newId = options.newId ?? createIdGenerator();
    this.#backoffMs = this.#opts.minBackoffMs;
    this.#status = { state: 'idle', since: this.#timers.now() };
  }

  get status(): ConnectionStatus {
    return { ...this.#status };
  }

  /** Connects now (if not connected) and keeps reconnecting until stop(). */
  start(): void {
    this.#stopped = false;
    this.#connect();
  }

  /** Disconnects, cancels reconnects and fails pending requests. */
  stop(): void {
    this.#stopped = true;
    this.#cancelRetry();
    const port = this.#port;
    this.#port = undefined;
    if (port) {
      try {
        port.disconnect();
      } catch {
        // already gone
      }
    }
    this.#failPending('disconnected', 'client stopped');
    this.#clearLate();
    this.#setStatus({ state: 'idle', since: this.#timers.now() });
  }

  /** True while connected and downexd is known to run: only then is capture attempted. */
  get daemonReady(): boolean {
    return this.#status.state === 'connected' && this.#status.daemonRunning === true;
  }

  /**
   * Sends a request: at once while connected, else once the relay's welcome was verified.
   * `timeoutMs` runs from this call, so it also bounds that wait.
   */
  request<T extends RequestType>(
    params: ParamsFor<T>,
    options: RequestOptions<ResultFor[T]> = {},
  ): Promise<Outcome<ResultFor[T]>> {
    if (this.#stopped) this.#stopped = false;
    if (!this.#port) this.#connect();
    if (!this.#port) {
      return Promise.resolve(
        failure('disconnected', this.#status.lastError ?? 'native host is not connected'),
      );
    }
    const timeoutMs = options.timeoutMs ?? this.#opts.requestTimeoutMs;
    const onLate = options.onLate as LateCallback | undefined;
    return new Promise<Outcome<SuccessResult>>((resolve) => {
      this.#submit(params, timeoutMs, onLate, resolve);
    }) as Promise<Outcome<ResultFor[T]>>;
  }

  add(fields: AddFields, options?: RequestOptions<AddedResult>): Promise<Outcome<AddedResult>> {
    return this.request<'add'>({ type: 'add', ...fields }, options);
  }

  /** Hands a manifest to Downex; `rejected` errors name `encrypted` / `live` streams. */
  addMedia(fields: AddMediaFields, options?: RequestOptions<AddedResult>): Promise<Outcome<AddedResult>> {
    return this.request<'addMedia'>({ type: 'addMedia', ...fields }, options);
  }

  /**
   * Lists the MP4 qualities of a manifest a page loaded (VIDEO-DETECT-SPEC §3.5); `outdated_downex`
   * when the relay does not list FEATURE_STREAMS.
   */
  probeStream(fields: ProbeStreamFields, options?: RequestOptions<VideoInfoResult>): Promise<Outcome<VideoInfoResult>> {
    return this.request<'probeStream'>({ type: 'probeStream', ...fields }, options);
  }

  /** Lists a page's MP4 qualities; `outdated_downex` when the relay does not serve videos. */
  probeVideo(fields: ProbeVideoFields, options?: RequestOptions<VideoInfoResult>): Promise<Outcome<VideoInfoResult>> {
    return this.request<'probeVideo'>({ type: 'probeVideo', ...fields }, options);
  }

  /** Adds a page's video; `outdated_downex` when the relay does not serve videos. */
  addVideo(fields: AddVideoFields, options?: RequestOptions<AddedResult>): Promise<Outcome<AddedResult>> {
    return this.request<'addVideo'>({ type: 'addVideo', ...fields }, options);
  }

  /**
   * Describes the clicked video from the files its tab played (SITE-MEDIA-SPEC §2.9);
   * `outdated_downex` when the relay does not list FEATURE_PLAYED.
   */
  probePlayed(fields: ProbePlayedFields, options?: RequestOptions<VideoInfoResult>): Promise<Outcome<VideoInfoResult>> {
    return this.request<'probePlayed'>({ type: 'probePlayed', ...fields }, options);
  }

  /** Adds the clicked video from the files its tab played; `outdated_downex` without FEATURE_PLAYED. */
  addPlayed(fields: AddPlayedFields, options?: RequestOptions<AddedResult>): Promise<Outcome<AddedResult>> {
    return this.request<'addPlayed'>({ type: 'addPlayed', ...fields }, options);
  }

  /**
   * Hands a download that failed in the browser to Downex (BROWSER-SPEC §6); `outdated_downex`
   * when the relay does not list FEATURE_RESCUE.
   */
  rescue(fields: RescueFields, options?: RequestOptions<RescuedResult>): Promise<Outcome<RescuedResult>> {
    return this.request<'rescue'>({ type: 'rescue', ...fields }, options);
  }

  /**
   * Asks for a request of the downloads whose tickets this browser holds (SITE-MEDIA-SPEC §2.8);
   * `outdated_downex` when the relay does not list FEATURE_BROWSER_FETCH.
   */
  fetchTake(tickets: string[], waitMs: number, options?: RequestOptions<FetchTaskResult>): Promise<Outcome<FetchTaskResult>> {
    return this.request<'fetchTake'>({ type: 'fetchTake', tickets, waitMs }, options);
  }

  /** Gives Downex what the browser received for a fetch; `stop` = abort it. */
  fetchPut(fields: Omit<FetchPutParams, 'type'>, options?: RequestOptions<FetchAckResult>): Promise<Outcome<FetchAckResult>> {
    return this.request<'fetchPut'>({ type: 'fetchPut', ...fields }, options);
  }

  /** The first start of a download `rescue` stored paused. */
  resume(downloadId: number | string, options?: RequestOptions<ResumedResult>): Promise<Outcome<ResumedResult>> {
    return this.request<'resume'>({ type: 'resume', downloadId }, options);
  }

  queryStatus(options?: RequestOptions<StatusResult>): Promise<Outcome<StatusResult>> {
    return this.request<'status'>({ type: 'status' }, options);
  }

  ping(options?: RequestOptions<PongResult>): Promise<Outcome<PongResult>> {
    return this.request<'ping'>({ type: 'ping' }, options);
  }

  /** Registers a request and posts it now if it is the hello or the welcome was verified. */
  #submit(
    params: RequestParams,
    timeoutMs: number,
    onLate: LateCallback | undefined,
    resolve: (outcome: Outcome<SuccessResult>) => void,
  ): void {
    const id = this.#newId();
    const message: WireRequest = { ...params, id };
    let json: string;
    try {
      json = JSON.stringify(message);
    } catch (e) {
      resolve(failure('invalid_request', errorMessage(e)));
      return;
    }
    if (utf8Length(json) > MAX_OUTBOUND_MESSAGE_BYTES) {
      resolve(failure('too_large', `request exceeds the ${MAX_OUTBOUND_MESSAGE_BYTES.toLocaleString('en-US')}-byte native-messaging limit`));
      return;
    }
    const timer = this.#timers.setTimeout(() => this.#expire(id, timeoutMs), timeoutMs);
    this.#pending.set(id, {
      type: params.type,
      message,
      timer,
      resolve,
      onLate,
      createdAt: this.#timers.now(),
      sent: false,
    });
    if (params.type === 'hello' || this.#status.state === 'connected') this.#post(id);
  }

  #post(id: string): void {
    const p = this.#pending.get(id);
    if (!p || p.sent) return;
    const port = this.#port;
    if (!port) {
      this.#settle(id, failure('disconnected', this.#status.lastError ?? 'native host is not connected'));
      return;
    }
    const m = p.message;
    const features = this.#status.features ?? [];
    if (m.type === 'probeStream' && !features.includes(FEATURE_STREAMS)) {
      // An older relay would refuse the request type: it is never sent one.
      this.#settle(id, failure('outdated_downex', OUTDATED_STREAMS_TEXT));
      return;
    }
    if (VIDEO_REQUESTS.has(m.type) && !features.includes(FEATURE_VIDEO)) {
      // An older relay would refuse the request type: it is never sent one.
      this.#settle(id, failure('outdated_downex', OUTDATED_DOWNEX_TEXT));
      return;
    }
    if (PLAYED_REQUESTS.has(m.type) && !features.includes(FEATURE_PLAYED)) {
      // An older relay would refuse the request type: it is never sent one.
      this.#settle(id, failure('outdated_downex', OUTDATED_PLAYED_TEXT));
      return;
    }
    if (RESCUE_REQUESTS.has(m.type) && !features.includes(FEATURE_RESCUE)) {
      // An older relay would refuse the request type: it is never sent one.
      this.#settle(id, failure('outdated_downex', OUTDATED_RESCUE_TEXT));
      return;
    }
    if (m.type === 'probeVideo' && m.prefetch && !features.includes(FEATURE_PREFETCH)) {
      // An older relay would forward a look-ahead as a user's probe: it is never sent one.
      this.#settle(id, failure('unsupported', 'this Downex does not prepare video qualities ahead'));
      return;
    }
    if ((m.type === 'probeVideo' || m.type === 'addVideo') && m.entryId && !features.includes(FEATURE_ITEMS)) {
      // An older relay would drop the item and Downex would take another video: it is never sent one.
      this.#settle(id, failure('outdated_downex', OUTDATED_ITEMS_TEXT));
      return;
    }
    if ((m.type === 'fetchTake' || m.type === 'fetchPut') && !features.includes(FEATURE_BROWSER_FETCH)) {
      // An older relay would refuse the request type: it is never sent one.
      this.#settle(id, failure('outdated_downex', 'this Downex does not take videos fetched by the browser'));
      return;
    }
    if (m.type === 'addVideo' && m.browserFetch !== undefined && !features.includes(FEATURE_BROWSER_FETCH)) {
      // An older relay ignores it all the same; no ticket comes back.
      delete m.browserFetch;
    }
    if ((m.type === 'probeVideo' || m.type === 'addVideo') && m.cookieDomain !== undefined && !features.includes(FEATURE_SIGN_IN)) {
      // An older relay would drop it all the same; Downex then keeps the cookies to the page's host.
      delete m.cookieDomain;
    }
    if ((m.type === 'addMedia' || m.type === 'addVideo') && m.clipStartMs !== undefined && !features.includes(FEATURE_CLIPS)) {
      // An older relay would drop the part and save the whole video: it is never sent one.
      this.#settle(id, failure('outdated_downex', CLIP_TEXTS.outdated));
      return;
    }
    if (m.type === 'addMedia' && !features.includes(FEATURE_STREAMS)) {
      // An older relay ignores the tracks (Downex's default pick): they are not sent at all.
      delete m.videoTrack;
      delete m.audioTrack;
    }
    if (DEADLINE_REQUESTS.has(m.type) && 'deadlineMs' in m && m.deadlineMs !== undefined) {
      // deadlineMs is timed from sending: the wait for the welcome is taken off it.
      const left = Math.floor(m.deadlineMs - (this.#timers.now() - p.createdAt));
      if (left <= 0) {
        this.#settle(id, failure('timeout', `the native host was not ready within the ${m.deadlineMs} ms deadline`));
        return;
      }
      m.deadlineMs = left;
    }
    p.sent = true;
    try {
      port.postMessage(m);
    } catch (e) {
      this.#settle(id, failure('disconnected', errorMessage(e)));
      this.#portDown(port, errorMessage(e));
    }
  }

  #expire(id: string, timeoutMs: number): void {
    const p = this.#pending.get(id);
    if (!p) return;
    this.#pending.delete(id);
    if (!p.sent) {
      p.resolve(failure('timeout', `the native host was not ready within ${timeoutMs} ms; the ${p.type} was not sent`));
      return;
    }
    if (p.onLate) this.#awaitLate(id, p.type, p.onLate);
    const outcome = failure('timeout', `no ${p.type} response within ${timeoutMs} ms`);
    this.#observe(p.type, outcome);
    p.resolve(outcome);
  }

  #settle(id: string, outcome: Outcome<SuccessResult>): void {
    const p = this.#pending.get(id);
    if (!p) return;
    this.#pending.delete(id);
    this.#timers.clearTimeout(p.timer);
    if (p.sent) this.#observe(p.type, outcome);
    p.resolve(outcome);
  }

  /** Keeps a timed-out request's late-answer callback for LATE_RESPONSE_MS. */
  #awaitLate(id: string, type: RequestType, onLate: LateCallback): void {
    const timer = this.#timers.setTimeout(() => this.#late.delete(id), LATE_RESPONSE_MS);
    this.#late.set(id, { type, timer, onLate });
  }

  #clearLate(): void {
    for (const l of this.#late.values()) this.#timers.clearTimeout(l.timer);
    this.#late.clear();
  }

  /** Tracks whether downexd runs from the answers that say so. */
  #observe(type: RequestType, outcome: Outcome<SuccessResult>): void {
    if (this.#status.state !== 'connected') return;
    let running: boolean | undefined;
    if (outcome.ok) {
      if (outcome.value.type === 'status') running = outcome.value.running;
      // Only a running downexd adds or describes a video (the welcome may have come while
      // it was still starting).
      else if (outcome.value.type === 'added' || outcome.value.type === 'videoInfo') running = true;
      else if (outcome.value.type === 'rescued' || outcome.value.type === 'resumed') running = true;
    } else if (outcome.error.code === 'rejected') {
      // Only downexd refuses a request: the relay's own refusals are invalid_request.
      running = true;
    } else if (outcome.error.code === 'daemon_unavailable') {
      if (type === 'addMedia' || type === 'rescue' || VIDEO_REQUESTS.has(type) || PLAYED_REQUESTS.has(type)) {
        // The daemon reads the manifests (or resolves the page) before it answers, so the
        // relay's deadline may have passed while it runs: the status says whether it does.
        this.#recheckDaemon();
        return;
      }
      running = false;
    } else if (type === 'add' && outcome.error.code === 'timeout') {
      // Possibly a daemon autostart: not known to run until a status or an add says so.
      running = false;
    }
    if (running === undefined || running === this.#status.daemonRunning) return;
    this.#setStatus({ ...this.#status, daemonRunning: running });
  }

  /** Asks for the daemon status once at a time; its answer is observed like any other. */
  #recheckDaemon(): void {
    if (this.#recheck || this.#status.state !== 'connected') return;
    this.#recheck = true;
    this.#submit({ type: 'status' }, this.#opts.requestTimeoutMs, undefined, () => {
      this.#recheck = false;
    });
  }

  #expected(type: RequestType, outcome: Outcome<SuccessResult>): Outcome<SuccessResult> {
    if (outcome.ok && outcome.value.type !== EXPECTED_RESULT[type]) {
      return failure('protocol', `expected ${EXPECTED_RESULT[type]} response, got ${outcome.value.type}`);
    }
    return outcome;
  }

  #onMessage(raw: unknown): void {
    const parsed = parseResponse(raw);
    if (!parsed) return;
    const p = this.#pending.get(parsed.id);
    if (p) {
      // An answer to a request that was never sent is not one.
      if (p.sent) this.#settle(parsed.id, this.#expected(p.type, parsed.outcome));
      return;
    }
    const late = this.#late.get(parsed.id);
    if (!late) return; // unknown, or later than LATE_RESPONSE_MS
    this.#late.delete(parsed.id);
    this.#timers.clearTimeout(late.timer);
    const outcome = this.#expected(late.type, parsed.outcome);
    this.#observe(late.type, outcome);
    try {
      late.onLate(outcome);
    } catch {
      // a late-answer callback must not break the connection
    }
  }

  #connect(): void {
    if (this.#port || this.#stopped) return;
    this.#cancelRetry();
    this.#setStatus({ state: 'connecting', since: this.#timers.now() });
    let port: NativePort;
    try {
      port = this.#opts.connect();
    } catch (e) {
      this.#scheduleRetry(errorMessage(e));
      return;
    }
    this.#port = port;
    port.onMessage.addListener((m: unknown) => {
      if (this.#port === port) this.#onMessage(m);
    });
    port.onDisconnect.addListener(() => {
      this.#portDown(port, this.#opts.disconnectReason?.(port) ?? 'native host disconnected');
    });
    // Answered synchronously from #settle, so requests waiting for the welcome go out with it.
    this.#submit({ type: 'hello', ...this.#opts.hello() }, this.#opts.helloTimeoutMs, undefined, (outcome) =>
      this.#onHello(port, outcome),
    );
  }

  #onHello(port: NativePort, outcome: Outcome<SuccessResult>): void {
    if (this.#port !== port) return;
    if (!outcome.ok) {
      this.#dropPort(port, `hello failed: ${outcome.error.code}: ${outcome.error.message}`);
      return;
    }
    if (outcome.value.type !== 'welcome') return; // #expected turned other types into errors
    const { protocol, daemonVersion, features } = outcome.value;
    if (protocol !== PROTOCOL_VERSION) {
      this.#dropPort(port, incompatibleRelayMessage(protocol), protocol);
      return;
    }
    this.#backoffMs = this.#opts.minBackoffMs;
    // The relay answers welcome with an empty daemonVersion when downexd is not running.
    this.#setStatus({
      state: 'connected',
      ...(daemonVersion ? { daemonVersion } : {}),
      features,
      daemonRunning: daemonVersion !== '',
      since: this.#timers.now(),
    });
    // Requests made while connecting go out in the order they were made.
    for (const [id, p] of [...this.#pending]) {
      if (!p.sent) this.#post(id);
    }
  }

  /** Closes our side of `port` and handles it as down. */
  #dropPort(port: NativePort, reason: string, incompatibleProtocol?: number): void {
    try {
      port.disconnect();
    } catch {
      // already gone
    }
    this.#portDown(port, reason, incompatibleProtocol);
  }

  #portDown(port: NativePort, reason: string, incompatibleProtocol?: number): void {
    if (this.#port !== port) return;
    this.#port = undefined;
    this.#failPending(incompatibleProtocol === undefined ? 'disconnected' : 'incompatible', reason);
    this.#clearLate();
    this.#scheduleRetry(reason, incompatibleProtocol);
  }

  #failPending(code: ProtocolError['code'], message: string): void {
    for (const id of [...this.#pending.keys()]) this.#settle(id, failure(code, message));
  }

  #scheduleRetry(reason: string, incompatibleProtocol?: number): void {
    const incompatible = incompatibleProtocol === undefined ? {} : { incompatibleProtocol };
    if (this.#stopped) {
      this.#setStatus({ state: 'idle', lastError: reason, ...incompatible, since: this.#timers.now() });
      return;
    }
    const delay = this.#backoffMs;
    this.#backoffMs = Math.min(this.#backoffMs * 2, this.#opts.maxBackoffMs);
    const now = this.#timers.now();
    this.#cancelRetry();
    this.#retryTimer = this.#timers.setTimeout(() => {
      this.#retryTimer = undefined;
      this.#connect();
    }, delay);
    this.#setStatus({
      state: 'disconnected',
      lastError: reason,
      ...incompatible,
      nextRetryAt: now + delay,
      since: now,
    });
  }

  #cancelRetry(): void {
    if (this.#retryTimer !== undefined) {
      this.#timers.clearTimeout(this.#retryTimer);
      this.#retryTimer = undefined;
    }
  }

  #setStatus(status: ConnectionStatus): void {
    this.#status = status;
    this.#opts.onStatus?.({ ...status });
  }
}

export function errorMessage(e: unknown): string {
  if (e instanceof Error) return e.message;
  if (typeof e === 'string') return e;
  return String(e);
}
