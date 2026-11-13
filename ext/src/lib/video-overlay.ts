// The "Download" button over a YouTube player and its quality panel (SITE-MEDIA-SPEC §5.1).
// Built with DOM calls only (no HTML strings: YouTube enforces Trusted Types) inside a closed
// shadow root that the content script provides; site strings are set as text. The look is
// Downex's own: its mark and the colour tokens of `assets/ui.css`, on a dark translucent pill.

import type { OverlayStateResponse } from './messages';
import { buttonAllowed, type VideoOptions } from './options-form';
import type { AddedResult, Outcome, Timers, VideoInfoResult } from './protocol';
import { systemTimers } from './protocol';
import { addedTone, initialCapital, type Tone } from './ui-format';
import type { TrackPick } from './video-picker';
import { MP4_NOTE, VideoPanel, type VideoPanelView } from './video-panel';

/**
 * Events stopped at the shadow root, so the player's shortcuts, play toggle and double-click
 * fullscreen never fire for the overlay; `mousemove` passes, so the player's controls still hide.
 */
export const ISOLATED_EVENTS = [
  'keydown',
  'keyup',
  'keypress',
  'click',
  'dblclick',
  'mousedown',
  'mouseup',
  'pointerdown',
  'pointerup',
  'contextmenu',
  'wheel',
  'touchstart',
  'touchend',
] as const;

/** How long the button stays after pointer activity in the player … */
export const REVEAL_ACTIVITY_MS = 3000;
/** … and after a new video loaded (`loadstart`). */
export const REVEAL_LOAD_MS = 5000;
/** The panel closes this long after the video was handed to Downex. */
export const SENT_CLOSE_MS = 3000;

/** The button's accessible name (every overlay). */
export const BUTTON_LABEL = 'Download this video with Downex';
/** Bidirectional controls (U+202A–202E, U+2066–2069) are dropped from site strings. */
const BIDI = /[‪-‮⁦-⁩]/g;
const SVG_NS = 'http://www.w3.org/2000/svg';

/** Site text as shown: no bidi controls. */
export function cleanSiteText(s: string): string {
  return s.replace(BIDI, '');
}

/**
 * Whether the YouTube overlay shows its button (VIDEO-DETECT-SPEC §5.4): the background's answer
 * to `downex:overlay-state`, which decides by the tab's host (embedded players follow the site
 * that embeds them). Until it answers, the top frame decides by its own host; an embedded player
 * waits.
 */
export function youtubeButtonEnabled(options: VideoOptions, state: OverlayStateResponse | undefined, topHost: string | undefined): boolean {
  if (state) return state.button;
  return topHost !== undefined && buttonAllowed(options, topHost);
}

/** Stops the events of ISOLATED_EVENTS at `target` (the shadow root). */
export function isolateEvents(target: EventTarget): void {
  for (const type of ISOLATED_EVENTS) target.addEventListener(type, (e) => e.stopPropagation());
}

/**
 * The YouTube overlay's `:host` rule: the shadow host inside the player covers the player and
 * lets the pointer through except over the button and the panel.
 */
export const OVERLAY_HOST_CSS = `
:host {
  position: absolute !important;
  inset: 0 !important;
  display: block !important;
  pointer-events: none !important;
  z-index: 2147483000 !important;
  overflow: visible !important;
}
`;

/**
 * The rules every Downex overlay shares ("Glass"): tokens, the button's pill, the panel and its
 * rows, status, action, skeleton and meter. The layer over any video (`overlay-host.ts`) puts its
 * own `:host` rule before them and its own rules after them (VIDEO-DETECT-SPEC §5.1).
 *
 * - The button is part of the video's chrome: always smoked glass with a white label. The panel
 *   follows the user's colour scheme: smoked glass, or frosted light glass. `color-scheme: dark`
 *   is the root's, so that only the light block sets the panel's own (light scrollbars and
 *   UA-drawn parts on the light glass).
 * - Contrast never depends on the blur: every tint is opaque enough that each text pair passes
 *   4.5:1 over a pure white or pure black page behind it, sheen and row tints included (worst:
 *   4.63, muted text on the default row of the dark panel over white). The light set writes in
 *   `#11613f` and keeps `#187a50` for fills; its glass (0.94) also hides the page well where the
 *   blur is not drawn (software raster). A row that cannot be used now keeps its text (muted)
 *   and dims only its disc.
 * - A closed popover is hidden in the same frame (`:not(:popover-open)`), not only after the
 *   asynchronous `toggle` event resets `data-open`.
 * - A list taller than the panel fades out at the visible bottom while more is below
 *   (`data-more`, overflowCue); frames up to 420 px tall draw a quality row on one line.
 * - A backdrop blur is re-composited every frame over a playing video, so the button blurs only
 *   while shown and the panel only while open; reduced transparency, forced colours and browsers
 *   without `backdrop-filter` get solid surfaces.
 * - One self-contained string: no `url()`, `@import`, `@property` or `@font-face` (no
 *   web-accessible resources; WXT would move the last two into the page). System fonts only.
 */
