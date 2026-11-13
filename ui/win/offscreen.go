//go:build windows

package win

import (
	"fmt"
	stdimage "image"
	"os"
	"unsafe"

	"downex/ui/kit"
	"downex/ui/win/internal/com"
	"downex/ui/win/internal/d2d"
	"downex/ui/win/internal/wic"
)

// Offscreen is a kit.RenderTarget that draws into a bitmap in memory (WIC), for screenshot
// tests and exported images. Its size is in device pixels; DIPs map to them at the scale it
// was made with. It draws with the same canvas as a window.
type Offscreen struct {
	res   *Resources
	bmp   *wic.Bitmap
	w, h  int
	ppd   float32
	alpha bool
	dev   *device
}

// OffscreenOptions are an Offscreen's choices.
type OffscreenOptions struct {
	// Transparent gives the bitmap an alpha channel that starts transparent; text on it is
	// then antialiased in grayscale. Without it the bitmap is opaque (it starts black) and
	// text takes ClearType as in a window, except inside a rounded clip: there a window keeps
	// ClearType (canvas.PushRoundedClip) and an offscreen bitmap draws grayscale, so that
	// its pixels at clip edges stay exactly those of an unclipped frame (TestNoClippedGlyphs).
	Transparent bool
}

// NewOffscreen makes a w × h pixel offscreen target at ppd device pixels per DIP (1.25 is
// a 125% monitor; 0 counts as 1).
func NewOffscreen(res *Resources, w, h int, ppd float32, opts OffscreenOptions) (*Offscreen, error) {
	if err := archErr(goarch); err != nil {
		return nil, err
	}
	if err := res.usable(); err != nil {
		return nil, err
	}
	if w <= 0 || h <= 0 || w > 16384 || h > 16384 {
		return nil, errInvalid("offscreen size %d×%d", w, h)
	}
	if !(ppd > 0) || !finite(ppd) {
		ppd = 1
	}
	f, err := res.imaging()
	if err != nil {
		return nil, err
	}
	format := &wic.GUID_WICPixelFormat32bppBGR
	if opts.Transparent {
		format = &wic.GUID_WICPixelFormat32bppPBGRA
	}
	bmp, hr := f.CreateBitmap(uint32(w), uint32(h), format, wic.BitmapCacheOnLoad)
	if hr.Failed() {
		return nil, fmt.Errorf("win: offscreen: %w", com.Check("IWICImagingFactory::CreateBitmap", hr))
	}
	o := &Offscreen{res: res, bmp: bmp, w: w, h: h, ppd: ppd, alpha: opts.Transparent}
	o.dev = newDevice(res, ppd, o.makeTarget, nil)
	// An opaque target has no alpha (ALPHA_MODE_IGNORE), as a window's: its rounded clips
	// keep ClearType too, so offscreen renders compare with the window's text.
	o.dev.clearTypeLayers = !opts.Transparent
	return o, nil
}

func (o *Offscreen) makeTarget() (*d2d.RenderTarget, error) {
	if o.bmp == nil {
		return nil, fmt.Errorf("win: offscreen closed: %w", kit.ErrClosed)
	}
	alpha := uint32(d2d.ALPHA_MODE_IGNORE)
	if o.alpha {
		alpha = d2d.ALPHA_MODE_PREMULTIPLIED
	}
	p := d2d.RenderTargetProperties{
		Type:        d2d.RENDER_TARGET_TYPE_SOFTWARE,
		PixelFormat: d2d.PixelFormat{Format: d2d.DXGI_FORMAT_B8G8R8A8_UNORM, AlphaMode: alpha},
		DpiX:        96 * o.dev.ppd, DpiY: 96 * o.dev.ppd,
	}
	rt, hr := o.res.d2d.CreateWicBitmapRenderTarget(&o.bmp.Object, &p)
	if hr.Failed() {
		return nil, fmt.Errorf("win: offscreen: %w", com.Check("ID2D1Factory::CreateWicBitmapRenderTarget", hr))
	}
	return rt, nil
}

// Size is the target's size in DIPs.
func (o *Offscreen) Size() kit.Size {
	return kit.Size{W: float32(o.w) / o.ppd, H: float32(o.h) / o.ppd}
}

// Bounds is the whole target in DIPs, what BeginFrame takes to redraw all of it.
func (o *Offscreen) Bounds() kit.Rect { return kit.RectAt(kit.Point{}, o.Size()) }

// BeginFrame implements kit.RenderTarget.
func (o *Offscreen) BeginFrame(dirty kit.Rect) (kit.Canvas, error) {
	if o.bmp == nil {
		return nil, fmt.Errorf("win: offscreen closed: %w", kit.ErrClosed)
	}
	return o.dev.begin(roundOut(dirty, o.ppd).Intersect(o.Bounds()))
}

// EndFrame implements kit.RenderTarget.
func (o *Offscreen) EndFrame() error { return o.dev.end() }

