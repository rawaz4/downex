<script lang="ts" module>
  /** What browser hand-offs do (Settings.confirmBrowserDownloads), or null when not known. */
  export type BrowserDownloads = 'ask' | 'start' | null;
</script>

<script lang="ts">
  // The filters (DESIGN-SYSTEM.md §4.4): Library (All downloads, the configured categories with
  // their file tiles, Other), Queues (running or the next scheduled start, and what is left to
  // do) and Status, on the window's chrome. The active item is Mint. The footer says honestly
  // what browser hand-offs do (the window cannot know whether the extension is installed) and
  // opens Settings → Integration.
  import Icon from './Icon.svelte';
  import FileIcon from './FileIcon.svelte';
  import StatusMark, { markOfFilter } from './StatusMark.svelte';
  import {
    ALL_LABEL,
    OTHER_LABEL,
    STATUS_FILTERS,
    STATUS_FILTER_LABELS,
    type CategoryFilter,
    type CategoryIndex,
    type FileKind,
    type StatusFilter,
  } from '../categories';
  import type { Queue } from '../api';
  import type { Counts } from '../query';
  import { formatCount } from '../format';
  import { queueNote } from '../queues';
  import { escapeBidi } from '../text';

  interface Props {
    /** The configured categories (Settings.categories). */
    index: CategoryIndex;
    category: CategoryFilter;
    status: StatusFilter;
    counts: Counts;
    oncategory: (c: CategoryFilter) => void;
    onstatus: (s: StatusFilter) => void;
    /** The queues (M6), main queue first; none: the section is not shown. */
    queues?: readonly Queue[];
    /** The selected queue filter (null: every queue). */
    queue?: number | null;
    /** Per queue: its downloads that are not completed. */
    queueCounts?: ReadonlyMap<number, number>;
    onqueue?: (id: number | null) => void;
    /** Opens the queue manager. */
    onmanagequeues?: () => void;
    /** The footer's line; without onintegration there is no footer. */
    browserDownloads?: BrowserDownloads;
    /** Opens Settings → Integration. */
    onintegration?: () => void;
  }

  let {
    index,
    category,
    status,
    counts,
    oncategory,
    onstatus,
    queues = [],
    queue = null,
    queueCounts = new Map(),
    onqueue,
    onmanagequeues,
    browserDownloads = null,
    onintegration,
  }: Props = $props();

  const items = $derived<{ key: CategoryFilter; label: string; kind: FileKind | 'all' }[]>([
    { key: 'all', label: ALL_LABEL, kind: 'all' },
    ...index.entries.map((e) => ({ key: e.key, label: escapeBidi(e.name), kind: e.kind })),
    { key: 'other', label: OTHER_LABEL, kind: 'other' },
  ]);

  const handoff = $derived(
    browserDownloads === 'ask'
      ? 'Browser downloads: ask first'
      : browserDownloads === 'start'
        ? 'Browser downloads: start at once'
        : 'Browser downloads',
  );
</script>

