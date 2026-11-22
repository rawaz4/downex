import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  CREATED_FALLBACK_DELAY_MS,
  type ChromiumCaptureDeps,
  DAEMON_CONNECT_WAIT_MS,
  DownloadClaims,
  candidateFromItem,
  createCreatedFallbackListener,
  createDeterminingFilenameListener,
  type DownloadItemLike,
  type DownloadsControl,
  HANDOFF_BACKSTOP_SLACK_MS,
  handOffBudget,
  nativeDaemonGate,
  pausedHandOff,
  reconnectable,
  requireDaemon,
} from './capture-chromium';
import { failure, NativeClient, type NativePort, type Outcome, PROTOCOL_VERSION } from './protocol';
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
    expect(calls).toEqual(['pause:7', 'add:7:3000', 'cancel:7', 'erase:7']);
    expect(suggest).not.toHaveBeenCalled();
    expect(results[0]).toEqual({ handedOff: true, value: { type: 'added', downloadId: 1 } });
    await vi.advanceTimersByTimeAsync(10_000);
    expect(suggest).not.toHaveBeenCalled();
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
    expect(calls).toEqual(['pause:7', 'add:3000', 'cancel:7', 'erase:7']);
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
    // Decided at once: the add keeps its whole budget (waitFor would move the clock first).
    await vi.advanceTimersByTimeAsync(0);
    expect(results).toHaveLength(1);
    expect(calls).toEqual(['pause:7', 'add:7:3000', 'cancel:7', 'erase:7']);
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

describe('requireDaemon with a wait', () => {
  it('waits for a gate that can connect, and refreshes only without one', async () => {
    const refresh = vi.fn();
    let ok = true;
    const connect = vi.fn(async (ms: number) => {
      expect(ms).toBe(DAEMON_CONNECT_WAIT_MS);
      return ok;
    });
    const gated = requireDaemon(() => CAPTURE, { ready: () => false, refresh, connect }, DAEMON_CONNECT_WAIT_MS);
    await expect(gated('zip')).resolves.toEqual(CAPTURE);
    ok = false;
    await expect(gated('zip')).resolves.toEqual({ capture: false, reason: 'daemon_unavailable' });
    expect(refresh).not.toHaveBeenCalled();
    // A gate whose connect fails leaves the download to the browser.
    const broken = requireDaemon(
      () => CAPTURE,
      { ready: () => false, refresh, connect: () => Promise.reject(new Error('gone')) },
      DAEMON_CONNECT_WAIT_MS,
    );
    await expect(broken('zip')).resolves.toEqual({ capture: false, reason: 'daemon_unavailable' });
    // No wait asked for (Firefox): at once, as before.
    expect(requireDaemon(() => CAPTURE, { ready: () => false, refresh, connect })('zip')).toEqual({
      capture: false,
      reason: 'daemon_unavailable',
    });
    expect(refresh).toHaveBeenCalledTimes(1);
    expect(connect).toHaveBeenCalledTimes(2);
    // Nothing to connect to: at once too, refreshed for the next download.
    const unreachable = requireDaemon(() => CAPTURE, { ready: () => false, refresh, connect: () => undefined }, DAEMON_CONNECT_WAIT_MS);
    expect(unreachable('zip')).toEqual({ capture: false, reason: 'daemon_unavailable' });
    expect(refresh).toHaveBeenCalledTimes(2);
  });

  it('takes the wait off the add budget, at most DAEMON_CONNECT_WAIT_MS of it', () => {
    expect(handOffBudget(3000, 0)).toBe(3000);
    expect(handOffBudget(3000, -5)).toBe(3000);
    expect(handOffBudget(3000, 310)).toBe(2690);
    expect(handOffBudget(3000, 5000)).toBe(3000 - DAEMON_CONNECT_WAIT_MS);
  });

  it('reconnects only a relay that is merely not connected', () => {
    const since = 0;
    expect(reconnectable({ state: 'idle', since })).toBe(true);
    expect(reconnectable({ state: 'connecting', since })).toBe(true);
    expect(reconnectable({ state: 'disconnected', lastError: 'Native host has exited.', since })).toBe(true);
    expect(reconnectable({ state: 'disconnected', lastError: HOST_NOT_FOUND, since })).toBe(false);
    expect(reconnectable({ state: 'disconnected', lastError: HOST_FORBIDDEN, since })).toBe(false);
    expect(reconnectable({ state: 'disconnected', lastError: 'relay protocol 2', incompatibleProtocol: 2, since })).toBe(false);
    expect(reconnectable({ state: 'connected', daemonRunning: false, since })).toBe(false);
  });
});

