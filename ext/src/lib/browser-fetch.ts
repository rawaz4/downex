// Browser-fetched saves, the background's part (SITE-MEDIA-SPEC §2.8). Some sites' video servers
// answer only web browsers; downexd then hands a download's requests to the browser that added
// it. The background keeps that download's fetch ticket, asks Downex for its requests
// (`fetchTake`), picks the address (the tab player's own address of the same resource when it saw
// one) and the frame (the click's, else a tab of the same site), has the frame's content script
// make the request (page-fetch.ts) and forwards the answer, piece by acknowledged piece, with
// `fetchPut`. It never reads cookies for this, never reads bodies of the page's own requests, and
// never runs for a look-ahead.

import { FETCH_PORT, type FetchCommand, type FetchReport, type PortLike } from './page-fetch';
import {
  type BrowserFetchTask,
  type FetchAckResult,
  type FetchPutParams,
  type FetchTaskResult,
  MAX_FETCH_TICKETS,
  type Outcome,
  type RequestOptions,
  type Timers,
  type VideoInfoResult,
  failure,
  isFetchTicket,
  systemTimers,
} from './protocol';
import {
  MAX_STORED_PLAYED_URL,
  PLAYED_PER_TAB,
  PLAYED_TTL_MS,
  type PlayedResource,
  playedAddressOk,
  type PlayedResponse,
  playedResponse,
} from './played';

export { PLAYED_PER_TAB, PLAYED_TTL_MS };

/** How long a take may wait for a request, and how long the extension waits for its answer. */
export const FETCH_WAIT_MS = 20_000;
export const FETCH_TAKE_TIMEOUT_MS = FETCH_WAIT_MS + 6_000;
/** A put's answer comes once Downex has read the piece. */
export const FETCH_PUT_TIMEOUT_MS = 35_000;
/** Downex not running, or a failed take: the next one after this. */
export const FETCH_RETRY_MS = 5_000;
/** Transfers at once (downexd's browserfetch.Lanes). */
export const FETCH_LANES = 2;
/** Tickets kept; the oldest go first. */
export const MAX_KEPT_TICKETS = MAX_FETCH_TICKETS;
/** How often the background removes expired entries from storage.session while it runs. */
export const PLAYED_SWEEP_MS = 5 * 60_000;
/** Closed tabs remembered, so that a response recorded as one closed is never written back. */
const MAX_CLOSED_TABS = 1024;

/** Storage keys (tickets: the ticket area; played: storage.session). */
export const TICKETS_KEY = 'browserFetchTickets';
export const PLAYED_PREFIX = 'played:';

/** The site a download's requests may be made from (SITE-MEDIA-SPEC §6.3's scope). */
export interface SiteScope {
  domain: string;
  /** Only that host (a generic page), else the domain and its subdomains. */
  exact: boolean;
}

/** A fetch ticket and the click it came from. */
export interface TicketRecord {
  ticket: string;
  downloadId: number | string;
  tabId: number;
  frameId: number;
  pageUrl: string;
  /** Cookie store (Firefox container) of the tab; "" for the default store. */
  storeId: string;
  scope: SiteScope;
  /**
   * The add carried the user's sign-in (its anonymous request was answered `auth_required`,
   * SITE-MEDIA-SPEC §6.3): only then do the fetches carry the site's credentials. Missing: no.
   */
  signedIn?: boolean;
  /**
   * A played video's ticket (SITE-MEDIA-SPEC §2.9): the only addresses its fetches may name (the
   * files the extension handed over), and those whose head needed the page's credentials.
   */
  played?: { urls: string[]; include: string[] };
  at: number;
}

/** The storage.session area used here (in memory, gone when the browser exits). */
export interface SessionArea {
  get(key: string): Promise<Record<string, unknown>>;
  set(items: Record<string, unknown>): Promise<void>;
  remove(key: string): Promise<void>;
  /** Every item (`get(null)`); missing: the sweep sees only the tabs this background read. */
  getAll?(): Promise<Record<string, unknown>>;
}

