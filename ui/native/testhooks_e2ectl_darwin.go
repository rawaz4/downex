//go:build darwin && cgo && e2etest

package main

// The remote control of an e2etest build (never shipped): with DOWNEX_E2E_UI_DIR set, each
// window process listens on <dir>/ctl/<pid>.sock for one command per connection (a line of
// tab-separated words) and answers with text, so that an end-to-end run can drive the real
// windows on a Mac without the Accessibility permission (synthesized events go to the
// process's own windows, as ui/mac's test drivers post them):
//
//	wins                         every window: number, title, frame, key/visible/minimised
//	shot   <n> <png>             what window n shows, its native fields included
//	kshot  <n> <png>             what the kit drew in window n (Window.WritePNG)
//	click  <n> <x> <y> [count] [right]   a click at (x, y) points from the content's top left
//	move   <n> <x> <y>           the pointer moves there
//	key    <n> <code> <chars> [mods]     a key press (mods: s shift, c control, o option, m command)
//	type   <n> <text>            text typed into the first responder
//	menu   <menu> <title>        a menu bar item, as a click
//	menukey <key> [mods]         a key equivalent to the menu bar
//	ax     <n> [depth]           window n's accessibility tree
//	axpress <n> <label>          AXPress on the first element whose title or description is label
//	modal                        the dialog in front (mac.ModalState)
//	press  <title>               a button of the dialog in front
//	resize <n> <w> <h>           window n's content to w×h points, its top kept
//	close  <n>                   window n's close button
//	front  <n>                   window n to the front and key (as a click in it would)
//	quit                         [NSApp terminate:], as the system's quit

