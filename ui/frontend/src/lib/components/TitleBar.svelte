<script lang="ts" module>
  /** How long a double-click on the bar waits for the system's own maximise before toggling it. */
  export const NATIVE_DBLCLICK_MS = 120;
</script>

<script lang="ts">
  // The window's title bar (DESIGN-SYSTEM.md §4.2): the logo and name, the list search with the
  // command palette's "Ctrl K" key, the theme and Settings buttons and, when the host window is
  // frameless (customTitleBar: the Windows main window), the drag region, a double-click that
  // maximises, and the minimise, maximise/restore and close buttons. Without customTitleBar
  // (macOS, older hosts, tests) the same bar shows without window controls or drag region.
  import BrandMark from './BrandMark.svelte';
  import Icon from './Icon.svelte';
  import type { WindowControls } from '../api';
  import { theme, nextTheme, type ThemeChoice } from '../stores/theme.svelte';

  interface Props {
    /** The host window is frameless: the bar is its drag region and draws the window controls. */
    customTitleBar?: boolean;
    /**
     * A modal dialog or the command palette is open: the search and the bar's buttons are inert.
     * On a frameless window the drag region and the window controls stay usable above the dialog's
     * scrim (and the window controls stay in the keyboard's reach: a TRAP_EXEMPT region).
     */
    modal?: boolean;
    /** The window (api.window); its controls are shown only with customTitleBar. */
    controls?: Pick<WindowControls, 'minimise' | 'toggleMaximise' | 'isMaximised' | 'close'>;
    search: string;
    onsearch: (value: string) => void;
    /** Opens the command palette (Ctrl+K). */
    oncommands: () => void;
    onsettings: () => void;
    searchInput?: HTMLInputElement | undefined;
    /** How long a double-click waits for a maximise by the system before it toggles (ms). */
    nativeWait?: number;
  }

  let {
    customTitleBar = false,
    modal = false,
    controls,
    search,
    onsearch,
    oncommands,
    onsettings,
    searchInput = $bindable(),
    nativeWait = NATIVE_DBLCLICK_MS,
  }: Props = $props();

  /** The part of Wails' runtime flags (window.wails.flags) the bar reads and sets. */
  interface WailsFlags {
    enableResize?: boolean;
    resizeEdge?: string;
    defaultCursor?: string | null;
  }

  const THEME_LABEL: Record<ThemeChoice, string> = {
    system: 'System theme',
    light: 'Light theme',
    dark: 'Dark theme',
  };
  const THEME_ICON = { system: 'system', light: 'sun', dark: 'moon' } as const;

  const frameless = $derived(customTitleBar && controls !== undefined);

  // The maximise button's glyph follows the window: read at start and after every resize
  // (maximising, restoring and snapping all resize the page).
  let maximised = $state(false);
  let reads = 0;
  function readMaximised(): void {
    if (!controls) return;
    const n = ++reads;
    controls.isMaximised().then(
      (m) => {
        if (n !== reads) return;
        maximised = m;
        if (frameless) followResizeEdges(m);
      },
      () => {},
    );
  }

  // Wails' runtime turns the outer 6 px of a frameless page into resize edges (and turns that
  // off only in full screen). On a maximised window they cannot resize anything, but they take
  // the press from the caption: the top edge would not start the drag that restores the window.
  // Off while maximised, back on when restored, and only when it was this bar that turned it off.
  let edgesOff = false;
  function followResizeEdges(isMaximised: boolean): void {
    const flags = (window as { wails?: { flags?: WailsFlags } }).wails?.flags;
    if (!flags) return;
    if (isMaximised && flags.enableResize === true) {
      flags.enableResize = false;
      edgesOff = true;
      if (flags.resizeEdge) {
        flags.resizeEdge = undefined;
        document.documentElement.style.cursor = flags.defaultCursor ?? '';
      }
    } else if (!isMaximised && edgesOff) {
      flags.enableResize = true;
      edgesOff = false;
    }
  }
  $effect(() => {
    if (!frameless) return;
    readMaximised();
    window.addEventListener('resize', readMaximised);
    return () => window.removeEventListener('resize', readMaximised);
  });

  function toggleMaximise(): void {
    controls?.toggleMaximise();
    readMaximised();
  }

  /** A control of the bar (a press there is not a drag or a double-click on the bar). */
  function onControl(target: EventTarget | null): boolean {
    return target instanceof Element && target.closest('button, input, a, [data-no-drag]') !== null;
  }

  // Double-click on empty bar space maximises or restores, as on a system title bar. Should
  // the system do that by itself as well, the toggle is skipped: the state at the first press
  // is compared with the state a moment after the double-click.
  let atPress: Promise<boolean> | null = null;

  function onmousedown(e: MouseEvent): void {
    if (!frameless || e.button !== 0 || onControl(e.target)) return;
    if (e.detail <= 1) atPress = controls?.isMaximised().catch(() => false) ?? null;
  }

  async function ondblclick(e: MouseEvent): Promise<void> {
    if (!frameless || !controls || onControl(e.target)) return;
    const before = await (atPress ?? controls.isMaximised().catch(() => false));
    atPress = null;
    if (nativeWait > 0) await new Promise((r) => setTimeout(r, nativeWait));
    const now = await controls.isMaximised().catch(() => before);
    if (now === before) toggleMaximise();
    else maximised = now;
  }
