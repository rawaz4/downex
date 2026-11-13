// Wiring of the background's media detection (MEDIA-SPEC §6.1) against a fake browser: the
// webRequest/webNavigation/tabs events it listens to, the badge, the popup messages and the
// `addMedia` it sends to the native relay.

import { beforeAll, describe, expect, it, vi } from 'vitest';

// Built inside vi.hoisted: the wxt/browser mock is installed before the background is imported.
const fake = vi.hoisted(() => {
  type Fn = (...args: unknown[]) => unknown;
  /** Records listeners with their filter and extraInfoSpec; emit returns what they returned. */
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
  const posted: Record<string, unknown>[] = [];
  /** While set, addMedia answers wait in `held` (a daemon still reading the manifests). */
  const flow = { hold: false, held: [] as (() => void)[], downloadId: 77 };
  const port = {
    onMessage: new FakeEvent(),
    onDisconnect: new FakeEvent(),
    postMessage(m: unknown) {
      const msg = JSON.parse(JSON.stringify(m)) as Record<string, unknown> & { id: string; type: string };
      posted.push(msg);
      const reply = (r: Record<string, unknown>) => queueMicrotask(() => port.onMessage.emit({ id: msg.id, ok: true, ...r }));
      if (msg.type === 'hello') reply({ type: 'welcome', protocol: 1, daemonVersion: '9.9.9' });
      else if (msg.type === 'status') reply({ type: 'status', running: true, active: 0, queued: 0 });
      else if (msg.type === 'addMedia') {
        const answer = { type: 'added', downloadId: flow.downloadId };
        if (flow.hold) flow.held.push(() => reply(answer));
        else reply(answer);
      }
    },
    disconnect() {},
  };
  const overrides: Record<string, unknown> = {
    'runtime.id': 'ext-id',
    'runtime.lastError': undefined,
    'runtime.getManifest': () => ({ version: '0.1.0' }),
    'runtime.getURL': (p: string) => `chrome-extension://ext-id${p}`,
    'runtime.connectNative': () => port,
    'storage.session': session,
    'storage.local': local,
    'tabs.get': async (id: number) => ({ id, url: 'https://site.example/watch?v=1', title: 'A video' }),
    'tabs.query': async () => [],
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
      apply(_t, _this, args: unknown[]) {
        calls.push({ path, args });
        return Promise.resolve(undefined);
      },
    });
  return { browser: node(''), events, calls, session, posted, port, flow };
});

vi.mock('wxt/browser', () => ({ browser: fake.browser }));

const event = (name: string) => {
  const e = fake.events.get(name);
  if (!e) throw new Error(`no listener for ${name}`);
  return e;
};

const POPUP = { id: 'ext-id', url: 'chrome-extension://ext-id/popup.html' };
const MANIFEST = 'https://cdn.example.com/v/1/master.m3u8';

function ask<T>(msg: unknown): Promise<T> {
  return new Promise<T>((resolve) => {
    const [kept] = event('runtime.onMessage').emit(msg, POPUP, resolve);
    expect(kept).toBe(true);
  });
}

function badgeCalls(tabId?: number) {
  return fake.calls
    .filter((c) => c.path === 'action.setBadgeText')
    .map((c) => c.args[0] as { tabId?: number; text?: string | null })
    .filter((a) => a.tabId === tabId);
}

function manifestLoad(tabId: number, url = MANIFEST) {
  event('webRequest.onSendHeaders').emit({
    url,
    method: 'GET',
    type: 'xmlhttprequest',
    tabId,
    requestHeaders: [
      { name: 'Cookie', value: 'sid=1' },
      { name: 'Referer', value: 'https://site.example/watch?v=1' },
      { name: 'Accept', value: '*/*' },
    ],
  });
  event('webRequest.onResponseStarted').emit({
    url,
    method: 'GET',
    type: 'xmlhttprequest',
    tabId,
    statusCode: 200,
    responseHeaders: [{ name: 'Content-Type', value: 'application/vnd.apple.mpegurl' }],
  });
}

