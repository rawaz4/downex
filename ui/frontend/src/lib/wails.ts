// Adapter from the Api contract to the Wails v2 host (Go, ../). The host binds an
// `App` struct in package `main`, so its methods appear as window.go.main.App.<Method>,
// and it forwards daemon Watch batches as the Wails event "downex:events" and requests
// to confirm a download ("downex-ui --confirm <id>") as "downex:confirm" ({id}). A
// progress window ("downex-ui --progress <id>", a process of its own) learns that from
// Startup; its events are those of its download only. On macOS the menu bar's commands
// arrive as "downex:menu" ({command}). Video pages (docs/SITE-MEDIA-SPEC.md) are media
// requests with origin MEDIA_ORIGIN_SITE; the state of yt-dlp and Deno comes from GetTools
// and the events' `tools` entries.
//
// Wire format: every argument and result is JSON shaped like the protojson encoding of
// the matching downex.v1 message (lowerCamelCase field names). The host can therefore
// forward messages with protojson.Marshal/Unmarshal, or use plain Go structs with the
// same JSON names. Values are parsed once here: int64 may arrive as a JSON number or a
// decimal string, enums as "DOWNLOAD_STATE_QUEUED", "queued" or their number, and
// timestamps as RFC 3339 strings (empty, null or the Go zero time = not set). Omitted fields
// take their proto3 default (0, "", false, []), as protojson omits them.

import {
  ADD_SOURCES,
  CONNECTION_STATES,
  DOWNLOAD_KINDS,
  DOWNLOAD_STATES,
  MEDIA_ORIGINS,
  MODES,
  POST_ACTIONS,
  PROXY_MODES,
  TOOL_STATES,
  toApiError,
  type AddMediaRequest,
  type AddRequest,
  type AddSource,
  type Api,
  type BuildInfo,
  type Category,
  type ConfirmOptions,
  type ConnectionInfo,
  type ConnectionState,
  type DaemonEvent,
  type Download,
  type DownloadOptions,
  type DownloadKind,
  type DownloadState,
  type EventBatch,
  type ExistingFile,
  type HostRule,
  type MediaInfo,
  type MediaOrigin,
  type MediaTrack,
  type MediaTrackKind,
  type MenuCommand,
  type Mode,
  type PostAction,
  type PostActionPending,
  type ProbeMediaRequest,
  type ProxyMode,
  type ProxySettings,
  type Queue,
  type QueueSpec,
  type ResumeOptions,
  type Schedule,
  type Segment,
  type Settings,
  type SiteLogin,
  type StartupView,
  type Status,
  type ToolInfo,
  type ToolState,
  type ToolsStatus,
  type Unsubscribe,
  type UpdateInfo,
  type WindowControls,
} from './api';

export const EVENTS_NAME = 'downex:events';
export const CONFIRM_EVENT = 'downex:confirm';
export const MENU_EVENT = 'downex:menu';

/** Methods the Go `App` struct binds (ui/app.go; TestBindingsAreExactlyTheContract). */
export interface WailsAppBindings {
  List(): Promise<unknown>;
  Get(req: { id: number }): Promise<unknown>;
  Add(req: WireAddRequest): Promise<unknown>;
  Pause(req: { id: number }): Promise<unknown>;
  Resume(req: WireResumeRequest): Promise<unknown>;
  Remove(req: { id: number; deleteFile: boolean }): Promise<unknown>;
  Confirm(req: WireConfirmRequest): Promise<unknown>;
  AddMedia(req: WireAddMediaRequest): Promise<unknown>;
  ProbeMedia(req: WireProbeMediaRequest): Promise<unknown>;
  PauseAll(): Promise<unknown>;
  ResumeAll(): Promise<unknown>;
  GetSettings(): Promise<unknown>;
  UpdateSettings(req: { settings: WireSettings }): Promise<unknown>;
  Status(): Promise<unknown>;
  OpenFile(req: { id: number }): Promise<unknown>;
  OpenFolder(req: { id: number }): Promise<unknown>;
  /** Rejects with "canceled: …" when no app was chosen, "unimplemented: …" without a dialog. */
  OpenWith(req: { id: number }): Promise<unknown>;
  /** Resolves to the chosen absolute folder, or "" when the dialog was cancelled. */
  PickFolder(req: { initialDir: string }): Promise<unknown>;
  SetDownloadOptions(req: WireDownloadOptions): Promise<unknown>;
  OpenProgressWindow(req: { id: number }): Promise<unknown>;
  /** {view: "main", openWith, hibernate, wakeTimers} or {view: "progress", id, …}. */
  Startup(): Promise<unknown>;
  // M6 (docs/M6-SPEC.md)
  /** Resolves to the chosen absolute file, or "" when the dialog was cancelled. */
  PickFile(req: { title?: string; initialDir?: string }): Promise<unknown>;
  ListQueues(): Promise<unknown>;
  CreateQueue(req: { queue: WireQueue }): Promise<unknown>;
  UpdateQueue(req: { queue: WireQueue }): Promise<unknown>;
  DeleteQueue(req: { id: number }): Promise<unknown>;
  StartQueue(req: { id: number }): Promise<unknown>;
  StopQueue(req: { id: number }): Promise<unknown>;
  MoveDownloads(req: { ids: number[]; queueId: number; position: number }): Promise<unknown>;
  CancelPostAction(req: { queueId: number }): Promise<unknown>;
  SetSecret(req: { kind: string; id: string; password: string }): Promise<unknown>;
  ClearSecret(req: { kind: string; id: string }): Promise<unknown>;
  CheckForUpdate(): Promise<unknown>;
  InstallUpdate(): Promise<unknown>;
  // Site media (docs/SITE-MEDIA-SPEC.md §5.4). A host built before them has neither: the
  // calls then reject with unimplemented, as an older downexd's do.
  /** {status: {tools, enabled, ready}}; "unimplemented: …" from a daemon without site media. */
  GetTools?(): Promise<unknown>;
  /** {status} when the install ends (or after 15 minutes, while it goes on). */
  InstallTools?(req: { update?: boolean }): Promise<unknown>;
  // The Studio window (DESIGN-SYSTEM.md §6.2). A host built before it has none: freeSpace then
  // rejects with unimplemented and the page hides the free space.
  /** {freeBytes: "<int64 decimal>"} for an absolute folder; "invalid_argument: …" / "unavailable: …". */
  FreeSpace?(req: { dir: string }): Promise<unknown>;
  // The window's theme, downloading again and Settings → About (0.8.2). A host built before
  // them has none: the calls then reject with unimplemented.
  /** {theme: "system" | "light" | "dark"}; the host applies and stores it. */
  SetTheme?(req: { theme: string }): Promise<unknown>;
  /** {exists} for a download ({id}): its file is on disk. */
  FileExists?(req: { id: number }): Promise<unknown>;
  /** {kind: "data" | "logs" | "tools" | "downloads"}: the host resolves the folder. */
  OpenAppFolder?(req: { kind: string }): Promise<unknown>;
  /** {text} for {webview?: "<version>"}. */
  Diagnostics?(req: { webview?: string }): Promise<unknown>;
  /** {text}: THIRD-PARTY-NOTICES.txt. */
  ThirdPartyNotices?(): Promise<unknown>;
}

