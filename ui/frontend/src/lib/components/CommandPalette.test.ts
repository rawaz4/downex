import { fireEvent, render, screen, within } from '@testing-library/svelte';
import { tick } from 'svelte';
import { describe, expect, it, vi } from 'vitest';
import CommandPalette, {
  MAX_RUNNING,
  filterCommands,
  fuzzyScore,
  highlightParts,
  paletteEntries,
  type Command,
  type PaletteDownload,
} from './CommandPalette.svelte';
import { MiB, makeDownload } from '../testing';
import type { Download } from '../api';

const cmd = (id: string, label: string, extra: Partial<Command> = {}): Command => ({
  id,
  label,
  group: 'Downloads',
  run: vi.fn(),
  ...extra,
});

describe('fuzzyScore', () => {
  it('matches subsequences, preferring word starts and runs', () => {
    expect(fuzzyScore('', 'anything')).toBe(0);
    expect(fuzzyScore('xyz', 'Pause all')).toBe(-1);
    expect(fuzzyScore('pa', 'Pause all')).toBeGreaterThan(fuzzyScore('pa', 'Open folder at path'));
    expect(fuzzyScore('ra', 'Resume all')).toBeGreaterThan(0);
  });

  it('ranks the best match first and keeps the order when the query is empty', () => {
    const list = [cmd('a', 'Open settings'), cmd('b', 'Pause all'), cmd('c', 'Pause selected')];
    expect(filterCommands(list, '').map((c) => c.id)).toEqual(['a', 'b', 'c']);
    expect(filterCommands(list, 'pause sel').map((c) => c.id)).toEqual(['c']);
    expect(filterCommands(list, 'set')[0]?.id).toBe('a');
  });
});

const item = (d: Download, extra: Partial<PaletteDownload> = {}): PaletteDownload => ({
  id: d.id,
  name: d.filename,
  host: 'files.example.com',
  kind: 'archives',
  download: d,
  ...extra,
});

describe('paletteEntries', () => {
  const running = (id: number, filename: string) =>
    item(makeDownload({ id, filename, state: 'downloading', size: 100 * MiB, received: 40 * MiB, speedBps: 5 * MiB }));

  it('lists the running downloads first with an empty query, then the commands in their order', () => {
    const list = [cmd('a', 'Open settings', { group: 'View' }), cmd('b', 'Pause all')];
    const downloads = [
      running(1, 'one.iso'),
      item(makeDownload({ id: 2, filename: 'done.zip', state: 'completed' })),
      ...Array.from({ length: 6 }, (_, i) => running(10 + i, `r${i}.iso`)),
    ];
    const rows = paletteEntries(list, downloads, '');
    const shown = rows.filter((r) => r.type === 'download');
    expect(shown).toHaveLength(MAX_RUNNING);
    expect(shown.every((r) => r.group === 'Running downloads')).toBe(true);
    expect(shown.map((r) => r.type === 'download' && r.item.id)).not.toContain(2);
    expect(rows.slice(MAX_RUNNING).map((r) => r.key)).toEqual(['c-a', 'c-b']);
  });

  it('ranks commands and downloads together, commands first on a tie', () => {
    const pause = cmd('pause', 'Pause all');
    const downloads = [item(makeDownload({ id: 7, filename: 'Pause all', state: 'completed' }), { host: '' })];
    const rows = paletteEntries([pause], downloads, 'pause all');
    expect(rows.map((r) => r.key)).toEqual(['c-pause', 'd-7']);
    expect(rows[1]?.group).toBe('Downloads');
    // a download that matches better comes first
    const better = paletteEntries([cmd('x', 'Show the archive list')], [item(makeDownload({ id: 3, filename: 'keynote.mp4' }))], 'keynote');
    expect(better.map((r) => r.key)).toEqual(['d-3']);
    // a group is never split: its entries stay together, groups ordered by their best match
    const go = cmd('go', 'Status: Paused', { group: 'Go to' });
    const pauseSel = cmd('pause-sel', 'Pause selected', { group: 'Commands' });
    const when = cmd('when', 'When done…', { group: 'Commands', description: 'Keep running, sleep, shut down, or quit Downex', keywords: 'pause' });
    const grouped = paletteEntries([pauseSel, go, when], [], 'pause');
    expect(grouped.map((r) => r.group)).toEqual(['Commands', 'Commands', 'Go to']);
    expect(grouped[0]?.key).toBe('c-pause-sel');
    // downloads are found by their server too
    expect(paletteEntries([], [item(makeDownload({ id: 4 }), { host: 'mirror.example.org' })], 'mirror').map((r) => r.key)).toEqual(['d-4']);
  });

  it('never puts an unavailable command first among equal matches', () => {
    const sel = cmd('pause-sel', 'Pause selected', { disabled: true, disabledReason: 'nothing selected' });
    const all = cmd('pause-all', 'Pause all');
    expect(paletteEntries([sel, all], [], 'pause').map((r) => r.key)).toEqual(['c-pause-all', 'c-pause-sel']);
    expect(filterCommands([sel, all], 'pause').map((c) => c.id)).toEqual(['pause-all', 'pause-sel']);
    // available, it keeps its place
    const on = cmd('pause-sel', 'Pause selected');
    expect(paletteEntries([on, all], [], 'pause').map((r) => r.key)).toEqual(['c-pause-sel', 'c-pause-all']);
  });

  it('finds commands by whole words of their description only', () => {
    const when = cmd('when', 'When done…', { description: 'Keep running, sleep, shut down, or quit Downex' });
    const remove = cmd('remove', 'Remove selected…', { description: 'Asks first; the files stay unless you choose to delete them' });
    expect(filterCommands([when, remove], 'shut').map((c) => c.id)).toEqual(['when']);
    expect(filterCommands([when, remove], 'sleep shut').map((c) => c.id)).toEqual(['when']);
    // a loose subsequence of a sentence is no match
    expect(filterCommands([when, remove], 'key')).toEqual([]);
  });

  it('highlights the matched letters in runs', () => {
    expect(highlightParts('key', 'conference-keynote.mp4')).toEqual([
      { text: 'conference-', hit: false },
      { text: 'key', hit: true },
      { text: 'note.mp4', hit: false },
    ]);
    expect(highlightParts('zz', 'abc')).toEqual([{ text: 'abc', hit: false }]);
    expect(highlightParts('', 'abc')).toEqual([{ text: 'abc', hit: false }]);
  });
});

