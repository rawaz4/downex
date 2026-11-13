package palette

import (
	"math"

	"google.golang.org/protobuf/types/known/timestamppb"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/display"
	"downex/ui/core/format"
	core "downex/ui/core/palette"
	"downex/ui/core/segments"
	"downex/ui/kit"
	"downex/ui/kit/icons"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/views"
	"downex/ui/views/dialogs/textrun"
)

// The rows' fonts: .label (0.84375rem, 500; <mark> 650) and the small print (var(--type-small)).
var (
	fontLabel = kit.Font{Family: tokens.FontUI, Size: kit.Rem(0.84375), Weight: 500, LineHeight: 1.4, Tabular: true, AutoOptical: true}
	fontMark  = kit.Font{Family: tokens.FontUI, Size: kit.Rem(0.84375), Weight: 650, LineHeight: 1.4, Tabular: true, AutoOptical: true}
	fontSmall = views.FontHint
	fontPct   = kit.Font{Family: tokens.FontUI, Size: tokens.TypeSmall, Weight: 600, LineHeight: 1.4, Tabular: true, AutoOptical: true}
	fontHit   = kit.Font{Family: tokens.FontUI, Size: tokens.TypeSmall, Weight: 650, LineHeight: 1.4, Tabular: true, AutoOptical: true}
)

// row is an option of the list (.option): a command (glyph, label, description, why it is
// unavailable, keys) or a download (file tile, name, badge, and its progress while it runs,
// else its state, date and size and its server). The current one has the --sel ground, its
// --sel-line edge and the 2 px --focus ring; a pointer over a row makes it current, a click
// runs it (Ctrl+click: a download's folder).
type row struct {
	kit.Element
	p        *Palette
	i        int
	download bool
	current  bool
	disabled bool
	pressed  bool
	ground   float32 // the --sel ground's share (background-color transition)
	tween    *kit.Tween

	glyph *kit.Element
	icon  *widgets.Icon
	tile  *fileTile
	label *textrun.Line
	desc  *views.Text
	why   *views.Text
	keys  *kit.Element
	key   string // the shortcut the keys show

	tag  *views.Tag
	pct  *views.Text
	bar  *widgets.SegmentBar
	meta *views.Text
	host *textrun.Line
	cell [1]widgets.SegCell
}

func newRow(p *Palette, i int) *row {
	e := &p.entries[i]
	r := &row{p: p, i: i, download: e.Type == core.EntryDownload}
	r.Semantics.Role = kit.RoleOption
	r.Cursor = kit.CursorPointer
	r.Style = kit.Style{
		Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(10), MinHeight: kit.Px(38),
		Padding: kit.EdgesInline(kit.Px(9)), Radius: kit.CornersAll(tokens.RMd),
	}
	r.glyph = &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, Justify: kit.JustifyCenter, Flex: kit.FlexNone, Width: kit.Px(20)}}
	r.glyph.Semantics.Hidden = true
	r.Add(r.glyph)
	plainInk := func(t *tokens.Theme) kit.Color {
		if r.disabled {
			return t.Faint
		}
		return t.Ink
	}
	markInk := func(t *tokens.Theme) kit.Color {
		if r.disabled {
			return t.Faint // an unavailable command's matched letters do not draw the eye
		}
		return t.AccentText
	}
	r.label = textrun.New(nil, fontLabel, fontMark, plainInk, markInk)
	r.label.Semantics.Role = kit.RoleNone
	small := func(t *tokens.Theme) kit.Color {
		if r.current {
			return t.Muted
		}
		return t.Faint
	}
	if r.download {
		r.tile = &fileTile{p: p}
		r.tile.Style = kit.Style{Width: kit.Px(20), Height: kit.Px(20), Flex: kit.FlexNone}
		r.glyph.Add(r.tile)
		r.label.Direction = kit.DirAuto
		r.label.Style.Flex = kit.FlexOf(0, 1, kit.Length{})
		r.label.Style.MinWidth = kit.Rem(6 * 0.84375) // min-inline-size: 6em
		r.Add(r.label)
		r.pct = views.NewText("", fontPct, views.InkAccent, kit.TextOptions{})
		r.pct.Style.Flex = kit.FlexNone
		r.bar = widgets.NewSegmentBar(widgets.SegMini)
		r.meta = views.NewLine("", fontSmall, small)
		r.meta.Style.Flex = kit.FlexOf(0, 40, kit.Length{})
		r.host = textrun.New(nil, fontSmall, fontHit, small, views.InkAccent)
		r.host.Semantics.Role = kit.RoleNone
		r.host.Direction = kit.DirAuto
		r.host.Style = kit.Style{Flex: kit.FlexOf(0, 20, kit.Length{}), MinWidth: kit.Px(0), MaxWidth: kit.Pct(30),
			Margin: kit.Edges{Start: kit.Auto}, Padding: kit.Edges{Start: kit.Px(8)}}
		r.Add(r.pct, r.bar, r.meta, r.host)
		return r
	}
	r.icon = widgets.NewIcon(0, 16, func(t *tokens.Theme) kit.Color {
		if r.current {
			return t.Accent
		}
		return t.Muted
	})
	r.glyph.Add(r.icon)
	r.label.Style.Flex = kit.FlexNone
	r.label.Style.MaxWidth = kit.Pct(60)
	r.desc = views.NewLine("", fontSmall, small)
	r.desc.Style.Flex = kit.FlexOf(1, 1, kit.Length{})
	r.desc.Semantics.Hidden = true // the option's description
	r.why = views.NewText("", fontSmall, views.InkMuted, kit.TextOptions{})
	r.why.Style.Flex = kit.FlexNone
	r.why.Semantics.Hidden = true
	r.keys = &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(3), Flex: kit.FlexNone, Margin: kit.Edges{Start: kit.Auto}}}
	r.keys.Semantics.Hidden = true
	r.Add(r.label, r.desc, r.why, r.keys)
	return r
}

