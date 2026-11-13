import { fireEvent, render, screen, within } from '@testing-library/svelte';
import { tick } from 'svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import App from './App.svelte';
import { DEFAULT_SETTINGS, MAIN_QUEUE_ID } from './lib/api';
import { density } from './lib/stores/density.svelte';
import { MB, MiB, fakeApi, makeDownload } from './lib/testing';

const GiB = 1024 * MiB;
const rowOf = (id: number) => document.getElementById(`dl-row-${id}`)!;
const card = () => document.querySelector<HTMLElement>('main.card')!;
const statusBar = () => screen.getByRole('contentinfo');
const limitButton = () => within(statusBar()).getByRole('button', { name: /^Global speed limit:/ });
/** `el` or an ancestor is inert (jsdom keeps Svelte's `inert` as a property, not an attribute). */
const isInert = (el: Element): boolean => {
  for (let n: Element | null = el; n; n = n.parentElement) if ((n as HTMLElement & { inert?: boolean }).inert === true || n.hasAttribute('inert')) return true;
  return false;
};
const whenDoneButton = () => within(statusBar()).queryByRole('button', { name: /^When done:/ });

afterEach(() => {
  density.set('comfortable');
  vi.useRealTimers();
});

describe('App shell: the title bar', () => {
  it('draws the window controls only for a frameless host window', async () => {
    const api = fakeApi([makeDownload({ id: 1 })], undefined, { customTitleBar: true });
    render(App, { props: { api, capabilities: { ...api.view } } });
    await vi.waitFor(() => expect(rowOf(1)).toBeTruthy());
    const controls = screen.getByRole('group', { name: 'Window' });
    expect(within(controls).getAllByRole('button').map((b) => b.getAttribute('aria-label'))).toEqual(['Minimise', 'Maximise', 'Close']);
    await fireEvent.click(within(controls).getByRole('button', { name: 'Minimise' }));
    expect(api.win.minimised).toBe(1);
    await fireEvent.click(within(controls).getByRole('button', { name: 'Maximise' }));
    expect(api.win.maximised).toBe(true);
    await vi.waitFor(() => expect(within(controls).getByRole('button', { name: 'Restore' })).toBeTruthy());
    expect(document.querySelector<HTMLElement>('header.titlebar')?.style.getPropertyValue('--wails-draggable')).toBe('drag');
  });

  it('keeps the drag region and the window controls usable above an open dialog', async () => {
    const api = fakeApi([makeDownload({ id: 1 })], undefined, { customTitleBar: true });
    render(App, { props: { api, capabilities: { ...api.view } } });
    await vi.waitFor(() => expect(rowOf(1)).toBeTruthy());
    await fireEvent.keyDown(window, { key: ',', ctrlKey: true });
    await screen.findByRole('dialog', { name: 'Settings' });
    const bar = document.querySelector<HTMLElement>('header.titlebar')!;
    const controls = bar.querySelector<HTMLElement>('.winctl')!;
    expect(isInert(controls)).toBe(false);
    expect(isInert(rowOf(1))).toBe(true);
    expect(bar.classList.contains('modal')).toBe(true);
    expect(bar.style.getPropertyValue('--wails-draggable')).toBe('drag');
    // the page's own controls in the bar are inert, as the page is
    expect(isInert(screen.getByLabelText('Search downloads by name or URL'))).toBe(true);
    expect(isInert(screen.getByRole('button', { name: 'Settings', hidden: true }))).toBe(true);
    // the window controls work and Tab reaches them from the dialog (an exempt region)
    expect(controls.getAttribute('data-focus-trap-exempt')).toBe('tab');
    await fireEvent.click(within(controls).getByRole('button', { name: 'Minimise' }));
    expect(api.win.minimised).toBe(1);
    await fireEvent.click(within(controls).getByRole('button', { name: 'Maximise' }));
    expect(api.win.maximised).toBe(true);
    expect(screen.getByRole('dialog', { name: 'Settings' })).toBeTruthy();
  });

  it('keeps the system frame (no controls) by default, with the search and the palette key', async () => {
    const api = fakeApi([makeDownload({ id: 1 }), makeDownload({ id: 2, filename: 'other.iso', createdAt: 1 })]);
    render(App, { props: { api } });
    await vi.waitFor(() => expect(rowOf(1)).toBeTruthy());
    expect(screen.queryByRole('group', { name: 'Window' })).toBeNull();
    expect(document.querySelector<HTMLElement>('header.titlebar')?.style.getPropertyValue('--wails-draggable')).toBe('');
    // the list search moved here unchanged
    await fireEvent.keyDown(window, { key: 'f', ctrlKey: true });
    const search = screen.getByRole('searchbox', { name: 'Search downloads by name or URL' });
    expect(document.activeElement).toBe(search);
    await fireEvent.input(search, { target: { value: 'other' } });
    await vi.waitFor(() => expect(document.getElementById('dl-row-1')).toBeNull());
    await fireEvent.click(screen.getByRole('button', { name: 'Commands' }));
    expect(await screen.findByRole('dialog', { name: 'Command palette' })).toBeTruthy();
  });
});

