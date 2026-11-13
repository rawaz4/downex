//go:build darwin && cgo

// The AppKit side of ui/mac's windows (see window_darwin.h). Everything runs on the main
// thread except the display link's callback, which only hops to it.

#import <Cocoa/Cocoa.h>
#import <QuartzCore/QuartzCore.h>
#import <CoreVideo/CoreVideo.h>
#include "window_darwin.h"
#include "field_darwin.h" // [fields]
#include "_cgo_export.h"
#include "present_darwin.h" // [render] the view's display link (present_darwin.m)

#pragma clang diagnostic ignored "-Wdeprecated-declarations" // CVDisplayLink: macOS 13 has no NSView displayLink

// ---- application ----------------------------------------------------------------------

@interface DXAppObserver : NSObject
@end

@implementation DXAppObserver
- (void)observeValueForKeyPath:(NSString *)path ofObject:(id)obj change:(NSDictionary *)change context:(void *)ctx {
	goAppEnvironment();
}
- (void)displayOptionsChanged:(NSNotification *)n {
	goAppEnvironment();
}
@end

static DXAppObserver *appObserver;

void dxAppInit(bool regular) {
	[NSApplication sharedApplication];
	[NSApp setActivationPolicy:regular ? NSApplicationActivationPolicyRegular : NSApplicationActivationPolicyAccessory];
	appObserver = [DXAppObserver new];
	[NSApp addObserver:appObserver forKeyPath:@"effectiveAppearance" options:0 context:NULL];
	[[[NSWorkspace sharedWorkspace] notificationCenter] addObserver:appObserver
		selector:@selector(displayOptionsChanged:)
		name:NSWorkspaceAccessibilityDisplayOptionsDidChangeNotification object:nil];
	[NSApp finishLaunching];
}

// dxRunGen counts the runs of the event loop; dxRunning is whether one is in progress. A stop
// ends only the run it was asked in: a block left from a run that ended otherwise (or asked
// for while none ran) must not end the next run at its start.
static long dxRunGen;
static bool dxRunning;

void dxAppRun(void) {
	dxRunGen++;
	dxRunning = true;
	[NSApp run];
	dxRunning = false;
}

void dxAppStop(void) {
	long gen = dxRunGen;
	bool running = dxRunning;
	// stop: ends the innermost loop: inside a modal one (an alert's runModal, a sheet's,
	// NSTerminateLater's wait), only that, losing the dialog's answer while run goes on with
	// no window. The block runs in the default mode only, once every modal and tracking loop
	// has returned to run's.
	CFRunLoopRef main = CFRunLoopGetMain();
	CFRunLoopPerformBlock(main, kCFRunLoopDefaultMode, ^{
		if (!running || !dxRunning || dxRunGen != gen) return;
		[NSApp stop:nil];
		// stop: takes effect after the next event: post one so that run returns now.
		NSEvent *e = [NSEvent otherEventWithType:NSEventTypeApplicationDefined location:NSZeroPoint
			modifierFlags:0 timestamp:0 windowNumber:0 context:nil subtype:0 data1:0 data2:0];
		[NSApp postEvent:e atStart:NO];
	});
	CFRunLoopWakeUp(main);
}

void dxAppWake(void) {
	dispatch_async(dispatch_get_main_queue(), ^{ goAppPosts(); });
}

bool dxAppDark(void) {
	NSAppearanceName n = [[NSApp effectiveAppearance]
		bestMatchFromAppearancesWithNames:@[NSAppearanceNameAqua, NSAppearanceNameDarkAqua]];
	return [n isEqualToString:NSAppearanceNameDarkAqua];
}

bool dxAppReduceMotion(void) {
	return [[NSWorkspace sharedWorkspace] accessibilityDisplayShouldReduceMotion];
}

// ---- the content view ---------------------------------------------------------------

