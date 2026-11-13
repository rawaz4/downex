import { describe, expect, it } from 'vitest';
import { ClickTracker, MAX_TRACKED_FRAMES } from './clicks';
import { BYPASS_WINDOW_MS, type ClickModifiers, defaultRules, evaluateCapture } from './rules';

const NOW = 1_700_000_000_000;

function click(over: Partial<ClickModifiers> = {}): ClickModifiers {
  return { alt: false, ctrl: false, shift: false, meta: false, ts: NOW, ...over };
}

describe('ClickTracker', () => {
  it('keeps the Alt-click of one tab when another tab is clicked', () => {
    const t = new ClickTracker();
    t.record(1, 0, click({ alt: true, ts: NOW - 100 }), NOW - 100);
    t.record(2, 0, click({ ts: NOW - 50 }), NOW - 50);
    expect(t.bypassClick(1, NOW)).toMatchObject({ alt: true });
    // The Alt-click of tab 1 never bypasses a download of tab 2.
    expect(t.bypassClick(2, NOW)).toBeUndefined();
  });

  it('keeps the Alt-click of one frame when another frame of the tab is clicked', () => {
    const t = new ClickTracker();
    t.record(1, 0, click({ alt: true, ts: NOW - 300 }), NOW - 300);
    t.record(1, 5, click({ ts: NOW - 10 }), NOW - 10);
    expect(t.bypassClick(1, NOW)?.ts).toBe(NOW - 300);
  });

  it('a later plain click in the same frame ends the bypass', () => {
    const t = new ClickTracker();
    t.record(1, 0, click({ alt: true, ts: NOW - 300 }), NOW - 300);
    t.record(1, 0, click({ ts: NOW - 10 }), NOW - 10);
    expect(t.bypassClick(1, NOW)).toBeUndefined();
  });

  it('considers every tab when the download tab is unknown', () => {
    const t = new ClickTracker();
    t.record(1, 0, click({ ts: NOW - 20 }), NOW - 20);
    t.record(2, 3, click({ alt: true, ts: NOW - 200 }), NOW - 200);
    expect(t.bypassClick(undefined, NOW)?.ts).toBe(NOW - 200);
  });

  it('expires clicks after the bypass window and forgets closed tabs', () => {
    const t = new ClickTracker();
    t.record(1, 0, click({ alt: true, ts: NOW }), NOW);
    t.record(2, 0, click({ alt: true, ts: NOW }), NOW);
    expect(t.bypassClick(1, NOW + BYPASS_WINDOW_MS)).toBeDefined();
    t.forgetTab(2);
    expect(t.bypassClick(2, NOW + 1)).toBeUndefined();
    expect(t.bypassClick(1, NOW + BYPASS_WINDOW_MS + 1)).toBeUndefined();
    expect(t.size).toBe(0);
  });

  it('stays bounded', () => {
    const t = new ClickTracker();
    for (let i = 0; i < MAX_TRACKED_FRAMES + 50; i++) t.record(i, 0, click({ alt: true }), NOW);
    expect(t.size).toBe(MAX_TRACKED_FRAMES);
    expect(t.bypassClick(MAX_TRACKED_FRAMES + 49, NOW)).toBeDefined();
    expect(t.bypassClick(0, NOW)).toBeUndefined();
  });

  it('feeds the capture rules', () => {
    const t = new ClickTracker();
    t.record(1, 0, click({ alt: true, ts: NOW - 100 }), NOW - 100);
    const decide = (tabId: number) =>
      evaluateCapture(defaultRules(), { url: 'https://a.example/a.zip', tabId }, { now: NOW, lastClick: t.bypassClick(tabId, NOW) });
    expect(decide(1)).toEqual({ capture: false, reason: 'bypass' });
    expect(decide(2)).toEqual({ capture: true, extension: 'zip' });
  });

  it("lets a tab opened by a click look at its opener's click", () => {
    const t = new ClickTracker();
    t.record(1, 0, click({ alt: true, ts: NOW - 100 }), NOW - 100);
    t.setOpener(7, 1); // target=_blank, middle-click or window.open from tab 1
    expect(t.bypassClick(7, NOW)).toMatchObject({ alt: true });
    t.setOpener(8, 7);
    expect(t.bypassClick(8, NOW)).toMatchObject({ alt: true });
    expect(t.bypassClick(9, NOW)).toBeUndefined();
    t.setOpener(1, 8); // a cycle ends the lookup
    expect(t.bypassClick(8, NOW)).toMatchObject({ alt: true });
    expect(t.bypassClick(2, NOW)).toBeUndefined();
    t.forgetTab(7);
    expect(t.bypassClick(7, NOW)).toBeUndefined();
  });

  it('names the tab of the latest link click for downloads of an unknown tab', () => {
    const t = new ClickTracker();
    expect(t.lastClickTab(NOW)).toBeUndefined();
    t.record(1, 0, click({ ts: NOW - 300 }), NOW - 300);
    t.record(2, 4, click({ ctrl: true, ts: NOW - 50 }), NOW - 50);
    expect(t.lastClickTab(NOW)).toBe(2);
    expect(t.lastClickTab(NOW - 50 + BYPASS_WINDOW_MS + 1)).toBeUndefined();
  });
});

