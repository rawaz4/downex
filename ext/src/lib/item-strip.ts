// The item row of a page with several videos (SITE-MEDIA-SPEC §3.12, VIDEO-DETECT-SPEC §5.2
// "Several videos"): "Video 2 of 3", one small tile per video (the page's thumbnail, else its
// number), the arrow keys moving between them, and the note on the page's other items.
// Shared by the in-page panel (offer-panel.ts) and the popup; DOM calls only, no HTML strings.

import { formatClipTime } from './clip';
import { cleanSiteText } from './ui-format';
import type { VideoItem } from './protocol';
import type { VideoPanelItems } from './video-panel';

/** What a page's items are: tracks on an audio page (SITE-MEDIA-SPEC §3.13), else videos. */
export function itemNoun(audio: boolean | undefined): 'Track' | 'Video' {
  return audio ? 'Track' : 'Video';
}

/** "Video 2 of 3" ("3 videos" while none is chosen); an audio page's "Track 2 of 3". */
export function itemsCountText(items: VideoPanelItems): string {
  const n = items.options.length;
  if (n === 0) return '';
  const noun = itemNoun(items.audio);
  const lower = noun.toLowerCase();
  return items.index >= 0 ? `${noun} ${items.index + 1} of ${n}` : n === 1 ? `1 ${lower}` : `${n} ${lower}s`;
}

/**
 * The note on the other items of the page ("" for none): photos, links, nested lists, live
 * streams, videos without an MP4 offer (protected, members only) and videos past 20 are all left
 * out (SITE-MEDIA-SPEC §3.12), so it names no single cause.
 */
export function itemsLeftOutText(leftOut: number, audio = false): string {
  if (leftOut <= 0) return '';
  if (audio) {
    return leftOut === 1
      ? '1 other item not listed: a link or audio Downex cannot save.'
      : `${leftOut} other items not listed: links or audio Downex cannot save.`;
  }
  return leftOut === 1
    ? '1 other item not listed: a photo, a link or a video Downex cannot save.'
    : `${leftOut} other items not listed: photos, links or videos Downex cannot save.`;
}

/** What the page shows of the video the user clicked (SITE-MEDIA-SPEC §3.12 "Which item"). */
export interface ClickedVideo {
  /** The element's own length in ms (0 unknown). */
  durationMs: number;
  /** The carousel slide the address shows, 1 first (addressSlide); undefined: none. */
  slide?: number;
}

/**
 * The item of the page the user clicked, when what the page shows names one: the one item whose
 * length is the element's (within a second, or 2 % of a long video); else, when the address shows a
 * slide and nothing is left out (every slide is a listed video, in the page's order), the item at
 * that slide, unless its known length is another. Undefined when nothing names one item: the page's
 * default stays.
 */
export function clickedItem(items: readonly VideoItem[], leftOut: number, seen: ClickedVideo): string | undefined {
  const ms = seen.durationMs > 0 && Number.isFinite(seen.durationMs) ? seen.durationMs : 0;
  const near = (it: VideoItem) => it.durationMs > 0 && Math.abs(it.durationMs - ms) <= Math.max(1000, ms * 0.02);
  const sameLength = ms > 0 ? items.filter(near) : [];
  if (sameLength.length === 1) return sameLength[0]!.id;
  const n = seen.slide;
  if (n === undefined || leftOut !== 0 || !Number.isInteger(n) || n < 1 || n > items.length) return undefined;
  const it = items[n - 1]!;
  return ms === 0 || it.durationMs <= 0 || near(it) ? it.id : undefined;
}

/** An item's accessible name: "Video 2: Title, 0:12" (an audio page's "Track 2: …"). */
export function itemLabel(position: number, title: string, durationMs: number, audio = false): string {
  const t = cleanSiteText(title.trim());
  const parts = [`${itemNoun(audio)} ${position}${t ? `: ${t}` : ''}`];
  if (durationMs > 0) parts.push(formatClipTime(durationMs));
  return parts.join(', ');
}

/**
 * The item row's rules (the in-page panel's, after OVERLAY_SHARED_CSS: its custom properties).
 * Tiles are 44 px squares in one scrolling line; the chosen one has the accent ring.
 */
