<script lang="ts" module>
  /** What the empty list says (DESIGN-SYSTEM.md §3.18). */
  export type EmptyKind = 'loading' | 'unreachable' | 'none' | 'filtered';
</script>

<script lang="ts">
  // The list area when it has no rows: while the first read runs, while downexd cannot be
  // reached, with no downloads at all (how to add one, and where browser downloads come
  // from), or with filters that match nothing (what matched nothing, and a way back).
  import Icon from './Icon.svelte';

  interface Props {
    kind: EmptyKind;
    /** Filtered: "No downloads match “query”" or "Nothing in Video". */
    title?: string;
    /** The daemon is unreachable: actions that change downloads are unavailable. */
    stale?: boolean;
    onadd?: () => void;
    onaddvideo?: () => void;
    /** Opens Settings → Integration (the extension and the relay). */
    onextension?: () => void;
    onclearfilters?: () => void;
  }

  let { kind, title = '', stale = false, onadd, onaddvideo, onextension, onclearfilters }: Props = $props();
</script>

<div class="empty kind-{kind}">
  {#if kind === 'loading'}
    <p class="quiet">Loading downloads…</p>
  {:else if kind === 'unreachable'}
    <p class="quiet">Your downloads appear here once Downex reaches downexd.</p>
  {:else if kind === 'none'}
    <div class="glyph" aria-hidden="true"><Icon name="tray" size={40} /></div>
    <h2 class="title">No downloads yet</h2>
    <p class="text">Paste a link anywhere in this window (Ctrl+V), drop one here, or add one.</p>
    {#if onadd || onaddvideo}
      <div class="buttons">
        {#if onadd}
          <button type="button" class="btn primary" disabled={stale} onclick={onadd}>
            <Icon name="add" size={16} />Add download
          </button>
        {/if}
        {#if onaddvideo}
          <button type="button" class="btn" disabled={stale} onclick={onaddvideo}>
            <Icon name="play" size={14} />Add video or stream
          </button>
        {/if}
      </div>
    {/if}
    <p class="extension">
      Downloads from Chrome and Edge arrive here with the Downex extension.
      {#if onextension}
        <button type="button" class="btn link" onclick={onextension}>Get the browser extension</button>
      {/if}
    </p>
  {:else}
    <div class="glyph quiet-glyph" aria-hidden="true"><Icon name="filter" size={26} /></div>
    <h2 class="title"><bdi>{title || 'No downloads match the current filters'}</bdi></h2>
    <p class="text">The filters hide every download.</p>
    {#if onclearfilters}
      <div class="buttons">
        <button type="button" class="btn" onclick={onclearfilters}>Clear filters</button>
      </div>
    {/if}
  {/if}
</div>

<style>
  .empty {
    display: grid;
    justify-items: center;
    align-content: center;
    gap: 10px;
    max-inline-size: 420px;
    margin-inline: auto;
    padding: var(--sp-9) var(--sp-7);
    text-align: center;
  }
  .glyph {
    display: grid;
    place-items: center;
    inline-size: 72px;
    block-size: 72px;
    margin-block-end: 6px;
    border-radius: 50%;
    background: var(--accent-soft);
    color: var(--accent-text);
  }
  .quiet-glyph {
    inline-size: 56px;
    block-size: 56px;
    background: var(--surface-2);
    border: 1px solid var(--line);
    color: var(--muted);
  }
  .title {
    font: 650 var(--type-section) / 1.25 var(--font-display);
    letter-spacing: -0.01em;
    color: var(--ink);
  }
  .text,
  .quiet,
  .extension {
    text-wrap: balance;
  }
  .text,
  .quiet {
    color: var(--muted);
    font-size: var(--type-body);
    line-height: 1.45;
  }
  .buttons {
    display: flex;
    flex-wrap: wrap;
    justify-content: center;
    gap: 8px;
    margin-block-start: 6px;
  }
  .extension {
    margin-block-start: 10px;
    padding-block-start: 14px;
    border-block-start: 1px solid var(--line-soft);
    color: var(--faint);
    font-size: var(--type-small);
    line-height: 1.5;
  }
  .extension .btn.link {
    font-size: var(--type-small);
  }
</style>
