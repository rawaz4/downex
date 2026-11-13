// Text shown by the popup, the options page and the video panels (BROWSER-SPEC §4,
// VIDEO-DETECT-SPEC §5.6). Pure: no DOM or browser APIs.

import type { HandoffRecord, HandoffSource } from './history';
import type { MediaFormat } from './media';
import {
  type AddedResult,
  type ConnectionStatus,
  daemonCode,
  daemonDetail,
  isRelayDeadline,
  OUTDATED_DOWNEX_TEXT,
  type Outcome,
  type ProtocolError,
  type StatusResult,
} from './protocol';
import {
  formatHiddenSites,
  hostListError,
  MAX_HIDDEN_SITES,
  mergeExcludedHosts,
  parseHiddenSites,
  siteHidden,
  type VideoOptions,
} from './options-form';
import { qualityClass, standardName } from './quality';
import { type CaptureRules, siteExclusion, siteHost } from './rules';
import { formatBytes } from './video-picker';

export type Tone = 'ok' | 'warn' | 'error';

export interface Label {
  text: string;
  tone: Tone;
}

export function connectionLabel(s: ConnectionStatus): Label {
  switch (s.state) {
    case 'connected':
      return { text: s.daemonVersion ? `Connected (Downex ${s.daemonVersion})` : 'Connected', tone: 'ok' };
    case 'connecting':
      return { text: 'Connecting…', tone: 'warn' };
    default:
      if (s.incompatibleProtocol !== undefined) {
        return {
          text: `Incompatible Downex: ${s.lastError ?? `relay protocol ${s.incompatibleProtocol}`}. Update Downex or this extension.`,
          tone: 'error',
        };
      }
      if (s.lastError && isHostMissing(s.lastError)) {
        return { text: `Not available: ${s.lastError} ${HOST_MISSING_HINT}`, tone: 'error' };
      }
      if (s.lastError && isHostForbidden(s.lastError)) {
        return { text: `Not available: ${s.lastError} ${HOST_FORBIDDEN_HINT}`, tone: 'error' };
      }
      return { text: s.lastError ? `Not available: ${s.lastError}` : 'Not available', tone: 'error' };
  }
}

/**
 * The popup's "Check again" button is named in it. A browser without Downex at all (the
 * extension came from a store) gets the same answer as a broken registration: install it.
 */
export const HOST_MISSING_HINT =
  'Downex is not registered with this browser: install or reinstall Downex, or run "downex-nmhost register", then press Check again.';

/**
 * Matches Chrome's and Firefox's texts for a host whose registration they cannot find. Chrome reads
 * the registration on every connect, so registering is enough: the browser needs no restart.
 */
export function isHostMissing(message: string): boolean {
  return /native messaging host not found|no such native application/i.test(message);
}

/**
 * Downex is registered, but not for this extension's ID: an older Downex that does not know the
 * ID a store gave the extension, or a copy loaded from elsewhere. A newer Downex registers it.
 */
export const HOST_FORBIDDEN_HINT = 'Downex does not accept this copy of the extension: install the latest Downex, then press Check again.';

/** Matches Chrome's text for a host whose allowed_origins do not name this extension. */
export function isHostForbidden(message: string): boolean {
  return /native messaging host is forbidden/i.test(message);
}

export function daemonLabel(o: Outcome<StatusResult>): Label {
  if (!o.ok && (isHostMissing(o.error.message) || isHostForbidden(o.error.message))) {
    return { text: 'Daemon status unavailable: the browser cannot reach Downex yet', tone: 'error' };
  }
  if (!o.ok) return { text: `Daemon status unavailable: ${o.error.message}`, tone: 'error' };
  const s = o.value;
  if (!s.running) return { text: 'Daemon is not running', tone: 'warn' };
  return { text: `Daemon running: ${s.active} active, ${s.queued} queued`, tone: 'ok' };
}

