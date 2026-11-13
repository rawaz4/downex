import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import ContextMenu, { type MenuItem } from './ContextMenu.svelte';

const items: MenuItem[] = [
  { id: 'open', label: 'Open', disabled: true },
  { id: 'folder', label: 'Open folder' },
  { id: 'pause', label: 'Pause', separated: true },
  { id: 'remove', label: 'Remove…', danger: true },
];

function setup() {
  const onselect = vi.fn();
  const onclose = vi.fn();
  render(ContextMenu, { props: { x: 10, y: 10, items, label: 'Download actions', onselect, onclose } });
  return { onselect, onclose, menu: screen.getByRole('menu', { name: 'Download actions' }) };
}

describe('ContextMenu', () => {
  it('focuses the first enabled item and moves with the arrow keys', async () => {
    const { menu } = setup();
    await waitFor(() => expect(document.activeElement?.getAttribute('role')).toBe('menuitem'));
    expect(document.activeElement?.textContent).toContain('Open folder');
    await fireEvent.keyDown(menu, { key: 'ArrowDown' });
    expect(document.activeElement?.textContent).toContain('Pause');
    await fireEvent.keyDown(menu, { key: 'End' });
    expect(document.activeElement?.textContent).toContain('Remove');
    await fireEvent.keyDown(menu, { key: 'ArrowDown' });
    expect(document.activeElement?.textContent).toContain('Open folder');
    await fireEvent.keyDown(menu, { key: 'p' });
    expect(document.activeElement?.textContent).toContain('Pause');
  });

  it('activates enabled items only and closes on Escape or an outside press', async () => {
    const { onselect, onclose, menu } = setup();
    await fireEvent.click(screen.getByRole('menuitem', { name: 'Open' }));
    expect(onselect).not.toHaveBeenCalled();
    await fireEvent.click(screen.getByRole('menuitem', { name: 'Pause' }));
    expect(onselect).toHaveBeenCalledWith('pause');
    await fireEvent.keyDown(menu, { key: 'Escape' });
    expect(onclose).toHaveBeenCalledTimes(1);
    await fireEvent.pointerDown(document.body);
    expect(onclose).toHaveBeenCalledTimes(2);
  });
});

describe('ContextMenu: Studio additions', () => {
  it('draws radio and checkbox items, notes, headings and a footnote', async () => {
    render(ContextMenu, {
      props: {
        x: 0,
        y: 0,
        label: 'More actions',
        onselect: vi.fn(),
        onclose: vi.fn(),
        footnote: 'Saved on this computer right away.',
        items: [
          { id: 'comfortable', label: 'Comfortable', kind: 'radio', checked: true, heading: 'Density' },
          { id: 'compact', label: 'Compact', kind: 'radio', checked: false, note: '18 rows' },
          { id: 'pane', label: 'Details pane', kind: 'checkbox', checked: true, separated: true },
          { id: 'queues', label: 'Manage queues…', icon: 'queue' },
        ],
      },
    });
    const menu = screen.getByRole('menu', { name: 'More actions' });
    expect(screen.getByRole('menuitemradio', { name: 'Comfortable' }).getAttribute('aria-checked')).toBe('true');
    expect(screen.getByRole('menuitemradio', { name: /^Compact/ }).getAttribute('aria-checked')).toBe('false');
    expect(screen.getByRole('menuitemradio', { name: /^Compact/ }).textContent).toContain('18 rows');
    expect(screen.getByRole('menuitemcheckbox', { name: 'Details pane' }).getAttribute('aria-checked')).toBe('true');
    expect(screen.getByRole('group', { name: 'Density' }).querySelectorAll('[role="menuitemradio"]')).toHaveLength(2);
    expect(menu.querySelectorAll('[role="separator"]')).toHaveLength(2);
    expect(document.getElementById(menu.getAttribute('aria-describedby') ?? '')?.textContent).toBe('Saved on this computer right away.');
    // every kind of item takes part in the keyboard model
    await waitFor(() => expect(document.activeElement?.getAttribute('role')).toBe('menuitemradio'));
    expect(document.activeElement?.textContent).toContain('Comfortable');
    await fireEvent.keyDown(menu, { key: 'End' });
    expect(document.activeElement?.textContent).toContain('Manage queues');
    await fireEvent.keyDown(menu, { key: 'ArrowUp' });
    expect(document.activeElement?.getAttribute('role')).toBe('menuitemcheckbox');
  });

  it('opens next to its anchor, above it when there is no room below, and ignores presses on it', async () => {
    const anchor = document.createElement('button');
    anchor.textContent = 'Unlimited';
    document.body.append(anchor);
    // a trigger in the status bar, at the bottom end of an 800 px window
    anchor.getBoundingClientRect = () => ({ left: 900, right: 1000, top: 760, bottom: 784, width: 100, height: 24, x: 900, y: 760, toJSON: () => ({}) });
    const onclose = vi.fn();
    try {
      render(ContextMenu, {
        props: {
          label: 'Global speed limit',
          onselect: vi.fn(),
          onclose,
          anchor: { el: anchor, align: 'end', direction: 'down' },
          initialFocus: 'last',
          items: [
            { id: 'a', label: 'Unlimited' },
            { id: 'b', label: '10 MiB/s' },
          ],
        },
      });
      const menu = screen.getByRole('menu');
      menu.getBoundingClientRect = () => ({ left: 0, right: 220, top: 0, bottom: 120, width: 220, height: 120, x: 0, y: 0, toJSON: () => ({}) });
      await waitFor(() => expect(document.activeElement?.getAttribute('role')).toBe('menuitem'));
      expect(document.activeElement?.textContent).toContain('10 MiB/s');
      expect(window.innerHeight).toBe(768);
      // no room below 784 in a 768 px window: above the anchor, end-aligned (1000 - 220)
      expect(menu.style.top).toBe(`${760 - 4 - 120}px`);
      expect(menu.style.left).toBe(`${Math.min(1000 - 220, window.innerWidth - 220 - 8)}px`);
      await fireEvent.pointerDown(anchor);
      expect(onclose).not.toHaveBeenCalled();
      await fireEvent.pointerDown(document.body);
      expect(onclose).toHaveBeenCalledTimes(1);
    } finally {
      anchor.remove();
    }
  });
});
