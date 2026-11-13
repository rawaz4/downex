//go:build windows

package win

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unsafe"

	"downex/ui/kit"
	"downex/ui/win/internal/w32"
)

// frameBar is a kit root with a title bar like the main window's: its ground is the
// caption, a field-like client control sits in it, and the three window buttons are
// buttons that are frame parts. The buttons record their presses instead of acting.
type frameBar struct {
	win     *kit.Window
	bar     *kit.Element
	search  *kit.Pressable
	buttons [3]*kit.Pressable // minimise, maximise, close
	presses []string
	events  []kit.PointerEvent
}

func newFrameBar() *frameBar {
	fb := &frameBar{}
	fb.bar = &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, Height: kit.Px(44), AlignItems: kit.AlignCenter}}
	fb.bar.Frame = kit.FrameCaption
	spacer := func() *kit.Element { return &kit.Element{Style: kit.Style{Flex: kit.Flex1}} }
	fb.search = kit.NewPressable(func() { fb.presses = append(fb.presses, "search") })
	fb.search.Style = kit.Style{Width: kit.Px(120), Height: kit.Px(28)}
	fb.bar.Add(spacer(), fb.search, spacer())
	for i, name := range []string{"minimise", "maximise", "close"} {
		name := name
		b := kit.NewPressable(func() { fb.presses = append(fb.presses, name) })
		b.Style = kit.Style{Width: kit.Px(46), Height: kit.Px(44), AlignSelf: kit.AlignStretch}
		b.Frame = []kit.FramePart{kit.FrameMinimize, kit.FrameMaximize, kit.FrameClose}[i]
		fb.buttons[i] = b
		fb.bar.Add(b)
	}
	page := &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, Axis: kit.AxisColumn}}
	page.Add(fb.bar, &kit.Element{Style: kit.Style{Flex: kit.Flex1}})
	fb.win = kit.NewWindow(page, kit.WindowOptions{Title: "Downex frame test", MinSize: kit.Size{W: 300, H: 160}})
	return fb
}

