// The rows of a frame's video panel (VIDEO-DETECT-SPEC §2.6), shared by the overlay panel and
// the popup: the site's qualities (yt-dlp), the qualities of the frame's streams merged into one
// list, the direct files ("This video" first), and the folded groups "More sources (n)" and
// "Other videos on this page (n)". Pure: the sources' states come in, rows go out.

import type { ClipRange } from './clip';
import type { FileOffer, OfferKey, StreamOffer } from './offer-types';
import type { ProtocolError, VideoInfoResult } from './protocol';
import { fileLabel, formatLabel, gettingInfoText, OFFER_TEXTS, streamFailureText, streamRefusal } from './ui-format';
import {
  codecsLabel,
  defaultAudio,
  defaultPick,
  highestWithSound,
  isVideoTrack,
  pickedAudio,
  type PickerInfo,
  qualityKey,
  qualityRows,
  type TrackPick,
  trackById,
  withVideo,
} from './video-picker';

export type OfferSource = 'site' | 'stream' | 'file' | 'played';

/** What is known of one stream's MP4 qualities (`downex:stream-probe`). */
export type StreamProbe =
  | { phase: 'idle' }
  | { phase: 'probing' }
  | { phase: 'listed'; info: VideoInfoResult }
  | { phase: 'failed'; error: ProtocolError };

/** One row of the panel: a quality of the site or of a stream, a stream still unlisted, or a file. */
export interface OfferRow {
  /** Stable across refreshes: `site:<track>`, `stream:<key>:<track>`, `stream:<key>`, `file:<key>`. */
  id: string;
  source: OfferSource;
  /** The source tag: the site's name, "Stream" or "File". */
  tag: string;
  /** "4K (2160p)", "1080p (Full HD) 60fps", "This video", "HLS stream". */
  quality: string;
  /** "H.264", "AV1", "720p (HD) · MP4", a stream's address or its state. */
  detail: string;
  /** "" when unknown. */
  size: string;
  /** Everything a screen reader says for the row. */
  label: string;
  /** Tooltip: a quality's picture ("3840×1634"), a file's host and name, a stream's address. */
  title?: string;
  /** The file or stream; none for the site's rows. */
  key?: OfferKey;
  /** The tracks a quality row sends ("" = Downex's default pick). */
  pick: TrackPick;
  /** What activating the row does: hand it to Downex, or (a stream not listed) get its qualities. */
  action: 'add' | 'probe';
  /** The clicked element plays it. */
  matched: boolean;
  /** Not usable now: the stream's qualities are being got. */
  disabled: boolean;
  /** The highest quality with sound of the site's and the played qualities, the default (§5.2). */
  recommended?: boolean;
}

export interface OfferRowsInput {
  files: readonly FileOffer[];
  streams: readonly StreamOffer[];
  /** The site's qualities once listed, and the audio chosen for them. */
  site?: { name: string; info: PickerInfo; audio: string } | undefined;
  /** The stream probes by key; a missing key is `idle`. */
  probes: ReadonlyMap<OfferKey, StreamProbe>;
  /** The audio chosen per stream (default: its default audio). */
  streamAudio?: ReadonlyMap<OfferKey, string>;
  /**
   * The part of the video the Part row sends with a site or stream quality (undefined: the whole
   * video): those rows' sizes are then the part's estimates (clipSize, "~").
   */
  clip?: ClipRange | undefined;
  /**
   * The clicked video's files as its tab played them, once Downex matched them (SITE-MEDIA-SPEC
   * §2.9), and the audio chosen for them.
   */
  played?: { key: OfferKey; info: VideoInfoResult; audio: string } | undefined;
  /**
   * The site's rows come before the Played ones: the frame's own address is the video, and its
   * video servers answer more than browsers (VIDEO-DETECT-SPEC §2.6, amendment 2026-11-18).
   */
  siteFirst?: boolean;
}

export interface OfferRows {
  /** Shown at once. */
  main: OfferRow[];
  /** "More sources (n)": the network rows once the site lists its qualities. */
  more: OfferRow[];
  /** "Other videos on this page (n)": other frames' items, then short files. */
  other: OfferRow[];
  /** The row focused first and sent by a Download without a choice; undefined without rows. */
  defaultId: string | undefined;
  /** The site source lists at least one video quality. */
  siteListed: boolean;
}

/** An estimate as the panel words it: "about 412 MB" (bandwidth × duration). */
function aboutSize(size: string): string {
  return size.startsWith('~') ? `about ${size.slice(1)}` : size;
}

function joinLabel(...parts: string[]): string {
  return parts.filter((p) => p !== '').join(' · ');
}

