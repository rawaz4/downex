// Chromium capture: pause → hand-off → commit via the downloads API (BROWSER-SPEC §3,
// REQUIREMENTS B4). The browser keeps the download on any error or after the timeout.
// Pure: the downloads API, rules and hand-off are injected.

import type { CaptureCandidate, CaptureDecision } from './rules';
import { PREPARE_TIMEOUT_MS } from './handoff';
import {
  type ConnectionStatus,
  type Outcome,
  type StatusResult,
  type Timers,
  errorMessage,
  failure,
  systemTimers,
} from './protocol';
import { isHostForbidden, isHostMissing } from './ui-format';

/** Budget of the `add` request itself, timed by the native client from when it is sent. */
export const CHROMIUM_HANDOFF_TIMEOUT_MS = 3000;
/**
 * A hand-off resolves within PREPARE_TIMEOUT_MS + its add budget. One that has not after
 * this extra time is abandoned; it is later than the add's own timeout, so it never races it.
 */
export const HANDOFF_BACKSTOP_SLACK_MS = PREPARE_TIMEOUT_MS + 1000;
/**
 * How long a Chromium capture waits for a relay that is merely not connected (the MV3 service
 * worker was just started for this download, or the port dropped) to connect again and say that
 * downexd runs, before the browser keeps the download. The wait is taken off the add's budget
 * (handOffBudget), so a hand-off that waited still answers within CHROMIUM_HANDOFF_TIMEOUT_MS
 * of the download's event.
 */
export const DAEMON_CONNECT_WAIT_MS = 800;

/** Whether downexd is known to run, and a way to refresh that knowledge. */
export interface DaemonGate {
  ready(): boolean;
  /** Called when a candidate is left to the browser at once because the daemon is not known to run. */
  refresh(): void;
  /**
   * Connects a relay that is merely not connected (connecting, disconnected or never started)
   * and resolves whether downexd is then known to run, within `ms`; never rejects. Undefined
   * when reconnecting cannot help: the host is missing or does not allow this extension, it
   * speaks another protocol, or it is connected and says downexd is not running.
   */
  connect?(ms: number): Promise<boolean> | undefined;
}

/**
 * Captures only while downexd is known to run. Otherwise the browser keeps the download
 * at once: an add would have to autostart the daemon, which can outlast the hand-off
 * timeout and end with the file downloaded twice. With `waitMs` > 0 and a gate that can
 * connect, a relay that is merely not connected is first given up to `waitMs` to connect
 * and report a running downexd (the decision is then a Promise); one that cannot be reached
 * that way still leaves the download to the browser at once.
 */
export function requireDaemon<C>(
  decide: (c: C) => CaptureDecision | Promise<CaptureDecision>,
  gate: DaemonGate,
  waitMs = 0,
): (c: C) => CaptureDecision | Promise<CaptureDecision> {
  const unavailable = (): CaptureDecision => ({ capture: false, reason: 'daemon_unavailable' });
  const check = (d: CaptureDecision): CaptureDecision | Promise<CaptureDecision> => {
    if (!d.capture || gate.ready()) return d;
    // The wait itself asks for the daemon's status: no refresh on top of it.
    const waiting = waitMs > 0 ? gate.connect?.(waitMs) : undefined;
    if (waiting) {
      return waiting.then(
        (ok) => (ok ? d : unavailable()),
        () => unavailable(),
      );
    }
    gate.refresh();
    return unavailable();
  };
  return (c) => {
    const d = decide(c);
    return d instanceof Promise ? d.then(check) : check(d);
  };
}

/** The native client as the daemon gate sees it (NativeClient). */
export interface DaemonConnection {
  readonly daemonReady: boolean;
  readonly status: ConnectionStatus;
  /** Connects when not connected, waits for the welcome, then asks the relay for the status. */
  queryStatus(options?: { timeoutMs?: number }): Promise<Outcome<StatusResult>>;
}

/**
 * Whether connecting again may reach a running downexd: the relay is not connected, and the
 * last attempt did not end because the browser has no host of that name, the host does not
 * allow this extension, or the host speaks another protocol. Each of those needs Downex
 * installed or updated, which no reconnect within a download's hand-off brings.
 */
