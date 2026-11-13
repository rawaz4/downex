// @vitest-environment happy-dom

import { afterEach, describe, expect, it, vi } from 'vitest';
import { PAGE, SelectMenu, type SelectMenuInit, type SelectMenuOption } from './select-menu';

const QUALITIES: SelectMenuOption[] = [
  { value: 'v2160', label: '4K (2160p)', detail: 'AV1', meta: '1.9 GB', title: '3840×2160' },
  { value: 'v1440', label: '1440p (2K)', detail: 'VP9' },
  { value: 'v1080', label: '1080p (Full HD)', detail: 'H.264', meta: '514 MB' },
  { value: 'v720', label: '720p (HD)', detail: 'H.264', meta: '212 MB' },
  { value: 'v480', label: '480p', detail: 'H.264', meta: '96 MB' },
];

let menus: SelectMenu[] = [];

function mount(init: Partial<SelectMenuInit> = {}, options: SelectMenuOption[] = QUALITIES, value = 'v1080') {
  const onChange = vi.fn();
  const menu = new SelectMenu({ id: 'q', ariaLabel: 'Quality', onChange, ...init });
  const label = document.createElement('label');
  label.htmlFor = 'q';
  label.textContent = 'Quality';
  document.body.append(label, menu.root);
  menu.setOptions(options, value);
  menus.push(menu);
  const t = menu.trigger;
  t.focus();
  const key = (k: string, extra: KeyboardEventInit = {}) => {
    const ev = new KeyboardEvent('keydown', { key: k, bubbles: true, cancelable: true, ...extra });
    t.dispatchEvent(ev);
    return ev;
  };
  const active = () => document.getElementById(t.getAttribute('aria-activedescendant') ?? '')?.dataset.value;
  return { menu, t, key, onChange, active, list: menu.list };
}

afterEach(() => {
  for (const m of menus) m.destroy();
  menus = [];
  document.body.replaceChildren();
});