describe('App shell: the page header', () => {
  it('names the filter shown, counts it, and filters by its chips', async () => {
    const api = fakeApi([
      makeDownload({ id: 1, state: 'downloading', speedBps: MiB, received: MiB }),
      makeDownload({ id: 2, state: 'queued' }),
      makeDownload({ id: 3, state: 'failed', error: 'HTTP 503' }),
      makeDownload({ id: 4, state: 'completed', received: 10 * MiB }),
    ]);
    render(App, { props: { api } });
    await vi.waitFor(() => expect(rowOf(4)).toBeTruthy());
    const header = document.querySelector<HTMLElement>('header.toolbar')!;
    expect(within(header).getByRole('heading', { level: 1 }).textContent).toBe('All downloads');
    expect(header.querySelector('.count')?.textContent).toBe('4');
    const downloading = within(header).getByRole('button', { name: '1 downloading' });
    expect(within(header).getByRole('button', { name: '1 queued' })).toBeTruthy();
    expect(within(header).getByRole('button', { name: '1 failed' })).toBeTruthy();
    await fireEvent.click(downloading);
    expect(downloading.getAttribute('aria-pressed')).toBe('true');
    await vi.waitFor(() => expect(document.getElementById('dl-row-2')).toBeNull());
    expect(within(header).getByRole('heading', { level: 1 }).textContent).toBe('Active');
    expect(header.querySelector('.count')?.textContent).toBe('1');
    await fireEvent.click(downloading);
    await vi.waitFor(() => expect(rowOf(2)).toBeTruthy());
  });

  it('switches the list density from the More menu, and remembers it', async () => {
    const api = fakeApi([makeDownload({ id: 1 })]);
    render(App, { props: { api } });
    await vi.waitFor(() => expect(rowOf(1)).toBeTruthy());
    const more = screen.getByRole('button', { name: 'More actions' });
    await fireEvent.click(more);
    const menu = await screen.findByRole('menu', { name: 'More actions' });
    expect(within(menu).getByRole('group', { name: 'List density' })).toBeTruthy();
    expect(within(menu).getByRole('menuitemradio', { name: /Comfortable/ }).getAttribute('aria-checked')).toBe('true');
    await fireEvent.click(within(menu).getByRole('menuitemradio', { name: /Compact/ }));
    expect(document.documentElement.dataset.density).toBe('compact');
    expect(localStorage.getItem('downex.density')).toBe('compact');
    expect(document.activeElement).toBe(more);
    await fireEvent.click(more);
    expect(within(await screen.findByRole('menu')).getByRole('menuitemradio', { name: /Compact/ }).getAttribute('aria-checked')).toBe('true');
  });

  it('keeps the window shortcuts out of an open menu', async () => {
    const api = fakeApi([makeDownload({ id: 1, state: 'downloading', speedBps: MiB, received: MiB })]);
    render(App, { props: { api } });
    await vi.waitFor(() => expect(rowOf(1)).toBeTruthy());
    await fireEvent.click(rowOf(1));
    await fireEvent.click(screen.getByRole('button', { name: 'More actions' }));
    await vi.waitFor(() => expect(document.activeElement?.getAttribute('role')).toMatch(/^menuitem/));
    await fireEvent.keyDown(document.activeElement!, { key: 'Delete' });
    await fireEvent.keyDown(document.activeElement!, { key: 'a', ctrlKey: true });
    await tick();
    expect(screen.queryByRole('alertdialog')).toBeNull();
    expect(api.calls).not.toContain('remove');
  });

  it('opens Add in either mode from its split button', async () => {
    const api = fakeApi([makeDownload({ id: 1 })]);
    render(App, { props: { api } });
    await vi.waitFor(() => expect(rowOf(1)).toBeTruthy());
    await fireEvent.click(screen.getByRole('button', { name: 'More ways to add' }));
    await fireEvent.click(await screen.findByRole('menuitem', { name: /Add video or stream/ }));
    const dialog = await screen.findByRole('dialog', { name: 'Add video or stream' });
    await fireEvent.keyDown(dialog, { key: 'Escape' });
    await vi.waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    await fireEvent.click(screen.getAllByRole('button', { name: 'Add' }).find((b) => b.closest('header.toolbar'))!);
    expect(await screen.findByRole('dialog', { name: 'Add downloads' })).toBeTruthy();
  });
});

