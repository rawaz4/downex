// Last click per tab and frame for the Alt bypass (BROWSER-SPEC §3 "Bypass"): a click in
// one tab or frame never replaces the Alt-click of another. A tab opened from another one
// (a link with a new tab as target, a middle- or Ctrl-click, window.open) sends the
// download request itself, so it also looks at its opener's clicks. Pure: no browser APIs.

import { BYPASS_WINDOW_MS, type ClickModifiers, isBypassActive } from './rules';

/** Upper bound on remembered frames; clicks older than the bypass window are dropped anyway. */
export const MAX_TRACKED_FRAMES = 256;
/** Upper bound on remembered opener links (forgotten when their tab closes). */
export const MAX_TRACKED_OPENERS = 1000;
/** How far up a chain of openers a download looks (tab opened from a tab opened from …). */
const MAX_OPENER_DEPTH = 4;

interface FrameClick {
  tabId: number;
  click: ClickModifiers;
}

export class ClickTracker {
  readonly #clicks = new Map<string, FrameClick>();
  /** Tab ID → the tab it was opened from. */
  readonly #openers = new Map<number, number>();

  constructor(private readonly windowMs: number = BYPASS_WINDOW_MS) {}

  get size(): number {
    return this.#clicks.size;
  }

  /** Records the last click of frame `frameId` in tab `tabId`, replacing that frame's previous one. */
  record(tabId: number, frameId: number, click: ClickModifiers, now: number): void {
    const key = `${tabId}:${frameId}`;
    this.#clicks.delete(key);
    this.#clicks.set(key, { tabId, click });
    this.#prune(now);
  }

  /**
   * The Alt-click that keeps a download started in `tabId` in the browser: the newest one
   * still within the bypass window among the last clicks of that tab's frames, or of every
   * tab's frames when the download's tab is unknown. Undefined when there is none.
   */
  bypassClick(tabId: number | undefined, now: number): ClickModifiers | undefined {
    this.#prune(now);
    const tabs = tabId === undefined ? undefined : this.#withOpeners(tabId);
    let best: ClickModifiers | undefined;
    for (const f of this.#clicks.values()) {
      if (tabs !== undefined && !tabs.has(f.tabId)) continue;
      if (!isBypassActive(f.click, now)) continue;
      if (!best || f.click.ts > best.ts) best = f.click;
    }
    return best;
  }

  /**
   * The tab of the newest click (on a link, or with Alt) still within the bypass window:
   * where a download whose request names no tab was most likely started.
   */
  lastClickTab(now: number): number | undefined {
    this.#prune(now);
    let best: FrameClick | undefined;
    for (const f of this.#clicks.values()) {
      if (!best || f.click.ts > best.click.ts) best = f;
    }
    return best?.tabId;
  }

  /** Tab `tabId` was opened from tab `openerTabId`. */
  setOpener(tabId: number, openerTabId: number): void {
    if (tabId === openerTabId) return;
    this.#openers.delete(tabId);
    this.#openers.set(tabId, openerTabId);
    for (const key of this.#openers.keys()) {
      if (this.#openers.size <= MAX_TRACKED_OPENERS) break;
      this.#openers.delete(key);
    }
  }

  /** Forgets a closed tab's clicks and opener. */
  forgetTab(tabId: number): void {
    for (const [key, f] of this.#clicks) {
      if (f.tabId === tabId) this.#clicks.delete(key);
    }
    this.#openers.delete(tabId);
  }

  #withOpeners(tabId: number): Set<number> {
    const tabs = new Set([tabId]);
    let tab = tabId;
    for (let i = 0; i < MAX_OPENER_DEPTH; i++) {
      const opener = this.#openers.get(tab);
      if (opener === undefined || tabs.has(opener)) break;
      tabs.add(opener);
      tab = opener;
    }
    return tabs;
  }

  #prune(now: number): void {
    for (const [key, f] of this.#clicks) {
      if (now - f.click.ts > this.windowMs) this.#clicks.delete(key);
    }
    for (const key of this.#clicks.keys()) {
      if (this.#clicks.size <= MAX_TRACKED_FRAMES) break;
      this.#clicks.delete(key);
    }
  }
}
