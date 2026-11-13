import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  CREATED_FALLBACK_DELAY_MS,
  type ChromiumCaptureDeps,
  DownloadClaims,
  candidateFromItem,
  createCreatedFallbackListener,
  createDeterminingFilenameListener,
  type DownloadItemLike,
  type DownloadsControl,
  HANDOFF_BACKSTOP_SLACK_MS,
  pausedHandOff,
  requireDaemon,
} from './capture-chromium';
import { failure, type Outcome } from './protocol';
import type { CaptureDecision } from './rules';

function item(over: Partial<DownloadItemLike> = {}): DownloadItemLike {
  return {
    id: 7,
    url: 'https://a.example/f.zip',
    filename: 'f.zip',
    incognito: false,
    state: 'in_progress',
    ...over,
  };
}

function fakeDownloads(opts: { pauseFails?: boolean; pauseMs?: number; cancelFails?: boolean } = {}) {
  const calls: string[] = [];
  const api: DownloadsControl = {
    pause: vi.fn(async (id: number) => {
      calls.push(`pause:${id}`);
      if (opts.pauseMs) await new Promise((r) => setTimeout(r, opts.pauseMs));
      if (opts.pauseFails) throw new Error('Download must be in progress');
    }),
    resume: vi.fn(async (id: number) => {
      calls.push(`resume:${id}`);
    }),
    cancel: vi.fn(async (id: number) => {
      calls.push(`cancel:${id}`);
      if (opts.cancelFails) throw new Error('Download is complete');
    }),
    erase: vi.fn(async (q: { id: number }) => {
      calls.push(`erase:${q.id}`);
      return [q.id];
    }),
  };
  return { api, calls };
}

const CAPTURE: CaptureDecision = { capture: true, extension: 'zip' };
const SKIP: CaptureDecision = { capture: false, reason: 'no_match' };

function setup(over: Partial<ChromiumCaptureDeps> & { handOffImpl?: () => Promise<Outcome<unknown>> } = {}) {
  const { api, calls } = fakeDownloads();
  const suggest = vi.fn(() => {
    calls.push('suggest');
  });
  const results: unknown[] = [];
  const handOff = vi.fn(
    over.handOffImpl ?? (async () => ({ ok: true, value: { type: 'added', downloadId: 1 } }) as Outcome<unknown>),
  );
  const listener = createDeterminingFilenameListener({
    downloads: api,
    decide: () => CAPTURE,
    handOff: async (it, ms) => {
      calls.push(`add:${it.id}:${ms}`);
      return handOff();
    },
    onResult: (_i, r) => results.push(r),
    ...over,
  });
  return { listener, suggest, calls, results, handOff, api };
}

beforeEach(() => {
  vi.useFakeTimers();
});
afterEach(() => {
  vi.useRealTimers();
});

