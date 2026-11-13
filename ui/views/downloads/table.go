package downloads

import (
	"slices"
	"time"

	"downex/ui/core/display"
	"downex/ui/core/query"
	"downex/ui/kit"
	"downex/ui/kit/widgets"
	"downex/ui/state"
)

// The grid's accessible texts (DownloadTable.svelte).
const (
	gridLabel      = "Downloads"
	gridLabelStale = "Downloads, last known state"
	// gridKeys is the grid's description (its hidden #keys paragraph).
	gridKeys = "Up and Down move between downloads, Left and Right between columns. Up from the first row " +
		"reaches the column headers, where Enter sorts. I reads the progress of the current download."
	gridShortcuts = "Control+A Delete Space Enter I"
)

// Table is DownloadTable.svelte's grid: the kit's virtual list of the visible downloads with
// the column headers, the keyboard's header row and cell cursor, sorting and column resizing.
type Table struct {
	*kit.List
	dl     *Downloads
	model  *listModel
	sel    *listSelection
	header *header
	cols   *Columns
	layout Layout

	// inHeader and col are the keyboard's position: the header row, and a column (-1: the
	// whole row).
	inHeader bool
	col      int

	stale   bool
	compact bool
	rem     float64
	// scrollX is the sideways offset; viewW the rows' visible width (without the vertical bar).
	scrollX kit.DIP
	viewW   kit.DIP
	sort    query.SortSpec
	gray    *grayTheme
}

func newTable(dl *Downloads) *Table {
	t := &Table{dl: dl, model: newListModel(), cols: NewColumns(dl.app.Storage), col: -1}
	t.sel = &listSelection{store: dl.store, clicked: func() { t.setHeader(false) }}
	t.List = kit.NewList(t.model, func() kit.ListRow { return newRow(t, false) }, t.rowHeight)
	t.Overscan = 10
	t.Selection = t.sel
	t.RowSemantics = t
	t.Semantics.Role = kit.RoleGrid
	t.Semantics.Name = gridLabel
	t.Semantics.Description = gridKeys
	t.Semantics.Shortcut = gridShortcuts
	t.Semantics.States |= kit.NodeMultiSelectable
	t.Ring = kit.Ring{Offset: kit.Px(-2)}
	t.OnOpen = func(k kit.ListKey) {
		if d, ok := dl.store.Get(int64(k)); ok {
			dl.open(d)
		}
	}
	t.OnMenu = func(_ kit.ListKey, at kit.Point) {
		t.setHeader(false)
		t.FocusList()
		dl.openMenu(at)
	}
	t.header = newHeader(t)
	t.SetHeader(t.header)
	t.gray = &grayTheme{base: dl.app.WidgetTheme}
	t.sort = dl.store.Sort()
	return t
}

func (t *Table) store() *state.DownloadsStore { return t.dl.store }

func (t *Table) baseTheme() widgets.Theme { return t.dl.app.WidgetTheme }
func (t *Table) grayTheme() widgets.Theme { return t.gray }

// rowHeight is the row height for the window's text size and the density (rowHeightFor).
func (t *Table) rowHeight(s kit.Scale) kit.DIP {
	return kit.DIP(display.RowHeightFor(t.remFor(s), t.compact))
}

// remFor is the table's px per rem: the window's text size when the table first lays out,
// kept from then on, as DownloadTable reads the root font size once in onMount (row and header
// heights, default column widths and the resize step follow it; the type and Name's minimum
// follow the page's).
func (t *Table) remFor(s kit.Scale) float64 {
	if t.rem == 0 {
		t.rem = float64(s.RemBase())
	}
	return t.rem
}

// FixRem pins the table's px per rem (a table that mounted at another text size, as the 0.9.2
// references' was: their text size changed after load). 0 measures it again.
func (t *Table) FixRem(px float64) {
	t.rem = px
	t.columnsChanged()
}

func (t *Table) nowMS() float64 { return float64(t.dl.app.Clock.Now().UnixMilli()) }

// acting reports whether rows offer their hover and inline actions (onaction is given).
func (t *Table) acting() bool { return true }

func (t *Table) rtl() bool {
	w := t.Window()
	return w != nil && w.Direction() == kit.DirRTL
}

