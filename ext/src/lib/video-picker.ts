// The quality picker of a page's video (SITE-MEDIA-SPEC §5.3), shared by the YouTube overlay
// and the popup. It mirrors the desktop picker (`ui/frontend/src/lib/media.ts`) in this
// package; nothing is imported across packages. Every offered quality is an MP4: the daemon
// lists no other (§3.11), and each pick is saved as one .mp4. Qualities are listed one per
// class under their standard names ("4K (2160p)"; lib/quality.ts). Pure.

import type { VideoInfoResult, VideoTrack } from './protocol';
import { onePerClass, pictureText, trackQuality } from './quality';

export type PickerInfo = Pick<VideoInfoResult, 'tracks' | 'selectedVideo' | 'selectedAudio' | 'durationMs'>;

/** A choice of tracks: audio "" = none (a muxed video carries its own). */
export interface TrackPick {
  readonly video: string;
  readonly audio: string;
}

/** Tracks that carry video: video-only ones (paired with an audio track) and muxed ones. */
export function isVideoTrack(t: Pick<VideoTrack, 'kind'>): boolean {
  return t.kind === 'video' || t.kind === 'muxed';
}

/**
 * The qualities, one per quality class (SITE-MEDIA-SPEC amendment 2026-10-23), best class
 * first. downexd lists one per class already, and its pick stays the one of its class; an older
 * downexd's longer list (every resolution, frame rate, HDR and codec) is reduced by the same
 * rule (lib/quality.ts), so no client ever lists more than downexd would.
 */
export function videoTracks(info: Pick<VideoInfoResult, 'tracks'> & Partial<Pick<VideoInfoResult, 'selectedVideo'>>): VideoTrack[] {
  return onePerClass(info.tracks, info.selectedVideo ?? '');
}

/** The MP4 audio tracks in the daemon's order (original language first). */
export function audioTracks(info: Pick<VideoInfoResult, 'tracks'>): VideoTrack[] {
  return info.tracks.filter((t) => t.kind === 'audio');
}

export function trackById(info: Pick<VideoInfoResult, 'tracks'>, id: string): VideoTrack | undefined {
  return id ? info.tracks.find((t) => t.id === id) : undefined;
}

/**
 * The best audio track when the daemon named none: the first in its order (original language,
 * not DRC; §3.11), as the desktop picker and the daemon pair a video-only quality.
 */
function bestAudio(info: Pick<VideoInfoResult, 'tracks'>): VideoTrack | undefined {
  return audioTracks(info)[0];
}

/** The audio paired with a video-only pick: the daemon's default, else the best. */
export function defaultAudio(info: Pick<VideoInfoResult, 'tracks' | 'selectedAudio'>): string {
  const chosen = trackById(info, info.selectedAudio);
  return (chosen?.kind === 'audio' ? chosen : bestAudio(info))?.id ?? '';
}

/**
 * The picker's starting point: the daemon's pick where it names an offered video, else the
 * best video; unless that is muxed, the daemon's audio, else the best audio.
 */
export function defaultPick(info: PickerInfo | undefined): TrackPick {
  if (!info) return { video: '', audio: '' };
  const selected = trackById(info, info.selectedVideo);
  const video = selected && isVideoTrack(selected) ? selected : videoTracks(info)[0];
  if (!video || video.kind === 'muxed') return { video: video?.id ?? '', audio: '' };
  return { video: video.id, audio: defaultAudio(info) };
}

/** Whether a pick needs an audio track of its own (its video is not muxed). */
export function needsAudio(info: Pick<VideoInfoResult, 'tracks'>, video: string): boolean {
  return trackById(info, video)?.kind !== 'muxed' && info.tracks.some((t) => t.kind === 'audio');
}

/** Whether the audio select is shown: a video-only pick and several MP4 audio tracks. */
export function showsAudioChoice(info: Pick<VideoInfoResult, 'tracks'>, pick: TrackPick): boolean {
  return needsAudio(info, pick.video) && audioTracks(info).length > 1;
}

/** `pick` with another video; an audio track is chosen when the new one needs it. */
export function withVideo(info: Pick<VideoInfoResult, 'tracks' | 'selectedAudio'>, pick: TrackPick, video: string): TrackPick {
  const keep = trackById(info, pick.audio)?.kind === 'audio';
  return { video, audio: needsAudio(info, video) && !keep ? defaultAudio(info) : pick.audio };
}

