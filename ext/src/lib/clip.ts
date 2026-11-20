// Parts of a video (clips, docs/MEDIA-SPEC.md §8): the time rules and texts of the in-page
// panel's Part row (VIDEO-DETECT-SPEC §5.2). They repeat internal/media's ParseClipTime and
// FormatClipTime and the desktop's texts (ui/core/media clip.go); internal/media's
// TestClipTimesMatchExtension holds clip.test.ts's CLIP_TIMES table to the Go parser.

/** The longest part: 168 hours, in ms (C1). */
export const MAX_CLIP_MS = 168 * 3600 * 1000;
/** The shortest part: 1 s (C1). */
export const MIN_CLIP_MS = 1000;

/**
 * The Part row's texts: the desktop's rules (SITE-MEDIA-SPEC §5.3) in the timeline trimmer's own
 * words, which has a start and an end where the desktop has From and To fields (VIDEO-DETECT-SPEC
 * §5.2).
 */
export const CLIP_TEXTS = {
  row: 'Part of the video',
  whole: 'Whole video',
  from: 'From',
  to: 'To',
  timeHint: 'Time, like 1:20',
  badTime: 'Enter a time like 1:20 or 1:02:03.',
  order: 'The start must be before the end.',
  short: 'The part must be at least 1 second long.',
  outdated: 'Update Downex to save part of a video.',
  timeline: 'Timeline',
  start: 'Start',
  end: 'End',
  startHere: 'Start here',
  endHere: 'End here',
  startHereName: 'Start the part at the player’s current time',
  endHereName: 'End the part at the player’s current time',
  preview: 'Preview',
  stopPreview: 'Stop',
  previewName: 'Play the part in the player',
  chapters: 'Chapters',
  editTime: 'Click to type a time',
  selected: 'selected',
} as const;

/** An end past the video's: "The video ends at 12:34." */
export function pastEndText(durationMs: number): string {
  return `The video ends at ${formatClipTime(durationMs)}.`;
}

const DIGITS = /^[0-9]{1,9}$/;

function int(s: string): number | undefined {
  return DIGITS.test(s) ? Number(s) : undefined;
}

/** "20", "20.5" (≤ 3 fraction digits) in ms; bounded: below 60 s. */
function seconds(s: string, bounded: boolean): number | undefined {
  const dot = s.indexOf('.');
  const whole = dot < 0 ? s : s.slice(0, dot);
  const v = int(whole);
  if (v === undefined || (bounded && v >= 60)) return undefined;
  let ms = v * 1000;
  if (dot >= 0) {
    const frac = s.slice(dot + 1);
    if (frac === '' || frac.length > 3 || int(frac) === undefined) return undefined;
    ms += Number(frac.padEnd(3, '0'));
  }
  return ms;
}

function colon(s: string): number | undefined {
  const parts = s.split(':');
  if (parts.length > 3) return undefined;
  let ms = 0;
  for (let i = 0; i < parts.length; i++) {
    const p = parts[i] ?? '';
    const last = i === parts.length - 1;
    if (i > 0 && (p.length < 2 || !/^[0-9]{2}/.test(p) || (!last && p.length !== 2))) return undefined;
    if (last) {
      const sec = seconds(p, i > 0);
      if (sec === undefined) return undefined;
      return ms + sec;
    }
    const v = int(p);
    if (v === undefined || (i > 0 && v >= 60)) return undefined;
    ms += v * (parts.length === 3 && i === 0 ? 3_600_000 : 60_000);
  }
  return ms;
}

function units(s: string): number | undefined {
  let ms = 0;
  let seen = '';
  const order = 'hms';
  while (s !== '') {
    const m = /^([0-9.]+)([hms])/.exec(s);
    if (!m) return undefined;
    const num = m[1] ?? '';
    const unit = m[2] ?? '';
    if (seen.includes(unit) || (seen !== '' && order.indexOf(seen[seen.length - 1] ?? '') >= order.indexOf(unit))) return undefined;
    const bounded = seen !== '';
    if (unit === 's') {
      const sec = seconds(num, bounded);
      if (sec === undefined) return undefined;
      ms += sec;
    } else {
      const v = int(num);
      if (v === undefined || (bounded && v >= 60)) return undefined;
      ms += v * (unit === 'h' ? 3_600_000 : 60_000);
    }
    seen += unit;
    s = s.slice(m[0].length);
  }
  return ms;
}

/**
 * A time of a part in ms (internal/media ParseClipTime): "80", "1:20", "01:20", "1:02:03",
 * "1:20.5" (≤ 3 fraction digits), "1m20s", "1h2m3s"; spaces around it ignored; 0 to 168 h.
 * Undefined when it is not one.
 */
