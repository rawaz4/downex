package kit

import (
	"fmt"
	"math"
	"slices"
)

// ListKey names a row of a List; a row keeps its key while it exists (a download's id).
type ListKey uint64

// ListModel is what a List shows: rows in order, each with its key. Its owner tells the
// list about changes (RowsChanged, RowChanged).
type ListModel interface {
	Len() int
	Key(i int) ListKey
	// Index is the row's position, or false when there is no such row.
	Index(k ListKey) (int, bool)
}

// ListRow is a widget that shows one row; the list makes as few as it needs and binds
// them again to other rows as it scrolls (recycling).
type ListRow interface {
	Widget
	// BindRow shows the row at index with key; it is called when the widget takes a row
	// and again when the row's data or position changed (RowChanged, a reorder).
	BindRow(index int, key ListKey)
}

// ListSelection is the selection the list shows and changes: hooks into its owner's
// selection model (lib/selection.ts's rules live there), called on clicks and keys.
type ListSelection interface {
	Selected(k ListKey) bool
	// Cursor is the keyboard's row (the active row), or false.
	Cursor() (ListKey, bool)
	SetCursor(k ListKey)
	// Click is a primary click on a row: Shift extends from the anchor, Toggle (Ctrl)
	// flips the row.
	Click(k ListKey, m SelectMods)
	// ContextClick is a secondary click: it keeps a selection that holds the row.
	ContextClick(k ListKey)
	// Move moves the cursor (by rows, or to MoveFirst or MoveLast): plainly it selects the
	// row, with Shift it extends from the anchor, with Toggle it moves the cursor only.
	Move(by ListMove, m SelectMods)
	// ToggleCursor flips the cursor's row (Ctrl+Space).
	ToggleCursor()
	// AppendSelected appends the selected keys, rows not laid out included.
	AppendSelected(dst []ListKey) []ListKey
}

// SelectMods are the modifiers of a selection change.
type SelectMods struct {
	Shift, Toggle bool
}

// ListMove is how far the cursor moves: rows, or MoveFirst and MoveLast.
type ListMove int

// The moves to the ends.
const (
	MoveFirst ListMove = math.MinInt32
	MoveLast  ListMove = math.MaxInt32
)

// RowState is how a row looks: what its widget reads when it paints (List.RowState).
type RowState struct {
	Selected, Cursor, Hovered bool
	// Focused: the list has the keyboard focus; FocusVisible: and shows it.
	Focused, FocusVisible bool
}

// ListSemantics describe to assistive technology the rows that are not laid out (the list
// is virtual): their role, name and states; the list adds ids, parents, positions,
// bounds and selection.
type ListSemantics interface {
	RowNode(index int, key ListKey) SemanticNode
	// Cells is the number of cells in a row, 0 for rows without cells.
	Cells() int
	CellNode(index int, key ListKey, col int) SemanticNode
}

// ListStats count what a list did.
type ListStats struct {
	RowsMade  int // row widgets made
	Binds     int // BindRow calls
	Syncs     int // times the realised rows were worked out
	Realised  int // rows realised now
	Pooled    int // row widgets waiting for a row
	NodeIDs   int // row node ids kept
	ScrollOps int // scroll offset changes
}

// List is a virtual list: of its rows only those in view, and Overscan more above and
// below, have widgets, which it recycles as it scrolls. A row update repaints only that
// row. It scrolls by wheel, by its scroll bar and by keys; with a Selection it moves a
// cursor with the arrow keys, Page Up and Page Down, Home and End, as the download grid
// does (DownloadTable.svelte), and without one the keys scroll. A sticky Header (the
// column headers) stays at the top. The list's own size comes from its parent (give it
// a flex grow or a height): its content never sizes it.
type List struct {
	Element
	// Model is what the list shows.
	Model ListModel
	// NewRow makes a row widget.
	NewRow func() ListRow
	// RowHeight is a row's height for a scale: DownloadTable's rowHeightFor follows the
	// text size (max(44, round(rem × 2.875)) for comfortable rows).
	RowHeight func(s Scale) DIP
	// Overscan is the number of rows realised beyond each edge of the view; 10 when 0.
	Overscan int
	// Selection is the selection the list shows; nil for a list that only scrolls.
	Selection ListSelection
	// RowSemantics describe the rows that are not laid out.
	RowSemantics ListSemantics
	// OnOpen is Enter on the cursor's row, or a double click on a row.
	OnOpen func(k ListKey)
	// OnMenu is a secondary click on a row, the Menu key or Shift+F10, at a window point.
	OnMenu func(k ListKey, at Point)
	// Overlay draws the scroll bar over the rows instead of in a gutter of its own, and
	// only while the pointer is over the list or the thumb is dragged.
	Overlay bool

	header *Element
	body   listBody
	bar    *Scrollbar

	rows    []*listSlot // realised, in index order
	free    []*listSlot // pooled
	byKey   map[ListKey]*listSlot
	ids     map[ListKey]NodeID
	idKeys  map[NodeID]ListKey
	cells   map[listCell]NodeID
	cellsOf map[NodeID]listCell

	scrollY    DIP
	laidScroll DIP
	laidLen    int
	semLen     int
	rowH       DIP
	headH      DIP
	view       Size
	rowW       DIP
	overflow   bool
	press      ListKey
	pressOn    bool
	clicks     int
	want       map[ListKey]int
	stats      ListStats
}

