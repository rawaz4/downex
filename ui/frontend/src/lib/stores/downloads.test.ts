import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Api, Download, EventBatch } from '../api';
import { createMockApi, type MockApi } from '../mock';
import { MiB, fakeApi, makeDownload } from '../testing';
import { DownloadsStore, mapLimit } from './downloads.svelte';

let disposers: (() => void)[] = [];
afterEach(() => {
  for (const d of disposers) d();
  disposers = [];
});

function mock(count = 0): MockApi {
  const api = createMockApi({ count, seed: 3, autoTick: false, failureRate: 0, defaultDir: '/home/you/Downloads' });
  disposers.push(() => api.dispose());
  return api;
}

/** A minimal Api whose events the test pushes by hand. */
function manualApi(initial: Download[]) {
  let listener: ((b: EventBatch) => void) | null = null;
  const api = {
    kind: 'mock',
    list: async () => initial,
    onEvents: (cb: (b: EventBatch) => void) => {
      listener = cb;
      return () => (listener = null);
    },
  } as unknown as Api;
  return { api, emit: (b: EventBatch) => listener?.(b) };
}

async function started(api: Api, now?: () => number) {
  const store = new DownloadsStore(api, now);
  disposers.push(() => store.dispose());
  await store.start();
  return store;
}

describe('DownloadsStore: loading and events', () => {
  it('loads the list and derives sorted rows and counts', async () => {
    const store = await started(mock(40));
    expect(store.ready).toBe(true);
    expect(store.all).toHaveLength(40);
    expect(store.visible).toHaveLength(40);
    const created = store.visible.map((d) => d.createdAt);
    expect(created).toEqual([...created].sort((a, b) => b - a));
    expect(store.counts.categories.get('all')).toBe(40);
  });

  it('applies updates and removals, ignoring stale updates', async () => {
    const { api, emit } = manualApi([makeDownload({ id: 1, updatedAt: 100 }), makeDownload({ id: 2 })]);
    const store = await started(api);
    emit([{ kind: 'updated', download: makeDownload({ id: 1, state: 'downloading', updatedAt: 200 }) }]);
    expect(store.get(1)?.state).toBe('downloading');
    emit([{ kind: 'updated', download: makeDownload({ id: 1, state: 'queued', updatedAt: 150 }) }]);
    expect(store.get(1)?.state).toBe('downloading');
    emit([{ kind: 'removed', id: 2 }, { kind: 'snapshot', download: makeDownload({ id: 3 }) }]);
    expect(store.all.map((d) => d.id).sort()).toEqual([1, 3]);
  });

  it('replays events that arrive while the list is loading', async () => {
    const hub: { release?: (list: Download[]) => void; listener?: (b: EventBatch) => void } = {};
    const api = {
      kind: 'mock',
      list: () => new Promise<Download[]>((r) => (hub.release = r)),
      onEvents: (cb: (b: EventBatch) => void) => {
        hub.listener = cb;
        return () => {};
      },
    } as unknown as Api;
    const store = new DownloadsStore(api);
    const done = store.start();
    hub.listener?.([{ kind: 'updated', download: makeDownload({ id: 1, state: 'completed', updatedAt: 9e12 }) }]);
    hub.release?.([makeDownload({ id: 1, state: 'downloading', updatedAt: 1 })]);
    await done;
    expect(store.get(1)?.state).toBe('completed');
  });

  it('records a speed history for running downloads', async () => {
    let t = 1000;
    const { api, emit } = manualApi([]);
    const store = await started(api, () => t);
    for (const v of [100, 200, 300]) {
      t += 250;
      emit([{ kind: 'updated', download: makeDownload({ id: 1, state: 'downloading', speedBps: v }) }]);
    }
    expect(store.historyOf(1).map((s) => s.v)).toEqual([100, 200, 300]);
    t += 120_000;
    emit([{ kind: 'updated', download: makeDownload({ id: 1, state: 'downloading', speedBps: 5 }) }]);
    expect(store.historyOf(1).map((s) => s.v)).toEqual([5]);
    emit([{ kind: 'removed', id: 1 }]);
    expect(store.historyOf(1)).toEqual([]);
  });

  it('records the total speed of every running download (the status bar sparkline)', async () => {
    let t = 1000;
    const { api, emit } = manualApi([makeDownload({ id: 1, updatedAt: 1 }), makeDownload({ id: 2, updatedAt: 1 })]);
    const store = await started(api, () => t);
    expect(store.totalHistory).toEqual([]);
    t += 250;
    emit([{ kind: 'updated', download: makeDownload({ id: 1, state: 'downloading', speedBps: 100, updatedAt: t }) }]);
    t += 250;
    emit([
      { kind: 'updated', download: makeDownload({ id: 1, state: 'downloading', speedBps: 300, updatedAt: t }) },
      { kind: 'updated', download: makeDownload({ id: 2, state: 'downloading', speedBps: 200, updatedAt: t }) },
    ]);
    t += 250;
    // a paused download no longer counts, and a stale speed of a stopped one never does
    emit([{ kind: 'updated', download: makeDownload({ id: 2, state: 'paused', speedBps: 999, updatedAt: t }) }]);
    expect(store.totalHistory).toEqual([
      { t: 1250, v: 100 },
      { t: 1500, v: 500 },
      { t: 1750, v: 300 },
    ]);
    // a batch that changes nothing adds no sample
    emit([{ kind: 'updated', download: makeDownload({ id: 2, state: 'paused', updatedAt: 1 }) }]);
    expect(store.totalHistory).toHaveLength(3);
    // samples older than the window fall out
    t += 120_000;
    emit([{ kind: 'updated', download: makeDownload({ id: 1, state: 'downloading', speedBps: 50, updatedAt: t }) }]);
    expect(store.totalHistory).toEqual([{ t, v: 50 }]);
  });
});

