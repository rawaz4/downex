import { fireEvent, render, screen, within } from '@testing-library/svelte';
import { tick } from 'svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import App from './App.svelte';
import RestartDialog from './lib/components/RestartDialog.svelte';
import { CONFIRM_GUARD_MS } from './lib/confirm';
import { DEFAULT_SETTINGS, type Download } from './lib/api';
import { MiB, PAGE, chooseOption, fakeApi, makeDownload, makeMediaDownload, makeSiteDownload, makeTools, optionValues } from './lib/testing';

afterEach(() => {
  vi.useRealTimers();
});

async function settle(ms = 0) {
  await vi.advanceTimersByTimeAsync(ms);
  await tick();
}

const grid = () => screen.getByRole('grid');
const rowOf = (id: number) => document.getElementById(`dl-row-${id}`)!;
/** Waits until a confirmation dialog that opened by itself accepts clicks. */
const pastGuard = () => new Promise((r) => setTimeout(r, CONFIRM_GUARD_MS + 50));
const toolbarButton = (name: string) =>
  screen.getAllByRole('button', { name }).find((b) => b.closest('header.toolbar'))!;
/** A dialog's quality cards (a radio group) and the chosen card. */
const qualityRadios = (dialog: HTMLElement) =>
  within(within(dialog).getByRole('radiogroup', { name: 'Video quality' })).getAllByRole('radio') as HTMLInputElement[];
const checkedQuality = (dialog: HTMLElement) =>
  within(within(dialog).getByRole('radiogroup', { name: 'Video quality' })).getByRole('radio', { checked: true }) as HTMLInputElement;

describe('App: downexd becomes unreachable', () => {
  it('shows a stale list with a retry banner, blocks actions and announces recovery', async () => {
    vi.useFakeTimers();
    const api = fakeApi([
      makeDownload({ id: 1, state: 'downloading', speedBps: 2 * MiB, received: MiB }),
      makeDownload({ id: 2, state: 'paused' }),
    ]);
    render(App, { props: { api } });
    await settle();
    expect(screen.queryByRole('region', { name: /can’t reach downexd/ })).toBeNull();

    await fireEvent.click(rowOf(1));
    expect(toolbarButton('Pause').getAttribute('aria-disabled')).toBeNull();

    api.down = true;
    await settle(3000);
    const banner = screen.getByRole('region', { name: /can’t reach downexd/ });
    expect(banner.textContent).toContain('last known state');
    expect(grid().classList.contains('stale')).toBe(true);
    expect(document.getElementById('dl-cell-1-speed')?.textContent).toBe('');
    expect(screen.getByRole('contentinfo').textContent).toContain('Total speed 0 B/s');
    for (const name of ['Add', 'Pause', 'Remove']) {
      expect(toolbarButton(name).getAttribute('aria-disabled')).toBe('true');
    }
    const callsBefore = api.calls.length;
    await fireEvent.click(toolbarButton('Pause'));
    await fireEvent.keyDown(grid(), { key: 'Delete' });
    await settle();
    expect(screen.queryByRole('alertdialog')).toBeNull();
    expect(api.calls.slice(callsBefore).filter((c) => c !== 'status')).toEqual([]);

    api.down = false;
    await fireEvent.click(screen.getByRole('button', { name: 'Retry now' }));
    await settle();
    expect(screen.queryByRole('region', { name: /can’t reach downexd/ })).toBeNull();
    expect(screen.getAllByRole('status').some((s) => s.textContent === 'Reconnected to downexd.')).toBe(true);
    expect(toolbarButton('Pause').getAttribute('aria-disabled')).toBeNull();
  });
});

describe('App: opening files', () => {
  it('confirms before opening a program whose name hides its extension', async () => {
    const api = fakeApi([
      makeDownload({ id: 1, filename: 'invoice‮fdp.exe', state: 'completed', received: 10 * MiB }),
      makeDownload({ id: 2, filename: 'notes.pdf', state: 'completed', received: 10 * MiB, createdAt: 1 }),
    ]);
    render(App, { props: { api } });
    await vi.waitFor(() => expect(rowOf(1)).toBeTruthy());
    expect(document.getElementById('dl-name-1')?.textContent).toBe('invoice⟨U+202E⟩fdp.exe');
    expect(document.getElementById('dl-name-1')?.getAttribute('dir')).toBe('auto');

    await fireEvent.dblClick(rowOf(1));
    const dialog = await screen.findByRole('alertdialog', { name: 'Run this program?' });
    expect(dialog.textContent).toContain('invoice⟨U+202E⟩fdp.exe');
    expect(dialog.textContent).toContain('hidden text-direction characters');
    expect(api.calls).not.toContain('openFile');
    await fireEvent.click(screen.getByRole('button', { name: 'Run' }));
    await vi.waitFor(() => expect(api.calls).toContain('openFile'));

    // ordinary documents open directly
    api.calls.length = 0;
    await fireEvent.dblClick(rowOf(2));
    await vi.waitFor(() => expect(api.calls).toContain('openFile'));
    expect(screen.queryByRole('alertdialog')).toBeNull();
  });
});

