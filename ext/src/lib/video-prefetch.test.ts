import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { failure, type Outcome, type VideoInfoResult } from './protocol';
import type { VideoTarget } from './video-handoff';
import { DEFAULT_VIDEO_OPTIONS } from './options-form';
import {
  type DwellView,
  lookAheadAllowed,
  lookAheadRequest,
  type LookAheadRequest,
  PREFETCH_BURST,
  PREFETCH_DWELL_MS,
  PREFETCH_REFILL_MS,
  PREFETCH_THROTTLE_MS,
  PrefetchDwell,
  dwellKey,
  lookAheadSite,
  prefetchVideo,
  siteThrottled,
  stillWatched,
  throttledAnswer,
  VideoLookAhead,
} from './video-prefetch';

const watch = (id: string) => `https://www.youtube.com/watch?v=${id}`;
const shorts = (id: string) => `https://www.youtube.com/shorts/${id}`;
const A = 'aaaaaaaaaaa';
const B = 'bbbbbbbbbbb';
const C = 'ccccccccccc';
const D = 'ddddddddddd';

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

describe('prefetchVideo', () => {
  it('names only watch and Shorts pages', () => {
    expect(prefetchVideo(watch(A))?.key).toBe(`youtube:${A}`);
    expect(prefetchVideo(`${watch(A)}&list=PL1&t=30s`)?.key).toBe(`youtube:${A}`);
    expect(prefetchVideo(shorts(A))).toMatchObject({ key: `youtube:${A}`, pageUrl: watch(A) });
    expect(prefetchVideo(`https://music.youtube.com/watch?v=${A}`)?.key).toBe(`youtube:${A}`);
    for (const other of [
      `https://www.youtube.com/embed/${A}`,
      `https://www.youtube.com/live/${A}`,
      'https://www.youtube.com/clip/UgkxAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA',
      'https://www.youtube.com/',
      'https://www.youtube.com/@channel',
      'https://www.youtube.com/playlist?list=PL1',
      undefined,
    ]) {
      expect(prefetchVideo(other)).toBeUndefined();
    }
  });

  it('names the curated sites marked for it, never their embeds, Facebook or Instagram', () => {
    for (const [url, key] of [
      ['https://vimeo.com/76979871', 'vimeo:76979871'],
      ['https://x.com/someone/status/42', 'x:42'],
      ['https://www.tiktok.com/@u/video/7300000000000000001', 'tiktok:7300000000000000001'],
      ['https://www.dailymotion.com/video/x8abc12', 'dailymotion:x8abc12'],
      ['https://dai.ly/x8abc12', 'dailymotion:x8abc12'],
      ['https://www.reddit.com/r/videos/comments/1abc2de/t/', 'reddit:1abc2de'],
      ['https://www.twitch.tv/videos/2000000001', 'twitch:2000000001'],
      ['https://www.twitch.tv/chan/clip/FunnyClip', 'twitch:clip:FunnyClip'],
      ['https://clips.twitch.tv/FunnyClip', 'twitch:clip:FunnyClip'],
      ['https://streamable.com/abc123', 'streamable:abc123'],
    ]) {
      expect(prefetchVideo(url)?.key).toBe(key);
    }
    for (const url of [
      'https://player.vimeo.com/video/76979871',
      'https://www.facebook.com/watch/?v=1234567890',
      'https://www.facebook.com/reel/1234567890',
      'https://fb.watch/abcd12/',
      'https://www.instagram.com/p/C1a2B3c4D5e/',
      'https://www.instagram.com/reel/C1a2B3c4D5e/',
      'https://www.tiktok.com/embed/v2/7300000000000000001',
      'https://www.dailymotion.com/embed/video/x8abc12',
      'https://player.twitch.tv/?video=v2000000001',
      'https://www.twitch.tv/some_channel',
      'https://streamable.com/e/abc123',
    ]) {
      expect(prefetchVideo(url)).toBeUndefined();
    }
    // The dwell counts for them as for YouTube.
    expect(dwellKey({ url: 'https://vimeo.com/76979871', visible: true, enabled: true })).toBe('vimeo:76979871');
    expect(dwellKey({ url: 'https://www.instagram.com/p/C1a2B3c4D5e/', visible: true, enabled: true })).toBeUndefined();
  });
});