describe('DownloadsStore: filters and selection', () => {
  const rows = [
    makeDownload({ id: 1, filename: 'a.mp4', state: 'downloading' }),
    makeDownload({ id: 2, filename: 'b.zip', state: 'completed' }),
    makeDownload({ id: 3, filename: 'c.mp4', state: 'paused' }),
    makeDownload({ id: 4, filename: 'd.pdf', state: 'failed' }),
  ];

  it('filters by category, status and search', async () => {
    const store = await started(manualApi(rows).api);
    store.setCategory('cat:Video');
    expect(store.visibleIds.sort()).toEqual([1, 3]);
    store.setStatusFilter('paused');
    expect(store.visibleIds).toEqual([3]);
    expect(store.counts.statuses).toMatchObject({ any: 2, active: 1, paused: 1 });
    store.setStatusFilter('any');
    store.setSearch('A.MP4');
    expect(store.visibleIds).toEqual([1]);
  });

  it('groups by the configured categories and drops a filter on a category that is gone', async () => {
    const store = await started(manualApi(rows).api);
    store.setCategories([
      { name: 'Clips', extensions: ['mp4'], dir: '' },
      { name: 'Docs', extensions: ['pdf'], dir: '' },
    ]);
    expect(Object.fromEntries(store.counts.categories)).toEqual({ all: 4, 'cat:Clips': 2, 'cat:Docs': 1, other: 1 });
    store.setCategory('cat:Docs');
    expect(store.visibleIds).toEqual([4]);
    store.setCategory('other');
    expect(store.visibleIds).toEqual([2]);
    store.setCategory('cat:Docs');
    store.setCategories([{ name: 'Clips', extensions: ['mp4'], dir: '' }]);
    expect(store.category).toBe('all');
    expect(store.visibleIds).toHaveLength(4);
  });

  it('confirms a waiting download and lists the waiting ones oldest first', async () => {
    const api = fakeApi([
      makeDownload({ id: 1, state: 'awaiting_confirmation', createdAt: 50 }),
      makeDownload({ id: 2, state: 'awaiting_confirmation', createdAt: 40 }),
      makeDownload({ id: 3, state: 'queued' }),
    ]);
    const store = await started(api);
    expect(store.awaiting.map((d) => d.id)).toEqual([2, 1]);
    expect(store.counts.statuses.awaiting).toBe(2);
    await store.confirm(2, { start: false, dir: '/data' });
    expect(store.get(2)).toMatchObject({ state: 'paused', dir: '/data' });
    expect(store.awaiting.map((d) => d.id)).toEqual([1]);
    await expect(store.confirm(2, { start: true })).rejects.toThrow(/not awaiting/);
    // restart never applies to a download that has not started
    expect(await store.restart([1])).toEqual([]);
  });

  it('acts only on visible selected rows and prunes the selection when filters hide rows', async () => {
    const { api, emit } = manualApi(rows);
    const store = await started(api);
    store.selectAll();
    expect(store.selected).toHaveLength(4);
    store.setCategory('cat:Video');
    expect([...store.selection.ids].sort()).toEqual([1, 3]);
    store.setCategory('all');
    store.setStatusFilter('active');
    store.selectAll();
    // the download completes and leaves the "Active" view: nothing visible stays selected
    emit([{ kind: 'updated', download: makeDownload({ id: 1, filename: 'a.mp4', state: 'completed', updatedAt: 9e12 }) }]);
    expect(store.selected).toEqual([]);
    emit([{ kind: 'removed', id: 1 }]);
    expect(store.selection.ids.has(1)).toBe(false);
  });

  it('drives selection by click and keyboard in display order', async () => {
    const store = await started(manualApi(rows).api);
    store.sortBy('name');
    expect(store.sort).toEqual({ key: 'name', dir: 'asc' });
    store.click(1);
    store.click(3, { shift: true });
    expect(store.selected.map((d) => d.id)).toEqual([1, 2, 3]);
    store.move(1);
    expect(store.selected.map((d) => d.id)).toEqual([4]);
    expect(store.detail?.id).toBe(4);
    store.clearSelection();
    expect(store.detail).toBeUndefined();
  });
});

