package widgets

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"downex/ui/kit"
	"downex/ui/kit/icons"
	"downex/ui/kit/tokens"
)

// SelectOption is one choice of a Select (Select.svelte's SelectOption).
type SelectOption struct {
	Value string
	Label string
	// Detail is secondary text after the label in --muted ("AAC 129 kbit/s").
	Detail string
	// Meta is a figure at the end (a size).
	Meta     string
	Disabled bool
	Title    string
	// Group lists options with the same group under its caps heading.
	Group string
}

// SelectSize is a Select's trigger.
type SelectSize uint8

// The trigger sizes.
const (
	SelectMd     SelectSize = iota // 36 px (dialogs)
	SelectSm                       // 30 px (the settings grid, popovers)
	SelectInline                   // 26 px, borderless until hovered (the details pane's limit)
)

// SelectOptions are a Select's choices beyond its options.
type SelectOptions struct {
	// Label is the trigger's label (what a <label for> says); it names the combobox and its
	// list. AriaLabel, when set, names them instead.
	Label, AriaLabel string
	Size             SelectSize
	// Icon is Icon.svelte's name for a leading icon ("" for none).
	Icon string
	// Placeholder shows when the value is none of the options.
	Placeholder string
	// Title is the trigger's tooltip (else the chosen option's).
	Title    string
	Disabled bool
	Invalid  bool
}

// typeaheadWindow is how long typed letters make one search (TYPEAHEAD_MS).
const typeaheadWindow = 500 * time.Millisecond

// selectPage is how far Page Up and Page Down move (PAGE).
const selectPage = 10

// Select is Select.svelte (DESIGN-SYSTEM.md §3.5) on the WAI-ARIA select-only combobox: a
// trigger (role combobox; --field with a --line-field border and 9 px corners) and a list in a
// popup window under it, or above it when there is no room, 4 px away. The focus stays on the
// trigger, which names the active option. Closed: Enter, Space, Down or Alt+Down open on the
// chosen option (Up too; Home and End on the first and last); typing chooses without opening.
// Open: Up and Down, Home and End, Page Up and Page Down (10), type-ahead; Enter, Space or
// Alt+Up choose and close; Esc closes without a change (and only the list); Tab chooses the
// active option and moves on; a click outside closes.
type Select struct {
	kit.Element
	Options  []SelectOption
	OnChange func(string)
	o        SelectOptions

	value   string
	open    bool
	active  int
	viaKeys bool
	typed   string
	typedAt time.Time

	lead    *Icon
	shown   *kit.Label
	dot     *kit.Label
	detail  *kit.Label
	chevron *chevron
	look    transition

	pop  *popup
	list *selectList
}

// NewSelect makes a Select.
func NewSelect(opts []SelectOption, value string, o SelectOptions, onChange func(string)) *Select {
	s := &Select{Options: opts, value: value, o: o, OnChange: onChange, active: -1}
	s.Focus = kit.FocusTab
	s.Cursor = kit.CursorPointer
	s.Ring = kit.Ring{None: true} // its border and ring show the focus
	s.Disabled = o.Disabled
	s.Semantics.Role = kit.RoleComboBox
	s.Semantics.States |= kit.NodeHasPopup
	s.Semantics.Name = o.AriaLabel
	if s.Semantics.Name == "" {
		s.Semantics.Name = o.Label
	}
	h, padS, padE, gap := kit.DIP(36), kit.DIP(11), kit.DIP(9), kit.DIP(8)
	switch o.Size {
	case SelectSm:
		h = 30
	case SelectInline:
		h, padS, padE, gap = 26, 7, 5, 5
	}
	s.Style = kit.Style{
		Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(gap), Width: kit.Pct(100), MinWidth: kit.Px(0),
		Height: kit.Px(h), Padding: kit.Edges{Start: kit.Px(padS), End: kit.Px(padE)}, Border: kit.EdgesAll(kit.Px(1)),
	}
	if o.Size == SelectInline {
		s.Style.Width = kit.Length{}
		s.Style.Flex = kit.FlexNone
	}
	if ic, ok := iconOf(o.Icon); ok {
		s.lead = NewIcon(ic, 16, func(t *tokens.Theme) kit.Color { return t.Faint })
		s.Add(s.lead)
	}
	f := FontBody
	if o.Size == SelectInline {
		f.Weight = 600
	}
	s.shown = label("", f)
	s.dot = label("·", f) // .dot-sep: margin-inline 0.4em
	s.dot.Style.Margin = kit.EdgesInline(kit.Rem(0.4 * 0.8125))
	s.dot.Style.Flex = kit.FlexNone
	s.detail = label("", f)
	shown := box(kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignBaseline, Flex: kit.FlexOf(1, 1, kit.Length{}), MinWidth: kit.Px(0), Overflow: kit.OverflowClipInline}, s.shown, s.dot, s.detail)
	s.shown.Style.Flex = kit.FlexOf(0, 1, kit.Length{})
	s.detail.Style.Flex = kit.FlexOf(0, 1, kit.Length{})
	s.Add(shown)
	s.chevron = &chevron{s: s}
	s.chevron.Style = kit.Style{Width: kit.Px(14), Height: kit.Px(14), Flex: kit.FlexNone}
	s.chevron.Semantics.Hidden = true
	s.Add(s.chevron)
	s.syncShown()
	return s
}