const SOURCES: Readonly<Record<HandoffSource, string>> = {
  capture: 'Captured',
  menu: 'Context menu',
  'page-links': 'Page links',
  media: 'Media',
  video: 'Video',
  'video-file': 'Video file',
};

export function sourceLabel(source: string): string {
  return Object.hasOwn(SOURCES, source) ? SOURCES[source as HandoffSource] : source;
}

/** File name, else the page title, else the URL's last path segment, else its host, else the URL itself. */
export function recordName(rec: HandoffRecord): string {
  if (rec.filename) return rec.filename;
  if (rec.title) return rec.title;
  if (rec.source === 'media') return mediaName(rec.url);
  if (rec.source === 'video') return rec.url;
  try {
    const u = new URL(rec.url);
    const seg = u.pathname.split('/').pop() ?? '';
    if (seg) {
      try {
        return decodeURIComponent(seg);
      } catch {
        return seg;
      }
    }
    return u.host || rec.url;
  } catch {
    return rec.url;
  }
}

/**
 * Downex accepted a browser hand-off. The daemon may hold it until the user confirms folder
 * and name (DESKTOP-SPEC §1); the `added` answer is the same either way.
 */
export const WAITING_FOR_CONFIRMATION = 'Waiting for confirmation in Downex';

export function recordResult(rec: HandoffRecord): string {
  if (rec.ok) {
    // `added.awaiting` false: Downex saves it at once (every source since VIDEO-DETECT-SPEC §3.5).
    if (rec.awaiting === false) return DOWNLOADING_IN_DOWNEX;
    return rec.duplicate ? `${WAITING_FOR_CONFIRMATION}; the browser kept its own copy too` : WAITING_FOR_CONFIRMATION;
  }
  if (!rec.error) return 'Failed';
  if (rec.source === 'video') return `Failed: ${videoFailureText(rec.error)}`;
  if (rec.source === 'video-file') return `Failed: ${fileFailureText(rec.error)}`;
  return `Failed: ${rec.source === 'media' ? mediaFailureText(rec.error) : rec.error.message}`;
}

/** Kind of file a hand-off carries, for its colored icon. */
export type FileKind = 'video' | 'audio' | 'archive' | 'document' | 'image' | 'program' | 'other';

const KIND_EXTENSIONS: ReadonlyArray<readonly [FileKind, readonly string[]]> = [
  ['video', ['mp4', 'mkv', 'webm', 'mov', 'avi', 'wmv', 'flv', 'm4v', 'ts', 'm3u8', 'mpd', 'mpg', 'mpeg', '3gp']],
  ['audio', ['mp3', 'flac', 'wav', 'ogg', 'opus', 'm4a', 'aac', 'wma', 'aiff']],
  ['archive', ['zip', 'rar', '7z', 'tar', 'gz', 'tgz', 'bz2', 'xz', 'zst', 'iso', 'img', 'dmg', 'cab']],
  ['document', ['pdf', 'doc', 'docx', 'xls', 'xlsx', 'ppt', 'pptx', 'odt', 'ods', 'odp', 'txt', 'rtf', 'csv', 'epub', 'md']],
  ['image', ['jpg', 'jpeg', 'png', 'gif', 'webp', 'svg', 'bmp', 'tif', 'tiff', 'avif', 'heic', 'ico']],
  ['program', ['exe', 'msi', 'msix', 'appx', 'apk', 'deb', 'rpm', 'appimage', 'pkg', 'jar', 'bat', 'sh']],
];

/** From the file name's extension; media and video hand-offs are video. */
export function fileKind(rec: HandoffRecord): FileKind {
  if (rec.source === 'media' || rec.source === 'video' || rec.source === 'video-file') return 'video';
  let name = rec.filename ?? '';
  if (!name) {
    try {
      name = new URL(rec.url).pathname.split('/').pop() ?? '';
    } catch {
      name = '';
    }
  }
  return fileKindOfName(name);
}

