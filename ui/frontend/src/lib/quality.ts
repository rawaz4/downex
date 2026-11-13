// Standard quality names (SITE-MEDIA-SPEC.md, amendment 2026-10-23). A video's quality class
// comes from its picture's size, not its raw height: a 3840×1634 film is "4K (2160p)", never
// "1634p". downexd names every track this way (internal/media/quality.go) and lists one quality
// per class; the UIs show its names. They compute a name here only where none came with the
// track (an older downexd, a manifest's own label, a file the page plays), and reduce a list to
// one quality per class by the same rule as downexd, so they never contradict its list.
//
// This file is the same in ext/src/lib/quality.ts and ui/frontend/src/lib/quality.ts (nothing is
// imported across packages); both are tested with the tables of internal/media/quality_test.go,
// and internal/media/quality_sync_test.go checks that the copies and the tables agree.
// Pure.

/** The standard steps, smallest first. */
export const QUALITY_LADDER: readonly number[] = [144, 240, 360, 480, 720, 1080, 1440, 2160, 4320];

/** The display name of each step that has a common name of its own. */
const CLASS_LABELS: Readonly<Record<number, string>> = {
  4320: '8K (4320p)',
  2160: '4K (2160p)',
  1440: '1440p (2K)',
  1080: '1080p (Full HD)',
  720: '720p (HD)',
};

function count(n: number): number {
  return Number.isFinite(n) && n > 0 ? n : 0;
}

/** round(n × 9/16), halves up: a width or a portrait's height scaled to 16:9. */
function scale169(n: number): number {
  return Math.round((n * 9) / 16);
}

/**
 * The quality class of a picture: the ladder step nearest to its longer side scaled to 16:9 —
 * max(height, round(width × 9/16)) for landscape, max(width, round(height × 9/16)) for portrait
 * — a tie going to the larger step. So 3840×1634 is 2160, 1920×818 is 1080, 1080×1920 (portrait)
 * is 1080, and 1440×1080 (4:3) is 1080: its height already is a step and its width adds nothing.
 * Above 4320 it is 4320, below 144 it is 144. With one side known it is that side (a height) or
 * the width scaled to 16:9; 0 when neither is known.
 */
export function qualityClass(width: number, height: number): number {
  const w = count(width);
  const h = count(height);
  let side: number;
  if (w === 0) side = h;
  else if (h === 0) side = scale169(w);
  else side = w >= h ? Math.max(h, scale169(w)) : Math.max(w, scale169(h));
  if (side === 0) return 0;
  let best = QUALITY_LADDER[0] ?? 0;
  for (const step of QUALITY_LADDER) if (Math.abs(side - step) <= Math.abs(side - best)) best = step;
  return best;
}

/** "4K (2160p)", "1080p (Full HD)", "480p"; "" for no class. */
export function classLabel(cls: number): string {
  if (!QUALITY_LADDER.includes(cls)) return '';
  return CLASS_LABELS[cls] ?? `${cls}p`;
}

/**
 * A class's display name with " 60fps" when the frame rate rounds to more than 30 (59.94 → 60;
 * 30.4 states none) and " HDR": "4K (2160p) 60fps HDR", "1080p (Full HD)", "480p"; "" for no
 * class.
 */
export function standardName(cls: number, frameRate: number, hdr: boolean): string {
  const label = classLabel(cls);
  if (!label) return '';
  const fps = Math.round(count(frameRate));
  return `${label}${fps > 30 ? ` ${fps}fps` : ''}${hdr ? ' HDR' : ''}`;
}

const STANDARD_NAME_RE = /^(8K \(4320p\)|4K \(2160p\)|1440p \(2K\)|1080p \(Full HD\)|720p \(HD\)|480p|360p|240p|144p)(?: ([1-9][0-9]{1,2})fps)?( HDR)?$/;

/** A standard name taken apart. */
export interface StandardName {
  readonly cls: number;
  /** The rounded frame rate the name states; 0 when it states none (30 or less). */
  readonly fps: number;
  readonly hdr: boolean;
}

/**
 * Takes apart a name of standardName's form ("4K (2160p) 60fps HDR"); undefined for anything
 * else: an older downexd's "1634p", "1080p60" or "2160p60 HDR", a manifest's "1920x1080".
 */
export function parseStandardName(name: string): StandardName | undefined {
  const m = STANDARD_NAME_RE.exec(name.trim());
  if (!m) return undefined;
  const label = m[1] ?? '';
  const cls = Number(Object.entries(CLASS_LABELS).find(([, l]) => l === label)?.[0] ?? label.slice(0, -1));
  return { cls, fps: m[2] ? Number(m[2]) : 0, hdr: m[3] !== undefined };
}

