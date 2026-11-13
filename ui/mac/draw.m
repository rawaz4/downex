//go:build darwin && cgo

#include "draw.h"
#include <Accelerate/Accelerate.h>
#include <dispatch/dispatch.h>
#include <math.h>
#include <stdlib.h>
#include <string.h>
#include <sys/mman.h>
#include <unistd.h>

CGColorSpaceRef dx_srgb(void) {
	static CGColorSpaceRef s;
	if (!s) s = CGColorSpaceCreateWithName(kCGColorSpaceSRGB);
	return s;
}

CGColorSpaceRef dx_gray(void) {
	static CGColorSpaceRef s;
	if (!s) s = CGColorSpaceCreateDeviceGray();
	return s;
}

static const uint32_t bgraInfo = kCGImageAlphaPremultipliedFirst | kCGBitmapByteOrder32Little;

void dx_bitmap_clear(CGContextRef c, int opaque) {
	@autoreleasepool {
		uint8_t *p = CGBitmapContextGetData(c);
		size_t bpr = CGBitmapContextGetBytesPerRow(c), h = CGBitmapContextGetHeight(c), w = CGBitmapContextGetWidth(c);
		memset(p, 0, bpr * h);
		if (opaque) { // opaque black, as a target without alpha starts
			for (size_t y = 0; y < h; y++) {
				uint8_t *row = p + y * bpr;
				for (size_t x = 0; x < w; x++) row[4 * x + 3] = 255;
			}
		}
	}
}

CGContextRef dx_bitmap_new(int w, int h, int opaque) {
	@autoreleasepool {
		CGContextRef c = CGBitmapContextCreate(NULL, w, h, 8, 0, dx_srgb(), bgraInfo);
		if (!c) return NULL;
		dx_bitmap_clear(c, opaque);
		return c;
	}
}

// dx_bitmap_copy copies src's pixels into dst from the top-left corner, as much as fits.
void dx_bitmap_copy(CGContextRef dst, CGContextRef src) {
	@autoreleasepool {
		uint8_t *d = CGBitmapContextGetData(dst), *s = CGBitmapContextGetData(src);
		size_t dbpr = CGBitmapContextGetBytesPerRow(dst), sbpr = CGBitmapContextGetBytesPerRow(src);
		size_t w = CGBitmapContextGetWidth(dst), sw = CGBitmapContextGetWidth(src);
		size_t h = CGBitmapContextGetHeight(dst), sh = CGBitmapContextGetHeight(src);
		if (sw < w) w = sw;
		if (sh < h) h = sh;
		for (size_t y = 0; y < h; y++) memcpy(d + y * dbpr, s + y * sbpr, 4 * w);
	}
}

static CGRect rectOf(dx_rect r) { return CGRectMake(r.x0, r.y0, r.x1 - r.x0, r.y1 - r.y0); }

void dx_frame_begin(CGContextRef c, int hpx, CGFloat ppd, dx_rect dirty, int clear) {
	@autoreleasepool {
		CGContextSaveGState(c);
		CGContextTranslateCTM(c, 0, hpx);
		CGContextScaleCTM(c, ppd, -ppd);
		CGContextClipToRect(c, rectOf(dirty));
		if (clear) CGContextClearRect(c, rectOf(dirty));
		CGContextSetFillColorSpace(c, dx_srgb());
		CGContextSetStrokeColorSpace(c, dx_srgb());
		CGContextSetShouldAntialias(c, true);
		CGContextSetAllowsAntialiasing(c, true);
		// app.css: -webkit-font-smoothing: antialiased, grayscale antialiasing without smoothing
		CGContextSetAllowsFontSmoothing(c, false);
		CGContextSetShouldSmoothFonts(c, false);
		CGContextSetMiterLimit(c, 4);
		CGContextSetInterpolationQuality(c, kCGInterpolationDefault);
		CGContextSetBlendMode(c, kCGBlendModeNormal);
	}
}

void dx_frame_end(CGContextRef c) {
	@autoreleasepool {
		CGContextRestoreGState(c);
	}
}

static void fillColor(CGContextRef c, dx_paint p) {
	CGFloat v[4] = {p.r, p.g, p.b, p.a};
	CGContextSetFillColor(c, v);
}