static int dxMods(NSEventModifierFlags f) {
	int m = 0;
	if (f & NSEventModifierFlagShift) m |= 1;
	if (f & NSEventModifierFlagControl) m |= 2;
	if (f & NSEventModifierFlagOption) m |= 4;
	if (f & NSEventModifierFlagCommand) m |= 8;
	return m;
}

static int dxButton(NSInteger n) { // kit.Buttons
	switch (n) {
	case 0: return 1;
	case 1: return 2;
	case 2: return 4;
	case 3: return 8;
	case 4: return 16;
	}
	return 0;
}

static int dxPressed(void) {
	NSUInteger b = [NSEvent pressedMouseButtons];
	int out = 0;
	for (int i = 0; i < 5; i++) {
		if (b & (1u << i)) out |= dxButton(i);
	}
	return out;
}

@interface DXView : NSView
@property(nonatomic) uintptr_t handle;
@property(nonatomic, strong) NSTrackingArea *tracking;
@end

@implementation DXView
- (BOOL)isFlipped { return YES; }
- (BOOL)acceptsFirstResponder { return YES; }
- (BOOL)wantsUpdateLayer { return YES; }
- (void)updateLayer {
	// A paint the system asks for (first show, a backing change): Go repaints all.
	goWindowExpose(self.handle);
}
- (void)updateTrackingAreas {
	if (self.tracking) [self removeTrackingArea:self.tracking];
	self.tracking = [[NSTrackingArea alloc] initWithRect:NSZeroRect
		options:NSTrackingMouseMoved | NSTrackingMouseEnteredAndExited | NSTrackingActiveAlways | NSTrackingInVisibleRect
		owner:self userInfo:nil];
	[self addTrackingArea:self.tracking];
	[super updateTrackingAreas];
}
- (void)pointer:(NSEvent *)e action:(int)action button:(int)button {
	NSPoint p = [self convertPoint:e.locationInWindow fromView:nil];
	int clicks = action == 2 ? (int)e.clickCount : 0;
	goWindowPointer(self.handle, action, p.x, p.y, button, dxPressed(), dxMods(e.modifierFlags), clicks);
}
- (void)mouseMoved:(NSEvent *)e {
	if (dxfRepeatMove((__bridge void *)self, (__bridge void *)e)) return; // [fields] each move once
	[self pointer:e action:1 button:0];
}
- (void)mouseDragged:(NSEvent *)e { [self pointer:e action:1 button:0]; }
- (void)rightMouseDragged:(NSEvent *)e { [self pointer:e action:1 button:0]; }
- (void)otherMouseDragged:(NSEvent *)e { [self pointer:e action:1 button:0]; }
- (void)mouseEntered:(NSEvent *)e { [self pointer:e action:4 button:0]; }
- (void)mouseExited:(NSEvent *)e { [self pointer:e action:5 button:0]; }
- (void)mouseDown:(NSEvent *)e { [self pointer:e action:2 button:1]; }
- (void)mouseUp:(NSEvent *)e { [self pointer:e action:3 button:1]; }
- (void)rightMouseDown:(NSEvent *)e { [self pointer:e action:2 button:2]; }
- (void)rightMouseUp:(NSEvent *)e { [self pointer:e action:3 button:2]; }
- (void)otherMouseDown:(NSEvent *)e { [self pointer:e action:2 button:dxButton(e.buttonNumber)]; }
- (void)otherMouseUp:(NSEvent *)e { [self pointer:e action:3 button:dxButton(e.buttonNumber)]; }
- (void)scrollWheel:(NSEvent *)e {
	NSPoint p = [self convertPoint:e.locationInWindow fromView:nil];
	// AppKit's deltas are positive towards the top and the left; the kit's like the DOM's.
	goWindowWheel(self.handle, p.x, p.y, -e.scrollingDeltaX, -e.scrollingDeltaY,
		e.hasPreciseScrollingDeltas, dxMods(e.modifierFlags));
}
- (void)key:(NSEvent *)e down:(bool)down {
	NSString *plain = e.charactersIgnoringModifiers;
	unichar c = plain.length == 1 ? [plain characterAtIndex:0] : 0;
	if (down && dxfRootComposing((__bridge void *)self, (__bridge void *)e)) return; // [fields] the input method's key
	bool used = goWindowKey(self.handle, down, e.keyCode, c, dxMods(e.modifierFlags), down && e.isARepeat);
	if (!down || used) return;
	if (dxfRootText((__bridge void *)self, (__bridge void *)e)) return; // [fields] the root's input methods
	if (e.keyCode == 53 && (self.window.styleMask & NSWindowStyleMaskFullScreen)) {
		// An Escape the page leaves alone reaches WailsWindow's cancelOperation:, which leaves
		// full screen (Wails' disableEscapeExitsFullscreen is off); other keys stay unused.
		[self.window toggleFullScreen:nil];
		return;
	}
	NSString *s = e.characters;
	if ((e.modifierFlags & (NSEventModifierFlagCommand | NSEventModifierFlagControl)) || s.length == 0) return;
	unichar first = [s characterAtIndex:0];
	if (first < 0x20 || first == 0x7f || (first >= 0xF700 && first <= 0xF8FF)) return; // control and function keys
	goWindowText(self.handle, (char *)s.UTF8String);
}
- (void)keyDown:(NSEvent *)e { [self key:e down:true]; }
- (void)keyUp:(NSEvent *)e { [self key:e down:false]; }
- (BOOL)performKeyEquivalent:(NSEvent *)e {
	// The root sees Command keys before the menu (as the page's keydown does in WebKit).
	if (self.window.firstResponder != self) return [super performKeyEquivalent:e];
	if (e.type == NSEventTypeKeyDown) {
		NSString *plain = e.charactersIgnoringModifiers;
		unichar c = plain.length == 1 ? [plain characterAtIndex:0] : 0;
		if (goWindowKey(self.handle, true, e.keyCode, c, dxMods(e.modifierFlags), e.isARepeat)) return YES;
	}
	return [super performKeyEquivalent:e];
}
- (void)flagsChanged:(NSEvent *)e {
	goWindowFlags(self.handle, e.keyCode, dxMods(e.modifierFlags));
}
// The Edit menu's Copy and Select All chosen with the mouse (nil-targeted, as Wails' are):
// WebKit runs the page's copy and select-all commands on what has the focus; here the focused
// element runs them only if it takes edit commands (the read-only Page address); a list
// never hears them, as WebKit fires no keydown for a menu command.
- (void)copy:(id)sender { goWindowEditCommand(self.handle, "copy"); }
- (void)selectAll:(id)sender { goWindowEditCommand(self.handle, "selectAll"); }
@end

