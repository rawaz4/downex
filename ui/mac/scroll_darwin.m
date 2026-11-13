//go:build darwin && cgo

// [mac scroll] The system's scroll bar settings, which today's WebKit window follows: the
// scroller style (overlay or legacy) and what a click in the track does. AppKit posts
// NSPreferredScrollerStyleDidChangeNotification when the style changes (the setting, or a
// mouse plugged in or out while it is "Automatically"); the paging setting comes with the
// global domain's change notice.

#import <Cocoa/Cocoa.h>
#include "scroll_darwin.h"
#include "_cgo_export.h"

@interface DXScrollObserver : NSObject
@end

@implementation DXScrollObserver
- (void)changed:(NSNotification *)n {
	goAppEnvironment();
}
@end

static DXScrollObserver *scrollObserver;

bool dxScrollOverlay(void) {
	return [NSScroller preferredScrollerStyle] == NSScrollerStyleOverlay;
}

bool dxScrollClickJumps(void) {
	return [[NSUserDefaults standardUserDefaults] boolForKey:@"AppleScrollerPagingBehavior"];
}

void dxScrollObserve(void) {
	if (scrollObserver != nil) {
		return;
	}
	scrollObserver = [DXScrollObserver new];
	[[NSNotificationCenter defaultCenter] addObserver:scrollObserver selector:@selector(changed:)
		name:NSPreferredScrollerStyleDidChangeNotification object:nil];
	// System Settings posts this when "Click in the scroll bar to" changes.
	[[NSDistributedNotificationCenter defaultCenter] addObserver:scrollObserver selector:@selector(changed:)
		name:@"AppleNoRedisplayAppearancePreferenceChanged" object:nil];
}

void dxScrollTestNotify(void) {
	[[NSNotificationCenter defaultCenter] postNotificationName:NSPreferredScrollerStyleDidChangeNotification object:nil];
}
