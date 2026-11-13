import { describe, expect, it } from 'vitest';
import {
  FRAME_MEDIA_SEEN_MS,
  FRAME_MEDIA_SEEN_MAX_MS,
  FrameMediaSeen,
  FrameReports,
  MAX_PENDING_DOCUMENTS,
  MAX_REDIRECTS,
  MAX_REPORT_FRAMES,
  PENDING_DOCUMENT_TTL_MS,
  PendingDocuments,
  REDIRECT_TTL_MS,
  RedirectMap,
  classifyFile,
  contentLengthSize,
  contentRangeTotal,
  isMediaTraffic,
  isMp4Path,
  playable,
  protectedOrLive,
  responseSize,
} from './frame-media';
import type { ResponseDetailsLike } from './media';
import type { FrameVideoMeta } from './offer-types';

const URL_MP4 = 'https://cdn.example.com/v/clip.mp4';

function response(over: Partial<ResponseDetailsLike> & { headers?: Record<string, string> } = {}): ResponseDetailsLike {
  const { headers = { 'Content-Type': 'video/mp4', 'Content-Length': '50000000' }, ...rest } = over;
  return {
    url: URL_MP4,
    method: 'GET',
    tabId: 3,
    type: 'media',
    statusCode: 200,
    frameId: 0,
    parentFrameId: -1,
    responseHeaders: Object.entries(headers).map(([name, value]) => ({ name, value })),
    ...rest,
  };
}

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

