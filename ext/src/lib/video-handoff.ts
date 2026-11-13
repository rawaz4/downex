// Videos of web pages in the background (SITE-MEDIA-SPEC §3.5, §3.6, §5.2; VIDEO-DETECT-SPEC
// §2.6, §3.6): which videos a tab and its frames show (YouTube and the curated sites of
// site-video.ts, else a frame's own address), the probe cache shared by the overlays, the popup,
// every tab and the look-ahead (video-prefetch.ts, BROWSER-SPEC §4), and the `addVideo` hand-off
// with its history record. Pure: browser APIs, the native client and storage are injected.

import { type CookieLike, cookieHeader, defaultPolicyReferer } from './context';
import type { HandoffHistory, HandoffRecord, HandoffSource } from './history';
import { cleanTitle } from './media-handoff';
import type { PageVideoEntry, VideoTaskState } from './messages';
import type { VideoOptions } from './options-form';
import {
  type AddedResult,
  type AddVideoFields,
  daemonCode,
  errorMessage,
  failure,
  type HeaderPair,
  type Outcome,
  type ProbeVideoFields,
  type RequestOptions,
  systemTimers,
  type Timers,
  type VideoInfoResult,
} from './protocol';
import {
  genericPageUrl,
  genericVideoKey,
  httpAddress,
  parseSiteVideo,
  sameOrigin,
  SITE_NAMES,
  type SiteVideo,
} from './site-video';
import { storeBuild, youtubeAddress } from './store-build';
import { isVideoRetry } from './ui-format';

/** How long a probe or add may take: the relay keeps 200 ms of it, the daemon answers 2 s before. */
export const VIDEO_DEADLINE_MS = 28_000;
/** Results and refusals of probes are kept this long … */
export const VIDEO_KEEP_MS = 10 * 60_000;
/** … for at most this many pages, the least recently used dropped first. */
export const VIDEO_CACHE_SIZE = 50;
/** Probes sent to Downex at once; more wait for a free one. */
export const VIDEO_PROBE_CONCURRENCY = 2;
/** `tools_installing` / `site_resolving`: asked again after this … */
export const VIDEO_RETRY_MS = 2000;
/** … for at most this long. */
export const VIDEO_RETRY_LIMIT_MS = 5 * 60_000;
/** Most time spent on the cookie lookup before giving up unsent. */
export const VIDEO_PREPARE_TIMEOUT_MS = 1000;

const PAGE_GONE_MESSAGE = 'This video is no longer on the page. Open the menu again.';
const NOT_A_VIDEO_PAGE = 'This page has no video Downex recognises.';

/** Refusals that hold for the page whatever asks again soon: kept like results. */
const KEPT_REFUSALS: ReadonlySet<string> = new Set([
  'encrypted',
  'live',
  'media_unsupported',
  'media_invalid',
  'site_unsupported',
  'site_unavailable',
  'auth_required',
]);

/** The end of the cache key of a probe that carries the user's sign-in (probeKey). */
const SIGN_IN_MARK = '\nsign-in';

/**
 * Whether a probe's answer is kept: its videoInfo, or a refusal of the page. An `auth_required`
 * of a probe that already carried the sign-in (`signedIn`) is not kept: the user may sign in on
 * the site and try again at once, and only Downex can tell whether that helped.
 */
export function keepProbe(o: Outcome<VideoInfoResult>, signedIn = false): boolean {
  if (o.ok) return true;
  const code = daemonCode(o.error);
  return o.error.code === 'rejected' && KEPT_REFUSALS.has(code) && !(signedIn && code === 'auth_required');
}

/**
 * Refusals kept for a probe but not for a look-ahead: YouTube answers look-aheads it throttles
 * (rate limit, bot check) with them, and a click must not inherit that for VIDEO_KEEP_MS; it asks
 * Downex itself (which keeps a refusal 30 s).
 */
const LOOK_AHEAD_UNKEPT: ReadonlySet<string> = new Set(['site_unavailable', 'auth_required']);

