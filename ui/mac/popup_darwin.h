// The AppKit side of ui/mac's popup windows and tooltips ([popups] part; popup.go,
// tooltip.go). Popups are borderless non-activating NSPanels, children of their owner
// window, drawn by Go into a bitmap shown as the content layer. Tooltips are AppKit's own,
// shown through a tooltip rectangle as WKWebView shows its title tooltips. Objective-C never
// holds a Go pointer: it knows a popup by its runtime/cgo handle, and an owner by its
// window's handle; a panel forgets its handle when it closes (dxpPanelClose), before Go
// deletes it.
#ifndef DOWNEX_UI_MAC_POPUP_H
#define DOWNEX_UI_MAC_POPUP_H

#include <stdint.h>
#include <stdbool.h>
#include <CoreGraphics/CoreGraphics.h>

// dxpPanelNew makes a hidden popup panel (+1, owned by Go until dxpPanelRelease).
void *dxpPanelNew(uintptr_t handle, const char *title);
// dxpPanelPlace puts the panel at (x, y, w, h), points of the owner's content area (y
// down), moved as little as needed to keep its hit area (hx, hy, hw, hh: the panel's own
// points, y down) on the owner's screen.
void dxpPanelPlace(void *panel, void *owner, double x, double y, double w, double h,
	double hx, double hy, double hw, double hh);
// dxpPanelSetHit sets the part of the panel that takes the pointer (panel points, y down).
void dxpPanelSetHit(void *panel, double hx, double hy, double hw, double hh);
void dxpPanelSetTitle(void *panel, const char *title);
// dxpPanelPresent shows img (device pixels, y down) as the panel's content.
void dxpPanelPresent(void *panel, CGImageRef img, double scale);
// dxpPanelShow orders the panel in above its owner, as the owner's child window.
void dxpPanelShow(void *panel, void *owner);
// dxpPanelClose takes the panel off the screen and stops its frames.
void dxpPanelClose(void *panel, void *owner);
void dxpPanelRelease(void *panel);
// dxpPanelRequestTick asks for one goPopupTick at the next display refresh.
void dxpPanelRequestTick(void *panel);
// dxpPanelPointerInHit reports whether the pointer is over the panel's hit area now.
bool dxpPanelPointerInHit(void *panel);
void dxpSetCursor(int cursor);

// dxpOwnerObserve starts (on) or stops following owner's activation, moves, size changes
// and minimising for its popups: goPopupOwner hears them.
void dxpOwnerObserve(void *owner, uintptr_t ownerHandle, bool on);
// dxpOwnerPoint converts a screen point (points, y up) to owner's content points (y
// down); it reports whether win (the window an event went to) is owner and the point is
// over owner's content area.
bool dxpOwnerPoint(void *owner, void *win, double sx, double sy, double *x, double *y);
// dxpMonitorRetain and dxpMonitorRelease hold the application's event monitors: presses
// and the wheel for popups (goPopupOutside), moves for tooltips (goTipMoved).
void dxpMonitorRetain(void);
void dxpMonitorRelease(void);

// Tooltips: dxpTipAttach puts the tooltip view over owner's content (+1, released by
// dxpTipDetach); dxpTipSet shows text after the system's delay ("" hides it).
void *dxpTipAttach(void *owner, uintptr_t ownerHandle);
void dxpTipDetach(void *tip);
void dxpTipSet(void *tip, const char *text);

// dxpPanelContentView is the panel's content view (unretained): its accessibility's view.
void *dxpPanelContentView(void *panel);

#endif
