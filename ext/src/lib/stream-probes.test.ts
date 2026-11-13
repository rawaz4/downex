import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { MediaItem } from './media';
import { MEDIA_PREPARE_TIMEOUT_MS } from './media-handoff';
import { failure, type Outcome, type ProbeStreamFields, type RequestOptions, type VideoInfoResult } from './protocol';
import {
  STREAM_CACHE_SIZE,
  STREAM_KEEP_MS,
  STREAM_PROBE_CONCURRENCY,
  STREAM_PROBE_DEADLINE_MS,
  createStreamProber,
  keepStreamProbe,
  streamProbeKey,
  streamRefusal,
} from './stream-probes';

const item = (url = 'https://cdn.example.com/v/master.m3u8', over: Partial<MediaItem> = {}): MediaItem => ({
  url,
  format: 'hls',
  firstSeen: 1,
  lastSeen: 1,
  frameId: 0,
  parentFrameId: -1,
  size: 0,
  reqType: 'xmlhttprequest',
  short: false,
  segment: false,
  seq: 1,
  pageUrl: 'https://site.example/watch/1',
  headers: [
    ['Cookie', 'sid=1'],
    ['Referer', 'https://site.example/watch/1'],
  ],
  ...over,
});

const info = (): VideoInfoResult => ({
  type: 'videoInfo',
  site: '',
  title: '',
  durationMs: 60_000,
  format: 'hls',
  container: 'mp4',
  selectedVideo: 'v1',
  selectedAudio: '',
  tracks: [],
});

const refused = (reason: string) => failure('rejected', `${reason}: x`, reason);

function deferred<T>() {
  let resolve!: (v: T) => void;
  const promise = new Promise<T>((r) => (resolve = r));
  return { promise, resolve };
}

beforeEach(() => {
  vi.useFakeTimers();
});
afterEach(() => {
  vi.useRealTimers();
});

function setup(answer?: (f: ProbeStreamFields) => Promise<Outcome<VideoInfoResult>>) {
  let now = 0;
  const sent: { fields: ProbeStreamFields; opts: RequestOptions<VideoInfoResult> | undefined }[] = [];
  const getCookies = vi.fn(async () => [{ name: 'jar', value: '1' }]);
  const prober = createStreamProber({
    getCookies,
    userAgent: 'UA/1',
    now: () => now,
    client: {
      probeStream: (fields, opts) => {
        sent.push({ fields, opts });
        return answer ? answer(fields) : Promise.resolve({ ok: true, value: info() });
      },
    },
  });
  return { prober, sent, getCookies, advance: (ms: number) => (now += ms) };
}

describe('stream refusals', () => {
  it('keeps answers and refusals of the stream, never failures that may pass', () => {
    expect(keepStreamProbe({ ok: true, value: info() })).toBe(true);
    for (const r of ['encrypted', 'live', 'media_unsupported', 'media_invalid']) {
      expect(keepStreamProbe(refused(r))).toBe(true);
      expect(streamRefusal(refused(r))).toBe(r);
    }
    // An older relay without `reason`: the message names the code.
    expect(streamRefusal(failure('rejected', 'live: no EXT-X-ENDLIST'))).toBe('live');
    for (const o of [
      refused('http_status'),
      refused('network'),
      failure('timeout', 'no probeStream response'),
      failure('outdated_downex', 'Update Downex to choose a stream quality.'),
      failure('daemon_unavailable', 'x'),
    ]) {
      expect(keepStreamProbe(o)).toBe(false);
      expect(streamRefusal(o)).toBeUndefined();
    }
    expect(streamProbeKey({ url: 'https://a/1.m3u8' })).toBe('\nhttps://a/1.m3u8');
    expect(streamProbeKey({ url: 'https://a/1.m3u8', storeId: 'c2' })).toBe('c2\nhttps://a/1.m3u8');
  });
});