// Value is the chosen value.
func (s *Select) Value() string { return s.value }

// IsOpen reports whether the list is open.
func (s *Select) IsOpen() bool { return s.open }

// Active is the value of the active option while open, else "".
func (s *Select) Active() string {
	if !s.open || s.active < 0 || s.active >= len(s.Options) {
		return ""
	}
	return s.Options[s.active].Value
}

// ViaKeys reports whether the active option was last moved by the keyboard (it then has the
// focus ring too).
func (s *Select) ViaKeys() bool { return s.viaKeys }

// List is the open list's popup window, or nil.
func (s *Select) List() *kit.Window {
	if s.pop == nil || s.pop.closed {
		return nil
	}
	return s.pop.win
}

// ListBounds is where the open list's box is (owner window DIPs).
func (s *Select) ListBounds() kit.Rect {
	if s.pop == nil {
		return kit.Rect{}
	}
	return s.pop.box
}

// SetValue changes the choice without OnChange.
func (s *Select) SetValue(v string) {
	if v != s.value {
		s.value = v
		s.syncShown()
	}
}

// SetOptions replaces the options.
func (s *Select) SetOptions(opts []SelectOption) {
	s.close()
	s.Options = opts
	s.syncShown()
}

// SetDisabled makes the Select unavailable (HTML's disabled).
func (s *Select) SetDisabled(d bool) {
	if d == s.Disabled {
		return
	}
	s.Disabled = d
	if d {
		s.close()
	}
	s.syncShown()
}

// SetInvalid marks the value wrong (aria-invalid: a --danger border).
func (s *Select) SetInvalid(v bool) {
	if v != s.o.Invalid {
		s.o.Invalid = v
		s.Invalidate()
		s.SemanticsChanged()
	}
}

func (s *Select) selectedIndex() int {
	for i, o := range s.Options {
		if o.Value == s.value {
			return i
		}
	}
	return -1
}

func (s *Select) syncShown() {
	i := s.selectedIndex()
	s.Tooltip = s.o.Title
	if i >= 0 {
		s.shown.SetText(s.Options[i].Label)
		if s.Tooltip == "" {
			s.Tooltip = s.Options[i].Title
		}
		d := s.Options[i].Detail
		s.detail.SetText(d)
		s.detail.SetHidden(d == "")
		s.dot.SetHidden(d == "")
	} else {
		s.shown.SetText(s.o.Placeholder)
		s.detail.SetHidden(true)
		s.dot.SetHidden(true)
	}
	s.Cursor = kit.CursorPointer
	if s.Disabled {
		s.Cursor = kit.CursorDefault
	}
	s.Invalidate()
	s.SemanticsChanged()
}

