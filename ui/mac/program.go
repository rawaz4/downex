//go:build darwin && cgo

package mac

/*
#include <stdlib.h>
#include "prog_darwin.h"
#include "window_darwin.h"
*/
import "C"

import (
	"runtime/cgo"
	"sync"
	"unsafe"

	"downex/ui/kit"
)

// The program part of the backend: what a window process (ui/native on macOS) needs besides
// the windows themselves, as the Windows backend offers it to ui/native/glue: the windows'
// placement and frame, bringing a window to the front, closing without a CloseEvent, the
// application's delegate and menu bar, activation, today's Wails hand-off (distributed
// notifications and the lock folder), drops, and the system dialogs.

// ---- windows ------------------------------------------------------------------------

// progOpened runs in NewWindow before the window shows: the restored size is remembered from
// here on (Placement) and a window asked Zoomed is zoomed.
func (w *Window) progOpened() {
	C.dxgWindowTrack(w.ns)
	if w.opts.Zoomed {
		C.dxgWindowZoom(w.ns)
		w.readSize()
		w.state = w.readState()
		if pw, ph, ppd := w.pixelSize(); w.target != nil {
			_ = w.target.Resize(pw, ph, ppd)
			w.invalidateAll()
			w.tick()
		}
	}
}

// NewMaximizedWindow is NewWindow for a window that opens zoomed (the main window left
// maximised): o.Size is the size it restores to.
func (a *App) NewMaximizedWindow(root kit.Root, o WindowOptions) (*Window, error) {
	o.Zoomed = true
	return a.NewWindow(root, o)
}

// Placement is a window's placement: its restored content size in points and whether it is
// zoomed (maximised) or minimised.
type Placement struct {
	Normal    kit.Size
	Maximized bool
	Minimized bool
}

// Placement reads the window's placement: the content size it has when neither zoomed, full
// screen nor minimised (its size now, or the size it restores to).
func (w *Window) Placement() (Placement, error) {
	if w.closed {
		return Placement{}, kit.ErrClosed
	}
	var cw, ch C.double
	C.dxgWindowNormalSize(w.ns, &cw, &ch)
	st := w.readState()
	return Placement{
		Normal:    kit.Size{W: float32(cw), H: float32(ch)},
		Maximized: st.Maximized || bool(C.dxgWindowFullScreen(w.ns)),
		Minimized: st.Minimized,
	}, nil
}

// FrameSize is what the title bar and the borders add to the content area (0 × 28 points on
// macOS 13-26): the progress window fits its outer height from it.
func (w *Window) FrameSize() kit.Size {
	if w.closed {
		return kit.Size{}
	}
	var fw, fh C.double
	C.dxgWindowFrameSize(w.ns, &fw, &fh)
	return kit.Size{W: float32(fw), H: float32(fh)}
}

// SystemFrameSize is FrameSize of a window with the standard title bar before one exists.
func SystemFrameSize() kit.Size {
	var fw, fh C.double
	C.dxgSystemFrameSize(&fw, &fh)
	return kit.Size{W: float32(fw), H: float32(fh)}
}

// BringToFront shows the window again (unhidden, deminiaturised) in front, and activates the
// application (a later start asked for it); activate false only orders it front.
func (w *Window) BringToFront(activate bool) bool {
	if w.closed {
		return false
	}
	C.dxgWindowFront(w.ns, C.bool(activate))
	return true
}

// CloseNow closes the window at once without a CloseEvent (the end of the session), unless a
// call into its root is in progress (one of its modal dialogs is open): then it reports false
// and the window stays.
func (w *Window) CloseNow() bool {
	if w.closed {
		return true
	}
	if w.depth > 0 {
		return false
	}
	C.dxWindowClose(w.ns)
	return true
}

// Discard closes a window whose opening failed or panicked, whatever calls it counts.
func (w *Window) Discard() {
	if w.closed {
		return
	}
	w.depth, w.pending, w.closing = 0, nil, false
	C.dxWindowClose(w.ns)
}

// Closed reports a closed window.
func (w *Window) Closed() bool { return w.closed }

// WindowNumber is the window's number in the window server (the -report's handle).
func (w *Window) WindowNumber() int64 {
	if w.closed {
		return 0
	}
	return int64(C.dxgWindowNumber(w.ns))
}

// PanicWindow is the window whose root call is in progress (innermost): in OnPanic, the
// window that panicked; nil outside a call.
func (a *App) PanicWindow() *Window {
	if n := len(a.progCalls); n > 0 {
		return a.progCalls[n-1]
	}
	return nil
}

