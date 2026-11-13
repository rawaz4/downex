import { describe, expect, it } from 'vitest';
import { EMPTY_SCHEDULE } from './api';
import {
  EVERY_DAY,
  POST_ACTION_LABELS,
  POST_ACTION_SHORT_LABELS,
  WORKDAYS,
  formatDays,
  insertionIndex,
  isDate,
  isPowerAction,
  minutesToTime,
  neighbourTarget,
  postActionChoices,
  queueForm,
  queueNote,
  queueSpec,
  reorder,
  scheduleSummary,
  secondsLeft,
  sortQueues,
  timeToMinutes,
  validateQueueForm,
  whenDoneText,
} from './queues';
import { makeQueue } from './testing';

const caps = { hibernate: true, wakeTimers: true };

describe('schedule times and days', () => {
  it('converts minutes after midnight and times of day', () => {
    expect(minutesToTime(0)).toBe('00:00');
    expect(minutesToTime(1439)).toBe('23:59');
    expect(minutesToTime(-5)).toBe('00:00');
    expect(timeToMinutes('23:30')).toBe(1410);
    expect(timeToMinutes('7:05')).toBe(425);
    for (const bad of ['24:00', '12:60', 'noon', '', '1230']) expect(timeToMinutes(bad)).toBeNull();
  });

  it('checks calendar dates', () => {
    expect(isDate('2026-10-24')).toBe(true);
    expect(isDate('2028-02-29')).toBe(true);
    expect(isDate('2026-02-29')).toBe(false);
    expect(isDate('2026-13-01')).toBe(false);
    expect(isDate('24.10.2026')).toBe(false);
  });

  it('names days and summarises a schedule', () => {
    expect(formatDays(EVERY_DAY)).toBe('Every day');
    expect(formatDays(WORKDAYS)).toBe('Mon–Fri');
    expect(formatDays(0b1000101)).toBe('Mon, Wed, Sun');
    expect(scheduleSummary(EMPTY_SCHEDULE)).toMatch(/^Not scheduled/);
    expect(
      scheduleSummary({ ...EMPTY_SCHEDULE, enabled: true, startMinute: 1380, stopEnabled: true, stopMinute: 420, weekdays: WORKDAYS, repeatMinutes: 30 }),
    ).toBe('Mon–Fri at 23:00, stops at 07:00, retries every 30 min');
    expect(scheduleSummary({ ...EMPTY_SCHEDULE, enabled: true, startMinute: 60, onceDate: '2026-10-24' })).toBe('Once on 2026-10-24 at 01:00');
  });

  it('counts down in whole seconds', () => {
    expect(secondsLeft(61_000, 1_000)).toBe(60);
    expect(secondsLeft(60_500, 1_000)).toBe(60);
    expect(secondsLeft(1_000, 5_000)).toBe(0);
  });
});

describe('queue order', () => {
  const order = [10, 11, 12, 13];

  it('finds the position among the other downloads', () => {
    expect(insertionIndex(order, [13], 10)).toBe(0);
    expect(insertionIndex(order, [10], 13)).toBe(2); // 11 12 [10] 13
    expect(insertionIndex(order, [10], null)).toBe(-1);
    expect(insertionIndex(order, [10], 99)).toBe(-1);
    expect(reorder(order, [10], 13)).toEqual([11, 12, 10, 13]);
    expect(reorder(order, [12, 13], 10)).toEqual([12, 13, 10, 11]);
    expect(reorder(order, [11], null)).toEqual([10, 12, 13, 11]);
  });

  it('moves one row up or down among the rows shown, also when the queue holds more', () => {
    // 12 is completed (not shown); 14 follows the shown rows.
    const full = [10, 11, 12, 13, 14];
    const shown = [10, 11, 13];
    expect(neighbourTarget(full, shown, 11, -1)).toBe(10);
    expect(neighbourTarget(full, shown, 10, -1)).toBeUndefined();
    // Down: just after 13, before 14.
    expect(neighbourTarget(full, shown, 11, 1)).toBe(14);
    expect(neighbourTarget(full, shown, 13, 1)).toBeUndefined();
    expect(neighbourTarget([10, 11], [10, 11], 10, 1)).toBeNull();
    expect(neighbourTarget(full, shown, 99, 1)).toBeUndefined();
  });

  it('lists the main queue first', () => {
    const list = sortQueues([makeQueue({ id: 3 }), makeQueue({ id: 1 }), makeQueue({ id: 2 })]);
    expect(list.map((q) => q.id)).toEqual([1, 2, 3]);
  });
});

describe('post-queue actions', () => {
  it('offers hibernation only where the system can', () => {
    expect(postActionChoices({ hibernate: true })).toEqual(['none', 'quit_downex', 'sleep', 'hibernate', 'shutdown']);
    expect(postActionChoices({ hibernate: false })).not.toContain('hibernate');
    // A stored choice stays visible (to be changed).
    expect(postActionChoices({ hibernate: false }, 'hibernate')).toContain('hibernate');
  });

  it('names every action shortly for the when-done menu, and reads it as a sentence', () => {
    expect(Object.keys(POST_ACTION_SHORT_LABELS).sort()).toEqual(Object.keys(POST_ACTION_LABELS).sort());
    expect(postActionChoices({ hibernate: true }).map((a) => POST_ACTION_SHORT_LABELS[a])).toEqual([
      'Keep running',
      'Quit Downex',
      'Sleep',
      'Hibernate',
      'Shut down',
    ]);
    expect(whenDoneText('none')).toBe('When done: keep running');
    expect(whenDoneText('quit_downex')).toBe('When done: Quit Downex');
    expect(whenDoneText('sleep')).toBe('When done: sleep');
    expect(whenDoneText('hibernate')).toBe('When done: hibernate');
    expect(whenDoneText('shutdown')).toBe('When done: shut down');
    expect(['none', 'quit_downex', 'sleep', 'hibernate', 'shutdown'].filter((a) => isPowerAction(a as never))).toEqual([
      'sleep',
      'hibernate',
      'shutdown',
    ]);
  });
});