// ---- the window ---------------------------------------------------------------------

@interface DXWindow : NSWindow
@property(nonatomic) uintptr_t handle;
@property(nonatomic) CVDisplayLinkRef link;
@property(atomic) bool tickWanted;
@property(atomic) bool tickQueued;
@end

@implementation DXWindow
@end

@interface DXWindowDelegate : NSObject <NSWindowDelegate>
@end

@implementation DXWindowDelegate
- (BOOL)windowShouldClose:(NSWindow *)w {
	return goWindowShouldClose(((DXWindow *)w).handle);
}
- (void)windowWillClose:(NSNotification *)n {
	DXWindow *w = n.object;
	dxrLinkStop((__bridge void *)w); // [render]
	if (w.link) {
		CVDisplayLinkStop(w.link);
	}
	goWindowClosed(w.handle);
}
- (void)windowDidResize:(NSNotification *)n { goWindowResized(((DXWindow *)n.object).handle); }
- (void)windowDidChangeBackingProperties:(NSNotification *)n { goWindowEnvironment(((DXWindow *)n.object).handle); }
- (void)windowDidChangeScreen:(NSNotification *)n {
	DXWindow *w = n.object;
	NSNumber *num = w.screen.deviceDescription[@"NSScreenNumber"];
	if (w.link && num) CVDisplayLinkSetCurrentCGDisplay(w.link, num.unsignedIntValue);
	goWindowEnvironment(w.handle);
}
- (void)windowDidBecomeKey:(NSNotification *)n { goWindowFocus(((DXWindow *)n.object).handle, true); }
- (void)windowDidResignKey:(NSNotification *)n { goWindowFocus(((DXWindow *)n.object).handle, false); }
- (void)windowDidMiniaturize:(NSNotification *)n { goWindowStateChanged(((DXWindow *)n.object).handle); }
- (void)windowDidDeminiaturize:(NSNotification *)n { goWindowStateChanged(((DXWindow *)n.object).handle); }
// [render] a window that shows again paints what it kept while hidden (present_window.go)
- (void)windowDidChangeOcclusionState:(NSNotification *)n { goRndOcclusion(((DXWindow *)n.object).handle); }
// [/render]
@end

