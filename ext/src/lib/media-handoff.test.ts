import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { HandoffHistory, type StorageAreaLike } from './history';
import type { MediaItem } from './media';
import {
  MEDIA_ADD_TIMEOUT_MS,
  MEDIA_PREPARE_TIMEOUT_MS,
  MEDIA_RESULT_KEEP_MS,
  MediaSends,
  buildAddMediaFields,
  cleanTitle,
  createMediaHandOff,
} from './media-handoff';
import type { AddedResult, AddMediaFields, Outcome, RequestOptions } from './protocol';

function memoryArea(): StorageAreaLike & { data: Record<string, unknown> } {
  const data: Record<string, unknown> = {};
  return {
    data,
    get: async (key) => (key in data ? { [key]: data[key] } : {}),
    set: async (items) => void Object.assign(data, items),
  };
}

const item = (over: Partial<MediaItem> = {}): MediaItem => ({
  url: 'https://cdn.example.com/v/1/master.m3u8',
  format: 'hls',
  firstSeen: 1,
  lastSeen: 2,
  pageUrl: 'https://site.example/watch?v=1#t=3',
  pageTitle: 'My video',
  frameId: 0,
  parentFrameId: -1,
  size: 0,
  reqType: 'xmlhttprequest',
  short: false,
  segment: false,
  seq: 1,
  ...over,
});

const deps = (cookies: { name: string; value: string }[] = []) => ({
  getCookies: vi.fn(async () => cookies),
  userAgent: 'TestUA/1',
});

describe('cleanTitle', () => {
  it('removes controls, collapses whitespace and trims', () => {
    expect(cleanTitle('  A\u0000\tvideo \n\u0085 title ')).toBe('A video title');
    expect(cleanTitle(' \u0007 ')).toBeUndefined();
    expect(cleanTitle(undefined)).toBeUndefined();
  });

  it('cuts at 512 UTF-8 bytes on a code-point boundary', () => {
    expect(cleanTitle('a'.repeat(600))).toBe('a'.repeat(512));
    const emoji = '\u{1F3AC}'.repeat(200); // 4 bytes each
    const cut = cleanTitle(emoji) ?? '';
    expect([...cut]).toHaveLength(128);
    expect(new TextEncoder().encode(cut).length).toBe(512);
    expect(cut.endsWith('\u{1F3AC}')).toBe(true);
  });
});

describe('buildAddMediaFields', () => {
  it('replays the headers the page sent, filtered, adding only a missing User-Agent', async () => {
    const d = deps([{ name: 'ignored', value: '1' }]);
    const r = await buildAddMediaFields(
      item({
        headers: [
          ['Cookie', 'sid=1'],
          ['Referer', 'https://site.example/'],
          ['Range', 'bytes=0-'],
          ['Origin', 'https://site.example'],
        ],
      }),
      d,
    );
    expect(r).toEqual({
      ok: true,
      value: {
        manifestUrl: 'https://cdn.example.com/v/1/master.m3u8',
        headers: [
          ['Cookie', 'sid=1'],
          ['Referer', 'https://site.example/'],
          ['User-Agent', 'TestUA/1'],
          ['Origin', 'https://site.example'],
        ],
        pageUrl: 'https://site.example/watch?v=1#t=3',
        title: 'My video',
      },
    });
    expect(d.getCookies).not.toHaveBeenCalled();
  });

  it('keeps a recorded request without cookies as it was', async () => {
    const d = deps([{ name: 'sid', value: '1' }]);
    const r = await buildAddMediaFields(item({ headers: [['User-Agent', 'PageUA']] }), d);
    expect(r.ok && r.value.headers).toEqual([['User-Agent', 'PageUA']]);
    expect(d.getCookies).not.toHaveBeenCalled();
  });

  it('without recorded headers: the manifest URL cookies of its store and a default-policy Referer', async () => {
    const d = deps([
      { name: 'a', value: '1' },
      { name: 'b', value: '2' },
    ]);
    const r = await buildAddMediaFields(item({ storeId: 'firefox-container-2', frameUrl: 'https://player.example.net/e/9' }), d);
    expect(d.getCookies).toHaveBeenCalledWith('https://cdn.example.com/v/1/master.m3u8', 'firefox-container-2');
    expect(r.ok && r.value.headers).toEqual([
      ['Cookie', 'a=1; b=2'],
      ['Referer', 'https://player.example.net/'],
      ['User-Agent', 'TestUA/1'],
    ]);
    // Same origin: the full page URL without fragment.
    const same = await buildAddMediaFields(
      item({ url: 'https://site.example/hls/a.m3u8', pageUrl: 'https://site.example/watch?v=1#t' }),
      deps(),
    );
    expect(same.ok && same.value.headers).toContainEqual(['Referer', 'https://site.example/watch?v=1']);
  });

  it('hands off without cookies when they cannot be read, and leaves out unusable page fields', async () => {
    const r = await buildAddMediaFields(item({ pageUrl: 'about:blank', pageTitle: ' \u0000 ' }), {
      getCookies: async () => {
        throw new Error('no permission');
      },
      userAgent: '',
    });
    expect(r).toEqual({ ok: true, value: { manifestUrl: 'https://cdn.example.com/v/1/master.m3u8', headers: [] } });
    const bad = await buildAddMediaFields(item({ url: 'blob:https://site.example/1' }), deps());
    expect(bad).toMatchObject({ ok: false, error: { code: 'invalid_request' } });
  });
});

