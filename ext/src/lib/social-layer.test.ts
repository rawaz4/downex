// @vitest-environment happy-dom
// The compact button and every video surface of social sites (VIDEO-DETECT-SPEC §5.1 "Social
// sites"): its size and free corner, the video the page shows under the pointer, grid tiles, and
// what the button tells (hint, working, sent).

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { ExtensionMessage } from './messages';
import type { FrameOffers, FrameVideoMeta } from './offer-types';
import {
  CORNER_RECHECK_MS,
  hintText,
  infoHint,
  LAYER_CSS,
  LAYER_TAG,
  OverlayLayer,
  type OverlayLayerDeps,
  placeSocial,
  type RectLike,
  SENT_LABEL,
  socialSize,
  TILE_ID,
  TILE_LABEL,
  topmostAt,
} from './overlay-host';
import type { VideoInfoResult, VideoTrack } from './protocol';
import { OVERLAY_CSS, REVEAL_ACTIVITY_MS, SENT_CLOSE_MS } from './video-overlay';
import type { TrackedVideo, TrackerApi, TrackerEvent, VideoTileTarget } from './video-tracker';

function rect(left: number, top: number, width: number, height: number): RectLike & DOMRect {
  return { left, top, width, height, right: left + width, bottom: top + height, x: left, y: top, toJSON: () => ({}) } as RectLike & DOMRect;
}

function meta(id: number, over: Partial<FrameVideoMeta> = {}): FrameVideoMeta {
  return {
    id,
    src: `https://cdn.example.com/${id}.mp4`,
    mse: false,
    width: 405,
    height: 720,
    videoWidth: 1080,
    videoHeight: 1920,
    durationMs: 15_000,
    drm: false,
    visible: true,
    decorative: false,
    meaningful: true,
    ...over,
  };
}

function videoAt(r: RectLike & DOMRect, parent: Element = document.body): HTMLVideoElement {
  const v = document.createElement('video');
  v.getBoundingClientRect = () => r;
  parent.append(v);
  return v;
}

function track(t: Partial<VideoTrack> & Pick<VideoTrack, 'id' | 'kind'>): VideoTrack {
  return { ext: 'mp4', codecs: '', width: 0, height: 0, frameRate: 0, bandwidth: 0, size: 0, sizeApprox: false, language: '', name: '', ...t };
}

const INFO: VideoInfoResult = {
  type: 'videoInfo',
  site: 'TikTok',
  title: 'A city waking up',
  durationMs: 15_000,
  format: 'http',
  container: 'mp4',
  selectedVideo: 'h1080',
  selectedAudio: '',
  tracks: [
    track({ id: 'h1080', kind: 'muxed', width: 1080, height: 1920, size: 6_400_000, name: '1080p (Full HD)' }),
    track({ id: 'h720', kind: 'muxed', width: 720, height: 1280, size: 3_100_000, name: '720p (HD)' }),
  ],
};

const FILE_OFFERS: FrameOffers = {
  title: 'Clip',
  topHost: 'www.tiktok.com',
  files: [
    { key: 'f1', display: 'cdn.example.com/1.mp4', size: 30_000_000, sizeKnown: true, filename: '1.mp4', frame: 'this', matched: true, short: false, width: 640, height: 360, durationMs: 15_000 },
  ],
  streams: [],
  features: { streams: true, video: true },
  drm: false,
  live: false,
};

const MEMORY: FrameOffers = {
  ...FILE_OFFERS,
  files: [],
  site: { key: 'tiktok:7300000000000000001', site: 'TikTok', kind: 'watch', pageUrl: 'https://www.tiktok.com/@someone/video/7300000000000000001', curated: true, probe: { result: { ok: true, value: INFO } } },
};

const trusted = new WeakSet<Event>();

