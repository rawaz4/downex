// The generic video tracker (VIDEO-DETECT-SPEC §2.5, §5.5): which `<video>` elements of a frame
// get a Download button. It reads only the public state of video elements (source, size,
// duration, DRM, visibility), never page text, scripts or storage. It listens to media events in
// the capture phase and to one IntersectionObserver and one ResizeObserver; there is no subtree
// MutationObserver but in the top frame of a compact social site (one whose callback only schedules
// a check), and no layout is read here (the observers deliver sizes) but once per right-click, once
// per choice of "Show Downex download panel" and, on compact sites, once per grid tile the pointer
// enters (VIDEO-DETECT-SPEC §5.1 "Social sites"). It reports the frame's videos to the background
// (`downex:frame-videos`) and asks for the overlay UI (`downex:overlay-state`, `downex:video-ui`)
// only once the frame has a video worth a button, or the user asked for a video's panel from the
// context menu. The overlay UI script finds it on `globalThis[Symbol.for('downex.video')]`
// (TrackerApi). In the top frame of a curated video address it also runs the look-ahead dwell.
// Pure apart from the DOM: observers, timers, EME, shadow roots, options and messaging are
// injected.

import type { ExtensionMessage, FrameMessage, ResponseFor } from './messages';
import { MAX_FRAME_VIDEOS, MAX_FRAME_VIDEOS_BYTES } from './messages';
import { type FrameVideoMeta, UI_PROTOCOL } from './offer-types';
import { buttonAllowed, type VideoOptions } from './options-form';
import { systemTimers, type Timers } from './protocol';
import { lookAheadAllowed, type PrefetchDwellDeps } from './video-prefetch';

/** Where the tracker publishes its API for the overlay UI script (same isolated world). */
export const TRACKER_KEY = Symbol.for('downex.video');

/** Most videos tracked per frame. */
export const MAX_TRACKED_VIDEOS = 32;
/** Smallest rendered size (CSS px) that gets a button. */
export const MIN_VIDEO_WIDTH = 200;
export const MIN_VIDEO_HEIGHT = 112;
/** Part of the video that must be inside the viewport. */
export const VISIBLE_RATIO = 0.5;
/** A muted, looping video without controls shorter than this is decorative (a background loop). */
export const DECORATIVE_MAX_MS = 30_000;
/**
 * A video whose finite duration grew this many times by LIVE_GROWTH_S or more is live: players
 * such as hls.js give a live stream a finite, growing duration rather than Infinity.
 */
export const LIVE_GROWTHS = 3;
const LIVE_GROWTH_S = 1;
/** Element reports go out at most this often, and only when something changed. */
export const REPORT_INTERVAL_MS = 1000;
/** Pointer activity looks for new videos at most this often. */
export const POINTER_CHECK_MS = 1000;
/** A right-clicked video is kept this long for "Show Downex download panel" (§5.1 "Menu"). */
export const MENU_VIDEO_MS = 60_000;
/** The deep scan for videos in shadow roots looks at most at this many elements … */
export const DEEP_SCAN_MAX_ELEMENTS = 5000;
/** … and enters at most this many nested shadow roots. */
export const DEEP_SCAN_MAX_DEPTH = 16;
/** The late-mount observer's check comes this long after the first change it saw (§5.1 "Social sites"). */
export const MUTATION_CHECK_MS = 250;
/** Smallest grid tile (CSS px) that gets a button (§5.1 "Grid tiles"). */
export const MIN_TILE_PX = 96;
/** Longest `src` reported (DAEMON-SPEC §4 URL limit). */
const MAX_SRC_CHARS = 32 * 1024;
const MAX_ID = 2 ** 31 - 1;

/** Media events the tracker follows (they do not bubble: listened to in the capture phase). */
export const MEDIA_EVENTS = [
  'loadstart',
  'loadedmetadata',
  'durationchange',
  'play',
  'playing',
  'resize',
  'emptied',
  'encrypted',
  'waitingforkey',
] as const;

/** What an IntersectionObserver entry tells the tracker. */
export interface IntersectionEntryLike {
  target: Element;
  intersectionRatio: number;
  boundingClientRect: { width: number; height: number };
}

/** What a ResizeObserver entry tells the tracker. */
export interface ResizeEntryLike {
  target: Element;
  contentRect: { width: number; height: number };
  borderBoxSize?: ReadonlyArray<{ inlineSize: number; blockSize: number }>;
}

export interface ObserverLike {
  observe(target: Element): void;
  unobserve(target: Element): void;
  disconnect(): void;
}

/** The late-mount observer (`new MutationObserver(cb)` observing childList and subtree). */
export interface MutationObserverLike {
  observe(target: Node, options: { childList: boolean; subtree: boolean }): void;
  disconnect(): void;
}

/**
 * A grid tile the pointer is over (VIDEO-DETECT-SPEC §5.1 "Grid tiles"): a link that shows a
 * picture of a video, with that video's own address (resolved `href`, no fragment).
 */
export interface VideoTileTarget {
  el: Element;
  url: string;
}

/** The dwell the top frame runs on a curated video address (PrefetchDwell). */
export interface DwellLike {
  sync(): void;
  stop(): void;
  /** The user pointed at a Download button: a stay being counted ends now (PrefetchDwell.now). */
  now?(): void;
}

/** A tracked video as the overlay UI sees it. */
export interface TrackedVideo {
  id: number;
  el: HTMLVideoElement;
  meta: FrameVideoMeta;
  /**
   * A decorative loop on a host that is not curated, otherwise worth a button (VIDEO-DETECT-SPEC
   * §2.5): it gets the mark alone, only while the pointer is over it (§5.1 "Quiet videos").
   */
  quiet?: boolean;
  /** When it last started playing or loaded its metadata (`timers.now()`), if it did. */
  startedAt?: number;
}

