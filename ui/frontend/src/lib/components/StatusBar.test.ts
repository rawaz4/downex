import { fireEvent, render, screen, within } from '@testing-library/svelte';
import { tick } from 'svelte';
import { describe, expect, it, vi } from 'vitest';
import StatusBar from './StatusBar.svelte';
import type { PostAction } from '../api';

const MB = 1000 * 1000;
const GB = 1000 * MB;
const STATUS = { version: '1.2.3', pid: 1, startedAt: 1, active: 0, queued: 0, endpoint: '' };

function setup(props: Record<string, unknown> = {}) {
  const onlimit = vi.fn();
  const onpostaction = vi.fn();
  const r = render(StatusBar, {
    props: {
      daemon: { ok: true, status: STATUS },
      demo: false,
      totals: { speed: 3 * MB, active: 3, queued: 7, count: 28 },
      shown: 28,
      selected: 1,
      limit: 0,
      onlimit,
      postAction: 'none' as PostAction,
      postActionChoices: ['none', 'quit_downex', 'sleep', 'hibernate', 'shutdown'] as PostAction[],
      onpostaction,
      freeBytes: 214 * GB,
      driveLabel: 'D:',
      needBytes: 0,
      ...props,
    },
  });
  return { ...r, onlimit, onpostaction };
}

const limitButton = () => screen.getByRole('button', { name: /^Global speed limit:/ });
const whenDoneButton = () => screen.getByRole('button', { name: /^When done:/ });

