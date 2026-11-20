import { describe, expect, it } from 'vitest';
import type { FrameReport } from './frame-media';
import {
  buildFrameOffers,
  displayOf,
  elementSendKey,
  fileMatches,
  itemVisibleTo,
  PROTECTED_FRAME_PROBE,
  resolveOfferKey,
  streamEntry,
  streamGroup,
  tabFileEntries,
  tabGroupCount,
  tabStreamEntries,
  type FrameOffersInput,
} from './frame-offers';
import { type MediaItem, itemKey } from './media';
import type { FrameVideoMeta } from './offer-types';
import { failure } from './protocol';

let seq = 0;
function item(over: Partial<MediaItem> = {}): MediaItem {
  return {
    url: 'https://cdn.example/v/clip.mp4',
    format: 'file',
    firstSeen: 1,
    lastSeen: 1,
    frameId: 0,
    parentFrameId: -1,
    size: 48 * 1024 * 1024,
    reqType: 'media',
    short: false,
    segment: false,
    seq: ++seq,
    ...over,
  };
}

const stream = (over: Partial<MediaItem> = {}) =>
  item({ url: 'https://cdn.example/v/master.m3u8', format: 'hls', size: 0, reqType: 'xmlhttprequest', ...over });

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

const report = (frameId: number, videos: FrameVideoMeta[], documentId?: string): FrameReport => ({
  frameId,
  videos,
  at: 0,
  ...(documentId ? { documentId } : {}),
});

function input(over: Partial<FrameOffersInput> = {}): FrameOffersInput {
  return {
    sender: { frameId: 0 },
    src: '',
    mse: false,
    title: 'A page',
    topHost: 'site.example',
    items: [],
    reports: [],
    features: { streams: true, video: true },
    streamProbe: () => undefined,
    refused: () => false,
    addState: () => undefined,
    ...over,
  };
}

describe('keys bound to frames', () => {
  it('lets the top frame use every item of its tab, another frame only its own document', () => {
    const own = { frameId: 4, parentFrameId: 0, documentId: 'D4' };
    expect(itemVisibleTo(own, { frameId: 0 })).toBe(true);
    expect(itemVisibleTo(own, { frameId: 4, documentId: 'D4' })).toBe(true);
    expect(itemVisibleTo(own, { frameId: 4 })).toBe(true);
    expect(itemVisibleTo(own, { frameId: 4, documentId: 'OLD' })).toBe(false);
    expect(itemVisibleTo(own, { frameId: 5 })).toBe(false);
    expect(itemVisibleTo({ frameId: 0, parentFrameId: -1 }, { frameId: 4 })).toBe(false);
  });

  it('resolves keys for the frame that asks, never the top frame’s for a sub-frame', () => {
    const top = item();
    const sub = item({ url: 'https://cdn.example/e/1.mp4', frameId: 4, parentFrameId: 0, documentId: 'D4' });
    const subStream = stream({ frameId: 4, parentFrameId: 0, documentId: 'D4' });
    const seg = item({ url: 'https://cdn.example/hls/s1.mp4', segment: true });
    const items = [top, sub, subStream, seg];
    expect(resolveOfferKey(itemKey(top), items, [], { frameId: 0 })).toEqual({ kind: 'file', item: top });
    expect(resolveOfferKey(itemKey(sub), items, [], { frameId: 0 })).toEqual({ kind: 'file', item: sub });
    expect(resolveOfferKey(itemKey(subStream), items, [], { frameId: 4, documentId: 'D4' })).toEqual({ kind: 'stream', item: subStream });
    expect(resolveOfferKey(itemKey(top), items, [], { frameId: 4, documentId: 'D4' })).toBeUndefined();
    expect(resolveOfferKey(itemKey(sub), items, [], { frameId: 4, documentId: 'OTHER' })).toBeUndefined();
    expect(resolveOfferKey(itemKey(seg), items, [], { frameId: 0 })).toBeUndefined();
    // A file key never names a stream, nor a stream key a file.
    expect(resolveOfferKey(`s${top.seq}`, items, [], { frameId: 0 })).toBeUndefined();
    expect(resolveOfferKey(`f${subStream.seq}`, items, [], { frameId: 0 })).toBeUndefined();
    for (const bad of ['', 'x1', 'f', 'f1234567890123', 'F1', 'f-1', 'f1 ']) {
      expect(resolveOfferKey(bad, items, [], { frameId: 0 })).toBeUndefined();
    }
  });

  it('resolves an element key to its own frame’s element, or to a recorded file playing it', () => {
    const src = 'https://cdn.example/unseen/clip.mp4?x=1';
    const reports = [report(4, [video(7, { src }), video(8, { src: 'https://cdn.example/a.webm' })], 'D4')];
    expect(resolveOfferKey('e7', [], reports, { frameId: 4, documentId: 'D4' })).toEqual({
      kind: 'element',
      video: reports[0]?.videos[0],
      sendKey: elementSendKey('e7', 4),
    });
    expect(resolveOfferKey('e7', [], reports, { frameId: 0 })).toBeUndefined();
    expect(resolveOfferKey('e7', [], reports, { frameId: 4, documentId: 'OLD' })).toBeUndefined();
    expect(resolveOfferKey('e8', [], reports, { frameId: 4 })).toBeUndefined();
    expect(resolveOfferKey('e9', [], reports, { frameId: 4 })).toBeUndefined();
    const seen = item({ url: 'https://cdn2.example/final.mp4', requestUrl: src, frameId: 4, parentFrameId: 0 });
    expect(resolveOfferKey('e7', [seen], reports, { frameId: 4 })).toEqual({ kind: 'file', item: seen });
  });
});

