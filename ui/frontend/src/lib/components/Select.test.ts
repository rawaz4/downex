import { fireEvent, render, screen } from '@testing-library/svelte';
import { tick } from 'svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { chooseOption, listboxOf, optionLabels, optionValues } from '../testing';
import Select, { type SelectOption } from './Select.svelte';

const OPTIONS: SelectOption<string>[] = [
  { value: 'none', label: 'Unlimited' },
  { value: '20', label: '20 MiB/s' },
  { value: '10', label: '10 MiB/s', detail: 'half' },
  { value: '5', label: '5 MiB/s', disabled: true },
  { value: '1', label: '1 MiB/s', meta: '~1 GiB/h' },
];

let labels: HTMLElement[] = [];
afterEach(() => {
  for (const l of labels) l.remove();
  labels = [];
});

/** A <label for> outside the component, as a dialog's field row has it. */
function labelFor(id: string, text: string): HTMLLabelElement {
  const label = document.createElement('label');
  label.htmlFor = id;
  label.textContent = text;
  document.body.prepend(label);
  labels.push(label);
  return label;
}

function setup(props: Partial<{ value: string; options: SelectOption<string>[]; disabled: boolean; invalid: boolean; placeholder: string }> = {}) {
  labelFor('limit', 'Speed limit');
  const onchange = vi.fn();
  const utils = render(Select, { props: { value: 'none', options: OPTIONS, id: 'limit', onchange, ...props } });
  const combo = screen.getByRole('combobox', { name: 'Speed limit' });
  const active = () => {
    const id = combo.getAttribute('aria-activedescendant');
    return id ? (document.getElementById(id)?.dataset.value ?? null) : null;
  };
  const key = (k: string, init: KeyboardEventInit = {}) => fireEvent.keyDown(combo, { key: k, ...init });
  return { ...utils, combo, onchange, active, key };
}

describe('Select: the trigger and its label', () => {
  it('is a combobox named by its <label for>, carrying its value', () => {
    const { combo } = setup({ value: '20' });
    expect(screen.getByLabelText('Speed limit')).toBe(combo);
    expect(combo.tagName).toBe('BUTTON');
    expect((combo as HTMLButtonElement).value).toBe('20');
    expect(combo.dataset.value).toBe('20');
    expect(combo.textContent).toContain('20 MiB/s');
    expect(combo.getAttribute('aria-haspopup')).toBe('listbox');
    expect(combo.getAttribute('aria-expanded')).toBe('false');
  });

  it('keeps its listbox in the DOM, hidden while closed, with the options in order', () => {
    const { combo } = setup();
    const listbox = listboxOf(combo);
    expect(listbox.getAttribute('role')).toBe('listbox');
    expect(listbox.hidden).toBe(true);
    expect(optionValues(combo)).toEqual(['none', '20', '10', '5', '1']);
    expect(optionLabels(combo)).toEqual(['Unlimited', '20 MiB/s', '10 MiB/s', '5 MiB/s', '1 MiB/s']);
    const options = listbox.querySelectorAll('[role="option"]');
    expect(options[0]?.getAttribute('aria-selected')).toBe('true');
    expect(options[3]?.getAttribute('aria-disabled')).toBe('true');
    expect(options[2]?.textContent).toContain('half');
    expect(options[4]?.textContent).toContain('~1 GiB/h');
  });

  it('shows the placeholder when the value is none of the options, and the error state', () => {
    const { combo } = setup({ value: '', placeholder: 'Choose a limit', invalid: true });
    expect(combo.textContent).toContain('Choose a limit');
    expect(combo.getAttribute('aria-invalid')).toBe('true');
  });
});

