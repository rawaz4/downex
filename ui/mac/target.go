//go:build darwin && cgo

package mac

/*
#include "draw.h"
#include <stdlib.h>
*/
import "C"

import (
	"errors"
	"fmt"

	"downex/ui/internal/gfx"
	"downex/ui/kit"
)

// bitmapTarget is a kit.RenderTarget over a retained CGBitmapContext: w × h device pixels,
// 32-bit BGRA with premultiplied alpha (the bytes of Direct2D's B8G8R8A8) in the sRGB
// colour space, so CSS colours arrive unconverted and blend as gamma-encoded sRGB, as in
// WebKit. A window presents it (CGImage) and an offscreen target reads it. Drawing is done
// by CoreGraphics on the CPU, so there is no device to lose.
type bitmapTarget struct {
	res    *Resources
	ctx    C.CGContextRef
	w, h   int     // device pixels
	ppd    float32 // device pixels per DIP
	opaque bool    // starts opaque black (and stays opaque: everything blends over it)

	canvas    canvas
	inFrame   bool
	frame     uint64
	frameClip kit.Rect // this frame's area (DIPs)

	gradients []*gradient
	shadows   map[shadowKey]*shadowMask
	blur      C.dx_blur // BlurBackdrop's scratch (C memory)

	// ClearFrames makes every frame start transparent inside its area, as a popup's
	// per-pixel-alpha surface wants (ui/win's popups clear theirs each frame).
	ClearFrames bool

	loseNext bool // the next EndFrame reports a device loss (tests)

	// [render] chain: a window's target draws into IOSurfaces the layer shows (present.go);
	// ctx is then the surface of the frame in progress, else the one shown. Nil for the
	// others (offscreen, popups), whose ctx is their own bitmap.
	chain     *rndChain
	chainArea rndPixRect // the frame in progress's area in device pixels
	blurUsed  uint64     // the last frame that blurred a backdrop (its scratch is freed later)
	// [/render]
}

// [render] newSurfaceTarget makes a window's target: w × h pixels at ppd over IOSurfaces
// (present.go), opaque or not.
func newSurfaceTarget(res *Resources, w, h int, ppd float32, opaque bool) (*bitmapTarget, error) {
	if err := res.usable(); err != nil {
		return nil, err
	}
	if w <= 0 || h <= 0 || w > 16384 || h > 16384 {
		return nil, errInvalid("target size %d×%d", w, h)
	}
	if !(ppd > 0) || !finite(ppd) {
		ppd = 1
	}
	ch, err := newRndChain(w, h, opaque)
	if err != nil {
		return nil, err
	}
	t := &bitmapTarget{res: res, ctx: ch.context(), w: w, h: h, ppd: ppd, opaque: opaque,
		shadows: map[shadowKey]*shadowMask{}, chain: ch}
	t.canvas.t = t
	return t, nil
}

// freeScratch frees BlurBackdrop's buffers; the next blur makes them again.
func (t *bitmapTarget) freeScratch() { C.dx_blur_free(&t.blur) }

// [/render]

// newBitmapTarget makes a w × h pixel target at ppd device pixels per DIP (0 counts as 1).
// An opaque one starts black, a transparent one transparent.
func newBitmapTarget(res *Resources, w, h int, ppd float32, opaque bool) (*bitmapTarget, error) {
	if err := res.usable(); err != nil {
		return nil, err
	}
	if w <= 0 || h <= 0 || w > 16384 || h > 16384 {
		return nil, errInvalid("target size %d×%d", w, h)
	}
	if !(ppd > 0) || !finite(ppd) {
		ppd = 1
	}
	ctx := C.dx_bitmap_new(C.int(w), C.int(h), cbool(opaque))
	if ctx == 0 {
		return nil, fmt.Errorf("mac: CGBitmapContextCreate %d×%d failed", w, h)
	}
	t := &bitmapTarget{res: res, ctx: ctx, w: w, h: h, ppd: ppd, opaque: opaque, shadows: map[shadowKey]*shadowMask{}}
	t.canvas.t = t
	return t, nil
}

