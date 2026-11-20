import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  type ConnectionStatus,
  createIdGenerator,
  FEATURE_PREFETCH,
  NativeClient,
  type NativePort,
  parseResponse,
  PROTOCOL_VERSION,
} from './protocol';

class FakeEvent<L extends (...args: never[]) => void> {
  listeners: L[] = [];
  addListener(l: L) {
    this.listeners.push(l);
  }
  removeListener(l: L) {
    this.listeners = this.listeners.filter((x) => x !== l);
  }
  emit(...args: Parameters<L>) {
    for (const l of [...this.listeners]) l(...args);
  }
}

class FakePort implements NativePort {
  sent: Record<string, unknown>[] = [];
  disconnected = false;
  onMessage = new FakeEvent<(m: unknown) => void>();
  onDisconnect = new FakeEvent<(p: never) => void>();
  postMessage(m: unknown) {
    if (this.disconnected) throw new Error('Attempting to use a disconnected port object');
    // Native messaging serialises to JSON; mimic it so non-JSON values surface.
    this.sent.push(JSON.parse(JSON.stringify(m)) as Record<string, unknown>);
  }
  disconnect() {
    this.disconnected = true;
  }
  /** Host side closes the pipe. */
  hostClose() {
    this.disconnected = true;
    this.onDisconnect.emit(this as never);
  }
  reply(m: unknown) {
    this.onMessage.emit(m);
  }
  last() {
    return this.sent[this.sent.length - 1] as Record<string, unknown> & { id: string; type: string };
  }
  welcome(daemonVersion = '1.2.3', protocol = 1) {
    const hello = this.sent.find((m) => m.type === 'hello') as { id: string };
    this.reply({ id: hello.id, ok: true, type: 'welcome', protocol, daemonVersion });
  }
}

function setup(opts: { failConnect?: () => boolean } = {}) {
  const ports: FakePort[] = [];
  const statuses: ConnectionStatus[] = [];
  let n = 0;
  const client = new NativeClient({
    connect: () => {
      if (opts.failConnect?.()) throw new Error('host not found');
      const p = new FakePort();
      ports.push(p);
      return p;
    },
    hello: () => ({ protocol: PROTOCOL_VERSION, browser: 'chrome', extVersion: '0.1.0' }),
    disconnectReason: () => 'Native host has exited.',
    onStatus: (s) => statuses.push(s),
    newId: () => `r${++n}`,
    requestTimeoutMs: 1000,
    helloTimeoutMs: 1000,
  });
  return { client, ports, statuses };
}

beforeEach(() => {
  vi.useFakeTimers();
});
afterEach(() => {
  vi.useRealTimers();
});

describe('added.renewCheck', () => {
  it('is kept when it is a boolean, and refuses the answer otherwise', () => {
    expect(parseResponse({ id: 'a', ok: true, type: 'added', downloadId: 7, awaiting: false, renewCheck: true })?.outcome).toEqual({
      ok: true,
      value: { type: 'added', downloadId: 7, awaiting: false, renewCheck: true },
    });
    expect(parseResponse({ id: 'a', ok: true, type: 'added', downloadId: 7 })?.outcome).toEqual({
      ok: true,
      value: { type: 'added', downloadId: 7 },
    });
    expect(parseResponse({ id: 'a', ok: true, type: 'added', downloadId: 7, renewCheck: 'yes' })?.outcome.ok).toBe(false);
  });
});

describe('parseResponse', () => {
  it('parses success and error responses', () => {
    expect(parseResponse({ id: 'a', ok: true, type: 'added', downloadId: 7 })).toEqual({
      id: 'a',
      outcome: { ok: true, value: { type: 'added', downloadId: 7 } },
    });
    expect(parseResponse({ id: 'a', ok: false, error: { code: 'rejected', message: 'no' } })).toEqual({
      id: 'a',
      outcome: { ok: false, error: { code: 'rejected', message: 'no' } },
    });
    expect(parseResponse({ id: 'a', ok: true, type: 'status', running: true, active: 1, queued: 0 })?.outcome.ok).toBe(
      true,
    );
    // Polite mode's fields (docs/POLITE-SPEC.md §8): optional, known states only, the rate only while slowed.
    const status = (extra: Record<string, unknown>) =>
      parseResponse({ id: 'a', ok: true, type: 'status', running: true, active: 1, queued: 0, ...extra })?.outcome;
    expect(status({})).toEqual({ ok: true, value: { type: 'status', running: true, active: 1, queued: 0 } });
    expect(status({ polite: 'slowed', politeRate: 2400000 })).toEqual({
      ok: true,
      value: { type: 'status', running: true, active: 1, queued: 0, polite: 'slowed', politeRate: 2400000 },
    });
    expect(status({ polite: 'watching', politeRate: 5 })).toEqual({
      ok: true,
      value: { type: 'status', running: true, active: 1, queued: 0, polite: 'watching' },
    });
    for (const junk of ['SLOWED', 'fast', 3, null, {}]) {
      expect(status({ polite: junk, politeRate: 1 })).toEqual({ ok: true, value: { type: 'status', running: true, active: 1, queued: 0 } });
    }
    expect(status({ polite: 'slowed', politeRate: -1 })).toEqual({
      ok: true,
      value: { type: 'status', running: true, active: 1, queued: 0, polite: 'slowed' },
    });
  });
  it('rejects uncorrelated and malformed messages', () => {
    expect(parseResponse(null)).toBeUndefined();
    expect(parseResponse({ ok: true, type: 'pong' })).toBeUndefined();
    expect(parseResponse({ id: 'a', ok: true, type: 'status', running: 'yes' })?.outcome).toMatchObject({
      ok: false,
      error: { code: 'protocol' },
    });
    expect(parseResponse({ id: 'a', type: 'pong' })?.outcome.ok).toBe(false);
  });
});