describe('queues in lists', () => {
  it('notes a running queue, or when its schedule starts it next', () => {
    expect(queueNote(makeQueue({ id: 1, running: true }))).toBe('running');
    expect(queueNote(makeQueue({ id: 2 }))).toBe('');
    const night = makeQueue({ id: 2, schedule: { ...EMPTY_SCHEDULE, enabled: true, startMinute: 60, weekdays: EVERY_DAY } });
    expect(queueNote(night)).toBe('01:00');
    expect(queueNote({ ...night, running: true })).toBe('running');
    expect(queueNote(makeQueue({ id: 3, schedule: { ...EMPTY_SCHEDULE, enabled: true, startMinute: 75, weekdays: 0, onceDate: '2026-10-30' } }))).toBe(
      '2026-10-30 01:15',
    );
    // A switched-off schedule says nothing.
    expect(queueNote({ ...night, schedule: { ...night.schedule, enabled: false } })).toBe('');
  });

  it("gives a queue's settings for UpdateQueue, with a change", () => {
    const night = makeQueue({ id: 2, name: 'Night', maxConcurrent: 2, speedLimit: 1024, postAction: 'sleep', downloadIds: [4, 5], running: true });
    expect(queueSpec(night)).toEqual({ name: 'Night', maxConcurrent: 2, speedLimit: 1024, schedule: night.schedule, postAction: 'sleep' });
    expect(queueSpec(night, { postAction: 'shutdown' })).toEqual({ ...queueSpec(night), postAction: 'shutdown' });
  });
});

describe('validateQueueForm', () => {
  const night = makeQueue({
    id: 2,
    name: 'Night',
    maxConcurrent: 2,
    speedLimit: 2 * 1024 * 1024,
    postAction: 'sleep',
    schedule: { ...EMPTY_SCHEDULE, enabled: true, startMinute: 1380, stopEnabled: true, stopMinute: 420, weekdays: WORKDAYS, wakeComputer: true },
  });

  it('round-trips a queue unchanged', () => {
    const result = validateQueueForm(queueForm(night), [], caps, night);
    expect(result).toEqual({
      ok: true,
      spec: { name: 'Night', maxConcurrent: 2, speedLimit: 2 * 1024 * 1024, postAction: 'sleep', schedule: night.schedule },
    });
  });

  it('starts a new queue unscheduled, with every day proposed', () => {
    const form = queueForm(null, new Date(2026, 9, 21, 12).getTime());
    expect(form).toMatchObject({ name: '', maxConcurrent: '0', scheduleEnabled: false, weekdays: EVERY_DAY, onceDate: '2026-10-21', mode: 'days' });
    const r = validateQueueForm({ ...form, name: '  Weekend ' }, [], caps);
    expect(r).toMatchObject({ ok: true, spec: { name: 'Weekend', schedule: { ...EMPTY_SCHEDULE } } });
  });

  it('reports what is wrong', () => {
    const r = validateQueueForm(
      {
        ...queueForm(night),
        name: 'main QUEUE',
        maxConcurrent: '17',
        speedValue: 'fast',
        startTime: '25:00',
        stopTime: 'x',
        weekdays: 0,
        repeatMinutes: '-1',
      },
      [makeQueue({ id: 1 })],
      caps,
      night,
    );
    expect(r.ok).toBe(false);
    if (r.ok) return;
    expect(Object.keys(r.errors).sort()).toEqual(['days', 'maxConcurrent', 'name', 'repeatMinutes', 'speed', 'startTime', 'stopTime']);
    expect(r.errors.name).toBe('Another queue has this name');
    const once = validateQueueForm({ ...queueForm(night), mode: 'once', onceDate: '2026-02-30' }, [], caps, night);
    expect(once).toMatchObject({ ok: false, errors: { onceDate: 'Choose a date' } });
    expect(validateQueueForm({ ...queueForm(night), name: '' }, [], caps)).toMatchObject({ ok: false, errors: { name: 'Enter a name' } });
    expect(validateQueueForm({ ...queueForm(night), name: 'x'.repeat(65) }, [], caps).ok).toBe(false);
  });

  it('refuses hibernation where the system cannot, and keeps the wake setting where it cannot wake', () => {
    expect(validateQueueForm({ ...queueForm(night), postAction: 'hibernate' }, [], { hibernate: false, wakeTimers: false })).toMatchObject({
      ok: false,
      errors: { postAction: 'This computer cannot hibernate' },
    });
    const r = validateQueueForm({ ...queueForm(night), wakeComputer: false }, [], { hibernate: false, wakeTimers: false }, night);
    expect(r).toMatchObject({ ok: true, spec: { schedule: { wakeComputer: true } } });
  });

  it('builds a once schedule, and a switched-off one keeps what was stored', () => {
    const once = validateQueueForm({ ...queueForm(night), mode: 'once', onceDate: '2026-10-24', startTime: '01:15', repeatMinutes: '15' }, [], caps, night);
    expect(once).toMatchObject({ ok: true, spec: { schedule: { enabled: true, weekdays: 0, onceDate: '2026-10-24', startMinute: 75, repeatMinutes: 15 } } });
    // Invalid fields of a disabled schedule do not block saving; the stored schedule stays.
    const off = validateQueueForm({ ...queueForm(night), scheduleEnabled: false, startTime: 'junk' }, [], caps, night);
    expect(off).toMatchObject({ ok: true, spec: { schedule: { ...night.schedule, enabled: false } } });
  });
});
