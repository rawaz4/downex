// The layer of Download buttons over the videos of one frame (VIDEO-DETECT-SPEC §5.1): one
// fixed-position host appended to `document.documentElement` with a closed shadow root, so
// nothing is inserted into the site's player and its layout and event handlers stay untouched.
// The host lets the pointer through except over the buttons and the panel. Each button sits at
// its video's top-right corner (the mark alone on small videos), placed from the video's
// rectangle in `requestAnimationFrame`; at most MAX_BUTTONS per frame, the largest videos. A page
// can move a video without any event the layer sees (content inserted above it), so pointer
// movement places the buttons again before it reveals one, every frame while the panel is open,
// and a button not shown lets clicks through to the page. The layer follows the tracker
// (TrackerApi): it mounts while buttons are allowed for the tab's site or the context menu asked
// for a video's panel, puts its host back when the page removed it, and removes itself for good
// when the extension goes away. Styles are adopted sheets, with a `<style>` element where
// constructable sheets are missing or refused. On a compact social site (VIDEO-DETECT-SPEC §5.1
// "Social sites") every button is the compact mark in a corner free of the site's own controls,
// widening on hover or focus to its label and the best quality once known, with its own working and
// sent states; only the video the page shows under the pointer reveals its button, and a hovered
// grid tile gets one too.

import { clipFields } from './clip';
import { feedPostLink } from './feed-link';
import type { ExtensionMessage, FrameOffersMessage, OfferAddMessage, PageVideoAddMessage, ResponseFor } from './messages';
import {
  elementAudio,
  MAX_OTHERS,
  MAX_PLAYED_URL,
  type PlayedTrace,
  playingSegments,
  playedName,
  playedNameOver,
  postContainer,
  profileLink,
  recentMedia,
  type TimingEntry,
} from './played';
import { PLAYED_KEY } from './played-handoff';
import type { FrameOffers } from './offer-types';
import { ITEM_STRIP_CSS } from './item-strip';
import { OfferPanel, OFFER_PANEL_CSS, OfferPanelDom, type OfferPanelView } from './offer-panel';
import { failure, type Outcome, systemTimers, type Timers, type VideoInfoResult } from './protocol';
import { addressSlide, compactSite, parseSiteVideo } from './site-video';
import { cleanSiteText, VIDEO_TEXTS } from './ui-format';
import {
  BUTTON_LABEL,
  downexMark,
  glyph,
  isolateEvents,
  OVERLAY_SHARED_CSS,
  REVEAL_ACTIVITY_MS,
  REVEAL_LOAD_MS,
  SENT_CLOSE_MS,
} from './video-overlay';
import { defaultPick, pickSize, qualityName, sizeLabel, trackById } from './video-picker';
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

/**
 * The compact button of a social site (VIDEO-DETECT-SPEC §5.1 "Social sites"): 34 px with an inset
 * of 10 px over a video at least COMPACT_TINY_WIDTH_PX × COMPACT_TINY_HEIGHT_PX, else 28 px with an
 * inset of 6 px (feed cards, grid tiles).
 */
export const SOCIAL_SMALL_PX = 34;
export const SOCIAL_SMALL_INSET_PX = 10;
export const SOCIAL_TINY_PX = 28;
export const SOCIAL_TINY_INSET_PX = 6;
export const COMPACT_TINY_WIDTH_PX = 260;
export const COMPACT_TINY_HEIGHT_PX = 220;
/** The corners under a control are taken only within this upper part of the visible video. */
export const UPPER_PART = 0.45;
/** A shown compact button checks its corner again after this long, or when its video's size changed … */
export const CORNER_RECHECK_MS = 500;
/** … by more than this. */
const CORNER_SLACK_PX = 4;
/** Ancestors of a video that may hold the element the page shows under the pointer (§5.1). */
export const TOPMOST_LEVELS = 12;
/** A compact button asks the background's memory for its hint at most this often. */
export const HINT_ASK_MS = 20_000;
/** A grid tile's descendants read for its own small parts (a badge) when its button is placed. */
const TILE_PARTS = 64;
/** The id of the grid tile's button (videos' ids are positive). */
export const TILE_ID = -1;
/**
 * A compact button's label is chosen to fit its widest form: at most this many px a character
 * (12 px semi-bold system text, generous), past the orb, the gap and the padding.
 */
