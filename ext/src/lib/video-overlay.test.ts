// @vitest-environment happy-dom

import { createHash } from 'node:crypto';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { BROWSER_FETCH_NOTE } from './ui-format';
import { type AddedResult, failure, type Outcome, type VideoInfoResult, type VideoTrack } from './protocol';
import {
  cleanSiteText,
  findPlayer,
  ISOLATED_EVENTS,
  OVERLAY_CSS,
  OVERLAY_HOST_CSS,
  OVERLAY_SHARED_CSS,
  REVEAL_ACTIVITY_MS,
  SENT_CLOSE_MS,
  VideoOverlay,
  type VideoOverlayDeps,
  youtubeButtonEnabled,
} from './video-overlay';
import { DEFAULT_VIDEO_OPTIONS } from './options-form';
import type { TrackPick } from './video-picker';

function track(t: Partial<VideoTrack> & Pick<VideoTrack, 'id' | 'kind'>): VideoTrack {
  return { ext: 'mp4', codecs: '', width: 0, height: 0, frameRate: 0, bandwidth: 0, size: 0, sizeApprox: false, language: '', name: '', ...t };
}

const INFO: VideoInfoResult = {
  type: 'videoInfo',
  site: 'YouTube',
  title: 'Title ‮with⁦ bidi',
  durationMs: 60_000,
  format: 'http',
  container: 'mp4',
  selectedVideo: '299',
  selectedAudio: '140',
  tracks: [
    // A 2.35:1 film as downexd names it: by the class of its picture, not its height.
    track({ id: '401', kind: 'video', codecs: 'av01.0.12M.08', width: 3840, height: 1634, frameRate: 60, size: 1200 * 1_000_000, name: '4K (2160p) 60fps' }),
    track({ id: '299', kind: 'video', codecs: 'avc1.64002a', width: 1920, height: 818, frameRate: 60, size: 400 * 1_000_000, name: '1080p (Full HD) 60fps' }),
    track({ id: '136', kind: 'video', codecs: 'avc1.4d401f', width: 1280, height: 544, frameRate: 30, size: 200 * 1_000_000, name: '720p (HD)' }),
    track({ id: '140', kind: 'audio', ext: 'm4a', codecs: 'mp4a.40.2', bandwidth: 129_000, size: 12 * 1_000_000, language: 'en', name: 'English original' }),
  ],
};

/** Events the test marks as the user's (happy-dom has no isTrusted). */
const trusted = new WeakSet<Event>();

function setup(probeAnswer: Outcome<VideoInfoResult> = { ok: true, value: INFO }, extra: Partial<VideoOverlayDeps> = {}) {
  document.body.replaceChildren();
  const player = document.createElement('div');
  player.className = 'html5-video-player';
  const host = document.createElement('downex-video');
  player.append(host);
  document.body.append(player);
  const shadow = host.attachShadow({ mode: 'closed' });
  const container = document.createElement('div');
  shadow.append(container);
  const picks: TrackPick[] = [];
  let addAnswer: Outcome<AddedResult> = { ok: true, value: { type: 'added', downloadId: 4, awaiting: true } };
  const probe = vi.fn(async () => probeAnswer);
  const overlay = new VideoOverlay(document, {
    probe,
    add: async (pick) => {
      picks.push(pick);
      return addAnswer;
    },
    isTrusted: (e) => trusted.has(e),
    ...extra,
  });
  overlay.attach(container, shadow);
  overlay.setVideo('youtube:aqz-KE-bpKQ');
  const click = (el: Element, isTrusted = true) => {
    const e = new MouseEvent('click', { bubbles: true, composed: true });
    if (isTrusted) trusted.add(e);
    el.dispatchEvent(e);
  };
  const key = (el: Element, k: string) => el.dispatchEvent(new KeyboardEvent('keydown', { key: k, bubbles: true, composed: true }));
  const rows = () => [...overlay.panelEl.querySelectorAll<HTMLButtonElement>('button.dx-row')];
  const status = () => overlay.panelEl.querySelector('.dx-status')?.textContent ?? '';
  return { player, host, shadow, overlay, picks, probe, click, key, rows, status, answerAdd: (o: Outcome<AddedResult>) => (addAnswer = o) };
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'] });
});
afterEach(() => {
  vi.useRealTimers();
});