static DXWindowDelegate *windowDelegate;

static CVReturn dxLinkTick(CVDisplayLinkRef link, const CVTimeStamp *now, const CVTimeStamp *out,
	CVOptionFlags flags, CVOptionFlags *flagsOut, void *ctx) {
	DXWindow *w = (__bridge DXWindow *)ctx;
	if (!w.tickWanted || w.tickQueued) return kCVReturnSuccess;
	w.tickQueued = true;
	dispatch_async(dispatch_get_main_queue(), ^{
		w.tickQueued = false;
		if (!w.tickWanted) return;
		w.tickWanted = false;
		goWindowTick(w.handle); // [popups] read now: 0 once the window was released
		if (!w.tickWanted) CVDisplayLinkStop(w.link); // idle: no ticks, no wake-ups
	});
	return kCVReturnSuccess;
}

void *dxWindowNew(uintptr_t handle, double w, double h, const char *title) {
	if (!windowDelegate) windowDelegate = [DXWindowDelegate new];
	NSWindowStyleMask style = NSWindowStyleMaskTitled | NSWindowStyleMaskClosable |
		NSWindowStyleMaskMiniaturizable | NSWindowStyleMaskResizable;
	// A size larger than the screen (a stored one from a larger display, a hand-edited file)
	// is fitted into its visible area, frame included, as Windows' first rectangle is fitted
	// into the work area: AppKit itself would keep it, and a side past the drawing surface's
	// 16384 pixels could not be drawn at all.
	NSScreen *screen = NSScreen.mainScreen;
	if (screen) {
		NSRect vis = screen.visibleFrame;
		NSRect most = [NSWindow contentRectForFrameRect:vis styleMask:style];
		w = MIN(w, MAX(most.size.width, 1));
		h = MIN(h, MAX(most.size.height, 1));
	}
	DXWindow *win = [[DXWindow alloc] initWithContentRect:NSMakeRect(0, 0, w, h) styleMask:style
		backing:NSBackingStoreBuffered defer:NO];
	win.handle = handle;
	win.releasedWhenClosed = NO;
	win.title = [NSString stringWithUTF8String:title];
	win.delegate = windowDelegate;
	win.acceptsMouseMovedEvents = YES;
	// Each of today's windows was a process of its own, so two never became tabs of one
	// window (System Settings' "Prefer tabs when opening documents", a window in full
	// screen); the progress windows now share a process, and the tab bar would also take its
	// height from the content the progress window fits.
	win.tabbingMode = NSWindowTabbingModeDisallowed;
	DXView *view = [[DXView alloc] initWithFrame:NSMakeRect(0, 0, w, h)];
	view.handle = handle;
	view.wantsLayer = YES;
	view.layerContentsRedrawPolicy = NSViewLayerContentsRedrawOnSetNeedsDisplay;
	view.layer.contentsGravity = kCAGravityTopLeft;
	view.layer.actions = @{ @"contents": [NSNull null] };
	win.contentView = view;
	[win makeFirstResponder:view];
	[win center];
	CVDisplayLinkRef link = NULL;
	if (dxrLinkAttach((__bridge void *)win, (__bridge void *)view, handle)) { // [render] macOS 14+: the view's own
	} else if (CVDisplayLinkCreateWithActiveCGDisplays(&link) == kCVReturnSuccess) {
		CVDisplayLinkSetOutputCallback(link, dxLinkTick, (__bridge void *)win);
		win.link = link;
	}
	return (__bridge_retained void *)win;
}

