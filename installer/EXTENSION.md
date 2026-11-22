Adding the Downex browser extension
===================================

Downex takes over downloads from Chrome, Edge, Brave and Chromium (based on Chromium 116 or
later) and Firefox (140 or later) through its browser extension. Browsers do not let programs
install extensions themselves, so you add it once in each browser. Setup has already
registered Downex with these browsers (the native-messaging host "com.downex.host"); only the
extension itself is missing.


Add it from the store (recommended)
-----------------------------------

Chrome, Edge, Brave and Chromium ("Add to Chrome"):

    https://chromewebstore.google.com/detail/llnofffhagcfejjcephcilcbndnhnbeg

The listing also installs in Opera, Vivaldi and other Chromium browsers, but Setup registers
Downex only with the browsers named above, so there the extension may say that Downex isn't
set up.

Firefox ("Add to Firefox"):

    https://addons.mozilla.org/firefox/addon/downex/

Open the link in the browser, click "Add to Chrome" (or "Add to Firefox"), then pin the
extension: click the extensions (puzzle) icon in the toolbar, then the pin next to Downex.
Downex's Settings > Integration has the same two buttons. Store copies update themselves.

The Chrome Web Store copy has no YouTube download button: the store does not allow one. Every
other site works the same, and the Firefox add-on keeps the button. Want the YouTube button in
Chrome? The guide "The YouTube button in Chrome" shows how to install the full copy that comes
with Downex:

    https://downex.app/chrome-youtube

Keep one copy per browser. If you install the copy from the folder (below), remove the Chrome
Web Store copy first, or both take over downloads.


Advanced: install from the folder
---------------------------------

The full copy of the extension is in the "extension" folder of the Downex installation, next
to this file. By default that is:

    %LOCALAPPDATA%\Programs\Downex\extension\chrome-mv3     (Chrome, Edge, Brave, Chromium)
    %LOCALAPPDATA%\Programs\Downex\extension\firefox-mv3    (Firefox)

If you installed Downex in another folder, use that one. In Downex, Settings > Integration >
"Advanced: install from the folder" shows both folders, with "Copy path" and "Open folder"
buttons. In the browser's file window, paste the path into the address bar at the top and
press Enter.

Do not move, rename or delete that folder while the extension is loaded from it.

Chrome, Edge, Brave and Chromium:

1. Remove the Chrome Web Store copy of Downex, if you have it.
2. Open the extensions page (type it in the address bar and press Enter):
   Chrome:   chrome://extensions
   Edge:     edge://extensions
   Brave:    brave://extensions
   Chromium: chrome://extensions
3. Turn on "Developer mode" (a switch at the top right; in Edge, in the left pane).
4. Click "Load unpacked" and select the folder "extension\chrome-mv3" of the Downex
   installation.
5. The extension "Downex" appears with the ID

       dlgpbdafcmciojddbbjikgeeefipejcn

   Downex accepts a copy loaded from this folder only with this ID. If a different ID is
   shown, the folder was not the one from this installation.
6. Pin it: click the extensions (puzzle) icon in the toolbar, then the pin next to Downex
   (in Edge, the eye icon, "Show in toolbar").

Keep "Developer mode" on. Recent versions of Chrome turn off extensions loaded this way when
it is switched off, and downloads then stay in the browser. The browser may show a notice
about developer-mode extensions when it starts; that is normal, and it is safe to keep Downex.

Firefox (temporary add-on, until Firefox closes): remove the Firefox Add-ons copy first, open
about:debugging#/runtime/this-firefox, click "Load Temporary Add-on..." and select the file
"manifest.json" in the folder "extension\firefox-mv3". The Firefox Add-ons copy above is the
one to keep.


Checking that it works
----------------------

Click the Downex button in the browser's toolbar (it may be hidden behind the extensions
(puzzle) icon). It shows "Connected" when it reaches Downex.

- "Downex isn't running": start Downex from the Start menu, then click "Check again".
- "Downex isn't set up in this browser": in Downex, open Settings > Integration and click
  "Register with browsers again", then "Check again". If it still says so, restart the
  browser.
- "Downex doesn't accept this extension": the extension came from another build of Downex,
  so its ID is wrong. Remove it, then add it from the store, or install the latest Downex.
- Downloads are taken twice, or by the wrong copy: two copies of the extension are
  installed in one browser. Keep one and remove the other on the extensions page.
- The copy loaded from the folder is gone or off in Chrome, Edge, Brave or Chromium:
  "Developer mode" was switched off. Turn it on again, then turn the Downex card on (or load
  the folder again).
- The temporary add-on is gone from Firefox: it goes when Firefox closes. Add Downex from
  Firefox Add-ons instead, and it stays.


After updating Downex
---------------------

- Store copies (Chrome Web Store, Firefox Add-ons) update themselves.
- A copy loaded from the folder: Setup replaces the files in the "extension" folder. Click
  the reload arrow of the Downex card on the extensions page (or restart the browser); a
  Firefox temporary add-on is loaded again.


Uninstalling
------------

Remove the extension in the browser (extensions page, "Remove"). The Downex uninstaller
removes the browser registration and the program files, but cannot remove extensions from
browsers.

Help: support@downex.app
