<script lang="ts" module>
  import type { IconName } from './Icon.svelte';

  export interface MenuItem {
    readonly id: string;
    readonly label: string;
    readonly icon?: IconName;
    readonly disabled?: boolean;
    readonly danger?: boolean;
    readonly shortcut?: string;
    /** Draw a separator above this item. */
    readonly separated?: boolean;
    /** A plain item (default), a radio item (menuitemradio) or a checkbox item (menuitemcheckbox). */
    readonly kind?: 'item' | 'radio' | 'checkbox';
    /** Radio and checkbox items: on (a Mint check in the gutter, aria-checked). */
    readonly checked?: boolean;
    /** A caps heading above this item: it starts a group named by it. */
    readonly heading?: string;
    /** Secondary text at the end, in --faint ("Ctrl L", "now"). */
    readonly note?: string;
    /** Tooltip (a disabled item's reason). */
    readonly title?: string;
  }

  /** Where an anchored menu opens: below or above `el`, aligned to its start or end edge. */
  export interface MenuAnchor {
    readonly el: HTMLElement;
    readonly align?: 'start' | 'end';
    readonly direction?: 'down' | 'up';
  }

  interface Group {
    readonly key: string;
    readonly heading: string;
    readonly separated: boolean;
    readonly items: readonly MenuItem[];
  }

  /** Splits the items into groups: a heading or a separator starts a new one. */
  function groupItems(items: readonly MenuItem[]): Group[] {
    const groups: { key: string; heading: string; separated: boolean; items: MenuItem[] }[] = [];
    for (const item of items) {
      const last = groups[groups.length - 1];
      if (!last || item.heading || item.separated) {
        groups.push({ key: item.id, heading: item.heading ?? '', separated: !!item.separated && groups.length > 0, items: [item] });
      } else last.items.push(item);
    }
    return groups;
  }

  const ITEM_ROLES = '[role="menuitem"],[role="menuitemradio"],[role="menuitemcheckbox"]';
</script>

