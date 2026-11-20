// Builds and sends one `addMedia` request (BROWSER-SPEC §2, MEDIA-SPEC §6.1) for a stream the
// popup or an overlay panel lists, with the tracks the user chose (VIDEO-DETECT-SPEC §3.5), and
// records it in the hand-off history. Pure: browser APIs are injected.

import type { ClipRange } from './clip';
import { type CookieLike, cookieHeader, defaultPolicyReferer, filterHeaders, hasHeader } from './context';
import type { HandoffHistory, HandoffRecord } from './history';
import { MAX_MEDIA_URL_BYTES, type MediaItem } from './media';
import {
  type AddedResult,
  type AddMediaFields,
  MAX_MEDIA_TITLE_CHARS,
  type Outcome,
  type RequestOptions,
  type Timers,
  failure,
  systemTimers,
  utf8Length,
} from './protocol';

/**
 * How long the extension waits for `added`: the daemon fetches and parses the manifest (and an
 * HLS master's variant and audio playlists) first. Sent as `deadlineMs`, so the relay drops an
 * addMedia it cannot finish in time instead of adding a download the popup reported as failed.
 */
export const MEDIA_ADD_TIMEOUT_MS = 15_000;
/** Most time spent on the cookie lookup before giving up unsent. */
export const MEDIA_PREPARE_TIMEOUT_MS = 1000;

// eslint-disable-next-line no-control-regex
const CONTROL = /[\u0000-\u001f\u007f-\u009f]+/g;

/**
 * A page title fit for `addMedia`: controls removed, whitespace collapsed, trimmed, and cut at a
 * code-point boundary to at most MAX_MEDIA_TITLE_CHARS UTF-8 bytes (so it holds whether the limit
 * is read as characters or bytes). Undefined when nothing is left.
 */
export function cleanTitle(raw: string | undefined): string | undefined {
  if (!raw) return undefined;
  const s = raw.replace(CONTROL, ' ').replace(/\s+/g, ' ').trim();
  if (!s) return undefined;
  if (utf8Length(s) <= MAX_MEDIA_TITLE_CHARS) return s;
  let out = '';
  let bytes = 0;
  for (const ch of s) {
    const n = utf8Length(ch);
    if (bytes + n > MAX_MEDIA_TITLE_CHARS) break;
    out += ch;
    bytes += n;
  }
  return out.trim() || undefined;
}

export interface MediaFieldsDeps {
  /** `cookies.getAll({url, storeId})` for the manifest URL only; used when no headers were recorded. */
  getCookies: (url: string, storeId: string | undefined) => Promise<readonly CookieLike[]>;
  userAgent: string;
  /**
   * Whether the page's credentials may go with the stream (the sign-in option, SITE-MEDIA-SPEC
   * §6.3); false: no Cookie or Authorization, recorded or read. Default true.
   */
  credentials?: () => Promise<boolean>;
}

function sendableUrl(v: string | undefined): string | undefined {
  if (!v || utf8Length(v) > MAX_MEDIA_URL_BYTES) return undefined;
  try {
    const p = new URL(v).protocol;
    return p === 'http:' || p === 'https:' ? v : undefined;
  } catch {
    return undefined;
  }
}

/**
 * The `addMedia` fields of a listed stream: the headers the page sent for the manifest when they
 * were recorded (never changed, as for `add`); otherwise the manifest URL's cookies in the
 * stream's cookie store and a Referer derived from the requesting document as the browser's
 * default policy would. A User-Agent is added when missing.
 */
export async function buildAddMediaFields(item: MediaItem, deps: MediaFieldsDeps): Promise<Outcome<AddMediaFields>> {
  const manifestUrl = sendableUrl(item.url);
  if (!manifestUrl) return failure('invalid_request', 'the stream URL cannot be handed to Downex');
  const credentials = deps.credentials ? await deps.credentials().catch(() => false) : true;
  let headers = filterHeaders(item.headers ?? []);
  if (!credentials) headers = headers.filter(([name]) => !/^(cookie|authorization)$/i.test(name));
  if (item.headers === undefined) {
    try {
      const cookies = credentials ? await deps.getCookies(manifestUrl, item.storeId) : [];
      if (cookies.length > 0) headers.push(['Cookie', cookieHeader(cookies)]);
    } catch {
      // no cookie access for this URL: hand off without cookies
    }
    const referer = defaultPolicyReferer(item.frameUrl ?? item.pageUrl, manifestUrl);
    if (referer) headers.push(['Referer', referer]);
  }
  if (deps.userAgent && !hasHeader(headers, 'user-agent')) headers.push(['User-Agent', deps.userAgent]);
  headers = filterHeaders(headers);
  const fields: AddMediaFields = { manifestUrl, headers };
  const pageUrl = sendableUrl(item.pageUrl);
  if (pageUrl) fields.pageUrl = pageUrl;
  const title = cleanTitle(item.pageTitle);
  if (title) fields.title = title;
  return { ok: true, value: fields };
}

