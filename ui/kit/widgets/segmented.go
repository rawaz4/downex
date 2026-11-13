package widgets

import (
	"downex/ui/kit"
	"downex/ui/kit/tokens"
)

// SegmentOption is one segment of a SegmentedControl (SegmentedControl.svelte's).
type SegmentOption struct {
	Value string
	Label string
	Icon  string // Icon.svelte's name, "" for none
	// Count is a number after the label; HasCount shows it.
	Count    int
	HasCount bool
	Disabled bool
	Title    string
}

// SegmentedControl is SegmentedControl.svelte (DESIGN-SYSTEM.md §3.4): it picks one value
// of a few. A track on the chrome ground (2 px padding and gap, a --line border, 9 px
// corners, 12.5 px text) whose segments share its width equally, so the raised thumb under
// the chosen one keeps its size as it slides (160 ms, --ease-out); the chosen label is Mint
// (--accent-text, 600) with a Mint icon. It is a group of radio buttons: the arrows move the
// choice, Tab reaches the chosen one. With Fill and SelectWhenNarrow it becomes a Select of
// the same choices where its container has 24rem or less.
type SegmentedControl struct {
	kit.Element
	Options  []SegmentOption
	Value    string
	OnChange func(string)
	Small    bool

	seg    *segTrack
	narrow *Select
}

// SegmentedOptions are a SegmentedControl's choices beyond its options.
type SegmentedOptions struct {
	// Legend names the group (a visually hidden legend), else AriaLabel does.
	Legend, AriaLabel string
	// Small is the 26 px control (20 px segments).
	Small bool
	// Fill makes the control as wide as its container, the segments sharing it.
	Fill bool
	// SelectWhenNarrow, with Fill, shows a Select of the same choices instead where the
	// control has 24rem or less.
	SelectWhenNarrow bool
	Disabled         bool
}

// NewSegmentedControl makes a segmented control.
func NewSegmentedControl(opts []SegmentOption, value string, o SegmentedOptions, onChange func(string)) *SegmentedControl {
	s := &SegmentedControl{Options: opts, Value: value, OnChange: onChange, Small: o.Small}
	name := o.Legend
	if name == "" {
		name = o.AriaLabel
	}
	s.seg = &segTrack{s: s, fill: o.Fill}
	s.seg.Semantics.Role = kit.RoleRadioGroup
	s.seg.Semantics.Fieldset = true // [ax] SegmentedControl.svelte's <fieldset>
	s.seg.Semantics.Name = name
	s.seg.Disabled = o.Disabled
	s.seg.Style = kit.Style{
		Padding: kit.EdgesAll(kit.Px(2)), Border: kit.EdgesAll(kit.Px(1)), Radius: kit.CornersAll(kit.Px(9)),
		AlignSelf: kit.AlignStart, MaxWidth: kit.Pct(100), MinWidth: kit.Px(0),
	}
	if o.Fill {
		s.seg.Style.AlignSelf = kit.AlignAuto
		s.seg.Style.Width = kit.Pct(100)
	}
	for i := range opts {
		s.seg.Add(s.seg.newOpt(i))
	}
	if o.Fill {
		s.Style = kit.Style{Width: kit.Pct(100), MinWidth: kit.Px(0)}
		s.Add(s.seg)
		if o.SelectWhenNarrow {
			sel := make([]SelectOption, len(opts))
			for i, op := range opts {
				sel[i] = SelectOption{Value: op.Value, Label: op.Label, Disabled: op.Disabled, Title: op.Title}
			}
			s.narrow = NewSelect(sel, value, SelectOptions{AriaLabel: name, Size: SelectSm, Disabled: o.Disabled}, func(v string) { s.choose(v, false) })
			s.narrow.Style.MaxWidth = kit.Rem(16)
			s.narrow.Style.Hidden = true
			narrowQ := kit.Query{MaxWidth: kit.Rem(24)}
			s.Style.Container = true
			s.seg.Style.Breakpoints = []kit.Breakpoint{{When: narrowQ, Apply: func(st *kit.Style) { st.Hidden = true }}}
			s.narrow.Style.Breakpoints = []kit.Breakpoint{{When: narrowQ, Apply: func(st *kit.Style) { st.Hidden = false }}}
			s.Add(s.narrow)
		}
	} else {
		s.Style = kit.Style{Display: kit.DisplayBlock, MinWidth: kit.Px(0)}
		s.Add(s.seg)
	}
	s.syncFocus()
	return s
}

