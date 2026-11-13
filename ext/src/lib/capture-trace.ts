// Capture log shown in the popup: what the browser reported for recent downloads and what the
// extension did with them, so a download the browser kept can be explained. Stored like the
// hand-off history (storage.session when available); URLs lose their query and fragment.

import type { StorageAreaLike } from './history';

export const TRACE_STORAGE_KEY = 'captureTrace';
export const TRACE_LIMIT = 20;

/**
 * `network`: a download-like response seen by webRequest; `created` / `determining`: the
 * downloads events; `decision`: capture or the skip reason; `result`: the hand-off outcome.
 */
export type TraceStep = 'network' | 'created' | 'determining' | 'decision' | 'result';

export interface TraceEntry {
  /** Epoch ms. */
  ts: number;
  step: TraceStep;
  url: string;
  /** Browser download ID, when the step concerns one. */
  downloadId?: number;
  detail: string;
}

const STEPS: ReadonlySet<string> = new Set(['network', 'created', 'determining', 'decision', 'result']);

/** Origin and path only: queries often carry one-time tokens. */
export function traceUrl(url: string): string {
  try {
    const u = new URL(url);
    if (u.protocol !== 'http:' && u.protocol !== 'https:') return `${u.protocol}…`;
    return `${u.origin}${u.pathname}${u.search ? '?…' : ''}`;
  } catch {
    return url.slice(0, 80);
  }
}

function isEntry(v: unknown): v is TraceEntry {
  if (typeof v !== 'object' || v === null) return false;
  const e = v as Record<string, unknown>;
  return (
    typeof e.ts === 'number' &&
    typeof e.step === 'string' &&
    STEPS.has(e.step) &&
    typeof e.url === 'string' &&
    typeof e.detail === 'string' &&
    (e.downloadId === undefined || typeof e.downloadId === 'number')
  );
}

/** Newest first, at most TRACE_LIMIT entries. */
export function parseTrace(raw: unknown): TraceEntry[] {
  return Array.isArray(raw) ? raw.filter(isEntry).slice(0, TRACE_LIMIT) : [];
}

export class CaptureTrace {
  #chain: Promise<unknown> = Promise.resolve();

  constructor(
    private readonly area: StorageAreaLike,
    private readonly key: string = TRACE_STORAGE_KEY,
  ) {}

  /** Appends an entry (URL shortened by traceUrl); writes are serialised like the history's. */
  add(entry: TraceEntry): Promise<void> {
    const rec: TraceEntry = { ...entry, url: traceUrl(entry.url) };
    const next = this.#chain.then(async () => {
      const got = await this.area.get(this.key);
      await this.area.set({ [this.key]: [rec, ...parseTrace(got[this.key])].slice(0, TRACE_LIMIT) });
    });
    this.#chain = next.catch(() => undefined);
    return next;
  }
}
