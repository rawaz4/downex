import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { HandoffRecord, StorageAreaLike } from './history';
import { HandoffHistory } from './history';
import type { VideoOptions } from './options-form';
import {
  type AddedResult,
  type AddVideoFields,
  failure,
  type Outcome,
  type ProbeVideoFields,
  type RequestOptions,
  type VideoInfoResult,
} from './protocol';
import {
  createVideoHandOff,
  findEntry,
  findPageVideos,
  frameVideo,
  keepProbe,
  MAX_SIGN_IN_COOKIE_BYTES,
  PageVideoCounts,
  probeKey,
  readSignIn,
  videoItemKey,
  senderTarget,
  tabTarget,
  tabVideoEntries,
  VIDEO_CACHE_SIZE,
  VIDEO_DEADLINE_MS,
  VIDEO_KEEP_MS,
  VIDEO_RETRY_LIMIT_MS,
  VIDEO_RETRY_MS,
  type VideoTarget,
  VideoProbes,
  videoHeaders,
} from './video-handoff';

const ID = 'aqz-KE-bpKQ';
const WATCH = `https://www.youtube.com/watch?v=${ID}`;
const EMBED = `https://www.youtube-nocookie.com/embed/${ID}?rel=0`;

const info = (title = 'A video'): VideoInfoResult => ({
  type: 'videoInfo',
  site: 'YouTube',
  title,
  durationMs: 1000,
  format: 'http',
  container: 'mp4',
  selectedVideo: 'v',
  selectedAudio: 'a',
  tracks: [],
});

const refused = (reason: string) => failure('rejected', `${reason}: x`, reason);

/** A promise and the function that settles it. */
function deferred<T>() {
  let resolve!: (v: T) => void;
  const promise = new Promise<T>((r) => (resolve = r));
  return { promise, resolve };
}

describe('VideoProbes', () => {
  it('joins a running probe and keeps its answer for 10 minutes', async () => {
    let now = 0;
    const probes = new VideoProbes({ now: () => now });
    const d = deferred<Outcome<VideoInfoResult>>();
    const start = vi.fn(() => d.promise);
    const a = probes.run('k', start);
    const b = probes.run('k', start);
    expect(probes.state('k')).toEqual({ running: true });
    d.resolve({ ok: true, value: info() });
    await expect(a).resolves.toMatchObject({ ok: true });
    await expect(b).resolves.toMatchObject({ ok: true });
    expect(start).toHaveBeenCalledTimes(1);
    expect(probes.state('k')).toEqual({ result: { ok: true, value: info() } });
    now = VIDEO_KEEP_MS - 1;
    await probes.run('k', start);
    expect(start).toHaveBeenCalledTimes(1);
    now += VIDEO_KEEP_MS;
    expect(probes.state('k')).toBeUndefined();
  });

  it('keeps refusals of the page, never failures that may pass', async () => {
    expect(keepProbe(refused('auth_required'))).toBe(true);
    expect(keepProbe(refused('media_unsupported'))).toBe(true);
    expect(keepProbe(refused('tools_installing'))).toBe(false);
    expect(keepProbe(refused('site_resolving'))).toBe(false);
    expect(keepProbe(refused('site_error'))).toBe(false);
    expect(keepProbe(refused('site_media_off'))).toBe(false);
    expect(keepProbe(failure('timeout', 'x'))).toBe(false);
    expect(keepProbe(failure('daemon_unavailable', 'x'))).toBe(false);
    const probes = new VideoProbes();
    const start = vi.fn(async () => refused('tools_installing'));
    await probes.run('k', start);
    await probes.run('k', start);
    expect(start).toHaveBeenCalledTimes(2);
  });

  it('never keeps an auth_required of a probe that carried the sign-in', async () => {
    // The user may sign in on the site and try again at once: only the daemon can tell.
    expect(keepProbe(refused('auth_required'), true)).toBe(false);
    expect(keepProbe(refused('media_unsupported'), true)).toBe(true);
    const probes = new VideoProbes();
    const start = vi.fn(async () => refused('auth_required'));
    const top: VideoTarget = { key: `youtube:${ID}`, pageUrl: WATCH, storeId: '', generic: false };
    await probes.run(probeKey(top, true), start);
    await probes.run(probeKey(top, true), start);
    expect(start).toHaveBeenCalledTimes(2);
    await probes.run(probeKey(top, false), start);
    await probes.run(probeKey(top, false), start);
    expect(start).toHaveBeenCalledTimes(3);
  });

  it('sends at most two probes at once', async () => {
    const probes = new VideoProbes();
    const pending = new Map<string, ReturnType<typeof deferred<Outcome<VideoInfoResult>>>>();
    const started: string[] = [];
    const run = (key: string) =>
      probes.run(key, () => {
        started.push(key);
        const d = deferred<Outcome<VideoInfoResult>>();
        pending.set(key, d);
        return d.promise;
      });
    const all = ['a', 'b', 'c', 'd'].map(run);
    await Promise.resolve();
    await Promise.resolve();
    expect(started).toEqual(['a', 'b']);
    expect(probes.active).toBe(2);
    pending.get('a')!.resolve({ ok: true, value: info() });
    await vi.waitFor(() => expect(started).toEqual(['a', 'b', 'c']));
    pending.get('b')!.resolve(refused('tools_installing'));
    await vi.waitFor(() => expect(started).toEqual(['a', 'b', 'c', 'd']));
    pending.get('c')!.resolve({ ok: true, value: info() });
    pending.get('d')!.resolve({ ok: true, value: info() });
    await Promise.all(all);
    expect(probes.active).toBe(0);
  });

  it('runs a look-ahead beside the user slots and lets a user probe join it', async () => {
    const probes = new VideoProbes();
    const held = ['a', 'b'].map((k) => {
      const d = deferred<Outcome<VideoInfoResult>>();
      return { d, p: probes.run(k, () => d.promise) };
    });
    await Promise.resolve();
    expect(probes.active).toBe(2);
    // Both user slots are taken: the look-ahead still starts, without taking one.
    const ahead = deferred<Outcome<VideoInfoResult>>();
    const startAhead = vi.fn(() => ahead.promise);
    const la = probes.lookAhead('k', startAhead);
    expect(la).toBeDefined();
    expect(startAhead).toHaveBeenCalledTimes(1);
    expect(probes.active).toBe(2);
    expect(probes.state('k')).toEqual({ running: true });
    // A second look-ahead of a running or kept page is not started.
    expect(probes.lookAhead('k', startAhead)).toBeUndefined();
    expect(probes.lookAhead('a', startAhead)).toBeUndefined();
    // The user's click joins the look-ahead: no probe of its own.
    const userStart = vi.fn(async (): Promise<Outcome<VideoInfoResult>> => ({ ok: true, value: info('own') }));
    const joined = probes.run('k', userStart);
    ahead.resolve({ ok: true, value: info('ahead') });
    await expect(la).resolves.toMatchObject({ ok: true, value: { title: 'ahead' } });
    await expect(joined).resolves.toMatchObject({ ok: true, value: { title: 'ahead' } });
    expect(userStart).not.toHaveBeenCalled();
    // Kept like a probe's answer: the next click is answered at once, a look-ahead is not sent.
    expect(probes.state('k')).toEqual({ result: { ok: true, value: info('ahead') } });
    await expect(probes.run('k', userStart)).resolves.toMatchObject({ value: { title: 'ahead' } });
    expect(probes.lookAhead('k', startAhead)).toBeUndefined();
    expect(userStart).not.toHaveBeenCalled();
    for (const h of held) h.d.resolve({ ok: true, value: info() });
    await Promise.all(held.map((h) => h.p));
    expect(probes.active).toBe(0);
  });

  it('asks Downex itself when the joined look-ahead got an answer it does not keep', async () => {
    const probes = new VideoProbes();
    // Answers only a look-ahead gets, and refusals YouTube gives look-aheads it throttles
    // (rate limit, bot check), which a click must not inherit for 10 minutes.
    for (const reason of ['tools_unavailable', 'tools_installing', 'site_resolving', 'site_unavailable', 'auth_required']) {
      const key = `k-${reason}`;
      const ahead = deferred<Outcome<VideoInfoResult>>();
      const la = probes.lookAhead(key, () => ahead.promise);
      const userStart = vi.fn(async (): Promise<Outcome<VideoInfoResult>> => ({ ok: true, value: info('own') }));
      // Two clicks (overlay and popup) join it and then share one probe of their own.
      const a = probes.run(key, userStart);
      const b = probes.run(key, userStart);
      ahead.resolve(refused(reason));
      await expect(la).resolves.toMatchObject({ ok: false, error: { reason } });
      await expect(a).resolves.toMatchObject({ ok: true, value: { title: 'own' } });
      await expect(b).resolves.toMatchObject({ ok: true, value: { title: 'own' } });
      expect(userStart).toHaveBeenCalledTimes(1);
    }
    for (const reason of ['site_unavailable', 'auth_required']) {
      probes.lookAhead(`alone-${reason}`, async () => refused(reason));
      await vi.waitFor(() => expect(probes.state(`alone-${reason}`)).toBeUndefined());
    }
    // A refusal of the page is the user's answer too.
    const ahead = deferred<Outcome<VideoInfoResult>>();
    probes.lookAhead('live', () => ahead.promise);
    const userStart = vi.fn(async (): Promise<Outcome<VideoInfoResult>> => ({ ok: true, value: info() }));
    const joined = probes.run('live', userStart);
    ahead.resolve(refused('live'));
    await expect(joined).resolves.toMatchObject({ ok: false, error: { reason: 'live' } });
    expect(userStart).not.toHaveBeenCalled();
  });

  it('drops the least recently used beyond 50 pages', async () => {
    const probes = new VideoProbes();
    for (let i = 0; i <= VIDEO_CACHE_SIZE; i++) await probes.run(`k${i}`, async () => ({ ok: true, value: info() }));
    expect(probes.state('k0')).toBeUndefined();
    expect(probes.state('k1')).toBeDefined();
    // Used again, an entry is the most recent one.
    await probes.run('k1', async () => failure('internal', 'not asked'));
    await probes.run('k51', async () => ({ ok: true, value: info() }));
    expect(probes.state('k1')).toBeDefined();
    expect(probes.state('k2')).toBeUndefined();
  });
});

