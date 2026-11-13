import { describe, expect, it, vi } from 'vitest';
import { ApiError, DEFAULT_SETTINGS, webviewVersion, type EventBatch, type Settings } from './api';
import { MiB, PAGE, makeTrack } from './testing';
import {
  CONFIRM_EVENT,
  EVENTS_NAME,
  MENU_EVENT,
  createWailsApi,
  parseDownload,
  parseConfirmRequest,
  parseMediaInfo,
  parseMenuCommand,
  parseEvents,
  parsePostAction,
  parseQueue,
  parseFreeSpace,
  parseSettings,
  parseStartup,
  parseStatus,
  parseToolsStatus,
  parseUpdateInfo,
  toWireSettings,
  type WailsAppBindings,
  type WailsRuntime,
  type WireQueue,
} from './wails';

/** What ui/app_m6_test.go (TestQueueBindingsJSONShape) shows the host sends. */
const protojsonQueue = {
  id: '2',
  name: 'Night',
  running: true,
  maxConcurrent: 2,
  speedLimit: '1048576',
  schedule: { enabled: true, startMinute: 1380, stopEnabled: true, stopMinute: 420, weekdays: 31, wakeComputer: true, repeatMinutes: 30 },
  postAction: 'POST_ACTION_SHUTDOWN',
  downloadIds: ['8', '7'],
};

const protojsonUpdate = {
  currentVersion: '0.3.3',
  latestVersion: '0.4.0',
  available: true,
  notes: 'Queues',
  publishedAt: '2026-10-20T08:00:00Z',
  checkedAt: '2026-10-21T09:30:00Z',
};

const protojsonDownload = {
  id: '12',
  url: 'https://downloads.example.com/a.iso',
  dir: 'C:\\Users\\you\\Downloads',
  filename: 'a.iso',
  size: '4294967296',
  received: '1073741824',
  state: 'DOWNLOAD_STATE_DOWNLOADING',
  mode: 'MODE_RANGED',
  createdAt: '2026-10-20T08:00:00Z',
  updatedAt: '2026-10-20T08:05:00.250Z',
  speedBps: 1048576.5,
  connections: 8,
  segments: [
    { start: '0', end: '2147483647', received: '536870912' },
    { start: '2147483648', end: '4294967295', received: '536870912' },
  ],
};

/** What ui/media_test.go (TestMediaDownloadJSONShape) shows the host sends. */
const protojsonMedia = {
  manifestUrl: 'https://media.example.org/talk/master.m3u8',
  format: 'hls',
  tracks: [
    { id: 'v0', kind: 'video', bandwidth: '800000', width: 640, height: 360, frameRate: 25, codecs: 'avc1.4d401e' },
    { id: 'v1', kind: 'video', bandwidth: '5000000', width: 1920, height: 1080, frameRate: 29.97, codecs: 'avc1.640028' },
    { id: 'v2', kind: 'muxed', bandwidth: '1200000', width: 960, height: 540, codecs: 'avc1.4d401f,mp4a.40.2' },
    { id: 'r0', kind: 'audio', bandwidth: '128000', codecs: 'mp4a.40.2', language: 'en', name: 'English' },
    { id: 'r1', kind: 'audio', codecs: 'mp4a.40.2', language: 'de', name: 'Deutsch' },
  ],
  selectedVideo: 'v1',
  selectedAudio: 'r0',
  durationMs: '5400250',
  fragmentsTotal: 1800,
  container: 'mp4',
  title: 'Keynote',
};

/** What ui/media_test.go (TestSiteMediaJSONShape) shows the host sends for a web page's video. */
const protojsonSite = {
  format: 'http',
  container: 'mp4',
  origin: 'MEDIA_ORIGIN_SITE',
  site: 'YouTube',
  pageUrl: PAGE,
  title: 'A talk',
  durationMs: '600000',
  tracks: [
    { id: '137', kind: 'video', bandwidth: '4400000', width: 1920, height: 1080, frameRate: 30, codecs: 'avc1.640028', name: '1080p (Full HD)', size: '330000000', ext: 'mp4' },
    { id: '399', kind: 'video', bandwidth: '2100000', width: 1280, height: 720, frameRate: 30, codecs: 'av01.0.08M.08', name: '720p (HD)', size: '157500000', sizeApprox: true, ext: 'mp4' },
    { id: '18', kind: 'muxed', bandwidth: '600000', width: 640, height: 360, frameRate: 30, codecs: 'avc1.42001E, mp4a.40.2', name: '360p', ext: 'mp4' },
    { id: '140', kind: 'audio', bandwidth: '129000', codecs: 'mp4a.40.2', language: 'en', name: 'medium', size: '9700000', ext: 'm4a' },
  ],
  selectedVideo: '137',
  selectedAudio: '140',
};

/** What ui/media_test.go (TestGetToolsJSONShape) shows the host sends. */
const protojsonTools = {
  enabled: true,
  tools: [
    { name: 'yt-dlp', state: 'TOOL_STATE_INSTALLING', bytesDone: '6291456', bytesTotal: '18874368', latestVersion: '2026.10.10.233141', checkedAt: '2026-10-22T08:00:00Z' },
    { name: 'deno', state: 'TOOL_STATE_FAILED', bytesTotal: '-1', error: 'could not download deno (network)', errorCode: 'network' },
  ],
};

