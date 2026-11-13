package viewstest

import (
	"image"
	"image/color"
	"math"
	"math/rand/v2"
	"testing"

	"downex/ui/kit"
)

// Compare and BestShift read the images' bytes; they give what reading a colour per pixel
// gave (the oracles below are the earlier per-pixel code), for a decoded reference (NRGBA)
// against a render (RGBA), with crops that leave the images.
func TestCompareAndBestShiftMatchThePerPixelOracle(t *testing.T) {
	rnd := rand.New(rand.NewPCG(1, 2))
	ref := image.NewNRGBA(image.Rect(0, 0, 90, 60))
	native := image.NewRGBA(image.Rect(0, 0, 90, 60))
	for y := 0; y < 60; y++ {
		for x := 0; x < 90; x++ {
			v := uint8((x*7 + y*3) % 256)
			ref.SetNRGBA(x, y, color.NRGBA{v, uint8(rnd.IntN(256)), 255 - v, 255})
		}
	}
	for y := 0; y < 60; y++ {
		for x := 0; x < 90; x++ {
			c := ref.NRGBAAt((x+1)%90, (y+2)%60) // the reference moved by (-1, -2), plus noise
			native.SetRGBA(x, y, color.RGBA{c.R, uint8(min(int(c.G)+rnd.IntN(9), 255)), c.B, 255})
		}
	}
	for _, c := range []struct {
		area  kit.Rect
		at    kit.Point
		scale float32
	}{
		{kit.R(10, 10, 70, 50), kit.Pt(10, 10), 1},
		{kit.R(0, 0, 90, 60), kit.Pt(4, -3), 1},
		{kit.R(8, 8, 56, 40), kit.Pt(9, 6), 1.25},
	} {
		got := Compare(ref, native, c.area, c.at, c.scale)
		want := compareOracle(ref, native, c.area, c.at, c.scale)
		if math.Abs(got.Mean-want.Mean) > 1e-9 || got.Differing != want.Differing || !equalPix(got.Sheet, want.Sheet) {
			t.Errorf("%v: Compare mean %v differing %v, oracle %v %v (sheets equal %v)", c.area, got.Mean, got.Differing, want.Mean, want.Differing, equalPix(got.Sheet, want.Sheet))
		}
		dx, dy, b, u := BestShift(ref, native, c.area, c.at, c.scale, 3)
		wdx, wdy, wb, wu := bestShiftOracle(ref, native, c.area, c.at, c.scale, 3)
		if dx != wdx || dy != wdy || math.Abs(b-wb) > 1e-9 || math.Abs(u-wu) > 1e-9 {
			t.Errorf("%v: BestShift %d,%d %v %v, oracle %d,%d %v %v", c.area, dx, dy, b, u, wdx, wdy, wb, wu)
		}
	}
	if dx, dy, _, _ := BestShift(ref, native, kit.R(10, 10, 70, 50), kit.Pt(10, 10), 1, 3); dx != -1 || dy != -2 {
		t.Errorf("BestShift found %d,%d, want -1,-2", dx, dy)
	}
}

func equalPix(a, b *image.RGBA) bool {
	if a.Bounds() != b.Bounds() {
		return false
	}
	for i := range a.Pix {
		if a.Pix[i] != b.Pix[i] {
			return false
		}
	}
	return true
}

