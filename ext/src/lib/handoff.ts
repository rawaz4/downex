// Builds and sends one `add` request and records it in the hand-off history.
// Shared by automatic capture and the context menus.

import { type BuildAddDeps, type HandoffTarget, baseName, buildAddFields } from './context';
import type { HandoffHistory, HandoffRecord, HandoffSource } from './history';
import {
  type AddedResult,
  type AddFields,
  type Outcome,
  type RequestOptions,
  type Timers,
  failure,
  systemTimers,
} from './protocol';
import type { DownloadItemLike } from './capture-chromium';

/** Most time spent building the `add` fields (cookie lookup) before giving up unsent. */
export const PREPARE_TIMEOUT_MS = 1000;

export interface AddSender {
  add(fields: AddFields, options?: RequestOptions<AddedResult>): Promise<Outcome<AddedResult>>;
}

export interface HandoffDeps extends BuildAddDeps {
  client: AddSender;
  history?: HandoffHistory;
  now?: () => number;
  timers?: Timers;
  onError?: (error: unknown) => void;
}

export interface HandoffOptions {
  source: HandoffSource;
  /**
   * Budget of the `add` request, timed from when it is sent; also sent as `deadlineMs` so
   * the relay drops an add it cannot commit before the browser takes the download back.
   */
  timeoutMs?: number;
}

/**
 * Never rejects. Resolves within PREPARE_TIMEOUT_MS + `timeoutMs` (the native client times
 * the add); an add that was not sent by then is never sent.
 */
export type HandOff = (target: HandoffTarget, options: HandoffOptions) => Promise<Outcome<AddedResult>>;

function withTimeout<T>(p: Promise<T>, ms: number, timers: Timers, onTimeout: () => T): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const timer = timers.setTimeout(() => resolve(onTimeout()), ms);
    p.then(
      (v) => {
        timers.clearTimeout(timer);
        resolve(v);
      },
      (e: unknown) => {
        timers.clearTimeout(timer);
        reject(e instanceof Error ? e : new Error(String(e)));
      },
    );
  });
}

export function createHandOff(deps: HandoffDeps): HandOff {
  const now = deps.now ?? (() => Date.now());
  const timers = deps.timers ?? systemTimers;

  const record = (target: HandoffTarget, source: HandoffSource, outcome: Outcome<AddedResult>, duplicate = false) => {
    if (!deps.history) return;
    const rec: HandoffRecord = { ts: now(), url: target.url, source, ok: outcome.ok };
    const filename = baseName(target.filename);
    if (filename) rec.filename = filename;
    if (outcome.ok) rec.downloadId = outcome.value.downloadId;
    else rec.error = outcome.error;
    if (duplicate) rec.duplicate = true;
    deps.history.add(rec).catch((e: unknown) => deps.onError?.(e));
  };

  const send = async (target: HandoffTarget, options: HandoffOptions): Promise<Outcome<AddedResult>> => {
    // Fields built after the timeout are discarded: that add is never sent.
    const built = await withTimeout(buildAddFields(target, deps), PREPARE_TIMEOUT_MS, timers, () =>
      failure('timeout', `request context not ready within ${PREPARE_TIMEOUT_MS} ms`),
    );
    if (!built.ok) return built;
    const fields = built.value;
    const capture = options.source === 'capture';
    // The browser already sent a POST (often with a one-time token); replaying it after
    // the browser download is cancelled may fail and lose the download (B4).
    if (capture && fields.method === 'POST') return failure('unsupported', 'POST downloads stay in the browser');
    const req: RequestOptions<AddedResult> = {
      onLate: (late) => {
        if (late.ok) record(target, options.source, late, capture);
      },
    };
    if (options.timeoutMs !== undefined) {
      req.timeoutMs = options.timeoutMs;
      if (capture) fields.deadlineMs = options.timeoutMs;
    }
    return deps.client.add(fields, req);
  };

  return async (target, options) => {
    let outcome: Outcome<AddedResult>;
    try {
      outcome = await send(target, options);
    } catch (e) {
      outcome = failure('internal', e instanceof Error ? e.message : String(e));
    }
    record(target, options.source, outcome);
    return outcome;
  };
}

/** The hand-off target of a browser download item. */
export function targetFromDownloadItem(item: DownloadItemLike): HandoffTarget {
  const t: HandoffTarget = { url: item.url };
  if (item.finalUrl) t.finalUrl = item.finalUrl;
  const filename = baseName(item.filename);
  if (filename) t.filename = filename;
  if (item.referrer) t.referrer = item.referrer;
  if (item.cookieStoreId) t.storeId = item.cookieStoreId;
  if (item.mime) t.mime = item.mime;
  const size = item.totalBytes !== undefined && item.totalBytes > 0 ? item.totalBytes : item.fileSize;
  if (size !== undefined && size > 0) t.size = size;
  return t;
}
