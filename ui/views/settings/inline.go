package settings

import (
	"math"
	"strings"

	"downex/ui/kit"
	"downex/ui/views"
)

// span is a run of a paragraph in one font and ink. code is Integration's `.steps .mono`: a
// chip of --surface-2 with 4 px of padding at its ends (padding-inline: 4px; border-radius:
// var(--r-xs)), in --ink.
type span struct {
	text string
	font kit.Font
	ink  views.Ink
	code bool
}

// piece is one span's part of a word.
type piece struct {
	span   int
	text   string // with the space after it, when one follows
	layout kit.TextLayout
	w, adv kit.DIP // the width without and with the trailing space
	base   kit.DIP // its first baseline in its layout
	x, y   kit.DIP // where its layout goes in the box
	line   int
	first  bool // the span's first piece (its padding at the start)
	last   bool // the span's last piece (its padding at the end)
}

// [mac webkit] chipRadii are a code piece's chip corners under WebKit's look: an inline box
// broken over lines is sliced (box-decoration-break: slice), so only the span's first piece
// has round start corners and only its last round end corners.
func chipRadii(p *piece, rtl bool) kit.Radii {
	var r kit.Radii
	start, end := p.first, p.last
	if rtl {
		start, end = end, start
	}
	if start {
		r.TopLeft, r.BottomLeft = 4, 4
	}
	if end {
		r.TopRight, r.BottomRight = 4, 4
	}
	return r
}

// word is what no line break splits: pieces between break opportunities (a space, or after
// a hyphen between letters, as Chromium breaks "downex-nmhost").
type word struct {
	from, to int // pieces
	space    bool
}

// inline is a paragraph of spans that wrap together, as inline content does: lines break at
// spaces (and after hyphens), each line lineHeight × the base font's size high with the text on
// one baseline. It is one text node for assistive technology.
type inline struct {
	kit.Element
	base       kit.Font
	lineHeight float32
	spans      []span
	pieces     []piece
	words      []word

	made    bool
	scale   kit.Scale
	codeAsc kit.DIP
	codeDes kit.DIP
	lineH   kit.DIP
	asc     kit.DIP // the line box's baseline from its top
	lines   int
	wrapW   kit.DIP
	// boxAbove and boxBelow are each span's inline box above and below the baseline (its
	// line-height's half-leading around its font), which grows a line it sticks out of (CSS 2
	// §10.8); tops and bases are the lines' tops and baselines. Both are kept from layout to
	// layout.
	boxAbove, boxBelow []kit.DIP
	tops               []kit.DIP
	bases              []kit.DIP

	// [mac webkit] src and srcWords are the pieces and words setSpans cut (they own the
	// layouts); pieces and words are what wrap places: src itself, or under WebKit's look
	// (anywhere) the code words too wide for a line cut where they fill it (overflow-wrap:
	// anywhere), from splitP and splitW, with the cut parts' layouts in cuts.
	src      []piece
	srcWords []word
	anywhere bool
	madeWK   bool // the look make measured for
	rtl      bool
	measure  kit.TextMeasurer
	splitP   []piece
	splitW   []word
	cuts     map[cutKey]cutText
}

// [mac webkit] cutKey and cutText are a cut part's text in its span and its layout.
type cutKey struct {
	span int
	text string
}

type cutText struct {
	layout       kit.TextLayout
	w, adv, base kit.DIP
}

// newInline makes a paragraph in base (its size and line height are the line boxes').
func newInline(base kit.Font, lineHeight float32, spans ...span) *inline {
	in := &inline{base: base, lineHeight: lineHeight}
	in.Style.MinWidth = kit.Px(0)
	in.setSpans(spans)
	return in
}

func (in *inline) setSpans(spans []span) {
	in.release()
	in.spans = spans
	in.pieces, in.words = in.src[:0], in.srcWords[:0] // [mac webkit] cut into src
	var b strings.Builder
	for _, s := range spans {
		b.WriteString(s.text)
	}
	in.Semantics.Role = kit.RoleText
	in.Semantics.Name = b.String()
	// Cut the spans into pieces at spaces and after a hyphen between letters; a word ends
	// at a break.
	start := 0
	for si, s := range spans {
		t := s.text
		firstPiece := len(in.pieces)
		for len(t) > 0 {
			i := breakIndex(t)
			p := piece{span: si, text: t[:i]}
			in.pieces = append(in.pieces, p)
			t = t[i:]
			// a break after this piece?
			pt := p.text
			if strings.HasSuffix(pt, " ") || strings.HasSuffix(pt, "-") && len(t) > 0 {
				in.words = append(in.words, word{from: start, to: len(in.pieces), space: strings.HasSuffix(pt, " ")})
				start = len(in.pieces)
			}
		}
		if len(in.pieces) > firstPiece {
			in.pieces[firstPiece].first = true
			in.pieces[len(in.pieces)-1].last = true
		}
	}
	if start < len(in.pieces) {
		in.words = append(in.words, word{from: start, to: len(in.pieces)})
	}
	in.src, in.srcWords = in.pieces, in.words // [mac webkit]
	in.made = false
	in.Relayout()
}

