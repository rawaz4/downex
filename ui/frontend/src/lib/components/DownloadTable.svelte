<script lang="ts" module>
  import type { SortKey } from '../query';
  import { COLUMNS, ROW_PAD_END, ROW_PAD_START, fitColumns, rowHeightFor, type Column } from '../display';

  const MAX_WIDTH_REM = 40;
  const OVERSCAN = 10;
  const WIDTHS_KEY = 'downex.columnWidths';

  /** Saved column widths (px) for this viewer; a convenience, so failures are ignored. */
  function loadWidths(): Partial<Record<SortKey, number>> {
    try {
      const raw: unknown = JSON.parse(localStorage.getItem(WIDTHS_KEY) ?? '{}');
      const out: Partial<Record<SortKey, number>> = {};
      if (raw && typeof raw === 'object') {
        for (const c of COLUMNS) {
          const v = (raw as Record<string, unknown>)[c.key];
          if (c.width > 0 && typeof v === 'number' && Number.isFinite(v) && v > 0) out[c.key] = v;
        }
      }
      return out;
    } catch {
      return {};
    }
  }

  function saveWidths(widths: Partial<Record<SortKey, number>>): void {
    try {
      localStorage.setItem(WIDTHS_KEY, JSON.stringify(widths));
    } catch {
      // storage unavailable: widths last for this session
    }
  }

</script>

