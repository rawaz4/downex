package downloads

import (
	"downex/ui/kit"
	"downex/ui/kit/icons"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/views"
)

// The empty list's texts (ListEmpty.svelte).
const (
	emptyLoading     = "Loading downloads…"
	emptyUnreachable = "Your downloads appear here once Downex reaches downexd."
	emptyNoneTitle   = "No downloads yet"
	emptyNoneText    = "Paste a link anywhere in this window (Ctrl+V), drop one here, or add one."
	emptyAdd         = "Add download"
	emptyAddVideo    = "Add video or stream"
	emptyExtension   = "Downloads from Chrome, Edge and Firefox arrive here with the Downex extension."
	emptyGetExt      = "Get the browser extension"
	emptyFilterTitle = "No downloads match the current filters"
	emptyFilterText  = "The filters hide every download."
	emptyClear       = "Clear filters"
)

var (
	// fontEmptyTitle is .title: 650 var(--type-section) / 1.25 var(--font-display), -0.01em.
	fontEmptyTitle = kit.Font{Family: tokens.FontDisplay, Size: tokens.TypeSection, Weight: 650, LineHeight: 1.25, LetterSpacing: -0.01, Tabular: true, AutoOptical: true}
	// fontEmptyText is .text and .quiet: var(--type-body) / 1.45.
	fontEmptyText = uiFont(tokens.TypeBody, 400, 1.45)
	// fontEmptyExt is .extension: var(--type-small) / 1.5.
	fontEmptyExt = uiFont(tokens.TypeSmall, 400, 1.5)
)

// buildEmpty is ListEmpty's .empty box for a kind: a centred column 420 px wide at most, 10 px
// between its parts, 24 × 16 px of padding.
func buildEmpty(dl *Downloads, kind EmptyKind, title string, stale bool, addRef func(*buttonRef)) *kit.Element {
	box := &kit.Element{Style: kit.Style{
		Display: kit.DisplayFlex, Axis: kit.AxisColumn, AlignItems: kit.AlignCenter, RowGap: kit.Px(10),
		MaxWidth: kit.Px(420), Margin: kit.EdgesInline(kit.Length{}), Padding: kit.EdgesOf(tokens.Sp9, tokens.Sp7),
		AlignSelf: kit.AlignCenter, Width: kit.Pct(100),
	}}
	box.PassThrough = true
	para := func(s string, f kit.Font, ink views.Ink) *views.Text {
		t := views.NewText(s, f, ink, kit.TextOptions{Wrap: kit.TextWrapWords, Align: kit.TextAlignCenter})
		t.Style.MaxWidth = kit.Pct(100)
		t.Style.AlignSelf = kit.AlignStretch
		return t
	}
	heading := func(s string) *views.Text {
		t := para(s, fontEmptyTitle, views.InkInk)
		t.Semantics.Role = kit.RoleHeading
		t.Semantics.Level = 2
		return t
	}
	buttons := func(bs ...kit.Widget) *kit.Element {
		e := &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, Wrap: true, Justify: kit.JustifyCenter, ColumnGap: kit.Px(8), RowGap: kit.Px(8), Margin: kit.Edges{Top: kit.Px(6)}}}
		e.Add(bs...)
		return e
	}
	switch kind {
	case EmptyLoading:
		box.Add(para(emptyLoading, fontEmptyText, views.InkMuted))
	case EmptyUnreachable:
		box.Add(para(emptyUnreachable, fontEmptyText, views.InkMuted))
	case EmptyNone:
		box.Add(newGlyph(false))
		box.Add(heading(emptyNoneTitle))
		box.Add(para(kit.KeyLabel(emptyNoneText), fontEmptyText, views.InkMuted))
		if dl.app.Open.Add != nil {
			add := widgets.NewButton(emptyAdd, "add", widgets.Primary, widgets.SizeMd, func() { dl.openAdd(false) })
			video := widgets.NewButton(emptyAddVideo, "play", widgets.Secondary, widgets.SizeMd, func() { dl.openAdd(true) })
			video.SetIconSize(14)
			add.SetDisabled(stale, false)
			video.SetDisabled(stale, false)
			addRef(&buttonRef{set: func(d bool) {
				add.SetDisabled(d, false)
				video.SetDisabled(d, false)
			}})
			box.Add(buttons(add, video))
		}
		ext := &extension{}
		ext.Style = kit.Style{
			Display: kit.DisplayBlock, Margin: kit.Edges{Top: kit.Px(10)}, Padding: kit.Edges{Top: kit.Px(14)},
			Border: kit.Edges{Top: kit.Px(1)}, AlignSelf: kit.AlignStretch,
		}
		ext.Add(para(emptyExtension, fontEmptyExt, views.InkFaint))
		if dl.app.Open.Settings != nil {
			link := widgets.NewButton(emptyGetExt, "", widgets.Link, widgets.SizeMd, func() { dl.openExtension() })
			link.Label().SetFont(kit.Font{Family: tokens.FontUI, Size: tokens.TypeSmall, Weight: 600, LineHeight: 1, Tabular: true, AutoOptical: true})
			line := &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, Justify: kit.JustifyCenter}}
			line.Add(link)
			ext.Add(line)
		}
		box.Add(ext)
	default:
		box.Add(newGlyph(true))
		t := title
		if t == "" {
			t = emptyFilterTitle
		}
		box.Add(heading(t))
		box.Add(para(emptyFilterText, fontEmptyText, views.InkMuted))
		if dl.app.Act.ClearFilters != nil {
			box.Add(buttons(widgets.NewButton(emptyClear, "", widgets.Secondary, widgets.SizeMd, func() { dl.clearFilters() })))
		}
	}
	return box
}

// glyph is the empty list's round mark: the tray in --accent-text on --accent-soft (72 px), or
// the filter in --muted on --surface-2 with a --line border (56 px).
type glyph struct {
	kit.Element
	quiet bool
}

func newGlyph(quiet bool) *glyph {
	g := &glyph{quiet: quiet}
	g.Semantics.Hidden = true
	size, icon, isz := kit.DIP(72), icons.Tray, kit.DIP(40)
	color := func(t *tokens.Theme) kit.Color { return t.AccentText }
	if quiet {
		size, icon, isz = 56, icons.Filter, 26
		color = func(t *tokens.Theme) kit.Color { return t.Muted }
	}
	g.Style = kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, Justify: kit.JustifyCenter,
		Width: kit.Px(size), Height: kit.Px(size), Flex: kit.FlexNone, Margin: kit.Edges{Bottom: kit.Px(6)}}
	g.Add(widgets.NewIcon(icon, isz, color))
	return g
}

// Paint is kit.Painter's.
func (g *glyph) Paint(p *kit.PaintContext) {
	t := tok(&g.Element)
	r := p.Scale.SnapRect(kit.RectAt(p.Origin, p.Size()))
	if !g.quiet {
		p.Canvas.FillEllipse(r, t.AccentSoft)
		return
	}
	p.Canvas.FillEllipse(r, t.Line)
	w := p.Scale.SnapWidth(1)
	p.Canvas.FillEllipse(r.Inset(kit.UniformInsets(w)), t.Surface2)
}

// extension is .extension: the paragraph under a 1 px --line-soft rule.
type extension struct{ kit.Element }

// Paint is kit.Painter's.
func (e *extension) Paint(p *kit.PaintContext) {
	t := tok(&e.Element)
	p.FillRect(kit.R(0, 0, p.Size().W, 1), t.LineSoft)
}
