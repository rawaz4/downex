// The overlay UI of a frame (VIDEO-DETECT-SPEC §5.1, §5.2): the Download buttons over its videos
// and their panel. An unlisted script, built to /video-ui.js (VIDEO_UI_SCRIPT) and injected by
// the background into one frame on `downex:video-ui`, so it needs no web-accessible resource. It
// finds the frame's tracker on `globalThis[Symbol.for('downex.video')]` (the same isolated
// world) and mounts only when the handshake's version matches.

import { defineUnlistedScript } from 'wxt/utils/define-unlisted-script';
import { startOverlayUi } from '../lib/overlay-host';
import { TRACKER_KEY } from '../lib/video-tracker';

export default defineUnlistedScript(() => {
  const api = (globalThis as Record<symbol, unknown>)[TRACKER_KEY];
  // The user's sign-in needs no prompt here: the background asks again with it after an
  // `auth_required` when the option is on (SITE-MEDIA-SPEC §6.3).
  startOverlayUi(api, { doc: document, location: () => location.href });
});
