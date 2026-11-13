//go:build windows

package win

import (
	"math"
	"unsafe"

	"downex/ui/kit"
	"downex/ui/kit/hostcap"
	"downex/ui/win/internal/w32"

	"golang.org/x/sys/windows"
)

// The windows' tooltips (hostcap.Tooltips): the system's own tooltip control (comctl32 v6's
// tooltips_class32, as the WebView window's title tooltips are the browser host's), one per
// window, as a tracking tool. The mouse hook follows the pointer over the window and its
// fields; once it has rested for the system's hover time (kit.Environment.HoverTime), the
// window's source gives the text at that point (kit.Window.TooltipAt: an element's Tooltip, or
// the whole text of a label an ellipsis cuts) and the tooltip shows under the pointer. It goes
// when the pointer leaves the source's area, a button, the wheel or a key goes down, or the
// window loses the activation.

var _ hostcap.Tooltips = (*Window)(nil)

// tipService is an App's tooltips, and the message-only window their timers come to.
type tipService struct {
	app     *App
	svc     w32.HWND
	byOwner map[*Window]*tipState
	byID    map[uintptr]*tipState
	next    uintptr
}

// tipState is one window's tooltip.
type tipState struct {
	s      *tipService
	w      *Window
	id     uintptr
	source func(kit.Point) (string, kit.Rect, bool)
	tip    w32.HWND
	ti     popToolInfo
	text   *uint16
	shown  bool
	area   kit.Rect // owner DIPs
	last   w32.Point
	timing bool
}

// popupService is the procedure of an App's message-only window of the popup class.
type popupService struct{ s *tipService }

var tipServices = map[*App]*tipService{}

// cursorPos is where the pointer is (tests stand in for it: no real pointer moves there).
var cursorPos = w32.GetCursorPos

var tooltipsClass = func() *uint16 { p, _ := windows.UTF16PtrFromString("tooltips_class32"); return p }()

// tips is the App's tooltip service.
func (a *App) tips() *tipService {
	s := tipServices[a]
	if s == nil {
		s = &tipService{app: a, byOwner: map[*Window]*tipState{}, byID: map[uintptr]*tipState{}}
		tipServices[a] = s
	}
	return s
}

// SetTooltipSource implements hostcap.Tooltips.
func (w *Window) SetTooltipSource(source func(p kit.Point) (string, kit.Rect, bool)) {
	s := w.app.tips()
	st := s.byOwner[w]
	switch {
	case source == nil && st != nil:
		s.hide(w)
		if st.tip != 0 {
			w32.DestroyWindow(st.tip)
		}
		delete(s.byOwner, w)
		delete(s.byID, st.id)
		w.app.hooks().release()
	case source != nil && st == nil:
		if s.svc == 0 && s.makeService() != nil {
			return
		}
		s.next++
		st = &tipState{s: s, w: w, id: s.next, source: source}
		s.byOwner[w], s.byID[st.id] = st, st
		w.app.hooks().retain()
	case source != nil:
		st.source = source
	}
}

func (s *tipService) makeService() error {
	if err := s.app.registerPopupClass(); err != nil {
		return err
	}
	h, err := w32.CreateWindowEx(0, popupClass, nil, 0, 0, 0, 0, 0, w32.HWND_MESSAGE, s.app.inst, 0)
	if err != nil {
		return err
	}
	s.svc = h
	popupTable.Lock()
	popupTable.service[h] = &popupService{s: s}
	popupTable.Unlock()
	return nil
}

func (ps *popupService) proc(h w32.HWND, msg uint32, wp, lp uintptr) uintptr {
	if msg == popWMTimer {
		killTimer(h, wp)
		if st := ps.s.byID[wp]; st != nil {
			st.timing = false
			st.due()
		}
		return 0
	}
	return w32.DefWindowProc(h, msg, wp, lp)
}

