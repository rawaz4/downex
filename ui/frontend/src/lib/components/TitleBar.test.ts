import { fireEvent, render, screen } from '@testing-library/svelte';
import { tick } from 'svelte';
import { describe, expect, it, vi } from 'vitest';
import TitleBar from './TitleBar.svelte';
import type { WindowControls } from '../api';

type Controls = Pick<WindowControls, 'minimise' | 'toggleMaximise' | 'isMaximised' | 'close'>;

/** A window whose maximised state the test can also change "from the system". */
function fakeWindow(): Controls & { state: { maximised: boolean; minimised: number; closed: number; toggles: number } } {
  const state = { maximised: false, minimised: 0, closed: 0, toggles: 0 };
  return {
    state,
    minimise: () => void state.minimised++,
    toggleMaximise: () => {
      state.toggles++;
      state.maximised = !state.maximised;
    },
    isMaximised: async () => state.maximised,
    close: () => void state.closed++,
  };
}

function setup(props: Record<string, unknown> = {}) {
  const onsearch = vi.fn();
  const oncommands = vi.fn();
  const onsettings = vi.fn();
  const r = render(TitleBar, { props: { search: '', onsearch, oncommands, onsettings, nativeWait: 0, ...props } });
  const header = r.container.querySelector<HTMLElement>('header.titlebar')!;
  return { ...r, header, onsearch, oncommands, onsettings };
}

const settle = async () => {
  for (let i = 0; i < 4; i++) {
    await Promise.resolve();
    await tick();
  }
};

