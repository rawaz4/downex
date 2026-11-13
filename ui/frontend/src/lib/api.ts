// Typed contract between the UI and the Downex daemon (downexd), mirroring
// proto/downex/v1/downex.proto. Implementations: ./mock.ts (simulated daemon for
// `pnpm dev` and tests) and ./wails.ts (Wails v2 Go bindings). ./backend.ts picks one.
//
// UI-side representation (normalised once at the adapter boundary):
// - int64 values are JS numbers (byte counts and ids stay far below 2^53);
// - enums are lowercase string unions without the proto prefix;
// - timestamps are epoch milliseconds, 0 = not set.

export type DownloadState =
  | 'unspecified'
  | 'queued'
  | 'probing'
  | 'downloading'
  | 'paused'
  | 'completed'
  | 'failed'
  | 'canceled'
  | 'awaiting_confirmation';

/** Indexed by the proto enum number. */
export const DOWNLOAD_STATES: readonly DownloadState[] = [
  'unspecified',
  'queued',
  'probing',
  'downloading',
  'paused',
  'completed',
  'failed',
  'canceled',
  'awaiting_confirmation',
];

export type Mode = 'unspecified' | 'ranged' | 'single';

export const MODES: readonly Mode[] = ['unspecified', 'ranged', 'single'];

/**
 * Who asked for a download (downex.v1.AddSource); browser hand-offs and links seen on the
 * clipboard (M6) may need confirming.
 */
export type AddSource = 'unspecified' | 'cli' | 'ui' | 'browser' | 'clipboard';

/** Indexed by the proto enum number. */
export const ADD_SOURCES: readonly AddSource[] = ['unspecified', 'cli', 'ui', 'browser', 'clipboard'];

/** The id of the main queue (M6-SPEC §1): it always exists and cannot be deleted. */
export const MAIN_QUEUE_ID = 1;

/**
 * What happens after a queue finished its downloads (downex.v1.PostAction), after a
 * cancellable countdown. The proto's UNSPECIFIED (nothing) is 'none'.
 */
export type PostAction = 'none' | 'quit_downex' | 'sleep' | 'hibernate' | 'shutdown';

/** Indexed by the proto enum number. */
export const POST_ACTIONS: readonly PostAction[] = ['none', 'quit_downex', 'sleep', 'hibernate', 'shutdown'];

/**
 * When a queue runs, in local time (downex.v1.Schedule): from startMinute on the days of
 * `weekdays` (bit 0 = Monday … bit 6 = Sunday), or once on `onceDate` when weekdays is 0,
 * until stopMinute when stopEnabled (earlier than the start = the next day).
 */
export interface Schedule {
  readonly enabled: boolean;
  /** Minutes after local midnight, 0–1439. */
  readonly startMinute: number;
  readonly stopEnabled: boolean;
  readonly stopMinute: number;
  readonly weekdays: number;
  /** "YYYY-MM-DD" (local), used when weekdays is 0. */
  readonly onceDate: string;
  /** Windows: wake the computer for the start (StartupView.wakeTimers). */
  readonly wakeComputer: boolean;
  /** 0 = off; while inside the window, unfinished downloads are tried again every N minutes. */
  readonly repeatMinutes: number;
}

export const EMPTY_SCHEDULE: Schedule = {
  enabled: false,
  startMinute: 0,
  stopEnabled: false,
  stopMinute: 0,
  weekdays: 0,
  onceDate: '',
  wakeComputer: false,
  repeatMinutes: 0,
};

/** downex.v1.Queue. Every download belongs to exactly one queue (Download.queueId). */
export interface Queue {
  readonly id: number;
  /** Unique, at most 64 characters. */
  readonly name: string;
  /** Processing downloads now. */
  readonly running: boolean;
  /** 0 = Settings.maxConcurrentDownloads; 1–16. */
  readonly maxConcurrent: number;
  /** Bytes per second; 0 = unlimited (within the global limit). */
  readonly speedLimit: number;
  readonly schedule: Schedule;
  readonly postAction: PostAction;
  /** The main queue cannot be deleted. */
  readonly isMain: boolean;
  /** The queue's downloads in order (changed with moveDownloads). */
  readonly downloadIds: readonly number[];
}

/** What createQueue and updateQueue set (updateQueue replaces all of it). */
export type QueueSpec = Pick<Queue, 'name' | 'maxConcurrent' | 'speedLimit' | 'schedule' | 'postAction'>;

/** A post-queue action counting down (downex.v1.PostActionPending), or its cancellation. */
export interface PostActionPending {
  readonly queueId: number;
  readonly action: PostAction;
  /** When the action runs (epoch ms; 0 = not known). */
  readonly deadline: number;
  /** The countdown was cancelled, or the action was aborted. */
  readonly cancelled: boolean;
}

/** How downexd reaches the network (downex.v1.ProxyMode); 'unspecified' means system. */
export type ProxyMode = 'unspecified' | 'system' | 'none' | 'manual' | 'pac';

/** Indexed by the proto enum number. */
export const PROXY_MODES: readonly ProxyMode[] = ['unspecified', 'system', 'none', 'manual', 'pac'];

/** downex.v1.ProxySettings. The password is never here: see Api.setSecret (kind "proxy"). */
export interface ProxySettings {
  readonly mode: ProxyMode;
  /** "host:port" for http:// downloads. */
  readonly httpProxy: string;
  /** "host:port" for https:// downloads. */
  readonly httpsProxy: string;
  /** "socks5://host:port" (or socks4, socks4a); replaces the HTTP proxies when set. */
  readonly socksProxy: string;
  /** ';'-separated host patterns; "<local>" = names without dots. */
  readonly bypass: string;
  /** Address of the proxy auto-configuration script (http, https or file). */
  readonly pacUrl: string;
  readonly username: string;
  /** Read-only: a proxy password is stored. */
  readonly hasPassword: boolean;
}

