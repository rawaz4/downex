// Observe-only request context (BROWSER-SPEC §3 "Request context", REQUIREMENTS B3).
// Records the headers and POST bodies the browser actually sent, keyed by cookie store and
// URL with a TTL, and turns them into the `add` request fields. Pure: browser APIs are injected.

import {
  MAX_BODY_BYTES,
  type AddFields,
  type HeaderPair,
  type HttpMethod,
  type Outcome,
  failure,
} from './protocol';

export const CONTEXT_TTL_MS = 60_000;
/** Most headers the daemon accepts (DAEMON-SPEC §4). */
export const MAX_HEADER_COUNT = 100;
/** Most total header name + value bytes the daemon accepts (DAEMON-SPEC §4). */
export const MAX_HEADER_BYTES = 64 * 1024;

/** Hop-by-hop, framing and range headers the daemon refuses: the engine owns them (DAEMON-SPEC §4). */
export const FORBIDDEN_HEADERS: ReadonlySet<string> = new Set([
  'host',
  'content-length',
  'transfer-encoding',
  'connection',
  'keep-alive',
  'proxy-connection',
  'te',
  'trailer',
  'upgrade',
  'expect',
  'http2-settings',
  'range',
  'if-range',
  'proxy-authorization',
  'proxy-authenticate',
]);

/** Kept first when the header budget is tight (REQUIREMENTS B3). */
const PRIORITY_HEADERS = ['cookie', 'referer', 'user-agent', 'authorization', 'content-type'];

const TOKEN = /^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/;
// eslint-disable-next-line no-control-regex
const BAD_VALUE = /[\u0000-\u0008\u000a-\u001f\u007f]/;

/** Recorded POST data. `form` keeps parsed fields until the Content-Type is known. */
export type RecordedBody =
  | { kind: 'data'; base64: string; bytes: number }
  | { kind: 'form'; fields: [string, string][] }
  | { kind: 'too_large' }
  | { kind: 'unavailable'; reason: string };

export interface RecordedRequest {
  url: string;
  /** Cookie store of the browsing context (Firefox cookieStoreId); undefined is the default. */
  store?: string;
  method: string;
  headers?: HeaderPair[];
  body?: RecordedBody;
  /** Tab that sent the request (webRequest tabId); undefined when not sent by a tab. */
  tabId?: number;
  /** Epoch ms of the last update. */
  ts: number;
}

export interface ContextStoreOptions {
  ttlMs?: number;
  maxEntries?: number;
  /** Upper bound for the summed size of stored bodies. */
  maxStoredBodyBytes?: number;
  now?: () => number;
}

function bodyWeight(b: RecordedBody | undefined): number {
  if (!b) return 0;
  if (b.kind === 'data') return b.base64.length;
  if (b.kind === 'form') return b.fields.reduce((n, [k, v]) => n + k.length + v.length, 0);
  return 0;
}

/** Map key: a request is only reused within its own cookie store (container, profile). */
function contextKey(url: string, store: string | undefined): string {
  return `${store ?? ''}\n${url}`;
}

/**
 * Store of what the browser sent, keyed by (cookie store, URL), expiring after `ttlMs`
 * (default 60 s). Callers must not record private-window requests.
 */
export class RequestContextStore {
  readonly #entries = new Map<string, RecordedRequest>();
  readonly #ttlMs: number;
  readonly #maxEntries: number;
  readonly #maxBodyBytes: number;
  readonly #now: () => number;
  #bodyBytes = 0;

  constructor(opts: ContextStoreOptions = {}) {
    this.#ttlMs = opts.ttlMs ?? CONTEXT_TTL_MS;
    this.#maxEntries = opts.maxEntries ?? 2000;
    this.#maxBodyBytes = opts.maxStoredBodyBytes ?? 16 * 1024 * 1024;
    this.#now = opts.now ?? (() => Date.now());
  }

  get size(): number {
    return this.#entries.size;
  }

  /** Records a POST body (webRequest.onBeforeRequest fires before onSendHeaders). */
  recordBody(url: string, body: RecordedBody, store?: string): void {
    this.#put(withStore({ url, method: 'POST', body, ts: this.#now() }, store));
  }

