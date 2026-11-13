// The layer of Download buttons over the videos of one frame (VIDEO-DETECT-SPEC §5.1): one
// fixed-position host appended to `document.documentElement` with a closed shadow root, so
// nothing is inserted into the site's player and its layout and event handlers stay untouched.
// The host lets the pointer through except over the buttons and the panel. Each button sits at
// its video's top-right corner (the mark alone on small videos), placed from the video's
// rectangle in `requestAnimationFrame`; at most MAX_BUTTONS per frame, the largest videos. A page
// can move a video without any event the layer sees (content inserted above it), so pointer
// movement places the buttons again before it reveals one, every frame while the panel is open,
// and a button not shown lets clicks through to the page. The layer follows the tracker
// (TrackerApi): it mounts while buttons are allowed for the tab's site, puts its host back when
// the page removed it, and removes itself for good when the extension goes away. Styles are
// adopted sheets, with a `<style>` element where constructable sheets are missing or refused.

import type { ExtensionMessage, OfferAddMessage, ResponseFor } from './messages';
import type { FrameOffers } from './offer-types';
import { OfferPanel, OFFER_PANEL_CSS, OfferPanelDom, type OfferPanelView } from './offer-panel';
import { failure, type Outcome, systemTimers, type Timers } from './protocol';
import {
  BUTTON_LABEL,
  downexMark,
  isolateEvents,
  OVERLAY_SHARED_CSS,
  REVEAL_ACTIVITY_MS,
  REVEAL_LOAD_MS,
  SENT_CLOSE_MS,
} from './video-overlay';
import { isTrackerApi, type TrackedVideo, type TrackerApi } from './video-tracker';

/** Most buttons in one frame: the largest visible videos. */
export const MAX_BUTTONS = 8;
/** Distance of a button from its video's top-right corner … */
export const BUTTON_INSET_PX = 12;
/** … and of the mark alone on a small video. */
export const COMPACT_INSET_PX = 8;
/** Below this video size the button is the mark alone (32 × 32 px). */
export const COMPACT_WIDTH_PX = 360;
export const COMPACT_HEIGHT_PX = 200;
/** Below this height left under its button, the panel is placed over the button instead. */
const MIN_PANEL_PX = 120;
/** The layer's element name: a custom name, so page rules for common tags do not reach it. */
export const LAYER_TAG = 'downex-video-layer';

/** The layer's `:host`: fixed over the viewport, the pointer passes through, above the page. */
export const LAYER_HOST_CSS = `
:host {
  all: initial !important;
  position: fixed !important;
  inset: 0 !important;
  display: block !important;
  pointer-events: none !important;
  z-index: 2147483647 !important;
  contain: layout !important;
  overflow: visible !important;
}
`;

/**
 * The layer's own rules after the shared ones: per-button reveal (the glass blurs only while the
 * button is shown), the small form, the mint orb alone in a 32 px disc, and a button its panel
 * covers (a short frame), hidden so that its halo does not show past the panel's corner.
 */
export const LAYER_CSS = `
.dx-button[data-shown='true'] { opacity: 1; transform: none; backdrop-filter: var(--b-blur); }
.dx-button[data-shown='false']:not(:focus-visible):not([aria-expanded='true']) { pointer-events: none; }
.dx-button[hidden] { display: none; }
.dx-button[data-covered='true'] { visibility: hidden; }
.dx-button[data-compact='true'] { gap: 0; width: 32px; padding: 0; justify-content: center; }
.dx-button[data-compact='true'] .dx-label { display: none; }
`;

/** Everything the layer's shadow root uses. */
export const LAYER_STYLE = LAYER_HOST_CSS + OVERLAY_SHARED_CSS + LAYER_CSS + OFFER_PANEL_CSS;

export interface RectLike {
  left: number;
  top: number;
  right: number;
  bottom: number;
  width: number;
  height: number;
}

/** Where a button goes, in the layer's (the viewport's) coordinates. */
export interface Placement {
  top: number;
  /** From the viewport's right edge. */
  right: number;
  compact: boolean;
}

/**
 * The button of a video at `rect` in a `width` × `height` viewport: at the top-right corner of
 * the video's visible part, inset 12 px (8 px for the mark alone below 360 × 200 px of video).
 * Undefined when no part of the video is inside the viewport.
 */