/** downex.v1.HostRule: limits for the downloads from hosts that match a pattern. */
export interface HostRule {
  /** "example.com" or "*.example.com"; the most specific pattern wins. */
  readonly pattern: string;
  /** 0 = default, 1–32. */
  readonly maxConnections: number;
  /** Bytes per second shared by the downloads from the host; 0 = unlimited. */
  readonly speedLimit: number;
  /** The longest ranged request to the host, in bytes: 0 = no cap, else 1 MiB–1 GiB (site media). */
  readonly maxRequestBytes: number;
}

/** downex.v1.SiteLogin. The password is never here: see Api.setSecret (kind "site_login"). */
export interface SiteLogin {
  /** Assigned by downexd; "" for a login that was not saved yet. */
  readonly id: string;
  /** "https://example.com/*", "*.example.com". */
  readonly urlPattern: string;
  readonly username: string;
  /** Read-only: a password is stored. */
  readonly hasPassword: boolean;
}

/** Whose password Api.setSecret stores. */
export type SecretKind = 'proxy' | 'site_login';

/** downex.v1.UpdateInfo. */
export interface UpdateInfo {
  readonly currentVersion: string;
  readonly latestVersion: string;
  readonly available: boolean;
  readonly notes: string;
  /** Epoch ms; 0 = not known. */
  readonly publishedAt: number;
  readonly checkedAt: number;
}

/**
 * What a download is (downex.v1.DownloadKind): one file, or a streaming-media job (HLS/DASH
 * fragments assembled into one file, MEDIA-SPEC §6). The proto's UNSPECIFIED means a file
 * and is read as 'file'.
 */
export type DownloadKind = 'file' | 'media';

/** Indexed by the proto enum number (0 = UNSPECIFIED = file). */
export const DOWNLOAD_KINDS: readonly DownloadKind[] = ['file', 'file', 'media'];

/** downex.v1.MediaTrack.kind; a kind this window does not know is 'other'. */
export type MediaTrackKind = 'video' | 'audio' | 'muxed' | 'subtitles' | 'other';

/**
 * Where a media download comes from (downex.v1.MediaOrigin): an HLS/DASH manifest, or a web
 * page that downexd resolves with yt-dlp (SITE-MEDIA-SPEC). The proto's UNSPECIFIED (every
 * client before site media) is 'manifest'.
 */
export type MediaOrigin = 'manifest' | 'site';

/** Indexed by the proto enum number (0 = UNSPECIFIED = manifest). */
export const MEDIA_ORIGINS: readonly MediaOrigin[] = ['manifest', 'manifest', 'site'];

/** One selectable track of a streaming presentation (downex.v1.MediaTrack). */
export interface MediaTrack {
  /** Selects the track (ConfirmOptions.videoTrack/audioTrack, AddMediaRequest). */
  readonly id: string;
  /** muxed = video and audio together (no separate audio track is needed). */
  readonly kind: MediaTrackKind;
  /** Bits per second; 0 when unknown. */
  readonly bandwidth: number;
  readonly width: number;
  readonly height: number;
  /** Frames per second; 0 when unknown. */
  readonly frameRate: number;
  /** RFC 6381 codecs list, e.g. "avc1.640028,mp4a.40.2". */
  readonly codecs: string;
  /** BCP 47 language tag ("" when not declared). */
  readonly language: string;
  readonly name: string;
  /** Site media: bytes as the site declares them; 0 = unknown. */
  readonly size: number;
  /** The size is the site's estimate. */
  readonly sizeApprox: boolean;
  /** Site media: the site's file type ("mp4", "m4a"; "" = unknown). */
  readonly ext: string;
}

/** downex.v1.MediaInfo: the stream of a media download, or the result of probeMedia. */
export interface MediaInfo {
  /** The manifest ("" for a web page). */
  readonly manifestUrl: string;
  /** "hls" | "dash"; a web page's direct formats are "http" (kept verbatim). */
  readonly format: string;
  readonly tracks: readonly MediaTrack[];
  /** Track ids of the daemon's pick ("" = none; no audio for a muxed video track). */
  readonly selectedVideo: string;
  readonly selectedAudio: string;
  /** 0 when unknown. */
  readonly durationMs: number;
  readonly fragmentsTotal: number;
  readonly fragmentsDone: number;
  /**
   * Output container: "mp4" for every new download (MPEG-TS streams are converted), "ts" only
   * for a download stored as MPEG-TS before 2026-10-23, "" not known yet.
   */
  readonly container: string;
  /** Page or media title hint. */
  readonly title: string;
  readonly origin: MediaOrigin;
  /** A web page's site, for display ("YouTube"); "" otherwise. */
  readonly site: string;
  /** The page (a web page's address; for a manifest, the page it was found on, when known). */
  readonly pageUrl: string;
}

/** Stable machine codes from DAEMON-SPEC §6. Unknown codes are kept verbatim. */
export type DownloadErrorCode =
  | 'validator_changed'
  | 'disk_full'
  | 'tls'
  | 'http_status'
  | 'network'
  | 'invalid_request'
  | 'stalled'
  | 'internal'
  /** Media downloads (MEDIA-SPEC §6): an init segment turned out to be encrypted. */
  | 'encrypted'
  | 'live'
  /** The server asked for credentials and no site login matches (M6-SPEC §2); a video site wants a sign-in. */
  | 'auth_required'
  /** Video pages (SITE-MEDIA-SPEC §3.2). */
  | 'media_unsupported'
  | 'media_invalid'
  | 'site_unsupported'
  | 'site_unavailable'
  | 'site_error'
  | 'tools_unavailable'
  | 'site_media_off';