func (s *Select) enabled(i int) bool { return i >= 0 && i < len(s.Options) && !s.Options[i].Disabled }

// nearest is the nearest enabled option from from in step's direction (from itself included).
func (s *Select) nearest(from, step int) int {
	for i := from; i >= 0 && i < len(s.Options); i += step {
		if s.enabled(i) {
			return i
		}
	}
	return -1
}

func (s *Select) first() int { return s.nearest(0, 1) }
func (s *Select) last() int  { return s.nearest(len(s.Options)-1, -1) }

func (s *Select) move(to, step int) {
	c := max(0, min(len(s.Options)-1, to))
	i := s.nearest(c, step)
	if i < 0 {
		i = s.nearest(c, -step)
	}
	s.setActive(i)
}

func (s *Select) setActive(i int) {
	if i == s.active {
		return
	}
	s.active = i
	if s.list != nil {
		s.list.activeChanged()
	}
	s.SemanticsChanged()
}

// search is type-ahead: the next option whose label starts with what was typed (a repeated
// letter cycles).
func (s *Select) search(key string, from int) int {
	now := popupNow(s.Window())
	if now.Sub(s.typedAt) > typeaheadWindow {
		s.typed = key
	} else {
		s.typed += key
	}
	s.typedAt = now
	needle := strings.ToLower(s.typed)
	r0, _ := utf8.DecodeRuneInString(needle)
	cycle := utf8.RuneCountInString(needle) > 1 && strings.Trim(needle, string(r0)) == ""
	prefix := needle
	if cycle {
		prefix = string(r0)
	}
	n := len(s.Options)
	start := max(0, from)
	if cycle || utf8.RuneCountInString(needle) == 1 {
		start = from + 1
	}
	for k := range n {
		i := (((start + k) % n) + n) % n
		if s.enabled(i) && strings.HasPrefix(strings.ToLower(s.Options[i].Label), prefix) {
			return i
		}
	}
	return -1
}

func (s *Select) choose(i int) {
	if !s.enabled(i) {
		return
	}
	v := s.Options[i].Value
	changed := v != s.value
	s.value = v
	s.syncShown()
	if changed && s.OnChange != nil {
		s.OnChange(v)
	}
}

// Open opens the list: on the chosen option (FocusChecked; the first enabled one when the
// chosen one is not), or on the first or last enabled one.
func (s *Select) Open(at InitialFocus) {
	if s.Disabled || s.open {
		return
	}
	w := s.Window()
	if w == nil {
		return
	}
	idx := s.selectedIndex()
	switch {
	case at == FocusFirst:
		idx = s.first()
	case at == FocusLast:
		idx = s.last()
	case !s.enabled(idx):
		idx = s.first()
	}
	s.active = idx
	theme := ThemeOf(&s.Element)
	view := w.Size()
	trig := s.WindowBounds()
	pos := PlaceSelectList(trig, len(s.Options), view)
	s.list = newSelectList(s, pos.MaxHeight, view)
	size := measure(w, theme, s.list)
	size.W = max(size.W, trig.Width())
	left := pos.Left
	if size.W > trig.Width() || isRTL(w) {
		start := pos.Left
		if isRTL(w) {
			start = trig.Max.X - size.W
		}
		left = max(8, min(start, view.W-size.W-8))
	}
	top := pos.Top
	if pos.Up {
		top = pos.Bottom - size.H
	}
	pop, err := openPopup(s, theme, s.list, kit.R(left, top, left+size.W, top+size.H), s.Semantics.Name, s.dismissed)
	if err != nil {
		s.list = nil
		s.active = -1
		return
	}
	s.pop, s.open = pop, true
	s.list.activeChanged()
	s.chevron.turn(true)
	s.Invalidate()
	s.SemanticsChanged()
}

// close closes the list.
func (s *Select) close() {
	if !s.open {
		return
	}
	s.open = false
	s.active = -1
	s.pop.close()
	s.pop, s.list = nil, nil
	s.chevron.turn(false)
	s.Invalidate()
	s.SemanticsChanged()
}

