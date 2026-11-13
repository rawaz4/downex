// [mac scroll] The system's scroll bar settings (scroll_darwin.m).
#include <stdbool.h>

// dxScrollOverlay reports whether AppKit's preferred scroller style is overlay
// (NSScroller.preferredScrollerStyle: System Settings › Appearance › Show scroll bars, and,
// for "Automatically", the pointing device).
bool dxScrollOverlay(void);
// dxScrollClickJumps reports whether a click in a scroll bar's track jumps to the spot
// clicked (AppleScrollerPagingBehavior: "Click in the scroll bar to: Jump to the spot
// that's clicked").
bool dxScrollClickJumps(void);
// dxScrollObserve starts telling goAppEnvironment when either changes. Main thread; once.
void dxScrollObserve(void);
// dxScrollTestNotify posts NSPreferredScrollerStyleDidChangeNotification, as AppKit does when
// the style changes: tests only (NSScroller caches the style, so a test cannot change it).
void dxScrollTestNotify(void);
