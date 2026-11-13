package kit_test

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"downex/ui/kit"
	"downex/ui/kit/kittest"
)

// downloads is a list model of n rows keyed 1..n, with the data a row shows.
type downloads struct {
	keys  []kit.ListKey
	index map[kit.ListKey]int
	pct   map[kit.ListKey]int
}

func newDownloads(n int) *downloads {
	m := &downloads{index: make(map[kit.ListKey]int, n), pct: map[kit.ListKey]int{}}
	for i := range n {
		k := kit.ListKey(i + 1)
		m.keys = append(m.keys, k)
		m.index[k] = i
	}
	return m
}

func (m *downloads) Len() int              { return len(m.keys) }
func (m *downloads) Key(i int) kit.ListKey { return m.keys[i] }
func (m *downloads) Index(k kit.ListKey) (int, bool) {
	i, ok := m.index[k]
	return i, ok
}

// RowNode, Cells and CellNode describe the rows that are not laid out.
func (m *downloads) RowNode(i int, k kit.ListKey) kit.SemanticNode {
	return kit.SemanticNode{Role: kit.RoleRow, Name: fmt.Sprintf("download-%05d-with-a-long-name.iso, Downloading", k)}
}
func (m *downloads) Cells() int { return 5 }
func (m *downloads) CellNode(i int, k kit.ListKey, col int) kit.SemanticNode {
	return kit.SemanticNode{Role: kit.RoleCell, Name: fmt.Sprintf("cell %d of %d", col, k)}
}

func (m *downloads) remove(k kit.ListKey) {
	i := m.index[k]
	m.keys = slices.Delete(m.keys, i, i+1)
	delete(m.index, k)
	for j := i; j < len(m.keys); j++ {
		m.index[m.keys[j]] = j
	}
}

// downloadRow is a row of the download grid in miniature: a name that ends in an
// ellipsis, a size, a percentage, a speed and a status, in a flex row.
type downloadRow struct {
	kit.Element
	m     *downloads
	list  *kit.List
	key   kit.ListKey
	binds int
	name  *kit.Label
	cells []*kit.Label
}

func newDownloadRow(m *downloads, l **kit.List) func() kit.ListRow {
	return func() kit.ListRow {
		r := &downloadRow{m: m}
		r.Style = kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, Padding: kit.EdgesInline(kit.Px(14))}
		r.Semantics.Role = kit.RoleRow
		r.name = kit.NewLabel("", font13, kit.TextOptions{Truncate: kit.TruncateEnd})
		r.name.Style = kit.Style{Flex: kit.Flex1, Overflow: kit.OverflowHidden}
		r.name.Semantics.Role = kit.RoleCell
		r.Add(r.name)
		for c := range 4 {
			lb := kit.NewLabel("", font13, kit.TextOptions{Align: kit.TextAlignEnd})
			lb.Style = kit.Style{Width: kit.Rem(5), Flex: kit.FlexNone}
			lb.Semantics.Role = kit.RoleCell
			lb.Semantics.Cell.Col = c + 1
			r.cells = append(r.cells, lb)
			r.Add(lb)
		}
		r.list = *l
		return r
	}
}

func (r *downloadRow) BindRow(i int, k kit.ListKey) {
	r.key = k
	r.binds++
	r.name.SetText(fmt.Sprintf("download-%05d-with-a-long-name.iso", k))
	r.cells[0].SetText("6.3 GB")
	r.cells[1].SetText(fmt.Sprintf("%d%%", r.m.pct[k]))
	r.cells[2].SetText(fmt.Sprintf("%d.0 MB/s", r.m.pct[k]%10))
	r.cells[3].SetText("Downloading")
}

func (r *downloadRow) Paint(p *kit.PaintContext) {
	st := r.list.RowState(r.key)
	switch {
	case st.Selected:
		p.FillRect(kit.RectAt(kit.Point{}, p.Size()), kit.RGB(0xeaf6ef))
	case st.Hovered:
		p.FillRect(kit.RectAt(kit.Point{}, p.Size()), kit.RGB(0xf2f6f4))
	}
}