// ringShown reports whether the grid shows its keyboard focus (.grid:focus-visible).
func (t *Table) ringShown() bool {
	w := t.Window()
	return t.Focused() && w != nil && w.RingShown()
}

// rowState is how a row looks now (the list's state for a bound row; nothing for a ghost).
func (t *Table) rowState(r *row) kit.RowState {
	if r.ghost {
		return kit.RowState{}
	}
	st := t.RowState(r.key)
	st.Hovered = r.Hovered()
	return st
}

// motionOf is a row's arrival or departure: its opacity, its offset and the arrival tint.
func (t *Table) motionOf(r *row) (alpha, dy, tint float32) {
	if r.ghost {
		a, d, ti, _ := t.dl.grid.ghosts.ghostOf(r)
		return a, d, ti
	}
	alpha, dy, tint = t.dl.arrival(int64(r.key))
	return alpha, dy + r.slide, tint
}

// FocusList focuses the grid at its cursor row (table.focus()).
func (t *Table) FocusList() {
	if w := t.Window(); w != nil {
		w.Focus(t)
	}
}

// colIndex is the active column in the shown ones, or -1 (the whole row).
func (t *Table) colIndex() int {
	if t.col < 0 {
		return -1
	}
	return min(t.col, len(t.layout.Shown)-1)
}

func (t *Table) setHeader(on bool) {
	if t.inHeader == on {
		return
	}
	t.inHeader = on
	t.positionChanged()
}

func (t *Table) setCol(c int) {
	if t.col == c {
		return
	}
	t.col = c
	t.positionChanged()
}

// positionChanged repaints what shows the keyboard's position and tells assistive technology.
func (t *Table) positionChanged() {
	t.header.Invalidate()
	for _, x := range t.header.ths {
		x.Invalidate()
	}
	if k, ok := t.sel.Cursor(); ok {
		if r, ok := t.RowWidget(k).(*row); ok {
			r.Invalidate()
			for _, c := range r.cells {
				c.Invalidate()
			}
		}
	}
	t.SemanticsChanged()
	t.SelectionChanged()
}

// fitColumns lays the columns out for the rows' width (the header calls it as it is laid out).
func (t *Table) fitColumns(width, rem, cssRem float64) {
	next := t.cols.Fit(width, rem, cssRem)
	if sameLayout(&next, &t.layout) {
		return
	}
	t.layout = next
	first, last := t.Realised()
	last = min(last, t.model.Len())
	for i := first; i < last; i++ {
		if r, ok := t.RowWidget(t.model.Key(i)).(*row); ok {
			r.RelayoutContent()
		}
	}
	t.dl.grid.relayoutGhosts()
	t.SemanticsChanged()
}

func sameLayout(a, b *Layout) bool {
	if len(a.Shown) != len(b.Shown) || a.Rem != b.Rem {
		return false
	}
	for i := range a.Shown {
		if a.Shown[i].Key != b.Shown[i].Key || a.W[i] != b.W[i] || a.X[i] != b.X[i] {
			return false
		}
	}
	return true
}

// columnsChanged lays the header and the rows out again for new widths.
func (t *Table) columnsChanged() {
	t.dl.grid.Relayout()
	t.header.Relayout()
	t.RelayoutContent()
	t.Invalidate()
}

// resetWidth gives a column its default width back (a double click on its handle).
func (t *Table) resetWidth(c display.Column) {
	t.cols.Reset(c.Key)
	t.columnsChanged()
}

// ---- the stores ------------------------------------------------------------------------------

// refresh follows the store: the rows shown and their order, the rows whose download changed,
// the selection, the sort and staleness.
func (t *Table) refresh() {
	s := t.store()
	stale := s.Stale()
	rebind := false
	if stale != t.stale {
		t.stale = stale
		t.Semantics.Name = gridLabel
		if stale {
			t.Semantics.Name = gridLabelStale
		}
		t.SemanticsChanged()
		rebind = true
	}
	if t.model.set(s.VisibleIDs()) {
		t.RowsChanged()
	}
	if sort := s.Sort(); sort != t.sort {
		t.sort = sort
		t.header.Invalidate()
		for _, x := range t.header.ths {
			x.btn.Invalidate()
			x.btn.arrow.Invalidate()
			x.SemanticsChanged()
		}
	}
	t.rebindChanged(rebind)
	t.SelectionChanged()
}