describe('targets', () => {
  const tab = { id: 4, url: WATCH, title: 'A video - YouTube' };

  it('finds the tab video and embedded players once each, the top first', () => {
    const found = findPageVideos({ url: 'https://blog.example/post' }, [
      { frameId: 0, url: 'https://blog.example/post' },
      { frameId: 3, url: EMBED },
      { frameId: 4, url: `https://www.youtube.com/embed/${ID}` },
      { frameId: 5, url: 'https://www.youtube.com/embed/aaaaaaaaaaa' },
      { frameId: 6, url: 'https://ads.example/frame' },
    ], false);
    expect(found.map((f) => [f.frame, f.video.key])).toEqual([
      ['embed', `youtube:${ID}`],
      ['embed', 'youtube:aaaaaaaaaaa'],
    ]);
    expect(findPageVideos(tab, [{ frameId: 2, url: EMBED }], false).map((f) => f.frame)).toEqual(['top']);
  });

  it('accepts a content script asking for its own frame video', () => {
    expect(senderTarget({ url: WATCH, frameId: 0, tab }, `${WATCH}&t=4`)).toEqual({
      ok: true,
      value: { key: `youtube:${ID}`, pageUrl: WATCH, storeId: '', generic: false, title: 'A video - YouTube' },
    });
    // An embedded player: the Referer is what the browser's default policy sends it for the
    // tab's page: the origin only, across origins (never the path, query or fragment).
    expect(
      senderTarget({ url: EMBED, frameId: 7, tab: { id: 4, url: 'https://blog.example/post?q=secret#token', cookieStoreId: 'firefox-container-2' } }, EMBED),
    ).toEqual({
      ok: true,
      value: { key: `youtube:${ID}`, pageUrl: WATCH, storeId: 'firefox-container-2', generic: false, referer: 'https://blog.example/' },
    });
    expect(senderTarget({ url: EMBED, frameId: 7, tab: { id: 4, url: 'about:blank' } }, EMBED)).toMatchObject({ ok: true, value: { generic: false } });
    expect((senderTarget({ url: EMBED, frameId: 7, tab: { id: 4, url: 'about:blank' } }, EMBED) as { value: VideoTarget }).value.referer).toBeUndefined();
    // Chromium's sender.origin wins over the URL.
    expect(senderTarget({ origin: 'https://www.youtube.com', url: 'https://x.example/', frameId: 0, tab }, WATCH).ok).toBe(true);
  });

  it.each([
    ['no tab', { url: WATCH, frameId: 0 }, WATCH],
    ['a tab without id', { url: WATCH, frameId: 0, tab: { url: WATCH } }, WATCH],
    ['no real tab', { url: WATCH, frameId: 0, tab: { id: -1, url: WATCH } }, WATCH],
    ['a private window', { url: WATCH, frameId: 0, tab: { id: 4, url: WATCH, incognito: true } }, WATCH],
    ['not a video page and not its own address', { url: 'https://www.youtube.com/', frameId: 0, tab }, 'https://www.youtube.com/feed'],
    ['another frame’s address', { url: 'https://blog.example/a', frameId: 3, tab }, 'https://blog.example/b'],
    ['a frame that is not http(s)', { url: 'about:blank', frameId: 3, tab }, 'about:blank'],
    ['another origin', { url: 'https://m.youtube.com/', frameId: 0, tab }, WATCH],
    ['an origin of another site', { url: 'https://evil.example/', origin: 'https://evil.example', frameId: 0, tab }, WATCH],
  ])('refuses a content script with %s', (_name, sender, url) => {
    expect(senderTarget(sender, url)).toMatchObject({ ok: false, error: { code: 'unsupported_page' } });
  });

  it('accepts a frame asking about its own address as a generic target, never with cookies', () => {
    const blog = { id: 4, url: 'https://blog.example/post', title: 'Post', cookieStoreId: 'c1' };
    expect(senderTarget({ url: 'https://news.example/story#play', frameId: 0, tab: { ...blog, url: 'https://news.example/story' } }, 'https://news.example/story')).toEqual({
      ok: true,
      value: { key: 'page:https://news.example/story', pageUrl: 'https://news.example/story', storeId: 'c1', generic: true, title: 'Post' },
    });
    // An embedded player's frame: the tab's page is its Referer.
    expect(senderTarget({ url: 'https://player.example/e/9', frameId: 5, tab: blog }, 'https://player.example/e/9#t=1')).toEqual({
      ok: true,
      value: { key: 'page:https://player.example/e/9', pageUrl: 'https://player.example/e/9', storeId: 'c1', generic: true, referer: 'https://blog.example/' },
    });
    // A same-origin frame gets the page's address without its fragment.
    expect(
      (senderTarget({ url: 'https://blog.example/embed/9', frameId: 5, tab: { ...blog, url: 'https://blog.example/post?id=2#c' } }, 'https://blog.example/embed/9') as {
        value: VideoTarget;
      }).value.referer,
    ).toBe('https://blog.example/post?id=2');
    // A curated address on the frame's origin is the site's video.
    expect(senderTarget({ url: 'https://player.vimeo.com/video/76979871', frameId: 5, tab: blog }, 'https://player.vimeo.com/video/76979871')).toMatchObject({
      ok: true,
      value: { key: 'vimeo:76979871', generic: false, referer: 'https://blog.example/' },
    });
  });

  it('names the video of a frame: curated, else its own address', () => {
    const blog = { id: 4, url: 'https://blog.example/post', title: 'Post' };
    expect(frameVideo(blog, 'https://player.vimeo.com/video/76979871?h=8272103f6e', 3, [])).toEqual({
      target: {
        key: 'vimeo:76979871',
        pageUrl: 'https://player.vimeo.com/video/76979871?h=8272103f6e',
        storeId: '',
        generic: false,
        referer: 'https://blog.example/',
      },
      kind: 'embed',
      site: 'Vimeo',
      curated: true,
    });
    expect(frameVideo({ ...blog, url: 'https://www.reddit.com/r/videos/comments/1abc2de/t/' }, 'https://www.reddit.com/r/videos/comments/1abc2de/t/', 0, [])).toMatchObject({
      target: { key: 'reddit:1abc2de', pageUrl: 'https://www.reddit.com/r/videos/comments/1abc2de/', title: 'Post' },
      kind: 'watch',
      site: 'Reddit',
      curated: true,
    });
    expect(frameVideo(blog, 'https://blog.example/post#c', 0, [])).toEqual({
      target: { key: 'page:https://blog.example/post', pageUrl: 'https://blog.example/post', storeId: '', generic: true, title: 'Post' },
      kind: 'page',
      site: 'blog.example',
      curated: false,
    });
    expect(frameVideo(blog, 'about:blank', 2, [])).toBeUndefined();
    expect(frameVideo(blog, undefined, 2, [])).toBeUndefined();
    expect(frameVideo({ ...blog, incognito: true }, 'https://blog.example/post', 0, [])).toBeUndefined();
  });

  it('names a feed video by its own post, checked again; a feed without one gets no Site source (§5.1 "Feed videos")', () => {
    const FEED = 'https://www.instagram.com/';
    const POST = 'https://www.instagram.com/p/CODEaaa111/';
    const tab = { id: 4, url: FEED, title: 'Instagram', cookieStoreId: 'c1' };
    expect(frameVideo(tab, FEED, 0, [], POST)).toEqual({
      // No tab title: the feed's title is not the video's.
      target: { key: 'instagram:CODEaaa111', pageUrl: POST, storeId: 'c1', generic: false },
      kind: 'watch',
      site: 'Instagram',
      curated: true,
      linked: true,
    });
    // No post, a post of another origin, of another site, or a network site's: nothing at all
    // (yt-dlp cannot read a feed as one video, so no generic target either).
    expect(frameVideo(tab, FEED, 0, [])).toBeUndefined();
    expect(frameVideo(tab, FEED, 0, [], 'https://instagram.com/p/CODEaaa111/')).toBeUndefined();
    expect(frameVideo(tab, FEED, 0, [], 'https://www.instagram.com/explore/')).toBeUndefined();
    expect(frameVideo(tab, 'https://x.com/home', 0, [], 'https://x.com/home')).toBeUndefined();
    expect(frameVideo({ ...tab, incognito: true }, FEED, 0, [], POST)).toBeUndefined();
    const threads = 'https://www.threads.com/';
    expect(frameVideo({ ...tab, url: threads }, threads, 0, [], 'https://www.threads.com/@someone/post/AbC12dEfGh')).toBeUndefined();
    // On a video address the post is ignored: the page is the video.
    const reel = 'https://www.instagram.com/reel/PAGEeee555/';
    expect(frameVideo({ ...tab, url: reel }, reel, 0, [], POST)?.target.key).toBe('instagram:PAGEeee555');
    // Any other page keeps its generic target, post or not.
    expect(frameVideo({ ...tab, url: 'https://blog.example/' }, 'https://blog.example/', 0, [], POST)?.target.key).toBe('page:https://blog.example/');

    // The panel's probe and add name the post by its address: no tab title either.
    expect(senderTarget({ url: FEED, frameId: 0, tab }, POST)).toEqual({ ok: true, value: { key: 'instagram:CODEaaa111', pageUrl: POST, storeId: 'c1', generic: false } });
    expect(senderTarget({ url: 'https://www.instagram.com/p/CODEaaa111/?img_index=1', frameId: 0, tab }, POST)).toMatchObject({ ok: true, value: { title: 'Instagram' } });
  });

  it('keeps the generic target on a curated host’s pages that list no videos: an episode, a clip, an album, a story', () => {
    const tab = { id: 5, url: 'https://www.example.com/', title: 'A page', cookieStoreId: 'c1' };
    for (const url of [
      'https://www.bilibili.com/bangumi/play/ep100001',
      'https://vk.com/clip-1_2',
      'https://www.reddit.com/user/someone/comments/abc123/title/',
      'https://www.reddit.com/comments/abc123/',
      'https://imgur.com/a/AbC12',
      'https://www.instagram.com/stories/someone/',
      'https://vimeo.com/ondemand/film/12345',
      'https://www.twitch.tv/somechannel',
      'https://kick.com/someone',
    ]) {
      expect(frameVideo({ ...tab, url }, url, 0, [])?.target.key, url).toBe(`page:${url}`);
    }
    // Feeds, profiles, searches and home pages get none without the clicked video's post.
    for (const url of [
      'https://www.tiktok.com/foryou',
      'https://www.tiktok.com/@someone',
      'https://www.instagram.com/',
      'https://www.instagram.com/someone/',
      'https://www.instagram.com/reels/',
      'https://x.com/home',
      'https://x.com/someone/media',
      'https://www.facebook.com/watch/',
      'https://www.facebook.com/somepage/videos',
      'https://www.reddit.com/r/videos/',
      'https://www.reddit.com/r/videos/top/',
      'https://www.pinterest.com/search/pins/?q=x',
      'https://bsky.app/profile/someone.bsky.social',
      'https://vimeo.com/',
      // Private messages: no generic target either (VIDEO-DETECT-SPEC §5.1 "DMs").
      'https://x.com/messages/123-456',
      'https://www.instagram.com/direct/t/1234567890/',
      'https://www.facebook.com/messages/t/123',
      'https://x.com/i/chat/1234567890-9876543210',
      'https://x.com/i/chat',
    ]) {
      expect(frameVideo({ ...tab, url }, url, 0, []), url).toBeUndefined();
    }
    // Nor a post a panel names there: no post is looked for in private messages.
    for (const [url, post] of [
      ['https://www.instagram.com/direct/t/1234567890/', 'https://www.instagram.com/reel/AbCdEf12345/'],
      ['https://x.com/messages/123-456', 'https://x.com/someone/status/1234567890'],
      ['https://x.com/i/chat/1234567890-9876543210', 'https://x.com/someone/status/1234567890'],
    ] as const) {
      expect(frameVideo({ ...tab, url }, url, 0, [], post), url).toBeUndefined();
    }
    // A post found on another page of the host is used all the same.
    const user = 'https://www.reddit.com/user/someone/comments/abc123/title/';
    const post = 'https://www.reddit.com/r/videos/comments/1abc2de/t/';
    expect(frameVideo({ ...tab, url: user }, user, 0, [], post)).toMatchObject({ linked: true, curated: true });
  });

  it('never sends a network site’s page to Downex, as a site or as a page (VIDEO-DETECT-SPEC §3.8)', () => {
    const THREADS = 'https://www.threads.com/@someone/post/AbC12dEfGh';
    const SNAP = 'https://www.snapchat.com/spotlight/AbCdEfGhIjKlMnOpQrStUv';
    const NETWORK_ONLY = 'Downex can’t read this site’s videos yet. Play the video, then try again.';
    const tab = { id: 6, url: THREADS, title: 'A post' };
    expect(frameVideo(tab, THREADS, 0, [])).toBeUndefined();
    expect(frameVideo({ ...tab, url: 'https://blog.example/' }, SNAP, 3, [])).toBeUndefined();
    expect(findPageVideos(tab, [{ frameId: 2, url: SNAP }], true)).toEqual([]);
    // A YouTube player in a frame of the post is still listed.
    expect(findPageVideos(tab, [{ frameId: 2, url: EMBED }], true).map((f) => f.video.key)).toEqual([`youtube:${ID}`]);
    expect(findEntry(tab, [])).toEqual(failure('unsupported_page', NETWORK_ONLY));
    expect(tabTarget(tab, [], `page:${THREADS}`)).toMatchObject({ ok: false, error: { code: 'expired' } });
    expect(senderTarget({ url: THREADS, origin: 'https://www.threads.com', frameId: 0, tab }, THREADS)).toEqual(failure('unsupported_page', NETWORK_ONLY));
    expect(tabVideoEntries(tab, [], () => undefined, () => undefined, true)).toEqual([]);
    const counts = new PageVideoCounts();
    counts.setTop(9, THREADS, true);
    counts.setFrame(9, 4, SNAP);
    expect(counts.count(9)).toBe(0);
  });

  it('lists a post only while a video of the tab plays, and still looks its key up (§5.3)', () => {
    const PIN = 'https://www.pinterest.com/pin/123456/';
    const tab = { id: 7, url: PIN, title: 'A pin' };
    expect(findPageVideos(tab, [], false)).toEqual([]);
    expect(tabVideoEntries(tab, [], () => undefined, () => undefined, false)).toEqual([]);
    // Without its row, "Find the video on this page" is offered for the pin.
    expect(findEntry(tab, [])).toMatchObject({ ok: true, value: { key: `page:${PIN}` } });
    expect(tabVideoEntries(tab, [], () => undefined, () => undefined, true)).toMatchObject([{ key: 'pinterest:123456', site: 'Pinterest', kind: 'post' }]);
    expect(tabTarget(tab, [], 'pinterest:123456')).toMatchObject({ ok: true, value: { key: 'pinterest:123456' } });
    // A watch page needs no report.
    expect(findPageVideos({ url: WATCH }, [], false).map((f) => f.video.key)).toEqual([`youtube:${ID}`]);
  });

  it('looks the popup key up again from the tab and its frames', () => {
    const blog = { id: 5, url: 'https://blog.example/post#c', title: 'Post' };
    const frames = [{ frameId: 3, url: EMBED }];
    expect(tabTarget(blog, frames, `youtube:${ID}`)).toEqual({
      ok: true,
      value: { key: `youtube:${ID}`, pageUrl: WATCH, storeId: '', generic: false, referer: 'https://blog.example/' },
    });
    expect(tabTarget(blog, [], `youtube:${ID}`)).toMatchObject({ ok: false, error: { code: 'expired' } });
    expect(tabTarget(blog, frames, 'page:https://blog.example/post')).toEqual({
      ok: true,
      value: { key: 'page:https://blog.example/post', pageUrl: 'https://blog.example/post', storeId: '', generic: true, title: 'Post' },
    });
    expect(tabTarget({ ...blog, url: 'https://blog.example/other' }, [], 'page:https://blog.example/post')).toMatchObject({
      ok: false,
      error: { code: 'expired' },
    });
    expect(tabTarget({ ...blog, incognito: true }, frames, `youtube:${ID}`)).toMatchObject({ ok: false });
  });

  it('lists the page videos for the popup with what the background knows', async () => {
    const probes = new VideoProbes();
    const key = probeKey({ storeId: '', key: `youtube:${ID}` }, false);
    await probes.run(key, async () => ({ ok: true, value: info('From yt-dlp') }));
    const adds = (k: string) => (k === `youtube:${ID}` ? { running: true as const } : undefined);
    const state = (t: VideoTarget) => probes.state(probeKey(t, false));
    const entries = tabVideoEntries(tab, [{ frameId: 2, url: 'https://www.youtube.com/embed/aaaaaaaaaaa' }], state, adds, false);
    expect(entries).toEqual([
      {
        key: `youtube:${ID}`,
        site: 'YouTube',
        kind: 'watch',
        frame: 'top',
        pageUrl: WATCH,
        title: 'From yt-dlp',
        probe: { result: { ok: true, value: info('From yt-dlp') } },
        add: { running: true },
      },
      { key: 'youtube:aaaaaaaaaaa', site: 'YouTube', kind: 'embed', frame: 'embed', pageUrl: 'https://www.youtube.com/watch?v=aaaaaaaaaaa' },
    ]);
    // Another page: only after "Find the video on this page" probed it.
    const page = { id: 6, url: 'https://video.example/v/1', title: 'Clip' };
    expect(tabVideoEntries(page, [], state, () => undefined, false)).toEqual([]);
    expect(findEntry(page, [])).toEqual({
      ok: true,
      value: { key: 'page:https://video.example/v/1', site: 'video.example', kind: 'page', frame: 'top', pageUrl: 'https://video.example/v/1', title: 'Clip' },
    });
    expect(findEntry({ id: 6, url: 'chrome://newtab/' }, [])).toMatchObject({ ok: false, error: { code: 'unsupported_page' } });
    // A found page's anonymous answer is what the popup lists.
    await probes.run(probeKey({ storeId: '', key: 'page:https://video.example/v/1' }, false), async () => ({
      ok: true,
      value: { ...info('Clip!'), site: 'Vimeo' },
    }));
    expect(tabVideoEntries(page, [], state, () => undefined, false)).toMatchObject([{ key: 'page:https://video.example/v/1', site: 'Vimeo', title: 'Clip!' }]);
  });
});

