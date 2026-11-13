import { fireEvent, render, screen } from '@testing-library/svelte';
import { createRawSnippet } from 'svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import Dialog from '../components/Dialog.svelte';
import { canRestoreFocus, trapFocus } from './focus-trap';

afterEach(() => {
  document.body.replaceChildren();
});

function setup() {
  const opener = document.createElement('button');
  opener.textContent = 'Remove';
  const grid = document.createElement('div');
  grid.tabIndex = 0;
  const dialog = document.createElement('div');
  dialog.innerHTML = '<button>Cancel</button><button>OK</button>';
  document.body.append(opener, grid, dialog);
  opener.focus();
  const trap = trapFocus(dialog, { fallback: () => grid });
  return { opener, grid, dialog, trap };
}

const flush = () => new Promise<void>((r) => queueMicrotask(r));

describe('trapFocus: returning focus on close', () => {
  it('returns focus to the opener when it can still take it', async () => {
    const { opener, dialog, trap } = setup();
    expect(dialog.contains(document.activeElement)).toBe(true);
    trap.destroy();
    dialog.remove();
    await flush();
    expect(document.activeElement).toBe(opener);
  });

  it('falls back when the opener became disabled while the dialog was open', async () => {
    const { opener, grid, dialog, trap } = setup();
    opener.disabled = true;
    trap.destroy();
    dialog.remove();
    await flush();
    expect(document.activeElement).toBe(grid);
  });

  it('falls back when the opener is inert, hidden or gone', async () => {
    for (const spoil of [
      (el: HTMLElement) => el.setAttribute('inert', ''),
      (el: HTMLElement) => (el.hidden = true),
      (el: HTMLElement) => el.remove(),
    ]) {
      const { opener, grid, dialog, trap } = setup();
      spoil(opener);
      trap.destroy();
      dialog.remove();
      await flush();
      expect(document.activeElement).toBe(grid);
      document.body.replaceChildren();
    }
  });

  it('decides after the page updated, not when the dialog starts closing', async () => {
    const { opener, grid, dialog, trap } = setup();
    trap.destroy();
    dialog.remove();
    opener.disabled = true; // e.g. the selection emptied in the same update
    await flush();
    expect(document.activeElement).toBe(grid);
  });

  it('lets an exempt region outside keep the focus and joins it to the Tab cycle', () => {
    const banner = document.createElement('section');
    banner.setAttribute('data-focus-trap-exempt', '');
    banner.innerHTML = '<button>Stop it</button>';
    const outside = document.createElement('button');
    const dialog = document.createElement('div');
    dialog.innerHTML = '<button>Cancel</button><button>OK</button>';
    document.body.append(outside, dialog, banner);
    const trap = trapFocus(dialog);
    const [cancel, ok] = [...dialog.querySelectorAll('button')] as HTMLButtonElement[];
    const stop = banner.querySelector('button')!;
    expect(document.activeElement).toBe(cancel);
    stop.focus();
    expect(document.activeElement).toBe(stop);
    outside.focus(); // anything else goes back into the dialog
    expect(document.activeElement).toBe(cancel);

    const tab = (from: HTMLElement, shiftKey = false) => {
      from.focus();
      from.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', shiftKey, bubbles: true, cancelable: true }));
      return document.activeElement;
    };
    expect(tab(ok!)).toBe(stop);
    expect(tab(stop)).toBe(cancel);
    expect(tab(cancel!, true)).toBe(stop);
    expect(tab(stop, true)).toBe(ok);
    trap.destroy();
  });

  it('leaves the focus in an exempt region to a modal that opened by itself', () => {
    const banner = document.createElement('section');
    banner.setAttribute('data-focus-trap-exempt', '');
    banner.innerHTML = '<button>Stop it</button>';
    const dialog = document.createElement('div');
    dialog.tabIndex = -1;
    dialog.innerHTML = '<button>OK</button>';
    document.body.append(dialog, banner);
    const stop = banner.querySelector('button')!;
    stop.focus();
    const self = trapFocus(dialog, { focusSelf: true });
    expect(document.activeElement).toBe(stop);
    self.destroy();
    // One the user opened takes it.
    const asked = trapFocus(dialog);
    expect(document.activeElement).toBe(dialog.querySelector('button'));
    asked.destroy();
  });

  it('joins a Tab-only exempt region (the window controls) to the cycle without keeping the focus there', () => {
    const controls = document.createElement('div');
    controls.setAttribute('data-focus-trap-exempt', 'tab');
    controls.innerHTML = '<button>Minimise</button>';
    const dialog = document.createElement('div');
    dialog.tabIndex = -1;
    dialog.innerHTML = '<button>OK</button>';
    document.body.append(controls, dialog);
    const minimise = controls.querySelector('button')!;
    minimise.focus();
    // a modal that opened by itself takes the focus from the window controls
    const self = trapFocus(dialog, { focusSelf: true });
    expect(document.activeElement).toBe(dialog);
    // Tab from the dialog's last control reaches them, and the focus may stay there
    const ok = dialog.querySelector('button')!;
    ok.focus();
    const e = new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true });
    ok.dispatchEvent(e);
    expect(e.defaultPrevented).toBe(true);
    expect(document.activeElement).toBe(minimise);
    self.destroy();
  });

  it('canRestoreFocus checks ancestors too', () => {
    const wrap = document.createElement('div');
    const b = document.createElement('button');
    wrap.append(b);
    document.body.append(wrap);
    expect(canRestoreFocus(b)).toBe(true);
    wrap.setAttribute('aria-hidden', 'true');
    expect(canRestoreFocus(b)).toBe(false);
    wrap.removeAttribute('aria-hidden');
    b.style.visibility = 'hidden';
    expect(canRestoreFocus(b)).toBe(false);
    expect(canRestoreFocus(null)).toBe(false);
  });
});