// selection follows lib/selection.ts's rules, for the tests.
type selection struct {
	m      *downloads
	ids    map[kit.ListKey]bool
	anchor kit.ListKey
	cursor kit.ListKey
}

func newSelection(m *downloads) *selection { return &selection{m: m, ids: map[kit.ListKey]bool{}} }

func (s *selection) Selected(k kit.ListKey) bool { return s.ids[k] }
func (s *selection) Cursor() (kit.ListKey, bool) { return s.cursor, s.cursor != 0 }
func (s *selection) SetCursor(k kit.ListKey)     { s.cursor = k }
func (s *selection) only(k kit.ListKey) {
	s.ids = map[kit.ListKey]bool{k: true}
	s.anchor, s.cursor = k, k
}
func (s *selection) span(a, b kit.ListKey, add bool) {
	ia, _ := s.m.Index(a)
	ib, _ := s.m.Index(b)
	if !add {
		s.ids = map[kit.ListKey]bool{}
	}
	for i := min(ia, ib); i <= max(ia, ib); i++ {
		s.ids[s.m.Key(i)] = true
	}
}
func (s *selection) Click(k kit.ListKey, m kit.SelectMods) {
	switch {
	case m.Shift:
		a := s.anchor
		if a == 0 {
			a = k
		}
		s.span(a, k, m.Toggle)
		s.anchor, s.cursor = a, k
	case m.Toggle:
		s.ids[k] = !s.ids[k]
		if !s.ids[k] {
			delete(s.ids, k)
		}
		s.anchor, s.cursor = k, k
	default:
		s.only(k)
	}
}
func (s *selection) ContextClick(k kit.ListKey) {
	if s.ids[k] {
		s.cursor = k
		return
	}
	s.only(k)
}
func (s *selection) Move(by kit.ListMove, m kit.SelectMods) {
	n := s.m.Len()
	cur := -1
	if s.cursor != 0 {
		cur, _ = s.m.Index(s.cursor)
	}
	var next int
	switch {
	case by == kit.MoveFirst:
		next = 0
	case by == kit.MoveLast:
		next = n - 1
	case cur < 0 && by > 0:
		next = 0
	case cur < 0:
		next = n - 1
	default:
		next = min(n-1, max(0, cur+int(by)))
	}
	k := s.m.Key(next)
	switch {
	case m.Shift:
		a := s.anchor
		if a == 0 {
			a = k
		}
		s.span(a, k, m.Toggle)
		s.anchor, s.cursor = a, k
	case m.Toggle:
		s.cursor = k
	default:
		s.only(k)
	}
}
func (s *selection) ToggleCursor() {
	if s.cursor == 0 {
		return
	}
	if s.ids[s.cursor] {
		delete(s.ids, s.cursor)
	} else {
		s.ids[s.cursor] = true
	}
	s.anchor = s.cursor
}
func (s *selection) AppendSelected(dst []kit.ListKey) []kit.ListKey {
	for _, k := range s.m.keys {
		if s.ids[k] {
			dst = append(dst, k)
		}
	}
	return dst
}

// comfortable is DownloadTable's comfortable row height: max(44, round(rem × 2.875)).
func comfortable(s kit.Scale) kit.DIP {
	return max(44, kit.DIP(int(s.Rem(2.875)+0.5)))
}

type gridFixture struct {
	w     *kit.Window
	loop  *kittest.Loop
	list  *kit.List
	m     *downloads
	sel   *selection
	head  *kit.Element
	opens []kit.ListKey
	menus []kit.ListKey
}

// newGrid opens a window whose content is a 30 px header over a list of n rows.
func newGrid(t testing.TB, n int, size kit.Size) *gridFixture {
	return newGridEnv(t, n, size, env100)
}