/** downex.v1.Queue as CreateQueue/UpdateQueue take it: every field sent (a full replacement). */
export interface WireQueue {
  id?: number;
  name: string;
  maxConcurrent: number;
  speedLimit: number;
  schedule: WireSchedule;
  postAction: string;
}

export interface WireSchedule {
  enabled: boolean;
  startMinute: number;
  stopEnabled: boolean;
  stopMinute: number;
  weekdays: number;
  onceDate: string;
  wakeComputer: boolean;
  repeatMinutes: number;
}

export interface WireDownloadOptions {
  id: number;
  speedLimit?: number;
  maxConnections?: number;
  completionDialog?: boolean;
}

export interface WireAddRequest {
  url: string;
  method?: string;
  headers?: { name: string; value: string }[];
  dir?: string;
  filename?: string;
  maxConnections?: number;
  speedLimit?: number;
  startPaused?: boolean;
  source?: string;
  expectedSize?: number;
  mime?: string;
  queueId?: number;
}

export interface WireAddMediaRequest {
  /** Left out for a web page. */
  manifestUrl?: string;
  headers?: { name: string; value: string }[];
  dir?: string;
  filename?: string;
  source?: string;
  videoTrack?: string;
  audioTrack?: string;
  pageUrl?: string;
  title?: string;
  startPaused?: boolean;
  queueId?: number;
  /** "MEDIA_ORIGIN_SITE" for a web page; left out for a manifest. */
  origin?: string;
}

export interface WireProbeMediaRequest {
  manifestUrl?: string;
  headers?: { name: string; value: string }[];
  pageUrl?: string;
  origin?: string;
}

export interface WireResumeRequest {
  id: number;
  restart: boolean;
  maxConnections?: number;
  speedLimit?: number;
  /** Only with restart (downexd refuses it otherwise). */
  existingFile?: 'EXISTING_FILE_KEEP_BOTH' | 'EXISTING_FILE_REPLACE';
}

export interface WireConfirmRequest {
  id: number;
  start: boolean;
  dir?: string;
  filename?: string;
  maxConnections?: number;
  speedLimit?: number;
  videoTrack?: string;
  audioTrack?: string;
  queueId?: number;
}

/** Every field is sent, false and empty ones too: settings are replaced as a whole. */
export interface WireSettings {
  maxConcurrentDownloads: number;
  globalSpeedLimit: number;
  defaultDir: string;
  defaultMaxConnections: number;
  categories: { name: string; extensions: string[]; dir: string }[];
  useCategoryFolders: boolean;
  confirmBrowserDownloads: boolean;
  notifyOnComplete: boolean;
  notifyOnFailure: boolean;
  launchAtLogin: boolean;
  showTrayIcon: boolean;
  showProgressWindow: boolean;
  showCompleteDialog: boolean;
  proxy: {
    mode: string;
    httpProxy: string;
    httpsProxy: string;
    socksProxy: string;
    bypass: string;
    pacUrl: string;
    username: string;
    hasPassword: boolean;
  };
  hostRules: { pattern: string; maxConnections: number; speedLimit: number; maxRequestBytes: number }[];
  siteLogins: { id: string; urlPattern: string; username: string; hasPassword: boolean }[];
  clipboardMonitor: boolean;
  markDownloadedFiles: boolean;
  avScanEnabled: boolean;
  avScanCommand: string;
  avScanArgs: string;
  updateFeedUrl: string;
  autoCheckUpdates: boolean;
  siteMedia: boolean;
  toolsAutoUpdate: boolean;
  toolsChannel: string;
}

/** The parts of the Wails v2 JavaScript runtime (window.runtime) this page uses. */
export interface WailsRuntime {
  EventsOn(name: string, cb: (...data: unknown[]) => void): (() => void) | void;
  EventsOff?(name: string, ...additional: string[]): void;
  ClipboardSetText?(text: string): Promise<boolean>;
  WindowSetTitle?(title: string): void;
  WindowSetSize?(width: number, height: number): void;
  WindowGetSize?(): Promise<{ w: number; h: number }>;
  WindowMinimise?(): void;
  WindowToggleMaximise?(): void;
  WindowIsMaximised?(): Promise<boolean>;
  Quit?(): void;
  BrowserOpenURL?(url: string): void;
}

