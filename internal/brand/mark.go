// Package brand draws the Downex mark: a "D" built from four download segments
// (square left edge, rounded right ends) whose third bar is split, its last chunk
// still arriving. The application icon puts the mark in mint on a dim dark tile (or
// deep mint on a light tile); the tray and menu-bar icons use the bare glyph in one
// colour. Every size is laid out on the pixel grid at its own resolution, so small
// sizes stay sharp: the 16 px icon and the 16 and 20 px tray glyphs reproduce the
// hand-drawn design pixel for pixel (see the tests).
//
// The master is drawn on a 256-unit square: bars 24 units high with 10-unit gaps,
// 116 units wide (the short bars 80, the live chunk 32), centred on the tile.
package brand

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
)

// Variant is the icon's colour scheme.
type Variant int

const (
	Dark  Variant = iota // mint on a dim dark tile, the default
	Light                // deep mint on a light tile
)

type palette struct{ tile, hairline, bar, head color.NRGBA }

func rgb(v uint32) color.NRGBA { return color.NRGBA{uint8(v >> 16), uint8(v >> 8), uint8(v), 255} }

var palettes = [...]palette{
	// Mint #93E0BA bars and a lighter live chunk on a softened dark tile with a
	// hairline edge that separates it from dark backgrounds.
	Dark: {tile: rgb(0x1A221E), hairline: rgb(0x34403A), bar: rgb(0x93E0BA), head: rgb(0xCEF1E0)},
	// Deep mint #187A50 (5.3:1 on white) and a darker live chunk on a light tile.
	Light: {tile: rgb(0xF3F7F4), hairline: rgb(0xCFDBD4), bar: rgb(0x187A50), head: rgb(0x0B4A31)},
}

// samples is the supersampling factor per axis.
const samples = 8

// bar is one segment of the mark in pixels, [x0,x1)×[y0,y1); round is the radius of
// its right end (0: square) and head marks the live chunk.
type bar struct {
	x0, y0, x1, y1 int
	round          float64
	head           bool
}

func (b bar) contains(x, y float64) bool {
	if x < float64(b.x0) || x >= float64(b.x1) || y < float64(b.y0) || y >= float64(b.y1) {
		return false
	}
	if c := float64(b.x1) - b.round; b.round > 0 && x > c {
		cy := float64(b.y0+b.y1) / 2
		return (x-c)*(x-c)+(y-cy)*(y-cy) <= b.round*b.round
	}
	return true
}

// fit lays the mark out in a size-pixel square at u pixels per master unit, snapped
// to the pixel grid and centred. Bar height and gap are rounded separately; when
// rounding makes the mark taller than the master allows, the one rounded up the most
// gives a pixel back.
func fit(size int, u float64) []bar {
	h := max(1, int(math.Round(24*u)))
	g := max(1, int(math.Round(10*u)))
	for limit := int(math.Round(126*u)) + 1; 4*h+3*g > limit; {
		if g > 1 && (float64(g)-10*u >= float64(h)-24*u || h <= 2) {
			g--
		} else if h > 1 {
			h--
		} else {
			break
		}
	}
	w := max(4, int(math.Round(116*u)))
	short := max(2, int(math.Round(80*u)))
	head := max(1, int(math.Round(32*u)))
	mid := w - head - g
	top, left := (size-(4*h+3*g))/2, (size-w)/2
	r := 0.0
	if h >= 4 {
		r = float64(h) / 2
	}
	y := func(i int) int { return top + i*(h+g) }
	return []bar{
		{left, y(0), left + short, y(0) + h, r, false},
		{left, y(1), left + w, y(1) + h, r, false},
		{left, y(2), left + mid, y(2) + h, 0, false},
		{left + mid + g, y(2), left + w, y(2) + h, r, true},
		{left, y(3), left + short, y(3) + h, r, false},
	}
}

// glyphUnit is the scale of the bare glyph (tray, menu bar): 12 of 16 pixels wide.
func glyphUnit(size int) float64 { return 0.75 * float64(size) / 116 }