describe('createMediaHandOff', () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  function setup(answer: (f: AddMediaFields, o?: RequestOptions<AddedResult>) => Promise<Outcome<AddedResult>>) {
    const area = memoryArea();
    const history = new HandoffHistory(area);
    const sent: { fields: AddMediaFields; opts: RequestOptions<AddedResult> | undefined }[] = [];
    const handOff = createMediaHandOff({
      ...deps(),
      history,
      now: () => 42,
      client: {
        addMedia: (fields, opts) => {
          sent.push({ fields, opts });
          return answer(fields, opts);
        },
      },
    });
    return { handOff, history, sent };
  }

  it('sends addMedia with its deadline and records the result in the history', async () => {
    const { handOff, history, sent } = setup(async () => ({ ok: true, value: { type: 'added', downloadId: 9 } }));
    const r = await handOff(item());
    expect(r).toEqual({ ok: true, value: { type: 'added', downloadId: 9 } });
    expect(sent[0]?.fields).toMatchObject({
      manifestUrl: 'https://cdn.example.com/v/1/master.m3u8',
      title: 'My video',
      deadlineMs: MEDIA_ADD_TIMEOUT_MS,
    });
    expect(sent[0]?.opts?.timeoutMs).toBe(MEDIA_ADD_TIMEOUT_MS);
    await vi.waitFor(async () => expect(await history.list()).toHaveLength(1));
    expect((await history.list())[0]).toEqual({
      ts: 42,
      url: 'https://cdn.example.com/v/1/master.m3u8',
      source: 'media',
      ok: true,
      title: 'My video',
      downloadId: 9,
    });
  });

  it('sends the chosen tracks, and records whether Downex waits for a confirmation', async () => {
    const { handOff, history, sent } = setup(async () => ({ ok: true, value: { type: 'added', downloadId: 9, awaiting: true } }));
    await handOff(item(), { videoTrack: 'v720', audioTrack: 'a-en' });
    expect(sent[0]?.fields).toMatchObject({ videoTrack: 'v720', audioTrack: 'a-en' });
    await handOff(item(), { videoTrack: '', audioTrack: '' });
    await handOff(item());
    for (const s of sent.slice(1)) {
      expect(s.fields).not.toHaveProperty('videoTrack');
      expect(s.fields).not.toHaveProperty('audioTrack');
    }
    await vi.waitFor(async () => expect(await history.list()).toHaveLength(3));
    expect((await history.list())[0]).toMatchObject({ source: 'media', ok: true, awaiting: true });
  });

  it('records refusals and a late added', async () => {
    let late: ((o: Outcome<AddedResult>) => void) | undefined;
    const { handOff, history } = setup(async (_f, o) => {
      late = o?.onLate;
      return { ok: false, error: { code: 'rejected', message: 'encrypted' } };
    });
    const r = await handOff(item({ pageTitle: undefined }));
    expect(r).toMatchObject({ ok: false, error: { code: 'rejected', message: 'encrypted' } });
    late?.({ ok: true, value: { type: 'added', downloadId: 3 } });
    late?.({ ok: false, error: { code: 'internal', message: 'x' } });
    await vi.waitFor(async () => expect(await history.list()).toHaveLength(2));
    const [newest, first] = await history.list();
    expect(first).toMatchObject({ ok: false, source: 'media', error: { message: 'encrypted' } });
    expect(first?.title).toBeUndefined();
    expect(newest).toMatchObject({ ok: true, downloadId: 3 });
  });

  it('gives up unsent when the cookie lookup hangs', async () => {
    const area = memoryArea();
    const addMedia = vi.fn();
    const handOff = createMediaHandOff({
      getCookies: () => new Promise(() => undefined),
      userAgent: 'UA',
      history: new HandoffHistory(area),
      client: { addMedia },
    });
    const r = handOff(item());
    await vi.advanceTimersByTimeAsync(MEDIA_PREPARE_TIMEOUT_MS);
    await expect(r).resolves.toMatchObject({ ok: false, error: { code: 'timeout' } });
    expect(addMedia).not.toHaveBeenCalled();
  });

  it('never rejects', async () => {
    const { handOff } = setup(async () => {
      throw new Error('boom');
    });
    await expect(handOff(item())).resolves.toMatchObject({ ok: false, error: { code: 'internal', message: 'boom' } });
  });
});

