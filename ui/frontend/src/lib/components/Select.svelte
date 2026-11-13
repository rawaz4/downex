<script lang="ts" module>
  import type { IconName } from './Icon.svelte';

  export interface SelectOption<V extends string | number = string> {
    readonly value: V;
    readonly label: string;
    /** Secondary text after the label, in --muted ("AAC 129 kbit/s"). */
    readonly detail?: string;
    /** A figure at the end (a size). */
    readonly meta?: string;
    readonly disabled?: boolean;
    readonly title?: string;
    /** Options with the same group are listed under its caps heading. */
    readonly group?: string;
  }

  export type SelectSize = 'md' | 'sm' | 'inline';

  /** How long typed letters make one search (type-ahead). */
  const TYPEAHEAD_MS = 500;
  const PAGE = 10;
</script>

<script lang="ts" generics="V extends string | number">
  // The custom select (DESIGN-SYSTEM.md §3.5) on the WAI-ARIA APG select-only combobox: a
  // button with role="combobox" (it takes a <label for>, and carries `value` and `data-value`)
  // and a listbox that is always in the DOM, hidden while closed and fixed-positioned so that
  // it escapes a dialog's overflow, flipping upward when there is no room below. The focus
  // stays on the trigger; aria-activedescendant names the active option.
  //
  // Closed: Enter, Space, Down or Alt+Down open on the chosen option (Up too; Home and End on
  // the first and last); typing chooses without opening. Open: Up/Down, Home/End, PageUp/
  // PageDown (10), type-ahead; Enter, Space or Alt+Up choose and close; Esc closes without a
  // change; Tab chooses the active option and moves on; a click outside closes.
  import { onDestroy, tick } from 'svelte';
  import Icon from './Icon.svelte';

  interface Props {
    value: V;
    options: readonly SelectOption<V>[];
    onchange?: (value: V) => void;
    /** The trigger's id (for a <label for>); generated otherwise. */
    id?: string;
    ariaLabel?: string;
    ariaLabelledby?: string;
    ariaDescribedby?: string;
    disabled?: boolean;
    invalid?: boolean;
    /** md 36 px (dialogs), sm 30 px (settings grid, popovers), inline 26 px borderless until hover. */
    size?: SelectSize;
    /** A leading icon in the trigger. */
    icon?: IconName;
    /** Shown when the value is none of the options. */
    placeholder?: string;
    /** The trigger's tooltip. */
    title?: string;
  }

  let {
    value = $bindable(),
    options,
    onchange,
    id,
    ariaLabel,
    ariaLabelledby,
    ariaDescribedby,
    disabled = false,
    invalid = false,
    size = 'md',
    icon,
    placeholder = '',
    title,
  }: Props = $props();

  const uid = $props.id();
  const triggerId = $derived(id ?? `${uid}-select`);
  const listId = `${uid}-listbox`;
  const optionId = (i: number) => `${uid}-option-${i}`;

  let trigger: HTMLButtonElement | undefined = $state();
  let listbox: HTMLDivElement | undefined = $state();
  let open = $state(false);
  let active = $state(-1);
  /** Where the list is, against the box it is placed in (`dx`: that box's left edge in the window). */
  let pos = $state<{ left: number; top: number | null; bottom: number | null; width: number; maxHeight: number; dx: number } | null>(null);
  /** The name of the listbox: the trigger's label, read when it opens. */
  let listName = $state('');

  let typed = '';
  let typedAt = 0;
  /** The active option was last moved by the keyboard (it then also gets the focus ring). */
  let viaKeys = $state(false);

  const selectedIndex = $derived(options.findIndex((o) => o.value === value));
  const selected = $derived(selectedIndex >= 0 ? options[selectedIndex] : undefined);

  /** Options in groups, in order of first appearance; each keeps its index in `options`. */
  const groups = $derived.by(() => {
    const out: { name: string; items: { option: SelectOption<V>; index: number }[] }[] = [];
    options.forEach((option, index) => {
      const name = option.group ?? '';
      let g = out.find((x) => x.name === name);
      if (!g) {
        g = { name, items: [] };
        out.push(g);
      }
      g.items.push({ option, index });
    });
    return out;
  });

  function enabled(i: number): boolean {
    const o = options[i];
    return o !== undefined && !o.disabled;
  }

  /** The nearest enabled option from `from` in `step`'s direction (`from` itself included). */
  function nearest(from: number, step: 1 | -1): number {
    for (let i = from; i >= 0 && i < options.length; i += step) if (enabled(i)) return i;
    return -1;
  }

  function first(): number {
    return nearest(0, 1);
  }

  function last(): number {
    return nearest(options.length - 1, -1);
  }

  function move(to: number, step: 1 | -1): void {
    const clamped = Math.max(0, Math.min(options.length - 1, to));
    const i = nearest(clamped, step);
    active = i >= 0 ? i : nearest(clamped, step === 1 ? -1 : 1);
    void reveal();
  }

  async function reveal(): Promise<void> {
    await tick();
    const el = active >= 0 ? listbox?.ownerDocument.getElementById(optionId(active)) : null;
    el?.scrollIntoView?.({ block: 'nearest' });
  }

  /**
   * The box the fixed list is placed against, in window coordinates: the window, or an ancestor
   * that holds fixed boxes (a filter or transform: a dialog's blurred backdrop starts under the
   * title bar). Read from a fixed box at 0 on every side, beside the list.
   */
  function fixedFrame(): { top: number; left: number; bottom: number } {
    const vh = window.innerHeight;
    const parent = listbox?.parentNode;
    if (!parent || !listbox) return { top: 0, left: 0, bottom: vh };
    const probe = document.createElement('div');
    probe.setAttribute('aria-hidden', 'true');
    Object.assign(probe.style, { position: 'fixed', top: '0', left: '0', right: '0', bottom: '0', visibility: 'hidden', pointerEvents: 'none' });
    parent.insertBefore(probe, listbox);
    const b = probe.getBoundingClientRect();
    probe.remove();
    // no layout (a test's DOM): the window
    return b.width > 0 || b.height > 0 ? { top: b.top, left: b.left, bottom: b.bottom } : { top: 0, left: 0, bottom: vh };
  }

  function place(): void {
    if (!trigger) return;
    const r = trigger.getBoundingClientRect();
    const vw = window.innerWidth;
    const vh = window.innerHeight;
    const margin = 8;
    const gap = 4;
    const below = vh - r.bottom - gap - margin;
    const above = r.top - gap - margin;
    const wanted = Math.min(320, Math.max(120, options.length * 30 + 12));
    const up = below < wanted && above > below;
    const maxHeight = Math.max(90, Math.min(320, up ? above : below));
    // window coordinates, made relative to the box the list is placed against
    const frame = fixedFrame();
    pos = {
      left: Math.max(margin, Math.min(r.left, vw - r.width - margin)) - frame.left,
      top: up ? null : r.bottom + gap - frame.top,
      bottom: up ? frame.bottom - r.top + gap : null,
      width: r.width,
      maxHeight,
      dx: frame.left,
    };
  }

  /** Keeps a listbox wider than the trigger inside the window. */
  async function fit(): Promise<void> {
    await tick();
    if (!listbox || !pos || !trigger) return;
    const vw = window.innerWidth;
    const w = listbox.getBoundingClientRect().width;
    const rtl = getComputedStyle(trigger).direction === 'rtl';
    const r = trigger.getBoundingClientRect();
    const start = rtl ? r.right - w : pos.left + pos.dx;
    const left = Math.max(8, Math.min(start, vw - w - 8)) - pos.dx;
    if (left !== pos.left) pos = { ...pos, left };
  }

  function openList(at: 'selected' | 'first' | 'last' = 'selected'): void {
    if (disabled || open) return;
    listName = ariaLabel ?? (trigger?.labels?.[0]?.textContent ?? '').trim();
    place();
    open = true;
    active =
      at === 'first' ? first() : at === 'last' ? last() : selectedIndex >= 0 && enabled(selectedIndex) ? selectedIndex : first();
    void fit();
    void reveal();
    document.addEventListener('pointerdown', onOutside, true);
    window.addEventListener('resize', onResize);
    document.addEventListener('scroll', onScroll, true);
  }

  function closeList(): void {
    if (!open) return;
    open = false;
    active = -1;
    document.removeEventListener('pointerdown', onOutside, true);
    window.removeEventListener('resize', onResize);
    document.removeEventListener('scroll', onScroll, true);
  }

  onDestroy(closeList);

  // The trigger's keys are read by a listener on the trigger itself, not through Svelte's
  // delegated one at the app root: a modal's focus trap listens on the dialog node, below the
  // root, and would otherwise see Escape first and close the whole dialog. Here Escape on an
  // open list stops at the trigger (closes the list only).
  $effect(() => {
    const el = trigger;
    if (!el) return;
    el.addEventListener('keydown', onkeydown);
    return () => el.removeEventListener('keydown', onkeydown);
  });

  function choose(i: number): void {
    const o = options[i];
    if (!o || o.disabled) return;
    const changed = o.value !== value;
    value = o.value;
    if (changed) onchange?.(o.value);
  }

  function onOutside(e: PointerEvent): void {
    const t = e.target;
    if (t instanceof Node && (trigger?.contains(t) || listbox?.contains(t))) return;
    closeList();
  }

  function onResize(): void {
    closeList();
  }

  function onScroll(e: Event): void {
    if (e.target instanceof Node && listbox?.contains(e.target)) return;
    closeList();
  }

  /** Type-ahead: the next option whose label starts with what was typed (a repeated letter cycles). */
  function search(key: string, from: number): number {
    const now = Date.now();
    typed = now - typedAt > TYPEAHEAD_MS ? key : typed + key;
    typedAt = now;
    const needle = typed.toLowerCase();
    const cycle = needle.length > 1 && [...needle].every((c) => c === needle[0]);
    const prefix = cycle ? (needle[0] ?? '') : needle;
    const n = options.length;
    const start = cycle || needle.length === 1 ? from + 1 : Math.max(0, from);
    for (let k = 0; k < n; k++) {
      const i = (((start + k) % n) + n) % n;
      if (enabled(i) && (options[i]?.label ?? '').toLowerCase().startsWith(prefix)) return i;
    }
    return -1;
  }

  function onkeydown(e: KeyboardEvent): void {
    if (disabled) return;
    const key = e.key;
    viaKeys = true;
    if (!open) {
      if (key === 'ArrowDown' || key === 'ArrowUp' || key === 'Enter' || key === ' ') {
        e.preventDefault();
        openList();
      } else if (key === 'Home' || key === 'End') {
        e.preventDefault();
        openList(key === 'Home' ? 'first' : 'last');
      } else if (key.length === 1 && /\S/.test(key) && !e.ctrlKey && !e.metaKey && !e.altKey) {
        e.preventDefault();
        const i = search(key, selectedIndex);
        if (i >= 0) choose(i);
      }
      return;
    }
    switch (key) {
      case 'ArrowDown':
        e.preventDefault();
        move(active + 1, 1);
        break;
      case 'ArrowUp':
        e.preventDefault();
        if (e.altKey) {
          if (active >= 0) choose(active);
          closeList();
        } else move(active - 1, -1);
        break;
      case 'Home':
        e.preventDefault();
        move(0, 1);
        break;
      case 'End':
        e.preventDefault();
        move(options.length - 1, -1);
        break;
      case 'PageDown':
        e.preventDefault();
        move(active + PAGE, 1);
        break;
      case 'PageUp':
        e.preventDefault();
        move(active - PAGE, -1);
        break;
      case 'Enter':
      case ' ':
        e.preventDefault();
        if (active >= 0) choose(active);
        closeList();
        break;
      case 'Escape':
        // Closes the list only: a dialog around it stays open.
        e.preventDefault();
        e.stopPropagation();
        closeList();
        break;
      case 'Tab':
        if (active >= 0) choose(active);
        closeList();
        break;
      default:
        if (key.length === 1 && /\S/.test(key) && !e.ctrlKey && !e.metaKey && !e.altKey) {
          e.preventDefault();
          const i = search(key, active);
          if (i >= 0) {
            active = i;
            void reveal();
          }
        }
    }
  }
