// The overlay UI of a frame (VIDEO-DETECT-SPEC §5.1, §5.2): the Download buttons over its videos
// and their panel. An unlisted script, built to /video-ui.js (VIDEO_UI_SCRIPT) and injected by
// the background into one frame on `downex:video-ui`, so it needs no web-accessible resource. It
// finds the frame's tracker on `globalThis[Symbol.for('downex.video')]` (the same isolated
// world) and mounts only when the handshake's version matches.

import { browser } from 'wxt/browser';
import { defineUnlistedScript } from 'wxt/utils/define-unlisted-script';
import { parseVideoOptions, VIDEO_OPTIONS_KEY } from '../lib/options-form';
import { startOverlayUi } from '../lib/overlay-host';
import { TRACKER_KEY } from '../lib/video-tracker';

export default defineUnlistedScript(() => {
  const api = (globalThis as Record<symbol, unknown>)[TRACKER_KEY];
  /** "Send my sign-in cookies when a video needs it", as stored. */
  let signIn = false;
  const readSignIn = (raw: unknown) => {
    signIn = parseVideoOptions(raw).useSignInForVideos;
  };
  const layer = startOverlayUi(api, {
    doc: document,
    location: () => location.href,
    allowSignIn: async () => {
      const got = await browser.storage.local.get(VIDEO_OPTIONS_KEY);
      await browser.storage.local.set({ [VIDEO_OPTIONS_KEY]: { ...parseVideoOptions(got[VIDEO_OPTIONS_KEY]), useSignInForVideos: true } });
      signIn = true;
    },
    signInAllowed: () => signIn,
  });
  if (!layer) return;
  void browser.storage.local.get(VIDEO_OPTIONS_KEY).then(
    (got) => readSignIn(got[VIDEO_OPTIONS_KEY]),
    () => undefined,
  );
  browser.storage.onChanged.addListener((changes, area) => {
    const change = changes[VIDEO_OPTIONS_KEY];
    if (area === 'local' && change) readSignIn(change.newValue);
  });
});
