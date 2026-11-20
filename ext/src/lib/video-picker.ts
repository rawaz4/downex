// The quality picker of a page's video (SITE-MEDIA-SPEC §5.3), shared by the YouTube overlay
// and the popup. It mirrors the desktop picker (`ui/frontend/src/lib/media.ts`) in this
// package; nothing is imported across packages. Every offered quality is an MP4: the daemon
// lists no other (§3.11), and each pick is saved as one .mp4. Qualities are listed one per
// class under their standard names ("4K (2160p)"; lib/quality.ts). Pure.

import type { ClipRange } from './clip';
import type { VideoInfoResult, VideoTrack } from './protocol';
import { onePerClass, pictureText, trackQuality } from './quality';

export type PickerInfo = Pick<VideoInfoResult, 'tracks' | 'selectedVideo' | 'selectedAudio' | 'durationMs'> &
  Partial<Pick<VideoInfoResult, 'container'>>;

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
 * An audio page's answer (SITE-MEDIA-SPEC §3.13): audio tracks only (container `m4a` or `mp3`).
 * Its rows are the audio tracks, a pick sends one as its audio (no video), and no Part row shows.
 * Any list without a video track counts, so an answer without a container is read the same way.
 */
export function audioOnlyList(info: Pick<VideoInfoResult, 'tracks'>): boolean {
  return info.tracks.length > 0 && !info.tracks.some(isVideoTrack) && info.tracks.some((t) => t.kind === 'audio');
}

/** The file type an answer is saved as: "MP4", or an audio page's "M4A" or "MP3" (SITE-MEDIA-SPEC §3.13). */
export function fileTypeLabel(info: Partial<Pick<VideoInfoResult, 'container'>> | undefined): string {
  const c = info?.container?.toLowerCase();
  return c === 'm4a' || c === 'mp3' ? c.toUpperCase() : 'MP4';
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
  if (audioOnlyList(info)) return { video: '', audio: defaultAudio(info) };
  const selected = trackById(info, info.selectedVideo);
  const video = selected && isVideoTrack(selected) ? selected : videoTracks(info)[0];
  if (!video || video.kind === 'muxed') return { video: video?.id ?? '', audio: '' };
  return { video: video.id, audio: defaultAudio(info) };
}

/** The badge of the quality a panel recommends and preselects (VIDEO-DETECT-SPEC §5.2, amended 2026-11-19). */
export const RECOMMENDED_TEXT = 'Recommended';

/**
 * The highest quality with sound (VIDEO-DETECT-SPEC §5.2 "Recommended", amended 2026-11-19): of
 * `tracks` (video or muxed), the one of the largest picture (width × height, else height) that has
 * sound (muxed, or a video paired with an audio track: `paired`), the first of equals; without
 * one with sound, the largest. Undefined for no track.
 */
export function highestWithSound<T extends Pick<VideoTrack, 'kind' | 'width' | 'height'>>(tracks: readonly T[], paired: (t: T) => boolean): T | undefined {
  const area = (t: T) => (t.width > 0 && t.height > 0 ? t.width * t.height : t.height * t.height);
  const best = (list: readonly T[]) => list.reduce<T | undefined>((b, t) => (!b || area(t) > area(b) ? t : b), undefined);
  return best(tracks.filter((t) => t.kind === 'muxed' || paired(t))) ?? best(tracks);
}

/** The pick a panel that recommends starts with: the highest quality with sound (highestWithSound). */
export function recommendedPick(info: PickerInfo | undefined): TrackPick {
  const pick = defaultPick(info);
  if (!info || audioOnlyList(info)) return pick;
  const sound = audioTracks(info).length > 0;
  const best = highestWithSound(videoTracks(info), () => sound);
  return best ? withVideo(info, pick, best.id) : pick;
}

/** Whether a pick needs an audio track of its own (its video is not muxed). */
export function needsAudio(info: Pick<VideoInfoResult, 'tracks'>, video: string): boolean {
  return trackById(info, video)?.kind !== 'muxed' && info.tracks.some((t) => t.kind === 'audio');
}

/**
 * Whether the audio select is shown: a video-only pick and several MP4 audio tracks. Never for an
 * audio page, whose rows are its audio tracks.
 */
export function showsAudioChoice(info: Pick<VideoInfoResult, 'tracks'>, pick: TrackPick): boolean {
  return !audioOnlyList(info) && needsAudio(info, pick.video) && audioTracks(info).length > 1;
}

/**
 * `pick` with another video; an audio track is chosen when the new one needs it. On an audio page
 * (audioOnlyList) a row's id is an audio track: the pick is that audio, without video.
 */
