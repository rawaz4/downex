package widgets

import (
	"downex/ui/kit"
	"downex/ui/kit/icons"
	"downex/ui/kit/tokens"
)

// toggler is a control a label around it toggles: a check box, a switch, a radio row.
type toggler interface {
	kit.Widget
	toggle()
}

// toggleArea is HTML's <label> around a control: a press anywhere in it (released inside)
// toggles the control and gives it the focus, as a click on a label does.
type toggleArea struct {
	kit.Element
	target  toggler
	pressed bool
}

// HandlePointer is kit.PointerHandler's.
func (a *toggleArea) HandlePointer(in *kit.PointerInput) bool {
	if a.target == nil || a.target.Base().Window() == nil {
		return false
	}
	inside := kit.RectAt(kit.Point{}, a.Size()).Contains(in.Local)
	switch in.Action {
	case kit.PointerDown:
		if in.Button != kit.ButtonLeft || a.target.Base().Disabled {
			return false
		}
		a.pressed = true
		a.Window().Capture(&a.Element)
		return true
	case kit.PointerUp:
		if !a.pressed || in.Button != kit.ButtonLeft {
			return false
		}
		a.pressed = false
		if inside {
			a.Window().Focus(a.target)
			a.target.toggle()
		}
		return true
	case kit.PointerCancel:
		a.pressed = false
	}
	return false
}

// Checkbox is app.css's .check: a native check box (Chromium's, in --accent) and its label,
// the whole row toggling it. The box is 16 px (14 px in the schedule's "Stop at").
type Checkbox struct {
	toggleArea
	Box   *CheckBox
	Label *kit.Label
}

// NewCheckbox makes a labelled check box.
func NewCheckbox(text string, checked bool, onChange func(bool)) *Checkbox {
	c := &Checkbox{}
	c.Box = NewCheckBox(16, checked, onChange)
	c.Box.Semantics.Name = text
	c.Label = kit.NewLabel(text, FontBody, kit.TextOptions{Wrap: kit.TextWrapWords})
	c.Label.Semantics.Role = kit.RoleNone
	c.Label.Style.MinWidth = kit.Px(0)
	c.target = c.Box
	c.Style = kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: tokens.Sp4, Flex: kit.FlexNone}
	c.Cursor = kit.CursorPointer
	c.Add(c.Box, c.Label)
	return c
}

// Checked reports whether it is checked.
func (c *Checkbox) Checked() bool { return c.Box.Checked }

// SetChecked checks or unchecks it without OnChange.
func (c *Checkbox) SetChecked(v bool) { c.Box.SetChecked(v) }

// SetText changes the label (and the box's name).
func (c *Checkbox) SetText(s string) {
	c.Label.SetText(s)
	c.Box.Semantics.Name = s
	c.Box.SemanticsChanged()
}

// SetDisabled makes it unavailable (HTML's disabled).
func (c *Checkbox) SetDisabled(d bool) {
	c.Box.Disabled = d
	c.Cursor = kit.CursorPointer
	if d {
		c.Cursor = kit.CursorDefault
	}
	c.Box.Invalidate()
	c.Box.SemanticsChanged()
}

// CheckBox is the native check box alone, as Chromium draws <input type=checkbox> with
// accent-color: var(--accent) (measured in the 0.9.2 references): a 1 px #767676 border on
// white (#858585 on #3b3b3b in the dark scheme) with 2 px corners; checked, an --accent
// square with a check of white (#3b3b3b in the dark scheme).
type CheckBox struct {
	kit.Element
	Checked  bool
	OnChange func(bool)
	size     kit.DIP
	space    bool
	check    kit.Path // the check, for checkFor's size
	checkFor kit.Size
	mac      webkitCheckPaths // [mac webkit] macOS's box (webkit.go)
}

