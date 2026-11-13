// Wiring of page videos in the background (SITE-MEDIA-SPEC §3.5, §3.6, §5.2) against a fake
// browser: the overlay's and the popup's requests, the checks on their senders, the shared probe
// cache, the badge, the context menu and the probeVideo/addVideo sent to the native relay.

import { beforeAll, describe, expect, it, vi } from 'vitest';

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
  const area = () => {
    const data: Record<string, unknown> = {};
    return {
      data,
      get: async (keys: string | string[] | null) => {
        if (keys === null) return structuredClone(data);
        const list = Array.isArray(keys) ? keys : [keys];
        return structuredClone(Object.fromEntries(list.filter((k) => k in data).map((k) => [k, data[k]])));
      },
      set: async (items: Record<string, unknown>) => void Object.assign(data, structuredClone(items)),
      remove: async (keys: string | string[]) => {
        for (const k of Array.isArray(keys) ? keys : [keys]) delete data[k];
      },
    };
  };
  const session = area();
  const local = area();
  const posted: (Record<string, unknown> & { id: string; type: string })[] = [];
  /** While set, video requests wait in `held` (a daemon resolving the page). */
  const flow = { hold: false, held: [] as (() => void)[], videoAnswer: undefined as Record<string, unknown> | undefined };
  const videoInfo = {
    type: 'videoInfo',
    site: 'YouTube',
    title: 'From yt-dlp',
    durationMs: 1000,
    format: 'http',
    container: 'mp4',
    selectedVideo: '299',
    selectedAudio: '140',
    tracks: [
      { id: '299', kind: 'video', ext: 'mp4', codecs: 'avc1.64002a', width: 1920, height: 1080, frameRate: 60, bandwidth: 6000000, size: 412000000, sizeApprox: false, language: '', name: '1080p (Full HD) 60fps' },
      { id: '140', kind: 'audio', ext: 'm4a', codecs: 'mp4a.40.2', width: 0, height: 0, frameRate: 0, bandwidth: 129000, size: 3000000, sizeApprox: false, language: 'en', name: 'English' },
    ],
  };
  const port = {
    onMessage: new FakeEvent(),
    onDisconnect: new FakeEvent(),
    postMessage(m: unknown) {
      const msg = JSON.parse(JSON.stringify(m)) as Record<string, unknown> & { id: string; type: string };
      posted.push(msg);
      const reply = (r: Record<string, unknown>) => queueMicrotask(() => port.onMessage.emit({ id: msg.id, ...r }));
      if (msg.type === 'hello') reply({ ok: true, type: 'welcome', protocol: 1, daemonVersion: '9.9.9', features: ['video'] });
      else if (msg.type === 'status') reply({ ok: true, type: 'status', running: true, active: 0, queued: 0 });
      else if (msg.type === 'probeVideo' || msg.type === 'addVideo') {
        const answer =
          flow.videoAnswer ??
          (msg.type === 'probeVideo' ? { ok: true, ...videoInfo } : { ok: true, type: 'added', downloadId: 31, awaiting: true });
        if (flow.hold) flow.held.push(() => reply(answer));
        else reply(answer);
      } else if (msg.type === 'add') reply({ ok: true, type: 'added', downloadId: 5 });
    },
    disconnect() {},
  };
  const tabs: Record<number, Record<string, unknown>> = {};
  const frames: Record<number, { frameId: number; url: string; parentFrameId: number }[]> = {};
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
      const t = tabs[id];
      if (!t) throw new Error(`No tab with id: ${id}.`);
      return { id, ...t };
    },
    'tabs.query': async () => [{ id: 70, url: 'https://www.youtube.com/watch?v=ccccccccccc' }],
    'webNavigation.getAllFrames': async ({ tabId }: { tabId: number }) => frames[tabId] ?? [],
    'cookies.getAll': async (q: unknown) => {
      cookieCalls.push(q);
      return [{ name: 'SID', value: 'sign-in' }];
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
  return { browser: node(''), events, calls, session, local, posted, flow, tabs, frames, cookieCalls };
});

vi.mock('wxt/browser', () => ({ browser: fake.browser }));

