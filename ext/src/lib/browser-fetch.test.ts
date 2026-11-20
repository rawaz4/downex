import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  BrowserFetchPump,
  FETCH_LANES,
  FetchTickets,
  NO_TAB,
  NOT_LISTED,
  PLAYED_PER_TAB,
  PLAYED_STORED_PER_TAB,
  PLAYED_STORED_TABS,
  PLAYED_TTL_MS,
  PlayedLog,
  type FetchClient,
  type SessionArea,
  type TicketRecord,
  hostInScope,
  PLAY_FIRST,
  playedOnly,
} from './browser-fetch';
import { playedResponse } from './played';
import {
  FEATURE_BROWSER_FETCH,
  NativeClient,
  daemonCode,
  type NativePort,
  type VideoInfoResult,
  isFetchTicket,
  parseResponse,
  parseVideoInfo,
} from './protocol';
import { FETCH_PORT, type PortLike, rangeHeader, servePageFetch, toBase64 } from './page-fetch';
import { BROWSER_FETCH_NOTE, canRetryVideo, siteNote, VIDEO_TEXTS, videoFailureText } from './ui-format';

const T1 = '0123456789abcdef0123456789abcdef';
const T2 = 'fedcba9876543210fedcba9876543210';
const PLAYER = 'https://v16.media.example/video/tos/abc123/?sig=browser';
const LISTED = 'https://v19.media.example/video/tos/abc123/?sig=ytdlp';

function memoryArea(delayMs = 0): SessionArea & { data: Record<string, unknown> } {
  const data: Record<string, unknown> = {};
  return {
    data,
    async get(key) {
      if (delayMs > 0) await new Promise((r) => setTimeout(r, delayMs));
      return key in data ? { [key]: structuredClone(data[key]) } : {};
    },
    async set(items) {
      Object.assign(data, structuredClone(items));
    },
    async remove(key) {
      delete data[key];
    },
    async getAll() {
      return structuredClone(data);
    },
  };
}

function record(ticket = T1, over: Partial<TicketRecord> = {}): TicketRecord {
  return {
    ticket,
    downloadId: 7,
    tabId: 3,
    frameId: 0,
    pageUrl: 'https://www.media.example/@u/video/1',
    storeId: '',
    scope: { domain: 'media.example', exact: false },
    at: 1,
    ...over,
  };
}

function mp4Response(url: string, size: number, status = 200, type = 'video/mp4') {
  const headers = [{ name: 'Content-Type', value: type }];
  headers.push(status === 206 ? { name: 'Content-Range', value: `bytes 0-1023/${size}` } : { name: 'Content-Length', value: String(size) });
  return { url, method: 'GET', tabId: 3, statusCode: status, responseHeaders: headers };
}

