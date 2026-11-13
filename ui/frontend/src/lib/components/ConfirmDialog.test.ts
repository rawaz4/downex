import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { tick } from 'svelte';
import { describe, expect, it, vi } from 'vitest';
import { ApiError, DEFAULT_SETTINGS, type ConfirmOptions } from '../api';
import {
  MiB,
  PAGE,
  chooseOption,
  makeDownload,
  makeMediaDownload,
  makeMediaInfo,
  makeQueue,
  makeSiteDownload,
  makeSiteMediaInfo,
  makeTrack,
  optionLabels,
  optionValues,
} from '../testing';
import ConfirmDialog from './ConfirmDialog.svelte';

/** The quality cards (a radio group, DESIGN-SYSTEM.md §3.19). */
const qualityGroup = () => screen.getByRole('radiogroup', { name: 'Video quality' });
const qualityRadios = () => within(qualityGroup()).getAllByRole('radio') as HTMLInputElement[];
const qualityRadio = (value: string) => qualityRadios().find((r) => r.value === value)!;
const checkedQuality = () => within(qualityGroup()).getByRole('radio', { checked: true }) as HTMLInputElement;
const qualityNames = () => qualityRadios().map((r) => r.getAttribute('aria-label'));
/** The video card's facts ("10:00 Up to 1080p Saved as MP4"). */
const cardFacts = () => screen.getByRole('list', { name: 'About this video' }).textContent?.replace(/\s+/g, ' ').trim();

const settings = {
  ...DEFAULT_SETTINGS,
  defaultDir: '/home/you/Downloads',
  categories: [...DEFAULT_SETTINGS.categories, { name: 'Books', extensions: ['epub'], dir: '/data/books' }],
};

function setup(
  patch: Partial<Parameters<typeof makeDownload>[0]> = {},
  onconfirmImpl?: (o: ConfirmOptions) => Promise<void>,
  opts: { requested?: boolean; guardMs?: number } = {},
) {
  const download = makeDownload({
    id: 4,
    url: 'https://media.example.org/get?id=4',
    filename: 'talk.mp4',
    dir: '/home/you/Downloads/Video',
    size: -1,
    expectedSize: 300 * MiB,
    state: 'awaiting_confirmation',
    source: 'browser',
    category: 'Video',
    ...patch,
  });
  const onconfirm = vi.fn(onconfirmImpl ?? (async () => {}));
  const oncancel = vi.fn(async () => {});
  const onclose = vi.fn();
  const pickFolder = vi.fn(async () => '/picked');
  const { requested = true, guardMs } = opts;
  render(ConfirmDialog, {
    props: { download, settings, waiting: 3, pickFolder, onconfirm, oncancel, onclose, requested, ...(guardMs !== undefined ? { guardMs } : {}) },
  });
  const name = () => screen.getByLabelText('File name') as HTMLInputElement;
  const folder = () => screen.getByLabelText('Save to folder') as HTMLInputElement;
  const category = () => screen.getByRole('combobox', { name: 'Category' }) as HTMLButtonElement;
  return { download, onconfirm, oncancel, onclose, pickFolder, name, folder, category };
}

