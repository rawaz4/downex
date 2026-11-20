import { describe, expect, it, vi } from 'vitest';
import {
  FILE_ADD_TIMEOUT_MS,
  MAX_FILE_NAME_CHARS,
  buildFileAddFields,
  createFileHandOff,
  fileName,
  meaningfulSegment,
  type FileSource,
} from './file-handoff';
import { HandoffHistory, type StorageAreaLike } from './history';
import type { AddedResult, AddFields, Outcome, RequestOptions } from './protocol';

function memoryArea(): StorageAreaLike {
  const data: Record<string, unknown> = {};
  return {
    get: async (key) => (key in data ? { [key]: data[key] } : {}),
    set: async (items) => void Object.assign(data, items),
  };
}

const source = (over: Partial<FileSource> = {}): FileSource => ({
  url: 'https://cdn2.example/cdn/clip.mp4?sig=abc',
  requestUrl: 'https://site.example/v/clip',
  headers: [
    ['Cookie', 'sid=1'],
    ['Referer', 'https://site.example/watch/1'],
    ['Range', 'bytes=0-'],
    ['Accept', '*/*'],
  ],
  size: 6 * 1024 * 1024,
  frameUrl: 'https://site.example/watch/1',
  pageTitle: 'My clip - Site',
  ...over,
});

