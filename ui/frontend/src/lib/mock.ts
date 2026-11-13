// Simulated daemon for `pnpm dev`, `pnpm preview` without a Wails host, and tests.
// It follows DAEMON-SPEC §3–§6 closely enough to exercise every UI path: a FIFO queue
// bounded by max_concurrent_downloads, probing, segmented transfers with dynamic
// splitting, per-download/global speed limits, failures with stable error codes and
// event batches at the daemon's 4-per-second cadence.
//
// At start (without `count`) the list is the Studio scene of DESIGN-SYSTEM.md §7.1: three
// downloads running on 16, 8 and 4 connections (one a YouTube video), one to confirm, one
// paused, seven queued, one failed (HTTP 503), a completed HLS stream and more completed ones.
// With `count` (`?mock=<n>`) it generates that many rows. Demo videos are MP4 only and audio is
// MP3 or M4A. Each connection runs a little faster or slower than the next, so their speeds
// differ and add up to the download's. freeSpace answers 214 GiB less what completes in this
// run; the window controls only record what the page asked.
//
// URL markers trigger scenarios: "404" (HTTP error while probing), "changed" (the
// remote file changes mid-download → validator_changed), "flaky" (network error at
// half way), "unknown-size" (no Content-Length, single connection), "norange"
// (no range support, single connection); "size=<bytes>" in the query fixes the size.
//
// Browser hand-offs (DESKTOP-SPEC §1): an Add with source "browser" waits for
// confirmation while settings.confirmBrowserDownloads is on, and asks the window to show
// its dialog (onConfirmRequest). With the timer running, the simulated browser hands a
// download over a few seconds after start and then every `handoffEveryMs`, every second
// one a stream.
//
// Streaming media (MEDIA-SPEC §6): manifests are addresses whose path ends in .m3u8 (HLS)
// or .mpd (DASH). Markers: "drm" or "encrypted" (refused: encrypted), "live" (refused:
// live), "404" (the manifest cannot be fetched), "audio" or "podcast" (audio tracks only),
// "muxed" (HLS variants of MPEG-TS with their sound, converted: every stream is saved as
// MP4, VIDEO-DETECT-SPEC §3.9), "init-protected" (the job
// fails with error code "encrypted" when it checks the first initialization segment).
// A media download counts fragments; its size is known once it is assembled (which takes
// a moment after the last fragment: the progress window shows "Assembling…").
//
// Video pages (SITE-MEDIA-SPEC): any other http(s) address probed or added with origin
// "site". yt-dlp and Deno are missing at first: the first page (or Install now) downloads
// them (tools_installing with the progress, tools events), then each page is "resolved"
// for a moment (site_resolving) and cached for 15 minutes. The offer is YouTube-like: one MP4
// quality per class under its standard name (SITE-MEDIA-SPEC amendment 2026-10-23: H.264 up to
// 1080p, AV1 above; the muxed 360p is dropped for the video-only one) best first, with sizes,
// and MP4 audio. "film" or "movie" in the address makes the pictures 2.35:1 (3840×1634 is
// "4K (2160p)"). Markers: "drm" (encrypted), "live", "/playlist" (media_unsupported), "podcast" or
// "audio-only" (no MP4 video), "private" or "unavailable" (site_unavailable), "age" or
// "signin" (auth_required), "unsupported" (site_unsupported), "broken" (site_error),
// "dub" (several audio languages). A video downloads in 10 MiB parts and is merged. A layout
// fixture's page names its offer: "qualities=<1|3|6|8>" (FIXTURE_LADDERS), "audio=<1-3>",
// "title=<long|ku|ar|none>"; MockOptions.fixture sets up a dialog state (applyFixture).
//
// Progress windows (DESKTOP-SPEC §7): running downloads report one ConnectionInfo per
// worker slot. A slot connects, sends its request and receives its range; now and then
// one waits before retrying (detail "HTTP 503, retrying in 3 s", as downexd says it); slots without work are
// finished or idle. setDownloadOptions changes the speed limit at once and the
// connection count as the transfer goes on, and marks a download for the download complete
// dialog (DESKTOP-SPEC §8; completed downloads refuse options, as downexd does). Downloads
// count their running time without pauses (activeMs) from their first start (startedAt).
//
// Queues (M6-SPEC §1): every download belongs to one queue, the main queue (running) by
// default; a queue starts its queued downloads in its order while it runs, up to its own
// limit and the global one. A seeded "Night" queue is stopped. A running queue with a
// post-queue action that finishes (after completing something) counts down 60 s
// (post_action events); the simulated daemon cannot sleep or shut down anything, so it
// then reports the action as aborted. Schedules are stored but not run. Addresses with
// "private" or "auth-required" fail with auth_required unless a site login matches their
// host. Secrets only mark hasPassword; the update feed always offers a newer version.

import {
  ApiError,
  DEFAULT_SETTINGS,
  EMPTY_SCHEDULE,
  LIMITS,
  MAIN_QUEUE_ID,
  APP_FOLDERS,
  numberedName,
  THEME_CHOICES,
  isResumable,
  type AddMediaRequest,
  type AddRequest,
  type Api,
  type AppFolder,
  type ConfirmOptions,
  type ConnectionInfo,
  type ConnectionState,
  type DaemonEvent,
  type Download,
  type DownloadOptions,
  type EventBatch,
  type MediaInfo,
  type MediaTrack,
  type PostAction,
  type PostActionPending,
  type Queue,
  type QueueSpec,
  type RemoveOptions,
  type ResumeOptions,
  type Schedule,
  type Segment,
  type Settings,
  type StartupView,
  type Status,
  type ThemeChoice,
  type ToolInfo,
  type ToolState,
  type ToolsStatus,
  type Unsubscribe,
  type UpdateInfo,
} from './api';
import { categoryKind, classifyName, extensionOf, type FileKind } from './categories';
import { categoryFolder } from './confirm';
import { defaultPick, estimatedBytes, isSite, pickSize, pickedAudio, trackById, withVideo, type TrackPick } from './media';
import { onePerClass, qualityClass, standardName } from './quality';
import { checkDir, checkFilename, checkUrl, validateCategories } from './validate';

export interface MockOptions {
  /** PRNG seed; the same seed and options give the same simulation. */
  seed?: number;
  /** Number of downloads present at start (default 28: the Studio scene). */
  count?: number;
  /**
   * What the list holds at start: 'studio' (the default without `count`), a fixed scene like the
   * design's main window: three downloads running on 16, 8 and 4 connections (one a YouTube
   * video), one to confirm, one paused, seven queued, one failed with HTTP 503, an HLS stream
   * and fourteen more completed. 'random' (the default with `count`): `count` generated rows.
   */
  scene?: 'studio' | 'random';
  /** Bytes free on every simulated volume at start (default 214 GiB); completions use it up. */
  freeBytes?: number;
  /** Run the simulation on a timer (default true). Tests call tick() instead. */
  autoTick?: boolean;
  /** Simulation step and event batch interval (default 250 ms = 4 batches/s). */
  tickMs?: number;
  /** Artificial RPC latency in ms (default 0). */
  latencyMs?: number;
  /** Clock (default Date.now). */
  now?: () => number;
  /** Default download folder (default: a platform-looking Downloads path). */
  defaultDir?: string;
  /** Chance that a started download hits a network error part way (default 0.05). */
  failureRate?: number;
  /**
   * Interval of simulated browser hand-offs while the timer runs (default 90 s, the
   * first one 6 s after start; 0 = none).
   */
  handoffEveryMs?: number;
  /**
   * How often a receiving connection has to wait and retry, per second (default 0.006;
   * 0 = never). Its range receives nothing meanwhile.
   */
  retryRate?: number;
  /** How long a media download takes to assemble its fragments (default 1500 ms). */
  assembleMs?: number;
  /** What startup() reports (default: the main window, with Open with…, hibernation and wake timers). */
  view?: StartupView;
  /** yt-dlp and Deno are installed at start (default false: the first video page installs them). */
  toolsReady?: boolean;
  /** How long their simulated download takes (default 4000 ms). */
  toolsInstallMs?: number;
  /** Their download fails (tools_unavailable, "could not download yt-dlp (network)"). */
  toolsFail?: boolean;
  /** How long yt-dlp takes to read a page (default 1200 ms; 0 = at once). */
  resolveMs?: number;
  /** Called by openProgressWindow (a development page may open a preview window). */
  onOpenProgressWindow?: (id: number) => void;
  /** Called by window.close() (default: nothing). */
  onCloseWindow?: () => void;
  settings?: Partial<Settings>;
  /**
   * A layout fixture for scripts/measure-dialogs.mjs (`?fixture=` in the demo's address):
   * comma-separated tokens added to the Studio scene, see applyFixture. With one, the simulated
   * browser hands nothing over by itself, so the layout stays as it was set up.
   */
  fixture?: string;
}

/** What the page asked of its (simulated) window. */
export interface MockWindowState {
  title: string;
  size: { width: number; height: number };
  sizes: { width: number; height: number }[];
  closed: boolean;
  /** Downloads whose progress window was requested, in order. */
  opened: number[];
  /** How often the page minimised its window, and whether it is maximised. */
  minimised: number;
  maximised: boolean;
  /** The themes setTheme was given, and the folders openAppFolder showed, in order. */
  themes: ThemeChoice[];
  folders: AppFolder[];
}

export interface MockApi extends Api {
  /** Advances the simulation by `ms` (default: one tick) and emits one event batch. */
  tick(ms?: number): void;
  /**
   * The browser hands a download over (an Add with source "browser"); when it then
   * awaits confirmation, the window is asked to show its dialog.
   */
  browserHandoff(req?: Partial<AddRequest>): Download;
  /** The browser hands a stream over (an addMedia with source "browser"). */
  browserMediaHandoff(req?: Partial<AddMediaRequest>): Download;
  /** The browser hands a web page's video over (origin "site"; yt-dlp must be ready). */
  browserVideoHandoff(req?: Partial<AddMediaRequest>): Download;
  /** The file of a download is deleted on disk (fileExists answers false until it is downloaded again). */
  deleteFile(id: number): void;
  /** Stops the timer and drops subscribers. */
  dispose(): void;
  /** The simulated window (title, size, closed) and the progress windows asked for. */
  readonly windowState: MockWindowState;
}

type Mutable<T> = { -readonly [K in keyof T]: T[K] };
type MSegment = Mutable<Segment>;
type MDownload = Omit<Mutable<Download>, 'segments'> & { segments: MSegment[] };

/** One worker slot of a running download (what ConnectionInfo reports). */
interface Slot {
  readonly index: number;
  state: ConnectionState;
  /** Time left in the current connecting/requesting/retrying phase. */
  phaseMs: number;
  /** Why it retries (shown with the countdown). */
  reason: string;
  /** The range it works on; null when it has none. */
  seg: MSegment | null;
  /** Its last range, still reported after it finished. */
  last: { start: number; end: number; received: number };
  speedBps: number;
}
interface Sim {
  d: MDownload;
  /** What the simulated server can deliver, bytes/s. */
  baseSpeed: number;
  /** Random-walk factor applied to baseSpeed. */
  wobble: number;
  probeLeftMs: number;
  /** Byte offset at which the transfer fails (-1 = never). */
  failAt: number;
  failCode: string;
  failMessage: string;
  /** HTTP error at probe time. */
  probeError: string;
  /** Real size for downloads whose size the server does not announce. */
  hiddenSize: number;
  /** The remote file changed: resuming saved data fails until a restart. */
  remoteChanged: boolean;
  /** Fail with validator_changed once this many bytes have been received. */
  changeAt: number;
  /** URL-marker scenarios fire once; a restarted download then succeeds. */
  scenarioDone: boolean;
  noRange: boolean;
  /** The size a download awaiting confirmation has once it is confirmed (-1 = unknown). */
  pendingSize: number;
  /** A media download: the stream as probed, and the bytes its chosen tracks take. */
  media: { readonly probed: MediaInfo; totalBytes: number } | null;
  /** Worker slots of the current run (DESKTOP-SPEC §7). */
  slots: Slot[];
  /** A media download that has all its fragments: time left to assemble them (-1 = not yet). */
  assembleLeftMs: number;
  /** Downloaded again to replace its file: that file's size, freed at completion (-1 = no). */
  replaceBytes: number;
}

/** A simulated queue: its settings and the order of its downloads. */
interface MQueue {
  readonly id: number;
  name: string;
  running: boolean;
  maxConcurrent: number;
  speedLimit: number;
  schedule: Schedule;
  postAction: PostAction;
  readonly isMain: boolean;
  order: number[];
  /** A download completed since the queue last started (or counted down). */
  completedThisRun: boolean;
}