describe('createIdGenerator', () => {
  it('is unique and prefixed per session', () => {
    const g = createIdGenerator(() => 'abc');
    expect([g(), g(), g()]).toEqual(['abc-1', 'abc-2', 'abc-3']);
    const ids = new Set(Array.from({ length: 5 }, createIdGenerator()));
    expect(ids.size).toBe(5);
  });
});

describe('NativeClient', () => {
  it('sends hello on connect and becomes connected on welcome', async () => {
    const { client, ports, statuses } = setup();
    client.start();
    expect(ports).toHaveLength(1);
    const p = ports[0]!;
    expect(p.sent[0]).toEqual({ id: 'r1', type: 'hello', protocol: 1, browser: 'chrome', extVersion: '0.1.0' });
    expect(client.status.state).toBe('connecting');
    p.welcome('9.9.9');
    await vi.waitFor(() => expect(client.status).toMatchObject({ state: 'connected', daemonVersion: '9.9.9' }));
    expect(statuses.map((s) => s.state)).toEqual(['connecting', 'connected']);
  });

  it('correlates out-of-order responses by id', async () => {
    const { client, ports } = setup();
    client.start();
    const p = ports[0]!;
    p.welcome();
    const a = client.add({ url: 'https://a/x.zip', method: 'GET', headers: [['Cookie', 'a=1']] });
    const aId = p.last().id;
    expect(p.last()).toMatchObject({ type: 'add', url: 'https://a/x.zip', method: 'GET', headers: [['Cookie', 'a=1']] });
    const s = client.queryStatus();
    const sId = p.last().id;
    p.reply({ id: sId, ok: true, type: 'status', running: true, active: 2, queued: 3 });
    p.reply({ id: aId, ok: true, type: 'added', downloadId: '42' });
    await expect(s).resolves.toEqual({ ok: true, value: { type: 'status', running: true, active: 2, queued: 3 } });
    await expect(a).resolves.toEqual({ ok: true, value: { type: 'added', downloadId: '42' } });
  });

  it('passes relay errors through and flags mismatched response types', async () => {
    const { client, ports } = setup();
    client.start();
    const p = ports[0]!;
    p.welcome();
    const a = client.add({ url: 'https://a/x', method: 'GET', headers: [] });
    p.reply({ id: p.last().id, ok: false, error: { code: 'daemon_unavailable', message: 'down' } });
    await expect(a).resolves.toEqual({ ok: false, error: { code: 'daemon_unavailable', message: 'down' } });
    const ping = client.ping();
    p.reply({ id: p.last().id, ok: true, type: 'status', running: true, active: 0, queued: 0 });
    await expect(ping).resolves.toMatchObject({ ok: false, error: { code: 'protocol' } });
  });

  it('times out requests and ignores late responses', async () => {
    const { client, ports } = setup();
    client.start();
    const p = ports[0]!;
    p.welcome();
    const a = client.add({ url: 'https://a/x', method: 'GET', headers: [] }, { timeoutMs: 300 });
    const id = p.last().id;
    vi.advanceTimersByTime(299);
    let settled = false;
    void a.then(() => (settled = true));
    await Promise.resolve();
    expect(settled).toBe(false);
    vi.advanceTimersByTime(1);
    await expect(a).resolves.toMatchObject({ ok: false, error: { code: 'timeout' } });
    expect(() => p.reply({ id, ok: true, type: 'added', downloadId: 1 })).not.toThrow();
  });

  it('reports a late answer of a timed-out request to onLate', async () => {
    const { client, ports } = setup();
    client.start();
    const p = ports[0]!;
    p.welcome();
    const late = vi.fn();
    const a = client.add({ url: 'https://a/x', method: 'GET', headers: [], deadlineMs: 300 }, { timeoutMs: 300, onLate: late });
    const sent = p.last();
    expect(sent).toMatchObject({ type: 'add', deadlineMs: 300 });
    vi.advanceTimersByTime(300);
    await expect(a).resolves.toMatchObject({ ok: false, error: { code: 'timeout' } });
    expect(late).not.toHaveBeenCalled();
    p.reply({ id: sent.id, ok: true, type: 'added', downloadId: 9 });
    expect(late).toHaveBeenCalledWith({ ok: true, value: { type: 'added', downloadId: 9 } });
    p.reply({ id: sent.id, ok: true, type: 'added', downloadId: 9 });
    expect(late).toHaveBeenCalledTimes(1);
  });

  it('tracks whether the daemon runs: an empty daemonVersion is not ready', async () => {
    const { client, ports } = setup();
    client.start();
    const p = ports[0]!;
    p.welcome('');
    await vi.waitFor(() => expect(client.status.state).toBe('connected'));
    expect(client.status.daemonRunning).toBe(false);
    expect(client.daemonReady).toBe(false);
    const s = client.queryStatus();
    p.reply({ id: p.last().id, ok: true, type: 'status', running: true, active: 0, queued: 0 });
    await s;
    expect(client.daemonReady).toBe(true);
    const a = client.add({ url: 'https://a/x', method: 'GET', headers: [] });
    p.reply({ id: p.last().id, ok: false, error: { code: 'daemon_unavailable', message: 'down' } });
    await a;
    expect(client.daemonReady).toBe(false);
  });

  it('a welcome with a daemonVersion is ready', async () => {
    const { client, ports } = setup();
    client.start();
    ports[0]!.welcome('1.0.0');
    await vi.waitFor(() => expect(client.daemonReady).toBe(true));
  });

  it('rejects oversized requests locally', async () => {
    const { client, ports } = setup();
    client.start();
    const p = ports[0]!;
    const sentBefore = p.sent.length;
    const r = await client.add({ url: 'https://a/x', method: 'POST', headers: [], body: 'A'.repeat(2 * 1024 * 1024) });
    expect(r).toMatchObject({ ok: false, error: { code: 'too_large', message: 'request exceeds the 2,097,152-byte native-messaging limit' } });
    expect(p.sent.length).toBe(sentBefore);
  });

  it('fails pending requests on disconnect and reconnects with backoff 1 s → 60 s', async () => {
    const { client, ports, statuses } = setup();
    client.start();
    ports[0]!.welcome();
    const pending = client.ping();
    ports[0]!.hostClose();
    await expect(pending).resolves.toEqual({
      ok: false,
      error: { code: 'disconnected', message: 'Native host has exited.' },
    });
    expect(client.status.state).toBe('disconnected');
    expect(client.status.lastError).toBe('Native host has exited.');

    const delays: number[] = [];
    for (let i = 0; i < 8; i++) {
      const before = ports.length;
      const next = client.status.nextRetryAt! - Date.now();
      delays.push(next);
      vi.advanceTimersByTime(next - 1);
      expect(ports.length).toBe(before);
      vi.advanceTimersByTime(1);
      expect(ports.length).toBe(before + 1);
      ports[ports.length - 1]!.hostClose();
    }
    expect(delays).toEqual([1000, 2000, 4000, 8000, 16000, 32000, 60000, 60000]);

    // A welcome resets the backoff.
    vi.advanceTimersByTime(60000);
    ports[ports.length - 1]!.welcome();
    await vi.waitFor(() => expect(client.status.state).toBe('connected'));
    ports[ports.length - 1]!.hostClose();
    expect(client.status.nextRetryAt! - Date.now()).toBe(1000);
    expect(statuses.some((s) => s.state === 'connected')).toBe(true);
  });

  it('treats a failed hello as unavailable and retries', async () => {
    const { client, ports } = setup();
    client.start();
    const p = ports[0]!;
    p.reply({ id: p.sent[0]!.id, ok: false, error: { code: 'daemon_unavailable', message: 'cannot start' } });
    await vi.waitFor(() => expect(client.status.state).toBe('disconnected'));
    expect(p.disconnected).toBe(true);
    expect(client.status.lastError).toContain('daemon_unavailable');
    vi.advanceTimersByTime(1000);
    expect(ports).toHaveLength(2);
  });

  it('connects on demand when a request arrives during backoff', async () => {
    let fail = true;
    const { client, ports } = setup({ failConnect: () => fail });
    client.start();
    expect(client.status.state).toBe('disconnected');
    const r = await client.ping();
    expect(r).toMatchObject({ ok: false, error: { code: 'disconnected' } });
    fail = false;
    const r2 = client.ping();
    expect(ports).toHaveLength(1);
    const p = ports[0]!;
    // The ping waits for the welcome, then goes out at once.
    expect(p.sent.map((m) => m.type)).toEqual(['hello']);
    p.welcome();
    expect(p.sent.map((m) => m.type)).toEqual(['hello', 'ping']);
    p.reply({ id: p.last().id, ok: true, type: 'pong' });
    await expect(r2).resolves.toEqual({ ok: true, value: { type: 'pong' } });
  });

  it('sends requests made while connecting after the welcome, in order', async () => {
    const { client, ports } = setup();
    client.start();
    const p = ports[0]!;
    const s = client.queryStatus();
    const a = client.add({ url: 'https://a/x.zip', method: 'GET', headers: [] });
    expect(p.sent.map((m) => m.type)).toEqual(['hello']);
    p.welcome();
    expect(p.sent.map((m) => m.type)).toEqual(['hello', 'status', 'add']);
    p.reply({ id: p.sent[2]!.id, ok: true, type: 'added', downloadId: 5 });
    p.reply({ id: p.sent[1]!.id, ok: true, type: 'status', running: true, active: 1, queued: 0 });
    await expect(a).resolves.toEqual({ ok: true, value: { type: 'added', downloadId: 5 } });
    await expect(s).resolves.toMatchObject({ ok: true });
  });

  it('times a waiting request from when it was made and never sends it late', async () => {
    const { client, ports } = setup();
    client.start();
    const p = ports[0]!;
    const late = vi.fn();
    const a = client.add({ url: 'https://a/x', method: 'GET', headers: [], deadlineMs: 300 }, { timeoutMs: 300, onLate: late });
    vi.advanceTimersByTime(300);
    await expect(a).resolves.toMatchObject({ ok: false, error: { code: 'timeout' } });
    p.welcome();
    expect(p.sent.map((m) => m.type)).toEqual(['hello']);
    expect(late).not.toHaveBeenCalled();
  });

  it('takes the wait for the welcome off an add deadline', async () => {
    const { client, ports } = setup();
    client.start();
    const p = ports[0]!;
    const a = client.add({ url: 'https://a/x', method: 'GET', headers: [], deadlineMs: 3000 }, { timeoutMs: 3000 });
    vi.advanceTimersByTime(400);
    p.welcome();
    expect(p.last()).toMatchObject({ type: 'add', deadlineMs: 2600 });
    vi.advanceTimersByTime(2599);
    let settled = false;
    void a.then(() => (settled = true));
    await Promise.resolve();
    expect(settled).toBe(false);
    vi.advanceTimersByTime(1);
    await expect(a).resolves.toMatchObject({ ok: false, error: { code: 'timeout' } });
  });

  it('ignores an answer to a request that was not sent yet', async () => {
    const { client, ports } = setup();
    client.start();
    const p = ports[0]!;
    const r = client.ping();
    // The id the ping will use: hello was r1.
    p.reply({ id: 'r2', ok: true, type: 'pong' });
    p.welcome();
    expect(p.last()).toMatchObject({ id: 'r2', type: 'ping' });
    p.reply({ id: 'r2', ok: true, type: 'pong' });
    await expect(r).resolves.toEqual({ ok: true, value: { type: 'pong' } });
  });

  it('never uses a relay that speaks another protocol', async () => {
    const { client, ports, statuses } = setup();
    client.start();
    const p = ports[0]!;
    const a = client.add({ url: 'https://a/x.zip', method: 'GET', headers: [] });
    p.welcome('2.0.0', 2);
    await expect(a).resolves.toMatchObject({ ok: false, error: { code: 'incompatible' } });
    expect(p.sent.map((m) => m.type)).toEqual(['hello']);
    expect(p.disconnected).toBe(true);
    expect(client.daemonReady).toBe(false);
    expect(client.status).toMatchObject({ state: 'disconnected', incompatibleProtocol: 2 });
    expect(client.status.lastError).toBe('the Downex relay speaks protocol 2, this extension speaks protocol 1');
    expect(statuses.some((s) => s.state === 'connected')).toBe(false);

    // Retries back off as for any failure: an upgraded relay is picked up later.
    expect(client.status.nextRetryAt! - Date.now()).toBe(1000);
    vi.advanceTimersByTime(1000);
    const p2 = ports[1]!;
    p2.welcome('2.0.0', 2);
    expect(client.status.nextRetryAt! - Date.now()).toBe(2000);
    vi.advanceTimersByTime(2000);
    ports[2]!.welcome('1.0.0', 1);
    expect(client.status).toMatchObject({ state: 'connected', daemonVersion: '1.0.0' });
    expect(client.status.incompatibleProtocol).toBeUndefined();
    expect(client.daemonReady).toBe(true);
  });

  it('stop() fails pending requests and cancels reconnects', async () => {
    const { client, ports } = setup();
    client.start();
    const r = client.ping();
    client.stop();
    await expect(r).resolves.toMatchObject({ ok: false, error: { code: 'disconnected' } });
    expect(ports[0]!.disconnected).toBe(true);
    vi.advanceTimersByTime(120_000);
    expect(ports).toHaveLength(1);
    expect(client.status.state).toBe('idle');
  });
});