// tile is the rounded square behind the mark, in pixels.
type tile struct {
	x0, x1, radius, hairline float64
	unit                     float64 // pixels per master unit of the mark on it
}

// appTile is the Windows and extension tile: full bleed with a 3.5/16 radius below
// 32 px, where the mark is drawn wider (10 of 16 pixels) to stay legible; from 32 px
// the master's 8-unit inset, 52-unit radius and hairline edge.
func appTile(size int) tile {
	s := float64(size)
	if size < 32 {
		return tile{0, s, 3.5 / 16 * s, 0, 0.625 * s / 116}
	}
	return tile{8.0 / 256 * s, 248.0 / 256 * s, 52.0 / 256 * s, math.Max(1, 1.5/256*s), s / 256}
}

// macTile follows the macOS icon grid: an 824/1024 tile with a 185/1024 radius.
func macTile(size int) tile {
	s := float64(size)
	return tile{100.0 / 1024 * s, 924.0 / 1024 * s, 185.0 / 1024 * s, math.Max(1, 6.0/1024*s), 824.0 / 1024 * s / 240}
}

func inRoundedRect(x, y, x0, y0, x1, y1, r float64) bool {
	if x < x0 || x > x1 || y < y0 || y > y1 {
		return false
	}
	cx := math.Min(math.Max(x, x0+r), x1-r)
	cy := math.Min(math.Max(y, y0+r), y1-r)
	return (x-cx)*(x-cx)+(y-cy)*(y-cy) <= r*r
}

func render(size int, t tile, v Variant) *image.NRGBA {
	p := palettes[v]
	bars := fit(size, t.unit)
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for py := range size {
		for px := range size {
			var sum [3]float64
			hits := 0
			for sy := range samples {
				for sx := range samples {
					x := float64(px) + (float64(sx)+0.5)/samples
					y := float64(py) + (float64(sy)+0.5)/samples
					if !inRoundedRect(x, y, t.x0, t.x0, t.x1, t.x1, t.radius) {
						continue
					}
					c := p.tile
					if t.hairline > 0 && !inRoundedRect(x, y, t.x0+t.hairline, t.x0+t.hairline,
						t.x1-t.hairline, t.x1-t.hairline, t.radius-t.hairline) {
						c = p.hairline
					}
					for _, b := range bars {
						if b.contains(x, y) {
							c = p.bar
							if b.head {
								c = p.head
							}
							break
						}
					}
					sum[0] += float64(c.R)
					sum[1] += float64(c.G)
					sum[2] += float64(c.B)
					hits++
				}
			}
			if hits == 0 {
				continue
			}
			k := float64(hits)
			img.SetNRGBA(px, py, color.NRGBA{
				uint8(math.Round(sum[0] / k)), uint8(math.Round(sum[1] / k)), uint8(math.Round(sum[2] / k)),
				uint8(math.Round(255 * k / (samples * samples))),
			})
		}
	}
	return img
}

// App draws the application icon (Windows, the browser extension) at size×size pixels.
func App(size int, v Variant) *image.NRGBA { return render(size, appTile(size), v) }

// Mac draws the application icon on the macOS icon grid (transparent margin around
// an 824/1024 tile), the input of the iconset.
func Mac(size int, v Variant) *image.NRGBA { return render(size, macTile(size), v) }

// Coverage returns the bare glyph's coverage (0..1) per pixel at size×size, row-major.
func Coverage(size int) []float64 {
	bars := fit(size, glyphUnit(size))
	cov := make([]float64, size*size)
	for py := range size {
		for px := range size {
			hits := 0
			for sy := range samples {
				for sx := range samples {
					x := float64(px) + (float64(sx)+0.5)/samples
					y := float64(py) + (float64(sy)+0.5)/samples
					for _, b := range bars {
						if b.contains(x, y) {
							hits++
							break
						}
					}
				}
			}
			cov[py*size+px] = float64(hits) / (samples * samples)
		}
	}
	return cov
}