describe('VideoOverlay', () => {
  it('is a real button with the accessible name, shown only for a video page', () => {
    const { overlay } = setup();
    const b = overlay.button;
    expect(b.tagName).toBe('BUTTON');
    expect(b.getAttribute('aria-label')).toBe('Download this video with Downex');
    expect(b.getAttribute('aria-haspopup')).toBe('dialog');
    expect(b.getAttribute('aria-expanded')).toBe('false');
    expect(b.textContent).toBe('Download');
    expect(overlay.root.hidden).toBe(false);
    overlay.setVideo(undefined);
    expect(overlay.root.hidden).toBe(true);
    overlay.setVideo('youtube:aqz-KE-bpKQ');
    overlay.setEnabled(false);
    expect(overlay.root.hidden).toBe(true);
    overlay.setEnabled(true);
    expect(overlay.root.hidden).toBe(false);
    // Revealed for a while after activity, hidden by opacity (it stays focusable).
    overlay.reveal(REVEAL_ACTIVITY_MS);
    expect(overlay.root.dataset.shown).toBe('true');
    vi.advanceTimersByTime(REVEAL_ACTIVITY_MS);
    expect(overlay.root.dataset.shown).toBe('false');
    expect(OVERLAY_CSS).toContain('pointer-events: none');
    expect(OVERLAY_CSS).toContain('prefers-reduced-motion');
    expect(OVERLAY_CSS).toContain('forced-colors');
  });

  it('opens only for a trusted activation, lists one quality per class and focuses the default', async () => {
    const { overlay, click, probe, rows, shadow, status } = setup();
    click(overlay.button, false);
    expect(overlay.isOpen).toBe(false);
    expect(probe).not.toHaveBeenCalled();
    click(overlay.button);
    expect(overlay.isOpen).toBe(true);
    expect(overlay.button.getAttribute('aria-expanded')).toBe('true');
    expect(overlay.panelEl.getAttribute('role')).toBe('dialog');
    expect(overlay.panelEl.getAttribute('popover')).toBe('auto');
    expect(status()).toBe('Getting video info…');
    await vi.advanceTimersByTimeAsync(0);
    expect(rows().map((r) => r.getAttribute('aria-label'))).toEqual([
      '4K (2160p) 60fps · AV1 · 1.2 GB',
      '1080p (Full HD) 60fps · H.264 · 412 MB',
      '720p (HD) · H.264 · 212 MB',
    ]);
    // The name, the codec small and the size; the picture only as the tooltip.
    const part = (r: Element, c: string) => r.querySelector(`.${c}`)?.textContent;
    expect(rows().map((r) => [part(r, 'dx-q'), part(r, 'dx-c'), part(r, 'dx-s'), (r as HTMLElement).title])).toEqual([
      ['4K (2160p) 60fps', 'AV1', '1.2 GB', '3840×1634'],
      ['1080p (Full HD) 60fps', 'H.264', '412 MB', '1920×818'],
      ['720p (HD)', 'H.264', '212 MB', '1280×544'],
    ]);
    expect(rows().map((r) => r.getAttribute('aria-current'))).toEqual(['false', 'true', 'false']);
    expect(shadow.activeElement).toBe(rows()[1]);
    // Site strings are text, without bidi controls; the title names the dialog.
    const title = overlay.panelEl.querySelector('h2')!;
    expect(title.textContent).toBe('Title with bidi');
    expect(overlay.panelEl.getAttribute('aria-labelledby')).toBe(title.id);
    expect(status()).toBe('Every quality is saved as one MP4 file.');
    // One audio track: no audio select.
    expect((overlay.panelEl.querySelector('.dx-audio') as HTMLElement).hidden).toBe(true);
  });

  it('draws the head without a second mark, a drawn close, a download disc per row and the MP4 note under the title', async () => {
    let answer: (o: Outcome<VideoInfoResult>) => void = () => undefined;
    const { overlay, click, rows } = setup(undefined, {
      probe: () =>
        new Promise<Outcome<VideoInfoResult>>((resolve) => {
          answer = resolve;
        }),
    });
    const panel = overlay.panelEl;
    const sub = panel.querySelector<HTMLElement>('.dx-head .dx-sub')!;
    const skel = panel.querySelector<HTMLElement>('.dx-skel')!;
    click(overlay.button);
    // Getting the qualities: a working status over skeleton rows, no note yet.
    expect(panel.dataset.working).toBe('true');
    expect(panel.dataset.phase).toBe('probing');
    expect(skel.hidden).toBe(false);
    expect(skel.getAttribute('aria-hidden')).toBe('true');
    expect(sub.hidden).toBe(true);
    answer({ ok: true, value: INFO });
    await vi.advanceTimersByTimeAsync(0);
    expect(panel.dataset.working).toBe('false');
    expect(skel.hidden).toBe(true);
    // The button carries the mark; the panel's head does not repeat it.
    expect(overlay.button.querySelector('.dx-tile')).not.toBeNull();
    expect(panel.querySelector('.dx-head .dx-tile')).toBeNull();
    const close = panel.querySelector<HTMLButtonElement>('.dx-close')!;
    expect(close.getAttribute('aria-label')).toBe('Close');
    expect(close.textContent).toBe('');
    expect(close.querySelector('svg')).not.toBeNull();
    for (const r of rows()) {
      const go = r.querySelector('.dx-go')!;
      expect(go.getAttribute('aria-hidden')).toBe('true');
      expect(go.querySelector('svg')).not.toBeNull();
    }
    // The note is shown under the title (not announced twice); the live region still says it once.
    expect(sub.hidden).toBe(false);
    expect(sub.textContent).toBe('Every quality is saved as one MP4 file.');
    expect(sub.getAttribute('aria-hidden')).toBe('true');
    const status = panel.querySelector<HTMLElement>('.dx-status')!;
    expect(status.dataset.note).toBe('true');
    expect(status.textContent).toBe('Every quality is saved as one MP4 file.');
  });

  it('marks the row being sent, and only that one', async () => {
    let settle: (o: Outcome<AddedResult>) => void = () => undefined;
    const { overlay, click, rows, status } = setup(undefined, {
      add: () =>
        new Promise<Outcome<AddedResult>>((resolve) => {
          settle = resolve;
        }),
    });
    click(overlay.button);
    await vi.advanceTimersByTimeAsync(0);
    click(rows()[2]!);
    expect(overlay.panelEl.dataset.working).toBe('true');
    expect(rows().map((r) => r.getAttribute('aria-busy'))).toEqual([null, null, 'true']);
    // The live region says "Sending…" now; the head keeps the note.
    expect(overlay.panelEl.querySelector<HTMLElement>('.dx-status')!.dataset.note).toBeUndefined();
    expect(overlay.panelEl.querySelector<HTMLElement>('.dx-sub')!.hidden).toBe(false);
    settle({ ok: true, value: { type: 'added', downloadId: 5 } });
    await vi.advanceTimersByTimeAsync(0);
    expect(rows().every((r) => !r.hasAttribute('aria-busy'))).toBe(true);
    expect(overlay.panelEl.dataset.working).toBe('false');
    expect(status()).toBe('Downloading in Downex');
    // The row that went to Downex keeps a check; saved at once is the ok tone.
    expect(rows().map((r) => r.dataset.sent)).toEqual([undefined, undefined, 'true']);
    expect(overlay.panelEl.querySelector<HTMLElement>('.dx-status')!.dataset.tone).toBe('ok');
  });

  it('shows the tools download as a meter from the percentage in its text', async () => {
    const { overlay, click } = setup(failure('rejected', 'tools_installing: downloading yt-dlp and Deno (34%)', 'tools_installing'));
    click(overlay.button);
    await vi.advanceTimersByTimeAsync(0);
    const panel = overlay.panelEl;
    expect(panel.querySelector('.dx-status-text')!.textContent).toBe('Preparing video downloads: downloading yt-dlp and Deno (34%)');
    expect(panel.dataset.working).toBe('true');
    const meter = panel.querySelector<HTMLElement>('.dx-meter')!;
    expect(meter.hidden).toBe(false);
    expect(meter.getAttribute('aria-hidden')).toBe('true');
    expect(meter.querySelector<HTMLElement>('span')!.style.width).toBe('34%');
    // The meter stands in for the skeleton.
    expect(panel.querySelector<HTMLElement>('.dx-skel')!.hidden).toBe(true);
  });

  it('draws the action with a glyph and keeps its text', async () => {
    const { overlay, click } = setup(failure('daemon_unavailable', 'cannot start downexd'));
    click(overlay.button);
    await vi.advanceTimersByTimeAsync(0);
    const action = overlay.panelEl.querySelector<HTMLButtonElement>('.dx-action')!;
    expect(action.textContent).toBe('Try again');
    expect(action.dataset.kind).toBe('retry');
    expect(action.querySelector('svg')).not.toBeNull();
  });

  it('keeps the whole panel inside a small embedded player, over its button', async () => {
    const { overlay, click, shadow } = setup();
    const rect = (top: number, h: number) => ({ width: 120, height: h, top, left: 280, right: 400, bottom: top + h, x: 280, y: top, toJSON: () => ({}) });
    overlay.button.getBoundingClientRect = () => rect(56, 32);
    const height = (h: number) => Object.defineProperty(window, 'innerHeight', { value: h, configurable: true });
    const before = window.innerHeight;
    try {
      // 400×225: under the button only 113 px would be left, and nothing past the frame can be seen
      // (the popover cannot draw outside it), so the panel goes over the button, to the frame's bottom.
      height(225);
      click(overlay.button);
      await vi.advanceTimersByTimeAsync(0);
      expect(overlay.panelEl.style.top).toBe('8px');
      expect(overlay.panelEl.style.maxHeight).toBe('209px');
      expect(overlay.button.dataset.covered).toBe('true');
      expect(OVERLAY_HOST_CSS).toContain(".dx-button[data-covered='true'] { visibility: hidden; }");
      // Closed, the button is shown again before the focus returns to it.
      overlay.close(true);
      expect(overlay.button.dataset.covered).toBeUndefined();
      expect(shadow.activeElement).toBe(overlay.button);
      // A taller frame keeps it under the button, never past the frame's bottom.
      height(720);
      click(overlay.button);
      await vi.advanceTimersByTimeAsync(0);
      expect(overlay.panelEl.style.top).toBe('96px');
      expect(overlay.panelEl.style.maxHeight).toBe('616px');
      expect(overlay.button.dataset.covered).toBeUndefined();
    } finally {
      height(before);
    }
  });

  it('moves between rows with Up and Down, and closes on Esc with the focus back on the button', async () => {
    const { overlay, click, key, rows, shadow } = setup();
    click(overlay.button);
    await vi.advanceTimersByTimeAsync(0);
    key(rows()[1]!, 'ArrowDown');
    expect(shadow.activeElement).toBe(rows()[2]);
    key(rows()[2]!, 'ArrowDown');
    expect(shadow.activeElement).toBe(rows()[2]);
    key(rows()[2]!, 'ArrowUp');
    key(rows()[1]!, 'ArrowUp');
    expect(shadow.activeElement).toBe(rows()[0]);
    key(rows()[0]!, 'Escape');
    expect(overlay.isOpen).toBe(false);
    expect(overlay.panelEl.dataset.open).toBe('false');
    expect(overlay.button.getAttribute('aria-expanded')).toBe('false');
    expect(shadow.activeElement).toBe(overlay.button);
  });

  it('keeps the player from seeing clicks, keys and wheel, but not pointer movement', () => {
    const { overlay, player, click, key } = setup();
    const seen: string[] = [];
    for (const type of [...ISOLATED_EVENTS, 'mousemove']) player.addEventListener(type, () => seen.push(type));
    click(overlay.button);
    key(overlay.button, ' ');
    overlay.button.dispatchEvent(new MouseEvent('dblclick', { bubbles: true, composed: true }));
    overlay.button.dispatchEvent(new WheelEvent('wheel', { bubbles: true, composed: true }));
    overlay.button.dispatchEvent(new MouseEvent('mousemove', { bubbles: true, composed: true }));
    expect(seen).toEqual(['mousemove']);
  });

  it('sends the chosen row, says where it went and closes after 3 s', async () => {
    const { overlay, click, rows, picks, status } = setup();
    click(overlay.button);
    await vi.advanceTimersByTimeAsync(0);
    click(rows()[2]!, false); // an untrusted click does nothing
    expect(picks).toEqual([]);
    click(rows()[2]!);
    expect(status()).toBe('Sending…');
    expect(rows().every((r) => r.disabled)).toBe(true);
    await vi.advanceTimersByTimeAsync(0);
    expect(picks).toEqual([{ video: '136', audio: '140' }]);
    expect(status()).toBe('Confirm in Downex');
    // Waiting for the user in Downex: the info tone (a clock), as the popup marks it.
    expect(overlay.panelEl.querySelector<HTMLElement>('.dx-status')!.dataset.tone).toBe('info');
    expect(rows()[2]!.dataset.sent).toBe('true');
    expect(overlay.isOpen).toBe(true);
    await vi.advanceTimersByTimeAsync(SENT_CLOSE_MS);
    expect(overlay.isOpen).toBe(false);
    // Opened again, it can send another quality.
    click(overlay.button);
    expect(rows().every((r) => !r.disabled)).toBe(true);
    expect(rows().every((r) => r.dataset.sent === undefined)).toBe(true);
  });

  it('keeps the focus in the panel when a row is activated from the keyboard, and returns it to the button', async () => {
    const { overlay, click, rows, shadow } = setup();
    click(overlay.button);
    await vi.advanceTimersByTimeAsync(0);
    const row = rows()[2]!;
    row.focus();
    click(row); // Enter on a focused button
    // The rows are updated in place; the one activated is disabled while sending, so the focus
    // waits on the panel, where Up, Down and Esc still work.
    expect(rows()[2]).toBe(row);
    expect(row.disabled).toBe(true);
    expect(overlay.panelEl.contains(shadow.activeElement)).toBe(true);
    await vi.advanceTimersByTimeAsync(0);
    expect(overlay.panelEl.contains(shadow.activeElement)).toBe(true);
    // Closed after sending: the focus goes back to the button, never to the page.
    await vi.advanceTimersByTimeAsync(SENT_CLOSE_MS);
    expect(overlay.isOpen).toBe(false);
    expect(shadow.activeElement).toBe(overlay.button);
  });

  it('moves the focus off "Try again" before it hides', async () => {
    const { overlay, click, shadow } = setup(failure('daemon_unavailable', 'cannot start downexd'));
    click(overlay.button);
    await vi.advanceTimersByTimeAsync(0);
    const action = overlay.panelEl.querySelector<HTMLButtonElement>('.dx-action')!;
    expect(action.hidden).toBe(false);
    action.focus();
    click(action);
    expect(action.hidden).toBe(true);
    expect(shadow.activeElement).toBe(overlay.panelEl);
  });

  it('announces "Getting video info…" once and shows the seconds apart', async () => {
    const slow = setup(undefined, { probe: () => new Promise<Outcome<VideoInfoResult>>(() => undefined) });
    slow.click(slow.overlay.button);
    const status = slow.overlay.panelEl.querySelector('.dx-status')!;
    const text = status.querySelector('.dx-status-text')!;
    const elapsed = status.querySelector('.dx-elapsed')!;
    let changes = 0;
    new MutationObserver(() => changes++).observe(text, { childList: true, characterData: true, subtree: true });
    await vi.advanceTimersByTimeAsync(3000);
    expect(text.textContent).toBe('Getting video info…');
    expect(changes).toBe(0);
    expect(elapsed.getAttribute('aria-hidden')).toBe('true');
    expect(elapsed.textContent).toBe('3 s');
    expect(status.getAttribute('aria-live')).toBe('polite');
  });

  it('offers "Try again" after auth_required when the sign-in was used and the site still refused', async () => {
    const refused = failure('rejected', 'auth_required: the site asks to sign in', 'auth_required');
    refused.error.signIn = { domain: 'youtube.com', state: 'used' };
    const { overlay, click, probe, status } = setup(refused);
    click(overlay.button);
    await vi.advanceTimersByTimeAsync(0);
    expect(status()).toBe('Downex asked again with this browser’s cookies for youtube.com, but the site still refused. Sign in to youtube.com in this browser, then try again.');
    const action = overlay.panelEl.querySelector<HTMLButtonElement>('.dx-action')!;
    expect(action.textContent).toBe('Try again');
    click(action);
    await vi.advanceTimersByTimeAsync(0);
    expect(probe).toHaveBeenCalledTimes(2);
  });

  it('offers no button after auth_required while the sign-in option is off: no prompt', async () => {
    const refused = failure('rejected', 'auth_required: the site asks to sign in', 'auth_required');
    refused.error.signIn = { domain: 'youtube.com', state: 'off' };
    const { overlay, click, status } = setup(refused);
    click(overlay.button);
    await vi.advanceTimersByTimeAsync(0);
    expect(status()).toBe(
      'This video needs a sign-in. To let Downex use yours, turn on “Use my sign-in for videos that need it” in Downex’s browser options.',
    );
    expect(overlay.panelEl.querySelector<HTMLButtonElement>('.dx-action')!.hidden).toBe(true);
  });

  it('says when the qualities needed the sign-in', async () => {
    const { overlay, click, status } = setup({ ok: true, value: { ...INFO, signedIn: 'youtube.com' } });
    click(overlay.button);
    await vi.advanceTimersByTimeAsync(0);
    expect(status()).toBe('Used your sign-in for youtube.com. Every quality is saved as one MP4 file.');
    // Drawn under the title, not only announced.
    expect(overlay.panelEl.querySelector('.dx-sub')!.textContent).toBe('Used your sign-in for youtube.com. Every quality is saved as one MP4 file.');
  });

  it('draws the browser fetch note under the title (SITE-MEDIA-SPEC §2.8)', async () => {
    const { overlay, click, status } = setup({ ok: true, value: { ...INFO, browserFetch: true } });
    click(overlay.button);
    await vi.advanceTimersByTimeAsync(0);
    const note = `Every quality is saved as one MP4 file. ${BROWSER_FETCH_NOTE}`;
    expect(status()).toBe(note);
    const sub = overlay.panelEl.querySelector<HTMLElement>('.dx-sub')!;
    expect(sub.hidden).toBe(false);
    expect(sub.textContent).toBe(note);
  });

  it('closes on another video, on fullscreen changes and when removed', async () => {
    const { overlay, click, rows, probe } = setup();
    click(overlay.button);
    await vi.advanceTimersByTimeAsync(0);
    overlay.setVideo('youtube:aaaaaaaaaaa');
    expect(overlay.isOpen).toBe(false);
    expect(rows()).toHaveLength(0);
    click(overlay.button);
    await vi.advanceTimersByTimeAsync(0);
    expect(probe).toHaveBeenCalledTimes(2);
    document.dispatchEvent(new Event('fullscreenchange'));
    expect(overlay.isOpen).toBe(false);
    // The same video again keeps its qualities.
    overlay.setVideo('youtube:aaaaaaaaaaa');
    click(overlay.button);
    expect(probe).toHaveBeenCalledTimes(2);
    overlay.destroy();
    expect(overlay.root.isConnected).toBe(false);
  });

  it('closes on a click outside the overlay', async () => {
    const { overlay, click } = setup();
    click(overlay.button);
    await vi.advanceTimersByTimeAsync(0);
    overlay.panelEl.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, composed: true }));
    expect(overlay.isOpen).toBe(true);
    document.body.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, composed: true }));
    expect(overlay.isOpen).toBe(false);
  });

  it('removes bidi controls from site text', () => {
    expect(cleanSiteText('a‪b‮c⁦d⁩e')).toBe('abcde');
  });
});

