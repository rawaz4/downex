Adding the Downex browser extension
===================================

Downex takes over downloads from Chrome, Edge, Brave, Chromium (based on Chromium 116 or
later) and Firefox (140 or later) through its browser extension. Browsers do not let programs install
extensions themselves, so you add it by hand, once in each browser. Setup has already
registered Downex with these browsers (the native-messaging host "com.downex.host"); only the
extension itself is missing.

The extension is not in any browser store yet. It is not on the Chrome Web Store, and the
listings on Firefox Add-ons and Edge Add-ons are planned but not ready. For now, load it from
the "extension" folder of the Downex installation, next to this file. By default that is:

    %LOCALAPPDATA%\Programs\Downex\extension\chrome-mv3     (Chrome, Edge, Brave, Chromium)
    %LOCALAPPDATA%\Programs\Downex\extension\firefox-mv3    (Firefox)

If you installed Downex in another folder, use that one. In Downex, Settings > Integration
shows both folders, with "Copy path" and "Open folder" buttons. In the browser's file window,
paste the path into the address bar at the top and press Enter.

Do not move, rename or delete that folder while the extension is loaded from it.


Chrome, Edge, Brave and Chromium
--------------------------------

1. Open the extensions page (type it in the address bar and press Enter):
   Chrome:   chrome://extensions
   Edge:     edge://extensions
   Brave:    brave://extensions
   Chromium: chrome://extensions
2. Turn on "Developer mode" (a switch at the top right; in Edge, in the left pane).
3. Click "Load unpacked" and select the folder "extension\chrome-mv3" of the Downex
   installation.
4. The extension "Downex" appears with the ID

       dlgpbdafcmciojddbbjikgeeefipejcn

   Downex accepts a copy loaded from this folder only with this ID. If a different ID is
   shown, the folder was not the one from this installation.
5. Pin it: click the extensions (puzzle) icon in the toolbar, then the pin next to Downex
   (in Edge, the eye icon, "Show in toolbar").

Keep "Developer mode" on. Recent versions of Chrome turn off extensions loaded this way when
it is switched off, and downloads then stay in the browser. The browser may warn about
developer-mode extensions when it starts; that is expected, and it is safe to keep Downex.


Firefox
-------

Firefox keeps an add-on for good only when Mozilla has signed it. Downex is not signed yet,
so release and Beta versions of Firefox load it as a temporary add-on, until Firefox closes:

1. Open about:debugging#/runtime/this-firefox
2. Click "Load Temporary Add-on..." and select the file "manifest.json" in the folder
   "extension\firefox-mv3" of the Downex installation.
3. Pin it: click the extensions (puzzle) icon in the toolbar, then the gear next to Downex,
   and choose "Pin to Toolbar".

Firefox removes temporary add-ons when it closes; repeat these steps after each restart.

Firefox Developer Edition, Nightly and ESR can keep it: set xpinstall.signatures.required to
false in about:config, copy the folder "extension\firefox-mv3" to another place, zip the
files inside it (manifest.json at the top of the zip) into a file named downex.xpi, and
install that file from about:addons (gear icon, "Install Add-on From File..."). Make the zip
in File Explorer (select all, then Send to > Compressed (zipped) folder) or with 7-Zip, not
with PowerShell's Compress-Archive. First turn on View > Show > File name extensions in File
Explorer, or the renamed file becomes downex.xpi.zip. Make a new downex.xpi after each
Downex update.


Checking that it works
----------------------

Click the Downex button in the browser's toolbar (it may be hidden behind the extensions
(puzzle) icon). It shows "Connected" when it reaches Downex.

- "Downex isn't running": start Downex from the Start menu, then click "Check again".
- "Downex isn't set up in this browser": in Downex, open Settings > Integration and click
  "Register with browsers again", then "Check again". If it still says so, restart the
  browser.
- "Downex doesn't accept this extension": the extension came from another build of Downex,
  so its ID is wrong. Remove it, then load the folder "extension\chrome-mv3" of this
  installation, or install the latest Downex.
- The extension is gone or off in Chrome, Edge, Brave or Chromium: "Developer mode" was
  switched off. Turn it on again, then turn the Downex card on (or load the folder again).
- The add-on is gone from Firefox: a temporary add-on goes when Firefox closes; load it
  again.


After updating Downex
---------------------

Setup replaces the files in the "extension" folder.

- Chrome, Edge, Brave, Chromium: click the reload arrow of the Downex card on the
  extensions page (or restart the browser).
- Firefox, temporary add-on: load it again.
- Firefox, downex.xpi: make and install a new one.


Uninstalling
------------

Remove the extension in the browser (extensions page, "Remove"). The Downex uninstaller
removes the browser registration and the program files, but cannot remove extensions from
browsers.

Help: support@downex.app