function setup(videos: TrackedVideo[], over: Partial<OverlayLayerDeps> = {}, offers: FrameOffers = FILE_OFFERS) {
  const listeners = new Set<(e: TrackerEvent) => void>();
  const sent: ExtensionMessage[] = [];
  let tile: VideoTileTarget | undefined;
  const api: TrackerApi = {
    version: 1,
    videos: () => videos,
    onChange: (fn) => {
      listeners.add(fn);
      return () => listeners.delete(fn);
    },
    send: async (msg) => {
      sent.push(msg);
      if (msg.type === 'downex:frame-offers') return { ok: true, value: offers } as never;
      if (msg.type === 'downex:offer-add') return { ok: true, value: { type: 'added', downloadId: 3, awaiting: true } } as never;
      if (msg.type === 'downex:page-video-probe') return { ok: true, value: INFO } as never;
      return undefined;
    },
    invalidated: false,
    allowed: true,
    recheck: (id) => videos.find((v) => v.id === id)?.meta,
    refreshState: () => undefined,
    claimUi: () => true,
    requested: undefined,
    release: () => undefined,
    get tile() {
      return tile?.el.isConnected ? tile : undefined;
    },
    shown: () => undefined,
  };
  const frames: (() => void)[] = [];
  const layer = new OverlayLayer({
    doc: document,
    api,
    raf: (cb) => frames.push(cb),
    isTrusted: (e) => trusted.has(e),
    location: () => 'https://www.tiktok.com/@someone/video/7300000000000000001',
    elementsAt: () => [],
    ...over,
  });
  const runFrames = () => {
    for (const f of frames.splice(0)) f();
  };
  const click = (el: Element) => {
    const e = new MouseEvent('click', { bubbles: true, composed: true });
    trusted.add(e);
    el.dispatchEvent(e);
  };
  const point = (x: number, y: number) => {
    document.dispatchEvent(new MouseEvent('pointermove', { clientX: x, clientY: y }));
    runFrames();
  };
  const emit = (e: TrackerEvent) => {
    for (const fn of [...listeners]) fn(e);
  };
  return { layer, sent, runFrames, click, point, emit, setTile: (t: VideoTileTarget | undefined) => (tile = t) };
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'] });
  document.body.replaceChildren();
  Object.defineProperty(document.documentElement, 'clientWidth', { value: 1200, configurable: true });
  Object.defineProperty(document.documentElement, 'clientHeight', { value: 800, configurable: true });
});
afterEach(() => {
  vi.useRealTimers();
  for (const el of document.querySelectorAll(LAYER_TAG)) el.remove();
  delete (document.body as Partial<Pick<HTMLElement, 'getBoundingClientRect'>>).getBoundingClientRect;
});

describe('the compact button’s place (§5.1 "A free corner")', () => {
  const free = () => undefined;

  it('is 34 px with an inset of 10 px over a vertical player, 28 px with 6 px over a card or tile', () => {
    expect(socialSize(rect(0, 0, 405, 720))).toBe('small');
    expect(socialSize(rect(0, 0, 259, 400))).toBe('tiny');
    expect(socialSize(rect(0, 0, 400, 219))).toBe('tiny');
    expect(placeSocial(rect(400, 40, 405, 720), 1200, 800, free)).toEqual({ top: 50, side: 'right', x: 405, size: 'small', corner: 0, maxWidth: 385 });
    expect(placeSocial(rect(100, 100, 200, 200), 1200, 800, free)).toEqual({ top: 106, side: 'right', x: 906, size: 'tiny', corner: 0, maxWidth: 188 });
  });

  it('takes the top-left corner when the site’s control holds the top-right one', () => {
    const control = rect(760, 50, 36, 36);
    const blocker = (box: RectLike) => (box.right > control.left && box.left < control.right && box.top < control.bottom ? control : undefined);
    expect(placeSocial(rect(400, 40, 405, 720), 1200, 800, blocker)).toMatchObject({ top: 50, side: 'left', x: 410, corner: 1 });
  });

  it('goes under the control that holds a top corner, within the upper part, then to an edge’s middle', () => {
    const topBar = rect(400, 40, 405, 60); // a story's header across the whole top
    const under = (box: RectLike) => (box.top < topBar.bottom ? topBar : undefined);
    expect(placeSocial(rect(400, 40, 405, 720), 1200, 800, under)).toMatchObject({ top: 110, side: 'right', corner: 2 });
    // A rail down the right edge and a header: the left edge under the header.
    const rail = rect(760, 40, 45, 720);
    const both = (box: RectLike) => (box.top < topBar.bottom ? topBar : box.right > rail.left ? rail : undefined);
    expect(placeSocial(rect(400, 40, 405, 720), 1200, 800, both)).toMatchObject({ top: 110, side: 'left', corner: 3 });
    // The header is too tall to go under (past the upper 45 %): the middle of an edge.
    const tall = rect(400, 40, 405, 300);
    const deep = (box: RectLike) => (box.top < tall.bottom ? tall : undefined);
    expect(placeSocial(rect(400, 40, 405, 720), 1200, 800, deep)).toMatchObject({ top: 383, side: 'right', corner: 4 });
  });

  it('has no place when every corner covers a control, or the visible part is too small', () => {
    expect(placeSocial(rect(400, 40, 405, 720), 1200, 800, (box) => box)).toBeUndefined();
    expect(placeSocial(rect(0, 0, 30, 30), 1200, 800, free)).toBeUndefined();
    expect(placeSocial(rect(0, 900, 405, 720), 1200, 800, free)).toBeUndefined();
  });
});