describe('onDeterminingFilename listener', () => {
  it('non-candidate: suggests immediately and synchronously', () => {
    const { listener, suggest, calls } = setup({ decide: () => SKIP });
    const ret = listener(item(), suggest);
    expect(ret).toBeUndefined();
    expect(suggest).toHaveBeenCalledTimes(1);
    expect(calls).toEqual(['suggest']);
  });

  it('candidate: pause → add → cancel + erase, suggest once', async () => {
    const { listener, suggest, calls, results } = setup();
    const ret = listener(item(), suggest);
    expect(ret).toBe(true);
    expect(suggest).not.toHaveBeenCalled();
    await vi.waitFor(() => expect(results).toHaveLength(1));
    expect(calls).toEqual(['pause:7', 'add:7:3000', 'cancel:7', 'erase:7', 'suggest']);
    expect(suggest).toHaveBeenCalledTimes(1);
    expect(results[0]).toEqual({ handedOff: true, value: { type: 'added', downloadId: 1 } });
    await vi.advanceTimersByTimeAsync(10_000);
    expect(suggest).toHaveBeenCalledTimes(1);
  });

  it('relay error: suggest + resume, the browser keeps the download', async () => {
    const { listener, suggest, calls, results } = setup({
      handOffImpl: async () => failure('daemon_unavailable', 'down'),
    });
    expect(listener(item(), suggest)).toBe(true);
    await vi.waitFor(() => expect(results).toHaveLength(1));
    expect(calls).toEqual(['pause:7', 'add:7:3000', 'suggest', 'resume:7']);
    expect(results[0]).toMatchObject({ handedOff: false, reason: 'error', code: 'daemon_unavailable' });
    expect(suggest).toHaveBeenCalledTimes(1);
  });

  it('thrown hand-off error: suggest + resume', async () => {
    const { listener, suggest, calls, results } = setup({
      handOffImpl: async () => {
        throw new Error('boom');
      },
    });
    listener(item(), suggest);
    await vi.waitFor(() => expect(results).toHaveLength(1));
    expect(calls).toEqual(['pause:7', 'add:7:3000', 'suggest', 'resume:7']);
    expect(suggest).toHaveBeenCalledTimes(1);
  });

  it('add timeout (decided by the add itself): suggest + resume', async () => {
    const { listener, suggest, calls, results } = setup({
      handOffImpl: async () => failure('timeout', 'no add response within 3000 ms'),
    });
    listener(item(), suggest);
    await vi.waitFor(() => expect(results).toHaveLength(1));
    expect(calls).toEqual(['pause:7', 'add:7:3000', 'suggest', 'resume:7']);
    expect(results[0]).toMatchObject({ handedOff: false, reason: 'timeout', code: 'timeout' });
  });

  it('the add budget starts after pause: a slow pause does not cut a timely answer short', async () => {
    const { api, calls } = fakeDownloads({ pauseMs: 1000 });
    const suggest = vi.fn(() => calls.push('suggest'));
    const results: unknown[] = [];
    const listener = createDeterminingFilenameListener({
      downloads: api,
      decide: () => CAPTURE,
      handOff: async (_it, ms) => {
        calls.push(`add:${ms}`);
        await new Promise((r) => setTimeout(r, 2500));
        return { ok: true, value: { type: 'added', downloadId: 3 } };
      },
      onResult: (_i, r) => results.push(r),
    });
    listener(item(), suggest);
    // 3.5 s after the event, but within the add's own 3 s budget: Downex has it, the browser does not.
    await vi.advanceTimersByTimeAsync(3500);
    await vi.waitFor(() => expect(results).toHaveLength(1));
    expect(results[0]).toMatchObject({ handedOff: true });
    expect(calls).toEqual(['pause:7', 'add:3000', 'cancel:7', 'erase:7', 'suggest']);
  });

  it('a hand-off that never answers is abandoned after the budget plus slack; a late answer changes nothing', async () => {
    let answer!: (o: Outcome<unknown>) => void;
    const { listener, suggest, calls, results } = setup({
      handOffImpl: () => new Promise((r) => (answer = r)),
    });
    listener(item(), suggest);
    await vi.advanceTimersByTimeAsync(3000 + HANDOFF_BACKSTOP_SLACK_MS - 1);
    expect(suggest).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1);
    await vi.waitFor(() => expect(results).toHaveLength(1));
    expect(calls).toEqual(['pause:7', 'add:7:3000', 'suggest', 'resume:7']);
    expect(results[0]).toMatchObject({ handedOff: false, reason: 'timeout' });
    answer({ ok: true, value: { type: 'added', downloadId: 2 } });
    await vi.advanceTimersByTimeAsync(10);
    expect(calls).toEqual(['pause:7', 'add:7:3000', 'suggest', 'resume:7']);
    expect(suggest).toHaveBeenCalledTimes(1);
  });

  it('a failed pause (download already complete) leaves it to the browser without an add', async () => {
    const { api, calls } = fakeDownloads({ pauseFails: true });
    const suggest = vi.fn(() => calls.push('suggest'));
    const done = vi.fn();
    const handOff = vi.fn(async () => ({ ok: true, value: {} }) as Outcome<unknown>);
    const listener = createDeterminingFilenameListener({ downloads: api, decide: () => CAPTURE, handOff, onResult: done });
    listener(item(), suggest);
    await vi.waitFor(() => expect(done).toHaveBeenCalled());
    expect(handOff).not.toHaveBeenCalled();
    expect(calls).toEqual(['pause:7', 'suggest']);
    expect(done.mock.calls[0]?.[1]).toMatchObject({ handedOff: false, reason: 'error', code: 'not_paused' });
  });

  it('never erases a download whose cancel failed', async () => {
    const { api, calls } = fakeDownloads({ cancelFails: true });
    const r = await pausedHandOff(7, { downloads: api, handOff: async () => ({ ok: true, value: 1 }), timeoutMs: 100 });
    expect(r).toMatchObject({ handedOff: true });
    expect(calls).toEqual(['pause:7', 'cancel:7']);
  });

  it('async decision (cold start): returns true and suggests once for non-candidates', async () => {
    const { listener, suggest, calls } = setup({ decide: () => Promise.resolve(SKIP) });
    expect(listener(item(), suggest)).toBe(true);
    await vi.waitFor(() => expect(suggest).toHaveBeenCalledTimes(1));
    expect(calls).toEqual(['suggest']);
  });

  it('async decision (cold start): candidates are handed off', async () => {
    const { listener, suggest, calls, results } = setup({ decide: () => Promise.resolve(CAPTURE) });
    expect(listener(item(), suggest)).toBe(true);
    await vi.waitFor(() => expect(results).toHaveLength(1));
    expect(calls).toEqual(['pause:7', 'add:7:3000', 'cancel:7', 'erase:7', 'suggest']);
  });

  it('decide throwing or rejecting still suggests exactly once', async () => {
    const a = setup({
      decide: () => {
        throw new Error('bad rules');
      },
    });
    expect(a.listener(item(), a.suggest)).toBeUndefined();
    expect(a.suggest).toHaveBeenCalledTimes(1);

    const b = setup({ decide: () => Promise.reject(new Error('storage')) });
    expect(b.listener(item(), b.suggest)).toBe(true);
    await vi.waitFor(() => expect(b.suggest).toHaveBeenCalledTimes(1));
    expect(b.calls).toEqual(['suggest']);
  });

  it('a throwing suggest() is reported, not retried', async () => {
    const onError = vi.fn();
    const { listener } = setup({ decide: () => SKIP, onError });
    const suggest = vi.fn(() => {
      throw new Error('already suggested');
    });
    listener(item(), suggest);
    expect(suggest).toHaveBeenCalledTimes(1);
    expect(onError).toHaveBeenCalledTimes(1);
  });
});

