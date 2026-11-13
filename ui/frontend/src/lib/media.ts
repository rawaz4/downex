// Streaming media (MEDIA-SPEC §6) and the video of web pages (SITE-MEDIA-SPEC §5): the
// quality picker's track order, labels, sizes and default pick, the facts shown for a
// media download, and what to tell the user about the daemon's refusals and errors.

import { ApiError, toApiError, type Download, type MediaInfo, type MediaOrigin, type MediaTrack } from './api';
import { formatBitrate, formatBytes, formatPercent } from './format';
import { onePerClass, pictureText, trackQuality } from './quality';

export function isMedia(d: Pick<Download, 'kind'>): boolean {
  return d.kind === 'media';
}

/** A web page's video (yt-dlp), not an HLS/DASH manifest. */
export function isSite(info: Pick<MediaInfo, 'origin'> | null | undefined): boolean {
  return info?.origin === 'site';
}

/** A media download of a web page's video. */
export function isSiteDownload(d: Pick<Download, 'kind' | 'media'>): boolean {
  return d.kind === 'media' && isSite(d.media);
}

/** "YouTube video"; "Video" when the site has no name. */
export function siteVideoLabel(info: Pick<MediaInfo, 'site'>): string {
  const site = info.site.trim();
  return site ? `${site} video` : 'Video';
}

/** "HLS", "DASH"; another format in capitals; "" when unknown. */
export function formatLabel(format: string): string {
  return format.trim().toUpperCase();
}

/**
 * Every stream is saved as MP4 (VIDEO-DETECT-SPEC §3.9); "ts" is a download stored as
 * MPEG-TS before 2026-10-23, which keeps its byte-for-byte .ts.
 */
const CONTAINERS: Record<string, string> = { mp4: 'MP4', ts: 'MPEG-TS' };

/** Output container for display ("" while the daemon has not decided). */
export function containerLabel(container: string): string {
  const c = container.trim().toLowerCase();
  return CONTAINERS[c] ?? c.toUpperCase();
}