describe('the video the page shows under the pointer (§5.1)', () => {
  it('is the one whose player holds the topmost element; none when something else covers them', () => {
    const sized = <E extends Element>(el: E, r: DOMRect): E => {
      el.getBoundingClientRect = () => r;
      return el;
    };
    const page = sized(document.createElement('main'), rect(0, 0, 1200, 4000));
    const feedCard = sized(document.createElement('div'), rect(300, 0, 470, 860));
    const feedVideo = sized(document.createElement('video'), rect(300, 60, 470, 760));
    feedCard.append(feedVideo);
    const modal = sized(document.createElement('div'), rect(0, 0, 1200, 800));
    const modalPlayer = sized(document.createElement('div'), rect(350, 100, 405, 600));
    const modalVideo = sized(document.createElement('video'), rect(350, 100, 405, 600));
    const controls = sized(document.createElement('div'), rect(350, 640, 405, 60));
    const backdrop = sized(document.createElement('div'), rect(0, 0, 1200, 800));
    modalPlayer.append(modalVideo, controls);
    modal.append(backdrop, modalPlayer);
    page.append(feedCard, modal);
    document.body.append(page);
    const candidates = [
      { id: 1, el: feedVideo },
      { id: 2, el: modalVideo },
    ];
    expect(topmostAt(candidates, controls)?.id).toBe(2);
    expect(topmostAt(candidates, feedVideo)?.id).toBe(1);
    // The viewer's backdrop over the feed: no player holds it (the page around them is not one).
    expect(topmostAt(candidates, backdrop)).toBeUndefined();
    expect(topmostAt([candidates[0]!], backdrop)).toBeUndefined();
    expect(topmostAt(candidates, undefined)).toBeUndefined();
  });

  it('crosses shadow roots from a video to its host', () => {
    const host = document.createElement('div');
    const root = host.attachShadow({ mode: 'open' });
    const v = document.createElement('video');
    root.append(v);
    const overlay = document.createElement('span');
    host.append(overlay);
    document.body.append(host);
    expect(topmostAt([{ id: 1, el: v }], overlay)?.id).toBe(1);
  });
});

describe('the hint (§5.1 "What the button tells")', () => {
  it('names the quality up to its detail, with the size', () => {
    expect(hintText('1080p (Full HD) 60fps', '6.4 MB')).toBe('1080p · 6.4 MB');
    expect(hintText('4K (2160p)', '~1.2 GB')).toBe('4K · ~1.2 GB');
    expect(hintText('360p · MP4', '')).toBe('360p');
    expect(hintText('This video', '30 MB')).toBeUndefined();
    expect(hintText('Audio · AAC', '3 MB')).toBeUndefined();
    expect(infoHint(INFO)).toBe('1080p · 6.4 MB');
    expect(infoHint({ ...INFO, tracks: [] })).toBeUndefined();
  });
});

