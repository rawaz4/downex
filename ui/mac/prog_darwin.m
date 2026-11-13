//go:build darwin && cgo

// The Objective-C side of ui/mac's program part (see prog_darwin.h and program.go), with ARC.
// It keeps what today's Wails window (ui/dock_darwin.m, ui/menu.go, Wails v2.17's
// AppDelegate.m, WailsMenu.m and WailsContext.m) does on macOS:
//
//   - the system's request to quit (logout, restart, shut down, the Dock's Quit, the quit
//     Apple event) is postponed (NSTerminateLater) until the windows have shut down, and
//     then proceeds; closing the last window never quits by itself (Go decides);
//   - the menu bar of ui/menu.go's macMenu with Wails' Edit and Window menus;
//   - activation the way macOS 14+ expects (activate, yields), and windows put in front of
//     other apps' windows when the activation is not granted;
//   - Wails' hand-off by distributed notification, and its lock folder;
//   - the open panel as a sheet and the Yes/No alert with No the default.
//
// APIs and their availability (the deployment target is macOS 13): NSApplication activate
// and yieldActivationToApplicationWithBundleIdentifier: (macOS 14, behind @available and the
// SDK check), everything else macOS 10.x or 11.

#include <AvailabilityMacros.h>
#import <Cocoa/Cocoa.h>
#import <objc/runtime.h>
#include <pthread.h>
#include "prog_darwin.h"
#include "_cgo_export.h"

#if !__has_feature(objc_arc)
#error "prog_darwin.m needs ARC (cgo.go passes -fobjc-arc)"
#endif

#if MAC_OS_X_VERSION_MAX_ALLOWED >= 140000
#define DXG_COOPERATIVE_ACTIVATION 1
#else
#define DXG_COOPERATIVE_ACTIVATION 0
#endif

#pragma clang diagnostic ignored "-Wdeprecated-declarations" // activateIgnoringOtherApps: before macOS 14

// dxgOnMain runs block on the main thread and waits for it. From another thread it is a
// run-loop block, not a dispatch block: a modal dialog it shows does not hold up the main
// dispatch queue, so the windows keep drawing (their frames and posts come through it).
static void dxgOnMain(void (^block)(void)) {
	if (pthread_main_np()) {
		block();
		return;
	}
	dispatch_semaphore_t done = dispatch_semaphore_create(0);
	CFRunLoopPerformBlock(CFRunLoopGetMain(), kCFRunLoopCommonModes, ^{
		block();
		dispatch_semaphore_signal(done);
	});
	CFRunLoopWakeUp(CFRunLoopGetMain());
	dispatch_semaphore_wait(done, DISPATCH_TIME_FOREVER);
}

// dxgHandleOf is the Go handle of a DXWindow (window_darwin.m's "handle" property), else 0.
static uintptr_t dxgHandleOf(NSWindow *w) {
	if (w == nil || ![w respondsToSelector:NSSelectorFromString(@"handle")]) return 0;
	NSNumber *n = [w valueForKey:@"handle"];
	return (uintptr_t)n.unsignedLongLongValue;
}

// ---- application delegate -----------------------------------------------------------

@interface DXGAppDelegate : NSObject <NSApplicationDelegate>
@end

@implementation DXGAppDelegate
- (NSApplicationTerminateReply)applicationShouldTerminate:(NSApplication *)sender {
	// The windows shut down first (goProgTerminate), then dxgReplyTerminate lets the
	// termination go on: a logout is never cancelled by Downex.
	goProgTerminate();
	return NSTerminateLater;
}
- (BOOL)applicationShouldTerminateAfterLastWindowClosed:(NSApplication *)sender {
	return NO; // the program ends its run loop itself with its last window
}
- (BOOL)applicationSupportsSecureRestorableState:(NSApplication *)app {
	return YES;
}
@end

// ---- menu bar -----------------------------------------------------------------------

@interface DXGMenuTarget : NSObject
@property(nonatomic, copy) NSString *aboutTitle;
@property(nonatomic, copy) NSString *aboutMessage;
@end