void dxWindowShow(void *p, bool activate) {
	DXWindow *w = (__bridge DXWindow *)p;
	if (activate) {
		[NSApp activateIgnoringOtherApps:YES];
		[w makeKeyAndOrderFront:nil];
	} else {
		[w orderFront:nil];
	}
}

void dxWindowClose(void *p) { [(__bridge DXWindow *)p close]; }

void dxWindowRelease(void *p) {
	dxrLinkStop(p); // [render] a window released without closing (NewWindow's error path)
	DXWindow *w = (__bridge_transfer DXWindow *)p;
	if (w.link) {
		CVDisplayLinkStop(w.link);
		CVDisplayLinkRelease(w.link);
		w.link = NULL;
	}
	w.delegate = nil;
	// [popups] Go deletes the handle right after this: AppKit may still hold the window and
	// its view (an event it queued, a mouseExited of a window closed under the pointer), so
	// both forget it, and what they report then finds no window.
	w.handle = 0;
	if ([w.contentView isKindOfClass:[DXView class]]) ((DXView *)w.contentView).handle = 0;
	// [/popups]
	w = nil;
}

void dxWindowSetTitle(void *p, const char *t) { ((__bridge DXWindow *)p).title = [NSString stringWithUTF8String:t]; }

void dxWindowSetMinSize(void *p, double w, double h) { ((__bridge DXWindow *)p).contentMinSize = NSMakeSize(w, h); }

void dxWindowSetMaxSize(void *p, double w, double h) { ((__bridge DXWindow *)p).contentMaxSize = NSMakeSize(w, h); }

void dxWindowSetContentSize(void *p, double w, double h) {
	DXWindow *win = (__bridge DXWindow *)p;
	NSRect f = win.frame;
	NSRect c = [win contentRectForFrameRect:f];
	NSRect nc = NSMakeRect(c.origin.x, NSMaxY(c) - h, w, h); // keep the top-left corner
	NSRect nf = [win frameRectForContentRect:nc];
	NSRect vis = win.screen ? win.screen.visibleFrame : nf;
	if (NSMaxX(nf) > NSMaxX(vis)) nf.origin.x = MAX(NSMinX(vis), NSMaxX(vis) - nf.size.width);
	if (NSMinY(nf) < NSMinY(vis)) nf.origin.y = NSMinY(vis);
	[win setFrame:nf display:YES];
}

void dxWindowContentSize(void *p, double *w, double *h) {
	NSSize s = ((__bridge DXWindow *)p).contentView.bounds.size;
	*w = s.width;
	*h = s.height;
}

double dxWindowScale(void *p) { return ((__bridge DXWindow *)p).backingScaleFactor; }

void dxWindowState(void *p, bool *zoomed, bool *mini) {
	DXWindow *w = (__bridge DXWindow *)p;
	*zoomed = w.isZoomed;
	*mini = w.isMiniaturized;
}

void dxWindowCommand(void *p, int cmd) {
	DXWindow *w = (__bridge DXWindow *)p;
	switch (cmd) {
	case 1: [w miniaturize:nil]; break;
	case 2: if (!w.isZoomed) [w zoom:nil]; break;
	case 3: if (w.isZoomed) [w zoom:nil]; break;
	case 4: [w performClose:nil]; break;
	}
}

void dxWindowSetColors(void *p, double r, double g, double b, double a, bool dark) {
	DXWindow *w = (__bridge DXWindow *)p;
	if (a > 0) {
		NSColor *c = [NSColor colorWithSRGBRed:r green:g blue:b alpha:a];
		w.backgroundColor = c;
		w.contentView.layer.backgroundColor = c.CGColor;
	}
	w.appearance = [NSAppearance appearanceNamed:dark ? NSAppearanceNameDarkAqua : NSAppearanceNameAqua];
}

