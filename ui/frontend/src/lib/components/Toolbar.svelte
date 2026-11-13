<script lang="ts" module>
  import type { DensityChoice } from '../stores/density.svelte';

  /** The page header's summary chips: how many downloads are downloading, queued or failed. */
  export interface HeaderChips {
    readonly downloading: number;
    readonly queued: number;
    readonly failed: number;
  }

  /** What a summary chip filters the list to. */
  export type ChipFilter = 'active' | 'queued' | 'failed';

  /** The More menu's choices that the window keeps (DESIGN-SYSTEM.md §4.3). */
  export interface MoreState {
    readonly density: DensityChoice;
    /** The details pane is shown (not collapsed to its header). */
    readonly details: boolean;
    /** Manage queues… is offered (downexd has queues). */
    readonly queues: boolean;
  }
</script>

<script lang="ts">
  // The page header in the work card (DESIGN-SYSTEM.md §4.3): the page title with its count and
  // the summary chips, then the selection's Resume, Pause and Remove with labels, the bulk pair
  // (icon only below 1366 px), the More menu (density, details pane, queues, select all) and the
  // Add split button. It never wraps: below 1180 px the selection labels hide, below 960 px the
  // chips do (their counts stay in the title's count and the sidebar).
  import Icon from './Icon.svelte';
  import MenuButton from './MenuButton.svelte';
  import SplitButton from './SplitButton.svelte';
  import type { MenuItem } from './ContextMenu.svelte';
  import { formatCount } from '../format';

  interface Props {
    /** False while downexd is unreachable. */
    canAdd?: boolean;
    canResume: boolean;
    canPause: boolean;
    canRemove: boolean;
    /** Something is active / paused (and downexd is reachable). */
    canPauseAll?: boolean;
    canResumeAll?: boolean;
    onadd: () => void;
    /** "Add video or stream…" of the Add button's menu. */
    onaddvideo?: () => void;
    onresume: () => void;
    onpause: () => void;
    onremove: () => void;
    onpauseall?: () => void;
    onresumeall?: () => void;
    /** "All downloads", or the name of the filter shown. */
    title?: string;
    /** How many downloads the list shows. */
    count?: number;
    chips?: HeaderChips;
    /** The status filter in effect ('any' or another: a chip is pressed when it is its own). */
    status?: string;
    onchip?: (filter: ChipFilter) => void;
    more?: MoreState;
    ondensity?: (choice: DensityChoice) => void;
    ontoggledetails?: () => void;
    onmanagequeues?: () => void;
    onselectall?: () => void;
  }

  let {
    canAdd = true,
    canResume,
    canPause,
    canRemove,
    canPauseAll = false,
    canResumeAll = false,
    onadd,
    onaddvideo,
    onresume,
    onpause,
    onremove,
    onpauseall = () => {},
    onresumeall = () => {},
    title = 'All downloads',
    count,
    chips = { downloading: 0, queued: 0, failed: 0 },
    status = 'any',
    onchip,
    more = { density: 'comfortable', details: true, queues: false },
    ondensity,
    ontoggledetails,
    onmanagequeues,
    onselectall,
  }: Props = $props();

  // Unavailable actions use aria-disabled rather than disabled: a button that becomes
  // unavailable while focused (Pause after pausing) keeps keyboard focus.
  function when(enabled: boolean, action: () => void): () => void {
    return () => {
      if (enabled) action();
    };
  }

  const addItems: MenuItem[] = [
    { id: 'files', label: 'Add downloads…', icon: 'add', shortcut: 'Ctrl+N' },
    { id: 'stream', label: 'Add video or stream…', icon: 'stream' },
  ];

  const moreItems = $derived<MenuItem[]>([
    { id: 'density-comfortable', label: 'Comfortable', kind: 'radio', checked: more.density === 'comfortable', heading: 'List density', note: '2 lines' },
    { id: 'density-compact', label: 'Compact', kind: 'radio', checked: more.density === 'compact', note: '1 line' },
    { id: 'details', label: 'Details pane', kind: 'checkbox', checked: more.details, separated: true },
    ...(more.queues && onmanagequeues ? [{ id: 'queues', label: 'Manage queues…', icon: 'queue' } satisfies MenuItem] : []),
    { id: 'select-all', label: 'Select all', shortcut: 'Ctrl+A', separated: !(more.queues && onmanagequeues) },
  ]);

  function onmore(id: string): void {
    switch (id) {
      case 'density-comfortable':
        ondensity?.('comfortable');
        break;
      case 'density-compact':
        ondensity?.('compact');
        break;
      case 'details':
        ontoggledetails?.();
        break;
      case 'queues':
        onmanagequeues?.();
        break;
      case 'select-all':
        onselectall?.();
        break;
    }
  }

  const CHIPS: { key: keyof HeaderChips; filter: ChipFilter; word: string; tone: string }[] = [
    { key: 'downloading', filter: 'active', word: 'downloading', tone: 'live' },
    { key: 'queued', filter: 'queued', word: 'queued', tone: '' },
    { key: 'failed', filter: 'failed', word: 'failed', tone: 'fail' },
  ];
</script>

