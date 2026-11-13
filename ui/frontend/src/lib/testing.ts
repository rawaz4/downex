// Test fixtures (imported by *.test.ts only; never by the app).

import { tick } from 'svelte';
import type {
  Api,
  AppFolder,
  ConnectionInfo,
  Download,
  EventBatch,
  MediaInfo,
  MediaTrack,
  MenuCommand,
  Queue,
  Settings,
  StartupView,
  Status,
  ThemeChoice,
  ToolsStatus,
  UpdateInfo,
} from './api';
import { ApiError, DEFAULT_SETTINGS, EMPTY_SCHEDULE, MAIN_QUEUE_ID, numberedName } from './api';

export const KiB = 1024;
export const MiB = 1024 * KiB;
/** Decimal units, as the window shows sizes and speeds (1 KB = 1,000 B). */
export const KB = 1000;
export const MB = 1000 * KB;
export const GB = 1000 * MB;

export function makeDownload(partial: Partial<Download> & { id: number }): Download {
  return {
    url: `https://downloads.example.com/file-${partial.id}.bin`,
    finalUrl: '',
    dir: '/home/you/Downloads',
    filename: `file-${partial.id}.bin`,
    size: 10 * MiB,
    received: 0,
    state: 'queued',
    mode: 'unspecified',
    maxConnections: 0,
    speedLimit: 0,
    error: '',
    errorCode: '',
    createdAt: 1_700_000_000_000 + partial.id * 1000,
    updatedAt: 1_700_000_000_000 + partial.id * 1000,
    completedAt: 0,
    speedBps: 0,
    connections: 0,
    segments: [],
    queuePosition: 0,
    source: 'unspecified',
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
    sizeApprox: false,
    ...partial,
  };
}

export function makeQueue(partial: Partial<Queue> & { id: number }): Queue {
  return {
    name: partial.id === MAIN_QUEUE_ID ? 'Main queue' : `Queue ${partial.id}`,
    running: partial.id === MAIN_QUEUE_ID,
    maxConcurrent: 0,
    speedLimit: 0,
    schedule: EMPTY_SCHEDULE,
    postAction: 'none',
    isMain: partial.id === MAIN_QUEUE_ID,
    downloadIds: [],
    ...partial,
  };
}

/** A worker connection receiving its range. */
export function makeConnection(partial: Partial<ConnectionInfo> & { index: number }): ConnectionInfo {
  return {
    state: 'receiving',
    start: 0,
    end: MiB - 1,
    received: 0,
    speedBps: 0,
    detail: '',
    fragment: -1,
    ...partial,
  };
}