type listCell struct {
	key ListKey
	col int
}

// listSlot is one recycled row: a box of the list's own that holds the row widget.
type listSlot struct {
	box   rowBox
	row   ListRow
	index int
	key   ListKey
	state RowState
	list  *List
}

// rowBox holds a row widget: the list's relayout boundary for the row, which repaints it
// when the pointer comes or goes.
type rowBox struct {
	Element
	slot *listSlot
}

func (b *rowBox) HoverChanged(bool) { b.slot.list.refreshSlot(b.slot) }

func (b *rowBox) ContentWidths(*LayoutContext) (min, max DIP) { return 0, 0 }
func (b *rowBox) ContentHeight(*LayoutContext, DIP) DIP       { return 0 }
func (b *rowBox) LayoutContent(ctx *LayoutContext, box Rect) {
	for _, c := range b.kids {
		ctx.place(c, box, box.Width())
	}
}

// listBody is the area under the header that the rows scroll in; it clips them.
type listBody struct {
	Element
}

func (b *listBody) ContentWidths(*LayoutContext) (min, max DIP) { return 0, 0 }
func (b *listBody) ContentHeight(*LayoutContext, DIP) DIP       { return 0 }
func (b *listBody) LayoutContent(*LayoutContext, Rect)          {}
func (b *listBody) childClip() Rect                             { return b.bounds.Size().rect() }

// NewList makes a list of model's rows, each shown by a widget newRow makes, rowHeight
// high. It takes the focus with Tab; set its Semantics (RoleGrid for the downloads).
func NewList(model ListModel, newRow func() ListRow, rowHeight func(Scale) DIP) *List {
	l := &List{
		Model:     model,
		NewRow:    newRow,
		RowHeight: rowHeight,
		byKey:     map[ListKey]*listSlot{},
		ids:       map[ListKey]NodeID{},
		idKeys:    map[NodeID]ListKey{},
		cells:     map[listCell]NodeID{},
		cellsOf:   map[NodeID]listCell{},
		want:      map[ListKey]int{},
	}
	l.self = l
	l.Focus = FocusTab
	l.Semantics.Role = RoleList
	l.flags |= isBoundary
	l.body.self = &l.body
	l.bar = NewScrollbar(l)
	l.Add(&l.body, l.bar)
	return l
}

// SetHeader sets the sticky header (nil for none): it is laid out at the top, as wide as
// the rows, and its height (its own, or its content's) is the first part of what scrolls.
func (l *List) SetHeader(h Widget) {
	if l.header != nil {
		l.Remove(l.header)
		l.header = nil
	}
	if h != nil {
		l.header = h.Base()
		l.Insert(0, h)
	}
	l.RelayoutContent()
}

// Scrollbar is the list's scroll bar.
func (l *List) Scrollbar() *Scrollbar { return l.bar }

// Stats are the list's counters.
func (l *List) Stats() ListStats {
	s := l.stats
	s.Realised, s.Pooled, s.NodeIDs = len(l.rows), len(l.free), len(l.ids)
	return s
}

// ScrollOffset is how far the list is scrolled.
func (l *List) ScrollOffset() DIP { return l.scrollY }

// RowHeightNow is the row height of the last layout.
func (l *List) RowHeightNow() DIP { return l.rowH }

// FirstVisible is the first row at least partly in view, or -1 when there are none.
func (l *List) FirstVisible() int {
	n := l.Model.Len()
	if n == 0 || l.rowH <= 0 {
		return -1
	}
	return min(n-1, max(0, int(math.Floor(float64(l.scrollY/l.rowH)))))
}

// Realised are the indexes [first, last) that have row widgets.
func (l *List) Realised() (first, last int) {
	if len(l.rows) == 0 {
		return 0, 0
	}
	return l.rows[0].index, l.rows[len(l.rows)-1].index + 1
}

// RowWidget is the widget showing the row with key k, or nil when it is not realised.
func (l *List) RowWidget(k ListKey) ListRow {
	if s := l.byKey[k]; s != nil {
		return s.row
	}
	return nil
}

func (l *List) overscan() int {
	if l.Overscan > 0 {
		return l.Overscan
	}
	return 10
}

func (l *List) bodyHeight() DIP { return max(l.view.H-l.headH, 0) }

func (l *List) contentHeight() DIP { return l.headH + DIP(l.Model.Len())*l.rowH }