export function parseClipTime(raw: string): number | undefined {
  const s = raw.trim();
  if (s === '' || s.length > 32) return undefined;
  let ms: number | undefined;
  if (s.includes(':')) ms = colon(s);
  else if (/[hmsHMS]/.test(s)) ms = units(s.toLowerCase());
  else ms = seconds(s, false);
  if (ms === undefined || ms < 0 || ms > MAX_CLIP_MS) return undefined;
  return ms;
}

/** "m:ss" under an hour, "h:mm:ss" from an hour; tenths only when not zero ("1:20.5"). */
export function formatClipTime(ms: number): string {
  const d = Math.max(0, Math.floor(ms));
  const tenths = Math.floor((d % 1000) / 100);
  const total = Math.floor(d / 1000);
  const h = Math.floor(total / 3600);
  const m = Math.floor(total / 60) % 60;
  const s = total % 60;
  const two = (n: number) => String(n).padStart(2, '0');
  let out = h > 0 ? `${h}:${two(m)}:${two(s)}` : `${m}:${two(s)}`;
  if (tenths > 0) out += `.${tenths}`;
  return out;
}

/** A part to send: [startMs, endMs), endMs 0 = to the end. */
export interface ClipRange {
  startMs: number;
  endMs: number;
}

/** What the Part row holds checked: its errors, else the part (undefined = the whole video). */
export interface ClipCheck {
  error?: string;
  clip?: ClipRange;
}

/**
 * Checks From and To for a video of durationMs (≤ 0 unknown) with the desktop's rules (B3): an
 * empty From is 0, an empty To the end, a To within 1 s past the end is the end; From 0 to the end
 * is the whole video.
 */
export function checkClip(from: string, to: string, durationMs: number): ClipCheck {
  let start = 0;
  let end = 0;
  if (from.trim() !== '') {
    const v = parseClipTime(from);
    if (v === undefined) return { error: CLIP_TEXTS.badTime };
    start = v;
  }
  if (to.trim() !== '') {
    const v = parseClipTime(to);
    if (v === undefined) return { error: CLIP_TEXTS.badTime };
    end = v;
  }
  return checkRange(start, end, durationMs);
}

/**
 * checkClip's rules for times in ms (endMs 0 = to the end), as the timeline trimmer holds them: a
 * To within 1 s past the end is the end; From 0 to the end is the whole video.
 */
export function checkRange(startMs: number, endMs: number, durationMs: number): ClipCheck {
  const start = startMs;
  let end = endMs;
  const dur = durationMs > 0 ? durationMs : 0;
  if (dur > 0 && end > dur + 1000) return { error: pastEndText(dur) };
  if (end > 0 && start >= end) return { error: CLIP_TEXTS.order };
  if (end > 0 && end - start < MIN_CLIP_MS) return { error: CLIP_TEXTS.short };
  if (end === 0 && dur > 0 && start >= dur) return { error: CLIP_TEXTS.order };
  if (end === 0 && dur > 0 && dur - start < MIN_CLIP_MS) return { error: CLIP_TEXTS.short };
  if (dur > 0 && end >= dur) end = 0;
  if (start === 0 && end === 0) return {};
  return { clip: { startMs: start, endMs: end } };
}

/**
 * A part's fields of a message (`downex:offer-add`, `downex:page-video-add`): `clipStartMs`, and
 * `clipEndMs` unless the part runs to the end.
 */
export function clipFields(clip: ClipRange): { clipStartMs: number; clipEndMs?: number } {
  return clip.endMs > 0 ? { clipStartMs: clip.startMs, clipEndMs: clip.endMs } : { clipStartMs: clip.startMs };
}

/** The Part row's summary: "Whole video", or "Part 1:20 – 2:45" ("… – end" with an open end). */
export function clipSummary(clip: ClipRange | undefined, durationMs: number): string {
  if (!clip) return CLIP_TEXTS.whole;
  const end = clip.endMs > 0 ? formatClipTime(clip.endMs) : durationMs > 0 ? formatClipTime(durationMs) : 'end';
  return `Part ${formatClipTime(clip.startMs)} – ${end}`;
}

/** A chapter of a videoInfo answer. */
export interface VideoChapter {
  startMs: number;
  endMs: number;
  title: string;
}

/** "1. Intro (0:00–1:20)". */
export function chapterLabel(c: VideoChapter, i: number): string {
  const title = c.title.trim() || `Chapter ${i + 1}`;
  return `${i + 1}. ${title} (${formatClipTime(c.startMs)}–${formatClipTime(c.endMs)})`;
}

/** Whether v is a part's field value a message may carry: an integer of ms within 168 h. */
export function isClipMs(v: unknown): v is number {
  return typeof v === 'number' && Number.isInteger(v) && v >= 0 && v <= MAX_CLIP_MS;
}