/** Whether a site login's pattern ("https://host/*", "*.example.com", "host") covers a host. */
function loginCovers(pattern: string, host: string): boolean {
  const p = pattern.trim().toLowerCase().replace(/^[a-z]+:\/\//, '').replace(/[/:].*$/, '');
  const h = host.toLowerCase();
  if (p.startsWith('*.')) return h === p.slice(2) || h.endsWith(p.slice(1));
  return p !== '' && p === h;
}

const KiB = 1024;
const MiB = 1024 * KiB;
const GiB = 1024 * MiB;
const MIN_SPLIT = 2 * MiB;
const MAX_SEGMENTS = 64;

const HOSTS = [
  'downloads.example.com',
  'mirror.example.org',
  'cdn.example.net',
  'files.example.com',
  'media.example.org',
];

// Demo names: every video is one MP4 and audio is MP3 or M4A (the owner's MP4-only rule; no
// .webm, .mkv, .mov, .flac or .ogg rows, DESIGN-SYSTEM.md §7.1).
const NAMES: Record<FileKind, readonly string[]> = {
  video: [
    'city-timelapse-4k.mp4',
    'conference-keynote.mp4',
    'drone-footage-coast.mp4',
    'tutorial-episode-#.mp4',
    'nature-documentary-part#.mp4',
  ],
  audio: ['podcast-episode-#.mp3', 'lecture-audio.m4a', 'field-recording-forest.m4a', 'ambient-loop-#.mp3'],
  archives: [
    'project-assets-#.zip',
    'dataset-sensors-2026.tar.gz',
    'source-snapshot.tar.xz',
    'photo-archive-#.7z',
    'disk-image-#.iso',
  ],
  documents: [
    'annual-report-2025.pdf',
    'user-guide.pdf',
    'research-paper-#.pdf',
    'budget-plan.xlsx',
    'meeting-notes-#.docx',
    'novel-draft.epub',
  ],
  programs: [
    'setup-x64-#.exe',
    'installer-#.msi',
    'toolkit_#_amd64.deb',
    'viewer-#.x86_64.rpm',
    'editor-#.AppImage',
  ],
  other: ['firmware-v#.bin', 'map-tiles-region-#.mbtiles', 'checksums-#.sha256', 'telemetry-#.dat'],
};

const SIZE_RANGE: Record<FileKind, readonly [number, number]> = {
  video: [150 * MiB, 4 * GiB],
  audio: [3 * MiB, 80 * MiB],
  archives: [10 * MiB, 3 * GiB],
  documents: [120 * KiB, 40 * MiB],
  programs: [2 * MiB, 600 * MiB],
  other: [200 * KiB, 120 * MiB],
};

/** One row of the Studio scene. */
interface SceneRow {
  readonly name: string;
  readonly host: string;
  /** Bytes (a stream's come from its tracks). */
  readonly size: number;
  readonly state: 'downloading' | 'awaiting_confirmation' | 'paused' | 'queued' | 'failed' | 'completed';
  /** Minutes before the start that it was added. */
  readonly ago: number;
  /** Share received (downloading, paused, failed). */
  readonly fraction?: number;
  /** Its own connection limit (the default otherwise). */
  readonly connections?: number;
  /** Bytes per second while it runs. */
  readonly speed?: number;
  readonly source?: 'ui' | 'browser' | 'cli';
  /** A web page's video (origin site). */
  readonly page?: string;
  readonly title?: string;
  /** An HLS manifest (a stream). */
  readonly manifest?: string;
}

const DAY_MIN = 24 * 60;

/**
 * The Studio scene (DESIGN-SYSTEM.md §7.1), newest first: what the design's main window shows.
 * Sidebar counts: Video 11, Audio 3, Archives 6, Documents 3, Programs 4, Other 1; states:
 * 3 downloading, 1 to confirm, 7 queued, 1 paused, 15 completed, 1 failed.
 */
const SCENE: readonly SceneRow[] = [
  { name: 'os-image-26.04-amd64.iso', host: 'releases.example.org', size: 5.9 * GiB, state: 'downloading', ago: 6, fraction: 0.67, connections: 16, speed: 11.2 * MiB, source: 'browser' },
  {
    name: 'conference-keynote.mp4',
    host: 'www.youtube.com',
    size: 0,
    state: 'downloading',
    ago: 11,
    fraction: 0.42,
    speed: 5.8 * MiB,
    source: 'browser',
    // a 38-minute talk offered up to 1440p: about 1.7 GiB in 1080p60 H.264
    page: 'https://www.youtube.com/watch?v=Kn000wdmX26',
    title: 'Conference keynote 2026: opening session',
  },
  { name: 'telemetry-183.dat', host: 'files.example.com', size: 412 * MiB, state: 'downloading', ago: 17, fraction: 0.81, connections: 4, speed: 0.91 * MiB },
  { name: 'budget-plan.xlsx', host: 'files.example.com', size: 1.2 * MiB, state: 'awaiting_confirmation', ago: 24, source: 'browser' },
  { name: 'city-timelapse-4k.mp4', host: 'media.example.org', size: 1.3 * GiB, state: 'paused', ago: DAY_MIN + 40, fraction: 0.18 },
  { name: 'setup-x64-312.exe', host: 'downloads.example.com', size: 543 * MiB, state: 'queued', ago: DAY_MIN + 95 },
  { name: 'annual-report-2025.pdf', host: 'files.example.com', size: 39.8 * MiB, state: 'queued', ago: 2 * DAY_MIN + 30, source: 'browser' },
  { name: 'photo-archive-277.7z', host: 'mirror.example.org', size: 2.5 * GiB, state: 'failed', ago: 2 * DAY_MIN + 210, fraction: 0.18 },
  { name: 'keynote-2026.mp4', host: 'media.example.org', size: 0, state: 'completed', ago: 3 * DAY_MIN + 60, manifest: 'https://media.example.org/talks/keynote-2026/master.m3u8', source: 'browser' },
  { name: 'podcast-episode-41.mp3', host: 'cdn.example.net', size: 74.8 * MiB, state: 'completed', ago: 3 * DAY_MIN + 300 },
  { name: 'drone-footage-coast.mp4', host: 'cdn.example.net', size: 2.1 * GiB, state: 'completed', ago: 4 * DAY_MIN + 90, source: 'browser' },
  { name: 'viewer-8.x86_64.rpm', host: 'downloads.example.com', size: 230 * MiB, state: 'completed', ago: 4 * DAY_MIN + 400, source: 'cli' },
  { name: 'novel-draft.epub', host: 'files.example.com', size: 14.3 * MiB, state: 'completed', ago: 5 * DAY_MIN + 120 },
  { name: 'project-assets-362.zip', host: 'mirror.example.org', size: 2.2 * GiB, state: 'completed', ago: 6 * DAY_MIN + 60 },
  { name: 'nature-documentary-part1.mp4', host: 'media.example.org', size: 1.6 * GiB, state: 'completed', ago: 7 * DAY_MIN + 30 },
  { name: 'ambient-loop-3.mp3', host: 'cdn.example.net', size: 8.4 * MiB, state: 'completed', ago: 7 * DAY_MIN + 500 },
  { name: 'source-snapshot.tar.xz', host: 'files.example.com', size: 412 * MiB, state: 'completed', ago: 8 * DAY_MIN + 200, source: 'cli' },
  { name: 'editor-5.AppImage', host: 'downloads.example.com', size: 118 * MiB, state: 'completed', ago: 9 * DAY_MIN + 45 },
  { name: 'tutorial-episode-9.mp4', host: 'media.example.org', size: 486 * MiB, state: 'completed', ago: 10 * DAY_MIN + 80 },
  { name: 'tutorial-episode-8.mp4', host: 'media.example.org', size: 471 * MiB, state: 'completed', ago: 11 * DAY_MIN + 80 },
  { name: 'nature-documentary-part3.mp4', host: 'media.example.org', size: 1.8 * GiB, state: 'completed', ago: 12 * DAY_MIN + 300 },
  { name: 'tutorial-episode-10.mp4', host: 'media.example.org', size: 502 * MiB, state: 'completed', ago: 13 * DAY_MIN + 60 },
  { name: 'project-assets-361.zip', host: 'mirror.example.org', size: 2.1 * GiB, state: 'completed', ago: 14 * DAY_MIN + 20 },
  // Queued #3 to #7: added earlier, waiting for their turn in the main queue.
  { name: 'tutorial-episode-12.mp4', host: 'media.example.org', size: 512 * MiB, state: 'queued', ago: 15 * DAY_MIN },
  { name: 'nature-documentary-part2.mp4', host: 'media.example.org', size: 1.7 * GiB, state: 'queued', ago: 15 * DAY_MIN + 300 },
  { name: 'dataset-sensors-2026.tar.gz', host: 'files.example.com', size: 860 * MiB, state: 'queued', ago: 16 * DAY_MIN },
  { name: 'installer-27.msi', host: 'downloads.example.com', size: 96 * MiB, state: 'queued', ago: 17 * DAY_MIN },
  { name: 'lecture-audio.m4a', host: 'cdn.example.net', size: 61 * MiB, state: 'queued', ago: 18 * DAY_MIN },
];

const SEED_FAILURES: readonly (readonly [string, string])[] = [
  ['validator_changed', 'The file on the server changed since the download started'],
  ['network', 'Connection reset by the server'],
  ['http_status', 'Server responded 403 Forbidden'],
  ['disk_full', 'Not enough free space on the destination disk'],
  ['tls', 'The server certificate is not valid for this host'],
  ['stalled', 'No data received for 60 seconds'],
];

/** Small deterministic PRNG (mulberry32). */
function prng(seed: number): () => number {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

function guessDefaultDir(): string {
  const ua = typeof navigator !== 'undefined' ? navigator.userAgent : '';
  return /Windows/i.test(ua) ? 'C:\\Users\\you\\Downloads' : '/home/you/Downloads';
}

/** Rough stand-in for the daemon's filename sanitiser (the daemon stays authoritative). */
function sanitizeFilename(name: string): string {
  const cleaned = Array.from(name, (ch) =>
    ch.charCodeAt(0) < 32 || '<>:"/\\|?*'.includes(ch) ? '_' : ch,
  )
    .join('')
    .replace(/[. ]+$/, '');
  return cleaned || 'download';
}

function nameFromUrl(url: string): string {
  try {
    const path = new URL(url).pathname;
    const last = decodeURIComponent(path.slice(path.lastIndexOf('/') + 1));
    return sanitizeFilename(last || 'index.html');
  } catch {
    return 'download';
  }
}

/** The kind of file a MIME type names, for category selection without a usable name. */
function kindOfMime(mime: string): FileKind | null {
  const m = mime.toLowerCase().split(';')[0]?.trim() ?? '';
  if (m.startsWith('video/')) return 'video';
  if (m.startsWith('audio/')) return 'audio';
  if (/^application\/(zip|x-7z-compressed|vnd\.rar|x-rar-compressed|gzip|x-tar|x-xz|x-bzip2|zstd|x-iso9660-image)$/.test(m)) {
    return 'archives';
  }
  if (/^(application\/(pdf|epub\+zip|msword|vnd\.openxmlformats-officedocument\..+|vnd\.oasis\.opendocument\..+)|text\/.+)$/.test(m)) {
    return 'documents';
  }
  if (/^application\/(x-msdownload|x-msi|vnd\.microsoft\.portable-executable|vnd\.android\.package-archive|vnd\.debian\.binary-package|x-rpm|java-archive)$/.test(m)) {
    return 'programs';
  }
  return null;
}

function segmentLength(s: Segment, size: number): number {
  const end = s.end < 0 ? (size > 0 ? size - 1 : Number.POSITIVE_INFINITY) : s.end;
  return end - s.start + 1;
}

// ---- streaming media ----------------------------------------------------------------

/** FNV-1a: a stable number per manifest address (its duration, title, sizes). */
function hashOf(text: string): number {
  let h = 0x811c9dc5;
  for (let i = 0; i < text.length; i++) {
    h ^= text.charCodeAt(i);
    h = Math.imul(h, 0x01000193) >>> 0;
  }
  return h;
}

function track(partial: Partial<MediaTrack> & Pick<MediaTrack, 'id' | 'kind'>): MediaTrack {
  return {
    bandwidth: 0,
    width: 0,
    height: 0,
    frameRate: 0,
    codecs: '',
    language: '',
    name: '',
    size: 0,
    sizeApprox: false,
    ext: '',
    ...partial,
  };
}

const VIDEO_LADDER = [
  { width: 640, height: 360, bandwidth: 800_000, codecs: 'avc1.4d401e' },
  { width: 1280, height: 720, bandwidth: 2_800_000, codecs: 'avc1.4d401f' },
  { width: 1920, height: 1080, bandwidth: 5_500_000, codecs: 'avc1.640028' },
] as const;

const AUDIO_SET = [
  { language: 'en', name: 'English', bandwidth: 128_000, codecs: 'mp4a.40.2', dash: 'audio-en' },
  { language: 'es', name: 'Español', bandwidth: 128_000, codecs: 'mp4a.40.2', dash: 'audio-es' },
  { language: 'en', name: 'Commentary', bandwidth: 64_000, codecs: 'mp4a.40.5', dash: 'audio-en-commentary' },
] as const;

/** Fragment length: HLS media playlists commonly use 6 s, DASH 4 s segments. */
function fragmentMs(format: string): number {
  return format === 'dash' ? 4000 : 6000;
}

/** The title a manifest's address suggests: the folder that holds it ("keynote-2026"). */
function titleOf(url: URL): string {
  const parts = url.pathname.split('/').filter(Boolean);
  const file = (parts.at(-1) ?? '').replace(/\.(m3u8|mpd)$/i, '');
  const generic = /^(master|manifest|index|playlist|stream|main|audio|video)$/i;
  let title = parts.length >= 2 && generic.test(file) ? (parts[parts.length - 2] ?? '') : file;
  try {
    title = decodeURIComponent(title);
  } catch {
    // keep it encoded
  }
  return title || 'stream';
}

/** What the daemon's probe would find (MEDIA-SPEC §6.3), or its refusal. */
function probeStream(manifestUrl: string): MediaInfo {
  const checked = checkUrl(manifestUrl);
  if (!checked.ok) throw new ApiError('invalid_argument', `manifest_url ${checked.reason}`);
  const url = new URL(manifestUrl);
  const format = /\.m3u8$/i.test(url.pathname) ? 'hls' : /\.mpd$/i.test(url.pathname) ? 'dash' : '';
  if (!format) throw new ApiError('invalid_argument', 'manifest_url: not an HLS (.m3u8) or DASH (.mpd) manifest');
  const marker = manifestUrl.toLowerCase();
  if (/404/.test(marker)) throw new ApiError('failed_precondition', 'manifest: the server responded 404 Not Found');
  if (/drm|encrypted/.test(marker)) {
    throw new ApiError('invalid_argument', 'encrypted: the stream is encrypted or DRM-protected');
  }
  if (/live/.test(marker)) throw new ApiError('invalid_argument', 'live: the presentation is live and still growing');
  const h = hashOf(manifestUrl);
  const durationMs = (3 + (h % 88)) * 60_000 + (h % 59) * 1000;
  const audioOnly = /audio|podcast/.test(marker);
  const muxed = format === 'hls' && !audioOnly && /muxed/.test(marker);
  const tracks: MediaTrack[] = [];
  if (!audioOnly) {
    VIDEO_LADDER.forEach((v, i) =>
      tracks.push(
        track({
          id: format === 'hls' ? `v${i}` : `video-${v.height}p`,
          kind: muxed ? 'muxed' : 'video',
          width: v.width,
          height: v.height,
          bandwidth: muxed ? v.bandwidth + 128_000 : v.bandwidth,
          codecs: muxed ? `${v.codecs},mp4a.40.2` : v.codecs,
          frameRate: i === 0 ? 25 : 30,
          // downexd names every variant by its quality class (one per class).
          name: standardName(qualityClass(v.width, v.height), i === 0 ? 25 : 30, false),
        }),
      ),
    );
  }
  if (!muxed) {
    AUDIO_SET.forEach((a, i) =>
      tracks.push(
        track({
          id: format === 'hls' ? `r${i}` : a.dash,
          kind: 'audio',
          bandwidth: a.bandwidth,
          codecs: a.codecs,
          language: a.language,
          name: a.name,
        }),
      ),
    );
  }
  const info: MediaInfo = {
    manifestUrl,
    format,
    tracks,
    selectedVideo: '',
    selectedAudio: '',
    durationMs,
    fragmentsTotal: 0,
    fragmentsDone: 0,
    container: 'mp4',
    title: titleOf(url),
    origin: 'manifest',
    site: '',
    pageUrl: '',
  };
  return withPick(info, defaultPick(info));
}

// ---- video pages (SITE-MEDIA-SPEC §3.11) ----------------------------------------------

/** A web page's video downloads in parts of this size (the googlevideo request cap). */
const PART_BYTES = 10 * MiB;

/**
 * The MP4 qualities a page may offer: height, frame rate, and the H.264 and AV1 bitrates
 * (kbit/s; 0 = not offered) with YouTube-like format ids.
 */
const SITE_LADDER = [
  { height: 2160, fps: 60, avc: 0, av1: 18_000, ids: ['', '701'] },
  { height: 2160, fps: 30, avc: 0, av1: 12_000, ids: ['', '401'] },
  { height: 1440, fps: 60, avc: 0, av1: 9_000, ids: ['', '700'] },
  { height: 1440, fps: 30, avc: 0, av1: 6_000, ids: ['', '400'] },
  { height: 1080, fps: 60, avc: 6_000, av1: 3_800, ids: ['299', '699'] },
  { height: 1080, fps: 30, avc: 4_400, av1: 2_100, ids: ['137', '399'] },
  { height: 720, fps: 60, avc: 3_300, av1: 1_900, ids: ['298', '698'] },
  { height: 720, fps: 30, avc: 2_200, av1: 1_200, ids: ['136', '398'] },
  { height: 480, fps: 30, avc: 1_100, av1: 650, ids: ['135', '397'] },
  { height: 360, fps: 30, avc: 650, av1: 400, ids: ['134', '396'] },
  { height: 240, fps: 30, avc: 300, av1: 200, ids: ['133', '395'] },
  { height: 144, fps: 30, avc: 120, av1: 90, ids: ['160', '394'] },
] as const;

/** The site's name for display: YouTube, else the host without "www.". */
function siteOf(url: URL): string {
  const host = url.hostname.toLowerCase();
  if (/(^|\.)(youtube\.com|youtu\.be|youtube-nocookie\.com)$/.test(host)) return 'YouTube';
  return host.replace(/^www\./, '');
}

/** The title a page suggests: a YouTube id, else its last path segment, else its host. */
function pageTitle(url: URL): string {
  const v = url.searchParams.get('v');
  if (v) return `Video ${v}`;
  const last = url.pathname.split('/').filter(Boolean).at(-1) ?? '';
  try {
    return decodeURIComponent(last) || url.hostname;
  } catch {
    return last || url.hostname;
  }
}

// ---- layout fixtures (scripts/measure-dialogs.mjs) ---------------------------------------

/** A fixture video's title: the owner's case, one that wraps, right-to-left ones, none. */
const FIXTURE_TITLES: Readonly<Record<string, string>> = {
  plain: 'Set Up Staging and Production with Dokploy',
  long: 'Set Up Staging and Production with Dokploy: Docker Compose, Traefik, Preview Deployments, Automatic Backups and Zero-Downtime Rollbacks on One VPS (Full Course, 2026 Edition)',
  ku: 'سلێمانی تا بەغداد بۆ بینینی The Odyssey و IMAX',
  ar: 'أطيب كباب في السليمانية_ أكلات شعبية وخبز التنور الحار',
  none: '',
};

/**
 * The offers of real YouTube pages by their number of MP4 qualities (one per class, best first):
 * height, frame rate, codec, kbit/s, HDR and the format id. 6 is the owner's report (1080p 50fps
 * down to 144p, all H.264); 8 goes from 4K HDR (AV1, sizes estimated) to 144p.
 */
const FIXTURE_LADDERS: Readonly<Record<number, readonly (readonly [number, number, 'avc' | 'av1', number, boolean, string])[]>> = {
  1: [[360, 30, 'avc', 650, false, '134']],
  3: [
    [720, 30, 'avc', 2200, false, '136'],
    [480, 30, 'avc', 1100, false, '135'],
    [360, 30, 'avc', 650, false, '134'],
  ],
  6: [
    [1080, 50, 'avc', 5200, false, '299'],
    [720, 50, 'avc', 3000, false, '298'],
    [480, 25, 'avc', 1100, false, '135'],
    [360, 25, 'avc', 650, false, '134'],
    [240, 25, 'avc', 300, false, '133'],
    [144, 25, 'avc', 120, false, '160'],
  ],
  8: [
    [2160, 60, 'av1', 21_000, true, '701'],
    [1440, 60, 'av1', 9500, true, '700'],
    [1080, 60, 'avc', 6000, false, '299'],
    [720, 60, 'avc', 3300, false, '298'],
    [480, 30, 'avc', 1100, false, '135'],
    [360, 30, 'avc', 650, false, '134'],
    [240, 30, 'avc', 300, false, '133'],
    [144, 30, 'avc', 120, false, '160'],
  ],
};

/** What a fixture video offers: its page, qualities (1, 3, 6 or 8), audio tracks (1–3) and title. */
interface FixtureVideo {
  readonly pageUrl: string;
  readonly qualities: number;
  readonly audio: number;
  readonly title: string;
  /** 16:31, as in the owner's report. */
  readonly durationMs?: number;
}

/** A fixture video's offer, as downexd lists a page's (SITE-MEDIA-SPEC §3.11). */
function fixtureOffer(v: FixtureVideo): MediaInfo {
  const ladder = FIXTURE_LADDERS[v.qualities];
  if (!ladder) throw new Error(`fixture: no offer with ${v.qualities} qualities (1, 3, 6 or 8)`);
  const durationMs = v.durationMs ?? 991_000;
  const bytes = (kbit: number) => Math.round((kbit * 1000 * durationMs) / 1000 / 8);
  const videos = ladder.map(([height, fps, codec, kbit, hdr, id]) => {
    const width = Math.round((height * 16) / 9);
    return track({
      id,
      kind: 'video',
      width,
      height,
      frameRate: fps,
      bandwidth: kbit * 1000,
      codecs: codec === 'avc' ? 'avc1.640028' : 'av01.0.12M.10',
      name: standardName(qualityClass(width, height), fps, hdr),
      size: bytes(kbit),
      sizeApprox: codec === 'av1',
      ext: 'mp4',
    });
  });
  const audios = [
    track({ id: '140', kind: 'audio', bandwidth: 129_000, codecs: 'mp4a.40.2', language: 'en', name: 'English original', size: bytes(129), ext: 'm4a' }),
    track({ id: '140-1', kind: 'audio', bandwidth: 129_000, codecs: 'mp4a.40.2', language: 'es', name: 'Español', size: bytes(129), ext: 'm4a' }),
    track({ id: '139', kind: 'audio', bandwidth: 49_000, codecs: 'mp4a.40.5', language: 'en', name: 'English original, low', size: bytes(49), ext: 'm4a' }),
  ];
  // two tracks: the page's medium and low; three: dubbed languages as well
  const sound = v.audio >= 3 ? audios : v.audio === 2 ? [audios[0], audios[2]] : [audios[0]];
  const info: MediaInfo = {
    manifestUrl: '',
    format: 'http',
    tracks: [...videos, ...sound.filter((t): t is MediaTrack => t !== undefined)],
    selectedVideo: '',
    selectedAudio: '',
    durationMs,
    fragmentsTotal: 0,
    fragmentsDone: 0,
    container: 'mp4',
    title: v.title,
    origin: 'site',
    site: 'YouTube',
    pageUrl: v.pageUrl,
  };
  const pick = defaultPick(info);
  return { ...info, selectedVideo: pick.video, selectedAudio: pickedAudio(info, pick) };
}

/** What yt-dlp would find on a page (formats already turned into downexd's offer), or its refusal. */
function siteOffer(pageUrl: string): MediaInfo {
  const url = new URL(pageUrl);
  // A layout fixture's page: "qualities=<1|3|6|8>", with "audio=<1-3>" and "title=<long|ku|ar|none>".
  const fixed = url.searchParams.get('qualities');
  if (fixed !== null) {
    return fixtureOffer({
      pageUrl,
      qualities: Number(fixed),
      audio: Number(url.searchParams.get('audio') ?? '2'),
      title: FIXTURE_TITLES[url.searchParams.get('title') ?? 'plain'] ?? FIXTURE_TITLES.plain ?? '',
    });
  }
  const marker = pageUrl.toLowerCase();
  if (/unsupported/.test(marker)) throw new ApiError('invalid_argument', 'site_unsupported: no video found on this page');
  if (/broken/.test(marker)) throw new ApiError('failed_precondition', 'site_error: yt-dlp could not read this page: HTTP Error 500');
  if (/private|unavailable/.test(marker)) {
    throw new ApiError('failed_precondition', 'site_unavailable: this video is not available: Private video');
  }
  if (/signin|\bage\b|age-restricted/.test(marker)) {
    throw new ApiError('failed_precondition', 'auth_required: the site asks to sign in: Sign in to confirm your age');
  }
  if (/drm|encrypted/.test(marker)) throw new ApiError('invalid_argument', 'encrypted: the video is DRM-protected');
  if (/live/.test(marker)) throw new ApiError('invalid_argument', 'live: this is a live stream');
  if (/\/playlist\b/.test(url.pathname)) throw new ApiError('invalid_argument', 'media_unsupported: a playlist: open a single video');
  if (/podcast|audio-only/.test(marker)) throw new ApiError('invalid_argument', 'media_unsupported: no MP4 video on this page');
  const h = hashOf(pageUrl);
  const durationMs = (2 + (h % 40)) * 60_000 + (h % 57) * 1000;
  const seconds = durationMs / 1000;
  const maxHeight = ([2160, 1440, 1080, 1080, 720] as const)[h % 5] ?? 1080;
  const sixty = h % 2 === 0;
  const film = /film|movie/.test(marker);
  const bytes = (kbit: number) => Math.round((kbit * 1000 * seconds) / 8);
  const formats: MediaTrack[] = [];
  for (const q of SITE_LADDER) {
    if (q.height > maxHeight || (q.fps > 30 && !sixty)) continue;
    const width = Math.round((q.height * 16) / 9);
    // A film's picture is as wide and lower: 3840×1634 for a 2160 step.
    const height = film ? Math.round(width / 2.35 / 2) * 2 : q.height;
    const name = standardName(qualityClass(width, height), q.fps, false);
    if (q.avc > 0) {
      formats.push(track({ id: q.ids[0], kind: 'video', width, height, frameRate: q.fps, bandwidth: q.avc * 1000, codecs: 'avc1.640028', name, size: bytes(q.avc), ext: 'mp4' }));
    }
    // The site estimates the size of its largest AV1 qualities.
    formats.push(track({ id: q.ids[1], kind: 'video', width, height, frameRate: q.fps, bandwidth: q.av1 * 1000, codecs: 'av01.0.08M.08', name, size: bytes(q.av1), sizeApprox: q.height > 1080, ext: 'mp4' }));
  }
  const muxedHeight = film ? 272 : 360;
  formats.push(track({ id: '18', kind: 'muxed', width: 640, height: muxedHeight, frameRate: 30, bandwidth: 760_000, codecs: 'avc1.42001E, mp4a.40.2', name: standardName(qualityClass(640, muxedHeight), 30, false), size: bytes(760), sizeApprox: true, ext: 'mp4' }));
  // downexd keeps one quality per class: video only, SDR, the higher frame rate, H.264, bitrate.
  const videos = onePerClass(formats);
  const audios: MediaTrack[] = /dub/.test(marker)
    ? [
        track({ id: '140-0', kind: 'audio', bandwidth: 129_000, codecs: 'mp4a.40.2', language: 'en', name: 'English original', size: bytes(129), ext: 'm4a' }),
        track({ id: '140-1', kind: 'audio', bandwidth: 129_000, codecs: 'mp4a.40.2', language: 'es', name: 'Español', size: bytes(129), ext: 'm4a' }),
        track({ id: '139-0', kind: 'audio', bandwidth: 49_000, codecs: 'mp4a.40.5', language: 'en', name: 'English original, low', size: bytes(49), ext: 'm4a' }),
      ]
    : [
        track({ id: '140', kind: 'audio', bandwidth: 129_000, codecs: 'mp4a.40.2', name: 'medium', size: bytes(129), ext: 'm4a' }),
        track({ id: '139', kind: 'audio', bandwidth: 49_000, codecs: 'mp4a.40.5', name: 'low', size: bytes(49), ext: 'm4a' }),
      ];
  const info: MediaInfo = {
    manifestUrl: '',
    format: 'http',
    tracks: [...videos, ...audios],
    selectedVideo: '',
    selectedAudio: '',
    durationMs,
    fragmentsTotal: 0,
    fragmentsDone: 0,
    container: 'mp4',
    title: pageTitle(url),
    origin: 'site',
    site: siteOf(url),
    pageUrl,
  };
  const pick = defaultPick(info);
  // A probe counts no parts: they are planned when the download runs.
  return { ...info, selectedVideo: pick.video, selectedAudio: pickedAudio(info, pick) };
}

/**
 * The stream with a choice of tracks: its selection and fragment count (a web page's
 * video: its parts, PART_BYTES of each chosen track).
 */
function withPick(info: MediaInfo, pick: TrackPick): MediaInfo {
  const audio = pickedAudio(info, pick);
  const chosen = [trackById(info, pick.video), trackById(info, audio)].filter((t): t is MediaTrack => t !== undefined);
  const fragmentsTotal = isSite(info)
    ? chosen.reduce((n, t) => n + Math.max(1, Math.ceil(t.size / PART_BYTES)), 0)
    : Math.ceil(info.durationMs / fragmentMs(info.format)) * chosen.length;
  return { ...info, selectedVideo: pick.video, selectedAudio: audio, fragmentsTotal, fragmentsDone: 0 };
}

/**
 * Resolves requested track ids as the daemon does: empty = its default pick; a muxed
 * video track carries its own sound, so no audio track is used with it.
 */
function choosePick(info: MediaInfo, video: string, audio: string): TrackPick {
  const v = trackById(info, video);
  if (video && (!v || (v.kind !== 'video' && v.kind !== 'muxed'))) {
    throw new ApiError('invalid_argument', `video_track: the stream has no video track "${video}"`);
  }
  if (audio && trackById(info, audio)?.kind !== 'audio') {
    throw new ApiError('invalid_argument', `audio_track: the stream has no audio track "${audio}"`);
  }
  const def = defaultPick(info);
  const base = withVideo(info, def, video || def.video);
  const pick = { video: base.video, audio: audio || base.audio };
  return { video: pick.video, audio: pickedAudio(info, pick) };
}

/**
 * The bytes the chosen tracks take: a web page's from their declared sizes, a stream's
 * from their bitrate (varied a little per stream).
 */
function mediaBytes(info: MediaInfo): number {
  if (isSite(info)) return Math.max(1, pickSize(info, { video: info.selectedVideo, audio: info.selectedAudio }).bytes);
  const bytes = estimatedBytes(info, { video: info.selectedVideo, audio: info.selectedAudio });
  const jitter = 0.85 + (hashOf(info.manifestUrl) % 20) / 100;
  return Math.max(1, Math.round(Math.max(0, bytes) * jitter));
}

/** The name the daemon gives a stream or a page's video: the requested name or the title, with ".mp4". */
function mediaFilename(base: string): string {
  const name = sanitizeFilename(base.trim() || 'stream');
  return extensionOf(name) === 'mp4' ? name : `${name}.mp4`;
}

function clone(d: MDownload): Download {
  // Connections are live: only a running download has them.
  return {
    ...d,
    segments: d.segments.map((s) => ({ ...s })),
    connectionInfo: d.state === 'downloading' ? d.connectionInfo : [],
  };
}

const RETRY_REASONS = ['HTTP 503', 'connection reset', 'timed out'] as const;

/**
 * A connection's weight in its download's speed: like real server connections, each runs a
 * little faster or slower than the next (0.85–1.15), so per-connection speeds differ and add up
 * to the download's.
 */
function slotWeight(index: number): number {
  return 0.85 + ((index * 37) % 31) / 100;
}

/** Moves a slot through its timed phases: connecting, requesting, retrying, then receiving. */
function advanceSlot(slot: Slot, dtMs: number): void {
  let left = dtMs;
  while (left > 0 && (slot.state === 'connecting' || slot.state === 'requesting' || slot.state === 'retrying')) {
    if (left < slot.phaseMs) {
      slot.phaseMs -= left;
      return;
    }
    left -= slot.phaseMs;
    if (slot.state === 'requesting') {
      slot.state = 'receiving';
      slot.phaseMs = 0;
    } else if (slot.state === 'connecting') {
      slot.state = 'requesting';
      slot.phaseMs = 200;
    } else {
      slot.state = 'connecting';
      slot.phaseMs = 300;
      slot.reason = '';
    }
  }
}

function slotInfo(slot: Slot, fragment: number): ConnectionInfo {
  const range = slot.seg ? { start: slot.seg.start, end: slot.seg.end, received: slot.seg.received } : slot.last;
  return {
    index: slot.index,
    state: slot.state,
    start: range.start,
    end: range.end,
    received: range.received,
    speedBps: slot.state === 'receiving' ? Math.round(slot.speedBps) : 0,
    // downexd's wording: "<reason>, retrying in <n> s".
    detail: slot.state === 'retrying' ? `${slot.reason}, retrying in ${Math.max(1, Math.ceil(slot.phaseMs / 1000))} s` : '',
    fragment,
  };
}

export function createMockApi(options: MockOptions = {}): MockApi {
  const rng = prng(options.seed ?? 0x5eed);
  const now = options.now ?? Date.now;
  const tickMs = options.tickMs ?? 250;
  const latencyMs = options.latencyMs ?? 0;
  const failureRate = options.failureRate ?? 0.05;
  const retryRate = options.retryRate ?? 0.006;
  const assembleMs = options.assembleMs ?? 1500;
  // (a fixture may hold the install or a page read: applyFixture)
  let toolsInstallMs = options.toolsInstallMs ?? 4000;
  let resolveMs = options.resolveMs ?? 1200;
  /** A fixture's add of a page's video that waits for yt-dlp for ten minutes (add:slow). */
  let addSlowUntil = 0;
  // Connection hiccups draw from their own sequence, so that they leave the rest of the
  // simulation (sizes, speeds, failures) as it is for a seed.
  const slotRng = prng((options.seed ?? 0x5eed) ^ 0x2f6b9c1d);
  const startedAt = now();
  let settings: Settings = {
    ...DEFAULT_SETTINGS,
    defaultDir: options.defaultDir ?? guessDefaultDir(),
    ...options.settings,
  };
  const sep = settings.defaultDir.includes('\\') ? '\\' : '/';

  const sims = new Map<number, Sim>();
  const subscribers = new Set<(batch: EventBatch) => void>();
  const confirmSubscribers = new Set<(id: number) => void>();
  const changed = new Set<number>();
  const removed: number[] = [];
  let nextId = 1;
  // Queues (M6-SPEC §1).
  const queues = new Map<number, MQueue>();
  const changedQueues = new Set<number>();
  const removedQueues: number[] = [];
  const postEvents: PostActionPending[] = [];
  /** Countdowns by queue: what runs at the deadline. */
  const countdowns = new Map<number, { action: PostAction; deadline: number; abortAt: number }>();
  let nextQueueId = 1;
  // Secrets: only whether one is stored.
  let proxyPassword = false;
  const loginPasswords = new Set<string>();
  let nextLogin = 1;
  let queueDirty = true;
  // yt-dlp and Deno (SITE-MEDIA-SPEC §4): missing until a video page or Install asks.
  let toolsPhase: 'missing' | 'installing' | 'ready' | 'failed' = options.toolsReady ? 'ready' : 'missing';
  let toolsLeftMs = 0;
  let toolsCheckedAt = options.toolsReady ? now() : 0;
  let toolsChanged = false;
  /** InstallTools calls waiting for the install to end. */
  let toolsWaiters: { resolve: (s: ToolsStatus) => void; reject: (e: unknown) => void }[] = [];
  /** Pages yt-dlp is reading (time left), and what it found (kept 15 minutes). */
  const resolving = new Map<string, number>();
  const resolved = new Map<string, { at: number; info: MediaInfo | null; error: ApiError | null }>();
  let timer: ReturnType<typeof setInterval> | undefined;
  let handoffTimer: ReturnType<typeof setTimeout> | undefined;
  const windowState: MockWindowState = {
    title: '',
    size: { width: 1200, height: 760 },
    sizes: [],
    closed: false,
    opened: [],
    minimised: 0,
    maximised: false,
    themes: [],
    folders: [],
  };
  /** Free space of the simulated volumes: what completes in this run takes from it. */
  const freeAtStart = options.freeBytes ?? 214 * GiB;
  let completedBytes = 0;
  /** Downloads whose file was deleted on disk (deleteFile). */
  const gone = new Set<number>();
  /** A fixture's refusal of every confirmation (the daemon's error the dialog shows). */
  let confirmRefusal: ApiError | null = null;

  /** A completion takes its size from the volume; a replaced file gives its own back. */
  function addCompleted(sim: Sim): void {
    completedBytes += sim.d.size - Math.max(0, sim.replaceBytes);
    sim.replaceBytes = -1;
  }

  /** The first "name (n).ext" no download in the same folder holds (as downexd picks it). */
  function freeVariant(d: MDownload): string {
    const taken = new Set([...sims.values()].filter((s) => s.d.dir === d.dir).map((s) => s.d.filename.toLowerCase()));
    for (let n = 1; ; n++) {
      const cand = numberedName(d.filename, n);
      if (!taken.has(cand.toLowerCase())) return cand;
    }
  }

  /** The user's home, as the default download folder shows it. */
  function mockHome(): string {
    return settings.defaultDir.replace(/[\\/]Downloads[\\/]?$/i, '') || (sep === '\\' ? 'C:\\Users\\you' : '/home/you');
  }

  /** downexd's folders under the user's home. */
  function mockFolders(): { dataDir: string; logDir: string; toolsDir: string } {
    const home = mockHome();
    return sep === '\\'
      ? {
          dataDir: `${home}\\AppData\\Roaming\\Downex`,
          logDir: `${home}\\AppData\\Local\\Downex\\logs`,
          toolsDir: `${home}\\AppData\\Local\\Downex\\tools`,
        }
      : { dataDir: `${home}/.config/Downex`, logDir: `${home}/.local/state/Downex`, toolsDir: `${home}/.local/share/Downex/tools` };
  }

  /** Diagnostics as the host writes them: no endpoint, address, file name or profile path. */
  function mockDiagnostics(webview: string): string {
    const home = mockHome();
    const hide = (p: string): string =>
      sep === '\\'
        ? p
            .replace(`${home}\\AppData\\Local`, '%LOCALAPPDATA%')
            .replace(`${home}\\AppData\\Roaming`, '%APPDATA%')
            .replace(home, '%USERPROFILE%')
        : p.replace(home, '~');
    const f = mockFolders();
    const counts = new Map<string, number>();
    for (const sim of sims.values()) counts.set(sim.d.state, (counts.get(sim.d.state) ?? 0) + 1);
    const lines = [
      `Downex window 0.0.0-mock (demo, ${sep === '\\' ? 'windows' : 'linux'}/amd64, Go 1.27.0)`,
      ...(webview ? [`WebView2 ${webview}`] : []),
      'downexd 0.0.0-mock (simulated)',
      'Updates supported: no',
      `Data folder: ${hide(f.dataDir)}`,
      `Log folder: ${hide(f.logDir)}`,
      `Tools folder: ${hide(f.toolsDir)}`,
      `Settings: ${settings.maxConcurrentDownloads} at once, ${settings.defaultMaxConnections} connections`,
      `Update address: ${settings.updateFeedUrl ? 'set' : 'not set'}`,
      `Proxy: ${settings.proxy.mode}`,
      `Host rules: ${settings.hostRules.length}; Site logins: ${settings.siteLogins.length}`,
      `Video sites: ${settings.siteMedia ? 'on' : 'off'}; tools ${toolsPhase}`,
      `Downloads: ${sims.size}; ${[...counts.entries()].map(([state, n]) => `${state}: ${n}`).sort().join(', ')}`,
    ];
    return `${lines.join('\n')}\n`;
  }

  const between = (lo: number, hi: number) => lo + rng() * (hi - lo);
  const pick = <T>(items: readonly T[]): T => items[Math.floor(rng() * items.length)] as T;

  function newSim(d: MDownload): Sim {
    return {
      d,
      baseSpeed: between(0.4 * MiB, 14 * MiB) * (rng() < 0.15 ? 0.1 : 1),
      wobble: 1,
      probeLeftMs: 0,
      failAt: -1,
      failCode: '',
      failMessage: '',
      probeError: '',
      hiddenSize: -1,
      remoteChanged: false,
      changeAt: -1,
      scenarioDone: false,
      noRange: false,
      pendingSize: -1,
      media: null,
      slots: [],
      assembleLeftMs: -1,
      replaceBytes: -1,
    };
  }

  /**
   * The category the daemon would choose (DESKTOP-SPEC §2): by the extension of the
   * requested name, else of the URL's last path segment, else by the MIME hint.
   */
  function chooseCategory(filename: string, url: string, mime: string): string {
    for (const name of [filename, nameFromUrl(url)]) {
      const ext = extensionOf(name);
      if (!ext) continue;
      const hit = settings.categories.find((c) => c.extensions.includes(ext));
      if (hit) return hit.name;
    }
    const kind = kindOfMime(mime);
    return kind ? (settings.categories.find((c) => categoryKind(c) === kind)?.name ?? '') : '';
  }

  function baseDownload(url: string, filename: string, dir: string, size: number, createdAt: number): MDownload {
    return {
      id: nextId++,
      url,
      finalUrl: '',
      dir,
      filename,
      size,
      sizeApprox: false,
      received: 0,
      state: 'queued',
      mode: 'unspecified',
      maxConnections: 0,
      speedLimit: 0,
      error: '',
      errorCode: '',
      createdAt,
      updatedAt: createdAt,
      completedAt: 0,
      speedBps: 0,
      connections: 0,
      segments: [],
      queuePosition: 0,
      source: 'ui',
      expectedSize: 0,
      mime: '',
      category: '',
      kind: 'file',
      media: null,
      connectionInfo: [],
      completionDialog: false,
      activeMs: 0,
      startedAt: 0,
      queueId: MAIN_QUEUE_ID,
    };
  }

  // ---- queues ---------------------------------------------------------------------

  function newQueue(spec: QueueSpec, isMain = false, running = isMain): MQueue {
    const q: MQueue = {
      id: nextQueueId++,
      name: spec.name,
      running,
      maxConcurrent: spec.maxConcurrent,
      speedLimit: spec.speedLimit,
      schedule: { ...spec.schedule },
      postAction: spec.postAction,
      isMain,
      order: [],
      completedThisRun: false,
    };
    queues.set(q.id, q);
    changedQueues.add(q.id);
    return q;
  }

  function queueClone(q: MQueue): Queue {
    return {
      id: q.id,
      name: q.name,
      running: q.running,
      maxConcurrent: q.maxConcurrent,
      speedLimit: q.speedLimit,
      schedule: { ...q.schedule },
      postAction: q.postAction,
      isMain: q.isMain,
      downloadIds: [...q.order],
    };
  }

  function getQueue(id: number): MQueue {
    const q = queues.get(id);
    if (!q) throw new ApiError('not_found', `queue ${id} not found`);
    return q;
  }

  /** The queue a request names (0 or absent = the main queue). */
  function targetQueue(id: number | undefined): MQueue {
    return getQueue(id && id > 0 ? id : MAIN_QUEUE_ID);
  }

  /** Puts a download at the end of a queue (out of the one it was in). */
  function enqueue(d: MDownload, q: MQueue): void {
    const old = queues.get(d.queueId);
    if (old && old !== q) {
      old.order = old.order.filter((id) => id !== d.id);
      changedQueues.add(old.id);
    }
    if (!q.order.includes(d.id)) q.order.push(d.id);
    d.queueId = q.id;
    changedQueues.add(q.id);
  }

  function checkQueueSpec(spec: QueueSpec, selfId: number | null): void {
    const name = spec.name.trim();
    if (name === '' || [...name].length > LIMITS.maxQueueNameLength) {
      throw new ApiError('invalid_argument', `name: must be 1-${LIMITS.maxQueueNameLength} characters`);
    }
    for (const q of queues.values()) {
      if (q.id !== selfId && q.name.toLowerCase() === name.toLowerCase()) {
        throw new ApiError('already_exists', `a queue named "${name}" exists`);
      }
    }
    if (!Number.isInteger(spec.maxConcurrent) || spec.maxConcurrent < 0 || spec.maxConcurrent > LIMITS.maxQueueConcurrent) {
      throw new ApiError('invalid_argument', `max_concurrent must be 0-${LIMITS.maxQueueConcurrent}`);
    }
    checkSpeed(spec.speedLimit);
    const s = spec.schedule;
    const minute = (m: number) => Number.isInteger(m) && m >= 0 && m < 1440;
    if (s.enabled && (!minute(s.startMinute) || (s.stopEnabled && !minute(s.stopMinute)))) {
      throw new ApiError('invalid_argument', 'schedule: times must be 0-1439 minutes');
    }
    if (s.enabled && s.weekdays === 0 && !/^\d{4}-\d{2}-\d{2}$/.test(s.onceDate)) {
      throw new ApiError('invalid_argument', 'schedule: once_date must be YYYY-MM-DD');
    }
  }

  function isRunningState(d: MDownload): boolean {
    return d.state === 'downloading' || d.state === 'probing';
  }

  /** Counts down a queue's action when it ran out of work after completing something. */
  function checkFinishedQueues(): void {
    const t = now();
    for (const q of queues.values()) {
      if (!q.running || q.postAction === 'none' || !q.completedThisRun || countdowns.has(q.id)) continue;
      const busy = q.order.some((id) => {
        const d = sims.get(id)?.d;
        return d !== undefined && (d.state === 'queued' || isRunningState(d));
      });
      if (busy) continue;
      q.completedThisRun = false;
      const deadline = t + 60_000;
      countdowns.set(q.id, { action: q.postAction, deadline, abortAt: deadline + 3000 });
      postEvents.push({ queueId: q.id, action: q.postAction, deadline, cancelled: false });
    }
    for (const [id, c] of countdowns) {
      // The simulated daemon cannot act: it reports the action as aborted.
      if (t >= c.abortAt) {
        countdowns.delete(id);
        postEvents.push({ queueId: id, action: c.action, deadline: c.deadline, cancelled: true });
      }
    }
  }

  /** The running time of seeded history, as if it had come at about 2 MiB/s. */
  function seedTimes(d: MDownload, bytes: number): void {
    d.activeMs = Math.max(1000, Math.round((bytes / (2 * MiB)) * 1000));
    d.startedAt = d.createdAt + 1500;
  }

  function planSegments(sim: Sim): void {
    const d = sim.d;
    const ranged = !sim.noRange && d.size > 0;
    d.mode = ranged ? 'ranged' : 'single';
    if (!ranged) {
      d.segments = [{ start: 0, end: d.size > 0 ? d.size - 1 : -1, received: 0 }];
      return;
    }
    const wanted = d.maxConnections || settings.defaultMaxConnections;
    const n = Math.max(1, Math.min(wanted, Math.floor(d.size / MiB)));
    const step = Math.floor(d.size / n);
    d.segments = Array.from({ length: n }, (_, i) => ({
      start: i * step,
      end: i === n - 1 ? d.size - 1 : (i + 1) * step - 1,
      received: 0,
    }));
  }

  /** Spreads `bytes` over fresh segments to give seeded downloads a believable map. */
  function seedProgress(sim: Sim, fraction: number): void {
    planSegments(sim);
    const d = sim.d;
    let total = 0;
    for (const s of d.segments) {
      const len = segmentLength(s, d.size);
      if (!Number.isFinite(len)) {
        s.received = Math.floor(between(1, 50) * MiB);
      } else {
        s.received = Math.floor(len * Math.min(1, Math.max(0, fraction + between(-0.25, 0.25))));
      }
      total += s.received;
    }
    d.received = total;
  }

  function seed(count: number): void {
    const t0 = now();
    const span = count > 500 ? 365 * 86400_000 : 30 * 86400_000;
    const kinds = Object.keys(NAMES) as FileKind[];
    for (let i = 0; i < count; i++) {
      const cat = pick(kinds);
      const template = pick(NAMES[cat]);
      const filename = template.replace('#', String(1 + Math.floor(rng() * 400)));
      const host = pick(HOSTS);
      // Older entries are mostly finished; the newest ones are still queued.
      const fromEnd = count - 1 - i;
      const unknownSize = fromEnd === 4;
      const noRange = fromEnd === 6;
      const [lo, hi] = SIZE_RANGE[cat];
      const size = Math.floor(between(lo, hi));
      const createdAt = Math.floor(t0 - span * ((count - i) / count) - rng() * 3600_000);
      const url = `https://${host}/${unknownSize ? 'live/unknown-size' : 'pub'}/${encodeURIComponent(filename)}`;
      const category = chooseCategory(filename, url, '');
      // Older downloads were saved before category folders existed.
      const dir = rng() < 0.5 ? categoryFolder(settings, category) : settings.defaultDir;
      const d = baseDownload(url, filename, dir, unknownSize ? -1 : size, createdAt);
      d.category = category;
      d.source = (['ui', 'browser', 'cli'] as const)[i % 3] ?? 'ui';
      const sim = newSim(d);
      if (unknownSize) sim.hiddenSize = size;
      if (noRange) sim.noRange = true;
      sims.set(d.id, sim);
      enqueue(d, targetQueue(MAIN_QUEUE_ID));

      const roll = rng();
      const recent = i / count >= 0.6;
      const completedShare = recent ? 0.25 : 0.82;
      if (unknownSize || noRange || fromEnd <= settings.maxConcurrentDownloads + 1) {
        d.state = 'queued';
      } else if (roll < completedShare) {
        d.state = 'completed';
        d.received = size;
        d.mode = 'ranged';
        d.completedAt = Math.min(t0, createdAt + Math.floor(between(20_000, 3_600_000)));
        d.updatedAt = d.completedAt;
        // Started soon after it was added; paused for part of the time now and then.
        d.startedAt = Math.min(d.completedAt, createdAt + 1500);
        d.activeMs = Math.round((d.completedAt - d.startedAt) * (0.6 + ((d.id * 37) % 41) / 100));
      } else if (roll < completedShare + 0.08) {
        d.state = 'paused';
        seedProgress(sim, rng());
        seedTimes(d, d.received);
      } else if (roll < completedShare + 0.16) {
        const [code, message] = pick(SEED_FAILURES);
        d.state = 'failed';
        seedProgress(sim, between(0.05, 0.9));
        seedTimes(d, d.received);
        d.errorCode = code;
        d.error = message;
        if (code === 'validator_changed') sim.remoteChanged = true;
      } else {
        d.state = 'queued';
      }
    }
    seedMedia();
    // Give the first view some live transfers.
    let started = 0;
    for (const sim of sims.values()) {
      if (started >= settings.maxConcurrentDownloads) break;
      if (sim.d.state !== 'queued' || sim.hiddenSize > 0) continue;
      start(sim);
      sim.probeLeftMs = 0;
      sim.d.state = 'downloading';
      seedProgress(sim, between(0.05, 0.6));
      seedTimes(sim.d, sim.d.received);
      sim.d.startedAt = Math.max(sim.d.createdAt, t0 - sim.d.activeMs);
      started++;
    }
    renumberQueue();
  }

  // ---- yt-dlp and Deno, and the pages they read (SITE-MEDIA-SPEC §3.3, §4) --------------

  const TOOLS = [
    { name: 'yt-dlp', version: '2026.10.10.233141', bytes: 18 * MiB, verified: 'openpgp+sha256', file: sep === '\\' ? 'yt-dlp.exe' : 'yt-dlp' },
    { name: 'deno', version: '2.5.4', bytes: 45 * MiB, verified: 'sha256+authenticode', file: sep === '\\' ? 'deno.exe' : 'deno' },
  ] as const;

  function toolsStatus(): ToolsStatus {
    const enabled = settings.siteMedia;
    const done = toolsPhase === 'installing' ? 1 - toolsLeftMs / Math.max(1, toolsInstallMs) : 0;
    const state: ToolState = !enabled ? 'disabled' : toolsPhase;
    const tools = TOOLS.map(
      (t): ToolInfo => ({
        name: t.name,
        state,
        version: toolsPhase === 'ready' ? t.version : '',
        path: toolsPhase === 'ready' ? `${settings.defaultDir}${sep}.downex-tools${sep}${t.name}${sep}${t.version}${sep}${t.file}` : '',
        managed: true,
        latestVersion: toolsCheckedAt > 0 ? t.version : '',
        checkedAt: toolsCheckedAt,
        bytesDone: toolsPhase === 'installing' ? Math.floor(t.bytes * done) : 0,
        bytesTotal: toolsPhase === 'installing' ? t.bytes : 0,
        error: toolsPhase === 'failed' ? `could not download ${t.name} (network)` : '',
        errorCode: toolsPhase === 'failed' ? 'network' : '',
        verified: toolsPhase === 'ready' ? t.verified : '',
      }),
    );
    return { tools, enabled, ready: enabled && toolsPhase === 'ready' };
  }

  function toolsPercent(): number {
    return toolsPhase === 'installing' ? Math.floor((1 - toolsLeftMs / Math.max(1, toolsInstallMs)) * 100) : 0;
  }

  /** The tools' phase now (read through a call: an install can end inside another call). */
  function phaseNow(): typeof toolsPhase {
    return toolsPhase;
  }

  /** Starts the install unless it runs; reports how it stands (an instant one is over at once). */
  function startInstall(): 'installing' | 'ready' | 'failed' {
    if (phaseNow() !== 'installing') {
      toolsPhase = 'installing';
      toolsLeftMs = toolsInstallMs;
      toolsChanged = true;
      if (toolsInstallMs <= 0) finishInstall();
    }
    const phase = phaseNow();
    return phase === 'missing' ? 'installing' : phase;
  }

  function toolsUnavailable(): ApiError {
    return new ApiError('failed_precondition', 'tools_unavailable: yt-dlp and Deno are not set up: could not download yt-dlp (network)');
  }

  function siteMediaOff(): ApiError {
    return new ApiError('failed_precondition', 'site_media_off: video downloads from web pages are turned off (Settings → Video sites)');
  }

  function finishInstall(): void {
    toolsPhase = options.toolsFail ? 'failed' : 'ready';
    toolsCheckedAt = now();
    toolsChanged = true;
    const waiters = toolsWaiters;
    toolsWaiters = [];
    for (const w of waiters) {
      if (toolsPhase === 'ready') w.resolve(toolsStatus());
      else w.reject(toolsUnavailable());
    }
  }

  /** Tools.Ensure: ready, or the install starts (or goes on) and the caller is told to come back. */
  function ensureTools(): void {
    if (!settings.siteMedia) throw siteMediaOff();
    if (toolsPhase === 'ready') return;
    if (toolsPhase === 'failed') throw toolsUnavailable();
    const phase = startInstall();
    if (phase === 'ready') return;
    if (phase === 'failed') throw toolsUnavailable();
    throw new ApiError('failed_precondition', `tools_installing: downloading yt-dlp and Deno (${toolsPercent()}%)`);
  }

  /** A page's offer: from the 15-minute cache, else yt-dlp reads it (site_resolving meanwhile). */
  function resolvePage(pageUrl: string): MediaInfo {
    const checked = checkUrl(pageUrl);
    if (!checked.ok) throw new ApiError('invalid_argument', `page_url ${checked.reason}`);
    ensureTools();
    const hit = resolved.get(pageUrl);
    if (hit && now() - hit.at < 15 * 60_000) {
      if (hit.error) throw hit.error;
      if (hit.info) return hit.info;
    }
    if (!resolving.has(pageUrl)) resolving.set(pageUrl, resolveMs);
    if (resolveMs <= 0) return finishResolve(pageUrl);
    throw new ApiError('failed_precondition', 'site_resolving: still getting the video info');
  }

  function finishResolve(pageUrl: string): MediaInfo {
    resolving.delete(pageUrl);
    try {
      const info = siteOffer(pageUrl);
      resolved.set(pageUrl, { at: now(), info, error: null });
      return info;
    } catch (err) {
      const error = err instanceof ApiError ? err : new ApiError('internal', String(err));
      resolved.set(pageUrl, { at: now(), info: null, error });
      throw error;
    }
  }

  /** The install and the page reads go on with the simulated time. */
  function stepTools(dtMs: number): void {
    if (toolsPhase === 'installing') {
      toolsLeftMs -= dtMs;
      toolsChanged = true;
      if (toolsLeftMs <= 0) finishInstall();
    }
    for (const [page, left] of [...resolving]) {
      if (left - dtMs > 0) resolving.set(page, left - dtMs);
      else {
        try {
          finishResolve(page);
        } catch {
          // kept as the page's answer
        }
      }
    }
  }

  /** The category the daemon gives a stream: Video, or Audio when it has no video. */
  function mediaCategory(info: MediaInfo): string {
    const kind: FileKind = trackById(info, info.selectedVideo) ? 'video' : 'audio';
    return settings.categories.find((c) => categoryKind(c) === kind)?.name ?? '';
  }

  /** Makes a seeded download a stream in the same state. */
  function seedAsMedia(sim: Sim, manifestUrl: string): void {
    const d = sim.d;
    const info = probeStream(manifestUrl);
    const total = mediaBytes(info);
    const fraction = d.state === 'completed' ? 1 : d.state === 'queued' ? 0 : 0.35;
    d.url = manifestUrl;
    d.finalUrl = '';
    d.filename = mediaFilename(info.title);
    d.kind = 'media';
    d.category = mediaCategory(info);
    d.mode = 'unspecified';
    d.segments = [];
    d.received = Math.floor(total * fraction);
    d.size = d.state === 'completed' ? total : -1;
    d.media = { ...info, fragmentsDone: fraction === 1 ? info.fragmentsTotal : Math.floor(info.fragmentsTotal * fraction) };
    if (d.state === 'failed') {
      d.errorCode = 'network';
      d.error = 'The connection was reset while fetching a fragment';
    }
    sim.media = { probed: info, totalBytes: total };
    sim.hiddenSize = -1;
    sim.noRange = false;
    sim.remoteChanged = false;
  }

  function seedMedia(): void {
    const manifests = [
      'https://media.example.org/talks/keynote-2026/master.m3u8',
      'https://media.example.org/films/coastline/manifest.mpd',
      'https://media.example.org/shows/evening-news/muxed/index.m3u8',
    ];
    const list = [...sims.values()];
    let next = 0;
    // Older entries that are not about to start (the newest ones are queued).
    for (let i = list.length - 8; i >= 0 && next < manifests.length; i -= 3) {
      const sim = list[i];
      if (!sim || sim.d.state === 'queued' || sim.hiddenSize > 0 || sim.noRange) continue;
      seedAsMedia(sim, manifests[next++] as string);
    }
  }

  // ---- the Studio scene (DESIGN-SYSTEM.md §7.1) -------------------------------------------

  /** Spreads `fraction` over fresh segments, each a little ahead or behind (the mean kept). */
  function spreadProgress(sim: Sim, fraction: number): void {
    planSegments(sim);
    const d = sim.d;
    const jitter = d.segments.map(() => between(-0.22, 0.22));
    const mean = jitter.reduce((a, b) => a + b, 0) / Math.max(1, jitter.length);
    let total = 0;
    d.segments.forEach((s, i) => {
      const len = segmentLength(s, d.size);
      const share = Math.min(1, Math.max(0.02, fraction + (jitter[i] ?? 0) - mean));
      s.received = Math.floor(len * share);
      total += s.received;
    });
    d.received = total;
  }

  /** A seeded download that is running now: its connections are already receiving. */
  function seedRunning(sim: Sim, speed: number): void {
    const d = sim.d;
    d.state = 'downloading';
    d.speedBps = speed;
    sim.baseSpeed = speed;
    seedTimes(d, d.received);
    d.startedAt = Math.max(d.createdAt + 1500, now() - d.activeMs);
    if (sim.media) {
      d.connections = Math.min(d.maxConnections || settings.defaultMaxConnections, Math.max(1, (d.media?.fragmentsTotal ?? 1) - (d.media?.fragmentsDone ?? 0)));
      sim.slots = Array.from({ length: d.connections }, (_, i): Slot => ({ ...newSlot(i + 1), state: 'receiving', phaseMs: 0 }));
      return;
    }
    const open = d.segments.filter((s) => s.received < segmentLength(s, d.size));
    sim.slots = open.map(
      (seg, i): Slot => ({
        index: i + 1,
        state: 'receiving',
        phaseMs: 0,
        reason: '',
        seg,
        last: { start: seg.start, end: seg.end, received: seg.received },
        speedBps: (speed * slotWeight(i + 1)) / open.length,
      }),
    );
    d.connections = sim.slots.length;
    d.connectionInfo = sim.slots.map((slot) => slotInfo(slot, -1));
  }

  /** The finished history of a seeded download: it came at 2 to 12 MiB/s soon after it was added. */
  function seedCompleted(d: MDownload): void {
    d.state = 'completed';
    d.received = d.size;
    d.startedAt = d.createdAt + 1500;
    const seconds = Math.max(2, d.size / (between(2, 12) * MiB));
    d.completedAt = Math.min(now(), d.startedAt + Math.round(seconds * 1000));
    d.updatedAt = d.completedAt;
    d.activeMs = Math.max(1000, d.completedAt - d.startedAt);
  }

  function seedStudio(): void {
    const t0 = now();
    for (const row of SCENE) {
      const createdAt = t0 - row.ago * 60_000;
      const url = row.page ?? row.manifest ?? `https://${row.host}/pub/${encodeURIComponent(row.name)}`;
      const size = Math.round(row.size);
      const category = chooseCategory(row.name, url, '');
      const d = baseDownload(url, row.name, categoryFolder(settings, category), size, createdAt);
      d.category = category;
      d.source = row.source ?? 'ui';
      d.maxConnections = row.connections ?? 0;
      const sim = newSim(d);
      sims.set(d.id, sim);
      enqueue(d, targetQueue(MAIN_QUEUE_ID));
      switch (row.state) {
        case 'completed':
          if (row.manifest) {
            d.state = 'completed';
            seedAsMedia(sim, row.manifest);
          } else d.mode = 'ranged';
          seedCompleted(d);
          break;
        case 'awaiting_confirmation':
          // As a browser hand-off: the size is the browser's hint until it is confirmed.
          d.state = 'awaiting_confirmation';
          d.size = -1;
          d.expectedSize = size;
          sim.pendingSize = size;
          break;
        case 'paused':
        case 'failed':
          d.state = row.state;
          spreadProgress(sim, row.fraction ?? 0.2);
          seedTimes(d, d.received);
          if (row.state === 'failed') {
            d.errorCode = 'http_status';
            d.error = 'HTTP 503 Service Unavailable';
          }
          break;
        case 'downloading':
          if (row.page) {
            // A YouTube video in 1080p (H.264 where the page has it), saved as one MP4.
            const offer = siteOffer(row.page);
            const hd = offer.tracks.find((t) => t.kind === 'video' && t.height === 1080 && /^avc1/i.test(t.codecs)) ??
              offer.tracks.find((t) => t.kind === 'video' && t.height === 1080);
            const info: MediaInfo = { ...withPick(offer, choosePick(offer, hd?.id ?? '', '')), title: row.title ?? offer.title };
            const total = mediaBytes(info);
            const fraction = row.fraction ?? 0.4;
            d.kind = 'media';
            d.media = { ...info, fragmentsDone: Math.floor(info.fragmentsTotal * fraction) };
            d.category = mediaCategory(info);
            d.dir = categoryFolder(settings, d.category);
            d.size = total;
            d.expectedSize = total;
            d.received = Math.floor(total * fraction);
            sim.media = { probed: offer, totalBytes: total };
          } else {
            spreadProgress(sim, row.fraction ?? 0.5);
          }
          seedRunning(sim, row.speed ?? 2 * MiB);
          break;
        case 'queued':
          d.state = 'queued';
          break;
      }
    }
    renumberQueue();
  }

  function mark(id: number): void {
    changed.add(id);
  }

  function touch(d: MDownload): void {
    d.updatedAt = Math.max(now(), d.updatedAt);
    mark(d.id);
  }

  function start(sim: Sim): void {
    const d = sim.d;
    d.state = 'probing';
    d.queuePosition = 0;
    d.error = '';
    d.errorCode = '';
    d.speedBps = 0;
    if (d.startedAt === 0) d.startedAt = now();
    // A web page's video is read again by yt-dlp first ("Getting video info…").
    sim.probeLeftMs = isSite(d.media) ? between(1200, 2400) : between(250, 900);
    sim.slots = [];
    sim.assembleLeftMs = -1;
    d.connectionInfo = [];
    // A stream's size is what its fragments add up to.
    const total = sim.media ? sim.media.totalBytes : d.size;
    if (/404/.test(d.url)) sim.probeError = 'Server responded 404 Not Found';
    if (/changed/.test(d.url) && !sim.scenarioDone && d.received === 0 && !sim.media) sim.changeAt = Math.floor(d.size * 0.3);
    if (/flaky/.test(d.url) && !sim.scenarioDone && d.received === 0) {
      sim.failAt = Math.floor(total * 0.5);
      sim.failCode = 'network';
      sim.failMessage = sim.media ? 'The connection was reset while fetching a fragment' : 'Connection reset by the server';
    } else if (sim.failAt < 0 && d.received === 0 && total > 0 && rng() < failureRate) {
      sim.failAt = Math.floor(total * between(0.1, 0.9));
      sim.failCode = 'network';
      sim.failMessage = 'The connection timed out';
    }
    if (/norange|unknown-size/.test(d.url)) sim.noRange = true;
    if (/unknown-size/.test(d.url) && d.size < 0 && sim.hiddenSize < 0) {
      sim.hiddenSize = Math.floor(between(20 * MiB, 300 * MiB));
    }
    queueDirty = true;
    touch(d);
  }

  /** Credentials for an address: its user info, or a site login with a password for its host. */
  function hasLogin(url: string): boolean {
    let parsed: URL;
    try {
      parsed = new URL(url);
    } catch {
      return false;
    }
    if (parsed.username !== '') return true;
    return settings.siteLogins.some((l) => loginPasswords.has(l.id) && loginCovers(l.urlPattern, parsed.hostname));
  }

  function fail(sim: Sim, code: string, message: string): void {
    const d = sim.d;
    d.state = 'failed';
    d.errorCode = code;
    d.error = message;
    d.speedBps = 0;
    d.connections = 0;
    touch(d);
  }

  function finishProbe(sim: Sim): void {
    const d = sim.d;
    if (sim.media && isSite(d.media)) {
      // The size is known from the formats (Download.size = MediaProgress.BytesTotal).
      d.size = sim.media.totalBytes;
    }
    if (sim.probeError) {
      fail(sim, 'http_status', sim.probeError);
      return;
    }
    if (sim.remoteChanged && d.received > 0) {
      fail(sim, 'validator_changed', 'The file on the server changed since the download started');
      return;
    }
    if (sim.media && /init-protected/.test(d.url)) {
      // MEDIA-SPEC §6.4: CheckInit runs on every init segment before any media fragment.
      fail(sim, 'encrypted', 'An initialization segment is encrypted (it declares content protection)');
      return;
    }
    if (/private|auth-required/.test(d.url) && !hasLogin(d.url)) {
      // M6-SPEC §2: no credentials match the server's challenge.
      fail(sim, 'auth_required', 'The server asks for a user name and password (HTTP 401)');
      return;
    }
    if (/redirect/.test(d.url)) d.finalUrl = d.url.replace(/^https?:\/\/[^/]+/, 'https://edge.example.net');
    d.state = 'downloading';
    if (d.segments.length === 0 && !sim.media) planSegments(sim);
    touch(d);
  }

  function split(d: MDownload): void {
    const active = d.segments.filter((s) => s.received < segmentLength(s, d.size));
    const wanted = d.maxConnections || settings.defaultMaxConnections;
    if (d.mode !== 'ranged' || active.length >= wanted || d.segments.length >= MAX_SEGMENTS) return;
    let best: MSegment | undefined;
    let bestLeft = 0;
    for (const s of active) {
      const left = segmentLength(s, d.size) - s.received;
      if (left > bestLeft) {
        best = s;
        bestLeft = left;
      }
    }
    if (!best || bestLeft < MIN_SPLIT) return;
    const mid = best.start + best.received + Math.floor(bestLeft / 2);
    const tail: MSegment = { start: mid, end: best.end, received: 0 };
    best.end = mid - 1;
    d.segments.splice(d.segments.indexOf(best) + 1, 0, tail);
  }

  /** The connections a run may use: the download's own limit, else the default. */
  function wantedConnections(d: MDownload): number {
    return d.maxConnections || settings.defaultMaxConnections;
  }

  function newSlot(index: number): Slot {
    return {
      index,
      state: 'connecting',
      phaseMs: 300,
      reason: '',
      seg: null,
      last: { start: -1, end: -1, received: 0 },
      speedBps: 0,
    };
  }

  /** Sometimes a receiving connection has to wait and try again. */
  function hiccup(slot: Slot, dtMs: number): void {
    if (slot.state !== 'receiving' || retryRate <= 0) return;
    if (slotRng() >= retryRate * (dtMs / 1000)) return;
    slot.state = 'retrying';
    slot.phaseMs = 2000 + Math.floor(slotRng() * 3) * 1000;
    slot.reason = RETRY_REASONS[Math.floor(slotRng() * RETRY_REASONS.length)] ?? 'HTTP 503';
  }

  /**
   * Gives each unfinished segment a worker slot (at most the wanted connections), moves
   * the slots through their phases and returns the segments that receive data now.
   */
  function runSlots(sim: Sim, active: readonly MSegment[], dtMs: number, lengthOf: (s: MSegment) => number): MSegment[] {
    const d = sim.d;
    const wanted = d.mode === 'ranged' ? wantedConnections(d) : 1;
    for (const slot of sim.slots) {
      if (slot.seg && slot.seg.received >= lengthOf(slot.seg)) {
        slot.last = { start: slot.seg.start, end: slot.seg.end, received: slot.seg.received };
        slot.seg = null;
        slot.state = 'done';
        slot.speedBps = 0;
      }
    }
    const busy = new Set(sim.slots.map((slot) => slot.seg).filter((seg): seg is MSegment => seg !== null));
    for (const seg of active) {
      if (busy.has(seg)) continue;
      let slot = sim.slots.find((x) => x.seg === null && x.index <= wanted);
      if (!slot && sim.slots.length < wanted) {
        slot = newSlot(sim.slots.length + 1);
        sim.slots.push(slot);
      }
      if (!slot) break;
      slot.seg = seg;
      slot.state = 'connecting';
      slot.phaseMs = 300;
      busy.add(seg);
    }
    // A lowered limit stops the extra slots at once (their ranges wait for a free slot).
    for (const slot of sim.slots) {
      if (slot.index > wanted && slot.seg) {
        slot.last = { start: slot.seg.start, end: slot.seg.end, received: slot.seg.received };
        slot.seg = null;
        slot.state = 'idle';
        slot.speedBps = 0;
      }
    }
    sim.slots = sim.slots.filter((slot) => slot.index <= wanted || slot.seg !== null);
    for (const slot of sim.slots) {
      advanceSlot(slot, dtMs);
      hiccup(slot, dtMs);
      if (!slot.seg && slot.state !== 'done') slot.state = 'idle';
    }
    return active.filter((seg) => sim.slots.some((slot) => slot.seg === seg && slot.state === 'receiving'));
  }

  /** Fragments arrive in parallel; when all are done the stream is assembled into one file. */
  function transferMedia(sim: Sim, media: NonNullable<Sim['media']>, budget: number, dtMs: number): void {
    const d = sim.d;
    const info = d.media;
    if (!info) return;
    if (sim.assembleLeftMs >= 0) {
      // All fragments are there: the remux takes a moment, without connections.
      sim.assembleLeftMs -= dtMs;
      if (sim.assembleLeftMs <= 0) {
        sim.assembleLeftMs = -1;
        d.size = d.received;
        d.state = 'completed';
        d.completedAt = now();
        addCompleted(sim);
        touch(d);
      }
      return;
    }
    const before = d.received;
    d.received = Math.min(media.totalBytes, d.received + Math.floor((budget * dtMs) / 1000));
    const instant = ((d.received - before) * 1000) / dtMs;
    d.speedBps = d.speedBps * 0.5 + instant * 0.5;
    const total = info.fragmentsTotal;
    const finished = d.received >= media.totalBytes;
    const done = finished ? total : Math.min(total - 1, Math.floor((total * d.received) / media.totalBytes));
    // A new object per change: emitted downloads are never modified afterwards.
    if (done !== info.fragmentsDone) d.media = { ...info, fragmentsDone: done };
    d.connections = Math.max(0, Math.min(total - done, d.maxConnections || settings.defaultMaxConnections));
    // One slot per connection, each on the next fragment that is not done.
    while (sim.slots.length < d.connections) sim.slots.push(newSlot(sim.slots.length + 1));
    sim.slots = sim.slots.slice(0, d.connections);
    const fragmentBytes = total > 0 ? media.totalBytes / total : 0;
    for (const slot of sim.slots) {
      advanceSlot(slot, dtMs);
      hiccup(slot, dtMs);
    }
    // The receiving connections share the download's speed, so their boxes add up to it.
    const weights = sim.slots.reduce((sum, slot) => sum + (slot.state === 'receiving' ? slotWeight(slot.index) : 0), 0);
    d.connectionInfo = sim.slots.map((slot, i) => {
      slot.speedBps = weights > 0 && slot.state === 'receiving' ? (d.speedBps * slotWeight(slot.index)) / weights : 0;
      const fragment = Math.min(total - 1, done + i);
      const received = Math.floor(fragmentBytes * ((d.received / Math.max(1, fragmentBytes) + i * 0.37) % 1));
      slot.last = { start: 0, end: Math.max(0, Math.round(fragmentBytes) - 1), received };
      return slotInfo(slot, fragment);
    });
    touch(d);
    if (sim.failAt >= 0 && d.received >= sim.failAt) {
      sim.failAt = -1;
      sim.scenarioDone = true;
      fail(sim, sim.failCode, sim.failMessage);
      return;
    }
    if (finished) {
      sim.assembleLeftMs = assembleMs;
      sim.slots = [];
      d.connectionInfo = [];
      d.speedBps = 0;
      d.connections = 0;
      touch(d);
      if (assembleMs <= 0) transferMedia(sim, media, 0, 0);
    }
  }

  function transfer(sim: Sim, budget: number, dtMs: number): void {
    if (sim.media) {
      transferMedia(sim, sim.media, budget, dtMs);
      return;
    }
    const d = sim.d;
    let bytes = Math.floor((budget * dtMs) / 1000);
    // An open-ended segment still ends where the simulated server stops sending.
    const lengthOf = (s: MSegment) => {
      const len = segmentLength(s, d.size);
      return Number.isFinite(len) || sim.hiddenSize < 0 ? len : sim.hiddenSize - s.start;
    };
    const active = d.segments.filter((s) => s.received < lengthOf(s));
    const flowing = runSlots(sim, active, dtMs, lengthOf);
    if (flowing.length > 0 && bytes > 0) {
      const slots = flowing.map((s) => sim.slots.find((x) => x.seg === s));
      const weights = slots.map((slot) => slotWeight(slot?.index ?? 1));
      const sum = weights.reduce((a, b) => a + b, 0);
      const budgetBytes = bytes;
      flowing.forEach((s, i) => {
        const share = Math.ceil((budgetBytes * (weights[i] ?? 1)) / sum);
        const take = Math.min(share, lengthOf(s) - s.received, bytes);
        s.received += take;
        bytes -= take;
        const slot = slots[i];
        if (slot) slot.speedBps = slot.speedBps * 0.5 + ((take * 1000) / dtMs) * 0.5;
      });
    }
    d.connectionInfo = sim.slots.map((slot) => slotInfo(slot, -1));
    const before = d.received;
    d.received = d.segments.reduce((sum, s) => sum + s.received, 0);
    const instant = ((d.received - before) * 1000) / dtMs;
    d.speedBps = d.speedBps * 0.5 + instant * 0.5;
    split(d);
    d.connections = sim.slots.filter((slot) => slot.seg !== null).length;
    touch(d);

    if (sim.changeAt >= 0 && d.received >= sim.changeAt) {
      sim.changeAt = -1;
      sim.scenarioDone = true;
      sim.remoteChanged = true;
      fail(sim, 'validator_changed', 'The file on the server changed while downloading');
      return;
    }
    if (sim.failAt >= 0 && d.received >= sim.failAt) {
      sim.failAt = -1;
      sim.scenarioDone = true;
      fail(sim, sim.failCode, sim.failMessage);
      return;
    }
    if (d.size < 0 && sim.hiddenSize > 0 && d.received >= sim.hiddenSize) {
      d.size = d.received;
      const only = d.segments[0];
      if (only) only.end = d.size - 1;
    }
    if (d.size >= 0 && d.received >= d.size) {
      d.received = d.size;
      d.state = 'completed';
      d.completedAt = now();
      addCompleted(sim);
      d.speedBps = 0;
      d.connections = 0;
      sim.slots = [];
      d.connectionInfo = [];
      touch(d);
    }
  }

  /** The queues in admission order: the main queue first, then by id. */
  function queuesInOrder(): MQueue[] {
    return [...queues.values()].sort((a, b) => Number(b.isMain) - Number(a.isMain) || a.id - b.id);
  }

  /**
   * Admission (M6-SPEC §1): a queued download starts when its queue runs, the queue has
   * fewer than its limit (0 = the global one) running, and the global limit allows it;
   * in the queue's order.
   */
  function schedule(): void {
    let running = 0;
    for (const sim of sims.values()) if (isRunningState(sim.d)) running++;
    for (const q of queuesInOrder()) {
      if (!q.running || running >= settings.maxConcurrentDownloads) continue;
      const cap = q.maxConcurrent || settings.maxConcurrentDownloads;
      let inQueue = q.order.filter((id) => {
        const d = sims.get(id)?.d;
        return d !== undefined && isRunningState(d);
      }).length;
      for (const id of q.order) {
        if (running >= settings.maxConcurrentDownloads || inQueue >= cap) break;
        const sim = sims.get(id);
        if (sim?.d.state === 'queued') {
          start(sim);
          running++;
          inQueue++;
        }
      }
    }
    if (queueDirty) renumberQueue();
  }

  /** queue_position: 1-based among the queued downloads, in admission order. */
  function renumberQueue(): void {
    queueDirty = false;
    let pos = 0;
    const seen = new Set<number>();
    for (const q of queuesInOrder()) {
      for (const id of q.order) {
        const sim = sims.get(id);
        if (!sim) continue;
        seen.add(id);
        const want = sim.d.state === 'queued' ? ++pos : 0;
        if (sim.d.queuePosition !== want) {
          sim.d.queuePosition = want;
          mark(id);
        }
      }
    }
    for (const sim of sims.values()) {
      if (!seen.has(sim.d.id) && sim.d.queuePosition !== 0) {
        sim.d.queuePosition = 0;
        mark(sim.d.id);
      }
    }
  }

  /**
   * runMedia of a web page waits in PROBING for the tools (SITE-MEDIA-SPEC §3.3); it fails
   * when they cannot be set up or video pages were turned off. Reports whether it may go on.
   */
  function siteReady(sim: Sim): boolean {
    if (!settings.siteMedia) {
      fail(sim, 'site_media_off', 'video downloads from web pages are turned off (Settings → Video sites)');
      return false;
    }
    if (toolsPhase === 'ready') return true;
    if (toolsPhase === 'failed') {
      fail(sim, 'tools_unavailable', 'yt-dlp and Deno are not set up: could not download yt-dlp (network)');
      return false;
    }
    startInstall();
    return false;
  }

  function step(dtMs: number): void {
    stepTools(dtMs);
    schedule();
    const transferring: Sim[] = [];
    for (const sim of sims.values()) {
      // The running time excludes pauses and the wait in the queue (Download.active_ms).
      if (sim.d.state === 'probing' || sim.d.state === 'downloading') sim.d.activeMs += dtMs;
      if (sim.d.state === 'probing') {
        if (isSite(sim.d.media) && !siteReady(sim)) continue;
        sim.probeLeftMs -= dtMs;
        if (sim.probeLeftMs <= 0) finishProbe(sim);
      } else if (sim.d.state === 'downloading') {
        transferring.push(sim);
      }
    }
    const wants = transferring.map((sim) => {
      sim.wobble = Math.min(1.35, Math.max(0.55, sim.wobble + (rng() - 0.5) * 0.25));
      const target = sim.baseSpeed * sim.wobble;
      return sim.d.speedLimit > 0 ? Math.min(target, sim.d.speedLimit) : target;
    });
    // Limits nest: download → queue → global (M6-SPEC §1).
    const perQueue = new Map<number, number>();
    transferring.forEach((sim, i) => perQueue.set(sim.d.queueId, (perQueue.get(sim.d.queueId) ?? 0) + (wants[i] ?? 0)));
    transferring.forEach((sim, i) => {
      const limit = queues.get(sim.d.queueId)?.speedLimit ?? 0;
      const sum = perQueue.get(sim.d.queueId) ?? 0;
      if (limit > 0 && sum > limit) wants[i] = ((wants[i] ?? 0) * limit) / sum;
    });
    const total = wants.reduce((a, b) => a + b, 0);
    const scale =
      settings.globalSpeedLimit > 0 && total > settings.globalSpeedLimit
        ? settings.globalSpeedLimit / total
        : 1;
    transferring.forEach((sim, i) => transfer(sim, (wants[i] ?? 0) * scale, dtMs));
    for (const sim of transferring) {
      if (sim.d.state !== 'downloading') queueDirty = true;
      const q = queues.get(sim.d.queueId);
      if (sim.d.state === 'completed' && q?.running) q.completedThisRun = true;
    }
    if (queueDirty) schedule();
    checkFinishedQueues();
  }

  function emit(): void {
    const events: DaemonEvent[] = [];
    for (const id of changed) {
      const sim = sims.get(id);
      if (sim) events.push({ kind: 'updated', download: clone(sim.d) });
    }
    changed.clear();
    for (const id of removed) events.push({ kind: 'removed', id });
    removed.length = 0;
    for (const id of changedQueues) {
      const q = queues.get(id);
      if (q) events.push({ kind: 'queue_updated', queue: queueClone(q) });
    }
    changedQueues.clear();
    for (const id of removedQueues) events.push({ kind: 'queue_removed', id });
    removedQueues.length = 0;
    for (const pending of postEvents) events.push({ kind: 'post_action', pending });
    postEvents.length = 0;
    if (toolsChanged) events.push({ kind: 'tools', status: toolsStatus() });
    toolsChanged = false;
    if (events.length === 0) return;
    for (const cb of [...subscribers]) cb(events);
  }

  function respond<T>(fn: () => T): Promise<T> {
    let value: T;
    try {
      value = fn();
    } catch (err) {
      return latencyMs > 0
        ? new Promise((_, reject) => setTimeout(() => reject(err), latencyMs))
        : Promise.reject(err);
    }
    return latencyMs > 0
      ? new Promise((resolve) => setTimeout(() => resolve(value), latencyMs))
      : Promise.resolve(value);
  }

  function get(id: number): Sim {
    const sim = sims.get(id);
    if (!sim) throw new ApiError('not_found', `download ${id} not found`);
    return sim;
  }

  function checkConnections(n: number | undefined): void {
    if (n === undefined) return;
    if (!Number.isInteger(n) || n < 0 || n > LIMITS.maxConnections) {
      throw new ApiError('invalid_argument', `max_connections must be 0-${LIMITS.maxConnections}`);
    }
  }

  function checkSpeed(n: number | undefined): void {
    if (n === undefined) return;
    if (!Number.isSafeInteger(n) || n < 0) {
      throw new ApiError('invalid_argument', 'speed_limit must be >= 0');
    }
  }

  function addNow(req: AddRequest): Download {
    const url = checkUrl(req.url);
    if (!url.ok) throw new ApiError('invalid_argument', `url ${url.reason}`);
    const method = req.method ?? 'GET';
    if (method !== 'GET' && method !== 'POST') {
      throw new ApiError('invalid_argument', 'method must be GET or POST');
    }
    checkConnections(req.maxConnections);
    checkSpeed(req.speedLimit);
    const queue = targetQueue(req.queueId);
    const filename = req.filename ? sanitizeFilename(req.filename) : nameFromUrl(req.url);
    const category = chooseCategory(req.filename ?? '', req.url, req.mime ?? '');
    const dir = req.dir || categoryFolder(settings, category);
    const dirError = checkDir(dir);
    if (dirError) throw new ApiError('invalid_argument', `dir: ${dirError}`);
    const [lo, hi] = SIZE_RANGE[classifyName(filename)];
    const unknown = /unknown-size/.test(req.url);
    const sizeMarker = /[?&]size=(\d+)/.exec(req.url);
    const hint = req.expectedSize !== undefined && req.expectedSize > 0 ? req.expectedSize : 0;
    const size = sizeMarker ? Number(sizeMarker[1]) : hint || Math.floor(between(lo, hi));
    const awaiting = req.source === 'browser' && settings.confirmBrowserDownloads;
    const d = baseDownload(req.url, filename, dir, unknown || awaiting ? -1 : size, now());
    d.maxConnections = req.maxConnections ?? 0;
    d.speedLimit = req.speedLimit ?? 0;
    d.state = awaiting ? 'awaiting_confirmation' : req.startPaused ? 'paused' : 'queued';
    d.source = req.source ?? 'unspecified';
    d.expectedSize = hint;
    d.mime = req.mime ?? '';
    d.category = category;
    const sim = newSim(d);
    if (unknown && sizeMarker) sim.hiddenSize = size;
    if (awaiting) sim.pendingSize = unknown ? -1 : size;
    sims.set(d.id, sim);
    enqueue(d, queue);
    queueDirty = true;
    mark(d.id);
    if (awaiting) {
      for (const cb of [...confirmSubscribers]) cb(d.id);
    }
    return clone(d);
  }

  /**
   * AddMedia of a web page (origin "site"): the offer of its page (or a fixture's `offer`), the
   * chosen MP4 quality, one .mp4.
   */
  function addSiteNow(req: AddMediaRequest, offer?: MediaInfo): Download {
    if (req.manifestUrl) throw new ApiError('invalid_argument', 'manifest_url: must be empty for a web page');
    const queue = targetQueue(req.queueId);
    // add:slow: the page's offer left the cache, and yt-dlp reads the page again
    if (!offer && now() < addSlowUntil) throw new ApiError('failed_precondition', 'site_resolving: still getting the video info');
    const probed = offer ?? resolvePage(req.pageUrl ?? '');
    const info = withPick(probed, choosePick(probed, req.videoTrack ?? '', req.audioTrack ?? ''));
    if (req.filename) {
      const nameError = checkFilename(req.filename);
      if (nameError) throw new ApiError('invalid_argument', `filename: ${nameError}`);
    }
    const category = mediaCategory(info);
    const dir = req.dir || categoryFolder(settings, category);
    const dirError = checkDir(dir);
    if (dirError) throw new ApiError('invalid_argument', `dir: ${dirError}`);
    const awaiting = req.source === 'browser' && settings.confirmBrowserDownloads;
    const d = baseDownload(probed.pageUrl, mediaFilename(req.filename || probed.title || req.title || 'video'), dir, -1, now());
    d.kind = 'media';
    d.media = info;
    d.category = category;
    d.source = req.source ?? 'unspecified';
    d.state = awaiting ? 'awaiting_confirmation' : req.startPaused ? 'paused' : 'queued';
    const sim = newSim(d);
    sim.media = { probed, totalBytes: mediaBytes(info) };
    d.expectedSize = sim.media.totalBytes;
    sims.set(d.id, sim);
    enqueue(d, queue);
    queueDirty = true;
    mark(d.id);
    if (awaiting) {
      for (const cb of [...confirmSubscribers]) cb(d.id);
    }
    return clone(d);
  }

  function addMediaNow(req: AddMediaRequest): Download {
    if (req.origin === 'site') return addSiteNow(req);
    const queue = targetQueue(req.queueId);
    const probed = probeStream(req.manifestUrl);
    const info = { ...withPick(probed, choosePick(probed, req.videoTrack ?? '', req.audioTrack ?? '')), title: req.title || probed.title };
    if (req.filename) {
      const nameError = checkFilename(req.filename);
      if (nameError) throw new ApiError('invalid_argument', `filename: ${nameError}`);
    }
    const category = mediaCategory(info);
    const dir = req.dir || categoryFolder(settings, category);
    const dirError = checkDir(dir);
    if (dirError) throw new ApiError('invalid_argument', `dir: ${dirError}`);
    const awaiting = req.source === 'browser' && settings.confirmBrowserDownloads;
    const d = baseDownload(req.manifestUrl, mediaFilename(req.filename || info.title), dir, -1, now());
    d.kind = 'media';
    d.media = info;
    d.category = category;
    d.source = req.source ?? 'unspecified';
    d.state = awaiting ? 'awaiting_confirmation' : req.startPaused ? 'paused' : 'queued';
    const sim = newSim(d);
    sim.media = { probed, totalBytes: mediaBytes(info) };
    sims.set(d.id, sim);
    enqueue(d, queue);
    queueDirty = true;
    mark(d.id);
    if (awaiting) {
      for (const cb of [...confirmSubscribers]) cb(d.id);
    }
    return clone(d);
  }

  /** A stream a page might be playing. */
  function mediaHandoffRequest(): AddMediaRequest {
    const n = 1 + Math.floor(rng() * 400);
    const manifest = pick([
      `https://media.example.org/talks/session-${n}/master.m3u8`,
      `https://media.example.org/films/feature-${n}/manifest.mpd`,
      `https://media.example.org/podcasts/episode-${n}/audio.m3u8`,
    ]);
    return {
      manifestUrl: manifest,
      headers: [{ name: 'Referer', value: 'https://media.example.org/watch' }],
      pageUrl: 'https://media.example.org/watch',
      source: 'browser',
    };
  }

  /** What a browser might hand over: a file of some kind, with a size and MIME hint. */
  function handoffRequest(): AddRequest {
    const kind = pick(['video', 'audio', 'archives', 'documents', 'programs'] as const);
    const filename = pick(NAMES[kind]).replace('#', String(1 + Math.floor(rng() * 400)));
    const [lo, hi] = SIZE_RANGE[kind];
    const mime: Record<typeof kind, string> = {
      video: 'video/mp4',
      audio: 'audio/mpeg',
      archives: 'application/zip',
      documents: 'application/pdf',
      programs: 'application/x-msdownload',
    };
    return {
      url: `https://${pick(HOSTS)}/get/${encodeURIComponent(filename)}?token=${Math.floor(rng() * 1e9)}`,
      headers: [{ name: 'Referer', value: `https://${pick(HOSTS)}/` }],
      filename,
      source: 'browser',
      expectedSize: Math.floor(between(lo, hi)),
      mime: mime[kind],
    };
  }

  function checkCategories(next: Settings): void {
    const result = validateCategories(
      next.categories.map((c, i) => ({ key: i, name: c.name, extensions: c.extensions.join(' '), dir: c.dir })),
    );
    if (result.ok) {
      const same = result.categories.every(
        (c, i) => c.name === next.categories[i]?.name && c.extensions.join(' ') === next.categories[i]?.extensions.join(' '),
      );
      if (same) return;
    }
    throw new ApiError('invalid_argument', 'categories: names must be unique and non-empty, extensions [a-z0-9]{1,16}, folders absolute');
  }

  newQueue({ name: 'Main queue', maxConcurrent: 0, speedLimit: 0, schedule: EMPTY_SCHEDULE, postAction: 'none' }, true);
  newQueue({
    name: 'Night',
    maxConcurrent: 2,
    speedLimit: 0,
    schedule: { ...EMPTY_SCHEDULE, enabled: true, startMinute: 60, stopEnabled: true, stopMinute: 420, weekdays: 0x7f },
    postAction: 'none',
  });
  /**
   * A layout fixture (MockOptions.fixture): comma-separated tokens on top of the scene, for the
   * dialog states scripts/measure-dialogs.mjs lays out.
   * - confirm:<file|site|hls|dash|muxed|audio>: one download awaits confirmation and its dialog
   *   opens by itself; file: the scene's budget-plan.xlsx; otherwise a browser hand-off (the only
   *   one waiting): a YouTube page's video with q:<1|3|6|8> qualities (6), audio:<1-3> tracks (2),
   *   title:<plain|long|ku|ar|none> and url:long (a playlist address with tracking parameters), or
   *   an HLS or DASH stream (muxed: HLS variants with their sound; audio: an audio-only stream);
   *   with confirm:file, url:long gives the file a signed storage address (about 600 characters);
   * - waiting:<n>: n downloads await confirmation in all (newer file hand-offs);
   * - fail: downexd refuses every confirmation (not enough disk space);
   * - queues:<n>: n queues in all (1: the main queue only; more: stopped ones);
   * - main:<n>, night:<n>: n more queued downloads in the main queue or the second queue;
   * - gone:<name>: the file of that completed download is no longer in its folder;
   * - settings:full: a manual proxy with a password, host rules and site logins;
   * - tools:ready: yt-dlp and Deno are installed; tools:slow: their download goes on for ten minutes;
   * - resolve:slow: yt-dlp reads each page for ten minutes ("Getting video info…");
   * - add:slow: adding a page's video waits for yt-dlp for ten minutes (finding its qualities does not).
   */
  function applyFixture(spec: string): void {
    const tokens = spec
      .split(',')
      .map((t) => t.trim())
      .filter(Boolean);
    const KNOWN = ['confirm', 'q', 'audio', 'title', 'url', 'waiting', 'fail', 'queues', 'main', 'night', 'gone', 'settings', 'tools', 'resolve', 'add'];
    for (const t of tokens) {
      if (!KNOWN.includes(t.split(':')[0] ?? '')) throw new Error(`fixture: unknown token "${t}"`);
    }
    const values = (name: string): string[] => tokens.filter((t) => t.startsWith(`${name}:`)).map((t) => t.slice(name.length + 1));
    const value = (name: string): string | undefined => values(name)[0];
    const count = (name: string, fallback: number): number => {
      const v = value(name);
      const n = v === undefined ? fallback : Number(v);
      if (!Number.isSafeInteger(n) || n < 0) throw new Error(`fixture: ${name} needs a count`);
      return n;
    };

    if (value('settings') === 'full') {
      settings = {
        ...settings,
        proxy: {
          ...settings.proxy,
          mode: 'manual',
          httpProxy: 'proxy.corp.example:8080',
          httpsProxy: 'proxy.corp.example:8443',
          bypass: '<local>;*.corp.example;10.*',
          username: 'downex',
        },
        hostRules: [
          { pattern: '*.googlevideo.com', maxConnections: 4, speedLimit: 0, maxRequestBytes: 10 * MiB },
          { pattern: 'mirror.example.org', maxConnections: 16, speedLimit: 8_000_000, maxRequestBytes: 0 },
          { pattern: '*.example.net', maxConnections: 2, speedLimit: 512_000, maxRequestBytes: 0 },
        ],
        siteLogins: [
          { id: 'login-1', urlPattern: 'https://files.example.com/*', username: 'zana', hasPassword: false },
          { id: 'login-2', urlPattern: '*.intranet.example', username: 'build-bot', hasPassword: false },
        ],
      };
      proxyPassword = true;
      loginPasswords.add('login-1');
      loginPasswords.add('login-2');
      nextLogin = 3;
    }
    if (value('tools') === 'ready') {
      toolsPhase = 'ready';
      toolsCheckedAt = now();
    }
    if (value('tools') === 'slow') toolsInstallMs = 600_000;
    if (value('resolve') === 'slow') resolveMs = 600_000;
    if (value('add') === 'slow') addSlowUntil = now() + 600_000;

    const queueCount = count('queues', 2);
    if (queueCount < 1) throw new Error('fixture: queues needs at least 1');
    if (queueCount === 1) {
      for (const q of [...queues.values()]) if (!q.isMain) queues.delete(q.id);
    }
    const extra = ['Weekend', 'Videos to watch', 'Work files', 'Overnight ISOs', 'دروس الفيديو', 'Курсы', 'Podcasts', 'Photo backups', 'Big files (night only)', 'Game updates', 'Linux images', 'Research papers'];
    for (let i = queues.size; i < queueCount; i++) {
      newQueue({ name: extra[(i - 2) % extra.length] ?? `Queue ${i + 1}`, maxConcurrent: 0, speedLimit: 0, schedule: EMPTY_SCHEDULE, postAction: 'none' });
    }

    /** More queued downloads at the end of a queue. */
    const fill = (q: MQueue | undefined, n: number): void => {
      if (!q) return;
      const kinds = ['video', 'archives', 'programs', 'documents', 'audio'] as const;
      for (let i = 0; i < n; i++) {
        const kind = kinds[i % kinds.length] ?? 'video';
        const filename = pick(NAMES[kind]).replace('#', String(100 + i));
        const [lo, hi] = SIZE_RANGE[kind];
        const url = `https://${pick(HOSTS)}/pub/${encodeURIComponent(filename)}`;
        const category = chooseCategory(filename, url, '');
        const d = baseDownload(url, filename, categoryFolder(settings, category), Math.floor(between(lo, hi)), now() - (n - i) * 60_000);
        d.category = category;
        sims.set(d.id, newSim(d));
        enqueue(d, q);
      }
    };
    fill(queues.get(MAIN_QUEUE_ID), count('main', 0));
    fill([...queues.values()].find((q) => !q.isMain), count('night', 0));

    for (const name of values('gone')) {
      const sim = [...sims.values()].find((s) => s.d.filename === name && s.d.state === 'completed');
      if (!sim) throw new Error(`fixture: no completed download "${name}"`);
      gone.add(sim.d.id);
    }
    if (tokens.includes('fail')) {
      confirmRefusal = new ApiError(
        'resource_exhausted',
        'disk_full: not enough free space on the destination disk for this download (it needs 1.5 GB, 327 MB are free)',
      );
    }

    const kind = value('confirm');
    if (kind === 'file' && value('url') === 'long') {
      // a signed storage link, as Drive, S3 and CDNs hand them to the browser
      const sim = [...sims.values()].find((s) => s.d.state === 'awaiting_confirmation');
      if (!sim) throw new Error('fixture: no download awaits confirmation');
      const signature = 'a3f9c27e8b4d6015f2e9ac7b38d4e6f1'.repeat(8);
      sim.d.url =
        `https://storage.example-cdn.net/prod-eu-west-3/uploads/2026/10/29/${encodeURIComponent(sim.d.filename)}` +
        '?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=AKIAEXAMPLE7Q3NV2XZD%2F20261029%2Feu-west-3%2Fs3%2Faws4_request' +
        '&X-Amz-Date=20261029T081512Z&X-Amz-Expires=3600&X-Amz-SignedHeaders=host' +
        `&response-content-disposition=attachment%3B%20filename%3D%22${encodeURIComponent(sim.d.filename)}%22` +
        `&X-Amz-Signature=${signature}`;
    }
    if (kind !== undefined && kind !== 'file') {
      // the hand-off is the only download waiting: the scene's own goes on as queued
      for (const sim of sims.values()) {
        if (sim.d.state !== 'awaiting_confirmation') continue;
        sim.d.state = 'queued';
        sim.d.size = sim.pendingSize;
      }
      if (kind === 'site') {
        const page = value('url') === 'long'
          ? 'https://www.youtube.com/watch?v=Xb7mQ2kLp9w&list=PLx0sYbCqOb8TBPRdmBHs5Iftvv9TPboYG&index=7&pp=iAQBsAgCiAQB&t=42s'
          : 'https://www.youtube.com/watch?v=Xb7mQ2kLp9w';
        const title = FIXTURE_TITLES[value('title') ?? 'plain'];
        if (title === undefined) throw new Error(`fixture: no title "${value('title') ?? ''}"`);
        const offer = fixtureOffer({ pageUrl: page, qualities: count('q', 6), audio: count('audio', 2), title });
        addSiteNow({ manifestUrl: '', pageUrl: page, origin: 'site', source: 'browser', headers: [] }, offer);
      } else {
        const manifest = {
          hls: 'https://media.example.org/talks/platform-engineering-day-2026/master.m3u8',
          dash: 'https://media.example.org/films/coastline/manifest.mpd',
          muxed: 'https://media.example.org/shows/evening-news/muxed/index.m3u8',
          audio: 'https://media.example.org/podcasts/episode-212/audio.m3u8',
        }[kind];
        if (!manifest) throw new Error(`fixture: no confirm kind "${kind}"`);
        addMediaNow({ manifestUrl: manifest, pageUrl: 'https://media.example.org/watch', headers: [{ name: 'Referer', value: 'https://media.example.org/watch' }], source: 'browser' });
      }
    }
    const waiting = [...sims.values()].filter((s) => s.d.state === 'awaiting_confirmation').length;
    for (let i = waiting; i < count('waiting', 0); i++) addNow(handoffRequest());
    renumberQueue();
  }

  if ((options.scene ?? (options.count === undefined ? 'studio' : 'random')) === 'studio') seedStudio();
  else seed(options.count ?? SCENE.length);
  if (options.fixture) applyFixture(options.fixture);
  changedQueues.clear(); // the state at start is what the first read sees

  /** Settings as getSettings reports them: with whether passwords are stored. */
  function settingsView(): Settings {
    return {
      ...settings,
      proxy: { ...settings.proxy, hasPassword: proxyPassword },
      siteLogins: settings.siteLogins.map((l) => ({ ...l, hasPassword: loginPasswords.has(l.id) })),
    };
  }

  function updateInfo(): UpdateInfo {
    if (!settings.updateFeedUrl) {
      throw new ApiError('failed_precondition', 'updates are off: set the address of an update feed first');
    }
    return {
      currentVersion: '0.0.0-mock',
      latestVersion: '0.0.1-mock',
      available: true,
      notes: 'A simulated update: queues, schedules and site logins.',
      publishedAt: startedAt - 86_400_000,
      checkedAt: now(),
    };
  }

  const api: MockApi = {
    kind: 'mock',

    windowState,

    window: {
      setTitle(title: string) {
        windowState.title = title;
        if (typeof document !== 'undefined') document.title = title;
      },
      setSize(width: number, height: number) {
        windowState.size = { width, height };
        windowState.sizes.push({ width, height });
      },
      getSize: async () => ({ ...windowState.size }),
      close() {
        windowState.closed = true;
        options.onCloseWindow?.();
      },
      // A browser page cannot move its window: the simulated one only records what was asked.
      minimise() {
        windowState.minimised++;
      },
      toggleMaximise() {
        windowState.maximised = !windowState.maximised;
      },
      isMaximised: async () => windowState.maximised,
    },

    freeSpace: (dir: string) =>
      respond(() => {
        // As the host: an absolute folder (a drive, a root path or a network share), never echoed.
        if (!/^([A-Za-z]:[\\/]|[\\/])/.test(dir.trim())) {
          throw new ApiError('invalid_argument', 'dir must be an absolute path');
        }
        return Math.max(0, freeAtStart - completedBytes);
      }),

    list: () => respond(() => [...sims.values()].map((s) => clone(s.d))),

    get: (id: number) => respond(() => clone(get(id).d)),

    startup: () =>
      respond(
        (): StartupView => {
          // The demo's own build, also under a caller's view that names none.
          const build = {
            version: '0.0.0-mock',
            os: sep === '\\' ? 'windows' : 'linux',
            arch: 'amd64',
            goVersion: 'go1.27.0',
            wailsVersion: 'v2.17.0',
            commit: '',
            commitTime: 0,
            modified: false,
          };
          if (options.view) return { ...options.view, build: options.view.build ?? build };
          return { view: 'main', openWith: true, hibernate: true, wakeTimers: true, build };
        },
      ),

    openProgressWindow: (id: number) =>
      respond(() => {
        get(id);
        windowState.opened.push(id);
        options.onOpenProgressWindow?.(id);
      }),

    setDownloadOptions: (id: number, opts: DownloadOptions) =>
      respond(() => {
        const d = get(id).d;
        checkConnections(opts.maxConnections);
        checkSpeed(opts.speedLimit);
        // As downexd: a completed download takes no options.
        if (d.state === 'completed') throw new ApiError('failed_precondition', `download ${id} is already completed`);
        if (opts.speedLimit !== undefined) d.speedLimit = opts.speedLimit;
        if (opts.maxConnections !== undefined) d.maxConnections = opts.maxConnections;
        // Only ever set: a download whose progress window was opened keeps the dialog.
        if (opts.completionDialog) d.completionDialog = true;
        touch(d);
        return clone(d);
      }),

    add: (req: AddRequest) => respond(() => addNow(req)),

    addMedia: (req: AddMediaRequest) => respond(() => addMediaNow(req)),

    probeMedia: (req) =>
      respond(() => {
        if (req.origin !== 'site') return probeStream(req.manifestUrl);
        if (req.manifestUrl) throw new ApiError('invalid_argument', 'manifest_url: must be empty for a web page');
        return resolvePage(req.pageUrl ?? '');
      }),

    getTools: () => respond(() => toolsStatus()),

    installTools: (update: boolean) => {
      // The user's install ignores the backoff of automatic ones; an update of ready copies
      // only looks for newer releases (there are none).
      if (!settings.siteMedia) {
        return respond((): ToolsStatus => {
          throw siteMediaOff();
        });
      }
      if (toolsPhase === 'ready') {
        if (update) {
          toolsCheckedAt = now();
          toolsChanged = true;
        }
        return respond(() => toolsStatus());
      }
      if (toolsPhase === 'failed') toolsPhase = 'missing';
      const phase = startInstall();
      if (phase === 'ready') return respond(() => toolsStatus());
      if (phase === 'failed') {
        return respond((): ToolsStatus => {
          throw toolsUnavailable();
        });
      }
      return new Promise<ToolsStatus>((resolve, reject) => toolsWaiters.push({ resolve, reject }));
    },

    openExternal: (url: string) =>
      respond(() => {
        if (!/^https:\/\/[^/]/i.test(url)) throw new ApiError('invalid_argument', 'not an https address');
        if (typeof window !== 'undefined' && options.autoTick !== false) window.open(url, '_blank', 'noopener,noreferrer');
      }),

    confirm: (id: number, opts: ConfirmOptions) =>
      respond(() => {
        const sim = get(id);
        const d = sim.d;
        if (d.state !== 'awaiting_confirmation') {
          throw new ApiError('failed_precondition', `download ${id} is not awaiting confirmation`);
        }
        if (confirmRefusal) throw confirmRefusal;
        checkConnections(opts.maxConnections);
        checkSpeed(opts.speedLimit);
        const dir = opts.dir || d.dir;
        const dirError = checkDir(dir) ?? (dir === '' ? 'must be absolute' : null);
        if (dirError) throw new ApiError('invalid_argument', `dir: ${dirError}`);
        if (opts.filename) {
          const nameError = checkFilename(opts.filename);
          if (nameError) throw new ApiError('invalid_argument', `filename: ${nameError}`);
          d.filename = sanitizeFilename(opts.filename);
        }
        if (sim.media && d.media) {
          // Empty = keep the daemon's pick (a muxed video then takes no audio track). A web
          // page's video stays one .mp4 whatever is chosen: its name keeps its extension.
          const chosen = choosePick(
            sim.media.probed,
            opts.videoTrack || d.media.selectedVideo,
            opts.audioTrack || d.media.selectedAudio,
          );
          d.media = { ...withPick(sim.media.probed, chosen), title: d.media.title };
          d.category = d.category || mediaCategory(d.media);
          sim.media.totalBytes = mediaBytes(d.media);
          if (isSite(d.media)) d.expectedSize = sim.media.totalBytes;
        } else if (opts.videoTrack || opts.audioTrack) {
          throw new ApiError('invalid_argument', `download ${id} is not a stream: it has no tracks`);
        }
        const queue = opts.queueId ? getQueue(opts.queueId) : null;
        d.dir = dir;
        if (opts.maxConnections !== undefined) d.maxConnections = opts.maxConnections;
        if (opts.speedLimit !== undefined) d.speedLimit = opts.speedLimit;
        d.size = sim.pendingSize;
        if (queue) enqueue(d, queue);
        // As downexd: a named queue takes it queued (it waits there until the queue runs,
        // M6-SPEC §1); the window names a running one only to start it.
        d.state = opts.start || queue !== null ? 'queued' : 'paused';
        queueDirty = true;
        touch(d);
        return clone(d);
      }),

    pauseAll: () =>
      respond(() => {
        let n = 0;
        for (const sim of sims.values()) {
          const d = sim.d;
          if (d.state !== 'queued' && d.state !== 'probing' && d.state !== 'downloading') continue;
          d.state = 'paused';
          d.speedBps = 0;
          d.connections = 0;
          d.queuePosition = 0;
          touch(d);
          n++;
        }
        queueDirty = true;
        return n;
      }),

    resumeAll: () =>
      respond(() => {
        let n = 0;
        for (const sim of sims.values()) {
          const d = sim.d;
          if (d.state !== 'paused') continue; // as the daemon: paused downloads only
          d.state = 'queued';
          touch(d);
          n++;
        }
        queueDirty = true;
        return n;
      }),

    pause: (id: number) =>
      respond(() => {
        const sim = get(id);
        const d = sim.d;
        if (d.state === 'paused') return clone(d);
        if (d.state !== 'queued' && d.state !== 'probing' && d.state !== 'downloading') {
          throw new ApiError('failed_precondition', `download ${id} is ${d.state}`);
        }
        d.state = 'paused';
        d.speedBps = 0;
        d.connections = 0;
        d.queuePosition = 0;
        queueDirty = true;
        touch(d);
        return clone(d);
      }),

    resume: (id: number, opts: ResumeOptions = {}) =>
      respond(() => {
        const sim = get(id);
        const d = sim.d;
        if (opts.existingFile && !opts.restart) {
          throw new ApiError('invalid_argument', 'existing_file is only for a restart');
        }
        const again = d.state === 'completed' && opts.restart === true;
        if (d.state === 'completed' && !again) {
          throw new ApiError('failed_precondition', `download ${id} is already completed`);
        }
        if (!again && !isResumable(d.state)) {
          throw new ApiError('failed_precondition', `download ${id} is ${d.state}`);
        }
        checkConnections(opts.maxConnections);
        checkSpeed(opts.speedLimit);
        if (opts.maxConnections !== undefined) d.maxConnections = opts.maxConnections;
        if (opts.speedLimit !== undefined) d.speedLimit = opts.speedLimit;
        if (again) {
          // As downexd (ENGINE-SPEC §7.1): replace keeps the name and frees the old copy's
          // space at completion; keep both saves under a free name; a file that is gone is
          // downloaded again under its own name.
          const there = !gone.has(id);
          gone.delete(id);
          const replace = there && opts.existingFile === 'replace';
          sim.replaceBytes = replace ? Math.max(0, d.size) : -1;
          if (there && !replace) d.filename = freeVariant(d);
          d.completedAt = 0;
          sim.scenarioDone = false;
        }
        if (opts.restart) {
          d.received = 0;
          d.segments = [];
          d.mode = 'unspecified';
          if (d.media) {
            d.media = { ...d.media, fragmentsDone: 0 };
            d.size = -1;
          }
          sim.remoteChanged = false;
          sim.changeAt = -1;
          if (d.size >= 0 && sim.hiddenSize > 0) d.size = -1;
        }
        d.state = 'queued';
        d.error = '';
        d.errorCode = '';
        queueDirty = true;
        touch(d);
        return clone(d);
      }),

    remove: (id: number, _opts: RemoveOptions) =>
      respond(() => {
        const d = get(id).d;
        const q = queues.get(d.queueId);
        if (q) {
          q.order = q.order.filter((x) => x !== id);
          changedQueues.add(q.id);
        }
        sims.delete(id);
        changed.delete(id);
        removed.push(id);
        queueDirty = true;
      }),

    getSettings: () => respond(() => settingsView()),

    updateSettings: (next: Settings) =>
      respond(() => {
        const { maxConcurrentDownloads: c, defaultMaxConnections: m, globalSpeedLimit: g } = next;
        if (!Number.isInteger(c) || c < LIMITS.minConcurrent || c > LIMITS.maxConcurrent) {
          throw new ApiError('invalid_argument', 'max_concurrent_downloads must be 1-16');
        }
        if (!Number.isInteger(m) || m < LIMITS.minConnections || m > LIMITS.maxConnections) {
          throw new ApiError('invalid_argument', 'default_max_connections must be 1-32');
        }
        checkSpeed(g);
        const dirError = next.defaultDir ? checkDir(next.defaultDir) : 'must be absolute';
        if (dirError) throw new ApiError('invalid_argument', `default_dir: ${dirError}`);
        checkCategories(next);
        if (next.updateFeedUrl && !/^https:\/\/[^/]/i.test(next.updateFeedUrl)) {
          throw new ApiError('invalid_argument', 'update_feed_url: must be an https address');
        }
        if (next.avScanEnabled && !/^([A-Za-z]:[\\/]|\/)/.test(next.avScanCommand)) {
          throw new ApiError('invalid_argument', 'av_scan_command: must be an absolute path');
        }
        // Site logins get their ids here; passwords of removed ones are dropped.
        const siteLogins = next.siteLogins.map((l) => ({ ...l, id: l.id || `login-${nextLogin++}` }));
        for (const id of [...loginPasswords]) if (!siteLogins.some((l) => l.id === id)) loginPasswords.delete(id);
        settings = {
          ...next,
          categories: next.categories.map((c) => ({ ...c, extensions: [...c.extensions] })),
          proxy: { ...next.proxy },
          hostRules: next.hostRules.map((h) => ({ ...h })),
          siteLogins,
        };
        return settingsView();
      }),

    pickFile: (opts = {}) =>
      respond(() => `${opts.initialDir || settings.defaultDir}${sep}scanner${sep === '\\' ? '.exe' : ''}`),

    listQueues: () => respond(() => queuesInOrder().map(queueClone)),

    createQueue: (spec: QueueSpec) =>
      respond(() => {
        checkQueueSpec(spec, null);
        return queueClone(newQueue({ ...spec, name: spec.name.trim() }));
      }),

    updateQueue: (id: number, spec: QueueSpec) =>
      respond(() => {
        const q = getQueue(id);
        checkQueueSpec(spec, id);
        q.name = spec.name.trim();
        q.maxConcurrent = spec.maxConcurrent;
        q.speedLimit = spec.speedLimit;
        q.schedule = { ...spec.schedule };
        q.postAction = spec.postAction;
        changedQueues.add(id);
        queueDirty = true;
        return queueClone(q);
      }),

    deleteQueue: (id: number) =>
      respond(() => {
        const q = getQueue(id);
        if (q.isMain) throw new ApiError('failed_precondition', 'the main queue cannot be deleted');
        const main = getQueue(MAIN_QUEUE_ID);
        for (const did of q.order) {
          const sim = sims.get(did);
          if (!sim) continue;
          main.order.push(did);
          sim.d.queueId = main.id;
          touch(sim.d);
        }
        queues.delete(id);
        countdowns.delete(id);
        changedQueues.delete(id);
        changedQueues.add(main.id);
        removedQueues.push(id);
        queueDirty = true;
      }),

    startQueue: (id: number) =>
      respond(() => {
        const q = getQueue(id);
        if (!q.running) {
          q.running = true;
          q.completedThisRun = false;
          changedQueues.add(id);
          queueDirty = true;
        }
        return queueClone(q);
      }),

    stopQueue: (id: number) =>
      respond(() => {
        const q = getQueue(id);
        q.running = false;
        // Its running downloads stop (with what they received) and wait in the queue.
        for (const did of q.order) {
          const sim = sims.get(did);
          if (!sim || !isRunningState(sim.d)) continue;
          sim.d.state = 'queued';
          sim.d.speedBps = 0;
          sim.d.connections = 0;
          sim.slots = [];
          sim.d.connectionInfo = [];
          touch(sim.d);
        }
        changedQueues.add(id);
        queueDirty = true;
        return queueClone(q);
      }),

    moveDownloads: (ids: readonly number[], queueId: number, position: number) =>
      respond(() => {
        const target = getQueue(queueId);
        const moving = ids.filter((id) => sims.has(id));
        if (moving.length !== ids.length) throw new ApiError('not_found', 'a download to move does not exist');
        for (const q of queues.values()) {
          const kept = q.order.filter((id) => !moving.includes(id));
          if (kept.length !== q.order.length) {
            q.order = kept;
            changedQueues.add(q.id);
          }
        }
        const at = position < 0 || position > target.order.length ? target.order.length : position;
        target.order.splice(at, 0, ...moving);
        for (const id of moving) {
          const d = (sims.get(id) as Sim).d;
          if (d.queueId !== target.id) {
            d.queueId = target.id;
            touch(d);
          }
        }
        changedQueues.add(target.id);
        queueDirty = true;
        return queueClone(target);
      }),

    cancelPostAction: (queueId: number) =>
      respond(() => {
        getQueue(queueId);
        const c = countdowns.get(queueId);
        if (!c) return;
        countdowns.delete(queueId);
        postEvents.push({ queueId, action: c.action, deadline: c.deadline, cancelled: true });
      }),

    setSecret: (kind, id, password) =>
      respond(() => {
        if (password === '') throw new ApiError('invalid_argument', 'password: must not be empty');
        if (kind === 'proxy') proxyPassword = true;
        else if (settings.siteLogins.some((l) => l.id === id)) loginPasswords.add(id);
        else throw new ApiError('not_found', `site login ${id} not found`);
      }),

    clearSecret: (kind, id) =>
      respond(() => {
        if (kind === 'proxy') proxyPassword = false;
        else loginPasswords.delete(id);
      }),

    checkForUpdate: () => respond(() => updateInfo()),

    // The simulated daemon has nothing to install: it answers as if the installer started.
    installUpdate: () => respond(() => updateInfo()),

    status: () =>
      respond((): Status => {
        let active = 0;
        let queued = 0;
        for (const sim of sims.values()) {
          if (sim.d.state === 'downloading' || sim.d.state === 'probing') active++;
          else if (sim.d.state === 'queued') queued++;
        }
        return { version: '0.0.0-mock', pid: 4242, startedAt, active, queued, endpoint: 'mock://downexd', ...mockFolders(), updatesSupported: false };
      }),

    setTheme: (theme: ThemeChoice) =>
      respond(() => {
        if (!(THEME_CHOICES as readonly string[]).includes(theme)) {
          throw new ApiError('invalid_argument', 'theme must be "system", "light" or "dark"');
        }
        windowState.themes.push(theme);
      }),

    fileExists: (id: number) =>
      respond(() => {
        const sim = get(id);
        // The file is there once the download completed, also while it is downloaded again to replace it.
        return !gone.has(id) && (sim.d.state === 'completed' || sim.replaceBytes >= 0);
      }),

    openAppFolder: (folder: AppFolder) =>
      respond(() => {
        if (!(APP_FOLDERS as readonly string[]).includes(folder)) {
          throw new ApiError('invalid_argument', 'kind must be "data", "logs", "tools" or "downloads"');
        }
        windowState.folders.push(folder);
      }),

    diagnostics: (opts: { readonly webview?: string } = {}) =>
      respond(() => {
        const webview = opts.webview ?? '';
        if (webview !== '' && (webview.length > 32 || !/^[0-9]+(\.[0-9]+){0,3}$/.test(webview))) {
          throw new ApiError('invalid_argument', 'webview must be a version such as 141.0.3537.71');
        }
        return mockDiagnostics(webview);
      }),

    thirdPartyNotices: () =>
      respond(
        () =>
          'THIRD-PARTY NOTICES (demo)\n\nThe desktop window shows the licences of every Go module and npm package that ' +
          'Downex is built from here (THIRD-PARTY-NOTICES.txt). Programs Downex downloads separately: yt-dlp ' +
          '(The Unlicense) and Deno (MIT License).\n',
      ),

    openFile: (id: number) =>
      respond(() => {
        const d = get(id).d;
        if (d.state !== 'completed') {
          throw new ApiError('failed_precondition', `download ${id} is not completed`);
        }
      }),

    openFolder: (id: number) =>
      respond(() => {
        get(id);
      }),

    // A browser has no "Open with" dialog: like the host, the checks pass and nothing opens.
    openWith: (id: number) =>
      respond(() => {
        const d = get(id).d;
        if (d.state !== 'completed') {
          throw new ApiError('failed_precondition', `download ${id} is not completed`);
        }
      }),

    pickFolder: (initialDir?: string) =>
      respond(() => `${initialDir || settings.defaultDir}${sep}Picked`),

    copyText: async (text: string) => {
      await navigator.clipboard?.writeText(text);
    },

    onEvents(cb: (batch: EventBatch) => void): Unsubscribe {
      subscribers.add(cb);
      queueMicrotask(() => {
        if (!subscribers.has(cb)) return;
        // As Watch without ids: every download, then the tools' state.
        const snapshot: DaemonEvent[] = [...sims.values()].map((s) => ({ kind: 'snapshot', download: clone(s.d) }));
        snapshot.push({ kind: 'tools', status: toolsStatus() });
        cb(snapshot);
      });
      return () => {
        subscribers.delete(cb);
      };
    },

    onConfirmRequest(cb: (id: number) => void): Unsubscribe {
      confirmSubscribers.add(cb);
      return () => {
        confirmSubscribers.delete(cb);
      };
    },

    // A browser page has no menu bar: no commands arrive.
    onMenuCommand(): Unsubscribe {
      return () => {};
    },

    tick(ms = tickMs) {
      step(ms);
      emit();
    },

    browserHandoff(req: Partial<AddRequest> = {}) {
      return addNow({ ...handoffRequest(), ...req, source: req.source ?? 'browser' });
    },

    browserMediaHandoff(req: Partial<AddMediaRequest> = {}) {
      return addMediaNow({ ...mediaHandoffRequest(), ...req, source: req.source ?? 'browser' });
    },

    browserVideoHandoff(req: Partial<AddMediaRequest> = {}) {
      const n = 1 + Math.floor(rng() * 400);
      return addMediaNow({
        manifestUrl: '',
        pageUrl: `https://www.youtube.com/watch?v=dQw4w9WgX${String(n % 10)}Q`,
        origin: 'site',
        ...req,
        source: req.source ?? 'browser',
      });
    },

    deleteFile(id: number) {
      get(id);
      gone.add(id);
    },

    dispose() {
      toolsWaiters = [];
      if (timer !== undefined) clearInterval(timer);
      timer = undefined;
      clearTimeout(handoffTimer);
      handoffTimer = undefined;
      subscribers.clear();
      confirmSubscribers.clear();
    },
  };

  if (options.autoTick ?? true) {
    timer = setInterval(() => api.tick(tickMs), tickMs);
    const every = options.handoffEveryMs ?? (options.fixture ? 0 : 90_000);
    if (every > 0) {
      let handoffs = 0;
      const next = (ms: number) => {
        handoffTimer = setTimeout(() => {
          if (handoffs++ % 2 === 1) api.browserMediaHandoff();
          else api.browserHandoff();
          next(every);
        }, ms);
      };
      next(Math.min(6000, every));
    }
  }
  return api;
}
