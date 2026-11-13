// @vitest-environment happy-dom

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { ExtensionMessage } from './messages';
import { isFrameVideoMeta, MAX_FRAME_VIDEOS, MAX_FRAME_VIDEOS_BYTES } from './messages';
import { DEFAULT_VIDEO_OPTIONS, type VideoOptions } from './options-form';
import type { PrefetchDwellDeps } from './video-prefetch';
import {
  DEEP_SCAN_MAX_DEPTH,
  DEEP_SCAN_MAX_ELEMENTS,
  type IntersectionEntryLike,
  isTrackerApi,
  MAX_TRACKED_VIDEOS,
  type ObserverLike,
  type ResizeEntryLike,
  TRACKER_KEY,
  type TrackerEvent,
  VideoTracker,
  type VideoTrackerDeps,
} from './video-tracker';

class FakeObserver<E> implements ObserverLike {
  readonly targets = new Set<Element>();
  constructor(readonly cb: (entries: readonly E[]) => void) {}
  observe(t: Element) {
    this.targets.add(t);
  }
  unobserve(t: Element) {
    this.targets.delete(t);
  }
  disconnect() {
    this.targets.clear();
  }
}

interface VideoProps {
  src?: string;
  duration?: number;
  readyState?: number;
  muted?: boolean;
  loop?: boolean;
  controls?: boolean;
  videoWidth?: number;
  videoHeight?: number;
  srcObject?: unknown;
  mediaKeys?: unknown;
}

function makeVideo(p: VideoProps = {}): HTMLVideoElement {
  const v = document.createElement('video');
  setVideo(v, { src: 'https://cdn.example.com/v/clip.mp4#t=3', duration: 120, readyState: 4, videoWidth: 1280, videoHeight: 720, ...p });
  return v;
}

function setVideo(v: HTMLVideoElement, p: VideoProps): void {
  const props: Record<string, unknown> = {
    currentSrc: p.src,
    duration: p.duration,
    readyState: p.readyState,
    muted: p.muted ?? false,
    loop: p.loop ?? false,
    controls: p.controls ?? false,
    videoWidth: p.videoWidth ?? 0,
    videoHeight: p.videoHeight ?? 0,
    srcObject: p.srcObject ?? null,
    mediaKeys: p.mediaKeys ?? null,
  };
  for (const [k, value] of Object.entries(props)) {
    if (k === 'currentSrc') Object.defineProperty(v, k, { value: value ?? '', configurable: true, writable: true });
    else if (value !== undefined) Object.defineProperty(v, k, { value, configurable: true, writable: true });
  }
}

function setup(over: Partial<VideoTrackerDeps> = {}, state = { button: true, host: 'www.example.com' }) {
  let io!: FakeObserver<IntersectionEntryLike>;
  let ro!: FakeObserver<ResizeEntryLike>;
  const sent: ExtensionMessage[] = [];
  const send = vi.fn(async (msg: ExtensionMessage): Promise<unknown> => {
    sent.push(msg);
    if (msg.type === 'downex:overlay-state') return state;
    if (msg.type === 'downex:video-ui') return { ok: true };
    return undefined;
  });
  const tracker = new VideoTracker({
    doc: document,
    send,
    intersection: (cb) => (io = new FakeObserver(cb)),
    resize: (cb) => (ro = new FakeObserver(cb)),
    ...over,
  });
  const events: TrackerEvent[] = [];
  tracker.api.onChange((e) => events.push(e));
  /** The observers report `v` at that size and part inside the viewport. */
  const show = (v: Element, width = 640, height = 360, ratio = 1) => {
    io.cb([{ target: v, intersectionRatio: ratio, boundingClientRect: { width, height } }]);
  };
  const resize = (v: Element, width: number, height: number) => ro.cb([{ target: v, contentRect: { width, height } }]);
  const types = () => sent.map((m) => m.type);
  const reports = () => sent.filter((m) => m.type === 'downex:frame-videos').map((m) => (m as { videos: unknown[] }).videos);
  return { tracker, send, sent, types, reports, events, show, resize, io: () => io, ro: () => ro, state };
}

/** Runs the timers due now, also those started by them and by answered messages (a 0 ms timer started during a tick waits 1 ms). */
async function flush(): Promise<void> {
  for (let i = 0; i < 5; i++) await vi.advanceTimersByTimeAsync(1);
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'] });
  document.body.replaceChildren();
});
afterEach(() => {
  vi.useRealTimers();
});

