import { describe, expect, it } from 'vitest';
import type { Api, Download, EventBatch } from '../api';
import { makeDownload } from '../testing';
import { DownloadsStore } from './downloads.svelte';
import { ProgressStore } from './progress.svelte';

// The ordered races of ReportOrder (ui/state order_test.go): downexd changes queuePosition
// without moving updatedAt, so a report with the same updatedAt as a newer one must not
// replace it.

function queued(id: number, queuePosition: number, updatedAt = 100): Download {
  return makeDownload({ id, state: 'queued', queuePosition, updatedAt });
}

/** A promise the test settles by hand. */
function deferred<T>() {
  let resolve!: (v: T) => void;
  const promise = new Promise<T>((r) => (resolve = r));
  return { promise, resolve };
}

/** An Api whose events the test pushes by hand and whose calls answer what the test gives. */
function handApi(calls: Record<string, (...args: unknown[]) => Promise<unknown>>) {
  let listener: ((b: EventBatch) => void) | null = null;
  const api = {
    kind: 'mock',
    onEvents: (cb: (b: EventBatch) => void) => {
      listener = cb;
      return () => (listener = null);
    },
    ...calls,
  } as unknown as Api;
  return { api, emit: (b: EventBatch) => listener?.(b) };
}

function positions(store: DownloadsStore, ...ids: number[]): number[] {
  return ids.map((id) => store.get(id)?.queuePosition ?? -1);
}

describe('ReportOrder: a report never replaces a newer one with the same updatedAt', () => {
  it('a list sent before an event keeps the event, a download added and one removed meanwhile', async () => {
    let list = deferred<Download[]>();
    let lists = 0;
    const { api, emit } = handApi({
      list: () => (lists++ === 0 ? Promise.resolve([queued(1, 1), queued(2, 2), queued(3, 3)]) : list.promise),
    });
    const store = new DownloadsStore(api);
    await store.start();
    const reload = store.reload(); // after a reconnect: List is sent before the move
    emit([{ kind: 'updated', download: queued(2, 1) }]);
    emit([{ kind: 'updated', download: queued(1, 2) }]);
    emit([{ kind: 'updated', download: queued(4, 4) }, { kind: 'removed', id: 3 }]);
    list.resolve([queued(1, 1), queued(2, 2), queued(3, 3)]);
    await reload;
    expect(positions(store, 1, 2, 3, 4)).toEqual([2, 1, -1, 4]);

    // A list sent after the events is the newer report.
    list = deferred<Download[]>();
    const later = store.reload();
    list.resolve([queued(1, 1), queued(2, 2), queued(3, 3)]);
    await later;
    expect(positions(store, 1, 2, 3, 4)).toEqual([1, 2, 3, -1]);
    // A newer updatedAt still wins whatever the order.
    emit([{ kind: 'updated', download: queued(1, 7, 50) }]);
    expect(positions(store, 1)).toEqual([1]);
    store.dispose();
  });

  it('an answer sent before an event keeps the event; one sent after it wins', async () => {
    let answer = deferred<Download>();
    const { api, emit } = handApi({
      list: () => Promise.resolve([makeDownload({ id: 1, state: 'downloading', queuePosition: 0, updatedAt: 100 })]),
      pause: () => answer.promise,
    });
    const store = new DownloadsStore(api);
    await store.start();
    const paused = store.pause([1]);
    emit([{ kind: 'updated', download: queued(1, 2) }]); // learned before the answer
    answer.resolve(queued(1, 1));
    await paused;
    expect(positions(store, 1)).toEqual([2]);

    emit([{ kind: 'updated', download: makeDownload({ id: 1, state: 'downloading', updatedAt: 100 }) }]);
    answer = deferred<Download>();
    const again = store.pause([1]);
    answer.resolve(queued(1, 1));
    await again;
    expect(positions(store, 1)).toEqual([1]);
    store.dispose();
  });

  it('a late snapshot keeps a newer answer; a reconnect snapshot replaces older events', async () => {
    const { api, emit } = handApi({
      list: () => Promise.resolve([makeDownload({ id: 1, state: 'downloading', updatedAt: 100 })]),
      pause: () => Promise.resolve(queued(1, 2)),
    });
    const store = new DownloadsStore(api);
    await store.start();
    emit([{ kind: 'updated', download: makeDownload({ id: 1, state: 'downloading', updatedAt: 100 }) }]);
    await store.pause([1]); // sent after the stream's last batch
    emit([{ kind: 'snapshot', download: queued(1, 1) }]); // taken before it
    expect(positions(store, 1)).toEqual([2]);
    emit([{ kind: 'updated', download: queued(1, 5) }]);
    emit([{ kind: 'snapshot', download: queued(1, 3) }]);
    expect(positions(store, 1)).toEqual([3]);
    emit([{ kind: 'updated', download: queued(1, 4) }]);
    expect(positions(store, 1)).toEqual([4]);
    store.dispose();
  });

  it('an answer sent before a removal does not bring the download back', async () => {
    const answer = deferred<Download>();
    const { api, emit } = handApi({
      list: () => Promise.resolve([makeDownload({ id: 1, state: 'downloading', updatedAt: 100 })]),
      pause: () => answer.promise,
    });
    const store = new DownloadsStore(api);
    await store.start();
    const paused = store.pause([1]);
    emit([{ kind: 'removed', id: 1 }]);
    answer.resolve(makeDownload({ id: 1, state: 'paused', updatedAt: 101 }));
    await paused;
    expect(store.get(1)).toBeUndefined();
    store.dispose();
  });

  it("a progress window's read sent before an event keeps the event", async () => {
    let read = deferred<Download>();
    let reads = 0;
    const { api, emit } = handApi({
      get: () => (reads++ === 0 ? Promise.resolve(queued(4, 1)) : read.promise),
      getSettings: () => Promise.reject(new Error('unavailable: no settings in this test')),
      setDownloadOptions: () => Promise.resolve({ ...queued(4, 1), completionDialog: true }),
      status: () => Promise.resolve({}),
    });
    const store = new ProgressStore(api, 4);
    await store.start(1e9);
    const refresh = store.refresh();
    emit([{ kind: 'updated', download: { ...queued(4, 2), completionDialog: true } }]);
    read.resolve({ ...queued(4, 1), completionDialog: true });
    await refresh;
    expect(store.download?.queuePosition).toBe(2);
    read = deferred<Download>();
    store.dispose();
  });
});