describe('MediaSends', () => {
  const added: Outcome<AddedResult> = { ok: true, value: { type: 'added', downloadId: 5 } };

  it('runs one hand-off per tab and stream at a time, and keeps its answer for a while', async () => {
    let t = 1000;
    const sends = new MediaSends({ now: () => t });
    let answer: (o: Outcome<AddedResult>) => void = () => undefined;
    const start = vi.fn(() => new Promise<Outcome<AddedResult>>((r) => (answer = r)));
    expect(sends.state(1, 'u')).toEqual({});
    await expect(sends.follow(1, 'u')).resolves.toBeUndefined();
    const a = sends.run(1, 'u', start);
    const b = sends.run(1, 'u', start);
    const otherTab = vi.fn(async () => added);
    await expect(sends.run(2, 'u', otherTab)).resolves.toEqual(added);
    expect(start).toHaveBeenCalledTimes(1);
    expect(otherTab).toHaveBeenCalledTimes(1);
    expect(sends.state(1, 'u')).toEqual({ sending: true });
    const followed = sends.follow(1, 'u');
    answer(added);
    await expect(a).resolves.toEqual(added);
    await expect(b).resolves.toEqual(added);
    await expect(followed).resolves.toEqual(added);
    expect(sends.state(1, 'u')).toEqual({ result: added });
    await expect(sends.follow(1, 'u')).resolves.toEqual(added);
    // A later click sends again.
    const again = sends.run(1, 'u', start);
    expect(start).toHaveBeenCalledTimes(2);
    expect(sends.state(1, 'u')).toEqual({ sending: true });
    const refused: Outcome<AddedResult> = { ok: false, error: { code: 'rejected', message: 'live' } };
    answer(refused);
    await expect(again).resolves.toEqual(refused);
    expect(sends.state(1, 'u')).toEqual({ result: refused });
    t += MEDIA_RESULT_KEEP_MS;
    expect(sends.state(1, 'u')).toEqual({});
    await expect(sends.follow(1, 'u')).resolves.toBeUndefined();
  });

  it('forgets a tab, keeps what keep() accepts only, and never rejects', async () => {
    const sends = new MediaSends({ keep: (o) => o.ok || o.error.code !== 'expired' });
    let answer: (o: Outcome<AddedResult>) => void = () => undefined;
    const pending = sends.run(3, 'u', () => new Promise((r) => (answer = r)));
    await sends.run(3, 'v', async () => added);
    sends.clearTab(3);
    expect(sends.state(3, 'u')).toEqual({});
    expect(sends.state(3, 'v')).toEqual({});
    answer(added);
    await expect(pending).resolves.toEqual(added);
    // Its answer came from the document the tab left.
    expect(sends.state(3, 'u')).toEqual({});
    await sends.run(4, 'u', async () => ({ ok: false, error: { code: 'expired', message: 'gone' } }));
    expect(sends.state(4, 'u')).toEqual({});
    await expect(
      sends.run(4, 'w', async () => {
        throw new Error('boom');
      }),
    ).resolves.toEqual({ ok: false, error: { code: 'internal', message: 'boom' } });
    sends.clearAll();
    expect(sends.state(4, 'w')).toEqual({});
  });
});