describe('the compact layer', () => {
  it('draws the compact mark on social sites only, and leaves YouTube’s button rules alone', () => {
    const el = videoAt(rect(400, 40, 405, 720));
    const card = videoAt(rect(40, 40, 220, 300));
    const { layer, runFrames } = setup([
      { id: 1, el, meta: meta(1) },
      { id: 2, el: card, meta: meta(2, { width: 220, height: 300 }) },
    ]);
    expect(layer.compact).toBe(true);
    layer.mount();
    runFrames();
    const big = layer.buttons.get(1)!;
    const small = layer.buttons.get(2)!;
    expect(big.dataset.social).toBe('true');
    expect(big.dataset.size).toBe('small');
    expect(small.dataset.size).toBe('tiny');
    expect(big.dataset.compact).toBeUndefined();
    expect(big.style.right).toBe('405px');
    expect(big.style.top).toBe('50px');
    expect(big.style.getPropertyValue('--dx-max')).toBe('385px');
    expect(big.getAttribute('aria-label')).toBe('Download this video with Downex');
    expect(big.querySelector('.dx-label')!.textContent).toBe('Download');
    // The rules are the layer's own, never YouTube's overlay's.
    expect(LAYER_CSS).toContain(".dx-button[data-social='true']");
    expect(OVERLAY_CSS).not.toContain('data-social');
    expect(LAYER_CSS).toMatch(/\.dx-button\[data-social='true'\]:is\(:hover, :focus-visible, \[aria-expanded='true'\], \[data-wide='true'\]\) \.dx-label \{ max-width: 22em; opacity: 1; \}/);

    const other = setup([{ id: 1, el: videoAt(rect(0, 0, 640, 360)), meta: meta(1) }], { location: () => 'https://news.example.com/story' });
    other.layer.mount();
    other.runFrames();
    expect(other.layer.compact).toBe(false);
    expect(other.layer.buttons.get(1)!.dataset.social).toBeUndefined();
  });

  it('moves to a free corner beside the site’s controls, and checks it again only while shown', () => {
    const player = document.createElement('div');
    document.body.append(player);
    const el = videoAt(rect(400, 40, 405, 720), player);
    const more = document.createElement('button');
    more.getBoundingClientRect = () => rect(760, 46, 40, 40);
    player.append(more);
    const elementsAt = vi.fn((x: number, y: number) => (x >= 760 && x <= 800 && y >= 46 && y <= 86 ? [more] : [el]));
    const { layer, runFrames, point } = setup([{ id: 1, el, meta: meta(1) }], { elementsAt });
    layer.mount();
    runFrames();
    const b = layer.buttons.get(1)!;
    expect(b.style.left).toBe('410px');
    expect(b.style.right).toBe('auto');
    // Hidden and unchanged: no hit test on the next placements.
    const calls = elementsAt.mock.calls.length;
    layer.schedulePlace();
    runFrames();
    expect(elementsAt.mock.calls.length).toBe(calls);
    // The pointer over the video: it is shown, and the corner is checked again after a while only.
    point(600, 400);
    expect(b.dataset.shown).toBe('true');
    const shownCalls = elementsAt.mock.calls.length;
    layer.schedulePlace();
    runFrames();
    expect(elementsAt.mock.calls.length).toBe(shownCalls);
    vi.advanceTimersByTime(CORNER_RECHECK_MS);
    layer.schedulePlace();
    runFrames();
    expect(elementsAt.mock.calls.length).toBeGreaterThan(shownCalls);
    expect(b.style.left).toBe('410px');
  });

  it('reveals only the video the page shows under the pointer (a post viewer over a feed)', () => {
    // The page around the players is far larger than either.
    document.body.getBoundingClientRect = () => rect(0, 0, 1200, 4000);
    const feed = document.createElement('div');
    const viewer = document.createElement('div');
    document.body.append(feed, viewer);
    const under = videoAt(rect(300, 0, 470, 800), feed);
    const over = videoAt(rect(350, 100, 405, 600), viewer);
    const viewerControls = document.createElement('div');
    viewer.append(viewerControls);
    let hit: Element = viewerControls;
    const { layer, runFrames, point } = setup(
      [
        { id: 1, el: under, meta: meta(1, { width: 470, height: 800 }) },
        { id: 2, el: over, meta: meta(2) },
      ],
      { elementsAt: () => [hit] },
    );
    layer.mount();
    runFrames();
    point(500, 300);
    expect(layer.buttons.get(2)!.dataset.shown).toBe('true');
    expect(layer.buttons.get(1)!.dataset.shown).toBe('false');
    // A backdrop the page lays over both: neither.
    const backdrop = document.createElement('div');
    document.body.append(backdrop);
    hit = backdrop;
    vi.advanceTimersByTime(REVEAL_ACTIVITY_MS);
    point(510, 310);
    expect(layer.buttons.get(1)!.dataset.shown).toBe('false');
    expect(layer.buttons.get(2)!.dataset.shown).toBe('false');
    backdrop.remove();
  });

  it('reveals no button of a video that starts under something the page laid over it', () => {
    document.body.getBoundingClientRect = () => rect(0, 0, 1200, 4000);
    const feed = document.createElement('div');
    document.body.append(feed);
    const el = videoAt(rect(300, 0, 470, 800), feed);
    const backdrop = document.createElement('div');
    document.body.append(backdrop);
    let hit: Element = backdrop;
    const { layer, runFrames } = setup([{ id: 1, el, meta: meta(1, { width: 470, height: 800 }), startedAt: Date.now() }], { elementsAt: () => [hit] });
    layer.mount();
    runFrames();
    expect(layer.buttons.get(1)!.dataset.shown).toBe('false');
    layer.reveal(1, REVEAL_ACTIVITY_MS);
    expect(layer.buttons.get(1)!.dataset.shown).toBe('false');
    // Nothing over it: a start shows it as on any site.
    hit = el;
    layer.reveal(1, REVEAL_ACTIVITY_MS);
    expect(layer.buttons.get(1)!.dataset.shown).toBe('true');
  });

  it('tells the best quality and size from the background’s memory, asked once while hovered', async () => {
    const el = videoAt(rect(400, 40, 405, 720));
    const { layer, runFrames, point, sent } = setup([{ id: 1, el, meta: meta(1) }], { elementsAt: () => [el] }, MEMORY);
    layer.mount();
    runFrames();
    point(600, 400);
    await vi.advanceTimersByTimeAsync(0);
    const b = layer.buttons.get(1)!;
    expect(sent.filter((m) => m.type === 'downex:frame-offers')).toHaveLength(1);
    expect(sent.some((m) => m.type === 'downex:page-video-probe' || m.type === 'downex:page-video-prefetch')).toBe(false);
    expect(b.querySelector('.dx-label')!.textContent).toBe('Download · 1080p · 6.4 MB');
    expect(b.getAttribute('aria-label')).toBe('Download this video with Downex, 1080p, 6.4 MB');
    point(610, 410);
    await vi.advanceTimersByTimeAsync(0);
    expect(sent.filter((m) => m.type === 'downex:frame-offers')).toHaveLength(1);
  });

  it('shortens what it tells to fit a small card: the hint alone', async () => {
    const el = videoAt(rect(400, 40, 200, 300));
    const { layer, runFrames, point } = setup([{ id: 1, el, meta: meta(1, { width: 200, height: 300 }) }], { elementsAt: () => [el] }, MEMORY);
    layer.mount();
    runFrames();
    point(500, 200);
    await vi.advanceTimersByTimeAsync(0);
    const b = layer.buttons.get(1)!;
    expect(b.dataset.size).toBe('tiny');
    expect(b.querySelector('.dx-label')!.textContent).toBe('1080p · 6.4 MB');
    expect(b.getAttribute('aria-label')).toBe('Download this video with Downex, 1080p, 6.4 MB');
  });

  it('asks nothing on a feed for a video whose post no click found', async () => {
    const el = videoAt(rect(400, 40, 405, 720));
    const { layer, runFrames, point, sent } = setup([{ id: 1, el, meta: meta(1) }], { location: () => 'https://www.tiktok.com/foryou', elementsAt: () => [el] }, MEMORY);
    layer.mount();
    runFrames();
    point(600, 400);
    await vi.advanceTimersByTimeAsync(0);
    expect(sent).toHaveLength(0);
    expect(layer.buttons.get(1)!.querySelector('.dx-label')!.textContent).toBe('Download');
  });

  it('turns while it works, says it was sent, keeps the check, and forgets it for another video', async () => {
    const el = videoAt(rect(400, 40, 405, 720));
    const { layer, runFrames, click } = setup([{ id: 1, el, meta: meta(1) }]);
    layer.mount();
    runFrames();
    const b = layer.buttons.get(1)!;
    click(b);
    expect(b.dataset.state).toBe('working');
    await vi.advanceTimersByTimeAsync(0);
    // The file row named its quality: the hint is learnt from the panel.
    expect(b.querySelector('.dx-label')!.textContent).toBe('Download · 360p · 30.0 MB');
    click(layer.panelDom.el.querySelector('button.dx-row')!);
    await vi.advanceTimersByTimeAsync(0);
    expect(b.dataset.state).toBe('sent');
    expect(b.dataset.wide).toBe('true');
    expect(b.querySelector('.dx-label')!.textContent).toBe(SENT_LABEL);
    expect(b.getAttribute('aria-label')).toBe('Download this video with Downex, 360p, 30.0 MB, sent to Downex');
    expect(b.querySelector('.dx-sent')).not.toBeNull();
    await vi.advanceTimersByTimeAsync(SENT_CLOSE_MS);
    expect(layer.panel.isOpen).toBe(false);
    expect(b.dataset.wide).toBeUndefined();
    expect(b.dataset.state).toBe('sent');
    expect(b.querySelector('.dx-label')!.textContent).toBe('Download · 360p · 30.0 MB');
    // The player reused its <video> for the next post.
    layer.emptied(1);
    expect(b.dataset.state).toBeUndefined();
    expect(b.querySelector('.dx-label')!.textContent).toBe('Download');
  });

  it('keeps a tile’s own badge in view: the button takes the other corner', () => {
    const tile = document.createElement('a');
    tile.href = 'https://www.instagram.com/reel/AbCdEf12345/';
    tile.getBoundingClientRect = () => rect(100, 100, 300, 400);
    const picture = document.createElement('img');
    picture.getBoundingClientRect = () => rect(100, 100, 300, 400);
    const badge = document.createElement('span');
    badge.getBoundingClientRect = () => rect(372, 108, 20, 20);
    tile.append(picture, badge);
    document.body.append(tile);
    // The badge takes no pointer: hit tests pass through it to the picture.
    const ctx = setup([], { location: () => 'https://www.instagram.com/some.user/', elementsAt: () => [picture, tile] });
    ctx.layer.mount();
    ctx.setTile({ el: tile, url: tile.href });
    ctx.layer.tileChanged();
    ctx.runFrames();
    const b = ctx.layer.buttons.get(TILE_ID)!;
    expect(b.style.left).toBe('110px');
    expect(b.style.top).toBe('110px');
  });

  it('puts one button on the hovered grid tile, opens its post, and gives way to a video in it', async () => {
    const tile = document.createElement('a');
    tile.href = 'https://www.instagram.com/reel/AbCdEf12345/';
    tile.getBoundingClientRect = () => rect(100, 100, 300, 400);
    document.body.append(tile);
    const videos: TrackedVideo[] = [];
    const ctx = setup(videos, { location: () => 'https://www.instagram.com/some.user/', elementsAt: () => [tile] }, { ...FILE_OFFERS, files: [] });
    ctx.layer.mount();
    ctx.setTile({ el: tile, url: tile.href });
    ctx.layer.tileChanged();
    ctx.runFrames();
    const b = ctx.layer.buttons.get(TILE_ID)!;
    expect(b.getAttribute('aria-label')).toBe(TILE_LABEL);
    expect(b.dataset.shown).toBe('true');
    expect(b.dataset.size).toBe('small');
    ctx.click(b);
    await vi.advanceTimersByTimeAsync(0);
    expect(ctx.sent).toContainEqual({ type: 'downex:frame-offers', src: '', mse: false, post: 'https://www.instagram.com/reel/AbCdEf12345/' });
    ctx.layer.closePanel(false);
    // The pointer leaves the tile: its button hides at once.
    ctx.point(900, 700);
    expect(b.dataset.shown).toBe('false');
    // A preview <video> big enough for a button of its own mounts in the tile: its button takes over.
    videos.push({ id: 5, el: videoAt(rect(100, 100, 300, 400), tile), meta: meta(5, { width: 300, height: 400 }) });
    ctx.layer.sync();
    expect(ctx.layer.buttons.has(TILE_ID)).toBe(false);
    expect(ctx.layer.buttons.has(5)).toBe(true);
  });

  it('keeps the tile’s button and post over a hover preview too small for a button (a 148 px TikTok tile)', async () => {
    const page = 'https://www.tiktok.com/explore';
    const post = 'https://www.tiktok.com/@someone/video/7300000000000000002';
    const tile = document.createElement('a');
    tile.href = post;
    tile.getBoundingClientRect = () => rect(436, 301, 148, 197);
    document.body.append(tile);
    const videos: TrackedVideo[] = [];
    const ctx = setup(videos, { location: () => page, elementsAt: () => [tile] }, { ...FILE_OFFERS, files: [] });
    ctx.layer.mount();
    ctx.setTile({ el: tile, url: post });
    ctx.layer.tileChanged();
    ctx.runFrames();
    expect(ctx.layer.buttons.has(TILE_ID)).toBe(true);
    // The site mounts a muted 148 px preview that plays: the tracker gives it no button.
    const preview = videoAt(rect(436, 301, 148, 197), tile);
    videos.push({ id: 6, el: preview, meta: meta(6, { width: 148, height: 197, visible: true, meaningful: false }), quiet: false });
    ctx.layer.sync();
    ctx.point(500, 400);
    const b = ctx.layer.buttons.get(TILE_ID)!;
    expect(b).toBeDefined();
    expect(ctx.layer.buttons.has(6)).toBe(false);
    expect(b.dataset.shown).toBe('true');
    ctx.click(b);
    await vi.advanceTimersByTimeAsync(0);
    expect(ctx.sent).toContainEqual({ type: 'downex:frame-offers', src: '', mse: false, post });
  });
});