const event = (name: string) => {
  const e = fake.events.get(name);
  if (!e) throw new Error(`no listener for ${name}`);
  return e;
};

const POPUP = { id: 'ext-id', url: 'chrome-extension://ext-id/popup.html' };
const ID = 'aqz-KE-bpKQ';
const WATCH = `https://www.youtube.com/watch?v=${ID}`;

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

/** The YouTube content script of tab `tabId`, frame `frameId`. */
const youtubeFrame = (tabId: number, frameId = 0, url = WATCH, tab: Record<string, unknown> = { url: WATCH }) => ({
  id: 'ext-id',
  url,
  origin: new URL(url).origin,
  frameId,
  tab: { id: tabId, ...tab },
});

const posts = (type: string) => fake.posted.filter((m) => m.type === type);

function badge(tabId: number) {
  return fake.calls
    .filter((c) => c.path === 'action.setBadgeText')
    .map((c) => c.args[0] as { tabId?: number; text?: string | null })
    .filter((a) => a.tabId === tabId)
    .at(-1)?.text;
}

describe('background page videos', () => {
  beforeAll(async () => {
    const bg = (await import('../entrypoints/background')).default as { main: () => void };
    bg.main();
    await vi.waitFor(() => expect(posts('hello')).toHaveLength(1));
  });

  it('probes the frame video of the overlay once, through the shared cache', async () => {
    fake.flow.hold = true;
    const sender = youtubeFrame(1);
    const a = send<Record<string, unknown>>({ type: 'downex:page-video-probe', url: `${WATCH}&t=30s` }, sender);
    const b = send<Record<string, unknown>>({ type: 'downex:page-video-probe', url: WATCH }, sender);
    await vi.waitFor(() => expect(fake.flow.held).toHaveLength(1));
    expect(posts('probeVideo')).toEqual([expect.objectContaining({ pageUrl: WATCH, deadlineMs: expect.any(Number) })]);
    // Without the sign-in option no cookie goes out.
    expect(posts('probeVideo')[0]).not.toHaveProperty('headers');
    fake.flow.hold = false;
    fake.flow.held.shift()?.();
    await expect(a).resolves.toMatchObject({ ok: true, value: { type: 'videoInfo', title: 'From yt-dlp' } });
    await expect(b).resolves.toMatchObject({ ok: true });
    // Asked again (the popup, another tab): the kept answer.
    await expect(send({ type: 'downex:page-video-probe', url: WATCH }, youtubeFrame(2))).resolves.toMatchObject({ ok: true });
    expect(posts('probeVideo')).toHaveLength(1);
  });

  it('answers a content script only about its own frame', async () => {
    const before = posts('probeVideo').length;
    const asked = [
      // Another origin than the asking frame.
      send({ type: 'downex:page-video-probe', url: WATCH }, youtubeFrame(1, 0, 'https://m.youtube.com/watch?v=x')),
      // Neither a video page nor the frame's own address.
      send({ type: 'downex:page-video-probe', url: 'https://www.youtube.com/feed/library' }, youtubeFrame(1, 0, 'https://www.youtube.com/feed/history')),
      // A private window, or no tab at all.
      send({ type: 'downex:page-video-probe', url: WATCH }, youtubeFrame(1, 0, WATCH, { url: WATCH, incognito: true })),
      send({ type: 'downex:page-video-probe', url: WATCH }, { id: 'ext-id', url: WATCH, frameId: 0 }),
    ];
    for (const a of asked) await expect(a).resolves.toMatchObject({ ok: false, error: { code: 'unsupported_page' } });
    // An extension page cannot use the content script request.
    await expect(ask({ type: 'downex:page-video-probe', url: WATCH })).resolves.toBeUndefined();
    // Nor a content script the popup's.
    await expect(send({ type: 'downex:tab-video-probe', tabId: 1, key: `youtube:${ID}` }, youtubeFrame(1))).resolves.toBeUndefined();
    expect(posts('probeVideo')).toHaveLength(before);
  });

  it('adds the overlay pick, with the embedding page as Referer for an embedded player', async () => {
    const embed = `https://www.youtube-nocookie.com/embed/${ID}`;
    const sender = youtubeFrame(3, 5, embed, { url: 'https://blog.example/post', title: 'Blog' });
    const r = await send({ type: 'downex:page-video-add', url: embed, videoTrack: '299', audioTrack: '140' }, sender);
    expect(r).toEqual({ ok: true, value: { type: 'added', downloadId: 31, awaiting: true } });
    expect(posts('addVideo').at(-1)).toMatchObject({
      pageUrl: WATCH,
      videoTrack: '299',
      audioTrack: '140',
      headers: [['Referer', 'https://blog.example/']],
    });
    expect(posts('addVideo').at(-1)).not.toHaveProperty('title');
    await vi.waitFor(() =>
      expect(fake.session.data.handoffHistory).toEqual(
        expect.arrayContaining([expect.objectContaining({ source: 'video', ok: true, downloadId: 31, awaiting: true, url: WATCH })]),
      ),
    );
  });

  it('lists the page videos for the popup before the streams, hiding googlevideo manifests', async () => {
    fake.tabs[4] = { url: WATCH, title: 'A video - YouTube' };
    fake.frames[4] = [
      { frameId: 0, url: WATCH, parentFrameId: -1 },
      { frameId: 9, url: 'https://www.youtube.com/embed/aaaaaaaaaaa', parentFrameId: 0 },
    ];
    for (const url of ['https://manifest.googlevideo.com/api/manifest/hls_variant/x.m3u8', 'https://cdn.example.com/other.m3u8']) {
      event('webRequest.onResponseStarted').emit({
        url,
        method: 'GET',
        type: 'xmlhttprequest',
        tabId: 4,
        statusCode: 200,
        responseHeaders: [{ name: 'Content-Type', value: 'application/vnd.apple.mpegurl' }],
      });
    }
    await vi.waitFor(async () => expect((await ask<{ items: unknown[] }>({ type: 'downex:tab-media', tabId: 4 }))?.items).toHaveLength(1));
    const r = await ask<{ items: { url: string }[]; videos: Record<string, unknown>[] }>({ type: 'downex:tab-media', tabId: 4 });
    expect(r?.items.map((i) => i.url)).toEqual(['https://cdn.example.com/other.m3u8']);
    expect(r?.videos).toEqual([
      expect.objectContaining({ key: `youtube:${ID}`, site: 'YouTube', frame: 'top', pageUrl: WATCH, title: 'From yt-dlp', probe: { result: expect.objectContaining({ ok: true }) } }),
      { key: 'youtube:aaaaaaaaaaa', site: 'YouTube', kind: 'embed', frame: 'embed', pageUrl: 'https://www.youtube.com/watch?v=aaaaaaaaaaa' },
    ]);
  });

  it('probes and adds a popup row looked up again from the tab', async () => {
    fake.tabs[6] = { url: 'https://blog.example/a', title: 'Blog' };
    fake.frames[6] = [{ frameId: 2, url: 'https://www.youtube.com/embed/bbbbbbbbbbb', parentFrameId: 0 }];
    const probe = await ask({ type: 'downex:tab-video-probe', tabId: 6, key: 'youtube:bbbbbbbbbbb' });
    expect(probe).toMatchObject({ ok: true });
    expect(posts('probeVideo').at(-1)).toMatchObject({
      pageUrl: 'https://www.youtube.com/watch?v=bbbbbbbbbbb',
      headers: [['Referer', 'https://blog.example/']],
    });
    const add = await ask({ type: 'downex:tab-video-add', tabId: 6, key: 'youtube:bbbbbbbbbbb', videoTrack: '', audioTrack: '' });
    expect(add).toMatchObject({ ok: true });
    expect(posts('addVideo').at(-1)).toEqual(
      expect.objectContaining({ pageUrl: 'https://www.youtube.com/watch?v=bbbbbbbbbbb' }),
    );
    expect(posts('addVideo').at(-1)).not.toHaveProperty('videoTrack');
    // A key the page no longer shows.
    await expect(ask({ type: 'downex:tab-video-add', tabId: 6, key: 'youtube:ccccccccccc', videoTrack: '', audioTrack: '' })).resolves.toMatchObject({
      ok: false,
      error: { code: 'expired' },
    });
    // The add is listed for a popup opened again.
    const listed = await ask<{ videos: Record<string, unknown>[] }>({ type: 'downex:tab-media', tabId: 6 });
    expect(listed?.videos[0]).toMatchObject({ add: { result: { ok: true } } });
  });

  it('finds the video of any other page only when asked, without cookies', async () => {
    fake.tabs[7] = { url: 'https://video.example/v/1#top', title: 'Clip' };
    await fake.local.set({ videoOptions: { videoButton: true, useSignInForVideos: true } });
    expect((await ask<{ videos: unknown[] }>({ type: 'downex:tab-media', tabId: 7 }))?.videos).toEqual([]);
    const found = await ask<{ ok: boolean; value: { key: string } }>({ type: 'downex:find-page-video', tabId: 7 });
    expect(found).toEqual({
      ok: true,
      value: { key: 'page:https://video.example/v/1', site: 'video.example', kind: 'page', frame: 'top', pageUrl: 'https://video.example/v/1', title: 'Clip' },
    });
    const before = fake.cookieCalls.length;
    await expect(ask({ type: 'downex:tab-video-probe', tabId: 7, key: found!.value.key })).resolves.toMatchObject({ ok: true });
    expect(posts('probeVideo').at(-1)).toEqual(expect.objectContaining({ pageUrl: 'https://video.example/v/1' }));
    expect(posts('probeVideo').at(-1)).not.toHaveProperty('headers');
    expect(fake.cookieCalls).toHaveLength(before);
    expect((await ask<{ videos: unknown[] }>({ type: 'downex:tab-media', tabId: 7 }))?.videos).toEqual([
      expect.objectContaining({ key: 'page:https://video.example/v/1', site: 'YouTube', kind: 'page' }),
    ]);
    fake.tabs[8] = { url: 'chrome://settings' };
    await expect(ask({ type: 'downex:find-page-video', tabId: 8 })).resolves.toMatchObject({ ok: false, error: { code: 'unsupported_page' } });
  });

  it('store build: finds no video on a page that embeds a YouTube player, from the popup or an overlay', async () => {
    const page = 'https://embeds.example/post';
    const key = `page:${page}`;
    fake.tabs[12] = { url: page, title: 'Post' };
    fake.frames[12] = [
      { frameId: 0, url: page, parentFrameId: -1 },
      { frameId: 3, url: `https://www.youtube-nocookie.com/embed/${ID}`, parentFrameId: 0 },
    ];
    const top = { id: 'ext-id', url: page, origin: 'https://embeds.example', frameId: 0, tab: { id: 12, url: page, title: 'Post' } };
    const probes = posts('probeVideo').length;
    const adds = posts('addVideo').length;
    vi.stubEnv('MODE', 'store');
    try {
      await expect(ask({ type: 'downex:find-page-video', tabId: 12 })).resolves.toMatchObject({ ok: false, error: { code: 'unsupported_page' } });
      await expect(ask({ type: 'downex:tab-video-probe', tabId: 12, key })).resolves.toMatchObject({ ok: false, error: { code: 'expired' } });
      await expect(ask({ type: 'downex:tab-video-add', tabId: 12, key, videoTrack: '299', audioTrack: '140' })).resolves.toMatchObject({ ok: false });
      await expect(send({ type: 'downex:page-video-probe', url: page }, top)).resolves.toMatchObject({ ok: false, error: { code: 'unsupported_page' } });
      await expect(send({ type: 'downex:page-video-add', url: page, videoTrack: '299', audioTrack: '140' }, top)).resolves.toMatchObject({
        ok: false,
        error: { code: 'unsupported_page' },
      });
      expect((await ask<{ videos: unknown[] }>({ type: 'downex:tab-media', tabId: 12 }))?.videos).toEqual([]);
      expect(posts('probeVideo')).toHaveLength(probes);
      expect(posts('addVideo')).toHaveLength(adds);
    } finally {
      vi.unstubAllEnvs();
    }
    // The default build finds it.
    await expect(ask({ type: 'downex:find-page-video', tabId: 12 })).resolves.toMatchObject({ ok: true, value: { key } });
    await expect(send({ type: 'downex:page-video-probe', url: page }, top)).resolves.toMatchObject({ ok: true });
    expect(posts('probeVideo').at(-1)).toEqual(expect.objectContaining({ pageUrl: page }));
  });

  it('sends the page cookies with the sign-in option, asking again once it changed', async () => {
    // The option was turned on in the previous test: the answer kept without it is not used.
    const r = await send({ type: 'downex:page-video-probe', url: WATCH }, youtubeFrame(1));
    expect(r).toMatchObject({ ok: true });
    expect(posts('probeVideo').at(-1)).toMatchObject({ pageUrl: WATCH, headers: [['Cookie', 'SID=sign-in']] });
    expect(fake.cookieCalls.at(-1)).toEqual({ url: WATCH });
    // Turned off again: the answer got without the sign-in is used again.
    await fake.local.set({ videoOptions: { videoButton: true, useSignInForVideos: false } });
    const n = posts('probeVideo').length;
    await expect(send({ type: 'downex:page-video-probe', url: WATCH }, youtubeFrame(1))).resolves.toMatchObject({ ok: true });
    expect(posts('probeVideo')).toHaveLength(n);
  });

  it('routes a video link of the context menu to addVideo, never to add', async () => {
    const adds = posts('add').length;
    const onClicked = event('contextMenus.onClicked');
    onClicked.emit(
      { menuItemId: 'downex-download', linkUrl: `https://youtu.be/${ID}?t=3`, pageUrl: 'https://blog.example/links' },
      { id: 9, url: 'https://blog.example/links' },
    );
    await vi.waitFor(() => expect(posts('addVideo').at(-1)).toMatchObject({ pageUrl: WATCH, headers: [['Referer', 'https://blog.example/links']] }));
    expect(posts('addVideo').at(-1)).not.toHaveProperty('videoTrack');
    onClicked.emit(
      { menuItemId: 'downex-download', linkUrl: 'https://files.example/a.zip', pageUrl: 'https://blog.example/links' },
      { id: 9, url: 'https://blog.example/links' },
    );
    await vi.waitFor(() => expect(posts('add').length).toBe(adds + 1));
  });

  it('badges page videos with the streams, without googlevideo manifests', async () => {
    // A tab open before the worker started counts from its address.
    await vi.waitFor(() => expect(badge(70)).toBe('1'));
    event('webNavigation.onCommitted').emit({ tabId: 11, frameId: 0, url: 'https://blog.example/', documentLifecycle: 'active' });
    event('webNavigation.onCommitted').emit({ tabId: 11, frameId: 3, url: `https://www.youtube.com/embed/${ID}`, documentLifecycle: 'active' });
    expect(badge(11)).toBe('1');
    event('webRequest.onResponseStarted').emit({
      url: 'https://rr1.googlevideo.com/live/index.m3u8',
      method: 'GET',
      type: 'xmlhttprequest',
      tabId: 11,
      statusCode: 200,
      responseHeaders: [{ name: 'Content-Type', value: 'application/x-mpegurl' }],
    });
    event('webRequest.onResponseStarted').emit({
      url: 'https://cdn.example.com/x.mpd',
      method: 'GET',
      type: 'xmlhttprequest',
      tabId: 11,
      statusCode: 200,
      responseHeaders: [{ name: 'Content-Type', value: 'application/dash+xml' }],
    });
    await vi.waitFor(() => expect(badge(11)).toBe('2'));
    // A single-page move to a watch page of another video adds it, which takes in the streams of
    // its frame (VIDEO-DETECT-SPEC §5.3).
    event('webNavigation.onHistoryStateUpdated').emit({ tabId: 11, frameId: 0, url: 'https://www.youtube.com/watch?v=ddddddddddd' });
    expect(badge(11)).toBe('2');
    event('webNavigation.onHistoryStateUpdated').emit({ tabId: 11, frameId: 0, url: 'https://blog.example/again' });
    expect(badge(11)).toBe('2');
    // Another document: its frames and streams are gone.
    event('webNavigation.onCommitted').emit({ tabId: 11, frameId: 0, url: 'https://blog.example/next', documentLifecycle: 'active' });
    await vi.waitFor(() => expect(badge(11)).toBeNull());
  });
});
