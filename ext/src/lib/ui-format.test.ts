import { describe, expect, it } from 'vitest';
import type { HandoffRecord } from './history';
import { defaultRules } from './rules';
import {
  CONFIRM_IN_DOWNEX,
  DOWNLOADING_IN_DOWNEX,
  ENCRYPTED_STREAM_TEXT,
  HOST_FORBIDDEN_HINT,
  HOST_MISSING_HINT,
  SENT_TO_DOWNEX,
  addedTone,
  initialCapital,
  LIVE_STREAM_TEXT,
  MEDIA_TOO_SLOW_TEXT,
  connectionLabel,
  daemonLabel,
  fileKind,
  formatLabel,
  mediaFailureText,
  mediaName,
  mediaRefusal,
  recordName,
  recordResult,
  recordResultDetail,
  SEND_HINT,
  siteSendSwitch,
  sourceLabel,
  canRetryVideo,
  gettingInfoText,
  isVideoRetry,
  toolsInstallingText,
  videoAddedText,
  videoFailureText,
  fileFailureText,
  fileLabel,
  hiddenSitesField,
  hiddenSitesResult,
  hideOnText,
  moreSourcesText,
  OFFER_TEXTS,
  offerAddedText,
  otherVideosText,
  REFUSAL_LABELS,
  BUTTONS_HINT,
  shortClipsText,
  siteButtonToggle,
  streamFailureText,
  streamRefusal,
} from './ui-format';
import { DEFAULT_VIDEO_OPTIONS } from './options-form';
import { daemonCode, daemonDetail, type ProtocolError } from './protocol';

const rec = (over: Partial<HandoffRecord>): HandoffRecord => ({
  ts: 0,
  url: 'https://dl.example.com/files/My%20File.zip?x=1',
  source: 'capture',
  ok: true,
  ...over,
});

describe('popup labels', () => {
  it('describes the relay connection', () => {
    expect(connectionLabel({ state: 'connected', daemonVersion: '1.2.0', since: 0 })).toEqual({
      text: 'Connected (Downex 1.2.0)',
      tone: 'ok',
    });
    expect(connectionLabel({ state: 'connecting', since: 0 }).tone).toBe('warn');
    expect(connectionLabel({ state: 'disconnected', lastError: 'host not found', since: 0 })).toEqual({
      text: 'Not available: host not found',
      tone: 'error',
    });
    expect(connectionLabel({ state: 'idle', since: 0 }).text).toBe('Not available');
  });

  it('tells how to recover when the browser cannot find the relay', () => {
    for (const lastError of [
      'Specified native messaging host not found.',
      'No such native application com.downex.host',
    ]) {
      expect(connectionLabel({ state: 'disconnected', lastError, since: 0 })).toEqual({
        text: `Not available: ${lastError} ${HOST_MISSING_HINT}`,
        tone: 'error',
      });
      expect(daemonLabel({ ok: false, error: { code: 'disconnected', message: lastError } }).text).toBe(
        'Daemon status unavailable: the browser cannot reach Downex yet',
      );
    }
    // The popup's not-set-up state has a "Check again" button: the hint names it.
    expect(HOST_MISSING_HINT).toBe(
      'Downex is not registered with this browser: install or reinstall Downex, or run "downex-nmhost register", then press Check again.',
    );
  });

  it('tells a refused extension ID apart from a missing relay: only a newer Downex helps', () => {
    // Chrome's text when the host's allowed_origins do not name this extension (a store's ID).
    const lastError = 'Access to the specified native messaging host is forbidden.';
    expect(connectionLabel({ state: 'disconnected', lastError, since: 0 })).toEqual({
      text: `Not available: ${lastError} ${HOST_FORBIDDEN_HINT}`,
      tone: 'error',
    });
    expect(daemonLabel({ ok: false, error: { code: 'disconnected', message: lastError } }).text).toBe(
      'Daemon status unavailable: the browser cannot reach Downex yet',
    );
    expect(HOST_FORBIDDEN_HINT).toBe('Downex does not accept this copy of the extension: install the latest Downex, then press Check again.');
    // Other relay failures keep their own words.
    expect(connectionLabel({ state: 'disconnected', lastError: 'Native host has exited.', since: 0 }).text).toBe(
      'Not available: Native host has exited.',
    );
  });

  it('describes the daemon status', () => {
    expect(daemonLabel({ ok: true, value: { type: 'status', running: true, active: 2, queued: 3 } })).toEqual({
      text: 'Daemon running: 2 active, 3 queued',
      tone: 'ok',
    });
    expect(daemonLabel({ ok: true, value: { type: 'status', running: false, active: 0, queued: 0 } }).tone).toBe('warn');
    expect(daemonLabel({ ok: false, error: { code: 'timeout', message: 'no answer' } }).text).toContain('no answer');
  });

  it('names a hand-off by file name, URL segment or host', () => {
    expect(recordName(rec({ filename: 'a.iso' }))).toBe('a.iso');
    expect(recordName(rec({}))).toBe('My File.zip');
    expect(recordName(rec({ url: 'https://example.com/' }))).toBe('example.com');
    expect(recordName(rec({ url: 'https://example.com/%E0%A4%A' }))).toBe('%E0%A4%A');
    expect(recordName(rec({ url: 'nonsense' }))).toBe('nonsense');
  });

  it('describes results and sources', () => {
    // Every relay `added` is a browser hand-off the daemon may hold for confirmation.
    expect(recordResult(rec({ downloadId: 4 }))).toBe('Waiting for confirmation in Downex');
    expect(recordResultDetail(rec({}))).toContain('settings');
    expect(recordResult(rec({ duplicate: true }))).toBe(
      'Waiting for confirmation in Downex; the browser kept its own copy too',
    );
    expect(recordResult(rec({ ok: false, error: { code: 'rejected', message: 'bad url' } }))).toBe('Failed: bad url');
    expect(recordResultDetail(rec({ ok: false }))).toBeUndefined();
    expect(sourceLabel('page-links')).toBe('Page links');
    expect(sourceLabel('other')).toBe('other');
  });

  it('reports an incompatible relay', () => {
    const label = connectionLabel({
      state: 'disconnected',
      lastError: 'the Downex relay speaks protocol 2, this extension speaks protocol 1',
      incompatibleProtocol: 2,
      since: 0,
    });
    expect(label.tone).toBe('error');
    expect(label.text).toBe(
      'Incompatible Downex: the Downex relay speaks protocol 2, this extension speaks protocol 1. Update Downex or this extension.',
    );
  });
});