describe('the pointer on the button (Instagram’s feed: it hid as soon as the pointer reached it)', () => {
  // A feed post: the video, and the post's header ("Follow •••") laid over its top by an element of
  // the whole post (far taller than the video, so not its player). The button sits below the header.
  function post() {
    document.body.getBoundingClientRect = () => rect(0, 0, 1200, 4000);
    const card = document.createElement('article');
    card.getBoundingClientRect = () => rect(300, 0, 405, 1100);
    const header = document.createElement('div');
    header.getBoundingClientRect = () => rect(300, 0, 405, 60);
    const video = videoAt(rect(300, 0, 405, 720), card);
    card.append(header);
    document.body.append(card);
    return { video, header };
  }

  it('stays shown while the pointer is on it, whatever the page has under it, and hides after it leaves', () => {
    const { video, header } = post();
    let layerHost: Element | undefined;
    // Over the button's spot the page's topmost element is the post's header layer; elsewhere the video.
    const elementsAt = (_x: number, y: number): Element[] => (y < 60 ? [...(layerHost ? [layerHost] : []), header] : [video]);
    const { layer, runFrames, point } = setup([{ id: 1, el: video, meta: meta(1) }], {
      location: () => 'https://www.instagram.com/',
      elementsAt,
    });
    layerHost = layer.host;
    layer.mount();
    runFrames();
    point(500, 400);
    const button = layer.buttons.get(1)!;
    expect(button.dataset.shown).toBe('true');
    // Step by step onto the button, at its spot (top-right, inset 10 px, 34 px).
    point(600, 200);
    point(660, 80);
    button.dispatchEvent(new MouseEvent('pointerenter'));
    point(670, 30);
    expect(button.dataset.shown).toBe('true');
    point(680, 24);
    expect(button.dataset.shown).toBe('true');
    // Resting on it: it does not time out.
    vi.advanceTimersByTime(REVEAL_ACTIVITY_MS * 3);
    runFrames();
    expect(button.dataset.shown).toBe('true');
    // Leaving it: shown for the usual time, then hidden.
    button.dispatchEvent(new MouseEvent('pointerleave'));
    point(900, 500);
    expect(button.dataset.shown).toBe('true');
    vi.advanceTimersByTime(REVEAL_ACTIVITY_MS);
    expect(button.dataset.shown).toBe('false');
  });
});

