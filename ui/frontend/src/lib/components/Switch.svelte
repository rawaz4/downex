<script lang="ts">
  // A settings row with a switch (DESIGN-SYSTEM.md §3.6): the label (13/500) and its hint (12,
  // --faint) at the start, the switch at the end; the whole row is the label, so a click
  // anywhere toggles it. The switch is a native checkbox with role="switch", so `.checked`
  // and getByLabelText(label) keep working; the hint describes it.
  interface Props {
    checked: boolean;
    label: string;
    hint?: string;
    disabled?: boolean;
    onchange?: (checked: boolean) => void;
    id?: string;
    name?: string;
  }

  let { checked = $bindable(), label, hint, disabled = false, onchange, id, name }: Props = $props();

  const uid = $props.id();
  const labelId = `${uid}-label`;
  const hintId = `${uid}-hint`;
</script>

<label class="switch-row" class:disabled>
  <span class="words">
    <span class="label" id={labelId}>{label}</span>
    {#if hint}<span class="hint" id={hintId}>{hint}</span>{/if}
  </span>
  <input
    type="checkbox"
    role="switch"
    class="switch"
    {id}
    {name}
    bind:checked
    {disabled}
    aria-checked={checked}
    aria-labelledby={labelId}
    aria-describedby={hint ? hintId : undefined}
    onchange={() => onchange?.(checked)}
  />
</label>

<style>
  .switch-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--sp-7);
    min-block-size: 44px;
    padding-block: 6px;
    cursor: pointer;
  }
  .switch-row.disabled {
    cursor: default;
  }
  .words {
    display: grid;
    gap: 2px;
    min-inline-size: 0;
  }
  .label {
    font-size: var(--type-body);
    font-weight: 500;
    color: var(--ink);
  }
  .disabled .label {
    color: var(--faint);
  }
  .hint {
    font-size: var(--type-small);
    color: var(--faint);
  }
</style>
