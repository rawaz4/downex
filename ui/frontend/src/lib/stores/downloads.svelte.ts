// Download list state (Svelte 5 runes). Downloads live in a plain Map that is
// replaced entry-by-entry from event batches; a revision counter makes the derived
// views (sorted/filtered rows, sidebar counts, selection) recompute once per batch
// instead of tracking 10,000 deep proxies. The sorted order is patched incrementally
// (see SortedView), and responses to bulk actions are coalesced to one revision per
// animation frame.

import {
  DEFAULT_CATEGORIES,
  isActive,
  isResumable,
  toApiError,
  type AddMediaRequest,
  type AddRequest,
  type Api,
  type Category,
  type ConfirmOptions,
  type Download,
  type EventBatch,
  type ExistingFile,
  type MediaInfo,
  type ProbeMediaRequest,
  type ResumeOptions,
  type Status,
  type Unsubscribe,
} from '../api';
import { CategoryIndex, displayName, type CategoryFilter, type StatusFilter } from '../categories';
import { awaitingInOrder } from '../confirm';
import {
  DEFAULT_SORT,
  SortedView,
  countFacets,
  countQueues,
  filterDownloads,
  toggleSort,
  type SortKey,
  type SortSpec,
} from '../query';
import {
  EMPTY_SELECTION,
  clickSelect,
  contextSelect,
  indexer,
  moveFocus,
  prune,
  selectAll,
  selectOnly,
  toggleFocused,
  type Modifiers,
  type Move,
  type Selection,
} from '../selection';
import { ReportOrder } from './report-order';

export interface SpeedSample {
  readonly t: number;
  readonly v: number;
}

export type Outcome =
  | { readonly id: number; readonly ok: true }
  | { readonly id: number; readonly ok: false; readonly error: string };

export type AddOutcome =
  | { readonly url: string; readonly ok: true; readonly download: Download }
  | { readonly url: string; readonly ok: false; readonly error: string };

export type DaemonState =
  | { readonly ok: true; readonly status: Status }
  | { readonly ok: false; readonly error: string };

/** Sparkline window. */
export const HISTORY_MS = 60_000;
const MAX_SAMPLES = 400;
/** Parallel RPCs for bulk actions (selecting 10,000 rows must not open 10,000 calls). */
const BULK_CONCURRENCY = 8;
/** Daemon status check interval (also the reconnect retry interval). */
export const STATUS_INTERVAL_MS = 3000;

export async function mapLimit<T, R>(
  items: readonly T[],
  limit: number,
  fn: (item: T) => Promise<R>,
): Promise<R[]> {
  const out = new Array<R>(items.length);
  let next = 0;
  const worker = async () => {
    while (next < items.length) {
      const i = next++;
      out[i] = await fn(items[i] as T);
    }
  };
  await Promise.all(Array.from({ length: Math.min(limit, items.length) }, worker));
  return out;
}

type FrameHandle = { cancel: () => void };

function nextFrame(cb: () => void): FrameHandle {
  if (typeof requestAnimationFrame === 'function') {
    const h = requestAnimationFrame(cb);
    return { cancel: () => cancelAnimationFrame(h) };
  }
  const h = setTimeout(cb, 16);
  return { cancel: () => clearTimeout(h) };
}

export class DownloadsStore {
  readonly api: Api;
  readonly #now: () => number;
  #items = new Map<number, Download>();
  #history = new Map<number, readonly SpeedSample[]>();
  #reports = new ReportOrder();
  readonly #order = new SortedView();
  #rev = $state(0);
  #unsubscribe: Unsubscribe | null = null;
  #statusTimer: ReturnType<typeof setTimeout> | undefined;
  #polling = false;
  #pollMs = STATUS_INTERVAL_MS;
  #check: Promise<void> | null = null;
  /** Bulk actions in flight: their responses bump the revision once per frame. */
  #bulkDepth = 0;
  #frame: FrameHandle | null = null;

