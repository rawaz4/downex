import { describe, expect, it } from 'vitest';
import {
  MAX_FILES_PER_FRAME,
  MAX_MEDIA_PER_TAB,
  MEDIA_BADGE_COLOR,
  MEDIA_STORAGE_PREFIX,
  MediaStore,
  RELAY_BADGE_COLOR,
  type MediaStorageArea,
  type RecordInput,
  type ResponseDetailsLike,
  detectMedia,
  frameFamily,
  frameOf,
  isPageVideoStream,
  itemKey,
  itemWeight,
  mediaFormat,
  mediaStorageKey,
  parseMediaItems,
  tabBadge,
  toMediaEntry,
} from './media';

function response(over: Partial<ResponseDetailsLike> & { contentType?: string } = {}): ResponseDetailsLike {
  const { contentType, ...rest } = over;
  return {
    url: 'https://cdn.example.com/v/1/master.m3u8',
    method: 'GET',
    tabId: 3,
    statusCode: 200,
    responseHeaders: contentType === undefined ? [] : [{ name: 'Content-Type', value: contentType }],
    ...rest,
  };
}

function memoryArea(initial: Record<string, unknown> = {}, opts: { failSet?: () => boolean; failGet?: boolean } = {}) {
  const data: Record<string, unknown> = { ...initial };
  const writes: Record<string, unknown>[] = [];
  const removes: string[][] = [];
  const area: MediaStorageArea = {
    get: async () => {
      if (opts.failGet) throw new Error('storage unavailable');
      return structuredClone(data);
    },
    set: async (items) => {
      if (opts.failSet?.()) throw new Error('quota');
      const copy = structuredClone(items);
      Object.assign(data, copy);
      writes.push(copy);
    },
    remove: async (keys) => {
      for (const k of keys) delete data[k];
      removes.push([...keys]);
    },
  };
  return { area, data, writes, removes };
}

function seen(url: string, seenAt: number, over: Partial<RecordInput> = {}): RecordInput {
  return { url, format: 'hls', tabId: 3, seenAt, frameId: 0, parentFrameId: -1, reqType: 'xmlhttprequest', ...over };
}

/** A file response of tab 3. */
function file(url: string, seenAt: number, over: Partial<RecordInput> = {}): RecordInput {
  return seen(url, seenAt, { format: 'file', reqType: 'media', size: 50 * 1024 * 1024, ...over });
}

describe('mediaFormat', () => {
  it('recognises the HLS and DASH Content-Types, case and parameters ignored', () => {
    const u = 'https://a.example/stream';
    expect(mediaFormat(u, 'application/vnd.apple.mpegurl')).toBe('hls');
    expect(mediaFormat(u, 'Application/X-MpegURL; charset=utf-8')).toBe('hls');
    expect(mediaFormat(u, 'audio/mpegurl')).toBe('hls');
    expect(mediaFormat(u, 'application/dash+xml')).toBe('dash');
    expect(mediaFormat(u, 'application/json')).toBeUndefined();
    expect(mediaFormat(u, undefined)).toBeUndefined();
  });

  it('falls back to a .m3u8 / .mpd path, ignoring the query, but not for HTML answers', () => {
    expect(mediaFormat('https://a.example/x/index.M3U8?token=1', 'application/octet-stream')).toBe('hls');
    expect(mediaFormat('https://a.example/x/manifest.mpd', undefined)).toBe('dash');
    expect(mediaFormat('https://a.example/x/manifest.mpd', 'text/html; charset=utf-8')).toBeUndefined();
    expect(mediaFormat('https://a.example/x?file=a.m3u8', undefined)).toBeUndefined();
    expect(mediaFormat('https://a.example/x.m3u8.js', undefined)).toBeUndefined();
    expect(mediaFormat('ftp://a.example/x.m3u8', undefined)).toBeUndefined();
    expect(mediaFormat('nonsense', 'application/dash+xml')).toBeUndefined();
  });
});

