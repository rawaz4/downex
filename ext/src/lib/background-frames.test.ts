// Wiring of video detection on every site in the background (VIDEO-DETECT-SPEC §2.4, §2.6, §3.6,
// §5.3) against a fake browser: direct files and their redirects, documents that are files,
// frames and documents, the panel's offers and keys, the hand-offs of files, element files and
// streams, the overlay injection, the site toggle, the frame-media-seen messages and the badge.

import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';

const fake = vi.hoisted(() => {
  type Fn = (...args: unknown[]) => unknown;
  class FakeEvent {
    regs: { fn: Fn; filter?: unknown; extra?: unknown }[] = [];
    addListener(fn: Fn, filter?: unknown, extra?: unknown) {
      this.regs.push({ fn, filter, extra });
    }
    removeListener(fn: Fn) {
      this.regs = this.regs.filter((r) => r.fn !== fn);
    }
    hasListener(fn: Fn) {
      return this.regs.some((r) => r.fn === fn);
    }
    emit(...args: unknown[]): unknown[] {
      return this.regs.map((r) => r.fn(...args));
    }
  }
  const events = new Map<string, FakeEvent>();
  const calls: { path: string; args: unknown[] }[] = [];
  const writes: string[][] = [];
  const area = (track: boolean) => {
    const data: Record<string, unknown> = {};
    return {
      data,
      get: async (keys: string | string[] | null) => {
        if (keys === null) return structuredClone(data);
        const list = Array.isArray(keys) ? keys : [keys];
        return structuredClone(Object.fromEntries(list.filter((k) => k in data).map((k) => [k, data[k]])));
      },
      set: async (items: Record<string, unknown>) => {
        if (track) writes.push(Object.keys(items));
        Object.assign(data, structuredClone(items));
      },
      remove: async (keys: string | string[]) => {
        for (const k of Array.isArray(keys) ? keys : [keys]) delete data[k];
      },
    };
  };
  const session = area(true);
  const local = area(false);
  const posted: (Record<string, unknown> & { id: string; type: string })[] = [];
  const flow = {
    probeAnswer: undefined as Record<string, unknown> | undefined,
  };
  const videoInfo = {
    type: 'videoInfo',
    site: '',
    title: '',
    durationMs: 60_000,
    format: 'hls',
    container: 'mp4',
    selectedVideo: 'v1',
    selectedAudio: 'a1',
    tracks: [
      { id: 'v1', kind: 'video', ext: 'mp4', codecs: 'avc1.64001f', width: 1280, height: 720, frameRate: 30, bandwidth: 3000000, size: 0, sizeApprox: true, language: '', name: '720p (HD)' },
      { id: 'a1', kind: 'audio', ext: 'm4a', codecs: 'mp4a.40.2', width: 0, height: 0, frameRate: 0, bandwidth: 128000, size: 0, sizeApprox: true, language: 'en', name: 'English' },
    ],
  };
  const port = {
    onMessage: new FakeEvent(),
    onDisconnect: new FakeEvent(),
    postMessage(m: unknown) {
      const msg = JSON.parse(JSON.stringify(m)) as Record<string, unknown> & { id: string; type: string };
      posted.push(msg);
      const reply = (r: Record<string, unknown>) => queueMicrotask(() => port.onMessage.emit({ id: msg.id, ...r }));
      if (msg.type === 'hello') reply({ ok: true, type: 'welcome', protocol: 1, daemonVersion: '9.9.9', features: ['video', 'prefetch', 'streams'] });
      else if (msg.type === 'status') reply({ ok: true, type: 'status', running: true, active: 0, queued: 0 });
      else if (msg.type === 'add') reply({ ok: true, type: 'added', downloadId: 41, awaiting: true });
      else if (msg.type === 'addMedia') reply({ ok: true, type: 'added', downloadId: 42, awaiting: false });
      else if (msg.type === 'probeStream') reply(flow.probeAnswer ?? { ok: true, ...videoInfo });
      else if (msg.type === 'probeVideo') reply({ ok: true, ...videoInfo, site: 'news.example', format: 'http' });
    },
    disconnect() {},
  };
  const tabs: Record<number, Record<string, unknown>> = {};
  const cookieCalls: unknown[] = [];
  const overrides: Record<string, unknown> = {
    'runtime.id': 'ext-id',
    'runtime.lastError': undefined,
    'runtime.getManifest': () => ({ version: '0.1.0' }),
    'runtime.getURL': (p: string) => `chrome-extension://ext-id${p}`,
    'runtime.connectNative': () => port,
    'storage.session': session,
    'storage.local': local,
    'tabs.get': async (id: number) => {
      calls.push({ path: 'tabs.get', args: [id] });
      const t = tabs[id];
      if (!t) throw new Error(`No tab with id: ${id}.`);
      return { id, ...t };
    },
    'tabs.query': async () => [],
    'webNavigation.getAllFrames': async () => [],
    'webNavigation.getFrame': async () => ({ url: 'https://player.example/embed/9' }),
    'cookies.getAll': async (q: unknown) => {
      cookieCalls.push(q);
      return [{ name: 'JAR', value: 'never' }];
    },
  };
  const node = (path: string): unknown =>
    new Proxy(function () {}, {
      get(_t, prop) {
        if (typeof prop === 'symbol' || prop === 'then') return undefined;
        const p = path ? `${path}.${prop}` : prop;
        if (p in overrides) return overrides[p];
        if (/^on[A-Z]/.test(prop)) {
          let e = events.get(p);
          if (!e) {
            e = new FakeEvent();
            events.set(p, e);
          }
          return e;
        }
        return node(p);
      },
      apply(_t, _this, args: unknown[]) {
        calls.push({ path, args });
        return Promise.resolve(undefined);
      },
    });
  return { browser: node(''), events, calls, writes, session, local, posted, flow, tabs, cookieCalls };
});

