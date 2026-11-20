import { describe, expect, it } from 'vitest';
import { barPath, brandLayout } from '../../assets/brand';
import type { HandoffRecord } from '../../lib/history';
import { DEFAULT_VIDEO_OPTIONS } from '../../lib/options-form';
import { defaultRules, siteExclusion } from '../../lib/rules';
import { MP4_NOTE } from '../../lib/video-panel';
import {
  CONFIRM_IN_DOWNEX,
  DOWNLOADING_IN_DOWNEX,
  HOST_MISSING_HINT,
  connectionLabel,
  daemonLabel,
  BROWSER_FETCH_NOTE,
  siteNote,
  siteSendSwitch,
  type FileKind,
} from '../../lib/ui-format';
import {
  DAEMON_UNKNOWN,
  FILE_TILES,
  FILE_TILE_AUDIO_SMALL,
  SMALL_TILE_PX,
  TILE_VIEWBOX,
  NOT_RUNNING_TEXT,
  addedMark,
  daemonLine,
  daemonNote,
  durationText,
  handoffMark,
  handoffPill,
  RENEW_CHECK_PILL,
  RENEW_CHECK_TITLE,
  handoffTime,
  hostMissing,
  keepUnits,
  offlineView,
  qualityOptions,
  rulesForSiteSwitch,
  sentence,
  shortQuality,
  showsNoCookies,
  splitStatus,
  statusChip,
  statusState,
  statusTooltip,
  streamNameParts,
  tileKind,
  videoSubline,
  listNoteExtra,
} from './popup-view';

describe('splitStatus', () => {
  it('keeps a label without a reason as the headline', () => {
    expect(splitStatus('Connected (Downex 0.7.3)')).toEqual({ headline: 'Connected (Downex 0.7.3)', detail: '' });
    expect(splitStatus('Checking connection…')).toEqual({ headline: 'Checking connection…', detail: '' });
    expect(splitStatus('Not available')).toEqual({ headline: 'Not available', detail: '' });
  });

  it('splits at the first ": " and starts the reason with a capital', () => {
    const incompatible = connectionLabel({ state: 'disconnected', incompatibleProtocol: 2, since: 0 }).text;
    expect(splitStatus(incompatible)).toEqual({
      headline: 'Incompatible Downex',
      detail: 'Relay protocol 2. Update Downex or this extension.',
    });
    expect(splitStatus('Background unavailable: Could not establish connection. Receiving end does not exist.')).toEqual({
      headline: 'Background unavailable',
      detail: 'Could not establish connection. Receiving end does not exist.',
    });
    expect(splitStatus('Not available: error: a: b')).toEqual({ headline: 'Not available', detail: 'Error: a: b' });
  });

  it('takes the host-missing hint apart around its command', () => {
    const text = connectionLabel({ state: 'disconnected', lastError: 'Specified native messaging host not found.', since: 0 }).text;
    expect(text.endsWith(HOST_MISSING_HINT)).toBe(true);
    expect(splitStatus(text)).toEqual({
      headline: 'Not available',
      detail: 'Specified native messaging host not found.',
      fix: {
        before: 'Downex is not registered with this browser: install or reinstall Downex, or run ',
        code: 'downex-nmhost register',
        after: ', then press Check again.',
      },
    });
  });

  it('takes the refused-extension hint off and keeps its fix', () => {
    const text = connectionLabel({ state: 'disconnected', lastError: 'Access to the specified native messaging host is forbidden.', since: 0 }).text;
    expect(splitStatus(text)).toEqual({
      headline: 'Not available',
      detail: 'Access to the specified native messaging host is forbidden.',
      refusedFix: 'Install the latest Downex, then press Check again.',
    });
  });
});