func compareOracle(ref, native image.Image, refArea kit.Rect, nativeAt kit.Point, scale float32) Comparison {
	c := Compare(ref, native, refArea, nativeAt, scale) // the sheet's first two panels
	px := func(v kit.DIP) int { return int(math.Round(float64(v * scale))) }
	r := image.Rect(px(refArea.Min.X), px(refArea.Min.Y), px(refArea.Max.X), px(refArea.Max.Y)).Intersect(ref.Bounds())
	off := image.Pt(px(nativeAt.X), px(nativeAt.Y)).Sub(image.Pt(px(refArea.Min.X), px(refArea.Min.Y)))
	n := r.Add(off).Intersect(native.Bounds())
	r = n.Sub(off)
	w, h := r.Dx(), r.Dy()
	const gap = 8
	sheet := image.NewRGBA(c.Sheet.Bounds())
	copy(sheet.Pix, c.Sheet.Pix)
	out := Comparison{Sheet: sheet, Threshold: 24}
	var sum float64
	var differing int
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			a := color.RGBAModel.Convert(ref.At(r.Min.X+x, r.Min.Y+y)).(color.RGBA)
			b := color.RGBAModel.Convert(native.At(n.Min.X+x, n.Min.Y+y)).(color.RGBA)
			dr, dg, db := absDiff(a.R, b.R), absDiff(a.G, b.G), absDiff(a.B, b.B)
			sum += float64(dr) + float64(dg) + float64(db)
			if max(dr, dg, db) > out.Threshold {
				differing++
			}
			amp := func(v uint8) uint8 { return uint8(min(int(v)*4, 255)) }
			sheet.SetRGBA(2*w+2*gap+x, y, color.RGBA{amp(dr), amp(dg), amp(db), 255})
		}
	}
	if n := w * h; n > 0 {
		out.Mean = sum / float64(3*n)
		out.Differing = float64(differing) / float64(n)
	}
	return out
}

func bestShiftOracle(ref, native image.Image, refArea kit.Rect, nativeAt kit.Point, scale float32, maxPx int) (dx, dy int, best, unshifted float64) {
	px := func(v kit.DIP) int { return int(math.Round(float64(v * scale))) }
	r := image.Rect(px(refArea.Min.X), px(refArea.Min.Y), px(refArea.Max.X), px(refArea.Max.Y))
	at := image.Pt(px(nativeAt.X), px(nativeAt.Y))
	lum := func(img image.Image, x, y int) float64 {
		if !(image.Point{x, y}.In(img.Bounds())) {
			return -1
		}
		return float64(color.GrayModel.Convert(img.At(x, y)).(color.Gray).Y)
	}
	measure := func(sx, sy int) float64 {
		var sum float64
		var n int
		for y := maxPx; y < r.Dy()-maxPx; y++ {
			for x := maxPx; x < r.Dx()-maxPx; x++ {
				a := lum(ref, r.Min.X+x, r.Min.Y+y)
				b := lum(native, at.X+x+sx, at.Y+y+sy)
				if a < 0 || b < 0 {
					continue
				}
				sum += math.Abs(a - b)
				n++
			}
		}
		if n == 0 {
			return math.Inf(1)
		}
		return sum / float64(n)
	}
	unshifted = measure(0, 0)
	best = unshifted
	for sy := -maxPx; sy <= maxPx; sy++ {
		for sx := -maxPx; sx <= maxPx; sx++ {
			if m := measure(sx, sy); m < best {
				best, dx, dy = m, sx, sy
			}
		}
	}
	return dx, dy, best, unshifted
}

// A whole-window comparison at 1200 x 760 and 1.25x, and a 3-pixel shift search over a
// dialog's header (oracle: the per-pixel code; CompareOracle runs a Compare first, for the sheet).
func BenchmarkCompare(b *testing.B) {
	ref := image.NewNRGBA(image.Rect(0, 0, 1500, 950))
	native := image.NewRGBA(image.Rect(0, 0, 1500, 950))
	for i := range ref.Pix {
		ref.Pix[i], native.Pix[i] = uint8(i), uint8(i*3)
	}
	area := kit.R(0, 0, 1200, 760)
	b.Run("Compare", func(b *testing.B) {
		for b.Loop() {
			Compare(ref, native, area, kit.Point{}, 1.25)
		}
	})
	b.Run("CompareOracle", func(b *testing.B) {
		for b.Loop() {
			compareOracle(ref, native, area, kit.Point{}, 1.25)
		}
	})
	head := kit.R(392, 294, 808, 360)
	b.Run("BestShift", func(b *testing.B) {
		for b.Loop() {
			BestShift(ref, native, head, head.Min, 1.25, 3)
		}
	})
	b.Run("BestShiftOracle", func(b *testing.B) {
		for b.Loop() {
			bestShiftOracle(ref, native, head, head.Min, 1.25, 3)
		}
	})
}
