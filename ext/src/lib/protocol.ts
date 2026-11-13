// Native-messaging protocol between the extension and downex-nmhost (BROWSER-SPEC §2).
// Pure module: the browser port is injected, so everything here runs under vitest.

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
  /** Only `Cookie` (the user's opt-in) and `Referer` (the embedding page) mean something. */
  headers?: HeaderPair[];
  deadlineMs?: number;
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
}

export interface AddVideoParams extends AddVideoFields {
  type: 'addVideo';
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
}

export interface StatusResult {
  type: 'status';
  running: boolean;
  active: number;
  queued: number;
}

export interface PongResult {
  type: 'pong';
}

export interface ResultFor {
  hello: WelcomeResult;
  add: AddedResult;
  addMedia: AddedResult;
  probeStream: VideoInfoResult;
  probeVideo: VideoInfoResult;
  addVideo: AddedResult;
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
  return { type: 'videoInfo', site, title, durationMs, format, container, selectedVideo, selectedAudio, tracks: out };
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
      if (m.awaiting === undefined) return { type: 'added', downloadId: d };
      return typeof m.awaiting === 'boolean' ? { type: 'added', downloadId: d, awaiting: m.awaiting } : undefined;
    }
    case 'videoInfo':
      return parseVideoInfo(m);
    case 'status':
      return typeof m.running === 'boolean' && isCount(m.active) && isCount(m.queued)
        ? { type: 'status', running: m.running, active: m.active, queued: m.queued }
        : undefined;
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
const DEADLINE_REQUESTS: ReadonlySet<RequestType> = new Set(['add', 'addMedia', 'probeVideo', 'addVideo', 'probeStream']);

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
    if (m.type === 'probeVideo' && m.prefetch && !features.includes(FEATURE_PREFETCH)) {
      // An older relay would forward a look-ahead as a user's probe: it is never sent one.
      this.#settle(id, failure('unsupported', 'this Downex does not prepare video qualities ahead'));
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
    } else if (outcome.error.code === 'rejected') {
      // Only downexd refuses a request: the relay's own refusals are invalid_request.
      running = true;
    } else if (outcome.error.code === 'daemon_unavailable') {
      if (type === 'addMedia' || VIDEO_REQUESTS.has(type)) {
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
