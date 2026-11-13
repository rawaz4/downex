package downloads

import (
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/display"
	"downex/ui/kit"
	"downex/ui/kit/icons"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/state"
)

// rowBar is a row's SegmentBar: one cell of the overall progress (6 px, 4 px in Compact), at
// 55% while the list is stale (.grid.stale .body .segbar).
type rowBar struct {
	*widgets.SegmentBar
	r     *row
	cells [1]widgets.SegCell
}

var probeTone = widgets.BarDownloading // tone="fill": the hatch in the fill colour

func newRowBar(r *row) *rowBar {
	return &rowBar{SegmentBar: widgets.NewSegmentBar(widgets.SegRow), r: r}
}

func (b *rowBar) setSize(s widgets.SegSize) {
	if b.Size == s {
		return
	}
	b.Size = s
	h := kit.DIP(6)
	if s == widgets.SegCompact {
		h = 4
	}
	b.Style.Height = kit.Px(h)
	b.Relayout()
}

func (b *rowBar) set(c widgets.SegCell, st widgets.BarState, probe bool) {
	if b.cells[0] == c && b.Fixed != nil && (b.Tone != nil) == probe {
		if cur := b.Shown(); len(cur) == 1 {
			b.Set(b.cells[:], st) // the state may have changed
			return
		}
	}
	b.cells[0] = c
	if probe {
		b.Tone = &probeTone
	} else {
		b.Tone = nil
	}
	b.Set(b.cells[:], st)
}

// Paint is kit.Painter's: the bar, at 55% opacity while stale.
func (b *rowBar) Paint(p *kit.PaintContext) {
	if b.r.stale && !b.r.ghost {
		p.Canvas.PushOpacity(0.55)
		b.SegmentBar.Paint(p)
		p.Canvas.Pop()
		return
	}
	b.SegmentBar.Paint(p)
}

// review is "Review ›" for a download to confirm: a mouse convenience (Enter opens the same
// dialog), hidden from assistive technology, out of the Tab order.
type review struct {
	kit.Pressable
	r       *row
	label   *kit.Label
	chevron *widgets.Icon
}

func newReview(r *row) *review {
	v := &review{r: r}
	v.OnPress = func() { r.act(ActConfirm) }
	v.Focus = kit.FocusNone
	v.Semantics.Hidden = true
	v.Tooltip = "Review this download"
	v.Cursor = kit.CursorPointer
	v.Style = kit.Style{
		Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(5), Flex: kit.FlexNone,
		Height: kit.Px(24), Padding: kit.Edges{Start: kit.Px(9), End: kit.Px(7)}, Border: kit.EdgesAll(kit.Px(1)),
	}
	v.label = kit.NewLabel("Review", fontReview, kit.TextOptions{})
	v.chevron = widgets.NewIcon(icons.ChevronRight, 12, func(t *tokens.Theme) kit.Color { return t.Info })
	v.Add(v.label, v.chevron)
	return v
}

func (v *review) setCompact(c bool) {
	h := kit.Px(24)
	if c {
		h = kit.Px(20)
	}
	if v.Style.Height != h {
		v.Style.Height = h
		v.Relayout()
	}
}

// Paint is kit.Painter's: --info-soft in a 1 px --info-line border (--info under the pointer),
// 7 px corners, --info text.
func (v *review) Paint(p *kit.PaintContext) {
	t := tok(&v.Element)
	border := t.InfoLine
	if v.Hovered() {
		border = t.Info
	}
	if t.IsForced() {
		border = t.Ink
	}
	// --info-soft is translucent: the ground first, then the 1 px border over its edge
	r := kit.RoundRect(p.Scale.SnapRect(kit.RectAt(p.Origin, p.Size())), 7)
	w := p.Scale.SnapWidth(1)
	p.Canvas.FillRoundedRect(r, t.InfoSoft)
	p.Canvas.StrokeRoundedRect(r.Inset(w/2), kit.Stroke{Width: w}, border)
	v.label.SetColor(t.Info)
}