describe('StatusBar', () => {
  it('shows the connection, the total speed, the counts and the selection', () => {
    setup();
    const bar = screen.getByRole('contentinfo');
    expect(bar.textContent).toContain('Connected');
    expect(bar.querySelector('[title="Connected to downexd 1.2.3"]')).toBeTruthy();
    expect(bar.textContent).toContain('Total speed 3.0 MB/s');
    expect(within(bar).getByRole('img', { name: 'Total speed over the last minute' })).toBeTruthy();
    expect(bar.textContent).toContain('3 active · 7 queued');
    expect(bar.textContent).toContain('28 items · 1 selected');
  });

  it('says when downexd is unreachable, connecting or simulated', async () => {
    const { rerender } = setup({ daemon: { ok: false, error: 'connection refused' } });
    const bar = screen.getByRole('contentinfo');
    expect(bar.textContent).toContain('downexd unavailable');
    expect(bar.querySelector('[title^="downexd unavailable: "]')?.getAttribute('title')).toContain('connection refused');
    await rerender({ daemon: null });
    expect(bar.textContent).toContain('Connecting…');
    await rerender({ demo: true });
    expect(bar.textContent).toContain('Demo mode');
  });

  it('chooses the global speed limit from a menu that opens upward', async () => {
    const { onlimit, rerender } = setup();
    const trigger = limitButton();
    expect(trigger.textContent).toContain('Unlimited');
    expect(trigger.getAttribute('aria-haspopup')).toBe('menu');
    await fireEvent.click(trigger);
    const menu = await screen.findByRole('menu', { name: 'Global speed limit' });
    expect(within(menu).getByRole('group', { name: 'Global speed limit' })).toBeTruthy();
    const radios = within(menu).getAllByRole('menuitemradio');
    expect(radios.map((r) => r.textContent?.trim())).toEqual(['Unlimited', '20 MB/s', '10 MB/s', '5 MB/s', '1 MB/s']);
    expect(radios[0]?.getAttribute('aria-checked')).toBe('true');
    expect(menu.textContent).toContain('Per-download and queue limits still apply.');
    await fireEvent.click(within(menu).getByRole('menuitemradio', { name: '10 MB/s' }));
    expect(onlimit).toHaveBeenCalledWith(10 * MB);
    expect(screen.queryByRole('menu')).toBeNull();

    // Custom… opens Settings (the caller does)
    await fireEvent.click(limitButton());
    await fireEvent.click(await screen.findByRole('menuitem', { name: 'Custom…' }));
    expect(onlimit).toHaveBeenLastCalledWith('custom');

    // a limit in effect reads as a warning; one set elsewhere is listed as checked
    await rerender({ limit: 3 * MB });
    expect(limitButton().textContent).toContain('Limit 3.0 MB/s');
    expect(limitButton().classList.contains('warn')).toBe(true);
    await fireEvent.click(limitButton());
    const checked = (await screen.findByRole('menu')).querySelector('[aria-checked="true"]');
    expect(checked?.textContent).toContain('3.0 MB/s');
    expect(checked?.textContent).toContain('custom');
  });

  it('keeps the menus closed while downexd is unreachable or the settings are unknown', async () => {
    const { onlimit, rerender } = setup({ stale: true });
    expect(limitButton().getAttribute('aria-disabled')).toBe('true');
    expect(whenDoneButton().getAttribute('aria-disabled')).toBe('true');
    await fireEvent.click(limitButton());
    await fireEvent.click(whenDoneButton());
    await tick();
    expect(screen.queryByRole('menu')).toBeNull();
    await rerender({ stale: false, limit: null });
    expect(limitButton().getAttribute('aria-disabled')).toBe('true');
    expect(onlimit).not.toHaveBeenCalled();
  });

  it('says what happens when the main queue is done, in words, and changes it', async () => {
    const { onpostaction, rerender } = setup();
    const trigger = whenDoneButton();
    expect(trigger.textContent).toContain('When done: keep running');
    expect(trigger.classList.contains('warn')).toBe(false);
    await fireEvent.click(trigger);
    const menu = await screen.findByRole('menu', { name: 'When the main queue is done' });
    expect(within(menu).getAllByRole('menuitemradio').map((r) => r.textContent?.trim())).toEqual([
      'Keep running',
      'Quit Downex',
      'Sleep',
      'Hibernate',
      'Shut down',
    ]);
    expect(within(menu).getByRole('menuitemradio', { name: 'Keep running' }).getAttribute('aria-checked')).toBe('true');
    await fireEvent.click(within(menu).getByRole('menuitemradio', { name: 'Shut down' }));
    expect(onpostaction).toHaveBeenCalledWith('shutdown');

    await fireEvent.click(whenDoneButton());
    await fireEvent.click(await screen.findByRole('menuitem', { name: 'Other queues…' }));
    expect(onpostaction).toHaveBeenLastCalledWith('other');

    // a power action is a warning
    await rerender({ postAction: 'shutdown' });
    expect(whenDoneButton().textContent).toContain('When done: shut down');
    expect(whenDoneButton().classList.contains('warn')).toBe(true);
    await rerender({ postAction: 'quit_downex' });
    expect(whenDoneButton().textContent).toContain('When done: Quit Downex');
    expect(whenDoneButton().classList.contains('warn')).toBe(false);

    // no queues (an older downexd): no control
    await rerender({ postAction: null });
    expect(screen.queryByRole('button', { name: /^When done:/ })).toBeNull();
  });

  it('opens its menus for the command palette', async () => {
    const { component } = setup();
    (component as unknown as { openLimitMenu(): void }).openLimitMenu();
    await tick();
    await tick();
    expect(screen.getByRole('menu', { name: 'Global speed limit' })).toBeTruthy();
    await fireEvent.keyDown(screen.getByRole('menu'), { key: 'Escape' });
    expect(screen.queryByRole('menu')).toBeNull();
    expect((component as unknown as { openWhenDoneMenu(): boolean }).openWhenDoneMenu()).toBe(true);
    await tick();
    expect(screen.getByRole('menu', { name: 'When the main queue is done' })).toBeTruthy();
  });

  it('shows the free space of the download drive, warns when it is too small and hides when unknown', async () => {
    const { rerender } = setup();
    const bar = screen.getByRole('contentinfo');
    const free = () => bar.querySelector<HTMLElement>('.free');
    expect(free()?.textContent).toContain('D:');
    expect(free()?.textContent).toContain('214 GB free');
    expect(free()?.dataset.short).toBeUndefined();
    expect(free()?.getAttribute('title')).toBe('214 GB free on the drive of the download folder');

    await rerender({ freeBytes: 2 * GB, needBytes: 5 * GB });
    expect(free()?.dataset.short).toBe('true');
    expect(free()?.classList.contains('warn')).toBe(true);
    const warning = 'Not enough free space for the active and queued downloads (about 3.0 GB more needed)';
    expect(free()?.getAttribute('title')).toBe(warning);
    expect(free()?.textContent).toContain(warning);

    await rerender({ freeBytes: null });
    expect(free()).toBeNull();
    expect(bar.textContent).not.toContain('free');
  });
});