export type TrackerEvent =
  /** The tracked videos or their state changed. */
  | { type: 'videos' }
  /** Whether buttons may show changed (`allowed`). */
  | { type: 'state' }
  /** A video started playing or loaded its metadata: its button shows for a while. */
  | { type: 'reveal'; id: number }
  /** A video lost its media (`emptied`): its panel closes. */
  | { type: 'emptied'; id: number }
  /** The pointer entered another grid tile (`tile`, compact sites only). */
  | { type: 'tile' }
  /** The user asked for the panel of this video from the context menu (`requested`). */
  | { type: 'open'; id: number }
  /** The extension went away (reloaded or updated): the UI removes itself for good. */
  | { type: 'invalidated' };

/**
 * The tracker ↔ overlay UI handshake (UI_PROTOCOL). `send` resolves undefined when the extension
 * is gone or did not answer.
 */
export interface TrackerApi {
  readonly version: number;
  videos(): TrackedVideo[];
  onChange(fn: (e: TrackerEvent) => void): () => void;
  send<M extends ExtensionMessage>(msg: M): Promise<ResponseFor[M['type']] | undefined>;
  readonly invalidated: boolean;
  /** Buttons may show in this tab (`downex:overlay-state`). */
  readonly allowed: boolean;
  /** The element's state now: DRM and live are checked again at click time. */
  recheck(id: number): FrameVideoMeta | undefined;
  /** Asks `downex:overlay-state` again (after "Don't show on <host>"). */
  refreshState(): void;
  /** True for the first UI script that mounts; false afterwards (one layer per frame). */
  claimUi(): boolean;
  /**
   * The video whose panel the user asked for from the context menu: it gets a button and its
   * panel even where buttons are not allowed or it is not meaningful (§5.1 "Menu").
   */
  readonly requested: number | undefined;
  /** The requested video's panel closed: the usual rules apply to it again. */
  release(id: number): void;
  /** The grid tile the pointer last entered, while it is in the page (§5.1 "Grid tiles"). */
  readonly tile?: VideoTileTarget | undefined;
  /**
   * Whether a video element is shown (its IntersectionObserver ratio at least 0.5): undefined when
   * it is not tracked or has no ratio yet (the feed walk's carousel rule, §5.1 "Carousels").
   */
  shown?(el: Element): boolean | undefined;
  /**
   * The pointer entered a Download button (VIDEO-DETECT-SPEC §5.5, amended 2026-11-19): the
   * look-ahead of the frame's video address, under its usual rules, goes out now instead of after
   * the rest of the dwell. Optional: an older tracker has none.
   */
  lookAheadNow?(): void;
}

/** Whether `v` is a tracker API of this protocol version. */
export function isTrackerApi(v: unknown): v is TrackerApi {
  if (typeof v !== 'object' || v === null) return false;
  const a = v as Partial<TrackerApi>;
  return a.version === UI_PROTOCOL && typeof a.videos === 'function' && typeof a.onChange === 'function' && typeof a.send === 'function';
}

export interface VideoTrackerDeps {
  doc: Document;
  /** `runtime.sendMessage`; rejects when the extension is gone. */
  send: (msg: ExtensionMessage) => Promise<unknown>;
  timers?: Timers;
  /** `new IntersectionObserver(cb, {threshold: [0, 0.5]})`; undefined: every video counts as visible. */
  intersection?: (cb: (entries: readonly IntersectionEntryLike[]) => void) => ObserverLike | undefined;
  /** `new ResizeObserver(cb)`; undefined: sizes come from the IntersectionObserver only. */
  resize?: (cb: (entries: readonly ResizeEntryLike[]) => void) => ObserverLike | undefined;
  /** The element's `mediaKeys` (EME); default the property itself. */
  mediaKeys?: (v: HTMLVideoElement) => unknown;
  /** An element's open or closed shadow root (`dom.openOrClosedShadowRoot`); default `el.shadowRoot`. */
  shadowRoot?: (el: Element) => ShadowRoot | null | undefined;
  /** This is the tab's top frame (the only one that runs the look-ahead dwell). */
  topFrame?: boolean;
  /** The frame's address. */
  location?: () => string;
  /** Whether an address is a curated video whose qualities may be got ahead (`prefetchVideo`). */
  lookAheadVideo?: (url: string) => unknown;
  /** The stored video options (read only on a curated look-ahead address). */
  readOptions?: () => Promise<VideoOptions>;
  /** Makes the dwell (`new PrefetchDwell(deps)`). */
  dwell?: (deps: PrefetchDwellDeps) => DwellLike;
  /** `document.visibilityState === 'visible'`. */
  visible?: () => boolean;
  /** Whether an event came from the user (`event.isTrusted`; tests substitute it). */
  isTrusted?: (e: Event) => boolean;
  /**
   * Whether an address is on a curated site's host (`siteOfAddress`, VIDEO-DETECT-SPEC §3.8): no
   * video of such a top frame (`topFrame`) is decorative (§2.5). An embedded frame on such a host
   * (a player on another site's page, such as a landing page's background loop) keeps the rule.
   * Undefined: none is.
   */
  curatedHost?: (url: string) => boolean;
  /**
   * Whether an address is on a compact social site's host (`compactSite`, VIDEO-DETECT-SPEC §5.1
   * "Social sites"): its top frame also runs the late-mount observer and finds grid tiles.
   */
  compactHost?: (url: string) => boolean;
  /** `new MutationObserver(cb)`; undefined: no late-mount observer. */
  mutations?: (cb: () => void) => MutationObserverLike | undefined;
  /**
   * The post a grid tile's link names in a frame at `frameUrl` (`tilePost`, §3.8 "Tiles", §5.1
   * "Grid tiles"): its address, else undefined (also on a video address or in private messages).
   */
  tileVideo?: (href: string, frameUrl: string) => string | undefined;
}

