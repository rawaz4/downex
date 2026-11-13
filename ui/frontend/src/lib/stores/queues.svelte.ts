// Queues (M6-SPEC §1) as the window knows them: read with ListQueues, kept up to date by
// the queue events of Watch (queue_updated, queue_removed), and read again after every
// (re)subscription's snapshot, since queues removed meanwhile send no event. Post-queue
// action countdowns (post_action events) are kept by queue: a subscription's snapshot
// lists all of them, a cancellation ends one, and one whose deadline passed ends by
// itself shortly after (downexd sends nothing when it performs the action).

import {
  ApiError,
  toApiError,
  type Api,
  type EventBatch,
  type PostAction,
  type PostActionPending,
  type Queue,
  type QueueSpec,
  type Unsubscribe,
} from '../api';
import { queueSpec, sortQueues } from '../queues';

/** How long a countdown stays shown after its deadline ("… now"), while the action runs. */
export const POST_ACTION_GRACE_MS = 10_000;
/** The countdown of a post-queue action (M6-SPEC §1), for one that names no deadline. */
const POST_ACTION_DELAY_MS = 60_000;

export class QueuesStore {
  readonly api: Api;
  #items = $state.raw<ReadonlyMap<number, Queue>>(new Map());
  #unsubscribe: Unsubscribe | null = null;
  #loading: Promise<boolean> | null = null;
  /** Queues an event changed while a ListQueues was in flight (their event wins). */
  #touched: Map<number, Queue | null> | null = null;
  #again = false;
  /** When each countdown in `pending` ends by itself, by queue id (ms since the epoch). */
  #ends = new Map<number, number>();
  #expiry: ReturnType<typeof setTimeout> | null = null;

  /** The queues were read since downexd last became reachable. */
  loaded = $state(false);
  /** downexd has QueueService (an older one answers unimplemented: no queues are shown). */
  supported = $state(true);
  loadError = $state<string | null>(null);
  /** Post-queue action countdowns by queue id (cancelled ones are dropped). */
  pending = $state.raw<ReadonlyMap<number, PostActionPending>>(new Map());

