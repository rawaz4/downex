// The CoreGraphics side of ui/mac's canvas: one C call per kit.Canvas operation, so that a
// frame crosses into C once per primitive and passes only values (no Go pointers kept).
// Coordinates are window DIPs, y down; the target's base transform maps them to device
// pixels (dx_frame_begin).

#include <CoreGraphics/CoreGraphics.h>
#include <ImageIO/ImageIO.h>

// dx_paint is a colour (sRGB, straight alpha) and, for strokes, how to stroke.
typedef struct {
	CGFloat r, g, b, a;
	CGFloat width, on, off; // a dash when off > 0 and on >= 0
	int cap, join;          // kit.LineCap, kit.LineJoin
} dx_paint;

typedef struct {
	CGFloat x0, y0, x1, y1;
} dx_rect;

typedef struct {
	CGFloat tl, tr, br, bl;
} dx_radii;

CGColorSpaceRef dx_srgb(void);
CGColorSpaceRef dx_gray(void);

CGContextRef dx_bitmap_new(int w, int h, int opaque);
void dx_bitmap_copy(CGContextRef dst, CGContextRef src);
void dx_bitmap_clear(CGContextRef c, int opaque);

void dx_frame_begin(CGContextRef c, int hpx, CGFloat ppd, dx_rect dirty, int clear);
void dx_frame_end(CGContextRef c);

void dx_fill_rect(CGContextRef c, dx_rect r, dx_paint p);
void dx_stroke_rect(CGContextRef c, dx_rect r, dx_paint p);
void dx_fill_rrect(CGContextRef c, dx_rect r, dx_radii q, dx_paint p);
void dx_stroke_rrect(CGContextRef c, dx_rect r, dx_radii q, dx_paint p);
void dx_fill_ellipse(CGContextRef c, dx_rect r, dx_paint p);
void dx_stroke_ellipse(CGContextRef c, dx_rect r, dx_paint p);
void dx_line(CGContextRef c, CGFloat ax, CGFloat ay, CGFloat bx, CGFloat by, dx_paint p);
void dx_fill_path(CGContextRef c, CGPathRef path, int evenOdd, dx_paint p);
void dx_stroke_path(CGContextRef c, CGPathRef path, dx_paint p);
void dx_ring(CGContextRef c, dx_rect outer, dx_radii oq, int hasInner, dx_rect inner, dx_radii iq, dx_paint p);
void dx_fill_mask(CGContextRef c, CGImageRef mask, dx_rect dst, dx_paint p);

CGGradientRef dx_gradient_new(const CGFloat *comps, const CGFloat *locs, int n);
void dx_draw_gradient(CGContextRef c, CGGradientRef g, dx_rect r, CGFloat sx, CGFloat sy, CGFloat ex, CGFloat ey);
// dx_draw_repeating fills r with g (laid out over m periods) once per chunk of m periods
// from period k0 for n periods along the line p + k·v: each chunk through an aliased clip
// to its band, so every pixel takes its colour from one chunk at its centre.
void dx_draw_repeating(CGContextRef c, CGGradientRef g, dx_rect r, CGFloat px, CGFloat py, CGFloat vx, CGFloat vy,
	CGFloat k0, int n, int m);

CGImageRef dx_mask_new(const uint8_t *pix, int w, int h);
CGImageRef dx_image_crop(CGImageRef img, int x, int y, int w, int h);
// dx_image_decode decodes the first image of data: *status is 0 on success, 1 when no
// decoder knows the format, 2 when the data is broken.
CGImageRef dx_image_decode(const void *data, long n, int *status);
void dx_draw_image(CGContextRef c, CGImageRef img, dx_rect full, dx_rect clip);

void dx_push_clip(CGContextRef c, dx_rect r);
void dx_push_clip_screen(CGContextRef c, dx_rect screen, int hpx, CGFloat ppd);
void dx_push_rclip(CGContextRef c, dx_rect r, dx_radii q);
void dx_push_layer(CGContextRef c, CGFloat alpha);
void dx_push_transform(CGContextRef c, CGAffineTransform m);
void dx_pop(CGContextRef c, int layer);

// dx_blur is BlurBackdrop's scratch: a copy of the area and vImage's temporary buffer.
typedef struct {
	void *buf, *tmp;
	size_t cap, tmpCap;
	int16_t kern[257];
	int kn;           // kernel length (odd), 0 before the first blur
	float sigma;      // the kernel's standard deviation in pixels
} dx_blur;

void dx_blur_free(dx_blur *b);
// dx_blur_backdrop blurs the device pixels [x0, x1) × [y0, y1) (rows from the top) of c's
// bitmap with a Gaussian of sigma pixels, outside the area transparent, and blends the
// result over them at alpha (source-over), the area being inside the clip in effect.
int dx_blur_backdrop(CGContextRef c, dx_blur *b, int x0, int y0, int x1, int y1, float sigma, CGFloat alpha);
