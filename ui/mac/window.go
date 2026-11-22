//go:build darwin && cgo

package mac

/*
#include <stdlib.h>
#include <CoreGraphics/CoreGraphics.h>
#include "window_darwin.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"math"
	"runtime/cgo"
	"sync"
	"time"
	"unsafe"

	"downex/ui/kit"
)

// WindowOptions place a new window.
type WindowOptions struct {
	Title   string
	Size    kit.Size // client size in DIPs (points)
	MinSize kit.Size
	// NoActivate shows the window without making the application active (a progress
	// window the daemon opens by itself).
	NoActivate bool
	// AcceptsFirstMouse lets the click that brings the window forward while another app is
	// active also press what it lands on (the Confirm download and progress windows, which
	// open over the browser the user is in); otherwise, as for the main window, that click
	// only brings the window forward.
	AcceptsFirstMouse bool
	// [program]
	// Zoomed opens the window zoomed (the main window left maximised); Size is the size it
	// restores to.
	Zoomed bool
	// [/program]
}

// Window is one top-level NSWindow with the standard title bar whose content is a kit
// Root: its kit.Host. Methods belong to the main thread, except Post.
type Window struct {
	app    *App
	handle cgo.Handle
	ns     unsafe.Pointer // the DXWindow, +1
	root   kit.Root
	opts   WindowOptions
	target *bitmapTarget

	size    kit.Size
	ppd     float32
	env     kit.Environment
	state   kit.WindowState
	shown   bool
	closed  bool
	closing bool

	dirty       kit.Rect
	frameWanted bool
	tickAsked   bool
	inFrame     bool

	depth   int      // calls into the root in progress
	pending []func() // calls queued while one was in progress (the never-nest rule)

	mods    kit.Modifiers
	buttons kit.Buttons
	inside  bool
	cursor  kit.Cursor

	mu     sync.Mutex
	posts  []func()
	postOK bool

	rnd rndWindow // [render] presentation state (present_window.go)
}

// NewWindow makes a window for root: root.Attach runs before it returns, and the window
// shows with the first frame the root paints.
func (a *App) NewWindow(root kit.Root, o WindowOptions) (*Window, error) {
	onMain()
	if o.Size.W <= 0 || o.Size.H <= 0 {
		return nil, errors.New("mac: a window needs a size")
	}
	w := &Window{app: a, root: root, opts: o, postOK: true}
	w.handle = cgo.NewHandle(w)
	title := C.CString(o.Title)
	w.ns = C.dxWindowNew(C.uintptr_t(w.handle), C.double(o.Size.W), C.double(o.Size.H), title)
	C.free(unsafe.Pointer(title))
	if w.ns == nil {
		w.handle.Delete()
		return nil, errors.New("mac: cannot make the window")
	}
	if o.MinSize.W > 0 || o.MinSize.H > 0 {
		C.dxWindowSetMinSize(w.ns, C.double(o.MinSize.W), C.double(o.MinSize.H))
	}
	if o.AcceptsFirstMouse {
		C.dxWindowSetFirstMouse(w.ns, C.bool(true))
	}
	w.readSize()
	w.limitSize()
	w.env = w.readEnvironment()
	w.state = w.readState()
	pw, ph, ppd := w.pixelSize()
	tgt, err := newSurfaceTarget(a.res, pw, ph, ppd, true) // [render] IOSurfaces, no copies
	if err != nil {
		C.dxWindowRelease(w.ns)
		w.handle.Delete()
		return nil, err
	}
	w.target = tgt
	a.windows[w] = struct{}{}
	// [program] the program learns the window before its root is attached, so that a panic
	// in Attach closes it (as win.AppOptions.OnWindowMade)
	if a.opts.OnWindowMade != nil {
		a.safely(func() { a.opts.OnWindowMade(w) })
	}
	// [/program]
	w.call(func() { root.Attach(w) })
	if w.closed {
		// Closed while it opened (the root closed it in Attach, or a panic there closed it):
		// it never shows. closedByAppKit has let it go.
		return nil, fmt.Errorf("mac: the window was closed while it opened: %w", kit.ErrClosed)
	}
	w.invalidateAll()
	w.tick() // the first frame, painted before the window shows
	// [program] placement memory, zoom
	w.progOpened()
	// [/program]
	C.dxWindowShow(w.ns, C.bool(!o.NoActivate))
	w.shown = true
	return w, nil
}

// resizeTarget fits the bitmap to the content area; the whole area is painted again.
func (w *Window) resizeTarget() {
	pw, ph, ppd := w.pixelSize()
	if err := w.target.Resize(pw, ph, ppd); err != nil {
		w.app.safely(func() { panic(err) })
	}
	w.invalidateAll()
}

// maxTargetSide is the largest side of a window's drawing surface, in device pixels
// (newSurfaceTarget).
const maxTargetSide = 16384

// limitSize keeps the content area within what the drawing surface can be at the window's
// scale: a window stretched over several screens can be resized no further.
func (w *Window) limitSize() {
	m := math.Floor(maxTargetSide / float64(w.ppd))
	C.dxWindowSetMaxSize(w.ns, C.double(m), C.double(m))
}

// pixelSize is the content area in device pixels.
func (w *Window) pixelSize() (int, int, float32) {
	return int(math.Ceil(float64(w.size.W * w.ppd))), int(math.Ceil(float64(w.size.H * w.ppd))), w.ppd
}

func (w *Window) readSize() {
	var cw, ch C.double
	C.dxWindowContentSize(w.ns, &cw, &ch)
	w.size = kit.Size{W: float32(cw), H: float32(ch)}
	w.ppd = float32(C.dxWindowScale(w.ns))
	if w.ppd <= 0 {
		w.ppd = 1
	}
}

func (w *Window) readEnvironment() kit.Environment {
	return withScrollEnvironment(kit.Environment{ // [mac scroll] scroll.go
		Scale:         kit.Scale{PixelsPerDIP: w.ppd, Text: 1},
		Dark:          bool(C.dxAppDark()),
		ReducedMotion: bool(C.dxAppReduceMotion()),
		// WebKit on macOS supports neither forced colours nor prefers-reduced-transparency:
		// forced-colors never matches (HighContrast stays off), and neither value of
		// prefers-reduced-transparency matches, so the dialog backdrop's blur, which app.css
		// gives only under "no-preference", is never drawn in today's Mac window. The kit draws
		// it unless ReducedTransparency, so the Mac reports it on: no blur, as today.
		ReducedTransparency: true,
		// [mac e2e] the window keeps its last frame's pixels: a shadow a view paints by
		// itself is repainted with it (kit.PaintOverflower), so none stays behind.
		PaintedOverflow: true,
		// [mac webkit] today's Mac window is WebKit: the kit draws app.css as WebKit does
		// where engines differ (kit.Environment.WebKit).
		WebKit: true,
	}) // [mac scroll]
}

func (w *Window) readState() kit.WindowState {
	var z, m C.bool
	C.dxWindowState(w.ns, &z, &m)
	return kit.WindowState{Maximized: bool(z), Minimized: bool(m)}
}

// ---- calls into the root ------------------------------------------------------------

// call runs f as a call into the root, or queues it while another is in progress.
func (w *Window) call(f func()) {
	if w.closed {
		return
	}
	if w.depth > 0 {
		w.pending = append(w.pending, f)
		return
	}
	w.depth++
	w.app.progCalls = append(w.app.progCalls, w) // [program] PanicWindow
	w.app.safely(f)
	w.app.progCalls = popWindow(w.app.progCalls) // [program] [ax] the slot left is cleared
	w.depth--
	for len(w.pending) > 0 && !w.closed {
		next := w.pending[0]
		w.pending = w.pending[1:]
		w.depth++
		w.app.progCalls = append(w.app.progCalls, w) // [program] PanicWindow
		w.app.safely(next)
		w.app.progCalls = popWindow(w.app.progCalls) // [program] [ax] the slot left is cleared
		w.depth--
	}
	if w.closing && !w.closed {
		w.closing = false
		C.dxWindowClose(w.ns)
	}
}

// event delivers e to the root and reports whether it used it; a queued event counts as
// not used.
func (w *Window) event(e kit.Event) bool {
	if w.closed {
		return false
	}
	if w.depth > 0 {
		w.pending = append(w.pending, func() { w.root.Event(e) })
		return false
	}
	used := false
	w.call(func() { used = w.root.Event(e) })
	return used
}

// ---- frames -------------------------------------------------------------------------

func (w *Window) askTick() {
	if w.closed || w.tickAsked {
		return
	}
	w.tickAsked = true
	if w.rndHidden() { // [render] a hidden window ticks once a second (present_window.go)
		w.rndHiddenTick()
		return
	} // [/render]
	C.dxWindowRequestTick(w.ns)
}

func (w *Window) invalidateAll() {
	w.dirty = kit.RectAt(kit.Point{}, w.size)
}

// tick makes one frame: Frame, then Paint of what is invalid.
func (w *Window) tick() {
	w.tickAsked = false
	if w.closed || w.inFrame || w.state.Minimized {
		return
	}
	if w.depth > 0 { // never inside a root call: come back at once after it
		// [program] queued in pending, the tick ran with depth > 0 again and queued itself
		// for ever (a Resize inside a call: the progress window's fit hung the process); the
		// next display refresh paints instead.
		w.askTick()
		return
		// [/program]
	}
	// [render] a hidden window runs Frame at most once a second and paints only when it
	// shows (present_window.go)
	hidden := w.rndHidden()
	if hidden && !w.rndHiddenFrameDue() {
		return
	} // [/render]
	w.frameWanted = false
	now := time.Now()
	w.call(func() { w.root.Frame(now) })
	if w.closed {
		return
	}
	if hidden { // [render]
		w.rnd.hiddenFrames++
		return
	} // [/render]
	dirty := w.dirty.Intersect(kit.RectAt(kit.Point{}, w.size))
	w.dirty = kit.Rect{}
	if dirty.Empty() {
		return
	}
	dirty = windowRoundOut(dirty, w.ppd)
	w.inFrame = true
	c, err := w.target.BeginFrame(dirty)
	if err != nil { // [render] nothing was drawn: the area stays to paint
		w.inFrame = false
		switch {
		case errors.Is(err, kit.ErrDeviceLost): // a surface cannot be locked: paint all again
			w.invalidateAll()
			w.askTick()
		case errors.Is(err, errRndBusy): // the compositor still holds the surface: next refresh
			w.dirty = w.dirty.Union(dirty)
			w.askTick()
		default:
			w.dirty = w.dirty.Union(dirty)
		}
		return
	} // [/render]
	w.call(func() { w.root.Paint(c, dirty) })
	err = w.target.EndFrame()
	w.inFrame = false
	if errors.Is(err, kit.ErrDeviceLost) {
		w.invalidateAll()
		w.askTick()
		return
	}
	w.present()
}

func (w *Window) present() {
	if w.rndPresent() { // [render] the target's IOSurface (present_window.go)
		return
	} // [/render]
	img := w.target.CGImage()
	if img == 0 {
		return
	}
	C.dxWindowPresent(w.ns, img, C.double(w.ppd))
	C.CGImageRelease(img)
}

// windowRoundOut is r rounded out to whole device pixels, in DIPs.
func windowRoundOut(r kit.Rect, ppd float32) kit.Rect {
	f := func(v float32, up bool) float32 {
		p := float64(v * ppd)
		if up {
			p = math.Ceil(p - 1e-3)
		} else {
			p = math.Floor(p + 1e-3)
		}
		return float32(p) / ppd
	}
	return kit.R(f(r.Min.X, false), f(r.Min.Y, false), f(r.Max.X, true), f(r.Max.Y, true))
}

// ---- kit.Host -----------------------------------------------------------------------

func (w *Window) Environment() kit.Environment { return w.env }
func (w *Window) Size() kit.Size               { return w.size }
func (w *Window) WindowState() kit.WindowState { return w.state }
func (w *Window) Resources() kit.Resources     { return w.app.res }

// Renderer: the window draws with Core Graphics on the CPU.
func (w *Window) Renderer() string { return "software" }

func (w *Window) Invalidate(r kit.Rect) {
	if w.closed || r.Empty() {
		return
	}
	if w.dirty.Empty() {
		w.dirty = r
	} else {
		w.dirty = w.dirty.Union(r)
	}
	if !w.inFrame {
		w.askTick()
	}
}

func (w *Window) RequestFrame() {
	if w.closed {
		return
	}
	w.frameWanted = true
	w.askTick()
}

func (w *Window) SetCursor(c kit.Cursor) {
	if c == w.cursor {
		return
	}
	w.cursor = c
	if w.inside {
		C.dxWindowSetCursor(C.int(c))
	}
}

// CapturePointer: AppKit sends the drags of a press to the view it went down in already.
func (w *Window) CapturePointer() {}
func (w *Window) ReleasePointer() {}

// SetTextInput: the input methods compose for the root's own text input (field.go).
func (w *Window) SetTextInput(t kit.TextInput) { w.fldSetTextInput(t) } // [fields]

func (w *Window) ClipboardText() (string, error) {
	if w.closed {
		return "", kit.ErrClosed
	}
	p := C.dxPasteboardText()
	if p == nil {
		return "", nil
	}
	defer C.free(unsafe.Pointer(p))
	return C.GoString(p), nil
}

func (w *Window) SetClipboardText(s string) error {
	if w.closed {
		return kit.ErrClosed
	}
	cs := C.CString(s)
	defer C.free(unsafe.Pointer(cs))
	if !C.dxPasteboardSetText(cs) {
		return errors.New("mac: cannot write the clipboard")
	}
	return nil
}

func (w *Window) SetTitle(t string) {
	if w.closed {
		return
	}
	cs := C.CString(t)
	C.dxWindowSetTitle(w.ns, cs)
	C.free(unsafe.Pointer(cs))
}

func (w *Window) SetMinSize(s kit.Size) {
	if w.closed {
		return
	}
	C.dxWindowSetMinSize(w.ns, C.double(max(s.W, 0)), C.double(max(s.H, 0)))
}

func (w *Window) Resize(s kit.Size) {
	if w.closed || !(s.W > 0 && s.H > 0) {
		return
	}
	C.dxWindowSetContentSize(w.ns, C.double(s.W), C.double(s.H))
}

func (w *Window) SetFrameColors(c kit.FrameColors) {
	if w.closed {
		return
	}
	b := c.Background
	C.dxWindowSetColors(w.ns, C.double(b.R), C.double(b.G), C.double(b.B), C.double(b.A), C.bool(c.Dark))
}

func (w *Window) Command(c kit.WindowCommand) {
	if w.closed {
		return
	}
	n := map[kit.WindowCommand]int{kit.WindowMinimize: 1, kit.WindowMaximize: 2, kit.WindowRestore: 3, kit.WindowClose: 4}[c]
	if n == 0 {
		return
	}
	// performClose asks windowShouldClose, a call into the root: run it after this one.
	w.app.Post(func() {
		if w.closed {
			return
		}
		if n == 4 {
			// AppKit ignores performClose: on a window with a sheet: the picker shown on it
			// ends as cancelled first, so that Quit and Close Window close it (today's menu
			// ended the process, sheet and all).
			dialogsOwnerClosing(w)
		}
		C.dxWindowCommand(w.ns, C.int(n))
	})
}

// Close closes the window without a CloseEvent once the call in progress has returned.
func (w *Window) Close() {
	if w.closed {
		return
	}
	if w.depth > 0 {
		w.closing = true
		return
	}
	C.dxWindowClose(w.ns)
}

func (w *Window) Post(f func()) error {
	w.mu.Lock()
	if !w.postOK {
		w.mu.Unlock()
		return kit.ErrClosed
	}
	w.posts = append(w.posts, f)
	w.mu.Unlock()
	w.app.Post(w.runPosts)
	return nil
}

func (w *Window) runPosts() {
	w.mu.Lock()
	fs := w.posts
	w.posts = nil
	w.mu.Unlock()
	for _, f := range fs {
		w.call(f)
	}
}

// NotifySemantics hands the change to the window's accessibility (ax.go).
func (w *Window) NotifySemantics(e kit.SemanticEvent) {
	// [ax]
	axNotify(w, e)
	// [/ax]
}

// ---- what AppKit tells -------------------------------------------------------------

func (w *Window) environmentChanged() {
	if w.closed {
		return
	}
	oldSize, oldPPD := w.size, w.ppd
	w.readSize()
	env := w.readEnvironment()
	if w.ppd != oldPPD {
		w.limitSize()
		w.resizeTarget()
		w.invalidateAll()
		w.fldRescale() // [fields] the native fields on the new device pixels
	}
	if env != w.env {
		w.env = env
		w.event(kit.EnvironmentEvent{Env: env})
	}
	if w.size != oldSize {
		w.event(kit.SizeEvent{Size: w.size})
	}
	w.askTick()
}

func (w *Window) resized() {
	if w.closed {
		return
	}
	old := w.size
	w.readSize()
	if w.size == old {
		return
	}
	w.resizeTarget()
	w.invalidateAll()
	w.event(kit.SizeEvent{Size: w.size})
	if st := w.readState(); st != w.state {
		w.state = st
		w.event(kit.WindowStateEvent{State: st})
	}
	w.tick() // during a live resize, paint before AppKit shows the new size
}

func (w *Window) stateChanged() {
	st := w.readState()
	if st == w.state {
		return
	}
	w.state = st
	w.event(kit.WindowStateEvent{State: st})
	if !st.Minimized {
		w.invalidateAll()
		w.askTick()
	}
}

func (w *Window) closedByAppKit() {
	if w.closed {
		return
	}
	w.fldWindowClosing() // [fields]
	// [popups] its popups hear DismissClosed and close; tooltip and drop target go
	popupOwnerClosing(w)
	// [/popups]
	w.mu.Lock()
	w.postOK = false
	w.posts = nil
	w.mu.Unlock()
	// [ax] the elements disconnect before the root's last call
	axClosed(w)
	// [/ax]
	if w.depth == 0 {
		w.app.safely(w.root.Detach)
	}
	w.closed = true
	w.pending = nil
	w.rndClosed() // [render] the idle timer stops
	w.target.Close()
	w.app.removed(w)
	ns := w.ns
	w.app.Post(func() {
		C.dxWindowRelease(ns)
		w.handle.Delete()
	})
}

//export goWindowTick
func goWindowTick(h C.uintptr_t) {
	if w := windowOf(h); w != nil {
		w.tick()
	}
}

//export goWindowExpose
func goWindowExpose(h C.uintptr_t) {
	if w := windowOf(h); w != nil && w.shown {
		w.invalidateAll()
		w.tick()
	}
}

//export goWindowShouldClose
func goWindowShouldClose(h C.uintptr_t) bool {
	w := windowOf(h)
	if w == nil || w.closed {
		return true
	}
	return !w.event(kit.CloseEvent{})
}

//export goWindowClosed
func goWindowClosed(h C.uintptr_t) {
	if w := windowOf(h); w != nil {
		w.closedByAppKit()
	}
}

//export goWindowResized
func goWindowResized(h C.uintptr_t) {
	if w := windowOf(h); w != nil {
		w.resized()
	}
}

//export goWindowEnvironment
func goWindowEnvironment(h C.uintptr_t) {
	if w := windowOf(h); w != nil {
		w.environmentChanged()
	}
}

//export goWindowFocus
func goWindowFocus(h C.uintptr_t, in bool) {
	if w := windowOf(h); w != nil {
		w.event(kit.FocusEvent{In: in})
	}
}

//export goWindowStateChanged
func goWindowStateChanged(h C.uintptr_t) {
	if w := windowOf(h); w != nil {
		w.stateChanged()
	}
}

//export goWindowPointer
func goWindowPointer(h C.uintptr_t, action C.int, x, y C.double, button, buttons, mods, clicks C.int) {
	w := windowOf(h)
	if w == nil {
		return
	}
	e := kit.PointerEvent{
		Action: kit.PointerAction(action), Pos: kit.Point{X: float32(x), Y: float32(y)},
		Button: kit.Buttons(button), Buttons: kit.Buttons(buttons), Mods: kit.Modifiers(mods), Clicks: int(clicks),
	}
	switch e.Action {
	case kit.PointerEnter:
		w.inside = true
		C.dxWindowSetCursor(C.int(w.cursor))
	case kit.PointerLeave:
		if buttons != 0 { // a drag goes on outside: AppKit keeps sending its events
			return
		}
		w.inside = false
	case kit.PointerDown:
		e.Buttons |= e.Button
	case kit.PointerUp:
		e.Buttons &^= e.Button
	}
	w.mods = e.Mods
	w.event(e)
}

//export goWindowWheel
func goWindowWheel(h C.uintptr_t, x, y, dx, dy C.double, precise bool, mods C.int) {
	w := windowOf(h)
	if w == nil {
		return
	}
	// A mouse wheel's notches scroll as far as WebKit's (popWheel, popup.go).
	if e, ok := popWheel(float32(x), float32(y), float32(dx), float32(dy), precise, kit.Modifiers(mods)); ok {
		w.event(e)
	}
}

//export goWindowKey
func goWindowKey(h C.uintptr_t, down bool, code C.ushort, ch C.ushort, mods C.int, repeat bool) bool {
	w := windowOf(h)
	if w == nil {
		return false
	}
	k := keyOf(uint16(code), rune(ch))
	if k == 0 {
		return false
	}
	a := kit.KeyUp
	if down {
		a = kit.KeyDown
	}
	return w.event(kit.KeyEvent{Action: a, Key: k, Mods: kit.Modifiers(mods), Repeat: repeat})
}

// goWindowEditCommand runs an Edit menu command chosen with the mouse ("copy",
// "selectAll") on the focused element when it takes edit commands, as a call into the root.
//
//export goWindowEditCommand
func goWindowEditCommand(h C.uintptr_t, cmd *C.char) {
	w := windowOf(h)
	if w == nil {
		return
	}
	name := C.GoString(cmd)
	w.call(func() {
		fr, ok := w.root.(interface{ Focused() *kit.Element })
		if !ok {
			return
		}
		el := fr.Focused()
		if el == nil {
			return
		}
		if ec, ok := el.Widget().(interface{ EditCommand(string) bool }); ok {
			ec.EditCommand(name)
		}
	})
}

//export goWindowFlags
func goWindowFlags(h C.uintptr_t, code C.ushort, mods C.int) {
	w := windowOf(h)
	if w == nil {
		return
	}
	k := keyOf(uint16(code), 0)
	if k == 0 {
		return
	}
	m := kit.Modifiers(mods)
	bit := map[kit.Key]kit.Modifiers{kit.KeyShift: kit.ModShift, kit.KeyControl: kit.ModCtrl, kit.KeyAlt: kit.ModAlt, kit.KeyMeta: kit.ModMeta}[k]
	a := kit.KeyUp
	if m&bit != 0 {
		a = kit.KeyDown
	}
	w.mods = m
	w.event(kit.KeyEvent{Action: a, Key: k, Mods: m})
}

//export goWindowText
func goWindowText(h C.uintptr_t, s *C.char) {
	if w := windowOf(h); w != nil {
		w.event(kit.TextEvent{Text: C.GoString(s)})
	}
}

// compile-time checks
var (
	_ kit.Host         = (*Window)(nil)
	_ kit.RendererInfo = (*Window)(nil)
)

// WritePNG writes what the window shows, as drawn (device pixels), for measuring tools.
// [render] A hidden window paints only when it shows again: its shot is its last frame.
func (w *Window) WritePNG(name string) error {
	if w.closed {
		return kit.ErrClosed
	}
	return (&Offscreen{t: w.target}).WritePNG(name)
}

// WriteViewsPNG writes what the window shows with its native text fields, as the screen
// shows it, for measuring tools (WritePNG has the kit's drawing alone).
func (w *Window) WriteViewsPNG(name string) error {
	if w.closed {
		return kit.ErrClosed
	}
	cs := C.CString(name)
	defer C.free(unsafe.Pointer(cs))
	if !C.dxWindowViewsPNG(w.ns, cs) {
		return errors.New("mac: cannot write the window's picture")
	}
	return nil
}
