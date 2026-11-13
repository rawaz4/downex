<script lang="ts">
  // A split button (DESIGN-SYSTEM.md §3.2): the primary action and a 26 px chevron that opens
  // a menu of other ways to do it (the Add button: "Add downloads…", "Add video or stream…").
  // The halves are separated by a 1 px line; the menu opens below, aligned to the end.
  import Icon, { type IconName } from './Icon.svelte';
  import MenuButton from './MenuButton.svelte';
  import type { MenuItem } from './ContextMenu.svelte';

  interface Props {
    label: string;
    icon?: IconName;
    onclick: () => void;
    items: readonly MenuItem[];
    onselect: (id: string) => void;
    /** Unavailable: both halves keep the focus (aria-disabled) and do nothing. */
    disabled?: boolean;
    /** The main half's tooltip ("Add downloads (Ctrl+N)"). */
    title?: string;
    /** aria-keyshortcuts of the main half ("Control+N"). */
    keyshortcuts?: string;
    /** The chevron's accessible name. */
    moreLabel?: string;
    /** The menu's accessible name. */
    menuLabel?: string;
  }

  let {
    label,
    icon,
    onclick,
    items,
    onselect,
    disabled = false,
    title,
    keyshortcuts,
    moreLabel = 'More ways to add',
    menuLabel = 'Add',
  }: Props = $props();

  let more: MenuButton | undefined = $state();

  /** Opens the chevron's menu (from a shortcut or the palette). */
  export function openMenu(): void {
    more?.openMenu();
  }
</script>

<div class="split">
  <button
    type="button"
    class="btn primary main"
    {title}
    aria-keyshortcuts={keyshortcuts}
    aria-disabled={disabled ? 'true' : undefined}
    onclick={() => {
      if (!disabled) onclick();
    }}
  >
    {#if icon}<Icon name={icon} size={16} />{/if}
    <span>{label}</span>
  </button>
  <MenuButton
    bind:this={more}
    class="btn primary more"
    ariaLabel={moreLabel}
    title={moreLabel}
    label={menuLabel}
    {items}
    {onselect}
    {disabled}
    align="end"
  >
    <Icon name="chevron-down" size={14} />
  </MenuButton>
</div>

<style>
  .split {
    display: inline-flex;
    flex: none;
    border-radius: var(--r-md);
    box-shadow: var(--primary-lift);
  }
  .split :global(.btn.primary) {
    box-shadow: none;
  }
  .split :global(.btn.primary.main) {
    padding-inline-end: 12px;
    border-start-end-radius: 0;
    border-end-end-radius: 0;
  }
  .split :global(.btn.primary.more) {
    inline-size: 26px;
    padding: 0;
    border-start-start-radius: 0;
    border-end-start-radius: 0;
    border-inline-start: 1px solid var(--split-line);
  }
</style>
