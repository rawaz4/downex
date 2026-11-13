import { describe, expect, it } from 'vitest';
import { joinPath, rawJoinPath } from './display';
import { formatClock, progressFraction } from './format';
import {
  AVERAGE_MIN_ACTIVE_MS,
  PROGRESS_WINDOW,
  addressLabel,
  averageBps,
  averageRateText,
  connectionText,
  downloadedText,
  doneAtText,
  formatElapsed,
  fragmentsText,
  headerPercent,
  isAssembling,
  isRefused,
  mediaFormatText,
  pieceNames,
  progressStatus,
  progressTitle,
  qualityDetail,
  qualityText,
  rateText,
  resumeSupport,
  sizeText,
  timeLeftText,
  timeTakenSummary,
  timeTakenText,
  fitWindowHeight,
  windowFrame,
} from './progress';
import { MiB, makeConnection, makeDownload, makeMediaDownload, makeMediaInfo, makeSiteDownload, makeSiteMediaInfo } from './testing';

describe('download complete dialog texts (DESKTOP-SPEC §8)', () => {
  it('formats a running time to the second', () => {
    expect(formatElapsed(0)).toBe('<1s');
    expect(formatElapsed(400)).toBe('<1s');
    expect(formatElapsed(1499)).toBe('1s');
    expect(formatElapsed(45_000)).toBe('45s');
    expect(formatElapsed(59_600)).toBe('1m 00s');
    expect(formatElapsed(754_250)).toBe('12m 34s');
    expect(formatElapsed(2 * 3600_000 + 7 * 60_000 + 12_000)).toBe('2h 07m 12s');
    expect(formatElapsed(3 * 86400_000 + 4 * 3600_000 + 10 * 60_000)).toBe('3d 04h 10m');
    expect(formatElapsed(-1)).toBe('—');
    expect(formatElapsed(Number.NaN)).toBe('—');
  });

  it('takes the time spent running, else the time from the first start to completion', () => {
    const t0 = Date.UTC(2026, 9, 20, 8, 0, 0);
    // downexd counts the running time without pauses: it wins over the clock.
    expect(timeTakenText({ activeMs: 90_000, startedAt: t0, completedAt: t0 + 3600_000 })).toBe('1m 30s');
    // An older downexd: from the first start to completion.
    expect(timeTakenText({ activeMs: 0, startedAt: t0, completedAt: t0 + 3723_000 })).toBe('1h 02m 03s');
    expect(timeTakenText({ activeMs: 0, startedAt: 0, completedAt: t0 })).toBe('Unknown');
    expect(timeTakenText({ activeMs: 0, startedAt: t0, completedAt: 0 })).toBe('Unknown');
    expect(timeTakenText({ activeMs: 0, startedAt: t0 + 5, completedAt: t0 })).toBe('Unknown');
  });

  it('adds the average speed of the whole download to the time taken, when downexd counted the running time', () => {
    const t0 = Date.UTC(2026, 9, 20, 8, 0, 0);
    const done = (partial: Parameters<typeof makeDownload>[0]) =>
      timeTakenSummary(makeDownload({ state: 'completed', startedAt: t0, completedAt: t0 + 3600_000, ...partial }));
    // 1368 MiB in 3 minutes of running (an hour with pauses): 7.6 MiB/s
    expect(done({ id: 1, size: 1368 * MiB, received: 1368 * MiB, activeMs: 180_000 })).toBe('3m 00s · average 8.0 MB/s');
    // a short one is still the whole download
    expect(done({ id: 2, size: 10 * MiB, received: 10 * MiB, activeMs: 400 })).toBe('<1s · average 26.2 MB/s');
    // an older downexd: the time from the first start includes pauses, so no average from it
    expect(done({ id: 3, received: 10 * MiB, activeMs: 0, completedAt: t0 + 3723_000 })).toBe('1h 02m 03s');
    expect(done({ id: 4, received: 10 * MiB, activeMs: 0, startedAt: 0 })).toBe('Unknown');
    // no bytes known: no average
    expect(done({ id: 5, size: 0, received: 0, activeMs: 5000 })).toBe('5s');
  });

  it('names a stream’s format and the file it became', () => {
    expect(mediaFormatText(makeMediaDownload({ id: 1 }))).toBe('HLS stream, saved as MP4');
    expect(mediaFormatText(makeMediaDownload({ id: 2, media: makeMediaInfo({ format: 'dash', container: 'ts' }) }))).toBe(
      'DASH stream, saved as MPEG-TS',
    );
    expect(mediaFormatText(makeMediaDownload({ id: 3, media: makeMediaInfo({ container: '' }) }))).toBe('HLS stream');
    expect(mediaFormatText(makeMediaDownload({ id: 4, media: makeMediaInfo({ format: '' }) }))).toBe('Saved as MP4');
    expect(mediaFormatText(makeDownload({ id: 5 }))).toBe('');
  });

  it('joins the saved path with the folder’s separator, escaped only for display', () => {
    expect(rawJoinPath('C:\\Users\\you\\Downloads', 'a.zip')).toBe('C:\\Users\\you\\Downloads\\a.zip');
    expect(rawJoinPath('/home/you/', 'a.zip')).toBe('/home/you/a.zip');
    expect(rawJoinPath('/home/you', '')).toBe('/home/you');
    expect(rawJoinPath('/d', 'x\u202egpj.exe')).toBe('/d/x\u202egpj.exe');
    expect(joinPath('/d', 'x\u202egpj.exe')).toBe('/d/x⟨U+202E⟩gpj.exe');
  });

  it('fits the window to the completion view: fractional content, the frame and slack, rounded up, within its limits', () => {
    const slack = PROGRESS_WINDOW.fitSlack;
    expect(slack).toBeGreaterThanOrEqual(2);
    expect(fitWindowHeight('complete', 300, 39)).toBe(300 + 39 + slack);
    // 254.5 px of content and 24 px of padding (the 125 % case that scrolled by 1 px): never less
    expect(fitWindowHeight('complete', 278.5, 39)).toBe(Math.ceil(278.5 + 39 + slack));
    expect(fitWindowHeight('complete', 278.5, 39)).toBeGreaterThan(278.5 + 39 + 1.9);
    expect(fitWindowHeight('complete', 100, 39)).toBe(PROGRESS_WINDOW.minHeight);
    expect(fitWindowHeight('complete', 2000, 39)).toBe(PROGRESS_WINDOW.maxHeight);
    // Nothing measurable (no layout): the default height.
    expect(fitWindowHeight('complete', 0, 39)).toBe(PROGRESS_WINDOW.completeHeight);
    expect(fitWindowHeight('complete', Number.NaN, 39)).toBe(PROGRESS_WINDOW.completeHeight);
  });

  it('fits the collapsed progress view, never below its usual height', () => {
    const slack = PROGRESS_WINDOW.fitSlack;
    // a plain file fits the usual window
    expect(fitWindowHeight('collapsed', 200, 39)).toBe(PROGRESS_WINDOW.collapsedHeight);
    // a web page's video with more facts gets the room it needs
    expect(fitWindowHeight('collapsed', 284.5, 39)).toBe(Math.ceil(284.5 + 39 + slack));
    expect(fitWindowHeight('collapsed', 5000, 39)).toBe(PROGRESS_WINDOW.maxHeight);
    expect(fitWindowHeight('collapsed', 0, 39)).toBe(PROGRESS_WINDOW.collapsedHeight);
  });

  it('takes the frame from the outer and inner heights, else the usual one', () => {
    expect(windowFrame(300, 261)).toBe(39);
    expect(windowFrame(300, 261.4)).toBeCloseTo(38.6);
    // the host reported no outer size, or one that cannot be this window's
    expect(windowFrame(null, 261)).toBe(PROGRESS_WINDOW.frame);
    expect(windowFrame(760, 261)).toBe(PROGRESS_WINDOW.frame);
    expect(windowFrame(200, 261)).toBe(PROGRESS_WINDOW.frame);
  });
});

