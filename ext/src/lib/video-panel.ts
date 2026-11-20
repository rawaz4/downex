// State of a page video's quality picker (SITE-MEDIA-SPEC §5.1, §5.2), shared by the YouTube
// overlay and the popup: getting the qualities (asking again every 2 s while Downex downloads
// yt-dlp or resolves the page, for up to 5 min), the pick, sending it, and the texts of §5.5.
// Pure: the background requests, the timers and the rendering are injected.

import type { ClipRange } from './clip';
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
  type VideoItem,
} from './protocol';
import {
  canRetryVideo,
  elapsedText,
  gettingInfoText,
  isVideoRetry,
  signInRetries,
  signInUsedText,
  siteNote,
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
  clipSize,
  defaultPick,
  fileTypeLabel,
  pickedAudio,
  pickSize,
  type QualityRow,
  qualityRows,
  recommendedPick,
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
  /** `recommended`: the quality the panel recommends (VideoPanelDeps.recommend). */
  rows: (QualityRow & { selected: boolean; recommended?: boolean })[];
  /** Only with a video-only pick and several MP4 audio tracks. */
  audio?: { value: string; options: { id: string; label: string }[] };
  pick: TrackPick;
  /** Size of the pick ("" unknown; "~" for an estimate). */
  size: string;
  /** A button under the status: try again. */
  action?: { kind: 'retry'; text: string };
  /** Requests are running: the controls are disabled. */
  busy: boolean;
  /** What the pick is saved as: "MP4", or an audio page's "M4A" or "MP3" (SITE-MEDIA-SPEC §3.13). */
  fileType: string;
  /**
   * A page with several videos (SITE-MEDIA-SPEC §3.12): its items, the chosen one marked, while
   * there are two or more, or other items left out. Kept while another item's qualities are got.
   */
  items?: VideoPanelItems;
}

/** The item row of a page with several videos. */
export interface VideoPanelItems {
  options: (VideoItem & { selected: boolean })[];
  /** Index of the chosen item in options (-1 while none is). */
  index: number;
  /** Other items of the page (photos, links, videos past 20): not downloaded. */
  leftOut: number;
  /** The item shown is an audio page's (SITE-MEDIA-SPEC §3.13): the items are tracks. */
  audio?: boolean;
}

export interface VideoPanelDeps {
  /** The qualities; with `entryId`, of that video of a page with several (SITE-MEDIA-SPEC §3.12). */
  probe: (entryId?: string) => Promise<Outcome<VideoInfoResult>>;
  /** Sends the pick; with `entryId`, of that video of a page with several. */
  add: (pick: TrackPick, entryId?: string) => Promise<Outcome<AddedResult>>;
  /**
   * The item of a page with several videos that the user clicked (SITE-MEDIA-SPEC §3.12 "Which
   * item"; clickedItem), asked once when the page's items are first known and none was chosen:
   * when it names another item than the page's default, the panel gets that one instead.
   */
  itemHint?: (items: readonly VideoItem[], leftOut: number) => string | undefined;
  render: (view: VideoPanelView) => void;
  timers?: Timers;
  /**
   * A failure in words (default videoFailureText): a stream's picker says "No MP4 video in this
   * stream." (VIDEO-DETECT-SPEC §5.6).
   */
  failureText?: (e: ProtocolError) => string;
  /** The line after Downex took the pick (default videoAddedText; streams: offerAddedText). */
  addedText?: (added: AddedResult) => string;
  /**
   * The part of the video that a download sends (the YouTube panel's Part row), undefined for
   * the whole video: the sizes are then the part's estimates (clipSize, "~").
   */
  clip?: () => ClipRange | undefined;
  /**
   * Start with the highest quality with sound and badge it "Recommended" (VIDEO-DETECT-SPEC §5.2,
   * amended 2026-11-19: the sites other than YouTube), instead of Downex's default pick.
   */
  recommend?: boolean;
}

export const MP4_NOTE = 'Every quality is saved as one MP4 file.';

