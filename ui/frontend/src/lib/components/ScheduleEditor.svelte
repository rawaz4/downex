<script lang="ts">
  // A queue's schedule in the queue editor (DESIGN-SYSTEM.md §4.10): a switch, then the start
  // and stop times, every week (weekday chips with aria-pressed) or once (a date) as a radio
  // segmented control, the retry interval and, where the computer can be woken, waking it.
  // Times and the date are the app's own 24-hour fields (DateTimeField), never the browser's.
  // The fields show only while the schedule is on; what they hold is kept while it is off.
  // The layout classes (q-*) are the queue dialog's.
  import { WEEKDAYS, type DaysMode, type QueueForm, type QueueFormErrors } from '../queues';
  import DateTimeField from './DateTimeField.svelte';
  import SegmentedControl from './SegmentedControl.svelte';
  import Switch from './Switch.svelte';

  type Form = { -readonly [K in keyof QueueForm]: QueueForm[K] };

  interface Props {
    /** The queue editor's form; the schedule fields are edited here. */
    form: Form;
    errors: QueueFormErrors;
    /** The computer can be woken for the start (Windows): the option is shown only then. */
    wakeTimers: boolean;
    disabled?: boolean;
  }

  let { form = $bindable(), errors, wakeTimers, disabled = false }: Props = $props();

  const uid = $props.id();
  const off = $derived(disabled || !form.scheduleEnabled);

  const MODES: readonly { value: DaysMode; label: string }[] = [
    { value: 'days', label: 'Every week' },
    { value: 'once', label: 'Once' },
  ];

  function toggleDay(bit: number) {
    form.weekdays = form.weekdays ^ bit;
  }

  function described(hint: string | null, key: keyof QueueFormErrors): string | undefined {
    const parts = [hint, errors[key] ? `${uid}-${key}-error` : null].filter(Boolean);
    return parts.length > 0 ? parts.join(' ') : undefined;
  }
</script>

