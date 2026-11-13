<script lang="ts" module>
  import type { SegmentSource } from '../segments';

  /** A hover or inline action on a row (the context menu and shortcuts offer the same). */
  export type RowAction = 'pause' | 'resume' | 'confirm' | 'folder' | 'remove' | 'restart';

  /** A probing download: an indeterminate bar (the hatch drifts while it connects). */
  const PROBING: SegmentSource = { size: -1, received: 1, state: 'downloading', segments: [], kind: 'file' };
</script>

<script lang="ts">
  import { untrack } from 'svelte';
  import type { Download } from '../api';
  import type { SortKey } from '../query';
  import Icon from './Icon.svelte';
  import FileIcon from './FileIcon.svelte';
  import FileName from './FileName.svelte';
  import SegmentBar from './SegmentBar.svelte';
  import StatusMark, { markOfState } from './StatusMark.svelte';
  import { motionOk, ticking } from '../motion.svelte';
  import { displayName, kindOf } from '../categories';
  import { inlineAction, mediaBadge, progressCell, rowDescription, rowSubline, sizeHint, statusText } from '../display';
  import { DASH, etaSeconds, formatBytes, formatDate, formatEta, formatPercent, formatSize, formatSpeed, progressFraction } from '../format';
  import { escapeBidi } from '../text';

  interface Props {
    d: Download;
    index: number;
    rowHeight: number;
    /** Keys of the columns shown, in order. */
    columns: readonly SortKey[];
    selected: boolean;
    cursor: boolean;
    /** Compact density: one line, a 16 px tile, the host in the tooltip. */
    compact?: boolean;
    /** The keyboard cell cursor when this is the cursor row (null: the whole row). */
    activeColumn?: SortKey | null;
    /** The daemon is unreachable: live values are not current. */
    stale?: boolean;
    /** It just arrived: play the arrival once. */
    fresh?: boolean;
    /** A row that was just removed, shown while it fades out (hidden from assistive technology). */
    ghost?: boolean;
    /** Hover and inline actions (mouse conveniences; the keyboard has the menu and shortcuts). */
    onaction?: (action: RowAction, d: Download) => void;
  }

  let {
    d,
    index,
    rowHeight,
    columns,
    selected,
    cursor,
    compact = false,
    activeColumn = null,
    stale = false,
    fresh = false,
    ghost = false,
    onaction,
  }: Props = $props();

  const cat = $derived(kindOf(d));
  const name = $derived(displayName(d));
  const sub = $derived(rowSubline(d));
  const live = $derived(d.state === 'downloading' && !stale);
  const status = $derived(statusText(d));
  /** "Queued #2": the position in --faint. */
  const queued = $derived(/^(Queued) (#\d+)$/.exec(status));
  const cell = $derived(progressCell(d));
  const pct = $derived(formatPercent(progressFraction(d)));
  /** Hover and inline actions are offered only where they can work. */
  const acting = $derived(onaction !== undefined && !stale && !ghost);
  const inline = $derived(acting ? inlineAction(d) : null);

  // Live numbers ease to each new value instead of jumping (not while motion is off).
  const speed = ticking(() => (live ? d.speedBps : Number.NaN));
  const eta = ticking(() => (live ? etaSeconds(d) : Number.NaN));

  // The check draws itself in when a download completes while its row is shown.
  let morph = $state(false);
  let lastState = untrack(() => d.state);
  $effect(() => {
    const state = d.state;
    if (state === 'completed' && lastState !== 'completed' && motionOk()) {
      morph = true;
      setTimeout(() => (morph = false), 900);
    }
    lastState = state;
  });

  type ActionIcon = 'pause' | 'resume' | 'confirm' | 'folder' | 'remove';
  const actions = $derived.by((): { id: RowAction; label: string; icon: ActionIcon }[] => {
    if (!acting) return [];
    const first: { id: RowAction; label: string; icon: ActionIcon } =
      d.state === 'completed'
        ? { id: 'folder', label: 'Open folder', icon: 'folder' }
        : d.state === 'awaiting_confirmation'
          ? { id: 'confirm', label: 'Confirm…', icon: 'confirm' }
          : d.state === 'paused' || d.state === 'failed' || d.state === 'canceled'
            ? { id: 'resume', label: 'Resume', icon: 'resume' }
            : { id: 'pause', label: 'Pause', icon: 'pause' };
    return [first, { id: 'remove', label: 'Remove…', icon: 'remove' }];
  });

  function act(e: MouseEvent, id: RowAction): void {
    e.stopPropagation();
    onaction?.(id, d);
  }
</script>

<!--
  The row's accessible name is only its name and status, which change rarely; size and
  date are its description. Speed, time left and progress change several times a second,
  so they are not part of either (the table's "I" key reads them on demand).
-->
<div
  class="row"
  class:compact
  class:selected
  class:cursor
  class:fresh
  class:ghost
  aria-hidden={ghost ? 'true' : undefined}
  role="row"
  id="dl-row-{d.id}"
  data-id={d.id}
  data-state={d.state}
  aria-rowindex={index + 2}
  aria-selected={selected}
  aria-labelledby="dl-name-{d.id} dl-status-{d.id}"
  aria-describedby="dl-desc-{d.id}"
  style:transform="translateY({index * rowHeight}px)"
>
  <span id="dl-desc-{d.id}" hidden>{rowDescription(d)}</span>
  {#each columns as key, i (key)}
    <div
      class="cell c-{key} {key === 'status' ? `state-${d.state}` : ''}"
      class:num={key !== 'name' && key !== 'progress' && key !== 'status'}
      class:active={cursor && activeColumn === key}
      role="gridcell"
      id="dl-cell-{d.id}-{key}"
      aria-colindex={i + 1}
      aria-selected={selected}
      title={key === 'status' ? (d.error ? escapeBidi(d.error) : status) : undefined}
    >
      {#if key === 'name'}
        <FileIcon kind={cat} size={compact ? 16 : 26} />
        <span class="names">
          <span class="line">
            <span class="text name" id="dl-name-{d.id}" dir="auto" title={compact && sub ? `${name}\n${sub}` : name}><FileName {name} /></span>
            {#if d.kind === 'media'}
              <!-- the row's description says it too ("HLS stream") -->
              <span class="tag media" aria-hidden="true" title="Streaming media">
                <Icon name="stream" size={11} />{mediaBadge(d)}
              </span>
            {/if}
          </span>
          {#if !compact && sub}
            <span class="sub" dir="auto">{sub}</span>
          {/if}
        </span>
      {:else if key === 'size'}
        {#if d.size < 0 && sizeHint(d) > 0}
          <bdi class="dim" title="The size the browser announced">~{formatBytes(sizeHint(d))}</bdi>
        {:else}
          <bdi class:dim={d.state === 'completed'}>{formatSize(d)}</bdi>
        {/if}
      {:else if key === 'progress'}
        {#if cell === 'bar'}
          <SegmentBar download={d} size={compact ? 'compact' : 'row'} />
          <span class="pct">{pct}</span>
        {:else if cell === 'probe'}
          <SegmentBar download={PROBING} size={compact ? 'compact' : 'row'} tone="fill" />
          <span class="pct"></span>
        {:else if cell === 'review' && acting}
          <button
            type="button"
            class="review"
            data-row-action
            aria-hidden="true"
            tabindex="-1"
            title="Review this download"
            onmousedown={(e) => e.preventDefault()}
            ondblclick={(e) => e.stopPropagation()}
            onclick={(e) => act(e, 'confirm')}
          >
            Review<Icon name="chevron-right" size={12} />
          </button>
        {/if}
      {:else if key === 'speed'}
        <bdi class:strong={live}>{live ? formatSpeed(speed.current) : ''}</bdi>
      {:else if key === 'eta'}
        <bdi>{live ? formatEta(eta.current) : ''}</bdi>
      {:else if key === 'connections'}
        {live ? d.connections : ''}
      {:else if key === 'status'}
        <StatusMark kind={markOfState(d.state)} size={compact ? 12 : 14} {morph} />
        <span class="text" id="dl-status-{d.id}"
          >{#if queued}{queued[1]}{' '}<span class="pos">{queued[2]}</span>{:else}{status}{/if}</span
        >
        {#if inline}
          <button
            type="button"
            class="inline-act"
            data-row-action
            aria-hidden="true"
            tabindex="-1"
            onmousedown={(e) => e.preventDefault()}
            ondblclick={(e) => e.stopPropagation()}
            onclick={(e) => act(e, inline.id)}
          >
            {inline.label}
          </button>
        {/if}
      {:else if key === 'added'}
        <bdi>{d.createdAt > 0 ? formatDate(d.createdAt) : DASH}</bdi>
      {/if}
    </div>
  {/each}
  {#if actions.length > 0}
    <!-- Mouse conveniences: hidden from assistive technology, out of the Tab order. -->
    <div class="acts" data-row-action aria-hidden="true">
      {#each actions as a (a.id)}
        <button
          type="button"
          class="act"
          class:danger={a.id === 'remove'}
          tabindex="-1"
          title={a.label}
          onmousedown={(e) => e.preventDefault()}
          ondblclick={(e) => e.stopPropagation()}
          onclick={(e) => act(e, a.id)}
        >
          <Icon name={a.icon} size={15} />
        </button>
      {/each}
    </div>
  {/if}
</div>

<style>
  .row {
    position: absolute;
    inset-block-start: 0;
    inset-inline: 0;
    display: grid;
    grid-template-columns: var(--cols);
    align-items: center;
    block-size: var(--row-h);
    padding-inline: var(--row-pad-start) var(--row-pad-end);
    box-sizing: border-box;
    border-block-end: 1px solid var(--line-soft);
    cursor: default;
    user-select: none;
    contain: layout style;
    --row-bg: transparent;
    background: var(--row-bg);
    /* re-sorting, inserts and removals slide rows to their new places */
    transition:
      transform var(--dur-5) var(--ease-out),
      background-color var(--dur-2) ease;
  }
  .row:hover {
    --row-bg: var(--hover);
    /* the hover pill may reach over the next row in Compact */
    z-index: 1;
  }
  /* Selection is never shown by tint alone: a 1 px ring and a 3 px Mint bar at the start. */
  .row.selected {
    --row-bg: var(--sel);
    border-block-end-color: transparent;
    box-shadow: inset 0 0 0 1px var(--sel-line);
  }
  .row.selected:hover {
    --row-bg: var(--sel-hover);
  }
  /* --faint is under 4.5:1 on the hovered selection: the small print steps up to --muted */
  .row.selected:hover .sub,
  .row.selected:hover .c-added,
  .row.selected:hover .pos,
  .row.selected:hover .state-completed,
  .row.selected:hover .state-canceled {
    color: var(--muted);
  }
  .row.selected::before {
    content: '';
    position: absolute;
    inset-block: 7px;
    inset-inline-start: 0;
    inline-size: 3px;
    border-start-end-radius: 3px;
    border-end-end-radius: 3px;
    background: var(--accent);
  }
  .row.compact.selected::before {
    inset-block: 4px;
  }
  .row.fresh {
    animation: arrive 900ms var(--ease-out);
  }
  .row.ghost {
    pointer-events: none;
    animation: leave 220ms ease-in forwards;
  }
  @keyframes arrive {
    0% {
      opacity: 0;
      translate: 0 -6px;
      background-color: var(--accent-soft);
    }
    35% {
      opacity: 1;
      translate: 0 0;
      background-color: var(--accent-soft);
    }
  }
  @keyframes leave {
    to {
      opacity: 0;
      translate: 0 3px;
    }
  }
  /* The keyboard cursor: a 2 px inset outline on the row, -3 px on the active cell. */
  :global(.grid:focus-visible) .row.cursor {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
  }
  :global(.grid:focus-visible) .row.cursor .cell.active {
    outline: 2px solid var(--focus);
    outline-offset: -3px;
    border-radius: var(--r-xs);
  }
  .cell {
    min-inline-size: 0;
    padding-inline: 6px;
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
    color: var(--ink-2);
  }
  .num {
    text-align: end;
    font-size: var(--type-body);
  }
  .strong {
    color: var(--ink);
    font-weight: 600;
  }
  .dim,
  .c-added {
    color: var(--faint);
  }
  .c-name {
    display: flex;
    align-items: center;
    gap: 11px;
  }
  .compact .c-name {
    gap: 8px;
  }
  .names {
    display: flex;
    flex-direction: column;
    gap: 3px;
    min-inline-size: 0;
  }
  .line {
    display: flex;
    align-items: center;
    gap: 7px;
    min-inline-size: 0;
  }
  .text {
    min-inline-size: 0;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  /* Glyphs reach past a 1.15 line box (Kurdish and Arabic marks, y, g, p), and overflow:
     hidden clips at the padding box. The padding gives the clip room; the negative margin
     takes it back, so the row's layout does not move. */
  .text,
  .sub {
    padding-block: 0.25em;
    margin-block: -0.25em;
  }
  .name {
    color: var(--ink);
    font-weight: 500;
    line-height: 1.15;
  }
  .compact .name {
    font-weight: 400;
  }
  .sub {
    overflow: hidden;
    text-overflow: ellipsis;
    color: var(--faint);
    font-size: var(--type-meta);
    line-height: 1.15;
  }
  .media :global(.icon) {
    flex: none;
  }
  .c-progress {
    display: flex;
    align-items: center;
    gap: 10px;
  }
  .pct {
    flex: none;
    min-inline-size: 2.2rem;
    text-align: end;
    font-size: var(--type-cell);
    font-weight: 600;
    color: var(--ink);
  }
  .compact .pct {
    font-size: var(--type-small);
  }
  [data-state='paused'] .pct {
    color: var(--warn);
  }
  [data-state='failed'] .pct {
    color: var(--danger);
  }
  [data-state='queued'] .pct {
    color: var(--muted);
  }
  /* Review › for a download to confirm (a mouse convenience; Enter opens the same dialog). */
  .review {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    block-size: 24px;
    padding-inline: 9px 7px;
    border: 1px solid var(--info-line);
    border-radius: 7px;
    background: var(--info-soft);
    color: var(--info);
    font: 600 var(--type-small) / 1 var(--font-ui);
    cursor: pointer;
    transition: border-color var(--dur-2) ease;
  }
  .compact .review {
    block-size: 20px;
  }
  .review:hover {
    border-color: var(--info);
  }
  .c-status {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: var(--type-cell);
  }
  .compact .c-status {
    gap: 7px;
  }
  .pos {
    color: var(--faint);
  }
  .state-downloading,
  .state-probing {
    color: var(--accent-text);
  }
  .state-downloading .text {
    font-weight: 600;
  }
  .state-paused {
    color: var(--warn);
    font-weight: 600;
  }
  .state-failed {
    color: var(--danger);
    font-weight: 600;
  }
  .state-awaiting_confirmation {
    color: var(--info);
    font-weight: 600;
  }
  .state-completed,
  .state-canceled {
    color: var(--faint);
  }
  /* Retry / Restart… after "Failed" (B): a link-like mouse convenience. */
  .inline-act {
    flex: none;
    padding: 1px 4px;
    margin-inline-start: 2px;
    border: 0;
    border-radius: var(--r-xs);
    background: none;
    color: var(--accent-text);
    font: 600 var(--type-cell) / 1.2 var(--font-ui);
    cursor: pointer;
  }
  .inline-act:hover {
    text-decoration: underline;
    text-underline-offset: 2px;
  }
  /* Hover actions: a raised pill at the row's end, over Status and Added. */
  .acts {
    position: absolute;
    inset-block-start: 50%;
    inset-inline-end: 10px;
    display: flex;
    align-items: center;
    gap: 2px;
    padding: 2px;
    border: 1px solid var(--line);
    border-radius: var(--r-md);
    background: var(--raised);
    box-shadow: var(--shadow-md);
    translate: 0 -50%;
    opacity: 0;
    pointer-events: none;
    transition: opacity var(--dur-2) ease;
  }
  .compact .acts {
    padding: 0;
  }
  .row:hover .acts {
    opacity: 1;
    pointer-events: auto;
  }
  .act {
    display: grid;
    place-items: center;
    inline-size: 26px;
    block-size: 26px;
    padding: 0;
    border: 0;
    border-radius: 6px;
    background: transparent;
    color: var(--muted);
    cursor: pointer;
    transition:
      background-color var(--dur-2) ease,
      color var(--dur-2) ease;
  }
  .compact .act {
    inline-size: 22px;
    block-size: 22px;
  }
  .act:hover {
    background: var(--hover);
    color: var(--ink);
  }
  .act.danger:hover {
    color: var(--danger);
  }
  @media (forced-colors: active) {
    .row.selected {
      background: Highlight;
      color: HighlightText;
    }
    /* the cells set their own colours: on Highlight every text and mark is HighlightText */
    .row.selected :global(*) {
      color: HighlightText;
    }
    .review,
    .acts {
      border: 1px solid ButtonText;
    }
  }
</style>
