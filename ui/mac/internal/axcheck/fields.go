//go:build darwin && cgo

// Package axcheck runs the native window's accessibility (ui/mac's [ax] part) in real
// AppKit windows on the main thread, read through the in-process client of ui/mac
// (AXElement) as the system's accessibility server reads it for VoiceOver. It holds only
// tests and their helpers.
package axcheck

/*
#cgo CFLAGS: -x objective-c -fobjc-arc -mmacosx-version-min=13.0
#cgo LDFLAGS: -framework Cocoa -mmacosx-version-min=13.0
#import <Cocoa/Cocoa.h>

// addField puts an NSTextField into win's content view at a screen rectangle (y up), as
// the fields part places a native field in its node's box.
static void *addField(void *win, double x, double y, double w, double h) {
	NSWindow *wn = (__bridge NSWindow *)win;
	NSView *v = wn.contentView;
	NSRect r = [v convertRect:[wn convertRectFromScreen:NSMakeRect(x, y, w, h)] fromView:nil];
	NSTextField *f = [[NSTextField alloc] initWithFrame:NSInsetRect(r, 2, 2)];
	[v addSubview:f];
	return (__bridge void *)f;
}

// addTextArea puts a multi-line field into win's content view at a screen rectangle (y
// up), built as the fields part builds one: an NSScrollView holding an NSTextView.
static void *addTextArea(void *win, double x, double y, double w, double h) {
	NSWindow *wn = (__bridge NSWindow *)win;
	NSView *v = wn.contentView;
	NSRect r = NSInsetRect([v convertRect:[wn convertRectFromScreen:NSMakeRect(x, y, w, h)] fromView:nil], 2, 2);
	NSScrollView *sv = [[NSScrollView alloc] initWithFrame:r];
	NSTextView *tv = [[NSTextView alloc] initWithFrame:NSMakeRect(0, 0, r.size.width, r.size.height)];
	tv.string = @"hello";
	sv.documentView = tv;
	sv.hasVerticalScroller = YES;
	[v addSubview:sv];
	return (__bridge void *)sv;
}

static void *scrollDocument(void *sv) { return (__bridge void *)((__bridge NSScrollView *)sv).documentView; }

static void setHidden(void *f, bool h) { ((__bridge NSView *)f).hidden = h; }
static void removeField(void *f) { [(__bridge NSView *)f removeFromSuperview]; }
*/
import "C"

import (
	"unsafe"

	"downex/ui/kit"
)

// addField places a native text field at r (screen, y up) in the window win (an NSWindow).
func addField(win unsafe.Pointer, r kit.Rect) unsafe.Pointer {
	return C.addField(win, C.double(r.Min.X), C.double(r.Min.Y), C.double(r.Width()), C.double(r.Height()))
}

// addTextArea places a multi-line field (an NSScrollView of an NSTextView) at r (screen,
// y up) in the window win; documentOf is its text view.
func addTextArea(win unsafe.Pointer, r kit.Rect) unsafe.Pointer {
	return C.addTextArea(win, C.double(r.Min.X), C.double(r.Min.Y), C.double(r.Width()), C.double(r.Height()))
}

func documentOf(sv unsafe.Pointer) unsafe.Pointer { return C.scrollDocument(sv) }

func hideField(f unsafe.Pointer, h bool) { C.setHidden(f, C.bool(h)) }
func removeField(f unsafe.Pointer)       { C.removeField(f) }

// unsafeField is a native field a test placed.
type unsafeField struct{ p unsafe.Pointer }
