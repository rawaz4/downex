// @vitest-environment happy-dom
import { describe, expect, it, vi } from 'vitest';
import {
  CANDIDATE_WINDOW_MS,
  candidates,
  isPlayedTrace,
  isYouTubeHost,
  looksLikeMp4,
  MAX_CANDIDATES,
  playedAddressOk,
  playedTitle,
  playingSegments,
  type PlayedResource,
  type PlayedTrace,
  playedName,
  playedNameOver,
  authorText,
  postContainer,
  profileLink,
  playedResponse,
  rangeInQuery,
  recentMedia,
  sameOrigin,
} from './played';
import { backgroundHead, type FetchLike, type HeadTry, mediaAnswer, PlayedHandoff, playedHeadReader, playsAnother, type FileHead, type PlayedClient } from './played-handoff';
import type { Outcome, VideoInfoResult } from './protocol';

describe('a byte range in the query (SITE-MEDIA-SPEC §2.9)', () => {
  it('folds a start/end pair or an a-b value whose length is the piece\'s', () => {
    expect(rangeInQuery('https://cdn.example/v/a.mp4?x=1&bytestart=1000&byteend=1999&y=2', 1000)).toBe('https://cdn.example/v/a.mp4?x=1&y=2');
    expect(rangeInQuery('https://cdn.example/v/a.mp4?bytestart=0&byteend=0', 1)).toBe('https://cdn.example/v/a.mp4');
    expect(rangeInQuery('https://cdn.example/v/a.mp4?id=5&bytes=100-199', 100)).toBe('https://cdn.example/v/a.mp4?id=5');
    // A number of the query that happens to fit never wins over the pair.
    expect(rangeInQuery('https://cdn.example/v/a.mp4?oh=1&bytestart=1000&byteend=1999', 1000)).toBe('https://cdn.example/v/a.mp4?oh=1');
  });

  it('leaves other queries alone', () => {
    expect(rangeInQuery('https://cdn.example/v/a.mp4?bytestart=0&byteend=999', 999)).toBeUndefined();
    expect(rangeInQuery('https://cdn.example/v/a.mp4?n=1000', 1000)).toBeUndefined();
    expect(rangeInQuery('https://cdn.example/v/a.mp4?a=x&b=y', 1)).toBeUndefined();
    expect(rangeInQuery(`https://cdn.example/v/a.mp4?s=0&e=${2 ** 40}`, 2 ** 40 + 1)).toBeUndefined();
    expect(rangeInQuery('https://cdn.example/v/a.mp4?r=5-1', 5)).toBeUndefined();
    expect(rangeInQuery('not a url', 5)).toBeUndefined();
    expect(rangeInQuery('https://cdn.example/v/a.mp4?s=0&e=9', 0)).toBeUndefined();
  });
});

