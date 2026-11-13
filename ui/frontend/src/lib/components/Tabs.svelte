<script lang="ts" module>
  import type { IconName } from './Icon.svelte';

  export interface TabOption<V extends string | number = string> {
    readonly value: V;
    readonly label: string;
    readonly icon?: IconName;
    /** A count after the label. */
    readonly count?: number;
    /** A Mint dot after the label: something is on in that view (a speed limit). Not read out. */
    readonly dot?: boolean;
    readonly disabled?: boolean;
    /** Tooltip. */
    readonly title?: string;
  }

  /** The id of the tab for `value` of a Tabs with this `idPrefix` (its panel's aria-labelledby). */
  export function tabId(idPrefix: string, value: string | number): string {
    return `${idPrefix}-tab-${String(value).replace(/[^A-Za-z0-9_-]/g, '_')}`;
  }
</script>

<script lang="ts" generics="V extends string | number">
  // Tabs (DESIGN-SYSTEM.md §3.4.1): switches between views of one place. A full-width row on a
  // 1 px --line hairline; the chosen tab's label has one 2 px --accent indicator under it, which
  // slides to the next choice. role="tablist" with role="tab" buttons, a roving tabindex, arrows
  // with automatic activation, Home and End; the consumer gives `panelId` (aria-controls) and
  // labels its panel with tabId(idPrefix, value). Too many tabs for the row scroll sideways,
  // with faded edges, and the chosen one is kept in view. Picking a value is SegmentedControl.
  import Icon from './Icon.svelte';

  interface Props {
    value: V;
    options: readonly TabOption<V>[];
    onchange?: (value: V) => void;
    /** The tablist's name. */
    ariaLabel: string;
    /** The panel the tabs control (aria-controls). */
    panelId?: string;
    /** The tabs' ids are tabId(idPrefix, value). */
    idPrefix?: string;
    /** md: 32 px tabs; sm: 28 px (the progress window). */
    size?: 'md' | 'sm';
    /**
     * The first label starts where the content beside it does: the row reaches into the
     * container's padding by a tab's padding, so every tab keeps its padding (hover and focus
     * marks stay symmetric); the hairline stays under the content. The container needs at least
     * that much inline padding (12 px; 10 px for sm).
     */
    flush?: boolean;
    disabled?: boolean;
  }

  let { value = $bindable(), options, onchange, ariaLabel, panelId, idPrefix, size = 'md', flush = false, disabled = false }: Props = $props();

  const uid = $props.id();
  const prefix = $derived(idPrefix ?? uid);

  let list: HTMLElement | undefined = $state();
  /** The indicator under the chosen label, once measured (jsdom: never; the tab marks itself). */
  let bar = $state<{ x: number; w: number } | null>(null);
  let animate = $state(false);
  /** The row is scrolled away from its start or its end (the edges fade there). */
  let fadeStart = $state(false);
  let fadeEnd = $state(false);

  function select(v: V): void {
    if (v === value) return;
    value = v;
    onchange?.(v);
  }

  function measure(): void {
    const el = list;
    if (!el) return;
    const label = el.querySelector<HTMLElement>('[aria-selected="true"] .label');
    const box = el.getBoundingClientRect();
    const r = label?.getBoundingClientRect();
    if (!r || !(r.width > 0)) {
      bar = null;
    } else {
      bar = { x: r.left - box.left + el.scrollLeft, w: r.width };
    }
    const max = el.scrollWidth - el.clientWidth;
    fadeStart = max > 1 && el.scrollLeft > 1;
    fadeEnd = max > 1 && el.scrollLeft < max - 1;
  }

  /** Keeps the chosen tab in view in a row that scrolls (never scrolls anything else). */
  function reveal(): void {
    const el = list;
    const tab = el?.querySelector<HTMLElement>('[aria-selected="true"]');
    if (!el || !tab || el.scrollWidth <= el.clientWidth) return;
    const start = tab.offsetLeft - 24;
    const end = tab.offsetLeft + tab.offsetWidth + 24;
    if (start < el.scrollLeft) el.scrollLeft = start;
    else if (end > el.scrollLeft + el.clientWidth) el.scrollLeft = end - el.clientWidth;
  }

  $effect(() => {
    void value;
    void options;
    reveal();
    measure();
    // the first placement does not slide in from nowhere
    const raf = typeof requestAnimationFrame === 'function' ? requestAnimationFrame(() => (animate = bar !== null)) : 0;
    return () => {
      if (raf) cancelAnimationFrame(raf);
    };
  });

  $effect(() => {
    void options;
    if (!list || typeof ResizeObserver === 'undefined') return;
    const ro = new ResizeObserver(() => measure());
    ro.observe(list);
    for (const label of list.querySelectorAll('.label')) ro.observe(label);
    return () => ro.disconnect();
  });

  function onKey(e: KeyboardEvent, index: number): void {
    const rtl = list ? getComputedStyle(list).direction === 'rtl' : false;
    const next = rtl ? 'ArrowLeft' : 'ArrowRight';
    const prev = rtl ? 'ArrowRight' : 'ArrowLeft';
    const enabledAt = (i: number) => !options[i]?.disabled;
    const n = options.length;
    let to = -1;
    if (e.key === next || e.key === 'ArrowDown') {
      for (let k = 1; k <= n && to < 0; k++) if (enabledAt((index + k) % n)) to = (index + k) % n;
    } else if (e.key === prev || e.key === 'ArrowUp') {
      for (let k = 1; k <= n && to < 0; k++) if (enabledAt((index - k + n) % n)) to = (index - k + n) % n;
    } else if (e.key === 'Home') {
      for (let i = 0; i < n && to < 0; i++) if (enabledAt(i)) to = i;
    } else if (e.key === 'End') {
      for (let i = n - 1; i >= 0 && to < 0; i--) if (enabledAt(i)) to = i;
    } else return;
    e.preventDefault();
    const target = options[to];
    if (!target) return;
    select(target.value);
    list?.ownerDocument.getElementById(tabId(prefix, target.value))?.focus();
  }