// update shows the entry (the same command or download, perhaps changed).
func (r *row) update(e *core.Entry) {
	q := r.p.text
	if !r.download {
		c := e.Command
		r.disabled = c.Disabled
		if n, ok := icons.ByName(c.Icon); ok {
			r.icon.SetName(n)
			r.icon.SetHidden(false)
		} else {
			r.icon.SetHidden(true)
		}
		r.label.SetParts(partsOf(core.HighlightParts(q, c.Label)))
		r.desc.SetText(c.Description)
		r.why.SetText(core.UnavailableText(c))
		if c.Shortcut != r.key || (r.key == "" && len(r.keys.Children()) > 0) {
			r.key = c.Shortcut
			r.keys.RemoveAll()
			for _, k := range core.Keys(c.Shortcut) {
				r.keys.Add(widgets.NewKbd(k))
			}
		}
		r.Semantics.Name = c.Label
		r.Semantics.Description = joinDesc(c.Description, core.UnavailableText(c))
		r.Semantics.Shortcut = kit.KeyLabel(c.Shortcut)
		if r.disabled {
			r.Semantics.States |= kit.NodeDisabled // aria-disabled
		} else {
			r.Semantics.States &^= kit.NodeDisabled
		}
		r.syncParts()
		r.SemanticsChanged()
		return
	}
	it := e.Item
	d := it.Download
	r.tile.kind = tileKind(it.Kind)
	r.tile.Invalidate()
	r.label.SetParts(partsOf(core.HighlightParts(q, it.Name)))
	if it.Tag != "" && (r.tag == nil || r.tag.Semantics.Name != it.Tag) {
		if r.tag != nil {
			r.Remove(r.tag)
		}
		r.tag = views.NewTag(it.Tag, nil)
		r.tag.Semantics.Hidden = true
		r.Insert(2, r.tag)
	} else if it.Tag == "" && r.tag != nil {
		r.Remove(r.tag)
		r.tag = nil
	}
	running := core.IsRunning(d)
	if running {
		percent, speed, eta := runningText(d)
		r.pct.SetText(percent)
		r.pct.SetHidden(percent == "")
		r.cell[0] = segCell(segments.RowCell(d))
		r.bar.Set(r.cell[:], barState(d.GetState()))
		meta := speed
		if eta != "" {
			meta += " · " + eta
		}
		r.meta.SetText(meta)
	} else {
		r.meta.SetText(restText(d, r.p.now()))
		r.host.SetParts(partsOf(core.HighlightParts(q, it.Host)))
	}
	r.pct.SetHidden(!running || r.pct.Text() == "")
	r.bar.SetHidden(!running)
	r.host.SetHidden(running || it.Host == "")
	r.Semantics.Name = it.Name
	r.SemanticsChanged()
}

func joinDesc(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + " " + b
}