describe('lookAheadAllowed', () => {
  it('needs the option and the Download button, and never runs with the sign-in allowed', () => {
    expect(lookAheadAllowed(DEFAULT_VIDEO_OPTIONS)).toBe(true);
    expect(lookAheadAllowed({ ...DEFAULT_VIDEO_OPTIONS, videoPrefetch: false })).toBe(false);
    // With the button off the user does not use Downex on YouTube.
    expect(lookAheadAllowed({ ...DEFAULT_VIDEO_OPTIONS, videoButton: false })).toBe(false);
    // A look-ahead never sends the user's cookies, and one without them is not the click's probe.
    expect(lookAheadAllowed({ ...DEFAULT_VIDEO_OPTIONS, useSignInForVideos: true })).toBe(false);
  });
});

describe('throttledAnswer', () => {
  it("names YouTube's rate limit and bot check, not the refusals of one video", () => {
    for (const [reason, message] of [
      ['site_unavailable', 'site_unavailable: HTTP Error 429: Too Many Requests'],
      ['site_unavailable', 'site_unavailable: The current session has been rate-limited by YouTube for up to an hour.'],
      ['site_unavailable', "site_unavailable: This content isn't available, try again later."],
      ['auth_required', "auth_required: Sign in to confirm you're not a bot."],
    ] as const) {
      expect(throttledAnswer(failure('rejected', message, reason))).toBe(true);
    }
    for (const o of [
      refused('site_unavailable'),
      failure('rejected', 'site_unavailable: Video unavailable. This video is private.', 'site_unavailable'),
      failure('rejected', 'auth_required: Sign in to confirm your age.', 'auth_required'),
      failure('rejected', 'site_error: HTTP Error 429 from the proxy', 'site_error'),
      failure('timeout', 'too many requests'),
      { ok: true, value: info() } as Outcome<VideoInfoResult>,
    ]) {
      expect(throttledAnswer(o)).toBe(false);
    }
  });

  it('pauses a site other than YouTube after any auth_required too', () => {
    const auth = failure('rejected', 'auth_required: This video needs a sign-in.', 'auth_required');
    expect(siteThrottled('vimeo', auth)).toBe(true);
    expect(siteThrottled('youtube', auth)).toBe(false);
    expect(siteThrottled('youtube', failure('rejected', 'site_unavailable: HTTP Error 429', 'site_unavailable'))).toBe(true);
    expect(siteThrottled('vimeo', refused('site_unavailable'))).toBe(false);
    expect(siteThrottled('vimeo', { ok: true, value: info() })).toBe(false);
    expect(lookAheadSite({ key: 'twitch:clip:abc' })).toBe('twitch');
    expect(lookAheadSite({ key: 'page:https://a.example/' })).toBe('page');
  });
});