<script lang="ts">
  // A menu (DESIGN-SYSTEM.md §3.3): at a point (the context menu) or anchored to a trigger
  // (MenuButton). Arrows, Home and End move, type-ahead jumps, Esc and Tab close; radio and
  // checkbox items carry aria-checked; headings name groups; a footnote explains the menu.
  import { onMount, tick } from 'svelte';
  import Icon from './Icon.svelte';

  interface Props {
    /** The point to open at (a context menu); ignored with `anchor`. */
    x?: number;
    y?: number;
    items: readonly MenuItem[];
    label: string;
    onselect: (id: string) => void;
    onclose: () => void;
    /** Small print under a separator (11.5 --faint); it also describes the menu. */
    footnote?: string;
    /** Opens next to an element instead of at a point; flips when there is no room. */
    anchor?: MenuAnchor;
    /** Which item has the focus at first (ArrowUp on a trigger opens on the last). */
    initialFocus?: 'first' | 'last' | 'checked';
    id?: string;
  }

  let { x = 0, y = 0, items, label, onselect, onclose, footnote, anchor, initialFocus = 'first', id }: Props = $props();

  const uid = $props.id();
  const footId = `${uid}-foot`;
  const groups = $derived(groupItems(items));

  let menu: HTMLDivElement | undefined = $state();
  let left = $state(0);
  let top = $state(0);
  let placed = $state(false);

  const MARGIN = 8;
  const GAP = 4;

  function buttons(): HTMLButtonElement[] {
    return menu ? Array.from(menu.querySelectorAll<HTMLButtonElement>(ITEM_ROLES)).filter((b) => b.getAttribute('aria-disabled') !== 'true') : [];
  }

  function focusAt(index: number): void {
    const list = buttons();
    if (list.length === 0) {
      menu?.focus();
      return;
    }
    const i = ((index % list.length) + list.length) % list.length;
    list[i]?.focus();
  }

  function focusInitial(): void {
    if (initialFocus === 'last') return focusAt(-1);
    if (initialFocus === 'checked') {
      const list = buttons();
      const at = list.findIndex((b) => b.getAttribute('aria-checked') === 'true');
      return focusAt(Math.max(0, at));
    }
    focusAt(0);
  }

  function onkeydown(e: KeyboardEvent) {
    const list = buttons();
    const current = list.indexOf(document.activeElement as HTMLButtonElement);
    switch (e.key) {
      case 'ArrowDown':
        e.preventDefault();
        focusAt(current + 1);
        break;
      case 'ArrowUp':
        e.preventDefault();
        focusAt(current < 0 ? -1 : current - 1);
        break;
      case 'Home':
        e.preventDefault();
        focusAt(0);
        break;
      case 'End':
        e.preventDefault();
        focusAt(-1);
        break;
      case 'Escape':
      case 'Tab':
        e.preventDefault();
        e.stopPropagation();
        onclose();
        break;
      default:
        if (e.key.length === 1 && /\S/.test(e.key)) {
          const k = e.key.toLowerCase();
          const start = current + 1;
          const order = [...list.slice(start), ...list.slice(0, start)];
          order.find((b) => (b.querySelector('.text')?.textContent ?? b.textContent ?? '').trim().toLowerCase().startsWith(k))?.focus();
        }
    }
  }

  function choose(item: MenuItem) {
    if (item.disabled) return;
    onselect(item.id);
  }

  function roleOf(item: MenuItem): 'menuitem' | 'menuitemradio' | 'menuitemcheckbox' {
    return item.kind === 'radio' ? 'menuitemradio' : item.kind === 'checkbox' ? 'menuitemcheckbox' : 'menuitem';
  }

  function clamp(v: number, lo: number, hi: number): number {
    return Math.max(lo, Math.min(v, hi));
  }

  /** Below (or above) the anchor, at its start or end edge; flipped when there is no room. */
  function placeAnchored(a: MenuAnchor, rect: DOMRect): void {
    const r = a.el.getBoundingClientRect();
    const vw = window.innerWidth;
    const vh = window.innerHeight;
    const rtl = getComputedStyle(a.el).direction === 'rtl';
    const atEnd = (a.align ?? 'start') === 'end' ? !rtl : rtl;
    left = clamp(atEnd ? r.right - rect.width : r.left, MARGIN, Math.max(MARGIN, vw - rect.width - MARGIN));
    const below = r.bottom + GAP;
    const above = r.top - GAP - rect.height;
    const fitsBelow = below + rect.height <= vh - MARGIN;
    const fitsAbove = above >= MARGIN;
    const up = (a.direction ?? 'down') === 'up' ? fitsAbove || !fitsBelow : !fitsBelow && fitsAbove;
    top = clamp(up ? above : below, MARGIN, Math.max(MARGIN, vh - rect.height - MARGIN));
  }

  onMount(() => {
    const onPointer = (e: PointerEvent) => {
      if (!(e.target instanceof Node) || !menu || menu.contains(e.target)) return;
      // The trigger of an anchored menu toggles it itself.
      if (anchor?.el.contains(e.target)) return;
      onclose();
    };
    const onWindow = () => onclose();
    // Rows move under a fixed menu when the list scrolls (scrolling the menu itself is fine).
    const onScroll = (e: Event) => {
      if (e.target instanceof Node && menu?.contains(e.target)) return;
      onclose();
    };
    document.addEventListener('pointerdown', onPointer, true);
    window.addEventListener('blur', onWindow);
    window.addEventListener('resize', onWindow);
    document.addEventListener('scroll', onScroll, true);
    void (async () => {
      await tick();
      if (!menu) return;
      const rect = menu.getBoundingClientRect();
      if (anchor) placeAnchored(anchor, rect);
      else {
        left = Math.max(4, Math.min(x, window.innerWidth - rect.width - 4));
        top = Math.max(4, Math.min(y, window.innerHeight - rect.height - 4));
      }
      placed = true;
      // Hidden (visibility) elements cannot take focus: wait until the menu is shown.
      await tick();
      focusInitial();
    })();
    return () => {
      document.removeEventListener('pointerdown', onPointer, true);
      window.removeEventListener('blur', onWindow);
      window.removeEventListener('resize', onWindow);
      document.removeEventListener('scroll', onScroll, true);
    };
  });
</script>

