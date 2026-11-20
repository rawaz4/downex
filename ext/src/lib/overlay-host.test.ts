// @vitest-environment happy-dom

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { ExtensionMessage } from './messages';
import type { FrameOffers, FrameVideoMeta } from './offer-types';
import { ITEM_STRIP_CSS } from './item-strip';
import { OFFER_PANEL_CSS } from './offer-panel';
import {
  applyStyles,
  buttonVideos,
  hasPlayed,
  LAYER_CSS,
  LAYER_HOST_CSS,
  LAYER_STYLE,
  LAYER_TAG,
  MAX_BUTTONS,
  OverlayLayer,
  type OverlayLayerDeps,
  placeButton,
  placeMark,
  type RectLike,
  startOverlayUi,
} from './overlay-host';
import { ISOLATED_EVENTS, OVERLAY_SHARED_CSS, REVEAL_ACTIVITY_MS, REVEAL_LOAD_MS, SENT_CLOSE_MS } from './video-overlay';
import type { TrackedVideo, TrackerApi, TrackerEvent } from './video-tracker';

function rect(left: number, top: number, width: number, height: number): RectLike & DOMRect {
  return { left, top, width, height, right: left + width, bottom: top + height, x: left, y: top, toJSON: () => ({}) } as RectLike & DOMRect;
}

function meta(id: number, over: Partial<FrameVideoMeta> = {}): FrameVideoMeta {
  return {
    id,
    src: `https://cdn.example.com/${id}.mp4`,
    mse: false,
    width: 640,
    height: 360,
    videoWidth: 1280,
    videoHeight: 720,
    durationMs: 60_000,
    drm: false,
    visible: true,
    decorative: false,
    meaningful: true,
    ...over,
  };
}

function videoAt(r: RectLike & DOMRect): HTMLVideoElement {
  const v = document.createElement('video');
  v.getBoundingClientRect = () => r;
  document.body.append(v);
  return v;
}

const OFFERS: FrameOffers = {
  title: 'Clip',
  topHost: 'www.example.com',
  files: [
    {
      key: 'f1',
      display: 'cdn.example.com/1.mp4',
      size: 1_000_000 * 30,
      sizeKnown: true,
      filename: '1.mp4',
      frame: 'this',
      matched: true,
      short: false,
      width: 640,
      height: 360,
      durationMs: 60_000,
    },
  ],
  streams: [],
  features: { streams: true, video: true },
  drm: false,
  live: false,
};

function fakeApi(videos: TrackedVideo[], offers: FrameOffers = OFFERS) {
  const listeners = new Set<(e: TrackerEvent) => void>();
  const sent: ExtensionMessage[] = [];
  let allowed = true;
  let invalidated = false;
  let claimed = false;
  let requested: number | undefined;
  const metas = new Map(videos.map((v) => [v.id, v.meta]));
  const refreshState = vi.fn();
  const release = vi.fn((id: number) => {
    if (requested === id) requested = undefined;
  });
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
      if (msg.type === 'downex:page-video-probe') return { ok: false, error: { code: 'rejected', message: 'site_unsupported: none', reason: 'site_unsupported' } } as never;
      if (msg.type === 'downex:offer-add') return { ok: true, value: { type: 'added', downloadId: 3, awaiting: true } } as never;
      if (msg.type === 'downex:set-site-hidden') return { hidden: true } as never;
      return undefined;
    },
    get invalidated() {
      return invalidated;
    },
    get allowed() {
      return allowed;
    },
    recheck: (id) => metas.get(id),
    refreshState,
    claimUi: () => {
      if (claimed) return false;
      claimed = true;
      return true;
    },
    get requested() {
      return requested;
    },
    release,
  };
  return {
    api,
    sent,
    metas,
    refreshState,
    release,
    setRequested: (id: number | undefined) => (requested = id),
    emit: (e: TrackerEvent) => {
      if (e.type === 'invalidated') invalidated = true;
      for (const fn of [...listeners]) fn(e);
    },
    setAllowed: (v: boolean) => (allowed = v),
  };
}

const trusted = new WeakSet<Event>();