export function reconnectable(s: ConnectionStatus): boolean {
  if (s.state === 'connected' || s.incompatibleProtocol !== undefined) return false;
  return !(s.lastError !== undefined && (isHostMissing(s.lastError) || isHostForbidden(s.lastError)));
}

export interface NativeDaemonGateOptions {
  /** Least time between the status refreshes of skipped captures. */
  refreshMs: number;
  now?: () => number;
}

/**
 * The daemon gate of a native client: ready while it is connected and downexd is known to run,
 * refreshed at most once per `refreshMs`, and connected on demand while the relay is merely
 * not connected. Captures that wait at the same time share one status request.
 */
export function nativeDaemonGate(conn: DaemonConnection, opts: NativeDaemonGateOptions): DaemonGate {
  const now = opts.now ?? (() => Date.now());
  let lastRefresh: number | undefined;
  let waiting: Promise<boolean> | undefined;
  const refresh = () => {
    const t = now();
    if (lastRefresh !== undefined && t - lastRefresh < opts.refreshMs) return;
    lastRefresh = t;
    void conn.queryStatus();
  };
  return {
    ready: () => conn.daemonReady,
    refresh,
    connect: (ms) => {
      if (conn.daemonReady) return Promise.resolve(true);
      if (!reconnectable(conn.status)) return undefined;
      waiting ??= conn
        .queryStatus({ timeoutMs: ms })
        .then(
          () => conn.daemonReady,
          () => false,
        )
        .finally(() => {
          waiting = undefined;
        });
      return waiting;
    },
  };
}

/**
 * The add budget left of `timeoutMs` once deciding took `decidedInMs`: the time spent waiting
 * for the daemon (and loading the rules on a cold start) is taken off it, but never more than
 * DAEMON_CONNECT_WAIT_MS, so the add always keeps most of its budget.
 */
export function handOffBudget(timeoutMs: number, decidedInMs: number): number {
  return timeoutMs - Math.min(Math.max(0, decidedInMs), DAEMON_CONNECT_WAIT_MS);
}

/** The downloads.* calls the hand-off uses. */
export interface DownloadsControl {
  pause(id: number): Promise<void>;
  resume(id: number): Promise<void>;
  cancel(id: number): Promise<void>;
  erase(query: { id: number }): Promise<unknown>;
}

/** The DownloadItem fields capture reads (Chromium and Firefox). */
export interface DownloadItemLike {
  id: number;
  url: string;
  finalUrl?: string;
  filename: string;
  mime?: string;
  referrer?: string;
  totalBytes?: number;
  fileSize?: number;
  incognito: boolean;
  state?: string;
  byExtensionId?: string;
  /** Firefox: cookie store (container) of the download. */
  cookieStoreId?: string;
}

export type HandoffResult =
  | { handedOff: true; value: unknown }
  | { handedOff: false; reason: 'timeout' | 'error'; code: string; message: string };

export interface PausedHandoffOptions {
  downloads: DownloadsControl;
  /**
   * Sends the `add` request once the download is paused; must not reject (rejections are
   * treated as errors) and must resolve within `timeoutMs` + HANDOFF_BACKSTOP_SLACK_MS.
   */
  handOff: () => Promise<Outcome<unknown>>;
  /** The add budget the hand-off was given (its own timer decides the timeout). */
  timeoutMs: number;
  timers?: Timers;
  /** Called exactly once when the outcome is decided, before resume on fallback. */
  release?: () => void;
}

async function quietly(p: () => Promise<unknown>): Promise<void> {
  try {
    await p();
  } catch {
    // the download may already be gone or not paused; nothing to undo
  }
}

/** Resolves the hand-off's outcome, or a timeout when it has not settled within `ms`. */
function backstop(handOff: () => Promise<Outcome<unknown>>, ms: number, timers: Timers): Promise<Outcome<unknown>> {
  return new Promise((resolve) => {
    const timer = timers.setTimeout(() => resolve(failure('timeout', `no hand-off answer within ${ms} ms`)), ms);
    const settle = (o: Outcome<unknown>) => {
      timers.clearTimeout(timer);
      resolve(o);
    };
    let p: Promise<Outcome<unknown>>;
    try {
      p = handOff();
    } catch (e) {
      p = Promise.reject(e instanceof Error ? e : new Error(String(e)));
    }
    p.then(settle, (e: unknown) => settle(failure('internal', errorMessage(e))));
  });
}

