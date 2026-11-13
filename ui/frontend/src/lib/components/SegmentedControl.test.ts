import { fireEvent, render, screen, within } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import SegmentedControl, { type SegmentOption } from './SegmentedControl.svelte';
import Switch from './Switch.svelte';
import { chooseOption } from '../testing';

const MODES: SegmentOption<string>[] = [
  { value: 'files', label: 'Files', icon: 'link' },
  { value: 'stream', label: 'Video or stream', icon: 'play' },
];

describe('SegmentedControl: radio mode', () => {
  it('is a named group of native radios, visually hidden, without radio dots', () => {
    render(SegmentedControl, { props: { value: 'files', options: MODES, legend: 'Add mode' } });
    const group = screen.getByRole('group', { name: 'Add mode' });
    expect(group.tagName).toBe('FIELDSET');
    const files = screen.getByRole('radio', { name: 'Files' }) as HTMLInputElement;
    const stream = screen.getByLabelText('Video or stream') as HTMLInputElement;
    expect(files.checked).toBe(true);
    expect(stream.checked).toBe(false);
    expect(files.type).toBe('radio');
    expect(files.classList.contains('sr-only')).toBe(true);
    expect(files.name).toBe(stream.name);
    expect(files.closest('label')?.classList.contains('on')).toBe(true);
  });

  it('changes with the radios and tells its consumer', async () => {
    const onchange = vi.fn();
    render(SegmentedControl, { props: { value: 'files', options: MODES, legend: 'Add mode', onchange } });
    await fireEvent.click(screen.getByRole('radio', { name: 'Video or stream' }));
    expect(onchange).toHaveBeenCalledWith('stream');
    expect((screen.getByRole('radio', { name: 'Video or stream' }) as HTMLInputElement).checked).toBe(true);
    expect((screen.getByRole('radio', { name: 'Files' }) as HTMLInputElement).checked).toBe(false);
    expect(screen.getByRole('radio', { name: 'Video or stream' }).closest('label')?.classList.contains('on')).toBe(true);
  });

  it('can be named by aria-label, keeps disabled options out, and shows counts', async () => {
    const onchange = vi.fn();
    render(SegmentedControl, {
      props: {
        value: 'light',
        ariaLabel: 'Theme',
        onchange,
        options: [
          { value: 'system', label: 'System' },
          { value: 'light', label: 'Light', count: 2 },
          { value: 'dark', label: 'Dark', disabled: true },
        ],
      },
    });
    expect(screen.getByRole('group', { name: 'Theme' })).toBeTruthy();
    expect((screen.getByRole('radio', { name: /^Dark/ }) as HTMLInputElement).disabled).toBe(true);
    expect(screen.getByRole('radio', { name: /^Light/ }).closest('label')?.textContent).toContain('2');
  });
});

describe('SegmentedControl: picking a value (radio only)', () => {
  it('is never a tablist: switching views is Tabs', () => {
    render(SegmentedControl, { props: { value: 'files', options: MODES, legend: 'Add mode' } });
    expect(screen.queryByRole('tablist')).toBeNull();
    expect(screen.queryAllByRole('tab')).toEqual([]);
    expect(screen.getAllByRole('radio')).toHaveLength(2);
  });

  it('gives every segment the same width (one grid column each), so the thumb never changes size', () => {
    render(SegmentedControl, {
      props: {
        value: 'comfortable',
        legend: 'List density',
        options: [
          { value: 'comfortable', label: 'Comfortable' },
          { value: 'compact', label: 'Compact' },
        ],
      },
    });
    const group = screen.getByRole('group', { name: 'List density' });
    expect(group.style.getPropertyValue('--segments')).toBe('2');
  });

  it('offers a Select named like the group for narrow places, which picks the same value', async () => {
    const onchange = vi.fn();
    render(SegmentedControl, {
      props: {
        value: 'system',
        legend: 'How downloads connect',
        fill: true,
        selectWhenNarrow: true,
        onchange,
        options: [
          { value: 'system', label: 'System' },
          { value: 'none', label: 'No proxy' },
          { value: 'manual', label: 'Manual' },
          { value: 'pac', label: 'PAC script' },
        ],
      },
    });
    const select = screen.getByRole('combobox', { name: 'How downloads connect' });
    expect(select.getAttribute('data-value')).toBe('system');
    await chooseOption(select, 'manual');
    expect(onchange).toHaveBeenLastCalledWith('manual');
    expect((screen.getByRole('radio', { name: 'Manual' }) as HTMLInputElement).checked).toBe(true);
    // the segments stay the group the radios belong to
    expect(within(screen.getByRole('group', { name: 'How downloads connect' })).getAllByRole('radio')).toHaveLength(4);
  });

  it('has no Select unless asked for one', () => {
    render(SegmentedControl, { props: { value: 'files', options: MODES, legend: 'Add mode' } });
    expect(screen.queryByRole('combobox')).toBeNull();
  });
});

describe('Switch', () => {
  it('is a labelled switch row: the label names it, the hint describes it, .checked works', async () => {
    const onchange = vi.fn();
    render(Switch, { props: { checked: false, label: 'Launch at login', hint: 'Starts the tray icon when you sign in', onchange } });
    const sw = screen.getByRole('switch', { name: 'Launch at login' }) as HTMLInputElement;
    expect(screen.getByLabelText('Launch at login')).toBe(sw);
    expect(sw.checked).toBe(false);
    expect(sw.getAttribute('aria-checked')).toBe('false');
    expect(sw.getAttribute('aria-describedby')).toBeTruthy();
    expect(document.getElementById(sw.getAttribute('aria-describedby') ?? '')?.textContent).toBe('Starts the tray icon when you sign in');
    // the whole row toggles it
    await fireEvent.click(screen.getByText('Starts the tray icon when you sign in'));
    expect(sw.checked).toBe(true);
    expect(sw.getAttribute('aria-checked')).toBe('true');
    expect(onchange).toHaveBeenLastCalledWith(true);
    await fireEvent.click(sw);
    expect(onchange).toHaveBeenLastCalledWith(false);
  });

  it('is a native disabled control while unavailable', () => {
    render(Switch, { props: { checked: true, label: 'Show the tray icon', disabled: true } });
    const sw = screen.getByRole('switch', { name: 'Show the tray icon' }) as HTMLInputElement;
    expect(sw.disabled).toBe(true);
    expect(sw.checked).toBe(true);
    expect(sw.closest('label')?.classList.contains('disabled')).toBe(true);
  });
});
