import { fireEvent, render, screen, within } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import App from './App.svelte';
import { chooseOption, fakeApi, makeDownload, makeQueue } from './lib/testing';

const rowOf = (id: number) => document.getElementById(`dl-row-${id}`)!;
const sidebar = () => screen.getByRole('navigation', { name: 'Filters' });

function setup() {
  const api = fakeApi([
    makeDownload({ id: 1, filename: 'one.iso', state: 'queued' }),
    makeDownload({ id: 2, filename: 'two.iso', state: 'completed' }),
    makeDownload({ id: 3, filename: 'three.iso', state: 'queued', queueId: 2 }),
    makeDownload({
      id: 4,
      filename: 'private.zip',
      url: 'https://private.example.net/files/private.zip',
      state: 'failed',
      errorCode: 'auth_required',
      error: 'The server asks for a user name and password',
    }),
  ]);
  api.queues.set(1, makeQueue({ id: 1, downloadIds: [1, 2, 4] }));
  api.queues.set(2, makeQueue({ id: 2, name: 'Night', downloadIds: [3] }));
  render(App, { props: { api, capabilities: { openWith: true, hibernate: true, wakeTimers: true } } });
  return api;
}

describe('App: queues (M6)', () => {
  it('lists the queues in the sidebar and filters the list by queue', async () => {
    const api = setup();
    await vi.waitFor(() => expect(within(sidebar()).getByRole('button', { name: /^Night/ })).toBeTruthy());
    expect(api.calls).toContain('listQueues');
    const main = within(sidebar()).getByRole('button', { name: /^Main queue/ });
    const night = within(sidebar()).getByRole('button', { name: /^Night/ });
    // Running state and what is left to do (completed downloads are not counted).
    expect(main.textContent).toContain('Running');
    expect(main.textContent).toContain('2');
    expect(night.textContent).toContain('Stopped');
    expect(night.textContent).toContain('1');

    await fireEvent.click(night);
    expect(night.getAttribute('aria-pressed')).toBe('true');
    expect(document.getElementById('dl-row-3')).toBeTruthy();
    expect(document.getElementById('dl-row-1')).toBeNull();
    await fireEvent.click(night);
    expect(document.getElementById('dl-row-1')).toBeTruthy();

    // A queue event updates the sidebar.
    api.emit([{ kind: 'queue_updated', queue: makeQueue({ id: 2, name: 'Night', running: true, downloadIds: [3] }) }]);
    await vi.waitFor(() => expect(within(sidebar()).getByRole('button', { name: /^Night/ }).textContent).toContain('Running'));
  });

  it('moves downloads to another queue from the context menu', async () => {
    const api = setup();
    await vi.waitFor(() => expect(within(sidebar()).getByRole('button', { name: /^Night/ })).toBeTruthy());
    await fireEvent.click(rowOf(1));
    await fireEvent.contextMenu(rowOf(1));
    await fireEvent.click(await screen.findByRole('menuitem', { name: /Move to queue/ }));
    const dialog = await screen.findByRole('dialog', { name: 'Move to queue' });
    expect(within(dialog).getByRole('radio', { name: /Night/ })).toHaveProperty('checked', true);
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Move' }));
    await vi.waitFor(() => expect(api.args.get('moveDownloads')).toEqual([[1], 2, -1]));
    await vi.waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    // The queue's name is bidi-isolated in the sentence.
    await vi.waitFor(() =>
      expect(screen.getAllByRole('status').some((s) => (s.textContent ?? '').includes('Moved to ⁨Night⁩'))).toBe(true),
    );
    // The download is counted in its new queue at once.
    expect(within(sidebar()).getByRole('button', { name: /^Night/ }).textContent).toContain('2');
  });

  it('opens the queue manager from the sidebar', async () => {
    setup();
    await fireEvent.click(await screen.findByRole('button', { name: 'Manage queues' }));
    const dialog = await screen.findByRole('dialog', { name: 'Queues' });
    expect(within(dialog).getByRole('heading', { name: 'Main queue' })).toBeTruthy();
    // The order lists what is left to do.
    expect(within(within(dialog).getByRole('list', { name: 'Order' })).getAllByText(/\.(iso|zip)$/).map((e) => e.textContent)).toEqual([
      'one.iso',
      'private.zip',
    ]);
  });

  it('counts down a post-queue action and cancels it', async () => {
    const api = setup();
    await vi.waitFor(() => expect(api.calls).toContain('listQueues'));
    api.emit([{ kind: 'post_action', pending: { queueId: 2, action: 'shutdown', deadline: Date.now() + 60_000, cancelled: false } }]);
    const banner = await screen.findByRole('region', { name: /Shutting down in (60|59) s/ });
    expect(banner.textContent).toContain('Night finished its downloads');
    const cancel = within(banner).getByRole('button', { name: 'Cancel' });
    expect(document.activeElement).toBe(cancel);
    await fireEvent.click(cancel);
    await vi.waitFor(() => expect(api.args.get('cancelPostAction')).toEqual([2]));
    await vi.waitFor(() => expect(screen.queryByRole('region', { name: /Shutting down/ })).toBeNull());

    // The daemon's own cancellation (another window, the tray) removes it too.
    api.emit([{ kind: 'post_action', pending: { queueId: 1, action: 'sleep', deadline: Date.now() + 30_000, cancelled: false } }]);
    await screen.findByRole('region', { name: /Going to sleep in/ });
    api.emit([{ kind: 'post_action', pending: { queueId: 1, action: 'sleep', deadline: 0, cancelled: true } }]);
    await vi.waitFor(() => expect(screen.queryByRole('region', { name: /Going to sleep/ })).toBeNull());
  });

  it('keeps the countdown in reach of the keyboard while a dialog is open', async () => {
    const api = setup();
    await vi.waitFor(() => expect(api.calls).toContain('listQueues'));
    await fireEvent.keyDown(window, { key: ',', ctrlKey: true });
    const dialog = await screen.findByRole('dialog', { name: 'Settings' });
    await vi.waitFor(() => expect(dialog.contains(document.activeElement)).toBe(true));

    api.emit([{ kind: 'post_action', pending: { queueId: 2, action: 'shutdown', deadline: Date.now() + 60_000, cancelled: false } }]);
    const banner = await screen.findByRole('region', { name: /Shutting down in/ });
    const cancel = within(banner).getByRole('button', { name: 'Cancel' });
    // It takes the focus when it appears, and the dialog lets it keep it.
    await vi.waitFor(() => expect(document.activeElement).toBe(cancel));
    // Tab goes on into the dialog, Shift+Tab from its first control back to the countdown.
    await fireEvent.keyDown(cancel, { key: 'Tab' });
    expect(dialog.contains(document.activeElement)).toBe(true);
    const first = document.activeElement as HTMLElement;
    await fireEvent.keyDown(first, { key: 'Tab', shiftKey: true });
    expect(document.activeElement).toBe(cancel);
    // From the dialog's last control, Tab reaches it too.
    const controls = [...dialog.querySelectorAll<HTMLElement>('button:not([disabled]), input:not([disabled]), select:not([disabled])')];
    const last = controls[controls.length - 1]!;
    last.focus();
    await fireEvent.keyDown(last, { key: 'Tab' });
    expect(document.activeElement).toBe(cancel);

    // Escape on the countdown cancels it, not the dialog.
    await fireEvent.keyDown(cancel, { key: 'Escape' });
    await vi.waitFor(() => expect(api.args.get('cancelPostAction')).toEqual([2]));
    await vi.waitFor(() => expect(screen.queryByRole('region', { name: /Shutting down/ })).toBeNull());
    expect(screen.getByRole('dialog', { name: 'Settings' })).toBe(dialog);
    // The keyboard goes back into the dialog.
    await vi.waitFor(() => expect(dialog.contains(document.activeElement)).toBe(true));
  });

  it('leaves the focus on the countdown when a confirmation dialog opens by itself', async () => {
    const api = setup();
    await vi.waitFor(() => expect(api.calls).toContain('listQueues'));
    api.emit([{ kind: 'post_action', pending: { queueId: 2, action: 'sleep', deadline: Date.now() + 60_000, cancelled: false } }]);
    const banner = await screen.findByRole('region', { name: /Going to sleep in/ });
    const cancel = within(banner).getByRole('button', { name: 'Cancel' });
    await vi.waitFor(() => expect(document.activeElement).toBe(cancel));
    const waiting = makeDownload({ id: 9, filename: 'handoff.zip', state: 'awaiting_confirmation', source: 'browser' });
    api.items.set(9, waiting);
    api.emit([{ kind: 'updated', download: waiting }]);
    await screen.findByRole('dialog', { name: 'Confirm download' });
    expect(document.activeElement).toBe(cancel);
  });

  it('drops a countdown that already ended when it is cancelled', async () => {
    const api = setup();
    await vi.waitFor(() => expect(api.calls).toContain('listQueues'));
    api.cancelPostAction = async () => {
      throw new Error('failed_precondition: queue 2 has no post-queue action pending');
    };
    api.emit([{ kind: 'post_action', pending: { queueId: 2, action: 'sleep', deadline: Date.now() + 60_000, cancelled: false } }]);
    const banner = await screen.findByRole('region', { name: /Going to sleep in/ });
    await fireEvent.click(within(banner).getByRole('button', { name: 'Cancel' }));
    await vi.waitFor(() => expect(screen.queryByRole('region', { name: /Going to sleep/ })).toBeNull());
    await vi.waitFor(() =>
      expect(screen.getAllByRole('status').some((s) => (s.textContent ?? '').includes('nothing left to cancel'))).toBe(true),
    );
  });

  it('confirms "Download later" as the queue hint says', async () => {
    const api = setup();
    await vi.waitFor(() => expect(api.calls).toContain('listQueues'));
    const waiting = makeDownload({ id: 9, filename: 'handoff.zip', state: 'awaiting_confirmation', source: 'browser' });
    const daytime = makeQueue({ id: 3, name: 'Daytime', running: true });
    api.items.set(9, waiting);
    api.queues.set(3, daytime);
    api.emit([{ kind: 'updated', download: waiting }, { kind: 'queue_updated', queue: daytime }]);
    const dialog = await screen.findByRole('dialog', { name: 'Confirm download' });
    await new Promise((r) => setTimeout(r, 650)); // past the guard of a dialog that opened by itself
    await vi.waitFor(() => expect(within(dialog).getByRole('combobox', { name: 'Queue' })).toBeTruthy());
    await chooseOption(within(dialog).getByRole('combobox', { name: 'Queue' }), 3);
    expect(within(dialog).getByText(/Download later keeps it paused/)).toBeTruthy();
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Download later' }));
    // Paused in Daytime, which is running: moved there first, then confirmed without a queue.
    await vi.waitFor(() => expect(api.args.get('confirm')).toEqual([9, { start: false, dir: '', filename: '' }]));
    expect(api.args.get('moveDownloads')).toEqual([[9], 3, -1]);
    expect(api.calls.indexOf('moveDownloads')).toBeLessThan(api.calls.indexOf('confirm'));
    expect(api.items.get(9)).toMatchObject({ state: 'paused', queueId: 3 });
  });

  it('offers a site login for a server that asked for credentials', async () => {
    setup();
    await vi.waitFor(() => expect(rowOf(4)).toBeTruthy());
    await fireEvent.click(rowOf(4));
    expect(screen.getByText(/Add a site login for it/)).toBeTruthy();
    await fireEvent.click(screen.getByRole('button', { name: /Add site login/ }));
    const dialog = await screen.findByRole('dialog', { name: 'Settings' });
    await vi.waitFor(() => expect((within(dialog).getByLabelText('Address') as HTMLInputElement).value).toBe('https://private.example.net/*'));
    expect(within(dialog).getByRole('tab', { name: 'Network' }).getAttribute('aria-selected')).toBe('true');
  });
});
