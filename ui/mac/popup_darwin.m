//go:build darwin && cgo

// [popups] The AppKit side of popup windows and tooltips (see popup_darwin.h).

#import <Cocoa/Cocoa.h>
#import <QuartzCore/QuartzCore.h>
#import <CoreVideo/CoreVideo.h>
#include "popup_darwin.h"
#include "ax_view.h"
#include "_cgo_export.h"

#pragma clang diagnostic ignored "-Wdeprecated-declarations" // CVDisplayLink (macOS 13)

static int dxpMods(NSEventModifierFlags f) {
	int m = 0;
	if (f & NSEventModifierFlagShift) m |= 1;
	if (f & NSEventModifierFlagControl) m |= 2;
	if (f & NSEventModifierFlagOption) m |= 4;
	if (f & NSEventModifierFlagCommand) m |= 8;
	return m;
}

static int dxpButton(NSInteger n) { // kit.Buttons
	switch (n) {
	case 0: return 1;
	case 1: return 2;
	case 2: return 4;
	case 3: return 8;
	case 4: return 16;
	}
	return 0;
}

static int dxpPressed(void) {
	NSUInteger b = [NSEvent pressedMouseButtons];
	int out = 0;
	for (int i = 0; i < 5; i++) {
		if (b & (1u << i)) out |= dxpButton(i);
	}
	return out;
}

// ---- the panel ----------------------------------------------------------------------

@interface DXPopupPanel : NSPanel
@property(nonatomic) uintptr_t handle;
@property(nonatomic) CVDisplayLinkRef link;
@property(atomic) bool tickWanted;
@property(atomic) bool tickQueued;
@property(nonatomic) NSRect hitLocal; // panel points, y down; empty = the whole panel
@property(nonatomic) bool closedPanel;
@end

@implementation DXPopupPanel
- (BOOL)canBecomeKeyWindow { return NO; }
- (BOOL)canBecomeMainWindow { return NO; }
// hitScreen is the hit area on the screen (points, y up).
- (NSRect)hitScreen {
	NSRect f = self.frame;
	NSRect h = self.hitLocal;
	if (NSIsEmptyRect(h)) return f;
	return NSMakeRect(f.origin.x + h.origin.x, f.origin.y + f.size.height - NSMaxY(h), h.size.width, h.size.height);
}
@end

static NSHashTable<DXPopupPanel *> *dxpPanels; // the open panels (weak)

@interface DXPopupView : NSView
@property(nonatomic) uintptr_t handle;
@property(nonatomic, strong) NSTrackingArea *tracking;
@end

@implementation DXPopupView
- (BOOL)isFlipped { return YES; }
- (BOOL)wantsUpdateLayer { return YES; }
- (void)updateLayer {}
- (BOOL)acceptsFirstMouse:(NSEvent *)e { return YES; }
- (BOOL)acceptsFirstResponder { return NO; }
- (void)updateTrackingAreas {
	if (self.tracking) [self removeTrackingArea:self.tracking];
	self.tracking = [[NSTrackingArea alloc] initWithRect:NSZeroRect
		options:NSTrackingMouseMoved | NSTrackingMouseEnteredAndExited | NSTrackingCursorUpdate |
			NSTrackingActiveAlways | NSTrackingInVisibleRect
		owner:self userInfo:nil];
	[self addTrackingArea:self.tracking];
	[super updateTrackingAreas];
}
- (void)pointer:(NSEvent *)e action:(int)action button:(int)button {
	NSPoint p = [self convertPoint:e.locationInWindow fromView:nil];
	goPopupPointer(self.handle, action, p.x, p.y, button, dxpPressed(), dxpMods(e.modifierFlags));
}
- (void)mouseMoved:(NSEvent *)e { [self pointer:e action:1 button:0]; }
- (void)mouseDragged:(NSEvent *)e { [self pointer:e action:1 button:0]; }
- (void)rightMouseDragged:(NSEvent *)e { [self pointer:e action:1 button:0]; }
- (void)otherMouseDragged:(NSEvent *)e { [self pointer:e action:1 button:0]; }
- (void)mouseEntered:(NSEvent *)e { [self pointer:e action:1 button:0]; }
- (void)mouseExited:(NSEvent *)e { [self pointer:e action:5 button:0]; }
- (void)mouseDown:(NSEvent *)e { [self pointer:e action:2 button:1]; }
- (void)mouseUp:(NSEvent *)e { [self pointer:e action:3 button:1]; }
- (void)rightMouseDown:(NSEvent *)e { [self pointer:e action:2 button:2]; }
- (void)rightMouseUp:(NSEvent *)e { [self pointer:e action:3 button:2]; }
- (void)otherMouseDown:(NSEvent *)e { [self pointer:e action:2 button:dxpButton(e.buttonNumber)]; }
- (void)otherMouseUp:(NSEvent *)e { [self pointer:e action:3 button:dxpButton(e.buttonNumber)]; }
- (void)cursorUpdate:(NSEvent *)e { goPopupCursor(self.handle); }
- (void)scrollWheel:(NSEvent *)e {
	NSPoint p = [self convertPoint:e.locationInWindow fromView:nil];
	goPopupWheel(self.handle, p.x, p.y, -e.scrollingDeltaX, -e.scrollingDeltaY,
		e.hasPreciseScrollingDeltas, dxpMods(e.modifierFlags));
}
// The popup root's semantic tree (menu and items, list and options), through the
// accessibility part's bridge (ax_view.h; popup.go registers the view).
- (NSArray *)accessibilityChildren {
	NSArray *a = dxaxViewChildren(self);
	return a ?: [super accessibilityChildren];
}
- (id)accessibilityHitTest:(NSPoint)p {
	id o = dxaxViewHitTest(self, p);
	return o ?: [super accessibilityHitTest:p];
}
- (id)accessibilityFocusedUIElement {
	id o = dxaxViewFocused(self);
	return o ?: [super accessibilityFocusedUIElement];
}
@end