describe('candidates', () => {
  const res = (key: string, lastAt: number, total = 0): PlayedResource => ({ key, total, kind: 'video', frameId: 0, firstAt: lastAt, lastAt });
  it('takes recent files, newest first, without small ones, then the page\'s own timing, at most 16', () => {
    const now = 100 * 60_000;
    const list = [res('https://cdn.example/old', now - CANDIDATE_WINDOW_MS - 1), res('https://cdn.example/a', now - 5), res('https://cdn.example/b', now - 1), res('https://cdn.example/ad', now, 10_000)];
    expect(candidates(list, ['https://cdn.example/a', 'https://cdn.example/t'], now).map((c) => c.key)).toEqual([
      'https://cdn.example/b',
      'https://cdn.example/a',
      'https://cdn.example/t',
    ]);
    const many = Array.from({ length: 30 }, (_, i) => res(`https://cdn.example/${i}`, now - i));
    expect(candidates(many, [], now)).toHaveLength(MAX_CANDIDATES);
  });

  it('says which candidates the browser answered as media, and never takes a refused host', () => {
    const now = 100 * 60_000;
    const list = [res('https://cdn.example/a.mp4', now - 1), res('https://rr1---sn-x.googlevideo.com/videoplayback', now - 2)];
    expect(candidates(list, ['https://cdn.example/t.mp4', 'http://192.168.1.10/cgi-bin/video.mp4', 'http://127.0.0.1:8080/admin/v.mp4'], now)).toEqual([
      { key: 'https://cdn.example/a.mp4', total: 0, kind: 'video', logged: true, direct: false },
      { key: 'https://cdn.example/t.mp4', total: 0, kind: '', logged: false, direct: false },
    ]);
  });

  it('knows the hosts Downex never saves a played file from (internal/played.CheckURL)', () => {
    for (const ok of ['https://cdn.example/v.mp4', 'http://203.0.113.9/v.mp4', 'https://[2606:4700::1111]/v.mp4', 'https://media.example.com./v.mp4', 'http://100.128.0.1/v.mp4', 'http://198.20.0.1/v.mp4']) {
      expect(playedAddressOk(ok)).toBe(true);
    }
    for (const bad of [
      'https://www.youtube.com/watch?v=x',
      'https://i.ytimg.com/vi/x/0.jpg',
      'https://rr1---sn-x.googlevideo.com/videoplayback',
      'http://localhost:3000/v.mp4',
      'http://app.localhost/v.mp4',
      'http://127.0.0.1/v.mp4',
      'http://10.1.2.3/v.mp4',
      'http://172.20.0.1/v.mp4',
      'http://192.168.0.10/v.mp4',
      'http://169.254.1.1/v.mp4',
      'http://0.0.0.0/v.mp4',
      'http://[::1]/v.mp4',
      'http://[fe80::1]/v.mp4',
      'http://[fd00::1]/v.mp4',
      'http://[::ffff:192.168.0.1]/v.mp4',
      'http://100.64.0.1/v.mp4',
      'http://100.100.100.100/v.mp4',
      'http://198.18.0.1/v.mp4',
      'http://192.0.0.8/v.mp4',
      'http://240.0.0.1/v.mp4',
      'http://[64:ff9b::7f00:1]/v.mp4',
      'http://[2002:c0a8:101::1]/v.mp4',
      'http://[fec0::1]/v.mp4',
      'http://[::127.0.0.1]/v.mp4',
      'http://[2001::1]/v.mp4',
      'http://[2001:db8::1]/v.mp4',
      'http://[::ffff:100.64.0.1]/v.mp4',
      'ftp://cdn.example/v.mp4',
      'not an address',
    ]) {
      expect(playedAddressOk(bad), bad).toBe(false);
    }
  });
});