export const OVERLAY_SHARED_CSS = `.dx {
  --b-glass: rgb(14 18 16 / 0.78);
  --b-solid: #0e1210;
  --b-edge: rgb(255 255 255 / 0.18);
  --b-blur: blur(14px) saturate(170%);
  --p-blur: blur(26px) saturate(180%);
  --p-glass: rgb(17 22 20 / 0.86);
  --p-solid: #161c19;
  --p-edge: rgb(255 255 255 / 0.12);
  --p-hi: rgb(255 255 255 / 0.05);
  --p-line: rgb(255 255 255 / 0.1);
  --p-hover: rgb(255 255 255 / 0.07);
  --p-current: rgb(147 224 186 / 0.13);
  --p-current-edge: rgb(147 224 186 / 0.45);
  --p-chip: rgb(255 255 255 / 0.09);
  --p-fg: #f1f5f2;
  --p-muted: #c0cac4;
  --p-accent: #93e0ba;
  --p-accent-fill: #93e0ba;
  --p-action-fill: #a6e9c8;
  --p-accent-fg: #0b2a1c;
  --p-warn: #f0c870;
  --p-error: #ffb0a6;
  --p-info: #9cc8f2;
  --p-focus: #93e0ba;
  --p-fade: rgb(17 22 20 / 0.94);
  --p-shadow: 0 28px 64px -16px rgb(0 0 0 / 0.65), 0 2px 10px rgb(0 0 0 / 0.3);
  --ease: cubic-bezier(0.2, 0.8, 0.3, 1);
  --spring: cubic-bezier(0.3, 1.25, 0.5, 1);
  color-scheme: dark;
  position: absolute;
  inset: 0;
  pointer-events: none;
  font: 13px/1.4 system-ui, -apple-system, 'Segoe UI', Roboto, 'Helvetica Neue', sans-serif;
  color: var(--p-fg);
  -webkit-font-smoothing: antialiased;
}
@media (prefers-color-scheme: light) {
  .dx-panel {
    --p-glass: rgb(250 252 251 / 0.94);
    --p-solid: #f9fbfa;
    --p-edge: rgb(20 40 30 / 0.14);
    --p-hi: rgb(255 255 255 / 0.9);
    --p-line: rgb(20 40 30 / 0.12);
    --p-hover: rgb(20 40 30 / 0.06);
    --p-current: rgb(24 122 80 / 0.1);
    --p-current-edge: rgb(24 122 80 / 0.42);
    --p-chip: rgb(20 40 30 / 0.07);
    --p-fg: #121614;
    --p-muted: #47514b;
    --p-accent: #11613f;
    --p-accent-fill: #187a50;
    --p-action-fill: #1a8054;
    --p-accent-fg: #ffffff;
    --p-warn: #714900;
    --p-error: #a1201a;
    --p-info: #1d5a9c;
    --p-focus: #187a50;
    --p-fade: rgb(250 252 251 / 0.97);
    --p-shadow: 0 28px 64px -16px rgb(0 0 0 / 0.5), 0 2px 10px rgb(0 0 0 / 0.18);
    color-scheme: light;
  }
}
.dx[hidden], .dx [hidden] { display: none !important; }
.dx *, .dx *::before, .dx *::after { box-sizing: border-box; }
.dx svg { width: 15px; height: 15px; flex: none; fill: none; stroke: currentColor; stroke-width: 1.8; stroke-linecap: round; stroke-linejoin: round; }
.dx-button {
  position: absolute;
  top: 12px;
  right: 12px;
  display: inline-flex;
  align-items: center;
  gap: 7px;
  min-height: 32px;
  padding: 0 13px 0 3px;
  border: 1px solid var(--b-edge);
  border-radius: 999px;
  background: linear-gradient(180deg, rgb(255 255 255 / 0.12), rgb(255 255 255 / 0) 55%), var(--b-glass);
  box-shadow: inset 0 1px 0 rgb(255 255 255 / 0.14), 0 8px 24px -8px rgb(0 0 0 / 0.6);
  color: #ffffff;
  font: inherit;
  font-weight: 600;
  letter-spacing: 0.01em;
  cursor: pointer;
  pointer-events: auto;
  opacity: 0;
  transform: translateY(-3px) scale(0.98);
  transition: opacity 0.2s var(--ease), transform 0.24s var(--spring), border-color 0.15s, box-shadow 0.2s, backdrop-filter 0.2s;
}
.dx[data-embed='true'] .dx-button { top: 56px; }
.dx[data-shown='true'] .dx-button,
.dx-button:focus-visible,
.dx-button[aria-expanded='true'] { opacity: 1; transform: none; backdrop-filter: var(--b-blur); }
.dx-button:hover,
.dx-button[aria-expanded='true'] {
  border-color: rgb(147 224 186 / 0.75);
  box-shadow: inset 0 1px 0 rgb(255 255 255 / 0.16), 0 8px 24px -8px rgb(0 0 0 / 0.6);
}
.dx-button:focus-visible { outline: 2px solid #93e0ba; outline-offset: 3px; }
.dx-tile {
  display: inline-grid;
  place-items: center;
  width: 24px;
  height: 24px;
  border-radius: 50%;
  flex: none;
  color: #0b2a1c;
  background: #93e0ba;
  box-shadow: inset 0 -1px 0 rgb(0 0 0 / 0.12);
}
.dx-tile svg { width: 14px; height: 14px; stroke-width: 2; }
.dx-panel {
  position: fixed;
  inset: auto;
  margin: 0;
  width: 320px;
  max-width: calc(100vw - 16px);
  max-height: 60vh;
  overflow: auto;
  overscroll-behavior: contain;
  --gap: 6px;
  display: flex;
  flex-direction: column;
  gap: var(--gap);
  padding: 8px;
  border: 1px solid var(--p-edge);
  border-radius: 18px;
  background: linear-gradient(160deg, var(--p-hi), transparent 30%), var(--p-glass);
  color: var(--p-fg);
  box-shadow: inset 0 1px 0 var(--p-hi), var(--p-shadow);
  pointer-events: auto;
  font: 13px/1.4 system-ui, -apple-system, 'Segoe UI', Roboto, 'Helvetica Neue', sans-serif;
  scrollbar-width: thin;
  transform-origin: top right;
}
.dx-panel[data-open='true'] { backdrop-filter: var(--p-blur); animation: dx-pop 0.22s var(--spring) both; }
.dx-panel[data-open='false'] { display: none; }
.dx-panel[popover]:not(:popover-open) { display: none; }
.dx-panel > * { flex: none; }
.dx-panel:focus { outline: none; }
.dx-panel[data-more='true']::after {
  content: '';
  flex: none;
  position: sticky;
  bottom: -8px;
  height: 30px;
  margin-top: calc(-30px - var(--gap));
  background: linear-gradient(to bottom, transparent, var(--p-fade) 85%);
  pointer-events: none;
}
@keyframes dx-pop { from { opacity: 0; transform: translateY(-4px) scale(0.97); } to { opacity: 1; transform: none; } }
.dx-head {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  grid-template-areas: 'title close' 'sub sub';
  align-items: start;
  column-gap: 10px;
  row-gap: 2px;
  padding: 4px 2px 2px 10px;
}
.dx-title {
  grid-area: title;
  align-self: center;
  margin: 0;
  font-size: 14px;
  font-weight: 650;
  line-height: 1.3;
  overflow: hidden;
  overflow-wrap: anywhere;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
}
.dx-sub { grid-area: sub; margin: 0; font-size: 12px; color: var(--p-muted); }
.dx-close {
  grid-area: close;
  display: grid;
  place-items: center;
  width: 28px;
  height: 28px;
  padding: 0;
  border: 0;
  border-radius: 50%;
  background: var(--p-chip);
  color: var(--p-muted);
  cursor: pointer;
}
.dx-close:hover { color: var(--p-fg); background: var(--p-hover); }
.dx-close svg { width: 14px; height: 14px; }
.dx-rows { list-style: none; margin: 0; padding: 0; display: grid; gap: 2px; }
.dx-row {
  width: 100%;
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto 26px;
  grid-template-areas: 'q q go' 'c s go';
  align-items: center;
  align-content: center;
  column-gap: 10px;
  row-gap: 1px;
  min-height: 46px;
  margin: 0;
  padding: 6px 8px 6px 10px;
  border: 1px solid transparent;
  border-radius: 12px;
  background: transparent;
  color: inherit;
  font: inherit;
  text-align: left;
  cursor: pointer;
  transition: background-color 0.14s, border-color 0.14s, opacity 0.14s;
}
.dx-row:hover:not(:disabled) { background: var(--p-hover); }
.dx-row[aria-current='true'] { background: var(--p-current); border-color: var(--p-current-edge); }
.dx-row:focus-visible { outline: 2px solid var(--p-focus); outline-offset: 1px; }
.dx-row:disabled { cursor: default; }
.dx-row:disabled:not([aria-busy='true']):not([data-sent='true']) :is(.dx-q, .dx-s) { color: var(--p-muted); }
.dx-row:disabled:not([aria-busy='true']):not([data-sent='true']) .dx-go { opacity: 0.45; }
.dx-q { grid-area: q; min-width: 0; font-size: 13.5px; font-weight: 650; line-height: 1.3; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.dx-row[aria-current='true'] .dx-q { color: var(--p-accent); }
.dx-c {
  grid-area: c;
  min-width: 0;
  font-size: 12px;
  line-height: 1.35;
  color: var(--p-muted);
  overflow: hidden;
  overflow-wrap: anywhere;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
}
.dx-s { grid-area: s; align-self: start; font-size: 12.5px; font-weight: 550; line-height: 1.35; font-variant-numeric: tabular-nums; color: var(--p-fg); white-space: nowrap; }
.dx-c:empty { display: none; }
.dx-row:has(> .dx-c:empty) { grid-template-areas: 'q s go'; }
.dx-row:has(> .dx-c:empty) .dx-s { align-self: center; }
.dx-go {
  grid-area: go;
  display: grid;
  place-items: center;
  width: 26px;
  height: 26px;
  border-radius: 50%;
  color: var(--p-muted);
  box-shadow: inset 0 0 0 1px var(--p-line);
  transition: background-color 0.14s, color 0.14s, box-shadow 0.2s;
}
.dx-go svg { width: 14px; height: 14px; }
.dx-go .dx-ring, .dx-go .dx-check, .dx-row[aria-busy='true'] .dx-go .dx-arrow, .dx-row[data-sent='true'] .dx-go .dx-arrow { display: none; }
.dx-row[aria-busy='true'] .dx-go .dx-ring, .dx-row[data-sent='true'] .dx-go .dx-check { display: inline; }
.dx-row[aria-busy='true'] .dx-go svg { animation: dx-spin 0.9s linear infinite; }
.dx-row:hover:not(:disabled) .dx-go,
.dx-row:focus-visible .dx-go,
.dx-row[aria-current='true'] .dx-go,
.dx-row[data-sent='true'] .dx-go {
  color: var(--p-accent-fg);
  background: var(--p-accent-fill);
  box-shadow: none;
}
.dx-audio { display: grid; grid-template-columns: auto minmax(0, 1fr); align-items: center; gap: 10px; padding: 4px 2px 2px 10px; font-size: 12px; font-weight: 600; color: var(--p-muted); }
.dx-audio select {
  min-width: 0;
  min-height: 32px;
  padding: 0 28px 0 10px;
  border: 1px solid var(--p-line);
  border-radius: 10px;
  background:
    linear-gradient(45deg, transparent 50%, var(--p-muted) 50%) calc(100% - 15px) 50% / 5px 5px no-repeat,
    linear-gradient(135deg, var(--p-muted) 50%, transparent 50%) calc(100% - 10px) 50% / 5px 5px no-repeat,
    var(--p-chip);
  color: var(--p-fg);
  font: inherit;
  font-weight: 500;
  appearance: none;
  cursor: pointer;
}
.dx-audio select:focus-visible { outline: 2px solid var(--p-focus); outline-offset: 1px; }
.dx-audio select option { background: var(--p-solid); color: var(--p-fg); }
.dx-skel { display: grid; gap: 6px; padding: 2px 8px 4px; }
.dx-skel > i { display: block; height: 36px; border-radius: 10px; background: linear-gradient(90deg, var(--p-chip), var(--p-hover), var(--p-chip)) 0 0 / 200% 100%; animation: dx-shimmer 1.4s linear infinite; }
@keyframes dx-shimmer { to { background-position: -200% 0; } }
.dx-status {
  --t: var(--p-muted);
  display: flex;
  align-items: flex-start;
  gap: 8px;
  margin: 0;
  padding: 8px 10px;
  border-radius: 12px;
  font-size: 12.5px;
  font-weight: 550;
  line-height: 1.4;
  color: var(--t);
  background: color-mix(in srgb, var(--t) 12%, transparent);
  overflow-wrap: anywhere;
}
.dx-status:has(> .dx-status-text:empty) { display: none; }
.dx-status::before {
  content: '';
  flex: none;
  width: 8px;
  height: 8px;
  margin-top: 5px;
  border-radius: 50%;
  background: currentColor;
}
.dx-status[data-tone='ok'] { --t: var(--p-accent); }
.dx-status[data-tone='warn'] { --t: var(--p-warn); }
.dx-status[data-tone='warn']::before { border-radius: 1px; transform: rotate(45deg) scale(0.9); }
.dx-status[data-tone='error'] { --t: var(--p-error); }
.dx-status[data-tone='error']::before { border-radius: 1px; }
.dx-status[data-tone='info'] { --t: var(--p-info); }
.dx-status[data-tone='info']::before {
  width: 10px;
  height: 10px;
  margin-top: 4px;
  border: 1.5px solid currentColor;
  background:
    linear-gradient(currentColor, currentColor) 2.95px 1px / 1.2px 2.8px no-repeat,
    linear-gradient(currentColor, currentColor) 2.95px 2.95px / 2.4px 1.2px no-repeat;
}
.dx-panel[data-working='true'] .dx-status { --t: var(--p-fg); background: var(--p-chip); }
.dx-panel[data-working='true'] .dx-status::before {
  width: 12px;
  height: 12px;
  margin-top: 3px;
  border: 2px solid color-mix(in srgb, currentColor 25%, transparent);
  border-top-color: var(--p-accent-fill);
  border-radius: 50%;
  background: none;
  transform: none;
  animation: dx-spin 0.8s linear infinite;
}
.dx-status[data-note='true'] { position: absolute; width: 1px; height: 1px; margin: -1px; padding: 0; overflow: hidden; clip-path: inset(50%); white-space: nowrap; }
.dx-elapsed { margin-left: auto; padding-left: 6px; white-space: nowrap; color: var(--p-muted); font-variant-numeric: tabular-nums; font-weight: 500; }
.dx-meter { height: 4px; margin: -2px 10px 2px; border-radius: 999px; background: var(--p-chip); overflow: hidden; }
.dx-meter > span { display: block; height: 100%; border-radius: inherit; background: var(--p-accent-fill); transition: width 0.3s var(--ease); }
@keyframes dx-spin { to { transform: rotate(360deg); } }
.dx-action {
  align-self: flex-start;
  display: inline-flex;
  align-items: center;
  gap: 8px;
  min-height: 34px;
  margin: 0;
  padding: 7px 16px 7px 13px;
  border: 0;
  border-radius: 999px;
  background: var(--p-action-fill);
  box-shadow: inset 0 1px 0 rgb(255 255 255 / 0.2);
  color: var(--p-accent-fg);
  font: inherit;
  font-weight: 600;
  line-height: 1.3;
  text-align: left;
  cursor: pointer;
}
.dx-action:disabled { opacity: 0.55; cursor: default; }
.dx-ico { display: inline-grid; place-items: center; flex: none; }
.dx-action:focus-visible, .dx-close:focus-visible { outline: 2px solid var(--p-focus); outline-offset: 2px; }
/* Frames up to 420 px tall (embedded players): a quality row takes one line (its name, size and
   disc; the codec left out, as in the narrow form) and the title one, so that more rows fit; files
   and rows without a size keep their second line. The short form (< 200 px) has its own. */
@media (max-height: 420px) {
  .dx-panel:not([data-short='true']) .dx-title { -webkit-line-clamp: 1; }
  .dx-panel:not([data-short='true']) .dx-row:not([data-source='file']):not(:has(> .dx-s:empty)) {
    grid-template-areas: 'q s go';
    min-height: 36px;
    padding-top: 4px;
    padding-bottom: 4px;
  }
  .dx-panel:not([data-short='true']) .dx-row:not([data-source='file']):not(:has(> .dx-s:empty)) > .dx-c { display: none; }
  .dx-panel:not([data-short='true']) .dx-row:not([data-source='file']):not(:has(> .dx-s:empty)) > .dx-s { align-self: center; }
}
@supports not (backdrop-filter: blur(1px)) {
  .dx-button { background: rgb(14 18 16 / 0.92); }
  .dx-panel { background: var(--p-solid); }
}
@media (prefers-reduced-transparency: reduce) {
  .dx { --b-blur: none; --p-blur: none; }
  .dx-button { background: var(--b-solid); }
  .dx-panel { background: var(--p-solid); }
  .dx-panel[data-more='true']::after { background: linear-gradient(to bottom, transparent, var(--p-solid) 85%); }
}
@media (prefers-reduced-motion: reduce) {
  .dx *, .dx *::before, .dx *::after { animation: none !important; transition: none !important; }
  .dx-button { transform: none; }
}
@media (forced-colors: active) {
  .dx { --b-blur: none; --p-blur: none; }
  .dx-button, .dx-panel, .dx-tile, .dx-row, .dx-go, .dx-status, .dx-action, .dx-close { box-shadow: none; }
  .dx-panel[data-more='true']::after { display: none; }
  .dx-button { background: ButtonFace; color: ButtonText; border: 1px solid ButtonText; }
  .dx-button:hover, .dx-button[aria-expanded='true'] { border-color: Highlight; }
  .dx-button:focus-visible, .dx-row:focus-visible, .dx-action:focus-visible, .dx-close:focus-visible, .dx-audio select:focus-visible { outline: 2px solid Highlight; }
  .dx-tile { forced-color-adjust: none; background: ButtonText; color: ButtonFace; }
  .dx-panel { background: Canvas; color: CanvasText; border: 1px solid CanvasText; }
  .dx-row { border: 1px solid ButtonText; background: ButtonFace; color: ButtonText; }
  .dx-row[aria-current='true'] { border: 2px solid Highlight; }
  .dx-row:hover:not(:disabled) { border-color: Highlight; }
  .dx-row[aria-current='true'] .dx-q, .dx-c, .dx-s { color: ButtonText; }
  .dx-row:disabled:not([aria-busy='true']):not([data-sent='true']) { border-color: GrayText; }
  .dx-row:disabled:not([aria-busy='true']):not([data-sent='true']) :is(.dx-q, .dx-c, .dx-s, .dx-go) { color: GrayText; opacity: 1; }
  .dx-row:hover:not(:disabled) .dx-go, .dx-row:focus-visible .dx-go, .dx-row[aria-current='true']:not(:disabled) .dx-go, .dx-row[data-sent='true'] .dx-go { forced-color-adjust: none; background: ButtonText; color: ButtonFace; }
  .dx-status, .dx-close, .dx-action { border: 1px solid CanvasText; background: Canvas; color: CanvasText; }
  .dx-status::before, .dx-meter > span { background: CanvasText; }
  .dx-status[data-tone='info']::before { background: none; border-color: CanvasText; }
  .dx-panel[data-working='true'] .dx-status::before { background: none; border-color: CanvasText; border-top-color: Canvas; }
  .dx-meter { border: 1px solid CanvasText; background: Canvas; }
  .dx-skel { display: none; }
  .dx-audio { position: relative; }
  .dx-audio::after { content: ''; position: absolute; top: 50%; right: 14px; width: 6px; height: 6px; margin-top: -3px; border: solid FieldText; border-width: 0 1.5px 1.5px 0; transform: translateY(-1px) rotate(45deg); pointer-events: none; }
  .dx-audio select { border: 1px solid ButtonText; background: Field; color: FieldText; }
}
`;

