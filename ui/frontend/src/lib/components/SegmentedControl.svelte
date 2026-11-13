<script lang="ts" module>
  import type { IconName } from './Icon.svelte';

  export interface SegmentOption<V extends string | number = string> {
    readonly value: V;
    readonly label: string;
    readonly icon?: IconName;
    /** A count after the label. */
    readonly count?: number;
    readonly disabled?: boolean;
    /** Tooltip. */
    readonly title?: string;
  }
</script>

<script lang="ts" generics="V extends string | number">
  // A segmented control (DESIGN-SYSTEM.md §3.4): picks one value of a few (switching between
  // views is Tabs). A track on the chrome ground whose chosen segment is a raised thumb that
  // slides; the chosen label is Mint (--accent-text, 600) with a Mint icon, so the state never
  // rests on the thumb's faint lift alone. Segments share the width equally, so the thumb keeps
  // its size. Never radio dots: native radios, visually hidden, in labels styled as segments, in
  // a fieldset with an sr-only legend; the browser's arrow keys apply, and getByLabelText /
  // getByRole('radio') / .checked keep working.
  import Icon from './Icon.svelte';
  import Select, { type SelectOption } from './Select.svelte';

  interface Props {
    value: V;
    options: readonly SegmentOption<V>[];
    onchange?: (value: V) => void;
    /** The radios' name (generated otherwise). */
    name?: string;
    /** The group's name, as a visually hidden legend. */
    legend?: string;
    /** The group's name when there is no legend. */
    ariaLabel?: string;
    /** md: 30 px high (as md buttons); sm: 26 px. */
    size?: 'md' | 'sm';
    disabled?: boolean;
    /** Fill the container's width (up to --seg-max), the segments sharing it. */
    fill?: boolean;
    /** With `fill`: where the control has less than 24rem, a Select of the same choices instead. */
    selectWhenNarrow?: boolean;
  }

  let {
    value = $bindable(),
    options,
    onchange,
    name,
    legend,
    ariaLabel,
    size = 'md',
    disabled = false,
    fill = false,
    selectWhenNarrow = false,
  }: Props = $props();

  const uid = $props.id();
  const groupName = $derived(name ?? `${uid}-seg`);
  const selectOptions = $derived<SelectOption<V>[]>(
    options.map((o) => ({ value: o.value, label: o.label, disabled: o.disabled, title: o.title })),
  );

  let root: HTMLElement | undefined = $state();
  /** The thumb's place under the chosen segment, once measured (jsdom: never). */
  let thumb = $state<{ x: number; w: number } | null>(null);
  let animate = $state(false);

  function select(v: V): void {
    if (v === value) return;
    value = v;
    onchange?.(v);
  }

  function measure(): void {
    if (!root) return;
    const on = root.querySelector<HTMLElement>('.opt.on');
    if (!on || on.offsetWidth === 0) {
      thumb = null;
      return;
    }
    thumb = { x: on.offsetLeft, w: on.offsetWidth };
  }

  $effect(() => {
    void value;
    void options;
    measure();
    // the first placement does not slide in from nowhere
    const raf = typeof requestAnimationFrame === 'function' ? requestAnimationFrame(() => (animate = thumb !== null)) : 0;
    return () => {
      if (raf) cancelAnimationFrame(raf);
    };
  });

  $effect(() => {
    if (!root || typeof ResizeObserver === 'undefined') return;
    const ro = new ResizeObserver(() => measure());
    ro.observe(root);
    return () => ro.disconnect();
  });
</script>