<header class="toolbar">
  <div class="heading">
    <h1 class="title" dir="auto">{title}</h1>
    {#if count !== undefined}<span class="count tnum">{formatCount(count)}</span>{/if}
    <div class="chips">
      {#each CHIPS as c (c.key)}
        {#if chips[c.key] > 0}
          <button
            type="button"
            class="chip {c.tone}"
            aria-pressed={status === c.filter}
            title={status === c.filter ? 'Show every status' : `Show only the ${c.word} downloads`}
            onclick={() => onchip?.(c.filter)}
          >
            {#if c.tone === 'live'}<span class="dot" aria-hidden="true"></span>{/if}
            <span class="tnum">{formatCount(chips[c.key])}</span>
            {c.word}
          </button>
        {/if}
      {/each}
    </div>
  </div>

  <div class="actions">
    <button
      type="button"
      class="btn ghost sel"
      onclick={when(canResume, onresume)}
      aria-disabled={canResume ? undefined : 'true'}
      title="Resume (Space)"
    >
      <Icon name="resume" size={15} />
      <span class="lbl">Resume</span>
    </button>
    <button
      type="button"
      class="btn ghost sel"
      onclick={when(canPause, onpause)}
      aria-disabled={canPause ? undefined : 'true'}
      title="Pause (Space)"
    >
      <Icon name="pause" size={15} />
      <span class="lbl">Pause</span>
    </button>
    <button
      type="button"
      class="btn ghost sel"
      onclick={when(canRemove, onremove)}
      aria-disabled={canRemove ? undefined : 'true'}
      aria-keyshortcuts="Delete"
      title="Remove (Del)"
    >
      <Icon name="remove" size={15} />
      <span class="lbl">Remove</span>
    </button>
    <span class="vsep" aria-hidden="true"></span>
    <button
      type="button"
      class="btn ghost bulk"
      onclick={when(canResumeAll, onresumeall)}
      aria-disabled={canResumeAll ? undefined : 'true'}
      title="Resume all paused downloads"
    >
      <Icon name="resume-all" size={15} />
      <span class="lbl">Resume all</span>
    </button>
    <button
      type="button"
      class="btn ghost bulk"
      onclick={when(canPauseAll, onpauseall)}
      aria-disabled={canPauseAll ? undefined : 'true'}
      title="Pause all active downloads"
    >
      <Icon name="pause-all" size={15} />
      <span class="lbl">Pause all</span>
    </button>
    <MenuButton
      class="icon-btn more"
      ariaLabel="More actions"
      title="More actions"
      label="More actions"
      items={moreItems}
      onselect={onmore}
      align="end"
    >
      <Icon name="more" size={18} />
    </MenuButton>
    <SplitButton
      label="Add"
      icon="add"
      onclick={onadd}
      items={addItems}
      onselect={(id) => (id === 'stream' && onaddvideo ? onaddvideo() : onadd())}
      disabled={!canAdd}
      title={canAdd ? 'Add downloads (Ctrl+N)' : 'Unavailable while downexd is unreachable'}
      keyshortcuts="Control+N"
    />
  </div>
</header>

<style>
  .toolbar {
    display: flex;
    align-items: center;
    gap: var(--sp-6);
    block-size: var(--pagehead-h);
    flex: none;
    padding-inline: 20px 12px;
    min-inline-size: 0;
    overflow: hidden;
  }
  .heading {
    display: flex;
    align-items: baseline;
    gap: var(--sp-4);
    flex: 1 1 auto;
    min-inline-size: 0;
  }
  .title {
    margin: 0;
    font: 650 var(--type-page) / 1.2 var(--font-display);
    letter-spacing: -0.02em;
    color: var(--ink);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    min-inline-size: 0;
  }
  .count {
    flex: none;
    font-size: var(--type-body);
    font-weight: 500;
    color: var(--faint);
  }
  .chips {
    display: flex;
    align-items: center;
    gap: 6px;
    margin-inline-start: var(--sp-4);
    align-self: center;
    flex: none;
  }
  .chips .chip {
    gap: 5px;
  }
  .actions {
    display: flex;
    align-items: center;
    gap: 2px;
    flex: none;
  }
  .actions :global(.more) {
    margin-inline: 2px 8px;
  }
  .actions .vsep {
    margin-inline: 6px;
  }
  .btn.ghost {
    padding-inline: 9px;
  }
  /* Below 1366 px the bulk pair is icon-only (their names stay, from the hidden labels). */
  @media (max-width: 1365.98px) {
    .bulk .lbl {
      position: absolute;
      inline-size: 1px;
      block-size: 1px;
      overflow: hidden;
      clip-path: inset(50%);
      white-space: nowrap;
    }
    .bulk {
      inline-size: 30px;
      padding-inline: 0;
    }
  }
  @media (max-width: 1179.98px) {
    .sel .lbl {
      position: absolute;
      inline-size: 1px;
      block-size: 1px;
      overflow: hidden;
      clip-path: inset(50%);
      white-space: nowrap;
    }
    .sel {
      inline-size: 30px;
      padding-inline: 0;
    }
  }
  @media (max-width: 959.98px) {
    .chips {
      display: none;
    }
  }
</style>