func (l *List) maxScroll() DIP { return max(l.contentHeight()-l.view.H, 0) }

// ---- layout -------------------------------------------------------------------------------

// ContentWidths is Layouter's: a list's content never sizes it.
func (l *List) ContentWidths(*LayoutContext) (min, max DIP) { return 0, 0 }

// ContentHeight is Layouter's.
func (l *List) ContentHeight(*LayoutContext, DIP) DIP { return 0 }

// LayoutContent is Layouter's: the header, the realised rows and the scroll bar.
func (l *List) LayoutContent(ctx *LayoutContext, box Rect) {
	l.view = box.Size()
	l.rowH = 1
	if l.RowHeight != nil {
		l.rowH = max(l.RowHeight(ctx.Scale), 1)
	}
	gutter := DIP(0)
	l.headH = 0
	measure := func(w DIP) {
		l.headH = 0
		if l.header != nil && ctx.inFlow(l.header) {
			l.headH = ctx.heightFor(l.header, w, w)
		}
	}
	measure(l.view.W)
	l.overflow = l.contentHeight() > l.view.H
	if l.overflow && !l.Overlay {
		gutter = ctx.Scale.Snap(l.bar.Metrics.Thickness)
		measure(l.view.W - gutter)
	}
	l.rowW = max(l.view.W-gutter, 0)
	l.scrollY = clampDIP(l.scrollY, 0, l.maxScroll())
	if l.scrollY != l.laidScroll && !ctx.full {
		l.Invalidate() // everything in view moved
	} else if n := l.Model.Len(); n != l.laidLen && !ctx.full {
		l.bar.Invalidate() // the thumb's length changed
	}
	l.laidLen = l.Model.Len()
	// The rows change first (binding, recycling), then everything is placed, so that what
	// the changes mark is laid out in this pass.
	l.sync()
	x := box.Min.X
	if ctx.RTL {
		x += gutter
	}
	if l.header != nil {
		ctx.place(l.header, R(x, box.Min.Y, x+l.rowW, box.Min.Y+l.headH), l.rowW)
	}
	ctx.place(&l.body.Element, R(x, box.Min.Y+l.headH, x+l.rowW, box.Max.Y), l.rowW)
	l.bar.Style.Hidden = !l.overflow || (l.Overlay && !l.Hovered() && !l.bar.dragging)
	t := ctx.Scale.Snap(l.bar.Metrics.BarWidth()) // [mac scroll] an overlay bar has no gutter
	bx := box.Max.X - t
	if ctx.RTL {
		bx = box.Min.X
	}
	ctx.place(&l.bar.Element, R(bx, box.Min.Y, bx+t, box.Max.Y), l.view.W)
	l.placeRows(ctx)
	l.laidScroll = l.scrollY
}

// placeRows lays the realised rows out where the scroll offset puts them.
func (l *List) placeRows(ctx *LayoutContext) {
	moved := l.scrollY != l.laidScroll
	if moved {
		ctx.quiet++ // the whole list is painted again
	}
	for _, s := range l.rows {
		y := DIP(s.index)*l.rowH - l.scrollY
		ctx.place(&s.box.Element, R(0, y, l.rowW, y+l.rowH), l.rowW)
	}
	if moved {
		ctx.quiet--
	}
	l.refreshStates()
}

// sync works out the rows to realise and recycles the widgets of the others.
func (l *List) sync() {
	l.stats.Syncs++
	n := l.Model.Len()
	ov := l.overscan()
	first := max(0, int(math.Floor(float64(l.scrollY/l.rowH)))-ov)
	last := min(n, int(math.Ceil(float64((l.scrollY+l.bodyHeight())/l.rowH)))+ov)
	if n == 0 || l.bodyHeight() <= 0 {
		first, last = 0, 0
	}
	clear(l.want)
	for i := first; i < last; i++ {
		l.want[l.Model.Key(i)] = i
	}
	kept := l.rows[:0]
	var gone []*listSlot
	changed := false // the realised rows or their order: the list's children
	for _, s := range l.rows {
		i, ok := l.want[s.key]
		if !ok {
			gone = append(gone, s)
			continue
		}
		delete(l.want, s.key)
		if s.index != i {
			s.index = i
			l.bind(s)
			changed = true
		}
		kept = append(kept, s)
	}
	changed = changed || len(gone) > 0 || len(l.want) > 0
	for _, s := range gone {
		l.park(s)
	}
	for i := first; i < last; i++ {
		k := l.Model.Key(i)
		if _, ok := l.want[k]; !ok {
			continue // kept
		}
		s := l.take()
		s.key, s.index = k, i
		l.byKey[k] = s
		s.box.flags &^= isParked
		l.win.rebindNode(s.row.Base(), l.rowID(k))
		l.bind(s)
		kept = append(kept, s)
	}
	slices.SortFunc(kept, func(a, b *listSlot) int { return a.index - b.index })
	l.rows = kept
	// The body's children in reading order: the realised rows, then the pooled ones.
	l.body.kids = l.body.kids[:0]
	for _, s := range l.rows {
		l.body.kids = append(l.body.kids, &s.box.Element)
	}
	for _, s := range l.free {
		l.body.kids = append(l.body.kids, &s.box.Element)
	}
	if changed && l.win != nil {
		l.win.semChildren(&l.Element)
	}
}

