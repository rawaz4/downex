// Queues, their schedules and post-queue actions (docs/M6-SPEC.md §1): labels, the queue
// editor's form and its validation, and ordering helpers. The daemon stays the authority
// and validates again.

import {
  EMPTY_SCHEDULE,
  LIMITS,
  MAIN_QUEUE_ID,
  type Capabilities,
  type PostAction,
  type Queue,
  type QueueSpec,
  type Schedule,
} from './api';
import { parseRate, splitRate, type RateUnit } from './format';
import { checkIntRange } from './validate';

/** The days of a schedule, Monday first: bit 0 = Monday … bit 6 = Sunday. */
export const WEEKDAYS: readonly { readonly bit: number; readonly short: string; readonly long: string }[] = [
  { bit: 1 << 0, short: 'Mon', long: 'Monday' },
  { bit: 1 << 1, short: 'Tue', long: 'Tuesday' },
  { bit: 1 << 2, short: 'Wed', long: 'Wednesday' },
  { bit: 1 << 3, short: 'Thu', long: 'Thursday' },
  { bit: 1 << 4, short: 'Fri', long: 'Friday' },
  { bit: 1 << 5, short: 'Sat', long: 'Saturday' },
  { bit: 1 << 6, short: 'Sun', long: 'Sunday' },
];

export const EVERY_DAY = 0x7f;
export const WORKDAYS = 0x1f;
export const WEEKEND = 0x60;

/** What the queue editor offers for a finished queue. */
export const POST_ACTION_LABELS: Record<PostAction, string> = {
  none: 'Keep running',
  quit_downex: 'Quit Downex',
  sleep: 'Put the computer to sleep',
  hibernate: 'Hibernate the computer',
  shutdown: 'Shut down the computer',
};

/**
 * The short names of the actions, for menus where the question is already asked ("When the
 * main queue is done": Keep running, Quit Downex, Sleep, …; DESIGN-SYSTEM.md §3.14).
 */
export const POST_ACTION_SHORT_LABELS: Record<PostAction, string> = {
  none: 'Keep running',
  quit_downex: 'Quit Downex',
  sleep: 'Sleep',
  hibernate: 'Hibernate',
  shutdown: 'Shut down',
};

/** The status bar's readable control: "When done: keep running", "When done: shut down". */
export function whenDoneText(action: PostAction): string {
  const short = POST_ACTION_SHORT_LABELS[action] ?? POST_ACTION_SHORT_LABELS.none;
  return `When done: ${action === 'quit_downex' ? short : short.toLowerCase()}`;
}

/** The action puts the computer to sleep, hibernates it or shuts it down (shown as a warning). */
export function isPowerAction(action: PostAction): boolean {
  return action === 'sleep' || action === 'hibernate' || action === 'shutdown';
}

/** The countdown's wording: "<verb> in 42 s". */
export const POST_ACTION_PROGRESS: Record<PostAction, string> = {
  none: 'Finishing',
  quit_downex: 'Quitting Downex',
  sleep: 'Going to sleep',
  hibernate: 'Hibernating',
  shutdown: 'Shutting down',
};

/**
 * The post-queue actions this system can do: hibernation only where the host says so
 * (not on macOS). A queue that already has an action keeps it on offer.
 */
export function postActionChoices(caps: Pick<Capabilities, 'hibernate'>, current: PostAction = 'none'): PostAction[] {
  const all: PostAction[] = ['none', 'quit_downex', 'sleep', 'hibernate', 'shutdown'];
  return all.filter((a) => a !== 'hibernate' || caps.hibernate || current === 'hibernate');
}

/** The main queue first, then the others in the order they were created. */
export function sortQueues(list: Iterable<Queue>): Queue[] {
  return [...list].sort((a, b) => Number(b.isMain || b.id === MAIN_QUEUE_ID) - Number(a.isMain || a.id === MAIN_QUEUE_ID) || a.id - b.id);
}

export function minutesToTime(minutes: number): string {
  const m = Number.isInteger(minutes) && minutes >= 0 && minutes < 1440 ? minutes : 0;
  return `${String(Math.floor(m / 60)).padStart(2, '0')}:${String(m % 60).padStart(2, '0')}`;
}

/** "HH:MM" (24 h) → minutes after midnight; null when it is not a time of day. */
export function timeToMinutes(text: string): number | null {
  const m = /^(\d{1,2}):(\d{2})$/.exec(text.trim());
  if (!m) return null;
  const h = Number(m[1]);
  const min = Number(m[2]);
  if (h > 23 || min > 59) return null;
  return h * 60 + min;
}

/** A local calendar date "YYYY-MM-DD" that exists. */
export function isDate(text: string): boolean {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(text);
  if (!m) return false;
  const y = Number(m[1]);
  const mo = Number(m[2]);
  const d = Number(m[3]);
  const date = new Date(y, mo - 1, d);
  return date.getFullYear() === y && date.getMonth() === mo - 1 && date.getDate() === d;
}

