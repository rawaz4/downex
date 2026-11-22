//go:build darwin && cgo

// [popups] The test driver's AppKit side (drive.go). Test-only: no product imports it.

#import <Cocoa/Cocoa.h>
#include <stdbool.h>

void dptPump(double seconds) {
	NSDate *until = [NSDate dateWithTimeIntervalSinceNow:seconds];
	for (;;) {
		@autoreleasepool {
			NSEvent *e = [NSApp nextEventMatchingMask:NSEventMaskAny untilDate:until inMode:NSDefaultRunLoopMode dequeue:YES];
			if (!e) break;
			[NSApp sendEvent:e];
		}
	}
}

void dptPostMouse(void *wp, int type, double x, double y, int clicks) {
	NSWindow *w = (__bridge NSWindow *)wp;
	NSPoint p = [w.contentView convertPoint:NSMakePoint(x, y) toView:nil];
	NSEvent *e = [NSEvent mouseEventWithType:(NSEventType)type location:p modifierFlags:0
		timestamp:NSProcessInfo.processInfo.systemUptime windowNumber:w.windowNumber context:nil
		eventNumber:0 clickCount:clicks pressure:(clicks > 0 ? 1 : 0)];
	[NSApp postEvent:e atStart:NO];
}

void dptPostScroll(void *wp, double x, double y, int lines, int phase) {
	NSWindow *w = (__bridge NSWindow *)wp;
	NSPoint p = [w.contentView convertPoint:NSMakePoint(x, y) toView:nil];
	NSPoint s = [w convertPointToScreen:p];
	CGFloat top = NSMaxY(NSScreen.screens.firstObject.frame);
	CGEventRef c = phase ? CGEventCreateScrollWheelEvent(NULL, kCGScrollEventUnitPixel, 2, lines, 0)
	                     : CGEventCreateScrollWheelEvent(NULL, kCGScrollEventUnitLine, 1, lines);
	if (!c) return;
	if (phase) {
		CGEventSetIntegerValueField(c, kCGScrollWheelEventIsContinuous, 1);
		CGEventSetIntegerValueField(c, kCGScrollWheelEventScrollPhase, phase);
	}
	CGEventSetLocation(c, CGPointMake(s.x, top - s.y));
	CGEventSetIntegerValueField(c, kCGMouseEventWindowUnderMousePointer, w.windowNumber);
	CGEventSetIntegerValueField(c, kCGMouseEventWindowUnderMousePointerThatCanHandleThisEvent, w.windowNumber);
	NSEvent *e = [NSEvent eventWithCGEvent:c];
	CFRelease(c);
	if (e) [NSApp postEvent:e atStart:NO];
}

void dptPostKey(void *wp, int keyCode, bool down, const char *chars) {
	NSWindow *w = (__bridge NSWindow *)wp;
	NSString *s = [NSString stringWithUTF8String:chars];
	NSEvent *e = [NSEvent keyEventWithType:down ? NSEventTypeKeyDown : NSEventTypeKeyUp location:NSZeroPoint
		modifierFlags:0 timestamp:NSProcessInfo.processInfo.systemUptime windowNumber:w.windowNumber context:nil
		characters:s charactersIgnoringModifiers:s isARepeat:NO keyCode:(unsigned short)keyCode];
	[NSApp postEvent:e atStart:NO];
}

void dptMakeKey(void *wp) {
	NSWindow *w = (__bridge NSWindow *)wp;
	[NSApp activateIgnoringOtherApps:YES];
	[w makeKeyAndOrderFront:nil];
}

bool dptIsKey(void *wp) { return ((__bridge NSWindow *)wp).isKeyWindow; }

bool dptActive(void) { return NSApp.active; }

void dptMoveWindow(void *wp, double dx) {
	NSWindow *w = (__bridge NSWindow *)wp;
	[w setFrameOrigin:NSMakePoint(w.frame.origin.x + dx, w.frame.origin.y)];
}

void dptFrame(void *wp, double *x, double *y, double *w, double *h) {
	NSRect f = ((__bridge NSWindow *)wp).frame;
	*x = f.origin.x;
	*y = f.origin.y;
	*w = f.size.width;
	*h = f.size.height;
}

bool dptIgnoresMouse(void *wp) { return ((__bridge NSWindow *)wp).ignoresMouseEvents; }

void dptContentPoint(void *wp, double sx, double sy, double *x, double *y) {
	NSWindow *w = (__bridge NSWindow *)wp;
	NSPoint p = [w.contentView convertPoint:[w convertPointFromScreen:NSMakePoint(sx, sy)] fromView:nil];
	*x = p.x;
	*y = p.y;
}