describe('the media a tab fetched (SITE-MEDIA-SPEC §2.8 "The address", §2.9 PlayedLog)', () => {
  it('takes media answers of every kind, by their whole file', () => {
    expect(playedResponse(mp4Response(PLAYER, 658_136))).toEqual({ key: PLAYER, total: 658_136, kind: 'video', direct: false });
    expect(playedResponse(mp4Response(PLAYER, 9_000, 206))).toEqual({ key: PLAYER, total: 9_000, kind: 'video', direct: false });
    expect(playedResponse(mp4Response(PLAYER, 9_000, 200, 'audio/mp4'))?.kind).toBe('audio');
    expect(playedResponse(mp4Response(PLAYER, 9_000, 200, 'text/html'))).toBeUndefined();
    expect(playedResponse(mp4Response(PLAYER, 9_000, 200, 'video/mp2t'))).toBeUndefined();
    expect(playedResponse(mp4Response('https://cdn.media.example/seg/7.m4s', 9_000))).toBeUndefined();
    expect(playedResponse({ ...mp4Response(PLAYER, 9_000), statusCode: 403 })).toBeUndefined();
    expect(playedResponse({ ...mp4Response(PLAYER, 9_000), incognito: true })).toBeUndefined();
    expect(playedResponse({ ...mp4Response(PLAYER, 9_000), tabId: -1 })).toBeUndefined();
    expect(playedResponse({ ...mp4Response(PLAYER, 9_000), method: 'POST' })).toBeUndefined();
    // No size known: still a file (its head will say), total 0.
    expect(playedResponse({ ...mp4Response(PLAYER, 9_000), responseHeaders: [{ name: 'Content-Type', value: 'video/mp4' }] })).toEqual({
      key: PLAYER,
      total: 0,
      kind: 'video',
      direct: false,
    });
    // An octet stream only with a media file's path.
    expect(playedResponse(mp4Response('https://cdn.media.example/v/a.mp4', 9, 200, 'application/octet-stream'))?.kind).toBe('');
    expect(playedResponse(mp4Response('https://cdn.media.example/v/a', 9, 200, 'application/octet-stream'))).toBeUndefined();
    // A piece whose byte range is in the query: its whole file, total unknown.
    expect(playedResponse(mp4Response('https://cdn.media.example/v/a.mp4?oh=1&bytestart=1000&byteend=1999&x=2', 1000))).toEqual({
      key: 'https://cdn.media.example/v/a.mp4?oh=1&x=2',
      total: 0,
      kind: 'video',
      direct: false,
    });
  });

  it('never records a file of a host Downex does not save from (YouTube, this computer, a local network)', () => {
    for (const url of [
      'https://rr1---sn-abc.googlevideo.com/videoplayback?itag=137',
      'http://127.0.0.1:8080/video.mp4',
      'http://192.168.1.10/cgi-bin/video.mp4',
      'http://localhost/video.mp4',
      'http://[::1]/video.mp4',
    ]) {
      expect(playedResponse(mp4Response(url, 900_000))).toBeUndefined();
    }
  });

  it('keeps responses recorded at once while the tab\'s list is read from storage.session', async () => {
    // The background restarted: the tab's list is not in memory, and reading it takes a while.
    const area = memoryArea(5);
    const played = new PlayedLog(area, { now: () => 1_000 });
    await Promise.all([
      played.record({ ...mp4Response('https://cdn.media.example/v/video.mp4', 900_000), tabId: 52 }),
      played.record({ ...mp4Response('https://cdn.media.example/v/audio.mp4', 90_000, 200, 'audio/mp4'), tabId: 52 }),
    ]);
    expect((await played.resources(52)).map((r) => r.key).sort()).toEqual([
      'https://cdn.media.example/v/audio.mp4',
      'https://cdn.media.example/v/video.mp4',
    ]);
  });

  it('matches the same path and exact size, in storage.session, bounded and forgotten with the tab', async () => {
    let now = 1_000;
    const area = memoryArea();
    const played = new PlayedLog(area, { now: () => now });
    await played.record(mp4Response(PLAYER, 658_136));
    expect(await played.match(3, LISTED, 658_136)).toBe(PLAYER);
    expect(await played.match(3, LISTED, 658_137)).toBeUndefined();
    // The probe's own signed address on another host and path (TikTok's yt-dlp address, refused
    // to the browser): the address the tab's player fetched with that exact size.
    expect(await played.match(3, 'https://v19.media.example/video/tos/other/?sig=x', 658_136)).toBe(PLAYER);
    expect(await played.match(3, 'https://v19.media.example/video/tos/other/?sig=x', 658_135)).toBeUndefined();
    expect(await played.match(3, LISTED, 0)).toBeUndefined();
    expect(await played.match(4, LISTED, 658_136)).toBeUndefined();
    // Bounded per tab in memory, fewer in storage.session.
    for (let i = 0; i < PLAYED_PER_TAB + 5; i++) await played.record(mp4Response(`https://cdn.media.example/f/${i}.mp4`, 100 + i));
    expect((await played.sizes(3)).length).toBe(PLAYED_PER_TAB);
    // Kept apart from the background's memory (a restarted background reads it back).
    await vi.waitFor(() => expect(area.data['played:3']).toBeDefined(), { timeout: 3000 });
    const again = new PlayedLog(area, { now: () => now });
    expect((await again.sizes(3)).length).toBe(PLAYED_STORED_PER_TAB);
    now += PLAYED_TTL_MS;
    expect(await again.sizes(3)).toEqual([]);
    await played.forget(3);
    expect(area.data['played:3']).toBeUndefined();
  });

  it('removes expired addresses from storage.session while the tab stays open', async () => {
    let now = 1_000;
    const area = memoryArea();
    const played = new PlayedLog(area, { now: () => now });
    await played.record(mp4Response(PLAYER, 658_136));
    await vi.waitFor(() => expect(area.data['played:3']).toBeDefined(), { timeout: 3000 });
    now += PLAYED_TTL_MS;
    // The sweep (an alarm in the background) drops what expired, without any new response.
    await played.sweep();
    expect(area.data['played:3']).toBeUndefined();
    // A tab's list that only partly expired keeps the rest.
    await played.record(mp4Response(PLAYER, 1));
    now += PLAYED_TTL_MS / 2;
    const other = LISTED.replace('abc123', 'def456');
    await played.record(mp4Response(other, 2));
    await vi.waitFor(() => expect((area.data['played:3'] as unknown[] | undefined)?.length).toBe(2), { timeout: 3000 });
    now += PLAYED_TTL_MS / 2;
    await played.sweep();
    expect(area.data['played:3']).toEqual([
      { key: other, total: 2, kind: 'video', frameId: 0, firstAt: now - PLAYED_TTL_MS / 2, lastAt: now - PLAYED_TTL_MS / 2, direct: false },
    ]);
    // Reading an expired list from storage (a restarted background) removes it there too.
    now += PLAYED_TTL_MS;
    const again = new PlayedLog(area, { now: () => now });
    expect(await again.sizes(3)).toEqual([]);
    await vi.waitFor(() => expect(area.data['played:3']).toBeUndefined(), { timeout: 3000 });
    // A restarted background's sweep finds the tabs it never read in storage.session.
    area.data['played:5'] = [{ key: PLAYER, total: 9, kind: '', frameId: 0, firstAt: 0, lastAt: now - PLAYED_TTL_MS }];
    area.data['other'] = 1;
    await new PlayedLog(area, { now: () => now }).sweep();
    expect(area.data['played:5']).toBeUndefined();
    expect(area.data['other']).toBe(1);
  });

  it('forgets a closed tab for good, even with a response recorded just before it closed', async () => {
    const area = memoryArea(20);
    const played = new PlayedLog(area);
    const pending = played.record(mp4Response(PLAYER, 658_136));
    await played.forget(3);
    await pending;
    await new Promise((r) => setTimeout(r, 1_200)); // past the 1 s flush
    expect(area.data['played:3']).toBeUndefined();
    expect(await played.sizes(3)).toEqual([]);
  });

  it('keeps every piece of a file as one, with its request times, and never records its own fetches', async () => {
    let now = 1_000;
    const played = new PlayedLog(undefined, { now: () => now });
    const piece = (a: number, b: number) => mp4Response(`https://cdn.media.example/v/a.mp4?bytestart=${a}&byteend=${b}`, b - a + 1);
    await played.record(piece(0, 999));
    now += 500;
    await played.record({ ...piece(1000, 4999), frameId: 2 });
    const list = await played.resources(3);
    expect(list).toEqual([{ key: 'https://cdn.media.example/v/a.mp4', total: 0, kind: 'video', frameId: 2, firstAt: 1_000, lastAt: 1_500, direct: false }]);
    played.ignore('https://cdn.media.example/v/b.mp4');
    await played.record(mp4Response('https://cdn.media.example/v/b.mp4', 70_000));
    expect((await played.resources(3)).length).toBe(1);
    played.unignore('https://cdn.media.example/v/b.mp4');
    await played.record(mp4Response('https://cdn.media.example/v/b.mp4', 70_000));
    expect((await played.resources(3)).length).toBe(2);
  });

  it('keeps at most 16 tabs in storage.session, and only addresses of at most 4 KiB; a full area leaves the log in memory', async () => {
    const area = memoryArea();
    const played = new PlayedLog(area);
    for (let tab = 1; tab <= PLAYED_STORED_TABS + 3; tab++) {
      await played.record({ ...mp4Response(`https://cdn.media.example/v/${tab}.mp4`, 70_000), tabId: tab });
    }
    await vi.waitFor(() => expect(area.data[`played:${PLAYED_STORED_TABS + 3}`]).toBeDefined(), { timeout: 3000 });
    expect(Object.keys(area.data).filter((k) => k.startsWith('played:')).length).toBe(PLAYED_STORED_TABS);
    expect(area.data['played:1']).toBeUndefined();
    // The oldest tab's list stays in memory.
    expect(await played.sizes(1)).toEqual([70_000]);
    const long = `https://cdn.media.example/v/${'x'.repeat(5000)}.mp4`;
    await played.record({ ...mp4Response(long, 80_000), tabId: 40 });
    await new Promise((r) => setTimeout(r, 1_200));
    expect(area.data['played:40']).toBeUndefined();
    expect(await played.sizes(40)).toEqual([80_000]);
    const full: SessionArea = { ...memoryArea(), set: async () => Promise.reject(new Error('QUOTA_BYTES quota exceeded')) };
    const quota = new PlayedLog(full);
    await quota.record(mp4Response(PLAYER, 658_136));
    await new Promise((r) => setTimeout(r, 1_200));
    expect(await quota.sizes(3)).toEqual([658_136]);
  }, 15_000);
});