/*
#cgo CFLAGS: -x objective-c -fobjc-arc -mmacosx-version-min=13.0
#cgo LDFLAGS: -framework Cocoa -framework QuartzCore -mmacosx-version-min=13.0
#include <stdlib.h>
#include <stdbool.h>
#import <Cocoa/Cocoa.h>
#import <QuartzCore/QuartzCore.h>
#include <pthread.h>

// e2eMain runs block on the main thread and waits (a CFRunLoop block in the common modes:
// it also runs inside a nested loop, which a main-queue block does not).
static void e2eMain(void (^block)(void)) {
	if (pthread_main_np()) { block(); return; }
	dispatch_semaphore_t done = dispatch_semaphore_create(0);
	CFRunLoopPerformBlock(CFRunLoopGetMain(), kCFRunLoopCommonModes, ^{ block(); dispatch_semaphore_signal(done); });
	CFRunLoopWakeUp(CFRunLoopGetMain());
	dispatch_semaphore_wait(done, DISPATCH_TIME_FOREVER);
}

static char *e2eWins(void) {
	__block NSMutableString *s = [NSMutableString string];
	e2eMain(^{
		for (NSWindow *w in NSApp.orderedWindows) {
			NSRect f = w.frame, c = [w contentRectForFrameRect:f];
			[s appendFormat:@"%ld\t%@\t%@\tframe=%gx%g@%g,%g\tcontent=%gx%g\tkey=%d visible=%d mini=%d main=%d\n", (long)w.windowNumber,
				NSStringFromClass(w.class), w.title, f.size.width, f.size.height, f.origin.x, f.origin.y,
				c.size.width, c.size.height, w.isKeyWindow, w.isVisible, w.isMiniaturized, w.isMainWindow];
		}
		[s appendFormat:@"app active=%d hidden=%d policy=%ld\n", NSApp.active, NSApp.hidden, (long)NSApp.activationPolicy];
	});
	return strdup(s.UTF8String);
}

static NSWindow *e2eWin(long num) { return [NSApp windowWithWindowNumber:num]; }

// e2ePoint is (x, y) from the content's top left in the window's coordinates.
static NSPoint e2ePoint(NSWindow *w, double x, double y) {
	NSView *v = w.contentView;
	if (!v.isFlipped) y = v.bounds.size.height - y;
	return [v convertPoint:NSMakePoint(x, y) toView:nil];
}

static bool e2eShot(long num, const char *path) {
	NSString *p = [NSString stringWithUTF8String:path];
	__block bool ok = false;
	e2eMain(^{
		NSWindow *w = e2eWin(num);
		if (!w) return;
		NSView *v = w.contentView;
		[v displayIfNeeded];
		for (NSView *s in v.subviews) [s displayIfNeeded];
		[CATransaction flush];
		double scale = w.backingScaleFactor;
		size_t pw = (size_t)ceil(v.bounds.size.width * scale), ph = (size_t)ceil(v.bounds.size.height * scale);
		CGColorSpaceRef cs = CGColorSpaceCreateWithName(kCGColorSpaceSRGB);
		CGContextRef c = CGBitmapContextCreate(NULL, pw, ph, 8, 0, cs, (CGBitmapInfo)kCGImageAlphaPremultipliedLast);
		CGColorSpaceRelease(cs);
		if (!c) return;
		CGContextTranslateCTM(c, 0, ph);
		CGContextScaleCTM(c, scale, -scale);
		[v.layer renderInContext:c];
		CGImageRef img = CGBitmapContextCreateImage(c);
		CGContextRelease(c);
		NSBitmapImageRep *rep = [[NSBitmapImageRep alloc] initWithCGImage:img];
		CGImageRelease(img);
		NSData *png = [rep representationUsingType:NSBitmapImageFileTypePNG properties:@{}];
		ok = [png writeToFile:p atomically:YES];
	});
	return ok;
}

static bool e2eClick(long num, double x, double y, int count, bool right) {
	__block bool ok = false;
	e2eMain(^{
		NSWindow *w = e2eWin(num);
		if (!w) return;
		NSPoint p = e2ePoint(w, x, y);
		for (int n = 1; n <= count; n++) {
			for (int i = 0; i < 2; i++) {
				NSEventType t = right ? (i == 0 ? NSEventTypeRightMouseDown : NSEventTypeRightMouseUp)
				                      : (i == 0 ? NSEventTypeLeftMouseDown : NSEventTypeLeftMouseUp);
				NSEvent *e = [NSEvent mouseEventWithType:t location:p modifierFlags:0
					timestamp:NSProcessInfo.processInfo.systemUptime windowNumber:w.windowNumber context:nil
					eventNumber:0 clickCount:n pressure:i == 0 ? 1 : 0];
				[NSApp postEvent:e atStart:NO];
			}
		}
		ok = true;
	});
	return ok;
}

static bool e2eMove(long num, double x, double y) {
	__block bool ok = false;
	e2eMain(^{
		NSWindow *w = e2eWin(num);
		if (!w) return;
		NSEvent *e = [NSEvent mouseEventWithType:NSEventTypeMouseMoved location:e2ePoint(w, x, y) modifierFlags:0
			timestamp:NSProcessInfo.processInfo.systemUptime windowNumber:w.windowNumber context:nil
			eventNumber:0 clickCount:0 pressure:0];
		[NSApp postEvent:e atStart:NO];
		ok = true;
	});
	return ok;
}

static bool e2eKey(long num, unsigned short code, const char *chars, unsigned long mods) {
	NSString *k = [NSString stringWithUTF8String:chars];
	__block bool ok = false;
	e2eMain(^{
		NSWindow *w = e2eWin(num);
		if (!w) return;
		const NSEventType types[2] = {NSEventTypeKeyDown, NSEventTypeKeyUp};
		for (int i = 0; i < 2; i++) {
			NSEvent *e = [NSEvent keyEventWithType:types[i] location:NSZeroPoint modifierFlags:mods
				timestamp:NSProcessInfo.processInfo.systemUptime windowNumber:w.windowNumber context:nil
				characters:k charactersIgnoringModifiers:k isARepeat:NO keyCode:code];
			// A key window: through the application's queue, as a typed key goes (local
			// monitors, the menu's key equivalents, the window).
			if (w.isKeyWindow && NSApp.active) { [NSApp postEvent:e atStart:NO]; continue; }
			// Else to the window itself, as the window server routes keys to a key window: an
			// inactive app has no key window for NSApp to route them to.
			if (types[i] == NSEventTypeKeyDown && (mods & NSEventModifierFlagCommand) && [NSApp.mainMenu performKeyEquivalent:e]) continue;
			if (types[i] == NSEventTypeKeyDown && [w performKeyEquivalent:e]) continue;
			[w sendEvent:e];
		}
		ok = true;
	});
	return ok;
}

static bool e2eType(long num, const char *text) {
	NSString *s = [NSString stringWithUTF8String:text];
	__block bool ok = false;
	e2eMain(^{
		NSWindow *w = e2eWin(num);
		if (!w) return;
		NSResponder *r = w.firstResponder;
		if (![r conformsToProtocol:@protocol(NSTextInputClient)]) return;
		[(id<NSTextInputClient>)r insertText:s replacementRange:NSMakeRange(NSNotFound, 0)];
		ok = true;
	});
	return ok;
}

static bool e2eResize(long num, double w, double h) {
	__block bool ok = false;
	e2eMain(^{
		NSWindow *win = e2eWin(num);
		if (!win) return;
		// As a drag of the window's edge ends: the content size changes, the top stays.
		NSRect f = win.frame, c = [win contentRectForFrameRect:f];
		NSRect n = [win frameRectForContentRect:NSMakeRect(c.origin.x, c.origin.y + c.size.height - h, w, h)];
		[win setFrame:n display:YES];
		ok = true;
	});
	return ok;
}

static bool e2eClose(long num) {
	__block bool ok = false;
	e2eMain(^{
		NSWindow *w = e2eWin(num);
		if (!w) return;
		[w performClose:nil]; // the close button
		ok = true;
	});
	return ok;
}

static bool e2eFront(long num) {
	__block bool ok = false;
	e2eMain(^{
		NSWindow *w = e2eWin(num);
		if (!w) return;
		[NSApp activateIgnoringOtherApps:YES];
		[w makeKeyAndOrderFront:nil];
		ok = w.isKeyWindow;
	});
	return ok;
}
*/
import "C"

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"downex/ui/mac"
)