describe('probe and add', () => {
  let now = 1_000_000;
  beforeEach(() => {
    vi.useFakeTimers();
    now = 1_000_000;
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  function setup(opts: Partial<VideoOptions> = {}) {
    const probes: ProbeVideoFields[] = [];
    const adds: AddVideoFields[] = [];
    const answers: Outcome<AddedResult>[] = [];
    const client = {
      probeVideo: vi.fn(async (f: ProbeVideoFields, _o?: RequestOptions<VideoInfoResult>): Promise<Outcome<VideoInfoResult>> => {
        probes.push(f);
        return { ok: true, value: info() };
      }),
      addVideo: vi.fn(async (f: AddVideoFields, _o?: RequestOptions<AddedResult>): Promise<Outcome<AddedResult>> => {
        adds.push(f);
        return answers.shift() ?? { ok: true, value: { type: 'added', downloadId: 9, awaiting: true } };
      }),
    };
    const data: Record<string, unknown> = {};
    const area: StorageAreaLike = {
      get: async (k) => ({ [k]: data[k] }),
      set: async (items) => void Object.assign(data, items),
    };
    const getCookies = vi.fn(async () => [{ name: 'SID', value: 'secret', domain: '.youtube.com' }]);
    const handOff = createVideoHandOff({
      client,
      getCookies,
      options: async () => ({ videoButton: true, useSignInForVideos: true, watchPlayed: true, videoPrefetch: true, hiddenSites: [], ...opts }),
      history: new HandoffHistory(area),
      now: () => now,
    });
    const history = async () => (await new HandoffHistory(area).list()) as HandoffRecord[];
    return { handOff, client, probes, adds, answers, getCookies, history };
  }

  const top: VideoTarget = { key: `youtube:${ID}`, pageUrl: WATCH, storeId: '', generic: false, title: '(2) A video - YouTube' };

  it('puts the sign-in’s Cookie and the embedding page in the headers', () => {
    expect(videoHeaders(top)).toEqual([]);
    expect(videoHeaders(top, 'SID=s; HSID=h')).toEqual([['Cookie', 'SID=s; HSID=h']]);
    expect(videoHeaders({ referer: 'https://blog.example/' })).toEqual([['Referer', 'https://blog.example/']]);
  });

  it('asks for one video of a page with several apart from the default one, and adds it', async () => {
    const { handOff, client, probes, adds } = setup();
    await handOff.probe(top);
    await handOff.probe(top, 'itemB');
    await handOff.probe(top, 'itemB');
    await handOff.probe(top);
    expect(client.probeVideo).toHaveBeenCalledTimes(2);
    expect(probes.map((p) => p.entryId)).toEqual([undefined, 'itemB']);
    expect(videoItemKey('k', undefined)).toBe('k');
    expect(videoItemKey('k', 'itemB')).toBe('k\nitem:itemB');
    await handOff.add(top, 'v', '', 'video', undefined, 'itemB');
    await handOff.add(top, '', '');
    expect(adds.map((a) => a.entryId)).toEqual(['itemB', undefined]);
  });

  it('keeps answers with and without the sign-in apart', () => {
    expect(probeKey(top, false)).toBe(`\nyoutube:${ID}`);
    expect(probeKey(top, true)).toBe(`\nyoutube:${ID}\nsign-in`);
    expect(probeKey({ ...top, storeId: 'c1', key: 'page:https://a.example/' }, true)).toBe('c1\npage:https://a.example/\nsign-in');
  });

  it('probes through the shared cache with the deadline, without cookies for a public video', async () => {
    const { handOff, client, probes, getCookies } = setup({ useSignInForVideos: true });
    await expect(handOff.probe(top)).resolves.toMatchObject({ ok: true });
    await expect(handOff.probe(top)).resolves.toMatchObject({ ok: true });
    expect(client.probeVideo).toHaveBeenCalledTimes(1);
    expect(probes[0]).toEqual({ pageUrl: WATCH, deadlineMs: VIDEO_DEADLINE_MS });
    expect(client.probeVideo.mock.calls[0]?.[1]).toEqual({ timeoutMs: VIDEO_DEADLINE_MS });
    // Another cookie store is another cache entry.
    await handOff.probe({ ...top, storeId: 'firefox-container-1' });
    expect(client.probeVideo).toHaveBeenCalledTimes(2);
    // A public video's probe and add never read the browser's cookies (SITE-MEDIA-SPEC §6.3).
    await handOff.add(top, '', '');
    expect(getCookies).not.toHaveBeenCalled();
  });

  it('sends a look-ahead marked prefetch into the same cache, joined by a click', async () => {
    const userRequests = vi.fn();
    let answer = deferred<Outcome<VideoInfoResult>>();
    const client = {
      probeVideo: vi.fn((_f: ProbeVideoFields, _o?: RequestOptions<VideoInfoResult>) => answer.promise),
      addVideo: vi.fn(async (): Promise<Outcome<AddedResult>> => ({ ok: true, value: { type: 'added', downloadId: 1 } })),
    };
    const getCookies = vi.fn(async () => [{ name: 'SID', value: 's', domain: '.youtube.com' }]);
    const handOff = createVideoHandOff({
      client,
      getCookies,
      options: async () => ({ videoButton: true, useSignInForVideos: true, watchPlayed: true, videoPrefetch: true, hiddenSites: [] }),
      onUserRequest: userRequests,
    });
    const ahead = handOff.lookAhead({ ...top, referer: 'https://blog.example/' });
    await vi.waitFor(() => expect(client.probeVideo).toHaveBeenCalledTimes(1));
    // Identical to a probe, marked as a look-ahead.
    expect(client.probeVideo.mock.calls[0]?.[0]).toEqual({
      pageUrl: WATCH,
      deadlineMs: VIDEO_DEADLINE_MS,
      prefetch: true,
      headers: [['Referer', 'https://blog.example/']],
    });
    expect(getCookies).not.toHaveBeenCalled();
    expect(client.probeVideo.mock.calls[0]?.[1]).toEqual({ timeoutMs: VIDEO_DEADLINE_MS });
    expect(userRequests).not.toHaveBeenCalled();
    // A click meanwhile joins it.
    const click = handOff.probe(top);
    await vi.advanceTimersByTimeAsync(0);
    expect(userRequests).toHaveBeenCalledTimes(1);
    answer.resolve({ ok: true, value: info('ahead') });
    await expect(ahead).resolves.toMatchObject({ ok: true });
    await expect(click).resolves.toMatchObject({ ok: true, value: { title: 'ahead' } });
    expect(client.probeVideo).toHaveBeenCalledTimes(1);
    // The popup lists it; a second look-ahead is not sent.
    expect((await handOff.probeState())(top)).toMatchObject({ result: { ok: true, value: { title: 'ahead' } } });
    await expect(handOff.lookAhead(top)).resolves.toBeUndefined();
    expect(client.probeVideo).toHaveBeenCalledTimes(1);
    // A look-ahead whose answer is not kept leaves the next click its own probe, unmarked.
    const other: VideoTarget = { ...top, key: 'youtube:aaaaaaaaaaa', pageUrl: 'https://www.youtube.com/watch?v=aaaaaaaaaaa' };
    answer = deferred();
    const second = handOff.lookAhead(other);
    await vi.waitFor(() => expect(client.probeVideo).toHaveBeenCalledTimes(2));
    answer.resolve(refused('tools_unavailable'));
    await expect(second).resolves.toMatchObject({ ok: false, error: { reason: 'tools_unavailable' } });
    answer = deferred();
    const own = handOff.probe(other);
    await vi.waitFor(() => expect(client.probeVideo).toHaveBeenCalledTimes(3));
    expect(client.probeVideo.mock.calls[2]?.[0]).not.toHaveProperty('prefetch');
    answer.resolve({ ok: true, value: info() });
    await expect(own).resolves.toMatchObject({ ok: true });
    // An add is a click too.
    await handOff.add(top, '', '');
    expect(userRequests).toHaveBeenCalledTimes(3);
  });

  it('sends a look-ahead without cookies while the sign-in is on, and never asks it again with them', async () => {
    const client = {
      probeVideo: vi.fn(async (_f: ProbeVideoFields): Promise<Outcome<VideoInfoResult>> => refused('auth_required')),
      addVideo: vi.fn(),
    };
    const getCookies = vi.fn(async () => [{ name: 'SID', value: 's', domain: '.youtube.com' }]);
    const handOff = createVideoHandOff({
      client: client as never,
      getCookies,
      options: async () => ({ videoButton: true, useSignInForVideos: true, watchPlayed: true, videoPrefetch: true, hiddenSites: [] }),
    });
    await expect(handOff.lookAhead(top)).resolves.toMatchObject({ ok: false, error: { reason: 'auth_required' } });
    expect(client.probeVideo).toHaveBeenCalledTimes(1);
    expect(client.probeVideo.mock.calls[0]?.[0]).toEqual({ pageUrl: WATCH, deadlineMs: VIDEO_DEADLINE_MS, prefetch: true });
    expect(getCookies).not.toHaveBeenCalled();
  });

  it('adds the pick and records it as a video hand-off', async () => {
    const { handOff, adds, history } = setup();
    await expect(handOff.add(top, '299', '140')).resolves.toEqual({ ok: true, value: { type: 'added', downloadId: 9, awaiting: true } });
    expect(adds[0]).toEqual({ pageUrl: WATCH, deadlineMs: VIDEO_DEADLINE_MS, videoTrack: '299', audioTrack: '140', title: '(2) A video - YouTube' });
    await vi.waitFor(async () =>
      expect(await history()).toEqual([
        { ts: now, url: WATCH, source: 'video', ok: true, title: '(2) A video - YouTube', downloadId: 9, awaiting: true },
      ]),
    );
    // Downex's default pick: no tracks.
    await handOff.add({ ...top, title: undefined }, '', '');
    expect(adds[1]).toEqual({ pageUrl: WATCH, deadlineMs: VIDEO_DEADLINE_MS });
  });

  it('asks again every 2 s while Downex sets up yt-dlp, for at most 5 minutes', async () => {
    const { handOff, client, answers, history } = setup();
    answers.push(refused('tools_installing'), refused('site_resolving'));
    const p = handOff.add(top, '', '');
    await vi.advanceTimersByTimeAsync(0);
    expect(client.addVideo).toHaveBeenCalledTimes(1);
    now += VIDEO_RETRY_MS;
    await vi.advanceTimersByTimeAsync(VIDEO_RETRY_MS);
    expect(client.addVideo).toHaveBeenCalledTimes(2);
    now += VIDEO_RETRY_MS;
    await vi.advanceTimersByTimeAsync(VIDEO_RETRY_MS);
    await expect(p).resolves.toMatchObject({ ok: true });
    expect(client.addVideo).toHaveBeenCalledTimes(3);
    await vi.waitFor(async () => expect(await history()).toHaveLength(1));

    // Past 5 minutes the last answer stands.
    client.addVideo.mockImplementation(async () => {
      now += 60_000;
      return refused('tools_installing');
    });
    const q = handOff.add(top, '', '');
    await vi.advanceTimersByTimeAsync(VIDEO_RETRY_LIMIT_MS);
    await expect(q).resolves.toMatchObject({ ok: false, error: { reason: 'tools_installing' } });
    expect(client.addVideo.mock.calls.length).toBeLessThanOrEqual(3 + 6);
    // A call that took the whole interval is followed at once, not after one more wait.
    let calls = 0;
    client.addVideo.mockImplementation(async () => {
      now += 25_000;
      return ++calls === 1 ? refused('tools_installing') : { ok: true, value: { type: 'added', downloadId: 10, awaiting: false } };
    });
    const r = handOff.add(top, '', '');
    await vi.advanceTimersByTimeAsync(0);
    await expect(r).resolves.toMatchObject({ ok: true });
    expect(calls).toBe(2);
    // A refusal is final at once.
    client.addVideo.mockImplementation(async () => refused('live'));
    await expect(handOff.add(top, '', '')).resolves.toMatchObject({ ok: false, error: { reason: 'live' } });
  });
});

describe('VideoProbes with another keep rule', () => {
  it('keeps only what the rule keeps', async () => {
    const probes = new VideoProbes({ keep: (o) => !o.ok && o.error.code === 'rejected' });
    const start = vi.fn(async (): Promise<Outcome<VideoInfoResult>> => ({ ok: true, value: info() }));
    await probes.run('a', start);
    await probes.run('a', start);
    expect(start).toHaveBeenCalledTimes(2);
    const refusal = vi.fn(async (): Promise<Outcome<VideoInfoResult>> => refused('encrypted'));
    await probes.run('b', refusal);
    await probes.run('b', refusal);
    expect(refusal).toHaveBeenCalledTimes(1);
    expect(probes.state('b')).toEqual({ result: refused('encrypted') });
  });
});

describe('PageVideoCounts', () => {
  it('names the page video of each frame', () => {
    const counts = new PageVideoCounts();
    counts.setTop(1, WATCH, true);
    counts.setFrame(1, 4, 'https://player.vimeo.com/video/76979871');
    counts.setFrame(1, 5, 'https://ads.example/');
    expect([...counts.frameKeys(1, false)]).toEqual([
      [4, 'vimeo:76979871'],
      [0, `youtube:${ID}`],
    ]);
    expect(counts.frameKeys(2, true).size).toBe(0);
  });

  it('names a post or share link only once a frame of the tab reported a playing video (§5.3)', () => {
    const counts = new PageVideoCounts();
    // Most posts and pins are text or images: no badge until a video plays there.
    counts.setTop(1, 'https://www.pinterest.com/pin/123456/', true);
    counts.setFrame(1, 3, 'https://player.vimeo.com/video/76979871');
    expect([...counts.frameKeys(1, false)]).toEqual([[3, 'vimeo:76979871']]);
    expect([...counts.frameKeys(1, true)]).toEqual([
      [3, 'vimeo:76979871'],
      [0, 'pinterest:123456'],
    ]);
    counts.setTop(2, 'https://t.me/some_channel/123', true);
    counts.setTop(3, 'https://www.facebook.com/someone/posts/123', true);
    counts.setFrame(3, 4, 'https://vm.tiktok.com/ZMabc123/');
    expect(counts.frameKeys(2, false).size).toBe(0);
    expect(counts.frameKeys(3, false).size).toBe(0);
    expect([...counts.frameKeys(3, true).values()].sort()).toEqual(['facebook:post:123', 'tiktok:link:ZMabc123']);
  });

  it('follows the top address, SPA moves and embedded players', () => {
    const c = new PageVideoCounts();
    c.setTop(1, WATCH, true);
    expect(c.count(1)).toBe(1);
    c.setFrame(1, 5, EMBED); // the same video
    expect(c.count(1)).toBe(1);
    c.setFrame(1, 6, 'https://www.youtube.com/embed/aaaaaaaaaaa');
    expect(c.count(1)).toBe(2);
    c.setTop(1, 'https://www.youtube.com/', false); // the home page, same document
    expect(c.count(1)).toBe(2);
    c.setFrame(1, 6, 'https://ads.example/');
    expect(c.count(1)).toBe(1);
    c.setTop(1, 'https://blog.example/', true); // another document: its frames are new
    expect(c.count(1)).toBe(0);
    expect(c.tabs()).toEqual([]);
    c.setTop(2, WATCH, true);
    c.replace(2, 3);
    expect([c.count(2), c.count(3)]).toEqual([0, 1]);
    c.clearTab(3);
    expect(c.count(3)).toBe(0);
  });
});

describe('the user\'s sign-in (SITE-MEDIA-SPEC §6.3: YouTube only since the second 2026-11-18 amendment)', () => {
  const REEL = 'https://www.youtube.com/watch?v=AbCdEfGhIjK';
  const reel: VideoTarget = { key: 'youtube:AbCdEfGhIjK', pageUrl: REEL, storeId: '', generic: false };
  /** What the browser holds for the reel's address: the site's, a subdomain's, and others that must never go. */
  const COOKIES = [
    { name: 'sessionid', value: 's1', domain: '.youtube.com' },
    { name: 'csrftoken', value: 'c2', domain: 'www.youtube.com' },
    { name: 'other', value: 'x', domain: '.facebook.com' },
    { name: 'lookalike', value: 'y', domain: 'youtube.com.evil.example' },
  ];

  function setup(opts: { signIn?: boolean; cookies?: unknown[]; signedAnswer?: Outcome<VideoInfoResult> } = {}) {
    const sent: ProbeVideoFields[] = [];
    const added: AddVideoFields[] = [];
    const client = {
      probeVideo: vi.fn(async (f: ProbeVideoFields): Promise<Outcome<VideoInfoResult>> => {
        sent.push(f);
        return f.headers?.some(([n]) => n === 'Cookie') ? (opts.signedAnswer ?? { ok: true, value: info('Signed') }) : refused('auth_required');
      }),
      addVideo: vi.fn(async (f: AddVideoFields): Promise<Outcome<AddedResult>> => {
        added.push(f);
        return f.headers?.some(([n]) => n === 'Cookie') ? { ok: true, value: { type: 'added', downloadId: 3, awaiting: false } } : refused('auth_required');
      }),
    };
    const getCookies = vi.fn(async () => (opts.cookies ?? COOKIES) as { name: string; value: string; domain: string }[]);
    const handOff = createVideoHandOff({
      client,
      getCookies,
      options: async () => ({ videoButton: true, useSignInForVideos: opts.signIn ?? true, watchPlayed: true, videoPrefetch: true, hiddenSites: [] }),
    });
    return { handOff, client, sent, added, getCookies };
  }

  it('asks once without cookies, then once more at once with only that site\'s cookies', async () => {
    const { handOff, client, sent, getCookies } = setup();
    await expect(handOff.probe(reel)).resolves.toEqual({ ok: true, value: { ...info('Signed'), signedIn: 'youtube.com' } });
    expect(client.probeVideo).toHaveBeenCalledTimes(2);
    expect(sent[0]).toEqual({ pageUrl: REEL, deadlineMs: VIDEO_DEADLINE_MS });
    expect(sent[1]).toEqual({
      pageUrl: REEL,
      deadlineMs: VIDEO_DEADLINE_MS,
      headers: [['Cookie', 'sessionid=s1; csrftoken=c2']],
      cookieDomain: 'youtube.com',
    });
    expect(getCookies).toHaveBeenCalledTimes(1);
    expect(getCookies).toHaveBeenCalledWith(REEL, undefined);
    // Both answers are kept: another click asks nothing and reads nothing.
    await expect(handOff.probe(reel)).resolves.toMatchObject({ ok: true, value: { signedIn: 'youtube.com' } });
    expect(client.probeVideo).toHaveBeenCalledTimes(2);
    expect(getCookies).toHaveBeenCalledTimes(1);
    // The popup lists the signed-in answer.
    expect((await handOff.probeState())(reel)).toMatchObject({ result: { ok: true, value: { title: 'Signed', signedIn: 'youtube.com' } } });
  });

  it('reads the tab\'s own cookie store', async () => {
    const { handOff, getCookies } = setup();
    await handOff.probe({ ...reel, storeId: 'firefox-container-2' });
    expect(getCookies).toHaveBeenCalledWith(REEL, 'firefox-container-2');
  });

  it('sends nothing more while the option is off, and says how to turn it on', async () => {
    const { handOff, client, getCookies } = setup({ signIn: false });
    await expect(handOff.probe(reel)).resolves.toMatchObject({
      ok: false,
      error: { reason: 'auth_required', signIn: { domain: 'youtube.com', state: 'off' } },
    });
    expect(client.probeVideo).toHaveBeenCalledTimes(1);
    expect(getCookies).not.toHaveBeenCalled();
  });

  it('sends nothing more without a cookie of the site, or with more than 16 KiB of them', async () => {
    for (const [cookies, state] of [
      [[], 'none'],
      [[{ name: 'other', value: 'x', domain: '.facebook.com' }], 'none'],
      [[{ name: 'big', value: 'v'.repeat(MAX_SIGN_IN_COOKIE_BYTES), domain: '.youtube.com' }], 'too-large'],
      // Only cookies downexd's cookie file can carry count: a non-ASCII value would be dropped there.
      [[{ name: 'big', value: 'é'.repeat(9000), domain: '.youtube.com' }], 'none'],
      [[{ name: 'bad name', value: 'x', domain: '.youtube.com' }], 'none'],
    ] as const) {
      const { handOff, client } = setup({ cookies: [...cookies] });
      await expect(handOff.probe(reel)).resolves.toMatchObject({ ok: false, error: { signIn: { domain: 'youtube.com', state } } });
      expect(client.probeVideo).toHaveBeenCalledTimes(1);
    }
    // A cookie lookup that fails counts as none.
    const { handOff, client, getCookies } = setup();
    getCookies.mockRejectedValue(new Error('no access'));
    await expect(handOff.probe(reel)).resolves.toMatchObject({ ok: false, error: { signIn: { state: 'none' } } });
    expect(client.probeVideo).toHaveBeenCalledTimes(1);
  });

  it('counts the 16 KiB in UTF-8 bytes of what downexd can use, and leaves out what it would drop', async () => {
    const opts = async () => ({ videoButton: true, useSignInForVideos: true, watchPlayed: true, videoPrefetch: true, hiddenSites: [] });
    const read = (cookies: { name: string; value: string; domain: string }[]) => readSignIn(reel, { getCookies: async () => cookies, options: opts });
    // A header of exactly 16 KiB goes; one byte more does not.
    const fits = { name: 'a', value: 'v'.repeat(MAX_SIGN_IN_COOKIE_BYTES - 2), domain: '.youtube.com' };
    await expect(read([fits])).resolves.toMatchObject({ cookie: `a=${fits.value}`, domain: 'youtube.com' });
    await expect(read([{ ...fits, value: `${fits.value}v` }])).resolves.toEqual({ note: { domain: 'youtube.com', state: 'too-large' } });
    // Multi-byte values (18 000 UTF-8 bytes in 9 000 characters) are left out, never sent.
    await expect(
      read([
        { name: 'sessionid', value: 's1', domain: '.youtube.com' },
        { name: 'wide', value: 'é'.repeat(9000), domain: '.youtube.com' },
        { name: 'spaced', value: 'a b', domain: '.youtube.com' },
        { name: 'semi', value: 'a;b', domain: '.youtube.com' },
      ]),
    ).resolves.toEqual({ cookie: 'sessionid=s1', domain: 'youtube.com', exact: false });
  });

  it('never sends the sign-in for a bot check, which is not about who the user is', async () => {
    for (const message of ['auth_required: Sign in to confirm you’re not a bot.', "auth_required: Sign in to confirm you're not a bot.", 'auth_required: Fresh cookies (not necessarily logged in) are needed']) {
      const { handOff, client, getCookies } = setup();
      const bot = failure('rejected', message, 'auth_required');
      client.probeVideo.mockImplementation(async () => bot);
      client.addVideo.mockImplementation(async () => bot);
      const watch: VideoTarget = { key: `youtube:${ID}`, pageUrl: WATCH, storeId: '', generic: false };
      const probed = await handOff.probe(watch);
      expect(probed).toEqual(bot);
      expect(client.probeVideo).toHaveBeenCalledTimes(1);
      // Reopened: the plain refusal, never a sign-in note.
      expect((await handOff.probeState())(watch)).toEqual({ result: bot });
      await expect(handOff.add(watch, '', '')).resolves.toEqual(bot);
      expect(client.addVideo).toHaveBeenCalledTimes(1);
      await handOff.probe(reel);
      expect(client.probeVideo).toHaveBeenCalledTimes(2);
      expect(getCookies).not.toHaveBeenCalled();
      for (const f of [...client.probeVideo.mock.calls, ...client.addVideo.mock.calls]) expect(f[0].headers).toBeUndefined();
    }
  });

  it('keeps what became of the sign-in when the panel or the popup is opened again', async () => {
    // Sent and still refused: the note and its Try again come back, with no request.
    const used = setup({ signedAnswer: refused('auth_required') });
    await used.handOff.probe(reel);
    expect(used.client.probeVideo).toHaveBeenCalledTimes(2);
    expect((await used.handOff.probeState())(reel)).toMatchObject({
      result: { ok: false, error: { reason: 'auth_required', signIn: { domain: 'youtube.com', state: 'used' } } },
    });
    expect(used.client.probeVideo).toHaveBeenCalledTimes(2);
    // No cookie of the site.
    const none = setup({ cookies: [] });
    await none.handOff.probe(reel);
    expect((await none.handOff.probeState())(reel)).toMatchObject({ result: { ok: false, error: { signIn: { state: 'none' } } } });
    // The switch off, also when it was turned off after the probe.
    const off = setup({ signIn: false });
    await off.handOff.probe(reel);
    expect((await off.handOff.probeState())(reel)).toMatchObject({ result: { ok: false, error: { signIn: { state: 'off' } } } });
    let on = true;
    const late = setup({ signedAnswer: refused('auth_required') });
    const lateOff = createVideoHandOff({
      client: late.client,
      getCookies: late.getCookies,
      options: async () => ({ videoButton: true, useSignInForVideos: on, watchPlayed: true, videoPrefetch: true, hiddenSites: [] }),
    });
    await lateOff.probe(reel);
    on = false;
    expect((await lateOff.probeState())(reel)).toMatchObject({ result: { ok: false, error: { signIn: { state: 'off' } } } });
    // Turned on after an "off" refusal: no stale advice; the panel asks again when opened.
    const offThenOn = createVideoHandOff({
      client: late.client,
      getCookies: late.getCookies,
      options: async () => ({ videoButton: true, useSignInForVideos: on, watchPlayed: true, videoPrefetch: true, hiddenSites: [] }),
    });
    await offThenOn.probe(reel);
    on = true;
    expect((await offThenOn.probeState())(reel)).toBeUndefined();
    // A share link without a sign-in keeps the plain refusal.
    const link: VideoTarget = { key: 'facebook:AbCd1234', pageUrl: 'https://fb.watch/AbCd1234/', storeId: '', generic: false };
    await used.handOff.probe(link);
    expect((await used.handOff.probeState())(link)).toEqual({ result: refused('auth_required') });
  });

  it('says the sign-in was used when the site still refuses, and asks again on the next click', async () => {
    const { handOff, client } = setup({ signedAnswer: refused('auth_required') });
    await expect(handOff.probe(reel)).resolves.toMatchObject({ ok: false, error: { signIn: { domain: 'youtube.com', state: 'used' } } });
    expect(client.probeVideo).toHaveBeenCalledTimes(2);
    // The anonymous refusal is kept; the signed-in one is not (the user may sign in on the site).
    await handOff.probe(reel);
    expect(client.probeVideo).toHaveBeenCalledTimes(3);
    expect(client.probeVideo.mock.calls[2]?.[0].headers).toEqual([['Cookie', 'sessionid=s1; csrftoken=c2']]);
  });

  it('asks no second time for another refusal', async () => {
    const { handOff, client, getCookies } = setup();
    client.probeVideo.mockImplementation(async () => refused('site_unavailable'));
    await expect(handOff.probe(reel)).resolves.toMatchObject({ ok: false, error: { reason: 'site_unavailable' } });
    client.probeVideo.mockImplementation(async () => failure('rejected', 'site_error: the site refused the request (HTTP 403, often bot protection)', 'site_error'));
    await handOff.probe({ ...reel, key: 'youtube:OtherOther1', pageUrl: 'https://www.youtube.com/watch?v=OtherOther1' });
    expect(client.probeVideo).toHaveBeenCalledTimes(2);
    expect(getCookies).not.toHaveBeenCalled();
  });

  it('never signs in for any site but YouTube: a generic page or a curated site is asked without cookies only', async () => {
    const page = 'https://video.blog.example/watch/7';
    const { handOff, client, getCookies } = setup({ cookies: [{ name: 'own', value: '1', domain: 'video.blog.example' }] });
    await expect(handOff.probe({ key: `page:${page}`, pageUrl: page, storeId: '', generic: true })).resolves.toEqual(refused('auth_required'));
    const ig: VideoTarget = { key: 'instagram:AbCdEfGh', pageUrl: 'https://www.instagram.com/reel/AbCdEfGh/', storeId: '', generic: false };
    await expect(handOff.probe(ig)).resolves.toEqual(refused('auth_required'));
    await expect(handOff.add(ig, '', '', 'menu')).resolves.toEqual(refused('auth_required'));
    expect(client.probeVideo).toHaveBeenCalledTimes(2);
    expect(client.addVideo).toHaveBeenCalledTimes(1);
    expect(getCookies).not.toHaveBeenCalled();
    // Nothing of a sign-in is said either: the refusal carries no note.
    expect((await handOff.probeState())(ig)).toEqual({ result: refused('auth_required') });
  });

  it('never signs in for a share link on another domain', async () => {
    const { handOff, client, getCookies } = setup();
    const link: VideoTarget = { key: 'facebook:AbCd1234', pageUrl: 'https://fb.watch/AbCd1234/', storeId: '', generic: false };
    await expect(handOff.probe(link)).resolves.toEqual(refused('auth_required'));
    expect(client.probeVideo).toHaveBeenCalledTimes(1);
    expect(getCookies).not.toHaveBeenCalled();
    expect(await readSignIn(link, { getCookies, options: async () => ({ videoButton: true, useSignInForVideos: true, watchPlayed: true, videoPrefetch: true, hiddenSites: [] }) })).toBeUndefined();
  });

  it('adds with the sign-in at once after a probe that needed it', async () => {
    const { handOff, client, added } = setup();
    await handOff.probe(reel);
    await expect(handOff.add(reel, 'v', '')).resolves.toEqual({
      ok: true,
      value: { type: 'added', downloadId: 3, awaiting: false, signedIn: 'youtube.com' },
    });
    expect(client.addVideo).toHaveBeenCalledTimes(1);
    expect(added[0]).toMatchObject({ headers: [['Cookie', 'sessionid=s1; csrftoken=c2']], cookieDomain: 'youtube.com', videoTrack: 'v' });
  });

  it('adds without cookies first, then once more with them after auth_required', async () => {
    const { handOff, client, added } = setup();
    await expect(handOff.add(reel, '', '', 'menu')).resolves.toMatchObject({ ok: true, value: { signedIn: 'youtube.com' } });
    expect(client.addVideo).toHaveBeenCalledTimes(2);
    expect(added[0]).toEqual({ pageUrl: REEL, deadlineMs: VIDEO_DEADLINE_MS });
    expect(added[1]).toEqual({ pageUrl: REEL, deadlineMs: VIDEO_DEADLINE_MS, headers: [['Cookie', 'sessionid=s1; csrftoken=c2']], cookieDomain: 'youtube.com' });
    // With the option off the add stops after the first answer and says why.
    const off = setup({ signIn: false });
    await expect(off.handOff.add(reel, '', '')).resolves.toMatchObject({ ok: false, error: { signIn: { state: 'off' } } });
    expect(off.client.addVideo).toHaveBeenCalledTimes(1);
    expect(off.getCookies).not.toHaveBeenCalled();
  });

  it('keeps no cookie in the history or the answers', async () => {
    const data: Record<string, unknown> = {};
    const area: StorageAreaLike = {
      get: async (k) => ({ [k]: data[k] }),
      set: async (items) => void Object.assign(data, items),
    };
    const { client } = setup();
    const handOff = createVideoHandOff({
      client,
      getCookies: async () => COOKIES,
      options: async () => ({ videoButton: true, useSignInForVideos: true, watchPlayed: true, videoPrefetch: true, hiddenSites: [] }),
      history: new HandoffHistory(area),
    });
    const probed = await handOff.probe(reel);
    const addedOutcome = await handOff.add(reel, '', '');
    await vi.waitFor(async () => expect(await new HandoffHistory(area).list()).toHaveLength(1));
    for (const v of [probed, addedOutcome, data]) expect(JSON.stringify(v)).not.toMatch(/s1|c2|sessionid/);
  });
});
