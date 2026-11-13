//go:build darwin && cgo

// The Objective-C side of the macOS menu-bar icon and its notifications
// (tray_darwin.go, docs/MACOS-SPEC.md §1), compiled with ARC. It is kept minimal: the
// logic is Go (menubar.go, mainthread.go), which calls the functions below on the
// main thread as AppKit requires; AppKit and UserNotifications call back through the
// Go functions that tray_darwin.go exports (_cgo_export.h).
//
// APIs used and their availability (the deployment target is macOS 13):
//   pthread_main_np                                                macOS 10.4
//   CGSessionCopyCurrentDictionary (CoreGraphics)                  macOS 10.3
//   dispatch_async, dispatch_get_main_queue                        macOS 10.6
//   NSApplication sharedApplication, run, stop:, postEvent:atStart:,
//     replyToApplicationShouldTerminate:, delegate                 macOS 10.0
//   NSApplication setActivationPolicy: (…Accessory)                macOS 10.6
//   NSApplicationDelegate applicationShouldTerminate: (NSTerminateLater),
//     applicationWillTerminate:                                    macOS 10.0
//   NSEvent otherEventWithType:… with NSEventTypeApplicationDefined macOS 10.12
//   NSProcessInfo beginActivityWithOptions:reason:
//     (NSActivityUserInitiatedAllowingIdleSystemSleep)             macOS 10.9
//   NSStatusBar systemStatusBar, statusItemWithLength:, removeStatusItem: macOS 10.0
//   NSStatusItem button                                            macOS 10.10
//   NSStatusItem visible                                           macOS 10.12
//   NSStatusItem menu; NSMenu, NSMenuItem (tag, target, enabled)   macOS 10.0
//   NSMenu cancelTrackingWithoutAnimation                          macOS 10.6
//   NSImage template; accessibilityDescription                     macOS 10.5; 10.6
//   NSBitmapImageRep imageRepWithData:                             macOS 10.0
//   UNUserNotificationCenter currentNotificationCenter, delegate,
//     requestAuthorizationWithOptions:completionHandler:,
//     addNotificationRequest:withCompletionHandler:                macOS 10.14
//   UNMutableNotificationContent, UNNotificationRequest            macOS 10.14
//   UNUserNotificationCenterDelegate, UNNotificationDefaultActionIdentifier macOS 10.14
//   UNNotificationPresentationOptionBanner, …List                  macOS 11.0
//   Test support: NSMenu itemArray, numberOfItems, performActionForItemAtIndex:,
//     NSMenuItem separatorItem                                     macOS 10.0
//
// Threading: every AppKit call runs on the main thread (the functions check it). The
// notification center's completion handlers run on a queue of its own: they only
// call thread-safe Go functions or hop to the main queue. Blocks queued with
// dispatch_async to the main queue run in the main run loop's common modes, so also
// while the status menu is open (event tracking) and while a termination is
// postponed (modal panel mode).

#import <AppKit/AppKit.h>
#import <CoreGraphics/CoreGraphics.h>
#import <UserNotifications/UserNotifications.h>
#include <pthread.h>
#include <string.h>

#include "tray_darwin.h"
#include "_cgo_export.h"

#if !__has_feature(objc_arc)
#error "tray_darwin.m needs ARC: tray_darwin.go's CFLAGS pass -fobjc-arc"
#endif

// DownexString converts a C string from Go (valid UTF-8 without NULs: cText);
// stringWithUTF8String: returns nil on anything else, and nil titles raise.
static NSString *DownexString(const char *s) {
	NSString *r = s != NULL ? [NSString stringWithUTF8String:s] : nil;
	return r != nil ? r : @"";
}

// DownexIsAppBundle reports whether the process runs from an app bundle: only one has
// a notification center; currentNotificationCenter raises for a bare executable, also
// for one with an embedded Info.plist (a bundle identifier but no .app).
static BOOL DownexIsAppBundle(void) {
	NSBundle *bundle = [NSBundle mainBundle];
	return bundle.bundleIdentifier != nil && [bundle.bundlePath.pathExtension isEqualToString:@"app"];
}

// The application's delegate, also the notification center's.
@interface DownexAppDelegate : NSObject <NSApplicationDelegate, UNUserNotificationCenterDelegate>
@end