void dxWindowPresent(void *p, CGImageRef img, double scale) {
	DXWindow *w = (__bridge DXWindow *)p;
	CALayer *layer = w.contentView.layer;
	[CATransaction begin];
	[CATransaction setDisableActions:YES];
	layer.contentsScale = scale;
	layer.contents = (__bridge id)img;
	[CATransaction commit];
}

void dxWindowRequestTick(void *p) {
	DXWindow *w = (__bridge DXWindow *)p;
	if (dxrLinkRequest(p)) return; // [render]
	w.tickWanted = true;
	if (w.link && !CVDisplayLinkIsRunning(w.link)) CVDisplayLinkStart(w.link);
	if (!w.link) {
		dispatch_after(dispatch_time(DISPATCH_TIME_NOW, 16 * NSEC_PER_MSEC), dispatch_get_main_queue(), ^{
			if (!w.tickWanted) return;
			w.tickWanted = false;
			goWindowTick(w.handle); // [popups] read now: 0 once the window was released
		});
	}
}

void dxWindowSetCursor(int c) {
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

void dxWindowMouseLocation(void *p, double *x, double *y) {
	DXWindow *w = (__bridge DXWindow *)p;
	NSPoint s = [NSEvent mouseLocation];
	NSPoint wp = [w convertPointFromScreen:s];
	NSPoint v = [w.contentView convertPoint:wp fromView:nil];
	*x = v.x;
	*y = v.y;
}

bool dxWindowIsKey(void *p) { return ((__bridge DXWindow *)p).isKeyWindow; }

// ---- clipboard ----------------------------------------------------------------------

char *dxPasteboardText(void) {
	NSString *s = [[NSPasteboard generalPasteboard] stringForType:NSPasteboardTypeString];
	if (!s) return NULL;
	return strdup(s.UTF8String);
}

bool dxPasteboardSetText(const char *s) {
	NSPasteboard *pb = [NSPasteboard generalPasteboard];
	[pb clearContents];
	return [pb setString:[NSString stringWithUTF8String:s] forType:NSPasteboardTypeString];
}

// dxWindowViewsPNG writes what the content view shows, its native subviews (text fields)
// included, as PNG at the window's scale: a measuring aid (DOWNEX_UI_SHOTS), never used by
// the product otherwise.
bool dxWindowViewsPNG(void *p, const char *path) {
	@autoreleasepool {
		NSWindow *w = (__bridge NSWindow *)p;
		NSView *v = w.contentView;
		[v displayIfNeeded];
		for (NSView *s in v.subviews) [s displayIfNeeded];
		[CATransaction flush];
		double scale = w.backingScaleFactor;
		size_t pw = (size_t)ceil(v.bounds.size.width * scale), ph = (size_t)ceil(v.bounds.size.height * scale);
		CGColorSpaceRef cs = CGColorSpaceCreateWithName(kCGColorSpaceSRGB);
		CGContextRef c = CGBitmapContextCreate(NULL, pw, ph, 8, 0, cs, (CGBitmapInfo)kCGImageAlphaPremultipliedLast);
		CGColorSpaceRelease(cs);
		if (!c) return false;
		CGContextTranslateCTM(c, 0, ph); // the layer tree is flipped already
		CGContextScaleCTM(c, scale, -scale);
		[v.layer renderInContext:c];
		CGImageRef img = CGBitmapContextCreateImage(c);
		CGContextRelease(c);
		if (!img) return false;
		NSBitmapImageRep *rep = [[NSBitmapImageRep alloc] initWithCGImage:img];
		CGImageRelease(img);
		NSData *png = [rep representationUsingType:NSBitmapImageFileTypePNG properties:@{}];
		return [png writeToFile:[NSString stringWithUTF8String:path] atomically:YES];
	}
}