declare global {
  interface Window {
    go?: { main?: { App?: WailsAppBindings } };
    runtime?: WailsRuntime;
  }
}

export function wailsBindings(): { app: WailsAppBindings; runtime: WailsRuntime } | null {
  if (typeof window === 'undefined') return null;
  const app = window.go?.main?.App;
  const runtime = window.runtime;
  return app && runtime ? { app, runtime } : null;
}

// ---- boundary parsing ------------------------------------------------------------

type Rec = Record<string, unknown>;

function rec(v: unknown): Rec {
  return v !== null && typeof v === 'object' && !Array.isArray(v) ? (v as Rec) : {};
}

function str(v: unknown): string {
  return typeof v === 'string' ? v : '';
}

/** int64/int32/double: JSON number or decimal string (protojson int64). */
function num(v: unknown, fallback = 0): number {
  if (typeof v === 'number') return Number.isFinite(v) ? v : fallback;
  if (typeof v === 'string' && /^-?\d+(\.\d+)?([eE][-+]?\d+)?$/.test(v.trim())) {
    const n = Number(v);
    return Number.isFinite(n) ? n : fallback;
  }
  return fallback;
}

function bool(v: unknown): boolean {
  return v === true;
}

function enumValue<T extends string>(v: unknown, values: readonly T[], prefix: string): T {
  const fallback = values[0] as T;
  if (typeof v === 'number') return values[v] ?? fallback;
  if (typeof v !== 'string') return fallback;
  const name = (v.startsWith(prefix) ? v.slice(prefix.length) : v).toLowerCase();
  return (values as readonly string[]).includes(name) ? (name as T) : fallback;
}

/** RFC 3339 → epoch ms; unset or the Go zero time (year 1) → 0. */
function timestamp(v: unknown): number {
  if (typeof v !== 'string' || v === '') return 0;
  const ms = Date.parse(v);
  return Number.isFinite(ms) && ms > 0 ? ms : 0;
}

function segment(v: unknown): Segment {
  const r = rec(v);
  return { start: num(r.start), end: num(r.end), received: num(r.received) };
}

function connectionInfo(v: unknown): ConnectionInfo {
  const r = rec(v);
  return {
    index: num(r.index),
    state: enumValue<ConnectionState>(r.state, CONNECTION_STATES, 'CONNECTION_STATE_'),
    start: num(r.start),
    end: num(r.end),
    received: num(r.received),
    speedBps: num(r.speedBps),
    detail: str(r.detail),
    fragment: num(r.fragment),
  };
}

const TRACK_KINDS: readonly MediaTrackKind[] = ['video', 'audio', 'muxed', 'subtitles'];

function trackKind(v: unknown): MediaTrackKind {
  const k = str(v).toLowerCase();
  return (TRACK_KINDS as readonly string[]).includes(k) ? (k as MediaTrackKind) : 'other';
}

function parseMediaTrack(v: unknown): MediaTrack {
  const r = rec(v);
  return {
    id: str(r.id),
    kind: trackKind(r.kind),
    bandwidth: num(r.bandwidth),
    width: num(r.width),
    height: num(r.height),
    frameRate: num(r.frameRate),
    codecs: str(r.codecs),
    language: str(r.language),
    name: str(r.name),
    size: Math.max(0, num(r.size)),
    sizeApprox: bool(r.sizeApprox),
    ext: str(r.ext),
  };
}

/** downex.v1.MediaInfo; a ProbeMediaResponse ({media: {...}}) is unwrapped. */
export function parseMediaInfo(v: unknown): MediaInfo {
  const r = rec(v);
  const m = r.media !== undefined ? rec(r.media) : r;
  return {
    manifestUrl: str(m.manifestUrl),
    format: str(m.format),
    // A track without an id cannot be selected; it is left out.
    tracks: Array.isArray(m.tracks) ? m.tracks.map(parseMediaTrack).filter((t) => t.id !== '') : [],
    selectedVideo: str(m.selectedVideo),
    selectedAudio: str(m.selectedAudio),
    durationMs: num(m.durationMs),
    fragmentsTotal: num(m.fragmentsTotal),
    fragmentsDone: num(m.fragmentsDone),
    container: str(m.container),
    title: str(m.title),
    origin: enumValue<MediaOrigin>(m.origin, MEDIA_ORIGINS, 'MEDIA_ORIGIN_'),
    site: str(m.site),
    pageUrl: str(m.pageUrl),
  };
}

function parseToolInfo(v: unknown): ToolInfo {
  const r = rec(v);
  return {
    name: str(r.name),
    state: enumValue<ToolState>(r.state, TOOL_STATES, 'TOOL_STATE_'),
    version: str(r.version),
    path: str(r.path),
    managed: bool(r.managed),
    latestVersion: str(r.latestVersion),
    checkedAt: timestamp(r.checkedAt),
    bytesDone: Math.max(0, num(r.bytesDone)),
    // -1 (unknown) is sent explicitly; anything below it is read as unknown too
    bytesTotal: Math.max(-1, num(r.bytesTotal)),
    error: str(r.error),
    errorCode: str(r.errorCode),
    verified: str(r.verified),
  };
}

/** downex.v1.ToolsStatus; a GetToolsResponse or InstallToolsResponse ({status: {...}}) is unwrapped. */
export function parseToolsStatus(v: unknown): ToolsStatus {
  const r0 = rec(v);
  const r = r0.status !== undefined ? rec(r0.status) : r0;
  return {
    // A tool without a name cannot be shown or told apart: it is left out.
    tools: Array.isArray(r.tools) ? r.tools.map(parseToolInfo).filter((t) => t.name !== '') : [],
    enabled: bool(r.enabled),
    ready: bool(r.ready),
  };
}

