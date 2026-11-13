//go:build darwin && cgo

package mac

/*
#include "textct.h"
*/
import "C"

import (
	"math"

	"downex/ui/kit"
)

// fitSlop is how far a line may measure past its box and still fit: float sums of the same
// advances.
const fitSlop = 0.01

// textLine is one line of a layout: the units [start, end) of the text (end before a hard
// line break), set as one Core Text line. A cut line shows "…" in place of [cutFrom,
// cutTo); a clamp's last line hides everything after it (cutTo is then the text's end). A
// cut line's Core Text line is made of its own string, whose indexes are local (see local
// and global); other lines use the text's indexes.
type textLine struct {
	line           C.CTLineRef // nil for an empty line
	start, end     int
	cut            bool
	cutFrom, cutTo int
	x              float32 // where the line's origin goes in the box
	// width and adv are the line's width without and with its trailing white space as
	// WebKit measures it: the sum of its pieces between break opportunities, each set
	// alone (no kerning across them). ink is where the line's text without its trailing
	// white space starts in the Core Text line (not 0 when right to left white space or
	// an ellipsis sits at the left).
	width, adv, ink float32
}

// textLayout is a kit.TextLayout: the text broken into lines once, each set by Core Text,
// placed in its box as WebKit places them (uniform line boxes, CSS half-leading).
type textLayout struct {
	eng  *textEngine
	ok   bool // laid out and not released
	text string
	m    kit.TextMetrics
	aa   kit.Antialias
	rtl  bool

	lineH, base float32
	lines       []textLine
	u           []uint16 // the text in UTF-16
	flags       []uint8  // per unit of u: clusterStart, breakBefore, icuBreak
	segs        []segment

	// scratch
	cls    []cluster
	cum    []float64
	cutU   []uint16
	making bool // inkLeft reads l.cutU: a cut line is being made
}

const (
	clusterStart = 1 // a composed character sequence starts here
	breakBefore  = 2 // a line may break before this unit (WebKit's rules)
	icuBreak     = 4 // the system's line breaking allows a break before this unit
)

// segment is a piece of a paragraph that WebKit measures on its own: the text between two
// break opportunities, white space apart. Its width is its advance set alone.
type segment struct {
	start, end int
	w          float64
	space      bool
}

// cluster is a composed character sequence of a line: its advance, its UTF-16 length,
// and whether it is white space.
type cluster struct {
	w  float32
	n  int
	ws bool
}

// layoutText implements kit.TextMeasurer's LayoutText.
func (e *textEngine) layoutText(text string, style kit.TextStyle, opts kit.TextOptions) (kit.TextLayout, error) {
	l := &textLayout{eng: e}
	if err := l.lay(text, style, opts); err != nil {
		l.release()
		return nil, err
	}
	l.ok = true
	e.live++
	return l, nil
}

// Relayout implements kit.TextRelayouter: the layout holds text in style with opts in place
// of what it held, so that a label whose text changes every tick makes no new layout. A
// failure releases the layout.
func (l *textLayout) Relayout(text string, style kit.TextStyle, opts kit.TextOptions) error {
	if !l.ok {
		return errInvalid("relayout of a released text layout")
	}
	if err := l.lay(text, style, opts); err != nil {
		l.release()
		l.ok = false
		l.eng.live--
		return err
	}
	return nil
}

// lay lays out text in l, replacing what l held. On an error the caller releases l.
func (l *textLayout) lay(text string, style kit.TextStyle, opts kit.TextOptions) error {
	e := l.eng
	if err := e.usable(); err != nil {
		return err
	}
	s, err := normStyle(style)
	if err != nil {
		return err
	}
	if !(opts.MaxWidth >= 0) || !finite(opts.MaxWidth) {
		return errInvalid("text box width %v", opts.MaxWidth)
	}
	opts.MaxLines = max(opts.MaxLines, 0)
	f, err := e.format(s)
	if err != nil {
		return err
	}
	l.release()
	l.text, l.aa = text, opts.Antialias
	l.lineH, l.base = f.metrics.LineHeight, f.metrics.Baseline
	l.u = append(l.u[:0], e.utf16Of(text)...)

	dir := opts.Direction
	if dir == kit.DirAuto {
		dir = kit.FirstStrong(text)
	}
	l.rtl = dir == kit.DirRTL
	middle := false
	if opts.Truncate == kit.TruncateMiddle {
		// The middle ellipsis is for one line; elsewhere it acts as the end one.
		middle = opts.MaxWidth > 0 && opts.Wrap == kit.TextWrapNone && !hasHardBreak(text)
		if !middle {
			opts.Truncate = kit.TruncateEnd
		}
	}
	return l.build(f, opts, middle)
}

