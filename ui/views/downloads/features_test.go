package downloads

import (
	"math"
	"slices"
	"testing"
	"time"

	"downex/ui/core/display"
	"downex/ui/kit"
	"downex/ui/kit/hostcap/hostcaptest"
	"downex/ui/kit/kittest"
	"downex/ui/kit/widgets"
	"downex/ui/state"
	"downex/ui/views/viewstest"
)

// The plan's step-11 behaviours that DownloadTable.test.ts does not table: the column widths
// kept in downex.columnWidths, resizing by keyboard and pointer, double-click reset, the
// Name column's width at the plan's window widths, the cell cursor mirrored right to left,
// and the idle and allocation guards.

func shownKeys(l *Layout) []string {
	var out []string
	for _, c := range l.Shown {
		out = append(out, c.Key)
	}
	return out
}

func widthOfKey(l *Layout, key string) float64 {
	if i := l.Index(key); i >= 0 {
		return l.W[i]
	}
	return -1
}

// The owner's saved widths ({"size":82}) load and apply: Size is 82 px, the others keep their
// rem defaults, and Name takes the rest.
func TestColumnWidthsOwnersValue(t *testing.T) {
	r := newRigPrep(t, viewstest.Options{}, gridBox, 0, func(h *viewstest.Harness) {
		if err := h.Storage.Set(state.ColumnWidthsKey, `{"size":82}`); err != nil {
			t.Fatal(err)
		}
	})
	l := &r.table.layout
	if w := widthOfKey(l, "size"); w != 82 {
		t.Errorf("size %v, want 82", w)
	}
	if w := widthOfKey(l, "progress"); w != 11*16 {
		t.Errorf("progress %v", w)
	}
	// the grid is 975 wide with a 15 px bar: 960 - 24 of padding - the fixed columns
	fixed := 0.0
	for i, c := range l.Shown {
		if c.Width > 0 {
			fixed += l.W[i]
		}
	}
	if w, want := widthOfKey(l, "name"), 960-24-fixed; math.Abs(w-want) > 0.5 {
		t.Errorf("name %v, want %v", w, want)
	}
	// the header's Size column is 82 px too
	for _, x := range r.table.header.ths {
		if x.col.Key == "size" && math.Abs(float64(x.Bounds().Width())-82) > 0.01 {
			t.Errorf("size header %v", x.Bounds())
		}
	}
}

// Ctrl+Right and Ctrl+Left in the header row resize the column by a rem, clamped to its
// minimum and 40 rem, and save the widths; a double click on its handle resets and saves.
func TestColumnResizeKeysAndReset(t *testing.T) {
	r := newRig(t, viewstest.Options{}, gridBox, 0)
	r.table.FocusList()
	r.Settle()
	if !r.Key(kit.KeyArrowUp, 0) || !r.table.inHeader {
		t.Fatal("no header row")
	}
	r.Key(kit.KeyArrowRight, 0) // Size
	if c := r.table.layout.Shown[r.table.colIndex()]; c.Key != "size" {
		t.Fatalf("column %q", c.Key)
	}
	r.Key(kit.KeyArrowRight, kit.ModCtrl)
	if w := widthOfKey(&r.table.layout, "size"); w != 96 {
		t.Errorf("size after Ctrl+Right %v, want 96", w)
	}
	if v, _ := r.Storage.Value(state.ColumnWidthsKey); v != `{"size":96}` {
		t.Errorf("saved %q", v)
	}
	for range 4 {
		r.Key(kit.KeyArrowLeft, kit.ModCtrl)
	}
	// 4 rem is Size's minimum
	if w := widthOfKey(&r.table.layout, "size"); w != 64 {
		t.Errorf("size at its minimum %v, want 64", w)
	}
	// at most 40 rem (a wide Size pushes the dropped columns out, as fitColumns does today, so
	// the clamp is the widths' own)
	if !r.table.cols.SetWidth(r.table.layout.Shown[1], 2000, 16) || r.table.cols.widths["size"] != 40*16 {
		t.Errorf("size at most %v, want 640", r.table.cols.widths["size"])
	}
	r.table.cols.SetWidth(r.table.layout.Shown[1], 64, 16)
	r.table.columnsChanged()
	r.Settle()
	// Name is not resizable: Ctrl+Left on it moves to it, nothing more
	r.Key(kit.KeyArrowLeft, 0)
	if c := r.table.layout.Shown[r.table.colIndex()]; c.Key != "name" {
		t.Fatalf("column %q", c.Key)
	}
	before, _ := r.Storage.Value(state.ColumnWidthsKey)
	r.Key(kit.KeyArrowLeft, kit.ModCtrl)
	if after, _ := r.Storage.Value(state.ColumnWidthsKey); after != before {
		t.Errorf("Name resized: %q → %q", before, after)
	}

	// double-click reset on Size's handle
	var grip *resizer
	for _, g := range r.table.header.grips {
		if g.col.Key == "size" {
			grip = g
		}
	}
	p := grip.WindowBounds().Center()
	r.Loop.Send(kit.PointerEvent{Action: kit.PointerMove, Pos: p})
	r.Loop.Send(kit.PointerEvent{Action: kit.PointerDown, Pos: p, Button: kit.ButtonLeft, Buttons: kit.ButtonLeft, Clicks: 2})
	r.Loop.Send(kit.PointerEvent{Action: kit.PointerUp, Pos: p, Button: kit.ButtonLeft})
	r.Settle()
	if w := widthOfKey(&r.table.layout, "size"); w != 80 {
		t.Errorf("size after reset %v, want 80", w)
	}
	if v, _ := r.Storage.Value(state.ColumnWidthsKey); v != `{}` && v != "" {
		t.Errorf("saved after reset %q", v)
	}
}