</script>

<button
  bind:this={trigger}
  type="button"
  id={triggerId}
  class="select size-{size}"
  class:open
  class:empty={!selected}
  role="combobox"
  aria-haspopup="listbox"
  aria-expanded={open}
  aria-controls={listId}
  aria-activedescendant={open && active >= 0 ? optionId(active) : undefined}
  aria-label={ariaLabel}
  aria-labelledby={ariaLabelledby}
  aria-describedby={ariaDescribedby}
  aria-invalid={invalid ? 'true' : undefined}
  value={String(value)}
  data-value={String(value)}
  title={title ?? selected?.title}
  {disabled}
  onclick={() => {
    viaKeys = false;
    if (open) closeList();
    else openList();
  }}
  onblur={closeList}
>
  {#if icon}<span class="lead"><Icon name={icon} size={16} /></span>{/if}
  <span class="shown">
    {#if selected}
      <span class="label">{selected.label}</span>{#if selected.detail}<span class="detail"><span class="dot-sep" aria-hidden="true">·</span>{selected.detail}</span>{/if}
    {:else}
      <span class="placeholder">{placeholder}</span>
    {/if}
  </span>
  <span class="chevron"><Icon name="chevron-down" size={14} /></span>
</button>

<div
  bind:this={listbox}
  id={listId}
  class="listbox"
  class:keys={viaKeys}
  role="listbox"
  tabindex="-1"
  aria-label={ariaLabelledby ? undefined : listName || undefined}
  aria-labelledby={ariaLabelledby}
  hidden={!open}
  style:left={pos ? `${pos.left}px` : undefined}
  style:top={pos?.top != null ? `${pos.top}px` : undefined}
  style:bottom={pos?.bottom != null ? `${pos.bottom}px` : undefined}
  style:min-inline-size={pos ? `${pos.width}px` : undefined}
  style:max-block-size={pos ? `${pos.maxHeight}px` : undefined}
  onmousedown={(e) => e.preventDefault()}
>
  {#snippet row(option: SelectOption<V>, index: number)}
    <div
      id={optionId(index)}
      class="option"
      class:active={index === active}
      class:chosen={index === selectedIndex}
      role="option"
      tabindex="-1"
      aria-selected={index === selectedIndex ? 'true' : 'false'}
      aria-disabled={option.disabled ? 'true' : undefined}
      data-value={String(option.value)}
      title={option.title}
      onclick={() => {
        if (option.disabled) return;
        choose(index);
        closeList();
        trigger?.focus();
      }}
      onkeydown={(e) => {
        // the options never hold the focus (the trigger does); this is for completeness
        if ((e.key === 'Enter' || e.key === ' ') && !option.disabled) {
          e.preventDefault();
          choose(index);
          closeList();
          trigger?.focus();
        }
      }}
      onpointermove={() => {
        viaKeys = false;
        if (!option.disabled && active !== index) active = index;
      }}
    >
      <span class="tick">{#if index === selectedIndex}<Icon name="check" size={14} />{/if}</span>
      <span class="text"
        ><span class="olabel" data-option-label>{option.label}</span>{#if option.detail}<span class="detail">{option.detail}</span
          >{/if}</span
      >
      {#if option.meta}<span class="meta">{option.meta}</span>{/if}
    </div>
  {/snippet}
  {#each groups as group (group.name)}
    {#if group.name}
      <div class="group" role="group" aria-labelledby="{uid}-group-{group.items[0]?.index ?? 0}">
        <div class="ghead" id="{uid}-group-{group.items[0]?.index ?? 0}">{group.name}</div>
        {#each group.items as item (item.index)}
          {@render row(item.option, item.index)}
        {/each}
      </div>
    {:else}
      {#each group.items as item (item.index)}
        {@render row(item.option, item.index)}
      {/each}
    {/if}
  {/each}
</div>

<style>
  .select {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    inline-size: 100%;
    min-inline-size: 0;
    block-size: 36px;
    padding-block: 0;
    padding-inline: 11px 9px;
    border: 1px solid var(--line-field);
    border-radius: 9px;
    background: var(--field);
    color: var(--ink);
    font-size: var(--type-body);
    text-align: start;
    cursor: pointer;
    transition:
      border-color var(--dur-2) ease,
      box-shadow var(--dur-2) ease,
      background-color var(--dur-2) ease;
  }
  .size-sm {
    block-size: 30px;
    border-radius: var(--r-md);
  }
  /* inline: borderless until hover (the details pane's speed limit) */
  .size-inline {
    inline-size: auto;
    block-size: 26px;
    padding-inline: 7px 5px;
    gap: 5px;
    border-color: transparent;
    border-radius: var(--r-sm);
    background: transparent;
    font-weight: 600;
  }
  .size-inline:hover:not(:disabled),
  .size-inline.open {
    border-color: var(--line);
    background: var(--field);
  }
  /* the fields' focus (app.css .input): a 2 px --focus edge, then a solid soft spread */
  .select:focus,
  .select:focus-visible,
  .select.open {
    outline: none;
    border-color: var(--focus);
    box-shadow:
      0 0 0 1px var(--focus),
      0 0 0 4px var(--accent-soft);
  }
  .select[aria-invalid='true'] {
    border-color: var(--danger);
  }
  .select:disabled {
    background: var(--surface-2);
    color: var(--faint);
    cursor: default;
  }
  .lead {
    display: grid;
    flex: none;
    color: var(--faint);
  }
  .shown {
    flex: 1 1 auto;
    min-inline-size: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .shown .detail {
    color: var(--muted);
  }
  .dot-sep {
    margin-inline: 0.4em;
  }
  .placeholder {
    color: var(--faint);
  }
  .chevron {
    display: grid;
    flex: none;
    color: var(--muted);
    transition: transform var(--dur-3) var(--ease-out);
  }
  .select.open .chevron {
    transform: rotate(180deg);
  }

  .listbox {
    position: fixed;
    z-index: 60;
    display: block;
    max-inline-size: min(480px, calc(100vw - 16px));
    overflow-y: auto;
    padding: 5px;
    border: 1px solid var(--line);
    border-radius: 11px;
    background: var(--raised);
    box-shadow: var(--shadow-lg);
    color: var(--ink-2);
    font-size: var(--type-body);
    animation: rise var(--dur-3) var(--ease-out);
  }
  .listbox[hidden] {
    display: none;
  }
  @keyframes rise {
    from {
      opacity: 0;
      transform: translateY(4px);
    }
  }
  .ghead {
    padding: 9px 9px 5px;
    font-size: var(--type-caps);
    font-weight: 600;
    letter-spacing: 0.07em;
    text-transform: uppercase;
    color: var(--faint);
  }
  .option {
    display: flex;
    align-items: center;
    gap: 8px;
    min-block-size: 30px;
    padding-inline: 8px 10px;
    border-radius: 7px;
    white-space: nowrap;
    cursor: pointer;
  }
  .option.active {
    background: var(--sel);
    box-shadow: inset 0 0 0 1px var(--sel-line);
    color: var(--ink);
  }
  /* the keyboard's option also gets a --focus ring (the tint alone is under 3:1), as in
     ContextMenu; the pointer's only the tint */
  .listbox.keys .option.active {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
  }
  .option.active .meta {
    color: var(--muted);
  }
  .option.chosen {
    color: var(--ink);
    font-weight: 600;
  }
  .option[aria-disabled='true'] {
    color: var(--faint);
    cursor: default;
  }
  .tick {
    display: grid;
    flex: none;
    place-items: center;
    inline-size: 14px;
    color: var(--accent);
  }
  .text {
    flex: 1 1 auto;
    min-inline-size: 0;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .text .detail {
    margin-inline-start: 6px;
    font-size: var(--type-small);
    font-weight: 400;
    color: var(--muted);
  }
  .meta {
    flex: none;
    margin-inline-start: 16px;
    font-size: var(--type-small);
    font-weight: 400;
    color: var(--faint);
    font-variant-numeric: tabular-nums;
  }
  @media (forced-colors: active) {
    .select {
      border-color: ButtonText;
    }
    .select:focus-visible {
      outline: 2px solid Highlight;
      outline-offset: 1px;
    }
    .listbox {
      border-color: CanvasText;
    }
    .option.active {
      background: Highlight;
      color: HighlightText;
      forced-color-adjust: none;
    }
  }
</style>