describe('PrefetchDwell', () => {
  function setup(view: Partial<DwellView> = {}) {
    const v: DwellView = { url: watch(A), visible: true, enabled: true, ...view };
    const fired: string[] = [];
    const dwell = new PrefetchDwell({ read: () => ({ ...v }), fire: (url) => fired.push(url) });
    return { v, fired, dwell };
  }

  it('reports a watch video after 1.5 s in a visible tab, once per stay', () => {
    const { fired, dwell } = setup();
    dwell.sync();
    vi.advanceTimersByTime(PREFETCH_DWELL_MS - 1);
    expect(fired).toEqual([]);
    vi.advanceTimersByTime(1);
    expect(fired).toEqual([watch(A)]);
    // The page's many re-syncs (mutations, loadstart) never report it again.
    for (let i = 0; i < 10; i++) {
      dwell.sync();
      vi.advanceTimersByTime(PREFETCH_DWELL_MS);
    }
    expect(fired).toEqual([watch(A)]);
  });

  it('starts again on SPA navigation, so Shorts swiped past are never reported', () => {
    const { v, fired, dwell } = setup({ url: shorts(A) });
    dwell.sync();
    vi.advanceTimersByTime(1000);
    v.url = shorts(B);
    dwell.sync();
    vi.advanceTimersByTime(1000);
    v.url = shorts(C);
    dwell.sync();
    vi.advanceTimersByTime(PREFETCH_DWELL_MS - 1);
    expect(fired).toEqual([]);
    vi.advanceTimersByTime(1);
    expect(fired).toEqual([shorts(C)]);
    // A watch page after it: its own dwell.
    v.url = watch(D);
    dwell.sync();
    vi.advanceTimersByTime(PREFETCH_DWELL_MS);
    expect(fired).toEqual([shorts(C), watch(D)]);
    // Back to the first Short: another stay.
    v.url = shorts(A);
    dwell.sync();
    vi.advanceTimersByTime(PREFETCH_DWELL_MS);
    expect(fired).toEqual([shorts(C), watch(D), shorts(A)]);
  });

  it('counts only time in a visible tab', () => {
    const { v, fired, dwell } = setup({ visible: false });
    dwell.sync();
    vi.advanceTimersByTime(10 * PREFETCH_DWELL_MS);
    expect(fired).toEqual([]);
    v.visible = true;
    dwell.sync();
    vi.advanceTimersByTime(1000);
    // Hidden before the dwell ended: the time so far does not count.
    v.visible = false;
    dwell.sync();
    vi.advanceTimersByTime(10 * PREFETCH_DWELL_MS);
    expect(fired).toEqual([]);
    v.visible = true;
    dwell.sync();
    vi.advanceTimersByTime(PREFETCH_DWELL_MS - 1);
    expect(fired).toEqual([]);
    vi.advanceTimersByTime(1);
    expect(fired).toEqual([watch(A)]);
  });

  it('never reports with the option off, in a frame or off a watch page', () => {
    const { v, fired, dwell } = setup({ enabled: false });
    dwell.sync();
    vi.advanceTimersByTime(10 * PREFETCH_DWELL_MS);
    for (const url of [undefined, `https://www.youtube.com/embed/${A}`, `https://www.youtube.com/live/${A}`, 'https://www.youtube.com/feed/library']) {
      v.enabled = true;
      v.url = url;
      dwell.sync();
      vi.advanceTimersByTime(10 * PREFETCH_DWELL_MS);
    }
    expect(fired).toEqual([]);
    // Turned on while a video plays: the dwell starts then.
    v.url = watch(B);
    v.enabled = false;
    dwell.sync();
    vi.advanceTimersByTime(PREFETCH_DWELL_MS);
    v.enabled = true;
    dwell.sync();
    vi.advanceTimersByTime(PREFETCH_DWELL_MS);
    expect(fired).toEqual([watch(B)]);
  });

  it('checks the address again when the dwell ends', () => {
    const { v, fired, dwell } = setup();
    dwell.sync();
    vi.advanceTimersByTime(1000);
    // The page moved on before any event said so.
    v.url = shorts(B);
    vi.advanceTimersByTime(500);
    expect(fired).toEqual([]);
    vi.advanceTimersByTime(PREFETCH_DWELL_MS - 1);
    expect(fired).toEqual([]);
    vi.advanceTimersByTime(1);
    expect(fired).toEqual([shorts(B)]);
  });

  it('stops for good', () => {
    const { fired, dwell } = setup();
    dwell.sync();
    dwell.stop();
    vi.advanceTimersByTime(10 * PREFETCH_DWELL_MS);
    expect(fired).toEqual([]);
  });
});