/** The kind of a file by its name's extension ("other" without a known one). */
export function fileKindOfName(name: string): FileKind {
  const dot = name.lastIndexOf('.');
  if (dot < 0) return 'other';
  const ext = name.slice(dot + 1).toLowerCase();
  for (const [kind, exts] of KIND_EXTENSIONS) if (exts.includes(ext)) return kind;
  return 'other';
}

/** Tooltip of a result line. */
export function recordResultDetail(rec: HandoffRecord): string | undefined {
  if (!rec.ok) return undefined;
  if ((rec.source === 'video' || rec.source === 'video-file') && rec.awaiting === false) return 'Downex saves it as one MP4 file.';
  if (rec.awaiting === false) return undefined;
  return 'Downex asks where to save downloads from the browser unless that is turned off in its settings.';
}

/**
 * The popup's "Send downloads to Downex" switch for the active tab's page: on while the page's host
 * is not excluded. It is the inverse of the stored rule (the host in the excluded hosts), which it
 * adds or removes as "Disable on this site" did; a wildcard entry of the options page locks it off.
 */
export interface SiteSwitch {
  /** Host the switch adds to or removes from the excluded hosts; undefined: not a web page. */
  host?: string;
  checked: boolean;
  disabled: boolean;
  hint: string;
}

/** The send switch's hint while downloads of the site go to Downex. */
export const SEND_HINT = 'Hold Alt while clicking to keep one in the browser';

function quoteList(items: readonly string[]): string {
  const shown = items.slice(0, 3).join(', ');
  return items.length > 3 ? `${shown} and ${items.length - 3} more` : shown;
}

export function siteSendSwitch(rules: CaptureRules, pageUrl: string | undefined): SiteSwitch {
  const host = siteHost(pageUrl);
  if (host === undefined) {
    return { checked: true, disabled: true, hint: 'Only web pages (http and https) can be excluded.' };
  }
  const { excluded, patterns } = siteExclusion(rules, host);
  if (patterns.length > 0) {
    return { host, checked: false, disabled: true, hint: `Excluded by ${quoteList(patterns)}; change it on the options page.` };
  }
  if (excluded) return { host, checked: false, disabled: false, hint: `Downloads from or started on ${host} stay in the browser.` };
  return {
    host,
    checked: true,
    disabled: false,
    hint: rules.enabled ? SEND_HINT : 'Capture browser downloads is off, so downloads stay in the browser.',
  };
}

// ---------------------------------------------------------------------------------------
// Streams in "Videos on this page" (MEDIA-SPEC §6.1, VIDEO-DETECT-SPEC §5.3)

export function formatLabel(format: MediaFormat): string {
  return format === 'hls' ? 'HLS' : 'DASH';
}

function decodeSegment(seg: string): string {
  try {
    return decodeURIComponent(seg);
  } catch {
    return seg;
  }
}

/** Host and the last two path segments of a manifest URL ("cdn.example.com/…/hls/master.m3u8"). */
export function mediaName(url: string): string {
  let u: URL;
  try {
    u = new URL(url);
  } catch {
    return url;
  }
  const segs = u.pathname.split('/').filter((x) => x !== '').map(decodeSegment);
  if (segs.length === 0) return u.host || url;
  const tail = segs.slice(-2).join('/');
  return segs.length > 2 ? `${u.host}/…/${tail}` : `${u.host}/${tail}`;
}

export type MediaRefusal = 'encrypted' | 'live';

/**
 * Why Downex refused a stream: the relay answers `rejected` with a message naming `encrypted`
 * or `live` (BROWSER-SPEC §2); a relay that uses those as codes is understood too.
 */
export function mediaRefusal(e: ProtocolError | undefined): MediaRefusal | undefined {
  if (!e) return undefined;
  if (e.code === 'encrypted') return 'encrypted';
  if (e.code === 'live') return 'live';
  if (e.code !== 'rejected') return undefined;
  const lead = /^\s*(encrypted|live)\b/i.exec(e.message)?.[1]?.toLowerCase();
  if (lead === 'encrypted' || lead === 'live') return lead;
  if (/\b(encrypted|encryption|drm)\b/i.test(e.message)) return 'encrypted';
  if (/\blive\b/i.test(e.message)) return 'live';
  return undefined;
}