describe('requireDaemon', () => {
  it('captures only while the daemon is known to run, refreshing otherwise', async () => {
    let ready = false;
    const refresh = vi.fn();
    const gated = requireDaemon((c: string) => (c === 'zip' ? CAPTURE : SKIP), { ready: () => ready, refresh });
    expect(gated('zip')).toEqual({ capture: false, reason: 'daemon_unavailable' });
    expect(refresh).toHaveBeenCalledTimes(1);
    expect(gated('txt')).toEqual(SKIP);
    expect(refresh).toHaveBeenCalledTimes(1);
    ready = true;
    expect(gated('zip')).toEqual(CAPTURE);
    const cold = requireDaemon(() => Promise.resolve(CAPTURE), { ready: () => false, refresh });
    await expect(cold('zip')).resolves.toEqual({ capture: false, reason: 'daemon_unavailable' });
  });

  it('a down daemon leaves the download to the browser at once, without pausing', () => {
    const { listener, suggest, calls } = setup({
      decide: requireDaemon(() => CAPTURE, { ready: () => false, refresh: () => undefined }),
    });
    expect(listener(item(), suggest)).toBeUndefined();
    expect(calls).toEqual(['suggest']);
  });
});

describe('onCreated fallback', () => {
  function fallbackSetup(over: Partial<ChromiumCaptureDeps> = {}) {
    const { api, calls } = fakeDownloads();
    const claims = new DownloadClaims();
    const results: unknown[] = [];
    const decisions: CaptureDecision[] = [];
    const deps = {
      downloads: api,
      decide: () => CAPTURE,
      handOff: async (it: DownloadItemLike, ms: number) => {
        calls.push(`add:${it.id}:${ms}`);
        return { ok: true, value: { type: 'added', downloadId: 1 } } as Outcome<unknown>;
      },
      claims,
      onDecision: (_i: DownloadItemLike, d: CaptureDecision) => decisions.push(d),
      onResult: (_i: DownloadItemLike, r: unknown) => results.push(r),
      ...over,
    };
    return { created: createCreatedFallbackListener(deps), determining: createDeterminingFilenameListener(deps), calls, results, decisions };
  }

  it('captures a download the browser never sent through onDeterminingFilename', async () => {
    const { created, calls, results, decisions } = fallbackSetup();
    created(item());
    await vi.advanceTimersByTimeAsync(CREATED_FALLBACK_DELAY_MS - 1);
    expect(calls).toEqual([]);
    await vi.advanceTimersByTimeAsync(1);
    await vi.waitFor(() => expect(results).toHaveLength(1));
    expect(calls).toEqual(['pause:7', 'add:7:3000', 'cancel:7', 'erase:7']);
    expect(decisions).toEqual([CAPTURE]);
  });

  it('leaves a download alone that onDeterminingFilename already took', async () => {
    const { created, determining, calls, results, decisions } = fallbackSetup();
    const suggest = vi.fn();
    created(item());
    expect(determining(item(), suggest)).toBe(true);
    await vi.waitFor(() => expect(results).toHaveLength(1));
    await vi.advanceTimersByTimeAsync(CREATED_FALLBACK_DELAY_MS);
    expect(calls).toEqual(['pause:7', 'add:7:3000', 'cancel:7', 'erase:7']);
    expect(decisions).toEqual([CAPTURE]);
    expect(suggest).toHaveBeenCalledTimes(1);
  });

  it('a late onDeterminingFilename only lets the browser name the file the fallback took', async () => {
    const { created, determining, calls, results } = fallbackSetup();
    created(item());
    await vi.advanceTimersByTimeAsync(CREATED_FALLBACK_DELAY_MS);
    const suggest = vi.fn();
    expect(determining(item(), suggest)).toBeUndefined();
    expect(suggest).toHaveBeenCalledTimes(1);
    await vi.waitFor(() => expect(results).toHaveLength(1));
    expect(calls.filter((c) => c.startsWith('add'))).toEqual(['add:7:3000']);
  });

  it('reports skip decisions and hands nothing off', async () => {
    const { created, calls, decisions } = fallbackSetup({ decide: () => SKIP });
    created(item());
    await vi.advanceTimersByTimeAsync(CREATED_FALLBACK_DELAY_MS);
    await vi.waitFor(() => expect(decisions).toEqual([SKIP]));
    expect(calls).toEqual([]);
  });
});