// Windows are the application's open windows.
func (a *App) Windows() []*Window {
	out := make([]*Window, 0, len(a.windows))
	for w := range a.windows {
		out = append(out, w)
	}
	return out
}

// ---- the application ----------------------------------------------------------------

// MenuCommand is a command of the menu bar that Go carries out.
type MenuCommand int

// The commands.
const (
	MenuSettings    MenuCommand = 1 // Settings… (⌘,)
	MenuQuit        MenuCommand = 2 // Quit Downex (⌘Q)
	MenuCloseWindow MenuCommand = 3 // File ▸ Close Window (⌘W)
)

// ProgramOptions are the application's delegate and menu bar.
type ProgramOptions struct {
	// Settings shows Settings… in the app menu (the main window's process).
	Settings bool
	// AboutTitle and AboutMessage are the About dialog's text.
	AboutTitle, AboutMessage string
	// OnMenu carries out a command for the key window (nil when the process has none).
	OnMenu func(c MenuCommand, w *Window)
	// OnTerminate is the system's request to quit (logout, restart, shut down, the Dock's
	// Quit): the windows shut down, then reply(true) lets the termination go on. Nil: it goes
	// on at once.
	OnTerminate func(reply func(ok bool))
}

var program struct {
	opts ProgramOptions
}

// InstallProgram sets the application's delegate and menu bar. Call it once, on the main
// thread, after NewApp and before Run.
func (a *App) InstallProgram(o ProgramOptions) {
	onMain()
	program.opts = o
	t := C.CString(o.AboutTitle)
	m := C.CString(o.AboutMessage)
	C.dxgInstall(C.bool(o.Settings), t, m)
	C.free(unsafe.Pointer(t))
	C.free(unsafe.Pointer(m))
}

//export goProgTerminate
func goProgTerminate() {
	f := program.opts.OnTerminate
	if f == nil {
		C.dxgReplyTerminate(true)
		return
	}
	var once sync.Once
	f(func(ok bool) { once.Do(func() { C.dxgReplyTerminate(C.bool(ok)) }) })
}

//export goProgMenu
func goProgMenu(cmd C.int, h C.uintptr_t) {
	if program.opts.OnMenu == nil {
		return
	}
	w := windowOf(h)
	if w != nil && w.closed {
		w = nil
	}
	program.opts.OnMenu(MenuCommand(cmd), w)
}

// RequestActivation asks for the application's activation (macOS 14+: activate, granted when
// the active app yielded); nothing before 14, where windows activate themselves.
func RequestActivation() { C.dxgRequestActivation() }

// Activate activates the application.
func Activate() { C.dxgActivate() }

// RaiseIfInactive puts the visible windows in front of other apps' windows, without
// activating, when the application is still not active after delay seconds.
func RaiseIfInactive(delay float64) { C.dxgRaiseIfInactive(C.double(delay)) }

// YieldActivation lets the apps of bundles take the focus from this one (macOS 14+). Any
// goroutine; it waits at most 250 ms for the main thread per bundle.
func YieldActivation(bundles ...string) {
	for _, b := range bundles {
		cs := C.CString(b)
		C.dxgYieldActivation(cs)
		C.free(unsafe.Pointer(cs))
	}
}

// SystemDark reports the system's dark appearance (any thread: it asks the main thread).
func SystemDark() bool { return bool(C.dxgSystemDark()) }

// TempDir is NSTemporaryDirectory(): the per-user folder of Wails' single-instance locks.
func TempDir() string {
	p := C.dxgTempDir()
	defer C.free(unsafe.Pointer(p))
	return C.GoString(p)
}

// PreferredLanguage is the user's first preferred language ("en-US"), what WebKit's
// navigator.language reports; "" when none.
func PreferredLanguage() string {
	p := C.dxgPreferredLanguage()
	if p == nil {
		return ""
	}
	defer C.free(unsafe.Pointer(p))
	return C.GoString(p)
}

// ---- distributed notifications ------------------------------------------------------

// PostDistributed posts the distributed notification name with object as its object, as a
// Wails v2.17 second start does to hand its arguments to the running window.
func PostDistributed(name, object string) {
	n, o := C.CString(name), C.CString(object)
	C.dxgPost(n, o)
	C.free(unsafe.Pointer(n))
	C.free(unsafe.Pointer(o))
}

// ObserveDistributed calls f on the main thread with the object of each distributed
// notification name, until cancel (main thread).
func ObserveDistributed(name string, f func(object string)) (cancel func()) {
	onMain()
	h := cgo.NewHandle(f)
	n := C.CString(name)
	obs := C.dxgObserve(n, C.uintptr_t(h))
	C.free(unsafe.Pointer(n))
	done := false
	return func() {
		if done {
			return
		}
		done = true
		C.dxgUnobserve(obs)
		h.Delete()
	}
}