/** Whether a look-ahead's answer is kept: as a probe's (keepProbe), but never LOOK_AHEAD_UNKEPT. */
export function keepLookAhead(o: Outcome<VideoInfoResult>, signedIn = false): boolean {
  return keepProbe(o, signedIn) && (o.ok || !LOOK_AHEAD_UNKEPT.has(daemonCode(o.error)));
}

export interface VideoProbesOptions {
  now?: () => number;
  concurrency?: number;
  maxEntries?: number;
  /** Which answers of a probe are kept (default keepProbe); a look-ahead's: keepLookAhead as well. */
  keep?: (outcome: Outcome<VideoInfoResult>, signedIn: boolean) => boolean;
}

interface RunningProbe {
  promise: Promise<Outcome<VideoInfoResult>>;
  /** A look-ahead (BROWSER-SPEC §4) rather than a user's probe. */
  lookAhead: boolean;
}

/**
 * The probes of page videos: one per key at a time (a repeat joins the running one), at most
 * VIDEO_PROBE_CONCURRENCY sent at once, and results and refusals kept VIDEO_KEEP_MS for at most
 * VIDEO_CACHE_SIZE keys. Other failures (`tools_installing`, timeouts …) are never kept.
 * A look-ahead fills the same cache without taking one of the users' slots (its scheduler sends
 * one at a time); a user's probe joins it.
 */
export class VideoProbes {
  readonly #now: () => number;
  readonly #concurrency: number;
  readonly #max: number;
  readonly #keep: (outcome: Outcome<VideoInfoResult>, signedIn: boolean) => boolean;
  readonly #running = new Map<string, RunningProbe>();
  /** Map order: least recently used first. */
  readonly #kept = new Map<string, { outcome: Outcome<VideoInfoResult>; at: number }>();
  readonly #waiting: (() => void)[] = [];
  #active = 0;

  constructor(options: VideoProbesOptions = {}) {
    this.#now = options.now ?? (() => Date.now());
    this.#concurrency = options.concurrency ?? VIDEO_PROBE_CONCURRENCY;
    this.#max = options.maxEntries ?? VIDEO_CACHE_SIZE;
    this.#keep = options.keep ?? keepProbe;
  }

  /**
   * The kept answer, the running probe, or `start` once a slot is free; never rejects. A user's
   * probe that joins a look-ahead takes its answer when it is kept (keepLookAhead: the qualities,
   * a refusal of the page); otherwise (`tools_unavailable` while yt-dlp is not ready,
   * `site_resolving` while Downex works for the user, YouTube throttling look-aheads …) it asks
   * Downex itself at once.
   */
  run(key: string, start: () => Promise<Outcome<VideoInfoResult>>): Promise<Outcome<VideoInfoResult>> {
    const kept = this.#get(key);
    if (kept) return Promise.resolve(kept);
    const running = this.#running.get(key);
    if (running?.lookAhead) {
      return running.promise.then((o) => (keepLookAhead(o, key.endsWith(SIGN_IN_MARK)) ? o : this.run(key, start)));
    }
    if (running) return running.promise;
    return this.#start(key, start, false);
  }