describe('Dialog (the trap in use): Studio header, body and footer', () => {
  const html = (markup: string) => createRawSnippet(() => ({ render: () => markup }));

  it('names itself by its title, or by ariaLabel when the visible title differs', () => {
    const onclose = vi.fn();
    const first = render(Dialog, { props: { title: 'Settings', onclose, children: html('<p>Body</p>') } });
    expect(screen.getByRole('dialog', { name: 'Settings' })).toBeTruthy();
    first.unmount();
    render(Dialog, {
      props: {
        title: 'Add',
        ariaLabel: 'Add video or stream',
        onclose,
        children: html('<input aria-label="Video page or stream address" data-autofocus />'),
        headerAside: html('<span class="mode">Files · Video or stream</span>'),
        icon: html('<svg data-testid="tile"></svg>'),
      },
    });
    const dialog = screen.getByRole('dialog', { name: 'Add video or stream' });
    expect(dialog.getAttribute('aria-labelledby')).toBeNull();
    expect(dialog.querySelector('h2')?.textContent).toBe('Add');
    const head = dialog.querySelector('header') as HTMLElement;
    // icon, title, aside, then the close button at the end
    expect(Array.from(head.children).map((c) => c.tagName.toLowerCase() + (c.className.includes('aside') ? '.aside' : ''))).toEqual([
      'span',
      'h2',
      'div.aside',
      'button',
    ]);
    expect(head.querySelector('[data-testid="tile"]')).not.toBeNull();
    // the field marked data-autofocus takes the focus (else the first control: the close button)
    expect(document.activeElement).toBe(screen.getByRole('textbox', { name: 'Video page or stream address' }));
  });

  it('sizes itself, keeps a fixed height, and lays out a flush body and a footer summary', () => {
    render(Dialog, {
      props: {
        title: 'Settings',
        onclose: vi.fn(),
        size: 'xl',
        fixedHeight: true,
        flush: true,
        children: html('<nav>Pages</nav>'),
        // a raw snippet has one root element
        footer: html('<span><button type="button" class="btn">Cancel</button><button type="submit" class="btn primary">Save</button></span>'),
        footerStart: html('<span>2 links · <b>Main queue</b></span>'),
      },
    });
    const dialog = screen.getByRole('dialog', { name: 'Settings' });
    expect(dialog.style.inlineSize.startsWith('min(60rem, ')).toBe(true);
    // the least height in rem (it grows with the text, and with a longer page up to the room the
    // window has), never more than that room
    expect(dialog.style.minBlockSize.startsWith('min(46rem, ')).toBe(true);
    expect(dialog.style.blockSize).toBe('');
    expect(dialog.parentElement?.classList.contains('fixed')).toBe(true);
    expect(dialog.querySelector('.body')?.classList.contains('flush')).toBe(true);
    const foot = dialog.querySelector('footer') as HTMLElement;
    expect(foot.firstElementChild?.classList.contains('summary')).toBe(true);
    expect(foot.textContent).toContain('2 links · Main queue');
    expect(Array.from(foot.querySelectorAll('button')).map((b) => b.textContent)).toEqual(['Cancel', 'Save']);
  });

  it('keeps a given width over the size, and closes on Escape unless busy', async () => {
    const onclose = vi.fn();
    const { rerender } = render(Dialog, { props: { title: 'Move to queue', onclose, width: '30rem', size: 'sm', busy: true, children: html('<p>x</p>') } });
    const dialog = screen.getByRole('dialog', { name: 'Move to queue' });
    expect(dialog.style.inlineSize.startsWith('min(30rem, ')).toBe(true);
    await fireEvent.keyDown(dialog, { key: 'Escape' });
    expect(onclose).not.toHaveBeenCalled();
    await rerender({ busy: false });
    await fireEvent.keyDown(dialog, { key: 'Escape' });
    expect(onclose).toHaveBeenCalledTimes(1);
  });
});