// Narrow is the Select shown where the control is narrow, or nil.
func (s *SegmentedControl) Narrow() *Select { return s.narrow }

// Track is the group of radio buttons.
func (s *SegmentedControl) Track() *kit.Element { return &s.seg.Element }

// SetValue changes the choice without OnChange (the thumb slides).
func (s *SegmentedControl) SetValue(v string) {
	if v == s.Value {
		return
	}
	s.Value = v
	s.changed()
}

func (s *SegmentedControl) choose(v string, keys bool) {
	if v == s.Value {
		return
	}
	s.Value = v
	s.changed()
	if keys {
		for _, c := range s.seg.Children() {
			if o, ok := c.Widget().(*segOpt); ok && s.Options[o.i].Value == v {
				if w := s.Window(); w != nil {
					w.SetFocusVisible(true)
					w.Focus(o)
				}
			}
		}
	}
	if s.OnChange != nil {
		s.OnChange(v)
	}
}

func (s *SegmentedControl) changed() {
	s.syncFocus()
	if s.narrow != nil {
		s.narrow.SetValue(s.Value)
	}
	s.seg.RelayoutContent()
	s.seg.Invalidate()
}

// syncFocus makes the chosen segment the one Tab reaches (a radio group's).
func (s *SegmentedControl) syncFocus() {
	any := false
	for _, c := range s.seg.Children() {
		o, ok := c.Widget().(*segOpt)
		if !ok {
			continue
		}
		on := s.Options[o.i].Value == s.Value
		o.Focus = kit.FocusClick
		if on && !s.Options[o.i].Disabled {
			o.Focus, any = kit.FocusTab, true
		}
		if s.seg.Disabled || s.Options[o.i].Disabled {
			o.Focus = kit.FocusNone
		}
		f := font(tokens.TypeCell, 500)
		if on {
			f.Weight = 600
		}
		o.label.SetFont(f)
		o.Invalidate()
		o.SemanticsChanged()
	}
	if !any {
		for _, c := range s.seg.Children() {
			if o, ok := c.Widget().(*segOpt); ok && o.Focus != kit.FocusNone {
				o.Focus = kit.FocusTab
				break
			}
		}
	}
}

// segTrack is the fieldset: equal columns, and the thumb.
type segTrack struct {
	kit.Element
	s     *SegmentedControl
	fill  bool
	rtl   bool
	thumb kit.Rect // where the thumb is drawn now (track coordinates)
	place kit.Rect // where it goes
	tween *kit.Tween
	from  kit.Rect
	laid  bool
}

func (t *segTrack) opts() []*segOpt {
	out := make([]*segOpt, 0, len(t.Children()))
	for _, c := range t.Children() {
		if o, ok := c.Widget().(*segOpt); ok {
			out = append(out, o)
		}
	}
	return out
}

// ContentWidths is kit.Layouter's: equal columns as wide as the widest segment
// (repeat(n, minmax(0, 1fr))).
func (t *segTrack) ContentWidths(ctx *kit.LayoutContext) (min, max kit.DIP) {
	os := t.opts()
	n := kit.DIP(len(os))
	if n == 0 {
		return 0, 0
	}
	var widest kit.DIP
	for _, o := range os {
		_, mx := ctx.IntrinsicWidths(o)
		widest = kit.DIP(maxf(float32(widest), float32(mx)))
	}
	gap := (n - 1) * 2
	return gap, n*widest + gap
}

func maxf(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}

func (t *segTrack) optHeight() kit.DIP {
	if t.s.Small {
		return 20
	}
	return 24
}

// ContentHeight is kit.Layouter's.
func (t *segTrack) ContentHeight(*kit.LayoutContext, kit.DIP) kit.DIP { return t.optHeight() }

// LayoutContent is kit.Layouter's: the segments in equal columns, and the thumb under the
// chosen one (it slides there once it has a place).
func (t *segTrack) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	t.rtl = ctx.RTL
	os := t.opts()
	n := len(os)
	if n == 0 {
		return
	}
	colW := (box.Width() - kit.DIP(n-1)*2) / kit.DIP(n)
	h := t.optHeight()
	var place kit.Rect
	for i, o := range os {
		x := box.Min.X + kit.DIP(i)*(colW+2)
		if ctx.RTL {
			x = box.Max.X - kit.DIP(i+1)*colW - kit.DIP(i)*2
		}
		r := kit.R(x, box.Min.Y, x+colW, box.Min.Y+h)
		ctx.Place(o, r, box.Width())
		if t.s.Options[o.i].Value == t.s.Value {
			place = r
		}
	}
	t.moveThumb(place)
}

