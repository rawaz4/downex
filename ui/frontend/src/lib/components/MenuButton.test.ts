import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { createRawSnippet, tick } from 'svelte';
import { describe, expect, it, vi } from 'vitest';
import type { MenuItem } from './ContextMenu.svelte';
import MenuButton from './MenuButton.svelte';
import SplitButton from './SplitButton.svelte';

const LIMITS: MenuItem[] = [
  { id: '0', label: 'Unlimited', kind: 'radio', checked: true, heading: 'Global speed limit' },
  { id: '20', label: '20 MiB/s', kind: 'radio', checked: false },
  { id: '10', label: '10 MiB/s', kind: 'radio', checked: false },
  { id: 'custom', label: 'Custom…', separated: true },
];

const text = (t: string) => createRawSnippet(() => ({ render: () => `<span>${t}</span>` }));

function setup(props: Partial<{ disabled: boolean; direction: 'up' | 'down' }> = {}) {
  const onselect = vi.fn();
  const ontoggle = vi.fn();
  const utils = render(MenuButton, {
    props: {
      items: LIMITS,
      label: 'Global speed limit',
      onselect,
      ontoggle,
      children: text('Unlimited'),
      footnote: 'Per-download and queue limits still apply.',
      direction: 'up',
      align: 'end',
      ...props,
    },
  });
  const trigger = screen.getByRole('button', { name: 'Unlimited' });
  return { ...utils, trigger, onselect, ontoggle };
}

describe('MenuButton', () => {
  it('opens its menu on click with the focus on the first item, and closes back to the trigger', async () => {
    const { trigger, ontoggle } = setup();
    expect(trigger.getAttribute('aria-haspopup')).toBe('menu');
    expect(trigger.getAttribute('aria-expanded')).toBe('false');
    await fireEvent.click(trigger);
    const menu = screen.getByRole('menu', { name: 'Global speed limit' });
    expect(trigger.getAttribute('aria-expanded')).toBe('true');
    expect(ontoggle).toHaveBeenLastCalledWith(true);
    await waitFor(() => expect(document.activeElement?.getAttribute('role')).toBe('menuitemradio'));
    expect(document.activeElement?.textContent).toContain('Unlimited');
    await fireEvent.keyDown(menu, { key: 'Escape' });
    expect(screen.queryByRole('menu')).toBeNull();
    expect(document.activeElement).toBe(trigger);
    expect(ontoggle).toHaveBeenLastCalledWith(false);
  });

  it('opens with Enter, Space and ArrowDown on the first item, and ArrowUp on the last', async () => {
    for (const [key, want] of [
      ['Enter', 'Unlimited'],
      [' ', 'Unlimited'],
      ['ArrowDown', 'Unlimited'],
      ['ArrowUp', 'Custom…'],
    ] as const) {
      const { trigger, unmount } = setup();
      trigger.focus();
      await fireEvent.keyDown(trigger, { key });
      await waitFor(() => expect(document.activeElement?.getAttribute('role'), key).toMatch(/^menuitem/));
      expect(document.activeElement?.textContent, key).toContain(want);
      unmount();
    }
  });

  it('has radio items with aria-checked under a heading, and a footnote that describes it', async () => {
    const { trigger } = setup();
    await fireEvent.click(trigger);
    const menu = screen.getByRole('menu');
    const radios = screen.getAllByRole('menuitemradio');
    expect(radios.map((r) => r.getAttribute('aria-checked'))).toEqual(['true', 'false', 'false']);
    expect(screen.getByRole('group', { name: 'Global speed limit' }).contains(radios[0] as Node)).toBe(true);
    expect(screen.getByRole('menuitem', { name: 'Custom…' })).toBeTruthy();
    const foot = document.getElementById(menu.getAttribute('aria-describedby') ?? '');
    expect(foot?.textContent).toBe('Per-download and queue limits still apply.');
  });

  it('chooses an item: the menu closes, the focus goes back, then the choice is told', async () => {
    const { trigger, onselect } = setup();
    await fireEvent.click(trigger);
    await fireEvent.click(screen.getByRole('menuitemradio', { name: '10 MiB/s' }));
    expect(onselect).toHaveBeenCalledWith('10');
    expect(screen.queryByRole('menu')).toBeNull();
    expect(document.activeElement).toBe(trigger);
  });

  it('toggles from its trigger, and closes on an outside press', async () => {
    const { trigger } = setup();
    await fireEvent.click(trigger);
    // a press on the trigger itself is the trigger's (it toggles), not an outside press
    await fireEvent.pointerDown(trigger);
    expect(screen.getByRole('menu')).toBeTruthy();
    await fireEvent.click(trigger);
    expect(screen.queryByRole('menu')).toBeNull();
    await fireEvent.click(trigger);
    await fireEvent.pointerDown(document.body);
    expect(screen.queryByRole('menu')).toBeNull();
  });

  it('stays closed while unavailable but keeps the focus', async () => {
    const { trigger } = setup({ disabled: true });
    expect(trigger.getAttribute('aria-disabled')).toBe('true');
    trigger.focus();
    expect(document.activeElement).toBe(trigger);
    await fireEvent.click(trigger);
    await fireEvent.keyDown(trigger, { key: 'ArrowDown' });
    expect(screen.queryByRole('menu')).toBeNull();
  });

  it('can be opened by its owner (openMenu) on the checked item', async () => {
    const { component } = setup();
    (component as unknown as { openMenu: (f?: 'checked') => void }).openMenu('checked');
    await tick();
    await waitFor(() => expect(document.activeElement?.getAttribute('aria-checked')).toBe('true'));
  });
});

describe('SplitButton', () => {
  it('runs its action from the main half and offers the other ways from the chevron', async () => {
    const onclick = vi.fn();
    const onselect = vi.fn();
    render(SplitButton, {
      props: {
        label: 'Add',
        icon: 'add',
        onclick,
        onselect,
        title: 'Add downloads (Ctrl+N)',
        keyshortcuts: 'Control+N',
        items: [
          { id: 'files', label: 'Add downloads…', shortcut: 'Ctrl+N' },
          { id: 'stream', label: 'Add video or stream…' },
        ],
      },
    });
    const main = screen.getByRole('button', { name: 'Add' });
    expect(main.getAttribute('aria-keyshortcuts')).toBe('Control+N');
    await fireEvent.click(main);
    expect(onclick).toHaveBeenCalledTimes(1);
    const more = screen.getByRole('button', { name: 'More ways to add' });
    expect(more.getAttribute('aria-haspopup')).toBe('menu');
    await fireEvent.click(more);
    await fireEvent.click(screen.getByRole('menuitem', { name: 'Add video or stream…' }));
    expect(onselect).toHaveBeenCalledWith('stream');
    expect(document.activeElement).toBe(more);
  });

  it('does nothing while unavailable', async () => {
    const onclick = vi.fn();
    render(SplitButton, { props: { label: 'Add', onclick, onselect: vi.fn(), items: [{ id: 'x', label: 'X' }], disabled: true } });
    await fireEvent.click(screen.getByRole('button', { name: 'Add' }));
    await fireEvent.click(screen.getByRole('button', { name: 'More ways to add' }));
    expect(onclick).not.toHaveBeenCalled();
    expect(screen.queryByRole('menu')).toBeNull();
  });
});
