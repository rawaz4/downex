import { describe, expect, it } from 'vitest';
import { DEFAULT_CATEGORIES, type Download } from './api';
import { CategoryIndex } from './categories';
import { SortedView, countFacets, countQueues, filterDownloads, sortDownloads, toggleSort } from './query';
import { makeDownload } from './testing';

const list = [
  makeDownload({ id: 1, filename: 'file10.zip', size: 300, state: 'completed', received: 300, createdAt: 30 }),
  makeDownload({ id: 2, filename: 'file2.zip', size: 100, state: 'downloading', received: 50, speedBps: 10, createdAt: 10 }),
  makeDownload({ id: 3, filename: 'Clip.mp4', size: -1, state: 'downloading', received: 5, speedBps: 50, createdAt: 20 }),
  makeDownload({ id: 4, filename: 'notes.pdf', size: 200, state: 'paused', received: 150, createdAt: 40 }),
  makeDownload({ id: 5, filename: 'song.mp3', size: 100, state: 'failed', received: 10, createdAt: 50, url: 'https://media.example.org/x' }),
  makeDownload({ id: 6, filename: 'tool.exe', size: 100, state: 'queued', queuePosition: 2, createdAt: 60 }),
  makeDownload({ id: 7, filename: 'tool2.exe', size: 100, state: 'queued', queuePosition: 1, createdAt: 60 }),
];

const ids = (xs: { id: number }[]) => xs.map((d) => d.id);
const index = new CategoryIndex(DEFAULT_CATEGORIES);

describe('filterDownloads', () => {
  it('filters by category, status and search together', () => {
    expect(ids(filterDownloads(list, { category: 'cat:Archives', status: 'any', search: '' }, index))).toEqual([1, 2]);
    expect(ids(filterDownloads(list, { category: 'all', status: 'active', search: '' }, index))).toEqual([2, 3]);
    expect(ids(filterDownloads(list, { category: 'cat:Archives', status: 'active', search: '' }, index))).toEqual([2]);
    expect(ids(filterDownloads(list, { category: 'all', status: 'any', search: '  CLIP ' }, index))).toEqual([3]);
  });

  it('searches URLs as well as names', () => {
    expect(ids(filterDownloads(list, { category: 'all', status: 'any', search: 'media.example' }, index))).toEqual([5]);
  });
});

describe('sortDownloads', () => {
  it('sorts names naturally and case-insensitively', () => {
    const sorted = sortDownloads(list, { key: 'name', dir: 'asc' });
    expect(sorted.map((d) => d.filename)).toEqual([
      'Clip.mp4',
      'file2.zip',
      'file10.zip',
      'notes.pdf',
      'song.mp3',
      'tool.exe',
      'tool2.exe',
    ]);
  });

  it('does not mutate its input', () => {
    const copy = [...list];
    sortDownloads(list, { key: 'size', dir: 'asc' });
    expect(list).toEqual(copy);
  });

  it('breaks ties on id in the sort direction', () => {
    expect(ids(sortDownloads(list, { key: 'size', dir: 'desc' }))).toEqual([1, 4, 7, 6, 5, 2, 3]);
    expect(ids(sortDownloads(list, { key: 'size', dir: 'asc' }))).toEqual([3, 2, 5, 6, 7, 4, 1]);
  });

  it('keeps unknown progress and ETA last in both directions', () => {
    expect(ids(sortDownloads(list, { key: 'progress', dir: 'asc' })).at(-1)).toBe(3);
    expect(ids(sortDownloads(list, { key: 'progress', dir: 'desc' })).at(-1)).toBe(3);
    const byEta = ids(sortDownloads(list, { key: 'eta', dir: 'desc' }));
    expect(byEta[0]).toBe(2);
    expect(ids(sortDownloads(list, { key: 'eta', dir: 'asc' }))[0]).toBe(2);
  });

  it('orders status by activity, queued by queue position', () => {
    expect(ids(sortDownloads(list, { key: 'status', dir: 'asc' }))).toEqual([2, 3, 7, 6, 4, 5, 1]);
  });

  it('sorts by speed with only running downloads counting', () => {
    expect(ids(sortDownloads(list, { key: 'speed', dir: 'desc' })).slice(0, 2)).toEqual([3, 2]);
  });

  it('sorts by added date', () => {
    expect(ids(sortDownloads(list, { key: 'added', dir: 'desc' }))).toEqual([7, 6, 5, 4, 1, 3, 2]);
  });
});