describe('playedOnly', () => {
  const info = (browserFetch: boolean): VideoInfoResult => ({
    type: 'videoInfo', site: 'TikTok', title: 't', durationMs: 6000, format: 'http', container: 'mp4',
    selectedVideo: 'hevc', selectedAudio: '', browserFetch,
    tracks: [
      { id: 'hevc', kind: 'muxed', ext: 'mp4', codecs: 'hvc1', width: 1080, height: 1920, frameRate: 30, bandwidth: 0, size: 2_000_000, sizeApprox: false, language: '', name: '1080p' },
      { id: 'h264', kind: 'muxed', ext: 'mp4', codecs: 'avc1', width: 576, height: 1024, frameRate: 30, bandwidth: 0, size: 658_136, sizeApprox: false, language: '', name: '540p' },
    ],
  });
  it('lists only the qualities the player fetched on a browser-only page, as the default', () => {
    const one = playedOnly(info(true), [658_136]);
    expect(one.ok && one.value.selectedVideo).toBe('h264');
    expect(one.ok && one.value.tracks.map((t) => t.id)).toEqual(['h264']);
    const both = playedOnly(info(true), [2_000_000, 658_136]);
    expect(both.ok && both.value.tracks.map((t) => t.id)).toEqual(['hevc', 'h264']);
    expect(both.ok && both.value.selectedVideo).toBe('hevc');
    // Not a browser-only page: the answer as it is.
    expect(playedOnly(info(false), [658_136])).toEqual({ ok: true, value: info(false) });
  });
  it('lists no quality that would fail: play it first when the player fetched none', () => {
    const none = playedOnly(info(true), [1]);
    expect(none.ok).toBe(false);
    if (!none.ok) {
      expect(daemonCode(none.error)).toBe(PLAY_FIRST);
      expect(videoFailureText(none.error)).toBe(VIDEO_TEXTS.play_first);
      expect(canRetryVideo(none.error)).toBe(true);
    }
    expect(playedOnly(info(true), []).ok).toBe(false);
  });
});

