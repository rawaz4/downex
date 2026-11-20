// Rescue of downloads that failed in the browser (BROWSER-SPEC §6): which browser downloads are
// offered, their reasons and texts, the request context kept for them, the `rescue` fields, the
// flow of one "Finish with Downex" click, the notification budget and the popup's list. Pure:
// the browser APIs and the native client are injected, so everything here runs under vitest.

import { cookieHeader, defaultPolicyReferer, filterHeaders, hasHeader, pageAddress, type CookieLike } from './context';
import type { StorageAreaLike } from './history';
import {
  daemonCode,
  daemonDetail,
  failure,
  isRelayDeadline,
  type AddedResult,
  type AddFields,
  type HeaderPair,
  type Outcome,
  type ProtocolError,
  type RequestOptions,
  type RescuedResult,
  type RescueFields,
  type ResumedResult,
} from './protocol';
import { formatBytes } from './video-picker';

/** The relay's bound of a rescue; also sent as `deadlineMs` (BROWSER-SPEC §2). */
export const RESCUE_DEADLINE_MS = 30_000;
/** How long the extension waits for `rescued`: the relay's bound and a margin for the hand-off. */
export const RESCUE_TIMEOUT_MS = RESCUE_DEADLINE_MS + 2_000;
/** Chromium resumes some interrupted downloads by itself: an item is offered only this long after it stopped. */
export const CHROMIUM_RECHECK_MS = 3_000;
/** Most rows of the popup's card "Failed in the browser". */
export const MAX_FAILED_ROWS = 5;
/** How long a finished row stays in the card. */
export const DONE_ROW_MS = 5_000;
/** How many interrupted downloads the start-up scan reads. */
export const SCAN_LIMIT = 10;

// ---------------------------------------------------------------------------------------
// Offerable downloads (R-E1)

/** The fields of a browser DownloadItem a rescue reads (Chromium and Firefox). */
export interface RescueItem {
  id: number;
  url: string;
  finalUrl?: string;
  /** The final path of the file (absolute). */
  filename: string;
  state?: string;
  /** Chromium InterruptReason, Firefox's error string. */
  error?: string;
  paused?: boolean;
  danger?: string;
  incognito?: boolean;
  bytesReceived?: number;
  totalBytes?: number;
  /** ISO 8601. */
  startTime?: string;
  referrer?: string;
  mime?: string;
  /** Firefox: cookie store (container) of the download. */
  cookieStoreId?: string;
  byExtensionId?: string;
}

/** What a failed download offers: finish it from the saved bytes, or download it (none saved). */
export type RescueKind = 'finish' | 'download';

/** Reasons that are never offered: the user's cancel, and files the browser blocked or flagged. */
const NOT_OFFERED: ReadonlySet<string> = new Set([
  'USER_CANCELED',
  'FILE_BLOCKED',
  'FILE_VIRUS_INFECTED',
  'FILE_SECURITY_CHECK_FAILED',
]);

/** A danger the browser cleared: none, or the user kept the file. */
const CLEARED_DANGER: ReadonlySet<string> = new Set(['safe', 'accepted']);

/**
 * Whether `item` is offered (R-E1), and how: never in a private window, only http(s), only a
 * file the browser named, never one the browser flagged as dangerous (its safety check is not
 * bypassed), never a POST (`method`, from the saved context), and only an interrupted download
 * the user did not cancel (Firefox: also a paused one).
 */
export function rescueOffer(item: RescueItem, isFirefox: boolean, method?: string): RescueKind | undefined {
  if (item.incognito) return undefined;
  if (!/^https?:/i.test(item.url)) return undefined;
  if (!item.filename) return undefined;
  if (item.danger !== undefined && item.danger !== '' && !CLEARED_DANGER.has(item.danger)) return undefined;
  if (method !== undefined && method.toUpperCase() === 'POST') return undefined;
  if (item.state !== 'interrupted') return undefined;
  const error = item.error ?? '';
  if (isFirefox) {
    if (!item.paused && error === 'USER_CANCELED') return undefined;
  } else if (NOT_OFFERED.has(error)) {
    return undefined;
  }
  return (item.bytesReceived ?? 0) > 0 ? 'finish' : 'download';
}