function setup(videos: TrackedVideo[], over: Partial<OverlayLayerDeps> = {}, offers: FrameOffers = OFFERS) {
  const fake = fakeApi(videos, offers);
  const frames: (() => void)[] = [];
  const layer = new OverlayLayer({
    doc: document,
    api: fake.api,
    raf: (cb) => frames.push(cb),
    isTrusted: (e) => trusted.has(e),
    location: () => 'https://www.example.com/watch#t=1',
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
  return { layer, ...fake, runFrames, click };
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
  Object.defineProperty(document, 'fullscreenElement', { value: null, configurable: true });
});

describe('placement', () => {
  it('puts the button at the video’s top-right corner, inset 12 px', () => {
    expect(placeButton(rect(100, 50, 640, 360), 1200, 800)).toEqual({ top: 62, right: 472, compact: false });
  });

  it('keeps it on the visible part of a video cut by the viewport', () => {
    expect(placeButton(rect(-100, -200, 1400, 700), 1200, 800)).toEqual({ top: 12, right: 12, compact: false });
  });

  it('shows the mark alone, inset 8 px, below 360 × 200 px', () => {
    expect(placeButton(rect(0, 0, 359, 300), 1200, 800)).toEqual({ top: 8, right: 849, compact: true });
    expect(placeButton(rect(0, 0, 400, 199), 1200, 800)?.compact).toBe(true);
  });

  it('has no place for a video outside the viewport', () => {
    expect(placeButton(rect(0, 900, 640, 360), 1200, 800)).toBeUndefined();
    expect(placeButton(rect(-700, 0, 640, 360), 1200, 800)).toBeUndefined();
  });

  it('gives quiet videos buttons after every meaningful one', () => {
    const big: TrackedVideo = { id: 1, el: document.createElement('video'), meta: meta(1, { width: 1200, height: 700, meaningful: false, decorative: true }), quiet: true };
    const small: TrackedVideo = { id: 2, el: document.createElement('video'), meta: meta(2, { width: 300, height: 200 }) };
    const none: TrackedVideo = { id: 3, el: document.createElement('video'), meta: meta(3, { meaningful: false }) };
    expect(buttonVideos([big, small, none]).map((v) => v.id)).toEqual([2, 1]);
    expect(buttonVideos([big, small], 1).map((v) => v.id)).toEqual([2]);
  });

  it('gives buttons to the 8 largest meaningful videos', () => {
    const videos: TrackedVideo[] = Array.from({ length: 12 }, (_, i) => ({
      id: i + 1,
      el: document.createElement('video'),
      meta: meta(i + 1, { width: 300 + i * 10, height: 200, meaningful: i !== 11 }),
    }));
    const chosen = buttonVideos(videos);
    expect(chosen).toHaveLength(MAX_BUTTONS);
    expect(chosen.map((v) => v.id)).toEqual([11, 10, 9, 8, 7, 6, 5, 4]);
  });
});

describe('styles', () => {
  it('adopts a constructable sheet, else falls back to a <style> element', () => {
    const host = document.createElement('div');
    const shadow = host.attachShadow({ mode: 'open' });
    const made: CSSStyleSheet[] = [];
    expect(
      applyStyles(shadow, 'a{color:red}', (css) => {
        const sheet = new CSSStyleSheet();
        sheet.replaceSync(css);
        made.push(sheet);
        return sheet;
      }),
    ).toBe('adopted');
    expect(shadow.adoptedStyleSheets).toEqual(made);
    expect(shadow.querySelector('style')).toBeNull();
    // The default makes the sheet itself.
    const plain = document.createElement('div').attachShadow({ mode: 'open' });
    expect(applyStyles(plain, 'a{}')).toBe('adopted');
    const other = document.createElement('div').attachShadow({ mode: 'open' });
    expect(
      applyStyles(other, '.x{}', () => {
        throw new Error('Accessing from Xray wrapper is not supported.');
      }),
    ).toBe('style');
    expect(other.querySelector('style')!.textContent).toBe('.x{}');
  });

  it('is the shared overlay rules under the layer’s own fixed host rule', () => {
    expect(LAYER_STYLE.startsWith(LAYER_HOST_CSS)).toBe(true);
    expect(LAYER_STYLE).toContain(OVERLAY_SHARED_CSS);
    for (const rule of ['position: fixed !important', 'inset: 0 !important', 'pointer-events: none !important', 'z-index: 2147483647 !important', 'contain: layout !important']) {
      expect(LAYER_HOST_CSS).toContain(rule);
    }
    expect(LAYER_STYLE.match(/:host\b/g)).toHaveLength(1);
  });

  it('is self-contained, and blurs a button only while it is shown', () => {
    expect(LAYER_STYLE).toBe(LAYER_HOST_CSS + OVERLAY_SHARED_CSS + LAYER_CSS + OFFER_PANEL_CSS + ITEM_STRIP_CSS);
    for (const banned of ['url(', '@import', '@property', '@font-face']) expect(LAYER_STYLE).not.toContain(banned);
    expect(LAYER_CSS).toMatch(/\.dx-button\[data-shown='true'\]\s*\{[^}]*backdrop-filter: var\(--b-blur\);/);
    // The panel's own rules never blur; the shared ones blur it only while open.
    expect(OFFER_PANEL_CSS).not.toContain('backdrop-filter');
    expect(LAYER_CSS.match(/backdrop-filter/g)).toHaveLength(1);
  });
});

describe('OverlayLayer', () => {
  it('mounts one fixed host with a closed shadow root and at most 8 buttons, placed from the rectangles', () => {
    const videos: TrackedVideo[] = Array.from({ length: 10 }, (_, i) => {
      const el = videoAt(rect(20 * i, 30 * i, 640 - i, 360));
      return { id: i + 1, el, meta: meta(i + 1, { width: 640 - i }) };
    });
    const { layer, runFrames } = setup(videos);
    layer.mount();
    expect(layer.host.localName).toBe(LAYER_TAG);
    expect(layer.host.parentNode).toBe(document.documentElement);
    expect(layer.host.shadowRoot).toBeNull();
    expect(layer.buttons.size).toBe(MAX_BUTTONS);
    runFrames();
    const first = layer.buttons.get(1)!;
    expect(first.getAttribute('aria-label')).toBe('Download this video with Downex');
    expect(first.getAttribute('aria-haspopup')).toBe('dialog');
    expect(first.style.top).toBe('12px');
    expect(first.style.right).toBe(`${1200 - 640 + 12}px`);
    expect(first.dataset.compact).toBe('false');
    expect(first.textContent).toBe('Download');
    // Nothing was put into the page's own elements.
    expect(videos.every((v) => v.el.childElementCount === 0)).toBe(true);
  });

  it('reveals a button on pointer movement over its video and on play, hidden by opacity afterwards', () => {
    const el = videoAt(rect(100, 100, 640, 360));
    const { layer, runFrames } = setup([{ id: 1, el, meta: meta(1) }]);
    layer.mount();
    runFrames();
    const b = layer.buttons.get(1)!;
    expect(b.dataset.shown).toBe('false');
    document.dispatchEvent(new MouseEvent('pointermove', { clientX: 300, clientY: 200 }));
    runFrames();
    expect(b.dataset.shown).toBe('true');
    vi.advanceTimersByTime(REVEAL_ACTIVITY_MS);
    expect(b.dataset.shown).toBe('false');
    document.dispatchEvent(new MouseEvent('pointermove', { clientX: 50, clientY: 50 }));
    runFrames();
    expect(b.dataset.shown).toBe('false');
    layer.reveal(1, REVEAL_LOAD_MS);
    expect(b.dataset.shown).toBe('true');
    vi.advanceTimersByTime(REVEAL_LOAD_MS);
    expect(b.dataset.shown).toBe('false');
  });

  it('follows a video moved without any event: the pointer finds it where it is now', () => {
    let r = rect(100, 100, 640, 360);
    const el = document.createElement('video');
    el.getBoundingClientRect = () => r;
    document.body.append(el);
    const { layer, runFrames } = setup([{ id: 1, el, meta: meta(1) }]);
    layer.mount();
    runFrames();
    const b = layer.buttons.get(1)!;
    expect(b.style.top).toBe('112px');
    // A banner pushed the video down by 300 px: no resize, no intersection change.
    r = rect(100, 400, 640, 360);
    document.dispatchEvent(new MouseEvent('pointermove', { clientX: 300, clientY: 200 }));
    runFrames();
    expect(b.dataset.shown).toBe('false');
    expect(b.style.top).toBe('412px');
    document.dispatchEvent(new MouseEvent('pointermove', { clientX: 300, clientY: 500 }));
    runFrames();
    expect(b.dataset.shown).toBe('true');
  });

  it('lets clicks through a button that is not shown', () => {
    expect(LAYER_STYLE).toMatch(/\.dx-button\[data-shown='false'\]:not\(:focus-visible\):not\(\[aria-expanded='true'\]\)\s*\{\s*pointer-events:\s*none;\s*\}/);
  });

  it('shows the button of a video that started before the layer was there', () => {
    const el = videoAt(rect(100, 100, 640, 360));
    const { layer } = setup([{ id: 1, el, meta: meta(1), startedAt: Date.now() - 1000 }, { id: 2, el: videoAt(rect(0, 500, 640, 360)), meta: meta(2), startedAt: Date.now() - REVEAL_LOAD_MS - 1 }]);
    layer.mount();
    expect(layer.buttons.get(1)!.dataset.shown).toBe('true');
    expect(layer.buttons.get(2)!.dataset.shown).toBe('false');
    vi.advanceTimersByTime(REVEAL_LOAD_MS - 1000);
    expect(layer.buttons.get(1)!.dataset.shown).toBe('false');
  });

  it('puts its host back when the page removed it', () => {
    const el = videoAt(rect(0, 0, 640, 360));
    const { layer, runFrames } = setup([{ id: 1, el, meta: meta(1) }]);
    layer.mount();
    layer.host.remove();
    layer.sync();
    expect(layer.host.parentNode).toBe(document.documentElement);
    layer.host.remove();
    window.dispatchEvent(new Event('resize'));
    runFrames();
    expect(layer.host.isConnected).toBe(true);
  });

  it('moves into a fullscreen container and back, but not into a fullscreen video', () => {
    const el = videoAt(rect(0, 0, 640, 360));
    const { layer } = setup([{ id: 1, el, meta: meta(1) }]);
    layer.mount();
    const container = document.createElement('div');
    document.body.append(container);
    Object.defineProperty(document, 'fullscreenElement', { value: container, configurable: true });
    document.dispatchEvent(new Event('fullscreenchange'));
    expect(layer.host.parentNode).toBe(container);
    Object.defineProperty(document, 'fullscreenElement', { value: el, configurable: true });
    document.dispatchEvent(new Event('fullscreenchange'));
    expect(layer.host.parentNode).toBe(document.documentElement);
    Object.defineProperty(document, 'fullscreenElement', { value: null, configurable: true });
    document.dispatchEvent(new Event('fullscreenchange'));
    expect(layer.host.parentNode).toBe(document.documentElement);
  });

  it('keeps clicks, keys and wheel from the page, but not pointer movement', () => {
    const el = videoAt(rect(0, 0, 640, 360));
    const { layer, click } = setup([{ id: 1, el, meta: meta(1) }]);
    layer.mount();
    const seen: string[] = [];
    for (const type of [...ISOLATED_EVENTS, 'mousemove']) document.addEventListener(type, () => seen.push(type));
    const b = layer.buttons.get(1)!;
    b.dispatchEvent(new MouseEvent('dblclick', { bubbles: true, composed: true }));
    b.dispatchEvent(new KeyboardEvent('keydown', { key: ' ', bubbles: true, composed: true }));
    b.dispatchEvent(new WheelEvent('wheel', { bubbles: true, composed: true }));
    b.dispatchEvent(new MouseEvent('mousemove', { bubbles: true, composed: true }));
    click(layer.buttons.get(1)!);
    expect(seen).toEqual(['mousemove']);
  });

  it('keeps the panel inside a short frame, over its button', () => {
    Object.defineProperty(document.documentElement, 'clientHeight', { value: 112, configurable: true });
    const el = videoAt(rect(0, 0, 200, 112));
    const { layer, click, runFrames } = setup([{ id: 1, el, meta: meta(1, { width: 200, height: 112 }) }]);
    layer.mount();
    runFrames();
    const b = layer.buttons.get(1)!;
    b.getBoundingClientRect = () => rect(160, 8, 32, 32);
    click(b);
    expect(layer.panelDom.el.style.top).toBe('8px');
    expect(layer.panelDom.el.style.maxHeight).toBe('96px');
    // The button under the panel is hidden meanwhile (its halo would show past the corner).
    expect(b.dataset.covered).toBe('true');
    expect(LAYER_CSS).toContain(".dx-button[data-covered='true'] { visibility: hidden; }");
    // Taller frames keep it under the button.
    Object.defineProperty(document.documentElement, 'clientHeight', { value: 800, configurable: true });
    runFrames();
    expect(layer.panelDom.el.style.top).toBe('48px');
    expect(layer.panelDom.el.style.maxHeight).toBe('744px');
    expect(b.dataset.covered).toBeUndefined();
    // Closed, the button is shown again, before the focus returns to it.
    Object.defineProperty(document.documentElement, 'clientHeight', { value: 112, configurable: true });
    runFrames();
    expect(b.dataset.covered).toBe('true');
    layer.closePanel(true);
    expect(b.dataset.covered).toBeUndefined();
    Object.defineProperty(document.documentElement, 'clientHeight', { value: 800, configurable: true });
  });

  it('opens the panel on a trusted click, checks DRM again, and returns the focus on Esc', async () => {
    const el = videoAt(rect(0, 0, 640, 360));
    const { layer, click, sent, metas, runFrames } = setup([{ id: 1, el, meta: meta(1) }]);
    layer.mount();
    runFrames();
    const b = layer.buttons.get(1)!;
    b.dispatchEvent(new MouseEvent('click', { bubbles: true, composed: true }));
    expect(layer.panel.isOpen).toBe(false);
    click(b);
    expect(layer.panel.isOpen).toBe(true);
    expect(b.getAttribute('aria-expanded')).toBe('true');
    await vi.advanceTimersByTimeAsync(0);
    expect(sent).toContainEqual({ type: 'downex:frame-offers', src: 'https://cdn.example.com/1.mp4', mse: false });
    const shadow = layer.panelDom.el.getRootNode() as ShadowRoot;
    const row = layer.panelDom.el.querySelector<HTMLButtonElement>('button.dx-row')!;
    expect(row.getAttribute('aria-label')).toBe('This video · 360p · MP4 · 30.0 MB · File');
    expect(shadow.activeElement).toBe(row);
    row.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, composed: true }));
    expect(layer.panel.isOpen).toBe(false);
    expect(shadow.activeElement).toBe(b);
    expect(b.getAttribute('aria-expanded')).toBe('false');

    // mediaKeys was set after the button appeared: the panel says so and asks nothing.
    metas.set(1, meta(1, { drm: true, meaningful: false }));
    const before = sent.length;
    click(b);
    await vi.advanceTimersByTimeAsync(0);
    expect(sent.length).toBe(before);
    expect(layer.panelDom.el.querySelector('.dx-status-text')!.textContent).toBe('This video is DRM-protected. Downex does not download protected content.');
  });

  it('sends the chosen file and closes 3 s after Downex took it', async () => {
    const el = videoAt(rect(0, 0, 640, 360));
    const { layer, click, sent } = setup([{ id: 1, el, meta: meta(1) }]);
    layer.mount();
    click(layer.buttons.get(1)!);
    await vi.advanceTimersByTimeAsync(0);
    click(layer.panelDom.el.querySelector('button.dx-row')!);
    await vi.advanceTimersByTimeAsync(0);
    expect(sent).toContainEqual({ type: 'downex:offer-add', key: 'f1' });
    expect(layer.panelDom.el.querySelector('.dx-status-text')!.textContent).toBe('Confirm in Downex');
    await vi.advanceTimersByTimeAsync(SENT_CLOSE_MS);
    expect(layer.panel.isOpen).toBe(false);
  });

  it('sends the part chosen on the timeline, with no end for a part to the end, and reads the clicked video', async () => {
    const offers: FrameOffers = {
      ...OFFERS,
      files: [],
      streams: [{ key: 's1', format: 'hls', display: 'cdn/s1.m3u8', frame: 'this', matched: true }],
      features: { streams: true, video: true, clips: true },
    };
    const info = {
      type: 'videoInfo', site: '', title: '', durationMs: 754_000, format: 'hls', container: 'mp4', selectedVideo: '', selectedAudio: '',
      tracks: [{ id: 'v720', kind: 'muxed', ext: 'mp4', codecs: '', width: 1280, height: 720, frameRate: 0, bandwidth: 2_000_000, size: 0, sizeApprox: false, language: '', name: '720p (HD)' }],
    };
    for (const [toEnd, want] of [
      [false, { type: 'downex:offer-add', key: 's1', videoTrack: 'v720', clipStartMs: 30_000, clipEndMs: 694_000 }],
      [true, { type: 'downex:offer-add', key: 's1', videoTrack: 'v720', clipStartMs: 30_000 }],
    ] as const) {
      const el = videoAt(rect(0, 0, 640, 360));
      Object.defineProperty(el, 'currentTime', { value: 30, writable: true, configurable: true });
      // The element plays the video (its media as long as it); happy-dom's has none (NaN).
      Object.defineProperty(el, 'duration', { value: 754.02, configurable: true });
      const fake = setup([{ id: 1, el, meta: meta(1, { src: '', mse: true }) }], {}, offers);
      const send = fake.api.send;
      fake.api.send = async (msg) => (msg.type === 'downex:stream-probe' ? ({ ok: true, value: info } as never) : send(msg));
      fake.layer.mount();
      fake.click(fake.layer.buttons.get(1)!);
      await vi.advanceTimersByTimeAsync(0);
      const panel = fake.layer.panelDom.el;
      fake.click(panel.querySelector('.dx-part-toggle')!);
      // Start here: the clicked <video>'s position (0:30); the end by its slider's keys.
      fake.click(panel.querySelector(".dx-trim-btn[data-here='from']")!);
      if (!toEnd) panel.querySelector(".dx-trim-handle[data-which='to']")!.dispatchEvent(new KeyboardEvent('keydown', { key: 'PageDown', bubbles: true, cancelable: true }));
      fake.click(panel.querySelector('button.dx-row')!);
      await vi.advanceTimersByTimeAsync(0);
      expect(fake.sent.filter((m) => m.type === 'downex:offer-add')).toEqual([want]);
      fake.layer.destroy();
    }
  });

  it('asks the site with the frame’s own address, without its fragment', async () => {
    const el = videoAt(rect(0, 0, 640, 360));
    const offers: FrameOffers = {
      ...OFFERS,
      files: [],
      site: { key: 'page:https://www.example.com/watch', site: 'www.example.com', kind: 'page', pageUrl: 'https://www.example.com/watch', curated: false },
    };
    const { layer, click, sent } = setup([{ id: 1, el, meta: meta(1, { src: '', mse: true }) }], {}, offers);
    layer.mount();
    click(layer.buttons.get(1)!);
    await vi.advanceTimersByTimeAsync(0);
    // No network row: the click itself asks yt-dlp, for the frame's address.
    expect(sent).toContainEqual({ type: 'downex:frame-offers', src: '', mse: true, played: expect.objectContaining({ href: 'https://www.example.com/watch' }) });
    expect(sent).toContainEqual({ type: 'downex:page-video-probe', url: 'https://www.example.com/watch' });
    expect(layer.panelDom.el.querySelector('.dx-status-text')!.textContent).toBe('Downex found no video on this page.');
  });

  it('on a feed, asks about the clicked video’s own post and adds it by that address (§5.1 "Feed videos")', async () => {
    const feed = 'https://www.instagram.com/';
    const el = videoAt(rect(0, 0, 640, 360));
    const article = document.createElement('article');
    const link = document.createElement('a');
    link.href = 'https://www.instagram.com/p/CODEaaa111/';
    const media = document.createElement('div');
    media.append(el);
    article.append(link, media);
    document.body.append(article);
    const offers: FrameOffers = {
      ...OFFERS,
      files: [],
      site: { key: 'instagram:CODEaaa111', site: 'Instagram', kind: 'watch', pageUrl: 'https://www.instagram.com/p/CODEaaa111/', curated: true, linked: true },
    };
    const { layer, click, sent } = setup([{ id: 1, el, meta: meta(1, { src: '', mse: true }) }], { location: () => feed }, offers);
    layer.mount();
    click(layer.buttons.get(1)!);
    await vi.advanceTimersByTimeAsync(0);
    expect(sent).toContainEqual(expect.objectContaining({ type: 'downex:frame-offers', src: '', mse: true, post: 'https://www.instagram.com/p/CODEaaa111/' }));
    expect(sent).toContainEqual({ type: 'downex:page-video-probe', url: 'https://www.instagram.com/p/CODEaaa111/' });
    // The feed reuses the element for another post: another video, read again at the next click.
    layer.closePanel(false);
    link.href = 'https://www.instagram.com/p/CODEbbb222/';
    sent.length = 0;
    click(layer.buttons.get(1)!);
    await vi.advanceTimersByTimeAsync(0);
    expect(sent).toContainEqual(expect.objectContaining({ type: 'downex:frame-offers', src: '', mse: true, post: 'https://www.instagram.com/p/CODEbbb222/' }));
  });

  it('names another video when the element loads another one, and sends the DRM state read on every poll (SITE-MEDIA-SPEC §2.9)', async () => {
    const el = videoAt(rect(0, 0, 640, 360));
    Object.defineProperty(el, 'duration', { value: 364.767, configurable: true });
    const fake = setup([{ id: 1, el, meta: meta(1, { src: '', mse: true }) }], { location: () => 'https://www.social.example/foryou' });
    fake.layer.mount();
    fake.click(fake.layer.buttons.get(1)!);
    await vi.advanceTimersByTimeAsync(0);
    const traces = () => fake.sent.filter((m) => m.type === 'downex:frame-offers').map((m) => (m as { played?: { element: string; drm?: boolean } }).played);
    const first = traces().at(-1)!;
    expect(first.drm).toBeUndefined();
    // The feed loads the next post into the same element (no address, no post link).
    Object.defineProperty(el, 'duration', { value: 6.477, configurable: true });
    el.dispatchEvent(new Event('durationchange'));
    await vi.advanceTimersByTimeAsync(2000);
    const second = traces().at(-1)!;
    expect(second.element).not.toBe(first.element);
    el.dispatchEvent(new Event('loadstart'));
    await vi.advanceTimersByTimeAsync(2000);
    expect(traces().at(-1)!.element).not.toBe(second.element);
    // mediaKeys set while the panel is open: the next poll says so, and a Played add is never sent.
    fake.metas.set(1, meta(1, { src: '', mse: true, drm: true }));
    await vi.advanceTimersByTimeAsync(2000);
    expect(traces().at(-1)!.drm).toBe(true);
    fake.layer.destroy();
  });

  it('sends no post where the frame’s own address is the video, or no link names one', async () => {
    const el = videoAt(rect(0, 0, 640, 360));
    const { layer, click, sent } = setup([{ id: 1, el, meta: meta(1, { src: '', mse: true }) }], { location: () => 'https://www.instagram.com/reel/PAGEeee555/' });
    layer.mount();
    click(layer.buttons.get(1)!);
    await vi.advanceTimersByTimeAsync(0);
    expect(sent.find((m) => m.type === 'downex:frame-offers')).toEqual({ type: 'downex:frame-offers', src: '', mse: true, played: expect.objectContaining({ durationMs: expect.any(Number) }) });
  });

  it('gives a quiet video (a background loop elsewhere) the mark alone, shown only while the pointer is over it', () => {
    const el = videoAt(rect(100, 100, 640, 360));
    const quiet: TrackedVideo = { id: 1, el, meta: meta(1, { decorative: true, meaningful: false }), quiet: true, startedAt: Date.now() };
    const loud = videoAt(rect(100, 500, 640, 360));
    const { layer, runFrames } = setup([quiet, { id: 2, el: loud, meta: meta(2) }]);
    layer.mount();
    runFrames();
    const b = layer.buttons.get(1)!;
    expect(b.dataset.quiet).toBe('true');
    expect(b.dataset.compact).toBe('true');
    expect(b.style.top).toBe('108px');
    // Never shown because the video started (it plays all the time).
    expect(b.dataset.shown).toBe('false');
    layer.reveal(1, REVEAL_LOAD_MS);
    expect(b.dataset.shown).toBe('false');
    document.dispatchEvent(new MouseEvent('pointermove', { clientX: 300, clientY: 200 }));
    runFrames();
    expect(b.dataset.shown).toBe('true');
    // Off the video: hidden at once, not after REVEAL_ACTIVITY_MS.
    document.dispatchEvent(new MouseEvent('pointermove', { clientX: 300, clientY: 600 }));
    runFrames();
    expect(b.dataset.shown).toBe('false');
    expect(layer.buttons.get(2)!.dataset.shown).toBe('true');
  });

  it('places a quiet mark beside the page’s own control in the video’s corner (a hero’s pause button)', () => {
    // The hero spans the viewport; the page's pause button sits in its top-right corner.
    const hero = rect(0, 40, 1200, 600);
    const pause = rect(1150, 44, 40, 40);
    const inside = (r: RectLike) => (b: RectLike) => b.left < r.right && b.right > r.left && b.top < r.bottom && b.bottom > r.top;
    expect(placeMark(hero, 1200, 800, () => false)).toEqual({ top: 48, right: 8, compact: true });
    expect(placeMark(hero, 1200, 800, inside(pause))).toEqual({ top: 48, right: 1200 - 8 - 32, compact: true });
    expect(placeMark(hero, 1200, 800, () => true)).toBeUndefined();

    const el = videoAt(hero);
    const button = document.createElement('button');
    button.setAttribute('aria-label', 'Pause animation');
    const link = document.createElement('a');
    link.href = 'https://www.example.com/';
    link.append(el);
    document.body.append(link, button);
    // A tile's link laid over the whole hero (beside the video, not around it) is its surface.
    const tile = document.createElement('a');
    tile.href = 'https://www.example.com/shop';
    tile.getBoundingClientRect = () => rect(0, 40, 1200, 600);
    document.body.append(tile);
    // Hit-testing: the layer itself first (skipped), then the page's button over the corner, the
    // tile's link or the video (inside a link around it, which is no other control) elsewhere.
    let layerHost: Element | undefined;
    const elementsAt = (x: number, y: number): Element[] => {
      const over = x >= pause.left && x <= pause.right && y >= pause.top && y <= pause.bottom ? [button] : x < 600 ? [tile, el] : [el];
      return layerHost ? [layerHost, ...over] : over;
    };
    const { layer, runFrames } = setup([{ id: 1, el, meta: meta(1, { width: 1200, height: 600, decorative: true, meaningful: false }), quiet: true }], { elementsAt });
    layerHost = layer.host;
    layer.mount();
    runFrames();
    const b = layer.buttons.get(1)!;
    expect(b.hidden).toBe(false);
    expect(b.style.top).toBe('48px');
    expect(b.style.right).toBe(`${1200 - 8 - 32}px`);
    // Nothing in the way: the usual top-right corner.
    button.remove();
    const free = setup([{ id: 2, el, meta: meta(2, { width: 1200, height: 600, decorative: true, meaningful: false }), quiet: true }], {
      elementsAt: (x, y) => elementsAt(x, y).filter((e) => e !== button),
    });
    free.layer.mount();
    free.runFrames();
    expect(free.layer.buttons.get(2)!.style.right).toBe('8px');
  });

  it('shows no quiet mark where controls lie under both top corners', () => {
    const el = videoAt(rect(100, 100, 640, 360));
    const control = document.createElement('button');
    document.body.append(control);
    const { layer, runFrames } = setup([{ id: 1, el, meta: meta(1, { decorative: true, meaningful: false }), quiet: true }], { elementsAt: () => [control] });
    layer.mount();
    runFrames();
    expect(layer.buttons.get(1)!.hidden).toBe(true);
  });

  it('turns a quiet mark into the usual button once the video is meaningful (unmuted)', () => {
    const el = videoAt(rect(100, 100, 640, 360));
    const v: TrackedVideo = { id: 1, el, meta: meta(1, { decorative: true, meaningful: false }), quiet: true };
    const { layer, runFrames } = setup([v]);
    layer.mount();
    runFrames();
    const b = layer.buttons.get(1)!;
    expect(b.dataset.compact).toBe('true');
    v.meta = meta(1);
    v.quiet = false;
    layer.sync();
    runFrames();
    expect(layer.buttons.get(1)).toBe(b);
    expect(b.dataset.quiet).toBeUndefined();
    expect(b.dataset.compact).toBe('false');
    layer.reveal(1, REVEAL_LOAD_MS);
    expect(b.dataset.shown).toBe('true');
  });

  it('keeps a quiet video’s mark while its panel is open', () => {
    const el = videoAt(rect(100, 100, 640, 360));
    const { layer, runFrames, click } = setup([{ id: 1, el, meta: meta(1, { decorative: true, meaningful: false }), quiet: true }]);
    layer.mount();
    runFrames();
    document.dispatchEvent(new MouseEvent('pointermove', { clientX: 300, clientY: 200 }));
    runFrames();
    const b = layer.buttons.get(1)!;
    click(b);
    expect(layer.panel.isOpen).toBe(true);
    document.dispatchEvent(new MouseEvent('pointermove', { clientX: 900, clientY: 700 }));
    runFrames();
    expect(b.dataset.shown).toBe('true');
    // Closed: shown a moment longer, then it follows the pointer again.
    layer.closePanel(false);
    expect(b.dataset.shown).toBe('true');
    vi.advanceTimersByTime(REVEAL_ACTIVITY_MS);
    expect(b.dataset.shown).toBe('false');
  });

  it('closes the panel when its video is removed or emptied, or the page moved on', async () => {
    const el = videoAt(rect(0, 0, 640, 360));
    const videos: TrackedVideo[] = [{ id: 1, el, meta: meta(1) }];
    let address = 'https://www.example.com/a';
    const { layer, click } = setup(videos, { location: () => address });
    layer.mount();
    click(layer.buttons.get(1)!);
    layer.emptied(1);
    expect(layer.panel.isOpen).toBe(false);
    click(layer.buttons.get(1)!);
    address = 'https://www.example.com/b';
    await vi.advanceTimersByTimeAsync(2000);
    expect(layer.panel.isOpen).toBe(false);
    click(layer.buttons.get(1)!);
    el.remove();
    layer.sync();
    expect(layer.panel.isOpen).toBe(false);
  });
});