describe('progress window texts', () => {
  it('titles the window "<percent>% <name>", just the name while the size is unknown', () => {
    expect(progressTitle(makeDownload({ id: 1, filename: 'a.iso', size: 200 * MiB, received: 50 * MiB, state: 'downloading' }))).toBe(
      '25% a.iso',
    );
    expect(progressTitle(makeDownload({ id: 2, filename: 'b.bin', size: -1, received: 5 * MiB, state: 'downloading' }))).toBe('b.bin');
    expect(progressTitle(makeDownload({ id: 3, filename: 'c.zip', state: 'completed', received: 10 * MiB }))).toBe('100% c.zip');
    // Never 100% before it is complete; hidden direction marks are shown.
    expect(progressTitle(makeDownload({ id: 4, filename: 'x‮gpj.exe', size: 1000, received: 999, state: 'downloading' }))).toBe(
      '99% x⟨U+202E⟩gpj.exe',
    );
    // A stream counts fragments.
    expect(progressTitle(makeMediaDownload({ id: 5, state: 'downloading', media: makeMediaInfo({ fragmentsDone: 50 }) }))).toBe(
      '25% Keynote.mp4',
    );
  });

  it('names each state as the status row', () => {
    const d = (partial: Parameters<typeof makeDownload>[0]) => progressStatus(makeDownload(partial));
    expect(d({ id: 1, state: 'probing' })).toBe('Connecting…');
    expect(d({ id: 1, state: 'downloading' })).toBe('Receiving data…');
    expect(
      d({ id: 1, state: 'downloading', connectionInfo: [makeConnection({ index: 1, state: 'connecting' }), makeConnection({ index: 2, state: 'requesting' })] }),
    ).toBe('Connecting…');
    expect(d({ id: 1, state: 'downloading', connectionInfo: [makeConnection({ index: 1, state: 'connecting' }), makeConnection({ index: 2 })] })).toBe(
      'Receiving data…',
    );
    expect(d({ id: 1, state: 'paused' })).toBe('Paused');
    expect(d({ id: 1, state: 'queued', queuePosition: 3 })).toBe('Waiting in queue (#3)');
    expect(d({ id: 1, state: 'queued' })).toBe('Waiting in queue');
    expect(d({ id: 1, state: 'completed' })).toBe('Completed');
    expect(d({ id: 1, state: 'failed', error: 'Server responded 404 Not Found' })).toBe('Failed: Server responded 404 Not Found');
    expect(d({ id: 1, state: 'failed' })).toBe('Failed: the download stopped with an error');
    expect(d({ id: 1, state: 'awaiting_confirmation' })).toBe('Waiting for confirmation');
    const assembling = makeMediaDownload({ id: 2, state: 'downloading', media: makeMediaInfo({ fragmentsDone: 200 }) });
    expect(isAssembling(assembling)).toBe(true);
    expect(progressStatus(assembling)).toBe('Assembling…');
    expect(rateText(assembling)).toBe('—');
    expect(timeLeftText(assembling)).toBe('—');
  });

  it('reports sizes, rates, time left and resume support', () => {
    const running = makeDownload({ id: 1, state: 'downloading', size: 100 * MiB, received: 25 * MiB, speedBps: 5 * MiB, mode: 'ranged' });
    expect(sizeText(running)).toBe('105 MB');
    expect(downloadedText(running)).toBe('26.2 MB (25%)');
    expect(rateText(running)).toBe('5.2 MB/s');
    expect(timeLeftText(running)).toBe('15s');
    expect(resumeSupport(running)).toBe('Yes');
    const unknown = makeDownload({ id: 2, state: 'downloading', size: -1, received: 3 * MiB, mode: 'single' });
    expect(sizeText(unknown)).toBe('Unknown');
    expect(downloadedText(unknown)).toBe('3.1 MB');
    expect(timeLeftText(unknown)).toBe('—');
    expect(resumeSupport(unknown)).toBe('No');
    expect(resumeSupport(makeDownload({ id: 3 }))).toBe('Unknown');
    expect(rateText(makeDownload({ id: 4, state: 'paused', speedBps: 9 }))).toBe('—');
    expect(resumeSupport(makeMediaDownload({ id: 5 }))).toBe('Yes');
  });

  it('averages the whole download: its bytes over the time all its runs took', () => {
    expect(averageBps({ received: 1368 * MiB, activeMs: 180_000 })).toBeCloseTo((1368 * MiB) / 180);
    expect(averageBps({ received: 0, activeMs: 4000 })).toBe(0);
    // no running time counted (not started, an older downexd): unknown, never a division by 0
    expect(averageBps({ received: 5 * MiB, activeMs: 0 })).toBeNaN();
    expect(averageBps({ received: -1, activeMs: 4000 })).toBeNaN();
  });

  it('shows the average beside the live rate once the download has run a few seconds', () => {
    const run = (partial: Partial<Parameters<typeof makeDownload>[0]>) =>
      makeDownload({ id: 1, state: 'downloading', size: 100 * MiB, received: 24 * MiB, speedBps: 5 * MiB, ...partial });
    expect(AVERAGE_MIN_ACTIVE_MS).toBeGreaterThanOrEqual(2000);
    // its first seconds are mostly connecting: no figure yet
    expect(averageRateText(run({ activeMs: 0 }))).toBe('');
    expect(averageRateText(run({ activeMs: AVERAGE_MIN_ACTIVE_MS - 1 }))).toBe('');
    expect(averageRateText(run({ activeMs: 3000 }))).toBe('8.4 MB/s');
    // earlier runs count: 24 MiB over 12 s of running in all
    expect(averageRateText(run({ activeMs: 12_000 }))).toBe('2.1 MB/s');
    // no live rate (paused, queued, assembling): no average beside it
    expect(averageRateText(run({ state: 'paused', speedBps: 0, activeMs: 3000 }))).toBe('');
    expect(averageRateText(run({ state: 'queued', speedBps: 0, activeMs: 3000 }))).toBe('');
    const assembling = makeMediaDownload({ id: 2, state: 'downloading', activeMs: 60_000, received: 300 * MiB, media: makeMediaInfo({ fragmentsDone: 200 }) });
    expect(averageRateText(assembling)).toBe('');
  });

  it('shows a stream’s estimated size with "~" and its time left from that size', () => {
    const stream = makeMediaDownload({
      id: 1,
      state: 'downloading',
      size: 2100 * MiB,
      sizeApprox: true,
      received: 700 * MiB,
      speedBps: 7 * MiB,
      media: makeMediaInfo({ fragmentsDone: 50 }),
    });
    expect(sizeText(stream)).toBe('~2.2 GB');
    // The percentage stays the fragments'.
    expect(downloadedText(stream)).toBe('734 MB (25%)');
    expect(progressTitle(stream)).toBe('25% Keynote.mp4');
    // (2100 − 700) MiB at 7 MiB/s.
    expect(timeLeftText(stream)).toBe('3m 20s');
    // Without an estimate: unknown, and the time left from the fragments (700 MiB were 25%).
    const unsized = { ...stream, size: -1, sizeApprox: false };
    expect(sizeText(unsized)).toBe('Unknown');
    expect(timeLeftText(unsized)).toBe('5m 00s');
    // Completed: the file's exact size.
    expect(sizeText({ ...stream, state: 'completed', size: 1900 * MiB, sizeApprox: false, received: 1900 * MiB })).toBe('2.0 GB');
    // A web page's video counts its bytes, never its parts, even while its size is an estimate
    // (decision N1, 0.8.2: a part's fraction went back on pause and resume).
    const site = makeSiteDownload({ id: 2, state: 'downloading', size: 460 * MiB, sizeApprox: true, received: 100 * MiB,
      media: makeSiteMediaInfo({ fragmentsTotal: 46, fragmentsDone: 23 }) });
    expect(sizeText(site)).toBe('~482 MB');
    expect(progressFraction(site)).toBeCloseTo(100 / 460);
  });

  it('shows the fragments and the quality of a stream in plain words, the details in its tooltip', () => {
    const m = makeMediaDownload({ id: 1, media: makeMediaInfo({ fragmentsDone: 12 }) });
    expect(fragmentsText(m)).toBe('12 / 200');
    // DESIGN-SYSTEM.md §4.8: the human codec text; the picture and raw codecs move to the tooltip.
    expect(qualityText(m)).toBe('1080p · H.264 · AAC 128 kbit/s');
    expect(qualityDetail(m)).toBe('1080p (Full HD) · H.264 · 5.0 Mbit/s · 1920×1080 · 29.97 fps · English · 128 kbit/s · mp4a.40.2');
    // a muxed quality's second codec is its sound (960×540 is in the 480p class)
    expect(qualityText(makeMediaDownload({ id: 3, media: makeMediaInfo({ selectedVideo: 'v2', selectedAudio: '' }) }))).toBe(
      '480p · H.264 · AAC',
    );
    // audio only
    expect(qualityText(makeMediaDownload({ id: 4, media: makeMediaInfo({ selectedVideo: '', selectedAudio: 'r1' }) }))).toBe('AAC 96 kbit/s');
    expect(fragmentsText(makeDownload({ id: 2 }))).toBe('');
    expect(qualityText(makeDownload({ id: 2 }))).toBe('');
    expect(qualityDetail(makeDownload({ id: 2 }))).toBe('');
  });

  it('says when a running download should be done, on the locale clock', () => {
    const t = Date.UTC(2026, 9, 28, 11, 0, 0);
    const running = makeDownload({ id: 1, state: 'downloading', size: 100 * MiB, received: 25 * MiB, speedBps: 5 * MiB });
    // 75 MiB at 5 MiB/s: 15 s from now
    expect(doneAtText(running, t)).toBe(formatClock(t + 15_000));
    expect(doneAtText({ ...running, speedBps: 0 }, t)).toBe('');
    expect(doneAtText({ ...running, size: -1 }, t)).toBe('');
    expect(doneAtText({ ...running, state: 'paused' }, t)).toBe('');
    expect(doneAtText(makeMediaDownload({ id: 2, state: 'downloading', media: makeMediaInfo({ fragmentsDone: 200 }) }), t)).toBe('');
  });

  it('gives the header its big percentage: "~" for an estimated size, none while it is unknown', () => {
    const d = makeDownload({ id: 1, state: 'downloading', size: 200 * MiB, received: 50 * MiB });
    expect(headerPercent(d)).toBe('25%');
    expect(headerPercent({ ...d, state: 'paused' })).toBe('25%');
    expect(headerPercent({ ...d, sizeApprox: true })).toBe('~25%');
    expect(headerPercent({ ...d, size: -1 })).toBe('');
    // never 100% before it is complete
    expect(headerPercent({ ...d, received: 200 * MiB - 1 })).toBe('99%');
    expect(headerPercent({ ...d, state: 'completed', sizeApprox: true, received: 200 * MiB })).toBe('100%');
  });

  it('describes each connection', () => {
    const c = (partial: Parameters<typeof makeConnection>[0]) => connectionText(makeConnection(partial), false);
    expect(c({ index: 1, state: 'connecting' })).toBe('Connecting…');
    expect(c({ index: 1, state: 'requesting' })).toBe('Sending request…');
    expect(c({ index: 1, state: 'receiving' })).toBe('Receiving data…');
    expect(c({ index: 1, state: 'retrying', detail: 'Retrying in 4 s (HTTP 503)' })).toBe('Retrying in 4 s (HTTP 503)');
    expect(c({ index: 1, state: 'retrying', detail: 'HTTP 503' })).toBe('Retrying (HTTP 503)');
    // downexd's own wording.
    expect(c({ index: 1, state: 'retrying', detail: 'HTTP 503, retrying in 4 s' })).toBe('Retrying in 4 s (HTTP 503)');
    expect(c({ index: 1, state: 'retrying', detail: 'connection reset, retrying in 2 min' })).toBe(
      'Retrying in 2 min (connection reset)',
    );
    expect(c({ index: 1, state: 'retrying' })).toBe('Retrying…');
    expect(c({ index: 1, state: 'idle' })).toBe('Idle');
    expect(c({ index: 1, state: 'done' })).toBe('Finished');
    expect(c({ index: 1, state: 'error', detail: 'disk full' })).toBe('Error: disk full');
    expect(c({ index: 1, state: 'error' })).toBe('Error');
    // Media: the fragment it fetches (shown from 1).
    expect(connectionText(makeConnection({ index: 2, fragment: 41 }), true)).toBe('Fragment 42 · Receiving data…');
    expect(connectionText(makeConnection({ index: 2, fragment: -1 }), true)).toBe('Receiving data…');
  });
});

