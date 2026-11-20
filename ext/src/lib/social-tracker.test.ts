// @vitest-environment happy-dom
// The tracker on compact social sites (VIDEO-DETECT-SPEC §2.5, §5.1 "Social sites"): the late-mount
// observer, grid tiles and the carousel walk's "shown".

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { ExtensionMessage } from './messages';
import { compactSite, tilePost } from './site-video';
import {
  type IntersectionEntryLike,
  MIN_TILE_PX,
  MUTATION_CHECK_MS,
  type MutationObserverLike,
  type ObserverLike,
  type TrackerEvent,
  VideoTracker,
  type VideoTrackerDeps,
} from './video-tracker';

class FakeObserver implements ObserverLike {
  readonly targets = new Set<Element>();
  constructor(readonly cb: (entries: readonly IntersectionEntryLike[]) => void) {}
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

class FakeMutations implements MutationObserverLike {
  observed: Node | undefined;
  disconnected = false;
  constructor(readonly cb: () => void) {}
  observe(target: Node) {
    this.observed = target;
  }
  disconnect() {
    this.disconnected = true;
  }
}

function video(): HTMLVideoElement {
  const v = document.createElement('video');
  for (const [k, value] of Object.entries({ currentSrc: 'https://cdn.example.com/v.mp4', duration: 12, readyState: 4, videoWidth: 720, videoHeight: 1280 })) {
    Object.defineProperty(v, k, { value, configurable: true });
  }
  return v;
}

const trusted = new WeakSet<Event>();

function setup(url: string, over: Partial<VideoTrackerDeps> = {}) {
  let io!: FakeObserver;
  let mo: FakeMutations | undefined;
  const sent: ExtensionMessage[] = [];
  const tracker = new VideoTracker({
    doc: document,
    send: async (msg) => {
      sent.push(msg);
      if (msg.type === 'downex:overlay-state') return { button: true, host: new URL(url).host };
      if (msg.type === 'downex:video-ui') return { ok: true };
      return undefined;
    },
    intersection: (cb) => (io = new FakeObserver(cb)),
    topFrame: true,
    location: () => url,
    isTrusted: (e) => trusted.has(e),
    compactHost: (u) => compactSite(u),
    mutations: (cb) => (mo = new FakeMutations(cb)),
    tileVideo: tilePost,
    ...over,
  });
  const events: TrackerEvent[] = [];
  tracker.api.onChange((e) => events.push(e));
  const over_ = (target: Element) => {
    const e = new MouseEvent('pointerover', { bubbles: true, composed: true });
    trusted.add(e);
    target.dispatchEvent(e);
  };
  return { tracker, sent, events, io: () => io, mo: () => mo, over: over_ };
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'] });
  document.body.replaceChildren();
});
afterEach(() => {
  vi.useRealTimers();
});

describe('videos that mount late (compact sites’ top frames)', () => {
  it('checks the page once, 250 ms after the first change, however many changes come', async () => {
    const { tracker, mo } = setup('https://www.instagram.com/');
    tracker.start();
    expect(mo()?.observed).toBe(document);
    expect(tracker.trackedCount).toBe(0);
    document.body.append(video());
    for (let i = 0; i < 20; i++) mo()!.cb();
    await vi.advanceTimersByTimeAsync(MUTATION_CHECK_MS - 1);
    expect(tracker.trackedCount).toBe(0);
    await vi.advanceTimersByTimeAsync(1);
    expect(tracker.trackedCount).toBe(1);
    // A video that leaves is let go at the next check, its observers' entries too.
    document.body.replaceChildren();
    mo()!.cb();
    await vi.advanceTimersByTimeAsync(MUTATION_CHECK_MS);
    expect(tracker.trackedCount).toBe(0);
    tracker.destroy();
    expect(mo()!.disconnected).toBe(true);
  });

  it('runs no observer on other sites, in sub-frames, or without the dependency', () => {
    for (const [url, top] of [
      ['https://news.example.com/', true],
      ['https://vimeo.com/76979871', true],
      ['https://www.tiktok.com/embed/v2/7300000000000000001', false],
    ] as const) {
      const { tracker, mo } = setup(url, { topFrame: top });
      tracker.start();
      expect(mo(), url).toBeUndefined();
      tracker.destroy();
    }
  });
});