// Close closes the list without a change.
func (s *Select) Close() { s.close() }

func (s *Select) dismissed(d kit.Dismiss) {
	if !s.open {
		return
	}
	if d.Reason == kit.DismissPress && s.WindowBounds().Contains(d.Pos) {
		return // the trigger's own press toggles it
	}
	s.close()
}

func (s *Select) openOn(which string) {
	switch which {
	case "first":
		s.Open(FocusFirst)
	case "last":
		s.Open(FocusLast)
	default:
		s.Open(FocusChecked)
	}
}

// HandleKey is kit.KeyHandler's: the combobox's keyboard model.
func (s *Select) HandleKey(e kit.KeyEvent) bool {
	if s.Disabled || e.Action != kit.KeyDown {
		return false
	}
	s.viaKeys = true
	alt := e.Mods&kit.ModAlt != 0
	plain := e.Mods&(kit.ModCtrl|kit.ModMeta) == 0
	if !s.open {
		switch e.Key {
		case kit.KeyArrowDown, kit.KeyArrowUp, kit.KeyEnter, kit.KeySpace:
			if e.Mods&(kit.ModCtrl|kit.ModMeta) != 0 {
				return false
			}
			s.openOn("selected")
			return true
		case kit.KeyHome:
			s.openOn("first")
			return true
		case kit.KeyEnd:
			s.openOn("last")
			return true
		}
		return false // letters arrive as text (HandleText)
	}
	switch e.Key {
	case kit.KeyArrowDown:
		s.move(s.active+1, 1)
	case kit.KeyArrowUp:
		if alt {
			if s.active >= 0 {
				s.choose(s.active)
			}
			s.close()
		} else {
			s.move(s.active-1, -1)
		}
	case kit.KeyHome:
		s.move(0, 1)
	case kit.KeyEnd:
		s.move(len(s.Options)-1, -1)
	case kit.KeyPageDown:
		s.move(s.active+selectPage, 1)
	case kit.KeyPageUp:
		s.move(s.active-selectPage, -1)
	case kit.KeyEnter, kit.KeySpace:
		if !plain {
			return false
		}
		if s.active >= 0 {
			s.choose(s.active)
		}
		s.close()
	case kit.KeyEscape:
		s.close() // the list only: a dialog around it stays open
	case kit.KeyTab:
		if s.active >= 0 {
			s.choose(s.active)
		}
		s.close()
		return false // and the focus moves on
	default:
		return false
	}
	return true
}

// HandleText is kit.TextHandler's: type-ahead. Closed, it chooses without opening; open, it
// moves the active option.
func (s *Select) HandleText(e kit.TextEvent) bool {
	if s.Disabled || utf8.RuneCountInString(e.Text) != 1 {
		return false
	}
	r, _ := utf8.DecodeRuneInString(e.Text)
	if unicode.IsSpace(r) || unicode.IsControl(r) {
		return false
	}
	s.viaKeys = true
	if !s.open {
		if i := s.search(e.Text, s.selectedIndex()); i >= 0 {
			s.choose(i)
		}
		return true
	}
	if i := s.search(e.Text, s.active); i >= 0 {
		s.setActive(i)
	}
	return true
}

// HandlePointer is kit.PointerHandler's: a click toggles the list.
func (s *Select) HandlePointer(in *kit.PointerInput) bool {
	if s.Disabled {
		return false
	}
	switch in.Action {
	case kit.PointerDown:
		return in.Button == kit.ButtonLeft
	case kit.PointerUp:
		if in.Button != kit.ButtonLeft || !kit.RectAt(kit.Point{}, s.Size()).Contains(in.Local) {
			return false
		}
		s.viaKeys = false
		if s.open {
			s.close()
		} else {
			s.openOn("selected")
		}
		return true
	}
	return false
}

// HoverChanged is kit.HoverHandler's.
func (s *Select) HoverChanged(bool) { s.Invalidate() }