<script lang="ts">
  import { onMount, tick, untrack } from 'svelte';
  import type { Download } from '../api';
  import type { DownloadsStore } from '../stores/downloads.svelte';
  import type { Move } from '../selection';
  import { progressSummary } from '../display';
  import { OTHER_LABEL, STATUS_FILTER_LABELS } from '../categories';
  import { density } from '../stores/density.svelte';
  import { isolate } from '../text';
  import DownloadRow, { type RowAction } from './DownloadRow.svelte';
  import ListEmpty, { type EmptyKind } from './ListEmpty.svelte';
  import { Arrivals, motionOk } from '../motion.svelte';

  interface Props {
    store: DownloadsStore;
    /** Enter / double-click on a row. */
    onopen: (d: Download) => void;
    /** Right-click or the context-menu key; coordinates are viewport pixels. */
    onmenu: (x: number, y: number) => void;
    /** The empty list's "Add download". */
    onadd: () => void;
    /** The empty list's "Add video or stream" (shown when given). */
    onaddvideo?: () => void;
    /** The empty list's "Get the browser extension": Settings → Integration (shown when given). */
    onextension?: () => void;
    /** "Clear filters" when the filters match nothing (shown when given). */
    onclearfilters?: () => void;
    /** A row's hover or inline action (pause, resume, confirm, open folder, remove, restart). */
    onaction?: (action: RowAction, d: Download) => void;
  }

  let { store, onopen, onmenu, onadd, onaddvideo, onextension, onclearfilters, onaction }: Props = $props();

  const uid = $props.id();

  let viewport: HTMLDivElement | undefined = $state();
  let scrollTop = $state(0);
  let height = $state(0);
  let gridWidth = $state(0);
  /** px per rem; row and header heights follow the text size. */
  let rem = $state(16);
  let widths = $state.raw<Partial<Record<SortKey, number>>>(loadWidths());
  let hasFocus = $state(false);
  /** Keyboard position: the header row, and a column (null = the whole row). */
  let inHeader = $state(false);
  let col = $state<number | null>(null);
  let announcement = $state('');

  const compact = $derived(density.compact);
  const rowHeight = $derived(rowHeightFor(rem, compact));
  const headerHeight = $derived(Math.max(30, Math.round(rem * 1.875)));
  const PAD = ROW_PAD_START + ROW_PAD_END;

  function widthOf(c: Column): number {
    return widths[c.key] ?? c.width * rem;
  }

  const shown = $derived(fitColumns(gridWidth > 0 ? gridWidth - PAD : 0, rem, widthOf));
  const shownKeys = $derived(shown.map((c) => c.key));
  const template = $derived(
    shown.map((c) => (c.width === 0 ? `minmax(${c.min}rem, 1fr)` : `${widthOf(c)}px`)).join(' '),
  );
  /** Only when even the essential columns do not fit does the table scroll sideways. */
  const minInline = $derived(PAD + shown.reduce((sum, c) => sum + (c.width === 0 ? c.min * rem : widthOf(c)), 0));
  const colIndex = $derived(col === null ? null : Math.min(col, shown.length - 1));

  const total = $derived(store.visible.length);
  const bodyHeight = $derived(Math.max(0, height - headerHeight));
  const first = $derived(Math.max(0, Math.floor(scrollTop / rowHeight) - OVERSCAN));
  const last = $derived(Math.min(total, Math.ceil((scrollTop + bodyHeight) / rowHeight) + OVERSCAN));
  const rows = $derived(store.visible.slice(first, last));
  const focusIndex = $derived(store.selection.focus === null ? -1 : store.indexOf(store.selection.focus));
  const activeKey = $derived(colIndex === null ? null : (shown[colIndex]?.key ?? null));
  const activeDescendant = $derived.by(() => {
    if (inHeader) return activeKey ? `dl-col-${activeKey}` : undefined;
    if (focusIndex < first || focusIndex >= last) return undefined;
    const id = store.selection.focus;
    return activeKey ? `dl-cell-${id}-${activeKey}` : `dl-row-${id}`;
  });
  const filtered = $derived(
    store.search.trim() !== '' || store.category !== 'all' || store.status !== 'any' || store.queue !== null,
  );
  const emptyKind = $derived<EmptyKind>(
    !store.ready ? (store.loadError ? 'unreachable' : 'loading') : filtered ? 'filtered' : 'none',
  );

  /** What the filters that match nothing are: the search, else the one filter in use. */
  const filterTitle = $derived.by(() => {
    const query = store.search.trim();
    if (query !== '') return `No downloads match “${isolate(query)}”`;
    const names: string[] = [];
    if (store.category === 'other') names.push(OTHER_LABEL);
    else if (store.category !== 'all') {
      const key = store.category;
      const entry = store.index.entries.find((e) => e.key === key);
      names.push(entry ? isolate(entry.name) : 'this category');
    }
    if (store.status !== 'any') names.push(STATUS_FILTER_LABELS[store.status]);
    if (store.queue !== null) names.push('this queue');
    return names.length === 1 ? `Nothing in ${names[0]}` : 'No downloads match the current filters';
  });

  // Arrivals and departures: a new row plays its arrival; a removed one that was on
  // screen fades out where it was (both only while motion is allowed).
  const arrivals = new Arrivals();
  let ghosts = $state.raw<{ d: Download; index: number }[]>([]);
  let lastRendered = new Map<number, { d: Download; index: number }>();

  $effect(() => {
    const ids = store.all.map((d) => d.id);
    const { removed } = arrivals.update(ids, store.ready);
    if (removed.length === 0 || !motionOk()) return;
    untrack(() => {
      const leaving = removed.flatMap((id) => lastRendered.get(id) ?? []);
      if (leaving.length === 0) return;
      ghosts = [...ghosts, ...leaving];
      setTimeout(() => (ghosts = ghosts.filter((g) => !leaving.includes(g))), 240);
    });
  });

  $effect(() => {
    const m = new Map<number, { d: Download; index: number }>();
    rows.forEach((d, i) => m.set(d.id, { d, index: first + i }));
    lastRendered = m;
  });

  onMount(() => {
    const px = parseFloat(getComputedStyle(document.documentElement).fontSize);
    if (px > 0) rem = px;
  });

  // Keep the cursor row in view while the grid has focus, including when a re-sort
  // (by speed, progress or time left) moves it.
  $effect(() => {
    const i = focusIndex;
    if (hasFocus && i >= 0) untrack(() => ensureVisible(i));
  });

  export function focus(): void {
    viewport?.focus({ preventScroll: true });
  }

  export function element(): HTMLElement | undefined {
    return viewport;
  }

  function ensureVisible(index: number): void {
    if (!viewport || index < 0) return;
    const top = index * rowHeight;
    if (top < viewport.scrollTop) viewport.scrollTop = top;
    else if (top + rowHeight > viewport.scrollTop + bodyHeight) {
      viewport.scrollTop = top + rowHeight - bodyHeight;
    }
    scrollTop = viewport.scrollTop;
  }

  /** The first row at least partly inside the viewport. */
  function firstRowInView(): number {
    const top = viewport?.scrollTop ?? 0;
    return Math.min(total - 1, Math.max(0, Math.floor(top / rowHeight)));
  }

  function rowIdFrom(target: EventTarget | null): number | null {
    if (!(target instanceof Element)) return null;
    const row = target.closest<HTMLElement>('[data-id]');
    return row ? Number(row.dataset.id) : null;
  }

  /** A click on a row's hover action is that action's, not the row's. */
  function onRowAction(e: Event): boolean {
    return e.target instanceof Element && e.target.closest('[data-row-action]') !== null;
  }

  function onclick(e: MouseEvent) {
    if (onRowAction(e)) return;
    const id = rowIdFrom(e.target);
    if (id === null) return;
    inHeader = false;
    store.click(id, { shift: e.shiftKey, toggle: e.ctrlKey || e.metaKey });
  }

  function ondblclick(e: MouseEvent) {
    if (onRowAction(e)) return;
    const id = rowIdFrom(e.target);
    const d = id === null ? undefined : store.get(id);
    if (d) onopen(d);
  }

  function oncontextmenu(e: MouseEvent) {
    const id = rowIdFrom(e.target);
    e.preventDefault();
    if (id === null) return;
    inHeader = false;
    store.contextClick(id);
    focus();
    onmenu(e.clientX, e.clientY);
  }

  function onfocus(e: FocusEvent) {
    if (e.target !== viewport) return;
    hasFocus = true;
    if (total === 0) return;
    if (focusIndex < 0) {
      // Start where the user is looking (the list may have been scrolled with the mouse).
      const id = store.visibleIds[firstRowInView()];
      if (id !== undefined) store.setCursor(id);
    } else {
      ensureVisible(focusIndex);
    }
  }

  function onblur(e: FocusEvent) {
    if (e.target === viewport) hasFocus = false;
  }

  async function announce(text: string): Promise<void> {
    announcement = '';
    await tick();
    announcement = text;
  }

  function sortByColumn(i: number): void {
    const c = shown[i];
    if (c) store.sortBy(c.key);
  }

  function resizeBy(c: Column, delta: number): void {
    setWidth(c, widthOf(c) + delta);
    saveWidths(widths);
  }

  function setWidth(c: Column, px: number): void {
    const w = Math.round(Math.min(MAX_WIDTH_REM * rem, Math.max(c.min * rem, px)));
    widths = { ...widths, [c.key]: w };
  }

  function resetWidth(c: Column): void {
    const next = { ...widths };
    delete next[c.key];
    widths = next;
    saveWidths(widths);
  }

  function startResize(e: PointerEvent, c: Column): void {
    if (e.button !== 0 || !viewport) return;
    e.preventDefault();
    e.stopPropagation();
    const handle = e.currentTarget as HTMLElement;
    handle.setPointerCapture?.(e.pointerId);
    const startX = e.clientX;
    const start = widthOf(c);
    const rtl = getComputedStyle(viewport).direction === 'rtl';
    const move = (ev: PointerEvent) => setWidth(c, start + (rtl ? startX - ev.clientX : ev.clientX - startX));
    const end = () => {
      handle.removeEventListener('pointermove', move);
      handle.removeEventListener('pointerup', end);
      handle.removeEventListener('pointercancel', end);
      saveWidths(widths);
    };
    handle.addEventListener('pointermove', move);
    handle.addEventListener('pointerup', end);
    handle.addEventListener('pointercancel', end);
  }

  function onkeydown(e: KeyboardEvent) {
    if (e.target !== viewport) return;
    const shift = e.shiftKey;
    const toggle = e.ctrlKey || e.metaKey;
    const rtl = getComputedStyle(viewport).direction === 'rtl';
    const page = Math.max(1, Math.floor(bodyHeight / rowHeight) - 1);
    const lastCol = shown.length - 1;

    if (e.key === 'ArrowLeft' || e.key === 'ArrowRight') {
      e.preventDefault();
      const forward = (e.key === 'ArrowRight') !== rtl;
      if (inHeader) {
        const current = colIndex ?? 0;
        const c = shown[current];
        if (toggle && c && c.width > 0) {
          resizeBy(c, forward ? rem : -rem);
          return;
        }
        col = Math.min(lastCol, Math.max(0, current + (forward ? 1 : -1)));
        return;
      }
      if (focusIndex < 0) {
        const id = store.visibleIds[firstRowInView()];
        if (id === undefined) return;
        store.setCursor(id);
      }
      if (forward) col = colIndex === null ? 0 : Math.min(lastCol, colIndex + 1);
      else col = colIndex === null || colIndex === 0 ? null : colIndex - 1;
      return;
    }

    if (inHeader) {
      switch (e.key) {
        case 'ArrowUp':
          e.preventDefault();
          return;
        case 'ArrowDown':
          e.preventDefault();
          inHeader = false;
          if (focusIndex < 0 && total > 0) {
            const id = store.visibleIds[firstRowInView()];
            if (id !== undefined) store.setCursor(id);
          } else ensureVisible(focusIndex);
          return;
        case 'Enter':
        case ' ':
          e.preventDefault();
          sortByColumn(colIndex ?? 0);
          return;
        case 'Escape':
          e.preventDefault();
          inHeader = false;
          return;
      }
      if (e.key === 'Home' || e.key === 'End' || e.key === 'PageUp' || e.key === 'PageDown') inHeader = false;
      else return;
    }

    if (e.key === 'ArrowUp' && !shift && !toggle && (focusIndex === 0 || total === 0)) {
      e.preventDefault();
      inHeader = true;
      col = colIndex ?? 0;
      return;
    }

    let move: Move | null = null;
    switch (e.key) {
      case 'ArrowDown':
        move = 1;
        break;
      case 'ArrowUp':
        move = -1;
        break;
      case 'PageDown':
        move = page;
        break;
      case 'PageUp':
        move = -page;
        break;
      case 'Home':
        move = 'first';
        break;
      case 'End':
        move = 'last';
        break;
    }
    if (move !== null) {
      e.preventDefault();
      store.move(move, { shift, toggle });
      ensureVisible(focusIndex);
      return;
    }
    if (e.key === ' ' && toggle) {
      e.preventDefault();
      store.toggleFocused();
      return;
    }
    if (e.key === 'Enter') {
      e.preventDefault();
      const d = store.detail;
      if (d) onopen(d);
      return;
    }
    if ((e.key === 'i' || e.key === 'I') && !toggle && !e.altKey) {
      const id = store.selection.focus;
      const d = id === null ? undefined : store.get(id);
      if (d) {
        e.preventDefault();
        void announce(progressSummary(d, store.stale));
      }
      return;
    }
    if (e.key === 'Escape') {
      if (col !== null) {
        e.preventDefault();
        col = null;
        return;
      }
      if (store.selection.ids.size > 0) {
        e.preventDefault();
        store.clearSelection();
      }
      return;
    }
    if (e.key === 'ContextMenu' || (e.key === 'F10' && shift)) {
      e.preventDefault();
      const id = store.selection.focus;
      if (id === null) return;
      if (!store.selection.ids.has(id)) store.selectOnly(id);
      ensureVisible(focusIndex);
      const row = document.getElementById(`dl-row-${id}`);
      const rect = (row ?? viewport!).getBoundingClientRect();
      onmenu(rtl ? rect.right - 32 : rect.left + 32, rect.bottom - 4);
    }
  }

  function ariaSort(key: SortKey): 'ascending' | 'descending' | undefined {
    if (store.sort.key !== key) return undefined;
    return store.sort.dir === 'asc' ? 'ascending' : 'descending';
  }