describe('grid tiles (§5.1 "Grid tiles")', () => {
  const tileLink = (href: string, w = 300, h = 400) => {
    const a = document.createElement('a');
    a.href = href;
    a.getBoundingClientRect = () => ({ left: 0, top: 0, right: w, bottom: h, width: w, height: h, x: 0, y: 0, toJSON: () => ({}) }) as DOMRect;
    const img = document.createElement('img');
    a.append(img);
    document.body.append(a);
    return { a, img };
  };

  it('takes a hovered link to a reel as the frame’s tile, and asks for the overlay UI', async () => {
    const { tracker, sent, events, over } = setup('https://www.instagram.com/some.user/');
    tracker.start();
    const { a, img } = tileLink('https://www.instagram.com/reel/AbCdEf12345/');
    over(img);
    expect(tracker.api.tile).toEqual({ el: a, url: 'https://www.instagram.com/reel/AbCdEf12345/' });
    expect(events).toContainEqual({ type: 'tile' });
    await vi.advanceTimersByTimeAsync(5);
    expect(sent.map((m) => m.type)).toEqual(['downex:overlay-state', 'downex:video-ui']);
    // The tile leaves the page: there is none.
    a.remove();
    expect(tracker.api.tile).toBeUndefined();
  });

  it('takes no photo post, other origin, small link, link with a video that has a button, or untrusted event', () => {
    const withVideo = tileLink('https://www.instagram.com/reel/HasVideo123/');
    const v = video();
    withVideo.a.append(v);
    const { tracker, over, io } = setup('https://www.instagram.com/some.user/');
    tracker.start();
    io().cb([{ target: v, intersectionRatio: 1, boundingClientRect: { width: 300, height: 400 } }]);
    over(tileLink('https://www.instagram.com/p/AbCdEf12345/').img);
    over(tileLink('https://www.facebook.com/reel/1234567890').img);
    over(tileLink('https://www.instagram.com/reel/SmallTile01/', MIN_TILE_PX - 1, 300).img);
    over(withVideo.img);
    const plain = tileLink('https://www.instagram.com/reel/Untrusted01/');
    plain.img.dispatchEvent(new MouseEvent('pointerover', { bubbles: true, composed: true }));
    expect(tracker.api.tile).toBeUndefined();
  });

  it('takes a tile whose hover preview is too small for a button of its own (a 148 px TikTok tile)', () => {
    const t = tileLink('https://www.tiktok.com/@someone/video/7300000000000000002', 148, 197);
    const preview = video();
    t.a.append(preview);
    const { tracker, over, io } = setup('https://www.tiktok.com/explore');
    tracker.start();
    io().cb([{ target: preview, intersectionRatio: 1, boundingClientRect: { width: 148, height: 197 } }]);
    expect(tracker.videos().some((x) => x.meta.meaningful || x.quiet)).toBe(false);
    over(t.img);
    expect(tracker.api.tile).toEqual({ el: t.a, url: 'https://www.tiktok.com/@someone/video/7300000000000000002' });
  });

  it('takes no tile on a video address or in private messages, and drops one when the frame navigates there', () => {
    for (const page of [
      'https://www.tiktok.com/@someone/video/7300000000000000001',
      'https://www.instagram.com/reel/PageReel123/',
      'https://www.instagram.com/direct/t/1234567890/',
      'https://x.com/i/chat/1234567890-9876543210',
    ]) {
      document.body.replaceChildren();
      const { tracker, over } = setup(page);
      tracker.start();
      const host = new URL(page).origin;
      over(tileLink(host.includes('tiktok') ? `${host}/@someone/video/7300000000000000002` : host.includes('x.com') ? `${host}/someone/status/1234567890/video/1` : `${host}/reel/TileReel123/`).img);
      expect(tracker.api.tile, page).toBeUndefined();
      tracker.destroy();
    }
    // A profile's tile, then the frame goes to a video address (a single-page app's navigation).
    document.body.replaceChildren();
    let url = 'https://www.tiktok.com/@someone';
    const { tracker, over, events } = setup('https://www.tiktok.com/@someone', { location: () => url });
    tracker.start();
    over(tileLink('https://www.tiktok.com/@someone/video/7300000000000000002').img);
    expect(tracker.api.tile).toBeDefined();
    events.length = 0;
    url = 'https://www.tiktok.com/@someone/video/7300000000000000001';
    tracker.navigated();
    expect(tracker.api.tile).toBeUndefined();
    expect(events).toContainEqual({ type: 'tile' });
  });

  it('listens to nothing of the kind on other sites', () => {
    const { tracker, over } = setup('https://news.example.com/');
    tracker.start();
    // The same origin as the frame, but not a compact site's.
    over(tileLink('https://news.example.com/reel/AbCdEf12345/').img);
    expect(tracker.api.tile).toBeUndefined();
  });
});

describe('shown, for the carousel walk', () => {
  it('is known only for tracked videos with a ratio', async () => {
    const v = video();
    const w = video();
    document.body.append(v, w);
    const { tracker, io } = setup('https://www.instagram.com/');
    tracker.start();
    expect(tracker.api.shown?.(v)).toBeUndefined();
    io().cb([
      { target: v, intersectionRatio: 1, boundingClientRect: { width: 470, height: 587 } },
      { target: w, intersectionRatio: 0, boundingClientRect: { width: 470, height: 587 } },
    ]);
    expect(tracker.api.shown?.(v)).toBe(true);
    expect(tracker.api.shown?.(w)).toBe(false);
    expect(tracker.api.shown?.(document.createElement('video'))).toBeUndefined();
  });
});
