package settings

import (
	"downex/ui/core/validate"
	"downex/ui/kit"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/views"
)

// shell is .settings-shell: the side navigation and the pane that scrolls, filling the dialog's
// body from the header to the footer (`.body.flush:has(> .settings-shell)`: one row of the
// fixed height, so that only the pane scrolls). It is a row of tabs above the pane when the
// dialog is narrow (@container dialog (max-width: 44rem)).
type shell struct {
	kit.Element
	d      *Dialog
	nav    *nav
	tabs   []*navTab
	pane   *widgets.ScrollPane
	body   *kit.Element // .pane's padding box: the settings-pane container
	note   *views.Note  // .load-error
	holder *pageBox
	narrow bool
	gutter bool
	// title is "Settings" above the navigation and closeBtn the close button over the pane's
	// top corner: the dialog's header is hidden while the navigation is at the side.
	title    *views.Text
	closeBtn *widgets.IconButton
	// padS and padE are the pane's side padding (inset)
	padS, padE kit.DIP
}

func newShell(d *Dialog) *shell {
	s := &shell{d: d, padS: padStart, padE: padEnd}
	s.Style = kit.Style{MinWidth: kit.Px(0)}
	s.nav = newNav(d)
	for _, p := range pages {
		t := newNavTab(d, p.id, p.label, p.icon)
		s.tabs = append(s.tabs, t)
		s.nav.Add(t)
	}
	s.holder = &pageBox{}
	s.holder.Style = kit.Style{Display: kit.DisplayBlock, MinWidth: kit.Px(0)}
	s.body = el(kit.Style{
		Display: kit.DisplayBlock, Container: true, MinWidth: kit.Px(0),
		Padding: kit.Edges{Top: kit.Px(22), Bottom: kit.Px(28), Start: kit.Px(24), End: kit.Px(20)},
	}, s.holder)
	s.pane = widgets.NewScrollPane(s.body)
	s.pane.Semantics.Role = kit.RoleTabPanel
	for _, t := range s.tabs {
		t.Semantics.Controls = s.pane
	}
	s.title = views.NewLine("Settings", fontNavTitle, views.InkInk)
	s.title.Semantics.Hidden = true // the dialog is named Settings already
	s.closeBtn = widgets.NewIconButton("close", 16, "Close", false, d.Close)
	s.closeBtn.Tooltip = "Close (Esc)"
	s.Add(s.nav, s.pane, s.title, s.closeBtn)
	d.SetHeaderHidden(true)
	return s
}

// tab is a page's tab.
func (s *shell) tab(id validate.SettingsPage) *navTab {
	for _, t := range s.tabs {
		if t.id == id {
			return t
		}
	}
	return s.tabs[0]
}

// setPage shows a page's view in the pane (with the load error above it).
func (s *shell) setPage(v kit.Widget) {
	s.body.RemoveAll()
	if s.d.loadError != "" {
		s.note = views.NewNote(views.NoteFail, "Could not load settings: "+escapeBidi(s.d.loadError))
		s.note.Semantics.Role = kit.RoleAlert
		s.note.Style.Margin = kit.Edges{}
		s.body.Add(el(kit.Style{Display: kit.DisplayBlock, Padding: kit.Edges{Bottom: kit.Px(16)}}, s.note))
	}
	s.holder.RemoveAll()
	if curSkin == skinMac {
		// System Settings shows no page title in the pane (DESIGN-SYSTEM §3.20): a band
		// keeps the first group clear of the close button over the pane's corner.
		s.holder.Add(el(kit.Style{Display: kit.DisplayBlock, Height: kit.Px(24)}))
	} else {
		s.holder.Add(pageTitle(s.tab(s.d.page).text))
	}
	s.holder.Add(v)
	s.body.Add(s.holder)
	s.holder.fadeIn()
	busy := kit.NodeState(0)
	if s.d.loading {
		busy = kit.NodeBusy
	}
	s.pane.Semantics.States = busy
	s.pane.Semantics.Name = s.tab(s.d.page).label.Text()
	s.pane.SemanticsChanged()
}