export function parseDownload(v: unknown): Download {
  const r = rec(v);
  if (r.id === undefined) throw toApiError('internal: download without an id from the host');
  return {
    id: num(r.id),
    url: str(r.url),
    finalUrl: str(r.finalUrl),
    dir: str(r.dir),
    filename: str(r.filename),
    size: num(r.size), // proto3 default 0 when omitted; -1 is sent explicitly
    sizeApprox: bool(r.sizeApprox),
    received: num(r.received),
    state: enumValue<DownloadState>(r.state, DOWNLOAD_STATES, 'DOWNLOAD_STATE_'),
    mode: enumValue<Mode>(r.mode, MODES, 'MODE_'),
    maxConnections: num(r.maxConnections),
    speedLimit: num(r.speedLimit),
    error: str(r.error),
    errorCode: str(r.errorCode),
    createdAt: timestamp(r.createdAt),
    updatedAt: timestamp(r.updatedAt),
    completedAt: timestamp(r.completedAt),
    speedBps: num(r.speedBps),
    connections: num(r.connections),
    segments: Array.isArray(r.segments) ? r.segments.map(segment) : [],
    queuePosition: num(r.queuePosition),
    source: enumValue<AddSource>(r.source, ADD_SOURCES, 'ADD_SOURCE_'),
    expectedSize: num(r.expectedSize),
    mime: str(r.mime),
    category: str(r.category),
    kind: enumValue<DownloadKind>(r.kind, DOWNLOAD_KINDS, 'DOWNLOAD_KIND_'),
    media: r.media != null && typeof r.media === 'object' ? parseMediaInfo(r.media) : null,
    connectionInfo: Array.isArray(r.connectionInfo) ? r.connectionInfo.map(connectionInfo) : [],
    completionDialog: bool(r.completionDialog),
    activeMs: Math.max(0, num(r.activeMs)),
    startedAt: timestamp(r.startedAt),
    queueId: Math.max(0, num(r.queueId)),
  };
}

/** POST_ACTION_* (or its number); UNSPECIFIED and unknown values are 'none'. */
export function parsePostAction(v: unknown): PostAction {
  if (typeof v === 'number') return POST_ACTIONS[v] ?? 'none';
  const name = str(v).replace(/^POST_ACTION_/, '').toLowerCase();
  return (POST_ACTIONS as readonly string[]).includes(name) ? (name as PostAction) : 'none';
}

function wirePostAction(a: PostAction): string {
  return a === 'none' ? 'POST_ACTION_UNSPECIFIED' : `POST_ACTION_${a.toUpperCase()}`;
}

function parseSchedule(v: unknown): Schedule {
  const r = rec(v);
  return {
    enabled: bool(r.enabled),
    startMinute: num(r.startMinute),
    stopEnabled: bool(r.stopEnabled),
    stopMinute: num(r.stopMinute),
    weekdays: num(r.weekdays),
    onceDate: str(r.onceDate),
    wakeComputer: bool(r.wakeComputer),
    repeatMinutes: num(r.repeatMinutes),
  };
}

/** downex.v1.Queue; a {queue: {...}} response is unwrapped. */
export function parseQueue(v: unknown): Queue {
  const r0 = rec(v);
  const r = r0.queue !== undefined ? rec(r0.queue) : r0;
  if (r.id === undefined) throw toApiError('internal: queue without an id from the host');
  return {
    id: num(r.id),
    name: str(r.name),
    running: bool(r.running),
    maxConcurrent: num(r.maxConcurrent),
    speedLimit: num(r.speedLimit),
    schedule: parseSchedule(r.schedule),
    postAction: parsePostAction(r.postAction),
    isMain: bool(r.isMain),
    downloadIds: Array.isArray(r.downloadIds) ? r.downloadIds.map((x) => num(x)).filter((x) => x > 0) : [],
  };
}

/** A ListQueuesResponse ({queues: [...]}) or a bare array. */
export function parseQueues(v: unknown): Queue[] {
  const list = Array.isArray(v) ? v : rec(v).queues;
  if (!Array.isArray(list)) return [];
  const out: Queue[] = [];
  for (const item of list) {
    try {
      out.push(parseQueue(item));
    } catch {
      // a queue without an id cannot be shown or addressed
    }
  }
  return out;
}

export function parsePostActionPending(v: unknown): PostActionPending {
  const r = rec(v);
  return {
    queueId: num(r.queueId),
    action: parsePostAction(r.action),
    deadline: timestamp(r.deadline),
    cancelled: bool(r.cancelled),
  };
}

export function toWireQueue(spec: QueueSpec, id?: number): WireQueue {
  const s = spec.schedule;
  const out: WireQueue = {
    name: spec.name,
    maxConcurrent: spec.maxConcurrent,
    speedLimit: spec.speedLimit,
    schedule: {
      enabled: s.enabled,
      startMinute: s.startMinute,
      stopEnabled: s.stopEnabled,
      stopMinute: s.stopMinute,
      weekdays: s.weekdays,
      onceDate: s.onceDate,
      wakeComputer: s.wakeComputer,
      repeatMinutes: s.repeatMinutes,
    },
    postAction: wirePostAction(spec.postAction),
  };
  if (id !== undefined) out.id = id;
  return out;
}

/** downex.v1.UpdateInfo; a {info: {...}} response is unwrapped. */
export function parseUpdateInfo(v: unknown): UpdateInfo {
  const r0 = rec(v);
  const r = r0.info !== undefined ? rec(r0.info) : r0;
  return {
    currentVersion: str(r.currentVersion),
    latestVersion: str(r.latestVersion),
    available: bool(r.available),
    notes: str(r.notes),
    publishedAt: timestamp(r.publishedAt),
    checkedAt: timestamp(r.checkedAt),
  };
}