// NewCheckBox makes a check box of a size (px).
func NewCheckBox(size kit.DIP, checked bool, onChange func(bool)) *CheckBox {
	b := &CheckBox{Checked: checked, OnChange: onChange, size: size}
	b.Style = kit.Style{Width: kit.Px(size), Height: kit.Px(size), Flex: kit.FlexNone}
	b.Focus = kit.FocusTab
	b.Cursor = kit.CursorPointer
	b.Semantics.Role = kit.RoleCheckBox
	return b
}

// SetChecked checks or unchecks the box without OnChange.
func (b *CheckBox) SetChecked(v bool) {
	if v != b.Checked {
		b.Checked = v
		b.Invalidate()
		b.SemanticsChanged()
	}
}

func (b *CheckBox) toggle() {
	if b.Disabled {
		return
	}
	b.Checked = !b.Checked
	b.Invalidate()
	b.SemanticsChanged()
	if b.OnChange != nil {
		b.OnChange(b.Checked)
	}
}

// HandlePointer is kit.PointerHandler's: a press on the box itself (outside a label).
func (b *CheckBox) HandlePointer(in *kit.PointerInput) bool {
	if _, ok := b.Parent().Widget().(*Checkbox); ok {
		return false // the label around it toggles it
	}
	if in.Action == kit.PointerUp && in.Button == kit.ButtonLeft && kit.RectAt(kit.Point{}, b.Size()).Contains(in.Local) {
		b.toggle()
		return true
	}
	return in.Action == kit.PointerDown && in.Button == kit.ButtonLeft
}

// HandleKey is kit.KeyHandler's: Space toggles when it comes up, as a check box does.
func (b *CheckBox) HandleKey(e kit.KeyEvent) bool {
	if e.Key != kit.KeySpace || e.Mods&(kit.ModCtrl|kit.ModAlt|kit.ModMeta) != 0 {
		return false
	}
	switch {
	case e.Action == kit.KeyDown:
		b.space = !b.Disabled
		return true
	case e.Action == kit.KeyUp && b.space:
		b.space = false
		b.toggle()
		return true
	}
	return false
}

// FocusChanged is kit.FocusHandler's.
func (b *CheckBox) FocusChanged(bool) { b.space = false }

// FillSemantics is kit.SemanticFiller's.
func (b *CheckBox) FillSemantics(n *kit.SemanticNode) {
	n.Toggle = kit.ToggleOff
	if b.Checked {
		n.Toggle = kit.ToggleOn
	}
	n.Actions |= kit.ActionToggle
}

// PerformAction is kit.ActionHandler's.
func (b *CheckBox) PerformAction(r kit.ActionRequest) error {
	if r.Action != kit.ActionToggle {
		return kit.ErrUnsupported
	}
	b.toggle()
	return nil
}

// The native check box's colours (Chromium's light and dark form controls).
var (
	checkBorderLight = kit.RGB(0x767676)
	checkBorderDark  = kit.RGB(0x858585)
	checkGroundDark  = kit.RGB(0x3b3b3b)
	checkMarkDark    = kit.RGB(0x3b3b3b)
	white            = kit.RGB(0xffffff)
)

// Paint is kit.Painter's.
func (b *CheckBox) Paint(p *kit.PaintContext) {
	t := tokensOf(&b.Element)
	r := snapBox(p, 2)
	s := p.Scale.SnapWidth(1)
	accent := t.Accent
	if t.IsForced() {
		accent = t.Focus // Highlight
	}
	if webkitLook(p, t.IsForced()) { // [mac webkit]
		b.paintWebKit(p, accent, t.Dark)
		return
	}
	if b.Checked {
		c := accent
		if b.Disabled {
			c = c.WithAlpha(0.5)
		}
		p.Canvas.FillRoundedRect(r, c)
		mark := white
		if t.Dark && !t.IsForced() {
			mark = checkMarkDark
		}
		if t.IsForced() {
			mark = t.AccentOn // HighlightText
		}
		if pth := b.checkPath(r.Rect.Size()); pth != nil {
			o := r.Rect.Min
			p.Canvas.PushTransform(kit.Translation(o.X, o.Y))
			p.Canvas.StrokePath(pth, kit.Stroke{Width: r.Rect.Height() * 0.16}, mark)
			p.Canvas.Pop()
		}
		return
	}
	ground, border := white, checkBorderLight
	if t.Dark && !t.IsForced() {
		ground, border = checkGroundDark, checkBorderDark
	}
	if t.IsForced() {
		ground, border = t.Surface, t.Ink
	}
	if b.Disabled {
		ground, border = ground.WithAlpha(0.5), border.WithAlpha(0.5)
	}
	p.Canvas.FillRoundedRect(r, ground)
	strokeInside(p.Canvas, r, s, border)
}