// newGridEnv is newGrid in env.
func newGridEnv(t testing.TB, n int, size kit.Size, env kit.Environment) *gridFixture {
	f := &gridFixture{m: newDownloads(n)}
	f.sel = newSelection(f.m)
	var l *kit.List
	l = kit.NewList(f.m, newDownloadRow(f.m, &l), comfortable)
	l.Style = kit.Style{Flex: kit.Flex1, MinHeight: kit.Px(0)}
	l.Semantics.Role = kit.RoleGrid
	l.Semantics.Name = "Downloads"
	l.Ring = kit.Ring{Offset: kit.Px(-2)}
	l.Selection = f.sel
	l.RowSemantics = f.m
	l.OnOpen = func(k kit.ListKey) { f.opens = append(f.opens, k) }
	l.OnMenu = func(k kit.ListKey, _ kit.Point) { f.menus = append(f.menus, k) }
	f.head = &kit.Element{Style: kit.Style{Height: kit.Px(30)}}
	f.head.Semantics.Role = kit.RoleRow
	l.SetHeader(f.head)
	f.list = l
	page := box(kit.Style{Display: kit.DisplayFlex, Axis: kit.AxisColumn}, l)
	f.w, f.loop = open(t, page, size, env)
	return f
}

func (f *gridFixture) key(e kit.KeyEvent) bool {
	if e.Action == 0 {
		e.Action = kit.KeyDown
	}
	used := f.loop.Send(e)
	f.loop.Settle(10)
	return used
}

// TestListVirtualizes checks the realised range: the rows in view and ten more each way.
func TestListVirtualizes(t *testing.T) {
	f := newGrid(t, 10_000, kit.Size{W: 1200, H: 760})
	l := f.list
	// Body 730 = 760 - 30 of 46 px rows: 16 in view, then 10 more.
	if first, last := l.Realised(); first != 0 || last != 26 {
		t.Fatalf("realised %d..%d at the top, want 0..26", first, last)
	}
	l.ScrollTo(5000 * 46)
	f.loop.Settle(10)
	if first, last := l.Realised(); first != 4990 || last != 5026 {
		t.Errorf("realised %d..%d at row 5000, want 4990..5026", first, last)
	}
	if s := l.Stats(); s.RowsMade > 37 {
		t.Errorf("%d row widgets made, want at most 37 (16 in view + 1 + 20 overscan)", s.RowsMade)
	}
	// The rows' geometry: the gutter leaves 15 DIPs for the scroll bar, and row 5000 is
	// right under the header.
	r := l.RowWidget(f.m.Key(5000)).Base().WindowBounds()
	if r != kit.R(0, 30, 1185, 76) {
		t.Errorf("row 5000 is %v, want (0,30)-(1185,76)", r)
	}
}

// TestListRecycles scrolls through 10,000 rows and checks that the row widgets and text
// layouts stay few.
func TestListRecycles(t *testing.T) {
	f := newGrid(t, 10_000, kit.Size{W: 1200, H: 760})
	l := f.list
	res := f.loop.Host.Res
	widgets := map[kit.ListRow]bool{}
	for l.ScrollOffset() < 10_000*46+30-760 {
		f.loop.Send(kit.WheelEvent{Pos: kit.Pt(600, 400), Delta: kit.Pt(0, 700), Mode: kit.WheelLines})
		f.loop.Settle(10)
		first, last := l.Realised()
		for i := first; i < last; i++ {
			widgets[l.RowWidget(f.m.Key(i))] = true
		}
	}
	if first, last := l.Realised(); last != 10_000 || first != 10_000-16-10 {
		t.Errorf("at the end, realised %d..%d", first, last)
	}
	s := l.Stats()
	if s.RowsMade > 37 || len(widgets) != s.RowsMade {
		t.Errorf("%d row widgets made (%d seen), want at most 37", s.RowsMade, len(widgets))
	}
	if s.Binds < 10_000 {
		t.Errorf("%d binds for 10,000 rows", s.Binds)
	}
	// Each label holds at most two layouts; the pool keeps its widgets' layouts.
	if max := s.RowsMade * 5 * 2; res.Live > max+10 {
		t.Errorf("%d live text layouts, want at most %d", res.Live, max)
	}
	t.Logf("rows made %d, binds %d, live text layouts %d (made %d)", s.RowsMade, s.Binds, res.Live, res.Made)
}