/** What naming and choosing a video track reads of it. */
export interface QualityTrack {
  readonly id: string;
  /** "video" (video only), "muxed" (with its own sound), or "audio". */
  readonly kind: string;
  readonly name: string;
  readonly width: number;
  readonly height: number;
  readonly frameRate: number;
  readonly codecs: string;
  readonly bandwidth: number;
  /** Bytes; 0 when unknown. */
  readonly size: number;
}

/** A video track's quality: its class (0 unknown), its display name ("" unknown) and HDR. */
export interface TrackQuality {
  readonly cls: number;
  readonly name: string;
  readonly hdr: boolean;
}

/**
 * A video track's quality, as every UI shows it (internal/media TrackName in Go): downexd's name
 * when it is a standard one (kept as it is, even where the picture alone would say otherwise:
 * downexd may follow the site's own label), unless it is an older downexd's raw height — a class
 * that is the picture's height while the picture is of another class ("480p" for 1138×480) —;
 * else the standard name of its picture (HDR when its old name said so); else its name as it is.
 */
export function trackQuality(t: Pick<QualityTrack, 'name' | 'width' | 'height' | 'frameRate'>): TrackQuality {
  const name = t.name.trim();
  const std = parseStandardName(name);
  const cls = qualityClass(t.width, t.height);
  if (std && (std.cls !== count(t.height) || std.cls === cls)) return { cls: std.cls, name, hdr: std.hdr };
  const hdr = /\bHDR\b/i.test(name);
  return cls > 0 ? { cls, name: standardName(cls, t.frameRate, hdr), hdr } : { cls: 0, name, hdr };
}

/** "3840×1634", the secondary detail beside a quality's name; "" when unknown. */
export function pictureText(t: Pick<QualityTrack, 'width' | 'height'>): string {
  return count(t.width) > 0 && count(t.height) > 0 ? `${t.width}×${t.height}` : '';
}

/** H.264 plays everywhere, then HEVC, then AV1; other codecs after them. */
const CODEC_RANK: Readonly<Record<string, number>> = { avc1: 0, avc3: 0, hvc1: 1, hev1: 1, av01: 2 };

function codecRank(codecs: string): number {
  const family = (codecs.split(',')[0] ?? '').trim().split('.')[0]?.toLowerCase() ?? '';
  return Object.hasOwn(CODEC_RANK, family) ? (CODEC_RANK[family] ?? 3) : 3;
}

/**
 * Orders two tracks of one class, the one to keep first, as downexd does: video only before muxed
 * (a muxed format is dropped when a video-only one of its class exists), SDR before HDR, the
 * higher frame rate (rounded: 59.94 and 60 are equal), H.264 before HEVC before AV1, the higher
 * bitrate, the larger picture, the larger size. 0 keeps the given order (downexd's).
 */
export function compareInClass(a: QualityTrack, b: QualityTrack): number {
  return (
    Number(a.kind !== 'video') - Number(b.kind !== 'video') ||
    Number(trackQuality(a).hdr) - Number(trackQuality(b).hdr) ||
    Math.round(count(b.frameRate)) - Math.round(count(a.frameRate)) ||
    codecRank(a.codecs) - codecRank(b.codecs) ||
    count(b.bandwidth) - count(a.bandwidth) ||
    count(b.width) * count(b.height) - count(a.width) * count(a.height) ||
    count(b.size) - count(a.size)
  );
}

/**
 * One video track per quality class, best class first: of each class the first by
 * compareInClass, except that `keep` (downexd's own pick) is the one kept of its class.
 * Tracks of unknown class follow, all of them, in their order. Audio tracks are left out.
 */
export function onePerClass<T extends QualityTrack>(tracks: readonly T[], keep = ''): T[] {
  const best = new Map<number, T>();
  const unknown: T[] = [];
  for (const t of tracks) {
    if (t.kind !== 'video' && t.kind !== 'muxed') continue;
    const cls = trackQuality(t).cls;
    if (cls === 0) {
      unknown.push(t);
      continue;
    }
    const kept = best.get(cls);
    if (!kept || (kept.id !== keep && (t.id === keep || compareInClass(t, kept) < 0))) best.set(cls, t);
  }
  return [...[...best.entries()].sort((a, b) => b[0] - a[0]).map(([, t]) => t), ...unknown];
}