/** A media track: unknown size and file type unless given (a manifest's tracks have none). */
export function makeTrack(partial: Partial<MediaTrack> & Pick<MediaTrack, 'id' | 'kind'>): MediaTrack {
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

/** An HLS presentation: three video variants (one muxed) and two audio renditions. */
export function makeMediaInfo(partial: Partial<MediaInfo> = {}): MediaInfo {
  return {
    manifestUrl: 'https://media.example.org/talk/master.m3u8',
    format: 'hls',
    tracks: [
      makeTrack({ id: 'v0', kind: 'video', bandwidth: 800_000, width: 640, height: 360, frameRate: 25, codecs: 'avc1.4d401e' }),
      makeTrack({ id: 'v1', kind: 'video', bandwidth: 5_000_000, width: 1920, height: 1080, frameRate: 29.97, codecs: 'avc1.640028' }),
      makeTrack({ id: 'v2', kind: 'muxed', bandwidth: 1_200_000, width: 960, height: 540, codecs: 'avc1.4d401f,mp4a.40.2' }),
      makeTrack({ id: 'r0', kind: 'audio', bandwidth: 128_000, codecs: 'mp4a.40.2', language: 'en', name: 'English' }),
      makeTrack({ id: 'r1', kind: 'audio', bandwidth: 96_000, codecs: 'mp4a.40.2', language: 'de', name: 'Deutsch' }),
    ],
    selectedVideo: 'v1',
    selectedAudio: 'r0',
    durationMs: 600_000,
    fragmentsTotal: 200,
    fragmentsDone: 0,
    container: 'mp4',
    title: 'Keynote',
    origin: 'manifest',
    site: '',
    pageUrl: '',
    ...partial,
  };
}

export const PAGE = 'https://www.youtube.com/watch?v=aqz-KE-bpKQ';

/**
 * A web page's video as downexd offers it (SITE-MEDIA-SPEC §3.11, amendment 2026-10-23): one
 * MP4 quality per class under its standard name, best first (H.264 where the class has it, an
 * AV1 one, a muxed one last) with the site's sizes (one an estimate, one unknown), and two MP4
 * audio tracks.
 */
export function makeSiteMediaInfo(partial: Partial<MediaInfo> = {}): MediaInfo {
  return makeMediaInfo({
    manifestUrl: '',
    format: 'http',
    tracks: [
      makeTrack({ id: '299', kind: 'video', bandwidth: 6_000_000, width: 1920, height: 1080, frameRate: 60, codecs: 'avc1.64002a', name: '1080p (Full HD) 60fps', size: 450 * MiB, ext: 'mp4' }),
      makeTrack({ id: '136', kind: 'video', bandwidth: 1_500_000, width: 1280, height: 720, frameRate: 30, codecs: 'avc1.4d401f', name: '720p (HD)', size: 110 * MiB, ext: 'mp4' }),
      makeTrack({ id: '397', kind: 'video', bandwidth: 2_000_000, width: 854, height: 480, frameRate: 30, codecs: 'av01.0.04M.08', name: '480p', size: 150 * MiB, sizeApprox: true, ext: 'mp4' }),
      makeTrack({ id: '18', kind: 'muxed', bandwidth: 600_000, width: 640, height: 360, frameRate: 30, codecs: 'avc1.42001E, mp4a.40.2', name: '360p', ext: 'mp4' }),
      makeTrack({ id: '140', kind: 'audio', bandwidth: 129_000, codecs: 'mp4a.40.2', language: 'en', name: 'English original', size: 10 * MiB, ext: 'm4a' }),
      makeTrack({ id: '140-1', kind: 'audio', bandwidth: 129_000, codecs: 'mp4a.40.2', language: 'de', name: 'Deutsch', size: 10 * MiB, ext: 'm4a' }),
    ],
    selectedVideo: '299',
    selectedAudio: '140',
    fragmentsTotal: 0,
    title: 'A talk',
    origin: 'site',
    site: 'YouTube',
    pageUrl: PAGE,
    ...partial,
  });
}

/** A media download of a web page's video, awaiting confirmation unless told otherwise. */
export function makeSiteDownload(partial: Partial<Download> & { id: number }): Download {
  return makeDownload({
    url: PAGE,
    filename: 'A talk.mp4',
    size: -1,
    category: 'Video',
    kind: 'media',
    media: makeSiteMediaInfo(),
    ...partial,
  });
}

/** Both tools ready (Downex's own copies). */
export function makeTools(partial: Partial<ToolsStatus> = {}): ToolsStatus {
  return {
    enabled: true,
    ready: true,
    tools: [
      { name: 'yt-dlp', state: 'ready', version: '2026.10.10.233141', path: 'C:\\Users\\you\\AppData\\Local\\Downex\\tools\\yt-dlp\\2026.10.10.233141\\yt-dlp.exe', managed: true, latestVersion: '2026.10.10.233141', checkedAt: 1_792_000_000_000, bytesDone: 0, bytesTotal: 0, error: '', errorCode: '', verified: 'openpgp+sha256' },
      { name: 'deno', state: 'ready', version: '2.5.4', path: 'C:\\Users\\you\\AppData\\Local\\Downex\\tools\\deno\\2.5.4\\deno.exe', managed: true, latestVersion: '2.5.4', checkedAt: 1_792_000_000_000, bytesDone: 0, bytesTotal: 0, error: '', errorCode: '', verified: 'sha256+authenticode' },
    ],
    ...partial,
  };
}

/** A media download (HLS, two tracks selected). */
export function makeMediaDownload(partial: Partial<Download> & { id: number }): Download {
  return makeDownload({
    url: 'https://media.example.org/talk/master.m3u8',
    filename: 'Keynote.mp4',
    size: -1,
    category: 'Video',
    kind: 'media',
    media: makeMediaInfo(),
    ...partial,
  });
}

export const REFUSED = 'unavailable: dial tcp 127.0.0.1:47820: connect: connection refused';

/** 214 GiB: what fakeApi's freeSpace answers unless told otherwise. */
export const FAKE_FREE_BYTES = 214 * 1024 * 1024 * 1024;

// ---- custom selects (Select.svelte, DESIGN-SYSTEM.md §3.5) -------------------------------

/** The listbox a select's combobox controls (it is always in the DOM, hidden while closed). */
export function listboxOf(combobox: HTMLElement): HTMLElement {
  const id = combobox.getAttribute('aria-controls') ?? '';
  const listbox = id ? combobox.ownerDocument.getElementById(id) : null;
  if (!listbox) throw new Error(`no listbox for combobox ${combobox.id || combobox.textContent}`);
  return listbox;
}

function optionsOf(combobox: HTMLElement): HTMLElement[] {
  return Array.from(listboxOf(combobox).querySelectorAll<HTMLElement>('[role="option"]'));
}

/** The values of a select's options, in order (as strings: their data-value). */
export function optionValues(combobox: HTMLElement): string[] {
  return optionsOf(combobox).map((o) => o.dataset.value ?? '');
}

/** The visible labels of a select's options, in order (without their detail and meta). */
export function optionLabels(combobox: HTMLElement): string[] {
  return optionsOf(combobox).map((o) => (o.querySelector('[data-option-label]')?.textContent ?? o.textContent ?? '').trim());
}

/**
 * Chooses an option of a select as a user does: opens it, clicks the option whose data-value is
 * `value`, and waits a tick. Throws when there is no such option or it is disabled.
 */
export async function chooseOption(combobox: HTMLElement, value: string | number): Promise<void> {
  const want = String(value);
  if (combobox.getAttribute('aria-expanded') !== 'true') combobox.click();
  await tick();
  const option = optionsOf(combobox).find((o) => o.dataset.value === want);
  if (!option) throw new Error(`no option "${want}" in ${optionValues(combobox).join(', ')}`);
  if (option.getAttribute('aria-disabled') === 'true') throw new Error(`option "${want}" is disabled`);
  option.click();
  await tick();
}

/** What fakeApi can be told at start. */
export interface FakeApiOptions {
  /** Startup's customTitleBar (absent by default: false). */
  readonly customTitleBar?: boolean;
  /** What freeSpace answers: bytes, or an error text such as "unimplemented: …" to reject with. */
  readonly freeBytes?: number | string;
}

/** A scriptable Api over an in-memory list: `down` makes every call fail like a stopped daemon. */
export interface FakeApi extends Api {
  down: boolean;
  settings: Settings;
  readonly items: Map<number, Download>;
  readonly calls: string[];
  /** Arguments of the calls, by name (the last call's). */
  readonly args: Map<string, unknown[]>;
  emit(batch: EventBatch): void;
  /** downexd asks the window to show the confirmation dialog of `id`. */
  requestConfirm(id: number): void;
  /** The user chose a command of the menu bar (macOS). */
  menuCommand(command: MenuCommand): void;
  /** What startup() reports. */
  view: StartupView;
  /** What the page asked of its window. */
  readonly win: {
    title: string;
    sizes: { width: number; height: number }[];
    closed: number;
    size: { width: number; height: number };
    minimised: number;
    maximised: boolean;
  };
  /** What freeSpace answers: bytes, or an error text to reject with. */
  freeBytes: number | string;
  /** Progress windows the page asked for. */
  readonly opened: number[];
  /** The queues (the main queue at start). */
  readonly queues: Map<number, Queue>;
  /** Stored secrets by "<kind>:<id>". */
  readonly secrets: Map<string, string>;
  /** What checkForUpdate answers (null: updates are off). */
  update: UpdateInfo | null;
  /** What pickFile answers. */
  pickedFile: string | null;
  /** The tools' state (getTools; null: a downexd without site media, unimplemented). */
  tools: ToolsStatus | null;
  /**
   * What the next probes and adds of web pages answer, in order (an error string such as
   * "failed_precondition: site_resolving: …", or the offer); then makeSiteMediaInfo.
   */
  readonly siteAnswers: (string | MediaInfo)[];
  /** Addresses openExternal was asked to open. */
  readonly external: string[];
  /** The themes setTheme was given, in order. */
  readonly themes: ThemeChoice[];
  /** The folders openAppFolder was asked to show, in order. */
  readonly folders: AppFolder[];
  /** Downloads whose file fileExists reports as gone (others: there). */
  readonly gone: Set<number>;
  /** What diagnostics and thirdPartyNotices answer. */
  diagnosticsText: string;
  notices: string;
}

export function fakeApi(
  initial: readonly Download[] = [],
  settings: Settings = { ...DEFAULT_SETTINGS, defaultDir: '/home/you/Downloads' },
  options: FakeApiOptions = {},
): FakeApi {
  const items = new Map(initial.map((d) => [d.id, d]));
  const listeners = new Set<(b: EventBatch) => void>();
  const confirmListeners = new Set<(id: number) => void>();
  const menuListeners = new Set<(command: MenuCommand) => void>();
  let nextId = Math.max(0, ...initial.map((d) => d.id)) + 1;
  const status: Status = {
    version: '1.0.0-test',
    pid: 1,
    startedAt: 1,
    active: 0,
    queued: 0,
    endpoint: '127.0.0.1:47820',
    dataDir: '/home/you/.config/Downex',
    logDir: '/home/you/.local/state/Downex',
    toolsDir: '/home/you/.local/share/Downex/tools',
    updatesSupported: false,
  };

  const win = {
    title: '',
    sizes: [] as { width: number; height: number }[],
    closed: 0,
    size: { width: 520, height: 300 },
    minimised: 0,
    maximised: false,
  };
  const opened: number[] = [];
  const queues = new Map<number, Queue>([[MAIN_QUEUE_ID, makeQueue({ id: MAIN_QUEUE_ID, downloadIds: initial.map((d) => d.id) })]]);
  let nextQueue = 2;

  function queue(id: number): Queue {
    const q = queues.get(id);
    if (!q) throw new Error(`not_found: queue ${id} not found`);
    return q;
  }

  function putQueue(q: Queue): Queue {
    queues.set(q.id, q);
    return q;
  }

  function move(ids: readonly number[], queueId: number, position: number): Queue {
    const target = queue(queueId);
    for (const q of [...queues.values()]) {
      putQueue({ ...q, downloadIds: q.downloadIds.filter((id) => !ids.includes(id)) });
    }
    const order = [...queue(queueId).downloadIds];
    order.splice(position < 0 || position > order.length ? order.length : position, 0, ...ids);
    for (const id of ids) if (items.has(id)) update(id, { queueId });
    return putQueue({ ...target, downloadIds: order });
  }

  const api: FakeApi = {
    kind: 'wails',
    down: false,
    settings,
    items,
    calls: [],
    args: new Map(),
    // customTitleBar stays absent (false) unless asked for
    view: {
      view: 'main',
      openWith: true,
      hibernate: true,
      wakeTimers: true,
      ...(options.customTitleBar !== undefined ? { customTitleBar: options.customTitleBar } : {}),
    },
    win,
    freeBytes: options.freeBytes ?? FAKE_FREE_BYTES,
    async freeSpace(dir) {
      gate('freeSpace', dir);
      if (typeof api.freeBytes === 'string') throw new Error(api.freeBytes);
      return api.freeBytes;
    },
    opened,
    queues,
    secrets: new Map(),
    update: null,
    pickedFile: null,
    tools: null,
    siteAnswers: [],
    external: [],
    themes: [],
    folders: [],
    gone: new Set(),
    diagnosticsText: 'Downex window 1.0.0-test (linux/amd64, Go 1.27.0)\n',
    notices: 'THIRD-PARTY NOTICES\n',
    async setTheme(theme) {
      gate('setTheme', theme);
      api.themes.push(theme);
    },
    async fileExists(id) {
      gate('fileExists', id);
      if (!items.has(id)) throw new Error(`not_found: download ${id} not found`);
      return !api.gone.has(id);
    },
    async openAppFolder(folder) {
      gate('openAppFolder', folder);
      api.folders.push(folder);
    },
    async diagnostics(opts) {
      gate('diagnostics', opts);
      return api.diagnosticsText;
    },
    async thirdPartyNotices() {
      gate('thirdPartyNotices');
      return api.notices;
    },
    async getTools() {
      gate('getTools');
      if (!api.tools) throw new Error('unimplemented: downex.v1.DaemonService.GetTools is not implemented');
      return api.tools;
    },
    async installTools(update) {
      gate('installTools', update);
      if (!api.tools) throw new Error('unimplemented: downex.v1.DaemonService.InstallTools is not implemented');
      api.tools = makeTools({ enabled: api.tools.enabled });
      return api.tools;
    },
    async openExternal(url) {
      gate('openExternal', url);
      api.external.push(url);
    },
    async pickFile(opts) {
      gate('pickFile', opts);
      return api.pickedFile;
    },
    async listQueues() {
      gate('listQueues');
      return [...queues.values()];
    },
    async createQueue(spec) {
      gate('createQueue', spec);
      return putQueue(makeQueue({ ...spec, id: nextQueue++, running: false }));
    },
    async updateQueue(id, spec) {
      gate('updateQueue', id, spec);
      return putQueue({ ...queue(id), ...spec });
    },
    async deleteQueue(id) {
      gate('deleteQueue', id);
      if (queue(id).isMain) throw new Error('failed_precondition: the main queue cannot be deleted');
      queues.delete(id);
    },
    async startQueue(id) {
      gate('startQueue', id);
      return putQueue({ ...queue(id), running: true });
    },
    async stopQueue(id) {
      gate('stopQueue', id);
      return putQueue({ ...queue(id), running: false });
    },
    async moveDownloads(ids, queueId, position) {
      gate('moveDownloads', ids, queueId, position);
      return move(ids, queueId, position);
    },
    async cancelPostAction(queueId) {
      gate('cancelPostAction', queueId);
    },
    async setSecret(kind, id, password) {
      gate('setSecret', kind, id, '<password>');
      api.secrets.set(`${kind}:${id}`, password);
    },
    async clearSecret(kind, id) {
      gate('clearSecret', kind, id);
      api.secrets.delete(`${kind}:${id}`);
    },
    async checkForUpdate() {
      gate('checkForUpdate');
      if (!api.update) throw new Error('failed_precondition: updates are off: no update feed is set');
      return api.update;
    },
    async installUpdate() {
      gate('installUpdate');
      if (!api.update?.available) throw new Error('failed_precondition: no update is available');
      return api.update;
    },
    window: {
      setTitle(title) {
        win.title = title;
      },
      setSize(width, height) {
        win.size = { width, height };
        win.sizes.push({ width, height });
      },
      async getSize() {
        return { ...win.size };
      },
      close() {
        win.closed++;
      },
      minimise() {
        win.minimised++;
      },
      toggleMaximise() {
        win.maximised = !win.maximised;
      },
      async isMaximised() {
        return win.maximised;
      },
    },
    async get(id) {
      gate('get', id);
      const d = items.get(id);
      if (!d) throw new Error(`not_found: download ${id} not found`);
      return d;
    },
    async setDownloadOptions(id, opts) {
      gate('setDownloadOptions', id, opts);
      const patch: Partial<Download> = {};
      if (opts.speedLimit !== undefined) Object.assign(patch, { speedLimit: opts.speedLimit });
      if (opts.maxConnections !== undefined) Object.assign(patch, { maxConnections: opts.maxConnections });
      if (opts.completionDialog !== undefined) {
        // As downexd: a completed download takes no options.
        if (items.get(id)?.state === 'completed') throw new Error(`failed_precondition: download ${id} is already completed`);
        Object.assign(patch, { completionDialog: opts.completionDialog });
      }
      return update(id, patch);
    },
    async openProgressWindow(id) {
      gate('openProgressWindow', id);
      opened.push(id);
    },
    async startup() {
      return api.view;
    },
    emit(batch) {
      for (const l of listeners) l(batch);
    },
    menuCommand(command) {
      for (const l of menuListeners) l(command);
    },
    requestConfirm(id) {
      for (const l of confirmListeners) l(id);
    },
    async list() {
      gate('list');
      return [...items.values()];
    },
    async add(req) {
      gate('add', req);
      const d = makeDownload({
        id: nextId++,
        url: req.url,
        filename: req.filename ?? '',
        state: req.startPaused ? 'paused' : 'queued',
        queueId: req.queueId || MAIN_QUEUE_ID,
      });
      items.set(d.id, d);
      return d;
    },
    async pause(id) {
      gate('pause', id);
      return update(id, { state: 'paused', speedBps: 0 });
    },
    async resume(id, opts) {
      gate('resume', id, opts);
      const d = items.get(id);
      if (opts?.existingFile && !opts.restart) throw new Error('invalid_argument: existing_file is only for a restart');
      if (d?.state === 'completed') {
        // As downexd: a completed download is downloaded again only with restart; keep both
        // saves the new copy under a free name unless the file is gone.
        if (!opts?.restart) throw new Error(`failed_precondition: download ${id} is already completed`);
        const keep = opts.existingFile !== 'replace' && !api.gone.has(id);
        api.gone.delete(id);
        return update(id, {
          state: 'queued',
          received: 0,
          completedAt: 0,
          filename: keep ? numberedName(d.filename, 1) : d.filename,
        });
      }
      return update(id, { state: 'queued' });
    },
    async remove(id, opts) {
      gate('remove', id, opts);
      if (!items.delete(id)) throw new Error('not_found: no such download');
    },
    async addMedia(req) {
      gate('addMedia', req);
      if (req.origin === 'site') {
        const info = siteAnswer(req.pageUrl ?? '');
        const d = makeSiteDownload({
          id: nextId++,
          url: req.pageUrl ?? '',
          media: { ...info, selectedVideo: req.videoTrack || info.selectedVideo, selectedAudio: req.audioTrack ?? info.selectedAudio },
        });
        items.set(d.id, d);
        return d;
      }
      if (/drm/.test(req.manifestUrl)) throw new ApiError('invalid_argument', 'encrypted: the stream is DRM-protected');
      const d = makeMediaDownload({
        id: nextId++,
        url: req.manifestUrl,
        media: makeMediaInfo({ manifestUrl: req.manifestUrl, selectedVideo: req.videoTrack || 'v1', selectedAudio: req.audioTrack ?? 'r0' }),
      });
      items.set(d.id, d);
      return d;
    },
    async probeMedia(req) {
      gate('probeMedia', req);
      if (req.origin === 'site') return siteAnswer(req.pageUrl ?? '');
      if (/drm/.test(req.manifestUrl)) throw new ApiError('invalid_argument', 'encrypted: the stream is DRM-protected');
      if (/live/.test(req.manifestUrl)) throw new ApiError('invalid_argument', 'live: the presentation is live');
      return makeMediaInfo({ manifestUrl: req.manifestUrl });
    },
    async confirm(id, opts) {
      gate('confirm', id, opts);
      const d = items.get(id);
      if (!d) throw new Error('not_found: no such download');
      if (d.state !== 'awaiting_confirmation') throw new Error(`failed_precondition: download ${id} is not awaiting confirmation`);
      // As downexd: a named queue takes it, queued (and to its end).
      if (opts.queueId) move([id], opts.queueId, -1);
      return update(id, {
        state: opts.start || opts.queueId ? 'queued' : 'paused',
        dir: opts.dir || d.dir,
        filename: opts.filename || d.filename,
      });
    },
    async pauseAll() {
      gate('pauseAll');
      let n = 0;
      for (const d of [...items.values()]) {
        if (d.state === 'downloading' || d.state === 'queued' || d.state === 'probing') {
          update(d.id, { state: 'paused', speedBps: 0 });
          n++;
        }
      }
      return n;
    },
    async resumeAll() {
      gate('resumeAll');
      let n = 0;
      for (const d of [...items.values()]) {
        if (d.state === 'paused') {
          update(d.id, { state: 'queued' });
          n++;
        }
      }
      return n;
    },
    async getSettings() {
      gate('getSettings');
      return api.settings;
    },
    async updateSettings(s) {
      gate('updateSettings', s);
      // As downexd: new site logins get an id.
      let n = 1;
      const stored = s.siteLogins.some((l) => l.id === '')
        ? { ...s, siteLogins: s.siteLogins.map((l) => (l.id ? l : { ...l, id: `login-${n++}` })) }
        : s;
      api.settings = stored;
      return stored;
    },
    async status() {
      gate('status');
      return status;
    },
    async openFile() {
      gate('openFile');
    },
    async openFolder() {
      gate('openFolder');
    },
    async openWith(id) {
      gate('openWith', id);
    },
    async pickFolder() {
      return null;
    },
    async copyText() {},
    onEvents(cb) {
      listeners.add(cb);
      return () => listeners.delete(cb);
    },
    onConfirmRequest(cb) {
      confirmListeners.add(cb);
      return () => confirmListeners.delete(cb);
    },
    onMenuCommand(cb) {
      menuListeners.add(cb);
      return () => menuListeners.delete(cb);
    },
  };

  /** The next answer of a web page (see siteAnswers). */
  function siteAnswer(pageUrl: string): MediaInfo {
    const next = api.siteAnswers.shift();
    if (typeof next === 'string') throw new Error(next);
    return next ?? makeSiteMediaInfo({ pageUrl });
  }

  function gate(name: string, ...args: unknown[]): void {
    api.calls.push(name);
    api.args.set(name, args);
    if (api.down) throw new Error(REFUSED);
  }

  function update(id: number, patch: Partial<Download>): Download {
    const d = items.get(id);
    if (!d) throw new Error('not_found: no such download');
    const next = { ...d, ...patch, updatedAt: d.updatedAt + 1 };
    items.set(id, next);
    return next;
  }

  return api;
}