  /**
   * Records the headers actually sent (webRequest.onSendHeaders) by tab `tabId` (negative or
   * undefined: not a tab's request).
   */
  recordHeaders(url: string, method: string, headers: HeaderPair[], store?: string, tabId?: number): void {
    const m = method.toUpperCase();
    const prev = this.get(url, store);
    const entry = withStore({ url, method: m, headers, ts: this.#now() }, store);
    if (tabId !== undefined && Number.isSafeInteger(tabId) && tabId >= 0) entry.tabId = tabId;
    // Same request: onBeforeRequest stored its body moments ago and has no headers yet.
    if (prev && prev.method === m && prev.body && !prev.headers) entry.body = prev.body;
    this.#put(entry);
  }

  /** The live record for `url` in cookie store `store`, or undefined when missing or expired. */
  get(url: string, store?: string): RecordedRequest | undefined {
    const key = contextKey(url, store);
    const e = this.#entries.get(key);
    if (!e) return undefined;
    if (this.#now() - e.ts > this.#ttlMs) {
      this.#delete(key);
      return undefined;
    }
    return e;
  }

  /** Drops expired entries (oldest first; insertion order follows update time). */
  prune(): void {
    const now = this.#now();
    for (const [key, e] of this.#entries) {
      if (now - e.ts <= this.#ttlMs) break;
      this.#delete(key);
    }
  }

  #put(entry: RecordedRequest): void {
    const key = contextKey(entry.url, entry.store);
    this.#delete(key);
    this.#entries.set(key, entry);
    this.#bodyBytes += bodyWeight(entry.body);
    this.prune();
    for (const k of this.#entries.keys()) {
      if (this.#entries.size <= this.#maxEntries && this.#bodyBytes <= this.#maxBodyBytes) break;
      if (k === key && this.#entries.size === 1) break;
      this.#delete(k);
    }
  }

  #delete(key: string): void {
    const e = this.#entries.get(key);
    if (!e) return;
    this.#bodyBytes -= bodyWeight(e.body);
    this.#entries.delete(key);
  }
}

function withStore(entry: RecordedRequest, store: string | undefined): RecordedRequest {
  if (store !== undefined) entry.store = store;
  return entry;
}

// ---------------------------------------------------------------------------------------
// Headers

export interface WebRequestHeader {
  name: string;
  value?: string;
}

/** Converts webRequest headers to pairs, skipping binary-only values. */
export function toHeaderPairs(headers: readonly WebRequestHeader[] | undefined): HeaderPair[] {
  const out: HeaderPair[] = [];
  for (const h of headers ?? []) {
    if (typeof h.value === 'string') out.push([h.name, h.value]);
  }
  return out;
}

/**
 * Keeps the headers the daemon may replay: valid token names (no HTTP/2 pseudo-headers),
 * values without CR/LF/controls, no hop-by-hop/framing/range headers, within the count and
 * byte limits. Cookie, Referer and User-Agent are kept first when the budget is tight.
 */
export function filterHeaders(headers: Iterable<HeaderPair>): HeaderPair[] {
  const valid: HeaderPair[] = [];
  for (const [name, value] of headers) {
    if (!TOKEN.test(name) || BAD_VALUE.test(value)) continue;
    if (FORBIDDEN_HEADERS.has(name.toLowerCase())) continue;
    valid.push([name, value]);
  }
  const rank = (n: string) => {
    const i = PRIORITY_HEADERS.indexOf(n.toLowerCase());
    return i < 0 ? PRIORITY_HEADERS.length : i;
  };
  const ordered = valid
    .map((h, i) => ({ h, i }))
    .sort((a, b) => rank(a.h[0]) - rank(b.h[0]) || a.i - b.i)
    .map((x) => x.h);
  const out: HeaderPair[] = [];
  let bytes = 0;
  for (const [name, value] of ordered) {
    if (out.length >= MAX_HEADER_COUNT) break;
    const size = name.length + value.length;
    if (bytes + size > MAX_HEADER_BYTES) continue;
    bytes += size;
    out.push([name, value]);
  }
  return out;
}

export function hasHeader(headers: readonly HeaderPair[], name: string): boolean {
  const n = name.toLowerCase();
  return headers.some(([k]) => k.toLowerCase() === n);
}

export function headerValue(headers: readonly HeaderPair[] | undefined, name: string): string | undefined {
  const n = name.toLowerCase();
  return headers?.find(([k]) => k.toLowerCase() === n)?.[1];
}

// ---------------------------------------------------------------------------------------
// Bodies

export interface WebRequestBodyLike {
  error?: string;
  /** Parsed form fields; binary values (ArrayBuffer) cannot be rebuilt. */
  formData?: Record<string, readonly (string | ArrayBuffer)[]>;
  raw?: readonly { bytes?: ArrayBuffer; file?: string }[];
}

export function bytesToBase64(bytes: Uint8Array): string {
  let bin = '';
  const CHUNK = 0x8000;
  for (let i = 0; i < bytes.length; i += CHUNK) {
    bin += String.fromCharCode(...bytes.subarray(i, i + CHUNK));
  }
  return btoa(bin);
}

/** Converts webRequest.onBeforeRequest `requestBody` into a bounded recorded body. */
export function encodeRequestBody(
  body: WebRequestBodyLike | null | undefined,
  maxBytes: number = MAX_BODY_BYTES,
): RecordedBody {
  if (!body) return { kind: 'data', base64: '', bytes: 0 };
  if (body.error) return { kind: 'unavailable', reason: body.error };
  if (body.formData) {
    const fields: [string, string][] = [];
    for (const [k, vs] of Object.entries(body.formData)) {
      for (const v of vs) {
        if (typeof v !== 'string') return { kind: 'unavailable', reason: 'form contains binary data' };
        fields.push([k, v]);
      }
    }
    return { kind: 'form', fields };
  }
  const parts = body.raw ?? [];
  let total = 0;
  for (const p of parts) {
    if (!p.bytes) return { kind: 'unavailable', reason: 'body contains a file upload' };
    total += p.bytes.byteLength;
    if (total > maxBytes) return { kind: 'too_large' };
  }
  const all = new Uint8Array(total);
  let off = 0;
  for (const p of parts) {
    const b = new Uint8Array(p.bytes as ArrayBuffer);
    all.set(b, off);
    off += b.length;
  }
  return { kind: 'data', base64: bytesToBase64(all), bytes: total };
}

/** Resolves a recorded body to base64 using the request's Content-Type for form fields. */
export function resolveBody(
  body: RecordedBody | undefined,
  contentType: string | undefined,
  maxBytes: number = MAX_BODY_BYTES,
): Outcome<string> {
  if (!body) return failure('unsupported', 'POST body was not observed');
  switch (body.kind) {
    case 'data':
      return { ok: true, value: body.base64 };
    case 'too_large':
      return failure('unsupported', `POST body exceeds ${MAX_BODY_BYTES.toLocaleString('en-US')} bytes`);
    case 'unavailable':
      return failure('unsupported', `POST body unavailable: ${body.reason}`);
    case 'form': {
      const essence = contentType?.split(';', 1)[0]?.trim().toLowerCase();
      if (essence !== 'application/x-www-form-urlencoded') {
        return failure('unsupported', `cannot rebuild a ${essence ?? 'form'} body`);
      }
      const bytes = new TextEncoder().encode(new URLSearchParams(body.fields).toString());
      if (bytes.length > maxBytes) return failure('unsupported', `POST body exceeds ${maxBytes.toLocaleString('en-US')} bytes`);
      return { ok: true, value: bytesToBase64(bytes) };
    }
  }
}

// ---------------------------------------------------------------------------------------
// Add request

export interface HandoffTarget {
  url: string;
  finalUrl?: string;
  filename?: string;
  /** Page or frame URL; sent only as the browser's default policy would send it. */
  referrer?: string;
  /** Cookie store of the browsing context (Firefox cookieStoreId); undefined is the default. */
  storeId?: string;
  mime?: string;
  /** Bytes; undefined, 0 or negative when unknown. */
  size?: number;
  startPaused?: boolean;
}

export interface CookieLike {
  name: string;
  value: string;
}

export interface BuildAddDeps {
  contexts: RequestContextStore;
  /**
   * `cookies.getAll({url, storeId})` for this URL only, in the target's cookie store; used
   * when no request context was recorded.
   */
  getCookies: (url: string, storeId: string | undefined) => Promise<readonly CookieLike[]>;
  userAgent: string;
}

export function cookieHeader(cookies: readonly CookieLike[]): string {
  return cookies.map((c) => `${c.name}=${c.value}`).join('; ');
}

/** Base name of a path (either separator); empty when none. */
export function baseName(path: string | undefined): string {
  return (path ?? '').split(/[\\/]/).pop()?.trim() ?? '';
}

/**
 * The recorded request that returned a target's file, in its cookie store: the original URL's
 * request first, then the final URL's. A non-GET answered by a redirect (a challenge or login
 * form posting to the page, then a 302 to the file) gives way to the final URL's GET from the
 * same tab that followed it: the browser fetched the file with that GET.
 */
export function findContext(
  contexts: RequestContextStore,
  target: Pick<HandoffTarget, 'url' | 'finalUrl' | 'storeId'>,
): RecordedRequest | undefined {
  const ctx = contexts.get(target.url, target.storeId);
  if (!target.finalUrl || target.finalUrl === target.url) return ctx;
  if (!ctx) return contexts.get(target.finalUrl, target.storeId);
  if (ctx.method === 'GET') return ctx;
  const final = contexts.get(target.finalUrl, target.storeId);
  const followed = final?.method === 'GET' && final.ts >= ctx.ts && final.tabId === ctx.tabId;
  return followed ? final : ctx;
}

/** https, or a loopback host (W3C Secure Contexts "potentially trustworthy"). */
function isTrustworthy(u: URL): boolean {
  if (u.protocol === 'https:') return true;
  const h = u.hostname.toLowerCase();
  return h === 'localhost' || h.endsWith('.localhost') || h === '[::1]' || /^127(\.\d{1,3}){3}$/.test(h);
}

/**
 * The Referer the browser's default policy (strict-origin-when-cross-origin) derives from
 * page `referrer` for a request to `target`: the full URL (without fragment and user info)
 * same-origin, the origin cross-origin, and nothing from a secure page to an insecure URL.
 */
export function defaultPolicyReferer(referrer: string | undefined, target: string): string | undefined {
  if (!referrer) return undefined;
  let r: URL;
  let t: URL;
  try {
    r = new URL(referrer);
    t = new URL(target);
  } catch {
    return undefined;
  }
  if (r.protocol !== 'http:' && r.protocol !== 'https:') return undefined;
  if (isTrustworthy(r) && !isTrustworthy(t)) return undefined;
  if (r.origin !== t.origin) return `${r.origin}/`;
  r.hash = '';
  r.username = '';
  r.password = '';
  return r.href;
}

/**
 * Builds the `add` fields from the recorded request context (original URL first, then
 * the final URL, in the target's cookie store). Without one, it falls back to
 * cookies.getAll for that URL in that store and a Referer derived from the page as the
 * browser's default policy would. A recorded request's Referer is never added or changed.
 */
export async function buildAddFields(target: HandoffTarget, deps: BuildAddDeps): Promise<Outcome<AddFields>> {
  const ctx = findContext(deps.contexts, target);
  const url = ctx?.url ?? target.url;
  const method: HttpMethod = ctx?.method === 'POST' ? 'POST' : 'GET';
  if (ctx && ctx.method !== 'GET' && ctx.method !== 'POST') {
    return failure('unsupported', `method ${ctx.method} cannot be handed off`);
  }

  let headers = filterHeaders(ctx?.headers ?? []);
  if (!ctx) {
    try {
      const cookies = await deps.getCookies(url, target.storeId);
      if (cookies.length > 0) headers.push(['Cookie', cookieHeader(cookies)]);
    } catch {
      // no cookie access for this URL: hand off without cookies
    }
    const referer = defaultPolicyReferer(target.referrer, url);
    if (referer) headers.push(['Referer', referer]);
  }
  if (deps.userAgent && !hasHeader(headers, 'user-agent')) headers.push(['User-Agent', deps.userAgent]);
  headers = filterHeaders(headers);

  const fields: AddFields = { url, method, headers };
  if (method === 'POST') {
    const body = resolveBody(ctx?.body, headerValue(ctx?.headers, 'content-type'));
    if (!body.ok) return body;
    fields.body = body.value;
  }
  const filename = baseName(target.filename);
  if (filename) fields.filename = filename;
  // Only the Referer actually sent (or derived above); never the full page URL.
  const referrer = headerValue(headers, 'referer');
  if (referrer) fields.referrer = referrer;
  if (target.mime) fields.mime = target.mime;
  if (target.size !== undefined && Number.isSafeInteger(target.size) && target.size > 0) fields.size = target.size;
  if (target.startPaused) fields.startPaused = true;
  return { ok: true, value: fields };
}
