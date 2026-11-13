Adding the Downex browser extension
===================================

Downex takes over downloads from Firefox, Edge, Chrome, Brave and Chromium through its
browser extension. Browsers do not let programs install extensions themselves, so it has to
be added once in each browser. Setup has already registered Downex with the browsers (the
native-messaging host "com.downex.host"); only the extension itself is missing.

Firefox and Edge install it from their add-on sites once Downex is listed there. Chrome,
Brave and Chromium (and Firefox and Edge until then) load it from the "extension" folder of
the Downex installation, next to this file. By default that is:

    %LOCALAPPDATA%\Programs\Downex\extension\chrome-mv3     (Chrome, Edge, Brave, Chromium)
    %LOCALAPPDATA%\Programs\Downex\extension\firefox-mv3    (Firefox)

Do not move or delete that folder while the extension is loaded from it.


Firefox
-------

1. Open https://addons.mozilla.org, search for "Downex" and click "Add to Firefox".
2. Firefox lists what the add-on may do and the data it hands to the Downex program on this
   computer; click "Add" to install it. Firefox keeps it and updates it from then on.

Until Downex is listed there, Firefox can load it from the installation for one session
only (release versions of Firefox keep only add-ons that Mozilla has signed):

1. Open about:debugging#/runtime/this-firefox
2. Click "Load Temporary Add-on..." and select the file "manifest.json" in the folder
   "extension\firefox-mv3" of the Downex installation.

Firefox removes temporary add-ons when it closes; repeat these steps after a restart, or
install the listed add-on as soon as it is there.


Edge
----

Once Downex is listed there, open https://microsoftedge.microsoft.com/addons, search for
"Downex" and click "Get". Edge keeps it and updates it from then on. If its button then
says that Downex does not accept this copy, this version of Downex does not know the listed
copy yet: remove it, and load Downex from the installation folder as below until Downex has
updated.

Until Downex is listed there, load it from the installation folder as described for Chrome
below, on edge://extensions.


Chrome, Brave and Chromium (and Edge until Downex is listed)
------------------------------------------------------------

1. Open the extensions page:
   Chrome:   chrome://extensions
   Edge:     edge://extensions
   Brave:    brave://extensions
2. Turn on "Developer mode" (a switch at the top right in Chrome, on the left in Edge).
3. Click "Load unpacked" and select the folder "extension\chrome-mv3" of the Downex
   installation.
4. The extension "Downex" appears with the ID

       dlgpbdafcmciojddbbjikgeeefipejcn

   Downex accepts a copy loaded from this folder only with this ID. If a different ID is
   shown, the folder was not the one from this installation.

Keep "Developer mode" on. Chrome 134 and later turn off extensions loaded this way when it is
switched off, and downloads then stay in the browser. The browser may warn about
developer-mode extensions when it starts; that is expected for extensions loaded this way.


Checking that it works
----------------------

Click the Downex button in the browser's toolbar (it may be hidden behind the extensions
(puzzle) icon). It shows whether Downex is reachable. When it is not, start Downex from the
Start menu once, then try again.


After updating Downex
---------------------

Setup replaces the files in the "extension" folder.

- Installed from addons.mozilla.org or Edge Add-ons: the browser updates the extension
  itself; nothing to do.
- Loaded from the folder (Chrome, Brave, Chromium, or Edge before the listing): click the
  reload arrow of the Downex card on the extensions page (or restart the browser).
- Loaded as a temporary add-on in Firefox: load it again.


Uninstalling
------------

Remove the extension in the browser (extensions page, "Remove"). The Downex uninstaller
removes the browser registration and the program files, but cannot remove extensions from
browsers.