const LABEL_CHAR_PX = 7;
const LABEL_CHROME_PX = 42;
/** The compact button's texts (§5.6). */
export const SENT_LABEL = 'Sent to Downex';
export const TILE_LABEL = 'Download the video of this post with Downex';
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
.dx-button[data-social='true'] {
  gap: 0;
  min-height: 0;
  height: 34px;
  max-width: var(--dx-max, 260px);
  padding: 0 2px;
  font-size: 12px;
  transition: opacity 0.2s var(--ease), transform 0.24s var(--spring), gap 0.22s var(--ease), padding 0.22s var(--ease), border-color 0.15s;
}
.dx-button[data-social='true'][data-size='tiny'] { height: 28px; padding: 0 2px; }
.dx-button[data-social='true'] .dx-tile { position: relative; width: 28px; height: 28px; }
.dx-button[data-social='true'] .dx-tile svg { width: 16px; height: 16px; }
.dx-button[data-social='true'][data-size='tiny'] .dx-tile { width: 24px; height: 24px; }
.dx-button[data-social='true'][data-size='tiny'] .dx-tile svg { width: 14px; height: 14px; }
.dx-button[data-social='true'] .dx-label {
  min-width: 0;
  max-width: 0;
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
  opacity: 0;
  transition: max-width 0.24s var(--ease), opacity 0.16s var(--ease);
}
.dx-button[data-social='true']:is(:hover, :focus-visible, [aria-expanded='true'], [data-wide='true']) { gap: 6px; padding-right: 10px; }
.dx-button[data-social='true']:is(:hover, :focus-visible, [aria-expanded='true'], [data-wide='true']) .dx-label { max-width: 22em; opacity: 1; }
.dx-button[data-social='true'] .dx-tile .dx-sent { display: none; }
.dx-button[data-social='true'][data-state='sent'] .dx-tile .mark { display: none; }
.dx-button[data-social='true'][data-state='sent'] .dx-tile .dx-sent { display: block; }
.dx-button[data-social='true'][data-state='working'] .dx-tile::after {
  content: '';
  position: absolute;
  inset: -3px;
  border: 2px solid rgb(255 255 255 / 0.22);
  border-top-color: #ffffff;
  border-right-color: #ffffff;
  border-radius: 50%;
  animation: dx-turn 0.9s linear infinite;
}
.dx-button[data-social='true'][data-state='working'] .dx-tile { background: rgb(147 224 186 / 0.7); }
@keyframes dx-turn { to { transform: rotate(1turn); } }
@media (forced-colors: active) {
  .dx-button[data-social='true'][data-state='working'] .dx-tile { background: ButtonText; }
  .dx-button[data-social='true'][data-state='working'] .dx-tile::after { border-color: ButtonText; border-top-color: Highlight; border-right-color: Highlight; }
}
`;

/** Everything the layer's shadow root uses. */
export const LAYER_STYLE = LAYER_HOST_CSS + OVERLAY_SHARED_CSS + LAYER_CSS + OFFER_PANEL_CSS + ITEM_STRIP_CSS;

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
export function placeButton(rect: RectLike, width: number, height: number, small = false): Placement | undefined {
  const left = Math.max(rect.left, 0);
  const right = Math.min(rect.right, width);
  const top = Math.max(rect.top, 0);
  const bottom = Math.min(rect.bottom, height);
  if (!(right > left) || !(bottom > top)) return undefined;
  const compact = small || rect.width < COMPACT_WIDTH_PX || rect.height < COMPACT_HEIGHT_PX;
  const inset = compact ? COMPACT_INSET_PX : BUTTON_INSET_PX;
  return { top: Math.round(top + inset), right: Math.round(width - right + inset), compact };
}

/** The mark alone's size (px; `.dx-button[data-compact='true']`). */
const MARK_PX = 32;

/** A compact button's size (VIDEO-DETECT-SPEC §5.1 "Social sites"). */
export type SocialSize = 'small' | 'tiny';

/** The compact button's size over a video of `rect`'s size: `tiny` on small ones (feed cards, tiles). */
export function socialSize(rect: Pick<RectLike, 'width' | 'height'>): SocialSize {
  return rect.width < COMPACT_TINY_WIDTH_PX || rect.height < COMPACT_TINY_HEIGHT_PX ? 'tiny' : 'small';
}

/** Where a compact button goes (viewport coordinates), and which corner it took. */
export interface SocialPlacement {
  top: number;
  /** The edge it is anchored to (it widens away from it) … */
  side: 'left' | 'right';
  /** … and its distance from that edge of the viewport. */
  x: number;
  size: SocialSize;
  /** 0 top-right, 1 top-left, 2 right under a control, 3 left under one, 4 right middle, 5 left middle. */
  corner: number;
  /** Its widest form: the visible width less both insets. */
  maxWidth: number;
}

/**
 * The compact button of a video at `rect` in a `width` × `height` viewport (§5.1 "A free corner"):
 * the first of its corners whose box `blocker` finds free of the page's own controls (`blocker`
 * answers the rectangle of the control that takes it). In order: the visible part's top-right and
 * top-left corners, the right and left edges just under the control that took that top corner
 * (within the upper UPPER_PART), the middle of the right and left edges. Undefined when no part is
 * visible, the visible part is smaller than the button and its insets, or no corner is free.
 */
export function placeSocial(rect: RectLike, width: number, height: number, blocker: (box: RectLike) => RectLike | undefined): SocialPlacement | undefined {
  const l = Math.max(rect.left, 0);
  const r = Math.min(rect.right, width);
  const t = Math.max(rect.top, 0);
  const b = Math.min(rect.bottom, height);
  if (!(r > l) || !(b > t)) return undefined;
  const size = socialSize(rect);
  const px = size === 'tiny' ? SOCIAL_TINY_PX : SOCIAL_SMALL_PX;
  const inset = size === 'tiny' ? SOCIAL_TINY_INSET_PX : SOCIAL_SMALL_INSET_PX;
  if (r - l < px + 2 * inset || b - t < px + 2 * inset) return undefined;
  const leftX = Math.round(l + inset);
  const rightX = Math.round(r - inset - px);
  const box = (x: number, y: number): RectLike => ({ left: x, top: y, right: x + px, bottom: y + px, width: px, height: px });
  const lowest = t + (b - t) * UPPER_PART - px;
  const middle = Math.round((t + b) / 2 - px / 2);
  const done = (corner: number, x: number, y: number): SocialPlacement => {
    const side = corner % 2 === 0 ? 'right' : 'left';
    return { top: Math.round(y), side, x: side === 'right' ? Math.round(width - x - px) : x, size, corner, maxWidth: Math.max(px, Math.floor(r - l - 2 * inset)) };
  };
  const top = Math.round(t + inset);
  const tr = blocker(box(rightX, top));
  if (!tr) return done(0, rightX, top);
  const tl = blocker(box(leftX, top));
  if (!tl) return done(1, leftX, top);
  for (const [corner, x, over] of [
    [2, rightX, tr],
    [3, leftX, tl],
  ] as const) {
    const y = Math.round(over.bottom + inset);
    if (y <= lowest && !blocker(box(x, y))) return done(corner, x, y);
  }
  if (!blocker(box(rightX, middle))) return done(4, rightX, middle);
  if (!blocker(box(leftX, middle))) return done(5, leftX, middle);
  return undefined;
}

/**
 * The button the page shows under the pointer (§5.1 "The video under the pointer"): of the
 * `candidates` (ids and their elements), the one whose player holds `hit`, the element topmost at
 * the pointer; the nearest wins. A player is the element and its ancestors, up to TOPMOST_LEVELS
 * (from a shadow root to its host), while they stay about its size (playerSized): a feed or a
 * page around it is not its player. Undefined when no player holds `hit` (something else covers
 * them, such as a post viewer's backdrop). `viewWidth` is the frame's viewport width: a card that
 * keeps the video's height may span it (playerSized).
 */
export function topmostAt<T extends { el: Element }>(candidates: readonly T[], hit: Element | undefined, viewWidth = 0): T | undefined {
  if (!hit) return undefined;
  const around = new Set<Element>();
  for (let n: Element | null = hit, i = 0; n && i < 256; n = upElement(n), i++) around.add(n);
  let best: T | undefined;
  let bestLevel = Number.POSITIVE_INFINITY;
  for (const c of candidates) {
    const r = c.el.getBoundingClientRect();
    const maxWidth = r.width * PLAYER_GROWTH + PLAYER_SLACK_PX;
    const maxHeight = r.height * PLAYER_GROWTH + PLAYER_SLACK_PX;
    let n: Element | null = c.el;
    for (let level = 0; n && level <= TOPMOST_LEVELS && level < bestLevel; level++) {
      if (around.has(n)) {
        best = c;
        bestLevel = level;
        break;
      }
      n = upElement(n);
      const a = n?.getBoundingClientRect();
      if (!a || !playerSized(a, maxWidth, maxHeight, viewWidth)) break;
    }
  }
  return best;
}

/**
 * Whether an ancestor's box `a` may be part of a video's player (topmostAt): within PLAYER_GROWTH
 * of the video's size on both axes (`maxWidth`, `maxHeight`); or a letterboxing card, which keeps
 * one axis and grows the other no further than a LETTERBOX frame of the kept one, or, keeping the
 * video's height, no wider than the viewport (`viewWidth`): a portrait video in a wide card on a
 * vertical feed, whose width follows the window's, not the video's; or a wrapper with no area,
 * which is no box at all. A feed column (far taller than wide) or a page grows past these.
 */
function playerSized(a: RectLike, maxWidth: number, maxHeight: number, viewWidth: number): boolean {
  if (!(a.width > 0 && a.height > 0)) return true;
  const wide = a.width <= maxWidth;
  const tall = a.height <= maxHeight;
  if (wide && tall) return true;
  if (tall) return a.width <= Math.max(a.height * LETTERBOX, viewWidth) + PLAYER_SLACK_PX;
  if (wide) return a.height <= a.width * LETTERBOX + PLAYER_SLACK_PX;
  return false;
}

/** A video's player may be this many times its size, plus PLAYER_SLACK_PX (a card's padding, a caption bar). */
const PLAYER_GROWTH = 1.25;
const PLAYER_SLACK_PX = 8;
/** The longest side a letterboxing card may have, in its other side: a 16:9 frame. */
const LETTERBOX = 16 / 9;

/** The parent element, or the host of the shadow root the element sits in. */
function upElement(el: Element): Element | null {
  if (el.parentElement) return el.parentElement;
  const root = el.parentNode;
  return root !== null && root.nodeType === 11 ? ((root as Partial<ShadowRoot>).host ?? null) : null;
}

/**
 * The compact button's hint (§5.1 "What the button tells"): "1080p · 24 MB" from a quality's name
 * (up to " (" or " ·") and size ("" unknown); undefined when the name is no quality.
 */
export function hintText(quality: string, size: string): string | undefined {
  const q = cleanSiteText(quality)
    .replace(/\s*[(·].*$/, '')
    .trim();
  if (!/^[0-9]/.test(q) && !/^[48]K\b/.test(q)) return undefined;
  const z = cleanSiteText(size).trim();
  return z ? `${q} · ${z}` : q;
}

/** The hint of a site answer: its default pick's quality and size. */
export function infoHint(info: VideoInfoResult): string | undefined {
  const pick = defaultPick(info);
  const t = pick.video ? trackById(info, pick.video) : undefined;
  return t ? hintText(qualityName(t), sizeLabel(pickSize(info, pick))) : undefined;
}

/**
 * A quiet video's mark (VIDEO-DETECT-SPEC §5.1 "Quiet videos"): at the top-right corner of the
 * video's visible part, inset 8 px, else at its top-left one, whichever covers none of the page's
 * own controls (`coversControl` of the mark's box in viewport coordinates). Undefined when both
 * would, or when no part of the video is inside the viewport.
 */
export function placeMark(rect: RectLike, width: number, height: number, coversControl: (box: RectLike) => boolean): Placement | undefined {
  const p = placeButton(rect, width, height, true);
  if (!p) return undefined;
  const box = (left: number): RectLike => ({ left, top: p.top, right: left + MARK_PX, bottom: p.top + MARK_PX, width: MARK_PX, height: MARK_PX });
  if (!coversControl(box(width - p.right - MARK_PX))) return p;
  const left = Math.round(Math.max(rect.left, 0) + COMPACT_INSET_PX);
  const right = width - left - MARK_PX;
  return right !== p.right && !coversControl(box(left)) ? { top: p.top, right, compact: true } : undefined;
}

/**
 * Whether a page element at `r` lies over half of the visible part of a video at `video` or more (in
 * a `width` × `height` viewport): the video's own surface, such as a tile's link laid over it.
 */
function surface(r: RectLike, video: RectLike, width: number, height: number): boolean {
  const vl = Math.max(video.left, 0);
  const vt = Math.max(video.top, 0);
  const vr = Math.min(video.right, width);
  const vb = Math.min(video.bottom, height);
  const visible = Math.max(0, vr - vl) * Math.max(0, vb - vt);
  const shared = Math.max(0, Math.min(r.right, vr) - Math.max(r.left, vl)) * Math.max(0, Math.min(r.bottom, vb) - Math.max(r.top, vt));
  return visible > 0 && shared * 2 >= visible;
}

/** The page's own controls a quiet mark must not cover (§5.1 "Quiet videos"). */
const PAGE_CONTROLS =
  'a[href], button, input, select, textarea, summary, label, [role="button"], [role="link"], [role="checkbox"], [role="switch"], [role="slider"], [role="tab"], [role="menuitem"]';
/** … and a compact button (§5.1 "A free corner"): also focusable elements the site made controls. */
const SOCIAL_CONTROLS = `${PAGE_CONTROLS}, [tabindex="0"]`;

/**
 * The videos that get a button: meaningful ones (visible included), then quiet ones (VIDEO-DETECT-SPEC
 * §2.5, a mark on hover only), the largest first within each, at most `max`.
 */
export function buttonVideos(videos: readonly TrackedVideo[], max = MAX_BUTTONS): TrackedVideo[] {
  return videos
    .filter((v) => v.meta.meaningful || v.quiet === true)
    .sort(
      (a, b) =>
        Number(a.quiet === true && !a.meta.meaningful) - Number(b.quiet === true && !b.meta.meaningful) ||
        b.meta.width * b.meta.height - a.meta.width * a.meta.height ||
        a.id - b.id,
    )
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
  /** `document.elementsFromPoint` (tests substitute it). */
  elementsAt?: (x: number, y: number) => readonly Element[];
  /** The frame is a compact social site's (`compactSite` of its address when undefined). */
  compact?: boolean;
}

/** A compact button's corner, kept relative to its video's visible part (§5.1 "A free corner"). */
interface CornerMemo {
  /** Undefined: no corner was free (checked again as any other). */
  placement: SocialPlacement | undefined;
  /** The visible part's top-left corner and the video's size when it was chosen. */
  left: number;
  top: number;
  width: number;
  height: number;
  at: number;
}

interface ButtonDom {
  id: number;
  /** The video, or the grid tile's link (TILE_ID). */
  el: Element;
  video: HTMLVideoElement | undefined;
  button: HTMLButtonElement;
  label: HTMLElement;
  revealTimer: unknown;
  /** A quiet video's mark: compact, shown only while the pointer is over the video (§5.1). */
  quiet: boolean;
  /** A compact social site's button, and its corner. */
  social: boolean;
  corner?: CornerMemo | undefined;
  /** What it tells (§5.1 "What the button tells"), for the video's source or post it was learnt for. */
  hint?: { text: string; for: string } | undefined;
  state?: { kind: 'working' | 'sent'; for: string } | undefined;
  wideTimer: unknown;
  hintAskedAt: number;
  /** The grid tile's post, or the post a click found for this video. */
  post?: string | undefined;
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
  /**
   * The button the pointer is on (§5.1 "Button"): it stays shown while the pointer rests there and
   * is never judged covered by what the page has under it (the layer's own elements never cover).
   */
  #hovered: number | undefined;
  /**
   * The pointer's last position in the frame, kept between placements (undefined once it left the
   * frame): a pointer that stopped on a button the moment it was revealed got no `pointerenter`
   * (that comes with the next movement), yet it rests on the button.
   */
  #last: { x: number; y: number } | undefined;
  /**
   * The clicked feed video's own post (VIDEO-DETECT-SPEC §5.1 "Feed videos"), found when its panel
   * opened: the address the panel's Site source asks about instead of the frame's.
   */
  #post: string | undefined;
  /** The name of the clicked video read when its panel opened (SITE-MEDIA-SPEC §2.9 "The name"). */
  #playedName = '';
  /** A compact social site's frame (§5.1 "Social sites"). */
  readonly #compact: boolean;

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
    this.#compact = deps.compact ?? compactSite(location());
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
      onPartToggle: () => this.panel.togglePart(),
      onPartSet: (which, ms, push) => this.panel.setPartMs(which, ms, push),
      onPartEdit: (which, text) => this.panel.editPart(which, text),
      onPartChapter: (n) => this.panel.choosePartChapter(n),
      onPartWhole: () => this.panel.wholePart(),
      onItem: (id) => this.panel.chooseItem(id),
      // The timeline's video: the one the panel was opened for, in this frame (§5.2); none for a tile.
      partVideo: () => (this.#panelFor === undefined ? undefined : this.#buttons.get(this.#panelFor)?.video),
      timers: this.#timers,
    });
    const panelDeps: ConstructorParameters<typeof OfferPanel>[0] = {
      offers: async (src, mse): Promise<Outcome<FrameOffers>> => {
        // The frame moved on (a single-page app's navigation): the panel's items are gone.
        if (frameAddress(location()) !== this.#openedAt) {
          this.closePanel(false);
          return failure('expired', 'The page changed.');
        }
        const post = this.#post;
        const msg: FrameOffersMessage = post ? { type: 'downex:frame-offers', src, mse, post } : { type: 'downex:frame-offers', src, mse };
        // What the clicked `blob:` video says of itself, for its Played source (SITE-MEDIA-SPEC §2.9).
        const played = mse ? this.#playedTrace(frameAddress(location())) : undefined;
        if (played) msg.played = played;
        return ask(msg);
      },
      streamProbe: (key) => ask({ type: 'downex:stream-probe', key }),
      offerAdd: (key, pick, clip) => {
        const msg: OfferAddMessage = { type: 'downex:offer-add', key };
        if (pick.video) msg.videoTrack = pick.video;
        if (pick.audio) msg.audioTrack = pick.audio;
        if (clip) Object.assign(msg, clipFields(clip));
        if (key === PLAYED_KEY) {
          const played = this.#playedTrace(frameAddress(location()));
          // mediaKeys set while the panel was open: protected now, never saved (§2.9 "Never").
          if (played?.drm) return Promise.resolve(failure('rejected', `encrypted: ${VIDEO_TEXTS.encrypted}`, 'encrypted'));
          if (played) msg.played = played;
        }
        return ask(msg);
      },
      siteProbe: (site, entryId) => {
        const url = this.#siteAddress(site.linked === true, location());
        return ask(entryId ? { type: 'downex:page-video-probe', url, entryId } : { type: 'downex:page-video-probe', url });
      },
      siteAdd: (site, pick, clip, entryId) => {
        const msg: PageVideoAddMessage = {
          type: 'downex:page-video-add',
          url: this.#siteAddress(site.linked === true, location()),
          videoTrack: pick.video,
          audioTrack: pick.audio,
        };
        if (clip) Object.assign(msg, clipFields(clip));
        if (entryId) msg.entryId = entryId;
        return ask(msg);
      },
      hideSite: () => this.#api.send({ type: 'downex:set-site-hidden', hidden: true }),
      render: (view) => this.#render(view),
      timers: this.#timers,
      viewport: () => {
        const w = doc.defaultView;
        return { width: w?.innerWidth ?? 0, height: w?.innerHeight ?? 0 };
      },
      // The slide of the frame's own post; a feed video's post is another page (§5.1).
      itemPosition: () => (this.#post ? undefined : addressSlide(location())),
      pointerOn: () => this.panelDom.el.matches(':hover'),
    };
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

  /** The buttons by video id (the grid tile's: TILE_ID). */
  get buttons(): ReadonlyMap<number, HTMLButtonElement> {
    return new Map([...this.#buttons].map(([id, b]) => [id, b.button]));
  }

  /** Whether this is a compact social site's layer (§5.1 "Social sites"). */
  get compact(): boolean {
    return this.#compact;
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
    listen(this.#doc, 'pointerout', (e) => {
      if (!(e as PointerEvent).relatedTarget) this.#last = undefined;
    }, passive);
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

  /**
   * The tracked videos changed: buttons are added, kept or removed. The video the context menu
   * asked for (`api.requested`) has one whatever the rules; on a site without buttons it is the
   * only one (VIDEO-DETECT-SPEC §5.1 "Menu").
   */
  sync(): void {
    if (!this.#mounted) return;
    this.#reattach();
    const videos = this.#api.videos();
    const chosen = this.#api.allowed ? buttonVideos(videos) : [];
    const requested = this.#api.requested;
    const asked = requested === undefined || chosen.some((v) => v.id === requested) ? undefined : videos.find((v) => v.id === requested);
    if (asked) chosen.push(asked);
    const keep = new Set(chosen.map((v) => v.id));
    // The video of an open panel keeps its button while it is in the page.
    const open = this.panel.isOpen ? this.#panelFor : undefined;
    if (open !== undefined && open !== TILE_ID) {
      const v = videos.find((x) => x.id === open);
      if (v?.el.isConnected) keep.add(open);
      else this.closePanel(false);
    }
    if (this.#syncTile(chosen)) keep.add(TILE_ID);
    for (const [id, b] of [...this.#buttons]) {
      if (keep.has(id)) continue;
      this.#dropButton(b);
      this.#buttons.delete(id);
      this.#rects.delete(id);
    }
    for (const v of chosen) {
      const b = this.#buttons.get(v.id);
      if (!b) {
        this.#addButton(v);
        continue;
      }
      // A quiet loop the user unmuted (or that got controls) is a video like any other now.
      const quiet = v.quiet === true && !v.meta.meaningful;
      if (b.quiet === quiet) continue;
      b.quiet = quiet;
      if (quiet) b.button.dataset.quiet = 'true';
      else delete b.button.dataset.quiet;
    }
    this.schedulePlace();
  }

  /**
   * Shows a button for `ms` (pointer activity over its video, a video starting). A quiet video's
   * mark shows only for the pointer (`hover`) or its open panel, never because the video started.
   */
  reveal(id: number, ms: number, hover = false): void {
    const b = this.#buttons.get(id);
    const tileLike = b !== undefined && (b.quiet || b.id === TILE_ID);
    const open = this.panel.isOpen && this.#panelFor === id;
    if (!b || (tileLike && !hover && !open)) return;
    // A video that started under something the page laid over it (a post viewer) stays hidden.
    if (b.social && !hover && !open && this.#covered(b)) return;
    if (b.social) this.#refreshTells(b, hover);
    b.button.dataset.shown = 'true';
    if (b.revealTimer !== undefined) this.#timers.clearTimeout(b.revealTimer);
    b.revealTimer = this.#timers.setTimeout(() => {
      b.revealTimer = undefined;
      if (this.panel.isOpen && this.#panelFor === id) return;
      // The pointer resting on a compact button keeps it while the page still shows its video
      // (a prompt laid over the page hides it); leaving it shows it for the usual time.
      if (b.social && (this.#hovered === id || this.#restsOn(b)) && this.#shows(b)) this.reveal(id, ms, true);
      else b.button.dataset.shown = 'false';
    }, ms);
  }

  /** A video lost its media: its panel closes, and its compact button forgets what it told. */
  emptied(id: number): void {
    if (this.panel.isOpen && this.#panelFor === id) this.closePanel(false);
    const b = this.#buttons.get(id);
    if (b?.social) this.#forget(b);
  }

  /** The pointer entered another grid tile (§5.1 "Grid tiles"): its button goes there. */
  tileChanged(): void {
    if (!this.#mounted) return;
    this.sync();
    if (this.#buttons.has(TILE_ID)) this.reveal(TILE_ID, REVEAL_ACTIVITY_MS, true);
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
    const pointer = this.#pointer;
    this.#pointer = undefined;
    for (const b of this.#buttons.values()) {
      const rect = b.el.getBoundingClientRect();
      const p = placeButton(rect, width, height);
      b.button.hidden = p === undefined;
      if (!p) {
        this.#rects.delete(b.id);
        continue;
      }
      this.#rects.set(b.id, rect);
      if (b.social) {
        const over = pointer !== undefined && pointer.x >= rect.left && pointer.x <= rect.right && pointer.y >= rect.top && pointer.y <= rect.bottom;
        this.#placeSocial(b, rect, width, height, over);
        continue;
      }
      // A quiet video's mark is the mark alone, inset as one, beside the page's own controls (in
      // its usual corner while its panel, opened from the menu, is open: the panel holds the focus).
      const open = this.panel.isOpen && this.#panelFor === b.id;
      const covers = (box: RectLike) => this.#controlAt(box, b.el, rect, width, height, PAGE_CONTROLS) !== undefined;
      const at = b.quiet ? (placeMark(rect, width, height, covers) ?? (open ? placeButton(rect, width, height, true) : undefined)) : p;
      if (!at) {
        b.button.hidden = true;
        continue;
      }
      b.button.style.top = `${at.top}px`;
      b.button.style.right = `${at.right}px`;
      b.button.dataset.compact = String(at.compact);
    }
    if (pointer) {
      const under: ButtonDom[] = [];
      for (const [id, r] of this.#rects) {
        const over = pointer.x >= r.left && pointer.x <= r.right && pointer.y >= r.top && pointer.y <= r.bottom;
        const b = this.#buttons.get(id);
        if (over && b) under.push(b);
        else this.#leave(id);
      }
      // On a compact site only the video the page shows under the pointer (§5.1): the one whose
      // player holds the element topmost there; else, where the page lays a layer of its own over a
      // part of the video (a post's header, where the button sits), the one it shows at its centre.
      // The button the pointer rests on is that video's while the page shows it.
      let shown: ButtonDom | undefined;
      if (this.#compact && under.length > 0) {
        const on = under.find((u) => u.id === this.#hovered);
        shown = on && this.#shows(on) ? on : (topmostAt(under, this.#hitAt(pointer.x, pointer.y), width) ?? under.find((u) => this.#shows(u)));
      }
      for (const u of under) {
        if (!this.#compact || u === shown) this.reveal(u.id, REVEAL_ACTIVITY_MS, true);
        else this.#leave(u.id, true);
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
    // DRM and live are checked again now: mediaKeys can be set after the button appeared. A grid
    // tile has no element: the site's answer says (§5.1 "Grid tiles").
    // A tile that plays a preview (`blob:`) has the Played source of that preview (SITE-MEDIA-SPEC §2.9).
    const preview = id === TILE_ID ? tilePreview(b.el) : undefined;
    const meta = id === TILE_ID ? { src: '', mse: preview !== undefined, drm: false, durationMs: -1 } : this.#api.recheck(id);
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
    const el = id === TILE_ID ? undefined : this.#api.videos().find((v) => v.id === id)?.el;
    // A feed video's own post, read now (a click): link addresses only (§5.1 "Feed videos"); a
    // grid tile's is its own link's.
    let post: string | undefined;
    try {
      const shown = this.#api.shown?.bind(this.#api);
      post = id === TILE_ID ? b.post : el ? feedPostLink(el, this.#openedAt, shown ? { shown } : {}) : undefined;
    } catch {
      post = undefined;
    }
    if (id !== TILE_ID && b.post !== post) {
      // Another post in the same element (a feed that reuses its players): what it told is gone.
      if (b.social) this.#forget(b);
      b.post = post;
    }
    if (post !== this.#post) {
      // Another post in the same element (a feed that reuses its players) is another video.
      this.panel.reset();
      this.#post = post;
    }
    // The played video's name, read now (a click): the post's author and caption (§2.9 "The name").
    this.#playedName = '';
    try {
      const named = el ?? preview;
      const profile = (href: string) => profileLink(href, this.#openedAt);
      // The post container, else the post or viewer the video is in (an article, a dialog), else
      // the author of the page's main content when it holds this video only.
      if (named && meta.mse) {
        const main = named.closest('main');
        this.#playedName =
          playedName(postContainer(named), profile) ||
          playedName(named.closest('article, [role="dialog"]') ?? undefined, profile) ||
          (main && main.querySelectorAll('video').length === 1 ? playedName(main, profile, { authorOnly: true }) : '') ||
          playedNameOver(named, profile);
      }
    } catch {
      this.#playedName = '';
    }
    this.panel.open({
      src: meta.src,
      mse: meta.mse,
      drm: meta.drm,
      live: meta.durationMs === -2,
      played: () => hasPlayed(el),
      durationMs: () => elementDuration(el),
    });
    this.#positionPanel();
    this.schedulePlace(); // and again in every frame while it is open
    this.panelDom.focusInside();
  }

  /**
   * What the panel's video says of itself (SITE-MEDIA-SPEC §2.9 "The flow" 2): its duration, picture
   * and sound, the page's resource-timing media addresses, the name read at the click and the
   * frame's address now. Undefined without a video (a grid tile).
   */
  #playedTrace(href: string): PlayedTrace | undefined {
    const b = this.#panelFor === undefined ? undefined : this.#buttons.get(this.#panelFor);
    const el = b?.video ?? (b ? tilePreview(b.el) : undefined);
    if (!el) return undefined;
    // A live stream has no Played source: its files are never a whole video (§2.9).
    if ((el as { duration?: unknown }).duration === Infinity) return undefined;
    let recent: string[] = [];
    let segments: string[] = [];
    try {
      const perf = this.#doc.defaultView?.performance;
      const entries = perf ? (perf.getEntriesByType('resource') as unknown as (TimingEntry & { startTime?: number })[]) : [];
      recent = recentMedia(entries);
      segments = playingSegments(entries);
    } catch {
      recent = [];
      segments = [];
    }
    const side = (n: unknown) => (typeof n === 'number' && Number.isFinite(n) ? Math.max(0, Math.min(16384, Math.round(n))) : 0);
    let playing = '';
    try {
      playing = el.currentSrc ?? '';
    } catch {
      playing = '';
    }
    // The frame's other videos' lengths: their files are theirs (a feed's preloaded neighbour).
    const others: number[] = [];
    try {
      for (const v of Array.from(this.#doc.querySelectorAll('video'))) {
        const d = v === el ? 0 : elementDuration(v);
        if (d > 0 && d <= 86_400_000 && others.length < MAX_OTHERS) others.push(d);
      }
    } catch {
      others.length = 0;
    }
    let drm = false;
    try {
      drm = b !== undefined && this.#panelFor !== TILE_ID && this.#panelFor !== undefined && this.#api.recheck(this.#panelFor)?.drm === true;
    } catch {
      drm = false;
    }
    return {
      // The element's identity: what it plays, its post, and how many times it loaded another
      // video (a feed's player that loads the next post keeps its address empty; review 2026-11-18).
      element: `${this.#panelFor ?? 0}-${shortHash(`${playing}\n${this.#post ?? ''}`)}-${loadGeneration(el)}`,
      durationMs: Math.min(elementDuration(el), 86_400_000),
      others,
      width: side(el.videoWidth),
      height: side(el.videoHeight),
      audio: elementAudio(el),
      recent,
      segments,
      name: this.#playedName.slice(0, 200),
      href: href.slice(0, MAX_PLAYED_URL),
      ...(drm ? { drm: true } : {}),
    };
  }

  /** Opens the panel of the video the context menu asked for (VIDEO-DETECT-SPEC §5.1 "Menu"). */
  showRequested(id: number): void {
    if (!this.#mounted || this.#api.requested !== id) return;
    // Already open for it (the layer opened it when it started, before the tracker's event).
    if (this.panel.isOpen && this.#panelFor === id) return;
    this.sync();
    this.reveal(id, REVEAL_ACTIVITY_MS);
    this.openPanel(id);
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
    if (b?.social && b.state?.kind === 'working') {
      b.state = undefined;
      this.#drawTells(b);
    }
    if (b && focusButton) b.button.focus();
    // The button was just used: shown for a moment, a quiet mark too.
    if (id !== undefined) this.reveal(id, REVEAL_ACTIVITY_MS, true);
    // The menu's video follows the usual rules again (the tracker says so in a moment).
    if (id !== undefined && this.#api.requested === id) this.#api.release(id);
  }

  // --- internals -------------------------------------------------------------------------------

  /** The address the Site source asks about: the clicked feed video's post, else the frame's own. */
  #siteAddress(linked: boolean, frameUrl: string): string {
    return linked && this.#post ? this.#post : frameAddress(frameUrl);
  }

  /**
   * The pointer is not over a quiet video or the grid tile (or, `covered`, over a compact button's
   * video the page covers there): its button hides (not while focused or its panel is open).
   */
  #leave(id: number, covered = false): void {
    const b = this.#buttons.get(id);
    if (!b || !(b.quiet || b.id === TILE_ID || covered) || (this.panel.isOpen && this.#panelFor === id)) return;
    const root = b.button.getRootNode() as ShadowRoot | Document;
    if (root.activeElement === b.button) return;
    if (b.revealTimer !== undefined) this.#timers.clearTimeout(b.revealTimer);
    b.revealTimer = undefined;
    b.button.dataset.shown = 'false';
  }

  get #popoverApi(): boolean {
    return typeof (this.panelDom.el as Partial<{ showPopover: unknown }>).showPopover === 'function';
  }

  /** The fullscreen element when it is not the video itself (which shows only the browser's controls). */
  #parent(): Element {
    const fs = this.#doc.fullscreenElement;
    return fs && fs.localName !== 'video' ? fs : this.#doc.documentElement;
  }

  /**
   * Whether a box (viewport coordinates) lies over one of the page's own controls in the video's
   * corner, such as a landing page's pause button over its hero (§5.1 "Quiet videos"). A link or
   * button around the video, or laid over half of its visible part or more (a tile's link), is the
   * video's own surface, not such a control. It hit-tests the box's corners and centre past this
   * layer; call in an animation frame.
   */
  #controlAt(box: RectLike, video: Element, rect: RectLike, width: number, height: number, controls: string): RectLike | undefined {
    const at = this.#elementsAt();
    const points: [number, number][] = [
      [box.left + 1, box.top + 1],
      [box.right - 1, box.top + 1],
      [box.left + 1, box.bottom - 1],
      [box.right - 1, box.bottom - 1],
      [(box.left + box.right) / 2, (box.top + box.bottom) / 2],
    ];
    const seen = new Set<Element>();
    for (const [x, y] of points) {
      const under = at(x, y).find((e) => e !== this.host);
      const control = under?.closest(controls);
      if (!control || seen.has(control)) continue;
      seen.add(control);
      const r = control.getBoundingClientRect();
      if (!control.contains(video) && !surface(r, rect, width, height)) return r;
    }
    return undefined;
  }

  /**
   * A grid tile's own small part in a box (§5.1 "Grid tiles"): its "reel" badge and the like, which
   * the page draws without taking the pointer (hit tests pass through them), so the tile's first
   * TILE_PARTS descendants are read instead; a part of half the tile or more is its picture.
   */
  #tilePart(box: RectLike, tile: Element, rect: RectLike, width: number, height: number): RectLike | undefined {
    const parts = tile.getElementsByTagName('*');
    for (let i = 0; i < parts.length && i < TILE_PARTS; i++) {
      const r = parts[i]?.getBoundingClientRect();
      if (!r || !(r.width > 0 && r.height > 0)) continue;
      const overlaps = r.left < box.right && r.right > box.left && r.top < box.bottom && r.bottom > box.top;
      if (overlaps && !surface(r, rect, width, height)) return r;
    }
    return undefined;
  }

  /** `document.elementsFromPoint`, or the tests' stand-in. */
  #elementsAt(): (x: number, y: number) => readonly Element[] {
    return this.#deps.elementsAt ?? ((x: number, y: number) => (typeof this.#doc.elementsFromPoint === 'function' ? this.#doc.elementsFromPoint(x, y) : []));
  }

  /**
   * Whether the page lays something else over a compact button's spot (§5.1 "The video under the
   * pointer", for a reveal without the pointer): the element topmost at the button's centre (else
   * the video's) is in no part of its player.
   */
  #covered(b: ButtonDom): boolean {
    const r = b.button.getBoundingClientRect();
    const v = r.width > 0 ? r : b.el.getBoundingClientRect();
    if (!(v.width > 0 && v.height > 0)) return false;
    const hit = this.#hitAt(v.left + v.width / 2, v.top + v.height / 2);
    return hit !== undefined && topmostAt([b], hit, this.#viewport().width) === undefined;
  }

  /**
   * Whether the page shows a compact button's video at the centre of its visible part (§5.1 "The
   * video under the pointer"): its player holds the element topmost there. A post viewer or a
   * prompt laid over the whole video does not; a layer over a strip of it (a post's header) does.
   */
  #shows(b: ButtonDom): boolean {
    const r = b.el.getBoundingClientRect();
    const { width, height } = this.#viewport();
    const l = Math.max(r.left, 0);
    const t = Math.max(r.top, 0);
    const right = width > 0 ? Math.min(r.right, width) : r.right;
    const bottom = height > 0 ? Math.min(r.bottom, height) : r.bottom;
    if (!(right > l && bottom > t)) return false;
    return topmostAt([b], this.#hitAt((l + right) / 2, (t + bottom) / 2), width) !== undefined;
  }

  /** The page's element topmost at a point, past this layer. */
  #hitAt(x: number, y: number): Element | undefined {
    return this.#elementsAt()(x, y).find((e) => e !== this.host);
  }

  /**
   * Places a compact button (§5.1 "A free corner"): its corner is chosen again only while it is
   * shown or the pointer is over its video, when the video's size changed or CORNER_RECHECK_MS
   * passed; otherwise it keeps its place relative to the video's visible part.
   */
  #placeSocial(b: ButtonDom, rect: RectLike, width: number, height: number, over: boolean): void {
    const l = Math.max(rect.left, 0);
    const t = Math.max(rect.top, 0);
    const m = b.corner;
    const now = this.#timers.now();
    const active = over || b.button.dataset.shown === 'true' || (this.panel.isOpen && this.#panelFor === b.id);
    const stale =
      !m ||
      Math.abs(m.width - rect.width) > CORNER_SLACK_PX ||
      Math.abs(m.height - rect.height) > CORNER_SLACK_PX ||
      (active && now - m.at >= CORNER_RECHECK_MS);
    let p: SocialPlacement | undefined;
    if (stale && (active || !m)) {
      p = placeSocial(
        rect,
        width,
        height,
        (box) => this.#controlAt(box, b.el, rect, width, height, SOCIAL_CONTROLS) ?? (b.id === TILE_ID ? this.#tilePart(box, b.el, rect, width, height) : undefined),
      );
      b.corner = { placement: p, left: l, top: t, width: rect.width, height: rect.height, at: now };
    } else if (m?.placement) {
      const dx = l - m.left;
      const dy = t - m.top;
      const q = m.placement;
      p = { ...q, top: Math.round(q.top + dy), x: Math.round(q.side === 'left' ? q.x + dx : q.x - dx) };
    }
    if (!p) {
      b.button.hidden = true;
      return;
    }
    b.button.dataset.size = p.size;
    b.button.style.top = `${p.top}px`;
    b.button.style.setProperty('--dx-max', `${p.maxWidth}px`);
    if (p.side === 'right') {
      b.button.style.right = `${p.x}px`;
      b.button.style.left = 'auto';
    } else {
      b.button.style.left = `${p.x}px`;
      b.button.style.right = 'auto';
    }
  }

  /** What a compact button tells is learnt for its video's source and post (§5.1). */
  #identity(b: ButtonDom): string {
    const src = b.video && typeof b.video.currentSrc === 'string' ? b.video.currentSrc : '';
    return `${b.post ?? ''}\n${src}`;
  }

  /** Writes a compact button's label, name and state. */
  #drawTells(b: ButtonDom): void {
    const base = b.id === TILE_ID ? TILE_LABEL : BUTTON_LABEL;
    const id = this.#identity(b);
    const hint = b.hint?.for === id ? b.hint.text : undefined;
    const state = b.state?.for === id ? b.state.kind : undefined;
    const wide = b.button.dataset.wide === 'true';
    if (state) {
      if (b.button.dataset.state !== state) b.button.dataset.state = state;
    } else if (b.button.dataset.state !== undefined) delete b.button.dataset.state;
    // The longest form that fits its widest (a grid tile's is narrow): the hint without the word,
    // "Sent" for "Sent to Downex"; the accessible name keeps everything.
    const forms = state === 'sent' && wide ? [SENT_LABEL, 'Sent'] : hint ? [`Download · ${hint}`, hint, 'Download'] : ['Download'];
    const room = b.corner?.placement ? b.corner.placement.maxWidth - LABEL_CHROME_PX : Number.POSITIVE_INFINITY;
    // Written only when it changed: this runs on every reveal by the pointer.
    const text = forms.find((f) => f.length * LABEL_CHAR_PX <= room) ?? forms[forms.length - 1]!;
    if (b.label.textContent !== text) b.label.textContent = text;
    const tells = [hint ? hint.replace(' · ', ', ') : '', state === 'sent' ? 'sent to Downex' : ''].filter((x) => x !== '');
    const name = [base, ...tells].join(', ');
    if (b.button.getAttribute('aria-label') !== name) b.button.setAttribute('aria-label', name);
  }

  /** The video changed (a recycled player, another post): the button forgets what it told. */
  #forget(b: ButtonDom): void {
    b.hint = undefined;
    b.state = undefined;
    b.hintAskedAt = Number.NEGATIVE_INFINITY;
    if (b.wideTimer !== undefined) this.#timers.clearTimeout(b.wideTimer);
    b.wideTimer = undefined;
    delete b.button.dataset.wide;
    this.#drawTells(b);
  }

  /**
   * A compact button is revealed: what it told is checked against its video (a recycled player
   * forgets it), and, revealed by the pointer without a hint, the background's memory is asked for
   * one (`downex:frame-offers`, nothing sent to Downex) at most once per HINT_ASK_MS, on a frame
   * whose own address is a curated video or for a post a click found (§5.1 "What the button tells").
   */
  #refreshTells(b: ButtonDom, hover: boolean): void {
    const id = this.#identity(b);
    if ((b.hint && b.hint.for !== id) || (b.state && b.state.for !== id)) {
      if (b.hint?.for !== id) b.hint = undefined;
      if (b.state?.for !== id) b.state = undefined;
    }
    this.#drawTells(b);
    if (!hover || b.hint) return;
    const now = this.#timers.now();
    if (now - b.hintAskedAt < HINT_ASK_MS) return;
    const frame = frameAddress(this.#deps.location?.() ?? this.#doc.location?.href ?? '');
    const own = parseSiteVideo(frame);
    if (!b.post && (!own || own.resolver === 'network')) return;
    b.hintAskedAt = now;
    const src = b.video && /^https?:/i.test(b.video.currentSrc ?? '') ? b.video.currentSrc : '';
    const mse = b.video ? /^blob:/i.test(b.video.currentSrc ?? '') : false;
    const msg: ExtensionMessage = b.post ? { type: 'downex:frame-offers', src, mse, post: b.post } : { type: 'downex:frame-offers', src, mse };
    void this.#api.send(msg).then((answer) => {
      const o = answer as Outcome<FrameOffers> | undefined;
      const probe = o?.ok ? o.value.site?.probe : undefined;
      const info = probe && 'result' in probe && probe.result.ok ? probe.result.value : undefined;
      const text = info ? infoHint(info) : undefined;
      if (!text || this.#identity(b) !== id || this.#buttons.get(b.id) !== b) return;
      b.hint = { text, for: id };
      this.#drawTells(b);
    });
  }

  /**
   * Keeps the grid tile's button (§5.1 "Grid tiles"): true while the tracker has a tile on a
   * compact site that holds none of the `chosen` videos (those with a button of their own: one
   * that mounts there takes over; a hover preview too small for a button does not), or its panel
   * is open. It moves to another tile only while its panel is closed.
   */
  #syncTile(chosen: readonly TrackedVideo[]): boolean {
    const b = this.#buttons.get(TILE_ID);
    if (b && this.panel.isOpen && this.#panelFor === TILE_ID) return true;
    const tile = this.#compact && this.#api.allowed ? this.#api.tile : undefined;
    if (!tile || chosen.some((v) => tile.el.contains(v.el))) return false;
    if (!b) {
      this.#addButton({ id: TILE_ID, el: tile.el as unknown as HTMLVideoElement, meta: TILE_META }, tile);
      return true;
    }
    if (b.el !== tile.el) {
      b.el = tile.el;
      b.post = tile.url;
      b.corner = undefined;
      this.#forget(b);
    }
    return true;
  }

  #viewport(): { width: number; height: number } {
    const el = this.#doc.documentElement;
    const win = this.#doc.defaultView;
    return { width: el.clientWidth || win?.innerWidth || 0, height: el.clientHeight || win?.innerHeight || 0 };
  }

  #addButton(v: TrackedVideo, tile?: { el: Element; url: string }): void {
    const doc = this.#doc;
    const button = doc.createElement('button');
    button.type = 'button';
    button.className = 'dx-button';
    button.setAttribute('aria-label', tile ? TILE_LABEL : BUTTON_LABEL);
    button.setAttribute('aria-haspopup', 'dialog');
    button.setAttribute('aria-expanded', 'false');
    button.setAttribute('aria-controls', this.panelDom.el.id);
    button.dataset.shown = 'false';
    button.dataset.video = String(v.id);
    const quiet = v.quiet === true && !v.meta.meaningful;
    if (quiet) button.dataset.quiet = 'true';
    const label = doc.createElement('span');
    label.className = 'dx-label';
    label.textContent = 'Download';
    const mark = downexMark(doc);
    if (this.#compact) {
      // The compact mark (§5.1 "Social sites"): the check of a video Downex took sits in the orb.
      button.dataset.social = 'true';
      const sent = glyph(doc, 'check');
      sent.setAttribute('class', 'dx-sent');
      mark.append(sent);
    }
    button.append(mark, label);
    const id = v.id;
    // Pointing at a button gets the video's qualities ahead under the look-ahead's rules (§5.5).
    button.addEventListener('pointerenter', () => this.#deps.api.lookAheadNow?.());
    // The pointer on a compact button keeps it shown (§5.1 "Button"); leaving it, it shows for the
    // usual time. The YouTube pill keeps its own timing.
    if (this.#compact) {
      button.addEventListener('pointerenter', () => {
        this.#hovered = id;
        if (button.dataset.shown === 'true') this.reveal(id, REVEAL_ACTIVITY_MS, true);
      });
      button.addEventListener('pointerleave', () => {
        if (this.#hovered !== id) return;
        this.#hovered = undefined;
        if (button.dataset.shown === 'true') this.reveal(id, REVEAL_ACTIVITY_MS, true);
      });
    }
    button.addEventListener('click', (e) => {
      if (!this.#isTrusted(e)) return;
      if (this.panel.isOpen && this.#panelFor === id) this.closePanel(true);
      else this.openPanel(id);
    });
    this.root.insertBefore(button, this.panelDom.el);
    this.#buttons.set(id, {
      id,
      el: tile ? tile.el : v.el,
      video: tile ? undefined : v.el,
      button,
      label,
      revealTimer: undefined,
      quiet,
      social: this.#compact,
      wideTimer: undefined,
      hintAskedAt: Number.NEGATIVE_INFINITY,
      post: tile?.url,
    });
    // A video that started (or loaded) before its button existed shows it for the rest of the time.
    const since = v.startedAt === undefined ? Number.POSITIVE_INFINITY : this.#timers.now() - v.startedAt;
    if (since >= 0 && since < REVEAL_LOAD_MS) this.reveal(id, REVEAL_LOAD_MS - since);
  }

  /** The page removed the host (it rewrote the children of <html>): it goes back. */
  #reattach(): void {
    if (this.#mounted && !this.host.isConnected) this.#parent().append(this.host);
  }

  #dropButton(b: ButtonDom): void {
    if (this.#hovered === b.id) this.#hovered = undefined;
    if (b.revealTimer !== undefined) this.#timers.clearTimeout(b.revealTimer);
    if (b.wideTimer !== undefined) this.#timers.clearTimeout(b.wideTimer);
    b.button.remove();
  }

  /** Pointer movement: the buttons are placed again, then those under the pointer are revealed. */
  #onPointer(e: PointerEvent): void {
    const x = e.clientX;
    const y = e.clientY;
    if (typeof x !== 'number' || typeof y !== 'number') return;
    this.#pointer = { x, y };
    this.#last = { x, y };
    this.schedulePlace();
  }

  /** Whether the pointer's last position is on a button (§5.1 "Button"). */
  #restsOn(b: ButtonDom): boolean {
    const p = this.#last;
    if (!p || b.button.dataset.shown !== 'true') return false;
    const r = b.button.getBoundingClientRect();
    return r.width > 0 && p.x >= r.left && p.x <= r.right && p.y >= r.top && p.y <= r.bottom;
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
    const b = this.#panelFor === undefined ? undefined : this.#buttons.get(this.#panelFor);
    if (b?.social && this.panel.isOpen) this.#tellFromView(b, view);
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

  /**
   * The panel's view tells its compact button (§5.1 "What the button tells"): the default row's
   * quality and size as its hint, a turning ring while it works, and "Sent to Downex" once Downex
   * took the video (wide for SENT_CLOSE_MS, then the check alone).
   */
  #tellFromView(b: ButtonDom, view: OfferPanelView): void {
    const id = this.#identity(b);
    const row = view.rows.find((r) => r.id === view.defaultId) ?? view.groups.flatMap((g) => g.rows).find((r) => r.id === view.defaultId);
    // A file row names its quality in its detail ("360p · MP4").
    const text = row ? (hintText(row.quality, row.size) ?? hintText(row.detail, row.size)) : undefined;
    if (text && !view.part?.expanded) b.hint = { text, for: id };
    const sentBefore = b.state?.kind === 'sent' && b.state.for === id;
    if (view.phase === 'sent') {
      if (!sentBefore) {
        b.state = { kind: 'sent', for: id };
        b.button.dataset.wide = 'true';
        if (b.wideTimer !== undefined) this.#timers.clearTimeout(b.wideTimer);
        b.wideTimer = this.#timers.setTimeout(() => {
          b.wideTimer = undefined;
          delete b.button.dataset.wide;
          this.#drawTells(b);
        }, SENT_CLOSE_MS);
      }
    } else if (view.working || view.phase === 'loading' || view.phase === 'sending') {
      if (!sentBefore || view.phase === 'sending') b.state = { kind: 'working', for: id };
    } else if (b.state?.kind === 'working') {
      b.state = undefined;
    }
    this.#drawTells(b);
  }

  async #hide(): Promise<void> {
    const hidden = await this.panel.hide();
    // The tracker asks `downex:overlay-state` again; buttons not allowed unmount the layer.
    if (hidden) this.#api.refreshState();
  }
}

