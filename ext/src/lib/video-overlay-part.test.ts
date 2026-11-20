// @vitest-environment happy-dom

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { type ClipRange, CLIP_TEXTS, pastEndText } from './clip';
import { PREVIEW_TICK_MS, SCRUB_LINGER_MS } from './part-row';
import type { AddedResult, Outcome, VideoInfoResult, VideoTrack } from './protocol';
import { playerVideo, VideoOverlay, type VideoOverlayDeps } from './video-overlay';
import type { TrackPick } from './video-picker';

// The YouTube panel's Part row, the timeline trimmer (MEDIA-SPEC §8, SITE-MEDIA-SPEC §5.1,
// VIDEO-DETECT-SPEC §5.2).

function track(t: Partial<VideoTrack> & Pick<VideoTrack, 'id' | 'kind'>): VideoTrack {
  return { ext: 'mp4', codecs: '', width: 0, height: 0, frameRate: 0, bandwidth: 0, size: 0, sizeApprox: false, language: '', name: '', ...t };
}

const INFO: VideoInfoResult = {
  type: 'videoInfo',
  site: 'YouTube',
  title: 'A talk',
  durationMs: 754_000,
  format: 'http',
  container: 'mp4',
  selectedVideo: '299',
  selectedAudio: '140',
  tracks: [
    track({ id: '299', kind: 'video', codecs: 'avc1.64002a', width: 1920, height: 1080, size: 742 * 1_000_000, name: '1080p (Full HD)' }),
    track({ id: '136', kind: 'video', codecs: 'avc1.4d401f', width: 1280, height: 720, size: 365 * 1_000_000, name: '720p (HD)' }),
    track({ id: '140', kind: 'audio', ext: 'm4a', codecs: 'mp4a.40.2', size: 12 * 1_000_000, language: 'en', name: 'English' }),
  ],
  chapters: [
    { startMs: 0, endMs: 80_000, title: 'Intro' },
    { startMs: 80_000, endMs: 754_000, title: 'The talk' },
  ],
};

/** The page's <video> as the trimmer sees it: its position, play state and their events. */
class FakeVideo extends EventTarget {
  currentTime = 83.4;
  paused = true;
  seeking = false;
  ended = false;
  /** The media's length in s (undefined: not told, as the other tests' video). */
  duration?: number;
  seeks: number[] = [];
  constructor() {
    super();
    // Every assignment is recorded (a real element would seek).
    let t = this.currentTime;
    Object.defineProperty(this, 'currentTime', {
      get: () => t,
      set: (v: number) => {
        t = v;
        this.seeks.push(Math.round(v * 1000));
      },
    });
  }
  play(): Promise<void> {
    if (this.paused) {
      this.paused = false;
      this.dispatchEvent(new Event('play'));
    }
    return Promise.resolve();
  }
  pause(): void {
    if (!this.paused) {
      this.paused = true;
      this.dispatchEvent(new Event('pause'));
    }
  }
  /** Where the video is now, in ms. */
  get ms(): number {
    return Math.round(this.currentTime * 1000);
  }
}

/** Events the test marks as the user's (happy-dom has no isTrusted). */
const trusted = new WeakSet<Event>();

/** The track's box: 754 px wide from x = 100, so that 1 px is 1 s of INFO's video. */
const BOX = { left: 100, width: 754 };
const x = (ms: number) => BOX.left + ms / 1000;