describe('sentence', () => {
  it('starts a result text with a capital, keeping the rest', () => {
    expect(sentence('the server answered 403 Forbidden. The address may have expired.')).toBe(
      'The server answered 403 Forbidden. The address may have expired.',
    );
    expect(sentence('Confirm in Downex')).toBe('Confirm in Downex');
    expect(sentence('')).toBe('');
  });

  it('leaves a reason that starts with a lowercase program name as it is', () => {
    expect(sentence('yt-dlp could not read this page: HTTP Error 403: Forbidden.')).toBe('yt-dlp could not read this page: HTTP Error 403: Forbidden.');
    expect(sentence('downexd could not be started: the program was not found next to downex-nmhost')).toBe(
      'downexd could not be started: the program was not found next to downex-nmhost',
    );
  });
});

describe('keepUnits', () => {
  it('joins a size to its unit with a no-break space', () => {
    expect(keepUnits('This video · 1080p (Full HD) · MP4 · 48.2 MB')).toBe('This video · 1080p (Full HD) · MP4 · 48.2 MB');
    expect(keepUnits('~925 MB · one file')).toBe('~925 MB · one file');
    expect(keepUnits('1.2 GB, 512 B and 128 kbit/s')).toBe('1.2 GB, 512 B and 128 kbit/s');
    // Not a unit: left alone.
    expect(keepUnits('720p (HD) · MP4')).toBe('720p (HD) · MP4');
    expect(keepUnits('2 Bytes')).toBe('2 Bytes');
  });
});

describe('streamNameParts', () => {
  it('names a stream by its path’s tail and keeps the host for the sub-line', () => {
    expect(streamNameParts('vod-adaptive.akamaized.example/…/7f1e/playlist.m3u8')).toEqual({
      name: '7f1e/playlist.m3u8',
      host: 'vod-adaptive.akamaized.example',
    });
    expect(streamNameParts('cdn.example.com/a/b.mpd')).toEqual({ name: 'a/b.mpd', host: 'cdn.example.com' });
    expect(streamNameParts('cdn.example.com')).toEqual({ name: 'cdn.example.com', host: '' });
    expect(streamNameParts('cdn.example.com/')).toEqual({ name: 'cdn.example.com/', host: 'cdn.example.com' });
  });
});

describe('statusState', () => {
  const ok = { text: 'Connected', tone: 'ok' } as const;
  const failed = { text: 'Not available: x', tone: 'error' } as const;
  it('follows the relay first, then the daemon', () => {
    expect(statusState(undefined, false, undefined)).toBe('checking');
    expect(statusState({ text: 'Connecting…', tone: 'warn' }, false, undefined)).toBe('connecting');
    expect(statusState(failed, false, { text: 'x', tone: 'error' })).toBe('offline');
    expect(statusState(failed, true, undefined)).toBe('incompatible');
    expect(statusState(ok, false, undefined)).toBe('checking-daemon');
    expect(statusState(ok, false, { text: 'Daemon running', tone: 'ok' })).toBe('ready');
    expect(statusState(ok, false, { text: 'Daemon is not running', tone: 'warn' })).toBe('daemon-stopped');
    expect(statusState(ok, false, { text: 'Daemon status unavailable: timeout', tone: 'error' })).toBe('daemon-error');
  });
});