// A drag on a handle resizes its column with the pointer, clamped, and saves on release.
func TestColumnResizePointer(t *testing.T) {
	r := newRig(t, viewstest.Options{}, gridBox, 0)
	var grip *resizer
	for _, g := range r.table.header.grips {
		if g.col.Key == "progress" {
			grip = g
		}
	}
	p := grip.WindowBounds().Center()
	r.Loop.Send(kit.PointerEvent{Action: kit.PointerMove, Pos: p})
	r.Loop.Send(kit.PointerEvent{Action: kit.PointerDown, Pos: p, Button: kit.ButtonLeft, Buttons: kit.ButtonLeft, Clicks: 1})
	r.Loop.Send(kit.PointerEvent{Action: kit.PointerMove, Pos: p.Add(kit.Pt(30, 0)), Buttons: kit.ButtonLeft})
	r.Settle()
	if w := widthOfKey(&r.table.layout, "progress"); w != 176+30 {
		t.Errorf("progress while dragging %v, want 206", w)
	}
	if _, saved := r.Storage.Value(state.ColumnWidthsKey); saved {
		t.Error("saved before the release")
	}
	r.Loop.Send(kit.PointerEvent{Action: kit.PointerMove, Pos: p.Add(kit.Pt(-300, 0)), Buttons: kit.ButtonLeft})
	r.Loop.Send(kit.PointerEvent{Action: kit.PointerUp, Pos: p.Add(kit.Pt(-300, 0)), Button: kit.ButtonLeft})
	r.Settle()
	// 7 rem is Progress's minimum
	if w := widthOfKey(&r.table.layout, "progress"); w != 112 {
		t.Errorf("progress after the drag %v, want 112", w)
	}
	if v, _ := r.Storage.Value(state.ColumnWidthsKey); v != `{"progress":112}` {
		t.Errorf("saved %q", v)
	}
	// the widths come back in a new window
	r2 := newRigPrep(t, viewstest.Options{}, gridBox, 0, func(h *viewstest.Harness) {
		v, _ := r.Storage.Value(state.ColumnWidthsKey)
		_ = h.Storage.Set(state.ColumnWidthsKey, v)
	})
	if w := widthOfKey(&r2.table.layout, "progress"); w != 112 {
		t.Errorf("progress in a new window %v", w)
	}
}