/** Days of a weekday mask, e.g. "Mon–Fri", "Every day", "Mon, Wed, Sat". */
export function formatDays(mask: number): string {
  const days = mask & EVERY_DAY;
  if (days === EVERY_DAY) return 'Every day';
  if (days === WORKDAYS) return 'Mon–Fri';
  if (days === WEEKEND) return 'Sat–Sun';
  return WEEKDAYS.filter((d) => days & d.bit)
    .map((d) => d.short)
    .join(', ');
}

/** One line about when a queue runs. */
export function scheduleSummary(s: Schedule): string {
  if (!s.enabled) return 'Not scheduled: starts and stops when you say';
  const when = s.weekdays === 0 ? `Once on ${s.onceDate || 'a date to choose'}` : formatDays(s.weekdays);
  const stop = s.stopEnabled ? `, stops at ${minutesToTime(s.stopMinute)}` : '';
  const repeat = s.repeatMinutes > 0 ? `, retries every ${s.repeatMinutes} min` : '';
  return `${when} at ${minutesToTime(s.startMinute)}${stop}${repeat}`;
}

/**
 * The short note beside a queue's name in a list (the sidebar, the queue manager): "running",
 * or the time its schedule starts it next ("01:00"; a one-time start also names its date), or
 * "" for a stopped queue without a schedule.
 */
export function queueNote(q: Pick<Queue, 'running' | 'schedule'>): string {
  if (q.running) return 'running';
  const s = q.schedule;
  if (!s.enabled) return '';
  const at = minutesToTime(s.startMinute);
  return s.weekdays === 0 && s.onceDate ? `${s.onceDate} ${at}` : at;
}

/** A queue's settings as UpdateQueue takes them, with `patch` applied (the when-done menu). */
export function queueSpec(q: Queue, patch: Partial<QueueSpec> = {}): QueueSpec {
  return {
    name: q.name,
    maxConcurrent: q.maxConcurrent,
    speedLimit: q.speedLimit,
    schedule: q.schedule,
    postAction: q.postAction,
    ...patch,
  };
}

/** Whole seconds until the deadline (never negative). */
export function secondsLeft(deadline: number, now: number): number {
  return Math.max(0, Math.ceil((deadline - now) / 1000));
}

/**
 * The position MoveDownloads takes to put `moved` just before `before` in a queue whose
 * downloads are `order` (`before` null = at the end): its index among the queue's other
 * downloads, or -1 for the end.
 */
export function insertionIndex(order: readonly number[], moved: readonly number[], before: number | null): number {
  if (before === null) return -1;
  const others = order.filter((id) => !moved.includes(id));
  const i = others.indexOf(before);
  return i < 0 ? -1 : i;
}

/** The order after moving `moved` before `before` (null = to the end), as the daemon will have it. */
export function reorder(order: readonly number[], moved: readonly number[], before: number | null): number[] {
  const others = order.filter((id) => !moved.includes(id));
  const i = insertionIndex(order, moved, before);
  const at = i < 0 ? others.length : i;
  return [...others.slice(0, at), ...moved, ...others.slice(at)];
}

/**
 * Moving `id` one place up (delta -1) or down (+1) among `shown` (the rows the editor
 * lists, in the order of the queue's downloads `order`, which may hold more): the
 * download it then goes before, or null for the end; undefined when it cannot move.
 */
export function neighbourTarget(
  order: readonly number[],
  shown: readonly number[],
  id: number,
  delta: -1 | 1,
): number | null | undefined {
  const i = shown.indexOf(id);
  if (i < 0) return undefined;
  if (delta < 0) return i === 0 ? undefined : (shown[i - 1] as number);
  const next = shown[i + 1];
  if (next === undefined) return undefined;
  // Just after the next row: before whatever follows it in the queue.
  const others = order.filter((x) => x !== id);
  const after = others[others.indexOf(next) + 1];
  return after === undefined ? null : after;
}

// ---- the queue editor ------------------------------------------------------------------

export type DaysMode = 'days' | 'once';

export interface QueueForm {
  readonly name: string;
  /** As typed; 0 = the global "simultaneous downloads" setting. */
  readonly maxConcurrent: string;
  readonly speedValue: string;
  readonly speedUnit: RateUnit;
  /** The limit as loaded (bytes per second): kept exactly while its text is unchanged (parseRate). */
  readonly speedLimitBytes?: number;
  readonly postAction: PostAction;
  readonly scheduleEnabled: boolean;
  readonly startTime: string;
  readonly stopEnabled: boolean;
  readonly stopTime: string;
  readonly mode: DaysMode;
  readonly weekdays: number;
  readonly onceDate: string;
  /** As typed; 0 or empty = off. */
  readonly repeatMinutes: string;
  readonly wakeComputer: boolean;
}

export interface QueueFormErrors {
  name?: string;
  maxConcurrent?: string;
  speed?: string;
  postAction?: string;
  startTime?: string;
  stopTime?: string;
  days?: string;
  onceDate?: string;
  repeatMinutes?: string;
}