export function withVideo(info: Pick<VideoInfoResult, 'tracks' | 'selectedAudio'>, pick: TrackPick, video: string): TrackPick {
  if (audioOnlyList(info) && trackById(info, video)?.kind === 'audio') return { video: '', audio: video };
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
  if (!video && audioOnlyList(info)) {
    // An audio page's pick: the audio alone (SITE-MEDIA-SPEC §3.13).
    const audio = trackById(info, pick.audio);
    if (!audio) return undefined;
    if (audio.size > 0) return { bytes: audio.size, approx: audio.sizeApprox };
    if (!(info.durationMs > 0) || !(audio.bandwidth > 0)) return undefined;
    return { bytes: Math.round((audio.bandwidth * info.durationMs) / 8000), approx: true };
  }
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

/**
 * A pick's size for a part of the video (MEDIA-SPEC §8; the desktop's `ClipSize`, SITE-MEDIA-SPEC
 * §5.3): the size × (To − From + 5 s, rounded up to a second) ÷ the duration, always an estimate
 * ("~"). The whole video, an unknown size or an unknown duration keep the size as it is.
 */
export function clipSize(size: PickSize | undefined, clip: ClipRange | undefined, durationMs: number): PickSize | undefined {
  if (!size || !clip || !(size.bytes > 0) || !(durationMs > 0)) return size;
  const end = clip.endMs <= 0 || clip.endMs > durationMs ? durationMs : clip.endMs;
  const span = Math.floor((end - clip.startMs + 5000 + 999) / 1000) * 1000;
  if (span >= durationMs || span <= 0) return size;
  return { bytes: Math.max(Math.round((size.bytes * span) / durationMs), 1), approx: true };
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
  mp3: 'MP3',
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

/** What a quality row says of a video that has no sound (SITE-MEDIA-SPEC §5.3, 2026-11-18). */
export const NO_SOUND = 'No sound';

/** One quality of the list: a video with the audio it would be paired with. */
export interface QualityRow {
  /** The video track. */
  id: string;
  /** "4K (2160p) · H.264 · 860 MB"; a muxed one names both codecs: "360p · H.264 + AAC · 16.0 MB". */
  label: string;
  /** "4K (2160p)", "1080p (Full HD) 60fps". */
  quality: string;
  /**
   * "H.264", "AV1", "H.264 + AAC"; "" when unknown. A video without sound (a video-only track of a
   * list without audio: a silent page, SITE-MEDIA-SPEC §5.3) adds "No sound": "H.264 · No sound".
   */
  codecs: string;
  /** "" when unknown. */
  size: string;
  /** The picture, "3840×1634" (a tooltip, never the name); "" when unknown. */
  picture: string;
  /** An audio page's row (SITE-MEDIA-SPEC §3.13): `id` is an audio track, sent as the pick's audio. */
  audio?: boolean;
}

/** What an audio page's row is named when its track has no note (SITE-MEDIA-SPEC §3.13). */
export const AUDIO_QUALITY = 'Audio';

/**
 * The qualities as the panels list them, one per class, best first, each with `audio` (or the
 * default audio) for its size: "4K (2160p) · AV1 · 1.9 GB", "1080p (Full HD) 60fps · H.264 ·
 * ~412 MB"; with `clip`, the part's estimate (clipSize, always "~"). Every quality is saved as one MP4, so the rows do not repeat it. A muxed quality
 * names its audio codec too ("H.264 + AAC"), as the desktop does.
 */
export function qualityRows(info: PickerInfo, audio: string, clip?: ClipRange): QualityRow[] {
  if (audioOnlyList(info)) {
    // An audio page (SITE-MEDIA-SPEC §3.13): "Audio · AAC · 96 kbit/s · 1.7 MB", no part.
    return audioTracks(info).map((t) => {
      const quality = t.name.trim() || AUDIO_QUALITY;
      const codecs = [codecsLabel(t), formatBitrate(t.bandwidth)].filter((p) => p !== '').join(' · ');
      const size = sizeLabel(pickSize(info, { video: '', audio: t.id }));
      const label = [quality, codecs, size].filter((p) => p !== '').join(' · ');
      return { id: t.id, label, quality, codecs, size, picture: '', audio: true };
    });
  }
  const silent = audioTracks(info).length === 0;
  return videoTracks(info).map((t) => {
    const pick = withVideo(info, { video: '', audio }, t.id);
    const size = sizeLabel(clipSize(pickSize(info, pick), clip, info.durationMs));
    const quality = qualityName(t);
    const codecs = [codecsLabel(t), silent && t.kind === 'video' ? NO_SOUND : ''].filter((p) => p !== '').join(' · ');
    const label = [quality, codecs, size].filter((p) => p !== '').join(' · ');
    return { id: t.id, label, quality, codecs, size, picture: pictureText(t) };
  });
}