// rebindChanged binds again the realised rows whose download changed (all with every).
func (t *Table) rebindChanged(every bool) {
	first, last := t.Realised()
	last = min(last, t.model.Len())
	s := t.store()
	for i := first; i < last; i++ {
		k := t.model.Key(i)
		r, ok := t.RowWidget(k).(*row)
		if !ok {
			continue
		}
		d, _ := s.Get(int64(k))
		if every || d != r.d || r.compact != t.compact {
			t.RowChanged(k)
		}
	}
}

// setCompact follows the density: the row height and every row's look.
func (t *Table) setCompact(c bool) {
	if c == t.compact {
		return
	}
	t.compact = c
	t.RelayoutContent()
	t.rebindChanged(true)
	t.Invalidate()
}

// ---- keys ----------------------------------------------------------------------------------

// firstRowInView is the id of the first row at least partly in view.
func (t *Table) firstRowInView() (kit.ListKey, bool) {
	i := t.FirstVisible()
	if i < 0 {
		return 0, false
	}
	return t.model.Key(i), true
}

func (t *Table) focusIndex() int {
	k, ok := t.sel.Cursor()
	if !ok {
		return -1
	}
	if i, ok := t.model.Index(k); ok {
		return i
	}
	return -1
}

// HandleKey is kit.KeyHandler's: DownloadTable's onkeydown. Left and Right move the cell
// cursor (mirrored right to left) or, in the header row, the column (Ctrl resizes it by a
// rem); Up from the first row enters the header row, where Enter and Space sort; I reads the
// cursor row's progress; Escape leaves the cell, then clears the selection. The rest is the
// list's (rows, pages, Ctrl+Space, the menu key).
func (t *Table) HandleKey(e kit.KeyEvent) bool {
	if e.Action != kit.KeyDown {
		return false
	}
	shift := e.Mods&kit.ModShift != 0
	toggle := e.Mods&(kit.ModCtrl|kit.ModMeta) != 0
	alt := e.Mods&kit.ModAlt != 0
	total := t.model.Len()
	last := len(t.layout.Shown) - 1
	s := t.store()

	if e.Key == kit.KeyArrowLeft || e.Key == kit.KeyArrowRight {
		forward := (e.Key == kit.KeyArrowRight) != t.rtl()
		if t.inHeader {
			current := max(t.colIndex(), 0)
			if toggle && current <= last {
				if c := t.layout.Shown[current]; c.Width > 0 {
					d := t.layout.Rem
					if !forward {
						d = -d
					}
					if t.cols.ResizeBy(c, d, t.layout.Rem) {
						t.columnsChanged()
					}
					return true
				}
			}
			step := 1
			if !forward {
				step = -1
			}
			t.setCol(min(last, max(0, current+step)))
			return true
		}
		if t.focusIndex() < 0 {
			k, ok := t.firstRowInView()
			if !ok {
				return true
			}
			t.sel.SetCursor(k)
		}
		ci := t.colIndex()
		if forward {
			if ci < 0 {
				t.setCol(0)
			} else {
				t.setCol(min(last, ci+1))
			}
		} else if ci <= 0 {
			t.setCol(-1)
		} else {
			t.setCol(ci - 1)
		}
		t.SelectionChanged()
		return true
	}

	if t.inHeader {
		switch e.Key {
		case kit.KeyArrowUp:
			return true
		case kit.KeyArrowDown:
			t.setHeader(false)
			if fi := t.focusIndex(); fi < 0 && total > 0 {
				if k, ok := t.firstRowInView(); ok {
					t.sel.SetCursor(k)
				}
			} else {
				t.EnsureVisible(fi)
			}
			t.SelectionChanged()
			return true
		case kit.KeyEnter, kit.KeySpace:
			if ci := max(t.colIndex(), 0); ci <= last {
				s.SortBy(query.SortKey(t.layout.Shown[ci].Key))
			}
			return true
		case kit.KeyEscape:
			t.setHeader(false)
			return true
		case kit.KeyHome, kit.KeyEnd, kit.KeyPageUp, kit.KeyPageDown:
			t.setHeader(false)
		default:
			return false
		}
	}

	if e.Key == kit.KeyArrowUp && !shift && !toggle && (t.focusIndex() == 0 || total == 0) {
		t.inHeader = true
		t.col = max(t.colIndex(), 0)
		t.positionChanged()
		return true
	}

	switch e.Key {
	case kit.KeyEnter:
		if d, ok := s.Detail(); ok {
			t.dl.open(d)
		}
		return true
	case kit.KeyI:
		if toggle || alt {
			return false
		}
		k, ok := t.sel.Cursor()
		if !ok {
			return false
		}
		d, ok := s.Get(int64(k))
		if !ok {
			return false
		}
		if w := t.Window(); w != nil {
			w.Announce(display.ProgressSummary(d, t.stale), kit.LivePolite, t)
		}
		t.dl.announced = display.ProgressSummary(d, t.stale)
		return true
	case kit.KeyEscape:
		if t.col >= 0 {
			t.setCol(-1)
			return true
		}
		if s.Selection().Len() > 0 {
			s.ClearSelection()
			t.SelectionChanged()
			return true
		}
		return false
	}
	return t.List.HandleKey(e)
}