@implementation DXGMenuTarget
- (void)about:(id)sender {
	NSAlert *a = [NSAlert new];
	a.alertStyle = NSAlertStyleInformational;
	a.messageText = self.aboutTitle;
	a.informativeText = self.aboutMessage;
	NSButton *ok = [a addButtonWithTitle:@"OK"];
	ok.keyEquivalent = @"\r";
	[a.window setLevel:NSFloatingWindowLevel];
	[a runModal];
}
- (void)settings:(id)sender { goProgMenu(1, dxgKeyWindowHandle()); }
- (void)quit:(id)sender { goProgMenu(2, dxgKeyWindowHandle()); }
- (void)closeWindow:(id)sender { goProgMenu(3, dxgKeyWindowHandle()); }
@end

static DXGAppDelegate *dxgDelegate;
static DXGMenuTarget *dxgMenuTarget;

static NSMenuItem *dxgItem(NSString *title, SEL action, NSString *key, NSEventModifierFlags mods, id target) {
	NSMenuItem *it = [[NSMenuItem alloc] initWithTitle:title action:action keyEquivalent:key];
	it.keyEquivalentModifierMask = mods;
	it.target = target;
	return it;
}

// dxgMenu is a menu as Wails makes each one (WailsMenu initWithNSTitle): its items are
// always enabled (autoenablesItems NO), whoever would answer them.
static NSMenu *dxgMenu(NSString *title) {
	NSMenu *m = [[NSMenu alloc] initWithTitle:title];
	m.autoenablesItems = NO;
	return m;
}

static NSMenu *dxgSubmenu(NSMenu *bar, NSString *title) {
	NSMenuItem *holder = [[NSMenuItem alloc] initWithTitle:title action:nil keyEquivalent:@""];
	NSMenu *m = dxgMenu(title);
	holder.submenu = m;
	[bar addItem:holder];
	return m;
}

