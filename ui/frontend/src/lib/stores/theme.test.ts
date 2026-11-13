import { afterEach, describe, expect, it, vi } from 'vitest';
import type { ThemeChoice } from '../api';
import { THEME_KEY, Theme, nextTheme, type ThemeStorage } from './theme.svelte';

function memory(initial: Record<string, string> = {}): NonNullable<ThemeStorage> & { data: Map<string, string> } {
  const data = new Map(Object.entries(initial));
  return {
    data,
    getItem: (k) => data.get(k) ?? null,
    setItem: (k, v) => void data.set(k, String(v)),
    removeItem: (k) => void data.delete(k),
  };
}

const broken: ThemeStorage = {
  getItem: () => {
    throw new Error('SecurityError: storage is disabled');
  },
  setItem: () => {
    throw new Error('QuotaExceededError');
  },
  removeItem: () => {
    throw new Error('SecurityError: storage is disabled');
  },
};

/** A host that records what the page asked of the native window parts. */
function host(fail?: string) {
  const themes: ThemeChoice[] = [];
  return {
    themes,
    setTheme: vi.fn(async (t: ThemeChoice) => {
      themes.push(t);
      if (fail) throw new Error(fail);
    }),
  };
}

/** Another window of the same page origin wrote the choice (WebView2 shares one storage). */
function otherWindowWrites(target: EventTarget, store: ReturnType<typeof memory>, value: ThemeChoice | null) {
  if (value === null || value === 'system') store.data.delete(THEME_KEY);
  else store.data.set(THEME_KEY, value);
  target.dispatchEvent(Object.assign(new Event('storage'), { key: THEME_KEY }));
}

const settle = () => new Promise((r) => setTimeout(r, 0));

afterEach(() => {
  delete document.documentElement.dataset.theme;
});

describe('theme store', () => {
  it('reads the stored choice, applies html[data-theme] and cycles system → light → dark', () => {
    const t = new Theme(() => memory({ [THEME_KEY]: 'dark' }), new EventTarget());
    expect(t.choice).toBe('dark');
    expect(document.documentElement.dataset.theme).toBe('dark');
    expect(new Theme(() => memory({ [THEME_KEY]: 'sepia' }), new EventTarget()).choice).toBe('system');
    expect(document.documentElement.dataset.theme).toBeUndefined();
    expect([nextTheme('system'), nextTheme('light'), nextTheme('dark')]).toEqual(['light', 'dark', 'system']);
  });

  it('gives the host the choice when it attaches and on every change (the native title bar)', async () => {
    const store = memory({ [THEME_KEY]: 'light' });
    const t = new Theme(() => store, new EventTarget());
    const h = host();
    t.attach(h);
    expect(h.themes).toEqual(['light']);
    t.set('dark');
    t.set('system');
    expect(h.themes).toEqual(['light', 'dark', 'system']);
    expect(store.data.has(THEME_KEY)).toBe(false);
    t.cycle();
    expect(h.themes.at(-1)).toBe('light');
    expect(store.data.get(THEME_KEY)).toBe('light');
  });

  it('follows a change made in another window: on its storage event, and when this window gets the focus', async () => {
    const store = memory({ [THEME_KEY]: 'light' });
    const win = new EventTarget();
    const t = new Theme(() => store, win);
    const h = host();
    t.attach(h);

    otherWindowWrites(win, store, 'dark');
    expect(t.choice).toBe('dark');
    expect(t.resolved).toBe('dark');
    expect(document.documentElement.dataset.theme).toBe('dark');
    expect(h.themes).toEqual(['light', 'dark']);

    // A storage event that did not arrive (separate WebView2 processes): the focus reads it again.
    store.data.delete(THEME_KEY);
    win.dispatchEvent(new Event('focus'));
    expect(t.choice).toBe('system');
    expect(document.documentElement.dataset.theme).toBeUndefined();
    expect(h.themes).toEqual(['light', 'dark', 'system']);

    // Nothing changed: the host is not asked again; other keys are ignored.
    win.dispatchEvent(new Event('focus'));
    store.data.set('downex.density', 'compact');
    win.dispatchEvent(Object.assign(new Event('storage'), { key: 'downex.density' }));
    expect(h.themes).toHaveLength(3);
  });

  it('keeps working when the host lacks the binding or cannot store the choice', async () => {
    const t = new Theme(() => memory(), new EventTarget());
    const h = host('unimplemented: SetTheme');
    t.attach(h);
    t.set('dark');
    await settle();
    expect(t.choice).toBe('dark');
    expect(h.setTheme).toHaveBeenCalledTimes(2);
  });

  it('keeps the session’s choice when storage fails, also when the window gets the focus', () => {
    const win = new EventTarget();
    const t = new Theme(() => broken, win);
    expect(t.choice).toBe('system');
    expect(() => t.set('dark')).not.toThrow();
    win.dispatchEvent(new Event('focus'));
    expect(t.choice).toBe('dark');
    expect(document.documentElement.dataset.theme).toBe('dark');
  });
});
