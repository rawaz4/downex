package settings

import (
	"downex/ui/host"
	"downex/ui/kit"
	"downex/ui/kit/icons"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/state"
	"downex/ui/views"
)

var themeHints = map[host.ThemeChoice]string{
	host.ThemeSystem: "Follows the light or dark setting of your system.",
	host.ThemeLight:  "Always light.",
	host.ThemeDark:   "Always dark.",
}

var densityHints = map[state.DensityChoice]string{
	state.Comfortable: "Two lines per download: name, then site and category.",
	state.Compact:     "One line per download, like a classic download manager.",
}

// The preview's sample rows: what each density shows of a download.
var samples = []struct {
	kind            icons.TileKind
	name, sub, size string
}{
	{icons.TileVideo, "conference-keynote.mp4", "youtube.com · Video", "3.4 GB"},
	{icons.TileArchives, "photo-archive-277.7z", "mirror.example.org · Archives", "2.5 GB"},
}

// appearancePage is Settings → Appearance: the theme and the list density, this viewer's
// choices that their stores keep on this computer and apply at once (they are not part of the
// settings Save sends to downexd).
type appearancePage struct {
	page
	themeHint, densityHint *views.Text
	themeCtl, densityCtl   *widgets.SegmentedControl
	preview                *surface
	rows                   []*sampleRow
	stops                  []func()
}

type sampleRow struct {
	box       *surface
	tile      *tile
	name, sub *views.Text
}

var (
	fontSampleName        = uiFont(tokens.TypeBody, 500, 1.3)
	fontSampleNameCompact = uiFont(tokens.TypeBody, 400, 1.3)
	fontSampleSub         = uiFont(tokens.TypeMeta, 400, 1.3)
)

func (p *appearancePage) theme() host.ThemeChoice {
	if t := p.d.opts.Theme; t != nil {
		return t.Choice()
	}
	return host.ThemeSystem
}

func (p *appearancePage) density() state.DensityChoice {
	if t := p.d.opts.Density; t != nil {
		return t.Choice()
	}
	return state.Comfortable
}

func newAppearancePage(d *Dialog) *appearancePage {
	p := &appearancePage{}
	p.init(d)
	p.themeHint = sHint("")
	p.densityHint = sHint("")
	p.themeCtl = widgets.NewSegmentedControl([]widgets.SegmentOption{
		{Value: string(host.ThemeSystem), Label: "System", Icon: "system"},
		{Value: string(host.ThemeLight), Label: "Light", Icon: "sun"},
		{Value: string(host.ThemeDark), Label: "Dark", Icon: "moon"},
	}, string(p.theme()), widgets.SegmentedOptions{Legend: "Theme"}, func(v string) {
		if t := d.opts.Theme; t != nil {
			t.Set(host.ThemeChoice(v))
		}
		p.sync()
	})
	p.densityCtl = widgets.NewSegmentedControl([]widgets.SegmentOption{
		{Value: string(state.Comfortable), Label: "Comfortable"},
		{Value: string(state.Compact), Label: "Compact"},
	}, string(p.density()), widgets.SegmentedOptions{Legend: "List density"}, func(v string) {
		if t := d.opts.Density; t != nil {
			t.Set(state.DensityChoice(v))
		}
		p.sync()
	})
	p.preview = newSurface(kit.Style{Display: kit.DisplayBlock, Border: kit.EdgesAll(kit.Px(1)), Overflow: kit.OverflowHidden,
		Radius: kit.CornersAll(tokens.RMd), MinWidth: kit.Px(0)}, inkSurface, inkLine, 8)
	p.preview.Semantics.Hidden = true
	for i, s := range samples {
		r := &sampleRow{tile: newTile(s.kind, 26)}
		r.name = views.NewLine(s.name, fontSampleName, views.InkInk)
		r.sub = views.NewLine(s.sub, fontSampleSub, views.InkFaint)
		words := el(kit.Style{Display: kit.DisplayBlock, Flex: kit.FlexAuto, MinWidth: kit.Px(0)}, r.name, r.sub)
		size := para(s.size, fontBody, views.InkFaint)
		size.Style.Flex = kit.FlexNone
		r.box = newSurface(kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(11), Height: kit.Px(46),
			Padding: kit.EdgesInline(kit.Px(12)), MinWidth: kit.Px(0)}, nil, inkLineSoft, 0, r.tile, words, size)
		r.box.rule = true
		if i > 0 {
			r.box.ruled = true
			r.box.Style.Border.Top = kit.Px(1)
		}
		p.rows = append(p.rows, r)
		p.preview.Add(r.box)
	}
	themeRow := sRow(sText("Theme", p.themeHint), sControl(p.themeCtl))
	densityRow := sRow(sText("List density", p.densityHint), sControl(p.densityCtl))
	// margin-block: 6px 4px (a grid item's margins: the wrapper's padding here)
	densityRow.addAt(el(kit.Style{Display: kit.DisplayBlock, MinWidth: kit.Px(0), Padding: kit.Edges{Top: kit.Px(6), Bottom: kit.Px(4)}}, p.preview),
		place{col: -1, span: -1, row: -1, rowSpan: 1})
	note := el(kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(7), MinWidth: kit.Px(0)},
		icon("check", 14, func(t *tokens.Theme) kit.Color { return t.Accent }), para("Saved on this computer right away.", fontCell, views.InkMuted))
	p.Add(sSection(sHead("Appearance"), sCard(themeRow, densityRow)), note)
	if d.app.Theme != nil {
		p.stops = append(p.stops, d.app.Watch(&d.app.Theme.Changes, p.sync))
	}
	if d.app.Density != nil {
		p.stops = append(p.stops, d.app.Watch(&d.app.Density.Changes, p.sync))
	}
	p.sync()
	return p
}

func (p *appearancePage) dispose() {
	for _, s := range p.stops {
		s()
	}
}

func (p *appearancePage) sync() {
	th, de := p.theme(), p.density()
	p.themeHint.SetText(themeHints[th])
	p.densityHint.SetText(densityHints[de])
	p.themeCtl.SetValue(string(th))
	p.densityCtl.SetValue(string(de))
	compact := de == state.Compact
	for _, r := range p.rows {
		size, h, gap := kit.DIP(26), kit.Px(46), kit.Px(11)
		f := fontSampleName
		if compact {
			size, h, gap, f = 16, kit.Px(24), kit.Px(8), fontSampleNameCompact
		}
		r.tile.set(r.tile.kind, size)
		r.name.SetFont(f)
		if r.box.Style.Height != h {
			r.box.Style.Height, r.box.Style.ColumnGap = h, gap
			r.box.Relayout()
		}
		r.sub.SetHidden(compact)
	}
}