export function placeButton(rect: RectLike, width: number, height: number): Placement | undefined {
  const left = Math.max(rect.left, 0);
  const right = Math.min(rect.right, width);
  const top = Math.max(rect.top, 0);
  const bottom = Math.min(rect.bottom, height);
  if (!(right > left) || !(bottom > top)) return undefined;
  const compact = rect.width < COMPACT_WIDTH_PX || rect.height < COMPACT_HEIGHT_PX;
  const inset = compact ? COMPACT_INSET_PX : BUTTON_INSET_PX;
  return { top: Math.round(top + inset), right: Math.round(width - right + inset), compact };
}

/** The videos that get a button: meaningful ones (visible included), the largest first, at most `max`. */
export function buttonVideos(videos: readonly TrackedVideo[], max = MAX_BUTTONS): TrackedVideo[] {
  return videos
    .filter((v) => v.meta.meaningful)
    .sort((a, b) => b.meta.width * b.meta.height - a.meta.width * a.meta.height || a.id - b.id)
    .slice(0, max);
}

/**
 * Puts `css` into `shadow`: an adopted constructable sheet, else (no constructable sheets, or
 * assigning them throws, as across Firefox's content-script X-rays) a `<style>` element.
 */
export function applyStyles(shadow: ShadowRoot, css: string, sheet?: (css: string) => CSSStyleSheet): 'adopted' | 'style' {
  try {
    const make =
      sheet ??
      ((text: string) => {
        const s = new CSSStyleSheet();
        s.replaceSync(text);
        return s;
      });
    shadow.adoptedStyleSheets = [make(css)];
    if (shadow.adoptedStyleSheets.length !== 1) throw new Error('not adopted');
    return 'adopted';
  } catch {
    const style = (shadow.ownerDocument ?? document).createElement('style');
    style.textContent = css;
    shadow.prepend(style);
    return 'style';
  }
}

export interface OverlayLayerDeps {
  doc: Document;
  api: TrackerApi;
  timers?: Timers;
  /** `requestAnimationFrame`. */
  raf?: (cb: () => void) => void;
  /** Whether an activation came from the user (`event.isTrusted`; tests substitute it). */
  isTrusted?: (e: Event) => boolean;
  /** Makes a constructable sheet (tests; the default is `new CSSStyleSheet()`). */
  sheet?: (css: string) => CSSStyleSheet;
  /** The frame's address. */
  location?: () => string;
  /** Turns on the sign-in option (curated sites' `auth_required`). */
  allowSignIn?: () => Promise<void>;
  signInAllowed?: () => boolean;
}

interface ButtonDom {
  id: number;
  video: HTMLVideoElement;
  button: HTMLButtonElement;
  revealTimer: unknown;
}

/** The URL without its fragment. */
function frameAddress(url: string): string {
  const i = url.indexOf('#');
  return i < 0 ? url : url.slice(0, i);
}

/** The buttons and the panel of one frame. */
export class OverlayLayer {
  readonly host: HTMLElement;
  readonly root: HTMLElement;
  readonly panel: OfferPanel;
  readonly panelDom: OfferPanelDom;
  readonly styleMode: 'adopted' | 'style';
  readonly #doc: Document;
  readonly #deps: OverlayLayerDeps;
  readonly #api: TrackerApi;
  readonly #timers: Timers;
  readonly #isTrusted: (e: Event) => boolean;
  readonly #buttons = new Map<number, ButtonDom>();
  /** The rectangles of the last placement, for the pointer's reveal. */
  readonly #rects = new Map<number, RectLike>();
  readonly #cleanup: (() => void)[] = [];
  #mounted = false;
  #destroyed = false;
  #placing = false;
  /** The video the panel is for, and the address it was opened on. */
  #panelFor: number | undefined;
  #openedAt = '';
  #sentTimer: unknown;
  /** The pointer's position to check against the rectangles of the next placement. */
  #pointer: { x: number; y: number } | undefined;