describe('detectMedia', () => {
  it('flags a manifest response of a tab, without fragment, with its content type and store', () => {
    expect(
      detectMedia(
        response({
          url: 'https://cdn.example.com/v.mpd#t=10',
          contentType: 'application/dash+xml;profiles=x',
          cookieStoreId: 'firefox-container-1',
          documentUrl: 'https://player.example.net/embed/9',
        }),
      ),
    ).toEqual({
      url: 'https://cdn.example.com/v.mpd',
      format: 'dash',
      tabId: 3,
      frameId: 0,
      parentFrameId: -1,
      reqType: 'xmlhttprequest',
      contentType: 'application/dash+xml',
      storeId: 'firefox-container-1',
      frameUrl: 'https://player.example.net/embed/9',
    });
    expect(detectMedia(response({ url: 'https://cdn.example.com/live', contentType: 'audio/mpegurl' }))?.format).toBe('hls');
    expect(detectMedia(response({ statusCode: 304 }))).toBeDefined();
    expect(detectMedia(response({ statusCode: undefined }))).toBeDefined();
    expect(detectMedia(response({ documentLifecycle: 'active' }))).toBeDefined();
  });

  it('ignores errors, other methods, requests without a tab, private and inactive documents', () => {
    expect(detectMedia(response({ statusCode: 404 }))).toBeUndefined();
    expect(detectMedia(response({ statusCode: 302 }))).toBeUndefined();
    expect(detectMedia(response({ method: 'POST' }))).toBeUndefined();
    expect(detectMedia(response({ tabId: -1 }))).toBeUndefined();
    expect(detectMedia(response({ incognito: true }))).toBeUndefined();
    expect(detectMedia(response({ documentLifecycle: 'prerender' }))).toBeUndefined();
    expect(detectMedia(response({ documentLifecycle: 'pending_deletion' }))).toBeUndefined();
    expect(detectMedia(response({ url: 'https://a.example/page.html', contentType: 'text/html' }))).toBeUndefined();
    expect(detectMedia(response({ url: `https://a.example/${'x'.repeat(33 * 1024)}.m3u8` }))).toBeUndefined();
    // A non-web requesting document is not kept as the Referer source.
    expect(detectMedia(response({ documentUrl: 'moz-extension://abc/page.html' }))?.frameUrl).toBeUndefined();
    // Fenced frames (ads) and URLs that are not http(s).
    expect(detectMedia(response({ frameType: 'fenced_frame' }))).toBeUndefined();
    expect(detectMedia(response({ frameType: 'sub_frame', frameId: 4, parentFrameId: 0 }))).toMatchObject({ frameId: 4, parentFrameId: 0 });
    expect(detectMedia(response({ url: 'blob:https://a.example/1.m3u8' }))).toBeUndefined();
  });

  it('records the frame and document, and keeps the M5 request types', () => {
    expect(
      detectMedia(response({ type: 'media', frameId: 7, parentFrameId: 2, documentId: 'DOC7', contentType: 'application/x-mpegurl' })),
    ).toMatchObject({ frameId: 7, parentFrameId: 2, documentId: 'DOC7', reqType: 'media' });
    // A request the browser names no frame for (a worker's) belongs to the top frame.
    expect(detectMedia(response({ frameId: -1, parentFrameId: -1 }))).toMatchObject({ frameId: 0, parentFrameId: -1 });
    for (const type of ['main_frame', 'sub_frame', 'object', 'image']) {
      expect(detectMedia(response({ type, contentType: 'application/vnd.apple.mpegurl' }))).toBeUndefined();
    }
  });
});

describe('frameOf and frameFamily', () => {
  it('normalises the frame of a request', () => {
    expect(frameOf({})).toEqual({ frameId: 0, parentFrameId: -1 });
    expect(frameOf({ frameId: 0, parentFrameId: 3, documentId: 'D' })).toEqual({ frameId: 0, parentFrameId: -1, documentId: 'D' });
    expect(frameOf({ frameId: 5, parentFrameId: 0 })).toEqual({ frameId: 5, parentFrameId: 0 });
    expect(frameOf({ frameId: 5 })).toEqual({ frameId: 5, parentFrameId: -1 });
    expect(frameOf({ frameId: 1.5, documentId: 'x'.repeat(200) })).toEqual({ frameId: 0, parentFrameId: -1 });
  });

  it('finds the frames below a navigated frame', () => {
    const frames = [
      { frameId: 0, parentFrameId: -1 },
      { frameId: 2, parentFrameId: 0 },
      { frameId: 5, parentFrameId: 2 },
      { frameId: 9, parentFrameId: 5 },
      { frameId: 3, parentFrameId: 0 },
    ];
    expect([...frameFamily(frames, [2])].sort()).toEqual([2, 5, 9]);
    expect([...frameFamily(frames, [3])]).toEqual([3]);
    expect([...frameFamily(frames, [])]).toEqual([]);
  });
});

