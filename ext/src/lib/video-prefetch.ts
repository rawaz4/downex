// Look-ahead of a page video's qualities while the user watches it (BROWSER-SPEC §4,
// VIDEO-DETECT-SPEC §5.5): after PREFETCH_DWELL_MS on a YouTube watch or Shorts page, or a
// curated site's video marked for it (§3.8), in a visible tab, the content script tells the
// background, which gets the qualities through the shared probe cache (marked `prefetch`), so that
// Download shows them at once. One look-ahead at a time in the whole browser, the newest video
// next, within a budget (PREFETCH_BURST, then one per PREFETCH_REFILL_MS); none while the options
// do not allow it (lookAheadAllowed, and the hidden-sites rule the background adds) or Downex is
// not known to work, none after Downex said its tools are not ready or video sites are off until
// the user asks for a video once, and none to a site for PREFETCH_THROTTLE_MS after it throttled
// one (siteThrottled). Pure: timers, the clock, the probe and the browser's answers are injected.

import type { VideoOptions } from './options-form';
import { daemonCode, type Outcome, systemTimers, type Timers, type VideoInfoResult } from './protocol';
import { siteHost } from './rules';
import { parseSiteVideo, siteOfKey, type SiteVideo } from './site-video';
import { type SenderLike, senderTarget, type TabLike, type VideoTarget } from './video-handoff';

/** How long the user stays on a video before its qualities are got ahead. */
export const PREFETCH_DWELL_MS = 1500;
/** Look-aheads that may go out at once after a quiet time … */
export const PREFETCH_BURST = 10;
/** … then one more per this long: at most 60 an hour, well within YouTube's limits (Shorts!). */
export const PREFETCH_REFILL_MS = 60_000;
/** After a site throttled a look-ahead (rate limit, bot check), none goes out to it for this long. */
export const PREFETCH_THROTTLE_MS = 30 * 60_000;

/**
 * Whether the video options allow look-aheads: "Prepare video qualities while you watch" on, the
 * Download button shown (with it off the user does not use Downex on YouTube), and the sign-in not
 * allowed: a look-ahead never carries the user's cookies (they go only with a video the user asked
 * for, SITE-MEDIA-SPEC §6.3), and one without them would not be the click's probe.
 */
export function lookAheadAllowed(o: VideoOptions): boolean {
  return o.videoPrefetch && o.videoButton && !o.useSignInForVideos;
}

/**
 * The video a look-ahead may be about: a YouTube watch or Shorts page, or a curated site's watch
 * or clip video marked for it (VIDEO-DETECT-SPEC §3.8); never an embed, a live page, YouTube's
 * clips, Facebook or Instagram.
 */
export function prefetchVideo(url: string | undefined): SiteVideo | undefined {
  const v = parseSiteVideo(url);
  return v?.lookAhead ? v : undefined;
}

// ---------------------------------------------------------------------------------------
// Content script: the dwell on one video

/** What the content script sees now. */
export interface DwellView {
  /** The top frame's address; undefined in any other frame. */
  url: string | undefined;
  /** `document.visibilityState === 'visible'`. */
  visible: boolean;
  /** The video options allow look-aheads (lookAheadAllowed). */
  enabled: boolean;
}

/** The video the dwell counts for, or undefined when nothing may be got ahead. */
export function dwellKey(v: DwellView): string | undefined {
  return v.enabled && v.visible ? prefetchVideo(v.url)?.key : undefined;
}

export interface PrefetchDwellDeps {
  read: () => DwellView;
  /** The dwell on the video at `url` ended: ask the background for a look-ahead. */
  fire: (url: string) => void;
  timers?: Pick<Timers, 'setTimeout' | 'clearTimeout'>;
  dwellMs?: number;
}

/**
 * Reports each continuous visible stay of PREFETCH_DWELL_MS on a watch or Shorts video once.
 * `sync()` is called on every navigation, visibility or option change (as often as the page
 * likes): another video, a hidden tab or the option turned off start the count again, so
 * Shorts swiped past are never reported. The address is read again when the time is up.
 */
export class PrefetchDwell {
  readonly #deps: PrefetchDwellDeps;
  readonly #timers: Pick<Timers, 'setTimeout' | 'clearTimeout'>;
  /** The video counted for, or already reported in this stay. */
  #key: string | undefined;
  #timer: unknown;

  constructor(deps: PrefetchDwellDeps) {
    this.#deps = deps;
    this.#timers = deps.timers ?? systemTimers;
  }

