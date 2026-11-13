// Package kittest holds fakes of the backend's side of the kit's contracts, for tests
// that run without a window: a host, a recording canvas, a text measurer with
// deterministic metrics, resources, a fake clock that also runs timers, and a loop that
// drives a root as a backend's message loop does. They follow the contracts' rules
// (frames only on request or for invalid areas, dirty areas rounded out to device
// pixels, idempotent releases) so that what passes here holds against a real backend.
package kittest

import (
	"fmt"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	"downex/ui/kit"
)

// The fake font's metrics, in em: every character but a space or a wide one is 0.5 em
// wide, a space 0.25 em, a wide one (CJK, emoji) 1 em, the ellipsis 1 em; the ascent is
// 0.9 em and the descent 0.25 em, with a 0.05 em line gap (normal line height 1.2 em).
const (
	charEm     = 0.5
	spaceEm    = 0.25
	wideEm     = 1.0
	ellipsisEm = 1.0
	ascentEm   = 0.9
	descentEm  = 0.25
	gapEm      = 0.05
)

// Measurer is a kit.TextMeasurer with deterministic metrics (see the constants above).
// It counts the layouts it made and those not yet released.
type Measurer struct {
	Made, Live int
}

func runeEm(r rune) float32 {
	switch {
	case r == ' ' || r == '\t':
		return spaceEm
	case r >= 0x2E80 && r <= 0xA4CF, r >= 0xAC00 && r <= 0xD7A3, r >= 0xF900 && r <= 0xFAFF,
		r >= 0xFF00 && r <= 0xFF60, r >= 0x1F000:
		return wideEm
	}
	return charEm
}

// Advance is the width of s in st without wrapping.
func Advance(s string, st kit.TextStyle) kit.DIP {
	var w kit.DIP
	for _, r := range s {
		w += kit.DIP(runeEm(r)*st.Size) + kit.DIP(st.LetterSpacing*st.Size)
	}
	return w
}

func fontMetrics(st kit.TextStyle) kit.FontMetrics {
	asc, desc, gap := kit.DIP(ascentEm*st.Size), kit.DIP(descentEm*st.Size), kit.DIP(gapEm*st.Size)
	lh := asc + desc + gap
	if st.LineHeight > 0 {
		lh = kit.DIP(st.LineHeight * st.Size)
	}
	return kit.FontMetrics{
		Ascent: asc, Descent: desc, LineGap: gap, LineHeight: lh,
		Baseline:  (lh-(asc+desc))/2 + asc,
		CapHeight: kit.DIP(0.7 * st.Size), XHeight: kit.DIP(0.5 * st.Size),
		UnderlineOffset: kit.DIP(0.1 * st.Size), UnderlineThickness: kit.DIP(0.07 * st.Size),
	}
}

func checkStyle(st kit.TextStyle) error {
	if !(st.Size > 0) || math.IsInf(float64(st.Size), 0) {
		return fmt.Errorf("kittest: font size %v: %w", st.Size, kit.ErrInvalid)
	}
	return nil
}

// FontMetrics is kit.TextMeasurer's.
func (m *Measurer) FontMetrics(st kit.TextStyle) (kit.FontMetrics, error) {
	if err := checkStyle(st); err != nil {
		return kit.FontMetrics{}, err
	}
	return fontMetrics(st), nil
}

// Layout is a laid-out paragraph of the fake measurer.
type Layout struct {
	Text  string
	Style kit.TextStyle
	Opts  kit.TextOptions

	m        *Measurer
	lines    []line
	metrics  kit.TextMetrics
	lh       kit.DIP
	released bool
}

type line struct {
	start, end int // byte range of the text the line shows
	text       string
	width      kit.DIP // without trailing spaces
	advance    kit.DIP
	x          kit.DIP // where the line starts in the box
}

// LayoutText is kit.TextMeasurer's.
func (m *Measurer) LayoutText(text string, st kit.TextStyle, o kit.TextOptions) (kit.TextLayout, error) {
	if err := checkStyle(st); err != nil {
		return nil, err
	}
	if !utf8.ValidString(text) {
		text = strings.ToValidUTF8(text, "�")
	}
	l := &Layout{Text: text, Style: st, Opts: o, m: m}
	l.build()
	m.Made++
	m.Live++
	return l, nil
}