// inlineAct is Retry or Restart… after "Failed": a link-like mouse convenience.
type inlineAct struct {
	kit.Pressable
	r     *row
	label *kit.Label
	id    RowAction
}

func newInlineAct(r *row) *inlineAct {
	a := &inlineAct{r: r}
	a.OnPress = func() { r.act(a.id) }
	a.Focus = kit.FocusNone
	a.Semantics.Hidden = true
	a.Cursor = kit.CursorPointer
	a.Style = kit.Style{
		Flex: kit.FlexNone, Padding: kit.EdgesOf(kit.Px(1), kit.Px(4)), Margin: kit.Edges{Start: kit.Px(2)},
	}
	a.label = kit.NewLabel("", fontInline, kit.TextOptions{})
	a.Add(a.label)
	return a
}

func (a *inlineAct) set(act display.Action) {
	a.id = RowAction(act.ID)
	a.label.SetText(act.Label)
}

// Paint is kit.Painter's: --accent-text, underlined 2 px below the baseline under the pointer.
func (a *inlineAct) Paint(p *kit.PaintContext) {
	t := tok(&a.Element)
	a.label.SetColor(t.AccentText)
	if !a.Hovered() {
		return
	}
	l := a.label.TextLayout()
	if l == nil {
		return
	}
	m := l.Metrics()
	o := a.label.WindowBounds().Min.Add(a.label.TextOrigin())
	y := p.Scale.Snap(o.Y + m.Baseline + 2)
	w := p.Scale.SnapWidth(1)
	p.Canvas.FillRect(kit.R(o.X, y, o.X+m.Width, y+w), t.AccentText)
}

// acts is the hover actions: a raised pill at the row's end, over Status and Added, shown
// while the pointer is over the row (opacity over --dur-2).
type acts struct {
	kit.Element
	r       *row
	btns    [2]*actButton
	alpha   float32
	anim    *kit.Tween
	compact bool
	pushed  bool
	acting  bool
	hovered bool
}

func newActs(r *row) *acts {
	a := &acts{r: r}
	a.Semantics.Hidden = true
	a.Inert = true
	a.Style = kit.Style{
		Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(2),
		Padding: kit.EdgesAll(kit.Px(2)), Border: kit.EdgesAll(kit.Px(1)), Hidden: true,
	}
	for i := range a.btns {
		a.btns[i] = newActButton(r)
		a.Add(a.btns[i])
	}
	return a
}

// set shows the actions a state offers (none while they cannot work).
func (a *acts) set(s downexv1.DownloadState, acting, compact bool) {
	a.acting = acting
	a.SetHidden(!acting || (a.alpha <= 0 && !a.hovered))
	if !acting {
		return
	}
	var first actSpec
	switch s {
	case state.StateCompleted:
		first = actSpec{ActFolder, "Open folder", icons.Folder}
	case state.StateAwaiting:
		first = actSpec{ActConfirm, "Confirm…", icons.Confirm}
	case state.StatePaused, state.StateFailed, state.StateCanceled:
		first = actSpec{ActResume, "Resume", icons.Resume}
	default:
		first = actSpec{ActPause, "Pause", icons.Pause}
	}
	a.btns[0].set(first, compact)
	a.btns[1].set(actSpec{ActRemove, "Remove…", icons.Remove}, compact)
	if compact != a.compact {
		a.compact = compact
		pad := kit.Px(2)
		if compact {
			pad = kit.Px(0)
		}
		a.Style.Padding = kit.EdgesAll(pad)
		a.Relayout()
	}
}

// hover fades the pill in or out and lets it take the pointer while shown.
func (a *acts) hover(on bool) {
	a.Inert = !on
	a.hovered = on
	if on && a.acting {
		a.SetHidden(false)
	}
	to := float32(0)
	if on {
		to = 1
	}
	w := a.Window()
	if a.anim != nil && w != nil {
		w.StopAnimation(a.anim)
		a.anim = nil
	}
	if w == nil || !w.MotionOK() {
		a.alpha = to
		a.settle()
		return
	}
	a.anim = &kit.Tween{From: a.alpha, To: to, Duration: tokens.Dur2, Ease: kit.EaseOut, Set: func(v float32) {
		a.alpha = v
		a.settle()
	}}
	w.Animate(a.anim)
}