void dxgInstall(bool settings, const char *aboutTitle, const char *aboutMessage) {
	dxgDelegate = [DXGAppDelegate new];
	NSApp.delegate = dxgDelegate;
	dxgMenuTarget = [DXGMenuTarget new];
	dxgMenuTarget.aboutTitle = [NSString stringWithUTF8String:aboutTitle];
	dxgMenuTarget.aboutMessage = [NSString stringWithUTF8String:aboutMessage];
	DXGMenuTarget *t = dxgMenuTarget;
	const NSEventModifierFlags cmd = NSEventModifierFlagCommand;

	NSMenu *bar = [[NSMenu alloc] initWithTitle:@""];
	// The app menu (macOS titles it with the app's name): ui/menu.go's macMenu.
	NSMenu *app = dxgSubmenu(bar, @"Downex");
	[app addItem:dxgItem(@"About Downex", @selector(about:), @"", 0, t)];
	if (settings) {
		[app addItem:[NSMenuItem separatorItem]];
		[app addItem:dxgItem(@"Settings…", @selector(settings:), @",", cmd, t)];
	}
	[app addItem:[NSMenuItem separatorItem]];
	[app addItem:dxgItem(@"Hide Downex", @selector(hide:), @"h", cmd, NSApp)];
	[app addItem:[NSMenuItem separatorItem]];
	[app addItem:dxgItem(@"Quit Downex", @selector(quit:), @"q", cmd, t)];

	NSMenu *file = dxgSubmenu(bar, @"File");
	[file addItem:dxgItem(@"Close Window", @selector(closeWindow:), @"w", cmd, t)];

	// Wails' Edit menu (WailsMenu.m EditMenu): the first responder's commands.
	NSMenu *edit = dxgSubmenu(bar, @"Edit");
	[edit addItem:dxgItem(@"Undo", @selector(undo:), @"z", cmd, nil)];
	[edit addItem:dxgItem(@"Redo", @selector(redo:), @"z", NSEventModifierFlagShift | cmd, nil)];
	[edit addItem:[NSMenuItem separatorItem]];
	[edit addItem:dxgItem(@"Cut", @selector(cut:), @"x", cmd, nil)];
	[edit addItem:dxgItem(@"Copy", @selector(copy:), @"c", cmd, nil)];
	[edit addItem:dxgItem(@"Paste", @selector(paste:), @"v", cmd, nil)];
	[edit addItem:dxgItem(@"Paste and Match Style", @selector(pasteAsRichText:), @"v",
		NSEventModifierFlagOption | NSEventModifierFlagShift | cmd, nil)];
	// Wails gives Delete U+0008 with no modifier (its accel "backspace"): the menu shows ⌫,
	// and the Backspace key, which types U+007F, never matches it, so text fields keep it.
	[edit addItem:dxgItem(@"Delete", @selector(delete:), @"\b", 0, nil)];
	[edit addItem:dxgItem(@"Select All", @selector(selectAll:), @"a", cmd, nil)];
	[edit addItem:[NSMenuItem separatorItem]];
	NSMenuItem *speechHolder = [[NSMenuItem alloc] initWithTitle:@"Speech" action:nil keyEquivalent:@""];
	NSMenu *speech = dxgMenu(@"Speech");
	[speech addItem:dxgItem(@"Start Speaking", @selector(startSpeaking:), @"", 0, nil)];
	[speech addItem:dxgItem(@"Stop Speaking", @selector(stopSpeaking:), @"", 0, nil)];
	speechHolder.submenu = speech;
	[edit addItem:speechHolder];

	// Wails' Window menu (WailsMenu.m WindowMenu): Full Screen is NSWindow's
	// enterFullScreenMode:, which only enters full screen and keeps the item's title (AppKit
	// retitles a toggleFullScreen: item "Enter/Exit Full Screen"). Wails never makes it
	// NSApp.windowsMenu, so AppKit adds neither its window list nor its tab and tiling items.
	NSMenu *window = dxgSubmenu(bar, @"Window");
	[window addItem:dxgItem(@"Minimize", @selector(performMiniaturize:), @"m", cmd, nil)];
	[window addItem:dxgItem(@"Zoom", @selector(performZoom:), @"", 0, nil)];
	[window addItem:[NSMenuItem separatorItem]];
	[window addItem:dxgItem(@"Full Screen", NSSelectorFromString(@"enterFullScreenMode:"), @"f",
		NSEventModifierFlagControl | cmd, nil)];

	NSApp.mainMenu = bar;
}

// The reply is a run-loop block, not a main-queue block: NSTerminateLater waits in a nested
// loop that may itself run inside a main-queue block (a terminate: posted to the main queue,
// a window's post), and the serial main queue runs nothing else until that block returns.
void dxgReplyTerminate(bool yes) {
	CFRunLoopRef main = CFRunLoopGetMain();
	CFRunLoopPerformBlock(main, kCFRunLoopCommonModes, ^{
		[NSApp replyToApplicationShouldTerminate:yes];
	});
	CFRunLoopWakeUp(main);
}

// ---- activation ---------------------------------------------------------------------

void dxgRequestActivation(void) {
#if DXG_COOPERATIVE_ACTIVATION
	if (@available(macOS 14.0, *)) {
		[NSApp activate];
	}
#endif
}

void dxgActivate(void) {
#if DXG_COOPERATIVE_ACTIVATION
	if (@available(macOS 14.0, *)) {
		[NSApp activate];
		return;
	}
#endif
	[NSApp activateIgnoringOtherApps:YES];
}

void dxgRaiseIfInactive(double delay) {
	dispatch_after(dispatch_time(DISPATCH_TIME_NOW, (int64_t)(delay * NSEC_PER_SEC)), dispatch_get_main_queue(), ^{
		if ([NSApp isActive]) return;
		if ([NSApp isHidden]) [NSApp unhideWithoutActivation];
		for (NSWindow *w in [NSApp windows]) {
			if ([w isVisible]) [w orderFrontRegardless];
		}
	});
}