// Template draws the bare glyph in black with coverage as alpha (a macOS template image).
func Template(size int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for i, c := range Coverage(size) {
		if c > 0 {
			img.SetNRGBA(i%size, i/size, color.NRGBA{A: uint8(math.Round(255 * c))})
		}
	}
	return img
}

// contourAlpha is the opacity of the tray glyph's dark contour.
const contourAlpha = 0.55

// Tray draws the Windows notification-area glyph: white, with a translucent dark
// contour one pixel wide, so that it reads on dark taskbars (where the contour
// disappears) and on light ones (where it outlines the bars and fills the gaps).
func Tray(size int) *image.NRGBA {
	cov := Coverage(size)
	at := func(x, y int) float64 {
		if x < 0 || y < 0 || x >= size || y >= size {
			return 0
		}
		return cov[y*size+x]
	}
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := range size {
		for x := range size {
			ag := at(x, y)
			dil := ag
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					w := 1.0
					if dx != 0 && dy != 0 {
						w = 0.7 // softer diagonal corners
					}
					dil = math.Max(dil, w*at(x+dx, y+dy))
				}
			}
			ao := contourAlpha * dil
			a := ag + ao*(1-ag)
			if a <= 0 {
				continue
			}
			v := uint8(math.Round(255 * ag / a)) // white glyph over the black contour
			img.SetNRGBA(x, y, color.NRGBA{v, v, v, uint8(math.Round(255 * a))})
		}
	}
	return img
}

// IconImage encodes img as an icon resource image (the input of
// CreateIconFromResourceEx, and an ICO entry below 256 px): a BITMAPINFOHEADER of
// double height, bottom-up BGRA rows, then the 1-bit AND mask (all zero: the alpha
// channel decides).
func IconImage(img *image.NRGBA) []byte {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	maskStride := ((w + 31) / 32) * 4
	var b bytes.Buffer
	b.Grow(40 + w*h*4 + maskStride*h)
	le := binary.LittleEndian
	_ = binary.Write(&b, le, struct {
		Size                     uint32
		Width, Height            int32
		Planes, BitCount         uint16
		Compression, ImageSize   uint32
		XPerMeter, YPerMeter     int32
		ColorsUsed, ColorsImport uint32
	}{40, int32(w), int32(2 * h), 1, 32, 0, uint32(w * h * 4), 0, 0, 0, 0})
	for y := h - 1; y >= 0; y-- {
		for x := range w {
			c := img.NRGBAAt(x, y)
			b.Write([]byte{c.B, c.G, c.R, c.A})
		}
	}
	b.Write(make([]byte, maskStride*h))
	return b.Bytes()
}

// ICO encodes an icon file with one entry per image (square, 1 to 256 px): 32-bit BMP
// entries below 256 px and a PNG entry at 256.
func ICO(images []*image.NRGBA) ([]byte, error) {
	data := make([][]byte, len(images))
	for i, img := range images {
		if img.Bounds().Dx() >= 256 {
			var b bytes.Buffer
			if err := png.Encode(&b, img); err != nil {
				return nil, err
			}
			data[i] = b.Bytes()
			continue
		}
		data[i] = IconImage(img)
	}
	var out bytes.Buffer
	le := binary.LittleEndian
	_ = binary.Write(&out, le, [3]uint16{0, 1, uint16(len(images))}) // reserved, type icon, count
	offset := 6 + 16*len(images)
	for i, img := range images {
		dim := uint8(img.Bounds().Dx())                  // 256 wraps to 0, which means 256
		out.Write([]byte{dim, dim, 0, 0})                // width, height, palette, reserved
		_ = binary.Write(&out, le, [2]uint16{1, 32})     // planes, bits per pixel
		_ = binary.Write(&out, le, uint32(len(data[i]))) // size of the image
		_ = binary.Write(&out, le, uint32(offset))       // its offset
		offset += len(data[i])
	}
	for _, b := range data {
		out.Write(b)
	}
	return out.Bytes(), nil
}

// PNG encodes img as PNG.
func PNG(img image.Image) []byte {
	var b bytes.Buffer
	_ = png.Encode(&b, img) // writing to a bytes.Buffer does not fail
	return b.Bytes()
}
