// State of a page video's quality picker (SITE-MEDIA-SPEC §5.1, §5.2), shared by the YouTube
// overlay and the popup: getting the qualities (asking again every 2 s while Downex downloads
// yt-dlp or resolves the page, for up to 5 min), the pick, sending it, and the texts of §5.5.
// Pure: the background requests, the timers and the rendering are injected.

import {
  type AddedResult,
  daemonCode,
  errorMessage,
  failure,
  type Outcome,
  type ProtocolError,
  systemTimers,
  type Timers,
  type VideoInfoResult,
} from './protocol';
import {
  canRetryVideo,
  elapsedText,
  gettingInfoText,
  isVideoRetry,
  type Tone,
  toolsInstallingText,
  VIDEO_TEXTS,
  videoAddedText,
  videoFailureText,
} from './ui-format';
import { VIDEO_RETRY_LIMIT_MS, VIDEO_RETRY_MS } from './video-handoff';
import {
  audioLabel,
  audioTracks,
  defaultPick,
  pickedAudio,
  pickSize,
  type QualityRow,
  qualityRows,
  showsAudioChoice,
  sizeLabel,
  type TrackPick,
  trackById,
  withVideo,
} from './video-picker';

/** The elapsed seconds next to "Getting video info…" are redrawn this often. */
const TICK_MS = 1000;

export type VideoPanelPhase = 'idle' | 'probing' | 'list' | 'sending' | 'sent' | 'error';

export interface VideoPanelView {
  phase: VideoPanelPhase;
  /** The video's title once known. */
  title: string;
  /**
   * The status line (announced politely). `elapsed` ("12 s") is the time spent getting the
   * video's info, shown next to the text but not announced: the text stays the same meanwhile.
   */
  status?: { text: string; tone: Tone; elapsed?: string };
  /** One MP4 quality per class, best first, with the size it has with the chosen audio. */
  rows: (QualityRow & { selected: boolean })[];
  /** Only with a video-only pick and several MP4 audio tracks. */
  audio?: { value: string; options: { id: string; label: string }[] };
  pick: TrackPick;
  /** Size of the pick ("" unknown; "~" for an estimate). */
  size: string;
  /** A button under the status: allow the browser sign-in (after auth_required), or try again. */
  action?: { kind: 'allow-sign-in' | 'retry'; text: string };
  /** Requests are running: the controls are disabled. */
  busy: boolean;
}

export interface VideoPanelDeps {
  probe: () => Promise<Outcome<VideoInfoResult>>;
  add: (pick: TrackPick) => Promise<Outcome<AddedResult>>;
  /**
   * Turns on "Send my sign-in cookies when a video needs it"; offered after `auth_required`
   * while it is off (undefined: never offered).
   */
  allowSignIn?: () => Promise<void>;
  /**
   * Whether that option is on now. After `auth_required` with it on, "Try again" is offered
   * instead (the user may have signed in on the site since).
   */
  signInAllowed?: () => boolean;
  render: (view: VideoPanelView) => void;
  timers?: Timers;
  /**
   * A failure in words (default videoFailureText): a stream's picker says "No MP4 video in this
   * stream." (VIDEO-DETECT-SPEC §5.6).
   */
  failureText?: (e: ProtocolError) => string;
  /** The line after Downex took the pick (default videoAddedText; streams: offerAddedText). */
  addedText?: (added: AddedResult) => string;
}

export const ALLOW_SIGN_IN_TEXT = 'Allow Downex to use your browser sign-in for videos';
export const MP4_NOTE = 'Every quality is saved as one MP4 file.';

export class VideoPanel {
  #phase: VideoPanelPhase = 'idle';
  #info: VideoInfoResult | undefined;
  #pick: TrackPick = { video: '', audio: '' };
  #status: VideoPanelView['status'];
  #action: VideoPanelView['action'];
  #open = false;
  /** The add shown as running was sent from an earlier popup (adoptAdd). */
  #adoptedSend = false;
  /** When the current probe started (for "N s") and when the first "ask again" came. */
  #probeStart = 0;
  #retryStart: number | undefined;
  #retryTimer: unknown;
  #tickTimer: unknown;
  /** Bumped by reset(): answers of an older video are dropped. */
  #generation = 0;
  readonly #timers: Timers;

