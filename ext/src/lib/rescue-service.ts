// The background's side of "Finish with Downex" (BROWSER-SPEC §6): failed browser downloads
// seen by downloads.onChanged (Chromium: still interrupted 3 s later), the popup's card, the
// click's flow with its history record, the hidden rows and the optional notifications. Browser
// APIs are injected (lib/rescue.ts holds the pure rules), so this runs under vitest.

import type { HandoffHistory, HandoffRecord, StorageAreaLike } from './history';
import type { FailedDownloadsResponse } from './messages';
import { FEATURE_RESCUE, type Outcome, type Timers, systemTimers } from './protocol';
import {
  CHROMIUM_RECHECK_MS,
  FailedList,
  NOTIFICATION_PREFIX,
  RescueNotifier,
  SCAN_LIMIT,
  failedNotice,
  finishWithDownex,
  itemName,
  rescueErrorText,
  rescueOffer,
  rescueRetry,
  resultNotice,
  savedLine,
  type FailedRow,
  type FinishDeps,
  type FinishResult,
  type RescueItem,
  type RescueKind,
  type RescueOptions,
} from './rescue';

export const RESCUE_HIDDEN_KEY = 'rescueHidden';

/** The notifications a rescue shows (the optional "notifications" permission). */
export interface RescueNotifications {
  /** Whether the permission is granted now. */
  permitted(): Promise<boolean>;
  /** Shows a notification; `button` adds "Finish with Downex" (Chromium only). */
  show(id: string, title: string, message: string, button: boolean): Promise<void>;
}

export interface RescueServiceDeps extends Omit<FinishDeps, 'onLate'> {
  isMac: boolean;
  /** The interrupted downloads, newest first (downloads.search state 'interrupted'). */
  interrupted(limit: number): Promise<RescueItem[]>;
  /** The relay's welcome features ([] while not connected). */
  features(): readonly string[];
  /** storage.session for the hidden rows; undefined: memory only. */
  session?: StorageAreaLike;
  history?: HandoffHistory;
  options(): Promise<RescueOptions>;
  notifications?: RescueNotifications;
  timers?: Timers;
  onError?: (e: unknown) => void;
}

export class RescueService {
  readonly #deps: RescueServiceDeps;
  readonly #timers: Timers;
  readonly #list: FailedList;
  readonly #notifier: RescueNotifier;
  /** Chromium: when an item was seen to stop; it is offered CHROMIUM_RECHECK_MS later. */
  readonly #stoppedAt = new Map<number, number>();
  readonly #hiddenMemory = new Set<number>();
  /** Rows the user acted on (their last known texts), for the notification of a result. */
  readonly #running = new Map<number, Promise<FailedRow | undefined>>();

  constructor(deps: RescueServiceDeps) {
    this.#deps = deps;
    this.#timers = deps.timers ?? systemTimers;
    this.#list = new FailedList(() => this.#timers.now());
    this.#notifier = new RescueNotifier(() => this.#timers.now());
  }

  /** The relay serves rescues. */
  get available(): boolean {
    return this.#deps.features().includes(FEATURE_RESCUE);
  }

