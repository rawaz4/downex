import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { HandoffHistory, type StorageAreaLike } from './history';
import { failure, type Outcome, type RescuedResult } from './protocol';
import type { RescueItem } from './rescue';
import { RESCUE_HIDDEN_KEY, RescueService, type RescueServiceDeps } from './rescue-service';

class MemoryArea implements StorageAreaLike {
  data: Record<string, unknown> = {};
  async get(key: string) {
    return key in this.data ? { [key]: structuredClone(this.data[key]) } : {};
  }
  async set(items: Record<string, unknown>) {
    Object.assign(this.data, structuredClone(items));
  }
}

const failed = (id: number, o: Partial<RescueItem> = {}): RescueItem => ({
  id,
  url: `https://cdn.example/f${id}.bin`,
  filename: `/home/u/Downloads/f${id}.bin`,
  state: 'interrupted',
  error: 'NETWORK_FAILED',
  bytesReceived: 2_000_000,
  totalBytes: 5_000_000,
  ...o,
});

function service(o: { features?: string[]; isFirefox?: boolean; notify?: boolean; permitted?: boolean; rescue?: Outcome<RescuedResult> } = {}) {
  let items: RescueItem[] = [failed(1), failed(2)];
  const shown: { id: string; title: string; message: string; button: boolean }[] = [];
  const session = new MemoryArea();
  const history = new HandoffHistory(session);
  const deps: RescueServiceDeps = {
    isFirefox: o.isFirefox ?? false,
    isMac: false,
    downloads: {
      search: async ({ id }) => items.filter((i) => i.id === id),
      cancel: async () => undefined,
      erase: async ({ id }) => {
        items = items.filter((i) => i.id !== id);
      },
    },
    interrupted: async () => items,
    features: () => o.features ?? ['video', 'rescue'],
    session,
    history,
    client: {
      rescue: async () => o.rescue ?? { ok: true, value: { type: 'rescued', downloadId: 57, keptBytes: 2_000_000 } },
      resume: async (id) => ({ ok: true, value: { type: 'resumed', downloadId: id } }),
      add: async () => ({ ok: true, value: { type: 'added', downloadId: 58 } }),
    },
    fields: async (i) => ({ url: i.url, headers: [], target: i.filename, bytesReceived: i.bytesReceived ?? 0, startPaused: true }),
    addFields: async (i) => ({ ok: true, value: { url: i.url, method: 'GET', headers: [] } }),
    options: async () => ({ rescueNotify: o.notify ?? true }),
    notifications: {
      permitted: async () => o.permitted ?? true,
      show: async (id, title, message, button) => {
        shown.push({ id, title, message, button });
      },
    },
  };
  const svc = new RescueService(deps);
  return { svc, shown, session, history, setItems: (x: RescueItem[]) => (items = x) };
}

beforeEach(() => {
  vi.useFakeTimers();
});
afterEach(() => {
  vi.useRealTimers();
});

describe('RescueService (BROWSER-SPEC §6)', () => {
  it('lists nothing for a relay without the rescue feature', async () => {
    const { svc } = service({ features: ['video'] });
    await expect(svc.list()).resolves.toEqual({ available: false, items: [] });
  });

  it('lists the offered downloads, holding a Chromium one back for 3 s after it stopped', async () => {
    const { svc } = service();
    svc.stopped(2);
    expect((await svc.list()).items.map((r) => r.downloadId)).toEqual([1]);
    await vi.advanceTimersByTimeAsync(3_000);
    expect((await svc.list()).items.map((r) => r.downloadId)).toEqual([1, 2]);
  });

  it('hides a row for the session', async () => {
    const { svc, session } = service();
    await svc.hide(1);
    expect(session.data[RESCUE_HIDDEN_KEY]).toEqual([1]);
    expect((await svc.list()).items.map((r) => r.downloadId)).toEqual([2]);
  });

  it('finishes one: the row shows the result, then leaves; the history says Finished in Downex', async () => {
    const { svc, history } = service();
    const row = await svc.finish(1);
    expect(row).toMatchObject({ downloadId: 1, state: { phase: 'done', keptBytes: 2_000_000, resumed: true } });
    expect((await svc.list()).items.map((r) => [r.downloadId, r.state?.phase])).toEqual([
      [1, 'done'],
      [2, undefined],
    ]);
    await vi.advanceTimersByTimeAsync(5_001);
    expect((await svc.list()).items.map((r) => r.downloadId)).toEqual([2]);
    const [rec] = await history.list();
    expect(rec).toMatchObject({ source: 'rescue', ok: true, downloadId: 57, keptBytes: 2_000_000, filename: 'f1.bin' });
  });

  it('a refusal stays on the row with its text and is recorded', async () => {
    const { svc, history } = service({ rescue: failure('rejected', 'rescue_changed: bytes 0-1 differ', 'rescue_changed') });
    const row = await svc.finish(1);
    expect(row?.state).toEqual({
      phase: 'failed',
      text: 'The file on the server is not the one the browser started, so the saved part cannot be used.',
      retry: 'from-start',
    });
    const [rec] = await history.list();
    expect(rec).toMatchObject({ source: 'rescue', ok: false, error: { reason: 'rescue_changed' } });
  });

  it('announces a failed download once, with the button on Chromium; then the result', async () => {
    const { svc, shown } = service();
    svc.stopped(1);
    svc.stopped(1);
    await vi.advanceTimersByTimeAsync(3_000);
    expect(shown).toEqual([
      { id: 'downex-rescue:1', title: 'Download failed: f1.bin', message: 'Network failed. Downex can finish it from 2.0 MB.', button: true },
    ]);
    await svc.finish(1, false, true);
    expect(shown.at(-1)).toMatchObject({ title: 'Continuing in Downex', message: 'f1.bin from 2.0 MB', button: false });
  });

  it('announces nothing without the option, the permission, or a download that resumed', async () => {
    for (const o of [{ notify: false }, { permitted: false }]) {
      const { svc, shown } = service(o);
      svc.stopped(1);
      await vi.advanceTimersByTimeAsync(3_000);
      expect(shown).toEqual([]);
    }
    const { svc, shown, setItems } = service();
    svc.stopped(1);
    setItems([failed(1, { state: 'in_progress' })]);
    await vi.advanceTimersByTimeAsync(3_000);
    expect(shown).toEqual([]);
  });

  it('Firefox: at once, without a button', async () => {
    const { svc, shown } = service({ isFirefox: true });
    svc.stopped(2);
    await vi.advanceTimersByTimeAsync(0);
    expect(shown).toMatchObject([{ id: 'downex-rescue:2', button: false }]);
  });
});
