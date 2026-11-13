// Chooses the Api implementation.
//
// A production build (`pnpm build`, embedded by the Wails host) only ever talks to the
// Wails bindings. If they do not appear, start-up fails visibly (see startup.ts) rather
// than showing invented downloads, and the simulated daemon (./mock) is not even part of
// dist/: the branch that imports it is removed at build time.
//
// The simulated daemon is available in development (`pnpm dev`, tests) and in a demo
// build made with VITE_DOWNEX_API=mock. VITE_DOWNEX_API ("wails" | "mock" | "auto"):
// - auto (the development default): the Wails bindings when window.go.main.App is
//   present (or appears shortly, when served by the Wails dev server), else the mock;
// - wails: the bindings or a start-up error; mock: always the simulated daemon.
// `?mock=<n>` in the URL sets the number of simulated downloads (e.g. ?mock=10000; without it
// the list is the design's Studio scene), `?progress=<id>` shows the progress window of a
// simulated download, and `?fixture=<tokens>` sets up a dialog state for the layout checks
// (scripts/measure-dialogs.mjs; MockOptions.fixture). There, opening a progress window opens
// such a page in a small browser window. The simulated main window reports a custom title bar
// (customTitleBar), so a browser shows the window controls as the frameless Windows window has them.

import type { Api } from './api';
import { createWailsApi, wailsBindings } from './wails';

/** How long the page waits for the Wails runtime to inject its bindings. */
export const BINDINGS_TIMEOUT_MS = 5000;

export class StartupError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'StartupError';
  }
}

function servedByWails(): boolean {
  const { protocol, hostname } = window.location;
  return protocol === 'wails:' || hostname === 'wails.localhost' || hostname === 'wails';
}

async function waitForBindings(timeoutMs: number): Promise<ReturnType<typeof wailsBindings>> {
  const deadline = Date.now() + timeoutMs;
  let found = wailsBindings();
  while (!found && Date.now() < deadline) {
    await new Promise((r) => setTimeout(r, 50));
    found = wailsBindings();
  }
  return found;
}

function missingBindings(): StartupError {
  return new StartupError('The Downex host bindings (window.go.main.App) are not available.');
}

export interface ResolveOptions {
  timeoutMs?: number;
}

export async function resolveApi(options: ResolveOptions = {}): Promise<Api> {
  const timeoutMs = options.timeoutMs ?? BINDINGS_TIMEOUT_MS;
  // Both operands are replaced at build time; in a normal production build the
  // condition is constant false and the mock chunk is dropped.
  if (import.meta.env.DEV || import.meta.env.VITE_DOWNEX_API === 'mock') {
    const choice = String(import.meta.env.VITE_DOWNEX_API ?? 'auto').toLowerCase();
    if (choice !== 'mock') {
      const bindings = await waitForBindings(choice === 'wails' || servedByWails() ? timeoutMs : 0);
      if (bindings) return createWailsApi(bindings.app, bindings.runtime);
      if (choice === 'wails') throw missingBindings();
    }
    const { createMockApi } = await import('./mock');
    const query = new URLSearchParams(window.location.search);
    const param = query.get('mock');
    const count = param !== null && /^\d+$/.test(param) ? Math.min(100_000, Number(param)) : undefined;
    const progress = Number(query.get('progress') ?? '');
    const fixture = query.get('fixture') ?? undefined;
    return createMockApi({
      count,
      fixture,
      latencyMs: 40,
      view:
        Number.isSafeInteger(progress) && progress > 0
          ? { view: 'progress', id: progress, openWith: true, hibernate: true, wakeTimers: true, customTitleBar: false }
          : { view: 'main', openWith: true, hibernate: true, wakeTimers: true, customTitleBar: true },
      onOpenProgressWindow: (id) => {
        const url = new URL(window.location.href);
        url.searchParams.set('progress', String(id));
        window.open(url.href, `downex-progress-${id}`, 'popup,width=520,height=300');
      },
      onCloseWindow: () => window.close(),
    });
  }
  const bindings = await waitForBindings(timeoutMs);
  if (bindings) return createWailsApi(bindings.app, bindings.runtime);
  throw missingBindings();
}