/** A stream's own streams first: matched ones, then in the order given (newest first). */
export function streamOrder<T extends Pick<StreamOffer, 'matched' | 'frame'>>(streams: readonly T[]): T[] {
  const rank = (s: T) => (s.frame === 'this' ? 0 : 2) + (s.matched ? 0 : 1);
  return streams.map((s, i) => ({ s, i })).sort((a, b) => rank(a.s) - rank(b.s) || a.i - b.i).map((x) => x.s);
}

/** Files as listed: matched first, then the largest (unknown sizes last), short ones after the rest. */
export function fileOrder<T extends Pick<FileOffer, 'matched' | 'short' | 'size' | 'sizeKnown'>>(files: readonly T[]): T[] {
  const size = (f: T) => (f.sizeKnown && f.size > 0 ? f.size : -1);
  return files
    .map((f, i) => ({ f, i }))
    .sort(
      (a, b) =>
        Number(b.f.matched) - Number(a.f.matched) || Number(a.f.short) - Number(b.f.short) || size(b.f) - size(a.f) || a.i - b.i,
    )
    .map((x) => x.f);
}

/**
 * A failed stream's line on its row: the reason's lead ("Downex is not available") or its first
 * sentence; the whole reason is the row's label and tooltip, and the panel's status says it
 * (OfferPanel.view), so that it is not shown twice one block apart.
 */
export function shortFailure(text: string): string {
  const colon = text.indexOf(': ');
  if (colon > 0) return text.slice(0, colon);
  const stop = text.search(/[.!?…] /);
  return stop > 0 ? text.slice(0, stop + 1) : text;
}

/** Whether Downex refused the stream for good: it is not listed (§3.7). */
export function streamRefused(p: StreamProbe | undefined): boolean {
  if (p?.phase === 'failed') return streamRefusal(p.error) !== undefined;
  return p?.phase === 'listed' && !p.info.tracks.some(isVideoTrack);
}

/**
 * A quality row's small text: its codecs ("H.264", "AV1", "H.264 + AAC"). Every quality is
 * saved as one MP4, so it says "MP4" only when the codecs are unknown.
 */
function codecDetail(codecs: string): string {
  return codecs || 'MP4';
}

function siteRows(site: NonNullable<OfferRowsInput['site']>, clip: ClipRange | undefined): OfferRow[] {
  // Qualities the site lists without a picture or a size read the same: one row says them all
  // (measured 2026-11-18: three "Video and audio, MP4" rows for one reel).
  const seen = new Set<string>();
  const rows = qualityRows(site.info, site.audio, clip).filter((r) => {
    const t = trackById(site.info, r.id);
    if (!t || t.height > 0 || t.width > 0 || r.size !== '') return true;
    const same = `${r.quality}|${r.codecs}`;
    if (seen.has(same)) return false;
    seen.add(same);
    return true;
  });
  return rows.map((r) => {
    const pick = withVideo(site.info, { video: '', audio: site.audio }, r.id);
    const detail = codecDetail(r.codecs);
    const row: OfferRow = {
      id: `site:${r.id}`,
      source: 'site',
      tag: site.name,
      quality: r.quality,
      detail,
      size: r.size,
      label: joinLabel(r.quality, detail, r.size, site.name),
      pick: { video: r.id, audio: pickedAudio(site.info, pick) },
      action: 'add',
      matched: false,
      disabled: false,
    };
    if (r.picture) row.title = r.picture;
    return row;
  });
}

/**
 * Whether an answer is downexd's for a bare media playlist (a level, audio or subtitle
 * playlist the player loaded after its master): one muxed track of unknown picture, or
 * without a bandwidth (a media playlist declares none; downexd may have measured its
 * picture and size from its segments).
 */
export function bareAnswer(info: Pick<VideoInfoResult, 'tracks'>): boolean {
  const t = info.tracks[0];
  return info.tracks.length === 1 && t !== undefined && t.kind === 'muxed' && (t.height === 0 || t.bandwidth === 0);
}

/**
 * The rows of a group of streams, in their order: the listed qualities merged (each stream's list
 * is one per quality class already, lib/quality.ts; across streams a quality another stream
 * already lists with the same encoding (qualityKey: kind, picture, frame rate, codec family, HDR)
 * is dropped, the first stream's kept, so a master playlist and the variant the player loaded give
 * one list, while a different video with qualities of the same classes keeps its own rows), and a
 * row per stream not listed yet. Bare media playlists (bareAnswer) come last, and only while no
 * stream of the group lists qualities; their rows are added to `bare`.
 */