function setup(opts: { clips?: boolean | undefined; info?: VideoInfoResult; extra?: Partial<VideoOverlayDeps>; noVideo?: boolean } = {}) {
  document.body.replaceChildren();
  const player = document.createElement('div');
  player.className = 'html5-video-player';
  const host = document.createElement('downex-video');
  player.append(host);
  document.body.append(player);
  const shadow = host.attachShadow({ mode: 'closed' });
  const container = document.createElement('div');
  shadow.append(container);
  const adds: [TrackPick, ClipRange | undefined][] = [];
  const video = new FakeVideo();
  video.seeks.length = 0;
  const clips = opts.clips;
  const deps: VideoOverlayDeps = {
    probe: async () => ({ ok: true, value: opts.info ?? INFO }),
    add: async (pick, clip): Promise<Outcome<AddedResult>> => {
      adds.push([pick, clip]);
      return { ok: true, value: { type: 'added', downloadId: 4, awaiting: false } };
    },
    isTrusted: (e) => trusted.has(e),
    ...(opts.noVideo ? {} : { video: () => video }),
    ...opts.extra,
  };
  if (clips !== undefined) deps.clips = async () => clips;
  const overlay = new VideoOverlay(document, deps);
  overlay.attach(container, shadow);
  overlay.setVideo('youtube:aqz-KE-bpKQ');
  const mark = <E extends Event>(e: E): E => {
    trusted.add(e);
    return e;
  };
  const click = (el: Element) => el.dispatchEvent(mark(new MouseEvent('click', { bubbles: true, composed: true })));
  const q = <T extends Element>(sel: string) => overlay.panelEl.querySelector<T>(sel)!;
  const part = () => q<HTMLElement>('.dx-part');
  const body = () => q<HTMLElement>('.dx-part-body');
  const toggle = () => q<HTMLButtonElement>('.dx-part-toggle');
  const summary = () => q('.dx-part-sum').textContent;
  const trim = () => q<HTMLElement>('.dx-trim');
  const handle = (which: 'from' | 'to') => q<HTMLElement>(`.dx-trim-handle[data-which='${which}']`);
  const chip = (which: 'from' | 'to') => q<HTMLButtonElement>(`.dx-trim-chip[data-which='${which}']`);
  const field = (which: 'from' | 'to') => chip(which).nextElementSibling as HTMLInputElement;
  const error = () => q<HTMLElement>('.dx-part-error');
  const rows = () => [...overlay.panelEl.querySelectorAll<HTMLButtonElement>('button.dx-row')];
  const button = (sel: string) => q<HTMLButtonElement>(sel);
  const pointer = (type: string, target: Element, clientX: number, init: PointerEventInit = {}) =>
    target.dispatchEvent(mark(new PointerEvent(type, { bubbles: true, composed: true, cancelable: true, clientX, pointerId: 1, pointerType: 'mouse', button: 0, ...init })));
  /** Drags from `fromX` (on `target`) to `toX` and lets go. */
  const drag = (target: Element, fromX: number, toX: number, init: PointerEventInit = {}) => {
    pointer('pointerdown', target, fromX, init);
    pointer('pointermove', trim(), toX, init);
    pointer('pointerup', trim(), toX, init);
  };
  const key = (el: Element, k: string, init: KeyboardEventInit = {}) => {
    const e = new KeyboardEvent('keydown', { key: k, bubbles: true, composed: true, cancelable: true, ...init });
    el.dispatchEvent(e);
    el.dispatchEvent(new KeyboardEvent('keyup', { key: k, bubbles: true, composed: true }));
    return e;
  };
  /** Types a time into a chip and commits it with Enter. */
  const typeTime = (which: 'from' | 'to', text: string) => {
    click(chip(which));
    field(which).value = text;
    key(field(which), 'Enter');
  };
  const open = async () => {
    click(overlay.button);
    await vi.advanceTimersByTimeAsync(0);
    Object.defineProperty(q('.dx-trim-box'), 'getBoundingClientRect', {
      configurable: true,
      value: () => ({ ...BOX, top: 0, right: BOX.left + BOX.width, bottom: 40, height: 40, x: BOX.left, y: 0 }),
    });
  };
  const openPart = async () => {
    await open();
    click(toggle());
  };
  const sizes = () => rows().map((r) => r.querySelector('.dx-s')!.textContent);
  return { overlay, shadow, player, adds, video, click, q, part, body, toggle, summary, trim, handle, chip, field, error, rows, button, pointer, drag, key, typeTime, open, openPart, sizes };
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'] });
});
afterEach(() => {
  vi.useRealTimers();
});

