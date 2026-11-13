//go:build windows

package win

import (
	"fmt"

	"downex/ui/kit"
	"downex/ui/win/internal/w32"
	"downex/ui/win/uia"
)

// The window's side of UI Automation (package uia): the provider of a window is made at the
// first WM_GETOBJECT that asks for UI Automation's root object, so a window that no client
// reads keeps none, and the kit keeps no lasting row ids for it either.

// wmGetObject is WM_GETOBJECT (winuser.h).
const wmGetObject = 0x003D

// uiaTrees are the windows' providers. They belong to the UI thread.
var uiaTrees = map[*Window]*uia.Tree{}

// uiaGetObject answers WM_GETOBJECT: UI Automation's root object gets the window's fragment
// root, and anything else (MSAA's object ids) is left to DefWindowProc (ok false).
func (w *Window) uiaGetObject(wp, lp uintptr) (uintptr, bool) {
	if int32(uint32(lp)) != uia.UiaRootObjectId || w.closed || w.uiaDone || !w.attached {
		return 0, false
	}
	t := uiaTrees[w]
	if t == nil {
		if w.root.Semantics() == nil {
			return 0, false
		}
		uiaSweep()
		var err error
		if t, err = uia.NewTree(uiaHost{w}); err != nil {
			w.hostErr = fmt.Errorf("win: UI Automation: %w", err)
			return 0, false
		}
		uiaTrees[w] = t
	}
	return t.GetObject(wp, lp)
}

// uiaNotify raises the UI Automation events of a semantic change: nothing while no client
// has read the window. Window.NotifySemantics (kit.Host's, window.go) hands it every event.
func (w *Window) uiaNotify(e kit.SemanticEvent) {
	if t := uiaTrees[w]; t != nil {
		t.Notify(e)
	}
}

// uiaDestroyed disconnects the window's providers: clients' elements of it report that
// they are gone, and UI Automation drops what it holds. The window's WM_DESTROY calls it
// (window.go), while the window and its root are still whole.
func (w *Window) uiaDestroyed() {
	w.uiaDone = true // a WM_GETOBJECT sent while the providers disconnect makes none again
	if t := uiaTrees[w]; t != nil {
		delete(uiaTrees, w)
		t.Close()
	}
}

// uiaSweep disconnects the providers of windows that are gone without a WM_DESTROY of their
// own (an App closed while a window's provider lived).
func uiaSweep() {
	for w, t := range uiaTrees {
		if w.closed {
			t.Close()
			delete(uiaTrees, w)
		}
	}
}

// uiaHost is a window as its provider sees it.
type uiaHost struct{ w *Window }

var _ uia.Host = uiaHost{}

func (h uiaHost) HWND() uintptr { return uintptr(h.w.hwnd) }

// Semantics is the root's tree while the window is open.
func (h uiaHost) Semantics() kit.SemanticTree {
	if h.w.closed || !h.w.attached {
		return nil
	}
	return h.w.root.Semantics()
}

// ToScreen maps window DIPs to physical screen pixels: the content's pixels per DIP (the
// window's DPI, or the one it is forced to draw at) from the client area's origin.
func (h uiaHost) ToScreen(r kit.Rect) uia.Rect {
	o := w32.ClientToScreen(h.w.hwnd, w32.Point{})
	ppd := float64(h.w.ppd())
	return uia.Rect{
		Left: float64(o.X) + float64(r.Min.X)*ppd, Top: float64(o.Y) + float64(r.Min.Y)*ppd,
		Width: float64(r.Width()) * ppd, Height: float64(r.Height()) * ppd,
	}
}

// FromScreen maps a physical screen point to window DIPs.
func (h uiaHost) FromScreen(x, y float64) kit.Point {
	o := w32.ClientToScreen(h.w.hwnd, w32.Point{})
	ppd := float64(h.w.ppd())
	return kit.Pt(float32((x-float64(o.X))/ppd), float32((y-float64(o.Y))/ppd))
}

// Perform does r as a call into the root, after the call in progress when one is.
func (h uiaHost) Perform(r kit.ActionRequest) error {
	st := h.Semantics()
	if st == nil {
		return fmt.Errorf("win: UI Automation action on a closed window: %w", kit.ErrClosed)
	}
	ran := false
	var err error
	h.w.call(func() {
		ran = true
		err = st.Perform(r)
	})
	if !ran {
		return nil // queued behind the call in progress (a modal loop the root entered)
	}
	return err
}

// Post does r as a call into the root once the client's call has returned.
func (h uiaHost) Post(r kit.ActionRequest) error {
	w := h.w
	return w.Post(func() {
		if st := (uiaHost{w}).Semantics(); st != nil {
			_ = st.Perform(r) // the client was told it was invoked; a node gone since is no error to report
		}
	})
}

// CustomFrame reports a window whose title bar the root draws.
func (h uiaHost) CustomFrame() bool { return h.w.custom }

// HostedBounds is where a hosted native field is.
func (h uiaHost) HostedBounds(hwnd uintptr) (kit.Rect, bool) {
	for _, f := range h.w.fields {
		if uintptr(f.hwnd) == hwnd && f.shown && !f.bounds.Empty() {
			return f.bounds, true
		}
	}
	return kit.Rect{}, false
}

// HostedWindow is the native field whose text area lies in node n's box: the field is the
// node's own window (a kit.TextField fills its content box with it).
func (h uiaHost) HostedWindow(n *kit.SemanticNode) uintptr {
	if n.Role != kit.RoleTextField || n.Bounds.Empty() {
		return 0
	}
	const slack = 0.5 // DIPs of rounding between the box and the field placed in it
	b := n.Bounds
	for _, f := range h.w.fields {
		r := f.bounds
		if !f.shown || r.Empty() {
			continue
		}
		if r.Min.X >= b.Min.X-slack && r.Min.Y >= b.Min.Y-slack && r.Max.X <= b.Max.X+slack && r.Max.Y <= b.Max.Y+slack {
			return uintptr(f.hwnd)
		}
	}
	return 0
}