// moved follows a mouse move: away from a shown tooltip's area hides it; every move restarts
// the hover time.
func (s *tipService) moved(m *popMouseHook) {
	o := s.app.ownerOf(m.Hwnd)
	for w, st := range s.byOwner {
		if w != o && st.shown {
			st.hideTip()
		}
	}
	st := s.byOwner[o]
	if st == nil {
		return
	}
	if m.Pt == st.last && (st.timing || st.shown) {
		return // the same point (a move the system repeats)
	}
	st.last = m.Pt
	if st.shown && !st.area.Contains(st.point()) {
		st.hideTip()
	}
	if st.shown {
		return
	}
	hover := o.env.HoverTime
	if hover <= 0 {
		hover = 400e6
	}
	setTimer(s.svc, st.id, uint32(hover.Milliseconds()))
	st.timing = true
}

// point is the last pointer position in the owner's DIPs.
func (st *tipState) point() kit.Point {
	c := w32.ScreenToClient(st.w.hwnd, st.last)
	ppd := st.w.ppd()
	return kit.Pt(float32(c.X)/ppd, float32(c.Y)/ppd)
}

// due is the hover time over: the source says what to show there.
func (st *tipState) due() {
	if st.w.closed || st.source == nil {
		return
	}
	if pt, ok := cursorPos(); !ok || pt != st.last {
		return // the pointer moved on in the meantime
	}
	p := st.point()
	st.w.call(func() {
		text, area, ok := st.source(p)
		if ok && text != "" {
			st.showTip(text, area)
		}
	})
}

func (st *tipState) showTip(text string, area kit.Rect) {
	if st.tip == 0 {
		h, err := w32.CreateWindowEx(0x00000008 /* WS_EX_TOPMOST */, tooltipsClass, nil, w32.WS_POPUP|popTTSNoPrefix|popTTSAlwaysTip,
			0, 0, 0, 0, st.w.hwnd, st.w.app.inst, 0)
		if err != nil {
			return
		}
		st.tip = h
		st.ti = popToolInfo{Flags: popTTFTrack | popTTFAbsolute, Hwnd: st.w.hwnd, ID: 1}
		st.ti.Size = uint32(unsafe.Sizeof(st.ti))
		st.text, _ = windows.UTF16PtrFromString(" ")
		st.ti.Text = st.text
		w32.SendMessage(h, popTTMAddToolW, 0, uintptr(unsafe.Pointer(&st.ti)))
	}
	st.text, _ = windows.UTF16PtrFromString(stripNUL(text))
	st.ti.Text = st.text
	w32.SendMessage(st.tip, popTTMUpdateTipTextW, 0, uintptr(unsafe.Pointer(&st.ti)))
	if m := w32.MonitorFromWindow(st.w.hwnd, w32.MONITOR_DEFAULTTONEAREST); m != 0 {
		if mi, ok := w32.GetMonitorInfo(m); ok {
			w32.SendMessage(st.tip, popTTMSetMaxTipWidth, 0, uintptr((mi.Work.Right-mi.Work.Left)/2))
		}
	}
	// under the pointer: the cursor's height below its hot spot, less a third (its shape)
	dy := int32(math.Round(float64(getSystemMetrics(popSMCYCursor)) * 2 / 3))
	x, y := st.last.X, st.last.Y+dy
	w32.SendMessage(st.tip, popTTMTrackPosition, 0, uintptr(uint16(int16(x)))|uintptr(uint16(int16(y)))<<16)
	w32.SendMessage(st.tip, popTTMTrackActivate, 1, uintptr(unsafe.Pointer(&st.ti)))
	st.shown, st.area = true, area
}

func (st *tipState) hideTip() {
	if st.shown && st.tip != 0 {
		w32.SendMessage(st.tip, popTTMTrackActivate, 0, uintptr(unsafe.Pointer(&st.ti)))
	}
	st.shown = false
	if st.timing {
		killTimer(st.s.svc, st.id)
		st.timing = false
	}
}

// hide hides w's tooltip.
func (s *tipService) hide(w *Window) {
	if st := s.byOwner[w]; st != nil {
		st.hideTip()
	}
}

// hideAll hides every tooltip (a button, the wheel or a key went down).
func (s *tipService) hideAll() {
	for _, st := range s.byOwner {
		st.hideTip()
	}
}

// Shown reports whether w's tooltip shows, and its text (tests).
func (s *tipService) shownText(w *Window) (string, bool) {
	st := s.byOwner[w]
	if st == nil || !st.shown || st.text == nil {
		return "", false
	}
	return windows.UTF16PtrToString(st.text), true
}
