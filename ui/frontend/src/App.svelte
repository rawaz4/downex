<script lang="ts">
  import { onDestroy, untrack } from 'svelte';
  import {
    DEFAULT_SETTINGS,
    MAIN_QUEUE_ID,
    isActive,
    isAwaiting,
    isResumable,
    needsRestart,
    queueOf,
    toApiError,
    type AddMediaRequest,
    type AddRequest,
    type Api,
    type BuildInfo,
    type Capabilities,
    type ConfirmOptions,
    type Download,
    type ExistingFile,
    type PostAction,
    type Settings,
  } from './lib/api';
  import {
    ALL_LABEL,
    OTHER_LABEL,
    STATUS_FILTERS,
    STATUS_FILTER_LABELS,
    isExecutableName,
    kindOf,
    rawName,
  } from './lib/categories';
  import { nextConfirmation } from './lib/confirm';
  import { DownloadsStore, type AddOutcome, type Outcome } from './lib/stores/downloads.svelte';
  import { QueuesStore } from './lib/stores/queues.svelte';
  import { toasts } from './lib/stores/toasts.svelte';
  import { ToolsStore, failedTools, installText, isFirstInstall } from './lib/stores/tools.svelte';
  import { splitUrls, type SettingsPage } from './lib/validate';
  import { escapeBidi, hasBidiControls, isolate } from './lib/text';
  import TitleBar from './lib/components/TitleBar.svelte';
  import Toolbar, { type ChipFilter } from './lib/components/Toolbar.svelte';
  import Sidebar from './lib/components/Sidebar.svelte';
  import DownloadTable from './lib/components/DownloadTable.svelte';
  import DetailsPanel, { type SelectionAction } from './lib/components/DetailsPanel.svelte';
  import StatusBar, { type LimitChoice, type WhenDoneChoice } from './lib/components/StatusBar.svelte';
  import ContextMenu, { type MenuItem } from './lib/components/ContextMenu.svelte';
  import AddDialog from './lib/components/AddDialog.svelte';
  import SettingsDialog from './lib/components/SettingsDialog.svelte';
  import RemoveDialog from './lib/components/RemoveDialog.svelte';
  import RestartDialog, { type AgainFile } from './lib/components/RestartDialog.svelte';
  import Toasts, { Notices } from './lib/components/Toasts.svelte';
  import CommandPalette, { type Command, type PaletteDownload } from './lib/components/CommandPalette.svelte';
  import type { RowAction } from './lib/components/DownloadRow.svelte';
  import { theme } from './lib/stores/theme.svelte';
  import { density } from './lib/stores/density.svelte';
  import { formatCount, formatDriveLabel, formatSpeed, hostOf } from './lib/format';
  import { mediaBadge } from './lib/display';
  import { isSite } from './lib/media';
  import { postActionChoices, whenDoneText } from './lib/queues';
  import { Arrivals } from './lib/motion.svelte';
  import ConnectionBanner from './lib/components/ConnectionBanner.svelte';
  import OpenDialog from './lib/components/OpenDialog.svelte';
  import ConfirmDialog from './lib/components/ConfirmDialog.svelte';
  import QueueDialog from './lib/components/QueueDialog.svelte';
  import MoveToQueueDialog from './lib/components/MoveToQueueDialog.svelte';
  import PostActionBanner from './lib/components/PostActionBanner.svelte';
  import ProgressBar from './lib/components/ProgressBar.svelte';
  import Icon from './lib/components/Icon.svelte';

  interface Props {
    api: Api;
    /** What the host's system can do (Startup): hibernation and wake timers for queues, and a frameless window whose title bar the page draws (customTitleBar). */
    capabilities?: Capabilities;
    /** What this window is built from (Startup; Settings → About); absent from older hosts. */
    build?: BuildInfo;
  }

  let { api, capabilities = { openWith: true, hibernate: true, wakeTimers: false }, build }: Props = $props();

  /** Which way the Add dialog opens (the Add split button's menu). */
  type AddMode = 'files' | 'stream';

  type DialogState =
    | { kind: 'add'; urls: string; mode?: AddMode }
    /**
     * `addLogin`: open Network with a new site login for this address pattern. `focusField`:
     * the field that takes the focus (the status bar's "Custom…" speed limit).
     */
    | { kind: 'settings'; page?: SettingsPage; addLogin?: string; focusField?: 'globalSpeedLimit' }
    | { kind: 'queues'; id: number | null }
    | { kind: 'move'; ids: number[] }
    | { kind: 'remove'; ids: number[]; deleteFile: boolean }
    /** `again`: the completed ones among `ids` (downloaded again) and whether their files are there. */
    | { kind: 'restart'; ids: number[]; reason: 'validator_changed' | 'manual' | 'completed'; again: AgainFile[] }
    | { kind: 'open'; id: number }
    /** `requested`: the user asked for it (else it opened by itself). */
    | { kind: 'confirm'; id: number; requested: boolean };

  const DETAILS_KEY = 'downex.detailsCollapsed';

  function readCollapsed(): boolean {
    try {
      return localStorage.getItem(DETAILS_KEY) === '1';
    } catch {
      return false;
    }
  }

  // svelte-ignore state_referenced_locally
  const store = new DownloadsStore(api);
  // svelte-ignore state_referenced_locally
  const queues = new QueuesStore(api);
  let settings = $state.raw<Settings>(DEFAULT_SETTINGS);
  let dialog = $state.raw<DialogState | null>(null);
  let menu = $state.raw<{ x: number; y: number } | null>(null);
  let table: ReturnType<typeof DownloadTable> | undefined = $state();
  let searchInput: HTMLInputElement | undefined = $state();
  let statusBar: ReturnType<typeof StatusBar> | undefined = $state();
  let detailsCollapsed = $state(readCollapsed());
  let palette = $state(false);
  const notices = new Notices();
  // yt-dlp and Deno for video pages (SITE-MEDIA-SPEC §5.4): their state from the events.
  // svelte-ignore state_referenced_locally
  const tools = new ToolsStore(api);

  // Confirmation dialogs (DESKTOP-SPEC §1): one at a time, oldest first, for every
  // download awaiting confirmation; closing a dialog leaves its download waiting (and
  // its dialog closed until it is asked for again).
  let dismissed = $state.raw<ReadonlySet<number>>(new Set());
  /** A download whose dialog the user asked for (it comes before older ones). */
  let preferred = $state<number | null>(null);

  // svelte-ignore state_referenced_locally
  const stopConfirmRequests = api.onConfirmRequest((id) => requestConfirm(id, false));
  // Settings… in the menu bar (macOS) does what ⌘, does: it opens the Settings dialog
  // unless another dialog is open.
  // svelte-ignore state_referenced_locally
  const stopMenuCommands = api.onMenuCommand((command) => {
    if (command !== 'settings' || dialog) return;
    openSettings();
  });

  queues.start();
  tools.start();
  void store.start();
  store.startStatusPolling();
  onDestroy(() => {
    stopConfirmRequests();
    stopMenuCommands();
    queues.dispose();
    tools.dispose();
    store.dispose();
    notices.clear();
  });

  function applySettings(s: Settings): void {
    settings = s;
    settingsLoaded = true;
    store.setCategories(s.categories);
  }

  // Settings come from downexd, which may be unreachable when the window opens, restart,
  // or be changed by another client (the CLI, the tray). They are read whenever downexd is
  // reachable and they were not read since it last was (again at every status check after
  // a failed read), when the window gets focus, and before each confirmation dialog.
  /** `settings` were read from downexd since it last became reachable. */
  let settingsLoaded = $state(false);
  let settingsRead: Promise<boolean> | null = null;

  /** Reads the settings (concurrent calls share one read); resolves to whether that worked. */
  function loadSettings(): Promise<boolean> {
    settingsRead ??= store.api
      .getSettings()
      .then(
        (s) => {
          applySettings(s);
          return true;
        },
        () => {
          // keep the last ones; the Settings dialog reports the error when opened
          settingsLoaded = false;
          return false;
        },
      )
      .finally(() => {
        settingsRead = null;
      });
    return settingsRead;
  }

  $effect(() => {
    void store.daemon; // a new value at every status check
    if (stale) {
      settingsLoaded = false;
      return;
    }
    if (!settingsLoaded) void loadSettings();
  });

  function onWindowFocus(): void {
    if (!stale) void loadSettings();
  }

  // Queues (M6-SPEC §1) are read like the settings: whenever downexd is reachable and they
  // were not read since it last was; their events keep them current in between.
  $effect(() => {
    void store.daemon;
    if (stale) {
      queues.invalidate();
      return;
    }
    if (!queues.loaded) void queues.load();
  });

  // A queue filter on a queue that was deleted shows every queue again.
  $effect(() => {
    if (queues.loaded) store.setQueues(queues.ids);
  });

  /** Queues to choose from in the dialogs (none while downexd has no queues). */
  const queueChoices = $derived(queues.supported ? queues.list : []);

  /** The countdown of a post-queue action, while downexd can still be asked to cancel it. */
  const countdown = $derived(store.stale ? null : queues.nextAction);

  async function cancelPostAction(queueId: number): Promise<void> {
    const cancelled = await queues.cancelPostAction(queueId);
    if (cancelled) toasts.success('Cancelled: nothing happens after the queue this time.');
    else toasts.show('The countdown had already ended: nothing left to cancel.');
  }

  function openQueues(id: number | null = store.queue) {
    menu = null;
    dialog = { kind: 'queues', id };
  }

  async function moveToQueue(ids: readonly number[], queueId: number): Promise<void> {
    await queues.move(ids, queueId, -1);
    store.assignQueue(ids, queueId);
    const name = isolate(queues.nameOf(queueId));
    toasts.success(ids.length === 1 ? `Moved to ${name}` : `${ids.length} downloads moved to ${name}`);
  }

  /** The site login pattern for the server that asked for credentials: its origin, any path. */
  function loginPatternFor(d: Download): string {
    try {
      const u = new URL(d.finalUrl || d.url);
      return `${u.protocol}//${u.host}/*`;
    } catch {
      return '';
    }
  }

  function addSiteLogin(d: Download) {
    if (stale) return unavailable();
    openSettings({ page: 'network', addLogin: loginPatternFor(d) });
  }

  /** Opens the Settings dialog (on a page, with a field focused). */
  function openSettings(opts: Omit<Extract<DialogState, { kind: 'settings' }>, 'kind'> = {}): void {
    menu = null;
    dialog = { kind: 'settings', ...opts };
  }

  // While downexd is unreachable the list is the last known state: nothing that changes
  // downloads is offered (it could only fail, one toast at a time).
  const stale = $derived(store.stale);
  const selectedIds = $derived(store.selected.map((d) => d.id));
  const canPause = $derived(!stale && store.selected.some((d) => isActive(d.state)));
  const canResume = $derived(!stale && store.selected.some((d) => isResumable(d.state)));
  const canRemove = $derived(!stale && store.selected.length > 0);
  const canAdd = $derived(!stale);
  const canPauseAll = $derived(!stale && store.totals.active + store.totals.queued > 0);
  // As the daemon's ResumeAll: paused downloads only (failed, canceled and awaiting ones stay).
  const canResumeAll = $derived(!stale && store.all.some((d) => d.state === 'paused'));

  // Its category selector and folder proposals need the current settings.
  const nextConfirm = $derived(
    store.ready && !stale && settingsLoaded ? nextConfirmation(store.awaiting, dismissed, preferred) : null,
  );

  // Show the next confirmation dialog once nothing else is open, with settings read just
  // before (a failed read clears settingsLoaded, and the dialog waits for the next one).
  $effect(() => {
    const id = nextConfirm;
    if (id === null || dialog !== null || menu !== null) return;
    let current = true;
    void loadSettings().then((ok) => {
      if (!current || !ok || dialog !== null || menu !== null) return;
      const requested = preferred === id;
      if (requested) preferred = null;
      dialog = { kind: 'confirm', id, requested };
    });
    return () => {
      current = false;
    };
  });

  // A download confirmed or removed elsewhere (or by its dialog) takes its dialog along.
  $effect(() => {
    if (dialog?.kind !== 'confirm') return;
    const d = store.get(dialog.id);
    if (!d || !isAwaiting(d.state)) dialog = null;
  });

  /** Asks for the dialog of `id`: again if it was closed, and first when the user asked. */
  function requestConfirm(id: number, explicit: boolean): void {
    if (dismissed.has(id)) {
      const next = new Set(dismissed);
      next.delete(id);
      dismissed = next;
    }
    if (explicit) preferred = id;
  }

  /** Closing the dialog of `id` (if it is still the one shown) leaves the download waiting. */
  function closeConfirm(id: number): void {
    if (!dismissed.has(id)) dismissed = new Set([...dismissed, id]);
    if (preferred === id) preferred = null;
    if (dialog?.kind === 'confirm' && dialog.id === id) dialog = null;
  }

  // Downloads handed over by the browser (or seen on the clipboard) say so in a toast:
  // one awaiting confirmation offers its dialog now or later (when a dialog is in the
  // way of the one that opens by itself); one already added offers to show it.
  const arrivals = new Arrivals();
  $effect(() => {
    const { added } = arrivals.update(
      store.all.map((d) => d.id),
      store.ready,
    );
    if (added.length === 0) return;
    untrack(() => {
      for (const id of added) {
        const d = store.get(id);
        if (!d || (d.source !== 'browser' && d.source !== 'clipboard')) continue;
        const from = d.source === 'browser' ? 'from the browser' : 'from the clipboard';
        const name = escapeBidi(store.nameOf(id));
        if (isAwaiting(d.state)) {
          if (dialog === null && menu === null && !palette) continue;
          notices.show({
            key: `dl-${id}`,
            title: `Download ${from} to confirm`,
            detail: name,
            actions: [
              { label: 'Later', run: () => closeConfirm(id) },
              { label: 'Review…', primary: true, run: () => requestConfirm(id, true) },
            ],
          });
        } else {
          notices.show({
            key: `dl-${id}`,
            title: `Download added ${from}`,
            detail: name,
            actions: [{ label: 'Show', primary: true, run: () => showDownload(id) }],
          });
        }
      }
    });
  });

  // One that was confirmed or removed takes its toast along.
  $effect(() => {
    for (const n of notices.items) {
      if (!n.key.startsWith('dl-')) continue;
      const id = Number(n.key.slice(3));
      const d = store.get(id);
      if (!d || (n.actions.length > 1 && !isAwaiting(d.state))) untrack(() => notices.dismiss(n.key));
    }
  });

  // The first use of video pages (SITE-MEDIA-SPEC §5.4): a progress toast while downexd
  // downloads yt-dlp and Deno (not for updates of working copies), and a toast that offers
  // the settings when that failed. There is no modal first-use dialog.
  const toolsToast = $derived(!stale && isFirstInstall(tools.status));
  let toolsWereInstalling = false;
  $effect(() => {
    const status = tools.status;
    if (isFirstInstall(status)) {
      toolsWereInstalling = true;
      return;
    }
    if (!toolsWereInstalling) return;
    toolsWereInstalling = false;
    const failed = failedTools(status);
    if (!status || status.ready || failed.length === 0) return;
    untrack(() =>
      notices.show({
        key: 'tools',
        title: 'Downex could not set up yt-dlp/Deno',
        detail: escapeBidi(failed[0]?.error || 'Video downloads from web pages need them.'),
        actions: [{ label: 'Open settings', primary: true, run: () => openVideoSettings() }],
      }),
    );
  });

  /** Settings → Video sites (from the Add dialog or a toast). */
  function openVideoSettings(): void {
    if (stale) return unavailable();
    openSettings({ page: 'video' });
  }

  /** Shows a download in the list: every filter cleared, selected, the list focused. */
  function showDownload(id: number): void {
    if (!store.get(id)) return;
    if (store.search !== '') store.setSearch('');
    if (store.category !== 'all') store.setCategory('all');
    if (store.status !== 'any') store.setStatusFilter('any');
    if (store.queue !== null) store.setQueueFilter(null);
    store.selectOnly(id);
    table?.focus();
  }

  async function confirmDownload(id: number, opts: ConfirmOptions): Promise<void> {
    const d = await store.confirm(id, opts);
    if (opts.start && settings.showProgressWindow && startsNow(d)) void openProgress(id);
  }

  // Progress windows (DESKTOP-SPEC §7): one process per download, opened by the host.

  /** A download that has a progress to show: anything not finished or waiting for confirmation. */
  function hasProgress(d: Download): boolean {
    return d.state !== 'completed' && !isAwaiting(d.state);
  }

  /**
   * The daemon started it (or is about to) rather than keeping it paused or putting it in
   * the queue behind max_concurrent_downloads (DESKTOP-SPEC §7: those start later and open
   * no window, like a queued browser hand-off).
   */
  function startsNow(d: Download): boolean {
    if (d.state === 'queued' && d.queuePosition > 0) return false;
    return d.state !== 'paused' && !isAwaiting(d.state) && d.state !== 'completed';
  }

  async function openProgress(id: number): Promise<void> {
    try {
      await api.openProgressWindow(id);
    } catch (err) {
      toasts.error(`Could not open the progress window of ${isolate(store.nameOf(id))}: ${isolate(toApiError(err).message)}`);
    }
  }

  /**
   * After the user resumed or restarted exactly one download in this window (DESKTOP-SPEC §7):
   * its progress window, when that is on and downexd starts it now (one it queues behind
   * max_concurrent_downloads opens nothing, as for an add). Resume all, several downloads,
   * queues, the scheduler, retries and the CLI never open one.
   */
  function showProgressIfStarted(id: number): void {
    const d = store.get(id);
    if (d && settings.showProgressWindow && startsNow(d)) void openProgress(id);
  }

  /** Adds downloads; one that starts at once gets its progress window (when that is on). */
  async function addDownloads(requests: AddRequest[]): Promise<AddOutcome[]> {
    const outcomes = await store.add(requests);
    // A list of addresses opens no windows: one per download would bury the screen.
    const only = outcomes.length === 1 ? outcomes[0] : undefined;
    if (only?.ok && settings.showProgressWindow && startsNow(only.download)) void openProgress(only.download.id);
    return outcomes;
  }

  async function addMediaDownload(req: AddMediaRequest): Promise<Download> {
    const d = await store.addMedia(req);
    if (settings.showProgressWindow && startsNow(d)) void openProgress(d.id);
    return d;
  }

  async function cancelDownload(id: number): Promise<void> {
    const [outcome] = await store.remove([id], false);
    if (outcome && !outcome.ok) throw new Error(outcome.error);
  }

  async function pauseAll() {
    try {
      const n = await store.pauseAll();
      if (n === 0) toasts.show('Nothing to pause');
      else toasts.success(n === 1 ? '1 download paused' : `${n} downloads paused`);
    } catch (err) {
      toasts.error(`Could not pause all downloads: ${isolate(toApiError(err).message)}`);
    }
  }

  async function resumeAll() {
    try {
      const n = await store.resumeAll();
      if (n === 0) toasts.show('Nothing to resume');
      else toasts.success(n === 1 ? '1 download resumed' : `${n} downloads resumed`);
    } catch (err) {
      toasts.error(`Could not resume all downloads: ${isolate(toApiError(err).message)}`);
    }
  }

  function unavailable(): void {
    toasts.show('Not available while downexd is unreachable.');
  }

  function toggleDetails(): void {
    detailsCollapsed = !detailsCollapsed;
    try {
      localStorage.setItem(DETAILS_KEY, detailsCollapsed ? '1' : '0');
    } catch {
      // storage unavailable: the choice lasts for this session
    }
  }

  const gridElement = () => table?.element();

  // ---- the page header (DESIGN-SYSTEM.md §4.3) ----

  /** "All downloads", or the names of the filters shown ("Video · Paused", "Main queue"). */
  const pageTitle = $derived.by(() => {
    const parts: string[] = [];
    if (store.category !== 'all') parts.push(escapeBidi(store.index.labelOf(store.category)));
    if (store.queue !== null) parts.push(escapeBidi(queues.nameOf(store.queue)));
    if (store.status !== 'any') parts.push(STATUS_FILTER_LABELS[store.status]);
    return parts.length > 0 ? parts.join(' · ') : ALL_LABEL;
  });

  const chips = $derived({
    downloading: store.counts.statuses.active,
    queued: store.counts.statuses.queued,
    failed: store.counts.statuses.failed,
  });

  function onChip(filter: ChipFilter): void {
    store.setStatusFilter(store.status === filter ? 'any' : filter);
  }

  // ---- the status bar's menus (DESIGN-SYSTEM.md §3.14) ----

  /** Sets the global speed limit with a full settings round-trip; "Custom…" opens Settings. */
  async function setGlobalLimit(choice: LimitChoice): Promise<void> {
    if (choice === 'custom') {
      openSettings({ page: 'downloads', focusField: 'globalSpeedLimit' });
      return;
    }
    if (stale) return unavailable();
    try {
      const current = await api.getSettings();
      const saved = await api.updateSettings({ ...current, globalSpeedLimit: choice });
      applySettings(saved);
      const limit = saved.globalSpeedLimit;
      toasts.success(limit > 0 ? `Global speed limit: ${formatSpeed(limit)}` : 'Global speed limit: unlimited');
    } catch (err) {
      toasts.error(`Could not change the global speed limit: ${isolate(toApiError(err).message)}`);
    }
  }

  /** The main queue's post-queue action; null while downexd has no queues (the control hides). */
  const whenDone = $derived<PostAction | null>(
    queues.supported && queues.loaded ? (queues.get(MAIN_QUEUE_ID)?.postAction ?? null) : null,
  );
  const whenDoneChoices = $derived(postActionChoices(capabilities, whenDone ?? 'none'));

  async function setWhenDone(choice: WhenDoneChoice): Promise<void> {
    if (choice === 'other') {
      openQueues(queueChoices.find((q) => q.id !== MAIN_QUEUE_ID)?.id ?? MAIN_QUEUE_ID);
      return;
    }
    if (stale) return unavailable();
    try {
      await queues.setPostAction(MAIN_QUEUE_ID, choice);
      toasts.success(`${whenDoneText(choice)} (main queue)`);
    } catch (err) {
      toasts.error(`Could not change what happens when the main queue is done: ${isolate(toApiError(err).message)}`);
    }
  }

  // ---- free space of the download folder's drive ----
  // Read at start, when the settings change, after each completion and every minute while the
  // window is visible; hidden when the host cannot tell (an older host: unimplemented).

  const FREE_SPACE_MS = 60_000;
  let freeBytes = $state<number | null>(null);
  let freeUnsupported = false;
  let freeReads = 0;

  async function readFreeSpace(): Promise<void> {
    const dir = settings.defaultDir;
    const n = ++freeReads;
    if (!settingsLoaded || dir === '') {
      freeBytes = null;
      return;
    }
    if (freeUnsupported || stale) return;
    try {
      const bytes = await api.freeSpace(dir);
      if (n === freeReads) freeBytes = bytes;
    } catch (err) {
      if (n !== freeReads) return;
      freeBytes = null;
      if (toApiError(err).code === 'unimplemented') freeUnsupported = true;
    }
  }

  $effect(() => {
    void settings.defaultDir;
    void settingsLoaded;
    untrack(() => void readFreeSpace());
  });

  const completedCount = $derived(store.all.reduce((n, d) => n + (d.state === 'completed' ? 1 : 0), 0));
  let lastCompleted = -1;
  $effect(() => {
    const n = completedCount;
    const grew = lastCompleted >= 0 && n > lastCompleted;
    lastCompleted = n;
    if (grew) untrack(() => void readFreeSpace());
  });

  $effect(() => {
    const timer = setInterval(() => {
      if (typeof document === 'undefined' || document.visibilityState !== 'hidden') void readFreeSpace();
    }, FREE_SPACE_MS);
    return () => clearInterval(timer);
  });

  /** What is left to download: downloading, queued and paused downloads of known size. */
  const needBytes = $derived(
    store.all.reduce(
      (sum, d) =>
        (d.state === 'downloading' || d.state === 'queued' || d.state === 'paused') && d.size > 0
          ? sum + Math.max(0, d.size - d.received)
          : sum,
      0,
    ),
  );

  function report(outcomes: readonly Outcome[], verb: string): void {
    const failed = outcomes.filter((o): o is Extract<Outcome, { ok: false }> => !o.ok);
    const first = failed[0];
    if (!first) return;
    // Inserted names and daemon messages are bidi-isolated within the sentence.
    toasts.error(
      failed.length === 1
        ? `Could not ${verb} ${isolate(store.nameOf(first.id))}: ${isolate(first.error)}`
        : `Could not ${verb} ${failed.length} downloads: ${isolate(first.error)}`,
    );
  }

  async function pause(ids: readonly number[] = selectedIds) {
    report(await store.pause(ids), 'pause');
  }

  async function resume(ids: readonly number[] = selectedIds) {
    const restartIds = ids.filter((id) => {
      const d = store.get(id);
      return d !== undefined && needsRestart(d);
    });
    const plain = ids.filter((id) => !restartIds.includes(id));
    if (plain.length > 0) {
      const outcomes = await store.resume(plain);
      report(outcomes, 'resume');
      const only = ids.length === 1 ? outcomes[0] : undefined;
      if (only?.ok) showProgressIfStarted(only.id);
    }
    if (restartIds.length > 0) void requestRestart(restartIds, 'validator_changed');
  }

  function togglePauseResume() {
    if (canPause) void pause();
    else if (canResume) void resume();
  }

  function requestRemove(ids: readonly number[] = selectedIds, deleteFile = false) {
    if (ids.length === 0) return;
    if (stale) return unavailable();
    dialog = { kind: 'remove', ids: [...ids], deleteFile };
  }

  /** Awaiting downloads have not started; a completed one is downloaded again (ENGINE-SPEC §7.1). */
  function restartable(d: Download): boolean {
    return !isAwaiting(d.state);
  }

  /**
   * The Restart / Download again dialog. For the completed downloads it first asks the host whether
   * their files are still there: one that is gone has nothing to keep or replace (a host that
   * cannot tell is taken as "there", so the choice is offered).
   */
  async function requestRestart(ids: readonly number[], reason: 'validator_changed' | 'manual' = 'manual'): Promise<void> {
    const targets = ids.filter((id) => {
      const d = store.get(id);
      return d !== undefined && restartable(d);
    });
    if (targets.length === 0) return;
    if (stale) return unavailable();
    const completed = targets.filter((id) => store.get(id)?.state === 'completed');
    const again = await Promise.all(
      completed.map(async (id): Promise<AgainFile> => {
        const d = store.get(id);
        const exists = await api.fileExists(id).catch(() => true);
        return { name: store.nameOf(id), exists, site: d?.kind === 'media' && isSite(d.media) };
      }),
    );
    // Another dialog (a confirmation that opened by itself) came first: this one is not shown.
    if (dialog !== null) return;
    dialog = { kind: 'restart', ids: targets, reason: completed.length === targets.length ? 'completed' : reason, again };
  }

  /** The restart dialog's Restart / Download again: one download gets its progress window. */
  async function restartDownloads(ids: readonly number[], existingFile: ExistingFile): Promise<void> {
    const outcomes = await store.restart(ids, existingFile);
    report(outcomes, 'restart');
    const only = ids.length === 1 ? outcomes[0] : undefined;
    if (only?.ok) showProgressIfStarted(only.id);
  }

  function openAdd(urls = '', mode?: AddMode) {
    menu = null;
    if (stale) return unavailable();
    dialog = mode ? { kind: 'add', urls, mode } : { kind: 'add', urls };
  }

  /**
   * Enter or a double-click on a row: opens a completed download, shows the progress
   * window of an unfinished one, or the dialog of one awaiting confirmation. Programs,
   * and names that hide their real type with bidi control characters, are confirmed
   * first: opening them can run code.
   */
  function openDownload(d: Download) {
    if (isAwaiting(d.state)) {
      if (stale) return unavailable();
      requestConfirm(d.id, true);
      return;
    }
    if (d.state !== 'completed') {
      // An unfinished download shows its progress window (whatever the setting says).
      if (stale) return unavailable();
      void openProgress(d.id);
      return;
    }
    const name = rawName(d);
    if (isExecutableName(name) || hasBidiControls(name)) {
      dialog = { kind: 'open', id: d.id };
      return;
    }
    void openFile(d.id);
  }

  async function openFile(id: number) {
    try {
      await store.openFile(id);
    } catch (err) {
      toasts.error(`Could not open ${isolate(store.nameOf(id))}: ${isolate(toApiError(err).message)}`);
    }
  }

  async function openFolder(d: Download) {
    try {
      await store.openFolder(d.id);
    } catch (err) {
      toasts.error(`Could not open the folder: ${isolate(toApiError(err).message)}`);
    }
  }

  async function copyUrls(ids: readonly number[]) {
    try {
      const n = await store.copyUrls(ids);
      if (n > 0) toasts.success(n === 1 ? 'URL copied' : `${n} URLs copied`);
    } catch (err) {
      toasts.error(`Could not copy: ${isolate(toApiError(err).message)}`);
    }
  }

  /** A row's hover action: the same as the toolbar's and the menu's, for that one download. */
  function onRowAction(action: RowAction, d: Download): void {
    if (stale) return unavailable();
    switch (action) {
      case 'pause':
        void pause([d.id]);
        break;
      case 'resume':
        void resume([d.id]);
        break;
      case 'confirm':
        requestConfirm(d.id, true);
        break;
      case 'folder':
        void openFolder(d);
        break;
      case 'remove':
        requestRemove([d.id], false);
        break;
      case 'restart':
        void requestRestart([d.id], needsRestart(d) ? 'validator_changed' : 'manual');
        break;
    }
  }

  /** The details pane's strip for several selected downloads. */
  function onSelectionAction(action: SelectionAction): void {
    if (action === 'pause') void pause();
    else if (action === 'resume') void resume();
    else requestRemove();
  }

  /** The details pane's speed limit of one download (DESKTOP-SPEC §7's SetDownloadOptions). */
  async function setDownloadLimit(d: Download, bytesPerSecond: number): Promise<void> {
    if (stale) return unavailable();
    const name = isolate(store.nameOf(d.id));
    try {
      await api.setDownloadOptions(d.id, { speedLimit: bytesPerSecond });
      toasts.success(bytesPerSecond > 0 ? `${name}: limited to ${formatSpeed(bytesPerSecond)}` : `${name}: no speed limit`);
    } catch (err) {
      toasts.error(`Could not change the speed limit of ${name}: ${isolate(toApiError(err).message)}`);
    }
  }

  /** The list's empty state: shows every download again. */
  function clearFilters(): void {
    if (store.search !== '') store.setSearch('');
    if (store.category !== 'all') store.setCategory('all');
    if (store.status !== 'any') store.setStatusFilter('any');
    if (store.queue !== null) store.setQueueFilter(null);
  }

  // The command palette (Ctrl+K, DESIGN-SYSTEM.md §3.16): every action of the window, with
  // what it does in plain words, found by typing; and the downloads.
  const commands: Command[] = $derived.by(() => {
    // why a command is unavailable (read with it): unreachable daemon, empty selection, else `none`
    const why = (needsSelection: boolean, none: string): string =>
      stale ? 'downexd is not reachable' : needsSelection && store.selected.length === 0 ? 'nothing selected' : none;
    const inUse = 'already in use';
    const sel = store.selected.length;
    const selected = sel === 1 ? 'the selected download' : `the ${formatCount(sel)} selected downloads`;
    // "its" for one download, "their" for several (and for none: the command is disabled then)
    const its = sel === 1 ? 'its' : 'their';
    const it = sel === 1 ? 'it' : 'they';
    const active = store.totals.active;
    const paused = store.counts.statuses.paused;
    const done = store.selected.filter((d) => d.state === 'completed').map((d) => d.id);
    const categoryLabel = escapeBidi(store.index.labelOf(store.category));
    const list: Command[] = [
      { id: 'add', group: 'Commands', label: 'Add download…', icon: 'add', shortcut: 'Ctrl+N', keywords: 'new url link', description: 'Paste or type the links of files to download', disabled: !canAdd, disabledReason: why(false, ''), run: () => openAdd() },
      { id: 'add-video', group: 'Commands', label: 'Add video or stream…', icon: 'stream', keywords: 'new youtube hls dash page', description: 'A video page or a stream address, saved as one MP4', disabled: !canAdd, disabledReason: why(false, ''), run: () => openAdd('', 'stream') },
      { id: 'pause', group: 'Commands', label: 'Pause selected', icon: 'pause', shortcut: 'Space', description: `Pauses ${selected}, keeps ${its} progress`, disabled: !canPause, disabledReason: why(true, 'nothing to pause in the selection'), run: () => void pause() },
      { id: 'resume', group: 'Commands', label: 'Resume selected', icon: 'resume', shortcut: 'Space', description: `Continues ${selected} where ${it} stopped`, disabled: !canResume, disabledReason: why(true, 'nothing to resume in the selection'), run: () => void resume() },
      { id: 'pause-all', group: 'Commands', label: 'Pause all', icon: 'pause-all', description: active > 0 ? active === 1 ? 'Stops the 1 active download, keeps its progress' : `Stops the ${formatCount(active)} active downloads, keeps their progress` : 'Holds the queued downloads, keeps their progress', disabled: !canPauseAll, disabledReason: why(false, 'nothing is downloading or queued'), run: () => void pauseAll() },
      { id: 'resume-all', group: 'Commands', label: 'Resume all', icon: 'resume-all', description: paused > 0 ? `Resumes the ${formatCount(paused)} paused download${paused === 1 ? '' : 's'}` : 'Resumes every paused download', disabled: !canResumeAll, disabledReason: why(false, 'nothing is paused'), run: () => void resumeAll() },
      { id: 'download-again', group: 'Commands', label: 'Download again…', icon: 'restart', keywords: 'restart redownload refresh completed finished', description: done.length > 1 ? `Gets the ${formatCount(done.length)} selected completed downloads again; asks what happens to their files` : 'Gets the selected completed download again; asks what happens to its file', disabled: stale || done.length === 0, disabledReason: why(true, 'nothing completed in the selection'), run: () => void requestRestart(done) },
      { id: 'remove', group: 'Commands', label: 'Remove selected…', icon: 'remove', shortcut: 'Del', keywords: 'delete', description: 'Asks first; the files stay unless you choose to delete them', disabled: !canRemove, disabledReason: why(true, ''), run: () => requestRemove() },
      { id: 'select-all', group: 'Commands', label: 'Select all', icon: 'check', shortcut: 'Ctrl+A', description: 'Every download the list shows', run: () => { store.selectAll(); table?.focus(); } },
      { id: 'limit', group: 'Commands', label: 'Set global speed limit…', icon: 'gauge', keywords: 'bandwidth throttle', description: `Now ${settings.globalSpeedLimit > 0 ? formatSpeed(settings.globalSpeedLimit) : 'unlimited'} · applies to every download`, disabled: stale || !settingsLoaded, disabledReason: stale ? 'downexd is not reachable' : 'the settings are not read yet', run: () => statusBar?.openLimitMenu() },
    ];
    if (whenDone !== null) {
      list.push({ id: 'when-done', group: 'Commands', label: 'When done…', icon: 'power', keywords: 'shut down sleep hibernate quit post queue', description: 'Keep running, sleep, shut down, or quit Downex', disabled: stale, disabledReason: 'downexd is not reachable', run: () => void statusBar?.openWhenDoneMenu() });
    }
    list.push(
      { id: 'search', group: 'View', label: 'Search downloads', icon: 'search', shortcut: 'Ctrl+F', keywords: 'find filter', description: 'Filters the list by name or address', run: () => { searchInput?.focus(); searchInput?.select(); } },
      { id: 'details', group: 'View', label: detailsCollapsed ? 'Show details pane' : 'Hide details pane', icon: 'sidebar-panel', keywords: 'panel', description: 'Facts, connections and speed of the selected download', run: toggleDetails },
      { id: 'density-comfortable', group: 'View', label: 'Density: comfortable', icon: 'density', keywords: 'rows list spacing', description: 'Two lines per download: name, then site and category', disabled: density.choice === 'comfortable', disabledReason: inUse, run: () => density.set('comfortable') },
      { id: 'density-compact', group: 'View', label: 'Density: compact', icon: 'density', keywords: 'rows list spacing dense', description: 'One line per download, about 18 rows', disabled: density.choice === 'compact', disabledReason: inUse, run: () => density.set('compact') },
      { id: 'theme-system', group: 'View', label: 'Theme: follow the system', icon: 'system', keywords: 'appearance dark light switch', description: 'Light or dark, as the system is set', disabled: theme.choice === 'system', disabledReason: inUse, run: () => theme.set('system') },
      { id: 'theme-light', group: 'View', label: 'Theme: light', icon: 'sun', keywords: 'appearance switch', description: 'Always light', disabled: theme.choice === 'light', disabledReason: inUse, run: () => theme.set('light') },
      { id: 'theme-dark', group: 'View', label: 'Theme: dark', icon: 'moon', keywords: 'appearance switch', description: 'Always dark', disabled: theme.choice === 'dark', disabledReason: inUse, run: () => theme.set('dark') },
      { id: 'settings', group: 'View', label: 'Open settings', icon: 'gear', shortcut: 'Ctrl+,', keywords: 'preferences options', description: 'Downloads, network, browser, video sites, appearance and updates', run: () => openSettings() },
    );
    if (queueChoices.length > 0) {
      list.push({ id: 'queues', group: 'View', label: 'Manage queues…', icon: 'queue', keywords: 'schedule', description: 'Order, limits, schedules and what happens when done', run: () => openQueues() });
    }
    const cats = [
      { key: 'all' as const, label: ALL_LABEL },
      ...store.index.entries.map((e) => ({ key: e.key, label: escapeBidi(e.name) })),
      { key: 'other' as const, label: OTHER_LABEL },
    ];
    for (const c of cats) {
      const n = store.counts.categories.get(c.key) ?? 0;
      list.push({ id: `cat-${c.key}`, group: 'Go to', label: `Category: ${c.label}`, icon: 'filter', keywords: 'filter show', description: `${formatCount(n)} download${n === 1 ? '' : 's'}`, run: () => store.setCategory(c.key) });
    }
    for (const st of STATUS_FILTERS) {
      const n = store.counts.statuses[st];
      list.push({ id: `status-${st}`, group: 'Go to', label: `Status: ${STATUS_FILTER_LABELS[st]}`, icon: 'filter', keywords: 'filter show', description: `${formatCount(n)} in ${categoryLabel}`, run: () => store.setStatusFilter(st) });
    }
    for (const q of queueChoices) {
      const n = store.queueCounts.get(q.id) ?? 0;
      list.push({ id: `queue-${q.id}`, group: 'Go to', label: `Queue: ${escapeBidi(q.name)}`, icon: 'queue', keywords: 'filter show', description: `${q.running ? 'Running' : 'Stopped'} · ${formatCount(n)} to do`, run: () => store.setQueueFilter(q.id) });
    }
    return list;
  });

  /** The downloads the palette finds: running ones first, then the newest. */
  const paletteDownloads: PaletteDownload[] = $derived.by(() => {
    if (!palette) return [];
    const running = (d: Download) => (d.state === 'downloading' || d.state === 'probing' ? 1 : 0);
    return [...store.all]
      .sort((a, b) => running(b) - running(a) || b.createdAt - a.createdAt || b.id - a.id)
      .map((d) => ({
        id: d.id,
        name: store.nameOf(d.id),
        host: escapeBidi(hostOf(d.finalUrl || d.url).replace(/^www\./i, '')),
        kind: kindOf(d),
        ...(d.kind === 'media' ? { tag: escapeBidi(mediaBadge(d)) } : {}),
        download: d,
      }));
  });

  /** Enter on a download in the palette: what its row's Enter does. */
  function openFromPalette(id: number): void {
    const d = store.get(id);
    if (d) openDownload(d);
  }

  /** Ctrl+Enter on a download in the palette: shows it in its folder. */
  function folderFromPalette(id: number): void {
    const d = store.get(id);
    if (d) void openFolder(d);
  }

  function openPalette(): void {
    menu = null;
    palette = true;
  }

  const menuItems: MenuItem[] = $derived.by(() => {
    const sel = store.selected;
    const one = sel.length === 1 ? sel[0] : undefined;
    const confirm: MenuItem[] =
      one && isAwaiting(one.state)
        ? [{ id: 'confirm', label: 'Confirm download…', icon: 'confirm', shortcut: 'Enter', disabled: stale }]
        : [];
    const progress: MenuItem[] =
      one && hasProgress(one)
        ? [{ id: 'progress', label: 'Show progress', icon: 'progress', shortcut: 'Enter', disabled: stale }]
        : [];
    return [
      ...confirm,
      ...progress,
      ...(confirm.length > 0 || progress.length > 0
        ? []
        : [{ id: 'open', label: 'Open', icon: 'open', shortcut: 'Enter', disabled: one?.state !== 'completed' } satisfies MenuItem]),
      { id: 'folder', label: 'Open folder', icon: 'folder', disabled: one === undefined },
      {
        id: 'toggle',
        label: canPause ? 'Pause' : 'Resume',
        icon: canPause ? 'pause' : 'resume',
        shortcut: 'Space',
        separated: true,
        disabled: !canPause && !canResume,
      },
      {
        id: 'restart',
        // every selected download is complete: they are downloaded again (the dialog asks about their files)
        label: sel.length > 0 && sel.every((d) => d.state === 'completed') ? 'Download again…' : 'Restart…',
        icon: 'restart',
        disabled: stale || !sel.some(restartable),
      },
      ...(one && one.errorCode === 'auth_required'
        ? [{ id: 'add-login', label: 'Add site login…', icon: 'add', disabled: stale } satisfies MenuItem]
        : []),
      ...(queueChoices.length > 1
        ? [{ id: 'move-queue', label: 'Move to queue…', icon: 'queue', disabled: stale || sel.length === 0 } satisfies MenuItem]
        : []),
      { id: 'resume-all', label: 'Resume all', icon: 'resume-all', disabled: !canResumeAll },
      { id: 'pause-all', label: 'Pause all', icon: 'pause-all', disabled: !canPauseAll },
      {
        id: 'copy',
        label: sel.length > 1 ? 'Copy URLs' : 'Copy URL',
        icon: 'copy',
        separated: true,
        disabled: sel.length === 0,
      },
      { id: 'remove', label: 'Remove…', icon: 'remove', shortcut: 'Del', separated: true, disabled: !canRemove },
      {
        id: 'remove-delete',
        label: 'Remove and delete file…',
        icon: 'remove',
        danger: true,
        shortcut: 'Shift+Del',
        disabled: !canRemove,
      },
    ];
  });

  function closeMenu() {
    menu = null;
    table?.focus();
  }

  function onMenuSelect(id: string) {
    const ids = selectedIds;
    const one = store.selected.length === 1 ? store.selected[0] : undefined;
    closeMenu();
    switch (id) {
      case 'confirm':
      case 'open':
        if (one) openDownload(one);
        break;
      case 'progress':
        if (one && !stale) void openProgress(one.id);
        break;
      case 'pause-all':
        void pauseAll();
        break;
      case 'resume-all':
        void resumeAll();
        break;
      case 'folder':
        if (one) void openFolder(one);
        break;
      case 'toggle':
        togglePauseResume();
        break;
      case 'restart':
        void requestRestart(ids);
        break;
      case 'add-login':
        if (one) addSiteLogin(one);
        break;
      case 'move-queue':
        if (stale) unavailable();
        else if (ids.length > 0) dialog = { kind: 'move', ids: [...ids] };
        break;
      case 'copy':
        void copyUrls(ids);
        break;
      case 'remove':
        requestRemove(ids, false);
        break;
      case 'remove-delete':
        requestRemove(ids, true);
        break;
    }
  }

  function isEditable(el: EventTarget | null): boolean {
    if (!(el instanceof HTMLElement)) return false;
    return el.isContentEditable || el.matches('input, textarea, select');
  }

  function onWindowKeydown(e: KeyboardEvent) {
    if (menu && e.key === 'Escape') {
      e.preventDefault();
      closeMenu();
      return;
    }
    if (dialog || menu || palette || e.defaultPrevented) return;
    // A menu of the page header or the status bar has the keyboard: its keys are its own.
    if (e.target instanceof Element && e.target.closest('[role="menu"]')) return;
    const mod = e.ctrlKey || e.metaKey;
    const key = e.key.toLowerCase();
    if (mod && !e.altKey && !e.shiftKey && key === 'k') {
      e.preventDefault();
      openPalette();
      return;
    }
    if (mod && !e.altKey && !e.shiftKey && key === 'n') {
      e.preventDefault();
      openAdd();
      return;
    }
    if (mod && !e.altKey && key === ',') {
      e.preventDefault();
      openSettings();
      return;
    }
    if (mod && !e.altKey && key === 'f') {
      e.preventDefault();
      searchInput?.focus();
      searchInput?.select();
      return;
    }
    if (isEditable(e.target)) return;
    if (mod && !e.altKey && key === 'a') {
      e.preventDefault();
      store.selectAll();
      table?.focus();
      return;
    }
    if (e.key === 'Delete' && !mod) {
      e.preventDefault();
      requestRemove(selectedIds, e.shiftKey);
      return;
    }
    const onControl = e.target instanceof Element && e.target.closest('button, a, summary, [role="menuitem"]');
    if (e.key === ' ' && !mod && !onControl) {
      e.preventDefault();
      togglePauseResume();
    }
  }

  function onWindowPaste(e: ClipboardEvent) {
    if (dialog || menu || palette || isEditable(e.target)) return;
    const text = e.clipboardData?.getData('text/plain') ?? '';
    const urls = splitUrls(text).filter((u) => /^https?:\/\//i.test(u));
    if (urls.length > 0) {
      e.preventDefault();
      openAdd(urls.join('\n'));
    }
  }

  /**
   * The desktop webview opens whatever is dropped on the page and not handled there (a
   * file, a link) in place of the app. Drops are handled or swallowed here, except text
   * dropped into an editable field.
   */
  function leaveToField(e: DragEvent): boolean {
    return isEditable(e.target) && !(e.dataTransfer?.types ?? []).includes('Files');
  }

  function onDragOver(e: DragEvent) {
    // A drop target of the page (the queue editor's order) took it.
    if (e.defaultPrevented) return;
    const types = e.dataTransfer?.types ?? [];
    if (!dialog && (types.includes('text/uri-list') || types.includes('text/plain'))) {
      e.preventDefault();
      if (e.dataTransfer) e.dataTransfer.dropEffect = 'copy';
    } else if (!leaveToField(e)) {
      e.preventDefault();
      if (e.dataTransfer) e.dataTransfer.dropEffect = 'none';
    }
  }

  function onDrop(e: DragEvent) {
    if (e.defaultPrevented) return;
    if (!dialog) {
      const text = e.dataTransfer?.getData('text/uri-list') || e.dataTransfer?.getData('text/plain') || '';
      const urls = splitUrls(text.replace(/^#.*$/gm, '')).filter((u) => /^https?:\/\//i.test(u));
      if (urls.length > 0) {
        e.preventDefault();
        openAdd(urls.join('\n'));
        return;
      }
    }
    if (!leaveToField(e)) e.preventDefault();
  }
</script>

<svelte:window
  onkeydown={onWindowKeydown}
  onpaste={onWindowPaste}
  ondragover={onDragOver}
  ondrop={onDrop}
  onfocus={onWindowFocus}
/>

<!-- The window (DESIGN-SYSTEM.md §2.3, §4): the title bar, the sidebar beside the work card
     (page header, list, details), and the status bar, all on the chrome. -->
<div class="window">
  {#if countdown}
    {@const pending = countdown}
    {#key `${pending.queueId}:${pending.deadline}`}
      <!-- First in the page, as on screen (outside the inert part). It takes the keyboard also
           from an open dialog, whose focus trap lets it keep it: the action is about to happen. -->
      <PostActionBanner
        {pending}
        queueName={queues.nameOf(pending.queueId)}
        oncancel={() => cancelPostAction(pending.queueId)}
        focus={menu === null}
        fallbackFocus={gridElement}
      />
    {/key}
  {/if}

  <!-- Outside the inert part: on a frameless window the bar is the window's caption, so its drag
       region and window controls keep working above an open dialog (its search and buttons are
       inert then, as the page is). -->
  <TitleBar
    customTitleBar={capabilities.customTitleBar === true}
    controls={api.window}
    modal={dialog !== null || palette}
    search={store.search}
    onsearch={(v) => store.setSearch(v)}
    oncommands={openPalette}
    onsettings={() => openSettings()}
    bind:searchInput
  />

  <div class="app" inert={dialog !== null || palette}>
    <div class="body">
      <Sidebar
        index={store.index}
        category={store.category}
        status={store.status}
        counts={store.counts}
        oncategory={(c) => store.setCategory(c)}
        onstatus={(s) => store.setStatusFilter(s)}
        queues={queueChoices}
        queue={store.queue}
        queueCounts={store.queueCounts}
        onqueue={(q) => store.setQueueFilter(q)}
        onmanagequeues={() => openQueues()}
        browserDownloads={settingsLoaded ? (settings.confirmBrowserDownloads ? 'ask' : 'start') : null}
        onintegration={() => openSettings({ page: 'integration' })}
      />

      <main class="card" class:has-details={store.selected.length > 0}>
        <!-- always present, so the header, table and details keep their grid rows -->
        <div class="banner-slot">
          <ConnectionBanner
            daemon={store.daemon}
            loadError={store.loadError}
            checking={store.checking}
            nextCheckAt={store.nextCheckAt}
            onretry={() => void store.retryNow()}
          />
        </div>
        <Toolbar
          {canAdd}
          {canResume}
          {canPause}
          {canRemove}
          {canPauseAll}
          {canResumeAll}
          onadd={() => openAdd()}
          onaddvideo={() => openAdd('', 'stream')}
          onresume={() => void resume()}
          onpause={() => void pause()}
          onremove={() => requestRemove()}
          onpauseall={() => void pauseAll()}
          onresumeall={() => void resumeAll()}
          title={pageTitle}
          count={store.visible.length}
          {chips}
          status={store.status}
          onchip={onChip}
          more={{ density: density.choice, details: !detailsCollapsed, queues: queueChoices.length > 0 }}
          ondensity={(c) => density.set(c)}
          ontoggledetails={toggleDetails}
          onmanagequeues={() => openQueues()}
          onselectall={() => {
            store.selectAll();
            table?.focus();
          }}
        />
        <DownloadTable
          bind:this={table}
          {store}
          onopen={(d) => openDownload(d)}
          onmenu={(x, y) => (menu = { x, y })}
          onadd={() => openAdd()}
          onaddvideo={() => openAdd('', 'stream')}
          onextension={() => openSettings({ page: 'integration' })}
          onclearfilters={clearFilters}
          onaction={onRowAction}
        />
        <!-- The details pane is not there when nothing is selected (DESIGN-SYSTEM.md §3.15). -->
        {#if store.selected.length > 0}
          <DetailsPanel
            download={store.detail}
            selectedCount={store.selected.length}
            selected={store.selected}
            history={store.detail ? store.historyOf(store.detail.id) : []}
            defaultConnections={settings.defaultMaxConnections}
            {stale}
            collapsed={detailsCollapsed}
            queueName={store.detail && queueChoices.length > 0 ? escapeBidi(queues.nameOf(queueOf(store.detail))) : undefined}
            ontoggle={toggleDetails}
            onrestart={(d) => void requestRestart([d.id], needsRestart(d) ? 'validator_changed' : 'manual')}
            onresume={(d) => void resume([d.id])}
            onpause={(d) => void pause([d.id])}
            oncopy={(d) => void copyUrls([d.id])}
            onopenfolder={(d) => void openFolder(d)}
            onconfirm={(d) => requestConfirm(d.id, true)}
            onaddlogin={(d) => addSiteLogin(d)}
            onlimit={(d, bps) => void setDownloadLimit(d, bps)}
            onprogress={(d) => {
              if (stale) unavailable();
              else void openProgress(d.id);
            }}
            onmore={(x, y) => (menu = { x, y })}
            onselection={onSelectionAction}
          />
        {/if}
      </main>
    </div>

    <StatusBar
      bind:this={statusBar}
      daemon={store.daemon}
      demo={api.kind === 'mock'}
      totals={store.totals}
      shown={store.visible.length}
      selected={store.selected.length}
      history={store.totalHistory}
      {stale}
      limit={settingsLoaded ? settings.globalSpeedLimit : null}
      onlimit={(c) => void setGlobalLimit(c)}
      postAction={whenDone}
      postActionChoices={whenDoneChoices}
      onpostaction={(c) => void setWhenDone(c)}
      {freeBytes}
      driveLabel={formatDriveLabel(settings.defaultDir)}
      {needBytes}
    />
  </div>
</div>

{#if menu}
  <ContextMenu
    x={menu.x}
    y={menu.y}
    items={menuItems}
    label="Download actions"
    onselect={onMenuSelect}
    onclose={closeMenu}
  />
{/if}

{#if dialog?.kind === 'add'}
  <AddDialog
    defaults={{ dir: settings.defaultDir, maxConnections: settings.defaultMaxConnections }}
    initialUrls={dialog.urls}
    initialMode={dialog.mode}
    freeSpace={(dir) => api.freeSpace(dir)}
    pickFolder={(dir) => api.pickFolder(dir)}
    submit={(requests) => addDownloads(requests)}
    probeMedia={(req) => store.probeMedia(req)}
    submitMedia={(req) => addMediaDownload(req)}
    {tools}
    onopensettings={openVideoSettings}
    onclose={() => (dialog = null)}
    fallbackFocus={gridElement}
    queues={queueChoices}
    defaultQueue={store.queue}
  />
{:else if dialog?.kind === 'settings'}
  <SettingsDialog
    load={() => api.getSettings()}
    save={(s) => api.updateSettings(s)}
    pickFolder={(dir) => api.pickFolder(dir)}
    onsaved={(s) => {
      applySettings(s);
      toasts.success('Settings saved');
    }}
    onclose={() => (dialog = null)}
    fallbackFocus={gridElement}
    initialPage={dialog.page}
    focusField={dialog.focusField}
    addLogin={dialog.addLogin}
    setSecret={(kind, id, password) => api.setSecret(kind, id, password)}
    clearSecret={(kind, id) => api.clearSecret(kind, id)}
    checkForUpdate={() => api.checkForUpdate()}
    installUpdate={() => api.installUpdate()}
    pickFile={(opts) => api.pickFile(opts)}
    {capabilities}
    {tools}
    openExternal={(url) => api.openExternal(url)}
    about={{
      ...(build ? { build } : {}),
      status: () => api.status(),
      openAppFolder: (folder) => api.openAppFolder(folder),
      copyText: (text) => api.copyText(text),
      diagnostics: (opts) => api.diagnostics(opts),
      thirdPartyNotices: () => api.thirdPartyNotices(),
    }}
  />
{:else if dialog?.kind === 'queues'}
  <QueueDialog
    queues={queueChoices}
    initial={dialog.id}
    {capabilities}
    {stale}
    downloadOf={(id) => store.get(id)}
    oncreate={(spec) => queues.create(spec)}
    onupdate={(id, spec) => queues.update(id, spec)}
    ondelete={(id) => queues.remove(id)}
    onstart={(id) => queues.startQueue(id)}
    onstop={(id) => queues.stopQueue(id)}
    onmove={async (ids, queueId, position) => {
      const q = await queues.move(ids, queueId, position);
      store.assignQueue(ids, queueId);
      return q;
    }}
    onclose={() => (dialog = null)}
    fallbackFocus={gridElement}
  />
{:else if dialog?.kind === 'move'}
  {@const ids = dialog.ids}
  {@const inQueues = new Set(ids.map((id) => queueOf(store.get(id) ?? { queueId: 0 })))}
  <MoveToQueueDialog
    names={ids.map((id) => store.nameOf(id))}
    queues={queueChoices}
    current={inQueues.size === 1 ? [...inQueues][0] : null}
    onmove={(queueId) => moveToQueue(ids, queueId)}
    onclose={() => (dialog = null)}
    fallbackFocus={gridElement}
  />
{:else if dialog?.kind === 'remove'}
  {@const d = dialog}
  <RemoveDialog
    names={d.ids.map((id) => store.nameOf(id))}
    deleteFile={d.deleteFile}
    onconfirm={async (deleteFile) => report(await store.remove(d.ids, deleteFile), 'remove')}
    onclose={() => (dialog = null)}
    fallbackFocus={gridElement}
  />
{:else if dialog?.kind === 'restart'}
  {@const d = dialog}
  <RestartDialog
    names={d.ids.map((id) => store.nameOf(id))}
    reason={d.reason}
    again={d.again}
    onconfirm={(existingFile) => restartDownloads(d.ids, existingFile)}
    onclose={() => (dialog = null)}
    fallbackFocus={gridElement}
  />
{:else if dialog?.kind === 'open'}
  {@const id = dialog.id}
  {@const raw = rawName(store.get(id) ?? { filename: '', url: '', finalUrl: '' })}
  <OpenDialog
    name={store.nameOf(id)}
    executable={isExecutableName(raw)}
    hiddenControls={hasBidiControls(raw)}
    onconfirm={() => void openFile(id)}
    onclose={() => (dialog = null)}
    fallbackFocus={gridElement}
  />
{:else if dialog?.kind === 'confirm'}
  {@const id = dialog.id}
  {@const requested = dialog.requested}
  {@const d = store.get(id)}
  {#if d}
    {#key id}
      <ConfirmDialog
        download={d}
        {settings}
        {requested}
        waiting={store.awaiting.length}
        pickFolder={(dir) => api.pickFolder(dir)}
        onconfirm={(opts) => confirmDownload(id, opts)}
        onmove={async (queueId) => {
          await queues.move([id], queueId, -1);
          store.assignQueue([id], queueId);
        }}
        oncancel={() => cancelDownload(id)}
        onclose={() => closeConfirm(id)}
        fallbackFocus={gridElement}
        queues={queueChoices}
      />
    {/key}
  {/if}
{/if}

{#if palette}
  <CommandPalette
    {commands}
    downloads={paletteDownloads}
    onopendownload={openFromPalette}
    onshowfolder={folderFromPalette}
    onclose={() => (palette = false)}
    fallbackFocus={gridElement}
  />
{/if}

<Toasts {notices} behind={dialog !== null} />

{#if toolsToast}
  <div class="tools-toast" class:behind={dialog !== null} role="status" aria-labelledby="tools-toast-title">
    <div class="tools-head">
      <span class="tools-tone" aria-hidden="true"><Icon name="stream" size={15} /></span>
      <p class="tools-title" id="tools-toast-title">Setting up video downloads</p>
    </div>
    <ProgressBar fraction={tools.fraction} state="downloading" label="Downloading yt-dlp and Deno" />
    <p class="tools-detail" aria-hidden="true">{installText(tools.status, Number.NaN, false)}</p>
    <p class="tools-detail">Downex downloads yt-dlp and Deno from GitHub once; then it reads video pages.</p>
  </div>
{/if}

<style>
  /* The window (DESIGN-SYSTEM.md §2.3): the 44 px title bar, then the grid of the 14rem sidebar
     beside the work card and the 32 px status bar; html and body are on --chrome. */
  .window {
    display: flex;
    flex-direction: column;
    block-size: 100vh;
    block-size: 100dvh;
    background: var(--chrome);
  }
  .app {
    flex: 1 1 auto;
    min-block-size: 0;
    display: grid;
    grid-template-rows: minmax(0, 1fr) var(--statusbar-h);
  }
  .body {
    display: grid;
    grid-template-columns: var(--sidebar-w) minmax(0, 1fr);
    min-block-size: 0;
  }
  /* The work card: from the sidebar's edge to the window's, between the title and status bars,
     rounded on its inline-start corners. */
  .card {
    display: grid;
    grid-template-rows: auto auto minmax(0, 1fr) auto;
    min-block-size: 0;
    min-inline-size: 0;
    border: 1px solid var(--line);
    border-inline-end: 0;
    border-start-start-radius: var(--r-xl);
    border-end-start-radius: var(--r-xl);
    background: var(--surface);
    box-shadow: var(--shadow-sm);
    overflow: hidden;
    container-type: inline-size;
  }
  .banner-slot {
    position: relative;
    min-inline-size: 0;
  }
  /* bottom left, clear of the toasts (bottom right), 16 px above the status bar */
  .tools-toast {
    position: fixed;
    inset-block-end: calc(var(--statusbar-h) + 16px);
    inset-inline-start: calc(var(--sidebar-w) + 16px);
    z-index: 60;
    display: grid;
    gap: 8px;
    inline-size: min(360px, calc(100vw - 32px));
    padding: 12px 14px 14px;
    border: 1px solid var(--line);
    border-radius: 11px;
    background: var(--raised);
    box-shadow: var(--shadow-lg);
    animation: rise var(--dur-4) var(--ease-out);
  }
  /* under a modal dialog's backdrop (z-index 40), like the toasts: the Add dialog shows the
     same setup progress itself */
  .tools-toast.behind {
    z-index: 30;
  }
  .tools-head {
    display: flex;
    align-items: center;
    gap: 10px;
  }
  .tools-tone {
    display: grid;
    place-items: center;
    inline-size: 22px;
    block-size: 22px;
    border-radius: 50%;
    background: var(--accent-soft);
    color: var(--accent-text);
  }
  .tools-title {
    font-weight: 600;
  }
  .tools-detail {
    font-size: var(--type-small);
    color: var(--muted);
  }
  @keyframes rise {
    from {
      opacity: 0;
      translate: 0 8px;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .tools-toast {
      animation: none;
    }
  }
  @media (max-width: 720px) {
    .body {
      grid-template-columns: minmax(0, 1fr);
      grid-template-rows: auto minmax(0, 1fr);
    }
    .card {
      border-inline-start: 0;
      border-radius: 0;
    }
    .tools-toast {
      inset-inline-start: 16px;
    }
  }
  @media (max-height: 620px) {
    .app {
      --details-h: 11rem;
    }
  }
  @media (forced-colors: active) {
    .card {
      border-color: CanvasText;
    }
  }
</style>
