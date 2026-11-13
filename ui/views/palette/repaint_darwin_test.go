//go:build darwin && cgo

package palette_test

import (
	"math"
	"testing"

	"downex/ui/kit"
	"downex/ui/mac"
	"downex/ui/views/palette"
	"downex/ui/views/viewstest"
)

// TestShrinkRepaintsWhatItLeft [e2e]: a window paints only what was invalidated, over the
// pixels of its last frame. When the palette shrinks (every command, then "pause": five
// rows), the part of the screen it covered before, its shadow included, must be painted
// again: the real Mac window showed the old palette's shadow below the new one until
// something else repainted there. The frame painted incrementally must equal a full paint.
func TestShrinkRepaintsWhatItLeft(t *testing.T) {
	if err := viewstest.CanRender(); err != nil {
		t.Skipf("no offscreen renderer: %v", err)
	}
	for _, theme := range []string{"dark", "light"} {
		t.Run(theme, func(t *testing.T) {
			const scale = 2
			h := viewstest.New(t, viewstest.Options{Width: 1000, Height: 700, Scale: scale, Theme: theme, Fixture: "palette", Real: true})
			// The Mac window's environment (ui/mac window.go readEnvironment).
			h.Host.Host.Env.ReducedTransparency = true
			h.Loop.Send(kit.EnvironmentEvent{Env: h.Host.Host.Env})
			p := palette.New(h.App, palette.Options{})
			h.App.Dialogs.Show(p)
			h.Settle()
			res, _, err := viewstest.RealResources()
			if err != nil {
				t.Fatal(err)
			}
			size := h.Window.Size()
			o, err := mac.NewOffscreen(res.(*mac.Resources), int(math.Ceil(float64(size.W*scale))), int(math.Ceil(float64(size.H*scale))), scale, mac.OffscreenOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer o.Close()
			paint := func(r kit.Rect) {
				c, err := o.BeginFrame(r)
				if err != nil {
					t.Fatal(err)
				}
				h.Window.Paint(c, r)
				if err := o.EndFrame(); err != nil {
					t.Fatal(err)
				}
			}
			paint(kit.RectAt(kit.Point{}, size))
			before := p.Card().WindowBounds()
			h.Host.Invalidations = nil
			typeQuery(h, p, "pause")
			after := p.Card().WindowBounds()
			if after.Height() >= before.Height() {
				t.Fatalf("the palette did not shrink: %v -> %v", before, after)
			}
			var dirty kit.Rect
			for _, r := range h.Host.Invalidations {
				dirty = dirty.Union(r)
			}
			paint(dirty)
			got, err := o.RGBA()
			if err != nil {
				t.Fatal(err)
			}
			want, err := h.Render()
			if err != nil {
				t.Fatal(err)
			}
			n, bounds := 0, kit.Rect{}
			for y := 0; y < want.Bounds().Dy(); y++ {
				for x := 0; x < want.Bounds().Dx(); x++ {
					// A shadow's mask is made once per size and stretched (ui/mac shadow.go): a
					// level or two apart from a fresh one is the same picture.
					if a, b := got.RGBAAt(x, y), want.RGBAAt(x, y); far(a.R, b.R) || far(a.G, b.G) || far(a.B, b.B) {
						n++
						px := kit.R(float32(x)/scale, float32(y)/scale, float32(x+1)/scale, float32(y+1)/scale)
						bounds = bounds.Union(px)
					}
				}
			}
			if n > 0 {
				t.Errorf("%d pixels differ from a full paint, in %v (palette %v -> %v, painted %v)", n, bounds, before, after, dirty)
			}
		})
	}
}

// far reports channel values more than 2 levels apart.
func far(a, b uint8) bool { return int(a) > int(b)+2 || int(b) > int(a)+2 }
