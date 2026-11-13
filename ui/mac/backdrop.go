//go:build darwin && cgo

package mac

/*
#include "draw.h"
*/
import "C"

import (
	"math"

	"downex/ui/internal/gfx"
	"downex/ui/kit"
)

// CSS backdrop-filter: blur() (kit.BackdropFilter), as the dialog backdrop's blur(1.5px),
// with ui/win's rules: the whole device pixels inside the area (r ∩ the frame's dirty area ∩
// every rectangle clip) are read from the target's own bitmap (an opaque target's alpha is 1
// everywhere, since its pixels start opaque and everything blends over them, so nothing
// needs forcing as on Windows), blurred by a Gaussian (Accelerate's vImage convolution, a
// 2·ceil(3σ)+1 tap kernel of 12-bit weights in each direction, with transparent pixels
// outside the area: a hard border) and blended back over them at alpha. The area lies
// wholly inside the clip, so the blend writes the bitmap directly. Nothing is drawn while a
// layer (an opacity, a rounded clip) is open: CoreGraphics draws a layer apart, so its
// pixels are not in the bitmap yet.

// BlurBackdrop implements kit.BackdropFilter.
func (c *canvas) BlurBackdrop(r kit.Rect, radius kit.DIP, alpha float32) {
	t := c.t
	ctx := c.ctx()
	if ctx == 0 || !finiteRect(r) || !(radius > 0) || !(alpha > 0) || !finite(radius) {
		return
	}
	area := gfx.XFBounds(c.xf, r).Intersect(t.frameClip)
	for i := range c.stack {
		switch p := &c.stack[i]; p.kind {
		case pushLayer, pushRClip:
			return
		case pushClip:
			area = area.Intersect(gfx.XFBounds(p.prev, p.clip))
		}
	}
	ppd := float64(t.ppd)
	// whole pixels inside the area
	x0, y0 := math.Ceil(float64(area.Min.X)*ppd-1e-3), math.Ceil(float64(area.Min.Y)*ppd-1e-3)
	x1, y1 := math.Floor(float64(area.Max.X)*ppd+1e-3), math.Floor(float64(area.Max.Y)*ppd+1e-3)
	x1, y1 = min(x1, float64(t.w)), min(y1, float64(t.h))
	if area.Empty() || x0 < 0 || y0 < 0 || x1 <= x0 || y1 <= y0 {
		return
	}
	t.blurUsed = t.frame // [render]
	C.dx_blur_backdrop(ctx, &t.blur, C.int(x0), C.int(y0), C.int(x1), C.int(y1), C.float(radius*t.ppd),
		C.CGFloat(min(alpha, 1)))
}