describe('VideoTracker: which videos get a button', () => {
  it('reports a playing, visible, large video as meaningful, without page text', async () => {
    const v = makeVideo();
    document.body.append(v);
    const { tracker, show, reports } = setup();
    tracker.start();
    show(v);
    await flush();
    const [meta] = tracker.videos().map((x) => x.meta);
    expect(meta).toEqual({
      id: 1,
      src: 'https://cdn.example.com/v/clip.mp4',
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
    });
    expect(isFrameVideoMeta(meta)).toBe(true);
    await flush();
    expect(reports()).toEqual([[meta]]);
  });

  it.each<[string, VideoProps, [number, number, number] | undefined, boolean]>([
    ['DRM (mediaKeys)', { mediaKeys: {} }, undefined, false],
    ['live (Infinity)', { duration: Number.POSITIVE_INFINITY }, undefined, false],
    ['a camera or call stream (srcObject)', { srcObject: {} }, undefined, false],
    ['too narrow', {}, [199, 360, 1], false],
    ['too low', {}, [640, 111, 1], false],
    ['less than half visible', {}, [640, 360, 0.49], false],
    ['decorative (muted loop without controls under 30 s)', { muted: true, loop: true, duration: 12 }, undefined, false],
    ['a muted loop with controls', { muted: true, loop: true, controls: true, duration: 12 }, undefined, true],
    ['a muted loop of 30 s', { muted: true, loop: true, duration: 30 }, undefined, true],
    ['without any source', { src: '', readyState: 0 }, undefined, false],
    ['MSE (blob:)', { src: 'blob:https://www.example.com/1234' }, undefined, true],
    ['a source known only by its metadata', { src: '', readyState: 1 }, undefined, true],
  ])('%s', async (_name, props, size, meaningful) => {
    const v = makeVideo(props);
    document.body.append(v);
    const { tracker, show } = setup();
    tracker.start();
    show(v, ...(size ?? [640, 360, 1]));
    const meta = tracker.videos()[0]!.meta;
    expect(meta.meaningful).toBe(meaningful);
    expect(isFrameVideoMeta(meta)).toBe(true);
  });

  it('marks DRM after an encrypted or waitingforkey event and checks again at click time', async () => {
    const v = makeVideo();
    const w = makeVideo();
    document.body.append(v, w);
    const { tracker, show } = setup();
    tracker.start();
    show(v);
    show(w);
    expect(tracker.api.recheck(1)?.meaningful).toBe(true);
    v.dispatchEvent(new Event('encrypted'));
    expect(tracker.api.recheck(1)).toMatchObject({ drm: true, meaningful: false });
    // mediaKeys set after the button appeared, without an event: the click-time check sees it.
    expect(tracker.api.recheck(2)?.meaningful).toBe(true);
    setVideo(w, { src: 'https://cdn.example.com/w.mp4', duration: 60, readyState: 4, mediaKeys: {} });
    expect(tracker.api.recheck(2)).toMatchObject({ drm: true, meaningful: false });
    // A stream that turned out live.
    setVideo(w, { src: 'https://cdn.example.com/w.mp4', duration: Number.POSITIVE_INFINITY, readyState: 4 });
    expect(tracker.api.recheck(2)).toMatchObject({ durationMs: -2, meaningful: false });
    expect(tracker.api.recheck(99)).toBeUndefined();
    v.dispatchEvent(new Event('waitingforkey'));
  });

  it('takes a video whose finite duration keeps growing as live (a player that hides Infinity)', () => {
    const v = makeVideo({ src: 'blob:https://www.example.com/1', duration: 30 });
    document.body.append(v);
    const { tracker, show } = setup();
    tracker.start();
    show(v);
    expect(tracker.api.recheck(1)?.meaningful).toBe(true);
    // Known once, then grown twice (a growth below 1 s, an MSE end adjustment, does not count).
    for (const d of [36, 36.5, 42, 48]) {
      setVideo(v, { src: 'blob:https://www.example.com/1', duration: d, readyState: 4 });
      v.dispatchEvent(new Event('durationchange'));
    }
    expect(tracker.api.recheck(1)).toMatchObject({ durationMs: 48_000, meaningful: true });
    setVideo(v, { src: 'blob:https://www.example.com/1', duration: 54, readyState: 4 });
    v.dispatchEvent(new Event('durationchange'));
    expect(tracker.api.recheck(1)).toMatchObject({ durationMs: -2, meaningful: false });
    // Another source starts over.
    setVideo(v, { src: 'blob:https://www.example.com/2', duration: 600, readyState: 4 });
    v.dispatchEvent(new Event('emptied'));
    v.dispatchEvent(new Event('durationchange'));
    expect(tracker.api.recheck(1)).toMatchObject({ durationMs: 600_000, meaningful: true });
  });

  it('makes room for a video that plays by letting go of one off-screen', () => {
    const videos = Array.from({ length: 40 }, () => makeVideo());
    document.body.append(...videos);
    const { tracker, show } = setup();
    tracker.start();
    expect(tracker.trackedCount).toBe(MAX_TRACKED_VIDEOS);
    const tracked = () => tracker.videos().map((v) => v.el);
    expect(tracked()).not.toContain(videos[39]);
    // Nothing known to be off-screen: no room.
    videos[39]!.dispatchEvent(new Event('play'));
    expect(tracked()).not.toContain(videos[39]);
    // The first ones scrolled out of view; the 40th plays.
    for (const v of videos.slice(0, 8)) show(v, 640, 360, 0);
    videos[39]!.dispatchEvent(new Event('play'));
    expect(tracked()).toContain(videos[39]);
    expect(tracked()).not.toContain(videos[0]);
    expect(tracker.trackedCount).toBe(MAX_TRACKED_VIDEOS);
  });

  it('swaps videos known to be off-screen for untracked ones, round the page, so the one scrolled to is tracked', () => {
    const videos = Array.from({ length: 70 }, () => makeVideo());
    document.body.append(...videos);
    const { tracker, show } = setup();
    tracker.start();
    const tracked = () => new Set(tracker.videos().map((v) => v.el));
    const pointer = () => {
      vi.advanceTimersByTime(1000);
      document.dispatchEvent(new Event('pointermove'));
    };
    // Nothing known to be off-screen yet: nothing changes.
    pointer();
    expect(tracker.trackedCount).toBe(MAX_TRACKED_VIDEOS);
    expect(tracked().has(videos[32]!)).toBe(false);
    // The first video stays in view; the user scrolls down to the last one, which does not play.
    show(videos[0]!);
    let rounds = 0;
    for (; rounds < 5 && !tracked().has(videos[69]!); rounds++) {
      for (const v of tracked()) if (v !== videos[0]) show(v, 640, 360, 0);
      pointer();
    }
    expect(rounds).toBeLessThanOrEqual(3);
    expect(tracked().has(videos[69]!)).toBe(true);
    expect(tracked().has(videos[0]!)).toBe(true); // a video in view is never let go
    expect(tracker.trackedCount).toBe(MAX_TRACKED_VIDEOS);
    show(videos[69]!);
    expect(tracker.videos().find((v) => v.el === videos[69])!.meta.meaningful).toBe(true);
  });

  it('takes sizes from the ResizeObserver too, and drops removed videos', async () => {
    const v = makeVideo();
    document.body.append(v);
    const { tracker, show, resize, io } = setup();
    tracker.start();
    show(v, 100, 50);
    expect(tracker.videos()[0]!.meta.meaningful).toBe(false);
    resize(v, 800, 450);
    expect(tracker.videos()[0]!.meta).toMatchObject({ width: 800, height: 450, meaningful: true });
    v.remove();
    tracker.check();
    expect(tracker.trackedCount).toBe(0);
    expect(io().targets.size).toBe(0);
  });

  it('tracks at most 32 videos and finds new ones on media events and pointer activity (at most once a second)', async () => {
    const { tracker } = setup();
    tracker.start();
    for (let i = 0; i < 40; i++) document.body.append(makeVideo());
    expect(tracker.trackedCount).toBe(0);
    document.body.firstElementChild!.dispatchEvent(new Event('loadstart'));
    expect(tracker.trackedCount).toBe(MAX_TRACKED_VIDEOS);

    const other = setup();
    document.body.replaceChildren();
    other.tracker.start();
    document.body.append(makeVideo());
    document.dispatchEvent(new Event('pointermove'));
    expect(other.tracker.trackedCount).toBe(1);
    document.body.append(makeVideo());
    document.dispatchEvent(new Event('pointermove'));
    expect(other.tracker.trackedCount).toBe(1);
    vi.advanceTimersByTime(1000);
    document.dispatchEvent(new Event('pointermove'));
    expect(other.tracker.trackedCount).toBe(2);
  });

  it('never uses a subtree MutationObserver', async () => {
    const constructed: unknown[] = [];
    const Original = globalThis.MutationObserver;
    globalThis.MutationObserver = class {
      constructor(cb: unknown) {
        constructed.push(cb);
      }
      observe() {}
      disconnect() {}
      takeRecords() {
        return [];
      }
    } as unknown as typeof MutationObserver;
    try {
      const v = makeVideo();
      document.body.append(v);
      const { tracker, show } = setup();
      tracker.start();
      show(v);
      tracker.deepScan();
      await vi.advanceTimersByTimeAsync(2000);
      tracker.destroy();
      expect(constructed).toEqual([]);
    } finally {
      globalThis.MutationObserver = Original;
    }
  });
});

