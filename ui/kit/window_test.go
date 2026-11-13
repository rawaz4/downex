package kit_test

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"downex/ui/kit"
	"downex/ui/kit/kittest"
)

// TestFrameProtocol drives a window as a backend does: Attach lays it out and asks for
// the whole window to be painted; afterwards nothing happens until something changes,
// and a change paints only its own area.
func TestFrameProtocol(t *testing.T) {
	a := kit.NewLabel("All downloads", font13, kit.TextOptions{})
	b := kit.NewLabel("28", font13, kit.TextOptions{})
	// The row sits in a block, so its height is its content's (a flex row that fills
	// the window would stretch its items to the window's height, as CSS does).
	row := box(kit.Style{Display: kit.DisplayFlex, ColumnGap: kit.Px(8), Padding: kit.EdgesAll(kit.Px(10))}, a, b)
	w, loop := open(t, box(kit.Style{}, row), kit.Size{W: 400, H: 100}, env100)
	h := loop.Host

	if h.Title != "Downex" {
		t.Errorf("title = %q", h.Title)
	}
	if loop.Paints != 1 || loop.Painted != kit.R(0, 0, 400, 100) {
		t.Fatalf("first frame: %d paints of %v, want 1 of the whole window", loop.Paints, loop.Painted)
	}
	if got := loop.Canvas.Texts(); len(got) != 2 || got[0] != "All downloads" || got[1] != "28" {
		t.Errorf("drawn texts = %q", got)
	}
	if op := loop.Canvas.Ops[0]; op.Kind != "FillRect" || op.Color != testPalette.Ground || op.Rect != loop.Painted {
		t.Errorf("first op %+v: Paint must cover the dirty area with the ground", op)
	}
	// "All downloads" at 13 px: 12 characters of 6.5 and one space of 3.25.
	if got, want := a.Bounds(), kit.R(10, 10, 10+81.25, 10+13*1.4); !approxRect(got, want) {
		t.Errorf("label bounds = %v, want %v", got, want)
	}
	if got := b.Bounds().Min.X; !approx(got, 10+81.25+8) {
		t.Errorf("second label at %v, want after the gap", got)
	}

	// Idle: no frames, no paints.
	before := w.Stats()
	loop.Canvas.Reset()
	loop.Run(2 * time.Second)
	if s := w.Stats(); s.Frames != before.Frames || s.Paints != before.Paints || s.Layouts != before.Layouts {
		t.Errorf("idle window worked: %+v -> %+v", before, s)
	}

	// A text change lays out once and paints the labels' areas only.
	h.Invalidations = nil
	b.SetText("29")
	loop.Step()
	if s := w.Stats(); s.Layouts != before.Layouts+1 || s.Paints != before.Paints+1 {
		t.Errorf("after one change: layouts %d -> %d, paints %d -> %d", before.Layouts, s.Layouts, before.Paints, s.Paints)
	}
	if !loop.Painted.Intersect(a.WindowBounds()).Empty() {
		t.Errorf("painted %v also covers the unchanged label %v", loop.Painted, a.WindowBounds())
	}
	if loop.Painted.Intersect(b.WindowBounds()).Empty() {
		t.Errorf("painted %v misses the changed label %v", loop.Painted, b.WindowBounds())
	}
	if !loop.Settle(5) {
		t.Error("the window does not go idle after a change")
	}
}

// TestDetachReleases checks that every text layout is released when the window closes.
func TestDetachReleases(t *testing.T) {
	var kids []kit.Widget
	for range 20 {
		kids = append(kids, kit.NewLabel("a name that is long enough to be cut", font13, kit.TextOptions{Truncate: kit.TruncateEnd}))
	}
	content := box(kit.Style{Display: kit.DisplayFlex, Axis: kit.AxisColumn, Width: kit.Px(120)}, kids...)
	w, loop := open(t, content, kit.Size{W: 400, H: 600}, env100)
	res := loop.Host.Res
	if res.Live == 0 {
		t.Fatal("no layouts made")
	}
	w.Detach()
	if res.Live != 0 {
		t.Errorf("%d text layouts still alive after Detach (%d made)", res.Live, res.Made)
	}
	if err := w.Post(func() {}); !errors.Is(err, kit.ErrClosed) {
		t.Errorf("Post after Detach = %v, want ErrClosed", err)
	}
}