/** What one worker connection is doing (downex.v1.ConnectionState, DESKTOP-SPEC §7). */
export type ConnectionState =
  | 'unspecified'
  | 'connecting'
  | 'requesting'
  | 'receiving'
  | 'retrying'
  | 'idle'
  | 'done'
  | 'error';

/** Indexed by the proto enum number. */
export const CONNECTION_STATES: readonly ConnectionState[] = [
  'unspecified',
  'connecting',
  'requesting',
  'receiving',
  'retrying',
  'idle',
  'done',
  'error',
];

/**
 * One worker connection of a running download (downex.v1.ConnectionInfo). Only Watch
 * updates of running downloads carry them; they are never stored.
 */
export interface ConnectionInfo {
  /** 1-based slot number, stable while the download runs. */
  readonly index: number;
  readonly state: ConnectionState;
  /** The current byte range (inclusive); -1 when there is none. */
  readonly start: number;
  readonly end: number;
  /** Bytes received in the current range. */
  readonly received: number;
  readonly speedBps: number;
  /** Short text from the daemon, e.g. why and when it retries (no URLs). */
  readonly detail: string;
  /** Media downloads: the fragment it fetches (index from 0); -1 otherwise. */
  readonly fragment: number;
}

/** A live view of one byte range. `end` is inclusive; -1 = open-ended. */
export interface Segment {
  readonly start: number;
  readonly end: number;
  readonly received: number;
}

export interface Download {
  readonly id: number;
  readonly url: string;
  readonly finalUrl: string;
  readonly dir: string;
  readonly filename: string;
  /** Total size in bytes; -1 when unknown; an estimate when `sizeApprox`. */
  readonly size: number;
  /**
   * `size` is downexd's estimate: a stream that is not completed (from the bytes of its
   * fragments so far, else the probe's size or bit rate). A completed download's size is exact.
   */
  readonly sizeApprox: boolean;
  readonly received: number;
  readonly state: DownloadState;
  readonly mode: Mode;
  /** 0 = daemon default. */
  readonly maxConnections: number;
  /** Bytes per second; 0 = unlimited. */
  readonly speedLimit: number;
  /** Human-readable; empty unless failed. */
  readonly error: string;
  /** Stable machine code (see DownloadErrorCode); empty unless failed. */
  readonly errorCode: string;
  readonly createdAt: number;
  readonly updatedAt: number;
  readonly completedAt: number;
  /** Live fields, only meaningful while downloading. */
  readonly speedBps: number;
  readonly connections: number;
  readonly segments: readonly Segment[];
  /** 1-based position among queued downloads, 0 otherwise. */
  readonly queuePosition: number;
  readonly source: AddSource;
  /** Size hint from the requester (the browser); 0 or -1 when unknown. `size` wins once probed. */
  readonly expectedSize: number;
  /** MIME hint from the requester. */
  readonly mime: string;
  /** Name of the category the daemon chose ("" = none). */
  readonly category: string;
  readonly kind: DownloadKind;
  /** The stream of a media download; null for files. */
  readonly media: MediaInfo | null;
  /** Live, one per worker connection (the progress window's details); empty unless running. */
  readonly connectionInfo: readonly ConnectionInfo[];
  /**
   * Its progress window was opened at least once: the daemon shows the download complete
   * dialog when it completes (DESKTOP-SPEC §8).
   */
  readonly completionDialog: boolean;
  /** Time spent running, pauses excluded, in ms; 0 when not known. */
  readonly activeMs: number;
  /** When the download first started running; 0 = not yet (or not known). */
  readonly startedAt: number;
  /** The queue it belongs to (M6); an older daemon's 0 means the main queue (see queueOf). */
  readonly queueId: number;
}

/** The queue a download belongs to: its queueId, or the main queue when it has none. */
export function queueOf(d: Pick<Download, 'queueId'>): number {
  return d.queueId > 0 ? d.queueId : MAIN_QUEUE_ID;
}

export interface Header {
  readonly name: string;
  readonly value: string;
}

/**
 * AddRequest mirrors downex.v1.AddRequest. The proto `body` field (POST body) is
 * only produced by the browser relay, so the desktop UI never sends it.
 */
export interface AddRequest {
  readonly url: string;
  readonly method?: 'GET' | 'POST';
  readonly headers?: readonly Header[];
  /** Absolute directory; empty = the category folder or settings.defaultDir. */
  readonly dir?: string;
  /** Optional; sanitised by the daemon. */
  readonly filename?: string;
  /** 0–32; 0 = daemon default. */
  readonly maxConnections?: number;
  /** Bytes per second; 0 = unlimited. */
  readonly speedLimit?: number;
  readonly startPaused?: boolean;
  /** Who asks (the desktop UI sends 'ui'). */
  readonly source?: AddSource;
  /** Optional size hint in bytes. */
  readonly expectedSize?: number;
  /** Optional MIME hint used for category selection. */
  readonly mime?: string;
  /** The queue to put it in; 0 or absent = the main queue. */
  readonly queueId?: number;
}

/**
 * downex.v1.AddMediaRequest: a streaming-media download of an HLS (.m3u8) or DASH (.mpd)
 * manifest, or (origin 'site') of the video of a web page, saved as one MP4 file.
 */
