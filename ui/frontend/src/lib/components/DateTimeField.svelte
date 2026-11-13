<script lang="ts" module>
  export type DateTimeKind = 'time' | 'date';

  /** The parts of a value: time [hours, minutes], date [year, month, day]; null when it does not parse. */
  export function parseParts(kind: DateTimeKind, value: string): number[] | null {
    const m = kind === 'time' ? /^\s*(\d{1,2}):?(\d{2})\s*$/.exec(value) : /^\s*(\d{4})-(\d{1,2})-(\d{1,2})\s*$/.exec(value);
    if (!m) return null;
    const parts = m.slice(1).map(Number);
    if (kind === 'time') {
      const [h = 0, min = 0] = parts;
      return h <= 23 && min <= 59 ? [h, min] : null;
    }
    const [y = 0, mo = 0, d = 0] = parts;
    return mo >= 1 && mo <= 12 && d >= 1 && d <= daysIn(y, mo) ? [y, mo, d] : null;
  }

  export function formatParts(kind: DateTimeKind, parts: readonly number[]): string {
    const p2 = (n: number) => String(n).padStart(2, '0');
    if (kind === 'time') return `${p2(parts[0] ?? 0)}:${p2(parts[1] ?? 0)}`;
    return `${String(parts[0] ?? 0).padStart(4, '0')}-${p2(parts[1] ?? 1)}-${p2(parts[2] ?? 1)}`;
  }

  function daysIn(year: number, month: number): number {
    return new Date(Date.UTC(year, month, 0)).getUTCDate();
  }

  /** Which part the caret is in: time hh|mm, date yyyy|mm|dd. */
  export function partAt(kind: DateTimeKind, caret: number): number {
    if (kind === 'time') return caret <= 2 ? 0 : 1;
    return caret <= 4 ? 0 : caret <= 7 ? 1 : 2;
  }

  /** Where each part sits in the formatted value [start, end). */
  export function partRange(kind: DateTimeKind, part: number): [number, number] {
    if (kind === 'time') return part === 0 ? [0, 2] : [3, 5];
    return part === 0 ? [0, 4] : part === 1 ? [5, 7] : [8, 10];
  }

  /**
   * One part stepped by `by`: hours and minutes wrap within the day (24-hour); a date moves by
   * whole years, months or days on the calendar (the 31st of a month steps to the last day of
   * a shorter one).
   */
  export function stepParts(kind: DateTimeKind, parts: readonly number[], part: number, by: number): number[] {
    if (kind === 'time') {
      const [h = 0, m = 0] = parts;
      if (part === 0) return [(((h + by) % 24) + 24) % 24, m];
      return [h, (((m + by) % 60) + 60) % 60];
    }
    const [y = 2000, mo = 1, d = 1] = parts;
    if (part === 2) {
      const t = new Date(Date.UTC(y, mo - 1, d + by));
      return [t.getUTCFullYear(), t.getUTCMonth() + 1, t.getUTCDate()];
    }
    const months = y * 12 + (mo - 1) + (part === 1 ? by : by * 12);
    const ny = Math.floor(months / 12);
    const nm = (months % 12) + 1;
    return [ny, nm, Math.min(d, daysIn(ny, nm))];
  }
</script>

<script lang="ts">
  // A time (24-hour hh:mm) or date (yyyy-mm-dd) field (DESIGN-SYSTEM.md §3.5): a text field in
  // the app's own format, never the browser's 12-hour picker or calendar. Typing works as in any
  // field; Up and Down step the part at the caret (hour or minute; year, month or day), PageUp
  // and PageDown by 10, and the part stays selected. A value it can read is written out in full
  // when the field is left ("7:5" stays as typed, "7:05" becomes "07:05").
  import { tick } from 'svelte';

  interface Props {
    kind: DateTimeKind;
    value: string;
    id?: string;
    ariaLabel?: string;
    ariaDescribedby?: string;
    invalid?: boolean;
    disabled?: boolean;
    /** Where Up and Down start from when the field holds nothing it can read. */
    fallback?: string;
  }

  let { kind, value = $bindable(), id, ariaLabel, ariaDescribedby, invalid = false, disabled = false, fallback }: Props = $props();

  const uid = $props.id();
  const placeholder = $derived(kind === 'time' ? 'hh:mm' : 'yyyy-mm-dd');
  const hintId = `${uid}-keys`;
  const describedBy = $derived([ariaDescribedby, hintId].filter(Boolean).join(' '));

  async function onkeydown(e: KeyboardEvent): Promise<void> {
    const steps: Record<string, number> = { ArrowUp: 1, ArrowDown: -1, PageUp: 10, PageDown: -10 };
    const by = steps[e.key];
    if (by === undefined || e.altKey || e.ctrlKey || e.metaKey) return;
    e.preventDefault();
    const input = e.currentTarget as HTMLInputElement;
    const parts = parseParts(kind, value) ?? parseParts(kind, fallback ?? '') ?? (kind === 'time' ? [0, 0] : todayParts());
    const part = partAt(kind, input.selectionStart ?? 0);
    value = formatParts(kind, stepParts(kind, parts, part, by));
    await tick();
    const [from, to] = partRange(kind, part);
    input.setSelectionRange(from, to);
  }

  function todayParts(): number[] {
    const d = new Date();
    return [d.getFullYear(), d.getMonth() + 1, d.getDate()];
  }

  function onblur(): void {
    const parts = parseParts(kind, value);
    if (parts) {
      const full = formatParts(kind, parts);
      if (full !== value) value = full;
    }
  }
</script>

<input
  {id}
  class="input sm dt dt-{kind}"
  type="text"
  inputmode="numeric"
  autocomplete="off"
  spellcheck="false"
  maxlength={kind === 'time' ? 5 : 10}
  {placeholder}
  aria-label={ariaLabel}
  aria-describedby={describedBy}
  aria-invalid={invalid ? 'true' : undefined}
  {disabled}
  bind:value
  onkeydown={(e) => void onkeydown(e)}
  {onblur}
/>
<span id={hintId} class="sr-only">
  {kind === 'time' ? '24-hour time. Up and Down change the hour or the minute at the cursor.' : 'Year, month, day. Up and Down change the part at the cursor.'}
</span>

<style>
  .dt {
    font-variant-numeric: tabular-nums;
  }
  .dt-time {
    inline-size: 6.5rem;
  }
  .dt-date {
    inline-size: 8.5rem;
  }
</style>
