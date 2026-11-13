<script lang="ts" module>
  /** A completed download of the dialog (Download again, ENGINE-SPEC §7.1). */
  export interface AgainFile {
    /** Its name (display text). */
    readonly name: string;
    /** Its file is still in its folder (false: it comes back under its own name, nothing to choose). */
    readonly exists: boolean;
    /** A web page's video: downexd reads its page again (fresh addresses). */
    readonly site?: boolean;
  }
</script>

<script lang="ts">
  // Restart and Download again (DESIGN-SYSTEM.md §4.7). Restarting discards what was received and
  // downloads from the beginning. A completed download is downloaded again: while its file is
  // still there, the dialog asks what happens to it, as selectable rows like Move to queue's: keep
  // both (the default; the new copy gets a numbered name) or replace it once the new copy is
  // complete. The choice is remembered on this computer.
  import { numberedName, type ExistingFile } from '../api';
  import Dialog from './Dialog.svelte';
  import Icon, { type IconName } from './Icon.svelte';
  import { isolate } from '../text';

  interface Props {
    /** Every download to restart (display text). */
    names: readonly string[];
    /**
     * validator_changed: the daemon cannot resume; manual: the user asked to restart; completed:
     * every one of them is complete (Download again).
     */
    reason: 'validator_changed' | 'manual' | 'completed';
    /** The completed downloads among `names`: what is done with their files. */
    again?: readonly AgainFile[];
    /** Starts them; `existingFile` applies to the completed ones. */
    onconfirm: (existingFile: ExistingFile) => Promise<void> | void;
    onclose: () => void;
    fallbackFocus?: () => HTMLElement | null | undefined;
  }

  let { names, reason, again = [], onconfirm, onclose, fallbackFocus }: Props = $props();

  /** Where the choice is remembered (per viewer; the dialog works without it). */
  const CHOICE_KEY = 'downex.existingFile';

  function readChoice(): ExistingFile {
    try {
      return localStorage.getItem(CHOICE_KEY) === 'replace' ? 'replace' : 'keepBoth';
    } catch {
      return 'keepBoth';
    }
  }

  function keepChoice(v: ExistingFile): void {
    try {
      localStorage.setItem(CHOICE_KEY, v);
    } catch {
      // storage unavailable: the default comes back next time
    }
  }

  const uid = $props.id();
  let busy = $state(false);
  let choice = $state<ExistingFile>(readChoice());

  const count = $derived(names.length);
  /** Quoted and isolated (U+2068…U+2069): a right-to-left name keeps its own order in the sentence. */
  const quote = (name: string) => `“${isolate(name)}”`;
  const subject = $derived(count === 1 ? quote(names[0] ?? '') : `${count} downloads`);

  /** The completed ones whose file is still there: they get the choice. */
  const present = $derived(again.filter((a) => a.exists));
  const gone = $derived(again.length - present.length);
  const choosing = $derived(present.length > 0);
  const allSites = $derived(again.length > 0 && again.every((a) => a.site === true));

  const description = $derived.by(() => {
    if (reason === 'validator_changed') {
      return `${subject} cannot be resumed: the file on the server changed, or the saved progress no longer matches the partial file. Restarting discards the data received so far and downloads the file again from the beginning.`;
    }
    if (reason === 'completed') {
      const one = again.length === 1 ? again[0] : undefined;
      const from = allSites ? 'its video page' : 'its address';
      if (one && !one.exists) return `${quote(one.name)} is no longer in its folder. Downex downloads it again from ${from} and saves it under the same name.`;
      if (one) return `Downex ${one.site ? 'gets' : 'downloads'} ${quote(one.name)} again from ${from}.`;
      return `Downex downloads ${again.length} completed downloads again from their ${allSites ? 'video pages' : 'addresses'}.`;
    }
    const restart = `Restarting ${subject} discards the data received so far and downloads from the beginning.`;
    if (again.length === 0) return restart;
    const done = again.length === 1 ? `${quote(again[0]?.name ?? '')} is complete: Downex downloads it again.` : `${again.length} of them are complete: Downex downloads them again.`;
    return `${restart} ${done}`;
  });

  /** The choice group's name: whose current file it is about. */
  const groupLabel = $derived(
    present.length === 1 ? `The current file of ${quote(present[0]?.name ?? '')}` : `The current files of the ${present.length} completed downloads`,
  );

  interface Choice {
    readonly value: ExistingFile;
    readonly label: string;
    readonly note: string;
    readonly icon: IconName;
  }

  const options = $derived.by((): Choice[] => {
    const first = present[0]?.name ?? '';
    return [
      {
        value: 'keepBoth',
        label: 'Keep both',
        note:
          present.length === 1
            ? `The new copy is saved as ${quote(numberedName(first, 1))} (or the next free number).`
            : `Each new copy gets a numbered name, such as ${quote(numberedName(first, 1))}.`,
        icon: 'copy',
      },
      {
        value: 'replace',
        label: present.length === 1 ? 'Replace the current file' : 'Replace the current files',
        note: 'The current file stays until the new copy is complete. If downloading fails, it stays as it is.',
        icon: 'refresh',
      },
    ];
  });

  const goneNote = $derived(
    gone === 0 || again.length === 1
      ? ''
      : gone === 1
        ? '1 of them is no longer in its folder: it comes back under its own name.'
        : `${gone} of them are no longer in their folders: they come back under their own names.`,
  );

  const title = $derived(
    reason === 'validator_changed' ? 'Restart from the beginning?' : reason === 'completed' ? 'Download again?' : count === 1 ? 'Restart download?' : 'Restart downloads?',
  );

  async function onsubmit() {
    busy = true;
    const used = choosing ? choice : 'keepBoth';
    if (choosing) keepChoice(used);
    try {
      await onconfirm(used);
    } finally {
      busy = false;
    }
    onclose();
  }
