// Pages with several videos (SITE-MEDIA-SPEC §3.12, VIDEO-DETECT-SPEC §5.2 "Several videos"): the
// relay answer's items, the panel state that chooses one, and the item row's DOM.

// @vitest-environment happy-dom

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { clickedItem, ItemStrip, itemLabel, itemsCountText, itemsLeftOutText } from './item-strip';
import { isExtensionMessage } from './messages';
import { type AddedResult, type Outcome, parseVideoInfo, type VideoInfoResult, type VideoItem, type VideoTrack } from './protocol';
import { VideoPanel, type VideoPanelItems, type VideoPanelView } from './video-panel';
import type { TrackPick } from './video-picker';

function track(t: Partial<VideoTrack> & Pick<VideoTrack, 'id' | 'kind'>): VideoTrack {
  return { ext: 'mp4', codecs: 'avc1.4d401e', width: 640, height: 640, frameRate: 30, bandwidth: 0, size: 0, sizeApprox: false, language: '', name: '480p', ...t };
}

const ITEMS: VideoItem[] = [
  { id: 'itemA', title: 'Video by example', durationMs: 12_000, thumbnail: 'https://img.example/a.jpg' },
  { id: 'itemB', title: 'Video by example', durationMs: 7_500, thumbnail: '' },
  { id: 'itemC', title: 'Third', durationMs: 0, thumbnail: 'https://img.example/c.jpg' },
];

function info(entryId: string, extra: Partial<VideoInfoResult> = {}): VideoInfoResult {
  return {
    type: 'videoInfo',
    site: 'Instagram',
    title: `Video ${entryId}`,
    durationMs: 0,
    format: 'http',
    container: 'mp4',
    selectedVideo: `v-${entryId}`,
    selectedAudio: '',
    tracks: [track({ id: `v-${entryId}`, kind: 'video' })],
    entryId,
    items: ITEMS,
    itemsLeftOut: 2,
    ...extra,
  };
}

describe('parseVideoInfo items', () => {
  const wire = (extra: Record<string, unknown>) => ({
    site: 'Instagram',
    title: 't',
    durationMs: 0,
    format: 'http',
    container: 'mp4',
    selectedVideo: 'v',
    selectedAudio: '',
    tracks: [{ id: 'v', kind: 'video', ext: 'mp4', codecs: '', width: 1, height: 1, frameRate: 0, bandwidth: 0, size: 0, sizeApprox: false, language: '', name: '' }],
    ...extra,
  });

  it('reads the items, the item described and the left-out count', () => {
    const got = parseVideoInfo(wire({ entryId: 'itemB', items: ITEMS, itemsLeftOut: 2 }));
    expect(got).toMatchObject({ entryId: 'itemB', items: ITEMS, itemsLeftOut: 2 });
  });

  it('keeps an answer without items as it is, and drops a bad list (never the answer)', () => {
    expect(parseVideoInfo(wire({}))?.items).toBeUndefined();
    for (const bad of [
      { entryId: 'gone', items: ITEMS, itemsLeftOut: 0 },
      { entryId: 'itemA', items: [ITEMS[0], ITEMS[0]], itemsLeftOut: 0 },
      { entryId: 'itemA', items: ITEMS, itemsLeftOut: -1 },
      { entryId: 'itemA', items: [{ ...ITEMS[0], durationMs: 1.5 }], itemsLeftOut: 0 },
      { entryId: 'x', items: Array.from({ length: 21 }, (_, i) => ({ id: i === 0 ? 'x' : `i${i}`, title: '', durationMs: 0, thumbnail: '' })), itemsLeftOut: 0 },
    ]) {
      const got = parseVideoInfo(wire(bad));
      expect(got?.type).toBe('videoInfo');
      expect(got?.items).toBeUndefined();
    }
  });

  it('keeps only https thumbnails without user information', () => {
    const items = [
      { id: 'a', title: '', durationMs: 0, thumbnail: 'http://img.example/a.jpg' },
      { id: 'b', title: '', durationMs: 0, thumbnail: 'https://u:p@img.example/b.jpg' },
      { id: 'c', title: '', durationMs: 0, thumbnail: 'javascript:alert(1)' },
      { id: 'd', title: '', durationMs: 0, thumbnail: 'https://img.example/d.jpg?x=1' },
    ];
    const got = parseVideoInfo(wire({ entryId: 'a', items, itemsLeftOut: 0 }));
    expect(got?.items?.map((i) => i.thumbnail)).toEqual(['', '', '', 'https://img.example/d.jpg?x=1']);
  });
});