describe('classifyFile', () => {
  it('takes a progressive MP4 a <video> played, with its frame, size and store', () => {
    expect(
      classifyFile(
        response({
          url: `${URL_MP4}#t=4`,
          frameId: 5,
          parentFrameId: 0,
          documentId: 'DOC',
          cookieStoreId: 'firefox-container-2',
          documentUrl: 'https://player.example/embed/1',
        }),
      ),
    ).toEqual({
      url: URL_MP4,
      format: 'file',
      tabId: 3,
      reqType: 'media',
      frameId: 5,
      parentFrameId: 0,
      documentId: 'DOC',
      storeId: 'firefox-container-2',
      frameUrl: 'https://player.example/embed/1',
      size: 50_000_000,
      contentType: 'video/mp4',
    });
  });

  it.each([
    // type, status, Content-Type, path, listed
    ['media', 200, 'video/mp4', '/v.bin', true],
    ['media', 206, 'video/mp4; codecs="avc1"', '/v', true],
    ['media', 200, 'video/x-m4v', '/v', true],
    ['main_frame', 200, 'video/mp4', '/v', true],
    ['sub_frame', 200, 'video/mp4', '/v', true],
    ['object', 200, 'video/mp4', '/v', true],
    ['media', 200, 'application/octet-stream', '/v.mp4', true],
    ['media', 200, 'binary/octet-stream', '/V.M4V', true],
    ['media', 200, undefined, '/v.mp4', true],
    ['media', 200, 'application/octet-stream', '/v.bin', false],
    ['media', 200, undefined, '/v', false],
    // Script requests are MSE segments or split tracks: never files.
    ['xmlhttprequest', 200, 'video/mp4', '/v.mp4', false],
    ['xmlhttprequest', 206, 'video/mp4', '/v.mp4', false],
    ['other', 200, 'video/mp4', '/v.mp4', false],
    ['image', 200, 'video/mp4', '/v.mp4', false],
    ['script', 200, 'video/mp4', '/v.mp4', false],
    // Other containers, whatever the path says.
    ['media', 200, 'video/webm', '/v.mp4', false],
    ['media', 200, 'video/ogg', '/v.mp4', false],
    ['media', 200, 'video/quicktime', '/v.mp4', false],
    ['media', 200, 'video/x-matroska', '/v.mp4', false],
    ['media', 200, 'video/mp2t', '/v.mp4', false],
    ['media', 200, 'video/x-flv', '/v.mp4', false],
    ['media', 200, 'audio/mp4', '/v.mp4', false],
    ['media', 200, 'audio/mpeg', '/v.mp4', false],
    ['media', 200, 'text/html', '/v.mp4', false],
    ['media', 200, 'text/plain', '/v.mp4', false],
    ['media', 200, 'application/vnd.apple.mpegurl', '/v.mp4', false],
    // Statuses.
    ['media', 201, 'video/mp4', '/v.mp4', false],
    ['media', 204, 'video/mp4', '/v.mp4', false],
    ['media', 302, 'video/mp4', '/v.mp4', false],
    ['media', 404, 'video/mp4', '/v.mp4', false],
    ['media', 500, 'video/mp4', '/v.mp4', false],
  ] as const)('%s %d %s at %s: %s', (type, statusCode, contentType, path, listed) => {
    const headers: Record<string, string> = contentType === undefined ? {} : { 'Content-Type': contentType };
    const d = classifyFile(response({ type, statusCode, url: `https://cdn.example.com${path}?sig=1`, headers }));
    expect(d !== undefined).toBe(listed);
  });

  it('takes the Content-Disposition name of an untyped answer, and leaves attachments to capture', () => {
    const named = { 'Content-Type': 'application/octet-stream', 'Content-Disposition': 'inline; filename="Holiday Clip.MP4"' };
    expect(classifyFile(response({ url: 'https://cdn.example.com/get?id=1', headers: named }))).toMatchObject({
      filename: 'Holiday Clip.MP4',
      contentType: 'application/octet-stream',
    });
    const attachment = { 'Content-Type': 'video/mp4', 'Content-Disposition': 'attachment; filename="a.mp4"' };
    expect(classifyFile(response({ type: 'main_frame', headers: attachment }))).toBeUndefined();
    expect(classifyFile(response({ type: 'sub_frame', headers: attachment }))).toBeUndefined();
    // A player's request with that header is still the file the page played.
    expect(classifyFile(response({ type: 'media', headers: attachment }))).toMatchObject({ filename: 'a.mp4' });
  });

  it('refreshes with a 304, which may carry no Content-Type', () => {
    expect(classifyFile(response({ statusCode: 304, headers: {} }))).toMatchObject({ refreshOnly: true, size: 0 });
    expect(classifyFile(response({ statusCode: 304, headers: { 'Content-Type': 'video/webm' } }))).toBeUndefined();
  });

  it('applies the common gates: tabs, GET, private, prerender, fenced frames, http(s), URL length', () => {
    expect(classifyFile(response({ tabId: -1 }))).toBeUndefined();
    expect(classifyFile(response({ method: 'POST' }))).toBeUndefined();
    expect(classifyFile(response({ incognito: true }))).toBeUndefined();
    expect(classifyFile(response({ documentLifecycle: 'prerender' }))).toBeUndefined();
    expect(classifyFile(response({ documentLifecycle: 'cached' }))).toBeUndefined();
    expect(classifyFile(response({ documentLifecycle: 'active' }))).toBeDefined();
    expect(classifyFile(response({ frameType: 'fenced_frame' }))).toBeUndefined();
    expect(classifyFile(response({ frameType: 'sub_frame', frameId: 2, parentFrameId: 0 }))).toBeDefined();
    expect(classifyFile(response({ url: 'ftp://cdn.example.com/v.mp4' }))).toBeUndefined();
    expect(classifyFile(response({ url: 'blob:https://cdn.example.com/1' }))).toBeUndefined();
    expect(classifyFile(response({ url: `https://cdn.example.com/${'x'.repeat(33 * 1024)}.mp4` }))).toBeUndefined();
    expect(classifyFile(response({ type: undefined }))).toBeUndefined();
    expect(classifyFile(response({ statusCode: undefined }))).toBeUndefined();
  });

  it('marks what Firefox classified as an ad', () => {
    expect(classifyFile(response({ urlClassification: { firstParty: [], thirdParty: ['tracking_ad', 'tracking'] } }))?.ad).toBe(true);
    expect(classifyFile(response({ urlClassification: { firstParty: ['tracking_ad'], thirdParty: [] } }))?.ad).toBe(true);
    expect(classifyFile(response({ urlClassification: { firstParty: ['tracking_social'], thirdParty: [] } }))).not.toHaveProperty('ad');
  });

  it('reads the total of a range answer, the length of a full one', () => {
    expect(classifyFile(response({ statusCode: 206, headers: { 'Content-Type': 'video/mp4', 'Content-Range': 'bytes 0-1023/48000000' } }))?.size).toBe(48_000_000);
    expect(classifyFile(response({ statusCode: 206, headers: { 'Content-Type': 'video/mp4', 'Content-Range': 'bytes 0-1023/*' } }))?.size).toBe(0);
    // A 206's Content-Length is the range, never the total.
    expect(classifyFile(response({ statusCode: 206, headers: { 'Content-Type': 'video/mp4', 'Content-Length': '1024' } }))?.size).toBe(0);
    expect(classifyFile(response({ statusCode: 200, headers: { 'Content-Type': 'video/mp4' } }))?.size).toBe(0);
  });
});