// release frees the layout's Core Text lines.
func (l *textLayout) release() {
	for i := range l.lines {
		C.dxtRelease(C.CFTypeRef(l.lines[i].line))
		l.lines[i].line = 0
	}
	l.lines = l.lines[:0]
	l.m = kit.TextMetrics{}
}

// isHardBreak reports whether the unit c ends a paragraph.
func isHardBreak(c uint16) bool {
	switch c {
	case '\n', '\r', '\v', '\f', 0x85, 0x2028, 0x2029:
		return true
	}
	return false
}

func hasHardBreak(s string) bool {
	for _, c := range s {
		if c < 0x10000 && isHardBreak(uint16(c)) {
			return true
		}
	}
	return false
}

// isSpace reports whether the unit c is white space that hangs at a line's end.
func isSpace(c uint16) bool {
	switch c {
	case ' ', '\t', 0x1680, 0x205F, 0x3000:
		return true
	}
	return c >= 0x2000 && c <= 0x200A && c != 0x2007
}

// isBreakableSpace is WebKit's: the white space a line may break at.
func isBreakableSpace(c uint16) bool { return c == ' ' || c == '\t' || c == '\n' }

// needsICU is WebKit's needsLineBreakIterator: past ASCII, line breaking asks ICU.
func needsICU(c uint16) bool { return c > 0x7F && c != 0xA0 }

func isDigit(c uint16) bool { return c >= '0' && c <= '9' }

func isAlnum(c uint16) bool { return isDigit(c) || c|0x20 >= 'a' && c|0x20 <= 'z' }

// asciiBreakAfter is WebKit's shouldBreakAfter (BreakLines): its table of breaks between
// two printable ASCII characters, compatible with other browsers. A line may break after
// "-" or "?" (before a digit only when a letter or digit comes before the hyphen, so that
// a minus sign stays with its number), and after other punctuation before an opening
// bracket; never between letters, digits or after a "/".
func asciiBreakAfter(lastLast, last, ch uint16) bool {
	if last == '-' && isDigit(ch) {
		return isAlnum(lastLast)
	}
	if ch < '!' || ch > 0x7F || last < '!' || last > 0x7F {
		return false
	}
	switch {
	case last == '-' || last == '?':
		return ch != '!' && ch != '$'
	case isAlnum(last), last == '(', last == '<', last == '[', last == '{':
		return false
	}
	return ch == '(' || ch == '<' || ch == '[' || ch == '{'
}