// take is a pooled slot, or a new one.
func (l *List) take() *listSlot {
	if n := len(l.free); n > 0 {
		s := l.free[n-1]
		l.free = l.free[:n-1]
		return s
	}
	s := &listSlot{list: l}
	s.box.slot = s
	s.box.self = &s.box
	s.box.flags |= isBoundary
	s.row = l.NewRow()
	s.box.Add(s.row)
	l.stats.RowsMade++
	l.body.Add(&s.box)
	return s
}

func (l *List) park(s *listSlot) {
	if l.byKey[s.key] == s {
		delete(l.byKey, s.key)
	}
	s.box.flags |= isParked
	s.box.flags &^= isHovered
	s.state = RowState{}
	if w := l.win; w != nil {
		if w.focused != nil && s.box.contains(w.focused) {
			w.setFocus(&l.Element)
		}
		// The row's node becomes virtual again; the widget waits with an id of its own.
		w.rebindNode(s.row.Base(), 0)
	}
	l.free = append(l.free, s)
}

func (l *List) bind(s *listSlot) {
	l.stats.Binds++
	s.row.BindRow(s.index, s.key)
	s.box.RelayoutContent()
	l.invalidateSlot(s)
}

func (l *List) invalidateSlot(s *listSlot) {
	if l.win != nil && s.box.flags&isParked == 0 {
		l.win.invalidateElement(&s.box.Element)
	}
}

// ---- the owner's changes ------------------------------------------------------------------

// RowsChanged tells the list that rows were added, removed or reordered: it binds what
// moved in the next frame, keeps the cursor in view while the list has the focus, and
// forgets the node ids of rows that are gone.
func (l *List) RowsChanged() {
	for k, id := range l.ids {
		if _, ok := l.Model.Index(k); !ok {
			delete(l.ids, k)
			delete(l.idKeys, id)
			delete(l.virtualOwner(), id)
		}
	}
	for c, id := range l.cells {
		if _, ok := l.Model.Index(c.key); !ok {
			delete(l.cells, c)
			delete(l.cellsOf, id)
			delete(l.virtualOwner(), id)
		}
	}
	l.RelayoutContent()
	if l.Focused() && l.Selection != nil {
		if k, ok := l.Selection.Cursor(); ok {
			if l.win != nil {
				l.win.AfterLayout(func() { l.ensureKey(k) })
			}
		}
	}
	if n := l.Model.Len(); n != l.semLen && l.win != nil {
		l.semLen = n
		l.win.semNode(&l.Element) // the grid's row count
	}
}

// RowChanged tells the list that the row with key k shows other data: a realised row is
// bound again and repainted, alone.
func (l *List) RowChanged(k ListKey) {
	s := l.byKey[k]
	if s == nil || s.box.flags&isParked != 0 {
		return
	}
	l.bind(s)
}

// SelectionChanged tells the list that the selection or the cursor changed: the rows whose
// look changed are repainted.
func (l *List) SelectionChanged() {
	l.refreshStates()
	if l.Focused() && l.win != nil {
		l.win.semFocus()
	}
}

// RowState is how the row with key k looks now.
func (l *List) RowState(k ListKey) RowState {
	var st RowState
	if l.Selection != nil {
		st.Selected = l.Selection.Selected(k)
		if c, ok := l.Selection.Cursor(); ok && c == k {
			st.Cursor = true
		}
	}
	if s := l.byKey[k]; s != nil {
		st.Hovered = s.box.Hovered()
	}
	st.Focused = l.Focused()
	st.FocusVisible = st.Focused && l.win != nil && l.win.RingShown()
	return st
}

func (l *List) refreshStates() {
	for _, s := range l.rows {
		l.refreshSlot(s)
	}
}

func (l *List) refreshSlot(s *listSlot) {
	if s.box.flags&isParked != 0 {
		return
	}
	st := l.RowState(s.key)
	if st == s.state {
		return
	}
	sel := st.Selected != s.state.Selected
	s.state = st
	l.invalidateSlot(s)
	if sel && l.win != nil {
		l.win.semNode(s.row.Base())
	}
}

// ---- scrolling ----------------------------------------------------------------------------