  constructor(deps: OverlayLayerDeps) {
    this.#deps = deps;
    this.#doc = deps.doc;
    this.#api = deps.api;
    this.#timers = deps.timers ?? systemTimers;
    this.#isTrusted = deps.isTrusted ?? ((e) => e.isTrusted);
    const doc = this.#doc;

    this.host = doc.createElement(LAYER_TAG);
    const shadow = this.host.attachShadow({ mode: 'closed' });
    this.styleMode = applyStyles(shadow, LAYER_STYLE, deps.sheet);
    isolateEvents(shadow);
    this.root = doc.createElement('div');
    this.root.className = 'dx';
    this.root.dataset.layer = 'true';
    shadow.append(this.root);

    const location = deps.location ?? (() => doc.location?.href ?? '');
    const ask = async <M extends ExtensionMessage>(msg: M): Promise<ResponseFor[M['type']]> => {
      const answer = await this.#api.send(msg);
      if (answer !== undefined) return answer;
      return failure('disconnected', 'Downex’s extension is not available. Reload the page.') as ResponseFor[M['type']];
    };
    this.panelDom = new OfferPanelDom({
      doc,
      isTrusted: this.#isTrusted,
      onRow: (id) => void this.panel.download(id),
      onGroup: (id) => this.panel.toggleGroup(id),
      onAudio: (id) => this.panel.chooseAudio(id),
      onAction: () => this.panel.siteAction(),
      onLookForMore: () => this.panel.lookForMore(),
      onHide: () => void this.#hide(),
      onClose: (focusButton) => this.closePanel(focusButton),
    });
    const panelDeps: ConstructorParameters<typeof OfferPanel>[0] = {
      offers: async (src, mse): Promise<Outcome<FrameOffers>> => {
        // The frame moved on (a single-page app's navigation): the panel's items are gone.
        if (frameAddress(location()) !== this.#openedAt) {
          this.closePanel(false);
          return failure('expired', 'The page changed.');
        }
        return ask({ type: 'downex:frame-offers', src, mse });
      },
      streamProbe: (key) => ask({ type: 'downex:stream-probe', key }),
      offerAdd: (key, pick) => {
        const msg: OfferAddMessage = { type: 'downex:offer-add', key };
        if (pick.video) msg.videoTrack = pick.video;
        if (pick.audio) msg.audioTrack = pick.audio;
        return ask(msg);
      },
      siteProbe: () => ask({ type: 'downex:page-video-probe', url: frameAddress(location()) }),
      siteAdd: (_site, pick) =>
        ask({ type: 'downex:page-video-add', url: frameAddress(location()), videoTrack: pick.video, audioTrack: pick.audio }),
      hideSite: () => this.#api.send({ type: 'downex:set-site-hidden', hidden: true }),
      render: (view) => this.#render(view),
      timers: this.#timers,
      viewport: () => {
        const w = doc.defaultView;
        return { width: w?.innerWidth ?? 0, height: w?.innerHeight ?? 0 };
      },
    };
    if (deps.allowSignIn) panelDeps.allowSignIn = deps.allowSignIn;
    if (deps.signInAllowed) panelDeps.signInAllowed = deps.signInAllowed;
    this.panel = new OfferPanel(panelDeps);
    this.root.append(this.panelDom.el);
    // Light dismiss (Esc, a click elsewhere) of the popover closes the panel too.
    this.panelDom.el.addEventListener('toggle', (e) => {
      if ((e as Event & { newState?: string }).newState === 'closed' && this.panel.isOpen) this.closePanel(false);
    });
  }

  get mounted(): boolean {
    return this.#mounted;
  }