describe('site media (SITE-MEDIA-SPEC §3.1)', () => {
  it('reads a web page’s video: origin, site, page and each quality’s size', () => {
    const info = parseMediaInfo({ media: protojsonSite });
    expect(info).toEqual({
      manifestUrl: '',
      format: 'http',
      tracks: [
        makeTrack({ id: '137', kind: 'video', bandwidth: 4_400_000, width: 1920, height: 1080, frameRate: 30, codecs: 'avc1.640028', name: '1080p (Full HD)', size: 330_000_000, ext: 'mp4' }),
        makeTrack({ id: '399', kind: 'video', bandwidth: 2_100_000, width: 1280, height: 720, frameRate: 30, codecs: 'av01.0.08M.08', name: '720p (HD)', size: 157_500_000, sizeApprox: true, ext: 'mp4' }),
        makeTrack({ id: '18', kind: 'muxed', bandwidth: 600_000, width: 640, height: 360, frameRate: 30, codecs: 'avc1.42001E, mp4a.40.2', name: '360p', ext: 'mp4' }),
        makeTrack({ id: '140', kind: 'audio', bandwidth: 129_000, codecs: 'mp4a.40.2', language: 'en', name: 'medium', size: 9_700_000, ext: 'm4a' }),
      ],
      selectedVideo: '137',
      selectedAudio: '140',
      durationMs: 600_000,
      fragmentsTotal: 0,
      fragmentsDone: 0,
      container: 'mp4',
      title: 'A talk',
      origin: 'site',
      site: 'YouTube',
      pageUrl: PAGE,
    });
    // The origin by number, unknown ones as a manifest; sizes that are no sizes as unknown.
    expect(parseMediaInfo({ origin: 2 }).origin).toBe('site');
    expect(parseMediaInfo({ origin: 'MEDIA_ORIGIN_MANIFEST' }).origin).toBe('manifest');
    expect(parseMediaInfo({ origin: 'MEDIA_ORIGIN_FUTURE' }).origin).toBe('manifest');
    expect(parseMediaInfo({ tracks: [{ id: 'a', kind: 'audio', size: '-5', sizeApprox: 'yes' }] }).tracks[0]).toMatchObject({ size: 0, sizeApprox: false });
  });

  it('reads the tools’ state, from GetTools and from tools events', () => {
    const status = parseToolsStatus({ status: protojsonTools });
    expect(status).toEqual({
      enabled: true,
      ready: false,
      tools: [
        { name: 'yt-dlp', state: 'installing', version: '', path: '', managed: false, latestVersion: '2026.10.10.233141', checkedAt: Date.UTC(2026, 9, 22, 8), bytesDone: 6 * MiB, bytesTotal: 18 * MiB, error: '', errorCode: '', verified: '' },
        { name: 'deno', state: 'failed', version: '', path: '', managed: false, latestVersion: '', checkedAt: 0, bytesDone: 0, bytesTotal: -1, error: 'could not download deno (network)', errorCode: 'network', verified: '' },
      ],
    });
    expect(parseToolsStatus({ tools: [{ state: 3 }, { name: 'deno', state: 'TOOL_STATE_NEW' }] }).tools).toMatchObject([{ name: 'deno', state: 'unspecified' }]);
    const batch = parseEvents({ events: [{ snapshot: { id: '1' } }, { tools: protojsonTools }, { tools: { enabled: true, ready: true } }] });
    expect(batch.map((e) => e.kind)).toEqual(['snapshot', 'tools', 'tools']);
    expect(batch[2]).toEqual({ kind: 'tools', status: { tools: [], enabled: true, ready: true } });
  });

  it('sends a web page as origin MEDIA_ORIGIN_SITE without a manifest; a manifest without an origin', async () => {
    const { app, runtime } = fakeHost();
    const api = createWailsApi(app, runtime);
    app.ProbeMedia.mockResolvedValueOnce({ media: protojsonSite });
    const info = await api.probeMedia({ manifestUrl: '', pageUrl: PAGE, origin: 'site', headers: [{ name: 'Referer', value: 'https://example.org/' }] });
    expect(app.ProbeMedia).toHaveBeenLastCalledWith({ pageUrl: PAGE, origin: 'MEDIA_ORIGIN_SITE', headers: [{ name: 'Referer', value: 'https://example.org/' }] });
    expect(info.origin).toBe('site');
    await api.addMedia({ manifestUrl: '', pageUrl: PAGE, origin: 'site', videoTrack: '399', audioTrack: '140', source: 'ui', queueId: 3 });
    expect(app.AddMedia).toHaveBeenLastCalledWith({ pageUrl: PAGE, origin: 'MEDIA_ORIGIN_SITE', videoTrack: '399', audioTrack: '140', source: 'ADD_SOURCE_UI', queueId: 3 });
    await api.probeMedia({ manifestUrl: 'https://media.example.org/a.m3u8', origin: 'manifest' });
    expect(app.ProbeMedia).toHaveBeenLastCalledWith({ manifestUrl: 'https://media.example.org/a.m3u8' });
    // The daemon's "come back" answers keep their code for the retry loop.
    app.ProbeMedia.mockRejectedValueOnce(new Error('failed_precondition: tools_installing: downloading yt-dlp and Deno (34%)'));
    await expect(api.probeMedia({ manifestUrl: '', pageUrl: PAGE, origin: 'site' })).rejects.toMatchObject({
      code: 'failed_precondition',
      message: 'tools_installing: downloading yt-dlp and Deno (34%)',
    });
  });

  it('gets and installs the tools; a host without those bindings answers unimplemented', async () => {
    const { app, runtime } = fakeHost();
    const api = createWailsApi(app, runtime);
    expect((await api.getTools()).tools.map((t) => t.state)).toEqual(['installing', 'failed']);
    expect(await api.installTools(true)).toEqual({ tools: [], enabled: true, ready: true });
    expect(app.InstallTools).toHaveBeenLastCalledWith({ update: true });
    await api.installTools(false);
    expect(app.InstallTools).toHaveBeenLastCalledWith({});
    app.InstallTools.mockRejectedValueOnce('failed_precondition: tools_unavailable: yt-dlp and Deno are not set up: no yt-dlp build for this platform');
    await expect(api.installTools(false)).rejects.toMatchObject({ code: 'failed_precondition' });

    const older: WailsAppBindings = { ...app, GetTools: undefined, InstallTools: undefined };
    const old = createWailsApi(older, runtime);
    await expect(old.getTools()).rejects.toMatchObject({ code: 'unimplemented' });
    await expect(old.installTools(true)).rejects.toMatchObject({ code: 'unimplemented' });
  });

  it('opens only https addresses in the system’s browser', async () => {
    const { app, runtime } = fakeHost();
    const api = createWailsApi(app, runtime);
    await api.openExternal('https://github.com/yt-dlp/yt-dlp/blob/master/LICENSE');
    expect(runtime.BrowserOpenURL).toHaveBeenCalledWith('https://github.com/yt-dlp/yt-dlp/blob/master/LICENSE');
    await expect(api.openExternal('file:///C:/Windows/system32/calc.exe')).rejects.toMatchObject({ code: 'invalid_argument' });
    await expect(api.openExternal('javascript:alert(1)')).rejects.toBeInstanceOf(ApiError);
    expect(runtime.BrowserOpenURL).toHaveBeenCalledTimes(1);
  });
});

describe('M5 media fields', () => {
  it('reads the kind and the media info of a media download', () => {
    const d = parseDownload({ id: '6', size: '-1', kind: 'DOWNLOAD_KIND_MEDIA', media: { ...protojsonMedia, fragmentsDone: 450 } });
    expect(d.kind).toBe('media');
    expect(d.media).toEqual({
      manifestUrl: 'https://media.example.org/talk/master.m3u8',
      format: 'hls',
      tracks: [
        makeTrack({ id: 'v0', kind: 'video', bandwidth: 800000, width: 640, height: 360, frameRate: 25, codecs: 'avc1.4d401e' }),
        makeTrack({ id: 'v1', kind: 'video', bandwidth: 5000000, width: 1920, height: 1080, frameRate: 29.97, codecs: 'avc1.640028' }),
        makeTrack({ id: 'v2', kind: 'muxed', bandwidth: 1200000, width: 960, height: 540, codecs: 'avc1.4d401f,mp4a.40.2' }),
        makeTrack({ id: 'r0', kind: 'audio', bandwidth: 128000, codecs: 'mp4a.40.2', language: 'en', name: 'English' }),
        makeTrack({ id: 'r1', kind: 'audio', codecs: 'mp4a.40.2', language: 'de', name: 'Deutsch' }),
      ],
      selectedVideo: 'v1',
      selectedAudio: 'r0',
      durationMs: 5400250,
      fragmentsTotal: 1800,
      fragmentsDone: 450,
      container: 'mp4',
      title: 'Keynote',
      // a manifest's origin is the proto default, left out
      origin: 'manifest',
      site: '',
      pageUrl: '',
    });
  });

  it('reads whether a size is downexd’s estimate (a running stream’s)', () => {
    const d = parseDownload({ id: '6', size: '2202009600', sizeApprox: true, kind: 'DOWNLOAD_KIND_MEDIA', media: protojsonMedia });
    expect(d).toMatchObject({ size: 2_202_009_600, sizeApprox: true });
    // proto3 leaves false out; anything but true is exact
    expect(parseDownload({ id: '7', size: '5' }).sizeApprox).toBe(false);
    expect(parseDownload({ id: '8', size: '5', sizeApprox: 'yes' }).sizeApprox).toBe(false);
  });

  it('reads files as kind file without media, whatever the enum says', () => {
    expect(parseDownload({ id: 1 })).toMatchObject({ kind: 'file', media: null });
    expect(parseDownload({ id: 2, kind: 'DOWNLOAD_KIND_FILE' }).kind).toBe('file');
    expect(parseDownload({ id: 3, kind: 'DOWNLOAD_KIND_UNSPECIFIED' }).kind).toBe('file');
    expect(parseDownload({ id: 4, kind: 2 }).kind).toBe('media');
    expect(parseDownload({ id: 5, kind: 'DOWNLOAD_KIND_SOMETHING_NEW' }).kind).toBe('file');
    expect(parseDownload({ id: 6, media: 'junk' }).media).toBeNull();
  });

  it('unwraps ProbeMediaResponse and drops tracks it cannot select', () => {
    const info = parseMediaInfo({
      media: { format: 'dash', tracks: [{ kind: 'video' }, { id: 'a', kind: 'AUDIO' }, { id: 't', kind: 'images' }], durationMs: 60000 },
    });
    expect(info.tracks.map((t) => [t.id, t.kind])).toEqual([
      ['a', 'audio'],
      ['t', 'other'],
    ]);
    expect(info).toMatchObject({ format: 'dash', durationMs: 60000, selectedVideo: '', fragmentsTotal: 0, container: '' });
    expect(parseMediaInfo(null).tracks).toEqual([]);
  });

  it('sends AddMedia, ProbeMedia and the confirmed tracks', async () => {
    const { app, runtime } = fakeHost();
    const api = createWailsApi(app, runtime);
    const added = await api.addMedia({
      manifestUrl: 'https://media.example.org/talk/master.m3u8',
      headers: [{ name: 'Referer', value: 'https://media.example.org/' }],
      dir: '',
      filename: 'talk',
      source: 'ui',
      videoTrack: 'v0',
      audioTrack: '',
      startPaused: false,
    });
    expect(app.AddMedia).toHaveBeenCalledWith({
      manifestUrl: 'https://media.example.org/talk/master.m3u8',
      headers: [{ name: 'Referer', value: 'https://media.example.org/' }],
      filename: 'talk',
      source: 'ADD_SOURCE_UI',
      videoTrack: 'v0',
    });
    expect(added).toMatchObject({ id: 8, kind: 'media', state: 'queued' });
    expect(added.media?.tracks).toHaveLength(5);

    const info = await api.probeMedia({ manifestUrl: 'https://media.example.org/a.mpd', headers: [] });
    expect(app.ProbeMedia).toHaveBeenCalledWith({ manifestUrl: 'https://media.example.org/a.mpd' });
    expect(info).toMatchObject({ selectedVideo: 'v1', durationMs: 5400250 });

    await api.confirm(8, { start: true, videoTrack: 'v2', audioTrack: '' });
    expect(app.Confirm).toHaveBeenLastCalledWith({ id: 8, start: true, videoTrack: 'v2' });
    await api.confirm(8, { start: true, videoTrack: 'v0', audioTrack: 'r1' });
    expect(app.Confirm).toHaveBeenLastCalledWith({ id: 8, start: true, videoTrack: 'v0', audioTrack: 'r1' });

    app.ProbeMedia.mockRejectedValueOnce('invalid_argument: encrypted: the stream is encrypted or DRM-protected');
    await expect(api.probeMedia({ manifestUrl: 'https://media.example.org/drm.m3u8' })).rejects.toMatchObject({
      code: 'invalid_argument',
      message: 'encrypted: the stream is encrypted or DRM-protected',
    });
  });
});

