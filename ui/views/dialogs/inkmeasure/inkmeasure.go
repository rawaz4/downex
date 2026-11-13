// Package inkmeasure measures where text is drawn in two renders of the same screen, for the
// parity tests of the dialogs and the palette: the 0.9.2 references carry element boxes for
// the dialogs only as the dialog's own box, so each native text's ink is found in its box
// and in the same box of the reference, and their ink boxes are compared (position in DIPs,
// width as a ratio). It is test support with no part in the window.
package inkmeasure

import (
	"image"
	"image/color"
	"math"
	"strings"

	"downex/ui/kit"
)

// Ink is the ink found in an area, in DIPs.
type Ink struct {
	// Box is the ink's bounding box; CX, CY its centroid, weighted by contrast.
	Box    kit.Rect
	CX, CY float64
	Found  bool
}

// Threshold is how far (0–255 luminance) from the area's ground a pixel is ink.
const Threshold = 56

func lum(img image.Image, x, y int) int {
	return int(color.GrayModel.Convert(img.At(x, y)).(color.Gray).Y)
}

// Measure finds the ink in area (DIPs, scale device pixels a DIP). The ground is the most
// common luminance on the area's edge.
func Measure(img image.Image, area kit.Rect, scale float32) Ink {
	s := float64(scale)
	r := image.Rect(int(math.Floor(float64(area.Min.X)*s)), int(math.Floor(float64(area.Min.Y)*s)),
		int(math.Ceil(float64(area.Max.X)*s)), int(math.Ceil(float64(area.Max.Y)*s))).Intersect(img.Bounds())
	if r.Dx() < 2 || r.Dy() < 2 {
		return Ink{}
	}
	var hist [64]int
	edge := func(x, y int) { hist[lum(img, x, y)/4]++ }
	for x := r.Min.X; x < r.Max.X; x++ {
		edge(x, r.Min.Y)
		edge(x, r.Max.Y-1)
	}
	for y := r.Min.Y; y < r.Max.Y; y++ {
		edge(r.Min.X, y)
		edge(r.Max.X-1, y)
	}
	mode := 0
	for i, n := range hist {
		if n > hist[mode] {
			mode = i
		}
	}
	ground := mode*4 + 2
	x0, y0, x1, y1 := r.Max.X, r.Max.Y, r.Min.X, r.Min.Y
	var sw, sx, sy float64
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			d := lum(img, x, y) - ground
			if d < 0 {
				d = -d
			}
			if d <= Threshold {
				continue
			}
			x0, y0, x1, y1 = min(x0, x), min(y0, y), max(x1, x+1), max(y1, y+1)
			w := float64(d)
			sw += w
			sx += w * (float64(x) + 0.5)
			sy += w * (float64(y) + 0.5)
		}
	}
	if sw == 0 {
		return Ink{}
	}
	return Ink{
		Box: kit.R(kit.DIP(float64(x0)/s), kit.DIP(float64(y0)/s), kit.DIP(float64(x1)/s), kit.DIP(float64(y1)/s)),
		CX:  sx / sw / s, CY: sy / sw / s, Found: true,
	}
}

// Delta is a text's ink in the native render against the reference's.
type Delta struct {
	Name string
	// DX, DY are the native ink box's start and the centroid's height less the
	// reference's (DIPs); Width the native ink width over the reference's (1: the same).
	DX, DY, DW float64
	Width      float64
	Found      bool
}

// Compare measures one text: its native box, and where it is in the reference (the box less
// the native dialog's origin plus the reference's).
func Compare(name string, ref, native image.Image, nativeBox kit.Rect, refOffset kit.Point, scale float32) Delta {
	n := Measure(native, nativeBox, scale)
	refBox := kit.R(nativeBox.Min.X+refOffset.X, nativeBox.Min.Y+refOffset.Y, nativeBox.Max.X+refOffset.X, nativeBox.Max.Y+refOffset.Y)
	r := Measure(ref, refBox, scale)
	d := Delta{Name: name, Found: n.Found && r.Found}
	if !d.Found {
		return d
	}
	d.DX = float64(n.Box.Min.X - refOffset.X - r.Box.Min.X)
	d.DY = n.CY - float64(refOffset.Y) - r.CY
	nw, rw := float64(n.Box.Width()), float64(r.Box.Width())
	d.DW = nw - rw
	if rw > 0 {
		d.Width = nw / rw
	}
	return d
}

// texter is a widget that shows a text (kit.Label, views.Text, textrun.Line).
type texter interface{ Text() string }

// Texts measures every text drawn under root whose box lies inside clip against the
// reference, where box is at refBox (box: root's box, or the box root is placed in). Texts
// under skip (a scrolled body, measured on its own with its view as clip) are left out.
func Texts(ref, native image.Image, root *kit.Element, box kit.Rect, clip kit.Rect, refBox kit.Rect, scale float32, skip *kit.Element) []Delta {
	at := box.Min
	off := kit.Pt(refBox.Min.X-at.X, refBox.Min.Y-at.Y)
	var out []Delta
	var walk func(e *kit.Element)
	walk = func(e *kit.Element) {
		if e.Style.Hidden || (skip != nil && e == skip) {
			return
		}
		if t, ok := e.Widget().(texter); ok && strings.TrimSpace(t.Text()) != "" {
			b := e.WindowBounds()
			if b.Width() > 0 && b.Min.Y >= clip.Min.Y && b.Max.Y <= clip.Max.Y {
				out = append(out, Compare(t.Text(), ref, native, kit.R(b.Min.X-2, b.Min.Y, b.Max.X+2, b.Max.Y), off, scale))
			}
			return
		}
		for _, c := range e.Children() {
			walk(c)
		}
	}
	walk(root)
	return out
}
