// "Download all links with Downex" (BROWSER-SPEC §4): collect a[href] http(s) links of a
// page, dedupe them, ask for confirmation above CONFIRM_THRESHOLD and hand each one off.

import type { StorageAreaLike } from './history';
import type { Outcome, ProtocolError } from './protocol';
import { type CaptureRules, candidateExtensions, normalizeExtension } from './rules';

/** More links than this need an explicit confirmation (BROWSER-SPEC §4). */
export const CONFIRM_THRESHOLD = 50;
/** Concurrent `add` requests while handing off a page's links. */
export const LINKS_CONCURRENCY = 4;
export const BATCH_STORAGE_KEY = 'pendingLinkBatches';
/** Unanswered confirmations are dropped after this long. */
export const BATCH_TTL_MS = 30 * 60_000;

/**
 * Runs inside the page through scripting.executeScript, so it must not reference anything
 * outside its own body. Returns every a[href] resolved against the document base URL.
 */
export function collectPageLinks(): string[] {
  const out: string[] = [];
  for (const a of Array.from(document.querySelectorAll('a[href]'))) {
    const href = a.getAttribute('href');
    if (href === null) continue;
    try {
      out.push(new URL(href, document.baseURI).href);
    } catch {
      // not a URL
    }
  }
  return out;
}

export function isHttpUrl(v: string | undefined): v is string {
  if (!v) return false;
  try {
    const p = new URL(v).protocol;
    return p === 'http:' || p === 'https:';
  } catch {
    return false;
  }
}

/** http(s) URLs only, fragment removed (it is never sent to the server), first occurrence kept. */
export function uniqueHttpLinks(raw: unknown): string[] {
  if (!Array.isArray(raw)) return [];
  const seen = new Set<string>();
  for (const v of raw) {
    if (typeof v !== 'string') continue;
    let u: URL;
    try {
      u = new URL(v);
    } catch {
      continue;
    }
    if (u.protocol !== 'http:' && u.protocol !== 'https:') continue;
    u.hash = '';
    seen.add(u.href);
  }
  return [...seen];
}

/**
 * The links whose URL names a file of the capture extension list. Only these are handed
 * off: navigation and action links (logout, delete) must never be fetched with the
 * user's cookies.
 */
export function fileLinks(links: readonly string[], rules: Pick<CaptureRules, 'extensions'>): string[] {
  const wanted = new Set(rules.extensions.map(normalizeExtension));
  return links.filter((url) => candidateExtensions({ url }).some((e) => wanted.has(e)));
}

/** Errors after which the remaining links are not attempted: the relay or daemon is gone. */
const FATAL_CODES: ReadonlySet<string> = new Set(['disconnected', 'daemon_unavailable', 'timeout']);

export interface LinksSummary {
  sent: number;
  failed: number;
  /** Not attempted because the relay or daemon became unavailable. */
  skipped: number;
  /** The error that stopped the batch, if any. */
  stoppedBy?: ProtocolError;
}

/**
 * Hands off every link with at most `concurrency` requests in flight. Stops starting new
 * requests after a fatal error so an unavailable daemon does not cost one timeout per link.
 */
export async function handOffLinks(
  links: readonly string[],
  send: (url: string) => Promise<Outcome<unknown>>,
  concurrency: number = LINKS_CONCURRENCY,
): Promise<LinksSummary> {
  const summary: LinksSummary = { sent: 0, failed: 0, skipped: 0 };
  let next = 0;
  const worker = async (): Promise<void> => {
    while (next < links.length && summary.stoppedBy === undefined) {
      const url = links[next++] as string;
      let outcome: Outcome<unknown>;
      try {
        outcome = await send(url);
      } catch (e) {
        outcome = { ok: false, error: { code: 'internal', message: e instanceof Error ? e.message : String(e) } };
      }
      if (outcome.ok) {
        summary.sent++;
      } else {
        summary.failed++;
        if (FATAL_CODES.has(outcome.error.code)) summary.stoppedBy ??= outcome.error;
      }
    }
  };
  const n = Math.max(1, Math.min(concurrency, links.length));
  await Promise.all(Array.from({ length: n }, worker));
  summary.skipped = links.length - next;
  return summary;
}

/** A page's links waiting for the user's confirmation. */
export interface LinksBatch {
  id: string;
  links: string[];
  /** Page (frame) URL the links were collected from; sent as the referrer. */
  referrer?: string;
  /** Cookie store of the page's tab (Firefox cookieStoreId). */
  storeId?: string;
  /** Epoch ms. */
  createdAt: number;
}

export function parseBatch(v: unknown): LinksBatch | undefined {
  if (typeof v !== 'object' || v === null) return undefined;
  const o = v as Record<string, unknown>;
  if (typeof o.id !== 'string' || typeof o.createdAt !== 'number' || !Array.isArray(o.links)) return undefined;
  const b: LinksBatch = {
    id: o.id,
    links: o.links.filter((l): l is string => typeof l === 'string'),
    createdAt: o.createdAt,
  };
  if (typeof o.referrer === 'string') b.referrer = o.referrer;
  if (typeof o.storeId === 'string') b.storeId = o.storeId;
  return b;
}

/** Pending confirmations, kept in one storage key so an idle worker can be restarted. */
export class PendingBatches {
  #chain: Promise<unknown> = Promise.resolve();

  constructor(
    private readonly area: StorageAreaLike,
    private readonly now: () => number = () => Date.now(),
    private readonly ttlMs: number = BATCH_TTL_MS,
    private readonly key: string = BATCH_STORAGE_KEY,
  ) {}

  async #read(): Promise<Map<string, LinksBatch>> {
    const got = await this.area.get(this.key);
    const raw = got[this.key];
    const out = new Map<string, LinksBatch>();
    if (typeof raw !== 'object' || raw === null) return out;
    const oldest = this.now() - this.ttlMs;
    for (const v of Object.values(raw)) {
      const b = parseBatch(v);
      if (b && b.createdAt >= oldest) out.set(b.id, b);
    }
    return out;
  }

  #write(map: Map<string, LinksBatch>): Promise<void> {
    return this.area.set({ [this.key]: Object.fromEntries(map) });
  }

  /** Serialises read-modify-write cycles. */
  #update<T>(fn: (map: Map<string, LinksBatch>) => T): Promise<T> {
    const next = this.#chain.then(async () => {
      const map = await this.#read();
      const result = fn(map);
      await this.#write(map);
      return result;
    });
    this.#chain = next.catch(() => undefined);
    return next;
  }

  /** Stores a batch (and drops expired ones). */
  put(batch: LinksBatch): Promise<void> {
    return this.#update((map) => {
      map.set(batch.id, batch);
    });
  }

  /** A batch that has not expired or been taken. */
  async get(id: string): Promise<LinksBatch | undefined> {
    await this.#chain;
    return (await this.#read()).get(id);
  }

  /** Removes and returns a batch: each confirmation is acted on at most once. */
  take(id: string): Promise<LinksBatch | undefined> {
    return this.#update((map) => {
      const b = map.get(id);
      map.delete(id);
      return b;
    });
  }
}
