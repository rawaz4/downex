import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { tick } from 'svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiError, type AddMediaRequest, type AddRequest, type MediaInfo, type ProbeMediaRequest, type ToolsStatus } from '../api';
import { REFUSALS, SITE_TEXTS } from '../media';
import type { AddOutcome } from '../stores/downloads.svelte';
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
  makeTools,
  optionLabels,
  optionValues,
} from '../testing';
import AddDialog from './AddDialog.svelte';

/** The quality cards (a radio group, DESIGN-SYSTEM.md §3.19). */
const qualityGroup = () => screen.getByRole('radiogroup', { name: 'Video quality' });
const qualityRadios = () => within(qualityGroup()).getAllByRole('radio') as HTMLInputElement[];
const qualityRadio = (value: string) => qualityRadios().find((r) => r.value === value)!;
const checkedQuality = () => within(qualityGroup()).getByRole('radio', { checked: true }) as HTMLInputElement;
/** Each card's value and accessible name, in order. */
const qualities = () => qualityRadios().map((r) => [r.value, r.getAttribute('aria-label')]);
const combobox = (name: string) => screen.getByRole('combobox', { name }) as HTMLButtonElement;
const openMore = () => fireEvent.click(screen.getByRole('button', { name: 'More options' }));
/** The footer's summary (the dialog's footerStart). */
const footerSummary = () => document.querySelector('.foot .summary')?.textContent?.replace(/\s+/g, ' ').trim() ?? '';

afterEach(() => {
  vi.useRealTimers();
});

function setup(result?: (reqs: AddRequest[]) => AddOutcome[]) {
  const submit = vi.fn(async (reqs: AddRequest[]): Promise<AddOutcome[]> =>
    result
      ? result(reqs)
      : reqs.map((r, i) => ({ url: r.url, ok: true, download: makeDownload({ id: i + 1, url: r.url }) })),
  );
  const onclose = vi.fn();
  const pickFolder = vi.fn(async () => '/picked/folder');
  const utils = render(AddDialog, {
    props: {
      defaults: { dir: '/home/you/Downloads', maxConnections: 8 },
      pickFolder,
      submit,
      onclose,
    },
  });
  const urls = screen.getByLabelText('URLs') as HTMLTextAreaElement;
  const addButton = () => screen.getByRole('button', { name: /^Add/ });
  return { ...utils, submit, onclose, pickFolder, urls, addButton };
}

describe('AddDialog', () => {
  it('is a labelled modal dialog that focuses the URL field', async () => {
    const { urls } = setup();
    await tick();
    const dialog = screen.getByRole('dialog', { name: 'Add downloads' });
    expect(dialog.getAttribute('aria-modal')).toBe('true');
    expect(document.activeElement).toBe(urls);
    expect((screen.getByLabelText('Connections') as HTMLInputElement).value).toBe('8');
  });

  it('requires a URL and does not submit an empty form', async () => {
    const { urls, addButton, submit } = setup();
    await fireEvent.click(addButton());
    await tick();
    expect(screen.getByText('Enter at least one URL')).toBeTruthy();
    expect(urls.getAttribute('aria-invalid')).toBe('true');
    expect(urls.getAttribute('aria-describedby')).toContain('-error');
    expect(document.activeElement).toBe(urls);
    expect(submit).not.toHaveBeenCalled();
  });

  it('rejects non-http URLs and out-of-range connections', async () => {
    const { urls, addButton, submit } = setup();
    await fireEvent.input(urls, { target: { value: 'ftp://files.example.com/a.zip' } });
    await fireEvent.input(screen.getByLabelText('Connections'), { target: { value: '40' } });
    await fireEvent.click(addButton());
    expect(screen.getByText(/must start with http/)).toBeTruthy();
    expect(screen.getByText(/Connections must be from 1 to 32/)).toBeTruthy();
    expect(submit).not.toHaveBeenCalled();
    // errors clear live once the input is fixed
    await fireEvent.input(urls, { target: { value: 'https://files.example.com/a.zip' } });
    expect(screen.queryByText(/must start with http/)).toBeNull();
  });

  it('submits one request per URL with the chosen options and closes', async () => {
    const { urls, submit, onclose } = setup();
    await fireEvent.input(urls, {
      target: { value: 'https://files.example.com/a.iso\nhttps://files.example.com/b.iso\n' },
    });
    await openMore();
    const filename = screen.getByLabelText('File name') as HTMLInputElement;
    expect(filename.disabled).toBe(true);
    await fireEvent.input(screen.getByLabelText('Connections'), { target: { value: '4' } });
    await fireEvent.input(screen.getByLabelText('Speed limit'), { target: { value: '2' } });
    await chooseOption(combobox('Speed limit unit'), 'KB/s');
    await fireEvent.click(screen.getByLabelText(/Add paused/));
    expect(footerSummary()).toBe('2 links · 4 connections');
    await fireEvent.click(screen.getByRole('button', { name: 'Add 2 downloads' }));
    await waitFor(() => expect(onclose).toHaveBeenCalledTimes(1));
    expect(submit).toHaveBeenCalledTimes(1);
    expect(submit.mock.calls[0]?.[0]).toEqual([
      { url: 'https://files.example.com/a.iso', dir: '', filename: '', maxConnections: 4, speedLimit: 2000, startPaused: true, source: 'ui' },
      { url: 'https://files.example.com/b.iso', dir: '', filename: '', maxConnections: 4, speedLimit: 2000, startPaused: true, source: 'ui' },
    ]);
    expect(onclose).toHaveBeenCalledTimes(1);
  });

  it('keeps the dialog open with only the URLs that failed', async () => {
    const { urls, onclose } = setup((reqs) =>
      reqs.map((r) =>
        r.url.endsWith('bad.zip')
          ? { url: r.url, ok: false, error: 'url is not allowed' }
          : { url: r.url, ok: true, download: makeDownload({ id: 1, url: r.url }) },
      ),
    );
    await fireEvent.input(urls, { target: { value: 'https://a.example/good.zip https://a.example/bad.zip' } });
    await fireEvent.keyDown(urls, { key: 'Enter', ctrlKey: true });
    await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('url is not allowed'));
    expect(onclose).not.toHaveBeenCalled();
    expect(urls.value).toBe('https://a.example/bad.zip');
  });

  it('fills the folder from the picker and closes on Escape', async () => {
    const { pickFolder, onclose } = setup();
    await fireEvent.click(screen.getByRole('button', { name: 'Change folder' }));
    expect(pickFolder).toHaveBeenCalledWith('/home/you/Downloads');
    await waitFor(() =>
      expect((screen.getByLabelText('Save to folder') as HTMLInputElement).value).toBe('/picked/folder'),
    );
    await fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' });
    expect(onclose).toHaveBeenCalledTimes(1);
  });

  it('keeps Tab focus inside the dialog', async () => {
    setup();
    await tick();
    const dialog = screen.getByRole('dialog');
    const close = screen.getByRole('button', { name: 'Close' });
    close.focus();
    await fireEvent.keyDown(dialog, { key: 'Tab', shiftKey: true });
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Add download' }));
    await fireEvent.keyDown(dialog, { key: 'Tab' });
    expect(document.activeElement).toBe(close);
  });
});