// syncParts shows what the current and unavailable states show: the reason on the current
// option, its description then hidden.
func (r *row) syncParts() {
	if r.download {
		return
	}
	shownWhy := r.current && r.disabled
	r.why.SetHidden(!shownWhy)
	r.desc.SetHidden(r.desc.Text() == "" || shownWhy)
	r.keys.SetHidden(len(r.keys.Children()) == 0)
	if r.disabled {
		r.Cursor = kit.CursorDefault
	} else {
		r.Cursor = kit.CursorPointer
	}
}

// setCurrent makes the row the keyboard's current option (aria-selected).
func (r *row) setCurrent(on bool) {
	if on == r.current {
		return
	}
	r.current = on
	if on {
		r.Semantics.States |= kit.NodeSelected
	} else {
		r.Semantics.States &^= kit.NodeSelected
	}
	r.syncParts()
	r.fade(on)
	r.Invalidate()
	r.SemanticsChanged()
}

// fade moves the --sel ground in (transition: background-color var(--dur-2) ease).
func (r *row) fade(on bool) {
	to := float32(0)
	if on {
		to = 1
	}
	w := r.Window()
	if w != nil && r.tween != nil {
		w.StopAnimation(r.tween)
		r.tween = nil
	}
	if w == nil || !w.MotionOK() {
		r.ground = to
		return
	}
	r.tween = &kit.Tween{From: r.ground, To: to, Duration: tokens.Dur2, Ease: cssEase, Set: func(v float32) {
		r.ground = v
		r.Invalidate()
	}}
	w.Animate(r.tween)
}

// cssEase is CSS's ease, cubic-bezier(0.25, 0.1, 0.25, 1).
var cssEase = kit.CubicBezier(0.25, 0.1, 0.25, 1)

// Attached is kit.Attacher's: a row shown is at its state at once.
func (r *row) Attached() {
	if r.current {
		r.ground = 1
	} else {
		r.ground = 0
	}
}

// Paint is kit.Painter's.
func (r *row) Paint(pc *kit.PaintContext) {
	t := views.TokensOf(&r.Element)
	box := views.SnapBox(pc, 8)
	if r.ground > 0 {
		pc.Canvas.FillRoundedRect(box, t.Sel.WithAlpha(t.Sel.A*r.ground))
	}
	if !r.current {
		return
	}
	w := pc.Scale.SnapWidth(1)
	if !t.IsForced() {
		pc.Canvas.StrokeRoundedRect(box.Inset(w/2), kit.Stroke{Width: w}, t.SelLine) // inset 0 0 0 1px
	}
	ring := pc.Scale.SnapWidth(2) // outline: 2px solid var(--focus); outline-offset: -2px
	pc.Canvas.StrokeRoundedRect(box.Inset(ring/2), kit.Stroke{Width: ring}, t.Focus)
}

// HandlePointer is kit.PointerHandler's: a pointer over the row makes it current
// (onpointermove), a click runs it.
func (r *row) HandlePointer(in *kit.PointerInput) bool {
	switch in.Action {
	case kit.PointerMove:
		r.p.setActive(r.i)
		return false
	case kit.PointerDown:
		if in.Button != kit.ButtonLeft {
			return false
		}
		r.pressed = true
		r.Window().Capture(&r.Element)
		return true
	case kit.PointerUp:
		if !r.pressed || in.Button != kit.ButtonLeft {
			return false
		}
		r.pressed = false
		if kit.RectAt(kit.Point{}, r.Size()).Contains(in.Local) && r.i < len(r.p.entries) {
			r.p.run(&r.p.entries[r.i], in.Mods&(kit.ModCtrl|kit.ModMeta) != 0)
		}
		return true
	case kit.PointerCancel:
		r.pressed = false
	}
	return false
}

// Label is the row's label (its text, highlighted).
func (r *row) Label() *textrun.Line { return r.label }

// Current reports the current row.
func (r *row) Current() bool { return r.current }

// Texts are the row's visible texts in order (tests: an option's textContent).
func (r *row) Texts() []string {
	var out []string
	add := func(e *kit.Element, s string) {
		if e != nil && !e.Style.Hidden && s != "" {
			out = append(out, s)
		}
	}
	add(&r.label.Element, r.label.Text())
	if r.download {
		if r.tag != nil {
			add(&r.tag.Element, r.tag.Semantics.Name)
		}
		add(&r.pct.Element, r.pct.Text())
		add(&r.meta.Element, r.meta.Text())
		add(&r.host.Element, r.host.Text())
		return out
	}
	add(&r.desc.Element, r.desc.Text())
	add(&r.why.Element, r.why.Text())
	if !r.keys.Style.Hidden {
		out = append(out, core.Keys(r.key)...)
	}
	return out
}