describe('fileKind', () => {
  it('picks the icon kind from the file name, else the URL path; media is video', () => {
    expect(fileKind(rec({ filename: 'Setup.EXE' }))).toBe('program');
    expect(fileKind(rec({ filename: 'backup.tar.gz' }))).toBe('archive');
    expect(fileKind(rec({ url: 'https://files.example/docs/report.pdf?x=1' }))).toBe('document');
    expect(fileKind(rec({ url: 'https://files.example/song.flac' }))).toBe('audio');
    expect(fileKind(rec({ url: 'https://files.example/a.png' }))).toBe('image');
    expect(fileKind(rec({ url: 'https://cdn.example/master.m3u8', source: 'media' }))).toBe('video');
    expect(fileKind(rec({ url: 'https://files.example/download' }))).toBe('other');
    expect(fileKind(rec({ url: 'not a url' }))).toBe('other');
  });
});

describe('siteSendSwitch', () => {
  const rules = (excludedHosts: string[], enabled = true) => ({ ...defaultRules(), excludedHosts, enabled });

  it('is on and usable on a site that is not excluded', () => {
    expect(siteSendSwitch(rules([]), 'https://Files.Example/a')).toEqual({
      host: 'files.example',
      checked: true,
      disabled: false,
      hint: SEND_HINT,
    });
    expect(SEND_HINT).toBe('Hold Alt while clicking to keep one in the browser');
  });

  it('says that nothing is sent while capture is off everywhere', () => {
    expect(siteSendSwitch(rules([], false), 'https://files.example/')).toMatchObject({
      checked: true,
      hint: 'Capture browser downloads is off, so downloads stay in the browser.',
    });
  });

  it('is off and usable when the site has its own entry', () => {
    expect(siteSendSwitch(rules(['files.example']), 'https://files.example/')).toEqual({
      host: 'files.example',
      checked: false,
      disabled: false,
      hint: 'Downloads from or started on files.example stay in the browser.',
    });
  });

  it('is off and locked when a wildcard of the options page excludes the site', () => {
    expect(siteSendSwitch(rules(['files.example', '*.example']), 'https://files.example/')).toMatchObject({
      checked: false,
      disabled: true,
      hint: 'Excluded by *.example; change it on the options page.',
    });
  });

  it('is unavailable on pages that are not http(s)', () => {
    const t = siteSendSwitch(rules([]), 'chrome://newtab/');
    expect(t).toEqual({ checked: true, disabled: true, hint: 'Only web pages (http and https) can be excluded.' });
    expect(t.host).toBeUndefined();
    expect(siteSendSwitch(rules([]), undefined).disabled).toBe(true);
  });
});

