package add

import (
	"testing"

	"downex/ui/kit"
)

// The URL list is today's textarea.input with resize: vertical: Chromium's resizer at its
// bottom-right corner drags its height (at least .input's 36 px, no most: the dialog grows
// around it), the pointer is ns-resize over it, and Files shown again after Video or stream
// has a new textarea, back at its 4 rows.
func TestAddURLListResizer(t *testing.T) {
	r := setupAdd(t, rigOptions{})
	area := r.a.urlsArea
	h0, card0 := area.Size().H, r.a.Card().WindowBounds().Height()
	b := area.WindowBounds()
	grip := kit.Pt(b.Max.X-6, b.Max.Y-5) // inside the 15 px corner, in the padding
	send := func(e kit.PointerEvent) { r.h.Loop.Send(e); r.h.Settle() }

	send(kit.PointerEvent{Action: kit.PointerMove, Pos: grip})
	if c := r.h.Host.Cursor; c != kit.CursorRowResize {
		t.Fatalf("cursor over the resizer %v, want ns-resize", c)
	}
	send(kit.PointerEvent{Action: kit.PointerMove, Pos: kit.Pt(b.Min.X+40, b.Min.Y+20)})
	if c := r.h.Host.Cursor; c == kit.CursorRowResize {
		t.Fatal("ns-resize away from the resizer")
	}

	send(kit.PointerEvent{Action: kit.PointerMove, Pos: grip})
	send(kit.PointerEvent{Action: kit.PointerDown, Pos: grip, Button: kit.ButtonLeft, Buttons: kit.ButtonLeft, Clicks: 1})
	send(kit.PointerEvent{Action: kit.PointerMove, Pos: grip.Add(kit.Pt(-30, 25)), Buttons: kit.ButtonLeft})
	if got := area.Size().H; !near(got, h0+25) {
		t.Fatalf("dragged 25 px down: %v, want %v", got, h0+25)
	}
	send(kit.PointerEvent{Action: kit.PointerMove, Pos: grip.Add(kit.Pt(0, 60)), Buttons: kit.ButtonLeft})
	send(kit.PointerEvent{Action: kit.PointerUp, Pos: grip.Add(kit.Pt(0, 60)), Button: kit.ButtonLeft})
	if got := area.Size().H; !near(got, h0+60) {
		t.Fatalf("released 60 px down: %v, want %v", got, h0+60)
	}
	if w := area.Size().W; !near(w, b.Width()) {
		t.Fatalf("the width changed: %v, was %v", w, b.Width())
	}
	if got := r.a.Card().WindowBounds().Height(); !near(got, card0+60) {
		t.Fatalf("dialog %v, want %v (laid out around the taller list)", got, card0+60)
	}
	if area.Native() != nil {
		if nb, c := area.ContentRect().Add(area.WindowBounds().Min), area.WindowBounds(); !near(nb.Max.Y, c.Max.Y-9) {
			t.Fatalf("the native field %v does not follow the box %v", nb, c)
		}
	}
	// a move after the release does not drag
	send(kit.PointerEvent{Action: kit.PointerMove, Pos: grip.Add(kit.Pt(0, 90))})
	if got := area.Size().H; !near(got, h0+60) {
		t.Fatalf("moved after the release: %v", got)
	}

	// dragged up past the least height, it stops at 36 px
	b = area.WindowBounds()
	grip = kit.Pt(b.Max.X-6, b.Max.Y-5)
	send(kit.PointerEvent{Action: kit.PointerMove, Pos: grip})
	send(kit.PointerEvent{Action: kit.PointerDown, Pos: grip, Button: kit.ButtonLeft, Buttons: kit.ButtonLeft, Clicks: 1})
	send(kit.PointerEvent{Action: kit.PointerMove, Pos: grip.Add(kit.Pt(0, -400)), Buttons: kit.ButtonLeft})
	if got := area.Size().H; !near(got, 36) {
		t.Fatalf("dragged far up: %v, want 36", got)
	}
	send(kit.PointerEvent{Action: kit.PointerCancel, Pos: grip})

	r.toStream()
	r.a.seg.SetValue(modeFiles)
	r.a.setMode(modeFiles)
	r.h.Settle()
	if got := area.Size().H; !near(got, h0) {
		t.Fatalf("Files again: %v, want its 4 rows' %v", got, h0)
	}
}

func near(a, b kit.DIP) bool { return a-b < 0.01 && b-a < 0.01 }