export const ENCRYPTED_STREAM_TEXT = 'This stream is encrypted (DRM). Downex downloads only unencrypted streams.';
export const LIVE_STREAM_TEXT = 'This is a live stream. Downex downloads only complete (on-demand) streams.';
/** The relay's deadline passed while Downex read the manifests (a slow or throttling server). */
export const MEDIA_TOO_SLOW_TEXT =
  'Downex took too long to read the stream. The stream server may be slow; check Downex before trying again.';

/** A media hand-off failure in words a user can act on. */
export function mediaFailureText(e: ProtocolError): string {
  switch (mediaRefusal(e)) {
    case 'encrypted':
      return ENCRYPTED_STREAM_TEXT;
    case 'live':
      return LIVE_STREAM_TEXT;
  }
  switch (e.code) {
    case 'daemon_unavailable':
      return isRelayDeadline(e) ? MEDIA_TOO_SLOW_TEXT : `Downex is not running: ${e.message}`;
    case 'disconnected':
    case 'incompatible':
      return `Downex is not available: ${e.message}`;
    case 'timeout':
      return 'Downex did not answer in time. Check its list before trying again.';
    default:
      return e.message;
  }
}

// ---------------------------------------------------------------------------------------
// Videos of web pages (SITE-MEDIA-SPEC §5.1, §5.5)

/** Downex added a video and saves it at once (no confirmation asked). */
export const DOWNLOADING_IN_DOWNEX = 'Downloading in Downex';
/** Downex added a video that waits for the user's confirmation there. */
export const CONFIRM_IN_DOWNEX = 'Confirm in Downex';

/**
 * How an accepted hand-off's line is marked, the same on every surface (the popup's rows and
 * history, the in-page panels): Downex saves it at once (ok), or it waits for the user in Downex
 * (info: "Confirm in Downex", and "Sent to Downex" of a relay that does not say).
 */
export function addedTone(text: string): 'ok' | 'info' {
  return text === DOWNLOADING_IN_DOWNEX ? 'ok' : 'info';
}

/** Lowercase names that start a sentence as they are ("yt-dlp could not read this page"). */
const LOWERCASE_NAMES = ['yt-dlp', 'downexd', 'downex-nmhost'];

/**
 * A result or reason as a sentence, starting with a capital (some daemon reasons start lowercase,
 * "the server answered …"), except a program whose name is lowercase.
 */
export function initialCapital(s: string): string {
  if (LOWERCASE_NAMES.some((n) => s.startsWith(n) && !/[\p{L}\p{N}]/u.test(s.charAt(n.length)))) return s;
  return s.charAt(0).toLocaleUpperCase('en') + s.slice(1);
}

/** Texts of the daemon's refusals (§5.5). */
export const VIDEO_TEXTS = {
  site_unsupported: 'Downex found no video on this page.',
  site_unavailable:
    'This video is not available (private, removed, blocked in your country, or the site asks to try again later).',
  auth_required: 'The site wants you to sign in (age check, members-only or bot check).',
  live: 'This is a live stream. Downex downloads only finished videos.',
  encrypted: 'This video is DRM-protected. Downex does not download protected content.',
  media_unsupported: 'No MP4 video on this page.',
  playlist: 'Open a single video of this playlist.',
  media_invalid: 'The chosen quality is no longer offered. Add the video again.',
  site_media_off: 'Video downloads from web pages are turned off (Settings → Video sites).',
  unsupported_page: 'Open a video page to download its video.',
  outdated_downex: OUTDATED_DOWNEX_TEXT,
  too_slow: 'Downex took too long to answer. Try again.',
  timeout: 'Downex did not answer in time. Try again.',
  still_installing: 'Downex is still setting up yt-dlp and Deno. Try again in a few minutes.',
} as const;