describe('sizes', () => {
  it('parses Content-Range totals between 1 and 2^40', () => {
    expect(contentRangeTotal('bytes 0-99/100')).toBe(100);
    expect(contentRangeTotal('Bytes 100-199/1000 ')).toBe(1000);
    expect(contentRangeTotal('bytes */5000')).toBe(5000);
    expect(contentRangeTotal(`bytes 0-1/${2 ** 40}`)).toBe(2 ** 40);
    for (const bad of [undefined, '', 'bytes 0-99/*', `bytes 0-1/${2 ** 40 + 1}`, 'bytes 0-99/0', 'bytes 0-99/-5', 'bytes 0-99/1e9', 'items 0-1/9', 'bytes 0-99/100, bytes 0-1/5', 'bytes 0-99/ 0x10', `bytes 0-1/${'9'.repeat(200)}`]) {
      expect(contentRangeTotal(bad)).toBeUndefined();
    }
  });

  it('parses Content-Length between 1 and 2^40', () => {
    expect(contentLengthSize('12345')).toBe(12345);
    expect(contentLengthSize(' 7 ')).toBe(7);
    for (const bad of [undefined, '', '0', '-1', '1.5', '12a', '1e6', String(2 ** 40 + 1)]) expect(contentLengthSize(bad)).toBeUndefined();
  });

  it('takes the size that fits the status', () => {
    const h: [string, string][] = [
      ['Content-Range', 'bytes 0-1/900'],
      ['Content-Length', '2'],
    ];
    expect(responseSize(206, h)).toBe(900);
    expect(responseSize(200, h)).toBe(2);
    expect(responseSize(304, h)).toBe(0);
  });

  it('never throws on random headers, and every size is in range', () => {
    const next = random(42);
    const alphabet = 'bytes */-0123456789 ,;=xXe.';
    for (let i = 0; i < 5000; i++) {
      let s = next() < 0.5 ? 'bytes ' : '';
      const n = Math.floor(next() * 40);
      for (let j = 0; j < n; j++) s += alphabet[Math.floor(next() * alphabet.length)];
      for (const v of [contentRangeTotal(s), contentLengthSize(s)]) {
        if (v !== undefined) {
          expect(Number.isSafeInteger(v)).toBe(true);
          expect(v).toBeGreaterThanOrEqual(1);
          expect(v).toBeLessThanOrEqual(2 ** 40);
        }
      }
    }
  });
});