/**
 * Pauses download `id` and hands it off. Success → cancel + erase the browser download.
 * A download that cannot be paused (e.g. it already completed) is left to the browser and
 * not handed off. Error or timeout → resume it: the user never loses a download.
 */
export async function pausedHandOff(id: number, opts: PausedHandoffOptions): Promise<HandoffResult> {
  const timers = opts.timers ?? systemTimers;
  let released = false;
  const release = () => {
    if (released) return;
    released = true;
    try {
      opts.release?.();
    } catch {
      // the release callback must not break the fallback
    }
  };

  try {
    await opts.downloads.pause(id);
  } catch (e) {
    release();
    return { handedOff: false, reason: 'error', code: 'not_paused', message: `cannot pause the download: ${errorMessage(e)}` };
  }

  const outcome = await backstop(opts.handOff, opts.timeoutMs + HANDOFF_BACKSTOP_SLACK_MS, timers);
  if (!outcome.ok) {
    release();
    await quietly(() => opts.downloads.resume(id));
    const reason = outcome.error.code === 'timeout' ? 'timeout' : 'error';
    return { handedOff: false, reason, code: outcome.error.code, message: outcome.error.message };
  }
  let cancelled = false;
  try {
    try {
      await opts.downloads.cancel(id);
      cancelled = true;
    } catch {
      // not cancelled (it may have completed)
    }
    // Erasing a download that could not be cancelled (it may have completed) would hide
    // the browser's file from the user.
    if (cancelled) await quietly(() => opts.downloads.erase({ id }));
  } finally {
    // A cancelled, erased download has no file name left to choose: answering the browser's
    // onDeterminingFilename for it fails with "Invalid downloadId" on the extension's Errors page.
    if (!cancelled) release();
  }
  return { handedOff: true, value: outcome.value };
}

/** Maps a DownloadItem to the rules' view of it. */
export function candidateFromItem(item: DownloadItemLike): CaptureCandidate {
  const size =
    item.totalBytes !== undefined && item.totalBytes > 0
      ? item.totalBytes
      : item.fileSize !== undefined && item.fileSize > 0
        ? item.fileSize
        : undefined;
  const c: CaptureCandidate = { url: item.url, incognito: item.incognito };
  if (item.finalUrl) c.finalUrl = item.finalUrl;
  if (item.filename) c.filename = item.filename;
  if (item.mime) c.mime = item.mime;
  if (size !== undefined) c.size = size;
  if (item.referrer) c.pageUrl = item.referrer;
  return c;
}

/** Download IDs one of the Chromium listeners took, so the other one leaves them alone. */
export class DownloadClaims {
  readonly #ids = new Map<number, number>();

  constructor(
    private readonly ttlMs: number = 60_000,
    private readonly now: () => number = () => Date.now(),
  ) {}

