package downloads

import (
	"downex/ui/kit"
	"downex/ui/views"
)

// run is a piece of a richLine: text in a font and an ink (a <b> inside a fact).
type run struct {
	text string
	font kit.Font
	ink  views.Ink
}

// richLine is one line of inline text in several fonts and inks, as HTML lays a <dd> with a
// <b> out: the runs one after another on a shared baseline, each advancing by its width with
// its trailing spaces. It does not wrap; past its box it is clipped (the details pane's lines
// fit their columns at the reference sizes).
type richLine struct {
	kit.Element
	runs    []run
	layouts []kit.TextLayout
	styles  []kit.TextStyle
	xs      []kit.DIP
	ys      []kit.DIP
	width   kit.DIP
	height  kit.DIP
	base    kit.DIP
	meas    kit.TextMeasurer
	dirty   bool
	align   kit.TextAlign
}

func newRichLine(runs ...run) *richLine {
	l := &richLine{}
	l.Semantics.Role = kit.RoleText
	l.Style = kit.Style{MinWidth: kit.Px(0), Overflow: kit.OverflowClipInline}
	l.set(runs...)
	return l
}

// set replaces the runs.
func (l *richLine) set(runs ...run) {
	if sameRuns(runs, l.runs) {
		return
	}
	l.runs = append(l.runs[:0], runs...)
	l.dirty = true
	name := ""
	for _, r := range runs {
		name += r.text
	}
	l.Semantics.Name = name
	l.SemanticsChanged()
	l.Relayout()
	l.Invalidate()
}

func sameRuns(a, b []run) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].text != b[i].text || a[i].font != b[i].font {
			return false
		}
	}
	return true
}

// TextContent is the line's text, for the names of its ancestors.
func (l *richLine) TextContent() string { return l.Semantics.Name }

func (l *richLine) release() {
	for _, t := range l.layouts {
		if t != nil {
			t.Release()
		}
	}
	l.layouts = l.layouts[:0]
}

// Detached is kit.Detacher's.
func (l *richLine) Detached() {
	l.release()
	l.dirty = true
}

func (l *richLine) measure(ctx *kit.LayoutContext) {
	styles := l.styles[:0]
	for _, r := range l.runs {
		styles = append(styles, r.font.Style(ctx.Scale))
	}
	same := !l.dirty && ctx.Text == l.meas && len(styles) == len(l.styles)
	if same {
		for i := range styles {
			if styles[i] != l.styles[i] {
				same = false
				break
			}
		}
	}
	l.styles = styles
	if same {
		return
	}
	l.release()
	l.meas, l.dirty = ctx.Text, false
	l.xs, l.ys = l.xs[:0], l.ys[:0]
	l.width, l.height, l.base = 0, 0, 0
	if ctx.Text == nil {
		return
	}
	var above, below kit.DIP
	x := kit.DIP(0)
	for i, r := range l.runs {
		t, err := ctx.Text.LayoutText(r.text, styles[i], kit.TextOptions{})
		if err != nil {
			t = nil
		}
		l.layouts = append(l.layouts, t)
		l.xs = append(l.xs, x)
		if t == nil {
			continue
		}
		m := t.Metrics()
		above = max(above, m.Baseline)
		below = max(below, m.Height-m.Baseline)
		l.width = x + m.Width
		x += m.Advance
	}
	l.base = above
	l.height = above + below
	for _, t := range l.layouts {
		y := kit.DIP(0)
		if t != nil {
			y = above - t.Metrics().Baseline
		}
		l.ys = append(l.ys, y)
	}
}

// ContentWidths is kit.Layouter's.
func (l *richLine) ContentWidths(ctx *kit.LayoutContext) (min, max kit.DIP) {
	l.measure(ctx)
	return l.width, l.width
}

// ContentHeight is kit.Layouter's.
func (l *richLine) ContentHeight(ctx *kit.LayoutContext, _ kit.DIP) kit.DIP {
	l.measure(ctx)
	return l.height
}

// LayoutContent is kit.Layouter's.
func (l *richLine) LayoutContent(ctx *kit.LayoutContext, _ kit.Rect) { l.measure(ctx) }

// ContentBaseline is kit.Baseliner's.
func (l *richLine) ContentBaseline() (kit.DIP, bool) { return l.base, len(l.layouts) > 0 }

// Paint is kit.Painter's.
func (l *richLine) Paint(p *kit.PaintContext) {
	t := tok(&l.Element)
	cr := l.ContentRect()
	dx := kit.DIP(0)
	switch l.align {
	case kit.TextAlignEnd:
		dx = cr.Width() - l.width
	case kit.TextAlignCenter:
		dx = (cr.Width() - l.width) / 2
	}
	for i, tl := range l.layouts {
		if tl == nil || i >= len(l.runs) {
			continue
		}
		ink := l.runs[i].ink
		if ink == nil {
			ink = views.InkInk
		}
		p.DrawText(tl, kit.Pt(cr.Min.X+dx+l.xs[i], cr.Min.Y+l.ys[i]), ink(t))
	}
}