interface VideoState {
  id: number;
  el: HTMLVideoElement;
  /** Rendered size from the observers; 0 unknown. */
  width: number;
  height: number;
  /** Part inside the viewport; undefined until the IntersectionObserver reported. */
  ratio: number | undefined;
  /** `encrypted` or `waitingforkey` was seen. */
  encrypted: boolean;
  /** Found in a shadow root: its own listeners (media events do not leave shadow roots). */
  shadow: boolean;
  /** When it last started playing or loaded its metadata. */
  startedAt?: number;
  /** The last finite duration seen on `durationchange` (s; NaN none), and how often it grew. */
  lastDuration: number;
  growths: number;
}

/** The URL without its fragment. */
function withoutFragment(url: string): string {
  const i = url.indexOf('#');
  return i < 0 ? url : url.slice(0, i);
}

function count(v: number, max: number): number {
  return Number.isFinite(v) && v > 0 ? Math.min(max, Math.round(v)) : 0;
}

function hostOf(url: string): string {
  try {
    return new URL(url).hostname.toLowerCase();
  } catch {
    return '';
  }
}

export class VideoTracker {
  readonly #deps: VideoTrackerDeps;
  readonly #doc: Document;
  readonly #timers: Timers;
  readonly #states = new WeakMap<Element, VideoState>();
  /** The tracked videos in the order found (strong references, pruned when disconnected). */
  #list: VideoState[] = [];
  readonly #collection: HTMLCollectionOf<HTMLVideoElement>;
  readonly #io: ObserverLike | undefined;
  readonly #ro: ObserverLike | undefined;
  readonly #listeners = new Set<(e: TrackerEvent) => void>();
  readonly #cleanup: (() => void)[] = [];
  #nextId = 0;
  /** Where the next look for untracked videos at the cap starts in the live collection. */
  #cursor = 0;
  #evalTimer: unknown;
  #reportTimer: unknown;
  #lastReportAt = Number.NEGATIVE_INFINITY;
  #lastReport: string | undefined;
  /** A video worth reporting was seen: reports go out from then on (DRM and live included). */
  #active = false;
  #lastPointerCheck = Number.NEGATIVE_INFINITY;
  /** `downex:overlay-state`: not asked yet, being asked, or answered. */
  #state: 'unknown' | 'asking' | 'known' = 'unknown';
  #askAgain = false;
  #allowed = false;
  /** The frame's videos asked for the overlay UI once (a refusal is not asked again for them). */
  #uiAskedOnce = false;
  #uiClaimed = false;
  #invalidated = false;
  #dwell: DwellLike | undefined;
  #options: VideoOptions | undefined;
  /** The pending or answered `downex:video-ui` request: whether the overlay UI is in the frame. */
  #ui: Promise<boolean> | undefined;
  /**
   * The last trusted `contextmenu` event, and when: its `<video>`, or else where it was (a video in
   * a closed shadow root that was never tracked is looked for there when the menu item is chosen).
   */
  #menuVideo: { el?: HTMLVideoElement; x?: number; y?: number; at: number } | undefined;
  /** The id of the video whose panel the context menu asked for (`showPanel`). */
  #requested: number | undefined;
  /** A trusted `pointermove` was seen in the frame: quiet videos may ask for the overlay UI. */
  #pointed = false;
  /** The frame address `curatedHost` was last asked about, and its answer. */
  #curatedFor: { url: string; curated: boolean } | undefined;
  /** The late-mount observer and its pending check (compact sites' top frames). */
  #mutations: MutationObserverLike | undefined;
  #mutationTimer: unknown;
  /** The grid tile the pointer last entered (§5.1 "Grid tiles"), and the last link judged. */
  #tile: VideoTileTarget | undefined;
  #lastLink: Element | undefined;
  readonly api: TrackerApi;