describe('item messages', () => {
  it('take an optional item id of printable text', () => {
    expect(isExtensionMessage({ type: 'downex:page-video-probe', url: 'https://www.example.com/p/1/', entryId: 'itemB' })).toBe(true);
    expect(isExtensionMessage({ type: 'downex:page-video-add', url: 'https://www.example.com/p/1/', videoTrack: '', audioTrack: '', entryId: 'itemB' })).toBe(true);
    expect(isExtensionMessage({ type: 'downex:tab-video-probe', tabId: 3, key: 'instagram:1', entryId: 'itemB' })).toBe(true);
    expect(isExtensionMessage({ type: 'downex:tab-video-add', tabId: 3, key: 'instagram:1', videoTrack: '', audioTrack: '', entryId: 'itemB' })).toBe(true);
    for (const entryId of [3, 'a\nb', 'x'.repeat(257), null]) {
      expect(isExtensionMessage({ type: 'downex:page-video-probe', url: 'https://www.example.com/p/1/', entryId })).toBe(false);
      expect(isExtensionMessage({ type: 'downex:tab-video-add', tabId: 3, key: 'k', videoTrack: '', audioTrack: '', entryId })).toBe(false);
    }
  });
});

describe('VideoPanel items', () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  function setup(answer: (entryId?: string) => Outcome<VideoInfoResult>) {
    const views: VideoPanelView[] = [];
    const adds: { pick: TrackPick; entryId: string | undefined }[] = [];
    const probe = vi.fn(async (entryId?: string) => answer(entryId));
    const panel = new VideoPanel({
      probe,
      add: async (pick, entryId) => {
        adds.push({ pick, entryId });
        return { ok: true, value: { type: 'added', downloadId: 1 } } as Outcome<AddedResult>;
      },
      render: (v) => views.push(v),
    });
    return { panel, probe, adds, last: () => views[views.length - 1]! };
  }

  it('lists the items and gets another item when one is chosen; Download sends it', async () => {
    const { panel, probe, adds, last } = setup((id) => ({ ok: true, value: info(id ?? 'itemA') }));
    panel.open();
    await vi.runAllTimersAsync();
    expect(probe).toHaveBeenLastCalledWith();
    let items = last().items as VideoPanelItems;
    expect(items.index).toBe(0);
    expect(items.leftOut).toBe(2);
    expect(items.options.map((o) => o.selected)).toEqual([true, false, false]);
    expect(itemsCountText(items)).toBe('Video 1 of 3');

    panel.chooseItem('itemC');
    // While it is got, the row stays (the chosen item marked) and the qualities are gone.
    expect(last().phase).toBe('probing');
    expect(last().rows).toEqual([]);
    expect(last().items?.index).toBe(2);
    await vi.runAllTimersAsync();
    expect(probe).toHaveBeenLastCalledWith('itemC');
    expect(last().title).toBe('Video itemC');
    items = last().items as VideoPanelItems;
    expect(itemsCountText(items)).toBe('Video 3 of 3');
    await panel.download();
    expect(adds).toEqual([{ pick: { video: 'v-itemC', audio: '' }, entryId: 'itemC' }]);
    // Ids the list does not have, and the item already shown, change nothing.
    probe.mockClear();
    panel.chooseItem('nope');
    panel.chooseItem('itemC');
    expect(probe).not.toHaveBeenCalled();
  });

  it('keeps the row after an item failed, and reset forgets it', async () => {
    const { panel, last } = setup((id) =>
      id === 'itemB'
        ? { ok: false, error: { code: 'rejected', message: 'site_unavailable: this video is not available: this item is no longer on the page', reason: 'site_unavailable' } }
        : { ok: true, value: info('itemA') },
    );
    panel.open();
    await vi.runAllTimersAsync();
    panel.chooseItem('itemB');
    await vi.runAllTimersAsync();
    expect(last().phase).toBe('error');
    expect(last().items?.index).toBe(1);
    // The reason is said (SITE-MEDIA-SPEC §5.5): the item left the page.
    expect(last().status?.text).toMatch(/This item is no longer on the page\.$/);
    panel.chooseItem('itemA');
    await vi.runAllTimersAsync();
    expect(last().phase).toBe('list');
    panel.reset();
    expect(last().items).toBeUndefined();
  });

  it('shows no row for a page with one video, and only the note for one video with photos', async () => {
    const plain = setup(() => ({ ok: true, value: info('', { entryId: undefined, items: undefined, itemsLeftOut: undefined }) }));
    plain.panel.open();
    await vi.runAllTimersAsync();
    expect(plain.last().items).toBeUndefined();
    const one = setup(() => ({ ok: true, value: info('itemA', { items: [ITEMS[0]!], itemsLeftOut: 2 }) }));
    one.panel.open();
    await vi.runAllTimersAsync();
    expect(one.last().items?.options).toHaveLength(1);
    expect(itemsCountText(one.last().items!)).toBe('Video 1 of 1');
    // Downloading the only video still names it.
    await one.panel.download();
    expect(one.adds[0]?.entryId).toBe('itemA');
  });
});