describe('App: dropping onto the window', () => {
  const transfer = (data: Record<string, string>) => ({
    types: Object.keys(data),
    dropEffect: 'none',
    getData: (type: string) => data[type] ?? '',
  });

  it('never leaves a dropped file or link to the webview, which would replace the app', async () => {
    const api = fakeApi([makeDownload({ id: 1 })]);
    render(App, { props: { api } });
    await vi.waitFor(() => expect(rowOf(1)).toBeTruthy());

    const file = transfer({ Files: '' });
    expect(await fireEvent.dragOver(grid(), { dataTransfer: file })).toBe(false);
    expect(file.dropEffect).toBe('none');
    expect(await fireEvent.drop(grid(), { dataTransfer: file })).toBe(false);
    const local = transfer({ 'text/uri-list': 'file:///C:/Users/me/notes.txt' });
    expect(await fireEvent.drop(grid(), { dataTransfer: local })).toBe(false);
    expect(screen.queryByRole('dialog')).toBeNull();

    // text dropped into a field is left to it
    const search = screen.getByRole('searchbox');
    expect(await fireEvent.drop(search, { dataTransfer: transfer({ 'text/plain': 'iso' }) })).toBe(true);
    expect(await fireEvent.drop(search, { dataTransfer: file })).toBe(false);

    // links still open the Add dialog
    expect(await fireEvent.drop(grid(), { dataTransfer: transfer({ 'text/uri-list': 'https://example.com/a.zip' }) })).toBe(false);
    await screen.findByRole('dialog');
  });
});

describe('App: focus after removing', () => {
  it('keeps keyboard focus when the toolbar Remove button is used', async () => {
    const api = fakeApi([makeDownload({ id: 1 }), makeDownload({ id: 2, createdAt: 1 })]);
    render(App, { props: { api } });
    await vi.waitFor(() => expect(rowOf(1)).toBeTruthy());
    await fireEvent.click(rowOf(1));
    const remove = toolbarButton('Remove');
    remove.focus();
    await fireEvent.click(remove);
    const dialog = await screen.findByRole('alertdialog', { name: 'Remove download?' });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Remove' }));
    await vi.waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull());
    await tick();
    await new Promise((r) => setTimeout(r, 0));
    expect(document.activeElement).not.toBe(document.body);
    // the next row took the removed one's place
    expect(rowOf(2).getAttribute('aria-selected')).toBe('true');
  });
});

describe('RestartDialog', () => {
  it('isolates the name inside the sentence', () => {
    render(RestartDialog, {
      props: { names: ['(final) report.pdf'], reason: 'manual', onconfirm: vi.fn(), onclose: vi.fn() },
    });
    const dialog = screen.getByRole('alertdialog');
    const desc = document.getElementById(dialog.getAttribute('aria-describedby')!);
    expect(desc?.textContent).toContain('“⁨(final) report.pdf⁩”');
  });
});

describe('App: confirming browser hand-offs', () => {
  const awaiting = (id: number, createdAt: number, filename: string) =>
    makeDownload({
      id,
      filename,
      state: 'awaiting_confirmation',
      source: 'browser',
      size: -1,
      createdAt,
      category: 'Video',
      dir: '/home/you/Downloads/Video',
    });

  it('shows one dialog per waiting download, oldest first, and remembers a closed one', async () => {
    const api = fakeApi([makeDownload({ id: 1 }), awaiting(2, 200, 'newer.mp4'), awaiting(3, 100, 'older.mp4')]);
    render(App, { props: { api } });
    let dialog = await screen.findByRole('dialog', { name: 'Confirm download' });
    expect((within(dialog).getByLabelText('File name') as HTMLInputElement).value).toBe('older.mp4');
    expect(document.getElementById('dl-status-3')?.textContent).toBe('Waiting for confirmation');
    // it opened by itself: keystrokes meant for something else do not reach its form
    expect(document.activeElement).toBe(dialog);

    await pastGuard();
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Start download' }));
    await vi.waitFor(() => expect(api.calls).toContain('confirm'));
    expect(api.args.get('confirm')).toEqual([3, { start: true, dir: '', filename: '' }]);
    // the next one follows
    dialog = await screen.findByRole('dialog', { name: 'Confirm download' });
    await vi.waitFor(() => expect((within(dialog).getByLabelText('File name') as HTMLInputElement).value).toBe('newer.mp4'));

    // closing leaves it waiting, and the dialog does not come back by itself
    await fireEvent.keyDown(dialog, { key: 'Escape' });
    await vi.waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    await new Promise((r) => setTimeout(r, 20));
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(api.items.get(2)?.state).toBe('awaiting_confirmation');

    // downexd asks again ("downex-ui --confirm 2")
    api.requestConfirm(2);
    dialog = await screen.findByRole('dialog', { name: 'Confirm download' });
    expect(document.activeElement).toBe(dialog);
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel download' }));
    await tick();
    expect(api.calls).not.toContain('remove'); // too soon after it appeared to be deliberate
    await pastGuard();
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel download' }));
    await vi.waitFor(() => expect(api.calls).toContain('remove'));
    expect(api.args.get('remove')).toEqual([2, { deleteFile: false }]);
    await vi.waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  });

  it('offers the dialog again from the list and filters waiting downloads', async () => {
    const api = fakeApi([makeDownload({ id: 1, state: 'paused' }), awaiting(2, 100, 'clip.mp4')]);
    render(App, { props: { api } });
    const dialog = await screen.findByRole('dialog', { name: 'Confirm download' });
    await fireEvent.keyDown(dialog, { key: 'Escape' });
    await vi.waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());

    await fireEvent.contextMenu(rowOf(2));
    const item = await screen.findByRole('menuitem', { name: /Confirm download/ });
    await fireEvent.click(item);
    await screen.findByRole('dialog', { name: 'Confirm download' });
    await fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' });
    await vi.waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());

    // Enter (or a double-click) on the row opens it too, ready for editing the name
    await fireEvent.dblClick(rowOf(2));
    const asked = await screen.findByRole('dialog', { name: 'Confirm download' });
    await vi.waitFor(() => expect(document.activeElement).toBe(within(asked).getByLabelText('File name')));
    await fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' });
    await vi.waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());

    await fireEvent.click(screen.getByRole('button', { name: /To confirm/ }));
    expect(document.getElementById('dl-row-1')).toBeNull();
    expect(rowOf(2)).toBeTruthy();
  });
});

