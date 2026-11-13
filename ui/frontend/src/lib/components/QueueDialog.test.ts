import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { tick } from 'svelte';
import { describe, expect, it, vi } from 'vitest';
import { EMPTY_SCHEDULE, type Queue, type QueueSpec } from '../api';
import { chooseOption, listboxOf, makeDownload, makeQueue, optionLabels } from '../testing';
import MoveToQueueDialog from './MoveToQueueDialog.svelte';
import QueueDialog from './QueueDialog.svelte';

const downloads = new Map(
  [
    makeDownload({ id: 10, filename: 'first.iso', state: 'queued' }),
    makeDownload({ id: 11, filename: 'second.iso', state: 'queued' }),
    makeDownload({ id: 12, filename: 'done.iso', state: 'completed' }),
    makeDownload({ id: 13, filename: 'third.iso', state: 'paused' }),
  ].map((d) => [d.id, d]),
);

function setup(opts: { queues?: Queue[]; caps?: { hibernate: boolean; wakeTimers: boolean }; initial?: number | null } = {}) {
  let queues: Queue[] = opts.queues ?? [
    makeQueue({ id: 1, downloadIds: [10, 11, 12, 13] }),
    makeQueue({ id: 2, name: 'Night', postAction: 'sleep' }),
  ];
  const put = (q: Queue) => {
    queues = queues.some((x) => x.id === q.id) ? queues.map((x) => (x.id === q.id ? q : x)) : [...queues, q];
    view.rerender({ queues });
    return q;
  };
  const oncreate = vi.fn(async (spec: QueueSpec) => put(makeQueue({ ...spec, id: 3 })));
  const onupdate = vi.fn(async (id: number, spec: QueueSpec) => put({ ...(queues.find((q) => q.id === id) as Queue), ...spec }));
  const ondelete = vi.fn(async (id: number) => {
    queues = queues.filter((q) => q.id !== id);
    view.rerender({ queues });
  });
  const onstart = vi.fn(async (id: number) => put({ ...(queues.find((q) => q.id === id) as Queue), running: true }));
  const onstop = vi.fn(async (id: number) => put({ ...(queues.find((q) => q.id === id) as Queue), running: false }));
  const onmove = vi.fn(async (ids: readonly number[], queueId: number, position: number) => {
    const q = queues.find((x) => x.id === queueId) as Queue;
    const others = q.downloadIds.filter((id) => !ids.includes(id));
    others.splice(position < 0 ? others.length : position, 0, ...ids);
    return put({ ...q, downloadIds: others });
  });
  const onclose = vi.fn();
  const view = render(QueueDialog, {
    props: {
      queues,
      initial: opts.initial ?? null,
      capabilities: opts.caps ?? { hibernate: true, wakeTimers: true },
      downloadOf: (id: number) => downloads.get(id),
      oncreate,
      onupdate,
      ondelete,
      onstart,
      onstop,
      onmove,
      onclose,
    },
  });
  return { oncreate, onupdate, ondelete, onstart, onstop, onmove, onclose, queues: () => queues };
}

const nameField = () => screen.getByLabelText('Name') as HTMLInputElement;
const orderRows = () => within(screen.getByRole('list', { name: 'Order' })).getAllByRole('listitem').filter((li) => li.textContent?.trim());

