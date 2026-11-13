//go:build windows

package win

import (
	"unsafe"

	"downex/ui/kit"
	"downex/ui/win/internal/com"
	"downex/ui/win/internal/w32"
)

// The custom frame (WindowOptions.CustomFrame), written from the documentation of
// WM_NCCALCSIZE, WM_NCHITTEST, DwmExtendFrameIntoClientArea and Windows 11's Snap Layouts
// for custom title bars:
//
//   - WM_NCCALCSIZE keeps the system's left, right and bottom borders, which are the
//     invisible resize borders outside the window's 1 px edge, and gives the top to the
//     client area, so the root draws its title bar from the window's top edge. A maximised
//     window hangs its borders off the monitor; the top is moved down by one border so that
//     the client area is the monitor's work area (one line less on an edge with an
//     auto-hide taskbar, so that the taskbar can still come up).
//   - DwmExtendFrameIntoClientArea with a 1 px top margin keeps the system's shadow and
//     border; Windows 11 rounds the corners.
//   - WM_NCHITTEST answers the resize codes in the borders and in a band one border thick
//     along the top of the client area, and asks the root (Root.FrameHit) about the rest:
//     HTCAPTION over the title bar's ground, HTMINBUTTON, HTMAXBUTTON (what opens Snap
//     Layouts on Windows 11) and HTCLOSE over the window buttons, HTCLIENT elsewhere.
//   - Over the buttons the system sends non-client mouse messages, which become pointer
//     events for the root: moves draw the hover, and a press and release make a click, on
//     which the root acts with Host.Command. They are kept from DefWindowProc. Its handling
//     of a press draws classic buttons. Its handling of moves is left out too: with them
//     passed on, the Snap Layouts flyout never opened in four hovers with the real pointer,
//     while an earlier custom-frame window on the same PC, which answered HTMAXBUTTON the
//     same way and kept the moves from DefWindowProc, got it. A press on the
//     caption is the system's (drag, double click), and the root hears it as a press on
//     its title bar's ground, which takes the focus from a field, as a page's would.

// frameX and frameY are the system's resize border at dpi: the sizing frame and the padded
// border, what AdjustWindowRectExForDpi adds on each side of WS_OVERLAPPEDWINDOW.
func frameX(dpi uint32) int32 {
	return w32.GetSystemMetricsForDpi(w32.SM_CXSIZEFRAME, dpi) + w32.GetSystemMetricsForDpi(w32.SM_CXPADDEDBORDER, dpi)
}

func frameY(dpi uint32) int32 {
	return w32.GetSystemMetricsForDpi(w32.SM_CYSIZEFRAME, dpi) + w32.GetSystemMetricsForDpi(w32.SM_CXPADDEDBORDER, dpi)
}

// sysDPI is the DPI of the window's monitor, which the system's borders follow (the content
// may be drawn at a forced one).
func (w *Window) sysDPI() uint32 {
	if d := w32.GetDpiForWindow(w.hwnd); d != 0 {
		return d
	}
	return w.dpi
}

// extendFrame keeps the system's shadow and border around the custom frame and makes the
// system ask the window for its frame again.
func (w *Window) extendFrame() {
	w.margins = w32.MARGINS{TopHeight: 1}
	w32.DwmExtendFrameIntoClientArea(w.hwnd, &w.margins)
	w32.SetWindowPos(w.hwnd, 0, 0, 0, 0, w32.SWP_NOMOVE|w32.SWP_NOSIZE|w32.SWP_NOZORDER|w32.SWP_NOACTIVATE|w32.SWP_FRAMECHANGED)
}

// frameProc handles the custom frame's messages; ok is false for those it leaves to the
// rest of the window procedure.
func (w *Window) frameProc(msg uint32, wp, lp uintptr) (uintptr, bool) {
	switch msg {
	case w32.WM_NCCALCSIZE:
		if wp == 0 {
			return 0, false
		}
		return w.ncCalcSize(lp), true
	case w32.WM_NCHITTEST:
		return w.hitTest(screenPoint(lp)), true
	case w32.WM_NCMOUSEMOVE:
		w.ncMouseMove(wp, screenPoint(lp))
		switch wp {
		case w32.HTMINBUTTON, w32.HTMAXBUTTON, w32.HTCLOSE:
			return 0, true // not DefWindowProc: see the frame's notes (Snap Layouts)
		}
		return 0, false
	case w32.WM_NCMOUSELEAVE:
		w.ncTracking = false
		w.pointerMaybeLeft()
		return 0, false
	case w32.WM_NCLBUTTONDOWN, w32.WM_NCLBUTTONDBLCLK:
		p := screenPoint(lp)
		switch wp {
		case w32.HTMINBUTTON, w32.HTMAXBUTTON, w32.HTCLOSE:
			w.ncPress(kit.PointerDown, p)
			return 0, true
		case w32.HTCAPTION:
			// The system drags the window (a modal loop that returns at the release) or
			// maximises it on a double click; the root hears a press on the ground.
			w.ncPress(kit.PointerDown, p)
			r := w32.DefWindowProc(w.hwnd, msg, wp, lp)
			w.ncPress(kit.PointerUp, p)
			return r, true
		}
	case w32.WM_NCLBUTTONUP:
		switch wp {
		case w32.HTMINBUTTON, w32.HTMAXBUTTON, w32.HTCLOSE:
			w.ncPress(kit.PointerUp, screenPoint(lp))
			return 0, true
		}
	}
	return 0, false
}