export const ITEM_STRIP_CSS = `
.dx-items { display: grid; gap: 4px; padding: 2px 10px 4px; }
.dx-items-head { display: flex; flex-wrap: wrap; align-items: baseline; column-gap: 8px; row-gap: 2px; min-width: 0; font-size: 12px; color: var(--p-muted); }
.dx-items-count { flex: none; font-weight: 600; color: var(--p-fg); }
.dx-items-note { flex: 1 1 12em; min-width: 0; overflow-wrap: anywhere; }
.dx-items-list { display: flex; gap: 6px; overflow-x: auto; padding: 3px 2px; scrollbar-width: thin; }
.dx-item {
  position: relative;
  flex: none;
  display: grid;
  place-items: center;
  width: 44px;
  height: 44px;
  padding: 0;
  overflow: hidden;
  border: 1px solid var(--p-line);
  border-radius: 9px;
  background: var(--p-chip);
  color: var(--p-fg);
  font: inherit;
  font-size: 13px;
  font-weight: 600;
  cursor: pointer;
}
.dx-item img { position: absolute; inset: 0; width: 100%; height: 100%; object-fit: cover; }
.dx-item-d {
  position: absolute;
  right: 2px;
  bottom: 2px;
  padding: 0 3px;
  border-radius: 4px;
  background: rgba(0, 0, 0, 0.62);
  color: #fff;
  font-size: 9.5px;
  font-weight: 600;
  line-height: 1.35;
}
.dx-item:hover:not(:disabled) { border-color: var(--p-current-edge); }
.dx-item[aria-checked='true'] { border-color: var(--p-accent); box-shadow: 0 0 0 2px var(--p-accent); }
.dx-item:disabled { opacity: 0.55; cursor: default; }
.dx-item:focus-visible { outline: 2px solid var(--p-focus); outline-offset: 3px; }
@media (forced-colors: active) {
  .dx-item { border-color: ButtonText; background: ButtonFace; color: ButtonText; }
  .dx-item[aria-checked='true'] { border: 3px solid Highlight; box-shadow: none; }
  .dx-item-d { background: Canvas; color: CanvasText; }
}
`;

/** The arrow keys' moves along the row. */
const ARROW_STEPS: ReadonlyMap<string, number> = new Map([
  ['ArrowLeft', -1],
  ['ArrowUp', -1],
  ['ArrowRight', 1],
  ['ArrowDown', 1],
]);

export interface ItemStripDeps {
  doc: Document;
  /** An item was chosen by the user (a click or an arrow key). */
  onChoose: (id: string) => void;
  /** Whether an activation came from the user (`event.isTrusted`; tests substitute it). */
  isTrusted?: (e: Event) => boolean;
  /** Draw the page's thumbnails (the in-page panel; the popup draws numbers only). */
  thumbnails: boolean;
}

/**
 * The row's elements. render() keeps each tile (and its image) while its item stays, so a refresh
 * never reloads an image. Thumbnails load without a Referer and never as a CSS URL; one that fails
 * leaves the number.
 */
export class ItemStrip {
  readonly el: HTMLElement;
  readonly #doc: Document;
  readonly #deps: ItemStripDeps;
  readonly #count: HTMLElement;
  readonly #note: HTMLElement;
  readonly #list: HTMLElement;
  readonly #tiles = new Map<string, HTMLButtonElement>();

