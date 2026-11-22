//go:build darwin && cgo

package popuptest

/*
#cgo CFLAGS: -x objective-c -fobjc-arc -mmacosx-version-min=13.0
#cgo LDFLAGS: -framework Cocoa -mmacosx-version-min=13.0
#include <stdlib.h>
#include <stdbool.h>
void dptPump(double seconds);
void dptPostMouse(void *win, int type, double x, double y, int clicks);
void dptPostScroll(void *win, double x, double y, int lines, int phase);
void dptPostKey(void *win, int keyCode, bool down, const char *chars);
void dptMakeKey(void *win);
bool dptIsKey(void *win);
void dptMoveWindow(void *win, double dx);
void dptFrame(void *win, double *x, double *y, double *w, double *h);
bool dptIgnoresMouse(void *win);
void dptContentPoint(void *win, double sx, double sy, double *x, double *y);
bool dptTooltipShown(void);
bool dptActive(void);
void dptWarp(void *win, double x, double y);
int dptDrag(void *win, int method, double x, double y, const char *url, const char *text, const char *html,
	const char *files, bool copyAllowed);
void *dptAddField(void *win, double x, double y, double w, double h, const char *text);
char *dptFieldText(void *field);
void dptRemoveField(void *field);
void dptAnswerDialog(int answer, double delay);
char *dptLastDialog(void);
bool dptSheetOn(void *win);
void *dptHoldContentView(void *win);
void dptPokeView(void *view);
void dptReleaseObject(void *p);
void dptSetIgnoresMouse(void *win, bool on);
int dptTabbedWindows(void *win);
bool dptVisible(void *win);
int dptVisibleTitled(const char *title);
void dptToggleFullScreen(void *win);
bool dptPerform(void *win, const char *sel);
bool dptFullScreen(void *win);
bool dptAcceptsFirstMouse(void *win);
void dptScreenContentMax(double *w, double *h);
void dptRunAlert(double abortAfter);
long dptAlertDone(void);
void dptScrollView(void *win, double x, double y, int lines);
*/
import "C"

import (
	"time"
	"unsafe"
)

// NSEventType values of the mouse events PostMouse posts.
const (
	LeftDown  = 1
	LeftUp    = 2
	RightDown = 3
	RightUp   = 4
	Moved     = 5
)

// Scroll phases (CGScrollPhase) of a trackpad's wheel events.
const (
	PhaseNone      = 0
	PhaseBegan     = 1
	PhaseChanged   = 2
	PhaseEnded     = 4
	PhaseCancelled = 8
	PhaseMayBegin  = 128
)

func cstr(s *string) *C.char {
	if s == nil {
		return nil
	}
	return C.CString(*s)
}

func free(ps ...*C.char) {
	for _, p := range ps {
		if p != nil {
			C.free(unsafe.Pointer(p))
		}
	}
}

// Pump handles the application's events for d (the main thread's).
func Pump(d time.Duration) { C.dptPump(C.double(d.Seconds())) }

// PostMouse posts a mouse event of type typ at (x, y) of win's content (points, y down), as
// the pointer makes it.
func PostMouse(win unsafe.Pointer, typ int, x, y float64, clicks int) {
	C.dptPostMouse(win, C.int(typ), C.double(x), C.double(y), C.int(clicks))
}

// PostScroll posts a wheel event over (x, y) of win's content: lines turns of a mouse wheel
// (phase PhaseNone), or with lines 0 and a phase, a trackpad event that scrolls nothing.
func PostScroll(win unsafe.Pointer, x, y float64, lines, phase int) {
	C.dptPostScroll(win, C.double(x), C.double(y), C.int(lines), C.int(phase))
}

// PostKey posts a key (a virtual key code) with its characters to win.
func PostKey(win unsafe.Pointer, code int, down bool, chars string) {
	cs := C.CString(chars)
	defer free(cs)
	C.dptPostKey(win, C.int(code), C.bool(down), cs)
}

// MakeKey activates the application and makes win the key window (macOS may refuse to
// activate a process started from a terminal).
func MakeKey(win unsafe.Pointer) { C.dptMakeKey(win) }

// IsKey reports whether win is the key window.
func IsKey(win unsafe.Pointer) bool { return bool(C.dptIsKey(win)) }

// Active reports whether the application is active.
func Active() bool { return bool(C.dptActive()) }

// MoveWindow moves win dx points to the right.
func MoveWindow(win unsafe.Pointer, dx float64) { C.dptMoveWindow(win, C.double(dx)) }

// Frame is win's frame on the screen (points, y up).
func Frame(win unsafe.Pointer) (x, y, w, h float64) {
	var cx, cy, cw, ch C.double
	C.dptFrame(win, &cx, &cy, &cw, &ch)
	return float64(cx), float64(cy), float64(cw), float64(ch)
}

// IgnoresMouse reports whether win lets the pointer through.
func IgnoresMouse(win unsafe.Pointer) bool { return bool(C.dptIgnoresMouse(win)) }

// ContentPoint converts a screen point (points, y up) to win's content points (y down).
func ContentPoint(win unsafe.Pointer, sx, sy float64) (x, y float64) {
	var cx, cy C.double
	C.dptContentPoint(win, C.double(sx), C.double(sy), &cx, &cy)
	return float64(cx), float64(cy)
}

// TooltipShown reports whether AppKit shows a tooltip window now.
func TooltipShown() bool { return bool(C.dptTooltipShown()) }