// build breaks l.u into lines, places them and measures them.
func (l *textLayout) build(f *textFormat, opts kit.TextOptions, middle bool) error {
	u := l.u
	n := len(u)
	free := opts.MaxWidth == 0
	w := opts.MaxWidth
	wrap := opts.Wrap
	if free {
		wrap = kit.TextWrapNone
	}
	attrs := f.attributes(l.rtl)

	if cap(l.flags) < n+1 {
		l.flags = make([]uint8, n+1, n+n/2+16)
	}
	l.flags = l.flags[:n+1]
	clear(l.flags)
	l.segs = l.segs[:0]
	var str C.CFStringRef
	var ts C.CTTypesetterRef
	if n > 0 {
		astr := C.dxtAttrString((*C.UniChar)(&u[0]), C.CFIndex(n), attrs)
		defer C.dxtRelease(C.CFTypeRef(astr))
		str = C.CFAttributedStringGetString(astr)
		ts = C.CTTypesetterCreateWithAttributedString(astr)
		defer C.dxtRelease(C.CFTypeRef(ts))
		C.dxtClusters(str, (*C.uchar)(&l.flags[0]), C.CFIndex(n))
	}
	l.flags[n] |= clusterStart

	var minW float64
	for ps := 0; ps <= n; {
		pe := ps
		for pe < n && !isHardBreak(u[pe]) {
			pe++
		}
		if pe > ps {
			l.opportunities(str, ps, pe)
			l.segment(attrs, ps, pe)
		}
		if wrap == kit.TextWrapNone || pe == ps {
			l.addLine(ts, attrs, ps, pe)
		} else {
			minW = max(minW, l.wrapParagraph(ts, attrs, ps, pe, w, wrap))
		}
		if pe >= n {
			break
		}
		next := pe + 1
		if u[pe] == '\r' && next < n && u[next] == '\n' {
			next++
		}
		if next >= n {
			// a text that ends in a line break has an empty line after it
			l.addLine(ts, attrs, n, n)
			break
		}
		ps = next
	}

	truncated := false
	if !free {
		if opts.MaxLines > 0 && len(l.lines) > opts.MaxLines {
			for i := opts.MaxLines; i < len(l.lines); i++ {
				C.dxtRelease(C.CFTypeRef(l.lines[i].line))
			}
			l.lines = l.lines[:opts.MaxLines]
			l.cutEnd(f, attrs, len(l.lines)-1, n, w)
			truncated = true
		}
		if opts.Truncate == kit.TruncateEnd {
			for i := range l.lines {
				if !l.lines[i].cut && l.lines[i].width > w+fitSlop {
					l.cutEnd(f, attrs, i, l.lines[i].end, w)
					truncated = true
				}
			}
		}
		if middle && len(l.lines) == 1 && l.lines[0].adv > w+fitSlop {
			if l.cutMiddle(f, attrs, w) {
				truncated = true
			}
		}
	}

	m := kit.TextMetrics{Lines: len(l.lines), Truncated: truncated, Direction: kit.DirLTR, Baseline: l.base}
	if l.rtl {
		m.Direction = kit.DirRTL
	}
	for _, ln := range l.lines {
		m.Width = max(m.Width, ln.width)
		m.Advance = max(m.Advance, ln.adv)
	}
	if truncated {
		m.Width, m.Advance = min(m.Width, w), min(m.Advance, w)
	}
	m.Height = float32(len(l.lines)) * l.lineH
	if wrap == kit.TextWrapNone {
		m.MinWidth = m.Width
	} else {
		m.MinWidth = float32(minW)
	}
	box := w
	if free {
		box = m.Width
	}
	for i := range l.lines {
		l.place(&l.lines[i], box, opts.Align)
	}
	l.m = m
	return nil
}

// opportunities marks where a line may break in the paragraph [ps, pe), as WebKit's
// nextBreakablePosition finds them: after white space, by its ASCII table, and where ICU
// allows one next to a character past ASCII.
func (l *textLayout) opportunities(str C.CFStringRef, ps, pe int) {
	u, f := l.u, l.flags
	for i := ps; i < pe; i++ {
		if needsICU(u[i]) {
			C.dxtBreaks(str, C.CFIndex(ps), C.CFIndex(pe), (*C.uchar)(&f[0]))
			break
		}
	}
	for i := ps + 1; i < pe; i++ {
		ch, last := u[i], u[i-1]
		var lastLast uint16
		if i-2 >= ps {
			lastLast = u[i-2]
		}
		switch {
		case isBreakableSpace(last) && !isBreakableSpace(ch):
			f[i] |= breakBefore // the white space hangs on the line before
		case isBreakableSpace(ch) || isBreakableSpace(last):
		case asciiBreakAfter(lastLast, last, ch):
			f[i] |= breakBefore
		case (needsICU(ch) || needsICU(last)) && f[i]&icuBreak != 0:
			f[i] |= breakBefore
		}
	}
	f[pe] |= breakBefore
}