/** A small deterministic generator for property tests (mulberry32). */
function random(seed: number): () => number {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

describe('file names', () => {
  it('takes the Content-Disposition name first, as a base name with .mp4', () => {
    expect(fileName(source({ filename: 'Holiday.MP4' }))).toBe('Holiday.mp4');
    expect(fileName(source({ filename: '../../etc/Holiday.m4v' }))).toBe('Holiday.mp4');
    expect(fileName(source({ filename: 'Holiday' }))).toBe('Holiday.mp4');
    expect(fileName(source({ filename: 'Clip\u0000\u0007 2.mp4' }))).toBe('Clip 2.mp4');
  });

  it('then a meaningful last path segment, else the title, else "video"', () => {
    expect(fileName(source())).toBe('clip.mp4');
    expect(fileName(source({ url: 'https://cdn.example/a/My%20Trip.m4v' }))).toBe('My Trip.mp4');
    expect(fileName(source({ url: 'https://cdn.example/a/video.mp4', requestUrl: undefined }))).toBe('My clip - Site.mp4');
    expect(fileName(source({ url: 'https://cdn.example/a/1234567.mp4', requestUrl: 'https://site.example/v/Trip' }))).toBe('Trip.mp4');
    expect(fileName(source({ url: `https://cdn.example/${'a1'.repeat(16)}.mp4`, requestUrl: undefined, pageTitle: undefined }))).toBe('video.mp4');
    expect(fileName({ url: 'https://cdn.example/stream/', pageTitle: '   ' })).toBe('video.mp4');
  });

  it.each([
    ['https://a.example/video.mp4', undefined],
    ['https://a.example/INDEX.M4V', undefined],
    ['https://a.example/file', undefined],
    ['https://a.example/media.mp4', undefined],
    ['https://a.example/download', undefined],
    ['https://a.example/play.mp4', undefined],
    ['https://a.example/stream.mp4', undefined],
    ['https://a.example/123456.mp4', undefined],
    ['https://a.example/0123456789abcdef01234567.mp4', undefined],
    ['https://a.example/', undefined],
    ['https://a.example/0123456789abcdef0123456.mp4', '0123456789abcdef0123456.mp4'],
    ['https://a.example/clip-1.mp4', 'clip-1.mp4'],
    ['https://a.example/%E2%82%AC%20clip.mp4', '€ clip.mp4'],
    ['https://a.example/%E0%A4%A.mp4', '%E0%A4%A.mp4'],
    ['not a url', undefined],
  ])('the last segment of %s is %s', (url, name) => {
    expect(meaningfulSegment(url)).toBe(name);
  });

  it(`cuts names to ${MAX_FILE_NAME_CHARS} characters with the extension`, () => {
    const long = fileName(source({ filename: `${'é'.repeat(500)}.mp4` }));
    expect([...long]).toHaveLength(MAX_FILE_NAME_CHARS);
    expect(long.endsWith('é.mp4')).toBe(true);
  });

  it('always gives one .mp4 name for random input', () => {
    const next = random(11);
    const chars = 'aZ09._- /\\%:?#\u0000‮€';
    const word = () => Array.from({ length: Math.floor(next() * 30) }, () => chars[Math.floor(next() * chars.length)]).join('');
    for (let i = 0; i < 3000; i++) {
      const name = fileName({
        url: `https://cdn.example/${word()}`,
        ...(next() < 0.5 ? { filename: word() } : {}),
        ...(next() < 0.5 ? { pageTitle: word() } : {}),
      });
      expect(name.endsWith('.mp4')).toBe(true);
      expect(name.length).toBeGreaterThan(4);
      expect([...name].length).toBeLessThanOrEqual(MAX_FILE_NAME_CHARS);
      // eslint-disable-next-line no-control-regex
      expect(name).not.toMatch(/[\u0000-\u001f\u007f-\u009f]/);
    }
  });
});

describe('buildFileAddFields', () => {
  it('sends the first URL with the headers the browser sent for it', () => {
    const r = buildFileAddFields(source(), { userAgent: 'TestUA/1' });
    expect(r).toEqual({
      ok: true,
      value: {
        url: 'https://site.example/v/clip',
        method: 'GET',
        headers: [
          ['Cookie', 'sid=1'],
          ['Referer', 'https://site.example/watch/1'],
          ['User-Agent', 'TestUA/1'],
          ['Accept', '*/*'],
        ],
        filename: 'clip.mp4',
        mime: 'video/mp4',
        size: 6 * 1024 * 1024,
        deadlineMs: FILE_ADD_TIMEOUT_MS,
        referrer: 'https://site.example/watch/1',
        // The playing frame's page, offered when the link expires (DESKTOP-SPEC §10).
        pageUrl: 'https://site.example/watch/1',
      },
    });
  });

  it('sends the tab address as the page, without user name or fragment', () => {
    const r = buildFileAddFields(source({ pageUrl: 'https://u:p@site.example/watch/1?t=2#c' }), { userAgent: 'UA' });
    expect(r.ok && r.value.pageUrl).toBe('https://site.example/watch/1?t=2');
    const none = buildFileAddFields(source({ pageUrl: 'about:blank', frameUrl: 'blob:https://site.example/1' }), { userAgent: 'UA' });
    expect(none.ok && none.value).not.toHaveProperty('pageUrl');
  });

  it('keeps a recorded request without a Cookie as it was', () => {
    const r = buildFileAddFields(source({ headers: [['Accept', '*/*']], requestUrl: undefined }), { userAgent: 'UA' });
    expect(r).toMatchObject({ ok: true, value: { url: 'https://cdn2.example/cdn/clip.mp4?sig=abc', headers: [['User-Agent', 'UA'], ['Accept', '*/*']] } });
    expect(r.ok && r.value).not.toHaveProperty('referrer');
  });

  it('without recorded headers: no Cookie, a default-policy Referer of the frame, size unknown', () => {
    const r = buildFileAddFields(source({ headers: undefined, requestUrl: undefined, size: 0, frameUrl: 'https://player.example/embed/9?x=1#t' }), {
      userAgent: 'UA',
    });
    expect(r).toMatchObject({
      ok: true,
      value: {
        url: 'https://cdn2.example/cdn/clip.mp4?sig=abc',
        headers: [
          ['Referer', 'https://player.example/'],
          ['User-Agent', 'UA'],
        ],
        size: -1,
        referrer: 'https://player.example/',
      },
    });
    // An https frame never sends a Referer to an http file.
    const plain = buildFileAddFields(source({ headers: undefined, requestUrl: undefined, url: 'http://cdn.example/a.mp4' }), { userAgent: '' });
    expect(plain).toMatchObject({ ok: true, value: { headers: [] } });
  });

  it('refuses addresses Downex cannot fetch', () => {
    expect(buildFileAddFields(source({ url: 'blob:https://site.example/1', requestUrl: undefined }), { userAgent: '' })).toMatchObject({
      ok: false,
      error: { code: 'invalid_request' },
    });
    // A first URL that is not http(s) gives way to the final one.
    expect(buildFileAddFields(source({ requestUrl: 'data:,x' }), { userAgent: '' })).toMatchObject({
      ok: true,
      value: { url: 'https://cdn2.example/cdn/clip.mp4?sig=abc' },
    });
  });
});

describe('createFileHandOff', () => {
  function setup(answer: (f: AddFields, o?: RequestOptions<AddedResult>) => Promise<Outcome<AddedResult>>) {
    const history = new HandoffHistory(memoryArea());
    const sent: { fields: AddFields; opts: RequestOptions<AddedResult> | undefined }[] = [];
    const handOff = createFileHandOff({
      userAgent: 'UA',
      history,
      now: () => 7,
      client: {
        add: (fields, opts) => {
          sent.push({ fields, opts });
          return answer(fields, opts);
        },
      },
    });
    return { handOff, history, sent };
  }

  it('sends add with its deadline and records a video-file hand-off without the query', async () => {
    const { handOff, history, sent } = setup(async () => ({ ok: true, value: { type: 'added', downloadId: 12, awaiting: true } }));
    const src = source({ requestUrl: undefined });
    await expect(handOff(src)).resolves.toEqual({ ok: true, value: { type: 'added', downloadId: 12, awaiting: true } });
    expect(sent[0]?.opts?.timeoutMs).toBe(FILE_ADD_TIMEOUT_MS);
    expect(sent[0]?.fields).toMatchObject({ url: src.url, deadlineMs: FILE_ADD_TIMEOUT_MS, mime: 'video/mp4' });
    await vi.waitFor(async () => expect(await history.list()).toHaveLength(1));
    expect((await history.list())[0]).toEqual({
      ts: 7,
      url: 'https://cdn2.example/cdn/clip.mp4',
      source: 'video-file',
      ok: true,
      filename: 'clip.mp4',
      title: 'My clip - Site',
      downloadId: 12,
      awaiting: true,
    });
  });

  it('records failures and a late added, and never rejects', async () => {
    let late: ((o: Outcome<AddedResult>) => void) | undefined;
    const { handOff, history } = setup(async (_f, o) => {
      late = o?.onLate;
      return { ok: false, error: { code: 'timeout', message: 'no add response within 15000 ms' } };
    });
    await expect(handOff(source())).resolves.toMatchObject({ ok: false, error: { code: 'timeout' } });
    late?.({ ok: true, value: { type: 'added', downloadId: 3 } });
    await vi.waitFor(async () => expect(await history.list()).toHaveLength(2));
    expect((await history.list())[0]).toMatchObject({ ok: true, downloadId: 3, source: 'video-file' });
    const broken = setup(async () => {
      throw new Error('boom');
    });
    await expect(broken.handOff(source())).resolves.toMatchObject({ ok: false, error: { code: 'internal', message: 'boom' } });
    const unsendable = setup(async () => ({ ok: true, value: { type: 'added', downloadId: 1 } }));
    await expect(unsendable.handOff(source({ url: 'blob:x', requestUrl: undefined }))).resolves.toMatchObject({ ok: false });
    expect(unsendable.sent).toHaveLength(0);
  });
});