void dxgYieldActivation(const char *bundlePath) {
#if DXG_COOPERATIVE_ACTIVATION
	if (@available(macOS 14.0, *)) {
		if (NSApp == nil || bundlePath == NULL) return;
		@autoreleasepool {
			NSString *path = [NSString stringWithUTF8String:bundlePath];
			if (path == nil) return;
			void (^yield)(void) = ^{
				NSString *identifier = [[NSBundle bundleWithPath:path] bundleIdentifier];
				if (identifier != nil) [NSApp yieldActivationToApplicationWithBundleIdentifier:identifier];
			};
			if (pthread_main_np()) {
				yield();
				return;
			}
			dispatch_semaphore_t done = dispatch_semaphore_create(0);
			dispatch_async(dispatch_get_main_queue(), ^{
				yield();
				dispatch_semaphore_signal(done);
			});
			dispatch_semaphore_wait(done, dispatch_time(DISPATCH_TIME_NOW, 250 * NSEC_PER_MSEC));
		}
	}
#else
	(void)bundlePath;
#endif
}

// ---- distributed notifications ------------------------------------------------------

void dxgPost(const char *name, const char *object) {
	@autoreleasepool {
		// Wails passes its message as the notification's object (a sandbox drops userInfo).
		NSString *msg = [NSString stringWithUTF8String:object];
		[[NSDistributedNotificationCenter defaultCenter]
			postNotificationName:[NSString stringWithUTF8String:name]
			object:msg userInfo:nil deliverImmediately:YES];
	}
}

void *dxgObserve(const char *name, uintptr_t token) {
	NSString *n = [NSString stringWithUTF8String:name];
	id obs = [[NSDistributedNotificationCenter defaultCenter] addObserverForName:n object:nil
		queue:[NSOperationQueue mainQueue] usingBlock:^(NSNotification *note) {
			id o = note.object;
			if (![o isKindOfClass:[NSString class]]) return;
			goProgDistributed(token, (char *)[(NSString *)o UTF8String]);
		}];
	return (__bridge_retained void *)obs;
}

void dxgUnobserve(void *p) {
	if (p == NULL) return;
	id obs = (__bridge_transfer id)p;
	[[NSDistributedNotificationCenter defaultCenter] removeObserver:obs];
}

char *dxgTempDir(void) {
	@autoreleasepool {
		return strdup(NSTemporaryDirectory().UTF8String);
	}
}

char *dxgPreferredLanguage(void) {
	@autoreleasepool {
		NSString *l = [NSLocale preferredLanguages].firstObject;
		return l ? strdup(l.UTF8String) : NULL;
	}
}

bool dxgSystemDark(void) {
	__block bool dark = false;
	dxgOnMain(^{
		NSAppearanceName n = [[NSApp effectiveAppearance]
			bestMatchFromAppearancesWithNames:@[NSAppearanceNameAqua, NSAppearanceNameDarkAqua]];
		dark = [n isEqualToString:NSAppearanceNameDarkAqua];
	});
	return dark;
}

// ---- windows ------------------------------------------------------------------------

static const char dxgNormalKey = 0;
static const char dxgObserverKey = 0;

static const char dxgZoomingKey = 0;

static void dxgRemember(NSWindow *w) {
	if (objc_getAssociatedObject(w, &dxgZoomingKey) != nil) return; // the zoom's animation frames
	if (w.isZoomed || w.isMiniaturized || (w.styleMask & NSWindowStyleMaskFullScreen)) return;
	NSSize s = w.contentView.bounds.size;
	objc_setAssociatedObject(w, &dxgNormalKey, [NSValue valueWithSize:s], OBJC_ASSOCIATION_RETAIN_NONATOMIC);
}