// breakIndex is the length of t's first piece: up to and with the spaces after its first
// word, or up to and with a hyphen between letters.
func breakIndex(t string) int {
	for i := 0; i < len(t); i++ {
		switch {
		case t[i] == ' ':
			j := i
			for j < len(t) && t[j] == ' ' {
				j++
			}
			return j
		case t[i] == '-' && i > 0 && i+1 < len(t) && isLetter(t[i-1]) && isLetter(t[i+1]):
			return i + 1
		}
	}
	return len(t)
}

func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

func (in *inline) release() {
	for i := range in.src { // [mac webkit] src owns the layouts
		if l := in.src[i].layout; l != nil {
			l.Release()
			in.src[i].layout = nil
		}
	}
	for k, c := range in.cuts { // [mac webkit]
		c.layout.Release()
		delete(in.cuts, k)
	}
	in.pieces, in.words = in.src, in.srcWords
	in.made = false
}

// Detached is kit.Detacher's: the layouts go with the window.
func (in *inline) Detached() { in.release() }

func (in *inline) make(ctx *kit.LayoutContext) {
	in.anywhere, in.measure = ctx.Env.WebKit, ctx.Text // [mac webkit]
	if in.made && in.scale == ctx.Scale && in.madeWK == in.anywhere {
		return
	}
	in.release()
	in.scale = ctx.Scale
	if ctx.Text == nil {
		return
	}
	for i := range in.src { // [mac webkit] src owns the layouts
		p := &in.src[i]
		s := in.spans[p.span]
		l, err := ctx.Text.LayoutText(p.text, s.font.Style(ctx.Scale), kit.TextOptions{})
		if err != nil {
			continue
		}
		m := l.Metrics()
		p.layout, p.w, p.adv, p.base = l, m.Width, m.Advance, m.Baseline
	}
	// a line's inline boxes as Chromium makes them (inline_box_state.cc), in CSS px (the
	// references lay out alike at 1× and 1.25×): the font's ascent and descent rounded, the
	// leading (line-height less both) split with its floored half above and the rest below
	box := func(fm kit.FontMetrics, lh kit.DIP) (above, below kit.DIP) {
		a := kit.DIP(math.Round(float64(fm.Ascent)))
		d := kit.DIP(math.Round(float64(fm.Descent)))
		lead := lh - (a + d)
		up := kit.DIP(math.Floor(float64(lead / 2)))
		return a + up, d + lead - up
	}
	bs := in.base.Style(ctx.Scale)
	in.lineH = in.cssLineHeight(in.lineHeight, bs.Size) // [mac webkit]
	if fm, err := ctx.Text.FontMetrics(bs); err == nil {
		in.asc, _ = box(fm, in.lineH)
	}
	in.boxAbove, in.boxBelow = in.boxAbove[:0], in.boxBelow[:0]
	for _, s := range in.spans {
		cs := s.font.Style(ctx.Scale)
		above, below := in.asc, in.lineH-in.asc
		if fm, err := ctx.Text.FontMetrics(cs); err == nil {
			// a span's line-height is its own (.mono's 1.4) or the paragraph's, inherited
			lh := in.lineHeight
			if s.code && s.font.LineHeight > 0 {
				lh = s.font.LineHeight
			}
			above, below = box(fm, in.cssLineHeight(lh, cs.Size)) // [mac webkit]
			if s.code {
				in.codeAsc, in.codeDes = fm.Ascent, fm.Descent
			}
		}
		in.boxAbove = append(in.boxAbove, above)
		in.boxBelow = append(in.boxBelow, below)
	}
	in.made, in.madeWK = true, in.anywhere
}

const codePad = 4

func (in *inline) pad(p *piece) (start, end kit.DIP) {
	if !in.spans[p.span].code {
		return 0, 0
	}
	if p.first {
		start = codePad
	}
	if p.last {
		end = codePad
	}
	return start, end
}