/** The audio of a pick as sent to Downex: none with a muxed video. */
export function pickedAudio(info: Pick<VideoInfoResult, 'tracks'>, pick: TrackPick): string {
  return needsAudio(info, pick.video) ? pick.audio : '';
}

export interface PickSize {
  bytes: number;
  /** An estimate: a site's estimate, or bandwidth × duration. */
  approx: boolean;
}

/**
 * The size of a pick: the sum of the tracks' sizes when every chosen track has one (an estimate
 * if any is), else the bandwidth × duration estimate; undefined when neither is known.
 */
export function pickSize(info: PickerInfo, pick: TrackPick): PickSize | undefined {
  const video = trackById(info, pick.video);
  if (!video) return undefined;
  const audio = trackById(info, pickedAudio(info, pick));
  const chosen = audio ? [video, audio] : [video];
  if (chosen.every((t) => t.size > 0)) {
    return { bytes: chosen.reduce((n, t) => n + t.size, 0), approx: chosen.some((t) => t.sizeApprox) };
  }
  if (!(info.durationMs > 0) || !(video.bandwidth > 0)) return undefined;
  const bits = video.bandwidth + (audio?.bandwidth ?? 0);
  return { bytes: Math.round((bits * info.durationMs) / 8000), approx: true };
}

// ---------------------------------------------------------------------------------------
// Labels

/** Codec names shown in every UI (§3.11). */
const CODEC_NAMES: Readonly<Record<string, string>> = {
  avc1: 'H.264',
  avc3: 'H.264',
  av01: 'AV1',
  hvc1: 'HEVC',
  hev1: 'HEVC',
  vp09: 'VP9',
  vp9: 'VP9',
  mp4a: 'AAC',
  opus: 'Opus',
  'ac-3': 'AC-3',
  'ec-3': 'E-AC-3',
};

/** "avc1.640028" → "H.264"; an unknown codec keeps its four-character code. */
export function codecName(codec: string): string {
  const c = codec.trim();
  const family = c.split('.')[0]?.toLowerCase() ?? '';
  return Object.hasOwn(CODEC_NAMES, family) ? (CODEC_NAMES[family] ?? family) : family;
}

/** A track's codecs by name: "H.264", "AAC", or "H.264 + AAC" for a muxed track. */
export function codecsLabel(t: Pick<VideoTrack, 'codecs'>): string {
  return t.codecs
    .split(',')
    .map(codecName)
    .filter((n) => n !== '')
    .join(' + ');
}

/**
 * What makes two tracks the same quality when several sources list them (VIDEO-DETECT-SPEC
 * §2.6: a master playlist and the variant the player loaded, or the same video under another
 * address): the same encoding, that is kind, picture, frame rate (rounded), codec family and HDR.
 * Tracks of one class with other pictures, frame rates or codecs are other videos (or other
 * encodings), and each source keeps them. Tracks of unknown picture are the same only in kind,
 * frame rate, codec family and name.
 */
export function qualityKey(t: Pick<VideoTrack, 'kind' | 'name' | 'width' | 'height' | 'frameRate' | 'codecs'>): string {
  const q = trackQuality(t);
  const family = codecName(t.codecs.split(',')[0] ?? '');
  const fps = Number.isFinite(t.frameRate) ? Math.round(t.frameRate) : 0;
  const side = (n: number) => (Number.isFinite(n) && n > 0 ? n : 0);
  if (side(t.width) > 0 || side(t.height) > 0) {
    return ['picture', t.kind, side(t.width), side(t.height), fps, family, q.hdr].join('|');
  }
  return ['unknown', t.kind, fps, family, q.name].join('|');
}

/**
 * The quality's name: downexd's standard name ("4K (2160p)", "1080p (Full HD) 60fps"), else the
 * standard name of its picture (an older downexd's "1634p" or "1920x1080"), else "Video" or
 * "Video and audio". Never a raw height.
 */
export function qualityName(t: Pick<VideoTrack, 'name' | 'width' | 'height' | 'frameRate' | 'kind'>): string {
  return trackQuality(t).name || (t.kind === 'muxed' ? 'Video and audio' : 'Video');
}

const BYTE_UNITS = ['B', 'KB', 'MB', 'GB', 'TB'] as const;