describe('tickets', () => {
  it('keeps at most 64, the newest, and forgets ended ones', async () => {
    const tickets = new FetchTickets(memoryArea());
    for (let i = 0; i < 70; i++) await tickets.add(record(i.toString(16).padStart(32, '0')));
    const list = await tickets.list();
    expect(list.length).toBe(64);
    expect(list[0]?.ticket).toBe((6).toString(16).padStart(32, '0'));
    await tickets.remove([list[0]!.ticket]);
    expect((await tickets.list()).length).toBe(63);
  });
  it('scopes', () => {
    expect(hostInScope('v16-webapp.media.example', { domain: 'media.example', exact: false })).toBe(true);
    expect(hostInScope('evilmedia.example', { domain: 'media.example', exact: false })).toBe(false);
    expect(hostInScope('cdn.page.example', { domain: 'page.example', exact: true })).toBe(false);
    expect(hostInScope('page.example', { domain: 'page.example', exact: true })).toBe(true);
  });
});

/** A content-script port pair: the background's end (PortLike) and the content script's. */
function portPair() {
  type L = (m: unknown) => void;
  const a = { msg: [] as L[], dis: [] as (() => void)[] };
  const b = { msg: [] as L[], dis: [] as (() => void)[] };
  let open = true;
  const end = (mine: typeof a, other: typeof a): PortLike => ({
    name: FETCH_PORT,
    sender: { id: 'ext' },
    postMessage: (m) => {
      if (!open) throw new Error('disconnected');
      const copy = JSON.parse(JSON.stringify(m)) as unknown;
      queueMicrotask(() => other.msg.forEach((l) => l(copy)));
    },
    disconnect: () => {
      if (!open) return;
      open = false;
      queueMicrotask(() => other.dis.forEach((l) => l()));
    },
    onMessage: { addListener: (l) => mine.msg.push(l) },
    onDisconnect: { addListener: (l) => mine.dis.push(l) },
  });
  return { bg: end(a, b), page: end(b, a), isOpen: () => open };
}