describe('ConfirmDialog', () => {
  it('shows the proposal and starts the download as proposed', async () => {
    const { onconfirm, onclose, name, folder, category } = setup();
    await tick();
    const dialog = screen.getByRole('dialog', { name: 'Confirm download' });
    expect(dialog.textContent).toContain('https://media.example.org/get?id=4');
    expect(dialog.textContent).toContain('Size: 315 MB');
    expect(dialog.textContent).toContain('2 more downloads are waiting');
    expect(name().value).toBe('talk.mp4');
    expect(document.activeElement).toBe(name());
    expect(folder().value).toBe('/home/you/Downloads/Video');
    expect(category().value).toBe('Video');
    expect(optionLabels(category())).toEqual([
      'Video',
      'Audio',
      'Archives',
      'Documents',
      'Programs',
      'Books',
      'None',
    ]);

    await fireEvent.click(screen.getByRole('button', { name: 'Start download' }));
    await waitFor(() => expect(onclose).toHaveBeenCalledTimes(1));
    // unchanged values are left to the daemon's proposal
    expect(onconfirm).toHaveBeenCalledWith({ start: true, dir: '', filename: '' });
  });

  it('proposes the folder of the chosen category and downloads later with the edits', async () => {
    const { onconfirm, name, folder, category } = setup();
    await chooseOption(category(), 'Books');
    expect(folder().value).toBe('/data/books');
    await chooseOption(category(), 'Audio');
    expect(folder().value).toBe('/home/you/Downloads/Audio');
    await chooseOption(category(), '');
    expect(category().value).toBe('');
    expect(folder().value).toBe('/home/you/Downloads');
    await fireEvent.input(name(), { target: { value: ' keynote.mp4 ' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Download later' }));
    await waitFor(() => expect(onconfirm).toHaveBeenCalled());
    expect(onconfirm).toHaveBeenCalledWith({ start: false, dir: '/home/you/Downloads', filename: 'keynote.mp4' });
  });

  it('browses for a folder', async () => {
    const { pickFolder, folder } = setup();
    await fireEvent.click(screen.getByRole('button', { name: /Browse/ }));
    await waitFor(() => expect(folder().value).toBe('/picked'));
    expect(pickFolder).toHaveBeenCalledWith('/home/you/Downloads/Video');
  });

  it('checks the name and folder before asking the daemon', async () => {
    const { onconfirm, name, folder } = setup();
    await fireEvent.input(name(), { target: { value: 'a/b.mp4' } });
    await fireEvent.input(folder(), { target: { value: 'relative' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Start download' }));
    await tick();
    expect(name().getAttribute('aria-invalid')).toBe('true');
    expect(folder().getAttribute('aria-invalid')).toBe('true');
    expect(document.activeElement).toBe(name());
    expect(onconfirm).not.toHaveBeenCalled();
  });

  it('keeps the dialog open with the daemon error', async () => {
    const { onclose } = setup({}, async () => {
      throw new ApiError('invalid_argument', 'dir: not writable');
    });
    await fireEvent.click(screen.getByRole('button', { name: 'Start download' }));
    expect((await screen.findByRole('alert')).textContent).toBe('dir: not writable');
    expect(onclose).not.toHaveBeenCalled();
  });

  it('cancels by removing; closing leaves it waiting', async () => {
    const first = setup();
    await fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' });
    expect(first.onclose).toHaveBeenCalledTimes(1);
    expect(first.oncancel).not.toHaveBeenCalled();
    expect(first.onconfirm).not.toHaveBeenCalled();
    document.body.innerHTML = '';

    const second = setup({ expectedSize: 0, category: 'Gone' });
    expect(screen.getByRole('dialog').textContent).toContain('Size: unknown');
    expect(second.category().value).toBe(''); // a category that no longer exists
    await fireEvent.click(screen.getByRole('button', { name: 'Cancel download' }));
    await waitFor(() => expect(second.onclose).toHaveBeenCalledTimes(1));
    expect(second.oncancel).toHaveBeenCalledTimes(1);
  });

  it('leaves an unnamed download to the server instead of showing a name that is not used', async () => {
    // DESKTOP-SPEC §1: no proposal = the server's answer names the file.
    const { onconfirm, onclose, name } = setup({ filename: '', url: 'https://media.example.org/get?id=4' });
    expect(name().value).toBe('');
    expect(name().placeholder).toBe('Named by the server');
    await fireEvent.click(screen.getByRole('button', { name: 'Start download' }));
    await waitFor(() => expect(onclose).toHaveBeenCalledTimes(1));
    expect(name().getAttribute('aria-invalid')).toBeNull();
    expect(onconfirm).toHaveBeenCalledWith({ start: true, dir: '', filename: '' });
  });

  it('never proposes the whole address as the name (path "/")', async () => {
    const { onconfirm, name } = setup({ filename: '', url: 'https://example.com/?file=7' });
    expect(name().value).toBe('');
    await fireEvent.click(screen.getByRole('button', { name: 'Download later' }));
    await waitFor(() => expect(onconfirm).toHaveBeenCalledWith({ start: false, dir: '', filename: '' }));
    expect(screen.queryByText(/cannot contain/)).toBeNull();
  });

  it('accepts a cleared name: the daemon keeps its proposal', async () => {
    const { onconfirm, name } = setup();
    await fireEvent.input(name(), { target: { value: '  ' } });
    expect(name().placeholder).toBe('talk.mp4');
    await fireEvent.click(screen.getByRole('button', { name: 'Start download' }));
    await waitFor(() => expect(onconfirm).toHaveBeenCalledWith({ start: true, dir: '', filename: '' }));
    expect(screen.queryByText('Enter a file name')).toBeNull();
  });

  it('opened by itself, keeps typing out of the name and ignores activation at first', async () => {
    // The guard reads performance.now(): the test moves that clock itself, so a slow or busy
    // machine cannot carry the first clicks past the guard.
    let now = 1000;
    vi.spyOn(performance, 'now').mockImplementation(() => now);
    const { onconfirm, oncancel, name } = setup({}, undefined, { requested: false, guardMs: 80 });
    await tick();
    const dialog = screen.getByRole('dialog', { name: 'Confirm download' });
    // focus is on the dialog, not in the form whose Enter starts the download
    expect(document.activeElement).toBe(dialog);
    expect(name().value).toBe('talk.mp4');

    // Enter in the form, and clicks that land right after it appeared, do nothing
    await fireEvent.submit(name().form!);
    await fireEvent.click(screen.getByRole('button', { name: 'Start download' }));
    await fireEvent.click(screen.getByRole('button', { name: 'Download later' }));
    await fireEvent.click(screen.getByRole('button', { name: 'Cancel download' }));
    await tick();
    expect(onconfirm).not.toHaveBeenCalled();
    expect(oncancel).not.toHaveBeenCalled();

    now += 100;
    await fireEvent.click(screen.getByRole('button', { name: 'Start download' }));
    await waitFor(() => expect(onconfirm).toHaveBeenCalledWith({ start: true, dir: '', filename: '' }));
  });

  it('asked for by the user, puts the cursor in the name and acts at once', async () => {
    const { onconfirm, name } = setup({}, undefined, { requested: true });
    await tick();
    expect(document.activeElement).toBe(name());
    await fireEvent.submit(name().form!);
    await waitFor(() => expect(onconfirm).toHaveBeenCalledTimes(1));
  });
});

describe('ConfirmDialog: media downloads', () => {
  function setupMedia(patch: Partial<Parameters<typeof makeMediaDownload>[0]> = {}) {
    const download = makeMediaDownload({
      id: 9,
      dir: '/home/you/Downloads/Video',
      state: 'awaiting_confirmation',
      source: 'browser',
      ...patch,
    });
    const onconfirm = vi.fn(async (_o: ConfirmOptions) => {});
    const onclose = vi.fn();
    render(ConfirmDialog, {
      props: { download, settings, pickFolder: vi.fn(async () => null), onconfirm, oncancel: vi.fn(async () => {}), onclose, requested: true },
    });
    const audio = () => screen.queryByRole('combobox', { name: 'Audio track' }) as HTMLButtonElement | null;
    return { onconfirm, onclose, audio, download };
  }

  it('shows a quality picker with the daemon pick and sends the chosen tracks', async () => {
    const { onconfirm, onclose, audio, download } = setupMedia();
    await tick();
    const dialog = screen.getByRole('dialog', { name: 'Confirm download' });
    expect(dialog.textContent).toContain('Choose its quality');
    expect(screen.getByRole('group', { name: 'Quality' })).toBeTruthy();
    // one card per class, best first, with its codec and the size of the file it makes
    expect(qualityRadios().map((r) => r.value)).toEqual(['v1', 'v2', 'v0']);
    expect(qualityNames()[0]).toBe('1080p (Full HD) · H.264 · ~385 MB');
    // The tooltip: the whole quality name (cut in a narrow column), its picture and bitrate.
    expect(qualityRadio('v1').closest('label')?.title).toBe('1080p (Full HD) · 1920×1080 · 29.97 fps · 5.0 Mbit/s');
    expect(checkedQuality().value).toBe('v1');
    expect(optionValues(audio()!)).toEqual(['r0', 'r1']);
    expect(optionLabels(audio()!)).toEqual(['English', 'Deutsch (German)']);
    expect(audio()!.value).toBe('r0');
    // the size is estimated from the bitrate and length (5.128 Mbit/s × 10 min)
    expect(dialog.textContent).toContain('Size: about 385 MB');
    // the video card: a stream, its address (no field of its own), its length, best class and
    // the MP4 it becomes
    expect(dialog.querySelector('.card')?.textContent).toContain('HLS stream ·');
    const address = within(dialog.querySelector<HTMLElement>('.card')!).getByRole('textbox', { name: 'Address' }) as HTMLInputElement;
    expect(address.value).toBe(download.url);
    expect(address.readOnly).toBe(true);
    expect(screen.queryByText('Address')).toBeNull();
    expect(cardFacts()).toBe('10:00 Up to 1080p Saved as MP4');

    await chooseOption(audio()!, 'r1');
    await fireEvent.click(qualityRadio('v0'));
    await fireEvent.click(screen.getByRole('button', { name: 'Start download' }));
    await waitFor(() => expect(onclose).toHaveBeenCalledTimes(1));
    expect(onconfirm).toHaveBeenCalledWith({ start: true, dir: '', filename: '', videoTrack: 'v0', audioTrack: 'r1' });
  });

  it('shows the size downexd measured of a bare media playlist, its picture and name', async () => {
    const media = makeMediaInfo({
      manifestUrl: 'https://moon.example/vd/abc/index-s1080p-v1-a1.m3u8',
      durationMs: 3_907_000,
      selectedVideo: 'v0',
      selectedAudio: '',
      tracks: [
        makeTrack({ id: 'v0', kind: 'muxed', width: 1920, height: 1080, frameRate: 25, codecs: 'avc1.640028,mp4a.40.2', name: '1080p (Full HD)', size: 2150 * MiB, sizeApprox: true }),
      ],
    });
    setupMedia({ media });
    await tick();
    const dialog = screen.getByRole('dialog', { name: 'Confirm download' });
    expect(dialog.textContent).toContain('Size: about 2.3 GB');
    expect(qualityNames()).toEqual(['1080p (Full HD) · H.264 + AAC · ~2.3 GB']);
  });

  it('hides the audio picker for a muxed video track', async () => {
    const { onconfirm, audio } = setupMedia();
    await fireEvent.click(qualityRadio('v2'));
    expect(audio()).toBeNull();
    expect(screen.getByText('This video quality includes its sound.')).toBeTruthy();
    await fireEvent.click(screen.getByRole('button', { name: 'Download later' }));
    await waitFor(() => expect(onconfirm).toHaveBeenCalledWith({ start: false, dir: '', filename: '', videoTrack: 'v2', audioTrack: '' }));
    // going back to a separate video track brings the audio picker back
    await fireEvent.click(qualityRadio('v1'));
    expect(audio()?.value).toBe('r0');
  });

  it('starts from a muxed default pick without an audio picker', async () => {
    const { audio } = setupMedia({ media: makeMediaInfo({ selectedVideo: 'v2', selectedAudio: '' }) });
    expect(checkedQuality().value).toBe('v2');
    expect(audio()).toBeNull();
  });

  it("keeps the daemon's audio-only pick of a stream that also lists videos", async () => {
    // e.g. DASH whose video Representations Downex cannot assemble (WebM, SegmentBase)
    const { onconfirm, audio } = setupMedia({ media: makeMediaInfo({ selectedVideo: '', selectedAudio: 'r1' }) });
    expect(checkedQuality().value).toBe('');
    expect(qualityRadios().map((r) => r.value)).toEqual(['v1', 'v2', 'v0', '']);
    expect(qualityNames()[3]).toBe('No video (audio only)');
    expect(audio()?.value).toBe('r1');
    expect(screen.getByRole('dialog').textContent).toContain('Size: about 7.2 MB');
    // a video quality and back to none
    await fireEvent.click(qualityRadio('v1'));
    expect(audio()?.value).toBe('r1');
    await fireEvent.click(qualityRadio(''));
    expect(checkedQuality().value).toBe('');
    expect(audio()?.value).toBe('r1');
    await fireEvent.click(screen.getByRole('button', { name: 'Start download' }));
    await waitFor(() => expect(onconfirm).toHaveBeenCalledWith({ start: true, dir: '', filename: '', videoTrack: '', audioTrack: 'r1' }));
  });

  it('offers only audio for an audio-only stream', async () => {
    const tracks = makeMediaInfo().tracks.filter((t) => t.kind === 'audio');
    const { onconfirm, audio } = setupMedia({ media: makeMediaInfo({ tracks, selectedVideo: '', selectedAudio: 'r1' }) });
    expect(screen.queryByRole('radiogroup', { name: 'Video quality' })).toBeNull();
    expect(audio()?.value).toBe('r1');
    await fireEvent.click(screen.getByRole('button', { name: 'Start download' }));
    await waitFor(() => expect(onconfirm).toHaveBeenCalledWith({ start: true, dir: '', filename: '', videoTrack: '', audioTrack: 'r1' }));
  });

  it('keeps file downloads without a picker or tracks', async () => {
    render(ConfirmDialog, {
      props: {
        download: makeDownload({ id: 3, state: 'awaiting_confirmation', filename: 'a.zip' }),
        settings,
        pickFolder: vi.fn(async () => null),
        onconfirm: vi.fn(async () => {}),
        oncancel: vi.fn(async () => {}),
        onclose: vi.fn(),
        requested: true,
      },
    });
    expect(screen.queryByRole('group', { name: 'Quality' })).toBeNull();
  });
});

describe('ConfirmDialog: video pages (SITE-MEDIA-SPEC §5.4)', () => {
  function setupSite(patch: Partial<Parameters<typeof makeSiteDownload>[0]> = {}) {
    const onconfirm = vi.fn(async (_o: ConfirmOptions) => {});
    const onclose = vi.fn();
    render(ConfirmDialog, {
      props: {
        download: makeSiteDownload({ id: 11, dir: '/home/you/Downloads/Video', state: 'awaiting_confirmation', source: 'browser', ...patch }),
        settings,
        pickFolder: vi.fn(async () => null),
        onconfirm,
        oncancel: vi.fn(async () => {}),
        onclose,
        requested: true,
      },
    });
    const name = () => screen.getByLabelText('File name') as HTMLInputElement;
    return { onconfirm, onclose, name };
  }

  it('shows the page, its title and site, and every MP4 quality with its size', async () => {
    setupSite();
    await tick();
    const dialog = screen.getByRole('dialog', { name: 'Confirm download' });
    expect(dialog.textContent).toContain('A video from the browser is waiting. Choose its quality');
    // The page's address is the card's, after the site's name (its host is not repeated): a
    // read-only field named "Page", so that it can be reached with Tab, selected and copied,
    // with the whole address as its tooltip. There is no "Page" field of its own.
    const card = dialog.querySelector<HTMLElement>('.card')!;
    const page = within(card).getByRole('textbox', { name: 'Page' }) as HTMLInputElement;
    expect(page.value).toBe(PAGE);
    expect(page.readOnly).toBe(true);
    expect(page.title).toBe(PAGE);
    expect(page.tabIndex).toBe(0);
    expect(screen.queryByText('Page')).toBeNull();
    expect(screen.queryByText('Address')).toBeNull();
    expect(screen.getByText('A talk')).toBeTruthy();
    // The video card names the site, and the facts that are true here: a browser hand-off may
    // carry cookies, so it never says "No cookies sent".
    expect(card.textContent).toContain('YouTube ·');
    expect(card.textContent).not.toContain('youtube.com');
    expect(cardFacts()).toBe('10:00 Up to 1080p Saved as MP4');
    expect(screen.queryByText('No cookies sent')).toBeNull();
    // Each quality with the size of the file it makes (the video with its audio).
    expect(qualityNames()).toEqual([
      '1080p (Full HD) 60fps · H.264 · 482 MB',
      '720p (HD) · H.264 · 126 MB',
      '480p · AV1 · ~168 MB',
      '360p · H.264 + AAC · ~45.0 MB',
    ]);
    expect(checkedQuality().value).toBe('299');
    expect(dialog.textContent).toContain('Size: 482 MB · saved as MP4');
    await fireEvent.click(qualityRadio('397'));
    expect(dialog.textContent).toContain('Size: ~168 MB · saved as MP4');
  });

  it('confirms another quality under the same .mp4 name; a new name keeps the extension', async () => {
    const { onconfirm, onclose, name } = setupSite();
    expect(name().value).toBe('A talk.mp4');
    await fireEvent.click(qualityRadio('136'));
    await fireEvent.click(screen.getByRole('button', { name: 'Start download' }));
    await waitFor(() => expect(onclose).toHaveBeenCalledTimes(1));
    expect(onconfirm).toHaveBeenCalledWith({ start: true, dir: '', filename: '', videoTrack: '136', audioTrack: '140' });
  });

  it('adds .mp4 to a name typed without it', async () => {
    const { onconfirm, name } = setupSite();
    await fireEvent.input(name(), { target: { value: ' The talk ' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Download later' }));
    await waitFor(() => expect(onconfirm).toHaveBeenCalled());
    expect(onconfirm.mock.calls[0]?.[0]).toMatchObject({ start: false, filename: 'The talk.mp4' });
  });

  it('keeps a typed .MP4 name as it is', async () => {
    const { onconfirm, name } = setupSite();
    await fireEvent.input(name(), { target: { value: 'Talk.MP4' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Start download' }));
    await waitFor(() => expect(onconfirm).toHaveBeenCalled());
    expect(onconfirm.mock.calls[0]?.[0]).toMatchObject({ filename: 'Talk.MP4' });
  });

  it('shows the one audio track instead of a choice', async () => {
    const info = makeSiteMediaInfo();
    setupSite({ media: { ...info, tracks: info.tracks.filter((t) => t.id !== '140-1') } });
    expect(screen.queryByLabelText('Audio track')).toBeNull();
    // The one track is shown as it is (the full label in its tooltip).
    const sound = screen.getByText('English original').closest<HTMLElement>('.sound')!;
    expect(sound.textContent).toBe('English original · AAC 129 kbit/s');
    expect(sound.title).toBe('English original · AAC · 129 kbit/s · 10.5 MB');
  });
});

describe('ConfirmDialog: queues (M6)', () => {
  function setupQueues(patch: Partial<Parameters<typeof makeDownload>[0]> = {}, queues = [makeQueue({ id: 1 }), makeQueue({ id: 2, name: 'Night' })]) {
    const download = makeDownload({ id: 4, filename: 'talk.mp4', dir: '/home/you/Downloads', state: 'awaiting_confirmation', ...patch });
    const calls: string[] = [];
    const onconfirm = vi.fn(async (_o: ConfirmOptions) => {
      calls.push('confirm');
    });
    const onmove = vi.fn(async (_queueId: number) => {
      calls.push('move');
    });
    render(ConfirmDialog, {
      props: { download, settings, pickFolder: async () => null, onconfirm, onmove, oncancel: async () => {}, onclose: () => {}, requested: true, queues },
    });
    return { onconfirm, onmove, calls, queue: () => screen.getByRole('combobox', { name: 'Queue' }) as HTMLButtonElement };
  }

  it('keeps a download for later paused in a running queue, as the hint says', async () => {
    const { onconfirm, onmove, calls, queue } = setupQueues({}, [makeQueue({ id: 1 }), makeQueue({ id: 3, name: 'Daytime', running: true })]);
    await chooseOption(queue(), 3);
    expect(screen.getByText(/Download later keeps it paused/)).toBeTruthy();
    await fireEvent.click(screen.getByRole('button', { name: 'Download later' }));
    await waitFor(() => expect(onconfirm).toHaveBeenCalled());
    // A queue_id would queue it there, and a running queue would start it at once.
    expect(onconfirm).toHaveBeenCalledWith({ start: false, dir: '', filename: '' });
    expect(onmove).toHaveBeenCalledWith(3);
    expect(calls).toEqual(['move', 'confirm']);
  });

  it('puts a download for later into its own queue when that queue is stopped', async () => {
    const { onconfirm, onmove, queue } = setupQueues({}, [makeQueue({ id: 1, running: false }), makeQueue({ id: 2, name: 'Night' })]);
    expect(queue().value).toBe('1');
    expect(screen.getByText(/starts when the queue runs/)).toBeTruthy();
    await fireEvent.click(screen.getByRole('button', { name: 'Download later' }));
    await waitFor(() => expect(onconfirm).toHaveBeenCalledWith({ start: false, dir: '', filename: '', queueId: 1 }));
    expect(onmove).not.toHaveBeenCalled();
  });

  it('starts a download in another running queue without moving it first', async () => {
    const { onconfirm, onmove, queue } = setupQueues({}, [makeQueue({ id: 1 }), makeQueue({ id: 3, name: 'Daytime', running: true })]);
    await chooseOption(queue(), 3);
    await fireEvent.click(screen.getByRole('button', { name: 'Start download' }));
    await waitFor(() => expect(onconfirm).toHaveBeenCalledWith({ start: true, dir: '', filename: '', queueId: 3 }));
    expect(onmove).not.toHaveBeenCalled();
  });

  it('reports a move that fails and confirms nothing', async () => {
    const { onconfirm, onmove, queue } = setupQueues({}, [makeQueue({ id: 1 }), makeQueue({ id: 3, name: 'Daytime', running: true })]);
    onmove.mockRejectedValueOnce(new Error('not_found: queue 3 not found'));
    await chooseOption(queue(), 3);
    await fireEvent.click(screen.getByRole('button', { name: 'Download later' }));
    await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('queue 3 not found'));
    expect(onconfirm).not.toHaveBeenCalled();
  });

  it('puts a download for later into the chosen queue', async () => {
    const { onconfirm, queue } = setupQueues();
    expect(queue().value).toBe('1');
    expect(optionLabels(queue())).toEqual(['Main queue', 'Night (stopped)']);
    await chooseOption(queue(), 2);
    expect(screen.getByText(/starts when the queue runs/)).toBeTruthy();
    await fireEvent.click(screen.getByRole('button', { name: 'Download later' }));
    await waitFor(() => expect(onconfirm).toHaveBeenCalled());
    expect(onconfirm).toHaveBeenCalledWith({ start: false, dir: '', filename: '', queueId: 2 });
  });

  it('keeps the queue it is in unless another is chosen', async () => {
    const { onconfirm, queue } = setupQueues({ queueId: 2 });
    expect(queue().value).toBe('2');
    await fireEvent.click(screen.getByRole('button', { name: 'Start download' }));
    await waitFor(() => expect(onconfirm).toHaveBeenCalledWith({ start: true, dir: '', filename: '' }));
  });

  it('offers no queue without queues', async () => {
    setup();
    await tick();
    expect(screen.queryByLabelText('Queue')).toBeNull();
  });
});

describe('ConfirmDialog: the Studio layout (DESIGN-SYSTEM.md §4.7)', () => {
  it('heads the dialog with the tile of its file kind', async () => {
    setup({ filename: 'talk.mp4' });
    await tick();
    const dialog = screen.getByRole('dialog', { name: 'Confirm download' });
    expect(dialog.querySelector('header svg.tile')?.getAttribute('data-kind')).toBe('video');
    document.body.innerHTML = '';
    setup({ filename: 'setup-x64.exe', url: 'https://downloads.example.com/setup-x64.exe' });
    await tick();
    expect(screen.getByRole('dialog').querySelector('header svg.tile')?.getAttribute('data-kind')).toBe('programs');
  });

  it('chooses a category from the keyboard, which proposes its folder', async () => {
    const { category, folder, onclose } = setup();
    const trigger = category();
    trigger.focus();
    await fireEvent.keyDown(trigger, { key: 'ArrowDown' });
    expect(trigger.getAttribute('aria-expanded')).toBe('true');
    // Video → Audio
    await fireEvent.keyDown(trigger, { key: 'ArrowDown' });
    await fireEvent.keyDown(trigger, { key: 'Enter' });
    expect(trigger.getAttribute('aria-expanded')).toBe('false');
    expect(trigger.value).toBe('Audio');
    expect(folder().value).toBe('/home/you/Downloads/Audio');
    // Escape closes the list without a change, and leaves the dialog open
    await fireEvent.keyDown(trigger, { key: 'ArrowDown' });
    await fireEvent.keyDown(trigger, { key: 'End' });
    await fireEvent.keyDown(trigger, { key: 'Escape' });
    expect(trigger.value).toBe('Audio');
    expect(trigger.getAttribute('aria-expanded')).toBe('false');
    expect(onclose).not.toHaveBeenCalled();
    expect(screen.getByRole('dialog', { name: 'Confirm download' })).toBeTruthy();
    // closed, Escape is the dialog's again
    await fireEvent.keyDown(trigger, { key: 'Escape' });
    expect(onclose).toHaveBeenCalledTimes(1);
  });

  it('moves between quality cards like one radio group', async () => {
    const onconfirm = vi.fn(async (_o: ConfirmOptions) => {});
    render(ConfirmDialog, {
      props: {
        download: makeSiteDownload({ id: 11, dir: '/home/you/Downloads/Video', state: 'awaiting_confirmation', source: 'browser' }),
        settings,
        pickFolder: vi.fn(async () => null),
        onconfirm,
        oncancel: vi.fn(async () => {}),
        onclose: vi.fn(),
        requested: true,
      },
    });
    // one group (the browser's arrow keys move within it), every card reachable
    const radios = qualityRadios();
    expect(new Set(radios.map((r) => r.name)).size).toBe(1);
    expect(radios.every((r) => r.tabIndex === 0 && !r.disabled)).toBe(true);
    // the chosen card is Mint, with its tick
    const on = checkedQuality().closest('label')!;
    expect(on.classList.contains('on')).toBe(true);
    expect(on.querySelector('.tick')).toBeTruthy();
    // Space (the browser's click) on another card chooses it
    qualityRadio('136').focus();
    await fireEvent.click(qualityRadio('136'));
    expect(checkedQuality().value).toBe('136');
    expect(on.classList.contains('on')).toBe(false);
    await fireEvent.click(screen.getByRole('button', { name: 'Start download' }));
    await waitFor(() => expect(onconfirm).toHaveBeenCalledWith(expect.objectContaining({ videoTrack: '136', audioTrack: '140' })));
  });
});

describe('ConfirmDialog: the fit rule (DESIGN-SYSTEM.md §3.9, §4.7; layout in scripts/measure-dialogs.mjs)', () => {
  function renderSite(waiting = 1) {
    render(ConfirmDialog, {
      props: {
        download: makeSiteDownload({ id: 11, dir: '/home/you/Downloads/Video', state: 'awaiting_confirmation', source: 'browser' }),
        settings,
        waiting,
        pickFolder: vi.fn(async () => null),
        onconfirm: vi.fn(async () => {}),
        oncancel: vi.fn(async () => {}),
        onclose: vi.fn(),
        requested: true,
      },
    });
    return screen.getByRole('dialog', { name: 'Confirm download' });
  }

  it('widens a video to 56rem and gives its qualities a column before the fields, in Tab order', () => {
    const dialog = renderSite();
    expect(dialog.style.inlineSize).toMatch(/^min\(56rem, /);
    const form = dialog.querySelector('.form.split')!;
    expect(form).toBeTruthy();
    const parts = [...form.children].map((el) => el.className.split(' ')[0]);
    expect(parts).toEqual(['card-area', 'quality-area', 'fields']);
    // Tab: the card's address, the qualities, the audio track, then the file name and the folder
    const order = [...dialog.querySelectorAll<HTMLElement>('input, button')].filter((el) => !el.closest('header, footer'));
    const at = (el: Element | null) => order.indexOf(el as HTMLElement);
    expect(at(screen.getByRole('textbox', { name: 'Page' }))).toBeLessThan(at(qualityRadios()[0]!));
    expect(at(qualityRadios().at(-1)!)).toBeLessThan(at(screen.getByRole('combobox', { name: 'Audio track' })));
    expect(at(screen.getByRole('combobox', { name: 'Audio track' }))).toBeLessThan(at(screen.getByLabelText('File name')));
    expect(at(screen.getByLabelText('File name'))).toBeLessThan(at(screen.getByLabelText('Save to folder')));
  });

  it('keeps a file in one column at 44rem, with its address as a field', () => {
    setup();
    const dialog = screen.getByRole('dialog', { name: 'Confirm download' });
    expect(dialog.style.inlineSize).toMatch(/^min\(44rem, /);
    expect(dialog.querySelector('.split')).toBeNull();
    expect(screen.getByText('Address')).toBeTruthy();
  });

  it('puts the description beside the icon, under the title, and the waiting count in the header', () => {
    const dialog = renderSite(3);
    const header = dialog.querySelector('header')!;
    const desc = document.getElementById(dialog.getAttribute('aria-describedby')!)!;
    expect(header.contains(desc)).toBe(true);
    expect(desc.textContent).toContain('A video from the browser is waiting.');
    const chip = within(header).getByText('2 more waiting').closest<HTMLElement>('.chip')!;
    expect(chip.title).toBe('2 more downloads are waiting for confirmation.');
    expect(chip.textContent).toContain('2 more downloads are waiting for confirmation.');
    expect(dialog.querySelector('.body')?.textContent).not.toContain('waiting for confirmation');
  });

  it('says the category proposes the folder in the folder’s label row, still describing the category', () => {
    setup();
    const category = screen.getByRole('combobox', { name: 'Category' });
    const hint = document.getElementById(category.getAttribute('aria-describedby')!)!;
    expect(hint.textContent).toBe('Choosing a category proposes its folder.');
    expect(hint.closest('.label-row')?.querySelector('label')?.textContent).toContain('Save to');
  });

  it('shows the duration once (a fact), not also on the poster', () => {
    renderSite();
    expect(cardFacts()).toBe('10:00 Up to 1080p Saved as MP4');
    expect(document.querySelector('.poster')?.textContent?.trim()).toBe('');
  });
});

describe('ConfirmDialog: a right-to-left title', () => {
  it('takes its direction from the title itself, so an Arabic title is right-aligned', () => {
    const title = 'أطيب كباب في السليمانية_ أكلات شعبية وخبز التنور الحار';
    render(ConfirmDialog, {
      props: {
        download: makeSiteDownload({ id: 12, state: 'awaiting_confirmation', source: 'browser', media: { ...makeSiteMediaInfo(), title } }),
        settings,
        pickFolder: vi.fn(async () => null),
        onconfirm: vi.fn(async () => {}),
        oncancel: vi.fn(async () => {}),
        onclose: vi.fn(),
        requested: true,
      },
    });
    const el = screen.getByText(title);
    // dir="auto" on the block that holds the text (a <bdi> inside would hide it from dir="auto")
    expect(el.classList.contains('title')).toBe(true);
    expect(el.getAttribute('dir')).toBe('auto');
  });
});

describe('ConfirmDialog: review of the fit change (2026-10-29)', () => {
  const props = (download: ReturnType<typeof makeDownload>, extra: Record<string, unknown> = {}) => ({
    download,
    settings,
    pickFolder: vi.fn(async () => null),
    onconfirm: vi.fn(async () => {}),
    oncancel: vi.fn(async () => {}),
    onclose: vi.fn(),
    requested: true,
    ...extra,
  });
  const queues = [makeQueue({ id: 1 }), makeQueue({ id: 2, name: 'Night' })];
  /** The form's controls in Tab order (the card's address, the qualities and the footer left out). */
  const tabOrder = (dialog: HTMLElement) =>
    [...dialog.querySelectorAll<HTMLElement>('.fields input, .fields button, .form > .field input, .form > .field button')].map(
      (el) => el.getAttribute('aria-label') || (el as HTMLInputElement).labels?.[0]?.textContent?.trim() || el.textContent?.trim(),
    );

  it('keeps an audio-only stream in one column at 44rem: it has no quality cards', () => {
    const tracks = makeMediaInfo().tracks.filter((t) => t.kind === 'audio');
    render(ConfirmDialog, {
      props: props(
        makeMediaDownload({ id: 9, state: 'awaiting_confirmation', source: 'browser', media: makeMediaInfo({ tracks, selectedVideo: '', selectedAudio: 'r1' }) }),
      ),
    });
    const dialog = screen.getByRole('dialog', { name: 'Confirm download' });
    expect(dialog.style.inlineSize).toMatch(/^min\(44rem, /);
    expect(dialog.querySelector('.split')).toBeNull();
    expect(dialog.querySelector('.quality-area')).toBeNull();
    expect(screen.getByRole('combobox', { name: 'Audio track' })).toBeTruthy();
  });

  it('gives a video’s folder a row of its own after the category and the queue, in Tab order', () => {
    render(ConfirmDialog, {
      props: props(makeSiteDownload({ id: 11, dir: '/home/you/Downloads/Video', state: 'awaiting_confirmation', source: 'browser' }), {
        queues,
        onmove: vi.fn(async () => {}),
      }),
    });
    const dialog = screen.getByRole('dialog', { name: 'Confirm download' });
    expect(tabOrder(dialog)).toEqual(['Audio track', 'File name', 'Category', 'Queue', 'Save to folder', 'Browse…']);
    const dir = screen.getByLabelText('Save to folder');
    // the folder is not in a row beside the category (it would show 22 characters of it)
    const row = dir.closest('.field')!;
    expect(row.contains(screen.getByRole('combobox', { name: 'Category' }))).toBe(false);
    expect(row.classList.contains('dir')).toBe(true);
    // the queue's hint is the row's, under both selects
    const hint = document.getElementById(screen.getByRole('combobox', { name: 'Queue' }).getAttribute('aria-describedby')!)!;
    expect(hint.closest('.field')).toBeNull();
  });

  it('shows the whole folder as its field’s tooltip', async () => {
    setup();
    const dir = screen.getByLabelText('Save to folder') as HTMLInputElement;
    expect(dir.title).toBe('/home/you/Downloads/Video');
    await fireEvent.input(dir, { target: { value: '/data/media/very/long/folder' } });
    expect(dir.title).toBe('/data/media/very/long/folder');
  });

  it('keeps a file’s category beside its folder without queues, and puts both selects first with them', () => {
    render(ConfirmDialog, { props: props(makeDownload({ id: 4, filename: 'a.zip', dir: '/home/you/Downloads', state: 'awaiting_confirmation' })) });
    const dialog = screen.getByRole('dialog', { name: 'Confirm download' });
    expect(tabOrder(dialog)).toEqual(['File name', 'Category', 'Save to folder', 'Browse…']);
  });

  it('clamps a long file address to a few lines, the whole address as its tooltip, without a scroller of its own', () => {
    const url = `https://storage.example-cdn.net/uploads/budget-plan.xlsx?X-Amz-Signature=${'a3f9c27e'.repeat(60)}`;
    render(ConfirmDialog, { props: props(makeDownload({ id: 4, url, filename: 'budget-plan.xlsx', state: 'awaiting_confirmation' })) });
    const shown = document.querySelector<HTMLElement>('p.url')!;
    expect(shown.textContent).toBe(url);
    expect(shown.title).toBe(url);
    expect(shown.querySelector('.clamp')?.textContent).toBe(url);
  });

  it('has the whole title as the tooltip of the title (clamped to two lines), over the card’s picture', () => {
    const title = 'How We Moved Forty Services From a Single VPS to a Staging and Production Setup With Dokploy, Traefik and Postgres Replicas';
    render(ConfirmDialog, {
      props: props(makeSiteDownload({ id: 12, state: 'awaiting_confirmation', source: 'browser', media: { ...makeSiteMediaInfo(), title } })),
    });
    const el = screen.getByText(title);
    expect(el.title).toBe(title);
    expect(el.closest<HTMLElement>('.card')!.title).toBe('1920×1080');
  });

  it('shows a page address with a bidi control escaped, in the field and its tooltip', async () => {
    const RLO = '‮';
    const url = `https://www.youtube.com/watch?v=Xb7mQ2kLp9w&t=${RLO}4pm.exe`;
    render(ConfirmDialog, { props: props(makeSiteDownload({ id: 7, url, state: 'awaiting_confirmation', source: 'browser' })) });
    await tick();
    const page = screen.getByRole('textbox', { name: 'Page' }) as HTMLInputElement;
    expect(page.value).not.toContain(RLO);
    expect(page.value).toContain('⟨U+202E⟩');
    expect(page.title).toBe(page.value);
  });
});