describe('App shell: the details pane', () => {
  it('is not there while nothing is selected', async () => {
    const api = fakeApi([makeDownload({ id: 1, state: 'paused' }), makeDownload({ id: 2, createdAt: 1 })]);
    render(App, { props: { api } });
    await vi.waitFor(() => expect(rowOf(1)).toBeTruthy());
    expect(within(card()).queryAllByRole('region')).toHaveLength(0);
    await fireEvent.click(rowOf(1));
    await vi.waitFor(() => expect(within(card()).getAllByRole('region')).toHaveLength(1));
    // Escape clears the selection, and the pane goes with it
    await fireEvent.keyDown(screen.getByRole('grid'), { key: 'Escape' });
    await vi.waitFor(() => expect(within(card()).queryAllByRole('region')).toHaveLength(0));
    expect(rowOf(1).getAttribute('aria-selected')).toBe('false');
  });
});

describe('App shell: the sidebar footer', () => {
  it('says honestly what browser downloads do and opens Settings → Integration', async () => {
    const api = fakeApi([makeDownload({ id: 1 })], { ...DEFAULT_SETTINGS, defaultDir: '/srv/dl', confirmBrowserDownloads: false });
    render(App, { props: { api } });
    const nav = await screen.findByRole('navigation', { name: 'Filters' });
    const footer = await within(nav).findByRole('button', { name: /Browser downloads: start at once/ });
    // the category buttons stay first
    expect(within(nav).getAllByRole('button')[0]?.querySelector('.label')?.textContent).toBe('All downloads');
    await fireEvent.click(footer);
    const settings = await screen.findByRole('dialog', { name: 'Settings' });
    expect(within(settings).getByRole('tab', { name: 'Integration' }).getAttribute('aria-selected')).toBe('true');
  });

  it('puts the toasts under an open dialog, so they never cover its buttons', async () => {
    render(App, { props: { api: fakeApi([makeDownload({ id: 1 })]) } });
    const nav = await screen.findByRole('navigation', { name: 'Filters' });
    const toasts = document.querySelector<HTMLElement>('.toasts')!;
    expect(toasts.classList.contains('behind')).toBe(false);
    await fireEvent.click(await within(nav).findByRole('button', { name: /Browser downloads:/ }));
    await screen.findByRole('dialog', { name: 'Settings' });
    expect(toasts.classList.contains('behind')).toBe(true);
    // the notices (with actions, mostly shown because a dialog is in the way) stay above it
    const notices = document.querySelector<HTMLElement>('.toasts.notice-stack')!;
    expect(notices.classList.contains('behind')).toBe(false);
    expect(notices.getAttribute('data-focus-trap-exempt')).toBe('tab');
    await fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    await vi.waitFor(() => expect(screen.queryByRole('dialog', { name: 'Settings' })).toBeNull());
    expect(toasts.classList.contains('behind')).toBe(false);
  });

  it('asks first by default', async () => {
    render(App, { props: { api: fakeApi([makeDownload({ id: 1 })]) } });
    const nav = await screen.findByRole('navigation', { name: 'Filters' });
    expect(await within(nav).findByRole('button', { name: /Browser downloads: ask first/ })).toBeTruthy();
  });
});

