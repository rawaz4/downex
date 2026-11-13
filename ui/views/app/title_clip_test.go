package app_test

import (
	"fmt"
	"math"
	"testing"

	"downex/ui/views/viewstest"
)

// TestPageTitleClipsToItsBox: at 1190 and 1200 px with 20 px text the page title gets a box
// only a few DIPs wide. Toolbar.svelte's .title is overflow: hidden, so the reference shows
// only what fits inside that box and keeps the sp-4 gap before the count clear. Before, the
// native label drew its first letter and the ellipsis past its box, over the gap and the
// count "28".
func TestPageTitleClipsToItsBox(t *testing.T) {
	if err := viewstest.CanRender(); err != nil {
		t.Skip(err)
	}
	for _, c := range []struct{ w, h int }{{1190, 752}, {1200, 760}} {
		for _, scale := range []float32{1, 1.25} {
			for _, theme := range []string{"light", "dark"} {
				t.Run(fmt.Sprintf("%dx%d %vx 20px %s", c.w, c.h, scale, theme), func(t *testing.T) {
					h, m := mainWindow(t, viewstest.Options{Width: c.w, Height: c.h, Scale: scale, Font: 20, Theme: theme, Real: true})
					m.Downloads.Grid().Table().FixRem(16)
					shotTheme(h, theme)
					img, err := h.Render()
					if err != nil {
						t.Fatal(err)
					}
					hd := m.Shell.Header()
					title, count := hd.Title().WindowBounds(), hd.CountText().WindowBounds()
					if !hd.Title().Truncated() {
						t.Skipf("the title is not cut here (%v wide)", title.Width())
					}
					px := func(v float32) int { return int(math.Floor(float64(v * scale))) }
					x0 := int(math.Ceil(float64(title.Max.X * scale)))
					x1 := px(count.Min.X) // the count's box starts here
					y0, y1 := px(title.Min.Y), px(title.Max.Y)
					if x1-x0 < 2 {
						t.Fatalf("no gap between the title %v and the count %v", title, count)
					}
					ground := img.RGBAAt(x0, px(hd.WindowBounds().Min.Y)+1)
					inked := 0
					for y := y0; y < y1; y++ {
						for x := x0; x < x1; x++ {
							if img.RGBAAt(x, y) != ground {
								inked++
							}
						}
					}
					if inked > 0 {
						t.Errorf("%d device px of ink in the gap x %d–%d between the title (%.2f wide) and the count", inked, x0, x1, title.Width())
					}
					t.Logf("title box %.2f DIP wide, gap %d device px clear before the count at %.2f", title.Width(), x1-x0, count.Min.X)
				})
			}
		}
	}
}