bool dptTooltipShown(void) {
	for (NSWindow *w in NSApp.windows) {
		if ([NSStringFromClass(w.class) containsString:@"ToolTip"] && w.visible) return true;
	}
	return false;
}

void dptWarp(void *wp, double x, double y) {
	NSWindow *w = (__bridge NSWindow *)wp;
	NSPoint s = [w convertPointToScreen:[w.contentView convertPoint:NSMakePoint(x, y) toView:nil]];
	CGWarpMouseCursorPosition(CGPointMake(s.x, NSMaxY(NSScreen.screens.firstObject.frame) - s.y));
}

// ---- a made-up drag ---------------------------------------------------------------------

static NSPasteboard *dptPasteboard(const char *url, const char *text, const char *html, const char *files) {
	NSPasteboard *pb = [NSPasteboard pasteboardWithUniqueName]; // never the user's
	[pb clearContents];
	NSPasteboardItem *it = [NSPasteboardItem new];
	if (url) [it setString:[NSString stringWithUTF8String:url] forType:NSPasteboardTypeURL];
	if (text) [it setString:[NSString stringWithUTF8String:text] forType:NSPasteboardTypeString];
	if (html) [it setString:[NSString stringWithUTF8String:html] forType:NSPasteboardTypeHTML];
	NSMutableArray *items = [NSMutableArray array];
	if (it.types.count > 0) [items addObject:it];
	if (files) {
		for (NSString *p in [[NSString stringWithUTF8String:files] componentsSeparatedByString:@"\n"]) {
			if (p.length == 0) continue;
			NSPasteboardItem *f = [NSPasteboardItem new];
			[f setString:[NSURL fileURLWithPath:p].absoluteString forType:NSPasteboardTypeFileURL];
			[items addObject:f];
		}
	}
	[pb writeObjects:items];
	return pb;
}

@interface DPTDrag : NSObject <NSDraggingInfo>
@property(nonatomic, strong) NSPasteboard *pb;
@property(nonatomic, weak) NSWindow *win;
@property(nonatomic) NSPoint at; // window coordinates
@property(nonatomic) NSDragOperation mask;
@end

@implementation DPTDrag
@synthesize draggingFormation, animatesToDestination, numberOfValidItemsForDrop, springLoadingHighlight;
- (NSWindow *)draggingDestinationWindow { return self.win; }
- (NSDragOperation)draggingSourceOperationMask { return self.mask; }
- (NSPoint)draggingLocation { return self.at; }
- (NSPoint)draggedImageLocation { return self.at; }
- (NSImage *)draggedImage { return nil; }
- (NSPasteboard *)draggingPasteboard { return self.pb; }
- (id)draggingSource { return nil; }
- (NSInteger)draggingSequenceNumber { return 1; }
- (void)slideDraggedImageTo:(NSPoint)p {}
- (NSArray<NSString *> *)namesOfPromisedFilesDroppedAtDestination:(NSURL *)u { return nil; }
- (void)enumerateDraggingItemsWithOptions:(NSDraggingItemEnumerationOptions)o forView:(NSView *)v
	classes:(NSArray<Class> *)c searchOptions:(NSDictionary *)s
	usingBlock:(void (^)(NSDraggingItem *, NSInteger, BOOL *))b {}
- (void)resetSpringLoading {}
@end

int dptDrag(void *wp, int method, double x, double y, const char *url, const char *text, const char *html,
	const char *files, bool copyAllowed) {
	@autoreleasepool {
		NSWindow *w = (__bridge NSWindow *)wp;
		NSView *cv = w.contentView;
		DPTDrag *d = [DPTDrag new];
		d.pb = dptPasteboard(url, text, html, files);
		d.win = w;
		d.at = [cv convertPoint:NSMakePoint(x, y) toView:nil];
		d.mask = copyAllowed ? NSDragOperationEvery : NSDragOperationMove;
		int out = 0;
		id<NSDraggingDestination> dst = (id<NSDraggingDestination>)cv;
		switch (method) {
		case 1: out = (int)[dst draggingEntered:d]; break;
		case 2: out = (int)[dst draggingUpdated:d]; break;
		case 3: [dst draggingExited:d]; break;
		case 4: out = [dst performDragOperation:d] ? (int)NSDragOperationCopy : 0; break;
		}
		[d.pb releaseGlobally];
		return out;
	}
}

// ---- a text field -------------------------------------------------------------------------