// wrap breaks the words into lines w wide and places the pieces; it returns the lines.
func (in *inline) wrap(w kit.DIP) int {
	in.pieces, in.words = in.src, in.srcWords // [mac webkit]
	if in.anywhere {
		in.splitWide(w)
	}
	x := kit.DIP(0)
	line := 0
	for wi, wd := range in.words {
		// the word's width (without its trailing space) and advance
		var width, adv kit.DIP
		for i := wd.from; i < wd.to; i++ {
			p := &in.pieces[i]
			a, b := in.pad(p)
			if i == wd.to-1 {
				width = adv + a + p.w + b
			}
			adv += a + p.adv + b
		}
		if x > 0 && x+width > w+0.01 {
			line++
			x = 0
		}
		for i := wd.from; i < wd.to; i++ {
			p := &in.pieces[i]
			a, b := in.pad(p)
			p.x = x + a
			p.line = line
			x += a + p.adv + b
		}
		_ = wi
	}
	if len(in.words) == 0 {
		return 0
	}
	return line + 1
}

// lineBoxes measures the lines wrap made: each is the strut's height, grown where a
// span's inline box sticks out of it above or below the baseline. It returns the height.
func (in *inline) lineBoxes(lines int) kit.DIP {
	in.tops = in.tops[:0]
	in.bases = in.bases[:0]
	strutAbove, strutBelow := in.asc, in.lineH-in.asc
	y := kit.DIP(0)
	for l := range lines {
		above, below := strutAbove, strutBelow
		for i := range in.pieces {
			if p := &in.pieces[i]; p.line == l && p.span < len(in.boxAbove) {
				above, below = max(above, in.boxAbove[p.span]), max(below, in.boxBelow[p.span])
			}
		}
		in.tops = append(in.tops, y)
		in.bases = append(in.bases, y+above)
		y += above + below
	}
	return y
}

// ContentWidths is kit.Layouter's: the widest word, and the whole text on one line.
func (in *inline) ContentWidths(ctx *kit.LayoutContext) (lo, hi kit.DIP) {
	in.make(ctx)
	for _, wd := range in.words {
		var width, adv kit.DIP
		for i := wd.from; i < wd.to; i++ {
			p := &in.pieces[i]
			a, b := in.pad(p)
			if i == wd.to-1 {
				width = adv + a + p.w + b
			}
			adv += a + p.adv + b
		}
		lo = max(lo, width)
		hi += adv
	}
	return lo, hi
}

// ContentHeight is kit.Layouter's.
func (in *inline) ContentHeight(ctx *kit.LayoutContext, w kit.DIP) kit.DIP {
	in.make(ctx)
	return in.lineBoxes(in.wrap(w))
}

// LayoutContent is kit.Layouter's.
func (in *inline) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	in.make(ctx)
	in.rtl = ctx.RTL // [mac webkit]
	in.lines = in.wrap(box.Width())
	in.wrapW = box.Width()
	in.lineBoxes(in.lines)
	for i := range in.pieces {
		p := &in.pieces[i]
		p.y = box.Min.Y + in.bases[p.line] - p.base
		p.x += box.Min.X
		if ctx.RTL {
			p.x = box.Max.X - (p.x - box.Min.X) - p.w
		}
	}
}

// Paint is kit.Painter's: the code chips, then the text.
func (in *inline) Paint(p *kit.PaintContext) {
	t := views.TokensOf(&in.Element)
	for i := range in.pieces {
		pc := &in.pieces[i]
		s := in.spans[pc.span]
		if !s.code {
			continue
		}
		a, b := in.pad(pc)
		// the chip covers the piece's text and its padding; a space inside the span too
		right := pc.x + pc.w + b
		if !pc.last && i+1 < len(in.pieces) && in.pieces[i+1].line == pc.line {
			right = pc.x + pc.adv
		}
		baseline := pc.y + pc.base
		r := kit.R(pc.x-a, baseline-in.codeAsc, right, baseline+in.codeDes)
		if in.anywhere { // [mac webkit] sliced at line breaks
			p.FillRoundedRect(kit.RRect{Rect: r, Radii: chipRadii(pc, in.rtl)}.Normalized(), inkSurface2(t))
			continue
		}
		p.FillRoundedRect(kit.RoundRect(r, 4), inkSurface2(t))
	}
	for i := range in.pieces {
		pc := &in.pieces[i]
		if pc.layout == nil {
			continue
		}
		s := in.spans[pc.span]
		ink := s.ink
		if ink == nil {
			ink = views.InkInk
		}
		p.DrawText(pc.layout, kit.Pt(pc.x, pc.y), ink(t))
	}
}
