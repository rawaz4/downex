// Browser-fetched saves, the content script's part (SITE-MEDIA-SPEC §2.8): the background opens
// a port to the frame of the video's tab and names one request; this script makes it with the
// page's own network stack (the page's origin and referrer, the browser's own headers), sends
// back the answer's head and then its body in base64 pieces of at most MAX_FETCH_PIECE bytes,
// each only after the background acknowledged the last one. The body is never held whole.

import { MAX_FETCH_PIECE } from './protocol';

/** The name of the background's port to a frame for one fetch. */
export const FETCH_PORT = 'downex-fetch';

/** background → content script */
export type FetchCommand =
  | { type: 'start'; url: string; start: number; end: number; credentials: 'include' | 'omit' }
  | { type: 'more' };

/** content script → background */
export type FetchReport =
  | { type: 'head'; status: number; contentType: string; contentRange: string; contentLength: number }
  | { type: 'data'; data: string; end: boolean }
  | { type: 'error'; error: string };

/** The runtime.Port fields used here. */
export interface PortLike {
  name: string;
  sender?: { id?: string };
  postMessage(message: unknown): void;
  disconnect(): void;
  onMessage: { addListener(listener: (message: unknown) => void): void };
  onDisconnect: { addListener(listener: () => void): void };
}

/** Base64 of bytes, built in slices (String.fromCharCode takes a bounded argument list). */
export function toBase64(bytes: Uint8Array): string {
  let s = '';
  for (let i = 0; i < bytes.length; i += 0x8000) {
    s += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  }
  return btoa(s);
}

/** A Range header for [start, end] (end -1 = to the end). */
export function rangeHeader(start: number, end: number): string {
  return end >= 0 ? `bytes=${start}-${end}` : `bytes=${start}-`;
}

/** Bytes read and not sent yet, taken in pieces of at most `max`. */
class Pending {
  #chunks: Uint8Array[] = [];
  size = 0;

  push(c: Uint8Array): void {
    if (c.length === 0) return;
    this.#chunks.push(c);
    this.size += c.length;
  }

  take(max: number): Uint8Array {
    const n = Math.min(max, this.size);
    const out = new Uint8Array(n);
    let off = 0;
    while (off < n) {
      const c = this.#chunks[0] as Uint8Array;
      const k = Math.min(c.length, n - off);
      out.set(c.subarray(0, k), off);
      off += k;
      if (k === c.length) this.#chunks.shift();
      else this.#chunks[0] = c.subarray(k);
    }
    this.size -= n;
    return out;
  }
}

function isStart(m: unknown): m is Extract<FetchCommand, { type: 'start' }> {
  if (typeof m !== 'object' || m === null) return false;
  const c = m as Record<string, unknown>;
  return (
    c.type === 'start' &&
    typeof c.url === 'string' &&
    /^https?:\/\//i.test(c.url) &&
    Number.isSafeInteger(c.start) &&
    (c.start as number) >= 0 &&
    Number.isSafeInteger(c.end) &&
    (c.end as number) >= -1 &&
    (c.credentials === 'include' || c.credentials === 'omit')
  );
}

/**
 * Serves one fetch port: the first `start` makes the request (`Range` only; credentials as the
 * background says), then the head goes out and each piece after a `more`. A disconnected port
 * aborts the request. `fetchFn` is the page's fetch (Firefox: `content.fetch`).
 */
export function servePageFetch(port: PortLike, fetchFn: typeof fetch, maxPiece = MAX_FETCH_PIECE): void {
  const abort = new AbortController();
  let wake: (() => void) | undefined;
  let gone = false;
  let started = false;
  const more = () =>
    new Promise<void>((resolve) => {
      wake = resolve;
    });
  const post = (r: FetchReport) => {
    if (!gone) port.postMessage(r);
  };
  port.onDisconnect.addListener(() => {
    gone = true;
    abort.abort();
    wake?.();
  });
  port.onMessage.addListener((m) => {
    if ((m as { type?: unknown })?.type === 'more') {
      const w = wake;
      wake = undefined;
      w?.();
      return;
    }
    if (started || !isStart(m)) return;
    started = true;
    void run(m);
  });

  const run = async (c: Extract<FetchCommand, { type: 'start' }>) => {
    try {
      const res = await fetchFn(c.url, {
        headers: { Range: rangeHeader(c.start, c.end) },
        credentials: c.credentials,
        signal: abort.signal,
      });
      const length = Number(res.headers.get('content-length') ?? '');
      const acked = more();
      post({
        type: 'head',
        status: res.status,
        contentType: res.headers.get('content-type') ?? '',
        contentRange: res.headers.get('content-range') ?? '',
        contentLength: res.headers.get('content-length') !== null && Number.isSafeInteger(length) && length >= 0 ? length : -1,
      });
      await acked;
      if (gone) return;
      const reader = res.body?.getReader();
      const pending = new Pending();
      for (let done = !reader; ; ) {
        if (!done && reader) {
          const r = await reader.read();
          if (r.value) pending.push(r.value);
          done = r.done;
        }
        if (gone) {
          await reader?.cancel().catch(() => undefined);
          return;
        }
        if (pending.size < maxPiece && !done) continue;
        const piece = pending.take(maxPiece);
        const end = done && pending.size === 0;
        const next = more();
        post({ type: 'data', data: toBase64(piece), end });
        if (end) return;
        await next;
      }
    } catch (e) {
      if (!gone) post({ type: 'error', error: String(e instanceof Error ? e.message : e).slice(0, 200) || 'fetch failed' });
    }
  };
}

/** The fetch that runs in the page's context: Firefox's `content.fetch`, else `fetch`. */
export function pageFetch(): typeof fetch {
  const content = (globalThis as { content?: { fetch?: typeof fetch } }).content;
  return typeof content?.fetch === 'function' ? content.fetch.bind(content) : fetch.bind(globalThis);
}

/** Listens for the background's fetch ports (from this extension only). */
export function listenPageFetch(
  onConnect: { addListener(l: (port: PortLike) => void): void },
  ownId: string,
  fetchFn: typeof fetch = pageFetch(),
): void {
  onConnect.addListener((port) => {
    if (port.name !== FETCH_PORT) return;
    if (port.sender?.id !== undefined && port.sender.id !== ownId) {
      port.disconnect();
      return;
    }
    servePageFetch(port, fetchFn);
  });
}
