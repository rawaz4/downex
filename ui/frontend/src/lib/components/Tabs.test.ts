import { fireEvent, render, screen } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import Tabs, { tabId, type TabOption } from './Tabs.svelte';

const VIEWS: TabOption<string>[] = [
  { value: 'status', label: 'Status' },
  { value: 'limit', label: 'Speed limit', dot: true },
  { value: 'gone', label: 'Gone', disabled: true },
  { value: 'log', label: 'Log', count: 3 },
];

function setup(onchange = vi.fn(), options = VIEWS) {
  render(Tabs, { props: { value: 'status', ariaLabel: 'Progress view', panelId: 'panel', idPrefix: 'pw', onchange, options } });
  return { onchange, tab: (name: string | RegExp) => screen.getByRole('tab', { name }) };
}

describe('Tabs', () => {
  it('is a named tablist with a roving tabindex, aria-controls and stable ids', () => {
    const { tab } = setup();
    const list = screen.getByRole('tablist', { name: 'Progress view' });
    expect(tab('Status').getAttribute('aria-selected')).toBe('true');
    expect(tab('Speed limit').getAttribute('aria-selected')).toBe('false');
    expect(tab('Status').tabIndex).toBe(0);
    expect(tab('Speed limit').tabIndex).toBe(-1);
    expect(tab('Speed limit').getAttribute('aria-controls')).toBe('panel');
    expect(tab('Status').id).toBe(tabId('pw', 'status'));
    expect(tabId('pw', 'a b/c')).toBe('pw-tab-a_b_c');
    // one view switcher: not a radio group, and nothing drawn twice per tab
    expect(screen.queryByRole('radio')).toBeNull();
    expect(list.querySelectorAll('[role=tab]')).toHaveLength(4);
  });

  it('keeps each tab named by its label alone (the bold twin, the dot and the count are hidden from the name)', () => {
    const { tab } = setup();
    expect(tab('Speed limit').textContent).toContain('Speed limit');
    // the Mint dot of an active limit is drawn but not read out
    expect(tab('Speed limit').querySelector('.dot')?.getAttribute('aria-hidden')).toBe('true');
    expect(tab('Status').querySelector('.dot')).toBeNull();
    expect(tab(/^Log/).querySelector('.count')?.textContent).toBe('3');
  });

  it('moves and activates with the arrows, Home and End, skipping disabled tabs', async () => {
    const { tab, onchange } = setup();
    tab('Status').focus();
    await fireEvent.keyDown(tab('Status'), { key: 'ArrowRight' });
    expect(onchange).toHaveBeenLastCalledWith('limit');
    expect(document.activeElement).toBe(tab('Speed limit'));
    expect(tab('Speed limit').getAttribute('aria-selected')).toBe('true');
    expect(tab('Speed limit').tabIndex).toBe(0);
    await fireEvent.keyDown(tab('Speed limit'), { key: 'ArrowRight' });
    expect(document.activeElement).toBe(tab(/^Log/));
    await fireEvent.keyDown(tab(/^Log/), { key: 'ArrowRight' });
    expect(document.activeElement).toBe(tab('Status')); // wraps
    await fireEvent.keyDown(tab('Status'), { key: 'ArrowLeft' });
    expect(document.activeElement).toBe(tab(/^Log/));
    await fireEvent.keyDown(tab(/^Log/), { key: 'Home' });
    expect(document.activeElement).toBe(tab('Status'));
    await fireEvent.keyDown(tab('Status'), { key: 'End' });
    expect(document.activeElement).toBe(tab(/^Log/));
    expect(onchange).not.toHaveBeenCalledWith('gone');
    await fireEvent.click(tab('Speed limit'));
    expect(onchange).toHaveBeenLastCalledWith('limit');
  });

  it('marks the chosen tab without a measured indicator (no layout), so it never shows nothing', () => {
    const { tab } = setup();
    const list = screen.getByRole('tablist');
    // jsdom measures nothing: the sliding indicator is not drawn and the chosen tab underlines itself
    expect(list.querySelector('.indicator')).toBeNull();
    expect(list.closest('.tabs')?.classList.contains('measured')).toBe(false);
    expect(tab('Status').classList.contains('on')).toBe(true);
  });

  it('draws one sliding indicator under the chosen label once it can measure it', async () => {
    const rect = (left: number, width: number) => ({ left, width, right: left + width, top: 0, bottom: 20, height: 20, x: left, y: 0, toJSON() {} }) as DOMRect;
    const spy = vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
      if (this.getAttribute('role') === 'tablist') return rect(0, 300);
      if (this.classList.contains('label')) {
        const tab = this.closest('[role=tab]');
        return tab?.id === tabId('pw', 'limit') ? rect(112, 70) : rect(14, 40);
      }
      return rect(0, 0);
    });
    const { tab } = setup();
    await fireEvent.click(tab('Speed limit'));
    await new Promise((r) => setTimeout(r, 0));
    const bar = screen.getByRole('tablist').querySelector<HTMLElement>('.indicator');
    expect(bar).toBeTruthy();
    expect(bar?.getAttribute('aria-hidden')).toBe('true');
    expect(bar?.style.inlineSize).toBe('70px');
    expect(bar?.style.transform).toBe('translateX(112px)');
    expect(screen.getByRole('tablist').querySelectorAll('.indicator')).toHaveLength(1);
    spy.mockRestore();
  });
});

describe('Tabs flush', () => {
  it('lines the first label up with the content by reaching into the padding, never by trimming a tab', () => {
    render(Tabs, { props: { value: 'status', ariaLabel: 'Progress view', options: VIEWS, size: 'sm', flush: true } });
    const root = screen.getByRole('tablist').closest('.tabs');
    expect(root?.classList.contains('flush')).toBe(true);
    // every tab is the same kind of tab: the first has no padding of its own (measure-dialogs.mjs checks the layout)
    const tabs = screen.getAllByRole('tab');
    expect(new Set(tabs.map((t) => t.className.replace(/\s*\bon\b/, '')))).toHaveProperty('size', 1);
  });
});
