//go:build windows

package win

import (
	"unsafe"

	"downex/ui/kit"
	"downex/ui/win/internal/w32"
)

// input turns the mouse and keyboard messages into kit events; ok is false for messages it
// leaves to DefWindowProc.
func (w *Window) input(msg uint32, wp, lp uintptr) (ret uintptr, ok bool) {
	switch msg {
	case w32.WM_MOUSEMOVE:
		if !w.tracking {
			w.tme = w32.TRACKMOUSEEVENT{Flags: w32.TME_LEAVE, Track: w.hwnd}
			w.tme.Size = uint32(unsafe.Sizeof(w.tme))
			w32.TrackMouseEvent(&w.tme)
			w.tracking = true
		}
		w.pointerEnter(w.dip(lp))
		w.event(kit.PointerEvent{Action: kit.PointerMove, Pos: w.dip(lp), Buttons: buttonsOf(wp), Mods: modsOf(wp)})
		return 0, true
	case w32.WM_MOUSELEAVE:
		w.tracking = false
		w.pointerMaybeLeft()
		return 0, true
	case w32.WM_LBUTTONDOWN, w32.WM_LBUTTONDBLCLK, w32.WM_RBUTTONDOWN, w32.WM_MBUTTONDOWN, w32.WM_XBUTTONDOWN:
		b := buttonOf(msg, wp)
		w.buttons = buttonsOf(wp)
		pt := w32.Point{X: int32(int16(lp)), Y: int32(int16(lp >> 16))}
		w.event(kit.PointerEvent{Action: kit.PointerDown, Pos: w.dip(lp), Button: b, Buttons: w.buttons, Mods: modsOf(wp), Clicks: w.clicks(b, pt)})
		if msg == w32.WM_XBUTTONDOWN {
			return 1, true
		}
		return 0, true
	case w32.WM_LBUTTONUP, w32.WM_RBUTTONUP, w32.WM_MBUTTONUP, w32.WM_XBUTTONUP:
		b := buttonOf(msg, wp)
		w.buttons = buttonsOf(wp)
		w.event(kit.PointerEvent{Action: kit.PointerUp, Pos: w.dip(lp), Button: b, Buttons: w.buttons, Mods: modsOf(wp)})
		if w.captured && w.buttons == 0 {
			// Capture ends by itself with the last button, as DOM pointer capture does.
			w.releasing = true
			w32.ReleaseCapture()
			w.releasing = false
			w.captured = false
			if !w.tracking && !w.ncTracking {
				w.pointerMaybeLeft() // released outside the window
			}
		}
		if msg == w32.WM_XBUTTONUP {
			return 1, true
		}
		return 0, true
	case w32.WM_CAPTURECHANGED:
		if w.captured && !w.releasing {
			w.captured = false
			w.event(kit.PointerEvent{Action: kit.PointerCancel, Buttons: w.buttons, Mods: modsOf(0)})
		}
		return 0, true
	case w32.WM_MOUSEWHEEL, w32.WM_MOUSEHWHEEL:
		delta := float32(int16(wp>>16)) / w32.WHEEL_DELTA
		p := w32.ScreenToClient(w.hwnd, w32.Point{X: int32(int16(lp)), Y: int32(int16(lp >> 16))})
		e := kit.WheelEvent{Pos: kit.Pt(float32(p.X)/w.ppd(), float32(p.Y)/w.ppd()), Mods: modsOf(wp & 0xFFFF)}
		lines := w.app.wheelLines
		if msg == w32.WM_MOUSEHWHEEL {
			lines = w.app.wheelChars
		}
		switch {
		case lines == w32.WHEEL_PAGESCROLL:
			e.Mode = kit.WheelPages
		case int16(wp>>16)%w32.WHEEL_DELTA == 0:
			e.Mode = kit.WheelLines
			delta *= float32(lines) * 100 / 3
		default:
			e.Mode = kit.WheelPrecise
			delta *= float32(lines) * 100 / 3
		}
		if msg == w32.WM_MOUSEWHEEL {
			e.Delta.Y = -delta // forward (away from the user) scrolls up: the DOM's negative
		} else {
			e.Delta.X = delta
		}
		w.event(e)
		return 0, true
	case w32.WM_KEYDOWN, w32.WM_SYSKEYDOWN:
		if wp == w32.VK_PROCESSKEY {
			return 0, false // the input method has it
		}
		e := kit.KeyEvent{Action: kit.KeyDown, Key: keyOf(wp), Mods: modsOf(0), Repeat: lp&(1<<30) != 0}
		if w.depth > 0 {
			w.event(e) // a modal loop: queued, and the system acts on it now
			return 0, false
		}
		if w.event(e) {
			w.keyUsed = true
			return 0, true
		}
		return 0, false
	case w32.WM_KEYUP, w32.WM_SYSKEYUP:
		e := kit.KeyEvent{Action: kit.KeyUp, Key: keyOf(wp), Mods: modsOf(0)}
		if w.event(e) && w.depth == 0 {
			return 0, true
		}
		return 0, false
	case w32.WM_CHAR:
		w.char(uint16(wp))
		return 0, true
	case w32.WM_SYSCHAR:
		// Alt+Space opens the system menu; other Alt+letters would only beep (no menu bar).
		if wp == ' ' {
			return 0, false
		}
		return 0, true
	}
	return 0, false
}