describe('App shell: the status bar', () => {
  it('sets the global speed limit with a full settings round-trip', async () => {
    const api = fakeApi([makeDownload({ id: 1 })]);
    render(App, { props: { api } });
    await vi.waitFor(() => expect(limitButton().getAttribute('aria-disabled')).toBeNull());
    expect(limitButton().textContent).toContain('Unlimited');
    await fireEvent.click(limitButton());
    const menu = await screen.findByRole('menu', { name: 'Global speed limit' });
    const reads = api.calls.filter((c) => c === 'getSettings').length;
    await fireEvent.click(within(menu).getByRole('menuitemradio', { name: '10 MB/s' }));
    await vi.waitFor(() => expect(api.calls).toContain('updateSettings'));
    // read first, then written back whole with only the limit changed
    expect(api.calls.filter((c) => c === 'getSettings').length).toBe(reads + 1);
    const [written] = api.args.get('updateSettings') as [typeof api.settings];
    expect(written).toEqual({ ...api.settings, globalSpeedLimit: 10 * MB });
    await vi.waitFor(() => expect(limitButton().textContent).toContain('Limit 10 MB/s'));
    expect(screen.getAllByRole('status').some((s) => s.textContent?.includes('Global speed limit: 10.0 MB/s'))).toBe(true);
  });

  it('opens Settings → Downloads for a custom limit', async () => {
    const api = fakeApi([makeDownload({ id: 1 })]);
    render(App, { props: { api } });
    await vi.waitFor(() => expect(limitButton().getAttribute('aria-disabled')).toBeNull());
    await fireEvent.click(limitButton());
    await fireEvent.click(await screen.findByRole('menuitem', { name: 'Custom…' }));
    const settings = await screen.findByRole('dialog', { name: 'Settings' });
    expect(within(settings).getByRole('tab', { name: 'Downloads' }).getAttribute('aria-selected')).toBe('true');
    expect(api.calls).not.toContain('updateSettings');
  });

  it('changes what happens when the main queue is done', async () => {
    const api = fakeApi([makeDownload({ id: 1 })]);
    render(App, { props: { api } });
    await vi.waitFor(() => expect(whenDoneButton()).toBeTruthy());
    expect(whenDoneButton()?.textContent).toContain('When done: keep running');
    await fireEvent.click(whenDoneButton()!);
    const menu = await screen.findByRole('menu', { name: 'When the main queue is done' });
    await fireEvent.click(within(menu).getByRole('menuitemradio', { name: 'Shut down' }));
    await vi.waitFor(() => expect(api.calls).toContain('updateQueue'));
    const [id, spec] = api.args.get('updateQueue') as [number, { postAction: string; name: string }];
    expect(id).toBe(MAIN_QUEUE_ID);
    expect(spec).toMatchObject({ name: 'Main queue', postAction: 'shutdown' });
    await vi.waitFor(() => expect(whenDoneButton()?.textContent).toContain('When done: shut down'));
  });

  it('hides "When done" for a downexd without queues', async () => {
    const api = fakeApi([makeDownload({ id: 1 })]);
    api.listQueues = async () => {
      throw new Error('unimplemented: downex.v1.QueueService.ListQueues is not implemented');
    };
    render(App, { props: { api } });
    await vi.waitFor(() => expect(rowOf(1)).toBeTruthy());
    await vi.waitFor(() => expect(api.calls).toContain('getSettings'));
    await tick();
    expect(whenDoneButton()).toBeNull();
  });

  it('shows the free space of the download drive, warns when it is too small, and refreshes it after a completion', async () => {
    const api = fakeApi(
      [makeDownload({ id: 1, state: 'queued', size: 3 * GiB }), makeDownload({ id: 2, state: 'downloading', size: 4 * GiB, received: GiB })],
      { ...DEFAULT_SETTINGS, defaultDir: 'D:\\Downloads' },
      { freeBytes: 200 * GiB },
    );
    render(App, { props: { api } });
    const free = () => statusBar().querySelector<HTMLElement>('.free');
    await vi.waitFor(() => expect(free()?.textContent).toContain('215 GB free'));
    expect(free()?.textContent).toContain('D:');
    expect(api.args.get('freeSpace')).toEqual(['D:\\Downloads']);
    expect(free()?.dataset.short).toBeUndefined();

    // 3 GiB + 3 GiB left to download, 5 GiB free
    api.freeBytes = 5 * GiB;
    const reads = api.calls.filter((c) => c === 'freeSpace').length;
    api.emit([{ kind: 'updated', download: makeDownload({ id: 3, state: 'completed', received: 10 * MiB }) }]);
    await vi.waitFor(() => expect(api.calls.filter((c) => c === 'freeSpace').length).toBe(reads + 1));
    await vi.waitFor(() => expect(free()?.dataset.short).toBe('true'));
    expect(free()?.getAttribute('title')).toBe('Not enough free space for the active and queued downloads (about 1.1 GB more needed)');
  });

  it('hides the free space when the host cannot tell, and stops asking', async () => {
    const api = fakeApi([makeDownload({ id: 1, state: 'queued' })], undefined, {
      freeBytes: 'unimplemented: the FreeSpace binding is not available',
    });
    render(App, { props: { api } });
    await vi.waitFor(() => expect(api.calls).toContain('freeSpace'));
    await tick();
    expect(statusBar().querySelector('.free')).toBeNull();
    api.emit([{ kind: 'updated', download: makeDownload({ id: 1, state: 'completed', received: 10 * MiB }) }]);
    await tick();
    await tick();
    expect(api.calls.filter((c) => c === 'freeSpace')).toHaveLength(1);
  });

  it('re-reads the free space every minute', async () => {
    vi.useFakeTimers();
    const api = fakeApi([makeDownload({ id: 1 })]);
    render(App, { props: { api } });
    await vi.advanceTimersByTimeAsync(100);
    const before = api.calls.filter((c) => c === 'freeSpace').length;
    expect(before).toBeGreaterThan(0);
    await vi.advanceTimersByTimeAsync(60_000);
    expect(api.calls.filter((c) => c === 'freeSpace').length).toBeGreaterThan(before);
  });
});