describe('VideoOverlay Part row', () => {
  it('is hidden without the clips feature, without its question, and without a known duration', async () => {
    for (const clips of [false, undefined]) {
      const s = setup({ clips });
      await s.open();
      expect(s.rows()).toHaveLength(2);
      expect(s.part().hidden).toBe(true);
      // The whole video goes, as before.
      s.click(s.rows()[1]!);
      await vi.advanceTimersByTimeAsync(0);
      expect(s.adds).toEqual([[{ video: '136', audio: '140' }, undefined]]);
    }
    const live = setup({ clips: true, info: { ...INFO, durationMs: 0 } });
    await live.open();
    expect(live.rows()).toHaveLength(2);
    expect(live.part().hidden).toBe(true);
  });

  it('sits under the qualities and the Audio select, closed with "Whole video"', async () => {
    const s = setup({ clips: true });
    await s.open();
    expect(s.part().hidden).toBe(false);
    expect(s.part().getAttribute('role')).toBe('group');
    expect(s.part().getAttribute('aria-label')).toBe(CLIP_TEXTS.row);
    const order = [...s.overlay.panelEl.children].map((c) => c.className.split(' ')[0]);
    expect(order.indexOf('dx-part')).toBe(order.indexOf('dx-audio') + 1);
    expect(order.indexOf('dx-part')).toBeLessThan(order.indexOf('dx-status'));
    expect(s.toggle().getAttribute('aria-expanded')).toBe('false');
    expect(s.toggle().getAttribute('aria-controls')).toBe(s.body().id);
    expect(s.toggle().textContent).toBe('Part of the videoWhole video');
    // Closed: the body is collapsed and out of reach (inert), its controls never focused.
    expect(s.body().dataset.open).toBe('false');
    expect(s.body().inert).toBe(true);
    expect(s.sizes()).toEqual(['754 MB', '377 MB']);
    s.click(s.rows()[0]!);
    await vi.advanceTimersByTimeAsync(0);
    expect(s.adds).toEqual([[{ video: '299', audio: '140' }, undefined]]);
  });

  it('opens on a timeline of the whole video, with its readout', async () => {
    const s = setup({ clips: true });
    await s.openPart();
    expect(s.toggle().getAttribute('aria-expanded')).toBe('true');
    expect(s.body().dataset.open).toBe('true');
    expect(s.body().inert).toBe(false);
    expect(s.chip('from').textContent).toBe('0:00');
    expect(s.chip('to').textContent).toBe('12:34');
    expect(s.q('.dx-trim-detail').textContent).toBe('Whole video · 754 MB at 1080p');
    expect(s.trim().style.getPropertyValue('--s')).toBe('0');
    expect(s.trim().style.getPropertyValue('--e')).toBe('1');
    // The track reads left to right on a right-to-left page too.
    expect(s.trim().dir).toBe('ltr');
    expect(s.q<HTMLElement>('.dx-trim-whole').hidden).toBe(true);
    // The handles are sliders in seconds, each bounded by the other.
    const [from, to] = [s.handle('from'), s.handle('to')];
    expect(from.getAttribute('role')).toBe('slider');
    expect(from.getAttribute('aria-label')).toBe(CLIP_TEXTS.start);
    expect([from.getAttribute('aria-valuemin'), from.getAttribute('aria-valuenow'), from.getAttribute('aria-valuemax')]).toEqual(['0', '0', '753']);
    expect(from.getAttribute('aria-valuetext')).toBe('Start 0:00');
    expect([to.getAttribute('aria-valuemin'), to.getAttribute('aria-valuenow'), to.getAttribute('aria-valuemax')]).toEqual(['1', '754', '754']);
    expect(to.getAttribute('aria-valuetext')).toBe('End 12:34');
    expect(from.tabIndex).toBe(0);
    // Closing it again sends the whole video.
    s.click(s.toggle());
    expect(s.body().dataset.open).toBe('false');
  });

  it('drags the handles with the pointer: a part, its estimates, and the part sent', async () => {
    const s = setup({ clips: true });
    await s.openPart();
    // The end handle, grabbed and moved to 2:45: it follows the pointer (captured) while held.
    s.pointer('pointerdown', s.handle('to'), x(754_000));
    expect(s.trim().dataset.drag).toBe('to');
    s.pointer('pointermove', s.trim(), x(165_400));
    expect(s.chip('to').textContent).toBe('2:45');
    expect(s.q('.dx-trim-tip').textContent).toBe('2:45');
    expect(s.q<HTMLElement>('.dx-trim-tip').dataset.show).toBe('true');
    s.pointer('pointerup', s.trim(), x(165_400));
    expect(s.trim().dataset.drag).toBeUndefined();
    expect(s.q<HTMLElement>('.dx-trim-tip').dataset.show).toBe('false');
    // A press on the track takes the nearer handle there (the start, at 1:20).
    s.drag(s.trim(), x(80_000), x(80_000));
    expect(s.summary()).toBe('Part 1:20 – 2:45');
    expect(s.toggle().dataset.part).toBe('true');
    expect(s.q('.dx-trim-detail').textContent).toBe('1:25 selected · ~90.0 MB at 1080p');
    expect(s.trim().style.getPropertyValue('--s')).toBe(String(80 / 754));
    expect(s.handle('from').getAttribute('aria-valuetext')).toBe('Start 1:20');
    // (85 s + 5 s) of 754 s, always "~".
    expect(s.sizes()).toEqual(['~90.0 MB', '~45.0 MB']);
    s.click(s.rows()[1]!);
    await vi.advanceTimersByTimeAsync(0);
    expect(s.adds).toEqual([[{ video: '136', audio: '140' }, { startMs: 80_000, endMs: 165_000 }]]);
  });

  it('never lets the handles cross: they keep one second between them', async () => {
    const s = setup({ clips: true });
    await s.openPart();
    s.drag(s.handle('to'), x(754_000), x(165_000));
    // The start dragged past the end stops a second short of it …
    s.drag(s.handle('from'), x(0), x(400_000));
    expect([s.chip('from').textContent, s.chip('to').textContent]).toEqual(['2:44', '2:45']);
    // … and the end dragged before the start stops a second after it.
    s.drag(s.handle('to'), x(165_000), x(10_000));
    expect([s.chip('from').textContent, s.chip('to').textContent]).toEqual(['2:44', '2:45']);
    s.key(s.handle('to'), 'Home');
    s.key(s.handle('from'), 'End');
    expect([s.chip('from').textContent, s.chip('to').textContent]).toEqual(['2:44', '2:45']);
    expect(s.error().hidden).toBe(true);
  });

  it('moves a focused handle with the keys, which stay in the slider and never reach the player', async () => {
    const s = setup({ clips: true });
    const seen: string[] = [];
    document.addEventListener('keydown', (e) => seen.push(e.key));
    await s.openPart();
    const from = s.handle('from');
    from.focus();
    expect(s.shadow.activeElement).toBe(from);
    const steps: [string, KeyboardEventInit, string][] = [
      ['ArrowRight', {}, '0:01'],
      ['ArrowRight', { shiftKey: true }, '0:11'],
      ['PageUp', {}, '1:11'],
      ['ArrowLeft', {}, '1:10'],
      ['ArrowUp', {}, '1:11'],
      ['ArrowDown', { shiftKey: true }, '1:01'],
      ['PageDown', {}, '0:01'],
      ['Home', {}, '0:00'],
    ];
    for (const [k, init, want] of steps) {
      const e = s.key(from, k, init);
      expect(e.defaultPrevented).toBe(true);
      expect(s.chip('from').textContent).toBe(want);
      expect(from.getAttribute('aria-valuetext')).toBe(`Start ${want}`);
      // Up and Down stay with the slider: the focus does not move to the quality rows.
      expect(s.shadow.activeElement).toBe(from);
    }
    const to = s.handle('to');
    to.focus();
    s.key(to, 'Home');
    expect(s.chip('to').textContent).toBe('0:01');
    s.key(to, 'End');
    expect(s.chip('to').textContent).toBe('12:34');
    expect(seen).toEqual([]);
  });

  it('chooses a chapter from its chip or a double-click on the track', async () => {
    const s = setup({ clips: true });
    await s.openPart();
    const chips = [...s.overlay.panelEl.querySelectorAll<HTMLButtonElement>('.dx-trim-chap')];
    expect(chips.map((c) => c.getAttribute('aria-label'))).toEqual(['1. Intro (0:00–1:20)', '2. The talk (1:20–12:34)']);
    expect(s.q('.dx-trim-chaps').getAttribute('aria-label')).toBe(CLIP_TEXTS.chapters);
    // The chapters are segments of the track (their edges the gaps).
    expect(s.overlay.panelEl.querySelectorAll('.dx-trim-seg')).toHaveLength(2);
    s.click(chips[0]!);
    expect(s.summary()).toBe('Part 0:00 – 1:20');
    expect(chips.map((c) => c.getAttribute('aria-pressed'))).toEqual(['true', 'false']);
    // A handle moved: no chapter any more.
    s.key(s.handle('to'), 'ArrowRight');
    expect(chips.map((c) => c.getAttribute('aria-pressed'))).toEqual(['false', 'false']);
    // A double-click on the second chapter's segment takes it: to the end, so no end is sent.
    s.trim().dispatchEvent(new MouseEvent('dblclick', { bubbles: true, clientX: x(300_000) }));
    expect(s.summary()).toBe('Part 0:00 – 1:21'); // not the user's: nothing
    const dbl = new MouseEvent('dblclick', { bubbles: true, clientX: x(300_000) });
    trusted.add(dbl);
    s.trim().dispatchEvent(dbl);
    expect(s.summary()).toBe('Part 1:20 – 12:34');
    expect(chips[1]!.getAttribute('aria-pressed')).toBe('true');
    // The time under the mouse, with its chapter, while it hovers the track.
    s.pointer('pointermove', s.trim(), x(30_000));
    expect(s.q('.dx-trim-tip').textContent).toBe('Intro · 0:30');
    expect(s.q<HTMLElement>('.dx-trim-tip').dataset.kind).toBe('hover');
    s.click(s.rows()[0]!);
    await vi.advanceTimersByTimeAsync(0);
    expect(s.adds).toEqual([[{ video: '299', audio: '140' }, { startMs: 80_000, endMs: 0 }]]);
  });

  it('has no chapter chips for a video without chapters', async () => {
    const { chapters: _, ...plain } = INFO;
    const s = setup({ clips: true, info: plain });
    await s.openPart();
    expect(s.q<HTMLElement>('.dx-trim-chaps').hidden).toBe(true);
    expect(s.overlay.panelEl.querySelectorAll('.dx-trim-seg')).toHaveLength(0);
  });

  it('shows the dragged frame in the player and puts it back, playing as it was, after the drag', async () => {
    const s = setup({ clips: true });
    s.video.paused = false; // playing at 1:23.4
    await s.openPart();
    s.pointer('pointerdown', s.handle('from'), x(0));
    // Held still while scrubbing, the frame of each position shown.
    expect(s.video.paused).toBe(true);
    s.pointer('pointermove', s.trim(), x(80_000));
    expect(s.video.ms).toBe(80_000);
    s.pointer('pointermove', s.trim(), x(95_000));
    expect(s.video.ms).toBe(95_000);
    s.pointer('pointerup', s.trim(), x(95_000));
    // The frame stays a moment, then the video goes back where it was, playing.
    await vi.advanceTimersByTimeAsync(SCRUB_LINGER_MS - 1);
    expect(s.video.ms).toBe(95_000);
    expect(s.video.paused).toBe(true);
    await vi.advanceTimersByTimeAsync(1);
    expect(s.video.ms).toBe(83_400);
    expect(s.video.paused).toBe(false);
    // Keys scrub too, and closing the panel puts the video back at once.
    s.key(s.handle('to'), 'PageDown');
    expect(s.video.ms).toBe(694_000);
    expect(s.video.paused).toBe(true);
    s.overlay.close(false);
    expect(s.video.ms).toBe(83_400);
    expect(s.video.paused).toBe(false);
  });

  it('waits for a seek to finish before the next one, the latest winning', async () => {
    const s = setup({ clips: true });
    await s.openPart();
    s.pointer('pointerdown', s.handle('from'), x(0));
    s.pointer('pointermove', s.trim(), x(10_000));
    expect(s.video.ms).toBe(10_000);
    s.video.seeking = true;
    s.pointer('pointermove', s.trim(), x(20_000));
    s.pointer('pointermove', s.trim(), x(30_000));
    expect(s.video.ms).toBe(10_000);
    s.video.seeking = false;
    s.video.dispatchEvent(new Event('seeked'));
    expect(s.video.ms).toBe(30_000);
    expect(s.video.seeks).toEqual([0, 10_000, 30_000]);
  });

  it('leaves the player where the user put it when they play or seek it meanwhile', async () => {
    const s = setup({ clips: true });
    await s.openPart();
    s.drag(s.handle('from'), x(0), x(80_000));
    // The user starts the video during the moment the frame stays: they keep it.
    void s.video.play();
    await vi.advanceTimersByTimeAsync(SCRUB_LINGER_MS);
    expect(s.video.ms).toBe(80_000);
    expect(s.video.paused).toBe(false);
    // A seek of their own during a drag ends the hold too.
    s.pointer('pointerdown', s.handle('from'), x(80_000));
    s.pointer('pointermove', s.trim(), x(90_000));
    s.video.currentTime = 400;
    s.video.dispatchEvent(new Event('seeking'));
    s.pointer('pointerup', s.trim(), x(90_000));
    s.overlay.close(false);
    await vi.advanceTimersByTimeAsync(SCRUB_LINGER_MS);
    expect(s.video.ms).toBe(400_000);
  });

  it('previews the part in the player, stops at its end and puts the player back', async () => {
    const s = setup({ clips: true });
    await s.openPart();
    s.typeTime('from', '1:20');
    s.typeTime('to', '1:30');
    const preview = s.button('.dx-trim-preview');
    expect(preview.hidden).toBe(false);
    expect(preview.textContent).toBe(CLIP_TEXTS.preview);
    s.click(preview);
    expect(s.video.ms).toBe(80_000);
    expect(s.video.paused).toBe(false);
    expect(preview.getAttribute('aria-pressed')).toBe('true');
    expect(preview.textContent).toBe(CLIP_TEXTS.stopPreview);
    // Playing on: still short of the end …
    s.video.currentTime = 85;
    await vi.advanceTimersByTimeAsync(PREVIEW_TICK_MS);
    expect(s.video.paused).toBe(false);
    // … then at it: paused there, the button back to Preview, and soon the player back as it was.
    s.video.currentTime = 89.99;
    await vi.advanceTimersByTimeAsync(PREVIEW_TICK_MS);
    expect(s.video.paused).toBe(true);
    expect(preview.getAttribute('aria-pressed')).toBe('false');
    await vi.advanceTimersByTimeAsync(SCRUB_LINGER_MS);
    expect(s.video.ms).toBe(83_400);
    expect(s.video.paused).toBe(true);
    // Stop ends a preview at once.
    s.click(preview);
    expect(s.video.paused).toBe(false);
    s.click(preview);
    expect(s.video.paused).toBe(true);
    expect(s.video.ms).toBe(83_400);
    // A pause of the user's during a preview leaves the player to them.
    s.click(preview);
    s.video.currentTime = 82;
    s.video.pause();
    expect(preview.getAttribute('aria-pressed')).toBe('false');
    await vi.advanceTimersByTimeAsync(SCRUB_LINGER_MS);
    expect(s.video.ms).toBe(82_000);
  });

  it('sets the start and the end from the player’s position, and follows it with the playhead', async () => {
    const s = setup({ clips: true });
    await s.openPart();
    const head = s.q<HTMLElement>('.dx-trim-head');
    expect(head.hidden).toBe(false);
    expect(Number(s.trim().style.getPropertyValue('--p'))).toBeCloseTo(83.4 / 754);
    s.video.currentTime = 377;
    s.video.dispatchEvent(new Event('timeupdate'));
    expect(s.trim().style.getPropertyValue('--p')).toBe('0.5');
    s.click(s.button(".dx-trim-btn[data-here='to']"));
    expect(s.chip('to').textContent).toBe('6:17');
    s.video.currentTime = 83.4;
    s.click(s.button(".dx-trim-btn[data-here='from']"));
    expect(s.chip('from').textContent).toBe('1:23.4');
    // Start here after the end: the end gives way (to the end of the video).
    s.video.currentTime = 500;
    s.click(s.button(".dx-trim-btn[data-here='from']"));
    expect([s.chip('from').textContent, s.chip('to').textContent]).toEqual(['8:20', '12:34']);
    // A synthetic click of the page does nothing.
    s.video.currentTime = 10;
    s.button(".dx-trim-btn[data-here='from']").dispatchEvent(new MouseEvent('click', { bubbles: true }));
    expect(s.chip('from').textContent).toBe('8:20');
    s.click(s.rows()[0]!);
    await vi.advanceTimersByTimeAsync(0);
    expect(s.adds.at(-1)?.[1]).toEqual({ startMs: 500_000, endMs: 0 });
  });

  it('has no playhead, preview or Start/End here without the page’s video', async () => {
    const s = setup({ clips: true, noVideo: true });
    await s.openPart();
    expect(s.q<HTMLElement>('.dx-trim-head').hidden).toBe(true);
    for (const b of s.overlay.panelEl.querySelectorAll<HTMLButtonElement>('.dx-trim-btn')) expect(b.hidden).toBe(true);
    // The timeline itself still works.
    s.drag(s.handle('to'), x(754_000), x(165_000));
    expect(s.summary()).toBe('Part 0:00 – 2:45');
  });

  it('types a time into a chip, with the desktop’s rules after the commit', async () => {
    const s = setup({ clips: true });
    await s.openPart();
    // A click turns the chip into its field, the time selected.
    s.click(s.chip('from'));
    expect(s.chip('from').hidden).toBe(true);
    expect(s.field('from').hidden).toBe(false);
    expect(s.shadow.activeElement).toBe(s.field('from'));
    expect(s.field('from').value).toBe('0:00');
    expect(s.field('from').getAttribute('aria-label')).toBe(CLIP_TEXTS.from);
    expect(s.field('from').getAttribute('aria-description')).toBe(CLIP_TEXTS.timeHint);
    // Nothing shows while typing; Enter commits.
    s.field('from').value = '1:2';
    s.field('from').dispatchEvent(new Event('input', { bubbles: true }));
    expect(s.error().hidden).toBe(true);
    s.key(s.field('from'), 'Enter');
    expect(s.error().getAttribute('role')).toBe('alert');
    expect(s.error().textContent).toBe(CLIP_TEXTS.badTime);
    expect(s.chip('from').textContent).toBe('1:2');
    expect(s.chip('from').dataset.invalid).toBe('true');
    expect(s.chip('from').getAttribute('aria-describedby')).toBe(s.error().id);
    expect(s.shadow.activeElement).toBe(s.chip('from'));
    // Blocked: nothing is sent, and the chip the error is about opens for a fix.
    s.click(s.rows()[0]!);
    await vi.advanceTimersByTimeAsync(0);
    expect(s.adds).toEqual([]);
    expect(s.shadow.activeElement).toBe(s.field('from'));
    s.field('from').value = '1:20';
    s.key(s.field('from'), 'Enter');
    expect(s.error().hidden).toBe(true);
    expect(s.summary()).toBe('Part 1:20 – 12:34');
    // The rules: before From, too short, past the end; the handles stay at the last good times.
    for (const [text, want] of [
      ['1:00', CLIP_TEXTS.order],
      ['1:20.5', CLIP_TEXTS.short],
      ['13:00', pastEndText(754_000)],
    ] as const) {
      s.typeTime('to', text);
      expect(s.error().textContent).toBe(want);
      expect(s.chip('to').textContent).toBe(text);
      expect(s.trim().style.getPropertyValue('--e')).toBe('1');
    }
    // Within a second past the end is the end; an empty To is the end too.
    s.typeTime('to', '12:35');
    expect(s.error().hidden).toBe(true);
    expect(s.chip('to').textContent).toBe('12:34');
    s.typeTime('to', '2:45');
    s.typeTime('to', '');
    expect(s.summary()).toBe('Part 1:20 – 12:34');
    // Esc leaves the field unchanged and keeps the panel open; leaving the field commits it.
    s.click(s.chip('from'));
    s.field('from').value = '9:99';
    s.key(s.field('from'), 'Escape');
    expect(s.overlay.isOpen).toBe(true);
    expect(s.chip('from').textContent).toBe('1:20');
    s.click(s.chip('from'));
    s.field('from').value = '2:00';
    s.field('from').dispatchEvent(new Event('blur'));
    expect(s.chip('from').textContent).toBe('2:00');
    // "Whole video" resets the part.
    const whole = s.q<HTMLButtonElement>('.dx-trim-whole');
    expect(whole.hidden).toBe(false);
    s.click(whole);
    expect(s.summary()).toBe('Whole video');
    expect(whole.hidden).toBe(true);
  });

  it('disables everything while sending and after the part was sent', async () => {
    let settle: (o: Outcome<AddedResult>) => void = () => undefined;
    const s = setup({
      clips: true,
      extra: {
        add: () =>
          new Promise<Outcome<AddedResult>>((resolve) => {
            settle = resolve;
          }),
      },
    });
    await s.openPart();
    s.drag(s.handle('from'), x(0), x(80_000));
    const summary = s.summary();
    const size = () => s.rows()[0]!.querySelector('.dx-s')!.textContent;
    const partSize = size();
    expect(partSize).toMatch(/^~/);
    const check = () => {
      expect(s.toggle().disabled).toBe(true);
      expect(s.part().dataset.busy).toBe('true');
      for (const which of ['from', 'to'] as const) {
        expect(s.chip(which).disabled).toBe(true);
        expect(s.handle(which).getAttribute('aria-disabled')).toBe('true');
        expect(s.handle(which).tabIndex).toBe(-1);
      }
      for (const b of s.overlay.panelEl.querySelectorAll<HTMLButtonElement>('.dx-trim-btn, .dx-trim-chap, .dx-trim-whole')) expect(b.disabled).toBe(true);
      // Nothing the user does changes the part that is going.
      s.click(s.toggle());
      s.drag(s.handle('to'), x(754_000), x(300_000));
      s.key(s.handle('from'), 'Home');
      s.click(s.chip('from'));
      expect(s.field('from').hidden).toBe(true);
      expect(s.toggle().getAttribute('aria-expanded')).toBe('true');
      expect(s.summary()).toBe(summary);
      expect(size()).toBe(partSize);
    };
    s.click(s.rows()[0]!);
    check();
    settle({ ok: true, value: { type: 'added', downloadId: 4, awaiting: false } });
    await vi.advanceTimersByTimeAsync(0);
    check(); // sent: the panel closes soon
  });

  it('follows the clips answer when the panel opens again, and forgets the part for another video', async () => {
    let clips = true;
    const s = setup({ extra: { clips: async () => clips } });
    await s.openPart();
    s.typeTime('from', '1:20');
    s.overlay.close(false);
    clips = false;
    await s.open();
    expect(s.part().hidden).toBe(true);
    s.click(s.rows()[0]!);
    await vi.advanceTimersByTimeAsync(0);
    expect(s.adds.at(-1)?.[1]).toBeUndefined();
    clips = true;
    s.overlay.setVideo('youtube:dQw4w9WgXcQ');
    await s.open();
    expect(s.part().hidden).toBe(false);
    expect(s.toggle().getAttribute('aria-expanded')).toBe('false');
    expect(s.chip('from').textContent).toBe('0:00');
  });
});