/** The browser's disconnect reasons for a host it cannot start. */
const HOST_NOT_FOUND = 'Specified native messaging host not found.';
const HOST_FORBIDDEN = 'Access to the specified native messaging host is forbidden.';

interface RelayBehaviour {
  /** Delay of the welcome; never answered when undefined. */
  welcomeMs?: number;
  /** The welcome's daemonVersion: '' when downexd is not running. */
  daemonVersion?: string;
  statusMs?: number;
  /** The browser cannot start the host: the port disconnects with this reason. */
  refuse?: string;
}

/** A relay port that answers like downex-nmhost, after the delays it is given. */
class RelayPort implements NativePort {
  readonly #messages: ((m: unknown) => void)[] = [];
  readonly #disconnects: ((p: never) => void)[] = [];
  readonly sent: string[] = [];
  reason: string | undefined;
  readonly onMessage = {
    addListener: (l: (m: unknown) => void) => void this.#messages.push(l),
    removeListener: () => undefined,
  };
  readonly onDisconnect = {
    addListener: (l: (p: never) => void) => void this.#disconnects.push(l),
    removeListener: () => undefined,
  };

  constructor(private readonly behaviour: RelayBehaviour) {
    const { refuse } = behaviour;
    // The browser reports a host it cannot start as a disconnect, after connectNative returned.
    if (refuse !== undefined) setTimeout(() => this.drop(refuse), 5);
  }