</script>

<div class="wrap" style:--header-h="{headerHeight}px" style:--row-pad-start="{ROW_PAD_START}px" style:--row-pad-end="{ROW_PAD_END}px">
  <div
    bind:this={viewport}
    bind:clientHeight={height}
    bind:clientWidth={gridWidth}
    class="grid"
    class:stale={store.stale}
    role="grid"
    tabindex="0"
    aria-label={store.stale ? 'Downloads, last known state' : 'Downloads'}
    aria-describedby="{uid}-keys"
    aria-rowcount={total + 1}
    aria-colcount={shown.length}
    aria-multiselectable="true"
    aria-activedescendant={activeDescendant}
    aria-keyshortcuts="Control+A Delete Space Enter I"
    style:--row-h="{rowHeight}px"
    style:--cols={template}
    style:--min-inline="{minInline}px"
    onscroll={() => (scrollTop = viewport?.scrollTop ?? 0)}
    {onclick}
    {ondblclick}
    {oncontextmenu}
    {onkeydown}
    {onfocus}
    {onblur}
  >
    <div class="header" class:active={inHeader} role="row" aria-rowindex={1}>
      {#each shown as c, i (c.key)}
        <div
          class="th"
          class:end={c.end}
          class:current={inHeader && colIndex === i}
          role="columnheader"
          id="dl-col-{c.key}"
          aria-colindex={i + 1}
          aria-sort={ariaSort(c.key)}
        >
          <button
            type="button"
            tabindex="-1"
            aria-label={c.title}
            title={`Sort by ${(c.title ?? c.label).toLowerCase()}`}
            onclick={() => {
              store.sortBy(c.key);
              focus();
            }}
          >
            <span>{c.label}</span>
            <svg
              class="arrow"
              class:sorted={store.sort.key === c.key}
              class:desc={store.sort.key === c.key ? store.sort.dir === 'desc' : c.end === true}
              viewBox="0 0 10 10"
              aria-hidden="true"
            >
              <path d="M5 8.5v-7M2.2 4.3 5 1.5l2.8 2.8" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" />
            </svg>
          </button>
          {#if c.width > 0}
            <span
              class="resizer"
              aria-hidden="true"
              title="Drag to resize; double-click to reset"
              onpointerdown={(e) => startResize(e, c)}
              ondblclick={(e) => {
                e.stopPropagation();
                resetWidth(c);
              }}
            ></span>
          {/if}
        </div>
      {/each}
    </div>
    <div class="body" role="rowgroup" style:block-size="{total * rowHeight}px">
      {#each rows as d, i (d.id)}
        <DownloadRow
          {d}
          index={first + i}
          {rowHeight}
          {compact}
          columns={shownKeys}
          selected={store.selection.ids.has(d.id)}
          cursor={store.selection.focus === d.id}
          activeColumn={inHeader ? null : activeKey}
          stale={store.stale}
          fresh={arrivals.fresh.has(d.id)}
          {onaction}
        />
      {/each}
      {#each ghosts as g (g)}
        <DownloadRow
          d={g.d}
          index={g.index}
          {rowHeight}
          {compact}
          columns={shownKeys}
          selected={false}
          cursor={false}
          ghost
        />
      {/each}
    </div>
  </div>

  <p id="{uid}-keys" hidden>
    Up and Down move between downloads, Left and Right between columns. Up from the first row
    reaches the column headers, where Enter sorts. I reads the progress of the current download.
  </p>
  <div class="sr-only" role="status">{announcement}</div>

  {#if total === 0}
    <div class="empty">
      <ListEmpty
        kind={emptyKind}
        title={filterTitle}
        stale={store.stale}
        {onadd}
        {onaddvideo}
        {onextension}
        {onclearfilters}
      />
    </div>
  {/if}
</div>

<style>
  .wrap {
    position: relative;
    min-block-size: 0;
    min-inline-size: 0;
    display: grid;
  }
  .grid {
    position: relative;
    overflow: auto;
    background: var(--surface);
    outline-offset: -2px;
    overscroll-behavior: contain;
  }
  /* Stale (daemon unreachable): drained of colour; text keeps its contrast. */
  .grid.stale .body {
    filter: grayscale(1);
  }
  .grid.stale .body :global(.segbar) {
    opacity: 0.55;
  }
  .header {
    position: sticky;
    inset-block-start: 0;
    z-index: 2;
    display: grid;
    grid-template-columns: var(--cols);
    block-size: var(--header-h);
    box-sizing: border-box;
    min-inline-size: var(--min-inline);
    padding-inline: var(--row-pad-start) var(--row-pad-end);
    background: var(--surface-2);
    border-block: 1px solid var(--line);
  }
  .th {
    position: relative;
    min-inline-size: 0;
    display: flex;
  }
  .grid:focus-visible .th.current {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
    border-radius: var(--r-xs);
  }
  .th button {
    display: flex;
    align-items: center;
    gap: 4px;
    inline-size: 100%;
    padding-inline: 6px;
    border: 0;
    background: transparent;
    font: 500 var(--type-meta) / 1 var(--font-ui);
    color: var(--faint);
    cursor: pointer;
    white-space: nowrap;
    overflow: hidden;
    transition: color var(--dur-2) ease;
  }
  .th button:hover {
    color: var(--ink);
  }
  .th[aria-sort] button {
    color: var(--ink-2);
    font-weight: 600;
  }
  /* numeric columns: the arrow leads, so the label ends flush with the right-aligned figures */
  .th.end button {
    flex-direction: row-reverse;
    justify-content: flex-start;
  }
  .resizer {
    position: absolute;
    inset-block: 0;
    inset-inline-end: -4px;
    z-index: 1;
    inline-size: 8px;
    cursor: col-resize;
    touch-action: none;
  }
  /* inside the table edge, so the last handle does not cause sideways scrolling */
  .th:last-child .resizer {
    inset-inline-end: 0;
  }
  .resizer::after {
    content: '';
    position: absolute;
    inset-block: 25%;
    inset-inline-start: 3px;
    inline-size: 2px;
    border-radius: 1px;
    background: var(--line-strong);
    opacity: 0;
    transition: opacity var(--dur-2);
  }
  .header:hover .resizer::after,
  .resizer:active::after {
    opacity: 1;
  }
  /* The arrow: shown on the sorted column, and faintly on hover where a click sorts. */
  .arrow {
    inline-size: 10px;
    block-size: 10px;
    flex: none;
    opacity: 0;
    transition:
      opacity var(--dur-2) ease,
      rotate var(--dur-3) var(--ease-out);
  }
  .th button:hover .arrow {
    opacity: 0.5;
  }
  .arrow.sorted,
  .th button:hover .arrow.sorted {
    opacity: 1;
    color: var(--ink-2);
  }
  .arrow.desc {
    rotate: 180deg;
  }
  .body {
    position: relative;
    min-inline-size: var(--min-inline);
  }
  .empty {
    position: absolute;
    inset: var(--header-h, 30px) 0 0 0;
    display: grid;
    align-content: center;
    overflow: auto;
    pointer-events: none;
  }
  .empty :global(button) {
    pointer-events: auto;
  }
</style>
