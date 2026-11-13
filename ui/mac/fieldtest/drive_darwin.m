//go:build darwin && cgo

// [fields] The test driver's AppKit side (drive.go).

#import <Cocoa/Cocoa.h>
#import <QuartzCore/QuartzCore.h>
#import <objc/runtime.h>
#include <stdbool.h>

void dtPump(double seconds) {
	NSDate *until = [NSDate dateWithTimeIntervalSinceNow:seconds];
	for (;;) {
		@autoreleasepool {
			NSEvent *e = [NSApp nextEventMatchingMask:NSEventMaskAny untilDate:until inMode:NSDefaultRunLoopMode dequeue:YES];
			if (!e) break;
			[NSApp sendEvent:e];
			[NSApp updateWindows];
		}
	}
}

void *dtWindow(const char *title) {
	NSString *t = [NSString stringWithUTF8String:title];
	for (NSWindow *w in NSApp.windows) {
		if ([w.title isEqualToString:t] && w.isVisible) return (__bridge void *)w;
	}
	return NULL;
}

bool dtActivate(void *p) {
	NSWindow *w = (__bridge NSWindow *)p;
	[NSApp activateIgnoringOtherApps:YES];
	[w makeKeyAndOrderFront:nil];
	for (int i = 0; i < 10 && !w.isKeyWindow; i++) dtPump(0.02);
	return w.isKeyWindow;
}

static NSEventModifierFlags dtFlagsOf(int m) {
	NSEventModifierFlags f = 0;
	if (m & 1) f |= NSEventModifierFlagShift;
	if (m & 2) f |= NSEventModifierFlagControl;
	if (m & 4) f |= NSEventModifierFlagOption;
	if (m & 8) f |= NSEventModifierFlagCommand;
	return f;
}

void dtKey(void *p, int down, unsigned short code, const char *chars, const char *plain, int mods) {
	NSWindow *w = (__bridge NSWindow *)p;
	NSEvent *e = [NSEvent keyEventWithType:down ? NSEventTypeKeyDown : NSEventTypeKeyUp location:NSZeroPoint
		modifierFlags:dtFlagsOf(mods) timestamp:NSProcessInfo.processInfo.systemUptime windowNumber:w.windowNumber
		context:nil characters:[NSString stringWithUTF8String:chars]
		charactersIgnoringModifiers:[NSString stringWithUTF8String:plain] isARepeat:NO keyCode:code];
	[NSApp postEvent:e atStart:NO];
	dtPump(0.01);
}

char *dtResponder(void *p) {
	NSResponder *r = ((__bridge NSWindow *)p).firstResponder;
	return strdup(r ? NSStringFromClass(r.class).UTF8String : "nil");
}

static NSTextView *dtTextView(void *p) {
	NSResponder *r = ((__bridge NSWindow *)p).firstResponder;
	return [r isKindOfClass:NSTextView.class] ? (NSTextView *)r : nil;
}

// dtClient is the first responder as a text input client: a field's text view, or the
// root view while its own text input is active.
static id<NSTextInputClient> dtClient(void *p) {
	NSResponder *r = ((__bridge NSWindow *)p).firstResponder;
	return [r conformsToProtocol:@protocol(NSTextInputClient)] ? (id<NSTextInputClient>)r : nil;
}

bool dtMarked(void *p, const char *s) {
	id<NSTextInputClient> tv = dtClient(p);
	if (!tv) return false;
	NSString *t = [NSString stringWithUTF8String:s];
	[tv setMarkedText:t selectedRange:NSMakeRange(t.length, 0) replacementRange:NSMakeRange(NSNotFound, 0)];
	return tv.hasMarkedText;
}

bool dtInsert(void *p, const char *s) {
	id<NSTextInputClient> tv = dtClient(p);
	if (!tv) return false;
	[tv insertText:[NSString stringWithUTF8String:s] replacementRange:NSMakeRange(NSNotFound, 0)];
	return true;
}

bool dtAction(void *p, const char *sel) {
	// What a menu item with a nil target does in the key window: along the first
	// responder's chain (the test process may not become active, so the window may not be
	// key).
	NSWindow *w = (__bridge NSWindow *)p;
	return [w.firstResponder tryToPerform:NSSelectorFromString([NSString stringWithUTF8String:sel]) with:nil];
}

void dtEditMenu(void) {
	NSMenu *bar = [NSMenu new];
	NSMenuItem *appItem = [NSMenuItem new];
	appItem.submenu = [[NSMenu alloc] initWithTitle:@"App"];
	[bar addItem:appItem];
	NSMenuItem *editItem = [NSMenuItem new];
	NSMenu *edit = [[NSMenu alloc] initWithTitle:@"Edit"];
	[edit addItemWithTitle:@"Undo" action:@selector(undo:) keyEquivalent:@"z"];
	[edit addItemWithTitle:@"Redo" action:@selector(redo:) keyEquivalent:@"Z"];
	[edit addItem:NSMenuItem.separatorItem];
	[edit addItemWithTitle:@"Cut" action:@selector(cut:) keyEquivalent:@"x"];
	[edit addItemWithTitle:@"Copy" action:@selector(copy:) keyEquivalent:@"c"];
	[edit addItemWithTitle:@"Paste" action:@selector(paste:) keyEquivalent:@"v"];
	[edit addItemWithTitle:@"Select All" action:@selector(selectAll:) keyEquivalent:@"a"];
	editItem.submenu = edit;
	[bar addItem:editItem];
	NSApp.mainMenu = bar;
}