// dxgShouldZoom is the window delegate's windowShouldZoom:toFrame: (added to its class when
// it has none): the size before a zoom is remembered, and the animation's frames, which are
// neither zoomed nor the user's, are not, until the zoom has ended.
static BOOL dxgShouldZoom(id self, SEL _cmd, NSWindow *w, NSRect frame) {
	if (!w.isZoomed) dxgRemember(w);
	objc_setAssociatedObject(w, &dxgZoomingKey, @YES, OBJC_ASSOCIATION_RETAIN_NONATOMIC);
	__weak NSWindow *weak = w;
	dispatch_after(dispatch_time(DISPATCH_TIME_NOW, (int64_t)(NSEC_PER_SEC)), dispatch_get_main_queue(), ^{
		NSWindow *s = weak;
		if (!s) return;
		objc_setAssociatedObject(s, &dxgZoomingKey, nil, OBJC_ASSOCIATION_RETAIN_NONATOMIC);
		dxgRemember(s);
	});
	return YES;
}

void dxgWindowTrack(void *p) {
	NSWindow *w = (__bridge NSWindow *)p;
	if (w.delegate != nil) {
		class_addMethod([w.delegate class], @selector(windowShouldZoom:toFrame:), (IMP)dxgShouldZoom,
			"c@:@{CGRect={CGPoint=dd}{CGSize=dd}}");
	}
	NSSize s = w.contentView.bounds.size;
	objc_setAssociatedObject(w, &dxgNormalKey, [NSValue valueWithSize:s], OBJC_ASSOCIATION_RETAIN_NONATOMIC);
	__weak NSWindow *weak = w;
	id obs = [[NSNotificationCenter defaultCenter] addObserverForName:NSWindowDidResizeNotification object:w
		queue:nil usingBlock:^(NSNotification *n) {
			NSWindow *s = weak;
			if (s) dxgRemember(s);
		}];
	id closeObs = [[NSNotificationCenter defaultCenter] addObserverForName:NSWindowWillCloseNotification object:w
		queue:nil usingBlock:^(NSNotification *n) {
			NSArray *all = objc_getAssociatedObject(n.object, &dxgObserverKey);
			for (id o in all) [[NSNotificationCenter defaultCenter] removeObserver:o];
			objc_setAssociatedObject(n.object, &dxgObserverKey, nil, OBJC_ASSOCIATION_RETAIN_NONATOMIC);
		}];
	objc_setAssociatedObject(w, &dxgObserverKey, @[obs, closeObs], OBJC_ASSOCIATION_RETAIN_NONATOMIC);
}

void dxgWindowNormalSize(void *p, double *wd, double *ht) {
	NSWindow *w = (__bridge NSWindow *)p;
	dxgRemember(w);
	NSValue *v = objc_getAssociatedObject(w, &dxgNormalKey);
	NSSize s = v ? v.sizeValue : w.contentView.bounds.size;
	*wd = s.width;
	*ht = s.height;
}

void dxgWindowZoom(void *p) {
	NSWindow *w = (__bridge NSWindow *)p;
	if (!w.isZoomed) [w zoom:nil];
}

void dxgWindowFrameSize(void *p, double *wd, double *ht) {
	NSWindow *w = (__bridge NSWindow *)p;
	NSRect f = w.frame;
	NSRect c = [w contentRectForFrameRect:f];
	*wd = f.size.width - c.size.width;
	*ht = f.size.height - c.size.height;
}

void dxgSystemFrameSize(double *wd, double *ht) {
	NSWindowStyleMask style = NSWindowStyleMaskTitled | NSWindowStyleMaskClosable |
		NSWindowStyleMaskMiniaturizable | NSWindowStyleMaskResizable;
	NSRect c = NSMakeRect(0, 0, 500, 500);
	NSRect f = [NSWindow frameRectForContentRect:c styleMask:style];
	*wd = f.size.width - c.size.width;
	*ht = f.size.height - c.size.height;
}

void dxgWindowFront(void *p, bool activate) {
	NSWindow *w = (__bridge NSWindow *)p;
	if ([NSApp isHidden]) [NSApp unhideWithoutActivation];
	if (w.isMiniaturized) [w deminiaturize:nil];
	if (activate) {
		[w makeKeyAndOrderFront:nil];
		[NSApp activateIgnoringOtherApps:YES]; // Wails' WindowShow; macOS 14 asks below
		dxgRequestActivation();
		dxgRaiseIfInactive(0.5);
	} else {
		[w orderFront:nil];
	}
}

