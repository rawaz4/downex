// Save what's playing, the background's part (SITE-MEDIA-SPEC §2.9): for the clicked `<video>`
// of a frame, the candidates from the tab's PlayedLog and the page's resource timing, their first
// 64 KiB read again as the player read them, `probePlayed` (Downex picks the files of the video by
// its duration) and `addPlayed`. One state per element (tab, frame, and the element's identity:
// never its length, which two posts share), probed again when the candidates or the element's
// duration change while nothing matched yet, at most MAX_PLAYED_PROBES times, and never while the
// element has no duration yet. Nothing is fetched until the user clicks; no cookie is ever read.

import { toBase64 } from './page-fetch';
import { contentLengthSize, contentRangeTotal } from './frame-media';
import { candidates, looksLikeMp4, playedAddressOk, type PlayedCandidate, type PlayedResource, type PlayedTrace, readUpTo } from './played';
import { mimeEssence } from './rules';
import {
  type AddedResult,
  daemonCode,
  type Outcome,
  type PlayedFile,
  type PlayedSourceFields,
  type ProtocolError,
  type RequestOptions,
  type AddPlayedFields,
  type ProbePlayedFields,
  type VideoInfoResult,
} from './protocol';
import type { VideoTaskState } from './messages';

/** Bytes of each file's head (§2.9 "The flow" 4). */
export const HEAD_BYTES = 64 * 1024;
/** Heads read at once, and how long heads stay known. */
export const HEAD_LANES = 4;
export const HEAD_TTL_MS = 10 * 60_000;
const MAX_HEADS = 64;
/** Probes of one element at most (the candidates changed while nothing matched). */
export const MAX_PLAYED_PROBES = 5;
/** Elements remembered at most. */
const MAX_ELEMENTS = 32;
export const PLAYED_PROBE_TIMEOUT_MS = 30_000;
export const PLAYED_ADD_TIMEOUT_MS = 35_000;
/** The key of a frame's Played offer. */
export const PLAYED_KEY = 'p1';

/** A file's head as read: its bytes, total, and whether the tab (and the page's credentials) was needed. */
export interface FileHead {
  head: Uint8Array;
  /** Exact total, 0 = unknown. */
  total: number;
  browser: boolean;
  include: boolean;
}

/** What the background asks Downex for played videos. */
export interface PlayedClient {
  probePlayed(fields: ProbePlayedFields, options?: RequestOptions<VideoInfoResult>): Promise<Outcome<VideoInfoResult>>;
  addPlayed(fields: AddPlayedFields, options?: RequestOptions<AddedResult>): Promise<Outcome<AddedResult>>;
}

export interface PlayedHandoffDeps {
  client: PlayedClient;
  /** The tab's played files (PlayedLog.resources). */
  resources(tabId: number): Promise<PlayedResource[]>;
  /**
   * Reads a file's head (playedHeadReader): the background first, then the tab's frame; `logged`: the
   * tab's PlayedLog saw the browser answer it as media (only such a file may carry the page's credentials);
   * `direct`: the browser got that answer from a public address (only such a file is read by the background).
   */
  readHead(tabId: number, frameId: number, url: string, logged: boolean, direct: boolean): Promise<FileHead | undefined>;
  now?: () => number;
  onError?: (e: unknown) => void;
}

/** The clicked element of a frame. */
export interface PlayedClick {
  tabId: number;
  frameId: number;
  /** The frame's address (a hint for Downex). */
  pageUrl: string;
  trace: PlayedTrace;
}

/** What a frame's panel knows of its Played source (offer-types PlayedOffer). */
export interface PlayedState {
  probe?: VideoTaskState<VideoInfoResult>;
  /** How long the panel has waited for a file of this video (ms). */
  waitingMs: number;
}

interface Element {
  firstAt: number;
  usedAt: number;
  signature: string;
  probes: number;
  running: boolean;
  /** What the element said when it was last probed (its length and picture). */
  probedFor?: Pick<PlayedTrace, 'durationMs' | 'width' | 'height'>;
  result?: Outcome<VideoInfoResult>;
  source?: PlayedSourceFields;
  /** The files' addresses by id, and those whose head needed the page's credentials. */
  urls: Map<string, string>;
  include: Set<string>;
}