  postMessage(m: unknown): void {
    const msg = m as { id: string; type: string };
    this.sent.push(msg.type);
    const running = (this.behaviour.daemonVersion ?? '1.1.2') !== '';
    const answer = (after: number | undefined, body: Record<string, unknown>) => {
      if (after === undefined) return;
      setTimeout(() => {
        if (this.reason !== undefined) return;
        for (const l of this.#messages) l({ id: msg.id, ok: true, ...body });
      }, after);
    };
    if (msg.type === 'hello') {
      answer(this.behaviour.welcomeMs, {
        type: 'welcome',
        protocol: PROTOCOL_VERSION,
        daemonVersion: this.behaviour.daemonVersion ?? '1.1.2',
        features: [],
      });
    } else if (msg.type === 'status') {
      answer(this.behaviour.statusMs ?? 10, { type: 'status', running, active: 0, queued: 0 });
    }
  }

  disconnect(): void {
    this.reason ??= 'disconnected by the extension';
  }

  /** The host side ends the connection. */
  drop(reason = 'Native host has exited.'): void {
    this.reason = reason;
    for (const l of this.#disconnects) l(this as never);
  }
}

describe('a Chromium capture while the relay is not connected', () => {
  function relaySetup(behaviour: RelayBehaviour) {
    const ports: RelayPort[] = [];
    let n = 0;
    const client = new NativeClient({
      connect: () => {
        const p = new RelayPort(behaviour);
        ports.push(p);
        return p;
      },
      hello: () => ({ protocol: PROTOCOL_VERSION, browser: 'chrome', extVersion: '1.1.2' }),
      disconnectReason: (p) => (p as RelayPort).reason,
      newId: () => `r${++n}`,
    });
    const gate = nativeDaemonGate(client, { refreshMs: 2000 });
    const s = setup({ decide: requireDaemon(() => CAPTURE, gate, DAEMON_CONNECT_WAIT_MS) });
    return { client, ports, ...s };
  }

  it('the service worker just started: waits for the welcome and the status, then hands the download off', async () => {
    const { client, ports, listener, suggest, calls, results } = relaySetup({ welcomeMs: 300 });
    client.start();
    expect(listener(item(), suggest)).toBe(true);
    await vi.advanceTimersByTimeAsync(299);
    expect(calls).toEqual([]);
    await vi.advanceTimersByTimeAsync(11);
    await vi.waitFor(() => expect(results).toHaveLength(1));
    // 310 ms of the 3 s went to the relay's start: the add gets what is left.
    expect(calls).toEqual(['pause:7', 'add:7:2690', 'cancel:7', 'erase:7']);
    expect(suggest).not.toHaveBeenCalled();
    expect(ports).toHaveLength(1);
    expect(ports[0]?.sent).toEqual(['hello', 'status']);
    client.stop();
  });

  it('the port dropped: connects again at once instead of after the backoff', async () => {
    const { client, ports, listener, suggest, calls, results } = relaySetup({ welcomeMs: 20 });
    client.start();
    await vi.advanceTimersByTimeAsync(20);
    expect(client.daemonReady).toBe(true);
    ports[0]?.drop();
    expect(client.status).toMatchObject({ state: 'disconnected', lastError: 'Native host has exited.' });
    expect(listener(item(), suggest)).toBe(true);
    await vi.advanceTimersByTimeAsync(30);
    await vi.waitFor(() => expect(results).toHaveLength(1));
    expect(ports).toHaveLength(2);
    expect(calls).toEqual(['pause:7', 'add:7:2970', 'cancel:7', 'erase:7']);
    client.stop();
  });

  it('downloads that wait together share one status request', async () => {
    const { client, ports, listener, results } = relaySetup({ welcomeMs: 100 });
    client.start();
    expect(listener(item({ id: 1 }), vi.fn())).toBe(true);
    expect(listener(item({ id: 2 }), vi.fn())).toBe(true);
    await vi.advanceTimersByTimeAsync(110);
    await vi.waitFor(() => expect(results).toHaveLength(2));
    expect(ports).toHaveLength(1);
    expect(ports[0]?.sent).toEqual(['hello', 'status']);
    client.stop();
  });

  it('no welcome within the wait: the browser keeps the download, never paused', async () => {
    const { client, listener, suggest, calls } = relaySetup({});
    client.start();
    expect(listener(item(), suggest)).toBe(true);
    await vi.advanceTimersByTimeAsync(DAEMON_CONNECT_WAIT_MS - 1);
    expect(suggest).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1);
    await vi.waitFor(() => expect(suggest).toHaveBeenCalledTimes(1));
    expect(calls).toEqual(['suggest']);
    client.stop();
  });

  for (const refuse of [HOST_FORBIDDEN, HOST_NOT_FOUND]) {
    it(`the host cannot be started (${refuse}): the browser keeps the download at once`, async () => {
      const { client, listener, suggest, calls } = relaySetup({ welcomeMs: 1, refuse });
      client.start();
      await vi.advanceTimersByTimeAsync(5);
      expect(client.status.lastError).toBe(refuse);
      expect(listener(item(), suggest)).toBeUndefined();
      expect(calls).toEqual(['suggest']);
      client.stop();
    });
  }

  it('downexd is not running: the browser keeps the download at once', async () => {
    const { client, ports, listener, suggest, calls } = relaySetup({ welcomeMs: 1, daemonVersion: '' });
    client.start();
    await vi.advanceTimersByTimeAsync(1);
    expect(client.status).toMatchObject({ state: 'connected', daemonRunning: false });
    expect(listener(item(), suggest)).toBeUndefined();
    expect(listener(item({ id: 8 }), suggest)).toBeUndefined();
    expect(calls).toEqual(['suggest', 'suggest']);
    // The daemon's state is asked for again for the next download, once per refresh period.
    expect(ports[0]?.sent).toEqual(['hello', 'status']);
    client.stop();
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
    expect(suggest).not.toHaveBeenCalled();
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