// todayName is DownloadTable.svelte's template for a grid w px wide (its client width,
// without the 15 px bar): fitColumns drops Added, Conn., Time left, Size and Speed in that
// order until the rest fits, then Name is minmax(12rem, 1fr) beside the fixed columns.
func todayName(client, rem float64) (name float64, shown []string) {
	type col struct {
		key        string
		width, min float64
		drop       int
	}
	cols := []col{{"name", 0, 12, 0}, {"size", 5, 4, 2}, {"progress", 11, 7, 0}, {"speed", 5.5, 4.5, 1}, {"eta", 5, 4, 3},
		{"connections", 3.25, 2.75, 4}, {"status", 11.25, 6, 0}, {"added", 4.25, 4, 5}}
	avail := client - 24
	need := func() float64 {
		s := 0.0
		for _, c := range cols {
			if c.width == 0 {
				s += c.min * rem
			} else {
				s += c.width * rem
			}
		}
		return s
	}
	for d := 5; d >= 1 && need() > avail; d-- {
		cols = slices.DeleteFunc(cols, func(c col) bool { return c.drop == d })
	}
	fixed := 0.0
	for _, c := range cols {
		shown = append(shown, c.key)
		fixed += c.width * rem
	}
	return math.Max(12*rem, avail-fixed), shown
}

// The Name column is as wide as today's at 1100, 1200, 1366, 1470 and 1920 px (the sidebar
// 225 px wide from 1000 px up, the grid to the window's end, a 15 px scroll bar), and the
// same columns show.
func TestNameColumnWidths(t *testing.T) {
	for _, w := range []int{1100, 1200, 1366, 1470, 1920} {
		for _, scale := range []float32{1, 1.25} {
			r := newRig(t, viewstest.Options{Width: w, Height: 760, Scale: scale}, kit.R(225, 103, kit.DIP(w), 727), 0)
			l := &r.table.layout
			bar := float64(r.table.Scrollbar().Bounds().Width())
			want, shown := todayName(float64(w)-225-15, 16)
			got := widthOfKey(l, "name")
			t.Logf("%d px %vx: Name %.1f px (today %.1f), bar %.1f, columns %v", w, scale, got, want, bar, shownKeys(l))
			if math.Abs(got-want) > 1 {
				t.Errorf("%d px %vx: Name %.2f, today %.2f", w, scale, got, want)
			}
			if !slices.Equal(shownKeys(l), shown) {
				t.Errorf("%d px: columns %v, today %v", w, shownKeys(l), shown)
			}
		}
	}
}

// rtlRig shows the grid in a second window laid out right to left (kit.WindowOptions.
// Direction), driven by its own loop; the App stays the harness's.
type rtlRig struct {
	*rig
	w    *kit.Window
	loop *kittest.Loop
}

func newRTLRig(t *testing.T) *rtlRig {
	t.Helper()
	h := viewstest.New(t, viewstest.Options{})
	dl, err := New(h.App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dl.Dispose)
	s := &placed{boxes: []kit.Rect{kit.R(0, 103, 975, 727)}}
	s.Style = kit.Style{Width: kit.Pct(100), Height: kit.Pct(100)}
	s.Add(dl.Grid())
	root := widgets.NewScope(h.App.WidgetTheme)
	root.Style = kit.Style{Display: kit.DisplayBlock, Width: kit.Pct(100), Height: kit.Pct(100)}
	root.Add(s)
	w := kit.NewWindow(root, kit.WindowOptions{Title: "rtl", Direction: kit.DirRTL, Palette: h.App.WidgetTheme.Palette,
		Now: h.Clock.Now, Scheduler: h.Clock})
	env := kit.Environment{Scale: kit.Scale{PixelsPerDIP: 1, Text: 1}, HoverTime: 400 * time.Millisecond}
	host := hostcaptest.NewHost(kit.Size{W: 975, H: 760}, env, h.Clock)
	loop := &kittest.Loop{Host: host.Host, Root: w, Canvas: &kittest.Canvas{}, Clock: h.Clock, Refresh: time.Second / 60}
	w.Attach(host)
	t.Cleanup(w.Detach)
	loop.Settle(120)
	return &rtlRig{rig: &rig{Harness: h, dl: dl, grid: dl.Grid(), table: dl.Grid().Table(), pane: dl.Details(), screen: s}, w: w, loop: loop}
}