/** Why the browser stopped a download, in a few words (Chromium InterruptReason, Firefox's error). */
export function interruptText(error: string | undefined, paused = false): string {
  const e = error ?? '';
  if (/^NETWORK_(FAILED|TIMEOUT|DISCONNECTED|SERVER_DOWN|INVALID_REQUEST)$/.test(e)) return 'Network failed';
  if (/^SERVER_(UNAUTHORIZED|FORBIDDEN|CERT_PROBLEM)$/.test(e)) return 'Refused by the server';
  if (/^SERVER_/.test(e)) return 'Server error';
  if (e === 'FILE_NO_SPACE') return 'Disk full';
  if (e === 'FILE_TOO_LARGE') return 'Too large for the disk';
  if (/^FILE_/.test(e)) return 'Could not write the file';
  if (e === 'USER_SHUTDOWN') return 'Browser closed';
  if (e === 'CRASH') return 'Browser crashed';
  if (paused && e === '') return 'Paused';
  return 'Stopped';
}

/** The base name of a browser path (either separator). */
export function itemName(item: Pick<RescueItem, 'filename'>): string {
  return item.filename.split(/[\\/]/).pop() ?? '';
}

/** "Network failed · 412.0 MB of 1.2 GB saved", or "… · 412.0 MB saved" without a total. */
export function savedLine(item: RescueItem): string {
  const saved = formatBytes(Math.max(item.bytesReceived ?? 0, 0));
  const total = item.totalBytes ?? 0;
  const reason = interruptText(item.error, item.paused);
  return total > 0 ? `${reason} · ${saved} of ${formatBytes(total)} saved` : `${reason} · ${saved} saved`;
}

// ---------------------------------------------------------------------------------------
// Rescue context (R-E2): how the browser asked for the file, kept per download

/** What the extension saw of a download's request when the browser kept it. */
export interface RescueContext {
  url: string;
  method: string;
  /** Filtered as for `add` (filterHeaders). */
  headers: HeaderPair[];
  etag?: string;
  lastModified?: string;
  /** Epoch ms. */
  ts: number;
}

export const RESCUE_CONTEXT_KEY = 'rescueContext';
export const RESCUE_CONTEXT_MAX = 20;
export const RESCUE_CONTEXT_TTL_MS = 24 * 60 * 60 * 1000;

function isPair(v: unknown): v is HeaderPair {
  return Array.isArray(v) && v.length === 2 && typeof v[0] === 'string' && typeof v[1] === 'string';
}

function parseContext(v: unknown): RescueContext | undefined {
  if (typeof v !== 'object' || v === null) return undefined;
  const r = v as Record<string, unknown>;
  if (typeof r.url !== 'string' || typeof r.method !== 'string' || typeof r.ts !== 'number') return undefined;
  if (!Array.isArray(r.headers) || !r.headers.every(isPair)) return undefined;
  const c: RescueContext = { url: r.url, method: r.method, headers: filterHeaders(r.headers), ts: r.ts };
  if (typeof r.etag === 'string') c.etag = r.etag;
  if (typeof r.lastModified === 'string') c.lastModified = r.lastModified;
  return c;
}

/**
 * The request contexts of downloads the browser kept, by download id: at most RESCUE_CONTEXT_MAX
 * for RESCUE_CONTEXT_TTL_MS. Headers carry cookies, so they live in storage.session (memory,
 * cleared with the browser session) and only in memory where a browser has no storage.session:
 * never in storage.local.
 */
export class RescueContextStore {
  readonly #memory = new Map<number, RescueContext>();
  #chain: Promise<unknown> = Promise.resolve();

  constructor(
    /** storage.session; undefined = memory only. */
    private readonly area: StorageAreaLike | undefined,
    private readonly now: () => number = () => Date.now(),
  ) {}