describe('NativeClient addMedia', () => {
  it('sends addMedia, takes the welcome wait off its deadline and expects added', async () => {
    const { client, ports } = setup();
    client.start();
    const p = ports[0]!;
    const fields = {
      manifestUrl: 'https://cdn.example.com/a/master.m3u8',
      headers: [['Cookie', 'a=1']] as [string, string][],
      pageUrl: 'https://site.example/watch',
      title: 'A video',
      deadlineMs: 5000,
    };
    const a = client.addMedia(fields, { timeoutMs: 5000 });
    vi.advanceTimersByTime(250);
    p.welcome();
    expect(p.last()).toMatchObject({ ...fields, type: 'addMedia', deadlineMs: 4750 });
    p.reply({ id: p.last().id, ok: true, type: 'added', downloadId: '12' });
    await expect(a).resolves.toEqual({ ok: true, value: { type: 'added', downloadId: '12' } });
    expect(client.daemonReady).toBe(true);

    const b = client.addMedia({ manifestUrl: 'https://cdn.example.com/b.mpd', headers: [] });
    p.reply({ id: p.last().id, ok: true, type: 'status', running: true, active: 0, queued: 0 });
    await expect(b).resolves.toMatchObject({ ok: false, error: { code: 'protocol' } });

    const c = client.addMedia({ manifestUrl: 'https://cdn.example.com/live.m3u8', headers: [] });
    p.reply({ id: p.last().id, ok: false, error: { code: 'rejected', message: 'live' } });
    await expect(c).resolves.toEqual({ ok: false, error: { code: 'rejected', message: 'live' } });
  });

  it('asks for the daemon status instead of taking an addMedia daemon_unavailable as a stopped daemon', async () => {
    const { client, ports, statuses } = setup();
    client.start();
    const p = ports[0]!;
    p.welcome();
    expect(client.daemonReady).toBe(true);
    // The daemon reads the manifests before it answers: the relay's deadline passed on a slow server.
    const a = client.addMedia({ manifestUrl: 'https://cdn.example.com/a.m3u8', headers: [] });
    const b = client.addMedia({ manifestUrl: 'https://cdn.example.com/b.m3u8', headers: [] });
    const [aId, bId] = p.sent.slice(-2).map((m) => m.id as string);
    const slow = { code: 'daemon_unavailable', message: 'downexd is not reachable: context deadline exceeded' };
    p.reply({ id: aId, ok: false, error: slow });
    p.reply({ id: bId, ok: false, error: slow });
    await expect(a).resolves.toEqual({ ok: false, error: slow });
    await expect(b).resolves.toEqual({ ok: false, error: slow });
    expect(client.daemonReady).toBe(true);
    expect(statuses.some((s) => s.daemonRunning === false)).toBe(false);
    // One status request for both.
    expect(p.sent.filter((m) => m.type === 'status')).toHaveLength(1);
    p.reply({ id: p.last().id, ok: true, type: 'status', running: true, active: 1, queued: 0 });
    await Promise.resolve();
    expect(client.daemonReady).toBe(true);

    // A daemon that really stopped: the status says so.
    const c = client.addMedia({ manifestUrl: 'https://cdn.example.com/c.m3u8', headers: [] });
    p.reply({ id: p.last().id, ok: false, error: { code: 'daemon_unavailable', message: 'cannot start downexd' } });
    await c;
    expect(p.last()).toMatchObject({ type: 'status' });
    expect(p.sent.filter((m) => m.type === 'status')).toHaveLength(2);
    p.reply({ id: p.last().id, ok: true, type: 'status', running: false, active: 0, queued: 0 });
    expect(client.daemonReady).toBe(false);
  });

  it('does not take a slow addMedia for a stopped daemon', async () => {
    const { client, ports } = setup();
    client.start();
    const p = ports[0]!;
    p.welcome();
    const a = client.addMedia({ manifestUrl: 'https://cdn.example.com/a.m3u8', headers: [] }, { timeoutMs: 500 });
    vi.advanceTimersByTime(500);
    await expect(a).resolves.toMatchObject({ ok: false, error: { code: 'timeout' } });
    expect(client.daemonReady).toBe(true);
  });
});