/**
 * The `addMedia` fields of a stream, given up unsent after MEDIA_PREPARE_TIMEOUT_MS (a cookie
 * lookup that hangs). Never rejects.
 */
export function prepareAddMediaFields(item: MediaItem, deps: MediaFieldsDeps, timers: Timers = systemTimers): Promise<Outcome<AddMediaFields>> {
  return new Promise<Outcome<AddMediaFields>>((resolve) => {
    const timer = timers.setTimeout(
      () => resolve(failure('timeout', `request context not ready within ${MEDIA_PREPARE_TIMEOUT_MS} ms`)),
      MEDIA_PREPARE_TIMEOUT_MS,
    );
    buildAddMediaFields(item, deps).then(
      (r) => {
        timers.clearTimeout(timer);
        resolve(r);
      },
      (e: unknown) => {
        timers.clearTimeout(timer);
        resolve(failure('internal', e instanceof Error ? e.message : String(e)));
      },
    );
  });
}

/** The tracks of a `probeStream` answer the user chose; missing or "" = Downex's default pick. */
export interface MediaTracks {
  videoTrack?: string;
  audioTrack?: string;
  /** A part of the video (MEDIA-SPEC §8); NativeClient refuses it to a relay without `clips`. */
  clip?: ClipRange;
}

export interface AddMediaSender {
  addMedia(fields: AddMediaFields, options?: RequestOptions<AddedResult>): Promise<Outcome<AddedResult>>;
}

export interface MediaHandoffDeps extends MediaFieldsDeps {
  client: AddMediaSender;
  history?: HandoffHistory;
  timeoutMs?: number;
  now?: () => number;
  timers?: Timers;
  onError?: (error: unknown) => void;
}

/**
 * Never rejects; resolves within MEDIA_PREPARE_TIMEOUT_MS + the add timeout. The tracks reach a
 * relay that lists `streams` only (NativeClient drops them otherwise).
 */
export type MediaHandOff = (item: MediaItem, tracks?: MediaTracks) => Promise<Outcome<AddedResult>>;

export function createMediaHandOff(deps: MediaHandoffDeps): MediaHandOff {
  const now = deps.now ?? (() => Date.now());
  const timers = deps.timers ?? systemTimers;
  const timeoutMs = deps.timeoutMs ?? MEDIA_ADD_TIMEOUT_MS;

  const record = (item: MediaItem, title: string | undefined, outcome: Outcome<AddedResult>) => {
    if (!deps.history) return;
    const rec: HandoffRecord = { ts: now(), url: item.url, source: 'media', ok: outcome.ok };
    if (title) rec.title = title;
    if (outcome.ok) {
      rec.downloadId = outcome.value.downloadId;
      if (outcome.value.awaiting !== undefined) rec.awaiting = outcome.value.awaiting;
    } else {
      rec.error = outcome.error;
    }
    deps.history.add(rec).catch((e: unknown) => deps.onError?.(e));
  };

  return async (item, tracks = {}) => {
    const title = cleanTitle(item.pageTitle);
    let outcome: Outcome<AddedResult>;
    try {
      const built = await prepareAddMediaFields(item, deps, timers);
      if (!built.ok) {
        outcome = built;
      } else {
        const fields: AddMediaFields = { ...built.value, deadlineMs: timeoutMs };
        if (tracks.videoTrack) fields.videoTrack = tracks.videoTrack;
        if (tracks.audioTrack) fields.audioTrack = tracks.audioTrack;
        if (tracks.clip) {
          fields.clipStartMs = tracks.clip.startMs;
          if (tracks.clip.endMs > 0) fields.clipEndMs = tracks.clip.endMs;
        }
        outcome = await deps.client.addMedia(fields, {
          timeoutMs,
          // Unlikely (the relay drops what misses the deadline), but an added download is shown.
          onLate: (late) => {
            if (late.ok) record(item, title, late);
          },
        });
      }
    } catch (e) {
      outcome = failure('internal', e instanceof Error ? e.message : String(e));
    }
    record(item, title, outcome);
    return outcome;
  };
}