func (l *Layout) build() {
	st, o := l.Style, l.Opts
	fm := fontMetrics(st)
	l.lh = fm.LineHeight
	ell := kit.DIP(ellipsisEm*st.Size) + kit.DIP(st.LetterSpacing*st.Size)
	var lines []line
	minW := kit.DIP(0)
	for _, para := range splitParagraphs(l.Text) {
		lines = append(lines, l.breakLines(para.text, para.start, &minW)...)
	}
	truncated := false
	if o.MaxLines > 0 && len(lines) > o.MaxLines {
		lines = lines[:o.MaxLines]
		room := o.MaxWidth
		if room <= 0 {
			room = kit.DIP(math.Inf(1))
		}
		lines[len(lines)-1] = l.cutTo(lines[len(lines)-1], room, ell, false)
		truncated = true
	}
	if o.MaxWidth > 0 && o.Truncate != kit.TruncateNone {
		for i := range lines {
			if lines[i].width > o.MaxWidth+1.0/64 {
				lines[i] = l.cutTo(lines[i], o.MaxWidth, ell, o.Truncate == kit.TruncateMiddle && o.Wrap == kit.TextWrapNone)
				truncated = true
			}
		}
	}
	dir := o.Direction
	if dir == kit.DirAuto {
		dir = kit.DirLTR
		for _, r := range l.Text {
			if unicode.In(r, unicode.Hebrew, unicode.Arabic) {
				dir = kit.DirRTL
				break
			}
			if unicode.IsLetter(r) {
				break
			}
		}
	}
	var w, adv kit.DIP
	for _, ln := range lines {
		w, adv = max(w, ln.width), max(adv, ln.advance)
	}
	box := o.MaxWidth
	if box <= 0 {
		box = w
	}
	for i := range lines {
		lines[i].x = alignX(o.Align, dir, box, lines[i].width)
	}
	if o.Wrap == kit.TextWrapNone {
		minW = w
	}
	l.lines = lines
	l.metrics = kit.TextMetrics{
		Width: w, Advance: adv, Height: kit.DIP(len(lines)) * l.lh, Baseline: fm.Baseline,
		MinWidth: minW, Lines: len(lines), Truncated: truncated, Direction: dir,
	}
}

func alignX(a kit.TextAlign, dir kit.Direction, box, w kit.DIP) kit.DIP {
	if w >= box {
		if dir == kit.DirRTL {
			return box - w
		}
		return 0
	}
	switch {
	case a == kit.TextAlignCenter:
		return (box - w) / 2
	case (a == kit.TextAlignEnd) != (dir == kit.DirRTL):
		return box - w
	}
	return 0
}

type paragraph struct {
	text  string
	start int
}

// splitParagraphs splits text at hard line breaks: \n, \r\n, \r, U+2028 and U+2029.
func splitParagraphs(s string) []paragraph {
	var out []paragraph
	start := 0
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		switch r {
		case '\r':
			out = append(out, paragraph{s[start:i], start})
			if i+1 < len(s) && s[i+1] == '\n' {
				n = 2
			}
			start = i + n
		case '\n', ' ', ' ':
			out = append(out, paragraph{s[start:i], start})
			start = i + n
		}
		i += n
	}
	return append(out, paragraph{s[start:], start})
}

// breakLines breaks one paragraph into lines by the layout's wrap mode and MaxWidth.
func (l *Layout) breakLines(p string, base int, minW *kit.DIP) []line {
	st, o := l.Style, l.Opts
	measure := func(s string) (w, adv kit.DIP) {
		adv = Advance(s, st)
		return Advance(strings.TrimRight(s, " "), st), adv
	}
	whole := func(s string, at int) line {
		w, adv := measure(s)
		return line{start: at, end: at + len(s), text: s, width: w, advance: adv}
	}
	// the unbreakable pieces, for MinWidth
	switch o.Wrap {
	case kit.TextWrapWords:
		for _, wd := range strings.Fields(p) {
			*minW = max(*minW, Advance(wd, st))
		}
	case kit.TextWrapAnywhere, kit.TextWrapCharacters:
		for _, r := range p {
			if r != ' ' {
				*minW = max(*minW, Advance(string(r), st))
			}
		}
	}
	if o.Wrap == kit.TextWrapNone || o.MaxWidth <= 0 {
		return []line{whole(p, base)}
	}
	maxW := o.MaxWidth + 1.0/64
	var out []line
	// tokens: words with their trailing spaces, or single characters
	var toks []string
	if o.Wrap == kit.TextWrapCharacters {
		for _, r := range p {
			toks = append(toks, string(r))
		}
	} else {
		i := 0
		for i < len(p) {
			j := i
			for j < len(p) && p[j] != ' ' {
				j++
			}
			for j < len(p) && p[j] == ' ' {
				j++
			}
			toks = append(toks, p[i:j])
			i = j
		}
	}
	cur, at := "", base // the line being filled and where it starts
	pos := base         // where the next token starts
	flush := func() {
		out = append(out, whole(cur, at))
		cur, at = "", pos
	}
	for _, t := range toks {
		tw := Advance(strings.TrimRight(t, " "), st)
		if cur != "" && Advance(cur, st)+tw > maxW {
			flush()
		}
		if cur == "" && tw > maxW && o.Wrap == kit.TextWrapAnywhere {
			// a word longer than the line breaks inside itself
			for _, r := range t {
				rs := string(r)
				if cur != "" && Advance(strings.TrimRight(cur+rs, " "), st) > maxW {
					flush()
				}
				cur += rs
				pos += len(rs)
			}
			continue
		}
		cur += t
		pos += len(t)
	}
	out = append(out, whole(cur, at))
	return out
}