/** A videoInfo as the relay sends it. */
function videoInfo(extra: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    type: 'videoInfo',
    site: 'YouTube',
    title: 'A video',
    durationMs: 212000,
    format: 'http',
    container: 'mp4',
    selectedVideo: '299',
    selectedAudio: '140',
    tracks: [
      { id: '299', kind: 'video', ext: 'mp4', codecs: 'avc1.64002a', width: 1920, height: 1080, frameRate: 60, bandwidth: 6000000, size: 412000000, sizeApprox: false, language: '', name: '1080p (Full HD) 60fps' },
      { id: '140', kind: 'audio', ext: 'm4a', codecs: 'mp4a.40.2', width: 0, height: 0, frameRate: 0, bandwidth: 129000, size: 3400000, sizeApprox: true, language: 'en', name: 'English original' },
    ],
    ...extra,
  };
}

const infoTracks = () => videoInfo().tracks as Record<string, unknown>[];

describe('parseResponse of video answers', () => {
  it('reads a videoInfo, the welcome features, awaiting and the error reason', () => {
    const v = parseResponse({ id: 'a', ok: true, ...videoInfo() });
    expect(v?.outcome).toMatchObject({ ok: true, value: { type: 'videoInfo', site: 'YouTube', selectedVideo: '299' } });
    expect((v?.outcome as { ok: true; value: { tracks: unknown[] } }).value.tracks).toEqual(infoTracks());
    expect(
      parseResponse({ id: 'w', ok: true, type: 'welcome', protocol: 1, daemonVersion: '', features: ['video', 7, '', 'x'.repeat(33), 'later'] })
        ?.outcome,
    ).toEqual({ ok: true, value: { type: 'welcome', protocol: 1, daemonVersion: '', features: ['video', 'later'] } });
    const many = Array.from({ length: 20 }, (_, i) => 'f' + String(i));
    const w = parseResponse({ id: 'w', ok: true, type: 'welcome', protocol: 1, daemonVersion: '', features: many });
    expect((w?.outcome as { ok: true; value: { features: string[] } }).value.features).toEqual(many.slice(0, 16));
    expect(parseResponse({ id: 'w', ok: true, type: 'welcome', protocol: 1, daemonVersion: '' })?.outcome).toMatchObject({
      ok: true,
      value: { features: [] },
    });
    expect(parseResponse({ id: 'd', ok: true, type: 'added', downloadId: 3, awaiting: true })?.outcome).toEqual({
      ok: true,
      value: { type: 'added', downloadId: 3, awaiting: true },
    });
    expect(parseResponse({ id: 'd', ok: true, type: 'added', downloadId: 3, awaiting: 'yes' })?.outcome.ok).toBe(false);
    expect(
      parseResponse({ id: 'e', ok: false, error: { code: 'rejected', message: 'tools_installing: x', reason: 'tools_installing' } })?.outcome,
    ).toEqual({ ok: false, error: { code: 'rejected', message: 'tools_installing: x', reason: 'tools_installing' } });
    expect(parseResponse({ id: 'e', ok: false, error: { code: 'rejected', message: 'm', reason: 'Bad-Reason' } })?.outcome).toEqual({
      ok: false,
      error: { code: 'rejected', message: 'm' },
    });
  });

  it('leaves out tracks of other kinds', () => {
    const subtitles = { ...infoTracks()[1], id: 's', kind: 'subtitles', codecs: 'wvtt' };
    const v = parseResponse({ id: 'a', ok: true, ...videoInfo({ tracks: [...infoTracks(), subtitles] }) });
    expect((v?.outcome as { ok: true; value: { tracks: { id: string }[] } }).value.tracks.map((t) => t.id)).toEqual(['299', '140']);
  });

  const track = infoTracks()[0]!;
  it.each([
    ['site not a string', { site: 5 }],
    ['long site', { site: 's'.repeat(65) }],
    ['negative duration', { durationMs: -1 }],
    ['fractional duration', { durationMs: 1.5 }],
    ['no tracks', { tracks: undefined }],
    ['tracks not an array', { tracks: {} }],
    ['too many tracks', { tracks: Array.from({ length: 201 }, (_, i) => ({ ...track, id: 'v' + String(i) })) }],
    ['duplicate ids', { tracks: [track, track] }],
    ['track without id', { tracks: [{ ...track, id: '' }] }],
    ['NaN frame rate', { tracks: [{ ...track, frameRate: Number.NaN }] }],
    ['frame rate string', { tracks: [{ ...track, frameRate: '60' }] }],
    ['negative size', { tracks: [{ ...track, size: -1 }] }],
    ['huge height', { tracks: [{ ...track, height: 70000 }] }],
    ['sizeApprox missing', { tracks: [{ ...track, sizeApprox: undefined }] }],
    ['long name', { tracks: [{ ...track, name: 'n'.repeat(257) }] }],
    ['track null', { tracks: [null] }],
    ['selectedVideo number', { selectedVideo: 299 }],
  ])('refuses a malformed videoInfo: %s', (_name, extra) => {
    expect(parseResponse({ id: 'a', ok: true, ...videoInfo(extra) })?.outcome).toMatchObject({
      ok: false,
      error: { code: 'protocol' },
    });
  });
});

