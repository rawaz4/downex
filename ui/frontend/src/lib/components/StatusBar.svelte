<script lang="ts" module>
  import type { PostAction } from '../api';

  /** 1 MB = 1,000,000 B: limits are shown in decimal units (lib/format.ts). */
  const MB = 1000 * 1000;

  /** The global speed limit's presets (bytes per second; 0 = unlimited), DESIGN-SYSTEM.md §3.14. */
  export const LIMIT_PRESETS: readonly number[] = [0, 20 * MB, 10 * MB, 5 * MB, 1 * MB];

  /** What the limit menu chose: a limit (bytes per second, 0 = unlimited), or "Custom…". */
  export type LimitChoice = number | 'custom';

  /** What the when-done menu chose: an action of the main queue, or "Other queues…". */
  export type WhenDoneChoice = PostAction | 'other';
</script>

<script lang="ts">
  // The status bar (DESIGN-SYSTEM.md §3.14): the connection, a mini chart of the total speed with
  // the speed, what is active and queued, then at the end the global speed limit menu, "When
  // done: keep running" (the main queue's post-queue action), the free space of the download
  // folder's drive (a warning when it is too small for what is left to download) and the count.
  import Icon from './Icon.svelte';
  import MenuButton from './MenuButton.svelte';
  import Sparkline from './Sparkline.svelte';
  import type { MenuItem } from './ContextMenu.svelte';
  import type { DaemonState, SpeedSample } from '../stores/downloads.svelte';
  import { formatBytes, formatCount, formatSpeed } from '../format';
  import { POST_ACTION_SHORT_LABELS, isPowerAction, whenDoneText } from '../queues';
  import { isolate } from '../text';
  import { ticking } from '../motion.svelte';

  interface Props {
    daemon: DaemonState | null;
    demo: boolean;
    totals: { speed: number; active: number; queued: number; count: number };
    shown: number;
    selected: number;
    /** The total speed over the last minute (DownloadsStore.totalHistory). */
    history?: readonly SpeedSample[];
    /** downexd is unreachable: the menus are unavailable. */
    stale?: boolean;
    /** Settings.globalSpeedLimit (bytes per second, 0 = unlimited); null: not known yet. */
    limit?: number | null;
    onlimit?: (choice: LimitChoice) => void;
    /** The main queue's post-queue action; null: downexd has no queues (the control is hidden). */
    postAction?: PostAction | null;
    /** The actions this system offers (postActionChoices). */
    postActionChoices?: readonly PostAction[];
    onpostaction?: (choice: WhenDoneChoice) => void;
    /** Bytes free on the download folder's drive; null: not known (the item is hidden). */
    freeBytes?: number | null;
    /** "D:", "Network folder", or the folder's name. */
    driveLabel?: string;
    /** What is left to download (active, queued and paused downloads of known size). */
    needBytes?: number;
  }

  let {
    daemon,
    demo,
    totals,
    shown,
    selected,
    history = [],
    stale = false,
    limit = null,
    onlimit,
    postAction = null,
    postActionChoices = ['none', 'quit_downex', 'sleep', 'shutdown'],
    onpostaction,
    freeBytes = null,
    driveLabel = '',
    needBytes = 0,
  }: Props = $props();

  const speed = ticking(() => totals.speed);

  const connection = $derived.by(() => {
    if (demo) return { tone: 'demo', text: 'Demo mode', title: 'Demo mode: a simulated downexd' };
    if (daemon === null) return { tone: 'pending', text: 'Connecting…', title: 'Connecting to downexd…' };
    if (!daemon.ok) return { tone: 'down', text: 'downexd unavailable', title: `downexd unavailable: ${isolate(daemon.error)}` };
    return { tone: 'ok', text: 'Connected', title: `Connected to downexd ${daemon.status.version}`.trim() };
  });

  // ---- global speed limit ----
  let limitMenu: MenuButton | undefined = $state();
  const limited = $derived(limit !== null && limit > 0);
  /** A preset reads "20 MB/s" (as in the details pane's menu); another limit keeps its decimals. */
  const limitLabel = (v: number): string => (LIMIT_PRESETS.includes(v) ? `${v / MB} MB/s` : formatSpeed(v));
  const limitText = $derived(limited ? `Limit ${limitLabel(limit ?? 0)}` : 'Unlimited');
  const limitItems = $derived.by<MenuItem[]>(() => {
    const items: MenuItem[] = LIMIT_PRESETS.map((v, i) => ({
      id: `limit-${v}`,
      label: v === 0 ? 'Unlimited' : limitLabel(v),
      kind: 'radio',
      checked: (limit ?? 0) === v,
      ...(i === 0 ? { heading: 'Global speed limit' } : {}),
    }));
    // A limit set elsewhere (Settings, the CLI) is shown as the checked one.
    if (limit !== null && limit > 0 && !LIMIT_PRESETS.includes(limit)) {
      items.push({ id: `limit-${limit}`, label: formatSpeed(limit), kind: 'radio', checked: true, note: 'custom' });
    }
    items.push({ id: 'custom', label: 'Custom…', icon: 'gauge', separated: true });
    return items;
  });

  function onlimitselect(id: string): void {
    if (id === 'custom') onlimit?.('custom');
    else onlimit?.(Number(id.slice('limit-'.length)));
  }

  // ---- when done ----
  let whenDoneMenu: MenuButton | undefined = $state();
  const whenDoneItems = $derived.by<MenuItem[]>(() => [
    ...postActionChoices.map<MenuItem>((a, i) => ({
      id: `post-${a}`,
      label: POST_ACTION_SHORT_LABELS[a],
      kind: 'radio',
      checked: postAction === a,
      ...(i === 0 ? { heading: 'When the main queue is done' } : {}),
    })),
    { id: 'other', label: 'Other queues…', icon: 'queue', separated: true },
  ]);

  function onwhendone(id: string): void {
    if (id === 'other') onpostaction?.('other');
    else onpostaction?.(id.slice('post-'.length) as PostAction);
  }

  /** Opens the limit menu (the palette's "Set global speed limit…"). */
  export function openLimitMenu(): void {
    limitMenu?.openMenu('checked');
  }

  /** Opens the when-done menu (the palette's "When done…"). */
  export function openWhenDoneMenu(): boolean {
    if (postAction === null || !whenDoneMenu) return false;
    whenDoneMenu.openMenu('checked');
    return true;
  }

  // ---- free space ----
  const short = $derived(freeBytes !== null && needBytes > freeBytes);
  const freeTitle = $derived(
    freeBytes === null
      ? ''
      : short
        ? `Not enough free space for the active and queued downloads (about ${formatBytes(needBytes - freeBytes)} more needed)`
        : `${formatBytes(freeBytes)} free on the drive of the download folder`,
  );