static void strokeWith(CGContextRef c, dx_paint p) {
	CGFloat v[4] = {p.r, p.g, p.b, p.a};
	CGContextSetStrokeColor(c, v);
	CGContextSetLineWidth(c, p.width);
	CGContextSetLineCap(c, (CGLineCap)p.cap);   // kit's order is CoreGraphics'
	CGContextSetLineJoin(c, (CGLineJoin)p.join);
	CGContextSetMiterLimit(c, 4);
	if (p.off > 0 && p.on >= 0) {
		CGFloat d[2] = {p.on, p.off};
		CGContextSetLineDash(c, 0, d, 2);
	} else {
		CGContextSetLineDash(c, 0, NULL, 0);
	}
}

void dx_fill_rect(CGContextRef c, dx_rect r, dx_paint p) {
	@autoreleasepool {
		fillColor(c, p);
		CGContextFillRect(c, rectOf(r));
	}
}

void dx_stroke_rect(CGContextRef c, dx_rect r, dx_paint p) {
	@autoreleasepool {
		strokeWith(c, p);
		CGContextBeginPath(c);
		CGContextAddRect(c, rectOf(r));
		CGContextStrokePath(c);
	}
}

// addRRect adds the rounded rectangle: straight sides joined by quarter circles, from the
// top-left corner's end clockwise on the screen.
static void addRRect(CGContextRef c, dx_rect r, dx_radii q) {
	CGContextMoveToPoint(c, r.x0 + q.tl, r.y0);
	CGContextAddArcToPoint(c, r.x1, r.y0, r.x1, r.y1, q.tr);
	CGContextAddArcToPoint(c, r.x1, r.y1, r.x0, r.y1, q.br);
	CGContextAddArcToPoint(c, r.x0, r.y1, r.x0, r.y0, q.bl);
	CGContextAddArcToPoint(c, r.x0, r.y0, r.x1, r.y0, q.tl);
	CGContextClosePath(c);
}

void dx_fill_rrect(CGContextRef c, dx_rect r, dx_radii q, dx_paint p) {
	@autoreleasepool {
		fillColor(c, p);
		CGContextBeginPath(c);
		addRRect(c, r, q);
		CGContextFillPath(c);
	}
}

void dx_stroke_rrect(CGContextRef c, dx_rect r, dx_radii q, dx_paint p) {
	@autoreleasepool {
		strokeWith(c, p);
		CGContextBeginPath(c);
		addRRect(c, r, q);
		CGContextStrokePath(c);
	}
}

void dx_fill_ellipse(CGContextRef c, dx_rect r, dx_paint p) {
	@autoreleasepool {
		fillColor(c, p);
		CGContextFillEllipseInRect(c, rectOf(r));
	}
}

void dx_stroke_ellipse(CGContextRef c, dx_rect r, dx_paint p) {
	@autoreleasepool {
		strokeWith(c, p);
		CGContextBeginPath(c);
		CGContextAddEllipseInRect(c, rectOf(r));
		CGContextStrokePath(c);
	}
}

void dx_line(CGContextRef c, CGFloat ax, CGFloat ay, CGFloat bx, CGFloat by, dx_paint p) {
	@autoreleasepool {
		strokeWith(c, p);
		CGContextBeginPath(c);
		CGContextMoveToPoint(c, ax, ay);
		CGContextAddLineToPoint(c, bx, by);
		CGContextStrokePath(c);
	}
}

void dx_fill_path(CGContextRef c, CGPathRef path, int evenOdd, dx_paint p) {
	@autoreleasepool {
		fillColor(c, p);
		CGContextBeginPath(c);
		CGContextAddPath(c, path);
		if (evenOdd) CGContextEOFillPath(c);
		else CGContextFillPath(c);
	}
}

void dx_stroke_path(CGContextRef c, CGPathRef path, dx_paint p) {
	@autoreleasepool {
		strokeWith(c, p);
		CGContextBeginPath(c);
		CGContextAddPath(c, path);
		CGContextStrokePath(c);
	}
}

// dx_ring fills outer less inner: a sharp shadow (focus rings, hairline insets).
void dx_ring(CGContextRef c, dx_rect outer, dx_radii oq, int hasInner, dx_rect inner, dx_radii iq, dx_paint p) {
	@autoreleasepool {
		CGContextSaveGState(c);
		if (hasInner) {
			CGRect all = CGRectUnion(rectOf(outer), rectOf(inner));
			CGContextBeginPath(c);
			CGContextAddRect(c, CGRectInset(all, -1, -1));
			addRRect(c, inner, iq);
			CGContextEOClip(c);
		}
		fillColor(c, p);
		CGContextBeginPath(c);
		addRRect(c, outer, oq);
		CGContextFillPath(c);
		CGContextRestoreGState(c);
	}
}

