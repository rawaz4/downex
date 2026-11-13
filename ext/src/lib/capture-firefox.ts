// Firefox capture (BROWSER-SPEC §3, REQUIREMENTS B4): a blocking webRequest.onHeadersReceived
// listener classifies main_frame/sub_frame attachments and cancels the request once Downex
// has accepted it, or lets it through after 1.5 s; downloads.onCreated is the fallback.
// Pure: rules, hand-off and the downloads API are injected.

import {
  type DownloadItemLike,
  type DownloadsControl,
  type HandoffResult,
  pausedHandOff,
  CHROMIUM_HANDOFF_TIMEOUT_MS,
  HANDOFF_BACKSTOP_SLACK_MS,
} from './capture-chromium';
import type { HandoffTarget, WebRequestHeader } from './context';
import { type Outcome, type Timers, errorMessage, systemTimers } from './protocol';
import { type CaptureCandidate, type CaptureDecision, mimeEssence } from './rules';

export const FIREFOX_HEADERS_TIMEOUT_MS = 1500;
/** How long a URL tried by the headers path is ignored by the onCreated fallback. */
export const ATTEMPT_MEMORY_MS = 30_000;

export interface ContentDisposition {
  type: string;
  filename?: string;
}

function unquote(v: string): string {
  if (v.length >= 2 && v.startsWith('"') && v.endsWith('"')) {
    return v.slice(1, -1).replace(/\\(.)/g, '$1');
  }
  return v;
}

/** Splits on `;` outside quoted strings. */
function splitParams(header: string): string[] {
  const out: string[] = [];
  let cur = '';
  let quoted = false;
  for (let i = 0; i < header.length; i++) {
    const ch = header[i] as string;
    if (quoted && ch === '\\' && i + 1 < header.length) {
      cur += ch + (header[++i] as string);
      continue;
    }
    if (ch === '"') quoted = !quoted;
    if (ch === ';' && !quoted) {
      out.push(cur);
      cur = '';
      continue;
    }
    cur += ch;
  }
  out.push(cur);
  return out.map((s) => s.trim());
}