/** How many times each element loaded another video since the layer first asked (loadGeneration). */
const loads = new WeakMap<HTMLVideoElement, { n: number; durationMs: number }>();

/**
 * How many times a `<video>` loaded another video since its panel first asked: its media was emptied
 * or loaded again (`emptied`, `loadstart`), or its duration moved by more than the matching
 * tolerance (max(0.3 s, 1.5 %)), as a feed's player does when it plays the next post in the same
 * element through a MediaSource whose address stays empty (SITE-MEDIA-SPEC §2.9 "The flow" 3).
 */
export function loadGeneration(v: HTMLVideoElement): number {
  let g = loads.get(v);
  if (!g) {
    const state = { n: 0, durationMs: elementDuration(v) };
    g = state;
    loads.set(v, state);
    const next = () => {
      state.n++;
      state.durationMs = 0;
    };
    try {
      v.addEventListener('emptied', next);
      v.addEventListener('loadstart', next);
      v.addEventListener('durationchange', () => {
        const d = elementDuration(v);
        if (d > 0 && state.durationMs > 0 && Math.abs(d - state.durationMs) > Math.max(300, state.durationMs * 0.015)) state.n++;
        if (d > 0) state.durationMs = d;
      });
    } catch {
      // not an element that dispatches events (a stand-in)
    }
  }
  return g.n;
}