static CVReturn dxpLinkTick(CVDisplayLinkRef link, const CVTimeStamp *now, const CVTimeStamp *out,
	CVOptionFlags flags, CVOptionFlags *flagsOut, void *ctx) {
	DXPopupPanel *p = (__bridge DXPopupPanel *)ctx;
	if (!p.tickWanted || p.tickQueued) return kCVReturnSuccess;
	p.tickQueued = true;
	dispatch_async(dispatch_get_main_queue(), ^{
		p.tickQueued = false;
		if (!p.tickWanted || p.closedPanel) return;
		p.tickWanted = false;
		goPopupTick(p.handle);
		if (!p.tickWanted && p.link) CVDisplayLinkStop(p.link);
	});
	return kCVReturnSuccess;
}

void *dxpPanelNew(uintptr_t handle, const char *title) {
	if (!dxpPanels) dxpPanels = [NSHashTable weakObjectsHashTable];
	DXPopupPanel *p = [[DXPopupPanel alloc] initWithContentRect:NSMakeRect(0, 0, 1, 1)
		styleMask:NSWindowStyleMaskBorderless | NSWindowStyleMaskNonactivatingPanel
		backing:NSBackingStoreBuffered defer:NO];
	p.handle = handle;
	p.releasedWhenClosed = NO;
	p.opaque = NO;
	p.backgroundColor = NSColor.clearColor;
	p.hasShadow = NO; // the root draws the box's --shadow-lg
	p.hidesOnDeactivate = NO;
	p.becomesKeyOnlyIfNeeded = YES;
	p.floatingPanel = NO;
	p.acceptsMouseMovedEvents = YES;
	p.animationBehavior = NSWindowAnimationBehaviorNone;
	p.collectionBehavior = NSWindowCollectionBehaviorTransient | NSWindowCollectionBehaviorIgnoresCycle |
		NSWindowCollectionBehaviorFullScreenAuxiliary;
	p.title = [NSString stringWithUTF8String:title];
	DXPopupView *v = [[DXPopupView alloc] initWithFrame:NSMakeRect(0, 0, 1, 1)];
	v.handle = handle;
	v.wantsLayer = YES;
	v.layerContentsRedrawPolicy = NSViewLayerContentsRedrawNever;
	v.layer.contentsGravity = kCAGravityTopLeft;
	v.layer.actions = @{ @"contents": [NSNull null] };
	v.layer.opaque = NO;
	p.contentView = v;
	CVDisplayLinkRef link = NULL;
	if (CVDisplayLinkCreateWithActiveCGDisplays(&link) == kCVReturnSuccess) {
		CVDisplayLinkSetOutputCallback(link, dxpLinkTick, (__bridge void *)p);
		p.link = link;
	}
	[dxpPanels addObject:p];
	return (__bridge_retained void *)p;
}