// settle repaints the pill and hides it once it has faded out (nothing of it draws).
func (a *acts) settle() {
	a.Invalidate()
	if a.alpha <= 0 && !a.hovered {
		a.SetHidden(true)
	}
}

// Paint is kit.Painter's: --raised in a 1 px --line border, --r-md corners, --shadow-md.
func (a *acts) Paint(p *kit.PaintContext) {
	a.pushed = false
	if a.alpha <= 0 {
		return
	}
	t := tok(&a.Element)
	if a.alpha < 1 {
		p.Canvas.PushOpacity(a.alpha)
		a.pushed = true
	}
	r := kit.RoundRect(p.Scale.SnapRect(kit.RectAt(p.Origin, p.Size())), 8)
	for i := len(t.ShadowMd) - 1; i >= 0; i-- {
		p.Canvas.DrawShadow(r, t.ShadowMd[i])
	}
	border := t.Line
	if t.IsForced() {
		border = t.Ink
	}
	p.Canvas.FillRoundedRect(r, border)
	w := p.Scale.SnapWidth(1)
	p.Canvas.FillRoundedRect(kit.RoundRect(r.Rect.Inset(kit.UniformInsets(w)), 8-w), t.Raised)
}

// PaintOver is kit.OverPainter's: the opacity layer ends after the buttons.
func (a *acts) PaintOver(p *kit.PaintContext) {
	if a.pushed {
		p.Canvas.Pop()
		a.pushed = false
	}
}

// visible reports whether the children should paint: a transparent pill draws nothing.
func (a *acts) visible() bool { return a.alpha > 0 }

type actSpec struct {
	id    RowAction
	label string
	icon  icons.Name
}

// actButton is one hover action: 26 px (22 in Compact), 6 px corners, a 15 px icon in --muted,
// --hover and --ink (--danger for Remove) under the pointer.
type actButton struct {
	kit.Pressable
	r    *row
	spec actSpec
	icon *widgets.Icon
}

func newActButton(r *row) *actButton {
	b := &actButton{r: r}
	b.OnPress = func() { r.act(b.spec.id) }
	b.Focus = kit.FocusNone
	b.Cursor = kit.CursorPointer
	b.Style = kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, Justify: kit.JustifyCenter,
		Width: kit.Px(26), Height: kit.Px(26), Flex: kit.FlexNone}
	b.icon = widgets.NewIcon(icons.Pause, 15, b.iconColor)
	b.Add(b.icon)
	return b
}

func (b *actButton) set(s actSpec, compact bool) {
	b.spec = s
	b.Tooltip = s.label
	b.icon.SetName(s.icon)
	sz := kit.Px(26)
	if compact {
		sz = kit.Px(22)
	}
	if b.Style.Width != sz {
		b.Style.Width, b.Style.Height = sz, sz
		b.Relayout()
	}
}

func (b *actButton) iconColor(t *tokens.Theme) kit.Color {
	if !b.Hovered() {
		return t.Muted
	}
	if b.spec.id == ActRemove {
		return t.Danger
	}
	return t.Ink
}

// Paint is kit.Painter's.
func (b *actButton) Paint(p *kit.PaintContext) {
	if b.Hovered() {
		t := tok(&b.Element)
		p.Canvas.FillRoundedRect(kit.RoundRect(p.Scale.SnapRect(kit.RectAt(p.Origin, p.Size())), 6), t.Hover)
	}
}

// HoverChanged is kit.HoverHandler's.
func (b *actButton) HoverChanged(h bool) {
	b.Pressable.HoverChanged(h)
	b.icon.Invalidate()
}

// act runs a row action for the row's download.
func (r *row) act(id RowAction) {
	if r.d == nil || r.ghost {
		return
	}
	r.t.dl.rowAction(id, r.d)
}

var _ = time.Second
