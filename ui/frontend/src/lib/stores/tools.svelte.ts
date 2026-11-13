// yt-dlp and Deno as the window knows them (SITE-MEDIA-SPEC §4, §5.4): downexd downloads
// them on first use for video pages and keeps its own copies up to date. Their state comes
// from GetTools and from the `tools` entries of Watch (a subscription's first batch, then
// every change); Settings → Video sites installs or updates them (InstallTools, which
// answers when the install ends while its progress arrives as events).

import { toApiError, type Api, type EventBatch, type ToolInfo, type ToolsStatus, type Unsubscribe } from '../api';
import { formatPercent } from '../format';
import { siteErrorText } from '../media';

const TOOL_NAMES: Record<string, string> = { 'yt-dlp': 'yt-dlp', deno: 'Deno' };

/** "yt-dlp", "Deno"; another name as it is. */
export function toolName(name: string): string {
  return TOOL_NAMES[name] ?? name;
}

/** Some tool is being downloaded or updated. */
export function isInstalling(status: ToolsStatus | null): boolean {
  return status?.tools.some((t) => t.state === 'installing') ?? false;
}

/**
 * The first-use install: downloading while nothing can resolve pages yet. An update of a
 * ready copy, which keeps working meanwhile, is not one (it shows no toast).
 */
export function isFirstInstall(status: ToolsStatus | null): boolean {
  return status !== null && status.enabled && !status.ready && isInstalling(status);
}

/** The tools that failed (their last install or check). */
export function failedTools(status: ToolsStatus | null): ToolInfo[] {
  return status?.tools.filter((t) => t.state === 'failed') ?? [];
}

/**
 * How far the running install is, 0..1 over the tools being downloaded; NaN while any of
 * them has no known total (or none is being downloaded).
 */
export function installFraction(status: ToolsStatus | null): number {
  const installing = status?.tools.filter((t) => t.state === 'installing') ?? [];
  if (installing.length === 0 || installing.some((t) => !(t.bytesTotal > 0))) return Number.NaN;
  const total = installing.reduce((sum, t) => sum + t.bytesTotal, 0);
  const done = installing.reduce((sum, t) => sum + Math.min(t.bytesDone, t.bytesTotal), 0);
  return Math.min(1, Math.max(0, done / total));
}

/** "Downloading yt-dlp and Deno: 34%" (the tools being downloaded); "" while none is. */
export function installText(status: ToolsStatus | null, fallbackFraction = Number.NaN, withPercent = true): string {
  const names = (status?.tools ?? []).filter((t) => t.state === 'installing').map((t) => toolName(t.name));
  const fraction = Number.isFinite(installFraction(status)) ? installFraction(status) : fallbackFraction;
  const what = `Downloading ${names.length > 0 ? names.join(' and ') : 'yt-dlp and Deno'}`;
  // beside a progress bar, which shows the percentage itself, the text names only what downloads
  return withPercent && Number.isFinite(fraction) ? `${what}: ${formatPercent(fraction)}` : `${what}…`;
}

export class ToolsStore {
  readonly api: Pick<Api, 'getTools' | 'installTools' | 'onEvents'>;
  #unsubscribe: Unsubscribe | null = null;
  #loading: Promise<void> | null = null;

  /** The last state downexd reported (null until it reported one). */
  status = $state.raw<ToolsStatus | null>(null);
  /** downexd has the tools (an older one answers unimplemented: "Update Downex"). */
  supported = $state(true);
  loadError = $state<string | null>(null);
  /** The install the user started runs ('update' also looks for newer releases). */
  busy = $state<'install' | 'update' | null>(null);
  /** Why the user's install failed (the daemon's tools_unavailable text). */
  installError = $state<string | null>(null);

  readonly installing: boolean = $derived(isInstalling(this.status));
  readonly fraction: number = $derived(installFraction(this.status));

  constructor(api: Pick<Api, 'getTools' | 'installTools' | 'onEvents'>) {
    this.api = api;
  }

  /** Subscribes to the tools events. */
  start(): void {
    if (this.#unsubscribe) return;
    this.#unsubscribe = this.api.onEvents((batch) => this.apply(batch));
  }

  dispose(): void {
    this.#unsubscribe?.();
    this.#unsubscribe = null;
  }

  apply(batch: EventBatch): void {
    for (const ev of batch) {
      if (ev.kind !== 'tools') continue;
      this.status = ev.status;
      this.supported = true;
    }
  }

  /** Reads the state (concurrent calls share one read). */
  load(): Promise<void> {
    this.#loading ??= this.api
      .getTools()
      .then(
        (status) => {
          this.status = status;
          this.supported = true;
          this.loadError = null;
        },
        (err) => {
          const e = toApiError(err);
          if (e.code === 'unimplemented') this.supported = false;
          else this.loadError = e.message;
        },
      )
      .finally(() => {
        this.#loading = null;
      });
    return this.#loading;
  }

  /**
   * Installs the tools (update: also newer releases), or joins the install that runs;
   * resolves to whether it ended well. Its progress arrives as events meanwhile.
   */
  async install(update: boolean): Promise<boolean> {
    if (this.busy !== null) return false;
    this.busy = update ? 'update' : 'install';
    this.installError = null;
    try {
      this.status = await this.api.installTools(update);
      this.supported = true;
      return true;
    } catch (err) {
      const e = toApiError(err);
      if (e.code === 'unimplemented') this.supported = false;
      this.installError = siteErrorText(e);
      // What the failure left (each tool's error) is shown from a fresh read.
      void this.load();
      return false;
    } finally {
      this.busy = null;
    }
  }
}
