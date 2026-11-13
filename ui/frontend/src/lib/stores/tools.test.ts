import { describe, expect, it, vi } from 'vitest';
import type { EventBatch, ToolInfo, ToolsStatus } from '../api';
import { MiB, makeTools } from '../testing';
import { ToolsStore, failedTools, installFraction, installText, isFirstInstall, isInstalling, toolName } from './tools.svelte';

function withTools(patch: (t: ToolInfo) => Partial<ToolInfo>, status: Partial<ToolsStatus> = {}): ToolsStatus {
  return makeTools({ tools: makeTools().tools.map((t) => ({ ...t, ...patch(t) })), ...status });
}

function fakeTools(answers: { get?: () => Promise<ToolsStatus>; install?: (update: boolean) => Promise<ToolsStatus> } = {}) {
  const listeners = new Set<(b: EventBatch) => void>();
  const api = {
    getTools: vi.fn(answers.get ?? (async () => makeTools())),
    installTools: vi.fn(answers.install ?? (async () => makeTools())),
    onEvents: vi.fn((cb: (b: EventBatch) => void) => {
      listeners.add(cb);
      return () => listeners.delete(cb);
    }),
  };
  const emit = (b: EventBatch) => {
    for (const l of listeners) l(b);
  };
  return { api, emit, store: new ToolsStore(api) };
}

describe('tools state (SITE-MEDIA-SPEC §4)', () => {
  it('measures the install over the tools being downloaded', () => {
    const installing = withTools((t) => ({ state: 'installing', version: '', bytesDone: t.name === 'deno' ? 30 * MiB : 6 * MiB, bytesTotal: t.name === 'deno' ? 45 * MiB : 18 * MiB }), { ready: false });
    expect(isInstalling(installing)).toBe(true);
    expect(installFraction(installing)).toBeCloseTo(36 / 63);
    expect(installText(installing)).toBe('Downloading yt-dlp and Deno: 57%');
    // a total not known yet: no percentage (unless the daemon's message gave one)
    const unknown = withTools((t) => ({ state: t.name === 'deno' ? 'installing' : 'ready', bytesTotal: -1 }), { ready: false });
    expect(installFraction(unknown)).toBeNaN();
    expect(installText(unknown)).toBe('Downloading Deno…');
    expect(installText(unknown, 0.4)).toBe('Downloading Deno: 40%');
    expect(installText(null)).toBe('Downloading yt-dlp and Deno…');
    // beside a progress bar the percentage is the bar's alone
    expect(installText(installing, Number.NaN, false)).toBe('Downloading yt-dlp and Deno…');
    expect(installFraction(makeTools())).toBeNaN();
    expect(toolName('deno')).toBe('Deno');
    expect(toolName('yt-dlp')).toBe('yt-dlp');
  });

  it('tells the first install from an update of working copies', () => {
    const first = withTools(() => ({ state: 'installing' }), { ready: false });
    expect(isFirstInstall(first)).toBe(true);
    expect(isFirstInstall({ ...first, ready: true })).toBe(false); // a ready copy keeps working
    expect(isFirstInstall({ ...first, enabled: false })).toBe(false);
    expect(isFirstInstall(null)).toBe(false);
    expect(failedTools(withTools((t) => (t.name === 'deno' ? { state: 'failed' } : {}))).map((t) => t.name)).toEqual(['deno']);
    expect(failedTools(null)).toEqual([]);
  });
});

describe('ToolsStore', () => {
  it('follows the tools events and reads the state', async () => {
    const { api, emit, store } = fakeTools();
    store.start();
    expect(store.status).toBeNull();
    const installing = withTools(() => ({ state: 'installing', bytesDone: 1, bytesTotal: 4 }), { ready: false });
    emit([{ kind: 'removed', id: 3 }, { kind: 'tools', status: installing }]);
    expect(store.status).toEqual(installing);
    expect(store.installing).toBe(true);
    expect(store.fraction).toBe(0.25);
    await store.load();
    expect(api.getTools).toHaveBeenCalledTimes(1);
    expect(store.status).toEqual(makeTools());
    expect(store.installing).toBe(false);
    store.dispose();
    emit([{ kind: 'tools', status: installing }]);
    expect(store.status).toEqual(makeTools());
  });

  it('knows a downexd without site media', async () => {
    const { store } = fakeTools({
      get: async () => {
        throw new Error('unimplemented: downex.v1.DaemonService.GetTools is not implemented');
      },
    });
    await store.load();
    expect(store.supported).toBe(false);
    expect(store.loadError).toBeNull();
  });

  it('reports a read that failed otherwise', async () => {
    const { store } = fakeTools({
      get: async () => {
        throw new Error('unavailable: downexd is not running');
      },
    });
    await store.load();
    expect(store.supported).toBe(true);
    expect(store.loadError).toBe('downexd is not running');
  });

  it('installs once at a time and says why an install failed', async () => {
    let finish: (s: ToolsStatus) => void = () => {};
    const { api, store } = fakeTools({ install: () => new Promise((r) => (finish = r)) });
    const first = store.install(false);
    expect(store.busy).toBe('install');
    expect(await store.install(true)).toBe(false); // joined: nothing more is asked
    expect(api.installTools).toHaveBeenCalledTimes(1);
    finish(makeTools());
    expect(await first).toBe(true);
    expect(store.busy).toBeNull();
    expect(store.status?.ready).toBe(true);

    api.installTools.mockRejectedValueOnce('failed_precondition: tools_unavailable: yt-dlp and Deno are not set up: yt-dlp failed its signature check');
    expect(await store.install(true)).toBe(false);
    expect(store.installError).toBe('Downex could not set up yt-dlp/Deno: yt-dlp failed its signature check.');
    expect(api.getTools).toHaveBeenCalled(); // what the failure left is read again
  });
});