{#snippet segments()}
  <fieldset
    bind:this={root}
    class="seg size-{size}"
    class:fill
    class:measured={thumb !== null}
    class:animate
    aria-label={legend ? undefined : ariaLabel}
    {disabled}
    style:--segments={options.length}
  >
    {#if legend}<legend class="sr-only">{legend}</legend>{/if}
    {#if thumb}<span class="thumb" aria-hidden="true" style:inline-size="{thumb.w}px" style:transform="translateX({thumb.x}px)"></span>{/if}
    {#each options as o (o.value)}
      <label class="opt" class:on={o.value === value} class:off={o.disabled} title={o.title}>
        <input
          class="sr-only"
          type="radio"
          name={groupName}
          value={String(o.value)}
          checked={o.value === value}
          disabled={o.disabled}
          onchange={() => select(o.value)}
        />
        {#if o.icon}<Icon name={o.icon} size={14} />{/if}
        <span class="text">{o.label}</span>
        {#if o.count !== undefined}<span class="count">{o.count}</span>{/if}
      </label>
    {/each}
  </fieldset>
{/snippet}

{#if fill}
  <div class="host" class:collapsible={selectWhenNarrow}>
    {@render segments()}
    {#if selectWhenNarrow}
      <div class="narrow">
        <Select {value} options={selectOptions} ariaLabel={legend ?? ariaLabel} size="sm" {disabled} onchange={(v) => select(v)} />
      </div>
    {/if}
  </div>
{:else}
  {@render segments()}
{/if}

<style>
  .seg {
    position: relative;
    display: inline-grid;
    grid-auto-flow: column;
    /* equal segments: each as wide as the widest label, so the thumb never changes size */
    grid-template-columns: repeat(var(--segments, 2), minmax(0, 1fr));
    justify-self: start;
    max-inline-size: 100%;
    gap: 2px;
    min-inline-size: 0;
    margin: 0;
    padding: 2px;
    border: 1px solid var(--line);
    border-radius: 9px;
    background: var(--chrome);
    font-size: var(--type-cell);
  }
  .host {
    container: segmented / inline-size;
    inline-size: 100%;
    min-inline-size: 0;
  }
  .seg.fill {
    display: grid;
    inline-size: 100%;
    max-inline-size: var(--seg-max, 100%);
  }
  .narrow {
    display: none;
  }
  @container segmented (max-width: 24rem) {
    .collapsible .seg {
      display: none;
    }
    .collapsible .narrow {
      display: block;
      max-inline-size: 16rem;
    }
  }
  .thumb {
    position: absolute;
    inset-block: 2px;
    left: 0;
    border-radius: 7px;
    background: var(--raised);
    box-shadow:
      0 0 0 1px var(--line-strong),
      var(--shadow-sm);
    pointer-events: none;
  }
  .animate .thumb {
    transition:
      transform var(--dur-3) var(--ease-out),
      inline-size var(--dur-3) var(--ease-out);
  }
  .opt {
    position: relative;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: 6px;
    min-inline-size: 0;
    block-size: 24px;
    padding-block: 0;
    padding-inline: 12px;
    border-radius: 7px;
    color: var(--muted);
    font-weight: 500;
    white-space: nowrap;
    cursor: pointer;
    transition: color var(--dur-2) ease;
  }
  .size-sm .opt {
    block-size: 20px;
    padding-inline: 10px;
  }
  .opt:hover:not(.on, .off) {
    color: var(--ink);
  }
  .opt.on {
    color: var(--accent-text);
    font-weight: 600;
  }
  .opt.on :global(.icon) {
    color: var(--accent);
  }
  /* until the thumb is measured, the chosen segment carries the raised look itself */
  .seg:not(.measured) .opt.on {
    background: var(--raised);
    box-shadow:
      0 0 0 1px var(--line-strong),
      var(--shadow-sm);
  }
  .opt.off,
  .seg:disabled .opt {
    color: var(--faint);
    cursor: default;
  }
  .seg:disabled .opt.on {
    color: var(--muted);
  }
  .text {
    position: relative;
    min-inline-size: 0;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .count {
    min-inline-size: 18px;
    padding-inline: 5px;
    border-radius: var(--r-pill);
    background: var(--surface-2);
    color: var(--muted);
    font-size: var(--type-meta);
    font-weight: 600;
    line-height: 17px;
    text-align: center;
  }
  .opt.on .count {
    background: var(--accent-soft);
    color: var(--accent-text);
  }
  /* the hidden input's keyboard focus is shown on its segment */
  .opt:has(input:focus-visible) {
    outline: 2px solid var(--focus);
    outline-offset: 1px;
  }
  @media (prefers-reduced-motion: reduce) {
    .animate .thumb,
    .opt {
      transition: none;
    }
  }
  @media (forced-colors: active) {
    .seg {
      border-color: CanvasText;
    }
    .opt.on {
      outline: 2px solid Highlight;
      color: Highlight;
    }
  }
</style>
