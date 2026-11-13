//go:build darwin && cgo

package mac

// [popups] Tooltips (kit.Tooltips).

/*
#include <stdlib.h>
#include "popup_darwin.h"
*/
import "C"

import (
	"unsafe"

	"downex/ui/kit"
)

// The windows' tooltips: AppKit's own, as WKWebView shows an element's title on today's
// Mac window. As WebKit does, the window asks for the tooltip at every pointer move (the
// source is a hit test), and when the text changes the tooltip view puts a new tooltip
// rectangle under the pointer (public NSView API), which AppKit's tooltip manager shows
// after the system's delay (NSInitialToolTipDelay), places and wraps, and hides when a
// button or a key goes down or the application deactivates. While the pointer stays inside
// the area the source gave for the shown text nothing is asked again. The text is shown as
// it is.

var _ kit.Tooltips = (*Window)(nil)

// tipState is one window's tooltip.
type tipState struct {
	w      *Window
	view   unsafe.Pointer // the DXTipView, +1
	source func(kit.Point) (string, kit.Rect, bool)
	text   string
	area   kit.Rect // window DIPs, of text
	last   kit.Point
	moved  bool
}

var tipsOf = map[*Window]*tipState{}

// SetTooltipSource implements kit.Tooltips.
func (w *Window) SetTooltipSource(source func(p kit.Point) (string, kit.Rect, bool)) {
	st := tipsOf[w]
	switch {
	case source == nil && st != nil:
		delete(tipsOf, w)
		C.dxpTipDetach(st.view)
		st.view = nil
		C.dxpMonitorRelease()
	case source != nil && st == nil:
		if w.closed || w.ns == nil {
			return
		}
		st = &tipState{w: w, source: source}
		st.view = C.dxpTipAttach(w.ns, C.uintptr_t(w.handle))
		tipsOf[w] = st
		C.dxpMonitorRetain()
	case source != nil:
		st.source = source
	}
}

// moved follows a pointer move over the window's content (window DIPs).
func (st *tipState) movedTo(p kit.Point) {
	if st.moved && p == st.last {
		return // the same point (a move the system repeats)
	}
	if st.text != "" && st.area.Contains(p) {
		st.moved, st.last = true, p
		return
	}
	if st.w.depth > 0 {
		// A call into the root is in progress (a modal loop run from inside one): the source
		// cannot be asked now, so nothing changes and the next move asks again.
		return
	}
	st.moved, st.last = true, p
	var text string
	var area kit.Rect
	st.w.call(func() {
		t, a, ok := st.source(p)
		if ok {
			text, area = t, a
		}
	})
	if tipsOf[st.w] != st { // the source turned tooltips off
		return
	}
	st.set(text, area)
}

// left is the pointer leaving the window's content: the tooltip goes (WebKit's title does
// too), and coming back asks again.
func (st *tipState) left() {
	st.moved = false
	st.set("", kit.Rect{})
}

func (st *tipState) set(text string, area kit.Rect) {
	if text == "" {
		area = kit.Rect{}
	}
	st.area = area
	if text == st.text {
		return
	}
	st.text = text
	cs := C.CString(stripNULs(text))
	C.dxpTipSet(st.view, cs)
	C.free(unsafe.Pointer(cs))
}

//export goTipMoved
func goTipMoved(h C.uintptr_t, x, y C.double) {
	if w := windowOf(h); w != nil {
		w.tipMoved(kit.Pt(float32(x), float32(y)))
	}
}

// tipMoved is a pointer move over w's content (window DIPs), as the event monitor sees it.
func (w *Window) tipMoved(p kit.Point) {
	if w.closed {
		return
	}
	if st := tipsOf[w]; st != nil {
		st.movedTo(p)
	}
}

//export goTipLeft
func goTipLeft(h C.uintptr_t) {
	if w := windowOf(h); w != nil {
		w.tipLeft()
	}
}

// tipLeft is the pointer leaving w's content.
func (w *Window) tipLeft() {
	if st := tipsOf[w]; st != nil {
		st.left()
	}
}