describe('App: categories from the settings', () => {
  it('lists the configured categories, custom ones too, and Other', async () => {
    const api = fakeApi([
      makeDownload({ id: 1, filename: 'a.cbz', category: 'Comics' }),
      makeDownload({ id: 2, filename: 'b.mp4', category: 'Video' }),
      makeDownload({ id: 3, filename: 'c.bin' }),
    ]);
    api.settings = {
      ...api.settings,
      categories: [
        { name: 'Video', extensions: ['mp4'], dir: '' },
        { name: 'Comics', extensions: ['cbz'], dir: '' },
      ],
    };
    render(App, { props: { api } });
    const nav = await screen.findByRole('navigation', { name: 'Filters' });
    await vi.waitFor(() => expect(within(nav).getByRole('button', { name: /Comics/ })).toBeTruthy());
    const labels = within(nav)
      .getAllByRole('button')
      .map((b) => b.querySelector('.label')?.textContent);
    expect(labels.slice(0, 4)).toEqual(['All downloads', 'Video', 'Comics', 'Other']);
    await fireEvent.click(within(nav).getByRole('button', { name: /Comics/ }));
    await vi.waitFor(() => expect(document.getElementById('dl-row-2')).toBeNull());
    expect(rowOf(1)).toBeTruthy();
    await fireEvent.click(within(nav).getByRole('button', { name: /Other/ }));
    await vi.waitFor(() => expect(document.getElementById('dl-row-1')).toBeNull());
    expect(rowOf(3)).toBeTruthy();
  });
});

describe('App: pause all and resume all', () => {
  it('uses the daemon calls and reports how many changed', async () => {
    const api = fakeApi([makeDownload({ id: 1, state: 'downloading' }), makeDownload({ id: 2, state: 'paused' })]);
    render(App, { props: { api } });
    await vi.waitFor(() => expect(rowOf(1)).toBeTruthy());
    await fireEvent.click(toolbarButton('Pause all'));
    await vi.waitFor(() => expect(api.calls).toContain('pauseAll'));
    await vi.waitFor(() => expect(screen.getAllByRole('status').some((s) => s.textContent?.includes('1 download paused'))).toBe(true));
    await fireEvent.click(toolbarButton('Resume all'));
    await vi.waitFor(() => expect(api.calls).toContain('resumeAll'));
  });

  it('offers Resume all only for paused downloads, as the daemon resumes only those', async () => {
    const api = fakeApi([
      makeDownload({ id: 1, state: 'failed' }),
      makeDownload({ id: 2, state: 'canceled' }),
    ]);
    render(App, { props: { api } });
    await vi.waitFor(() => expect(rowOf(1)).toBeTruthy());
    expect(toolbarButton('Resume all').getAttribute('aria-disabled')).toBe('true');
  });
});

describe('App: settings stay current', () => {
  const awaitingClip = (id: number) =>
    makeDownload({
      id,
      filename: 'clip.mp4',
      state: 'awaiting_confirmation',
      source: 'browser',
      size: -1,
      category: 'Books',
      dir: '/srv/books',
    });
  const custom = {
    ...DEFAULT_SETTINGS,
    defaultDir: '/srv/dl',
    categories: [...DEFAULT_SETTINGS.categories, { name: 'Books', extensions: ['epub'], dir: '/srv/books' }],
  };

  it('reads the settings once downexd is reachable, also when it was not at first', async () => {
    vi.useFakeTimers();
    const api = fakeApi([awaitingClip(1)], custom);
    api.down = true;
    render(App, { props: { api } });
    await settle();
    expect(screen.queryByRole('dialog')).toBeNull();

    api.down = false;
    await settle(3000);
    await settle();
    const dialog = screen.getByRole('dialog', { name: 'Confirm download' });
    const category = within(dialog).getByRole('combobox', { name: 'Category' }) as HTMLButtonElement;
    expect(optionValues(category)).toContain('Books');
    expect(category.value).toBe('Books');
    await chooseOption(category, 'Audio');
    expect((within(dialog).getByLabelText('Save to folder') as HTMLInputElement).value).toBe('/srv/dl/Audio');
  });

  it('tries again at the next status check after a failed read', async () => {
    vi.useFakeTimers();
    const api = fakeApi([makeDownload({ id: 1, filename: 'a.epub', category: 'Books' })], custom);
    const read = api.getSettings.bind(api);
    let failing = true;
    api.getSettings = async () => {
      if (failing) throw new Error('deadline_exceeded: downexd did not answer');
      return read();
    };
    render(App, { props: { api } });
    await settle();
    const nav = screen.getByRole('navigation', { name: 'Filters' });
    expect(within(nav).queryByRole('button', { name: /Books/ })).toBeNull();
    failing = false;
    await settle(3000);
    await settle();
    expect(within(nav).getByRole('button', { name: /Books/ })).toBeTruthy();
  });

  it('reads the settings again before showing a confirmation dialog', async () => {
    const api = fakeApi([makeDownload({ id: 1 })]);
    render(App, { props: { api } });
    await vi.waitFor(() => expect(rowOf(1)).toBeTruthy());
    await vi.waitFor(() => expect(api.calls).toContain('getSettings'));
    // changed by another client ("downex settings --dir ...")
    api.settings = custom;
    api.items.set(2, awaitingClip(2));
    api.emit([{ kind: 'updated', download: awaitingClip(2) }]);
    const dialog = await screen.findByRole('dialog', { name: 'Confirm download' });
    const category = within(dialog).getByRole('combobox', { name: 'Category' }) as HTMLButtonElement;
    expect(optionValues(category)).toContain('Books');
    expect(category.value).toBe('Books');
  });
});


