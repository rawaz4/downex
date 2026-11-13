package queues

import (
	"slices"
	"strconv"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/categories"
	"downex/ui/core/display"
	"downex/ui/core/queues"
	"downex/ui/kit"
	"downex/ui/kit/icons"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/views"
)

// The Order section: the queue's downloads that still wait, in the order they start; each row
// is dragged to another place, or moved one place with its arrows (MoveDownloads).

const (
	orderHint = "Drag downloads, or use the arrows, to change the order in which they start."
	// noDrop: nowhere is marked (dropBefore undefined); queues.End marks the list's end.
	noDrop int64 = -1
	// dragThreshold is how far the pointer moves with the button down before a row is dragged
	// (Windows' SM_CXDRAG/SM_CYDRAG, which Chromium's drag and drop uses).
	dragThreshold = 4
)

type orderSection struct {
	kit.Element
	d     *Dialog
	hint  *views.Text
	empty *card
	list  *orderList
	more  *views.Text
	rows  map[int64]*orderRow
	ids   []int64
}

func newOrderSection(d *Dialog) *orderSection {
	o := &orderSection{d: d, rows: map[int64]*orderRow{}}
	o.Semantics.Role = kit.RoleRegion
	o.Semantics.Name = "Order"
	o.Style = kit.Style{Display: kit.DisplayBlock, RowGap: tokens.Sp4, MinWidth: kit.Px(0)}
	o.hint = qHint(orderHint)
	o.hint.Style.MinWidth = kit.Px(0)
	head := box(kit.Style{Display: kit.DisplayFlex, Wrap: true, AlignItems: kit.AlignBaseline, Justify: kit.JustifySpaceBetween,
		RowGap: tokens.Sp1, ColumnGap: tokens.Sp7, MinWidth: kit.Px(0)}, title("Order"), o.hint)
	emptyText := views.NewParagraph("No downloads wait in this queue.", fontCell, views.InkMuted)
	o.empty = newCard(kit.Style{Display: kit.DisplayBlock, Padding: kit.EdgesAll(kit.Px(14))}, emptyText)
	o.list = newOrderList(o)
	o.more = qHint("")
	o.Add(head, o.empty, o.list, o.more)
	return o
}

// refresh shows the queue's order: the rows (rebuilt in the new order when it changed), their
// states, and what is locked.
func (o *orderSection) refresh() {
	d := o.d
	q := d.queue()
	o.SetHidden(q == nil)
	if q == nil {
		return
	}
	pending := d.pendingIDs()
	shown := pending
	if len(shown) > MaxRows {
		shown = shown[:MaxRows]
	}
	o.hint.SetHidden(len(shown) == 0)
	o.empty.SetHidden(len(shown) > 0)
	o.list.SetHidden(len(shown) == 0)
	if n := len(pending) - len(shown); n > 0 {
		o.more.SetText("And " + strconv.Itoa(n) + " more, after these.")
		o.more.SetHidden(false)
	} else {
		o.more.SetHidden(true)
	}
	if !slices.Equal(shown, o.ids) {
		o.ids = slices.Clone(shown)
		ws := make([]kit.Widget, 0, len(shown)+1)
		keep := map[int64]bool{}
		for _, id := range shown {
			r := o.rows[id]
			if r == nil {
				r = newOrderRow(o, id)
				o.rows[id] = r
			}
			keep[id] = true
			ws = append(ws, r)
		}
		for id := range o.rows {
			if !keep[id] {
				delete(o.rows, id)
			}
		}
		ws = append(ws, o.list.end)
		o.list.SetChildren(ws...)
	}
	locked := d.locked()
	for i, id := range o.ids {
		o.rows[id].set(d.downloadOf(id), i, len(o.ids), locked)
	}
	o.list.refreshMarks()
}

// focusArrow puts the keyboard on the arrow of the row that moved (its other arrow once it
// reached an end).
func (o *orderSection) focusArrow(id int64, delta int) {
	r := o.rows[id]
	w := o.Window()
	if r == nil || w == nil {
		return
	}
	same, other := r.up, r.down
	if delta > 0 {
		same, other = r.down, r.up
	}
	if !same.Disabled {
		w.Focus(same)
	} else {
		w.Focus(other)
	}
}