/** A refusal that a later probe may answer: nothing matched yet, or Downex was not reached. */
export function playedRetryable(e: ProtocolError): boolean {
  if (daemonCode(e) === 'not_found_yet') return true;
  return ['timeout', 'disconnected', 'daemon_unavailable', 'internal'].includes(e.code);
}

/** Whether a probe's answer means "keep waiting" (no file of this video yet). */
export function playedWaiting(o: Outcome<VideoInfoResult> | undefined): boolean {
  return o !== undefined && !o.ok && daemonCode(o.error) === 'not_found_yet';
}

function elementKey(c: PlayedClick): string {
  return `${c.tabId}:${c.frameId}:${c.trace.element}`;
}

/**
 * Whether the element now plays another video than the one it was probed for (a feed that loads
 * another post into the same `<video>`, review 2026-11-18): its length moved by more than the
 * video tolerance (max(0.3 s, 1.5 %), internal/played) or its picture's shape changed (2 %).
 * A length learnt (it had none) or a quality switch of the same picture shape is the same video.
 */
export function playsAnother(was: Pick<PlayedTrace, 'durationMs' | 'width' | 'height'>, now: Pick<PlayedTrace, 'durationMs' | 'width' | 'height'>): boolean {
  if (was.durationMs > 0 && now.durationMs > 0 && Math.abs(was.durationMs - now.durationMs) > Math.max(300, was.durationMs * 0.015)) return true;
  if (was.width > 0 && was.height > 0 && now.width > 0 && now.height > 0) {
    const a = was.width / was.height;
    const b = now.width / now.height;
    if (Math.abs(a - b) > 0.02 * a) return true;
  }
  return false;
}

/** One try of a head read: the head, `not-media` (answered with something that is not media: never asked again), or nothing. */
export type HeadTry = FileHead | 'not-media' | undefined;

/** Whether a response's Content-Type may be a media file's: video, audio, an octet stream, or none. */
export function mediaAnswer(contentType: string | undefined): boolean {
  const t = mimeEssence(contentType) ?? '';
  return t === '' || t.startsWith('video/') || t.startsWith('audio/') || t === 'application/octet-stream' || t === 'binary/octet-stream';
}

export interface HeadReaderDeps {
  /** The background's own request (backgroundHead): no credentials. */
  background(url: string): Promise<HeadTry>;
  /** The tab's frame's request (the page's own network stack, §2.8). */
  page(tabId: number, frameId: number, url: string, credentials: 'omit' | 'include'): Promise<HeadTry>;
  /** Whether the file is on the site of the tab's page. */
  onSite(tabId: number, url: string): Promise<boolean>;
}

/**
 * A played file's head (§2.9 "The flow" 4): the background's own request first, only for a file the
 * browser fetched for the tab's player from a public address (`direct`: the background's request
 * follows no local network rule, and a host name may point at this computer); then the tab's frame
 * without credentials (the page's own network stack and its rules), then, only for a file the tab's
 * player itself fetched as media (`logged`) on the page's site, with the page's credentials (what the
 * browser attaches for the page; the extension reads no cookie). An answer that is not media ends the
 * read at once: the address is never requested again, and never with credentials.
 */
export function playedHeadReader(deps: HeadReaderDeps): PlayedHandoffDeps['readHead'] {
  return async (tabId, frameId, url, logged, direct) => {
    const own = direct ? await deps.background(url) : undefined;
    if (own === 'not-media') return undefined;
    if (own) return own;
    const omit = await deps.page(tabId, frameId, url, 'omit');
    if (omit === 'not-media') return undefined;
    if (omit) return omit;
    if (!logged || !(await deps.onSite(tabId, url))) return undefined;
    const include = await deps.page(tabId, frameId, url, 'include');
    return include === 'not-media' ? undefined : include;
  };
}

/** A fetch as the background makes it (the global `fetch`; a fake in tests). */
export type FetchLike = (url: string, init: RequestInit) => Promise<Response>;

