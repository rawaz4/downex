//go:build windows

package win

import (
	"fmt"
	"time"

	"downex/ui/kit"
	"downex/ui/win/internal/com"
	"downex/ui/win/internal/d2d"
	"downex/ui/win/internal/w32"
)

// hwndTarget is a window's kit.RenderTarget: an ID2D1HwndRenderTarget, on the GPU unless
// the window asked for the software renderer or the GPU target cannot be made (Remote
// Desktop without one, no GPU driver). It keeps its contents between frames
// (D2D1_PRESENT_OPTIONS_RETAIN_CONTENTS), so a frame redraws only its dirty area, and it does
// not wait for the display in EndDraw: the frame clock paces animations.
//
// A GPU that keeps losing the device (a broken driver, resets one after another) would make
// every frame a new target and a full repaint, a loop at full CPU. After lossLimit losses
// within lossSpan the target falls back to the software renderer for a while (swBackoff,
// doubling up to swBackoffMax while the GPU keeps failing), and when the software target
// keeps losing it too, the window waits before it repaints (Window.retryTarget).
//
// A frame made for a new size waits until the compositor has it (waitComposed), so that the
// window is never shown at its new size with the previous frame.
type hwndTarget struct {
	w        *Window
	rt       *d2d.HwndRenderTarget
	dev      *device
	software bool  // asked for
	usedSW   bool  // the target in use is the software one
	lastErr  error // the last frame's error, for diagnostics

	lossStart time.Time     // the first of the recent losses
	lossCount int           // losses since lossStart
	swUntil   time.Time     // the GPU is not tried again before then (zero: no fallback)
	backoff   time.Duration // the last fallback's length

	composeWaits int   // frames waitComposed waited for (tests)
	gpuWaits     int   // of them, those that waited for the GPU too (tests)
	syncErr      error // the last failed wait for the GPU, for diagnostics
}

const (
	lossLimit    = 3
	lossSpan     = 5 * time.Second
	swBackoff    = 30 * time.Second
	swBackoffMax = 10 * time.Minute
)

func newHWNDTarget(w *Window, software bool) *hwndTarget {
	t := &hwndTarget{w: w, software: software}
	t.dev = newDevice(w.app.res, w.ppd(), t.create, func() { t.rt = nil })
	t.dev.clearTypeLayers = true // ALPHA_MODE_IGNORE
	return t
}

func (t *hwndTarget) create() (*d2d.RenderTarget, error) {
	rc := w32.GetClientRect(t.w.hwnd)
	hp := d2d.HwndRenderTargetProperties{
		Hwnd:           t.w.hwnd,
		PixelSize:      d2d.SizeU{W: uint32(max(rc.Right-rc.Left, 1)), H: uint32(max(rc.Bottom-rc.Top, 1))},
		PresentOptions: d2d.PRESENT_OPTIONS_RETAIN_CONTENTS | d2d.PRESENT_OPTIONS_IMMEDIATELY,
	}
	p := d2d.RenderTargetProperties{
		Type:        d2d.RENDER_TARGET_TYPE_HARDWARE,
		PixelFormat: d2d.PixelFormat{Format: d2d.DXGI_FORMAT_B8G8R8A8_UNORM, AlphaMode: d2d.ALPHA_MODE_IGNORE},
		DpiX:        96 * t.dev.ppd, DpiY: 96 * t.dev.ppd,
	}
	var rt *d2d.HwndRenderTarget
	var hr com.HRESULT = com.E_FAIL
	if !t.software && t.swUntil.IsZero() {
		rt, hr = t.w.app.res.d2d.CreateHwndRenderTarget(&p, &hp)
	}
	t.usedSW = false
	if hr.Failed() {
		hwErr := hr
		p.Type = d2d.RENDER_TARGET_TYPE_SOFTWARE
		if rt, hr = t.w.app.res.d2d.CreateHwndRenderTarget(&p, &hp); hr.Failed() {
			return nil, fmt.Errorf("win: window target (hardware %v): %w", hwErr, com.Check("ID2D1Factory::CreateHwndRenderTarget", hr))
		}
		t.usedSW = true
	}
	t.rt = rt
	return &rt.RenderTarget, nil
}