/** The codec names every UI uses (SITE-MEDIA-SPEC §3.11), by the codec's first part. */
const CODEC_NAMES: Record<string, string> = {
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

/** "avc1.640028" → "H.264", "mp4a.40.2" → "AAC"; another codec as it is. */
export function codecName(codec: string): string {
  const c = codec.trim();
  return CODEC_NAMES[(c.split('.')[0] ?? '').toLowerCase()] ?? c;
}

/** The names of a codecs list: "avc1.42001E, mp4a.40.2" → "H.264 + AAC". */
export function codecNames(codecs: string): string {
  return codecs
    .split(',')
    .map((c) => c.trim())
    .filter((c) => c !== '')
    .map(codecName)
    .join(' + ');
}

/** Tracks that carry video: video-only ones (paired with an audio track) and muxed ones. */
export function isVideoTrack(t: Pick<MediaTrack, 'kind'>): boolean {
  return t.kind === 'video' || t.kind === 'muxed';
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

/**
 * Video tracks, one per quality class (SITE-MEDIA-SPEC amendment 2026-10-23), best class
 * first. downexd lists one per class already (a web page's in its own order); an older
 * downexd's longer list is reduced by the same rule (lib/quality.ts: video only, SDR, frame
 * rate, H.264, bitrate, picture, size), so the picker never lists more than downexd would.
 * `keep` (the daemon's pick, `selectedVideo`) stays the quality of its class. A manifest's
 * tracks are sorted by picture, bandwidth, frame rate and ID first, which settles what the
 * rule leaves equal.
 */
export function videoTracks(tracks: readonly MediaTrack[], site = false, keep = ''): MediaTrack[] {
  const videos = tracks.filter(isVideoTrack);
  if (!site) {
    videos.sort(
      (a, b) =>
        b.width * b.height - a.width * a.height ||
        b.height - a.height ||
        b.bandwidth - a.bandwidth ||
        b.frameRate - a.frameRate ||
        a.id.localeCompare(b.id),
    );
  }
  return onePerClass(videos, keep);
}

/**
 * Audio tracks: a web page's in downexd's order (the original language first); a
 * manifest's by language (undeclared last), then highest bandwidth first.
 */
export function audioTracks(tracks: readonly MediaTrack[], site = false): MediaTrack[] {
  const audios = tracks.filter((t) => t.kind === 'audio');
  if (site) return audios;
  return audios.sort((a, b) => {
    if (!a.language !== !b.language) return a.language ? -1 : 1;
    return (
      languageName(a.language).localeCompare(languageName(b.language)) ||
      b.bandwidth - a.bandwidth ||
      a.name.localeCompare(b.name) ||
      a.id.localeCompare(b.id)
    );
  });
}

function trimRate(fps: number): string {
  return String(Math.round(fps * 100) / 100);
}

/** A track's declared size: "412 MB", "~412 MB" for the site's estimate, "" when unknown. */
export function trackSizeText(t: Pick<MediaTrack, 'size' | 'sizeApprox'>): string {
  if (!(t.size > 0)) return '';
  return `${t.sizeApprox ? '~' : ''}${formatBytes(t.size)}`;
}

/**
 * A video quality's name: downexd's standard name ("4K (2160p)", "1080p (Full HD) 60fps",
 * "480p"), else the standard name of its picture (an older downexd's "1634p", a manifest's
 * "1920x1080"), else its name as it is; never a raw height. "" when nothing names it.
 */
export function qualityName(t: Pick<MediaTrack, 'name' | 'width' | 'height' | 'frameRate'>): string {
  return trackQuality(t).name;
}

/** A quality's name, or what the track is when nothing names it. */
function shownName(t: MediaTrack): string {
  return qualityName(t) || (t.kind === 'muxed' ? 'Video and audio' : 'Video');
}

/**
 * The picture of a video track, the secondary detail beside its name: "3840×1634",
 * "1920×1080 · 29.97 fps" (a manifest's, whose frame rate the name states only over 30);
 * "" when unknown.
 */
export function videoPictureText(t: MediaTrack, site = false): string {
  const picture = pictureText(t);
  if (site || !picture || !(t.frameRate > 0)) return picture;
  return `${picture} · ${trimRate(t.frameRate)} fps`;
}

/**
 * A video quality as the picker lists it: its standard name, its codecs by name and its
 * bitrate, "1080p (Full HD) · H.264 · 5.0 Mbit/s" (a muxed one names both codecs: "480p ·
 * H.264 + AAC · 1.2 Mbit/s"). A web page's (`site`) gives the site's size where it has one:
 * "1080p (Full HD) 60fps · H.264 · 412 MB", "360p · H.264 + AAC · ~12 MB". The picture is
 * not part of it (videoPictureText).
 */
export function videoTrackLabel(t: MediaTrack, site = false): string {
  const parts = [shownName(t)];
  const codecs = codecNames(t.codecs);
  if (codecs) parts.push(codecs);
  const size = (site ? trackSizeText(t) : '') || formatBitrate(t.bandwidth);
  if (size) parts.push(size);
  return parts.join(' · ');
}

/**
 * A web page's quality as the picker lists it (SITE-MEDIA-SPEC §5.3), with the size of the file
 * it makes: the video and the audio `pick` pairs it with (its audio, or the default one), as the
 * browser's picker shows it: "4K (2160p) · AV1 · 860 MB", "360p · H.264 + AAC · ~12 MB".
 */
export function siteQualityLabel(info: Pick<MediaInfo, 'tracks' | 'durationMs'> & Partial<Pick<MediaInfo, 'origin'>>, pick: TrackPick, t: MediaTrack): string {
  const parts = [shownName(t)];
  const codecs = codecNames(t.codecs);
  if (codecs) parts.push(codecs);
  const size = pickSizeText(pickSize(info, withVideo(info, pick, t.id))) || formatBitrate(t.bandwidth);
  if (size) parts.push(size);
  return parts.join(' · ');
}

/**
 * "Commentary (English) · 128 kbit/s · mp4a.40.2". A web page's (`site`) names its codec
 * and adds its size: "English · AAC · 129 kbit/s · 9.3 MB".
 */
/** yt-dlp's quality words in a page's audio format note ("medium", "low, DRC"): not a name. */
const AUDIO_NOTE_WORDS = /^(?:ultralow|low|medium|high|drc)$/i;

/**
 * An audio track's name: its own name with the language unless the name says it ("Deutsch
 * (German)", "English original"). A page's track (`site`) is named by yt-dlp's format note,
 * whose quality words ("medium", "low") are left out, so "medium" reads "Audio" (or its
 * language); dynamic range compression is said as "· DRC".
 */
export function audioTrackTitle(t: Pick<MediaTrack, 'name' | 'language'>, site = false): string {
  const lang = languageName(t.language);
  let name = t.name.trim();
  let drc = false;
  if (site) {
    const words = name.split(',').map((w) => w.trim()).filter((w) => w !== '');
    drc = words.some((w) => w.toLowerCase() === 'drc');
    name = words.filter((w) => !AUDIO_NOTE_WORDS.test(w)).join(', ');
  }
  let title = name || lang || 'Audio';
  if (name && lang && !name.toLowerCase().includes(lang.toLowerCase())) title = `${name} (${lang})`;
  return drc ? `${title} · DRC` : title;
}

export function audioTrackLabel(t: MediaTrack, site = false): string {
  const parts = [audioTrackTitle(t, site)];
  if (site && t.codecs) parts.push(codecNames(t.codecs));
  const rate = formatBitrate(t.bandwidth);
  if (rate) parts.push(rate);
  if (site) {
    const size = trackSizeText(t);
    if (size) parts.push(size);
  } else if (t.codecs) parts.push(t.codecs);
  return parts.join(' · ');
}

export function trackById(info: Pick<MediaInfo, 'tracks'>, id: string): MediaTrack | undefined {
  return id ? info.tracks.find((t) => t.id === id) : undefined;
}

/**
 * A choice of tracks: video "" = none (none offered, or the daemon's audio-only pick of a
 * manifest); audio "" = none (or in the muxed video).
 */
export interface TrackPick {
  readonly video: string;
  readonly audio: string;
}

/** The best audio track: a web page's first (downexd's default order), else the highest bandwidth. */
function bestAudio(info: Pick<MediaInfo, 'tracks'> & Partial<Pick<MediaInfo, 'origin'>>): MediaTrack | undefined {
  if (info.origin === 'site') return info.tracks.find((t) => t.kind === 'audio');
  let best: MediaTrack | undefined;
  for (const t of info.tracks) if (t.kind === 'audio' && (!best || t.bandwidth > best.bandwidth)) best = t;
  return best;
}

/**
 * Whether "no video" can be chosen. A video track "" asks the daemon for its own pick, so it
 * means audio only when that pick is: the daemon leaves out video tracks it cannot save (e.g.
 * WebM or SegmentBase DASH Representations) although the stream still lists them. Never for
 * a web page: its video is always one MP4 file with a picture.
 */
export function audioOnlyOffered(
  info: Pick<MediaInfo, 'tracks' | 'selectedVideo' | 'selectedAudio'> & Partial<Pick<MediaInfo, 'origin'>>,
): boolean {
  if (info.origin === 'site') return false;
  return info.selectedVideo === '' && trackById(info, info.selectedAudio)?.kind === 'audio';
}

type PickInfo = Pick<MediaInfo, 'tracks' | 'selectedVideo' | 'selectedAudio'> & Partial<Pick<MediaInfo, 'origin'>>;

/**
 * The picker's starting point: the daemon's pick where it names offered tracks (a
 * manifest's audio-only pick included), else the best video and, unless that is muxed, the
 * best audio.
 */
export function defaultPick(info: PickInfo | null): TrackPick {
  if (!info) return { video: '', audio: '' };
  if (audioOnlyOffered(info)) return { video: '', audio: info.selectedAudio };
  const selected = trackById(info, info.selectedVideo);
  const video = selected && isVideoTrack(selected) ? selected : videoTracks(info.tracks, info.origin === 'site', info.selectedVideo)[0];
  if (video?.kind === 'muxed') return { video: video.id, audio: '' };
  const chosen = trackById(info, info.selectedAudio);
  const audio = chosen?.kind === 'audio' ? chosen : bestAudio(info);
  return { video: video?.id ?? '', audio: audio?.id ?? '' };
}

/** Whether a pick needs an audio track of its own (its video is not muxed). */
export function needsAudio(info: Pick<MediaInfo, 'tracks'>, video: string): boolean {
  return trackById(info, video)?.kind !== 'muxed' && info.tracks.some((t) => t.kind === 'audio');
}

/** `pick` with another video track; an audio track is chosen when the new one needs it. */
export function withVideo(
  info: Pick<MediaInfo, 'tracks'> & Partial<Pick<MediaInfo, 'origin'>>,
  pick: TrackPick,
  video: string,
): TrackPick {
  const audio = needsAudio(info, video) && !trackById(info, pick.audio) ? (bestAudio(info)?.id ?? '') : pick.audio;
  return { video, audio };
}

/** The audio of a pick as sent to the daemon: none with a muxed video. */
export function pickedAudio(info: Pick<MediaInfo, 'tracks'>, pick: TrackPick): string {
  return needsAudio(info, pick.video) ? pick.audio : '';
}

/**
 * The size a pick will have, from the tracks' declared bandwidth and the duration (bytes;
 * -1 when either is unknown). Only an estimate: declared bandwidths are peaks or averages.
 */
export function estimatedBytes(info: Pick<MediaInfo, 'tracks' | 'durationMs'>, pick: TrackPick): number {
  if (!(info.durationMs > 0)) return -1;
  const video = trackById(info, pick.video);
  const audio = trackById(info, pickedAudio(info, pick));
  const bits = (video?.bandwidth ?? 0) + (audio?.bandwidth ?? 0);
  if (!(bits > 0) || (video && !(video.bandwidth > 0))) return -1;
  return Math.round((bits * info.durationMs) / 8000);
}

/**
 * The size of a pick (SITE-MEDIA-SPEC §5.3): the sum of the chosen tracks' sizes when every
 * one has one ('exact'; when any is an estimate, 'approx' for a web page's, whose site
 * estimated it, and 'estimate' for a manifest's, which downexd measured from a few segments
 * of a track without a bandwidth), else the bandwidth × duration 'estimate', else unknown
 * (bytes -1).
 */
export interface PickSize {
  readonly bytes: number;
  readonly kind: 'exact' | 'approx' | 'estimate' | 'unknown';
}

export function pickSize(info: Pick<MediaInfo, 'tracks' | 'durationMs'> & Partial<Pick<MediaInfo, 'origin'>>, pick: TrackPick): PickSize {
  const chosen = [trackById(info, pick.video), trackById(info, pickedAudio(info, pick))].filter(
    (t): t is MediaTrack => t !== undefined,
  );
  if (chosen.length > 0 && chosen.every((t) => t.size > 0)) {
    const approx = chosen.some((t) => t.sizeApprox);
    return {
      bytes: chosen.reduce((sum, t) => sum + t.size, 0),
      kind: !approx ? 'exact' : info.origin === 'site' ? 'approx' : 'estimate',
    };
  }
  const estimate = estimatedBytes(info, pick);
  return estimate > 0 ? { bytes: estimate, kind: 'estimate' } : { bytes: -1, kind: 'unknown' };
}

/** "412 MB", "~412 MB" (the site's estimate), "about 412 MB" (downexd's: from the bitrate or measured); "" when unknown. */
export function pickSizeText(size: PickSize): string {
  switch (size.kind) {
    case 'exact':
      return formatBytes(size.bytes);
    case 'approx':
      return `~${formatBytes(size.bytes)}`;
    case 'estimate':
      return `about ${formatBytes(size.bytes)}`;
    case 'unknown':
      return '';
  }
}

/**
 * Labels of the tracks a media download uses, for its details: the video's with its picture
 * ("4K (2160p) · AV1 · 860 MB · 3840×1634").
 */
export function chosenTracks(info: MediaInfo): { video: string; audio: string } {
  const site = isSite(info);
  const video = trackById(info, info.selectedVideo);
  const audio = trackById(info, info.selectedAudio);
  return {
    video: video ? [videoTrackLabel(video, site), videoPictureText(video, site)].filter(Boolean).join(' · ') : '',
    audio: audio ? audioTrackLabel(audio, site) : video?.kind === 'muxed' ? 'In the video track' : '',
  };
}

/**
 * The chosen quality of a web page's video in short: "4K (2160p) AV1 + AAC", "1080p (Full HD)
 * 60fps H.264 + AAC" (a muxed quality names its own codecs); "" when no video track is chosen.
 */
export function siteQualityText(info: MediaInfo): string {
  const video = trackById(info, info.selectedVideo);
  if (!video) return '';
  const audio = video.kind === 'muxed' ? undefined : trackById(info, info.selectedAudio);
  const codecs = [codecNames(video.codecs), audio ? codecNames(audio.codecs) || 'audio' : ''].filter(Boolean).join(' + ');
  return [qualityName(video), codecs].filter(Boolean).join(' ');
}

/** The short class of a quality: "1080p (Full HD) 60fps" → "1080p", "4K (2160p)" → "4K". */
function shortClass(t: MediaTrack): string {
  const name = qualityName(t);
  const cut = name.indexOf(' (');
  return (cut >= 0 ? name.slice(0, cut) : name).trim();
}

/**
 * The quality of a stream in plain words, for headers and the complete dialog (DESIGN-SYSTEM.md
 * §4.8): "1080p · H.264 · AAC 128 kbit/s". It joins the short class, the video codec, and the
 * audio codec with its bitrate; a muxed track's second codec stands for the audio (its bitrate
 * is the whole track's, so none is shown). Audio only: "AAC 128 kbit/s". `pick` defaults to the
 * stream's selection. "" when nothing is known.
 */
export function codecSummary(info: Pick<MediaInfo, 'tracks' | 'selectedVideo' | 'selectedAudio'>, pick?: TrackPick): string {
  const chosen = pick ?? { video: info.selectedVideo, audio: info.selectedAudio };
  const video = trackById(info, chosen.video);
  const parts: string[] = [];
  let audioText = '';
  if (video && isVideoTrack(video)) {
    const cls = shortClass(video);
    if (cls) parts.push(cls);
    const codecs = video.codecs
      .split(',')
      .map((c) => c.trim())
      .filter((c) => c !== '');
    if (codecs[0]) parts.push(codecName(codecs[0]));
    if (video.kind === 'muxed' && codecs[1]) audioText = codecName(codecs[1]);
  }
  const audio = video?.kind === 'muxed' ? undefined : trackById(info, chosen.audio);
  if (audio && audio.kind === 'audio') {
    const name = audio.codecs ? codecName(audio.codecs.split(',')[0] ?? '') : '';
    const rate = formatBitrate(audio.bandwidth);
    audioText = [name || (rate ? 'Audio' : ''), rate].filter(Boolean).join(' ');
  }
  if (audioText) parts.push(audioText);
  return parts.join(' · ');
}

// ---- refusals and errors (MEDIA-SPEC §6.3, SITE-MEDIA-SPEC §3.2, §5.5) ----------------

/** Why the daemon refuses a stream (MEDIA-SPEC §2, §6.3). */
export type MediaRefusal = 'encrypted' | 'live';

/**
 * Recognises the daemon's refusal of a stream: invalid_argument naming the stable code
 * "encrypted" (also DRM) or "live" in its message. Other failures return null.
 */
export function mediaRefusal(err: unknown): MediaRefusal | null {
  const e = toApiError(err);
  if (e.code !== 'invalid_argument') return null;
  const m = e.message.toLowerCase();
  if (/\b(encrypted|drm)\b/.test(m)) return 'encrypted';
  if (/\blive\b/.test(m)) return 'live';
  return null;
}

export const REFUSALS: Record<MediaRefusal, string> = {
  encrypted: 'This stream is encrypted or DRM-protected. Downex does not download protected content.',
  live: 'This is a live stream. Downex downloads only complete (on-demand) streams.',
};

/**
 * The daemon's codes for video pages, with the phrase its message carries after the code
 * ("site_error: yt-dlp could not read this page: <reason>"; internal/sitemedia/errors.go).
 */
const SITE_PHRASES: Record<string, string> = {
  site_unsupported: 'no video found on this page',
  site_unavailable: 'this video is not available',
  auth_required: 'the site asks to sign in',
  site_error: 'yt-dlp could not read this page',
  site_resolving: 'still getting the video info',
  tools_installing: 'downloading yt-dlp and Deno',
  tools_unavailable: 'yt-dlp and Deno are not set up',
  site_media_off: 'video downloads from web pages are turned off (Settings → Video sites)',
};

/** What every UI says about a video page's code (SITE-MEDIA-SPEC §5.5). */
export const SITE_TEXTS = {
  site_unsupported: 'Downex found no video on this page.',
  site_unavailable:
    'This video is not available (private, removed, blocked in your country, or the site asks to try again later).',
  auth_required: 'The site wants you to sign in (age check, members-only or bot check).',
  live: 'This is a live stream. Downex downloads only finished videos.',
  encrypted: 'This video is DRM-protected. Downex does not download protected content.',
  media_unsupported: 'No MP4 video on this page.',
  playlist: 'Open a single video of this playlist.',
  media_invalid: 'The chosen quality is no longer offered. Add the video again.',
  site_error: 'yt-dlp could not read this page',
  tools_unavailable: 'Downex could not set up yt-dlp/Deno',
  site_media_off: 'Video downloads from web pages are turned off (Settings → Video sites).',
  site_resolving: 'Getting video info…',
  tools_installing: 'Downloading yt-dlp and Deno',
  outdated: 'Update Downex to download videos.',
} as const;

/** A daemon message split into its stable code ("site_error") and the rest; code "" without one. */
export function splitCode(message: string): { code: string; rest: string } {
  const m = /^([a-z][a-z_]{0,63}):\s*(.*)$/s.exec(message.trim());
  return m ? { code: m[1] ?? '', rest: m[2] ?? '' } : { code: '', rest: message.trim() };
}

/** The reason after a code's phrase ("site_error: yt-dlp could not read this page: HTTP 500" → "HTTP 500"). */
function reasonOf(code: string, rest: string): string {
  const phrase = SITE_PHRASES[code];
  let reason = rest.trim();
  if (phrase && reason.toLowerCase().startsWith(phrase.toLowerCase())) reason = reason.slice(phrase.length);
  return reason.replace(/^[\s:;,.-]+/, '').trim();
}

/** A reason ending its sentence: "HTTP 500" → "HTTP 500."; "" stays "". */
function closed(text: string): string {
  const t = text.trim();
  if (!t) return '';
  return /[.!?…]['")\]]*$/.test(t) ? t : `${t}.`;
}

/**
 * A reason as a sentence of its own: "private video" → "Private video."; a name such as
 * "yt-dlp" or "deno.exe" keeps its spelling.
 */
function sentence(text: string): string {
  const t = closed(text);
  return /^[a-z0-9]+[-.]/.test(t) ? t : t.charAt(0).toUpperCase() + t.slice(1);
}

/** What a probe or add of a web page answered while it should be asked for again (SITE-MEDIA-SPEC §3.3). */
export type SiteRetry =
  | { readonly kind: 'tools_installing'; /** 0..1, NaN when the message has none. */ readonly fraction: number }
  | { readonly kind: 'site_resolving' };

/** Recognises tools_installing ("… (34%)") and site_resolving; null for anything else. */
export function siteRetry(err: unknown): SiteRetry | null {
  const e = toApiError(err);
  if (e.code !== 'failed_precondition') return null;
  const { code, rest } = splitCode(e.message);
  if (code === 'site_resolving') return { kind: 'site_resolving' };
  if (code !== 'tools_installing') return null;
  const pct = /\((\d{1,3})%\)/.exec(rest);
  return { kind: 'tools_installing', fraction: pct ? Math.min(1, Number(pct[1]) / 100) : Number.NaN };
}

/** A probe or add of a web page is asked for again at most this often (SITE-MEDIA-SPEC §3.3). */
export const SITE_RETRY_MS = 2000;
/** …and for at most this long: an install takes at most 15 minutes, an extraction 90 s. */
export const SITE_RETRY_LIMIT_MS = 16 * 60_000;

export interface SiteRetryOptions {
  /** Stops the retries: no call is made again, and retrySite rejects with canceled (a success still counts). */
  readonly signal?: AbortSignal;
  /** Hears each answer that asks for another try. */
  readonly onwait?: (retry: SiteRetry) => void;
  readonly now?: () => number;
}

function stopped(): ApiError {
  return new ApiError('canceled', 'stopped');
}

function pause(ms: number, signal: AbortSignal | undefined): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) {
      reject(stopped());
      return;
    }
    const onAbort = () => {
      clearTimeout(timer);
      reject(stopped());
    };
    const timer = setTimeout(() => {
      signal?.removeEventListener('abort', onAbort);
      resolve();
    }, ms);
    signal?.addEventListener('abort', onAbort, { once: true });
  });
}

