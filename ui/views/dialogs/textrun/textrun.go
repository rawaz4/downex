// Package textrun is a line of text in runs of two looks: the plain text and the marked
// parts. Today's window writes these as inline HTML: the palette's matched letters
// (CommandPalette.svelte's <mark>: 650 --accent-text in a 500 label) and Move to queue's lead
// ("Move <bdi class="what">name</bdi> to the end of:", the name 600 --ink in --ink-2 text).
// kit.Label holds one font, so a Line lays out one text layout per run and draws them one
// after another on a shared baseline, each starting at the previous run's advance.
//
// A Line is one line that ends in an ellipsis where it does not fit (the palette's
// white-space: nowrap; text-overflow: ellipsis), or, with Wrap, runs that go on to the next
// line when they do not fit (the lead's normal wrapping, broken between runs; a run wider
// than the line wraps inside itself). Its owner is the screen that shows it; it releases its
// layouts when it leaves its window.
package textrun

import (
	"strings"

	"downex/ui/kit"
	"downex/ui/views"
)

// Part is a run of the line: Mark draws it in the marked font and ink.
type Part struct {
	Text string
	Mark bool
}

// Line is a line of runs.
type Line struct {
	kit.Element
	// Plain and Marked are the runs' fonts; PlainInk and MarkInk their colours (nil: --ink).
	Plain, Marked     kit.Font
	PlainInk, MarkInk views.Ink
	// Wrap lets runs that do not fit go on to the next line; otherwise the line is cut
	// with an ellipsis.
	Wrap bool
	// Direction is the line's base direction (DirAuto: its first strong character's, as
	// dir=auto and <bdi> take it).
	Direction kit.Direction

	parts []Part
	text  string // the parts joined: the accessible name and the tooltip

	measurer    kit.TextMeasurer
	plainStyle  kit.TextStyle
	markedStyle kit.TextStyle
	natural     []kit.TextLayout // one per part, unbounded
	cut         kit.TextLayout   // the last shown run, cut or wrapped to its room
	cutAt       int              // the part cut (-1: none)
	cutW        kit.DIP
	placed      []placement
	rtl         bool
	truncated   bool
}

// placement is where a run is drawn, in the content box.
type placement struct {
	layout kit.TextLayout
	at     kit.Point
	mark   bool
}

// New makes a line of parts.
func New(parts []Part, plain, marked kit.Font, plainInk, markInk views.Ink) *Line {
	l := &Line{Plain: plain, Marked: marked, PlainInk: plainInk, MarkInk: markInk, cutAt: -1}
	l.Semantics.Role = kit.RoleText
	l.Style.MinWidth = kit.Px(0)
	l.SetParts(parts)
	return l
}

// Parts are the line's runs.
func (l *Line) Parts() []Part { return l.parts }

// Text is the line's text, its runs joined.
func (l *Line) Text() string { return l.text }

// TextContent is the line's text (kit's text content of an element, for tests and names).
func (l *Line) TextContent() string { return l.text }

// SetParts changes the runs.
func (l *Line) SetParts(parts []Part) {
	if equalParts(parts, l.parts) {
		return
	}
	l.parts = append(l.parts[:0], parts...)
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Text)
	}
	l.text = b.String()
	l.Semantics.Name = l.text
	l.release()
	l.Relayout()
	l.SemanticsChanged()
}

func equalParts(a, b []Part) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Truncated reports that the ellipsis cuts the line.
func (l *Line) Truncated() bool { return l.truncated }

// TooltipText is kit.TooltipSource's: the whole line while an ellipsis cuts it.
func (l *Line) TooltipText() string {
	if l.truncated {
		return l.text
	}
	return ""
}

func (l *Line) release() {
	for _, t := range l.natural {
		if t != nil {
			t.Release()
		}
	}
	l.natural = l.natural[:0]
	if l.cut != nil {
		l.cut.Release()
	}
	l.cut, l.cutAt, l.measurer = nil, -1, nil
	l.placed = l.placed[:0]
}