<section class="q-section schedule" aria-labelledby="{uid}-title">
  <h4 class="q-title" id="{uid}-title">Schedule</h4>
  <div class="q-card">
    <div class="q-row q-switch">
      <Switch
        bind:checked={form.scheduleEnabled}
        label="Start and stop this queue on a schedule"
        hint={form.scheduleEnabled ? undefined : 'Off: the queue starts and stops when you say.'}
        {disabled}
      />
    </div>

    {#if form.scheduleEnabled}
      <div class="q-row times">
        <div class="field">
          <label for="{uid}-start">Start at</label>
          <DateTimeField
            kind="time"
            id="{uid}-start"
            bind:value={form.startTime}
            disabled={off}
            invalid={!!errors.startTime}
            ariaDescribedby={described(null, 'startTime')}
          />
          {#if errors.startTime}
            <p id="{uid}-startTime-error" class="error-text">{errors.startTime}</p>
          {/if}
        </div>
        <div class="field">
          <label class="check stop">
            <input type="checkbox" bind:checked={form.stopEnabled} disabled={off} />
            Stop at
          </label>
          <DateTimeField
            kind="time"
            id="{uid}-stop"
            ariaLabel="Stop time"
            bind:value={form.stopTime}
            disabled={off || !form.stopEnabled}
            invalid={!!errors.stopTime}
            ariaDescribedby={described(`${uid}-stop-hint`, 'stopTime')}
            fallback={form.startTime}
          />
          {#if errors.stopTime}
            <p id="{uid}-stopTime-error" class="error-text">{errors.stopTime}</p>
          {/if}
        </div>
        <p id="{uid}-stop-hint" class="hint times-hint">A stop time before the start means the next day.</p>
      </div>

      <div class="q-row when">
        <SegmentedControl
          value={form.mode}
          options={MODES}
          legend="Repeat"
          size="sm"
          onchange={(v) => (form.mode = v)}
          disabled={off}
        />
        {#if form.mode === 'days'}
          <div class="days" role="group" aria-label="Weekdays" aria-describedby={errors.days ? `${uid}-days-error` : undefined}>
            {#each WEEKDAYS as day (day.bit)}
              <button
                type="button"
                class="chip day"
                aria-pressed={(form.weekdays & day.bit) !== 0}
                aria-label={day.long}
                title={day.long}
                disabled={off}
                onclick={() => toggleDay(day.bit)}
              >
                {day.short}
              </button>
            {/each}
          </div>
        {:else}
          <DateTimeField
            kind="date"
            ariaLabel="Date"
            bind:value={form.onceDate}
            disabled={off}
            invalid={!!errors.onceDate}
            ariaDescribedby={errors.onceDate ? `${uid}-onceDate-error` : undefined}
          />
        {/if}
        {#if errors.days && form.mode === 'days'}
          <p id="{uid}-days-error" class="error-text when-error">{errors.days}</p>
        {/if}
        {#if errors.onceDate && form.mode === 'once'}
          <p id="{uid}-onceDate-error" class="error-text when-error">{errors.onceDate}</p>
        {/if}
      </div>

      <div class="q-row">
        <div class="q-text">
          <label class="q-label" for="{uid}-repeat">Retry unfinished downloads every … minutes</label>
          <p id="{uid}-repeat-hint" class="q-hint">While the queue runs on schedule, failed and unfinished downloads are tried again. Empty = off.</p>
        </div>
        <div class="suffixed">
          <input
            id="{uid}-repeat"
            class="input sm repeat tnum"
            type="number"
            min="0"
            step="1"
            inputmode="numeric"
            placeholder="Off"
            value={form.repeatMinutes}
            oninput={(e) => (form.repeatMinutes = e.currentTarget.value)}
            disabled={off}
            aria-invalid={errors.repeatMinutes ? 'true' : undefined}
            aria-describedby={described(`${uid}-repeat-hint`, 'repeatMinutes')}
          />
          <span class="suffix" aria-hidden="true">min</span>
        </div>
        {#if errors.repeatMinutes}
          <p id="{uid}-repeatMinutes-error" class="error-text q-error">{errors.repeatMinutes}</p>
        {/if}
      </div>

      {#if wakeTimers}
        <div class="q-row q-switch">
          <Switch
            bind:checked={form.wakeComputer}
            label="Wake the computer from sleep to start"
            hint="While Downex runs in the background."
            disabled={off}
          />
        </div>
      {/if}
    {/if}
  </div>
</section>

<style>
  /* The schedule is a size container of its own: in a wide editor it is a column beside the
     settings (QueueDialog), so its rows follow the column's width, not the editor's. */
  .schedule {
    container: schedule / inline-size;
  }
  .q-row.times {
    grid-template-columns: repeat(2, minmax(0, 10rem)) minmax(0, 1fr);
    align-items: start;
    column-gap: 16px;
  }
  .times .field {
    gap: 6px;
  }
  .times label {
    font-size: var(--type-cell);
    font-weight: 600;
    color: var(--ink);
  }
  .stop {
    gap: 6px;
    min-block-size: 0;
    line-height: 1.2;
  }
  .stop input {
    inline-size: 14px;
    block-size: 14px;
  }
  .times-hint {
    align-self: end;
    padding-block-end: 7px;
  }
  .q-row.when {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 10px 14px;
  }
  .days {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
  }
  .chip.day {
    justify-content: center;
    min-inline-size: 2.75rem;
    block-size: 28px;
    padding-inline: 8px;
    border-radius: 7px;
    font-weight: 500;
  }
  .chip.day[aria-pressed='true'] {
    border-color: var(--accent-line);
    background: var(--accent-soft);
    color: var(--accent-text);
    font-weight: 600;
  }
  .chip.day:disabled {
    opacity: 0.55;
    cursor: default;
  }
  .when-error {
    flex-basis: 100%;
  }
  .suffixed {
    position: relative;
  }
  .repeat {
    inline-size: 7rem;
    padding-inline-end: 36px;
    text-align: end;
  }
  .suffix {
    position: absolute;
    inset-block: 0;
    inset-inline-end: 10px;
    display: grid;
    place-items: center;
    color: var(--faint);
    font-size: var(--type-small);
    pointer-events: none;
  }
  /* the schedule's width, not the window's: the hint goes under the times in a column, and the
     times stack when it is narrow */
  @container schedule (max-width: 36rem) {
    .q-row.times {
      grid-template-columns: repeat(2, minmax(0, 1fr));
      row-gap: 6px;
    }
    .times-hint {
      grid-column: 1 / -1;
      padding-block-end: 0;
    }
  }
  @container schedule (max-width: 22rem) {
    .q-row.times {
      grid-template-columns: minmax(0, 1fr);
    }
  }
</style>