describe('App: streaming media', () => {
  it('confirms a stream from the browser with the chosen quality', async () => {
    const api = fakeApi([
      makeMediaDownload({ id: 2, state: 'awaiting_confirmation', source: 'browser', dir: '/home/you/Downloads/Video' }),
    ]);
    render(App, { props: { api } });
    const dialog = await screen.findByRole('dialog', { name: 'Confirm download' });
    expect(checkedQuality(dialog).value).toBe('v1');
    await fireEvent.click(qualityRadios(dialog).find((r) => r.value === 'v2')!); // muxed: no audio track
    expect(checkedQuality(dialog).value).toBe('v2');
    expect(within(dialog).queryByLabelText('Audio track')).toBeNull();
    await pastGuard();
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Start download' }));
    await vi.waitFor(() => expect(api.calls).toContain('confirm'));
    expect(api.args.get('confirm')).toEqual([2, { start: true, dir: '', filename: '', videoTrack: 'v2', audioTrack: '' }]);
  });

  it('adds a stream pasted into the window after listing its qualities', async () => {
    const api = fakeApi([makeDownload({ id: 1, state: 'completed' })]);
    render(App, { props: { api } });
    await vi.waitFor(() => expect(rowOf(1)).toBeTruthy());
    const paste = new Event('paste', { bubbles: true, cancelable: true }) as ClipboardEvent;
    Object.defineProperty(paste, 'clipboardData', {
      value: { getData: () => 'https://media.example.org/talk/master.m3u8' },
    });
    window.dispatchEvent(paste);
    const dialog = await screen.findByRole('dialog', { name: 'Add video or stream' });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Find qualities' }));
    await vi.waitFor(() => expect(api.calls).toContain('probeMedia'));
    await within(dialog).findByRole('radiogroup', { name: 'Video quality' });
    expect(checkedQuality(dialog).value).toBe('v1');
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Add stream' }));
    await vi.waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    expect(api.args.get('addMedia')).toEqual([
      { manifestUrl: 'https://media.example.org/talk/master.m3u8', dir: '', filename: '', source: 'ui', videoTrack: 'v1', audioTrack: 'r0', startPaused: false },
    ]);
    // the new stream is listed at once, with its badge
    await vi.waitFor(() => expect(document.querySelector('#dl-cell-2-name .media')?.textContent?.trim()).toBe('HLS'));
  });

  it('tells why an encrypted stream cannot be added', async () => {
    const api = fakeApi([]);
    render(App, { props: { api } });
    await vi.waitFor(() => expect(api.calls).toContain('list'));
    await fireEvent.keyDown(window, { key: 'n', ctrlKey: true });
    const dialog = await screen.findByRole('dialog', { name: 'Add downloads' });
    await fireEvent.click(within(dialog).getByLabelText('Video or stream'));
    await fireEvent.input(within(dialog).getByLabelText('Video page or stream address'), {
      target: { value: 'https://media.example.org/drm/master.m3u8' },
    });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Find qualities' }));
    expect((await within(dialog).findByRole('alert')).textContent).toContain('encrypted or DRM-protected');
    expect(api.calls).not.toContain('addMedia');
  });
});

