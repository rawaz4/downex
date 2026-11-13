// The Objective-C side of ui/mac's application and windows: AppKit objects only, every
// decision is Go's (window.go, app.go). Each window is known to Go by a handle (a
// runtime/cgo Handle value); Objective-C never holds a Go pointer.
#ifndef DOWNEX_UI_MAC_WINDOW_H
#define DOWNEX_UI_MAC_WINDOW_H

#include <stdint.h>
#include <stdbool.h>
#include <CoreGraphics/CoreGraphics.h>

// dxAppInit makes the shared NSApplication with the regular (Dock icon, menu bar) or the
// accessory activation policy and finishes its launch.
void dxAppInit(bool regular);
// dxAppRun runs the application's event loop until dxAppStop.
void dxAppRun(void);
// dxAppStop ends dxAppRun once the current event is handled.
void dxAppStop(void);
// dxAppWake makes the main thread call goAppPosts soon; any thread may call it.
void dxAppWake(void);
// dxAppDark reports whether the application's effective appearance is dark.
bool dxAppDark(void);
// dxAppReduceMotion reports the system's Reduce motion setting.
bool dxAppReduceMotion(void);

// dxWindowNew makes a window with the standard title bar whose content area is w x h
// points, hidden until dxWindowShow. It returns the NSWindow (+1, owned by Go until
// dxWindowRelease).
void *dxWindowNew(uintptr_t handle, double w, double h, const char *title);
void dxWindowShow(void *win, bool activate);
void dxWindowClose(void *win);
void dxWindowRelease(void *win);
void dxWindowSetTitle(void *win, const char *title);
void dxWindowSetMinSize(void *win, double w, double h);
void dxWindowSetMaxSize(void *win, double w, double h);
void dxWindowSetContentSize(void *win, double w, double h);
void dxWindowContentSize(void *win, double *w, double *h);
double dxWindowScale(void *win);
// dxWindowState reports zoomed (maximised) and miniaturized (minimised).
void dxWindowState(void *win, bool *zoomed, bool *mini);
void dxWindowCommand(void *win, int cmd); // 1 minimise, 2 zoom, 3 unzoom, 4 performClose
void dxWindowSetColors(void *win, double r, double g, double b, double a, bool dark);
// dxWindowPresent shows img (y-down pixels of the whole content area) as the content.
void dxWindowPresent(void *win, CGImageRef img, double scale);
// dxWindowRequestTick asks for one goWindowTick at the next display refresh.
void dxWindowRequestTick(void *win);
void dxWindowSetCursor(int cursor);
// dxWindowMouseLocation is the pointer's position in content points (y down).
void dxWindowMouseLocation(void *win, double *x, double *y);
bool dxWindowIsKey(void *win);

// Clipboard (general pasteboard), UTF-8; dxPasteboardText returns a malloc'd string or NULL.
char *dxPasteboardText(void);
bool dxPasteboardSetText(const char *s);

// dxWindowViewsPNG writes the content view with its native subviews as PNG (measuring aid).
bool dxWindowViewsPNG(void *win, const char *path);

#endif