/**
 * Calls `call` until downexd answers something else than tools_installing or
 * site_resolving (the install or extraction goes on in downexd meanwhile, and the next call
 * joins it). A call starts at most every SITE_RETRY_MS: one that came back only at
 * downexd's wait budget is made again at once, one answered at once after the rest of the
 * interval.
 */
export async function retrySite<T>(call: () => Promise<T>, opts: SiteRetryOptions = {}): Promise<T> {
  const now = opts.now ?? Date.now;
  const started = now();
  for (;;) {
    if (opts.signal?.aborted) throw stopped();
    const at = now();
    let retry: SiteRetry | null;
    try {
      // An answer that arrives after a stop still counts: an add it made is not lost.
      return await call();
    } catch (err) {
      if (opts.signal?.aborted) throw stopped();
      retry = siteRetry(err);
      if (!retry || now() - started >= SITE_RETRY_LIMIT_MS) throw err;
    }
    opts.onwait?.(retry);
    const wait = at + SITE_RETRY_MS - now();
    if (wait > 0) await pause(wait, opts.signal);
  }
}

/** The daemon's code of a failed call ("tools_unavailable"), "" without one. */
export function siteErrorCode(err: unknown): string {
  return splitCode(toApiError(err).message).code;
}

/**
 * What to tell the user about a failed probe or add of a web page's video (SITE-MEDIA-SPEC
 * §5.5): the text of the daemon's code, with its reason where it helps.
 */