// segment splits the paragraph [ps, pe) into the pieces WebKit measures alone (at break
// opportunities, and where white space starts or ends) and measures them.
func (l *textLayout) segment(attrs C.CFDictionaryRef, ps, pe int) {
	u := l.u
	a := ps
	for i := ps + 1; i <= pe; i++ {
		if i < pe && l.flags[i]&breakBefore == 0 && isSpace(u[i]) == isSpace(u[i-1]) {
			continue
		}
		l.segs = append(l.segs, segment{start: a, end: i, w: l.widthOf(attrs, a, i), space: isSpace(u[a])})
		a = i
	}
}

// widthOf is the advance of the units [a, b) set alone.
func (l *textLayout) widthOf(attrs C.CFDictionaryRef, a, b int) float64 {
	if b <= a {
		return 0
	}
	return float64(C.dxtWidthOf((*C.UniChar)(&l.u[a]), C.CFIndex(b-a), attrs))
}

// segAt is the index of the segment that holds unit i (len(l.segs) past them).
func (l *textLayout) segAt(i int) int {
	lo, hi := 0, len(l.segs)
	for lo < hi {
		m := (lo + hi) / 2
		if l.segs[m].end <= i {
			lo = m + 1
		} else {
			hi = m
		}
	}
	return lo
}

// measure is the width of [s, e) as WebKit measures it, without and with the white space
// at its end: the sum of its segments, a piece of one set alone.
func (l *textLayout) measure(attrs C.CFDictionaryRef, s, e int) (width, adv float64) {
	for k := l.segAt(s); k < len(l.segs) && l.segs[k].start < e; k++ {
		sg := l.segs[k]
		w := sg.w
		if sg.start < s || sg.end > e {
			w = l.widthOf(attrs, max(sg.start, s), min(sg.end, e))
		}
		adv += w
		if !sg.space {
			width = adv
		}
	}
	return width, adv
}

// place sets where ln's origin goes in a box w wide: its text (without trailing white
// space, which hangs past the end) at the alignment's edge, or at the start edge when it
// is wider than the box.
func (l *textLayout) place(ln *textLine, w float32, align kit.TextAlign) {
	tw := ln.width
	var left float32
	switch {
	case tw > w+fitSlop || align == kit.TextAlignStart:
		if l.rtl {
			left = w - tw
		}
	case align == kit.TextAlignCenter:
		left = (w - tw) / 2
	default: // end
		if !l.rtl {
			left = w - tw
		}
	}
	ln.x = left - ln.ink
}

// addLine adds the line [start, end) of the typesetter's text.
func (l *textLayout) addLine(ts C.CTTypesetterRef, attrs C.CFDictionaryRef, start, end int) {
	ln := textLine{start: start, end: end}
	if end > start {
		ln.line = C.CTTypesetterCreateLine(ts, C.CFRange{location: C.CFIndex(start), length: C.CFIndex(end - start)})
		ln.ink = l.inkLeft(ln.line, start, end)
		w, adv := l.measure(attrs, start, end)
		ln.width, ln.adv = float32(w), float32(adv)
	}
	l.lines = append(l.lines, ln)
}

// lineBounds is line's width without and with its trailing white space.
func (l *textLayout) lineBounds(line C.CTLineRef) (width, adv float32) {
	b := &l.eng.bounds
	C.dxtLineBounds(line, &b[0], &b[1])
	return float32(b[0] - b[1]), float32(b[0])
}

// inkLeft is where the glyphs of the units [a, b) of line (in its indexes), trailing
// white space left out, start: the left edge of its text.
func (l *textLayout) inkLeft(line C.CTLineRef, a, b int) float32 {
	for b > a && isSpace(l.lineUnit(b-1)) {
		b--
	}
	if b <= a || line == 0 {
		return 0
	}
	out := l.eng.runScratch()
	n := int(C.dxtRangeRuns(line, C.CFIndex(a), C.CFIndex(b), &out[0], C.int(len(out)/3)))
	if n == 0 {
		return 0
	}
	x := out[0]
	for i := 1; i < n; i++ {
		x = min(x, out[3*i])
	}
	return float32(x)
}

// lineUnit is unit i of the string whose line is being made: l.cutU while a cut line is,
// else l.u.
func (l *textLayout) lineUnit(i int) uint16 {
	if l.making {
		return l.cutU[i]
	}
	return l.u[i]
}

