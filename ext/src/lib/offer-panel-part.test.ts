// @vitest-environment happy-dom

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { ClipRange } from './clip';
import { CLIP_TEXTS, pastEndText } from './clip';
import type { FrameOffers, SiteOffer, StreamOffer } from './offer-types';
import { OfferPanel, type OfferPanelDeps, OfferPanelDom, type OfferPanelDomDeps, type OfferPanelView, type PanelTarget } from './offer-panel';
import type { PartView } from './part-row';
import type { VideoInfoResult, VideoTrack } from './protocol';
import type { TrackPick } from './video-picker';

// The in-page panel's Part row, the timeline trimmer (MEDIA-SPEC §8, VIDEO-DETECT-SPEC §5.2).

function track(t: Partial<VideoTrack> & Pick<VideoTrack, 'id' | 'kind'>): VideoTrack {
  return { ext: 'mp4', codecs: '', width: 0, height: 0, frameRate: 0, bandwidth: 0, size: 0, sizeApprox: false, language: '', name: '', ...t };
}

const STREAM_INFO: VideoInfoResult = {
  type: 'videoInfo', site: '', title: '', durationMs: 754_000, format: 'hls', container: 'mp4', selectedVideo: '', selectedAudio: '',
  tracks: [track({ id: 'v720', kind: 'muxed', height: 720, bandwidth: 2_000_000, name: '720p (HD)' })],
};
const SITE_INFO: VideoInfoResult = {
  type: 'videoInfo', site: 'Vimeo', title: 'T', durationMs: 754_000, format: 'http', container: 'mp4', selectedVideo: '1080', selectedAudio: 'a',
  tracks: [track({ id: '1080', kind: 'video', height: 1080, size: 300_000_000, name: '1080p' }), track({ id: 'a', kind: 'audio', size: 5_000_000 })],
  chapters: [{ startMs: 0, endMs: 80_000, title: 'Intro' }, { startMs: 80_000, endMs: 754_000, title: 'Rest' }],
};

const stream = (key: string): StreamOffer => ({ key, format: 'hls', display: `cdn/${key}.m3u8`, frame: 'this', matched: true });
const site: SiteOffer = { key: 'vimeo:1', site: 'Vimeo', kind: 'watch', pageUrl: 'https://vimeo.com/1', curated: true };

function offers(clips: boolean, over: Partial<FrameOffers> = {}): FrameOffers {
  return { title: 'Page', topHost: 'example.com', files: [], streams: [], features: { streams: true, video: true, clips }, drm: false, live: false, ...over };
}

const TARGET: PanelTarget = { src: '', mse: true, drm: false, live: false };

function setup(o: FrameOffers, over: Partial<OfferPanelDeps> = {}) {
  const views: OfferPanelView[] = [];
  const added: [string, TrackPick, ClipRange | undefined][] = [];
  const siteAdds: [TrackPick, ClipRange | undefined][] = [];
  const panel = new OfferPanel({
    offers: async () => ({ ok: true, value: o }),
    streamProbe: async () => ({ ok: true, value: STREAM_INFO }),
    offerAdd: async (key, pick, clip) => {
      added.push([key, pick, clip]);
      return { ok: true, value: { type: 'added', downloadId: 7, awaiting: false } };
    },
    siteProbe: async () => ({ ok: true, value: SITE_INFO }),
    siteAdd: async (_s, pick, clip) => {
      siteAdds.push([pick, clip]);
      return { ok: true, value: { type: 'added', downloadId: 9, awaiting: false } };
    },
    hideSite: async () => ({ hidden: true }),
    render: (v) => views.push(v),
    ...over,
  });
  return { panel, views, added, siteAdds, last: () => views[views.length - 1]! };
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'] });
});
afterEach(() => {
  vi.useRealTimers();
});