// openFrame opens a custom-frame window for a kit root on the test's locked thread.
func openFrame(t *testing.T, root kit.Root, size kit.Size) (*App, *Window) {
	t.Helper()
	runtimeLock(t)
	a, err := NewApp(AppOptions{ClassName: "DownexFrameTest"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	mon := w32.MonitorFromPoint(w32.Point{}, w32.MONITOR_DEFAULTTOPRIMARY)
	mi, _ := w32.GetMonitorInfo(mon)
	w, err := a.NewWindow(root, WindowOptions{
		Size: size, CustomFrame: true, NoActivate: true, Placed: true,
		At: w32.Point{X: mi.Work.Left + 60, Y: mi.Work.Top + 60},
	})
	if err != nil {
		t.Fatal(err)
	}
	w.overWindow = func() bool { return false } // the real pointer is the owner's, elsewhere
	pump(t, a, w, "the first frame", 2*time.Second, func() bool { return w.painted })
	return a, w
}

// The custom frame keeps the system's side and bottom borders and gives the top to the
// client area, whose size is exactly the one asked for in DIPs.
func TestCustomFrameGeometry(t *testing.T) {
	fb := newFrameBar()
	_, w := openFrame(t, fb.win, kit.Size{W: 600, H: 300})
	ppd := w.ppd()
	rc := w32.GetClientRect(w.hwnd)
	if want := (w32.Point{X: w.pixels(600), Y: w.pixels(300)}); rc.Right != want.X || rc.Bottom != want.Y {
		t.Fatalf("client %dx%d px, want %dx%d (600x300 DIPs at %.2f)", rc.Right, rc.Bottom, want.X, want.Y, ppd)
	}
	wr := w32.GetWindowRect(w.hwnd)
	o := w32.ClientToScreen(w.hwnd, w32.Point{})
	fx, fy := frameX(w.sysDPI()), frameY(w.sysDPI())
	if o.Y != wr.Top {
		t.Errorf("the client area starts %d px below the window's top: the custom frame has no top", o.Y-wr.Top)
	}
	if o.X-wr.Left != fx || wr.Right-(o.X+rc.Right) != fx || wr.Bottom-(o.Y+rc.Bottom) != fy {
		t.Errorf("borders left %d right %d bottom %d, want %d %d %d", o.X-wr.Left, wr.Right-(o.X+rc.Right), wr.Bottom-(o.Y+rc.Bottom), fx, fx, fy)
	}
	if ww, wh := w.outerSize(rc.Right, rc.Bottom, w.dpi); ww != wr.Right-wr.Left || wh != wr.Bottom-wr.Top {
		t.Errorf("outerSize %dx%d, window %dx%d", ww, wh, wr.Right-wr.Left, wr.Bottom-wr.Top)
	}
	// WM_GETMINMAXINFO asks for the minimum client size with the custom frame around it: the
	// system's frame at the monitor's DPI without its top, computed here from the style.
	mmi := heapMMI
	*mmi = w32.MINMAXINFO{}
	w.proc(w32.WM_GETMINMAXINFO, 0, uintptr(unsafe.Pointer(mmi)))
	want := w32.Rect{Right: int32(300*ppd + 0.5), Bottom: int32(160*ppd + 0.5)}
	w32.AdjustWindowRectExForDpi(&want, w32.WS_OVERLAPPEDWINDOW|w32.WS_CLIPCHILDREN, w32.WS_EX_NOACTIVATE, w32.GetDpiForWindow(w.hwnd))
	if mw, mh := want.Right-want.Left, want.Bottom; mmi.MinTrackSize.X != mw || mmi.MinTrackSize.Y != mh {
		t.Errorf("min track %v, want %dx%d", mmi.MinTrackSize, mw, mh)
	}
	// WM_GETDPISCALEDSIZE keeps the DIP size exact on a monitor of another DPI.
	for _, dpi := range []uint32{96, 120, 144, 192} {
		sz := heapPoint
		*sz = w32.Point{}
		if send(w, w32.WM_GETDPISCALEDSIZE, uintptr(dpi), uintptr(unsafe.Pointer(sz))) != 1 {
			t.Fatalf("WM_GETDPISCALEDSIZE(%d) not answered", dpi)
		}
		k := float64(dpi) / 96
		rc := w32.Rect{Right: int32(600*k + 0.5), Bottom: int32(300*k + 0.5)}
		w32.AdjustWindowRectExForDpi(&rc, windowStyle, 0, dpi)
		if sz.X != rc.Right-rc.Left || sz.Y != rc.Bottom {
			t.Errorf("WM_GETDPISCALEDSIZE(%d) = %v, want %dx%d (600x300 DIPs and the frame at that DPI, no top)", dpi, *sz, rc.Right-rc.Left, rc.Bottom)
		}
	}
}

// The hit-test table: resize borders and corners outside and along the top, the root's
// caption and buttons, the client area.
func TestCustomFrameHitTest(t *testing.T) {
	fb := newFrameBar()
	_, w := openFrame(t, fb.win, kit.Size{W: 600, H: 300})
	wr := w32.GetWindowRect(w.hwnd)
	o := w32.ClientToScreen(w.hwnd, w32.Point{})
	ppd := w.ppd()
	at := func(x, y float32) w32.Point { // client DIPs to screen pixels
		return w32.Point{X: o.X + int32(x*ppd), Y: o.Y + int32(y*ppd)}
	}
	fx, fy := frameX(w.sysDPI()), frameY(w.sysDPI())
	cw := w32.GetClientRect(w.hwnd).Right
	ch := w32.GetClientRect(w.hwnd).Bottom
	for _, c := range []struct {
		name string
		p    w32.Point
		want uintptr
	}{
		{"maximise", at(600-46-46+20, 22), w32.HTMAXBUTTON},
		{"minimise", at(600-46*3+20, 22), w32.HTMINBUTTON},
		{"close", at(600-20, 22), w32.HTCLOSE},
		{"caption", at(100, 22), w32.HTCAPTION},
		{"control in the bar", at(231, 22), w32.HTCLIENT},
		{"client", at(300, 200), w32.HTCLIENT},
		{"left", w32.Point{X: wr.Left + 1, Y: o.Y + ch/2}, w32.HTLEFT},
		{"right", w32.Point{X: wr.Right - 2, Y: o.Y + ch/2}, w32.HTRIGHT},
		{"bottom", w32.Point{X: o.X + cw/2, Y: wr.Bottom - 2}, w32.HTBOTTOM},
		{"top", w32.Point{X: o.X + cw/2, Y: wr.Top + 1}, w32.HTTOP},
		{"top inside the band", w32.Point{X: o.X + cw/2, Y: o.Y + fy - 1}, w32.HTTOP},
		{"below the band", w32.Point{X: o.X + cw/2, Y: o.Y + fy}, w32.HTCAPTION},
		{"top-left", w32.Point{X: wr.Left + 1, Y: wr.Top + 1}, w32.HTTOPLEFT},
		{"top-right", w32.Point{X: wr.Right - 2, Y: wr.Top + 1}, w32.HTTOPRIGHT},
		{"bottom-left", w32.Point{X: wr.Left + 1, Y: wr.Bottom - 2}, w32.HTBOTTOMLEFT},
		{"bottom-right", w32.Point{X: wr.Right - 2, Y: wr.Bottom - 2}, w32.HTBOTTOMRIGHT},
		{"top edge near the left corner", w32.Point{X: o.X + fx, Y: wr.Top + 1}, w32.HTTOPLEFT},
		{"outside", w32.Point{X: wr.Right + 5, Y: wr.Top + 5}, w32.HTNOWHERE},
	} {
		lp := uintptr(uint16(int16(c.p.X))) | uintptr(uint16(int16(c.p.Y)))<<16
		if got := send(w, w32.WM_NCHITTEST, 0, lp); got != c.want {
			t.Errorf("%s at %v: hit %d, want %d", c.name, c.p, got, c.want)
		}
	}
}

func TestResizeHitTable(t *testing.T) {
	const cw, ch, f = 1000, 600, 9
	for _, c := range []struct {
		x, y int32
		want uintptr
	}{
		{-3, 300, w32.HTLEFT}, {1003, 300, w32.HTRIGHT}, {500, 603, w32.HTBOTTOM}, {500, 2, w32.HTTOP},
		{500, 9, w32.HTCLIENT}, {-3, 5, w32.HTTOPLEFT}, {10, 2, w32.HTTOPLEFT}, {17, 2, w32.HTTOPLEFT},
		{18, 2, w32.HTTOP}, {-3, 17, w32.HTTOPLEFT}, {-3, 18, w32.HTLEFT}, {1003, 595, w32.HTBOTTOMRIGHT},
		{-3, 590, w32.HTBOTTOMLEFT}, {990, 603, w32.HTBOTTOMRIGHT}, {500, 300, w32.HTCLIENT},
	} {
		if got := resizeHit(c.x, c.y, cw, ch, f, f); got != c.want {
			t.Errorf("resizeHit(%d, %d) = %d, want %d", c.x, c.y, got, c.want)
		}
	}
}

func TestAutoHideInset(t *testing.T) {
	mon := w32.Rect{Left: 0, Top: 0, Right: 2560, Bottom: 1600}
	rc := mon
	insetForAutoHide(&rc, mon, [4]bool{w32.ABE_BOTTOM: true})
	if rc != (w32.Rect{Right: 2560, Bottom: 1599}) {
		t.Errorf("bottom auto-hide: %v", rc)
	}
	rc = w32.Rect{Left: 0, Top: 0, Right: 2560, Bottom: 1540} // a work area that keeps a taskbar
	insetForAutoHide(&rc, mon, [4]bool{w32.ABE_BOTTOM: true, w32.ABE_LEFT: true})
	if rc != (w32.Rect{Left: 1, Right: 2560, Bottom: 1540}) {
		t.Errorf("left auto-hide on a work area: %v", rc)
	}
}

// The window buttons: over a button the root hears moves (its hover), a press and a release
// there click it; a press on Close released over Maximise does nothing; a press on the
// caption is a press on the bar's ground, and the system's double click is left to it.
func TestCustomFrameButtons(t *testing.T) {
	fb := newFrameBar()
	a, w := openFrame(t, fb.win, kit.Size{W: 600, H: 300})
	o := w32.ClientToScreen(w.hwnd, w32.Point{})
	ppd := w.ppd()
	screen := func(x, y float32) uintptr {
		return uintptr(uint16(int16(o.X+int32(x*ppd)))) | uintptr(uint16(int16(o.Y+int32(y*ppd))))<<16
	}
	client := func(x, y float32) uintptr { return lparam(int(x*ppd), int(y*ppd)) }
	maxX, closeX := float32(600-46-46+20), float32(600-20)

	send(w, w32.WM_NCMOUSEMOVE, w32.HTMAXBUTTON, screen(maxX, 22))
	if !fb.buttons[1].Hovered() {
		t.Error("a move over the maximise button did not hover it")
	}
	if !w.inside {
		t.Error("no PointerEnter for a move over the frame")
	}
	send(w, w32.WM_NCLBUTTONDOWN, w32.HTMAXBUTTON, screen(maxX, 22))
	if !fb.buttons[1].Down() || w32.GetCapture() != w.hwnd {
		t.Fatalf("a press on maximise: down %v, captured %v", fb.buttons[1].Down(), w32.GetCapture() == w.hwnd)
	}
	// The capture sends the release as a client message.
	send(w, w32.WM_LBUTTONUP, 0, client(maxX, 22))
	if fmt.Sprint(fb.presses) != "[maximise]" {
		t.Errorf("press and release on maximise: %v", fb.presses)
	}
	if w32.GetCapture() == w.hwnd {
		t.Error("the capture outlived the release")
	}

	// Pressed on Close, released on Maximise: nothing.
	fb.presses = nil
	send(w, w32.WM_NCMOUSEMOVE, w32.HTCLOSE, screen(closeX, 22))
	send(w, w32.WM_NCLBUTTONDOWN, w32.HTCLOSE, screen(closeX, 22))
	send(w, w32.WM_MOUSEMOVE, w32.MK_LBUTTON, client(maxX, 22))
	if fb.buttons[2].Down() {
		t.Error("close still shows pressed with the pointer over maximise")
	}
	send(w, w32.WM_LBUTTONUP, 0, client(maxX, 22))
	if len(fb.presses) != 0 {
		t.Errorf("pressed on close, released on maximise: %v", fb.presses)
	}

	// Something else takes the capture during a press (a system menu): the press is
	// cancelled, and the release that follows as a non-client message clicks nothing.
	send(w, w32.WM_NCLBUTTONDOWN, w32.HTMINBUTTON, screen(600-46*3+20, 22))
	w32.ReleaseCapture()
	send(w, w32.WM_NCLBUTTONUP, w32.HTMINBUTTON, screen(600-46*3+20, 22))
	if len(fb.presses) != 0 || fb.buttons[0].Down() {
		t.Errorf("a press whose capture was taken: presses %v, down %v", fb.presses, fb.buttons[0].Down())
	}

	// The pointer leaves the frame for good: PointerLeave, the hover ends.
	send(w, w32.WM_NCMOUSEMOVE, w32.HTMAXBUTTON, screen(maxX, 22))
	send(w, w32.WM_NCMOUSELEAVE, 0, 0)
	if w.inside || fb.buttons[1].Hovered() {
		t.Errorf("after WM_NCMOUSELEAVE: inside %v, maximise hovered %v", w.inside, fb.buttons[1].Hovered())
	}
	// Moving from a button into the client area is not a leave.
	send(w, w32.WM_NCMOUSEMOVE, w32.HTMAXBUTTON, screen(maxX, 22))
	w.overWindow = func() bool { return true }
	send(w, w32.WM_NCMOUSELEAVE, 0, 0)
	send(w, w32.WM_MOUSEMOVE, 0, client(300, 150))
	if !w.inside || fb.buttons[1].Hovered() {
		t.Errorf("button to client: inside %v, maximise hovered %v", w.inside, fb.buttons[1].Hovered())
	}
	w.overWindow = func() bool { return false }

	// Commands reach the system as the title bar's buttons ask (posted, so the root's call
	// has returned): here only the restore of a window that is not maximised, a no-op.
	w.Command(kit.WindowRestore)
	pumpFor(t, a, w, 100*time.Millisecond, func() bool { return false })
	if w32.IsZoomed(w.hwnd) || w32.IsIconic(w.hwnd) {
		t.Error("WindowRestore changed a restored window")
	}
}

// The root's title bar hears a press on the caption as a press on its ground, before the
// system's drag; the window procedure passes the press on (DefWindowProc returns at once
// for a message sent without a real button held).
func TestCaptionPressReachesRoot(t *testing.T) {
	root := &testRoot{}
	root.frameHit = func(p kit.Point) kit.FramePart {
		if p.Y < 44 {
			return kit.FrameCaption
		}
		return kit.FrameClient
	}
	runtimeLock(t)
	a, err := NewApp(AppOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	root.t = t
	mon := w32.MonitorFromPoint(w32.Point{}, w32.MONITOR_DEFAULTTOPRIMARY)
	mi, _ := w32.GetMonitorInfo(mon)
	w, err := a.NewWindow(root, WindowOptions{Size: kit.Size{W: 400, H: 200}, CustomFrame: true, NoActivate: true, Placed: true,
		At: w32.Point{X: mi.Work.Left + 60, Y: mi.Work.Top + 60}})
	if err != nil {
		t.Fatal(err)
	}
	w.overWindow = func() bool { return false }
	pump(t, a, w, "the first frame", 2*time.Second, func() bool { return w.painted })
	o := w32.ClientToScreen(w.hwnd, w32.Point{})
	lp := uintptr(uint16(int16(o.X+50))) | uintptr(uint16(int16(o.Y+10)))<<16
	if got := send(w, w32.WM_NCHITTEST, 0, lp); got != w32.HTCAPTION {
		t.Fatalf("hit %d over the caption", got)
	}
	root.events = nil
	w32.PostMessage(w.hwnd, w32.WM_NCLBUTTONDOWN, w32.HTCAPTION, lp)
	pumpFor(t, a, w, 300*time.Millisecond, func() bool {
		for _, e := range root.events {
			if p, ok := e.(kit.PointerEvent); ok && p.Action == kit.PointerUp {
				return true
			}
		}
		return false
	})
	var got []string
	for _, e := range root.events {
		if p, ok := e.(kit.PointerEvent); ok {
			got = append(got, fmt.Sprintf("%d", p.Action))
		}
	}
	if s := strings.Join(got, " "); !strings.Contains(s, fmt.Sprintf("%d %d", kit.PointerDown, kit.PointerUp)) {
		t.Errorf("a caption press reached the root as %q, want a down then an up", s)
	}
}