</script>

<footer class="statusbar">
  <span class="sb-item conn tone-{connection.tone}" title={connection.title}>
    <span class="dot" aria-hidden="true"></span>
    <span class="text">{connection.text}</span>
  </span>
  <span class="sep" aria-hidden="true"></span>
  <span class="sb-item speed" title="Total download speed">
    <Sparkline samples={history} variant="mini" label="Total speed over the last minute" />
    <Icon name="arrow-down" size={13} />
    <span class="sr-only">Total speed</span>
    <span class="speed-value tnum">{formatSpeed(speed.current)}</span>
  </span>
  <span class="sep counts-sep" aria-hidden="true"></span>
  <span class="sb-item counts">{formatCount(totals.active)} active · {formatCount(totals.queued)} queued</span>

  <span class="end">
    {#if onlimit}
      <MenuButton
        bind:this={limitMenu}
        class="sb-item sb-menu limit{limited ? ' warn' : ''}"
        label="Global speed limit"
        title={stale ? 'Unavailable while downexd is unreachable' : 'Global speed limit'}
        items={limitItems}
        onselect={onlimitselect}
        disabled={stale || limit === null}
        direction="up"
        align="end"
        footnote="Per-download and queue limits still apply."
      >
        <Icon name="gauge" size={14} />
        <span class="sr-only">Global speed limit:</span>
        <span class="tnum">{limitText}</span>
        <Icon name="chevron-down" size={12} />
      </MenuButton>
    {/if}
    {#if postAction !== null && onpostaction}
      <span class="sep" aria-hidden="true"></span>
      <MenuButton
        bind:this={whenDoneMenu}
        class="sb-item sb-menu whendone{isPowerAction(postAction) ? ' warn' : ''}"
        label="When the main queue is done"
        title={stale ? 'Unavailable while downexd is unreachable' : 'What happens when the main queue is done'}
        items={whenDoneItems}
        onselect={onwhendone}
        disabled={stale}
        direction="up"
        align="end"
      >
        <Icon name="power" size={14} />
        <span>{whenDoneText(postAction)}</span>
        <Icon name="chevron-down" size={12} />
      </MenuButton>
    {/if}
    {#if freeBytes !== null}
      <span class="sep" aria-hidden="true"></span>
      <span class="sb-item free" class:warn={short} title={freeTitle} data-short={short ? 'true' : undefined}>
        <Icon name={short ? 'alert' : 'drive'} size={14} />
        {#if driveLabel}<span class="drive" dir="auto">{driveLabel}</span>{/if}
        <span><b class="tnum">{formatBytes(freeBytes)}</b> free</span>
        {#if short}<span class="sr-only">. {freeTitle}</span>{/if}
      </span>
    {/if}
    <span class="sep" aria-hidden="true"></span>
    <span class="sb-item total">
      {formatCount(shown)}{shown !== totals.count ? ` of ${formatCount(totals.count)}` : ''} items{selected > 0
        ? ` · ${formatCount(selected)} selected`
        : ''}
    </span>
  </span>
</footer>

<style>
  .statusbar {
    display: flex;
    align-items: center;
    gap: 4px;
    block-size: var(--statusbar-h);
    padding-inline: 10px;
    background: var(--chrome);
    font-size: var(--type-small);
    color: var(--muted);
    white-space: nowrap;
    overflow: hidden;
  }
  .statusbar > span {
    unicode-bidi: plaintext;
  }
  .statusbar :global(.sb-item) {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    block-size: 24px;
    padding-inline: 8px;
    border-radius: var(--r-sm);
    min-inline-size: 0;
  }
  .sep {
    flex: none;
    inline-size: 1px;
    block-size: 14px;
    margin-inline: 2px;
    background: var(--line-strong);
  }
  .conn .text {
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .conn .dot {
    background: var(--faint);
  }
  .tone-ok .dot {
    background: var(--accent);
  }
  .tone-pending .dot,
  .tone-demo .dot {
    background: var(--warn);
  }
  .tone-down {
    color: var(--danger);
  }
  .tone-down .dot {
    background: var(--danger);
  }
  .speed {
    gap: 7px;
  }
  .speed :global(.icon) {
    color: var(--accent);
  }
  .speed-value {
    font-weight: 600;
    color: var(--ink);
  }
  .end {
    display: flex;
    align-items: center;
    gap: 4px;
    margin-inline-start: auto;
    min-inline-size: 0;
  }
  /* the menus' triggers: quiet text buttons with a hover ground */
  .statusbar :global(.sb-menu) {
    border: 0;
    background: transparent;
    color: var(--muted);
    font: inherit;
    cursor: pointer;
    transition:
      background-color var(--dur-2) ease,
      color var(--dur-2) ease;
  }
  .statusbar :global(.sb-menu:hover:not([aria-disabled='true'])),
  .statusbar :global(.sb-menu[aria-expanded='true']) {
    background: var(--hover);
    color: var(--ink);
  }
  .statusbar :global(.sb-menu[aria-disabled='true']) {
    color: var(--faint);
    cursor: default;
  }
  .statusbar :global(.sb-menu .icon:last-child) {
    color: var(--faint);
  }
  .statusbar :global(.sb-menu.warn),
  .free.warn {
    color: var(--warn);
  }
  .statusbar :global(.sb-menu.warn:hover) {
    color: var(--warn);
  }
  .free b {
    font-weight: 600;
    color: var(--ink-2);
  }
  .free.warn b {
    color: var(--warn);
  }
  .drive {
    color: var(--ink-2);
  }
  @media (max-width: 1100px) {
    .statusbar .counts,
    .statusbar .counts-sep {
      display: none;
    }
  }
  @media (max-width: 960px) {
    .speed :global(svg.mini) {
      display: none;
    }
  }
</style>