// checkPath is the check for a box of size sz (box coordinates): from 20%, 50% down to 40%,
// 70% and up to 80%, 20%, as the references show it. It is made once per size and kept.
func (b *CheckBox) checkPath(sz kit.Size) kit.Path {
	if b.check != nil && b.checkFor == sz {
		return b.check
	}
	w := b.Window()
	if w == nil || w.Host() == nil {
		return nil
	}
	if b.check != nil {
		b.check.Release()
		b.check = nil
	}
	d := kit.PathData{Ops: []kit.PathOp{
		{Verb: kit.PathMove, P: [3]kit.Point{kit.Pt(sz.W*0.2, sz.H*0.5)}},
		{Verb: kit.PathLine, P: [3]kit.Point{kit.Pt(sz.W*0.4, sz.H*0.7)}},
		{Verb: kit.PathLine, P: [3]kit.Point{kit.Pt(sz.W*0.8, sz.H*0.2)}},
	}}
	pth, err := w.Host().Resources().NewPath(d)
	if err != nil {
		return nil
	}
	b.check, b.checkFor = pth, sz
	return pth
}

// Detached is kit.Detacher's: the check's path goes.
func (b *CheckBox) Detached() {
	if b.check != nil {
		b.check.Release()
		b.check = nil
	}
	b.mac.release() // [mac webkit]
}

// Switch is app.css's .switch: a 30 × 18 pill track (--line-strong with a 1 px --line-field
// edge; --accent when on) whose 14 px white knob slides 12 px (160 ms, --ease-out).
type Switch struct {
	kit.Element
	On       bool
	OnChange func(bool)
	knob     float32 // 0 off, 1 on: where the knob is
	tween    *kit.Tween
	look     transition
	space    bool
	rtl      bool // the layout is mirrored: the knob slides the other way
	style    SwitchStyle
}

// SwitchStyle is how a switch is drawn.
type SwitchStyle uint8

const (
	// SwitchClassic is app.css's .switch (30 × 18, a 14 px knob).
	SwitchClassic SwitchStyle = iota
	// SwitchFluent is Windows 11's toggle: 40 × 20 with a 1 px edge when off; the knob is 12 px
	// off and 14 px on.
	SwitchFluent
	// SwitchMac is macOS's switch: 38 × 22, an 18 px knob.
	SwitchMac
)

// switchGeom is a style's track size, knob size off and on, and inset.
func (st SwitchStyle) geom() (w, h, off, on, inset kit.DIP) {
	switch st {
	case SwitchFluent:
		return 40, 20, 12, 14, 4
	case SwitchMac:
		return 38, 22, 18, 18, 2
	}
	return 30, 18, 14, 14, 2
}

// SetStyle draws the switch in a style.
func (s *Switch) SetStyle(st SwitchStyle) {
	if st == s.style {
		return
	}
	s.style = st
	w, h, _, _, _ := st.geom()
	s.Style.Width, s.Style.Height = kit.Px(w), kit.Px(h)
	s.Relayout()
	s.Invalidate()
}