describe('VideoTracker: messages', () => {
  it('sends nothing on a page without a video worth a button', async () => {
    const tiny = makeVideo();
    document.body.append(tiny);
    const { tracker, show, sent } = setup();
    tracker.start();
    show(tiny, 100, 60);
    await vi.advanceTimersByTimeAsync(5000);
    expect(sent).toEqual([]);
  });

  it('asks for the overlay state, then for the UI, once a video is meaningful', async () => {
    const v = makeVideo();
    document.body.append(v);
    const { tracker, show, types, events } = setup();
    tracker.start();
    await flush();
    expect(types()).toEqual([]);
    show(v);
    await flush();
    expect(types()[0]).toBe('downex:overlay-state');
    expect([...types()].sort()).toEqual(['downex:frame-videos', 'downex:overlay-state', 'downex:video-ui']);
    expect(tracker.api.allowed).toBe(true);
    expect(events.some((e) => e.type === 'state')).toBe(true);
    // Never twice.
    show(v, 700, 400);
    await vi.advanceTimersByTimeAsync(2000);
    expect(types().filter((t) => t === 'downex:video-ui')).toHaveLength(1);
  });

  it('does not ask for the UI on a hidden site, and asks again when the options change', async () => {
    const v = makeVideo();
    document.body.append(v);
    const s = setup({}, { button: false, host: 'www.example.com' });
    s.tracker.start();
    s.show(v);
    await flush();
    expect(s.types()).toEqual(['downex:overlay-state', 'downex:frame-videos']);
    expect(s.tracker.api.allowed).toBe(false);
    s.state.button = true;
    s.tracker.optionsChanged({ ...DEFAULT_VIDEO_OPTIONS });
    await flush();
    expect(s.types().slice(2)).toEqual(['downex:overlay-state', 'downex:video-ui']);
    expect(s.tracker.api.allowed).toBe(true);
    // "Don't show on <host>": asked again, the layer learns it may not show.
    s.state.button = false;
    s.tracker.api.refreshState();
    await flush();
    expect(s.tracker.api.allowed).toBe(false);
    expect(s.events.filter((e) => e.type === 'state')).toHaveLength(2);
  });

  it('reports protected and live players (the popup and the badge need them), at most once a second and only on changes', async () => {
    const v = makeVideo({ mediaKeys: {} });
    document.body.append(v);
    const { tracker, show, reports, types } = setup();
    tracker.start();
    show(v);
    await flush();
    expect(types()).toEqual(['downex:frame-videos']);
    expect(reports()[0]).toEqual([expect.objectContaining({ drm: true, meaningful: false })]);
    show(v, 700, 400);
    await flush();
    expect(reports()).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(1000);
    expect(reports()).toHaveLength(2);
    show(v, 700, 400);
    await vi.advanceTimersByTimeAsync(5000);
    expect(reports()).toHaveLength(2);
    // Removed: one empty report.
    v.remove();
    tracker.check();
    await vi.advanceTimersByTimeAsync(1000);
    expect(reports().at(-1)).toEqual([]);
  });

  it('reports again after the page came back from the back/forward cache', async () => {
    const v = makeVideo();
    document.body.append(v);
    const { tracker, show, reports } = setup();
    tracker.start();
    show(v);
    await vi.advanceTimersByTimeAsync(1000);
    expect(reports()).toHaveLength(1);
    // The background cleared the tab when the restored page committed; the same report goes again.
    tracker.restored();
    await vi.advanceTimersByTimeAsync(1000);
    expect(reports()).toHaveLength(2);
    expect(reports()[1]).toEqual(reports()[0]);
  });

  it('keeps a report within 16 videos and 64 KiB, meaningful ones first', async () => {
    const long = `https://cdn.example.com/${'a'.repeat(6000)}.mp4`;
    const videos = Array.from({ length: 24 }, (_, i) => makeVideo({ src: `${long}?n=${i}` }));
    document.body.append(...videos);
    const { tracker, show } = setup();
    tracker.start();
    videos.forEach((v, i) => show(v, i === 23 ? 1280 : 300, i === 23 ? 720 : 200));
    const list = tracker.reportVideos();
    expect(list.length).toBeLessThanOrEqual(MAX_FRAME_VIDEOS);
    expect(JSON.stringify(list).length).toBeLessThanOrEqual(MAX_FRAME_VIDEOS_BYTES);
    expect(list[0]!.id).toBe(24);
    expect(new Set(list.map((m) => m.id)).size).toBe(list.length);
  });

  it('emits reveal on play and loadedmetadata, and emptied', async () => {
    const v = makeVideo();
    document.body.append(v);
    const { tracker, events } = setup();
    tracker.start();
    v.dispatchEvent(new Event('play'));
    v.dispatchEvent(new Event('loadedmetadata'));
    v.dispatchEvent(new Event('emptied'));
    expect(events.filter((e) => e.type !== 'videos')).toEqual([
      { type: 'reveal', id: 1 },
      { type: 'reveal', id: 1 },
      { type: 'emptied', id: 1 },
    ]);
  });

  it('publishes its API for the overlay UI and stops for good when invalidated', async () => {
    const v = makeVideo();
    document.body.append(v);
    const { tracker, show, send, events } = setup();
    const target: Record<symbol, unknown> = {};
    tracker.publish(target);
    expect(isTrackerApi(target[TRACKER_KEY])).toBe(true);
    expect(isTrackerApi({ version: 2, videos() {}, onChange() {}, send() {} })).toBe(false);
    expect(tracker.api.claimUi()).toBe(true);
    expect(tracker.api.claimUi()).toBe(false);
    tracker.start();
    tracker.destroy();
    expect(tracker.api.invalidated).toBe(true);
    expect(events.at(-1)).toEqual({ type: 'invalidated' });
    show(v);
    v.dispatchEvent(new Event('play'));
    await vi.advanceTimersByTimeAsync(5000);
    expect(send).not.toHaveBeenCalled();
    expect(await tracker.api.send({ type: 'downex:overlay-state' })).toBeUndefined();
  });

  it('answers undefined when the extension is gone', async () => {
    const { tracker } = setup({ send: async () => Promise.reject(new Error('Extension context invalidated.')) });
    expect(await tracker.api.send({ type: 'downex:overlay-state' })).toBeUndefined();
  });
});