void *dptAddField(void *wp, double x, double y, double w, double h, const char *text) {
	NSWindow *win = (__bridge NSWindow *)wp;
	NSTextField *f = [NSTextField textFieldWithString:[NSString stringWithUTF8String:text]];
	f.frame = NSMakeRect(x, y, w, h); // the content view is flipped, as the window's is
	if (!win.contentView.isFlipped) f.frame = NSMakeRect(x, win.contentView.bounds.size.height - y - h, w, h);
	[win.contentView addSubview:f];
	return (__bridge void *)f;
}

char *dptFieldText(void *fp) { return strdup(((__bridge NSTextField *)fp).stringValue.UTF8String); }

void dptRemoveField(void *fp) { [(__bridge NSTextField *)fp removeFromSuperview]; }

// ---- answering dialogs --------------------------------------------------------------------

static NSString *dptLast;

static NSString *dptKey(NSString *k) {
	if ([k isEqualToString:@"\r"]) return @"\\r";
	if ([k isEqualToString:@"\033"]) return @"\\e";
	return k;
}

static void dptCollect(NSView *v, NSMutableArray *texts, NSMutableArray *buttons) {
	for (NSView *s in v.subviews) {
		if ([s isKindOfClass:NSButton.class] && ((NSButton *)s).title.length > 0) {
			[buttons addObject:s];
		} else if ([s isKindOfClass:NSTextField.class] && ((NSTextField *)s).stringValue.length > 0) {
			[texts addObject:((NSTextField *)s).stringValue];
		}
		dptCollect(s, texts, buttons);
	}
}

static NSString *dptPanel(NSOpenPanel *p, bool sheet) {
	return [NSString stringWithFormat:@"picker files=%d dirs=%d create=%d multiple=%d other=%d hidden=%d aliases=%d packages=%d dir=%@ title=%@ sheet=%d",
		p.canChooseFiles, p.canChooseDirectories, p.canCreateDirectories, p.allowsMultipleSelection,
		p.allowsOtherFileTypes, p.showsHiddenFiles, p.resolvesAliases, p.treatsFilePackagesAsDirectories,
		p.directoryURL.path ?: @"", p.title ?: @"", sheet];
}

// dptTry answers what is open now; false when nothing is.
static bool dptTry(int answer, bool waitUp) {
	NSModalResponse code = answer == 1 ? NSModalResponseOK : NSModalResponseCancel;
	for (NSWindow *w in NSApp.windows) {
		if ([w.attachedSheet isKindOfClass:NSOpenPanel.class]) {
			dptLast = dptPanel((NSOpenPanel *)w.attachedSheet, true);
			if (answer) [w endSheet:w.attachedSheet returnCode:code];
			return true;
		}
	}
	NSWindow *m = NSApp.modalWindow;
	if (!m) return false;
	if ([m isKindOfClass:NSOpenPanel.class]) {
		dptLast = dptPanel((NSOpenPanel *)m, false);
		if (answer) [NSApp stopModalWithCode:code];
		return true;
	}
	// An alert is read once runModal has it up (it raises the window to the modal level
	// while the application is active): a second at most.
	if (!m.visible || (m.level < NSModalPanelWindowLevel && waitUp)) return false;
	NSMutableArray *texts = [NSMutableArray array], *buttons = [NSMutableArray array];
	dptCollect(m.contentView, texts, buttons);
	NSMutableArray *names = [NSMutableArray array];
	NSButton *pick = nil;
	for (NSButton *b in buttons) {
		[names addObject:[NSString stringWithFormat:@"%@[%@]", b.title, dptKey(b.keyEquivalent)]];
		if ((answer == 1 && [b.title isEqualToString:@"Yes"]) || (answer == 2 && [b.title isEqualToString:@"No"])) pick = b;
	}
	[names sortUsingSelector:@selector(compare:)];
	dptLast = [NSString stringWithFormat:@"alert texts=%@ buttons=%@ level=%ld",
		[texts componentsJoinedByString:@"|"], [names componentsJoinedByString:@","], (long)m.level];
	if (pick) [pick performClick:nil];
	return true;
}

static void dptAfter(int answer, double delay, int tries) {
	dispatch_after(dispatch_time(DISPATCH_TIME_NOW, (int64_t)(delay * NSEC_PER_SEC)), dispatch_get_main_queue(), ^{
		if (!dptTry(answer, tries > 180) && tries > 0) dptAfter(answer, 0.05, tries - 1);
	});
}

void dptAnswerDialog(int answer, double delay) {
	dptLast = nil;
	dptAfter(answer, delay, 200);
}

char *dptLastDialog(void) { return strdup((dptLast ?: @"").UTF8String); }

bool dptSheetOn(void *wp) { return ((__bridge NSWindow *)wp).attachedSheet != nil; }

// ---- a window's views after it closed --------------------------------------------------