// TestFrameHit checks the custom frame's parts: the caption's ground, its buttons, and
// what is interactive inside it.
func TestFrameHit(t *testing.T) {
	caption := box(kit.Style{Display: kit.DisplayFlex, Height: kit.Px(44)})
	caption.Frame = kit.FrameCaption
	brand := leaf(24, 24)
	search := leaf(440, 32)
	search.Focus = kit.FocusTab
	settings := kit.NewPressable(nil) // a button of the bar that is not a window button
	settings.Style = kit.Style{Width: kit.Px(32), Height: kit.Px(30)}
	// The window buttons are buttons (the host sends them the pointer), and still the frame's.
	minBtn, maxBtn, closeBtn := kit.NewPressable(nil), kit.NewPressable(nil), kit.NewPressable(nil)
	for _, b := range []*kit.Pressable{minBtn, maxBtn, closeBtn} {
		b.Style = kit.Style{Width: kit.Px(46), Height: kit.Px(44)}
	}
	minBtn.Frame, maxBtn.Frame, closeBtn.Frame = kit.FrameMinimize, kit.FrameMaximize, kit.FrameClose
	spacer := &kit.Element{Style: kit.Style{Flex: kit.Flex1}}
	caption.Add(brand, search, spacer, settings, minBtn, maxBtn, closeBtn)
	page := box(kit.Style{Display: kit.DisplayFlex, Axis: kit.AxisColumn}, caption, &kit.Element{Style: kit.Style{Flex: kit.Flex1}})
	w, _ := open(t, page, kit.Size{W: 1200, H: 760}, env100)

	for _, c := range []struct {
		p    kit.Point
		want kit.FramePart
	}{
		{kit.Pt(10, 10), kit.FrameCaption},            // the brand mark
		{kit.Pt(100, 10), kit.FrameClient},            // the search field
		{kit.Pt(700, 20), kit.FrameCaption},           // empty title bar
		{kit.Pt(1200-46*3-32+5, 20), kit.FrameClient}, // the settings button
		{kit.Pt(1200-46*3+5, 20), kit.FrameMinimize},
		{kit.Pt(1200-46*2+5, 20), kit.FrameMaximize},
		{kit.Pt(1199, 43), kit.FrameClose},
		{kit.Pt(600, 400), kit.FrameClient},
	} {
		if got := w.FrameHit(c.p); got != c.want {
			t.Errorf("FrameHit(%v) = %v, want %v", c.p, got, c.want)
		}
	}
}

// TestPointerCaptureAndHover checks the hover chain, cursors and a capture that ends at
// the last button up.
func TestPointerCaptureAndHover(t *testing.T) {
	btn := kit.NewPressable(nil)
	btn.Style = kit.Style{Width: kit.Px(100), Height: kit.Px(30)}
	btn.Cursor = kit.CursorPointer
	pressed := 0
	btn.OnPress = func() { pressed++ }
	page := box(kit.Style{Padding: kit.EdgesAll(kit.Px(10))}, btn)
	w, loop := open(t, page, kit.Size{W: 300, H: 200}, env100)
	h := loop.Host

	loop.Send(kit.PointerEvent{Action: kit.PointerEnter, Pos: kit.Pt(20, 20)})
	loop.Send(kit.PointerEvent{Action: kit.PointerMove, Pos: kit.Pt(20, 20)})
	if !btn.Hovered() || !page.Hovered() || h.Cursor != kit.CursorPointer {
		t.Fatalf("hover: button %v, page %v, cursor %v", btn.Hovered(), page.Hovered(), h.Cursor)
	}
	loop.Send(kit.PointerEvent{Action: kit.PointerDown, Pos: kit.Pt(20, 20), Button: kit.ButtonLeft, Buttons: kit.ButtonLeft, Clicks: 1})
	if w.Captured() != &btn.Element || !h.Captured || !btn.Down() {
		t.Fatal("a press on a button captures the pointer and shows it down")
	}
	if w.Focused() != &btn.Element {
		t.Error("a press focuses the button")
	}
	// Dragged out: still captured, no longer down; released outside: no press.
	loop.Send(kit.PointerEvent{Action: kit.PointerMove, Pos: kit.Pt(250, 150), Buttons: kit.ButtonLeft})
	if btn.Down() || !btn.Hovered() {
		t.Errorf("dragged out: down %v, hovered %v (hover stays with the capture)", btn.Down(), btn.Hovered())
	}
	loop.Send(kit.PointerEvent{Action: kit.PointerUp, Pos: kit.Pt(250, 150), Button: kit.ButtonLeft})
	if pressed != 0 || w.Captured() != nil {
		t.Errorf("released outside: %d presses, capture %v", pressed, w.Captured())
	}
	if btn.Hovered() || h.Cursor != kit.CursorDefault {
		t.Errorf("after the capture: hovered %v, cursor %v", btn.Hovered(), h.Cursor)
	}
	// A click inside presses once.
	loop.Send(kit.PointerEvent{Action: kit.PointerDown, Pos: kit.Pt(20, 20), Button: kit.ButtonLeft, Buttons: kit.ButtonLeft, Clicks: 1})
	loop.Send(kit.PointerEvent{Action: kit.PointerUp, Pos: kit.Pt(20, 20), Button: kit.ButtonLeft})
	if pressed != 1 {
		t.Errorf("click: %d presses", pressed)
	}
	// A cancelled press does nothing.
	loop.Send(kit.PointerEvent{Action: kit.PointerDown, Pos: kit.Pt(20, 20), Button: kit.ButtonLeft, Buttons: kit.ButtonLeft, Clicks: 1})
	loop.Send(kit.PointerEvent{Action: kit.PointerCancel, Pos: kit.Pt(20, 20)})
	loop.Send(kit.PointerEvent{Action: kit.PointerUp, Pos: kit.Pt(20, 20), Button: kit.ButtonLeft})
	if pressed != 1 || btn.Down() {
		t.Errorf("cancelled press: %d presses, down %v", pressed, btn.Down())
	}
}