/** Accepts a WatchResponse ({events: [...]}) or a bare array of Event. */
export function parseEvents(v: unknown): DaemonEvent[] {
  const list = Array.isArray(v) ? v : rec(v).events;
  if (!Array.isArray(list)) return [];
  const out: DaemonEvent[] = [];
  for (const item of list) {
    const e = rec(item);
    try {
      if (e.snapshot != null) out.push({ kind: 'snapshot', download: parseDownload(e.snapshot) });
      else if (e.updated != null) out.push({ kind: 'updated', download: parseDownload(e.updated) });
      else if (e.removed != null) out.push({ kind: 'removed', id: num(e.removed) });
      else if (e.queueUpdated != null) out.push({ kind: 'queue_updated', queue: parseQueue(e.queueUpdated) });
      else if (e.queueRemoved != null) out.push({ kind: 'queue_removed', id: num(e.queueRemoved) });
      else if (e.postAction != null) out.push({ kind: 'post_action', pending: parsePostActionPending(e.postAction) });
      else if (e.tools != null) out.push({ kind: 'tools', status: parseToolsStatus(e.tools) });
    } catch {
      // A malformed entry must not drop the rest of the batch.
    }
  }
  return out;
}

function parseCategory(v: unknown): Category {
  const r = rec(v);
  return {
    name: str(r.name),
    extensions: Array.isArray(r.extensions) ? r.extensions.filter((e): e is string => typeof e === 'string') : [],
    dir: str(r.dir),
  };
}

function parseProxy(v: unknown): ProxySettings {
  const r = rec(v);
  return {
    mode: enumValue<ProxyMode>(r.mode, PROXY_MODES, 'PROXY_MODE_'),
    httpProxy: str(r.httpProxy),
    httpsProxy: str(r.httpsProxy),
    socksProxy: str(r.socksProxy),
    bypass: str(r.bypass),
    pacUrl: str(r.pacUrl),
    username: str(r.username),
    hasPassword: bool(r.hasPassword),
  };
}

function parseHostRule(v: unknown): HostRule {
  const r = rec(v);
  return {
    pattern: str(r.pattern),
    maxConnections: num(r.maxConnections),
    speedLimit: num(r.speedLimit),
    maxRequestBytes: Math.max(0, num(r.maxRequestBytes)),
  };
}

function parseSiteLogin(v: unknown): SiteLogin {
  const r = rec(v);
  return { id: str(r.id), urlPattern: str(r.urlPattern), username: str(r.username), hasPassword: bool(r.hasPassword) };
}

export function parseSettings(v: unknown): Settings {
  const r = rec(v);
  const s = r.settings !== undefined ? rec(r.settings) : r;
  return {
    maxConcurrentDownloads: num(s.maxConcurrentDownloads),
    globalSpeedLimit: num(s.globalSpeedLimit),
    defaultDir: str(s.defaultDir),
    defaultMaxConnections: num(s.defaultMaxConnections),
    // Every entry is kept, even one this window would not accept: it is written back as it came.
    categories: Array.isArray(s.categories) ? s.categories.map(parseCategory) : [],
    useCategoryFolders: bool(s.useCategoryFolders),
    confirmBrowserDownloads: bool(s.confirmBrowserDownloads),
    notifyOnComplete: bool(s.notifyOnComplete),
    notifyOnFailure: bool(s.notifyOnFailure),
    launchAtLogin: bool(s.launchAtLogin),
    showTrayIcon: bool(s.showTrayIcon),
    showProgressWindow: bool(s.showProgressWindow),
    showCompleteDialog: bool(s.showCompleteDialog),
    proxy: parseProxy(s.proxy),
    // Every entry is kept as it came, to be written back.
    hostRules: Array.isArray(s.hostRules) ? s.hostRules.map(parseHostRule) : [],
    siteLogins: Array.isArray(s.siteLogins) ? s.siteLogins.map(parseSiteLogin) : [],
    clipboardMonitor: bool(s.clipboardMonitor),
    markDownloadedFiles: bool(s.markDownloadedFiles),
    avScanEnabled: bool(s.avScanEnabled),
    avScanCommand: str(s.avScanCommand),
    avScanArgs: str(s.avScanArgs),
    updateFeedUrl: str(s.updateFeedUrl),
    autoCheckUpdates: bool(s.autoCheckUpdates),
    siteMedia: bool(s.siteMedia),
    toolsAutoUpdate: bool(s.toolsAutoUpdate),
    // "" (and a value this window does not know, which downexd repairs) reads as the default
    toolsChannel: str(s.toolsChannel) === 'stable' ? 'stable' : 'nightly',
  };
}

export function toWireSettings(s: Settings): WireSettings {
  return {
    maxConcurrentDownloads: s.maxConcurrentDownloads,
    globalSpeedLimit: s.globalSpeedLimit,
    defaultDir: s.defaultDir,
    defaultMaxConnections: s.defaultMaxConnections,
    categories: s.categories.map((c) => ({ name: c.name, extensions: [...c.extensions], dir: c.dir })),
    useCategoryFolders: s.useCategoryFolders,
    confirmBrowserDownloads: s.confirmBrowserDownloads,
    notifyOnComplete: s.notifyOnComplete,
    notifyOnFailure: s.notifyOnFailure,
    launchAtLogin: s.launchAtLogin,
    showTrayIcon: s.showTrayIcon,
    showProgressWindow: s.showProgressWindow,
    showCompleteDialog: s.showCompleteDialog,
    proxy: {
      mode: `PROXY_MODE_${s.proxy.mode.toUpperCase()}`,
      httpProxy: s.proxy.httpProxy,
      httpsProxy: s.proxy.httpsProxy,
      socksProxy: s.proxy.socksProxy,
      bypass: s.proxy.bypass,
      pacUrl: s.proxy.pacUrl,
      username: s.proxy.username,
      hasPassword: s.proxy.hasPassword,
    },
    hostRules: s.hostRules.map((h) => ({
      pattern: h.pattern,
      maxConnections: h.maxConnections,
      speedLimit: h.speedLimit,
      maxRequestBytes: h.maxRequestBytes,
    })),
    siteLogins: s.siteLogins.map((l) => ({ id: l.id, urlPattern: l.urlPattern, username: l.username, hasPassword: l.hasPassword })),
    clipboardMonitor: s.clipboardMonitor,
    markDownloadedFiles: s.markDownloadedFiles,
    avScanEnabled: s.avScanEnabled,
    avScanCommand: s.avScanCommand,
    avScanArgs: s.avScanArgs,
    updateFeedUrl: s.updateFeedUrl,
    autoCheckUpdates: s.autoCheckUpdates,
    siteMedia: s.siteMedia,
    toolsAutoUpdate: s.toolsAutoUpdate,
    toolsChannel: s.toolsChannel,
  };
}

