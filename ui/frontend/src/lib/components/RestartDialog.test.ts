import { fireEvent, render, screen, within } from '@testing-library/svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import RestartDialog from './RestartDialog.svelte';

const KEY = 'downex.existingFile';

afterEach(() => {
  vi.restoreAllMocks();
  localStorage.removeItem(KEY);
});

function show(props: Record<string, unknown>) {
  const onconfirm = vi.fn();
  const onclose = vi.fn();
  render(RestartDialog, { props: { names: ['report.pdf'], reason: 'completed', onconfirm, onclose, ...props } as never });
  const dialog = screen.getByRole('alertdialog');
  const description = () => document.getElementById(dialog.getAttribute('aria-describedby') ?? '')?.textContent ?? '';
  return { dialog, description, onconfirm, onclose };
}

const choices = (dialog: HTMLElement) => within(dialog).queryAllByRole('radio') as HTMLInputElement[];

describe('RestartDialog: downloading a completed file again', () => {
  it('asks to keep both (the default, with the numbered name) or to replace the current file', async () => {
    const { dialog, description, onconfirm } = show({ names: ['report.pdf'], again: [{ name: 'report.pdf', exists: true }] });
    expect(screen.getByRole('heading', { name: 'Download again?' })).toBeTruthy();
    expect(description()).toBe('Downex downloads “⁨report.pdf⁩” again from its address.');
    const [keep, replace] = choices(dialog);
    expect(keep?.checked).toBe(true);
    expect(keep?.closest('label')?.textContent).toContain('Keep both');
    expect(keep?.closest('label')?.textContent).toContain('saved as “⁨report (1).pdf⁩”');
    expect(replace?.closest('label')?.textContent).toContain('Replace the current file');
    expect(replace?.closest('label')?.textContent).toContain('stays until the new copy is complete');
    // the chosen row has the keyboard, like the queue rows of Move to queue
    expect(document.activeElement).toBe(keep);

    await fireEvent.click(within(dialog).getByRole('button', { name: 'Download again' }));
    await vi.waitFor(() => expect(onconfirm).toHaveBeenCalledWith('keepBoth'));
  });

  it('remembers Replace for the next time, on this computer', async () => {
    const first = show({ again: [{ name: 'report.pdf', exists: true }] });
    await fireEvent.click(choices(first.dialog)[1]!);
    expect(first.dialog.querySelector('.choice.on')?.textContent).toContain('Replace the current file');
    await fireEvent.click(within(first.dialog).getByRole('button', { name: 'Download again' }));
    await vi.waitFor(() => expect(first.onconfirm).toHaveBeenCalledWith('replace'));
    expect(localStorage.getItem(KEY)).toBe('replace');
    document.body.innerHTML = '';

    const second = show({ again: [{ name: 'report.pdf', exists: true }] });
    expect(choices(second.dialog)[1]?.checked).toBe(true);
  });

  it('works when storage cannot be read or written', async () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('SecurityError');
    });
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('QuotaExceededError');
    });
    const { dialog, onconfirm } = show({ again: [{ name: 'report.pdf', exists: true }] });
    expect(choices(dialog)[0]?.checked).toBe(true);
    await fireEvent.click(choices(dialog)[1]!);
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Download again' }));
    await vi.waitFor(() => expect(onconfirm).toHaveBeenCalledWith('replace'));
  });

  it('offers no choice when the file is no longer there: it comes back under its own name', async () => {
    const { dialog, description, onconfirm } = show({ again: [{ name: 'report.pdf', exists: false }] });
    expect(choices(dialog)).toEqual([]);
    expect(description()).toBe(
      '“⁨report.pdf⁩” is no longer in its folder. Downex downloads it again from its address and saves it under the same name.',
    );
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Download again' }));
    await vi.waitFor(() => expect(onconfirm).toHaveBeenCalledWith('keepBoth'));
  });

  it('says a web page’s video is read from its page again', () => {
    const { description } = show({ names: ['A talk.mp4'], again: [{ name: 'A talk.mp4', exists: true, site: true }] });
    expect(description()).toBe('Downex gets “⁨A talk.mp4⁩” again from its video page.');
  });

  it('gives several completed downloads one choice, and says which files are gone', () => {
    const { dialog, description } = show({
      names: ['a.zip', 'b.zip', 'c.zip'],
      again: [
        { name: 'a.zip', exists: true },
        { name: 'b.zip', exists: false },
        { name: 'c.zip', exists: true },
      ],
    });
    expect(description()).toBe('Downex downloads 3 completed downloads again from their addresses.');
    expect(within(dialog).getAllByRole('listitem').map((li) => li.textContent)).toEqual(['a.zip', 'b.zip', 'c.zip']);
    expect(choices(dialog)[0]?.closest('label')?.textContent).toContain('Each new copy gets a numbered name, such as “⁨a (1).zip⁩”');
    expect(dialog.textContent).toContain('1 of them is no longer in its folder: it comes back under its own name.');
  });

  it('with a mixed selection, restarts the others and asks only about the completed ones', async () => {
    const { dialog, description, onconfirm } = show({
      names: ['big.iso', 'notes.pdf'],
      reason: 'manual',
      again: [{ name: 'notes.pdf', exists: true }],
    });
    expect(screen.getByRole('heading', { name: 'Restart downloads?' })).toBeTruthy();
    expect(description()).toBe(
      'Restarting 2 downloads discards the data received so far and downloads from the beginning. “⁨notes.pdf⁩” is complete: Downex downloads it again.',
    );
    const group = within(dialog).getByRole('radiogroup', { name: 'The current file of “⁨notes.pdf⁩”' });
    expect(within(group).getAllByRole('radio')).toHaveLength(2);
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Restart' }));
    await vi.waitFor(() => expect(onconfirm).toHaveBeenCalledWith('keepBoth'));
  });

  it('keeps the restart texts of downloads that are not complete', () => {
    const { dialog, description } = show({ names: ['big.iso'], reason: 'validator_changed' });
    expect(screen.getByRole('heading', { name: 'Restart from the beginning?' })).toBeTruthy();
    expect(description()).toContain('cannot be resumed');
    expect(choices(dialog)).toEqual([]);
    expect(within(dialog).getByRole('button', { name: 'Not now' })).toBeTruthy();
  });
});
