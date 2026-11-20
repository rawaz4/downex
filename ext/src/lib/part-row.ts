// The Part row of a video panel (MEDIA-SPEC §8, VIDEO-DETECT-SPEC §5.2, SITE-MEDIA-SPEC §5.1):
// "Part of the video", a disclosure whose body is a timeline trimmer. The whole video is a track
// with two handles (start, end), the part between them filled, the rest dimmed, the player's
// position as a playhead and the chapters as gaps in the track (and as chips under it); the times
// read big above it, each a chip that turns into a field for typing a time.
//
// Shared by the in-page panel over any video (offer-panel.ts) and the YouTube panel
// (video-overlay.ts). PartRow is its state (pure); PartTrimmer builds its elements with DOM calls
// only (no HTML strings: Trusted Types), draws a PartView into them and reports what the user does;
// PartPlayer is everything the row does to the page's own <video> element: it reads and sets
// `currentTime`, reads `paused` and `seeking`, calls play() and pause() and listens to their
// events. Nothing else of the page or its player is read (CLEAN-ROOM.md). The time rules and
// texts are lib/clip.ts's.

import {
  chapterLabel,
  checkRange,
  CLIP_TEXTS,
  clipSummary,
  type ClipCheck,
  type ClipRange,
  formatClipTime,
  MIN_CLIP_MS,
  parseClipTime,
  type VideoChapter,
} from './clip';
import { systemTimers, type Timers } from './protocol';
import { cleanSiteText } from './ui-format';

export type PartField = 'from' | 'to';

/** The video a Part row works on: its duration (> 0) and its chapters. */
export interface PartInfo {
  durationMs: number;
  chapters?: VideoChapter[];
}

/** A chapter as the trimmer draws it: on the track and as a chip. */
export interface PartChapterView {
  startMs: number;
  endMs: number;
  title: string;
  /** "1. Intro (0:00–1:20)". */
  label: string;
}

/** The Part row as drawn. */
export interface PartView {
  expanded: boolean;
  /** "Whole video" or "Part 1:20 – 2:45" (closed: always "Whole video"). */
  summary: string;
  durationMs: number;
  /** The handles: the last valid times, the end at durationMs for "to the end". */
  startMs: number;
  endMs: number;
  /** The time chips: the time, or a mistyped time as it was typed. */
  from: string;
  to: string;
  /** No part: the whole video goes. */
  whole: boolean;
  /** "1:25" (the part's length; "" for the whole video) … */
  length: string;
  /** … and "~78 MB at 1080p" ("" unknown). */
  size: string;
  chapters: PartChapterView[];
  /** The chapter the part is exactly (1 first), else 0. */
  chapter: number;
  error?: string;
  /** The chip the error is about. */
  errorField?: PartField;
}

/**
 * "~78 MB at 1080p": a pick's size with its quality's short name (up to " (" or " ·"), for the
 * readout; "" without a size.
 */
