//go:build darwin && cgo

package mac

// [popups] The tests' ways into AppKit (package popuptest, which no product imports) and
// into the windows' and popups' state.

import (
	"time"

	"downex/ui/kit"
	"downex/ui/mac/popuptest"
)

// pumpEvents handles the application's events for d on the main thread.
func pumpEvents(d time.Duration) { popuptest.Pump(d) }

// postMouse posts a mouse event (an NSEventType) at p (w's content DIPs), as the pointer
// makes it.
func (w *Window) postMouse(typ int, p kit.Point, clicks int) {
	popuptest.PostMouse(w.ns, typ, float64(p.X), float64(p.Y), clicks)
}

// postKey posts a key (a virtual key code) with its characters.
func (w *Window) postKey(code int, down bool, chars string) {
	popuptest.PostKey(w.ns, code, down, chars)
}

// postScroll posts lines turns of a mouse wheel over p.
func (w *Window) postScroll(p kit.Point, lines int) {
	popuptest.PostScroll(w.ns, float64(p.X), float64(p.Y), lines, popuptest.PhaseNone)
}

// postScrollPhase posts a trackpad wheel event of that phase over p that scrolls nothing.
func (w *Window) postScrollPhase(p kit.Point, phase int) {
	popuptest.PostScroll(w.ns, float64(p.X), float64(p.Y), 0, phase)
}

func (w *Window) makeKey()       { popuptest.MakeKey(w.ns) }
func (w *Window) isKeyNow() bool { return popuptest.IsKey(w.ns) }

// moveBy moves the window dx points to the right.
func (w *Window) moveBy(dx float64) { popuptest.MoveWindow(w.ns, dx) }

// screenToContent converts a screen point (points, y up) to w's content DIPs.
func (w *Window) screenToContent(sx, sy float64) kit.Point {
	x, y := popuptest.ContentPoint(w.ns, sx, sy)
	return kit.Pt(float32(x), float32(y))
}

// postMouse posts a mouse event to the popup's panel at at (popup DIPs).
func (p *popupWindow) postMouse(typ int, at kit.Point, clicks int) {
	at = at.Add(p.shift)
	popuptest.PostMouse(p.ns, typ, float64(at.X), float64(at.Y), clicks)
}

// screenFrame is the panel's frame on the screen (points, y up).
func (p *popupWindow) screenFrame() (x, y, w, h float64) { return popuptest.Frame(p.ns) }

// ignoresPointer reports whether the panel lets the pointer through now.
func (p *popupWindow) ignoresPointer() bool { return popuptest.IgnoresMouse(p.ns) }

// ownerEvent tells w's popups what its observer tells (2 deactivated, 3 moved).
func (w *Window) ownerEvent(reason int) { w.popupOwnerEvent(kit.DismissReason(reason)) }

// tooltipText is w's tooltip text as armed now ("" for none).
func (w *Window) tooltipText() string {
	if st := tipsOf[w]; st != nil {
		return st.text
	}
	return ""
}

// dragCall calls w's view's dragging method (1 entered, 2 updated, 3 exited, 4 perform)
// with a drag of those strings at p; it reports a copy.
func (w *Window) dragCall(method int, p kit.Point, url, text *string, files string, copyOK bool) bool {
	return popuptest.Drag(w.ns, method, float64(p.X), float64(p.Y), url, text, nil, files, copyOK) != 0
}

// addTestField puts an editable NSTextField at r of w's content.
func (w *Window) addTestField(r kit.Rect, text string) popuptest.Field {
	return popuptest.AddField(w.ns, float64(r.Min.X), float64(r.Min.Y), float64(r.Width()), float64(r.Height()), text)
}