// TestListRowRepaint checks that a row update lays out and repaints that row alone.
func TestListRowRepaint(t *testing.T) {
	f := newGrid(t, 10_000, kit.Size{W: 1200, H: 760})
	l, h := f.list, f.loop.Host
	f.loop.Settle(10)
	before := f.w.Stats()
	h.Invalidations = nil
	k := f.m.Key(3)
	f.m.pct[k] = 42
	l.RowChanged(k)
	f.loop.Step()
	row := l.RowWidget(k).Base().WindowBounds()
	var union kit.Rect
	for _, r := range h.Invalidations {
		union = union.Union(r)
	}
	if !approxRect(union.Intersect(row), union) || union.Empty() {
		t.Errorf("invalidated %v, which is not inside row %v", union, row)
	}
	if !approxRect(f.loop.Painted, row) {
		t.Errorf("painted %v, want the row %v", f.loop.Painted, row)
	}
	s := f.w.Stats()
	if s.Layouts != before.Layouts+1 || s.Paints != before.Paints+1 {
		t.Errorf("one row update: %d layouts, %d paints", s.Layouts-before.Layouts, s.Paints-before.Paints)
	}
	if n := s.BoxLayouts - before.BoxLayouts; n > 7 {
		t.Errorf("one row update laid out %d boxes, want the row's own (at most 7)", n)
	}
	if got := l.RowWidget(k).(*downloadRow).cells[1].Text(); got != "42%" {
		t.Errorf("cell = %q", got)
	}
	// An update of a row that is not realised costs nothing.
	h.Invalidations = nil
	l.RowChanged(f.m.Key(9000))
	if len(h.Invalidations) != 0 || h.FrameWanted {
		t.Errorf("an update off screen invalidated %v", h.Invalidations)
	}
}