// ContentWidths, ContentHeight and LayoutContent make the switch a Layouter with no content,
// which learns the layout's direction.
func (s *Switch) ContentWidths(*kit.LayoutContext) (min, max kit.DIP) { return 0, 0 }
func (s *Switch) ContentHeight(*kit.LayoutContext, kit.DIP) kit.DIP   { return 0 }
func (s *Switch) LayoutContent(ctx *kit.LayoutContext, _ kit.Rect)    { s.rtl = ctx.RTL }

// NewSwitch makes a switch.
func NewSwitch(on bool, onChange func(bool)) *Switch {
	s := &Switch{On: on, OnChange: onChange}
	if on {
		s.knob = 1
	}
	s.Style = kit.Style{Width: kit.Px(30), Height: kit.Px(18), Flex: kit.FlexNone, Radius: kit.CornersAll(tokens.RPill)}
	s.Focus = kit.FocusTab
	s.Cursor = kit.CursorPointer
	s.Semantics.Role = kit.RoleSwitch
	return s
}

// SetOn turns the switch on or off without OnChange (the knob slides).
func (s *Switch) SetOn(on bool) {
	if on == s.On {
		return
	}
	s.On = on
	s.slide()
	s.SemanticsChanged()
}

func (s *Switch) slide() {
	to := float32(0)
	if s.On {
		to = 1
	}
	st := uint8(0)
	if s.On {
		st = 1
	}
	s.look.moveTo(&s.Element, st, tokens.Dur3)
	w := s.Window()
	if s.tween != nil && w != nil {
		w.StopAnimation(s.tween)
	}
	if w == nil {
		s.knob = to
		return
	}
	s.tween = &kit.Tween{From: s.knob, To: to, Duration: tokens.Dur3, Ease: tokens.EaseOut.Easing(), Set: func(v float32) {
		s.knob = v
		s.Invalidate()
	}}
	w.Animate(s.tween)
}

func (s *Switch) toggle() {
	if s.Disabled {
		return
	}
	s.On = !s.On
	s.slide()
	s.SemanticsChanged()
	if s.OnChange != nil {
		s.OnChange(s.On)
	}
}

// Attached is kit.Attacher's.
func (s *Switch) Attached() {
	st := uint8(0)
	if s.On {
		st = 1
	}
	s.look.jump(&s.Element, st)
}

// HandlePointer is kit.PointerHandler's: a press on the switch itself (outside a row).
func (s *Switch) HandlePointer(in *kit.PointerInput) bool {
	if _, ok := s.Parent().Widget().(*SwitchRow); ok {
		return false
	}
	if p := s.Parent(); p != nil {
		if _, ok := p.Widget().(*toggleArea); ok {
			return false
		}
	}
	if in.Action == kit.PointerUp && in.Button == kit.ButtonLeft && kit.RectAt(kit.Point{}, s.Size()).Contains(in.Local) {
		s.toggle()
		return true
	}
	return in.Action == kit.PointerDown && in.Button == kit.ButtonLeft
}

// HandleKey is kit.KeyHandler's: Space toggles when it comes up (a check box's key).
func (s *Switch) HandleKey(e kit.KeyEvent) bool {
	if e.Key != kit.KeySpace || e.Mods&(kit.ModCtrl|kit.ModAlt|kit.ModMeta) != 0 {
		return false
	}
	switch {
	case e.Action == kit.KeyDown:
		s.space = !s.Disabled
		return true
	case e.Action == kit.KeyUp && s.space:
		s.space = false
		s.toggle()
		return true
	}
	return false
}

// FillSemantics is kit.SemanticFiller's.
func (s *Switch) FillSemantics(n *kit.SemanticNode) {
	n.Toggle = kit.ToggleOff
	if s.On {
		n.Toggle = kit.ToggleOn
	}
	n.Actions |= kit.ActionToggle
}