describe('OfferPanel Part row', () => {
  it('is shown only when the relay lists clips, and sends a stream’s part', async () => {
    const without = setup(offers(false, { streams: [stream('s1')] }));
    without.panel.open(TARGET);
    await vi.advanceTimersByTimeAsync(0);
    expect(without.last().part).toBeUndefined();

    const s = setup(offers(true, { streams: [stream('s1')] }));
    s.panel.open(TARGET);
    await vi.advanceTimersByTimeAsync(0);
    expect(s.last().part).toMatchObject({ expanded: false, summary: 'Whole video', durationMs: 754_000, startMs: 0, endMs: 754_000, whole: true });
    // Closed, the whole video goes.
    await s.panel.download();
    expect(s.added.at(-1)?.[2]).toBeUndefined();

    s.panel.togglePart();
    s.panel.setPartMs('from', 83_400);
    s.panel.setPartMs('to', 165_000);
    // The readout's size is the default row's, for the part: 188.5 MB (bandwidth × duration) × 87 s ÷ 754 s.
    expect(s.last().part).toMatchObject({ expanded: true, from: '1:23.4', to: '2:45', summary: 'Part 1:23.4 – 2:45', length: '1:21.6', size: '~21.8 MB at 720p' });
    await s.panel.download();
    expect(s.added.at(-1)).toEqual(['s1', { video: 'v720', audio: '' }, { startMs: 83_400, endMs: 165_000 }]);
  });

  it('keeps the handles from crossing, unless Start here or End here pushes the other one away', async () => {
    const s = setup(offers(true, { streams: [stream('s1')] }));
    s.panel.open(TARGET);
    await vi.advanceTimersByTimeAsync(0);
    s.panel.togglePart();
    s.panel.setPartMs('to', 165_000);
    s.panel.setPartMs('from', 400_000);
    expect(s.last().part).toMatchObject({ startMs: 164_000, endMs: 165_000 });
    s.panel.setPartMs('to', 0);
    expect(s.last().part).toMatchObject({ startMs: 164_000, endMs: 165_000 });
    s.panel.setPartMs('from', 400_000, true);
    expect(s.last().part).toMatchObject({ startMs: 400_000, endMs: 754_000, summary: 'Part 6:40 – 12:34' });
    s.panel.setPartMs('to', 30_000, true);
    expect(s.last().part).toMatchObject({ startMs: 0, endMs: 30_000 });
    expect(s.last().part?.error).toBeUndefined();
  });

  it('shows a typed time’s error once committed and sends nothing while it shows', async () => {
    const s = setup(offers(true, { streams: [stream('s1')] }));
    s.panel.open(TARGET);
    await vi.advanceTimersByTimeAsync(0);
    s.panel.togglePart();
    s.panel.editPart('from', '2:45');
    s.panel.editPart('to', '1:20');
    expect(s.last().part).toMatchObject({ error: CLIP_TEXTS.order, errorField: 'to', to: '1:20', endMs: 754_000, size: '' });
    s.panel.editPart('to', '1:2');
    expect(s.last().part).toMatchObject({ error: CLIP_TEXTS.badTime, errorField: 'to' });
    await s.panel.download();
    expect(s.added).toEqual([]);
    s.panel.editPart('to', '13:00');
    expect(s.last().part?.error).toBe(pastEndText(754_000));
    // A handle moved: the typed time is dropped with its error.
    s.panel.setPartMs('to', 300_000);
    expect(s.last().part?.error).toBeUndefined();
    await s.panel.download();
    expect(s.added.at(-1)?.[2]).toEqual({ startMs: 165_000, endMs: 300_000 });
  });

  it('offers a page video’s chapters and sends the part with the site add', async () => {
    const s = setup(offers(true, { site }));
    s.panel.open(TARGET);
    await vi.advanceTimersByTimeAsync(0);
    s.panel.togglePart();
    expect(s.last().part?.chapters.map((c) => c.label)).toEqual(['1. Intro (0:00–1:20)', '2. Rest (1:20–12:34)']);
    s.panel.choosePartChapter(2);
    expect(s.last().part).toMatchObject({ from: '1:20', to: '12:34', summary: 'Part 1:20 – 12:34', chapter: 2 });
    s.panel.setPartMs('to', 300_000);
    expect(s.last().part?.chapter).toBe(0);
    await s.panel.download();
    await vi.advanceTimersByTimeAsync(0);
    expect(s.siteAdds.at(-1)?.[1]).toEqual({ startMs: 80_000, endMs: 300_000 });
    s.panel.wholePart();
    expect(s.last().part).toMatchObject({ whole: true, summary: 'Whole video' });
  });

  it('shows the part’s estimate in the rows too, the same as the readout’s', async () => {
    const s = setup(offers(true, { streams: [stream('s1')] }));
    s.panel.open(TARGET);
    await vi.advanceTimersByTimeAsync(0);
    expect(s.last().rows[0]?.size).toBe('about 189 MB');
    s.panel.togglePart();
    s.panel.setPartMs('from', 83_400);
    s.panel.setPartMs('to', 165_000);
    expect(s.last().part?.size).toBe('~21.8 MB at 720p');
    expect(s.last().rows[0]).toMatchObject({ size: 'about 21.8 MB', label: expect.stringContaining('about 21.8 MB') });
    // Closed, the whole video goes: its size again.
    s.panel.togglePart();
    expect(s.last().rows[0]?.size).toBe('about 189 MB');

    const v = setup(offers(true, { site }));
    v.panel.open(TARGET);
    await vi.advanceTimersByTimeAsync(0);
    const whole = v.last().rows[0]?.size;
    v.panel.togglePart();
    v.panel.choosePartChapter(1);
    const row = v.last().rows[0]!;
    expect(row.size).toMatch(/^~/);
    expect(row.size).not.toBe(whole);
    expect(v.last().part?.size).toBe(`${row.size} at 1080p`);
  });

  it('takes the clicked element’s duration when the site gives none (Instagram), and has no row without either', async () => {
    const zero: VideoInfoResult = { ...SITE_INFO, site: 'Instagram', durationMs: 0, chapters: undefined };
    const ig: SiteOffer = { key: 'instagram:CODEaaa111', site: 'Instagram', kind: 'watch', pageUrl: 'https://www.instagram.com/reel/CODEaaa111/', curated: true };
    let elementMs = 0;
    const s = setup(offers(true, { site: ig }), { siteProbe: async () => ({ ok: true, value: zero }) });
    s.panel.open({ ...TARGET, durationMs: () => elementMs });
    await vi.advanceTimersByTimeAsync(0);
    // Neither knows the length: no row (before 2026-11-18 a zero-length timeline was drawn).
    expect(s.last().rows.length).toBeGreaterThan(0);
    expect(s.last().part).toBeUndefined();
    // The element's metadata arrived: read on every render.
    elementMs = 41_500;
    s.panel.togglePart();
    expect(s.last().part).toMatchObject({ durationMs: 41_500, endMs: 41_500, whole: true });
    s.panel.setPartMs('from', 10_000);
    s.panel.setPartMs('to', 20_000);
    expect(s.last().part).toMatchObject({ from: '0:10', to: '0:20', summary: 'Part 0:10 – 0:20' });
    await s.panel.download();
    await vi.advanceTimersByTimeAsync(0);
    expect(s.siteAdds.at(-1)?.[1]).toEqual({ startMs: 10_000, endMs: 20_000 });
  });

  it('keeps the site’s own duration over the element’s, and never takes a live element’s', async () => {
    const s = setup(offers(true, { site }));
    s.panel.open({ ...TARGET, durationMs: () => 5_000 });
    await vi.advanceTimersByTimeAsync(0);
    expect(s.last().part?.durationMs).toBe(754_000);

    const zero: VideoInfoResult = { ...STREAM_INFO, durationMs: 0 };
    const live = setup(offers(true, { streams: [stream('s1')] }), { streamProbe: async () => ({ ok: true, value: zero }) });
    live.panel.open({ ...TARGET, live: true, durationMs: () => 60_000 });
    await vi.advanceTimersByTimeAsync(0);
    expect(live.last().part).toBeUndefined();
    const broken = setup(offers(true, { streams: [stream('s1')] }), { streamProbe: async () => ({ ok: true, value: zero }) });
    broken.panel.open({
      ...TARGET,
      durationMs: () => {
        throw new Error('gone');
      },
    });
    await vi.advanceTimersByTimeAsync(0);
    expect(broken.last().part).toBeUndefined();
  });
});

