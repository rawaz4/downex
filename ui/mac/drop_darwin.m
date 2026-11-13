//go:build darwin && cgo

// [popups] Drag and drop onto a window (see drop_darwin.h). The dragging methods are added
// to the content view's class at run time (class_addMethod), so that window_darwin.m's view
// stays as it is; each view knows its drop handle through an associated object.

#import <Cocoa/Cocoa.h>
#import <objc/runtime.h>
#include "drop_darwin.h"
#include "_cgo_export.h"

static char dxpDropKey;

static uintptr_t dxpDropHandleOf(NSView *v) {
	NSNumber *n = objc_getAssociatedObject(v, &dxpDropKey);
	return n ? (uintptr_t)n.unsignedLongLongValue : 0;
}

static char *dxpDup(NSString *s) { return s ? strdup(s.UTF8String) : NULL; }

static NSString *dxpWeblocURL(NSString *path) {
	if (![[path.pathExtension lowercaseString] isEqualToString:@"webloc"]) return nil;
	NSDictionary *d = [NSDictionary dictionaryWithContentsOfURL:[NSURL fileURLWithPath:path] error:nil];
	id u = d[@"URL"];
	return [u isKindOfClass:NSString.class] ? u : nil;
}

static dxpDropData dxpRead(NSPasteboard *pb) {
	dxpDropData d = {0};
	NSArray<NSPasteboardType> *types = pb.types;
	NSArray<NSURL *> *files = [pb readObjectsForClasses:@[NSURL.class]
		options:@{NSPasteboardURLReadingFileURLsOnlyKey: @YES}];
	d.files = files.count > 0 || [types containsObject:@"com.apple.pasteboard.promised-file-url"] ||
		[types containsObject:@"com.apple.pasteboard.promised-file-content-type"] ||
		[types containsObject:(NSPasteboardType)kPasteboardTypeFileURLPromise];
	if (files.count > 0) {
		NSString *path = files[0].path;
		d.file = dxpDup(path);
		d.webloc = dxpDup(dxpWeblocURL(path));
	}
	if ([types containsObject:NSPasteboardTypeURL]) {
		NSString *u = [pb stringForType:NSPasteboardTypeURL];
		NSURL *url = u ? [NSURL URLWithString:u] : nil;
		if (u && !url.isFileURL) {
			d.url = dxpDup(u);
			d.hasURL = true;
		}
	}
	if (!d.url) {
		NSArray<NSURL *> *urls = [pb readObjectsForClasses:@[NSURL.class] options:nil];
		for (NSURL *u in urls) {
			if (!u.isFileURL) {
				d.url = dxpDup(u.absoluteString);
				d.hasURL = true;
				break;
			}
		}
	}
	if ([types containsObject:NSPasteboardTypeString]) {
		d.hasText = true;
		d.text = dxpDup([pb stringForType:NSPasteboardTypeString]);
	}
	if ([types containsObject:NSPasteboardTypeHTML]) {
		d.hasHTML = true;
		d.html = dxpDup([pb stringForType:NSPasteboardTypeHTML]);
	}
	return d;
}

static void dxpFree(dxpDropData *d) {
	free(d->url);
	free(d->text);
	free(d->html);
	free(d->file);
	free(d->webloc);
}

// dxpEditableAt is the editable text field or text view at p (content points, y down).
static NSView *dxpEditableAt(NSView *cv, NSPoint p) {
	NSPoint inSuper = cv.superview ? [cv convertPoint:p toView:cv.superview] : p;
	NSView *v = [cv hitTest:inSuper];
	for (; v && v != cv; v = v.superview) {
		if ([v isKindOfClass:NSTextView.class] && ((NSTextView *)v).isEditable) return v;
		if ([v isKindOfClass:NSTextField.class] && ((NSTextField *)v).isEditable && ((NSTextField *)v).isEnabled) return v;
	}
	return nil;
}

// dxpOperation asks Go about the drag (method 1 entered, 2 updated, 4 perform).
static NSDragOperation dxpAsk(NSView *self, id<NSDraggingInfo> info, int method) {
	uintptr_t h = dxpDropHandleOf(self);
	if (!h) return NSDragOperationNone;
	NSPoint p = [self convertPoint:info.draggingLocation fromView:nil];
	bool copy = (info.draggingSourceOperationMask & NSDragOperationCopy) != 0;
	bool field = dxpEditableAt(self, p) != nil;
	dxpDropData d = {0};
	bool read = method != 2; // entered and perform read the pasteboard; moves use the last
	if (read) d = dxpRead(info.draggingPasteboard);
	int act = goDrop(h, method, p.x, p.y, read ? &d : NULL, field, copy);
	if (read) dxpFree(&d);
	return act ? NSDragOperationCopy : NSDragOperationNone;
}