describe('VideoOverlay Part row and the page’s video', () => {
  it('puts the player back after previewing a part that runs to the end (pause, then ended)', async () => {
    const s = setup({ clips: true });
    s.video.paused = false; // playing at 1:23.4
    await s.openPart();
    s.typeTime('from', '12:00'); // to the end (12:34)
    s.click(s.button('.dx-trim-preview'));
    expect([s.video.ms, s.video.paused]).toEqual([720_000, false]);
    await vi.advanceTimersByTimeAsync(PREVIEW_TICK_MS);
    // The element reaches its end between two checks (its own length a little short of the
    // video's): it fires pause, then ended (HTML), which is not the user taking over.
    s.video.currentTime = 753.6;
    s.video.ended = true;
    s.video.paused = true;
    s.video.dispatchEvent(new Event('pause'));
    s.video.dispatchEvent(new Event('ended'));
    expect(s.button('.dx-trim-preview').getAttribute('aria-pressed')).toBe('false');
    await vi.advanceTimersByTimeAsync(SCRUB_LINGER_MS + 100);
    expect([s.video.ms, s.video.paused]).toEqual([83_400, false]);
  });

  it('lets go of the player without touching it when another video loads into it', async () => {
    // Mid-drag: YouTube (a single-page app) loads the next video into the same element.
    const s = setup({ clips: true });
    s.video.paused = false;
    await s.openPart();
    s.pointer('pointerdown', s.handle('from'), x(0));
    s.pointer('pointermove', s.trim(), x(80_000));
    s.video.dispatchEvent(new Event('emptied'));
    s.video.currentTime = 0;
    s.video.dispatchEvent(new Event('loadstart'));
    s.video.seeks.length = 0;
    s.pointer('pointerup', s.trim(), x(80_000));
    s.overlay.setVideo('youtube:dQw4w9WgXcQ');
    await vi.advanceTimersByTimeAsync(SCRUB_LINGER_MS);
    expect(s.video.seeks).toEqual([]);

    // During the moment after a drag, with only the new address seen (no media event yet).
    const t = setup({ clips: true });
    await t.openPart();
    t.drag(t.handle('from'), x(0), x(80_000));
    t.video.seeks.length = 0;
    t.overlay.setVideo('youtube:dQw4w9WgXcQ');
    await vi.advanceTimersByTimeAsync(SCRUB_LINGER_MS);
    expect(t.video.seeks).toEqual([]);

    // During a preview: the new video's autoplay is left alone.
    const u = setup({ clips: true });
    await u.openPart();
    u.typeTime('to', '1:30');
    u.click(u.button('.dx-trim-preview'));
    u.video.dispatchEvent(new Event('emptied'));
    expect(u.button('.dx-trim-preview').getAttribute('aria-pressed')).toBe('false');
    u.video.currentTime = 0;
    u.video.seeks.length = 0;
    await vi.advanceTimersByTimeAsync(SCRUB_LINGER_MS + PREVIEW_TICK_MS * 3);
    expect([u.video.seeks, u.video.paused]).toEqual([[], false]);
  });

  it('leaves the element alone while it plays something else than the video (an ad)', async () => {
    const s = setup({ clips: true });
    // 0:07 into a 15 s ad, in the same element as the video.
    s.video.duration = 15;
    s.video.currentTime = 7;
    s.video.seeks.length = 0;
    await s.openPart();
    expect(s.q<HTMLElement>('.dx-trim-head').hidden).toBe(true);
    for (const b of s.overlay.panelEl.querySelectorAll<HTMLButtonElement>('.dx-trim-btn')) expect(b.hidden).toBe(true);
    // The timeline works without it: a drag neither pauses nor seeks the ad.
    s.drag(s.handle('from'), x(0), x(80_000));
    expect(s.summary()).toBe('Part 1:20 – 12:34');
    expect([s.video.seeks, s.video.paused]).toEqual([[], true]);
    // The video itself is back (its length within a second or two of the video's): all of it shows.
    s.video.duration = 754.04;
    s.video.currentTime = 83.4;
    s.video.dispatchEvent(new Event('durationchange'));
    expect(s.q<HTMLElement>('.dx-trim-head').hidden).toBe(false);
    for (const b of s.overlay.panelEl.querySelectorAll<HTMLButtonElement>('.dx-trim-btn')) expect(b.hidden).toBe(false);
    // An ad that starts during a hold: the hold is given up, the ad never sought.
    s.pointer('pointerdown', s.handle('from'), x(80_000));
    s.pointer('pointermove', s.trim(), x(90_000));
    s.video.dispatchEvent(new Event('emptied'));
    s.video.duration = 15;
    s.video.currentTime = 0;
    s.video.seeks.length = 0;
    s.pointer('pointerup', s.trim(), x(90_000));
    s.overlay.close(false);
    await vi.advanceTimersByTimeAsync(SCRUB_LINGER_MS);
    expect(s.video.seeks).toEqual([]);
  });

  it('leaves arrow keys with Alt, Ctrl or Meta to the browser', async () => {
    const s = setup({ clips: true });
    await s.openPart();
    s.drag(s.handle('from'), x(0), x(80_000));
    for (const init of [{ altKey: true }, { ctrlKey: true }, { metaKey: true }]) {
      for (const k of ['ArrowLeft', 'ArrowRight', 'Home', 'PageUp']) {
        const e = s.key(s.handle('from'), k, init);
        expect(e.defaultPrevented).toBe(false);
        expect(s.chip('from').textContent).toBe('1:20');
      }
    }
    // Shift stays the 10 s step.
    expect(s.key(s.handle('from'), 'ArrowLeft', { shiftKey: true }).defaultPrevented).toBe(true);
    expect(s.chip('from').textContent).toBe('1:10');
  });

  it('shows the hovered time on the handles’ grid of whole seconds', async () => {
    const s = setup({ clips: true });
    await s.openPart();
    s.pointer('pointermove', s.trim(), x(467_400));
    expect(s.q('.dx-trim-tip').textContent).toBe('The talk · 7:47');
    s.pointer('pointermove', s.trim(), x(30_600));
    expect(s.q('.dx-trim-tip').textContent).toBe('Intro · 0:31');
  });

  it('shows an error right under the times, in the trimmer’s words, the part dimmed meanwhile', async () => {
    const s = setup({ clips: true });
    await s.openPart();
    s.typeTime('from', '1:20');
    s.typeTime('to', '2:45');
    s.typeTime('from', '5:00');
    expect(s.error().textContent).toBe('The start must be before the end.');
    expect(CLIP_TEXTS.order).toBe('The start must be before the end.');
    // In the readout, in place of the length (the part that cannot go has none).
    expect(s.error().parentElement).toBe(s.q('.dx-trim-read'));
    expect(s.q<HTMLElement>('.dx-trim-detail').hidden).toBe(true);
    expect(s.part().dataset.error).toBe('true');
    s.typeTime('to', '13:00');
    expect(s.error().textContent).toBe('The video ends at 12:34.');
    s.typeTime('to', '2:45');
    s.typeTime('from', '1:20');
    expect(s.error().hidden).toBe(true);
    expect(s.q<HTMLElement>('.dx-trim-detail').hidden).toBe(false);
    expect(s.part().dataset.error).toBe('false');
  });

  it('draws its timeline, chapters and buttons left to right on a right-to-left page', async () => {
    const s = setup({ clips: true });
    await s.openPart();
    expect(s.body().dir).toBe('ltr');
    // A chapter's title keeps its own direction inside its chip.
    for (const t of s.overlay.panelEl.querySelectorAll<HTMLElement>('.dx-trim-chap > span')) expect(t.dir).toBe('auto');
  });
});

describe('playerVideo', () => {
  it('finds the player’s main video, else its first one', () => {
    const player = document.createElement('div');
    expect(playerVideo(null)).toBeUndefined();
    expect(playerVideo(player)).toBeUndefined();
    const ad = document.createElement('video');
    player.append(ad);
    expect(playerVideo(player)).toBe(ad);
    const main = document.createElement('video');
    main.className = 'video-stream html5-main-video';
    player.append(main);
    expect(playerVideo(player)).toBe(main);
  });
});