  constructor(private readonly deps: VideoPanelDeps) {
    this.#timers = deps.timers ?? systemTimers;
  }

  get phase(): VideoPanelPhase {
    return this.#phase;
  }

  get info(): VideoInfoResult | undefined {
    return this.#info;
  }

  view(): VideoPanelView {
    const info = this.#info;
    const rows = info
      ? qualityRows(info, this.#pick.audio).map((r) => ({ ...r, selected: r.id === this.#pick.video }))
      : [];
    const view: VideoPanelView = {
      phase: this.#phase,
      title: info?.title ?? '',
      rows,
      pick: this.#pick,
      size: info ? sizeLabel(pickSize(info, this.#pick)) : '',
      busy: this.#phase === 'probing' || this.#phase === 'sending',
    };
    if (info && showsAudioChoice(info, this.#pick)) {
      view.audio = { value: this.#pick.audio, options: audioTracks(info).map((t) => ({ id: t.id, label: audioLabel(t) })) };
    }
    if (this.#status) view.status = this.#status;
    if (this.#action) view.action = this.#action;
    return view;
  }

  /** The panel was opened: gets the qualities unless they are known or being got. */
  open(): void {
    this.#open = true;
    if (this.#phase === 'idle' || (this.#phase === 'error' && !this.#info && this.#action?.kind !== 'allow-sign-in')) {
      void this.#probe();
      return;
    }
    if (this.#phase === 'probing') this.#tick();
    this.#render();
  }

  /** The panel was closed: no more asking again or ticking; what is known stays. */
  close(): void {
    this.#open = false;
    this.#stopTimers();
    if (this.#phase === 'probing') {
      this.#generation++;
      this.#phase = 'idle';
      this.#status = undefined;
    }
  }

  /** After a video was sent, the list can be used again (the overlay opened once more). */
  clearSent(): void {
    if (this.#phase !== 'sent') return;
    this.#phase = this.#info ? 'list' : 'idle';
    this.#status = this.#info ? { text: MP4_NOTE, tone: 'ok' } : undefined;
  }

  /** Another video: everything is forgotten. */
  reset(): void {
    this.#generation++;
    this.#stopTimers();
    this.#phase = 'idle';
    this.#info = undefined;
    this.#pick = { video: '', audio: '' };
    this.#status = undefined;
    this.#action = undefined;
    this.#retryStart = undefined;
    this.#render();
  }

  /** Uses the qualities the background already knows (a popup opened again). */
  adopt(result: Outcome<VideoInfoResult>): void {
    if (this.#phase !== 'idle') return;
    this.#settleProbe(result);
  }

  /**
   * Shows an add the background runs or answered (sent from a popup that closed since); a
   * running one disables Download until its answer is adopted too.
   */
  adoptAdd(state: { running: true } | { result: Outcome<AddedResult> }): void {
    const own = this.#phase === 'sending' && !this.#adoptedSend;
    if (own || this.#phase === 'probing' || (this.#phase === 'sent' && !this.#adoptedSend)) return;
    if ('running' in state) {
      this.#phase = 'sending';
      this.#adoptedSend = true;
      this.#status = { text: 'Sending…', tone: 'warn' };
      this.#action = undefined;
    } else if (state.result.ok) {
      this.#phase = 'sent';
      this.#adoptedSend = false;
      this.#status = { text: this.#added(state.result.value), tone: 'ok' };
    } else if (this.#adoptedSend || this.#phase === 'idle') {
      this.#adoptedSend = false;
      this.#phase = this.#info ? 'list' : 'error';
      this.#failed(state.result.error);
    } else {
      return;
    }
    this.#render();
  }

  chooseVideo(id: string): void {
    if (!this.#info || !trackById(this.#info, id)) return;
    this.#pick = withVideo(this.#info, this.#pick, id);
    this.#render();
  }

  chooseAudio(id: string): void {
    if (!this.#info || trackById(this.#info, id)?.kind !== 'audio') return;
    this.#pick = { ...this.#pick, audio: id };
    this.#render();
  }

  /**
   * Sends the pick (with `video`, that quality and the chosen audio). Before the qualities are
   * known it sends Downex's default pick ("" tracks).
   */
  async download(video?: string): Promise<void> {
    if (this.#phase === 'sending' || this.#phase === 'probing') return;
    if (video !== undefined) this.chooseVideo(video);
    const info = this.#info;
    const pick = info ? { video: this.#pick.video, audio: pickedAudio(info, this.#pick) } : { video: '', audio: '' };
    const generation = this.#generation;
    this.#phase = 'sending';
    this.#status = { text: 'Sending…', tone: 'warn' };
    this.#action = undefined;
    this.#render();
    let outcome: Outcome<AddedResult>;
    try {
      outcome = await this.deps.add(pick);
    } catch (e) {
      outcome = failure('internal', `Background unavailable: ${errorMessage(e)}`);
    }
    if (generation !== this.#generation) return;
    if (outcome.ok) {
      this.#phase = 'sent';
      this.#status = { text: this.#added(outcome.value), tone: 'ok' };
    } else {
      this.#phase = info ? 'list' : 'error';
      this.#failed(outcome.error);
    }
    this.#render();
  }

  /** "Try again" after a failure. */
  retry(): void {
    if (this.#phase === 'probing' || this.#phase === 'sending') return;
    if (this.#info) {
      this.#status = undefined;
      this.#action = undefined;
      this.#phase = 'list';
      this.#render();
      return;
    }
    this.#retryStart = undefined;
    void this.#probe();
  }

  /** "Allow Downex to use your browser sign-in for videos", then the qualities again. */
  async allowSignIn(): Promise<void> {
    if (!this.deps.allowSignIn || this.#action?.kind !== 'allow-sign-in') return;
    this.#action = undefined;
    try {
      await this.deps.allowSignIn();
    } catch (e) {
      this.#status = { text: `Could not save the option: ${errorMessage(e)}`, tone: 'error' };
      this.#render();
      return;
    }
    this.#info = undefined;
    this.#retryStart = undefined;
    void this.#probe();
  }

  async #probe(): Promise<void> {
    const generation = this.#generation;
    this.#stopTimers();
    this.#phase = 'probing';
    this.#action = undefined;
    this.#probeStart = this.#timers.now();
    if (!this.#status || this.#retryStart === undefined) this.#status = this.#gettingInfo(0);
    this.#render();
    this.#tick();
    let outcome: Outcome<VideoInfoResult>;
    try {
      outcome = await this.deps.probe();
    } catch (e) {
      outcome = failure('internal', `Background unavailable: ${errorMessage(e)}`);
    }
    if (generation !== this.#generation) return;
    this.#settleProbe(outcome);
  }

  #settleProbe(outcome: Outcome<VideoInfoResult>): void {
    this.#stopTimers();
    if (outcome.ok) {
      this.#info = outcome.value;
      this.#pick = defaultPick(outcome.value);
      this.#retryStart = undefined;
      this.#action = undefined;
      if (outcome.value.tracks.some((t) => t.kind !== 'audio')) {
        this.#phase = 'list';
        this.#status = { text: MP4_NOTE, tone: 'ok' };
      } else {
        // Downex lists no audio-only page; a list without a video is not one to pick from.
        this.#phase = 'error';
        this.#info = undefined;
        this.#status = { text: this.#text(failure('rejected', 'media_unsupported: no video', 'media_unsupported').error), tone: 'warn' };
      }
      this.#render();
      return;
    }
    const e = outcome.error;
    if (isVideoRetry(e)) {
      const now = this.#timers.now();
      this.#retryStart ??= now;
      if (now - this.#retryStart + VIDEO_RETRY_MS <= VIDEO_RETRY_LIMIT_MS) {
        this.#phase = 'probing';
        this.#status =
          daemonCode(e) === 'tools_installing'
            ? { text: toolsInstallingText(e), tone: 'warn' }
            : this.#gettingInfo(now - this.#retryStart);
        this.#render();
        if (this.#open) {
          const generation = this.#generation;
          // The next try starts VIDEO_RETRY_MS after this one did: at once after a call that
          // used Downex's whole wait (§3.3).
          const wait = this.#probeStart > 0 ? this.#probeStart + VIDEO_RETRY_MS - now : VIDEO_RETRY_MS;
          this.#retryTimer = this.#timers.setTimeout(() => {
            this.#retryTimer = undefined;
            if (generation === this.#generation && this.#open) void this.#probe();
          }, Math.max(0, wait));
        } else {
          this.#phase = 'idle';
        }
        return;
      }
      this.#retryStart = undefined;
      this.#phase = 'error';
      this.#status = {
        text: daemonCode(e) === 'tools_installing' ? VIDEO_TEXTS.still_installing : VIDEO_TEXTS.too_slow,
        tone: 'error',
      };
      this.#action = { kind: 'retry', text: 'Try again' };
      this.#render();
      return;
    }
    this.#retryStart = undefined;
    this.#phase = 'error';
    this.#failed(e);
    this.#render();
  }

  #failed(e: ProtocolError): void {
    const code = daemonCode(e);
    if (isVideoRetry(e)) {
      // An add that gave up while Downex still set up the tools or resolved the page.
      this.#status = { text: code === 'tools_installing' ? VIDEO_TEXTS.still_installing : VIDEO_TEXTS.too_slow, tone: 'error' };
      this.#action = { kind: 'retry', text: 'Try again' };
      return;
    }
    const final = !canRetryVideo(e);
    const signedIn = this.deps.signInAllowed?.() === true;
    this.#status = { text: this.#text(e), tone: final ? 'warn' : 'error' };
    if (code === 'auth_required' && this.deps.allowSignIn && !signedIn) {
      this.#action = { kind: 'allow-sign-in', text: ALLOW_SIGN_IN_TEXT };
    } else if (!final || (code === 'auth_required' && signedIn)) {
      this.#action = { kind: 'retry', text: 'Try again' };
    } else {
      this.#action = undefined;
    }
  }

  #added(a: AddedResult): string {
    return (this.deps.addedText ?? videoAddedText)(a);
  }

  #text(e: ProtocolError): string {
    return (this.deps.failureText ?? videoFailureText)(e);
  }

  /** "Getting video info…" with the seconds spent so far apart. */
  #gettingInfo(elapsedMs: number): NonNullable<VideoPanelView['status']> {
    const status: NonNullable<VideoPanelView['status']> = { text: gettingInfoText(0), tone: 'warn' };
    const elapsed = elapsedText(elapsedMs);
    if (elapsed) status.elapsed = elapsed;
    return status;
  }

  /** Redraws the seconds next to "Getting video info…" every second while getting the info. */
  #tick(): void {
    if (this.#tickTimer !== undefined || !this.#open) return;
    this.#tickTimer = this.#timers.setTimeout(() => {
      this.#tickTimer = undefined;
      if (this.#phase !== 'probing' || !this.#open) return;
      // The tools text has its own progress; the elapsed time is that of the page.
      if (!this.#status?.text.startsWith('Preparing')) {
        const next = this.#gettingInfo(this.#timers.now() - (this.#retryStart ?? this.#probeStart));
        if (next.elapsed !== this.#status?.elapsed || next.text !== this.#status?.text) {
          this.#status = next;
          this.#render();
        }
      }
      this.#tick();
    }, TICK_MS);
  }

  #stopTimers(): void {
    if (this.#retryTimer !== undefined) this.#timers.clearTimeout(this.#retryTimer);
    if (this.#tickTimer !== undefined) this.#timers.clearTimeout(this.#tickTimer);
    this.#retryTimer = undefined;
    this.#tickTimer = undefined;
  }

  #render(): void {
    this.deps.render(this.view());
  }
}
