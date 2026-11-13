package downloads

// [mac scroll] The grid's bars under the macOS scroll bar styles (kit/scrollmac.go).

import (
	"testing"
	"time"

	"downex/ui/kit"
	"downex/ui/views/viewstest"
)

func TestGridMacScrollBars(t *testing.T) {
	r := newRig(t, viewstest.Options{}, kit.R(0, 0, 360, 300), 0)
	g := r.grid
	if g.hbar.Style.Hidden {
		t.Fatal("no sideways bar in a 360 DIP grid")
	}
	box := g.WindowBounds()
	fluent := g.hbar.WindowBounds()
	if fluent.Height() != 15 || fluent.Max.Y != box.Max.Y {
		t.Fatalf("Fluent sideways bar at %v in a grid at %v", fluent, box)
	}
	if lb := r.table.WindowBounds(); lb.Max.Y != box.Max.Y-15 {
		t.Errorf("Fluent: rows end at %v, want above the 15 DIP gutter", lb.Max.Y)
	}

	send := func(s kit.ScrollBarStyle) {
		env := r.Window.Environment()
		env.ScrollBars = s
		r.Loop.Send(kit.EnvironmentEvent{Env: env})
		r.Settle()
	}

	send(kit.ScrollBarsMacLegacy)
	if hb := g.hbar.WindowBounds(); hb.Height() != 17 || r.table.WindowBounds().Max.Y != box.Max.Y-17 {
		t.Errorf("legacy: sideways bar %v, rows end at %v", hb, r.table.WindowBounds().Max.Y)
	}
	th, ok := g.hbar.thumbRect()
	if !ok || th.Height() != 11 || th.Min.X != 3 || th.Max.Y != 14 {
		t.Errorf("legacy: sideways thumb %v, want 11 DIPs high, 3 from the bottom and the start", th)
	}
	if sb := r.table.Scrollbar(); !sb.Style.Hidden && sb.Bounds().Width() != 17 {
		t.Errorf("legacy: the rows' bar is %v wide", sb.Bounds().Width())
	}

	send(kit.ScrollBarsMacOverlay)
	hb := g.hbar.WindowBounds()
	if hb.Height() != 14 || hb.Max.Y != box.Max.Y || r.table.WindowBounds().Max.Y != box.Max.Y {
		t.Errorf("overlay: sideways bar %v over rows that end at %v (grid %v)", hb, r.table.WindowBounds().Max.Y, box)
	}
	r.Loop.Canvas.Ops = nil
	r.Loop.Host.Invalidate(hb)
	r.Loop.Step()
	if !g.hbar.PassThrough || g.hbar.reveal.Alpha() != 0 {
		t.Errorf("overlay: the sideways bar shows (alpha %v) before anything scrolled", g.hbar.reveal.Alpha())
	}
	r.table.scrollToX(40)
	r.Loop.Step()
	if g.hbar.reveal.Alpha() != 1 || g.hbar.PassThrough {
		t.Errorf("overlay: alpha %v after a sideways scroll", g.hbar.reveal.Alpha())
	}
	r.Advance(time.Second)
	if g.hbar.reveal.Alpha() != 0 {
		t.Errorf("overlay: alpha %v a second after the scroll", g.hbar.reveal.Alpha())
	}

	send(kit.ScrollBarsFluent)
	if hb := g.hbar.WindowBounds(); hb != fluent {
		t.Errorf("Fluent again: sideways bar %v, want %v", hb, fluent)
	}
}
