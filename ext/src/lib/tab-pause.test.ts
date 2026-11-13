import { describe, expect, it } from 'vitest';
import type { StorageAreaLike } from './history';
import { MAX_PAUSED_TABS, PAUSED_TABS_STORAGE_KEY, PausedTabs, parsePausedTabs } from './tab-pause';

function memoryArea(initial: Record<string, unknown> = {}, opts: { failSet?: () => boolean; failGet?: boolean } = {}) {
  const data: Record<string, unknown> = { ...initial };
  const writes: unknown[] = [];
  const area: StorageAreaLike = {
    get: async (key) => {
      if (opts.failGet) throw new Error('storage unavailable');
      return key in data ? { [key]: data[key] } : {};
    },
    set: async (items) => {
      if (opts.failSet?.()) throw new Error('quota');
      Object.assign(data, items);
      writes.push(items[PAUSED_TABS_STORAGE_KEY]);
    },
  };
  return { area, data, writes };
}

describe('parsePausedTabs', () => {
  it('keeps unique tab IDs only', () => {
    expect(parsePausedTabs([3, 3, -1, 1.5, '4', 7])).toEqual([3, 7]);
    expect(parsePausedTabs('x')).toEqual([]);
    expect(parsePausedTabs(Array.from({ length: MAX_PAUSED_TABS + 5 }, (_, i) => i))).toHaveLength(MAX_PAUSED_TABS);
  });
});

describe('PausedTabs', () => {
  it('loads the stored tabs and answers synchronously once ready', async () => {
    const { area } = memoryArea({ [PAUSED_TABS_STORAGE_KEY]: [5] });
    const tabs = new PausedTabs(area);
    expect(tabs.loaded).toBe(false);
    await tabs.ready;
    expect(tabs.loaded).toBe(true);
    expect(tabs.isPaused(5)).toBe(true);
    expect(tabs.isPaused(6)).toBe(false);
    expect(tabs.isPaused(undefined)).toBe(false);
  });

  it('pauses and resumes a tab and stores the change', async () => {
    const { area, data, writes } = memoryArea();
    const tabs = new PausedTabs(area);
    await tabs.set(4, true);
    expect(tabs.isPaused(4)).toBe(true);
    expect(data[PAUSED_TABS_STORAGE_KEY]).toEqual([4]);
    await tabs.set(4, true); // no change: no write
    await tabs.set(4, false);
    expect(tabs.isPaused(4)).toBe(false);
    expect(writes).toEqual([[4], []]);
  });

  it('applies changes made before the store was read on top of it, in order', async () => {
    const { area, data } = memoryArea({ [PAUSED_TABS_STORAGE_KEY]: [1, 2] });
    const tabs = new PausedTabs(area);
    const a = tabs.set(1, false);
    const b = tabs.set(3, true);
    const c = tabs.set(3, false);
    await Promise.all([a, b, c]);
    expect(data[PAUSED_TABS_STORAGE_KEY]).toEqual([2]);
  });

  it('forgets closed tabs, follows replaced tabs and clears', async () => {
    const { area, data } = memoryArea({ [PAUSED_TABS_STORAGE_KEY]: [1, 2] });
    const tabs = new PausedTabs(area);
    await tabs.forget(1);
    await tabs.forget(99);
    expect(data[PAUSED_TABS_STORAGE_KEY]).toEqual([2]);
    await tabs.replace(2, 20);
    expect(tabs.isPaused(2)).toBe(false);
    expect(tabs.isPaused(20)).toBe(true);
    await tabs.replace(7, 70);
    expect(tabs.isPaused(70)).toBe(false);
    await tabs.clear();
    expect(data[PAUSED_TABS_STORAGE_KEY]).toEqual([]);
  });

  it('keeps working in memory when storage fails', async () => {
    let fail = true;
    const { area } = memoryArea({}, { failSet: () => fail });
    const tabs = new PausedTabs(area);
    await expect(tabs.set(8, true)).rejects.toThrow('quota');
    expect(tabs.isPaused(8)).toBe(true);
    fail = false;
    await expect(tabs.set(9, true)).resolves.toBeUndefined();

    const unreadable = new PausedTabs(memoryArea({}, { failGet: true }).area);
    await unreadable.ready;
    expect(unreadable.loaded).toBe(true);
    expect(unreadable.isPaused(1)).toBe(false);
  });

  it('ignores invalid tab IDs', async () => {
    const { area, writes } = memoryArea();
    const tabs = new PausedTabs(area);
    await tabs.set(-1, true);
    await tabs.set(1.5, true);
    expect(writes).toEqual([]);
  });

  it('pauses a tab opened from a paused tab (a link that opens a new tab)', async () => {
    const { area, data } = memoryArea({ [PAUSED_TABS_STORAGE_KEY]: [5] });
    const tabs = new PausedTabs(area);
    const opened = tabs.inherit(5, 9); // before the stored tabs were read
    await opened;
    expect(tabs.isPaused(9)).toBe(true);
    expect(data[PAUSED_TABS_STORAGE_KEY]).toEqual([5, 9]);
    await tabs.inherit(6, 10);
    await tabs.inherit(9, -1);
    expect(tabs.isPaused(10)).toBe(false);
    expect(data[PAUSED_TABS_STORAGE_KEY]).toEqual([5, 9]);
  });
});

