//go:build windows

package win

import (
	"errors"
	"fmt"
	"unsafe"

	"downex/ui/internal/gfx"
	"downex/ui/kit"
	"downex/ui/win/internal/com"
	"downex/ui/win/internal/d2d"
)

// device is what draws on one render target: the Direct2D target and everything made for
// it (the brush, gradients, image bitmaps, shadow masks). A lost device takes all of that
// with it; the next frame makes it again.
type device struct {
	res    *Resources
	create func() (*d2d.RenderTarget, error) // makes the target (again after a loss)
	lost   func()                            // drops the maker's own reference to the target

	rt     *d2d.RenderTarget
	dc     *d2d.DeviceContext // the same target as Direct2D 1.1; nil before Windows 8
	brush  *d2d.SolidColorBrush
	color  kit.Color
	textAA uint32
	ppd    float32 // device pixels per DIP
	// clearTypeLayers: rounded clips use layers without alpha, which keep ClearType (a
	// window's target, ALPHA_MODE_IGNORE)
	clearTypeLayers bool

	canvas  canvas
	inFrame bool
	frame   uint64

	// finishGPU's 1 × 1 pixel bitmap the CPU can read (made at its first use), and what its
	// Map answers.
	gpuSync   *d2d.Bitmap1
	gpuMapped d2d.MappedRect

	frameClip kit.Rect // this frame's dirty area (DIPs)
	backdrop  backdrop // BlurBackdrop's copy and effects

	images    map[*image]struct{}
	gradients []*gradient
	shadows   map[shadowKey]*shadowMask

	// simulateLoss makes the next EndFrame report D2DERR_RECREATE_TARGET (tests).
	simulateLoss bool
	// counts for tests and diagnostics
	targetsMade, losses int
}

func newDevice(res *Resources, ppd float32, create func() (*d2d.RenderTarget, error), lost func()) *device {
	d := &device{res: res, create: create, lost: lost, ppd: ppd, images: map[*image]struct{}{}, shadows: map[shadowKey]*shadowMask{}}
	d.canvas.dev = d
	return d
}

// ensure makes the target and its brush when there are none.
func (d *device) ensure() error {
	if d.rt != nil {
		return nil
	}
	if err := d.res.usable(); err != nil {
		return err
	}
	rt, err := d.create()
	if err != nil {
		return err
	}
	d.rt = rt
	d.targetsMade++
	d.dc, _ = com.Query[d2d.DeviceContext](&rt.Object, &d2d.IID_ID2D1DeviceContext)
	black := d2d.ColorF{A: 1}
	b, hr := rt.CreateSolidColorBrush(&black)
	if hr.Failed() {
		d.drop()
		return fmt.Errorf("win: %w", com.Check("ID2D1RenderTarget::CreateSolidColorBrush", hr))
	}
	d.brush = b
	d.color = kit.Color{A: 1}
	d.textAA = d2d.TEXT_ANTIALIAS_MODE_DEFAULT
	return nil
}

// drop releases the target and everything made for it.
func (d *device) drop() {
	for im := range d.images {
		if b, ok := im.devs[d]; ok {
			b.Release()
			delete(im.devs, d)
		}
		delete(d.images, im)
	}
	for _, g := range d.gradients {
		g.release()
	}
	d.gradients = d.gradients[:0]
	for k, s := range d.shadows {
		s.release()
		delete(d.shadows, k)
	}
	d.backdrop.release()
	com.Release(&d.brush)
	com.Release(&d.gpuSync)
	com.Release(&d.dc)
	com.Release(&d.rt)
	if d.lost != nil {
		d.lost()
	}
	d.inFrame = false
}

// setPPD changes the target's DPI (a window moved to another monitor).
func (d *device) setPPD(ppd float32) {
	if ppd == d.ppd {
		return
	}
	d.ppd = ppd
	if d.rt != nil {
		d.rt.SetDpi(96*ppd, 96*ppd)
	}
	d.backdrop.release()          // its copy has the old DPI
	for k, s := range d.shadows { // masks are made at the old scale
		s.release()
		delete(d.shadows, k)
	}
}

// roundOut is r rounded out to whole device pixels, in DIPs.
func roundOut(r kit.Rect, ppd float32) kit.Rect { return gfx.RoundOut(r, ppd) }

// begin starts a frame clipped to dirty, which the caller has rounded out to whole pixels.
func (d *device) begin(dirty kit.Rect) (kit.Canvas, error) {
	if d.inFrame {
		return nil, errInvalid("BeginFrame inside a frame")
	}
	if err := d.ensure(); err != nil {
		return nil, err
	}
	rt := d.rt
	rt.BeginDraw()
	rt.SetTransform(&d2d.Identity)
	rt.SetAntialiasMode(d2d.ANTIALIAS_MODE_PER_PRIMITIVE)
	if d.textAA != d2d.TEXT_ANTIALIAS_MODE_DEFAULT {
		rt.SetTextAntialiasMode(d2d.TEXT_ANTIALIAS_MODE_DEFAULT)
		d.textAA = d2d.TEXT_ANTIALIAS_MODE_DEFAULT
	}
	d.frameClip = dirty
	clip := rectF(dirty)
	rt.PushAxisAlignedClip(&clip, d2d.ANTIALIAS_MODE_ALIASED)
	d.inFrame = true
	d.canvas.reset()
	return &d.canvas, nil
}