/** A short hash of a text (FNV-1a, 32 bits, base 36): an identity that never carries the text itself. */
export function shortHash(text: string): string {
  let h = 0x811c9dc5;
  for (let i = 0; i < text.length; i++) {
    h ^= text.charCodeAt(i);
    h = Math.imul(h, 0x01000193) >>> 0;
  }
  return h.toString(36);
}

/** The grid tile's stand-in state: no element, so no source, never protected or live at click time. */
/** The `blob:` preview a grid tile plays (MSE), if any: its Played source (SITE-MEDIA-SPEC §2.9). */
function tilePreview(tile: Element): HTMLVideoElement | undefined {
  for (const v of Array.from(tile.querySelectorAll?.('video') ?? [])) {
    const src = (v as HTMLVideoElement).currentSrc ?? '';
    if (/^blob:/i.test(src) || (v as HTMLVideoElement & { srcObject?: unknown }).srcObject) return v as HTMLVideoElement;
  }
  return undefined;
}

const TILE_META = {
  id: TILE_ID,
  src: '',
  mse: false,
  width: 0,
  height: 0,
  videoWidth: 0,
  videoHeight: 0,
  durationMs: -1,
  drm: false,
  visible: true,
  decorative: false,
  meaningful: true,
};

/** A video element's own duration in ms when finite and above 0, else 0 (the Part row's fallback, §5.2). */
export function elementDuration(v: HTMLVideoElement | undefined): number {
  const d = v ? (v as { duration?: unknown }).duration : undefined;
  return typeof d === 'number' && Number.isFinite(d) && d > 0 ? Math.min(Number.MAX_SAFE_INTEGER, Math.round(d * 1000)) : 0;
}