function streamRows(streams: readonly StreamOffer[], input: OfferRowsInput, bare: Set<string>): OfferRow[] {
  const probeOf = (s: StreamOffer): StreamProbe => input.probes.get(s.key) ?? { phase: 'idle' };
  const onlyBare = !streams.some((s) => {
    const p = probeOf(s);
    return p.phase === 'listed' && !streamRefused(p) && !bareAnswer(p.info);
  });
  const rows: OfferRow[] = [];
  const later: OfferRow[] = [];
  const seen = new Set<string>();
  for (const s of streams) {
    const probe = probeOf(s);
    if (streamRefused(probe)) continue;
    if (probe.phase === 'listed') {
      const isBare = bareAnswer(probe.info);
      if (isBare && !onlyBare) continue;
      const info = probe.info;
      const audio = input.streamAudio?.get(s.key) ?? defaultAudio(info);
      for (const r of qualityRows(info, audio, input.clip)) {
        const track = trackById(info, r.id);
        if (!track) continue;
        const dedupe = qualityKey(track);
        if (seen.has(dedupe)) continue;
        seen.add(dedupe);
        const pick = withVideo(info, { video: '', audio }, r.id);
        const detail = codecDetail(r.codecs);
        const size = aboutSize(r.size);
        const row: OfferRow = {
          id: `stream:${s.key}:${r.id}`,
          source: 'stream',
          tag: OFFER_TEXTS.stream,
          quality: r.quality,
          detail,
          size,
          label: joinLabel(r.quality, detail, size, OFFER_TEXTS.stream),
          title: joinLabel(r.picture, s.display),
          key: s.key,
          pick: { video: r.id, audio: pickedAudio(info, pick) },
          action: 'add',
          matched: s.matched,
          disabled: false,
        };
        if (isBare) {
          bare.add(row.id);
          later.push(row);
        } else rows.push(row);
      }
      continue;
    }
    // Not listed yet: one row for the stream. Without the relay's `streams` it hands the stream
    // over with Downex's default pick; after another failure it asks again.
    const kind = `${formatLabel(s.format)} stream`;
    const failed = probe.phase === 'failed' ? streamFailureText(probe.error) : '';
    const outdated = failed === OFFER_TEXTS.streamsOutdated;
    const detail = probe.phase === 'probing' ? gettingInfoText(0) : failed ? shortFailure(failed) : s.display;
    rows.push({
      id: `stream:${s.key}`,
      source: 'stream',
      tag: OFFER_TEXTS.stream,
      quality: kind,
      detail,
      size: '',
      label: joinLabel(kind, failed || detail, OFFER_TEXTS.stream),
      title: joinLabel(failed, s.display),
      key: s.key,
      pick: { video: '', audio: '' },
      action: outdated ? 'add' : 'probe',
      matched: s.matched,
      disabled: probe.phase === 'probing',
    });
  }
  return [...rows, ...later];
}

/** The Played source's qualities ("Playing"): what the tab played, one row per quality class. */
function playedRows(played: NonNullable<OfferRowsInput['played']>): OfferRow[] {
  const { info, key } = played;
  return qualityRows(info, played.audio, undefined).map((r) => {
    const pick = withVideo(info, { video: '', audio: played.audio }, r.id);
    // A video file saved with its sound names both, as a muxed file does ("VP9 + AAC").
    const sound = trackById(info, pickedAudio(info, pick));
    const joined = trackById(info, r.id)?.kind === 'video' && sound ? codecsLabel(sound) : '';
    const detail = codecDetail([r.codecs, joined].filter((c) => c !== '').join(' + '));
    const row: OfferRow = {
      id: `played:${r.id}`,
      source: 'played',
      tag: OFFER_TEXTS.played,
      quality: r.quality,
      detail,
      size: r.size,
      label: joinLabel(r.quality, detail, r.size, OFFER_TEXTS.played),
      key,
      pick: { video: r.id, audio: pickedAudio(info, pick) },
      action: 'add',
      matched: true,
      disabled: false,
    };
    if (r.picture) row.title = r.picture;
    return row;
  });
}

function fileRow(f: FileOffer): OfferRow {
  const l = fileLabel(f);
  return {
    id: `file:${f.key}`,
    source: 'file',
    tag: OFFER_TEXTS.file,
    quality: l.quality,
    detail: l.detail,
    size: l.size,
    label: joinLabel(l.label, OFFER_TEXTS.file),
    title: l.title,
    key: f.key,
    pick: { video: '', audio: '' },
    action: 'add',
    matched: f.matched,
    disabled: false,
  };
}

/**
 * The panel's rows in the order of VIDEO-DETECT-SPEC §2.6:
 * 1. the site's qualities, once listed;
 * 2. the frame's stream qualities (matched streams first, then newest), merged;
 * 3. the files the element plays ("This video");
 * 4. the frame's other files, largest first;
 * 5. "Other videos on this page (n)": other frames' items, then short files.
 * Once the site lists qualities, 2-5 fold into "More sources (n)", and a file whose exact size is
 * that of a site track is dropped as the same video.
 */
