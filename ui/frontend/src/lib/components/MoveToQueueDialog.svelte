<script lang="ts">
  // Move to queue (DESIGN-SYSTEM.md §4.10): the queues as selectable rows, each a label around a
  // visually hidden native radio named after its queue (arrow keys move between them), with the
  // queue's icon, its state and a Mint check on the chosen one.
  import { toApiError, type Queue } from '../api';
  import { escapeBidi } from '../text';
  import Dialog from './Dialog.svelte';
  import Icon from './Icon.svelte';

  interface Props {
    /** Names of the downloads to move (display text). */
    names: readonly string[];
    queues: readonly Queue[];
    /** The queue to propose (the one the downloads are in when they share one). */
    current?: number | null;
    /** Moves the downloads to the end of the chosen queue; rejects with the daemon's error. */
    onmove: (queueId: number) => Promise<void>;
    onclose: () => void;
    fallbackFocus?: () => HTMLElement | null | undefined;
  }

  let { names, queues, current = null, onmove, onclose, fallbackFocus }: Props = $props();

  const uid = $props.id();
  // svelte-ignore state_referenced_locally
  let choice = $state<number>(queues.find((q) => q.id !== current)?.id ?? queues[0]?.id ?? 0);
  let busy = $state(false);
  let failure = $state<string | null>(null);

  const what = $derived(names.length === 1 ? escapeBidi(names[0] ?? '') : `${names.length} downloads`);

  /** What a row says beside the name: where the downloads are now, else the queue's state. */
  function noteOf(q: Queue): string {
    if (q.id === current) return '(now)';
    return q.running ? 'running' : 'stopped';
  }

  async function onsubmit() {
    if (busy || choice <= 0) return;
    busy = true;
    failure = null;
    try {
      await onmove(choice);
    } catch (err) {
      failure = toApiError(err).message;
      busy = false;
      return;
    }
    busy = false;
    onclose();
  }
</script>

<Dialog title="Move to queue" {onclose} {onsubmit} {busy} {fallbackFocus} size="sm">
  <p class="lead">Move <bdi class="what">{what}</bdi> to the end of:</p>
  <fieldset class="choices">
    <legend class="sr-only">Queue</legend>
    {#each queues as q, i (q.id)}
      <label class="choice" class:on={choice === q.id}>
        <input
          class="sr-only"
          type="radio"
          name="{uid}-queue"
          value={q.id}
          bind:group={choice}
          aria-labelledby="{uid}-q{q.id}-name"
          aria-describedby="{uid}-q{q.id}-note"
          data-autofocus={i === 0 ? '' : undefined}
        />
        <span class="qicon" class:running={q.running}><Icon name="queue" size={16} /></span>
        <bdi class="qname" id="{uid}-q{q.id}-name">{escapeBidi(q.name)}</bdi>
        <span class="qnote" id="{uid}-q{q.id}-note">{noteOf(q)}</span>
        <span class="tick" aria-hidden="true">{#if choice === q.id}<Icon name="check" size={16} />{/if}</span>
      </label>
    {/each}
  </fieldset>
  {#if failure}
    <p class="note fail" role="alert"><bdi>{escapeBidi(failure)}</bdi></p>
  {/if}

  {#snippet footer()}
    <button type="button" class="btn" onclick={onclose} disabled={busy}>Cancel</button>
    <button type="submit" class="btn primary" disabled={busy || choice === current}>{busy ? 'Moving…' : 'Move'}</button>
  {/snippet}
</Dialog>

<style>
  .lead {
    color: var(--ink-2);
  }
  .what {
    color: var(--ink);
    font-weight: 600;
  }
  .choices {
    display: grid;
    gap: 0;
    min-inline-size: 0;
    /* no scroller of its own: a long list grows the dialog to the window first, then the
       dialog's body scrolls (one scroll region, §3.9) */
    margin: 0;
    padding: 4px;
    border: 1px solid var(--line);
    border-radius: var(--r-lg);
    background: var(--surface);
  }
  .choice {
    display: flex;
    align-items: center;
    gap: 10px;
    min-block-size: 34px;
    padding-block: 0;
    padding-inline: 10px 8px;
    border-radius: 7px;
    color: var(--ink-2);
    cursor: pointer;
    transition: background-color var(--dur-2) ease;
  }
  .choice:hover:not(.on) {
    background: var(--hover);
  }
  .choice.on {
    background: var(--sel);
    box-shadow: inset 0 0 0 1px var(--sel-line);
    color: var(--ink);
  }
  .choice:has(input:focus-visible) {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
  }
  .qicon {
    display: grid;
    flex: none;
    color: var(--muted);
  }
  .qicon.running,
  .choice.on .qicon {
    color: var(--accent);
  }
  .qname {
    flex: 1 1 auto;
    min-inline-size: 0;
    overflow: hidden;
    font-weight: 500;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .choice.on .qname {
    font-weight: 600;
  }
  .qnote {
    flex: none;
    color: var(--faint);
    font-size: var(--type-small);
  }
  .tick {
    display: grid;
    flex: none;
    place-items: center;
    inline-size: 16px;
    color: var(--accent);
  }
  @media (prefers-reduced-motion: reduce) {
    .choice {
      transition: none;
    }
  }
  @media (forced-colors: active) {
    .choice.on {
      outline: 2px solid Highlight;
      outline-offset: -2px;
    }
  }
</style>