export interface AddMediaRequest {
  /** The manifest; "" for a web page (origin 'site'). */
  readonly manifestUrl: string;
  readonly headers?: readonly Header[];
  /** Absolute directory; empty = the category folder or settings.defaultDir. */
  readonly dir?: string;
  /** Without extension is fine: the daemon appends .mp4. */
  readonly filename?: string;
  readonly source?: AddSource;
  /** Track ids; empty = the daemon's default (best) pick. */
  readonly videoTrack?: string;
  readonly audioTrack?: string;
  /** The page; for origin 'site' the address to download. */
  readonly pageUrl?: string;
  readonly title?: string;
  readonly startPaused?: boolean;
  /** The queue to put it in; 0 or absent = the main queue. */
  readonly queueId?: number;
  /** Absent = 'manifest'. */
  readonly origin?: MediaOrigin;
}

/**
 * downex.v1.ProbeMediaRequest: lists a manifest's tracks, or (origin 'site') every MP4
 * quality of a web page's video, without adding anything.
 */
export interface ProbeMediaRequest {
  /** The manifest; "" for a web page (origin 'site'). */
  readonly manifestUrl: string;
  readonly headers?: readonly Header[];
  /** For origin 'site': the page to resolve. */
  readonly pageUrl?: string;
  /** Absent = 'manifest'. */
  readonly origin?: MediaOrigin;
}

/** What one tool is doing (downex.v1.ToolState). */
export type ToolState = 'unspecified' | 'missing' | 'installing' | 'ready' | 'failed' | 'disabled';

/** Indexed by the proto enum number. */
export const TOOL_STATES: readonly ToolState[] = ['unspecified', 'missing', 'installing', 'ready', 'failed', 'disabled'];

/**
 * yt-dlp or Deno as downexd uses them for video pages (downex.v1.ToolInfo,
 * SITE-MEDIA-SPEC §4): Downex's own copy, downloaded on first use, or one found on PATH.
 */
export interface ToolInfo {
  /** "yt-dlp" | "deno". */
  readonly name: string;
  readonly state: ToolState;
  /** Of the copy in use ("" = none). */
  readonly version: string;
  /** Absolute path of the copy in use. */
  readonly path: string;
  /** true: Downex's tools folder; false: found on PATH. */
  readonly managed: boolean;
  /** The last release seen ("" = not checked). */
  readonly latestVersion: string;
  /** When releases were last looked for (epoch ms; 0 = never). */
  readonly checkedAt: number;
  /** While installing; bytesTotal -1 = unknown. */
  readonly bytesDone: number;
  readonly bytesTotal: number;
  /** Short text of the last failure (no URLs). */
  readonly error: string;
  readonly errorCode: string;
  /** What Downex checked: "openpgp+sha256" | "sha256+authenticode" | "sha256" | "" (a PATH copy). */
  readonly verified: string;
}

/** downex.v1.ToolsStatus: both tools, from GetTools/InstallTools and the `tools` events. */
export interface ToolsStatus {
  readonly tools: readonly ToolInfo[];
  /** Settings.siteMedia. */
  readonly enabled: boolean;
  /** yt-dlp is ready, and Deno is ready or not built for this system. */
  readonly ready: boolean;
}

/** The yt-dlp release channel downexd installs from (Settings.toolsChannel). */
export type ToolsChannel = 'stable' | 'nightly';

/**
 * What a restart does with the file of a completed download (downex.v1.ExistingFile,
 * ENGINE-SPEC §7.1): 'keepBoth' (the default) keeps it and saves the new copy under a free
 * name ("name (1).ext"); 'replace' keeps the name, and the new copy replaces the file once it
 * is complete (until then, and if it fails, the file stays as it is; a folder or link is never
 * replaced). A file that is gone is downloaded again under its own name either way.
 */
export type ExistingFile = 'keepBoth' | 'replace';

/**
 * "name (n).ext" as downexd numbers a free name for n > 0 (the engine's rule: the last
 * extension stays; a dot-file such as ".profile" has none). Keep both of the first copy of
 * "report.pdf" is "report (1).pdf", of "report (1).pdf" "report (1) (1).pdf".
 */
export function numberedName(name: string, n: number): string {
  if (n === 0) return name;
  const dot = name.lastIndexOf('.');
  if (dot <= 0) return `${name} (${n})`;
  return `${name.slice(0, dot)} (${n})${name.slice(dot)}`;
}

export interface ResumeOptions {
  /**
   * Discard saved data and download from the start (needed after a `validator_changed`
   * failure). A completed download is resumed only with restart: it is downloaded again.
   */
  readonly restart?: boolean;
  /** With restart, for a completed download (ignored for others; never sent without restart). */
  readonly existingFile?: ExistingFile;
  readonly maxConnections?: number;
  readonly speedLimit?: number;
}

export interface RemoveOptions {
  /** Also delete the completed file or the partial file. */
  readonly deleteFile: boolean;
}

/** Completes a download awaiting confirmation (downex.v1.ConfirmRequest). To cancel it, remove it. */
export interface ConfirmOptions {
  /** true = queue it now ("Start download"); false = keep it paused ("Download later"). */
  readonly start: boolean;
  /** Absolute folder; empty = the folder the daemon proposed. */
  readonly dir?: string;
  /** Empty = the proposed name; sanitised by the daemon. */
  readonly filename?: string;
  readonly maxConnections?: number;
  readonly speedLimit?: number;
  /** Media downloads: the chosen track ids; empty = keep the daemon's pick. */
  readonly videoTrack?: string;
  readonly audioTrack?: string;
  /**
   * The queue to put it in (0 or absent = keep the current one). With start false
   * ("Download later") it waits there, queued, until that queue runs.
   */
  readonly queueId?: number;
}

/**
 * Changes a download's limits (downex.v1.SetDownloadOptionsRequest). A field left out
 * stays as it is. A running download applies the speed limit at once and the connection
 * limit as soon as it can (at the latest when it starts again).
 */