describe('reaching the button while it is hidden, and what the page lays over it (owner’s report: Instagram)', () => {
  // The same post: its header layer over the video's top strip, where the button sits.
  function post() {
    Object.defineProperty(document.documentElement, 'clientWidth', { value: 1200, configurable: true });
    Object.defineProperty(document.documentElement, 'clientHeight', { value: 900, configurable: true });
    document.body.getBoundingClientRect = () => rect(0, 0, 1200, 4000);
    const card = document.createElement('article');
    card.getBoundingClientRect = () => rect(300, 0, 405, 1100);
    const header = document.createElement('div');
    header.getBoundingClientRect = () => rect(300, 0, 405, 60);
    const video = videoAt(rect(300, 0, 405, 720), card);
    card.append(header);
    document.body.append(card);
    return { video, header };
  }
  afterEach(() => {
    delete (document.documentElement as unknown as Record<string, unknown>).clientWidth;
    delete (document.documentElement as unknown as Record<string, unknown>).clientHeight;
  });
  // The button's own spot, from where the layer placed it (top-right corner of the video).
  const spot = (b: HTMLElement) => ({ x: 1200 - Number.parseFloat(b.style.right) - 14, y: Number.parseFloat(b.style.top) + 14 });

  it('shows when the pointer comes straight onto its spot from beside the video', () => {
    const { video, header } = post();
    let prompt: Element | undefined;
    const elementsAt = (x: number, y: number): Element[] => (prompt ? [prompt] : x >= 300 && x <= 705 && y < 60 ? [header] : [video]);
    const { layer, runFrames, point } = setup([{ id: 1, el: video, meta: meta(1) }], { location: () => 'https://www.instagram.com/', elementsAt });
    layer.mount();
    runFrames();
    const button = layer.buttons.get(1)!;
    const at = spot(button);
    expect(at.y).toBeLessThan(60);
    expect(button.dataset.shown).not.toBe('true');
    point(900, at.y);
    point(720, at.y);
    point(at.x + 6, at.y);
    point(at.x, at.y);
    expect(button.dataset.shown).toBe('true');
    // The pointer stops there: no pointerenter comes (that needs one more movement), yet it rests
    // on the button, which stays.
    button.getBoundingClientRect = () => rect(at.x - 14, at.y - 14, 28, 28);
    vi.advanceTimersByTime(REVEAL_ACTIVITY_MS * 3);
    expect(button.dataset.shown).toBe('true');
    // Something laid over the whole video still hides it.
    prompt = document.createElement('div');
    point(at.x - 1, at.y);
    expect(button.dataset.shown).toBe('false');
  });

  it('a prompt laid over the page while the pointer rests on the button hides it', () => {
    const { video, header } = post();
    let prompt: Element | undefined;
    const elementsAt = (x: number, y: number): Element[] => (prompt ? [prompt] : x >= 300 && x <= 705 && y < 60 ? [header] : [video]);
    const { layer, runFrames, point } = setup([{ id: 1, el: video, meta: meta(1) }], { location: () => 'https://www.instagram.com/', elementsAt });
    layer.mount();
    runFrames();
    point(500, 400);
    const button = layer.buttons.get(1)!;
    const at = spot(button);
    button.dispatchEvent(new MouseEvent('pointerenter'));
    point(at.x, at.y);
    vi.advanceTimersByTime(REVEAL_ACTIVITY_MS * 3);
    expect(button.dataset.shown).toBe('true');
    // The site opens its sign-up prompt on its own; the pointer does not move.
    prompt = document.createElement('div');
    vi.advanceTimersByTime(REVEAL_ACTIVITY_MS);
    expect(button.dataset.shown).toBe('false');
  });

  it('the YouTube pill is unchanged: it hides 3 s after the last movement, pointer on it or not', () => {
    const v = videoAt(rect(100, 80, 960, 540));
    const { layer, runFrames, point } = setup([{ id: 1, el: v, meta: meta(1, { width: 960, height: 540 }) }], {
      location: () => 'https://www.youtube.com/watch?v=aqz-KE-bpKQ',
      elementsAt: () => [v],
    });
    layer.mount();
    runFrames();
    point(600, 300);
    const button = layer.buttons.get(1)!;
    expect(button.dataset.shown).toBe('true');
    button.dispatchEvent(new MouseEvent('pointerenter'));
    point(1000, 100);
    vi.advanceTimersByTime(REVEAL_ACTIVITY_MS);
    expect(button.dataset.shown).toBe('false');
  });
});