export function sizeAt(size: string, quality: string): string {
  if (!size) return '';
  const q = cleanSiteText(quality)
    .replace(/\s*[(·].*$/, '')
    .trim();
  return q ? `${size} at ${q}` : size;
}

/**
 * The Part row's state: open, the part as times in ms (end 0: to the end of the video), and a
 * time typed into a chip that cannot be used (kept as typed, with its error, until the next change).
 * The handles never cross and keep MIN_CLIP_MS between them.
 */
export class PartRow {
  #open = false;
  #start = 0;
  #end = 0;
  #draft: { which: PartField; text: string } | undefined;

  /** Another video: closed, the whole video. */
  reset(): void {
    this.#open = false;
    this.whole();
  }

  get open(): boolean {
    return this.#open;
  }

  /** The disclosure. */
  toggle(): void {
    this.#open = !this.#open;
  }

  /** "Whole video": the part is reset. */
  whole(): void {
    this.#start = 0;
    this.#end = 0;
    this.#draft = undefined;
  }

  /**
   * A handle moved (a drag, a key, Start here, End here) to `ms` of a video of `durationMs`:
   * clamped to the video, and short of the other handle by MIN_CLIP_MS. With `push` (Start here,
   * End here) the other handle gives way instead (the end goes to the video's end, the start to
   * 0). False: nothing changed.
   */
  setMs(which: PartField, ms: number, durationMs: number, push = false): boolean {
    if (!Number.isFinite(ms) || !(durationMs > 0)) return false;
    const dur = durationMs;
    const min = Math.min(MIN_CLIP_MS, dur);
    let start = Math.min(Math.max(0, this.#start), dur);
    let end = this.#end > 0 && this.#end < dur ? this.#end : dur;
    const t = Math.min(Math.max(0, Math.round(ms)), dur);
    if (which === 'from') {
      if (push && t > end - min) end = dur;
      start = Math.min(t, Math.max(0, end - min));
    } else {
      if (push && t < start + min) start = 0;
      end = Math.max(t, Math.min(dur, start + min));
    }
    const nextEnd = end >= dur ? 0 : end;
    if (start === this.#start && nextEnd === this.#end && this.#draft === undefined) return false;
    this.#start = start;
    this.#end = nextEnd;
    this.#draft = undefined;
    return true;
  }

  /**
   * A time typed into a chip and committed (Enter, or the field left): "" is the video's start or
   * end. A time that fits is taken; one that does not (mistyped, or against the rules) is kept as
   * typed and shown with its error, the handles staying where they were.
   */
  edit(which: PartField, text: string, durationMs: number): void {
    const v = text.trim() === '' ? 0 : parseClipTime(text);
    if (v !== undefined) {
      const c = checkRange(which === 'from' ? v : this.#start, which === 'to' ? v : this.#end, durationMs);
      if (!c.error) {
        this.#start = c.clip?.startMs ?? 0;
        this.#end = c.clip?.endMs ?? 0;
        this.#draft = undefined;
        return;
      }
    }
    this.#draft = { which, text };
  }

  /** A chapter (1 first) becomes the part; an unknown one changes nothing. */
  chooseChapter(n: number, chapters: readonly VideoChapter[], durationMs: number): void {
    const c = chapters[n - 1];
    if (!c) return;
    this.#start = Math.max(0, c.startMs);
    this.#end = durationMs > 0 && c.endMs >= durationMs ? 0 : c.endMs;
    this.#draft = undefined;
  }

  /** The row's check for the video `info`: undefined while the row is closed or not shown. */
  check(info: PartInfo | undefined): ClipCheck | undefined {
    if (!info || !this.#open) return undefined;
    const d = this.#draft;
    if (d) {
      const v = d.text.trim() === '' ? 0 : parseClipTime(d.text);
      if (v === undefined) return { error: CLIP_TEXTS.badTime };
      return checkRange(d.which === 'from' ? v : this.#start, d.which === 'to' ? v : this.#end, info.durationMs);
    }
    return checkRange(this.#start, this.#end, info.durationMs);
  }

  /** The part to send with a download (undefined: the whole video, or an error). */
  clip(info: PartInfo | undefined): ClipRange | undefined {
    return this.check(info)?.clip;
  }

  /** The chip an error is about: the one typed into, else From. */
  errorField(): PartField {
    return this.#draft?.which ?? 'from';
  }

  /** The row as drawn for the video `info`; `size`: the readout's "~78 MB at 1080p". */
  view(info: PartInfo, size = ''): PartView {
    const dur = Math.max(0, info.durationMs);
    const check = this.check(info);
    const startMs = Math.min(Math.max(0, this.#start), dur);
    const endMs = this.#end > 0 ? Math.min(Math.max(this.#end, startMs), dur) : dur;
    const whole = check !== undefined ? check.error === undefined && check.clip === undefined : this.#start === 0 && this.#end === 0;
    const d = this.#draft;
    const chapters = (info.chapters ?? []).map((c, i) => ({
      startMs: c.startMs,
      endMs: Math.min(c.endMs, dur),
      title: cleanSiteText(c.title.trim()) || `Chapter ${i + 1}`,
      label: chapterLabel(c, i),
    }));
    const chapter = d ? 0 : chapters.findIndex((c) => c.startMs === startMs && (c.endMs === endMs || (endMs >= dur && c.endMs >= dur))) + 1;
    const view: PartView = {
      expanded: this.#open,
      summary: clipSummary(check?.clip, dur),
      durationMs: dur,
      startMs,
      endMs,
      from: d?.which === 'from' ? d.text : formatClipTime(startMs),
      to: d?.which === 'to' ? d.text : formatClipTime(endMs),
      whole,
      length: whole ? '' : formatClipTime(endMs - startMs),
      // A part that cannot be sent has no size: the panel's would be the whole video's.
      size: check?.error ? '' : size,
      chapters,
      chapter,
    };
    if (check?.error) {
      view.error = check.error;
      view.errorField = this.errorField();
    }
    return view;
  }
}

// ---------------------------------------------------------------------------------------------
// The page's video

/**
 * What the Part row uses of the page's own `<video>` (an HTMLVideoElement is one): its position,
 * its media's length, whether it is paused, seeking or ended, play and pause, and their events.
 */
export interface PartVideo {
  currentTime: number;
  readonly paused: boolean;
  readonly seeking: boolean;
  /** The media's length in s: NaN with no media, Infinity for a stream (undefined: not known). */
  readonly duration?: number;
  readonly ended?: boolean;
  play(): Promise<void> | void;
  pause(): void;
  addEventListener(type: string, listener: (e: Event) => void): void;
  removeEventListener(type: string, listener: (e: Event) => void): void;
}

/** A handle let go (or the preview's end) shows its frame this long before the player is put back. */
export const SCRUB_LINGER_MS = 1200;
/** How often the preview checks whether the part's end was reached. */
export const PREVIEW_TICK_MS = 40;
/** A seek this far from the trimmer's own is the user's: the player is left to them. */
const USER_SEEK_MS = 750;
/**
 * The element plays the video when its media is this close to the video's length (or 2 % of it):
 * otherwise it plays something else, as an ad in YouTube's own element.
 */
const SAME_LENGTH_MS = 2000;
/** A pause this close to the part's end (or the media's) during a preview is its end, not the user's. */
const END_SLACK_MS = 250;
/** The player's events the hold follows: emptied and loadstart are other media in the element. */
const PLAYER_EVENTS = ['play', 'pause', 'seeking', 'seeked', 'ended', 'emptied', 'loadstart'] as const;

/**
 * The trimmer's hold on the page's video. The first scrub (a handle moving) or a preview remembers
 * where the video was and whether it played, pauses it and seeks it; the handle let go, or the
 * preview's end, shows the frame for SCRUB_LINGER_MS and then puts the video back; closing the row
 * or the panel puts it back at once. A play, pause or seek the user makes in the player meanwhile
 * is their choice to stay: the video is left where they put it. Other media in the element (the next
 * video of a single-page site, an ad) ends the hold without touching it, and an element whose media
 * is not as long as the video (`expect`) is not the video: nothing reads or drives it.
 */
export class PartPlayer {
  readonly #source: () => PartVideo | undefined;
  readonly #timers: Timers;
  readonly #onChange: () => void;
  #mode: 'idle' | 'scrub' | 'linger' | 'preview' = 'idle';
  #saved: { video: PartVideo; ms: number; paused: boolean } | undefined;
  /** The trimmer's last seek (ms), and one waiting while the video still seeks. */
  #asked: number | undefined;
  #pending: number | undefined;
  #timer: unknown;
  #endMs = 0;
  /** The video's length (0: not known, any media is taken). */
  #expectMs = 0;
  readonly #onEvent = (e: Event) => this.#event(e.type);

  /** `onChange`: a preview started or ended, or the hold was given up (the Preview button follows). */
  constructor(source: () => PartVideo | undefined, timers: Timers, onChange: () => void) {
    this.#source = source;
    this.#timers = timers;
    this.#onChange = onChange;
  }

  /** The video is `durationMs` long: an element playing media of another length is not it. */
  expect(durationMs: number): void {
    this.#expectMs = durationMs > 0 ? durationMs : 0;
  }

  /** The page's element, while it has a position, whatever it plays. */
  element(): PartVideo | undefined {
    let v: PartVideo | undefined;
    try {
      v = this.#source();
    } catch {
      v = undefined;
    }
    return v && Number.isFinite(v.currentTime) && v.currentTime >= 0 ? v : undefined;
  }

  /** The page's video: the element, while it plays the video. */
  video(): PartVideo | undefined {
    const v = this.element();
    return v && this.#plays(v) ? v : undefined;
  }

  /** Whether the element's media is the video (by its length; an unknown length or a stream is). */
  #plays(v: PartVideo): boolean {
    const d = v.duration;
    if (d === undefined || this.#expectMs === 0) return true;
    if (Number.isNaN(d)) return false;
    if (!Number.isFinite(d)) return true;
    return Math.abs(d * 1000 - this.#expectMs) <= Math.max(SAME_LENGTH_MS, this.#expectMs * 0.02);
  }

  /** The video's position in ms. */
  timeMs(): number | undefined {
    const v = this.video();
    return v ? Math.round(v.currentTime * 1000) : undefined;
  }

  get previewing(): boolean {
    return this.#mode === 'preview';
  }

  /** A handle moved to `ms`: the video shows that frame (paused, the first time remembered). */
  scrub(ms: number): void {
    const v = this.#begin();
    if (!v) return;
    this.#clearTimer();
    const was = this.#mode;
    this.#mode = 'scrub';
    if (!v.paused) v.pause();
    this.#seek(v, ms);
    if (was === 'preview') this.#onChange();
  }

  /** The handle was let go: its frame stays a moment, then the video is put back. */
  release(): void {
    if (this.#mode !== 'scrub') return;
    this.#mode = 'linger';
    this.#later(SCRUB_LINGER_MS, () => this.restore());
  }

  /** Plays the part [startMs, endMs) in the page's player, stopping at its end. */
  preview(startMs: number, endMs: number): void {
    const v = this.#begin();
    if (!v) return;
    this.#clearTimer();
    this.#mode = 'preview';
    this.#endMs = endMs;
    this.#seek(v, startMs);
    play(v);
    this.#tick();
    this.#onChange();
  }

  /** Stop: the preview ends and the video goes back at once. */
  stopPreview(): void {
    if (this.#mode === 'preview') this.restore();
  }

  /**
   * Puts the video back where it was, playing or paused as it was (nothing held: nothing done; other
   * media in the element now: it is left alone).
   */
  restore(): void {
    const saved = this.#saved;
    if (!saved) return;
    if (!this.#plays(saved.video)) {
      this.#leave();
      return;
    }
    this.#release();
    const v = saved.video;
    try {
      v.currentTime = saved.ms / 1000;
      if (saved.paused) v.pause();
      else play(v);
    } catch {
      // the video went away
    }
    this.#onChange();
  }

  /** Lets go of the video where it is (another video is coming): nothing is put back. */
  forget(): void {
    if (this.#saved) this.#leave();
  }

  #begin(): PartVideo | undefined {
    const v = this.video();
    if (this.#saved && this.#saved.video !== v) this.restore();
    if (!v) return undefined;
    if (!this.#saved) {
      this.#saved = { video: v, ms: Math.round(v.currentTime * 1000), paused: v.paused };
      for (const t of PLAYER_EVENTS) v.addEventListener(t, this.#onEvent);
    }
    return v;
  }

  /** Lets go of the video without touching it. */
  #release(): void {
    this.#clearTimer();
    const v = this.#saved?.video;
    if (v) for (const t of PLAYER_EVENTS) v.removeEventListener(t, this.#onEvent);
    this.#saved = undefined;
    this.#mode = 'idle';
    this.#asked = undefined;
    this.#pending = undefined;
  }

  /** The user took the player over: it stays where they put it. */
  #leave(): void {
    this.#release();
    this.#onChange();
  }

  #event(type: string): void {
    const v = this.#saved?.video;
    if (!v) return;
    if (type === 'emptied' || type === 'loadstart') {
      this.#leave();
    } else if (type === 'seeked') {
      const p = this.#pending;
      this.#pending = undefined;
      if (p !== undefined) this.#seek(v, p);
    } else if (type === 'seeking') {
      if (this.#pending === undefined && this.#asked !== undefined && Math.abs(v.currentTime * 1000 - this.#asked) > USER_SEEK_MS) this.#leave();
    } else if (type === 'play') {
      // The trimmer holds the video paused while scrubbing: a play is the user's.
      if (this.#mode === 'scrub' || this.#mode === 'linger') this.#leave();
    } else if (type === 'pause') {
      // During a preview the trimmer pauses only after it left 'preview'; the element pauses itself
      // at its media's end (then 'ended' follows).
      if (this.#mode === 'preview') {
        if (this.#atEnd(v, END_SLACK_MS)) this.#finish();
        else this.#leave();
      }
    } else if (type === 'ended' && this.#mode === 'preview') {
      this.#finish();
    }
  }

  /** Seeks to `ms`, or once the video's current seek is over (the latest wins). */
  #seek(v: PartVideo, ms: number): void {
    const target = Math.max(0, Math.round(ms));
    if (v.seeking && this.#asked !== undefined) {
      this.#pending = target;
      return;
    }
    this.#asked = target;
    try {
      v.currentTime = target / 1000;
    } catch {
      // not seekable now
    }
  }

  #tick(): void {
    this.#later(PREVIEW_TICK_MS, () => {
      const v = this.#saved?.video;
      if (this.#mode !== 'preview' || !v) return;
      if (this.#pending === undefined && !v.seeking && this.#atEnd(v, PREVIEW_TICK_MS / 2)) this.#finish();
      else this.#tick();
    });
  }

  /** The preview is at the part's end, or the media's (which may be a little short of the video's). */
  #atEnd(v: PartVideo, slack: number): boolean {
    if (v.ended === true) return true;
    const d = v.duration;
    const end = d !== undefined && Number.isFinite(d) && d > 0 ? Math.min(this.#endMs, d * 1000) : this.#endMs;
    return v.currentTime * 1000 >= end - slack;
  }

  /** The preview reached the part's end: paused there a moment, then the video goes back. */
  #finish(): void {
    const v = this.#saved?.video;
    if (!v) return;
    this.#mode = 'linger';
    v.pause();
    this.#onChange();
    this.#later(SCRUB_LINGER_MS, () => this.restore());
  }

  #later(ms: number, fn: () => void): void {
    this.#clearTimer();
    this.#timer = this.#timers.setTimeout(() => {
      this.#timer = undefined;
      fn();
    }, ms);
  }

  #clearTimer(): void {
    if (this.#timer !== undefined) this.#timers.clearTimeout(this.#timer);
    this.#timer = undefined;
  }
}

/** play(), whose refusal (an autoplay rule) is the page's business. */
function play(v: PartVideo): void {
  try {
    const p = v.play();
    if (p && typeof (p as Promise<void>).catch === 'function') (p as Promise<void>).catch(() => undefined);
  } catch {
    // refused
  }
}

// ---------------------------------------------------------------------------------------------
// DOM

/**
 * The Part row's rules, part of the rules every overlay shares (OVERLAY_SHARED_CSS): the panel's
 * tokens (`--p-trim*` for the trimmer's Mint), so smoked glass and the light glass alike.
 *
 * - The toggle: the label on one line and the summary ("Whole video") beside it while closed; open,
 *   the readout says it all, so the summary fades out.
 * - The readout: the times as chips, and under them the part's length and size, or (`data-error`)
 *   why a typed time cannot be used, the track dimmed meanwhile.
 * - The track: a rail with ruler ticks; the part filled with a Mint gradient between two Mint
 *   handles joined by a frame, the rest dimmed; the playhead; chapters as 3 px gaps (2 px past 12
 *   chapters; past 24, short ticks in place of the ruler's). Room above the rail (`--tip`) holds the
 *   bubble, so that it never covers the readout.
 * - Positions are fractions in `--s`, `--e` (the handles), `--p` (the playhead) and `--t` (the
 *   bubble) on `.dx-trim`; they glide unless a handle is dragged (`data-drag`). The part is drawn
 *   at least `--gap` wide (`--vs`, `--ve`), so a short part of a long video still shows between
 *   its handles.
 * - The body opens and closes by its height (grid rows 0fr → 1fr), its parts rising in turn, the
 *   fill sweeping in and the handles popping in; a grabbed handle grows. Reduced motion stops all of
 *   it (the shared rule).
 * - The body reads left to right whatever the page's direction (`dir="ltr"`).
 */
export const PART_CSS = `.dx-part { display: grid; font-size: 12px; color: var(--p-muted); }
.dx-part-toggle { display: flex; align-items: center; gap: 9px; width: 100%; min-height: 34px; padding: 0 8px 0 7px; border: 0; border-radius: 11px; background: transparent; color: var(--p-fg); font: inherit; font-size: 12.5px; font-weight: 600; text-align: left; cursor: pointer; transition: background-color 0.14s; }
.dx-part-toggle:hover:not(:disabled) { background: var(--p-hover); }
.dx-part-toggle:disabled { cursor: default; }
.dx-part-ico { display: inline-grid; place-items: center; flex: none; width: 24px; height: 24px; border-radius: 8px; color: var(--p-accent); background: var(--p-current); box-shadow: inset 0 0 0 1px var(--p-current-edge); }
.dx-part-ico svg { width: 14px; height: 14px; }
.dx-part-label { flex: none; white-space: nowrap; }
.dx-part-sum { margin-inline-start: auto; min-width: 0; padding: 2px 0; font-weight: 500; color: var(--p-muted); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; font-variant-numeric: tabular-nums; transition: opacity 0.2s, visibility 0s; }
.dx-part-toggle[aria-expanded='true'] .dx-part-sum { opacity: 0; visibility: hidden; transition: opacity 0.12s, visibility 0s linear 0.12s; }
.dx-part-chev { display: inline-grid; place-items: center; flex: none; color: var(--p-muted); transition: transform 0.3s var(--ease); }
.dx-part-chev svg { width: 14px; height: 14px; }
.dx-part-toggle[aria-expanded='true'] .dx-part-chev { transform: rotate(180deg); }
.dx-part-body { display: grid; grid-template-rows: 0fr; opacity: 0; visibility: hidden; transition: grid-template-rows 0.3s var(--ease), opacity 0.16s, visibility 0s linear 0.3s; }
.dx-part-body[data-open='true'] { grid-template-rows: 1fr; opacity: 1; visibility: visible; transition: grid-template-rows 0.42s var(--ease), opacity 0.3s 0.04s, visibility 0s; }
.dx-part-inner { min-height: 0; overflow: hidden; }
.dx-part-pad { display: grid; gap: 8px; padding: 6px 6px 6px 8px; }
.dx-part-body[data-open='true'] .dx-part-pad > * { animation: dx-rise 0.44s var(--ease) backwards; }
.dx-part-body[data-open='true'] .dx-part-pad > :nth-child(2) { animation-delay: 0.05s; }
.dx-part-body[data-open='true'] .dx-part-pad > :nth-child(3) { animation-delay: 0.1s; }
.dx-part-body[data-open='true'] .dx-part-pad > :nth-child(n + 4) { animation-delay: 0.15s; }
.dx-part-body[data-open='true'] :is(.dx-trim-fill, .dx-trim-frame) { animation: dx-sweep 0.7s var(--ease) 0.12s backwards; }
.dx-part-body[data-open='true'] .dx-trim-handle { animation: dx-grab 0.55s var(--spring) 0.22s backwards; }
@keyframes dx-rise { from { opacity: 0; transform: translateY(6px); } }
@keyframes dx-sweep { from { clip-path: inset(0 100% 0 0); } to { clip-path: inset(0 0 0 0); } }
@keyframes dx-grab { from { opacity: 0; transform: scale(0.4); } }
.dx-trim-read { display: grid; grid-template-columns: minmax(0, 1fr) auto; align-items: center; column-gap: 8px; }
.dx-trim-times { display: flex; align-items: center; gap: 2px; min-width: 0; }
.dx-trim-times > svg { width: 15px; height: 15px; margin: 0 2px; color: var(--p-muted); }
.dx-trim-chip { min-width: 0; min-height: 30px; padding: 0 6px; border: 1px solid transparent; border-radius: 9px; background: transparent; color: var(--p-fg); font: inherit; font-size: 18px; font-weight: 680; line-height: 1; letter-spacing: -0.01em; font-variant-numeric: tabular-nums; white-space: nowrap; cursor: text; transition: background-color 0.14s, border-color 0.14s; }
.dx-trim-chip:hover:not(:disabled) { background: var(--p-chip); border-color: var(--p-line); }
.dx-trim-chip:disabled { cursor: default; }
.dx-trim-chip[data-invalid='true'] { color: var(--p-error); border-color: var(--p-error); }
.dx-trim-edit { width: 6.5em; min-height: 30px; padding: 0 7px; border: 1px solid var(--p-focus); border-radius: 9px; background: var(--p-chip); color: var(--p-fg); font: inherit; font-size: 15px; font-weight: 600; font-variant-numeric: tabular-nums; }
.dx-trim-edit[aria-invalid='true'] { border-color: var(--p-error); }
.dx-trim-detail { grid-column: 1 / -1; margin: 0; padding-left: 7px; color: var(--p-muted); font-variant-numeric: tabular-nums; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.dx-trim-detail > b { color: var(--p-accent); font-weight: 650; }
.dx-trim-whole { display: inline-flex; align-items: center; gap: 5px; min-height: 26px; padding: 0 9px 0 7px; border: 0; border-radius: 999px; background: transparent; color: var(--p-muted); font: inherit; font-weight: 600; cursor: pointer; }
.dx-trim-whole svg { width: 13px; height: 13px; }
.dx-trim-whole:hover:not(:disabled) { color: var(--p-fg); background: var(--p-hover); }
.dx-trim { --rt: 10px; --rh: 32px; --tip: 16px; --gap: 10px; --vs: clamp(0%, min(var(--s) * 100%, (var(--s) + var(--e)) * 50% - var(--gap) / 2), 100% - var(--gap)); --ve: max(var(--e) * 100%, var(--vs) + var(--gap)); position: relative; height: calc(var(--tip) + var(--rh) + 2 * var(--rt)); margin: 0 -2px; touch-action: none; user-select: none; -webkit-user-select: none; cursor: pointer; }
.dx-trim-box { position: absolute; inset: var(--tip) 16px 0; }
.dx-trim-rail { position: absolute; left: 0; right: 0; top: var(--rt); height: var(--rh); border-radius: 9px; overflow: hidden; background: repeating-linear-gradient(90deg, var(--p-trim-tick) 0 1px, transparent 1px 6px) 0 50% / 100% 12px no-repeat, var(--p-chip); box-shadow: inset 0 0 0 1px var(--p-line); }
.dx-trim-seg { position: absolute; top: 0; bottom: 0; transition: background-color 0.15s; }
.dx-trim-seg + .dx-trim-seg { border-left: 3px solid var(--p-solid); }
.dx-trim-rail:is([data-dense='1'], [data-dense='2']) { background: var(--p-chip); }
.dx-trim-rail[data-dense='1'] .dx-trim-seg + .dx-trim-seg { border-left-width: 2px; }
.dx-trim-rail[data-dense='2'] .dx-trim-seg + .dx-trim-seg { border-left: 0; }
.dx-trim-rail[data-dense='2'] .dx-trim-seg + .dx-trim-seg::before { content: ''; position: absolute; left: 0; top: 28%; bottom: 28%; width: 1px; background: color-mix(in srgb, var(--p-fg) 30%, transparent); }
.dx-trim-seg[data-hover='true'] { background: var(--p-hover); }
.dx-trim-fill { position: absolute; top: 0; bottom: 0; left: var(--vs); right: calc(100% - var(--ve)); background: linear-gradient(180deg, color-mix(in srgb, var(--p-trim) 46%, transparent), color-mix(in srgb, var(--p-trim) 18%, transparent)); }
.dx-trim-dim { position: absolute; top: 0; bottom: 0; background: var(--p-trim-dim); }
.dx-trim-dim[data-side='before'] { left: 0; right: calc(100% - var(--vs)); }
.dx-trim-dim[data-side='after'] { left: var(--ve); right: 0; }
.dx-trim-frame { position: absolute; top: calc(var(--rt) - 2px); height: calc(var(--rh) + 4px); left: var(--vs); right: calc(100% - var(--ve)); border-top: 3px solid var(--p-trim); border-bottom: 3px solid var(--p-trim); pointer-events: none; }
.dx-trim-head { position: absolute; top: calc(var(--rt) - 5px); height: calc(var(--rh) + 8px); left: calc(var(--p) * 100%); width: 2px; margin-left: -1px; border-radius: 1px; background: var(--p-fg); box-shadow: 0 0 0 1px var(--p-trim-halo); pointer-events: none; transition: left 0.25s linear; }
.dx-trim-head::before { content: ''; position: absolute; top: -3px; left: -3px; width: 8px; height: 8px; border-radius: 50%; background: var(--p-fg); box-shadow: 0 0 0 1px var(--p-trim-halo); }
.dx-trim-handle { position: absolute; top: calc(var(--rt) - 2px); width: 16px; height: calc(var(--rh) + 4px); display: grid; place-items: center; background: linear-gradient(180deg, var(--p-trim-hi), var(--p-trim)); color: var(--p-trim-grip); cursor: grab; touch-action: none; box-shadow: 0 2px 8px -2px rgb(0 0 0 / 0.45); }
.dx-trim-handle[data-which='from'] { left: var(--vs); margin-left: -16px; border-radius: 10px 3px 3px 10px; }
.dx-trim-handle[data-which='to'] { left: var(--ve); border-radius: 3px 10px 10px 3px; }
.dx-trim-handle::before { content: ''; position: absolute; inset: -8px -10px; }
.dx-trim-handle::after { content: ''; width: 2px; height: 14px; border-left: 1.5px solid currentColor; border-right: 1.5px solid currentColor; box-sizing: content-box; opacity: 0.8; }
.dx-trim-handle:hover { transform: scale(1.06); }
.dx-trim-handle:focus { outline: none; }
.dx-trim-handle:focus-visible { outline: 2px solid var(--p-focus); outline-offset: 2px; }
.dx-trim[data-drag='from'] .dx-trim-handle[data-which='from'], .dx-trim[data-drag='to'] .dx-trim-handle[data-which='to'] { transform: scale(1.16); cursor: grabbing; box-shadow: 0 0 0 5px color-mix(in srgb, var(--p-trim) 26%, transparent), 0 6px 16px -4px rgb(0 0 0 / 0.5); }
.dx-trim[data-drag] { cursor: grabbing; }
.dx-trim[data-drag] .dx-trim-head { transition: none; }
.dx-trim:not([data-drag]) :is(.dx-trim-fill, .dx-trim-dim, .dx-trim-frame) { transition: left 0.34s var(--ease), right 0.34s var(--ease); }
.dx-trim:not([data-drag]) .dx-trim-handle { transition: left 0.34s var(--ease), transform 0.2s var(--spring), box-shadow 0.2s; }
.dx-trim[data-drag] .dx-trim-handle { transition: transform 0.2s var(--spring), box-shadow 0.2s; }
.dx-trim-tip { position: absolute; bottom: calc(100% - var(--rt) + 6px); left: clamp(20px, calc(var(--t) * 100%), calc(100% - 20px)); z-index: 1; padding: 3px 8px; border-radius: 7px; background: var(--p-trim); color: var(--p-trim-grip); font-size: 12px; font-weight: 700; line-height: 1.35; white-space: nowrap; font-variant-numeric: tabular-nums; pointer-events: none; opacity: 0; transform: translate(-50%, 4px) scale(0.9); transition: opacity 0.14s, transform 0.2s var(--spring); box-shadow: 0 6px 16px -6px rgb(0 0 0 / 0.55); }
.dx-trim-tip::after { content: ''; position: absolute; top: 100%; left: 50%; margin-left: -4px; border: 4px solid transparent; border-top-color: var(--p-trim); }
.dx-trim-tip[data-kind='hover'] { background: var(--p-solid); color: var(--p-fg); font-weight: 600; box-shadow: inset 0 0 0 1px var(--p-line), 0 6px 16px -6px rgb(0 0 0 / 0.55); }
.dx-trim-tip[data-kind='hover']::after { border-top-color: var(--p-solid); }
.dx-trim-tip[data-show='true'] { opacity: 1; transform: translate(-50%, 0) scale(1); }
.dx-trim-ends { display: flex; justify-content: space-between; margin-top: -6px; padding: 0 14px; font-size: 10.5px; color: var(--p-muted); font-variant-numeric: tabular-nums; }
.dx-trim-chaps { display: flex; gap: 5px; margin: -2px; padding: 2px; overflow-x: auto; scrollbar-width: none; overscroll-behavior: contain; }
.dx-trim-chaps[data-fade='end'] { mask-image: linear-gradient(90deg, #000 calc(100% - 28px), transparent); }
.dx-trim-chaps[data-fade='start'] { mask-image: linear-gradient(90deg, transparent, #000 28px); }
.dx-trim-chaps[data-fade='both'] { mask-image: linear-gradient(90deg, transparent, #000 28px, #000 calc(100% - 28px), transparent); }
.dx-trim-chap { flex: none; display: inline-flex; align-items: center; gap: 6px; max-width: 180px; min-height: 26px; padding: 0 10px 0 4px; border: 1px solid var(--p-line); border-radius: 999px; background: transparent; color: var(--p-muted); font: inherit; font-size: 11.5px; font-weight: 600; cursor: pointer; transition: background-color 0.14s, border-color 0.14s, color 0.14s; }
.dx-trim-chap > i { display: inline-grid; place-items: center; min-width: 18px; height: 18px; padding: 0 4px; border-radius: 999px; background: var(--p-chip); color: var(--p-fg); font-style: normal; font-size: 10.5px; font-variant-numeric: tabular-nums; }
.dx-trim-chap > span { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.dx-trim-chap:hover:not(:disabled) { color: var(--p-fg); background: var(--p-hover); }
.dx-trim-chap[aria-pressed='true'] { color: var(--p-accent); background: var(--p-current); border-color: var(--p-current-edge); }
.dx-trim-chap[aria-pressed='true'] > i { background: var(--p-accent-fill); color: var(--p-accent-fg); }
.dx-trim-acts { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; }
.dx-trim-btn { display: inline-flex; align-items: center; gap: 6px; min-height: 30px; padding: 0 12px 0 9px; border: 1px solid var(--p-line); border-radius: 999px; background: var(--p-chip); color: var(--p-fg); font: inherit; font-size: 12px; font-weight: 600; cursor: pointer; transition: background-color 0.14s, border-color 0.14s, color 0.14s, transform 0.12s; }
.dx-trim-btn svg { width: 13px; height: 13px; }
.dx-trim-btn:hover:not(:disabled) { background: var(--p-hover); border-color: var(--p-current-edge); }
.dx-trim-btn:active:not(:disabled) { transform: scale(0.96); }
.dx-trim-btn:disabled, .dx-trim-chap:disabled, .dx-trim-whole:disabled { opacity: 0.5; cursor: default; }
.dx-trim-preview { color: var(--p-accent); background: var(--p-current); border-color: var(--p-current-edge); }
.dx-trim-preview[aria-pressed='true'] { color: var(--p-accent-fg); background: var(--p-accent-fill); border-color: transparent; }
.dx-part[data-busy='true'] .dx-trim { cursor: default; opacity: 0.6; pointer-events: none; }
.dx-part-error { grid-column: 1 / -1; display: flex; align-items: center; gap: 6px; margin: 0; padding-left: 7px; color: var(--p-error); font-weight: 600; line-height: 1.35; animation: dx-rise 0.3s var(--ease); }
.dx-part-error::before { content: '!'; display: inline-grid; place-items: center; flex: none; width: 15px; height: 15px; border-radius: 50%; background: var(--p-error); color: var(--p-solid); font-size: 11px; font-weight: 800; line-height: 1; }
.dx-part[data-error='true'] .dx-trim-box { opacity: 0.45; filter: saturate(0.3); }
.dx-trim-box { transition: opacity 0.2s, filter 0.2s; }
.dx-part-toggle:focus-visible, .dx-trim-chip:focus-visible, .dx-trim-edit:focus-visible, .dx-trim-btn:focus-visible, .dx-trim-chap:focus-visible, .dx-trim-whole:focus-visible { outline: 2px solid var(--p-focus); outline-offset: 1px; }
@media (max-height: 420px) {
  .dx-trim { --rt: 8px; --rh: 28px; --tip: 18px; }
  .dx-part-pad { gap: 6px; padding-top: 4px; }
  .dx-trim-chip { font-size: 16px; min-height: 28px; }
  .dx-trim-ends { display: none; }
}
@media (forced-colors: active) {
  .dx-part-ico { box-shadow: none; border: 1px solid ButtonText; }
  .dx-trim-rail { border: 1px solid CanvasText; background: Canvas; }
  .dx-trim-seg + .dx-trim-seg { border-left-color: CanvasText; }
  .dx-trim-fill { forced-color-adjust: none; background: Highlight; opacity: 0.4; }
  .dx-trim-dim { display: none; }
  .dx-trim-frame { border-color: Highlight; }
  .dx-trim-handle { forced-color-adjust: none; background: Highlight; color: HighlightText; box-shadow: none; }
  .dx-trim-head, .dx-trim-head::before { forced-color-adjust: none; background: CanvasText; box-shadow: none; }
  .dx-trim-tip { forced-color-adjust: none; background: Canvas; color: CanvasText; border: 1px solid CanvasText; }
  .dx-trim-tip::after { display: none; }
  .dx-trim-chip, .dx-trim-edit, .dx-trim-btn, .dx-trim-chap, .dx-trim-whole { border: 1px solid ButtonText; background: ButtonFace; color: ButtonText; }
  .dx-trim-chap[aria-pressed='true'], .dx-trim-preview[aria-pressed='true'] { border: 2px solid Highlight; }
  .dx-part-error::before { forced-color-adjust: none; background: CanvasText; color: Canvas; }
  .dx-part-toggle:focus-visible, .dx-trim-chip:focus-visible, .dx-trim-edit:focus-visible, .dx-trim-btn:focus-visible, .dx-trim-chap:focus-visible, .dx-trim-whole:focus-visible, .dx-trim-handle:focus-visible { outline: 2px solid Highlight; }
}
`;

const SVG_NS = 'http://www.w3.org/2000/svg';

/** The row's line glyphs (24-unit grid, stroked with the text colour), drawn for Downex. */
const PART_GLYPHS = {
  scissors: ['M4.5 6.5a2.5 2.5 0 1 0 5 0a2.5 2.5 0 1 0-5 0', 'M4.5 17.5a2.5 2.5 0 1 0 5 0a2.5 2.5 0 1 0-5 0', 'M8.6 8.4 19.5 18M8.6 15.6 19.5 6'],
  chevron: ['m6.5 9.5 5.5 5.5 5.5-5.5'],
  to: ['M5 12h14', 'm13.5 6.5 5.5 5.5-5.5 5.5'],
  play: ['M8 5.8v12.4a.6.6 0 0 0 .9.5l9.6-6.2a.6.6 0 0 0 0-1L8.9 5.3a.6.6 0 0 0-.9.5z'],
  stop: ['M7.5 6.5h9a1 1 0 0 1 1 1v9a1 1 0 0 1-1 1h-9a1 1 0 0 1-1-1v-9a1 1 0 0 1 1-1z'],
  startHere: ['M10 5H6.5v14H10', 'M15.5 10.5V19', 'M14 8a1.5 1.5 0 1 0 3 0a1.5 1.5 0 1 0-3 0'],
  endHere: ['M14 5h3.5v14H14', 'M8.5 10.5V19', 'M7 8a1.5 1.5 0 1 0 3 0a1.5 1.5 0 1 0-3 0'],
  whole: ['M4 12h16', 'M7.5 8.5 4 12l3.5 3.5', 'M16.5 8.5 20 12l-3.5 3.5'],
} as const satisfies Record<string, readonly string[]>;

function partGlyph(doc: Document, name: keyof typeof PART_GLYPHS): SVGSVGElement {
  const svg = doc.createElementNS(SVG_NS, 'svg');
  svg.setAttribute('viewBox', '0 0 24 24');
  svg.setAttribute('aria-hidden', 'true');
  svg.setAttribute('focusable', 'false');
  for (const d of PART_GLYPHS[name]) {
    const path = doc.createElementNS(SVG_NS, 'path');
    path.setAttribute('d', d);
    svg.append(path);
  }
  return svg;
}

/** What the user does in the Part row. */
export interface PartHandlers {
  toggle: () => void;
  /** A handle moved (drag, keys) to `ms`; `push`: Start here or End here (the other gives way). */
  set: (which: PartField, ms: number, push: boolean) => void;
  /** A time typed into a chip was committed (Enter, or the field left). */
  edit: (which: PartField, text: string) => void;
  /** A chapter (1 first) was chosen. */
  chapter: (n: number) => void;
  /** "Whole video". */
  whole: () => void;
}

export interface PartTrimmerDeps {
  doc: Document;
  /** Makes the row's ids unique in the panel. */
  id: string;
  handlers: PartHandlers;
  /** Whether an activation came from the user (`event.isTrusted`; tests substitute it). */
  isTrusted?: (e: Event) => boolean;
  /** The page's own <video> the part is of (undefined: no playhead, preview, Start/End here). */
  video?: () => PartVideo | undefined;
  timers?: Timers;
  /** The row's height settled after it opened or closed (the panel's overflow cue follows). */
  onLayout?: () => void;
}

/** How long the body takes to open (the panel is then scrolled to show it). */
const OPEN_MS = 440;
/** The element's events the playhead follows … */
const TIME_EVENTS = ['timeupdate', 'seeked'] as const;
/** … and those after which it may play other media (an ad, the next video) or its length is known. */
const MEDIA_EVENTS = ['emptied', 'loadedmetadata', 'durationchange'] as const;
/** A keyboard nudge's bubble stays this long after the key. */
const KEY_TIP_MS = 900;

interface ChipDom {
  chip: HTMLButtonElement;
  input: HTMLInputElement;
}

/**
 * The Part row's elements and what the user does with them. `render` draws a PartView (undefined:
 * the row is hidden); the handlers get each change, and the panel draws the row again.
 */
export class PartTrimmer {
  readonly section: HTMLElement;
  readonly #doc: Document;
  readonly #deps: PartTrimmerDeps;
  readonly #isTrusted: (e: Event) => boolean;
  readonly #timers: Timers;
  readonly #player: PartPlayer;
  readonly #toggle: HTMLButtonElement;
  readonly #summary: HTMLElement;
  readonly #body: HTMLElement;
  readonly #chips: Record<PartField, ChipDom>;
  readonly #whole: HTMLButtonElement;
  readonly #detail: HTMLElement;
  readonly #trim: HTMLElement;
  readonly #box: HTMLElement;
  readonly #rail: HTMLElement;
  readonly #segs: HTMLElement;
  readonly #head: HTMLElement;
  readonly #handles: Record<PartField, HTMLElement>;
  readonly #tip: HTMLElement;
  readonly #ends: [HTMLElement, HTMLElement];
  readonly #chaps: HTMLElement;
  readonly #preview: HTMLButtonElement;
  readonly #previewText: HTMLElement;
  readonly #here: Record<PartField, HTMLButtonElement>;
  readonly #error: HTMLElement;
  #view: PartView | undefined;
  #busy = false;
  #editing: PartField | undefined;
  #drag: { which: PartField; pointerId: number; offset: number } | undefined;
  #keyTip: unknown;
  #openTimer: unknown;
  #watched: PartVideo | undefined;
  readonly #onTime = () => this.#drawHead();
  /** Other media, or its length known: whether the element plays the video may have changed. */
  readonly #onMedia = () => this.#drawPlayer();

  constructor(deps: PartTrimmerDeps) {
    const doc = deps.doc;
    this.#doc = doc;
    this.#deps = deps;
    this.#isTrusted = deps.isTrusted ?? ((e) => e.isTrusted);
    this.#timers = deps.timers ?? systemTimers;
    this.#player = new PartPlayer(() => deps.video?.(), this.#timers, () => this.#drawPlayer());
    const id = deps.id;
    const el = <K extends keyof HTMLElementTagNameMap>(tag: K, className: string): HTMLElementTagNameMap[K] => {
      const e = doc.createElement(tag);
      e.className = className;
      return e;
    };
    const button = (className: string): HTMLButtonElement => {
      const b = el('button', className);
      b.type = 'button';
      return b;
    };

    this.section = el('div', 'dx-part');
    this.section.hidden = true;
    this.section.setAttribute('role', 'group');
    this.section.setAttribute('aria-label', CLIP_TEXTS.row);
    this.#toggle = button('dx-part-toggle');
    this.#toggle.setAttribute('aria-expanded', 'false');
    this.#toggle.setAttribute('aria-controls', `${id}-part`);
    const ico = el('span', 'dx-part-ico');
    ico.append(partGlyph(doc, 'scissors'));
    const label = el('span', 'dx-part-label');
    label.textContent = CLIP_TEXTS.row;
    this.#summary = el('span', 'dx-part-sum');
    const chev = el('span', 'dx-part-chev');
    chev.append(partGlyph(doc, 'chevron'));
    this.#toggle.append(ico, label, this.#summary, chev);

    this.#body = el('div', 'dx-part-body');
    this.#body.id = `${id}-part`;
    // The trimmer reads left to right whatever the page's direction: the track, the chips under it
    // and Start here before End here, as the handles (a chapter's title keeps its own, dir=auto).
    this.#body.dir = 'ltr';
    this.#body.dataset.open = 'false';
    this.#body.inert = true;
    const inner = el('div', 'dx-part-inner');
    const pad = el('div', 'dx-part-pad');
    inner.append(pad);
    this.#body.append(inner);

    // The readout: From → To as big chips (click: type a time), "Whole video", and the line under
    // them ("1:25 selected · ~78 MB at 1080p").
    const read = el('div', 'dx-trim-read');
    const times = el('div', 'dx-trim-times');
    const chip = (which: PartField): ChipDom => {
      const c = button('dx-trim-chip');
      c.dataset.which = which;
      c.title = CLIP_TEXTS.editTime;
      const input = el('input', 'dx-trim-edit');
      input.type = 'text';
      input.id = `${id}-part-${which}`;
      input.hidden = true;
      input.inputMode = 'decimal';
      input.autocomplete = 'off';
      input.spellcheck = false;
      input.setAttribute('aria-label', which === 'from' ? CLIP_TEXTS.from : CLIP_TEXTS.to);
      input.setAttribute('aria-description', CLIP_TEXTS.timeHint);
      return { chip: c, input };
    };
    this.#chips = { from: chip('from'), to: chip('to') };
    times.append(this.#chips.from.chip, this.#chips.from.input, partGlyph(doc, 'to'), this.#chips.to.chip, this.#chips.to.input);
    this.#whole = button('dx-trim-whole');
    this.#whole.append(partGlyph(doc, 'whole'), CLIP_TEXTS.whole);
    this.#detail = el('p', 'dx-trim-detail');
    // A time that cannot be used says why right under the times, in place of the length.
    this.#error = el('p', 'dx-part-error');
    this.#error.id = `${id}-part-error`;
    this.#error.setAttribute('role', 'alert');
    this.#error.hidden = true;
    read.append(times, this.#whole, this.#detail, this.#error);

    // The track: the rail (chapters, the part's fill, the dimmed rest), the frame, the playhead, the
    // two handles and the bubble; its ends under it.
    this.#trim = el('div', 'dx-trim');
    this.#trim.dir = 'ltr';
    this.#box = el('div', 'dx-trim-box');
    this.#rail = el('div', 'dx-trim-rail');
    this.#segs = el('div', 'dx-trim-segs');
    const fill = el('div', 'dx-trim-fill');
    const before = el('div', 'dx-trim-dim');
    before.dataset.side = 'before';
    const after = el('div', 'dx-trim-dim');
    after.dataset.side = 'after';
    this.#rail.append(this.#segs, fill, before, after);
    const frame = el('div', 'dx-trim-frame');
    this.#head = el('div', 'dx-trim-head');
    this.#head.hidden = true;
    const handle = (which: PartField): HTMLElement => {
      const h = el('div', 'dx-trim-handle');
      h.dataset.which = which;
      h.tabIndex = 0;
      h.setAttribute('role', 'slider');
      h.setAttribute('aria-label', which === 'from' ? CLIP_TEXTS.start : CLIP_TEXTS.end);
      h.setAttribute('aria-orientation', 'horizontal');
      return h;
    };
    this.#handles = { from: handle('from'), to: handle('to') };
    this.#tip = el('div', 'dx-trim-tip');
    this.#tip.setAttribute('aria-hidden', 'true');
    this.#tip.dataset.show = 'false';
    this.#box.append(this.#rail, frame, this.#head, this.#handles.from, this.#handles.to, this.#tip);
    this.#trim.append(this.#box);
    this.#trim.setAttribute('role', 'group');
    this.#trim.setAttribute('aria-label', CLIP_TEXTS.timeline);
    const ends = el('div', 'dx-trim-ends');
    ends.setAttribute('aria-hidden', 'true');
    this.#ends = [el('span', ''), el('span', '')];
    ends.append(...this.#ends);

    this.#chaps = el('div', 'dx-trim-chaps');
    this.#chaps.setAttribute('role', 'group');
    this.#chaps.setAttribute('aria-label', CLIP_TEXTS.chapters);
    this.#chaps.hidden = true;

    const acts = el('div', 'dx-trim-acts');
    this.#preview = button('dx-trim-btn dx-trim-preview');
    this.#preview.setAttribute('aria-pressed', 'false');
    this.#preview.title = CLIP_TEXTS.previewName;
    this.#previewText = el('span', '');
    this.#preview.append(partGlyph(doc, 'play'), this.#previewText);
    const here = (which: PartField): HTMLButtonElement => {
      const b = button('dx-trim-btn');
      b.dataset.here = which;
      b.append(partGlyph(doc, which === 'from' ? 'startHere' : 'endHere'), which === 'from' ? CLIP_TEXTS.startHere : CLIP_TEXTS.endHere);
      b.title = which === 'from' ? CLIP_TEXTS.startHereName : CLIP_TEXTS.endHereName;
      return b;
    };
    this.#here = { from: here('from'), to: here('to') };
    acts.append(this.#preview, this.#here.from, this.#here.to);

    pad.append(read, this.#trim, ends, this.#chaps, acts);
    this.section.append(this.#toggle, this.#body);
    this.#drawPlayer();
    this.#listen();
  }

  /**
   * Draws `v` (undefined: the row is hidden). `busy` (a pick being sent, or sent) disables every
   * control, the disclosure too, so that the row keeps showing the part that went.
   */
  render(v: PartView | undefined, busy: boolean): void {
    const wasOpen = this.#view?.expanded === true && !this.section.hidden;
    this.#view = v;
    this.#busy = busy;
    if (v) this.#player.expect(v.durationMs);
    this.section.hidden = v === undefined;
    if (!v || !v.expanded || busy) {
      this.#endDrag();
      this.#cancelEdit();
    }
    if (!v || !v.expanded) {
      this.#player.restore();
      this.#hideTip();
    }
    if (!v) {
      this.#watch(undefined);
      this.#body.dataset.open = 'false';
      this.#body.inert = true;
      return;
    }
    this.section.dataset.busy = String(busy);
    this.section.dataset.error = String(v.error !== undefined);
    this.#toggle.setAttribute('aria-expanded', String(v.expanded));
    this.#toggle.disabled = busy;
    this.#toggle.dataset.part = String(v.summary !== CLIP_TEXTS.whole);
    setText(this.#summary, v.summary);
    this.#body.dataset.open = String(v.expanded);
    this.#body.inert = !v.expanded;
    if (v.expanded !== wasOpen) this.#settle(v.expanded);

    const dur = v.durationMs;
    const frac = (ms: number) => (dur > 0 ? Math.min(1, Math.max(0, ms / dur)) : 0);
    this.#trim.style.setProperty('--s', String(frac(v.startMs)));
    this.#trim.style.setProperty('--e', String(frac(v.endMs)));
    for (const which of ['from', 'to'] as const) {
      const c = this.#chips[which];
      const text = which === 'from' ? v.from : v.to;
      const invalid = v.error !== undefined && v.errorField === which;
      setText(c.chip, text);
      c.chip.setAttribute('aria-label', `${which === 'from' ? CLIP_TEXTS.from : CLIP_TEXTS.to} ${text}`);
      c.chip.dataset.invalid = String(invalid);
      c.chip.disabled = busy;
      c.input.setAttribute('aria-invalid', String(invalid));
      for (const e of [c.chip, c.input]) {
        if (v.error !== undefined) e.setAttribute('aria-describedby', this.#error.id);
        else e.removeAttribute('aria-describedby');
      }
      // The sliders: seconds, each bounded by the other handle.
      const h = this.#handles[which];
      const min = Math.min(MIN_CLIP_MS, dur);
      const [lo, hi, at] = which === 'from' ? [0, Math.max(0, v.endMs - min), v.startMs] : [Math.min(dur, v.startMs + min), dur, v.endMs];
      setAttr(h, 'aria-valuemin', seconds(lo));
      setAttr(h, 'aria-valuemax', seconds(hi));
      setAttr(h, 'aria-valuenow', seconds(at));
      setAttr(h, 'aria-valuetext', `${which === 'from' ? CLIP_TEXTS.start : CLIP_TEXTS.end} ${formatClipTime(at)}`);
      setAttr(h, 'aria-disabled', String(busy));
      h.tabIndex = busy ? -1 : 0;
    }
    this.#whole.hidden = v.whole && v.error === undefined;
    this.#whole.disabled = busy;
    this.#drawDetail(v);
    setText(this.#ends[0], formatClipTime(0));
    setText(this.#ends[1], formatClipTime(dur));
    this.#drawChapters(v);
    const newError = v.error !== undefined && this.#error.hidden;
    setText(this.#error, v.error ?? '');
    this.#error.hidden = v.error === undefined;
    this.#detail.hidden = v.error !== undefined;
    if (newError && v.expanded) this.#reveal(this.#error);
    for (const b of [this.#here.from, this.#here.to]) b.disabled = busy;
    // A part that cannot be sent is not previewed either (Start here and End here fix it).
    this.#preview.disabled = busy || v.error !== undefined;
    this.#drawPlayer();
    if (this.#drag) this.#showTip(this.#drag.which, 'drag');
  }

  /** Moves the focus to a time chip, its field open (an error that blocked a download is about it). */
  focusField(which: PartField): void {
    const v = this.#view;
    if (!v?.expanded || this.#busy || this.section.hidden) return;
    this.#openEdit(which);
  }

  /**
   * The panel closed: drags and edits end, the player is put back. `putBack` false (another video):
   * the player is let go where it is, since the element is about to play the other video.
   */
  stop(putBack = true): void {
    this.#endDrag();
    this.#cancelEdit();
    this.#hideTip();
    if (putBack) this.#player.restore();
    else this.#player.forget();
    this.#watch(undefined);
    if (this.#openTimer !== undefined) this.#timers.clearTimeout(this.#openTimer);
    this.#openTimer = undefined;
  }

  // --- drawing -----------------------------------------------------------------------------------

  #drawDetail(v: PartView): void {
    const parts: (string | Node)[] = [];
    if (v.whole || v.length === '') parts.push(CLIP_TEXTS.whole);
    else {
      const b = this.#doc.createElement('b');
      b.textContent = v.length;
      parts.push(b, ` ${CLIP_TEXTS.selected}`);
    }
    if (v.size) parts.push(` · ${v.size}`);
    const text = parts.map((p) => (typeof p === 'string' ? p : p.textContent)).join('');
    if (this.#detail.dataset.text === text) return;
    this.#detail.dataset.text = text;
    this.#detail.replaceChildren(...parts);
  }

  #drawChapters(v: PartView): void {
    const dur = v.durationMs;
    const signature = v.chapters.map((c) => `${c.startMs}\n${c.endMs}\n${c.label}`).join('\n\n') + `\n${dur}`;
    if (this.#chaps.dataset.signature !== signature) {
      this.#chaps.dataset.signature = signature;
      // On the track: one segment per chapter (the gaps are their edges, thinner the more there are) …
      const n = v.chapters.length;
      this.#rail.dataset.dense = n > 24 ? '2' : n > 12 ? '1' : '0';
      this.#segs.replaceChildren(
        ...v.chapters.map((c, i) => {
          const seg = this.#doc.createElement('div');
          seg.className = 'dx-trim-seg';
          seg.dataset.n = String(i + 1);
          const a = dur > 0 ? c.startMs / dur : 0;
          const b = dur > 0 ? Math.min(1, c.endMs / dur) : 0;
          seg.style.left = `${(a * 100).toFixed(4)}%`;
          seg.style.width = `${(Math.max(0, b - a) * 100).toFixed(4)}%`;
          return seg;
        }),
      );
      // … and a chip each under it.
      this.#chaps.replaceChildren(
        ...v.chapters.map((c, i) => {
          const b = this.#doc.createElement('button');
          b.type = 'button';
          b.className = 'dx-trim-chap';
          b.dataset.n = String(i + 1);
          b.title = cleanSiteText(c.label);
          b.setAttribute('aria-label', cleanSiteText(c.label));
          const n = this.#doc.createElement('i');
          n.textContent = String(i + 1);
          const t = this.#doc.createElement('span');
          t.dir = 'auto';
          t.textContent = c.title;
          b.append(n, t);
          return b;
        }),
      );
    }
    this.#chaps.hidden = v.chapters.length === 0;
    let chosen: HTMLButtonElement | undefined;
    for (const b of this.#chaps.querySelectorAll<HTMLButtonElement>('button')) {
      const on = Number(b.dataset.n) === v.chapter;
      if (on && b.getAttribute('aria-pressed') !== 'true') chosen = b;
      setAttr(b, 'aria-pressed', String(on));
      b.disabled = this.#busy;
    }
    // A chapter chosen (on the track too) scrolls its chip into the strip's view.
    if (chosen && !this.#chaps.hidden) {
      const left = chosen.offsetLeft - this.#chaps.offsetLeft;
      const right = left + chosen.offsetWidth;
      if (left < this.#chaps.scrollLeft) this.#chaps.scrollLeft = Math.max(0, left - 16);
      else if (right > this.#chaps.scrollLeft + this.#chaps.clientWidth) this.#chaps.scrollLeft = right - this.#chaps.clientWidth + 16;
    }
    this.#chapsFade();
  }

  /** Fades the chip strip's edges where more chips are out of view. */
  #chapsFade(): void {
    const c = this.#chaps;
    const more = c.scrollWidth - c.clientWidth > 1;
    const fade = !more ? 'none' : c.scrollLeft <= 1 ? 'end' : c.scrollLeft >= c.scrollWidth - c.clientWidth - 1 ? 'start' : 'both';
    if (c.dataset.fade !== fade) c.dataset.fade = fade;
  }

  /** Preview, Start here, End here and the playhead: only with the page's video. */
  #drawPlayer(): void {
    const v = this.#view;
    // The element is followed while the row is open, whatever it plays; its controls only while it
    // plays the video (not an ad, not before its media is known).
    const element = v?.expanded ? this.#player.element() : undefined;
    const video = element ? this.#player.video() : undefined;
    const has = video !== undefined;
    for (const b of [this.#preview, this.#here.from, this.#here.to]) b.hidden = !has;
    const previewing = this.#player.previewing;
    setAttr(this.#preview, 'aria-pressed', String(previewing));
    setText(this.#previewText, previewing ? CLIP_TEXTS.stopPreview : CLIP_TEXTS.preview);
    const icon = this.#preview.querySelector('svg');
    const want = previewing ? 'stop' : 'play';
    if (icon && this.#preview.dataset.glyph !== want) {
      this.#preview.dataset.glyph = want;
      icon.replaceWith(partGlyph(this.#doc, want));
    }
    this.#watch(element && !this.section.hidden ? element : undefined);
    this.#drawHead();
  }

  #drawHead(): void {
    const v = this.#view;
    const ms = this.#watched ? this.#player.timeMs() : undefined;
    this.#head.hidden = ms === undefined || !v || !(v.durationMs > 0);
    if (ms !== undefined && v && v.durationMs > 0) this.#trim.style.setProperty('--p', String(Math.min(1, Math.max(0, ms / v.durationMs))));
  }

  /** Follows the element while the row is open: its position (the playhead) and its media. */
  #watch(video: PartVideo | undefined): void {
    if (video === this.#watched) return;
    const old = this.#watched;
    for (const t of TIME_EVENTS) old?.removeEventListener(t, this.#onTime);
    for (const t of MEDIA_EVENTS) old?.removeEventListener(t, this.#onMedia);
    this.#watched = video;
    for (const t of TIME_EVENTS) video?.addEventListener(t, this.#onTime);
    for (const t of MEDIA_EVENTS) video?.addEventListener(t, this.#onMedia);
  }

  /**
   * The body opened or closed: once it has its height the panel's overflow cue follows, and an
   * opened body is scrolled into the panel's view whole.
   */
  #settle(opened: boolean): void {
    if (this.#openTimer !== undefined) this.#timers.clearTimeout(this.#openTimer);
    this.#openTimer = this.#timers.setTimeout(() => {
      this.#openTimer = undefined;
      this.#deps.onLayout?.();
      // The whole row, else (taller than the panel) its top; an error line shown meanwhile wins.
      if (opened) this.#reveal(this.#error.hidden ? this.section : this.#error);
    }, OPEN_MS);
  }

  /** Scrolls the panel (never the page) so that `el` shows, its top first. */
  #reveal(el: HTMLElement): void {
    const panel = this.section.closest<HTMLElement>('.dx-panel');
    if (!panel || panel.scrollHeight <= panel.clientHeight) return;
    const p = panel.getBoundingClientRect();
    const r = el.getBoundingClientRect();
    // The bottom fade covers the last 30 px of a panel with more below it.
    const by = Math.min(r.bottom - p.bottom + 34, r.top - p.top - 8);
    if (by <= 0) return;
    const reduce = this.#doc.defaultView?.matchMedia?.('(prefers-reduced-motion: reduce)').matches === true;
    if (typeof panel.scrollTo === 'function') panel.scrollTo({ top: panel.scrollTop + by, behavior: reduce ? 'auto' : 'smooth' });
    else panel.scrollTop += by;
  }

  #showTip(which: PartField, kind: 'drag' | 'key'): void {
    const v = this.#view;
    if (!v || !(v.durationMs > 0)) return;
    const ms = which === 'from' ? v.startMs : v.endMs;
    this.#tipAt(ms / v.durationMs, formatClipTime(ms), kind);
  }

  #tipAt(frac: number, text: string, kind: 'drag' | 'key' | 'hover'): void {
    setText(this.#tip, text);
    this.#tip.dataset.kind = kind;
    this.#trim.style.setProperty('--t', String(Math.min(1, Math.max(0, frac))));
    this.#tip.dataset.show = 'true';
  }

  #hideTip(): void {
    if (this.#keyTip !== undefined) this.#timers.clearTimeout(this.#keyTip);
    this.#keyTip = undefined;
    this.#tip.dataset.show = 'false';
  }

  // --- what the user does --------------------------------------------------------------------------

  #listen(): void {
    const h = this.#deps.handlers;
    const trusted = (fn: (e: Event) => void) => (e: Event) => {
      if (this.#isTrusted(e)) fn(e);
    };
    this.#toggle.addEventListener(
      'click',
      trusted(() => {
        if (!this.#busy) h.toggle();
      }),
    );
    this.#whole.addEventListener(
      'click',
      trusted(() => {
        if (!this.#busy) h.whole();
      }),
    );
    for (const which of ['from', 'to'] as const) {
      const c = this.#chips[which];
      c.chip.addEventListener(
        'click',
        trusted(() => this.#openEdit(which)),
      );
      c.input.addEventListener('keydown', (e) => {
        if (e.key === 'Enter') {
          e.preventDefault();
          this.#commitEdit(true);
        } else if (e.key === 'Escape') {
          // Esc leaves the field, not the panel.
          e.preventDefault();
          e.stopPropagation();
          this.#cancelEdit(true);
        }
      });
      c.input.addEventListener('blur', () => this.#commitEdit(false));
      this.#here[which].addEventListener(
        'click',
        trusted(() => {
          const ms = this.#player.timeMs();
          if (!this.#busy && ms !== undefined) h.set(which, ms, true);
        }),
      );
      const handle = this.#handles[which];
      handle.addEventListener('keydown', (e) => this.#onKey(which, e));
      handle.addEventListener('keyup', () => {
        if (this.#drag) return;
        this.#player.release();
        if (this.#keyTip !== undefined) this.#timers.clearTimeout(this.#keyTip);
        this.#keyTip = this.#timers.setTimeout(() => this.#hideTip(), KEY_TIP_MS);
      });
      handle.addEventListener('blur', () => {
        if (!this.#drag) this.#hideTip();
      });
    }
    this.#preview.addEventListener(
      'click',
      trusted(() => {
        const v = this.#view;
        if (!v || this.#busy) return;
        if (this.#player.previewing) this.#player.stopPreview();
        else this.#player.preview(v.startMs, v.endMs);
      }),
    );
    this.#chaps.addEventListener('scroll', () => this.#chapsFade(), { passive: true });
    this.#chaps.addEventListener(
      'click',
      trusted((e) => {
        const b = (e.target as Element | null)?.closest?.('button.dx-trim-chap') as HTMLButtonElement | null;
        if (b && !b.disabled && !this.#busy) h.chapter(Number(b.dataset.n));
      }),
    );
    // The track: a press grabs the handle under it, else the nearer one, which jumps there; it
    // follows the pointer (captured: mouse, touch or pen) until let go. A double-click on a chapter
    // takes the chapter.
    this.#trim.addEventListener('pointerdown', (e) => this.#onDown(e));
    this.#trim.addEventListener('pointermove', (e) => this.#onMove(e));
    for (const t of ['pointerup', 'pointercancel', 'lostpointercapture'] as const) {
      this.#trim.addEventListener(t, (e) => this.#onUp(e));
    }
    this.#trim.addEventListener('pointerleave', () => {
      if (!this.#drag && this.#tip.dataset.kind === 'hover') this.#hideTip();
      this.#hoverSeg(0);
    });
    this.#trim.addEventListener(
      'dblclick',
      trusted((e) => {
        const v = this.#view;
        if (!v || this.#busy || v.chapters.length === 0) return;
        const n = this.#chapterAt(this.#msAt((e as MouseEvent).clientX));
        if (n > 0) h.chapter(n);
      }),
    );
  }

  #onKey(which: PartField, e: KeyboardEvent): void {
    const v = this.#view;
    // Alt, Ctrl and Meta with an arrow are the browser's (Back, Forward) or the system's.
    if (!v || this.#busy || e.altKey || e.ctrlKey || e.metaKey) return;
    const at = which === 'from' ? v.startMs : v.endMs;
    const step = e.shiftKey ? 10_000 : 1000;
    let next: number;
    switch (e.key) {
      case 'ArrowRight':
      case 'ArrowUp':
        next = at + step;
        break;
      case 'ArrowLeft':
      case 'ArrowDown':
        next = at - step;
        break;
      case 'PageUp':
        next = at + 60_000;
        break;
      case 'PageDown':
        next = at - 60_000;
        break;
      case 'Home':
        next = 0;
        break;
      case 'End':
        next = v.durationMs;
        break;
      default:
        return;
    }
    e.preventDefault();
    this.#move(which, next, 'key');
  }

  /** A handle moves to `ms`: the state takes it (clamped), the player shows its frame. */
  #move(which: PartField, ms: number, kind: 'drag' | 'key'): void {
    this.#deps.handlers.set(which, ms, false);
    const v = this.#view;
    if (!v) return;
    this.#player.scrub(which === 'from' ? v.startMs : v.endMs);
    if (kind === 'key' && this.#keyTip !== undefined) {
      this.#timers.clearTimeout(this.#keyTip);
      this.#keyTip = undefined;
    }
    this.#showTip(which, kind);
  }

  /** The time under the pointer, from the track's box. */
  #msAt(clientX: number): number {
    const v = this.#view;
    if (!v) return 0;
    const r = this.#box.getBoundingClientRect();
    if (!(r.width > 0)) return 0;
    return Math.min(1, Math.max(0, (clientX - r.left) / r.width)) * v.durationMs;
  }

  /** A dragged time on the video's grid: whole seconds (tenths under 20 s), the very end kept. */
  #snap(ms: number): number {
    const dur = this.#view?.durationMs ?? 0;
    const step = dur < 20_000 ? 100 : 1000;
    if (ms >= dur - step / 2) return dur;
    return Math.max(0, Math.round(ms / step) * step);
  }

  #chapterAt(ms: number): number {
    const chapters = this.#view?.chapters ?? [];
    const i = chapters.findIndex((c, k) => ms >= c.startMs && (ms < c.endMs || k === chapters.length - 1));
    return i + 1;
  }

  #onDown(e: PointerEvent): void {
    const v = this.#view;
    if (!v || this.#busy || !v.expanded || this.#drag || !this.#isTrusted(e)) return;
    if (e.pointerType === 'mouse' && e.button !== 0) return;
    const ms = this.#msAt(e.clientX);
    const grabbed = (e.target as Element | null)?.closest?.('.dx-trim-handle') as HTMLElement | null;
    let which: PartField;
    let offset = 0;
    if (grabbed?.dataset.which === 'from' || grabbed?.dataset.which === 'to') {
      which = grabbed.dataset.which;
      // A grabbed handle keeps its place under the pointer: it does not jump.
      offset = (which === 'from' ? v.startMs : v.endMs) - ms;
    } else if (ms <= v.startMs) which = 'from';
    else if (ms >= v.endMs) which = 'to';
    else which = ms - v.startMs <= v.endMs - ms ? 'from' : 'to';
    e.preventDefault();
    try {
      this.#trim.setPointerCapture(e.pointerId);
    } catch {
      // the pointer is gone
    }
    this.#drag = { which, pointerId: e.pointerId, offset };
    this.#trim.dataset.drag = which;
    this.#hoverSeg(0);
    this.#handles[which].focus({ preventScroll: true });
    this.#move(which, this.#snap(ms + offset), 'drag');
  }

  #onMove(e: PointerEvent): void {
    const d = this.#drag;
    if (d && e.pointerId === d.pointerId) {
      e.preventDefault();
      this.#move(d.which, this.#snap(this.#msAt(e.clientX) + d.offset), 'drag');
      return;
    }
    // A mouse over the track: the time (and the chapter) under it.
    const v = this.#view;
    if (d || !v || this.#busy || e.pointerType !== 'mouse' || !(v.durationMs > 0)) return;
    // On the handles' grid: the time a press there would give.
    const ms = this.#snap(this.#msAt(e.clientX));
    const n = this.#chapterAt(ms);
    const c = v.chapters[n - 1];
    this.#hoverSeg(n);
    this.#tipAt(ms / v.durationMs, c ? `${c.title} · ${formatClipTime(ms)}` : formatClipTime(ms), 'hover');
  }

  #onUp(e: PointerEvent): void {
    if (!this.#drag || e.pointerId !== this.#drag.pointerId) return;
    this.#endDrag();
    this.#hideTip();
  }

  /** Ends a drag: the handle is let go, the player shows its frame a moment, then goes back. */
  #endDrag(): void {
    const d = this.#drag;
    if (!d) return;
    this.#drag = undefined;
    delete this.#trim.dataset.drag;
    try {
      if (this.#trim.hasPointerCapture?.(d.pointerId)) this.#trim.releasePointerCapture(d.pointerId);
    } catch {
      // already released
    }
    this.#player.release();
  }

  #hoverSeg(n: number): void {
    for (const seg of this.#segs.children) {
      const on = String(Number((seg as HTMLElement).dataset.n) === n);
      if ((seg as HTMLElement).dataset.hover !== on) (seg as HTMLElement).dataset.hover = on;
    }
  }

  // --- typing a time ------------------------------------------------------------------------------

  #openEdit(which: PartField): void {
    if (this.#busy || !this.#view?.expanded) return;
    if (this.#editing && this.#editing !== which) this.#commitEdit(false);
    const c = this.#chips[which];
    if (this.#editing !== which) {
      this.#editing = which;
      c.input.value = which === 'from' ? this.#view.from : this.#view.to;
      c.chip.hidden = true;
      c.input.hidden = false;
    }
    c.input.focus();
    c.input.select();
  }

  /** Enter or the field left: the time goes to the state; Enter puts the focus back on its chip. */
  #commitEdit(refocus: boolean): void {
    const which = this.#editing;
    if (!which) return;
    const c = this.#chips[which];
    this.#editing = undefined;
    const text = c.input.value;
    c.input.hidden = true;
    c.chip.hidden = false;
    this.#deps.handlers.edit(which, text);
    if (refocus && !c.chip.disabled) c.chip.focus();
  }

  #cancelEdit(refocus = false): void {
    const which = this.#editing;
    if (!which) return;
    const c = this.#chips[which];
    this.#editing = undefined;
    c.input.hidden = true;
    c.chip.hidden = false;
    if (refocus && !c.chip.disabled) c.chip.focus();
  }
}

/** Sets an element's text only when it changes (a live region announces every change). */
function setText(el: HTMLElement, text: string): void {
  if (el.textContent !== text) el.textContent = text;
}

function setAttr(el: Element, name: string, value: string): void {
  if (el.getAttribute(name) !== value) el.setAttribute(name, value);
}

/** Seconds for a slider's aria values ("80", "80.5"). */
function seconds(ms: number): string {
  return String(Math.round(ms / 100) / 10);
}
