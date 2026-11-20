//go:build windows

package win

import (
	"sort"
	"strings"
	"testing"
	"unicode/utf8"

	"downex/ui/kit"
)

// A file name lays out in DirectWrite in the visual order today's window shows it in:
// FileName.svelte's markup (the stem, then the extension in <bdi dir="ltr">) in the list's
// <span dir="auto">, and in a <bdi> of the details title and the progress window's name.
// The wants are headless Edge 156's (Chromium on Windows 11) for exactly that markup: each
// character's Range.getBoundingClientRect, sorted left to right, ASCII characters as
// themselves, a run of other characters as "R", spaces left out. A stem whose last letter
// is Latin ("… IMAX.mp4", "…-4k.mp4") gets no isolate, so the extension stays with that
// word ("IMAX.mp4"), where an isolate would move it to the far left (".mp4IMAX").
func TestFileNameOrderMatchesChromium(t *testing.T) {
	r := newRes(t)
	for _, c := range []struct{ name, want string }{
		{"سلێمانی تا بەغداد بۆ بینینی The Odyssey و IMAX.mp4", "IMAX.mp4RTheOdysseyR"}, // the Kurdish fixture title
		{"أطيب كباب في السليمانية_ أكلات شعبية وخبز التنور الحار.mp4", ".mp4R_R"},      // the Arabic one
		{"ڕاپۆرتی-ساڵانەی-٢٠٢٦.pdf", ".pdfR-R-R"},
		{"تقرير-الميزانية-٢٠٢٦.xlsx", ".xlsxR-R-R"},
		{"گەشتی-هەولێر-4k.mp4", "4k.mp4-R-R"},
		{"أغنية-الصباح.mp3", ".mp3R-R"},
		{"وزە بۆ کردنی کارێک ؟.mp4", ".mp4R"},
		{"2015_12_17 لە سوید.mp4", ".mp4R17_12_2015"},
		{"INSIDE Kurdistan ڕاپۆرت.mkv", "INSIDEKurdistanR.mkv"},
		{"ڕاپۆرت.پدف", "R.R"},
		{"Win.10.Aio.19045.7807.Multi.iso", "Win.10.Aio.19045.7807.Multi.iso"},
	} {
		for _, dir := range []kit.Direction{kit.DirAuto, kit.DirLTR} { // the row; the title and progress name
			text := kit.FileNameLayoutText(c.name, dir)
			l := layout(t, r, text, body, kit.TextOptions{Direction: dir})
			if got := visualOrder(l, text); got != c.want {
				t.Errorf("%q in a %v paragraph reads %q left to right, want %q (Chromium)", c.name, dir, got, c.want)
			}
		}
	}
}

// visualOrder is text's characters as l places them, left to right: ASCII characters as
// themselves, each run of other characters as "R", without spaces and the bidi controls.
func visualOrder(l *textLayout, text string) string {
	type glyph struct {
		c   rune
		x   float32
		pos int
	}
	var gs []glyph
	for pos, c := range text {
		if c == ' ' || 0x200E <= c && c <= 0x200F || 0x202A <= c && c <= 0x202E || 0x2066 <= c && c <= 0x2069 {
			continue
		}
		rs := l.AppendRangeRects(nil, pos, pos+utf8.RuneLen(c))
		if len(rs) == 0 {
			continue
		}
		gs = append(gs, glyph{c, (rs[0].Min.X + rs[0].Max.X) / 2, pos})
	}
	sort.SliceStable(gs, func(i, j int) bool { return gs[i].x < gs[j].x })
	var b strings.Builder
	for _, g := range gs {
		switch {
		case g.c < utf8.RuneSelf:
			b.WriteRune(g.c)
		case !strings.HasSuffix(b.String(), "R"):
			b.WriteByte('R')
		}
	}
	return b.String()
}
