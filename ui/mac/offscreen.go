//go:build darwin && cgo

package mac

/*
#include "draw.h"
*/
import "C"

import (
	"bytes"
	stdimage "image"
	"image/png"
	"os"
	"unsafe"

	"downex/ui/kit"
)

// Offscreen is a kit.RenderTarget that draws into a bitmap in memory, for screenshot tests
// and exported images. Its size is in device pixels; DIPs map to them at the scale it was
// made with. It draws with the same target and canvas as a window.
type Offscreen struct {
	t     *bitmapTarget
	alpha bool
}

// OffscreenOptions are an Offscreen's choices.
type OffscreenOptions struct {
	// Transparent gives the bitmap an alpha channel that starts transparent. Without it the
	// bitmap is opaque (it starts black), as a window's. Text is antialiased in grayscale
	// either way (macOS has no subpixel antialiasing, and app.css turns smoothing off).
	Transparent bool
}

// NewOffscreen makes a w × h pixel offscreen target at ppd device pixels per DIP (2 is a
// Retina display; 0 counts as 1).
func NewOffscreen(res *Resources, w, h int, ppd float32, opts OffscreenOptions) (*Offscreen, error) {
	t, err := newBitmapTarget(res, w, h, ppd, !opts.Transparent)
	if err != nil {
		return nil, err
	}
	return &Offscreen{t: t, alpha: opts.Transparent}, nil
}

// Size is the target's size in DIPs.
func (o *Offscreen) Size() kit.Size { return o.t.Size() }

// Bounds is the whole target in DIPs, what BeginFrame takes to redraw all of it.
func (o *Offscreen) Bounds() kit.Rect { return o.t.Bounds() }

// BeginFrame implements kit.RenderTarget.
func (o *Offscreen) BeginFrame(dirty kit.Rect) (kit.Canvas, error) { return o.t.BeginFrame(dirty) }

// EndFrame implements kit.RenderTarget.
func (o *Offscreen) EndFrame() error { return o.t.EndFrame() }

// SimulateDeviceLoss makes the next EndFrame report kit.ErrDeviceLost and clear the bitmap
// and the caches, as a lost device would, so that the recovery path can be tested (a
// CoreGraphics bitmap is never lost by itself).
func (o *Offscreen) SimulateDeviceLoss() { o.t.loseNext = true }

// Close releases the target and its bitmap. It is idempotent.
func (o *Offscreen) Close() { o.t.Close() }

// RGBA copies the bitmap into an image (alpha premultiplied, as image.RGBA is).
func (o *Offscreen) RGBA() (*stdimage.RGBA, error) {
	t := o.t
	if t.ctx == 0 {
		return nil, errTargetClosed
	}
	if t.inFrame {
		return nil, errInvalid("RGBA during a frame")
	}
	if t.chain != nil { // [render] a window's surface is read under its lock
		if !t.chain.lockRead() {
			return nil, errInvalid("the window's surface cannot be locked")
		}
		defer t.chain.unlockRead()
	}
	stride := int(C.CGBitmapContextGetBytesPerRow(t.ctx))
	src := unsafe.Slice((*byte)(C.CGBitmapContextGetData(t.ctx)), stride*t.h)
	img := stdimage.NewRGBA(stdimage.Rect(0, 0, t.w, t.h))
	for y := 0; y < t.h; y++ {
		row := src[y*stride : y*stride+4*t.w]
		out := img.Pix[y*img.Stride : y*img.Stride+4*t.w]
		for x := 0; x < t.w; x++ {
			b, g, r, a := row[4*x], row[4*x+1], row[4*x+2], row[4*x+3]
			if !o.alpha {
				a = 255
			}
			out[4*x], out[4*x+1], out[4*x+2], out[4*x+3] = r, g, b, a
		}
	}
	return img, nil
}

// EncodePNG encodes the bitmap as PNG: 24-bit when opaque, 32-bit with straight alpha when
// transparent.
func (o *Offscreen) EncodePNG() ([]byte, error) {
	img, err := o.RGBA()
	if err != nil {
		return nil, err
	}
	// image/png writes 24-bit RGB for an opaque image and un-premultiplies the others.
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// WritePNG writes EncodePNG's result to the file name.
func (o *Offscreen) WritePNG(name string) error {
	b, err := o.EncodePNG()
	if err != nil {
		return err
	}
	return os.WriteFile(name, b, 0o644)
}

var _ kit.RenderTarget = (*Offscreen)(nil)