describe('buildFrameOffers', () => {
  it('lists the frame’s files with the clicked element first, its size, name and element facts', () => {
    const playing = item({ url: 'https://cdn.example/v/clip.mp4?sig=1', requestUrl: 'https://site.example/v/clip' });
    const other = item({ url: 'https://cdn.example/v/big.mp4', size: 900 * 1024 * 1024 });
    const small = item({ url: 'https://cdn.example/v/teaser.mp4', size: 1000, short: true });
    const seg = item({ url: 'https://cdn.example/hls/1.mp4', segment: true });
    const offers = buildFrameOffers(
      input({
        src: 'https://site.example/v/clip',
        items: [small, other, playing, seg],
        reports: [report(0, [video(1, { src: 'https://site.example/v/clip', videoHeight: 1080, videoWidth: 1920, durationMs: 90_000 })])],
        addState: (k) => (k === itemKey(other) ? { running: true } : undefined),
      }),
    );
    expect(offers.files.map((f) => f.key)).toEqual([itemKey(playing), itemKey(other), itemKey(small)]);
    expect(offers.files[0]).toEqual({
      key: itemKey(playing),
      display: 'cdn.example/clip.mp4',
      size: 48 * 1024 * 1024,
      sizeKnown: true,
      filename: 'clip.mp4',
      frame: 'this',
      matched: true,
      short: false,
      width: 1920,
      height: 1080,
      durationMs: 90_000,
    });
    expect(offers.files[1]).toMatchObject({ matched: false, width: 0, height: 0, durationMs: -1, add: { running: true } });
    expect(offers.files[2]).toMatchObject({ short: true });
    expect(JSON.stringify(offers)).not.toContain('sig=1');
    expect(offers).toMatchObject({ title: 'A page', topHost: 'site.example', drm: false, live: false, features: { streams: true, video: true } });
  });

  it('lists a file taken for a segment when an element plays it', () => {
    const clip = item({ url: 'https://cdn.example/videos/clip5.mp4', segment: true });
    const reports = [report(0, [video(1, { src: clip.url })])];
    expect(buildFrameOffers(input({ items: [clip] })).files).toEqual([]);
    expect(buildFrameOffers(input({ items: [clip], reports })).files.map((f) => f.key)).toEqual([itemKey(clip)]);
    expect(tabFileEntries([clip], reports, '', () => undefined).map((f) => f.key)).toEqual([itemKey(clip)]);
    expect(tabGroupCount({ items: [clip], pageVideos: new Map(), reports, refused: () => false })).toBe(1);
    expect(resolveOfferKey(itemKey(clip), [clip], reports, { frameId: 0 })).toEqual({ kind: 'file', item: clip });
    expect(resolveOfferKey(itemKey(clip), [clip], [], { frameId: 0 })).toBeUndefined();
  });

  it('treats a file its element plays for under 30 s as short', () => {
    const ad = item({ url: 'https://ads.example/pre.mp4' });
    const offers = buildFrameOffers(input({ items: [ad], reports: [report(0, [video(1, { src: ad.url, durationMs: 15_000 })])] }));
    expect(offers.files[0]).toMatchObject({ short: true, durationMs: 15_000 });
  });

  it('gives every frame only its own items: the page can read what its panel shows', () => {
    const top = item();
    const sub = item({ url: 'https://player.example/v.mp4', frameId: 4, parentFrameId: 0 });
    const subStream = stream({ frameId: 4, parentFrameId: 0, lastSeen: 5 });
    const topStream = stream({ url: 'https://cdn.example/top.mpd', format: 'dash', lastSeen: 2 });
    const all = [top, sub, subStream, topStream];
    const fromTop = buildFrameOffers(input({ items: all }));
    expect(fromTop.files.map((f) => [f.key, f.frame])).toEqual([[itemKey(top), 'this']]);
    expect(fromTop.streams.map((s) => [s.key, s.frame, s.format])).toEqual([[itemKey(topStream), 'this', 'dash']]);
    expect(fromTop).toMatchObject({ title: 'A page', topHost: 'site.example' });
    const fromSub = buildFrameOffers(input({ sender: { frameId: 4 }, items: all, mse: true }));
    expect(fromSub.files.map((f) => f.key)).toEqual([itemKey(sub)]);
    expect(fromSub.streams).toEqual([{ key: itemKey(subStream), format: 'hls', display: 'cdn.example/master.m3u8', group: streamGroup(subStream.url), frame: 'this', matched: true }]);
    // A stream's group never carries its address.
    expect(fromSub.streams[0]?.group).not.toContain('cdn.example');
    expect(streamGroup('https://cdn.example/v/abc_720w.m3u8')).toBe(streamGroup('https://cdn.example/v/abc.m3u8'));
    expect(streamGroup('https://cdn.example/v/abc_720w.m3u8')).not.toBe(streamGroup('https://cdn.example/v/abd_720w.m3u8'));
    expect(streamGroup('https://cdn.example/a/master.m3u8')).not.toBe(streamGroup('https://cdn.example/b/master.m3u8'));
    // A sub-frame (an ad network's, a third-party player's) learns neither the tab's title nor
    // its host, and its files are not named after the tab.
    expect(fromSub).toMatchObject({ title: '', topHost: '' });
    expect(fromSub.files[0]!.filename).toBe('v.mp4');
    const unnamed = buildFrameOffers(input({ sender: { frameId: 4 }, items: [item({ url: 'https://player.example/play', frameId: 4, parentFrameId: 0 })] }));
    expect(unnamed.files[0]!.filename).toBe('video.mp4');
  });

  it('leaves out refused and hidden streams, and passes probe and add states', () => {
    const ok = stream();
    const drm = stream({ url: 'https://cdn.example/drm.m3u8' });
    const google = stream({ url: 'https://manifest.googlevideo.com/x.m3u8' });
    const offers = buildFrameOffers(
      input({
        items: [ok, drm, google],
        refused: (i) => i === drm,
        hideStream: (i) => i === google,
        streamProbe: (i) => (i === ok ? { running: true } : undefined),
        addState: (k) => (k === itemKey(ok) ? { result: failure('rejected', 'live') } : undefined),
      }),
    );
    expect(offers.streams).toEqual([
      expect.objectContaining({ key: itemKey(ok), probe: { running: true }, add: { result: failure('rejected', 'live') } }),
    ]);
  });

  it('offers the clicked element’s MP4 address when no response of it was seen', () => {
    const src = 'https://cdn.example/cached/My%20Trip.mp4';
    const reports = [report(3, [video(9, { src, durationMs: 200_000, videoHeight: 480, videoWidth: 854 })], 'D3')];
    const offers = buildFrameOffers(
      input({
        sender: { frameId: 3, documentId: 'D3' },
        src,
        reports,
        addState: (k) => (k === elementSendKey('e9', 3) ? { running: true } : undefined),
      }),
    );
    expect(offers.files).toEqual([
      {
        key: 'e9',
        display: 'cdn.example/My Trip.mp4',
        size: 0,
        sizeKnown: false,
        filename: 'My Trip.mp4',
        frame: 'this',
        matched: true,
        short: false,
        width: 854,
        height: 480,
        durationMs: 200_000,
        add: { running: true },
      },
    ]);
    // Not for another container, nor when the element was not reported.
    expect(buildFrameOffers(input({ sender: { frameId: 3 }, src: 'https://cdn.example/a.webm', reports })).files).toEqual([]);
    expect(buildFrameOffers(input({ sender: { frameId: 3 }, src: 'https://cdn.example/b.mp4', reports })).files).toEqual([]);
    // Nor for an address whose response was seen and refused (a WebM named .mp4).
    expect(buildFrameOffers(input({ sender: { frameId: 3, documentId: 'D3' }, src, reports, rejected: (u) => u === src })).files).toEqual([]);
    expect(resolveOfferKey('e9', [], reports, { frameId: 3, documentId: 'D3' }, (u) => u === src)).toBeUndefined();
    // A recorded file playing it is offered instead.
    const seen = item({ url: src, frameId: 3, parentFrameId: 0 });
    expect(buildFrameOffers(input({ sender: { frameId: 3 }, src, reports, items: [seen] })).files.map((f) => f.key)).toEqual([itemKey(seen)]);
  });

  it('says whether the clicked element is protected or live', () => {
    const reports = [report(0, [video(1, { src: 'https://a.example/x.mp4', drm: true }), video(2, { mse: true, durationMs: -2 })])];
    expect(buildFrameOffers(input({ src: 'https://a.example/x.mp4', reports }))).toMatchObject({ drm: true, live: false });
    expect(buildFrameOffers(input({ mse: true, reports }))).toMatchObject({ drm: false, live: true });
    expect(buildFrameOffers(input({ src: 'https://a.example/other.mp4', reports }))).toMatchObject({ drm: false, live: false });
  });

  it('takes an MSE element as live once Downex refused every stream of its frame as live', () => {
    // hls.js gives a live stream a finite, growing duration: the element itself does not say so.
    const a = stream({ url: 'https://cdn.example/live/a.m3u8' });
    const b = stream({ url: 'https://cdn.example/live/b.m3u8' });
    const live = { result: failure('rejected', 'live: the stream has no end', 'live') };
    const reports = [report(0, [video(1, { mse: true, durationMs: 3_600_000 })])];
    const probe = (states: Map<MediaItem, unknown>) => (i: MediaItem) => states.get(i) as never;
    expect(buildFrameOffers(input({ mse: true, reports, items: [a, b], streamProbe: probe(new Map([[a, live], [b, live]])) }))).toMatchObject({
      live: true,
    });
    // One stream not asked yet, or listed: not (yet) live.
    expect(buildFrameOffers(input({ mse: true, reports, items: [a, b], streamProbe: probe(new Map([[a, live]])) })).live).toBe(false);
    // A file element is not judged by the frame's streams.
    expect(buildFrameOffers(input({ src: 'https://a.example/x.mp4', reports, items: [a], streamProbe: probe(new Map([[a, live]])) })).live).toBe(false);
  });

  it('carries the site source', () => {
    const site = { key: 'vimeo:1', site: 'Vimeo', kind: 'watch' as const, pageUrl: 'https://vimeo.com/1', curated: true };
    expect(buildFrameOffers(input({ site })).site).toEqual(site);
    expect(buildFrameOffers(input())).not.toHaveProperty('site');
  });
});