// advances fills the engine's widths scratch with the advance of every unit of [start,
// end) in line (whose string indexes start at base), and returns it.
func (l *textLayout) advances(line C.CTLineRef, base, start, end int) []C.double {
	e := l.eng
	n := end - start
	if cap(e.widths) < n+1 {
		e.widths = make([]C.double, n+1, n+n/2+16)
	}
	ws := e.widths[:n+1]
	clear(ws)
	if n > 0 && line != 0 {
		C.dxtIndexAdvances(line, C.CFIndex(base+start), &ws[0], C.CFIndex(n))
	}
	return ws[:n]
}

// wrapParagraph breaks the paragraph [ps, pe) into lines no wider than w where wrap
// allows, as WebKit's line breaking does: greedily, white space hanging at the end of a
// line. It returns the paragraph's min-content width.
func (l *textLayout) wrapParagraph(ts C.CTTypesetterRef, attrs C.CFDictionaryRef, ps, pe int, w float32, wrap kit.TextWrap) float64 {
	u, flags, segs := l.u, l.flags, l.segs

	// the advance of every unit in the paragraph's line, summed: for breaks inside a word
	// and the widest cluster
	cum := l.cum[:0]
	if wrap != kit.TextWrapWords {
		pl := C.CTTypesetterCreateLine(ts, C.CFRange{location: C.CFIndex(ps), length: C.CFIndex(pe - ps)})
		ws := l.advances(pl, 0, ps, pe)
		C.dxtRelease(C.CFTypeRef(pl))
		cum = append(cum, 0)
		for i, v := range ws {
			cum = append(cum, cum[i]+float64(v))
		}
		l.cum = cum
	}
	// clustered is the width of [a, b) without the white space at its end, from cum
	clustered := func(a, b int) float64 {
		for b > a && isSpace(u[b-1]) {
			b--
		}
		return cum[b-ps] - cum[a-ps]
	}

	var minW float64
	if wrap == kit.TextWrapWords {
		// the widest piece between break opportunities
		var acc, content float64
		for k := l.segAt(ps); k < len(segs) && segs[k].start < pe; k++ {
			acc += segs[k].w
			if !segs[k].space {
				content = acc
			}
			if flags[segs[k].end]&breakBefore != 0 {
				minW = max(minW, content)
				acc, content = 0, 0
			}
		}
	} else {
		// the widest cluster that is not white space
		a := ps
		for i := ps + 1; i <= pe; i++ {
			if flags[i]&clusterStart != 0 {
				if !isSpace(u[a]) {
					minW = max(minW, cum[i-ps]-cum[a-ps])
				}
				a = i
			}
		}
	}

	limit := float64(w) + fitSlop
	for s := ps; s < pe; {
		best := -1
		if wrap != kit.TextWrapCharacters {
			var acc, content float64
			for k := l.segAt(s); k < len(segs) && segs[k].start < pe; k++ {
				sg := segs[k]
				pw := sg.w
				if sg.start < s {
					pw = l.widthOf(attrs, s, sg.end)
				}
				acc += pw
				if !sg.space {
					content = acc
				}
				if content > limit {
					break
				}
				if flags[sg.end]&breakBefore != 0 {
					best = sg.end
				}
			}
		}
		switch {
		case best >= 0:
		case wrap == kit.TextWrapWords:
			// a word wider than the line overflows it whole
			for best = s + 1; best < pe && flags[best]&breakBefore == 0; best++ {
			}
		default:
			// break between clusters, after those that fit (at least one); with
			// TextWrapAnywhere only inside a word that fits on no line
			for i := s + 1; i <= pe; i++ {
				if flags[i]&clusterStart == 0 {
					continue
				}
				if best >= 0 && clustered(s, i) > limit {
					break
				}
				best = i
			}
			// the white space after the break hangs on this line
			for best < pe && isSpace(u[best]) {
				best++
			}
		}
		l.addLine(ts, attrs, s, best)
		s = best
	}
	return minW
}