func screenPoint(lp uintptr) w32.Point {
	return w32.Point{X: int32(int16(lp)), Y: int32(int16(lp >> 16))}
}

// ncCalcSize is WM_NCCALCSIZE with wParam TRUE: the system's borders except the top.
func (w *Window) ncCalcSize(lp uintptr) uintptr {
	p := (*w32.NCCALCSIZE_PARAMS)(com.Addr(lp))
	top := p.Rgrc[0].Top
	r := w32.DefWindowProc(w.hwnd, w32.WM_NCCALCSIZE, 1, lp)
	p.Rgrc[0].Top = top
	if w32.IsZoomed(w.hwnd) {
		p.Rgrc[0].Top += frameY(w.sysDPI())
		w.autoHideInset(&p.Rgrc[0])
	}
	return r
}

// autoHideInset leaves a line of a maximised window's client area free on the edges of its
// monitor where an auto-hide taskbar waits, so that moving the pointer there shows it. It
// reads the App's cache (autohide.go), never the taskbar itself.
func (w *Window) autoHideInset(rc *w32.Rect) {
	mon := w32.MonitorFromWindow(w.hwnd, w32.MONITOR_DEFAULTTONEAREST)
	mi, ok := w32.GetMonitorInfo(mon)
	if !ok {
		return
	}
	if edges, ok := w.app.autoHide.edges(mon, mi.Monitor); ok {
		insetForAutoHide(rc, mi.Monitor, edges)
	}
}

// insetForAutoHide takes one pixel off each edge of rc that lies on the monitor's edge
// where an auto-hide bar is (edges in ABE_ order).
func insetForAutoHide(rc *w32.Rect, mon w32.Rect, edges [4]bool) {
	if edges[w32.ABE_LEFT] && rc.Left <= mon.Left {
		rc.Left = mon.Left + 1
	}
	if edges[w32.ABE_TOP] && rc.Top <= mon.Top {
		rc.Top = mon.Top + 1
	}
	if edges[w32.ABE_RIGHT] && rc.Right >= mon.Right {
		rc.Right = mon.Right - 1
	}
	if edges[w32.ABE_BOTTOM] && rc.Bottom >= mon.Bottom {
		rc.Bottom = mon.Bottom - 1
	}
}

// hitTest is WM_NCHITTEST's answer at screen point p.
func (w *Window) hitTest(p w32.Point) uintptr {
	wr := w32.GetWindowRect(w.hwnd)
	if p.X < wr.Left || p.X >= wr.Right || p.Y < wr.Top || p.Y >= wr.Bottom {
		return w32.HTNOWHERE
	}
	o := w32.ClientToScreen(w.hwnd, w32.Point{})
	rc := w32.GetClientRect(w.hwnd)
	x, y := p.X-o.X, p.Y-o.Y
	if !w32.IsZoomed(w.hwnd) && !w32.IsIconic(w.hwnd) {
		if code := resizeHit(x, y, rc.Right, rc.Bottom, frameX(w.sysDPI()), frameY(w.sysDPI())); code != w32.HTCLIENT {
			return code
		}
	}
	if x < 0 || y < 0 || x >= rc.Right || y >= rc.Bottom {
		return w32.HTNOWHERE // a maximised window's borders, off its monitor
	}
	return w.partAt(x, y)
}

// resizeHit is the resize code of client point (x, y) of a restored window whose client area
// is cw × ch pixels with borders fx and fy outside it (and a band fy thick inside its top),
// or HTCLIENT. Corners reach twice the border along each edge, as the system's do.
func resizeHit(x, y, cw, ch, fx, fy int32) uintptr {
	left, right, bottom, top := x < 0, x >= cw, y >= ch, y < fy
	nearLeft, nearRight := x < 2*fx, x >= cw-2*fx
	nearTop, nearBottom := y < 2*fy, y >= ch-2*fy
	switch {
	case top && (left || nearLeft), left && nearTop:
		return w32.HTTOPLEFT
	case top && (right || nearRight), right && nearTop:
		return w32.HTTOPRIGHT
	case bottom && (left || nearLeft), left && nearBottom:
		return w32.HTBOTTOMLEFT
	case bottom && (right || nearRight), right && nearBottom:
		return w32.HTBOTTOMRIGHT
	case left:
		return w32.HTLEFT
	case right:
		return w32.HTRIGHT
	case bottom:
		return w32.HTBOTTOM
	case top:
		return w32.HTTOP
	}
	return w32.HTCLIENT
}