describe('a web page video in the progress window (SITE-MEDIA-SPEC §5.4)', () => {
  const site = (partial: Partial<Parameters<typeof makeSiteDownload>[0]> = {}) =>
    makeSiteDownload({ id: 1, state: 'downloading', size: 460 * MiB, received: 115 * MiB, media: makeSiteMediaInfo({ fragmentsTotal: 46, fragmentsDone: 11 }), ...partial });

  it('says "Page", "Getting video info…", parts and "Merging…"', () => {
    expect(addressLabel(site())).toBe('Page');
    expect(addressLabel(makeMediaDownload({ id: 2 }))).toBe('Manifest');
    expect(addressLabel(makeDownload({ id: 3 }))).toBe('Address');
    expect(progressStatus(site({ state: 'probing' }))).toBe('Getting video info…');
    expect(progressStatus(makeMediaDownload({ id: 2, state: 'probing' }))).toBe('Connecting…');
    expect(pieceNames(site())).toEqual({ one: 'Part', many: 'Parts' });
    expect(pieceNames(makeMediaDownload({ id: 2 }))).toEqual({ one: 'Fragment', many: 'Fragments' });
    expect(connectionText(makeConnection({ index: 1, fragment: 11 }), true, 'Part')).toBe('Part 12 · Receiving data…');
    const merging = site({ media: makeSiteMediaInfo({ fragmentsTotal: 46, fragmentsDone: 46 }) });
    expect(isAssembling(merging)).toBe(true);
    expect(progressStatus(merging)).toBe('Merging…');
    expect(progressStatus(makeMediaDownload({ id: 2, state: 'downloading', media: makeMediaInfo({ fragmentsDone: 200 }) }))).toBe('Assembling…');
  });

  it('takes the size and percentage from the bytes downexd counts (BytesTotal)', () => {
    expect(sizeText(site())).toBe('482 MB');
    // 115 of 460 MiB: 25%, though 11 of 46 parts would be 23%
    expect(downloadedText(site())).toBe('121 MB (25%)');
    expect(progressFraction(site())).toBe(0.25);
    expect(progressTitle(site())).toBe('25% A talk.mp4');
    expect(fragmentsText(site())).toBe('11 / 46');
    // a stream counts its fragments; a web page's video with no size yet has no fraction (never its parts)
    expect(progressFraction(makeMediaDownload({ id: 2, received: 1, media: makeMediaInfo({ fragmentsDone: 50 }) }))).toBe(0.25);
    expect(progressFraction(site({ size: -1 }))).toBeNaN();
  });

  it('names the site, quality and file once complete', () => {
    const done = site({ state: 'completed', received: 460 * MiB });
    expect(qualityText(done)).toBe('1080p · H.264 · AAC 129 kbit/s');
    expect(mediaFormatText(done)).toBe('YouTube video, saved as MP4');
    const muxed = site({ media: makeSiteMediaInfo({ site: '', selectedVideo: '18', selectedAudio: '' }) });
    expect(mediaFormatText(muxed)).toBe('Video, saved as MP4');
    expect(qualityText(muxed)).toBe('360p · H.264 · AAC');
  });

  it('offers no retry for what retrying cannot change', () => {
    expect(isRefused({ errorCode: 'media_invalid' })).toBe(true);
    expect(isRefused({ errorCode: 'site_unsupported' })).toBe(true);
    expect(isRefused({ errorCode: 'site_unavailable' })).toBe(false);
    expect(isRefused({ errorCode: 'encrypted' })).toBe(true);
  });
});