describe('parseMediaItems', () => {
  it('keeps valid items, newest first, unique, capped', () => {
    const raw = [
      { url: 'https://a/1.m3u8', format: 'hls', firstSeen: 1, lastSeen: 1 },
      { url: 'https://a/2.mpd', format: 'dash', firstSeen: 2, lastSeen: 5, headers: [['Cookie', 'a=1']] },
      { url: 'https://a/1.m3u8', format: 'hls', firstSeen: 0, lastSeen: 0 },
      { url: 'javascript:alert(1)', format: 'hls', firstSeen: 1, lastSeen: 1 },
      { url: 'https://a/3.m3u8', format: 'webm', firstSeen: 1, lastSeen: 1 },
      { url: 'https://a/4.m3u8', format: 'hls', firstSeen: 1, lastSeen: 'x' },
      { url: 'https://a/5.m3u8', format: 'hls', firstSeen: 1, lastSeen: 2, headers: [['Cookie']], pageUrl: 'file:///x' },
      'junk',
    ];
    const items = parseMediaItems(raw);
    expect(items.map((i) => i.url)).toEqual(['https://a/2.mpd', 'https://a/5.m3u8', 'https://a/1.m3u8']);
    // Stored before frames and files: streams of the top frame.
    expect(items[0]).toMatchObject({ frameId: 0, parentFrameId: -1, reqType: 'xmlhttprequest', size: 0, short: false, segment: false });
    expect(items[0]?.headers).toEqual([['Cookie', 'a=1']]);
    expect(items[1]?.headers).toBeUndefined();
    expect(items[1]?.pageUrl).toBeUndefined();
    expect(items[2]?.lastSeen).toBe(1);
    expect(parseMediaItems({})).toEqual([]);
    const many = Array.from({ length: 70 }, (_, i) => ({ url: `https://a/${i}.m3u8`, format: 'hls', firstSeen: i, lastSeen: i }));
    expect(parseMediaItems(many)).toHaveLength(MAX_MEDIA_PER_TAB);
    expect(parseMediaItems(many)[0]?.url).toBe('https://a/69.m3u8');
  });

  it('reads files and frames, the same URL once per frame and document', () => {
    const raw = [
      {
        url: 'https://a/v.mp4',
        format: 'file',
        firstSeen: 1,
        lastSeen: 3,
        frameId: 4,
        parentFrameId: 0,
        documentId: 'D',
        size: 1024,
        seq: 7,
        reqType: 'media',
        requestUrl: 'https://a/r?x=1',
        filename: 'Clip.mp4',
      },
      { url: 'https://a/v.mp4', format: 'file', firstSeen: 1, lastSeen: 2, frameId: 4, parentFrameId: 0, documentId: 'D', size: 9 },
      { url: 'https://a/v.mp4', format: 'file', firstSeen: 1, lastSeen: 1, frameId: 0, size: 5 * 1024 * 1024, ad: true, segment: true },
    ];
    const items = parseMediaItems(raw);
    expect(items).toHaveLength(2);
    expect(items[0]).toMatchObject({ frameId: 4, documentId: 'D', size: 1024, short: true, seq: 7, requestUrl: 'https://a/r?x=1', filename: 'Clip.mp4' });
    expect(items[1]).toMatchObject({ frameId: 0, parentFrameId: -1, size: 5 * 1024 * 1024, ad: true, short: true, segment: true, reqType: 'media' });
    const odd = parseMediaItems([
      { url: 'https://a/v.mp4', format: 'file', firstSeen: 1, lastSeen: 1, size: -4, requestUrl: 'javascript:x', reqType: 'nope', seq: 1e13 },
    ])[0];
    expect(odd).toMatchObject({ size: 0, reqType: 'media', seq: 0 });
    expect(odd).not.toHaveProperty('requestUrl');
  });
});