describe('matching and display', () => {
  it('matches an element to the first or the final URL of a file, fragments ignored', () => {
    const f = item({ url: 'https://cdn.example/x.mp4?sig=1', requestUrl: 'https://site.example/v/1' });
    expect(fileMatches(f, 'https://cdn.example/x.mp4?sig=1#t=3')).toBe(true);
    expect(fileMatches(f, 'https://site.example/v/1')).toBe(true);
    expect(fileMatches(f, 'https://cdn.example/x.mp4?sig=2')).toBe(false);
    expect(fileMatches(f, '')).toBe(false);
    expect(displayOf('https://cdn.example/a/b/%E2%82%AC.mp4?q=secret')).toBe('cdn.example/€.mp4');
    expect(displayOf('https://cdn.example/?q=1')).toBe('cdn.example');
    expect(displayOf('nope')).toBe('');
  });
});

describe('popup entries', () => {
  it('lists the tab’s files with their URL and frame, never segments', () => {
    const top = item({ url: 'https://cdn.example/a.mp4', filename: 'Holiday.mp4' });
    const sub = item({ url: 'https://player.example/b.mp4', frameId: 2, parentFrameId: 0, size: 0 });
    const seg = item({ url: 'https://cdn.example/hls/1.mp4', segment: true });
    const entries = tabFileEntries([top, sub, seg], [report(2, [video(1, { src: sub.url, durationMs: 4000 })])], 'T', (k) =>
      k === itemKey(top) ? { running: true } : undefined,
    );
    expect(entries).toEqual([
      expect.objectContaining({ key: itemKey(top), url: top.url, frame: 'top', filename: 'Holiday.mp4', matched: false, add: { running: true } }),
      expect.objectContaining({ key: itemKey(sub), url: sub.url, frame: 'embed', sizeKnown: false, matched: true, short: true, durationMs: 4000 }),
    ]);
  });

  it('marks the streams of protected frames and leaves out those of live frames', () => {
    const ok = stream();
    const drm = stream({ url: 'https://drm.example/a.mpd', format: 'dash', frameId: 2, parentFrameId: 0 });
    const live = stream({ url: 'https://live.example/a.m3u8', frameId: 3, parentFrameId: 0 });
    const liveFile = item({ url: 'https://live.example/a.mp4', frameId: 3, parentFrameId: 0 });
    const reports = [report(2, [video(1, { drm: true })]), report(3, [video(1, { durationMs: -2 })])];
    const probed = { result: failure('rejected', 'media_unsupported: x', 'media_unsupported') };
    const entries = tabStreamEntries([ok, drm, live], reports, () => ({}), (i) => (i === ok ? probed : undefined));
    expect(entries.map((e) => [e.url, e.probe])).toEqual([
      [ok.url, probed],
      [drm.url, PROTECTED_FRAME_PROBE],
    ]);
    expect(PROTECTED_FRAME_PROBE).toMatchObject({ result: { ok: false, error: { code: 'encrypted', reason: 'encrypted' } } });
    expect(tabFileEntries([liveFile], reports, 'T', () => undefined)).toEqual([]);
  });

  it('gives a stream entry its key, frame, send and probe state', () => {
    const s = stream({ frameId: 3, parentFrameId: 0, pageTitle: 'T', headers: [['Cookie', 'x']] });
    const e = streamEntry(s, { sending: true }, { running: true });
    expect(e).toEqual({
      url: s.url,
      format: 'hls',
      firstSeen: 1,
      lastSeen: 1,
      pageTitle: 'T',
      sending: true,
      key: itemKey(s),
      frame: 'embed',
      probe: { running: true },
    });
  });
});