// dx_fill_mask fills dst with the colour through mask (an 8-bit gray image of alpha values,
// drawn upright and unsmoothed into dst). A shadow's nine pieces meet edge to edge: under
// a transform that only scales and moves, dst is put on whole device pixels and filled
// without antialiasing, so that pieces meeting at a fractional position leave no light
// seam where two half-covered pixels would blend twice (both edges round alike, so the
// pieces still abut).
void dx_fill_mask(CGContextRef c, CGImageRef mask, dx_rect dst, dx_paint p) {
	@autoreleasepool {
		CGContextSaveGState(c);
		CGAffineTransform m = CGContextGetCTM(c);
		if (m.b == 0 && m.c == 0) {
			CGPoint a = CGContextConvertPointToDeviceSpace(c, CGPointMake(dst.x0, dst.y0));
			CGPoint b = CGContextConvertPointToDeviceSpace(c, CGPointMake(dst.x1, dst.y1));
			a = CGContextConvertPointToUserSpace(c, CGPointMake(round(a.x), round(a.y)));
			b = CGContextConvertPointToUserSpace(c, CGPointMake(round(b.x), round(b.y)));
			dst.x0 = fmin(a.x, b.x);
			dst.x1 = fmax(a.x, b.x);
			dst.y0 = fmin(a.y, b.y);
			dst.y1 = fmax(a.y, b.y);
			if (!(dst.x1 > dst.x0) || !(dst.y1 > dst.y0)) {
				CGContextRestoreGState(c);
				return;
			}
			CGContextSetShouldAntialias(c, false);
		}
		CGContextSetInterpolationQuality(c, kCGInterpolationNone);
		CGContextTranslateCTM(c, 0, dst.y0 + dst.y1);
		CGContextScaleCTM(c, 1, -1);
		CGRect r = rectOf(dst);
		CGContextClipToMask(c, r, mask);
		fillColor(c, p);
		CGContextFillRect(c, r);
		CGContextRestoreGState(c);
	}
}

CGGradientRef dx_gradient_new(const CGFloat *comps, const CGFloat *locs, int n) {
	@autoreleasepool {
		return CGGradientCreateWithColorComponents(dx_srgb(), comps, locs, n);
	}
}

void dx_draw_gradient(CGContextRef c, CGGradientRef g, dx_rect r, CGFloat sx, CGFloat sy, CGFloat ex, CGFloat ey) {
	@autoreleasepool {
		CGContextSaveGState(c);
		CGContextClipToRect(c, rectOf(r));
		CGContextDrawLinearGradient(c, g, CGPointMake(sx, sy), CGPointMake(ex, ey),
			kCGGradientDrawsBeforeStartLocation | kCGGradientDrawsAfterEndLocation);
		CGContextRestoreGState(c);
	}
}

static void drawRepeating(CGContextRef c, CGGradientRef g, dx_rect r, CGFloat px, CGFloat py, CGFloat vx, CGFloat vy,
	CGFloat k0, int n, int m);

void dx_draw_repeating(CGContextRef c, CGGradientRef g, dx_rect r, CGFloat px, CGFloat py, CGFloat vx, CGFloat vy,
	CGFloat k0, int n, int m) {
	@autoreleasepool {
		drawRepeating(c, g, r, px, py, vx, vy, k0, n, m);
	}
}