// e2eListen starts the remote control of app a (once per process).
func e2eListen(a *mac.App) {
	d := os.Getenv(envE2EUIDir)
	if d == "" || e2eStarted {
		return
	}
	e2eStarted = true
	dir := filepath.Join(d, "ctl")
	_ = os.MkdirAll(dir, 0o700)
	path := filepath.Join(dir, strconv.Itoa(os.Getpid())+".sock")
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e control:", err)
		return
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				line, _ := bufio.NewReader(c).ReadString('\n')
				out := e2eDo(a, strings.Split(strings.TrimRight(line, "\r\n"), "\t"))
				_, _ = c.Write([]byte(out + "\n"))
			}()
		}
	}()
}

var e2eStarted bool

// e2eWindow is window number n of a.
func e2eWindow(a *mac.App, n int64) *mac.Window {
	for _, w := range a.Windows() {
		if w.WindowNumber() == n {
			return w
		}
	}
	return nil
}

// onWindow runs f on w's thread and waits (5 s at most).
func onWindow(w *mac.Window, f func() string) string {
	done := make(chan string, 1)
	if err := w.Post(func() { done <- f() }); err != nil {
		return "error: " + err.Error()
	}
	select {
	case s := <-done:
		return s
	case <-time.After(5 * time.Second):
		return "error: timeout"
	}
}

