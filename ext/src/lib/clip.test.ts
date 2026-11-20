import { describe, expect, it } from 'vitest';

import { checkClip, checkRange, chapterLabel, clipFields, clipSummary, CLIP_TEXTS, formatClipTime, isClipMs, MAX_CLIP_MS, parseClipTime } from './clip';

// Held to internal/media ParseClipTime by TestClipTimesMatchExtension: [input, ms or -1].
const CLIP_TIMES: [string, number][] = [
  ['80', 80000],
  ['0', 0],
  [' 1:20 ', 80000],
  ['01:20', 80000],
  ['80:00', 4800000],
  ['1:02:03', 3723000],
  ['1:20.5', 80500],
  ['1:20.25', 80250],
  ['12.5', 12500],
  ['1m20s', 80000],
  ['1h2m3s', 3723000],
  ['2h', 7200000],
  ['45s', 45000],
  ['1M20S', 80000],
  ['168:00:00', 604800000],
  ['', -1],
  ['abc', -1],
  ['1:2', -1],
  ['1:60', -1],
  ['1:02:3', -1],
  ['1:20.5000', -1],
  ['1:20.', -1],
  ['-5', -1],
  ['1:02:03:04', -1],
  ['168:00:01', -1],
  ['1s2m', -1],
  ['1h60m', -1],
  ['+1', -1],
];

describe('parseClipTime', () => {
  it.each(CLIP_TIMES)('%s', (input, ms) => {
    expect(parseClipTime(input) ?? -1).toBe(ms);
  });
});

describe('formatClipTime', () => {
  it('shows m:ss, h:mm:ss and tenths only when not zero', () => {
    expect(formatClipTime(0)).toBe('0:00');
    expect(formatClipTime(80500)).toBe('1:20.5');
    expect(formatClipTime(80050)).toBe('1:20');
    expect(formatClipTime(754000)).toBe('12:34');
    expect(formatClipTime(3723000)).toBe('1:02:03');
    expect(formatClipTime(-1)).toBe('0:00');
  });
});

describe('checkClip', () => {
  const dur = 754000; // 12:34
  it('takes the desktop’s rules', () => {
    expect(checkClip('1:20', '2:45', dur)).toEqual({ clip: { startMs: 80000, endMs: 165000 } });
    expect(checkClip('1:20', '', dur)).toEqual({ clip: { startMs: 80000, endMs: 0 } });
    expect(checkClip('', '12:34', dur)).toEqual({});
    expect(checkClip('1:20', '12:35', dur)).toEqual({ clip: { startMs: 80000, endMs: 0 } });
    expect(checkClip('1:2', '', dur)).toEqual({ error: CLIP_TEXTS.badTime });
    expect(checkClip('2:45', '1:20', dur)).toEqual({ error: CLIP_TEXTS.order });
    expect(checkClip('1:20', '1:20.5', dur)).toEqual({ error: CLIP_TEXTS.short });
    expect(checkClip('1:20', '13:00', dur)).toEqual({ error: 'The video ends at 12:34.' });
    expect(checkClip('1:00', '99:00', 0)).toEqual({ clip: { startMs: 60000, endMs: 5940000 } });
  });
  it('holds times in ms to the same rules', () => {
    expect(checkRange(80000, 165000, dur)).toEqual({ clip: { startMs: 80000, endMs: 165000 } });
    expect(checkRange(80000, 0, dur)).toEqual({ clip: { startMs: 80000, endMs: 0 } });
    expect(checkRange(0, dur, dur)).toEqual({});
    expect(checkRange(80000, dur + 1000, dur)).toEqual({ clip: { startMs: 80000, endMs: 0 } });
    expect(checkRange(80000, dur + 1001, dur)).toEqual({ error: 'The video ends at 12:34.' });
    expect(checkRange(165000, 80000, dur)).toEqual({ error: CLIP_TEXTS.order });
    expect(checkRange(80000, 80999, dur)).toEqual({ error: CLIP_TEXTS.short });
    expect(checkRange(dur, 0, dur)).toEqual({ error: CLIP_TEXTS.order });
    expect(checkRange(dur - 999, 0, dur)).toEqual({ error: CLIP_TEXTS.short });
  });
  it('gives a message its part’s fields, with no end for a part to the end', () => {
    expect(clipFields({ startMs: 80000, endMs: 165000 })).toEqual({ clipStartMs: 80000, clipEndMs: 165000 });
    expect(clipFields({ startMs: 80000, endMs: 0 })).toEqual({ clipStartMs: 80000 });
  });
  it('summarises and labels', () => {
    expect(clipSummary(undefined, dur)).toBe('Whole video');
    expect(clipSummary({ startMs: 80000, endMs: 165000 }, dur)).toBe('Part 1:20 – 2:45');
    expect(clipSummary({ startMs: 80000, endMs: 0 }, dur)).toBe('Part 1:20 – 12:34');
    expect(chapterLabel({ startMs: 0, endMs: 80000, title: 'Intro' }, 0)).toBe('1. Intro (0:00–1:20)');
    expect(chapterLabel({ startMs: 80000, endMs: 754000, title: ' ' }, 1)).toBe('2. Chapter 2 (1:20–12:34)');
  });
  it('bounds message values', () => {
    expect(isClipMs(0)).toBe(true);
    expect(isClipMs(MAX_CLIP_MS)).toBe(true);
    expect(isClipMs(MAX_CLIP_MS + 1)).toBe(false);
    expect(isClipMs(1.5)).toBe(false);
    expect(isClipMs('80')).toBe(false);
  });
});
