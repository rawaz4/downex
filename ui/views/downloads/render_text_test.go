package downloads

import (
	"fmt"
	"image"
	"image/draw"
	"math"
	"testing"

	"downex/ui/core/format"
	"downex/ui/core/locale"
	"downex/ui/kit"
	"downex/ui/views/viewstest"
)

// inkExtent is the horizontal extent, in device pixels, of what is drawn in area (window
// DIPs) of img: the columns holding a pixel that differs from the area's most common colour
// (its background) by more than 96 over the three channels. ok is false when nothing is drawn.
func inkExtent(img *image.RGBA, area kit.Rect, scale float32) (x0, x1 int, ok bool) {
	px := func(v kit.DIP) int { return int(math.Round(float64(v * kit.DIP(scale)))) }
	r := image.Rect(px(area.Min.X), px(area.Min.Y), px(area.Max.X), px(area.Max.Y)).Intersect(img.Bounds())
	if r.Empty() {
		return 0, 0, false
	}
	count := map[[3]uint8]int{}
	var bg [3]uint8
	best := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			o := img.PixOffset(x, y)
			c := [3]uint8{img.Pix[o], img.Pix[o+1], img.Pix[o+2]}
			count[c]++
			if count[c] > best {
				best, bg = count[c], c
			}
		}
	}
	abs := func(a, b uint8) int {
		if a > b {
			return int(a - b)
		}
		return int(b - a)
	}
	x0, x1 = r.Max.X, r.Min.X-1
	for x := r.Min.X; x < r.Max.X; x++ {
		for y := r.Min.Y; y < r.Max.Y; y++ {
			o := img.PixOffset(x, y)
			if abs(img.Pix[o], bg[0])+abs(img.Pix[o+1], bg[1])+abs(img.Pix[o+2], bg[2]) > 96 {
				x0, x1 = min(x0, x), max(x1, x)
				break
			}
		}
	}
	return x0, x1, x1 >= x0
}

// optionsFor is the harness at a reference's size, scale, text size and theme, with real text.
func optionsFor(s *viewstest.Shot) viewstest.Options {
	return viewstest.Options{Width: s.CSS.W, Height: s.CSS.H, Scale: float32(s.Scale), Font: s.Font, Theme: s.Theme, Real: true}
}

// toRGBA copies img into an RGBA image with the same bounds.
func toRGBA(img image.Image) *image.RGBA {
	if r, ok := img.(*image.RGBA); ok {
		return r
	}
	out := image.NewRGBA(img.Bounds())
	draw.Draw(out, out.Bounds(), img, img.Bounds().Min, draw.Src)
	return out
}

// labels are the header's shown column labels.
func (h *header) labels() []*kit.Label {
	var out []*kit.Label
	for _, x := range h.ths {
		if x.btn != nil && x.btn.label != nil && !x.Bounds().Empty() {
			out = append(out, x.btn.label)
		}
	}
	return out
}

// TestRenderTextWidths measures the text the grid draws against the references: the ink
// extent of every fully shown row's name, site line, size, status and Added text, and of
// the header's labels, in the reference image and the native render at every main
// reference's size, scale, text size and theme. The references have no boxes for the text
// inside the grid (layout.nodes stops at div.body), so this measures the images. The live
// values (percent, speed, time left) are left out: the scene's simulation ticks between
// the references' captures, so they hold other numbers.
//
// The gate: a text's width within 1% of the reference's, or 1 device pixel where 1% is
// less than a pixel; its start within 1 DIP.
func TestRenderTextWidths(t *testing.T) {
	refs, shots := mainShots(t)
	if f, err := locale.New("en-GB", "Asia/Baghdad"); err == nil {
		prev := format.Locale()
		format.SetLocale(f)
		t.Cleanup(func() { format.SetLocale(prev) })
	}
	var total, worstN, overPct, exact int
	var worstPct, worstStart float64
	for _, s := range shots {
		name := fmt.Sprintf("%dx%d-%vx-%dpx-%s", s.CSS.W, s.CSS.H, s.Scale, s.Font, s.Theme)
		t.Run(name, func(t *testing.T) {
			box, ok := wrapBox(s)
			if !ok {
				t.Skip("no div.wrap")
			}
			o := optionsFor(s)
			r := newRig(t, o, box, 0)
			if s.Font != 16 {
				r.table.FixRem(16)
				r.Settle()
			}
			img, err := r.Render()
			if err != nil {
				t.Fatal(err)
			}
			refImg, err := refs.Image(s)
			if err != nil {
				t.Fatal(err)
			}
			ref := toRGBA(refImg)
			scale := float32(s.Scale)
			body := r.table.BodyBounds()
			type probe struct {
				what string
				l    *kit.Label
			}
			var probes []probe
			for _, c := range r.table.header.labels() {
				probes = append(probes, probe{"header " + c.Text(), c})
			}
			for _, id := range r.table.model.ids {
				rw := r.row(id)
				if rw == nil {
					continue
				}
				wb := rw.WindowBounds()
				// body is the list's whole content; only the rows the view shows whole count,
				// above the bottom 16 DIP a horizontal scroll bar takes at 20 px text
				if wb.Min.Y < body.Min.Y || wb.Max.Y > min(body.Max.Y, box.Max.Y-16) {
					continue
				}
				for _, p := range []probe{{"name", rw.name}, {"site", rw.sub}, {"size", rw.size}, {"status", rw.status}, {"added", rw.added}} {
					if p.l != nil && p.l.Text() != "" && !p.l.Bounds().Empty() {
						probes = append(probes, probe{fmt.Sprintf("row %d %s %q", id, p.what, p.l.Text()), p.l})
					}
				}
			}
			var bad, n int
			var maxPct, maxStart float64
			for _, p := range probes {
				a := p.l.WindowBounds()
				if a.Width() <= 0 || a.Max.X > box.Max.X-16 {
					continue // cut by the window's end or under the scroll bar
				}
				a = kit.R(a.Min.X-1, a.Min.Y, a.Max.X+1, a.Max.Y)
				r0, r1, rok := inkExtent(ref, a, scale)
				n0, n1, nok := inkExtent(img, a, scale)
				if !rok || !nok {
					if rok != nok {
						t.Errorf("%s: drawn in the reference %v, natively %v", p.what, rok, nok)
						bad++
					}
					continue
				}
				n++
				rw, nw := float64(r1-r0+1), float64(n1-n0+1)
				pct := math.Abs(nw-rw) / rw * 100
				start := math.Abs(float64(n0-r0)) / float64(scale)
				maxPct, maxStart = math.Max(maxPct, pct), math.Max(maxStart, start)
				if pct > 1 {
					overPct++ // by one device pixel at most, or the gate below fails
				}
				if nw == rw {
					exact++
				}
				if (pct > 1 && math.Abs(nw-rw) > 1) || start > 1 {
					bad++
					t.Errorf("%s: width %.0f px, reference %.0f (%.1f%%), start %+.1f DIP", p.what, nw, rw, pct, float64(n0-r0)/float64(scale))
				}
			}
			t.Logf("%d texts measured: widest width difference %.1f%%, start %.1f DIP; %d outside the gate", n, maxPct, maxStart, bad)
			total += n
			if maxPct > worstPct {
				worstPct, worstN = maxPct, n
			}
			worstStart = math.Max(worstStart, maxStart)
		})
	}
	t.Logf("all shots: %d texts, %d as wide as the reference, %d more than 1%% off (by 1 device px); widest difference %.1f%% (in a shot of %d texts), start %.1f DIP", total, exact, overPct, worstPct, worstN, worstStart)
}