  async #read(): Promise<Map<number, RescueContext>> {
    if (!this.area) return this.#memory;
    const got = await this.area.get(RESCUE_CONTEXT_KEY);
    const raw = got[RESCUE_CONTEXT_KEY];
    const out = new Map<number, RescueContext>();
    if (typeof raw === 'object' && raw !== null) {
      for (const [k, v] of Object.entries(raw as Record<string, unknown>)) {
        const id = Number(k);
        const c = parseContext(v);
        if (Number.isSafeInteger(id) && c) out.set(id, c);
      }
    }
    return out;
  }

  async #write(m: Map<number, RescueContext>): Promise<void> {
    if (!this.area) return;
    await this.area.set({ [RESCUE_CONTEXT_KEY]: Object.fromEntries([...m].map(([k, v]) => [String(k), v])) });
  }

  #live(m: Map<number, RescueContext>): Map<number, RescueContext> {
    const now = this.now();
    for (const [k, v] of m) if (now - v.ts > RESCUE_CONTEXT_TTL_MS) m.delete(k);
    return m;
  }

  #serial<T>(f: () => Promise<T>): Promise<T> {
    const next = this.#chain.then(f);
    this.#chain = next.catch(() => undefined);
    return next;
  }

  /** Keeps `ctx` for download `id`; the oldest entries give way beyond RESCUE_CONTEXT_MAX. */
  put(id: number, ctx: Omit<RescueContext, 'ts' | 'headers'> & { headers: readonly HeaderPair[] }): Promise<void> {
    return this.#serial(async () => {
      const m = this.#live(await this.#read());
      m.delete(id);
      m.set(id, { ...ctx, headers: filterHeaders(ctx.headers), ts: this.now() });
      const sorted = [...m].sort((a, b) => a[1].ts - b[1].ts);
      while (sorted.length > RESCUE_CONTEXT_MAX) m.delete(sorted.shift()![0]);
      await this.#write(m);
    });
  }

  async get(id: number): Promise<RescueContext | undefined> {
    const m = this.#live(await this.#read());
    return m.get(id);
  }

  delete(id: number): Promise<void> {
    return this.#serial(async () => {
      const m = await this.#read();
      if (m.delete(id)) await this.#write(m);
    });
  }
}

/** Response validators of download-like responses, kept by address for a minute (R-E2). */
export class ValidatorMemory {
  readonly #entries = new Map<string, { etag?: string; lastModified?: string; ts: number }>();

  constructor(
    private readonly now: () => number = () => Date.now(),
    private readonly ttlMs = 60_000,
    private readonly max = 256,
  ) {}

  record(url: string, store: string | undefined, etag: string | undefined, lastModified: string | undefined): void {
    if (!etag && !lastModified) return;
    const key = `${store ?? ''}\n${url}`;
    this.#entries.delete(key);
    const e: { etag?: string; lastModified?: string; ts: number } = { ts: this.now() };
    if (etag) e.etag = etag;
    if (lastModified) e.lastModified = lastModified;
    this.#entries.set(key, e);
    for (const k of this.#entries.keys()) {
      if (this.#entries.size <= this.max) break;
      this.#entries.delete(k);
    }
  }