long dxgWindowNumber(void *p) { return (long)((__bridge NSWindow *)p).windowNumber; }

bool dxgWindowFullScreen(void *p) {
	return (((__bridge NSWindow *)p).styleMask & NSWindowStyleMaskFullScreen) != 0;
}

uintptr_t dxgKeyWindowHandle(void) {
	uintptr_t h = dxgHandleOf(NSApp.keyWindow);
	if (h == 0) h = dxgHandleOf(NSApp.mainWindow);
	if (h == 0) {
		for (NSWindow *w in NSApp.orderedWindows) {
			if (w.isVisible && (h = dxgHandleOf(w)) != 0) break;
		}
	}
	return h;
}

// ---- system dialogs -----------------------------------------------------------------




char *dxgMenuDump(void) {
	@autoreleasepool {
		NSMutableString *out = [NSMutableString string];
		for (NSMenuItem *top in NSApp.mainMenu.itemArray) {
			for (NSMenuItem *it in top.submenu.itemArray) {
				if (it.isSeparatorItem) {
					[out appendFormat:@"%@\t-\t\t0\t\n", top.title];
					continue;
				}
				[out appendFormat:@"%@\t%@\t%@\t%lu\t%@\n", top.title, it.title, it.keyEquivalent,
					(unsigned long)it.keyEquivalentModifierMask, it.action ? NSStringFromSelector(it.action) : @""];
				for (NSMenuItem *sub in it.submenu.itemArray) {
					[out appendFormat:@"%@ > %@\t%@\t%@\t%lu\t%@\n", top.title, it.title, sub.title, sub.keyEquivalent,
						(unsigned long)sub.keyEquivalentModifierMask, sub.action ? NSStringFromSelector(sub.action) : @""];
				}
			}
		}
		return strdup(out.UTF8String);
	}
}

char *dxgMenuAutoenabling(void) {
	@autoreleasepool {
		NSMutableString *out = [NSMutableString string];
		for (NSMenuItem *top in NSApp.mainMenu.itemArray) {
			if (top.submenu.autoenablesItems) [out appendFormat:@"%@\n", top.title];
			for (NSMenuItem *it in top.submenu.itemArray) {
				if (it.submenu.autoenablesItems) [out appendFormat:@"%@ > %@\n", top.title, it.title];
			}
		}
		return strdup(out.UTF8String);
	}
}

bool dxgPerformKey(const char *key, unsigned long mods) {
	NSString *k = [NSString stringWithUTF8String:key];
	NSEvent *e = [NSEvent keyEventWithType:NSEventTypeKeyDown location:NSZeroPoint modifierFlags:mods
		timestamp:0 windowNumber:0 context:nil characters:k charactersIgnoringModifiers:k isARepeat:NO keyCode:0];
	return [NSApp.mainMenu performKeyEquivalent:e];
}

bool dxgPerformItem(const char *menu, const char *title) {
	NSString *m = [NSString stringWithUTF8String:menu];
	NSString *t = [NSString stringWithUTF8String:title];
	__block NSMenu *sub = nil;
	__block NSInteger index = -1;
	dxgOnMain(^{
		for (NSMenuItem *top in NSApp.mainMenu.itemArray) {
			if (![top.title isEqualToString:m]) continue;
			sub = top.submenu;
			index = [sub indexOfItemWithTitle:t];
		}
	});
	if (sub == nil || index < 0) return false;
	// Performed from the main run loop, as a click is: a modal action (About) runs inside.
	dxgOnMain(^{
		NSMenuItem *it = [sub itemAtIndex:index];
		[NSApp sendAction:it.action to:it.target from:it];
	});
	return true;
}

