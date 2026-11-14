# Changelog

All notable changes to Downex are listed here, newest first. Versions follow
[semantic versioning](https://semver.org).

## 1.0.0

The first public release of Downex, a free and open-source download manager for Windows and
macOS (GPL-3.0-or-later).

- **Downloads:** each file over up to 32 connections, HTTP/2, speed limits for everything, per
  queue and per download, and per-site connection limits. Progress is saved only once it is on
  disk, so a download continues after a pause, a quit, a crash or a restart; a file that changed
  on the server is detected and never mixed with the old one.
- **Videos and streams:** unencrypted HLS and DASH streams, and the video of a web page (YouTube
  and the other sites yt-dlp supports), saved as one `.mp4` file in the quality you pick, without
  re-encoding. Downex fetches and verifies yt-dlp and Deno from their publishers the first time
  they are needed. Encrypted and DRM-protected content is refused.
- **Browser extension** for Firefox, Edge, Chrome, Brave and Chromium: it hands downloads and
  videos to Downex with the page's sign-in, and can ask first for the name, folder and category.
- **Desktop app:** a native window on Windows and macOS with a progress window per download,
  queues with schedules and an action when they finish, categories with their own folders, a
  command palette, tray and menu-bar icons, notifications, a clipboard monitor, proxies and site
  logins, a virus-scan hook, and light and dark themes.
- **Command line:** `downex` does everything the window does.
- **Updates:** Downex checks its signed update feed and asks in its window whether to update now
  or later.
- **Privacy:** no account, no telemetry; the window, the command line and the extension talk to
  the background service only on your own computer.

Known limits: the Windows Setup is not code-signed and the macOS app is not notarised (see the
README for the first start); Windows on Arm and Linux have no release yet; the browser extension
is in no browser store yet (it is not on the Chrome Web Store, and the Firefox Add-ons and Edge
Add-ons listings are not ready), so it is added by hand: loaded unpacked in Developer mode in
Chrome, Edge, Brave and Chromium, and as a temporary add-on in Firefox, which removes it when
Firefox closes (see the README).