describe('QueueDialog', () => {
  it('creates a queue with a schedule and an action', async () => {
    const { oncreate } = setup();
    await fireEvent.click(screen.getByRole('button', { name: 'New queue' }));
    expect(document.activeElement).toBe(nameField());
    await fireEvent.input(nameField(), { target: { value: 'Overnight' } });
    await fireEvent.input(screen.getByLabelText('Simultaneous downloads'), { target: { value: '2' } });
    await fireEvent.input(screen.getByLabelText('Speed limit of the queue'), { target: { value: '512' } });
    await chooseOption(screen.getByLabelText('Queue speed limit unit'), 'KB/s');
    await chooseOption(screen.getByLabelText('When the queue is done'), 'shutdown');
    await fireEvent.click(screen.getByRole('switch', { name: /on a schedule/ }));
    await fireEvent.input(screen.getByLabelText('Start at'), { target: { value: '23:30' } });
    await fireEvent.click(screen.getByRole('checkbox', { name: 'Stop at' }));
    await fireEvent.input(screen.getByLabelText('Stop time'), { target: { value: '06:45' } });
    // Every day is proposed: keep Monday to Friday.
    await fireEvent.click(screen.getByRole('button', { name: 'Saturday' }));
    await fireEvent.click(screen.getByRole('button', { name: 'Sunday' }));
    expect(screen.getByRole('button', { name: 'Sunday' }).getAttribute('aria-pressed')).toBe('false');
    await fireEvent.input(screen.getByLabelText(/Retry unfinished/), { target: { value: '30' } });
    await fireEvent.click(screen.getByRole('switch', { name: /Wake the computer/ }));
    await fireEvent.click(screen.getByRole('button', { name: 'Create queue' }));
    await waitFor(() => expect(oncreate).toHaveBeenCalled());
    expect(oncreate).toHaveBeenCalledWith({
      name: 'Overnight',
      maxConcurrent: 2,
      speedLimit: 512 * 1000,
      postAction: 'shutdown',
      schedule: { enabled: true, startMinute: 1410, stopEnabled: true, stopMinute: 405, weekdays: 0x1f, onceDate: '', wakeComputer: true, repeatMinutes: 30 },
    });
    // The new queue is selected and can be started.
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Overnight' })).toBeTruthy());
    expect(screen.getByRole('button', { name: 'Start' })).toBeTruthy();
  });

  it('shows what needs attention and saves nothing', async () => {
    const { onupdate } = setup({ initial: 2 });
    await fireEvent.input(nameField(), { target: { value: 'main queue' } });
    await fireEvent.click(screen.getByRole('switch', { name: /on a schedule/ }));
    await fireEvent.click(screen.getByRole('radio', { name: 'Once' }));
    await fireEvent.input(screen.getByLabelText('Date'), { target: { value: '' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Save queue' }));
    await waitFor(() => expect(screen.getByText('Another queue has this name')).toBeTruthy());
    expect(screen.getByText('Choose a date')).toBeTruthy();
    expect(document.activeElement).toBe(nameField());
    expect(onupdate).not.toHaveBeenCalled();
  });

  it('offers only what the system can do', async () => {
    setup({ caps: { hibernate: false, wakeTimers: false }, initial: 2 });
    const options = optionLabels(screen.getByLabelText('When the queue is done'));
    expect(options).toEqual(['Keep running', 'Quit Downex', 'Put the computer to sleep', 'Shut down the computer']);
    await fireEvent.click(screen.getByRole('switch', { name: /on a schedule/ }));
    expect(screen.getByLabelText('Start at')).toBeTruthy();
    expect(screen.queryByRole('switch', { name: /Wake the computer/ })).toBeNull();
  });

  it('starts and stops a queue, and deletes it after asking', async () => {
    const { onstart, onstop, ondelete } = setup({ initial: 2 });
    expect(screen.getByText('Stopped')).toBeTruthy();
    await fireEvent.click(screen.getByRole('button', { name: 'Start' }));
    await waitFor(() => expect(onstart).toHaveBeenCalledWith(2));
    await waitFor(() => expect(screen.getByRole('button', { name: 'Stop' })).toBeTruthy());
    await fireEvent.click(screen.getByRole('button', { name: 'Stop' }));
    await waitFor(() => expect(onstop).toHaveBeenCalledWith(2));

    await fireEvent.click(screen.getByRole('button', { name: 'Delete queue…' }));
    expect(screen.getByRole('alert').textContent).toContain('Its downloads move to the end of the main queue');
    expect(ondelete).not.toHaveBeenCalled();
    await fireEvent.click(screen.getByRole('button', { name: 'Delete queue now' }));
    await waitFor(() => expect(ondelete).toHaveBeenCalledWith(2));
    // Back on the main queue, which cannot be deleted.
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Main queue' })).toBeTruthy());
    expect(screen.queryByRole('button', { name: /Delete queue/ })).toBeNull();
  });

  it('reorders the queue with the arrows and by dragging', async () => {
    const { onmove } = setup({ initial: 1 });
    // Completed downloads are not part of the order shown.
    expect(orderRows().map((li) => li.querySelector('bdi')?.textContent)).toEqual(['first.iso', 'second.iso', 'third.iso']);
    await fireEvent.click(screen.getByRole('button', { name: 'Move second.iso up' }));
    await waitFor(() => expect(onmove).toHaveBeenLastCalledWith([11], 1, 0));
    await waitFor(() => expect(orderRows()[0]?.textContent).toContain('second.iso'));
    // At the top its Up arrow is off: the keyboard stays on the row, on its other arrow.
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Move second.iso down' }));

    // Down from the first row: after first.iso, which is followed by the completed one.
    await fireEvent.click(screen.getByRole('button', { name: 'Move second.iso down' }));
    await waitFor(() => expect(onmove).toHaveBeenLastCalledWith([11], 1, 1));

    // Drag third.iso onto the top half of the first row.
    const rows = orderRows();
    const types = ['application/x-downex-download'];
    const data = { types, getData: () => '13', setData: vi.fn(), dropEffect: 'none', effectAllowed: 'all' };
    await fireEvent.dragStart(rows[2]!, { dataTransfer: data });
    expect(data.setData).toHaveBeenCalledWith('application/x-downex-download', '13');
    await fireEvent.dragOver(rows[0]!, { dataTransfer: data, clientY: 0 });
    await fireEvent.drop(rows[0]!, { dataTransfer: data, clientY: 0 });
    await waitFor(() => expect(onmove).toHaveBeenLastCalledWith([13], 1, 0));
  });

  it('drops a row below another by where the pointer is, and onto itself or its own place as nothing', async () => {
    const { onmove } = setup({ initial: 1 });
    const types = ['application/x-downex-download'];
    // Rows of a real height (jsdom lays nothing out): the lower half starts at y 16.
    const rows = orderRows();
    rows.forEach((row, i) => {
      row.getBoundingClientRect = () => ({ top: i * 32, bottom: i * 32 + 32, height: 32, left: 0, right: 400, width: 400, x: 0, y: i * 32, toJSON: () => ({}) });
    });
    const drag = async (from: number, to: number, clientY: number) => {
      const id = String([10, 11, 13][from]);
      const data = { types, getData: () => id, setData: vi.fn(), dropEffect: 'none', effectAllowed: 'all' };
      await fireEvent.dragStart(rows[from]!, { dataTransfer: data });
      const over = new MouseEvent('dragover', { bubbles: true, cancelable: true, clientY });
      Object.defineProperty(over, 'dataTransfer', { value: data });
      await fireEvent(rows[to]!, over);
      const marked = rows.filter((r) => r.classList.contains('drop-before')).length;
      const drop = new MouseEvent('drop', { bubbles: true, cancelable: true, clientY });
      Object.defineProperty(drop, 'dataTransfer', { value: data });
      await fireEvent(rows[to]!, drop);
      await fireEvent.dragEnd(rows[from]!, { dataTransfer: data });
      return marked;
    };

    // third.iso onto the lower half of its own row, and of the row above it: it stays.
    expect(await drag(2, 2, 2 * 32 + 30)).toBe(0);
    expect(await drag(2, 1, 32 + 30)).toBe(0);
    // first.iso onto the upper half of its own row and of the next one.
    expect(await drag(0, 0, 2)).toBe(0);
    expect(await drag(0, 1, 32 + 2)).toBe(0);
    await Promise.resolve();
    expect(onmove).not.toHaveBeenCalled();

    // first.iso onto the lower half of second.iso: after it (and the completed one), before third.iso.
    expect(await drag(0, 1, 32 + 30)).toBe(1);
    await waitFor(() => expect(onmove).toHaveBeenLastCalledWith([10], 1, 2));
    expect(onmove).toHaveBeenCalledTimes(1);
  });

  it('keeps the edited fields when the queue changes elsewhere, and reports refusals', async () => {
    const { onupdate } = setup({ initial: 2 });
    await fireEvent.input(nameField(), { target: { value: 'Nightly' } });
    onupdate.mockRejectedValueOnce(new Error('already_exists: a queue named "Nightly" exists'));
    await fireEvent.click(screen.getByRole('button', { name: 'Save queue' }));
    await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('a queue named "Nightly" exists'));
    expect(nameField().value).toBe('Nightly');
  });

  it('lists the schedule of a queue', () => {
    setup({
      initial: 2,
      queues: [
        makeQueue({ id: 1 }),
        makeQueue({ id: 2, name: 'Night', schedule: { ...EMPTY_SCHEDULE, enabled: true, startMinute: 60, weekdays: 0x7f } }),
      ],
    });
    expect(screen.getByText(/Every day at 01:00/)).toBeTruthy();
  });
});

describe('QueueDialog: the restyled editor (DESIGN-SYSTEM.md §4.10)', () => {
  it('lists the queues like the sidebar: the chosen one pressed, with a note and what waits in each', () => {
    setup({
      initial: 2,
      queues: [
        makeQueue({ id: 1, running: true, downloadIds: [10, 11, 12, 13] }),
        makeQueue({ id: 2, name: 'Night', schedule: { ...EMPTY_SCHEDULE, enabled: true, startMinute: 60, weekdays: 0x7f } }),
      ],
    });
    const list = screen.getByRole('navigation', { name: 'Queues' });
    const [main, night] = within(list).getAllByRole('button').filter((b) => b.hasAttribute('aria-pressed'));
    expect(main?.getAttribute('aria-pressed')).toBe('false');
    expect(night?.getAttribute('aria-pressed')).toBe('true');
    // The main queue runs; three of its downloads wait (the completed one is not counted).
    expect(main?.textContent?.replace(/\s+/g, ' ').trim()).toBe('Main queue running 3');
    // Night is stopped, and its schedule starts it at 01:00.
    expect(night?.textContent?.replace(/\s+/g, ' ').trim()).toBe('Night 01:00 stopped 0');
    expect(screen.getByRole('heading', { name: 'Night' })).toBeTruthy();
  });

  it('chooses the speed unit and the action when done from the keyboard (custom selects)', async () => {
    const { onupdate } = setup({ initial: 2 });
    const after = screen.getByLabelText('When the queue is done');
    expect(after.getAttribute('role')).toBe('combobox');
    // Night's stored action is shown.
    expect(after.getAttribute('data-value')).toBe('sleep');
    after.focus();
    await fireEvent.keyDown(after, { key: 'ArrowDown' });
    await tick();
    expect(after.getAttribute('aria-expanded')).toBe('true');
    expect(listboxOf(after).hidden).toBe(false);
    await fireEvent.keyDown(after, { key: 'End' });
    await fireEvent.keyDown(after, { key: 'Enter' });
    await tick();
    expect(after.getAttribute('aria-expanded')).toBe('false');
    expect(after.getAttribute('data-value')).toBe('shutdown');
    expect(after.textContent).toContain('Shut down the computer');

    const unit = screen.getByLabelText('Queue speed limit unit');
    unit.focus();
    // Type-ahead chooses without opening.
    await fireEvent.keyDown(unit, { key: 'k' });
    await tick();
    expect(unit.getAttribute('data-value')).toBe('KB/s');
    await fireEvent.input(screen.getByLabelText('Speed limit of the queue'), { target: { value: '300' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Save queue' }));
    await waitFor(() => expect(onupdate).toHaveBeenCalled());
    expect(onupdate.mock.calls[0]?.[1]).toMatchObject({ postAction: 'shutdown', speedLimit: 300 * 1000 });
  });

  it('closes only the list of an open select on Escape, not the dialog', async () => {
    const { onclose } = setup({ initial: 2 });
    const after = screen.getByLabelText('When the queue is done');
    after.focus();
    await fireEvent.keyDown(after, { key: 'Enter' });
    await waitFor(() => expect(after.getAttribute('aria-expanded')).toBe('true'));
    await fireEvent.keyDown(after, { key: 'Escape' });
    await waitFor(() => expect(after.getAttribute('aria-expanded')).toBe('false'));
    expect(after.getAttribute('data-value')).toBe('sleep');
    expect(onclose).not.toHaveBeenCalled();
    expect(screen.getByRole('dialog', { name: 'Queues' })).toBeTruthy();
  });

  it('switches between days of the week and one date, with weekday chips that are pressed or not', async () => {
    const { onupdate } = setup({ initial: 2 });
    // Off: only the switch, which says what that means.
    expect(screen.queryByLabelText('Start at')).toBeNull();
    expect(screen.getByRole('switch', { name: /on a schedule/ }).getAttribute('aria-describedby')).toBeTruthy();
    await fireEvent.click(screen.getByRole('switch', { name: /on a schedule/ }));
    const repeat = screen.getByRole('group', { name: 'Repeat' });
    expect(within(repeat).getAllByRole('radio').map((r) => (r as HTMLInputElement).checked)).toEqual([true, false]);
    const days = screen.getByRole('group', { name: 'Weekdays' });
    const chips = within(days).getAllByRole('button');
    expect(chips.map((c) => c.getAttribute('aria-label'))).toEqual(['Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday', 'Sunday']);
    expect(chips.every((c) => c.getAttribute('aria-pressed') === 'true')).toBe(true);
    // No day: nothing to save.
    for (const c of chips) await fireEvent.click(c);
    expect(chips.every((c) => c.getAttribute('aria-pressed') === 'false')).toBe(true);
    await fireEvent.click(screen.getByRole('button', { name: 'Save queue' }));
    await waitFor(() => expect(screen.getByText('Choose at least one day')).toBeTruthy());
    expect(onupdate).not.toHaveBeenCalled();
    await fireEvent.click(within(days).getByRole('button', { name: 'Wednesday' }));

    // Once: a date instead of the chips.
    await fireEvent.click(within(repeat).getByRole('radio', { name: 'Once' }));
    expect(screen.queryByRole('group', { name: 'Weekdays' })).toBeNull();
    await fireEvent.input(screen.getByLabelText('Date'), { target: { value: '2026-11-02' } });
    await fireEvent.click(within(repeat).getByRole('radio', { name: 'Every week' }));
    expect(within(screen.getByRole('group', { name: 'Weekdays' })).getByRole('button', { name: 'Wednesday' }).getAttribute('aria-pressed')).toBe('true');
    await fireEvent.click(screen.getByRole('button', { name: 'Save queue' }));
    await waitFor(() => expect(onupdate).toHaveBeenCalled());
    expect(onupdate.mock.calls[0]?.[1].schedule).toMatchObject({ enabled: true, weekdays: 1 << 2 });
  });

  it('lists the order with each download\'s state', () => {
    setup({ initial: 1 });
    const rows = orderRows();
    expect(rows.map((li) => li.querySelector('.dstate')?.textContent?.trim())).toEqual(['Queued', 'Queued', 'Paused']);
    expect(rows.every((li) => li.querySelector('svg[data-kind]') !== null)).toBe(true);
    // a name cut in a narrow column is whole in its tooltip
    expect(rows.map((li) => li.querySelector<HTMLElement>('.dname')?.title)).toEqual(rows.map((li) => li.querySelector('.dname')?.textContent));
  });
});

describe('QueueDialog: sizing (DESIGN-SYSTEM.md §3.9)', () => {
  it('is 72rem wide and at least 48rem high, growing to the room the window has (scripts/measure-dialogs.mjs checks its one scroll region)', () => {
    setup();
    const dialog = screen.getByRole('dialog', { name: 'Queues' });
    // 72rem: a wide editor puts the schedule beside the settings and the order (§4.10)
    expect(dialog.style.inlineSize).toMatch(/^min\(72rem, .*100vw/);
    // never lower than 48rem (switching queues does not shrink it), at most the window's room (CSS)
    expect(dialog.style.minBlockSize).toMatch(/^min\(48rem, .*100vh/);
    expect(dialog.style.minBlockSize).toContain('--modal-top');
    expect(dialog.style.blockSize).toBe('');
  });
});

describe('MoveToQueueDialog', () => {
  const queues = [makeQueue({ id: 1, running: true }), makeQueue({ id: 2, name: 'Night' }), makeQueue({ id: 3, name: 'Weekend' })];

  function open(current: number | null = 1) {
    const onmove = vi.fn(async () => {});
    const onclose = vi.fn();
    render(MoveToQueueDialog, { props: { names: ['one.iso'], queues, current, onmove, onclose } });
    return { onmove, onclose };
  }

  it('names each radio after its queue, proposes another queue and says where the downloads are', async () => {
    const { onmove, onclose } = open(1);
    const dialog = screen.getByRole('dialog', { name: 'Move to queue' });
    const radios = within(dialog).getAllByRole('radio') as HTMLInputElement[];
    // Each radio's name is exactly its queue's name.
    expect(['Main queue', 'Night', 'Weekend'].map((name) => within(dialog).getByRole('radio', { name }))).toEqual(radios);
    expect(radios.map((r) => r.checked)).toEqual([false, true, false]);
    expect(within(dialog).getByRole('radio', { name: 'Main queue' }).getAttribute('aria-describedby')).toBeTruthy();
    expect(dialog.textContent).toContain('(now)');
    // The chosen row is marked (not by its tint alone: a check too).
    const night = radios[1]!.closest('label');
    expect(night?.classList.contains('on')).toBe(true);
    expect(night?.querySelector('.tick svg')).toBeTruthy();
    await fireEvent.click(within(dialog).getByRole('radio', { name: 'Weekend' }));
    expect(radios[2]!.checked).toBe(true);
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Move' }));
    await waitFor(() => expect(onmove).toHaveBeenCalledWith(3));
    await waitFor(() => expect(onclose).toHaveBeenCalled());
  });

  it('cannot move the downloads to the queue they are in, and reports a refusal', async () => {
    const { onmove, onclose } = open(1);
    onmove.mockRejectedValueOnce(new Error('not_found: queue 2 not found'));
    await fireEvent.click(screen.getByRole('radio', { name: 'Main queue' }));
    expect((screen.getByRole('button', { name: 'Move' }) as HTMLButtonElement).disabled).toBe(true);
    await fireEvent.click(screen.getByRole('radio', { name: 'Night' }));
    await fireEvent.click(screen.getByRole('button', { name: 'Move' }));
    await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('queue 2 not found'));
    expect(onclose).not.toHaveBeenCalled();
  });
});