  /**
   * The total speed of every running download over the last HISTORY_MS (the status bar's
   * sparkline): one sample per event batch, taken with the per-download history.
   */
  totalHistory = $state.raw<readonly SpeedSample[]>([]);

  ready = $state(false);
  loadError = $state<string | null>(null);
  daemon = $state.raw<DaemonState | null>(null);
  /** A status check is running (the "Retry now" button shows progress). */
  checking = $state(false);
  /** When the next automatic status check runs (epoch ms; 0 = not scheduled). */
  nextCheckAt = $state(0);

  category = $state<CategoryFilter>('all');
  status = $state<StatusFilter>('any');
  /** The sidebar's queue filter (M6): a queue id, or null for every queue. */
  queue = $state<number | null>(null);
  search = $state('');
  sort = $state.raw<SortSpec>(DEFAULT_SORT);
  selection = $state.raw<Selection>(EMPTY_SELECTION);
  /** Settings.categories (the defaults until the settings are loaded). */
  categories = $state.raw<readonly Category[]>(DEFAULT_CATEGORIES);

  /** Groups the downloads into the configured categories and "other". */
  readonly index: CategoryIndex = $derived(new CategoryIndex(this.categories));

  /**
   * The daemon is unreachable (or the list could not be loaded): what is shown is the
   * last known state, live values are not current, and actions cannot be carried out.
   */
  readonly stale: boolean = $derived(
    (this.daemon !== null && !this.daemon.ok) || this.loadError !== null,
  );