// ScrollTo scrolls to offset (clamped to the content).
func (l *List) ScrollTo(offset DIP) {
	if !finite(offset) {
		return
	}
	if l.view.H > 0 {
		offset = clampDIP(offset, 0, l.maxScroll())
	} else {
		offset = max(offset, 0)
	}
	if offset == l.scrollY {
		return
	}
	l.scrollY = offset
	l.stats.ScrollOps++
	l.RelayoutContent()
	l.Invalidate()
}

// ScrollBy scrolls by d.
func (l *List) ScrollBy(d DIP) { l.ScrollTo(l.scrollY + d) }

// EnsureVisible scrolls the least that shows the row at index whole (DownloadTable's
// ensureVisible).
func (l *List) EnsureVisible(index int) {
	if index < 0 || l.rowH <= 0 {
		return
	}
	top := DIP(index) * l.rowH
	switch {
	case top < l.scrollY:
		l.ScrollTo(top)
	case top+l.rowH > l.scrollY+l.bodyHeight():
		l.ScrollTo(top + l.rowH - l.bodyHeight())
	}
}

func (l *List) ensureKey(k ListKey) {
	if i, ok := l.Model.Index(k); ok {
		l.EnsureVisible(i)
	}
}

// ScrollRange is ScrollTarget's: the offset, the rows' (and header's) height and the view's.
func (l *List) ScrollRange() (offset, content, view DIP) {
	return l.scrollY, l.contentHeight(), l.view.H
}

// PageStep is ScrollTarget's: how far a page scrolls, 87.5% of the scroller's height,
// header included, as Chromium pages a scroll container.
func (l *List) PageStep() DIP { return max(l.bar.Metrics.PageFor(l.view.H), 1) } // [mac scroll] PageFor

// ScrollToShow is ScrollShower's: it scrolls the row that holds e into view.
func (l *List) ScrollToShow(e *Element) {
	for _, s := range l.rows {
		if s.box.contains(e) {
			l.EnsureVisible(s.index)
			return
		}
	}
}

// pageRows is how many rows Page Up and Page Down move the cursor (DownloadTable's page).
func (l *List) pageRows() int {
	return max(1, int(math.Floor(float64(l.bodyHeight()/l.rowH)))-1)
}

// ---- input --------------------------------------------------------------------------------

// HoverChanged is HoverHandler's: an overlay scroll bar shows while the pointer is over
// the list.
func (l *List) HoverChanged(bool) {
	if l.Overlay && l.overflow {
		l.RelayoutContent()
		l.Invalidate()
	}
}

// slotAt is the realised row at a point in the list's coordinates.
func (l *List) slotAt(p Point) *listSlot {
	if p.Y < l.headH || p.Y >= l.view.H || l.rowH <= 0 {
		return nil
	}
	i := int(math.Floor(float64((p.Y - l.headH + l.scrollY) / l.rowH)))
	for _, s := range l.rows {
		if s.index == i {
			return s
		}
	}
	return nil
}

// HandlePointer is PointerHandler's: clicks select (on release, as the DOM's click does),
// a double click opens, a secondary click opens the menu.
func (l *List) HandlePointer(in *PointerInput) bool {
	s := l.slotAt(in.Local)
	switch in.Action {
	case PointerDown:
		l.pressOn = s != nil
		if s != nil {
			l.press, l.clicks = s.key, in.Clicks
		}
		// [mac webkit] WebKit fires contextmenu on the press (a right press, or macOS's
		// Control+click), so the menu shows while the button is held and the release does
		// nothing more; Chromium's (Windows') stays below: the menu on release.
		if s != nil && l.win != nil && l.win.Environment().WebKit && webKitContextPress(in) {
			l.pressOn = false
			l.contextClick(s.key, in.Pos)
		}
		// [/mac webkit]
		return s != nil
	case PointerUp:
		if s == nil || !l.pressOn || s.key != l.press {
			l.pressOn = false
			return false
		}
		l.pressOn = false
		k := s.key
		switch in.Button {
		case ButtonLeft:
			if l.Selection != nil {
				l.Selection.Click(k, SelectMods{Shift: in.Mods&ModShift != 0, Toggle: in.Mods&(ModCtrl|ModMeta) != 0})
				l.SelectionChanged()
			}
			if l.clicks >= 2 && l.OnOpen != nil {
				l.OnOpen(k)
			}
		case ButtonRight:
			l.contextClick(k, in.Pos)
		}
		return true
	case PointerCancel:
		l.pressOn = false
	}
	return false
}

// contextClick is a secondary click on row k at pos: the selection keeps a row it holds,
// else becomes it, and the menu opens.
func (l *List) contextClick(k ListKey, pos Point) {
	if l.Selection != nil {
		l.Selection.ContextClick(k)
		l.SelectionChanged()
	}
	if l.OnMenu != nil {
		l.OnMenu(k, pos)
	}
}

// [mac webkit]
// webKitContextPress: a press WebKit on macOS fires contextmenu for, a right press or a
// left press with Control (and not Command, which toggles).
func webKitContextPress(in *PointerInput) bool {
	switch in.Button {
	case ButtonRight:
		return true
	case ButtonLeft:
		return in.Mods&ModCtrl != 0 && in.Mods&ModMeta == 0
	}
	return false
}