func (t *segTrack) moveThumb(r kit.Rect) {
	if r == t.place && t.laid {
		return
	}
	t.place = r
	w := t.Window()
	if !t.laid || w == nil || r.Empty() || t.thumb.Empty() {
		t.laid = true
		t.thumb = r
		t.Invalidate()
		return
	}
	if t.tween != nil {
		w.StopAnimation(t.tween)
	}
	from := t.thumb
	t.tween = &kit.Tween{From: 0, To: 1, Duration: tokens.Dur3, Ease: tokens.EaseOut.Easing(), Set: func(v float32) {
		t.thumb = kit.R(from.Min.X+(r.Min.X-from.Min.X)*v, r.Min.Y, from.Max.X+(r.Max.X-from.Max.X)*v, r.Max.Y)
		t.Invalidate()
	}}
	w.Animate(t.tween)
}

// Paint is kit.Painter's: the track and the thumb (shadow 0 0 0 1px --line-strong and
// --shadow-sm, --raised, 7 px corners).
func (t *segTrack) Paint(p *kit.PaintContext) {
	th := tokensOf(&t.Element)
	b := snapBox(p, 9)
	p.Canvas.FillRoundedRect(b, th.Chrome)
	border := th.Line
	if th.IsForced() {
		border = th.Ink
	}
	strokeInside(p.Canvas, b, p.Scale.SnapWidth(1), border)
	if t.thumb.Empty() {
		return
	}
	r := kit.RRect{Rect: p.Scale.SnapRect(t.thumb.Add(p.Origin)), Radii: kit.UniformRadii(7)}
	drawShadows(p.Canvas, r, th.ShadowSm)
	p.Canvas.DrawShadow(r, kit.Shadow{Spread: 1, Color: th.LineStrong})
	p.Canvas.FillRoundedRect(r, th.Raised)
}

// segOpt is one segment: a label around a visually hidden radio.
type segOpt struct {
	kit.Element
	t     *segTrack
	i     int
	icon  *Icon
	label *kit.Label
	count *segCount
}

func (t *segTrack) newOpt(i int) *segOpt {
	op := t.s.Options[i]
	o := &segOpt{t: t, i: i}
	o.Style = kit.Style{
		Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, Justify: kit.JustifyCenter, ColumnGap: kit.Px(6),
		MinWidth: kit.Px(0), Padding: kit.EdgesInline(kit.Px(12)), Radius: kit.CornersAll(kit.Px(7)),
	}
	if t.s.Small {
		o.Style.Padding = kit.EdgesInline(kit.Px(10))
	}
	o.Ring = kit.Ring{Offset: kit.Px(1)}
	o.Cursor = kit.CursorPointer
	o.Tooltip = op.Title
	o.Semantics.Role = kit.RoleRadioButton
	o.Semantics.PosInSet, o.Semantics.SetSize = i+1, len(t.s.Options)
	if ic, ok := iconOf(op.Icon); ok {
		o.icon = NewIcon(ic, 14, func(th *tokens.Theme) kit.Color {
			if o.on() && !o.off() && !th.IsForced() {
				return th.Accent // .opt.on .icon
			}
			return o.ink(th)
		})
		o.Add(o.icon)
	}
	o.label = label(op.Label, font(tokens.TypeCell, 500))
	o.Add(o.label)
	if op.HasCount {
		o.count = newSegCount(o.on, op.Count)
		o.Add(o.count)
	}
	return o
}

func (o *segOpt) on() bool { return o.t.s.Options[o.i].Value == o.t.s.Value }

func (o *segOpt) off() bool { return o.t.s.Options[o.i].Disabled || o.t.Disabled }

func (o *segOpt) ink(th *tokens.Theme) kit.Color {
	switch {
	case o.t.Disabled && o.on():
		return th.Muted
	case o.off():
		return th.Faint
	case o.on():
		if th.IsForced() {
			return th.Focus // Highlight
		}
		return th.AccentText
	case o.Hovered():
		return th.Ink
	}
	return th.Muted
}

