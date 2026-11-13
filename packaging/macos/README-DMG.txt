Downex @VERSION@ for macOS
==========================

Downex needs macOS 13 Ventura or newer, on Apple silicon or Intel.


1. Install
----------

Drag Downex onto the Applications folder in this window.


2. Open it the first time
-------------------------

Open Downex from Applications.

A build without a Developer ID signature (the default when Downex is built from source) is
blocked by Gatekeeper the first time, with a message that Apple cannot check it or that
the developer cannot be verified:

- macOS 13 and 14: right-click (Control-click) Downex in Applications, choose "Open", then
  "Open" again in the dialog.
- macOS 15 and later: open it once and close the message, then go to System Settings ->
  Privacy & Security, scroll down to the message about Downex and click "Open Anyway".

Browsers and the login item start Downex's helper programs themselves, outside the app.
If they are blocked as well, or macOS says that Downex "is damaged", remove the quarantine
flag that the download put on the app (in Terminal):

    xattr -dr com.apple.quarantine /Applications/Downex.app


3. Connect the browsers
-----------------------

Once per user, in Terminal (again after moving the app):

    /Applications/Downex.app/Contents/Helpers/downex-nmhost register

Then add the browser extension to each browser as described in EXTENSION.txt inside the app:

    open -e /Applications/Downex.app/Contents/Resources/EXTENSION.txt


4. Command line (optional)
--------------------------

The command-line client is /Applications/Downex.app/Contents/Helpers/downex. To run it as
"downex", link it into a folder on your PATH, for example:

    sudo mkdir -p /usr/local/bin
    sudo ln -sf /Applications/Downex.app/Contents/Helpers/downex /usr/local/bin/downex