vi.mock('wxt/browser', () => ({ browser: fake.browser }));

const event = (name: string) => {
  const e = fake.events.get(name);
  if (!e) throw new Error(`no listener for ${name}`);
  return e;
};

const POPUP = { id: 'ext-id', url: 'chrome-extension://ext-id/popup.html' };
const PAGE = 'https://news.example/story';
const EMBED = 'https://player.example/embed/9';
const MP4 = 'https://cdn.example/v/clip.mp4';

/** A message from a sender; resolves with the answer, or undefined when it was not taken. */
function send<T>(msg: unknown, sender: unknown): Promise<T | undefined> {
  return new Promise((resolve) => {
    let answered = false;
    const [kept] = event('runtime.onMessage').emit(msg, sender, (r: T) => {
      answered = true;
      resolve(r);
    });
    if (kept !== true && !answered) resolve(undefined);
  });
}
const ask = <T>(msg: unknown) => send<T>(msg, POPUP);

/** The content script of frame `frameId` of tab `tabId` (its tab shows PAGE). */
function frame(tabId: number, frameId = 0, url = PAGE, documentId = `DOC${tabId}-${frameId}`, tab: Record<string, unknown> = {}) {
  fake.tabs[tabId] ??= { url: PAGE, title: 'A story', active: true };
  return { id: 'ext-id', url, origin: new URL(url).origin, frameId, documentId, tab: { id: tabId, ...fake.tabs[tabId], ...tab } };
}

interface Load {
  url?: string;
  tabId: number;
  frameId?: number;
  parentFrameId?: number;
  documentId?: string;
  type?: string;
  statusCode?: number;
  contentType?: string;
  headers?: Record<string, string>;
  requestId?: string;
  requestHeaders?: [string, string][];
}

/** A request the page sent and its response, as webRequest reports them. */
function load(l: Load) {
  const url = l.url ?? MP4;
  const base = {
    url,
    method: 'GET',
    type: l.type ?? 'media',
    tabId: l.tabId,
    frameId: l.frameId ?? 0,
    parentFrameId: l.parentFrameId ?? (l.frameId ? 0 : -1),
    ...(l.documentId ? { documentId: l.documentId } : {}),
    requestId: l.requestId ?? `r-${Math.random()}`,
  };
  const requestHeaders = (l.requestHeaders ?? [
    ['Cookie', 'sid=1'],
    ['Referer', PAGE],
    ['Range', 'bytes=0-'],
  ]).map(([name, value]) => ({ name, value }));
  event('webRequest.onSendHeaders').emit({ ...base, requestHeaders });
  const headers = l.headers ?? { 'Content-Type': l.contentType ?? 'video/mp4', 'Content-Range': 'bytes 0-1023/50000000' };
  event('webRequest.onResponseStarted').emit({
    ...base,
    statusCode: l.statusCode ?? 206,
    responseHeaders: Object.entries(headers).map(([name, value]) => ({ name, value })),
  });
}