// end finishes the frame: pops what the root left pushed, presents, and handles a lost
// device.
func (d *device) end() error {
	if !d.inFrame {
		return errInvalid("EndFrame without BeginFrame")
	}
	c := &d.canvas
	var errs []error
	if n := len(c.stack); n > 0 || c.unbalanced > 0 {
		for len(c.stack) > 0 {
			c.Pop()
		}
		errs = append(errs, errInvalid("frame ended with %d pushes open and %d pops without a push", n, c.unbalanced))
	}
	d.rt.PopAxisAlignedClip()
	hr := d.rt.EndDraw()
	d.inFrame = false
	d.frame++
	if d.simulateLoss && !hr.Failed() {
		d.simulateLoss = false
		hr = com.D2DERR_RECREATE_TARGET
	}
	if hr == com.D2DERR_RECREATE_TARGET {
		d.losses++
		d.drop()
		errs = append(errs, fmt.Errorf("win: %w: %w", com.Check("ID2D1RenderTarget::EndDraw", hr), kit.ErrDeviceLost))
	} else if hr.Failed() {
		errs = append(errs, fmt.Errorf("win: %w", com.Check("ID2D1RenderTarget::EndDraw", hr)))
	}
	d.age()
	return errors.Join(errs...)
}

// The pixel finishGPU copies, and where to.
var (
	gpuSyncFrom = d2d.RectU{Right: 1, Bottom: 1}
	gpuSyncTo   d2d.PointU
)

// finishGPU returns once the GPU has done what the target was given so far (the last frame
// and its present). It copies one pixel of the target into a bitmap the CPU can read and
// maps it; Map waits for that copy, which the GPU runs after the work queued before it. It
// is for a target of ALPHA_MODE_IGNORE on Direct2D 1.1 (a window's), between frames. An
// error names the call and its HRESULT; a lost device among them is found by the next frame.
func (d *device) finishGPU() error {
	switch {
	case d.dc == nil:
		return errInvalid("finishGPU without a Direct2D 1.1 target")
	case d.inFrame:
		return errInvalid("finishGPU inside a frame")
	}
	if d.gpuSync == nil {
		p := d2d.BitmapProperties1{
			PixelFormat: d2d.PixelFormat{Format: d2d.DXGI_FORMAT_B8G8R8A8_UNORM, AlphaMode: d2d.ALPHA_MODE_IGNORE},
			DpiX:        96, DpiY: 96,
			Options: d2d.BITMAP_OPTIONS_CPU_READ | d2d.BITMAP_OPTIONS_CANNOT_DRAW,
		}
		b, hr := d.dc.CreateBitmap(d2d.SizeU{W: 1, H: 1}, nil, 0, &p)
		if hr.Failed() {
			return fmt.Errorf("win: %w", com.Check("ID2D1DeviceContext::CreateBitmap", hr))
		}
		d.gpuSync = b
	}
	if hr := d.gpuSync.CopyFromRenderTarget(&gpuSyncTo, d.rt, &gpuSyncFrom); hr.Failed() {
		return fmt.Errorf("win: %w", com.Check("ID2D1Bitmap::CopyFromRenderTarget", hr))
	}
	if hr := d.gpuSync.Map(d2d.MAP_OPTIONS_READ, &d.gpuMapped); hr.Failed() {
		return fmt.Errorf("win: %w", com.Check("ID2D1Bitmap1::Map", hr))
	}
	if hr := d.gpuSync.Unmap(); hr.Failed() {
		return fmt.Errorf("win: %w", com.Check("ID2D1Bitmap1::Unmap", hr))
	}
	return nil
}

// age releases gradients and shadow masks no frame has used for a while.
func (d *device) age() {
	const keep = 600 // frames: about 10 s of animation, or many idle repaints
	if len(d.gradients) > 16 {
		live := d.gradients[:0]
		for _, g := range d.gradients {
			if d.frame-g.used > keep {
				g.release()
				continue
			}
			live = append(live, g)
		}
		clear(d.gradients[len(live):])
		d.gradients = live
	}
	if len(d.shadows) > 32 {
		for k, s := range d.shadows {
			if d.frame-s.used > keep {
				s.release()
				delete(d.shadows, k)
			}
		}
	}
}

// setColor makes c the brush's colour.
func (d *device) setColor(c kit.Color) {
	if c == d.color {
		return
	}
	d.color = c
	d.brush.SetColor((*d2d.ColorF)(unsafe.Pointer(&c)))
}

func rectF(r kit.Rect) d2d.RectF {
	return d2d.RectF{Left: r.Min.X, Top: r.Min.Y, Right: r.Max.X, Bottom: r.Max.Y}
}

func matrix(m kit.Affine) d2d.Matrix3x2F {
	a, b, c, dd, e, f := m.Elems()
	return d2d.Matrix3x2F{M11: a, M12: b, M21: c, M22: dd, M31: e, M32: f}
}