// after is where a row dropped just below the row of id goes: before the next download that
// waits in the queue other than the dragged one moved (completed ones in between are not
// listed), or End. id is never moved (see targetOf).
func (o *orderSection) after(id, moved int64) int64 {
	var rest []int64
	for _, x := range o.d.pendingIDs() {
		if x != moved {
			rest = append(rest, x)
		}
	}
	i := slices.Index(rest, id)
	if i < 0 {
		return id
	}
	if i+1 < len(rest) {
		return rest[i+1]
	}
	return queues.End
}

// targetOf is what the download moved, dropped on the row of id, goes before: that row in its
// upper half, the next one in its lower half. A row dropped onto itself stays (its own id).
func (o *orderSection) targetOf(id, moved int64, lowerHalf bool) int64 {
	if id == moved {
		return id
	}
	if lowerHalf {
		return o.after(id, moved)
	}
	return id
}

// orderRow is li.dl: grip, position, tile, name, state and the arrows (8 px gaps, 34 px high, a
// 1 px --line-soft between rows); dragged at 50%; a 2 px --accent line above where a dragged
// row would go.
type orderRow struct {
	kit.Element
	o     *orderSection
	id    int64
	first bool
	pos   *chText
	tile  *tile
	name  *views.Text
	state *kit.Element
	mark  *mark
	text  *views.Text
	up    *widgets.IconButton
	down  *widgets.IconButton
	tone  icons.MarkKind
	label string
	alpha bool // pushed an opacity in Paint
}

func newOrderRow(o *orderSection, id int64) *orderRow {
	r := &orderRow{o: o, id: id, tone: 255}
	r.Semantics.Role = kit.RoleListItem
	r.Cursor = kit.CursorGrab
	r.Style = kit.Style{
		Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: tokens.Sp4, MinHeight: kit.Px(34),
		Padding: kit.Edges{Start: kit.Px(8), End: kit.Px(6)}, Border: kit.Edges{Top: kit.Px(2)}, MinWidth: kit.Px(0),
	}
	r.pos = newChText("", fontSmall, views.InkFaint, 2.5)
	r.pos.Semantics.Role = kit.RoleNone
	r.tile = newTile(&o.d.icons, 16)
	r.name = views.NewLine("", views.FontBody, views.InkInk)
	r.name.Style.Flex = kit.FlexAuto
	r.mark = newMark(&o.d.icons, 12)
	r.text = views.NewText("", fontCell, r.stateInk, kit.TextOptions{})
	r.text.Semantics.Role = kit.RoleNone
	r.state = box(kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: tokens.Sp3, Flex: kit.FlexNone}, r.mark, r.text)
	r.state.Semantics.Role = kit.RoleText
	r.up = widgets.NewIconButton("chevron-up", 14, "", true, func() { o.d.step(id, -1) })
	r.up.Tooltip = "Move up"
	r.down = widgets.NewIconButton("chevron-down", 14, "", true, func() { o.d.step(id, 1) })
	r.down.Tooltip = "Move down"
	moves := box(kit.Style{Display: kit.DisplayFlex, ColumnGap: kit.Px(1), Flex: kit.FlexNone}, r.up, r.down)
	r.Add(newGrip(), r.pos, r.tile, r.name, r.state, moves)
	return r
}

// stateInk is .dstate's colour by its mark.
func (r *orderRow) stateInk(t *tokens.Theme) kit.Color {
	switch r.tone {
	case icons.MarkDownloading:
		return t.AccentText
	case icons.MarkPaused:
		return t.Warn
	case icons.MarkFailed:
		return t.Danger
	case icons.MarkAwaiting:
		return t.Info
	}
	return t.Muted
}