/** The YouTube overlay's style sheet: its `:host` rule and the shared rules, unchanged. */
export const OVERLAY_CSS = OVERLAY_HOST_CSS + OVERLAY_SHARED_CSS;

export interface VideoOverlayDeps {
  probe: () => Promise<Outcome<VideoInfoResult>>;
  add: (pick: TrackPick) => Promise<Outcome<AddedResult>>;
  /** Turns on the sign-in option after `auth_required` (undefined: never offered). */
  allowSignIn?: () => Promise<void>;
  /** Whether that option is on now: after `auth_required` "Try again" is offered instead. */
  signInAllowed?: () => boolean;
  timers?: Timers;
  /** Whether an activation came from the user (`event.isTrusted`; tests substitute it). */
  isTrusted?: (e: Event) => boolean;
}

/**
 * The overlay's line glyphs (24-unit grid, stroked with the text colour), drawn for Downex. Each
 * is one or more path data strings; a glyph is built with createElementNS, never from markup.
 */
export const GLYPHS = {
  mark: ['M12 4v11M7.5 10.5 12 15l4.5-4.5M5 19.5h14'],
  close: ['M7 7l10 10M17 7 7 17'],
  /** A row's "download": the arrow, and the ring that turns while that row is being sent. */
  arrow: ['M12 5v10M8 11l4 4 4-4M6.5 19h11'],
  ring: ['M12 4.5a7.5 7.5 0 1 1-7.5 7.5'],
  /** … and the check of the row that was sent. */
  check: ['M6.5 12.5 10 16l7.5-8'],
  more: ['M5 11a6 6 0 1 0 12 0a6 6 0 1 0-12 0', 'm20 20-4.5-4.5M11 8.5v5M8.5 11h5'],
  hide: ['M3.5 3.5l17 17M10.6 6.2A9 9 0 0 1 12 6c5 0 8.5 4.5 9 6-.3.9-1.4 2.6-3.1 4M6.6 7.6C4.7 8.9 3.4 10.8 3 12c.5 1.5 4 6 9 6 1.5 0 2.9-.4 4.1-1M9.9 9.9a3 3 0 0 0 4.2 4.2'],
  retry: ['M19 12a7 7 0 1 1-2.05-4.95M19 4.5V8h-3.5'],
  key: ['M4.5 15a3.5 3.5 0 1 0 7 0a3.5 3.5 0 1 0-7 0', 'M10.5 12.5 19 4M15.5 7.5l2 2M13.5 9.5l1.5 1.5'],
  stream: ['M4 8c2.7-2 5.3-2 8 0s5.3 2 8 0M4 12.5c2.7-2 5.3-2 8 0s5.3 2 8 0M4 17c2.7-2 5.3-2 8 0s5.3 2 8 0'],
  file: [
    'M6.5 4.5h11a2.5 2.5 0 0 1 2.5 2.5v10a2.5 2.5 0 0 1-2.5 2.5h-11A2.5 2.5 0 0 1 4 17V7a2.5 2.5 0 0 1 2.5-2.5z',
    'M8.5 4.5v15M15.5 4.5v15M4 9.5h4.5M4 14.5h4.5M15.5 9.5H20M15.5 14.5H20',
  ],
  site: ['M4 12a8 8 0 1 0 16 0a8 8 0 1 0-16 0', 'M4 12h16M12 4c2.2 2.3 3.2 5 3.2 8s-1 5.7-3.2 8c-2.2-2.3-3.2-5-3.2-8s1-5.7 3.2-8z'],
} as const satisfies Record<string, readonly string[]>;