describe('MediaStore', () => {
  it('dedupes by URL, moves a stream seen again to the front with fresh headers, in memory only', async () => {
    const { area, data, writes } = memoryArea();
    const store = new MediaStore(area);
    const first = await store.record(seen('https://a/1.m3u8', 10, { headers: [['Cookie', 'old']] }));
    await store.record(seen('https://a/2.mpd', 20, { format: 'dash' }));
    expect(writes).toHaveLength(2);
    const again = await store.record(seen('https://a/1.m3u8', 30, { headers: [['Cookie', 'new']] }));
    expect(again).toEqual({ key: first?.key, added: false, shown: false });
    const list = await store.list(3);
    expect(list.map((i) => [i.url, i.firstSeen, i.lastSeen])).toEqual([
      ['https://a/1.m3u8', 10, 30],
      ['https://a/2.mpd', 20, 20],
    ]);
    expect(list[0]?.headers).toEqual([['Cookie', 'new']]);
    expect(store.count(3)).toBe(2);
    // Nothing shown changed: not written until the next change that is.
    expect(writes).toHaveLength(2);
    await store.record(seen('https://a/3.m3u8', 40));
    expect(parseMediaItems(data[mediaStorageKey(3)]).map((i) => [i.url, i.headers])).toEqual([
      ['https://a/3.m3u8', undefined],
      ['https://a/1.m3u8', [['Cookie', 'new']]],
      ['https://a/2.mpd', undefined],
    ]);
  });

  it('keys items by kind and a number unique in the worker, and finds them by key', async () => {
    const store = new MediaStore(memoryArea().area);
    const a = await store.record(seen('https://a/1.m3u8', 1));
    const b = await store.record(file('https://a/1.mp4', 2, { tabId: 4 }));
    expect(a?.key).toMatch(/^s[0-9]+$/);
    expect(b?.key).toMatch(/^f[0-9]+$/);
    expect(a?.key.slice(1)).not.toBe(b?.key.slice(1));
    expect((await store.byKey(4, b!.key))?.url).toBe('https://a/1.mp4');
    expect(await store.byKey(3, b!.key)).toBeUndefined();
    expect(itemKey({ format: 'dash', seq: 12 })).toBe('s12');
    expect(await store.get(4, 'https://a/1.mp4')).toBeUndefined();
    expect((await store.get(4, 'https://a/1.mp4', 'file'))?.format).toBe('file');
  });

  it('keeps the newest 50 streams of a tab', async () => {
    const { area } = memoryArea();
    const counts: number[] = [];
    const store = new MediaStore(area, { onChange: (_t, n) => counts.push(n) });
    for (let i = 0; i < 60; i++) void store.record(seen(`https://a/${i}.m3u8`, i));
    await store.list(3);
    // Every new stream changed the listing, also those that pushed the oldest out.
    expect(counts).toHaveLength(60);
    await store.record(seen('https://a/59.m3u8', 100)); // seen again: nothing shown changed
    expect(counts).toHaveLength(60);
    const list = await store.list(3);
    expect(list).toHaveLength(MAX_MEDIA_PER_TAB);
    expect(list[0]?.url).toBe('https://a/59.m3u8');
    expect(list.at(-1)?.url).toBe('https://a/10.m3u8');
    expect(counts.at(-1)).toBe(50);
  });

  it('clears a tab on navigation or close, and keeps other tabs', async () => {
    const { area, data, removes } = memoryArea();
    const changes: [number, number][] = [];
    const store = new MediaStore(area, { onChange: (t, n) => changes.push([t, n]) });
    await store.record(seen('https://a/1.m3u8', 1));
    await store.record(seen('https://a/2.m3u8', 2, { tabId: 4 }));
    await store.clearTab(3);
    expect(await store.list(3)).toEqual([]);
    expect(store.count(4)).toBe(1);
    expect(data[mediaStorageKey(3)]).toBeUndefined();
    expect(removes).toEqual([[mediaStorageKey(3)]]);
    expect(changes).toEqual([
      [3, 1],
      [4, 1],
      [3, 0],
    ]);
    await store.clearTab(3); // nothing listed: no write
    expect(removes).toHaveLength(1);
    await store.clearAll();
    expect(Object.keys(data)).toEqual([]);
  });

  it('moves a replaced tab to its new ID', async () => {
    const { area, data } = memoryArea();
    const store = new MediaStore(area);
    await store.record(seen('https://a/1.m3u8', 1));
    await store.replace(3, 9);
    expect(store.count(3)).toBe(0);
    expect(store.count(9)).toBe(1);
    expect(Object.keys(data)).toEqual([mediaStorageKey(9)]);
    await store.replace(5, 6);
    expect(store.count(6)).toBe(0);
  });

  it('survives a service-worker restart: a new store reads the list back', async () => {
    const { area, data } = memoryArea({ unrelated: 1, [`${MEDIA_STORAGE_PREFIX}x`]: [] });
    const first = new MediaStore(area);
    await first.record(seen('https://a/1.m3u8', 1, { headers: [['Cookie', 'c=1']] }));
    await first.record(seen('https://a/2.m3u8', 2, { tabId: 7 }));
    const counts: [number, number][] = [];
    const second = new MediaStore(area, { onChange: (t, n) => counts.push([t, n]) });
    expect(second.loaded).toBe(false);
    expect(second.count(3)).toBe(0);
    await second.ready;
    expect(second.loaded).toBe(true);
    expect((await second.list(3))[0]?.headers).toEqual([['Cookie', 'c=1']]);
    expect(counts.sort()).toEqual([
      [3, 1],
      [7, 1],
    ]);
    expect(data.unrelated).toBe(1);
  });

  it('applies changes made before the store was read on top of it, in call order', async () => {
    const { area } = memoryArea({ [mediaStorageKey(3)]: [{ url: 'https://a/old.m3u8', format: 'hls', firstSeen: 1, lastSeen: 1 }] });
    const store = new MediaStore(area);
    const a = store.record(seen('https://a/new.m3u8', 5));
    const b = store.clearTab(3);
    const c = store.record(seen('https://a/after.m3u8', 6));
    await Promise.all([a, b, c]);
    expect((await store.list(3)).map((i) => i.url)).toEqual(['https://a/after.m3u8']);
  });

  it('keeps headers in memory only when asked not to persist them', async () => {
    const { area, data } = memoryArea();
    const store = new MediaStore(area, { persistHeaders: false });
    await store.record(seen('https://a/1.m3u8', 1, { headers: [['Cookie', 'secret']] }));
    expect((await store.list(3))[0]?.headers).toEqual([['Cookie', 'secret']]);
    expect(JSON.stringify(data)).not.toContain('secret');
    expect(parseMediaItems(data[mediaStorageKey(3)])[0]?.url).toBe('https://a/1.m3u8');
  });

  it('only annotates the item it looked the page up for', async () => {
    const { area } = memoryArea();
    const store = new MediaStore(area);
    const old = await store.record(seen('https://a/1.m3u8', 10));
    await store.annotate(3, old!.key, { url: 'https://site.example/watch?v=1', title: '  A video  ' });
    let [item] = await store.list(3);
    expect(item?.pageUrl).toBe('https://site.example/watch?v=1');
    expect(item?.pageTitle).toBe('A video');
    // Another document: the same manifest is another item.
    await store.clearTab(3);
    const fresh = await store.record(seen('https://a/1.m3u8', 20));
    expect(fresh?.key).not.toBe(old?.key);
    // The lookup of the earlier sighting arrives late: the newer item keeps its own page.
    await store.annotate(3, old!.key, { title: 'Old page' });
    await store.annotate(3, fresh!.key, { url: 'chrome://newtab/', title: 'New page' });
    [item] = await store.list(3);
    expect(item?.pageTitle).toBe('New page');
    expect(item?.pageUrl).toBeUndefined();
    // Cleared by a navigation meanwhile: nothing comes back.
    await store.clearTab(3);
    await store.annotate(3, fresh!.key, { title: 'x' });
    expect(store.count(3)).toBe(0);
  });

  it('bounds what it stores: oldest headers go first, then the oldest streams', async () => {
    const { area } = memoryArea();
    const big = (n: number): [string, string][] => [['Cookie', 'x'.repeat(n)]];
    const one = itemWeight({ url: 'https://a/0.m3u8', headers: big(1000) });
    const store = new MediaStore(area, { maxBytes: one * 3 });
    for (let i = 0; i < 3; i++) await store.record(seen(`https://a/${i}.m3u8`, i, { headers: big(1000) }));
    expect((await store.list(3)).every((i) => i.headers)).toBe(true);
    await store.record(seen('https://a/3.m3u8', 3, { headers: big(1000), tabId: 4 }));
    const tab3 = await store.list(3);
    expect(tab3.find((i) => i.url === 'https://a/0.m3u8')?.headers).toBeUndefined();
    expect(tab3.find((i) => i.url === 'https://a/2.m3u8')?.headers).toBeDefined();
    expect(store.count(4)).toBe(1);

    const tiny = new MediaStore(memoryArea().area, { maxBytes: 300 });
    for (let i = 0; i < 5; i++) await tiny.record(seen(`https://a/${i}.m3u8`, i));
    const left = await tiny.list(3);
    expect(left.length).toBeLessThan(5);
    expect(left[0]?.url).toBe('https://a/4.m3u8');
  });

  it('keeps at most maxTabs tabs, dropping the least recently updated', async () => {
    const { area, data } = memoryArea();
    const store = new MediaStore(area, { maxTabs: 2 });
    await store.record(seen('https://a/1.m3u8', 1, { tabId: 1 }));
    await store.record(seen('https://a/2.m3u8', 2, { tabId: 2 }));
    await store.record(seen('https://a/1b.m3u8', 3, { tabId: 1 }));
    await store.record(seen('https://a/3.m3u8', 4, { tabId: 3 }));
    expect(store.counts().map(([t]) => t).sort()).toEqual([1, 3]);
    expect(data[mediaStorageKey(2)]).toBeUndefined();
  });

  it('coalesces writes and retries a failed write with the next change', async () => {
    let fail = true;
    const { area, data, writes } = memoryArea({}, { failSet: () => fail });
    const store = new MediaStore(area);
    await expect(store.record(seen('https://a/1.m3u8', 1))).rejects.toThrow('quota');
    expect(store.count(3)).toBe(1);
    fail = false;
    await store.record(seen('https://a/2.m3u8', 2, { tabId: 4 }));
    expect(Object.keys(data).sort()).toEqual([mediaStorageKey(3), mediaStorageKey(4)]);

    writes.length = 0;
    await Promise.all(Array.from({ length: 10 }, (_, i) => store.record(seen(`https://a/x${i}.m3u8`, 10 + i))));
    expect(writes.length).toBeLessThan(10);
    expect(parseMediaItems(data[mediaStorageKey(3)])).toHaveLength(11);
  });

  it('treats an unreadable store as empty', async () => {
    const store = new MediaStore(memoryArea({}, { failGet: true }).area);
    await store.ready;
    expect(store.loaded).toBe(true);
    expect(await store.list(3)).toEqual([]);
  });

  it('tells the popup nothing about headers', () => {
    expect(
      toMediaEntry({
        url: 'https://a/1.m3u8',
        format: 'hls',
        firstSeen: 1,
        lastSeen: 2,
        pageTitle: 'T',
        headers: [['Cookie', 'secret']],
        storeId: 's',
        frameId: 3,
        parentFrameId: 0,
        size: 0,
        reqType: 'xmlhttprequest',
        short: false,
        segment: false,
        seq: 4,
      }),
    ).toEqual({ url: 'https://a/1.m3u8', format: 'hls', firstSeen: 1, lastSeen: 2, pageTitle: 'T' });
  });

  it('keeps the same URL apart per frame and document', async () => {
    const store = new MediaStore(memoryArea().area);
    const a = await store.record(seen('https://a/1.m3u8', 1));
    const b = await store.record(seen('https://a/1.m3u8', 2, { frameId: 4, parentFrameId: 0, documentId: 'D1' }));
    const c = await store.record(seen('https://a/1.m3u8', 3, { frameId: 4, parentFrameId: 0, documentId: 'D2' }));
    // Firefox names no document: it matches the item of the frame.
    const d = await store.record(seen('https://a/1.m3u8', 4, { frameId: 4, parentFrameId: 0 }));
    expect(new Set([a?.key, b?.key, c?.key]).size).toBe(3);
    expect(d?.added).toBe(false);
    expect(store.count(3)).toBe(3);
  });

  it('clears a navigated frame and the frames below it, keeping the others', async () => {
    const changes: [number, number][] = [];
    const { area, data } = memoryArea();
    const store = new MediaStore(area, { onChange: (t, n) => changes.push([t, n]) });
    await store.record(seen('https://a/top.m3u8', 1));
    await store.record(file('https://a/2.mp4', 2, { frameId: 2, parentFrameId: 0 }));
    await store.record(seen('https://a/5.m3u8', 3, { frameId: 5, parentFrameId: 2 }));
    await store.record(seen('https://a/3.m3u8', 4, { frameId: 3, parentFrameId: 0 }));
    expect([...(await store.clearFrames(3, [2]))].sort()).toEqual([2, 5]);
    expect((await store.list(3)).map((i) => i.url)).toEqual(['https://a/3.m3u8', 'https://a/top.m3u8']);
    expect(parseMediaItems(data[mediaStorageKey(3)])).toHaveLength(2);
    expect(changes.at(-1)).toEqual([3, 2]);
    // Nothing of that frame: nothing written or reported.
    const n = changes.length;
    await store.clearFrames(3, [7]);
    expect(changes).toHaveLength(n);
  });
});

