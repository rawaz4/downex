<script lang="ts">
  import Dialog from './Dialog.svelte';
  import Icon from './Icon.svelte';

  interface Props {
    names: readonly string[];
    deleteFile?: boolean;
    onconfirm: (deleteFile: boolean) => Promise<void> | void;
    onclose: () => void;
    fallbackFocus?: () => HTMLElement | null | undefined;
  }

  let { names, deleteFile = false, onconfirm, onclose, fallbackFocus }: Props = $props();

  // svelte-ignore state_referenced_locally
  let alsoDelete = $state(deleteFile);
  let busy = $state(false);

  const count = $derived(names.length);
  const title = $derived(count === 1 ? 'Remove download?' : `Remove ${count} downloads?`);
  const shown = $derived(names.slice(0, 5));

  async function onsubmit() {
    busy = true;
    try {
      await onconfirm(alsoDelete);
    } finally {
      busy = false;
    }
    onclose();
  }
</script>

<Dialog
  {title}
  role="alertdialog"
  description={count === 1
    ? 'The download is removed from the list.'
    : 'The downloads are removed from the list.'}
  {onclose}
  {onsubmit}
  {busy}
  {fallbackFocus}
  size="sm"
>
  {#snippet icon()}
    <span class="tone danger" aria-hidden="true"><Icon name={alsoDelete ? 'alert' : 'remove'} size={18} /></span>
  {/snippet}
  <ul class="names">
    {#each shown as name, i (i)}
      <li title={name}><bdi>{name}</bdi></li>
    {/each}
    {#if count > shown.length}
      <li class="more">and {count - shown.length} more</li>
    {/if}
  </ul>

  <label class="check">
    <input type="checkbox" bind:checked={alsoDelete} />
    Also delete the {count === 1 ? 'file' : 'files'} from disk (including partial data)
  </label>

  {#snippet footer()}
    <button type="button" class="btn" onclick={onclose} disabled={busy} data-autofocus>Cancel</button>
    <button type="submit" class="btn danger" disabled={busy}>
      {alsoDelete ? 'Remove and delete' : 'Remove'}
    </button>
  {/snippet}
</Dialog>

<style>
  .names {
    display: grid;
    gap: 2px;
    margin: 0;
    padding: 8px 11px;
    list-style: none;
    max-block-size: 9rem;
    overflow: auto;
    border: 1px solid var(--line);
    border-radius: 9px;
    background: var(--surface-2);
    color: var(--ink);
    font-size: var(--type-cell);
  }
  .names li {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .more {
    list-style: none;
    color: var(--muted);
  }
  .check {
    font-size: var(--type-cell);
  }
  .tone {
    display: grid;
    place-items: center;
    inline-size: 32px;
    block-size: 32px;
    border-radius: var(--r-md);
  }
  .tone.danger {
    background: var(--danger-soft);
    color: var(--danger);
  }
  @media (forced-colors: active) {
    .tone {
      border: 1px solid CanvasText;
    }
  }
</style>