export class VideoPanel {
  #phase: VideoPanelPhase = 'idle';
  #info: VideoInfoResult | undefined;
  #pick: TrackPick = { video: '', audio: '' };
  #status: VideoPanelView['status'];
  #action: VideoPanelView['action'];
  /** The item of a page with several videos the user chose (undefined: the page's default). */
  #entry: string | undefined;
  /** That page's items, from the last answer that listed them. */
  #items: { list: VideoItem[]; leftOut: number } | undefined;
  /** deps.itemHint was asked (once per video). */
  #hinted = false;
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
    const clip = info ? this.deps.clip?.() : undefined;
    const listed = info ? qualityRows(info, this.#pick.audio, clip) : [];
    const recommended = info && this.deps.recommend && listed.length > 1 && !listed[0]?.audio ? recommendedPick(info).video : '';
    const rows = listed.map((r) => {
      const row: VideoPanelView['rows'][number] = { ...r, selected: r.id === (r.audio ? this.#pick.audio : this.#pick.video) };
      if (recommended && r.id === recommended) row.recommended = true;
      return row;
    });
    const view: VideoPanelView = {
      phase: this.#phase,
      title: info?.title ?? '',
      rows,
      pick: this.#pick,
      size: info ? sizeLabel(clipSize(pickSize(info, this.#pick), clip, info.durationMs)) : '',
      busy: this.#phase === 'probing' || this.#phase === 'sending',
      fileType: fileTypeLabel(info),
    };
    if (info && showsAudioChoice(info, this.#pick)) {
      view.audio = { value: this.#pick.audio, options: audioTracks(info).map((t) => ({ id: t.id, label: audioLabel(t) })) };
    }
    if (this.#status) view.status = this.#status;
    if (this.#action) view.action = this.#action;
    const items = this.#items;
    if (items && (items.list.length > 1 || items.leftOut > 0)) {
      const chosen = this.entryId;
      const options = items.list.map((it) => ({ ...it, selected: it.id === chosen }));
      view.items = { options, index: options.findIndex((o) => o.selected), leftOut: items.leftOut };
      if (info && fileTypeLabel(info) !== 'MP4') view.items.audio = true;
    }
    return view;
  }

  /** The item the panel is about: the answer's, else the one chosen ("" for the page's default). */
  get entryId(): string {
    return this.#info?.entryId || this.#entry || '';
  }

  /**
   * Another video of a page with several (SITE-MEDIA-SPEC §3.12): its qualities are got (the list
   * of items stays), and Download sends it. Ignored while a pick is being sent, and for an id the
   * list does not have.
   */
  chooseItem(id: string): void {
    if (this.#phase === 'sending' || !this.#items?.list.some((it) => it.id === id) || id === this.entryId) return;
    this.#generation++;
    this.#stopTimers();
    this.#entry = id;
    this.#info = undefined;
    this.#pick = { video: '', audio: '' };
    this.#status = undefined;
    this.#action = undefined;
    this.#retryStart = undefined;
    this.#phase = 'idle';
    if (this.#open) void this.#probe();
    else this.#render();
  }

  /** The panel was opened: gets the qualities unless they are known or being got. */
  open(): void {
    this.#open = true;
    if (this.#phase === 'idle' || (this.#phase === 'error' && !this.#info)) {
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
    this.#status = this.#info ? { text: this.#listNote(this.#info), tone: 'ok' } : undefined;
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
    this.#entry = undefined;
    this.#items = undefined;
    this.#hinted = false;
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
      const entry = this.entryId;
      outcome = await (entry ? this.deps.add(pick, entry) : this.deps.add(pick));
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
      const entry = this.#entry;
      outcome = await (entry ? this.deps.probe(entry) : this.deps.probe());
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
      this.#pick = this.deps.recommend ? recommendedPick(outcome.value) : defaultPick(outcome.value);
      const items = outcome.value.items;
      if (items && (items.length > 0 || (outcome.value.itemsLeftOut ?? 0) > 0)) {
        const chosen = this.#entry !== undefined;
        this.#items = { list: items, leftOut: outcome.value.itemsLeftOut ?? 0 };
        if (outcome.value.entryId) this.#entry = outcome.value.entryId;
        // The page's default item (the page's own id, else the first) may not be the video the
        // user clicked: what the page shows of that one picks it instead.
        if (!chosen && !this.#hinted && items.length > 1 && this.deps.itemHint) {
          this.#hinted = true;
          const id = this.deps.itemHint(items, this.#items.leftOut);
          if (id !== undefined && id !== outcome.value.entryId && items.some((it) => it.id === id)) {
            this.#info = outcome.value;
            this.chooseItem(id);
            return;
          }
        }
      } else if (!this.#entry) {
        this.#items = undefined;
      }
      this.#retryStart = undefined;
      this.#action = undefined;
      if (outcome.value.tracks.some((t) => t.kind !== 'audio')) {
        this.#phase = 'list';
        this.#status = { text: this.#listNote(outcome.value), tone: 'ok' };
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
    this.#status = { text: this.#text(e), tone: final ? 'warn' : 'error' };
    // After the sign-in was used or found missing, the user may sign in on the site and try again (§6.3).
    this.#action = !final || (code === 'auth_required' && signInRetries(e.signIn)) ? { kind: 'retry', text: 'Try again' } : undefined;
  }

  /** The line after Downex took the pick, saying when the add carried the user's sign-in (§6.3). */
  #added(a: AddedResult): string {
    const text = (this.deps.addedText ?? videoAddedText)(a);
    if (!a.signedIn) return text;
    return `${/[.!?…]$/.test(text) ? text : `${text}.`} ${signInUsedText(a.signedIn)}`;
  }

  /** The status under a list of qualities: the MP4 note, after the sign-in that answer needed (§6.3). */
  #listNote(info: VideoInfoResult): string {
    return siteNote(info, MP4_NOTE);
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