export type GlyphName = keyof typeof GLYPHS;

/** A glyph as an `<svg>`; its paths get `pathClass` (e.g. to show one of two in CSS). */
export function glyph(doc: Document, name: GlyphName, pathClass?: string): SVGSVGElement {
  const svg = doc.createElementNS(SVG_NS, 'svg');
  svg.setAttribute('viewBox', '0 0 24 24');
  svg.setAttribute('aria-hidden', 'true');
  svg.setAttribute('focusable', 'false');
  for (const d of GLYPHS[name]) {
    const path = doc.createElementNS(SVG_NS, 'path');
    path.setAttribute('d', d);
    if (pathClass) path.setAttribute('class', pathClass);
    svg.append(path);
  }
  return svg;
}

/** Downex's mark: the download glyph on a mint orb (the button's; the panel does not repeat it). */
export function downexMark(doc: Document): HTMLElement {
  const tile = doc.createElement('span');
  tile.className = 'dx-tile';
  tile.setAttribute('aria-hidden', 'true');
  const svg = glyph(doc, 'mark');
  svg.setAttribute('class', 'mark');
  tile.append(svg);
  return tile;
}

/**
 * A row's download disc: the arrow, a turning ring while the row is being sent, or a check once
 * it was sent (`aria-busy`, `data-sent`; CSS shows one).
 */
