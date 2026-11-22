// The Objective-C side of ui/mac's program part (program.go): what a window process adds to
// the windows of window_darwin.m: the application's delegate (the system's request to quit),
// the menu bar, activation, distributed notifications (today's Wails hand-off), the window's
// placement and frame, drops on the content view, the system dialogs. Every function runs on
// the main thread unless it says otherwise. Names start with dxg (the program part's prefix).
#ifndef DOWNEX_UI_MAC_PROG_H
#define DOWNEX_UI_MAC_PROG_H

#include <stdint.h>
#include <stdbool.h>

// dxgInstall sets the application's delegate (applicationShouldTerminate: answers
// NSTerminateLater and calls goProgTerminate; the last window's close never quits) and the
// menu bar: the app menu (About, Settings… when settings, Hide, Quit), File (Close Window),
// Edit and Window. aboutTitle and aboutMessage are the About dialog's text.
void dxgInstall(bool settings, const char *aboutTitle, const char *aboutMessage);
// dxgReplyTerminate lets a termination postponed by NSTerminateLater proceed (yes) or not.
void dxgReplyTerminate(bool yes);

// Activation (dock_darwin.m's rules): dxgRequestActivation asks for the activation on macOS
// 14+ (activate), dxgActivate activates (activate, or activateIgnoringOtherApps: before 14),
// dxgRaiseIfInactive puts the visible windows in front of other apps' after delay seconds
// when the app is not active then, dxgYieldActivation lets the app of a bundle take the focus
// (any thread: it waits at most 250 ms for the main thread).
void dxgRequestActivation(void);
void dxgActivate(void);
void dxgRaiseIfInactive(double delay);
void dxgYieldActivation(const char *bundlePath);

// Distributed notifications (Wails v2.17's hand-off on macOS): dxgPost posts name with the
// UTF-8 string object (any thread); dxgObserve calls goProgDistributed(token, object) on the
// main thread for each notification name delivers, until dxgUnobserve(the returned id).
void dxgPost(const char *name, const char *object);
void *dxgObserve(const char *name, uintptr_t token);
void dxgUnobserve(void *observer);

// dxgTempDir is NSTemporaryDirectory() (malloc'd UTF-8), where Wails puts its locks.
char *dxgTempDir(void);
// dxgPreferredLanguage is [NSLocale preferredLanguages][0] (malloc'd UTF-8) or NULL.
char *dxgPreferredLanguage(void);
// dxgSystemDark reports the system's dark appearance (NSApp's effective appearance).
bool dxgSystemDark(void);

// Windows (DXWindow pointers of window_darwin.m).
void dxgWindowTrack(void *win);          // remember the restored (not zoomed) content size
void dxgWindowNormalSize(void *win, double *w, double *h);
void dxgWindowZoom(void *win);           // zoom before the window shows
void dxgWindowFrameSize(void *win, double *w, double *h);
void dxgSystemFrameSize(double *w, double *h);
void dxgWindowFront(void *win, bool activate);
// dxgWindowRaise puts win in front of other apps' windows without activating the app (a
// window of a download the user just started in the browser): the app is unhidden, win is
// deminiaturised, moves to the active Space and is ordered front at once, then again after
// each activation of an app that is not Downex for seconds, until win becomes key or closes
// (seconds <= 0: only at once). A later call replaces an earlier one's following.
void dxgWindowRaise(void *win, double seconds);
// dxgWindowRaiseState reports whether dxgWindowRaise still follows the activations for win
// and whether win moves to the active Space (tests and tools).
void dxgWindowRaiseState(void *win, bool *following, bool *activeSpace);
long dxgWindowNumber(void *win);
bool dxgWindowFullScreen(void *win);
// dxgKeyWindowHandle is the handle of the key window (else the main window, else the front
// visible window) when it is a DXWindow, else 0.
uintptr_t dxgKeyWindowHandle(void);

// dxgAlertCritical shows a critical alert (a fatal error before any window).
void dxgAlertCritical(const char *title, const char *message);

// Inspection for tests and tools: dxgMenuDump lists the menu bar (malloc'd, one line per
// item: menu, title, key, modifier mask, action, separated by tabs); dxgPerformKey sends a
// key equivalent to the menu bar and reports whether an item took it.
char *dxgMenuDump(void);
char *dxgMenuAutoenabling(void); // the menus whose items AppKit enables (tests)
bool dxgPerformKey(const char *key, unsigned long mods);
// More inspection (any thread: they run on the main thread, also inside a modal loop):
// dxgPerformItem performs the item title of the menu bar's menu (its action runs, a modal
// one inside the call) and reports whether there is such an item; dxgModalState describes
// the dialog in front (malloc'd, "" for none); dxgPostKey puts a key press (characters,
// virtual key code) in the application's event queue, as typed, for the dialog in front or
// the key window; dxgAppState reports active, hidden; dxgWindowState a window's key,
// visible, miniaturised; dxgSendQuitEvent sends this process the quit Apple event (what
// logout, restart, shut down and the Dock's Quit send).
bool dxgPerformItem(const char *menu, const char *title);
char *dxgModalState(void);
void dxgPostKey(const char *key, unsigned short code, unsigned long mods);
// dxgPressModalButton clicks the button title of the dialog in front (any thread); false
// when there is none.
bool dxgPressModalButton(const char *title);
void dxgAppState(bool *active, bool *hidden);
void dxgWindowState(void *win, bool *key, bool *visible, bool *mini);
bool dxgSendQuitEvent(void);
// dxgTerminate calls [NSApp terminate:] on the main thread (any thread; tests).
void dxgTerminate(void);

#endif
