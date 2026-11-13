// State of a progress window (DESKTOP-SPEC §7): one download, kept current from the host's
// events (a progress window's host watches that download only and subscribes again after
// downexd restarts), read once at the start and again whenever downexd becomes reachable.
// The settings can change elsewhere (the main window, the CLI, another progress window):
// they are read at the start, again when downexd becomes reachable or a read failed, and
// whenever the window asks (on focus, before it decides what completion shows).

import {
  toApiError,
  type Api,
  type Download,
  type DownloadOptions,
  type EventBatch,
  type Settings,
  type Unsubscribe,
} from '../api';
import { ReportOrder } from './report-order';

/** How often the window checks that downexd answers (as the main window does). */
export const PROGRESS_STATUS_MS = 3000;

export class ProgressStore {
  readonly api: Api;
  readonly id: number;

  /** The download as last reported; null until it is known. */
  download = $state.raw<Download | null>(null);
  /** The download did not exist when the window opened. */
  missing = $state(false);
  /** The download was removed while the window showed it. */
  removed = $state(false);
  /** Why the download could not be read (other than not existing). */
  loadError = $state<string | null>(null);
  /** downexd answered the last check. */
  connected = $state(true);
  /** The daemon's settings; null until read (or when they could not be). */
  settings = $state.raw<Settings | null>(null);
  /** The state in which the window first saw the download. */
  firstState = $state.raw<Download['state'] | null>(null);
  /** The first reads (download and settings) are over, whatever their outcome. */
  ready = $state(false);

  #unsubscribe: Unsubscribe | null = null;
  #timer: ReturnType<typeof setTimeout> | undefined;
  #disposed = false;
  /** Counts settings reads and writes, so that an older answer never replaces a newer one. */
  #settingsGen = 0;
  /**
   * The mark that gives the download its complete dialog (DESKTOP-SPEC §8): sent once,
   * when the window first knows the download; again only after downexd could not be
   * reached.
   */
  #mark: 'unsent' | 'sending' | 'done' = 'unsent';
  #reports = new ReportOrder();

  constructor(api: Api, id: number) {
    this.api = api;
    this.id = id;
  }