describe('tabGroupCount', () => {
  const none = () => false;

  it('counts page videos, frames with streams and full files', () => {
    const items = [
      stream(),
      stream({ url: 'https://cdn.example/b.m3u8' }),
      stream({ frameId: 3, parentFrameId: 0 }),
      item(),
      item({ url: 'https://cdn.example/2.mp4' }),
      item({ url: 'https://cdn.example/short.mp4', short: true }),
      item({ url: 'https://cdn.example/seg.mp4', segment: true }),
    ];
    expect(tabGroupCount({ items, pageVideos: new Map(), reports: [], refused: none })).toBe(4);
    // A refused stream is not counted; a frame keeps counting while one of its streams is not refused.
    expect(tabGroupCount({ items, pageVideos: new Map(), reports: [], refused: (i) => i.frameId === 3 })).toBe(3);
  });

  it('lets a page video take in its frame’s items', () => {
    const items = [stream({ url: 'https://manifest.googlevideo.com/a.m3u8' }), item(), stream({ frameId: 5, parentFrameId: 0 })];
    const pageVideos = new Map([
      [0, 'youtube:aaaaaaaaaaa'],
      [7, 'youtube:aaaaaaaaaaa'],
      [5, 'vimeo:1'],
    ]);
    expect(tabGroupCount({ items, pageVideos, reports: [], refused: none })).toBe(2);
  });

  it('leaves out frames whose videos are all protected or all live, and files of short elements', () => {
    const items = [
      stream({ frameId: 2, parentFrameId: 0 }),
      item({ frameId: 4, parentFrameId: 0 }),
      item({ url: 'https://ads.example/pre.mp4' }),
    ];
    const reports = [
      report(2, [video(1, { drm: true })]),
      report(4, [video(1, { durationMs: -2 })]),
      report(0, [video(1, { src: 'https://ads.example/pre.mp4', durationMs: 10_000 })]),
    ];
    expect(tabGroupCount({ items, pageVideos: new Map([[2, 'vimeo:1']]), reports, refused: none })).toBe(0);
    expect(tabGroupCount({ items, pageVideos: new Map(), reports: [], refused: none })).toBe(3);
  });
});

