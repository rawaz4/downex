<script lang="ts" module>
  import { motionOk } from '../motion.svelte';
  import { EXIT_MS } from '../stores/toasts.svelte';

  export interface NoticeAction {
    readonly label: string;
    readonly primary?: boolean;
    readonly run: () => void;
  }

  /** A toast with actions (a download that arrived from the browser, or awaits confirmation). */
  export interface Notice {
    readonly key: string;
    readonly title: string;
    /** The download's name (shown isolated, on its own line). */
    readonly detail?: string;
    readonly actions: readonly NoticeAction[];
  }

  const NOTICE_MS = 9000;
  const MAX_NOTICES = 3;

  /** Notices with a lifetime that waits while the pointer or the focus is on them. */
  export class Notices {
    items = $state.raw<readonly Notice[]>([]);
    /** Notices fading out (still drawn, hidden from assistive technology). */
    leaving = $state.raw<ReadonlySet<string>>(new Set());
    #timers = new Map<string, ReturnType<typeof setTimeout>>();
    /** Per notice: when it goes, or (while held) how long it has left. */
    #deadline = new Map<string, number>();
    #left = new Map<string, number>();

    show(notice: Notice): void {
      this.#remove(notice.key);
      const kept = [...this.items, notice];
      const live = kept.filter((n) => !this.leaving.has(n.key));
      const drop = new Set(live.slice(0, Math.max(0, live.length - MAX_NOTICES)).map((n) => n.key));
      for (const key of drop) this.#forget(key);
      this.items = kept.filter((n) => !drop.has(n.key));
      this.#arm(notice.key);
    }

    dismiss(key: string): void {
      if (this.leaving.has(key) || !this.items.some((n) => n.key === key)) {
        this.#forget(key);
        return;
      }
      this.#forget(key);
      if (!motionOk()) {
        this.#remove(key);
        return;
      }
      this.leaving = new Set([...this.leaving, key]);
      this.#timers.set(
        key,
        setTimeout(() => this.#remove(key), EXIT_MS),
      );
    }

    hold(key: string): void {
      if (!this.#timers.has(key) || this.leaving.has(key)) return;
      clearTimeout(this.#timers.get(key));
      this.#timers.delete(key);
      this.#left.set(key, Math.max(0, (this.#deadline.get(key) ?? 0) - Date.now()));
    }

    release(key: string): void {
      const left = this.#left.get(key);
      if (left === undefined || this.#timers.has(key)) return;
      this.#left.delete(key);
      this.#arm(key, left);
    }

    clear(): void {
      for (const t of this.#timers.values()) clearTimeout(t);
      this.#timers.clear();
      this.#deadline.clear();
      this.#left.clear();
      this.items = [];
      this.leaving = new Set();
    }

    /** Stops a notice's timer. */
    #forget(key: string): void {
      clearTimeout(this.#timers.get(key));
      this.#timers.delete(key);
      this.#deadline.delete(key);
      this.#left.delete(key);
    }

    /** Takes a notice away at once. */
    #remove(key: string): void {
      this.#forget(key);
      if (this.leaving.has(key)) {
        const next = new Set(this.leaving);
        next.delete(key);
        this.leaving = next;
      }
      if (this.items.some((n) => n.key === key)) this.items = this.items.filter((n) => n.key !== key);
    }

    #arm(key: string, ms = NOTICE_MS): void {
      this.#deadline.set(key, Date.now() + ms);
      this.#timers.set(
        key,
        setTimeout(() => this.dismiss(key), ms),
      );
    }
  }
</script>

<script lang="ts">
  // Toasts (DESIGN-SYSTEM.md §3.17): bottom right, 16 px above the status bar, the short toasts
  // of the store with at most three notices with actions on top of them. Each has a tone icon (a Mint check
  // for something done, info, danger), its text and its actions (primary last). They rise in
  // and fade out; a notice waits while the pointer or the focus is on it.
  import Icon from './Icon.svelte';
  import { toasts } from '../stores/toasts.svelte';

  interface Props {
    notices?: Notices;
    /**
     * A modal dialog is open: the plain toasts sit under its scrim, so they never cover the
     * dialog's buttons (they are still announced, and come back to the front when the dialog
     * closes). Notices stay above it: they carry actions and mostly appear because a dialog is in
     * the way (a browser download to confirm), so they must stay usable; Tab reaches them too.
     */
    behind?: boolean;
  }

  let { notices, behind = false }: Props = $props();

  /** The plain stack's height: the notices sit on top of it. */
  let plainHeight = $state(0);

  const TONE_ICON = { success: 'check', info: 'info', error: 'alert' } as const;
</script>

<!-- Two stacks at the bottom right: the plain toasts, and above them the notices. -->
<div class="toasts plain-stack" class:behind role="status" aria-live="polite" bind:clientHeight={plainHeight}>
  {#each toasts.items as t (t.id)}
    <div class="toast plain tone-{t.tone}" class:leaving={t.leaving} aria-hidden={t.leaving ? 'true' : undefined} inert={t.leaving}>
      <span class="tone" aria-hidden="true"><Icon name={TONE_ICON[t.tone]} size={15} /></span>
      <span class="msg">{t.message}</span>
      <button type="button" class="icon-btn sm dismiss" aria-label="Dismiss" title="Dismiss" onclick={() => toasts.dismiss(t.id)}>
        <Icon name="close" size={14} />
      </button>
    </div>
  {/each}
</div>

<div
  class="toasts notice-stack"
  role="status"
  aria-live="polite"
  data-focus-trap-exempt="tab"
  style:inset-block-end={plainHeight > 0 ? `calc(var(--statusbar-h) + 16px + ${plainHeight + 8}px)` : undefined}
>
  {#if notices}
    {#each notices.items as n (n.key)}
      {@const leaving = notices.leaving.has(n.key)}
      <div
        class="toast notice"
        class:leaving
        role="group"
        aria-label={n.title}
        aria-hidden={leaving ? 'true' : undefined}
        inert={leaving}
        onpointerenter={() => notices.hold(n.key)}
        onpointerleave={() => notices.release(n.key)}
        onfocusin={() => notices.hold(n.key)}
        onfocusout={() => notices.release(n.key)}
      >
        <span class="tone tone-notice" aria-hidden="true"><Icon name="arrow-down" size={15} /></span>
        <div class="body">
          <p class="title">{n.title}</p>
          {#if n.detail}<p class="detail"><bdi>{n.detail}</bdi></p>{/if}
          <div class="actions">
            {#each n.actions as a (a.label)}
              <button
                type="button"
                class="btn sm"
                class:primary={a.primary}
                onclick={() => {
                  notices.dismiss(n.key);
                  a.run();
                }}
              >
                {a.label}
              </button>
            {/each}
          </div>
        </div>
        <span class="timer" aria-hidden="true"></span>
      </div>
    {/each}
  {/if}
</div>

<style>
  .toasts {
    position: fixed;
    inset-block-end: calc(var(--statusbar-h) + 16px);
    inset-inline-end: 16px;
    z-index: 60;
    display: grid;
    gap: 8px;
    inline-size: min(360px, calc(100vw - 32px));
    justify-items: stretch;
    pointer-events: none;
  }
  /* under the dialog backdrop (z-index 40) */
  .toasts.behind {
    z-index: 30;
  }
  .notice-stack {
    transition: inset-block-end var(--dur-3) var(--ease-out);
  }
  @media (prefers-reduced-motion: reduce) {
    .notice-stack {
      transition: none;
    }
  }
  .toast {
    position: relative;
    display: flex;
    align-items: flex-start;
    gap: 10px;
    padding: 10px 6px 10px 12px;
    border: 1px solid var(--line);
    border-radius: 11px;
    background: var(--raised);
    box-shadow: var(--shadow-lg);
    color: var(--ink);
    overflow: hidden;
    pointer-events: auto;
    animation: rise var(--dur-4) var(--ease-out);
  }
  .toast.leaving {
    animation: leave var(--dur-exit) var(--ease-in) forwards;
  }
  .tone {
    flex: none;
    display: grid;
    place-items: center;
    inline-size: 22px;
    block-size: 22px;
    border-radius: 50%;
    background: var(--accent-soft);
    color: var(--accent-text);
  }
  .plain .tone {
    margin-block-start: 1px;
  }
  .tone-info .tone {
    background: var(--info-soft);
    color: var(--info);
  }
  .tone-error .tone {
    background: var(--danger-soft);
    color: var(--danger);
  }
  .tone-error {
    border-color: var(--danger-line);
  }
  .msg {
    flex: 1;
    min-inline-size: 0;
    padding-block: 3px;
    font-size: var(--type-body);
    line-height: 1.35;
    overflow-wrap: anywhere;
  }
  .dismiss {
    color: var(--faint);
  }
  .notice {
    padding: 12px;
  }
  .body {
    flex: 1;
    display: grid;
    gap: 2px;
    min-inline-size: 0;
  }
  .title {
    font-size: var(--type-body);
    font-weight: 600;
    line-height: 22px;
  }
  .detail {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: var(--type-small);
    color: var(--muted);
  }
  .actions {
    display: flex;
    justify-content: flex-end;
    gap: 6px;
    margin-block-start: 8px;
  }
  /* the time left before it goes away by itself (it waits while hovered or focused) */
  .timer {
    position: absolute;
    inset-inline: 0;
    inset-block-start: 0;
    block-size: 2px;
    background: var(--accent);
    transform-origin: 0 50%;
    animation: timer 9s linear forwards;
  }
  :global([dir='rtl']) .timer {
    transform-origin: 100% 50%;
  }
  .notice:hover .timer,
  .notice:focus-within .timer {
    animation-play-state: paused;
  }
  @keyframes rise {
    from {
      opacity: 0;
      translate: 0 8px;
    }
  }
  @keyframes leave {
    to {
      opacity: 0;
      translate: 0 4px;
    }
  }
  @keyframes timer {
    to {
      transform: scaleX(0);
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .timer {
      display: none;
    }
  }
  @media (forced-colors: active) {
    .toast {
      border-color: CanvasText;
    }
  }
</style>