describe('the clicked element (§2.9 "The flow" 2)', () => {
  const trace: PlayedTrace = { element: '1-a', durationMs: 19_000, others: [], width: 720, height: 1280, audio: 1, recent: ['https://cdn.example/v.mp4'], segments: [], name: 'a - b', href: 'https://www.social.example/feed' };
  it('validates a trace', () => {
    expect(isPlayedTrace(trace)).toBe(true);
    for (const bad of [
      { ...trace, durationMs: -1 },
      { ...trace, durationMs: 1.5 },
      { ...trace, others: [0] },
      { ...trace, others: Array(9).fill(5_000) },
      { ...trace, others: undefined },
      { ...trace, element: 'x y' },
      { ...trace, width: 20_000 },
      { ...trace, audio: 2 },
      { ...trace, recent: ['javascript:alert(1)'] },
      { ...trace, recent: Array(25).fill('https://cdn.example/v.mp4') },
      { ...trace, recent: ['https://cdn.example/v.mp4#x'] },
      { ...trace, name: 'x'.repeat(201) },
      { ...trace, href: 7 },
      null,
    ]) {
      expect(isPlayedTrace(bad)).toBe(false);
    }
  });

  it('reads the page\'s media timing entries, newest first, never a stream segment', () => {
    expect(
      recentMedia([
        { name: 'https://cdn.example/app.js', initiatorType: 'script' },
        { name: 'https://cdn.example/v/a.mp4?x=1', initiatorType: 'fetch' },
        { name: 'https://cdn.example/seg/1.m4s', initiatorType: 'xmlhttprequest' },
        { name: 'https://video.cdn.example/obj/123', initiatorType: 'video' },
        { name: 'https://cdn.example/img/1.jpg', initiatorType: 'img' },
        { name: 'https://cdn.example/api/feed', initiatorType: 'fetch' },
      ]),
    ).toEqual(['https://video.cdn.example/obj/123', 'https://cdn.example/v/a.mp4?x=1']);
  });

  it('says where the segments fetched last are (the stream playing now)', () => {
    // Measured 2026-11-18 (a feed of HLS posts): the previous post's segments 50 s before the playing one's.
    expect(
      playingSegments([
        { name: 'https://video.example/watch/a/720p/video0.ts?session=1', initiatorType: 'xmlhttprequest', startTime: 27_082 },
        { name: 'https://video.example/watch/b/playlist.m3u8', initiatorType: 'xmlhttprequest', startTime: 43_789 },
        { name: 'https://video.example/watch/b/720p/video0.ts?session=2', initiatorType: 'xmlhttprequest', startTime: 43_800 },
        { name: 'https://video.example/watch/b/720p/video6.ts?session=2', initiatorType: 'xmlhttprequest', startTime: 77_391 },
        { name: 'https://video.example/watch/b/audio/a6.m4s', initiatorType: 'xmlhttprequest', startTime: 77_000 },
        { name: 'https://cdn.example/app.js', initiatorType: 'script', startTime: 80_000 },
      ]),
    ).toEqual(['https://video.example/watch/b/720p/', 'https://video.example/watch/b/audio/']);
    expect(playingSegments([{ name: 'https://cdn.example/v/a.mp4', initiatorType: 'fetch', startTime: 5 }])).toEqual([]);
  });

  it('never takes the site\'s own API or logging calls for media, whatever words their addresses have', () => {
    expect(
      recentMedia([
        { name: 'https://www.social.example/api/video/view/?id=7', initiatorType: 'fetch' },
        { name: 'https://log.social.example/collect?event=video_play&dur=15', initiatorType: 'xmlhttprequest' },
        { name: 'https://www.social.example/api/user/videos/list?cursor=0', initiatorType: 'fetch' },
        { name: 'https://audio.social.example/mp4/obj/9', initiatorType: 'other' },
        { name: 'http://192.168.1.10/cgi-bin/video.mp4', initiatorType: 'video' },
        { name: 'https://rr1---sn-x.googlevideo.com/videoplayback?mime=video%2Fmp4', initiatorType: 'video' },
      ]),
    ).toEqual([]);
  });

  it('names a played video by its post\'s author and caption, never a player time', () => {
    document.body.innerHTML = `
      <article>
        <div><video id="v"></video><span>00:04 / 00:05</span></div>
        <a href="https://www.social.example/@someone">@someone</a>
        <button>Follow this account now</button>
        <p>A sunset at the beach with the cats</p>
      </article>
      <article><video></video></article>`;
    const v = document.getElementById('v') as HTMLVideoElement;
    const box = postContainer(v);
    expect(box?.tagName).toBe('ARTICLE');
    expect(playedName(box, (h) => profileLink(h, 'https://www.social.example/feed'))).toBe('someone - A sunset at the beach with the cats');
    expect(playedName(undefined, () => false)).toBe('');
    // A tile in a page section of many links: the walk stops below it, never naming the page's menu.
    document.body.innerHTML = `<nav>${'<a href="/x">menu entry text</a>'.repeat(70)}<div id="tile"><video id="p"></video></div></nav>`;
    expect(postContainer(document.getElementById('p') as Element)?.id).toBe('tile');
  });

  it('never names a video by the page’s menus or footer (TikTok’s single-video page)', () => {
    document.body.innerHTML = `
      <div id="app">
        <div class="side"><a href="https://www.tiktok.com/explore">Explore</a><a href="https://www.tiktok.com/following">Following</a>
          <div class="foot"><footer><a href="https://www.tiktok.com/legal/terms">Terms &amp; Policies</a></footer></div></div>
        <div class="main"><div class="player"><video id="v"></video></div></div>
      </div>`;
    const v = document.getElementById('v') as HTMLVideoElement;
    const profile = (h: string) => profileLink(h, 'https://www.tiktok.com/@a/video/1');
    expect(profileLink('https://www.tiktok.com/explore', 'https://www.tiktok.com/@a/video/1')).toBe(false);
    expect(profileLink('https://www.tiktok.com/@someone', 'https://www.tiktok.com/@a/video/1')).toBe(true);
    expect(postContainer(v)?.className).toBe('main');
    expect(playedName(postContainer(v), profile)).toBe('');
    // Even a container that reaches the app root reads nothing from its menus or footer.
    expect(playedName(document.getElementById('app') as Element, profile)).toBe('');
  });

  it('titles a played video by its post, else its own page\'s title without the site\'s parts', () => {
    expect(playedTitle('someone - a caption', 'Watch trending videos for you | Social', undefined)).toBe('someone - a caption');
    // A feed's own title never names the clicked video.
    expect(playedTitle('', 'Watch trending videos for you | Social', undefined)).toBe('');
    expect(playedTitle('', 'A walk by the sea | Social', 'Social')).toBe('A walk by the sea');
    expect(playedTitle('', '(3) 4.6K views · 38K reactions | The trend - a day out | Example Page', 'Facebook')).toBe('The trend - a day out | Example Page');
    expect(playedTitle('', 'Instagram', 'Instagram')).toBe('');
    expect(playedTitle('', 'Cats - funny • Instagram', 'Instagram')).toBe('Cats - funny');
  });

  it('never names a video by a dialog over its post, and can name it by its author alone', () => {
    document.body.innerHTML = `
      <main>
        <div><video id="v"></video></div>
        <a href="https://www.social.example/someone/">someone</a>
        <div role="dialog"><p>Never miss a post from someone</p></div>
        <p>Original audio by someone</p>
      </main>`;
    const main = document.querySelector('main') as Element;
    const profile = (h: string) => profileLink(h, 'https://www.social.example/reel/x/');
    expect(playedName(main, profile)).toBe('someone - Original audio by someone');
    expect(playedName(main, profile, { authorOnly: true })).toBe('someone');
  });

  it('names a feed video whose author and caption lie in a layer over it, beside its own empty box (review 2026-11-18)', () => {
    // Trimmed from a short-video feed with its comments closed: the item's own box holds only the
    // video; its author and caption are in a sibling layer, under a column that also holds the next
    // item's video and that item's own layer.
    document.body.innerHTML = `
      <div id="column">
        <article id="a1"><div><video id="v1"></video></div></article>
        <div id="layer1"><a id="au1" href="https://www.social.example/@first.author">first.author</a><span id="cap1">What a lovely day at the lake</span></div>
        <article id="a2"><div><video id="v2"></video></div></article>
        <div id="layer2"><a id="au2" href="https://www.social.example/@second.author">second.author</a><span id="cap2">Another clip that is not this one</span></div>
      </div>`;
    const box = (id: string, top: number, h: number) =>
      ((document.getElementById(id) as Element).getBoundingClientRect = () => ({ left: 10, top, width: 380, height: h, right: 390, bottom: top + h, x: 10, y: top, toJSON: () => ({}) }) as DOMRect);
    box('v1', 0, 700);
    box('au1', 600, 20);
    box('cap1', 630, 20);
    box('v2', 720, 700);
    box('au2', 1320, 20);
    box('cap2', 1350, 20);
    const v1 = document.getElementById('v1') as HTMLVideoElement;
    const profile = (h: string) => profileLink(h, 'https://www.social.example/foryou');
    expect(playedName(postContainer(v1), profile)).toBe('');
    expect(playedNameOver(v1, profile)).toBe('first.author - What a lovely day at the lake');
    expect(playedNameOver(document.getElementById('v2') as HTMLVideoElement, profile)).toBe('second.author - Another clip that is not this one');
  });

  it('reads an author link of a display name and a handle with a space, without the handle (review 2026-11-18)', () => {
    document.body.innerHTML = `<a id="a" href="https://www.social.example/profile/mayor.example"><span>Mayor of a City</span><span>@mayor.example</span></a><a id="b" href="https://www.social.example/@only.handle">@only.handle</a>`;
    expect(authorText(document.getElementById('a') as Element)).toBe('Mayor of a City');
    expect(authorText(document.getElementById('b') as Element)).toBe('only.handle');
  });

  it('knows profiles, origins, MP4 heads and YouTube', () => {
    expect(profileLink('https://www.social.example/@a.b', 'https://www.social.example/x')).toBe(true);
    expect(profileLink('https://www.social.example/user/abc/', 'https://www.social.example/x')).toBe(true);
    expect(profileLink('https://www.social.example/p/abc/def', 'https://www.social.example/x')).toBe(false);
    expect(profileLink('https://evil.example/@a', 'https://www.social.example/x')).toBe(false);
    expect(sameOrigin('https://a.example/x', 'https://a.example/y')).toBe(true);
    expect(sameOrigin('https://a.example/x', 'https://b.example/x')).toBe(false);
    expect(sameOrigin('javascript:1', 'javascript:1')).toBe(false);
    expect(looksLikeMp4(new Uint8Array([0, 0, 0, 24, 0x66, 0x74, 0x79, 0x70]))).toBe(true);
    expect(looksLikeMp4(new TextEncoder().encode('<!doctype html>'))).toBe(false);
    expect(isYouTubeHost('rr3---sn-x.googlevideo.com')).toBe(true);
    expect(isYouTubeHost('www.social.example')).toBe(false);
  });
});