void dxpPanelPlace(void *pp, void *op, double x, double y, double w, double h,
	double hx, double hy, double hw, double hh) {
	DXPopupPanel *p = (__bridge DXPopupPanel *)pp;
	NSWindow *owner = (__bridge NSWindow *)op;
	NSView *cv = owner.contentView;
	NSRect r = [cv convertRect:NSMakeRect(x, y, w, h) toView:nil];
	r = [owner convertRectToScreen:r];
	NSRect hit = (hw > 0 && hh > 0) ? NSMakeRect(r.origin.x + hx, r.origin.y + h - hy - hh, hw, hh) : r;
	NSScreen *sc = owner.screen ?: NSScreen.mainScreen;
	if (sc) {
		NSRect f = sc.frame;
		CGFloat dx = 0, dy = 0;
		if (NSMaxX(hit) > NSMaxX(f)) dx = NSMaxX(f) - NSMaxX(hit);
		if (NSMinX(hit) + dx < NSMinX(f)) dx = NSMinX(f) - NSMinX(hit);
		if (NSMinY(hit) < NSMinY(f)) dy = NSMinY(f) - NSMinY(hit);
		if (NSMaxY(hit) + dy > NSMaxY(f)) dy = NSMaxY(f) - NSMaxY(hit);
		r = NSOffsetRect(r, dx, dy);
	}
	p.hitLocal = (hw > 0 && hh > 0) ? NSMakeRect(hx, hy, hw, hh) : NSZeroRect;
	[p setFrame:r display:NO];
	NSNumber *num = sc.deviceDescription[@"NSScreenNumber"];
	if (p.link && num) CVDisplayLinkSetCurrentCGDisplay(p.link, num.unsignedIntValue);
}

void dxpPanelSetHit(void *pp, double hx, double hy, double hw, double hh) {
	DXPopupPanel *p = (__bridge DXPopupPanel *)pp;
	p.hitLocal = (hw > 0 && hh > 0) ? NSMakeRect(hx, hy, hw, hh) : NSZeroRect;
}

void dxpPanelSetTitle(void *pp, const char *t) {
	((__bridge DXPopupPanel *)pp).title = [NSString stringWithUTF8String:t];
}

void dxpPanelPresent(void *pp, CGImageRef img, double scale) {
	DXPopupPanel *p = (__bridge DXPopupPanel *)pp;
	CALayer *layer = p.contentView.layer;
	[CATransaction begin];
	[CATransaction setDisableActions:YES];
	layer.contentsScale = scale;
	layer.contents = (__bridge id)img;
	[CATransaction commit];
}

// dxpUpdateIgnore lets the pointer through every panel where it is not over the panel's
// hit area (HTTRANSPARENT's counterpart), telling a popup the pointer left it.
// A popup told so may close at once (dxpPanelClose takes it out of dxpPanels): the loop
// goes over a copy, and skips a panel closed meanwhile.
static void dxpUpdateIgnore(NSPoint s) {
	for (DXPopupPanel *p in dxpPanels.allObjects) {
		if (p.closedPanel || !p.handle || !p.visible) continue;
		bool in = NSPointInRect(s, [p hitScreen]);
		if (p.ignoresMouseEvents == !in) continue;
		if (!in && dxpPressed() != 0) continue; // a drag that began inside goes on
		p.ignoresMouseEvents = !in;
		if (!in) goPopupPointer(p.handle, 5, 0, 0, 0, dxpPressed(), dxpMods(NSEvent.modifierFlags));
	}
}

void dxpPanelShow(void *pp, void *op) {
	DXPopupPanel *p = (__bridge DXPopupPanel *)pp;
	NSWindow *owner = (__bridge NSWindow *)op;
	p.ignoresMouseEvents = !NSPointInRect([NSEvent mouseLocation], [p hitScreen]);
	if (owner.visible) {
		[owner addChildWindow:p ordered:NSWindowAbove];
	}
	[p orderFront:nil];
}

void dxpPanelClose(void *pp, void *op) {
	DXPopupPanel *p = (__bridge DXPopupPanel *)pp;
	p.closedPanel = true;
	p.tickWanted = false;
	// Go deletes the handle after this: an event AppKit still has for the panel finds none.
	p.handle = 0;
	((DXPopupView *)p.contentView).handle = 0;
	if (p.link) CVDisplayLinkStop(p.link);
	NSWindow *owner = (__bridge NSWindow *)op;
	if (owner && p.parentWindow == owner) [owner removeChildWindow:p];
	[p orderOut:nil];
	[dxpPanels removeObject:p];
}