describe('the stream of the video playing now (VIDEO-DETECT-SPEC §2.6)', () => {
  it('marks only the streams whose segments the frame fetched last, among a grid\'s many', () => {
    const tile = (n: number, lastSeen: number) => [
      stream({ url: `https://media.example/hls/a${n}/b${n}/clip${n}_720w.m3u8`, lastSeen }),
      stream({ url: `https://media.example/hls/a${n}/b${n}/clip${n}_audio.m3u8`, lastSeen }),
    ];
    const seg = (n: number, i: number, lastSeen: number) =>
      item({ url: `https://media.example/hls/a${n}/b${n}/clip${n}_720w0000${i}.ts`, segment: true, size: 300_000, lastSeen });
    const items = [...tile(1, 100), ...tile(2, 200), ...tile(3, 300), seg(1, 1, 1_000), seg(1, 2, 2_000), seg(3, 1, -20_000)];
    const o = buildFrameOffers(input({ mse: true, items }));
    const matched = o.streams.filter((s) => s.matched).map((s) => s.display);
    expect(matched.sort()).toEqual(['media.example/clip1_720w.m3u8', 'media.example/clip1_audio.m3u8']);
    // No segment names a stream (another host's segments, or none): every stream, as before.
    const none = buildFrameOffers(input({ mse: true, items: [...tile(1, 100), ...tile(2, 200)] }));
    expect(none.streams.every((s) => s.matched)).toBe(true);
    const elsewhere = buildFrameOffers(input({ mse: true, items: [...tile(1, 100), ...tile(2, 200), item({ url: 'https://other.example/x/y/1.ts', segment: true })] }));
    expect(elsewhere.streams.every((s) => s.matched)).toBe(true);
  });

  it('takes the page\'s own word for where the segments fetched last are (TS segments are no recorded files)', () => {
    const items = [
      stream({ url: 'https://video.example/watch/a/CIDA/playlist.m3u8', lastSeen: 10 }),
      stream({ url: 'https://video.example/watch/a/CIDA/720p/video.m3u8', lastSeen: 11 }),
      stream({ url: 'https://video.example/watch/b/CIDB/playlist.m3u8', lastSeen: 20 }),
      stream({ url: 'https://video.example/watch/b/CIDB/720p/video.m3u8', lastSeen: 21 }),
    ];
    const o = buildFrameOffers(input({ mse: true, items, playingDirs: ['https://video.example/watch/a/CIDA/720p/'] }));
    expect(o.streams.filter((s) => s.matched).map((s) => s.key).sort()).toEqual([itemKey(items[0] as MediaItem), itemKey(items[1] as MediaItem)].sort());
  });

  it('takes a master playlist above its variants\' folders', () => {
    const items = [
      stream({ url: 'https://media.example/v/123/master.m3u8', lastSeen: 10 }),
      stream({ url: 'https://media.example/v/456/master.m3u8', lastSeen: 20 }),
      item({ url: 'https://media.example/v/123/720p/seg1.ts', segment: true, size: 300_000, lastSeen: 50 }),
    ];
    const o = buildFrameOffers(input({ mse: true, items }));
    expect(o.streams.filter((s) => s.matched).map((s) => s.display)).toEqual(['media.example/master.m3u8']);
    expect(o.streams.find((s) => s.matched)?.key).toBe(itemKey(items[0] as MediaItem));
  });
});