describe('lookAheadRequest', () => {
  const sender = (url: string, extra: Record<string, unknown> = {}, tab: Record<string, unknown> = {}) => ({
    url,
    origin: new URL(url).origin,
    frameId: 0,
    tab: { id: 4, url, active: true, ...tab },
    ...extra,
  });

  it('takes the top frame video of the shown tab', () => {
    expect(lookAheadRequest(sender(watch(A)), `${watch(A)}&t=3s`)).toEqual({
      tabId: 4,
      target: { key: `youtube:${A}`, pageUrl: watch(A), storeId: '', generic: false },
      host: 'www.youtube.com',
    });
    expect(lookAheadRequest(sender(shorts(B), {}, { cookieStoreId: 'firefox-container-2', title: 'S' }), shorts(B))).toEqual({
      tabId: 4,
      target: { key: `youtube:${B}`, pageUrl: watch(B), storeId: 'firefox-container-2', generic: false, title: 'S' },
      host: 'www.youtube.com',
    });
    // A curated site's video.
    const reddit = 'https://www.reddit.com/r/videos/comments/1abc2de/some_title/';
    expect(lookAheadRequest(sender(reddit), reddit)).toEqual({
      tabId: 4,
      target: { key: 'reddit:1abc2de', pageUrl: 'https://www.reddit.com/r/videos/comments/1abc2de/', storeId: '', generic: false },
      host: 'www.reddit.com',
    });
    // Chromium names no `active` on some senders: the content script saw the tab visible.
    expect(lookAheadRequest(sender(watch(A), {}, { active: undefined }), watch(A))).toBeDefined();
  });

  it('refuses frames, hidden tabs, private windows, other origins and other pages', () => {
    expect(lookAheadRequest(sender(watch(A), { frameId: 3 }), watch(A))).toBeUndefined();
    expect(lookAheadRequest(sender(watch(A), {}, { active: false }), watch(A))).toBeUndefined();
    expect(lookAheadRequest(sender(watch(A), {}, { incognito: true }), watch(A))).toBeUndefined();
    expect(lookAheadRequest(sender('https://m.youtube.com/'), watch(A))).toBeUndefined();
    expect(lookAheadRequest(sender(`https://www.youtube.com/live/${A}`), `https://www.youtube.com/live/${A}`)).toBeUndefined();
    expect(lookAheadRequest({ url: watch(A), frameId: 0 }, watch(A))).toBeUndefined();
    // Never Facebook or Instagram, never a generic page.
    const fb = 'https://www.facebook.com/watch/?v=1234567890';
    expect(lookAheadRequest(sender(fb), fb)).toBeUndefined();
    expect(lookAheadRequest(sender('https://news.example/story'), 'https://news.example/story')).toBeUndefined();
  });

  it('knows whether the tab still shows the video', () => {
    const target: VideoTarget = { key: `youtube:${A}`, pageUrl: watch(A), storeId: '', generic: false };
    expect(stillWatched({ id: 4, url: `${watch(A)}&t=9s`, active: true }, target)).toBe(true);
    expect(stillWatched({ id: 4, url: shorts(A) }, target)).toBe(true);
    expect(stillWatched({ id: 4, url: watch(B), active: true }, target)).toBe(false);
    expect(stillWatched({ id: 4, url: watch(A), active: false }, target)).toBe(false);
    expect(stillWatched({ id: 4, url: watch(A), incognito: true }, target)).toBe(false);
  });
});

