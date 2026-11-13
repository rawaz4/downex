import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Api } from './api';
import { StartupError, resolveApi } from './backend';
import { boot, startupView } from './startup';
import { fakeApi } from './testing';

afterEach(() => {
  vi.unstubAllEnvs();
  delete window.go;
  delete window.runtime;
  document.body.replaceChildren();
});

function installBindings(): void {
  const ok = async () => ({});
  window.go = {
    main: {
      App: {
        List: async () => [],
        Add: ok,
        Pause: ok,
        Resume: ok,
        Remove: ok,
        GetSettings: ok,
        UpdateSettings: ok,
        Status: ok,
        OpenFile: ok,
        OpenFolder: ok,
        OpenWith: ok,
        PickFolder: async () => '',
        Confirm: ok,
        AddMedia: ok,
        ProbeMedia: ok,
        PauseAll: ok,
        ResumeAll: ok,
        Get: ok,
        SetDownloadOptions: ok,
        OpenProgressWindow: ok,
        Startup: async () => ({ view: 'main', openWith: true }),
        PickFile: async () => '',
        ListQueues: ok,
        CreateQueue: ok,
        UpdateQueue: ok,
        DeleteQueue: ok,
        StartQueue: ok,
        StopQueue: ok,
        MoveDownloads: ok,
        CancelPostAction: ok,
        SetSecret: ok,
        ClearSecret: ok,
        CheckForUpdate: ok,
        InstallUpdate: ok,
      },
    },
  };
  window.runtime = { EventsOn: () => () => {} };
}

describe('resolveApi in a production build', () => {
  it('never falls back to the simulated daemon', async () => {
    vi.stubEnv('DEV', false);
    vi.stubEnv('PROD', true);
    await expect(resolveApi({ timeoutMs: 0 })).rejects.toBeInstanceOf(StartupError);
  });

  it('uses the Wails bindings, also when they appear late', async () => {
    vi.stubEnv('DEV', false);
    vi.stubEnv('PROD', true);
    setTimeout(installBindings, 60);
    const api = await resolveApi({ timeoutMs: 2000 });
    expect(api.kind).toBe('wails');
  });

  it('still offers the simulated daemon in development', async () => {
    const api = await resolveApi({ timeoutMs: 0 });
    expect(api.kind).toBe('mock');
    (api as Api & { dispose?: () => void }).dispose?.();
  });
});

describe('boot', () => {
  it('shows a start-up alert with a retry instead of an app', async () => {
    const target = document.createElement('div');
    document.body.append(target);
    const resolve = vi
      .fn<() => Promise<Api>>()
      .mockRejectedValueOnce(new StartupError('The Downex host bindings (window.go.main.App) are not available.'))
      .mockResolvedValueOnce(fakeApi());
    const mount = vi.fn();
    await boot({ target, resolve, mount });
    const alert = target.querySelector('[role="alert"]');
    expect(alert?.querySelector('h1')?.textContent).toBe('Downex could not start');
    expect(alert?.textContent).toContain('window.go.main.App');
    expect(mount).not.toHaveBeenCalled();
    const retry = target.querySelector('button')!;
    expect(document.activeElement).toBe(retry);

    retry.click();
    retry.click(); // a second click while retrying is ignored
    await vi.waitFor(() => expect(mount).toHaveBeenCalledTimes(1));
    expect(resolve).toHaveBeenCalledTimes(2);
    expect(target.querySelector('[role="alert"]')).toBeNull();
  });
});

describe('startupView', () => {
  it('tells the main window from a progress window, and falls back to the main one', async () => {
    const api = fakeApi();
    expect(await startupView(api)).toEqual({ view: 'main', openWith: true, hibernate: true, wakeTimers: true });
    api.view = { view: 'progress', id: 9, openWith: false, hibernate: false, wakeTimers: false };
    expect(await startupView(api)).toEqual({ view: 'progress', id: 9, openWith: false, hibernate: false, wakeTimers: false });
    api.startup = async () => {
      throw new Error('unimplemented: no Startup binding');
    };
    // A host that cannot tell is offered nothing it may not have.
    expect(await startupView(api)).toEqual({ view: 'main', openWith: false, hibernate: false, wakeTimers: false });
  });

  it('waits for an asynchronous mount', async () => {
    const target = document.createElement('div');
    document.body.append(target);
    const order: string[] = [];
    await boot({
      target,
      resolve: async () => fakeApi(),
      mount: async () => {
        await new Promise((r) => setTimeout(r, 10));
        order.push('mounted');
      },
    });
    order.push('booted');
    expect(order).toEqual(['mounted', 'booted']);
  });
});
