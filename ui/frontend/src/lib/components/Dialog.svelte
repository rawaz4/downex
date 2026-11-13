<script lang="ts">
  // A modal dialog (DESIGN-SYSTEM.md §3.9): --raised, radius 14, over the scrim. The header
  // holds an optional icon, the title, an optional aside (the Add mode control) and the close
  // button; the body scrolls; the footer (on --surface-2) holds an optional summary at the start
  // and the buttons at the end, the primary last. Open and close animate; the focus is trapped.
  import { onDestroy, type Snippet } from 'svelte';
  import Icon from './Icon.svelte';
  import { trapFocus } from '../actions/focus-trap';

  interface Props {
    title: string;
    onclose: () => void;
    /** Rendered inside a <form>; submitting calls this (Enter in a field, or a submit button). */
    onsubmit?: () => void;
    children: Snippet;
    footer?: Snippet;
    description?: string;
    role?: 'dialog' | 'alertdialog';
    /** A CSS width; wins over `size`. */
    width?: string;
    /** Blocks Escape/close while an operation is in flight. */
    busy?: boolean;
    /** Where focus returns when the opener no longer exists. */
    fallbackFocus?: () => HTMLElement | null | undefined;
    /** Focus the dialog itself at first instead of its first field. */
    focusDialog?: boolean;
    /** Before the title: a file tile or a glyph. */
    icon?: Snippet;
    /** After the title: a control that belongs to the header (the Add dialog's mode). */
    headerAside?: Snippet;
    /** The footer's summary at the start (12.5 --muted, <b> in --ink). */
    footerStart?: Snippet;
    /** sm 26rem, md 38rem, lg 48rem, xl 60rem (default 32rem); never wider than the window less 32 px. */
    size?: 'sm' | 'md' | 'lg' | 'xl';
    /**
     * A height that switching pages never lowers: true = 46rem, or a CSS length (in rem, so that
     * it grows with the text). At most the room the window has; a longer page makes the dialog
     * taller, up to that room, with its top where it was (the navigation does not move).
     */
    fixedHeight?: boolean | string;
    /** No body padding or gap: the content brings its own layout (Settings' side navigation). */
    flush?: boolean;
    /** An accessible name that differs from the visible title (the Add dialog shows "Add"). */
    ariaLabel?: string;
  }

  let {
    title,
    onclose,
    onsubmit,
    children,
    footer,
    description,
    role = 'dialog',
    width,
    busy = false,
    fallbackFocus,
    focusDialog = false,
    icon,
    headerAside,
    footerStart,
    size,
    fixedHeight = false,
    flush = false,
    ariaLabel,
  }: Props = $props();

  const SIZES = { sm: '26rem', md: '38rem', lg: '48rem', xl: '60rem' } as const;
  /** The height a dialog has in the window: under the title bar, less the room around it. */
  const ROOM = 'calc(100vh - var(--modal-top, 0px) - 2 * var(--dialog-gap, 16px))';

  const uid = $props.id();
  const titleId = `${uid}-title`;
  const descId = `${uid}-desc`;

  const inline = $derived(width ?? (size ? SIZES[size] : '32rem'));
  /** The least height of a fixed-height dialog, at most the room the window has. */
  const least = $derived(fixedHeight === false ? undefined : fixedHeight === true ? '46rem' : fixedHeight);
  const block = $derived(least === undefined ? undefined : `min(${least}, ${ROOM})`);
  /** With an icon, the description sits under the title beside it (one header block, no extra line). */
  const stacked = $derived(icon !== undefined && !!description);

  let backdropEl = $state<HTMLElement | undefined>();
  let dialogEl = $state<HTMLElement | undefined>();
  /** The dialog plays its closing animation (Escape or ×); onclose follows it. */
  let leaving = $state(false);
  let destroyed = false;
  onDestroy(() => (destroyed = true));

  const CLOSE_MS = 140;

  function reducedMotion(): boolean {
    return typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches;
  }

  function close() {
    if (busy || leaving) return;
    const el = dialogEl;
    if (!el || typeof el.animate !== 'function' || reducedMotion()) {
      onclose();
      return;
    }
    leaving = true;
    const timing: KeyframeAnimationOptions = { duration: CLOSE_MS, easing: 'cubic-bezier(0.4, 0, 1, 1)', fill: 'forwards' };
    backdropEl?.animate([{ opacity: 1 }, { opacity: 0 }], timing);
    const done = () => {
      if (!destroyed) onclose();
    };
    el.animate([{ opacity: 1, scale: '1' }, { opacity: 0, scale: '0.98' }], timing).finished.then(done, done);
  }
