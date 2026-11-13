//go:build darwin && cgo

package viewstest

import (
	"fmt"
	"image"
	"math"
	"sync"

	"downex/ui/kit"
	"downex/ui/mac"
)

// RealResources are the macOS backend's resources (Core Graphics, Core Text, ImageIO),
// shared by every harness of the process; release does nothing (they live until the
// process ends).
func RealResources() (kit.Resources, func(), error) {
	res, err := sharedResources()
	if err != nil {
		return nil, nil, err
	}
	return res, func() {}, nil
}

// sharedResources makes the process's resources once.
var sharedResources = sync.OnceValues(mac.NewResources)

// CanRender reports whether this build renders offscreen (macOS with cgo).
func CanRender() error {
	_, err := sharedResources()
	return err
}

// Render paints the whole window offscreen at the harness's scale, as the backend paints a
// window (grayscale text on an opaque bitmap, as WebKit draws it under
// -webkit-font-smoothing: antialiased), and returns the image in device pixels. The
// harness must be made with Real (its layout then used Core Text).
func (h *Harness) Render() (*image.RGBA, error) {
	if !h.Opts.Real {
		return nil, fmt.Errorf("viewstest: Render needs a harness made with Real")
	}
	res, err := sharedResources()
	if err != nil {
		return nil, err
	}
	h.Settle()
	scale := h.Opts.Scale
	size := h.Window.Size()
	px := func(v kit.DIP) int { return int(math.Ceil(float64(v * scale))) }
	o, err := mac.NewOffscreen(res, px(size.W), px(size.H), scale, mac.OffscreenOptions{})
	if err != nil {
		return nil, err
	}
	defer o.Close()
	full := kit.RectAt(kit.Point{}, size)
	c, err := o.BeginFrame(full)
	if err != nil {
		return nil, err
	}
	h.Window.Paint(c, full)
	if err := o.EndFrame(); err != nil {
		return nil, err
	}
	return o.RGBA()
}
