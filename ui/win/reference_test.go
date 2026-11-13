//go:build windows

package win

import (
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"

	"downex/ui/kit"
)

// TestReferenceTextWidths compares the ink width of texts of the 0.9.2 main window (the
// WebView2 reference shots at 100% and 125%, plan step 1) with this backend's, drawn at the
// same scale, and fails when one is off by more than 2% (the plan's gate). It needs
// $DOWNEX_REF_092, the reference folder (ref-0.9.2).
func TestReferenceTextWidths(t *testing.T) {
	dir := os.Getenv("DOWNEX_REF_092")
	if dir == "" {
		t.Skip("set DOWNEX_REF_092 to the 0.9.2 reference folder to compare text widths")
	}
	ink := func(img interface{ At(x, y int) color.Color }, x0, x1, y0, y1 int) int {
		l, r := -1, -1
		for x := x0; x < x1; x++ {
			for y := y0; y < y1; y++ {
				if c := color.GrayModel.Convert(img.At(x, y)).(color.Gray); c.Y < 170 {
					if l < 0 {
						l = x
					}
					r = x
					break
				}
			}
		}
		if l < 0 {
			return 0
		}
		return r - l + 1
	}
	title := kit.TextStyle{Family: fontDisplay, Size: 20, Weight: 650, LetterSpacing: -0.02, LineHeight: 1.2, Tabular: true}
	name := kit.TextStyle{Family: fontUI, Size: 13, Weight: 500, LineHeight: 1.4, Tabular: true}
	item := kit.TextStyle{Family: fontUI, Size: 13, Weight: 400, LineHeight: 1.4, Tabular: true}
	samples := []struct {
		text           string
		style          kit.TextStyle
		x0, x1, y0, y1 int // where the 100% reference shot has it
	}{
		{"All downloads", title, 240, 372, 62, 90},
		{"os-image-26.04-amd64.iso", name, 280, 470, 139, 157},
		{"telemetry-183.dat", name, 280, 470, 231, 249},
		{"city-timelapse-4k.mp4", name, 280, 470, 323, 341},
		{"annual-report-2025.pdf", name, 280, 470, 415, 433},
		{"Documents", item, 44, 150, 207, 224},
		{"Programs", item, 44, 150, 237, 254},
	}
	r := newRes(t)
	for _, sc := range []struct {
		file string
		ppd  float32
	}{{"main__1200x760__1x__16px__light.png", 1}, {"main__1200x760__1.25x__16px__light.png", 1.25}} {
		f, err := os.Open(filepath.Join(dir, "screens", "main", sc.file))
		if err != nil {
			t.Skip(err)
		}
		ref, err := png.Decode(f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		k := float64(sc.ppd)
		w, h := int(math.Ceil(260*k)), int(math.Ceil(40*k))
		for _, s := range samples {
			o := newOffscreen(t, r, w, h, sc.ppd)
			l := layout(t, r, s.text, s.style, kit.TextOptions{})
			frame(t, o, func(c kit.Canvas) {
				c.FillRect(o.Bounds(), surface)
				c.DrawText(l, kit.Pt(10, 6), ink0)
			})
			got := ink(pixels(t, o), 0, w, 0, h)
			want := ink(ref, int(float64(s.x0)*k), int(math.Ceil(float64(s.x1)*k)), int(float64(s.y0)*k)-2, int(math.Ceil(float64(s.y1)*k))+2)
			d := float64(got-want) / float64(want)
			t.Logf("%.2fx %-26q reference %3d px, ours %3d (%+.1f%%)", k, s.text, want, got, 100*d)
			if d < -0.02 || d > 0.02 {
				t.Errorf("%.2fx %q: %d px against the reference's %d", k, s.text, got, want)
			}
		}
	}
}

var ink0 = kit.RGB(0x0f1613)