// cutTo shortens a line to fit maxW with an ellipsis at its end (or in its middle).
func (l *Layout) cutTo(ln line, maxW, ell kit.DIP, middle bool) line {
	st := l.Style
	runes := []rune(strings.TrimRight(ln.text, " "))
	room := maxW - ell
	if middle {
		head, tail := 0, len(runes)
		var w kit.DIP
		for head < tail {
			nw := w + Advance(string(runes[head]), st)
			if nw > room {
				break
			}
			w = nw
			head++
			if head >= tail {
				break
			}
			nw = w + Advance(string(runes[tail-1]), st)
			if nw > room {
				break
			}
			w = nw
			tail--
		}
		s := string(runes[:head]) + "…" + string(runes[tail:])
		ln.text, ln.width = s, w+ell
		ln.advance = ln.width
		return ln
	}
	n := 0
	var w kit.DIP
	for n < len(runes) {
		nw := w + Advance(string(runes[n]), st)
		if nw > room {
			break
		}
		w = nw
		n++
	}
	ln.text = string(runes[:n]) + "…"
	ln.width = w + ell
	ln.advance = ln.width
	ln.end = ln.start + len(string(runes[:n]))
	return ln
}

// Metrics is kit.TextLayout's.
func (l *Layout) Metrics() kit.TextMetrics {
	if l.released {
		return kit.TextMetrics{}
	}
	return l.metrics
}

// Lines are the texts of the layout's lines (an ellipsis as …).
func (l *Layout) Lines() []string {
	out := make([]string, len(l.lines))
	for i, ln := range l.lines {
		out[i] = ln.text
	}
	return out
}

// Released reports whether Release was called.
func (l *Layout) Released() bool { return l.released }

// HitTest is kit.TextLayout's.
func (l *Layout) HitTest(p kit.Point) kit.TextHit {
	if l.released || len(l.lines) == 0 {
		return kit.TextHit{}
	}
	i := int(math.Floor(float64(p.Y / l.lh)))
	inside := i >= 0 && i < len(l.lines)
	i = min(max(i, 0), len(l.lines)-1)
	ln := l.lines[i]
	x := p.X - ln.x
	inside = inside && x >= 0 && x < ln.width
	pos := ln.start
	var acc kit.DIP
	for j, r := range l.Text[ln.start:min(ln.end, len(l.Text))] {
		w := Advance(string(r), l.Style)
		if x < acc+w/2 {
			return kit.TextHit{Pos: ln.start + j, Inside: inside}
		}
		acc += w
		pos = ln.start + j + utf8.RuneLen(r)
	}
	return kit.TextHit{Pos: pos, Inside: inside}
}

// Caret is kit.TextLayout's.
func (l *Layout) Caret(pos int) (kit.Point, kit.DIP) {
	if l.released || len(l.lines) == 0 {
		return kit.Point{}, 0
	}
	for i, ln := range l.lines {
		if pos <= ln.end || i == len(l.lines)-1 {
			p := min(max(pos, ln.start), min(ln.end, len(l.Text)))
			return kit.Pt(ln.x+Advance(l.Text[ln.start:p], l.Style), kit.DIP(i)*l.lh), l.lh
		}
	}
	return kit.Point{}, l.lh
}

// AppendRangeRects is kit.TextLayout's.
func (l *Layout) AppendRangeRects(dst []kit.Rect, start, end int) []kit.Rect {
	if l.released {
		return dst
	}
	for i, ln := range l.lines {
		a, b := max(start, ln.start), min(end, ln.end, len(l.Text))
		if a >= b {
			continue
		}
		x0 := ln.x + Advance(l.Text[ln.start:a], l.Style)
		x1 := ln.x + Advance(l.Text[ln.start:b], l.Style)
		dst = append(dst, kit.R(x0, kit.DIP(i)*l.lh, x1, kit.DIP(i+1)*l.lh))
	}
	return dst
}

// Release is kit.TextLayout's; it is idempotent.
func (l *Layout) Release() {
	if l.released {
		return
	}
	l.released = true
	if l.m != nil {
		l.m.Live--
	}
}