export interface DownloadOptions {
  /** Bytes per second; 0 = unlimited. */
  readonly speedLimit?: number;
  /** 1–32; 0 = the daemon's default. */
  readonly maxConnections?: number;
  /**
   * true: the download gets the download complete dialog (DESKTOP-SPEC §8). A progress
   * window sends it once when it opens; the daemon refuses completed downloads.
   */
  readonly completionDialog?: boolean;
}

/** What this window shows (the main list, or the progress window of one download), and what its host can do. */
export type StartupView = ({ readonly view: 'main' } | { readonly view: 'progress'; readonly id: number }) & {
  /**
   * The host can show the system's "Open with" dialog (openWith). False (macOS for now):
   * Open with… is not offered.
   */
  readonly openWith: boolean;
  /** The system can hibernate after a queue (false on macOS): the queue editor offers it only then. */
  readonly hibernate: boolean;
  /** A queue's schedule can wake the computer (Windows): "Wake the computer" is offered only then. */
  readonly wakeTimers: boolean;
  /**
   * The host window is frameless and the page draws the title bar's window controls (the
   * Windows main window, DESIGN-SYSTEM.md §6.2). Absent = false (macOS, progress windows,
   * older hosts).
   */
  readonly customTitleBar?: boolean;
  /** What this window is built from (Settings → About); absent from older hosts. */
  readonly build?: BuildInfo;
};

/** The window's build (StartupView.build). */
export interface BuildInfo {
  /** The window's version ("dev" for a development build). */
  readonly version: string;
  /** Go's names: "windows" | "darwin" | "linux"; "amd64" | "arm64". */
  readonly os: string;
  readonly arch: string;
  /** "go1.27.0". */
  readonly goVersion: string;
  /** "v2.17.0"; '' when not known. */
  readonly wailsVersion: string;
  /** The commit it was built from; '' when the build has none (then show nothing). */
  readonly commit: string;
  /** When that commit was made (epoch ms; 0 = not known). */
  readonly commitTime: number;
  /** Built with changes that were not committed. */
  readonly modified: boolean;
}

/** The app's theme (Settings → Appearance), as the host applies it to the native window parts. */
export type ThemeChoice = 'system' | 'light' | 'dark';

export const THEME_CHOICES: readonly ThemeChoice[] = ['system', 'light', 'dark'];

/** One of Downex's folders (Settings → About): the host resolves its path itself. */
export type AppFolder = 'data' | 'logs' | 'tools' | 'downloads';

export const APP_FOLDERS: readonly AppFolder[] = ['data', 'logs', 'tools', 'downloads'];

/**
 * The WebView2 (Edge) version in a user agent ("… Edg/141.0.3537.71"), for Settings → About and
 * the diagnostics; '' when it names none (another browser, the demo).
 */
export function webviewVersion(userAgent: string): string {
  const m = /\bEdg\/(\d+(?:\.\d+){0,3})(?:\s|$)/.exec(userAgent);
  return m?.[1] ?? '';
}

/** What the host's system can do, for the editors (the StartupView flags). */
export type Capabilities = Pick<StartupView, 'openWith' | 'hibernate' | 'wakeTimers' | 'customTitleBar'>;

/** A command of the desktop's menu bar (macOS) for the page: "settings" shows the Settings dialog. */
export type MenuCommand = 'settings';

/** The window of this page (the desktop host has one window per process). */
export interface WindowControls {
  setTitle(title: string): void;
  /** Size in CSS pixels at the default zoom (the host scales them for the display). */
  setSize(width: number, height: number): void;
  /** The window's size, or null when the host cannot tell. */
  getSize(): Promise<{ width: number; height: number } | null>;
  /** Closes the window (and ends its process). */
  close(): void;
  /** Minimises the window (a host without it does nothing). */
  minimise(): void;
  /** Maximises the window, or restores it when it is maximised (a host without it does nothing). */
  toggleMaximise(): void;
  /** Whether the window is maximised (false when the host cannot tell). */
  isMaximised(): Promise<boolean>;
}

/** downex.v1.Category: file extensions mapped to a save folder. */
export interface Category {
  /** Unique, e.g. "Video". */
  readonly name: string;
  /** Lower case, without dots, e.g. ["mp4", "mkv"]. */
  readonly extensions: readonly string[];
  /** Absolute; empty = <defaultDir>/<name>. */
  readonly dir: string;
}

/**
 * Settings are replaced as a whole: clients read them, change what they show and write
 * every field back, so that nothing they do not display is lost.
 */