// PerformAction is kit.ActionHandler's.
func (s *Switch) PerformAction(r kit.ActionRequest) error {
	if r.Action != kit.ActionToggle {
		return kit.ErrUnsupported
	}
	s.toggle()
	return nil
}

// knobShadow is the knob's box-shadow: 0 1px 2px rgb(0 0 0 / 0.25).
var knobShadow = kit.Shadow{Offset: kit.Pt(0, 1), Blur: 2, Color: kit.Color{A: 0.25}}

// Paint is kit.Painter's.
func (s *Switch) Paint(p *kit.PaintContext) {
	t := tokensOf(&s.Element)
	_, th, _, _, _ := s.style.geom()
	r := snapBox(p, th/2)
	off, on := t.LineStrong, t.Accent
	if s.style == SwitchFluent {
		off = t.LineStrong.WithAlpha(0) // Windows 11's off track is hollow: its edge only
	}
	if t.IsForced() {
		off, on = t.Surface, t.Focus // Canvas, Highlight
	}
	from, to := off, on
	if s.look.fromState() == 1 {
		from = on
	}
	if s.look.state() == 0 {
		to = off
	}
	track := s.look.mix(from, to)
	if s.Disabled {
		p.Canvas.PushOpacity(0.5)
	}
	p.Canvas.FillRoundedRect(r, track)
	edge := p.Scale.SnapWidth(1)
	switch {
	case t.IsForced():
		strokeInside(p.Canvas, r, edge, t.Ink)
	case s.style == SwitchMac:
		// macOS's track has no edge
	default:
		// box-shadow: inset 0 0 0 1px var(--line-field), gone when checked (it transitions)
		a := float32(1)
		if s.look.state() == 1 {
			a = 1 - s.look.share
			if !s.look.running {
				a = 0
			}
		} else if s.look.fromState() == 1 && s.look.running {
			a = s.look.share
		}
		if a > 0 {
			strokeInside(p.Canvas, r, edge, t.LineField.WithAlpha(t.LineField.A*a))
		}
	}
	w, h, koff, kon, inset := s.style.geom()
	ks := koff + (kon-koff)*kit.DIP(s.knob)
	kin := inset - (ks-koff)/2 // a knob that grows keeps its centre on the track's
	if s.style == SwitchClassic || s.style == SwitchMac {
		kin = inset
	}
	travel := w - 2*kin - ks
	x := p.Origin.X + kin + travel*kit.DIP(s.knob)
	if s.rtl {
		x = p.Origin.X + w - kin - ks - travel*kit.DIP(s.knob)
	}
	y := p.Origin.Y + (h-ks)/2
	k := kit.R(x, y, x+ks, y+ks)
	kr := kit.RRect{Rect: k, Radii: kit.UniformRadii(ks / 2)}
	knob := white
	if s.style == SwitchFluent && !t.IsForced() {
		// Windows 11's off knob is the text colour
		kf, kt := t.Ink2, white
		if s.look.fromState() == 1 {
			kf = white
		}
		if s.look.state() == 0 {
			kt = t.Ink2
		}
		knob = s.look.mix(kf, kt)
	}
	switch {
	case t.IsForced() && s.On:
		knob = t.AccentOn // HighlightText
	case t.IsForced():
		knob = t.Ink // ButtonText
	default:
		p.Canvas.DrawShadow(kr, knobShadow)
	}
	p.Canvas.FillEllipse(k, knob)
	if s.Disabled {
		p.Canvas.Pop()
	}
}

// SwitchRow is Switch.svelte: a settings row with its label (13/500 --ink) and hint (12
// --faint) at the start and the switch at the end; the whole row toggles it. The switch is
// named by the label and described by the hint.
type SwitchRow struct {
	toggleArea
	Switch *Switch
	label  *kit.Label
	hint   *kit.Label
}

