// Types shared by the background, the generic video tracker, the injected overlay UI and the
// popup (VIDEO-DETECT-SPEC §3.6). Frozen: the lanes that build on them never change them.
// Imports are types only (the type-only cycle with messages.ts is fine).

import type { MediaEntry } from './media';
import type { VideoTaskState } from './messages';
import type { AddedResult, VideoInfoResult } from './protocol';
import type { SiteVideoKind } from './site-video';

/** Version of the tracker ↔ overlay UI handshake (`globalThis[Symbol.for('downex.video')]`). */
export const UI_PROTOCOL = 1;
/** The overlay UI script the background injects on demand (WXT unlisted `entrypoints/video-ui.ts`). */
export const VIDEO_UI_SCRIPT = '/video-ui.js';

/** What the tracker reports of one `<video>` element: public element state only, never page text. */
export interface FrameVideoMeta {
  /** Tracker-local, 1..2^31-1. */
  id: number;
  /** `currentSrc` when http(s) (no fragment, ≤ 32 KiB), else ''. */
  src: string;
  /** `currentSrc` is a `blob:` URL (Media Source Extensions). */
  mse: boolean;
  /** Rendered size in CSS px, rounded. */
  width: number;
  height: number;
  /** Intrinsic size; 0 unknown. */
  videoWidth: number;
  videoHeight: number;
  /** -1 unknown, -2 live (`Infinity`). */
  durationMs: number;
  /** `mediaKeys` is set, or `encrypted`/`waitingforkey` was seen. */
  drm: boolean;
  /** At least 50 % inside the viewport. */
  visible: boolean;
  /** A background loop: muted, looping, without controls, under 30 s. */
  decorative: boolean;
  /** Gets a button apart from the site rule (VIDEO-DETECT-SPEC §2.5). */
  meaningful: boolean;
}

/** `f<seq>` a file item, `s<seq>` a stream item, `e<id>` an element file. */
export type OfferKey = string;
/** The form of every OfferKey. */
export const OFFER_KEY_RE = /^[fse][0-9]{1,12}$/;

/** A direct MP4 file of the frame (or of the tab, for the top frame), for the page's panel. */
export interface FileOffer {
  key: OfferKey;
  /** Host and last path segment: the full URL is never sent to the page's UI. */
  display: string;
  size: number;
  sizeKnown: boolean;
  filename: string;
  frame: 'this' | 'other';
  /** The clicked element plays this file. */
  matched: boolean;
  /** De-prioritised: small, short or an ad (VIDEO-DETECT-SPEC §2.4). */
  short: boolean;
  /** From the matched element, else 0 / -1. */
  width: number;
  height: number;
  durationMs: number;
  add?: VideoTaskState<AddedResult>;
}

/** An HLS/DASH manifest of the frame (or of the tab, for the top frame). */
export interface StreamOffer {
  key: OfferKey;
  format: 'hls' | 'dash';
  display: string;
  frame: 'this' | 'other';
  matched: boolean;
  probe?: VideoTaskState<VideoInfoResult>;
  add?: VideoTaskState<AddedResult>;
}

/** The frame's page video resolved with yt-dlp: a curated site's video or the frame's address. */
export interface SiteOffer {
  /** `<site>:<id>`, or `page:<address>`. */
  key: string;
  /** Display name, or the frame's host for a generic target. */
  site: string;
  kind: SiteVideoKind | 'page';
  pageUrl: string;
  /** A curated site (VIDEO-DETECT-SPEC §3.8) or YouTube. */
  curated: boolean;
  probe?: VideoTaskState<VideoInfoResult>;
  add?: VideoTaskState<AddedResult>;
}

/** Answer to `downex:frame-offers`: everything a frame's panel lists, from the background's memory. */
export interface FrameOffers {
  /** The tab's title. */
  title: string;
  topHost: string;
  files: FileOffer[];
  streams: StreamOffer[];
  site?: SiteOffer;
  /** What the relay serves (`streams`: stream qualities; `video`: yt-dlp). */
  features: { streams: boolean; video: boolean };
  /** The clicked element is protected / live. */
  drm: boolean;
  live: boolean;
}

/** A file of the tab for the popup (an extension page, never a web page): it carries the URL. */
export interface FileEntry extends Omit<FileOffer, 'frame'> {
  url: string;
  frame: 'top' | 'embed';
}

/** A stream of the tab for the popup. MediaEntry (lib/media.ts) keeps its shape. */
export interface StreamEntry extends MediaEntry {
  key: OfferKey;
  frame: 'top' | 'embed';
  probe?: VideoTaskState<VideoInfoResult>;
}