describe('toggleSort', () => {
  it('flips direction on the same key and picks a natural first direction otherwise', () => {
    expect(toggleSort({ key: 'size', dir: 'desc' }, 'size')).toEqual({ key: 'size', dir: 'asc' });
    expect(toggleSort({ key: 'size', dir: 'asc' }, 'name')).toEqual({ key: 'name', dir: 'asc' });
    expect(toggleSort({ key: 'name', dir: 'asc' }, 'added')).toEqual({ key: 'added', dir: 'desc' });
  });
});

describe('countFacets', () => {
  it('counts categories under the status filter and statuses under the category', () => {
    const all = countFacets(list, 'all', 'any', index);
    expect(Object.fromEntries(all.categories)).toEqual({ all: 7, 'cat:Archives': 2, 'cat:Video': 1, 'cat:Documents': 1, 'cat:Audio': 1, 'cat:Programs': 2, other: 0 });
    expect(all.statuses).toMatchObject({ any: 7, active: 2, queued: 2, paused: 1, completed: 1, failed: 1 });

    const activeOnly = countFacets(list, 'cat:Archives', 'active', index);
    expect(Object.fromEntries(activeOnly.categories)).toMatchObject({ all: 2, 'cat:Archives': 1, 'cat:Video': 1, 'cat:Programs': 0 });
    expect(activeOnly.statuses).toMatchObject({ any: 2, active: 1, completed: 1, queued: 0 });
  });
});