@implementation DownexAppDelegate

// The system asks the app to quit (logout, restart, Activity Monitor): the daemon
// stops gracefully first (goDownexAppShouldTerminate), then the termination proceeds
// (downexAppReplyTerminate).
- (NSApplicationTerminateReply)applicationShouldTerminate:(NSApplication *)sender {
	return goDownexAppShouldTerminate() ? NSTerminateNow : NSTerminateLater;
}

- (void)applicationWillTerminate:(NSNotification *)notification {
	goDownexAppWillTerminate(); // exits with the daemon's status
}

// Notifications also show while the app is active.
- (void)userNotificationCenter:(UNUserNotificationCenter *)center
       willPresentNotification:(UNNotification *)notification
         withCompletionHandler:(void (^)(UNNotificationPresentationOptions options))completionHandler {
	completionHandler(UNNotificationPresentationOptionBanner | UNNotificationPresentationOptionList);
}

// A click on a notification opens the window.
- (void)userNotificationCenter:(UNUserNotificationCenter *)center
    didReceiveNotificationResponse:(UNNotificationResponse *)response
             withCompletionHandler:(void (^)(void))completionHandler {
	if ([response.actionIdentifier isEqualToString:UNNotificationDefaultActionIdentifier]) {
		goDownexNotificationClicked();
	}
	completionHandler();
}

@end

// Both delegate properties are weak: the delegate is kept here, as is the activity.
static DownexAppDelegate *downexDelegate;
static id<NSObject> downexActivity;

int downexIsMainThread(void) {
	return pthread_main_np() != 0;
}

int downexHasWindowServer(void) {
	CFDictionaryRef session = CGSessionCopyCurrentDictionary();
	if (session == NULL) {
		return 0;
	}
	CFRelease(session);
	return 1;
}

int downexAppStart(void) {
	if (!pthread_main_np()) {
		return DOWNEX_APP_NOT_MAIN;
	}
	if (!downexHasWindowServer()) {
		return DOWNEX_APP_NO_SESSION;
	}
	@autoreleasepool {
		[NSApplication sharedApplication];
		// Downex Agent.app is LSUIElement already; a bare downexd gets no Dock icon either.
		[NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
		downexDelegate = [[DownexAppDelegate alloc] init];
		NSApp.delegate = downexDelegate;
		// Before the app finishes launching ([NSApp run]), so that the click that
		// launched the app is delivered. Exceptions must not unwind into Go.
		if (DownexIsAppBundle()) {
			@try {
				[UNUserNotificationCenter currentNotificationCenter].delegate = downexDelegate;
			} @catch (NSException *e) {
				NSLog(@"Downex: no notification center: %@", e.reason);
			}
		}
		// An app without visible windows is put into App Nap, which throttles the
		// timers and the I/O of the whole process: the daemon's downloads. The
		// activity keeps it away for the life of the process (idle sleep stays allowed).
		downexActivity = [[NSProcessInfo processInfo]
		    beginActivityWithOptions:NSActivityUserInitiatedAllowingIdleSystemSleep
		                      reason:@"Downex runs downloads in the background"];
	}
	return DOWNEX_APP_OK;
}

void downexAppRun(void) {
	if (!pthread_main_np()) {
		return;
	}
	@autoreleasepool {
		[NSApp run];
	}
}

void downexAppStop(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		[NSApp stop:nil];
		// stop: takes effect after the next event has been processed: post one.
		NSEvent *wake = [NSEvent otherEventWithType:NSEventTypeApplicationDefined
		                                   location:NSZeroPoint
		                              modifierFlags:0
		                                  timestamp:0
		                               windowNumber:0
		                                    context:nil
		                                    subtype:0
		                                      data1:0
		                                      data2:0];
		if (wake != nil) {
			[NSApp postEvent:wake atStart:YES];
		}
	});
}

void downexAppReplyTerminate(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		[NSApp replyToApplicationShouldTerminate:YES];
	});
}

void downexWake(uintptr_t handle) {
	dispatch_async(dispatch_get_main_queue(), ^{
		goDownexTrayDrain(handle);
	});
}