export type QueueFormResult =
  | { readonly ok: true; readonly spec: QueueSpec }
  | { readonly ok: false; readonly errors: QueueFormErrors };

function today(now: number): string {
  const d = new Date(now);
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
}

/** The editor's form for a queue, or for a new one (no queue). */
export function queueForm(q: Queue | null, now: number = Date.now()): QueueForm {
  const s = q?.schedule ?? EMPTY_SCHEDULE;
  const rate = splitRate(q?.speedLimit ?? 0);
  return {
    name: q?.name ?? '',
    maxConcurrent: String(q?.maxConcurrent ?? 0),
    speedValue: rate.value,
    speedUnit: rate.unit,
    speedLimitBytes: q?.speedLimit ?? 0,
    postAction: q?.postAction ?? 'none',
    scheduleEnabled: s.enabled,
    startTime: minutesToTime(s.startMinute),
    stopEnabled: s.stopEnabled,
    stopTime: minutesToTime(s.stopMinute),
    mode: s.enabled && s.weekdays === 0 ? 'once' : 'days',
    // A new schedule starts with every day.
    weekdays: s.weekdays === 0 ? EVERY_DAY : s.weekdays,
    onceDate: s.onceDate || today(now),
    repeatMinutes: s.repeatMinutes > 0 ? String(s.repeatMinutes) : '',
    wakeComputer: s.wakeComputer,
  };
}

/**
 * Checks the queue editor: a unique name (ignoring case) of at most 64 characters, 0–16
 * simultaneous downloads, a speed limit, an action this system can do, and a schedule
 * with valid times and at least one day (or a date). A disabled schedule keeps what was
 * entered, unchecked, so that switching it on again restores it. `others` are the other
 * queues (for the name); `base` the queue being edited, whose stored schedule values are
 * kept for the fields the editor leaves alone.
 */
export function validateQueueForm(
  form: QueueForm,
  others: readonly Pick<Queue, 'name'>[],
  caps: Pick<Capabilities, 'hibernate' | 'wakeTimers'>,
  base: Queue | null = null,
): QueueFormResult {
  const errors: QueueFormErrors = {};
  const name = form.name.trim();
  if (name === '') errors.name = 'Enter a name';
  else if ([...name].length > LIMITS.maxQueueNameLength) errors.name = `Keep the name under ${LIMITS.maxQueueNameLength + 1} characters`;
  else if (/[\u0000-\u001f\u007f]/.test(name)) errors.name = 'A name cannot contain control characters';
  else if (others.some((q) => q.name.trim().toLowerCase() === name.toLowerCase())) errors.name = 'Another queue has this name';

  const concurrentError = checkIntRange(form.maxConcurrent, 0, LIMITS.maxQueueConcurrent, 'Simultaneous downloads');
  if (concurrentError) errors.maxConcurrent = concurrentError;
  const speed = parseRate(form.speedValue, form.speedUnit, form.speedLimitBytes);
  if (speed === null) errors.speed = 'Enter a number, or leave empty for no limit';
  if (form.postAction === 'hibernate' && !caps.hibernate) errors.postAction = 'This computer cannot hibernate';

  const start = timeToMinutes(form.startTime);
  const stop = timeToMinutes(form.stopTime);
  const repeatText = form.repeatMinutes.trim();
  const repeatError = repeatText === '' ? null : checkIntRange(repeatText, 0, LIMITS.maxRepeatMinutes, 'The interval');
  if (form.scheduleEnabled) {
    if (start === null) errors.startTime = 'Enter a time such as 23:30';
    if (form.stopEnabled && stop === null) errors.stopTime = 'Enter a time such as 07:00';
    if (form.mode === 'days' && (form.weekdays & EVERY_DAY) === 0) errors.days = 'Choose at least one day';
    if (form.mode === 'once' && !isDate(form.onceDate)) errors.onceDate = 'Choose a date';
    if (repeatError) errors.repeatMinutes = repeatError;
  }
  if (Object.keys(errors).length > 0) return { ok: false, errors };

  const kept = base?.schedule ?? EMPTY_SCHEDULE;
  const schedule: Schedule = form.scheduleEnabled
    ? {
        enabled: true,
        startMinute: start ?? 0,
        stopEnabled: form.stopEnabled,
        stopMinute: stop ?? kept.stopMinute,
        weekdays: form.mode === 'once' ? 0 : form.weekdays & EVERY_DAY,
        onceDate: form.mode === 'once' ? form.onceDate : kept.onceDate,
        // Only where the computer can be woken; elsewhere what was stored stays.
        wakeComputer: caps.wakeTimers ? form.wakeComputer : kept.wakeComputer,
        repeatMinutes: repeatText === '' ? 0 : Number(repeatText),
      }
    : { ...kept, enabled: false };
  return {
    ok: true,
    spec: {
      name,
      maxConcurrent: Number(form.maxConcurrent.trim()),
      speedLimit: speed ?? 0,
      schedule,
      postAction: form.postAction,
    },
  };
}