describe('statusChip', () => {
  it('says the state in a few words, with the active downloads while connected', () => {
    expect(statusChip('ready', { active: 3 })).toEqual({ text: 'Connected · 3 active', tone: 'ok' });
    expect(statusChip('ready', { active: 0 })).toEqual({ text: 'Connected', tone: 'ok' });
    expect(statusChip('ready')).toEqual({ text: 'Connected', tone: 'ok' });
    expect(statusChip('checking-daemon', { active: 5 })).toEqual({ text: 'Connected', tone: 'ok' });
    expect(statusChip('checking')).toEqual({ text: 'Connecting…', tone: 'warn' });
    expect(statusChip('connecting')).toEqual({ text: 'Connecting…', tone: 'warn' });
    expect(statusChip('daemon-stopped')).toEqual({ text: 'Not running', tone: 'done' });
    expect(statusChip('offline')).toEqual({ text: 'Not running', tone: 'done' });
    expect(statusChip('offline', { hostMissing: true })).toEqual({ text: 'Not set up', tone: 'fail' });
    expect(statusChip('incompatible')).toEqual({ text: 'Update needed', tone: 'warn' });
    expect(statusChip('daemon-error')).toEqual({ text: 'Not responding', tone: 'warn' });
  });

  it('reads the chip from the relay and the daemon as the popup gets them', () => {
    const relay = connectionLabel({ state: 'connected', daemonVersion: '0.7.3', since: 0 });
    const daemon = daemonLabel({ ok: true, value: { type: 'status', running: true, active: 2, queued: 1 } });
    expect(statusChip(statusState(relay, false, daemon), { active: 2 }).text).toBe('Connected · 2 active');
    const missing = connectionLabel({ state: 'disconnected', lastError: 'Specified native messaging host not found.', since: 0 });
    expect(hostMissing(missing)).toBe(true);
    expect(hostMissing(connectionLabel({ state: 'disconnected', lastError: 'Native host has exited.', since: 0 }))).toBe(false);
    expect(hostMissing(undefined)).toBe(false);
    expect(statusChip(statusState(missing, false, undefined), { hostMissing: hostMissing(missing) }).text).toBe('Not set up');
  });

  it('puts the relay’s and the daemon’s full lines in the tooltip, without the fix', () => {
    const relay = connectionLabel({ state: 'connected', daemonVersion: '0.7.3', since: 0 });
    const daemon = daemonLabel({ ok: true, value: { type: 'status', running: true, active: 2, queued: 1 } });
    expect(statusTooltip(relay, daemon)).toBe('Connected (Downex 0.7.3). Daemon running: 2 active, 1 queued');
    expect(statusTooltip(relay, undefined)).toBe('Connected (Downex 0.7.3)');
    expect(statusTooltip(undefined, undefined)).toBe('Checking connection…');
    // Polite mode adds a sentence only while it slows downloads; the chip's text is unchanged.
    expect(statusTooltip(relay, daemon, 'slowed')).toBe(
      'Connected (Downex 0.7.3). Daemon running: 2 active, 1 queued. Polite: slowed to keep the line smooth.',
    );
    for (const p of ['off', 'idle', 'watching', 'measuring', 'no_signal', 'unavailable', undefined] as const) {
      expect(statusTooltip(relay, daemon, p)).toBe('Connected (Downex 0.7.3). Daemon running: 2 active, 1 queued');
    }
    expect(statusTooltip(relay, undefined, 'slowed')).toBe('Connected (Downex 0.7.3)');
    const missing = connectionLabel({ state: 'disconnected', lastError: 'Specified native messaging host not found.', since: 0 });
    const unknown = daemonLabel({ ok: false, error: { code: 'disconnected', message: 'Specified native messaging host not found.' } });
    expect(statusTooltip(missing, unknown)).toBe(`Not available: Specified native messaging host not found. ${DAEMON_UNKNOWN}`);
  });
});