// scrollTop scrolls the pane to its top (a new page starts there).
func (s *shell) scrollTop() { s.pane.ScrollTo(0) }

// syncTabs shows the chosen tab and the pages that need attention.
func (s *shell) syncTabs() {
	errs := s.d.errors()
	for _, t := range s.tabs {
		t.set(t.id == s.d.page, s.d.pageHasErrors(errs, t.id))
	}
	s.pane.Semantics.Name = s.tab(s.d.page).label.Text()
	s.pane.SemanticsChanged()
}

// narrowAt reports a narrow dialog: its content box 44rem wide or less.
func narrowAt(sc kit.Scale, w kit.DIP) bool { return w <= sc.Rem(44) }

// fillHeight is the body's height: the dialog's fixed height (46rem, at most the room the window
// has) less its header, footer and border, so that the dialog keeps its size on every page.
func (s *shell) fillHeight(ctx *kit.LayoutContext, w kit.DIP) kit.DIP {
	win := s.Window()
	card := s.d.Card()
	if win == nil || card == nil {
		return 0
	}
	size := win.Size()
	top := s.d.app.ModalTop()
	gap := kit.DIP(16)
	if size.H <= 640 {
		gap = 8
	}
	room := max(size.H-top-2*gap, 0)
	least := min(ctx.Scale.Rem(s.d.skin.heightRem()), room)
	kids := card.Children()
	var other kit.DIP
	for _, k := range kids {
		if k == s.d.Body.Parent() || k.Contains(&s.Element) || k.Style.Hidden {
			continue // the shell's own part, or the header hidden while the navigation is at the side
		}
		other += ctx.HeightFor(k.Widget(), w, w)
	}
	// The dialog keeps one height on every page, as Windows' and macOS's settings do: a long
	// page scrolls in the pane.
	line := kit.DIP(0)
	if s.narrow {
		line = 1 // the shell's top line under the header
	}
	return max(least-2-other-line, 0) // less the card's border
}

// ContentWidths is kit.Layouter's: the dialog's width decides (min-inline-size: 0).
func (s *shell) ContentWidths(ctx *kit.LayoutContext) (lo, hi kit.DIP) { return 0, 0 }

// ContentHeight is kit.Layouter's.
func (s *shell) ContentHeight(ctx *kit.LayoutContext, w kit.DIP) kit.DIP { return s.fillHeight(ctx, w) }

// LayoutContent is kit.Layouter's.
func (s *shell) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	w := box.Width()
	narrow := narrowAt(ctx.Scale, w+horizontalBorder(s))
	if narrow != s.narrow {
		s.narrow = narrow
		// narrow, the tabs are a row under the dialog's own header, as before
		s.d.SetHeaderHidden(!narrow)
		s.title.SetHidden(narrow)
		s.closeBtn.Style.Hidden = narrow
		s.Style.Border.Top = kit.Length{}
		if narrow {
			s.Style.Border.Top = kit.Px(1)
		}
		s.nav.setNarrow(narrow)
		for _, t := range s.tabs {
			t.setNarrow(narrow)
		}
	}
	var navR, paneR kit.Rect
	if narrow {
		nh := ctx.HeightFor(s.nav, w, w)
		navR = kit.R(box.Min.X, box.Min.Y, box.Max.X, box.Min.Y+nh)
		paneR = kit.R(box.Min.X, box.Min.Y+nh, box.Max.X, box.Max.Y)
	} else {
		nw := s.d.skin.navWidth(ctx.Scale)
		navR = kit.R(box.Min.X, box.Min.Y, box.Min.X+nw, box.Max.Y)
		paneR = kit.R(box.Min.X+nw, box.Min.Y, box.Max.X, box.Max.Y)
		if ctx.RTL {
			navR = kit.R(box.Max.X-nw, box.Min.Y, box.Max.X, box.Max.Y)
			paneR = kit.R(box.Min.X, box.Min.Y, box.Max.X-nw, box.Max.Y)
		}
		// "Settings" at the top of the navigation's column, the tabs under it
		const titleTop, titleGap = 20, 10
		th := ctx.HeightFor(s.title, nw-36, nw-36)
		ctx.Place(s.title, kit.R(navR.Min.X+18, navR.Min.Y+titleTop, navR.Max.X-18, navR.Min.Y+titleTop+th), nw-36)
		navR.Min.Y += titleTop + th + titleGap
		// the close button over the pane's top corner, clear of its scroll bar
		bw, _ := ctx.IntrinsicWidths(s.closeBtn)
		bh := ctx.HeightFor(s.closeBtn, bw, bw)
		bar := s.pane.Scrollbar().Metrics.Thickness
		x := paneR.Max.X - bar - 12 - bw
		if ctx.RTL {
			x = paneR.Min.X + 12
		}
		ctx.Place(s.closeBtn, kit.R(x, box.Min.Y+14, x+bw, box.Min.Y+14+bh), bw)
	}
	ctx.Place(s.nav, navR, w)
	// scrollbar-gutter: stable: the bar's room is kept whether the page overflows or not.
	t := s.pane.Scrollbar().Metrics.Thickness
	pw := paneR.Width()
	s.inset(max(pw-t, 0))
	s.d.scale = ctx.Scale
	s.d.paneW = s.contentW(max(pw-t, 0)) // .pane's content box: less the gutter and the padding
	overflow := ctx.HeightFor(s.body, max(pw-t, 0), max(pw-t, 0)) > paneR.Height()+0.5
	pad := kit.Px(t)
	if overflow {
		pad = kit.Length{}
	}
	if s.pane.Style.Padding.End != pad {
		s.pane.Style.Padding.End = pad
		s.pane.Relayout()
	}
	ctx.Place(s.pane, paneR, w)
}