// NewSwitchRow makes a switch row; hint may be "".
func NewSwitchRow(text, hint string, on bool, onChange func(bool)) *SwitchRow {
	r := &SwitchRow{}
	r.Switch = NewSwitch(on, onChange)
	r.Switch.Semantics.Name = text
	r.Switch.Semantics.Description = hint
	r.target = r.Switch
	r.label = kit.NewLabel(text, font(tokens.TypeBody, 500), kit.TextOptions{Wrap: kit.TextWrapWords})
	r.label.Semantics.Role = kit.RoleNone
	words := box(kit.Style{Display: kit.DisplayBlock, RowGap: kit.Px(2), MinWidth: kit.Px(0), Flex: kit.FlexOf(0, 1, kit.Length{})}, r.label)
	if hint != "" {
		r.hint = kit.NewLabel(hint, font(tokens.TypeSmall, 400), kit.TextOptions{Wrap: kit.TextWrapWords})
		r.hint.Semantics.Role = kit.RoleNone
		words.Add(r.hint)
	}
	r.Style = kit.Style{
		Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, Justify: kit.JustifySpaceBetween, ColumnGap: tokens.Sp7,
		MinHeight: kit.Px(44), Padding: kit.EdgesBlock(kit.Px(6)),
	}
	r.Cursor = kit.CursorPointer
	r.Add(words, r.Switch)
	return r
}

// SetDisabled makes the row unavailable: the label goes --faint, the switch to 50%.
func (r *SwitchRow) SetDisabled(d bool) {
	r.Switch.Disabled = d
	r.Cursor = kit.CursorPointer
	if d {
		r.Cursor = kit.CursorDefault
	}
	r.Invalidate()
	r.Switch.Invalidate()
	r.Switch.SemanticsChanged()
}

// Paint is kit.Painter's: the words' colours.
func (r *SwitchRow) Paint(p *kit.PaintContext) {
	t := tokensOf(&r.Element)
	c := t.Ink
	if r.Switch.Disabled {
		c = t.Faint
	}
	r.label.SetColor(c)
	if r.hint != nil {
		r.hint.SetColor(t.Faint)
	}
}

// RadioOption is one row of RadioRows.
type RadioOption struct {
	Value string
	Label string
	// Note is what the choice does (12 --muted), under the label or beside it (Inline).
	Note string
	// Icon is Icon.svelte's name for the row's icon.
	Icon string
}

// RadioRows are the choice rows of RestartDialog and MoveToQueueDialog (DESIGN-SYSTEM.md
// §4.10): a --surface box with a --line border and 10 px corners holding label rows around
// visually hidden native radios, each with an icon, the choice and what it does, and a Mint
// check on the chosen one (--sel ground with a --sel-line edge). The arrows move the choice,
// as a radio group's do; Tab reaches the chosen row.
type RadioRows struct {
	kit.Element
	Options  []RadioOption
	Value    string
	OnChange func(string)
	// Inline puts the note beside the label in a 34 px row (Move to queue); otherwise it is
	// under it in a row of at least 36 px (Restart).
	Inline bool
	rows   []*radioRow
}

type radioRow struct {
	toggleArea
	g     *RadioRows
	i     int
	look  transition
	icon  *Icon
	tick  *Icon
	label *kit.Label
	note  *kit.Label
}

// NewRadioRows makes the rows, named name (aria-label).
func NewRadioRows(name string, opts []RadioOption, value string, inline bool, onChange func(string)) *RadioRows {
	g := &RadioRows{Options: opts, Value: value, OnChange: onChange, Inline: inline}
	g.Semantics.Role = kit.RoleRadioGroup
	g.Semantics.Name = name
	gap := kit.Px(2)
	if inline {
		gap = kit.Px(0)
	}
	g.Style = kit.Style{
		Display: kit.DisplayBlock, RowGap: gap, MinWidth: kit.Px(0), Padding: kit.EdgesAll(kit.Px(4)),
		Border: kit.EdgesAll(kit.Px(1)), Radius: kit.CornersAll(tokens.RLg),
	}
	for i := range opts {
		g.rows = append(g.rows, g.newRow(i))
		g.Add(g.rows[i])
	}
	g.syncFocus()
	return g
}