export function goDisc(doc: Document): HTMLElement {
  const go = doc.createElement('span');
  go.className = 'dx-go';
  go.setAttribute('aria-hidden', 'true');
  const svg = glyph(doc, 'arrow', 'dx-arrow');
  for (const [name, cls] of [['ring', 'dx-ring'], ['check', 'dx-check']] as const) {
    for (const d of GLYPHS[name]) {
      const path = doc.createElementNS(SVG_NS, 'path');
      path.setAttribute('d', d);
      path.setAttribute('class', cls);
      svg.append(path);
    }
  }
  go.append(svg);
  return go;
}

/**
 * The tone a status is drawn in: an accepted hand-off as the popup marks it (`addedTone`: saved at
 * once ok, waiting for the user in Downex info), every other status in its own tone.
 */
export function statusTone(phase: string, status: { text: string; tone: Tone }): Tone | 'info' {
  return phase === 'sent' && status.tone === 'ok' ? addedTone(status.text) : status.tone;
}

/**
 * Keeps `data-more` on a scrolling panel while part of its list is below the visible bottom (a
 * fade says so, CSS). Returns the update, for after a render or a new maximum height.
 */
export function overflowCue(panel: HTMLElement): () => void {
  const update = () => {
    const more = String(panel.scrollHeight - panel.scrollTop - panel.clientHeight > 4);
    if (panel.dataset.more !== more) panel.dataset.more = more;
  };
  panel.addEventListener('scroll', update, { passive: true });
  return update;
}

/** The round close button: a drawn ×, named "Close". */
export function closeButton(doc: Document): HTMLButtonElement {
  const close = doc.createElement('button');
  close.type = 'button';
  close.className = 'dx-close';
  close.setAttribute('aria-label', 'Close');
  close.append(glyph(doc, 'close'));
  return close;
}

/** A button's text beside its glyph: set with setText on the span, so the glyph stays. */
export function labelled(doc: Document, button: HTMLButtonElement, name: GlyphName): HTMLSpanElement {
  const icon = doc.createElement('span');
  icon.className = 'dx-ico';
  icon.append(glyph(doc, name));
  const text = doc.createElement('span');
  text.className = 'dx-txt';
  button.dataset.glyph = name;
  button.append(icon, text);
  return text;
}

/** Sets the glyph of a button made by labelled(). */
export function setGlyph(doc: Document, button: HTMLElement, name: GlyphName): void {
  const icon = button.querySelector('.dx-ico');
  if (!icon || button.dataset.glyph === name) return;
  button.dataset.glyph = name;
  icon.replaceChildren(glyph(doc, name));
}

/** The skeleton rows drawn while the first list loads (decorative). */
export function skeleton(doc: Document): HTMLElement {
  const skel = doc.createElement('div');
  skel.className = 'dx-skel';
  skel.setAttribute('aria-hidden', 'true');
  skel.hidden = true;
  skel.append(doc.createElement('i'), doc.createElement('i'), doc.createElement('i'));
  return skel;
}

/** A thin meter for a status that ends with "(n%)" (the tools download); decorative. */
export function meter(doc: Document): { el: HTMLElement; set: (statusText: string, working: boolean) => boolean } {
  const el = doc.createElement('div');
  el.className = 'dx-meter';
  el.setAttribute('aria-hidden', 'true');
  el.hidden = true;
  const bar = doc.createElement('span');
  el.append(bar);
  return {
    el,
    set(statusText, working) {
      const m = working ? /\((\d{1,3})%\)\s*$/.exec(statusText) : null;
      const pct = m ? Math.min(100, Number(m[1])) : undefined;
      el.hidden = pct === undefined;
      if (pct !== undefined && bar.style.width !== `${pct}%`) bar.style.width = `${pct}%`;
      return pct !== undefined;
    },
  };
}

/** "Every quality is saved as one MP4 file." under the title (aria-hidden: the status says it). */
export function noteLine(doc: Document): HTMLElement {
  const sub = doc.createElement('p');
  sub.className = 'dx-sub';
  sub.setAttribute('aria-hidden', 'true');
  sub.hidden = true;
  sub.textContent = MP4_NOTE;
  return sub;
}