  constructor(deps: ItemStripDeps) {
    this.#deps = deps;
    const doc = deps.doc;
    this.#doc = doc;
    const trusted = deps.isTrusted ?? ((e: Event) => e.isTrusted);
    this.el = doc.createElement('div');
    this.el.className = 'dx-items';
    this.el.hidden = true;
    const head = doc.createElement('div');
    head.className = 'dx-items-head';
    this.#count = doc.createElement('span');
    this.#count.className = 'dx-items-count';
    this.#note = doc.createElement('span');
    this.#note.className = 'dx-items-note';
    head.append(this.#count, this.#note);
    this.#list = doc.createElement('div');
    this.#list.className = 'dx-items-list';
    this.#list.setAttribute('role', 'radiogroup');
    this.#list.setAttribute('aria-label', 'Videos of this page');
    this.el.append(head, this.#list);
    this.#list.addEventListener('click', (e) => {
      if (!trusted(e)) return;
      const tile = (e.target as Element | null)?.closest?.('button.dx-item') as HTMLButtonElement | null;
      if (tile?.dataset.id && !tile.disabled) deps.onChoose(tile.dataset.id);
    });
    this.#list.addEventListener('keydown', (e) => {
      if (!trusted(e)) return;
      const step = ARROW_STEPS.get(e.key);
      const ends = e.key === 'Home' ? 'first' : e.key === 'End' ? 'last' : undefined;
      if (step === undefined && ends === undefined) return;
      const tiles = [...this.#list.querySelectorAll<HTMLButtonElement>('button.dx-item')];
      const at = tiles.findIndex((t) => t === this.#doc.activeElement || t === (e.target as Element | null));
      if (tiles.length === 0) return;
      e.preventDefault();
      e.stopPropagation();
      const next = ends === 'first' ? 0 : ends === 'last' ? tiles.length - 1 : Math.min(tiles.length - 1, Math.max(0, at + (step ?? 0)));
      const tile = tiles[next];
      if (!tile || tile.disabled) return;
      tile.focus();
      if (tile.dataset.id && tile.getAttribute('aria-checked') !== 'true') deps.onChoose(tile.dataset.id);
    });
  }

  /** Draws the row (hidden without items); `busy` disables the tiles. */
  render(items: VideoPanelItems | undefined, busy: boolean): void {
    this.el.hidden = items === undefined;
    if (!items) return;
    const count = itemsCountText(items);
    const note = itemsLeftOutText(items.leftOut, items.audio);
    const listName = items.audio ? 'Tracks of this page' : 'Videos of this page';
    if (this.#list.getAttribute('aria-label') !== listName) this.#list.setAttribute('aria-label', listName);
    if (this.#count.textContent !== count) this.#count.textContent = count;
    if (this.#note.textContent !== note) this.#note.textContent = note;
    this.#list.hidden = items.options.length < 2;
    const keep = new Set<string>();
    const tiles = items.options.map((it, i) => {
      keep.add(it.id);
      let tile = this.#tiles.get(it.id);
      if (!tile) {
        tile = this.#tile(it.thumbnail);
        this.#tiles.set(it.id, tile);
      }
      tile.dataset.id = it.id;
      const n = tile.querySelector('.dx-item-n');
      if (n && n.textContent !== String(i + 1)) n.textContent = String(i + 1);
      const d = tile.querySelector('.dx-item-d') as HTMLElement | null;
      const length = it.durationMs > 0 ? formatClipTime(it.durationMs) : '';
      if (d && d.textContent !== length) d.textContent = length;
      if (d) d.hidden = length === '';
      const label = itemLabel(i + 1, it.title, it.durationMs, items.audio);
      tile.setAttribute('aria-label', label);
      tile.title = label;
      tile.setAttribute('aria-checked', String(it.selected));
      // One tab stop: the chosen tile, else the first.
      tile.tabIndex = it.selected || (items.index < 0 && i === 0) ? 0 : -1;
      tile.disabled = busy;
      return tile;
    });
    for (const id of [...this.#tiles.keys()]) if (!keep.has(id)) this.#tiles.delete(id);
    const same = tiles.length === this.#list.children.length && tiles.every((t, i) => this.#list.children[i] === t);
    if (!same) this.#list.replaceChildren(...tiles);
  }

  #tile(thumbnail: string): HTMLButtonElement {
    const doc = this.#doc;
    const tile = doc.createElement('button');
    tile.type = 'button';
    tile.className = 'dx-item';
    tile.setAttribute('role', 'radio');
    const n = doc.createElement('span');
    n.className = 'dx-item-n';
    n.setAttribute('aria-hidden', 'true');
    tile.append(n);
    if (this.#deps.thumbnails && thumbnail.startsWith('https://')) {
      const img = doc.createElement('img');
      img.alt = '';
      img.draggable = false;
      img.decoding = 'async';
      img.referrerPolicy = 'no-referrer';
      img.setAttribute('loading', 'lazy');
      img.addEventListener('error', () => img.remove(), { once: true });
      img.src = thumbnail;
      tile.append(img);
    }
    const d = doc.createElement('span');
    d.className = 'dx-item-d';
    d.setAttribute('aria-hidden', 'true');
    d.hidden = true;
    tile.append(d);
    return tile;
  }
}