  constructor(deps: VideoTrackerDeps) {
    this.#deps = deps;
    this.#doc = deps.doc;
    this.#timers = deps.timers ?? systemTimers;
    this.#collection = this.#doc.getElementsByTagName('video');
    this.#io = deps.intersection?.((entries) => this.#onIntersection(entries));
    this.#ro = deps.resize?.((entries) => this.#onResize(entries));
    const self = this;
    this.api = {
      version: UI_PROTOCOL,
      videos: () => this.videos(),
      onChange: (fn) => {
        this.#listeners.add(fn);
        return () => this.#listeners.delete(fn);
      },
      send: (msg) => this.#send(msg),
      get invalidated() {
        return self.#invalidated;
      },
      get allowed() {
        return self.#allowed && !self.#invalidated;
      },
      recheck: (id) => {
        const s = this.#list.find((x) => x.id === id);
        return s ? this.#meta(s) : undefined;
      },
      refreshState: () => this.refreshState(),
      claimUi: () => {
        if (this.#uiClaimed || this.#invalidated) return false;
        this.#uiClaimed = true;
        return true;
      },
      get requested() {
        return self.#invalidated ? undefined : self.#requested;
      },
      release: (id) => this.#release(id),
      get tile() {
        const t = self.#tile;
        return !self.#invalidated && t?.el.isConnected ? t : undefined;
      },
      shown: (el) => {
        const s = this.#states.get(el);
        return s?.ratio === undefined ? undefined : s.ratio >= VISIBLE_RATIO;
      },
      lookAheadNow: () => {
        if (!this.#invalidated) this.#dwell?.now?.();
      },
    };
  }

  /** Starts listening; publish `api` on TRACKER_KEY separately (`publish`). */
  start(): void {
    const onMedia = (e: Event) => this.#onMedia(e);
    for (const type of MEDIA_EVENTS) {
      this.#doc.addEventListener(type, onMedia, true);
      this.#cleanup.push(() => this.#doc.removeEventListener(type, onMedia, true));
    }
    const onPointer = (e: Event) => {
      if (!this.#pointed && (this.#deps.isTrusted ?? ((x: Event) => x.isTrusted))(e)) {
        this.#pointed = true;
        // A quiet video may now ask for the overlay UI (§2.5).
        this.#changed(false);
      }
      const now = this.#timers.now();
      if (now - this.#lastPointerCheck < POINTER_CHECK_MS) return;
      this.#lastPointerCheck = now;
      this.check();
    };
    const opts: AddEventListenerOptions = { capture: true, passive: true };
    this.#doc.addEventListener('pointermove', onPointer, opts);
    this.#cleanup.push(() => this.#doc.removeEventListener('pointermove', onPointer, opts));
    // Which video the user right-clicked, for "Show Downex download panel"; never prevented.
    const onMenu = (e: Event) => this.#onContextMenu(e);
    this.#doc.addEventListener('contextmenu', onMenu, opts);
    this.#cleanup.push(() => this.#doc.removeEventListener('contextmenu', onMenu, opts));
    if (this.#compactFrame()) this.#startCompact(opts);
    this.check();
    this.navigated();
  }

  /**
   * A compact social site's top frame (VIDEO-DETECT-SPEC §5.1 "Social sites"): the late-mount
   * observer, whose callback only schedules one check MUTATION_CHECK_MS later, and the grid tiles'
   * `pointerover`.
   */
  #startCompact(opts: AddEventListenerOptions): void {
    const mo = this.#deps.mutations?.(() => {
      if (this.#mutationTimer !== undefined || this.#invalidated) return;
      this.#mutationTimer = this.#timers.setTimeout(() => {
        this.#mutationTimer = undefined;
        this.check();
      }, MUTATION_CHECK_MS);
    });
    if (mo) {
      try {
        mo.observe(this.#doc, { childList: true, subtree: true });
        this.#mutations = mo;
      } catch {
        mo.disconnect();
      }
    }
    if (this.#deps.tileVideo) {
      const onOver = (e: Event) => this.#onPointerOver(e);
      this.#doc.addEventListener('pointerover', onOver, opts);
      this.#cleanup.push(() => this.#doc.removeEventListener('pointerover', onOver, opts));
    }
  }

  /** Whether this is the top frame of a compact social site (`compactHost`). */
  #compactFrame(): boolean {
    if (this.#deps.topFrame !== true || !this.#deps.compactHost) return false;
    try {
      return this.#deps.compactHost((this.#deps.location ?? (() => this.#doc.location?.href ?? ''))());
    } catch {
      return false;
    }
  }

  /**
   * The pointer entered an element (trusted `pointerover`, compact sites' top frames): when it is
   * in a link to a video (`tileVideo`) on the frame's origin, holding no `<video>` with a button of
   * its own and at least MIN_TILE_PX square, that link is the frame's grid tile (§5.1 "Grid tiles"). Only the link's
   * resolved address and its box are read, locally; the overlay UI is asked for as for a quiet video.
   */
  #onPointerOver(e: Event): void {
    if (this.#invalidated || !(this.#deps.isTrusted ?? ((x: Event) => x.isTrusted))(e)) return;
    const path = typeof e.composedPath === 'function' ? e.composedPath() : [e.target];
    let link: Element | undefined;
    for (let i = 0; i < path.length && i < 16; i++) {
      const n = path[i] as Partial<Element> | null;
      if (n && n.localName === 'a') {
        link = n as Element;
        break;
      }
    }
    if (!link || link === this.#lastLink) return;
    this.#lastLink = link;
    if (this.#tile?.el === link) return;
    const href = (link as HTMLAnchorElement).href;
    const url = typeof href === 'string' && href !== '' ? this.#deps.tileVideo?.(href, this.#frameUrl()) : undefined;
    if (!url || this.#holdsButtonVideo(link)) return;
    const r = link.getBoundingClientRect();
    if (!(r.width >= MIN_TILE_PX && r.height >= MIN_TILE_PX)) return;
    this.#tile = { el: link, url };
    this.#emit({ type: 'tile' });
    this.#changed(false);
  }

  /** The frame's address now. */
  #frameUrl(): string {
    return (this.#deps.location ?? (() => this.#doc.location?.href ?? ''))();
  }

  /**
   * Whether a link holds a `<video>` that gets a button of its own (meaningful, or a quiet loop):
   * a hover preview too small for one (under MIN_VIDEO_WIDTH × MIN_VIDEO_HEIGHT) leaves the tile
   * its button (§5.1 "Grid tiles").
   */
  #holdsButtonVideo(link: Element): boolean {
    const videos = link.getElementsByTagName('video');
    for (let i = 0; i < videos.length; i++) {
      const s = videos[i] ? this.#states.get(videos[i] as HTMLVideoElement) : undefined;
      if (!s) continue;
      const x = this.#examine(s);
      if (x.meta.meaningful || x.quiet) return true;
    }
    return false;
  }

  /** Puts the API where the overlay UI script finds it. */
  publish(target: object = globalThis): void {
    Object.defineProperty(target, TRACKER_KEY, { value: this.api, configurable: true, writable: true, enumerable: false });
  }

  get allowed(): boolean {
    return this.#allowed;
  }

  get trackedCount(): number {
    return this.#list.length;
  }

  videos(): TrackedVideo[] {
    return this.#list
      .filter((s) => s.el.isConnected)
      .map((s) => {
        const { meta, quiet } = this.#examine(s);
        const v: TrackedVideo = { id: s.id, el: s.el, meta, quiet };
        if (s.startedAt !== undefined) v.startedAt = s.startedAt;
        return v;
      });
  }

  /**
   * Looks for new videos in the live collection and drops removed ones. At MAX_TRACKED_VIDEOS,
   * videos known to be off-screen make room for untracked ones, taken round the collection from
   * where the last swap stopped: on pages that keep many videos, the one the user scrolls to is
   * tracked within a few checks, also when it does not play. Videos found in shadow roots stay
   * (only a deep scan finds them again).
   */
  check(): void {
    if (this.#invalidated) return;
    let changed = false;
    for (const s of [...this.#list]) {
      if (!s.el.isConnected) {
        this.#untrack(s);
        changed = true;
      }
    }
    const n = this.#collection.length;
    for (let i = 0; i < n && this.#list.length < MAX_TRACKED_VIDEOS; i++) {
      const v = this.#collection[i];
      if (v && !this.#states.has(v)) {
        this.#track(v, false);
        changed = true;
      }
    }
    if (this.#list.length >= MAX_TRACKED_VIDEOS && n > this.#list.length) {
      const victims = this.#list.filter((x) => x.ratio === 0 && !x.shadow);
      for (let k = 0; k < n && victims.length > 0; k++) {
        const i = (this.#cursor + k) % n;
        const v = this.#collection[i];
        if (!v || this.#states.has(v)) continue;
        this.#untrack(victims.shift()!);
        this.#track(v, false);
        this.#cursor = (i + 1) % n;
        changed = true;
      }
    }
    if (changed) this.#changed(true);
  }

  /**
   * The background saw media traffic for this frame while it reported no video: a player may keep
   * its `<video>` in a shadow root. Looks at most at DEEP_SCAN_MAX_ELEMENTS elements and
   * DEEP_SCAN_MAX_DEPTH nested shadow roots. Returns the number of elements looked at.
   */
  deepScan(): number {
    if (this.#invalidated) return 0;
    const shadowOf = this.#deps.shadowRoot ?? ((el: Element) => el.shadowRoot);
    let visited = 0;
    let found = false;
    const stack: { node: ParentNode; depth: number }[] = [{ node: this.#doc, depth: 0 }];
    outer: while (stack.length > 0) {
      const { node, depth } = stack.pop()!;
      const children = node.children;
      for (let i = 0; i < children.length; i++) {
        const el = children[i];
        if (!el) continue;
        if (++visited > DEEP_SCAN_MAX_ELEMENTS) {
          visited = DEEP_SCAN_MAX_ELEMENTS;
          break outer;
        }
        if (el.localName === 'video' && !this.#states.has(el) && this.#list.length < MAX_TRACKED_VIDEOS) {
          this.#track(el as HTMLVideoElement, depth > 0);
          found = true;
        }
        if (el.firstElementChild) stack.push({ node: el, depth });
        let root: ShadowRoot | null | undefined;
        try {
          root = shadowOf(el);
        } catch {
          root = undefined;
        }
        if (root && depth < DEEP_SCAN_MAX_DEPTH) stack.push({ node: root, depth: depth + 1 });
      }
    }
    if (found) this.#changed(true);
    return visited;
  }

  /** A message of the background (validated with isFrameMessage); `downex:show-panel` → `showPanel()`. */
  onFrameMessage(m: FrameMessage): void {
    if (m.type === 'downex:frame-media-seen') this.deepScan();
    else if (m.type === 'downex:show-panel') void this.showPanel();
  }

  /**
   * "Show Downex download panel" (VIDEO-DETECT-SPEC §5.1 "Menu"): the video right-clicked in the
   * last MENU_VIDEO_MS, still in the page, is tracked (room is made for it at the cap), marked
   * requested and its panel opened by the overlay UI, asked for if it was not yet. A right-click
   * that named no video looks under the pointer once more after a deep scan (a closed shadow
   * root's video that never played). False when there is no such video, or the overlay UI could
   * not be put into the frame (asked again on the next request).
   */
  async showPanel(): Promise<boolean> {
    if (this.#invalidated) return false;
    const m = this.#menuVideo;
    this.#menuVideo = undefined;
    if (!m || this.#timers.now() - m.at > MENU_VIDEO_MS) return false;
    let el = m.el;
    if (!el && m.x !== undefined && m.y !== undefined) {
      this.deepScan();
      el = this.#videoAt(m.x, m.y);
    }
    if (!el || !el.isConnected) return false;
    let s = this.#states.get(el);
    if (!s) {
      if (this.#list.length >= MAX_TRACKED_VIDEOS) {
        const victim = this.#list.find((x) => x.ratio === 0) ?? this.#list.find((x) => x.id !== this.#requested);
        if (victim) this.#untrack(victim);
      }
      this.#track(el, el.getRootNode() !== this.#doc);
      s = this.#states.get(el);
      if (!s) return false;
    }
    const id = s.id;
    this.#requested = id;
    this.#changed(true);
    if (!(await this.#askUi())) {
      if (this.#requested === id) this.#requested = undefined;
      return false;
    }
    if (this.#invalidated || this.#requested !== id) return false;
    this.#emit({ type: 'open', id });
    return true;
  }

  /** Asks again whether buttons may show (options changed, "Don't show on <host>"). */
  refreshState(): void {
    if (this.#invalidated || this.#state === 'unknown') return;
    if (this.#state === 'asking') {
      this.#askAgain = true;
      return;
    }
    this.#askState();
  }

  /** The stored video options changed. */
  optionsChanged(o: VideoOptions): void {
    this.#options = o;
    this.refreshState();
    this.#dwell?.sync();
  }

  /** The frame's address may have changed (SPA navigation, back/forward, page cache). */
  navigated(): void {
    if (this.#invalidated) return;
    // A tile the new address no longer takes (a video address, private messages) goes (§5.1 "Grid tiles").
    const tile = this.#tile;
    if (tile && this.#deps.tileVideo?.(tile.url, this.#frameUrl()) === undefined) {
      this.#tile = undefined;
      this.#lastLink = undefined;
      this.#emit({ type: 'tile' });
      this.#changed(false);
    }
    this.#startDwell();
    this.#dwell?.sync();
  }

  /**
   * The page came back from the back/forward cache: the background cleared the tab's reports
   * when it committed, so the frame reports again (also an unchanged list).
   */
  restored(): void {
    if (this.#invalidated) return;
    this.#lastReport = undefined;
    this.#scheduleReport();
    this.navigated();
  }

  /** The tab was hidden or shown again. */
  visibilityChanged(): void {
    this.#dwell?.sync();
  }

  /** The extension went away: everything stops, and the UI removes itself. */
  destroy(): void {
    if (this.#invalidated) return;
    this.#invalidated = true;
    for (const undo of this.#cleanup.splice(0)) undo();
    for (const s of this.#list) this.#untrackListeners(s);
    this.#io?.disconnect();
    this.#ro?.disconnect();
    this.#mutations?.disconnect();
    if (this.#mutationTimer !== undefined) this.#timers.clearTimeout(this.#mutationTimer);
    this.#mutationTimer = undefined;
    this.#tile = undefined;
    if (this.#evalTimer !== undefined) this.#timers.clearTimeout(this.#evalTimer);
    if (this.#reportTimer !== undefined) this.#timers.clearTimeout(this.#reportTimer);
    this.#evalTimer = undefined;
    this.#reportTimer = undefined;
    this.#dwell?.stop();
    this.#emit({ type: 'invalidated' });
    this.#listeners.clear();
  }

  // --- tracking ------------------------------------------------------------------------------

  #track(el: HTMLVideoElement, shadow: boolean): void {
    if (this.#nextId >= MAX_ID) this.#nextId = 0;
    const s: VideoState = { id: ++this.#nextId, el, width: 0, height: 0, ratio: undefined, encrypted: false, shadow, lastDuration: Number.NaN, growths: 0 };
    this.#states.set(el, s);
    this.#list.push(s);
    this.#io?.observe(el);
    this.#ro?.observe(el);
    if (shadow) {
      const onMedia = (e: Event) => this.#onMedia(e);
      for (const type of MEDIA_EVENTS) el.addEventListener(type, onMedia);
      shadowListeners.set(el, onMedia);
    }
  }

  /**
   * A video that is not tracked had a media event (it plays) while MAX_TRACKED_VIDEOS are: it is
   * tracked in place of one known to be off-screen, so the videos in use get buttons on pages
   * that keep many videos. Undefined when every tracked video may be on screen.
   */
  #makeRoom(el: HTMLVideoElement): VideoState | undefined {
    if (!el.isConnected || this.#list.length < MAX_TRACKED_VIDEOS) return undefined;
    const victim = this.#list.find((x) => x.ratio === 0);
    if (!victim) return undefined;
    this.#untrack(victim);
    this.#track(el, false);
    this.#changed(true);
    return this.#states.get(el);
  }

  #untrack(s: VideoState): void {
    if (s.id === this.#requested) this.#requested = undefined;
    this.#untrackListeners(s);
    this.#io?.unobserve(s.el);
    this.#ro?.unobserve(s.el);
    this.#states.delete(s.el);
    this.#list = this.#list.filter((x) => x !== s);
  }

  #untrackListeners(s: VideoState): void {
    const onMedia = shadowListeners.get(s.el);
    if (!onMedia) return;
    for (const type of MEDIA_EVENTS) s.el.removeEventListener(type, onMedia);
    shadowListeners.delete(s.el);
  }

  /**
   * A right-click: its target is kept when it is a `<video>` (an open shadow root's too), else a
   * tracked video under the pointer (one in a closed shadow root, which the event does not name),
   * else the point, which `showPanel` looks under again after a deep scan. With `showPanel`, the
   * only place the tracker reads layout.
   */
  #onContextMenu(e: Event): void {
    if (this.#invalidated || !(this.#deps.isTrusted ?? ((x: Event) => x.isTrusted))(e)) return;
    const path = typeof e.composedPath === 'function' ? e.composedPath() : [];
    const t = (path[0] ?? e.target) as Element | null;
    const at = this.#timers.now();
    if (t && t.localName === 'video') {
      this.#menuVideo = { el: t as HTMLVideoElement, at };
      return;
    }
    const { clientX: x, clientY: y } = e as MouseEvent;
    if (typeof x !== 'number' || typeof y !== 'number') {
      this.#menuVideo = undefined;
      return;
    }
    const el = this.#videoAt(x, y);
    this.#menuVideo = el ? { el, at } : { x, y, at };
  }

  /** The tracked video whose box holds the point (client coordinates). */
  #videoAt(x: number, y: number): HTMLVideoElement | undefined {
    return this.#list.find((s) => {
      if (!s.el.isConnected) return false;
      const r = s.el.getBoundingClientRect();
      return x >= r.left && x <= r.right && y >= r.top && y <= r.bottom && r.width > 0 && r.height > 0;
    })?.el;
  }

  /**
   * Asks the background for the overlay UI once per frame (`downex:video-ui`); a refusal or no
   * answer lets the next request ask again.
   */
  #askUi(): Promise<boolean> {
    if (!this.#ui) {
      const asking = this.#send({ type: 'downex:video-ui' }).then((answer) => answer?.ok === true);
      this.#ui = asking;
      void asking.then((ok) => {
        if (!ok && this.#ui === asking) this.#ui = undefined;
      });
    }
    return this.#ui;
  }

  /** The requested video's panel closed; the overlay UI hears it in a moment (not from inside its close). */
  #release(id: number): void {
    if (this.#invalidated || this.#requested !== id) return;
    this.#requested = undefined;
    this.#timers.setTimeout(() => {
      if (this.#invalidated) return;
      this.#emit({ type: 'state' });
      this.#emit({ type: 'videos' });
    }, 0);
  }

  #onMedia(e: Event): void {
    if (this.#invalidated) return;
    const t = e.target as Element | null;
    if (!t || t.localName !== 'video') return;
    // Every media event looks at the live collection again: a new or removed video.
    this.check();
    const s = this.#states.get(t) ?? this.#makeRoom(t as HTMLVideoElement);
    if (!s) return;
    if (e.type === 'encrypted' || e.type === 'waitingforkey') s.encrypted = true;
    if (e.type === 'emptied' || e.type === 'loadstart') {
      s.lastDuration = Number.NaN;
      s.growths = 0;
    }
    if (e.type === 'durationchange') {
      const d = s.el.duration;
      if (typeof d === 'number' && Number.isFinite(d) && d > 0) {
        if (d >= s.lastDuration + LIVE_GROWTH_S) s.growths++;
        s.lastDuration = d;
      }
    }
    if (e.type === 'play' || e.type === 'loadedmetadata') {
      s.startedAt = this.#timers.now();
      this.#emit({ type: 'reveal', id: s.id });
    }
    if (e.type === 'emptied') this.#emit({ type: 'emptied', id: s.id });
    this.#changed(false);
  }

  #onIntersection(entries: readonly IntersectionEntryLike[]): void {
    for (const e of entries) {
      const s = this.#states.get(e.target);
      if (!s) continue;
      s.ratio = e.intersectionRatio;
      s.width = e.boundingClientRect.width;
      s.height = e.boundingClientRect.height;
    }
    this.#changed(false);
  }

  #onResize(entries: readonly ResizeEntryLike[]): void {
    for (const e of entries) {
      const s = this.#states.get(e.target);
      if (!s) continue;
      const box = e.borderBoxSize?.[0];
      s.width = box ? box.inlineSize : e.contentRect.width;
      s.height = box ? box.blockSize : e.contentRect.height;
    }
    this.#changed(false);
  }

  // --- state of one video ----------------------------------------------------------------------

  #meta(s: VideoState): FrameVideoMeta {
    return this.#examine(s).meta;
  }

  /** Whether the top frame's own address is on a curated site's host (§2.5), asked once per address. */
  #curatedFrame(): boolean {
    const ask = this.#deps.curatedHost;
    if (!ask || this.#deps.topFrame !== true) return false;
    const url = (this.#deps.location ?? (() => this.#doc.location?.href ?? ''))();
    if (this.#curatedFor?.url !== url) {
      let curated = false;
      try {
        curated = ask(url);
      } catch {
        curated = false;
      }
      this.#curatedFor = { url, curated };
    }
    return this.#curatedFor.curated;
  }

  /**
   * The video's state (VIDEO-DETECT-SPEC §2.5), and whether it is `notable`: a video the user
   * may be watching (it has media, no camera stream, is visible, large enough and not a
   * background loop), worth reporting even when protected or live. `quiet`: a background loop
   * that would otherwise be meaningful, off the curated hosts (its mark shows on hover only).
   */
  #examine(s: VideoState): { meta: FrameVideoMeta; notable: boolean; quiet: boolean } {
    const v = s.el;
    const current = typeof v.currentSrc === 'string' ? v.currentSrc : '';
    const http = /^https?:\/\//i.test(current);
    const src = http ? withoutFragment(current) : '';
    const duration = typeof v.duration === 'number' ? v.duration : Number.NaN;
    const live = duration === Number.POSITIVE_INFINITY || s.growths >= LIVE_GROWTHS;
    const durationMs = live ? -2 : Number.isFinite(duration) && duration >= 0 ? Math.min(Number.MAX_SAFE_INTEGER, Math.round(duration * 1000)) : -1;
    let keys: unknown;
    try {
      keys = (this.#deps.mediaKeys ?? ((x: HTMLVideoElement) => (x as { mediaKeys?: unknown }).mediaKeys))(v);
    } catch {
      keys = undefined;
    }
    const drm = s.encrypted || (keys !== null && keys !== undefined);
    const visible = this.#io ? (s.ratio ?? 0) >= VISIBLE_RATIO : true;
    const sized = s.width >= MIN_VIDEO_WIDTH && s.height >= MIN_VIDEO_HEIGHT;
    const loop = v.muted && v.loop && !v.controls && durationMs >= 0 && durationMs < DECORATIVE_MAX_MS;
    // On a curated site's host such loops are the site's videos (TikTok, Instagram, X, Pinterest).
    const decorative = loop && !this.#curatedFrame();
    // A MediaStream (a camera, a call, a screen) is never a button's; any other source object is
    // Media Source Extensions run from a worker (a MediaSourceHandle: Facebook's players), an MSE
    // player like a `blob:` one (§2.5, amended 2026-11-18).
    const object = (v as { srcObject?: unknown }).srcObject;
    const stream = object != null && typeof (object as { getTracks?: unknown }).getTracks === 'function';
    const handle = object != null && !stream;
    const source = http || /^blob:/i.test(current) || (v.readyState ?? 0) >= 1;
    const free = !stream;
    const watchable = v.isConnected && free && source && sized && visible;
    const notable = watchable && !decorative;
    return {
      meta: {
        id: s.id,
        src: src.length <= MAX_SRC_CHARS ? src : '',
        mse: /^blob:/i.test(current) || handle,
        width: count(s.width, 1_000_000),
        height: count(s.height, 1_000_000),
        videoWidth: count(v.videoWidth, 65_535),
        videoHeight: count(v.videoHeight, 65_535),
        durationMs,
        drm,
        visible,
        decorative,
        meaningful: notable && !live && !drm,
      },
      notable,
      quiet: watchable && decorative && !live && !drm,
    };
  }

  // --- evaluation, reports and the UI requests ---------------------------------------------------

  #changed(newVideo: boolean): void {
    if (newVideo) this.#dwell?.sync();
    if (this.#evalTimer !== undefined || this.#invalidated) return;
    this.#evalTimer = this.#timers.setTimeout(() => {
      this.#evalTimer = undefined;
      this.#evaluate();
    }, 0);
  }

  #evaluate(): void {
    if (this.#invalidated) return;
    // A removed video is let go (its IntersectionObserver entry usually brings us here).
    for (const s of this.#list) if (!s.el.isConnected) this.#untrack(s);
    const examined = this.#list.map((s) => this.#examine(s));
    // A quiet video asks for the UI only once the pointer moved in the frame (§2.5); a grid tile
    // only exists once the pointer entered it (§5.1 "Grid tiles").
    if (this.#tile && !this.#tile.el.isConnected) this.#tile = undefined;
    const meaningful = examined.some((x) => x.meta.meaningful || (x.quiet && this.#pointed)) || this.#tile !== undefined;
    // Reports start with the first video the user may be watching, protected or live included
    // (they drive the popup's "Protected" and the badge); pages without one cost nothing.
    if (!this.#active) this.#active = examined.some((x) => x.notable);
    if (meaningful && this.#state === 'unknown') this.#askState();
    if (meaningful && this.#allowed && !this.#uiAskedOnce) {
      this.#uiAskedOnce = true;
      void this.#askUi();
    }
    this.#emit({ type: 'videos' });
    this.#scheduleReport();
  }

  #askState(): void {
    this.#state = 'asking';
    this.#askAgain = false;
    void this.#send({ type: 'downex:overlay-state' }).then((answer) => {
      if (this.#invalidated) return;
      if (this.#askAgain) {
        this.#askState();
        return;
      }
      this.#state = 'known';
      const allowed = answer?.button === true;
      const changed = allowed !== this.#allowed;
      this.#allowed = allowed;
      if (changed) this.#emit({ type: 'state' });
      this.#changed(false);
    });
  }

  #scheduleReport(): void {
    if (!this.#active || this.#reportTimer !== undefined || this.#invalidated) return;
    const wait = Math.max(0, this.#lastReportAt + REPORT_INTERVAL_MS - this.#timers.now());
    this.#reportTimer = this.#timers.setTimeout(() => {
      this.#reportTimer = undefined;
      this.#report();
    }, wait);
  }

  /** The videos a report lists: meaningful first, then visible, then the largest; within the limits. */
  reportVideos(): FrameVideoMeta[] {
    const metas = this.videos()
      .map((v) => v.meta)
      .sort((a, b) => Number(b.meaningful) - Number(a.meaningful) || Number(b.visible) - Number(a.visible) || b.width * b.height - a.width * a.height)
      .slice(0, MAX_FRAME_VIDEOS);
    while (metas.length > 0 && JSON.stringify(metas).length > MAX_FRAME_VIDEOS_BYTES) metas.pop();
    return metas;
  }

  #report(): void {
    if (this.#invalidated) return;
    const videos = this.reportVideos();
    const json = JSON.stringify(videos);
    if (json === this.#lastReport) return;
    this.#lastReport = json;
    this.#lastReportAt = this.#timers.now();
    void this.#send({ type: 'downex:frame-videos', videos });
  }

  // --- look-ahead dwell (top frame of a curated video address) ---------------------------------

  #startDwell(): void {
    if (this.#dwell || !this.#deps.topFrame || !this.#deps.dwell || !this.#deps.lookAheadVideo) return;
    const location = this.#deps.location ?? (() => '');
    if (!this.#deps.lookAheadVideo(location())) return;
    this.#dwell = this.#deps.dwell({
      read: () => {
        const url = location();
        const o = this.#options;
        return {
          url,
          visible: this.#deps.visible?.() ?? true,
          enabled: o !== undefined && lookAheadAllowed(o) && buttonAllowed(o, hostOf(url)),
        };
      },
      fire: (url) => void this.#send({ type: 'downex:page-video-prefetch', url }),
      timers: this.#timers,
    });
    if (this.#options === undefined && this.#deps.readOptions) {
      void this.#deps.readOptions().then(
        (o) => {
          if (this.#invalidated) return;
          this.#options ??= o;
          this.#dwell?.sync();
        },
        () => undefined,
      );
    }
  }

  // --- messaging --------------------------------------------------------------------------------

  async #send<M extends ExtensionMessage>(msg: M): Promise<ResponseFor[M['type']] | undefined> {
    if (this.#invalidated) return undefined;
    try {
      return (await this.#deps.send(msg)) as ResponseFor[M['type']] | undefined;
    } catch {
      // The extension was reloaded or updated: this script is orphaned.
      return undefined;
    }
  }

  #emit(e: TrackerEvent): void {
    for (const fn of [...this.#listeners]) {
      try {
        fn(e);
      } catch {
        // a listener's failure never stops the tracker
      }
    }
  }
}

/** The listeners of videos found in shadow roots, to remove them again. */
const shadowListeners = new WeakMap<Element, (e: Event) => void>();