// FocusChanged is kit.FocusHandler's: leaving the trigger closes the list.
func (s *Select) FocusChanged(f bool) {
	if !f {
		s.close()
	}
	s.Invalidate()
}

// Detached is kit.Detacher's.
func (s *Select) Detached() { s.close() }

// FillSemantics is kit.SemanticFiller's: the shown choice, the list's state.
func (s *Select) FillSemantics(n *kit.SemanticNode) {
	if i := s.selectedIndex(); i >= 0 {
		n.Value = s.Options[i].Label
	}
	n.Expand = kit.ExpandClosed
	if s.open {
		n.Expand = kit.ExpandOpen
	}
	if s.o.Invalid {
		n.States |= kit.NodeInvalid
	}
	n.Actions |= kit.ActionExpand | kit.ActionCollapse | kit.ActionSetValue
}

// PerformAction is kit.ActionHandler's.
func (s *Select) PerformAction(r kit.ActionRequest) error {
	if s.Disabled {
		return kit.ErrNotEnabled
	}
	switch r.Action {
	case kit.ActionExpand:
		s.openOn("selected")
	case kit.ActionCollapse:
		s.close()
	case kit.ActionSetValue:
		for i, o := range s.Options {
			if o.Value == r.Text || o.Label == r.Text {
				s.choose(i)
				return nil
			}
		}
		return kit.ErrInvalid
	default:
		return kit.ErrUnsupported
	}
	return nil
}

func (s *Select) borderColor(t *tokens.Theme) kit.Color {
	switch {
	case t.IsForced():
		return t.Ink
	case s.o.Invalid:
		return t.Danger
	case s.Focused() || s.open:
		return t.Focus
	case s.o.Size == SelectInline && !(s.Hovered() && !s.Disabled):
		return kit.Color{}
	case s.o.Size == SelectInline:
		return t.Line
	}
	return t.LineField
}

// Paint is kit.Painter's: the trigger's box and focus look, the texts' colours.
func (s *Select) Paint(p *kit.PaintContext) {
	t := tokensOf(&s.Element)
	rad := kit.DIP(9)
	switch s.o.Size {
	case SelectSm:
		rad = 8
	case SelectInline:
		rad = 6
	}
	r := snapBox(p, rad)
	focused := s.Focused() || s.open
	if focused && !t.IsForced() {
		drawFieldFocus(p.Canvas, r, t.Focus, t.AccentSoft)
	}
	ground := t.Field
	switch {
	case s.Disabled:
		ground = t.Surface2
	case s.o.Size == SelectInline && !(s.Hovered() || s.open):
		ground = kit.Color{}
	}
	if ground.A > 0 {
		p.Canvas.FillRoundedRect(r, ground)
	}
	strokeInside(p.Canvas, r, p.Scale.SnapWidth(1), s.borderColor(t))
	if focused && t.IsForced() && s.Focused() && p.Element().Window().RingShown() {
		w := p.Scale.SnapWidth(2)
		p.Canvas.StrokeRoundedRect(r.Inset(-1-w/2), kit.Stroke{Width: w}, t.Focus)
	}
	ink := t.Ink
	switch {
	case s.Disabled:
		ink = t.Faint
	case s.selectedIndex() < 0:
		ink = t.Faint // the placeholder
	}
	s.shown.SetColor(ink)
	s.dot.SetColor(t.Muted)
	s.detail.SetColor(t.Muted)
}

// chevron is the trigger's chevron (14 px --muted), turned half a turn while the list is open
// (160 ms, --ease-out).
type chevron struct {
	kit.Element
	s     *Select
	turnV float32 // 0 down, 1 up
	tween *kit.Tween
}

func (c *chevron) turn(open bool) {
	to := float32(0)
	if open {
		to = 1
	}
	w := c.Window()
	if c.tween != nil && w != nil {
		w.StopAnimation(c.tween)
	}
	if w == nil {
		c.turnV = to
		return
	}
	c.tween = &kit.Tween{From: c.turnV, To: to, Duration: tokens.Dur3, Ease: tokens.EaseOut.Easing(), Set: func(v float32) {
		c.turnV = v
		c.Invalidate()
	}}
	w.Animate(c.tween)
}

