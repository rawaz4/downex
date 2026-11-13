//go:build windows

package win

import (
	"math"
	"unsafe"

	"downex/ui/internal/gfx"
	"downex/ui/kit"
	"downex/ui/win/internal/com"
	"downex/ui/win/internal/d2d"
)

// CSS backdrop-filter: blur() (kit.BackdropFilter), as the dialog backdrop's blur(1.5px).
// The pixels already drawn under the area are copied into a bitmap of the device
// (ID2D1Bitmap::CopyFromRenderTarget, which wants no clip or layer pushed, so the clips
// are popped around it and pushed again), cropped to the area (Crop effect), made opaque
// on a target without alpha (a window's: what its alpha channel holds is whatever its
// translucent fills left there, and the GPU's effects read it as coverage, which drew the
// blurred copy about twice too bright; a Color matrix in straight mode sets alpha to 1 and
// keeps the colours as they are) and drawn back
// through Direct2D's Gaussian blur with a hard border, so the blur samples nothing outside
// the area, as a browser's backdrop root does. It needs Direct2D 1.1 (Windows 8); without
// it the backdrop stays sharp.

// backdrop is a device's blur: its copy of the target and the two effects, made at the
// first blur and kept until the device goes (or its scale changes).
type backdrop struct {
	copy       *d2d.Bitmap
	w, h       uint32 // the copy's size in pixels
	crop, blur *d2d.Effect
	opaque     *d2d.Effect // the Color matrix between them, on a target without alpha
	out        *d2d.Image  // blur's output
	sigma      float32     // the blur's standard deviation now (DIPs)
	failed     bool        // the effects cannot be made on this device: no blur
	// the property values handed to Direct2D, kept here so passing them allocates nothing
	cropRect [4]float32
	value    float32
	mode     uint32
	matrix   [20]float32
}

func (b *backdrop) release() {
	com.Release(&b.out)
	com.Release(&b.blur)
	com.Release(&b.opaque)
	com.Release(&b.crop)
	com.Release(&b.copy)
	*b = backdrop{}
}

// backdropGrain rounds the copy's size up, so a window resized by a few pixels keeps it.
const backdropGrain = 256

// ensureBackdrop makes the effects (once) and a copy bitmap of at least w × h pixels.
func (d *device) ensureBackdrop(w, h uint32) bool {
	b := &d.backdrop
	if b.failed || d.dc == nil {
		return false
	}
	if b.blur == nil {
		crop, hr := d.dc.CreateEffect(&d2d.CLSID_D2D1Crop)
		if hr.Failed() {
			b.failed = true
			return false
		}
		blur, hr := d.dc.CreateEffect(&d2d.CLSID_D2D1GaussianBlur)
		if hr.Failed() {
			com.Release(&crop)
			b.failed = true
			return false
		}
		b.crop, b.blur = crop, blur
		b.mode = d2d.BORDER_MODE_HARD
		blur.SetValue(d2d.GAUSSIANBLUR_PROP_BORDER_MODE, d2d.PROPERTY_TYPE_ENUM, unsafe.Pointer(&b.mode), 4)
		b.mode = d2d.GAUSSIANBLUR_OPTIMIZATION_QUALITY
		blur.SetValue(d2d.GAUSSIANBLUR_PROP_OPTIMIZATION, d2d.PROPERTY_TYPE_ENUM, unsafe.Pointer(&b.mode), 4)
		in := crop.GetOutput()
		if d.clearTypeLayers { // ALPHA_MODE_IGNORE
			opaque, hr := d.dc.CreateEffect(&d2d.CLSID_D2D1ColorMatrix)
			if hr.Failed() {
				com.Release(&in)
				b.release()
				b.failed = true
				return false
			}
			b.opaque = opaque
			// rows R, G, B, A, offset; columns R, G, B, A: the colours kept, alpha 1
			b.matrix = [20]float32{0: 1, 5: 1, 10: 1, 19: 1}
			opaque.SetValue(d2d.COLORMATRIX_PROP_COLOR_MATRIX, d2d.PROPERTY_TYPE_MATRIX_5X4, unsafe.Pointer(&b.matrix), 80)
			b.mode = d2d.COLORMATRIX_ALPHA_MODE_STRAIGHT
			opaque.SetValue(d2d.COLORMATRIX_PROP_ALPHA_MODE, d2d.PROPERTY_TYPE_ENUM, unsafe.Pointer(&b.mode), 4)
			opaque.SetInput(0, in, true)
			com.Release(&in)
			in = opaque.GetOutput()
		}
		blur.SetInput(0, in, true)
		com.Release(&in)
		b.out = blur.GetOutput()
		if b.out == nil {
			b.release()
			b.failed = true
			return false
		}
	}
	if b.copy == nil || b.w < w || b.h < h {
		nw := (max(w, b.w) + backdropGrain - 1) / backdropGrain * backdropGrain
		nh := (max(h, b.h) + backdropGrain - 1) / backdropGrain * backdropGrain
		alpha := uint32(d2d.ALPHA_MODE_PREMULTIPLIED)
		if d.clearTypeLayers { // the target's own alpha mode (see hwndtarget and offscreen)
			alpha = d2d.ALPHA_MODE_IGNORE
		}
		p := d2d.BitmapProperties{PixelFormat: d2d.PixelFormat{Format: d2d.DXGI_FORMAT_B8G8R8A8_UNORM, AlphaMode: alpha}, DpiX: 96 * d.ppd, DpiY: 96 * d.ppd}
		bmp, hr := d.rt.CreateBitmap(nw, nh, nil, 0, &p)
		if hr.Failed() {
			return false
		}
		b.crop.SetInput(0, nil, false)
		com.Release(&b.copy)
		b.copy, b.w, b.h = bmp, nw, nh
		b.crop.SetInput(0, &bmp.Image, true)
	}
	return true
}

