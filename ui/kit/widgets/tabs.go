package widgets

import (
	"strings"

	"downex/ui/kit"
	"downex/ui/kit/tokens"
)

// TabOption is one tab (Tabs.svelte's TabOption).
type TabOption struct {
	Value string
	Label string
	Icon  string // Icon.svelte's name, "" for none
	// Count is a number after the label; HasCount shows it.
	Count    int
	HasCount bool
	// Dot is a Mint dot after the label: something is on in that view (a speed limit). It is
	// not read out.
	Dot      bool
	Disabled bool
	Title    string
}

// TabID is the id of the tab for value of a Tabs with this prefix (Tabs.svelte's tabId): the
// tab's AutomationID, and what its panel is labelled by.
func TabID(prefix, value string) string {
	var b strings.Builder
	b.WriteString(prefix)
	b.WriteString("-tab-")
	for _, r := range value {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// TabsOptions are a Tabs' choices beyond its tabs.
type TabsOptions struct {
	// AriaLabel names the tab list.
	AriaLabel string
	// Panel is the view the tabs control (aria-controls).
	Panel kit.Widget
	// IDPrefix makes the tabs' AutomationIDs (TabID).
	IDPrefix string
	// Small is the 28 px tab row (the progress window's); md is 32 px.
	Small bool
	// Flush starts the first label where the content beside it does: the row reaches into
	// the container's padding by a tab's padding (12 px; 10 px small), and the hairline stays
	// under the content.
	Flush    bool
	Disabled bool
}

// Tabs is Tabs.svelte (DESIGN-SYSTEM.md §3.4.1): it switches between views of one place. A
// row of tabs (13 px 500 --muted; the chosen --ink 600 with a Mint icon) on a 1 px --line
// hairline, with one 2 px --accent indicator under the chosen label that slides to the next
// choice (160 ms, --ease-out). Each tab keeps the width its label has in bold, so nothing
// shifts. The arrows (mirrored right to left), Home and End move and choose at once, skipping
// disabled tabs; Tab reaches the chosen tab. A row too long for its box scrolls sideways and
// keeps the chosen tab in view.
type Tabs struct {
	kit.Element
	Options  []TabOption
	Value    string
	OnChange func(string)
	o        TabsOptions
	tabs     []*tab
	rtl      bool
	scroll   kit.DIP // how far the row is scrolled sideways
	bar      kit.Rect
	barTo    kit.Rect
	tween    *kit.Tween
	laid     bool
}

// NewTabs makes a tab row.
func NewTabs(opts []TabOption, value string, o TabsOptions, onChange func(string)) *Tabs {
	t := &Tabs{Options: opts, Value: value, OnChange: onChange, o: o}
	t.Semantics.Role = kit.RoleTabList
	t.Semantics.Name = o.AriaLabel
	t.Semantics.Orientation = kit.OrientationHorizontal
	t.Style = kit.Style{MinWidth: kit.Px(0), Overflow: kit.OverflowHidden}
	if o.Flush {
		t.Style.Margin = kit.Edges{Start: kit.Px(-t.pad())}
	}
	for i := range opts {
		t.tabs = append(t.tabs, t.newTab(i))
		t.Add(t.tabs[i])
	}
	t.sync()
	return t
}

func (t *Tabs) pad() kit.DIP {
	if t.o.Small {
		return 10
	}
	return 12
}

func (t *Tabs) height() kit.DIP {
	if t.o.Small {
		return 28
	}
	return 32
}

// SetValue changes the chosen tab without OnChange (the indicator slides).
func (t *Tabs) SetValue(v string) {
	if v != t.Value {
		t.Value = v
		t.sync()
		t.RelayoutContent()
	}
}

// Indicator is where the indicator is drawn now (the tab row's coordinates).
func (t *Tabs) Indicator() kit.Rect { return t.bar }

// TabWidget is the tab for value, or nil.
func (t *Tabs) TabWidget(value string) kit.Widget {
	for _, tb := range t.tabs {
		if t.Options[tb.i].Value == value {
			return tb
		}
	}
	return nil
}

func (t *Tabs) choose(i int, focus bool) {
	v := t.Options[i].Value
	if focus {
		if w := t.Window(); w != nil {
			w.SetFocusVisible(true)
			w.Focus(t.tabs[i])
		}
	}
	if v == t.Value {
		return
	}
	t.Value = v
	t.sync()
	t.RelayoutContent()
	if t.OnChange != nil {
		t.OnChange(v)
	}
}

// sync gives every tab its state: the chosen one bold and the one Tab reaches.
func (t *Tabs) sync() {
	for _, tb := range t.tabs {
		on := tb.on()
		tb.Focus = kit.FocusClick
		if on {
			tb.Focus = kit.FocusTab
		}
		tb.Disabled = t.o.Disabled || t.Options[tb.i].Disabled
		f := font(tokens.TypeBody, 500)
		if on {
			f.Weight = 600
		}
		tb.label.label.SetFont(f)
		tb.update()
		tb.SemanticsChanged()
	}
}

// ContentWidths is kit.Layouter's: the tabs side by side (the row scrolls, so it needs none).
func (t *Tabs) ContentWidths(ctx *kit.LayoutContext) (min, max kit.DIP) {
	var w kit.DIP
	for i, tb := range t.tabs {
		_, mx := ctx.IntrinsicWidths(tb)
		w += mx
		if i > 0 {
			w += 2
		}
	}
	return 0, w
}

// ContentHeight is kit.Layouter's.
func (t *Tabs) ContentHeight(*kit.LayoutContext, kit.DIP) kit.DIP { return t.height() }

// LayoutContent is kit.Layouter's: the tabs in a row from the inline start, scrolled to keep
// the chosen one in view, and the indicator under its label.
func (t *Tabs) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	t.rtl = ctx.RTL
	h := t.height()
	widths := make([]kit.DIP, len(t.tabs))
	var total kit.DIP
	for i, tb := range t.tabs {
		_, widths[i] = ctx.IntrinsicWidths(tb)
		total += widths[i]
		if i > 0 {
			total += 2
		}
	}
	// keep the chosen tab in view (24 px of room beside it), never scrolling otherwise
	max := kit.DIP(maxf(0, float32(total-box.Width())))
	var at kit.DIP
	for i, tb := range t.tabs {
		if tb.on() && max > 0 {
			start, end := at-24, at+widths[i]+24
			if start < t.scroll {
				t.scroll = start
			} else if end > t.scroll+box.Width() {
				t.scroll = end - box.Width()
			}
		}
		at += widths[i] + 2
	}
	t.scroll = kit.DIP(maxf(0, minf(float32(t.scroll), float32(max))))
	x := -t.scroll
	var bar kit.Rect
	for i, tb := range t.tabs {
		r := kit.R(box.Min.X+x, box.Min.Y, box.Min.X+x+widths[i], box.Min.Y+h)
		if t.rtl {
			r = kit.R(box.Max.X-x-widths[i], box.Min.Y, box.Max.X-x, box.Min.Y+h)
		}
		ctx.Place(tb, r, box.Width())
		if tb.on() {
			lb := tb.label.Bounds().Add(r.Min)
			bar = kit.R(lb.Min.X, box.Min.Y+h-2, lb.Max.X, box.Min.Y+h)
		}
		x += widths[i] + 2
	}
	t.moveBar(bar)
}

func minf(a, b float32) float32 {
	if a < b {
		return a
	}
	return b
}

func (t *Tabs) moveBar(r kit.Rect) {
	if r == t.barTo && t.laid {
		return
	}
	t.barTo = r
	w := t.Window()
	if !t.laid || w == nil || r.Empty() || t.bar.Empty() {
		t.laid = true
		t.bar = r
		t.Invalidate()
		return
	}
	if t.tween != nil {
		w.StopAnimation(t.tween)
	}
	from := t.bar
	t.tween = &kit.Tween{From: 0, To: 1, Duration: tokens.Dur3, Ease: tokens.EaseOut.Easing(), Set: func(v float32) {
		t.bar = kit.R(from.Min.X+(r.Min.X-from.Min.X)*v, r.Min.Y, from.Max.X+(r.Max.X-from.Max.X)*v, r.Max.Y)
		t.Invalidate()
	}}
	w.Animate(t.tween)
}

// Paint is kit.Painter's: the hairline the tabs stand on (under the content when flush).
func (t *Tabs) Paint(p *kit.PaintContext) {
	th := tokensOf(&t.Element)
	c := th.Line
	if th.IsForced() {
		c = th.Ink
	}
	sz := p.Size()
	x0 := kit.DIP(0)
	if t.o.Flush {
		x0 = t.pad()
		if t.rtl {
			x0 = 0
			sz.W -= t.pad()
		}
	}
	w := p.Scale.SnapWidth(1)
	y := p.Scale.Snap(sz.H) - w
	p.FillRect(kit.R(x0, y, sz.W, y+w), c)
}

// PaintOver is kit.OverPainter's: the indicator (2 px, corners 2 2 0 0).
func (t *Tabs) PaintOver(p *kit.PaintContext) {
	if t.bar.Empty() {
		return
	}
	th := tokensOf(&t.Element)
	c := th.Accent
	if th.IsForced() {
		c = th.Focus
	}
	r := p.Scale.SnapRect(t.bar.Add(p.Origin))
	p.Canvas.FillRoundedRect(kit.RRect{Rect: r, Radii: kit.Radii{TopLeft: 2, TopRight: 2}}, c)
}

// HandleWheel is kit.WheelHandler's: a row too long for its box scrolls sideways.
func (t *Tabs) HandleWheel(in *kit.WheelInput) bool {
	d := in.Delta.X
	if d == 0 && in.Mods&kit.ModShift != 0 {
		d = in.Delta.Y
	}
	if d == 0 {
		return false
	}
	t.scroll += d
	t.RelayoutContent()
	return true
}

// tab is one tab: a button with its label (and its bold twin's width), icon, count and dot.
type tab struct {
	control
	t     *Tabs
	i     int
	icon  *Icon
	label *twinLabel
	count *segCount
}

func (t *Tabs) newTab(i int) *tab {
	op := t.Options[i]
	tb := &tab{t: t, i: i}
	tb.init(func() { t.choose(tb.i, false) })
	tb.Semantics.Role = kit.RoleTab
	tb.Semantics.PosInSet, tb.Semantics.SetSize = i+1, len(t.Options)
	tb.Semantics.Controls = t.o.Panel
	if t.o.IDPrefix != "" {
		tb.Semantics.AutomationID = TabID(t.o.IDPrefix, op.Value)
	}
	tb.Tooltip = op.Title
	tb.Ring = kit.Ring{Offset: kit.Px(-2)}
	tb.Style = kit.Style{
		Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(7), Flex: kit.FlexNone,
		Height: kit.Px(t.height()), Padding: kit.Edges{Bottom: kit.Px(1), Start: kit.Px(t.pad()), End: kit.Px(t.pad())},
		Radius: kit.Corners{TopStart: tokens.RSm, TopEnd: tokens.RSm},
	}
	if ic, ok := iconOf(op.Icon); ok {
		size := kit.DIP(16)
		if t.o.Small {
			size = 14
		}
		tb.icon = NewIcon(ic, size, func(th *tokens.Theme) kit.Color {
			if tb.on() && !th.IsForced() {
				return th.Accent
			}
			return tb.ink(th, tb.look.state())
		})
		tb.Add(tb.icon)
	}
	tb.label = newTwinLabel(op.Label, font(tokens.TypeBody, 500), 600)
	tb.Add(tb.label)
	if op.HasCount {
		tb.count = newSegCount(tb.on, op.Count)
		tb.Add(tb.count)
	}
	if op.Dot {
		d := &tabDot{}
		d.Style = kit.Style{Width: kit.Px(6), Height: kit.Px(6), Flex: kit.FlexNone}
		d.Semantics.Hidden = true
		tb.Add(d)
	}
	return tb
}

func (tb *tab) on() bool { return tb.t.Options[tb.i].Value == tb.t.Value }

func (tb *tab) ink(th *tokens.Theme, s uint8) kit.Color {
	switch {
	case tb.Disabled:
		return th.Faint
	case tb.on() && th.IsForced():
		return th.Focus
	case tb.on():
		return th.Ink
	case s&stHover != 0:
		return th.Ink
	}
	return th.Muted
}

// Paint is kit.Painter's: the hover ground of a tab that is not chosen.
func (tb *tab) Paint(p *kit.PaintContext) {
	th := tokensOf(&tb.Element)
	ground := func(s uint8) kit.Color {
		if s&stHover != 0 && !tb.on() && !tb.Disabled {
			return th.Hover
		}
		return kit.Color{}
	}
	if bg := tb.look.mix(ground(tb.look.fromState()), ground(tb.look.state())); bg.A > 0 {
		r := p.Scale.SnapRect(kit.RectAt(p.Origin, p.Size()))
		p.Canvas.FillRoundedRect(kit.RRect{Rect: r, Radii: kit.Radii{TopLeft: 6, TopRight: 6}}, bg)
	}
	tb.label.label.SetColor(tb.look.mix(tb.ink(th, tb.look.fromState()), tb.ink(th, tb.look.state())))
}

// HandleKey is kit.KeyHandler's: arrows (mirrored right to left), Home and End choose and
// focus the next enabled tab, wrapping.
func (tb *tab) HandleKey(e kit.KeyEvent) bool {
	if e.Action == kit.KeyDown && e.Mods&(kit.ModCtrl|kit.ModAlt|kit.ModMeta) == 0 {
		t := tb.t
		next, prev := kit.KeyArrowRight, kit.KeyArrowLeft
		if t.rtl {
			next, prev = prev, next
		}
		n := len(t.Options)
		enabled := func(i int) bool { return !t.Options[i].Disabled && !t.o.Disabled }
		to := -1
		switch e.Key {
		case next, kit.KeyArrowDown:
			for k := 1; k <= n && to < 0; k++ {
				if j := (tb.i + k) % n; enabled(j) {
					to = j
				}
			}
		case prev, kit.KeyArrowUp:
			for k := 1; k <= n && to < 0; k++ {
				if j := (tb.i - k + n) % n; enabled(j) {
					to = j
				}
			}
		case kit.KeyHome:
			for j := 0; j < n && to < 0; j++ {
				if enabled(j) {
					to = j
				}
			}
		case kit.KeyEnd:
			for j := n - 1; j >= 0 && to < 0; j-- {
				if enabled(j) {
					to = j
				}
			}
		}
		if to >= 0 {
			t.choose(to, true)
			return true
		}
	}
	return tb.control.HandleKey(e)
}

// FillSemantics is kit.SemanticFiller's.
func (tb *tab) FillSemantics(n *kit.SemanticNode) {
	n.States |= kit.NodeSelectable
	if tb.on() {
		n.States |= kit.NodeSelected
	}
	n.Actions |= kit.ActionSelect
}

// PerformAction is kit.ActionHandler's.
func (tb *tab) PerformAction(r kit.ActionRequest) error {
	switch r.Action {
	case kit.ActionSelect, kit.ActionInvoke:
		if tb.Disabled {
			return kit.ErrNotEnabled
		}
		tb.t.choose(tb.i, false)
		return nil
	}
	return kit.ErrUnsupported
}

// tabDot is a tab's 6 px Mint dot.
type tabDot struct{ kit.Element }

// Paint is kit.Painter's.
func (d *tabDot) Paint(p *kit.PaintContext) {
	th := tokensOf(&d.Element)
	c := th.Accent
	if th.IsForced() {
		c = th.Focus
	}
	p.Canvas.FillEllipse(p.Scale.SnapRect(kit.RectAt(p.Origin, p.Size())), c)
}

// twinLabel is a label as wide as its text in bold (Tabs.svelte's bold twin): a tab keeps its
// width whether it is chosen or not.
type twinLabel struct {
	kit.Element
	label  *kit.Label
	bold   float32
	twin   kit.TextLayout
	twinOf kit.TextStyle
	text   string
}

func newTwinLabel(text string, f kit.Font, bold float32) *twinLabel {
	l := &twinLabel{bold: bold, text: text}
	l.label = kit.NewLabel(text, f, kit.TextOptions{})
	l.label.Semantics.Role = kit.RoleNone
	l.Add(l.label)
	return l
}

func (l *twinLabel) twinWidth(ctx *kit.LayoutContext) kit.DIP {
	if ctx.Text == nil {
		return 0
	}
	f := l.label.Font()
	f.Weight = l.bold
	st := f.Style(ctx.Scale)
	if l.twin == nil || st != l.twinOf {
		if l.twin != nil {
			l.twin.Release()
			l.twin = nil
		}
		t, err := ctx.Text.LayoutText(l.text, st, kit.TextOptions{})
		if err != nil {
			return 0
		}
		l.twin, l.twinOf = t, st
	}
	return l.twin.Metrics().Width
}

// ContentWidths is kit.Layouter's.
func (l *twinLabel) ContentWidths(ctx *kit.LayoutContext) (min, max kit.DIP) {
	_, w := ctx.IntrinsicWidths(l.label)
	w = kit.DIP(maxf(float32(w), float32(l.twinWidth(ctx))))
	return w, w
}

// ContentHeight is kit.Layouter's.
func (l *twinLabel) ContentHeight(ctx *kit.LayoutContext, w kit.DIP) kit.DIP {
	return ctx.HeightFor(l.label, w, w)
}

// LayoutContent is kit.Layouter's: the label at the start of the box. [mac webkit] WebKit
// centres it in the bold twin's width instead (the button's text-align: center applies to
// the text of the inline-grid .label, whose column is the twin's width): a 500-weight label
// sits half the difference further in.
func (l *twinLabel) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	if ctx.Env.WebKit {
		if _, w := ctx.IntrinsicWidths(l.label); w < box.Width() {
			d := (box.Width() - w) / 2
			box = kit.R(box.Min.X+d, box.Min.Y, box.Max.X-d, box.Max.Y)
		}
	}
	ctx.Place(l.label, box, box.Width())
}

// ContentBaseline is kit.Baseliner's.
func (l *twinLabel) ContentBaseline() (kit.DIP, bool) { return l.label.ContentBaseline() }

// Detached is kit.Detacher's.
func (l *twinLabel) Detached() {
	if l.twin != nil {
		l.twin.Release()
		l.twin = nil
	}
}