export interface Settings {
  /** 1–16, default 3. */
  readonly maxConcurrentDownloads: number;
  /** Bytes per second; 0 = unlimited. */
  readonly globalSpeedLimit: number;
  /** Absolute; default = the user's Downloads folder. */
  readonly defaultDir: string;
  /** 1–32, default 8. */
  readonly defaultMaxConnections: number;
  /** In matching order: the first category with a file's extension wins. */
  readonly categories: readonly Category[];
  /** Save into the category's folder when a download names no folder. */
  readonly useCategoryFolders: boolean;
  /** Downloads handed over by the browser wait for the user to confirm them. */
  readonly confirmBrowserDownloads: boolean;
  readonly notifyOnComplete: boolean;
  readonly notifyOnFailure: boolean;
  /** Start downexd (with its tray icon) when the user signs in. */
  readonly launchAtLogin: boolean;
  readonly showTrayIcon: boolean;
  /** Open a progress window when a download is started from the window or the confirmation dialog. */
  readonly showProgressWindow: boolean;
  /**
   * Downloads whose progress window was opened end with the download complete dialog
   * (DESKTOP-SPEC §8): an open progress window turns into it, else downexd opens it. Off,
   * a progress window closes when its download completes.
   */
  readonly showCompleteDialog: boolean;
  // ---- M6 (docs/M6-SPEC.md) ----
  /** How downloads reach the network. */
  readonly proxy: ProxySettings;
  readonly hostRules: readonly HostRule[];
  /** Credentials for servers that ask for them (passwords via setSecret). */
  readonly siteLogins: readonly SiteLogin[];
  /** Offer download links copied to the clipboard (default off). */
  readonly clipboardMonitor: boolean;
  /** Mark finished files as downloaded from the internet (default on). */
  readonly markDownloadedFiles: boolean;
  readonly avScanEnabled: boolean;
  /** Absolute path of the scanner program. */
  readonly avScanCommand: string;
  /** Its arguments; [File] is replaced by the downloaded file (default "[File]"). */
  readonly avScanArgs: string;
  /** https address of the signed update manifest; empty = updates off. */
  readonly updateFeedUrl: string;
  readonly autoCheckUpdates: boolean;
  // ---- site media (docs/SITE-MEDIA-SPEC.md §3.8) ----
  /** Resolve video pages with yt-dlp, downloaded on first use (default on). */
  readonly siteMedia: boolean;
  /** Keep Downex's own yt-dlp and Deno up to date (default on). */
  readonly toolsAutoUpdate: boolean;
  readonly toolsChannel: ToolsChannel;
}

export interface Status {
  readonly version: string;
  readonly pid: number;
  readonly startedAt: number;
  readonly active: number;
  readonly queued: number;
  /** The IPC endpoint: never shown or copied (Settings → About, diagnostics). */
  readonly endpoint: string;
  /**
   * downexd's folders, absolute: data (database, settings), logs, tools. '' or absent = not known
   * (an older downexd); both adapters always set them.
   */
  readonly dataDir?: string;
  readonly logDir?: string;
  readonly toolsDir?: string;
  /** This build can verify and install updates (false or absent: "Off in this build", no signing key). */
  readonly updatesSupported?: boolean;
}

/** A download entry of a downex.v1.WatchResponse batch (the proto `oneof kind`). */
export type DownloadEvent =
  | { readonly kind: 'snapshot'; readonly download: Download }
  | { readonly kind: 'updated'; readonly download: Download }
  | { readonly kind: 'removed'; readonly id: number };

/** A queue entry of a WatchResponse batch (M6). */
export type QueueEvent =
  | { readonly kind: 'queue_updated'; readonly queue: Queue }
  | { readonly kind: 'queue_removed'; readonly id: number }
  | { readonly kind: 'post_action'; readonly pending: PostActionPending };

/** The state of yt-dlp and Deno (site media): in a subscription's first batch, then on each change. */
export type ToolsEvent = { readonly kind: 'tools'; readonly status: ToolsStatus };

export type DaemonEvent = DownloadEvent | QueueEvent | ToolsEvent;

export type EventBatch = readonly DaemonEvent[];

/** Whether an event is about a download (not a queue). */
export function isDownloadEvent(e: DaemonEvent): e is DownloadEvent {
  return e.kind === 'snapshot' || e.kind === 'updated' || e.kind === 'removed';
}

export type Unsubscribe = () => void;

export interface Api {
  /** Which implementation is serving the UI (shown in the status bar). */
  readonly kind: 'wails' | 'mock';
  list(): Promise<Download[]>;
  /** One download; fails with not_found when it does not exist. */
  get(id: number): Promise<Download>;
  add(req: AddRequest): Promise<Download>;
  pause(id: number): Promise<Download>;
  resume(id: number, opts?: ResumeOptions): Promise<Download>;
  remove(id: number, opts: RemoveOptions): Promise<void>;
  /**
   * Adds a streaming-media download. The daemon fetches and parses the manifest first: an
   * encrypted or live stream fails with invalid_argument (see mediaRefusal in ./media).
   */
  addMedia(req: AddMediaRequest): Promise<Download>;
  /** Fetches and parses a manifest and lists its tracks; fails like addMedia. */
  probeMedia(req: ProbeMediaRequest): Promise<MediaInfo>;
  /** Completes a download awaiting confirmation; fails with failed_precondition otherwise. */
  confirm(id: number, opts: ConfirmOptions): Promise<Download>;
  /** Changes a download's speed and connection limits (DESKTOP-SPEC §7). */
  setDownloadOptions(id: number, opts: DownloadOptions): Promise<Download>;
  /** Opens (or brings to the front) the progress window of a download. */
  openProgressWindow(id: number): Promise<void>;
  /** What this window shows ("downex-ui --progress <id>" makes a progress window). */
  startup(): Promise<StartupView>;
  /** This page's window. */
  readonly window: WindowControls;
  /** Pauses every active download; resolves to how many were paused. */
  pauseAll(): Promise<number>;
  /** Resumes every paused download; resolves to how many were resumed. */
  resumeAll(): Promise<number>;
  getSettings(): Promise<Settings>;
  /** Full replacement; validated by the daemon. Resolves to the stored settings. */
  updateSettings(settings: Settings): Promise<Settings>;
  status(): Promise<Status>;
  /** Opens a completed download with the system's default application. */
  openFile(id: number): Promise<void>;
  /**
   * Lets the user choose the application that opens a completed download (Windows: the
   * system's "Open with" dialog), after the same checks as openFile. Rejects with
   * `canceled` when the dialog was closed without a choice, and with `unimplemented` where
   * the system has no such dialog (StartupView.openWith is false there).
   */
  openWith(id: number): Promise<void>;
  /** Reveals the download's folder in the system file manager. */
  openFolder(id: number): Promise<void>;
  /** Native folder picker. Resolves to null when the user cancels. */
  pickFolder(initialDir?: string): Promise<string | null>;
  /** Native file picker (an existing file). Resolves to null when the user cancels. */
  pickFile(opts?: { readonly title?: string; readonly initialDir?: string }): Promise<string | null>;
  /**
   * Bytes free on the volume that holds `dir` (an absolute folder; the status bar's free space,
   * the Add dialog's "214 GiB free"). Rejects ApiError 'unimplemented' when the host lacks the
   * binding, 'invalid_argument' for a relative folder and 'unavailable' when it cannot be read.
   */
  freeSpace(dir: string): Promise<number>;