void dtNoMenu(void) { NSApp.mainMenu = [NSMenu new]; }

bool dtSnapshot(void *p, const char *path) {
	NSWindow *w = (__bridge NSWindow *)p;
	NSView *v = w.contentView;
	// The caret blinks: it is left out of the picture, so that the text alone is compared.
	NSMutableArray<NSView *> *carets = [NSMutableArray array];
	NSTextView *tv = dtTextView(p);
	NSColor *caretColor = tv.insertionPointColor;
	if (tv) {
		tv.insertionPointColor = NSColor.clearColor;
		for (NSView *s in tv.subviews) {
			if ([NSStringFromClass(s.class) containsString:@"Insertion"] && !s.hidden) {
				s.hidden = YES;
				[carets addObject:s];
			}
		}
		[tv setNeedsDisplay:YES];
	}
	[v displayIfNeeded];
	[tv displayIfNeeded];
	for (NSView *s in v.subviews) [s displayIfNeeded];
	[CATransaction flush];
	double scale = w.backingScaleFactor;
	size_t pw = (size_t)ceil(v.bounds.size.width * scale), ph = (size_t)ceil(v.bounds.size.height * scale);
	CGColorSpaceRef cs = CGColorSpaceCreateWithName(kCGColorSpaceSRGB);
	CGContextRef c = CGBitmapContextCreate(NULL, pw, ph, 8, 0, cs, (CGBitmapInfo)kCGImageAlphaPremultipliedLast);
	CGColorSpaceRelease(cs);
	if (!c) return false;
	// CALayer draws y up in a bitmap context; the content view's layer tree is flipped
	// already, so the context is flipped to match what the screen shows.
	CGContextTranslateCTM(c, 0, ph);
	CGContextScaleCTM(c, scale, -scale);
	[v.layer renderInContext:c];
	CGImageRef img = CGBitmapContextCreateImage(c);
	CGContextRelease(c);
	if (tv) tv.insertionPointColor = caretColor;
	for (NSView *s in carets) s.hidden = NO;
	NSBitmapImageRep *rep = [[NSBitmapImageRep alloc] initWithCGImage:img];
	CGImageRelease(img);
	NSData *png = [rep representationUsingType:NSBitmapImageFileTypePNG properties:@{}];
	return [png writeToFile:[NSString stringWithUTF8String:path] atomically:YES];
}

void dtClick(void *p, double x, double y) {
	NSWindow *w = (__bridge NSWindow *)p;
	NSView *v = w.contentView;
	NSPoint wp = [v convertPoint:NSMakePoint(x, y) toView:nil];
	for (int i = 0; i < 2; i++) {
		NSEvent *e = [NSEvent mouseEventWithType:i == 0 ? NSEventTypeLeftMouseDown : NSEventTypeLeftMouseUp
			location:wp modifierFlags:0 timestamp:NSProcessInfo.processInfo.systemUptime
			windowNumber:w.windowNumber context:nil eventNumber:0 clickCount:1 pressure:i == 0 ? 1 : 0];
		[NSApp postEvent:e atStart:NO]; // both before the loop runs: a press tracks until its release
	}
	dtPump(0.05);
}

void dtMove(void *p, double x, double y) {
	NSWindow *w = (__bridge NSWindow *)p;
	NSPoint wp = [w.contentView convertPoint:NSMakePoint(x, y) toView:nil];
	NSEvent *e = [NSEvent mouseEventWithType:NSEventTypeMouseMoved location:wp modifierFlags:0
		timestamp:NSProcessInfo.processInfo.systemUptime windowNumber:w.windowNumber context:nil
		eventNumber:0 clickCount:0 pressure:0];
	[NSApp postEvent:e atStart:NO];
	dtPump(0.02);
}