  sync(): void {
    const key = dwellKey(this.#deps.read());
    if (key === this.#key) return;
    this.#clear();
    this.#key = key;
    if (key !== undefined) this.#timer = this.#timers.setTimeout(() => this.#elapsed(), this.#deps.dwellMs ?? PREFETCH_DWELL_MS);
  }

  stop(): void {
    this.#clear();
    this.#key = undefined;
  }

  #elapsed(): void {
    this.#timer = undefined;
    const view = this.#deps.read();
    if (dwellKey(view) !== this.#key || view.url === undefined) {
      // The page moved on before an event said so: count for what it shows now.
      this.#key = undefined;
      this.sync();
      return;
    }
    this.#deps.fire(view.url);
  }

  #clear(): void {
    if (this.#timer === undefined) return;
    this.#timers.clearTimeout(this.#timer);
    this.#timer = undefined;
  }
}

// ---------------------------------------------------------------------------------------
// Background: which look-ahead is sent, and when

/** A look-ahead a content script asked for: its tab's top frame video. */
export interface LookAheadRequest {
  tabId: number;
  target: VideoTarget;
  /** The tab's host, which the hidden-sites rule reads ("" when unknown). */
  host: string;
}

/**
 * The look-ahead a content script may ask for: the video of its top frame a look-ahead may be
 * about (prefetchVideo), in a tab shown in its window (the content script checked the page is
 * visible), not private, on the sender's own origin (senderTarget). Undefined otherwise.
 */
export function lookAheadRequest(sender: SenderLike, url: string): LookAheadRequest | undefined {
  const tabId = sender.tab?.id;
  if ((sender.frameId ?? 0) !== 0 || sender.tab?.active === false || tabId === undefined) return undefined;
  if (!prefetchVideo(url)) return undefined;
  const t = senderTarget(sender, url);
  return t.ok ? { tabId, target: t.value, host: siteHost(sender.tab?.url) ?? '' } : undefined;
}

/** The site a look-ahead is for (`youtube`, `vimeo`, …), which its throttle is kept for. */
export function lookAheadSite(target: Pick<VideoTarget, 'key'>): string {
  return siteOfKey(target.key) ?? 'page';
}

/** Whether `tab` still shows the target's video in its window (checked before a waiting look-ahead goes out). */
export function stillWatched(tab: TabLike, target: VideoTarget): boolean {
  return !tab.incognito && tab.active !== false && prefetchVideo(tab.url)?.key === target.key;
}

/** Answers after which no look-ahead is sent until the user asks for a video once. */
const BACK_OFF_CODES: ReadonlySet<string> = new Set(['tools_installing', 'tools_unavailable', 'site_media_off']);

/**
 * A site's rate limit and bot check in yt-dlp's words, as Downex passes them on: classified
 * `site_unavailable` ("HTTP Error 429", "rate-limited", "try again later") or `auth_required`
 * ("confirm you're not a bot"), SITE-MEDIA-SPEC §3.10. Other refusals of those codes are about one
 * video (private, removed, age check).
 */
const THROTTLED_TEXT = /\b429\b|rate.?limit|too many requests|try again later|not a bot/i;

/** Whether an answer says the site throttles this computer (THROTTLED_TEXT). */
export function throttledAnswer(o: Outcome<unknown>): boolean {
  if (o.ok || o.error.code !== 'rejected') return false;
  const code = daemonCode(o.error);
  return (code === 'site_unavailable' || code === 'auth_required') && THROTTLED_TEXT.test(o.error.message);
}

/**
 * Whether an answer pauses look-aheads of `site` for PREFETCH_THROTTLE_MS (VIDEO-DETECT-SPEC §5.5):
 * a rate limit or bot check (throttledAnswer), and on sites other than YouTube any
 * `auth_required`: they need a sign-in the look-ahead never sends.
 */
export function siteThrottled(site: string, o: Outcome<unknown>): boolean {
  if (throttledAnswer(o)) return true;
  return site !== 'youtube' && !o.ok && o.error.code === 'rejected' && daemonCode(o.error) === 'auth_required';
}

export interface VideoLookAheadDeps {
  /** The video options allow this look-ahead (lookAheadAllowed and the tab's hidden-sites rule). */
  enabled: (request: LookAheadRequest) => Promise<boolean>;
  /** The relay is connected and serves look-aheads, and downexd is known to run. */
  ready: () => boolean;
  /** Sends the look-ahead through the probe cache; undefined when nothing was sent (kept, or a probe runs). */
  run: (target: VideoTarget) => Promise<Outcome<VideoInfoResult> | undefined>;
  /** Whether the tab still shows the video: asked before a look-ahead that waited goes out. */
  stillShown?: (request: LookAheadRequest) => Promise<boolean>;
  onError?: (error: unknown) => void;
  /** The clock of the budget and the throttle pause (Date.now). */
  now?: () => number;
}

const sameVideo = (a: VideoTarget, b: VideoTarget) => a.key === b.key && a.storeId === b.storeId;

/**
 * How long a curated site's look-ahead the background could not send yet (no frame of the tab
 * reported a playable video: a player that creates its `<video>` late, in a shadow root or in a
 * sub-frame) waits for the first such report.
 */
export const PREFETCH_HOLD_MS = 30_000;
/** Most look-aheads held at once (one per tab). */
const MAX_HELD = 100;

/**
 * The look-aheads held back per tab (VIDEO-DETECT-SPEC §5.5): the dwell fires once per stay, so
 * one that came before the tab's first playable video report is kept for PREFETCH_HOLD_MS and
 * sent when that report arrives (the run still checks that the tab shows the video).
 */
export class HeldLookAheads {
  readonly #held = new Map<number, { req: LookAheadRequest; at: number }>();