describe('media on this page', () => {
  it('names a stream by host and path tail', () => {
    expect(mediaName('https://cdn.example.com/v/123/hls/master.m3u8?token=abc')).toBe('cdn.example.com/…/hls/master.m3u8');
    expect(mediaName('https://cdn.example.com/a/b.mpd')).toBe('cdn.example.com/a/b.mpd');
    expect(mediaName('https://cdn.example.com/My%20Show.m3u8')).toBe('cdn.example.com/My Show.m3u8');
    expect(mediaName('https://cdn.example.com/%E0%A4%A.m3u8')).toBe('cdn.example.com/%E0%A4%A.m3u8');
    expect(mediaName('https://cdn.example.com/?x=1')).toBe('cdn.example.com');
    expect(mediaName('nonsense')).toBe('nonsense');
    expect(formatLabel('hls')).toBe('HLS');
    expect(formatLabel('dash')).toBe('DASH');
  });

  it('understands encrypted and live refusals', () => {
    expect(mediaRefusal({ code: 'rejected', message: 'encrypted' })).toBe('encrypted');
    expect(mediaRefusal({ code: 'rejected', message: 'live: the playlist has no EXT-X-ENDLIST' })).toBe('live');
    expect(mediaRefusal({ code: 'rejected', message: 'stream is DRM-protected (ContentProtection)' })).toBe('encrypted');
    expect(mediaRefusal({ code: 'rejected', message: 'this is a live stream' })).toBe('live');
    expect(mediaRefusal({ code: 'encrypted', message: 'x' })).toBe('encrypted');
    expect(mediaRefusal({ code: 'live', message: 'x' })).toBe('live');
    expect(mediaRefusal({ code: 'rejected', message: 'not alive or delivered' })).toBeUndefined();
    expect(mediaRefusal({ code: 'internal', message: 'encrypted' })).toBeUndefined();
    expect(mediaRefusal(undefined)).toBeUndefined();
  });

  it('words failures for users', () => {
    expect(mediaFailureText({ code: 'rejected', message: 'encrypted' })).toBe(ENCRYPTED_STREAM_TEXT);
    expect(mediaFailureText({ code: 'rejected', message: 'live' })).toBe(LIVE_STREAM_TEXT);
    expect(mediaFailureText({ code: 'daemon_unavailable', message: 'cannot start' })).toBe('Downex is not running: cannot start');
    // The daemon reads the manifests before it answers: a deadline is a slow stream server.
    expect(
      mediaFailureText({ code: 'daemon_unavailable', message: 'downexd is not reachable: context deadline exceeded' }),
    ).toBe(MEDIA_TOO_SLOW_TEXT);
    expect(mediaFailureText({ code: 'daemon_unavailable', message: 'downexd did not answer in time' })).toBe(
      MEDIA_TOO_SLOW_TEXT,
    );
    expect(MEDIA_TOO_SLOW_TEXT).toContain('took too long to read the stream');
    // … but not a daemon that is still starting.
    expect(
      mediaFailureText({
        code: 'daemon_unavailable',
        message: 'downexd is starting and did not answer in time: context deadline exceeded',
      }),
    ).toBe('Downex is not running: downexd is starting and did not answer in time: context deadline exceeded');
    expect(
      mediaFailureText({ code: 'daemon_unavailable', message: 'downexd is not reachable: connection refused' }),
    ).toBe('Downex is not running: downexd is not reachable: connection refused');
    expect(mediaFailureText({ code: 'timeout', message: 'no addMedia response within 15000 ms' })).toContain('did not answer');
    expect(mediaFailureText({ code: 'rejected', message: 'manifest: HTTP 403' })).toBe('manifest: HTTP 403');
  });

  it('shows media hand-offs in the history by title or stream name, with readable refusals', () => {
    const media = rec({ url: 'https://cdn.example.com/v/1/hls/master.m3u8', source: 'media' });
    expect(recordName({ ...media, title: 'My video' })).toBe('My video');
    expect(recordName(media)).toBe('cdn.example.com/…/hls/master.m3u8');
    expect(sourceLabel('media')).toBe('Media');
    expect(recordResult(media)).toBe('Waiting for confirmation in Downex');
    expect(recordResult({ ...media, ok: false, error: { code: 'rejected', message: 'encrypted' } })).toBe(
      `Failed: ${ENCRYPTED_STREAM_TEXT}`,
    );
    expect(recordResult({ ...media, ok: false, error: { code: 'rejected', message: 'live' } })).toBe(
      `Failed: ${LIVE_STREAM_TEXT}`,
    );
    // Other sources keep the relay's words.
    expect(recordResult(rec({ ok: false, error: { code: 'rejected', message: 'live' } }))).toBe('Failed: live');
  });
});

