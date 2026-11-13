<script lang="ts">
  import { onMount, tick } from 'svelte';
  import { canRestoreFocus } from '../actions/focus-trap';
  import { toApiError, type PostActionPending } from '../api';
  import { POST_ACTION_PROGRESS, secondsLeft } from '../queues';
  import { escapeBidi } from '../text';
  import Icon from './Icon.svelte';

  interface Props {
    /** The countdown of a queue's post-queue action (M6-SPEC §1). */
    pending: PostActionPending;
    /** The name of the queue that finished. */
    queueName: string;
    /** Cancels the action; rejects with the daemon's error. */
    oncancel: () => Promise<void>;
    now?: () => number;
    /**
     * Takes the keyboard focus when it appears, also from an open dialog (whose focus trap
     * lets it keep it: the banner is a TRAP_EXEMPT region).
     */
    focus?: boolean;
    /** Where focus goes when the banner leaves with it, and neither where it came from nor an open dialog can take it. */
    fallbackFocus?: () => HTMLElement | null | undefined;
  }

  let { pending, queueName, oncancel, now = Date.now, focus = false, fallbackFocus }: Props = $props();

  // svelte-ignore state_referenced_locally
  let t = $state(now());
  // svelte-ignore state_referenced_locally
  const announced = pending.deadline > 0 ? secondsLeft(pending.deadline, t) : 60;
  // svelte-ignore state_referenced_locally
  const announcedVerb = POST_ACTION_PROGRESS[pending.action];
  let busy = $state(false);
  let failure = $state<string | null>(null);
  let button: HTMLButtonElement | undefined = $state();

  const left = $derived(pending.deadline > 0 ? secondsLeft(pending.deadline, t) : null);
  const verb = $derived(POST_ACTION_PROGRESS[pending.action]);
  const text = $derived(left === null ? `${verb} soon` : left > 0 ? `${verb} in ${left} s` : `${verb} now…`);

  onMount(() => {
    const timer = setInterval(() => (t = now()), 250);
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    if (focus) void tick().then(() => button?.focus());
    return () => {
      clearInterval(timer);
      // Leaving with the focus (it fell back to the page): it goes back where it came from,
      // else to the open dialog itself (Tab then enters it), else to the fallback.
      queueMicrotask(() => {
        const active = document.activeElement;
        if (active !== null && active !== document.body) return;
        const modal = document.querySelector<HTMLElement>('[aria-modal="true"]');
        const target = canRestoreFocus(previous) && (!modal || modal.contains(previous)) ? previous : (modal ?? fallbackFocus?.());
        target?.focus();
      });
    };
  });

  async function cancel() {
    if (busy) return;
    busy = true;
    failure = null;
    try {
      await oncancel();
    } catch (err) {
      failure = toApiError(err).message;
    } finally {
      busy = false;
    }
  }

  function onkeydown(e: KeyboardEvent) {
    // Escape on the countdown cancels it (not an open dialog behind it).
    if (e.key !== 'Escape') return;
    e.preventDefault();
    e.stopPropagation();
    void cancel();
  }
</script>

<section class="banner" aria-labelledby="post-action-title" data-focus-trap-exempt>
  <span class="icon" aria-hidden="true"><Icon name="power" size={16} /></span>
  <!-- Announced once when the countdown starts; the seconds are not read out every second. -->
  <p class="sr-only" role="alert">
    {announcedVerb} in about {announced} seconds, because the queue {escapeBidi(queueName)} finished. Cancel (or Escape) to stop it.
  </p>
  <h2 id="post-action-title" class="title" aria-live="off">{text}</h2>
  <p class="detail"><bdi>{escapeBidi(queueName)}</bdi> finished its downloads.</p>
  {#if failure}
    <p class="error-text"><bdi>{escapeBidi(failure)}</bdi></p>
  {/if}
  <button
    bind:this={button}
    type="button"
    class="btn sm"
    onclick={() => void cancel()}
    {onkeydown}
    disabled={busy}
    aria-keyshortcuts="Escape"
  >
    {busy ? 'Cancelling…' : 'Cancel'}
  </button>
</section>

<style>
  /* A 40 px strip at the very top of the window, above the title bar (DESIGN-SYSTEM.md §3.17).
     Above a modal dialog's scrim (z-index 40; .window makes no stacking context) and below the
     menus (50 is shared with ContextMenu, which never overlaps it) and toasts (60): its Cancel
     stops a sleep or shut down, so it stays clickable while a dialog is open. */
  .banner {
    position: relative;
    z-index: 50;
    display: flex;
    align-items: center;
    gap: 10px;
    flex: none;
    min-block-size: 40px;
    padding-block: 4px;
    padding-inline: 14px 10px;
    background: var(--warn-soft);
    border-block-end: 1px solid var(--warn-line);
    color: var(--ink);
  }
  .icon {
    flex: none;
    display: grid;
    color: var(--warn);
  }
  .title {
    flex: none;
    margin: 0;
    font-size: var(--type-body);
    font-weight: 600;
    font-variant-numeric: tabular-nums;
    color: var(--warn);
  }
  .detail {
    flex: 1;
    min-inline-size: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--ink-2);
    font-size: var(--type-cell);
  }
  .error-text {
    flex: none;
    max-inline-size: 40%;
    overflow-wrap: anywhere;
  }
  .btn {
    flex: none;
  }
  @media (forced-colors: active) {
    .banner {
      border-block-end-color: CanvasText;
    }
  }
</style>