// BlurBackdrop implements kit.BackdropFilter.
func (c *canvas) BlurBackdrop(r kit.Rect, radius kit.DIP, alpha float32) {
	rt := c.rt()
	d := c.dev
	if rt == nil || d.dc == nil || !finiteRect(r) || !(radius > 0) || !(alpha > 0) || !finite(radius) {
		return
	}
	area := xfBounds(c.xf, r).Intersect(d.frameClip)
	for i := range c.stack {
		switch p := &c.stack[i]; p.kind {
		case pushLayer:
			return // a layer cannot be popped and pushed again
		case pushClip:
			area = area.Intersect(xfBounds(p.prev, p.clip))
		}
	}
	// whole pixels inside the area
	x0, y0 := math.Ceil(float64(area.Min.X*d.ppd)-1e-3), math.Ceil(float64(area.Min.Y*d.ppd)-1e-3)
	x1, y1 := math.Floor(float64(area.Max.X*d.ppd)+1e-3), math.Floor(float64(area.Max.Y*d.ppd)+1e-3)
	if area.Empty() || x0 < 0 || y0 < 0 || x1 <= x0 || y1 <= y0 {
		return
	}
	if !d.ensureBackdrop(uint32(x1), uint32(y1)) {
		return
	}
	b := &d.backdrop
	// Pop every clip (the stack's, then the frame's), copy, and push them back.
	for i := len(c.stack) - 1; i >= 0; i-- {
		if c.stack[i].kind == pushClip {
			rt.PopAxisAlignedClip()
		}
	}
	rt.PopAxisAlignedClip()
	src := d2d.RectU{Left: uint32(x0), Top: uint32(y0), Right: uint32(x1), Bottom: uint32(y1)}
	dst := d2d.PointU{X: uint32(x0), Y: uint32(y0)}
	hr := b.copy.CopyFromRenderTarget(&dst, rt, &src)
	rt.SetTransform(&d2d.Identity)
	fc := rectF(d.frameClip)
	rt.PushAxisAlignedClip(&fc, d2d.ANTIALIAS_MODE_ALIASED)
	for i := range c.stack {
		if p := &c.stack[i]; p.kind == pushClip {
			m := matrix(p.prev)
			rt.SetTransform(&m)
			rc := rectF(p.clip)
			rt.PushAxisAlignedClip(&rc, p.aa)
		}
	}
	rt.SetTransform(&d2d.Identity)
	if !hr.Failed() {
		ppd := d.ppd
		b.cropRect = [4]float32{float32(x0) / ppd, float32(y0) / ppd, float32(x1) / ppd, float32(y1) / ppd}
		b.crop.SetValue(d2d.CROP_PROP_RECT, d2d.PROPERTY_TYPE_VECTOR4, unsafe.Pointer(&b.cropRect), 16)
		if b.sigma != radius {
			b.sigma, b.value = radius, radius
			b.blur.SetValue(d2d.GAUSSIANBLUR_PROP_STANDARD_DEVIATION, d2d.PROPERTY_TYPE_FLOAT, unsafe.Pointer(&b.value), 4)
		}
		layer := alpha < 1
		if layer {
			p := d2d.LayerParameters{
				ContentBounds: d2d.RectF{Left: b.cropRect[0], Top: b.cropRect[1], Right: b.cropRect[2], Bottom: b.cropRect[3]},
				MaskTransform: d2d.Identity, Opacity: alpha,
			}
			rt.PushLayer(&p, nil)
		}
		d.dc.DrawImage(b.out, nil, nil, d2d.INTERPOLATION_MODE_LINEAR, d2d.COMPOSITE_MODE_SOURCE_OVER)
		if layer {
			rt.PopLayer()
		}
	}
	m := matrix(c.xf)
	rt.SetTransform(&m)
}

// xfBounds is the bounds of r through m.
func xfBounds(m kit.Affine, r kit.Rect) kit.Rect { return gfx.XFBounds(m, r) }

var _ kit.BackdropFilter = (*canvas)(nil)
