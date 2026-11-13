package settings

// [mac webkit] overflow-wrap: anywhere (Integration's `.steps .mono`) as WebKit breaks it,
// under kit.Environment.WebKit only: a code word wider than a whole line (it moves to a new
// line first, at the ordinary break before it) is cut after the last character that still
// fits the line, and its rest goes on the next lines, cut again while it is too wide
// ("Downex.app/Contents/Resources/extension/chr" | "ome-mv3"). The Windows window keeps the
// word whole, as approved.

import (
	"math"
	"unicode/utf8"

	"downex/ui/kit"
)

const fitSlack = 0.01

// cssLineHeight is the line box height of line-height: k at size: k × size as Chromium
// keeps it, or under WebKit's look in whole pixels as WebKit computes it (k × size in
// LayoutUnits of 1/64 px, floored, then truncated: 12.5 px × 1.5 is 18, 12 × 1.4 is 16;
// measured in WKWebView, Integration's steps are 18 px lines and ui/mac's text engine
// makes its line boxes so).
func (in *inline) cssLineHeight(k float32, size kit.DIP) kit.DIP {
	if !in.anywhere {
		return size * kit.DIP(k)
	}
	v := math.Round(float64(k)*float64(size)*1e4) / 1e4
	return kit.DIP(math.Floor(math.Floor(v*64) / 64))
}

// splitWide sets pieces and words to src's with the code words too wide for w cut.
func (in *inline) splitWide(w kit.DIP) {
	wide := false
	for _, wd := range in.srcWords {
		if in.wordWidth(in.src[wd.from:wd.to]) > w+fitSlack && in.hasCode(in.src[wd.from:wd.to]) {
			wide = true
			break
		}
	}
	if !wide || in.measure == nil {
		return
	}
	P, W := in.splitP[:0], in.splitW[:0]
	emit := func(ps []piece, space bool) {
		from := len(P)
		P = append(P, ps...)
		W = append(W, word{from: from, to: len(P), space: space})
	}
	var cur []piece
	for _, wd := range in.srcWords {
		cur = append(cur[:0], in.src[wd.from:wd.to]...)
		if in.wordWidth(cur) <= w+fitSlack || !in.hasCode(cur) {
			emit(cur, wd.space)
			continue
		}
		for guard := 0; guard < 1000; guard++ {
			i, x := in.overflowAt(cur, w)
			if i < 0 {
				emit(cur, wd.space)
				break
			}
			p := cur[i]
			if !in.spans[p.span].code {
				// only the code span breaks anywhere: before it, or not at all
				if i > 0 && in.spans[cur[i-1].span].code {
					emit(cur[:i], false)
					cur = append([]piece(nil), cur[i:]...)
					continue
				}
				emit(cur, wd.space)
				break
			}
			a, _ := in.pad(&p)
			head, tail, ok := in.cutPiece(p, w-x-a, i == 0)
			if !ok && i == 0 { // one character wider than the line: it overflows
				emit(cur, wd.space)
				break
			}
			if !ok { // nothing of it fits after what is before it: the break goes before it
				emit(cur[:i], false)
				cur = append([]piece(nil), cur[i:]...)
				continue
			}
			rest := append([]piece{tail}, cur[i+1:]...)
			emit(append(cur[:i:i], head), false)
			cur = rest
		}
	}
	in.splitP, in.splitW = P, W
	in.pieces, in.words = P, W
}

// wordWidth is a word's width without its trailing space, its chips' padding with it.
func (in *inline) wordWidth(ps []piece) kit.DIP {
	var adv kit.DIP
	for i := range ps {
		a, b := in.pad(&ps[i])
		if i == len(ps)-1 {
			return adv + a + ps[i].w + b
		}
		adv += a + ps[i].adv + b
	}
	return 0
}

func (in *inline) hasCode(ps []piece) bool {
	for i := range ps {
		if in.spans[ps[i].span].code {
			return true
		}
	}
	return false
}

// overflowAt is the first piece that ends past w on a line of its own, and where it
// starts; -1 when the word fits.
func (in *inline) overflowAt(ps []piece, w kit.DIP) (int, kit.DIP) {
	x := kit.DIP(0)
	for i := range ps {
		a, b := in.pad(&ps[i])
		if x+a+ps[i].w+b > w+fitSlack {
			return i, x
		}
		x += a + ps[i].adv + b
	}
	return -1, 0
}

// cutPiece cuts p after the most characters that fit in avail (one at least when must):
// head ends the line, tail starts the next.
func (in *inline) cutPiece(p piece, avail kit.DIP, must bool) (head, tail piece, ok bool) {
	runes := utf8.RuneCountInString(p.text)
	at := func(n int) int { // byte offset of rune n
		i := 0
		for k := 0; k < n; k++ {
			_, s := utf8.DecodeRuneInString(p.text[i:])
			i += s
		}
		return i
	}
	// the longest prefix of n runes (1 ≤ n < runes) whose width fits
	lo, hi := 0, runes-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		c, okc := in.cut(p.span, p.text[:at(mid)])
		if okc && c.w <= avail+fitSlack {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	if lo == 0 {
		if !must || runes < 2 {
			return piece{}, piece{}, false
		}
		lo = 1
	}
	h, okh := in.cut(p.span, p.text[:at(lo)])
	t, okt := in.cut(p.span, p.text[at(lo):])
	if !okh || !okt {
		return piece{}, piece{}, false
	}
	head, tail = p, p
	head.text, head.layout, head.w, head.adv, head.base, head.last = p.text[:at(lo)], h.layout, h.w, h.w, h.base, false
	tail.text, tail.layout, tail.w, tail.adv, tail.base, tail.first = p.text[at(lo):], t.layout, t.w, t.adv, t.base, false
	return head, tail, true
}

// cut is the layout of a part of span's text, made once and kept until the next release.
func (in *inline) cut(span int, text string) (cutText, bool) {
	k := cutKey{span, text}
	if c, ok := in.cuts[k]; ok {
		return c, true
	}
	l, err := in.measure.LayoutText(text, in.spans[span].font.Style(in.scale), kit.TextOptions{})
	if err != nil {
		return cutText{}, false
	}
	m := l.Metrics()
	c := cutText{layout: l, w: m.Width, adv: m.Advance, base: m.Baseline}
	if in.cuts == nil {
		in.cuts = map[cutKey]cutText{}
	}
	in.cuts[k] = c
	return c, true
}