func horizontalBorder(*shell) kit.DIP { return 0 }

// The pane's padding at its sides: Windows centres a page at most fluentMaxWidth wide.
const (
	padStart kit.DIP = 24
	padEnd   kit.DIP = 20
)

// inset sets the pane's side padding for a pane w wide (less its scroll bar's room).
func (s *shell) inset(w kit.DIP) {
	st, en := padStart, padEnd
	if s.d.skin == skinFluent && !s.narrow {
		st, en = 32, 32
		if extra := w - st - en - fluentMaxWidth; extra > 0 {
			st += extra / 2
			en += extra / 2
		}
	}
	if s.padS != st || s.padE != en {
		s.padS, s.padE = st, en
		s.body.Style.Padding.Start, s.body.Style.Padding.End = kit.Px(st), kit.Px(en)
		s.body.Relayout()
	}
}

// contentW is the pages' width in a pane w wide (less its scroll bar's room), after inset.
func (s *shell) contentW(w kit.DIP) kit.DIP {
	return max(w-s.padS-s.padE, 0)
}

// Paint is kit.Painter's: the --line above the shell.
func (s *shell) Paint(p *kit.PaintContext) {
	t := views.TokensOf(&s.Element)
	if s.narrow {
		p.FillRect(kit.R(0, 0, p.Size().W, p.Scale.SnapWidth(1)), t.Line)
		return
	}
	// the navigation's column, from the dialog's top to its footer: --line-soft at its end
	nb := s.nav.Bounds()
	w := p.Scale.SnapWidth(1)
	x := nb.Max.X - w
	if win := s.Window(); win != nil && win.Direction() == kit.DirRTL {
		x = nb.Min.X
	}
	p.FillRect(kit.R(x, 0, x+w, p.Size().H), t.LineSoft)
}

// nav is .nav: a vertical tablist on --surface-2 with a --line at its end (a row of tabs on a
// --line hairline when narrow) that the arrows, Home and End move through.
type nav struct {
	kit.Element
	d      *Dialog
	narrow bool
}

func newNav(d *Dialog) *nav {
	n := &nav{d: d}
	n.Semantics.Role = kit.RoleTabList
	n.Semantics.Name = "Settings pages"
	n.Semantics.Orientation = kit.OrientationVertical
	n.setNarrow(false)
	return n
}

