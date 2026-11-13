//go:build darwin && cgo && (desktop || dev || bindings)

// The Objective-C side of the macOS window processes (dock_darwin.go,
// docs/MACOS-SPEC.md §5), compiled with ARC. Each Downex window (the main window and every
// progress window) is a Wails v2.17.0 app of its own, whose NSApplication delegate this
// file corrects where it does not suit macOS:
//
//   - A progress window becomes an accessory app (no Dock icon, as LSUIElement in the
//     Info.plist of Downex Progress.app asks): Wails sets the regular policy in
//     applicationWillFinishLaunching: (downexBecomeAccessory).
//   - The system's request to quit (logout, restart, shut down, the Dock's Quit,
//     Activity Monitor: the quit Apple event, applicationShouldTerminate:) is postponed
//     until the window has shut down, and then proceeds. Wails' delegate answers
//     NSTerminateCancel, which cancels a logout, restart or shut down, and quits a moment
//     later anyway (DownexShouldTerminate).
//   - Activation: from macOS 14 on an app that is not active can only ask to be
//     activated (activate), which is granted when the active app has yielded to it
//     (downexYieldActivation, before a window opens another one); Wails uses
//     activateIgnoringOtherApps:, whose flag macOS 14 ignores. A window that is not the
//     active app shortly after it opened or was asked to show again (a second start
//     handed its arguments over: handleSecondInstanceNotification:) is put in front of
//     the other apps' windows without being activated (DownexRaiseIfInactive), so that a
//     confirmation or a progress window does not open unseen behind the browser.
//
// APIs used and their availability (the deployment target is macOS 13):
//   NSApplication activationPolicy, setActivationPolicy: (…Accessory)     macOS 10.6
//   NSApplication activateIgnoringOtherApps: (deprecated from 14)        macOS 10.0
//   NSApplication activate, yieldActivationToApplicationWithBundleIdentifier:
//     (@available; declared from the macOS 14 SDK on)                     macOS 14.0
//   NSApplication delegate, isActive, isHidden, unhideWithoutActivation, windows,
//     replyToApplicationShouldTerminate:                                  macOS 10.0
//   NSApplicationDelegate applicationShouldTerminate: (NSTerminateLater)  macOS 10.0
//   NSWindow isVisible, orderFrontRegardless                              macOS 10.0
//   NSBundle bundleWithPath:, bundleIdentifier                            macOS 10.0
//   class_getInstanceMethod, class_replaceMethod, method_getImplementation,
//     method_getTypeEncoding, protocol_getMethodDescription (objc runtime) macOS 10.5
//   dispatch_async, dispatch_after, dispatch_once, dispatch_semaphore_*   macOS 10.6
//   pthread_main_np                                                       macOS 10.4
//
// Threading: AppKit is used on the main thread only, from blocks queued to the main
// queue or where the caller is on the main thread. The main run loop serves the main
// queue in its common modes, also while a termination is postponed (modal panel mode).

#include <AvailabilityMacros.h>
#import <Cocoa/Cocoa.h>
#import <objc/runtime.h>
#include <pthread.h>

#include "dock_darwin.h"
#include "_cgo_export.h"

#if !__has_feature(objc_arc)
#error "dock_darwin.m needs ARC: dock_darwin.go's CFLAGS pass -fobjc-arc"
#endif

// The SDK declares activate and the activation yields from macOS 14 on; built with an
// older SDK, the app does without them.
#if MAC_OS_X_VERSION_MAX_ALLOWED >= 140000
#define DOWNEX_COOPERATIVE_ACTIVATION 1
#else
#define DOWNEX_COOPERATIVE_ACTIVATION 0
#endif

// DownexRequestActivation asks for the activation of the app on macOS 14 and later
// (activate); before, Wails' activateIgnoringOtherApps: activates it. Main thread.
static void DownexRequestActivation(void) {
#if DOWNEX_COOPERATIVE_ACTIVATION
	if (@available(macOS 14.0, *)) {
		[NSApp activate];
	}
#endif
}

// DownexActivate activates the app: activate on macOS 14 and later,
// activateIgnoringOtherApps: before. Main thread.
static void DownexActivate(void) {
#if DOWNEX_COOPERATIVE_ACTIVATION
	if (@available(macOS 14.0, *)) {
		[NSApp activate];
		return;
	}
#endif
	[NSApp activateIgnoringOtherApps:YES];
}