export function buildOfferRows(input: OfferRowsInput): OfferRows {
  const listed = input.site !== undefined && input.site.info.tracks.some(isVideoTrack);
  const site = listed && input.site ? siteRows(input.site, input.clip) : [];
  const siteSizes = new Set(listed && input.site ? input.site.info.tracks.filter((t) => t.size > 0).map((t) => t.size) : []);
  const keep = (f: FileOffer) => !(f.sizeKnown && siteSizes.has(f.size));

  const ordered = streamOrder(input.streams);
  const bare = new Set<string>();
  const ownStreams = streamRows(
    ordered.filter((s) => s.frame === 'this'),
    input,
    bare,
  );
  const otherStreams = streamRows(
    ordered.filter((s) => s.frame === 'other'),
    input,
    bare,
  );
  const files = fileOrder(input.files.filter(keep));
  // A short clip the element plays (a pre-roll ad) is listed as "This video", never the default
  // while there is anything else.
  const short = new Set(files.filter((f) => f.short).map((f) => `file:${f.key}`));
  const matched = files.filter((f) => f.frame === 'this' && f.matched).map(fileRow);
  const ownFiles = files.filter((f) => f.frame === 'this' && !f.matched && !f.short).map(fileRow);
  const otherFiles = files.filter((f) => f.frame === 'other' && !f.short).map(fileRow);
  const shortFiles = files.filter((f) => f.short && !(f.frame === 'this' && f.matched)).map(fileRow);

  const network = [...ownStreams, ...matched, ...ownFiles];
  const folded = [...otherStreams, ...otherFiles, ...shortFiles];
  const played = input.played && input.played.info.tracks.some(isVideoTrack) ? playedRows(input.played) : [];
  let rows: OfferRows;
  if (played.length > 0 && listed && input.siteFirst) {
    // The frame's own video page: the site's qualities first, what played under "More sources".
    rows = { main: site, more: [...played, ...network, ...folded], other: [], defaultId: undefined, siteListed: true };
  } else if (played.length > 0) {
    // What the tab played first (§2.9), then the site's qualities of the clicked post.
    rows = listed
      ? { main: [...played, ...site], more: [...network, ...folded], other: [], defaultId: undefined, siteListed: true }
      : { main: played, more: network, other: folded, defaultId: undefined, siteListed: false };
  } else {
    rows = listed
      ? { main: site, more: [...network, ...folded], other: [], defaultId: undefined, siteListed: true }
      : { main: network, more: [], other: folded, defaultId: undefined, siteListed: false };
  }
  // The highest quality with sound of the site's and the played qualities is recommended and the
  // default (VIDEO-DETECT-SPEC §5.2, amended 2026-11-19), when there are several to choose from.
  const best = recommendedRow(rows.main, input);
  if (best) {
    best.recommended = true;
    rows.defaultId = best.id;
    return rows;
  }
  if (played.length > 0 && !(listed && input.siteFirst) && input.played) {
    const first = rows.main.find((r) => r.id === `played:${defaultPick(input.played?.info).video}`) ?? played[0];
    rows.defaultId = first?.id;
    return rows;
  }

  // The default: the matched file (when shown at once and not a short clip), else the site's
  // default, else the first stream quality, else the first row shown; a bare media playlist or a
  // short clip only when nothing else is.
  const firstMatched = rows.main.find((r) => r.source === 'file' && r.matched && !short.has(r.id));
  const siteDefault =
    listed && input.site ? rows.main.find((r) => r.id === `site:${defaultPick(input.site?.info).video}`) : undefined;
  const firstStream = rows.main.find((r) => r.source === 'stream' && r.action === 'add' && r.pick.video !== '' && !bare.has(r.id));
  const usable = (r: OfferRow) => !r.disabled && !bare.has(r.id) && !short.has(r.id);
  const fallback =
    rows.main.find(usable) ?? rows.other.find(usable) ?? rows.main.find((r) => !r.disabled) ?? rows.other.find((r) => !r.disabled);
  rows.defaultId = (firstMatched ?? siteDefault ?? firstStream ?? fallback)?.id;
  return rows;
}

/** Of the site's and the played rows of `main`, the highest with sound; none with fewer than two. */
function recommendedRow(main: OfferRow[], input: OfferRowsInput): OfferRow | undefined {
  const infoOf = (r: OfferRow) => (r.source === 'site' ? input.site?.info : r.source === 'played' ? input.played?.info : undefined);
  const scored = main.flatMap((row) => {
    const info = infoOf(row);
    const t = info ? trackById(info, row.pick.video) : undefined;
    return t && isVideoTrack(t) ? [{ row, kind: t.kind, width: t.width, height: t.height, sound: row.pick.audio !== '' }] : [];
  });
  if (scored.length < 2) return undefined;
  return highestWithSound(scored, (s) => s.sound)?.row;
}

/** Every row of a panel, in the order shown when every group is open. */
export function allRows(rows: OfferRows): OfferRow[] {
  return [...rows.main, ...rows.more, ...rows.other];
}
