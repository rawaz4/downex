//go:build darwin && cgo

package viewstest_test

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"downex/ui/views/app"
	"downex/ui/views/viewstest"
)

// The main window renders offscreen through ui/mac on this Mac (no references needed): the
// whole window at 1× and 2× in both themes, with ink in it (not one flat colour), the
// light theme lighter than the dark one. $DOWNEX_VIEWS_SHOTS keeps the PNGs.
func TestMacRenderMainWindow(t *testing.T) {
	if err := viewstest.CanRender(); err != nil {
		t.Fatalf("CanRender on darwin: %v", err)
	}
	mean := map[string]float64{}
	for _, theme := range []string{"light", "dark"} {
		for _, scale := range []float32{1, 2} {
			o := viewstest.Options{Width: 1200, Height: 760, Scale: scale, Theme: theme, Real: true}
			h := viewstest.New(t, o)
			m, err := app.New(h.App, app.Options{})
			if err != nil {
				t.Fatal(err)
			}
			h.Show(m.Root())
			img, err := h.Render()
			m.Dispose()
			h.Close()
			if err != nil {
				t.Fatalf("%s %v: %v", theme, scale, err)
			}
			b := img.Bounds()
			if b.Dx() != int(1200*scale) || b.Dy() != int(760*scale) {
				t.Errorf("%s %v: %v", theme, scale, b)
			}
			colours := map[[3]uint8]bool{}
			sum := 0.0
			for y := b.Min.Y; y < b.Max.Y; y += 3 {
				for x := b.Min.X; x < b.Max.X; x += 3 {
					c := img.RGBAAt(x, y)
					colours[[3]uint8{c.R, c.G, c.B}] = true
					sum += float64(c.R) + float64(c.G) + float64(c.B)
				}
			}
			if len(colours) < 50 {
				t.Errorf("%s %v: only %d colours drawn", theme, scale, len(colours))
			}
			if scale == 1 {
				mean[theme] = sum
			}
			if dir := os.Getenv("DOWNEX_VIEWS_SHOTS"); dir != "" {
				f, err := os.Create(filepath.Join(dir, "mac-main-"+theme+"-"+map[float32]string{1: "1x", 2: "2x"}[scale]+".png"))
				if err == nil {
					_ = png.Encode(f, img)
					f.Close()
				}
			}
		}
	}
	if !(mean["light"] > mean["dark"]) {
		t.Errorf("light %v is not lighter than dark %v", mean["light"], mean["dark"])
	}
}