/** The phrase the daemon puts before a reason (internal/sitemedia sentinels, §3.2). */
const SITE_ERROR_LEAD = 'yt-dlp could not read this page';
const TOOLS_UNAVAILABLE_LEAD = 'yt-dlp and Deno are not set up';

function sentence(s: string): string {
  const t = s.trim();
  if (!t) return t;
  return /[.!?…]$/.test(t) ? t : `${t}.`;
}

/** What follows `lead` (and ": ") in a daemon message; the whole detail when it does not start so. */
function reasonAfter(detail: string, lead: string): string {
  if (detail === lead) return '';
  return detail.startsWith(`${lead}: `) ? detail.slice(lead.length + 2) : detail;
}

/** Refusals that are final for the page: no "Try again" (auth_required has its own action). */
const FINAL_CODES: ReadonlySet<string> = new Set([
  'encrypted',
  'live',
  'media_unsupported',
  'media_invalid',
  'site_unsupported',
  'site_unavailable',
  'auth_required',
  'site_media_off',
  'outdated_downex',
  'unsupported_page',
]);

/** Downex is still downloading yt-dlp/Deno or resolving the page: ask again in 2 s (§2.2). */
export function isVideoRetry(e: ProtocolError): boolean {
  const code = daemonCode(e);
  return code === 'tools_installing' || code === 'site_resolving';
}

/** Whether the panel offers "Try again" after this failure. */
export function canRetryVideo(e: ProtocolError): boolean {
  return !FINAL_CODES.has(daemonCode(e)) && !isVideoRetry(e);
}

/** "Preparing video downloads: downloading yt-dlp and Deno (34%)" (tools_installing). */
export function toolsInstallingText(e: ProtocolError): string {
  const detail = daemonDetail(e).trim();
  return `Preparing video downloads: ${detail || 'downloading yt-dlp and Deno'}`;
}

/** "Getting video info… 12 s". */
export function gettingInfoText(elapsedMs: number): string {
  const s = elapsedText(elapsedMs);
  return s ? `Getting video info… ${s}` : 'Getting video info…';
}

/**
 * "12 s" ("" under a second): the time spent getting a video's info, shown next to a status
 * line that stays the same, so that a screen reader does not announce every second.
 */
export function elapsedText(elapsedMs: number): string {
  const s = Math.max(0, Math.floor(elapsedMs / 1000));
  return s > 0 ? `${s} s` : '';
}

/** A failed video probe or add in words a user can act on (§5.5). */
export function videoFailureText(e: ProtocolError): string {
  const code = daemonCode(e);
  const detail = daemonDetail(e);
  switch (code) {
    case 'site_unsupported':
    case 'site_unavailable':
    case 'auth_required':
    case 'live':
    case 'encrypted':
    case 'media_invalid':
    case 'site_media_off':
    case 'unsupported_page':
    case 'outdated_downex':
      return VIDEO_TEXTS[code];
    case 'media_unsupported':
      return /\bplaylist\b/i.test(detail) ? VIDEO_TEXTS.playlist : VIDEO_TEXTS.media_unsupported;
    case 'site_error': {
      const reason = reasonAfter(detail, SITE_ERROR_LEAD);
      return sentence(reason ? `${SITE_ERROR_LEAD}: ${reason}` : SITE_ERROR_LEAD);
    }
    case 'tools_unavailable': {
      const reason = reasonAfter(detail, TOOLS_UNAVAILABLE_LEAD);
      return sentence(`Downex could not set up yt-dlp/Deno${reason ? `: ${reason}` : ''}`);
    }
    case 'tools_installing':
      return toolsInstallingText(e);
    case 'site_resolving':
      return gettingInfoText(0);
  }
  switch (e.code) {
    case 'daemon_unavailable':
      return isRelayDeadline(e) ? VIDEO_TEXTS.too_slow : `Downex is not running: ${e.message}`;
    case 'disconnected':
    case 'incompatible':
      return `Downex is not available: ${e.message}`;
    case 'timeout':
      return VIDEO_TEXTS.timeout;
    default:
      return e.message;
  }
}

