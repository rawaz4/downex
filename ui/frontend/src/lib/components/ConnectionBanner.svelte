<script lang="ts">
  import Icon from './Icon.svelte';
  import { STATUS_INTERVAL_MS, type DaemonState } from '../stores/downloads.svelte';
  import { escapeBidi } from '../text';

  interface Props {
    daemon: DaemonState | null;
    /** List() failed (the first load, or the reload after a reconnect). */
    loadError: string | null;
    /** A status check is running. */
    checking: boolean;
    /** When the next automatic check runs (epoch ms; 0 = not scheduled). */
    nextCheckAt: number;
    onretry: () => void;
    now?: () => number;
  }

  let { daemon, loadError, checking, nextCheckAt, onretry, now = Date.now }: Props = $props();

  const uid = $props.id();
  const daemonDown = $derived(daemon !== null && !daemon.ok);
  const down = $derived(daemonDown || loadError !== null);
  const error = $derived(daemon !== null && !daemon.ok ? daemon.error : (loadError ?? ''));
  const title = $derived(
    daemonDown ? 'Downex can’t reach downexd, its download service' : 'Downex couldn’t load your downloads from downexd',
  );
  const every = Math.round(STATUS_INTERVAL_MS / 1000);

  // A 1 Hz clock for the countdown, running only while the banner is shown.
  let clock = $state(0);
  $effect(() => {
    if (!down) return;
    clock = now();
    const timer = setInterval(() => (clock = now()), 1000);
    return () => clearInterval(timer);
  });
  const seconds = $derived(nextCheckAt > 0 ? Math.max(0, Math.ceil((nextCheckAt - clock) / 1000)) : 0);

  // Screen readers: one assertive message when the connection is lost, a polite one when
  // it is back. The visible banner (with its ticking countdown) is not a live region.
  let alertText = $state('');
  let statusText = $state('');
  let wasDown = false;
  $effect(() => {
    const isDown = down;
    if (isDown && !wasDown) {
      statusText = '';
      alertText = daemonDown
        ? 'Lost connection to downexd. The list shows the last known state and actions are unavailable until it reconnects.'
        : 'Downex couldn’t load your downloads from downexd.';
    } else if (!isDown && wasDown) {
      alertText = '';
      statusText = 'Reconnected to downexd.';
    }
    wasDown = isDown;
  });
</script>

{#if down}
  <section class="banner" aria-labelledby="{uid}-title">
    <span class="icon" aria-hidden="true"><Icon name="alert" size={18} /></span>
    <div class="body">
      <h2 class="title" id="{uid}-title">{title}</h2>
      <p class="text">
        {#if daemonDown}
          The list shows the last known state, so speeds and times are not current. Adding, pausing and
          removing downloads is unavailable until it reconnects.
        {/if}
        Downex tries again every {every} seconds. If downexd isn’t running, start it with
        <code>downex daemon start</code>.
      </p>
      {#if error}
        <details class="details">
          <summary>Technical details</summary>
          <code class="raw" dir="auto">{escapeBidi(error)}</code>
        </details>
      {/if}
    </div>
    <div class="actions">
      <span class="countdown">{checking ? 'Checking…' : seconds > 0 ? `Next try in ${seconds} s` : ''}</span>
      <button
        type="button"
        class="btn sm"
        aria-disabled={checking ? 'true' : undefined}
        onclick={() => {
          if (!checking) onretry();
        }}
      >
        <Icon name="refresh" size={14} />
        Retry now
      </button>
    </div>
  </section>
{/if}
<div class="sr-only" role="alert">{alertText}</div>
<div class="sr-only" role="status">{statusText}</div>

<style>
  /* A strip across the top of the work card (DESIGN-SYSTEM.md §3.17). */
  .banner {
    display: flex;
    align-items: flex-start;
    gap: 10px;
    padding-block: 10px;
    padding-inline: 20px 14px;
    background: var(--danger-soft);
    border-block-end: 1px solid var(--danger-line);
    color: var(--ink);
  }
  .icon {
    flex: none;
    display: grid;
    place-items: center;
    margin-block-start: 1px;
    color: var(--danger);
  }
  .body {
    flex: 1;
    min-inline-size: 0;
    display: grid;
    gap: 3px;
  }
  .title {
    margin: 0;
    font-size: var(--type-body);
    font-weight: 600;
    line-height: 20px;
  }
  .text {
    font-size: var(--type-cell);
    color: var(--ink-2);
  }
  code {
    font-family: var(--font-mono);
    font-weight: var(--mono-weight);
    font-size: var(--type-small);
  }
  .details summary {
    inline-size: fit-content;
    cursor: pointer;
    color: var(--muted);
    font-size: var(--type-small);
  }
  .raw {
    display: block;
    margin-block-start: var(--sp-2);
    padding-block: var(--sp-2);
    padding-inline: var(--sp-4);
    border: 1px solid var(--line);
    border-radius: var(--r-sm);
    background: var(--surface);
    overflow-wrap: anywhere;
    user-select: text;
  }
  .actions {
    display: flex;
    align-items: center;
    gap: 10px;
    flex: none;
  }
  .countdown {
    color: var(--muted);
    font-size: var(--type-small);
    font-variant-numeric: tabular-nums;
    min-inline-size: 7.5em;
    text-align: end;
  }
  @container (max-width: 46rem) {
    .banner {
      flex-wrap: wrap;
    }
    .actions {
      inline-size: 100%;
      justify-content: flex-end;
    }
  }
  @media (forced-colors: active) {
    .banner {
      border-block-end-color: CanvasText;
    }
  }
</style>
