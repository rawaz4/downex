package palette_test

import (
	"testing"

	"downex/ui/views/palette"
	"downex/ui/views/viewstest"
)

// TestQueryFieldClearsTheAccentLine: the search's native field is opaque (the page's input is
// transparent), so it ends above the row's accent line (the 1 px over the row's 1 px bottom
// border) and keeps as much room at the top: the focused line runs unbroken under the glass,
// the field and the scope, with the field's text centred as before.
func TestQueryFieldClearsTheAccentLine(t *testing.T) {
	h := viewstest.New(t, viewstest.Options{Width: 1000, Height: 700, Fixture: "palette"})
	p := palette.New(h.App, palette.Options{})
	h.App.Dialogs.Show(p)
	h.Settle()
	f := p.Field()
	row := f.Parent().WindowBounds()
	got := f.WindowBounds()
	line := row.Max.Y - 2 // the accent line's top: above the 1 px bottom border
	if got.Max.Y > line {
		t.Errorf("the field ends at y=%v, over the accent line from y=%v (row %v)", got.Max.Y, line, row)
	}
	if top, bottom := got.Min.Y-row.Min.Y, row.Max.Y-1-got.Max.Y; top != bottom {
		t.Errorf("the field is off centre: %v above, %v below (field %v, row %v)", top, bottom, got, row)
	}
}