describe('App: video pages (SITE-MEDIA-SPEC §5.4)', () => {
  it('confirms a video from the browser in another MP4 quality, keeping its .mp4 name', async () => {
    const api = fakeApi([makeSiteDownload({ id: 3, state: 'awaiting_confirmation', source: 'browser', dir: '/home/you/Downloads/Video' })]);
    render(App, { props: { api } });
    const dialog = await screen.findByRole('dialog', { name: 'Confirm download' });
    expect(dialog.textContent).toContain('A video from the browser is waiting');
    // the video card: the site, its page's address and the length (a hand-off may carry
    // cookies: no "No cookies sent")
    expect(dialog.textContent).toContain('YouTube ·');
    expect(within(dialog).getByRole('list', { name: 'About this video' }).textContent).toContain('10:00');
    expect(within(dialog).queryByText('No cookies sent')).toBeNull();
    expect((within(dialog).getByRole('textbox', { name: 'Page' }) as HTMLInputElement).value).toBe(PAGE);
    expect(checkedQuality(dialog).value).toBe('299');
    expect(qualityRadios(dialog).map((r) => r.value)).toEqual(['299', '136', '397', '18']);
    await fireEvent.click(qualityRadios(dialog).find((r) => r.value === '136')!);
    expect(within(dialog).getByText(/Size: 126 MB/)).toBeTruthy();
    await pastGuard();
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Start download' }));
    await vi.waitFor(() => expect(api.calls).toContain('confirm'));
    expect(api.args.get('confirm')).toEqual([3, { start: true, dir: '', filename: '', videoTrack: '136', audioTrack: '140' }]);
    expect(api.items.get(3)?.filename).toBe('A talk.mp4');
  });

  it('adds a video page from the Add dialog after listing its qualities', async () => {
    const api = fakeApi([]);
    api.siteAnswers.push('failed_precondition: site_resolving: still getting the video info');
    render(App, { props: { api } });
    await vi.waitFor(() => expect(api.calls).toContain('list'));
    await fireEvent.keyDown(window, { key: 'n', ctrlKey: true });
    const dialog = await screen.findByRole('dialog', { name: 'Add downloads' });
    await fireEvent.click(within(dialog).getByLabelText('Video or stream'));
    await fireEvent.input(within(dialog).getByLabelText('Video page or stream address'), { target: { value: PAGE } });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Find qualities' }));
    // site_resolving first: asked again 2 s later
    await within(dialog).findByRole('radiogroup', { name: 'Video quality' }, { timeout: 4000 });
    // The Add dialog never sends cookies: its video card says so.
    expect(within(dialog).getByText('No cookies sent')).toBeTruthy();
    expect(api.calls.filter((c) => c === 'probeMedia')).toHaveLength(2);
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Add video' }));
    await vi.waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    expect(api.args.get('addMedia')).toEqual([
      { manifestUrl: '', pageUrl: PAGE, origin: 'site', dir: '', filename: '', source: 'ui', videoTrack: '299', audioTrack: '140', startPaused: false },
    ]);
  });

  it('shows the first download of yt-dlp and Deno in a toast, and offers the settings when it fails', async () => {
    const api = fakeApi([makeDownload({ id: 1, state: 'completed' })]);
    api.tools = makeTools();
    render(App, { props: { api } });
    await vi.waitFor(() => expect(rowOf(1)).toBeTruthy());
    const installing = makeTools({
      ready: false,
      tools: makeTools().tools.map((t) => ({ ...t, state: 'installing' as const, version: '', bytesDone: 10 * MiB, bytesTotal: 40 * MiB })),
    });
    api.emit([{ kind: 'tools', status: installing }]);
    await tick();
    const toast = screen.getByRole('status', { name: 'Setting up video downloads' });
    expect(within(toast).getByRole('progressbar', { name: 'Downloading yt-dlp and Deno' }).getAttribute('aria-valuenow')).toBe('25');
    // the percentage once, beside the bar
    expect(toast.textContent).toContain('Downloading yt-dlp and Deno…');
    expect(toast.textContent).not.toContain('Deno: 25%');

    const failed = makeTools({
      ready: false,
      tools: makeTools().tools.map((t) => ({ ...t, state: 'failed' as const, version: '', error: `could not download ${t.name} (network)` })),
    });
    api.emit([{ kind: 'tools', status: failed }]);
    await tick();
    expect(screen.queryByRole('status', { name: 'Setting up video downloads' })).toBeNull();
    const notice = screen.getByRole('group', { name: 'Downex could not set up yt-dlp/Deno' });
    expect(notice.textContent).toContain('could not download yt-dlp (network)');
    await fireEvent.click(within(notice).getByRole('button', { name: 'Open settings' }));
    const settings = await screen.findByRole('dialog', { name: 'Settings' });
    expect(within(settings).getByRole('tab', { name: 'Video sites' }).getAttribute('aria-selected')).toBe('true');
    await vi.waitFor(() => expect(api.calls).toContain('getTools'));
  });

  it('shows no toast while ready copies are updated', async () => {
    const api = fakeApi([makeDownload({ id: 1, state: 'completed' })]);
    render(App, { props: { api } });
    await vi.waitFor(() => expect(rowOf(1)).toBeTruthy());
    const updating = makeTools({ tools: makeTools().tools.map((t) => ({ ...t, state: 'installing' as const, bytesTotal: 40 * MiB })) });
    api.emit([{ kind: 'tools', status: updating }]);
    api.emit([{ kind: 'tools', status: makeTools({ tools: makeTools().tools.map((t) => ({ ...t, state: 'failed' as const })) }) }]);
    await tick();
    expect(screen.queryByRole('status', { name: 'Setting up video downloads' })).toBeNull();
    expect(screen.queryByRole('group', { name: 'Downex could not set up yt-dlp/Deno' })).toBeNull();
  });
});

