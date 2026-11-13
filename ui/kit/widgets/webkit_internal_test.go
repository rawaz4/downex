package widgets

// [mac webkit] The colour rules of macOS's check box as WebKit draws it (webkit.go).

import (
	"math"
	"testing"

	"downex/ui/kit"
)

func hexOf(c kit.Color) uint32 {
	b := func(v float32) uint32 { return uint32(math.Round(float64(v) * 255)) }
	return b(c.R)<<16 | b(c.G)<<8 | b(c.B)
}

// The fills and marks measured on WKWebView snapshots (macOS 26, 2026-11-10) of
// <input type=checkbox checked> with accent-color: the fill is the accent scaled in linear
// light to a relative luminance of 0.5 (at most in light, at least in dark when it fits),
// and the mark is black on a fill lighter than that.
func TestWebKitCheckColours(t *testing.T) {
	for _, c := range []struct {
		accent     uint32
		dark       bool
		fill, mark uint32
	}{
		{0x10804f, false, 0x10804f, 0xffffff}, // the app's light --accent
		{0x3ccb8b, true, 0x3fd491, 0xffffff},  // the app's dark --accent
		{0x10804f, true, 0x22d687, 0xffffff},
		{0x808080, true, 0xbcbcbc, 0xffffff},
		{0x6fdcaa, false, 0x68cfa0, 0xffffff},
		{0x6fdcaa, true, 0x6fdcaa, 0x000000},
		{0xffffff, false, 0xbcbcbc, 0xffffff},
		{0xffff00, false, 0xc2c200, 0xffffff},
		{0x00ff00, false, 0x00da00, 0xffffff},
		{0xff0000, true, 0xff0000, 0xffffff}, // a red cannot reach 0.5: kept
		{0x0000ff, true, 0x0000ff, 0xffffff},
	} {
		fill := webkitCheckFill(kit.RGB(c.accent), c.dark)
		if got := hexOf(fill); absDiff(got, c.fill) > 1 {
			t.Errorf("accent %06x dark %v: fill %06x, want %06x", c.accent, c.dark, got, c.fill)
		}
		if got := hexOf(webkitCheckMark(fill)); got != c.mark {
			t.Errorf("accent %06x dark %v: mark %06x, want %06x", c.accent, c.dark, got, c.mark)
		}
	}
}

// absDiff is the largest difference of the three channels.
func absDiff(a, b uint32) uint32 {
	var m uint32
	for s := 0; s < 24; s += 8 {
		x, y := a>>s&0xff, b>>s&0xff
		d := x - y
		if y > x {
			d = y - x
		}
		m = max(m, d)
	}
	return m
}