describe('VideoLookAhead', () => {
  const req = (id: string, tabId = 1): LookAheadRequest => ({
    tabId,
    target: { key: `youtube:${id}`, pageUrl: watch(id), storeId: '', generic: false },
    host: 'www.youtube.com',
  });
  const vimeo = (id: string, tabId = 1): LookAheadRequest => ({
    tabId,
    target: { key: `vimeo:${id}`, pageUrl: `https://vimeo.com/${id}`, storeId: '', generic: false },
    host: 'vimeo.com',
  });

  function setup() {
    const state = { enabled: true, ready: true, shown: true };
    const answers = new Map<string, ReturnType<typeof deferred<Outcome<VideoInfoResult> | undefined>>>();
    const run = vi.fn((t: VideoTarget) => {
      const d = deferred<Outcome<VideoInfoResult> | undefined>();
      answers.set(t.key, d);
      return d.promise;
    });
    const stillShown = vi.fn(async (_r: LookAheadRequest) => state.shown);
    const enabled = vi.fn(async (_r: LookAheadRequest) => state.enabled);
    const ahead = new VideoLookAhead({
      enabled,
      ready: () => state.ready,
      run,
      stillShown,
    });
    const sent = () => run.mock.calls.map(([t]) => t.key.slice(t.key.indexOf(':') + 1));
    const answer = async (id: string, o: Outcome<VideoInfoResult> | undefined, site = 'youtube') => {
      answers.get(`${site}:${id}`)!.resolve(o);
      await vi.advanceTimersByTimeAsync(0);
    };
    return { state, run, stillShown, enabled, ahead, sent, answer };
  }

  it('asks whether each look-ahead is enabled for its tab', async () => {
    const { ahead, enabled, answer } = setup();
    ahead.request(vimeo('76979871'));
    await vi.advanceTimersByTimeAsync(0);
    expect(enabled).toHaveBeenCalledWith(vimeo('76979871'));
    await answer('76979871', { ok: true, value: info() }, 'vimeo');
  });

  it('pauses only the site that throttled or needs a sign-in', async () => {
    const { ahead, sent, answer } = setup();
    ahead.request(vimeo('1'));
    // Waiting: another site's video is not dropped by Vimeo's answer.
    ahead.request(req(A));
    await vi.advanceTimersByTimeAsync(0);
    await answer('1', failure('rejected', 'auth_required: log in to watch', 'auth_required'), 'vimeo');
    expect(ahead.isThrottled('vimeo')).toBe(true);
    expect(ahead.isThrottled('youtube')).toBe(false);
    expect(ahead.throttled).toBe(true);
    expect(sent()).toEqual(['1', A]);
    await answer(A, failure('rejected', 'auth_required: Sign in to confirm your age.', 'auth_required'));
    // YouTube's own auth_required is about one video.
    expect(ahead.isThrottled('youtube')).toBe(false);
    ahead.request(vimeo('2'));
    ahead.request(req(B));
    await vi.advanceTimersByTimeAsync(0);
    expect(sent()).toEqual(['1', A, B]);
    await answer(B, { ok: true, value: info() });
    await vi.advanceTimersByTimeAsync(PREFETCH_THROTTLE_MS);
    expect(ahead.throttled).toBe(false);
    ahead.request(vimeo('2'));
    await vi.advanceTimersByTimeAsync(0);
    expect(sent()).toEqual(['1', A, B, '2']);
  });

  it('sends one at a time, the newest waiting video next', async () => {
    const { ahead, sent, answer, stillShown } = setup();
    ahead.request(req(A));
    await vi.advanceTimersByTimeAsync(0);
    expect(sent()).toEqual([A]);
    // The user moves on twice while A runs: only the newest waits.
    ahead.request(req(B));
    ahead.request(req(C, 2));
    // The video in flight is not asked for twice.
    ahead.request(req(A));
    await vi.advanceTimersByTimeAsync(0);
    expect(sent()).toEqual([A]);
    expect(ahead.waiting?.key).toBe(`youtube:${C}`);
    await answer(A, { ok: true, value: info() });
    expect(sent()).toEqual([A, C]);
    // A waiting one is sent only while its tab still shows it.
    expect(stillShown).toHaveBeenCalledWith(req(C, 2));
    await answer(C, { ok: true, value: info() });
    expect(ahead.running).toBeUndefined();
    ahead.request(req(D));
    await vi.advanceTimersByTimeAsync(0);
    expect(sent()).toEqual([A, C, D]);
  });

  it('frees the slot at once when nothing was sent (kept answer, probe running)', async () => {
    const { ahead, sent, answer } = setup();
    ahead.request(req(A));
    ahead.request(req(B));
    await vi.advanceTimersByTimeAsync(0);
    await answer(A, undefined);
    expect(sent()).toEqual([A, B]);
  });

  it('drops a waiting video the tab no longer shows', async () => {
    const { state, ahead, sent, answer } = setup();
    ahead.request(req(A));
    ahead.request(req(B));
    await vi.advanceTimersByTimeAsync(0);
    state.shown = false;
    await answer(A, { ok: true, value: info() });
    expect(sent()).toEqual([A]);
    expect(ahead.running).toBeUndefined();
  });

  it('sends nothing while the relay or downexd is not known to work, or with the option off', async () => {
    const { state, ahead, sent, answer } = setup();
    state.ready = false;
    ahead.request(req(A));
    await vi.advanceTimersByTimeAsync(0);
    expect(sent()).toEqual([]);
    expect(ahead.running).toBeUndefined();
    state.ready = true;
    state.enabled = false;
    ahead.request(req(A));
    await vi.advanceTimersByTimeAsync(0);
    expect(sent()).toEqual([]);
    expect(ahead.running).toBeUndefined();
    state.enabled = true;
    ahead.request(req(A));
    // The relay went away while A ran: the waiting one is dropped.
    ahead.request(req(B));
    await vi.advanceTimersByTimeAsync(0);
    state.ready = false;
    await answer(A, refused('site_resolving'));
    expect(sent()).toEqual([A]);
  });

  it.each(['tools_unavailable', 'tools_installing', 'site_media_off'])('backs off after %s until the user asks once', async (reason) => {
    const { ahead, sent, answer } = setup();
    ahead.request(req(A));
    ahead.request(req(B));
    await vi.advanceTimersByTimeAsync(0);
    await answer(A, refused(reason));
    expect(ahead.backedOff).toBe(true);
    // The waiting one and every later one are dropped …
    ahead.request(req(C));
    await vi.advanceTimersByTimeAsync(0);
    expect(sent()).toEqual([A]);
    // … until the user clicks Download once.
    ahead.userAsked();
    expect(ahead.backedOff).toBe(false);
    ahead.request(req(C));
    await vi.advanceTimersByTimeAsync(0);
    expect(sent()).toEqual([A, C]);
  });

  it(`sends at most ${PREFETCH_BURST} at once, then one a minute; one that sent nothing costs nothing`, async () => {
    const { ahead, run, sent, answer } = setup();
    const id = (i: number) => `v${String(i).padStart(10, '0')}`;
    const watchFor = async (i: number) => {
      const before = run.mock.calls.length;
      ahead.request(req(id(i)));
      await vi.advanceTimersByTimeAsync(0);
      if (run.mock.calls.length > before) await answer(id(i), { ok: true, value: info() });
    };
    // Answers already kept (nothing sent) take nothing from the budget.
    for (let i = 0; i < PREFETCH_BURST + 2; i++) {
      ahead.request(req(A));
      await vi.advanceTimersByTimeAsync(0);
      await answer(A, undefined);
    }
    for (let i = 0; i < PREFETCH_BURST + 2; i++) await watchFor(i);
    const burst = Array.from({ length: PREFETCH_BURST }, (_, i) => id(i));
    expect(sent()).toEqual([...Array<string>(PREFETCH_BURST + 2).fill(A), ...burst]);
    await vi.advanceTimersByTimeAsync(PREFETCH_REFILL_MS - 1);
    await watchFor(100);
    expect(sent().slice(PREFETCH_BURST + 2)).toEqual(burst);
    await vi.advanceTimersByTimeAsync(1);
    await watchFor(101);
    await watchFor(102);
    expect(sent().slice(PREFETCH_BURST + 2)).toEqual([...burst, id(101)]);
    // A quiet hour fills the budget again, but never beyond the burst.
    await vi.advanceTimersByTimeAsync(60 * PREFETCH_REFILL_MS);
    for (let i = 200; i < 200 + PREFETCH_BURST + 1; i++) await watchFor(i);
    expect(sent().length).toBe(PREFETCH_BURST + 2 + PREFETCH_BURST + 1 + PREFETCH_BURST);
  });

  it(`pauses ${PREFETCH_THROTTLE_MS / 60_000} minutes after YouTube throttled one, whatever the user does`, async () => {
    const { ahead, sent, answer } = setup();
    ahead.request(req(A));
    ahead.request(req(B));
    await vi.advanceTimersByTimeAsync(0);
    await answer(A, failure('rejected', "auth_required: Sign in to confirm you're not a bot.", 'auth_required'));
    expect(ahead.throttled).toBe(true);
    expect(sent()).toEqual([A]);
    ahead.userAsked();
    ahead.request(req(C));
    await vi.advanceTimersByTimeAsync(PREFETCH_THROTTLE_MS - 1);
    ahead.request(req(C));
    await vi.advanceTimersByTimeAsync(0);
    expect(sent()).toEqual([A]);
    await vi.advanceTimersByTimeAsync(1);
    expect(ahead.throttled).toBe(false);
    ahead.request(req(C));
    await vi.advanceTimersByTimeAsync(0);
    expect(sent()).toEqual([A, C]);
  });

  it('keeps going after answers that are not about the tools', async () => {
    const { ahead, sent, answer } = setup();
    for (const [id, o] of [
      [A, refused('site_resolving')],
      [B, refused('live')],
      [D, failure('rejected', 'site_unavailable: Video unavailable. This video is private.', 'site_unavailable')],
      [C, failure('timeout', 'no probeVideo response within 28000 ms')],
    ] as const) {
      ahead.request(req(id));
      await vi.advanceTimersByTimeAsync(0);
      await answer(id, o);
      expect(ahead.backedOff).toBe(false);
      expect(ahead.throttled).toBe(false);
    }
    expect(sent()).toEqual([A, B, D, C]);
  });
});