describe('App shell: the command palette', () => {
  it('lists running downloads; Enter shows a running one, Ctrl+Enter its folder', async () => {
    const api = fakeApi([
      makeDownload({ id: 1, filename: 'running.iso', state: 'downloading', speedBps: 2 * MiB, received: 3 * MiB }),
      makeDownload({ id: 2, filename: 'done.zip', state: 'completed', received: 10 * MiB }),
    ]);
    render(App, { props: { api } });
    await vi.waitFor(() => expect(rowOf(1)).toBeTruthy());
    await fireEvent.keyDown(window, { key: 'k', ctrlKey: true });
    const input = await screen.findByRole('combobox', { name: 'Search commands' });
    const running = screen.getByRole('group', { name: 'Running downloads' });
    expect(within(running).getByRole('option').textContent).toContain('running.iso');
    // every command says what it does
    for (const option of screen.getAllByRole('option').filter((o) => o.closest('[role="group"]') !== running)) {
      expect(option.getAttribute('aria-describedby')).toBeTruthy();
    }
    await fireEvent.keyDown(input, { key: 'Enter' });
    await vi.waitFor(() => expect(api.opened).toEqual([1]));

    await fireEvent.keyDown(window, { key: 'k', ctrlKey: true });
    const again = await screen.findByRole('combobox', { name: 'Search commands' });
    await fireEvent.input(again, { target: { value: 'done.zip' } });
    expect(screen.getAllByRole('option')[0]?.textContent).toContain('done.zip');
    await fireEvent.keyDown(again, { key: 'Enter', ctrlKey: true });
    await vi.waitFor(() => expect(api.calls).toContain('openFolder'));
    expect(api.calls).not.toContain('openFile');
  });

  it('offers density, speed limit and when-done as commands', async () => {
    const api = fakeApi([makeDownload({ id: 1 })]);
    render(App, { props: { api } });
    await vi.waitFor(() => expect(limitButton().getAttribute('aria-disabled')).toBeNull());
    await vi.waitFor(() => expect(whenDoneButton()).toBeTruthy());
    await fireEvent.keyDown(window, { key: 'k', ctrlKey: true });
    const input = await screen.findByRole('combobox', { name: 'Search commands' });
    await fireEvent.input(input, { target: { value: 'density compact' } });
    await fireEvent.keyDown(input, { key: 'Enter' });
    await vi.waitFor(() => expect(document.documentElement.dataset.density).toBe('compact'));

    await fireEvent.keyDown(window, { key: 'k', ctrlKey: true });
    const limit = await screen.findByRole('combobox', { name: 'Search commands' });
    await fireEvent.input(limit, { target: { value: 'global speed limit' } });
    expect(screen.getAllByRole('option')[0]?.textContent).toContain('Set global speed limit…');
    await fireEvent.keyDown(limit, { key: 'Enter' });
    expect(await screen.findByRole('menu', { name: 'Global speed limit' })).toBeTruthy();
    await fireEvent.keyDown(screen.getByRole('menu'), { key: 'Escape' });

    await fireEvent.keyDown(window, { key: 'k', ctrlKey: true });
    const done = await screen.findByRole('combobox', { name: 'Search commands' });
    await fireEvent.input(done, { target: { value: 'shut down' } });
    expect(screen.getAllByRole('option')[0]?.textContent).toContain('When done…');
    await fireEvent.keyDown(done, { key: 'Enter' });
    expect(await screen.findByRole('menu', { name: 'When the main queue is done' })).toBeTruthy();
  });
});
