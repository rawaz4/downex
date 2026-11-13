import { describe, expect, it, vi } from 'vitest';
import { RequestContextStore, encodeRequestBody } from './context';
import { createHandOff, PREPARE_TIMEOUT_MS } from './handoff';
import { HandoffHistory, type StorageAreaLike } from './history';
import type { AddedResult, AddFields, Outcome, RequestOptions } from './protocol';

function memoryArea(): StorageAreaLike & { data: Record<string, unknown> } {
  const data: Record<string, unknown> = {};
  return {
    data,
    get: async (key) => (key in data ? { [key]: data[key] } : {}),
    set: async (items) => void Object.assign(data, items),
  };
}

function setup(answer: (fields: AddFields, opts?: RequestOptions<AddedResult>) => Promise<Outcome<AddedResult>>) {
  const contexts = new RequestContextStore();
  const area = memoryArea();
  const history = new HandoffHistory(area);
  const sent: { fields: AddFields; opts: RequestOptions<AddedResult> | undefined }[] = [];
  const handOff = createHandOff({
    contexts,
    getCookies: async () => [],
    userAgent: 'UA',
    history,
    client: {
      add: (fields, opts) => {
        sent.push({ fields, opts });
        return answer(fields, opts);
      },
    },
  });
  return { handOff, contexts, sent, history };
}

describe('createHandOff', () => {
  it('never captures a POST: the browser keeps it and nothing is sent', async () => {
    const { handOff, contexts, sent } = setup(async () => ({ ok: true, value: { type: 'added', downloadId: 1 } }));
    contexts.recordBody('https://a/export', encodeRequestBody({ raw: [{ bytes: new TextEncoder().encode('csrf=1').buffer }] }));
    contexts.recordHeaders('https://a/export', 'POST', [['Content-Type', 'application/octet-stream']]);
    const r = await handOff({ url: 'https://a/export' }, { source: 'capture', timeoutMs: 3000 });
    expect(r).toMatchObject({ ok: false, error: { code: 'unsupported' } });
    expect(sent).toHaveLength(0);
    // An explicit menu action may still replay it.
    const m = await handOff({ url: 'https://a/export' }, { source: 'menu' });
    expect(m.ok).toBe(true);
    expect(sent[0]?.fields.method).toBe('POST');
  });

  it('sends the capture deadline and records a late `added` as a duplicate', async () => {
    let late: ((o: Outcome<AddedResult>) => void) | undefined;
    const { handOff, sent, history } = setup(async (_f, opts) => {
      late = opts?.onLate;
      return { ok: false, error: { code: 'timeout', message: 'no add response within 1500 ms' } };
    });
    const r = await handOff({ url: 'https://a/f.zip', filename: 'f.zip' }, { source: 'capture', timeoutMs: 1500 });
    expect(r.ok).toBe(false);
    expect(sent[0]?.fields.deadlineMs).toBe(1500);
    expect(sent[0]?.opts?.timeoutMs).toBe(1500);
    late?.({ ok: true, value: { type: 'added', downloadId: 5 } });
    await vi.waitFor(async () => expect(await history.list()).toHaveLength(2));
    const [newest, first] = await history.list();
    expect(first).toMatchObject({ ok: false, error: { code: 'timeout' } });
    expect(newest).toMatchObject({ ok: true, downloadId: 5, duplicate: true, filename: 'f.zip' });
  });

  it('gives up unsent when the request context is not ready in time', async () => {
    vi.useFakeTimers();
    try {
      const sent: AddFields[] = [];
      let cookies!: (v: []) => void;
      const handOff = createHandOff({
        contexts: new RequestContextStore(),
        getCookies: () => new Promise((r) => (cookies = r)),
        userAgent: 'UA',
        client: { add: async (f) => (sent.push(f), { ok: true, value: { type: 'added', downloadId: 1 } }) },
      });
      const p = handOff({ url: 'https://a/f.zip' }, { source: 'capture', timeoutMs: 3000 });
      await vi.advanceTimersByTimeAsync(PREPARE_TIMEOUT_MS);
      await expect(p).resolves.toMatchObject({ ok: false, error: { code: 'timeout' } });
      cookies([]);
      await vi.advanceTimersByTimeAsync(10);
      expect(sent).toHaveLength(0);
    } finally {
      vi.useRealTimers();
    }
  });
});