describe('OfferPanelDom Part row', () => {
  const VIEW: PartView = {
    expanded: true, summary: 'Part 1:20 – 2:45', durationMs: 754_000, startMs: 80_000, endMs: 165_000, from: '1:20', to: '2:45', whole: false,
    length: '1:25', size: '~22.5 MB at 720p', chapter: 0,
    chapters: [{ startMs: 0, endMs: 80_000, title: 'Intro', label: '1. Intro (0:00–1:20)' }, { startMs: 80_000, endMs: 754_000, title: 'Rest', label: '2. Rest (1:20–12:34)' }],
  };
  const BASE: OfferPanelView = { phase: 'ready', title: 'T', rows: [], groups: [], defaultId: undefined, lookForMore: false, narrow: false, busy: false };

  function dom(over: Partial<OfferPanelDomDeps> = {}) {
    document.body.replaceChildren();
    const host = document.createElement('div');
    document.body.append(host);
    const shadow = host.attachShadow({ mode: 'closed' });
    const calls: string[] = [];
    const trusted = new WeakSet<Event>();
    const d = new OfferPanelDom({
      doc: document,
      isTrusted: (e) => trusted.has(e),
      onRow: () => {},
      onGroup: () => {},
      onAudio: () => {},
      onAction: () => {},
      onLookForMore: () => {},
      onHide: () => {},
      onClose: () => calls.push('close'),
      onPartToggle: () => calls.push('toggle'),
      onPartSet: (w, ms, push) => calls.push(`set:${w}:${ms}${push ? ':push' : ''}`),
      onPartEdit: (w, t) => calls.push(`edit:${w}:${t}`),
      onPartChapter: (n) => calls.push(`chapter:${n}`),
      onPartWhole: () => calls.push('whole'),
      ...over,
    });
    shadow.append(d.el);
    d.shown = true;
    const mark = <E extends Event>(e: E): E => {
      trusted.add(e);
      return e;
    };
    const click = (el: Element) => el.dispatchEvent(mark(new MouseEvent('click', { bubbles: true })));
    const q = <T extends Element>(sel: string) => d.el.querySelector<T>(sel)!;
    Object.defineProperty(q('.dx-trim-box'), 'getBoundingClientRect', { value: () => ({ left: 0, width: 754, top: 0, height: 40, right: 754, bottom: 40, x: 0, y: 0 }) });
    const pointer = (type: string, el: Element, clientX: number) =>
      el.dispatchEvent(mark(new PointerEvent(type, { bubbles: true, cancelable: true, clientX, pointerId: 3, pointerType: 'touch' })));
    return { d, shadow, calls, click, q, pointer, mark };
  }

  it('draws the trimmer and reports what the user does', () => {
    const { d, calls, click, q, pointer } = dom();
    d.render(BASE);
    expect(q<HTMLElement>('.dx-part').hidden).toBe(true);
    d.render({ ...BASE, part: VIEW });
    expect(q<HTMLElement>('.dx-part').hidden).toBe(false);
    expect(q('.dx-part-toggle').getAttribute('aria-expanded')).toBe('true');
    expect(q('.dx-trim-detail').textContent).toBe('1:25 selected · ~22.5 MB at 720p');
    click(q('.dx-part-toggle'));
    // A touch drag on the track: the nearer handle (the end) follows it.
    pointer('pointerdown', q('.dx-trim'), 300);
    pointer('pointermove', q('.dx-trim'), 320.4);
    pointer('pointerup', q('.dx-trim'), 320.4);
    // The keys of a focused handle.
    q<HTMLElement>(".dx-trim-handle[data-which='from']").dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowLeft', shiftKey: true, bubbles: true, cancelable: true }));
    // A chip typed into, a chapter, "Whole video".
    click(q(".dx-trim-chip[data-which='to']"));
    const field = q<HTMLInputElement>(".dx-trim-chip[data-which='to'] + input");
    field.value = '3:00';
    field.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }));
    click(d.el.querySelectorAll('.dx-trim-chap')[1]!);
    click(q('.dx-trim-whole'));
    expect(calls).toEqual(['toggle', 'set:to:300000', 'set:to:320000', 'set:from:70000', 'edit:to:3:00', 'chapter:2', 'whole']);
    // Busy (a pick being sent, or sent): every control of the row is disabled, the toggle too.
    d.render({ ...BASE, busy: true, part: VIEW });
    expect(q<HTMLButtonElement>('.dx-part-toggle').disabled).toBe(true);
    expect(q<HTMLButtonElement>('.dx-trim-chip').disabled).toBe(true);
    expect(q('.dx-trim-handle').getAttribute('aria-disabled')).toBe('true');
    calls.length = 0;
    click(q('.dx-part-toggle'));
    pointer('pointerdown', q('.dx-trim'), 300);
    expect(calls).toEqual([]);
    d.render({ ...BASE, phase: 'sent', part: VIEW });
    expect(q<HTMLButtonElement>('.dx-part-toggle').disabled).toBe(true);
  });

  it('keeps Up and Down in the row’s sliders and fields, and Esc in a field', () => {
    const { d, shadow, calls, click, q } = dom();
    const row = { id: 'r1', source: 'stream', key: 's1', action: 'add', pick: { video: 'v', audio: '' }, quality: '720p', detail: '', size: '', label: '720p', tag: 'Stream', disabled: false, selected: true } as unknown as OfferPanelView['rows'][number];
    d.render({ ...BASE, rows: [row], defaultId: 'r1', part: VIEW });
    const handle = q<HTMLElement>(".dx-trim-handle[data-which='to']");
    handle.focus();
    for (const k of ['ArrowDown', 'ArrowUp']) {
      const e = new KeyboardEvent('keydown', { key: k, bubbles: true, composed: true, cancelable: true });
      handle.dispatchEvent(e);
      expect(shadow.activeElement).toBe(handle);
    }
    click(q(".dx-trim-chip[data-which='from']"));
    const field = q<HTMLInputElement>(".dx-trim-chip[data-which='from'] + input");
    expect(shadow.activeElement).toBe(field);
    field.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true, composed: true, cancelable: true }));
    expect(shadow.activeElement).toBe(field);
    field.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, composed: true, cancelable: true }));
    expect(calls).not.toContain('close');
    expect(field.hidden).toBe(true);
    // Elsewhere they still move between the rows.
    q<HTMLButtonElement>('.dx-part-toggle').focus();
    q('.dx-part-toggle').dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowUp', bubbles: true, composed: true, cancelable: true }));
    expect(shadow.activeElement).toBe(q('button.dx-row'));
  });
});