void downexRelease(uintptr_t handle) {
	dispatch_async(dispatch_get_main_queue(), ^{
		goDownexTrayReleased(handle);
	});
}

// DownexStatus is one menu-bar icon: its status item and menu.
@interface DownexStatus : NSObject
@property(nonatomic) uintptr_t handle; // 0 once destroyed: the menu calls nothing
@property(nonatomic, strong) NSStatusItem *item;
@property(nonatomic, strong) NSMenu *menu;
@property(nonatomic, strong) NSMenuItem *line;
- (void)choose:(NSMenuItem *)sender;
@end

@implementation DownexStatus

- (void)choose:(NSMenuItem *)sender {
	uintptr_t handle = self.handle;
	if (handle != 0) {
		goDownexTrayCommand(handle, (int)sender.tag);
	}
}

@end

static NSImage *DownexTemplateImage(const void *png1x, int len1x, const void *png2x, int len2x, double points) {
	NSImage *image = [[NSImage alloc] initWithSize:NSMakeSize(points, points)];
	const void *data[2] = {png1x, png2x};
	int lens[2] = {len1x, len2x};
	for (int i = 0; i < 2; i++) {
		if (data[i] == NULL || lens[i] <= 0) {
			continue;
		}
		NSBitmapImageRep *rep = [NSBitmapImageRep imageRepWithData:[NSData dataWithBytes:data[i] length:(NSUInteger)lens[i]]];
		if (rep == nil) {
			continue;
		}
		rep.size = image.size; // in points: the larger one serves Retina displays
		[image addRepresentation:rep];
	}
	if (image.representations.count == 0) {
		return nil;
	}
	image.template = YES; // tinted by the menu bar for its appearance and highlight
	image.accessibilityDescription = @"Downex";
	return image;
}

void *downexStatusCreate(uintptr_t handle, const char *tip, const char *line, const void *png1x, int len1x,
                         const void *png2x, int len2x, double points) {
	if (!pthread_main_np()) {
		return NULL;
	}
	@autoreleasepool {
		NSImage *image = DownexTemplateImage(png1x, len1x, png2x, len2x, points);
		NSStatusItem *item =
		    [[NSStatusBar systemStatusBar] statusItemWithLength:(image != nil ? NSSquareStatusItemLength : NSVariableStatusItemLength)];
		if (item == nil) {
			return NULL;
		}
		DownexStatus *status = [[DownexStatus alloc] init];
		status.handle = handle;
		status.item = item;
		NSStatusBarButton *button = item.button;
		if (image != nil) {
			button.image = image;
		} else {
			button.title = @"Downex";
		}
		button.toolTip = DownexString(tip);

		NSMenu *menu = [[NSMenu alloc] initWithTitle:@"Downex"];
		menu.autoenablesItems = NO;
		NSMenuItem *first = [[NSMenuItem alloc] initWithTitle:DownexString(line) action:NULL keyEquivalent:@""];
		first.enabled = NO;
		[menu addItem:first];
		[menu addItem:[NSMenuItem separatorItem]];
		status.menu = menu;
		status.line = first;
		item.menu = menu; // AppKit opens it on a click
		item.visible = YES; // the system remembers an item hidden in an earlier run
		return (__bridge_retained void *)status;
	}
}

void downexStatusAddItem(void *p, int tag, const char *label) {
	if (!pthread_main_np() || p == NULL) {
		return;
	}
	@autoreleasepool {
		DownexStatus *status = (__bridge DownexStatus *)p;
		if (tag == 0) {
			[status.menu addItem:[NSMenuItem separatorItem]];
			return;
		}
		NSMenuItem *item = [[NSMenuItem alloc] initWithTitle:DownexString(label)
		                                              action:@selector(choose:)
		                                       keyEquivalent:@""];
		item.target = status; // weak: the status object is retained by Go's pointer
		item.tag = tag;
		item.enabled = YES;
		[status.menu addItem:item];
	}
}

void downexStatusSetVisible(void *p, int visible) {
	if (!pthread_main_np() || p == NULL) {
		return;
	}
	DownexStatus *status = (__bridge DownexStatus *)p;
	if (!visible) {
		[status.menu cancelTrackingWithoutAnimation];
	}
	status.item.visible = visible != 0;
}