func e2eDo(a *mac.App, f []string) string {
	arg := func(i int) string {
		if i < len(f) {
			return f[i]
		}
		return ""
	}
	num := func(i int) float64 { v, _ := strconv.ParseFloat(arg(i), 64); return v }
	cs := func(s string) *C.char { return C.CString(s) }
	ok := func(b bool) string {
		if b {
			return "ok"
		}
		return "error: failed"
	}
	switch arg(0) {
	case "wins":
		p := C.e2eWins()
		defer C.free(unsafe.Pointer(p))
		return C.GoString(p)
	case "shot":
		p := cs(arg(2))
		defer C.free(unsafe.Pointer(p))
		return ok(bool(C.e2eShot(C.long(num(1)), p)))
	case "kshot":
		w := e2eWindow(a, int64(num(1)))
		if w == nil {
			return "error: no such window"
		}
		return onWindow(w, func() string {
			if err := w.WritePNG(arg(2)); err != nil {
				return "error: " + err.Error()
			}
			return "ok"
		})
	case "click":
		count := 1
		if c := arg(4); c != "" {
			count, _ = strconv.Atoi(c)
		}
		return ok(bool(C.e2eClick(C.long(num(1)), C.double(num(2)), C.double(num(3)), C.int(count), arg(5) == "right")))
	case "move":
		return ok(bool(C.e2eMove(C.long(num(1)), C.double(num(2)), C.double(num(3)))))
	case "key":
		code, _ := strconv.Atoi(arg(2))
		p := cs(unescape(arg(3)))
		defer C.free(unsafe.Pointer(p))
		return ok(bool(C.e2eKey(C.long(num(1)), C.ushort(code), p, C.ulong(mods(arg(4))))))
	case "type":
		p := cs(arg(2))
		defer C.free(unsafe.Pointer(p))
		return ok(bool(C.e2eType(C.long(num(1)), p)))
	case "resize":
		return ok(bool(C.e2eResize(C.long(num(1)), C.double(num(2)), C.double(num(3)))))
	case "close":
		return ok(bool(C.e2eClose(C.long(num(1)))))
	case "front":
		return ok(bool(C.e2eFront(C.long(num(1)))))
	case "menu":
		return ok(mac.PerformMenuItem(arg(1), arg(2)))
	case "menukey":
		ws := a.Windows()
		if len(ws) == 0 {
			return "error: no window"
		}
		// The menu belongs to the main thread.
		return onWindow(ws[0], func() string { return ok(mac.PerformMenuKey(arg(1), mods(arg(2)))) })
	case "ax", "axpress":
		w := e2eWindow(a, int64(num(1)))
		if w == nil {
			return "error: no such window"
		}
		return onWindow(w, func() string {
			root := w.AXWindow()
			if root == nil {
				return "error: closed"
			}
			defer root.Release()
			if arg(0) == "ax" {
				depth := 12
				if d := arg(2); d != "" {
					depth, _ = strconv.Atoi(d)
				}
				return mac.AXDump(root, depth)
			}
			e := axFind(root, arg(2))
			if e == nil {
				return "error: not found"
			}
			defer e.Release()
			e.Perform("AXPress")
			return "ok"
		})
	case "modal":
		return mac.ModalState()
	case "press":
		return ok(mac.PressModalButton(arg(1)))
	case "quit":
		go mac.Terminate()
		return "ok"
	}
	return "error: unknown command"
}

// axFind is the first element under e (depth first) whose title or description is label.
func axFind(e *mac.AXElement, label string) *mac.AXElement {
	for _, c := range e.Children() {
		if c.String("AXTitle") == label || c.String("AXDescription") == label {
			return c
		}
		if f := axFind(c, label); f != nil {
			c.Release()
			return f
		}
		c.Release()
	}
	return nil
}

// mods is NSEventModifierFlags of "smoc" letters.
func mods(s string) uint {
	var m uint
	for _, r := range s {
		switch r {
		case 's':
			m |= 1 << 17
		case 'c':
			m |= 1 << 18
		case 'o':
			m |= 1 << 19
		case 'm':
			m |= 1 << 20
		}
	}
	return m
}

// unescape reads \r, \t, \e (Escape), \b (Backspace) and \uXXXX in a key's characters.
func unescape(s string) string {
	r := strings.NewReplacer(`\r`, "\r", `\t`, "\t", `\e`, "\x1b", `\b`, "\x7f")
	s = r.Replace(s)
	if strings.HasPrefix(s, `\u`) {
		if v, err := strconv.ParseUint(s[2:], 16, 32); err == nil {
			return string(rune(v))
		}
	}
	return s
}
