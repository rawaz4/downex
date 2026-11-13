// [ax] For content views of another class than DXView (a popup's panel, say): forward the
// view's accessibilityChildren, accessibilityHitTest: and accessibilityFocusedUIElement
// here, after ax.go's axRegister tied the view to its host. Objective-C only.
#ifndef DOWNEX_UI_MAC_AX_VIEW_H
#define DOWNEX_UI_MAC_AX_VIEW_H

#ifdef __OBJC__
#import <Cocoa/Cocoa.h>

// Each returns nil when the view has no tree (the caller then answers super's).
NSArray *dxaxViewChildren(NSView *view);
id dxaxViewHitTest(NSView *view, NSPoint screen);
id dxaxViewFocused(NSView *view);
#endif

#endif
