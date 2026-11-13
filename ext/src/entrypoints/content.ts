// Records the modifiers of the last link click and reports them to the background, which
// lets the browser keep an Alt-click download (BROWSER-SPEC §3 "Bypass").

import { browser } from 'wxt/browser';
import { defineContentScript } from 'wxt/utils/define-content-script';
import type { ClickMessage } from '../lib/messages';

export default defineContentScript({
  matches: ['http://*/*', 'https://*/*'],
  allFrames: true,
  runAt: 'document_start',
  main() {
    const report = (e: MouseEvent) => {
      if (!e.isTrusted) return;
      const target = e.composedPath()[0];
      const onLink = target instanceof Element && target.closest('a[href], area[href]') !== null;
      // Alt anywhere matters (script-started downloads); other clicks only on links.
      if (!onLink && !e.altKey) return;
      const msg: ClickMessage = {
        type: 'downex:click',
        alt: e.altKey,
        ctrl: e.ctrlKey,
        shift: e.shiftKey,
        meta: e.metaKey,
        ts: Date.now(),
      };
      try {
        browser.runtime.sendMessage(msg).catch(() => undefined);
      } catch {
        // extension reloaded: this orphaned script can no longer reach the background
      }
    };
    const opts = { capture: true, passive: true } as const;
    window.addEventListener('mousedown', report, opts);
    window.addEventListener('click', report, opts);
    window.addEventListener('auxclick', report, opts);
  },
});