describe('AddDialog: streams', () => {
  const MANIFEST = 'https://media.example.org/talk/master.m3u8';

  function setupStream(initialUrls = '', probe?: (req: ProbeMediaRequest) => Promise<MediaInfo>) {
    const submit = vi.fn(async (): Promise<AddOutcome[]> => []);
    const probeMedia = vi.fn(probe ?? (async (req: ProbeMediaRequest) => makeMediaInfo({ manifestUrl: req.manifestUrl })));
    const submitMedia = vi.fn(async (req: AddMediaRequest) => makeMediaDownload({ id: 1, url: req.manifestUrl }));
    const onclose = vi.fn();
    render(AddDialog, {
      props: {
        defaults: { dir: '/home/you/Downloads', maxConnections: 8 },
        pickFolder: vi.fn(async () => '/picked'),
        submit,
        probeMedia,
        submitMedia,
        onclose,
        initialUrls,
      },
    });
    const primary = () => screen.getAllByRole('button').find((b) => b.getAttribute('type') === 'submit')!;
    return { submit, probeMedia, submitMedia, onclose, primary };
  }

  it('lists the qualities first, then adds the stream with the chosen tracks', async () => {
    const { probeMedia, submitMedia, onclose, primary } = setupStream();
    await fireEvent.click(screen.getByLabelText('Video or stream'));
    expect(screen.getByRole('dialog', { name: 'Add video or stream' })).toBeTruthy();
    expect(screen.queryByLabelText('Connections')).toBeNull();
    const address = screen.getByLabelText('Video page or stream address') as HTMLInputElement;
    await fireEvent.input(address, { target: { value: MANIFEST } });
    expect(primary().textContent).toBe('Find qualities');
    await fireEvent.click(primary());
    await waitFor(() => expect(screen.getByRole('radiogroup', { name: 'Video quality' })).toBeTruthy());
    expect(probeMedia).toHaveBeenCalledWith({ manifestUrl: MANIFEST });
    expect(submitMedia).not.toHaveBeenCalled();
    // The keyboard goes on with the choice: the chosen quality card has the focus.
    expect(document.activeElement).toBe(checkedQuality());
    expect(checkedQuality().value).toBe('v1');
    expect(screen.getByText('Found')).toBeTruthy();
    expect(primary().textContent).toBe('Add stream');
    expect((screen.getByLabelText('File name') as HTMLInputElement).placeholder).toBe('Keynote');

    await fireEvent.click(qualityRadio('v0'));
    expect(checkedQuality().value).toBe('v0');
    await chooseOption(combobox('Audio track'), 'r1');
    await openMore();
    await fireEvent.input(screen.getByLabelText('File name'), { target: { value: ' talk ' } });
    await fireEvent.click(screen.getByLabelText(/Add paused/));
    await fireEvent.click(primary());
    await waitFor(() => expect(onclose).toHaveBeenCalledTimes(1));
    expect(submitMedia).toHaveBeenCalledWith({
      manifestUrl: MANIFEST,
      dir: '',
      filename: 'talk',
      source: 'ui',
      videoTrack: 'v0',
      audioTrack: 'r1',
      startPaused: true,
    });
  });

  it("adds a stream with the daemon's audio-only pick", async () => {
    const { submitMedia, onclose, primary } = setupStream(MANIFEST, async (req) =>
      makeMediaInfo({ manifestUrl: req.manifestUrl, selectedVideo: '', selectedAudio: 'r1' }),
    );
    await fireEvent.click(primary());
    await waitFor(() => expect(screen.getByRole('radiogroup', { name: 'Video quality' })).toBeTruthy());
    // The audio-only pick is the last card.
    expect(checkedQuality().value).toBe('');
    expect(checkedQuality().getAttribute('aria-label')).toBe('No video (audio only)');
    expect(qualityRadios().at(-1)).toBe(checkedQuality());
    expect(combobox('Audio track').value).toBe('r1');
    await fireEvent.click(primary());
    await waitFor(() => expect(onclose).toHaveBeenCalledTimes(1));
    expect(submitMedia).toHaveBeenCalledWith(expect.objectContaining({ videoTrack: '', audioTrack: 'r1' }));
  });

  it('lists the qualities again when the address changes', async () => {
    const { probeMedia, primary } = setupStream(MANIFEST);
    await fireEvent.click(primary());
    await waitFor(() => expect(screen.getByRole('radiogroup', { name: 'Video quality' })).toBeTruthy());
    await fireEvent.input(screen.getByLabelText('Video page or stream address'), { target: { value: 'https://media.example.org/b.mpd' } });
    expect(screen.queryByRole('radiogroup', { name: 'Video quality' })).toBeNull();
    expect(screen.queryByText('Found')).toBeNull();
    expect(primary().textContent).toBe('Find qualities');
    await fireEvent.click(primary());
    await waitFor(() => expect(probeMedia).toHaveBeenCalledTimes(2));
  });

  it('explains why an encrypted or live stream cannot be added', async () => {
    const { probeMedia, submitMedia, onclose, primary } = setupStream(MANIFEST, async (req) => {
      throw new ApiError('invalid_argument', req.manifestUrl.includes('live') ? 'live: the presentation is live' : 'encrypted: DRM');
    });
    await fireEvent.click(primary());
    expect((await screen.findByRole('alert')).textContent).toContain(REFUSALS.encrypted);
    await fireEvent.input(screen.getByLabelText('Video page or stream address'), { target: { value: 'https://media.example.org/live.m3u8' } });
    await fireEvent.click(primary());
    await waitFor(() => expect(screen.getByRole('alert').textContent).toContain(REFUSALS.live));
    expect(probeMedia).toHaveBeenCalledTimes(2);
    expect(submitMedia).not.toHaveBeenCalled();
    expect(onclose).not.toHaveBeenCalled();
    expect(screen.queryByLabelText('Video quality')).toBeNull();
  });

  it('checks the address before asking the daemon', async () => {
    const { probeMedia, primary } = setupStream();
    await fireEvent.click(screen.getByLabelText('Video or stream'));
    await fireEvent.click(primary());
    const address = screen.getByLabelText('Video page or stream address');
    expect(screen.getByText(/Enter the address of a video page/)).toBeTruthy();
    expect(address.getAttribute('aria-invalid')).toBe('true');
    expect(document.activeElement).toBe(address);
    await fireEvent.input(address, { target: { value: 'ftp://media.example.org/a.m3u8' } });
    expect(screen.getByText(/must start with http/)).toBeTruthy();
    expect(probeMedia).not.toHaveBeenCalled();
  });

  it('opens in stream mode for a manifest address, and suggests it for one typed as a file', async () => {
    setupStream(MANIFEST);
    expect((screen.getByLabelText('Video page or stream address') as HTMLInputElement).value).toBe(MANIFEST);
    expect(document.activeElement).toBe(screen.getByLabelText('Video page or stream address'));
    expect((screen.getByLabelText('Video or stream') as HTMLInputElement).checked).toBe(true);
    await fireEvent.click(screen.getByLabelText('Files'));
    expect((screen.getByLabelText('URLs') as HTMLTextAreaElement).value).toBe(MANIFEST);
    await fireEvent.click(screen.getByRole('button', { name: 'Download it as a video' }));
    expect(screen.getByRole('dialog', { name: 'Add video or stream' })).toBeTruthy();
  });

  it('adds files only without the stream functions', () => {
    setup();
    expect(screen.queryByLabelText('Video or stream')).toBeNull();
  });
});