func (r *orderRow) set(d *downexv1.Download, i, n int, locked bool) {
	first := i == 0
	if first != r.first {
		r.first = first
		if first {
			r.Style.Border, r.Style.Padding.Top = kit.Edges{Top: kit.Px(2)}, kit.Length{}
		} else {
			r.Style.Border, r.Style.Padding.Top = kit.Edges{Top: kit.Px(1)}, kit.Px(1)
		}
		r.Relayout()
		r.Invalidate()
	}
	r.pos.SetText(strconv.Itoa(i + 1))
	name := "#" + strconv.FormatInt(r.id, 10)
	if d != nil {
		name = categories.DisplayName(d)
	}
	r.name.SetText(name)
	r.name.Tooltip = ""
	r.tile.SetHidden(d == nil)
	r.state.SetHidden(d == nil)
	if d != nil {
		r.name.Tooltip = name // the whole name (title), also while it is cut
		r.tile.set(tileOf(d))
		k := markOf(d.GetState())
		r.mark.set(k)
		if k != r.tone {
			r.tone = k
			w := float32(400)
			switch k {
			case icons.MarkDownloading, icons.MarkPaused, icons.MarkFailed, icons.MarkAwaiting:
				w = 600
			}
			r.text.SetFont(withWeight(fontCell, w))
			r.text.Invalidate()
		}
		st := display.StatusText(d)
		r.text.SetText(st)
		if r.state.Semantics.Name != st {
			r.state.Semantics.Name = st
			r.state.SemanticsChanged()
		}
	}
	if name != r.label {
		r.label = name
		r.up.Semantics.Name = "Move " + name + " up"
		r.down.Semantics.Name = "Move " + name + " down"
		r.up.SemanticsChanged()
		r.down.SemanticsChanged()
		r.Semantics.Name = name
		r.SemanticsChanged()
	}
	r.up.SetDisabled(locked || i == 0, false)
	r.down.SetDisabled(locked || i == n-1, false)
	r.Cursor = kit.CursorGrab
	if locked {
		r.Cursor = kit.CursorDefault // draggable="false"
	}
}

// HoverChanged is kit.HoverHandler's.
func (r *orderRow) HoverChanged(bool) { r.Invalidate() }

// Paint is kit.Painter's: the hover ground, the line between rows, the drop mark, and 50% while
// dragged.
func (r *orderRow) Paint(p *kit.PaintContext) {
	t := views.TokensOf(&r.Element)
	l := r.o.list
	r.alpha = l.dragging && l.dragID == r.id
	if r.alpha {
		p.Canvas.PushOpacity(0.5)
	}
	sz := p.Size()
	bw := kit.DIP(2)
	if !r.first {
		bw = 1
	}
	if r.Hovered() {
		if r.first {
			// the list clips its rows' hover to its rounded corners
			box := p.Scale.SnapRect(kit.RectAt(p.Origin, sz))
			p.Canvas.FillRoundedRect(kit.RRect{Rect: box, Radii: kit.Radii{TopLeft: 9, TopRight: 9}}.Normalized(), t.Hover)
		} else {
			p.FillRect(kit.R(0, 0, sz.W, sz.H), t.Hover)
		}
	}
	if !r.first {
		p.FillRect(kit.R(0, 0, sz.W, p.Scale.SnapWidth(1)), t.LineSoft)
	}
	if l.dropAt == r.id {
		p.FillRect(kit.R(0, bw, sz.W, bw+2), t.Accent)
	}
}

// PaintOver is kit.OverPainter's: the end of the dragged row's opacity.
func (r *orderRow) PaintOver(p *kit.PaintContext) {
	if r.alpha {
		p.Canvas.Pop()
		r.alpha = false
	}
}

// orderEnd is li.end: 8 px under the last row where a row dropped goes to the end.
type orderEnd struct {
	kit.Element
	l *orderList
}

// Paint is kit.Painter's: the drop mark.
func (e *orderEnd) Paint(p *kit.PaintContext) {
	if e.l.dropAt == queues.End && e.l.dragging {
		p.FillRect(kit.R(0, 0, p.Size().W, 2), views.TokensOf(&e.Element).Accent)
	}
}

// orderList is ol.q-card.downloads: the rows in a card that clips them (no scroller of its
// own: it scrolls inside the editor), named Order and described by the hint. It takes the
// pointer for dragging: a row pressed and moved 4 px is dragged; over a row's upper half it
// goes before that row, over its lower half after it, over the end to the end; where it would
// stay, nothing is marked.
type orderList struct {
	kit.Element
	o   *orderSection
	end *orderEnd

	pressID  int64
	pressAt  kit.Point
	pressed  bool
	dragging bool
	dragID   int64
	dropAt   int64
	marked   int64
}

func newOrderList(o *orderSection) *orderList {
	l := &orderList{o: o, dropAt: noDrop, marked: noDrop}
	l.Semantics.Role = kit.RoleList
	l.Semantics.Name = "Order"
	l.Semantics.Description = orderHint
	l.Style = kit.Style{Display: kit.DisplayBlock, Border: kit.EdgesAll(kit.Px(1)), Overflow: kit.OverflowHidden, MinWidth: kit.Px(0)}
	l.end = &orderEnd{l: l}
	l.end.Style = kit.Style{MinHeight: kit.Px(8)}
	l.end.Semantics.Hidden = true
	l.Add(l.end)
	return l
}