export function parseStatus(v: unknown): Status {
  const r = rec(v);
  return {
    version: str(r.version),
    pid: num(r.pid),
    startedAt: timestamp(r.startedAt),
    active: num(r.active),
    queued: num(r.queued),
    endpoint: str(r.endpoint),
    dataDir: str(r.dataDir),
    logDir: str(r.logDir),
    toolsDir: str(r.toolsDir),
    updatesSupported: bool(r.updatesSupported),
  };
}

/** Startup's build (absent from an older host: undefined). */
function parseBuild(v: unknown): BuildInfo | undefined {
  if (v === null || typeof v !== 'object' || Array.isArray(v)) return undefined;
  const r = rec(v);
  return {
    version: str(r.version),
    os: str(r.os),
    arch: str(r.arch),
    goVersion: str(r.goVersion),
    wailsVersion: str(r.wailsVersion),
    commit: str(r.commit),
    commitTime: timestamp(r.commitTime),
    modified: bool(r.modified),
  };
}

/** A {text} result (Diagnostics, ThirdPartyNotices). */
function parseText(v: unknown): string {
  return str(rec(v).text);
}

const WIRE_EXISTING_FILE: Record<ExistingFile, NonNullable<WireResumeRequest['existingFile']>> = {
  keepBoth: 'EXISTING_FILE_KEEP_BOTH',
  replace: 'EXISTING_FILE_REPLACE',
};

/** The payload of "downex:confirm": {id}; null when it names no download. */
export function parseConfirmRequest(v: unknown): number | null {
  const id = num(rec(v).id);
  return Number.isSafeInteger(id) && id > 0 ? id : null;
}

/**
 * The result of Startup: the main window unless it names a progress window's download.
 * Open with… and hibernation are offered unless the host says its system has none
 * (false); waking the computer only when the host says it can (true). The page draws the
 * window controls only when the host says its window is frameless (customTitleBar true).
 */
export function parseStartup(v: unknown): StartupView {
  const r = rec(v);
  const id = num(r.id);
  const build = parseBuild(r.build);
  const caps = {
    openWith: r.openWith !== false,
    hibernate: r.hibernate !== false,
    wakeTimers: r.wakeTimers === true,
    customTitleBar: r.customTitleBar === true,
    ...(build ? { build } : {}),
  };
  return str(r.view) === 'progress' && Number.isSafeInteger(id) && id > 0
    ? { view: 'progress', id, ...caps }
    : { view: 'main', ...caps };
}

/**
 * A FreeSpaceResponse ({freeBytes: "<int64 decimal>"}, or a number) → bytes. A missing value
 * is the proto3 default 0; anything that is not a whole number of bytes is refused.
 */
export function parseFreeSpace(v: unknown): number {
  const raw = rec(v).freeBytes;
  if (raw === undefined || raw === null || raw === '') return 0;
  const n = num(raw, Number.NaN);
  if (!Number.isSafeInteger(n) || n < 0) throw toApiError('internal: the host answered an invalid free space');
  return n;
}

/** The payload of MENU_EVENT ({command}); null for a command this page does not know. */
export function parseMenuCommand(v: unknown): MenuCommand | null {
  return str(rec(v).command) === 'settings' ? 'settings' : null;
}

/** `{download: {...}}` (AddResponse etc.) or a bare Download. */
function unwrapDownload(v: unknown): Download {
  const r = rec(v);
  return parseDownload(r.download !== undefined ? r.download : v);
}

function wireSource(source: AddSource | undefined): string | undefined {
  return source && source !== 'unspecified' ? `ADD_SOURCE_${source.toUpperCase()}` : undefined;
}

function toWireAdd(req: AddRequest): WireAddRequest {
  const out: WireAddRequest = { url: req.url };
  if (req.method) out.method = req.method;
  const headers = wireHeaders(req.headers);
  if (headers) out.headers = headers;
  if (req.dir) out.dir = req.dir;
  if (req.filename) out.filename = req.filename;
  if (req.maxConnections) out.maxConnections = req.maxConnections;
  if (req.speedLimit) out.speedLimit = req.speedLimit;
  if (req.startPaused) out.startPaused = true;
  const source = wireSource(req.source);
  if (source) out.source = source;
  if (req.expectedSize && req.expectedSize > 0) out.expectedSize = req.expectedSize;
  if (req.mime) out.mime = req.mime;
  if (req.queueId && req.queueId > 0) out.queueId = req.queueId;
  return out;
}

function toWireConfirm(id: number, opts: ConfirmOptions): WireConfirmRequest {
  const out: WireConfirmRequest = { id, start: opts.start };
  if (opts.dir) out.dir = opts.dir;
  if (opts.filename) out.filename = opts.filename;
  if (opts.maxConnections !== undefined) out.maxConnections = opts.maxConnections;
  if (opts.speedLimit !== undefined) out.speedLimit = opts.speedLimit;
  if (opts.videoTrack) out.videoTrack = opts.videoTrack;
  if (opts.audioTrack) out.audioTrack = opts.audioTrack;
  if (opts.queueId && opts.queueId > 0) out.queueId = opts.queueId;
  return out;
}