  get(url: string, store: string | undefined): { etag?: string; lastModified?: string } | undefined {
    const key = `${store ?? ''}\n${url}`;
    const e = this.#entries.get(key);
    if (!e) return undefined;
    if (this.now() - e.ts > this.ttlMs) {
      this.#entries.delete(key);
      return undefined;
    }
    const { ts: _ts, ...v } = e;
    return v;
  }
}

// ---------------------------------------------------------------------------------------
// The `rescue` fields (R-E3)

export interface RescueFieldDeps {
  /** cookies.getAll({url, storeId}) for this URL only (when no context was saved). */
  getCookies: (url: string, storeId: string | undefined) => Promise<readonly CookieLike[]>;
  userAgent: string;
}

/** Longest validator a `rescue` carries (BROWSER-SPEC §2). */
const MAX_VALIDATOR_CHARS = 1024;

function validator(v: string | undefined): string | undefined {
  // eslint-disable-next-line no-control-regex
  return v && v.length <= MAX_VALIDATOR_CHARS && !/[\u0000-\u001f\u007f]/.test(v) ? v : undefined;
}

/**
 * The `rescue` fields of a failed download: the saved context's headers and validators, else the
 * cookies of that one address, the Referer the browser's default policy derives from the page,
 * and the User-Agent. Stored paused: the extension erases its entry before Downex starts it.
 */
export async function buildRescueFields(item: RescueItem, saved: RescueContext | undefined, deps: RescueFieldDeps): Promise<RescueFields> {
  const url = saved?.url ?? item.url;
  let headers: HeaderPair[] = filterHeaders(saved?.headers ?? []);
  // No request seen (the start-up scan after a restart, or a download whose request was not
  // recorded): the cookies of that one address and a Referer from the page.
  if (headers.length === 0) {
    try {
      const cookies = await deps.getCookies(url, item.cookieStoreId);
      if (cookies.length > 0) headers.push(['Cookie', cookieHeader(cookies)]);
    } catch {
      // no cookie access for this URL: rescue without cookies
    }
    const referer = defaultPolicyReferer(item.referrer, url);
    if (referer) headers.push(['Referer', referer]);
  }
  if (deps.userAgent && !hasHeader(headers, 'user-agent')) headers.push(['User-Agent', deps.userAgent]);
  headers = filterHeaders(headers);
  const fields: RescueFields = {
    url,
    headers,
    target: item.filename,
    bytesReceived: Math.max(0, Math.floor(item.bytesReceived ?? 0)),
    startPaused: true,
    deadlineMs: RESCUE_DEADLINE_MS,
  };
  const total = item.totalBytes ?? 0;
  fields.totalBytes = Number.isSafeInteger(total) && total > 0 ? total : -1;
  const start = item.startTime ? Date.parse(item.startTime) : NaN;
  if (Number.isSafeInteger(start) && start > 0) fields.startTime = start;
  const etag = validator(saved?.etag);
  if (etag) fields.etag = etag;
  const lastModified = validator(saved?.lastModified);
  if (lastModified) fields.lastModified = lastModified;
  const page = pageAddress(item.referrer);
  if (page) fields.pageUrl = page;
  return fields;
}

// ---------------------------------------------------------------------------------------
// Texts (R-E7): the table the desktop window and the CLI share (internal/rescuetext)

export const RESCUE_TEXTS: Readonly<Record<string, string>> = {
  rescue_no_ranges: 'This server cannot continue a download from the middle, so the saved part cannot be used.',
  rescue_size_unknown: 'The server does not say how big the file is, so Downex cannot check the saved part.',
  rescue_changed: 'The file on the server is not the one the browser started, so the saved part cannot be used.',
  rescue_unproven: 'The saved part holds too little that tells files apart, so Downex cannot check it.',
  rescue_not_found: "The browser's partial file is gone. Browsers delete it when a download is cancelled.",
  rescue_ambiguous: 'More than one unfinished browser file could be this one. In Downex, use Rescue partial file… and choose it.',
  rescue_locked: 'The browser is still using the partial file. Try again in a few seconds.',
  rescue_no_access: 'Downex is not allowed to open the partial file.',
  rescue_empty: 'The partial file is empty: there is nothing to finish.',
  link_expired: 'The address has expired. Start the download again on the web page.',
  http_status: 'The server refused the address. It may have expired: start the download again on the web page.',
  auth_required: 'The server asks for a sign-in. Add a site login in Downex, then try again.',
  network: 'Downex could not reach the server.',
};

/** Added to rescue_no_access on macOS (the privacy settings for files and folders). */
export const MAC_NO_ACCESS = ' Allow Downex Agent in System Settings > Privacy & Security > Files and Folders.';

export const NO_LONGER_LISTED = "This download is no longer in the browser's list.";
export const RESUME_FAILED_TEXT = 'Downex has it, paused. Resume it in Downex.';
export const CHECKING_TEXT = 'Checking the file with the server…';

/** A refused or failed rescue in words a user can act on (also the notification's text). */
export function rescueErrorText(e: ProtocolError, isMac: boolean): string {
  const code = daemonCode(e);
  const text = Object.hasOwn(RESCUE_TEXTS, code) ? RESCUE_TEXTS[code] : undefined;
  if (text) return code === 'rescue_no_access' && isMac ? text + MAC_NO_ACCESS : text;
  switch (e.code) {
    case 'gone':
      return NO_LONGER_LISTED;
    case 'daemon_unavailable':
      return isRelayDeadline(e) ? 'Downex took too long to check the file. Check its list before trying again.' : `Downex is not running: ${e.message}`;
    case 'disconnected':
    case 'incompatible':
      return `Downex is not available: ${e.message}`;
    case 'timeout':
      return 'Downex did not answer in time. Check its list before trying again.';
    default:
      return daemonDetail(e);
  }
}

/** What the row offers after a failure: try again, download from the start, or nothing. */
export type RescueRetry = 'retry' | 'from-start' | 'none';

export function rescueRetry(e: ProtocolError): RescueRetry {
  const code = daemonCode(e);
  if (code === 'rescue_no_ranges' || code === 'rescue_size_unknown' || code === 'rescue_changed' || code === 'rescue_unproven') {
    return 'from-start';
  }
  if (code === 'rescue_locked' || code === 'network' || code === 'daemon_unavailable' || code === 'timeout' || code === 'disconnected') {
    return 'retry';
  }
  return 'none';
}

// ---------------------------------------------------------------------------------------
// One click (behaviour A.4)

/** The downloads API a rescue uses. */
export interface RescueDownloads {
  search(query: { id: number }): Promise<RescueItem[]>;
  cancel(id: number): Promise<void>;
  erase(query: { id: number }): Promise<unknown>;
}

/** The native client's requests a rescue sends. */
export interface RescueClient {
  rescue(fields: RescueFields, options?: RequestOptions<RescuedResult>): Promise<Outcome<RescuedResult>>;
  resume(downloadId: number | string, options?: RequestOptions<ResumedResult>): Promise<Outcome<ResumedResult>>;
  add(fields: AddFields, options?: RequestOptions<AddedResult>): Promise<Outcome<AddedResult>>;
}

export interface FinishDeps {
  downloads: RescueDownloads;
  client: RescueClient;
  isFirefox: boolean;
  /** The saved context's method ("" = none): a POST is never offered. */
  method?: (item: RescueItem) => Promise<string | undefined>;
  fields: (item: RescueItem) => Promise<RescueFields>;
  /** The `add` of a download from its start (the browser kept no bytes, or the user chose it). */
  addFields: (item: RescueItem) => Promise<Outcome<AddFields>>;
  /** A `rescued` that came after the extension stopped waiting; the flow went on with it. */
  onLate?: (outcome: Outcome<FinishResult>) => void;
}

/** A download Downex took over. */
export interface FinishResult {
  downloadId: number | string;
  /** Bytes kept from the browser's partial file (0: downloaded from the start). */
  keptBytes: number;
  /** The rescue's first start was sent; false: Downex holds it paused. */
  resumed: boolean;
  /** Downloaded from the start (`add`). */
  fromStart: boolean;
}

/** Cancels `id` when the browser runs it again (auto-resume), then erases its entry. */
async function leaveBrowser(downloads: RescueDownloads, id: number): Promise<void> {
  const [now] = await downloads.search({ id }).catch((): RescueItem[] => []);
  if (now?.state === 'in_progress') await downloads.cancel(id).catch(() => undefined);
  await downloads.erase({ id }).catch(() => undefined);
}

/**
 * "Finish with Downex" for browser download `id` (behaviour A.4): (a) read it again and stop when
 * it is no longer offered; (b) nothing saved, or `fromStart`: `add`, and only once Downex has it,
 * cancel a running entry and erase it; (c) otherwise `rescue`, stored paused; (d) on `rescued`:
 * cancel the entry if the browser runs it again, erase it, then send the first start (`resume`).
 * A `rescued` after the timeout still goes through (d), reported to onLate. Never rejects.
 */
export async function finishWithDownex(id: number, deps: FinishDeps, fromStart = false): Promise<Outcome<FinishResult>> {
  try {
    const [item] = await deps.downloads.search({ id });
    const method = item ? await deps.method?.(item) : undefined;
    const kind = item ? rescueOffer(item, deps.isFirefox, method) : undefined;
    if (!item || !kind) return failure('gone', NO_LONGER_LISTED);
    if (kind === 'download' || fromStart) {
      const fields = await deps.addFields(item);
      if (!fields.ok) return fields;
      const added = await deps.client.add(fields.value, { timeoutMs: RESCUE_TIMEOUT_MS });
      if (!added.ok) return added;
      await leaveBrowser(deps.downloads, id);
      return { ok: true, value: { downloadId: added.value.downloadId, keptBytes: 0, resumed: true, fromStart: true } };
    }
    const take = async (r: RescuedResult): Promise<Outcome<FinishResult>> => {
      await leaveBrowser(deps.downloads, id);
      const resumed = await deps.client.resume(r.downloadId);
      return { ok: true, value: { downloadId: r.downloadId, keptBytes: r.keptBytes, resumed: resumed.ok, fromStart: false } };
    };
    const rescued = await deps.client.rescue(await deps.fields(item), {
      timeoutMs: RESCUE_TIMEOUT_MS,
      onLate: (late) => {
        if (!late.ok) return;
        void take(late.value).then((o) => deps.onLate?.(o));
      },
    });
    if (!rescued.ok) return rescued;
    return await take(rescued.value);
  } catch (e) {
    return failure('internal', e instanceof Error ? e.message : String(e));
  }
}

// ---------------------------------------------------------------------------------------
// The popup's card (behaviour A.1) and the notifications (A.2, R-E6)

/** The state of a row the user acted on. */
export type RowState =
  | { phase: 'checking' }
  | { phase: 'done'; keptBytes: number; resumed: boolean; fromStart: boolean; at: number }
  | { phase: 'failed'; text: string; retry: RescueRetry };

/** A row of the card "Failed in the browser", as the background sends it to the popup (never headers). */
export interface FailedRow {
  downloadId: number;
  name: string;
  /** "Network failed · 412.0 MB of 1.2 GB saved". */
  line: string;
  kind: RescueKind;
  state?: RowState;
}

/**
 * The card's rows: the interrupted downloads still offered, newest first (the order searched),
 * without hidden ones, at most MAX_FAILED_ROWS, plus finished rows for DONE_ROW_MS after their
 * entry left the browser's list.
 */
export class FailedList {
  readonly #states = new Map<number, RowState>();
  /** Rows whose browser entry is gone, kept to show their result. */
  readonly #done = new Map<number, FailedRow>();

