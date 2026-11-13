//go:build darwin && cgo

package mac

/*
#include <stdlib.h>
#include <string.h>
#include <CoreGraphics/CoreGraphics.h>

// dxtProbeContext is a premultiplied sRGB RGBA bitmap context of w x h pixels filled with
// the opaque sRGB colour (r, g, b) (or cleared when a is 0), whose coordinates are DIPs, y
// down, at ppd pixels per DIP: what a canvas hands DrawText.
static CGContextRef dxtProbeContext(int w, int h, double ppd, double r, double g, double b, double a) {
	CGColorSpaceRef cs = CGColorSpaceCreateWithName(kCGColorSpaceSRGB);
	CGContextRef c = CGBitmapContextCreate(NULL, w, h, 8, 4 * w, cs, kCGImageAlphaPremultipliedLast | kCGBitmapByteOrder32Big);
	CGColorSpaceRelease(cs);
	if (c == NULL) {
		return NULL;
	}
	if (a > 0) {
		CGContextSetRGBFillColor(c, r, g, b, a);
		CGContextFillRect(c, CGRectMake(0, 0, w, h));
	}
	CGContextTranslateCTM(c, 0, h);
	CGContextScaleCTM(c, ppd, -ppd);
	return c;
}

static void dxtProbeRead(CGContextRef c, unsigned char *out, size_t n) {
	memcpy(out, CGBitmapContextGetData(c), n);
}
*/
import "C"

import "downex/ui/kit"

// renderText draws l at origin in c into a w x h pixel bitmap at ppd pixels per DIP over
// the opaque background bg (transparent when bg.A is 0) and returns its premultiplied
// RGBA pixels, rows top down. It is the text engine's own check of draw, for tests and for
// calibration against WebKit's pixels; windows draw through the canvas.
func renderText(l *textLayout, w, h int, ppd float32, origin kit.Point, c, bg kit.Color) []byte {
	ctx := C.dxtProbeContext(C.int(w), C.int(h), C.double(ppd), C.double(bg.R), C.double(bg.G), C.double(bg.B), C.double(bg.A))
	if ctx == 0 {
		return nil
	}
	defer C.CGContextRelease(ctx)
	l.draw(ctx, origin, c)
	out := make([]byte, 4*w*h)
	C.dxtProbeRead(ctx, (*C.uchar)(&out[0]), C.size_t(len(out)))
	return out
}