describe('videos of web pages', () => {
  const rejected = (message: string, reason?: string): ProtocolError =>
    reason === undefined ? { code: 'rejected', message } : { code: 'rejected', message, reason };

  it('reads the daemon code from the reason, else from the message', () => {
    expect(daemonCode(rejected('auth_required: the site asks to sign in', 'auth_required'))).toBe('auth_required');
    expect(daemonCode(rejected('live: this is live'))).toBe('live');
    expect(daemonCode(rejected('Live stream'))).toBe('rejected');
    expect(daemonCode({ code: 'timeout', message: 'x' })).toBe('timeout');
    expect(daemonDetail(rejected('site_error: yt-dlp could not read this page: HTTP Error 500', 'site_error'))).toBe(
      'yt-dlp could not read this page: HTTP Error 500',
    );
  });

  it.each([
    ['site_unsupported', 'site_unsupported: no video found on this page', 'Downex found no video on this page.'],
    [
      'site_unavailable',
      'site_unavailable: this video is not available: Private video',
      'This video is not available (private, removed, blocked in your country, or the site asks to try again later).',
    ],
    ['auth_required', 'auth_required: the site asks to sign in: Sign in to confirm your age', 'The site wants you to sign in (age check, members-only or bot check).'],
    ['live', 'live: media: the stream is live', 'This is a live stream. Downex downloads only finished videos.'],
    ['encrypted', 'encrypted: media: DRM', 'This video is DRM-protected. Downex does not download protected content.'],
    ['media_unsupported', 'media_unsupported: No MP4 video on this page', 'No MP4 video on this page.'],
    ['media_unsupported', 'media_unsupported: this is a playlist', 'Open a single video of this playlist.'],
    ['media_invalid', 'media_invalid: the chosen quality is no longer offered', 'The chosen quality is no longer offered. Add the video again.'],
    ['site_error', 'site_error: yt-dlp could not read this page: HTTP Error 500', 'yt-dlp could not read this page: HTTP Error 500.'],
    ['site_error', 'site_error: yt-dlp could not read this page', 'yt-dlp could not read this page.'],
    [
      'tools_unavailable',
      'tools_unavailable: yt-dlp and Deno are not set up: could not download yt-dlp (network)',
      'Downex could not set up yt-dlp/Deno: could not download yt-dlp (network).',
    ],
    [
      'site_media_off',
      'site_media_off: video downloads from web pages are turned off (Settings → Video sites)',
      'Video downloads from web pages are turned off (Settings → Video sites).',
    ],
    ['tools_installing', 'tools_installing: downloading yt-dlp and Deno (34%)', 'Preparing video downloads: downloading yt-dlp and Deno (34%)'],
  ])('%s: %s', (reason, message, text) => {
    expect(videoFailureText(rejected(message, reason))).toBe(text);
    // An older relay without reason: the message prefix says the same.
    expect(videoFailureText(rejected(message))).toBe(text);
  });

  it('words local and transport failures', () => {
    expect(videoFailureText({ code: 'outdated_downex', message: 'x' })).toBe('Update Downex to download videos.');
    expect(videoFailureText({ code: 'unsupported_page', message: 'x' })).toBe('Open a video page to download its video.');
    expect(videoFailureText({ code: 'daemon_unavailable', message: 'downexd did not answer in time' })).toBe(
      'Downex took too long to answer. Try again.',
    );
    expect(videoFailureText({ code: 'daemon_unavailable', message: 'cannot start downexd' })).toBe('Downex is not running: cannot start downexd');
    expect(videoFailureText({ code: 'timeout', message: 'x' })).toBe('Downex did not answer in time. Try again.');
    expect(videoFailureText({ code: 'expired', message: 'This video is no longer on the page.' })).toBe('This video is no longer on the page.');
  });

  it('asks again only while Downex sets up yt-dlp or resolves the page', () => {
    expect(isVideoRetry(rejected('tools_installing: downloading yt-dlp and Deno (3%)', 'tools_installing'))).toBe(true);
    expect(isVideoRetry(rejected('site_resolving: still getting the video info'))).toBe(true);
    expect(isVideoRetry(rejected('site_error: x', 'site_error'))).toBe(false);
    expect(canRetryVideo(rejected('site_error: x', 'site_error'))).toBe(true);
    expect(canRetryVideo({ code: 'timeout', message: 'x' })).toBe(true);
    for (const final of ['encrypted', 'live', 'media_unsupported', 'site_unsupported', 'auth_required', 'site_media_off']) {
      expect(canRetryVideo(rejected(final + ': x', final))).toBe(false);
    }
    expect(toolsInstallingText(rejected('tools_installing: ', 'tools_installing'))).toBe('Preparing video downloads: downloading yt-dlp and Deno');
    expect(gettingInfoText(0)).toBe('Getting video info…');
    expect(gettingInfoText(12_400)).toBe('Getting video info… 12 s');
  });

  it('says whether an added video waits for confirmation', () => {
    expect(videoAddedText({ type: 'added', downloadId: 1, awaiting: true })).toBe('Confirm in Downex');
    expect(videoAddedText({ type: 'added', downloadId: 1, awaiting: false })).toBe('Downloading in Downex');
    expect(videoAddedText({ type: 'added', downloadId: 1 })).toBe('Downloading in Downex');
  });

  it('shows direct video files in the history, and every source added without confirmation', () => {
    const f = rec({ source: 'video-file', url: 'https://cdn.example.com/v/clip.mp4', filename: 'Clip.mp4', awaiting: true });
    expect(sourceLabel('video-file')).toBe('Video file');
    expect(fileKind({ ...f, filename: undefined, url: 'https://cdn.example.com/v/clip' })).toBe('video');
    expect(recordName(f)).toBe('Clip.mp4');
    expect(recordResult(f)).toBe('Waiting for confirmation in Downex');
    expect(recordResult({ ...f, awaiting: false })).toBe('Downloading in Downex');
    expect(recordResultDetail({ ...f, awaiting: false })).toBe('Downex saves it as one MP4 file.');
    expect(recordResult({ ...rec({ source: 'media' }), awaiting: false })).toBe('Downloading in Downex');
    expect(recordResult({ ...f, ok: false, error: { code: 'expired', message: 'gone' } })).toBe(
      'Failed: This video is no longer listed. Play it again, then download it.',
    );
  });

  it('shows video hand-offs in the history', () => {
    const v = rec({ source: 'video', url: 'https://www.youtube.com/watch?v=aqz-KE-bpKQ', ok: true, downloadId: 3, awaiting: false });
    expect(sourceLabel('video')).toBe('Video');
    expect(fileKind(v)).toBe('video');
    expect(recordName(v)).toBe('https://www.youtube.com/watch?v=aqz-KE-bpKQ');
    expect(recordName({ ...v, title: 'A video' })).toBe('A video');
    expect(recordResult(v)).toBe('Downloading in Downex');
    expect(recordResult({ ...v, awaiting: true })).toBe('Waiting for confirmation in Downex');
    expect(recordResult({ ...v, ok: false, error: { code: 'rejected', message: 'live: x', reason: 'live' } })).toBe(
      'Failed: This is a live stream. Downex downloads only finished videos.',
    );
  });
});