  constructor(private readonly now: () => number = () => Date.now()) {}

  set(id: number, state: RowState | undefined, row?: FailedRow): void {
    if (state === undefined) this.#states.delete(id);
    else this.#states.set(id, state);
    if (state?.phase === 'done' && row) this.#done.set(id, { ...row, state });
  }

  state(id: number): RowState | undefined {
    return this.#states.get(id);
  }

  /**
   * `offered` are the browser's interrupted downloads that rescueOffer accepts, newest first;
   * `hidden` the ids the user hid.
   */
  rows(offered: readonly { item: RescueItem; kind: RescueKind }[], hidden: ReadonlySet<number>): FailedRow[] {
    const now = this.now();
    for (const [id, row] of this.#done) {
      if (row.state?.phase === 'done' && now - row.state.at > DONE_ROW_MS) {
        this.#done.delete(id);
        this.#states.delete(id);
      }
    }
    const out: FailedRow[] = [];
    const seen = new Set<number>();
    for (const { item, kind } of offered) {
      if (hidden.has(item.id) || seen.has(item.id)) continue;
      seen.add(item.id);
      const row: FailedRow = { downloadId: item.id, name: itemName(item), line: savedLine(item), kind };
      const state = this.#states.get(item.id);
      if (state) row.state = state;
      out.push(row);
    }
    for (const [id, row] of this.#done) if (!seen.has(id) && !hidden.has(id)) out.unshift(row);
    return out.slice(0, MAX_FAILED_ROWS);
  }
}

/** The texts of one row (behaviour A.1). */
export interface RescueRowView {
  /** The sub-line: the reason and the bytes, or the flow's state. */
  line: string;
  tone: 'muted' | 'ok' | 'error';
  /** The primary button ("" = none). */
  button: string;
  busy: boolean;
  /** What the button does. */
  action: 'finish' | 'from-start' | 'none';
}

export const FINISH_BUTTON = 'Finish with Downex';
export const DOWNLOAD_BUTTON = 'Download with Downex';
export const RETRY_BUTTON = 'Retry';
export const FROM_START_BUTTON = 'Download from the start with Downex';
export const HIDE_TITLE = 'Hide from this list';

export function rescueRow(row: FailedRow): RescueRowView {
  const first = row.kind === 'finish' ? FINISH_BUTTON : DOWNLOAD_BUTTON;
  const s = row.state;
  switch (s?.phase) {
    case 'checking':
      return { line: CHECKING_TEXT, tone: 'muted', button: first, busy: true, action: 'none' };
    case 'done':
      if (s.fromStart) return { line: 'Downloading in Downex from the start', tone: 'ok', button: '', busy: false, action: 'none' };
      return {
        line: s.resumed ? `Continuing in Downex from ${formatBytes(s.keptBytes)}` : RESUME_FAILED_TEXT,
        tone: s.resumed ? 'ok' : 'muted',
        button: '',
        busy: false,
        action: 'none',
      };
    case 'failed':
      if (s.retry === 'from-start') return { line: s.text, tone: 'error', button: FROM_START_BUTTON, busy: false, action: 'from-start' };
      if (s.retry === 'retry') return { line: s.text, tone: 'error', button: RETRY_BUTTON, busy: false, action: 'finish' };
      return { line: s.text, tone: 'error', button: '', busy: false, action: 'none' };
    default:
      return { line: row.line, tone: 'muted', button: first, busy: false, action: 'finish' };
  }
}

/** The notification of a download that just failed (behaviour A.2). */
export function failedNotice(item: RescueItem): { title: string; message: string } {
  const reason = interruptText(item.error, item.paused);
  return {
    title: `Download failed: ${itemName(item)}`,
    message: `${reason}. Downex can finish it from ${formatBytes(Math.max(item.bytesReceived ?? 0, 0))}.`,
  };
}

/** The notification of a rescue's result. */
export function resultNotice(name: string, outcome: Outcome<FinishResult>, isMac: boolean): { title: string; message: string } {
  if (!outcome.ok) return { title: `Downex could not finish ${name}`, message: rescueErrorText(outcome.error, isMac) };
  const v = outcome.value;
  if (v.fromStart) return { title: 'Downloading in Downex', message: `${name} from the start` };
  if (!v.resumed) return { title: 'Continuing in Downex', message: `${name}: ${RESUME_FAILED_TEXT}` };
  return { title: 'Continuing in Downex', message: `${name} from ${formatBytes(v.keptBytes)}` };
}

export const NOTIFICATION_PREFIX = 'downex-rescue:';
export const NOTIFICATIONS_PER_MINUTE = 3;

/** The download id of a notification of ours, or undefined. */
export function notificationDownload(notificationId: string): number | undefined {
  if (!notificationId.startsWith(NOTIFICATION_PREFIX)) return undefined;
  const id = Number(notificationId.slice(NOTIFICATION_PREFIX.length));
  return Number.isSafeInteger(id) && id >= 0 ? id : undefined;
}

/**
 * The notification budget (R-E6): at most one notification per download and
 * NOTIFICATIONS_PER_MINUTE in any minute, none for downloads the start-up scan found, none
 * without the option or the permission.
 */
export class RescueNotifier {
  readonly #notified = new Set<number>();
  #sent: number[] = [];

  constructor(private readonly now: () => number = () => Date.now()) {}

  /** Whether download `id` may be announced now; counts it when so. */
  allow(id: number, opts: { enabled: boolean; permitted: boolean; fromScan: boolean }): boolean {
    if (!opts.enabled || !opts.permitted || opts.fromScan || this.#notified.has(id)) return false;
    const now = this.now();
    this.#sent = this.#sent.filter((t) => now - t < 60_000);
    if (this.#sent.length >= NOTIFICATIONS_PER_MINUTE) return false;
    this.#sent.push(now);
    this.#notified.add(id);
    return true;
  }
}

// ---------------------------------------------------------------------------------------
// The option (behaviour A.3)

export const RESCUE_OPTIONS_KEY = 'rescueOptions';

export interface RescueOptions {
  /** "Offer to finish failed downloads with a notification"; needs the notifications permission. */
  rescueNotify: boolean;
}

/** Missing or malformed: off. */
export function parseRescueOptions(raw: unknown): RescueOptions {
  const r = typeof raw === 'object' && raw !== null ? (raw as Record<string, unknown>) : {};
  return { rescueNotify: r.rescueNotify === true };
}

export const NOTIFY_REFUSED_TEXT = 'The browser did not allow notifications.';
