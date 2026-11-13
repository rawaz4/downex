// [popups] The AppKit side of a window's system dialogs (dialogs.go): the Yes/No alert,
// the folder and file pickers, and opening an address in the browser.
#ifndef DOWNEX_UI_MAC_DIALOGS_H
#define DOWNEX_UI_MAC_DIALOGS_H

#include <stdint.h>
#include <stdbool.h>

// dxpDialogsWake makes the main thread call goDialogsRun(handle) from the run loop (in
// every mode, outside the main dispatch queue, so that a modal loop it runs keeps the
// queue's blocks, the window's frames and posts, running).
void dxpDialogsWake(uintptr_t handle);
// dxpAlertRun asks title/message in today's alert: Yes then No, No the default. It reports
// a yes.
bool dxpAlertRun(const char *title, const char *message);
// dxpPickerRun shows the folder or file picker: a sheet on owner, or app-modal when owner
// is NULL. goPickDone(token, path) hears the answer (path NULL: cancelled).
void dxpPickerRun(void *owner, uintptr_t token, bool folders, const char *title, const char *start);
// dxpPickerEnd ends the picker shown as a sheet on owner as cancelled.
void dxpPickerEnd(void *owner);
// dxpOwnerCanSheet reports whether a sheet can show on owner now (visible, not minimised,
// no sheet of its own).
bool dxpOwnerCanSheet(void *owner);
// dxpOpenURL opens an address in the default browser, on the main thread (from any
// thread).
bool dxpOpenURL(const char *url);

#endif