func (n *nav) setNarrow(narrow bool) {
	n.narrow = narrow
	if narrow {
		n.Style = kit.Style{Display: kit.DisplayFlex, Axis: kit.AxisRow, ColumnGap: kit.Px(2), MinWidth: kit.Px(0),
			Padding: kit.EdgesInline(tokens.Sp6), Overflow: kit.OverflowHidden}
	} else {
		pad := kit.EdgesOf(kit.Px(10), kit.Px(8))
		if n.d.skin == skinFluent {
			pad = kit.EdgesOf(kit.Px(14), kit.Px(10))
		}
		n.Style = kit.Style{Display: kit.DisplayFlex, Axis: kit.AxisColumn, RowGap: kit.Px(2), MinHeight: kit.Px(0),
			Padding: pad, Border: kit.Edges{End: kit.Px(1)}, Overflow: kit.OverflowHidden}
		n.Style.Padding.Top = kit.Px(0) // "Settings" is above it
	}
	n.Relayout()
	n.Invalidate()
}

// Paint is kit.Painter's.
func (n *nav) Paint(p *kit.PaintContext) {
	t := views.TokensOf(&n.Element)
	sz := p.Size()
	line := t.Line
	if n.narrow {
		p.FillRect(kit.R(0, sz.H-p.Scale.SnapWidth(1), sz.W, sz.H), line)
		return
	}
	// at the side, the navigation is on the dialog's own ground; the shell draws the line
	// at its end, from the dialog's top
}

// HandleKey is kit.KeyHandler's: Up/Down (and Left/Right), Home and End move between the pages.
func (n *nav) HandleKey(e kit.KeyEvent) bool {
	if e.Action != kit.KeyDown || e.Mods&(kit.ModCtrl|kit.ModAlt|kit.ModMeta) != 0 {
		return false
	}
	i := 0
	for j, p := range pages {
		if p.id == n.d.page {
			i = j
		}
	}
	next := -1
	switch e.Key {
	case kit.KeyArrowDown, kit.KeyArrowRight:
		next = (i + 1) % len(pages)
	case kit.KeyArrowUp, kit.KeyArrowLeft:
		next = (i - 1 + len(pages)) % len(pages)
	case kit.KeyHome:
		next = 0
	case kit.KeyEnd:
		next = len(pages) - 1
	}
	if next < 0 {
		return false
	}
	n.d.showPage(pages[next].id, true)
	return true
}

// navTab is a page's .tab: its icon and label (and the "!" mark of a page that needs
// attention), 32 px high with 7 px corners; the chosen one on --sel with a --sel-line edge,
// in --ink 600 with an --accent icon. Narrow, it is a 36 px label with a 2 px --accent bar
// under the chosen one.
type navTab struct {
	kit.Pressable
	d       *Dialog
	id      validate.SettingsPage
	icon    *widgets.Icon
	label   *kit.Label
	mark    *tabMark
	text    string
	on      bool
	invalid bool
	narrow  bool
}

var (
	fontTab   = uiFont(tokens.TypeBody, 500, 1.4)
	fontTabOn = uiFont(tokens.TypeBody, 600, 1.4)
)

func newNavTab(d *Dialog, id validate.SettingsPage, label, iconName string) *navTab {
	t := &navTab{d: d, id: id, text: label}
	t.OnPress = func() { d.showPage(id, false) }
	t.Semantics.Role = kit.RoleTab
	t.Semantics.AutomationID = "settings-tab-" + string(id)
	t.Cursor = kit.CursorPointer
	t.Ring = kit.Ring{Offset: kit.Px(-2)}
	if curSkin == skinMac {
		// the chosen item is filled with Mint, the ring's colour: the ring goes outside
		t.Ring = kit.Ring{Offset: kit.Px(2)}
	}
	t.icon = icon(iconName, 16, nil)
	t.icon.Style.Flex = kit.FlexNone
	t.label = kit.NewLabel(label, fontTab, kit.TextOptions{Truncate: kit.TruncateEnd})
	t.label.Semantics.Role = kit.RoleNone
	t.label.Style.MinWidth = kit.Px(0)
	t.label.Style.Flex = kit.FlexAuto
	t.mark = newTabMark()
	t.mark.Style.Hidden = true
	t.Add(t.icon, t.label, t.mark)
	t.icon.SetColor(func(th *tokens.Theme) kit.Color { return t.iconColor(th) })
	t.setNarrow(false)
	t.set(id == d.page, false)
	return t
}