func (g *RadioRows) newRow(i int) *radioRow {
	o := g.Options[i]
	r := &radioRow{g: g, i: i}
	r.target = r
	r.Focus = kit.FocusClick
	r.Cursor = kit.CursorPointer
	r.Ring = kit.Ring{Offset: kit.Px(-2)}
	r.Semantics.Role = kit.RoleRadioButton
	r.Semantics.Name = o.Label
	r.Semantics.Description = o.Note
	r.Semantics.PosInSet, r.Semantics.SetSize = i+1, len(g.Options)
	ic, _ := iconOf(o.Icon)
	r.icon = NewIcon(ic, 16, func(t *tokens.Theme) kit.Color {
		if r.on() {
			return t.Accent
		}
		return t.Muted
	})
	r.tick = NewIcon(icons.Check, 16, func(t *tokens.Theme) kit.Color { return t.Accent })
	r.label = kit.NewLabel(o.Label, font(tokens.TypeBody, 500), kit.TextOptions{Truncate: kit.TruncateEnd})
	r.label.Semantics.Role = kit.RoleNone
	r.label.Style.MinWidth = kit.Px(0)
	r.note = kit.NewLabel(o.Note, font(tokens.TypeSmall, 400), kit.TextOptions{Wrap: kit.TextWrapAnywhere})
	r.note.Semantics.Role = kit.RoleNone
	r.note.Style.MinWidth = kit.Px(0)
	if g.Inline {
		r.Style = kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: tokens.Sp5,
			MinHeight: kit.Px(34), Padding: kit.Edges{Start: kit.Px(10), End: kit.Px(8)}, Radius: kit.CornersAll(kit.Px(7))}
		r.label.Style.Flex = kit.FlexOf(1, 1, kit.Length{})
		r.Add(r.icon, r.label, r.note, r.tick)
	} else {
		r.Style = kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignStart, ColumnGap: tokens.Sp5,
			MinHeight: kit.Px(36), Padding: kit.Edges{Top: kit.Px(8), Bottom: kit.Px(8), Start: kit.Px(10), End: kit.Px(8)},
			Radius: kit.CornersAll(kit.Px(7))}
		r.icon.Style.Margin = kit.Edges{Top: kit.Px(1)}
		r.tick.Style.Margin = kit.Edges{Top: kit.Px(1)}
		text := box(kit.Style{Display: kit.DisplayBlock, RowGap: kit.Px(2), MinWidth: kit.Px(0), Flex: kit.FlexOf(1, 1, kit.Length{})}, r.label, r.note)
		r.Add(r.icon, text, r.tick)
	}
	return r
}

func (r *radioRow) on() bool { return r.g.Value == r.g.Options[r.i].Value }

func (r *radioRow) toggle() { r.g.choose(r.i, false) }

// choose makes option i the value (and focuses it when moved by the keyboard).
func (g *RadioRows) choose(i int, keys bool) {
	if i < 0 || i >= len(g.rows) || g.Disabled {
		return
	}
	v := g.Options[i].Value
	if keys {
		if w := g.Window(); w != nil {
			w.SetFocusVisible(true)
			w.Focus(g.rows[i])
		}
	}
	if v == g.Value {
		return
	}
	g.Value = v
	g.syncFocus()
	if g.OnChange != nil {
		g.OnChange(v)
	}
}

// SetValue changes the choice without OnChange.
func (g *RadioRows) SetValue(v string) {
	if v != g.Value {
		g.Value = v
		g.syncFocus()
	}
}