// [/mac webkit]

// HandleWheel is WheelHandler's: the wheel scrolls the list (and not what is around it,
// as overscroll-behavior: contain keeps it); Shift scrolls sideways, which a list does not.
func (l *List) HandleWheel(in *WheelInput) bool {
	// [mac webkit] WebKit scrolls a Shift + vertical wheel vertically (macOS turns a
	// notched one sideways before it arrives).
	if in.Mods&ModShift != 0 && (l.win == nil || !l.win.Environment().WebKit) || !l.overflow {
		return false
	}
	d := in.Delta.Y
	if in.Mode == WheelPages {
		d *= l.PageStep()
	}
	l.ScrollBy(d)
	return true
}

// FocusChanged is FocusHandler's: taking the focus puts the cursor where the user looks.
func (l *List) FocusChanged(focused bool) {
	if focused && l.Selection != nil && l.Model.Len() > 0 {
		if k, ok := l.Selection.Cursor(); ok {
			l.ensureKey(k)
		} else if i := l.FirstVisible(); i >= 0 {
			l.Selection.SetCursor(l.Model.Key(i))
		}
	}
	l.SelectionChanged()
}

// HandleKey is KeyHandler's: the cursor keys (with a Selection) or scrolling.
func (l *List) HandleKey(e KeyEvent) bool {
	if e.Action != KeyDown {
		return false
	}
	shift := e.Mods&ModShift != 0
	toggle := e.Mods&(ModCtrl|ModMeta) != 0
	if l.Selection == nil {
		return l.scrollKey(e, shift)
	}
	var move ListMove
	switch e.Key {
	case KeyArrowDown:
		move = 1
	case KeyArrowUp:
		move = -1
	case KeyPageDown:
		move = ListMove(l.pageRows())
	case KeyPageUp:
		move = -ListMove(l.pageRows())
	case KeyHome:
		move = MoveFirst
	case KeyEnd:
		move = MoveLast
	case KeySpace:
		if !toggle {
			return false
		}
		l.Selection.ToggleCursor()
		l.SelectionChanged()
		return true
	case KeyEnter:
		if k, ok := l.Selection.Cursor(); ok && l.OnOpen != nil {
			l.OnOpen(k)
			return true
		}
		return false
	case KeyContextMenu, KeyF10:
		if e.Key == KeyF10 && !shift {
			return false
		}
		k, ok := l.Selection.Cursor()
		if !ok || l.OnMenu == nil {
			return ok
		}
		if !l.Selection.Selected(k) {
			l.Selection.Click(k, SelectMods{})
			l.SelectionChanged()
		}
		l.ensureKey(k)
		l.OnMenu(k, l.menuPoint(k))
		return true
	default:
		return false
	}
	if l.Model.Len() == 0 {
		return true
	}
	l.Selection.Move(move, SelectMods{Shift: shift, Toggle: toggle})
	if k, ok := l.Selection.Cursor(); ok {
		l.ensureKey(k)
	}
	l.SelectionChanged()
	return true
}

// menuPoint is where the keyboard opens a row's menu: 32 DIPs in from its start edge and
// 4 up from its bottom, as DownloadTable does.
func (l *List) menuPoint(k ListKey) Point {
	r := l.WindowBounds()
	if i, ok := l.Model.Index(k); ok {
		top := r.Min.Y + l.headH + DIP(i)*l.rowH - l.scrollY
		r = R(r.Min.X, top, r.Min.X+l.rowW, top+l.rowH)
	}
	if l.win != nil && l.win.rtl() {
		return Pt(r.Max.X-32, r.Max.Y-4)
	}
	return Pt(r.Min.X+32, r.Max.Y-4)
}

// scrollKey scrolls a list without a selection: arrows by a line, Page Up and Page Down
// (and Space) by a page, Home and End to the ends.
func (l *List) scrollKey(e KeyEvent, shift bool) bool {
	line := l.bar.Metrics.Line
	switch e.Key {
	case KeyArrowDown:
		l.ScrollBy(line)
	case KeyArrowUp:
		l.ScrollBy(-line)
	case KeyPageDown:
		l.ScrollBy(l.PageStep())
	case KeyPageUp:
		l.ScrollBy(-l.PageStep())
	case KeySpace:
		if shift {
			l.ScrollBy(-l.PageStep())
		} else {
			l.ScrollBy(l.PageStep())
		}
	case KeyHome:
		l.ScrollTo(0)
	case KeyEnd:
		l.ScrollTo(l.maxScroll())
	default:
		return false
	}
	return true
}

// ---- semantics ------------------------------------------------------------------------------