  // ---- queues (M6-SPEC §1) ----
  /** Every queue, the main queue first. */
  listQueues(): Promise<Queue[]>;
  createQueue(spec: QueueSpec): Promise<Queue>;
  /** Replaces the queue's name, limits, schedule and post-queue action. */
  updateQueue(id: number, spec: QueueSpec): Promise<Queue>;
  /** Its downloads move to the end of the main queue; the main queue cannot be deleted. */
  deleteQueue(id: number): Promise<void>;
  startQueue(id: number): Promise<Queue>;
  /** Pauses the queue's running downloads. */
  stopQueue(id: number): Promise<Queue>;
  /**
   * Moves downloads into a queue at a 0-based position among its other downloads (-1 =
   * the end); resolves to the target queue with its new order.
   */
  moveDownloads(ids: readonly number[], queueId: number, position: number): Promise<Queue>;
  /** Stops the countdown of a queue's post-queue action. */
  cancelPostAction(queueId: number): Promise<void>;

  // ---- secrets and updates (M6-SPEC §2, §4) ----
  /** Stores a password in the system's credential store (never returned or shown). */
  setSecret(kind: SecretKind, id: string, password: string): Promise<void>;
  clearSecret(kind: SecretKind, id: string): Promise<void>;
  /** Checks the update feed of the saved settings. */
  checkForUpdate(): Promise<UpdateInfo>;
  /** Downloads and starts the update; downexd (and Downex) then exit for the installer. */
  installUpdate(): Promise<UpdateInfo>;

  // ---- site media (docs/SITE-MEDIA-SPEC.md §5.4) ----
  /** The state of yt-dlp and Deno; a daemon without site media rejects with `unimplemented`. */
  getTools(): Promise<ToolsStatus>;
  /**
   * Installs yt-dlp and Deno (update: also looks for newer releases), or joins the install
   * that runs. Resolves when it ends (at the latest after 15 minutes, with the progress so
   * far); a failure rejects with the daemon's "tools_unavailable: …" text.
   */
  installTools(update: boolean): Promise<ToolsStatus>;
  /** Opens a fixed https address (a licence) in the system's browser. */
  openExternal(url: string): Promise<void>;

  // ---- the window's theme, downloading again, Settings → About ----
  /**
   * Applies the app's theme to the native window parts (the title bar of the progress and
   * complete windows; macOS's appearance) and stores it for windows opened later. Call it when
   * the theme store attaches and on every change, in every window. Rejects 'invalid_argument'
   * for another value, 'internal' when it could not be stored (it still applies), 'unimplemented'
   * from a host without it.
   */
  setTheme(theme: ThemeChoice): Promise<void>;
  /**
   * Whether the file of a download is on disk (the Download again dialog: without it there is
   * nothing to keep or replace). Rejects 'unimplemented' from a host without it.
   */
  fileExists(id: number): Promise<boolean>;
  /**
   * Shows one of Downex's folders in the file manager; the host resolves the path. Rejects
   * 'failed_precondition' when it does not exist yet, 'invalid_argument' for another kind.
   */
  openAppFolder(folder: AppFolder): Promise<void>;
  /**
   * Plain text for a problem report: versions, system, states, settings that matter and counts,
   * never the IPC endpoint, an address, a file name, a login, a proxy or the update address;
   * profile folders as %USERPROFILE%/%APPDATA%/%LOCALAPPDATA%. `webview` (webviewVersion of the
   * user agent) is added when given.
   */
  diagnostics(opts?: { readonly webview?: string }): Promise<string>;
  /** The open-source notices of Downex's programs (THIRD-PARTY-NOTICES.txt). */
  thirdPartyNotices(): Promise<string>;

  /** Writes text to the system clipboard. */
  copyText(text: string): Promise<void>;
  /**
   * Subscribes to daemon events. The daemon sends at most 4 batches per second;
   * a (re)subscription starts with a `snapshot` for every download.
   */
  onEvents(cb: (batch: EventBatch) => void): Unsubscribe;
  /**
   * Subscribes to requests to show the confirmation dialog of a download (downexd
   * started the window, or asked the open one, with "--confirm <id>").
   */
  onConfirmRequest(cb: (id: number) => void): Unsubscribe;
  /** Subscribes to the commands of the desktop's menu bar (macOS: Settings…). */
  onMenuCommand(cb: (command: MenuCommand) => void): Unsubscribe;
}

/** Connect RPC codes (snake_case, as connect-go prints them). */
export type ApiErrorCode =
  | 'canceled'
  | 'unknown'
  | 'invalid_argument'
  | 'deadline_exceeded'
  | 'not_found'
  | 'already_exists'
  | 'permission_denied'
  | 'resource_exhausted'
  | 'failed_precondition'
  | 'aborted'
  | 'out_of_range'
  | 'unimplemented'
  | 'internal'
  | 'unavailable'
  | 'data_loss'
  | 'unauthenticated';