/** A fetch answering `body` with headers, its stream in chunks of 100,000 bytes. */
function fakeFetch(body: Uint8Array, init: { status?: number; headers?: Record<string, string> } = {}) {
  const calls: { url: string; init: RequestInit }[] = [];
  let pulled = 0;
  const fn = vi.fn(async (url: string, ri: RequestInit) => {
    calls.push({ url, init: ri });
    const stream = new ReadableStream<Uint8Array>({
      pull(c) {
        if (ri.signal?.aborted) return c.error(new Error('aborted'));
        if (pulled >= body.length) return c.close();
        c.enqueue(body.subarray(pulled, pulled + 100_000));
        pulled += 100_000;
      },
    });
    return new Response(stream, { status: init.status ?? 206, headers: init.headers ?? { 'Content-Type': 'video/mp4', 'Content-Length': String(body.length) } });
  });
  return { fn: fn as unknown as typeof fetch, calls, pulled: () => pulled };
}

describe('the fetch in the page (page-fetch.ts)', () => {
  it('makes the request with Range only and sends the head, then pieces each after an ack', async () => {
    const body = new Uint8Array(600_000).map((_, i) => i % 251);
    const f = fakeFetch(body);
    const { bg, page } = portPair();
    servePageFetch(page, f.fn, 256 * 1024);
    const got: Record<string, unknown>[] = [];
    bg.onMessage.addListener((m) => {
      got.push(m as Record<string, unknown>);
      const r = m as { type: string; end?: boolean };
      if (r.type === 'head' || (r.type === 'data' && !r.end)) setTimeout(() => bg.postMessage({ type: 'more' }), 5);
    });
    bg.postMessage({ type: 'start', url: PLAYER, start: 0, end: 599_999, credentials: 'omit' });
    await vi.waitFor(() => expect(got.at(-1)).toMatchObject({ type: 'data', end: true }), { timeout: 3000 });
    expect(f.calls[0]?.init.headers).toEqual({ Range: 'bytes=0-599999' });
    expect(f.calls[0]?.init.credentials).toBe('omit');
    expect(got[0]).toEqual({ type: 'head', status: 206, contentType: 'video/mp4', contentRange: '', contentLength: 600_000 });
    const pieces = got.slice(1).map((m) => Uint8Array.from(atob(m.data as string), (c) => c.charCodeAt(0)));
    expect(pieces.every((p) => p.length <= 256 * 1024)).toBe(true);
    const all = new Uint8Array(pieces.reduce((n, p) => n + p.length, 0));
    pieces.reduce((off, p) => (all.set(p, off), off + p.length), 0);
    expect(all).toEqual(body);
  });

  it('reads no further without an ack, and stops when the port closes', async () => {
    const body = new Uint8Array(2_000_000);
    const f = fakeFetch(body);
    const { bg, page } = portPair();
    servePageFetch(page, f.fn, 256 * 1024);
    const got: unknown[] = [];
    bg.onMessage.addListener((m) => got.push(m));
    bg.postMessage({ type: 'start', url: PLAYER, start: 0, end: -1, credentials: 'include' });
    await vi.waitFor(() => expect(got.length).toBe(1));
    await new Promise((r) => setTimeout(r, 30));
    expect(f.pulled()).toBeLessThanOrEqual(100_000); // the stream's own read-ahead, no more
    bg.postMessage({ type: 'more' });
    await vi.waitFor(() => expect(got.length).toBe(2));
    await new Promise((r) => setTimeout(r, 30));
    expect(f.pulled()).toBeLessThan(500_000);
    bg.disconnect();
    await new Promise((r) => setTimeout(r, 30));
    expect(f.pulled()).toBeLessThan(500_000);
    expect(f.calls[0]?.init.headers).toEqual({ Range: 'bytes=0-' });
  });

  it('reports a failed fetch, and ignores a malformed start', async () => {
    const { bg, page } = portPair();
    servePageFetch(page, (async () => {
      throw new TypeError('Failed to fetch');
    }) as unknown as typeof fetch);
    const got: unknown[] = [];
    bg.onMessage.addListener((m) => got.push(m));
    bg.postMessage({ type: 'start', url: 'javascript:alert(1)', start: 0, end: -1, credentials: 'include' });
    await new Promise((r) => setTimeout(r, 10));
    expect(got).toEqual([]);
    bg.postMessage({ type: 'start', url: PLAYER, start: 0, end: -1, credentials: 'include' });
    await vi.waitFor(() => expect(got).toEqual([{ type: 'error', error: 'Failed to fetch' }]));
  });

  it('encodes and ranges', () => {
    expect(toBase64(new Uint8Array([0, 255, 1]))).toBe('AP8B');
    expect(rangeHeader(5, -1)).toBe('bytes=5-');
    expect(rangeHeader(5, 9)).toBe('bytes=5-9');
  });
});