  /** Subscribes to events, then reads the download and the settings, and starts the checks. */
  async start(statusMs = PROGRESS_STATUS_MS): Promise<void> {
    if (this.#unsubscribe || this.#disposed) return;
    this.#unsubscribe = this.api.onEvents((batch) => this.apply(batch));
    await Promise.all([this.refresh(), this.loadSettings()]);
    this.ready = true;
    this.#schedule(statusMs);
  }

  dispose(): void {
    this.#disposed = true;
    this.#unsubscribe?.();
    this.#unsubscribe = null;
    clearTimeout(this.#timer);
    this.#timer = undefined;
  }

  apply(batch: EventBatch): void {
    const { events, snapshot } = this.#reports.arrived();
    for (const ev of batch) {
      if (ev.kind === 'removed') {
        // The host also reports a download that it cannot watch (it does not exist).
        if (ev.id === this.id) {
          this.#reports.remove(this.id, events);
          if (this.download === null) this.missing = true;
          else this.removed = true;
        }
        continue;
      }
      if ((ev.kind === 'snapshot' || ev.kind === 'updated') && ev.download.id === this.id) {
        this.#put(ev.download, true, ev.kind === 'snapshot' ? snapshot : events);
      }
    }
  }

  /**
   * Reads the download; not_found means it is gone. The answer replaces the download shown
   * only when it is newer (ReportOrder).
   */
  async refresh(): Promise<void> {
    const at = this.#reports.sent();
    try {
      const d = await this.api.get(this.id);
      this.loadError = null;
      this.#put(d, false, at);
    } catch (err) {
      const e = toApiError(err);
      if (e.code === 'not_found') {
        if (this.download === null) this.missing = true;
        else this.removed = true;
        return;
      }
      if (this.download === null) this.loadError = e.message;
    } finally {
      this.#reports.landed();
    }
  }

  /** Reads the settings; when that fails, the last ones read (null if none were). */
  async loadSettings(): Promise<Settings | null> {
    const gen = ++this.#settingsGen;
    try {
      const s = await this.api.getSettings();
      if (gen === this.#settingsGen) this.settings = s;
      return s;
    } catch {
      return this.settings;
    }
  }

  /**
   * Stores a newer report, learned at at (ReportOrder). Only Watch updates carry the
   * connections of a running download; a read in between keeps the ones last reported.
   */
  #put(d: Download, fromEvent: boolean, at: number): void {
    const prev = this.download;
    if (!this.#reports.newer(d, prev, at)) return;
    this.#reports.hold(d.id, at);
    let next = d;
    if (!fromEvent && prev && d.connectionInfo.length === 0 && d.state === 'downloading' && prev.state === 'downloading') {
      next = { ...d, connectionInfo: prev.connectionInfo };
    }
    this.firstState ??= next.state;
    this.download = next;
    this.missing = false;
    this.#markForCompletion(next);
  }

  /**
   * A download whose progress window was opened gets the download complete dialog, also
   * when the window is closed before it completes: the window tells downexd once. A
   * completed download needs no mark (this window already shows its completion), nor one
   * marked already.
   */
  #markForCompletion(d: Download): void {
    if (this.#mark !== 'unsent' || this.#disposed) return;
    if (d.completionDialog || d.state === 'completed') {
      this.#mark = 'done';
      return;
    }
    this.#mark = 'sending';
    const at = this.#reports.sent();
    this.api.setDownloadOptions(this.id, { completionDialog: true }).then(
      (answer) => {
        this.#mark = 'done';
        if (!this.#disposed) this.#put(answer, false, at);
        this.#reports.landed();
      },
      (err: unknown) => {
        // Unreachable: the next report (after downexd is back) tries again. Any other
        // refusal (completed meanwhile, being removed) is final.
        const code = toApiError(err).code;
        this.#mark = code === 'unavailable' || code === 'deadline_exceeded' ? 'unsent' : 'done';
        this.#reports.landed();
      },
    );
  }

  #schedule(ms: number): void {
    if (this.#disposed) return;
    this.#timer = setTimeout(() => void this.#check(ms), ms);
  }

  async #check(ms: number): Promise<void> {
    try {
      await this.api.status();
      const wasDown = !this.connected;
      this.connected = true;
      await Promise.all([
        wasDown || this.loadError !== null ? this.refresh() : undefined,
        wasDown || this.settings === null ? this.loadSettings() : undefined,
      ]);
    } catch {
      this.connected = false;
    }
    this.#schedule(ms);
  }

  // ---- actions (they reject with the daemon's error) ------------------------------

  async pause(): Promise<void> {
    await this.#answer(() => this.api.pause(this.id));
  }

  async resume(restart = false): Promise<void> {
    await this.#answer(() => this.api.resume(this.id, restart ? { restart: true } : {}));
  }

  /** Changes the download's limits; resolves to the download as the daemon answered it. */
  setOptions(opts: DownloadOptions): Promise<Download> {
    return this.#answer(() => this.api.setDownloadOptions(this.id, opts));
  }

  /** Sends call, whose answer is the download, and shows it unless a newer report arrived meanwhile. */
  async #answer(call: () => Promise<Download>): Promise<Download> {
    const at = this.#reports.sent();
    try {
      const d = await call();
      this.#put(d, false, at);
      return d;
    } finally {
      this.#reports.landed();
    }
  }

  /**
   * Turns the completion view on or off (Settings.showCompleteDialog). Settings are
   * replaced as a whole: they are read first, and every other field is written back as
   * it was.
   */
  async setShowCompleteDialog(show: boolean): Promise<void> {
    const current = await this.api.getSettings();
    const saved = await this.api.updateSettings({ ...current, showCompleteDialog: show });
    this.#settingsGen++;
    this.settings = saved;
  }
}
