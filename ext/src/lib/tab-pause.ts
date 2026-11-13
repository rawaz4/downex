// "Pause capture in this tab" (REQUIREMENTS B2): tab IDs whose downloads stay in the browser.
// Session-scoped: kept in storage.session (cleared with the browser session) so a restarted
// service worker still knows them, and forgotten when the tab closes. Pure: storage is injected.

import type { StorageAreaLike } from './history';

export const PAUSED_TABS_STORAGE_KEY = 'pausedTabs';
/** More paused tabs than a browser realistically has open; bounds what is stored. */
export const MAX_PAUSED_TABS = 1000;

export function isTabId(v: unknown): v is number {
  return typeof v === 'number' && Number.isSafeInteger(v) && v >= 0;
}

export function parsePausedTabs(raw: unknown): number[] {
  return Array.isArray(raw) ? [...new Set(raw.filter(isTabId))].slice(-MAX_PAUSED_TABS) : [];
}

/**
 * Paused tab IDs, cached for synchronous capture decisions once `ready` resolved. Changes
 * apply in call order after the stored tabs were read, each written before the next applies.
 */
export class PausedTabs {
  readonly #tabs = new Set<number>();
  #loaded = false;
  #chain: Promise<unknown>;
  /** Resolves once the stored tabs were read (an unreadable store counts as none). */
  readonly ready: Promise<void>;

  constructor(
    private readonly area: StorageAreaLike,
    private readonly key: string = PAUSED_TABS_STORAGE_KEY,
  ) {
    this.ready = area
      .get(key)
      .then(
        (got) => {
          for (const id of parsePausedTabs(got[key])) this.#tabs.add(id);
        },
        () => undefined,
      )
      .finally(() => {
        this.#loaded = true;
      });
    this.#chain = this.ready;
  }

  get loaded(): boolean {
    return this.#loaded;
  }

  isPaused(tabId: number | undefined): boolean {
    return tabId !== undefined && this.#tabs.has(tabId);
  }

  /** Pauses or resumes capture in a tab; resolves once stored, rejects when storing failed. */
  set(tabId: number, paused: boolean): Promise<void> {
    return this.#update(() => {
      if (!isTabId(tabId) || this.#tabs.has(tabId) === paused) return false;
      if (paused) this.#tabs.add(tabId);
      else this.#tabs.delete(tabId);
      while (this.#tabs.size > MAX_PAUSED_TABS) this.#tabs.delete(this.#tabs.values().next().value as number);
      return true;
    });
  }

  /**
   * Tab `tabId` was opened from tab `openerTabId` (a link with a new tab as target, a
   * middle- or Ctrl-click, window.open): it sends the requests of downloads started there,
   * so it inherits the opener's pause. Resolves once stored, rejects when storing failed.
   */
  inherit(openerTabId: number, tabId: number): Promise<void> {
    return this.#update(() => {
      if (!isTabId(tabId) || !this.#tabs.has(openerTabId) || this.#tabs.has(tabId)) return false;
      this.#tabs.add(tabId);
      while (this.#tabs.size > MAX_PAUSED_TABS) this.#tabs.delete(this.#tabs.values().next().value as number);
      return true;
    });
  }

  /** A closed tab's pause ends with it. */
  forget(tabId: number): Promise<void> {
    return this.set(tabId, false);
  }

  /** The browser replaced a tab (e.g. a prerendered page): the pause moves to the new ID. */
  replace(removedTabId: number, addedTabId: number): Promise<void> {
    return this.#update(() => {
      if (!this.#tabs.delete(removedTabId)) return false;
      if (isTabId(addedTabId)) this.#tabs.add(addedTabId);
      return true;
    });
  }

  /** Forgets every tab (a new browser session reuses tab IDs). */
  clear(): Promise<void> {
    return this.#update(() => {
      if (this.#tabs.size === 0) return false;
      this.#tabs.clear();
      return true;
    });
  }

  /** Applies `change` after the stored tabs were read, then writes them when it changed something. */
  #update(change: () => boolean): Promise<void> {
    const next = this.#chain.then(async () => {
      if (change()) await this.area.set({ [this.key]: [...this.#tabs] });
    });
    this.#chain = next.catch(() => undefined);
    return next;
  }
}