// clusters reads the clusters of [start, end) of ln's line into l.cls.
func (l *textLayout) clusters(ln *textLine, start, end int) []cluster {
	cls := l.cls[:0]
	if end <= start || ln.line == 0 {
		l.cls = cls
		return cls
	}
	ws := l.advances(ln.line, 0, start, end)
	a := start
	for i := start + 1; i <= end; i++ {
		if i < end && l.flags[i]&clusterStart == 0 {
			continue
		}
		var cw float64
		for _, v := range ws[a-start : i-start] {
			cw += float64(v)
		}
		cls = append(cls, cluster{w: float32(cw), n: i - a, ws: isSpace(l.u[a])})
		a = i
	}
	l.cls = cls
	return cls
}

// ellipsisWidth is the advance of "…" in f, letter spacing included.
func (l *textLayout) ellipsisWidth(f *textFormat, attrs C.CFDictionaryRef) float32 {
	if !f.ellWOK {
		e := [1]uint16{0x2026}
		line := C.dxtLineOf((*C.UniChar)(&e[0]), 1, attrs)
		if line != 0 {
			_, f.ellW = l.lineBounds(line)
			C.dxtRelease(C.CFTypeRef(line))
		}
		f.ellWOK = true
	}
	return f.ellW
}

// setCut makes line i show "…" in place of [from, to) of the text: its line is made of
// [start, from) + "…" + [to, end).
func (l *textLayout) setCut(attrs C.CFDictionaryRef, i, from, to int) {
	ln := &l.lines[i]
	b := append(l.cutU[:0], l.u[ln.start:from]...)
	b = append(b, 0x2026)
	if to < ln.end {
		b = append(b, l.u[to:ln.end]...)
	}
	l.cutU = b
	C.dxtRelease(C.CFTypeRef(ln.line))
	ln.line = C.dxtLineOf((*C.UniChar)(&b[0]), C.CFIndex(len(b)), attrs)
	ln.width, ln.adv = l.lineBounds(ln.line)
	l.making = true
	ln.ink = l.inkLeft(ln.line, 0, len(b))
	l.making = false
	ln.cut, ln.cutFrom, ln.cutTo = true, from, to
}

// cutEnd ends line i with "…" in place of what does not fit a box w wide, hiding the text
// up to hideTo (the line's end, or the text's end for a clamp): whole clusters are kept
// from its logical start, white space before the cut goes with it.
func (l *textLayout) cutEnd(f *textFormat, attrs C.CFDictionaryRef, i, hideTo int, w float32) {
	ln := &l.lines[i]
	cls := l.clusters(ln, ln.start, ln.end)
	avail := w - l.ellipsisWidth(f, attrs)
	keep := 0
	var kw float32
	for keep < len(cls) && kw+cls[keep].w <= avail+fitSlop {
		kw += cls[keep].w
		keep++
	}
	for try := 0; try < 8; try++ {
		k := keep
		for k > 0 && cls[k-1].ws {
			k--
		}
		from := ln.start
		for _, c := range cls[:k] {
			from += c.n
		}
		l.setCut(attrs, i, from, max(hideTo, from))
		if ln.adv <= w+fitSlop || keep == 0 {
			return
		}
		keep-- // shaping made it wider than the clusters added up to
	}
}

// cutMiddle replaces the middle of the one line of the layout with "…", keeping whole
// clusters from both ends so that the ellipsis sits near the middle (kit.TruncateMiddle:
// addresses and paths); white space next to the cut goes with it. It reports whether it
// cut anything. It is ui/win's textmiddle.go over Core Text's clusters.
func (l *textLayout) cutMiddle(f *textFormat, attrs C.CFDictionaryRef, w float32) bool {
	ln := &l.lines[0]
	cls := l.clusters(ln, ln.start, ln.end)
	avail := w - l.ellipsisWidth(f, attrs)
	head, tail := 0, len(cls) // kept: cls[:head] and cls[tail:]
	var hw, tw float32
fill:
	for head < tail {
		// Add to the shorter side what still fits, else to the other side, else stop.
		switch {
		case hw <= tw && hw+tw+cls[head].w <= avail:
			hw += cls[head].w
			head++
		case hw+tw+cls[tail-1].w <= avail:
			tail--
			tw += cls[tail].w
		case hw+tw+cls[head].w <= avail:
			hw += cls[head].w
			head++
		default:
			break fill
		}
	}
	if head == tail { // everything fits after all
		return false
	}
	for try := 0; try < 8; try++ {
		h, t := head, tail
		for h > 0 && cls[h-1].ws {
			h--
		}
		for t < len(cls) && cls[t].ws {
			t++
		}
		from, to := ln.start, ln.end
		for _, c := range cls[:h] {
			from += c.n
		}
		for _, c := range cls[t:] {
			to -= c.n
		}
		l.setCut(attrs, 0, from, to)
		if ln.adv <= w+fitSlop || (head == 0 && tail == len(cls)) {
			return true
		}
		// Shaping across the cut made it wider than the clusters added up to: drop one more
		// cluster from the longer side.
		if hw >= tw && head > 0 {
			head--
			hw -= cls[head].w
		} else if tail < len(cls) {
			tw -= cls[tail].w
			tail++
		} else if head > 0 {
			head--
		}
	}
	return true
}