// Detached is kit.Detacher's: the layouts go.
func (l *Line) Detached() { l.release() }

func (l *Line) styleOf(mark bool) kit.TextStyle {
	if mark {
		return l.markedStyle
	}
	return l.plainStyle
}

// layouts lays every run out unbounded once for the scale in effect.
func (l *Line) layouts(ctx *kit.LayoutContext) []kit.TextLayout {
	ps, ms := l.Plain.Style(ctx.Scale), l.Marked.Style(ctx.Scale)
	if l.measurer == ctx.Text && ps == l.plainStyle && ms == l.markedStyle && len(l.natural) == len(l.parts) {
		return l.natural
	}
	l.release()
	l.plainStyle, l.markedStyle = ps, ms
	if ctx.Text == nil {
		return nil
	}
	l.measurer = ctx.Text
	o := kit.TextOptions{Direction: l.Direction}
	for _, p := range l.parts {
		t, err := ctx.Text.LayoutText(p.Text, l.styleOf(p.Mark), o)
		if err != nil {
			t = nil
		}
		l.natural = append(l.natural, t)
	}
	return l.natural
}

func metricsOf(t kit.TextLayout) kit.TextMetrics {
	if t == nil {
		return kit.TextMetrics{}
	}
	return t.Metrics()
}

// ContentWidths is kit.Layouter's: the runs side by side; with Wrap, the widest run that
// cannot break is the least.
func (l *Line) ContentWidths(ctx *kit.LayoutContext) (min, max kit.DIP) {
	ts := l.layouts(ctx)
	for i, t := range ts {
		m := metricsOf(t)
		if i == len(ts)-1 {
			max += m.Width
		} else {
			max += m.Advance
		}
		if l.Wrap {
			min = kit.DIP(maxf(float32(min), float32(m.MinWidth)))
		}
	}
	if !l.Wrap {
		min = max
	}
	return min, max
}

func maxf(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}

// lineHeight is a line box's height: the taller of the two fonts'.
func (l *Line) lineHeight(ts []kit.TextLayout) kit.DIP {
	var h kit.DIP
	for _, t := range ts {
		h = max(h, metricsOf(t).Height)
	}
	return h
}

// ContentHeight is kit.Layouter's.
func (l *Line) ContentHeight(ctx *kit.LayoutContext, w kit.DIP) kit.DIP {
	ts := l.layouts(ctx)
	if len(ts) == 0 {
		return 0
	}
	if !l.Wrap {
		return l.lineHeight(ts)
	}
	return l.flow(ctx, w, false)
}

// LayoutContent is kit.Layouter's.
func (l *Line) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	ts := l.layouts(ctx)
	l.placed = l.placed[:0]
	was := l.truncated
	l.truncated = false
	if len(ts) == 0 {
		return
	}
	w := box.Width()
	l.rtl = l.resolvedRTL(ts)
	if l.Wrap {
		l.flow(ctx, w, true)
	} else {
		l.oneLine(ctx, w)
	}
	if was != l.truncated {
		l.SemanticsChanged()
	}
}

// resolvedRTL is the line's direction: its own, or its first run's that has a strong
// character (dir=auto).
func (l *Line) resolvedRTL(ts []kit.TextLayout) bool {
	switch l.Direction {
	case kit.DirRTL:
		return true
	case kit.DirLTR:
		return false
	}
	for i, t := range ts {
		if hasStrong(l.parts[i].Text) {
			return metricsOf(t).Direction == kit.DirRTL
		}
	}
	return false
}

// hasStrong reports a letter in s (a run of only spaces and punctuation takes no side).
func hasStrong(s string) bool {
	for _, r := range s {
		if r > 0x40 && (r < 0x5b || r > 0x60) && (r < 0x7b || r > 0xbf) {
			return true
		}
	}
	return false
}