// TestListKeyboard drives the cursor as DownloadTable's keys do.
func TestListKeyboard(t *testing.T) {
	f := newGrid(t, 10_000, kit.Size{W: 1200, H: 760})
	l := f.list
	f.key(kit.KeyEvent{Key: kit.KeyTab})
	if f.w.Focused() != &l.Element {
		t.Fatal("Tab did not focus the list")
	}
	if k, ok := f.sel.Cursor(); !ok || k != f.m.Key(0) {
		t.Fatalf("focus put the cursor on %v, want the first row in view", k)
	}
	cursor := func() int {
		k, _ := f.sel.Cursor()
		i, _ := f.m.Index(k)
		return i
	}
	for range 3 {
		f.key(kit.KeyEvent{Key: kit.KeyArrowDown})
	}
	if cursor() != 3 || !f.sel.Selected(f.m.Key(3)) || len(f.sel.ids) != 1 {
		t.Errorf("after 3 downs: cursor %d, selection %v", cursor(), f.sel.ids)
	}
	f.key(kit.KeyEvent{Key: kit.KeyPageDown})
	if page := 730/46 - 1; cursor() != 3+page {
		t.Errorf("Page Down moved to %d, want %d", cursor(), 3+page)
	}
	f.key(kit.KeyEvent{Key: kit.KeyEnd})
	if cursor() != 9999 || l.ScrollOffset() != 10_000*46+30-760 {
		t.Errorf("End: cursor %d, scroll %v", cursor(), l.ScrollOffset())
	}
	f.key(kit.KeyEvent{Key: kit.KeyHome})
	if cursor() != 0 || l.ScrollOffset() != 0 {
		t.Errorf("Home: cursor %d, scroll %v", cursor(), l.ScrollOffset())
	}
	f.key(kit.KeyEvent{Key: kit.KeyArrowDown, Mods: kit.ModShift})
	f.key(kit.KeyEvent{Key: kit.KeyArrowDown, Mods: kit.ModShift})
	if len(f.sel.ids) != 3 {
		t.Errorf("Shift+Down twice selects 3 rows, got %v", f.sel.ids)
	}
	f.key(kit.KeyEvent{Key: kit.KeyArrowDown, Mods: kit.ModCtrl})
	if cursor() != 3 || len(f.sel.ids) != 3 {
		t.Errorf("Ctrl+Down moves the cursor only: cursor %d, %d selected", cursor(), len(f.sel.ids))
	}
	f.key(kit.KeyEvent{Key: kit.KeySpace, Mods: kit.ModCtrl})
	if !f.sel.Selected(f.m.Key(3)) {
		t.Error("Ctrl+Space toggles the cursor's row")
	}
	if f.key(kit.KeyEvent{Key: kit.KeySpace}) {
		t.Error("plain Space is the window's (pause and resume), not the list's")
	}
	f.key(kit.KeyEvent{Key: kit.KeyEnter})
	if len(f.opens) != 1 || f.opens[0] != f.m.Key(3) {
		t.Errorf("Enter opened %v", f.opens)
	}
	f.key(kit.KeyEvent{Key: kit.KeyF10, Mods: kit.ModShift})
	if len(f.menus) != 1 {
		t.Errorf("Shift+F10 opened %d menus", len(f.menus))
	}
	// The cursor row repaints when it moves: only the two rows involved.
	f.loop.Host.Invalidations = nil
	f.key(kit.KeyEvent{Key: kit.KeyArrowUp, Mods: kit.ModCtrl})
	var union kit.Rect
	for _, r := range f.loop.Host.Invalidations {
		union = union.Union(r)
	}
	if union.Height() > 2*46+2 {
		t.Errorf("moving the cursor repainted %v, more than its two rows", union)
	}
}

// TestListPointer checks click, Shift and Ctrl clicks (on release), a double click and a
// secondary click, and the hover row.
func TestListPointer(t *testing.T) {
	f := newGrid(t, 100, kit.Size{W: 800, H: 600})
	rowY := func(i int) kit.DIP { return 30 + kit.DIP(i)*46 + 20 }
	click := func(i int, mods kit.Modifiers, clicks int) {
		p := kit.Pt(200, rowY(i))
		f.loop.Send(kit.PointerEvent{Action: kit.PointerDown, Pos: p, Button: kit.ButtonLeft, Buttons: kit.ButtonLeft, Mods: mods, Clicks: clicks})
		f.loop.Send(kit.PointerEvent{Action: kit.PointerUp, Pos: p, Button: kit.ButtonLeft, Mods: mods})
		f.loop.Settle(10)
	}
	click(2, 0, 1)
	if !f.sel.Selected(3) || len(f.sel.ids) != 1 {
		t.Fatalf("click selected %v", f.sel.ids)
	}
	if f.w.Focused() != &f.list.Element {
		t.Error("a click on a row focuses the list")
	}
	click(5, kit.ModShift, 1)
	if len(f.sel.ids) != 4 {
		t.Errorf("Shift+click selected %v", f.sel.ids)
	}
	click(8, kit.ModCtrl, 1)
	if len(f.sel.ids) != 5 || !f.sel.Selected(9) {
		t.Errorf("Ctrl+click selected %v", f.sel.ids)
	}
	click(8, 0, 2)
	if len(f.opens) != 1 || f.opens[0] != 9 {
		t.Errorf("double click opened %v", f.opens)
	}
	// Pressed on one row and released on another: no click.
	f.loop.Send(kit.PointerEvent{Action: kit.PointerDown, Pos: kit.Pt(200, rowY(1)), Button: kit.ButtonLeft, Buttons: kit.ButtonLeft, Clicks: 1})
	f.loop.Send(kit.PointerEvent{Action: kit.PointerUp, Pos: kit.Pt(200, rowY(4)), Button: kit.ButtonLeft})
	if f.sel.Selected(2) || f.sel.Selected(5) && len(f.sel.ids) != 1 {
		t.Errorf("a drag between rows clicked: %v", f.sel.ids)
	}
	// A secondary click keeps a selection that holds the row and opens the menu.
	click(8, 0, 1)
	click(9, kit.ModCtrl, 1)
	p := kit.Pt(200, rowY(8))
	f.loop.Send(kit.PointerEvent{Action: kit.PointerDown, Pos: p, Button: kit.ButtonRight, Buttons: kit.ButtonRight, Clicks: 1})
	f.loop.Send(kit.PointerEvent{Action: kit.PointerUp, Pos: p, Button: kit.ButtonRight})
	if len(f.sel.ids) != 2 || len(f.menus) != 1 || f.menus[0] != 9 {
		t.Errorf("secondary click: selection %v, menus %v", f.sel.ids, f.menus)
	}
	// Hover marks one row and repaints only it.
	f.loop.Settle(10)
	f.loop.Host.Invalidations = nil
	f.loop.Send(kit.PointerEvent{Action: kit.PointerMove, Pos: kit.Pt(300, rowY(6))})
	if st := f.list.RowState(7); !st.Hovered {
		t.Error("the row under the pointer is not hovered")
	}
	for _, r := range f.loop.Host.Invalidations {
		if r.Height() > 46+2 {
			t.Errorf("hover invalidated %v", r)
		}
	}
}