describe('createStreamProber', () => {
  it('probes with the stream hand-off headers and the deadline, once per stream', async () => {
    const { prober, sent, getCookies } = setup();
    const a = prober.probe(item());
    const b = prober.probe(item());
    await expect(a).resolves.toMatchObject({ ok: true, value: { container: 'mp4' } });
    await expect(b).resolves.toMatchObject({ ok: true });
    expect(sent).toHaveLength(1);
    expect(sent[0]?.fields).toEqual({
      manifestUrl: 'https://cdn.example.com/v/master.m3u8',
      headers: [
        ['Cookie', 'sid=1'],
        ['Referer', 'https://site.example/watch/1'],
        ['User-Agent', 'UA/1'],
      ],
      pageUrl: 'https://site.example/watch/1',
      deadlineMs: STREAM_PROBE_DEADLINE_MS,
    });
    expect(sent[0]?.opts?.timeoutMs).toBe(STREAM_PROBE_DEADLINE_MS);
    expect(getCookies).not.toHaveBeenCalled();
    expect(prober.state(item())).toMatchObject({ result: { ok: true } });
    // Without recorded headers the M5 rule reads the manifest URL's cookies.
    await prober.probe(item('https://cdn.example.com/other.mpd', { headers: undefined, format: 'dash' }));
    expect(getCookies).toHaveBeenCalledWith('https://cdn.example.com/other.mpd', undefined);
    expect(sent[1]?.fields.headers).toContainEqual(['Cookie', 'jar=1']);
  });

  it(`sends at most ${STREAM_PROBE_CONCURRENCY} at once`, async () => {
    const held: ReturnType<typeof deferred<Outcome<VideoInfoResult>>>[] = [];
    const { prober, sent } = setup(() => {
      const d = deferred<Outcome<VideoInfoResult>>();
      held.push(d);
      return d.promise;
    });
    const all = [1, 2, 3, 4].map((i) => prober.probe(item(`https://cdn.example.com/${i}.m3u8`)));
    await vi.advanceTimersByTimeAsync(0);
    expect(sent).toHaveLength(STREAM_PROBE_CONCURRENCY);
    expect(prober.state(item('https://cdn.example.com/3.m3u8'))).toEqual({ running: true });
    held[0]?.resolve({ ok: true, value: info() });
    await vi.advanceTimersByTimeAsync(0);
    expect(sent).toHaveLength(3);
    for (const d of held.slice(1)) d.resolve({ ok: true, value: info() });
    await vi.advanceTimersByTimeAsync(0);
    held.at(-1)?.resolve({ ok: true, value: info() });
    await Promise.all(all);
    expect(sent).toHaveLength(4);
  });

  it('keeps refusals for 10 minutes, and asks again after other failures', async () => {
    let answer: Outcome<VideoInfoResult> = refused('encrypted');
    const { prober, sent, advance } = setup(async () => answer);
    await prober.probe(item());
    await prober.probe(item());
    expect(sent).toHaveLength(1);
    expect(prober.refused(item())).toBe(true);
    // Another container of the same manifest is another stream.
    expect(prober.refused(item(undefined, { storeId: 'c2' }))).toBe(false);
    advance(STREAM_KEEP_MS);
    expect(prober.refused(item())).toBe(false);
    answer = refused('network');
    await prober.probe(item());
    await prober.probe(item());
    expect(sent).toHaveLength(3);
    expect(prober.state(item())).toBeUndefined();
    expect(prober.refused(item())).toBe(false);
  });

  it(`keeps at most ${STREAM_CACHE_SIZE} streams`, async () => {
    const { prober } = setup();
    for (let i = 0; i <= STREAM_CACHE_SIZE; i++) await prober.probe(item(`https://cdn.example.com/${i}.m3u8`));
    expect(prober.state(item('https://cdn.example.com/0.m3u8'))).toBeUndefined();
    expect(prober.state(item(`https://cdn.example.com/${STREAM_CACHE_SIZE}.m3u8`))).toBeDefined();
  });

  it('gives up unsent when the cookie lookup hangs, and leaves out what cannot be sent', async () => {
    const sent: unknown[] = [];
    const prober = createStreamProber({
      getCookies: () => new Promise(() => undefined),
      userAgent: 'UA',
      client: { probeStream: async (f) => (sent.push(f), { ok: true, value: info() }) },
    });
    const r = prober.probe(item(undefined, { headers: undefined }));
    await vi.advanceTimersByTimeAsync(MEDIA_PREPARE_TIMEOUT_MS);
    await expect(r).resolves.toMatchObject({ ok: false, error: { code: 'timeout' } });
    await expect(prober.probe(item('blob:https://site.example/1'))).resolves.toMatchObject({ ok: false, error: { code: 'invalid_request' } });
    expect(sent).toHaveLength(0);
  });
});
