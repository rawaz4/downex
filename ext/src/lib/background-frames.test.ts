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
    /** How a frame's content script answers tabs.sendMessage (undefined: nobody answers). */
    frameMessage: undefined as ((tabId: number, msg: unknown, opts: { frameId: number }) => Promise<unknown>) | undefined,
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
      if (msg.type === 'hello') reply({ ok: true, type: 'welcome', protocol: 1, daemonVersion: '9.9.9', features: ['video', 'prefetch', 'streams', 'browserFetch', 'played'] });
      else if (msg.type === 'status') reply({ ok: true, type: 'status', running: true, active: 0, queued: 0 });
      else if (msg.type === 'add') reply({ ok: true, type: 'added', downloadId: 41, awaiting: true });
      else if (msg.type === 'addMedia') reply({ ok: true, type: 'added', downloadId: 42, awaiting: false });
      else if (msg.type === 'probeStream') reply(flow.probeAnswer ?? { ok: true, ...videoInfo });
      else if (msg.type === 'probeVideo') reply({ ok: true, ...videoInfo, site: 'news.example', format: 'http' });
      else if (msg.type === 'probePlayed') reply({ ok: true, ...videoInfo, site: 'social.example', format: 'http', selectedVideo: 'r1', selectedAudio: '', tracks: [{ ...videoInfo.tracks[0], id: 'r1', kind: 'muxed' }] });
      else if (msg.type === 'addPlayed') reply({ ok: true, type: 'added', downloadId: 43, awaiting: false, fetchTicket: '0123456789abcdef0123456789abcdef' });
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
    'tabs.sendMessage': async (tabId: number, msg: unknown, opts: { frameId: number }) => {
      calls.push({ path: 'tabs.sendMessage', args: [tabId, msg, opts] });
      return flow.frameMessage?.(tabId, msg, opts);
    },
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
  /** The server's address the browser reports (absent: from its cache). */
  ip?: string;
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
    ...(l.ip ? { ip: l.ip } : {}),
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
  value: {
    files: Record<string, unknown>[];
    streams: Record<string, unknown>[];
    site?: Record<string, unknown>;
    features: Record<string, boolean>;
    networkOnly?: true; feed?: string;
  };
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

  it('hands a file off with the first URL of its redirect chain and the headers sent for it, the cookie only with the sign-in option', async () => {
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
    // The sign-in option is off (the default): the page's cookie never goes to Downex (review 2026-11-18).
    expect(posts('add').at(-1)).toMatchObject({
      url: first,
      method: 'GET',
      headers: [
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
    // With the option on, the cookie the browser sent goes with it (and no other).
    await fake.local.set({ videoOptions: { useSignInForVideos: true } });
    await ask({ type: 'downex:add-file', tabId: 3, url: final });
    expect(posts('add').at(-1)).toMatchObject({ headers: [['Cookie', 'session=first'], ['Referer', PAGE], ['User-Agent', navigator.userAgent]] });
    expect(fake.cookieCalls).toHaveLength(cookies);
    await fake.local.set({ videoOptions: {} });
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
    expect(fromSub.value.features).toEqual({ streams: true, video: true, clips: false });
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

  it('offers only the browser’s own traffic on a network site, with its own empty text (VIDEO-DETECT-SPEC §3.8)', async () => {
    const threads = 'https://www.threads.com/@someone/post/AbC12dEfGh';
    fake.tabs[16] = { url: threads, title: 'A post', active: true };
    const relayCalls = fake.posted.length;
    const offers = (await send<Offers>({ type: 'downex:frame-offers', src: '', mse: true }, frame(16, 0, threads)))!;
    expect(offers.ok).toBe(true);
    expect(offers.value.site).toBeUndefined();
    expect(offers.value.networkOnly).toBe(true);
    // Its overlay cannot ask yt-dlp about it either.
    await expect(send({ type: 'downex:page-video-probe', url: threads }, frame(16, 0, threads))).resolves.toMatchObject({
      ok: false,
      error: { code: 'unsupported_page' },
    });
    expect(fake.posted).toHaveLength(relayCalls);
    // Any other page has no such mark.
    const other = (await send<Offers>({ type: 'downex:frame-offers', src: '', mse: true }, frame(6)))!;
    expect(other.value).not.toHaveProperty('networkOnly');
  });

  it('offers a feed video’s own post as its Site source, checked again, and says so when none was found (§5.1 "Feed videos")', async () => {
    const feed = 'https://www.instagram.com/';
    const post = 'https://www.instagram.com/p/CODEaaa111/';
    fake.tabs[17] = { url: feed, title: 'Instagram', active: true };
    const relayCalls = fake.posted.length;
    const linked = (await send<Offers>({ type: 'downex:frame-offers', src: '', mse: true, post }, frame(17, 0, feed)))!;
    expect(linked.value.site).toEqual({ key: 'instagram:CODEaaa111', site: 'Instagram', kind: 'watch', pageUrl: post, curated: true, linked: true });
    expect(linked.value).not.toHaveProperty('feed');
    // Without a post, or with one the background does not take (another origin), no Site source.
    for (const p of [undefined, 'https://evil.example/p/CODEaaa111/']) {
      const msg = p ? { type: 'downex:frame-offers', src: '', mse: true, post: p } : { type: 'downex:frame-offers', src: '', mse: true };
      const none = (await send<Offers>(msg, frame(17, 0, feed)))!;
      expect(none.value.site).toBeUndefined();
      // No text sends the user elsewhere (SITE-MEDIA-SPEC §2.9): without a trace, no Played source either.
      expect(none.value).not.toHaveProperty('feed');
      expect(none.value).not.toHaveProperty('played');
    }
    // A malformed post is refused as a message.
    await expect(send({ type: 'downex:frame-offers', src: '', mse: true, post: 'javascript:alert(1)' }, frame(17, 0, feed))).resolves.toBeUndefined();
    // Nothing reached Downex for any of it: the panel asks when it probes.
    expect(fake.posted).toHaveLength(relayCalls);
    // A network site's feed is network-only, not a feed.
    const threads = 'https://www.threads.com/';
    fake.tabs[18] = { url: threads, title: 'Threads', active: true };
    const t = (await send<Offers>({ type: 'downex:frame-offers', src: '', mse: true }, frame(18, 0, threads)))!;
    expect(t.value).toMatchObject({ networkOnly: true });
    expect(t.value).not.toHaveProperty('feed');
    expect(t.value.site).toBeUndefined();
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
      headers: expect.arrayContaining([['Referer', PAGE]]),
      pageUrl: PAGE,
      deadlineMs: expect.any(Number),
    });
    // The sign-in option is off: the page's cookie never goes with the stream.
    expect(JSON.stringify(posts('probeStream').at(-1))).not.toContain('Cookie');
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

  it('lets "Show Downex download panel" put the overlay UI where buttons are off or the site is hidden, and only then', async () => {
    const injections = () => fake.calls.filter((c) => c.path === 'scripting.executeScript');
    const failures = async () =>
      (await ask<{ history: { url: string; ok: boolean; error?: { message: string } }[] }>({ type: 'downex:get-state' }))!.history.filter((h) => !h.ok);
    const onClicked = event('contextMenus.onClicked');
    const cases: [number, Record<string, unknown>][] = [
      [31, { videoButton: false, hiddenSites: [] }],
      [32, { videoButton: true, hiddenSites: ['videos.example'] }],
    ];
    for (const [tabId, videoOptions] of cases) {
      fake.tabs[tabId] = { url: 'https://videos.example/watch', title: 'Videos' };
      await fake.local.set({ videoOptions });
      // Unasked (a page cannot ask for it either), the frame gets no UI.
      await expect(send({ type: 'downex:video-ui' }, frame(tabId, 3, EMBED, `DOC${tabId}-3`))).resolves.toEqual({ ok: false });
      // The tracker asks for the UI while it answers the menu's request, as video.content.ts does.
      fake.flow.frameMessage = async (id, msg, opts) => {
        expect(msg).toEqual({ type: 'downex:show-panel' });
        const ui = await send<{ ok: boolean }>({ type: 'downex:video-ui' }, frame(id, opts.frameId, EMBED, `DOC${id}-${opts.frameId}`));
        return { shown: ui?.ok === true };
      };
      const before = injections().length;
      const failed = (await failures()).length;
      onClicked.emit(
        { menuItemId: 'downex-show-panel', frameId: 3, frameUrl: EMBED, pageUrl: 'https://videos.example/watch' },
        { id: tabId, url: 'https://videos.example/watch' },
      );
      await vi.waitFor(() => expect(injections()).toHaveLength(before + 1));
      expect(injections().at(-1)?.args[0]).toEqual({ target: { tabId, documentIds: [`DOC${tabId}-3`] }, files: ['/video-ui.js'] });
      await new Promise((r) => setTimeout(r, 0));
      expect(await failures()).toHaveLength(failed);
      // The request ended with the frame's answer: another frame asking is refused again.
      fake.flow.frameMessage = undefined;
      await expect(send({ type: 'downex:video-ui' }, frame(tabId, 5, EMBED, `DOC${tabId}-5`))).resolves.toEqual({ ok: false });
    }
    await fake.local.set({ videoOptions: { videoButton: true, hiddenSites: [] } });
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

  it('tells the YouTube overlay whether Downex takes a part of a video, and no extension page', async () => {
    fake.tabs[12] = { url: 'https://www.youtube.com/watch?v=aqz-KE-bpKQ', title: 'YouTube' };
    // This relay's welcome lists no `clips`: the panel shows no Part row.
    await expect(send({ type: 'downex:video-features' }, frame(12))).resolves.toEqual({ clips: false });
    await expect(ask({ type: 'downex:video-features' })).resolves.toBeUndefined();
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

describe('save what is playing (SITE-MEDIA-SPEC §2.9)', () => {
  // The background started in the describe above (main runs once per module).
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  const FEED = 'https://www.social.example/feed';
  const trace = { element: '1-abc', durationMs: 19_000, others: [6243], width: 720, height: 1280, audio: 1, recent: [], segments: [], name: 'someone - a caption', href: FEED };

  it('records the played file, reads its head without credentials, probes and adds it, bound to its address', async () => {
    fake.tabs[31] = { url: FEED, title: 'Social', active: true };
    // A piece whose byte range is in its query, fetched by the player's script.
    load({
      tabId: 31,
      type: 'xmlhttprequest',
      url: 'https://media.example/v/a.mp4?oh=1&bytestart=0&byteend=999',
      statusCode: 200,
      headers: { 'Content-Type': 'video/mp4', 'Content-Length': '1000' },
      ip: '93.184.215.14',
    });
    const fetched: { url: string; init: RequestInit }[] = [];
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string, init: RequestInit) => {
        fetched.push({ url, init });
        const head = new Uint8Array(64);
        head.set([0, 0, 0, 16, 0x66, 0x74, 0x79, 0x70]);
        return new Response(head, { status: 206, headers: { 'Content-Range': 'bytes 0-63/3000000' } });
      }),
    );
    await settle();
    const ask = () => send<Offers & { value: { played?: Record<string, unknown> } }>({ type: 'downex:frame-offers', src: '', mse: true, played: trace }, frame(31, 0, FEED));
    let o = (await ask())!;
    expect(o.value.played).toMatchObject({ key: 'p1' });
    await vi.waitFor(async () => {
      o = (await ask())!;
      expect(o.value.played).toMatchObject({ probe: { result: { ok: true } } });
    });
    // The head was read by the background itself, without credentials, at the whole file's address.
    expect(fetched[0]).toMatchObject({ url: 'https://media.example/v/a.mp4?oh=1', init: { credentials: 'omit', headers: { Range: 'bytes=0-65535' } } });
    const probe = posts('probePlayed').at(-1) as unknown as { pageUrl: string; played: { resources: Record<string, unknown>[]; durationMs: number } };
    expect(probe.pageUrl).toBe(FEED);
    expect(probe.played.durationMs).toBe(19_000);
    // The frame's other videos' lengths go with it: their files are theirs.
    expect((probe.played as { otherDurationsMs?: number[] }).otherDurationsMs).toEqual([6243]);
    expect(probe.played.resources).toEqual([
      { id: 'r1', url: 'https://media.example/v/a.mp4?oh=1', size: 3_000_000, kind: 'video', head: expect.any(String), browser: false },
    ]);
    // No cookie, no Referer: none was read.
    expect(JSON.stringify(probe)).not.toContain('Cookie');
    const added = await send<{ ok: boolean }>({ type: 'downex:offer-add', key: 'p1', videoTrack: 'r1', played: trace }, frame(31, 0, FEED));
    expect(added).toMatchObject({ ok: true });
    expect(posts('addPlayed').at(-1)).toMatchObject({ pageUrl: FEED, videoTrack: 'r1', title: 'someone - a caption' });
    await vi.waitFor(() => {
      const tickets = fake.local.data.browserFetchTickets as { ticket: string; played?: { urls: string[]; include: string[] } }[];
      expect(tickets.at(-1)).toMatchObject({ ticket: '0123456789abcdef0123456789abcdef', played: { urls: ['https://media.example/v/a.mp4?oh=1'], include: [] } });
    });
  });

  it('refuses a Played source and its add once the element is protected, and names a nameless post "<Site> video"', async () => {
    fake.tabs[35] = { url: FEED, title: 'Watch trending videos for you | Social', active: true };
    const drm = await send<Offers & { value: { played?: unknown; drm: boolean } }>(
      { type: 'downex:frame-offers', src: '', mse: true, played: { ...trace, drm: true } },
      frame(35, 0, FEED),
    );
    expect(drm?.value.drm).toBe(true);
    expect(drm?.value).not.toHaveProperty('played');
    const before = posts('addPlayed').length;
    await expect(send({ type: 'downex:offer-add', key: 'p1', played: { ...trace, drm: true } }, frame(35, 0, FEED))).resolves.toMatchObject({
      ok: false,
      error: { reason: 'encrypted' },
    });
    expect(posts('addPlayed')).toHaveLength(before);
    // A post whose name was not found: never the feed's own title.
    const nameless = await send<Offers & { value: { title: string } }>({ type: 'downex:frame-offers', src: '', mse: true, played: { ...trace, element: '1-nameless', name: '' } }, frame(35, 0, FEED));
    expect(nameless?.value.title).toBe('social.example video');
  });

  it('offers no Played source on YouTube, for a file element, or without a trace', async () => {
    fake.tabs[32] = { url: 'https://www.youtube.com/watch?v=abcdefghijk', title: 'YouTube', active: true };
    const yt = await send<Offers & { value: { played?: unknown } }>(
      { type: 'downex:frame-offers', src: '', mse: true, played: { ...trace, href: 'https://www.youtube.com/watch?v=abcdefghijk' } },
      frame(32, 0, 'https://www.youtube.com/watch?v=abcdefghijk'),
    );
    expect(yt?.value).not.toHaveProperty('played');
    fake.tabs[33] = { url: FEED, title: 'Social', active: true };
    const file = await send<Offers & { value: { played?: unknown } }>({ type: 'downex:frame-offers', src: MP4, mse: false, played: trace }, frame(33, 0, FEED));
    expect(file?.value).not.toHaveProperty('played');
    // A trace out of bounds is refused as a message.
    await expect(send({ type: 'downex:frame-offers', src: '', mse: true, played: { ...trace, durationMs: -1 } }, frame(33, 0, FEED))).resolves.toBeUndefined();
  });

  it('judges the frame by the address read at the click, on the same origin only (VIDEO-DETECT-SPEC §3.6)', async () => {
    fake.tabs[34] = { url: 'https://www.tiktok.com/foryou', title: 'TikTok', active: true };
    const own = 'https://www.tiktok.com/@someone/video/7123456789012345678';
    const o = await send<Offers>({ type: 'downex:frame-offers', src: '', mse: true, played: { ...trace, href: own } }, frame(34, 0, 'https://www.tiktok.com/foryou'));
    expect(o?.value.site).toMatchObject({ key: 'tiktok:7123456789012345678' });
    const other = await send<Offers>(
      { type: 'downex:frame-offers', src: '', mse: true, played: { ...trace, href: 'https://evil.example/@someone/video/7123456789012345678' } },
      frame(34, 0, 'https://www.tiktok.com/foryou'),
    );
    expect(other?.value.site).toBeUndefined();
  });
});
