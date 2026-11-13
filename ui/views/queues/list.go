package queues

import (
	"strconv"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/queues"
	"downex/ui/core/text"
	"downex/ui/kit"
	"downex/ui/kit/icons"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/views"
)

// The list column (QueueDialog.svelte's nav.list, "like the sidebar"): the caps heading, the
// queues (the chosen one pressed; each with its note and what waits in it; a "New queue" row
// while a new one is edited) and the New queue button.

// queueRow is .queue: a 32 px row button (9 px gap, 10/8 px padding, 7 px corners) in --ink-2
// 13/500, hovered --hover (and --ink), chosen --sel with a 1 px --sel-line inset and --ink 600.
type queueRow struct {
	kit.Pressable
	id      int64 // 0: the new queue's row
	active  bool
	running bool
	icon    *widgets.Icon
	name    *views.Text
	note    *views.Text
	count   *chText
	hover   fade
}

func newQueueRow(id int64, onPress func()) *queueRow {
	r := &queueRow{id: id}
	r.OnPress = onPress
	r.Focus = kit.FocusTab
	r.Cursor = kit.CursorPointer
	r.Semantics.Role = kit.RoleButton
	r.Ring = kit.Ring{Offset: kit.Px(-2)}
	r.Style = kit.Style{
		Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(9), Width: kit.Pct(100),
		Height: kit.Px(32), Padding: kit.Edges{Start: kit.Px(10), End: kit.Px(8)}, Radius: kit.CornersAll(kit.Px(7)),
	}
	ic := icons.Queue
	if id == 0 {
		ic = icons.Add
	}
	r.icon = widgets.NewIcon(ic, 16, r.iconColor)
	r.name = views.NewLine("", fontQueue, r.ink)
	r.name.Style.Flex = kit.FlexAuto
	r.name.Semantics.Role = kit.RoleNone
	r.note = views.NewText("", fontNote, r.noteInk, kit.TextOptions{})
	r.note.Style.Flex = kit.FlexNone
	r.note.Semantics.Role = kit.RoleNone
	r.count = newChText("", fontSmall, r.countInk, 1.5)
	r.count.Semantics.Role = kit.RoleNone
	r.Add(r.icon, r.name, r.note, r.count)
	if id == 0 {
		r.note.SetHidden(true)
		r.count.SetHidden(true)
	}
	return r
}

func (r *queueRow) iconColor(t *tokens.Theme) kit.Color {
	if r.running || r.active {
		return t.Accent
	}
	return t.Muted
}

func (r *queueRow) ink(t *tokens.Theme) kit.Color {
	if r.active || (r.Hovered() && !r.active) {
		return t.Ink
	}
	return t.Ink2
}

func (r *queueRow) noteInk(t *tokens.Theme) kit.Color {
	if r.active {
		return t.Muted // small print on the selection's tint is --muted (§2.8)
	}
	return t.Faint
}

func (r *queueRow) countInk(t *tokens.Theme) kit.Color {
	if r.active {
		return t.Ink2
	}
	return t.Faint
}

// set shows a queue (nil: the new queue's row) and whether it is the chosen one.
func (r *queueRow) set(q *downexv1.Queue, pending int, active bool) {
	name, note := "New queue", ""
	if q != nil {
		name, note = text.EscapeBidi(q.GetName()), queues.QueueNote(q)
	}
	running := q.GetRunning()
	r.name.SetText(name)
	r.note.SetText(note)
	r.note.SetHidden(note == "")
	count := strconv.Itoa(pending)
	if q != nil {
		r.count.SetText(count)
	}
	if active != r.active {
		r.active = active
		nameW, countW := float32(500), float32(400)
		if active {
			nameW, countW = 600, 600
		}
		r.name.SetFont(withWeight(fontQueue, nameW))
		r.count.SetFont(withWeight(fontSmall, countW))
		r.hover.jump(0)
		r.Invalidate()
		r.SemanticsChanged()
	}
	if running != r.running {
		r.running = running
		r.Invalidate()
	}
	// The button's name is its text: the name, the note, "stopped" (a visually hidden word)
	// and the count.
	accessible := name
	if note != "" {
		accessible += " " + note
	}
	if q != nil && !running {
		accessible += " stopped"
	}
	if q != nil {
		accessible += " " + count
	}
	if accessible != r.Semantics.Name {
		r.Semantics.Name = accessible
		r.SemanticsChanged()
	}
}

// HoverChanged is kit.HoverHandler's: the hover ground fades in and out (var(--dur-2) ease).
func (r *queueRow) HoverChanged(h bool) {
	r.Pressable.HoverChanged(h)
	target := float32(0)
	if h && !r.active {
		target = 1
	}
	r.hover.to(&r.Element, target, tokens.Dur2)
}

// FillSemantics is kit.SemanticFiller's: aria-pressed.
func (r *queueRow) FillSemantics(n *kit.SemanticNode) {
	r.Pressable.FillSemantics(n)
	n.Toggle = kit.ToggleOff
	if r.active {
		n.Toggle = kit.ToggleOn
	}
}

// Paint is kit.Painter's.
func (r *queueRow) Paint(p *kit.PaintContext) {
	t := views.TokensOf(&r.Element)
	box := views.SnapBox(p, 7)
	if r.active {
		p.Canvas.FillRoundedRect(box, t.Sel)
		w := p.Scale.SnapWidth(1)
		p.Canvas.StrokeRoundedRect(box.Inset(w/2), kit.Stroke{Width: w}, t.SelLine)
		return
	}
	if r.hover.v > 0 {
		h := t.Hover
		h.A *= r.hover.v
		p.Canvas.FillRoundedRect(box, h)
	}
}