describe('Select: the keyboard (APG select-only combobox)', () => {
  it('rings the keyboard’s active option, not the pointer’s', async () => {
    const { combo, key } = setup();
    const listbox = listboxOf(combo);
    await key('ArrowDown');
    expect(listbox.classList.contains('keys')).toBe(true);
    await fireEvent.pointerMove(listbox.querySelector('[data-value="20"]')!);
    expect(listbox.classList.contains('keys')).toBe(false);
    await key('ArrowDown');
    expect(listbox.classList.contains('keys')).toBe(true);
    await key('Escape');
    // opened with the mouse: no ring until a key moves it
    await fireEvent.click(combo);
    expect(listbox.classList.contains('keys')).toBe(false);
  });

  it('opens on the chosen option, moves past disabled ones, and chooses with Enter', async () => {
    const { combo, key, active, onchange } = setup({ value: '20' });
    await key('ArrowDown');
    expect(combo.getAttribute('aria-expanded')).toBe('true');
    expect(listboxOf(combo).hidden).toBe(false);
    expect(active()).toBe('20');
    await key('ArrowDown');
    expect(active()).toBe('10');
    await key('ArrowDown');
    expect(active()).toBe('1'); // 5 MiB/s is disabled
    await key('ArrowDown');
    expect(active()).toBe('1'); // the end stays the end
    await key('Home');
    expect(active()).toBe('none');
    await key('End');
    expect(active()).toBe('1');
    await key('ArrowUp');
    expect(active()).toBe('10');
    await key('Enter');
    expect(combo.getAttribute('aria-expanded')).toBe('false');
    expect(combo.dataset.value).toBe('10');
    expect(onchange).toHaveBeenCalledWith('10');
  });

  it('opens with Enter, Space, Up and Alt+Down; Home and End open on the ends', async () => {
    for (const [k, init, want] of [
      ['Enter', {}, 'none'],
      [' ', {}, 'none'],
      ['ArrowUp', {}, 'none'],
      ['ArrowDown', { altKey: true }, 'none'],
      ['End', {}, '1'],
      ['Home', {}, 'none'],
    ] as const) {
      const { combo, key, active, unmount } = setup();
      await key(k, init);
      expect(combo.getAttribute('aria-expanded'), k).toBe('true');
      expect(active(), k).toBe(want);
      unmount();
      for (const l of labels) l.remove();
      labels = [];
    }
  });

  it('closes on Escape without a change, and keeps the Escape from the dialog around it', async () => {
    const { combo, key, onchange } = setup();
    const outer = vi.fn();
    const onDocument = (e: KeyboardEvent) => {
      if (e.key === 'Escape') outer();
    };
    document.addEventListener('keydown', onDocument);
    try {
      await key('ArrowDown');
      await key('ArrowDown');
      await key('Escape');
      expect(combo.getAttribute('aria-expanded')).toBe('false');
      expect(combo.dataset.value).toBe('none');
      expect(onchange).not.toHaveBeenCalled();
      expect(outer).not.toHaveBeenCalled();
      // closed, Escape is the dialog's again
      await key('Escape');
      expect(outer).toHaveBeenCalledTimes(1);
    } finally {
      document.removeEventListener('keydown', onDocument);
    }
  });

  it('chooses the active option with Tab and Alt+Up', async () => {
    const { combo, key, onchange } = setup();
    await key('ArrowDown');
    await key('ArrowDown');
    await key('Tab');
    expect(combo.getAttribute('aria-expanded')).toBe('false');
    expect(combo.dataset.value).toBe('20');
    await key('ArrowDown');
    await key('ArrowDown');
    await key('ArrowUp', { altKey: true });
    expect(combo.getAttribute('aria-expanded')).toBe('false');
    expect(combo.dataset.value).toBe('10');
    expect(onchange.mock.calls).toEqual([['20'], ['10']]);
  });

  it('moves by ten with PageUp and PageDown', async () => {
    const many = Array.from({ length: 30 }, (_, i) => ({ value: String(i + 1), label: `${i + 1} connections` }));
    const { key, active } = setup({ value: '1', options: many });
    await key('ArrowDown');
    expect(active()).toBe('1');
    await key('PageDown');
    expect(active()).toBe('11');
    await key('PageDown');
    await key('PageDown');
    expect(active()).toBe('30');
    await key('PageUp');
    expect(active()).toBe('20');
  });

  it('chooses by typing while closed, and moves by typing while open', async () => {
    const { combo, key, active, onchange } = setup();
    await key('1');
    expect(combo.getAttribute('aria-expanded')).toBe('false');
    expect(combo.dataset.value).toBe('10');
    expect(onchange).toHaveBeenLastCalledWith('10');
    await key('1'); // the same letter again: the next match
    expect(combo.dataset.value).toBe('1');
    await new Promise((r) => setTimeout(r, 520));
    await key('ArrowDown');
    await key('u');
    expect(active()).toBe('none');
    await new Promise((r) => setTimeout(r, 520));
    await key('2');
    expect(active()).toBe('20');
    expect(combo.dataset.value).toBe('1'); // nothing chosen until Enter
  });
});