// softwareNow: the window draws with the software renderer, or is asked to (before its
// first frame), or the GPU is not tried again yet after its losses. Its popups draw so too.
func (t *hwndTarget) softwareNow() bool {
	return t.usedSW || t.software || !t.swUntil.IsZero()
}

// Renderer names the renderer in use: "hardware", "software", or "" before the first frame.
func (t *hwndTarget) Renderer() string {
	switch {
	case t.rt == nil:
		return ""
	case t.usedSW:
		return "software"
	}
	return "hardware"
}

// BeginFrame implements kit.RenderTarget.
func (t *hwndTarget) BeginFrame(dirty kit.Rect) (kit.Canvas, error) {
	if t.w.closed {
		return nil, fmt.Errorf("win: window closed: %w", kit.ErrClosed)
	}
	return t.dev.begin(roundOut(dirty, t.dev.ppd))
}

// EndFrame implements kit.RenderTarget.
func (t *hwndTarget) EndFrame() error { return t.dev.end() }

func (t *hwndTarget) resize(px w32.Point) {
	if t.rt == nil || px.X <= 0 || px.Y <= 0 {
		return
	}
	s := d2d.SizeU{W: uint32(px.X), H: uint32(px.Y)}
	if hr := t.rt.Resize(&s); hr == com.D2DERR_RECREATE_TARGET {
		t.dev.losses++
		t.dev.drop()
		t.lost(time.Now()) // the paint that follows the resize repaints everything
	}
}

// lost counts a lost device at now. It falls back to the software renderer when the GPU has
// lost it lossLimit times within lossSpan, and reports true (wait before repainting) when
// the software renderer loses it that often.
func (t *hwndTarget) lost(now time.Time) (wait bool) {
	if t.lossCount == 0 || now.Sub(t.lossStart) > lossSpan {
		t.lossStart, t.lossCount = now, 0
	}
	if t.lossCount++; t.lossCount < lossLimit {
		return false
	}
	t.lossStart, t.lossCount = now, 0
	if t.usedSW || t.software {
		return true
	}
	t.backoff = min(max(2*t.backoff, swBackoff), swBackoffMax)
	t.swUntil = now.Add(t.backoff)
	return false
}

// retryHardware drops the software target of a fallback once its time is over, so that the
// next frame tries the GPU again; it reports whether it did (the frame then paints the whole
// window). The backoff is kept: a GPU that fails again falls back for twice as long.
func (t *hwndTarget) retryHardware(now time.Time) bool {
	if t.swUntil.IsZero() || now.Before(t.swUntil) {
		return false
	}
	t.swUntil = time.Time{}
	if t.rt == nil {
		return false // no target yet: the next one is made on the GPU anyway
	}
	t.dev.drop()
	return true
}

// waitComposed returns once the compositor has taken the frame just presented, which a
// resize calls before it returns. The GPU target's present reaches the compositor (DWM) on
// its own schedule, while the window's new size reaches it with the size change itself: the
// compositor then showed the window at its new size with the previous frame for one refresh
// in 51 to 57 of 80 resize steps (caption buttons a step behind). DwmFlush returns at the
// compositor's next present, once the DirectX surface updates outstanding then are made, but
// a present the GPU has not finished is not one of them yet. A fixed number of refreshes
// is therefore a guess at the GPU's time: two DwmFlush calls left 2 to 11 of 80 steps stale
// on the 239 Hz monitor, later in a run (a GPU that has clocked down takes longer). So the
// wait is for the GPU first (device.finishGPU: 11 to 13 ms there, the frame and its present),
// then for one composition that began after it (0 of 80). When the GPU cannot be waited
// for, two DwmFlush calls are the fallback. It costs nothing in other frames. The software
// target draws through GDI into the window's surface, which the compositor takes with the
// size change (0 of 80 without waiting), so it does not wait.
func (t *hwndTarget) waitComposed() {
	if t.rt == nil || t.usedSW {
		return // no frame was presented (a lost device), or it reached the window already
	}
	t.composeWaits++
	flushes := 1
	if err := t.dev.finishGPU(); err != nil {
		t.syncErr = err
		flushes = 2
	} else {
		t.gpuWaits++
	}
	for range flushes {
		if w32.DwmFlush() != nil {
			return // composition is off (a session without one): nothing to wait for
		}
	}
}

func (t *hwndTarget) close() { t.dev.drop() }

var _ kit.RenderTarget = (*hwndTarget)(nil)