describe('NativeClient video requests', () => {
  const welcomeVideo = (p: FakePort) => {
    const hello = p.sent.find((m) => m.type === 'hello') as { id: string };
    p.reply({ id: hello.id, ok: true, type: 'welcome', protocol: 1, daemonVersion: '2.0.0', features: ['video'] });
  };
  const page = 'https://www.youtube.com/watch?v=aqz-KE-bpKQ';

  it('sends nothing to a relay without the video feature', async () => {
    const { client, ports } = setup();
    client.start();
    const p = ports[0]!;
    const a = client.probeVideo({ pageUrl: page, deadlineMs: 28000 });
    p.welcome('1.0.0');
    await expect(a).resolves.toEqual({ ok: false, error: { code: 'outdated_downex', message: 'Update Downex to download videos.' } });
    const b = client.addVideo({ pageUrl: page });
    await expect(b).resolves.toMatchObject({ ok: false, error: { code: 'outdated_downex' } });
    expect(p.sent.map((m) => m.type)).toEqual(['hello']);
    expect(client.status.features).toEqual([]);
  });

  it('sends probeVideo and addVideo, taking the welcome wait off their deadlines', async () => {
    const { client, ports } = setup();
    client.start();
    const p = ports[0]!;
    const probe = client.probeVideo({ pageUrl: page, headers: [['Cookie', 'SID=1']], deadlineMs: 28000 }, { timeoutMs: 28000 });
    vi.advanceTimersByTime(300);
    welcomeVideo(p);
    expect(client.status.features).toEqual(['video']);
    expect(p.last()).toMatchObject({ type: 'probeVideo', pageUrl: page, headers: [['Cookie', 'SID=1']], deadlineMs: 27700 });
    p.reply({ id: p.last().id, ok: true, ...videoInfo() });
    await expect(probe).resolves.toMatchObject({ ok: true, value: { type: 'videoInfo', tracks: [{ id: '299' }, { id: '140' }] } });

    const add = client.addVideo({ pageUrl: page, videoTrack: '299', audioTrack: '140', title: 'A video', deadlineMs: 28000 });
    expect(p.last()).toMatchObject({ type: 'addVideo', videoTrack: '299', audioTrack: '140', title: 'A video', deadlineMs: 28000 });
    p.reply({ id: p.last().id, ok: true, type: 'added', downloadId: 5, awaiting: false });
    await expect(add).resolves.toEqual({ ok: true, value: { type: 'added', downloadId: 5, awaiting: false } });

    // A probe answered with "added" is a protocol error.
    const bad = client.probeVideo({ pageUrl: page });
    p.reply({ id: p.last().id, ok: true, type: 'added', downloadId: 1 });
    await expect(bad).resolves.toMatchObject({ ok: false, error: { code: 'protocol' } });
  });

  it('sends a look-ahead probe only to a relay that serves it', async () => {
    const { client, ports } = setup();
    client.start();
    const p = ports[0]!;
    // A relay with videos but without look-aheads would drop the mark: the probe would install tools.
    const a = client.probeVideo({ pageUrl: page, prefetch: true, deadlineMs: 28000 });
    welcomeVideo(p);
    await expect(a).resolves.toMatchObject({ ok: false, error: { code: 'unsupported' } });
    expect(p.sent.map((m) => m.type)).toEqual(['hello']);
    // A user's probe still goes out.
    const user = client.probeVideo({ pageUrl: page });
    expect(p.last()).toMatchObject({ type: 'probeVideo', pageUrl: page });
    expect(p.last()).not.toHaveProperty('prefetch');
    p.reply({ id: p.last().id, ok: true, ...videoInfo() });
    await expect(user).resolves.toMatchObject({ ok: true });

    const { client: c2, ports: ports2 } = setup();
    c2.start();
    const q = ports2[0]!;
    const hello = q.sent.find((m) => m.type === 'hello') as { id: string };
    q.reply({ id: hello.id, ok: true, type: 'welcome', protocol: 1, daemonVersion: '2.1.0', features: ['video', FEATURE_PREFETCH] });
    const ahead = c2.probeVideo({ pageUrl: page, prefetch: true, deadlineMs: 28000 });
    expect(q.last()).toMatchObject({ type: 'probeVideo', pageUrl: page, prefetch: true, deadlineMs: 28000 });
    q.reply({ id: q.last().id, ok: true, ...videoInfo() });
    await expect(ahead).resolves.toMatchObject({ ok: true });
  });

  it('takes a video answer or a refusal from downexd as downexd running', async () => {
    // A welcome sent while downexd was still starting said it did not run.
    for (const answer of [
      (id: string) => ({ id, ok: true, ...videoInfo() }),
      (id: string) => ({ id, ok: false, error: { code: 'rejected', message: 'live: a live stream', reason: 'live' } }),
    ]) {
      const { client, ports } = setup();
      client.start();
      const p = ports[0]!;
      const hello = p.sent.find((m) => m.type === 'hello') as { id: string };
      p.reply({ id: hello.id, ok: true, type: 'welcome', protocol: 1, daemonVersion: '', features: ['video', FEATURE_PREFETCH] });
      expect(client.daemonReady).toBe(false);
      const probe = client.probeVideo({ pageUrl: page, deadlineMs: 28000 });
      p.reply(answer(p.last().id));
      await probe;
      expect(client.daemonReady).toBe(true);
    }
  });

  it('asks for the daemon status instead of taking a slow video request for a stopped daemon', async () => {
    const { client, ports } = setup();
    client.start();
    const p = ports[0]!;
    welcomeVideo(p);
    const a = client.probeVideo({ pageUrl: page });
    p.reply({ id: p.last().id, ok: false, error: { code: 'daemon_unavailable', message: 'downexd did not answer in time' } });
    await expect(a).resolves.toMatchObject({ ok: false, error: { code: 'daemon_unavailable' } });
    expect(client.daemonReady).toBe(true);
    expect(p.last()).toMatchObject({ type: 'status' });
  });
});