describe('DownloadsStore: actions', () => {
  it('pauses, resumes, restarts and removes through the api', async () => {
    const api = mock();
    const store = await started(api);
    const [ok, bad] = await store.add([
      { url: 'https://downloads.example.com/pub/a.mkv' },
      { url: 'ftp://downloads.example.com/b.mkv' },
    ]);
    expect(ok).toMatchObject({ ok: true });
    expect(bad).toMatchObject({ ok: false, error: expect.stringContaining('http') });
    const id = ok?.ok ? ok.download.id : -1;
    expect(store.get(id)?.state).toBe('queued');

    expect(await store.pause([id])).toEqual([{ id, ok: true }]);
    expect(store.get(id)?.state).toBe('paused');
    // nothing to pause any more: filtered out instead of failing
    expect(await store.pause([id])).toEqual([]);

    await store.resume([id]);
    expect(store.get(id)?.state).toBe('queued');
    for (let i = 0; i < 5; i++) api.tick(1000);
    await store.restart([id]);
    expect(store.get(id)).toMatchObject({ state: 'queued', received: 0 });

    store.selectOnly(id);
    expect(await store.remove([id], true)).toEqual([{ id, ok: true }]);
    expect(store.get(id)).toBeUndefined();
    expect(store.selection.ids.size).toBe(0);
    const failed = await store.remove([id], false);
    expect(failed[0]).toMatchObject({ ok: false });
  });

  it('downloads completed downloads again with the choice for their file; others restart as before', async () => {
    const api = fakeApi([
      makeDownload({ id: 1, filename: 'report.pdf', state: 'completed', received: 10 * MiB }),
      makeDownload({ id: 2, filename: 'video.mp4', state: 'completed', received: 10 * MiB }),
      makeDownload({ id: 3, state: 'downloading', received: MiB }),
      makeDownload({ id: 4, state: 'awaiting_confirmation' }),
    ]);
    const resumes: unknown[][] = [];
    const resume = api.resume;
    api.resume = async (id, opts) => {
      resumes.push([id, opts]);
      return resume(id, opts);
    };
    const store = await started(api);

    expect(await store.restart([1, 3, 4], 'replace')).toEqual([
      { id: 1, ok: true },
      { id: 3, ok: true },
    ]);
    // existing_file only for the completed one (downexd ignores it for others, never without restart)
    expect(resumes).toEqual([
      [1, { restart: true, existingFile: 'replace' }],
      [3, { restart: true }],
    ]);
    expect(store.get(1)).toMatchObject({ state: 'queued', filename: 'report.pdf' });
    // the running one was paused first, as before
    expect(api.calls.filter((c) => c === 'pause')).toHaveLength(1);

    // Keep both is the default: the new copy gets a numbered name.
    resumes.length = 0;
    expect(await store.restart([2])).toEqual([{ id: 2, ok: true }]);
    expect(resumes).toEqual([[2, { restart: true, existingFile: 'keepBoth' }]]);
    expect(store.get(2)?.filename).toBe('video (1).mp4');
  });

  it('copies URLs of the given downloads', async () => {
    const copied: string[] = [];
    const { api } = manualApi([makeDownload({ id: 1, url: 'https://a.example/1' }), makeDownload({ id: 2, url: 'https://a.example/2' })]);
    (api as { copyText: (t: string) => Promise<void> }).copyText = async (t) => {
      copied.push(t);
    };
    const store = await started(api);
    expect(await store.copyUrls([2, 1, 99])).toBe(2);
    expect(copied).toEqual(['https://a.example/2\nhttps://a.example/1']);
  });
});

describe('mapLimit', () => {
  it('bounds concurrency and keeps result order', async () => {
    let running = 0;
    let peak = 0;
    const out = await mapLimit([5, 1, 4, 2, 3, 0], 2, async (n) => {
      running++;
      peak = Math.max(peak, running);
      await new Promise((r) => setTimeout(r, n));
      running--;
      return n * 2;
    });
    expect(out).toEqual([10, 2, 8, 4, 6, 0]);
    expect(peak).toBe(2);
  });
});