function wireHeaders(headers: AddRequest['headers']): { name: string; value: string }[] | undefined {
  return headers && headers.length > 0 ? headers.map((h) => ({ name: h.name, value: h.value })) : undefined;
}

/** MEDIA_ORIGIN_SITE for a web page; a manifest's origin is the proto default (left out). */
function wireOrigin(origin: MediaOrigin | undefined): string | undefined {
  return origin === 'site' ? 'MEDIA_ORIGIN_SITE' : undefined;
}

function toWireAddMedia(req: AddMediaRequest): WireAddMediaRequest {
  const out: WireAddMediaRequest = {};
  if (req.manifestUrl) out.manifestUrl = req.manifestUrl;
  const origin = wireOrigin(req.origin);
  if (origin) out.origin = origin;
  const headers = wireHeaders(req.headers);
  if (headers) out.headers = headers;
  if (req.dir) out.dir = req.dir;
  if (req.filename) out.filename = req.filename;
  const source = wireSource(req.source);
  if (source) out.source = source;
  if (req.videoTrack) out.videoTrack = req.videoTrack;
  if (req.audioTrack) out.audioTrack = req.audioTrack;
  if (req.pageUrl) out.pageUrl = req.pageUrl;
  if (req.title) out.title = req.title;
  if (req.startPaused) out.startPaused = true;
  if (req.queueId && req.queueId > 0) out.queueId = req.queueId;
  return out;
}

function toWireOptions(id: number, opts: DownloadOptions): WireDownloadOptions {
  const out: WireDownloadOptions = { id };
  if (opts.speedLimit !== undefined) out.speedLimit = opts.speedLimit;
  if (opts.maxConnections !== undefined) out.maxConnections = opts.maxConnections;
  if (opts.completionDialog !== undefined) out.completionDialog = opts.completionDialog;
  return out;
}

function toWireProbeMedia(req: ProbeMediaRequest): WireProbeMediaRequest {
  const out: WireProbeMediaRequest = {};
  if (req.manifestUrl) out.manifestUrl = req.manifestUrl;
  const headers = wireHeaders(req.headers);
  if (headers) out.headers = headers;
  if (req.pageUrl) out.pageUrl = req.pageUrl;
  const origin = wireOrigin(req.origin);
  if (origin) out.origin = origin;
  return out;
}

/** The rejection of a binding that the host does not have. */
function noBinding(name: string): string {
  return `unimplemented: this Downex window has no ${name}`;
}

async function call<T>(fn: () => Promise<unknown>, parse: (v: unknown) => T): Promise<T> {
  let raw: unknown;
  try {
    raw = await fn();
  } catch (err) {
    throw toApiError(err);
  }
  return parse(raw);
}

function subscribe(runtime: WailsRuntime, name: string, cb: (payload: unknown) => void): Unsubscribe {
  const off = runtime.EventsOn(name, (payload) => cb(payload));
  return () => {
    if (typeof off === 'function') off();
    else runtime.EventsOff?.(name);
  };
}

/** The window controls of the Wails runtime; what it lacks does nothing. */
function windowControls(runtime: WailsRuntime): WindowControls {
  return {
    setTitle: (title) => {
      if (runtime.WindowSetTitle) runtime.WindowSetTitle(title);
      else document.title = title;
    },
    setSize: (width, height) => runtime.WindowSetSize?.(Math.round(width), Math.round(height)),
    getSize: async () => {
      if (!runtime.WindowGetSize) return null;
      try {
        const r = rec(await runtime.WindowGetSize());
        const width = num(r.w);
        const height = num(r.h);
        return width > 0 && height > 0 ? { width, height } : null;
      } catch {
        return null;
      }
    },
    close: () => {
      if (runtime.Quit) runtime.Quit();
      else window.close();
    },
    minimise: () => runtime.WindowMinimise?.(),
    toggleMaximise: () => runtime.WindowToggleMaximise?.(),
    isMaximised: async () => {
      if (!runtime.WindowIsMaximised) return false;
      try {
        return (await runtime.WindowIsMaximised()) === true;
      } catch {
        return false;
      }
    },
  };
}