{#snippet entry(item: MenuItem)}
  {@const checkable = item.kind === 'radio' || item.kind === 'checkbox'}
  <button
    type="button"
    role={roleOf(item)}
    class="item"
    class:danger={item.danger}
    class:checked={checkable && item.checked}
    tabindex="-1"
    title={item.title}
    aria-checked={checkable ? (item.checked ? 'true' : 'false') : undefined}
    aria-disabled={item.disabled ? 'true' : undefined}
    onclick={() => choose(item)}
  >
    <span class="glyph">
      {#if checkable}
        {#if item.checked}<span class="tick"><Icon name="check" size={14} /></span>{/if}
      {:else if item.icon}
        <Icon name={item.icon} size={16} />
      {/if}
    </span>
    <span class="text">{item.label}</span>
    {#if item.note}<span class="item-note">{item.note}</span>{/if}
    {#if item.shortcut}<kbd>{item.shortcut}</kbd>{/if}
  </button>
{/snippet}

<div
  bind:this={menu}
  {id}
  class="menu"
  class:placed
  role="menu"
  aria-label={label}
  aria-describedby={footnote ? footId : undefined}
  tabindex="-1"
  style:left="{placed ? left : anchor ? 0 : x}px"
  style:top="{placed ? top : anchor ? 0 : y}px"
  {onkeydown}
>
  {#each groups as group (group.key)}
    {#if group.separated}
      <div class="sep" role="separator"></div>
    {/if}
    {#if group.heading}
      <div class="group" role="group" aria-labelledby="{uid}-g-{group.key}">
        <div class="heading" id="{uid}-g-{group.key}">{group.heading}</div>
        {#each group.items as item (item.id)}
          {@render entry(item)}
        {/each}
      </div>
    {:else}
      {#each group.items as item (item.id)}
        {@render entry(item)}
      {/each}
    {/if}
  {/each}
  {#if footnote}
    <div class="sep" role="separator"></div>
    <p class="footnote" id={footId}>{footnote}</p>
  {/if}
</div>

<style>
  .menu {
    position: fixed;
    z-index: 50;
    min-inline-size: 220px;
    max-inline-size: min(360px, calc(100vw - 16px));
    max-block-size: calc(100vh - 16px);
    overflow-y: auto;
    padding: 5px;
    border: 1px solid var(--line);
    border-radius: 11px;
    background: var(--raised);
    box-shadow: var(--shadow-lg);
    color: var(--ink-2);
    font-size: var(--type-body);
    visibility: hidden;
  }
  .menu:focus {
    outline: none;
  }
  .menu.placed {
    visibility: visible;
    animation: rise var(--dur-3) var(--ease-out);
  }
  @keyframes rise {
    from {
      opacity: 0;
      transform: translateY(4px);
    }
  }
  .heading {
    padding: 9px 9px 7px;
    font-size: var(--type-caps);
    font-weight: 600;
    letter-spacing: 0.07em;
    text-transform: uppercase;
    color: var(--faint);
  }
  .item {
    display: flex;
    align-items: center;
    gap: 9px;
    inline-size: 100%;
    min-block-size: 30px;
    padding-block: 0;
    padding-inline: 9px;
    border: 0;
    border-radius: 7px;
    background: transparent;
    color: var(--ink-2);
    text-align: start;
    white-space: nowrap;
    cursor: pointer;
    transition: background-color var(--dur-2) ease;
  }
  .item:hover:not([aria-disabled='true']),
  .item:focus {
    background: var(--sel);
    box-shadow: inset 0 0 0 1px var(--sel-line);
    color: var(--ink);
  }
  .item:focus {
    outline: none;
  }
  /* the keyboard's item also gets the focus ring (the tint alone is under 3:1) */
  .item:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
  }
  /* --faint is under 4.5:1 on the tint */
  .item:focus .item-note {
    color: var(--muted);
  }
  .item.checked {
    color: var(--ink);
    font-weight: 600;
  }
  .item[aria-disabled='true'] {
    color: var(--faint);
    cursor: default;
  }
  .item[aria-disabled='true'] .glyph {
    opacity: 0.7;
  }
  .item.danger:not([aria-disabled='true']) {
    color: var(--danger);
  }
  .glyph {
    flex: none;
    inline-size: 16px;
    display: grid;
    place-items: center;
    color: var(--muted);
  }
  .item.danger .glyph {
    color: inherit;
  }
  .tick {
    display: grid;
    color: var(--accent);
  }
  .text {
    flex: 1;
    min-inline-size: 0;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .item-note {
    flex: none;
    margin-inline-start: 12px;
    font-size: var(--type-small);
    font-weight: 400;
    color: var(--faint);
  }
  kbd {
    flex: none;
    margin-inline-start: 12px;
  }
  .sep {
    block-size: 1px;
    margin: 5px 4px;
    background: var(--line);
  }
  .footnote {
    max-inline-size: 30ch;
    padding: 4px 9px 6px;
    font-size: var(--type-meta);
    line-height: 1.4;
    color: var(--faint);
    white-space: normal;
  }
  @media (forced-colors: active) {
    .menu {
      border-color: CanvasText;
    }
    .item:focus,
    .item:hover:not([aria-disabled='true']) {
      background: Highlight;
      color: HighlightText;
    }
    /* the danger colour would outrank it */
    .item.danger:focus,
    .item.danger:hover:not([aria-disabled='true']) {
      color: HighlightText;
    }
  }
</style>