func (t *navTab) setNarrow(narrow bool) {
	t.narrow = narrow
	if narrow {
		t.Style = kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignStretch, ColumnGap: kit.Px(10), Flex: kit.FlexNone,
			Height: kit.Px(36), Padding: kit.EdgesInline(kit.Px(10)),
			Radius: kit.Corners{TopStart: tokens.RSm, TopEnd: tokens.RSm}}
		t.icon.Style.Hidden = true
		t.label.Style.AlignSelf = kit.AlignCenter
		t.mark.Style.AlignSelf = kit.AlignCenter
	} else {
		h, gap, start := kit.Px(32), kit.Px(10), kit.Px(10)
		switch t.d.skin {
		case skinFluent:
			h, gap, start = kit.Px(36), kit.Px(12), kit.Px(14)
		case skinMac:
			h, gap, start = kit.Px(30), kit.Px(12), kit.Px(11) // the tile reaches 3 px past the icon
		}
		t.Style = kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: gap, Flex: kit.FlexNone,
			Height: h, Padding: kit.Edges{Start: start, End: kit.Px(8)}, Radius: kit.CornersAll(kit.Px(7))}
		t.icon.Style.Hidden = false
		t.label.Style.AlignSelf = kit.AlignAuto
		t.mark.Style.AlignSelf = kit.AlignAuto
	}
	t.Relayout()
	t.Invalidate()
}

// set shows whether the tab is the chosen one and whether its page needs attention.
func (t *navTab) set(on, invalid bool) {
	if t.on == on && t.invalid == invalid && t.Semantics.Name != "" {
		return
	}
	t.on, t.invalid = on, invalid
	f := fontTab
	if on {
		f = fontTabOn
		t.Focus = kit.FocusTab
		t.Semantics.States |= kit.NodeSelected
	} else {
		t.Focus = kit.FocusClick
		t.Semantics.States &^= kit.NodeSelected
	}
	t.label.SetFont(f)
	t.mark.SetHidden(!invalid)
	name := t.text
	if invalid {
		name += " (needs attention)"
	}
	t.Semantics.Name = name
	t.SemanticsChanged()
	t.Invalidate()
}

func (t *navTab) iconColor(th *tokens.Theme) kit.Color {
	if t.d.skin == skinMac && !t.narrow && !th.IsForced() {
		return kit.RGB(0xffffff) // on its tile
	}
	switch {
	case t.invalid:
		return th.Danger
	case t.on:
		return th.Accent
	}
	return th.Muted
}

// HoverChanged is kit.HoverHandler's.
func (t *navTab) HoverChanged(h bool) {
	t.Pressable.HoverChanged(h)
	t.Invalidate()
}