describe('youtubeButtonEnabled', () => {
  const options = (hiddenSites: string[], videoButton = true) => ({ ...DEFAULT_VIDEO_OPTIONS, hiddenSites, videoButton });

  it('follows the background’s overlay state, which decides by the tab’s host', () => {
    expect(youtubeButtonEnabled(options([]), { button: false, host: 'news.example' }, 'www.youtube.com')).toBe(false);
    expect(youtubeButtonEnabled(options(['www.youtube.com']), { button: true, host: 'news.example' }, undefined)).toBe(true);
  });

  it('honours the hidden sites (YouTube too) before the background answers', () => {
    expect(youtubeButtonEnabled(options([]), undefined, 'www.youtube.com')).toBe(true);
    expect(youtubeButtonEnabled(options(['www.youtube.com']), undefined, 'www.youtube.com')).toBe(false);
    expect(youtubeButtonEnabled(options(['*.youtube.com']), undefined, 'm.youtube.com')).toBe(false);
    expect(youtubeButtonEnabled(options([], false), undefined, 'www.youtube.com')).toBe(false);
    // An embedded player waits for the tab's host.
    expect(youtubeButtonEnabled(options([]), undefined, undefined)).toBe(false);
  });
});

describe('OVERLAY_CSS', () => {
  it('changes only on purpose (VIDEO-DETECT-SPEC §5.1; the Glass restyle of 2026-10-25, its glows removed 2026-10-28, the Part row 2026-11, its fields and the covered button 2026-11-17, its timeline trimmer 2026-11-17 and its polish the same day, the Recommended badge 2026-11-19)', () => {
    // A pin, not a spec: update length and hash together with a deliberate change of the
    // overlay's look (the YouTube overlay and the layer over any video share these rules).
    expect(OVERLAY_CSS).toBe(OVERLAY_HOST_CSS + OVERLAY_SHARED_CSS);
    expect(OVERLAY_CSS).toHaveLength(32881);
    expect(createHash('sha256').update(OVERLAY_CSS).digest('hex')).toBe('253d54ce52060f2758c059b3226b535544527b3173ab15e4cde0903ddfa9b46e');
    // The host rule is the only `:host` rule; the shared rules start with the tokens.
    expect(OVERLAY_HOST_CSS.trim().startsWith(':host {')).toBe(true);
    expect(OVERLAY_SHARED_CSS.startsWith('.dx {')).toBe(true);
    expect(OVERLAY_SHARED_CSS).not.toContain(':host');
  });

  it('keeps the light panel light: no later rule sets the colour scheme again', () => {
    // The dark scheme is the root's (inherited); only the light block sets the panel's own, so
    // the scrollbar and the UA-drawn parts of a light panel are light.
    const light = OVERLAY_SHARED_CSS.indexOf('@media (prefers-color-scheme: light)');
    expect(light).toBeGreaterThan(0);
    const schemes = [...OVERLAY_SHARED_CSS.matchAll(/(?<![\w-])color-scheme:\s*(\w+)/g)].map((m) => [m.index, m[1]] as const);
    expect(schemes.map(([, v]) => v)).toEqual(['dark', 'light']);
    expect(schemes[0]![0]).toBeLessThan(light);
    expect(OVERLAY_SHARED_CSS.slice(0, OVERLAY_SHARED_CSS.indexOf('}'))).toContain('color-scheme: dark');
  });

  it('hides a closed popover at once, in a rule of its own', () => {
    // A light dismiss resets data-open only in the (async) toggle event; this rule hides the panel
    // in the same frame, and a browser without :popover-open drops only this rule.
    expect(OVERLAY_SHARED_CSS).toContain("\n.dx-panel[popover]:not(:popover-open) { display: none; }\n");
  });

  it('is one self-contained string: nothing loaded, nothing that leaks into the page', () => {
    // No web-accessible resources (VIDEO-DETECT-SPEC §5.1, §6.6) and nothing WXT would move to the
    // page's <head> (@property, @font-face): system fonts and inline glyphs only.
    for (const banned of ['url(', '@import', '@property', '@font-face', 'expression(']) expect(OVERLAY_CSS).not.toContain(banned);
    expect(OVERLAY_CSS).toContain('prefers-reduced-transparency');
    // Hidden means hidden, whatever display a rule gives (the empty "Audio" select).
    expect(OVERLAY_SHARED_CSS).toContain('.dx[hidden], .dx [hidden] { display: none !important; }');
  });

  it('blurs the button only while shown and the panel only while open', () => {
    // Every rule that sets a backdrop filter: its selector, and the value.
    const blurs = [...OVERLAY_SHARED_CSS.matchAll(/([^{}]+)\{[^{}]*?backdrop-filter:\s*([^;]+);/g)].map((m) => [m[1]!.trim(), m[2]!.trim()] as const);
    expect(blurs.length).toBeGreaterThan(0);
    for (const [selector, value] of blurs) {
      if (value === 'none') continue;
      for (const s of selector.split(',').map((x) => x.trim())) {
        expect(s, `${s} blurs`).toMatch(/data-shown='true'|:focus-visible|aria-expanded='true'|data-open='true'/);
      }
    }
    // Reduced transparency and forced colors turn every blur off through the tokens.
    const offs = [...OVERLAY_SHARED_CSS.matchAll(/@media \((prefers-reduced-transparency: reduce|forced-colors: active)\) \{\s*\.dx \{ --b-blur: none; --p-blur: none;/g)];
    expect(offs).toHaveLength(2);
  });
});

describe('findPlayer', () => {
  const sized = (v: HTMLVideoElement, w: number, h: number) => {
    v.getBoundingClientRect = () => ({ width: w, height: h, top: 0, left: 0, right: w, bottom: h, x: 0, y: 0, toJSON: () => ({}) });
  };

  it('takes the player of the largest visible video', () => {
    document.body.replaceChildren();
    const mini = document.createElement('div');
    mini.className = 'html5-video-player mini';
    const small = document.createElement('video');
    sized(small, 320, 180);
    mini.append(small);
    const main = document.createElement('div');
    main.className = 'html5-video-player';
    const wrap = document.createElement('div');
    const big = document.createElement('video');
    sized(big, 1280, 720);
    wrap.append(big);
    main.append(wrap);
    const hidden = document.createElement('video');
    sized(hidden, 0, 0);
    document.body.append(mini, main, hidden);
    expect(findPlayer(document)).toBe(main);
    document.body.replaceChildren(hidden);
    expect(findPlayer(document)).toBeNull();
  });
});