describe('MediaStore files', () => {
  it('updates a file per range request in memory, and writes when its size is learned', async () => {
    const { area, data, writes } = memoryArea();
    const changes: number[] = [];
    const store = new MediaStore(area, { onChange: (_t, n) => changes.push(n) });
    const first = await store.record(file('https://cdn.example/v/clip.mp4', 1, { size: 0 }));
    expect(first).toMatchObject({ added: true, shown: true });
    expect(writes).toHaveLength(1);
    // The next range requests of the playing video.
    for (let i = 2; i < 30; i++) {
      expect(await store.record(file('https://cdn.example/v/clip.mp4', i, { size: 0, headers: [['Accept', `x${i}`]] }))).toEqual({
        key: first?.key,
        added: false,
        shown: false,
      });
    }
    expect(writes).toHaveLength(1);
    expect(changes).toHaveLength(1);
    // A Content-Range named the total.
    expect(await store.record(file('https://cdn.example/v/clip.mp4', 30, { size: 48_000_000 }))).toMatchObject({ shown: true });
    expect(writes).toHaveLength(2);
    expect(parseMediaItems(data[mediaStorageKey(3)])[0]).toMatchObject({ size: 48_000_000, lastSeen: 30, short: false });
    // A smaller size later never wins.
    await store.record(file('https://cdn.example/v/clip.mp4', 31, { size: 1000 }));
    expect((await store.list(3))[0]?.size).toBe(48_000_000);
  });

  it('refreshes a listed file with a 304 and adds none', async () => {
    const store = new MediaStore(memoryArea().area);
    expect(await store.record(file('https://a/x.mp4', 1, { refreshOnly: true, size: 0 }))).toBeUndefined();
    expect(store.count(3)).toBe(0);
    await store.record(file('https://a/x.mp4', 2));
    expect(await store.record(file('https://a/x.mp4', 3, { refreshOnly: true, size: 0 }))).toMatchObject({ added: false });
    expect((await store.list(3))[0]?.lastSeen).toBe(3);
  });

  it('replaces a file whose query token changed when origin, path and size are the same', async () => {
    const store = new MediaStore(memoryArea().area);
    const a = await store.record(file('https://cdn.example/v/clip.mp4?token=1', 1, { headers: [['Cookie', 'a']] }));
    const b = await store.record(file('https://cdn.example/v/clip.mp4?token=2', 2, { headers: [['Cookie', 'b']] }));
    expect(b).toEqual({ key: a?.key, added: false, shown: true });
    expect(await store.list(3)).toEqual([
      expect.objectContaining({ url: 'https://cdn.example/v/clip.mp4?token=2', headers: [['Cookie', 'b']], firstSeen: 1 }),
    ]);
    // Another size, another path or an unknown size: another file.
    await store.record(file('https://cdn.example/v/clip.mp4?token=3', 3, { size: 1234 * 1024 }));
    await store.record(file('https://cdn.example/v/other.mp4?token=2', 4));
    await store.record(file('https://cdn.example/v/clip.mp4?token=4', 5, { size: 0 }));
    expect(store.count(3)).toBe(4);
  });

  it('keeps the first URL of a redirect chain, the file name and the short marks', async () => {
    const store = new MediaStore(memoryArea().area);
    await store.record(file('https://cdn.example/x.mp4?sig=1', 1, { requestUrl: 'https://site.example/v/1', filename: 'Clip.mp4' }));
    await store.record(file('https://cdn.example/small.mp4', 2, { size: 1024 }));
    await store.record(file('https://ads.example/ad.mp4', 3, { ad: true }));
    const [ad, small, big] = await store.list(3);
    expect(big).toMatchObject({ requestUrl: 'https://site.example/v/1', filename: 'Clip.mp4', short: false });
    expect(small).toMatchObject({ short: true });
    expect(ad).toMatchObject({ ad: true, short: true });
  });

  it('marks four files of one directory within a minute as segments of a stream', async () => {
    const { area, data } = memoryArea();
    const store = new MediaStore(area);
    const seg = (i: number, at: number, frameId = 2) =>
      file(`https://cdn.example/hls/seg${i}.mp4`, at, { size: 900_000, frameId, parentFrameId: 0 });
    for (let i = 0; i < 3; i++) expect(await store.record(seg(i, i * 1000))).toMatchObject({ added: true, shown: true });
    // Until the fourth one they are listed as short files.
    expect((await store.list(3)).map((x) => [x.short, x.segment])).toEqual([
      [true, false],
      [true, false],
      [true, false],
    ]);
    expect(await store.record(seg(3, 3000))).toMatchObject({ shown: true });
    expect((await store.list(3)).every((x) => x.segment)).toBe(true);
    expect(parseMediaItems(data[mediaStorageKey(3)]).every((x) => x.segment)).toBe(true);
    // A later file within a minute of the run is a segment at once; one after a pause is not.
    await store.record(seg(4, 30_000));
    expect((await store.list(3))[0]?.segment).toBe(true);
    await store.record(seg(5, 500_000));
    expect((await store.list(3))[0]).toMatchObject({ url: 'https://cdn.example/hls/seg5.mp4', segment: false, short: true });
    // Files of another frame are counted on their own, and files spread over more than a minute are not segments.
    for (let i = 0; i < 4; i++) await store.record(seg(10 + i, i * 1000, 3));
    for (let i = 0; i < 4; i++) await store.record(file(`https://cdn.example/slow/${i}.mp4`, 1_000_000 + i * 61_000));
    const listed = (await store.list(3)).filter((x) => !x.segment).map((x) => x.url);
    expect(listed).toEqual([
      'https://cdn.example/slow/3.mp4',
      'https://cdn.example/slow/2.mp4',
      'https://cdn.example/slow/1.mp4',
      'https://cdn.example/slow/0.mp4',
      'https://cdn.example/hls/seg5.mp4',
    ]);
  });

  it('never takes large files of one directory for segments', async () => {
    const store = new MediaStore(memoryArea().area);
    for (let i = 1; i <= 5; i++) await store.record(file(`https://cdn.example/videos/clip${i}.mp4`, i * 1000, { size: 40 * 1024 * 1024 }));
    expect((await store.list(3)).map((x) => x.segment)).toEqual([false, false, false, false, false]);
  });

  it(`keeps at most ${MAX_FILES_PER_FRAME} files per frame, dropping segments first`, async () => {
    const store = new MediaStore(memoryArea().area);
    for (let i = 0; i < 4; i++) await store.record(file(`https://cdn.example/seg/${i}.mp4`, i, { size: 1000 }));
    for (let i = 0; i < MAX_FILES_PER_FRAME; i++) await store.record(file(`https://cdn.example/f${i}/v.mp4`, 10 + i));
    const files = await store.list(3);
    expect(files).toHaveLength(MAX_FILES_PER_FRAME);
    expect(files.every((x) => !x.segment)).toBe(true);
    await store.record(file('https://cdn.example/new/v.mp4', 100));
    const after = await store.list(3);
    expect(after).toHaveLength(MAX_FILES_PER_FRAME);
    expect(after[0]?.url).toBe('https://cdn.example/new/v.mp4');
    expect(after.some((x) => x.url === 'https://cdn.example/f0/v.mp4')).toBe(false);
    // Another frame has its own room.
    await store.record(file('https://cdn.example/other/v.mp4', 101, { frameId: 6, parentFrameId: 0 }));
    expect(store.count(3)).toBe(MAX_FILES_PER_FRAME + 1);
  });

  it('gives up segments first when the storage budget is tight', async () => {
    const big: [string, string][] = [['Cookie', 'x'.repeat(2000)]];
    const one = itemWeight({ url: 'https://cdn.example/hls/seg0.mp4', headers: big });
    const store = new MediaStore(memoryArea().area, { maxBytes: one * 5 });
    await store.record(file('https://cdn.example/feature.mp4', 1, { headers: big }));
    for (let i = 0; i < 4; i++) await store.record(file(`https://cdn.example/hls/seg${i}.mp4`, 2 + i, { size: 1000, headers: big }));
    expect((await store.list(3)).filter((x) => x.segment)).toHaveLength(4);
    await store.record(file('https://cdn.example/other.mp4', 10, { headers: big }));
    const left = await store.list(3);
    expect(left.find((x) => x.url === 'https://cdn.example/feature.mp4')?.headers).toEqual(big);
    expect(left.filter((x) => x.segment).length).toBeLessThan(4);
  });

  it('gives restored items new keys above the stored ones when they clash', async () => {
    const { area } = memoryArea({
      [mediaStorageKey(3)]: [
        { url: 'https://a/1.m3u8', format: 'hls', firstSeen: 1, lastSeen: 1, seq: 41 },
        { url: 'https://a/2.m3u8', format: 'hls', firstSeen: 2, lastSeen: 2 },
      ],
      [mediaStorageKey(4)]: [{ url: 'https://a/3.mp4', format: 'file', firstSeen: 3, lastSeen: 3, seq: 41 }],
    });
    const store = new MediaStore(area);
    await store.ready;
    const seqs = [...(await store.list(3)), ...(await store.list(4))].map((i) => i.seq);
    expect(new Set(seqs).size).toBe(3);
    expect(seqs).toContain(41);
    const next = await store.record(seen('https://a/9.m3u8', 9));
    expect(Number(next?.key.slice(1))).toBeGreaterThan(Math.max(...seqs));
  });
});