// partAt asks the root what client point (x, y) pixels is. It may be called while a call
// into the root is in progress, as the kit allows for a modal loop the root entered.
func (w *Window) partAt(x, y int32) uintptr {
	if !w.attached || w.closed {
		return w32.HTCLIENT
	}
	ppd := w.ppd()
	w.depth++
	part := w.root.FrameHit(kit.Pt(float32(x)/ppd, float32(y)/ppd))
	w.depth--
	w.drain()
	switch part {
	case kit.FrameCaption:
		return w32.HTCAPTION
	case kit.FrameMinimize:
		return w32.HTMINBUTTON
	case kit.FrameMaximize:
		return w32.HTMAXBUTTON
	case kit.FrameClose:
		return w32.HTCLOSE
	}
	return w32.HTCLIENT
}

// clientDIP is a screen point in the client area's DIPs.
func (w *Window) clientDIP(p w32.Point) kit.Point {
	c := w32.ScreenToClient(w.hwnd, p)
	ppd := w.ppd()
	return kit.Pt(float32(c.X)/ppd, float32(c.Y)/ppd)
}

// ncMouseMove is a move over the frame: over the caption and the buttons the root hears it
// as a move over its title bar; over a resize border the pointer has left what the root
// sees.
func (w *Window) ncMouseMove(code uintptr, p w32.Point) {
	switch code {
	case w32.HTCAPTION, w32.HTMINBUTTON, w32.HTMAXBUTTON, w32.HTCLOSE:
	default:
		w.pointerMaybeLeft()
		return
	}
	if !w.ncTracking {
		w.tme = w32.TRACKMOUSEEVENT{Flags: w32.TME_LEAVE | w32.TME_NONCLIENT, Track: w.hwnd}
		w.tme.Size = uint32(unsafe.Sizeof(w.tme))
		w32.TrackMouseEvent(&w.tme)
		w.ncTracking = true
	}
	pos := w.clientDIP(p)
	w.pointerEnter(pos)
	w.event(kit.PointerEvent{Action: kit.PointerMove, Pos: pos, Buttons: w.buttons, Mods: modsOf(0)})
}

// ncPress is a left press or release over the caption or a window button.
func (w *Window) ncPress(a kit.PointerAction, p w32.Point) {
	pos := w.clientDIP(p)
	w.pointerEnter(pos)
	e := kit.PointerEvent{Action: a, Pos: pos, Button: kit.ButtonLeft, Mods: modsOf(0)}
	if a == kit.PointerDown {
		w.buttons |= kit.ButtonLeft
		e.Clicks = w.clicks(kit.ButtonLeft, w32.ScreenToClient(w.hwnd, p))
	} else {
		w.buttons &^= kit.ButtonLeft
	}
	e.Buttons = w.buttons
	w.event(e)
}

// pointerEnter tells the root the pointer came over the window, once.
func (w *Window) pointerEnter(pos kit.Point) {
	if w.inside {
		return
	}
	w.inside = true
	w.event(kit.PointerEvent{Action: kit.PointerEnter, Pos: pos, Buttons: w.buttons, Mods: modsOf(0)})
}

// pointerMaybeLeft tells the root the pointer left the window, unless it only moved
// between the parts the root sees (the client area, the caption and its buttons, a hosted
// field), whose own moves follow.
func (w *Window) pointerMaybeLeft() {
	if !w.inside || w.captured || w.overWindow() {
		return
	}
	w.inside = false
	w.event(kit.PointerEvent{Action: kit.PointerLeave, Buttons: w.buttons, Mods: modsOf(0)})
}

// pointerOverWindow reports whether the system's pointer is over the window's client area,
// caption or buttons, or over a field it hosts.
func (w *Window) pointerOverWindow() bool {
	p, ok := w32.GetCursorPos()
	if !ok {
		return false
	}
	h := w32.WindowFromPoint(p)
	switch {
	case h == 0:
		return false
	case h != w.hwnd:
		return w32.GetAncestor(h, w32.GA_ROOT) == w.hwnd // a hosted field
	case !w.custom:
		return w.hitTestClient(p)
	}
	switch w.hitTest(p) {
	case w32.HTCLIENT, w32.HTCAPTION, w32.HTMINBUTTON, w32.HTMAXBUTTON, w32.HTCLOSE:
		return true
	}
	return false
}

// hitTestClient reports whether screen point p is in a system-framed window's client area.
func (w *Window) hitTestClient(p w32.Point) bool {
	c := w32.ScreenToClient(w.hwnd, p)
	rc := w32.GetClientRect(w.hwnd)
	return c.X >= 0 && c.Y >= 0 && c.X < rc.Right && c.Y < rc.Bottom
}