describe('the Played hand-off (§2.9 "The flow" 3-6)', () => {
  const HEAD: FileHead = { head: new Uint8Array([0, 0, 0, 8, 0x66, 0x74, 0x79, 0x70]), total: 5_000_000, browser: false, include: false };
  const trace: PlayedTrace = { element: '1-a', durationMs: 19_000, others: [], width: 720, height: 1280, audio: 1, recent: [], segments: [], name: 'n', href: 'https://www.social.example/feed' };
  const click = { tabId: 3, frameId: 0, pageUrl: 'https://www.social.example/feed', trace };
  const info: VideoInfoResult = { type: 'videoInfo', site: 'social.example', title: '', durationMs: 19_000, format: 'http', container: 'mp4', selectedVideo: 'r1', selectedAudio: '', tracks: [] };

  function setup(answers: Outcome<VideoInfoResult>[]) {
    let files: PlayedResource[] = [{ key: 'https://cdn.example/a.mp4', total: 0, kind: 'video', frameId: 0, firstAt: 1, lastAt: 1 }];
    let now = 10;
    const client: PlayedClient = {
      probePlayed: vi.fn(async (): Promise<Outcome<VideoInfoResult>> => answers.shift() ?? answers.at(-1) ?? { ok: true, value: info }),
      addPlayed: vi.fn(async () => ({ ok: true as const, value: { type: 'added' as const, downloadId: 1, fetchTicket: '0123456789abcdef0123456789abcdef' } })),
    };
    const readHead = vi.fn(async (_t: number, _f: number, url: string) => (url.includes('page') ? undefined : { ...HEAD, include: url.includes('prime') }));
    const h = new PlayedHandoff({ client, resources: async () => files, readHead, now: () => now });
    return { h, client, readHead, setFiles: (f: PlayedResource[]) => (files = f), tick: (ms: number) => (now += ms) };
  }

  it('probes once, waits while nothing matched, probes again when other files played, and adds with the files\' addresses', async () => {
    const t = setup([{ ok: false, error: { code: 'rejected', message: 'not_found_yet: none', reason: 'not_found_yet' } }, { ok: true, value: info }]);
    expect((await t.h.state(click)).probe).toEqual({ running: true });
    await vi.waitFor(async () => expect((await t.h.state(click)).probe).toMatchObject({ result: { ok: false } }));
    // The same files: no second probe.
    await t.h.state(click);
    expect(t.client.probePlayed).toHaveBeenCalledTimes(1);
    t.setFiles([
      { key: 'https://cdn.example/a.mp4', total: 0, kind: 'video', frameId: 0, firstAt: 1, lastAt: 1 },
      { key: 'https://prime.cdn.example/b.mp4', total: 0, kind: 'audio', frameId: 0, firstAt: 2, lastAt: 2 },
    ]);
    await t.h.state(click);
    await vi.waitFor(async () => expect((await t.h.state(click)).probe).toMatchObject({ result: { ok: true } }));
    expect(t.client.probePlayed).toHaveBeenCalledTimes(2);
    // Heads are read once per file (cached).
    expect(t.readHead.mock.calls.map((c) => c[2])).toEqual(['https://cdn.example/a.mp4', 'https://prime.cdn.example/b.mp4']);
    const add = await t.h.add(click, { video: 'r1', audio: '' }, 'someone - a caption');
    expect(add.outcome.ok).toBe(true);
    expect(add.urls.sort()).toEqual(['https://cdn.example/a.mp4', 'https://prime.cdn.example/b.mp4']);
    expect(add.include).toEqual(['https://prime.cdn.example/b.mp4']);
    expect(t.client.addPlayed).toHaveBeenCalledWith(
      expect.objectContaining({ pageUrl: click.pageUrl, videoTrack: 'r1', title: 'someone - a caption' }),
      expect.anything(),
    );
  });

  it('says how long the panel waited, and asks nothing without files', async () => {
    const t = setup([]);
    t.setFiles([]);
    expect(await t.h.state(click)).toEqual({ waitingMs: 0 });
    t.tick(5_000);
    expect(await t.h.state(click)).toEqual({ waitingMs: 5_000 });
    expect(t.client.probePlayed).not.toHaveBeenCalled();
    // An add before Downex matched anything is refused, nothing sent.
    expect((await t.h.add(click, { video: '', audio: '' }, '')).outcome.ok).toBe(false);
    expect(t.client.addPlayed).not.toHaveBeenCalled();
  });

  it('probes another item of the same length in the same frame, and adds that item\'s own files', async () => {
    const t = setup([]);
    const itemA = { ...click, trace: { ...trace, element: '7-aaaa', durationMs: 15_000 } };
    const itemB = { ...click, trace: { ...trace, element: '7-bbbb', durationMs: 15_000 } };
    t.setFiles([{ key: 'https://cdn.example/item-a.mp4', total: 0, kind: 'video', frameId: 0, firstAt: 1, lastAt: 1 }]);
    await t.h.state(itemA);
    await vi.waitFor(async () => expect((await t.h.state(itemA)).probe).toMatchObject({ result: { ok: true } }));
    t.setFiles([
      { key: 'https://cdn.example/item-a.mp4', total: 0, kind: 'video', frameId: 0, firstAt: 1, lastAt: 1 },
      { key: 'https://cdn.example/item-b.mp4', total: 0, kind: 'video', frameId: 0, firstAt: 2, lastAt: 2 },
    ]);
    await t.h.state(itemB);
    await vi.waitFor(async () => expect((await t.h.state(itemB)).probe).toMatchObject({ result: { ok: true } }));
    expect(t.client.probePlayed).toHaveBeenCalledTimes(2);
    const add = await t.h.add(itemB, { video: '', audio: '' }, 'item B');
    expect(add.urls).toContain('https://cdn.example/item-b.mp4');
  });

  it('waits without asking while the element has no duration, and a panel listed then can still be saved', async () => {
    const t = setup([]);
    const unloaded = { ...click, trace: { ...trace, element: '9-cccc', durationMs: 0 } };
    expect(await t.h.state(unloaded)).toEqual({ waitingMs: 0 });
    expect(t.client.probePlayed).not.toHaveBeenCalled();
    // The element learnt its duration (the same element): probed now, and Download works.
    const loaded = { ...click, trace: { ...trace, element: '9-cccc', durationMs: 19_250 } };
    await t.h.state(loaded);
    await vi.waitFor(async () => expect((await t.h.state(loaded)).probe).toMatchObject({ result: { ok: true } }));
    const later = { ...click, trace: { ...trace, element: '9-cccc', durationMs: 19_500 } };
    expect((await t.h.add(later, { video: '', audio: '' }, '')).outcome.ok).toBe(true);
  });

  it('probes again when the same element plays another post (a feed that reuses its players), never answering with the last one', async () => {
    // Review 2026-11-18 (TikTok For You): one element played a 364.8 s item, then a 6.48 s one;
    // the element's identity stayed the same (no address, no post link).
    const t = setup([]);
    const long = { ...click, trace: { ...trace, element: '5-46bd25', durationMs: 364_767, width: 1280, height: 576 } };
    await t.h.state(long);
    await vi.waitFor(async () => expect((await t.h.state(long)).probe).toMatchObject({ result: { ok: true } }));
    const short = { ...click, trace: { ...trace, element: '5-46bd25', durationMs: 6_477, width: 576, height: 1024 } };
    const now = await t.h.state(short);
    expect(now.probe).toEqual({ running: true });
    await vi.waitFor(() => expect(t.client.probePlayed).toHaveBeenCalledTimes(2));
    expect(t.client.probePlayed).toHaveBeenLastCalledWith(expect.objectContaining({ played: expect.objectContaining({ durationMs: 6_477 }) }), expect.anything());
    // An add with the new post's trace before its probe answered never saves the last one.
    const t2 = setup([]);
    await t2.h.state(long);
    await vi.waitFor(async () => expect((await t2.h.state(long)).probe).toMatchObject({ result: { ok: true } }));
    expect((await t2.h.add(short, { video: '', audio: '' }, '')).outcome.ok).toBe(false);
    expect(t2.client.addPlayed).not.toHaveBeenCalled();
  });

  it('takes a length learnt or a quality switch for the same video, another length or shape for another', () => {
    const was = { durationMs: 19_250, width: 720, height: 1280 };
    expect(playsAnother(was, { durationMs: 19_500, width: 1080, height: 1920 })).toBe(false);
    expect(playsAnother({ ...was, durationMs: 0 }, was)).toBe(false);
    expect(playsAnother(was, { ...was, durationMs: 20_000 })).toBe(true);
    expect(playsAnother(was, { ...was, width: 1280, height: 720 })).toBe(true);
  });

  it('keeps a refusal that waiting does not change (protected, an older Downex)', async () => {
    const t = setup([{ ok: false, error: { code: 'outdated_downex', message: 'Update Downex to save this video.' } }]);
    await t.h.state(click);
    await vi.waitFor(async () => expect((await t.h.state(click)).probe).toMatchObject({ result: { ok: false } }));
    t.setFiles([{ key: 'https://cdn.example/c.mp4', total: 0, kind: 'video', frameId: 0, firstAt: 3, lastAt: 3 }]);
    await t.h.state(click);
    expect(t.client.probePlayed).toHaveBeenCalledTimes(1);
  });
});