void dxpPanelRelease(void *pp) {
	DXPopupPanel *p = (__bridge_transfer DXPopupPanel *)pp;
	if (p.link) {
		CVDisplayLinkStop(p.link);
		CVDisplayLinkRelease(p.link);
		p.link = NULL;
	}
	p.contentView.layer.contents = nil;
	p = nil;
}

void dxpPanelRequestTick(void *pp) {
	DXPopupPanel *p = (__bridge DXPopupPanel *)pp;
	if (p.closedPanel) return;
	p.tickWanted = true;
	if (p.link) {
		if (!CVDisplayLinkIsRunning(p.link)) CVDisplayLinkStart(p.link);
		return;
	}
	dispatch_after(dispatch_time(DISPATCH_TIME_NOW, 16 * NSEC_PER_MSEC), dispatch_get_main_queue(), ^{
		if (!p.tickWanted || p.closedPanel) return;
		p.tickWanted = false;
		goPopupTick(p.handle);
	});
}

bool dxpPanelPointerInHit(void *pp) {
	DXPopupPanel *p = (__bridge DXPopupPanel *)pp;
	return NSPointInRect([NSEvent mouseLocation], [p hitScreen]);
}

void *dxpPanelContentView(void *pp) { return (__bridge void *)((__bridge DXPopupPanel *)pp).contentView; }

void dxpSetCursor(int c) {
	NSCursor *cur;
	switch (c) {
	case 1: cur = NSCursor.pointingHandCursor; break;
	case 2: cur = NSCursor.IBeamCursor; break;
	case 3: cur = NSCursor.resizeLeftRightCursor; break;
	case 4: cur = NSCursor.resizeUpDownCursor; break;
	case 5: cur = NSCursor.openHandCursor; break;
	case 6: cur = NSCursor.closedHandCursor; break;
	case 7: cur = NSCursor.closedHandCursor; break;
	case 8: cur = NSCursor.operationNotAllowedCursor; break;
	default: cur = NSCursor.arrowCursor; break;
	}
	[cur set];
}

// ---- the owner ----------------------------------------------------------------------

@interface DXPOwnerObserver : NSObject
@property(nonatomic) uintptr_t handle;
@end

@implementation DXPOwnerObserver
- (void)deactivated:(NSNotification *)n { goPopupOwner(self.handle, 2); }
- (void)moved:(NSNotification *)n { goPopupOwner(self.handle, 3); }
@end

static NSMapTable<NSWindow *, DXPOwnerObserver *> *dxpObservers;

void dxpOwnerObserve(void *op, uintptr_t handle, bool on) {
	NSWindow *owner = (__bridge NSWindow *)op;
	if (!dxpObservers) dxpObservers = [NSMapTable weakToStrongObjectsMapTable];
	NSNotificationCenter *nc = NSNotificationCenter.defaultCenter;
	DXPOwnerObserver *o = [dxpObservers objectForKey:owner];
	if (!on) {
		if (o) {
			[nc removeObserver:o];
			[dxpObservers removeObjectForKey:owner];
		}
		return;
	}
	if (o) return;
	o = [DXPOwnerObserver new];
	o.handle = handle;
	[dxpObservers setObject:o forKey:owner];
	[nc addObserver:o selector:@selector(deactivated:) name:NSWindowDidResignKeyNotification object:owner];
	[nc addObserver:o selector:@selector(deactivated:) name:NSApplicationDidResignActiveNotification object:NSApp];
	for (NSNotificationName name in @[NSWindowWillMoveNotification, NSWindowDidMoveNotification,
			NSWindowDidResizeNotification, NSWindowWillStartLiveResizeNotification,
			NSWindowWillMiniaturizeNotification, NSWindowWillEnterFullScreenNotification,
			NSWindowDidEnterFullScreenNotification, NSWindowDidExitFullScreenNotification,
			NSWindowDidChangeScreenNotification]) {
		[nc addObserver:o selector:@selector(moved:) name:name object:owner];
	}
}

bool dxpOwnerPoint(void *op, void *wp, double sx, double sy, double *x, double *y) {
	NSWindow *owner = (__bridge NSWindow *)op;
	NSPoint w = [owner convertPointFromScreen:NSMakePoint(sx, sy)];
	NSView *cv = owner.contentView;
	NSPoint v = [cv convertPoint:w fromView:nil];
	*x = v.x;
	*y = v.y;
	return wp == op && NSPointInRect(v, cv.bounds);
}