// Keys are the shortcut's key caps.
func (r *row) Keys() []string {
	if r.download {
		return nil
	}
	return core.Keys(r.key)
}

// Marks are the label's highlighted runs.
func (r *row) Marks() []string {
	var out []string
	for _, l := range []*textrun.Line{r.label, r.host} {
		if l == nil || l.Style.Hidden {
			continue
		}
		for _, p := range l.Parts() {
			if p.Mark {
				out = append(out, p.Text)
			}
		}
	}
	return out
}

// HasBar reports the mini segment bar shown (.segbar.size-mini).
func (r *row) HasBar() bool { return r.bar != nil && !r.bar.Style.Hidden }

func partsOf(ps []core.Part) []textrun.Part {
	out := make([]textrun.Part, len(ps))
	for i, p := range ps {
		out[i] = textrun.Part{Text: p.Text, Mark: p.Hit}
	}
	return out
}

// runningText is CommandPalette's runningText: the percentage, the speed (or the state) and
// what is left.
func runningText(d *downexv1.Download) (percent, speed, eta string) {
	percent = format.Percent(format.ProgressFraction(d))
	downloading := d.GetState() == downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING
	speed = display.StatusText(d)
	if downloading {
		speed = format.Speed(float64(d.GetSpeedBps()))
	}
	if left := format.EtaSeconds(d); downloading && !math.IsInf(left, 0) && !math.IsNaN(left) {
		eta = format.Eta(left) + " left"
	}
	return percent, speed, eta
}

// restText is CommandPalette's restText: "Completed · 22 Oct · 2.3 GB".
func restText(d *downexv1.Download, now float64) string {
	s := display.StatusText(d)
	when := millisOf(d.GetCreatedAt())
	if d.GetState() == downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED {
		if c := millisOf(d.GetCompletedAt()); c > 0 {
			when = c
		}
	}
	if when > 0 {
		s += " · " + format.Date(when, now)
	}
	if d.GetSize() >= 0 {
		s += " · " + format.Size(d)
	}
	return s
}

// millisOf is a timestamp in epoch milliseconds (0 when unset).
func millisOf(ts *timestamppb.Timestamp) float64 {
	if ts == nil {
		return 0
	}
	return float64(ts.AsTime().UnixMilli())
}

func segCell(c segments.SegCell) widgets.SegCell {
	return widgets.SegCell{Fraction: c.Fraction, Live: c.Live, Indeterminate: c.Indeterminate}
}

// barState is a download state's bar colour (SegmentBar's toneOf).
func barState(s downexv1.DownloadState) widgets.BarState {
	switch s {
	case downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING:
		return widgets.BarDownloading
	case downexv1.DownloadState_DOWNLOAD_STATE_PROBING:
		return widgets.BarProbing
	case downexv1.DownloadState_DOWNLOAD_STATE_PAUSED:
		return widgets.BarPaused
	case downexv1.DownloadState_DOWNLOAD_STATE_FAILED:
		return widgets.BarFailed
	case downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED:
		return widgets.BarCompleted
	case downexv1.DownloadState_DOWNLOAD_STATE_CANCELED:
		return widgets.BarCanceled
	}
	return widgets.BarQueued
}

// tileKind is a FileIcon kind by its name.
func tileKind(name string) icons.TileKind {
	for k := icons.TileKind(0); k <= icons.TileOther; k++ {
		if k.String() == name {
			return k
		}
	}
	return icons.TileOther
}

// fileTile is FileIcon at 20 px.
type fileTile struct {
	kit.Element
	p    *Palette
	kind icons.TileKind
}

// Paint is kit.Painter's.
func (f *fileTile) Paint(pc *kit.PaintContext) {
	s := f.p.tileSet()
	if s == nil {
		return
	}
	at := kit.Pt(pc.Scale.Snap(pc.Origin.X), pc.Scale.Snap(pc.Origin.Y))
	s.DrawTile(pc.Canvas, f.kind, at, 20, views.TokensOf(&f.Element))
}