describe('a portrait video in a card wider than 16:9 (owner’s report: TikTok For You)', () => {
  const sized = <E extends Element>(e: E, r: DOMRect): E => {
    e.getBoundingClientRect = () => r;
    return e;
  };
  // Measured on the real For You page (1906x946 viewport): VIDEO 514x914 > DIV 1634x0 >
  // xgplayer-container 1634x0 > DivBasicPlayerWrapper 1634x914 > DivContainer 1634x914 >
  // SectionMediaCard 1634x914 (with DivMediaCardOverlay 1634x914, the hit) > ArticleItemContainer
  // 1634x946 > the feed.
  function card(vx: number, vw: number, vh: number, cx: number, cw: number) {
    document.body.getBoundingClientRect = () => rect(0, 0, cx + cw + 100, 20000);
    const feed = sized(document.createElement('div'), rect(cx, 0, cw, 20000));
    const article = sized(document.createElement('article'), rect(cx, 0, cw, vh + 32));
    const section = sized(document.createElement('section'), rect(cx, 16, cw, vh));
    const container = sized(document.createElement('div'), rect(cx, 16, cw, vh));
    const wrapper = sized(document.createElement('div'), rect(cx, 16, cw, vh));
    const xg = sized(document.createElement('div'), rect(cx, 16, cw, 0));
    const inner = sized(document.createElement('div'), rect(cx, 16, cw, 0));
    const overlay = sized(document.createElement('div'), rect(cx, 16, cw, vh));
    const video = videoAt(rect(vx, 16, vw, vh), inner);
    xg.append(inner);
    wrapper.append(xg);
    container.append(wrapper);
    section.append(container, overlay);
    article.append(section);
    feed.append(article);
    document.body.append(feed);
    return { feed, video, overlay };
  }

  it('is the video’s player at a maximised 1080p window (1906 px viewport)', () => {
    const { video, overlay, feed } = card(816, 514, 914, 136, 1634);
    expect(topmostAt([{ id: 1, el: video }], overlay, 1906)?.id).toBe(1);
    // The feed and the page around it still are not.
    const banner = sized(document.createElement('div'), rect(136, 0, 1634, 60));
    feed.append(banner);
    expect(topmostAt([{ id: 1, el: video }], banner, 1906)).toBeUndefined();
    const prompt = sized(document.createElement('div'), rect(0, 0, 1906, 946));
    document.body.append(prompt);
    expect(topmostAt([{ id: 1, el: video }], prompt, 1906)).toBeUndefined();
  });

  it('is the video’s player in a wider window and in the 1243 px card', () => {
    const wide = card(1035, 514, 914, 136, 2100);
    expect(topmostAt([{ id: 1, el: wide.video }], wide.overlay, 2400)?.id).toBe(1);
    const small = card(600, 315, 559, 136, 1243);
    expect(topmostAt([{ id: 2, el: small.video }], small.overlay, 1500)?.id).toBe(2);
  });

  it('shows the button under the pointer on the real layer', () => {
    Object.defineProperty(document.documentElement, 'clientWidth', { value: 1906, configurable: true });
    try {
      const { video, overlay } = card(816, 514, 914, 136, 1634);
      const { layer, runFrames, point } = setup([{ id: 1, el: video, meta: meta(1, { width: 514, height: 914 }) }], {
        location: () => 'https://www.tiktok.com/foryou',
        elementsAt: () => [overlay],
      });
      layer.mount();
      runFrames();
      point(1073, 400);
      expect(layer.buttons.get(1)!.dataset.shown).toBe('true');
    } finally {
      delete (document.documentElement as unknown as Record<string, unknown>).clientWidth;
    }
  });
});