  /** True the first time `id` is claimed within the TTL. */
  claim(id: number): boolean {
    const now = this.now();
    for (const [k, ts] of this.#ids) {
      if (now - ts <= this.ttlMs) break;
      this.#ids.delete(k);
    }
    if (this.#ids.has(id)) return false;
    this.#ids.set(id, now);
    return true;
  }
}

/**
 * How long downloads.onCreated waits for onDeterminingFilename before capturing the download
 * itself: the browser sends both within milliseconds when it delivers both.
 */
export const CREATED_FALLBACK_DELAY_MS = 1000;

export interface ChromiumCaptureDeps {
  downloads: DownloadsControl;
  /** Runs the rules; synchronous once rules are loaded, a Promise on a cold start. */
  decide: (item: DownloadItemLike) => CaptureDecision | Promise<CaptureDecision>;
  /** Builds and sends the `add` request for this item. */
  handOff: (item: DownloadItemLike, timeoutMs: number) => Promise<Outcome<unknown>>;
  /** Shared with the onCreated fallback; without it every event is decided. */
  claims?: DownloadClaims;
  timeoutMs?: number;
  timers?: Timers;
  onDecision?: (item: DownloadItemLike, decision: CaptureDecision) => void;
  onResult?: (item: DownloadItemLike, result: HandoffResult) => void;
  onError?: (error: unknown) => void;
}

function reportDecision(deps: ChromiumCaptureDeps, item: DownloadItemLike, d: CaptureDecision): CaptureDecision {
  try {
    deps.onDecision?.(item, d);
  } catch (e) {
    deps.onError?.(e);
  }
  return d;
}

/**
 * Returns a downloads.onCreated listener for downloads the browser never sent through
 * onDeterminingFilename: after `delayMs` it claims the download, decides, and hands it off
 * paused like the main path (the browser has started writing the file by then).
 */
export function createCreatedFallbackListener(
  deps: ChromiumCaptureDeps & { claims: DownloadClaims; delayMs?: number },
): (item: DownloadItemLike) => void {
  const timeoutMs = deps.timeoutMs ?? CHROMIUM_HANDOFF_TIMEOUT_MS;
  const timers = deps.timers ?? systemTimers;
  return (item) => {
    timers.setTimeout(() => {
      if (!deps.claims.claim(item.id)) return;
      const startedAt = timers.now();
      void (async () => {
        try {
          const decision = reportDecision(deps, item, await deps.decide(item));
          if (!decision.capture) return;
          const budget = handOffBudget(timeoutMs, timers.now() - startedAt);
          const result = await pausedHandOff(item.id, {
            downloads: deps.downloads,
            handOff: () => deps.handOff(item, budget),
            timeoutMs: budget,
            ...(deps.timers ? { timers: deps.timers } : {}),
          });
          deps.onResult?.(item, result);
        } catch (e) {
          deps.onError?.(e);
        }
      })();
    }, deps.delayMs ?? CREATED_FALLBACK_DELAY_MS);
  };
}

/** `suggest` is only ever called without arguments: the browser keeps its file name. */
export type Suggest = () => void;

/**
 * Returns a downloads.onDeterminingFilename listener. It calls `suggest()` exactly once on
 * every path: synchronously for non-candidates, otherwise after the hand-off is decided
 * (returning `true` to keep the event open).
 */
export function createDeterminingFilenameListener(
  deps: ChromiumCaptureDeps,
): (item: DownloadItemLike, suggest: Suggest) => true | undefined {
  const timeoutMs = deps.timeoutMs ?? CHROMIUM_HANDOFF_TIMEOUT_MS;
  const timers = deps.timers ?? systemTimers;

  const capture = async (item: DownloadItemLike, startedAt: number, suggestOnce: () => void, settled: () => void) => {
    const budget = handOffBudget(timeoutMs, timers.now() - startedAt);
    const result = await pausedHandOff(item.id, {
      downloads: deps.downloads,
      handOff: () => deps.handOff(item, budget),
      timeoutMs: budget,
      ...(deps.timers ? { timers: deps.timers } : {}),
      release: suggestOnce,
    });
    // Handed off: the browser's download is cancelled and erased (or, if it could not be
    // cancelled, already answered). A later suggest() would fail with "Invalid downloadId".
    if (result.handedOff) settled();
    deps.onResult?.(item, result);
  };

  return (item, suggest) => {
    const startedAt = timers.now();
    let suggested = false;
    const settled = () => {
      suggested = true;
    };
    const suggestOnce = () => {
      if (suggested) return;
      suggested = true;
      try {
        suggest();
      } catch (e) {
        deps.onError?.(e);
      }
    };

    // The onCreated fallback took it already: the browser goes on choosing the file name.
    if (deps.claims && !deps.claims.claim(item.id)) {
      suggestOnce();
      return undefined;
    }

    let decision: CaptureDecision | Promise<CaptureDecision>;
    try {
      decision = deps.decide(item);
    } catch (e) {
      deps.onError?.(e);
      suggestOnce();
      return undefined;
    }

    if (!(decision instanceof Promise)) {
      if (!reportDecision(deps, item, decision).capture) {
        suggestOnce();
        return undefined;
      }
      capture(item, startedAt, suggestOnce, settled).catch((e: unknown) => {
        deps.onError?.(e);
        suggestOnce();
      });
      return true;
    }

    decision
      .then((d) => (reportDecision(deps, item, d).capture ? capture(item, startedAt, suggestOnce, settled) : suggestOnce()))
      .catch((e: unknown) => {
        deps.onError?.(e);
      })
      .finally(suggestOnce);
    return true;
  };
}