describe('ItemStrip', () => {
  const view = (index: number, leftOut = 0): VideoPanelItems => ({
    options: ITEMS.map((it, i) => ({ ...it, selected: i === index })),
    index,
    leftOut,
  });

  function setup(thumbnails = true) {
    const chosen: string[] = [];
    const strip = new ItemStrip({ doc: document, thumbnails, isTrusted: () => true, onChoose: (id) => chosen.push(id) });
    document.body.replaceChildren(strip.el);
    const tiles = () => [...strip.el.querySelectorAll<HTMLButtonElement>('button.dx-item')];
    return { strip, chosen, tiles };
  }

  it('draws one tile per video, the chosen one checked, with the thumbnail loaded without a Referer', () => {
    const { strip, tiles } = setup();
    strip.render(view(1, 2), false);
    expect(strip.el.hidden).toBe(false);
    expect(strip.el.querySelector('.dx-items-count')?.textContent).toBe('Video 2 of 3');
    expect(strip.el.querySelector('.dx-items-note')?.textContent).toBe('2 other items not listed: photos, links or videos Downex cannot save.');
    const t = tiles();
    expect(t.map((b) => b.getAttribute('aria-checked'))).toEqual(['false', 'true', 'false']);
    expect(t.map((b) => b.tabIndex)).toEqual([-1, 0, -1]);
    expect(t[0]!.getAttribute('aria-label')).toBe('Video 1: Video by example, 0:12');
    const img = t[0]!.querySelector('img');
    expect(img?.getAttribute('src')).toBe('https://img.example/a.jpg');
    expect(img?.referrerPolicy).toBe('no-referrer');
    expect(img?.alt).toBe('');
    expect(t[1]!.querySelector('img')).toBeNull();
    expect(t[1]!.querySelector('.dx-item-n')?.textContent).toBe('2');
    expect(t[1]!.querySelector('.dx-item-d')?.textContent).toBe('0:07.5');
    // A refresh keeps the tiles (no image is loaded again).
    strip.render(view(1, 2), false);
    expect(tiles()[0]).toBe(t[0]);
    strip.render(undefined, false);
    expect(strip.el.hidden).toBe(true);
  });

  it('chooses by click and by arrow keys, and not while busy', () => {
    const { strip, chosen, tiles } = setup();
    strip.render(view(0), false);
    tiles()[2]!.click();
    tiles()[0]!.focus();
    tiles()[0]!.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true }));
    tiles()[1]!.dispatchEvent(new KeyboardEvent('keydown', { key: 'End', bubbles: true }));
    expect(chosen).toEqual(['itemC', 'itemB', 'itemC']);
    strip.render(view(0), true);
    tiles()[1]!.click();
    expect(chosen).toHaveLength(3);
    expect(tiles().every((b) => b.disabled)).toBe(true);
  });

  it('draws numbers only where asked (the popup), and drops a thumbnail that fails', () => {
    const { strip, tiles } = setup(false);
    strip.render(view(0), false);
    expect(strip.el.querySelector('img')).toBeNull();
    const withImages = setup(true);
    withImages.strip.render(view(0), false);
    const img = withImages.tiles()[0]!.querySelector('img')!;
    img.dispatchEvent(new Event('error'));
    expect(withImages.tiles()[0]!.querySelector('img')).toBeNull();
    expect(tiles()).toHaveLength(3);
  });

  it('ignores clicks the page made', () => {
    const chosen: string[] = [];
    const strip = new ItemStrip({ doc: document, thumbnails: true, isTrusted: () => false, onChoose: (id) => chosen.push(id) });
    strip.render(view(0), false);
    strip.el.querySelector<HTMLButtonElement>('button.dx-item')!.click();
    expect(chosen).toEqual([]);
  });

  it('words the count, the note and the labels', () => {
    expect(itemsCountText({ options: [], index: -1, leftOut: 3 })).toBe('');
    expect(itemsCountText(view(-1))).toBe('3 videos');
    expect(itemsLeftOutText(0)).toBe('');
    expect(itemsLeftOutText(1)).toBe('1 other item not listed: a photo, a link or a video Downex cannot save.');
    expect(itemLabel(3, '‮Evil', 0)).toBe('Video 3: Evil');
    expect(itemLabel(1, '', 61_000)).toBe('Video 1, 1:01');
  });
});