/** The line after a video was handed to Downex. */
export function videoAddedText(added: AddedResult): string {
  return added.awaiting ? CONFIRM_IN_DOWNEX : DOWNLOADING_IN_DOWNEX;
}

// ---------------------------------------------------------------------------------------
// Videos on every site (VIDEO-DETECT-SPEC §5.2-§5.6)

/** Downex accepted a hand-off but did not say whether it waits for confirmation (an older relay). */
export const SENT_TO_DOWNEX = 'Sent to Downex';

/** Texts of the overlay panel and the popup's "Videos on this page" (§5.6). */
export const OFFER_TEXTS = {
  thisVideo: 'This video',
  lookForMore: 'Look for more qualities',
  foundNothing: 'Downex found no video it can save here.',
  streamUnsupported: 'No MP4 video in this stream.',
  streamsOutdated: 'Update Downex to choose a stream quality.',
  expired: 'This video is no longer listed. Play it again, then download it.',
  addressExpired: 'The address may have expired. Play the video again, then download it.',
  moreInToolbar: 'More in the Downex toolbar menu',
  hideHere: "Don't show on this site",
  untitled: 'Video',
  file: 'File',
  stream: 'Stream',
  mp4File: 'MP4 file',
} as const;

export function moreSourcesText(n: number): string {
  return `More sources (${n})`;
}

export function otherVideosText(n: number): string {
  return `Other videos on this page (${n})`;
}

export function shortClipsText(n: number): string {
  return `Short clips (${n})`;
}

export function hideOnText(host: string): string {
  return `Don't show on ${host}`;
}

/**
 * The line after a file or stream was handed to Downex (`added.awaiting`, VIDEO-DETECT-SPEC
 * §3.5): a relay that does not say is answered "Sent to Downex".
 */
export function offerAddedText(added: AddedResult): string {
  if (added.awaiting === true) return CONFIRM_IN_DOWNEX;
  if (added.awaiting === false) return DOWNLOADING_IN_DOWNEX;
  return SENT_TO_DOWNEX;
}

/** Daemon codes that mean a direct file's address no longer works (§5.6). */
const ADDRESS_CODES: ReadonlySet<string> = new Set(['http_status', 'network']);

/** A failed direct-file `add` (`downex:offer-add`, `downex:add-file`) in words a user can act on. */
export function fileFailureText(e: ProtocolError): string {
  if (e.code === 'expired') return OFFER_TEXTS.expired;
  const text = mediaFailureText(e);
  if (!ADDRESS_CODES.has(daemonCode(e))) return text;
  return `${sentence(daemonDetail(e) || text)} ${OFFER_TEXTS.addressExpired}`;
}

/** Why a stream is not offered: Downex refused it for good (§3.7). */
export type StreamRefusal = 'encrypted' | 'live' | 'no_mp4';

/** The refusal of a stream probe (`encrypted`, `live`, `media_unsupported`/`media_invalid`), if any. */
export function streamRefusal(e: ProtocolError): StreamRefusal | undefined {
  const code = daemonCode(e);
  if (code === 'encrypted' || code === 'live') return code;
  if (code === 'media_unsupported' || code === 'media_invalid') return 'no_mp4';
  return mediaRefusal(e);
}

/** The popup's mark of a refused stream (§3.7): "Protected", "Live" or "No MP4 video". */
export const REFUSAL_LABELS: Readonly<Record<StreamRefusal, string>> = {
  encrypted: 'Protected',
  live: 'Live',
  no_mp4: 'No MP4 video',
};

/** A failed stream probe or add in words a user can act on (§5.6). */
export function streamFailureText(e: ProtocolError): string {
  if (e.code === 'expired') return OFFER_TEXTS.expired;
  const code = daemonCode(e);
  if (code === 'outdated_downex') return OFFER_TEXTS.streamsOutdated;
  if (code === 'media_unsupported') return OFFER_TEXTS.streamUnsupported;
  if (code === 'encrypted' || code === 'live') return VIDEO_TEXTS[code];
  if (isVideoRetry(e) || code === 'media_invalid') return videoFailureText(e);
  return mediaFailureText(e);
}