/** Whether `host` is on a scope: the domain or, unless exact, a subdomain of it. */
export function hostInScope(host: string, scope: SiteScope): boolean {
  const h = host.toLowerCase();
  return scope.exact ? h === scope.domain : h === scope.domain || h.endsWith(`.${scope.domain}`);
}

/** The host of an http(s) address, "" otherwise. */
export function httpHost(url: string | undefined): string {
  if (!url) return '';
  try {
    const u = new URL(url);
    return u.protocol === 'https:' || u.protocol === 'http:' ? u.hostname.toLowerCase() : '';
  } catch {
    return '';
  }
}

// ---------------------------------------------------------------------------------------
// Tickets

function isScope(v: unknown): v is SiteScope {
  const s = v as SiteScope;
  return typeof s === 'object' && s !== null && typeof s.domain === 'string' && s.domain !== '' && typeof s.exact === 'boolean';
}

function isRecordOf(v: unknown): v is TicketRecord {
  const r = v as TicketRecord;
  return (
    typeof r === 'object' &&
    r !== null &&
    isFetchTicket(r.ticket) &&
    (typeof r.downloadId === 'number' || typeof r.downloadId === 'string') &&
    Number.isSafeInteger(r.tabId) &&
    Number.isSafeInteger(r.frameId) &&
    typeof r.pageUrl === 'string' &&
    typeof r.storeId === 'string' &&
    isScope(r.scope) &&
    (r.signedIn === undefined || typeof r.signedIn === 'boolean') &&
    (r.played === undefined ||
      (typeof r.played === 'object' &&
        r.played !== null &&
        Array.isArray(r.played.urls) &&
        r.played.urls.every((u) => typeof u === 'string') &&
        Array.isArray(r.played.include) &&
        r.played.include.every((u) => typeof u === 'string'))) &&
    Number.isFinite(r.at)
  );
}

/**
 * The tickets this browser holds, in the area given (the background gives storage.local, so that a
 * download downexd kept the ticket of is fetched again after the browser restarted, SITE-MEDIA-SPEC
 * §2.8 amendment 2026-11-19; memory where there is none). A ticket leaves when Downex names it ended.
 */
export class FetchTickets {
  readonly #area: SessionArea | undefined;
  #memory: TicketRecord[] = [];

  constructor(area?: SessionArea) {
    this.#area = area;
  }

