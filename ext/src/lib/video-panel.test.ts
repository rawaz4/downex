import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { type AddedResult, failure, type Outcome, type VideoInfoResult, type VideoTrack } from './protocol';
import { VIDEO_RETRY_LIMIT_MS, VIDEO_RETRY_MS } from './video-handoff';
import { ALLOW_SIGN_IN_TEXT, MP4_NOTE, VideoPanel, type VideoPanelView } from './video-panel';
import type { TrackPick } from './video-picker';

const MB = 1_000_000;

function track(t: Partial<VideoTrack> & Pick<VideoTrack, 'id' | 'kind'>): VideoTrack {
  return { ext: 'mp4', codecs: '', width: 0, height: 0, frameRate: 0, bandwidth: 0, size: 0, sizeApprox: false, language: '', name: '', ...t };
}

const INFO: VideoInfoResult = {
  type: 'videoInfo',
  site: 'YouTube',
  title: 'A ‮video',
  durationMs: 60_000,
  format: 'http',
  container: 'mp4',
  selectedVideo: '299',
  selectedAudio: '140',
  tracks: [
    track({ id: '401', kind: 'video', codecs: 'av01.0.12M.08', width: 3840, height: 1634, frameRate: 60, size: 1200 * MB, name: '4K (2160p) 60fps' }),
    track({ id: '299', kind: 'video', codecs: 'avc1.64002a', width: 1920, height: 818, frameRate: 60, size: 400 * MB, name: '1080p (Full HD) 60fps' }),
    track({ id: '140', kind: 'audio', ext: 'm4a', codecs: 'mp4a.40.2', bandwidth: 129_000, size: 12 * MB, language: 'en', name: 'English original' }),
    track({ id: '140-drc', kind: 'audio', ext: 'm4a', codecs: 'mp4a.40.2', bandwidth: 129_000, size: 12 * MB, language: 'en', name: 'English original, DRC' }),
  ],
};

const refused = (reason: string, detail = 'x') => failure('rejected', `${reason}: ${detail}`, reason);

function setup(answers: Outcome<VideoInfoResult>[] = [{ ok: true, value: INFO }]) {
  const views: VideoPanelView[] = [];
  const picks: TrackPick[] = [];
  const queue = [...answers];
  const probe = vi.fn(async () => queue.shift() ?? ({ ok: true, value: INFO } as Outcome<VideoInfoResult>));
  let addAnswer: Outcome<AddedResult> = { ok: true, value: { type: 'added', downloadId: 1, awaiting: true } };
  const allowSignIn = vi.fn(async () => undefined);
  const panel = new VideoPanel({
    probe,
    add: async (pick) => {
      picks.push(pick);
      return addAnswer;
    },
    allowSignIn,
    render: (v) => views.push(v),
  });
  return {
    panel,
    probe,
    picks,
    allowSignIn,
    last: () => views[views.length - 1]!,
    answerAdd: (o: Outcome<AddedResult>) => (addAnswer = o),
    queue,
  };
}

beforeEach(() => {
  vi.useFakeTimers();
});
afterEach(() => {
  vi.useRealTimers();
});

