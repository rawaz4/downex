package brand

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/png"
	"strings"
	"testing"
)

// mask renders which pixels a layout covers, '#' for bars and '+' for the live chunk.
func mask(size int, bars []bar) string {
	var sb strings.Builder
	for y := range size {
		for x := range size {
			ch := byte('.')
			for _, b := range bars {
				if x >= b.x0 && x < b.x1 && y >= b.y0 && y < b.y1 {
					ch = '#'
					if b.head {
						ch = '+'
					}
				}
			}
			sb.WriteByte(ch)
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

// The hand-drawn small sizes of the design (B2-Icon: kApp16, kTray16, kTray20).
func TestHandDrawnSizes(t *testing.T) {
	for _, tc := range []struct {
		name string
		size int
		unit float64
		want string
	}{
		{"app 16", 16, appTile(16).unit, `
................
................
...#######......
...#######......
................
...##########...
...##########...
................
...######.+++...
...######.+++...
................
...#######......
...#######......
................
................
................
`},
		{"tray 16", 16, glyphUnit(16), `
................
................
..########......
..########......
................
..############..
..############..
................
..########.+++..
..########.+++..
................
..########......
..########......
................
................
................
`},
		{"tray 20", 20, glyphUnit(20), `
....................
....................
..##########........
..##########........
..##########........
....................
..###############...
..###############...
..###############...
....................
..##########.++++...
..##########.++++...
..##########.++++...
....................
..##########........
..##########........
..##########........
....................
....................
....................
`},
	} {
		if got := "\n" + mask(tc.size, fit(tc.size, tc.unit)); got != tc.want {
			t.Errorf("%s:%s\nwant:%s", tc.name, got, tc.want)
		}
	}
}

func TestFitStaysInMasterProportions(t *testing.T) {
	for _, size := range []int{16, 18, 20, 24, 32, 36, 40, 48, 64, 128, 256, 1024} {
		for _, u := range []float64{appTile(size).unit, glyphUnit(size), macTile(size).unit} {
			bars := fit(size, u)
			top, bottom := bars[0].y0, bars[4].y1
			if top < 0 || bottom > size || bars[0].x0 < 0 || bars[1].x1 > size {
				t.Fatalf("%d px (u %.3f): mark %v leaves the square", size, u, bars)
			}
			if h := bottom - top; float64(h) > 126*u+1.5 {
				t.Errorf("%d px (u %.3f): mark is %d px high, master %.1f", size, u, h, 126*u)
			}
			if bars[3].x0 <= bars[2].x1 || bars[3].x1 != bars[1].x1 {
				t.Errorf("%d px: the live chunk %v does not follow bar %v", size, bars[3], bars[2])
			}
		}
	}
	// The 256 px master is the design's geometry.
	b := fit(256, 1)
	if b[0] != (bar{70, 65, 150, 89, 12, false}) || b[3] != (bar{154, 133, 186, 157, 12, true}) || b[4].y1 != 191 {
		t.Errorf("256 px master: %v", b)
	}
}

func TestApp(t *testing.T) {
	for _, v := range []Variant{Dark, Light} {
		for _, size := range []int{16, 32, 48, 128} {
			img := App(size, v)
			if a := img.NRGBAAt(0, 0).A; a == 255 {
				t.Errorf("variant %d, %d px: corner is opaque", v, size)
			}
			bars := fit(size, appTile(size).unit)
			p := palettes[v]
			if c := img.NRGBAAt(bars[1].x0, bars[1].y0); c != p.bar {
				t.Errorf("variant %d, %d px: bar pixel %v, want %v", v, size, c, p.bar)
			}
			if c := img.NRGBAAt(bars[3].x0, bars[3].y0); c != p.head {
				t.Errorf("variant %d, %d px: live chunk %v, want %v", v, size, c, p.head)
			}
			if c := img.NRGBAAt(size/2, size-1-size/8); c != p.tile {
				t.Errorf("variant %d, %d px: tile %v, want %v", v, size, c, p.tile)
			}
		}
	}
	if a := Mac(1024, Dark).NRGBAAt(60, 512).A; a != 0 {
		t.Errorf("macOS grid margin has alpha %d", a)
	}
}

func TestTrayAndTemplate(t *testing.T) {
	for _, size := range []int{16, 18, 20, 24, 32, 36} {
		bars := fit(size, glyphUnit(size))
		tray, tmpl := Tray(size), Template(size)
		if c := tray.NRGBAAt(bars[1].x0, bars[1].y0); c.R != 255 || c.A != 255 {
			t.Errorf("%d px: tray glyph pixel %v, want opaque white", size, c)
		}
		gap := tray.NRGBAAt(bars[2].x1, bars[2].y0) // between the third bar and its live chunk
		if gap.A == 0 || gap.R > 64 {
			t.Errorf("%d px: tray gap %v, want the dark contour", size, gap)
		}
		if a := tray.NRGBAAt(0, 0).A; a != 0 {
			t.Errorf("%d px: tray corner alpha %d", size, a)
		}
		for y := range size {
			for x := range size {
				if c := tmpl.NRGBAAt(x, y); c.R != 0 || c.G != 0 || c.B != 0 {
					t.Fatalf("%d px: template pixel %v is not black", size, c)
				}
			}
		}
		if a := tmpl.NRGBAAt(bars[1].x0, bars[1].y0).A; a != 255 {
			t.Errorf("%d px: template bar alpha %d", size, a)
		}
	}
}

func TestICO(t *testing.T) {
	sizes := []int{16, 32, 256}
	data, err := ICO([]*image.NRGBA{App(16, Dark), App(32, Dark), App(256, Dark)})
	if err != nil {
		t.Fatal(err)
	}
	le := binary.LittleEndian
	if le.Uint16(data[2:]) != 1 || int(le.Uint16(data[4:])) != len(sizes) {
		t.Fatalf("bad ICO header % x", data[:6])
	}
	for i, s := range sizes {
		e := data[6+16*i:]
		if w := int(e[0]); w != s%256 {
			t.Errorf("entry %d: width %d, want %d", i, w, s%256)
		}
		off, n := le.Uint32(e[12:]), le.Uint32(e[8:])
		img := data[off : off+n]
		if s == 256 {
			if _, err := png.Decode(bytes.NewReader(img)); err != nil {
				t.Errorf("256 px entry: %v", err)
			}
			continue
		}
		maskStride := ((s + 31) / 32) * 4
		if want := 40 + s*s*4 + maskStride*s; len(img) != want || img[0] != 40 || int(le.Uint32(img[8:])) != 2*s {
			t.Errorf("%d px entry: %d bytes, header % x", s, len(img), img[:16])
		}
	}
}