describe('CommandPalette', () => {
  it('filters as you type and runs the current command with Enter', async () => {
    const pause = cmd('pause', 'Pause all');
    const settings = cmd('settings', 'Open settings', { group: 'View', keywords: 'preferences' });
    const onclose = vi.fn();
    render(CommandPalette, { props: { commands: [pause, settings], onclose } });
    const input = screen.getByRole('combobox', { name: 'Search commands' });
    expect(screen.getByRole('dialog', { name: 'Command palette' })).toBeTruthy();
    expect(screen.getAllByRole('option')).toHaveLength(2);

    await fireEvent.input(input, { target: { value: 'prefs' } });
    const options = screen.getAllByRole('option');
    expect(options.map((o) => o.textContent?.trim())).toEqual(['Open settings']);
    expect(input.getAttribute('aria-activedescendant')).toBe(options[0]?.id);

    await fireEvent.keyDown(input, { key: 'Enter' });
    expect(onclose).toHaveBeenCalled();
    await tick();
    expect(settings.run).toHaveBeenCalled();
    expect(pause.run).not.toHaveBeenCalled();
  });

  it('moves with the arrows, wraps, and does not run disabled commands', async () => {
    const a = cmd('a', 'Resume all', { disabled: true });
    const b = cmd('b', 'Pause all');
    const onclose = vi.fn();
    render(CommandPalette, { props: { commands: [a, b], onclose } });
    const input = screen.getByRole('combobox');
    await fireEvent.keyDown(input, { key: 'Enter' });
    expect(onclose).not.toHaveBeenCalled();
    await fireEvent.keyDown(input, { key: 'ArrowUp' });
    expect(screen.getAllByRole('option')[1]?.getAttribute('aria-selected')).toBe('true');
    await fireEvent.keyDown(input, { key: 'ArrowDown' });
    expect(screen.getAllByRole('option')[0]?.getAttribute('aria-selected')).toBe('true');
    expect(screen.getAllByRole('option')[0]?.getAttribute('aria-disabled')).toBe('true');
  });

  it('names its groups, says why a command is unavailable and announces the count', async () => {
    const a = cmd('a', 'Pause selected', { disabled: true, disabledReason: 'nothing selected' });
    const b = cmd('b', 'Open settings', { group: 'View' });
    render(CommandPalette, { props: { commands: [a, b], onclose: vi.fn() } });
    expect(screen.getByRole('group', { name: 'Downloads' })).toBeTruthy();
    expect(screen.getByRole('group', { name: 'View' })).toBeTruthy();
    const pause = screen.getByRole('option', { name: 'Pause selected' });
    const describedBy = pause.getAttribute('aria-describedby');
    expect(document.getElementById(describedBy ?? '')?.textContent).toBe('Unavailable: nothing selected');
    expect(screen.getByRole('status').textContent).toBe('2 commands');
    await fireEvent.input(screen.getByRole('combobox'), { target: { value: 'settings' } });
    expect(screen.getByRole('status').textContent).toBe('1 command');
  });

  it('closes on Escape and says when nothing matches', async () => {
    const onclose = vi.fn();
    render(CommandPalette, { props: { commands: [cmd('a', 'Pause all')], onclose } });
    const input = screen.getByRole('combobox');
    await fireEvent.input(input, { target: { value: 'zzz' } });
    expect(screen.queryAllByRole('option')).toHaveLength(0);
    expect(screen.getByText('No matching commands')).toBeTruthy();
    await fireEvent.keyDown(input, { key: 'Escape' });
    expect(onclose).toHaveBeenCalled();
  });

  it('describes every command in plain words', () => {
    const pause = cmd('pause-all', 'Pause all', { description: 'Stops the 3 active downloads, keeps their progress', shortcut: 'Ctrl+Shift+P' });
    render(CommandPalette, { props: { commands: [pause], onclose: vi.fn() } });
    const option = screen.getByRole('option', { name: 'Pause all' });
    expect(document.getElementById(option.getAttribute('aria-describedby') ?? '')?.textContent).toBe(
      'Stops the 3 active downloads, keeps their progress',
    );
    expect([...option.querySelectorAll('kbd')].map((k) => k.textContent)).toEqual(['Ctrl', 'Shift', 'P']);
  });

  it('shows running downloads with their progress; Enter opens one, Ctrl+Enter shows it in its folder', async () => {
    const live = makeDownload({ id: 5, filename: 'conference-keynote.mp4', state: 'downloading', size: 100 * MiB, received: 42 * MiB, speedBps: 6 * MiB });
    const done = makeDownload({ id: 6, filename: 'keynote-2026.mp4', state: 'completed', received: 10 * MiB, completedAt: 1_700_000_500_000 });
    const downloads = [item(live, { kind: 'video', host: 'youtube.com', tag: 'YouTube' }), item(done, { kind: 'video', host: 'media.example.org' })];
    const onopendownload = vi.fn();
    const onshowfolder = vi.fn();
    const onclose = vi.fn();
    const { unmount } = render(CommandPalette, {
      props: { commands: [cmd('add', 'Add download…')], downloads, onopendownload, onshowfolder, onclose },
    });
    const group = screen.getByRole('group', { name: 'Running downloads' });
    const row = within(group).getByRole('option');
    expect(row.textContent).toContain('conference-keynote.mp4');
    expect(row.textContent).toContain('42%');
    expect(row.textContent).toContain('6.3 MB/s');
    expect(row.textContent).toMatch(/left/);
    expect(row.querySelector('.segbar.size-mini')).toBeTruthy();
    // only running downloads before a query
    expect(screen.queryByText('keynote-2026.mp4')).toBeNull();
    expect(screen.getByRole('status').textContent).toBe('1 download · 1 command');
    expect(screen.getByText('show in folder')).toBeTruthy();

    const input = screen.getByRole('combobox', { name: 'Search commands' });
    expect(input.getAttribute('aria-activedescendant')).toBe(row.id);
    await fireEvent.keyDown(input, { key: 'Enter' });
    expect(onclose).toHaveBeenCalledTimes(1);
    await tick();
    expect(onopendownload).toHaveBeenCalledWith(5);
    unmount();

    render(CommandPalette, { props: { commands: [], downloads, onopendownload, onshowfolder, onclose } });
    const again = screen.getByRole('combobox');
    await fireEvent.input(again, { target: { value: 'keynote 2026' } });
    const found = screen.getAllByRole('option');
    expect(found).toHaveLength(1);
    expect(found[0]?.textContent).toContain('Completed');
    expect(found[0]?.textContent).toContain('media.example.org');
    expect(found[0]?.querySelectorAll('mark').length).toBeGreaterThan(0);
    await fireEvent.keyDown(again, { key: 'Enter', ctrlKey: true });
    await tick();
    expect(onshowfolder).toHaveBeenCalledWith(6);
    expect(onopendownload).toHaveBeenCalledTimes(1);
  });
});