describe('the pump', () => {
  beforeEach(() => vi.useRealTimers());
  afterEach(() => vi.useRealTimers());

  function pumpWith(body: Uint8Array, opts: { frame?: boolean; signIn?: boolean; stopAt?: number } = {}) {
    const tickets = new FetchTickets(memoryArea());
    const played = new PlayedLog(memoryArea());
    const puts: Record<string, unknown>[] = [];
    let takes = 0;
    const task = { ticket: T1, fetchId: '9', url: LISTED, start: 0, end: body.length - 1, total: 658_136 };
    const client: FetchClient = {
      fetchTake: vi.fn(async (ts: string[]) => {
        takes++;
        if (takes === 1) return { ok: true as const, value: { type: 'fetchTask' as const, fetch: task, ended: ts.filter((t) => t === T2) } };
        await new Promise((r) => setTimeout(r, 20));
        return { ok: true as const, value: { type: 'fetchTask' as const, ended: ts } };
      }),
      fetchPut: vi.fn(async (fields) => {
        puts.push(fields as unknown as Record<string, unknown>);
        return { ok: true as const, value: { type: 'fetchAck' as const, stop: opts.stopAt !== undefined && puts.length >= opts.stopAt } };
      }),
    };
    const f = fakeFetch(body);
    const pairs: ReturnType<typeof portPair>[] = [];
    const pump = new BrowserFetchPump({
      client,
      tickets,
      played,
      frameFor: async () => (opts.frame === false ? undefined : { tabId: 3, frameId: 0 }),
      connect: () => {
        const p = portPair();
        pairs.push(p);
        servePageFetch(p.page, f.fn, 256 * 1024);
        return p.bg;
      },
      signInOn: async () => opts.signIn ?? true,
    });
    return { pump, tickets, played, puts, f, pairs, client };
  }

  it('fetches at the player own address with the site credentials and puts head then pieces in order', async () => {
    const body = new Uint8Array(400_000).fill(7);
    const t = pumpWith(body);
    await t.played.record(mp4Response(PLAYER, 658_136));
    await t.pump.bind(record(T1, { signedIn: true }));
    await t.tickets.add(record(T2));
    await vi.waitFor(() => expect(t.puts.at(-1)).toMatchObject({ end: true }), { timeout: 3000 });
    expect(t.f.calls[0]?.url).toBe(PLAYER);
    expect(t.f.calls[0]?.init.credentials).toBe('include');
    expect(t.puts.map((p) => p.seq)).toEqual([0, 1, 2]);
    expect(t.puts[0]).toEqual({ ticket: T1, fetchId: '9', seq: 0, head: { status: 206, contentType: 'video/mp4', contentLength: 400_000 } });
    // Ended tickets are forgotten; the pump stops with none left.
    await vi.waitFor(async () => expect(await t.tickets.list()).toEqual([]), { timeout: 3000 });
  });

  it('omits credentials off the site or with the sign-in switch off, and uses the listed address without a match', async () => {
    const t = pumpWith(new Uint8Array(10), { signIn: false });
    await t.pump.bind(record(T1, { signedIn: true }));
    await vi.waitFor(() => expect(t.puts.at(-1)).toMatchObject({ end: true }), { timeout: 3000 });
    expect(t.f.calls[0]?.url).toBe(LISTED);
    expect(t.f.calls[0]?.init.credentials).toBe('omit');
  });

  it('omits credentials for a video whose add never needed the sign-in, with the switch on', async () => {
    const t = pumpWith(new Uint8Array(10), { signIn: true });
    await t.played.record(mp4Response(PLAYER, 658_136));
    await t.pump.bind(record(T1));
    await vi.waitFor(() => expect(t.puts.at(-1)).toMatchObject({ end: true }), { timeout: 3000 });
    expect(t.f.calls[0]?.url).toBe(PLAYER);
    expect(t.f.calls[0]?.init.credentials).toBe('omit');
  });

  it('puts no_tab when no tab of the site is open', async () => {
    const t = pumpWith(new Uint8Array(10), { frame: false });
    await t.pump.bind(record(T1));
    await vi.waitFor(() => expect(t.puts[0]).toEqual({ ticket: T1, fetchId: '9', seq: 0, error: NO_TAB }), { timeout: 3000 });
  });

  it('aborts the page fetch when Downex says stop', async () => {
    const t = pumpWith(new Uint8Array(3_000_000), { stopAt: 2 });
    await t.pump.bind(record(T1));
    await vi.waitFor(() => expect(t.puts.length).toBe(2), { timeout: 3000 });
    await vi.waitFor(() => expect(t.pairs[0]?.isOpen()).toBe(false));
    await new Promise((r) => setTimeout(r, 30));
    expect(t.puts.length).toBe(2);
    expect(t.f.pulled()).toBeLessThan(3_000_000);
  });

  it('runs at most two transfers', () => {
    expect(FETCH_LANES).toBe(2);
  });

  it('a played ticket fetches only the files handed over, with credentials only where their head needed them (§2.9)', async () => {
    const t = pumpWith(new Uint8Array(10));
    await t.pump.bind(record(T1, { played: { urls: [LISTED], include: [LISTED] } }));
    await vi.waitFor(() => expect(t.puts.at(-1)).toMatchObject({ end: true }), { timeout: 3000 });
    // The address as given (never another the tab played) and the page's credentials.
    expect(t.f.calls[0]?.url).toBe(LISTED);
    expect(t.f.calls[0]?.init.credentials).toBe('include');
    // Its own request was not recorded as played.
    expect(await t.played.resources(3)).toEqual([]);

    const u = pumpWith(new Uint8Array(10));
    await u.pump.bind(record(T1, { played: { urls: [LISTED], include: [] } }));
    await vi.waitFor(() => expect(u.puts.at(-1)).toMatchObject({ end: true }), { timeout: 3000 });
    expect(u.f.calls[0]?.init.credentials).toBe('omit');

    const w = pumpWith(new Uint8Array(10));
    await w.pump.bind(record(T1, { played: { urls: [PLAYER], include: [PLAYER] } }));
    await vi.waitFor(() => expect(w.puts[0]).toEqual({ ticket: T1, fetchId: '9', seq: 0, error: NOT_LISTED }), { timeout: 3000 });
    expect(w.f.calls).toEqual([]);
  });
});