describe('background media detection', () => {
  beforeAll(async () => {
    const bg = (await import('../entrypoints/background')).default as { main: () => void };
    bg.main();
    await vi.waitFor(() => expect(fake.posted.some((m) => m.type === 'hello')).toBe(true));
    await vi.waitFor(() => expect(badgeCalls(undefined).at(-1)?.text).toBe(''));
  });

  it('observes manifest loads without blocking them, with the headers the page sent', () => {
    const started = event('webRequest.onResponseStarted').regs[0];
    expect(started?.filter).toMatchObject({ types: ['xmlhttprequest', 'media', 'other', 'main_frame', 'sub_frame', 'object'] });
    expect(started?.extra).toEqual(['responseHeaders']);
    const send = event('webRequest.onSendHeaders').regs[0];
    expect((send?.filter as { types: string[] }).types).toContain('xmlhttprequest');
    expect(send?.extra).toEqual(['requestHeaders', 'extraHeaders']);
  });

  it('lists a detected stream for its tab, badges the count and keeps it in storage.session', async () => {
    manifestLoad(4);
    manifestLoad(4); // the same manifest again: still one
    await vi.waitFor(() => expect(badgeCalls(4).at(-1)?.text).toBe('1'));
    const r = await ask<{ items: Record<string, unknown>[] }>({ type: 'downex:tab-media', tabId: 4 });
    expect(r.items).toEqual([
      expect.objectContaining({ url: MANIFEST, format: 'hls', pageTitle: 'A video', pageUrl: 'https://site.example/watch?v=1' }),
    ]);
    expect(r.items[0]).not.toHaveProperty('headers');
    await vi.waitFor(() => expect(JSON.stringify(fake.session.data['media:4'])).toContain('sid=1'));
    expect((await ask<{ items: unknown[] }>({ type: 'downex:tab-media', tabId: 5 })).items).toEqual([]);
  });

  it('sends addMedia with the recorded headers and records it in the history', async () => {
    const r = await ask({ type: 'downex:add-media', tabId: 4, url: MANIFEST });
    expect(r).toEqual({ ok: true, value: { type: 'added', downloadId: 77 } });
    const sent = fake.posted.find((m) => m.type === 'addMedia');
    expect(sent).toMatchObject({
      manifestUrl: MANIFEST,
      pageUrl: 'https://site.example/watch?v=1',
      title: 'A video',
      headers: [
        ['Cookie', 'sid=1'],
        ['Referer', 'https://site.example/watch?v=1'],
        ['User-Agent', navigator.userAgent],
        ['Accept', '*/*'],
      ],
    });
    expect(typeof sent?.deadlineMs).toBe('number');
    await vi.waitFor(() =>
      expect(fake.session.data.handoffHistory).toEqual([expect.objectContaining({ source: 'media', ok: true, downloadId: 77 })]),
    );
    const gone = await ask({ type: 'downex:add-media', tabId: 4, url: 'https://cdn.example.com/other.m3u8' });
    expect(gone).toMatchObject({ ok: false, error: { code: 'expired' } });
  });

  it('sends one addMedia per stream at a time and lists a stream in flight as sending', async () => {
    const url = 'https://cdn.example.com/v/8/master.m3u8';
    manifestLoad(8, url);
    await vi.waitFor(() => expect(badgeCalls(8).at(-1)?.text).toBe('1'));
    const addMedias = () => fake.posted.filter((m) => m.type === 'addMedia').length;
    const before = addMedias();
    fake.flow.hold = true;
    fake.flow.downloadId = 78;
    const first = ask({ type: 'downex:add-media', tabId: 8, url });
    await vi.waitFor(() => expect(fake.flow.held).toHaveLength(1));
    const listed = await ask<{ items: Record<string, unknown>[] }>({ type: 'downex:tab-media', tabId: 8 });
    expect(listed.items[0]).toMatchObject({ url, sending: true });
    expect(listed.items[0]).not.toHaveProperty('result');
    // The popup closed while the daemon read the manifest, and was opened again.
    const followed = ask({ type: 'downex:media-result', tabId: 8, url });
    const second = ask({ type: 'downex:add-media', tabId: 8, url });
    await new Promise((r) => setTimeout(r, 20));
    expect(addMedias()).toBe(before + 1);
    fake.flow.hold = false;
    fake.flow.held.shift()?.();
    const added = { ok: true, value: { type: 'added', downloadId: 78 } };
    await expect(first).resolves.toEqual(added);
    await expect(second).resolves.toEqual(added);
    await expect(followed).resolves.toEqual({ result: added });
    // The answer outlives the popup for a while, so "Sent" survives a reopen.
    const after = await ask<{ items: Record<string, unknown>[] }>({ type: 'downex:tab-media', tabId: 8 });
    expect(after.items[0]).toMatchObject({ url, result: added });
    expect(after.items[0]).not.toHaveProperty('sending');
    await expect(ask({ type: 'downex:media-result', tabId: 8, url })).resolves.toEqual({ result: added });
    await expect(ask({ type: 'downex:media-result', tabId: 8, url: 'https://cdn.example.com/x.m3u8' })).resolves.toEqual(
      {},
    );
    // Another document in the tab: what was sent from the old one is forgotten.
    event('webNavigation.onCommitted').emit({ tabId: 8, frameId: 0, documentLifecycle: 'active' });
    manifestLoad(8, url);
    await vi.waitFor(async () =>
      expect((await ask<{ items: unknown[] }>({ type: 'downex:tab-media', tabId: 8 })).items).toHaveLength(1),
    );
    const fresh = await ask<{ items: Record<string, unknown>[] }>({ type: 'downex:tab-media', tabId: 8 });
    expect(fresh.items[0]).not.toHaveProperty('result');
    expect(fresh.items[0]).not.toHaveProperty('sending');
    fake.flow.downloadId = 77;
  });

  it('ignores content scripts asking for streams', () => {
    const [kept] = event('runtime.onMessage').emit(
      { type: 'downex:tab-media', tabId: 4 },
      { id: 'ext-id', url: 'https://site.example/watch', tab: { id: 4 } },
      () => undefined,
    );
    expect(kept).toBeUndefined();
  });

  it('clears the tab on a top-level navigation only', async () => {
    event('webNavigation.onCommitted').emit({ tabId: 4, frameId: 3, documentLifecycle: 'active' });
    event('webNavigation.onCommitted').emit({ tabId: 4, frameId: 0, documentLifecycle: 'prerender' });
    expect((await ask<{ items: unknown[] }>({ type: 'downex:tab-media', tabId: 4 })).items).toHaveLength(1);
    event('webNavigation.onCommitted').emit({ tabId: 4, frameId: 0, documentLifecycle: 'active' });
    expect((await ask<{ items: unknown[] }>({ type: 'downex:tab-media', tabId: 4 })).items).toEqual([]);
    await vi.waitFor(() => expect(fake.session.data['media:4']).toBeUndefined());
    expect(badgeCalls(4).at(-1)?.text).toBeNull();
  });

  it('clears a closed tab and lets the relay error badge win over counts', async () => {
    manifestLoad(6, 'https://cdn.example.com/a.mpd');
    await vi.waitFor(() => expect(badgeCalls(6).at(-1)?.text).toBe('1'));
    fake.port.onDisconnect.emit(fake.port);
    await vi.waitFor(() => expect(badgeCalls(undefined).at(-1)?.text).toBe('!'));
    expect(badgeCalls(6).at(-1)?.text).toBeNull();
    event('tabs.onRemoved').emit(6, {});
    await vi.waitFor(() => expect(fake.session.data['media:6']).toBeUndefined());
  });
});