// SimulateDeviceLoss makes the next EndFrame report D2DERR_RECREATE_TARGET, as a reset GPU
// does, so the recovery path can be tested.
func (o *Offscreen) SimulateDeviceLoss() { o.dev.simulateLoss = true }

// Close releases the target and its bitmap. It is idempotent.
func (o *Offscreen) Close() {
	if o.bmp == nil {
		return
	}
	o.dev.drop()
	com.Release(&o.bmp)
}

// RGBA copies the bitmap into an image (alpha premultiplied, as image.RGBA is).
func (o *Offscreen) RGBA() (*stdimage.RGBA, error) {
	if o.bmp == nil {
		return nil, fmt.Errorf("win: offscreen closed: %w", kit.ErrClosed)
	}
	if o.dev.inFrame {
		return nil, errInvalid("RGBA during a frame")
	}
	rc := wic.Rect{W: int32(o.w), H: int32(o.h)}
	lock, hr := o.bmp.Lock(&rc, wic.BitmapLockRead)
	if hr.Failed() {
		return nil, fmt.Errorf("win: %w", com.Check("IWICBitmap::Lock", hr))
	}
	defer lock.Release()
	stride, hr := lock.GetStride()
	if hr.Failed() {
		return nil, fmt.Errorf("win: %w", com.Check("IWICBitmapLock::GetStride", hr))
	}
	p, size, hr := lock.GetDataPointer()
	if hr.Failed() || p == nil || int(size) < int(stride)*(o.h-1)+4*o.w {
		return nil, fmt.Errorf("win: %w", com.Check("IWICBitmapLock::GetDataPointer", hr))
	}
	src := unsafe.Slice((*byte)(p), size)
	img := stdimage.NewRGBA(stdimage.Rect(0, 0, o.w, o.h))
	for y := 0; y < o.h; y++ {
		row := src[y*int(stride) : y*int(stride)+4*o.w]
		out := img.Pix[y*img.Stride : y*img.Stride+4*o.w]
		for x := 0; x < o.w; x++ {
			b, g, r, a := row[4*x], row[4*x+1], row[4*x+2], row[4*x+3]
			if !o.alpha {
				a = 255
			}
			out[4*x], out[4*x+1], out[4*x+2], out[4*x+3] = r, g, b, a
		}
	}
	return img, nil
}

// EncodePNG encodes the bitmap as PNG with WIC's encoder: 24-bit when opaque, 32-bit with
// straight alpha when transparent.
func (o *Offscreen) EncodePNG() ([]byte, error) {
	if o.bmp == nil {
		return nil, fmt.Errorf("win: offscreen closed: %w", kit.ErrClosed)
	}
	if o.dev.inFrame {
		return nil, errInvalid("EncodePNG during a frame")
	}
	f, err := o.res.imaging()
	if err != nil {
		return nil, err
	}
	stream, err := wic.NewGrowingStream()
	if err != nil {
		return nil, fmt.Errorf("win: PNG: %w", err)
	}
	defer stream.Release()
	enc, hr := f.CreateEncoder(&wic.GUID_ContainerFormatPng)
	if hr.Failed() {
		return nil, fmt.Errorf("win: PNG: %w", com.Check("IWICImagingFactory::CreateEncoder", hr))
	}
	defer enc.Release()
	if hr := enc.Initialize(stream, wic.BitmapEncoderNoCache); hr.Failed() {
		return nil, fmt.Errorf("win: PNG: %w", com.Check("IWICBitmapEncoder::Initialize", hr))
	}
	frame, props, hr := enc.CreateNewFrame()
	if hr.Failed() {
		return nil, fmt.Errorf("win: PNG: %w", com.Check("IWICBitmapEncoder::CreateNewFrame", hr))
	}
	defer frame.Release()
	if props != nil {
		defer props.Release()
	}
	format := wic.GUID_WICPixelFormat24bppBGR
	if o.alpha {
		format = wic.GUID_WICPixelFormat32bppBGRA
	}
	for _, step := range []struct {
		op string
		hr func() com.HRESULT
	}{
		{"IWICBitmapFrameEncode::Initialize", func() com.HRESULT { return frame.Initialize(props) }},
		{"IWICBitmapFrameEncode::SetSize", func() com.HRESULT { return frame.SetSize(uint32(o.w), uint32(o.h)) }},
		{"IWICBitmapFrameEncode::SetPixelFormat", func() com.HRESULT { return frame.SetPixelFormat(&format) }},
		{"IWICBitmapFrameEncode::WriteSource", func() com.HRESULT { return frame.WriteSource(&o.bmp.BitmapSource) }},
		{"IWICBitmapFrameEncode::Commit", frame.Commit},
		{"IWICBitmapEncoder::Commit", enc.Commit},
	} {
		if hr := step.hr(); hr.Failed() {
			return nil, fmt.Errorf("win: PNG: %w", com.Check(step.op, hr))
		}
	}
	b, err := stream.Bytes()
	if err != nil {
		return nil, fmt.Errorf("win: PNG: %w", err)
	}
	return b, nil
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