// Paint is kit.Painter's: the label's colour (an icon of the chosen one is --accent).
func (o *segOpt) Paint(p *kit.PaintContext) {
	th := tokensOf(&o.Element)
	o.label.SetColor(o.ink(th))
	if th.IsForced() && o.on() {
		w := p.Scale.SnapWidth(2)
		p.Canvas.StrokeRoundedRect(snapBox(p, 7).Inset(-w/2), kit.Stroke{Width: w}, th.Focus)
	}
}

// HoverChanged is kit.HoverHandler's.
func (o *segOpt) HoverChanged(bool) {
	o.Invalidate()
	if o.icon != nil {
		o.icon.Invalidate()
	}
}

// HandlePointer is kit.PointerHandler's: a click chooses the segment.
func (o *segOpt) HandlePointer(in *kit.PointerInput) bool {
	if o.off() {
		return false
	}
	switch in.Action {
	case kit.PointerDown:
		return in.Button == kit.ButtonLeft
	case kit.PointerUp:
		if in.Button == kit.ButtonLeft && kit.RectAt(kit.Point{}, o.Size()).Contains(in.Local) {
			o.t.s.choose(o.t.s.Options[o.i].Value, false)
			return true
		}
	}
	return false
}

// HandleKey is kit.KeyHandler's: the arrows choose the next or previous enabled segment,
// wrapping, as a radio group's do (mirrored in a right-to-left layout).
func (o *segOpt) HandleKey(e kit.KeyEvent) bool {
	if e.Action != kit.KeyDown || e.Mods&(kit.ModCtrl|kit.ModAlt|kit.ModMeta) != 0 {
		return false
	}
	step := 0
	switch e.Key {
	case kit.KeyArrowDown:
		step = 1
	case kit.KeyArrowUp:
		step = -1
	case kit.KeyArrowRight:
		step = 1
		if o.t.rtl {
			step = -1
		}
	case kit.KeyArrowLeft:
		step = -1
		if o.t.rtl {
			step = 1
		}
	case kit.KeySpace:
		o.t.s.choose(o.t.s.Options[o.i].Value, true)
		return true
	default:
		return false
	}
	opts := o.t.s.Options
	n := len(opts)
	for k := 1; k <= n; k++ {
		j := ((o.i+step*k)%n + n) % n
		if !opts[j].Disabled {
			o.t.s.choose(opts[j].Value, true)
			break
		}
	}
	return true
}

// FillSemantics is kit.SemanticFiller's.
func (o *segOpt) FillSemantics(n *kit.SemanticNode) {
	n.States |= kit.NodeSelectable
	if o.on() {
		n.States |= kit.NodeSelected
	}
	if o.off() {
		n.States |= kit.NodeDisabled
	}
	n.Actions |= kit.ActionSelect
}

// PerformAction is kit.ActionHandler's.
func (o *segOpt) PerformAction(r kit.ActionRequest) error {
	if r.Action != kit.ActionSelect {
		return kit.ErrUnsupported
	}
	if o.off() {
		return kit.ErrNotEnabled
	}
	o.t.s.choose(o.t.s.Options[o.i].Value, false)
	return nil
}

// segCount is a segment's (or a tab's) count: an 18 px wide pill of 11.5 px 600 text, 17 px
// high (--surface-2 and --muted; --accent-soft and --accent-text on the chosen one).
type segCount struct {
	kit.Element
	on    func() bool
	label *kit.Label
}

func newSegCount(on func() bool, n int) *segCount {
	c := &segCount{on: on}
	c.Style = kit.Style{
		Display: kit.DisplayFlex, Justify: kit.JustifyCenter, AlignItems: kit.AlignCenter, Flex: kit.FlexNone,
		MinWidth: kit.Px(18), Height: kit.Px(17), Padding: kit.EdgesInline(kit.Px(5)), Radius: kit.CornersAll(tokens.RPill),
	}
	f := font(tokens.TypeMeta, 600)
	f.LineHeight = 17.0 / (11.5)
	c.label = label(itoa(n), f)
	c.Add(c.label)
	return c
}

// Paint is kit.Painter's.
func (c *segCount) Paint(p *kit.PaintContext) {
	th := tokensOf(&c.Element)
	bg, ink := th.Surface2, th.Muted
	if c.on() {
		bg, ink = th.AccentSoft, th.AccentText
	}
	p.Canvas.FillRoundedRect(snapBox(p, p.Size().H/2), bg)
	c.label.SetColor(ink)
}

// itoa formats a count as a plain integer (the counts the controls show are small).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