</script>

<Dialog {title} role="alertdialog" {description} {onclose} {onsubmit} {busy} {fallbackFocus} size="sm">
  {#snippet icon()}
    <span class="tone info" aria-hidden="true"><Icon name="restart" size={18} /></span>
  {/snippet}
  {#if count > 1}
    <ul class="names">
      {#each names.slice(0, 5) as name, i (i)}
        <li><bdi>{name}</bdi></li>
      {/each}
      {#if count > 5}
        <li class="more">and {count - 5} more</li>
      {/if}
    </ul>
  {/if}

  {#if choosing}
    <div class="choices" role="radiogroup" aria-label={groupLabel}>
      {#each options as o (o.value)}
        <label class="choice" class:on={choice === o.value}>
          <input
            class="sr-only"
            type="radio"
            name="{uid}-existing"
            value={o.value}
            bind:group={choice}
            aria-labelledby="{uid}-{o.value}-label"
            aria-describedby="{uid}-{o.value}-note"
            data-autofocus={choice === o.value ? '' : undefined}
          />
          <span class="cicon"><Icon name={o.icon} size={16} /></span>
          <span class="ctext">
            <span class="clabel" id="{uid}-{o.value}-label">{o.label}</span>
            <span class="cnote" id="{uid}-{o.value}-note">{o.note}</span>
          </span>
          <span class="tick" aria-hidden="true">{#if choice === o.value}<Icon name="check" size={16} />{/if}</span>
        </label>
      {/each}
    </div>
    {#if goneNote}
      <p class="gone">{goneNote}</p>
    {/if}
  {/if}

  {#snippet footer()}
    <button type="button" class="btn" onclick={onclose} disabled={busy} data-autofocus={choosing ? undefined : ''}>
      {reason === 'validator_changed' ? 'Not now' : 'Cancel'}
    </button>
    <button type="submit" class="btn primary" disabled={busy}>
      {#if reason === 'completed'}{busy ? 'Starting…' : 'Download again'}{:else}{busy ? 'Restarting…' : 'Restart'}{/if}
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
  .tone {
    display: grid;
    place-items: center;
    inline-size: 32px;
    block-size: 32px;
    border-radius: var(--r-md);
  }
  .tone.info {
    background: var(--info-soft);
    color: var(--info);
  }

  /* The choice rows (as Move to queue's, DESIGN-SYSTEM.md §4.10): a label around a visually hidden
     native radio, an icon, the choice and what it does, a Mint check on the chosen one. */
  .choices {
    display: grid;
    gap: 2px;
    min-inline-size: 0;
    padding: 4px;
    border: 1px solid var(--line);
    border-radius: var(--r-lg);
    background: var(--surface);
  }
  .choice {
    display: flex;
    align-items: flex-start;
    gap: 10px;
    min-block-size: 36px;
    padding-block: 8px;
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
  .cicon {
    display: grid;
    flex: none;
    margin-block-start: 1px;
    color: var(--muted);
  }
  .choice.on .cicon {
    color: var(--accent);
  }
  .ctext {
    display: grid;
    flex: 1 1 auto;
    gap: 2px;
    min-inline-size: 0;
  }
  .clabel {
    font-weight: 500;
  }
  .choice.on .clabel {
    font-weight: 600;
  }
  .cnote {
    color: var(--muted);
    font-size: var(--type-small);
    line-height: 1.4;
    overflow-wrap: anywhere;
  }
  .tick {
    display: grid;
    flex: none;
    place-items: center;
    inline-size: 16px;
    margin-block-start: 1px;
    color: var(--accent);
  }
  .gone {
    color: var(--muted);
    font-size: var(--type-small);
  }
  @media (prefers-reduced-motion: reduce) {
    .choice {
      transition: none;
    }
  }
  @media (forced-colors: active) {
    .tone {
      border: 1px solid CanvasText;
    }
    .choice.on {
      outline: 2px solid Highlight;
      outline-offset: -2px;
    }
  }
</style>