func (l *List) virtualOwner() map[NodeID]virtualNodes {
	if l.win == nil {
		return map[NodeID]virtualNodes{}
	}
	return l.win.virtual
}

// rowID is the id a realised row's widget stands for: the row's lasting id once
// assistive technology reads the tree, else its own.
func (l *List) rowID(k ListKey) NodeID {
	if l.win == nil || !l.win.semUsed {
		return 0
	}
	return l.idFor(k)
}

// bindIDs gives the realised rows their lasting ids (the tree is read from now on).
func (l *List) bindIDs() {
	for _, s := range l.rows {
		l.win.rebindNode(s.row.Base(), l.idFor(s.key))
	}
}

// Attached is Attacher's: the window knows its lists.
func (l *List) Attached() { l.win.lists = append(l.win.lists, l) }

// idFor is the stable node id of the row with key k.
func (l *List) idFor(k ListKey) NodeID {
	if id, ok := l.ids[k]; ok {
		return id
	}
	if l.win == nil {
		return 0
	}
	id := l.win.newID()
	l.ids[k], l.idKeys[id] = id, k
	l.win.virtual[id] = l
	return id
}

func (l *List) cellID(k ListKey, col int) NodeID {
	c := listCell{k, col}
	if id, ok := l.cells[c]; ok {
		return id
	}
	if l.win == nil {
		return 0
	}
	id := l.win.newID()
	l.cells[c], l.cellsOf[id] = id, c
	l.win.virtual[id] = l
	return id
}

// headerRows is 1 with a header, which takes the grid's first row.
func (l *List) headerRows() int {
	if l.header != nil && !l.header.hidden() {
		return 1
	}
	return 0
}

// FillSemantics is SemanticFiller's: the grid's size counts every row.
func (l *List) FillSemantics(n *SemanticNode) {
	if n.Role == RoleGrid || n.Role == RoleTable || n.Role == RoleListBox || n.Role == RoleList {
		if n.Grid.Rows == 0 && (n.Role == RoleGrid || n.Role == RoleTable) {
			n.Grid.Rows = l.Model.Len() + l.headerRows()
		}
		if n.Grid.Cols == 0 && l.RowSemantics != nil {
			n.Grid.Cols = l.RowSemantics.Cells()
		}
	}
}

// ActiveDescendant is the cursor's row while the list has the focus.
func (l *List) ActiveDescendant() NodeID {
	if l.Selection == nil {
		return 0
	}
	k, ok := l.Selection.Cursor()
	if !ok {
		return 0
	}
	if _, ok := l.Model.Index(k); !ok {
		return 0
	}
	if s := l.byKey[k]; s != nil {
		return s.row.Base().NodeID()
	}
	if l.win == nil || !l.win.semUsed {
		return 0 // nobody reads the tree: the list itself stands for its cursor
	}
	return l.idFor(k)
}

// decorate adds what the list knows to a realised row's node.
func (l *List) decorate(e *Element, n *SemanticNode) {
	for _, s := range l.rows {
		if s.row.Base() != e {
			continue
		}
		l.rowNode(s.index, s.key, n)
		return
	}
}

func (l *List) rowNode(index int, k ListKey, n *SemanticNode) {
	n.Cell.Row = index + l.headerRows()
	n.PosInSet, n.SetSize = index+1, l.Model.Len()
	if l.Selection != nil {
		n.States |= NodeSelectable
		n.Actions |= ActionSelect | ActionAddToSelection | ActionRemoveFromSelection
		if l.Selection.Selected(k) {
			n.States |= NodeSelected
		}
	}
	n.Actions |= ActionScrollIntoView
}

func (l *List) virtualNode(id NodeID) (SemanticNode, bool) {
	if l.win == nil {
		return SemanticNode{}, false
	}
	if k, ok := l.idKeys[id]; ok {
		i, ok := l.Model.Index(k)
		if !ok {
			return SemanticNode{}, false
		}
		if s := l.byKey[k]; s != nil && s.box.flags&isParked == 0 {
			return l.win.node(s.row.Base()), true
		}
		var n SemanticNode
		if l.RowSemantics != nil {
			n = l.RowSemantics.RowNode(i, k)
		}
		if n.Role == RoleNone {
			n.Role = RoleRow
		}
		n.ID, n.Parent = id, l.NodeID()
		n.Bounds = l.virtualRect(i)
		n.States |= NodeOffscreen
		l.rowNode(i, k, &n)
		return n, true
	}
	if c, ok := l.cellsOf[id]; ok {
		i, ok := l.Model.Index(c.key)
		if !ok || l.RowSemantics == nil {
			return SemanticNode{}, false
		}
		n := l.RowSemantics.CellNode(i, c.key, c.col)
		if n.Role == RoleNone {
			n.Role = RoleCell
		}
		n.ID, n.Parent = id, l.idFor(c.key)
		n.Cell.Row, n.Cell.Col = i+l.headerRows(), c.col
		n.Bounds = l.virtualRect(i)
		n.States |= NodeOffscreen
		return n, true
	}
	return SemanticNode{}, false
}