describe('DownloadsStore: daemon unavailable', () => {
  it('marks the list stale, zeroes the live total and reloads after reconnecting', async () => {
    const api = fakeApi([
      makeDownload({ id: 1, state: 'downloading', speedBps: 5 * MiB, received: 1 * MiB }),
      makeDownload({ id: 2, state: 'paused' }),
    ]);
    const store = await started(api);
    await store.refreshStatus();
    expect(store.stale).toBe(false);
    expect(store.totals.speed).toBe(5 * MiB);

    api.down = true;
    await store.refreshStatus();
    expect(store.daemon).toMatchObject({ ok: false, error: expect.stringContaining('connection refused') });
    expect(store.stale).toBe(true);
    expect(store.totals.speed).toBe(0);
    expect(store.all).toHaveLength(2); // the last known list stays visible

    api.down = false;
    api.items.delete(2);
    api.calls.length = 0;
    await store.refreshStatus();
    expect(store.stale).toBe(false);
    expect(api.calls).toEqual(['status', 'list']);
    expect(store.all.map((d) => d.id)).toEqual([1]);
  });

  it('is stale when the first load fails, and recovers on the next check', async () => {
    const api = fakeApi([makeDownload({ id: 1 })]);
    api.down = true;
    const store = await started(api);
    expect(store.ready).toBe(false);
    expect(store.loadError).toContain('connection refused');
    expect(store.stale).toBe(true);
    api.down = false;
    await store.retryNow();
    expect(store.ready).toBe(true);
    expect(store.stale).toBe(false);
  });

  it('schedules the next check and lets "Retry now" check at once', async () => {
    vi.useFakeTimers();
    try {
      let t = 10_000;
      const api = fakeApi([]);
      api.down = true;
      const store = new DownloadsStore(api, () => t);
      disposers.push(() => store.dispose());
      store.startStatusPolling(3000);
      await vi.advanceTimersByTimeAsync(0);
      expect(store.daemon?.ok).toBe(false);
      expect(store.nextCheckAt).toBe(13_000);
      const before = api.calls.filter((c) => c === 'status').length;
      t = 11_000;
      api.down = false;
      await store.retryNow();
      expect(api.calls.filter((c) => c === 'status').length).toBe(before + 1);
      expect(store.daemon?.ok).toBe(true);
      expect(store.nextCheckAt).toBe(14_000);
      // the earlier timer was replaced, not doubled
      await vi.advanceTimersByTimeAsync(3000);
      expect(api.calls.filter((c) => c === 'status').length).toBe(before + 2);
    } finally {
      vi.useRealTimers();
    }
  });
});

describe('DownloadsStore: keyboard position after removing', () => {
  const list = Array.from({ length: 10 }, (_, i) => makeDownload({ id: i + 1, createdAt: 1000 - i }));

  it('selects the next surviving row, else the previous one', async () => {
    const store = await started(fakeApi(list));
    // default sort: added, newest first → ids 1..10 in order
    expect(store.visibleIds).toEqual([1, 2, 3, 4, 5, 6, 7, 8, 9, 10]);
    store.click(4);
    store.click(6, { shift: true });
    await store.remove([4, 5, 6], false);
    expect([...store.selection.ids]).toEqual([7]);
    expect(store.selection.focus).toBe(7);

    store.click(10);
    await store.remove([10], false);
    expect(store.selection.focus).toBe(9);
  });

  it('leaves the selection alone when some removals fail', async () => {
    const api = fakeApi(list);
    const store = await started(api);
    store.click(2);
    store.click(3, { toggle: true });
    api.items.delete(3); // the daemon no longer knows it: removing fails
    const outcomes = await store.remove([2, 3], false);
    expect(outcomes.map((o) => o.ok)).toEqual([true, false]);
    expect([...store.selection.ids]).toEqual([3]);
  });
});

describe('DownloadsStore: bulk responses', () => {
  it('coalesces revisions from a bulk action into animation frames', async () => {
    const api = fakeApi(Array.from({ length: 40 }, (_, i) => makeDownload({ id: i + 1, state: 'downloading' })));
    const store = await started(api);
    const raf = vi.spyOn(globalThis, 'requestAnimationFrame');
    const outcomes = await store.pause(store.visibleIds);
    expect(outcomes).toHaveLength(40);
    expect(raf.mock.calls.length).toBeLessThanOrEqual(2);
    // settled synchronously when the action resolves
    expect(store.visible.every((d) => d.state === 'paused')).toBe(true);
  });
});