describe('protocol (SITE-MEDIA-SPEC §2.8)', () => {
  it('reads fetchTask, fetchAck, added.fetchTicket and videoInfo.browserFetch', () => {
    expect(parseResponse({ id: 'x', ok: true, type: 'fetchTask', ended: [T2], fetch: { ticket: T1, fetchId: '1', url: LISTED, start: 0, end: -1, total: 0 } })?.outcome).toEqual({
      ok: true,
      value: { type: 'fetchTask', ended: [T2], fetch: { ticket: T1, fetchId: '1', url: LISTED, start: 0, end: -1, total: 0 } },
    });
    for (const bad of [
      { ended: ['nope'] },
      { ended: [], fetch: { ticket: T1, fetchId: '1', url: 'file:///x', start: 0, end: -1, total: 0 } },
      { ended: [], fetch: { ticket: T1, fetchId: '1', url: LISTED, start: -1, end: -1, total: 0 } },
      { ended: [], fetch: { ticket: 'ABC', fetchId: '1', url: LISTED, start: 0, end: -1, total: 0 } },
    ]) {
      expect(parseResponse({ id: 'x', ok: true, type: 'fetchTask', ...bad })?.outcome).toMatchObject({ ok: false, error: { code: 'protocol' } });
    }
    expect(parseResponse({ id: 'x', ok: true, type: 'fetchAck', stop: true })?.outcome).toEqual({ ok: true, value: { type: 'fetchAck', stop: true } });
    expect(parseResponse({ id: 'x', ok: true, type: 'added', downloadId: 1, fetchTicket: T1 })?.outcome).toMatchObject({ ok: true, value: { fetchTicket: T1 } });
    expect(parseResponse({ id: 'x', ok: true, type: 'added', downloadId: 1, fetchTicket: 'x' })?.outcome).toMatchObject({ ok: false });
    const vi1 = parseVideoInfo({ site: 'TikTok', title: '', durationMs: 0, format: 'http', container: 'mp4', selectedVideo: '', selectedAudio: '', tracks: [], browserFetch: true });
    expect(vi1?.browserFetch).toBe(true);
    expect(isFetchTicket(T1)).toBe(true);
    expect(isFetchTicket(T1.toUpperCase())).toBe(false);
  });

  it('sends fetchTake, fetchPut and browserFetch only to a relay that lists the feature', async () => {
    vi.useFakeTimers();
    const sent: Record<string, unknown>[][] = [];
    const ports: { reply: (m: unknown) => void }[] = [];
    const mk = (): NativePort => {
      const msgs: Record<string, unknown>[] = [];
      sent.push(msgs);
      const listeners: ((m: unknown) => void)[] = [];
      ports.push({ reply: (m) => listeners.forEach((l) => l(m)) });
      return {
        postMessage: (m: unknown) => msgs.push(JSON.parse(JSON.stringify(m)) as Record<string, unknown>),
        disconnect: () => undefined,
        onMessage: { addListener: (l: (m: unknown) => void) => listeners.push(l), removeListener: () => undefined },
        onDisconnect: { addListener: () => undefined, removeListener: () => undefined },
      } as unknown as NativePort;
    };
    for (const features of [['video', 'signIn'], ['video', 'signIn', FEATURE_BROWSER_FETCH]]) {
      let n = 0;
      const client = new NativeClient({ connect: mk, hello: () => ({ protocol: 1, browser: 'chrome', extVersion: 't' }), newId: () => `r${++n}` });
      client.start();
      const i = sent.length - 1;
      const take = client.fetchTake([T1], 0);
      const add = client.addVideo({ pageUrl: 'https://www.media.example/v/1', browserFetch: true });
      ports[i]!.reply({ id: 'r1', ok: true, type: 'welcome', protocol: 1, daemonVersion: '1', features });
      const types = sent[i]!.map((m) => m.type);
      if (!features.includes(FEATURE_BROWSER_FETCH)) {
        await expect(take).resolves.toMatchObject({ ok: false, error: { code: 'outdated_downex' } });
        expect(types).toEqual(['hello', 'addVideo']);
        expect(sent[i]![1]).not.toHaveProperty('browserFetch');
      } else {
        expect(types).toEqual(['hello', 'fetchTake', 'addVideo']);
        expect(sent[i]![2]).toMatchObject({ browserFetch: true });
      }
      void add;
      client.stop();
    }
    vi.useRealTimers();
  });
});

describe('panel note', () => {
  it('says the browser fetches the video after the MP4 note', () => {
    expect(siteNote({ browserFetch: true }, 'MP4.')).toBe(`MP4. ${BROWSER_FETCH_NOTE}`);
    expect(siteNote({ signedIn: 'media.example' }, 'MP4.')).toBe('Used your sign-in for media.example. MP4.');
    expect(siteNote(undefined, 'MP4.')).toBe('MP4.');
  });
});