describe('classifyFile over random responses', () => {
  it('never throws, and lists only player or document responses of MP4 files', () => {
    const next = random(7);
    const pick = <T>(list: readonly T[]): T => list[Math.floor(next() * list.length)] as T;
    const types = ['media', 'main_frame', 'sub_frame', 'object', 'xmlhttprequest', 'other', 'image', undefined];
    const mimes = ['video/mp4', 'video/webm', 'application/octet-stream', 'text/html', '', 'VIDEO/MP4;x=1', 'audio/mp4', undefined];
    const paths = ['/a.mp4', '/a.m4v', '/a.webm', '/a', '/a.mp4.js', '/%2e%2e/a.mp4', '/a.MP4?x=.webm'];
    const dispositions = ['attachment; filename=a.mp4', 'inline; filename="b.m4v"', 'inline', ';;;', 'attachment', undefined];
    for (let i = 0; i < 4000; i++) {
      const headers: Record<string, string> = {};
      const mime = pick(mimes);
      if (mime !== undefined) headers['Content-Type'] = mime;
      const cd = pick(dispositions);
      if (cd !== undefined) headers['Content-Disposition'] = cd;
      if (next() < 0.5) headers['Content-Range'] = `bytes 0-${Math.floor(next() * 1000)}/${pick(['*', '0', '123', '99999999999999', 'x'])}`;
      const type = pick(types);
      const d = classifyFile(
        response({
          type,
          statusCode: pick([200, 206, 304, 302, 404, undefined]),
          url: `${pick(['https', 'http', 'ftp', 'blob:https'])}://cdn.example.com${pick(paths)}`,
          headers,
        }),
      );
      if (!d) continue;
      expect(['media', 'main_frame', 'sub_frame', 'object']).toContain(type);
      expect(d.format).toBe('file');
      expect(d.url.startsWith('http')).toBe(true);
      expect(d.size === 0 || (d.size !== undefined && d.size >= 1 && d.size <= 2 ** 40)).toBe(true);
      if ((type === 'main_frame' || type === 'sub_frame') && cd?.startsWith('attachment')) throw new Error('attachment listed');
    }
  });
});

describe('isMp4Path and isMediaTraffic', () => {
  it('recognises an element source of an MP4 file', () => {
    expect(isMp4Path('https://cdn.example.com/a/clip.mp4?sig=1')).toBe(true);
    expect(isMp4Path('http://cdn.example.com/a/CLIP.M4V')).toBe(true);
    expect(isMp4Path('https://cdn.example.com/a/clip.webm')).toBe(false);
    expect(isMp4Path('https://cdn.example.com/watch?f=clip.mp4')).toBe(false);
    expect(isMp4Path('blob:https://cdn.example.com/1')).toBe(false);
    expect(isMp4Path(undefined)).toBe(false);
  });

  it('tells media traffic from other answers', () => {
    expect(isMediaTraffic(response({ type: 'media', headers: {} }))).toBe(true);
    expect(isMediaTraffic(response({ type: 'xmlhttprequest', headers: { 'Content-Type': 'video/mp4' } }))).toBe(true);
    // Audio alone (a podcast's <audio>, a player's audio half) is no reason to look for a video.
    expect(isMediaTraffic(response({ type: 'xmlhttprequest', headers: { 'Content-Type': 'audio/mp4' } }))).toBe(false);
    expect(isMediaTraffic(response({ type: 'media', headers: { 'Content-Type': 'audio/mpeg' } }))).toBe(false);
    expect(isMediaTraffic(response({ type: 'xmlhttprequest', headers: { 'Content-Type': 'application/json' } }))).toBe(false);
    expect(isMediaTraffic(response({ type: 'media', incognito: true }))).toBe(false);
  });
});

describe('RedirectMap', () => {
  it('keeps the first URL of a chain until the request ends', () => {
    let now = 0;
    const map = new RedirectMap({ now: () => now });
    map.redirected('r1', 'https://site.example/v/1#x');
    map.redirected('r1', 'https://cdn1.example/hop');
    expect(map.first('r1')).toBe('https://site.example/v/1');
    expect(map.first('r2')).toBeUndefined();
    expect(map.first(undefined)).toBeUndefined();
    map.done('r1');
    expect(map.first('r1')).toBeUndefined();
    // Not http(s): not kept.
    map.redirected('r3', 'data:text/plain,x');
    expect(map.first('r3')).toBeUndefined();
    map.redirected('r4', 'https://site.example/v/4');
    now = REDIRECT_TTL_MS + 1;
    expect(map.first('r4')).toBeUndefined();
  });

  it(`keeps at most ${MAX_REDIRECTS} chains, dropping the oldest`, () => {
    const map = new RedirectMap();
    for (let i = 0; i <= MAX_REDIRECTS; i++) map.redirected(`r${i}`, `https://a.example/${i}`);
    expect(map.size).toBe(MAX_REDIRECTS);
    expect(map.first('r0')).toBeUndefined();
    expect(map.first(`r${MAX_REDIRECTS}`)).toBe(`https://a.example/${MAX_REDIRECTS}`);
  });
});

