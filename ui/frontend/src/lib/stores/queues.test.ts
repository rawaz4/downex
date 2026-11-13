import { describe, expect, it, vi } from 'vitest';
import { ApiError, EMPTY_SCHEDULE } from '../api';
import { fakeApi, makeDownload, makeQueue } from '../testing';
import { POST_ACTION_GRACE_MS, QueuesStore } from './queues.svelte';

function setup() {
  const api = fakeApi([makeDownload({ id: 1 }), makeDownload({ id: 2 }), makeDownload({ id: 3 })]);
  api.queues.set(2, makeQueue({ id: 2, name: 'Night' }));
  const store = new QueuesStore(api);
  store.start();
  return { api, store };
}

const spec = { name: 'Weekend', maxConcurrent: 1, speedLimit: 0, schedule: EMPTY_SCHEDULE, postAction: 'none' as const };

describe('QueuesStore', () => {
  it('reads the queues, the main queue first, and follows their events', async () => {
    const { api, store } = setup();
    api.queues.set(5, makeQueue({ id: 5, name: 'Later' }));
    expect(await store.load()).toBe(true);
    expect(store.list.map((q) => q.name)).toEqual(['Main queue', 'Night', 'Later']);
    expect(store.loaded).toBe(true);

    api.emit([{ kind: 'queue_updated', queue: makeQueue({ id: 2, name: 'Nightly', running: true }) }, { kind: 'queue_removed', id: 5 }]);
    expect(store.get(2)).toMatchObject({ name: 'Nightly', running: true });
    expect(store.get(5)).toBeUndefined();
    expect(store.ids).toEqual(new Set([1, 2]));
    expect(store.nameOf(9)).toBe('Queue 9');
  });

  it('keeps what events said while a read was in flight', async () => {
    const { api, store } = setup();
    let release: () => void = () => {};
    const gate = new Promise<void>((r) => (release = r));
    const list = api.listQueues;
    api.listQueues = async () => {
      const result = await list();
      await gate;
      return result;
    };
    const loading = store.load();
    // Meanwhile Night started and a new queue appeared.
    api.emit([{ kind: 'queue_updated', queue: makeQueue({ id: 2, name: 'Night', running: true }) }]);
    api.emit([{ kind: 'queue_updated', queue: makeQueue({ id: 7, name: 'New' }) }]);
    release();
    await loading;
    expect(store.get(2)?.running).toBe(true);
    expect(store.get(7)?.name).toBe('New');
  });

  it('reads again after a re-subscription, not after the first snapshot', async () => {
    const { api, store } = setup();
    await store.load();
    const reads = () => api.calls.filter((c) => c === 'listQueues').length;
    api.emit([{ kind: 'snapshot', download: makeDownload({ id: 1 }) }]);
    expect(reads()).toBe(1);
    api.queues.delete(2); // removed while the stream was down
    api.emit([{ kind: 'snapshot', download: makeDownload({ id: 1 }) }]);
    await Promise.resolve();
    await Promise.resolve();
    expect(reads()).toBe(2);
    await expect.poll(() => store.get(2)).toBeUndefined();
  });

  it('shows no queues for a daemon without them, and reports other failures', async () => {
    const { api, store } = setup();
    api.listQueues = async () => {
      throw new ApiError('unimplemented', 'unknown procedure');
    };
    expect(await store.load()).toBe(true);
    expect(store).toMatchObject({ supported: false, loaded: true });
    expect(store.list).toEqual([]);

    const other = setup();
    other.api.down = true;
    expect(await other.store.load()).toBe(false);
    expect(other.store.loaded).toBe(false);
    expect(other.store.loadError).toMatch(/connection refused/);
  });

  it('keeps countdowns by queue, the first deadline first, and drops cancelled ones', async () => {
    const { api, store } = setup();
    await store.load();
    const now = Date.now();
    api.emit([
      { kind: 'post_action', pending: { queueId: 2, action: 'shutdown', deadline: now + 60_000, cancelled: false } },
      { kind: 'post_action', pending: { queueId: 1, action: 'sleep', deadline: now + 30_000, cancelled: false } },
    ]);
    expect(store.nextAction).toMatchObject({ queueId: 1, action: 'sleep' });
    api.emit([{ kind: 'post_action', pending: { queueId: 1, action: 'sleep', deadline: now + 30_000, cancelled: true } }]);
    expect(store.nextAction).toMatchObject({ queueId: 2 });
    expect(await store.cancelPostAction(2)).toBe(true);
    expect(api.args.get('cancelPostAction')).toEqual([2]);
    expect(store.nextAction).toBeNull();
    // A countdown of a deleted queue goes with it.
    api.emit([{ kind: 'post_action', pending: { queueId: 2, action: 'quit_downex', deadline: Date.now() + 60_000, cancelled: false } }]);
    api.emit([{ kind: 'queue_removed', id: 2 }]);
    expect(store.pending.size).toBe(0);
    store.dispose();
  });

  it('keeps countdowns while one status check fails: the stream may still be up', async () => {
    const { api, store } = setup();
    await store.load();
    api.emit([{ kind: 'post_action', pending: { queueId: 1, action: 'shutdown', deadline: Date.now() + 60_000, cancelled: false } }]);
    store.invalidate();
    expect(store.loaded).toBe(false);
    expect(store.nextAction).toMatchObject({ queueId: 1, action: 'shutdown' });
    store.dispose();
  });

  it('takes the countdowns of a new subscription from its snapshot', async () => {
    const { api, store } = setup();
    await store.load();
    const soon = Date.now() + 60_000;
    api.emit([{ kind: 'snapshot', download: makeDownload({ id: 1 }) }]);
    api.emit([
      { kind: 'post_action', pending: { queueId: 1, action: 'sleep', deadline: soon, cancelled: false } },
      { kind: 'post_action', pending: { queueId: 2, action: 'shutdown', deadline: soon, cancelled: false } },
    ]);
    expect(store.pending.size).toBe(2);
    // The stream was down meanwhile: Main's countdown was cancelled elsewhere, Night's runs on.
    api.emit([
      { kind: 'snapshot', download: makeDownload({ id: 1 }) },
      { kind: 'queue_updated', queue: makeQueue({ id: 2, name: 'Night' }) },
      { kind: 'post_action', pending: { queueId: 2, action: 'shutdown', deadline: soon, cancelled: false } },
    ]);
    expect([...store.pending.keys()]).toEqual([2]);
    store.dispose();
  });

  it('drops a countdown shortly after its deadline: the action ran', async () => {
    vi.useFakeTimers();
    try {
      const { api, store } = setup();
      await store.load();
      api.emit([
        { kind: 'post_action', pending: { queueId: 2, action: 'sleep', deadline: Date.now() + 60_000, cancelled: false } },
        { kind: 'post_action', pending: { queueId: 1, action: 'shutdown', deadline: Date.now() + 90_000, cancelled: false } },
      ]);
      vi.advanceTimersByTime(60_000);
      expect(store.nextAction).toMatchObject({ queueId: 2 }); // "Going to sleep now…" for a moment
      vi.advanceTimersByTime(POST_ACTION_GRACE_MS);
      expect(store.nextAction).toMatchObject({ queueId: 1 });
      vi.advanceTimersByTime(30_000 + POST_ACTION_GRACE_MS);
      expect(store.nextAction).toBeNull();
      // One without a deadline gets the usual 60 s.
      api.emit([{ kind: 'post_action', pending: { queueId: 1, action: 'quit_downex', deadline: 0, cancelled: false } }]);
      vi.advanceTimersByTime(60_000);
      expect(store.nextAction).not.toBeNull();
      vi.advanceTimersByTime(POST_ACTION_GRACE_MS);
      expect(store.nextAction).toBeNull();
      store.dispose();
    } finally {
      vi.useRealTimers();
    }
  });

  it('treats a countdown downexd no longer has as gone when cancelling it', async () => {
    const { api, store } = setup();
    await store.load();
    const soon = Date.now() + 60_000;
    api.emit([
      { kind: 'post_action', pending: { queueId: 1, action: 'sleep', deadline: soon, cancelled: false } },
      { kind: 'post_action', pending: { queueId: 2, action: 'shutdown', deadline: soon, cancelled: false } },
    ]);
    api.cancelPostAction = async (id) => {
      throw new ApiError(id === 1 ? 'failed_precondition' : 'not_found', id === 1 ? 'queue 1 has no post-queue action pending' : 'queue 2 not found');
    };
    expect(await store.cancelPostAction(1)).toBe(false);
    expect(await store.cancelPostAction(2)).toBe(false);
    expect(store.pending.size).toBe(0);

    // Other failures keep it: it may still run.
    api.emit([{ kind: 'post_action', pending: { queueId: 1, action: 'sleep', deadline: soon, cancelled: false } }]);
    api.cancelPostAction = async () => {
      throw new ApiError('unavailable', 'connection refused');
    };
    await expect(store.cancelPostAction(1)).rejects.toThrow(/connection refused/);
    expect(store.pending.has(1)).toBe(true);
    api.cancelPostAction = async () => {};
    expect(await store.cancelPostAction(1)).toBe(true);
    expect(store.pending.size).toBe(0);
    store.dispose();
  });

  it('creates, changes, starts, stops, moves into and deletes queues', async () => {
    const { api, store } = setup();
    await store.load();
    const created = await store.create(spec);
    expect(store.get(created.id)?.name).toBe('Weekend');
    await store.update(created.id, { ...spec, name: 'Weekends' });
    expect(store.get(created.id)?.name).toBe('Weekends');
    expect((await store.startQueue(created.id)).running).toBe(true);
    expect(store.get(created.id)?.running).toBe(true);
    await store.stopQueue(created.id);
    expect(store.get(created.id)?.running).toBe(false);

    await store.move([1, 3], created.id, -1);
    expect(store.get(created.id)?.downloadIds).toEqual([1, 3]);
    expect(store.get(1)?.downloadIds).toEqual([2]); // they left the main queue
    expect(api.args.get('moveDownloads')).toEqual([[1, 3], created.id, -1]);

    await store.remove(created.id);
    expect(store.get(created.id)).toBeUndefined();
    expect(store.get(1)?.downloadIds).toEqual([2, 1, 3]);
    await expect(store.remove(1)).rejects.toThrow(/main queue/);
  });

  it('changes only what a queue does when done, keeping the rest of its settings', async () => {
    const { api, store } = setup();
    api.queues.set(2, makeQueue({ id: 2, name: 'Night', maxConcurrent: 2, speedLimit: 4096, postAction: 'none', downloadIds: [3] }));
    await store.load();
    const q = await store.setPostAction(2, 'shutdown');
    expect(api.args.get('updateQueue')).toEqual([2, { name: 'Night', maxConcurrent: 2, speedLimit: 4096, schedule: EMPTY_SCHEDULE, postAction: 'shutdown' }]);
    expect(q.postAction).toBe('shutdown');
    expect(store.get(2)?.postAction).toBe('shutdown');
    expect(store.get(2)?.downloadIds).toEqual([3]);
    // A queue this window does not know is refused as downexd would.
    await expect(store.setPostAction(9, 'sleep')).rejects.toMatchObject({ code: 'not_found' });
  });
});