static void drawRepeating(CGContextRef c, CGGradientRef g, dx_rect r, CGFloat px, CGFloat py, CGFloat vx, CGFloat vy,
	CGFloat k0, int n, int m) {
	CGFloat l =sqrt(vx * vx + vy * vy);
	if (!(l > 0) || m < 1) return;
	CGFloat wx = -vy / l, wy = vx / l; // across the line
	CGFloat cx = (r.x0 + r.x1) / 2, cy = (r.y0 + r.y1) / 2;
	CGFloat diag = hypot(r.x1 - r.x0, r.y1 - r.y0);
	CGFloat tc = (cx - px) * wx + (cy - py) * wy;
	CGFloat t0 = tc - diag, t1 = tc + diag;
	CGContextSaveGState(c);
	CGContextClipToRect(c, rectOf(r));
	for (int k = 0; k < n; k += m) {
		CGFloat ka = k0 + k, kb = ka + m;
		CGFloat ax = px + vx * ka, ay = py + vy * ka, bx = px + vx * kb, by = py + vy * kb;
		CGContextSaveGState(c);
		CGContextSetShouldAntialias(c, false);
		CGContextBeginPath(c);
		CGContextMoveToPoint(c, ax + wx * t0, ay + wy * t0);
		CGContextAddLineToPoint(c, bx + wx * t0, by + wy * t0);
		CGContextAddLineToPoint(c, bx + wx * t1, by + wy * t1);
		CGContextAddLineToPoint(c, ax + wx * t1, ay + wy * t1);
		CGContextClosePath(c);
		CGContextClip(c);
		CGContextDrawLinearGradient(c, g, CGPointMake(ax, ay), CGPointMake(bx, by),
			kCGGradientDrawsBeforeStartLocation | kCGGradientDrawsAfterEndLocation);
		CGContextRestoreGState(c);
	}
	CGContextRestoreGState(c);
}

CGImageRef dx_mask_new(const uint8_t *pix, int w, int h) {
	@autoreleasepool {
		CFDataRef data = CFDataCreate(NULL, pix, (CFIndex)w * h);
		if (!data) return NULL;
		CGDataProviderRef prov = CGDataProviderCreateWithCFData(data);
		CFRelease(data);
		if (!prov) return NULL;
		CGImageRef img = CGImageCreate(w, h, 8, 8, w, dx_gray(), (CGBitmapInfo)kCGImageAlphaNone, prov, NULL, false,
			kCGRenderingIntentDefault);
		CGDataProviderRelease(prov);
		return img;
	}
}

CGImageRef dx_image_crop(CGImageRef img, int x, int y, int w, int h) {
	@autoreleasepool {
		return CGImageCreateWithImageInRect(img, CGRectMake(x, y, w, h));
	}
}

// orientedImage is img (the first image of src) turned upright by its EXIF/TIFF
// orientation, as WebKit draws an <img> (CSS image-orientation: from-image is the
// default): a new +1 image, or NULL when it is upright already or cannot be turned.
static CGImageRef orientedImage(CGImageSourceRef src, CGImageRef img) {
	CFDictionaryRef props = CGImageSourceCopyPropertiesAtIndex(src, 0, NULL);
	if (!props) return NULL;
	int o = 1;
	CFNumberRef n = CFDictionaryGetValue(props, kCGImagePropertyOrientation);
	if (n) CFNumberGetValue(n, kCFNumberIntType, &o);
	CFRelease(props);
	if (o < 2 || o > 8) return NULL;
	size_t w = CGImageGetWidth(img), h = CGImageGetHeight(img);
	size_t ow = o >= 5 ? h : w, oh = o >= 5 ? w : h; // 5-8 swap the sides
	CGColorSpaceRef cs = CGImageGetColorSpace(img);
	if (!cs || CGColorSpaceGetModel(cs) != kCGColorSpaceModelRGB) cs = dx_srgb();
	CGContextRef c = CGBitmapContextCreate(NULL, ow, oh, 8, 0, cs, bgraInfo);
	if (!c) return NULL;
	// The transforms of TIFF orientations 2-8 in CoreGraphics' y-up space: each maps the
	// stored image onto the upright one.
	CGAffineTransform t = CGAffineTransformIdentity;
	switch (o) {
	case 2: t = CGAffineTransformMake(-1, 0, 0, 1, w, 0); break;
	case 3: t = CGAffineTransformMake(-1, 0, 0, -1, w, h); break;
	case 4: t = CGAffineTransformMake(1, 0, 0, -1, 0, h); break;
	case 5: t = CGAffineTransformMake(0, -1, -1, 0, h, w); break;
	case 6: t = CGAffineTransformMake(0, -1, 1, 0, 0, w); break;
	case 7: t = CGAffineTransformMake(0, 1, 1, 0, 0, 0); break;
	case 8: t = CGAffineTransformMake(0, 1, -1, 0, h, 0); break;
	}
	CGContextConcatCTM(c, t);
	CGContextSetInterpolationQuality(c, kCGInterpolationNone);
	CGContextSetBlendMode(c, kCGBlendModeCopy);
	CGContextDrawImage(c, CGRectMake(0, 0, w, h), img);
	CGImageRef out = CGBitmapContextCreateImage(c);
	CGContextRelease(c);
	return out;
}

