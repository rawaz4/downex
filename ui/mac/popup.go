//go:build darwin && cgo

package mac

// [popups] Popup windows (kit.Popups): menus and Select lists.

/*
#include <stdlib.h>
#include <CoreGraphics/CoreGraphics.h>
#include "popup_darwin.h"
#include "ax_darwin.h"
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

// popupWindow is a popup window of a Window (kit.Popups): a borderless NSPanel that never
// becomes key (NSWindowStyleMaskNonactivatingPanel, canBecomeKeyWindow NO), a child window
// of its owner so that it stays above it, as ui/win's WS_EX_NOACTIVATE popup. It is not
// opaque: its root draws the box's shadow (--shadow-lg) on a transparent ground, and the
// pointer goes through the panel wherever it is not over the root's Hit area (the panel
// ignores mouse events there, and a press that still reaches it is sent to the window
// beneath). Its root gets keys only through Deliver: the owner keeps the keyboard. It
// implements kit.Host for its root and calls into it one call at a time, as Window does.
type popupWindow struct {
	owner  *Window
	app    *App
	root   kit.Root
	opts   kit.PopupOptions
	handle cgo.Handle
	ns     unsafe.Pointer // the DXPopupPanel, +1

	bounds kit.Rect // owner window DIPs
	hit    kit.Rect // popup DIPs
	ppd    float32
	size   kit.Size
	pw, ph int       // the panel's device pixels
	shift  kit.Point // where the root's (0, 0) is in the panel (DIPs, below a point)
	env    kit.Environment
	target *bitmapTarget

	pending     kit.Rect
	frameWanted bool
	shown       bool

	depth    int
	queue    []popQueued
	closing  bool
	closed   bool
	attached bool

	postMu    sync.Mutex
	posts     []func()
	postWoken bool
	postShut  bool

	cursor  kit.Cursor
	inside  bool
	buttons kit.Buttons

	frames, paints int // counts for tests
}

type popQueued struct {
	e kit.Event
	f func()
}

// popupsOf are each Window's open popups (main thread).
var popupsOf = map[*Window][]*popupWindow{}

var _ kit.Popups = (*Window)(nil)

// OpenPopup implements kit.Popups.
func (w *Window) OpenPopup(root kit.Root, o kit.PopupOptions) (kit.Popup, error) {
	if w.closed || w.ns == nil {
		return nil, fmt.Errorf("mac: popup: %w", kit.ErrClosed)
	}
	if root == nil {
		return nil, errInvalid("OpenPopup without a root")
	}
	onMain()
	p := &popupWindow{owner: w, app: w.app, root: root, opts: o, bounds: o.Bounds, hit: o.Hit, ppd: w.ppd, env: w.env}
	if !(p.ppd > 0) {
		p.ppd = 1
	}
	p.layout()
	tgt, err := newBitmapTarget(w.app.res, p.pw, p.ph, p.ppd, false)
	if err != nil {
		return nil, fmt.Errorf("mac: popup: %w", err)
	}
	tgt.ClearFrames = true
	p.target = tgt
	p.handle = cgo.NewHandle(p)
	title := C.CString(stripNULs(o.Name))
	p.ns = C.dxpPanelNew(C.uintptr_t(p.handle), title)
	C.free(unsafe.Pointer(title))
	if p.ns == nil {
		p.handle.Delete()
		tgt.Close()
		return nil, errors.New("mac: cannot make the popup panel")
	}
	p.place()
	axRegister(p) // its accessibility finds it from the first request (nothing is read now)
	if len(popupsOf[w]) == 0 {
		C.dxpOwnerObserve(w.ns, C.uintptr_t(w.handle), true)
	}
	popupsOf[w] = append(popupsOf[w], p)
	C.dxpMonitorRetain()
	p.attached = true
	p.depth++
	w.app.safely(func() { root.Attach(p) })
	p.depth--
	p.drain() // a Close inside Attach closes it now, before any Frame
	if !p.closed {
		p.pending = kit.RectAt(kit.Point{}, p.size)
		p.frame()
		p.drain()
	}
	if p.closed {
		return nil, fmt.Errorf("mac: the popup was closed while it opened: %w", kit.ErrClosed)
	}
	return p, nil
}

// layout works out the popup's pixels from its bounds in the owner's DIPs, rounded to the
// owner's device pixels (at least one). AppKit puts a window only on whole points, so a
// popup that starts between two (a half point on a Retina screen) gets a panel from the
// whole point before it, and its root draws shifted by the rest (shift), where WebKit
// would have drawn the box.
func (p *popupWindow) layout() {
	b, ppd := p.bounds, p.ppd
	x0 := math.Round(float64(b.Min.X * ppd))
	y0 := math.Round(float64(b.Min.Y * ppd))
	x1 := math.Round(float64(b.Max.X * ppd))
	y1 := math.Round(float64(b.Max.Y * ppd))
	w, h := max(int(x1-x0), 1), max(int(y1-y0), 1)
	p.size = kit.Size{W: float32(w) / ppd, H: float32(h) / ppd}
	ox, oy := x0/float64(ppd), y0/float64(ppd)
	p.shift = kit.Pt(float32(ox-math.Floor(ox+1e-6)), float32(oy-math.Floor(oy+1e-6)))
	p.pw = w + int(math.Round(float64(p.shift.X*ppd)))
	p.ph = h + int(math.Round(float64(p.shift.Y*ppd)))
}

// origin is the popup's top-left corner in the owner's DIPs, on a device pixel: where its
// root's (0, 0) is.
func (p *popupWindow) origin() kit.Point {
	ppd := float64(p.ppd)
	return kit.Pt(float32(math.Round(float64(p.bounds.Min.X)*ppd)/ppd), float32(math.Round(float64(p.bounds.Min.Y)*ppd)/ppd))
}

// place puts the panel at its bounds (the owner's screen keeps its hit area).
func (p *popupWindow) place() {
	o := p.origin().Sub(p.shift)
	h := p.hitArea().Add(p.shift)
	C.dxpPanelPlace(p.ns, p.owner.ns, C.double(o.X), C.double(o.Y),
		C.double(float32(p.pw)/p.ppd), C.double(float32(p.ph)/p.ppd),
		C.double(h.Min.X), C.double(h.Min.Y), C.double(h.Width()), C.double(h.Height()))
}

// hitArea is the part that takes the pointer, in popup DIPs: Hit, or the whole popup.
func (p *popupWindow) hitArea() kit.Rect {
	if p.hit.Empty() {
		return kit.RectAt(kit.Point{}, p.size)
	}
	return p.hit
}

// frame makes one frame: the root's Frame, then a paint of what is invalid on a
// transparent ground, shown as the panel's content; the first one shows the panel.
func (p *popupWindow) frame() {
	if p.closed {
		return
	}
	if p.depth > 0 {
		p.queue = append(p.queue, popQueued{f: p.frame})
		return
	}
	p.frames++
	now := time.Now()
	p.call(func() { p.root.Frame(now) })
	if p.closed {
		return
	}
	dirty := p.pending.Intersect(kit.RectAt(kit.Point{}, p.size))
	p.pending = kit.Rect{}
	if dirty.Empty() {
		return
	}
	dirty = windowRoundOut(dirty, p.ppd)
	c, err := p.target.BeginFrame(dirty.Add(p.shift))
	if err != nil {
		return
	}
	p.paints++
	shifted := p.shift != (kit.Point{})
	if shifted {
		c.PushTransform(kit.Translation(p.shift.X, p.shift.Y))
	}
	p.call(func() { p.root.Paint(c, dirty) })
	if shifted {
		c.Pop()
	}
	err = p.target.EndFrame()
	if errors.Is(err, kit.ErrDeviceLost) {
		p.Invalidate(kit.RectAt(kit.Point{}, p.size))
		return
	}
	if p.closed {
		return
	}
	if img := p.target.CGImage(); img != 0 {
		C.dxpPanelPresent(p.ns, img, C.double(p.ppd))
		C.CGImageRelease(img)
	}
	p.show()
}

// show orders the panel in with its first frame. A popup that opens under the pointer (a
// context menu at the right-click) takes it from the owner at once, as an element over a
// row takes :hover in the WebView: the row's hover goes without waiting for a move.
func (p *popupWindow) show() {
	if p.shown {
		return
	}
	p.shown = true
	C.dxpPanelShow(p.ns, p.owner.ns)
	axPopupOpened(p.owner, p) // VoiceOver follows the popup's active item while it is open
	if bool(C.dxpPanelPointerInHit(p.ns)) && p.owner.inside {
		p.owner.event(kit.PointerEvent{Action: kit.PointerLeave, Mods: p.owner.mods})
	}
}

// ---- calls into the root, one at a time ----

func (p *popupWindow) event(e kit.Event) bool {
	if p.closed || !p.attached {
		return false
	}
	if p.depth > 0 {
		p.queue = append(p.queue, popQueued{e: e})
		return false
	}
	used := false
	p.depth++
	p.app.safely(func() { used = p.root.Event(e) })
	p.depth--
	p.drain()
	return used
}

func (p *popupWindow) call(f func()) {
	if p.closed || !p.attached || f == nil {
		return
	}
	if p.depth > 0 {
		p.queue = append(p.queue, popQueued{f: f})
		return
	}
	p.depth++
	p.app.safely(f)
	p.depth--
	p.drain()
}

func (p *popupWindow) drain() {
	for p.depth == 0 && !p.closed {
		if p.closing {
			p.destroy()
			return
		}
		if len(p.queue) == 0 {
			return
		}
		q := p.queue[0]
		p.queue[0] = popQueued{}
		p.queue = p.queue[1:]
		p.depth++
		if q.f != nil {
			p.app.safely(q.f)
		} else {
			e := q.e
			p.app.safely(func() { p.root.Event(e) })
		}
		p.depth--
	}
}

// destroy closes the popup now: the root's last call, then the panel and what it holds.
func (p *popupWindow) destroy() {
	if p.closed {
		return
	}
	p.closed = true
	p.queue = nil
	p.postMu.Lock()
	p.postShut = true
	p.posts = nil
	p.postMu.Unlock()
	C.dxpPanelClose(p.ns, p.owner.ns)
	// Its accessibility ends while the root is still whole, before Detach; VoiceOver's
	// focus goes back to the owner.
	axPopupClosed(p)
	axCloseHost(p)
	if p.attached {
		p.app.safely(p.root.Detach)
	}
	p.target.Close()
	p.forget()
	ns, h := p.ns, p.handle
	// The panel goes once AppKit is done with the event that closed it.
	p.app.Post(func() {
		C.dxpPanelRelease(ns)
		h.Delete()
	})
}

// forget takes the popup out of its owner's list.
func (p *popupWindow) forget() {
	list := popupsOf[p.owner]
	for i, x := range list {
		if x == p {
			popupsOf[p.owner] = append(list[:i:i], list[i+1:]...)
			C.dxpMonitorRelease()
			break
		}
	}
	if len(popupsOf[p.owner]) == 0 {
		delete(popupsOf, p.owner)
		if !p.owner.closed {
			C.dxpOwnerObserve(p.owner.ns, C.uintptr_t(p.owner.handle), false)
		}
	}
}

// dismiss tells the owner's root why the popup should close, as a call into it.
func (p *popupWindow) dismiss(d kit.Dismiss) {
	if p.closed || p.opts.OnDismiss == nil {
		return
	}
	f := p.opts.OnDismiss
	p.owner.call(func() { f(d) })
}

// ---- kit.Popup ----

// SetBounds implements kit.Popup: a new pixel size sends a SizeEvent and paints all again.
func (p *popupWindow) SetBounds(r kit.Rect) {
	if p.closed || r == p.bounds {
		return
	}
	ow, oh, os, osh := p.pw, p.ph, p.size, p.shift
	p.bounds = r
	p.layout()
	p.place()
	if p.pw != ow || p.ph != oh || p.shift != osh {
		if err := p.target.Resize(p.pw, p.ph, p.ppd); err != nil {
			return
		}
		if p.size != os {
			p.event(kit.SizeEvent{Size: p.size})
		}
		p.Invalidate(kit.RectAt(kit.Point{}, p.size))
	}
}

// SetHit implements kit.Popup.
func (p *popupWindow) SetHit(r kit.Rect) {
	if p.closed {
		return
	}
	p.hit = r
	h := p.hitArea().Add(p.shift)
	C.dxpPanelSetHit(p.ns, C.double(h.Min.X), C.double(h.Min.Y), C.double(h.Width()), C.double(h.Height()))
}

// Deliver implements kit.Popup.
func (p *popupWindow) Deliver(e kit.Event) bool { return p.event(e) }

// Close implements kit.Popup and kit.Host: the root gets Detach once the call in progress
// has returned.
func (p *popupWindow) Close() {
	if p.closed {
		return
	}
	if p.depth > 0 {
		p.closing = true
		return
	}
	p.destroy()
}

// ---- kit.Host ----

func (p *popupWindow) Environment() kit.Environment { return p.env }
func (p *popupWindow) Size() kit.Size               { return p.size }
func (p *popupWindow) WindowState() kit.WindowState { return kit.WindowState{} }
func (p *popupWindow) Resources() kit.Resources     { return p.app.res }

// Renderer: the popup draws as its owner does.
func (p *popupWindow) Renderer() string { return "software" }

func (p *popupWindow) Invalidate(r kit.Rect) {
	if p.closed || r.Empty() {
		return
	}
	if p.pending.Empty() {
		p.pending = r
	} else {
		p.pending = p.pending.Union(r)
	}
	p.RequestFrame()
}

func (p *popupWindow) RequestFrame() {
	if p.closed || p.frameWanted {
		return
	}
	p.frameWanted = true
	C.dxpPanelRequestTick(p.ns)
}

func (p *popupWindow) SetCursor(c kit.Cursor) {
	p.cursor = c
	if p.inside {
		C.dxpSetCursor(C.int(c))
	}
}

// CapturePointer: AppKit sends the drags of a press to the panel it went down in already.
func (p *popupWindow) CapturePointer()            {}
func (p *popupWindow) ReleasePointer()            {}
func (p *popupWindow) SetTextInput(kit.TextInput) {}

// ClipboardText implements kit.Host: the owner's clipboard.
func (p *popupWindow) ClipboardText() (string, error) {
	if p.closed {
		return "", kit.ErrClosed
	}
	return p.owner.ClipboardText()
}

func (p *popupWindow) SetClipboardText(s string) error {
	if p.closed {
		return kit.ErrClosed
	}
	return p.owner.SetClipboardText(s)
}

func (p *popupWindow) SetTitle(t string) {
	if p.closed {
		return
	}
	cs := C.CString(stripNULs(t))
	C.dxpPanelSetTitle(p.ns, cs)
	C.free(unsafe.Pointer(cs))
}

func (p *popupWindow) SetMinSize(kit.Size)            {}
func (p *popupWindow) SetFrameColors(kit.FrameColors) {}
func (p *popupWindow) Command(kit.WindowCommand)      {}

// Resize implements kit.Host: the popup grows or shrinks from its top-left corner.
func (p *popupWindow) Resize(s kit.Size) {
	if !(s.W > 0 && s.H > 0) {
		return
	}
	p.SetBounds(kit.RectAt(p.bounds.Min, s))
}

func (p *popupWindow) Post(f func()) error {
	p.postMu.Lock()
	if p.postShut {
		p.postMu.Unlock()
		return fmt.Errorf("mac: popup closed: %w", kit.ErrClosed)
	}
	p.posts = append(p.posts, f)
	wake := !p.postWoken
	p.postWoken = true
	p.postMu.Unlock()
	if wake {
		p.app.Post(p.runPosts)
	}
	return nil
}

func (p *popupWindow) runPosts() {
	p.postMu.Lock()
	take := p.posts
	p.posts = nil
	p.postWoken = false
	p.postMu.Unlock()
	for _, f := range take {
		p.call(f)
	}
}

// NotifySemantics hands the change to the popup's accessibility (ax.go).
func (p *popupWindow) NotifySemantics(e kit.SemanticEvent) {
	if !p.closed {
		axNotifyHost(p, e)
	}
}

// ---- the popup as an axHost (ax.go): its own semantic tree, a Menu or a List ----

func (p *popupWindow) axSemantics() kit.SemanticTree {
	if p.closed || !p.attached {
		return nil
	}
	return p.root.Semantics()
}

func (p *popupWindow) axPerform(r kit.ActionRequest) error {
	st := p.axSemantics()
	if st == nil {
		return kit.ErrClosed
	}
	ran := false
	var err error
	p.call(func() {
		ran = true
		err = st.Perform(r)
	})
	if !ran && p.closed {
		return kit.ErrClosed
	}
	return err // not run yet: queued behind the call in progress, reported done
}

func (p *popupWindow) axPost(r kit.ActionRequest) error {
	return p.Post(func() {
		if st := p.axSemantics(); st != nil {
			_ = st.Perform(r) // the client was told it was done
		}
	})
}

func (p *popupWindow) axView() unsafe.Pointer {
	if p.ns == nil {
		return nil
	}
	return C.dxpPanelContentView(p.ns)
}

// axClosed reports a popup that is gone, whose accessibility is never made again.
func (p *popupWindow) axClosed() bool { return p.closed }

// axGone reports a popup that is closing or closed: it is never registered again.
func (p *popupWindow) axGone() bool { return p.closed }

var _ axHost = (*popupWindow)(nil)

// AXWindow is the popup's panel as an accessibility element, as Window.AXWindow is the
// window's (tests, macspike).
func (p *popupWindow) AXWindow() *AXElement {
	if p.closed || p.ns == nil {
		return nil
	}
	return newAXElement(C.dxaxWindowElement(p.ns))
}

var (
	_ kit.Host         = (*popupWindow)(nil)
	_ kit.Popup        = (*popupWindow)(nil)
	_ kit.RendererInfo = (*popupWindow)(nil)
)

// ---- what AppKit tells ----

// popupOf is the popup of a panel's handle: 0 for a panel that closed (dxpPanelClose
// clears it before the handle is deleted).
func popupOf(h C.uintptr_t) *popupWindow {
	if h == 0 {
		return nil
	}
	p, _ := cgo.Handle(h).Value().(*popupWindow)
	if p == nil || p.closed {
		return nil
	}
	return p
}

//export goPopupTick
func goPopupTick(h C.uintptr_t) {
	if p := popupOf(h); p != nil && p.frameWanted {
		p.frameWanted = false
		p.frame()
	}
}

//export goPopupPointer
func goPopupPointer(h C.uintptr_t, action C.int, x, y C.double, button, buttons, mods C.int) {
	p := popupOf(h)
	if p == nil {
		return
	}
	pos := kit.Pt(float32(x), float32(y)).Sub(p.shift)
	e := kit.PointerEvent{Action: kit.PointerAction(action), Pos: pos, Button: kit.Buttons(button),
		Buttons: kit.Buttons(buttons), Mods: kit.Modifiers(mods)}
	switch e.Action {
	case kit.PointerMove:
		if e.Buttons == 0 && !p.hitArea().Contains(pos) {
			p.leave(e.Mods) // over the shadow: what is under the popup has the pointer
			return
		}
		if !p.inside {
			p.inside = true
			C.dxpSetCursor(C.int(p.cursor))
			p.event(kit.PointerEvent{Action: kit.PointerEnter, Pos: pos, Buttons: e.Buttons, Mods: e.Mods})
		}
	case kit.PointerLeave:
		if e.Buttons != 0 && p.buttons != 0 {
			return // a drag goes on outside
		}
		p.leave(e.Mods)
		return
	case kit.PointerDown:
		e.Buttons |= e.Button
		e.Clicks = 1
		p.buttons = e.Buttons
	case kit.PointerUp:
		e.Buttons &^= e.Button
		p.buttons = e.Buttons
	}
	p.event(e)
}

func (p *popupWindow) leave(mods kit.Modifiers) {
	if !p.inside {
		return
	}
	p.inside = false
	p.event(kit.PointerEvent{Action: kit.PointerLeave, Mods: mods})
}

//export goPopupCursor
func goPopupCursor(h C.uintptr_t) {
	if p := popupOf(h); p != nil && p.inside {
		C.dxpSetCursor(C.int(p.cursor))
	}
}

//export goPopupWheel
func goPopupWheel(h C.uintptr_t, x, y, dx, dy C.double, precise bool, mods C.int) {
	p := popupOf(h)
	if p == nil {
		return
	}
	if e, ok := popWheel(float32(x)-p.shift.X, float32(y)-p.shift.Y, float32(dx), float32(dy), precise, kit.Modifiers(mods)); ok {
		p.event(e)
	}
}

// webKitLineStep is how far WebKit scrolls for a line of a mouse wheel's notch on macOS
// (Scrollbar::pixelsPerLineStep): a WKWebView scrolled 40 points, at once, for an event of
// one line, whose scrollingDelta is 1 (lines, not points, for a notched wheel).
const webKitLineStep = 40

// popWheel is the wheel event the windows' and popups' views make of AppKit's deltas (DOM
// directions): a notched wheel's lines scroll webKitLineStep each, a trackpad's or a
// free-spinning wheel's points as they are, both at once, as WebKit does.
func popWheel(x, y, dx, dy float32, precise bool, mods kit.Modifiers) (kit.WheelEvent, bool) {
	e := kit.WheelEvent{Pos: kit.Pt(x, y), Mods: mods, Mode: kit.WheelPrecise}
	if precise {
		e.Delta = kit.Pt(dx, dy)
	} else {
		e.Delta = kit.Pt(dx*webKitLineStep, dy*webKitLineStep)
	}
	return e, e.Delta != (kit.Point{})
}

// goPopupOutside hears a press (kind 1) or the wheel (kind 4) anywhere in the application,
// before it goes to its window: every popup it was not over hears DismissPress or
// DismissWheel (the wheel over any popup dismisses nothing). win is the window the event
// goes to, sx and sy the screen point (points, y up), hit the popup whose Hit it is over.
//
//export goPopupOutside
func goPopupOutside(kind C.int, win unsafe.Pointer, sx, sy C.double, hit C.uintptr_t) {
	reason := kit.DismissReason(kind)
	var over *popupWindow
	if hit != 0 {
		over, _ = cgo.Handle(hit).Value().(*popupWindow)
	}
	if reason == kit.DismissWheel && over != nil {
		return // the wheel over a popup scrolls it
	}
	for owner, list := range popupsOf {
		for _, p := range append([]*popupWindow(nil), list...) {
			if p == over || p.closed {
				continue
			}
			d := kit.Dismiss{Reason: reason}
			if reason == kit.DismissPress {
				var x, y C.double
				d.Inside = bool(C.dxpOwnerPoint(owner.ns, win, sx, sy, &x, &y))
				d.Pos = kit.Pt(float32(x), float32(y))
			}
			p.dismiss(d)
		}
	}
}

// goPopupOwner hears the owner window losing the activation (2) or moving, changing size,
// being minimised or going full screen (3).
//
//export goPopupOwner
func goPopupOwner(h C.uintptr_t, reason C.int) {
	if w := windowOf(h); w != nil {
		w.popupOwnerEvent(kit.DismissReason(reason))
	}
}

// popupOwnerEvent tells w's popups why they should close (DismissDeactivate, DismissMove).
func (w *Window) popupOwnerEvent(reason kit.DismissReason) {
	for _, p := range append([]*popupWindow(nil), popupsOf[w]...) {
		p.dismiss(kit.Dismiss{Reason: reason})
	}
}

// popupOwnerClosing is the owner window closing: its popups hear DismissClosed and are
// closed by the host; its tooltip and drop target go with it.
func popupOwnerClosing(w *Window) {
	for _, p := range append([]*popupWindow(nil), popupsOf[w]...) {
		p.dismiss(kit.Dismiss{Reason: kit.DismissClosed})
		p.Close()
		if !p.closed { // closing inside one of its calls: now
			p.closing = false
			p.destroy()
		}
	}
	if len(popupsOf[w]) == 0 {
		delete(popupsOf, w)
	}
	C.dxpOwnerObserve(w.ns, C.uintptr_t(w.handle), false)
	w.SetTooltipSource(nil)
	w.DisableDrop()
	dialogsOwnerClosing(w) // a picker's sheet on it answers "cancelled"
}

// stripNULs drops NUL characters, which a C string cannot hold.
func stripNULs(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == 0 {
			b := make([]byte, 0, len(s))
			for j := 0; j < len(s); j++ {
				if s[j] != 0 {
					b = append(b, s[j])
				}
			}
			return string(b)
		}
	}
	return s
}