describe('parseDownload', () => {
  it('reads the protojson encoding of downex.v1.Download', () => {
    const d = parseDownload(protojsonDownload);
    expect(d).toMatchObject({
      id: 12,
      size: 4294967296,
      received: 1073741824,
      state: 'downloading',
      mode: 'ranged',
      finalUrl: '',
      error: '',
      errorCode: '',
      maxConnections: 0,
      queuePosition: 0,
      completedAt: 0,
      speedBps: 1048576.5,
    });
    expect(d.createdAt).toBe(Date.UTC(2026, 9, 20, 8, 0, 0));
    expect(d.updatedAt).toBe(Date.UTC(2026, 9, 20, 8, 5, 0, 250));
    expect(d.segments[1]).toEqual({ start: 2147483648, end: 4294967295, received: 536870912 });
  });

  it('accepts plain JSON numbers, lowercase or numeric enums, and Go zero times', () => {
    const d = parseDownload({
      id: 3,
      size: -1,
      state: 'failed',
      mode: 2,
      errorCode: 'validator_changed',
      completedAt: '0001-01-01T00:00:00Z',
      segments: null,
    });
    expect(d).toMatchObject({ id: 3, size: -1, state: 'failed', mode: 'single', completedAt: 0, segments: [] });
    expect(parseDownload({ id: 4, state: 5 }).state).toBe('completed');
    expect(parseDownload({ id: 5, state: 'DOWNLOAD_STATE_SOMETHING_NEW' }).state).toBe('unspecified');
  });

  it('uses proto3 defaults for omitted fields (size 0 is a real size)', () => {
    expect(parseDownload({ id: 6 })).toMatchObject({ size: 0, received: 0, state: 'unspecified', segments: [] });
  });

  it('rejects entries without an id', () => {
    expect(() => parseDownload({ url: 'x' })).toThrow(ApiError);
  });
});

describe('parseEvents', () => {
  it('reads a WatchResponse and skips malformed entries', () => {
    const batch = parseEvents({
      events: [
        { snapshot: { id: '1', state: 'DOWNLOAD_STATE_QUEUED' } },
        { updated: { url: 'missing id' } },
        { updated: { id: 2, state: 'DOWNLOAD_STATE_PAUSED' } },
        { removed: '3' },
        { unknownKind: true },
      ],
    });
    expect(batch.map((e) => e.kind)).toEqual(['snapshot', 'updated', 'removed']);
    expect(batch[2]).toEqual({ kind: 'removed', id: 3 });
  });

  it('accepts a bare array and ignores junk', () => {
    expect(parseEvents([{ removed: 9 }])).toEqual([{ kind: 'removed', id: 9 }]);
    expect(parseEvents('nope')).toEqual([]);
    expect(parseEvents(null)).toEqual([]);
  });
});

/**
 * The M6 and site media settings as parsed when the daemon leaves them out (proto3
 * defaults; an empty yt-dlp channel reads as the default, nightly).
 */
const M6_OMITTED = {
  proxy: { mode: 'unspecified', httpProxy: '', httpsProxy: '', socksProxy: '', bypass: '', pacUrl: '', username: '', hasPassword: false },
  hostRules: [],
  siteLogins: [],
  clipboardMonitor: false,
  markDownloadedFiles: false,
  avScanEnabled: false,
  avScanCommand: '',
  avScanArgs: '',
  updateFeedUrl: '',
  autoCheckUpdates: false,
  siteMedia: false,
  toolsAutoUpdate: false,
  toolsChannel: 'nightly',
} as const;

/** How M6_OMITTED is written back: every field, the proxy mode by name. */
const M6_OMITTED_WIRE = { ...M6_OMITTED, proxy: { ...M6_OMITTED.proxy, mode: 'PROXY_MODE_UNSPECIFIED' } };

