//go:build darwin && cgo

// [popups] A window's system dialogs (see dialogs_darwin.h), set up as Wails v2.17's
// darwin dialogs set them up for today's Mac window (WailsContext.m MessageDialog,
// OpenFileDialog; WailsAlert.m addButton).

#import <Cocoa/Cocoa.h>
#include "dialogs_darwin.h"
#include "_cgo_export.h"

// Called from Go's threads: what it autoreleases goes with its own pool (a Go thread's
// pool never drains).
void dxpDialogsWake(uintptr_t handle) {
	@autoreleasepool {
		CFRunLoopRef main = CFRunLoopGetMain();
		CFRunLoopPerformBlock(main, kCFRunLoopCommonModes, ^{
			@autoreleasepool {
				goDialogsRun(handle);
			}
		});
		CFRunLoopWakeUp(main);
	}
}

bool dxpAlertRun(const char *title, const char *message) {
	NSAlert *alert = [NSAlert new];
	// Wails' QuestionDialog: the informational style (the application's icon).
	alert.alertStyle = NSAlertStyleInformational;
	alert.messageText = [NSString stringWithUTF8String:title];
	alert.informativeText = [NSString stringWithUTF8String:message];
	// Buttons "Yes" then "No"; No is both the default and the cancel button, and the
	// default wins (WailsAlert addButton): No takes Return, nothing takes Escape.
	NSButton *yes = [alert addButtonWithTitle:@"Yes"];
	yes.keyEquivalent = @"";
	NSButton *no = [alert addButtonWithTitle:@"No"];
	no.keyEquivalent = @"\r";
	alert.window.level = NSFloatingWindowLevel;
	NSModalResponse r = [alert runModal];
	return r == NSAlertFirstButtonReturn;
}

bool dxpOwnerCanSheet(void *op) {
	if (!op) return false;
	NSWindow *w = (__bridge NSWindow *)op;
	return w.visible && !w.miniaturized && w.attachedSheet == nil;
}

void dxpPickerRun(void *op, uintptr_t token, bool folders, const char *title, const char *start) {
	NSOpenPanel *p = [NSOpenPanel openPanel];
	NSString *t = [NSString stringWithUTF8String:title];
	if (t.length > 0) p.title = t; // a sheet shows no title, as today's
	if (!folders) {
		p.allowsOtherFileTypes = YES; // no filter
		p.allowsMultipleSelection = NO;
	}
	p.showsHiddenFiles = NO;
	NSString *dir = [NSString stringWithUTF8String:start];
	if (dir.length > 0) p.directoryURL = [NSURL fileURLWithPath:dir isDirectory:YES];
	p.canChooseFiles = !folders;
	p.canChooseDirectories = folders;
	p.canCreateDirectories = folders;
	p.resolvesAliases = NO;
	p.treatsFilePackagesAsDirectories = NO;
	NSWindow *owner = op ? (__bridge NSWindow *)op : nil;
	void (^done)(NSModalResponse) = ^(NSModalResponse r) {
		NSString *path = (r == NSModalResponseOK && p.URLs.count > 0) ? p.URLs.firstObject.path : nil;
		goPickDone(token, path ? (char *)path.UTF8String : NULL);
	};
	if (owner) {
		[p beginSheetModalForWindow:owner completionHandler:done];
	} else {
		done([p runModal]);
	}
}

void dxpPickerEnd(void *op) {
	NSWindow *owner = (__bridge NSWindow *)op;
	NSWindow *sheet = owner.attachedSheet;
	if ([sheet isKindOfClass:NSOpenPanel.class]) [owner endSheet:sheet returnCode:NSModalResponseCancel];
}

// dxpOpenURL is called from Go's threads (and the main one): its own pool, and NSWorkspace
// asked on the main thread, as AppKit expects. A main thread that does not get to it within
// a few seconds (busy, or waiting for this very call) leaves it queued and the call failed.
bool dxpOpenURL(const char *s) {
	@autoreleasepool {
		NSURL *u = [NSURL URLWithString:[NSString stringWithUTF8String:s]];
		if (!u) return false;
		if ([NSThread isMainThread]) return [[NSWorkspace sharedWorkspace] openURL:u];
		__block bool ok = false;
		dispatch_semaphore_t sem = dispatch_semaphore_create(0);
		dispatch_async(dispatch_get_main_queue(), ^{
			@autoreleasepool {
				ok = [[NSWorkspace sharedWorkspace] openURL:u];
			}
			dispatch_semaphore_signal(sem);
		});
		if (dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, 5 * NSEC_PER_SEC)) != 0) return false;
		return ok;
	}
}