// Warp moves the real pointer to (x, y) of win's content: AppKit's tooltip manager follows
// the real pointer only. Only the tests that say so in their environment use it.
func Warp(win unsafe.Pointer, x, y float64) { C.dptWarp(win, C.double(x), C.double(y)) }

// Drag calls win's content view's dragging method (1 entered, 2 updated, 3 exited, 4
// perform) with a made-up drag of a private pasteboard holding those strings (nil: not
// offered) and file paths (newline separated); it returns the operation (0 none).
func Drag(win unsafe.Pointer, method int, x, y float64, url, text, html *string, files string, copyOK bool) int {
	u, tx, h := cstr(url), cstr(text), cstr(html)
	var f *C.char
	if files != "" {
		f = C.CString(files)
	}
	defer free(u, tx, h, f)
	return int(C.dptDrag(win, C.int(method), C.double(x), C.double(y), u, tx, h, f, C.bool(copyOK)))
}

// Field is a plain NSTextField put on a window's content.
type Field struct{ p unsafe.Pointer }

// AddField puts an editable NSTextField at (x, y, w, h) of win's content.
func AddField(win unsafe.Pointer, x, y, w, h float64, text string) Field {
	cs := C.CString(text)
	defer free(cs)
	return Field{C.dptAddField(win, C.double(x), C.double(y), C.double(w), C.double(h), cs)}
}

// Text is the field's text.
func (f Field) Text() string {
	p := C.dptFieldText(f.p)
	defer C.free(unsafe.Pointer(p))
	return C.GoString(p)
}

// Remove takes the field away.
func (f Field) Remove() { C.dptRemoveField(f.p) }

// AnswerDialog answers the next alert or open panel once it is up, delay after now: answer 1
// is the alert's "Yes" or the panel's OK, 2 its "No" or Cancel, 0 records it and leaves it
// open. LastDialog then describes it.
func AnswerDialog(answer int, delay time.Duration) {
	C.dptAnswerDialog(C.int(answer), C.double(delay.Seconds()))
}

// LastDialog describes the last dialog AnswerDialog found.
func LastDialog() string {
	p := C.dptLastDialog()
	defer C.free(unsafe.Pointer(p))
	return C.GoString(p)
}

// SheetOn reports whether a sheet is attached to win.
func SheetOn(win unsafe.Pointer) bool { return bool(C.dptSheetOn(win)) }

// HoldContentView keeps win's content view alive (as AppKit may, with an event it still
// has for it) until ReleaseObject.
func HoldContentView(win unsafe.Pointer) unsafe.Pointer { return C.dptHoldContentView(win) }

// PokeView gives view a mouse move, a mouse exit and an accessibility request.
func PokeView(view unsafe.Pointer) { C.dptPokeView(view) }

// ReleaseObject releases what HoldContentView kept.
func ReleaseObject(p unsafe.Pointer) { C.dptReleaseObject(p) }

// SetIgnoresMouse sets whether win lets the pointer through.
func SetIgnoresMouse(win unsafe.Pointer, on bool) { C.dptSetIgnoresMouse(win, C.bool(on)) }

// TabbedWindows is how many windows win is a tab of (0: not in a tab group).
func TabbedWindows(win unsafe.Pointer) int { return int(C.dptTabbedWindows(win)) }

// Visible reports win on screen (ordered in).
func Visible(win unsafe.Pointer) bool { return bool(C.dptVisible(win)) }

// VisibleTitled counts the application's visible windows titled title.
func VisibleTitled(title string) int {
	cs := C.CString(title)
	defer C.free(unsafe.Pointer(cs))
	return int(C.dptVisibleTitled(cs))
}

// Perform sends the action sel (a menu item's, nil-targeted) to win's first responder; false
// when nothing in the chain answers it.
func Perform(win unsafe.Pointer, sel string) bool {
	c := C.CString(sel)
	defer C.free(unsafe.Pointer(c))
	return bool(C.dptPerform(win, c))
}

// ToggleFullScreen puts win in or out of full screen (animated).
func ToggleFullScreen(win unsafe.Pointer) { C.dptToggleFullScreen(win) }

// FullScreen reports win in full screen.
func FullScreen(win unsafe.Pointer) bool { return bool(C.dptFullScreen(win)) }

// AcceptsFirstMouse reports whether win's content view takes the click that brings win
// forward from another app (acceptsFirstMouse:).
func AcceptsFirstMouse(win unsafe.Pointer) bool { return bool(C.dptAcceptsFirstMouse(win)) }

// ScreenContentMax is the largest content area of a standard window on the main screen.
func ScreenContentMax() (w, h float64) {
	var cw, ch C.double
	C.dptScreenContentMax(&cw, &ch)
	return float64(cw), float64(ch)
}

// RunAlert runs an NSAlert's runModal from a run-loop block soon, aborting it after
// abortAfter; AlertDone is its response then (0 while it runs: NSModalResponseAbort -1001,
// NSModalResponseStop -1000).
func RunAlert(abortAfter time.Duration) { C.dptRunAlert(C.double(abortAfter.Seconds())) }

// AlertDone is RunAlert's response, 0 while it has none.
func AlertDone() int { return int(C.dptAlertDone()) }

// ScrollView hands lines notches of a mouse wheel at (x, y) of win's content (points, y
// down) straight to its content view (positive: up, as CGEvent's wheel).
func ScrollView(win unsafe.Pointer, x, y float64, lines int) {
	C.dptScrollView(win, C.double(x), C.double(y), C.int(lines))
}