describe('SortedView (incremental order)', () => {
  const KEYS = ['name', 'size', 'progress', 'speed', 'eta', 'connections', 'status', 'added'] as const;
  const STATES = ['queued', 'downloading', 'paused', 'completed', 'failed', 'probing'] as const;

  function rng(seed: number) {
    let s = seed >>> 0;
    return () => {
      s = (s * 1664525 + 1013904223) >>> 0;
      return s / 2 ** 32;
    };
  }

  function randomDownload(id: number, r: () => number): Download {
    const size = r() < 0.1 ? -1 : Math.floor(r() * 1000);
    return makeDownload({
      id,
      filename: r() < 0.3 ? '' : `file ${Math.floor(r() * 50)}.zip`,
      url: `https://example.com/d/${Math.floor(r() * 40)}.bin`,
      size,
      received: Math.floor(r() * 1000),
      state: STATES[Math.floor(r() * STATES.length)],
      speedBps: Math.floor(r() * 5) * 100,
      connections: Math.floor(r() * 4),
      queuePosition: Math.floor(r() * 5),
      createdAt: Math.floor(r() * 30),
    });
  }

  it.each(KEYS.flatMap((key) => (['asc', 'desc'] as const).map((dir) => ({ key, dir }))))(
    'matches a full sort after random updates ($key $dir)',
    ({ key, dir }) => {
      const r = rng(key.length * 31 + dir.length);
      const items = new Map<number, Download>();
      for (let id = 1; id <= 300; id++) items.set(id, randomDownload(id, r));
      const view = new SortedView();
      const spec = { key, dir };
      expect(view.update(items, spec)).toEqual(sortDownloads([...items.values()], spec));
      let nextId = 301;
      for (let batch = 0; batch < 40; batch++) {
        for (let n = 0; n < 6; n++) {
          const roll = r();
          const id = 1 + Math.floor(r() * (nextId - 1));
          const prev = items.get(id);
          if (roll < 0.15 && prev) {
            view.touch(id, prev);
            items.delete(id);
          } else if (roll < 0.3) {
            const d = randomDownload(nextId++, r);
            view.touch(d.id, undefined);
            items.set(d.id, d);
          } else if (prev) {
            // live update: often only the volatile fields change
            const next =
              r() < 0.5
                ? { ...prev, received: prev.received + 1, updatedAt: prev.updatedAt + 1 }
                : { ...randomDownload(id, r), createdAt: prev.createdAt };
            view.touch(id, prev);
            items.set(id, next);
          }
        }
        expect(ids(view.update(items, spec))).toEqual(ids(sortDownloads([...items.values()], spec)));
      }
    },
  );

  it('keeps the order without re-sorting when no sort value changed', () => {
    const items = new Map<number, Download>();
    for (let id = 1; id <= 1000; id++) {
      items.set(id, makeDownload({ id, filename: '', url: `https://example.com/q/${id}.iso`, state: 'downloading', speedBps: 10 }));
    }
    const view = new SortedView();
    const spec = { key: 'name', dir: 'asc' } as const;
    const first = view.update(items, spec);
    expect(view.fullSorts).toBe(1);
    expect(view.update(items, spec)).toBe(first); // nothing changed: same array
    for (let batch = 0; batch < 20; batch++) {
      for (const id of [3, 500, 999]) {
        const prev = items.get(id)!;
        view.touch(id, prev);
        items.set(id, { ...prev, received: prev.received + 1000, speedBps: prev.speedBps + 1 });
      }
      const next = view.update(items, spec);
      expect(next).not.toBe(first);
      expect(next.find((d) => d.id === 500)).toBe(items.get(500));
    }
    expect(view.fullSorts).toBe(1);
    view.update(items, { key: 'name', dir: 'desc' });
    expect(view.fullSorts).toBe(2);
  });

  it('sorts and searches 10,000 rows with empty file names quickly', () => {
    const items = new Map<number, Download>();
    for (let id = 1; id <= 10_000; id++) {
      items.set(id, makeDownload({ id, filename: '', url: `https://cdn.example.com/files/archive%20${id}.zip?x=${id}`, state: 'downloading', speedBps: id }));
    }
    const view = new SortedView();
    const spec = { key: 'name', dir: 'asc' } as const;
    view.update(items, spec);
    const t0 = performance.now();
    for (let batch = 0; batch < 20; batch++) {
      for (let k = 0; k < 16; k++) {
        const id = 1 + ((batch * 97 + k * 613) % 10_000);
        const prev = items.get(id)!;
        view.touch(id, prev);
        items.set(id, { ...prev, received: prev.received + 1, speedBps: prev.speedBps + 1 });
      }
      const sorted = view.update(items, spec);
      filterDownloads(sorted, { category: 'all', status: 'any', search: 'archive 99' }, index);
    }
    // Previously ~220 ms per batch; the bound is loose so slow CI machines pass.
    expect((performance.now() - t0) / 20).toBeLessThan(60);
    expect(view.fullSorts).toBe(1);
  });
});

describe('queues (M6)', () => {
  const queued = [
    makeDownload({ id: 1, filename: 'a.zip', state: 'queued', queueId: 2 }),
    makeDownload({ id: 2, filename: 'b.zip', state: 'completed', queueId: 2 }),
    makeDownload({ id: 3, filename: 'c.mp4', state: 'paused', queueId: 1 }),
    // An older daemon sends no queue: the main queue.
    makeDownload({ id: 4, filename: 'd.mp4', state: 'downloading', queueId: 0 }),
  ];

  it('filters by queue, with the other filters', () => {
    expect(ids(filterDownloads(queued, { category: 'all', status: 'any', search: '', queue: 2 }, index))).toEqual([1, 2]);
    expect(ids(filterDownloads(queued, { category: 'all', status: 'any', search: '', queue: 1 }, index))).toEqual([3, 4]);
    expect(ids(filterDownloads(queued, { category: 'cat:Video', status: 'active', search: '', queue: 1 }, index))).toEqual([4]);
    expect(ids(filterDownloads(queued, { category: 'all', status: 'any', search: '', queue: null }, index))).toHaveLength(4);
  });

  it('counts facets within the queue, and what each queue has left to do', () => {
    const counts = countFacets(queued, 'all', 'any', index, 2);
    expect(counts.categories.get('all')).toBe(2);
    expect(counts.statuses.completed).toBe(1);
    expect(countQueues(queued)).toEqual(new Map([[2, 1], [1, 2]]));
  });
});