  readonly all: Download[] = $derived.by(() => {
    void this.#rev;
    return Array.from(this.#items.values());
  });

  readonly #sorted: Download[] = $derived.by(() => {
    void this.#rev;
    return this.#order.update(this.#items, this.sort);
  });

  readonly counts = $derived(countFacets(this.all, this.category, this.status, this.index, this.queue));

  /** Per queue: its downloads that are not completed (the sidebar's queue counts). */
  readonly queueCounts: ReadonlyMap<number, number> = $derived(countQueues(this.all));

  readonly visible: Download[] = $derived(
    filterDownloads(
      this.#sorted,
      {
        category: this.category,
        status: this.status,
        search: this.search,
        queue: this.queue,
      },
      this.index,
    ),
  );

  /** Downloads awaiting confirmation, oldest first. */
  readonly awaiting: Download[] = $derived(awaitingInOrder(this.all));

  readonly visibleIds: number[] = $derived(this.visible.map((d) => d.id));

  readonly indexOf = $derived(indexer(this.visibleIds));

  /** Selected downloads in displayed order; rows hidden by filters are never acted on. */
  readonly selected: Download[] = $derived(
    this.selection.ids.size === 0 ? [] : this.visible.filter((d) => this.selection.ids.has(d.id)),
  );

  /** The download shown in the details panel. */
  readonly detail: Download | undefined = $derived.by(() => {
    const focus = this.selection.focus;
    if (focus !== null && this.selection.ids.has(focus) && this.indexOf(focus) >= 0) {
      return this.get(focus);
    }
    return this.selected.length === 1 ? this.selected[0] : undefined;
  });

  readonly totals = $derived.by(() => {
    let speed = 0;
    let active = 0;
    let queued = 0;
    for (const d of this.all) {
      if (d.state === 'downloading') speed += d.speedBps;
      if (d.state === 'downloading' || d.state === 'probing') active++;
      else if (d.state === 'queued') queued++;
    }
    // A stale list must not claim data is still arriving.
    return { speed: this.stale ? 0 : speed, active, queued, count: this.all.length };
  });

  constructor(api: Api, now: () => number = Date.now) {
    this.api = api;
    this.#now = now;
  }

  get(id: number): Download | undefined {
    void this.#rev;
    return this.#items.get(id);
  }

  historyOf(id: number): readonly SpeedSample[] {
    void this.#rev;
    return this.#history.get(id) ?? [];
  }

  /** Subscribes to events, then loads the list; events that arrive meanwhile are replayed. */
  async start(): Promise<void> {
    if (this.#unsubscribe) return;
    // Batches that arrive meanwhile keep the order of their arrival (ReportOrder).
    let buffer: { batch: EventBatch; stamp: { events: number; snapshot: number } }[] | null = [];
    this.#unsubscribe = this.api.onEvents((batch) => {
      if (buffer) buffer.push({ batch, stamp: this.#reports.arrived() });
      else this.apply(batch);
    });
    try {
      await this.reload();
    } finally {
      const pending = buffer;
      buffer = null;
      for (const { batch, stamp } of pending) this.#apply(batch, stamp);
    }
  }

  /**
   * Replaces the whole list from List() (initial load, or after the daemon reconnects). What
   * the window learned after sending List is newer and stays: a report newer than the listed
   * one, a download reported meanwhile, a removal (ReportOrder).
   */
  async reload(): Promise<void> {
    const at = this.#reports.sent();
    try {
      const list = await this.api.list();
      const listed = new Map<number, Download>();
      for (const d of list) listed.set(d.id, d);
      const items = new Map<number, Download>();
      const held = new Map<number, number>();
      for (const [id, d] of listed) {
        const prev = this.#items.get(id);
        if (this.#reports.newer(d, prev, at)) {
          items.set(id, d);
          held.set(id, at);
        } else if (prev) {
          items.set(id, prev); // reported after List was sent
          held.set(id, this.#reports.heldAt(id));
        } // else removed after List was sent
      }
      for (const [id, prev] of this.#items) {
        if (!listed.has(id) && this.#reports.heldAt(id) > at) {
          items.set(id, prev); // reported after List was sent
          held.set(id, this.#reports.heldAt(id));
        }
      }
      this.#reports.keepOnly(new Set(items.keys()));
      for (const [id, when] of held) this.#reports.hold(id, when);
      for (const id of this.#history.keys()) if (!items.has(id)) this.#history.delete(id);
      this.#items = items;
      this.#order.invalidate();
      this.loadError = null;
      this.ready = true;
      this.#rev++;
      this.#pruneSelection();
    } catch (err) {
      this.loadError = toApiError(err).message;
    } finally {
      this.#reports.landed();
    }
  }

  apply(batch: EventBatch): void {
    this.#apply(batch, this.#reports.arrived());
  }

  /** Takes a batch with its orders (ReportOrder.arrived). */
  #apply(batch: EventBatch, stamp: { events: number; snapshot: number }): void {
    let changed = false;
    const t = this.#now();
    for (const ev of batch) {
      if (ev.kind === 'removed') {
        if (this.#delete(ev.id, stamp.events)) changed = true;
      } else if (ev.kind === 'snapshot') {
        if (this.#put(ev.download, t, stamp.snapshot)) changed = true;
      } else if (ev.kind === 'updated') {
        if (this.#put(ev.download, t, stamp.events)) changed = true;
      }
      // queue events are the QueuesStore's
    }
    if (changed) {
      this.#rev++;
      this.#pruneSelection();
      this.#sampleTotal(t);
    }
  }

  /** Adds the total speed now to totalHistory (the window and sample cap of the per-download history). */
  #sampleTotal(t: number): void {
    let speed = 0;
    for (const d of this.#items.values()) {
      if (d.state === 'downloading' && d.speedBps > 0) speed += d.speedBps;
    }
    const old = this.totalHistory;
    const cutoff = t - HISTORY_MS;
    let from = 0;
    while (from < old.length && (old[from] as SpeedSample).t < cutoff) from++;
    const next = old.slice(Math.max(from, old.length + 1 - MAX_SAMPLES));
    next.push({ t, v: speed });
    this.totalHistory = next;
  }

  /** Stores a download, learned at at (ReportOrder), unless the one held is newer. */
  #put(d: Download, t: number, at: number): boolean {
    const prev = this.#items.get(d.id);
    if (!this.#reports.newer(d, prev, at)) return false;
    this.#reports.hold(d.id, at);
    this.#order.touch(d.id, prev);
    this.#items.set(d.id, d);
    if (d.state === 'downloading') {
      const old = this.#history.get(d.id) ?? [];
      const cutoff = t - HISTORY_MS;
      let from = 0;
      while (from < old.length && (old[from] as SpeedSample).t < cutoff) from++;
      const next = old.slice(Math.max(from, old.length + 1 - MAX_SAMPLES));
      next.push({ t, v: d.speedBps });
      this.#history.set(d.id, next);
    }
    return true;
  }

  /** Removes a download, as learned at at (undefined: now). */
  #delete(id: number, at?: number): boolean {
    const prev = this.#items.get(id);
    this.#reports.remove(id, at);
    this.#history.delete(id);
    if (!prev) return false;
    this.#order.touch(id, prev);
    this.#items.delete(id);
    return true;
  }

  /** One revision now, or (during a bulk action) at most one per animation frame. */
  #changed(): void {
    if (this.#bulkDepth === 0) {
      this.#rev++;
      this.#pruneSelection();
      return;
    }
    this.#frame ??= nextFrame(() => {
      this.#frame = null;
      this.#rev++;
      this.#pruneSelection();
    });
  }

  async #bulk<T>(fn: () => Promise<T>): Promise<T> {
    this.#bulkDepth++;
    try {
      return await fn();
    } finally {
      this.#bulkDepth--;
      if (this.#bulkDepth === 0 && this.#frame) {
        // Settle synchronously so callers see the final state when the action resolves.
        this.#frame.cancel();
        this.#frame = null;
        this.#rev++;
        this.#pruneSelection();
      }
    }
  }

  /**
   * Sends call, whose answer is a download, and stores that answer unless the window
   * learned a newer report meanwhile (ReportOrder).
   */
  async #answer(call: () => Promise<Download>): Promise<Download> {
    const at = this.#reports.sent();
    try {
      const d = await call();
      if (this.#put(d, this.#now(), at)) this.#changed();
      return d;
    } finally {
      this.#reports.landed();
    }
  }

  #pruneSelection(): void {
    const items = this.#items;
    this.selection = prune(this.selection, (id) => items.has(id));
  }

  // ---- daemon status ------------------------------------------------------------

  /** Checks the daemon once; reloads the list after it comes back. Concurrent calls share one check. */
  refreshStatus(): Promise<void> {
    this.#check ??= this.#refreshStatus().finally(() => {
      this.#check = null;
    });
    return this.#check;
  }

  async #refreshStatus(): Promise<void> {
    const wasDown = this.daemon !== null && !this.daemon.ok;
    this.checking = true;
    try {
      const status = await this.api.status();
      this.daemon = { ok: true, status };
      if (wasDown || this.loadError !== null || !this.ready) await this.reload();
    } catch (err) {
      this.daemon = { ok: false, error: toApiError(err).message };
    } finally {
      this.checking = false;
    }
  }

  startStatusPolling(intervalMs = STATUS_INTERVAL_MS): void {
    if (this.#polling) return;
    this.#polling = true;
    this.#pollMs = intervalMs;
    void this.#poll();
  }

  async #poll(): Promise<void> {
    clearTimeout(this.#statusTimer);
    this.#statusTimer = undefined;
    this.nextCheckAt = 0;
    await this.refreshStatus();
    if (!this.#polling || this.#statusTimer !== undefined) return;
    this.nextCheckAt = this.#now() + this.#pollMs;
    this.#statusTimer = setTimeout(() => void this.#poll(), this.#pollMs);
  }

  /** "Retry now": checks at once and restarts the retry countdown. */
  retryNow(): Promise<void> {
    if (this.#check) return this.#check;
    return this.#polling ? this.#poll() : this.refreshStatus();
  }

  dispose(): void {
    this.#unsubscribe?.();
    this.#unsubscribe = null;
    this.#polling = false;
    clearTimeout(this.#statusTimer);
    this.#statusTimer = undefined;
    this.#frame?.cancel();
    this.#frame = null;
  }

  // ---- filters ------------------------------------------------------------------

  setCategory(category: CategoryFilter): void {
    this.category = category;
    this.#pruneToVisible();
  }

  /** Takes the categories of new settings; a filter on a category that is gone shows all. */
  setCategories(categories: readonly Category[]): void {
    this.categories = categories;
    if (!this.index.has(this.category)) this.category = 'all';
    this.#pruneToVisible();
  }

  setStatusFilter(status: StatusFilter): void {
    this.status = status;
    this.#pruneToVisible();
  }

  /** Shows the downloads of one queue (null: of every queue). */
  setQueueFilter(queue: number | null): void {
    this.queue = queue;
    this.#pruneToVisible();
  }

  /** Takes the current queues: a filter on a queue that is gone shows every queue. */
  setQueues(ids: ReadonlySet<number>): void {
    if (this.queue !== null && !ids.has(this.queue)) this.setQueueFilter(null);
  }

  setSearch(search: string): void {
    this.search = search;
    this.#pruneToVisible();
  }

  sortBy(key: SortKey): void {
    this.sort = toggleSort(this.sort, key);
  }

  #pruneToVisible(): void {
    const indexOf = this.indexOf;
    this.selection = prune(this.selection, (id) => indexOf(id) >= 0);
  }

  // ---- selection ----------------------------------------------------------------

  click(id: number, mods: Modifiers = {}): void {
    this.selection = clickSelect(this.selection, this.visibleIds, this.indexOf, id, mods);
  }

  contextClick(id: number): void {
    this.selection = contextSelect(this.selection, id);
  }

  move(move: Move, mods: Modifiers = {}): void {
    this.selection = moveFocus(this.selection, this.visibleIds, this.indexOf, move, mods);
  }

  /** Puts the keyboard cursor on a row without changing the selection. */
  setCursor(id: number): void {
    if (this.indexOf(id) < 0) return;
    this.selection = { ...this.selection, focus: id };
  }

  toggleFocused(): void {
    this.selection = toggleFocused(this.selection);
  }

  selectAll(): void {
    this.selection = selectAll(this.selection, this.visibleIds);
  }

  selectOnly(id: number): void {
    this.selection = selectOnly(id);
  }

  clearSelection(): void {
    this.selection = EMPTY_SELECTION;
  }

  // ---- actions ------------------------------------------------------------------

  #each(ids: readonly number[], fn: (id: number) => Promise<void>): Promise<Outcome[]> {
    return this.#bulk(() =>
      mapLimit(ids, BULK_CONCURRENCY, async (id): Promise<Outcome> => {
        try {
          await fn(id);
          return { id, ok: true };
        } catch (err) {
          return { id, ok: false, error: toApiError(err).message };
        }
      }),
    );
  }

  pause(ids: readonly number[]): Promise<Outcome[]> {
    const targets = ids.filter((id) => {
      const d = this.#items.get(id);
      return d !== undefined && isActive(d.state);
    });
    return this.#each(targets, async (id) => void (await this.#answer(() => this.api.pause(id))));
  }

  resume(ids: readonly number[], opts?: ResumeOptions): Promise<Outcome[]> {
    const targets = ids.filter((id) => {
      const d = this.#items.get(id);
      return d !== undefined && isResumable(d.state);
    });
    return this.#each(targets, async (id) => void (await this.#answer(() => this.api.resume(id, opts))));
  }

  /**
   * Discards saved data and starts again; running downloads are paused first. A completed one is
   * downloaded again (ENGINE-SPEC §7.1), its file kept beside the new copy ('keepBoth', the
   * default) or replaced once the new copy is complete ('replace'). Downloads awaiting
   * confirmation have not started and are left out.
   */
  restart(ids: readonly number[], existingFile: ExistingFile = 'keepBoth'): Promise<Outcome[]> {
    const targets = ids.filter((id) => {
      const d = this.#items.get(id);
      return d !== undefined && d.state !== 'awaiting_confirmation';
    });
    return this.#each(targets, async (id) => {
      const d = this.#items.get(id);
      if (d && isActive(d.state)) await this.#answer(() => this.api.pause(id));
      const again = d?.state === 'completed';
      await this.#answer(() => this.api.resume(id, again ? { restart: true, existingFile } : { restart: true }));
    });
  }

  /**
   * Removes downloads. When that empties the selection, the nearest surviving row
   * (the one after the removed ones, else the one before) becomes the selected cursor
   * row, so keyboard users keep their place in the list.
   */
  async remove(ids: readonly number[], deleteFile: boolean): Promise<Outcome[]> {
    const survivor = this.#nearestSurvivor(ids);
    const outcomes = await this.#each(ids, async (id) => {
      await this.api.remove(id, { deleteFile });
      if (this.#delete(id)) this.#changed();
    });
    if (survivor !== null && this.selection.ids.size === 0 && this.indexOf(survivor) >= 0) {
      this.selection = selectOnly(survivor);
    }
    return outcomes;
  }

  #nearestSurvivor(ids: readonly number[]): number | null {
    const order = this.visibleIds;
    const removing = new Set(ids);
    const focus = this.selection.focus;
    let start = focus !== null && removing.has(focus) ? this.indexOf(focus) : -1;
    if (start < 0) {
      for (const id of ids) {
        const i = this.indexOf(id);
        if (i >= 0 && (start < 0 || i < start)) start = i;
      }
    }
    if (start < 0) return null;
    for (let i = start; i < order.length; i++) {
      const id = order[i] as number;
      if (!removing.has(id)) return id;
    }
    for (let i = start - 1; i >= 0; i--) {
      const id = order[i] as number;
      if (!removing.has(id)) return id;
    }
    return null;
  }

  add(requests: readonly AddRequest[]): Promise<AddOutcome[]> {
    return this.#bulk(() =>
      mapLimit(requests, BULK_CONCURRENCY, async (req): Promise<AddOutcome> => {
        try {
          const download = await this.#answer(() => this.api.add(req));
          return { url: req.url, ok: true, download };
        } catch (err) {
          return { url: req.url, ok: false, error: toApiError(err).message };
        }
      }),
    );
  }

  /** Adds a streaming-media download; rejects with the daemon's error (encrypted, live, …). */
  async addMedia(req: AddMediaRequest): Promise<Download> {
    return this.#answer(() => this.api.addMedia(req));
  }

  /** Lists the tracks of a manifest; rejects with the daemon's error. */
  probeMedia(req: ProbeMediaRequest): Promise<MediaInfo> {
    return this.api.probeMedia(req);
  }

  /** Confirms a download awaiting confirmation; rejects with the daemon's error. */
  async confirm(id: number, opts: ConfirmOptions): Promise<Download> {
    return this.#answer(() => this.api.confirm(id, opts));
  }

  /**
   * Records that downloads moved to a queue (after QueueService.MoveDownloads), until
   * the daemon's own updates arrive.
   */
  assignQueue(ids: readonly number[], queueId: number): void {
    let changed = false;
    for (const id of ids) {
      const d = this.#items.get(id);
      if (!d || d.queueId === queueId) continue;
      this.#order.touch(id, d);
      this.#items.set(id, { ...d, queueId });
      changed = true;
    }
    if (changed) this.#changed();
  }

  /** Pauses every active download; the events bring the new states. */
  pauseAll(): Promise<number> {
    return this.api.pauseAll();
  }

  resumeAll(): Promise<number> {
    return this.api.resumeAll();
  }

  async openFile(id: number): Promise<void> {
    await this.api.openFile(id);
  }

  async openFolder(id: number): Promise<void> {
    await this.api.openFolder(id);
  }

  async copyUrls(ids: readonly number[]): Promise<number> {
    const urls = ids
      .map((id) => this.#items.get(id))
      .filter((d): d is Download => d !== undefined)
      .map((d) => d.url);
    if (urls.length > 0) await this.api.copyText(urls.join('\n'));
    return urls.length;
  }

  /** Display name (bidi controls made visible); `#id` for unknown ids. */
  nameOf(id: number): string {
    const d = this.#items.get(id);
    return d ? displayName(d) : `#${id}`;
  }
}