// DownexRaiseIfInactive puts the app's windows in front of the other apps' windows,
// without activating it, when it is not the active app after delay seconds: Launch
// Services opened it in the background, or macOS 14+ did not grant its activation.
static void DownexRaiseIfInactive(double delay) {
	dispatch_after(dispatch_time(DISPATCH_TIME_NOW, (int64_t)(delay * NSEC_PER_SEC)), dispatch_get_main_queue(), ^{
		if ([NSApp isActive]) {
			return;
		}
		if ([NSApp isHidden]) {
			[NSApp unhideWithoutActivation];
		}
		for (NSWindow *window in [NSApp windows]) {
			if ([window isVisible]) {
				[window orderFrontRegardless];
			}
		}
	});
}

void downexBecomeAccessory(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		if ([NSApp activationPolicy] != NSApplicationActivationPolicyAccessory) {
			[NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
			// Changing the policy can deactivate the app.
			DownexActivate();
		}
	});
}

// DownexShouldTerminate replaces applicationShouldTerminate: of Wails' delegate: the
// window shuts down (goDownexWindowShouldTerminate) and then lets the termination
// proceed (downexReplyTerminate). Main thread.
static NSApplicationTerminateReply DownexShouldTerminate(id receiver, SEL cmd, NSApplication *sender) {
	goDownexWindowShouldTerminate();
	return NSTerminateLater;
}

// Wails' handleSecondInstanceNotification:, which DownexHandOff wraps.
static IMP downexHandOffIMP;

// DownexHandOff runs Wails' handleSecondInstanceNotification: (a second start of this
// window handed its arguments over; Wails then shows the window and activates the app),
// asks for the activation the way macOS 14+ expects and puts the window in front when
// it is not granted. Main thread (distributed notifications arrive on the run loop of
// the thread that registered: Wails' delegate registered on the main thread).
static void DownexHandOff(id receiver, SEL cmd, NSNotification *note) {
	((void (*)(id, SEL, NSNotification *))downexHandOffIMP)(receiver, cmd, note);
	DownexRequestActivation();
	DownexRaiseIfInactive(0.5);
}

// DownexInstall replaces the methods once Wails has set its delegate (Wails' Run, before
// [NSApp run] serves the main queue; tried again for 5 s should it not be set yet).
// Main thread.
static void DownexInstall(int attempt) {
	id delegate = [NSApp delegate];
	if (delegate == nil) {
		if (attempt < 100) {
			dispatch_after(dispatch_time(DISPATCH_TIME_NOW, 50 * NSEC_PER_MSEC), dispatch_get_main_queue(), ^{
				DownexInstall(attempt + 1);
			});
		} else {
			NSLog(@"Downex: no application delegate: logout and activation hooks not installed");
		}
		return;
	}
	Class cls = [delegate class];
	SEL terminate = @selector(applicationShouldTerminate:);
	Method current = class_getInstanceMethod(cls, terminate);
	const char *types = current != NULL ? method_getTypeEncoding(current)
	                                    : protocol_getMethodDescription(@protocol(NSApplicationDelegate), terminate, NO, YES).types;
	if (types != NULL) {
		class_replaceMethod(cls, terminate, (IMP)DownexShouldTerminate, types);
	}
	SEL handOff = NSSelectorFromString(@"handleSecondInstanceNotification:");
	Method wails = class_getInstanceMethod(cls, handOff);
	if (wails != NULL) {
		downexHandOffIMP = method_getImplementation(wails);
		class_replaceMethod(cls, handOff, (IMP)DownexHandOff, method_getTypeEncoding(wails));
	}
	DownexRequestActivation();
	DownexRaiseIfInactive(0.5);
}

void downexInstallAppHooks(void) {
	static dispatch_once_t once;
	dispatch_once(&once, ^{
		dispatch_async(dispatch_get_main_queue(), ^{
			DownexInstall(0);
		});
	});
}

void downexReplyTerminate(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		[NSApp replyToApplicationShouldTerminate:YES];
	});
}

void downexYieldActivation(const char *bundlePath) {
#if DOWNEX_COOPERATIVE_ACTIVATION
	if (@available(macOS 14.0, *)) {
		if (NSApp == nil || bundlePath == NULL) {
			return; // AppKit has not started: this app has nothing to yield
		}
		@autoreleasepool {
			NSString *path = [NSString stringWithUTF8String:bundlePath];
			if (path == nil) {
				return;
			}
			void (^yield)(void) = ^{
				NSString *identifier = [[NSBundle bundleWithPath:path] bundleIdentifier];
				if (identifier != nil) {
					[NSApp yieldActivationToApplicationWithBundleIdentifier:identifier];
				}
			};
			if (pthread_main_np()) {
				yield();
				return;
			}
			// Before the caller opens the app, which then asks for its activation.
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