describe('NativeClient stream requests (VIDEO-DETECT-SPEC §3.5)', () => {
  const welcome = (p: FakePort, features: string[]) => {
    const hello = p.sent.find((m) => m.type === 'hello') as { id: string };
    p.reply({ id: hello.id, ok: true, type: 'welcome', protocol: 1, daemonVersion: '3.0.0', features });
  };
  const manifest = 'https://cdn.example.com/v/master.m3u8';
  const fields = {
    manifestUrl: manifest,
    headers: [['Cookie', 'sid=1']] as [string, string][],
    pageUrl: 'https://site.example/watch',
    deadlineMs: 28000,
  };

  it('never sends probeStream to a relay without the streams feature', async () => {
    for (const features of [[], ['video', 'prefetch']]) {
      const { client, ports } = setup();
      client.start();
      const p = ports[0]!;
      const a = client.probeStream(fields, { timeoutMs: 28000 });
      welcome(p, features);
      await expect(a).resolves.toEqual({
        ok: false,
        error: { code: 'outdated_downex', message: 'Update Downex to choose a stream quality.' },
      });
      expect(p.sent.map((m) => m.type)).toEqual(['hello']);
    }
  });

  it('sends a part only to a relay that lists clips, and reads videoInfo chapters', async () => {
    const part = { ...fields, clipStartMs: 80000, clipEndMs: 165000 };
    {
      const { client, ports } = setup();
      client.start();
      const p = ports[0]!;
      const a = client.addMedia(part, { timeoutMs: 15000 });
      welcome(p, ['video', 'streams']);
      await expect(a).resolves.toEqual({ ok: false, error: { code: 'outdated_downex', message: 'Update Downex to save part of a video.' } });
      expect(p.sent.map((m) => m.type)).toEqual(['hello']);
    }
    const { client, ports } = setup();
    client.start();
    const p = ports[0]!;
    const a = client.addMedia(part, { timeoutMs: 15000 });
    welcome(p, ['video', 'streams', 'clips']);
    expect(p.last()).toMatchObject({ type: 'addMedia', clipStartMs: 80000, clipEndMs: 165000 });
    p.reply({ id: p.last().id, ok: true, type: 'added', downloadId: 3, awaiting: false });
    await expect(a).resolves.toMatchObject({ ok: true });
    const probe = client.probeStream(fields);
    const chapters = [{ startMs: 0, endMs: 80000, title: 'Intro' }, { startMs: 80000, endMs: 165000, title: 'Rest' }];
    p.reply({ id: p.last().id, ok: true, ...videoInfo({ site: '', format: 'hls' }), chapters });
    await expect(probe).resolves.toMatchObject({ ok: true, value: { chapters } });
    // Chapters out of order are no chapters; the answer stands.
    const probe2 = client.probeStream(fields);
    p.reply({ id: p.last().id, ok: true, ...videoInfo({ site: '', format: 'hls' }), chapters: [chapters[1], chapters[0]] });
    const got = await probe2;
    expect(got.ok && got.value.chapters).toBe(undefined);
  });

  it('sends the sign-in’s domain only to a relay that lists signIn, the Cookie either way', async () => {
    const page = {
      pageUrl: 'https://www.instagram.com/reel/AbCdEfGh/',
      deadlineMs: 28000,
      headers: [['Cookie', 'sessionid=s1']] as [string, string][],
      cookieDomain: 'instagram.com',
    };
    for (const [features, domain] of [
      [['video'], undefined],
      [['video', 'signIn'], 'instagram.com'],
    ] as const) {
      const { client, ports } = setup();
      client.start();
      const p = ports[0]!;
      const probe = client.probeVideo({ ...page }, { timeoutMs: 28000 });
      const add = client.addVideo({ ...page }, { timeoutMs: 28000 });
      welcome(p, [...features]);
      const sent = p.sent.filter((m) => m.type === 'probeVideo' || m.type === 'addVideo');
      expect(sent).toHaveLength(2);
      for (const m of sent) {
        expect(m).toMatchObject({ headers: [['Cookie', 'sessionid=s1']] });
        expect(m.cookieDomain).toBe(domain);
      }
      for (const m of sent) p.reply({ id: m.id, ok: true, ...(m.type === 'probeVideo' ? videoInfo() : { type: 'added', downloadId: 1 }) });
      await probe;
      await add;
    }
  });

  it('sends an item of a page with several videos only to a relay that lists items', async () => {
    const page = { pageUrl: 'https://www.example.com/p/POST/', deadlineMs: 28000, entryId: 'itemB' };
    {
      const { client, ports } = setup();
      client.start();
      const p = ports[0]!;
      const a = client.probeVideo(page, { timeoutMs: 28000 });
      const b = client.addVideo(page, { timeoutMs: 28000 });
      welcome(p, ['video', 'prefetch', 'streams', 'clips']);
      for (const r of [a, b]) {
        await expect(r).resolves.toEqual({ ok: false, error: { code: 'outdated_downex', message: 'Update Downex to choose a video of this page.' } });
      }
      expect(p.sent.map((m) => m.type)).toEqual(['hello']);
    }
    const { client, ports } = setup();
    client.start();
    const p = ports[0]!;
    const a = client.probeVideo(page, { timeoutMs: 28000 });
    welcome(p, ['video', 'items']);
    expect(p.last()).toMatchObject({ type: 'probeVideo', entryId: 'itemB' });
    // Without an item, an older relay is still asked.
    const plain = { pageUrl: page.pageUrl };
    p.reply({ id: p.last().id, ok: true, ...videoInfo() });
    await a;
    const { client: c2, ports: p2 } = setup();
    c2.start();
    const q = c2.probeVideo(plain, { timeoutMs: 28000 });
    welcome(p2[0]!, ['video']);
    expect(p2[0]!.last()).toMatchObject({ type: 'probeVideo', pageUrl: page.pageUrl });
    p2[0]!.reply({ id: p2[0]!.last().id, ok: true, ...videoInfo() });
    await expect(q).resolves.toMatchObject({ ok: true });
  });

  it('sends probeStream to a relay with streams, taking the welcome wait off its deadline', async () => {
    const { client, ports } = setup();
    client.start();
    const p = ports[0]!;
    const a = client.probeStream(fields, { timeoutMs: 28000 });
    vi.advanceTimersByTime(400);
    welcome(p, ['video', 'prefetch', 'streams']);
    expect(p.last()).toEqual({ ...fields, type: 'probeStream', id: p.last().id, deadlineMs: 27600 });
    p.reply({ id: p.last().id, ok: true, ...videoInfo({ site: '', format: 'hls' }) });
    await expect(a).resolves.toMatchObject({ ok: true, value: { type: 'videoInfo', site: '', format: 'hls', container: 'mp4' } });
    // A probe answered with "added" is a protocol error.
    const bad = client.probeStream(fields);
    p.reply({ id: p.last().id, ok: true, type: 'added', downloadId: 1 });
    await expect(bad).resolves.toMatchObject({ ok: false, error: { code: 'protocol' } });
    // A refusal passes through with its reason.
    const refused = client.probeStream(fields);
    p.reply({ id: p.last().id, ok: false, error: { code: 'rejected', message: 'encrypted: DRM', reason: 'encrypted' } });
    await expect(refused).resolves.toEqual({ ok: false, error: { code: 'rejected', message: 'encrypted: DRM', reason: 'encrypted' } });
  });

  it('asks for the daemon status instead of taking a slow probeStream for a stopped daemon', async () => {
    const { client, ports } = setup();
    client.start();
    const p = ports[0]!;
    welcome(p, ['video', 'streams']);
    const a = client.probeStream(fields);
    p.reply({ id: p.last().id, ok: false, error: { code: 'daemon_unavailable', message: 'downexd did not answer in time' } });
    await expect(a).resolves.toMatchObject({ ok: false, error: { code: 'daemon_unavailable' } });
    expect(client.daemonReady).toBe(true);
    expect(p.last()).toMatchObject({ type: 'status' });
  });

  it('sends the tracks of addMedia only to a relay with streams', async () => {
    const media = { manifestUrl: manifest, headers: [], videoTrack: 'v720', audioTrack: 'a1' };
    const old = setup();
    old.client.start();
    const p = old.ports[0]!;
    welcome(p, ['video', 'prefetch']);
    const a = old.client.addMedia(media);
    expect(p.last()).toMatchObject({ type: 'addMedia', manifestUrl: manifest });
    expect(p.last()).not.toHaveProperty('videoTrack');
    expect(p.last()).not.toHaveProperty('audioTrack');
    p.reply({ id: p.last().id, ok: true, type: 'added', downloadId: 3 });
    await expect(a).resolves.toEqual({ ok: true, value: { type: 'added', downloadId: 3 } });

    const current = setup();
    current.client.start();
    const q = current.ports[0]!;
    // Made before the welcome: the tracks are decided when it is sent.
    const b = current.client.addMedia(media);
    welcome(q, ['video', 'prefetch', 'streams']);
    expect(q.last()).toMatchObject({ type: 'addMedia', videoTrack: 'v720', audioTrack: 'a1' });
    q.reply({ id: q.last().id, ok: true, type: 'added', downloadId: 4, awaiting: true });
    await expect(b).resolves.toEqual({ ok: true, value: { type: 'added', downloadId: 4, awaiting: true } });
  });

  it('reads awaiting on the answer to every add', () => {
    for (const awaiting of [true, false]) {
      expect(parseResponse({ id: 'a', ok: true, type: 'added', downloadId: 9, awaiting })?.outcome).toEqual({
        ok: true,
        value: { type: 'added', downloadId: 9, awaiting },
      });
    }
    expect(parseResponse({ id: 'a', ok: true, type: 'added', downloadId: 9 })?.outcome).toEqual({
      ok: true,
      value: { type: 'added', downloadId: 9 },
    });
    expect(parseResponse({ id: 'a', ok: true, type: 'added', downloadId: 9, awaiting: 'yes' })?.outcome).toMatchObject({
      ok: false,
      error: { code: 'protocol' },
    });
  });
});

