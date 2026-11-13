<script lang="ts">
  // A menu button (DESIGN-SYSTEM.md §3.3): any button look + an anchored ContextMenu.
  // aria-haspopup="menu" and aria-expanded; opens on click, Enter, Space or ArrowDown (on the
  // first item) and ArrowUp (on the last); closing returns the focus to the trigger.
  // `openMenu()` and `closeMenu()` are exported for callers that open it from elsewhere (the
  // command palette's "Set global speed limit…").
  import type { Snippet } from 'svelte';
  import ContextMenu, { type MenuItem } from './ContextMenu.svelte';

  interface Props {
    items: readonly MenuItem[];
    /** The menu's accessible name. */
    label: string;
    onselect: (id: string) => void;
    /** The trigger's content (icon, text, chevron). */
    children: Snippet;
    /** The trigger's classes: "btn ghost" (default), "btn sm", "icon-btn", … */
    class?: string;
    /** The trigger's accessible name when its content is not one (icon only). */
    ariaLabel?: string;
    title?: string;
    /** Unavailable: the trigger keeps the focus (aria-disabled) and does not open. */
    disabled?: boolean;
    align?: 'start' | 'end';
    direction?: 'down' | 'up';
    footnote?: string;
    id?: string;
    /** Hears the menu open and close. */
    ontoggle?: (open: boolean) => void;
  }

  let {
    items,
    label,
    onselect,
    children,
    class: cls = 'btn ghost',
    ariaLabel,
    title,
    disabled = false,
    align = 'start',
    direction = 'down',
    footnote,
    id,
    ontoggle,
  }: Props = $props();

  let trigger: HTMLButtonElement | undefined = $state();
  let open = $state(false);
  let initial = $state<'first' | 'last' | 'checked'>('first');

  /** Opens the menu with the focus on its first item, its last (`'last'`) or its checked one. */
  export function openMenu(focus: 'first' | 'last' | 'checked' = 'first'): void {
    if (disabled || open) return;
    initial = focus;
    open = true;
    ontoggle?.(true);
  }

  /** Closes the menu; the focus goes back to the trigger unless `restore` is false. */
  export function closeMenu(restore = true): void {
    if (!open) return;
    open = false;
    ontoggle?.(false);
    if (restore) trigger?.focus();
  }

  function onclick() {
    if (open) closeMenu(false);
    else openMenu();
  }

  function onkeydown(e: KeyboardEvent) {
    if (e.key === 'ArrowDown' || e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();
      openMenu('first');
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      openMenu('last');
    }
  }
</script>

<button
  bind:this={trigger}
  type="button"
  class={cls}
  {id}
  {title}
  aria-label={ariaLabel}
  aria-haspopup="menu"
  aria-expanded={open}
  aria-disabled={disabled ? 'true' : undefined}
  {onclick}
  {onkeydown}
>
  {@render children()}
</button>
{#if open && trigger}
  <ContextMenu
    {items}
    {label}
    {footnote}
    anchor={{ el: trigger, align, direction }}
    initialFocus={initial}
    onselect={(itemId) => {
      closeMenu();
      onselect(itemId);
    }}
    onclose={() => closeMenu()}
  />
{/if}