// Paint is kit.Painter's.
func (c *chevron) Paint(p *kit.PaintContext) {
	w := c.Window()
	if w == nil || w.Host() == nil {
		return
	}
	t := tokensOf(&c.Element)
	at := kit.Pt(p.Scale.Snap(p.Origin.X), p.Scale.Snap(p.Origin.Y))
	if c.turnV != 0 {
		cx, cy := at.X+7, at.Y+7
		p.Canvas.PushTransform(kit.Translation(-cx, -cy).Then(kit.Rotation(180 * c.turnV)).Then(kit.Translation(cx, cy)))
	}
	iconsFor(w.Host().Resources()).DrawIcon(p.Canvas, icons.ChevronDown, at, 14, t.Muted)
	if c.turnV != 0 {
		p.Canvas.Pop()
	}
}

// ListPlace is where a Select's list goes (Select.svelte's place): its left edge, and either
// its top (below the trigger) or its bottom (above it), with the most height it may have.
type ListPlace struct {
	Left      kit.DIP
	Up        bool
	Top       kit.DIP // the list's top, when below the trigger
	Bottom    kit.DIP // the list's bottom, when above it
	MaxHeight kit.DIP
}

// PlaceSelectList is Select.svelte's place for a trigger at trig (window DIPs) with n options
// in a viewport of size view: 4 px below the trigger, or above it when the room below is less
// than the list wants (n × 30 + 12, between 120 and 320) and the room above is larger, never
// taller than 320 nor shorter than 90, kept 8 px inside the viewport.
func PlaceSelectList(trig kit.Rect, n int, view kit.Size) ListPlace {
	const margin, gap = 8, 4
	below := view.H - trig.Max.Y - gap - margin
	above := trig.Min.Y - gap - margin
	wanted := min(320, max(120, kit.DIP(n)*30+12))
	up := below < wanted && above > below
	room := below
	if up {
		room = above
	}
	pl := ListPlace{
		Left:      max(margin, min(trig.Min.X, view.W-trig.Width()-margin)),
		Up:        up,
		MaxHeight: max(90, min(320, room)),
	}
	if up {
		pl.Bottom = trig.Min.Y - gap
	} else {
		pl.Top = trig.Max.Y + gap
	}
	return pl
}

// selectList is the list in its popup window: a --raised box (5 px padding, a --line border,
// 11 px corners, --shadow-lg) at most min(480, viewport - 16) wide, at least the trigger's.
type selectList struct {
	kit.Element
	s    *Select
	pane *ScrollPane
	opts []*selectOpt
}

func newSelectList(s *Select, maxH kit.DIP, view kit.Size) *selectList {
	l := &selectList{s: s}
	l.Semantics.Role = kit.RoleListBox
	l.Semantics.Name = s.Semantics.Name
	l.Style = kit.Style{
		Display: kit.DisplayBlock, Padding: kit.EdgesAll(kit.Px(5)), Border: kit.EdgesAll(kit.Px(1)), Radius: kit.CornersAll(kit.Px(11)),
		MinWidth: kit.Px(s.WindowBounds().Width()), MaxWidth: kit.Px(min(480, view.W-16)),
	}
	body := &kit.Element{Style: kit.Style{Display: kit.DisplayBlock}}
	groups := map[string]*kit.Element{}
	var order []string
	for i, o := range s.Options {
		g, ok := groups[o.Group]
		if !ok {
			g = &kit.Element{Style: kit.Style{Display: kit.DisplayBlock}}
			if o.Group != "" {
				g.Semantics.Role = kit.RoleGroup
				g.Semantics.Name = o.Group
				h := kit.NewLabel(strings.ToUpper(o.Group), fontCaps, kit.TextOptions{Truncate: kit.TruncateEnd})
				h.Semantics.Role = kit.RoleNone
				h.Style.Padding = kit.Edges{Top: kit.Px(9), End: kit.Px(9), Bottom: kit.Px(5), Start: kit.Px(9)}
				g.Add(&menuHeading{Label: h})
			}
			groups[o.Group] = g
			order = append(order, o.Group)
		}
		op := newSelectOpt(l, i)
		l.opts = append(l.opts, op)
		g.Add(op)
	}
	for _, name := range order {
		body.Add(groups[name])
	}
	l.pane = NewScrollPane(body)
	l.pane.Style.MaxHeight = kit.Px(maxH - 12)
	l.Add(l.pane)
	return l
}

