package downloads

import (
	"testing"

	"downex/ui/kit"
)

// TestShiftWheelInWebKit: macOS turns Shift + a notched wheel into a sideways event (deltaX
// set, deltaY 0, Shift still held), which WebKit scrolls sideways; a trackpad's vertical
// swipe with Shift held stays vertical there (measured with a WKWebView: scrollY moved). On
// Windows (Chromium) Shift turns a vertical wheel sideways, as before.
func TestShiftWheelInWebKit(t *testing.T) {
	wheel := func(r *rig, d kit.Point, mods kit.Modifiers) bool {
		in := &kit.WheelInput{WheelEvent: kit.WheelEvent{Delta: d, Mods: mods, Mode: kit.WheelPrecise}}
		return r.table.HandleWheel(in)
	}
	r := mockRig(t, 60)
	env := r.Window.Environment()
	env.WebKit = true
	r.Loop.Send(kit.EnvironmentEvent{Env: env})
	r.Settle()
	r.table.viewW = 300 // the columns overflow
	if !wheel(r, kit.Pt(40, 0), kit.ModShift) || r.table.scrollX != 40 {
		t.Errorf("Shift + notched wheel (sideways from AppKit): scrollX %v, want 40", r.table.scrollX)
	}
	r.table.scrollX = 0
	before := r.table.ScrollOffset()
	if !wheel(r, kit.Pt(0, 30), kit.ModShift) || r.table.scrollX != 0 || r.table.ScrollOffset() == before {
		t.Errorf("Shift + vertical swipe: scrollX %v, offset %v -> %v; want a vertical scroll", r.table.scrollX, before, r.table.ScrollOffset())
	}

	w := mockRig(t, 60) // Chromium (Windows): unchanged
	w.table.viewW = 300
	if !wheel(w, kit.Pt(0, 40), kit.ModShift) || w.table.scrollX != 40 {
		t.Errorf("Chromium Shift + vertical wheel: scrollX %v, want 40", w.table.scrollX)
	}
}

// TestRowMenuOnPressInWebKit: today's Mac window (WebKit) opens the row menu on the press,
// for a right press and for Control+click (macOS's secondary click), and the release does
// not toggle the row.
func TestRowMenuOnPressInWebKit(t *testing.T) {
	r := mockRig(t, 50)
	env := r.Window.Environment()
	env.WebKit = true
	r.Loop.Send(kit.EnvironmentEvent{Env: env})
	r.Settle()
	ids := r.visibleIDs()
	for i, b := range []struct {
		button kit.Buttons
		mods   kit.Modifiers
	}{{kit.ButtonRight, 0}, {kit.ButtonLeft, kit.ModCtrl}} {
		id := ids[i+1]
		p := r.rowPoint(id)
		r.Loop.Send(kit.PointerEvent{Action: kit.PointerMove, Pos: p})
		r.Loop.Send(kit.PointerEvent{Action: kit.PointerDown, Pos: p, Button: b.button, Buttons: b.button, Mods: b.mods, Clicks: 1})
		r.Settle()
		if n := count(r.Calls, "OpenMenu"); n != i+1 {
			t.Fatalf("press %d (%v): %d menus before the release", i, b, n)
		}
		r.Loop.Send(kit.PointerEvent{Action: kit.PointerUp, Pos: p, Button: b.button, Mods: b.mods})
		r.Settle()
		if n := count(r.Calls, "OpenMenu"); n != i+1 {
			t.Errorf("release %d: %d menus", i, n)
		}
		if got := r.selectedIDs(); len(got) != 1 || got[0] != id {
			t.Errorf("press %d: selection %v, want [%d]", i, got, id)
		}
	}
}