// syncFocus makes the chosen row the one Tab reaches, and repaints the rows.
func (g *RadioRows) syncFocus() {
	chosen := false
	for _, r := range g.rows {
		on := r.on()
		r.Focus = kit.FocusClick
		if on {
			r.Focus, chosen = kit.FocusTab, true
		}
		r.tick.SetHidden(!on)
		f := font(tokens.TypeBody, 500)
		if on {
			f.Weight = 600
		}
		r.label.SetFont(f)
		st := uint8(0)
		if on {
			st = 2
		}
		r.look.jump(&r.Element, st|r.look.state()&stHover)
		r.Invalidate()
		r.icon.Invalidate()
		r.SemanticsChanged()
	}
	if !chosen && len(g.rows) > 0 {
		g.rows[0].Focus = kit.FocusTab // no choice yet: the group is reached at its first row
	}
}

// HandleKey is kit.KeyHandler's: the arrows move the choice, wrapping (a radio group's
// keys); Space chooses the focused row.
func (r *radioRow) HandleKey(e kit.KeyEvent) bool {
	if e.Action != kit.KeyDown || e.Mods&(kit.ModCtrl|kit.ModAlt|kit.ModMeta) != 0 {
		return false
	}
	n := len(r.g.rows)
	switch e.Key {
	case kit.KeyArrowDown, kit.KeyArrowRight:
		r.g.choose((r.i+1)%n, true)
	case kit.KeyArrowUp, kit.KeyArrowLeft:
		r.g.choose((r.i-1+n)%n, true)
	case kit.KeySpace:
		r.g.choose(r.i, true)
	default:
		return false
	}
	return true
}

// HoverChanged is kit.HoverHandler's: the hover ground of a row that is not chosen.
func (r *radioRow) HoverChanged(h bool) {
	st := r.look.state() &^ stHover
	if h {
		st |= stHover
	}
	r.look.moveTo(&r.Element, st, tokens.Dur2)
}

// FillSemantics is kit.SemanticFiller's.
func (r *radioRow) FillSemantics(n *kit.SemanticNode) {
	if r.on() {
		n.States |= kit.NodeSelected
	}
	n.States |= kit.NodeSelectable
	n.Actions |= kit.ActionSelect
}

// PerformAction is kit.ActionHandler's.
func (r *radioRow) PerformAction(a kit.ActionRequest) error {
	if a.Action != kit.ActionSelect {
		return kit.ErrUnsupported
	}
	r.g.choose(r.i, false)
	return nil
}

func (r *radioRow) colorsOf(t *tokens.Theme, st uint8) (bg kit.Color) {
	switch {
	case st&2 != 0:
		return t.Sel
	case st&stHover != 0:
		return t.Hover
	}
	return kit.Color{}
}

// Paint is kit.Painter's.
func (r *radioRow) Paint(p *kit.PaintContext) {
	t := tokensOf(&r.Element)
	bg := r.look.mix(r.colorsOf(t, r.look.fromState()), r.colorsOf(t, r.look.state()))
	box := snapBox(p, 7)
	if bg.A > 0 {
		p.Canvas.FillRoundedRect(box, bg)
	}
	ink := t.Ink2
	if r.on() {
		ink = t.Ink
		strokeInside(p.Canvas, box, p.Scale.SnapWidth(1), t.SelLine) // inset 0 0 0 1px var(--sel-line)
		if t.IsForced() {
			w := p.Scale.SnapWidth(2)
			p.Canvas.StrokeRoundedRect(box.Inset(w/2+2), kit.Stroke{Width: w}, t.Focus)
		}
	}
	r.label.SetColor(ink)
	r.note.SetColor(t.Muted)
}

// Paint is kit.Painter's: the group's box.
func (g *RadioRows) Paint(p *kit.PaintContext) {
	t := tokensOf(&g.Element)
	b := snapBox(p, 10)
	p.Canvas.FillRoundedRect(b, t.Surface)
	strokeInside(p.Canvas, b, p.Scale.SnapWidth(1), t.Line)
}
