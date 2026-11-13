package palette

import (
	core "downex/ui/core/palette"
	"downex/ui/kit"
	"downex/ui/kit/icons"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/views"
)

// fontQuery is the field's text: 0.9375rem in the UI font.
var fontQuery = kit.Font{Family: tokens.FontUI, Size: kit.Rem(0.9375), Weight: 400, LineHeight: 1.4, Tabular: true, AutoOptical: true}

// fieldRow is the palette's .field: the search glass (17 px, --accent), the field and the
// scope ("Commands and downloads", var(--type-small) --faint), 46 px high with 14 px sides
// over a 1 px --line, which an --accent line joins while the field has the focus
// (:focus-within: box-shadow inset 0 -1px 0 var(--accent)).
type fieldRow struct {
	kit.Element
	p *Palette
}

func newFieldRow(p *Palette, q *queryField) *fieldRow {
	f := &fieldRow{p: p}
	f.Style = kit.Style{
		Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(10), Flex: kit.FlexNone,
		Height: kit.Px(46), Padding: kit.EdgesInline(kit.Px(14)), Border: kit.Edges{Bottom: kit.Px(1)},
	}
	glass := widgets.NewIcon(icons.Search, 17, func(t *tokens.Theme) kit.Color { return t.Accent })
	scope := views.NewText(core.ListLabel, views.FontHint, views.InkFaint, kit.TextOptions{})
	scope.Style.Flex = kit.FlexNone
	scope.Semantics.Hidden = true
	f.Add(glass, q, scope)
	return f
}

// Paint is kit.Painter's: the line under the row, and the accent line while the field has the
// focus.
func (f *fieldRow) Paint(pc *kit.PaintContext) {
	t := views.TokensOf(&f.Element)
	sz := pc.Size()
	w := pc.Scale.SnapWidth(1)
	line := t.Line
	if t.IsForced() {
		line = t.Ink
	}
	pc.FillRect(kit.R(0, sz.H-w, sz.W, sz.H), line)
	if f.p.query.Focused() {
		pc.FillRect(kit.R(0, sz.H-2*w, sz.W, sz.H-w), t.Accent)
	}
}

// queryField is the palette's search: the system's own text field (kit.TextField) on the
// palette's ground with no border, 0.9375rem --ink text and the placeholder in --faint; a
// combobox for assistive technology, its list the rows.
type queryField struct {
	kit.TextField
	p     *Palette
	scale kit.Scale
}

func newQueryField(p *Palette) *queryField {
	q := &queryField{p: p}
	q.TextField = *kit.NewTextField(kit.TextFieldOptions{})
	q.OnChange = p.queryChanged
	q.Ring = kit.Ring{None: true} // .query:focus-visible { outline: none }
	q.Semantics.Role = kit.RoleComboBox
	q.Semantics.Name = core.SearchLabel
	q.Semantics.Expand = kit.ExpandOpen
	// The native field is opaque (the page's input is transparent): it stops 1 px short of the
	// row's bottom, where the accent line runs, and as much at the top, so its text stays centred.
	q.Style = kit.Style{Flex: kit.FlexOf(1, 1, kit.Length{}), MinWidth: kit.Px(0), AlignSelf: kit.AlignStretch,
		Margin: kit.EdgesBlock(kit.Px(1))}
	return q
}

// applyOptions gives the native field the theme's colours and the font at the window's scale.
func (q *queryField) applyOptions() {
	w := q.Window()
	if w == nil {
		return
	}
	t := views.TokensOf(&q.Element)
	q.SetOptions(kit.TextFieldOptions{
		Style: fontQuery.Style(w.Environment().Scale), Color: t.Ink, Background: t.Raised,
		Placeholder: core.Placeholder, PlaceholderColor: t.Faint, Name: core.SearchLabel,
	})
}

// Attached is kit.Attacher's.
func (q *queryField) Attached() {
	q.TextField.Attached()
	q.applyOptions()
}

// LayoutContent is kit.Layouter's: a new scale gives the field its font again.
func (q *queryField) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	if ctx.Scale != q.scale {
		q.scale = ctx.Scale
		q.applyOptions()
	}
	q.TextField.LayoutContent(ctx, box)
}

