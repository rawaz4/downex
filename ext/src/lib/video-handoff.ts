// Videos of web pages in the background (SITE-MEDIA-SPEC §3.5, §3.6, §5.2; VIDEO-DETECT-SPEC
// §2.6, §3.6): which videos a tab and its frames show (YouTube and the curated sites of
// site-video.ts, else a frame's own address), the probe cache shared by the overlays, the popup,
// every tab and the look-ahead (video-prefetch.ts, BROWSER-SPEC §4), and the `addVideo` hand-off
// with its history record. Pure: browser APIs, the native client and storage are injected.

import type { ClipRange } from './clip';
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
  type SignInNote,
  systemTimers,
  type Timers,
  type VideoInfoResult,
} from './protocol';
import {
  curatedPage,
  genericPageUrl,
  genericVideoKey,
  httpAddress,
  inSignInScope,
  NETWORK_ONLY_TEXT,
  parseSiteVideo,
  sameOrigin,
  signInScope,
  SITE_NAMES,
  type SiteVideo,
  messagesPage,
  ytDlpReads,
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
/**
 * The largest Cookie header of a sign-in in UTF-8 bytes, as downexd counts it (SITE-MEDIA-SPEC
 * §6.3, validate.SiteCookie); a larger one is not sent at all.
 */
export const MAX_SIGN_IN_COOKIE_BYTES = 16 * 1024;

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

/** Whether a cache key is a signed-in probe's (probeKey), of the page or of one of its items (videoItemKey). */
function signedInKey(key: string): boolean {
  return key.endsWith(SIGN_IN_MARK) || key.includes(`${SIGN_IN_MARK}\nitem:`);
}

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
      return running.promise.then((o) => (keepLookAhead(o, signedInKey(key)) ? o : this.run(key, start)));
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
      const signedIn = signedInKey(key);
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
  /**
   * "Find the video on this page" or a frame's own address: user-initiated; its sign-in, after an
   * `auth_required`, is only the page's exact host's (SITE-MEDIA-SPEC §6.3).
   */
  generic: boolean;
  /** The tab's title, a name hint for the add (Downex prefers the site's own title). */
  title?: string;
}

/**
 * The cache key of a target: `${storeId}\n${key}`, plus a mark when its probe carries the user's
 * sign-in (§6.3). The anonymous answer (an `auth_required` among them) and the signed-in one are
 * kept apart: a click asks the first, then the second only when the first needed it.
 */
export function probeKey(t: Pick<VideoTarget, 'storeId' | 'key'>, signIn: boolean): string {
  return `${t.storeId}\n${t.key}${signIn ? SIGN_IN_MARK : ''}`;
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
  /**
   * A feed video's own post found from its links (VIDEO-DETECT-SPEC §5.1 "Feed videos"), not the
   * frame's address: the tab's title is not the video's.
   */
  linked?: true;
}

/**
 * A tab's page videos that yt-dlp reads: its own address first, then players in its frames,
 * unique by key. A `network` site's video (VIDEO-DETECT-SPEC §3.8) is none of them; a post or
 * share link (mayHoldVideo) only when `playing`: a frame of the tab reported a video that plays.
 */