describe('clickedItem (the item the user looks at)', () => {
  const list = [
    { id: 'a', title: '', durationMs: 12_000, thumbnail: '' },
    { id: 'b', title: '', durationMs: 7_500, thumbnail: '' },
    { id: 'c', title: '', durationMs: 7_900, thumbnail: '' },
    { id: 'd', title: '', durationMs: 0, thumbnail: '' },
  ];
  it('takes the one item of the clicked length, else the slide when nothing is left out', () => {
    expect(clickedItem(list, 0, { durationMs: 12_300 })).toBe('a');
    // two items of about that length: the slide decides between them, never for another
    expect(clickedItem(list, 0, { durationMs: 7_700 })).toBeUndefined();
    expect(clickedItem(list, 0, { durationMs: 7_700, slide: 3 })).toBe('c');
    expect(clickedItem(list, 0, { durationMs: 7_700, slide: 1 })).toBeUndefined();
    // an item of unknown length can be the slide's
    expect(clickedItem(list, 0, { durationMs: 0, slide: 4 })).toBe('d');
    expect(clickedItem(list, 0, { durationMs: 20_000, slide: 4 })).toBe('d');
    // the slides include items left out (photos): their number names no item
    expect(clickedItem(list, 1, { durationMs: 0, slide: 2 })).toBeUndefined();
    expect(clickedItem(list, 0, { durationMs: 0, slide: 5 })).toBeUndefined();
    expect(clickedItem(list, 0, { durationMs: 0 })).toBeUndefined();
  });
});
