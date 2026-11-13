import { fireEvent, render, screen } from '@testing-library/svelte';
import { tick } from 'svelte';
import { describe, expect, it } from 'vitest';
import DateTimeField, { formatParts, parseParts, partAt, stepParts } from './DateTimeField.svelte';

function field(kind: 'time' | 'date', value: string, fallback?: string) {
  render(DateTimeField, { props: { kind, value, ariaLabel: kind === 'time' ? 'Start at' : 'Date', ...(fallback ? { fallback } : {}) } });
  return screen.getByRole('textbox', { name: kind === 'time' ? 'Start at' : 'Date' }) as HTMLInputElement;
}

async function press(input: HTMLInputElement, key: string, caret: number) {
  input.focus();
  input.setSelectionRange(caret, caret);
  const ev = await fireEvent.keyDown(input, { key });
  await tick();
  await tick();
  return ev;
}

describe('DateTimeField: the rules', () => {
  it('reads and writes 24-hour times and calendar dates', () => {
    expect(parseParts('time', '07:05')).toEqual([7, 5]);
    expect(parseParts('time', '7:05')).toEqual([7, 5]);
    expect(parseParts('time', '24:00')).toBeNull();
    expect(parseParts('time', '12:60')).toBeNull();
    expect(parseParts('date', '2026-02-29')).toBeNull();
    expect(parseParts('date', '2028-02-29')).toEqual([2028, 2, 29]);
    expect(formatParts('time', [7, 5])).toBe('07:05');
    expect(formatParts('date', [2026, 11, 2])).toBe('2026-11-02');
  });

  it('steps the part at the caret, wrapping times within the day and dates on the calendar', () => {
    expect(partAt('time', 0)).toBe(0);
    expect(partAt('time', 4)).toBe(1);
    expect(partAt('date', 6)).toBe(1);
    expect(partAt('date', 9)).toBe(2);
    expect(stepParts('time', [23, 59], 0, 1)).toEqual([0, 59]);
    expect(stepParts('time', [0, 0], 1, -1)).toEqual([0, 59]);
    expect(stepParts('date', [2026, 10, 31], 2, 1)).toEqual([2026, 11, 1]);
    expect(stepParts('date', [2026, 1, 31], 1, 1)).toEqual([2026, 2, 28]);
    expect(stepParts('date', [2026, 12, 15], 1, 1)).toEqual([2027, 1, 15]);
    expect(stepParts('date', [2028, 2, 29], 0, 1)).toEqual([2029, 2, 28]);
  });
});

describe('DateTimeField: the keyboard', () => {
  it('is a plain text field in the app’s 24-hour format, never the browser’s picker', () => {
    const input = field('time', '01:00');
    expect(input.type).toBe('text');
    expect(input.getAttribute('inputmode')).toBe('numeric');
    expect(input.placeholder).toBe('hh:mm');
    expect(input.getAttribute('aria-describedby')).toBeTruthy();
  });

  it('steps the hour or the minute at the caret with Up, Down, PageUp and PageDown', async () => {
    const input = field('time', '23:30');
    await press(input, 'ArrowUp', 1);
    expect(input.value).toBe('00:30');
    // the stepped part stays selected
    expect([input.selectionStart, input.selectionEnd]).toEqual([0, 2]);
    await press(input, 'ArrowDown', 4);
    expect(input.value).toBe('00:29');
    expect([input.selectionStart, input.selectionEnd]).toEqual([3, 5]);
    await press(input, 'PageUp', 4);
    expect(input.value).toBe('00:39');
  });

  it('starts from the fallback (the start time) when it holds nothing it can read', async () => {
    const input = field('time', '', '22:15');
    // an empty field's caret is at the start: the hour
    await press(input, 'ArrowUp', 0);
    expect(input.value).toBe('23:15');
  });

  it('steps a date by day, month or year, and writes a readable value out in full when left', async () => {
    const input = field('date', '2026-10-31');
    await press(input, 'ArrowUp', 9);
    expect(input.value).toBe('2026-11-01');
    await press(input, 'ArrowDown', 6);
    expect(input.value).toBe('2026-10-01');
    await fireEvent.input(input, { target: { value: '2026-1-5' } });
    await fireEvent.blur(input);
    expect(input.value).toBe('2026-01-05');
    // what it cannot read stays as typed (the form says what is wrong)
    await fireEvent.input(input, { target: { value: '2026-02-30' } });
    await fireEvent.blur(input);
    expect(input.value).toBe('2026-02-30');
  });
});