describe('VideoTracker: shadow roots', () => {
  /** Hosts with closed roots the injected provider can open. */
  function shadowTree(depth: number): { outer: HTMLElement; roots: Map<Element, ShadowRoot>; at: (d: number) => ShadowRoot } {
    const roots = new Map<Element, ShadowRoot>();
    const list: ShadowRoot[] = [];
    const outer = document.createElement('player-shell');
    let host: HTMLElement = outer;
    for (let d = 0; d < depth; d++) {
      const root = host.attachShadow({ mode: 'closed' });
      roots.set(host, root);
      list.push(root);
      const next = document.createElement(`level-${d}`);
      root.append(next);
      host = next;
    }
    return { outer, roots, at: (d) => list[d]! };
  }

  it('finds videos in nested closed shadow roots on frame-media-seen, up to 16 levels', async () => {
    const tree = shadowTree(20);
    const shallow = makeVideo();
    const deep = makeVideo();
    tree.at(2).append(shallow);
    tree.at(DEEP_SCAN_MAX_DEPTH).append(deep);
    document.body.append(tree.outer);
    const { tracker, show } = setup({ shadowRoot: (el) => tree.roots.get(el) ?? null });
    tracker.start();
    expect(tracker.trackedCount).toBe(0);
    tracker.onFrameMessage({ type: 'downex:frame-media-seen' });
    expect(tracker.videos().map((v) => v.el)).toEqual([shallow]);
    // Its media events do not leave the shadow root: the tracker listens on the video itself.
    show(shallow);
    shallow.dispatchEvent(new Event('encrypted'));
    expect(tracker.videos()[0]!.meta.drm).toBe(true);
  });

  it('looks at no more than 5,000 elements', () => {
    const wrap = document.createElement('div');
    for (let i = 0; i < 6000; i++) wrap.append(document.createElement('span'));
    const late = makeVideo();
    wrap.append(late);
    const host = document.createElement('x-player');
    const root = host.attachShadow({ mode: 'closed' });
    root.append(wrap);
    document.body.append(host);
    const { tracker } = setup({ shadowRoot: (el) => (el === host ? root : null) });
    tracker.start();
    expect(tracker.deepScan()).toBe(DEEP_SCAN_MAX_ELEMENTS);
    expect(tracker.trackedCount).toBe(0);
  });
});