// TestEnvironmentChange checks that a larger Windows text size scales rem lengths and text
// while px lengths stay.
func TestEnvironmentChange(t *testing.T) {
	side := &kit.Element{Style: kit.Style{Width: kit.Rem(14), Height: kit.Px(29)}}
	lbl := kit.NewLabel("Video", font13, kit.TextOptions{})
	page := box(kit.Style{}, box(kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignStart}, side, lbl))
	w, loop := open(t, page, kit.Size{W: 1200, H: 760}, env100)
	if got := side.Size(); got != (kit.Size{W: 224, H: 29}) {
		t.Fatalf("14rem x 29px = %v at the default text size, want 224 x 29", got)
	}
	big := env100
	big.Scale.Text = 1.25
	loop.Send(kit.EnvironmentEvent{Env: big})
	loop.Step()
	if got := side.Size(); got != (kit.Size{W: 280, H: 29}) {
		t.Errorf("at 125%% text: %v, want 280 x 29 (rem grows, px stays)", got)
	}
	if got, want := lbl.Size().H, float32(16.25*1.4); !approx(got, want) {
		t.Errorf("label height at 20 px rem = %v, want %v", got, want)
	}
	if loop.Painted != kit.R(0, 0, 1200, 760) {
		t.Errorf("an environment change paints %v, want the whole window", loop.Painted)
	}
	_ = w
}

// TestPostRunsOnTheLoop checks Post's ordering and that deferred work runs after the call.
func TestPostRunsOnTheLoop(t *testing.T) {
	w, loop := open(t, box(kit.Style{}), kit.Size{W: 100, H: 100}, env100)
	var got []int
	for i := range 3 {
		if err := w.Post(func() { got = append(got, i) }); err != nil {
			t.Fatal(err)
		}
	}
	if len(got) != 0 {
		t.Fatal("Post ran at once")
	}
	loop.Host.RunPosted()
	if len(got) != 3 || got[0] != 0 || got[2] != 2 {
		t.Errorf("posted ran %v", got)
	}
	// A timer costs no frames while it waits.
	before := w.Stats().FrameRequests
	fired := false
	w.After(500*time.Millisecond, func() { fired = true })
	loop.Run(400 * time.Millisecond)
	if fired || w.Stats().FrameRequests != before {
		t.Errorf("timer early: fired %v, frames asked %d", fired, w.Stats().FrameRequests-before)
	}
	loop.Run(200 * time.Millisecond)
	if !fired {
		t.Error("timer did not fire")
	}
	if loop.Clock.Timers() != 0 {
		t.Errorf("%d timers left", loop.Clock.Timers())
	}
	_ = kittest.NewClock
}

// TestPostFromGoroutines posts from other goroutines and runs a real timer (the window's
// own scheduler), as a backend's message loop sees them; run it with -race.
func TestPostFromGoroutines(t *testing.T) {
	w, loop := open(t, box(kit.Style{}), kit.Size{W: 100, H: 100}, env100)
	var wg sync.WaitGroup
	var n atomic.Int32
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				if err := w.Post(func() { n.Add(1) }); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	loop.Host.RunPosted()
	if n.Load() != 800 {
		t.Errorf("%d of 800 posted functions ran", n.Load())
	}

	rw := kit.NewWindow(box(kit.Style{}), kit.WindowOptions{})
	rl := kittest.NewLoop(rw, nil, kit.Size{W: 100, H: 100}, env100)
	fired := false
	rw.After(5*time.Millisecond, func() { fired = true })
	for deadline := time.Now().Add(5 * time.Second); !fired; {
		rl.Host.RunPosted()
		if time.Now().After(deadline) {
			t.Fatal("a real timer did not fire within 5 s")
		}
		time.Sleep(time.Millisecond)
	}
	rw.Detach()
	if err := rw.Post(func() {}); !errors.Is(err, kit.ErrClosed) {
		t.Errorf("Post after Detach = %v", err)
	}
}