describe('startOverlayUi', () => {
  it('mounts while buttons are allowed, unmounts when not, and removes itself when invalidated', () => {
    const el = videoAt(rect(0, 0, 640, 360));
    const fake = fakeApi([{ id: 1, el, meta: meta(1) }]);
    const deps = { doc: document, raf: () => undefined };
    const layer = startOverlayUi(fake.api, deps)!;
    expect(layer.mounted).toBe(true);
    expect(startOverlayUi(fake.api, deps)).toBeUndefined();
    expect(startOverlayUi({ version: 2 }, deps)).toBeUndefined();
    fake.setAllowed(false);
    fake.emit({ type: 'state' });
    expect(layer.mounted).toBe(false);
    expect(layer.host.isConnected).toBe(false);
    fake.setAllowed(true);
    fake.emit({ type: 'state' });
    expect(layer.mounted).toBe(true);
    fake.emit({ type: 'reveal', id: 1 });
    expect(layer.buttons.get(1)!.dataset.shown).toBe('true');
    fake.emit({ type: 'invalidated' });
    expect(layer.mounted).toBe(false);
    fake.emit({ type: 'state' });
    expect(layer.mounted).toBe(false);
  });

  it('opens the panel of the video the context menu asked for, also where buttons are off or it is decorative', async () => {
    const big = videoAt(rect(0, 0, 640, 360));
    const loop = videoAt(rect(0, 400, 160, 90));
    const fake = fakeApi([
      { id: 1, el: big, meta: meta(1) },
      { id: 2, el: loop, meta: meta(2, { width: 160, height: 90, decorative: true, meaningful: false }) },
    ]);
    fake.setAllowed(false);
    // The request came before the UI script was injected: it opens as soon as it starts.
    fake.setRequested(2);
    const layer = startOverlayUi(fake.api, { doc: document, raf: () => undefined, isTrusted: () => true })!;
    expect(layer.mounted).toBe(true);
    expect([...layer.buttons.keys()]).toEqual([2]);
    expect(layer.panel.isOpen).toBe(true);
    expect(layer.buttons.get(2)!.getAttribute('aria-expanded')).toBe('true');
    await vi.advanceTimersByTimeAsync(0);
    expect(fake.sent).toContainEqual({ type: 'downex:frame-offers', src: 'https://cdn.example.com/2.mp4', mse: false });
    // Closed: the request is let go, and the layer goes when the tracker says so.
    layer.closePanel(false);
    expect(fake.release).toHaveBeenCalledWith(2);
    fake.emit({ type: 'state' });
    expect(layer.mounted).toBe(false);

    // Later, from the menu while the layer runs: an open event.
    fake.setRequested(1);
    fake.emit({ type: 'open', id: 1 });
    expect(layer.mounted).toBe(true);
    expect([...layer.buttons.keys()]).toEqual([1]);
    expect(layer.panel.isOpen).toBe(true);
    // An open event for a video that is no longer the requested one does nothing.
    layer.closePanel(false);
    fake.emit({ type: 'open', id: 1 });
    expect(layer.panel.isOpen).toBe(false);
  });

  it('adds the requested video to the usual buttons where they are allowed', () => {
    const big = videoAt(rect(0, 0, 640, 360));
    const loop = videoAt(rect(0, 400, 160, 90));
    const fake = fakeApi([
      { id: 1, el: big, meta: meta(1) },
      { id: 2, el: loop, meta: meta(2, { width: 160, height: 90, decorative: true, meaningful: false }) },
    ]);
    const layer = startOverlayUi(fake.api, { doc: document, raf: () => undefined, isTrusted: () => true })!;
    expect([...layer.buttons.keys()]).toEqual([1]);
    fake.setRequested(2);
    fake.emit({ type: 'open', id: 2 });
    expect([...layer.buttons.keys()].sort()).toEqual([1, 2]);
    expect(layer.panel.isOpen).toBe(true);
    layer.closePanel(false);
    fake.emit({ type: 'videos' });
    expect([...layer.buttons.keys()]).toEqual([1]);
  });

  it('"Don\'t show on <host>" hides the buttons through the background and asks the state again', async () => {
    const el = videoAt(rect(0, 0, 640, 360));
    const fake = fakeApi([{ id: 1, el, meta: meta(1) }]);
    const layer = startOverlayUi(fake.api, { doc: document, raf: () => undefined, isTrusted: () => true })!;
    layer.buttons.get(1)!.click();
    await vi.advanceTimersByTimeAsync(0);
    const hide = layer.panelDom.el.querySelector<HTMLButtonElement>('.dx-hide')!;
    expect(hide.textContent).toBe("Don't show on www.example.com");
    hide.click();
    await vi.advanceTimersByTimeAsync(0);
    expect(fake.sent).toContainEqual({ type: 'downex:set-site-hidden', hidden: true });
    expect(fake.refreshState).toHaveBeenCalledTimes(1);
    fake.setAllowed(false);
    fake.emit({ type: 'state' });
    expect(layer.mounted).toBe(false);
  });
});

describe('hasPlayed', () => {
  it('reads the played ranges, else the position', () => {
    const v = document.createElement('video');
    expect(hasPlayed(undefined)).toBe(false);
    Object.defineProperty(v, 'played', { value: { length: 0 }, configurable: true });
    Object.defineProperty(v, 'currentTime', { value: 0, configurable: true, writable: true });
    expect(hasPlayed(v)).toBe(false);
    Object.defineProperty(v, 'currentTime', { value: 6, configurable: true, writable: true });
    expect(hasPlayed(v)).toBe(true);
    Object.defineProperty(v, 'currentTime', { value: 0, configurable: true, writable: true });
    Object.defineProperty(v, 'played', { value: { length: 1 }, configurable: true });
    expect(hasPlayed(v)).toBe(true);
  });
});