// char sends the text of a WM_CHAR, joining surrogate pairs; control characters are keys
// only.
func (w *Window) char(u uint16) {
	switch {
	case u >= 0xD800 && u < 0xDC00:
		w.surrogate = u
		return
	case u >= 0xDC00 && u < 0xE000:
		if w.surrogate != 0 {
			r := utf16Decode(w.surrogate, u)
			w.surrogate = 0
			w.event(kit.TextEvent{Text: string(r)})
		}
		return
	}
	w.surrogate = 0
	if u < 0x20 || u == 0x7F {
		return
	}
	w.event(kit.TextEvent{Text: string(rune(u))})
}

// dip is a mouse message's client point in DIPs (signed: outside while captured).
func (w *Window) dip(lp uintptr) kit.Point {
	x, y := int16(lp), int16(lp>>16)
	return kit.Pt(float32(x)/w.ppd(), float32(y)/w.ppd())
}

// clicks counts the presses of a button in a row within the double-click time and box.
func (w *Window) clicks(b kit.Buttons, pt w32.Point) int {
	t := w32.GetMessageTime()
	dx := w32.GetSystemMetricsForDpi(w32.SM_CXDOUBLECLK, w.dpi) / 2
	dy := w32.GetSystemMetricsForDpi(w32.SM_CYDOUBLECLK, w.dpi) / 2
	c := &w.click
	if c.n > 0 && c.button == b && uint32(t-c.time) <= w32.GetDoubleClickTime() &&
		abs32(pt.X-c.pt.X) <= dx && abs32(pt.Y-c.pt.Y) <= dy {
		c.n++
	} else {
		c.n = 1
	}
	c.button, c.time, c.pt = b, t, pt
	return c.n
}

func abs32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}

func buttonOf(msg uint32, wp uintptr) kit.Buttons {
	switch msg {
	case w32.WM_LBUTTONDOWN, w32.WM_LBUTTONDBLCLK, w32.WM_LBUTTONUP:
		return kit.ButtonLeft
	case w32.WM_RBUTTONDOWN, w32.WM_RBUTTONUP:
		return kit.ButtonRight
	case w32.WM_MBUTTONDOWN, w32.WM_MBUTTONUP:
		return kit.ButtonMiddle
	}
	if wp>>16 == 2 { // XBUTTON2
		return kit.ButtonForward
	}
	return kit.ButtonBack
}

// buttonsOf is the buttons held, from a mouse message's MK_ flags.
func buttonsOf(wp uintptr) kit.Buttons {
	var b kit.Buttons
	if wp&w32.MK_LBUTTON != 0 {
		b |= kit.ButtonLeft
	}
	if wp&w32.MK_RBUTTON != 0 {
		b |= kit.ButtonRight
	}
	if wp&w32.MK_MBUTTON != 0 {
		b |= kit.ButtonMiddle
	}
	if wp&w32.MK_XBUTTON1 != 0 {
		b |= kit.ButtonBack
	}
	if wp&w32.MK_XBUTTON2 != 0 {
		b |= kit.ButtonForward
	}
	return b
}

// modsOf is the modifier keys held: Shift and Ctrl from a mouse message's flags when it has
// them, else from the keyboard state, and Alt and the Windows keys from the keyboard state.
func modsOf(wp uintptr) kit.Modifiers {
	var m kit.Modifiers
	if wp&w32.MK_SHIFT != 0 || w32.KeyDown(w32.VK_SHIFT) {
		m |= kit.ModShift
	}
	if wp&w32.MK_CONTROL != 0 || w32.KeyDown(w32.VK_CONTROL) {
		m |= kit.ModCtrl
	}
	if w32.KeyDown(w32.VK_MENU) {
		m |= kit.ModAlt
	}
	if w32.KeyDown(w32.VK_LWIN) || w32.KeyDown(w32.VK_RWIN) {
		m |= kit.ModMeta
	}
	return m
}

// keyOf is the kit's key of a virtual-key code: Windows' own, with the right Windows key
// read as the left one.
func keyOf(vk uintptr) kit.Key {
	if vk == w32.VK_RWIN {
		return kit.KeyMeta
	}
	return kit.Key(vk & 0xFFFF)
}