<nav class="sidebar" aria-label="Filters">
  <div class="scroll">
    <h2 class="group" id="sidebar-categories">Library</h2>
    <ul aria-labelledby="sidebar-categories">
      {#each items as c (c.key)}
        <li>
          <button
            type="button"
            class="item"
            class:active={category === c.key}
            aria-pressed={category === c.key}
            title={c.label}
            onclick={() => oncategory(c.key)}
          >
            <span class="glyph">
              {#if c.kind === 'all'}
                <span class="tray"><Icon name="tray" size={16} /></span>
              {:else}
                <FileIcon kind={c.kind} size={16} />
              {/if}
            </span>
            <span class="label" dir="auto">{c.label}</span>
            <span class="count tnum">{formatCount(counts.categories.get(c.key) ?? 0)}</span>
          </button>
        </li>
      {/each}
    </ul>

    {#if queues.length > 0}
      <div class="group-head">
        <h2 class="group" id="sidebar-queues">Queues</h2>
        {#if onmanagequeues}
          <button
            type="button"
            class="icon-btn sm manage"
            aria-label="Manage queues"
            title="Manage queues…"
            onclick={onmanagequeues}
          >
            <Icon name="settings" size={14} />
          </button>
        {/if}
      </div>
      <ul aria-labelledby="sidebar-queues">
        {#each queues as q (q.id)}
          {@const n = queueCounts.get(q.id) ?? 0}
          {@const note = queueNote(q)}
          <li>
            <button
              type="button"
              class="item"
              class:active={queue === q.id}
              class:running={q.running}
              aria-pressed={queue === q.id}
              aria-describedby="sidebar-queue-{q.id}-state"
              title="{escapeBidi(q.name)}: {q.running ? 'running' : 'stopped'}, {n} to do"
              onclick={() => onqueue?.(queue === q.id ? null : q.id)}
            >
              <span class="glyph queue-glyph"><Icon name={q.running || !q.schedule.enabled ? 'queue' : 'clock'} size={16} /></span>
              <span class="label" dir="auto">{escapeBidi(q.name)}</span>
              {#if note}<span class="qnote tnum" aria-hidden="true">{note}</span>{/if}
              <span id="sidebar-queue-{q.id}-state" class="sr-only">{q.running ? 'Running' : 'Stopped'}</span>
              <span class="count tnum">{formatCount(n)}</span>
            </button>
          </li>
        {/each}
      </ul>
    {/if}

    <h2 class="group" id="sidebar-status">Status</h2>
    <ul aria-labelledby="sidebar-status">
      {#each STATUS_FILTERS as s (s)}
        <li>
          <button
            type="button"
            class="item"
            class:active={status === s}
            aria-pressed={status === s}
            title={STATUS_FILTER_LABELS[s]}
            onclick={() => onstatus(status === s ? 'any' : s)}
          >
            <span class="glyph"><StatusMark kind={markOfFilter(s)} size={14} /></span>
            <span class="label">{STATUS_FILTER_LABELS[s]}</span>
            <span class="count tnum">{formatCount(counts.statuses[s])}</span>
          </button>
        </li>
      {/each}
    </ul>
  </div>

  {#if onintegration}
    <div class="foot">
      <button type="button" class="handoff" onclick={onintegration} title="Settings › Integration">
        <span class="dot" class:known={browserDownloads !== null} aria-hidden="true"></span>
        <span class="handoff-text">
          <span class="handoff-title">{handoff}</span>
          <span class="handoff-hint">Change in Settings › Integration</span>
        </span>
      </button>
    </div>
  {/if}
</nav>

<style>
  .sidebar {
    display: flex;
    flex-direction: column;
    min-block-size: 0;
    background: var(--chrome);
  }
  .scroll {
    flex: 1 1 auto;
    min-block-size: 0;
    overflow-y: auto;
    padding-block: 4px 10px;
    padding-inline: 10px;
  }
  .group {
    margin: 0;
    padding-block: 16px 6px;
    padding-inline: 9px;
    font-size: var(--type-caps);
    font-weight: 600;
    letter-spacing: 0.07em;
    text-transform: uppercase;
    color: var(--faint);
  }
  .scroll > .group:first-child {
    padding-block-start: 12px;
  }
  .group-head {
    display: flex;
    align-items: flex-end;
    justify-content: space-between;
  }
  .manage {
    margin-block-end: 2px;
    color: var(--faint);
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: 1px;
  }
  .item {
    display: flex;
    align-items: center;
    gap: 10px;
    inline-size: 100%;
    block-size: 29px;
    padding-block: 0;
    padding-inline: 9px;
    border: 0;
    border-radius: 7px;
    background: transparent;
    color: var(--ink-2);
    font: inherit;
    text-align: start;
    cursor: pointer;
    transition:
      background-color var(--dur-2) ease,
      color var(--dur-2) ease;
  }
  .item:hover {
    background: var(--hover);
    color: var(--ink);
  }
  /* Active: Mint tint with a ring (never shown by tint alone), full-contrast text, Mint icon. */
  .item.active {
    background: var(--sel);
    box-shadow: inset 0 0 0 1px var(--sel-line);
    color: var(--ink);
    font-weight: 600;
  }
  .item.active:hover {
    background: var(--sel-hover);
  }
  .item:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
  }
  .label {
    flex: 1;
    min-inline-size: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .qnote {
    flex: none;
    max-inline-size: 7em;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: var(--type-meta);
    font-weight: 400;
    color: var(--faint);
  }
  .count {
    flex: none;
    min-inline-size: 1.5em;
    text-align: end;
    font-size: var(--type-small);
    font-weight: 400;
    color: var(--faint);
  }
  .item.active .count {
    color: var(--ink-2);
    font-weight: 600;
  }
  .glyph {
    flex: none;
    display: grid;
    place-items: center;
    inline-size: 18px;
    color: var(--muted);
  }
  .tray {
    display: grid;
    color: var(--accent);
  }
  .queue-glyph {
    color: var(--muted);
  }
  .running .queue-glyph,
  .item.active .queue-glyph {
    color: var(--accent);
  }

  /* ---- footer: what browser hand-offs do ---- */
  .foot {
    flex: none;
    padding: 8px 10px 10px;
    border-block-start: 1px solid var(--line);
  }
  .handoff {
    display: flex;
    align-items: flex-start;
    gap: 9px;
    inline-size: 100%;
    padding: 7px 9px;
    border: 0;
    border-radius: 7px;
    background: transparent;
    color: var(--ink);
    font: inherit;
    text-align: start;
    cursor: pointer;
    transition: background-color var(--dur-2) ease;
  }
  .handoff:hover {
    background: var(--hover);
  }
  .handoff:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
  }
  .handoff .dot {
    margin-block-start: 6px;
    background: var(--seg-idle);
  }
  .handoff .dot.known {
    background: var(--accent);
  }
  .handoff-text {
    display: grid;
    gap: 1px;
    min-inline-size: 0;
  }
  .handoff-title {
    font-size: var(--type-cell);
    font-weight: 600;
  }
  .handoff-hint {
    font-size: var(--type-meta);
    color: var(--faint);
  }

  /* A narrow window (721–999 px): a 56 px rail of glyphs, each count a small badge and each
     name in its tooltip and accessible name, so the Name column keeps its room. */
  @media (min-width: 721px) and (max-width: 999px) {
    .scroll {
      padding-inline: 8px;
    }
    .group,
    .label,
    .qnote,
    .handoff-text {
      position: absolute;
      inline-size: 1px;
      block-size: 1px;
      overflow: hidden;
      clip-path: inset(50%);
      white-space: nowrap;
    }
    ul:not(:last-child) {
      padding-block-end: 6px;
      margin-block-end: 6px;
      border-block-end: 1px solid var(--line);
    }
    .group-head {
      justify-content: center;
    }
    .scroll > .group:first-child + ul {
      margin-block-start: 8px;
    }
    .item {
      position: relative;
      justify-content: center;
      block-size: 34px;
      padding-inline: 0;
    }
    .count {
      position: absolute;
      inset-block-start: 1px;
      inset-inline-end: 1px;
      min-inline-size: 15px;
      block-size: 14px;
      padding-inline: 3px;
      border-radius: var(--r-pill);
      background: var(--chrome);
      font-size: 0.625rem;
      line-height: 14px;
      text-align: center;
    }
    .item.active .count {
      background: var(--sel);
    }
    .foot {
      padding: 8px;
    }
    .handoff {
      justify-content: center;
      padding: 9px 0;
    }
    .handoff .dot {
      margin-block-start: 0;
    }
  }
  @media (max-width: 720px) {
    .sidebar {
      flex-direction: row;
    }
    .scroll {
      display: flex;
      gap: var(--sp-6);
      overflow-x: auto;
      overflow-y: hidden;
      padding-block: var(--sp-2);
    }
    .group,
    .group-head,
    .foot {
      position: absolute;
      inline-size: 1px;
      block-size: 1px;
      overflow: hidden;
      clip-path: inset(50%);
    }
    ul {
      display: flex;
      gap: var(--sp-1);
    }
    .item {
      white-space: nowrap;
    }
  }
  @media (forced-colors: active) {
    .item.active {
      outline: 2px solid Highlight;
      outline-offset: -2px;
    }
  }
</style>