describe('offlineView', () => {
  it('replaces the page’s videos only while the relay cannot be reached', () => {
    const ok = connectionLabel({ state: 'connected', since: 0 });
    for (const s of ['checking', 'connecting', 'checking-daemon', 'ready', 'daemon-stopped', 'daemon-error'] as const) {
      expect(offlineView(s, ok)).toBeUndefined();
    }
    expect(offlineView('offline', undefined)).toBeUndefined();
  });

  it('says Downex is not running, with the relay’s reason, and offers no start button', () => {
    const relay = connectionLabel({ state: 'disconnected', lastError: 'Native host has exited.', since: 0 });
    expect(offlineView('offline', relay)).toEqual({
      kind: 'not-running',
      title: 'Downex isn’t running',
      text: NOT_RUNNING_TEXT,
      detail: 'Native host has exited.',
    });
    expect(NOT_RUNNING_TEXT).toBe('Downex starts with the next download you send. Until then, downloads stay in the browser.');
  });

  it('names the command that registers Downex when the browser cannot find it', () => {
    const relay = connectionLabel({ state: 'disconnected', lastError: 'Specified native messaging host not found.', since: 0 });
    expect(offlineView('offline', relay)).toEqual({
      kind: 'not-set-up',
      title: 'Downex isn’t set up in this browser',
      text: 'Install or reinstall Downex, or run this command, then press Check again.',
      command: 'downex-nmhost register',
      detail: 'Specified native messaging host not found.',
    });
  });

  it('asks for the latest Downex when it refuses this extension’s ID, with no command', () => {
    const relay = connectionLabel({ state: 'disconnected', lastError: 'Access to the specified native messaging host is forbidden.', since: 0 });
    expect(hostMissing(relay)).toBe(true);
    expect(statusChip('offline', { hostMissing: hostMissing(relay) })).toEqual({ text: 'Not set up', tone: 'fail' });
    expect(offlineView('offline', relay)).toEqual({
      kind: 'not-set-up',
      title: 'Downex doesn’t accept this extension',
      text: 'Install the latest Downex, then press Check again.',
      detail: 'Access to the specified native messaging host is forbidden.',
    });
    const unknown = daemonLabel({ ok: false, error: { code: 'disconnected', message: 'Access to the specified native messaging host is forbidden.' } });
    expect(statusTooltip(relay, unknown)).toBe(`Not available: Access to the specified native messaging host is forbidden. ${DAEMON_UNKNOWN}`);
  });

  it('asks for an update on a protocol mismatch, and says when the background itself is gone', () => {
    const relay = connectionLabel({ state: 'disconnected', incompatibleProtocol: 2, since: 0 });
    expect(offlineView('incompatible', relay)).toMatchObject({ kind: 'update', title: 'Downex needs an update', text: 'Relay protocol 2. Update Downex or this extension.' });
    expect(offlineView('offline', { text: 'Background unavailable: Receiving end does not exist.', tone: 'error' })).toMatchObject({
      kind: 'background',
      detail: 'Receiving end does not exist.',
    });
  });
});

describe('daemonNote', () => {
  it('explains a stopped daemon and offers Check again when it does not answer', () => {
    expect(daemonNote('daemon-stopped', undefined)).toEqual({ text: 'Downex isn’t running. It starts with the next download you send.', tone: 'muted', retry: false });
    const timeout = daemonLabel({ ok: false, error: { code: 'timeout', message: 'no answer' } });
    expect(daemonNote('daemon-error', timeout)).toEqual({ text: 'Downex isn’t answering: No answer', tone: 'warn', retry: true });
    expect(daemonNote('ready', undefined)).toBeUndefined();
    expect(daemonNote('offline', timeout)).toBeUndefined();
  });
});

describe('daemonLine', () => {
  it('mutes the daemon line while the relay itself fails, instead of repeating its error', () => {
    const relay = connectionLabel({ state: 'disconnected', lastError: 'Native host has exited.', since: 0 });
    const daemon = daemonLabel({ ok: false, error: { code: 'disconnected', message: 'Native host has exited.' } });
    expect(daemonLine(relay, daemon)).toEqual({ text: DAEMON_UNKNOWN, tone: 'muted' });
    expect(DAEMON_UNKNOWN).toBe('Daemon status unavailable');
  });

  it('keeps the daemon line as it is while the relay is connected or connecting', () => {
    const daemon = daemonLabel({ ok: false, error: { code: 'timeout', message: 'no answer' } });
    expect(daemonLine({ text: 'Connected', tone: 'ok' }, daemon)).toEqual(daemon);
    expect(daemonLine({ text: 'Connecting…', tone: 'warn' }, daemon)).toEqual(daemon);
    expect(daemonLine(undefined, daemon)).toEqual(daemon);
    const stopped = daemonLabel({ ok: true, value: { type: 'status', running: false, active: 0, queued: 0 } });
    expect(daemonLine({ text: 'Not available', tone: 'error' }, stopped)).toEqual(stopped);
  });
});