describe('TitleBar', () => {
  it('shows the logo, the list search, the palette key, theme and Settings', async () => {
    const { onsearch, oncommands, onsettings } = setup({ search: 'iso' });
    expect(screen.getByText('Downex')).toBeTruthy();
    const search = screen.getByRole('searchbox', { name: 'Search downloads by name or URL' }) as HTMLInputElement;
    expect(search.value).toBe('iso');
    expect(search.getAttribute('aria-keyshortcuts')).toBe('Control+F');
    await fireEvent.input(search, { target: { value: 'isos' } });
    expect(onsearch).toHaveBeenLastCalledWith('isos');
    // Escape clears a search (and only then is it taken)
    expect(await fireEvent.keyDown(search, { key: 'Escape' })).toBe(false);
    expect(onsearch).toHaveBeenLastCalledWith('');

    const commands = screen.getByRole('button', { name: 'Commands' });
    expect(commands.getAttribute('aria-keyshortcuts')).toBe('Control+K');
    expect(commands.getAttribute('title')).toBe('Commands (Ctrl+K)');
    expect(commands.textContent?.trim()).toBe('Ctrl K');
    await fireEvent.click(commands);
    expect(oncommands).toHaveBeenCalledTimes(1);

    await fireEvent.click(screen.getByRole('button', { name: 'Settings' }));
    expect(onsettings).toHaveBeenCalledTimes(1);
    expect(screen.getByRole('button', { name: /theme\. Switch to/ })).toBeTruthy();
  });

  it('turns Wails’ resize edges off while maximised, and back on when restored', async () => {
    const flags: { enableResize?: boolean; resizeEdge?: string; defaultCursor?: string | null } = {
      enableResize: true,
      resizeEdge: 'n-resize',
      defaultCursor: '',
    };
    (window as { wails?: unknown }).wails = { flags };
    try {
      const win = fakeWindow();
      win.state.maximised = true;
      setup({ customTitleBar: true, controls: win });
      await settle();
      expect(flags.enableResize).toBe(false);
      expect(flags.resizeEdge).toBeUndefined();
      await fireEvent.click(screen.getByRole('button', { name: 'Restore' }));
      await settle();
      expect(win.state.maximised).toBe(false);
      expect(flags.enableResize).toBe(true);
      // a window Wails made not resizable stays so
      flags.enableResize = false;
      await fireEvent.click(screen.getByRole('button', { name: 'Maximise' }));
      await settle();
      await fireEvent.click(screen.getByRole('button', { name: 'Restore' }));
      await settle();
      expect(flags.enableResize).toBe(false);
    } finally {
      delete (window as { wails?: unknown }).wails;
    }
  });

  it('has no window controls and no drag region without a custom title bar', () => {
    const { header } = setup({ controls: fakeWindow() });
    for (const name of ['Minimise', 'Maximise', 'Restore', 'Close']) {
      expect(screen.queryByRole('button', { name })).toBeNull();
    }
    expect(header.style.getPropertyValue('--wails-draggable')).toBe('');
  });

  it('draws the window controls and the drag region of a frameless window', async () => {
    const win = fakeWindow();
    const { header } = setup({ customTitleBar: true, controls: win });
    await settle();
    // the whole bar drags the window; its controls do not
    expect(header.style.getPropertyValue('--wails-draggable')).toBe('drag');
    const search = screen.getByRole('searchbox');
    expect(search.closest<HTMLElement>('.search')?.style.getPropertyValue('--wails-draggable')).toBe('no-drag');
    const settings = screen.getByRole('button', { name: 'Settings' });
    expect(settings.closest<HTMLElement>('.end')?.style.getPropertyValue('--wails-draggable')).toBe('no-drag');

    const group = screen.getByRole('group', { name: 'Window' });
    expect([...group.querySelectorAll('button')].map((b) => b.getAttribute('aria-label'))).toEqual(['Minimise', 'Maximise', 'Close']);

    await fireEvent.click(screen.getByRole('button', { name: 'Minimise' }));
    expect(win.state.minimised).toBe(1);

    // Maximise turns into Restore (its glyph follows the window), and back
    await fireEvent.click(screen.getByRole('button', { name: 'Maximise' }));
    await settle();
    const restore = screen.getByRole('button', { name: 'Restore' });
    expect(restore.dataset.maximised).toBe('true');
    expect(win.state.maximised).toBe(true);
    await fireEvent.click(restore);
    await settle();
    expect(screen.getByRole('button', { name: 'Maximise' }).dataset.maximised).toBe('false');

    await fireEvent.click(screen.getByRole('button', { name: 'Close' }));
    expect(win.state.closed).toBe(1);
  });

  it('follows a maximise by the system (snap, Win+Up) when the window resizes', async () => {
    const win = fakeWindow();
    setup({ customTitleBar: true, controls: win });
    await settle();
    expect(screen.getByRole('button', { name: 'Maximise' })).toBeTruthy();
    win.state.maximised = true;
    window.dispatchEvent(new Event('resize'));
    await settle();
    expect(screen.getByRole('button', { name: 'Restore' })).toBeTruthy();
  });

  it('maximises on a double-click on empty bar space, once', async () => {
    const win = fakeWindow();
    const { header } = setup({ customTitleBar: true, controls: win });
    await settle();
    const brand = screen.getByText('Downex');
    await fireEvent.mouseDown(brand, { button: 0, detail: 1 });
    await fireEvent.mouseDown(brand, { button: 0, detail: 2 });
    await fireEvent.dblClick(brand);
    await vi.waitFor(() => expect(win.state.toggles).toBe(1));
    expect(win.state.maximised).toBe(true);
    await vi.waitFor(() => expect(screen.getByRole('button', { name: 'Restore' })).toBeTruthy());

    // not on its controls
    await fireEvent.dblClick(screen.getByRole('searchbox'));
    await fireEvent.dblClick(screen.getByRole('button', { name: 'Settings' }));
    await settle();
    expect(win.state.toggles).toBe(1);

    // When the system maximised it by itself, the bar does not toggle it back.
    win.state.maximised = false;
    await fireEvent.mouseDown(header, { button: 0, detail: 1 });
    await settle();
    win.state.maximised = true; // the system's own double-click handling
    await fireEvent.dblClick(header);
    await settle();
    await new Promise((r) => setTimeout(r, 5));
    expect(win.state.toggles).toBe(1);
    expect(win.state.maximised).toBe(true);
  });

  it('ignores double-clicks without a custom title bar', async () => {
    const win = fakeWindow();
    const { header } = setup({ controls: win });
    await fireEvent.dblClick(header);
    await settle();
    expect(win.state.toggles).toBe(0);
  });
});