describe('parseSettings', () => {
  it('unwraps GetSettingsResponse; omitted fields are their proto3 defaults', () => {
    expect(
      parseSettings({ settings: { maxConcurrentDownloads: 3, globalSpeedLimit: '1048576', defaultDir: '/d', defaultMaxConnections: 8 } }),
    ).toEqual({
      maxConcurrentDownloads: 3,
      globalSpeedLimit: 1048576,
      defaultDir: '/d',
      defaultMaxConnections: 8,
      categories: [],
      useCategoryFolders: false,
      confirmBrowserDownloads: false,
      notifyOnComplete: false,
      notifyOnFailure: false,
      launchAtLogin: false,
      showTrayIcon: false,
      showProgressWindow: false,
      showCompleteDialog: false,
      ...M6_OMITTED,
    });
  });

  it('reads the M6 settings and writes every one of them back (ui/app_m6_test.go shapes)', () => {
    const wire = {
      maxConcurrentDownloads: 3,
      globalSpeedLimit: 0,
      defaultDir: 'C:\\dl',
      defaultMaxConnections: 8,
      categories: [],
      useCategoryFolders: true,
      confirmBrowserDownloads: true,
      notifyOnComplete: true,
      notifyOnFailure: true,
      launchAtLogin: false,
      showTrayIcon: true,
      showProgressWindow: true,
      showCompleteDialog: true,
      proxy: {
        mode: 'PROXY_MODE_MANUAL',
        httpProxy: 'proxy:8080',
        httpsProxy: 'proxy:8443',
        socksProxy: 'socks5://s:1080',
        bypass: '<local>;*.lan',
        pacUrl: '',
        username: 'me',
        hasPassword: true,
      },
      hostRules: [{ pattern: '*.example.com', maxConnections: 4, speedLimit: '2097152', maxRequestBytes: '10485760' }],
      siteLogins: [{ id: 'a1', urlPattern: 'https://example.com/*', username: 'u', hasPassword: true }],
      clipboardMonitor: true,
      markDownloadedFiles: true,
      avScanEnabled: true,
      avScanCommand: 'C:\\Tools\\scan.exe',
      avScanArgs: '--quiet [File]',
      updateFeedUrl: 'https://updates.example/feed.json',
      autoCheckUpdates: true,
      // site media (ui/media_test.go TestUpdateSettingsWritesTheSiteMediaFields)
      siteMedia: true,
      toolsAutoUpdate: false,
      toolsChannel: 'stable',
    };
    const s = parseSettings({ settings: wire });
    expect(s.proxy).toEqual({ ...wire.proxy, mode: 'manual' });
    const rules = [{ pattern: '*.example.com', maxConnections: 4, speedLimit: 2 << 20, maxRequestBytes: 10 << 20 }];
    expect(s.hostRules).toEqual(rules);
    expect(s.siteLogins).toEqual(wire.siteLogins);
    expect(s).toMatchObject({ siteMedia: true, toolsAutoUpdate: false, toolsChannel: 'stable' });
    expect(toWireSettings(s)).toEqual({ ...wire, hostRules: rules });
    // A channel this window does not know reads as the default (downexd repairs it too).
    expect(parseSettings({ toolsChannel: 'beta' }).toolsChannel).toBe('nightly');
    // Modes by number and unknown ones; entries without fields are kept to be written back.
    expect(parseSettings({ proxy: { mode: 4 } }).proxy.mode).toBe('pac');
    expect(parseSettings({ proxy: { mode: 'PROXY_MODE_NONE' } }).proxy.mode).toBe('none');
    expect(parseSettings({ proxy: { mode: 'PROXY_MODE_FUTURE' } }).proxy.mode).toBe('unspecified');
    expect(parseSettings({ siteLogins: [{}], hostRules: [{}] })).toMatchObject({
      siteLogins: [{ id: '', urlPattern: '', username: '', hasPassword: false }],
      hostRules: [{ pattern: '', maxConnections: 0, speedLimit: 0, maxRequestBytes: 0 }],
    });
  });

  it('reads categories and switches, and writes every field back unchanged', () => {
    const wire = {
      maxConcurrentDownloads: 3,
      globalSpeedLimit: 0,
      defaultDir: 'C:\\Users\\you\\Downloads',
      defaultMaxConnections: 8,
      categories: [
        { name: 'Video', extensions: ['mp4', 'mkv'] },
        { name: 'Books', extensions: ['epub', 7], dir: 'D:\\Books' },
        { extensions: null },
      ],
      useCategoryFolders: true,
      confirmBrowserDownloads: true,
      notifyOnFailure: true,
      showTrayIcon: true,
      showProgressWindow: true,
    };
    const s = parseSettings({ settings: wire });
    expect(s.categories).toEqual([
      { name: 'Video', extensions: ['mp4', 'mkv'], dir: '' },
      { name: 'Books', extensions: ['epub'], dir: 'D:\\Books' },
      { name: '', extensions: [], dir: '' }, // kept: the daemon decides
    ]);
    expect(s).toMatchObject({ useCategoryFolders: true, confirmBrowserDownloads: true, notifyOnComplete: false, launchAtLogin: false });
    expect(toWireSettings(s)).toEqual({
      ...wire,
      categories: s.categories.map((c) => ({ ...c })),
      notifyOnComplete: false,
      launchAtLogin: false,
      showCompleteDialog: false,
      ...M6_OMITTED_WIRE,
    });
    expect(s).toMatchObject({ showProgressWindow: true, showCompleteDialog: false });
  });
});

describe('M4.5 download fields', () => {
  it('reads the awaiting state, the source and the hints', () => {
    const d = parseDownload({
      id: '9',
      state: 'DOWNLOAD_STATE_AWAITING_CONFIRMATION',
      source: 'ADD_SOURCE_BROWSER',
      expectedSize: '8589934592',
      mime: 'video/mp4',
      category: 'Video',
    });
    expect(d).toMatchObject({ state: 'awaiting_confirmation', source: 'browser', expectedSize: 8589934592, mime: 'video/mp4', category: 'Video' });
    expect(parseDownload({ id: 1, state: 8, source: 2 })).toMatchObject({ state: 'awaiting_confirmation', source: 'ui' });
    expect(parseDownload({ id: 2 })).toMatchObject({ source: 'unspecified', expectedSize: 0, mime: '', category: '' });
    expect(parseDownload({ id: 3, source: 'ADD_SOURCE_SOMETHING_NEW' }).source).toBe('unspecified');
  });

  it('reads the confirmation request payload', () => {
    expect(parseConfirmRequest({ id: '42' })).toBe(42);
    expect(parseConfirmRequest({ id: 7 })).toBe(7);
    for (const bad of [{ id: '0' }, { id: '-1' }, { id: 'x' }, {}, null, '5', { id: '1.5' }]) {
      expect(parseConfirmRequest(bad)).toBeNull();
    }
  });
});

function fakeHost() {
  const handlers = new Map<string, (...data: unknown[]) => void>();
  const off = vi.fn();
  const app = {
    List: vi.fn(async () => ({ downloads: [protojsonDownload] })),
    Add: vi.fn(async () => ({ download: { id: '7', state: 'DOWNLOAD_STATE_QUEUED' } })),
    Pause: vi.fn(async () => ({ download: { id: '7', state: 'DOWNLOAD_STATE_PAUSED' } })),
    Resume: vi.fn(async () => ({ download: { id: '7', state: 'DOWNLOAD_STATE_QUEUED' } })),
    Remove: vi.fn(async () => ({})),
    Confirm: vi.fn(async () => ({ download: { id: '7', state: 'DOWNLOAD_STATE_QUEUED' } })),
    AddMedia: vi.fn(async () => ({ download: { id: '8', state: 'DOWNLOAD_STATE_QUEUED', kind: 'DOWNLOAD_KIND_MEDIA', media: protojsonMedia } })),
    ProbeMedia: vi.fn(async (): Promise<unknown> => ({ media: protojsonMedia })),
    PauseAll: vi.fn(async () => ({ paused: 3 })),
    ResumeAll: vi.fn(async () => ({})),
    GetSettings: vi.fn(async () => ({ settings: { maxConcurrentDownloads: 3, defaultDir: '/d', defaultMaxConnections: 8 } })),
    UpdateSettings: vi.fn(async (req: { settings: unknown }) => req as unknown),
    Status: vi.fn(async () => ({ version: '1.0.0', pid: 10, startedAt: '2026-10-20T08:00:00Z', active: 1, endpoint: 'pipe' })),
    OpenFile: vi.fn(async () => undefined),
    OpenFolder: vi.fn(async () => undefined),
    OpenWith: vi.fn(async () => undefined),
    PickFolder: vi.fn(async () => ''),
    Get: vi.fn(async () => ({ download: protojsonDownload })),
    SetDownloadOptions: vi.fn(async (req: { id: number; speedLimit?: number }) => ({
      download: { id: String(req.id), speedLimit: String(req.speedLimit ?? 0), state: 'DOWNLOAD_STATE_DOWNLOADING' },
    })),
    OpenProgressWindow: vi.fn(async () => undefined),
    Startup: vi.fn(async (): Promise<unknown> => ({ view: 'main', openWith: true })),
    PickFile: vi.fn(async (): Promise<unknown> => 'C:\\Tools\\scan.exe'),
    ListQueues: vi.fn(async (): Promise<unknown> => ({ queues: [protojsonQueue, { name: 'no id' }] })),
    CreateQueue: vi.fn(async (req: { queue: WireQueue }): Promise<unknown> => ({ queue: { ...req.queue, id: '2' } })),
    UpdateQueue: vi.fn(async (req: { queue: WireQueue }): Promise<unknown> => ({ queue: req.queue })),
    DeleteQueue: vi.fn(async (): Promise<unknown> => ({})),
    StartQueue: vi.fn(async (req: { id: number }): Promise<unknown> => ({ queue: { id: String(req.id), running: true } })),
    StopQueue: vi.fn(async (req: { id: number }): Promise<unknown> => ({ queue: { id: String(req.id) } })),
    MoveDownloads: vi.fn(async (req: { ids: number[]; queueId: number }): Promise<unknown> => ({
      queue: { id: String(req.queueId), downloadIds: req.ids.map(String) },
    })),
    CancelPostAction: vi.fn(async (): Promise<unknown> => ({})),
    SetSecret: vi.fn(async (): Promise<unknown> => ({})),
    ClearSecret: vi.fn(async (): Promise<unknown> => ({})),
    CheckForUpdate: vi.fn(async (): Promise<unknown> => ({ info: protojsonUpdate })),
    InstallUpdate: vi.fn(async (): Promise<unknown> => ({ info: protojsonUpdate })),
    GetTools: vi.fn(async (): Promise<unknown> => ({ status: protojsonTools })),
    InstallTools: vi.fn(async (): Promise<unknown> => ({ status: { enabled: true, ready: true } })),
    FreeSpace: vi.fn(async (): Promise<unknown> => ({ freeBytes: '229780750336' })),
  } satisfies WailsAppBindings;
  const runtime = {
    EventsOn: vi.fn((name: string, cb: (...data: unknown[]) => void) => {
      handlers.set(name, cb);
      return off;
    }),
    ClipboardSetText: vi.fn(async () => true),
    WindowSetTitle: vi.fn(),
    WindowSetSize: vi.fn(),
    WindowGetSize: vi.fn(async () => ({ w: 600, h: 300 })),
    WindowMinimise: vi.fn(),
    WindowToggleMaximise: vi.fn(),
    WindowIsMaximised: vi.fn(async () => true),
    Quit: vi.fn(),
    BrowserOpenURL: vi.fn(),
  } satisfies WailsRuntime;
  return { app, runtime, handlers, off };
}