describe('reading a played file\'s head (§2.9 "The flow" 4)', () => {
  const HEAD: FileHead = { head: new Uint8Array([0, 0, 0, 8, 0x66, 0x74, 0x79, 0x70]), total: 5_000_000, browser: true, include: false };
  function reader(answers: { background: HeadTry; omit: HeadTry; include: HeadTry }, onSite = true) {
    const calls: string[] = [];
    const read = playedHeadReader({
      background: async () => {
        calls.push('background');
        return answers.background;
      },
      page: async (_t, _f, _u, credentials) => {
        calls.push(credentials);
        return credentials === 'include' ? answers.include : answers.omit;
      },
      onSite: async () => onSite,
    });
    return { read, calls };
  }

  it('stops at an answer that is not media: the address is never asked again, never with credentials', async () => {
    const r = reader({ background: 'not-media', omit: undefined, include: HEAD });
    expect(await r.read(3, 0, 'https://www.social.example/api/user/videos/list?cursor=0', true, true)).toBeUndefined();
    expect(r.calls).toEqual(['background']);
    const p = reader({ background: undefined, omit: 'not-media', include: HEAD });
    expect(await p.read(3, 0, 'https://www.social.example/api/feed', true, true)).toBeUndefined();
    expect(p.calls).toEqual(['background', 'omit']);
  });

  it('sends the page\'s credentials only for a file the tab\'s player fetched as media, on the page\'s site', async () => {
    const timingOnly = reader({ background: undefined, omit: undefined, include: HEAD });
    expect(await timingOnly.read(3, 0, 'https://media.social.example/v/a.mp4', false, false)).toBeUndefined();
    expect(timingOnly.calls).toEqual(['omit']);
    const offSite = reader({ background: undefined, omit: undefined, include: HEAD }, false);
    expect(await offSite.read(3, 0, 'https://cdn.example/v/a.mp4', true, true)).toBeUndefined();
    expect(offSite.calls).toEqual(['background', 'omit']);
    const logged = reader({ background: undefined, omit: undefined, include: { ...HEAD, include: true } });
    expect(await logged.read(3, 0, 'https://media.social.example/v/a.mp4', true, true)).toMatchObject({ include: true });
    expect(logged.calls).toEqual(['background', 'omit', 'include']);
  });

  it('takes video, audio, an octet stream or no type for media, nothing else', () => {
    for (const t of ['video/mp4', 'audio/mp4; codecs="mp4a.40.2"', 'application/octet-stream', 'binary/octet-stream', '', undefined]) expect(mediaAnswer(t)).toBe(true);
    for (const t of ['application/json', 'text/html; charset=utf-8', 'image/jpeg', 'text/plain']) expect(mediaAnswer(t)).toBe(false);
  });
});