describe('VideoTracker: look-ahead dwell', () => {
  const curated = (url: string) => (url.startsWith('https://vimeo.com/') ? { lookAhead: true } : undefined);

  function dwellSetup(url: string, over: Partial<VideoTrackerDeps> = {}, options: VideoOptions = { ...DEFAULT_VIDEO_OPTIONS }) {
    let deps: PrefetchDwellDeps | undefined;
    const dwell = { sync: vi.fn(), stop: vi.fn() };
    let address = url;
    const readOptions = vi.fn(async () => options);
    const s = setup({
      topFrame: true,
      location: () => address,
      lookAheadVideo: curated,
      readOptions,
      visible: () => true,
      dwell: (d) => {
        deps = d;
        return dwell;
      },
      ...over,
    });
    return { ...s, dwell, readOptions, deps: () => deps, go: (u: string) => (address = u) };
  }

  it('runs in the top frame of a curated address with the button and look-ahead options', async () => {
    const s = dwellSetup('https://vimeo.com/76979871');
    s.tracker.start();
    expect(s.deps()).toBeDefined();
    await flush();
    expect(s.readOptions).toHaveBeenCalledTimes(1);
    expect(s.deps()!.read()).toEqual({ url: 'https://vimeo.com/76979871', visible: true, enabled: true });
    expect(s.dwell.sync).toHaveBeenCalled();
    s.deps()!.fire('https://vimeo.com/76979871');
    expect(s.sent).toContainEqual({ type: 'downex:page-video-prefetch', url: 'https://vimeo.com/76979871' });
    // Hidden on the site, or look-ahead off: not enabled.
    s.tracker.optionsChanged({ ...DEFAULT_VIDEO_OPTIONS, hiddenSites: ['vimeo.com'] });
    expect(s.deps()!.read().enabled).toBe(false);
    s.tracker.optionsChanged({ ...DEFAULT_VIDEO_OPTIONS, videoPrefetch: false });
    expect(s.deps()!.read().enabled).toBe(false);
    s.tracker.optionsChanged({ ...DEFAULT_VIDEO_OPTIONS });
    expect(s.deps()!.read().enabled).toBe(true);
  });

  it('restarts on navigation, visibility and a new video; never in sub-frames or elsewhere', async () => {
    const s = dwellSetup('https://www.example.com/');
    s.tracker.start();
    expect(s.deps()).toBeUndefined();
    expect(s.readOptions).not.toHaveBeenCalled();
    s.go('https://vimeo.com/1');
    s.tracker.navigated();
    expect(s.deps()).toBeDefined();
    const calls = s.dwell.sync.mock.calls.length;
    s.tracker.visibilityChanged();
    document.body.append(makeVideo());
    s.tracker.check();
    expect(s.dwell.sync.mock.calls.length).toBe(calls + 2);
    s.tracker.destroy();
    expect(s.dwell.stop).toHaveBeenCalled();

    const sub = dwellSetup('https://vimeo.com/1', { topFrame: false });
    sub.tracker.start();
    expect(sub.deps()).toBeUndefined();
  });
});