// virtualRect is where the row at index is (or would be), in window coordinates.
func (l *List) virtualRect(i int) Rect {
	r := l.WindowBounds()
	top := r.Min.Y + l.headH + DIP(i)*l.rowH - l.scrollY
	x := r.Min.X
	if l.win != nil && l.win.rtl() && l.overflow && !l.Overlay {
		x += r.Width() - l.rowW
	}
	return R(x, top, x+l.rowW, top+l.rowH)
}

func (l *List) virtualChildren(dst []NodeID, id NodeID) []NodeID {
	k, ok := l.idKeys[id]
	if !ok || l.RowSemantics == nil {
		return dst
	}
	for c := range l.RowSemantics.Cells() {
		dst = append(dst, l.cellID(k, c))
	}
	return dst
}

func (l *List) performVirtual(r ActionRequest) error {
	k, ok := l.idKeys[r.Node]
	if !ok {
		if c, ok := l.cellsOf[r.Node]; ok {
			k = c.key
		} else {
			return fmt.Errorf("perform on node %d: %w", r.Node, ErrNoNode)
		}
	}
	i, ok := l.Model.Index(k)
	if !ok {
		return fmt.Errorf("perform on node %d: %w", r.Node, ErrNoNode)
	}
	l.win.enter()
	defer l.win.leave()
	return l.performRow(i, k, r)
}

func (l *List) performRow(i int, k ListKey, r ActionRequest) error {
	switch r.Action {
	case ActionScrollIntoView:
		l.EnsureVisible(i)
		return nil
	case ActionSelect, ActionAddToSelection, ActionRemoveFromSelection:
		if l.Selection == nil {
			break
		}
		sel := l.Selection.Selected(k)
		switch {
		case r.Action == ActionSelect:
			l.Selection.Click(k, SelectMods{})
		case r.Action == ActionAddToSelection && !sel, r.Action == ActionRemoveFromSelection && sel:
			l.Selection.Click(k, SelectMods{Toggle: true})
		}
		l.SelectionChanged()
		return nil
	}
	return fmt.Errorf("action %#x on row %d: %w", uint32(r.Action), i, ErrUnsupported)
}

// performOn does a row action asked of a realised row's node.
func (l *List) performOn(e *Element, r ActionRequest) (bool, error) {
	for _, s := range l.rows {
		if s.row.Base() == e {
			switch r.Action {
			case ActionScrollIntoView, ActionSelect, ActionAddToSelection, ActionRemoveFromSelection:
				return true, l.performRow(s.index, s.key, r)
			}
			return false, nil
		}
	}
	return false, nil
}

func (l *List) cellNode(row, col int) NodeID {
	hr := l.headerRows()
	if row < hr {
		if l.header == nil {
			return 0
		}
		return l.win.tree.Cell(l.header.NodeID(), row, col)
	}
	i := row - hr
	if i < 0 || i >= l.Model.Len() || col < 0 {
		return 0
	}
	if l.RowSemantics != nil && col >= l.RowSemantics.Cells() {
		return 0
	}
	k := l.Model.Key(i)
	if s := l.byKey[k]; s != nil && s.box.flags&isParked == 0 {
		var found NodeID
		var walk func(*Element)
		walk = func(p *Element) {
			for _, c := range p.kids {
				if found != 0 || !l.win.reachable(c) {
					continue
				}
				switch c.Semantics.Role {
				case RoleCell, RoleRowHeader, RoleColumnHeader:
					if c.Semantics.Cell.Col == col {
						found = c.NodeID()
						return
					}
				}
				walk(c)
			}
		}
		walk(s.row.Base())
		if found != 0 {
			return found
		}
	}
	return l.cellID(k, col)
}

func (l *List) appendSelection(dst []NodeID) []NodeID {
	if l.Selection == nil {
		return dst
	}
	for _, k := range l.Selection.AppendSelected(nil) {
		if _, ok := l.Model.Index(k); !ok {
			continue
		}
		if s := l.byKey[k]; s != nil && s.box.flags&isParked == 0 {
			dst = append(dst, s.row.Base().NodeID())
		} else {
			dst = append(dst, l.idFor(k))
		}
	}
	return dst
}

// Detached forgets the rows' node ids, which die with the window.
func (l *List) Detached() {
	if w := l.win; w != nil {
		for i, x := range w.lists {
			if x == l {
				w.lists = append(w.lists[:i], w.lists[i+1:]...)
				break
			}
		}
	}
	for id := range l.idKeys {
		delete(l.virtualOwner(), id)
	}
	for id := range l.cellsOf {
		delete(l.virtualOwner(), id)
	}
	clear(l.ids)
	clear(l.idKeys)
	clear(l.cells)
	clear(l.cellsOf)
}