describe('tabBadge', () => {
  it('shows the count while Downex is available', () => {
    expect(tabBadge(3, false)).toEqual({ text: '3', color: MEDIA_BADGE_COLOR });
    expect(tabBadge(120, false).text).toBe('99+');
  });

  it('gives way to the global badge: the relay error wins, no streams show nothing of their own', () => {
    expect(tabBadge(3, true)).toEqual({ text: null, color: RELAY_BADGE_COLOR });
    expect(tabBadge(0, false)).toEqual({ text: null, color: RELAY_BADGE_COLOR });
  });
});

describe('badge input of a tab with page videos', () => {
  it('recognises the googlevideo manifests a page video hides', () => {
    expect(isPageVideoStream('https://rr3---sn-abc.googlevideo.com/videoplayback/manifest.m3u8')).toBe(true);
    expect(isPageVideoStream('https://manifest.googlevideo.com/api/manifest/hls_variant')).toBe(true);
    expect(isPageVideoStream('https://googlevideo.com.evil.example/a.m3u8')).toBe(false);
    expect(isPageVideoStream('https://cdn.example.com/a.m3u8')).toBe(false);
    expect(isPageVideoStream('nope')).toBe(false);
  });

  it('counts a tab without the streams the caller hides', async () => {
    const store = new MediaStore(memoryArea().area);
    await store.record(seen('https://manifest.googlevideo.com/a.m3u8', 1, { tabId: 2 }));
    await store.record(seen('https://cdn.example.com/b.mpd', 2, { format: 'dash', tabId: 2 }));
    expect(store.count(2)).toBe(2);
    expect(store.count(2, isPageVideoStream)).toBe(1);
    expect(store.count(3, isPageVideoStream)).toBe(0);
  });
});