func cbool(b bool) C.int {
	if b {
		return 1
	}
	return 0
}

var errTargetClosed = fmt.Errorf("mac: target closed: %w", kit.ErrClosed)

// Size is the target's size in DIPs.
func (t *bitmapTarget) Size() kit.Size {
	return kit.Size{W: float32(t.w) / t.ppd, H: float32(t.h) / t.ppd}
}

// Bounds is the whole target in DIPs.
func (t *bitmapTarget) Bounds() kit.Rect { return kit.RectAt(kit.Point{}, t.Size()) }

// PixelsPerDIP is the target's scale.
func (t *bitmapTarget) PixelsPerDIP() float32 { return t.ppd }

// Renderer is what draws the target, for kit.RendererInfo: CoreGraphics on the CPU.
func (t *bitmapTarget) Renderer() string { return "software" }

// BeginFrame implements kit.RenderTarget: dirty rounded out to whole device pixels and
// clipped to the target; outside it the previous frame stays.
func (t *bitmapTarget) BeginFrame(dirty kit.Rect) (kit.Canvas, error) {
	if t.ctx == 0 {
		return nil, errTargetClosed
	}
	if t.inFrame {
		return nil, errInvalid("BeginFrame inside a frame")
	}
	if err := t.res.usable(); err != nil {
		return nil, err
	}
	area := kit.Rect{}
	if finiteRect(dirty) {
		area = gfx.RoundOut(dirty, t.ppd).Intersect(t.Bounds())
	}
	t.frameClip = area
	if t.chain != nil { // [render]
		t.chainArea = rndPixels(area, t.ppd)
		ctx, err := t.chain.begin(t.chainArea)
		if err != nil { // errRndBusy (the window waits a refresh) or errRndLock (a device loss)
			return nil, err
		}
		t.ctx = ctx
	} // [/render]
	C.dx_frame_begin(t.ctx, C.int(t.h), C.CGFloat(t.ppd), crect(area), cbool(t.ClearFrames))
	t.inFrame = true
	t.canvas.reset()
	return &t.canvas, nil
}

// EndFrame implements kit.RenderTarget: pops what the frame left pushed (and reports it).
func (t *bitmapTarget) EndFrame() error {
	if !t.inFrame {
		return errInvalid("EndFrame without BeginFrame")
	}
	c := &t.canvas
	var errs []error
	if n := len(c.stack); n > 0 || c.unbalanced > 0 {
		for len(c.stack) > 0 {
			c.Pop()
		}
		errs = append(errs, errInvalid("frame ended with %d pushes open and %d pops without a push", n, c.unbalanced))
	}
	C.dx_frame_end(t.ctx)
	if t.chain != nil { // [render]
		t.chain.end(t.chainArea)
	} // [/render]
	t.inFrame = false
	t.frame++
	if t.loseNext {
		t.loseNext = false
		t.dropCaches()
		C.dx_bitmap_clear(t.ctx, cbool(t.opaque))
		errs = append(errs, fmt.Errorf("mac: simulated device loss: %w", kit.ErrDeviceLost))
	}
	t.age()
	return errors.Join(errs...)
}

// CGImage is a +1 image of the target's pixels now, for presenting (copy on write: the
// next frame's drawing copies the bitmap if the image is still held).
func (t *bitmapTarget) CGImage() C.CGImageRef {
	if t.ctx == 0 || t.inFrame {
		return 0
	}
	return C.CGBitmapContextCreateImage(t.ctx)
}

// cgImageRelease releases an image CGImage returned.
func cgImageRelease(im C.CGImageRef) {
	if im != 0 {
		C.CGImageRelease(im)
	}
}