// FocusChanged is kit.FocusHandler's: the row's accent line.
func (q *queryField) FocusChanged(f bool) {
	q.TextField.FocusChanged(f)
	q.p.field.Invalidate()
}

// Paint is kit.Painter's: the field's colours follow a theme change.
func (q *queryField) Paint(pc *kit.PaintContext) {
	t := views.TokensOf(&q.Element)
	if o := q.Options(); o.Background != t.Raised || o.Color != t.Ink {
		if w := q.Window(); w != nil {
			_ = w.Post(q.applyOptions)
		}
	}
	q.PaintStandIn(pc) // its text, while its native field is hidden (kit.TextField.SetStandIn)
}

// foot is the palette's .foot: the key hints at the start and the live count at the end,
// 38 px high on --surface-2 under a 1 px --line, var(--type-small) --muted.
type foot struct {
	kit.Element
	p       *Palette
	hints   *kit.Element
	open    *views.Text // "open" or "run"
	folder  *kit.Element
	summary *views.Text
}

func newFoot(p *Palette) *foot {
	f := &foot{p: p}
	f.Style = kit.Style{
		Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(14), Flex: kit.FlexNone,
		MinHeight: kit.Px(38), Padding: kit.EdgesInline(kit.Px(12)), Border: kit.Edges{Top: kit.Px(1)},
	}
	f.hints = &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, Wrap: true, AlignItems: kit.AlignCenter, RowGap: kit.Px(4), ColumnGap: kit.Px(14), MinWidth: kit.Px(0)}}
	hint := func(text string, keys ...string) (*kit.Element, *views.Text) {
		h := &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(4), Flex: kit.FlexNone}}
		for _, k := range keys {
			h.Add(widgets.NewKbd(k))
		}
		l := views.NewText(text, views.FontHint, views.InkMuted, kit.TextOptions{})
		h.Add(l)
		f.hints.Add(h)
		return h, l
	}
	hint(core.HintMove, "↑", "↓")
	_, f.open = hint(core.HintRun, "Enter")
	f.folder, _ = hint(core.HintFolder, "Ctrl", "Enter")
	hint(core.HintClose, "Esc")
	f.summary = views.NewText("", views.FontHint, views.InkFaint, kit.TextOptions{})
	f.summary.Style = kit.Style{Flex: kit.FlexNone, Margin: kit.Edges{Start: kit.Auto}}
	f.summary.Semantics.Role = kit.RoleStatus
	f.summary.Semantics.Live = kit.LivePolite
	f.Add(f.hints, f.summary)
	return f
}

// update shows the hints and the count for the rows.
func (f *foot) update() {
	p := f.p
	dl := p.hasDownloads()
	word := core.HintRun
	if dl {
		word = core.HintOpen
	}
	f.open.SetText(word)
	f.folder.SetHidden(!(dl && p.opts.ShowFolder != nil))
	s := core.Summary(p.entries, len(p.downloads) > 0)
	if s != f.summary.Text() {
		f.summary.SetText(s)
		f.summary.Semantics.Name = s
		f.summary.SemanticsChanged()
	}
	p.query.Semantics.Description = f.hintText()
}

// hintText is the hints as read (aria-describedby).
func (f *foot) hintText() string {
	s := "↑ ↓ " + core.HintMove + " Enter " + f.open.Text()
	if !f.folder.Style.Hidden {
		s += " " + kit.KeyLabel("Ctrl") + " Enter " + core.HintFolder
	}
	return s + " Esc " + core.HintClose
}

// Paint is kit.Painter's.
func (f *foot) Paint(pc *kit.PaintContext) {
	t := views.TokensOf(&f.Element)
	sz := pc.Size()
	pc.FillRect(kit.R(0, 0, sz.W, sz.H), t.Surface2)
	line := t.Line
	if t.IsForced() {
		line = t.Ink
	}
	pc.FillRect(kit.R(0, 0, sz.W, pc.Scale.SnapWidth(1)), line)
}
