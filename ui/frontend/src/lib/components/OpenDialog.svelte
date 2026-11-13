<script lang="ts">
  import Dialog from './Dialog.svelte';
  import Icon from './Icon.svelte';

  interface Props {
    /** Display name (bidi controls already made visible). */
    name: string;
    /** The file would run code when opened. */
    executable: boolean;
    /** The real name contains bidi control characters. */
    hiddenControls: boolean;
    /** Open with…: the user chooses the app next (the question says so). */
    withApp?: boolean;
    onconfirm: () => void;
    onclose: () => void;
    fallbackFocus?: () => HTMLElement | null | undefined;
  }

  let { name, executable, hiddenControls, withApp = false, onconfirm, onclose, fallbackFocus }: Props = $props();

  const title = $derived(executable ? (withApp ? 'Open this program with another app?' : 'Run this program?') : 'Open this file?');
  const description = $derived(
    executable
      ? withApp
        ? 'It is a program or script, and the app you choose may run it on this computer. Programs from the internet can harm your computer; open it only if you trust where it came from.'
        : 'Opening it runs the program on this computer. Programs from the internet can harm your computer; open it only if you trust where it came from.'
      : 'Check the file name before opening it.',
  );
  const action = $derived(withApp ? 'Choose app…' : executable ? 'Run' : 'Open');

  function onsubmit() {
    // confirm first: closing clears the caller's dialog state
    onconfirm();
    onclose();
  }
</script>

<Dialog
  {title}
  role="alertdialog"
  {description}
  {onclose}
  {onsubmit}
  {fallbackFocus}
  size="sm"
>
  {#snippet icon()}
    <span class="tone {executable ? 'danger' : 'info'}" aria-hidden="true">
      <Icon name={executable ? 'alert' : 'open'} size={18} />
    </span>
  {/snippet}
  <p class="name mono"><bdi>{name}</bdi></p>
  {#if hiddenControls}
    <p class="warning">
      The name contains hidden text-direction characters, shown here as ⟨U+…⟩. They can make a
      file look like a different type than it is.
    </p>
  {/if}

  {#snippet footer()}
    <button type="button" class="btn" onclick={onclose} data-autofocus>Cancel</button>
    <button type="submit" class="btn primary">{action}</button>
  {/snippet}
</Dialog>

<style>
  .name {
    padding: 8px 11px;
    border: 1px solid var(--line);
    border-radius: 9px;
    background: var(--surface-2);
    color: var(--ink);
    overflow-wrap: anywhere;
  }
  .warning {
    color: var(--danger);
    font-size: var(--type-cell);
    font-weight: 600;
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
  .tone.info {
    background: var(--info-soft);
    color: var(--info);
  }
  @media (forced-colors: active) {
    .tone {
      border: 1px solid CanvasText;
    }
  }
</style>
