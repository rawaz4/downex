// Last hand-offs shown in the popup (BROWSER-SPEC §4). Stored in storage.session when
// available (cleared with the browser session), otherwise storage.local.

import type { ProtocolError } from './protocol';

export const HISTORY_STORAGE_KEY = 'handoffHistory';
export const HISTORY_LIMIT = 10;

/**
 * `media`: an HLS/DASH stream from the popup or an overlay panel (`addMedia`); `video`: a web
 * page's video from the overlay, the popup or the context menu (`addVideo`); `video-file`: a
 * direct MP4 file a page played, from the popup or an overlay panel (`add`).
 */
export type HandoffSource = 'capture' | 'menu' | 'page-links' | 'media' | 'video' | 'video-file';

export interface HandoffRecord {
  /** Epoch ms. */
  ts: number;
  url: string;
  filename?: string;
  /** Page title sent with a media or video hand-off. */
  title?: string;
  source: HandoffSource;
  ok: boolean;
  /** Daemon download ID when ok. */
  downloadId?: number | string;
  /** A video Downex added: whether it waits for the user's confirmation there. */
  awaiting?: boolean;
  /** Failure reason when not ok; the browser kept the download for captures. */
  error?: ProtocolError;
  /**
   * Downex accepted a capture only after the hand-off had timed out, so the browser kept
   * the download too: the file is downloaded twice.
   */
  duplicate?: true;
}

/** Newest first, at most HISTORY_LIMIT entries. */
export function pushRecord(list: readonly HandoffRecord[], rec: HandoffRecord): HandoffRecord[] {
  return [rec, ...list].slice(0, HISTORY_LIMIT);
}

function isRecord(v: unknown): v is HandoffRecord {
  if (typeof v !== 'object' || v === null) return false;
  const r = v as Record<string, unknown>;
  return typeof r.ts === 'number' && typeof r.url === 'string' && typeof r.ok === 'boolean' && typeof r.source === 'string';
}

export function parseHistory(raw: unknown): HandoffRecord[] {
  return Array.isArray(raw) ? raw.filter(isRecord).slice(0, HISTORY_LIMIT) : [];
}

/** The storage.StorageArea subset the history uses. */
export interface StorageAreaLike {
  get(key: string): Promise<Record<string, unknown>>;
  set(items: Record<string, unknown>): Promise<void>;
}

export class HandoffHistory {
  #chain: Promise<unknown> = Promise.resolve();

  constructor(
    private readonly area: StorageAreaLike,
    private readonly key: string = HISTORY_STORAGE_KEY,
  ) {}

  async list(): Promise<HandoffRecord[]> {
    const got = await this.area.get(this.key);
    return parseHistory(got[this.key]);
  }

  /** Appends a record; writes are serialised so concurrent hand-offs are not lost. */
  add(rec: HandoffRecord): Promise<void> {
    const next = this.#chain.then(async () => {
      const list = await this.list();
      await this.area.set({ [this.key]: pushRecord(list, rec) });
    });
    this.#chain = next.catch(() => undefined);
    return next;
  }
}