describe('PendingDocuments', () => {
  const doc = (tabId: number, frameId: number, url: string) => ({ tabId, frameId, url });

  it('hands the file over when its frame commits that URL', () => {
    let now = 0;
    const pending = new PendingDocuments<ReturnType<typeof doc>>({ now: () => now });
    pending.add(doc(1, 0, URL_MP4));
    pending.add(doc(1, 4, 'https://a.example/b.mp4'));
    expect(pending.take(1, 0, `${URL_MP4}#t=1`)).toEqual(doc(1, 0, URL_MP4));
    expect(pending.take(1, 0, URL_MP4)).toBeUndefined();
    // Another document committed: the response was a download or blocked.
    expect(pending.take(1, 4, 'https://a.example/other')).toBeUndefined();
    expect(pending.size).toBe(0);
    pending.add(doc(2, 0, URL_MP4));
    now = PENDING_DOCUMENT_TTL_MS + 1;
    expect(pending.take(2, 0, URL_MP4)).toBeUndefined();
  });

  it(`keeps at most ${MAX_PENDING_DOCUMENTS}, the latest per frame, and forgets closed tabs`, () => {
    const pending = new PendingDocuments<ReturnType<typeof doc>>();
    for (let i = 0; i < MAX_PENDING_DOCUMENTS + 5; i++) pending.add(doc(i, 0, URL_MP4));
    expect(pending.size).toBe(MAX_PENDING_DOCUMENTS);
    expect(pending.take(0, 0, URL_MP4)).toBeUndefined();
    pending.add(doc(100, 2, 'https://a.example/1.mp4'));
    pending.add(doc(100, 2, 'https://a.example/2.mp4'));
    expect(pending.take(100, 2, 'https://a.example/2.mp4')?.url).toBe('https://a.example/2.mp4');
    pending.add(doc(100, 3, 'https://a.example/3.mp4'));
    pending.clearTab(100);
    expect(pending.take(100, 3, 'https://a.example/3.mp4')).toBeUndefined();
  });
});

function video(id: number, over: Partial<FrameVideoMeta> = {}): FrameVideoMeta {
  return {
    id,
    src: '',
    mse: false,
    width: 640,
    height: 360,
    videoWidth: 1280,
    videoHeight: 720,
    durationMs: 120_000,
    drm: false,
    visible: true,
    decorative: false,
    meaningful: true,
    ...over,
  };
}