describe('marks and hand-off pills', () => {
  const rec = (over: Partial<HandoffRecord>): HandoffRecord => ({ ts: 0, url: 'https://a.example/f.zip', source: 'capture', ok: true, ...over });
  it('gives the same message the same tone and glyph in rows and in the history', () => {
    expect(handoffMark(rec({ awaiting: false }))).toEqual(addedMark(DOWNLOADING_IN_DOWNEX));
    expect(handoffMark(rec({ awaiting: true }))).toEqual(addedMark(CONFIRM_IN_DOWNEX));
    expect(addedMark(DOWNLOADING_IN_DOWNEX)).toEqual({ tone: 'ok', glyph: 'inbox' });
    expect(addedMark(CONFIRM_IN_DOWNEX)).toEqual({ tone: 'info', glyph: 'awaiting' });
  });

  it('marks a kept browser copy and a failure apart', () => {
    expect(handoffMark(rec({ duplicate: true }))).toEqual({ tone: 'warn', glyph: 'copy' });
    expect(handoffMark(rec({ duplicate: true, awaiting: false }))).toEqual({ tone: 'ok', glyph: 'inbox' });
    expect(handoffMark(rec({ ok: false }))).toEqual({ tone: 'error', glyph: 'failed' });
  });

  it('says what was recorded, never live progress', () => {
    expect(handoffPill(rec({ awaiting: false }))).toEqual({ text: 'Sent to Downex', tone: 'ok', title: 'Downloading in Downex.', note: '' });
    expect(handoffPill(rec({ awaiting: true }))).toEqual({
      text: 'Waiting in Downex',
      tone: 'info',
      title: 'Waiting for confirmation in Downex. Downex asks where to save downloads from the browser unless that is turned off in its settings.',
      note: '',
    });
    expect(handoffPill(rec({ duplicate: true }))).toMatchObject({ text: 'Already in Downex', tone: 'warn', note: 'The browser kept its own copy too.' });
    expect(handoffPill(rec({ ok: false, error: { code: 'daemon_unavailable', message: 'downexd is not reachable' } }))).toEqual({
      text: 'Not sent',
      tone: 'fail',
      title: 'Failed: downexd is not reachable.',
      note: 'downexd is not reachable',
    });
    expect(handoffPill(rec({ ok: false }))).toMatchObject({ text: 'Not sent', tone: 'fail', note: '' });
    expect(handoffPill(rec({ source: 'video', awaiting: false }))).toMatchObject({ title: 'Downloading in Downex. Downex saves it as one MP4 file.' });
    // A download that failed in the browser and Downex finishes (BROWSER-SPEC §6).
    expect(handoffPill(rec({ source: 'rescue', awaiting: false, keptBytes: 9 }))).toEqual({
      text: 'Finished in Downex',
      tone: 'ok',
      title: 'Finished in Downex. Downex continues it from the bytes the browser saved.',
      note: '',
    });
  });

  it('dates a hand-off by the day: the time today, "Yesterday", else a date', () => {
    const now = new Date(2026, 9, 28, 15, 30).getTime();
    expect(handoffTime(new Date(2026, 9, 28, 14, 2).getTime(), now, 'en-GB')).toBe('14:02');
    expect(handoffTime(new Date(2026, 9, 27, 13, 41).getTime(), now, 'en-GB')).toBe('Yesterday 13:41');
    expect(handoffTime(new Date(2026, 9, 20, 9, 5).getTime(), now, 'en-GB')).toBe('20/10/2026, 09:05');
  });
});

