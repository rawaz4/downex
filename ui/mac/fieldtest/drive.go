//go:build darwin && cgo

package fieldtest

/*
#cgo CFLAGS: -x objective-c -fobjc-arc -mmacosx-version-min=13.0
#cgo LDFLAGS: -framework Cocoa -mmacosx-version-min=13.0
#include <stdlib.h>
#include <stdbool.h>
void dtPump(double seconds);
void *dtWindow(const char *title);
bool dtActivate(void *win);
void dtKey(void *win, int down, unsigned short code, const char *chars, const char *plain, int mods);
char *dtResponder(void *win);
bool dtMarked(void *win, const char *s);
bool dtInsert(void *win, const char *s);
bool dtAction(void *win, const char *sel);
void dtEditMenu(void);
void dtNoMenu(void);
bool dtSnapshot(void *win, const char *path);
void dtClick(void *win, double x, double y);
char *dtEditorInfo(void *win);
void dtMove(void *win, double x, double y);
void dtPrivatePasteboard(void);
bool dtRealPasteboardChanged(void);
long dtBoardCount(void);
char *dtBoardString(void);
void dtSetBoardString(const char *s);
void dtReleaseBoard(void);
char *dtFieldViews(void *win);
void dtMoveTwice(void *win, double x, double y);
char *dtDuplicateSelectors(const char *cls);
*/
import "C"

import (
	"unsafe"
)

// Window is a window found by its title.
type Window struct{ p unsafe.Pointer }

// Find finds the window titled title (nil when there is none).
func Find(title string) *Window {
	cs := C.CString(title)
	defer C.free(unsafe.Pointer(cs))
	p := C.dtWindow(cs)
	if p == nil {
		return nil
	}
	return &Window{p}
}

// Pump runs the event loop for seconds.
func Pump(seconds float64) { C.dtPump(C.double(seconds)) }

// Activate makes the application active and the window key; it reports whether it is key.
func (w *Window) Activate() bool { return bool(C.dtActivate(w.p)) }

// Key posts a key down or up (Carbon key code, the characters it types and those without
// modifiers, the kit's modifier bits) and runs the loop until it is handled.
func (w *Window) Key(down bool, code uint16, chars, plain string, mods int) {
	c, p := C.CString(chars), C.CString(plain)
	defer C.free(unsafe.Pointer(c))
	defer C.free(unsafe.Pointer(p))
	d := C.int(0)
	if down {
		d = 1
	}
	C.dtKey(w.p, d, C.ushort(code), c, p, C.int(mods))
}

// Press is a key down then up.
func (w *Window) Press(code uint16, chars, plain string, mods int) {
	w.Key(true, code, chars, plain, mods)
	w.Key(false, code, chars, plain, mods)
}

// Type types s, one key per character (the key code of an A, the character itself).
func (w *Window) Type(s string) {
	for _, r := range s {
		w.Press(0, string(r), string(r), 0)
	}
}

// Responder is the class name of the window's first responder.
func (w *Window) Responder() string {
	p := C.dtResponder(w.p)
	defer C.free(unsafe.Pointer(p))
	return C.GoString(p)
}

// SetMarked has the first responder compose s (an input method's marked text); Insert
// commits s. They report whether the first responder takes text.
func (w *Window) SetMarked(s string) bool {
	cs := C.CString(s)
	defer C.free(unsafe.Pointer(cs))
	return bool(C.dtMarked(w.p, cs))
}

// Insert commits s as an input method does.
func (w *Window) Insert(s string) bool {
	cs := C.CString(s)
	defer C.free(unsafe.Pointer(cs))
	return bool(C.dtInsert(w.p, cs))
}

// Action sends an action (an Edit menu item's: "copy:", "paste:", "undo:") to the
// window's first responder chain.
func (w *Window) Action(sel string) bool {
	cs := C.CString(sel)
	defer C.free(unsafe.Pointer(cs))
	return bool(C.dtAction(w.p, cs))
}

// EditMenu installs a main menu with today's Edit menu (as Wails' menu.EditMenu has it).
func EditMenu() { C.dtEditMenu() }

// NoMenu removes the main menu.
func NoMenu() { C.dtNoMenu() }

// Snapshot writes the window's content, native views included, as PNG at its backing
// scale.
func (w *Window) Snapshot(path string) bool {
	cs := C.CString(path)
	defer C.free(unsafe.Pointer(cs))
	return bool(C.dtSnapshot(w.p, cs))
}

// Click clicks at (x, y), content points (y down).
func (w *Window) Click(x, y float64) { C.dtClick(w.p, C.double(x), C.double(y)) }

// Move posts a pointer move to (x, y), content points (y down).
func (w *Window) Move(x, y float64) { C.dtMove(w.p, C.double(x), C.double(y)) }

// EditorInfo describes the first responder text view's settings.
func (w *Window) EditorInfo() string {
	p := C.dtEditorInfo(w.p)
	defer C.free(unsafe.Pointer(p))
	return C.GoString(p)
}

// PrivatePasteboard makes the general pasteboard a private one for this process: the
// fields' Cut, Copy and Paste in the tests never touch the user's clipboard.
func PrivatePasteboard() { C.dtPrivatePasteboard() }

// RealPasteboardChanged reports whether the user's pasteboard changed since
// PrivatePasteboard (it is only read; another program may have written it).
func RealPasteboardChanged() bool { return bool(C.dtRealPasteboardChanged()) }

// ReleasePasteboard gives the private pasteboard back.
func ReleasePasteboard() { C.dtReleaseBoard() }

// BoardCount is the private pasteboard's change count.
func BoardCount() int { return int(C.dtBoardCount()) }

// BoardString is the private pasteboard's text.
func BoardString() string {
	p := C.dtBoardString()
	defer C.free(unsafe.Pointer(p))
	return C.GoString(p)
}

// SetBoardString puts s on the private pasteboard.
func SetBoardString(s string) {
	cs := C.CString(s)
	defer C.free(unsafe.Pointer(cs))
	C.dtSetBoardString(cs)
}

// FieldViews describes the content view's subviews: class, hidden and whether it shows.
func (w *Window) FieldViews() string {
	p := C.dtFieldViews(w.p)
	defer C.free(unsafe.Pointer(p))
	return C.GoString(p)
}

// MoveTwice delivers one pointer move to (x, y) by both of AppKit's paths: the root
// view's tracking area and the first responder.
func (w *Window) MoveTwice(x, y float64) { C.dtMoveTwice(w.p, C.double(x), C.double(y)) }

// DuplicateSelectors lists the selectors class cls implements more than once (its own and
// categories'), comma-separated.
func DuplicateSelectors(cls string) string {
	cs := C.CString(cls)
	defer C.free(unsafe.Pointer(cs))
	p := C.dtDuplicateSelectors(cs)
	defer C.free(unsafe.Pointer(p))
	return C.GoString(p)
}