static NSDragOperation dxpEntered(id self, SEL _cmd, id<NSDraggingInfo> info) { return dxpAsk(self, info, 1); }
static NSDragOperation dxpUpdated(id self, SEL _cmd, id<NSDraggingInfo> info) { return dxpAsk(self, info, 2); }
static void dxpExited(id self, SEL _cmd, id<NSDraggingInfo> info) {
	uintptr_t h = dxpDropHandleOf(self);
	if (h) goDrop(h, 3, 0, 0, NULL, false, false);
}
static BOOL dxpPrepare(id self, SEL _cmd, id<NSDraggingInfo> info) { return YES; }
static BOOL dxpPerform(id self, SEL _cmd, id<NSDraggingInfo> info) {
	return dxpAsk(self, info, 4) != NSDragOperationNone;
}
static BOOL dxpNoPeriodic(id self, SEL _cmd) { return NO; }

// dxpTypes is the type encoding of a method returning ret with the given argument
// encodings after self and _cmd, from @encode: BOOL is "B" on arm64 and "c" on x86_64 (kept
// for the class's life: once per class).
static const char *dxpTypes(const char *ret, const char *args) {
	return strdup([NSString stringWithFormat:@"%s%s%s%s", ret, @encode(id), @encode(SEL), args].UTF8String);
}

static void dxpAddMethods(Class c) {
	static NSMutableSet *done;
	if (!done) done = [NSMutableSet new];
	NSString *name = NSStringFromClass(c);
	if ([done containsObject:name]) return;
	[done addObject:name];
	@autoreleasepool {
		const char *op = @encode(NSDragOperation), *b = @encode(BOOL), *obj = @encode(id);
		class_addMethod(c, @selector(draggingEntered:), (IMP)dxpEntered, dxpTypes(op, obj));
		class_addMethod(c, @selector(draggingUpdated:), (IMP)dxpUpdated, dxpTypes(op, obj));
		class_addMethod(c, @selector(draggingExited:), (IMP)dxpExited, dxpTypes(@encode(void), obj));
		class_addMethod(c, @selector(prepareForDragOperation:), (IMP)dxpPrepare, dxpTypes(b, obj));
		class_addMethod(c, @selector(performDragOperation:), (IMP)dxpPerform, dxpTypes(b, obj));
		class_addMethod(c, @selector(wantsPeriodicDraggingUpdates), (IMP)dxpNoPeriodic, dxpTypes(b, ""));
	}
}

void dxpDropEnable(void *wp, uintptr_t handle) {
	NSWindow *w = (__bridge NSWindow *)wp;
	NSView *cv = w.contentView;
	dxpAddMethods(cv.class);
	objc_setAssociatedObject(cv, &dxpDropKey, @(handle), OBJC_ASSOCIATION_RETAIN_NONATOMIC);
	[cv registerForDraggedTypes:@[NSPasteboardTypeURL, NSPasteboardTypeFileURL, NSPasteboardTypeString,
		NSPasteboardTypeHTML, @"com.apple.pasteboard.promised-file-url",
		(NSPasteboardType)kPasteboardTypeFileURLPromise]];
}

void dxpDropDisable(void *wp) {
	NSWindow *w = (__bridge NSWindow *)wp;
	NSView *cv = w.contentView;
	[cv unregisterDraggedTypes];
	objc_setAssociatedObject(cv, &dxpDropKey, nil, OBJC_ASSOCIATION_RETAIN_NONATOMIC);
}

bool dxpDropFieldAt(void *wp, double x, double y) {
	NSWindow *w = (__bridge NSWindow *)wp;
	return dxpEditableAt(w.contentView, NSMakePoint(x, y)) != nil;
}

bool dxpDropInsert(void *wp, double x, double y, const char *text) {
	NSWindow *w = (__bridge NSWindow *)wp;
	NSView *cv = w.contentView;
	NSView *v = dxpEditableAt(cv, NSMakePoint(x, y));
	if (!v) return false;
	NSString *s = [NSString stringWithUTF8String:text];
	NSTextView *tv = nil;
	if ([v isKindOfClass:NSTextView.class]) {
		tv = (NSTextView *)v;
		if (w.firstResponder != tv) [w makeFirstResponder:tv];
	} else {
		NSTextField *f = (NSTextField *)v;
		if (!f.currentEditor) [w makeFirstResponder:f];
		tv = (NSTextView *)f.currentEditor;
		if (![tv isKindOfClass:NSTextView.class]) return false;
		// a single line takes no line breaks
		if (f.cell.usesSingleLineMode || f.maximumNumberOfLines == 1 || !f.cell.wraps) {
			s = [[s componentsSeparatedByCharactersInSet:NSCharacterSet.newlineCharacterSet] componentsJoinedByString:@""];
		}
	}
	NSPoint inWin = [cv convertPoint:NSMakePoint(x, y) toView:nil];
	NSUInteger i = [tv characterIndexForInsertionAtPoint:[tv convertPoint:inWin fromView:nil]];
	if (i > tv.string.length) i = tv.string.length;
	tv.selectedRange = NSMakeRange(i, 0);
	[tv insertText:s replacementRange:NSMakeRange(i, 0)];
	return true;
}