// Metrics implements kit.TextLayout.
func (l *textLayout) Metrics() kit.TextMetrics {
	if !l.ok {
		return kit.TextMetrics{}
	}
	return l.m
}

// Release implements kit.TextLayout.
func (l *textLayout) Release() {
	if !l.ok {
		return
	}
	l.release()
	l.ok = false
	l.eng.live--
}

// engine is the engine that made l, for a canvas's check that the layout is its own.
func (l *textLayout) engine() *textEngine { return l.eng }

// lineOf is the line that holds index i of l.u: the last that starts at or before it.
func (l *textLayout) lineOf(i int) int {
	k := 0
	for k+1 < len(l.lines) && l.lines[k+1].start <= i {
		k++
	}
	return k
}

// local is index i of l.u in ln's line: before the ellipsis for text it hides (after it
// when after is set, for the end of a range), clamped to the line.
func (ln *textLine) local(i int, after bool) int {
	if !ln.cut {
		return min(max(i, ln.start), ln.end)
	}
	e := ln.cutFrom - ln.start // the ellipsis's index
	switch {
	case i <= ln.start:
		return 0
	case i <= ln.cutFrom:
		return i - ln.start
	case i < ln.cutTo:
		if after {
			return e + 1
		}
		return e
	}
	return e + 1 + min(i, ln.end) - min(ln.cutTo, ln.end)
}

// global is the index in l.u of index j of ln's line; the ellipsis stands for the start
// of the text it hides.
func (ln *textLine) global(j int) int {
	if !ln.cut {
		return j
	}
	e := ln.cutFrom - ln.start
	switch {
	case j <= e:
		return ln.start + j
	case j == e+1:
		return ln.cutTo
	}
	return min(ln.cutTo, ln.end) + j - e - 1
}

// HitTest implements kit.TextLayout.
func (l *textLayout) HitTest(p kit.Point) kit.TextHit {
	if !l.ok || !finitePt(p) || len(l.lines) == 0 {
		return kit.TextHit{}
	}
	k := 0
	if l.lineH > 0 {
		// clamped as a float first: a point far away (or a tiny line height) would
		// overflow the conversion to int
		f := math.Floor(float64(p.Y) / float64(l.lineH))
		k = int(min(max(f, 0), float64(len(l.lines)-1)))
	}
	k = min(max(k, 0), len(l.lines)-1)
	ln := &l.lines[k]
	left := ln.x + ln.ink
	inside := p.Y >= 0 && p.Y < l.m.Height && p.X >= left && p.X <= left+ln.width && ln.line != 0
	pos := ln.start
	if ln.line != 0 {
		x := p.X - ln.x
		j := int(C.dxtIndexAt(ln.line, C.double(x)))
		if ln.cut && l.onEllipsis(ln, x) {
			pos = ln.cutFrom
		} else {
			pos = ln.global(j)
		}
	}
	pos = l.clusterStart(pos)
	return kit.TextHit{Pos: utf8Offset(l.text, pos), Inside: inside}
}