describe('VideoPanel', () => {
  it('gets the qualities when opened and starts from the daemon pick', async () => {
    const { panel, probe, last } = setup();
    panel.open();
    expect(last()).toMatchObject({ phase: 'probing', busy: true, status: { text: 'Getting video info…' } });
    await vi.advanceTimersByTimeAsync(0);
    const v = last();
    expect(v.phase).toBe('list');
    expect(v.title).toBe('A ‮video');
    expect(v.status).toEqual({ text: MP4_NOTE, tone: 'ok' });
    // downexd's names and one quality per class; the picture is only the tooltip.
    expect(v.rows.map((r) => [r.label, r.selected, r.picture])).toEqual([
      ['4K (2160p) 60fps · AV1 · 1.2 GB', false, '3840×1634'],
      ['1080p (Full HD) 60fps · H.264 · 412 MB', true, '1920×818'],
    ]);
    expect(v.pick).toEqual({ video: '299', audio: '140' });
    expect(v.size).toBe('412 MB');
    expect(v.audio?.options.map((o) => o.id)).toEqual(['140', '140-drc']);
    // Opened again: nothing is asked again.
    panel.close();
    panel.open();
    expect(probe).toHaveBeenCalledTimes(1);
    expect(last().phase).toBe('list');
  });

  it('counts the seconds while Downex resolves the page', async () => {
    const slow = new Promise<Outcome<VideoInfoResult>>(() => undefined);
    const views: VideoPanelView[] = [];
    const panel = new VideoPanel({ probe: () => slow, add: async () => failure('x', 'x'), render: (v) => views.push(v) });
    panel.open();
    await vi.advanceTimersByTimeAsync(3000);
    // The announced text stays; the seconds are apart (shown, not announced).
    expect(views.at(-1)?.status).toEqual({ text: 'Getting video info…', tone: 'warn', elapsed: '3 s' });
    panel.close();
    const n = views.length;
    await vi.advanceTimersByTimeAsync(5000);
    expect(views).toHaveLength(n);
  });

  it('asks again every 2 s while Downex downloads yt-dlp and Deno, and gives up after 5 minutes', async () => {
    const { panel, probe, last, queue } = setup([
      refused('tools_installing', 'downloading yt-dlp and Deno (12%)'),
      refused('tools_installing', 'downloading yt-dlp and Deno (34%)'),
      refused('site_resolving', 'still getting the video info'),
    ]);
    panel.open();
    await vi.advanceTimersByTimeAsync(0);
    expect(last()).toMatchObject({ phase: 'probing', status: { text: 'Preparing video downloads: downloading yt-dlp and Deno (12%)' } });
    await vi.advanceTimersByTimeAsync(VIDEO_RETRY_MS);
    expect(probe).toHaveBeenCalledTimes(2);
    expect(last().status?.text).toBe('Preparing video downloads: downloading yt-dlp and Deno (34%)');
    await vi.advanceTimersByTimeAsync(VIDEO_RETRY_MS);
    expect(last().status).toMatchObject({ text: 'Getting video info…', elapsed: '4 s' });
    await vi.advanceTimersByTimeAsync(VIDEO_RETRY_MS);
    expect(last().phase).toBe('list');
    expect(probe).toHaveBeenCalledTimes(4);

    // Closing stops the asking; past 5 minutes the panel says so and offers to try again.
    const other = setup([]);
    other.probe.mockImplementation(async () => refused('tools_installing', 'downloading yt-dlp and Deno (1%)'));
    other.panel.open();
    await vi.advanceTimersByTimeAsync(VIDEO_RETRY_MS * 2);
    other.panel.close();
    const calls = other.probe.mock.calls.length;
    await vi.advanceTimersByTimeAsync(VIDEO_RETRY_MS * 5);
    expect(other.probe.mock.calls.length).toBe(calls);
    other.panel.open();
    await vi.advanceTimersByTimeAsync(VIDEO_RETRY_LIMIT_MS + VIDEO_RETRY_MS);
    expect(other.last()).toMatchObject({
      phase: 'error',
      status: { text: 'Downex is still setting up yt-dlp and Deno. Try again in a few minutes.' },
      action: { kind: 'retry' },
    });
    expect(queue).toHaveLength(0);
  });

  it('offers the browser sign-in after auth_required and asks again with it', async () => {
    const { panel, probe, last, allowSignIn } = setup([refused('auth_required', 'the site asks to sign in')]);
    panel.open();
    await vi.advanceTimersByTimeAsync(0);
    expect(last()).toMatchObject({
      phase: 'error',
      status: { text: 'The site wants you to sign in (age check, members-only or bot check).', tone: 'warn' },
      action: { kind: 'allow-sign-in', text: ALLOW_SIGN_IN_TEXT },
    });
    // Reopening does not ask again before the user allows it.
    panel.close();
    panel.open();
    expect(probe).toHaveBeenCalledTimes(1);
    await panel.allowSignIn();
    await vi.advanceTimersByTimeAsync(0);
    expect(allowSignIn).toHaveBeenCalledTimes(1);
    expect(probe).toHaveBeenCalledTimes(2);
    expect(last().phase).toBe('list');
  });

  it('offers "Try again" after auth_required when the sign-in is already allowed', async () => {
    const views: VideoPanelView[] = [];
    const probe = vi
      .fn<() => Promise<Outcome<VideoInfoResult>>>()
      .mockResolvedValueOnce(refused('auth_required', 'the site asks to sign in'))
      .mockResolvedValue({ ok: true, value: INFO });
    const allowSignIn = vi.fn(async () => undefined);
    const panel = new VideoPanel({
      probe,
      add: async () => failure('x', 'x'),
      allowSignIn,
      signInAllowed: () => true,
      render: (v) => views.push(v),
    });
    panel.open();
    await vi.advanceTimersByTimeAsync(0);
    expect(views.at(-1)).toMatchObject({ phase: 'error', action: { kind: 'retry', text: 'Try again' } });
    panel.retry();
    await vi.advanceTimersByTimeAsync(0);
    expect(probe).toHaveBeenCalledTimes(2);
    expect(allowSignIn).not.toHaveBeenCalled();
    expect(views.at(-1)?.phase).toBe('list');
  });

  it('asks again 2 s after the start of a call, at once after a call that took that long', async () => {
    const starts: number[] = [];
    let n = 0;
    const probe = () => {
      starts.push(Date.now());
      const answer: Outcome<VideoInfoResult> =
        ++n < 3 ? refused('tools_installing', `downloading yt-dlp and Deno (${n * 20}%)`) : { ok: true, value: INFO };
      // The first call takes 25 s (Downex's budget), the others answer at once.
      return new Promise<Outcome<VideoInfoResult>>((r) => setTimeout(() => r(answer), n === 1 ? 25_000 : 0));
    };
    const views: VideoPanelView[] = [];
    const panel = new VideoPanel({ probe, add: async () => failure('x', 'x'), render: (v) => views.push(v) });
    panel.open();
    await vi.advanceTimersByTimeAsync(25_000);
    await vi.advanceTimersByTimeAsync(1);
    expect(starts).toHaveLength(2);
    // Not 25 s + 2 s: the call itself took longer than the interval.
    expect(starts[1]! - starts[0]!).toBeLessThan(25_000 + VIDEO_RETRY_MS);
    await vi.advanceTimersByTimeAsync(VIDEO_RETRY_MS);
    expect(starts).toHaveLength(3);
    expect(starts[2]! - starts[1]!).toBe(VIDEO_RETRY_MS);
  });

  it('says what to do when an add gives up while Downex still sets up or resolves', async () => {
    const { panel, last, answerAdd, probe } = setup();
    answerAdd(refused('tools_installing', 'downloading yt-dlp and Deno (80%)'));
    await panel.download();
    expect(last()).toMatchObject({
      phase: 'error',
      status: { text: 'Downex is still setting up yt-dlp and Deno. Try again in a few minutes.', tone: 'error' },
      action: { kind: 'retry' },
    });
    panel.retry();
    await vi.advanceTimersByTimeAsync(0);
    expect(probe).toHaveBeenCalledTimes(1);
    expect(last().phase).toBe('list');
    answerAdd(refused('site_resolving', 'still getting the video info'));
    await panel.download();
    expect(last()).toMatchObject({ phase: 'list', status: { text: 'Downex took too long to answer. Try again.' }, action: { kind: 'retry' } });
  });

  it('shows refusals without "Try again" and other failures with it', async () => {
    const a = setup([refused('live')]);
    a.panel.open();
    await vi.advanceTimersByTimeAsync(0);
    expect(a.last()).toMatchObject({ phase: 'error', status: { text: 'This is a live stream. Downex downloads only finished videos.' } });
    expect(a.last().action).toBeUndefined();
    const b = setup([failure('daemon_unavailable', 'cannot start downexd')]);
    b.panel.open();
    await vi.advanceTimersByTimeAsync(0);
    expect(b.last()).toMatchObject({ phase: 'error', action: { kind: 'retry' }, status: { tone: 'error' } });
    b.panel.retry();
    await vi.advanceTimersByTimeAsync(0);
    expect(b.last().phase).toBe('list');
    // A list without any video is not one to pick from.
    const c = setup([{ ok: true, value: { ...INFO, tracks: INFO.tracks.filter((t) => t.kind === 'audio') } }]);
    c.panel.open();
    await vi.advanceTimersByTimeAsync(0);
    expect(c.last()).toMatchObject({ phase: 'error', status: { text: 'No MP4 video on this page.' }, rows: [] });
  });

  it('sends the chosen quality with the chosen audio', async () => {
    const { panel, picks, last, answerAdd } = setup();
    panel.open();
    await vi.advanceTimersByTimeAsync(0);
    panel.chooseAudio('140-drc');
    panel.chooseAudio('401'); // not an audio track
    expect(last().pick).toEqual({ video: '299', audio: '140-drc' });
    const sending = panel.download('401');
    expect(last()).toMatchObject({ phase: 'sending', busy: true, status: { text: 'Sending…' } });
    await sending;
    expect(picks).toEqual([{ video: '401', audio: '140-drc' }]);
    expect(last()).toMatchObject({ phase: 'sent', status: { text: 'Confirm in Downex', tone: 'ok' } });
    // A failed add keeps the list.
    panel.clearSent();
    answerAdd(refused('media_invalid'));
    await panel.download();
    expect(last()).toMatchObject({ phase: 'list', status: { text: 'The chosen quality is no longer offered. Add the video again.' } });
    answerAdd({ ok: true, value: { type: 'added', downloadId: 2, awaiting: false } });
    await panel.download();
    expect(last().status?.text).toBe('Downloading in Downex');
  });

  it('sends Downex default pick before the qualities are known', async () => {
    const { panel, picks, probe } = setup();
    await panel.download();
    expect(picks).toEqual([{ video: '', audio: '' }]);
    expect(probe).not.toHaveBeenCalled();
  });

  it('drops the answers of a video it no longer shows', async () => {
    let answer!: (o: Outcome<VideoInfoResult>) => void;
    const views: VideoPanelView[] = [];
    const panel = new VideoPanel({
      probe: () => new Promise((r) => (answer = r)),
      add: async () => failure('x', 'x'),
      render: (v) => views.push(v),
    });
    panel.open();
    panel.reset();
    answer({ ok: true, value: INFO });
    await vi.advanceTimersByTimeAsync(0);
    expect(views.at(-1)).toMatchObject({ phase: 'idle', rows: [] });
  });

  it('adopts what the background knows for a popup opened again', () => {
    const { panel, last } = setup();
    panel.adopt({ ok: true, value: INFO });
    expect(last().phase).toBe('list');
    panel.adoptAdd({ running: true });
    expect(last()).toMatchObject({ phase: 'sending', busy: true });
    panel.adoptAdd({ result: { ok: true, value: { type: 'added', downloadId: 3, awaiting: false } } });
    expect(last()).toMatchObject({ phase: 'sent', status: { text: 'Downloading in Downex' } });
    const other = setup();
    other.panel.adoptAdd({ result: refused('encrypted') });
    expect(other.last()).toMatchObject({ phase: 'error', status: { text: 'This video is DRM-protected. Downex does not download protected content.' } });
  });

  it('words a stream’s failures and adds with the texts it is given', async () => {
    const views: VideoPanelView[] = [];
    const panel = new VideoPanel({
      probe: async () => refused('media_unsupported', 'no MP4 video'),
      add: async () => ({ ok: true, value: { type: 'added', downloadId: 1 } }),
      render: (v) => views.push(v),
      failureText: (e) => `stream: ${e.reason ?? e.code}`,
      addedText: (a) => (a.awaiting === undefined ? 'Sent to Downex' : 'other'),
    });
    panel.open();
    await vi.advanceTimersByTimeAsync(0);
    expect(views.at(-1)!.status).toEqual({ text: 'stream: media_unsupported', tone: 'warn' });
    await panel.download();
    expect(views.at(-1)!.status).toEqual({ text: 'Sent to Downex', tone: 'ok' });
    // An audio-only list is worded by the same function.
    const audioOnly = new VideoPanel({
      probe: async () => ({ ok: true, value: { ...INFO, tracks: INFO.tracks.filter((t) => t.kind === 'audio') } }),
      add: async () => ({ ok: true, value: { type: 'added', downloadId: 1 } }),
      render: (v) => views.push(v),
      failureText: (e) => `stream: ${e.reason ?? e.code}`,
    });
    audioOnly.open();
    await vi.advanceTimersByTimeAsync(0);
    expect(views.at(-1)!.status).toEqual({ text: 'stream: media_unsupported', tone: 'warn' });
  });
});