// Resize makes the target w × h pixels at ppd. What it showed is kept from the top-left
// corner when the scale stays (the caller repaints the target all the same).
func (t *bitmapTarget) Resize(w, h int, ppd float32) error {
	if t.ctx == 0 {
		return errTargetClosed
	}
	if t.inFrame {
		return errInvalid("Resize inside a frame")
	}
	if w <= 0 || h <= 0 || w > 16384 || h > 16384 {
		return errInvalid("target size %d×%d", w, h)
	}
	if !(ppd > 0) || !finite(ppd) {
		ppd = 1
	}
	if w == t.w && h == t.h && ppd == t.ppd {
		return nil
	}
	if t.chain != nil { // [render] new surfaces: the window paints all of them next
		// A chain that cannot be made at the new size keeps the old one (and the target its
		// old size), so the target stays usable.
		if err := t.chain.resize(w, h); err != nil {
			return err
		}
		if ppd != t.ppd {
			t.dropShadows()
		}
		t.ctx, t.w, t.h, t.ppd = t.chain.context(), w, h, ppd
		return nil
	} // [/render]
	ctx := C.dx_bitmap_new(C.int(w), C.int(h), cbool(t.opaque))
	if ctx == 0 {
		return fmt.Errorf("mac: CGBitmapContextCreate %d×%d failed", w, h)
	}
	if ppd == t.ppd {
		C.dx_bitmap_copy(ctx, t.ctx)
	} else {
		t.dropShadows() // masks are made at the old scale
	}
	C.CGContextRelease(t.ctx)
	t.ctx, t.w, t.h, t.ppd = ctx, w, h, ppd
	return nil
}

// Close releases the bitmap and the caches. It is idempotent.
func (t *bitmapTarget) Close() {
	if t.ctx == 0 {
		return
	}
	t.dropCaches()
	C.dx_blur_free(&t.blur)
	if t.chain != nil { // [render] the chain owns the contexts
		t.chain.close()
	} else {
		C.CGContextRelease(t.ctx)
	} // [/render]
	t.ctx = 0
	t.inFrame = false
}

func (t *bitmapTarget) dropCaches() {
	for _, g := range t.gradients {
		g.release()
	}
	clear(t.gradients)
	t.gradients = t.gradients[:0]
	t.dropShadows()
}

func (t *bitmapTarget) dropShadows() {
	for k, s := range t.shadows {
		s.release()
		delete(t.shadows, k)
	}
}

// age releases gradients and shadow masks no frame has used for a while (ui/win's rule).
func (t *bitmapTarget) age() {
	const keep = 600 // frames: about 10 s of animation, or many idle repaints
	// [render] BlurBackdrop's scratch is as large as the area it blurred (the whole window
	// while a dialog fades): two full-window buffers kept for good were 28 MB at 2×.
	if t.blur.cap > 0 && t.frame-t.blurUsed > 120 {
		t.freeScratch()
	} // [/render]
	if len(t.gradients) > 16 {
		live := t.gradients[:0]
		for _, g := range t.gradients {
			if t.frame-g.used > keep {
				g.release()
				continue
			}
			live = append(live, g)
		}
		clear(t.gradients[len(live):])
		t.gradients = live
	}
	if len(t.shadows) > 32 {
		for k, s := range t.shadows {
			if t.frame-s.used > keep {
				s.release()
				delete(t.shadows, k)
			}
		}
	}
}

// crect is r for C; an empty r is an empty rectangle at its corner (CoreGraphics would
// turn a negative size around).
func crect(r kit.Rect) C.dx_rect {
	if r.Empty() {
		return C.dx_rect{x0: cg(r.Min.X), y0: cg(r.Min.Y), x1: cg(r.Min.X), y1: cg(r.Min.Y)}
	}
	return C.dx_rect{x0: cg(r.Min.X), y0: cg(r.Min.Y), x1: cg(r.Max.X), y1: cg(r.Max.Y)}
}

func cradii(q kit.Radii) C.dx_radii {
	return C.dx_radii{tl: cg(q.TopLeft), tr: cg(q.TopRight), br: cg(q.BottomRight), bl: cg(q.BottomLeft)}
}

func caffine(m kit.Affine) C.CGAffineTransform {
	a, b, c, d, e, f := m.Elems()
	return C.CGAffineTransform{a: cg(a), b: cg(b), c: cg(c), d: cg(d), tx: cg(e), ty: cg(f)}
}

var _ kit.RenderTarget = (*bitmapTarget)(nil)
