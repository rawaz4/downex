// Light/dark theme: follows the system by default (prefers-color-scheme), with a manual override
// stored per viewer in localStorage['downex.theme'] and applied as <html data-theme>. Every window
// of Downex (the main window, each progress window) shares that storage: a change made in one is
// followed by the others (its storage event, else when they get the focus). The host is told the
// choice when the store attaches and on every change, so the native parts it draws (the title bar
// of a progress window, macOS's appearance) match the page (DESIGN-SYSTEM.md §4.8).

import type { Api, ThemeChoice } from '../api';

export type { ThemeChoice };

export const THEME_KEY = 'downex.theme';
const ORDER: readonly ThemeChoice[] = ['system', 'light', 'dark'];

/** What the store reads and writes; null when there is none. */
export type ThemeStorage = Pick<Storage, 'getItem' | 'setItem' | 'removeItem'> | null;

function browserStorage(): ThemeStorage {
  try {
    return typeof localStorage === 'undefined' ? null : localStorage;
  } catch {
    return null;
  }
}

function browserWindow(): EventTarget | null {
  return typeof window === 'undefined' ? null : window;
}

function systemPrefersDark(): boolean {
  return typeof matchMedia === 'function' && matchMedia('(prefers-color-scheme: dark)').matches;
}

export class Theme {
  choice = $state<ThemeChoice>('system');
  #systemDark = $state(systemPrefersDark());
  readonly #storage: () => ThemeStorage;
  #host: Pick<Api, 'setTheme'> | null = null;

  /** The scheme actually shown; canvases read it to know when to redraw. */
  readonly resolved: 'light' | 'dark' = $derived(
    this.choice === 'system' ? (this.#systemDark ? 'dark' : 'light') : this.choice,
  );

  /** `target`: where another window's change shows (its storage events, and this window's focus). */
  constructor(storage: () => ThemeStorage = browserStorage, target: EventTarget | null = browserWindow()) {
    this.#storage = storage;
    this.choice = this.#read() ?? 'system';
    if (typeof matchMedia === 'function') {
      const mq = matchMedia('(prefers-color-scheme: dark)');
      mq.addEventListener?.('change', (e) => {
        this.#systemDark = e.matches;
      });
    }
    target?.addEventListener('storage', (e) => {
      const key = (e as StorageEvent).key;
      // null: the whole storage was cleared
      if (key === THEME_KEY || key === null) this.#follow();
    });
    target?.addEventListener('focus', () => this.#follow());
    this.#apply();
  }

  /**
   * Connects the host (main.ts, once the Api is resolved): the native window parts take the choice
   * now and on every change. A host without the binding, or one that could not store it, rejects;
   * the page keeps its theme either way.
   */
  attach(host: Pick<Api, 'setTheme'>): void {
    this.#host = host;
    this.#push();
  }

  set(choice: ThemeChoice): void {
    this.choice = choice;
    try {
      const s = this.#storage();
      if (choice === 'system') s?.removeItem(THEME_KEY);
      else s?.setItem(THEME_KEY, choice);
    } catch {
      // storage unavailable: the choice lasts for this session only
    }
    this.#apply();
    this.#push();
  }

  cycle(): void {
    this.set(nextTheme(this.choice));
  }

  /** The stored choice, or null when storage cannot be read. */
  #read(): ThemeChoice | null {
    try {
      const s = this.#storage();
      if (!s) return null;
      const v = s.getItem(THEME_KEY);
      return v === 'light' || v === 'dark' ? v : 'system';
    } catch {
      return null;
    }
  }

  /** Another window changed the choice: take it (unreadable storage keeps this session's). */
  #follow(): void {
    const stored = this.#read();
    if (stored === null || stored === this.choice) return;
    this.choice = stored;
    this.#apply();
    this.#push();
  }

  #push(): void {
    const host = this.#host;
    if (!host) return;
    try {
      host.setTheme(this.choice).catch(() => undefined);
    } catch {
      // a host that throws instead of rejecting: nothing to do either
    }
  }

  #apply(): void {
    if (typeof document === 'undefined') return;
    const root = document.documentElement;
    if (this.choice === 'system') delete root.dataset.theme;
    else root.dataset.theme = this.choice;
  }
}

export function nextTheme(choice: ThemeChoice): ThemeChoice {
  return ORDER[(ORDER.indexOf(choice) + 1) % ORDER.length] as ThemeChoice;
}

export const theme = new Theme();