func (r *rtlRig) key(k kit.Key, mods kit.Modifiers) {
	r.loop.Send(kit.KeyEvent{Action: kit.KeyDown, Key: k, Mods: mods})
	r.loop.Send(kit.KeyEvent{Action: kit.KeyUp, Key: k, Mods: mods})
	r.loop.Settle(120)
	r.Settle()
}

// Right to left the columns run from the right (Name at the right edge) and Left and Right
// move the cell cursor the other way: Left is the next column, as DownloadTable's
// isRtl() swaps them.
func TestCellCursorMirrorsRightToLeft(t *testing.T) {
	r := newRTLRig(t)
	tb := r.table
	if !tb.rtl() {
		t.Fatal("the table is not right to left")
	}
	var name, size *th
	for _, x := range tb.header.ths {
		switch x.col.Key {
		case "name":
			name = x
		case "size":
			size = x
		}
	}
	if !(name.Bounds().Min.X > size.Bounds().Min.X) {
		t.Errorf("Name %v is not right of Size %v", name.Bounds(), size.Bounds())
	}
	r.w.Focus(tb)
	r.loop.Settle(120)
	r.key(kit.KeyArrowDown, 0) // the first row
	r.key(kit.KeyArrowLeft, 0) // forward: the first cell
	if tb.colIndex() != 0 {
		t.Fatalf("Left gave column %d, want 0", tb.colIndex())
	}
	r.key(kit.KeyArrowLeft, 0)
	if tb.colIndex() != 1 {
		t.Errorf("Left again gave column %d, want 1", tb.colIndex())
	}
	r.key(kit.KeyArrowRight, 0) // back
	if tb.colIndex() != 0 {
		t.Errorf("Right gave column %d, want 0", tb.colIndex())
	}
	r.key(kit.KeyArrowRight, 0) // back to the whole row
	if tb.colIndex() != -1 {
		t.Errorf("Right from the first cell gave %d, want the row (-1)", tb.colIndex())
	}
	// in the header row Ctrl+Left widens the column right to left
	r.key(kit.KeyArrowUp, kit.ModCtrl) // Ctrl+Up does not enter the header
	tb.setHeader(true)
	tb.setCol(1) // Size
	r.key(kit.KeyArrowLeft, kit.ModCtrl)
	if w := widthOfKey(&tb.layout, "size"); w != 96 {
		t.Errorf("size after Ctrl+Left right to left %v, want 96", w)
	}
	// the cells mirror too: the name cell is at the row's right
	k, _ := tb.sel.Cursor()
	rw, _ := tb.RowWidget(k).(*row)
	if rw == nil {
		t.Fatal("no cursor row")
	}
	nc, sc := rw.cells[columnOf("name")], rw.cells[columnOf("size")]
	if !(nc.Bounds().Min.X > sc.Bounds().Min.X) {
		t.Errorf("name cell %v is not right of the size cell %v", nc.Bounds(), sc.Bounds())
	}
}

// Columns drop from the right when narrow and come back when wide, in today's order.
func TestColumnsDropFromTheRight(t *testing.T) {
	for _, c := range []struct {
		w    float64
		want []string
	}{
		{828, []string{"name", "size", "progress", "speed", "eta", "status"}},
		{888, []string{"name", "size", "progress", "speed", "eta", "connections", "status"}},
		{960, []string{"name", "size", "progress", "speed", "eta", "connections", "status", "added"}},
		{500, []string{"name", "progress", "status"}},
	} {
		cols := NewColumns(nil)
		l := cols.Fit(c.w, 16, 16)
		if !slices.Equal(shownKeys(&l), c.want) {
			t.Errorf("%v px: %v, want %v", c.w, shownKeys(&l), c.want)
		}
		// the grid scrolls sideways only below the shown columns' least width
		if l.MinInline > c.w+0.01 && c.w >= 600 {
			t.Errorf("%v px: least width %v", c.w, l.MinInline)
		}
	}
	_ = display.Columns
}