/** Whether a video has played some of its media (its `played` ranges, else its position). */
export function hasPlayed(v: HTMLVideoElement | undefined): boolean {
  if (!v) return false;
  const ranges = (v as { played?: { length?: unknown } }).played;
  if (typeof ranges?.length === 'number' && ranges.length > 0) return true;
  return typeof v.currentTime === 'number' && v.currentTime > 0;
}

/**
 * Starts the layer for the tracker's API: mounted while buttons are allowed or the context menu
 * asked for a video's panel (opened at once, also when that came before this script was
 * injected), unmounted when neither holds, removed for good when the extension goes away.
 * Undefined when the API is missing, of another protocol version, invalidated, or already has a
 * layer.
 */
export function startOverlayUi(api: unknown, deps: Omit<OverlayLayerDeps, 'api'>): OverlayLayer | undefined {
  if (!isTrackerApi(api) || api.invalidated || !api.claimUi()) return undefined;
  const layer = new OverlayLayer({ ...deps, api });
  const apply = () => {
    if (api.allowed || api.requested !== undefined) layer.mount();
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
      case 'tile':
        layer.tileChanged();
        return;
      case 'open':
        apply();
        layer.showRequested(e.id);
        return;
    }
  });
  apply();
  if (api.requested !== undefined) layer.showRequested(api.requested);
  return layer;
}
