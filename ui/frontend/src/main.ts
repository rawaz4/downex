import './app.css';
import { mount } from 'svelte';
import App from './App.svelte';
import ProgressWindow from './ProgressWindow.svelte';
import { resolveApi } from './lib/backend';
import { setFormatLocale } from './lib/format';
import { boot, startupView } from './lib/startup';
import { theme } from './lib/stores/theme.svelte';

const target = document.getElementById('app');
if (!target) throw new Error('missing #app');

try {
  setFormatLocale(navigator.language || 'en-US');
} catch {
  // unsupported locale tag: keep the default
}

// Layout direction for testing RTL before translations land: ?dir=rtl
const dir = new URLSearchParams(window.location.search).get('dir');
if (dir === 'rtl' || dir === 'ltr') document.documentElement.dir = dir;

void boot({
  target,
  resolve: () => resolveApi(),
  // "downex-ui --progress <id>" is the progress window of one download (DESKTOP-SPEC §7).
  mount: async (api) => {
    // The native parts of this window (a progress window's title bar, macOS's appearance) take the
    // app's theme now and on every change, also one made in another window (DESIGN-SYSTEM.md §4.8).
    theme.attach(api);
    const view = await startupView(api);
    if (view.view === 'progress') {
      document.documentElement.dataset.view = 'progress';
      mount(ProgressWindow, { target, props: { api, id: view.id, openWith: view.openWith } });
    } else {
      mount(App, { target, props: { api, capabilities: view, build: view.build } });
    }
  },
});