describe('Select: the pointer and the helpers', () => {
  it('chooses with a click, ignores disabled options and closes on an outside press', async () => {
    const { combo, onchange } = setup();
    await fireEvent.click(combo);
    const listbox = listboxOf(combo);
    const disabled = listbox.querySelector<HTMLElement>('[data-value="5"]');
    await fireEvent.click(disabled!);
    expect(combo.getAttribute('aria-expanded')).toBe('true');
    expect(onchange).not.toHaveBeenCalled();
    await fireEvent.click(listbox.querySelector<HTMLElement>('[data-value="20"]')!);
    expect(combo.getAttribute('aria-expanded')).toBe('false');
    expect(onchange).toHaveBeenCalledWith('20');
    await fireEvent.click(combo);
    expect(combo.getAttribute('aria-expanded')).toBe('true');
    await fireEvent.pointerDown(document.body);
    expect(combo.getAttribute('aria-expanded')).toBe('false');
  });

  it('chooseOption picks by value like a user, and refuses missing and disabled options', async () => {
    const { combo, onchange } = setup();
    await chooseOption(combo, '10');
    expect(combo.dataset.value).toBe('10');
    expect(combo.getAttribute('aria-expanded')).toBe('false');
    expect(onchange).toHaveBeenCalledWith('10');
    await expect(chooseOption(combo, '5')).rejects.toThrow(/disabled/);
    await expect(chooseOption(combo, '99')).rejects.toThrow(/no option "99"/);
  });

  it('keeps number values as numbers, and lists groups under their heading', async () => {
    labelFor('queue', 'Queue');
    const onchange = vi.fn();
    render(Select, {
      props: {
        id: 'queue',
        value: 1,
        onchange,
        options: [
          { value: 1, label: 'Main queue', group: 'Running' },
          { value: 3, label: 'Night', detail: 'starts 01:00', group: 'Stopped' },
          { value: 2, label: 'Later', group: 'Stopped' },
        ],
      },
    });
    const combo = screen.getByRole('combobox', { name: 'Queue' });
    expect(optionValues(combo)).toEqual(['1', '3', '2']);
    expect(screen.getByRole('group', { name: 'Stopped', hidden: true })).toBeTruthy();
    await chooseOption(combo, 2);
    expect(onchange).toHaveBeenCalledWith(2);
    expect(combo.dataset.value).toBe('2');
  });

  it('cannot be opened while disabled', async () => {
    const { combo } = setup({ disabled: true });
    expect((combo as HTMLButtonElement).disabled).toBe(true);
    await fireEvent.click(combo);
    await tick();
    expect(combo.getAttribute('aria-expanded')).toBe('false');
  });

  it('can be named by aria-label alone, and names its listbox the same', async () => {
    render(Select, { props: { value: 'a', options: [{ value: 'a', label: 'A' }], ariaLabel: 'Speed limit unit' } });
    const combo = screen.getByRole('combobox', { name: 'Speed limit unit' });
    await fireEvent.click(combo);
    expect(screen.getByRole('listbox', { name: 'Speed limit unit' })).toBe(listboxOf(combo));
  });

  it('names its listbox after the <label for> when it opens', async () => {
    const { combo } = setup();
    await fireEvent.click(combo);
    expect(screen.getByRole('listbox', { name: 'Speed limit' })).toBe(listboxOf(combo));
  });
});

describe('Select: where its list opens (DESIGN-SYSTEM.md §3.5)', () => {
  /**
   * Layout jsdom does not do: the window is 1190×752; the trigger's box; and, for a fixed box
   * spanning its containing block (`position: fixed` at 0 on every side), that block. A dialog's blurred
   * backdrop (backdrop-filter) is such a block below the 44 px title bar: fixed children are
   * placed against it, not against the window.
   */
  function layout(trigger: { top: number; bottom: number; left: number; width: number }, frame = { top: 44, left: 0, right: 1190, bottom: 752 }) {
    vi.spyOn(window, 'innerWidth', 'get').mockReturnValue(1190);
    vi.spyOn(window, 'innerHeight', 'get').mockReturnValue(752);
    const box = (b: { top: number; bottom: number; left: number; right: number }) =>
      ({ ...b, x: b.left, y: b.top, width: b.right - b.left, height: b.bottom - b.top, toJSON: () => b }) as DOMRect;
    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
      if (this.getAttribute('role') === 'combobox') return box({ ...trigger, right: trigger.left + trigger.width });
      if (this.style.position === 'fixed' && this.style.top === '0px' && this.style.bottom === '0px') return box(frame);
      return box({ top: 0, left: 0, right: 0, bottom: 0 });
    });
  }
  afterEach(() => vi.restoreAllMocks());

  it('opens 4 px under its trigger in a dialog whose backdrop holds fixed boxes', async () => {
    layout({ top: 507, bottom: 543, left: 300, width: 150 });
    const { combo } = setup();
    await fireEvent.click(combo);
    await tick();
    const list = listboxOf(combo);
    // 543 + 4 in the window is 547 − 44 in the backdrop
    expect(list.style.top).toBe('503px');
    expect(list.style.left).toBe('300px');
    expect(list.style.maxBlockSize).toBe(`${752 - 543 - 4 - 8}px`);
  });

  it('opens 4 px above its trigger, also against such a backdrop, when there is no room below', async () => {
    layout({ top: 690, bottom: 726, left: 300, width: 150 });
    const { combo } = setup();
    await fireEvent.click(combo);
    await tick();
    const list = listboxOf(combo);
    expect(list.style.top).toBe('');
    // its bottom edge 4 px above the trigger (690 − 4 = 686 in the window): 752 − 686 from the block's bottom
    expect(list.style.bottom).toBe('66px');
  });

  it('places against the window itself where nothing holds fixed boxes', async () => {
    layout({ top: 507, bottom: 543, left: 300, width: 150 }, { top: 0, left: 0, right: 1190, bottom: 752 });
    const { combo } = setup();
    await fireEvent.click(combo);
    await tick();
    expect(listboxOf(combo).style.top).toBe('547px');
  });
});
