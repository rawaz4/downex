// The look-ahead of page videos in the background (BROWSER-SPEC §4) against a fake browser and
// relay: the content script's report, the rules for sending a look-ahead (top frame of a shown
// tab, one at a time, newest next, the option, the relay and daemon, the back-off), the click
// that joins it and the shared probe cache the overlay and the popup read.

import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';

const fake = vi.hoisted(() => {
  type Fn = (...args: unknown[]) => unknown;
  class FakeEvent {
    regs: { fn: Fn }[] = [];
    addListener(fn: Fn) {
      this.regs.push({ fn });
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
  /**
   * `features`: what the next relay's welcome lists. While `hold` is set, probes wait in `held`
   * (a daemon resolving the page). `answer`: the answer to probes instead of the qualities.
   */
  const flow = {
    features: ['video', 'prefetch'],
    hold: false,
    held: [] as (() => void)[],
    answer: undefined as Record<string, unknown> | undefined,
  };
  const videoInfo = (id: string) => ({
    type: 'videoInfo',
    site: 'YouTube',
    title: `Video ${id}`,
    durationMs: 1000,
    format: 'http',
    container: 'mp4',
    selectedVideo: '299',
    selectedAudio: '140',
    tracks: [
      { id: '299', kind: 'video', ext: 'mp4', codecs: 'avc1.64002a', width: 1920, height: 1080, frameRate: 60, bandwidth: 6000000, size: 412000000, sizeApprox: false, language: '', name: '1080p (Full HD) 60fps' },
      { id: '140', kind: 'audio', ext: 'm4a', codecs: 'mp4a.40.2', width: 0, height: 0, frameRate: 0, bandwidth: 129000, size: 3000000, sizeApprox: false, language: 'en', name: 'English' },
    ],
  });
  type Port = { onMessage: FakeEvent; onDisconnect: FakeEvent; postMessage(m: unknown): void; disconnect(): void };
  const ports: Port[] = [];
  const connect = (): Port => {
    const features = [...flow.features];
    const port: Port = {
      onMessage: new FakeEvent(),
      onDisconnect: new FakeEvent(),
      postMessage(m: unknown) {
        const msg = JSON.parse(JSON.stringify(m)) as Record<string, unknown> & { id: string; type: string };
        posted.push(msg);
        const reply = (r: Record<string, unknown>) => queueMicrotask(() => port.onMessage.emit({ id: msg.id, ...r }));
        if (msg.type === 'hello') reply({ ok: true, type: 'welcome', protocol: 1, daemonVersion: '9.9.9', features });
        else if (msg.type === 'status') reply({ ok: true, type: 'status', running: true, active: 0, queued: 0 });
        else if (msg.type === 'probeVideo') {
          const id = new URL(String(msg.pageUrl)).searchParams.get('v') ?? '';
          const answer = flow.answer ?? { ok: true, ...videoInfo(id) };
          if (flow.hold) flow.held.push(() => reply(answer));
          else reply(answer);
        } else if (msg.type === 'addVideo') reply({ ok: true, type: 'added', downloadId: 31, awaiting: false });
      },
      disconnect() {},
    };
    ports.push(port);
    return port;
  };
  const tabs: Record<number, Record<string, unknown>> = {};
  const overrides: Record<string, unknown> = {
    'runtime.id': 'ext-id',
    'runtime.lastError': undefined,
    'runtime.getManifest': () => ({ version: '0.1.0' }),
    'runtime.getURL': (p: string) => `chrome-extension://ext-id${p}`,
    'runtime.connectNative': connect,
    'storage.session': session,
    'storage.local': local,
    'tabs.get': async (id: number) => {
      const t = tabs[id];
      if (!t) throw new Error(`No tab with id: ${id}.`);
      return { id, ...t };
    },
    'tabs.query': async () => [],
    'webNavigation.getAllFrames': async () => [],
    'cookies.getAll': async () => [],
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
      apply() {
        return Promise.resolve(undefined);
      },
    });
  return { browser: node(''), events, local, posted, flow, tabs, ports };
});

vi.mock('wxt/browser', () => ({ browser: fake.browser }));

const event = (name: string) => {
  const e = fake.events.get(name);
  if (!e) throw new Error(`no listener for ${name}`);
  return e;
};

const watch = (id: string) => `https://www.youtube.com/watch?v=${id}`;
const POPUP = { id: 'ext-id', url: 'chrome-extension://ext-id/popup.html' };

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

/** The YouTube content script of the top frame of tab `tabId`, which shows `url`. */
function topFrame(tabId: number, url: string, tab: Record<string, unknown> = {}) {
  fake.tabs[tabId] = { url, title: 'A video - YouTube', active: true, ...tab };
  return { id: 'ext-id', url, origin: new URL(url).origin, frameId: 0, tab: { id: tabId, ...fake.tabs[tabId] } };
}

/** A content script's sender, as the browser describes it. */
type Sender = { url: string } & Record<string, unknown>;

/** The content script's report that the user stayed on its video. */
const report = (sender: Sender, url = sender.url) => send({ type: 'downex:page-video-prefetch', url }, sender);
const click = (sender: Sender) => send<Record<string, unknown>>({ type: 'downex:page-video-probe', url: sender.url }, sender);

const probes = (id?: string) =>
  fake.posted.filter((m) => m.type === 'probeVideo' && (id === undefined || m.pageUrl === watch(id)));
/** Lets the background's promise chains run (storage reads, the relay's answers). */
const settle = () => new Promise((r) => setTimeout(r, 20));

describe('background look-ahead of page videos', () => {
  beforeAll(async () => {
    const bg = (await import('../entrypoints/background')).default as { main: () => void };
    bg.main();
    await vi.waitFor(() => expect(fake.posted.filter((m) => m.type === 'hello')).toHaveLength(1));
    await settle();
  });

  it('gets the qualities ahead into the cache the overlay and the popup read', async () => {
    const tab = topFrame(1, watch('aaaaaaaaaaa'));
    await expect(report(tab, `${watch('aaaaaaaaaaa')}&t=12s`)).resolves.toBeUndefined();
    await vi.waitFor(() => expect(probes('aaaaaaaaaaa')).toHaveLength(1));
    expect(probes('aaaaaaaaaaa')[0]).toMatchObject({ prefetch: true, deadlineMs: expect.any(Number) });
    expect(probes('aaaaaaaaaaa')[0]).not.toHaveProperty('headers');
    await settle();
    // The click on Download is answered from the cache: nothing more goes to Downex.
    await expect(click(tab)).resolves.toMatchObject({ ok: true, value: { title: 'Video aaaaaaaaaaa' } });
    const listed = await send<{ videos: Record<string, unknown>[] }>({ type: 'downex:tab-media', tabId: 1 }, POPUP);
    expect(listed?.videos[0]).toMatchObject({
      key: 'youtube:aaaaaaaaaaa',
      title: 'Video aaaaaaaaaaa',
      probe: { result: { ok: true, value: expect.objectContaining({ type: 'videoInfo' }) } },
    });
    // Reported again (back to the tab): kept, not asked again.
    await report(tab);
    await settle();
    expect(probes('aaaaaaaaaaa')).toHaveLength(1);
  });

  it('lets a click join the look-ahead still running', async () => {
    fake.flow.hold = true;
    const tab = topFrame(2, watch('bbbbbbbbbbb'));
    await report(tab);
    await vi.waitFor(() => expect(fake.flow.held).toHaveLength(1));
    const clicked = click(tab);
    await settle();
    expect(probes('bbbbbbbbbbb')).toHaveLength(1);
    fake.flow.hold = false;
    fake.flow.held.shift()?.();
    await expect(clicked).resolves.toMatchObject({ ok: true, value: { title: 'Video bbbbbbbbbbb' } });
    expect(probes('bbbbbbbbbbb')).toHaveLength(1);
  });

  it('sends one look-ahead at a time, the newest waiting video next', async () => {
    fake.flow.hold = true;
    await report(topFrame(3, watch('ccccccccccc')));
    await vi.waitFor(() => expect(fake.flow.held).toHaveLength(1));
    await report(topFrame(4, watch('ddddddddddd')));
    await report(topFrame(5, watch('eeeeeeeeeee')));
    await settle();
    expect(probes('ddddddddddd')).toHaveLength(0);
    expect(probes('eeeeeeeeeee')).toHaveLength(0);
    fake.flow.held.shift()?.();
    await vi.waitFor(() => expect(probes('eeeeeeeeeee')).toHaveLength(1));
    expect(probes('ddddddddddd')).toHaveLength(0);
    fake.flow.hold = false;
    fake.flow.held.shift()?.();
    await settle();
    // A waiting video the tab left meanwhile is not sent.
    fake.flow.hold = true;
    await report(topFrame(6, watch('fffffffffff')));
    await vi.waitFor(() => expect(fake.flow.held).toHaveLength(1));
    await report(topFrame(7, watch('ggggggggggg')));
    fake.tabs[7] = { url: 'https://www.youtube.com/', active: true };
    fake.flow.hold = false;
    fake.flow.held.shift()?.();
    await settle();
    expect(probes('ggggggggggg')).toHaveLength(0);
  });

  it('takes only the top frame of a shown tab, about a watch or Shorts video', async () => {
    const before = probes().length;
    const top = topFrame(8, watch('hhhhhhhhhhh'));
    await report({ ...top, frameId: 4 });
    await report({ ...top, tab: { ...top.tab, active: false } });
    await report(topFrame(8, 'https://www.youtube.com/live/hhhhhhhhhhh'));
    await report(top, 'https://m.youtube.com/watch?v=hhhhhhhhhhh');
    // Extension pages cannot ask for one.
    await send({ type: 'downex:page-video-prefetch', url: watch('hhhhhhhhhhh') }, POPUP);
    await settle();
    expect(probes()).toHaveLength(before);
    const shorts = topFrame(8, 'https://www.youtube.com/shorts/iiiiiiiiiii');
    await report(shorts);
    await vi.waitFor(() => expect(probes('iiiiiiiiiii')).toEqual([expect.objectContaining({ prefetch: true })]));
    await settle();
  });

  it('sends none with the option or the Download button off; with the sign-in on, one without cookies', async () => {
    for (const off of [
      { videoButton: true, useSignInForVideos: true, videoPrefetch: false, hiddenSites: [] },
      { videoButton: false, useSignInForVideos: true, videoPrefetch: true, hiddenSites: [] },
    ]) {
      await fake.local.set({ videoOptions: off });
      await report(topFrame(9, watch('jjjjjjjjjjj')));
      await settle();
      expect(probes('jjjjjjjjjjj')).toHaveLength(0);
    }
    // The sign-in switch does not stop look-aheads (SITE-MEDIA-SPEC §6.3): they never carry cookies.
    await fake.local.set({ videoOptions: { videoButton: true, useSignInForVideos: true, videoPrefetch: true, hiddenSites: [] } });
    await report(topFrame(9, watch('jjjjjjjjjjj')));
    await vi.waitFor(() => expect(probes('jjjjjjjjjjj')).toHaveLength(1));
    expect(probes('jjjjjjjjjjj')[0]).toEqual(expect.objectContaining({ prefetch: true }));
    expect(probes('jjjjjjjjjjj')[0]).not.toHaveProperty('headers');
    expect(probes('jjjjjjjjjjj')[0]).not.toHaveProperty('cookieDomain');
    await settle();
  });

  it('backs off after Downex said its tools are not ready, until the user clicks once', async () => {
    fake.flow.answer = { ok: false, error: { code: 'rejected', message: 'tools_unavailable: yt-dlp is not set up', reason: 'tools_unavailable' } };
    await report(topFrame(10, watch('kkkkkkkkkkk')));
    await vi.waitFor(() => expect(probes('kkkkkkkkkkk')).toHaveLength(1));
    await settle();
    await report(topFrame(11, watch('lllllllllll')));
    await settle();
    expect(probes('lllllllllll')).toHaveLength(0);
    // The click asks Downex itself (installing the tools) …
    fake.flow.answer = undefined;
    const tab = topFrame(10, watch('kkkkkkkkkkk'));
    await expect(click(tab)).resolves.toMatchObject({ ok: true });
    expect(probes('kkkkkkkkkkk')).toHaveLength(2);
    expect(probes('kkkkkkkkkkk')[1]).not.toHaveProperty('prefetch');
    // … and look-aheads go out again.
    await report(topFrame(11, watch('lllllllllll')));
    await vi.waitFor(() => expect(probes('lllllllllll')).toEqual([expect.objectContaining({ prefetch: true })]));
    await settle();
  });

  describe('on curated sites (VIDEO-DETECT-SPEC §5.5)', () => {
    afterEach(() => {
      vi.useRealTimers();
    });
    /** A quiet hour: the look-ahead budget is full again. */
    const quietHour = () => {
      vi.useFakeTimers({ toFake: ['Date'] });
      vi.setSystemTime(Date.now() + 2 * 60 * 60_000);
    };
    const pages = (url: string) => fake.posted.filter((m) => m.type === 'probeVideo' && m.pageUrl === url);
    const meaningful = { id: 1, src: '', mse: true, width: 640, height: 360, videoWidth: 0, videoHeight: 0, durationMs: 60_000, drm: false, visible: true, decorative: false, meaningful: true };
    const reportVideos = (sender: Record<string, unknown>, frameId: number, videos: unknown[]) =>
      send({ type: 'downex:frame-videos', videos }, { ...sender, frameId, documentId: `D${frameId}` });

    it('needs a meaningful video reported in some frame of the tab, and never a protected or live one', async () => {
      quietHour();
      const page = 'https://vimeo.com/76979871';
      // Vimeo's pages go to Downex as the player's address (VIDEO-DETECT-SPEC §3.8).
      const url = 'https://player.vimeo.com/video/76979871';
      const tab = topFrame(20, page, { title: 'A film on Vimeo' });
      await report(tab);
      await settle();
      expect(pages(url)).toHaveLength(0);
      // Its player plays a protected video: still none.
      await reportVideos(tab, 0, [{ ...meaningful, drm: true }]);
      await report(tab);
      await settle();
      expect(pages(url)).toHaveLength(0);
      // The video sits in a sub-frame.
      await reportVideos(tab, 3, [meaningful]);
      await report(tab);
      await vi.waitFor(() => expect(pages(url)).toEqual([expect.objectContaining({ prefetch: true })]));
      expect(pages(url)[0]).not.toHaveProperty('headers');
      await settle();
      // The top frame commits another document: its reports are gone.
      event('webNavigation.onCommitted').emit({ tabId: 20, frameId: 0, url: 'https://vimeo.com/1234', documentLifecycle: 'active' });
      const next = topFrame(20, 'https://vimeo.com/1234');
      await report(next);
      await settle();
      expect(pages('https://player.vimeo.com/video/1234')).toHaveLength(0);
    });

    it('runs a look-ahead held back for a while when the video is reported after the dwell', async () => {
      quietHour();
      const url = 'https://www.dailymotion.com/video/x8held1';
      const tab = topFrame(27, url);
      // The dwell fires before the (late) player reported its video: held back.
      await report(tab);
      await settle();
      expect(pages(url)).toHaveLength(0);
      await reportVideos(tab, 2, [meaningful]);
      await vi.waitFor(() => expect(pages(url)).toEqual([expect.objectContaining({ prefetch: true })]));
      await settle();
      // Held back longer than the dwell's grace: dropped.
      const late = 'https://www.dailymotion.com/video/x8held2';
      const other = topFrame(28, late);
      await report(other);
      vi.setSystemTime(Date.now() + 61_000);
      await reportVideos(other, 0, [meaningful]);
      await settle();
      expect(pages(late)).toHaveLength(0);
    });

    it('never looks ahead on Facebook, Instagram, Reddit or Threads, or on a hidden site', async () => {
      quietHour();
      for (const url of [
        'https://www.instagram.com/reel/C1a2B3c4D5e/',
        'https://www.facebook.com/watch/?v=1234567890',
        'https://www.reddit.com/r/videos/comments/abc111/x/',
        'https://www.threads.com/@someone/post/AbC12dEfGh',
      ]) {
        const tab = topFrame(21, url);
        await reportVideos(tab, 0, [meaningful]);
        await report(tab);
      }
      await settle();
      expect(fake.posted.filter((m) => m.type === 'probeVideo' && /instagram|facebook|reddit|threads/.test(String(m.pageUrl)))).toHaveLength(0);
      await fake.local.set({ videoOptions: { videoButton: true, useSignInForVideos: false, videoPrefetch: true, hiddenSites: ['*.dailymotion.com', 'www.youtube.com'] } });
      const dm = topFrame(22, 'https://www.dailymotion.com/video/x8abc12');
      await reportVideos(dm, 0, [meaningful]);
      await report(dm);
      await report(topFrame(23, watch('nnnnnnnnnnn')));
      await settle();
      expect(pages('https://www.dailymotion.com/video/x8abc12')).toHaveLength(0);
      expect(probes('nnnnnnnnnnn')).toHaveLength(0);
      await fake.local.set({ videoOptions: { videoButton: true, useSignInForVideos: false, videoPrefetch: true, hiddenSites: [] } });
      await report(dm);
      await vi.waitFor(() => expect(pages('https://www.dailymotion.com/video/x8abc12')).toHaveLength(1));
      await settle();
    });

    it('pauses a site that asks for a sign-in, never another', async () => {
      quietHour();
      fake.flow.answer = { ok: false, error: { code: 'rejected', message: 'auth_required: log in to watch', reason: 'auth_required' } };
      const first = topFrame(24, 'https://x.com/someone/status/111');
      await reportVideos(first, 0, [meaningful]);
      await report(first);
      await vi.waitFor(() => expect(pages('https://x.com/i/status/111')).toHaveLength(1));
      await settle();
      fake.flow.answer = undefined;
      const second = topFrame(25, 'https://x.com/someone/status/222');
      await reportVideos(second, 0, [meaningful]);
      await report(second);
      await report(topFrame(26, watch('ppppppppppp')));
      await vi.waitFor(() => expect(probes('ppppppppppp')).toHaveLength(1));
      await settle();
      expect(pages('https://x.com/i/status/222')).toHaveLength(0);
    });
  });

  it('sends none while the relay is down or does not serve look-aheads', async () => {
    fake.flow.features = ['video'];
    fake.ports.at(-1)?.onDisconnect.emit();
    await report(topFrame(12, watch('mmmmmmmmmmm')));
    await settle();
    expect(probes('mmmmmmmmmmm')).toHaveLength(0);
    // Reconnected to a relay that serves videos but not look-aheads.
    await vi.waitFor(() => expect(fake.posted.filter((m) => m.type === 'hello')).toHaveLength(2), { timeout: 5000 });
    await settle();
    await report(topFrame(12, watch('mmmmmmmmmmm')));
    await settle();
    expect(probes('mmmmmmmmmmm')).toHaveLength(0);
    // A click still gets the qualities.
    await expect(click(topFrame(12, watch('mmmmmmmmmmm')))).resolves.toMatchObject({ ok: true });
    expect(probes('mmmmmmmmmmm')).toEqual([expect.not.objectContaining({ prefetch: true })]);
  });
});
