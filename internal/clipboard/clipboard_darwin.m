//go:build darwin && cgo

// The Objective-C side of the macOS clipboard monitor (clipboard_darwin.go), compiled
// with ARC. It only reads the general pasteboard; the rules (which types exclude the
// content, what a text means) are Go.
//
// APIs used and their availability (the deployment target is macOS 13):
//   NSPasteboard generalPasteboard, changeCount, types, stringForType:   macOS 10.0
//   NSPasteboardTypeString                                               macOS 10.6
//   NSString lengthOfBytesUsingEncoding:, UTF8String                     macOS 10.0

#import <AppKit/AppKit.h>
#include <stdlib.h>
#include <string.h>
#include "clipboard_darwin.h"

long downexClipChangeCount(void) {
	@autoreleasepool {
		return (long)[[NSPasteboard generalPasteboard] changeCount];
	}
}

char *downexClipTypes(long *changeCount) {
	@autoreleasepool {
		NSPasteboard *pb = [NSPasteboard generalPasteboard];
		*changeCount = (long)[pb changeCount];
		NSArray<NSPasteboardType> *types = [pb types];
		if (types == nil || [types count] == 0) {
			return NULL;
		}
		NSString *joined = [types componentsJoinedByString:@"\n"];
		const char *s = [joined UTF8String];
		return s == NULL ? NULL : strdup(s);
	}
}

char *downexClipText(long maxBytes, long *changeCount) {
	@autoreleasepool {
		NSPasteboard *pb = [NSPasteboard generalPasteboard];
		*changeCount = (long)[pb changeCount];
		NSString *text = [pb stringForType:NSPasteboardTypeString];
		if (text == nil || (long)[text lengthOfBytesUsingEncoding:NSUTF8StringEncoding] > maxBytes) {
			return NULL;
		}
		const char *s = [text UTF8String];
		return s == NULL ? NULL : strdup(s);
	}
}