  hold(req: LookAheadRequest, now: number): void {
    this.#held.delete(req.tabId);
    this.#held.set(req.tabId, { req, at: now });
    while (this.#held.size > MAX_HELD) this.#held.delete(this.#held.keys().next().value as number);
  }

  /** The tab's held look-ahead, if one is held and not older than PREFETCH_HOLD_MS; it is let go. */
  take(tabId: number, now: number): LookAheadRequest | undefined {
    const h = this.#held.get(tabId);
    this.#held.delete(tabId);
    return h && now - h.at <= PREFETCH_HOLD_MS ? h.req : undefined;
  }

  forget(tabId: number): void {
    this.#held.delete(tabId);
  }
}

/**
 * The look-aheads of the whole browser: at most one in flight; while it runs, only the newest
 * request waits (an older waiting one is dropped). At most PREFETCH_BURST go out at once, then one
 * per PREFETCH_REFILL_MS (one that sent nothing costs nothing). Requests are dropped while Downex
 * is not known to work (`ready`), after an answer about the tools or video sites being off
 * (BACK_OFF_CODES) until `userAsked()`, and those of a site for PREFETCH_THROTTLE_MS after it
 * throttled one (siteThrottled), whatever the user does; a refusal on one site never pauses another.
 */
export class VideoLookAhead {
  readonly #deps: VideoLookAheadDeps;
  readonly #now: () => number;
  #current: LookAheadRequest | undefined;
  #next: LookAheadRequest | undefined;
  #backedOff = false;
  /** Per site: until when its look-aheads pause. */
  readonly #throttledUntil = new Map<string, number>();
  #tokens = PREFETCH_BURST;
  /** When the budget last gained a look-ahead (or was full). */
  #refilledAt: number | undefined;

  constructor(deps: VideoLookAheadDeps) {
    this.#deps = deps;
    this.#now = deps.now ?? (() => Date.now());
  }

  /** A content script's dwell on a video ended. */
  request(req: LookAheadRequest): void {
    if (this.#backedOff || this.isThrottled(lookAheadSite(req.target)) || !this.#deps.ready()) return;
    if (this.#current) {
      if (!sameVideo(this.#current.target, req.target)) this.#next = req;
      return;
    }
    this.#start(req, false);
  }

  /** The user asked for a video (a probe or an add): look-aheads may be sent again. */
  userAsked(): void {
    this.#backedOff = false;
  }

  get backedOff(): boolean {
    return this.#backedOff;
  }

  /** `site` throttled a look-ahead less than PREFETCH_THROTTLE_MS ago. */
  isThrottled(site: string): boolean {
    const until = this.#throttledUntil.get(site);
    if (until === undefined) return false;
    if (this.#now() < until) return true;
    this.#throttledUntil.delete(site);
    return false;
  }

  /** Some site throttled a look-ahead less than PREFETCH_THROTTLE_MS ago. */
  get throttled(): boolean {
    return [...this.#throttledUntil.keys()].some((site) => this.isThrottled(site));
  }

  /** The look-ahead in flight (being checked or sent). */
  get running(): VideoTarget | undefined {
    return this.#current?.target;
  }

  /** The newest request waiting for it. */
  get waiting(): VideoTarget | undefined {
    return this.#next?.target;
  }

  #start(req: LookAheadRequest, waited: boolean): void {
    this.#current = req;
    void this.#send(req, waited)
      .catch((e: unknown) => this.#deps.onError?.(e))
      .finally(() => {
        this.#current = undefined;
        const next = this.#next;
        this.#next = undefined;
        if (next) this.#start(next, true);
      });
  }

  async #send(req: LookAheadRequest, waited: boolean): Promise<void> {
    const site = lookAheadSite(req.target);
    if (!(await this.#deps.enabled(req))) return;
    if (this.#backedOff || this.isThrottled(site) || !this.#deps.ready()) return;
    if (waited && this.#deps.stillShown && !(await this.#deps.stillShown(req))) return;
    if (!this.#take()) return;
    const outcome = await this.#deps.run(req.target);
    if (!outcome) {
      this.#tokens = Math.min(PREFETCH_BURST, this.#tokens + 1); // nothing was sent
      return;
    }
    if (!outcome.ok && BACK_OFF_CODES.has(daemonCode(outcome.error))) {
      this.#backedOff = true;
      this.#next = undefined;
    } else if (siteThrottled(site, outcome)) {
      this.#throttledUntil.set(site, this.#now() + PREFETCH_THROTTLE_MS);
      if (this.#next && lookAheadSite(this.#next.target) === site) this.#next = undefined;
    }
  }

  /** Takes a look-ahead from the budget, refilled one per PREFETCH_REFILL_MS up to PREFETCH_BURST. */
  #take(): boolean {
    const now = this.#now();
    const since = this.#refilledAt ?? now;
    const gained = Math.max(0, Math.floor((now - since) / PREFETCH_REFILL_MS));
    this.#tokens = Math.min(PREFETCH_BURST, this.#tokens + gained);
    this.#refilledAt = this.#tokens === PREFETCH_BURST ? now : since + gained * PREFETCH_REFILL_MS;
    if (this.#tokens === 0) return false;
    this.#tokens--;
    return true;
  }
}