CGImageRef dx_image_decode(const void *bytes, long n, int *status) {
	@autoreleasepool {
		*status = 2;
		CFDataRef data = CFDataCreate(NULL, bytes, n);
		if (!data) return NULL;
		CGImageSourceRef src = CGImageSourceCreateWithData(data, NULL);
		CFRelease(data);
		if (!src) {
			*status = 1;
			return NULL;
		}
		CGImageRef img = NULL;
		if (!CGImageSourceGetType(src)) {
			*status = 1;
		} else if (CGImageSourceGetCount(src) > 0 && CGImageSourceGetStatusAtIndex(src, 0) == kCGImageStatusComplete) {
			const void *keys[] = {kCGImageSourceShouldCacheImmediately, kCGImageSourceShouldCache};
			const void *vals[] = {kCFBooleanTrue, kCFBooleanTrue};
			CFDictionaryRef opts = CFDictionaryCreate(NULL, keys, vals, 2, &kCFTypeDictionaryKeyCallBacks,
				&kCFTypeDictionaryValueCallBacks);
			img = CGImageSourceCreateImageAtIndex(src, 0, opts);
			CFRelease(opts);
			if (img) {
				CGImageRef up = orientedImage(src, img);
				if (up) {
					CGImageRelease(img);
					img = up;
				}
			}
			if (img) *status = 0;
		}
		CFRelease(src);
		return img;
	}
}

void dx_draw_image(CGContextRef c, CGImageRef img, dx_rect full, dx_rect clip) {
	@autoreleasepool {
		CGContextSaveGState(c);
		CGContextClipToRect(c, rectOf(clip));
		CGContextTranslateCTM(c, 0, full.y0 + full.y1);
		CGContextScaleCTM(c, 1, -1);
		CGContextDrawImage(c, rectOf(full), img);
		CGContextRestoreGState(c);
	}
}

void dx_push_clip(CGContextRef c, dx_rect r) {
	@autoreleasepool {
		CGContextSaveGState(c);
		CGContextClipToRect(c, rectOf(r));
	}
}

// dx_push_clip_screen clips to screen (window DIPs, untransformed) under any transform.
void dx_push_clip_screen(CGContextRef c, dx_rect screen, int hpx, CGFloat ppd) {
	@autoreleasepool {
		CGContextSaveGState(c);
		CGAffineTransform ctm = CGContextGetCTM(c);
		CGAffineTransform base = CGAffineTransformScale(CGAffineTransformMakeTranslation(0, hpx), ppd, -ppd);
		CGContextConcatCTM(c, CGAffineTransformInvert(ctm));
		CGContextConcatCTM(c, base);
		CGContextClipToRect(c, rectOf(screen));
		CGContextConcatCTM(c, CGAffineTransformInvert(base));
		CGContextConcatCTM(c, ctm);
	}
}

void dx_push_rclip(CGContextRef c, dx_rect r, dx_radii q) {
	@autoreleasepool {
		CGContextSaveGState(c);
		CGContextBeginPath(c);
		addRRect(c, r, q);
		CGContextClip(c);
	}
}

void dx_push_layer(CGContextRef c, CGFloat alpha) {
	@autoreleasepool {
		CGContextSaveGState(c);
		CGContextSetAlpha(c, alpha);
		CGContextBeginTransparencyLayerWithRect(c, CGContextGetClipBoundingBox(c), NULL);
	}
}

void dx_push_transform(CGContextRef c, CGAffineTransform m) {
	@autoreleasepool {
		CGContextSaveGState(c);
		CGContextConcatCTM(c, m);
	}
}

void dx_pop(CGContextRef c, int layer) {
	@autoreleasepool {
		if (layer) CGContextEndTransparencyLayer(c);
		CGContextRestoreGState(c);
	}
}

// The blur's scratch is mapped memory, not malloc's: a full window's area is megabytes,
// and malloc keeps freed large blocks dirty in its cache, where they count against the
// process's footprint until memory runs short.
static void unmapScratch(void *p, size_t cap) {
	if (p && cap) munmap(p, cap);
}

void dx_blur_free(dx_blur *b) {
	@autoreleasepool {
		unmapScratch(b->buf, b->cap);
		unmapScratch(b->tmp, b->tmpCap);
		b->buf = b->tmp = NULL;
		b->cap = b->tmpCap = 0;
	}
}