type Media = { items: Record<string, unknown>[]; files: Record<string, unknown>[]; hiddenHere: boolean; host: string };
const tabMedia = (tabId: number) => ask<Media>({ type: 'downex:tab-media', tabId });
const posts = (type: string) => fake.posted.filter((m) => m.type === type);
const settle = () => new Promise((r) => setTimeout(r, 20));
const badge = (tabId: number) =>
  fake.calls
    .filter((c) => c.path === 'action.setBadgeText')
    .map((c) => c.args[0] as { tabId?: number; text?: string | null })
    .filter((a) => a.tabId === tabId)
    .at(-1)?.text;

type Offers = {
  ok: true;
  value: { files: Record<string, unknown>[]; streams: Record<string, unknown>[]; site?: Record<string, unknown>; features: Record<string, boolean> };
};

describe('background video detection on every site', () => {
  beforeAll(async () => {
    const bg = (await import('../entrypoints/background')).default as { main: () => void };
    bg.main();
    await vi.waitFor(() => expect(posts('hello')).toHaveLength(1));
    await settle();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it('observes file responses and their redirects without blocking them', () => {
    const started = event('webRequest.onResponseStarted').regs[0];
    expect((started?.filter as { types: string[] }).types).toEqual(['xmlhttprequest', 'media', 'other', 'main_frame', 'sub_frame', 'object']);
    for (const name of ['webRequest.onBeforeRedirect', 'webRequest.onCompleted', 'webRequest.onErrorOccurred']) {
      const reg = event(name).regs[0];
      expect(reg?.filter).toEqual({ urls: ['http://*/*', 'https://*/*'], types: ['media', 'main_frame', 'sub_frame', 'object'] });
      expect(reg?.extra).toBeUndefined();
    }
  });

  it('lists a file a <video> played with its size, writing storage once for all its range requests', async () => {
    fake.tabs[1] = { url: PAGE, title: 'A story' };
    const writes = () => fake.writes.filter((keys) => keys.includes('media:1')).length;
    const lookups = () => fake.calls.filter((c) => c.path === 'tabs.get' && c.args[0] === 1).length;
    for (let i = 0; i < 12; i++) load({ tabId: 1 });
    await settle();
    // One tab lookup and two writes: the new file, then the page it was seen on.
    expect(lookups()).toBe(1);
    expect(writes()).toBe(2);
    const { files } = (await tabMedia(1))!;
    expect(files[0]).toMatchObject({ url: MP4, size: 50_000_000, sizeKnown: true, frame: 'top', filename: 'clip.mp4', short: false });
    expect(files[0]?.key).toMatch(/^f[0-9]+$/);
    expect(JSON.stringify(files)).not.toContain('sid=1');
    await vi.waitFor(() => expect(badge(1)).toBe('1'));
  });

  it('never lists script requests, other containers, prerendered or fenced frames', async () => {
    load({ tabId: 2, type: 'xmlhttprequest' });
    load({ tabId: 2, type: 'other', url: 'https://cdn.example/a.mp4' });
    load({ tabId: 2, contentType: 'video/webm', url: 'https://cdn.example/a.webm' });
    load({ tabId: 2, url: 'https://cdn.example/b.mp4', headers: { 'Content-Type': 'video/mp4' }, statusCode: 404 });
    event('webRequest.onResponseStarted').emit({
      url: 'https://cdn.example/c.mp4',
      method: 'GET',
      type: 'media',
      tabId: 2,
      frameId: 0,
      statusCode: 200,
      documentLifecycle: 'prerender',
      responseHeaders: [{ name: 'Content-Type', value: 'video/mp4' }],
    });
    event('webRequest.onResponseStarted').emit({
      url: 'https://ads.example/d.mp4',
      method: 'GET',
      type: 'media',
      tabId: 2,
      frameId: 9,
      frameType: 'fenced_frame',
      statusCode: 200,
      responseHeaders: [{ name: 'Content-Type', value: 'video/mp4' }],
    });
    await settle();
    expect((await tabMedia(2))?.files).toEqual([]);
  });

  it('hands a file off with the first URL of its redirect chain and the headers sent for it, never other cookies', async () => {
    fake.tabs[3] = { url: PAGE, title: 'A story' };
    const first = 'https://news.example/v/clip';
    const final = 'https://cdn2.example/cdn/clip.mp4?sig=abc';
    event('webRequest.onSendHeaders').emit({
      url: first,
      method: 'GET',
      type: 'media',
      tabId: 3,
      frameId: 0,
      requestHeaders: [
        { name: 'Cookie', value: 'session=first' },
        { name: 'Referer', value: PAGE },
        { name: 'Range', value: 'bytes=0-' },
      ],
    });
    event('webRequest.onBeforeRedirect').emit({ url: first, redirectUrl: final, tabId: 3, requestId: 'redir-1', type: 'media' });
    load({ tabId: 3, url: final, requestId: 'redir-1', requestHeaders: [['Range', 'bytes=0-']] });
    event('webRequest.onCompleted').emit({ requestId: 'redir-1' });
    await vi.waitFor(async () => expect((await tabMedia(3))?.files).toHaveLength(1));
    const cookies = fake.cookieCalls.length;
    const r = await ask({ type: 'downex:add-file', tabId: 3, url: final });
    expect(r).toEqual({ ok: true, value: { type: 'added', downloadId: 41, awaiting: true } });
    expect(posts('add').at(-1)).toMatchObject({
      url: first,
      method: 'GET',
      headers: [
        ['Cookie', 'session=first'],
        ['Referer', PAGE],
        ['User-Agent', navigator.userAgent],
      ],
      filename: 'clip.mp4',
      mime: 'video/mp4',
      size: 50_000_000,
      deadlineMs: expect.any(Number),
      referrer: PAGE,
    });
    expect(fake.cookieCalls).toHaveLength(cookies);
    await vi.waitFor(() =>
      expect(fake.session.data.handoffHistory).toEqual(
        expect.arrayContaining([expect.objectContaining({ source: 'video-file', ok: true, url: first, awaiting: true })]),
      ),
    );
    // The popup sees the add; a URL that is not listed is never fetched.
    expect((await tabMedia(3))?.files[0]).toMatchObject({ add: { result: { ok: true } } });
    await expect(ask({ type: 'downex:add-file', tabId: 3, url: 'https://evil.example/x.mp4' })).resolves.toMatchObject({
      ok: false,
      error: { code: 'expired' },
    });
  });

  it('holds a document that is the file until its frame commits it', async () => {
    const doc = 'https://cdn.example/direct/movie.mp4';
    load({ tabId: 4, type: 'main_frame', url: doc, statusCode: 200, headers: { 'Content-Type': 'video/mp4', 'Content-Length': '9000000' } });
    await settle();
    expect((await tabMedia(4))?.files).toEqual([]);
    event('webNavigation.onCommitted').emit({ tabId: 4, frameId: 0, url: doc, documentId: 'DOC-movie', documentLifecycle: 'active' });
    await vi.waitFor(async () => expect((await tabMedia(4))?.files).toEqual([expect.objectContaining({ url: doc, size: 9_000_000 })]));
    // A response that never commits (a download, a blocked navigation) is dropped.
    load({ tabId: 4, type: 'main_frame', url: 'https://cdn.example/direct/other.mp4', statusCode: 200, headers: { 'Content-Type': 'video/mp4' } });
    event('webNavigation.onCommitted').emit({ tabId: 4, frameId: 0, url: 'https://news.example/', documentLifecycle: 'active' });
    // An attachment is a browser download, which capture decides.
    load({
      tabId: 4,
      type: 'main_frame',
      url: 'https://cdn.example/direct/att.mp4',
      statusCode: 200,
      headers: { 'Content-Type': 'video/mp4', 'Content-Disposition': 'attachment; filename=att.mp4' },
    });
    event('webNavigation.onCommitted').emit({ tabId: 4, frameId: 0, url: 'https://cdn.example/direct/att.mp4', documentLifecycle: 'active' });
    await settle();
    expect((await tabMedia(4))?.files).toEqual([]);
  });

  it('clears a frame that navigated and the frames below it, keeping the top frame', async () => {
    load({ tabId: 5, url: 'https://cdn.example/top.mp4' });
    load({ tabId: 5, frameId: 3, parentFrameId: 0, url: 'https://player.example/3.m3u8', type: 'xmlhttprequest', contentType: 'application/vnd.apple.mpegurl' });
    load({ tabId: 5, frameId: 7, parentFrameId: 3, url: 'https://player.example/7.mp4' });
    await vi.waitFor(async () => {
      const m = await tabMedia(5);
      expect([m?.items.length, m?.files.length]).toEqual([1, 2]);
    });
    event('webNavigation.onCommitted').emit({ tabId: 5, frameId: 3, parentFrameId: 0, url: 'https://player.example/next', documentLifecycle: 'active' });
    await vi.waitFor(async () => {
      const m = await tabMedia(5);
      expect(m?.items).toEqual([]);
      expect(m?.files.map((f) => f.url)).toEqual(['https://cdn.example/top.mp4']);
    });
    // A same-document navigation keeps them.
    event('webNavigation.onHistoryStateUpdated').emit({ tabId: 5, frameId: 0, url: `${PAGE}#part2` });
    expect((await tabMedia(5))?.files).toHaveLength(1);
  });

  it('answers a frame’s panel from memory, with keys bound to the frame', async () => {
    fake.tabs[6] = { url: PAGE, title: 'A story' };
    load({ tabId: 6, url: 'https://cdn.example/top-video.mp4' });
    load({ tabId: 6, frameId: 4, documentId: 'DOC6-4', url: 'https://player.example/media/embed.mp4' });
    load({ tabId: 6, frameId: 4, documentId: 'DOC6-4', type: 'xmlhttprequest', url: 'https://player.example/hls/master.m3u8', contentType: 'application/x-mpegurl' });
    await vi.waitFor(async () => expect((await tabMedia(6))?.files).toHaveLength(2));
    const relayCalls = fake.posted.length;
    const sub = frame(6, 4, EMBED, 'DOC6-4');
    const fromSub = (await send<Offers>({ type: 'downex:frame-offers', src: '', mse: true }, sub))!;
    expect(fromSub.ok).toBe(true);
    expect(fromSub.value.files.map((f) => [f.display, f.frame])).toEqual([['player.example/embed.mp4', 'this']]);
    expect(fromSub.value.streams).toEqual([expect.objectContaining({ format: 'hls', frame: 'this', matched: true, display: 'player.example/master.m3u8' })]);
    expect(fromSub.value.site).toEqual({ key: `page:${EMBED}`, site: 'player.example', kind: 'page', pageUrl: EMBED, curated: false });
    expect(fromSub.value.features).toEqual({ streams: true, video: true });
    expect(JSON.stringify([fromSub.value.files, fromSub.value.streams])).not.toContain('https://');
    // The embedded frame learns neither the tab's title nor its host.
    expect(fromSub.value).toMatchObject({ title: '', topHost: '' });
    const top = (await send<Offers>({ type: 'downex:frame-offers', src: 'https://cdn.example/top-video.mp4', mse: false }, frame(6)))!;
    // The top frame's panel lists its own items only (the popup lists every frame's).
    expect(top.value.files.map((f) => [f.display, f.frame, f.matched])).toEqual([['cdn.example/top-video.mp4', 'this', true]]);
    expect(top.value.streams).toEqual([]);
    expect(top.value).toMatchObject({ title: 'A story', topHost: 'news.example' });
    // No relay call to list them.
    expect(fake.posted).toHaveLength(relayCalls);
    // A sub-frame cannot use the top frame's keys; the top frame may use every key of its tab.
    const topKey = top.value.files[0]?.key;
    await expect(send({ type: 'downex:offer-add', key: topKey }, sub)).resolves.toMatchObject({ ok: false, error: { code: 'expired' } });
    // Nor an old document of its frame.
    await expect(send({ type: 'downex:offer-add', key: fromSub.value.files[0]?.key }, frame(6, 4, EMBED, 'DOC-OLD'))).resolves.toMatchObject({
      ok: false,
      error: { code: 'expired' },
    });
    expect(posts('add').filter((a) => String(a.url).includes('top-video'))).toHaveLength(0);
    await expect(send({ type: 'downex:offer-add', key: fromSub.value.files[0]?.key }, frame(6))).resolves.toMatchObject({ ok: true });
    expect(posts('add').at(-1)).toMatchObject({ url: 'https://player.example/media/embed.mp4' });
    // Extension pages and private tabs cannot ask.
    await expect(ask({ type: 'downex:frame-offers', src: '', mse: false })).resolves.toBeUndefined();
    await expect(send({ type: 'downex:frame-offers', src: '', mse: false }, frame(6, 0, PAGE, 'D', { incognito: true }))).resolves.toMatchObject({
      ok: false,
      error: { code: 'unsupported_page' },
    });
  });

  it('offers the clicked element’s MP4 address and adds it without headers or cookies', async () => {
    fake.tabs[7] = { url: PAGE, title: 'Holiday clips' };
    const src = 'https://static.example/cached/holiday.mp4';
    const sub = frame(7, 2, EMBED, 'DOC7-2');
    const videos = [
      { id: 5, src, mse: false, width: 640, height: 360, videoWidth: 1280, videoHeight: 720, durationMs: 95_000, drm: false, visible: true, decorative: false, meaningful: true },
    ];
    await send({ type: 'downex:frame-videos', videos }, sub);
    const offers = (await send<Offers>({ type: 'downex:frame-offers', src, mse: false }, sub))!;
    expect(offers.value.files).toEqual([
      expect.objectContaining({ key: 'e5', display: 'static.example/holiday.mp4', sizeKnown: false, matched: true, height: 720, durationMs: 95_000 }),
    ]);
    const cookies = fake.cookieCalls.length;
    await expect(send({ type: 'downex:offer-add', key: 'e5' }, sub)).resolves.toMatchObject({ ok: true });
    expect(posts('add').at(-1)).toMatchObject({
      url: src,
      headers: [
        ['Referer', 'https://player.example/'],
        ['User-Agent', navigator.userAgent],
      ],
      filename: 'holiday.mp4',
      size: -1,
    });
    expect(fake.cookieCalls).toHaveLength(cookies);
    // Another frame's element key is not this frame's.
    await expect(send({ type: 'downex:offer-add', key: 'e5' }, frame(7, 3, EMBED, 'DOC7-3'))).resolves.toMatchObject({
      ok: false,
      error: { code: 'expired' },
    });
  });

  it('never offers an element’s .mp4 address whose response was seen and was not an MP4', async () => {
    fake.tabs[17] = { url: PAGE, title: 'Clips' };
    const src = 'https://static.example/clips/really-webm.mp4';
    const sub = frame(17, 2, EMBED, 'DOC17-2');
    load({ tabId: 17, frameId: 2, documentId: 'DOC17-2', url: src, contentType: 'video/webm' });
    const videos = [
      { id: 6, src, mse: false, width: 640, height: 360, videoWidth: 1280, videoHeight: 720, durationMs: 95_000, drm: false, visible: true, decorative: false, meaningful: true },
    ];
    await send({ type: 'downex:frame-videos', videos }, sub);
    const offers = (await send<Offers>({ type: 'downex:frame-offers', src, mse: false }, sub))!;
    expect(offers.value.files).toEqual([]);
    await expect(send({ type: 'downex:offer-add', key: 'e6' }, sub)).resolves.toMatchObject({ ok: false, error: { code: 'expired' } });
  });

  it('probes a stream and adds it with the chosen tracks; a refused stream leaves the panel and the badge', async () => {
    fake.tabs[8] = { url: PAGE, title: 'Live news' };
    load({ tabId: 8, type: 'xmlhttprequest', url: 'https://cdn.example/news/master.m3u8', contentType: 'application/vnd.apple.mpegurl' });
    load({ tabId: 8, type: 'xmlhttprequest', url: 'https://cdn.example/drm/master.mpd', contentType: 'application/dash+xml' });
    await vi.waitFor(async () => expect((await tabMedia(8))?.items).toHaveLength(2));
    const top = frame(8);
    const offers = (await send<Offers>({ type: 'downex:frame-offers', src: '', mse: true }, top))!;
    const [drm, hls] = offers.value.streams;
    expect([drm?.format, hls?.format]).toEqual(['dash', 'hls']);
    const probe = await send({ type: 'downex:stream-probe', key: hls?.key }, top);
    expect(probe).toMatchObject({ ok: true, value: { type: 'videoInfo', container: 'mp4' } });
    expect(posts('probeStream').at(-1)).toMatchObject({
      manifestUrl: 'https://cdn.example/news/master.m3u8',
      headers: expect.arrayContaining([['Cookie', 'sid=1']]),
      pageUrl: PAGE,
      deadlineMs: expect.any(Number),
    });
    await send({ type: 'downex:stream-probe', key: hls?.key }, top);
    expect(posts('probeStream')).toHaveLength(1);
    // A file key is not a stream.
    await expect(send({ type: 'downex:stream-probe', key: 's999999' }, top)).resolves.toMatchObject({ ok: false, error: { code: 'expired' } });
    const added = await send({ type: 'downex:offer-add', key: hls?.key, videoTrack: 'v1', audioTrack: 'a1' }, top);
    expect(added).toEqual({ ok: true, value: { type: 'added', downloadId: 42, awaiting: false } });
    expect(posts('addMedia').at(-1)).toMatchObject({ manifestUrl: 'https://cdn.example/news/master.m3u8', videoTrack: 'v1', audioTrack: 'a1' });
    // The popup's own path carries the tracks too, and joins nothing that is not running.
    await ask({ type: 'downex:add-media', tabId: 8, url: 'https://cdn.example/news/master.m3u8', videoTrack: 'v1' });
    expect(posts('addMedia').at(-1)).toMatchObject({ videoTrack: 'v1' });
    expect(posts('addMedia').at(-1)).not.toHaveProperty('audioTrack');
    await vi.waitFor(() => expect(badge(8)).toBe('1'));
    // Protected: refused and kept, so it disappears.
    fake.flow.probeAnswer = { ok: false, error: { code: 'rejected', message: 'encrypted: DRM', reason: 'encrypted' } };
    await expect(ask({ type: 'downex:tab-stream-probe', tabId: 8, url: 'https://cdn.example/drm/master.mpd' })).resolves.toMatchObject({
      ok: false,
      error: { reason: 'encrypted' },
    });
    fake.flow.probeAnswer = undefined;
    const after = (await send<Offers>({ type: 'downex:frame-offers', src: '', mse: true }, top))!;
    expect(after.value.streams.map((s) => s.key)).toEqual([hls?.key]);
    const popup = (await tabMedia(8))!;
    expect(popup.items.find((i) => i.url === 'https://cdn.example/drm/master.mpd')).toMatchObject({
      probe: { result: { ok: false, error: { reason: 'encrypted' } } },
    });
    // The other stream keeps the frame counted.
    expect(badge(8)).toBe('1');
  });

  it('injects the overlay UI once per document, and never on a hidden site', async () => {
    fake.tabs[9] = { url: 'https://videos.example/watch', title: 'Videos' };
    const sub = frame(9, 4, EMBED, 'DOC9-4');
    const injections = () => fake.calls.filter((c) => c.path === 'scripting.executeScript');
    const before = injections().length;
    await expect(send({ type: 'downex:video-ui' }, sub)).resolves.toEqual({ ok: true });
    await expect(send({ type: 'downex:video-ui' }, sub)).resolves.toEqual({ ok: true });
    expect(injections().slice(before).map((c) => c.args[0])).toEqual([{ target: { tabId: 9, documentIds: ['DOC9-4'] }, files: ['/video-ui.js'] }]);
    // Another document of the frame gets its own.
    event('webNavigation.onCommitted').emit({ tabId: 9, frameId: 4, parentFrameId: 0, url: EMBED, documentLifecycle: 'active' });
    await send({ type: 'downex:video-ui' }, frame(9, 4, EMBED, 'DOC9-4b'));
    expect(injections().slice(before)).toHaveLength(2);
    // Firefox names no document: the frame is the target.
    await send({ type: 'downex:video-ui' }, { ...frame(9, 6, EMBED), documentId: undefined });
    expect(injections().at(-1)?.args[0]).toEqual({ target: { tabId: 9, frameIds: [6] }, files: ['/video-ui.js'] });
    // Private tabs and senders without a tab are refused.
    await expect(send({ type: 'downex:video-ui' }, frame(9, 7, EMBED, 'X', { incognito: true }))).resolves.toEqual({ ok: false });
    await expect(send({ type: 'downex:video-ui' }, { id: 'ext-id', url: EMBED, frameId: 0 })).resolves.toEqual({ ok: false });
    await expect(ask({ type: 'downex:video-ui' })).resolves.toBeUndefined();
  });

  it('hides and shows the buttons per site, from a panel or from the popup', async () => {
    fake.tabs[10] = { url: 'https://videos.example/watch', title: 'Videos' };
    const sub = frame(10, 2, EMBED, 'DOC10-2');
    await expect(send({ type: 'downex:overlay-state' }, sub)).resolves.toEqual({ button: true, host: 'videos.example' });
    await expect(send({ type: 'downex:set-site-hidden', hidden: true }, sub)).resolves.toEqual({ hidden: true });
    expect(fake.local.data.videoOptions).toMatchObject({ hiddenSites: ['videos.example'] });
    await expect(send({ type: 'downex:overlay-state' }, sub)).resolves.toEqual({ button: false, host: 'videos.example' });
    await expect(send({ type: 'downex:video-ui' }, frame(10, 3, EMBED, 'DOC10-3'))).resolves.toEqual({ ok: false });
    expect((await tabMedia(10))?.hiddenHere).toBe(true);
    // A content script never names another tab.
    await expect(send({ type: 'downex:set-site-hidden', hidden: false, tabId: 10 }, sub)).resolves.toBeUndefined();
    await expect(ask({ type: 'downex:set-site-hidden', tabId: 10, hidden: false })).resolves.toEqual({ hidden: false });
    expect(fake.local.data.videoOptions).toMatchObject({ hiddenSites: [] });
    await expect(send({ type: 'downex:overlay-state' }, sub)).resolves.toEqual({ button: true, host: 'videos.example' });
    // A wildcard entry keeps it hidden whatever the toggle does.
    await fake.local.set({ videoOptions: { videoButton: true, hiddenSites: ['*.example'], keep: 'me' } });
    await expect(ask({ type: 'downex:set-site-hidden', tabId: 10, hidden: false })).resolves.toEqual({ hidden: true });
    await fake.local.set({ videoOptions: { videoButton: true, hiddenSites: [] } });
    await expect(ask({ type: 'downex:set-site-hidden', tabId: 404, hidden: true })).resolves.toEqual({ hidden: false });
  });

  it('tells a frame that reported no video about its media traffic, at most once per 5 s', async () => {
    vi.useFakeTimers({ toFake: ['Date'] });
    vi.setSystemTime(1_000_000);
    const told = () =>
      fake.calls.filter((c) => c.path === 'tabs.sendMessage' && c.args[0] === 11).map((c) => [c.args[1], c.args[2]]);
    load({ tabId: 11, frameId: 6, type: 'xmlhttprequest', url: 'https://cdn.example/seg/1.m4s', headers: { 'Content-Type': 'video/mp4' } });
    load({ tabId: 11, frameId: 6, type: 'xmlhttprequest', url: 'https://cdn.example/seg/2.m4s', headers: { 'Content-Type': 'video/mp4' } });
    expect(told()).toEqual([[{ type: 'downex:frame-media-seen' }, { frameId: 6 }]]);
    vi.setSystemTime(1_005_000);
    load({ tabId: 11, frameId: 6, type: 'xmlhttprequest', url: 'https://cdn.example/seg/3.m4s', headers: { 'Content-Type': 'video/mp4' } });
    expect(told()).toHaveLength(2);
    // A frame that reported a video found it already; other answers are not media traffic.
    await send({ type: 'downex:frame-videos', videos: [{ id: 1, src: '', mse: true, width: 1, height: 1, videoWidth: 0, videoHeight: 0, durationMs: -1, drm: false, visible: true, decorative: false, meaningful: false }] }, frame(11, 6, EMBED));
    vi.setSystemTime(1_020_000);
    load({ tabId: 11, frameId: 6, type: 'xmlhttprequest', url: 'https://cdn.example/seg/4.m4s', headers: { 'Content-Type': 'video/mp4' } });
    load({ tabId: 11, frameId: 8, type: 'xmlhttprequest', url: 'https://cdn.example/api.json', headers: { 'Content-Type': 'application/json' } });
    expect(told()).toHaveLength(2);
    // YouTube's frames have no generic tracker to tell.
    event('webRequest.onResponseStarted').emit({
      url: 'https://rr1.googlevideo.com/videoplayback?id=1',
      method: 'GET',
      type: 'xmlhttprequest',
      tabId: 11,
      frameId: 9,
      statusCode: 200,
      initiator: 'https://www.youtube.com',
      responseHeaders: [{ name: 'Content-Type', value: 'video/mp4' }],
    });
    expect(told()).toHaveLength(2);
  });

  it('badges full files and streams, never short files, segments or protected frames', async () => {
    fake.tabs[12] = { url: PAGE, title: 'A story' };
    load({ tabId: 12, url: 'https://cdn.example/feature.mp4' });
    await vi.waitFor(() => expect(badge(12)).toBe('1'));
    load({ tabId: 12, url: 'https://cdn.example/teaser.mp4', headers: { 'Content-Type': 'video/mp4', 'Content-Range': 'bytes 0-1/900000' } });
    for (let i = 0; i < 4; i++) {
      load({ tabId: 12, frameId: 3, url: `https://cdn.example/hls/seg${i}.mp4`, headers: { 'Content-Type': 'video/mp4', 'Content-Length': '500000' }, statusCode: 200 });
    }
    load({ tabId: 12, frameId: 5, type: 'xmlhttprequest', url: 'https://drm.example/a.mpd', contentType: 'application/dash+xml' });
    await vi.waitFor(async () => expect((await tabMedia(12))?.items).toHaveLength(1));
    await vi.waitFor(() => expect(badge(12)).toBe('2'));
    // The popup lists the short clip, never the segments.
    const files = (await tabMedia(12))!.files;
    expect(files.map((f) => [f.url, f.short])).toEqual([
      ['https://cdn.example/teaser.mp4', true],
      ['https://cdn.example/feature.mp4', false],
    ]);
    // Frame 5's videos are all protected: its stream is not counted.
    await send(
      { type: 'downex:frame-videos', videos: [{ id: 1, src: '', mse: true, width: 640, height: 360, videoWidth: 0, videoHeight: 0, durationMs: 60_000, drm: true, visible: true, decorative: false, meaningful: false }] },
      frame(12, 5, 'https://drm.example/player'),
    );
    expect(badge(12)).toBe('1');
  });
});