describe('AddDialog: queues (M6)', () => {
  const queues = [makeQueue({ id: 1 }), makeQueue({ id: 2, name: 'Night' }), makeQueue({ id: 3, name: 'Work', running: true })];

  function setupQueues(defaultQueue: number | null = null) {
    const submit = vi.fn(async (reqs: AddRequest[]): Promise<AddOutcome[]> =>
      reqs.map((r, i) => ({ url: r.url, ok: true, download: makeDownload({ id: i + 1, url: r.url }) })),
    );
    const submitMedia = vi.fn(async (req: AddMediaRequest) => makeMediaDownload({ id: 9, url: req.manifestUrl }));
    render(AddDialog, {
      props: {
        defaults: { dir: '/home/you/Downloads', maxConnections: 8 },
        pickFolder: async () => null,
        submit,
        probeMedia: async (req: ProbeMediaRequest) => makeMediaInfo({ manifestUrl: req.manifestUrl }),
        submitMedia,
        onclose: () => {},
        queues,
        defaultQueue,
      },
    });
    return { submit, submitMedia, queue: () => screen.getByRole('combobox', { name: 'Queue', hidden: true }) as HTMLButtonElement };
  }

  it('adds into the chosen queue; the main queue is the default', async () => {
    const { submit, queue } = setupQueues();
    expect(queue().value).toBe('1');
    await fireEvent.input(screen.getByLabelText('URLs'), { target: { value: 'https://example.com/a.zip' } });
    await fireEvent.click(screen.getByRole('button', { name: /^Add download/ }));
    await waitFor(() => expect(submit).toHaveBeenCalled());
    expect(submit.mock.calls[0]?.[0]?.[0]).not.toHaveProperty('queueId');
  });

  it('proposes the queue shown in the list, and says when it is stopped', async () => {
    const { submit, queue } = setupQueues(2);
    expect(queue().value).toBe('2');
    // More options sums up the queue while it is closed.
    expect(screen.getByRole('button', { name: 'More options' }).getAttribute('aria-expanded')).toBe('false');
    expect(screen.getByText(/· Night ·/)).toBeTruthy();
    await openMore();
    expect(optionValues(queue())).toEqual(['1', '2', '3']);
    expect(optionLabels(queue())).toEqual(['Main queue', 'Night (stopped)', 'Work']);
    expect(screen.getByText(/This queue is stopped/)).toBeTruthy();
    await chooseOption(queue(), 3);
    expect(screen.queryByText(/This queue is stopped/)).toBeNull();
    await fireEvent.input(screen.getByLabelText('URLs'), { target: { value: 'https://example.com/a.zip\nhttps://example.com/b.zip' } });
    await fireEvent.click(screen.getByRole('button', { name: /^Add 2 downloads/ }));
    await waitFor(() => expect(submit).toHaveBeenCalled());
    expect(submit.mock.calls[0]?.[0]?.map((r) => r.queueId)).toEqual([3, 3]);
  });

  it('adds a stream into the chosen queue', async () => {
    const { submitMedia, queue } = setupQueues(2);
    await fireEvent.click(screen.getByRole('radio', { name: /Video or stream/ }));
    await fireEvent.input(screen.getByLabelText('Video page or stream address'), { target: { value: 'https://media.example.org/a/master.m3u8' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Find qualities' }));
    await waitFor(() => expect(screen.getByRole('button', { name: 'Add stream' })).toBeTruthy());
    expect(queue().value).toBe('2');
    await fireEvent.click(screen.getByRole('button', { name: 'Add stream' }));
    await waitFor(() => expect(submitMedia).toHaveBeenCalled());
    expect(submitMedia.mock.calls[0]?.[0]).toMatchObject({ queueId: 2 });
  });
});
describe('AddDialog: video pages (SITE-MEDIA-SPEC §5.4)', () => {
  function setupSite(
    probe: (req: ProbeMediaRequest) => Promise<MediaInfo> = async (req) => makeSiteMediaInfo({ pageUrl: req.pageUrl ?? '' }),
    tools: { status: ToolsStatus | null } = { status: null },
  ) {
    const probeMedia = vi.fn(probe);
    const submitMedia = vi.fn(async (req: AddMediaRequest) => makeSiteDownload({ id: 1, url: req.pageUrl ?? '' }));
    const onclose = vi.fn();
    const onopensettings = vi.fn();
    render(AddDialog, {
      props: {
        defaults: { dir: '/home/you/Downloads', maxConnections: 8 },
        pickFolder: vi.fn(async () => '/picked'),
        submit: vi.fn(async (): Promise<AddOutcome[]> => []),
        probeMedia,
        submitMedia,
        onclose,
        onopensettings,
        tools,
        initialUrls: '',
      },
    });
    const primary = () => screen.getAllByRole('button').find((b) => b.getAttribute('type') === 'submit')!;
    const address = () => screen.getByLabelText('Video page or stream address') as HTMLInputElement;
    return { probeMedia, submitMedia, onclose, onopensettings, primary, address };
  }

  async function enterPage(ui: ReturnType<typeof setupSite>, url = PAGE) {
    await fireEvent.click(screen.getByLabelText('Video or stream'));
    await fireEvent.input(ui.address(), { target: { value: url } });
  }

  it('widens once the qualities are found, and gives them a column beside the fields', async () => {
    const ui = setupSite();
    await enterPage(ui);
    const dialog = screen.getByRole('dialog');
    const before = dialog.style.inlineSize;
    expect(before).toContain('38rem');
    expect(dialog.querySelector('.form.split')).toBeNull();
    await fireEvent.click(ui.primary());
    await waitFor(() => expect(screen.getByRole('radiogroup', { name: 'Video quality' })).toBeTruthy());
    // 56rem, as Confirm download: the qualities (start) beside the audio track, the folder and
    // More options (end), in the order Tab takes them (scripts/measure-dialogs.mjs checks the fit)
    expect(dialog.style.inlineSize).toContain('56rem');
    const form = dialog.querySelector('.form.split');
    expect(form).toBeTruthy();
    const order = [...form!.children].map((el) => el.className.split(' ')[0]);
    expect(order.indexOf('quality-area')).toBeLessThan(order.indexOf('pair'));
    expect(order.indexOf('pair')).toBeLessThan(order.indexOf('more'));
  });

  it('lists every MP4 quality of a page with its size, and adds the chosen one as an .mp4', async () => {
    const ui = setupSite();
    await enterPage(ui);
    expect(screen.getByText(/The extension \(\.mp4\) is added for you/)).toBeTruthy();
    await fireEvent.click(ui.primary());
    await waitFor(() => expect(screen.getByRole('radiogroup', { name: 'Video quality' })).toBeTruthy());
    expect(ui.probeMedia).toHaveBeenCalledWith({ manifestUrl: '', pageUrl: PAGE, origin: 'site' });
    // downexd's order (best first), one card per class, no "audio only"; each with the size of
    // the file it makes (the video with its audio), "~" where it is an estimate
    expect(qualities()).toEqual([
      ['299', '1080p (Full HD) 60fps · H.264 · 482 MB'],
      ['136', '720p (HD) · H.264 · 126 MB'],
      ['397', '480p · AV1 · ~168 MB'],
      ['18', '360p · H.264 + AAC · ~45.0 MB'],
    ]);
    expect(checkedQuality().value).toBe('299');
    expect(screen.getByText('One MP4 per resolution, best first')).toBeTruthy();
    // H.264 mixed with AV1: the cards explain the difference.
    expect(screen.getByText('AV1 is smaller; H.264 plays everywhere.')).toBeTruthy();
    const audio = combobox('Audio track');
    expect(optionValues(audio)).toEqual(['140', '140-1']);
    expect(optionLabels(audio)).toEqual(['English original', 'Deutsch (German)']);
    // The trigger shows the name and what the track is ("English original · AAC 129 kbit/s").
    expect(audio.querySelector('.label')?.textContent).toBe('English original');
    expect(audio.querySelector('.detail')?.textContent).toContain('AAC 129 kbit/s');
    // The video card: the title, the site, and what is true of this download.
    const card = document.querySelector<HTMLElement>('.card')!;
    expect(card.textContent).toContain('A talk');
    expect(card.textContent).toContain('YouTube · youtube.com');
    expect(within(card).getByRole('list', { name: 'About this video' }).textContent?.replace(/\s+/g, ' ').trim()).toBe(
      '10:00 Up to 1080p Saved as MP4 No cookies sent',
    );
    // The chosen quality's picture is the card's tooltip, never its name.
    expect(card.getAttribute('title')).toBe('1920×1080');
    expect(footerSummary()).toBe('1080p · H.264 + English audio · 482 MB');
    expect(ui.primary().textContent).toBe('Add video');
    expect((screen.getByLabelText('File name') as HTMLInputElement).placeholder).toBe('A talk');

    // an estimated size says so; a muxed quality brings its own sound
    await fireEvent.click(qualityRadio('397'));
    expect(footerSummary()).toBe('480p · AV1 + English audio · ~168 MB');
    await fireEvent.click(qualityRadio('18'));
    expect(screen.queryByLabelText('Audio track')).toBeNull();
    expect(screen.getByText('This video quality includes its sound.')).toBeTruthy();
    // no declared size: 600 kbit/s × 10 min
    expect(footerSummary()).toBe('360p · H.264 with its sound · about 45.0 MB');
    await fireEvent.click(ui.primary());
    await waitFor(() => expect(ui.onclose).toHaveBeenCalledTimes(1));
    expect(ui.submitMedia).toHaveBeenCalledWith({
      manifestUrl: '',
      pageUrl: PAGE,
      origin: 'site',
      dir: '',
      filename: '',
      source: 'ui',
      videoTrack: '18',
      audioTrack: '',
      startPaused: false,
    });
  });

  it('asks again while yt-dlp is set up and the page is read, showing how far it is', async () => {
    vi.useFakeTimers();
    const answers: (string | MediaInfo)[] = [
      'failed_precondition: tools_installing: downloading yt-dlp and Deno (34%)',
      'failed_precondition: site_resolving: still getting the video info',
      makeSiteMediaInfo(),
    ];
    const ui = setupSite(async () => {
      const next = answers.shift()!;
      if (typeof next === 'string') throw new Error(next);
      return next;
    });
    await enterPage(ui);
    await fireEvent.click(ui.primary());
    await vi.advanceTimersByTimeAsync(0);
    expect(ui.probeMedia).toHaveBeenCalledTimes(1);
    expect(ui.primary().textContent).toBe('Finding qualities…');
    expect(screen.getByRole('progressbar', { name: 'Downloading yt-dlp and Deno' }).getAttribute('aria-valuenow')).toBe('34');
    // the percentage once: the bar's; the line names what downloads
    expect(screen.getByText('Downloading yt-dlp and Deno…')).toBeTruthy();
    expect(screen.queryByText(/Deno: 34%/)).toBeNull();
    expect(screen.getByText(/The first time, Downex downloads yt-dlp/)).toBeTruthy();

    await vi.advanceTimersByTimeAsync(2000); // every 2 s
    expect(ui.probeMedia).toHaveBeenCalledTimes(2);
    expect(screen.getByText(/Getting video info…/)).toBeTruthy();
    await vi.advanceTimersByTimeAsync(1000);
    expect(screen.getByText('3 s')).toBeTruthy();
    expect(ui.probeMedia).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(1000);
    expect(ui.probeMedia).toHaveBeenCalledTimes(3);
    await vi.advanceTimersByTimeAsync(0);
    expect(screen.getByLabelText('Video quality')).toBeTruthy();
    expect(screen.queryByText(/Getting video info/)).toBeNull();
    expect(screen.queryByRole('alert')).toBeNull();
  });

  it('asks again at once after an answer that took downexd its whole wait budget', async () => {
    vi.useFakeTimers();
    let calls = 0;
    const ui = setupSite(async () => {
      calls++;
      if (calls === 1) {
        await new Promise((r) => setTimeout(r, 25_000));
        throw new Error('failed_precondition: site_resolving: still getting the video info');
      }
      return makeSiteMediaInfo();
    });
    await enterPage(ui);
    await fireEvent.click(ui.primary());
    await vi.advanceTimersByTimeAsync(25_000);
    expect(calls).toBe(2);
    await vi.advanceTimersByTimeAsync(0);
    expect(screen.getByLabelText('Video quality')).toBeTruthy();
  });

  it('shows the install progress the tools events report', async () => {
    vi.useFakeTimers();
    const installing = makeTools({
      ready: false,
      tools: makeTools().tools.map((t) => ({ ...t, state: 'installing' as const, version: '', bytesDone: 5 * MiB, bytesTotal: 20 * MiB })),
    });
    const ui = setupSite(async () => {
      throw new Error('failed_precondition: tools_installing: downloading yt-dlp and Deno (10%)');
    }, { status: installing });
    await enterPage(ui);
    await fireEvent.click(ui.primary());
    await vi.advanceTimersByTimeAsync(0);
    expect(screen.getByRole('progressbar', { name: 'Downloading yt-dlp and Deno' }).getAttribute('aria-valuenow')).toBe('25');
    expect(screen.getByText('Downloading yt-dlp and Deno…')).toBeTruthy();
  });

  it('stops asking on Cancel, and stays open to try again', async () => {
    vi.useFakeTimers();
    const ui = setupSite(async () => {
      throw new Error('failed_precondition: site_resolving: still getting the video info');
    });
    await enterPage(ui);
    await fireEvent.click(ui.primary());
    await vi.advanceTimersByTimeAsync(2000);
    expect(ui.probeMedia).toHaveBeenCalledTimes(2);
    const dialog = screen.getByRole('dialog', { name: 'Add video or stream' });
    expect(dialog.getAttribute('aria-busy')).toBe('false'); // Escape still closes it
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }));
    await vi.advanceTimersByTimeAsync(10_000);
    expect(ui.probeMedia).toHaveBeenCalledTimes(2);
    expect(ui.onclose).not.toHaveBeenCalled();
    expect(ui.primary().textContent).toBe('Find qualities');
    expect(screen.queryByText(/Getting video info/)).toBeNull();
    expect(screen.queryByRole('alert')).toBeNull();
    // a changed address also ends the wait for the old one
    await fireEvent.click(ui.primary());
    await vi.advanceTimersByTimeAsync(0);
    expect(ui.probeMedia).toHaveBeenCalledTimes(3);
    await fireEvent.input(ui.address(), { target: { value: `${PAGE}x` } });
    await vi.advanceTimersByTimeAsync(10_000);
    expect(ui.probeMedia).toHaveBeenCalledTimes(3);
  });

  it('explains why a page cannot be added, and offers the settings when yt-dlp is missing', async () => {
    const answers = [
      new ApiError('failed_precondition', 'tools_unavailable: yt-dlp and Deno are not set up: could not download yt-dlp (network)'),
      new ApiError('failed_precondition', 'site_unavailable: this video is not available: Private video'),
      new ApiError('invalid_argument', 'media_unsupported: a playlist: open a single video'),
      new ApiError('invalid_argument', 'media_unsupported: no MP4 video on this page'),
      new ApiError('failed_precondition', 'site_error: yt-dlp could not read this page: HTTP Error 500'),
      new ApiError('unimplemented', 'downex.v1.DaemonService.ProbeMedia is not implemented'),
    ];
    const ui = setupSite(async () => {
      throw answers.shift()!;
    });
    await enterPage(ui);
    const expectAlert = async (text: string) => {
      await fireEvent.click(ui.primary());
      await waitFor(() => expect(screen.getByRole('alert').textContent).toContain(text));
    };
    await expectAlert('Downex could not set up yt-dlp/Deno: could not download yt-dlp (network).');
    await fireEvent.click(screen.getByRole('button', { name: 'Open settings' }));
    expect(ui.onopensettings).toHaveBeenCalledTimes(1);
    await expectAlert(`${SITE_TEXTS.site_unavailable} Private video.`);
    expect(screen.queryByRole('button', { name: 'Open settings' })).toBeNull();
    await expectAlert(SITE_TEXTS.playlist);
    await expectAlert(SITE_TEXTS.media_unsupported);
    await expectAlert('yt-dlp could not read this page: HTTP Error 500.');
    await expectAlert('Update Downex to download videos.');
    expect(ui.probeMedia).toHaveBeenCalledTimes(6);
    expect(ui.submitMedia).not.toHaveBeenCalled();
  });

  it('asks for a manifest once (no retries) and names the .mp4 extension', async () => {
    const ui = setupSite(async () => {
      throw new ApiError('failed_precondition', 'tools_installing: downloading yt-dlp and Deno (34%)');
    });
    await enterPage(ui, 'https://media.example.org/a/master.m3u8');
    expect(screen.getByText(/The extension \(\.mp4\) is added for you/)).toBeTruthy();
    await fireEvent.click(ui.primary());
    await waitFor(() => expect(screen.getByRole('alert')).toBeTruthy());
    expect(ui.probeMedia).toHaveBeenCalledTimes(1);
    expect(ui.probeMedia).toHaveBeenCalledWith({ manifestUrl: 'https://media.example.org/a/master.m3u8' });
  });
});