// FocusChanged is kit.FocusHandler's: the list's, and the header's ring.
func (t *Table) FocusChanged(focused bool) {
	t.List.FocusChanged(focused)
	t.positionChanged()
}

// ---- semantics --------------------------------------------------------------------------------

// FillSemantics is kit.SemanticFiller's: the rows (with the header) and the shown columns.
func (t *Table) FillSemantics(n *kit.SemanticNode) {
	t.List.FillSemantics(n)
	n.Grid.Cols = len(t.layout.Shown)
}

// ActiveDescendant is the keyboard's position: the header cell, the cursor row's active cell,
// or the cursor row.
func (t *Table) ActiveDescendant() kit.NodeID {
	ci := t.colIndex()
	if t.inHeader {
		if ci < 0 || ci >= len(t.layout.Shown) {
			return 0
		}
		key := t.layout.Shown[ci].Key
		for _, x := range t.header.ths {
			if x.col.Key == key {
				return x.NodeID()
			}
		}
		return 0
	}
	if ci >= 0 {
		if k, ok := t.sel.Cursor(); ok {
			if r, ok := t.RowWidget(k).(*row); ok {
				if c := columnOf(t.layout.Shown[ci].Key); c >= 0 {
					return r.cells[c].NodeID()
				}
			}
		}
	}
	return t.List.ActiveDescendant()
}

// RowNode is kit.ListSemantics': a row that is not laid out, named by its name and status.
func (t *Table) RowNode(_ int, k kit.ListKey) kit.SemanticNode {
	n := kit.SemanticNode{Role: kit.RoleRow}
	if d, ok := t.store().Get(int64(k)); ok {
		n.Name = t.store().NameOf(d.GetId()) + " " + display.StatusText(d)
		n.Description = display.RowDescription(d, t.nowMS())
	}
	return n
}

// Cells is kit.ListSemantics': one per shown column.
func (t *Table) Cells() int { return len(t.layout.Shown) }

// CellNode is kit.ListSemantics'.
func (t *Table) CellNode(_ int, _ kit.ListKey, _ int) kit.SemanticNode {
	return kit.SemanticNode{Role: kit.RoleCell}
}

var _ = slices.Equal[[]int64]
var _ = time.Second

// HeaderBounds is the header row's box in window coordinates.
func (t *Table) HeaderBounds() kit.Rect { return t.header.WindowBounds() }

// BodyBounds is the rows' box in window coordinates (div.body: as wide as the header, as
// tall as every row), unscrolled.
func (t *Table) BodyBounds() kit.Rect {
	h := t.header.WindowBounds()
	n := kit.DIP(t.model.Len()) * t.RowHeightNow()
	return kit.R(h.Min.X, h.Max.Y, h.Max.X, h.Max.Y+n)
}

// Paint is kit.Painter's: the grid's --surface ground.
func (t *Table) Paint(p *kit.PaintContext) {
	sz := p.Size()
	p.FillRect(kit.R(0, 0, sz.W, sz.H), tok(&t.Element).Surface)
}