// cutLayout is part i laid out in w with opts (the ellipsis, or wrapping), kept until the
// next change.
func (l *Line) cutLayout(ctx *kit.LayoutContext, i int, w kit.DIP, o kit.TextOptions) kit.TextLayout {
	if l.cut != nil && l.cutAt == i && l.cutW == w {
		return l.cut
	}
	if l.cut != nil {
		l.cut.Release()
		l.cut = nil
	}
	o.MaxWidth = max(w, 1.0/64)
	o.Direction = l.Direction
	t, err := ctx.Text.LayoutText(l.parts[i].Text, l.styleOf(l.parts[i].Mark), o)
	if err != nil {
		l.cutAt = -1
		return nil
	}
	l.cut, l.cutAt, l.cutW = t, i, w
	return t
}

// place records a run at x (from the start edge) on a line whose top is y.
func (l *Line) place(t kit.TextLayout, mark bool, x, y, w, base kit.DIP) {
	m := metricsOf(t)
	at := kit.Pt(x, y+base-m.Baseline)
	if l.rtl {
		at.X = w - x - m.Width
	}
	l.placed = append(l.placed, placement{layout: t, at: at, mark: mark})
}

// oneLine places the runs on one line, the first that does not fit cut with an ellipsis and
// the rest left out.
func (l *Line) oneLine(ctx *kit.LayoutContext, w kit.DIP) {
	ts := l.natural
	base := kit.DIP(0)
	for _, t := range ts {
		base = max(base, metricsOf(t).Baseline)
	}
	const slop = 1.0 / 64
	x := kit.DIP(0)
	for i, t := range ts {
		m := metricsOf(t)
		if x+m.Width <= w+slop {
			l.place(t, l.parts[i].Mark, x, 0, w, base)
			x += m.Advance
			continue
		}
		c := l.cutLayout(ctx, i, w-x, kit.TextOptions{Truncate: kit.TruncateEnd})
		if c != nil {
			l.place(c, l.parts[i].Mark, x, 0, w, base)
		}
		l.truncated = true
		return
	}
}

// flow places the runs in lines w wide, a run that does not fit on the line starting the
// next, and returns the height; with place false it only measures.
func (l *Line) flow(ctx *kit.LayoutContext, w kit.DIP, place bool) kit.DIP {
	ts := l.natural
	lh := l.lineHeight(ts)
	base := kit.DIP(0)
	for _, t := range ts {
		base = max(base, metricsOf(t).Baseline)
	}
	const slop = 1.0 / 64
	x, y := kit.DIP(0), kit.DIP(0)
	for i, t := range ts {
		m := metricsOf(t)
		if x > 0 && x+m.Width > w+slop {
			x, y = 0, y+lh
		}
		if m.Width > w+slop {
			// a run wider than the line wraps inside itself, alone on its lines
			c := l.cutLayout(ctx, i, w, kit.TextOptions{Wrap: kit.TextWrapWords})
			cm := metricsOf(c)
			if place && c != nil {
				l.place(c, l.parts[i].Mark, 0, y, w, base)
			}
			y += max(cm.Height, lh)
			x = 0
			continue
		}
		if place {
			l.place(t, l.parts[i].Mark, x, y, w, base)
		}
		x += m.Advance
	}
	if x > 0 {
		y += lh
	}
	return y
}

// ContentBaseline is kit.Baseliner's: the first line's.
func (l *Line) ContentBaseline() (kit.DIP, bool) {
	if len(l.natural) == 0 {
		return 0, false
	}
	b := kit.DIP(0)
	for _, t := range l.natural {
		b = max(b, metricsOf(t).Baseline)
	}
	return b, true
}

// Paint is kit.Painter's: each run in its ink.
func (l *Line) Paint(p *kit.PaintContext) {
	t := views.TokensOf(&l.Element)
	plain, mark := t.Ink, t.Ink
	if l.PlainInk != nil {
		plain = l.PlainInk(t)
	}
	if l.MarkInk != nil {
		mark = l.MarkInk(t)
	}
	origin := l.ContentRect().Min
	for i := range l.placed {
		r := &l.placed[i]
		c := plain
		if r.mark {
			c = mark
		}
		p.DrawText(r.layout, origin.Add(r.at), c)
	}
}