describe('progress window contract', () => {
  it('reads connection_info with its states, and none for other downloads', () => {
    const d = parseDownload({
      ...protojsonDownload,
      connectionInfo: [
        { index: 1, state: 'CONNECTION_STATE_RECEIVING', end: '2147483647', received: '1048576', speedBps: 524288.5, fragment: -1 },
        { index: 2, state: 'CONNECTION_STATE_RETRYING', start: '2147483648', end: '4294967295', detail: 'Retrying in 4 s (HTTP 503)', fragment: -1 },
        { index: 3, state: 4 },
        { index: 4, state: 'CONNECTION_STATE_SOMETHING_NEW' },
      ],
    });
    expect(d.connectionInfo).toEqual([
      { index: 1, state: 'receiving', start: 0, end: 2147483647, received: 1048576, speedBps: 524288.5, detail: '', fragment: -1 },
      { index: 2, state: 'retrying', start: 2147483648, end: 4294967295, received: 0, speedBps: 0, detail: 'Retrying in 4 s (HTTP 503)', fragment: -1 },
      { index: 3, state: 'retrying', start: 0, end: 0, received: 0, speedBps: 0, detail: '', fragment: 0 },
      { index: 4, state: 'unspecified', start: 0, end: 0, received: 0, speedBps: 0, detail: '', fragment: 0 },
    ]);
    expect(parseDownload(protojsonDownload).connectionInfo).toEqual([]);
    expect(parseDownload({ id: 1, connectionInfo: 'junk' }).connectionInfo).toEqual([]);
  });

  it('reads the completion dialog mark and the running time (DESKTOP-SPEC §8)', () => {
    const d = parseDownload({
      ...protojsonDownload,
      state: 'DOWNLOAD_STATE_COMPLETED',
      completionDialog: true,
      activeMs: '754250',
      startedAt: '2026-10-20T08:00:05Z',
    });
    expect(d).toMatchObject({ completionDialog: true, activeMs: 754250, startedAt: Date.UTC(2026, 9, 20, 8, 0, 5) });
    // Omitted (proto3 defaults), the Go zero time, junk.
    expect(parseDownload({ id: 1 })).toMatchObject({ completionDialog: false, activeMs: 0, startedAt: 0 });
    expect(parseDownload({ id: 2, completionDialog: 'yes', activeMs: '-5', startedAt: '0001-01-01T00:00:00Z' })).toMatchObject({
      completionDialog: false,
      activeMs: 0,
      startedAt: 0,
    });
  });

  it('calls OpenWith and passes its refusals on', async () => {
    const { app, runtime } = fakeHost();
    const api = createWailsApi(app, runtime);
    await api.openWith(12);
    expect(app.OpenWith).toHaveBeenCalledWith({ id: 12 });
    app.OpenWith.mockRejectedValueOnce('canceled: no app was chosen');
    await expect(api.openWith(12)).rejects.toMatchObject({ code: 'canceled', message: 'no app was chosen' });
    app.OpenWith.mockRejectedValueOnce('unimplemented: choosing an app is not available on this system yet');
    await expect(api.openWith(12)).rejects.toMatchObject({ code: 'unimplemented' });
  });

  it('reads the startup view', () => {
    expect(parseStartup({ view: 'main', openWith: true })).toMatchObject({ view: 'main', openWith: true });
    expect(parseStartup({ view: 'progress', id: '42', openWith: true })).toMatchObject({ view: 'progress', id: 42, openWith: true });
    expect(parseStartup({ view: 'progress', id: 0 })).toMatchObject({ view: 'main', openWith: true });
    expect(parseStartup({ view: 'progress' })).toMatchObject({ view: 'main', openWith: true });
    expect(parseStartup({ view: 'something-else', id: '3' })).toMatchObject({ view: 'main', openWith: true });
    expect(parseStartup(null)).toEqual({ view: 'main', openWith: true, hibernate: true, wakeTimers: false, customTitleBar: false });
  });

  it('reads what the system can do after a queue and for its schedule (M6)', () => {
    // As ui/progress.go sends them: Windows, macOS.
    expect(parseStartup({ view: 'main', openWith: true, hibernate: true, wakeTimers: true })).toEqual({
      view: 'main',
      openWith: true,
      hibernate: true,
      wakeTimers: true,
      customTitleBar: false,
    });
    expect(parseStartup({ view: 'progress', id: '4', openWith: false, hibernate: false, wakeTimers: false })).toEqual({
      view: 'progress',
      id: 4,
      openWith: false,
      hibernate: false,
      wakeTimers: false,
      customTitleBar: false,
    });
    // Hibernation unless the host says no; waking the computer only when it says yes.
    expect(parseStartup({ view: 'main', wakeTimers: 'yes' })).toMatchObject({ hibernate: true, wakeTimers: false });
  });

  it('reads whether the page draws the title bar (the frameless Windows main window)', () => {
    // As ui/main.go sends it (always): the main view on Windows, a progress window, macOS.
    expect(parseStartup({ view: 'main', openWith: true, customTitleBar: true })).toMatchObject({ view: 'main', customTitleBar: true });
    expect(parseStartup({ view: 'progress', id: '4', customTitleBar: false })).toMatchObject({ view: 'progress', customTitleBar: false });
    // An older host does not say: its window has the system's frame.
    expect(parseStartup({ view: 'main' }).customTitleBar).toBe(false);
    expect(parseStartup({ view: 'main', customTitleBar: 'true' }).customTitleBar).toBe(false);
    expect(parseStartup({ view: 'main', customTitleBar: 1 }).customTitleBar).toBe(false);
  });

  it('reads whether Open with… is available (macOS: not yet)', () => {
    expect(parseStartup({ view: 'progress', id: '42', openWith: false })).toMatchObject({ view: 'progress', id: 42, openWith: false });
    expect(parseStartup({ view: 'main', openWith: false })).toMatchObject({ view: 'main', openWith: false });
    // Only an explicit false hides it: a host that does not say had the dialog (Windows).
    expect(parseStartup({ view: 'main' }).openWith).toBe(true);
    expect(parseStartup({ view: 'main', openWith: 'no' }).openWith).toBe(true);
  });

  it('calls Get, SetDownloadOptions, OpenProgressWindow and Startup', async () => {
    const { app, runtime } = fakeHost();
    const api = createWailsApi(app, runtime);
    expect((await api.get(12)).id).toBe(12);
    expect(app.Get).toHaveBeenCalledWith({ id: 12 });

    expect((await api.setDownloadOptions(12, { speedLimit: 1048576 })).speedLimit).toBe(1048576);
    expect(app.SetDownloadOptions).toHaveBeenLastCalledWith({ id: 12, speedLimit: 1048576 });
    await api.setDownloadOptions(12, { speedLimit: 0, maxConnections: 4 });
    expect(app.SetDownloadOptions).toHaveBeenLastCalledWith({ id: 12, speedLimit: 0, maxConnections: 4 });
    await api.setDownloadOptions(12, {});
    expect(app.SetDownloadOptions).toHaveBeenLastCalledWith({ id: 12 });
    await api.setDownloadOptions(12, { completionDialog: true });
    expect(app.SetDownloadOptions).toHaveBeenLastCalledWith({ id: 12, completionDialog: true });

    await api.openProgressWindow(12);
    expect(app.OpenProgressWindow).toHaveBeenCalledWith({ id: 12 });
    app.OpenProgressWindow.mockRejectedValueOnce('internal: cannot open the progress window: no such file');
    await expect(api.openProgressWindow(13)).rejects.toMatchObject({ code: 'internal' });

    expect(await api.startup()).toMatchObject({ view: 'main', openWith: true });
    app.Startup.mockResolvedValueOnce({ view: 'progress', id: '12', openWith: false });
    expect(await api.startup()).toMatchObject({ view: 'progress', id: 12, openWith: false });
  });

  it('controls its window through the Wails runtime', async () => {
    const { app, runtime } = fakeHost();
    const api = createWailsApi(app, runtime);
    api.window.setTitle('42% a.iso');
    expect(runtime.WindowSetTitle).toHaveBeenCalledWith('42% a.iso');
    api.window.setSize(520.4, 560);
    expect(runtime.WindowSetSize).toHaveBeenCalledWith(520, 560);
    expect(await api.window.getSize()).toEqual({ width: 600, height: 300 });
    runtime.WindowGetSize.mockRejectedValueOnce(new Error('no window'));
    expect(await api.window.getSize()).toBeNull();
    api.window.close();
    expect(runtime.Quit).toHaveBeenCalled();

    // A runtime without window functions: the title goes to the document, the rest does nothing.
    const bare = createWailsApi(app, { EventsOn: () => () => {} });
    bare.window.setTitle('7% b.zip');
    expect(document.title).toBe('7% b.zip');
    bare.window.setSize(1, 1);
    expect(await bare.window.getSize()).toBeNull();
    bare.window.minimise();
    bare.window.toggleMaximise();
    expect(await bare.window.isMaximised()).toBe(false);
  });

  it('minimises, maximises and restores through the Wails runtime', async () => {
    const { app, runtime } = fakeHost();
    const api = createWailsApi(app, runtime);
    api.window.minimise();
    expect(runtime.WindowMinimise).toHaveBeenCalledTimes(1);
    api.window.toggleMaximise();
    expect(runtime.WindowToggleMaximise).toHaveBeenCalledTimes(1);
    expect(await api.window.isMaximised()).toBe(true);
    runtime.WindowIsMaximised.mockResolvedValueOnce(false);
    expect(await api.window.isMaximised()).toBe(false);
    runtime.WindowIsMaximised.mockRejectedValueOnce(new Error('no window'));
    expect(await api.window.isMaximised()).toBe(false);
  });

  it('reads the free space of a folder (FreeSpace)', async () => {
    const { app, runtime } = fakeHost();
    const api = createWailsApi(app, runtime);
    expect(await api.freeSpace('D:\\Downloads')).toBe(229_780_750_336);
    expect(app.FreeSpace).toHaveBeenCalledWith({ dir: 'D:\\Downloads' });
    // int64 as a JSON number, and the proto3 default
    app.FreeSpace.mockResolvedValueOnce({ freeBytes: 1024 });
    expect(await api.freeSpace('/home/you')).toBe(1024);
    app.FreeSpace.mockResolvedValueOnce({});
    expect(await api.freeSpace('/home/you')).toBe(0);
    // the host's refusals keep their code; a nonsense answer is internal
    app.FreeSpace.mockRejectedValueOnce('invalid_argument: dir must be an absolute path');
    await expect(api.freeSpace('Downloads')).rejects.toMatchObject({ code: 'invalid_argument' });
    app.FreeSpace.mockRejectedValueOnce('unavailable: could not read the free space');
    await expect(api.freeSpace('Z:\\')).rejects.toMatchObject({ code: 'unavailable' });
    app.FreeSpace.mockResolvedValueOnce({ freeBytes: '-5' });
    await expect(api.freeSpace('/')).rejects.toMatchObject({ code: 'internal' });
    app.FreeSpace.mockResolvedValueOnce({ freeBytes: '1.5' });
    await expect(api.freeSpace('/')).rejects.toMatchObject({ code: 'internal' });
    expect(parseFreeSpace({ freeBytes: '0' })).toBe(0);

    // A host built before the binding: unimplemented, so the page hides the free space.
    const { FreeSpace: _gone, ...older } = app;
    const old = createWailsApi(older, runtime);
    await expect(old.freeSpace('/')).rejects.toMatchObject({ code: 'unimplemented' });
  });
});

