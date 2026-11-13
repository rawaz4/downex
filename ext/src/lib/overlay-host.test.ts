// @vitest-environment happy-dom

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { ExtensionMessage } from './messages';
import type { FrameOffers, FrameVideoMeta } from './offer-types';
import { OFFER_PANEL_CSS } from './offer-panel';
import {
  applyStyles,
  buttonVideos,
  LAYER_CSS,
  LAYER_HOST_CSS,
  LAYER_STYLE,
  LAYER_TAG,
  MAX_BUTTONS,
  OverlayLayer,
  type OverlayLayerDeps,
  placeButton,
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
  const metas = new Map(videos.map((v) => [v.id, v.meta]));
  const refreshState = vi.fn();
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
  };
  return {
    api,
    sent,
    metas,
    refreshState,
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
    expect(LAYER_STYLE).toBe(LAYER_HOST_CSS + OVERLAY_SHARED_CSS + LAYER_CSS + OFFER_PANEL_CSS);
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
    expect(sent).toContainEqual({ type: 'downex:frame-offers', src: '', mse: true });
    expect(sent).toContainEqual({ type: 'downex:page-video-probe', url: 'https://www.example.com/watch' });
    expect(layer.panelDom.el.querySelector('.dx-status-text')!.textContent).toBe('Downex found no video on this page.');
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
