// Builds and sends one `add` for a direct MP4 file a page played (VIDEO-DETECT-SPEC §2.3, §3.7):
// an ordinary download (segmented engine, ranged resume) with the headers the browser sent for
// the file, and records it in the hand-off history. Never `cookies.getAll` (§6.3, §9 #3): a file
// carries only the Cookie the browser itself sent with its request. Pure: the client is injected.

import { baseName, defaultPolicyReferer, filterHeaders, hasHeader, headerValue } from './context';
import type { AddSender } from './handoff';
import type { HandoffHistory, HandoffRecord } from './history';
import { httpUrl } from './media';
import { cleanTitle } from './media-handoff';
import { type AddedResult, type AddFields, type HeaderPair, type Outcome, errorMessage, failure } from './protocol';

/** How long the extension waits for `added` (and the relay's deadline): the add path's 15 s. */
export const FILE_ADD_TIMEOUT_MS = 15_000;
/** Longest file name sent, extension included. */
export const MAX_FILE_NAME_CHARS = 200;
/** Last path segments that name nothing (`video.mp4`, `play`, …). */
const GENERIC_NAMES: ReadonlySet<string> = new Set(['video', 'index', 'file', 'media', 'download', 'play', 'stream']);
const MP4_EXTENSION = /\.(mp4|m4v)$/i;

/** What a file hand-off is built from: a recorded file item, or an element's own source. */
export interface FileSource {
  /** The final URL (an element file: the element's `currentSrc`). */
  url: string;
  /** The first URL of the redirect chain, which the engine requests (§2.3). */
  requestUrl?: string;
  /** The headers the browser sent for the request (filtered); undefined when none were recorded. */
  headers?: HeaderPair[];
  /** Total bytes; 0 unknown. */
  size: number;
  /** The Content-Disposition file name. */
  filename?: string;
  /** The playing frame's address: the source of a Referer when no headers were recorded. */
  frameUrl?: string;
  /** The tab's title. */
  pageTitle?: string;
}

function decodeSegment(seg: string): string {
  try {
    return decodeURIComponent(seg);
  } catch {
    return seg;
  }
}

/** The last path segment of a URL when it names the video (§3.7); undefined for a generic one. */
export function meaningfulSegment(url: string | undefined): string | undefined {
  if (!url) return undefined;
  let seg: string;
  try {
    seg = decodeSegment(new URL(url).pathname.split('/').pop() ?? '').trim();
  } catch {
    return undefined;
  }
  const stem = seg.replace(MP4_EXTENSION, '').trim().toLowerCase();
  if (stem === '' || GENERIC_NAMES.has(stem) || /^[0-9]+$/.test(stem) || /^[0-9a-f]{24,}$/.test(stem)) return undefined;
  return seg;
}

/**
 * The file name of a hand-off: the Content-Disposition name, else the URL's last path segment
 * when it names the video, else the tab's title, else `video`; cleaned and cut to
 * MAX_FILE_NAME_CHARS with a `.mp4` extension (`.m4v` becomes `.mp4`).
 */
export function fileName(src: Pick<FileSource, 'filename' | 'url' | 'requestUrl' | 'pageTitle'>): string {
  const candidates = [baseName(src.filename), meaningfulSegment(src.url), meaningfulSegment(src.requestUrl), src.pageTitle];
  for (const c of candidates) {
    const stem = cleanTitle(c?.replace(MP4_EXTENSION, ''));
    if (!stem) continue;
    const cut = [...stem].slice(0, MAX_FILE_NAME_CHARS - '.mp4'.length).join('').trim();
    if (cut) return `${cut}.mp4`;
  }
  return 'video.mp4';
}

/** History keeps a file's address without its query (signatures, tokens) or fragment. */
function historyUrl(url: string): string {
  try {
    const u = new URL(url);
    u.search = '';
    u.hash = '';
    return u.href;
  } catch {
    return url;
  }
}

export interface FileFieldsDeps {
  userAgent: string;
  timeoutMs?: number;
}

/**
 * The `add` fields of a file: the first URL of its redirect chain, the headers the browser sent
 * for it (never another Cookie; without recorded headers, no Cookie at all and a default-policy
 * Referer of the playing frame), a User-Agent when missing, the name of fileName, `video/mp4`
 * and the known size (else -1).
 */
export function buildFileAddFields(src: FileSource, deps: FileFieldsDeps): Outcome<AddFields> {
  const url = httpUrl(src.requestUrl) ?? httpUrl(src.url);
  if (!url) return failure('invalid_request', 'the video address cannot be handed to Downex');
  let headers = filterHeaders(src.headers ?? []);
  if (src.headers === undefined) {
    const referer = defaultPolicyReferer(httpUrl(src.frameUrl), url);
    if (referer) headers.push(['Referer', referer]);
  }
  if (deps.userAgent && !hasHeader(headers, 'user-agent')) headers.push(['User-Agent', deps.userAgent]);
  headers = filterHeaders(headers);
  const fields: AddFields = {
    url,
    method: 'GET',
    headers,
    filename: fileName(src),
    mime: 'video/mp4',
    size: Number.isSafeInteger(src.size) && src.size > 0 ? src.size : -1,
    deadlineMs: deps.timeoutMs ?? FILE_ADD_TIMEOUT_MS,
  };
  // Only the Referer actually sent (or derived above): the relay adds `referrer` as one.
  const referrer = headerValue(headers, 'referer');
  if (referrer) fields.referrer = referrer;
  return { ok: true, value: fields };
}

export interface FileHandoffDeps extends FileFieldsDeps {
  client: AddSender;
  history?: HandoffHistory;
  now?: () => number;
  onError?: (error: unknown) => void;
}

/** Never rejects; resolves within the add timeout. */
export type FileHandOff = (src: FileSource) => Promise<Outcome<AddedResult>>;

export function createFileHandOff(deps: FileHandoffDeps): FileHandOff {
  const now = deps.now ?? (() => Date.now());
  const timeoutMs = deps.timeoutMs ?? FILE_ADD_TIMEOUT_MS;

  const record = (fields: AddFields | undefined, src: FileSource, outcome: Outcome<AddedResult>) => {
    if (!deps.history) return;
    const rec: HandoffRecord = { ts: now(), url: historyUrl(fields?.url ?? src.url), source: 'video-file', ok: outcome.ok };
    const filename = fields?.filename ?? fileName(src);
    if (filename) rec.filename = filename;
    const title = cleanTitle(src.pageTitle);
    if (title) rec.title = title;
    if (outcome.ok) {
      rec.downloadId = outcome.value.downloadId;
      if (outcome.value.awaiting !== undefined) rec.awaiting = outcome.value.awaiting;
    } else {
      rec.error = outcome.error;
    }
    deps.history.add(rec).catch((e: unknown) => deps.onError?.(e));
  };

  return async (src) => {
    let fields: AddFields | undefined;
    let outcome: Outcome<AddedResult>;
    try {
      const built = buildFileAddFields(src, { ...deps, timeoutMs });
      if (!built.ok) {
        outcome = built;
      } else {
        fields = built.value;
        const sent = fields;
        outcome = await deps.client.add(
          { ...sent, headers: [...sent.headers] },
          {
            timeoutMs,
            // The relay drops what misses the deadline, but an added download is shown.
            onLate: (late) => {
              if (late.ok) record(sent, src, late);
            },
          },
        );
      }
    } catch (e) {
      outcome = failure('internal', errorMessage(e));
    }
    record(fields, src, outcome);
    return outcome;
  };
}