describe('createWailsApi', () => {
  it('sends request-shaped arguments and unwraps responses', async () => {
    const { app, runtime } = fakeHost();
    const api = createWailsApi(app, runtime);
    expect(api.kind).toBe('wails');

    expect((await api.list())[0]?.id).toBe(12);
    await api.add({ url: 'https://example.com/x', dir: '', maxConnections: 4, speedLimit: 0, startPaused: true });
    expect(app.Add).toHaveBeenCalledWith({ url: 'https://example.com/x', maxConnections: 4, startPaused: true });
    expect((await api.pause(7)).state).toBe('paused');
    expect(app.Pause).toHaveBeenCalledWith({ id: 7 });
    await api.resume(7, { restart: true, speedLimit: 1024 });
    expect(app.Resume).toHaveBeenCalledWith({ id: 7, restart: true, speedLimit: 1024 });
    await api.resume(7);
    expect(app.Resume).toHaveBeenLastCalledWith({ id: 7, restart: false });
    await api.remove(7, { deleteFile: true });
    expect(app.Remove).toHaveBeenCalledWith({ id: 7, deleteFile: true });

    const s: Settings = { ...DEFAULT_SETTINGS, maxConcurrentDownloads: 4, defaultDir: '/x', defaultMaxConnections: 16, launchAtLogin: true };
    expect(await api.updateSettings(s)).toEqual(s);
    expect(app.UpdateSettings).toHaveBeenCalledWith({ settings: toWireSettings(s) });
    expect(toWireSettings(s).proxy.mode).toBe('PROXY_MODE_SYSTEM');
    expect((await api.getSettings()).globalSpeedLimit).toBe(0);
    expect(await api.status()).toMatchObject({ version: '1.0.0', active: 1, queued: 0 });
    expect(await api.pickFolder('/start')).toBeNull();
    expect(app.PickFolder).toHaveBeenCalledWith({ initialDir: '/start' });
    await api.copyText('hello');
    expect(runtime.ClipboardSetText).toHaveBeenCalledWith('hello');
  });

  it('turns rejected calls into ApiError with the Connect code', async () => {
    const { app, runtime } = fakeHost();
    app.Pause.mockRejectedValueOnce('failed_precondition: download 7 is completed');
    app.Resume.mockRejectedValueOnce(new Error('daemon not reachable'));
    const api = createWailsApi(app, runtime);
    await expect(api.pause(7)).rejects.toMatchObject({ code: 'failed_precondition', message: 'download 7 is completed' });
    await expect(api.resume(7)).rejects.toMatchObject({ code: 'unknown', message: 'daemon not reachable' });
  });

  it('subscribes to downex:events and unsubscribes', () => {
    const { app, runtime, handlers, off } = fakeHost();
    const api = createWailsApi(app, runtime);
    const batches: EventBatch[] = [];
    const unsubscribe = api.onEvents((b) => batches.push(b));
    expect(runtime.EventsOn).toHaveBeenCalledWith(EVENTS_NAME, expect.any(Function));
    handlers.get(EVENTS_NAME)?.({ events: [{ removed: '5' }] });
    handlers.get(EVENTS_NAME)?.({ events: [] });
    expect(batches).toEqual([[{ kind: 'removed', id: 5 }]]);
    unsubscribe();
    expect(off).toHaveBeenCalled();
  });

  it('confirms, pauses and resumes all, and sends the Add hints', async () => {
    const { app, runtime } = fakeHost();
    const api = createWailsApi(app, runtime);
    expect((await api.confirm(7, { start: true, dir: 'D:\\Films', filename: '' })).state).toBe('queued');
    expect(app.Confirm).toHaveBeenLastCalledWith({ id: 7, start: true, dir: 'D:\\Films' });
    await api.confirm(7, { start: false, maxConnections: 4, speedLimit: 0 });
    expect(app.Confirm).toHaveBeenLastCalledWith({ id: 7, start: false, maxConnections: 4, speedLimit: 0 });
    expect(await api.pauseAll()).toBe(3);
    expect(await api.resumeAll()).toBe(0); // {} = none resumed
    await api.add({ url: 'https://example.com/y', source: 'ui', expectedSize: 2048, mime: 'application/zip' });
    expect(app.Add).toHaveBeenLastCalledWith({
      url: 'https://example.com/y',
      source: 'ADD_SOURCE_UI',
      expectedSize: 2048,
      mime: 'application/zip',
    });
    await api.add({ url: 'https://example.com/z', source: 'unspecified', expectedSize: -1 });
    expect(app.Add).toHaveBeenLastCalledWith({ url: 'https://example.com/z' });
    app.Confirm.mockRejectedValueOnce('failed_precondition: download 7 is not awaiting confirmation');
    await expect(api.confirm(7, { start: true })).rejects.toMatchObject({ code: 'failed_precondition' });
  });

  it('subscribes to downex:confirm', () => {
    const { app, runtime, handlers, off } = fakeHost();
    const api = createWailsApi(app, runtime);
    const ids: number[] = [];
    const unsubscribe = api.onConfirmRequest((id) => ids.push(id));
    expect(runtime.EventsOn).toHaveBeenCalledWith(CONFIRM_EVENT, expect.any(Function));
    handlers.get(CONFIRM_EVENT)?.({ id: '15' });
    handlers.get(CONFIRM_EVENT)?.({ id: 'junk' });
    expect(ids).toEqual([15]);
    unsubscribe();
    expect(off).toHaveBeenCalled();
  });

  it('subscribes to downex:menu (the macOS menu bar)', () => {
    const { app, runtime, handlers, off } = fakeHost();
    const api = createWailsApi(app, runtime);
    const commands: string[] = [];
    const unsubscribe = api.onMenuCommand((command) => commands.push(command));
    expect(runtime.EventsOn).toHaveBeenCalledWith(MENU_EVENT, expect.any(Function));
    handlers.get(MENU_EVENT)?.({ command: 'settings' });
    handlers.get(MENU_EVENT)?.({ command: 'something-newer' });
    handlers.get(MENU_EVENT)?.(null);
    expect(commands).toEqual(['settings']);
    expect(parseMenuCommand({ command: 'settings' })).toBe('settings');
    expect(parseMenuCommand('settings')).toBeNull();
    unsubscribe();
    expect(off).toHaveBeenCalled();
  });
});