describe('videos on every site', () => {
  const rejected = (message: string, reason?: string): ProtocolError =>
    reason === undefined ? { code: 'rejected', message } : { code: 'rejected', message, reason };

  it('labels files as the panel lists them', () => {
    const base = { display: 'cdn.example.com/clip.mp4', size: 50_593_792, sizeKnown: true, width: 1280, height: 720, matched: true };
    expect(fileLabel(base)).toEqual({
      quality: 'This video',
      detail: '720p (HD) · MP4',
      size: '50.6 MB',
      label: 'This video · 720p (HD) · MP4 · 50.6 MB',
      title: 'cdn.example.com/clip.mp4',
    });
    expect(fileLabel({ ...base, matched: false }).label).toBe('720p (HD) · MP4 · 50.6 MB');
    // The element's picture unknown (videoWidth and videoHeight are 0 until its metadata loads).
    const unknown = { width: 0, height: 0 };
    expect(fileLabel({ ...base, ...unknown, matched: false }).label).toBe('MP4 file · 50.6 MB');
    expect(fileLabel({ ...base, ...unknown, matched: false, sizeKnown: false }).label).toBe('MP4 file');
    expect(fileLabel({ ...base, ...unknown, sizeKnown: false }).label).toBe('This video · MP4');
  });

  it('names a file by the standard name of its picture, never its height', () => {
    const base = { display: 'cdn.example.com/film.mp4', size: 0, sizeKnown: false, matched: false };
    expect(fileLabel({ ...base, width: 3840, height: 1634 }).quality).toBe('4K (2160p)');
    expect(fileLabel({ ...base, width: 1280, height: 544 }).quality).toBe('720p (HD)');
    expect(fileLabel({ ...base, width: 1080, height: 1920 }).quality).toBe('1080p (Full HD)');
    // The element's size only: a width of 0 leaves the height to decide; -1 is unknown.
    expect(fileLabel({ ...base, width: 0, height: 480 }).quality).toBe('480p');
    expect(fileLabel({ ...base, width: -1, height: -1 }).quality).toBe('MP4 file');
  });

  it('says whether a file or stream waits for confirmation, and "Sent to Downex" when the relay does not say', () => {
    expect(offerAddedText({ type: 'added', downloadId: 1, awaiting: true })).toBe('Confirm in Downex');
    expect(offerAddedText({ type: 'added', downloadId: 1, awaiting: false })).toBe('Downloading in Downex');
    expect(offerAddedText({ type: 'added', downloadId: 1 })).toBe('Sent to Downex');
  });

  it('words file failures, with the expiry hint for address problems', () => {
    expect(fileFailureText({ code: 'expired', message: 'gone' })).toBe('This video is no longer listed. Play it again, then download it.');
    expect(fileFailureText(rejected('http_status: the server answered 403 Forbidden', 'http_status'))).toBe(
      'the server answered 403 Forbidden. The address may have expired. Play the video again, then download it.',
    );
    expect(fileFailureText(rejected('network: connection reset', 'network'))).toBe(
      'connection reset. The address may have expired. Play the video again, then download it.',
    );
    expect(fileFailureText({ code: 'timeout', message: 'x' })).toBe('Downex did not answer in time. Check its list before trying again.');
  });

  it('words stream failures and recognises refusals', () => {
    expect(streamFailureText(rejected('media_unsupported: no MP4 video', 'media_unsupported'))).toBe('No MP4 video in this stream.');
    expect(streamFailureText({ code: 'outdated_downex', message: 'x' })).toBe('Update Downex to choose a stream quality.');
    expect(streamFailureText({ code: 'expired', message: 'x' })).toBe('This video is no longer listed. Play it again, then download it.');
    expect(streamFailureText(rejected('encrypted: DRM', 'encrypted'))).toBe('This video is DRM-protected. Downex does not download protected content.');
    expect(streamFailureText(rejected('live: live', 'live'))).toBe('This is a live stream. Downex downloads only finished videos.');
    expect(streamFailureText({ code: 'daemon_unavailable', message: 'cannot start downexd' })).toBe('Downex is not running: cannot start downexd');
    expect(streamRefusal(rejected('encrypted: x', 'encrypted'))).toBe('encrypted');
    expect(streamRefusal(rejected('media_invalid: x', 'media_invalid'))).toBe('no_mp4');
    expect(streamRefusal(rejected('The stream is encrypted (DRM)'))).toBe('encrypted');
    expect(streamRefusal({ code: 'timeout', message: 'x' })).toBeUndefined();
    expect(REFUSAL_LABELS).toEqual({ encrypted: 'Protected', live: 'Live', no_mp4: 'No MP4 video' });
  });

  it('names the groups and actions', () => {
    expect(moreSourcesText(3)).toBe('More sources (3)');
    expect(otherVideosText(2)).toBe('Other videos on this page (2)');
    expect(shortClipsText(4)).toBe('Short clips (4)');
    expect(hideOnText('example.com')).toBe("Don't show on example.com");
    expect(OFFER_TEXTS.lookForMore).toBe('Look for more qualities');
    expect(OFFER_TEXTS.foundNothing).toBe('Downex found no video it can save here.');
  });

  it('shows the per-site button switch, locked by a wildcard entry or the global option', () => {
    const o = (hiddenSites: string[], videoButton = true) => ({ ...DEFAULT_VIDEO_OPTIONS, hiddenSites, videoButton });
    expect(siteButtonToggle(o([]), 'www.example.com')).toEqual({ host: 'www.example.com', checked: true, disabled: false, hint: BUTTONS_HINT });
    expect(BUTTONS_HINT).toBe('Shown over the player on this site');
    expect(siteButtonToggle(o(['www.example.com']), 'www.example.com')).toMatchObject({ checked: false, disabled: false });
    // The popup lists the page's videos on its "This page" tab: the hint names it.
    expect(siteButtonToggle(o(['www.example.com']), 'www.example.com').hint).toBe(
      'No button over the players of this site. "This page" still lists its videos.',
    );
    expect(siteButtonToggle(o(['*.example.com']), 'www.example.com')).toMatchObject({
      checked: false,
      disabled: true,
      hint: 'Hidden by *.example.com; change it on the options page.',
    });
    expect(siteButtonToggle(o([], false), 'www.example.com')).toMatchObject({ checked: false, disabled: true });
    expect(siteButtonToggle(o([]), undefined)).toMatchObject({ disabled: true, checked: false });
  });

  it('keeps the user’s edits of the hidden sites when a panel or the toolbar menu changes them', () => {
    // Not edited: the field shows the stored list.
    expect(hiddenSitesField('old.example', false, ['old.example'], ['old.example', 'new.example'])).toBe('old.example\nnew.example');
    // Edited: the user's text stays, with the host added elsewhere appended and the one removed taken out.
    expect(hiddenSitesField('typed.example\nold.example', true, ['old.example'], ['new.example'])).toBe('typed.example\nnew.example');
    expect(hiddenSitesField('typed.example  bad!', true, [], [])).toBe('typed.example  bad!');
  });

  it('checks the hidden-sites field with the excluded hosts’ messages', () => {
    expect(hiddenSitesResult('example.com\n*.video.example\n')).toEqual({ ok: true, sites: ['example.com', '*.video.example'] });
    expect(hiddenSitesResult('ok.example, host!, a/b c')).toEqual({ ok: false, error: 'Not a host name: "host!". Use e.g. example.com or *.example.com.' });
    const many = Array.from({ length: 502 }, (_, i) => `h${i}.example`).join('\n');
    const r = hiddenSitesResult(many);
    expect(r.ok && r.sites.length).toBe(500);
    expect(r.ok && r.note).toBe('Only the first 500 sites are kept; 2 more were left out.');
  });
});

describe('addedTone and initialCapital', () => {
  it('marks an accepted hand-off the same way everywhere: saved at once ok, else waiting (info)', () => {
    expect(addedTone(DOWNLOADING_IN_DOWNEX)).toBe('ok');
    expect(addedTone(CONFIRM_IN_DOWNEX)).toBe('info');
    expect(addedTone(SENT_TO_DOWNEX)).toBe('info');
  });

  it('starts a sentence with a capital, but leaves a lowercase program name as it is', () => {
    expect(initialCapital('the server answered 403 Forbidden.')).toBe('The server answered 403 Forbidden.');
    expect(initialCapital('yt-dlp could not read this page: HTTP Error 403: Forbidden.')).toBe('yt-dlp could not read this page: HTTP Error 403: Forbidden.');
    expect(initialCapital('downexd could not be started')).toBe('downexd could not be started');
    expect(initialCapital('downex-nmhost is missing')).toBe('downex-nmhost is missing');
    // Only the whole name: other words that start the same are capitalised.
    expect(initialCapital('downexdata is gone')).toBe('Downexdata is gone');
    expect(initialCapital('downex could not')).toBe('Downex could not');
    expect(initialCapital('')).toBe('');
  });
});