  /** The main queue first. */
  readonly list: Queue[] = $derived(sortQueues(this.#items.values()));
  readonly ids: ReadonlySet<number> = $derived(new Set(this.#items.keys()));
  /** The countdown that ends first, if any. */
  readonly nextAction: PostActionPending | null = $derived.by(() => {
    let first: PostActionPending | null = null;
    for (const p of this.pending.values()) if (!first || p.deadline < first.deadline) first = p;
    return first;
  });

  constructor(api: Api) {
    this.api = api;
  }

  get(id: number): Queue | undefined {
    return this.#items.get(id);
  }

  nameOf(id: number): string {
    return this.#items.get(id)?.name ?? `Queue ${id}`;
  }

  /** Subscribes to the queue events. */
  start(): void {
    if (this.#unsubscribe) return;
    this.#unsubscribe = this.api.onEvents((batch) => this.apply(batch));
  }

  dispose(): void {
    this.#unsubscribe?.();
    this.#unsubscribe = null;
    if (this.#expiry !== null) clearTimeout(this.#expiry);
    this.#expiry = null;
  }

  /** Reads every queue (concurrent calls share one read); resolves to whether that worked. */
  load(): Promise<boolean> {
    if (this.#loading) {
      this.#again = true; // a snapshot arrived meanwhile: read once more afterwards
      return this.#loading;
    }
    this.#loading = this.#load().finally(() => {
      this.#loading = null;
      if (this.#again) {
        this.#again = false;
        void this.load();
      }
    });
    return this.#loading;
  }

  async #load(): Promise<boolean> {
    this.#touched = new Map();
    try {
      const list = await this.api.listQueues();
      const next = new Map(list.map((q) => [q.id, q]));
      for (const [id, q] of this.#touched) {
        if (q) next.set(id, q);
        else next.delete(id);
      }
      this.#items = next;
      this.#dropPendingOfMissing();
      this.supported = true;
      this.loaded = true;
      this.loadError = null;
      return true;
    } catch (err) {
      const e = toApiError(err);
      if (e.code === 'unimplemented') {
        // downexd without queues: nothing to show, and nothing to read again until it restarts
        this.#items = new Map();
        this.supported = false;
        this.loaded = true;
        this.loadError = null;
        return true;
      }
      this.loaded = false;
      this.loadError = e.message;
      return false;
    } finally {
      this.#touched = null;
    }
  }

  /**
   * downexd did not answer a status check: the queues are read again when it does. The
   * countdowns stay (the window hides them meanwhile): the event stream may still be up,
   * and nothing would send them again; a new subscription's snapshot replaces them.
   */
  invalidate(): void {
    this.loaded = false;
  }

  apply(batch: EventBatch): void {
    let items: Map<number, Queue> | null = null;
    let pending: Map<number, PostActionPending> | null = null;
    let snapshot = false;
    for (const ev of batch) {
      switch (ev.kind) {
        case 'snapshot':
          // A subscription's first batch: its post_action events are every countdown.
          if (!snapshot) pending = new Map();
          snapshot = true;
          break;
        case 'queue_updated':
          items ??= new Map(this.#items);
          items.set(ev.queue.id, ev.queue);
          this.#touched?.set(ev.queue.id, ev.queue);
          break;
        case 'queue_removed':
          items ??= new Map(this.#items);
          items.delete(ev.id);
          this.#touched?.set(ev.id, null);
          pending ??= new Map(this.pending);
          pending.delete(ev.id);
          break;
        case 'post_action':
          pending ??= new Map(this.pending);
          if (ev.pending.cancelled || ev.pending.action === 'none') pending.delete(ev.pending.queueId);
          else pending.set(ev.pending.queueId, ev.pending);
          break;
      }
    }
    if (items) this.#items = items;
    if (pending) this.#setPending(pending);
    if (!snapshot) return;
    // A new subscription (not the first): queues removed while it was down sent no event.
    if (this.#subscribed && this.supported && this.loaded) void this.load();
    this.#subscribed = true;
  }

  /** The first subscription's snapshot was seen. */
  #subscribed = false;

  #put(q: Queue): Queue {
    const next = new Map(this.#items);
    next.set(q.id, q);
    this.#items = next;
    return q;
  }

  #dropPendingOfMissing(): void {
    if ([...this.pending.keys()].some((id) => !this.#items.has(id))) {
      this.#setPending(new Map([...this.pending].filter(([id]) => this.#items.has(id))));
    }
  }

  #dropPending(queueId: number): void {
    if (!this.pending.has(queueId)) return;
    const next = new Map(this.pending);
    next.delete(queueId);
    this.#setPending(next);
  }

  /**
   * Sets the countdowns: each ends by itself a little after its deadline (a new one that
   * names none, 60 s after it arrived), and those already over are left out.
   */
  #setPending(next: Map<number, PostActionPending>): void {
    const now = Date.now();
    const ends = new Map<number, number>();
    for (const [id, p] of next) {
      const known = this.pending.get(id) === p ? this.#ends.get(id) : undefined;
      const end = known ?? (p.deadline > 0 ? p.deadline : now + POST_ACTION_DELAY_MS) + POST_ACTION_GRACE_MS;
      if (end <= now) next.delete(id);
      else ends.set(id, end);
    }
    this.#ends = ends;
    this.pending = next;
    if (this.#expiry !== null) clearTimeout(this.#expiry);
    this.#expiry = null;
    if (ends.size === 0) return;
    // (Timers take at most 2^31-1 ms; a later end is checked again then.)
    const wait = Math.min(Math.min(...ends.values()) - now, 0x7fffffff);
    this.#expiry = setTimeout(() => {
      this.#expiry = null;
      this.#setPending(new Map(this.pending));
    }, wait);
  }

  // ---- actions (reject with the daemon's error) -------------------------------------

  async create(spec: QueueSpec): Promise<Queue> {
    return this.#put(await this.api.createQueue(spec));
  }

  async update(id: number, spec: QueueSpec): Promise<Queue> {
    return this.#put(await this.api.updateQueue(id, spec));
  }

  /**
   * Changes only what a queue does when it is done (the status bar's when-done menu): the
   * rest of its settings are sent as this window knows them. Rejects when the queue is not
   * known (not_found), as UpdateQueue would.
   */
  async setPostAction(id: number, postAction: PostAction): Promise<Queue> {
    const q = this.#items.get(id);
    if (!q) throw new ApiError('not_found', `queue ${id} not found`);
    return this.update(id, queueSpec(q, { postAction }));
  }

  /** Deletes a queue; its downloads join the main queue (the daemon's events say where). */
  async remove(id: number): Promise<void> {
    await this.api.deleteQueue(id);
    const next = new Map(this.#items);
    const gone = next.get(id);
    next.delete(id);
    const main = [...next.values()].find((q) => q.isMain);
    if (gone && main) next.set(main.id, { ...main, downloadIds: [...main.downloadIds, ...gone.downloadIds] });
    this.#items = next;
    this.#dropPendingOfMissing();
  }

  async startQueue(id: number): Promise<Queue> {
    return this.#put(await this.api.startQueue(id));
  }

  async stopQueue(id: number): Promise<Queue> {
    return this.#put(await this.api.stopQueue(id));
  }

  /** Moves downloads into a queue at `position` (-1 = the end); they leave their old queues. */
  async move(ids: readonly number[], queueId: number, position: number): Promise<Queue> {
    const target = await this.api.moveDownloads(ids, queueId, position);
    const next = new Map<number, Queue>();
    for (const [id, q] of this.#items) {
      next.set(id, id === target.id ? target : { ...q, downloadIds: q.downloadIds.filter((d) => !ids.includes(d)) });
    }
    next.set(target.id, target);
    this.#items = next;
    return target;
  }

  /**
   * Cancels a countdown; it disappears at once (the daemon confirms with an event).
   * Resolves to false when downexd had none for the queue any more (the action ran, or
   * the countdown or the queue ended elsewhere): it disappears too. Other failures reject
   * and keep it, since it may still run.
   */
  async cancelPostAction(queueId: number): Promise<boolean> {
    let cancelled = true;
    try {
      await this.api.cancelPostAction(queueId);
    } catch (err) {
      const code = toApiError(err).code;
      if (code !== 'failed_precondition' && code !== 'not_found') throw err;
      cancelled = false;
    }
    this.#dropPending(queueId);
    return cancelled;
  }
}
