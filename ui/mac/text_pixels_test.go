//go:build darwin && cgo

package mac

import (
	stdimage "image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"downex/ui/kit"
)

// TestWebKitPixels compares the engine's drawing with a WKWebView snapshot of the same
// text and CSS at 2x ($DOWNEX_WK_CAL/wk.png, made by a throwaway program outside the
// repository: four lines at (10, 10), (10, 30), (10, 60) and (10, 80), black on white). It
// logs per line the mean and largest difference of the gray levels, and writes the
// engine's picture beside it (mac.png).
func TestWebKitPixels(t *testing.T) {
	dir := os.Getenv("DOWNEX_WK_CAL")
	if dir == "" {
		t.Skip("DOWNEX_WK_CAL not set")
	}
	f, err := os.Open(filepath.Join(dir, "wk.png"))
	if err != nil {
		t.Skip(err)
	}
	ref, err := png.Decode(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	e := newEngine(t)
	const ppd = 2
	w, h := ref.Bounds().Dx(), ref.Bounds().Dy()
	body := kit.TextStyle{Family: fontUI, Size: 13, LineHeight: 1.4, Tabular: true}
	italic := body
	italic.Italic = true
	lines := []struct {
		text string
		st   kit.TextStyle
		y    float32
		opts kit.TextOptions
	}{
		{"تقرير-الميزانية-٢٠٢٦.xlsx and ڕاپۆرتی-ساڵانە-٢٠٢٥.pdf", body, 100, kit.TextOptions{Direction: kit.DirLTR}},
		{"ڕاپۆرتی-ساڵانە-٢٠٢٥.pdf", body, 120, kit.TextOptions{MaxWidth: 300, Direction: kit.DirRTL}},
		{"Hello 👋🏽 world 😀 é́", body, 140, kit.TextOptions{}},
		{"Italic text 123", italic, 160, kit.TextOptions{}},
		{"Download complete 12:34 — city-timelapse-4k.mp4", kit.TextStyle{Family: fontUI, Size: 13, LineHeight: 1.4, Tabular: true}, 10, kit.TextOptions{}},
		{"All downloads", kit.TextStyle{Family: fontDisplay, Size: 20, Weight: 650, LineHeight: 1.2, LetterSpacing: -0.02, Tabular: true}, 30, kit.TextOptions{}},
		{"https://media.example.org/videos/2026/x.mp4", kit.TextStyle{Family: fontMono, Size: 12, Weight: 350, LineHeight: 1.4, LetterSpacing: -0.01, Tabular: true}, 60, kit.TextOptions{}},
		{"QUEUES", kit.TextStyle{Family: fontUI, Size: 10.5, Weight: 600, LetterSpacing: 0.07, Tabular: true}, 80, kit.TextOptions{}},
	}
	out := stdimage.NewRGBA(stdimage.Rect(0, 0, w, h))
	for i := range out.Pix {
		out.Pix[i] = 255
	}
	for _, ln := range lines {
		l := lay(t, e, ln.text, ln.st, ln.opts)
		px := renderText(l, w, h, ppd, kit.Pt(10, ln.y), kit.RGB(0), kit.RGB(0xffffff))
		y0, y1 := int(ln.y*ppd), int((ln.y+l.Metrics().Height)*ppd)
		var sum, n, worst, inkA, inkB int
		first := -1
		for y := y0; y < y1 && y < h; y++ {
			for x := 0; x < w; x++ {
				if r, _, _, _ := ref.At(x, y).RGBA(); int(r>>8) != int(px[4*(y*w+x)]) && (first < 0 || x < first) {
					first = x
				}
				a := int(px[4*(y*w+x)])
				r, _, _, _ := ref.At(x, y).RGBA()
				b := int(r >> 8)
				d := a - b
				if d < 0 {
					d = -d
				}
				sum += d
				n++
				worst = max(worst, d)
				inkA += 255 - a
				inkB += 255 - b
				copy(out.Pix[4*(y*w+x):], px[4*(y*w+x):4*(y*w+x)+4])
			}
		}
		t.Logf("%-50q first different column %d (DIP %.1f), mean |Δ| %.2f, largest %d, ink %d vs WebKit %d (%+.1f%%)", ln.text, first, float32(first)/ppd-10, float64(sum)/float64(n), worst, inkA, inkB, 100*float64(inkA-inkB)/float64(inkB))
	}
	g, err := os.Create(filepath.Join(dir, "mac.png"))
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if err := png.Encode(g, out); err != nil {
		t.Fatal(err)
	}
}
