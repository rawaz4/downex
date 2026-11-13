//go:build darwin && cgo

// The C interface of the macOS menu-bar icon (tray_darwin.m) for tray_darwin.go.
// Declarations only: tray_darwin.go exports Go functions, so its preamble (which
// includes this file) must not define anything.
//
// Functions marked "main thread" must be called on the process's main thread, where
// AppKit requires them; elsewhere they do nothing (and return NULL or an error). The
// others may be called from any thread.

#ifndef DOWNEX_TRAY_DARWIN_H
#define DOWNEX_TRAY_DARWIN_H

#include <stdint.h>

// downexAppStart results.
#define DOWNEX_APP_OK 0
#define DOWNEX_APP_NOT_MAIN 1   // not called on the main thread
#define DOWNEX_APP_NO_SESSION 2 // no window-server (GUI) session: ssh, before login

// Kinds of goDownexNotifyResult.
#define DOWNEX_NOTIFY_DENIED 1 // the user has not allowed notifications
#define DOWNEX_NOTIFY_FAILED 2 // the notification center refused a notification

// downexIsMainThread reports whether the caller runs on the main thread.
int downexIsMainThread(void);
// downexHasWindowServer reports whether the process runs in a GUI session.
int downexHasWindowServer(void);

// downexAppStart sets up NSApplication as an accessory (no Dock icon), its delegate
// (also the notification center's) and an activity that keeps App Nap away. Main thread.
int downexAppStart(void);
// downexAppRun runs [NSApp run] until downexAppStop. Main thread.
void downexAppRun(void);
// downexAppStop ends downexAppRun (also when called before it).
void downexAppStop(void);
// downexAppReplyTerminate lets a termination postponed by NSTerminateLater proceed.
void downexAppReplyTerminate(void);

// downexWake calls goDownexTrayDrain(handle) on the main thread.
void downexWake(uintptr_t handle);
// downexRelease calls goDownexTrayReleased(handle) on the main thread, after every
// block queued before it.
void downexRelease(uintptr_t handle);

// downexStatusCreate adds a visible status item: the template image made of the
// PNG representations (points × points; png2x for Retina displays), the tooltip
// tip, and a menu whose first line is the disabled item line. Its menu items call
// goDownexTrayCommand(handle, tag). Returns the retained status object, or NULL.
// Main thread.
void *downexStatusCreate(uintptr_t handle, const char *tip, const char *line, const void *png1x, int len1x,
                         const void *png2x, int len2x, double points);
// downexStatusAddItem appends a menu item (tag 0: a separator). Main thread.
void downexStatusAddItem(void *status, int tag, const char *label);
// downexStatusSetVisible shows or hides the status item. Main thread.
void downexStatusSetVisible(void *status, int visible);
// downexStatusSetTip changes the tooltip and the menu's first line. Main thread.
void downexStatusSetTip(void *status, const char *tip, const char *line);
// downexStatusDestroy removes the status item and releases the status object; its
// menu calls nothing afterwards. Main thread.
void downexStatusDestroy(void *status);

// Test support (tray_darwin_test.go). downexStatusDescribe returns, one per line, the
// visibility ("visible"/"hidden"), the tooltip and the menu items ("-" for a
// separator, "(disabled) " before a disabled one), malloc'd: the caller frees it.
// downexStatusPerform performs the action of the menu item at index. Main thread.
char *downexStatusDescribe(void *status);
void downexStatusPerform(void *status, int index);

// downexNotifyAvailable reports whether notifications can be posted: only an app
// bundle (with a bundle identifier) has a notification center.
int downexNotifyAvailable(void);
// downexNotifyPost asks for authorization on first use (alerts only, no sound) and
// posts a notification; failures arrive through goDownexNotifyResult. Main thread.
void downexNotifyPost(const char *title, const char *body);

#endif