static NSString *dxgKeyName(NSString *k) {
	if ([k isEqualToString:@"\r"]) return @"\\r";
	if ([k isEqualToString:@"\033"]) return @"\\e";
	return k;
}

char *dxgModalState(void) {
	__block NSString *s = @"";
	dxgOnMain(^{
		NSWindow *front = NSApp.modalWindow;
		NSString *kind = @"modal";
		if (front == nil) {
			for (NSWindow *w in NSApp.windows) {
				if (w.attachedSheet != nil) {
					front = w.attachedSheet;
					kind = @"sheet";
					break;
				}
			}
		}
		if (front == nil) return;
		NSMutableString *out = [NSMutableString stringWithFormat:@"%@ %@", kind, NSStringFromClass([front class])];
		if ([front isKindOfClass:[NSSavePanel class]]) {
			NSSavePanel *p = (NSSavePanel *)front;
			[out appendFormat:@" title=%@ message=%@ visible=%d", p.title ?: @"", p.message ?: @"", p.isVisible];
		}
		// An alert's texts and buttons, found in its window (NSAlert keeps no link to it).
		NSMutableArray *texts = [NSMutableArray array];
		NSMutableArray *buttons = [NSMutableArray array];
		NSMutableArray *stack = [NSMutableArray arrayWithObject:(front.contentView.superview ?: front.contentView)];
		while (stack.count > 0 && ![front isKindOfClass:[NSSavePanel class]]) {
			NSView *v = stack.lastObject;
			[stack removeLastObject];
			if ([v isKindOfClass:[NSButton class]] && ((NSButton *)v).title.length > 0) {
				NSButton *b = (NSButton *)v;
				[buttons addObject:[NSString stringWithFormat:@"%@[%@]", b.title, dxgKeyName(b.keyEquivalent)]];
			} else if ([v isKindOfClass:[NSTextField class]] && ((NSTextField *)v).stringValue.length > 0) {
				[texts addObject:((NSTextField *)v).stringValue];
			}
			for (NSView *c in v.subviews.reverseObjectEnumerator) [stack addObject:c];
		}
		if (texts.count > 0) [out appendFormat:@" text=%@", [texts componentsJoinedByString:@"|"]];
		if (buttons.count > 0) [out appendFormat:@" buttons=%@", [buttons componentsJoinedByString:@","]];
		// What Return presses (NSWindow's default button), whatever the buttons' own keys.
		NSButtonCell *def = front.defaultButtonCell;
		[out appendFormat:@" default=%@", def ? def.title : @""];
		s = out;
	});
	return strdup(s.UTF8String);
}

void dxgPostKey(const char *key, unsigned short code, unsigned long mods) {
	NSString *k = [NSString stringWithUTF8String:key];
	dxgOnMain(^{
		NSWindow *w = NSApp.modalWindow;
		if (w == nil) {
			for (NSWindow *o in NSApp.windows) {
				if (o.attachedSheet != nil) { w = o.attachedSheet; break; }
			}
		}
		if (w == nil) w = NSApp.keyWindow;
		const NSEventType types[2] = {NSEventTypeKeyDown, NSEventTypeKeyUp};
		for (int i = 0; i < 2; i++) {
			NSEvent *e = [NSEvent keyEventWithType:types[i] location:NSZeroPoint modifierFlags:mods
				timestamp:[NSProcessInfo processInfo].systemUptime windowNumber:w.windowNumber context:nil
				characters:k charactersIgnoringModifiers:k isARepeat:NO keyCode:code];
			// To the window itself, as the window server would route it to a key window: an
			// inactive app has no key window for NSApp to route it to.
			if (types[i] == NSEventTypeKeyDown && [w performKeyEquivalent:e]) continue;
			[w sendEvent:e];
		}
	});
}