// Paint is kit.Painter's: the card.
func (l *orderList) Paint(p *kit.PaintContext) { paintCard(p, &l.Element) }

// rowAt is the row under a window point, and whether the point is in its lower half; end
// reports the list's end.
func (l *orderList) rowAt(pt kit.Point) (r *orderRow, lower, end bool) {
	for _, c := range l.Children() {
		b := c.WindowBounds()
		if pt.Y < b.Min.Y || pt.Y >= b.Max.Y || pt.X < b.Min.X || pt.X >= b.Max.X {
			continue
		}
		if c == &l.end.Element {
			return nil, false, true
		}
		if row, ok := c.Widget().(*orderRow); ok {
			return row, b.Height() > 0 && pt.Y > b.Min.Y+b.Height()/2, false
		}
	}
	return nil, false, false
}

// over is where the dragged row would go with the pointer at pt (noDrop: nowhere).
func (l *orderList) over(pt kit.Point) int64 {
	row, lower, end := l.rowAt(pt)
	switch {
	case end:
		return queues.End
	case row != nil:
		return l.o.targetOf(row.id, l.dragID, lower)
	}
	return noDrop
}

// HandlePointer is kit.PointerHandler's: the rows' dragging (not their arrows: they take their
// own presses).
func (l *orderList) HandlePointer(in *kit.PointerInput) bool {
	w := l.Window()
	switch in.Action {
	case kit.PointerDown:
		if in.Button != kit.ButtonLeft || w == nil {
			return false
		}
		row, _, _ := l.rowAt(in.Pos)
		if row == nil || l.o.d.locked() {
			return false // draggable="false" while locked
		}
		l.pressed, l.pressID, l.pressAt = true, row.id, in.Pos
		w.Capture(&l.Element)
		return true
	case kit.PointerMove:
		if !l.pressed {
			return false
		}
		if !l.dragging {
			dx, dy := in.Pos.X-l.pressAt.X, in.Pos.Y-l.pressAt.Y
			if max(dx, -dx) < dragThreshold && max(dy, -dy) < dragThreshold {
				return true
			}
			if l.o.d.locked() {
				l.cancel()
				return true
			}
			l.dragging, l.dragID = true, l.pressID
			l.Cursor = kit.CursorGrabbing
			if r := l.o.rows[l.dragID]; r != nil {
				r.Invalidate()
			}
			w.UpdateCursor()
		}
		before := l.over(in.Pos)
		// Nothing is marked where the row would stay in place.
		if before != noDrop && !l.o.d.changesOrder(l.dragID, before) {
			before = noDrop
		}
		l.setDrop(before)
		return true
	case kit.PointerUp:
		if !l.pressed || in.Button != kit.ButtonLeft {
			return false
		}
		dragging, moved := l.dragging, l.dragID
		before := noDrop
		if dragging {
			before = l.over(in.Pos)
		}
		l.cancel()
		if dragging && before != noDrop {
			l.o.d.moveBefore(moved, before, nil)
		}
		return true
	case kit.PointerCancel:
		l.cancel()
	}
	return false
}

// HandleKey is kit.KeyHandler's: Escape drops a drag where it started.
func (l *orderList) HandleKey(e kit.KeyEvent) bool {
	if e.Action == kit.KeyDown && e.Key == kit.KeyEscape && l.dragging {
		l.cancel()
		return true
	}
	return false
}

func (l *orderList) cancel() {
	was := l.dragID
	l.pressed, l.dragging, l.dragID = false, false, 0
	l.Cursor = kit.CursorDefault
	l.setDrop(noDrop)
	if w := l.Window(); w != nil {
		if w.Captured() == &l.Element {
			w.ReleaseCapture()
		}
		w.UpdateCursor()
	}
	if r := l.o.rows[was]; r != nil {
		r.Invalidate()
	}
}

func (l *orderList) setDrop(at int64) {
	if at == l.dropAt {
		return
	}
	l.dropAt = at
	l.refreshMarks()
}

// refreshMarks repaints the row that was marked and the one that is.
func (l *orderList) refreshMarks() {
	if l.marked == l.dropAt {
		return
	}
	for _, id := range []int64{l.marked, l.dropAt} {
		switch {
		case id == queues.End:
			l.end.Invalidate()
		case id > 0:
			if r := l.o.rows[id]; r != nil {
				r.Invalidate()
			}
		}
	}
	l.marked = l.dropAt
}