/**
 * A file's head read by the background itself (§2.9 "The flow" 4): `Range: bytes=0-65535`, without
 * credentials, following the server's redirects (measured 2026-11-18: Instagram's and Facebook's
 * files answer the background's request through a redirect, and an unfollowed one is no answer at
 * all), and refusing the answer before its body when the address it ended at is one Downex never
 * fetches (playedAddressOk). An answer that is not media is never read ('not-media'); a 206's
 * Content-Range, else a 200's Content-Length, gives the total.
 */
export async function backgroundHead(fetchFn: FetchLike, url: string, timeoutMs = 8000): Promise<HeadTry> {
  if (!playedAddressOk(url)) return 'not-media';
  const ctl = new AbortController();
  const timer = setTimeout(() => ctl.abort(), timeoutMs);
  try {
    const res = await fetchFn(url, {
      headers: { Range: `bytes=0-${HEAD_BYTES - 1}` },
      credentials: 'omit',
      cache: 'no-store',
      redirect: 'follow',
      signal: ctl.signal,
    });
    if (res.url && res.url !== url && !playedAddressOk(res.url)) return 'not-media';
    if (res.status !== 200 && res.status !== 206) return undefined;
    if (!mediaAnswer(res.headers.get('content-type') ?? undefined)) return 'not-media';
    const total =
      res.status === 206
        ? (contentRangeTotal(res.headers.get('content-range') ?? undefined) ?? 0)
        : (contentLengthSize(res.headers.get('content-length') ?? undefined) ?? 0);
    const head = await readUpTo(res, HEAD_BYTES);
    return looksLikeMp4(head) ? { head, total, browser: false, include: false } : undefined;
  } catch {
    return undefined;
  } finally {
    clearTimeout(timer);
    ctl.abort();
  }
}

export class PlayedHandoff {
  readonly #deps: PlayedHandoffDeps;
  readonly #now: () => number;
  readonly #elements = new Map<string, Element>();
  readonly #heads = new Map<string, { at: number; head: FileHead | undefined }>();

  constructor(deps: PlayedHandoffDeps) {
    this.#deps = deps;
    this.#now = deps.now ?? (() => Date.now());
  }