export function findPageVideos(tab: TabLike, frames: readonly FrameLike[], playing: boolean): FoundVideo[] {
  const out: FoundVideo[] = [];
  const seen = new Set<string>();
  const listed = (v: SiteVideo | undefined): v is SiteVideo => v !== undefined && v.resolver !== 'network' && (playing || !mayHoldVideo(v));
  const top = parseSiteVideo(tab.url);
  if (listed(top)) {
    out.push({ video: top, frame: 'top' });
    seen.add(top.key);
  }
  for (const f of frames) {
    if (f.frameId === 0) continue;
    const v = parseSiteVideo(f.url);
    if (!listed(v) || seen.has(v.key)) continue;
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
  } else if (tab.title && !found.linked) {
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
 * The generic target of a frame's own address (VIDEO-DETECT-SPEC §2.2 D): user-initiated, with
 * its exact host's sign-in only after an `auth_required` (SITE-MEDIA-SPEC §6.3); an embedded frame's Referer is the tab's page as the browser's default policy sends
 * it (embedReferer), the top frame names the tab's title. None in the store build when a frame of
 * the tab is YouTube's (storeHidesGeneric).
 */
function genericFrameTarget(
  tab: TabLike,
  frameUrl: string | undefined,
  frameId: number,
  frames: readonly FrameLike[] | undefined,
): VideoTarget | undefined {
  // A `network` site's address is not sent as a page either: yt-dlp cannot read it (§3.8); nor are
  // a site's private messages (§5.1 "DMs").
  if (tab.incognito || !frameUrl || !ytDlpReads(frameUrl) || messagesPage(frameUrl) || storeHidesGeneric(tab, frames)) return undefined;
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
  /** The clicked feed video's own post (`post` of frameVideo), not the frame's address. */
  linked?: true;
}

/**
 * The video of a frame: the curated (or YouTube) page or embed video its address shows, else a
 * generic target for its address; undefined in private tabs, off http(s) and on a `network`
 * site (§3.8). `frames` are the tab's (webNavigation.getAllFrames), which the store build's
 * generic target checks.
 */
export function frameVideo(
  tab: TabLike,
  frameUrl: string | undefined,
  frameId: number,
  frames: readonly FrameLike[],
  post?: string,
): FrameVideo | undefined {
  if (tab.incognito) return undefined;
  const video = parseSiteVideo(frameUrl);
  if (video?.resolver === 'network') return undefined;
  if (video) {
    const target = siteTarget({ video, frame: frameId === 0 ? 'top' : 'embed' }, tab);
    return { target, kind: video.kind, site: SITE_NAMES[video.site], curated: true };
  }
  const page = curatedPage(frameUrl);
  if (page === 'network') return undefined;
  // A curated site's page that is no video address: the clicked video's own post (§5.1 "Feed videos").
  const linked = linkedVideo(frameUrl, post);
  if (linked) {
    const target = siteTarget({ video: linked, frame: frameId === 0 ? 'top' : 'embed', linked: true }, tab);
    return { target, kind: linked.kind, site: SITE_NAMES[linked.site], curated: true, linked: true };
  }
  // A feed, profile, search or home page without it: nothing (yt-dlp cannot read a feed as one
  // video). A curated host's other pages (an episode, a clip, an album) keep the generic target.
  if (page === 'feed') return undefined;
  const target = genericFrameTarget(tab, frameUrl, frameId, frames);
  return target ? { target, kind: 'page', site: hostOf(target.pageUrl), curated: false } : undefined;
}

/**
 * The feed video's own post a panel names (`downex:frame-offers {post}`): a curated video that yt-dlp
 * reads, on the frame's own origin. Undefined otherwise, and on a site's private messages, where no
 * post is looked for (§5.1 "DMs").
 */
function linkedVideo(frameUrl: string | undefined, post: string | undefined): SiteVideo | undefined {
  if (!post || !sameOrigin(post, frameUrl) || messagesPage(frameUrl)) return undefined;
  const v = parseSiteVideo(post);
  return v && v.resolver !== 'network' ? v : undefined;
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
  // A post the popup listed while its video played stays its target.
  const found = findPageVideos(tab, frames, true).find((f) => f.video.key === key);
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
 * generic target, whose sign-in is its exact host's only). An embed's Referer is the tab's page (http(s) only).
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
  if (video?.resolver === 'network') return failure('unsupported_page', NETWORK_ONLY_TEXT);
  if (video) {
    if (!sameOrigin(url, sender.origin ?? sender.url)) {
      return failure('unsupported_page', 'The address is not the asking frame’s own.');
    }
    // Another video than the frame's own address shows: a feed video's post (§5.1 "Feed videos").
    const linked = parseSiteVideo(sender.url)?.key !== video.key;
    return { ok: true, value: siteTarget({ video, frame: frameId === 0 ? 'top' : 'embed', ...(linked ? { linked: true as const } : {}) }, tab) };
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

/**
 * The popup's entries of a tab: its page videos, else a page video found with "Find". `playing`:
 * a frame of the tab reported a video that plays, which a post or share link needs (§5.3).
 */
export function tabVideoEntries(
  tab: TabLike,
  frames: readonly FrameLike[],
  probeState: ProbeState,
  adds: (key: string) => VideoTaskState<AddedResult> | undefined,
  playing: boolean,
): PageVideoEntry[] {
  if (tab.incognito) return [];
  const found = findPageVideos(tab, frames, playing);
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
    if (!tab.incognito && !ytDlpReads(tab.url)) return failure('unsupported_page', NETWORK_ONLY_TEXT);
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

/** A cookie as `cookies.getAll` gives it: its `domain` decides whether it is within a sign-in's scope. */
export interface SiteCookie extends CookieLike {
  /** The cookie's host, or its domain with a leading dot. */
  domain: string;
}

export interface VideoHandoffDeps {
  client: VideoSender;
  /**
   * `cookies.getAll({url, storeId})` for the canonical page address only, in the tab's own cookie
   * store (SITE-MEDIA-SPEC §6.3): the cookies the browser itself would send to the page.
   */
  getCookies: (url: string, storeId: string | undefined) => Promise<readonly SiteCookie[]>;
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
  /**
   * The qualities of a target, through the shared cache; the user asked. Never rejects. With
   * `entryId`, those of that video of a page with several (SITE-MEDIA-SPEC §3.12), cached apart
   * (videoItemKey). Asked without cookies first; an `auth_required` answer is asked once more with
   * the user's sign-in (SITE-MEDIA-SPEC §6.3), and the answer says so (`signedIn`, `signIn`).
   */
  probe(target: VideoTarget, entryId?: string): Promise<Outcome<VideoInfoResult>>;
  /**
   * A look-ahead of a target (`prefetch`, BROWSER-SPEC §4): the anonymous request of `probe`, into
   * the same cache, never with cookies and never asked again with them. Undefined (nothing sent)
   * when its answer is kept or a probe of it runs.
   */
  lookAhead(target: VideoTarget): Promise<Outcome<VideoInfoResult> | undefined>;
  /**
   * What the cache knows of targets now: the signed-in answer where the anonymous one needed it,
   * else that refusal with what became of the sign-in, else nothing (SITE-MEDIA-SPEC §6.3 "Reopened").
   */
  probeState(): Promise<ProbeState>;
  /**
   * Adds a target's video ("" tracks: Downex's default pick), asking again while Downex
   * downloads yt-dlp or resolves the page (each try VIDEO_RETRY_MS after the start of the last),
   * for up to VIDEO_RETRY_LIMIT_MS; recorded in the history. Never rejects. With the user's sign-in
   * at once when the probe's anonymous answer needed it, else once more with it after an
   * `auth_required` (SITE-MEDIA-SPEC §6.3).
   */
  add(
    target: VideoTarget,
    videoTrack: string,
    audioTrack: string,
    source?: HandoffSource,
    clip?: ClipRange,
    entryId?: string,
  ): Promise<Outcome<AddedResult>>;
}

/**
 * The cache key of a probe of one video of a page with several (SITE-MEDIA-SPEC §3.12): the
 * target's key, plus the item's id when one is named. The panel's own state (probeState) is that
 * of the page's default item.
 */
export function videoItemKey(key: string, entryId: string | undefined): string {
  return entryId ? `${key}\nitem:${entryId}` : key;
}

/** The headers of a target's requests: the sign-in's Cookie when given (§6.3), an embed's page as Referer. */
export function videoHeaders(target: Pick<VideoTarget, 'referer'>, cookie?: string): HeaderPair[] {
  const headers: HeaderPair[] = [];
  if (cookie) headers.push(['Cookie', cookie]);
  if (target.referer) headers.push(['Referer', target.referer]);
  return headers;
}

/**
 * An `auth_required` that is a bot check (SITE-MEDIA-SPEC §6.3): "confirm you're not a bot", or
 * "fresh cookies (not necessarily logged in)". The site wants to tell people from programs, not to
 * know who the user is: the user's sign-in is never sent for it.
 */
const BOT_CHECK_TEXT = /\bnot a bot\b|\bfresh cookies\b/i;

/**
 * Whether Downex answered that the page wants a sign-in (SITE-MEDIA-SPEC §3.2, §6.3), the only
 * answer a click asks again with the user's cookies; a bot check (BOT_CHECK_TEXT) is not one.
 */
export function needsSignIn(o: Outcome<unknown>): boolean {
  return !o.ok && o.error.code === 'rejected' && daemonCode(o.error) === 'auth_required' && !BOT_CHECK_TEXT.test(o.error.message);
}

/**
 * Whether downexd's cookie file can carry a cookie (SITE-MEDIA-SPEC §4.7, §6.3): a token name and a
 * value of printable ASCII without ';' or white space. downexd drops the others, so they are never
 * sent, never counted and never called a sign-in.
 */
function usableCookie(c: Pick<CookieLike, 'name' | 'value'>): boolean {
  return /^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/.test(c.name) && /^[\x21-\x3a\x3c-\x7e]*$/.test(c.value);
}

/**
 * The sign-in scope of a target, for YouTube only (SITE-MEDIA-SPEC §6.3, amendment 2026-11-18: the
 * owner's "never rely on login"): every other site is asked without cookies only, and its refusals
 * carry no sign-in note.
 */
export function youTubeSignInScope(key: string, pageUrl: string): ReturnType<typeof signInScope> {
  return key.startsWith('youtube:') ? signInScope(key, pageUrl) : undefined;
}

/** The user's sign-in for a target's second request (§6.3): its Cookie and domain, or why there is none. */
export type SignIn = { cookie: string; domain: string; exact: boolean } | { note: SignInNote };

/**
 * The user's sign-in for a target (SITE-MEDIA-SPEC §6.3), read only now that its anonymous request
 * was answered `auth_required`: undefined when the target has no sign-in scope (signInScope: a
 * share link on another domain), a note when the option is off, the browser holds no cookie of
 * the scope that downexd can use (usableCookie) or they exceed MAX_SIGN_IN_COOKIE_BYTES. The
 * cookies are the page's (`cookies.getAll({url})` in the tab's store) within the scope; never stored.
 */
export async function readSignIn(
  target: Pick<VideoTarget, 'key' | 'pageUrl' | 'storeId'>,
  deps: Pick<VideoHandoffDeps, 'getCookies' | 'options'>,
): Promise<SignIn | undefined> {
  const scope = youTubeSignInScope(target.key, target.pageUrl);
  if (!scope) return undefined;
  const { domain } = scope;
  let on = false;
  try {
    on = (await deps.options()).useSignInForVideos;
  } catch {
    on = false;
  }
  if (!on) return { note: { domain, state: 'off' } };
  let cookies: readonly SiteCookie[];
  try {
    cookies = await deps.getCookies(target.pageUrl, target.storeId || undefined);
  } catch {
    cookies = []; // no cookie access for this address
  }
  const kept = cookies.filter((c) => typeof c.domain === 'string' && inSignInScope(c.domain, scope) && usableCookie(c));
  if (kept.length === 0) return { note: { domain, state: 'none' } };
  const cookie = cookieHeader(kept);
  if (new TextEncoder().encode(cookie).length > MAX_SIGN_IN_COOKIE_BYTES) return { note: { domain, state: 'too-large' } };
  return { cookie, domain, exact: scope.exact };
}

/** An outcome with what became of the sign-in: `signedIn` on an answer, `signIn` on a refusal. */
function noted<T extends VideoInfoResult | AddedResult>(o: Outcome<T>, domain: string, note: SignInNote | undefined): Outcome<T> {
  if (o.ok) return note ? o : { ok: true, value: { ...o.value, signedIn: domain } };
  return needsSignIn(o) ? { ok: false, error: { ...o.error, signIn: note ?? { domain, state: 'used' } } } : o;
}

export function createVideoHandOff(deps: VideoHandoffDeps): VideoHandOff {
  const probes = deps.probes ?? new VideoProbes();
  const now = deps.now ?? (() => Date.now());
  const timers = deps.timers ?? systemTimers;

  const sleep = (ms: number) => new Promise<void>((resolve) => timers.setTimeout(resolve, ms));

  /** The sign-in (readSignIn) within VIDEO_PREPARE_TIMEOUT_MS; a timeout or a failure counts as no cookie. */
  const signInOf = (target: VideoTarget) =>
    new Promise<SignIn | undefined>((resolve) => {
      const scope = youTubeSignInScope(target.key, target.pageUrl);
      const none = (): SignIn | undefined => (scope ? { note: { domain: scope.domain, state: 'none' } } : undefined);
      const timer = timers.setTimeout(() => resolve(none()), VIDEO_PREPARE_TIMEOUT_MS);
      readSignIn(target, deps).then(
        (s) => {
          timers.clearTimeout(timer);
          resolve(s);
        },
        (e: unknown) => {
          timers.clearTimeout(timer);
          deps.onError?.(e);
          resolve(none());
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

  /** The fields of both requests: the page, its headers, the item and the sign-in's domain. */
  const pageFields = (target: VideoTarget, signIn: SignIn | undefined, entryId?: string) => {
    const cookie = signIn && 'cookie' in signIn ? signIn : undefined;
    const fields: ProbeVideoFields & AddVideoFields = { pageUrl: target.pageUrl, deadlineMs: VIDEO_DEADLINE_MS };
    const headers = videoHeaders(target, cookie?.cookie);
    if (headers.length > 0) fields.headers = headers;
    // A generic page's cookies stay with its own host: no domain is named (§6.3).
    if (cookie && !cookie.exact) fields.cookieDomain = cookie.domain;
    if (entryId) fields.entryId = entryId;
    return fields;
  };

  const sendProbe = (target: VideoTarget, prefetch: boolean, signIn: SignIn | undefined, entryId?: string) => {
    // A look-ahead never carries the cookies (§6.3).
    const fields: ProbeVideoFields = pageFields(target, prefetch ? undefined : signIn, entryId);
    if (prefetch) fields.prefetch = true;
    return deps.client.probeVideo(fields, { timeoutMs: VIDEO_DEADLINE_MS });
  };

  const userRequest = () => {
    try {
      deps.onUserRequest?.();
    } catch (e) {
      deps.onError?.(e);
    }
  };

  /** The option "Use my sign-in for videos that need it" as stored now; off when it cannot be read. */
  const signInOn = async () => {
    try {
      return (await deps.options()).useSignInForVideos;
    } catch {
      return false;
    }
  };

  /**
   * What became of the sign-in after a probe's kept anonymous `auth_required` (§6.3 "Reopened"),
   * by the anonymous answer's key: a note only (a domain and a state), never a cookie. The
   * signed-in refusal itself is not kept (keepProbe), so a panel or popup opened again shows this.
   */
  const notes = new Map<string, SignInNote>();
  const remember = (key: string, o: Outcome<VideoInfoResult>) => {
    notes.delete(key);
    if (!o.ok && o.error.signIn) notes.set(key, o.error.signIn);
    while (notes.size > VIDEO_CACHE_SIZE) notes.delete(notes.keys().next().value as string);
  };

  /**
   * What the background knows of targets now (§6.3): the signed-in answer where the anonymous one
   * needed it, else that refusal with what became of the sign-in (the switch off, the note of the
   * last click); nothing when that is not known, so that opening the panel asks again.
   */
  const probeState = async (): Promise<ProbeState> => {
    const on = await signInOn();
    return (t) => {
      const key = probeKey(t, false);
      const anonymous = probes.state(key);
      if (!anonymous || !('result' in anonymous) || anonymous.result.ok || !needsSignIn(anonymous.result)) return anonymous;
      const scope = youTubeSignInScope(t.key, t.pageUrl);
      if (!scope) return anonymous;
      const refusal = anonymous.result;
      const withNote = (signIn: SignInNote): VideoTaskState<VideoInfoResult> => ({ result: { ok: false, error: { ...refusal.error, signIn } } });
      // With the option off, not even a signed-in answer kept from before is used.
      if (!on) return withNote({ domain: scope.domain, state: 'off' });
      const signed = probes.state(probeKey(t, true));
      if (signed) return signed;
      const note = notes.get(key);
      return note && note.state !== 'off' ? withNote(note) : undefined;
    };
  };

  const probe = async (target: VideoTarget, entryId?: string) => {
    userRequest();
    const key = videoItemKey(probeKey(target, false), entryId);
    const first = await probes.run(key, () => sendProbe(target, false, undefined, entryId));
    const scope = needsSignIn(first) ? youTubeSignInScope(target.key, target.pageUrl) : undefined;
    if (!scope) return first;
    let outcome: Outcome<VideoInfoResult>;
    if (!(await signInOn())) {
      // With the option off, not even a signed-in answer kept from before is used.
      outcome = noted(first, scope.domain, { domain: scope.domain, state: 'off' });
    } else {
      // The same click, once more with the user's sign-in (§6.3); a kept signed-in answer needs no
      // cookie read. A refusal with a note (no cookie, too large) is an auth_required, never kept.
      outcome = await probes.run(videoItemKey(probeKey(target, true), entryId), async () => {
        const signIn = await signInOf(target);
        if (!signIn) return first;
        if ('note' in signIn) return noted(first, signIn.note.domain, signIn.note);
        return noted(await sendProbe(target, false, signIn, entryId), signIn.domain, undefined);
      });
    }
    remember(key, outcome);
    return outcome;
  };

  const lookAhead = async (target: VideoTarget) => probes.lookAhead(probeKey(target, false), () => sendProbe(target, true, undefined));

  const add = async (
    target: VideoTarget,
    videoTrack: string,
    audioTrack: string,
    source: HandoffSource = 'video',
    clip?: ClipRange,
    entryId?: string,
  ) => {
    userRequest();
    const title = cleanTitle(target.title);
    const send = async (signIn: SignIn | undefined): Promise<Outcome<AddedResult>> => {
      const fields: AddVideoFields = pageFields(target, signIn, entryId);
      // This browser fetches the bytes when the site's video servers answer only browsers
      // (SITE-MEDIA-SPEC §2.8); NativeClient drops it for an older relay. Never for YouTube,
      // whose downloads stay as they were.
      if (!target.key.startsWith('youtube:')) fields.browserFetch = true;
      if (videoTrack) fields.videoTrack = videoTrack;
      if (audioTrack) fields.audioTrack = audioTrack;
      if (title) fields.title = title;
      if (clip) {
        // A part of the video (MEDIA-SPEC §8); NativeClient refuses it to a relay without `clips`.
        fields.clipStartMs = clip.startMs;
        if (clip.endMs > 0) fields.clipEndMs = clip.endMs;
      }
      let outcome: Outcome<AddedResult>;
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
      return outcome;
    };
    let outcome: Outcome<AddedResult>;
    try {
      // The probe of this video was answered `auth_required` without the sign-in: the add sends it
      // at once (one request); otherwise it asks without, and once more with it after the same refusal.
      const known = probes.state(videoItemKey(probeKey(target, false), entryId));
      let signIn = known && 'result' in known && needsSignIn(known.result) ? await signInOf(target) : undefined;
      outcome = await send(signIn && 'cookie' in signIn ? signIn : undefined);
      if (!signIn && needsSignIn(outcome)) {
        signIn = await signInOf(target);
        if (signIn && 'cookie' in signIn) outcome = await send(signIn);
      }
      if (signIn) outcome = 'note' in signIn ? noted(outcome, signIn.note.domain, signIn.note) : noted(outcome, signIn.domain, undefined);
    } catch (e) {
      outcome = failure('internal', errorMessage(e));
    }
    record(target, title, source, outcome);
    return outcome;
  };

  return { probes, probe, lookAhead, probeState, add };
}

/**
 * Whether an address only may show a video (§3.8): a `post` (most posts are text or images) or a
 * share `link`. Such a page video is listed and counted only once a frame of the tab reported a
 * video that plays (§5.3).
 */
export function mayHoldVideo(v: SiteVideo): boolean {
  return v.kind === 'post' || v.kind === 'link';
}

/** A page video on the badge: its key, and whether it only may be one (mayHoldVideo). */
interface BadgeVideo {
  key: string;
  maybe: boolean;
}

/** The page video an address counts as on the badge; none for a `network` site's (§3.8). */
function badgeVideo(url: string): BadgeVideo | undefined {
  const v = parseSiteVideo(url);
  return v && v.resolver !== 'network' ? { key: v.key, maybe: mayHoldVideo(v) } : undefined;
}

// ---------------------------------------------------------------------------------------
// Badge input: the page videos of each tab, followed from navigations

interface TabVideos {
  top?: BadgeVideo;
  frames: Map<number, BadgeVideo>;
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
    t.top = badgeVideo(url);
    if (document) t.frames.clear();
    this.#prune(tabId, t);
  }

  /** A frame other than the top one committed `url`. */
  setFrame(tabId: number, frameId: number, url: string): void {
    const t = this.#tab(tabId);
    const video = badgeVideo(url);
    if (video) t.frames.set(frameId, video);
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

  /**
   * The page video of each frame of a tab that shows one (0: the top frame). A post or share
   * link (mayHoldVideo) only when `playing`: a frame of the tab reported a video that plays.
   */
  frameKeys(tabId: number, playing: boolean): Map<number, string> {
    const t = this.#tabs.get(tabId);
    const out = new Map<number, string>();
    for (const [frameId, v] of t?.frames ?? []) if (playing || !v.maybe) out.set(frameId, v.key);
    if (t?.top && (playing || !t.top.maybe)) out.set(0, t.top.key);
    return out;
  }

  /** Distinct videos of a tab, posts and share links included (frameKeys decides what counts). */
  count(tabId: number): number {
    const t = this.#tabs.get(tabId);
    if (!t) return 0;
    const keys = new Set([...t.frames.values()].map((v) => v.key));
    if (t.top) keys.add(t.top.key);
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