describe('a letterboxed player (TikTok’s For You feed)', () => {
  // The shape of a real For You page, on example markup: a portrait video in zero-height wrappers
  // inside a wide card, and the site's transparent card overlay, a sibling of the player, on top.
  const sized = <E extends Element>(e: E, r: DOMRect): E => {
    e.getBoundingClientRect = () => r;
    return e;
  };
  function forYou() {
    document.body.getBoundingClientRect = () => rect(0, 0, 1200, 8000);
    const column = sized(document.createElement('div'), rect(192, 0, 1008, 8000));
    const card = sized(document.createElement('div'), rect(192, 16, 1008, 768));
    const player = sized(document.createElement('div'), rect(192, 16, 1008, 768));
    const xg = sized(document.createElement('div'), rect(192, 16, 1008, 0));
    const inner = sized(document.createElement('div'), rect(192, 16, 1008, 0));
    const overlay = sized(document.createElement('div'), rect(192, 16, 1008, 768));
    const video = videoAt(rect(544, 16, 432, 768), inner);
    xg.append(inner);
    player.append(xg);
    card.append(player, overlay);
    column.append(card);
    document.body.append(column);
    return { column, card, video, overlay };
  }

  it('is the video’s player: the card’s overlay is in it', () => {
    const { video, overlay, column } = forYou();
    expect(topmostAt([{ id: 1, el: video }], overlay)?.id).toBe(1);
    // A feed column or the page around it is not: something they hold over the video covers it.
    const banner = sized(document.createElement('div'), rect(192, 0, 1008, 60));
    column.append(banner);
    expect(topmostAt([{ id: 1, el: video }], banner)).toBeUndefined();
    const prompt = sized(document.createElement('div'), rect(0, 0, 1200, 800));
    document.body.append(prompt);
    expect(topmostAt([{ id: 1, el: video }], prompt)).toBeUndefined();
  });

  it('shows the button under the pointer, and for a video that starts there', () => {
    const { video, overlay } = forYou();
    let hit: Element = overlay;
    const { layer, runFrames, point } = setup([{ id: 1, el: video, meta: meta(1, { width: 432, height: 768 }), startedAt: Date.now() }], {
      location: () => 'https://www.tiktok.com/foryou',
      elementsAt: () => [hit],
    });
    layer.mount();
    runFrames();
    point(760, 400);
    expect(layer.buttons.get(1)!.dataset.shown).toBe('true');
    vi.advanceTimersByTime(REVEAL_ACTIVITY_MS);
    runFrames();
    layer.reveal(1, REVEAL_ACTIVITY_MS);
    expect(layer.buttons.get(1)!.dataset.shown).toBe('true');
    // A sign-up prompt laid over the page hides it.
    const prompt = document.createElement('div');
    document.body.append(prompt);
    hit = prompt;
    vi.advanceTimersByTime(REVEAL_ACTIVITY_MS);
    runFrames();
    expect(layer.buttons.get(1)!.dataset.shown).toBe('false');
    layer.reveal(1, REVEAL_ACTIVITY_MS);
    expect(layer.buttons.get(1)!.dataset.shown).toBe('false');
    point(770, 410);
    expect(layer.buttons.get(1)!.dataset.shown).toBe('false');
  });
});