void downexStatusSetTip(void *p, const char *tip, const char *line) {
	if (!pthread_main_np() || p == NULL) {
		return;
	}
	@autoreleasepool {
		DownexStatus *status = (__bridge DownexStatus *)p;
		status.item.button.toolTip = DownexString(tip);
		status.line.title = DownexString(line); // also while the menu is open
	}
}

void downexStatusDestroy(void *p) {
	if (!pthread_main_np() || p == NULL) {
		return;
	}
	@autoreleasepool {
		// Takes over Go's reference: the object is released at the end of this scope.
		DownexStatus *status = (__bridge_transfer DownexStatus *)p;
		status.handle = 0;
		[status.menu cancelTrackingWithoutAnimation];
		for (NSMenuItem *item in status.menu.itemArray) {
			item.target = nil;
		}
		status.item.menu = nil;
		[[NSStatusBar systemStatusBar] removeStatusItem:status.item];
		status.item = nil;
	}
}

char *downexStatusDescribe(void *p) {
	if (!pthread_main_np() || p == NULL) {
		return NULL;
	}
	@autoreleasepool {
		DownexStatus *status = (__bridge DownexStatus *)p;
		NSMutableArray<NSString *> *lines = [NSMutableArray array];
		[lines addObject:(status.item.visible ? @"visible" : @"hidden")];
		NSString *tip = status.item.button.toolTip;
		[lines addObject:(tip != nil ? tip : @"")];
		for (NSMenuItem *item in status.menu.itemArray) {
			if (item.separatorItem) {
				[lines addObject:@"-"];
			} else {
				[lines addObject:[(item.enabled ? @"" : @"(disabled) ") stringByAppendingString:item.title]];
			}
		}
		return strdup([lines componentsJoinedByString:@"\n"].UTF8String);
	}
}

void downexStatusPerform(void *p, int index) {
	if (!pthread_main_np() || p == NULL) {
		return;
	}
	DownexStatus *status = (__bridge DownexStatus *)p;
	if (index >= 0 && index < status.menu.numberOfItems) {
		[status.menu performActionForItemAtIndex:index];
	}
}

int downexNotifyAvailable(void) {
	return DownexIsAppBundle();
}

void downexNotifyPost(const char *title, const char *body) {
	if (!pthread_main_np() || !downexNotifyAvailable()) {
		return;
	}
	@autoreleasepool {
		NSString *t = DownexString(title);
		NSString *b = DownexString(body);
		UNUserNotificationCenter *center = nil;
		@try { // exceptions must not unwind into Go
			center = [UNUserNotificationCenter currentNotificationCenter];
		} @catch (NSException *e) {
			NSString *why = e.reason;
			goDownexNotifyResult(DOWNEX_NOTIFY_FAILED, (char *)(why != nil ? why.UTF8String : NULL));
			return;
		}
		// The first request asks the user (alerts only: no sound, no badge); later ones
		// answer at once with the user's choice, which System Settings may change.
		[center requestAuthorizationWithOptions:UNAuthorizationOptionAlert
		                      completionHandler:^(BOOL granted, NSError *error) {
			// On the notification center's queue.
			if (!granted) {
				NSString *why = error != nil ? error.localizedDescription : nil;
				goDownexNotifyResult(DOWNEX_NOTIFY_DENIED, (char *)(why != nil ? why.UTF8String : NULL));
				return;
			}
			dispatch_async(dispatch_get_main_queue(), ^{
				UNMutableNotificationContent *content = [[UNMutableNotificationContent alloc] init];
				content.title = t;
				content.body = b; // content.sound stays nil: silent
				UNNotificationRequest *request = [UNNotificationRequest requestWithIdentifier:[NSUUID UUID].UUIDString
				                                                                      content:content
				                                                                      trigger:nil];
				[center addNotificationRequest:request
				         withCompletionHandler:^(NSError *err) {
					if (err != nil) {
						NSString *why = err.localizedDescription;
						goDownexNotifyResult(DOWNEX_NOTIFY_FAILED, (char *)(why != nil ? why.UTF8String : NULL));
					}
				}];
			});
		}];
	}
}
