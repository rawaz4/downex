package palette_test

import (
	"testing"

	"downex/ui/kit"
	"downex/ui/views"
	"downex/ui/views/palette"
	"downex/ui/views/viewstest"
)

// TestShrinkRepaintsShadow: when the palette shrinks (every command, then "pause"), the area
// repainted covers the old card with the --shadow-lg its Paint draws and the 4 DIP it rises
// from, on every host: the windows keep their pixels and repaint only what was invalidated,
// and the real Windows window kept the old shadow below the new card
// (TestRealWindowShrinkRepaint; TestShrinkRepaintsWhatItLeft on the Mac).
func TestShrinkRepaintsShadow(t *testing.T) {
	h := viewstest.New(t, viewstest.Options{Width: 1000, Height: 700, Fixture: "palette"})
	p := palette.New(h.App, palette.Options{})
	h.App.Dialogs.Show(p)
	h.Settle()
	before := p.Card().WindowBounds()
	h.Host.Invalidations = nil
	typeQuery(h, p, "pause")
	after := p.Card().WindowBounds()
	if after.Height() >= before.Height() {
		t.Fatalf("the palette did not shrink: %v -> %v", before, after)
	}
	var dirty kit.Rect
	for _, r := range h.Host.Invalidations {
		dirty = dirty.Union(r)
	}
	d := kit.ShadowsOverflow(views.TokensOf(p.Card()).ShadowLg)
	win := kit.RectAt(kit.Point{}, h.Window.Size())
	want := before.Inset(kit.Insets{Top: -d - 4, Right: -d, Bottom: -d, Left: -d}).Intersect(win)
	if dirty.Intersect(want) != want {
		t.Errorf("repainted %v, want at least the old card and its shadow %v", dirty, want)
	}
}