  /** The buttons by video id. */
  get buttons(): ReadonlyMap<number, HTMLButtonElement> {
    return new Map([...this.#buttons].map(([id, b]) => [id, b.button]));
  }

  /** Shows the layer (buttons allowed for the tab's site). */
  mount(): void {
    if (this.#mounted || this.#destroyed) return;
    this.#mounted = true;
    this.#parent().append(this.host);
    const win = this.#doc.defaultView;
    const listen = (target: EventTarget | null | undefined, type: string, fn: (e: Event) => void, opts?: AddEventListenerOptions) => {
      if (!target) return;
      target.addEventListener(type, fn, opts);
      this.#cleanup.push(() => target.removeEventListener(type, fn, opts));
    };
    const passive = { capture: true, passive: true };
    listen(win, 'scroll', () => this.schedulePlace(), passive);
    listen(win, 'resize', () => this.schedulePlace(), { passive: true });
    listen(this.#doc, 'fullscreenchange', () => this.#onFullscreen());
    listen(this.#doc, 'pointermove', (e) => this.#onPointer(e as PointerEvent), passive);
    listen(win, 'pagehide', () => this.closePanel(false));
    if (!this.#popoverApi) {
      // Without the Popover API a click outside the layer closes the panel.
      listen(
        this.#doc,
        'pointerdown',
        (e) => {
          if (this.panel.isOpen && !e.composedPath().includes(this.root)) this.closePanel(false);
        },
        { capture: true },
      );
    }
    this.sync();
  }

  /** Hides the layer (buttons not allowed); it can mount again. */
  unmount(): void {
    if (!this.#mounted) return;
    this.closePanel(false);
    this.#mounted = false;
    for (const undo of this.#cleanup.splice(0)) undo();
    for (const b of this.#buttons.values()) this.#dropButton(b);
    this.#buttons.clear();
    this.#rects.clear();
    this.host.remove();
  }

  /** The extension went away: the layer is removed for good. */
  destroy(): void {
    this.unmount();
    this.#destroyed = true;
    this.panel.reset();
  }

  /** The tracked videos changed: buttons are added, kept or removed. */
  sync(): void {
    if (!this.#mounted) return;
    this.#reattach();
    const videos = this.#api.videos();
    const chosen = buttonVideos(videos);
    const keep = new Set(chosen.map((v) => v.id));
    // The video of an open panel keeps its button while it is in the page.
    const open = this.panel.isOpen ? this.#panelFor : undefined;
    if (open !== undefined) {
      const v = videos.find((x) => x.id === open);
      if (v?.el.isConnected) keep.add(open);
      else this.closePanel(false);
    }
    for (const [id, b] of [...this.#buttons]) {
      if (keep.has(id)) continue;
      this.#dropButton(b);
      this.#buttons.delete(id);
      this.#rects.delete(id);
    }
    for (const v of chosen) if (!this.#buttons.has(v.id)) this.#addButton(v);
    this.schedulePlace();
  }

  /** Shows a button for `ms` (pointer activity over its video, a video starting). */
  reveal(id: number, ms: number): void {
    const b = this.#buttons.get(id);
    if (!b) return;
    b.button.dataset.shown = 'true';
    if (b.revealTimer !== undefined) this.#timers.clearTimeout(b.revealTimer);
    b.revealTimer = this.#timers.setTimeout(() => {
      b.revealTimer = undefined;
      if (!(this.panel.isOpen && this.#panelFor === id)) b.button.dataset.shown = 'false';
    }, ms);
  }

  /** A video lost its media: its panel closes. */
  emptied(id: number): void {
    if (this.panel.isOpen && this.#panelFor === id) this.closePanel(false);
  }

  /** Places the buttons (and the open panel) in the next animation frame. */
  schedulePlace(): void {
    if (!this.#mounted) return;
    this.#reattach();
    if (this.#placing) return;
    this.#placing = true;
    const raf = this.#deps.raf ?? ((cb: () => void) => void (this.#doc.defaultView ?? globalThis).requestAnimationFrame(cb));
    raf(() => {
      this.#placing = false;
      this.place();
    });
  }

  /**
   * Reads the videos' rectangles and moves the buttons (call in an animation frame); then reveals
   * the buttons of the videos under the pointer's last position. While the panel is open, it
   * places again in the next frame.
   */
  place(): void {
    if (!this.#mounted) return;
    const { width, height } = this.#viewport();
    for (const b of this.#buttons.values()) {
      const rect = b.video.getBoundingClientRect();
      const p = placeButton(rect, width, height);
      b.button.hidden = p === undefined;
      if (!p) {
        this.#rects.delete(b.id);
        continue;
      }
      this.#rects.set(b.id, rect);
      b.button.style.top = `${p.top}px`;
      b.button.style.right = `${p.right}px`;
      b.button.dataset.compact = String(p.compact);
    }
    const pointer = this.#pointer;
    this.#pointer = undefined;
    if (pointer) {
      for (const [id, r] of this.#rects) {
        if (pointer.x >= r.left && pointer.x <= r.right && pointer.y >= r.top && pointer.y <= r.bottom) this.reveal(id, REVEAL_ACTIVITY_MS);
      }
    }
    if (this.panel.isOpen) {
      this.#positionPanel();
      this.schedulePlace();
    }
  }

  /** Opens the panel of the video `id` (its button was activated). */
  openPanel(id: number): void {
    const b = this.#buttons.get(id);
    if (!b || !this.#mounted) return;
    // DRM and live are checked again now: mediaKeys can be set after the button appeared.
    const meta = this.#api.recheck(id);
    if (!meta) return;
    if (this.#panelFor !== id) {
      if (this.panel.isOpen) this.closePanel(false);
      this.panel.reset();
      this.#panelFor = id;
    }
    for (const other of this.#buttons.values()) other.button.setAttribute('aria-expanded', String(other === b));
    b.button.dataset.shown = 'true';
    this.#openedAt = frameAddress(this.#deps.location?.() ?? this.#doc.location?.href ?? '');
    this.panelDom.shown = true;
    if (this.#popoverApi) {
      try {
        (this.panelDom.el as HTMLElement & { showPopover(): void }).showPopover();
      } catch {
        // not connected yet, or already shown
      }
    }
    this.panel.open({ src: meta.src, mse: meta.mse, drm: meta.drm, live: meta.durationMs === -2 });
    this.#positionPanel();
    this.schedulePlace(); // and again in every frame while it is open
    this.panelDom.focusInside();
  }

  /** Closes the panel; `focusButton` returns the focus to its button (Esc, Close). */
  closePanel(focusButton: boolean): void {
    if (this.#sentTimer !== undefined) this.#timers.clearTimeout(this.#sentTimer);
    this.#sentTimer = undefined;
    for (const b of this.#buttons.values()) delete b.button.dataset.covered;
    if (!this.panel.isOpen && !this.panelDom.shown) return;
    this.panel.close();
    this.panelDom.shown = false;
    if (this.#popoverApi) {
      try {
        (this.panelDom.el as HTMLElement & { hidePopover(): void }).hidePopover();
      } catch {
        // already hidden
      }
    }
    const id = this.#panelFor;
    const b = id === undefined ? undefined : this.#buttons.get(id);
    b?.button.setAttribute('aria-expanded', 'false');
    if (b && focusButton) b.button.focus();
    if (id !== undefined) this.reveal(id, REVEAL_ACTIVITY_MS);
  }

  // --- internals -------------------------------------------------------------------------------

  get #popoverApi(): boolean {
    return typeof (this.panelDom.el as Partial<{ showPopover: unknown }>).showPopover === 'function';
  }

  /** The fullscreen element when it is not the video itself (which shows only the browser's controls). */
  #parent(): Element {
    const fs = this.#doc.fullscreenElement;
    return fs && fs.localName !== 'video' ? fs : this.#doc.documentElement;
  }

  #viewport(): { width: number; height: number } {
    const el = this.#doc.documentElement;
    const win = this.#doc.defaultView;
    return { width: el.clientWidth || win?.innerWidth || 0, height: el.clientHeight || win?.innerHeight || 0 };
  }

  #addButton(v: TrackedVideo): void {
    const doc = this.#doc;
    const button = doc.createElement('button');
    button.type = 'button';
    button.className = 'dx-button';
    button.setAttribute('aria-label', BUTTON_LABEL);
    button.setAttribute('aria-haspopup', 'dialog');
    button.setAttribute('aria-expanded', 'false');
    button.setAttribute('aria-controls', this.panelDom.el.id);
    button.dataset.shown = 'false';
    button.dataset.video = String(v.id);
    const label = doc.createElement('span');
    label.className = 'dx-label';
    label.textContent = 'Download';
    button.append(downexMark(doc), label);
    const id = v.id;
    button.addEventListener('click', (e) => {
      if (!this.#isTrusted(e)) return;
      if (this.panel.isOpen && this.#panelFor === id) this.closePanel(true);
      else this.openPanel(id);
    });
    this.root.insertBefore(button, this.panelDom.el);
    this.#buttons.set(id, { id, video: v.el, button, revealTimer: undefined });
    // A video that started (or loaded) before its button existed shows it for the rest of the time.
    const since = v.startedAt === undefined ? Number.POSITIVE_INFINITY : this.#timers.now() - v.startedAt;
    if (since >= 0 && since < REVEAL_LOAD_MS) this.reveal(id, REVEAL_LOAD_MS - since);
  }

  /** The page removed the host (it rewrote the children of <html>): it goes back. */
  #reattach(): void {
    if (this.#mounted && !this.host.isConnected) this.#parent().append(this.host);
  }

  #dropButton(b: ButtonDom): void {
    if (b.revealTimer !== undefined) this.#timers.clearTimeout(b.revealTimer);
    b.button.remove();
  }

  /** Pointer movement: the buttons are placed again, then those under the pointer are revealed. */
  #onPointer(e: PointerEvent): void {
    const x = e.clientX;
    const y = e.clientY;
    if (typeof x !== 'number' || typeof y !== 'number') return;
    this.#pointer = { x, y };
    this.schedulePlace();
  }

  #onFullscreen(): void {
    this.closePanel(false);
    const parent = this.#parent();
    if (this.host.parentNode !== parent) parent.append(this.host);
    this.schedulePlace();
  }

  /** Places the panel under its button, right-aligned and kept inside the frame. */
  #positionPanel(): void {
    const id = this.#panelFor;
    const b = id === undefined ? undefined : this.#buttons.get(id);
    if (!b) return;
    const { width: vw, height: vh } = this.#viewport();
    const r = b.button.getBoundingClientRect();
    const width = Math.max(0, Math.min(320, vw - 16));
    const left = Math.max(8, Math.min(r.right - width, vw - width - 8));
    let top = Math.max(8, r.bottom + 8);
    // A short frame: the panel goes over the button rather than out of the frame (the button is
    // hidden under it meanwhile, see LAYER_CSS).
    const over = vh > 0 && vh - top - 8 < MIN_PANEL_PX;
    if (over) top = 8;
    if (over) b.button.dataset.covered = 'true';
    else delete b.button.dataset.covered;
    const el = this.panelDom.el;
    el.style.left = `${Math.round(left)}px`;
    el.style.top = `${Math.round(top)}px`;
    const maxHeight = vh > 0 ? `${Math.max(0, Math.round(vh - top - 8))}px` : el.style.maxHeight;
    if (el.style.maxHeight !== maxHeight) {
      el.style.maxHeight = maxHeight;
      this.panelDom.updateOverflow();
    }
  }

  #render(view: OfferPanelView): void {
    this.panelDom.render(view);
    if (view.phase === 'sent' && this.panel.isOpen && this.#sentTimer === undefined) {
      this.#sentTimer = this.#timers.setTimeout(() => {
        this.#sentTimer = undefined;
        // The focus goes back to the button when it was in the panel, never to the page.
        const root = this.panelDom.el.getRootNode() as ShadowRoot | Document;
        const inPanel = root.activeElement !== null && this.panelDom.el.contains(root.activeElement);
        this.closePanel(inPanel);
      }, SENT_CLOSE_MS);
    }
  }

  async #hide(): Promise<void> {
    const hidden = await this.panel.hide();
    // The tracker asks `downex:overlay-state` again; buttons not allowed unmount the layer.
    if (hidden) this.#api.refreshState();
  }
}

/**
 * Starts the layer for the tracker's API: mounted while buttons are allowed, unmounted when not,
 * removed for good when the extension goes away. Undefined when the API is missing, of another
 * protocol version, invalidated, or already has a layer.
 */
export function startOverlayUi(api: unknown, deps: Omit<OverlayLayerDeps, 'api'>): OverlayLayer | undefined {
  if (!isTrackerApi(api) || api.invalidated || !api.claimUi()) return undefined;
  const layer = new OverlayLayer({ ...deps, api });
  const apply = () => {
    if (api.allowed) layer.mount();
    else layer.unmount();
  };
  const off = api.onChange((e) => {
    switch (e.type) {
      case 'invalidated':
        off();
        layer.destroy();
        return;
      case 'state':
        apply();
        return;
      case 'videos':
        layer.sync();
        return;
      case 'reveal':
        layer.reveal(e.id, REVEAL_LOAD_MS);
        return;
      case 'emptied':
        layer.emptied(e.id);
        return;
    }
  });
  apply();
  return layer;
}