describe('the background’s own head read (§2.9 "The flow" 4)', () => {
  const mp4 = new Uint8Array([0, 0, 0, 8, 0x66, 0x74, 0x79, 0x70]);
  function answer(url: string, status: number, headers: Record<string, string>, redirected = false): Response {
    const r = new Response(mp4, { status, headers });
    Object.defineProperty(r, 'url', { value: url });
    Object.defineProperty(r, 'redirected', { value: redirected });
    return r;
  }

  it('follows a redirect to a CDN and takes the total from Content-Range', async () => {
    // Review 2026-11-18 (Instagram, Facebook): an unfollowed redirect was no answer at all.
    const fetchFn = vi.fn<FetchLike>(async () =>
      answer('https://video.cdn.example/v/a.mp4?x=1', 206, { 'content-type': 'video/mp4', 'content-range': 'bytes 0-65535/4188024' }, true),
    );
    const h = await backgroundHead(fetchFn, 'https://media.social.example/v/a.mp4');
    expect(h).toMatchObject({ total: 4_188_024, browser: false, include: false });
    expect(fetchFn.mock.calls[0]?.[1]).toMatchObject({ redirect: 'follow', credentials: 'omit' });
  });

  it('never reads an answer that ended on this computer, a local network or YouTube', async () => {
    for (const to of ['http://127.0.0.1:9471/media/lan.mp4', 'https://192.168.1.2/v.mp4', 'https://rr1---sn-x.googlevideo.com/videoplayback']) {
      const fetchFn = vi.fn<FetchLike>(async () => answer(to, 206, { 'content-type': 'video/mp4', 'content-range': 'bytes 0-65535/4188024' }, true));
      expect(await backgroundHead(fetchFn, 'https://media.social.example/v/a.mp4')).toBe('not-media');
    }
    const never = vi.fn<FetchLike>();
    expect(await backgroundHead(never, 'http://10.0.0.5/v.mp4')).toBe('not-media');
    expect(never).not.toHaveBeenCalled();
  });

  it('is never asked for a file the browser did not get from a public address', async () => {
    const calls: string[] = [];
    const read = playedHeadReader({
      background: async () => {
        calls.push('background');
        return undefined;
      },
      page: async (_t, _f, _u, credentials) => {
        calls.push(credentials);
        return undefined;
      },
      onSite: async () => false,
    });
    await read(3, 0, 'http://lan.example:9471/media/lan.mp4', true, false);
    expect(calls).toEqual(['omit']);
  });

  it('records no file a name led the browser to on this computer or a local network', () => {
    const base = { url: 'http://lan.example:9471/media/lan.mp4', method: 'GET', tabId: 4, statusCode: 200, responseHeaders: [{ name: 'Content-Type', value: 'video/mp4' }, { name: 'Content-Length', value: '2400000' }] };
    expect(playedResponse({ ...base, ip: '127.0.0.1' })).toBeUndefined();
    expect(playedResponse({ ...base, ip: '::1' })).toBeUndefined();
    expect(playedResponse({ ...base, ip: '192.168.0.10' })).toBeUndefined();
    expect(playedResponse({ ...base, ip: '93.184.215.14' })).toMatchObject({ direct: true });
    expect(playedResponse(base)).toMatchObject({ direct: false });
  });
});
