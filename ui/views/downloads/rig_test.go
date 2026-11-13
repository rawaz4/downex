package downloads

import (
	"testing"

	"downex/ui/kit"
	"downex/ui/views/viewstest"
)

// placed lays its children at fixed boxes in the window: the grid where the shell puts it
// (the card's third row), the pane under it.
type placed struct {
	kit.Element
	boxes []kit.Rect
}

func (p *placed) ContentWidths(*kit.LayoutContext) (min, max kit.DIP) { return 0, 0 }
func (p *placed) ContentHeight(*kit.LayoutContext, kit.DIP) kit.DIP   { return 0 }
func (p *placed) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	for i, c := range p.Children() {
		if i < len(p.boxes) {
			ctx.Place(c.Widget(), p.boxes[i].Add(box.Min), box.Width())
		}
	}
}

// rig is a harness with the downloads lane's widgets shown.
type rig struct {
	*viewstest.Harness
	dl     *Downloads
	grid   *Grid
	table  *Table
	pane   *Details
	screen *placed
}

// gridBox is the reference's div.wrap at 1200 × 760 (the card's grid row).
var gridBox = kit.R(225, 103, 1200, 727)

// newRig makes a harness, the lane's widgets, and shows the grid at box (the window's whole
// area when empty) with the pane under it when paneH > 0.
func newRig(t *testing.T, o viewstest.Options, box kit.Rect, paneH kit.DIP) *rig {
	t.Helper()
	return newRigPrep(t, o, box, paneH, nil)
}

// newRigPrep is newRig with prep run on the harness before the lane's widgets are made (the
// prefs they read at start).
func newRigPrep(t *testing.T, o viewstest.Options, box kit.Rect, paneH kit.DIP, prep func(h *viewstest.Harness)) *rig {
	t.Helper()
	h := viewstest.New(t, o)
	if prep != nil {
		prep(h)
	}
	dl, err := New(h.App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dl.Dispose)
	if box.Empty() {
		box = kit.R(0, 0, kit.DIP(h.Opts.Width), kit.DIP(h.Opts.Height))
	}
	s := &placed{}
	s.Style = kit.Style{Width: kit.Pct(100), Height: kit.Pct(100)}
	gb := box
	if paneH > 0 {
		gb.Max.Y -= paneH
	}
	s.boxes = []kit.Rect{gb, kit.R(box.Min.X, gb.Max.Y, box.Max.X, box.Max.Y)}
	s.Add(dl.Grid(), dl.Details())
	h.Show(s)
	h.Window.Semantics().Root() // assistive technology reads the tree: rows keep lasting ids
	r := &rig{Harness: h, dl: dl, grid: dl.Grid(), table: dl.Grid().Table(), pane: dl.Details(), screen: s}
	r.Settle()
	return r
}

// TestSmoke: the Studio scene's rows show in the grid.
func TestSmoke(t *testing.T) {
	r := newRig(t, viewstest.Options{}, gridBox, 0)
	first, last := r.table.Realised()
	if last-first == 0 {
		t.Fatalf("no rows realised (model %d)", r.table.Model.Len())
	}
	t.Logf("realised %d..%d of %d; header %v", first, last, r.table.Model.Len(), r.table.Bounds())
}