char *dtEditorInfo(void *p) {
	NSTextView *tv = dtTextView(p);
	if (!tv) return strdup("");
	NSString *s = [NSString stringWithFormat:
		@"quotes=%d dashes=%d replace=%d correct=%d spell=%d links=%d data=%d padding=%g field=%d secure=%d marked=%d",
		tv.automaticQuoteSubstitutionEnabled, tv.automaticDashSubstitutionEnabled, tv.automaticTextReplacementEnabled,
		tv.automaticSpellingCorrectionEnabled, tv.continuousSpellCheckingEnabled, tv.automaticLinkDetectionEnabled,
		tv.automaticDataDetectionEnabled, tv.textContainer.lineFragmentPadding, tv.isFieldEditor,
		[NSStringFromClass(tv.class) containsString:@"Secure"], tv.hasMarkedText];
	if (tv.textStorage.length > 0) {
		NSLayoutManager *lm = tv.layoutManager;
		CGGlyph g = [lm CGGlyphAtIndex:0];
		NSFont *f = [tv.textStorage attribute:NSFontAttributeName atIndex:0 effectiveRange:NULL];
		NSRect r = [lm boundingRectForGlyphRange:NSMakeRange(0, 1) inTextContainer:tv.textContainer];
		NSString *name = (__bridge_transfer NSString *)CTFontCopyNameForGlyph((__bridge CTFontRef)f, g);
		s = [s stringByAppendingFormat:@" glyph=%u/%@ font=%@/%g advance=%g", g, name, f.fontName, f.pointSize, r.size.width];
	}
	return strdup(s.UTF8String);
}

// ---- the pasteboard: a private one for the whole test process ------------------------

static NSPasteboard *dtBoard;
static NSInteger dtRealCount = -1;

static NSPasteboard *dtGeneral(id cls, SEL sel) { return dtBoard; }

// dtPrivatePasteboard makes +[NSPasteboard generalPasteboard] a private, uniquely named
// pasteboard for this process, so that the fields' Cut, Copy and Paste never touch the
// user's clipboard; it notes the real one's change count (read only).
void dtPrivatePasteboard(void) {
	if (dtBoard) return;
	dtRealCount = [NSPasteboard pasteboardWithName:NSPasteboardNameGeneral].changeCount;
	dtBoard = [NSPasteboard pasteboardWithUniqueName];
	Method m = class_getClassMethod(NSPasteboard.class, @selector(generalPasteboard));
	method_setImplementation(m, (IMP)dtGeneral);
}

// dtRealPasteboardChanged reports whether anything wrote to the user's pasteboard since
// dtPrivatePasteboard (another program may have: it is a hint, read only).
bool dtRealPasteboardChanged(void) {
	return dtRealCount >= 0 && [NSPasteboard pasteboardWithName:NSPasteboardNameGeneral].changeCount != dtRealCount;
}

long dtBoardCount(void) { return dtBoard ? (long)dtBoard.changeCount : -1; }

char *dtBoardString(void) {
	NSString *s = [dtBoard stringForType:NSPasteboardTypeString];
	return strdup(s ? s.UTF8String : "");
}

void dtSetBoardString(const char *s) {
	[dtBoard clearContents];
	[dtBoard setString:[NSString stringWithUTF8String:s] forType:NSPasteboardTypeString];
}

void dtReleaseBoard(void) { [dtBoard releaseGlobally]; }

// ---- views and methods --------------------------------------------------------------

// dtFieldViews describes the content view's subviews (the fields' views): class, hidden,
// and whether any of it shows inside the content.
char *dtFieldViews(void *p) {
	NSView *v = ((__bridge NSWindow *)p).contentView;
	NSMutableArray *out = [NSMutableArray array];
	for (NSView *s in v.subviews) {
		bool shows = !s.hidden && NSIntersectsRect(s.frame, v.bounds);
		[out addObject:[NSString stringWithFormat:@"%@ hidden=%d shows=%d frame=%@", NSStringFromClass(s.class),
			s.hidden, shows, NSStringFromRect(s.frame)]];
	}
	return strdup([out componentsJoinedByString:@"; "].UTF8String);
}

// dtMoveTwice delivers one pointer move by both of AppKit's paths: to the root view as its
// tracking area's owner (directly) and to the first responder (posted).
void dtMoveTwice(void *p, double x, double y) {
	NSWindow *w = (__bridge NSWindow *)p;
	NSPoint wp = [w.contentView convertPoint:NSMakePoint(x, y) toView:nil];
	NSEvent *e = [NSEvent mouseEventWithType:NSEventTypeMouseMoved location:wp modifierFlags:0
		timestamp:NSProcessInfo.processInfo.systemUptime windowNumber:w.windowNumber context:nil
		eventNumber:0 clickCount:0 pressure:0];
	[w.contentView mouseMoved:e];
	[NSApp postEvent:e atStart:NO];
	dtPump(0.02);
}

// dtDuplicateSelectors lists the selectors class cls itself (not its superclasses) has more
// than one implementation of: a category's and the class's own, or two categories'.
char *dtDuplicateSelectors(const char *cls) {
	Class c = objc_getClass(cls);
	if (!c) return strdup("no class");
	unsigned int n = 0;
	Method *ms = class_copyMethodList(c, &n);
	NSCountedSet *seen = [NSCountedSet set];
	for (unsigned int i = 0; i < n; i++) [seen addObject:NSStringFromSelector(method_getName(ms[i]))];
	free(ms);
	NSMutableArray *dup = [NSMutableArray array];
	for (NSString *s in seen) {
		if ([seen countForObject:s] > 1) [dup addObject:s];
	}
	return strdup([dup componentsJoinedByString:@","].UTF8String);
}