/** How long the answer to a stream's hand-off is kept for a popup opened again. */
export const MEDIA_RESULT_KEEP_MS = 5 * 60_000;

export interface MediaSendState {
  sending?: true;
  result?: Outcome<AddedResult>;
}

export interface MediaSendsOptions {
  now?: () => number;
  /** Whether an answer is kept for MEDIA_RESULT_KEEP_MS (default: every answer). */
  keep?: (outcome: Outcome<AddedResult>) => boolean;
}

interface Running {
  promise: Promise<Outcome<AddedResult>>;
}

/**
 * The hand-offs of listed streams, in the background so they outlive the popup that asked: one
 * per (tab, stream URL) at a time (a repeated request gets the running one's answer), and the
 * last answer for MEDIA_RESULT_KEEP_MS. A tab's are forgotten with its streams.
 */
export class MediaSends {
  readonly #now: () => number;
  readonly #keep: (outcome: Outcome<AddedResult>) => boolean;
  readonly #running = new Map<string, Running>();
  readonly #results = new Map<string, { outcome: Outcome<AddedResult>; at: number }>();

  constructor(options: MediaSendsOptions = {}) {
    this.#now = options.now ?? (() => Date.now());
    this.#keep = options.keep ?? (() => true);
  }

  /** Starts `start` unless the stream's hand-off is running; never rejects. */
  run(tabId: number, url: string, start: () => Promise<Outcome<AddedResult>>): Promise<Outcome<AddedResult>> {
    const key = sendKey(tabId, url);
    const running = this.#running.get(key);
    if (running) return running.promise;
    this.#prune();
    // Registered before `start` gets past its first await, which is all a repeat can observe.
    const entry = {} as Running;
    entry.promise = (async (): Promise<Outcome<AddedResult>> => {
      let outcome: Outcome<AddedResult>;
      try {
        outcome = await start();
      } catch (e) {
        outcome = failure('internal', e instanceof Error ? e.message : String(e));
      }
      // Not when the tab was cleared meanwhile: the answer belongs to the document it left.
      if (this.#running.get(key) === entry) {
        this.#running.delete(key);
        if (this.#keep(outcome)) this.#results.set(key, { outcome, at: this.#now() });
        else this.#results.delete(key);
      }
      return outcome;
    })();
    this.#running.set(key, entry);
    return entry.promise;
  }

  /** What a popup shows for the stream: a running hand-off, else the kept answer. */
  state(tabId: number, url: string): MediaSendState {
    const key = sendKey(tabId, url);
    if (this.#running.has(key)) return { sending: true };
    const outcome = this.#result(key);
    return outcome ? { result: outcome } : {};
  }

  /** The running hand-off's answer, else the kept one; undefined when there is none. */
  follow(tabId: number, url: string): Promise<Outcome<AddedResult> | undefined> {
    const key = sendKey(tabId, url);
    const running = this.#running.get(key);
    return running ? running.promise : Promise.resolve(this.#result(key));
  }

  clearTab(tabId: number): void {
    const prefix = `${tabId}\n`;
    for (const map of [this.#running, this.#results]) {
      for (const key of [...map.keys()]) if (key.startsWith(prefix)) map.delete(key);
    }
  }

  clearAll(): void {
    this.#running.clear();
    this.#results.clear();
  }

  #result(key: string): Outcome<AddedResult> | undefined {
    const r = this.#results.get(key);
    if (!r) return undefined;
    if (this.#now() - r.at < MEDIA_RESULT_KEEP_MS) return r.outcome;
    this.#results.delete(key);
    return undefined;
  }

  #prune(): void {
    const now = this.#now();
    for (const [key, r] of this.#results) if (now - r.at >= MEDIA_RESULT_KEEP_MS) this.#results.delete(key);
  }
}

function sendKey(tabId: number, url: string): string {
  return `${tabId}\n${url}`;
}
