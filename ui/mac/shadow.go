//go:build darwin && cgo

package mac

/*
#include "draw.h"
*/
import "C"

import (
	"unsafe"

	"downex/ui/internal/gfx"
	"downex/ui/kit"
)

// CSS box-shadow (kit.Shadow), with ui/win's rules (ui/internal/gfx). A sharp shadow (Blur
// 0: focus rings, hairline insets) is the shadow's shape less the box, filled exactly. A
// blurred one is gfx's alpha mask, made once per size in device pixels, kept as an 8-bit
// gray image and filled through as a clip mask, in nine pieces with the middle row and
// column stretched when the mask was made for a smaller box. CoreGraphics' own shadows
// (CGContextSetShadow) are not used: their blur and spread differ from CSS's, and they
// show under translucent fills.

type shadowKey = gfx.ShadowKey

// shadowMask is a blurred shadow's mask on a target: its layout, the image and its pieces
// (made at their first use).
type shadowMask struct {
	l      gfx.MaskLayout
	img    C.CGImageRef
	pieces [9]C.CGImageRef
	used   uint64
	// [render] blank marks the pieces whose alpha is 0 everywhere: an outer shadow's
	// middle, which lies under the box (cut out), stretched over most of a dialog. Filling
	// through it cost Core Graphics a full resampled mask of that area every frame.
	blank [9]bool
}

// rndBlankPieces finds the nine pieces of mask pix (layout l, split as drawShadow splits
// it) that are 0 everywhere.
func rndBlankPieces(l *gfx.MaskLayout, pix []byte) (blank [9]bool) {
	cols, rows := [3][2]int{{0, l.W}}, [3][2]int{{0, l.H}}
	if l.DX > 0 {
		cols = [3][2]int{{0, l.CX}, {l.CX, l.CX + 1}, {l.CX + 1, l.W}}
	}
	if l.DY > 0 {
		rows = [3][2]int{{0, l.CY}, {l.CY, l.CY + 1}, {l.CY + 1, l.H}}
	}
	for i := range 3 {
		for j := range 3 {
			zero := true
			for y := rows[i][0]; y < rows[i][1] && zero; y++ {
				for x := cols[j][0]; x < cols[j][1]; x++ {
					if pix[y*l.W+x] != 0 {
						zero = false
						break
					}
				}
			}
			blank[i*3+j] = zero
		}
	}
	return blank
}

// [/render]

func (m *shadowMask) release() {
	for i, p := range m.pieces {
		if p != 0 {
			C.CGImageRelease(p)
			m.pieces[i] = 0
		}
	}
	if m.img != 0 {
		C.CGImageRelease(m.img)
		m.img = 0
	}
}

func (t *bitmapTarget) drawShadow(r kit.RRect, s kit.Shadow) {
	if s.Blur == 0 {
		t.drawRing(r, s)
		return
	}
	k := gfx.ShadowKeyOf(r, s, t.ppd)
	m, ok := t.shadows[k]
	if !ok {
		// A mask that cannot be made is cached empty, so it is not tried every frame, and
		// ages out as the others do.
		m = &shadowMask{}
		if l, pix := gfx.ShadowAlpha(k); l != nil && len(pix) == l.W*l.H && len(pix) > 0 {
			m.l = *l
			m.img = C.dx_mask_new((*C.uint8_t)(unsafe.Pointer(&pix[0])), C.int(l.W), C.int(l.H))
			m.blank = rndBlankPieces(l, pix) // [render]
		}
		t.shadows[k] = m
	}
	m.used = t.frame
	if m.img == 0 {
		return
	}
	l := &m.l
	ppd := t.ppd
	// The mask is in device pixels from the box's top-left corner.
	ox, oy := r.Rect.Min.X+float32(l.X0)/ppd, r.Rect.Min.Y+float32(l.Y0)/ppd
	p := paint(s.Color)
	if l.DX <= 0 && l.DY <= 0 {
		dst := kit.R(ox, oy, ox+float32(l.W)/ppd, oy+float32(l.H)/ppd)
		C.dx_fill_mask(t.ctx, m.img, crect(dst), p)
		return
	}
	cols := [3][2]int{{0, l.W}}
	nc := 1
	if l.DX > 0 {
		cols = [3][2]int{{0, l.CX}, {l.CX, l.CX + 1}, {l.CX + 1, l.W}}
		nc = 3
	}
	rows := [3][2]int{{0, l.H}}
	nr := 1
	if l.DY > 0 {
		rows = [3][2]int{{0, l.CY}, {l.CY, l.CY + 1}, {l.CY + 1, l.H}}
		nr = 3
	}
	for i := 0; i < nr; i++ {
		y0, y1 := rows[i][0], rows[i][1]
		dy0, dy1 := y0, y1
		if nr == 3 {
			if i >= 1 {
				dy1 += l.DY
			}
			if i == 2 {
				dy0 += l.DY
			}
		}
		for j := 0; j < nc; j++ {
			x0, x1 := cols[j][0], cols[j][1]
			if x1 <= x0 || y1 <= y0 || m.blank[i*3+j] { // [render] blank pieces draw nothing
				continue
			}
			dx0, dx1 := x0, x1
			if nc == 3 {
				if j >= 1 {
					dx1 += l.DX
				}
				if j == 2 {
					dx0 += l.DX
				}
			}
			piece := &m.pieces[i*3+j]
			if *piece == 0 {
				*piece = C.dx_image_crop(m.img, C.int(x0), C.int(y0), C.int(x1-x0), C.int(y1-y0))
				if *piece == 0 {
					continue
				}
			}
			dst := kit.R(ox+float32(dx0)/ppd, oy+float32(dy0)/ppd, ox+float32(dx1)/ppd, oy+float32(dy1)/ppd)
			C.dx_fill_mask(t.ctx, *piece, crect(dst), p)
		}
	}
}

// drawRing fills a sharp shadow: the spread shape less the box for an outer shadow, the box
// less the inner edge for an inset one.
func (t *bitmapTarget) drawRing(r kit.RRect, s kit.Shadow) {
	box := r
	shape := gfx.ShadowShape(r.Rect.Size(), r.Radii, s)
	shape.Rect = shape.Rect.Add(r.Rect.Min)
	outer, inner := shape, box
	if s.Inset {
		outer, inner = box, shape
	}
	if outer.Rect.Empty() {
		return
	}
	C.dx_ring(t.ctx, crect(outer.Rect), cradii(outer.Radii), cbool(!inner.Rect.Empty()), crect(inner.Rect),
		cradii(inner.Radii), paint(s.Color))
}