// activeChanged repaints the options and keeps the active one in view (and focused in the
// list's window, which stands for aria-activedescendant there).
func (l *selectList) activeChanged() {
	w := l.Window()
	for _, o := range l.opts {
		o.Invalidate()
	}
	if w == nil {
		return
	}
	w.SetFocusVisible(l.s.viaKeys)
	if a := l.s.active; a >= 0 && a < len(l.opts) {
		w.Focus(l.opts[a])
		w.AfterLayout(func() { l.pane.Show(&l.opts[a].Element) })
	} else {
		w.Blur()
	}
}

// Paint is kit.Painter's.
func (l *selectList) Paint(p *kit.PaintContext) {
	t := tokensOf(&l.Element)
	r := snapBox(p, 11)
	drawShadows(p.Canvas, r, t.ShadowLg)
	p.Canvas.FillRoundedRect(r, t.Raised)
	border := t.Line
	if t.IsForced() {
		border = t.Ink
	}
	strokeInside(p.Canvas, r, p.Scale.SnapWidth(1), border)
}

// selectOpt is one option: a tick, the label and its detail, and the meta.
type selectOpt struct {
	kit.Element
	l      *selectList
	i      int
	label  *kit.Label
	detail *kit.Label
	meta   *kit.Label
	tick   *Icon
	down   bool
}

func newSelectOpt(l *selectList, i int) *selectOpt {
	o := l.s.Options[i]
	op := &selectOpt{l: l, i: i}
	op.Focus = kit.FocusClick
	op.Ring = kit.Ring{Offset: kit.Px(-2)}
	op.Cursor = kit.CursorPointer
	if o.Disabled {
		op.Cursor = kit.CursorDefault
	}
	op.Tooltip = o.Title
	op.Semantics.Role = kit.RoleOption
	op.Semantics.PosInSet, op.Semantics.SetSize = i+1, len(l.s.Options)
	op.Style = kit.Style{
		Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: tokens.Sp4, MinHeight: kit.Px(30),
		Padding: kit.Edges{Start: kit.Px(8), End: kit.Px(10)}, Radius: kit.CornersAll(kit.Px(7)),
	}
	tick := box(kit.Style{Width: kit.Px(14), Flex: kit.FlexNone, Display: kit.DisplayFlex, Justify: kit.JustifyCenter, AlignItems: kit.AlignCenter})
	tick.Semantics.Hidden = true
	op.tick = NewIcon(icons.Check, 14, func(t *tokens.Theme) kit.Color { return t.Accent })
	tick.Add(op.tick)
	op.Add(tick)
	chosen := o.Value == l.s.value
	f := FontBody
	if chosen {
		f.Weight = 600
	}
	op.label = label(o.Label, f)
	op.label.Style.Flex = kit.FlexOf(0, 1, kit.Length{})
	text := box(kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignBaseline, Flex: kit.FlexOf(1, 1, kit.Length{}), MinWidth: kit.Px(0), Overflow: kit.OverflowClipInline}, op.label)
	if o.Detail != "" {
		op.detail = label(o.Detail, font(tokens.TypeSmall, 400))
		op.detail.Style.Margin = kit.Edges{Start: kit.Px(6)}
		op.detail.Style.Flex = kit.FlexOf(0, 1, kit.Length{})
		text.Add(op.detail)
	}
	op.Add(text)
	if o.Meta != "" {
		op.meta = label(o.Meta, font(tokens.TypeSmall, 400))
		op.meta.Style.Margin = kit.Edges{Start: kit.Px(16)}
		op.meta.Style.Flex = kit.FlexNone
		op.Add(op.meta)
	}
	return op
}