  async list(): Promise<TicketRecord[]> {
    if (!this.#area) return [...this.#memory];
    try {
      const v = (await this.#area.get(TICKETS_KEY))[TICKETS_KEY];
      return Array.isArray(v) ? v.filter(isRecordOf) : [];
    } catch {
      return [...this.#memory];
    }
  }

  async #save(list: TicketRecord[]): Promise<void> {
    this.#memory = list;
    if (!this.#area) return;
    try {
      await this.#area.set({ [TICKETS_KEY]: list });
    } catch {
      // memory only
    }
  }

  async add(rec: TicketRecord): Promise<void> {
    const list = (await this.list()).filter((r) => r.ticket !== rec.ticket);
    list.push(rec);
    await this.#save(list.slice(-MAX_KEPT_TICKETS));
  }

  async get(ticket: string): Promise<TicketRecord | undefined> {
    return (await this.list()).find((r) => r.ticket === ticket);
  }

  async remove(tickets: readonly string[]): Promise<void> {
    if (tickets.length === 0) return;
    const list = await this.list();
    const kept = list.filter((r) => !tickets.includes(r.ticket));
    if (kept.length !== list.length) await this.#save(kept);
  }
}

// ---------------------------------------------------------------------------------------
// The media the tabs' pages fetched (SITE-MEDIA-SPEC §2.8 "The address", §2.9 "The flow" 1)

/** The webRequest.onResponseStarted fields read here. */
export type PlayedDetails = PlayedResponse;

/** Tabs whose files are written to storage.session at most (the others stay in memory). */
export const PLAYED_STORED_TABS = 16;
/** Files of a tab written to storage.session at most (the newest). */
export const PLAYED_STORED_PER_TAB = 32;

/** The path of an address ("" when it does not parse). */
function pathOf(url: string): string {
  try {
    return new URL(url).pathname;
  } catch {
    return '';
  }
}

function isResource(e: unknown): e is PlayedResource {
  const r = e as PlayedResource;
  return (
    typeof r === 'object' &&
    r !== null &&
    typeof r.key === 'string' &&
    Number.isSafeInteger(r.total) &&
    (r.kind === 'video' || r.kind === 'audio' || r.kind === '') &&
    Number.isSafeInteger(r.frameId) &&
    Number.isFinite(r.firstAt) &&
    Number.isFinite(r.lastAt) &&
    (r.direct === undefined || typeof r.direct === 'boolean')
  );
}

/**
 * What each tab's pages fetched of media (PlayedLog, SITE-MEDIA-SPEC §2.9): per tab at most
 * PLAYED_PER_TAB files for PLAYED_TTL_MS by their whole-file key, with their total, kind, frame and
 * request times; storage.session holds the newest PLAYED_STORED_PER_TAB of at most
 * PLAYED_STORED_TABS tabs (addresses of at most MAX_STORED_PLAYED_URL), so the log outlives the
 * background's restarts but never the browser; a write it refuses leaves the log in memory. Expired
 * files leave it (`sweep`, and whenever a tab's list is read), and a closed tab's for good. The
 * requests the extension itself makes are never recorded (`ignore`). Never sent anywhere but, on a
 * click, to the local Downex.
 */
export class PlayedLog {
  readonly #area: SessionArea | undefined;
  readonly #now: () => number;
  readonly #tabs = new Map<number, PlayedResource[]>();
  /** A tab's list being read from storage.session: every caller waits for the one read. */
  readonly #loading = new Map<number, Promise<PlayedResource[] | undefined>>();
  readonly #dirty = new Set<number>();
  /** Tabs that closed: a response recorded while one closed is never written back. */
  readonly #closed = new Set<number>();
  /** The tabs written to storage.session, oldest write first. */
  readonly #stored: number[] = [];
  /** Addresses the extension fetches itself now (how many times). */
  readonly #ignored = new Map<string, number>();
  #flush: unknown;
  readonly #timers: Timers;

  constructor(area?: SessionArea, opts: { now?: () => number; timers?: Timers } = {}) {
    this.#area = area;
    this.#now = opts.now ?? (() => Date.now());
    this.#timers = opts.timers ?? systemTimers;
  }

  /** A tab's list as storage.session holds it, read once however many callers wait for it. */
  #readStored(tabId: number): Promise<PlayedResource[] | undefined> {
    let p = this.#loading.get(tabId);
    if (!p) {
      const area = this.#area as SessionArea;
      p = (async () => {
        try {
          const v = (await area.get(PLAYED_PREFIX + tabId))[PLAYED_PREFIX + tabId];
          return Array.isArray(v) ? v.filter(isResource) : undefined;
        } catch {
          return [];
        }
      })().finally(() => this.#loading.delete(tabId));
      this.#loading.set(tabId, p);
    }
    return p;
  }

  /**
   * A tab's live files; what expired is dropped here and, through a write, from storage. Callers
   * that overlap a read of storage.session share it, and a list another caller set meanwhile wins
   * (two responses recorded at once both stay).
   */
  async #load(tabId: number): Promise<PlayedResource[]> {
    let list = this.#tabs.get(tabId);
    if (!list && this.#area) {
      const stored = await this.#readStored(tabId);
      list = this.#tabs.get(tabId) ?? stored;
    }
    if (this.#closed.has(tabId)) return [];
    const all = list ?? [];
    const live = all.filter((e) => this.#now() - e.lastAt < PLAYED_TTL_MS);
    this.#tabs.set(tabId, live);
    if (live.length !== all.length) this.#changed(tabId);
    return live;
  }

  /** Marks a tab's list to be written (at most once a second). */
  #changed(tabId: number): void {
    this.#dirty.add(tabId);
    if (this.#area && this.#flush === undefined) {
      this.#flush = this.#timers.setTimeout(() => {
        this.#flush = undefined;
        void this.#write();
      }, 1000);
    }
  }

  /** Never records `url` (and its whole-file key) until as many `unignore` calls. */
  ignore(url: string): void {
    this.#ignored.set(url, (this.#ignored.get(url) ?? 0) + 1);
  }

  unignore(url: string): void {
    const n = (this.#ignored.get(url) ?? 0) - 1;
    if (n > 0) this.#ignored.set(url, n);
    else this.#ignored.delete(url);
  }

  /** Records a response a tab's page fetched (playedResponse); written at most once a second. */
  async record(d: PlayedDetails): Promise<void> {
    if (this.#closed.has(d.tabId) || this.#ignored.has(d.url)) return;
    if (d.ip && !playedAddressOk(`http://${d.ip.includes(':') ? `[${d.ip}]` : d.ip}/`)) {
      // A name that now leads to this computer or a local network: none of its files is trusted.
      const host = httpHost(d.url);
      const list = host ? await this.#load(d.tabId) : [];
      const kept = list.filter((e) => httpHost(e.key) !== host);
      if (kept.length !== list.length) {
        this.#tabs.set(d.tabId, kept);
        this.#changed(d.tabId);
      }
      return;
    }
    const r = playedResponse(d);
    if (!r || this.#ignored.has(r.key)) return;
    await this.#load(d.tabId);
    if (this.#closed.has(d.tabId)) return; // it closed while its list was read
    // The list now, after the read (another record may have added to it meanwhile).
    const list = this.#tabs.get(d.tabId) ?? [];
    const now = this.#now();
    const i = list.findIndex((e) => e.key === r.key);
    const old = i >= 0 ? list[i] : undefined;
    const next: PlayedResource = {
      key: r.key,
      total: r.total > 0 ? r.total : (old?.total ?? 0),
      kind: r.kind || old?.kind || '',
      frameId: d.frameId ?? old?.frameId ?? 0,
      firstAt: old?.firstAt ?? now,
      lastAt: now,
      // Where the browser got it: a cached answer keeps what an earlier one said.
      direct: r.direct || (old?.direct ?? false),
    };
    const kept = list.filter((e) => e.key !== r.key);
    kept.push(next);
    this.#tabs.set(d.tabId, kept.slice(-PLAYED_PER_TAB));
    this.#changed(d.tabId);
  }

  async #write(): Promise<void> {
    if (!this.#area) return;
    for (const tabId of [...this.#dirty]) {
      this.#dirty.delete(tabId);
      if (this.#closed.has(tabId)) continue;
      const list = (this.#tabs.get(tabId) ?? []).filter((e) => e.key.length <= MAX_STORED_PLAYED_URL).slice(-PLAYED_STORED_PER_TAB);
      try {
        if (list.length === 0) {
          await this.#area.remove(PLAYED_PREFIX + tabId);
          this.#forgetStored(tabId);
          continue;
        }
        await this.#area.set({ [PLAYED_PREFIX + tabId]: list });
        this.#forgetStored(tabId);
        this.#stored.push(tabId);
        // The oldest written tabs leave storage.session; their lists stay in memory.
        while (this.#stored.length > PLAYED_STORED_TABS) {
          const oldest = this.#stored.shift() as number;
          await this.#area.remove(PLAYED_PREFIX + oldest);
        }
      } catch {
        // memory only (the area is full or gone)
      }
    }
  }

  #forgetStored(tabId: number): void {
    const i = this.#stored.indexOf(tabId);
    if (i >= 0) this.#stored.splice(i, 1);
  }

  /**
   * Removes what expired (PLAYED_TTL_MS) from storage.session for every tab, also of tabs still
   * open whose pages fetched nothing since. The background runs it when it starts and every
   * PLAYED_SWEEP_MS while it runs.
   */
  async sweep(): Promise<void> {
    const ids = new Set(this.#tabs.keys());
    if (this.#area?.getAll) {
      try {
        for (const key of Object.keys(await this.#area.getAll())) {
          if (!key.startsWith(PLAYED_PREFIX)) continue;
          const id = Number(key.slice(PLAYED_PREFIX.length));
          if (Number.isSafeInteger(id)) ids.add(id);
        }
      } catch {
        // the tabs this background read
      }
    }
    for (const id of ids) await this.#load(id);
    if (this.#flush !== undefined) {
      this.#timers.clearTimeout(this.#flush);
      this.#flush = undefined;
    }
    await this.#write();
  }

  /** Forgets a tab's files for good (it closed), also those of a response being recorded. */
  async forget(tabId: number): Promise<void> {
    this.#closed.add(tabId);
    if (this.#closed.size > MAX_CLOSED_TABS) {
      const oldest = this.#closed.values().next().value;
      if (oldest !== undefined) this.#closed.delete(oldest);
    }
    this.#tabs.delete(tabId);
    this.#dirty.delete(tabId);
    this.#forgetStored(tabId);
    try {
      await this.#area?.remove(PLAYED_PREFIX + tabId);
    } catch {
      // gone with the browser
    }
  }

  /** A tab's files, as recorded. */
  async resources(tabId: number): Promise<PlayedResource[]> {
    return [...(await this.#load(tabId))];
  }

  /** The exact sizes of the media a tab's pages fetched (those whose size is known). */
  async sizes(tabId: number): Promise<number[]> {
    return (await this.#load(tabId)).filter((e) => e.total > 0).map((e) => e.total);
  }

  /**
   * The address the tab's page fetched for the same resource as `url`: the same total size
   * (`total` > 0), preferring the same path, the latest one; undefined when there is none. The size
   * alone is enough: playedOnly lists a quality only for an exact size the player fetched, and the
   * probe's own signed address (another host and path on TikTok) is refused to the browser.
   */
  async match(tabId: number, url: string, total: number): Promise<string | undefined> {
    if (!(total > 0)) return undefined;
    const path = pathOf(url);
    if (!path) return undefined;
    const list = await this.#load(tabId);
    let sized: string | undefined;
    for (let i = list.length - 1; i >= 0; i--) {
      const e = list[i] as PlayedResource;
      if (e.total !== total) continue;
      if (pathOf(e.key) === path) return e.key;
      sized ??= e.key;
    }
    return sized;
  }
}

/** The reason (relay `reason`) of a browser-only page whose player fetched none of its qualities. */
export const PLAY_FIRST = 'play_first';

/**
 * A probe's answer as the panels list it (SITE-MEDIA-SPEC §2.8 "Surfaces", amended 2026-11-19): on
 * a page whose video servers answer only browsers, only the listed video (or muxed) tracks whose
 * exact size is one the tab's player fetched (the only addresses that answer), the first of them
 * the default, and the audio tracks; when the player fetched none, no list but a `play_first`
 * failure ("play it, then download it"), never a row that fails with 403. Otherwise the answer as
 * it is.
 */
export function playedOnly(info: VideoInfoResult, sizes: readonly number[]): Outcome<VideoInfoResult> {
  if (!info.browserFetch) return { ok: true, value: info };
  const played = (t: VideoInfoResult['tracks'][number]) => t.kind !== 'audio' && !t.sizeApprox && t.size > 0 && sizes.includes(t.size);
  const tracks = info.tracks.filter((t) => t.kind === 'audio' || played(t));
  const videos = tracks.filter((t) => t.kind !== 'audio');
  if (videos.length === 0) return failure('rejected', `${PLAY_FIRST}: the player of this tab fetched none of its qualities`, PLAY_FIRST);
  const pick = videos.find((t) => t.id === info.selectedVideo) ?? videos[0]!;
  const audio = tracks.some((t) => t.id === info.selectedAudio) ? info.selectedAudio : '';
  return { ok: true, value: { ...info, tracks, selectedVideo: pick.id, selectedAudio: pick.kind === 'muxed' ? '' : audio } };
}

// ---------------------------------------------------------------------------------------
// The pump

/** What the pump asks the relay. */
export interface FetchClient {
  fetchTake(tickets: string[], waitMs: number, options?: RequestOptions<FetchTaskResult>): Promise<Outcome<FetchTaskResult>>;
  fetchPut(fields: Omit<FetchPutParams, 'type'>, options?: RequestOptions<FetchAckResult>): Promise<Outcome<FetchAckResult>>;
}

/** A frame to fetch from. */
export interface FetchFrame {
  tabId: number;
  frameId: number;
}

export interface FetchPumpDeps {
  client: FetchClient;
  tickets: FetchTickets;
  played: PlayedLog;
  /** The frame a ticket's request is made from (the click's, else a tab of its site); undefined: none. */
  frameFor(rec: TicketRecord): Promise<FetchFrame | undefined>;
  /** Opens a port to a frame's content script (tabs.connect with FETCH_PORT). */
  connect(frame: FetchFrame): PortLike;
  /** "Use my sign-in for videos that need it" now. */
  signInOn(): Promise<boolean>;
  timers?: Timers;
  onError?: (e: unknown) => void;
}

/** The error a fetch puts when no tab of the site is open (downexd's browserfetch.NoTab). */
export const NO_TAB = 'no_tab';
/** The error a played ticket's fetch puts for an address it was not given (SITE-MEDIA-SPEC §2.9). */
export const NOT_LISTED = 'not_listed';

/**
 * Runs the transfers of the tickets this browser holds: one take at a time while a lane is free,
 * at most FETCH_LANES transfers, until no ticket is left.
 */
export class BrowserFetchPump {
  readonly #deps: FetchPumpDeps;
  readonly #timers: Timers;
  #running = false;
  /** A ticket came while the loop ran: it looks again once it ends. */
  #again = false;
  #lanes = 0;
  #laneFree: (() => void) | undefined;

  constructor(deps: FetchPumpDeps) {
    this.#deps = deps;
    this.#timers = deps.timers ?? systemTimers;
  }

  /** Keeps a ticket Downex answered an add with, and starts the pump. */
  async bind(rec: TicketRecord): Promise<void> {
    await this.#deps.tickets.add(rec);
    this.#again = true;
    this.start();
  }

  /** Starts the pump when it is not running (also after the background started again). */
  start(): void {
    if (this.#running) return;
    this.#running = true;
    void this.#loop()
      .catch((e: unknown) => this.#deps.onError?.(e))
      .finally(() => {
        this.#running = false;
        // A ticket kept while the loop was ending (it read none left) is served too.
        if (this.#again) {
          this.#again = false;
          this.start();
        }
      });
  }

  #sleep(ms: number): Promise<void> {
    return new Promise((resolve) => this.#timers.setTimeout(resolve, ms));
  }

  async #loop(): Promise<void> {
    for (;;) {
      const tickets = (await this.#deps.tickets.list()).map((r) => r.ticket);
      if (tickets.length === 0) return;
      while (this.#lanes >= FETCH_LANES) {
        await new Promise<void>((resolve) => {
          this.#laneFree = resolve;
        });
      }
      const o = await this.#deps.client.fetchTake(tickets, FETCH_WAIT_MS, { timeoutMs: FETCH_TAKE_TIMEOUT_MS });
      if (!o.ok) {
        // An older Downex never takes these: the tickets are of no use.
        if (o.error.code === 'outdated_downex' || o.error.code === 'invalid_request') {
          await this.#deps.tickets.remove(tickets);
          return;
        }
        await this.#sleep(FETCH_RETRY_MS);
        continue;
      }
      await this.#deps.tickets.remove(o.value.ended);
      const f = o.value.fetch;
      if (!f) continue;
      this.#lanes++;
      void this.transfer(f)
        .catch((e: unknown) => this.#deps.onError?.(e))
        .finally(() => {
          this.#lanes--;
          const w = this.#laneFree;
          this.#laneFree = undefined;
          w?.();
        });
    }
  }

  #put(fields: Omit<FetchPutParams, 'type'>): Promise<boolean> {
    return this.#deps.client.fetchPut(fields, { timeoutMs: FETCH_PUT_TIMEOUT_MS }).then(
      (o) => !o.ok || o.value.stop,
      () => true,
    );
  }

  /** Makes one request in the page and forwards its answer. */
  async transfer(f: BrowserFetchTask): Promise<void> {
    const base = { ticket: f.ticket, fetchId: f.fetchId };
    const rec = await this.#deps.tickets.get(f.ticket);
    const frame = rec && (await this.#deps.frameFor(rec));
    if (!rec || !frame) {
      await this.#put({ ...base, seq: 0, error: NO_TAB });
      return;
    }
    let url: string;
    let credentials: 'include' | 'omit';
    if (rec.played) {
      // A played video's ticket names only the files handed over (§2.9 "The ticket's addresses"),
      // with the page's credentials only where the file's head needed them.
      if (!rec.played.urls.includes(f.url)) {
        await this.#put({ ...base, seq: 0, error: NOT_LISTED });
        return;
      }
      url = f.url;
      credentials = rec.played.include.includes(url) && hostInScope(httpHost(url), rec.scope) ? 'include' : 'omit';
    } else {
      url = (await this.#deps.played.match(frame.tabId, f.url, f.total)) ?? f.url;
      // The site's credentials only for a video whose add needed the sign-in (§2.8, §6.3).
      credentials = rec.signedIn === true && hostInScope(httpHost(url), rec.scope) && (await this.#deps.signInOn()) ? 'include' : 'omit';
    }
    let port: PortLike;
    try {
      port = this.#deps.connect(frame);
    } catch {
      await this.#put({ ...base, seq: 0, error: NO_TAB });
      return;
    }
    // The extension's own request is never recorded as one the page played.
    this.#deps.played.ignore(url);
    await new Promise<void>((resolve) => {
      let seq = 0;
      let done = false;
      let gone = false;
      // Each report waits for the last one's put: the content script reads no further until
      // it gets `more`, so they come one at a time anyway.
      let chain = Promise.resolve();
      const finish = () => {
        if (done) return;
        done = true;
        try {
          port.disconnect();
        } catch {
          // gone
        }
        resolve();
      };
      const send = async (fields: Omit<FetchPutParams, 'type' | 'ticket' | 'fetchId' | 'seq'>, last: boolean) => {
        let stop: boolean;
        try {
          stop = await this.#put({ ...base, seq: seq++, ...fields });
        } catch {
          stop = true; // downexd unreachable: end this transfer, never leave a lane taken
        }
        if (stop || last) finish();
        else if (!done && !gone) {
          try {
            port.postMessage({ type: 'more' } satisfies FetchCommand);
          } catch {
            // The tab closed during the put: the disconnect handler reports it.
          }
        }
      };
      port.onMessage.addListener((raw) => {
        const r = raw as FetchReport;
        if (done) return;
        chain = chain.then(async () => {
          if (done) return;
          switch (r?.type) {
            case 'head':
              await send(
                {
                  head: {
                    status: r.status,
                    ...(r.contentType ? { contentType: r.contentType } : {}),
                    ...(r.contentRange ? { contentRange: r.contentRange } : {}),
                    contentLength: r.contentLength,
                  },
                },
                false,
              );
              break;
            case 'data':
              await send({ ...(r.data ? { data: r.data } : {}), ...(r.end ? { end: true } : {}) }, r.end);
              break;
            case 'error':
              await send({ error: (r.error || 'fetch failed').slice(0, 200) }, true);
              break;
            default:
              await send({ error: 'unexpected message from the page' }, true);
          }
        });
      });
      port.onDisconnect.addListener(() => {
        readLastError(); // a frame without the content script is expected, not an error
        gone = true;
        if (done) return;
        // No content script in the frame (no head yet), or the tab closed while it sent.
        chain = chain.then(() => (done ? undefined : send({ error: seq === 0 ? NO_TAB : 'the tab was closed' }, true)));
      });
      port.postMessage({ type: 'start', url, start: f.start, end: f.end, credentials } satisfies FetchCommand);
    }).finally(() => this.#deps.played.unignore(url));
  }
}

/** Reads (and so clears) Chrome's lastError after a port disconnect: an expected disconnect is not an error. */
function readLastError(): void {
  try {
    void (globalThis as { chrome?: { runtime?: { lastError?: unknown } } }).chrome?.runtime?.lastError;
  } catch {
    // no chrome namespace (Firefox, tests)
  }
}

export { FETCH_PORT, readLastError };
