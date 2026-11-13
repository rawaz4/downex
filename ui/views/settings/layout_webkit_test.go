package settings

// [mac webkit] WebKit's column balancing, against WKWebView's layout of the same blocks
// (columns: 2; break-inside: avoid; margin-block-end: 24px; macOS 26, 2026-11-10).

import (
	"slices"
	"testing"

	"downex/ui/kit"
)

func TestWebKitColumns(t *testing.T) {
	type at struct {
		col int
		top kit.DIP
	}
	for _, c := range []struct {
		hs   []kit.DIP
		H    kit.DIP
		want []at
	}{
		{[]kit.DIP{359, 118, 328}, 501, []at{{0, 0}, {0, 383}, {1, 24}}},
		{[]kit.DIP{359, 118, 300}, 501, []at{{0, 0}, {0, 383}, {1, 24}}},
		{[]kit.DIP{300, 118, 328}, 442, []at{{0, 0}, {0, 324}, {1, 24}}},
		{[]kit.DIP{200, 200, 200}, 424, []at{{0, 0}, {0, 224}, {1, 24}}},
		{[]kit.DIP{359, 60, 328}, 443, []at{{0, 0}, {0, 383}, {1, 24}}},
		{[]kit.DIP{100, 300, 100}, 424, []at{{0, 0}, {0, 124}, {1, 24}}},
		{[]kit.DIP{50, 50, 400}, 548, []at{{0, 0}, {0, 74}, {0, 148}}},
		{[]kit.DIP{400, 50, 50}, 400, []at{{0, 0}, {1, 24}, {1, 98}}},
		{[]kit.DIP{359, 118, 328, 40}, 501, []at{{0, 0}, {0, 383}, {1, 24}, {1, 376}}},
		{[]kit.DIP{200, 100, 100, 200}, 348, []at{{0, 0}, {0, 224}, {1, 0}, {1, 124}}},
	} {
		cols, tops, H := webkitColumns(c.hs, 24, 2, nil, nil)
		var got []at
		for i := range cols {
			got = append(got, at{cols[i], tops[i]})
		}
		if H != c.H || !slices.Equal(got, c.want) {
			t.Errorf("%v: height %v %v, want %v %v", c.hs, H, got, c.H, c.want)
		}
	}
}