  /**
   * Starts a look-ahead of `key` beside the users' slots; undefined (nothing sent) when its
   * answer is kept or a probe of it runs. Its answer is kept like a probe's (keepLookAhead).
   */
  lookAhead(key: string, start: () => Promise<Outcome<VideoInfoResult>>): Promise<Outcome<VideoInfoResult>> | undefined {
    if (this.#running.has(key) || this.#get(key)) return undefined;
    return this.#start(key, start, true);
  }

  #start(key: string, start: () => Promise<Outcome<VideoInfoResult>>, lookAhead: boolean): Promise<Outcome<VideoInfoResult>> {
    const entry: RunningProbe = { lookAhead, promise: undefined as unknown as Promise<Outcome<VideoInfoResult>> };
    // Listed before `start` runs: a repeat of the key always finds it.
    this.#running.set(key, entry);
    entry.promise = (async (): Promise<Outcome<VideoInfoResult>> => {
      if (!lookAhead) await this.#acquire();
      let outcome: Outcome<VideoInfoResult>;
      try {
        outcome = await start();
      } catch (e) {
        outcome = failure('internal', errorMessage(e));
      } finally {
        if (!lookAhead) this.#release();
      }
      if (this.#running.get(key) === entry) this.#running.delete(key);
      const signedIn = key.endsWith(SIGN_IN_MARK);
      if (this.#keep(outcome, signedIn) && (!lookAhead || keepLookAhead(outcome, signedIn))) this.#put(key, outcome);
      return outcome;
    })();
    return entry.promise;
  }

  /** What the popup shows of a key: a running probe, else the kept answer. */
  state(key: string): VideoTaskState<VideoInfoResult> | undefined {
    if (this.#running.has(key)) return { running: true };
    const outcome = this.#get(key);
    return outcome ? { result: outcome } : undefined;
  }

  /** Probes sent to Downex right now (for tests and diagnostics). */
  get active(): number {
    return this.#active;
  }

  #get(key: string): Outcome<VideoInfoResult> | undefined {
    const k = this.#kept.get(key);
    if (!k) return undefined;
    this.#kept.delete(key);
    if (this.#now() - k.at >= VIDEO_KEEP_MS) return undefined;
    this.#kept.set(key, k);
    return k.outcome;
  }

  #put(key: string, outcome: Outcome<VideoInfoResult>): void {
    this.#kept.delete(key);
    this.#kept.set(key, { outcome, at: this.#now() });
    while (this.#kept.size > this.#max) {
      const oldest = this.#kept.keys().next().value as string;
      this.#kept.delete(oldest);
    }
  }

  #acquire(): Promise<void> {
    if (this.#active < this.#concurrency) {
      this.#active++;
      return Promise.resolve();
    }
    return new Promise((resolve) => this.#waiting.push(resolve));
  }

  #release(): void {
    const next = this.#waiting.shift();
    if (next) next();
    else this.#active--;
  }
}

// ---------------------------------------------------------------------------------------
// Targets: the video a request is about, looked up again from the tab and its frames

/** What is probed or added. */
export interface VideoTarget {
  /** `youtube:<id>`, or `page:<address>` for "Find the video on this page". */
  key: string;
  /** The address Downex resolves. */
  pageUrl: string;
  /** Cookie store (Firefox container) of the tab; "" for the default store. */
  storeId: string;
  /** The embedding page of a player in a frame; sent as Referer. */
  referer?: string;
  /** "Find the video on this page": user-initiated, never with cookies. */
  generic: boolean;
  /** The tab's title, a name hint for the add (Downex prefers the site's own title). */
  title?: string;
}

/**
 * The cache key of a target: `${storeId}\n${key}`, plus a mark when its probe carries the user's
 * sign-in (§6.3). An answer got without the sign-in (an `auth_required` among them) is then never
 * taken for one with it, however soon after the user allowed it the next probe comes.
 */
export function probeKey(t: Pick<VideoTarget, 'storeId' | 'key' | 'generic'>, signIn: boolean): string {
  return `${t.storeId}\n${t.key}${signIn && !t.generic ? SIGN_IN_MARK : ''}`;
}

/** What the background knows of a target's probe (see probeKey). */
export type ProbeState = (target: VideoTarget) => VideoTaskState<VideoInfoResult> | undefined;

/** The tabs.Tab fields the lookups read (cookieStoreId: Firefox). */
export interface TabLike {
  id?: number;
  url?: string;
  title?: string;
  incognito?: boolean;
  cookieStoreId?: string;
  /** The tab shown in its window. */
  active?: boolean;
}

/** The webNavigation.getAllFrames fields the lookups read. */
export interface FrameLike {
  frameId: number;
  url: string;
}

export interface FoundVideo {
  video: SiteVideo;
  frame: 'top' | 'embed';
}

/** A tab's page videos: its own address first, then players in its frames, unique by key. */
export function findPageVideos(tab: TabLike, frames: readonly FrameLike[]): FoundVideo[] {
  const out: FoundVideo[] = [];
  const seen = new Set<string>();
  const top = parseSiteVideo(tab.url);
  if (top) {
    out.push({ video: top, frame: 'top' });
    seen.add(top.key);
  }
  for (const f of frames) {
    if (f.frameId === 0) continue;
    const v = parseSiteVideo(f.url);
    if (!v || seen.has(v.key)) continue;
    seen.add(v.key);
    out.push({ video: v, frame: 'embed' });
  }
  return out;
}

/**
 * The Referer of an embedded video's probe: what the browser's default policy
 * (strict-origin-when-cross-origin) sends for the tab's page, so yt-dlp never gets more of it
 * than the embed's host already did: the origin across origins, never a fragment or user name.
 */
function embedReferer(tab: TabLike, pageUrl: string): string | undefined {
  const page = httpAddress(tab.url);
  return page ? defaultPolicyReferer(page, pageUrl) : undefined;
}

function siteTarget(found: FoundVideo, tab: TabLike): VideoTarget {
  const t: VideoTarget = { key: found.video.key, pageUrl: found.video.pageUrl, storeId: tab.cookieStoreId ?? '', generic: false };
  if (found.frame === 'embed') {
    const referer = embedReferer(tab, t.pageUrl);
    if (referer) t.referer = referer;
  } else if (tab.title) {
    t.title = tab.title;
  }
  return t;
}

/**
 * Whether the store build gives this tab no generic target: yt-dlp's generic extractor follows a
 * YouTube player that a page (or a frame of it) embeds, so "Find the video on this page" there
 * would hand over YouTube's video all the same. Any frame on a YouTube address counts, the tab's
 * own page too; frames not known (undefined) count as such a frame.
 */
function storeHidesGeneric(tab: TabLike, frames: readonly FrameLike[] | undefined): boolean {
  if (!storeBuild()) return false;
  return frames === undefined || youtubeAddress(tab.url) || frames.some((f) => youtubeAddress(f.url));
}

/**
 * The target of the tab's own page for "Find the video on this page"; undefined off http(s) and,
 * in the store build, in a tab one of whose `frames` (webNavigation.getAllFrames) is YouTube's.
 */
export function genericTarget(tab: TabLike, frames: readonly FrameLike[]): VideoTarget | undefined {
  return genericFrameTarget(tab, tab.url, 0, frames);
}

/**
 * The generic target of a frame's own address (VIDEO-DETECT-SPEC §2.2 D): user-initiated, never
 * with cookies; an embedded frame's Referer is the tab's page as the browser's default policy sends
 * it (embedReferer), the top frame names the tab's title. None in the store build when a frame of
 * the tab is YouTube's (storeHidesGeneric).
 */
function genericFrameTarget(
  tab: TabLike,
  frameUrl: string | undefined,
  frameId: number,
  frames: readonly FrameLike[] | undefined,
): VideoTarget | undefined {
  if (tab.incognito || !frameUrl || storeHidesGeneric(tab, frames)) return undefined;
  const key = genericVideoKey(frameUrl);
  const pageUrl = key && genericPageUrl(key);
  if (!key || !pageUrl) return undefined;
  const t: VideoTarget = { key, pageUrl, storeId: tab.cookieStoreId ?? '', generic: true };
  if (frameId !== 0) {
    const referer = embedReferer(tab, pageUrl);
    if (referer) t.referer = referer;
  } else if (tab.title) {
    t.title = tab.title;
  }
  return t;
}

/** The yt-dlp target of a frame, as a panel's Site source shows it (VIDEO-DETECT-SPEC §2.6). */
export interface FrameVideo {
  target: VideoTarget;
  kind: PageVideoEntry['kind'];
  /** The site's display name, or the frame's host for a generic target. */
  site: string;
  /** A curated site (§3.8) or YouTube rather than the frame's address. */
  curated: boolean;
}

/**
 * The video of a frame: the curated (or YouTube) page or embed video its address shows, else a
 * generic target for its address; undefined in private tabs and off http(s). `frames` are the
 * tab's (webNavigation.getAllFrames), which the store build's generic target checks.
 */
export function frameVideo(tab: TabLike, frameUrl: string | undefined, frameId: number, frames: readonly FrameLike[]): FrameVideo | undefined {
  if (tab.incognito) return undefined;
  const video = parseSiteVideo(frameUrl);
  if (video) {
    const target = siteTarget({ video, frame: frameId === 0 ? 'top' : 'embed' }, tab);
    return { target, kind: video.kind, site: SITE_NAMES[video.site], curated: true };
  }
  const target = genericFrameTarget(tab, frameUrl, frameId, frames);
  return target ? { target, kind: 'page', site: hostOf(target.pageUrl), curated: false } : undefined;
}

/**
 * The target the popup names by key, looked up again from the tab and its frames (the popup's
 * address is never trusted); an `expired` failure when the page no longer shows it.
 */
export function tabTarget(tab: TabLike, frames: readonly FrameLike[], key: string): Outcome<VideoTarget> {
  if (tab.incognito) return failure('unsupported_page', NOT_A_VIDEO_PAGE);
  if (key.startsWith('page:')) {
    const t = genericTarget(tab, frames);
    return t && t.key === key ? { ok: true, value: t } : failure('expired', PAGE_GONE_MESSAGE);
  }
  const found = findPageVideos(tab, frames).find((f) => f.video.key === key);
  return found ? { ok: true, value: siteTarget(found, tab) } : failure('expired', PAGE_GONE_MESSAGE);
}

/** The runtime.MessageSender fields the content-script check reads (origin: Chromium). */
export interface SenderLike {
  url?: string;
  origin?: string;
  frameId?: number;
  tab?: TabLike;
}

/**
 * The target a content script may ask about (SITE-MEDIA-SPEC §3.6, VIDEO-DETECT-SPEC §3.6): its
 * own frame's video. The tab is a real one and not private, and `url` is either a curated (or
 * YouTube) video address on the sender frame's origin, or the sender frame's own address (a
 * generic target, never with cookies). An embed's Referer is the tab's page (http(s) only).
 * `unsupported_page` otherwise. `frames` are the tab's (webNavigation.getAllFrames): without
 * them, the store build gives no generic target (storeHidesGeneric).
 */
export function senderTarget(sender: SenderLike, url: string, frames?: readonly FrameLike[]): Outcome<VideoTarget> {
  const tab = sender.tab;
  if (!tab || tab.id === undefined || tab.id < 0 || tab.incognito) {
    return failure('unsupported_page', 'Only a tab of this browser window can ask for its video.');
  }
  const frameId = sender.frameId ?? 0;
  const video = parseSiteVideo(url);
  if (video) {
    if (!sameOrigin(url, sender.origin ?? sender.url)) {
      return failure('unsupported_page', 'The address is not the asking frame’s own.');
    }
    return { ok: true, value: siteTarget({ video, frame: frameId === 0 ? 'top' : 'embed' }, tab) };
  }
  const own = sender.url ? genericVideoKey(sender.url) : undefined;
  const target = own !== undefined && own === genericVideoKey(url) ? genericFrameTarget(tab, sender.url, frameId, frames) : undefined;
  return target ? { ok: true, value: target } : failure('unsupported_page', NOT_A_VIDEO_PAGE);
}

/** A popup entry of a found video with what the background knows of it. */
export function videoEntry(
  target: VideoTarget,
  found: Pick<FoundVideo, 'frame'> & { kind: PageVideoEntry['kind']; site: string },
  probe: VideoTaskState<VideoInfoResult> | undefined,
  add: VideoTaskState<AddedResult> | undefined,
): PageVideoEntry {
  const e: PageVideoEntry = { key: target.key, site: found.site, kind: found.kind, frame: found.frame, pageUrl: target.pageUrl };
  const title = probe && 'result' in probe && probe.result.ok ? probe.result.value.title : found.frame === 'top' ? target.title : undefined;
  const clean = cleanTitle(title);
  if (clean) e.title = clean;
  if (probe) e.probe = probe;
  if (add) e.add = add;
  return e;
}

/** The popup's entries of a tab: its page videos, else a page video found with "Find". */
export function tabVideoEntries(
  tab: TabLike,
  frames: readonly FrameLike[],
  probeState: ProbeState,
  adds: (key: string) => VideoTaskState<AddedResult> | undefined,
): PageVideoEntry[] {
  if (tab.incognito) return [];
  const found = findPageVideos(tab, frames);
  const entries = found.map((f) => {
    const t = siteTarget(f, tab);
    return videoEntry(t, { frame: f.frame, kind: f.video.kind, site: SITE_NAMES[f.video.site] }, probeState(t), adds(t.key));
  });
  if (found.some((f) => f.frame === 'top')) return entries;
  const g = genericTarget(tab, frames);
  const probe = g && probeState(g);
  if (g && probe) {
    const site = 'result' in probe && probe.result.ok && probe.result.value.site ? probe.result.value.site : hostOf(g.pageUrl);
    entries.unshift(videoEntry(g, { frame: 'top', kind: 'page', site }, probe, adds(g.key)));
  }
  return entries;
}

/** The entry "Find the video on this page" adds for a tab and its frames (no probe yet). */
export function findEntry(tab: TabLike, frames: readonly FrameLike[]): Outcome<PageVideoEntry> {
  const g = genericTarget(tab, frames);
  if (!g) {
    return storeHidesGeneric(tab, frames) && !tab.incognito
      ? failure('unsupported_page', 'Downex can’t search this page for a video.')
      : failure('unsupported_page', 'Only web pages (http and https) can be searched for a video.');
  }
  return { ok: true, value: videoEntry(g, { frame: 'top', kind: 'page', site: hostOf(g.pageUrl) }, undefined, undefined) };
}

function hostOf(url: string): string {
  try {
    return new URL(url).host;
  } catch {
    return url;
  }
}

// ---------------------------------------------------------------------------------------
// Probe and add

export interface VideoSender {
  probeVideo(fields: ProbeVideoFields, options?: RequestOptions<VideoInfoResult>): Promise<Outcome<VideoInfoResult>>;
  addVideo(fields: AddVideoFields, options?: RequestOptions<AddedResult>): Promise<Outcome<AddedResult>>;
}

export interface VideoHandoffDeps {
  client: VideoSender;
  /** `cookies.getAll({url, storeId})` for the canonical page address only. */
  getCookies: (url: string, storeId: string | undefined) => Promise<readonly CookieLike[]>;
  /** The stored video options (storage.local). */
  options: () => Promise<VideoOptions>;
  probes?: VideoProbes;
  history?: HandoffHistory;
  now?: () => number;
  timers?: Timers;
  onError?: (error: unknown) => void;
  /** A user asked for a video (a probe or an add): look-aheads may be sent again. */
  onUserRequest?: () => void;
}

export interface VideoHandOff {
  readonly probes: VideoProbes;
  /** The qualities of a target, through the shared cache; the user asked. Never rejects. */
  probe(target: VideoTarget): Promise<Outcome<VideoInfoResult>>;
  /**
   * A look-ahead of a target (`prefetch`, BROWSER-SPEC §4): otherwise identical to `probe`, into
   * the same cache. Undefined (nothing sent) when its answer is kept, a probe of it runs, or the
   * sign-in is allowed: a look-ahead never carries the user's cookies (§6.3).
   */
  lookAhead(target: VideoTarget): Promise<Outcome<VideoInfoResult> | undefined>;
  /** What the cache knows of targets now, with the stored sign-in option. */
  probeState(): Promise<ProbeState>;
  /**
   * Adds a target's video ("" tracks: Downex's default pick), asking again while Downex
   * downloads yt-dlp or resolves the page (each try VIDEO_RETRY_MS after the start of the last),
   * for up to VIDEO_RETRY_LIMIT_MS; recorded in the history. Never rejects.
   */
  add(target: VideoTarget, videoTrack: string, audioTrack: string, source?: HandoffSource): Promise<Outcome<AddedResult>>;
}

/**
 * The headers of a target's requests: the page's cookies only with the user's opt-in
 * (`useSignInForVideos`, §6.3), never for a found page and never without `cookies` (a
 * look-ahead); an embed's page as Referer.
 */
export async function videoHeaders(
  target: VideoTarget,
  deps: Pick<VideoHandoffDeps, 'getCookies' | 'options'>,
  cookies = true,
): Promise<HeaderPair[]> {
  const headers: HeaderPair[] = [];
  if (cookies && !target.generic && (await deps.options()).useSignInForVideos) {
    try {
      const cookies = await deps.getCookies(target.pageUrl, target.storeId || undefined);
      if (cookies.length > 0) headers.push(['Cookie', cookieHeader(cookies)]);
    } catch {
      // no cookie access for this address: ask without the sign-in
    }
  }
  if (target.referer) headers.push(['Referer', target.referer]);
  return headers;
}

export function createVideoHandOff(deps: VideoHandoffDeps): VideoHandOff {
  const probes = deps.probes ?? new VideoProbes();
  const now = deps.now ?? (() => Date.now());
  const timers = deps.timers ?? systemTimers;

  const sleep = (ms: number) => new Promise<void>((resolve) => timers.setTimeout(resolve, ms));

  const prepare = (target: VideoTarget, cookies = true) =>
    new Promise<Outcome<HeaderPair[]>>((resolve) => {
      const timer = timers.setTimeout(
        () => resolve(failure('timeout', `request context not ready within ${VIDEO_PREPARE_TIMEOUT_MS} ms`)),
        VIDEO_PREPARE_TIMEOUT_MS,
      );
      videoHeaders(target, deps, cookies).then(
        (headers) => {
          timers.clearTimeout(timer);
          resolve({ ok: true, value: headers });
        },
        (e: unknown) => {
          timers.clearTimeout(timer);
          resolve(failure('internal', errorMessage(e)));
        },
      );
    });

  const record = (target: VideoTarget, title: string | undefined, source: HandoffSource, outcome: Outcome<AddedResult>) => {
    if (!deps.history) return;
    const rec: HandoffRecord = { ts: now(), url: target.pageUrl, source, ok: outcome.ok };
    if (title) rec.title = title;
    if (outcome.ok) {
      rec.downloadId = outcome.value.downloadId;
      if (outcome.value.awaiting !== undefined) rec.awaiting = outcome.value.awaiting;
    } else {
      rec.error = outcome.error;
    }
    deps.history.add(rec).catch((e: unknown) => deps.onError?.(e));
  };

  const signIn = async () => {
    try {
      return (await deps.options()).useSignInForVideos;
    } catch {
      return false;
    }
  };

  const probeState = async (): Promise<ProbeState> => {
    const s = await signIn();
    return (t) => probes.state(probeKey(t, s));
  };

  const sendProbe = async (target: VideoTarget, prefetch: boolean): Promise<Outcome<VideoInfoResult>> => {
    // A look-ahead never carries the cookies, whatever the option says by now.
    const headers = await prepare(target, !prefetch);
    if (!headers.ok) return headers;
    const fields: ProbeVideoFields = { pageUrl: target.pageUrl, deadlineMs: VIDEO_DEADLINE_MS };
    if (prefetch) fields.prefetch = true;
    if (headers.value.length > 0) fields.headers = headers.value;
    return deps.client.probeVideo(fields, { timeoutMs: VIDEO_DEADLINE_MS });
  };

  const userRequest = () => {
    try {
      deps.onUserRequest?.();
    } catch (e) {
      deps.onError?.(e);
    }
  };

  const probe = async (target: VideoTarget) => {
    userRequest();
    return probes.run(probeKey(target, await signIn()), () => sendProbe(target, false));
  };

  const lookAhead = async (target: VideoTarget) => {
    // With the sign-in allowed the click's probe carries the cookies: a look-ahead without them
    // would be another request (another answer, another cache entry in Downex).
    if (await signIn()) return undefined;
    return probes.lookAhead(probeKey(target, false), () => sendProbe(target, true));
  };

  const add = async (target: VideoTarget, videoTrack: string, audioTrack: string, source: HandoffSource = 'video') => {
    userRequest();
    const title = cleanTitle(target.title);
    let outcome: Outcome<AddedResult>;
    try {
      const headers = await prepare(target);
      if (!headers.ok) {
        outcome = headers;
      } else {
        const fields: AddVideoFields = { pageUrl: target.pageUrl, deadlineMs: VIDEO_DEADLINE_MS };
        if (headers.value.length > 0) fields.headers = headers.value;
        if (videoTrack) fields.videoTrack = videoTrack;
        if (audioTrack) fields.audioTrack = audioTrack;
        if (title) fields.title = title;
        const started = now();
        for (;;) {
          const tried = now();
          outcome = await deps.client.addVideo(
            { ...fields },
            {
              timeoutMs: VIDEO_DEADLINE_MS,
              // The relay drops what misses the deadline, but an added video is shown.
              onLate: (late) => {
                if (late.ok) record(target, title, source, late);
              },
            },
          );
          if (outcome.ok || !isVideoRetry(outcome.error) || now() - started + VIDEO_RETRY_MS > VIDEO_RETRY_LIMIT_MS) break;
          // The next try starts VIDEO_RETRY_MS after this one did: at once after a call that
          // used Downex's whole wait.
          const wait = tried + VIDEO_RETRY_MS - now();
          if (wait > 0) await sleep(wait);
        }
      }
    } catch (e) {
      outcome = failure('internal', errorMessage(e));
    }
    record(target, title, source, outcome);
    return outcome;
  };

  return { probes, probe, lookAhead, probeState, add };
}

// ---------------------------------------------------------------------------------------
// Badge input: the page videos of each tab, followed from navigations

interface TabVideos {
  top?: string;
  frames: Map<number, string>;
}

/**
 * The page videos each tab shows, for the badge (§5.2): the top frame's address (documents
 * and same-document navigations) and players committed in other frames. In memory only: after
 * a worker restart a tab counts again from its next navigation, and the popup still lists its
 * videos from the frames.
 */
export class PageVideoCounts {
  readonly #tabs = new Map<number, TabVideos>();