// Paint is kit.Painter's.
func (t *navTab) Paint(p *kit.PaintContext) {
	th := views.TokensOf(t.Base())
	sz := p.Size()
	hover := t.Hovered() && !t.on
	r := kit.RoundRect(kit.RectAt(kit.Point{}, sz), 7)
	if t.narrow {
		r = kit.RRect{Rect: kit.RectAt(kit.Point{}, sz), Radii: kit.Radii{TopLeft: 6, TopRight: 6}}
	}
	look := t.d.skin
	if t.narrow || th.IsForced() {
		look = skinPlain // the row of tabs, and forced colours
	}
	ink := th.Ink2
	switch {
	case hover || t.on && !t.invalid:
		ink = th.Ink
	case t.invalid:
		ink = th.Danger
	}
	if hover {
		ink = th.Ink
	}
	switch look {
	case skinFluent:
		// Windows 11: the chosen page on --hover with a 3 × 16 px --accent pill at its start
		r = kit.RoundRect(kit.RectAt(kit.Point{}, sz), 6)
		switch {
		case t.on:
			p.FillRoundedRect(r, th.Hover)
			x := kit.DIP(0)
			if w := t.Window(); w != nil && w.Direction() == kit.DirRTL {
				x = sz.W - 3
			}
			pill := kit.R(x, (sz.H-16)/2, x+3, (sz.H+16)/2)
			p.FillRoundedRect(kit.RoundRect(pill, 1.5), th.Accent)
		case hover:
			p.FillRoundedRect(r, th.Hover)
		}
	case skinMac:
		// macOS: the chosen page filled with --accent; every icon on a coloured tile
		r = kit.RoundRect(kit.RectAt(kit.Point{}, sz), 6)
		switch {
		case t.on:
			p.FillRoundedRect(r, th.Accent)
			ink = th.AccentOn // the red tile and the "!" mark say it needs attention
		case hover:
			p.FillRoundedRect(r, th.Hover)
		}
		ib := t.icon.Bounds()
		tile := kit.R(ib.Min.X-3, ib.Min.Y-3, ib.Max.X+3, ib.Max.Y+3)
		col := macTileColors[0]
		for i, pg := range pages {
			if pg.id == t.id && i < len(macTileColors) {
				col = macTileColors[i]
			}
		}
		if t.invalid {
			col = th.Danger
		}
		p.FillRoundedRect(kit.RoundRect(tile, 6), col)
	default:
		switch {
		case t.on && !t.narrow:
			p.FillRoundedRect(r, th.Sel)
			p.Border(r, 1, th.SelLine)
		case hover:
			p.FillRoundedRect(r, th.Hover)
		}
	}
	t.label.SetColor(ink)
	if t.on && t.narrow {
		lb := t.label.Bounds()
		bar := kit.R(lb.Min.X, sz.H-2, lb.Max.X, sz.H)
		p.FillRoundedRect(kit.RRect{Rect: bar, Radii: kit.Radii{TopLeft: 2, TopRight: 2}}, th.Accent)
	}
}

// tabMark is .mark: a 16 px --danger circle with a "!" (0.6875rem 650) in --tone-on. It is
// hidden from assistive technology (the tab says "(needs attention)").
type tabMark struct {
	kit.Element
	bang *kit.Label
}

func newTabMark() *tabMark {
	m := &tabMark{}
	m.Semantics.Hidden = true
	m.Style = kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, Justify: kit.JustifyCenter, Flex: kit.FlexNone,
		Width: kit.Px(16), Height: kit.Px(16)}
	m.bang = kit.NewLabel("!", uiFont(kit.Rem(0.6875), 650, 1), kit.TextOptions{})
	m.Add(m.bang)
	return m
}

// Paint is kit.Painter's.
func (m *tabMark) Paint(p *kit.PaintContext) {
	t := views.TokensOf(&m.Element)
	sz := p.Size()
	p.FillRoundedRect(kit.RoundRect(kit.RectAt(kit.Point{}, sz), sz.W/2), t.Danger)
	m.bang.SetColor(t.ToneOn)
}

// pageBox holds the page shown and fades it in when it is mounted (page-in: opacity from 0,
// var(--dur-3) var(--ease-out); none under reduced motion).
type pageBox struct {
	kit.Element
	alpha  float32
	pushed bool
	tween  *kit.Tween
}

func (b *pageBox) fadeIn() {
	w := b.Window()
	b.alpha = 1
	if w == nil || !w.MotionOK() {
		return
	}
	if b.tween != nil {
		w.StopAnimation(b.tween)
	}
	b.alpha = 0
	b.tween = &kit.Tween{From: 0, To: 1, Duration: tokens.Dur3, Ease: tokens.EaseOut.Easing(), Set: func(v float32) {
		b.alpha = v
		b.Invalidate()
	}}
	w.Animate(b.tween)
}

// Paint is kit.Painter's.
func (b *pageBox) Paint(p *kit.PaintContext) {
	b.pushed = b.alpha < 1
	if b.pushed {
		p.Canvas.PushOpacity(b.alpha)
	}
}

// PaintOver is kit.OverPainter's.
func (b *pageBox) PaintOver(p *kit.PaintContext) {
	if b.pushed {
		p.Canvas.Pop()
		b.pushed = false
	}
}