describe('FrameReports', () => {
  it('keeps the latest report per frame and says when it changed', () => {
    const reports = new FrameReports();
    expect(reports.set(1, 0, undefined, [video(1)], 10)).toBe(true);
    expect(reports.set(1, 0, undefined, [video(1)], 11)).toBe(false);
    expect(reports.set(1, 0, undefined, [video(1, { durationMs: 5000 })], 12)).toBe(true);
    expect(reports.set(1, 0, 'D2', [video(1, { durationMs: 5000 })], 13)).toBe(true);
    expect(reports.frame(1, 0)).toMatchObject({ documentId: 'D2', at: 13, videos: [{ durationMs: 5000 }] });
    expect(reports.frame(2, 0)).toBeUndefined();
  });

  it('tells whether a tab plays a video a look-ahead may be for', () => {
    const reports = new FrameReports();
    reports.set(1, 0, undefined, [video(1, { meaningful: false }), video(2, { drm: true })], 0);
    reports.set(1, 3, undefined, [video(3, { durationMs: -2 })], 0);
    expect(reports.playable(1)).toBe(false);
    reports.set(1, 4, undefined, [video(4)], 0);
    expect(reports.playable(1)).toBe(true);
    expect(playable(video(5, { durationMs: -1 }))).toBe(true);
  });

  it('recognises frames whose videos are all protected or all live', () => {
    expect(protectedOrLive(undefined)).toBe(false);
    expect(protectedOrLive({ frameId: 0, videos: [], at: 0 })).toBe(false);
    expect(protectedOrLive({ frameId: 0, videos: [video(1, { drm: true }), video(2, { drm: true })], at: 0 })).toBe(true);
    expect(protectedOrLive({ frameId: 0, videos: [video(1, { durationMs: -2 })], at: 0 })).toBe(true);
    expect(protectedOrLive({ frameId: 0, videos: [video(1, { drm: true }), video(2)], at: 0 })).toBe(false);
  });

  it('clears a committed frame and the frames below it; the top frame clears the tab', () => {
    const reports = new FrameReports();
    reports.committed(1, 2, 0);
    reports.committed(1, 5, 2);
    reports.committed(1, 7, 0);
    for (const f of [0, 2, 5, 7]) reports.set(1, f, undefined, [video(1)], 0);
    expect([...reports.committed(1, 2, 0)].sort()).toEqual([2, 5]);
    expect(reports.tab(1).map((r) => r.frameId).sort()).toEqual([0, 7]);
    expect([...reports.committed(1, 0)].sort()).toEqual([0, 2, 7]);
    expect(reports.tab(1)).toEqual([]);
  });

  it(`keeps at most ${MAX_REPORT_FRAMES} frames of a tab and 16 videos of a frame`, () => {
    const reports = new FrameReports();
    for (let f = 0; f <= MAX_REPORT_FRAMES; f++) reports.set(1, f, undefined, [video(1)], f);
    expect(reports.tab(1)).toHaveLength(MAX_REPORT_FRAMES);
    expect(reports.frame(1, 0)).toBeUndefined();
    reports.set(1, 99, undefined, Array.from({ length: 40 }, (_, i) => video(i + 1)), 0);
    expect(reports.frame(1, 99)?.videos).toHaveLength(16);
    reports.replace(1, 8);
    expect(reports.tab(1)).toEqual([]);
    expect(reports.tab(8)).toHaveLength(MAX_REPORT_FRAMES);
    reports.clearAll();
    expect(reports.tab(8)).toEqual([]);
  });
});

describe('FrameMediaSeen', () => {
  it(`tells a frame at most once per ${FRAME_MEDIA_SEEN_MS} ms`, () => {
    let now = 0;
    const seen = new FrameMediaSeen({ now: () => now });
    expect(seen.due(1, 3)).toBe(true);
    expect(seen.due(1, 3)).toBe(false);
    expect(seen.due(1, 4)).toBe(true);
    expect(seen.due(2, 3)).toBe(true);
    now = FRAME_MEDIA_SEEN_MS - 1;
    expect(seen.due(1, 3)).toBe(false);
    now = FRAME_MEDIA_SEEN_MS;
    expect(seen.due(1, 3)).toBe(true);
    seen.clearTab(1);
    expect(seen.due(1, 3)).toBe(true);
  });

  it('waits twice as long after each time a frame was told, up to 5 minutes', () => {
    let now = 0;
    const seen = new FrameMediaSeen({ now: () => now });
    const told: number[] = [];
    for (; now <= 30 * 60_000; now += 1000) if (seen.due(1, 3)) told.push(now / 1000);
    expect(told.slice(0, 6)).toEqual([0, 5, 15, 35, 75, 155]);
    expect(told.at(-1)! - told.at(-2)!).toBe(FRAME_MEDIA_SEEN_MAX_MS / 1000);
    expect(told.length).toBeLessThan(15);
  });
});