/** RFC 8187 ext-value: charset'language'pct-encoded. */
function decodeExtValue(v: string): string | undefined {
  const m = /^([^']*)'[^']*'(.*)$/.exec(v);
  if (!m) return undefined;
  const charset = (m[1] ?? '').toLowerCase();
  const encoded = m[2] ?? '';
  try {
    if (charset === 'utf-8') return decodeURIComponent(encoded);
    if (charset === 'iso-8859-1') {
      return encoded.replace(/%([0-9a-f]{2})/gi, (_, h: string) => String.fromCharCode(parseInt(h, 16)));
    }
  } catch {
    return undefined;
  }
  return undefined;
}

/** Parses a Content-Disposition header (RFC 6266); `filename*` wins over `filename`. */
export function parseContentDisposition(header: string | undefined): ContentDisposition | undefined {
  if (!header) return undefined;
  const [type, ...params] = splitParams(header);
  if (!type) return undefined;
  const cd: ContentDisposition = { type: type.toLowerCase() };
  let plain: string | undefined;
  let ext: string | undefined;
  for (const p of params) {
    const eq = p.indexOf('=');
    if (eq <= 0) continue;
    const key = p.slice(0, eq).trim().toLowerCase();
    const value = p.slice(eq + 1).trim();
    if (key === 'filename*') ext = decodeExtValue(value);
    else if (key === 'filename') plain = unquote(value);
  }
  const filename = ext ?? plain;
  if (filename) cd.filename = filename;
  return cd;
}

/** MIME types Firefox shows in the tab instead of downloading. */
function rendersInline(mime: string | undefined): boolean {
  if (!mime) return true;
  if (/^(text|image|video|audio)\//.test(mime)) return true;
  if (mime.endsWith('+xml') || mime.endsWith('+json')) return true;
  return [
    'application/pdf',
    'application/json',
    'application/xml',
    'application/javascript',
    'application/ecmascript',
    'multipart/x-mixed-replace',
  ].includes(mime);
}

export interface HeadersReceivedDetailsLike {
  requestId: string;
  url: string;
  method: string;
  type: string;
  statusCode: number;
  tabId: number;
  incognito?: boolean;
  cookieStoreId?: string;
  originUrl?: string;
  documentUrl?: string;
  responseHeaders?: readonly WebRequestHeader[];
}

export type BlockingResponseLike = { cancel?: boolean };

function header(details: HeadersReceivedDetailsLike, name: string): string | undefined {
  return details.responseHeaders?.find((h) => h.name.toLowerCase() === name)?.value;
}

/** Classifies a response as a download candidate, or undefined when it renders in the tab. */
export function classifyResponse(
  details: HeadersReceivedDetailsLike,
): { candidate: CaptureCandidate; target: HandoffTarget } | undefined {
  if (details.type !== 'main_frame' && details.type !== 'sub_frame') return undefined;
  if (details.statusCode !== 200 && details.statusCode !== 203) return undefined;
  // A POST (often a one-time form token) cannot be safely replayed once the browser's
  // request is cancelled: the browser keeps it (B4).
  if (details.method.toUpperCase() !== 'GET') return undefined;

  const cd = parseContentDisposition(header(details, 'content-disposition'));
  const mime = mimeEssence(header(details, 'content-type'));
  const attachment = cd?.type === 'attachment';
  if (!attachment && rendersInline(mime)) return undefined;

  const lengthHeader = header(details, 'content-length');
  const length = lengthHeader !== undefined && /^\d+$/.test(lengthHeader.trim()) ? Number(lengthHeader) : undefined;

  const candidate: CaptureCandidate = { url: details.url };
  if (details.incognito) candidate.incognito = true;
  if (cd?.filename) candidate.filename = cd.filename;
  if (mime) candidate.mime = mime;
  if (length !== undefined && Number.isSafeInteger(length)) candidate.size = length;

  if (Number.isSafeInteger(details.tabId) && details.tabId >= 0) candidate.tabId = details.tabId;

  const target: HandoffTarget = { url: details.url };
  if (details.cookieStoreId) target.storeId = details.cookieStoreId;
  if (cd?.filename) target.filename = cd.filename;
  if (mime) target.mime = mime;
  if (candidate.size !== undefined) target.size = candidate.size;
  const referrer = details.type === 'main_frame' ? details.originUrl : details.documentUrl;
  if (referrer && /^https?:/i.test(referrer)) {
    target.referrer = referrer;
    candidate.pageUrl = referrer;
  }
  return { candidate, target };
}

/** Remembers URLs the headers path already tried, so onCreated does not retry them. */
export class AttemptMemory {
  readonly #seen = new Map<string, number>();
  constructor(
    private readonly ttlMs: number = ATTEMPT_MEMORY_MS,
    private readonly now: () => number = () => Date.now(),
  ) {}

  mark(url: string): void {
    this.#seen.delete(url);
    this.#seen.set(url, this.now());
    for (const [u, ts] of this.#seen) {
      if (this.now() - ts <= this.ttlMs) break;
      this.#seen.delete(u);
    }
  }

  /** True once per marked URL within the TTL. */
  consume(url: string): boolean {
    const ts = this.#seen.get(url);
    if (ts === undefined) return false;
    this.#seen.delete(url);
    return this.now() - ts <= this.ttlMs;
  }
}

export interface FirefoxCaptureDeps {
  decide: (candidate: CaptureCandidate) => CaptureDecision | Promise<CaptureDecision>;
  handOff: (target: HandoffTarget, timeoutMs: number) => Promise<Outcome<unknown>>;
  attempts: AttemptMemory;
  timeoutMs?: number;
  timers?: Timers;
  onError?: (error: unknown) => void;
}

/**
 * Blocking onHeadersReceived listener: resolves `{cancel: true}` once the daemon accepted
 * the download, `{}` on error or after the timeout so the browser keeps it. The add gets
 * what is left of `timeoutMs` and its own timer decides; the listener only abandons a
 * hand-off that breaks that contract.
 */
export function createHeadersReceivedListener(
  deps: FirefoxCaptureDeps,
): (details: HeadersReceivedDetailsLike) => BlockingResponseLike | Promise<BlockingResponseLike> {
  const timeoutMs = deps.timeoutMs ?? FIREFOX_HEADERS_TIMEOUT_MS;
  const timers = deps.timers ?? systemTimers;

  return (details) => {
    let classified: ReturnType<typeof classifyResponse>;
    try {
      classified = classifyResponse(details);
    } catch (e) {
      deps.onError?.(e);
      return {};
    }
    if (!classified) return {};
    const { candidate, target } = classified;
    const start = timers.now();

    return new Promise<BlockingResponseLike>((resolve) => {
      let done = false;
      const finish = (r: BlockingResponseLike) => {
        if (done) return;
        done = true;
        timers.clearTimeout(timer);
        resolve(r);
      };
      const timer = timers.setTimeout(() => finish({}), timeoutMs + HANDOFF_BACKSTOP_SLACK_MS);
      void (async () => {
        try {
          const decision = await deps.decide(candidate);
          if (!decision.capture || done) return finish({});
          const remaining = timeoutMs - (timers.now() - start);
          if (remaining <= 0) return finish({});
          deps.attempts.mark(details.url);
          const outcome = await deps.handOff(target, remaining);
          finish(outcome.ok ? { cancel: true } : {});
        } catch (e) {
          deps.onError?.(e);
          finish({});
        }
      })();
    });
  };
}

export interface FirefoxFallbackDeps {
  downloads: DownloadsControl;
  decide: (item: DownloadItemLike) => CaptureDecision | Promise<CaptureDecision>;
  handOff: (item: DownloadItemLike, timeoutMs: number) => Promise<Outcome<unknown>>;
  attempts: AttemptMemory;
  ownExtensionId?: string;
  timeoutMs?: number;
  timers?: Timers;
  onResult?: (item: DownloadItemLike, result: HandoffResult) => void;
  onError?: (error: unknown) => void;
}

/** downloads.onCreated fallback for downloads the headers path did not see. */
export function createDownloadCreatedListener(deps: FirefoxFallbackDeps): (item: DownloadItemLike) => void {
  const timeoutMs = deps.timeoutMs ?? CHROMIUM_HANDOFF_TIMEOUT_MS;
  return (item) => {
    if (deps.attempts.consume(item.url) || (item.finalUrl && deps.attempts.consume(item.finalUrl))) return;
    if (deps.ownExtensionId && item.byExtensionId === deps.ownExtensionId) return;
    if (item.state !== undefined && item.state !== 'in_progress') return;
    void (async () => {
      try {
        const decision = await deps.decide(item);
        if (!decision.capture) return;
        const result = await pausedHandOff(item.id, {
          downloads: deps.downloads,
          handOff: () => deps.handOff(item, timeoutMs),
          timeoutMs,
          ...(deps.timers ? { timers: deps.timers } : {}),
        });
        deps.onResult?.(item, result);
      } catch (e) {
        deps.onError?.(new Error(errorMessage(e)));
      }
    })();
  };
}