static int grow(void **p, size_t *cap, size_t need) {
	if (*cap >= need) return 1;
	size_t page = (size_t)getpagesize();
	size_t n = (need + page - 1) / page * page;
	void *m = mmap(NULL, n, PROT_READ | PROT_WRITE, MAP_PRIVATE | MAP_ANON, -1, 0);
	if (m == MAP_FAILED) return 0;
	unmapScratch(*p, *cap);
	*p = m;
	*cap = n;
	return 1;
}

int dx_blur_backdrop(CGContextRef c, dx_blur *b, int x0, int y0, int x1, int y1, float sigma, CGFloat alpha) {
	@autoreleasepool {
		uint8_t *data = CGBitmapContextGetData(c);
		size_t bpr = CGBitmapContextGetBytesPerRow(c);
		int w = x1 - x0, h = y1 - y0;
		if (!data || w <= 0 || h <= 0) return 0;
		size_t rb = (size_t)w * 4, need = rb * h;
		if (!grow(&b->buf, &b->cap, need) || !grow(&b->tmp, &b->tmpCap, need)) return 0;
		if (b->kn == 0 || b->sigma != sigma) {
			int rad = (int)ceilf(3 * sigma);
			if (rad > 128) rad = 128;
			if (rad < 1) rad = 1;
			b->kn = 2 * rad + 1;
			b->sigma = sigma;
			for (int i = 0; i < b->kn; i++) {
				float x = (float)(i - rad);
				b->kern[i] = (int16_t)lroundf(4096 * expf(-x * x / (2 * sigma * sigma)));
			}
		}
		int32_t div = 0;
		for (int i = 0; i < b->kn; i++) div += b->kern[i];
		// The area is read where it is (an opaque target's alpha is 1 everywhere: its pixels
		// start opaque and every drawing blends over them): rows, then columns, into buf.
		vImage_Buffer area = {data + (size_t)y0 * bpr + (size_t)x0 * 4, (vImagePixelCount)h, (vImagePixelCount)w, bpr};
		vImage_Buffer tmp = {b->tmp, (vImagePixelCount)h, (vImagePixelCount)w, rb};
		vImage_Buffer out = {b->buf, (vImagePixelCount)h, (vImagePixelCount)w, rb};
		Pixel_8888 none = {0, 0, 0, 0}; // outside the area: transparent (a hard border)
		if (vImageConvolve_ARGB8888(&area, &tmp, NULL, 0, 0, b->kern, 1, b->kn, div, none, kvImageBackgroundColorFill) != kvImageNoError ||
			vImageConvolve_ARGB8888(&tmp, &out, NULL, 0, 0, b->kern, b->kn, 1, div, none, kvImageBackgroundColorFill) != kvImageNoError) {
			return 0;
		}
		// Source-over at alpha, straight into the bitmap: every pixel of the area lies wholly
		// inside the clip (the area is the clip's whole pixels), so no clip is needed, and
		// drawing a CGImage of it through CoreGraphics cost six times the blur. Bands of rows
		// blend on every core.
		unsigned A = (unsigned)lround(alpha * 255);
		const uint8_t *buf = b->buf;
		size_t bands = (size_t)(h + 63) / 64;
		dispatch_apply(bands, DISPATCH_APPLY_AUTO, ^(size_t band) {
			int ya = (int)band * 64, yb = ya + 64 < h ? ya + 64 : h;
			for (int y = ya; y < yb; y++) {
				const uint8_t *s = buf + (size_t)y * rb;
				uint8_t *d = data + (size_t)(y0 + y) * bpr + (size_t)x0 * 4;
				for (int x = 0; x < w; x++, s += 4, d += 4) {
					unsigned sa = (s[3] * A + 127) / 255, keep = 255 - sa;
					d[0] = (uint8_t)((s[0] * A + 127) / 255 + (d[0] * keep + 127) / 255);
					d[1] = (uint8_t)((s[1] * A + 127) / 255 + (d[1] * keep + 127) / 255);
					d[2] = (uint8_t)((s[2] * A + 127) / 255 + (d[2] * keep + 127) / 255);
					d[3] = (uint8_t)(sa + (d[3] * keep + 127) / 255);
				}
			}
		});
		return 1;
	}
}