export function createWailsApi(app: WailsAppBindings, runtime: WailsRuntime): Api {
  return {
    kind: 'wails',
    window: windowControls(runtime),
    list: () =>
      call(
        () => app.List(),
        (v) => {
          const list = Array.isArray(v) ? v : rec(v).downloads;
          return Array.isArray(list) ? list.map(parseDownload) : [];
        },
      ),
    get: (id) => call(() => app.Get({ id }), unwrapDownload),
    add: (req) => call(() => app.Add(toWireAdd(req)), unwrapDownload),
    pause: (id) => call(() => app.Pause({ id }), unwrapDownload),
    resume: (id, opts: ResumeOptions = {}) => {
      const req: WireResumeRequest = { id, restart: opts.restart ?? false };
      if (opts.maxConnections !== undefined) req.maxConnections = opts.maxConnections;
      if (opts.speedLimit !== undefined) req.speedLimit = opts.speedLimit;
      if (req.restart && opts.existingFile) req.existingFile = WIRE_EXISTING_FILE[opts.existingFile];
      return call(() => app.Resume(req), unwrapDownload);
    },
    remove: (id, opts) => call(() => app.Remove({ id, deleteFile: opts.deleteFile }), () => undefined),
    confirm: (id, opts) => call(() => app.Confirm(toWireConfirm(id, opts)), unwrapDownload),
    addMedia: (req) => call(() => app.AddMedia(toWireAddMedia(req)), unwrapDownload),
    probeMedia: (req) => call(() => app.ProbeMedia(toWireProbeMedia(req)), parseMediaInfo),
    setDownloadOptions: (id, opts) => call(() => app.SetDownloadOptions(toWireOptions(id, opts)), unwrapDownload),
    openProgressWindow: (id) => call(() => app.OpenProgressWindow({ id }), () => undefined),
    startup: () => call(() => app.Startup(), parseStartup),
    pauseAll: () => call(() => app.PauseAll(), (v) => num(rec(v).paused)),
    resumeAll: () => call(() => app.ResumeAll(), (v) => num(rec(v).resumed)),
    getSettings: () => call(() => app.GetSettings(), parseSettings),
    updateSettings: (settings) =>
      call(() => app.UpdateSettings({ settings: toWireSettings(settings) }), parseSettings),
    status: () => call(() => app.Status(), parseStatus),
    openFile: (id) => call(() => app.OpenFile({ id }), () => undefined),
    openFolder: (id) => call(() => app.OpenFolder({ id }), () => undefined),
    openWith: (id) => call(() => app.OpenWith({ id }), () => undefined),
    pickFolder: (initialDir = '') =>
      call(
        () => app.PickFolder({ initialDir }),
        (v) => (typeof v === 'string' && v !== '' ? v : null),
      ),
    pickFile: (opts = {}) =>
      call(
        () => {
          const req: { title?: string; initialDir?: string } = {};
          if (opts.title) req.title = opts.title;
          if (opts.initialDir) req.initialDir = opts.initialDir;
          return app.PickFile(req);
        },
        (v) => (typeof v === 'string' && v !== '' ? v : null),
      ),
    freeSpace: (dir) =>
      call(() => (app.FreeSpace ? app.FreeSpace({ dir }) : Promise.reject(noBinding('FreeSpace'))), parseFreeSpace),
    listQueues: () => call(() => app.ListQueues(), parseQueues),
    createQueue: (spec) => call(() => app.CreateQueue({ queue: toWireQueue(spec) }), parseQueue),
    updateQueue: (id, spec) => call(() => app.UpdateQueue({ queue: toWireQueue(spec, id) }), parseQueue),
    deleteQueue: (id) => call(() => app.DeleteQueue({ id }), () => undefined),
    startQueue: (id) => call(() => app.StartQueue({ id }), parseQueue),
    stopQueue: (id) => call(() => app.StopQueue({ id }), parseQueue),
    moveDownloads: (ids, queueId, position) =>
      call(() => app.MoveDownloads({ ids: [...ids], queueId, position }), parseQueue),
    cancelPostAction: (queueId) => call(() => app.CancelPostAction({ queueId }), () => undefined),
    setSecret: (kind, id, password) => call(() => app.SetSecret({ kind, id, password }), () => undefined),
    clearSecret: (kind, id) => call(() => app.ClearSecret({ kind, id }), () => undefined),
    checkForUpdate: () => call(() => app.CheckForUpdate(), parseUpdateInfo),
    installUpdate: () => call(() => app.InstallUpdate(), parseUpdateInfo),
    getTools: () => call(() => (app.GetTools ? app.GetTools() : Promise.reject(noBinding('GetTools'))), parseToolsStatus),
    installTools: (update) =>
      call(
        () => (app.InstallTools ? app.InstallTools(update ? { update: true } : {}) : Promise.reject(noBinding('InstallTools'))),
        parseToolsStatus,
      ),
    setTheme: (theme) =>
      call(() => (app.SetTheme ? app.SetTheme({ theme }) : Promise.reject(noBinding('SetTheme'))), () => undefined),
    fileExists: (id) =>
      call(
        () => (app.FileExists ? app.FileExists({ id }) : Promise.reject(noBinding('FileExists'))),
        (v) => bool(rec(v).exists),
      ),
    openAppFolder: (kind) =>
      call(() => (app.OpenAppFolder ? app.OpenAppFolder({ kind }) : Promise.reject(noBinding('OpenAppFolder'))), () => undefined),
    diagnostics: (opts = {}) =>
      call(
        () => (app.Diagnostics ? app.Diagnostics(opts.webview ? { webview: opts.webview } : {}) : Promise.reject(noBinding('Diagnostics'))),
        parseText,
      ),
    thirdPartyNotices: () =>
      call(() => (app.ThirdPartyNotices ? app.ThirdPartyNotices() : Promise.reject(noBinding('ThirdPartyNotices'))), parseText),
    openExternal: async (url) => {
      // Only https addresses: the page passes fixed ones (the tools' licences).
      if (!/^https:\/\/[^/]/i.test(url)) throw toApiError('invalid_argument: not an https address');
      if (runtime.BrowserOpenURL) runtime.BrowserOpenURL(url);
      else window.open(url, '_blank', 'noopener,noreferrer');
    },
    copyText: async (text) => {
      if (runtime.ClipboardSetText) {
        const ok = await runtime.ClipboardSetText(text);
        if (ok) return;
      }
      await navigator.clipboard.writeText(text);
    },
    onEvents(cb: (batch: EventBatch) => void): Unsubscribe {
      return subscribe(runtime, EVENTS_NAME, (payload) => {
        const batch = parseEvents(payload);
        if (batch.length > 0) cb(batch);
      });
    },
    onConfirmRequest(cb: (id: number) => void): Unsubscribe {
      return subscribe(runtime, CONFIRM_EVENT, (payload) => {
        const id = parseConfirmRequest(payload);
        if (id !== null) cb(id);
      });
    },
    onMenuCommand(cb: (command: MenuCommand) => void): Unsubscribe {
      return subscribe(runtime, MENU_EVENT, (payload) => {
        const command = parseMenuCommand(payload);
        if (command !== null) cb(command);
      });
    },
  };
}