describe('App: progress windows', () => {
  const addDialog = async () => {
    await fireEvent.keyDown(window, { key: 'n', ctrlKey: true });
    return screen.findByRole('dialog', { name: 'Add downloads' });
  };

  it('opens the progress window of an unfinished download from the list, whatever the setting', async () => {
    const api = fakeApi(
      [
        makeDownload({ id: 1, state: 'downloading', speedBps: MiB }),
        makeDownload({ id: 2, state: 'paused' }),
        makeDownload({ id: 3, state: 'completed', received: 10 * MiB }),
      ],
      { ...DEFAULT_SETTINGS, defaultDir: '/home/you/Downloads', showProgressWindow: false },
    );
    render(App, { props: { api } });
    await vi.waitFor(() => expect(rowOf(1)).toBeTruthy());
    await fireEvent.dblClick(rowOf(1));
    await vi.waitFor(() => expect(api.opened).toEqual([1]));
    await fireEvent.click(rowOf(2));
    await fireEvent.keyDown(grid(), { key: 'Enter' });
    await vi.waitFor(() => expect(api.opened).toEqual([1, 2]));

    await fireEvent.contextMenu(rowOf(1));
    await fireEvent.click(await screen.findByRole('menuitem', { name: /Show progress/ }));
    await vi.waitFor(() => expect(api.opened).toEqual([1, 2, 1]));

    // A completed download opens its file instead, and has no progress to show.
    await fireEvent.contextMenu(rowOf(3));
    expect(screen.queryByRole('menuitem', { name: /Show progress/ })).toBeNull();
    await fireEvent.keyDown(window, { key: 'Escape' });
    await fireEvent.dblClick(rowOf(3));
    await vi.waitFor(() => expect(api.calls).toContain('openFile'));
    expect(api.opened).toEqual([1, 2, 1]);
  });

  it('opens it for a download added to start now, when the setting is on', async () => {
    const api = fakeApi([]);
    render(App, { props: { api } });
    await vi.waitFor(() => expect(api.calls).toContain('getSettings'));
    let dialog = await addDialog();
    await fireEvent.input(within(dialog).getByLabelText('URLs'), { target: { value: 'https://example.com/one.zip' } });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Add download' }));
    await vi.waitFor(() => expect(api.opened).toEqual([1]));

    // Added paused: no window.
    dialog = await addDialog();
    await fireEvent.input(within(dialog).getByLabelText('URLs'), { target: { value: 'https://example.com/two.zip' } });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'More options' }));
    await fireEvent.click(within(dialog).getByLabelText(/Add paused/));
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Add download' }));
    await vi.waitFor(() => expect(api.calls.filter((c) => c === 'add')).toHaveLength(2));
    await vi.waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());

    // A list of addresses opens none.
    dialog = await addDialog();
    await fireEvent.input(within(dialog).getByLabelText('URLs'), {
      target: { value: 'https://example.com/three.zip\nhttps://example.com/four.zip' },
    });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Add 2 downloads' }));
    await vi.waitFor(() => expect(api.calls.filter((c) => c === 'add')).toHaveLength(4));
    await new Promise((r) => setTimeout(r, 20));
    expect(api.opened).toEqual([1]);
  });

  it('opens it after "Start download" in the confirmation dialog, unless it is turned off', async () => {
    const waiting = (id: number) =>
      makeDownload({ id, state: 'awaiting_confirmation', source: 'browser', size: -1, createdAt: id, dir: '/home/you/Downloads' });
    const api = fakeApi([waiting(1), waiting(2)]);
    render(App, { props: { api } });
    let dialog = await screen.findByRole('dialog', { name: 'Confirm download' });
    await pastGuard();
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Start download' }));
    await vi.waitFor(() => expect(api.opened).toEqual([1]));

    // "Download later" keeps it paused: no window.
    dialog = await screen.findByRole('dialog', { name: 'Confirm download' });
    api.settings = { ...api.settings, showProgressWindow: false };
    await fireEvent.focus(window);
    await vi.waitFor(() => expect(api.calls.filter((c) => c === 'getSettings').length).toBeGreaterThan(1));
    await pastGuard();
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Start download' }));
    await vi.waitFor(() => expect(api.calls.filter((c) => c === 'confirm')).toHaveLength(2));
    await new Promise((r) => setTimeout(r, 20));
    expect(api.opened).toEqual([1]);
  });

  it('opens none for a download that waits in the queue (max_concurrent_downloads reached)', async () => {
    const waiting = makeDownload({ id: 1, state: 'awaiting_confirmation', source: 'browser', size: -1, createdAt: 1 });
    const api = fakeApi([waiting]);
    // Like downexd with every slot taken: what it accepts is queued at position 1.
    const inQueue = (d: Download): Download => {
      const q = { ...d, state: 'queued' as const, queuePosition: 1 };
      api.items.set(q.id, q);
      return q;
    };
    const confirm = api.confirm;
    api.confirm = async (id, opts) => inQueue(await confirm(id, opts));
    const add = api.add;
    api.add = async (req) => inQueue(await add(req));
    render(App, { props: { api } });

    const confirmDialog = await screen.findByRole('dialog', { name: 'Confirm download' });
    await pastGuard();
    await fireEvent.click(within(confirmDialog).getByRole('button', { name: 'Start download' }));
    await vi.waitFor(() => expect(api.calls).toContain('confirm'));
    await vi.waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());

    const dialog = await addDialog();
    await fireEvent.input(within(dialog).getByLabelText('URLs'), { target: { value: 'https://example.com/one.zip' } });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Add download' }));
    await vi.waitFor(() => expect(api.calls).toContain('add'));
    await vi.waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    await new Promise((r) => setTimeout(r, 20));
    expect(api.opened).toEqual([]);
    expect(api.calls).not.toContain('openProgressWindow');
  });

  /** The state a row shows (the list has taken the daemon's answer). */
  const shown = (id: number) => rowOf(id)?.dataset.state;
  /** Clicks a toolbar button once it is enabled (the selection's state has reached the list). */
  const press = async (name: string) => {
    const b = await vi.waitFor(() => {
      const el = toolbarButton(name);
      expect(el.getAttribute('aria-disabled')).toBeNull();
      return el;
    });
    await fireEvent.click(b);
  };

  it('opens it after resuming one download from the main window (toolbar, Space, row action), when the setting is on', async () => {
    const api = fakeApi([makeDownload({ id: 1, state: 'paused', received: MiB })]);
    render(App, { props: { api } });
    await vi.waitFor(() => expect(api.calls).toContain('getSettings'));
    await vi.waitFor(() => expect(rowOf(1)).toBeTruthy());
    await fireEvent.click(rowOf(1));
    await press('Resume');
    await vi.waitFor(() => expect(api.opened).toEqual([1]));

    // Space pauses (no window), then resumes (a window)
    await vi.waitFor(() => expect(shown(1)).toBe('queued'));
    await fireEvent.keyDown(grid(), { key: ' ' });
    await vi.waitFor(() => expect(shown(1)).toBe('paused'));
    await fireEvent.keyDown(grid(), { key: ' ' });
    await vi.waitFor(() => expect(api.opened).toEqual([1, 1]));

    await vi.waitFor(() => expect(shown(1)).toBe('queued'));
    await press('Pause');
    await vi.waitFor(() => expect(shown(1)).toBe('paused'));
    const resume = await vi.waitFor(() => {
      const b = rowOf(1).querySelector<HTMLButtonElement>('[data-row-action] button[title="Resume"]');
      expect(b).toBeTruthy();
      return b!;
    });
    await fireEvent.click(resume);
    await vi.waitFor(() => expect(api.opened).toEqual([1, 1, 1]));
  });

  it('opens none after resuming several downloads, Resume all, a resume that waits in the queue, or with the setting off', async () => {
    const api = fakeApi([
      makeDownload({ id: 1, state: 'paused', received: MiB }),
      makeDownload({ id: 2, state: 'paused', received: MiB, createdAt: 1 }),
    ]);
    render(App, { props: { api } });
    await vi.waitFor(() => expect(api.calls).toContain('getSettings'));
    await vi.waitFor(() => expect(rowOf(2)).toBeTruthy());

    // several selected
    await fireEvent.click(rowOf(1));
    await fireEvent.click(rowOf(2), { ctrlKey: true });
    await press('Resume');
    await vi.waitFor(() => expect(shown(1)).toBe('queued'));
    await vi.waitFor(() => expect(shown(2)).toBe('queued'));
    expect(api.calls.filter((c) => c === 'resume')).toHaveLength(2);

    // Resume all
    await press('Pause');
    await vi.waitFor(() => expect(shown(2)).toBe('paused'));
    await vi.waitFor(() => expect(shown(1)).toBe('paused'));
    await fireEvent.contextMenu(rowOf(1));
    await fireEvent.click(await screen.findByRole('menuitem', { name: /Resume all/ }));
    await vi.waitFor(() => expect(api.calls).toContain('resumeAll'));
    await fireEvent.keyDown(window, { key: 'Escape' });

    // one download that downexd queues behind max_concurrent_downloads (the list still shows it
    // paused: the fake sends no events for Resume all)
    await fireEvent.click(rowOf(1));
    const resume = api.resume;
    api.resume = async (id, opts) => {
      const d = { ...(await resume(id, opts)), queuePosition: 2 };
      api.items.set(id, d);
      return d;
    };
    await press('Resume');
    await vi.waitFor(() => expect(shown(1)).toBe('queued'));
    api.resume = resume;

    // the setting off
    await press('Pause');
    await vi.waitFor(() => expect(shown(1)).toBe('paused'));
    api.settings = { ...api.settings, showProgressWindow: false };
    await fireEvent.focus(window);
    await vi.waitFor(() => expect(api.calls.filter((c) => c === 'getSettings').length).toBeGreaterThan(1));
    await new Promise((r) => setTimeout(r, 20));
    await press('Resume');
    await vi.waitFor(() => expect(shown(1)).toBe('queued'));
    await new Promise((r) => setTimeout(r, 20));
    expect(api.opened).toEqual([]);
  });
  it('opens it after restarting one download, and after downloading a completed one again', async () => {
    const api = fakeApi([
      makeDownload({ id: 1, state: 'failed', error: 'HTTP 500', errorCode: 'http_status', received: MiB }),
      makeDownload({ id: 2, filename: 'notes.pdf', state: 'completed', received: 10 * MiB, createdAt: 1 }),
    ]);
    render(App, { props: { api } });
    await vi.waitFor(() => expect(api.calls).toContain('getSettings'));
    await vi.waitFor(() => expect(rowOf(2)).toBeTruthy());

    await fireEvent.contextMenu(rowOf(1));
    await fireEvent.click(await screen.findByRole('menuitem', { name: /Restart…/ }));
    let dialog = await screen.findByRole('alertdialog', { name: 'Restart download?' });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Restart' }));
    await vi.waitFor(() => expect(api.opened).toEqual([1]));

    await fireEvent.contextMenu(rowOf(2));
    await fireEvent.click(await screen.findByRole('menuitem', { name: /Download again…/ }));
    dialog = await screen.findByRole('alertdialog', { name: 'Download again?' });
    await fireEvent.click(within(dialog).getByRole('radio', { name: /Replace the current file/ }));
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Download again' }));
    await vi.waitFor(() => expect(api.opened).toEqual([1, 2]));
    expect(api.args.get('resume')).toEqual([2, { restart: true, existingFile: 'replace' }]);
    localStorage.removeItem('downex.existingFile');
  });

  it('reports a progress window that cannot be opened', async () => {
    const api = fakeApi([makeDownload({ id: 1, state: 'downloading' })]);
    api.openProgressWindow = async () => {
      throw new Error('internal: cannot open the progress window: no such file');
    };
    render(App, { props: { api } });
    await vi.waitFor(() => expect(rowOf(1)).toBeTruthy());
    await fireEvent.dblClick(rowOf(1));
    await vi.waitFor(() => expect(screen.getByText(/Could not open the progress window of/)).toBeTruthy());
    expect(screen.getByText(/Could not open the progress window of/).textContent).toContain('no such file');
  });
});