// ---- the event monitors -------------------------------------------------------------

@interface DXTipView : NSView
@property(nonatomic) uintptr_t ownerHandle;
@property(nonatomic, copy) NSString *text;
@end

static NSMapTable<NSWindow *, DXTipView *> *dxpTips;
static id dxpLocal, dxpGlobal;
static int dxpHolds;

static NSPoint dxpScreenPoint(NSEvent *e) {
	if (e.window) return [e.window convertPointToScreen:e.locationInWindow];
	return e.locationInWindow;
}

static bool dxpIsDown(NSEventType t) {
	return t == NSEventTypeLeftMouseDown || t == NSEventTypeRightMouseDown || t == NSEventTypeOtherMouseDown;
}

// dxpRetarget is e sent to under instead (the pointer over a panel's shadow belongs to
// what is beneath it), nil for an event AppKit gives no way to send there (the wheel and
// the other buttons: an NSEvent made from a CGEvent keeps its window), which then goes to
// nobody rather than to the popup, or e when it cannot be sent at all.
static NSEvent *dxpRetarget(NSEvent *e, NSWindow *under, NSPoint s) {
	switch (e.type) {
	case NSEventTypeLeftMouseDown: case NSEventTypeLeftMouseUp: case NSEventTypeLeftMouseDragged:
	case NSEventTypeRightMouseDown: case NSEventTypeRightMouseUp: case NSEventTypeRightMouseDragged:
	case NSEventTypeMouseMoved:
		return [NSEvent mouseEventWithType:e.type location:[under convertPointFromScreen:s]
			modifierFlags:e.modifierFlags timestamp:e.timestamp windowNumber:under.windowNumber
			context:nil eventNumber:e.eventNumber clickCount:e.clickCount pressure:e.pressure];
	case NSEventTypeOtherMouseDown: case NSEventTypeScrollWheel:
		// It still dismisses (goPopupOutside, as over the window beneath).
		return nil;
	default:
		return e;
	}
}

static NSEvent *dxpFilter(NSEvent *e) {
	NSEventType t = e.type;
	if (t == NSEventTypeMouseExited || t == NSEventTypeMouseEntered) {
		// The pointer left an owner's content: its tooltip goes (WebKit's title does too).
		if (t == NSEventTypeMouseExited && e.window && e.trackingArea && e.trackingArea.owner == e.window.contentView) {
			DXTipView *tip = [dxpTips objectForKey:e.window];
			if (tip.ownerHandle) goTipLeft(tip.ownerHandle);
		}
		return e;
	}
	NSPoint s = dxpScreenPoint(e);
	if (dxpPanels.count > 0) dxpUpdateIgnore(s);
	NSEvent *out = e;
	NSWindow *dest = e.window; // the window the event is for (the one beneath a shadow)
	DXPopupPanel *on = nil;
	if ([e.window isKindOfClass:DXPopupPanel.class]) {
		on = (DXPopupPanel *)e.window;
		if (!NSPointInRect(s, [on hitScreen]) && !(t == NSEventTypeLeftMouseDragged || t == NSEventTypeRightMouseDragged ||
				t == NSEventTypeOtherMouseDragged || t == NSEventTypeLeftMouseUp || t == NSEventTypeRightMouseUp ||
				t == NSEventTypeOtherMouseUp)) {
			NSInteger n = [NSWindow windowNumberAtPoint:s belowWindowWithWindowNumber:on.windowNumber];
			NSWindow *under = [NSApp windowWithWindowNumber:n];
			if (under && ![under isKindOfClass:DXPopupPanel.class]) {
				out = dxpRetarget(e, under, s);
				dest = under;
			}
			on = nil;
		}
	} else {
		// Over a popup that lets the pointer through, outside its hit area, or not one at all.
		for (DXPopupPanel *p in dxpPanels.allObjects) {
			if (!p.closedPanel && p.visible && NSPointInRect(s, [p hitScreen])) {
				on = p;
				break;
			}
		}
	}
	if (dxpIsDown(t)) {
		goPopupOutside(1, (__bridge void *)dest, s.x, s.y, on ? on.handle : 0);
	} else if (t == NSEventTypeScrollWheel) {
		// Only a wheel that scrolls dismisses, as a scroll of today's page does: events without
		// a delta (fingers resting on a trackpad, the end or cancel of its gesture, a Magic
		// Mouse touch) leave the popups open.
		if (e.scrollingDeltaX != 0 || e.scrollingDeltaY != 0) {
			goPopupOutside(4, (__bridge void *)dest, s.x, s.y, on ? on.handle : 0);
		}
	} else if ((t == NSEventTypeMouseMoved || t == NSEventTypeLeftMouseDragged) && out.window && dxpTips) {
		DXTipView *tip = [dxpTips objectForKey:out.window];
		if (tip.ownerHandle) {
			NSPoint v = [out.window.contentView convertPoint:out.locationInWindow fromView:nil];
			goTipMoved(tip.ownerHandle, v.x, v.y);
		}
	}
	return out;
}