let nextId = 0;

/**
 * The button and its panel for one frame. The content script attaches it to the player's
 * shadow root, tells it which video the frame shows and when to reveal the button.
 */
export class VideoOverlay {
  readonly root: HTMLElement;
  readonly button: HTMLButtonElement;
  readonly panelEl: HTMLElement;
  readonly #doc: Document;
  readonly #deps: VideoOverlayDeps;
  readonly #timers: Timers;
  readonly #isTrusted: (e: Event) => boolean;
  readonly #panel: VideoPanel;
  readonly #titleEl: HTMLElement;
  /** The MP4 note under the title, while there are qualities. */
  readonly #subEl: HTMLElement;
  readonly #skelEl: HTMLElement;
  readonly #rowsEl: HTMLUListElement;
  readonly #audioEl: HTMLLabelElement;
  readonly #audioSelect: HTMLSelectElement;
  readonly #statusEl: HTMLElement;
  /** The status text, which the live region announces … */
  readonly #statusText: HTMLElement;
  /** … and the seconds spent getting the info, shown but not announced. */
  readonly #elapsedEl: HTMLElement;
  readonly #meter: ReturnType<typeof meter>;
  readonly #actionEl: HTMLButtonElement;
  readonly #actionText: HTMLElement;
  /** The quality rows by video id: updated in place, so that a focused row stays focused. */
  readonly #rowEls = new Map<string, { li: HTMLLIElement; button: HTMLButtonElement; q: HTMLElement; c: HTMLElement; s: HTMLElement }>();
  /** Marks the panel while part of its list is below the visible bottom. */
  readonly #updateMore: () => void;
  #key: string | undefined;
  #enabled = true;
  #open = false;
  #revealTimer: unknown;
  #sentTimer: unknown;
  #lastView: VideoPanelView | undefined;
  readonly #cleanup: (() => void)[] = [];
  readonly #isolated = new WeakSet<ShadowRoot>();
  #listening = false;