describe('App: downloading a completed download again', () => {
  const menuItem = (name: RegExp) => screen.findByRole('menuitem', { name });

  it('names the menu item after the selection and offers it for completed downloads', async () => {
    const api = fakeApi([
      makeDownload({ id: 1, filename: 'notes.pdf', state: 'completed', received: 10 * MiB }),
      makeDownload({ id: 2, state: 'paused', createdAt: 1 }),
    ]);
    render(App, { props: { api } });
    await vi.waitFor(() => expect(rowOf(1)).toBeTruthy());

    await fireEvent.contextMenu(rowOf(1));
    const again = await menuItem(/Download again…/);
    expect(again.getAttribute('aria-disabled')).not.toBe('true');
    expect(screen.queryByRole('menuitem', { name: /Restart…/ })).toBeNull();
    await fireEvent.keyDown(window, { key: 'Escape' });

    // a mixed selection: Restart… (the dialog asks only about the completed one)
    await fireEvent.click(rowOf(1));
    await fireEvent.click(rowOf(2), { ctrlKey: true });
    await fireEvent.contextMenu(rowOf(2));
    await fireEvent.click(await menuItem(/Restart…/));
    const dialog = await screen.findByRole('alertdialog', { name: 'Restart downloads?' });
    expect(within(dialog).getByRole('radiogroup').getAttribute('aria-label')).toBe('The current file of “⁨notes.pdf⁩”');
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Restart' }));
    await vi.waitFor(() => expect(api.calls.filter((c) => c === 'resume')).toHaveLength(2));
    // several downloads: no progress windows
    expect(api.opened).toEqual([]);
    expect(api.items.get(1)?.filename).toBe('notes (1).pdf');
  });

  it('skips the choice when the file is gone, and offers it in the details pane and the palette', async () => {
    const api = fakeApi([makeDownload({ id: 1, filename: 'notes.pdf', state: 'completed', received: 10 * MiB })]);
    api.gone.add(1);
    render(App, { props: { api } });
    await vi.waitFor(() => expect(rowOf(1)).toBeTruthy());
    await fireEvent.click(rowOf(1));
    const details = await screen.findByRole('region', { name: /notes\.pdf/ });
    await fireEvent.click(within(details).getByRole('button', { name: 'Download again…' }));
    let dialog = await screen.findByRole('alertdialog', { name: 'Download again?' });
    expect(api.args.get('fileExists')).toEqual([1]);
    expect(within(dialog).queryAllByRole('radio')).toEqual([]);
    expect(dialog.textContent).toContain('is no longer in its folder');
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }));
    await vi.waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull());

    api.gone.delete(1);
    await fireEvent.keyDown(window, { key: 'k', ctrlKey: true });
    const input = await screen.findByRole('combobox', { name: 'Search commands' });
    await fireEvent.input(input, { target: { value: 'download again' } });
    expect(screen.getAllByRole('option')[0]?.textContent).toContain('Download again…');
    await fireEvent.keyDown(input, { key: 'Enter' });
    dialog = await screen.findByRole('alertdialog', { name: 'Download again?' });
    expect(within(dialog).getAllByRole('radio')).toHaveLength(2);
  });
});