const API_ERROR_CODES: ReadonlySet<string> = new Set<ApiErrorCode>([
  'canceled',
  'unknown',
  'invalid_argument',
  'deadline_exceeded',
  'not_found',
  'already_exists',
  'permission_denied',
  'resource_exhausted',
  'failed_precondition',
  'aborted',
  'out_of_range',
  'unimplemented',
  'internal',
  'unavailable',
  'data_loss',
  'unauthenticated',
]);

export class ApiError extends Error {
  readonly code: ApiErrorCode;

  constructor(code: ApiErrorCode, message: string) {
    super(message);
    this.name = 'ApiError';
    this.code = code;
  }
}

/**
 * Normalises a rejection into an ApiError. Connect errors print as
 * "<code>: <message>"; a recognised code prefix is lifted into `code`.
 */
export function toApiError(err: unknown): ApiError {
  if (err instanceof ApiError) return err;
  const text =
    err instanceof Error ? err.message : typeof err === 'string' ? err : safeStringify(err);
  const match = /^([a-z_]+):\s*(.*)$/s.exec(text);
  if (match && API_ERROR_CODES.has(match[1] ?? '')) {
    return new ApiError(match[1] as ApiErrorCode, match[2] || text);
  }
  return new ApiError('unknown', text || 'Unknown error');
}

function safeStringify(value: unknown): string {
  try {
    return JSON.stringify(value) ?? String(value);
  } catch {
    return String(value);
  }
}

/** Client-input limits from DAEMON-SPEC §3–§4 and DESKTOP-SPEC §2, mirrored for early UI validation. */
export const LIMITS = {
  minConnections: 1,
  maxConnections: 32,
  minConcurrent: 1,
  maxConcurrent: 16,
  maxUrlBytes: 32 * 1024,
  maxFilenameLength: 255,
  maxCategories: 32,
  maxCategoryNameLength: 64,
  maxExtensionsPerCategory: 200,
  maxExtensionLength: 16,
  /** M6-SPEC §1 / downex.v1.Queue. */
  maxQueueNameLength: 64,
  maxQueueConcurrent: 16,
  /** Repeat interval of a schedule: up to a day. */
  maxRepeatMinutes: 1440,
  /** Settings → Integration: the scanner's arguments. */
  maxScanArgsLength: 4096,
  /** A host rule's longest ranged request (SITE-MEDIA-SPEC §3.8): 1 MiB–1 GiB, 0 = no cap. */
  minRequestBytes: 1024 * 1024,
  maxRequestBytes: 1024 * 1024 * 1024,
} as const;

export const DEFAULT_PROXY: ProxySettings = {
  mode: 'system',
  httpProxy: '',
  httpsProxy: '',
  socksProxy: '',
  bypass: '',
  pacUrl: '',
  username: '',
  hasPassword: false,
};

/** The daemon's default categories (DESKTOP-SPEC §2). */
export const DEFAULT_CATEGORIES: readonly Category[] = [
  { name: 'Video', extensions: ['mp4', 'mkv', 'avi', 'mov', 'webm', 'wmv', 'flv', 'm4v', 'mpg', 'mpeg', 'ts', '3gp'], dir: '' },
  { name: 'Audio', extensions: ['mp3', 'flac', 'm4a', 'aac', 'wav', 'ogg', 'opus', 'wma'], dir: '' },
  {
    name: 'Archives',
    extensions: ['zip', 'rar', '7z', 'gz', 'xz', 'bz2', 'tar', 'tgz', 'zst', 'iso', 'img', 'dmg'],
    dir: '',
  },
  {
    name: 'Documents',
    extensions: ['pdf', 'epub', 'doc', 'docx', 'xls', 'xlsx', 'ppt', 'pptx', 'odt', 'ods', 'txt', 'csv', 'md'],
    dir: '',
  },
  {
    name: 'Programs',
    extensions: ['exe', 'msi', 'msix', 'appx', 'apk', 'deb', 'rpm', 'appimage', 'bat', 'cmd', 'ps1', 'jar'],
    dir: '',
  },
];

export const DEFAULT_SETTINGS: Settings = {
  maxConcurrentDownloads: 3,
  globalSpeedLimit: 0,
  defaultDir: '',
  defaultMaxConnections: 32,
  categories: DEFAULT_CATEGORIES,
  useCategoryFolders: true,
  confirmBrowserDownloads: true,
  notifyOnComplete: true,
  notifyOnFailure: true,
  launchAtLogin: false,
  showTrayIcon: true,
  showProgressWindow: true,
  showCompleteDialog: true,
  proxy: DEFAULT_PROXY,
  hostRules: [],
  siteLogins: [],
  clipboardMonitor: false,
  markDownloadedFiles: true,
  avScanEnabled: false,
  avScanCommand: '',
  avScanArgs: '[File]',
  updateFeedUrl: '',
  autoCheckUpdates: true,
  siteMedia: true,
  toolsAutoUpdate: true,
  toolsChannel: 'nightly',
};

/** States in which a download holds a scheduler slot or waits for one. */
export function isActive(state: DownloadState): boolean {
  return state === 'downloading' || state === 'probing' || state === 'queued';
}

/** States that Resume accepts (DAEMON-SPEC §3: failed downloads can be resumed). */
export function isResumable(state: DownloadState): boolean {
  return state === 'paused' || state === 'failed' || state === 'canceled';
}

/** A browser hand-off waiting for the user to confirm its folder and name (DESKTOP-SPEC §1). */
export function isAwaiting(state: DownloadState): boolean {
  return state === 'awaiting_confirmation';
}

export function needsRestart(d: Pick<Download, 'state' | 'errorCode'>): boolean {
  return d.state === 'failed' && d.errorCode === 'validator_changed';
}