  constructor(doc: Document, deps: VideoOverlayDeps) {
    this.#doc = doc;
    this.#deps = deps;
    this.#timers = deps.timers ?? systemTimers;
    this.#isTrusted = deps.isTrusted ?? ((e) => e.isTrusted);
    const id = `dx-${++nextId}`;

    this.root = doc.createElement('div');
    this.root.className = 'dx';
    this.root.hidden = true;
    this.root.dataset.shown = 'false';
    this.root.dataset.embed = 'false';

    this.button = doc.createElement('button');
    this.button.type = 'button';
    this.button.className = 'dx-button';
    this.button.setAttribute('aria-label', BUTTON_LABEL);
    this.button.setAttribute('aria-haspopup', 'dialog');
    this.button.setAttribute('aria-expanded', 'false');
    this.button.setAttribute('aria-controls', `${id}-panel`);
    const label = doc.createElement('span');
    label.textContent = 'Download';
    this.button.append(downexMark(doc), label);

    this.panelEl = doc.createElement('div');
    this.panelEl.id = `${id}-panel`;
    this.panelEl.className = 'dx-panel';
    this.panelEl.setAttribute('popover', 'auto');
    this.panelEl.setAttribute('role', 'dialog');
    this.panelEl.setAttribute('aria-labelledby', `${id}-title`);
    this.panelEl.tabIndex = -1;
    this.panelEl.dataset.open = 'false';
    this.panelEl.dataset.phase = 'idle';
    this.panelEl.dataset.working = 'false';
    // The head: the title (the button beside the panel carries the mark), a drawn close, and the
    // MP4 note under them.
    const head = doc.createElement('div');
    head.className = 'dx-head';
    this.#titleEl = doc.createElement('h2');
    this.#titleEl.id = `${id}-title`;
    this.#titleEl.className = 'dx-title';
    this.#titleEl.textContent = 'YouTube video';
    const close = closeButton(doc);
    this.#subEl = noteLine(doc);
    head.append(this.#titleEl, close, this.#subEl);
    this.#skelEl = skeleton(doc);
    this.#rowsEl = doc.createElement('ul');
    this.#rowsEl.className = 'dx-rows';
    this.#rowsEl.setAttribute('aria-label', 'MP4 qualities');
    this.#audioEl = doc.createElement('label');
    this.#audioEl.className = 'dx-audio';
    this.#audioEl.hidden = true;
    this.#audioSelect = doc.createElement('select');
    this.#audioEl.append('Audio', this.#audioSelect);
    this.#statusEl = doc.createElement('p');
    this.#statusEl.className = 'dx-status';
    this.#statusEl.setAttribute('role', 'status');
    this.#statusEl.setAttribute('aria-live', 'polite');
    this.#statusText = doc.createElement('span');
    this.#statusText.className = 'dx-status-text';
    this.#elapsedEl = doc.createElement('span');
    this.#elapsedEl.className = 'dx-elapsed';
    this.#elapsedEl.setAttribute('aria-hidden', 'true');
    this.#statusEl.append(this.#statusText, this.#elapsedEl);
    this.#meter = meter(doc);
    this.#actionEl = doc.createElement('button');
    this.#actionEl.type = 'button';
    this.#actionEl.className = 'dx-action';
    this.#actionEl.hidden = true;
    this.#actionText = labelled(doc, this.#actionEl, 'retry');
    this.panelEl.append(head, this.#skelEl, this.#rowsEl, this.#audioEl, this.#statusEl, this.#meter.el, this.#actionEl);
    this.root.append(this.button, this.panelEl);
    this.#updateMore = overflowCue(this.panelEl);

    const panelDeps: ConstructorParameters<typeof VideoPanel>[0] = {
      probe: () => this.#deps.probe(),
      add: (pick) => this.#deps.add(pick),
      render: (view) => this.#render(view),
      timers: this.#timers,
    };
    if (deps.allowSignIn) panelDeps.allowSignIn = deps.allowSignIn;
    if (deps.signInAllowed) panelDeps.signInAllowed = deps.signInAllowed;
    this.#panel = new VideoPanel(panelDeps);

    this.button.addEventListener('click', (e) => {
      if (!this.#isTrusted(e)) return;
      if (this.#open) this.close(true);
      else this.open();
    });
    close.addEventListener('click', (e) => {
      if (this.#isTrusted(e)) this.close(true);
    });
    this.panelEl.addEventListener('keydown', (e) => this.#onKey(e));
    this.#rowsEl.addEventListener('click', (e) => {
      if (!this.#isTrusted(e)) return;
      const row = (e.target as Element | null)?.closest?.('button.dx-row') as HTMLButtonElement | null;
      if (row?.dataset.id) void this.#panel.download(row.dataset.id);
    });
    this.#audioSelect.addEventListener('change', () => this.#panel.chooseAudio(this.#audioSelect.value));
    this.#actionEl.addEventListener('click', (e) => {
      if (!this.#isTrusted(e)) return;
      const kind = this.#lastView?.action?.kind;
      if (kind === 'allow-sign-in') void this.#panel.allowSignIn();
      else if (kind === 'retry') this.#panel.retry();
    });
    // Light dismiss (Esc, a click elsewhere) of the popover closes the panel too.
    this.panelEl.addEventListener('toggle', (e) => {
      if ((e as Event & { newState?: string }).newState === 'closed' && this.#open) this.close(false);
    });
  }

  /** Puts the overlay into `container` of the shadow root `shadow`, stopping the player's events there. */
  attach(container: HTMLElement, shadow: ShadowRoot): void {
    if (!this.#isolated.has(shadow)) {
      this.#isolated.add(shadow);
      isolateEvents(shadow);
    }
    if (!this.#listening) {
      this.#listening = true;
      const onFullscreen = () => this.close(false);
      this.#doc.addEventListener('fullscreenchange', onFullscreen);
      this.#cleanup.push(() => this.#doc.removeEventListener('fullscreenchange', onFullscreen));
      if (!this.#popoverApi) {
        // Without the Popover API a click outside the overlay closes the panel.
        const onDown = (e: Event) => {
          if (this.#open && !e.composedPath().includes(this.root)) this.close(false);
        };
        this.#doc.addEventListener('pointerdown', onDown, true);
        this.#cleanup.push(() => this.#doc.removeEventListener('pointerdown', onDown, true));
      }
    }
    if (this.root.parentNode !== container) container.append(this.root);
  }

  /**
   * The video the frame shows (undefined: no video page, e.g. the miniplayer or a channel).
   * Another video closes the panel and forgets the first one's qualities.
   */
  setVideo(key: string | undefined, embed = false): void {
    this.root.dataset.embed = String(embed);
    if (key !== this.#key) {
      this.close(false);
      this.#panel.reset();
      this.#key = key;
    }
    this.#syncHidden();
  }

  /** "Show a Download button over videos" and the hidden sites (youtubeButtonEnabled). */
  setEnabled(enabled: boolean): void {
    this.#enabled = enabled;
    if (!enabled) this.close(false);
    this.#syncHidden();
  }

  get isOpen(): boolean {
    return this.#open;
  }

  get videoKey(): string | undefined {
    return this.#key;
  }

  /** Shows the button for `ms` (pointer activity, a new video loading). */
  reveal(ms: number): void {
    this.root.dataset.shown = 'true';
    if (this.#revealTimer !== undefined) this.#timers.clearTimeout(this.#revealTimer);
    this.#revealTimer = this.#timers.setTimeout(() => {
      this.#revealTimer = undefined;
      if (!this.#open) this.root.dataset.shown = 'false';
    }, ms);
  }

  open(): void {
    if (this.#open || this.root.hidden) return;
    this.#open = true;
    this.root.dataset.shown = 'true';
    this.button.setAttribute('aria-expanded', 'true');
    this.panelEl.dataset.open = 'true';
    if (this.#popoverApi) {
      try {
        (this.panelEl as HTMLElement & { showPopover(): void }).showPopover();
      } catch {
        // not connected yet, or already shown
      }
    }
    this.#position();
    this.#panel.clearSent();
    this.#panel.open();
    this.#focusInside();
  }

  /** Closes the panel; `focusButton` returns the focus to the button (Esc, Close). */
  close(focusButton: boolean): void {
    if (this.#sentTimer !== undefined) this.#timers.clearTimeout(this.#sentTimer);
    this.#sentTimer = undefined;
    if (!this.#open) return;
    this.#open = false;
    this.button.setAttribute('aria-expanded', 'false');
    this.panelEl.dataset.open = 'false';
    if (this.#popoverApi) {
      try {
        (this.panelEl as HTMLElement & { hidePopover(): void }).hidePopover();
      } catch {
        // already hidden
      }
    }
    this.#panel.close();
    if (focusButton) this.button.focus();
    this.reveal(REVEAL_ACTIVITY_MS);
  }

  /** The extension went away (context invalidated): the overlay is removed for good. */
  destroy(): void {
    this.close(false);
    this.#panel.reset();
    for (const undo of this.#cleanup.splice(0)) undo();
    this.#listening = false;
    if (this.#revealTimer !== undefined) this.#timers.clearTimeout(this.#revealTimer);
    this.root.remove();
  }

  get #popoverApi(): boolean {
    return typeof (this.panelEl as Partial<{ showPopover: unknown }>).showPopover === 'function';
  }

  #syncHidden(): void {
    const hidden = !this.#enabled || this.#key === undefined;
    this.root.hidden = hidden;
    if (hidden) this.close(false);
  }

  /** Places the panel under the button, right-aligned and kept inside the window. */
  #position(): void {
    const view = this.#doc.defaultView;
    const vw = view?.innerWidth || this.#doc.documentElement.clientWidth || 0;
    const vh = view?.innerHeight || this.#doc.documentElement.clientHeight || 0;
    const r = this.button.getBoundingClientRect();
    const width = Math.max(0, Math.min(320, vw - 16));
    const left = Math.max(8, Math.min(r.right - width, vw - width - 8));
    const top = Math.max(8, r.bottom + 8);
    this.panelEl.style.left = `${Math.round(left)}px`;
    this.panelEl.style.top = `${Math.round(top)}px`;
    if (vh > 0) this.panelEl.style.maxHeight = `${Math.max(160, Math.round(vh - top - 8))}px`;
    this.#updateMore();
  }

  #focusInside(): void {
    const row = this.#rowsEl.querySelector<HTMLButtonElement>('button.dx-row[aria-current="true"]:not(:disabled)');
    (row ?? this.panelEl).focus();
  }

  /** The focused element of the overlay's root (its shadow root), or null. */
  #activeElement(): Element | null {
    return (this.root.getRootNode() as Document | ShadowRoot).activeElement ?? null;
  }

  /** Whether the focus is in the panel. */
  #focusInPanel(): boolean {
    const active = this.#activeElement();
    return active !== null && this.panelEl.contains(active);
  }

  /**
   * After a render, keeps the focus in the panel when it was there: on the element that had it
   * while that can still take it, else on the chosen quality, else on the panel itself (where
   * Up, Down and Esc still work), so that keys never fall through to the page.
   */
  #keepFocus(was: Element | null): void {
    if (!was || !this.#open) return;
    const usable =
      was.isConnected &&
      !(was as HTMLButtonElement | HTMLSelectElement).disabled &&
      !(was as HTMLElement).closest('[hidden]');
    if (usable) {
      if (this.#activeElement() !== was) (was as HTMLElement).focus();
      return;
    }
    this.#focusInside();
  }

  /** The rows of `view`, made or updated in place, in its order. */
  #renderRows(view: VideoPanelView): void {
    const disabled = view.busy || view.phase === 'sent';
    // The row being sent shows it (a turning ring), then that it was sent (a check, at full
    // strength), the others rest: VideoPanel sends the chosen quality, the selected row.
    const sending = view.phase === 'sending';
    const sent = view.phase === 'sent';
    const lis: HTMLLIElement[] = [];
    const ids = new Set<string>();
    for (const r of view.rows) {
      ids.add(r.id);
      let el = this.#rowEls.get(r.id);
      if (!el) {
        const li = this.#doc.createElement('li');
        const button = this.#doc.createElement('button');
        button.type = 'button';
        button.className = 'dx-row';
        button.dataset.id = r.id;
        const q = this.#doc.createElement('span');
        q.className = 'dx-q';
        const c = this.#doc.createElement('span');
        c.className = 'dx-c';
        const s = this.#doc.createElement('span');
        s.className = 'dx-s';
        button.append(q, c, s, goDisc(this.#doc));
        li.append(button);
        el = { li, button, q, c, s };
        this.#rowEls.set(r.id, el);
      }
      const label = cleanSiteText(r.label);
      if (el.button.disabled !== disabled) el.button.disabled = disabled;
      if (sending && r.selected) el.button.setAttribute('aria-busy', 'true');
      else el.button.removeAttribute('aria-busy');
      if (sent && r.selected) el.button.dataset.sent = 'true';
      else delete el.button.dataset.sent;
      if (el.button.getAttribute('aria-current') !== String(r.selected)) el.button.setAttribute('aria-current', String(r.selected));
      if (el.button.getAttribute('aria-label') !== label) el.button.setAttribute('aria-label', label);
      // The standard name, the codec small (every quality is one MP4) and the size; the
      // picture ("3840×1634") only as the tooltip.
      if (el.button.title !== r.picture) el.button.title = r.picture;
      setText(el.q, cleanSiteText(r.quality));
      setText(el.c, r.codecs || 'MP4');
      setText(el.s, r.size);
      lis.push(el.li);
    }
    for (const id of [...this.#rowEls.keys()]) if (!ids.has(id)) this.#rowEls.delete(id);
    const current = [...this.#rowsEl.children];
    if (current.length !== lis.length || current.some((li, i) => li !== lis[i])) this.#rowsEl.replaceChildren(...lis);
  }

  #onKey(e: KeyboardEvent): void {
    if (e.key === 'Escape') {
      e.preventDefault();
      this.close(true);
      return;
    }
    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
    const rows = [...this.#rowsEl.querySelectorAll<HTMLButtonElement>('button.dx-row')];
    if (rows.length === 0) return;
    const active = (this.root.getRootNode() as Document | ShadowRoot).activeElement;
    const i = rows.indexOf(active as HTMLButtonElement);
    const next = e.key === 'ArrowDown' ? (i < 0 ? 0 : Math.min(rows.length - 1, i + 1)) : i < 0 ? rows.length - 1 : Math.max(0, i - 1);
    e.preventDefault();
    rows[next]?.focus();
  }

  #render(view: VideoPanelView): void {
    const hadRows = this.#rowsEl.childElementCount > 0;
    const focused = this.#focusInPanel() ? this.#activeElement() : null;
    this.#lastView = view;
    setText(this.#titleEl, cleanSiteText(view.title) || 'YouTube video');
    this.#renderRows(view);
    this.#audioEl.hidden = view.audio === undefined;
    if (view.audio) {
      this.#audioSelect.replaceChildren(
        ...view.audio.options.map((o) => {
          const opt = this.#doc.createElement('option');
          opt.value = o.id;
          opt.textContent = cleanSiteText(o.label);
          return opt;
        }),
      );
      this.#audioSelect.value = view.audio.value;
      this.#audioSelect.disabled = view.busy;
    }
    // The live region changes only when its text does: the seconds beside it are not announced.
    setText(this.#statusText, initialCapital(view.status?.text ?? ''));
    setText(this.#elapsedEl, view.status?.elapsed ?? '');
    if (view.status) this.#statusEl.dataset.tone = statusTone(view.phase, view.status);
    else delete this.#statusEl.dataset.tone;
    // Getting the qualities or sending one is progress (a neutral line with a turning ring), not a
    // warning; the tools download gets a meter, the first list skeleton rows.
    const working = view.phase === 'probing' || view.phase === 'sending';
    this.panelEl.dataset.phase = view.phase;
    this.panelEl.dataset.working = String(working);
    const metered = this.#meter.set(view.status?.text ?? '', working);
    this.#skelEl.hidden = !(view.phase === 'probing' && view.rows.length === 0 && !metered);
    // The MP4 note is drawn under the title while there are qualities. The status of a usable
    // list (phase `list`, tone ok) is that note, as in OfferPanel.view() and the popup: the live
    // region keeps announcing it, visually hidden, so it is neither drawn twice nor lost.
    this.#subEl.hidden = view.rows.length === 0;
    if (view.phase === 'list' && view.status?.tone === 'ok') this.#statusEl.dataset.note = 'true';
    else delete this.#statusEl.dataset.note;
    this.#actionEl.hidden = view.action === undefined;
    setText(this.#actionText, view.action?.text ?? '');
    if (view.action) {
      this.#actionEl.dataset.kind = view.action.kind;
      setGlyph(this.#doc, this.#actionEl, view.action.kind === 'allow-sign-in' ? 'key' : 'retry');
    }
    // The list arrived while the panel was open: the focus moves to the default quality.
    if (this.#open && !hadRows && view.rows.length > 0) this.#focusInside();
    else this.#keepFocus(focused);
    if (view.phase === 'sent' && this.#open && this.#sentTimer === undefined) {
      this.#sentTimer = this.#timers.setTimeout(() => {
        this.#sentTimer = undefined;
        // The focus goes back to the button when it was in the panel, never to the page.
        this.close(this.#focusInPanel());
      }, SENT_CLOSE_MS);
    }
    this.#updateMore();
  }
}

/** Sets an element's text only when it changes (a live region announces every change). */
export function setText(el: HTMLElement, text: string): void {
  if (el.textContent !== text) el.textContent = text;
}

/**
 * Where the overlay goes: the `.html5-video-player` holding the largest visible `<video>`, else
 * that video's offsetParent; null without a visible video (the button is then not shown).
 */
export function findPlayer(doc: Document): HTMLElement | null {
  let best: HTMLVideoElement | undefined;
  let bestArea = 0;
  for (const v of doc.querySelectorAll('video')) {
    const r = v.getBoundingClientRect();
    const area = Math.max(0, r.width) * Math.max(0, r.height);
    if (area > bestArea) {
      best = v;
      bestArea = area;
    }
  }
  if (!best) return null;
  const player = best.closest<HTMLElement>('.html5-video-player');
  if (player) return player;
  const parent = best.offsetParent;
  return parent instanceof HTMLElement ? parent : null;
}