</script>

<!-- svelte-ignore a11y_no_static_element_interactions (double-click on empty bar space is a mouse convenience; the maximise button is the keyboard's way) -->
<header
  class="titlebar"
  class:frameless
  class:modal
  style:--wails-draggable={frameless ? 'drag' : undefined}
  {onmousedown}
  ondblclick={(e) => void ondblclick(e)}
>
  <div class="brand">
    <BrandMark size={24} />
    <span class="word">Downex</span>
  </div>

  <div class="search" style:--wails-draggable={frameless ? 'no-drag' : undefined} inert={modal}>
    <span class="glass" aria-hidden="true"><Icon name="search" size={15} /></span>
    <input
      bind:this={searchInput}
      class="field"
      type="search"
      placeholder="Search downloads"
      aria-label="Search downloads by name or URL"
      aria-keyshortcuts="Control+F"
      spellcheck="false"
      autocomplete="off"
      value={search}
      oninput={(e) => onsearch(e.currentTarget.value)}
      onkeydown={(e) => {
        if (e.key === 'Escape' && search !== '') {
          e.preventDefault();
          onsearch('');
        }
      }}
    />
    <button
      type="button"
      class="palette-key"
      onclick={oncommands}
      aria-label="Commands"
      aria-keyshortcuts="Control+K"
      title="Commands (Ctrl+K)"
    >
      <kbd>Ctrl K</kbd>
    </button>
  </div>

  <div class="end" style:--wails-draggable={frameless ? 'no-drag' : undefined}>
    <div class="bar-btns" inert={modal}>
      <button
        type="button"
        class="icon-btn bar-btn"
        onclick={() => theme.cycle()}
        aria-label="{THEME_LABEL[theme.choice]}. Switch to {THEME_LABEL[nextTheme(theme.choice)].toLowerCase()}"
        title="{THEME_LABEL[theme.choice]} (click to change)"
      >
        <Icon name={THEME_ICON[theme.choice]} size={17} />
      </button>
      <button type="button" class="icon-btn bar-btn" onclick={onsettings} aria-label="Settings" title="Settings (Ctrl+,)">
        <Icon name="gear" size={17} />
      </button>
    </div>
    {#if frameless && controls}
      <!-- TRAP_EXEMPT, Tab only: reachable with Tab from an open dialog -->
      <div class="winctl" role="group" aria-label="Window" data-focus-trap-exempt="tab">
        <button type="button" class="wc" aria-label="Minimise" title="Minimise" onclick={() => controls.minimise()}>
          <Icon name="win-minimise" size={20} />
        </button>
        <button
          type="button"
          class="wc"
          aria-label={maximised ? 'Restore' : 'Maximise'}
          title={maximised ? 'Restore' : 'Maximise'}
          onclick={toggleMaximise}
          data-maximised={maximised ? 'true' : 'false'}
        >
          <Icon name={maximised ? 'win-restore' : 'win-maximise'} size={20} />
        </button>
        <button type="button" class="wc close" aria-label="Close" title="Close" onclick={() => controls.close()}>
          <Icon name="win-close" size={20} />
        </button>
      </div>
    {/if}
  </div>
</header>

<style>
  .titlebar {
    display: grid;
    flex: none;
    grid-template-columns: minmax(0, 1fr) auto minmax(0, 1fr);
    align-items: center;
    gap: var(--sp-6);
    block-size: var(--titlebar-h);
    padding-inline: 14px 10px;
    background: var(--chrome);
    color: var(--ink);
    user-select: none;
    -webkit-user-select: none;
  }
  /* the window controls reach the window's edge */
  .titlebar.frameless {
    padding-inline-end: 0;
  }
  .brand {
    display: flex;
    align-items: center;
    gap: 9px;
    min-inline-size: 0;
  }
  .word {
    font: 650 0.9375rem / 1 var(--font-display);
    letter-spacing: -0.015em;
    white-space: nowrap;
  }

  /* ---- search ---- */
  .search {
    position: relative;
    inline-size: min(460px, 40vw);
    color: var(--faint);
  }
  .glass {
    position: absolute;
    inset-inline-start: 10px;
    inset-block-start: 50%;
    translate: 0 -50%;
    display: grid;
    pointer-events: none;
  }
  .field {
    inline-size: 100%;
    block-size: 28px;
    margin: 0;
    padding-block: 0;
    padding-inline: 32px 62px;
    /* a field's edge (3:1 on the chrome), as every other field */
    border: 1px solid var(--line-field);
    border-radius: var(--r-md);
    background: var(--search-ground, var(--surface));
    box-shadow: var(--shadow-sm);
    color: var(--ink);
    font: inherit;
    font-size: var(--type-body);
    transition:
      border-color var(--dur-2) ease,
      box-shadow var(--dur-2) ease;
  }
  .field::placeholder {
    color: var(--faint);
  }
  .field::-webkit-search-cancel-button {
    display: none;
  }
  .field:hover {
    border-color: var(--ink-2);
  }
  .field:focus {
    outline: none;
    border-color: var(--focus);
    box-shadow:
      0 0 0 1px var(--focus),
      0 0 0 4px var(--accent-soft);
  }
  /* a 24 px target (DESIGN-SYSTEM.md §2.8) inside the 28 px field, the key cap centred in it */
  .palette-key {
    position: absolute;
    inset-inline-end: 3px;
    inset-block-start: 50%;
    translate: 0 -50%;
    display: grid;
    place-items: center;
    block-size: 24px;
    min-inline-size: 24px;
    padding-inline: 2px;
    padding-block: 0;
    border: 0;
    border-radius: var(--r-sm);
    background: none;
    cursor: pointer;
  }
  .palette-key kbd {
    block-size: 19px;
    padding-inline: 6px;
    font-size: 0.6875rem;
    cursor: pointer;
    transition:
      color var(--dur-2) ease,
      border-color var(--dur-2) ease;
  }
  .palette-key:hover kbd {
    color: var(--ink);
    border-color: var(--line-field);
  }
  .palette-key:focus-visible {
    outline-offset: 1px;
  }

  /* ---- end ---- */
  .end {
    justify-self: end;
    display: flex;
    align-items: center;
    gap: 2px;
    block-size: 100%;
  }
  .bar-btns {
    display: flex;
    align-items: center;
    gap: 2px;
  }
  .bar-btn {
    inline-size: 32px;
    block-size: 30px;
  }
  /* Above a modal dialog's scrim (z-index 40) on a frameless window: the caption's drag region,
     double-click and window controls keep working, as a system caption would. Its own search and
     buttons are inert then and step back. */
  .titlebar.frameless {
    position: relative;
    z-index: 45;
  }
  /* a modal dialog's scrim starts below the caption (Dialog.svelte) */
  :global(:root:has(.titlebar.frameless)) {
    --modal-top: var(--titlebar-h);
  }
  .search,
  .bar-btns {
    transition: opacity var(--dur-2) ease;
  }
  .titlebar.modal .search,
  .titlebar.modal .bar-btns {
    opacity: 0.45;
  }
  .winctl {
    display: flex;
    block-size: 100%;
    margin-inline-start: 10px;
  }
  .wc {
    display: grid;
    place-items: center;
    inline-size: 46px;
    block-size: 100%;
    padding: 0;
    border: 0;
    border-radius: 0;
    background: transparent;
    color: var(--ink-2);
    cursor: default;
    transition:
      background-color var(--dur-2) ease,
      color var(--dur-2) ease;
  }
  .wc:hover {
    background: var(--press);
    color: var(--ink);
  }
  .wc:active {
    background: var(--line);
  }
  .wc.close:hover,
  .wc.close:active {
    background: var(--winctl-close);
    color: #fff;
  }
  .wc:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
  }
  .wc.close:focus-visible:hover {
    outline-color: #fff;
  }

  :global(:root[data-theme='dark']) .titlebar {
    --search-ground: #141a17;
  }
  @media (prefers-color-scheme: dark) {
    :global(:root:not([data-theme='light'])) .titlebar {
      --search-ground: #141a17;
    }
  }
  @media (max-width: 1040px) {
    .search {
      inline-size: min(360px, 36vw);
    }
  }
  @media (forced-colors: active) {
    .field:focus {
      outline: 2px solid Highlight;
      outline-offset: 1px;
    }
    .wc.close:hover {
      background: Highlight;
      color: HighlightText;
    }
  }
</style>