bool dxgPressModalButton(const char *title) {
	NSString *t = [NSString stringWithUTF8String:title];
	__block NSButton *found = nil;
	__block NSSavePanel *panel = nil;
	dxgOnMain(^{
		NSWindow *w = NSApp.modalWindow;
		if (w == nil) {
			for (NSWindow *o in NSApp.windows) {
				if (o.attachedSheet != nil) { w = o.attachedSheet; break; }
			}
		}
		if (w == nil) return;
		// An open panel's buttons are drawn by another process: its ok: and cancel:.
		if ([w isKindOfClass:[NSSavePanel class]]) {
			panel = (NSSavePanel *)w;
			return;
		}
		NSMutableArray *stack = [NSMutableArray arrayWithObject:(w.contentView.superview ?: w.contentView)];
		while (stack.count > 0 && found == nil) {
			NSView *v = stack.lastObject;
			[stack removeLastObject];
			if ([v isKindOfClass:[NSButton class]] && [((NSButton *)v).title isEqualToString:t]) found = (NSButton *)v;
			for (NSView *c in v.subviews) [stack addObject:c];
		}
	});
	if (panel != nil) {
		bool cancel = [t isEqualToString:@"Cancel"];
		dispatch_async(dispatch_get_main_queue(), ^{
			if (cancel) [panel cancel:nil];
			else [panel ok:nil];
		});
		return true;
	}
	if (found == nil) return false;
	// Clicked from the run loop after this call: the click may end the modal loop.
	dispatch_async(dispatch_get_main_queue(), ^{ [found performClick:nil]; });
	return true;
}

void dxgAppState(bool *active, bool *hidden) {
	__block bool a = false, h = false;
	dxgOnMain(^{
		a = NSApp.isActive;
		h = NSApp.isHidden;
	});
	*active = a;
	*hidden = h;
}

void dxgWindowState(void *p, bool *key, bool *visible, bool *mini) {
	NSWindow *w = (__bridge NSWindow *)p;
	__block bool k = false, v = false, m = false;
	dxgOnMain(^{
		k = w.isKeyWindow;
		v = w.isVisible;
		m = w.isMiniaturized;
	});
	*key = k;
	*visible = v;
	*mini = m;
}

bool dxgSendQuitEvent(void) {
	// The event as Launch Services delivers it (logout, restart, shut down, the Dock's
	// Quit), handed to the Apple Event Manager on the main thread, which calls the handler
	// NSApplication registered for it. (An event this process sends to itself is not
	// delivered by the window server; another process's needs the user's consent.)
	NSAppleEventDescriptor *me = [NSAppleEventDescriptor descriptorWithProcessIdentifier:getpid()];
	NSAppleEventDescriptor *ev = [NSAppleEventDescriptor appleEventWithEventClass:kCoreEventClass
		eventID:kAEQuitApplication targetDescriptor:me returnID:kAutoGenerateReturnID transactionID:kAnyTransactionID];
	if (ev == nil) return false;
	CFRunLoopRef main = CFRunLoopGetMain();
	CFRunLoopPerformBlock(main, kCFRunLoopCommonModes, ^{
		AppleEvent reply = {typeNull, NULL}; // handlerRefCon: any non-null value (NSApplication ignores it)
		OSErr e = [[NSAppleEventManager sharedAppleEventManager] dispatchRawAppleEvent:ev.aeDesc
			withRawReply:&reply handlerRefCon:(SRefCon)&reply];
		if (e != noErr) NSLog(@"Downex: quit event: %d", e);
		AEDisposeDesc(&reply);
	});
	CFRunLoopWakeUp(main);
	return true;
}

void dxgTerminate(void) {
	dispatch_async(dispatch_get_main_queue(), ^{ [NSApp terminate:nil]; });
}

void dxgAlertCritical(const char *title, const char *message) {
	NSString *t = [NSString stringWithUTF8String:title];
	NSString *m = [NSString stringWithUTF8String:message];
	dxgOnMain(^{
		[NSApplication sharedApplication];
		NSAlert *a = [NSAlert new];
		a.alertStyle = NSAlertStyleCritical;
		a.messageText = t;
		a.informativeText = m;
		[a addButtonWithTitle:@"OK"];
		[a runModal];
	});
}