// queueList is nav.list: a flex column (6 px gap, 10/8 px padding) on --surface-2 with a 1 px
// --line at its end; the queues scroll when they do not fit (ul: flex 0 1 auto, overflow-y:
// auto). A narrow dialog (40rem or less) makes it a row above the editor.
type queueList struct {
	kit.Element
	head   *views.Text
	pane   *widgets.ScrollPane
	ul     *kit.Element
	newBtn *widgets.Button
	rows   []*queueRow
	draft  *queueRow // the new queue's row
	narrow bool
}

func newQueueList(onNew func()) *queueList {
	l := &queueList{}
	l.Semantics.Role = kit.RoleNavigation
	l.Semantics.Name = "Queues"
	l.Style = kit.Style{
		Display: kit.DisplayFlex, Axis: kit.AxisColumn, RowGap: tokens.Sp3, MinHeight: kit.Px(0),
		Padding: kit.EdgesOf(kit.Px(10), kit.Px(8)), Border: kit.Edges{End: kit.Px(1)},
	}
	l.head = views.NewCaps("Queues")
	l.head.Semantics.Hidden = true // aria-hidden: the nav's name says it
	l.head.Style.Padding = kit.Edges{Top: kit.Px(4), Start: kit.Px(10), End: kit.Px(10), Bottom: kit.Px(2)}
	l.head.Style.Flex = kit.FlexNone
	l.ul = box(kit.Style{Display: kit.DisplayBlock, RowGap: kit.Px(2)})
	l.ul.Semantics.Role = kit.RoleList
	l.pane = widgets.NewScrollPane(l.ul)
	l.pane.Style.Flex = kit.FlexOf(0, 1, kit.Length{})
	l.newBtn = widgets.NewButton("New queue", "add", widgets.Secondary, widgets.SizeMd, onNew)
	l.newBtn.Style.AlignSelf = kit.AlignStart
	l.newBtn.Style.Margin = kit.Edges{Top: kit.Px(4), Start: kit.Px(2)}
	l.Add(l.head, l.pane, l.newBtn)
	return l
}

// setNarrow lays the list out as a row (QueueDialog's @container dialog (max-width: 40rem)):
// no heading, the queues in a row, the button beside them.
func (l *queueList) setNarrow(n bool) {
	if n == l.narrow {
		return
	}
	l.narrow = n
	if n {
		l.Style.Axis, l.Style.AlignItems = kit.AxisRow, kit.AlignCenter
		l.Style.Padding = kit.EdgesOf(kit.Px(6), kit.Px(8))
		l.Style.Border = kit.Edges{Bottom: kit.Px(1)}
		l.ul.Style = kit.Style{Display: kit.DisplayFlex, ColumnGap: kit.Px(2)}
		l.pane.Style.Flex = kit.FlexAuto
		l.pane.Style.MinWidth = kit.Px(0)
		l.newBtn.Style.AlignSelf, l.newBtn.Style.Margin = kit.AlignCenter, kit.Edges{}
		l.newBtn.Style.Flex = kit.FlexNone
	} else {
		l.Style.Axis, l.Style.AlignItems = kit.AxisColumn, kit.AlignAuto
		l.Style.Padding = kit.EdgesOf(kit.Px(10), kit.Px(8))
		l.Style.Border = kit.Edges{End: kit.Px(1)}
		l.ul.Style = kit.Style{Display: kit.DisplayBlock, RowGap: kit.Px(2)}
		l.pane.Style.Flex = kit.FlexOf(0, 1, kit.Length{})
		l.pane.Style.MinWidth = kit.Length{}
		l.newBtn.Style.AlignSelf = kit.AlignStart
		l.newBtn.Style.Margin = kit.Edges{Top: kit.Px(4), Start: kit.Px(2)}
		l.newBtn.Style.Flex = kit.Flex{}
	}
	l.head.SetHidden(n)
	for _, r := range l.allRows() {
		r.Style.Width = kit.Pct(100)
		if n {
			r.Style.Width = kit.Length{}
			r.name.SetOptions(kit.TextOptions{})
		} else {
			r.name.SetOptions(kit.TextOptions{Truncate: kit.TruncateEnd})
		}
	}
	l.Relayout()
}

func (l *queueList) allRows() []*queueRow {
	if l.draft != nil {
		return append(l.rows[:len(l.rows):len(l.rows)], l.draft)
	}
	return l.rows
}

// Paint is kit.Painter's: --surface-2 and the 1 px --line at the end (or below, narrow).
func (l *queueList) Paint(p *kit.PaintContext) {
	t := views.TokensOf(&l.Element)
	sz := p.Size()
	p.FillRect(kit.R(0, 0, sz.W, sz.H), t.Surface2)
	w := p.Scale.SnapWidth(1)
	switch {
	case l.narrow:
		p.FillRect(kit.R(0, sz.H-w, sz.W, sz.H), t.Line)
	case l.Window() != nil && l.Window().Direction() == kit.DirRTL:
		p.FillRect(kit.R(0, 0, w, sz.H), t.Line)
	default:
		p.FillRect(kit.R(sz.W-w, 0, sz.W, sz.H), t.Line)
	}
}