  async #hidden(): Promise<Set<number>> {
    const area = this.#deps.session;
    if (!area) return this.#hiddenMemory;
    const got = await area.get(RESCUE_HIDDEN_KEY).catch((): Record<string, unknown> => ({}));
    const raw = got[RESCUE_HIDDEN_KEY];
    return new Set(Array.isArray(raw) ? raw.filter((v): v is number => Number.isSafeInteger(v)) : []);
  }

  async #offer(item: RescueItem): Promise<RescueKind | undefined> {
    if (item.byExtensionId !== undefined && item.byExtensionId !== '') return undefined;
    const method = await this.#deps.method?.(item).catch(() => undefined);
    return rescueOffer(item, this.#deps.isFirefox, method);
  }

  /**
   * A download changed to interrupted (or, on Firefox, paused): on Chromium it is looked at
   * again CHROMIUM_RECHECK_MS later (the browser resumes some interruptions by itself), then
   * announced when the option, the permission and the budget allow.
   */
  stopped(id: number): void {
    if (this.#deps.isFirefox) {
      void this.#announce(id);
      return;
    }
    this.#stoppedAt.set(id, this.#timers.now());
    this.#timers.setTimeout(() => void this.#announce(id), CHROMIUM_RECHECK_MS);
  }

  async #announce(id: number): Promise<void> {
    try {
      const n = this.#deps.notifications;
      if (!n || !this.available) return;
      const [item] = await this.#deps.downloads.search({ id });
      if (!item || (await this.#offer(item)) !== 'finish') return;
      const [options, permitted] = await Promise.all([this.#deps.options(), n.permitted()]);
      if (!this.#notifier.allow(id, { enabled: options.rescueNotify, permitted, fromScan: false })) return;
      const { title, message } = failedNotice(item);
      await n.show(`${NOTIFICATION_PREFIX}${id}`, title, message, !this.#deps.isFirefox);
    } catch (e) {
      this.#deps.onError?.(e);
    }
  }

  /** The card's rows (the start-up scan included: the browser's interrupted downloads, newest first). */
  async list(): Promise<FailedDownloadsResponse> {
    if (!this.available) return { available: false, items: [] };
    const now = this.#timers.now();
    const items = await this.#deps.interrupted(SCAN_LIMIT).catch((): RescueItem[] => []);
    const offered: { item: RescueItem; kind: RescueKind }[] = [];
    for (const item of items) {
      const at = this.#stoppedAt.get(item.id);
      if (at !== undefined && now - at < CHROMIUM_RECHECK_MS) continue;
      const kind = await this.#offer(item);
      if (kind) offered.push({ item, kind });
    }
    return { available: true, items: this.#list.rows(offered, await this.#hidden()) };
  }

  /** Hides a row for this browser session. */
  async hide(id: number): Promise<void> {
    const hidden = await this.#hidden();
    hidden.add(id);
    if (this.#deps.session) await this.#deps.session.set({ [RESCUE_HIDDEN_KEY]: [...hidden].slice(-200) });
  }

  /** A browser entry was erased: its row has nothing left to offer. */
  erased(id: number): void {
    this.#stoppedAt.delete(id);
    if (this.#list.state(id)?.phase !== 'done') this.#list.set(id, undefined);
  }

  /**
   * "Finish with Downex" for download `id` (also from a notification: `notify` shows the result).
   * One flow per download at a time; resolves to its row afterwards (undefined: not offered).
   */
  finish(id: number, fromStart = false, notify = false): Promise<FailedRow | undefined> {
    const running = this.#running.get(id);
    if (running) return running;
    const p = this.#finish(id, fromStart, notify).finally(() => this.#running.delete(id));
    this.#running.set(id, p);
    return p;
  }

  async #finish(id: number, fromStart: boolean, notify: boolean): Promise<FailedRow | undefined> {
    const [item] = await this.#deps.downloads.search({ id }).catch((): RescueItem[] => []);
    const kind = item ? await this.#offer(item) : undefined;
    const row: FailedRow | undefined = item && kind ? { downloadId: id, name: itemName(item), line: savedLine(item), kind } : undefined;
    const name = item ? itemName(item) : '';
    this.#list.set(id, { phase: 'checking' });
    const settle = (outcome: Outcome<FinishResult>): FailedRow | undefined => {
      if (outcome.ok) {
        const v = outcome.value;
        this.#list.set(id, { phase: 'done', keptBytes: v.keptBytes, resumed: v.resumed, fromStart: v.fromStart, at: this.#timers.now() }, row);
      } else {
        this.#list.set(id, { phase: 'failed', text: rescueErrorText(outcome.error, this.#deps.isMac), retry: rescueRetry(outcome.error) });
      }
      if (item) this.#record(item, outcome);
      if (notify && this.#deps.notifications) {
        const { title, message } = resultNotice(name, outcome, this.#deps.isMac);
        this.#deps.notifications.show(`${NOTIFICATION_PREFIX}${id}`, title, message, false).catch((e: unknown) => this.#deps.onError?.(e));
      }
      return row ? { ...row, state: this.#list.state(id)! } : undefined;
    };
    const outcome = await finishWithDownex(id, { ...this.#deps, onLate: (late) => void settle(late) }, fromStart);
    if (!outcome.ok && outcome.error.code === 'gone') {
      this.#list.set(id, undefined);
      return undefined;
    }
    return settle(outcome);
  }

  #record(item: RescueItem, outcome: Outcome<FinishResult>): void {
    const h = this.#deps.history;
    if (!h) return;
    const rec: HandoffRecord = { ts: this.#timers.now(), url: item.url, source: 'rescue', ok: outcome.ok };
    const name = itemName(item);
    if (name) rec.filename = name;
    if (outcome.ok) {
      rec.downloadId = outcome.value.downloadId;
      rec.awaiting = false;
      if (outcome.value.keptBytes > 0) rec.keptBytes = outcome.value.keptBytes;
    } else {
      rec.error = { ...outcome.error, message: rescueErrorText(outcome.error, this.#deps.isMac) };
    }
    h.add(rec).catch((e: unknown) => this.#deps.onError?.(e));
  }
}