export function siteErrorText(err: unknown): string {
  const e = toApiError(err);
  // A downexd older than site media has no tools RPCs and refuses a page as a manifest.
  if (e.code === 'unimplemented') return SITE_TEXTS.outdated;
  const { code, rest } = splitCode(e.message);
  const reason = reasonOf(code, rest);
  switch (code) {
    case 'site_unsupported':
    case 'site_unavailable':
    case 'auth_required':
      return [SITE_TEXTS[code], sentence(reason)].filter(Boolean).join(' ');
    case 'live':
    case 'encrypted':
    case 'media_invalid':
    case 'site_media_off':
      return SITE_TEXTS[code];
    case 'media_unsupported':
      return /playlist|multi[_-]?video/i.test(rest) ? SITE_TEXTS.playlist : SITE_TEXTS.media_unsupported;
    case 'site_error':
    case 'tools_unavailable':
      return reason ? `${SITE_TEXTS[code]}: ${closed(reason)}` : `${SITE_TEXTS[code]}.`;
    case 'site_resolving':
      return SITE_TEXTS.site_resolving;
    case 'tools_installing': {
      const r = siteRetry(e);
      const pct = r?.kind === 'tools_installing' && Number.isFinite(r.fraction) ? ` (${formatPercent(r.fraction)})` : '';
      return `${SITE_TEXTS.tools_installing}${pct}…`;
    }
    default:
      return e.message;
  }
}