  /** The top frame of a tab is at `url`; `document`: a new document, whose frames start empty. */
  setTop(tabId: number, url: string, document: boolean): void {
    const t = this.#tab(tabId);
    t.top = parseSiteVideo(url)?.key;
    if (document) t.frames.clear();
    this.#prune(tabId, t);
  }

  /** A frame other than the top one committed `url`. */
  setFrame(tabId: number, frameId: number, url: string): void {
    const t = this.#tab(tabId);
    const key = parseSiteVideo(url)?.key;
    if (key) t.frames.set(frameId, key);
    else t.frames.delete(frameId);
    this.#prune(tabId, t);
  }

  clearTab(tabId: number): void {
    this.#tabs.delete(tabId);
  }

  replace(removedTabId: number, addedTabId: number): void {
    const t = this.#tabs.get(removedTabId);
    this.#tabs.delete(removedTabId);
    if (t) this.#tabs.set(addedTabId, t);
  }

  clearAll(): void {
    this.#tabs.clear();
  }

  /** The page video of each frame of a tab that shows one (0: the top frame). */
  frameKeys(tabId: number): Map<number, string> {
    const t = this.#tabs.get(tabId);
    const out = new Map(t?.frames ?? []);
    if (t?.top) out.set(0, t.top);
    return out;
  }

  /** Distinct videos of a tab. */
  count(tabId: number): number {
    const t = this.#tabs.get(tabId);
    if (!t) return 0;
    const keys = new Set(t.frames.values());
    if (t.top) keys.add(t.top);
    return keys.size;
  }

  /** Tabs with at least one page video. */
  tabs(): number[] {
    return [...this.#tabs.keys()];
  }

  #tab(tabId: number): TabVideos {
    let t = this.#tabs.get(tabId);
    if (!t) {
      t = { frames: new Map() };
      this.#tabs.set(tabId, t);
    }
    return t;
  }

  #prune(tabId: number, t: TabVideos): void {
    if (!t.top && t.frames.size === 0) this.#tabs.delete(tabId);
  }
}