void *dptHoldContentView(void *wp) { return (void *)CFBridgingRetain(((__bridge NSWindow *)wp).contentView); }

void dptPokeView(void *vp) {
	@autoreleasepool {
		NSView *v = (__bridge NSView *)vp;
		NSEvent *e = [NSEvent mouseEventWithType:NSEventTypeMouseMoved location:NSMakePoint(5, 5) modifierFlags:0
			timestamp:NSProcessInfo.processInfo.systemUptime windowNumber:0 context:nil eventNumber:0 clickCount:0 pressure:0];
		[v mouseMoved:e];
		[v mouseExited:e];
		[v accessibilityChildren];
	}
}

void dptReleaseObject(void *p) { CFRelease(p); }

void dptSetIgnoresMouse(void *wp, bool on) { ((__bridge NSWindow *)wp).ignoresMouseEvents = on; }

// ---- window state -------------------------------------------------------------------------

int dptTabbedWindows(void *wp) { return (int)((__bridge NSWindow *)wp).tabbedWindows.count; }

bool dptVisible(void *wp) { return ((__bridge NSWindow *)wp).visible; }

int dptVisibleTitled(const char *title) {
	NSString *t = [NSString stringWithUTF8String:title];
	int n = 0;
	for (NSWindow *w in NSApp.windows) {
		if (w.visible && [w.title isEqualToString:t]) n++;
	}
	return n;
}

bool dptPerform(void *wp, const char *sel) {
	NSWindow *w = (__bridge NSWindow *)wp;
	return [w.firstResponder tryToPerform:NSSelectorFromString([NSString stringWithUTF8String:sel]) with:nil];
}

void dptToggleFullScreen(void *wp) { [(__bridge NSWindow *)wp toggleFullScreen:nil]; }

bool dptFullScreen(void *wp) { return (((__bridge NSWindow *)wp).styleMask & NSWindowStyleMaskFullScreen) != 0; }

bool dptAcceptsFirstMouse(void *wp) {
	NSWindow *w = (__bridge NSWindow *)wp;
	NSEvent *e = [NSEvent mouseEventWithType:NSEventTypeLeftMouseDown location:NSMakePoint(1, 1)
		modifierFlags:0 timestamp:0 windowNumber:w.windowNumber context:nil eventNumber:0 clickCount:1 pressure:1];
	return [w.contentView acceptsFirstMouse:e];
}

void dptScreenContentMax(double *w, double *h) {
	NSWindowStyleMask style = NSWindowStyleMaskTitled | NSWindowStyleMaskClosable |
		NSWindowStyleMaskMiniaturizable | NSWindowStyleMaskResizable;
	NSRect c = [NSWindow contentRectForFrameRect:NSScreen.mainScreen.visibleFrame styleMask:style];
	*w = c.size.width;
	*h = c.size.height;
}

// ---- an alert in a modal loop ---------------------------------------------------------------

static long dptAlertResult = 0;

void dptRunAlert(double abortAfter) {
	dptAlertResult = 0;
	// From a run-loop block, as ui/mac's dialogs run theirs (outside any window's call).
	CFRunLoopPerformBlock(CFRunLoopGetMain(), kCFRunLoopCommonModes, ^{
		dispatch_after(dispatch_time(DISPATCH_TIME_NOW, (int64_t)(abortAfter * NSEC_PER_SEC)), dispatch_get_main_queue(), ^{
			if (NSApp.modalWindow) [NSApp abortModal];
		});
		NSAlert *a = [NSAlert new];
		a.messageText = @"modal test";
		dptAlertResult = (long)[a runModal];
	});
	CFRunLoopWakeUp(CFRunLoopGetMain());
}

long dptAlertDone(void) { return dptAlertResult; }

// dptScrollView hands lines notches of a mouse wheel (a line event, as a notched wheel makes)
// at (x, y) of win's content straight to its content view.
void dptScrollView(void *wp, double x, double y, int lines) {
	NSWindow *w = (__bridge NSWindow *)wp;
	NSPoint p = [w.contentView convertPoint:NSMakePoint(x, y) toView:nil];
	NSPoint s = [w convertPointToScreen:p];
	CGFloat top = NSMaxY(NSScreen.screens.firstObject.frame);
	CGEventRef c = CGEventCreateScrollWheelEvent(NULL, kCGScrollEventUnitLine, 1, lines);
	if (!c) return;
	CGEventSetLocation(c, CGPointMake(s.x, top - s.y));
	NSEvent *e = [NSEvent eventWithCGEvent:c];
	CFRelease(c);
	if (e) [w.contentView scrollWheel:e];
}