describe('NativeClient rescue requests (BROWSER-SPEC §6)', () => {
  const welcome = (p: FakePort, features: string[]) => {
    const hello = p.sent.find((m) => m.type === 'hello') as { id: string };
    p.reply({ id: hello.id, ok: true, type: 'welcome', protocol: 1, daemonVersion: '3.0.0', features });
  };
  const fields = {
    url: 'https://cdn.example.com/big.iso',
    headers: [['Cookie', 'sid=1']] as [string, string][],
    target: '/home/u/Downloads/big.iso',
    bytesReceived: 4096,
    totalBytes: 10_000,
    startPaused: true,
    deadlineMs: 30_000,
  };

  it('never sends rescue or resume to a relay without the rescue feature', async () => {
    const { client, ports } = setup();
    client.start();
    const p = ports[0]!;
    const a = client.rescue(fields);
    const b = client.resume(7);
    welcome(p, ['video', 'prefetch', 'streams', 'renew']);
    const outdated = { ok: false, error: { code: 'outdated_downex', message: 'Update Downex to finish downloads that failed in the browser.' } };
    await expect(a).resolves.toEqual(outdated);
    await expect(b).resolves.toEqual(outdated);
    expect(p.sent.map((m) => m.type)).toEqual(['hello']);
  });

  it('sends rescue with its deadline less the welcome wait, and reads rescued and resumed', async () => {
    const { client, ports } = setup();
    client.start();
    const p = ports[0]!;
    const a = client.rescue(fields, { timeoutMs: 32_000 });
    vi.advanceTimersByTime(300);
    welcome(p, ['video', 'rescue']);
    expect(p.last()).toEqual({ ...fields, type: 'rescue', id: p.last().id, deadlineMs: 29_700 });
    p.reply({ id: p.last().id, ok: true, type: 'rescued', downloadId: 57, keptBytes: 4096 });
    await expect(a).resolves.toEqual({ ok: true, value: { type: 'rescued', downloadId: 57, keptBytes: 4096 } });
    expect(client.daemonReady).toBe(true);
    const r = client.resume(57);
    expect(p.last()).toEqual({ type: 'resume', downloadId: 57, id: p.last().id });
    p.reply({ id: p.last().id, ok: true, type: 'resumed', downloadId: '57' });
    await expect(r).resolves.toEqual({ ok: true, value: { type: 'resumed', downloadId: '57' } });
  });

  it('refuses malformed rescued and resumed answers', () => {
    expect(parseResponse({ id: 'x', ok: true, type: 'rescued', downloadId: 1, keptBytes: -1 })?.outcome).toMatchObject({ ok: false, error: { code: 'protocol' } });
    expect(parseResponse({ id: 'x', ok: true, type: 'rescued', downloadId: '', keptBytes: 1 })?.outcome).toMatchObject({ ok: false });
    expect(parseResponse({ id: 'x', ok: true, type: 'resumed' })?.outcome).toMatchObject({ ok: false });
  });
});