//export goProgDistributed
func goProgDistributed(token C.uintptr_t, object *C.char) {
	f, ok := cgo.Handle(token).Value().(func(string))
	if !ok {
		return
	}
	s := C.GoString(object)
	if current != nil {
		current.safely(func() { f(s) })
		return
	}
	f(s)
}

// ---- system dialogs -----------------------------------------------------------------

// MenuBar lists the menu bar (tests and tools): one line per item, "menu\ttitle\tkey\tmodifier
// mask\taction"; a separator is "menu\t-\t\t0\t".
func MenuBar() string {
	p := C.dxgMenuDump()
	defer C.free(unsafe.Pointer(p))
	return C.GoString(p)
}

// MenuAutoenabling lists the menus of the menu bar whose items AppKit enables and disables
// by itself, one per line ("Edit", "Edit > Speech"); Wails' menus are none of them (tests).
func MenuAutoenabling() string {
	p := C.dxgMenuAutoenabling()
	defer C.free(unsafe.Pointer(p))
	return C.GoString(p)
}

// PerformMenuKey sends key with the modifier mask (NSEventModifierFlags: ⌘ 1<<20, ⇧ 1<<17,
// ⌥ 1<<19, ⌃ 1<<18) to the menu bar and reports whether an item took it (tests).
func PerformMenuKey(key string, mods uint) bool {
	cs := C.CString(key)
	defer C.free(unsafe.Pointer(cs))
	return bool(C.dxgPerformKey(cs, C.ulong(mods)))
}

// PerformMenuItem performs the item titled title of the menu bar's menu (as a click: its
// action runs; a modal one, About, runs inside the call) and reports whether there is such
// an item (tests and tools). Any goroutine but the main thread's while it must go on.
func PerformMenuItem(menu, title string) bool {
	m, t := C.CString(menu), C.CString(title)
	defer C.free(unsafe.Pointer(m))
	defer C.free(unsafe.Pointer(t))
	return bool(C.dxgPerformItem(m, t))
}

// ModalState describes the dialog in front (tests and tools): "" when none, else "modal" or
// "sheet", its class, a panel's title and message, an alert's texts ("text=a|b") and
// buttons with their key equivalents ("buttons=Yes[],No[\r]"). Any goroutine.
func ModalState() string {
	p := C.dxgModalState()
	defer C.free(unsafe.Pointer(p))
	return C.GoString(p)
}

// PostKey puts a key press (its characters, virtual key code and NSEventModifierFlags) in
// the application's event queue for the dialog in front, else the key window, as if typed
// (tests). Any goroutine.
func PostKey(key string, code uint16, mods uint) {
	cs := C.CString(key)
	defer C.free(unsafe.Pointer(cs))
	C.dxgPostKey(cs, C.ushort(code), C.ulong(mods))
}

// PressModalButton clicks the button titled title of the dialog in front (an open panel:
// "Cancel" is its cancel:, any other its ok:) (tests) and
// reports whether there is one. Any goroutine.
func PressModalButton(title string) bool {
	cs := C.CString(title)
	defer C.free(unsafe.Pointer(cs))
	return bool(C.dxgPressModalButton(cs))
}

// AppState reports whether the application is active and whether it is hidden. Any
// goroutine.
func AppState() (active, hidden bool) {
	var a, h C.bool
	C.dxgAppState(&a, &h)
	return bool(a), bool(h)
}

// FrontState reports whether the window is the key window, visible and minimised. Any
// goroutine (while the window is open).
func (w *Window) FrontState() (key, visible, minimized bool) {
	var k, v, m C.bool
	C.dxgWindowState(w.ns, &k, &v, &m)
	return bool(k), bool(v), bool(m)
}

// SendQuitEvent sends this process the quit Apple event, which logout, restart, shut down
// and the Dock's Quit send (tests): the application's delegate decides. Any goroutine.
func SendQuitEvent() bool { return bool(C.dxgSendQuitEvent()) }

// Terminate asks the application to terminate as the system's quit does ([NSApp
// terminate:]): the application's delegate decides (tests). Any goroutine.
func Terminate() { C.dxgTerminate() }

// AlertCritical shows a critical alert (a fatal error); it blocks until it is dismissed.
func AlertCritical(title, message string) {
	t, m := C.CString(title), C.CString(message)
	defer C.free(unsafe.Pointer(t))
	defer C.free(unsafe.Pointer(m))
	C.dxgAlertCritical(t, m)
}