func (op *selectOpt) chosen() bool { return op.l.s.Options[op.i].Value == op.l.s.value }
func (op *selectOpt) active() bool { return op.l.s.active == op.i }

// Paint is kit.Painter's.
func (op *selectOpt) Paint(p *kit.PaintContext) {
	t := tokensOf(&op.Element)
	o := op.l.s.Options[op.i]
	r := snapBox(p, 7)
	if op.active() {
		if t.IsForced() {
			p.Canvas.FillRoundedRect(r, t.Focus)
		} else {
			p.Canvas.FillRoundedRect(r, t.Sel)
			strokeInside(p.Canvas, r, p.Scale.SnapWidth(1), t.SelLine)
		}
	}
	ink := t.Ink2
	switch {
	case o.Disabled:
		ink = t.Faint
	case op.active() || op.chosen():
		ink = t.Ink
	}
	if op.active() && t.IsForced() {
		ink = t.AccentOn
	}
	op.label.SetColor(ink)
	if op.detail != nil {
		op.detail.SetColor(t.Muted)
	}
	if op.meta != nil {
		c := t.Faint
		if op.active() {
			c = t.Muted
		}
		op.meta.SetColor(c)
	}
	op.tick.SetHidden(!op.chosen())
}

// HandlePointer is kit.PointerHandler's: moving over an option makes it the active one (the
// pointer's: no ring); a click chooses it, closes the list and keeps the trigger's focus.
func (op *selectOpt) HandlePointer(in *kit.PointerInput) bool {
	s := op.l.s
	o := s.Options[op.i]
	switch in.Action {
	case kit.PointerMove:
		if s.viaKeys || (!o.Disabled && !op.active()) {
			s.viaKeys = false
			if !o.Disabled {
				s.setActive(op.i)
			} else {
				op.l.activeChanged()
			}
		}
		return false
	case kit.PointerDown:
		if in.Button == kit.ButtonLeft {
			op.down = true
			return true
		}
	case kit.PointerUp:
		if !op.down || in.Button != kit.ButtonLeft {
			return false
		}
		op.down = false
		if o.Disabled || !kit.RectAt(kit.Point{}, op.Size()).Contains(in.Local) {
			return true
		}
		i := op.i
		post(s.Window(), func() {
			s.choose(i)
			s.close()
			if w := s.Window(); w != nil {
				w.Focus(s)
			}
		})
		return true
	}
	return false
}

// FillSemantics is kit.SemanticFiller's.
func (op *selectOpt) FillSemantics(n *kit.SemanticNode) {
	n.States |= kit.NodeSelectable
	if op.chosen() {
		n.States |= kit.NodeSelected
	}
	if op.l.s.Options[op.i].Disabled {
		n.States |= kit.NodeDisabled
	}
	n.Actions |= kit.ActionSelect
}

// PerformAction is kit.ActionHandler's.
func (op *selectOpt) PerformAction(r kit.ActionRequest) error {
	if r.Action != kit.ActionSelect {
		return kit.ErrUnsupported
	}
	s := op.l.s
	if s.Options[op.i].Disabled {
		return kit.ErrNotEnabled
	}
	i := op.i
	post(s.Window(), func() { s.choose(i); s.close() })
	return nil
}

// ListPreview is a Select's open list drawn in place, for previews and screenshots: value is
// the chosen option, active the index of the active one (-1 for none) and viaKeys whether the
// keyboard moved it (its focus ring); minWidth is the trigger's width.
func ListPreview(opts []SelectOption, value string, active int, viaKeys bool, minWidth kit.DIP) kit.Widget {
	s := NewSelect(opts, value, SelectOptions{}, nil)
	s.active, s.viaKeys, s.open = active, viaKeys, true
	l := newSelectList(s, 320, kit.Size{W: 1200, H: 760})
	l.Style.MinWidth = kit.Px(minWidth)
	return l
}