void dxpMonitorRetain(void) {
	if (dxpHolds++ > 0) return;
	NSEventMask m = NSEventMaskLeftMouseDown | NSEventMaskRightMouseDown | NSEventMaskOtherMouseDown |
		NSEventMaskLeftMouseUp | NSEventMaskRightMouseUp | NSEventMaskOtherMouseUp |
		NSEventMaskMouseMoved | NSEventMaskLeftMouseDragged | NSEventMaskRightMouseDragged |
		NSEventMaskOtherMouseDragged | NSEventMaskScrollWheel | NSEventMaskMouseExited | NSEventMaskMouseEntered;
	dxpLocal = [NSEvent addLocalMonitorForEventsMatchingMask:m handler:^NSEvent *(NSEvent *e) { return dxpFilter(e); }];
	// Moves over other applications' windows: a panel's shadow there lets the pointer through.
	dxpGlobal = [NSEvent addGlobalMonitorForEventsMatchingMask:NSEventMaskMouseMoved handler:^(NSEvent *e) {
		if (dxpPanels.count > 0) dxpUpdateIgnore([NSEvent mouseLocation]);
	}];
}

void dxpMonitorRelease(void) {
	if (dxpHolds == 0 || --dxpHolds > 0) return;
	if (dxpLocal) [NSEvent removeMonitor:dxpLocal];
	if (dxpGlobal) [NSEvent removeMonitor:dxpGlobal];
	dxpLocal = dxpGlobal = nil;
}

// ---- tooltips ------------------------------------------------------------------------
//
// AppKit's tooltips, as WKWebView shows an element's title (WebViewImpl::toolTipChanged):
// whenever the text changes, every tooltip rectangle of the view goes and one rectangle over
// the whole view comes with the new text. AppKit's tooltip manager arms a rectangle added
// under the pointer at once, so the system's delay starts when the pointer reaches an
// element with a title; it then places, wraps and hides the tooltip as it does for every
// application. Only public NSView API is used.

@implementation DXTipView
- (BOOL)isFlipped { return YES; }
- (NSView *)hitTest:(NSPoint)p { return nil; }
- (BOOL)acceptsFirstResponder { return NO; }
- (NSString *)view:(NSView *)v stringForToolTip:(NSToolTipTag)tag point:(NSPoint)p userData:(void *)d {
	return self.text ?: @"";
}
- (void)setTip:(NSString *)text {
	if ([text isEqualToString:self.text ?: @""]) return;
	self.text = text;
	[self removeAllToolTips];
	if (text.length == 0) return;
	// WebKit's "wide open" rectangle: it covers the view whatever its size becomes.
	[self addToolTipRect:NSMakeRect(-100000, -100000, 200000, 200000) owner:self userData:NULL];
}
@end

void *dxpTipAttach(void *op, uintptr_t handle) {
	NSWindow *owner = (__bridge NSWindow *)op;
	if (!dxpTips) dxpTips = [NSMapTable weakToWeakObjectsMapTable];
	NSView *cv = owner.contentView;
	DXTipView *t = [[DXTipView alloc] initWithFrame:cv.bounds];
	t.ownerHandle = handle;
	t.text = @"";
	t.autoresizingMask = NSViewWidthSizable | NSViewHeightSizable;
	[cv addSubview:t positioned:NSWindowBelow relativeTo:nil];
	[dxpTips setObject:t forKey:owner];
	return (__bridge_retained void *)t;
}

void dxpTipDetach(void *tp) {
	DXTipView *t = (__bridge_transfer DXTipView *)tp;
	t.ownerHandle = 0;
	[t setTip:@""];
	[t removeAllToolTips];
	if (t.window) [dxpTips removeObjectForKey:t.window];
	[t removeFromSuperview];
	t = nil;
}

void dxpTipSet(void *tp, const char *text) {
	[(__bridge DXTipView *)tp setTip:[NSString stringWithUTF8String:text]];
}