// TestListScrollbar checks the bar against WebView2's measured one: a 28-row list in a
// 624 DIP viewport (content 30 + 28 × 46 = 1318), and a 10,000-row one.
func TestListScrollbar(t *testing.T) {
	f := newGrid(t, 28, kit.Size{W: 400, H: 624})
	bar := f.list.Scrollbar()
	if b := bar.WindowBounds(); b != kit.R(385, 0, 400, 624) {
		t.Fatalf("bar at %v, want the 15 DIP gutter at the end", b)
	}
	thumb := func() kit.Rect {
		f.loop.Canvas.Reset()
		f.w.InvalidateRect(kit.R(0, 0, 400, 624))
		f.loop.Step()
		for _, op := range f.loop.Canvas.Ops {
			if op.Kind == "FillRoundedRect" && (op.Color == testPalette.ScrollThumb || op.Color == testPalette.ScrollThumbActive) {
				return op.Rect
			}
		}
		t.Fatal("no thumb drawn")
		return kit.Rect{}
	}
	// Measured: the thumb starts 18 DIPs down (after the arrow), is 278 long and 8 wide,
	// 3.5 in from each side.
	if got, want := thumb(), kit.R(388.5, 18, 396.5, 296); got != want {
		t.Errorf("thumb %v, want %v", got, want)
	}
	// An arrow click scrolls 40; a click on the track below the thumb a page (87.5%).
	press := func(p kit.Point) {
		f.loop.Send(kit.PointerEvent{Action: kit.PointerDown, Pos: p, Button: kit.ButtonLeft, Buttons: kit.ButtonLeft, Clicks: 1})
		f.loop.Send(kit.PointerEvent{Action: kit.PointerUp, Pos: p, Button: kit.ButtonLeft})
		f.loop.Settle(10)
	}
	press(kit.Pt(392, 615))
	if got := f.list.ScrollOffset(); got != 40 {
		t.Errorf("arrow click scrolled to %v, want 40", got)
	}
	press(kit.Pt(392, 580))
	if got := f.list.ScrollOffset(); got != 40+546 {
		t.Errorf("track click scrolled to %v, want 586", got)
	}
	// Holding the down arrow repeats: 13 steps of 40 in 830 ms (250 ms, then every 50).
	f.list.ScrollTo(0)
	f.loop.Settle(10)
	big := newGrid(t, 300, kit.Size{W: 400, H: 624})
	big.loop.Send(kit.PointerEvent{Action: kit.PointerDown, Pos: kit.Pt(392, 615), Button: kit.ButtonLeft, Buttons: kit.ButtonLeft, Clicks: 1})
	big.loop.Run(830 * time.Millisecond)
	big.loop.Send(kit.PointerEvent{Action: kit.PointerUp, Pos: kit.Pt(392, 615), Button: kit.ButtonLeft})
	big.loop.Run(500 * time.Millisecond)
	if got := big.list.ScrollOffset(); got != 13*40 {
		t.Errorf("held arrow scrolled %v, want 520", got)
	}
	// Dragging the thumb by 100 DIPs scrolls by 100 × (content - view) / (track - thumb).
	f.loop.Send(kit.PointerEvent{Action: kit.PointerDown, Pos: kit.Pt(392, 100), Button: kit.ButtonLeft, Buttons: kit.ButtonLeft, Clicks: 1})
	if got := thumb(); got.Min.Y != 18 || f.loop.Canvas.Ops == nil {
		t.Errorf("thumb moved on press: %v", got)
	}
	f.loop.Send(kit.PointerEvent{Action: kit.PointerMove, Pos: kit.Pt(380, 200), Buttons: kit.ButtonLeft})
	f.loop.Send(kit.PointerEvent{Action: kit.PointerUp, Pos: kit.Pt(380, 200), Button: kit.ButtonLeft})
	f.loop.Settle(10)
	if got, want := f.list.ScrollOffset(), kit.DIP(100*(1318-624))/(588-278); !approx(got, want) {
		t.Errorf("thumb drag scrolled to %v, want %v", got, want)
	}

	// 10,000 rows: the thumb is at its 17 DIP minimum, and at an offset of 200,000 of
	// 459,406 it is 266 down (measured: 248 into the track, after the 18 DIP arrow).
	g := newGrid(t, 10_000, kit.Size{W: 400, H: 624})
	g.list.ScrollTo(200_000)
	g.loop.Settle(10)
	g.loop.Canvas.Reset()
	g.w.InvalidateRect(kit.R(0, 0, 400, 624))
	g.loop.Step()
	found := false
	for _, op := range g.loop.Canvas.Ops {
		if op.Kind == "FillRoundedRect" && op.Color == testPalette.ScrollThumb {
			found = true
			if op.Rect.Height() != 17 || op.Rect.Min.Y != 266 {
				t.Errorf("10,000-row thumb %v, want 17 long at 266", op.Rect)
			}
		}
	}
	if !found {
		t.Error("no thumb")
	}
}