describe('M6 bindings', () => {
  it('reads queues, their schedules and actions (ui/app_m6_test.go shapes)', async () => {
    const { app, runtime } = fakeHost();
    const api = createWailsApi(app, runtime);
    const list = await api.listQueues();
    // A queue without an id cannot be addressed: it is left out.
    expect(list).toEqual([
      {
        id: 2,
        name: 'Night',
        running: true,
        maxConcurrent: 2,
        speedLimit: 1048576,
        schedule: { enabled: true, startMinute: 1380, stopEnabled: true, stopMinute: 420, weekdays: 31, onceDate: '', wakeComputer: true, repeatMinutes: 30 },
        postAction: 'shutdown',
        isMain: false,
        downloadIds: [8, 7],
      },
    ]);
    expect(parseQueue({ id: '1', isMain: true, postAction: 'POST_ACTION_UNSPECIFIED' })).toMatchObject({
      id: 1,
      isMain: true,
      postAction: 'none',
      schedule: { enabled: false, weekdays: 0, onceDate: '' },
      downloadIds: [],
    });
    expect(parsePostAction(3)).toBe('hibernate');
    expect(parsePostAction('POST_ACTION_QUIT_DOWNEX')).toBe('quit_downex');
    expect(parsePostAction('POST_ACTION_SOMETHING_NEW')).toBe('none');
  });

  it('sends queue requests as full protojson messages', async () => {
    const { app, runtime } = fakeHost();
    const api = createWailsApi(app, runtime);
    const spec = {
      name: 'Weekend',
      maxConcurrent: 0,
      speedLimit: 0,
      schedule: { enabled: true, startMinute: 60, stopEnabled: false, stopMinute: 0, weekdays: 0, onceDate: '2026-10-24', wakeComputer: false, repeatMinutes: 0 },
      postAction: 'none' as const,
    };
    expect((await api.createQueue(spec)).id).toBe(2);
    expect(app.CreateQueue).toHaveBeenCalledWith({ queue: { ...spec, postAction: 'POST_ACTION_UNSPECIFIED' } });
    await api.updateQueue(5, { ...spec, postAction: 'sleep' });
    expect(app.UpdateQueue).toHaveBeenCalledWith({ queue: { ...spec, id: 5, postAction: 'POST_ACTION_SLEEP' } });
    expect((await api.startQueue(3)).running).toBe(true);
    expect(app.StartQueue).toHaveBeenCalledWith({ id: 3 });
    expect((await api.stopQueue(3)).running).toBe(false);
    await api.deleteQueue(3);
    expect(app.DeleteQueue).toHaveBeenCalledWith({ id: 3 });
    expect((await api.moveDownloads([7, 8], 2, -1)).downloadIds).toEqual([7, 8]);
    expect(app.MoveDownloads).toHaveBeenCalledWith({ ids: [7, 8], queueId: 2, position: -1 });
    await api.cancelPostAction(2);
    expect(app.CancelPostAction).toHaveBeenCalledWith({ queueId: 2 });
    app.DeleteQueue.mockRejectedValueOnce('failed_precondition: the main queue cannot be deleted');
    await expect(api.deleteQueue(1)).rejects.toMatchObject({ code: 'failed_precondition' });
  });

  it('puts downloads into a queue when adding and confirming', async () => {
    const { app, runtime } = fakeHost();
    const api = createWailsApi(app, runtime);
    await api.add({ url: 'https://example.com/a', queueId: 3 });
    expect(app.Add).toHaveBeenLastCalledWith({ url: 'https://example.com/a', queueId: 3 });
    await api.add({ url: 'https://example.com/b', queueId: 0 });
    expect(app.Add).toHaveBeenLastCalledWith({ url: 'https://example.com/b' });
    await api.confirm(7, { start: false, queueId: 3 });
    expect(app.Confirm).toHaveBeenLastCalledWith({ id: 7, start: false, queueId: 3 });
    await api.addMedia({ manifestUrl: 'https://media.example.org/a.m3u8', queueId: 4 });
    expect(app.AddMedia).toHaveBeenLastCalledWith({ manifestUrl: 'https://media.example.org/a.m3u8', queueId: 4 });
    expect(parseDownload({ id: 1, queueId: '3' }).queueId).toBe(3);
    expect(parseDownload({ id: 1 }).queueId).toBe(0);
    expect(parseDownload({ id: 1, source: 'ADD_SOURCE_CLIPBOARD' }).source).toBe('clipboard');
  });

  it('reads queue and post-action events', () => {
    const batch = parseEvents({
      events: [
        { queueUpdated: protojsonQueue },
        { queueRemoved: '3' },
        { postAction: { queueId: '2', action: 'POST_ACTION_SLEEP', deadline: '2026-10-21T23:01:00Z' } },
        { postAction: { queueId: '2', action: 'POST_ACTION_SLEEP', cancelled: true } },
        { queueUpdated: { name: 'without id' } },
      ],
    });
    expect(batch.map((e) => e.kind)).toEqual(['queue_updated', 'queue_removed', 'post_action', 'post_action']);
    expect(batch[1]).toEqual({ kind: 'queue_removed', id: 3 });
    expect(batch[2]).toEqual({
      kind: 'post_action',
      pending: { queueId: 2, action: 'sleep', deadline: Date.UTC(2026, 9, 21, 23, 1), cancelled: false },
    });
    expect(batch[3]).toMatchObject({ pending: { cancelled: true, deadline: 0 } });
  });

  it('stores secrets, checks for and installs updates, and picks a file', async () => {
    const { app, runtime } = fakeHost();
    const api = createWailsApi(app, runtime);
    await api.setSecret('site_login', 'a1', 'pa55');
    expect(app.SetSecret).toHaveBeenCalledWith({ kind: 'site_login', id: 'a1', password: 'pa55' });
    await api.clearSecret('proxy', '');
    expect(app.ClearSecret).toHaveBeenCalledWith({ kind: 'proxy', id: '' });
    const info = { currentVersion: '0.3.3', latestVersion: '0.4.0', available: true, notes: 'Queues', publishedAt: Date.UTC(2026, 9, 20, 8), checkedAt: Date.UTC(2026, 9, 21, 9, 30) };
    expect(await api.checkForUpdate()).toEqual(info);
    expect(await api.installUpdate()).toEqual(info);
    expect(parseUpdateInfo({})).toEqual({ currentVersion: '', latestVersion: '', available: false, notes: '', publishedAt: 0, checkedAt: 0 });
    app.CheckForUpdate.mockRejectedValueOnce('failed_precondition: updates are off');
    await expect(api.checkForUpdate()).rejects.toMatchObject({ code: 'failed_precondition' });
    expect(await api.pickFile({ title: 'Choose the scanner', initialDir: 'C:\\Tools' })).toBe('C:\\Tools\\scan.exe');
    expect(app.PickFile).toHaveBeenCalledWith({ title: 'Choose the scanner', initialDir: 'C:\\Tools' });
    app.PickFile.mockResolvedValueOnce('');
    expect(await api.pickFile()).toBeNull();
    expect(app.PickFile).toHaveBeenLastCalledWith({});
  });
});
describe('download again, theme and About (notes 2, 4, 8)', () => {
  it('sends existingFile with a restart only', async () => {
    const { app, runtime } = fakeHost();
    const api = createWailsApi(app, runtime);
    await api.resume(7, { restart: true, existingFile: 'replace' });
    expect(app.Resume).toHaveBeenLastCalledWith({ id: 7, restart: true, existingFile: 'EXISTING_FILE_REPLACE' });
    await api.resume(7, { restart: true, existingFile: 'keepBoth' });
    expect(app.Resume).toHaveBeenLastCalledWith({ id: 7, restart: true, existingFile: 'EXISTING_FILE_KEEP_BOTH' });
    await api.resume(7, { restart: true });
    expect(app.Resume).toHaveBeenLastCalledWith({ id: 7, restart: true });
    // Without a restart the choice means nothing: the daemon would refuse it, so it is not sent.
    await api.resume(7, { existingFile: 'replace' });
    expect(app.Resume).toHaveBeenLastCalledWith({ id: 7, restart: false });
  });

  it('asks whether the file of a download is still there', async () => {
    const { app, runtime } = fakeHost();
    const FileExists = vi.fn(async (): Promise<unknown> => ({ exists: true }));
    const api = createWailsApi({ ...app, FileExists }, runtime);
    expect(await api.fileExists(7)).toBe(true);
    expect(FileExists).toHaveBeenCalledWith({ id: 7 });
    FileExists.mockResolvedValueOnce({});
    expect(await api.fileExists(7)).toBe(false);
    await expect(createWailsApi(app, runtime).fileExists(7)).rejects.toMatchObject({ code: 'unimplemented' });
  });

  it('maps the theme choice to SetTheme', async () => {
    const { app, runtime } = fakeHost();
    const SetTheme = vi.fn(async (): Promise<unknown> => undefined);
    const api = createWailsApi({ ...app, SetTheme }, runtime);
    for (const theme of ['system', 'light', 'dark'] as const) {
      await api.setTheme(theme);
      expect(SetTheme).toHaveBeenLastCalledWith({ theme });
    }
    SetTheme.mockRejectedValueOnce('internal: cannot store the theme: access denied');
    await expect(api.setTheme('dark')).rejects.toMatchObject({ code: 'internal' });
    await expect(createWailsApi(app, runtime).setTheme('dark')).rejects.toMatchObject({ code: 'unimplemented' });
  });

  it('opens app folders by kind, never by path', async () => {
    const { app, runtime } = fakeHost();
    const OpenAppFolder = vi.fn(async (): Promise<unknown> => undefined);
    const api = createWailsApi({ ...app, OpenAppFolder }, runtime);
    for (const kind of ['data', 'logs', 'tools', 'downloads'] as const) {
      await api.openAppFolder(kind);
      expect(OpenAppFolder).toHaveBeenLastCalledWith({ kind });
    }
    OpenAppFolder.mockRejectedValueOnce('failed_precondition: the tools folder does not exist yet');
    await expect(api.openAppFolder('tools')).rejects.toMatchObject({ code: 'failed_precondition' });
  });

  it('reads the diagnostics with the WebView2 version and the notices', async () => {
    const { app, runtime } = fakeHost();
    const Diagnostics = vi.fn(async (): Promise<unknown> => ({ text: 'Downex window 0.8.2\n' }));
    const ThirdPartyNotices = vi.fn(async (): Promise<unknown> => ({ text: 'THIRD-PARTY NOTICES' }));
    const api = createWailsApi({ ...app, Diagnostics, ThirdPartyNotices }, runtime);
    expect(await api.diagnostics({ webview: '141.0.3537.71' })).toBe('Downex window 0.8.2\n');
    expect(Diagnostics).toHaveBeenLastCalledWith({ webview: '141.0.3537.71' });
    await api.diagnostics();
    expect(Diagnostics).toHaveBeenLastCalledWith({});
    expect(await api.thirdPartyNotices()).toBe('THIRD-PARTY NOTICES');
    await expect(createWailsApi(app, runtime).diagnostics()).rejects.toMatchObject({ code: 'unimplemented' });
    await expect(createWailsApi(app, runtime).thirdPartyNotices()).rejects.toMatchObject({ code: 'unimplemented' });
  });

  it('reads the folders and update support of the status', () => {
    expect(
      parseStatus({
        version: '0.8.2',
        endpoint: 'pipe',
        dataDir: 'C:\\Users\\you\\AppData\\Roaming\\Downex',
        logDir: 'C:\\Users\\you\\AppData\\Local\\Downex\\logs',
        toolsDir: 'C:\\Users\\you\\AppData\\Local\\Downex\\tools',
        updatesSupported: true,
      }),
    ).toMatchObject({
      dataDir: 'C:\\Users\\you\\AppData\\Roaming\\Downex',
      logDir: 'C:\\Users\\you\\AppData\\Local\\Downex\\logs',
      toolsDir: 'C:\\Users\\you\\AppData\\Local\\Downex\\tools',
      updatesSupported: true,
    });
    expect(parseStatus({ version: '0.8.1' })).toMatchObject({ dataDir: '', logDir: '', toolsDir: '', updatesSupported: false });
  });

  it('reads the build of the window from Startup', () => {
    const v = parseStartup({
      view: 'main',
      openWith: true,
      build: {
        version: '0.8.2',
        os: 'windows',
        arch: 'amd64',
        goVersion: 'go1.27.0',
        wailsVersion: 'v2.17.0',
        commit: '0123456789abcdef',
        commitTime: '2026-10-29T08:00:00Z',
        modified: true,
      },
    });
    expect(v.build).toEqual({
      version: '0.8.2',
      os: 'windows',
      arch: 'amd64',
      goVersion: 'go1.27.0',
      wailsVersion: 'v2.17.0',
      commit: '0123456789abcdef',
      commitTime: Date.UTC(2026, 9, 29, 8),
      modified: true,
    });
    // An older host sends none.
    expect(parseStartup({ view: 'main' }).build).toBeUndefined();
    expect(parseStartup({ view: 'main', build: { version: 'dev' } }).build).toEqual({
      version: 'dev',
      os: '',
      arch: '',
      goVersion: '',
      wailsVersion: '',
      commit: '',
      commitTime: 0,
      modified: false,
    });
  });

  it('reads the WebView2 version from the user agent', () => {
    expect(
      webviewVersion(
        'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36 Edg/141.0.3537.71',
      ),
    ).toBe('141.0.3537.71');
    expect(webviewVersion('Mozilla/5.0 (Macintosh) AppleWebKit/605.1.15 (KHTML, like Gecko)')).toBe('');
    expect(webviewVersion('Edg/<script>')).toBe('');
  });
});