describe('the video card and the quality select', () => {
  it('shortens a quality for the sub-line and names the source', () => {
    expect(shortQuality('4K (2160p)')).toBe('4K');
    expect(shortQuality('1080p (Full HD) 60fps')).toBe('1080p 60fps');
    expect(shortQuality('480p')).toBe('480p');
    expect(videoSubline('YouTube', false, '4K (2160p)')).toBe('YouTube · up to 4K · MP4');
    expect(videoSubline('Vimeo', true, '')).toBe('Vimeo · Embedded · MP4');
  });

  it('shows a known duration as the player does', () => {
    expect(durationText(317_000)).toBe('5:17');
    expect(durationText(3_723_000)).toBe('1:02:03');
    expect(durationText(59_400)).toBe('0:59');
    expect(durationText(0)).toBe('');
    expect(durationText(-1)).toBe('');
  });

  it('lists one option per quality class: name, codecs and size, the picture as tooltip', () => {
    expect(
      qualityOptions([
        { id: 'v1', quality: '4K (2160p)', codecs: 'AV1', size: '1.9 GB', picture: '3840×2160' },
        { id: 'v2', quality: '480p', codecs: '', size: '', picture: '' },
      ]),
    ).toEqual([
      { value: 'v1', label: '4K (2160p)', detail: 'AV1', meta: '1.9 GB', title: '3840×2160' },
      { value: 'v2', label: '480p' },
    ]);
  });

  it('badges the recommended quality (VIDEO-DETECT-SPEC §5.2)', () => {
    expect(qualityOptions([{ id: 'v1', quality: '1080p', codecs: '', size: '', picture: '', recommended: true }])).toEqual([
      { value: 'v1', label: '1080p', badge: 'Recommended' },
    ]);
  });

  it('shows "No cookies sent" by Find the video only while the sign-in option is off', () => {
    // Off by default since the second 2026-11-18 amendment (SITE-MEDIA-SPEC §6.3).
    expect(showsNoCookies(DEFAULT_VIDEO_OPTIONS)).toBe(true);
    expect(showsNoCookies({ ...DEFAULT_VIDEO_OPTIONS, useSignInForVideos: true })).toBe(false);
  });
});

describe('the "Send downloads to Downex" switch', () => {
  const page = 'https://files.example/a';
  it('is the inverse of the stored exclusion and writes the same rule "Disable on this site" did', () => {
    const rules = defaultRules();
    expect(siteSendSwitch(rules, page)).toMatchObject({ host: 'files.example', checked: true, disabled: false });
    // Off: the exact host joins the excluded hosts, as the old checkbox stored it.
    const off = rulesForSiteSwitch(rules, 'files.example', false);
    expect(off.excludedHosts).toEqual(['files.example']);
    expect(siteExclusion(off, 'files.example')).toEqual({ excluded: true, exact: ['files.example'], patterns: [] });
    expect(siteSendSwitch(off, page)).toMatchObject({ checked: false, disabled: false, hint: 'Downloads from or started on files.example stay in the browser.' });
    // On again: the entry is gone, everything else as it was.
    const on = rulesForSiteSwitch(off, 'files.example', true);
    expect(on).toEqual(rules);
    expect(siteSendSwitch(on, page).checked).toBe(true);
    // Other entries and fields are kept.
    const busy = { ...rules, enabled: false, minSizeBytes: 1024, excludedHosts: ['other.example'] };
    expect(rulesForSiteSwitch(busy, 'files.example', false)).toEqual({ ...busy, excludedHosts: ['other.example', 'files.example'] });
  });
});