/**
 * 1500 → "1.5 KB", as the desktop shows sizes: decimal units (1 KB = 1,000 B, 1 MB = 1,000,000 B),
 * one decimal below 100 (a value that rounds to 100 has none: 99,950 B → "100 KB"), and a value that
 * would round to 1000 is shown in the next unit (999,999 B → "1.0 MB"), so no size has four integer
 * digits.
 */
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return '';
  if (bytes < 999.5) return `${Math.round(bytes)} B`;
  let unit = 0;
  let value = bytes;
  while (value >= 1000 && unit < BYTE_UNITS.length - 1) {
    value /= 1000;
    unit++;
  }
  let digits = value < 100 && Number(value.toFixed(1)) < 100 ? 1 : 0;
  if (Number(value.toFixed(digits)) >= 1000 && unit < BYTE_UNITS.length - 1) {
    value /= 1000;
    unit++;
    digits = 1;
  }
  return `${value.toFixed(digits)} ${BYTE_UNITS[unit]}`;
}

/** "412 MB", "~1.2 GB" for an estimate. */
export function sizeLabel(size: PickSize | undefined): string {
  if (!size) return '';
  return `${size.approx ? '~' : ''}${formatBytes(size.bytes)}`;
}

/** 128000 → "128 kbit/s", as the desktop shows bitrates. */
export function formatBitrate(bitsPerSecond: number): string {
  if (!Number.isFinite(bitsPerSecond) || bitsPerSecond <= 0) return '';
  if (bitsPerSecond < 1000) return `${Math.round(bitsPerSecond)} bit/s`;
  if (bitsPerSecond < 999_500) return `${Math.round(bitsPerSecond / 1000)} kbit/s`;
  const mbit = bitsPerSecond / 1_000_000;
  return `${mbit.toFixed(mbit < 100 ? 1 : 0)} Mbit/s`;
}

let languageNames: Intl.DisplayNames | null | undefined;

/** "en" → "English"; the tag itself when it is not a valid language tag. */
export function languageName(tag: string): string {
  if (!tag) return '';
  if (languageNames === undefined) {
    try {
      languageNames = new Intl.DisplayNames(['en'], { type: 'language', fallback: 'code' });
    } catch {
      languageNames = null;
    }
  }
  try {
    return languageNames?.of(tag) ?? tag;
  } catch {
    return tag;
  }
}

/** "English original · AAC · 129 kbit/s"; the language is added when the name lacks it. */
export function audioLabel(t: VideoTrack): string {
  const lang = languageName(t.language);
  let title = t.name.trim() || lang || 'Audio';
  if (t.name.trim() && lang && !title.toLowerCase().includes(lang.toLowerCase())) title = `${title} (${lang})`;
  return [title, codecsLabel(t), formatBitrate(t.bandwidth)].filter((p) => p !== '').join(' · ');
}

/** One quality of the list: a video with the audio it would be paired with. */
export interface QualityRow {
  /** The video track. */
  id: string;
  /** "4K (2160p) · H.264 · 860 MB"; a muxed one names both codecs: "360p · H.264 + AAC · 16.0 MB". */
  label: string;
  /** "4K (2160p)", "1080p (Full HD) 60fps". */
  quality: string;
  /** "H.264", "AV1", "H.264 + AAC"; "" when unknown. */
  codecs: string;
  /** "" when unknown. */
  size: string;
  /** The picture, "3840×1634" (a tooltip, never the name); "" when unknown. */
  picture: string;
}

/**
 * The qualities as the panels list them, one per class, best first, each with `audio` (or the
 * default audio) for its size: "4K (2160p) · AV1 · 1.9 GB", "1080p (Full HD) 60fps · H.264 ·
 * ~412 MB". Every quality is saved as one MP4, so the rows do not repeat it. A muxed quality
 * names its audio codec too ("H.264 + AAC"), as the desktop does.
 */
export function qualityRows(info: PickerInfo, audio: string): QualityRow[] {
  return videoTracks(info).map((t) => {
    const pick = withVideo(info, { video: '', audio }, t.id);
    const size = sizeLabel(pickSize(info, pick));
    const quality = qualityName(t);
    const codecs = codecsLabel(t);
    const label = [quality, codecs, size].filter((p) => p !== '').join(' · ');
    return { id: t.id, label, quality, codecs, size, picture: pictureText(t) };
  });
}
