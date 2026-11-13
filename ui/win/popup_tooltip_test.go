//go:build windows

package win

import (
	"testing"
	"time"

	"downex/ui/kit"
	"downex/ui/kit/widgets"
	"downex/ui/win/internal/w32"
)

// A window with tooltips that is destroyed (closed, or with its App) releases its tooltip
// state and its hold on the thread's hooks: once the last such window is gone, the mouse,
// call-window and keyboard hooks are uninstalled (stage 1's re-check found them held for the
// App's life). A tooltip shown at the moment goes with the window.
func TestDestroyedWindowReleasesTooltipsAndHooks(t *testing.T) {
	long := "annual-report-2025-final-version-with-every-appendix.pdf"
	lbl := kit.NewLabel(long, widgets.FontBody, kit.TextOptions{Truncate: kit.TruncateEnd})
	lbl.Style = kit.Style{Width: kit.Px(120)}
	ww := openWidgets(t, "DownexTipCloseTest", false, kit.Size{W: 320, H: 120}, lbl)
	a, w := ww.a, ww.w
	if !widgets.EnableTooltips(ww.kw) {
		t.Fatal("the window has no tooltips")
	}
	h := hooksOf[a]
	if h == nil || h.refs != 1 || h.mouse == 0 || h.cwp == 0 || h.keyboard == 0 {
		t.Fatalf("tooltips on, hooks %+v", h)
	}
	// a second window with tooltips: the hooks stay until both are gone
	lbl2 := kit.NewLabel(long, widgets.FontBody, kit.TextOptions{Truncate: kit.TruncateEnd})
	lbl2.Style = kit.Style{Width: kit.Px(120)}
	scope2 := widgets.NewScope(&widgets.Choice{}, lbl2)
	kw2 := kit.NewWindow(scope2, kit.WindowOptions{Title: "Downex widget test 2"})
	w2, err := a.NewWindow(kw2, WindowOptions{Size: kit.Size{W: 320, H: 120}, NoActivate: true, Placed: true, At: w32.Point{X: 40, Y: 40}})
	if err != nil {
		t.Fatal(err)
	}
	w2.overWindow = func() bool { return false }
	pump(t, a, w2, "the second window's first frame", 2*time.Second, func() bool { return w2.painted })
	if !widgets.EnableTooltips(kw2) {
		t.Fatal("the second window has no tooltips")
	}
	if h.refs != 2 {
		t.Fatalf("two windows with tooltips hold %d references", h.refs)
	}

	// show the first window's tooltip, then close the window while it shows
	c := lbl.WindowBounds().Center()
	pt := w32.ClientToScreen(w.hwnd, w32.Point{X: int32(c.X * w.ppd()), Y: int32(c.Y * w.ppd())})
	cursorPos = func() (w32.Point, bool) { return pt, true }
	t.Cleanup(func() { cursorPos = w32.GetCursorPos })
	a.onMouse(popWMMouseMove, &popMouseHook{Pt: pt, Hwnd: w.hwnd, HitTest: popHTClient})
	pump(t, a, w, "the tooltip", 2*time.Second, func() bool { _, ok := a.tips().shownText(w); return ok })
	tip := a.tips().byOwner[w].tip
	w.Close()
	pump(t, a, w2, "the first window's close", 2*time.Second, func() bool { return w.closed })
	s := a.tips()
	if _, ok := s.byOwner[w]; ok || len(s.byOwner) != 1 || len(s.byID) != 1 {
		t.Fatalf("the closed window's tooltip state stays: %d by owner, %d by id", len(s.byOwner), len(s.byID))
	}
	if isWindow(tip) {
		t.Error("the closed window's tooltip window stays")
	}
	if h.refs != 1 || h.mouse == 0 {
		t.Fatalf("one window with tooltips left, hooks %+v", *h)
	}

	// the last one goes: the hooks come off
	w2.Close()
	pumpFor(t, a, w2, 200*time.Millisecond, func() bool { return w2.closed })
	if !w2.closed {
		t.Fatal("the second window did not close")
	}
	if h.refs != 0 || h.mouse != 0 || h.cwp != 0 || h.keyboard != 0 {
		t.Fatalf("every window with tooltips is gone, hooks %+v", *h)
	}
	if len(s.byOwner) != 0 || len(s.byID) != 0 {
		t.Fatalf("tooltip state left: %d by owner, %d by id", len(s.byOwner), len(s.byID))
	}
	t.Logf("hooks after both windows closed: %d references, mouse %#x, call-window %#x, keyboard %#x", h.refs, h.mouse, h.cwp, h.keyboard)
}