/**
 * What a direct-file row shows (§5.2): "This video · 720p (HD) · MP4 · 48.2 MB", "MP4 file ·
 * 48.2 MB". The quality is the standard name of the element's picture (lib/quality.ts): a
 * 1280×544 film reads "720p (HD)", never "544p".
 */
export interface FileLabel {
  quality: string;
  detail: string;
  /** "" when unknown. */
  size: string;
  label: string;
  /** The host and last path segment of the file. */
  title: string;
}

export interface FileLabelInput {
  display: string;
  size: number;
  sizeKnown: boolean;
  /** The element's picture; 0 (or less) unknown. */
  width: number;
  height: number;
  matched: boolean;
}

export function fileLabel(f: FileLabelInput): FileLabel {
  const safe = (n: number) => (Number.isSafeInteger(n) && n > 0 ? n : 0);
  const height = standardName(qualityClass(safe(f.width), safe(f.height)), 0, false);
  const quality = f.matched ? OFFER_TEXTS.thisVideo : height || OFFER_TEXTS.mp4File;
  const detail = f.matched ? [height, 'MP4'].filter((p) => p !== '').join(' · ') : height ? 'MP4' : '';
  const size = f.sizeKnown && f.size > 0 ? formatBytes(f.size) : '';
  return { quality, detail, size, label: [quality, detail, size].filter((p) => p !== '').join(' · '), title: f.display };
}

/** The popup's "Download buttons on videos" switch of the active tab's host (§5.3). */
export interface SiteButtonToggle {
  /** The tab's host; undefined: not a web page. */
  host?: string;
  checked: boolean;
  disabled: boolean;
  hint: string;
}

/** The buttons switch's hint while the buttons show on the host. */
export const BUTTONS_HINT = 'Shown over the player on this site';

/**
 * The switch for the buttons on the tab's host: on when they show, locked when a wildcard entry
 * of the options page hides the host (or the buttons are turned off everywhere).
 */
export function siteButtonToggle(options: VideoOptions, host: string | undefined): SiteButtonToggle {
  if (!host) {
    return { checked: false, disabled: true, hint: 'Only web pages (http and https) have Download buttons.' };
  }
  if (!options.videoButton) {
    return { host, checked: false, disabled: true, hint: 'Download buttons are turned off on the options page.' };
  }
  const { hidden, patterns } = siteHidden(options, host);
  if (patterns.length > 0) {
    return { host, checked: false, disabled: true, hint: `Hidden by ${quoteList(patterns)}; change it on the options page.` };
  }
  return {
    host,
    checked: !hidden,
    disabled: false,
    hint: hidden ? 'No button over the players of this site. "This page" still lists its videos.' : BUTTONS_HINT,
  };
}

/** The options page's "Don't show the button on these sites" field after the user edited it. */
export type HiddenSitesResult = { ok: true; sites: string[]; note?: string } | { ok: false; error: string };

/**
 * The hidden-sites field after the stored list changed from `before` to `after` (a panel's
 * "Don't show on <host>", the toolbar menu's switch, another options page): the new list, or,
 * while the user has unsaved edits, those edits with the entries added and removed elsewhere.
 */
export function hiddenSitesField(text: string, edited: boolean, before: readonly string[], after: readonly string[]): string {
  return edited ? mergeExcludedHosts(text, before, after) : formatHiddenSites(after);
}

export function hiddenSitesResult(text: string): HiddenSitesResult {
  const form = parseHiddenSites(text);
  if (form.invalid.length > 0) return { ok: false, error: hostListError(form.invalid) };
  if (form.dropped > 0) {
    return { ok: true, sites: form.sites, note: `Only the first ${MAX_HIDDEN_SITES} sites are kept; ${form.dropped} more were left out.` };
  }
  return { ok: true, sites: form.sites };
}