describe('SelectMenu', () => {
  it('is a select-only combobox: a named button that carries its value, and a hidden listbox', () => {
    const { t, list } = mount();
    expect(t.getAttribute('role')).toBe('combobox');
    expect(t.getAttribute('aria-haspopup')).toBe('listbox');
    expect(t.getAttribute('aria-expanded')).toBe('false');
    expect(t.getAttribute('aria-controls')).toBe(list.id);
    expect(t.getAttribute('aria-label')).toBe('Quality');
    expect(t.value).toBe('v1080');
    expect(t.dataset.value).toBe('v1080');
    expect(t.textContent).toContain('1080p (Full HD)');
    expect(t.textContent).toContain('H.264 · 514 MB');
    expect(list.getAttribute('role')).toBe('listbox');
    expect(list.hidden).toBe(true);
    const opts = [...list.querySelectorAll('[role="option"]')];
    expect(opts.map((o) => (o as HTMLElement).dataset.value)).toEqual(['v2160', 'v1440', 'v1080', 'v720', 'v480']);
    expect(opts.map((o) => o.getAttribute('aria-selected'))).toEqual(['false', 'false', 'true', 'false', 'false']);
    expect((opts[0] as HTMLElement).title).toBe('3840×2160');
    // A <label for> finds it.
    expect((document.querySelector('label[for="q"]') as HTMLLabelElement).control).toBe(t);
  });

  it('opens on the chosen option with Enter, moves with the arrows and chooses with Enter', () => {
    const { t, key, onChange, active, list, menu } = mount();
    expect(key('Enter').defaultPrevented).toBe(true);
    expect(menu.isOpen).toBe(true);
    expect(list.hidden).toBe(false);
    expect(t.getAttribute('aria-expanded')).toBe('true');
    expect(active()).toBe('v1080');
    key('ArrowDown');
    expect(active()).toBe('v720');
    key('ArrowUp');
    key('ArrowUp');
    expect(active()).toBe('v1440');
    key('Enter');
    expect(menu.isOpen).toBe(false);
    expect(t.hasAttribute('aria-activedescendant')).toBe(false);
    expect(onChange).toHaveBeenCalledWith('v1440');
    expect(menu.value).toBe('v1440');
    expect(t.value).toBe('v1440');
    expect(document.activeElement).toBe(t);
  });

  it('opens with Space, Down, Up and Alt+Down on the chosen option; Home and End on the ends', () => {
    for (const [k, extra, want] of [
      [' ', {}, 'v1080'],
      ['ArrowDown', {}, 'v1080'],
      ['ArrowUp', {}, 'v1080'],
      ['ArrowDown', { altKey: true }, 'v1080'],
      ['Home', {}, 'v2160'],
      ['End', {}, 'v480'],
    ] as const) {
      const { key, active, menu } = mount();
      key(k, extra);
      expect(menu.isOpen, k).toBe(true);
      expect(active(), k).toBe(want);
      menu.destroy();
    }
  });

  it('moves to the ends with Home and End and by ten with PageUp and PageDown, never past the ends', () => {
    const many = Array.from({ length: 25 }, (_, i) => ({ value: `o${i}`, label: `Option ${i}` }));
    const { key, active } = mount({}, many, 'o0');
    key('Enter');
    key('PageDown');
    expect(active()).toBe(`o${PAGE}`);
    key('PageDown');
    key('PageDown');
    expect(active()).toBe('o24');
    key('ArrowDown');
    expect(active()).toBe('o24');
    key('PageUp');
    expect(active()).toBe('o14');
    key('Home');
    expect(active()).toBe('o0');
    key('ArrowUp');
    expect(active()).toBe('o0');
    key('End');
    expect(active()).toBe('o24');
  });

  it('closes on Escape without a change, and the key goes no further (a dialog stays open)', () => {
    const { key, onChange, menu, t } = mount();
    const seen: string[] = [];
    const outer = (ev: KeyboardEvent) => seen.push(ev.key);
    document.body.addEventListener('keydown', outer);
    key('Enter');
    key('ArrowDown');
    const ev = key('Escape');
    expect(ev.defaultPrevented).toBe(true);
    expect(seen).not.toContain('Escape');
    expect(menu.isOpen).toBe(false);
    expect(onChange).not.toHaveBeenCalled();
    expect(t.value).toBe('v1080');
    // Closed, Escape is the page's again.
    key('Escape');
    expect(seen.filter((k) => k === 'Escape')).toHaveLength(1);
    document.body.removeEventListener('keydown', outer);
  });

  it('chooses with Alt+Up, and with Tab without holding the focus back', () => {
    const a = mount();
    a.key('Enter');
    a.key('ArrowDown');
    a.key('ArrowUp', { altKey: true });
    expect(a.menu.isOpen).toBe(false);
    expect(a.onChange).toHaveBeenLastCalledWith('v720');
    a.key('Enter');
    a.key('ArrowDown');
    expect(a.onChange).toHaveBeenCalledTimes(1);
    const tab = a.key('Tab');
    expect(tab.defaultPrevented).toBe(false);
    expect(a.menu.isOpen).toBe(false);
    expect(a.onChange).toHaveBeenLastCalledWith('v480');
  });

  it('chooses by typing: closed without opening, open by moving the active option', () => {
    vi.useFakeTimers({ toFake: ['Date'] });
    try {
      const { key, onChange, menu, active } = mount();
      // "4" from 1080p: the next label that starts so, 480p.
      key('4');
      expect(menu.isOpen).toBe(false);
      expect(onChange).toHaveBeenLastCalledWith('v480');
      expect(menu.value).toBe('v480');
      // The same letter again goes on to the next one (wrapping), 4K.
      key('4');
      expect(menu.value).toBe('v2160');
      vi.advanceTimersByTime(600);
      key('Enter');
      key('7');
      expect(active()).toBe('v720');
      expect(menu.value).toBe('v2160');
      vi.advanceTimersByTime(600);
      // Several letters typed together make one search.
      key('1');
      key('4');
      expect(active()).toBe('v1440');
      key('Enter');
      expect(menu.value).toBe('v1440');
    } finally {
      vi.useRealTimers();
    }
  });

  it('skips disabled options and never chooses them', () => {
    const opts = [
      { value: 'a', label: 'Alpha' },
      { value: 'b', label: 'Beta', disabled: true },
      { value: 'c', label: 'Gamma' },
    ];
    const { key, active, list, menu } = mount({}, opts, 'a');
    key('Enter');
    key('ArrowDown');
    expect(active()).toBe('c');
    expect(list.querySelector('[data-value="b"]')?.getAttribute('aria-disabled')).toBe('true');
    (list.querySelector('[data-value="b"]') as HTMLElement).click();
    expect(menu.isOpen).toBe(true);
    expect(menu.value).toBe('a');
    key('b');
    expect(active()).toBe('c');
  });

  it('opens and chooses with the mouse, and closes on a press outside', () => {
    const { t, list, menu, onChange } = mount();
    t.click();
    expect(menu.isOpen).toBe(true);
    (list.querySelector('[data-value="v720"]') as HTMLElement).click();
    expect(menu.isOpen).toBe(false);
    expect(onChange).toHaveBeenCalledWith('v720');
    t.click();
    expect(menu.isOpen).toBe(true);
    t.click();
    expect(menu.isOpen).toBe(false);
    t.click();
    document.body.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true }));
    expect(menu.isOpen).toBe(false);
    expect(menu.value).toBe('v720');
  });

  it('rings the keyboard’s active option, not the pointer’s', () => {
    const { t, key, list } = mount();
    key('ArrowDown');
    expect(list.classList.contains('keys')).toBe(true);
    (list.querySelector('[data-value="v720"]') as HTMLElement).dispatchEvent(new PointerEvent('pointermove', { bubbles: true }));
    expect(list.classList.contains('keys')).toBe(false);
    key('Escape');
    t.click();
    expect(list.classList.contains('keys')).toBe(false);
  });

  it('asks for options it does not have, then opens when they arrive while it has the focus', () => {
    const onRequest = vi.fn();
    const { t, key, menu, active } = mount({ placeholder: 'Default quality', onRequest }, [], '');
    expect(t.textContent).toContain('Default quality');
    expect(t.dataset.empty).toBe('true');
    key('Enter');
    expect(onRequest).toHaveBeenCalledTimes(1);
    expect(menu.isOpen).toBe(false);
    menu.setBusy(true);
    expect(t.getAttribute('aria-busy')).toBe('true');
    menu.setOptions(QUALITIES, 'v1080');
    menu.setBusy(false);
    expect(menu.isOpen).toBe(true);
    expect(active()).toBe('v1080');
    expect(t.hasAttribute('aria-busy')).toBe(false);
  });

  it('does not open options that arrive after the focus left', () => {
    const onRequest = vi.fn();
    const { t, menu } = mount({ onRequest }, [], '');
    t.click();
    expect(onRequest).toHaveBeenCalledTimes(1);
    t.blur();
    menu.setOptions(QUALITIES, 'v1080');
    expect(menu.isOpen).toBe(false);
  });

  it('stays shut while disabled, and setValue changes the value without a change event', () => {
    const { t, key, menu, onChange } = mount();
    menu.setDisabled(true);
    expect(t.disabled).toBe(true);
    key('Enter');
    t.click();
    expect(menu.isOpen).toBe(false);
    menu.setDisabled(false);
    menu.setValue('v480');
    expect(t.value).toBe('v480');
    expect(t.textContent).toContain('480p');
    expect(onChange).not.toHaveBeenCalled();
    menu.setInvalid(true);
    expect(t.getAttribute('aria-invalid')).toBe('true');
  });

  it('keeps its list at the end of the page and takes it away when destroyed', () => {
    const { menu, list } = mount();
    expect(list.parentElement).toBe(document.body);
    expect(menu.root.contains(list)).toBe(false);
    menu.destroy();
    expect(list.isConnected).toBe(false);
  });
});