describe('DownloadClaims', () => {
  it('claims an ID once until it expires', () => {
    let now = 0;
    const claims = new DownloadClaims(1000, () => now);
    expect(claims.claim(1)).toBe(true);
    expect(claims.claim(1)).toBe(false);
    expect(claims.claim(2)).toBe(true);
    now = 1001;
    expect(claims.claim(1)).toBe(true);
  });
});

describe('pausedHandOff', () => {
  it('release is called exactly once even when the backstop races success', async () => {
    let answer!: (o: Outcome<unknown>) => void;
    const { api, calls } = fakeDownloads();
    const release = vi.fn();
    const p = pausedHandOff(1, {
      downloads: api,
      handOff: () => new Promise((r) => (answer = r)),
      timeoutMs: 100,
      release,
    });
    await vi.advanceTimersByTimeAsync(100 + HANDOFF_BACKSTOP_SLACK_MS);
    answer({ ok: true, value: null });
    await expect(p).resolves.toMatchObject({ handedOff: false, reason: 'timeout' });
    expect(release).toHaveBeenCalledTimes(1);
    expect(calls).toEqual(['pause:1', 'resume:1']);
  });
});

describe('candidateFromItem', () => {
  it('maps the item, its known size and the page it was started on', () => {
    expect(
      candidateFromItem(
        item({ finalUrl: 'https://cdn.example/f.zip', mime: 'application/zip', totalBytes: 0, fileSize: 9, referrer: 'https://site.example/p' }),
      ),
    ).toEqual({
      url: 'https://a.example/f.zip',
      finalUrl: 'https://cdn.example/f.zip',
      filename: 'f.zip',
      mime: 'application/zip',
      size: 9,
      incognito: false,
      pageUrl: 'https://site.example/p',
    });
    expect(candidateFromItem(item({ totalBytes: -1 }))).toEqual({ url: 'https://a.example/f.zip', filename: 'f.zip', incognito: false });
  });
});