/** What to tell the user about a failed probe or media add: of a manifest, or (origin 'site') of a web page. */
export function mediaErrorText(err: unknown, origin: MediaOrigin = 'manifest'): string {
  if (origin === 'site') return siteErrorText(err);
  const refusal = mediaRefusal(err);
  return refusal ? REFUSALS[refusal] : toApiError(err).message;
}

/** The failure needs the tools set up again: the message offers Settings → Video sites. */
export function needsToolsSettings(err: unknown): boolean {
  const code = siteErrorCode(err);
  return code === 'tools_unavailable' || code === 'site_media_off';
}

/** Hints for a failed download of a web page's video, by its error code (shown under the daemon's message). */
const SITE_HINTS: Record<string, string> = {
  encrypted: SITE_TEXTS.encrypted,
  live: SITE_TEXTS.live,
  media_unsupported: SITE_TEXTS.media_unsupported,
  media_invalid: SITE_TEXTS.media_invalid,
  site_unsupported: SITE_TEXTS.site_unsupported,
  site_unavailable: SITE_TEXTS.site_unavailable,
  auth_required: SITE_TEXTS.auth_required,
  site_error: 'yt-dlp could not read this page. Retrying may help later, once yt-dlp is updated.',
  tools_unavailable: 'Downex could not set up yt-dlp/Deno. Settings → Video sites shows why.',
  site_media_off: SITE_TEXTS.site_media_off,
};

/** The hint of a web page's failed download ("" for codes it shares with files, such as network). */
export function siteErrorHint(code: string): string {
  return SITE_HINTS[code] ?? '';
}

/**
 * A failure that trying again cannot help with: Downex never downloads protected or live
 * streams, and a page without an MP4 video (or without the chosen quality) stays so.
 */
export function cannotRetry(code: string): boolean {
  return (
    code === 'encrypted' ||
    code === 'live' ||
    code === 'media_unsupported' ||
    code === 'media_invalid' ||
    code === 'site_unsupported'
  );
}

/** An address whose path names an HLS playlist (.m3u8) or a DASH manifest (.mpd). */
export function looksLikeManifest(url: string): boolean {
  try {
    const u = new URL(url.trim());
    if (u.protocol !== 'http:' && u.protocol !== 'https:') return false;
    return /\.(m3u8|mpd)$/i.test(u.pathname);
  } catch {
    return false;
  }
}