  #element(c: PlayedClick): Element {
    const key = elementKey(c);
    let e = this.#elements.get(key);
    if (!e) {
      e = { firstAt: this.#now(), usedAt: this.#now(), signature: '', probes: 0, running: false, urls: new Map(), include: new Set() };
      this.#elements.set(key, e);
      while (this.#elements.size > MAX_ELEMENTS) this.#elements.delete(this.#elements.keys().next().value as string);
    }
    e.usedAt = this.#now();
    return e;
  }

  /**
   * The Played source of a click (a panel's poll): its state now, after starting a probe when none
   * was made, or when the candidates changed while nothing matched yet.
   */
  async state(c: PlayedClick): Promise<PlayedState> {
    let e = this.#element(c);
    if (e.probedFor && !e.running && playsAnother(e.probedFor, c.trace)) {
      // Another post in the same element: what was found for the last one is never this one's.
      this.#elements.delete(elementKey(c));
      e = this.#element(c);
    }
    const cands = candidates(await this.#deps.resources(c.tabId), c.trace.recent, this.#now());
    // The element's duration is part of what Downex matches on: once it is known, probe again.
    const signature = [c.trace.durationMs, ...cands.map((x) => x.key)].join('\n');
    const again =
      !e.running &&
      // An element that has not loaded has no duration: nothing is "this video" yet (keep waiting).
      c.trace.durationMs > 0 &&
      cands.length > 0 &&
      e.probes < MAX_PLAYED_PROBES &&
      (e.result === undefined || (!e.result.ok && playedRetryable(e.result.error) && signature !== e.signature));
    if (again) {
      e.running = true;
      e.signature = signature;
      e.probes++;
      e.probedFor = { durationMs: c.trace.durationMs, width: c.trace.width, height: c.trace.height };
      void this.#probe(c, e, cands)
        .catch((err: unknown) => this.#deps.onError?.(err))
        .finally(() => {
          e.running = false;
        });
    }
    const state: PlayedState = { waitingMs: this.#now() - e.firstAt };
    if (e.running && !e.result) state.probe = { running: true };
    else if (e.result) state.probe = { result: e.result };
    return state;
  }

  /** Reads the heads of the candidates (HEAD_LANES at a time), cached for HEAD_TTL_MS. */
  async #heads4(c: PlayedClick, cands: readonly PlayedCandidate[]): Promise<(FileHead | undefined)[]> {
    const out: (FileHead | undefined)[] = new Array(cands.length).fill(undefined);
    let next = 0;
    const lane = async () => {
      for (;;) {
        const i = next++;
        if (i >= cands.length) return;
        const cand = cands[i] as PlayedCandidate;
        const url = cand.key;
        const known = this.#heads.get(`${cand.logged ? 1 : 0}${cand.direct ? 1 : 0}${url}`);
        if (known && this.#now() - known.at < HEAD_TTL_MS) {
          out[i] = known.head;
          continue;
        }
        let head: FileHead | undefined;
        try {
          head = await this.#deps.readHead(c.tabId, c.frameId, url, cand.logged, cand.direct);
        } catch {
          head = undefined;
        }
        this.#heads.set(`${cand.logged ? 1 : 0}${cand.direct ? 1 : 0}${url}`, { at: this.#now(), head });
        while (this.#heads.size > MAX_HEADS) this.#heads.delete(this.#heads.keys().next().value as string);
        out[i] = head;
      }
    };
    await Promise.all(Array.from({ length: Math.min(HEAD_LANES, cands.length) }, lane));
    return out;
  }

  async #probe(c: PlayedClick, e: Element, cands: readonly PlayedCandidate[]): Promise<void> {
    const heads = await this.#heads4(c, cands);
    const files: PlayedFile[] = [];
    const urls = new Map<string, string>();
    const include = new Set<string>();
    cands.forEach((cand, i) => {
      const h = heads[i];
      if (!h) return;
      const id = `r${files.length + 1}`;
      files.push({
        id,
        url: cand.key,
        size: h.total > 0 ? h.total : cand.total,
        kind: cand.kind,
        head: toBase64(h.head),
        browser: h.browser,
      });
      urls.set(id, cand.key);
      if (h.include) include.add(cand.key);
    });
    if (files.length === 0) {
      e.result = { ok: false, error: { code: 'rejected', message: 'not_found_yet: no file of this video could be read', reason: 'not_found_yet' } };
      return;
    }
    const source: PlayedSourceFields = {
      resources: files,
      durationMs: c.trace.durationMs,
      videoWidth: c.trace.width,
      videoHeight: c.trace.height,
      hasAudio: c.trace.audio,
    };
    if (c.trace.others.length > 0) source.otherDurationsMs = c.trace.others.slice(0, 8);
    const o = await this.#deps.client.probePlayed({ pageUrl: c.pageUrl, played: source }, { timeoutMs: PLAYED_PROBE_TIMEOUT_MS });
    e.result = o;
    if (o.ok) {
      e.source = source;
      e.urls = urls;
      e.include = include;
    }
  }

  /**
   * Adds the clicked video with the tracks chosen ("" = Downex's default pick): the probed source
   * again, and the addresses its fetch ticket may name (§2.9 "The ticket's addresses").
   */
  async add(
    c: PlayedClick,
    pick: { video: string; audio: string },
    title: string,
  ): Promise<{ outcome: Outcome<AddedResult>; urls: string[]; include: string[] }> {
    const e = this.#element(c);
    if (!e.source || !e.result?.ok || (e.probedFor && playsAnother(e.probedFor, c.trace))) {
      return { outcome: { ok: false, error: { code: 'expired', message: 'This video is not known yet. Let it play, then click Download again.' } }, urls: [], include: [] };
    }
    const fields: AddPlayedFields = { pageUrl: c.pageUrl, played: e.source };
    if (pick.video) fields.videoTrack = pick.video;
    if (pick.audio) fields.audioTrack = pick.audio;
    if (title) fields.title = title.slice(0, 200);
    const outcome = await this.#deps.client.addPlayed(fields, { timeoutMs: PLAYED_ADD_TIMEOUT_MS });
    return { outcome, urls: [...e.urls.values()], include: [...e.include] };
  }
}