</script>

<div class="tabs size-{size}" class:flush class:measured={bar !== null} class:animate class:fade-start={fadeStart} class:fade-end={fadeEnd}>
  <div bind:this={list} class="row" role="tablist" aria-label={ariaLabel} onscroll={measure}>
    {#each options as o, i (o.value)}
      <button
        type="button"
        role="tab"
        id={tabId(prefix, o.value)}
        class="tab"
        class:on={o.value === value}
        title={o.title}
        aria-selected={o.value === value}
        aria-controls={panelId}
        tabindex={o.value === value ? 0 : -1}
        disabled={disabled || o.disabled}
        onclick={() => select(o.value)}
        onkeydown={(e) => onKey(e, i)}
      >
        {#if o.icon}<Icon name={o.icon} size={size === 'sm' ? 14 : 16} />{/if}
        <!-- the bold twin keeps the tab as wide as when it is chosen: nothing shifts -->
        <span class="label">{o.label}<span class="twin" aria-hidden="true">{o.label}</span></span>
        {#if o.count !== undefined}<span class="count">{o.count}</span>{/if}
        {#if o.dot}<span class="dot" aria-hidden="true"></span>{/if}
      </button>
    {/each}
    {#if bar}<span class="indicator" aria-hidden="true" style:inline-size="{bar.w}px" style:transform="translateX({bar.x}px)"></span>{/if}
  </div>
</div>

<style>
  .tabs {
    --tab-pad: var(--sp-6);
    position: relative;
    min-inline-size: 0;
  }
  .size-sm {
    --tab-pad: 10px;
  }
  .row {
    position: relative;
    display: flex;
    align-items: stretch;
    gap: 2px;
    min-inline-size: 0;
    overflow-x: auto;
    overflow-y: hidden;
    overscroll-behavior-inline: contain;
    scroll-snap-type: x proximity;
    scrollbar-width: none;
    /* the hairline the tabs stand on (inside the row, so it spans it and the indicator covers it) */
    box-shadow: inset 0 -1px 0 var(--line);
  }
  .row::-webkit-scrollbar {
    display: none;
  }
  /* flush: the row reaches into the padding; the hairline is the container's, under the content
     (painted before the row, so the indicator still covers it) */
  .flush .row {
    margin-inline-start: calc(-1 * var(--tab-pad));
    box-shadow: none;
  }
  .flush::before {
    content: '';
    position: absolute;
    inset-inline: 0;
    inset-block-end: 0;
    block-size: 1px;
    background: var(--line);
  }
  .fade-start .row {
    mask-image: linear-gradient(to right, transparent, #000 24px);
  }
  .fade-end .row {
    mask-image: linear-gradient(to left, transparent, #000 24px);
  }
  .fade-start.fade-end .row {
    mask-image: linear-gradient(to right, transparent, #000 24px, #000 calc(100% - 24px), transparent);
  }
  .tab {
    position: relative;
    display: inline-flex;
    flex: none;
    align-items: center;
    gap: 7px;
    block-size: 32px;
    padding-block: 0 1px;
    padding-inline: var(--tab-pad);
    border: 0;
    border-radius: var(--r-sm) var(--r-sm) 0 0;
    background: transparent;
    color: var(--muted);
    font-size: var(--type-body);
    font-weight: 500;
    white-space: nowrap;
    scroll-snap-align: start;
    cursor: pointer;
    transition:
      background-color var(--dur-2) ease,
      color var(--dur-2) ease;
  }
  .size-sm .tab {
    block-size: 28px;
  }
  .tab:hover:not(.on, :disabled) {
    background: var(--hover);
    color: var(--ink);
  }
  .tab.on {
    color: var(--ink);
    font-weight: 600;
  }
  .tab.on :global(.icon) {
    color: var(--accent);
  }
  .tab:disabled {
    color: var(--faint);
    cursor: default;
  }
  .tab:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
  }
  .label {
    position: relative;
    display: inline-grid;
  }
  .twin {
    block-size: 0;
    overflow: hidden;
    font-weight: 600;
    visibility: hidden;
  }
  /* until the indicator is measured (or without layout), the chosen label underlines itself */
  .tabs:not(.measured) .tab.on .label::after {
    content: '';
    position: absolute;
    inset-inline: 0;
    inset-block-end: -7.5px;
    block-size: 2px;
    border-radius: 2px 2px 0 0;
    background: var(--accent);
  }
  .size-sm:not(.measured) .tab.on .label::after {
    inset-block-end: -5.5px;
  }
  .indicator {
    position: absolute;
    inset-block-end: 0;
    inset-inline-start: 0;
    block-size: 2px;
    border-radius: 2px 2px 0 0;
    background: var(--accent);
    pointer-events: none;
  }
  .animate .indicator {
    transition:
      transform var(--dur-3) var(--ease-out),
      inline-size var(--dur-3) var(--ease-out);
  }
  .count {
    min-inline-size: 18px;
    padding-inline: 5px;
    border-radius: var(--r-pill);
    background: var(--surface-2);
    color: var(--muted);
    font-size: var(--type-meta);
    font-weight: 600;
    line-height: 17px;
    text-align: center;
  }
  .tab.on .count {
    background: var(--accent-soft);
    color: var(--accent-text);
  }
  .dot {
    inline-size: 6px;
    block-size: 6px;
    border-radius: 50%;
    background: var(--accent);
  }
  @media (prefers-reduced-motion: reduce) {
    .animate .indicator,
    .tab {
      transition: none;
    }
  }
  @media (forced-colors: active) {
    .tabs:not(.flush) .row {
      box-shadow: inset 0 -1px 0 CanvasText;
    }
    .flush::before {
      background: CanvasText;
    }
    .indicator,
    .tabs:not(.measured) .tab.on .label::after,
    .dot {
      forced-color-adjust: none;
      background: Highlight;
    }
    .tab.on {
      color: Highlight;
    }
  }
</style>