describe('file tiles', () => {
  it('maps every file kind to a tile of its own', () => {
    const kinds: FileKind[] = ['video', 'audio', 'archive', 'document', 'image', 'program', 'other'];
    expect(kinds.map(tileKind)).toEqual(['video', 'audio', 'archives', 'documents', 'images', 'programs', 'other']);
    const bodies = Object.values(FILE_TILES);
    expect(new Set(bodies).size).toBe(bodies.length);
    for (const body of bodies) expect(body).toContain('var(--hue)');
  });

  it('crops to the drawn area and keeps three bars on a small audio disc, as the desktop does', () => {
    expect(TILE_VIEWBOX).toBe('2 2 28 28');
    expect(SMALL_TILE_PX).toBe(20);
    expect(FILE_TILE_AUDIO_SMALL).toContain('M11 12v8M16 9v14M21 12v8');
    expect(FILE_TILE_AUDIO_SMALL).toContain('stroke-width="2.8"');
    expect(FILE_TILES.audio.match(/M[\d.]+ [\d.]+v/g)).toHaveLength(5);
  });

  it('never draws a document as a page with text lines', () => {
    // Two sheets and a bookmark ribbon: filled shapes only, no stroked lines.
    expect(FILE_TILES.documents).not.toContain('stroke');
    expect(FILE_TILES.documents).toContain('var(--deep)');
  });
});

describe('brand mark', () => {
  it('lays the mark out like internal/brand: full bleed below 32 px, inset with a hairline from 32 px', () => {
    const small = brandLayout(22);
    expect(small).toMatchObject({ x0: 0, x1: 22, hairline: 0 });
    expect(small.radius).toBeCloseTo(4.8125);
    // 3 px bars with 1 px gaps on whole pixels; the third bar split, its live chunk at the end.
    expect(small.bars.map((b) => [b.x, b.y, b.w, b.h, b.head])).toEqual([
      [4, 3, 9, 3, false],
      [4, 7, 14, 3, false],
      [4, 11, 9, 3, false],
      [14, 11, 4, 3, true],
      [4, 15, 9, 3, false],
    ]);
    const big = brandLayout(32);
    expect(big.hairline).toBe(1);
    expect(big.x0).toBe(1);
    expect(big.bars.every((b) => Number.isInteger(b.x) && Number.isInteger(b.y))).toBe(true);
  });

  it('rounds a bar’s right end only when it is tall enough', () => {
    expect(barPath({ x: 1, y: 2, w: 10, h: 3, r: 0, head: false })).toBe('M1 2h10v3h-10z');
    expect(barPath({ x: 1, y: 2, w: 10, h: 4, r: 2, head: false })).toBe('M1 2h8a2 2 0 0 1 0 4h-8z');
  });
});

describe('the renewal check pill (DESKTOP-SPEC §10)', () => {
  it('says Downex is checking the link, in the info tone', () => {
    const rec: HandoffRecord = { ts: 1, url: 'https://a/f.zip', source: 'capture', ok: true, downloadId: 3, renewCheck: true };
    expect(handoffPill(rec)).toEqual({ text: RENEW_CHECK_PILL, tone: 'info', title: RENEW_CHECK_TITLE, note: '' });
    expect(RENEW_CHECK_PILL).toBe('Checking in Downex');
    expect(RENEW_CHECK_TITLE).toBe('Downex is checking whether this link continues a stopped download.');
    expect(handoffMark(rec)).toEqual({ tone: 'info', glyph: 'awaiting' });
    expect(handoffPill({ ...rec, ok: false, error: { code: 'rejected', message: 'no' } }).text).not.toBe(RENEW_CHECK_PILL);
  });
});

describe('a video row list note (SITE-MEDIA-SPEC §2.8, §6.3)', () => {
  it('shows what the site note adds to the MP4 note the select says', () => {
    expect(listNoteExtra(MP4_NOTE, MP4_NOTE)).toBe('');
    expect(listNoteExtra(siteNote({ browserFetch: true }, MP4_NOTE), MP4_NOTE)).toBe(BROWSER_FETCH_NOTE);
    expect(listNoteExtra(siteNote({ signedIn: 'media.example', browserFetch: true }, MP4_NOTE), MP4_NOTE)).toBe(
      `Used your sign-in for media.example. ${BROWSER_FETCH_NOTE}`,
    );
  });
});