</script>

<div class="backdrop" class:leaving class:fixed={block !== undefined} style:--least={block} bind:this={backdropEl}>
  <div
    bind:this={dialogEl}
    class="dialog"
    class:fixed={block !== undefined}
    {role}
    aria-modal="true"
    aria-labelledby={ariaLabel ? undefined : titleId}
    aria-label={ariaLabel}
    aria-describedby={description ? descId : undefined}
    aria-busy={busy}
    tabindex="-1"
    style:inline-size="min({inline}, calc(100vw - 32px))"
    style:min-block-size={block}
    use:trapFocus={{ onEscape: close, fallback: fallbackFocus, focusSelf: focusDialog }}
  >
    <header class="head" class:stacked>
      {#if icon}<span class="head-icon">{@render icon()}</span>{/if}
      {#if stacked}
        <!-- beside the icon: the title (and its aside) over the description, as one block -->
        <div class="titles">
          <div class="title-row">
            <h2 id={titleId}>{title}</h2>
            {#if headerAside}<div class="aside">{@render headerAside()}</div>{/if}
          </div>
          <p id={descId} class="desc">{description}</p>
        </div>
      {:else}
        <h2 id={titleId}>{title}</h2>
        {#if headerAside}<div class="aside">{@render headerAside()}</div>{/if}
      {/if}
      <button type="button" class="icon-btn close" aria-label="Close" title="Close (Esc)" onclick={close} disabled={busy}>
        <Icon name="close" size={16} />
      </button>
    </header>
    {#if description && !stacked}
      <p id={descId} class="desc">{description}</p>
    {/if}
    <form
      novalidate
      onsubmit={(e) => {
        e.preventDefault();
        if (!busy) onsubmit?.();
      }}
    >
      <div class="body" class:flush>
        {@render children()}
      </div>
      {#if footer}
        <footer class="foot">
          {#if footerStart}<div class="summary">{@render footerStart()}</div>{/if}
          {@render footer()}
        </footer>
      {/if}
    </form>
  </div>
</div>

<style>
  /* Below a frameless window's title bar (--modal-top, set by TitleBar), which stays usable
     above the scrim as the window's caption. */
  .backdrop {
    position: fixed;
    inset: 0;
    /* the room around a dialog: 16 px, 8 px in a short window (900×600, 125 % scaling), where
       every line of the dialog counts */
    --dialog-gap: var(--sp-7);
    inset-block-start: var(--modal-top, 0px);
    z-index: 40;
    display: grid;
    place-items: center;
    padding: var(--dialog-gap) var(--sp-7);
    background: var(--scrim);
    animation: fade var(--dur-4) var(--ease-out);
  }
  @media (prefers-reduced-transparency: no-preference) {
    .backdrop {
      backdrop-filter: blur(1.5px);
    }
  }
  /* A height query on the backdrop, not the content: a container cannot see the height it has. */
  @media (max-height: 640px) {
    .backdrop {
      --dialog-gap: var(--sp-4);
    }
  }
  .backdrop.leaving {
    pointer-events: none;
  }
  /* A dialog of a fixed height (Settings, Queues) grows with a longer page, up to the window's
     room, without moving its top: the space above it is where a dialog of its least height is
     centred, and gives way (to 0) only when the dialog needs the room above it. In a flex column:
     the space above may shrink (and it alone does), the space below takes what is left. */
  .backdrop.fixed {
    display: flex;
    flex-direction: column;
    align-items: center;
  }
  .backdrop.fixed::before {
    content: '';
    flex: 0 1 calc((100vh - var(--modal-top, 0px) - 2 * var(--dialog-gap) - var(--least)) / 2);
  }
  .backdrop.fixed::after {
    content: '';
    flex: 1 1 0;
  }
  .backdrop.fixed > .dialog {
    flex: none;
  }
  /* The dialog is a size container named "dialog": its content adapts to the dialog's width
     (@container dialog (…)), never to the window's, which cannot be narrower than 900 px.
     (A container is no containing block for fixed popovers, but the backdrop's blur is: Select
     places its list against that box, which starts under the title bar.) */
  .dialog {
    container: dialog / inline-size;
    display: flex;
    flex-direction: column;
    max-block-size: calc(100vh - var(--modal-top, 0px) - 2 * var(--dialog-gap));
    overflow: hidden;
    border: 1px solid var(--line);
    border-radius: var(--r-2xl);
    background: var(--raised);
    box-shadow: var(--shadow-lg);
    transform-origin: 50% 40%;
    animation: open var(--dur-4) var(--ease-out);
  }
  .dialog:focus {
    outline: none;
  }
  .head {
    display: flex;
    flex: none;
    align-items: center;
    gap: var(--sp-6);
    min-block-size: 46px;
    padding-block: var(--sp-7) 0;
    padding-inline: var(--sp-8) var(--sp-7);
  }
  .head-icon {
    display: grid;
    flex: none;
  }
  h2 {
    min-inline-size: 0;
    font: 650 var(--type-dialog) / 1.25 var(--font-display);
    letter-spacing: -0.01em;
    color: var(--ink);
    overflow-wrap: anywhere;
  }
  .aside {
    display: flex;
    flex: 0 1 auto;
    align-items: center;
    min-inline-size: 0;
  }
  .close {
    margin-inline-start: auto;
  }
  .head.stacked {
    align-items: flex-start;
  }
  .head.stacked .close {
    align-self: flex-start;
    margin-block-start: -2px;
  }
  .titles {
    display: grid;
    flex: 1 1 auto;
    gap: 2px;
    min-inline-size: 0;
  }
  .title-row {
    display: flex;
    align-items: center;
    gap: var(--sp-6);
    min-inline-size: 0;
  }
  .titles .desc {
    padding: 0;
  }
  .desc {
    flex: none;
    padding-block: var(--sp-3) 0;
    padding-inline: var(--sp-8);
    color: var(--muted);
  }
  form {
    display: flex;
    flex: 1 1 auto;
    flex-direction: column;
    min-block-size: 0;
  }
  .body {
    display: grid;
    flex: 1 1 auto;
    align-content: start;
    gap: var(--sp-7);
    min-block-size: 0;
    overflow: auto;
    padding-block: var(--sp-7);
    padding-inline: var(--sp-8);
  }
  .body.flush {
    gap: 0;
    padding: 0;
  }
  .foot {
    display: flex;
    flex: none;
    align-items: center;
    justify-content: flex-end;
    gap: var(--sp-4);
    padding-block: var(--sp-6);
    padding-inline: var(--sp-8) var(--sp-7);
    border-block-start: 1px solid var(--line);
    background: var(--surface-2);
  }
  .foot :global(.btn:not(.sm, .link)) {
    block-size: 32px;
    min-block-size: 32px;
  }
  .summary {
    min-inline-size: 0;
    margin-inline-end: auto;
    font-size: var(--type-cell);
    color: var(--muted);
  }
  .summary :global(b) {
    font-weight: 600;
    color: var(--ink);
  }
  @keyframes fade {
    from {
      opacity: 0;
    }
  }
  @keyframes open {
    from {
      opacity: 0;
      scale: 0.98;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .backdrop,
    .dialog {
      animation: none;
    }
  }
  @media (forced-colors: active) {
    .backdrop {
      backdrop-filter: none;
    }
    .dialog {
      border-color: CanvasText;
    }
  }
</style>
