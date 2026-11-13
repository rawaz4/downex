import { fireEvent, render, screen } from '@testing-library/svelte';
import { tick } from 'svelte';
import { describe, expect, it, vi } from 'vitest';
import ConnectionBanner from './ConnectionBanner.svelte';

const STATUS = { version: '1.0.0', pid: 1, startedAt: 1, active: 0, queued: 0, endpoint: '' };

describe('ConnectionBanner', () => {
  it('explains the outage once assertively, with retry, countdown and raw details', async () => {
    const onretry = vi.fn();
    const now = () => 1_000;
    const { rerender } = render(ConnectionBanner, {
      props: { daemon: { ok: true, status: STATUS }, loadError: null, checking: false, nextCheckAt: 0, onretry, now },
    });
    expect(screen.queryByRole('region')).toBeNull();
    expect(screen.getByRole('alert').textContent).toBe('');

    await rerender({ daemon: { ok: false, error: 'dial tcp 127.0.0.1:47820: connect: connection refused' }, nextCheckAt: 3_000 });
    await tick();
    const banner = screen.getByRole('region', { name: /can’t reach downexd/ });
    expect(banner.textContent).toContain('downex daemon start');
    expect(banner.textContent).toContain('every 3 seconds');
    expect(banner.textContent).toContain('Next try in 2 s');
    expect(screen.getByRole('alert').textContent).toMatch(/^Lost connection to downexd/);
    // the countdown is not in a live region
    expect(banner.closest('[role="alert"],[role="status"],[aria-live]')).toBeNull();
    // the raw error is kept, behind a disclosure
    expect(banner.querySelector('details code')?.textContent).toContain('connection refused');

    await fireEvent.click(screen.getByRole('button', { name: 'Retry now' }));
    expect(onretry).toHaveBeenCalledTimes(1);

    await rerender({ checking: true });
    const retry = screen.getByRole('button', { name: 'Retry now' });
    expect(retry.getAttribute('aria-disabled')).toBe('true');
    expect(banner.textContent).toContain('Checking…');
    await fireEvent.click(retry);
    expect(onretry).toHaveBeenCalledTimes(1);

    await rerender({ daemon: { ok: true, status: STATUS }, checking: false });
    await tick();
    expect(screen.queryByRole('region')).toBeNull();
    expect(screen.getByRole('alert').textContent).toBe('');
    expect(screen.getByRole('status').textContent).toBe('Reconnected to downexd.');
  });

  it('covers a failed first load', async () => {
    render(ConnectionBanner, {
      props: { daemon: { ok: true, status: STATUS }, loadError: 'internal: list failed', checking: false, nextCheckAt: 0, onretry: vi.fn() },
    });
    await tick();
    expect(screen.getByRole('region', { name: /couldn’t load your downloads/ })).toBeTruthy();
    expect(screen.getByRole('alert').textContent).toContain('couldn’t load');
  });
});