describe('App: the menu bar (macOS)', () => {
  it('opens Settings from Settings…, unless another dialog is open', async () => {
    const api = fakeApi([makeDownload({ id: 1, state: 'paused' })]);
    render(App, { props: { api } });
    await vi.waitFor(() => expect(rowOf(1)).toBeTruthy());

    await fireEvent.keyDown(window, { key: 'n', ctrlKey: true });
    const add = await screen.findByRole('dialog', { name: 'Add downloads' });
    api.menuCommand('settings');
    await tick();
    expect(screen.queryByRole('dialog', { name: 'Settings' })).toBeNull();
    expect(add.isConnected).toBe(true);

    await fireEvent.keyDown(add, { key: 'Escape' });
    await vi.waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    api.menuCommand('settings');
    expect(await screen.findByRole('dialog', { name: 'Settings' })).toBeTruthy();
  });
});

describe('App: command palette', () => {
  it('opens with Ctrl+K, finds a command by typing and runs it', async () => {
    const api = fakeApi([makeDownload({ id: 1, state: 'paused' }), makeDownload({ id: 2, state: 'completed' })]);
    render(App, { props: { api } });
    await vi.waitFor(() => expect(rowOf(1)).toBeTruthy());
    await fireEvent.keyDown(window, { key: 'k', ctrlKey: true });
    const input = await screen.findByRole('combobox', { name: 'Search commands' });
    await vi.waitFor(() => expect(document.activeElement).toBe(input));
    await fireEvent.input(input, { target: { value: 'status paused' } });
    expect(screen.getAllByRole('option')[0]?.textContent).toContain('Status: Paused');
    await fireEvent.keyDown(input, { key: 'Enter' });
    await vi.waitFor(() => expect(screen.queryByRole('dialog', { name: 'Command palette' })).toBeNull());
    await vi.waitFor(() => expect(document.getElementById('dl-row-2')).toBeNull());
    expect(rowOf(1)).toBeTruthy();

    // Escape closes it without running anything
    await fireEvent.keyDown(window, { key: 'k', ctrlKey: true });
    const again = await screen.findByRole('combobox', { name: 'Search commands' });
    await fireEvent.keyDown(again, { key: 'Escape' });
    await vi.waitFor(() => expect(screen.queryByRole('dialog', { name: 'Command palette' })).toBeNull());
  });
});

describe('App: downloads arriving from the browser', () => {
  it('says so in a toast that can show the download', async () => {
    const api = fakeApi([makeDownload({ id: 1 })]);
    render(App, { props: { api } });
    await vi.waitFor(() => expect(rowOf(1)).toBeTruthy());
    api.emit([{ kind: 'updated', download: makeDownload({ id: 7, filename: 'handed.zip', state: 'queued', source: 'browser' }) }]);
    const toast = await screen.findByRole('group', { name: 'Download added from the browser' });
    expect(toast.textContent).toContain('handed.zip');
    await fireEvent.click(within(toast).getByRole('button', { name: 'Show' }));
    await vi.waitFor(() => expect(rowOf(7).getAttribute('aria-selected')).toBe('true'));
    expect(screen.queryByRole('group', { name: 'Download added from the browser' })).toBeNull();
  });

  it('pauses a download from its row hover action', async () => {
    const api = fakeApi([makeDownload({ id: 1, state: 'downloading', speedBps: MiB, received: MiB })]);
    render(App, { props: { api } });
    await vi.waitFor(() => expect(rowOf(1)).toBeTruthy());
    const pause = rowOf(1).querySelector<HTMLButtonElement>('[data-row-action] button[title="Pause"]');
    expect(pause).toBeTruthy();
    await fireEvent.click(pause!);
    await vi.waitFor(() => expect(api.args.get('pause')).toEqual([1]));
  });
});
