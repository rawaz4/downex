// Start-up: resolves the Api, then mounts the app. A failure (for example the Wails
// bindings never appeared) replaces the page with an alert that explains it and offers
// to try again, instead of falling back to anything that looks like real data.

import type { Api, StartupView } from './api';

export interface BootOptions {
  target: HTMLElement;
  resolve: () => Promise<Api>;
  mount: (api: Api) => void | Promise<void>;
}

/**
 * What this window shows: the main list, or the progress window of one download
 * ("downex-ui --progress <id>"). A host that cannot tell shows the main list, and offers
 * nothing it may not have (Open with…).
 */
export async function startupView(api: Api): Promise<StartupView> {
  try {
    return await api.startup();
  } catch {
    return { view: 'main', openWith: false, hibernate: false, wakeTimers: false };
  }
}

export async function boot(options: BootOptions): Promise<void> {
  const { target, resolve, mount } = options;
  let api: Api;
  try {
    api = await resolve();
  } catch (err) {
    showFailure(target, err instanceof Error ? err.message : String(err), () => boot(options));
    return;
  }
  target.replaceChildren();
  await mount(api);
}

function el<K extends keyof HTMLElementTagNameMap>(tag: K, className: string, text?: string): HTMLElementTagNameMap[K] {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
}

export function showFailure(target: HTMLElement, message: string, retry: () => Promise<void>): void {
  const box = el('div', 'fatal');
  box.setAttribute('role', 'alert');
  const title = el('h1', '', 'Downex could not start');
  const explain = el(
    'p',
    '',
    'This window could not connect to the Downex application. It may still be starting, or the page was opened outside Downex.',
  );
  const detail = el('p', '');
  detail.append('Details: ', el('code', '', message));
  const button = el('button', 'btn primary', 'Try again');
  button.type = 'button';
  let busy = false;
  button.addEventListener('click', () => {
    if (busy) return;
    busy = true;
    button.setAttribute('aria-disabled', 'true');
    button.textContent = 'Trying again…';
    void retry();
  });
  box.append(title, explain, detail, button);
  target.replaceChildren(box);
  button.focus();
}