// TestListSemantics checks the grid's nodes: sizes that count every row, rows realised or
// not with stable ids, the selection off screen, the active row.
func TestListSemantics(t *testing.T) {
	f := newGrid(t, 10_000, kit.Size{W: 1200, H: 760})
	tree := f.w.Semantics()
	l := f.list
	n, ok := tree.Node(l.NodeID())
	if !ok || n.Role != kit.RoleGrid || n.Grid.Rows != 10_001 || n.Name != "Downloads" {
		t.Fatalf("grid node %+v", n)
	}
	kids := tree.AppendChildren(nil, l.NodeID())
	if len(kids) != 1+26 {
		t.Errorf("grid children: %d, want the header and 26 rows", len(kids))
	}
	r5, _ := tree.Node(kids[6])
	if r5.Role != kit.RoleRow || r5.Cell.Row != 6 || r5.PosInSet != 6 || r5.SetSize != 10_000 || r5.Parent != l.NodeID() {
		t.Errorf("row 5 node %+v", r5)
	}
	// A row far away: made for the call, the same id later, also once it is realised.
	id := tree.Cell(l.NodeID(), 5001, 0)
	if id == 0 {
		t.Fatal("no cell for row 5000")
	}
	if again := tree.Cell(l.NodeID(), 5001, 0); again != id {
		t.Errorf("cell id changed: %d then %d", id, again)
	}
	cell, ok := tree.Node(id)
	if !ok || cell.States&kit.NodeOffscreen == 0 {
		t.Errorf("off-screen cell %+v", cell)
	}
	rowID := cell.Parent
	row, ok := tree.Node(rowID)
	if !ok || row.Role != kit.RoleRow || row.Cell.Row != 5001 {
		t.Errorf("off-screen row %+v %v", row, ok)
	}
	l.ScrollTo(5000*46 - 100)
	f.loop.Settle(10)
	if got := l.RowWidget(f.m.Key(5000)).Base().NodeID(); got != rowID {
		t.Errorf("realised row 5000 has id %d, it was %d", got, rowID)
	}
	if real := tree.Cell(l.NodeID(), 5001, 0); real == id {
		// realised: the cell is now the row's own element
		_ = real
	}
	l.ScrollTo(0)
	f.loop.Settle(10)
	if n, ok := tree.Node(rowID); !ok || n.States&kit.NodeOffscreen == 0 {
		t.Errorf("row 5000 scrolled away: %+v %v", n, ok)
	}
	// Selection, off-screen rows included.
	f.sel.Click(f.m.Key(1), kit.SelectMods{})
	f.sel.Click(f.m.Key(7000), kit.SelectMods{Toggle: true})
	l.SelectionChanged()
	sel := tree.AppendSelection(nil, l.NodeID())
	if len(sel) != 2 {
		t.Fatalf("selection nodes %v", sel)
	}
	if n, _ := tree.Node(sel[1]); n.States&kit.NodeSelected == 0 || n.Cell.Row != 7001 {
		t.Errorf("selected off-screen row %+v", n)
	}
	// Selecting by assistive technology, on a row that is not laid out.
	if err := tree.Perform(kit.ActionRequest{Node: sel[1], Action: kit.ActionSelect}); err != nil {
		t.Fatal(err)
	}
	if len(f.sel.ids) != 1 || !f.sel.Selected(f.m.Key(7000)) {
		t.Errorf("select by node: %v", f.sel.ids)
	}
	// The active row while the list has the focus.
	f.w.Focus(l)
	f.loop.Settle(10)
	if got := tree.Focused(); got != sel[1] {
		t.Errorf("focused node %d, want the cursor's row %d", got, sel[1])
	}
	// A removed row's node is gone.
	f.m.remove(f.m.Key(7000))
	l.RowsChanged()
	f.loop.Settle(10)
	if _, ok := tree.Node(sel[1]); ok {
		t.Error("a removed row still has a node")
	}
}

// TestListScrollKeys checks a list without a selection: the keys scroll it as Chromium
// scrolls a focused scroller (an arrow 40 DIPs, a page 87.5% of the view, Home and End).
func TestListScrollKeys(t *testing.T) {
	f := newGrid(t, 1000, kit.Size{W: 800, H: 600})
	f.list.Selection = nil
	f.w.Focus(f.list)
	keys := []struct {
		key  kit.Key
		mods kit.Modifiers
		want kit.DIP
	}{
		{kit.KeyArrowDown, 0, 40},
		{kit.KeyArrowDown, 0, 80},
		{kit.KeyArrowUp, 0, 40},
		{kit.KeyPageDown, 0, 40 + 525},
		{kit.KeySpace, kit.ModShift, 40},
		{kit.KeyEnd, 0, 1000*46 + 30 - 600},
		{kit.KeyHome, 0, 0},
	}
	for _, k := range keys {
		f.key(kit.KeyEvent{Key: k.key, Mods: k.mods})
		if got := f.list.ScrollOffset(); got != k.want {
			t.Errorf("after %#x: scrolled to %v, want %v", k.key, got, k.want)
		}
	}
}
