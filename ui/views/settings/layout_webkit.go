package settings

// [mac webkit] CSS multi-column balancing as WebKit does it (Integration's `.page.cols`,
// under kit.Environment.WebKit only; the Windows window keeps Chromium's split in columns).
// Measured in WKWebView (macOS 26, 2026-11-10) on blocks with break-inside: avoid and a 24 px
// bottom margin: the column height starts at the whole flow (margins and the last one
// included) over the column count; the blocks go down a column while they fit, a block
// that does not is pushed to the next column (which starts with what the margin before it
// left past the column's end), and when the flow does not fit the height grows by the least
// shortfall a push or an overflowing block recorded in a column other than the last, and the
// flow is laid out again. The page is the column height high. So 359, 118 and 328 px blocks
// make one 501 px column of the first two (WebKit's Integration page) where the least height
// would put the last two together; 50, 50 and 400 make one 548 px column.

import (
	"math"

	"downex/ui/kit"
)

// webkitColumns lays the blocks out: each one's column and top, and the column height.
func webkitColumns(heights []kit.DIP, mg kit.DIP, n int, cols []int, tops []kit.DIP) ([]int, []kit.DIP, kit.DIP) {
	cols, tops = cols[:0], tops[:0]
	for range heights {
		cols, tops = append(cols, 0), append(tops, 0)
	}
	if len(heights) == 0 {
		return cols, tops, 0
	}
	var total float64
	for _, h := range heights {
		total += float64(h + mg)
	}
	H := total / float64(max(n, 1))
	for range 64 {
		col, y := 0, 0.0
		short := math.Inf(1)
		fits := true
		for i, hd := range heights {
			h := float64(hd)
			if y > 0 && y+h > H+1e-6 {
				if col+1 < n {
					short = min(short, y+h-H)
					col++
					y = max(0, y-H)
				} else {
					fits = false
				}
			}
			if y+h > H+1e-6 && col+1 < n { // a block taller than a whole column
				short = min(short, y+h-H)
				fits = false
			}
			cols[i], tops[i] = col, kit.DIP(y)
			y += h + float64(mg)
		}
		if y-float64(mg) > H+1e-6 {
			fits = false
		}
		if fits || math.IsInf(short, 1) {
			return cols, tops, kit.DIP(H)
		}
		H += short
	}
	return cols, tops, kit.DIP(H)
}