describe('AddDialog: the Studio layout (DESIGN-SYSTEM.md §4.6)', () => {
  function setupLayout(props: Record<string, unknown> = {}) {
    const submit = vi.fn(async (reqs: AddRequest[]): Promise<AddOutcome[]> =>
      reqs.map((r, i) => ({ url: r.url, ok: true, download: makeDownload({ id: i + 1, url: r.url }) })),
    );
    const probeMedia = vi.fn(async (req: ProbeMediaRequest) => makeSiteMediaInfo({ pageUrl: req.pageUrl ?? '' }));
    const submitMedia = vi.fn(async (req: AddMediaRequest) => makeSiteDownload({ id: 1, url: req.pageUrl ?? '' }));
    const onclose = vi.fn();
    render(AddDialog, {
      props: {
        defaults: { dir: '/home/you/Downloads', maxConnections: 8 },
        pickFolder: vi.fn(async () => '/picked'),
        submit,
        probeMedia,
        submitMedia,
        onclose,
        ...props,
      },
    });
    const primary = () => screen.getAllByRole('button').find((b) => b.getAttribute('type') === 'submit')!;
    const more = () => screen.getByRole('button', { name: 'More options' });
    return { submit, probeMedia, submitMedia, onclose, primary, more };
  }

  it('shows "Add" with the mode as a segmented control, and keeps the dialog names', async () => {
    setupLayout();
    const dialog = screen.getByRole('dialog', { name: 'Add downloads' });
    expect(within(dialog).getByRole('heading', { level: 2 }).textContent).toBe('Add');
    const group = within(dialog).getByRole('group', { name: 'What to add' });
    const radios = within(group).getAllByRole('radio') as HTMLInputElement[];
    expect(radios.map((r) => r.value)).toEqual(['files', 'stream']);
    // no radio dots: the native radios are visually hidden inside the segments
    expect(radios.every((r) => r.classList.contains('sr-only'))).toBe(true);
    await fireEvent.click(within(group).getByLabelText('Video or stream'));
    expect(screen.getByRole('dialog', { name: 'Add video or stream' })).toBe(dialog);
  });

  it('opens in the mode it is asked for, with a page address in the stream field', async () => {
    setupLayout({ initialMode: 'stream', initialUrls: PAGE });
    expect(screen.getByRole('dialog', { name: 'Add video or stream' })).toBeTruthy();
    expect((screen.getByLabelText('Video page or stream address') as HTMLInputElement).value).toBe(PAGE);
    expect(document.activeElement).toBe(screen.getByLabelText('Video page or stream address'));
  });

  it('closes only an open list on Escape, keeping the dialog and what was typed', async () => {
    const { urls, onclose } = setup();
    await fireEvent.input(urls, { target: { value: 'https://example.com/a.zip' } });
    await openMore();
    const unit = combobox('Speed limit unit');
    unit.focus();
    await fireEvent.keyDown(unit, { key: 'ArrowDown' });
    expect(unit.getAttribute('aria-expanded')).toBe('true');
    await fireEvent.keyDown(unit, { key: 'Escape' });
    expect(unit.getAttribute('aria-expanded')).toBe('false');
    expect(onclose).not.toHaveBeenCalled();
    expect(urls.value).toBe('https://example.com/a.zip');
    await fireEvent.keyDown(unit, { key: 'Escape' });
    expect(onclose).toHaveBeenCalledTimes(1);
  });

  it('keeps More options closed with a summary of what it holds, and opens on demand', async () => {
    const { more } = setupLayout();
    await fireEvent.input(screen.getByLabelText('URLs'), { target: { value: 'https://example.com/a.zip' } });
    expect(more().getAttribute('aria-expanded')).toBe('false');
    const body = document.getElementById(more().getAttribute('aria-controls')!)!;
    expect(body.hidden).toBe(true);
    // The summary describes the button; it is not part of its name.
    const summary = document.getElementById(more().getAttribute('aria-describedby')!)!;
    expect(summary.textContent).toBe('Name from the server · 8 connections · No speed limit · Start now');
    await fireEvent.click(more());
    expect(more().getAttribute('aria-expanded')).toBe('true');
    expect(body.hidden).toBe(false);
    expect(more().getAttribute('aria-describedby')).toBeNull();
    await fireEvent.input(screen.getByLabelText('File name'), { target: { value: 'b.zip' } });
    await fireEvent.input(screen.getByLabelText('Speed limit'), { target: { value: '500' } });
    await chooseOption(combobox('Speed limit unit'), 'KB/s');
    await fireEvent.click(screen.getByLabelText(/Add paused/));
    await fireEvent.click(more());
    expect(body.hidden).toBe(true);
    expect(summary.textContent).toBe('b.zip · 8 connections · limit 500 KB/s · Add paused');
    expect(footerSummary()).toBe('1 link · 8 connections');
    // a unit typed with the number is read back in decimal units
    await fireEvent.click(more());
    await fireEvent.input(screen.getByLabelText('Speed limit'), { target: { value: '2 MiB' } });
    await fireEvent.click(more());
    expect(summary.textContent).toBe('b.zip · 8 connections · limit 2.1 MB/s · Add paused');
  });

  it('opens More options by itself when a field inside it is wrong, and focuses that field', async () => {
    const { more, submit, primary } = setupLayout();
    await fireEvent.input(screen.getByLabelText('URLs'), { target: { value: 'https://example.com/a.zip' } });
    await fireEvent.input(screen.getByLabelText('Connections'), { target: { value: '99' } });
    expect(more().getAttribute('aria-expanded')).toBe('false');
    await fireEvent.click(primary());
    await tick();
    expect(more().getAttribute('aria-expanded')).toBe('true');
    expect(document.activeElement).toBe(screen.getByLabelText('Connections'));
    expect(screen.getByText(/Connections must be from 1 to 32/)).toBeTruthy();
    expect(submit).not.toHaveBeenCalled();
  });

  it('opens More options for a wrong video file name too', async () => {
    const { more, primary, probeMedia, submitMedia } = setupLayout({ initialMode: 'stream', initialUrls: PAGE });
    await fireEvent.click(primary());
    await waitFor(() => expect(probeMedia).toHaveBeenCalledTimes(1));
    await screen.findByRole('radiogroup', { name: 'Video quality' });
    // Typed while More options was open, then closed again.
    await fireEvent.click(more());
    await fireEvent.input(screen.getByLabelText('File name'), { target: { value: 'a/b' } });
    await fireEvent.click(more());
    expect(more().getAttribute('aria-expanded')).toBe('false');
    await fireEvent.click(primary());
    await tick();
    expect(more().getAttribute('aria-expanded')).toBe('true');
    expect(document.activeElement).toBe(screen.getByLabelText('File name'));
    expect(submitMedia).not.toHaveBeenCalled();
  });

  it('sums up the video in More options: whether it starts now, then its file name (cut first), whole as its tooltip', async () => {
    const { more, primary } = setupLayout({ initialMode: 'stream', initialUrls: PAGE });
    const summaryEl = () => document.getElementById(more().getAttribute('aria-describedby')!)!;
    const summary = () => summaryEl().textContent;
    expect(summary()).toBe('Start now · Name from the video title');
    await fireEvent.click(primary());
    await screen.findByRole('radiogroup', { name: 'Video quality' });
    expect(summary()).toBe('Start now · A talk.mp4');
    expect(summaryEl().title).toBe('Start now · A talk.mp4');
  });

  it('says "No cookies sent" on the video card: the Add dialog never sends cookies', async () => {
    const { primary } = setupLayout({ initialMode: 'stream', initialUrls: PAGE });
    await fireEvent.click(primary());
    await screen.findByRole('radiogroup', { name: 'Video quality' });
    expect(within(screen.getByRole('list', { name: 'About this video' })).getByText('No cookies sent')).toBeTruthy();
  });

  it('shows the free space of the folder, and nothing when it cannot be read', async () => {
    const freeSpace = vi.fn(async (dir: string) => {
      if (dir.startsWith('/mnt')) throw new Error('unavailable: could not read the free space');
      return 214 * 1024 * MiB;
    });
    setupLayout({ freeSpace });
    await waitFor(() => expect(screen.getByText('230 GB free')).toBeTruthy());
    expect(freeSpace).toHaveBeenCalledWith('/home/you/Downloads');
    await fireEvent.input(screen.getByLabelText('Save to folder'), { target: { value: '/mnt/share' } });
    await waitFor(() => expect(freeSpace).toHaveBeenCalledWith('/mnt/share'));
    await waitFor(() => expect(screen.queryByText(/GB free/)).toBeNull());
    // A relative folder is not asked about.
    freeSpace.mockClear();
    await fireEvent.input(screen.getByLabelText('Save to folder'), { target: { value: 'relative' } });
    await new Promise((r) => setTimeout(r, 350));
    expect(freeSpace).not.toHaveBeenCalled();
  });

  it('warns when the chosen video is larger than the free space', async () => {
    const { primary } = setupLayout({ initialMode: 'stream', initialUrls: PAGE, freeSpace: async () => 200 * MiB });
    await fireEvent.click(primary());
    await screen.findByRole('radiogroup', { name: 'Video quality' });
    const free = await screen.findByText('210 MB free');
    // 1080p is 460 MiB; 720p is 120 MiB
    expect(free.classList.contains('tight')).toBe(true);
    await fireEvent.click(qualityRadio('136'));
    expect(free.classList.contains('tight')).toBe(false);
  });
});