// onEllipsis reports whether x (in ln's line) is on its ellipsis.
func (l *textLayout) onEllipsis(ln *textLine, x float32) bool {
	e := ln.cutFrom - ln.start
	out := l.eng.runScratch()
	if C.dxtRangeRuns(ln.line, C.CFIndex(e), C.CFIndex(e+1), &out[0], 1) == 0 {
		return false
	}
	return float64(x) >= float64(out[0]) && float64(x) <= float64(out[1])
}

// clusterStart moves index i of l.u back to the start of its cluster.
func (l *textLayout) clusterStart(i int) int {
	i = min(max(i, 0), len(l.u))
	for i > 0 && i < len(l.u) && l.flags[i]&clusterStart == 0 {
		i--
	}
	return i
}

// Caret implements kit.TextLayout.
func (l *textLayout) Caret(pos int) (kit.Point, kit.DIP) {
	if !l.ok || len(l.lines) == 0 {
		return kit.Point{}, 0
	}
	i := l.clusterStart(utf16Len(l.text, pos))
	k := l.lineOf(i)
	ln := &l.lines[k]
	x := ln.x
	if ln.line != 0 {
		x += float32(C.dxtOffsetAt(ln.line, C.CFIndex(ln.local(i, false))))
	}
	return kit.Point{X: x, Y: float32(k) * l.lineH}, l.lineH
}

// AppendRangeRects implements kit.TextLayout: per line, one rectangle for each run of one
// direction (Core Text's runs, split at every fallback font, merged where they touch).
func (l *textLayout) AppendRangeRects(dst []kit.Rect, start, end int) []kit.Rect {
	if !l.ok || end <= start {
		return dst
	}
	a := utf16Len(l.text, start)
	b := utf16Len(l.text, end)
	if b <= a {
		return dst
	}
	e := l.eng
	for k := range l.lines {
		ln := &l.lines[k]
		if ln.line == 0 {
			continue
		}
		hi := ln.end
		if ln.cut {
			hi = max(hi, ln.cutTo)
		}
		if b <= ln.start || a >= hi {
			continue
		}
		la, lb := ln.local(a, false), ln.local(b, true)
		if lb <= la {
			continue
		}
		out := e.runScratch()
		n := int(C.dxtRangeRuns(ln.line, C.CFIndex(la), C.CFIndex(lb), &out[0], C.int(len(out)/3)))
		// sort the pieces by x (insertion sort: a handful), then merge those of one
		// direction that touch
		for i := 1; i < n; i++ {
			for j := i; j > 0 && out[3*j] < out[3*(j-1)]; j-- {
				for c := 0; c < 3; c++ {
					out[3*j+c], out[3*(j-1)+c] = out[3*(j-1)+c], out[3*j+c]
				}
			}
		}
		top := float32(k) * l.lineH
		for i := 0; i < n; {
			x0, x1, dir := out[3*i], out[3*i+1], out[3*i+2]
			j := i + 1
			for j < n && out[3*j+2] == dir && out[3*j] <= x1+0.5 {
				x1 = max(x1, out[3*j+1])
				j++
			}
			dst = append(dst, kit.R(ln.x+float32(x0), top, ln.x+float32(x1), top+l.lineH))
			i = j
		}
	}
	return dst
}

// draw draws the layout with the top-left corner of its box at origin in c, in the
// context's current (DIP, y-down) coordinates: grayscale antialiasing without font
// smoothing for either kit.Antialias (app.css's -webkit-font-smoothing: antialiased), the
// baseline on a device pixel as WebKit puts it, colour glyphs in their colours.
func (l *textLayout) draw(ctx C.CGContextRef, origin kit.Point, c kit.Color) {
	if l == nil || !l.ok || ctx == 0 || !finitePt(origin) {
		return
	}
	C.dxtTextBegin(ctx, C.double(c.R), C.double(c.G), C.double(c.B), C.double(c.A))
	for k := range l.lines {
		ln := &l.lines[k]
		if ln.line == 0 {
			continue
		}
		C.dxtDrawLine(ctx, ln.line, C.double(origin.X+ln.x), C.double(origin.Y+float32(k)*l.lineH+l.base))
	}
	C.dxtTextEnd(ctx)
}

var (
	_ kit.TextLayout     = (*textLayout)(nil)
	_ kit.TextRelayouter = (*textLayout)(nil)
)
