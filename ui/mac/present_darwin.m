//go:build darwin && cgo

// [render] The IOSurface side of a window's presentation (present_darwin.h, present.go).

#import <Cocoa/Cocoa.h>
#import <QuartzCore/QuartzCore.h>
#include <IOSurface/IOSurface.h>
#include <string.h>
#include <malloc/malloc.h>
#include <objc/runtime.h>
#include "present_darwin.h"
#include "_cgo_export.h"
#include "draw.h"
#include "window_darwin.h"

static const uint32_t dxrBGRA = kCGImageAlphaPremultipliedFirst | kCGBitmapByteOrder32Little;

static void dxrSetInt(CFMutableDictionaryRef d, CFStringRef k, long v) {
	CFNumberRef n = CFNumberCreate(NULL, kCFNumberLongType, &v);
	CFDictionarySetValue(d, k, n);
	CFRelease(n);
}

IOSurfaceRef dxr_surface_new(int w, int h, int opaque, int clear) {
	@autoreleasepool {
		if (w <= 0 || h <= 0) return NULL;
		size_t bpr = IOSurfaceAlignProperty(kIOSurfaceBytesPerRow, (size_t)w * 4);
		CFMutableDictionaryRef p = CFDictionaryCreateMutable(NULL, 0, &kCFTypeDictionaryKeyCallBacks,
			&kCFTypeDictionaryValueCallBacks);
		dxrSetInt(p, kIOSurfaceWidth, w);
		dxrSetInt(p, kIOSurfaceHeight, h);
		dxrSetInt(p, kIOSurfaceBytesPerElement, 4);
		dxrSetInt(p, kIOSurfaceBytesPerRow, (long)bpr);
		dxrSetInt(p, kIOSurfaceAllocSize, (long)(bpr * (size_t)h));
		dxrSetInt(p, kIOSurfacePixelFormat, 'BGRA');
		IOSurfaceRef s = IOSurfaceCreate(p);
		CFRelease(p);
		if (!s) return NULL;
		// The compositor converts the pixels from sRGB to the display, as WebKit's are.
		CFPropertyListRef cs = CGColorSpaceCopyPropertyList(dx_srgb());
		if (cs) {
			IOSurfaceSetValue(s, kIOSurfaceColorSpace, cs);
			CFRelease(cs);
		}
		if (!clear) return s;
		IOSurfaceLock(s, 0, NULL);
		uint8_t *b = IOSurfaceGetBaseAddress(s);
		size_t rb = IOSurfaceGetBytesPerRow(s);
		memset(b, 0, rb * (size_t)h);
		if (opaque) {
			for (int y = 0; y < h; y++) {
				uint8_t *row = b + (size_t)y * rb;
				for (int x = 0; x < w; x++) row[4 * x + 3] = 255;
			}
		}
		IOSurfaceUnlock(s, 0, NULL);
		return s;
	}
}

CGContextRef dxr_surface_context(IOSurfaceRef s, int w, int h) {
	return CGBitmapContextCreate(IOSurfaceGetBaseAddress(s), w, h, 8, IOSurfaceGetBytesPerRow(s), dx_srgb(), dxrBGRA);
}

int dxr_context_matches(CGContextRef c, IOSurfaceRef s) {
	return c && CGBitmapContextGetData(c) == IOSurfaceGetBaseAddress(s);
}

int dxr_lock(IOSurfaceRef s, int readonly) {
	return IOSurfaceLock(s, readonly ? kIOSurfaceLockReadOnly : 0, NULL) == kIOReturnSuccess;
}

void dxr_unlock(IOSurfaceRef s, int readonly) { IOSurfaceUnlock(s, readonly ? kIOSurfaceLockReadOnly : 0, NULL); }

int dxr_in_use(IOSurfaceRef s) { return IOSurfaceIsInUse(s) ? 1 : 0; }

int dxr_set_volatile(IOSurfaceRef s, int vol) {
	uint32_t old = 0;
	if (IOSurfaceSetPurgeable(s, vol ? kIOSurfacePurgeableVolatile : kIOSurfacePurgeableNonVolatile, &old) != KERN_SUCCESS) {
		return 0;
	}
	return !vol && old == kIOSurfacePurgeableEmpty;
}

void dxr_copy(IOSurfaceRef dst, IOSurfaceRef src, int x0, int y0, int x1, int y1) {
	uint8_t *d = IOSurfaceGetBaseAddress(dst);
	const uint8_t *s = IOSurfaceGetBaseAddress(src);
	size_t dr = IOSurfaceGetBytesPerRow(dst), sr = IOSurfaceGetBytesPerRow(src);
	int w = (int)IOSurfaceGetWidth(dst), h = (int)IOSurfaceGetHeight(dst);
	if ((int)IOSurfaceGetWidth(src) < w) w = (int)IOSurfaceGetWidth(src);
	if ((int)IOSurfaceGetHeight(src) < h) h = (int)IOSurfaceGetHeight(src);
	if (x0 < 0) x0 = 0;
	if (y0 < 0) y0 = 0;
	if (x1 > w) x1 = w;
	if (y1 > h) y1 = h;
	if (x1 <= x0 || y1 <= y0) return;
	size_t n = (size_t)(x1 - x0) * 4;
	for (int y = y0; y < y1; y++) memcpy(d + (size_t)y * dr + (size_t)x0 * 4, s + (size_t)y * sr + (size_t)x0 * 4, n);
}

// ---- the view's display link ----------------------------------------------------------

// dxrFallbackAfter is how long a wanted tick waits for the display link before the
// fallback timer serves it (a link that stopped firing, as for a covered window).
static const double dxrFallbackAfter = 1.0;

@interface DXRLinkTarget : NSObject
@property(nonatomic) uintptr_t handle;
@property(nonatomic) bool wanted;
@property(nonatomic) bool fallbackArmed;
@property(nonatomic) CFTimeInterval lastFire; // when the link last fired (CACurrentMediaTime)
@property(nonatomic, strong) id link;         // a CADisplayLink (macOS 14+)
- (void)tick:(id)l;
- (void)armFallback;
@end

@implementation DXRLinkTarget
- (void)tick:(id)l {
	self.lastFire = CACurrentMediaTime();
	if (!self.wanted) {
		[l setPaused:YES];
		return;
	}
	self.wanted = false;
	goWindowTick(self.handle);
	if (!self.wanted && self.link) [self.link setPaused:YES]; // idle: no ticks, no wake-ups
}
// armFallback serves a wanted tick the link has not served within dxrFallbackAfter.
- (void)armFallback {
	if (self.fallbackArmed) return;
	self.fallbackArmed = true;
	__weak DXRLinkTarget *weakSelf = self;
	dispatch_after(dispatch_time(DISPATCH_TIME_NOW, (int64_t)(dxrFallbackAfter * NSEC_PER_SEC)), dispatch_get_main_queue(), ^{
		DXRLinkTarget *t = weakSelf;
		if (!t) return;
		t.fallbackArmed = false;
		if (!t.link || !t.wanted) return; // stopped, or served
		if (CACurrentMediaTime() - t.lastFire >= dxrFallbackAfter * 0.9) {
			t.wanted = false;
			goWindowTick(t.handle);
		}
		if (t.link && t.wanted) [t armFallback];
	});
}
@end

static char dxrLinkKey;

static DXRLinkTarget *dxrTargetOf(void *win) {
	if (!win) return nil;
	return objc_getAssociatedObject((__bridge id)win, &dxrLinkKey);
}

bool dxrLinkAttach(void *win, void *view, uintptr_t handle) {
	if (@available(macOS 14.0, *)) {
		@autoreleasepool {
			DXRLinkTarget *t = [DXRLinkTarget new];
			t.handle = handle;
			NSView *v = (__bridge NSView *)view;
			CADisplayLink *l = [v displayLinkWithTarget:t selector:@selector(tick:)];
			if (!l) return false;
			l.paused = YES;
			[l addToRunLoop:NSRunLoop.mainRunLoop forMode:NSRunLoopCommonModes];
			t.link = l;
			objc_setAssociatedObject((__bridge id)win, &dxrLinkKey, t, OBJC_ASSOCIATION_RETAIN_NONATOMIC);
			return true;
		}
	}
	return false;
}

bool dxrLinkRequest(void *win) {
	DXRLinkTarget *t = dxrTargetOf(win);
	if (!t) return false;
	if (t.link) {
		t.wanted = true;
		[t.link setPaused:NO];
		[t armFallback];
	}
	return true;
}

bool dxrWindowVisible(void *win) {
	return (((__bridge NSWindow *)win).occlusionState & NSWindowOcclusionStateVisible) != 0;
}

void dxrLinkStop(void *win) {
	@autoreleasepool {
		DXRLinkTarget *t = dxrTargetOf(win);
		if (!t || !t.link) return;
		t.wanted = false;
		[t.link invalidate]; // the run loop lets go of it, and it of its target
		t.link = nil;
	}
}

bool dxrLinkRunning(void *win) {
	DXRLinkTarget *t = dxrTargetOf(win);
	return t && t.link && ![t.link isPaused];
}

void dxr_relief(void) { malloc_zone_pressure_relief(NULL, 0); }

void dxr_release(IOSurfaceRef r) {
	if (r) CFRelease(r);
}

void dxr_present(void *p, IOSurfaceRef s, double scale, int opaque) {
	@autoreleasepool {
		NSWindow *w = (__bridge NSWindow *)p;
		CALayer *layer = w.contentView.layer;
		if (!layer) return;
		[CATransaction begin];
		[CATransaction setDisableActions:YES];
		layer.contentsScale = scale;
		layer.opaque = opaque ? YES : NO;
		layer.contents = (__bridge id)s;
		[CATransaction commit];
	}
}

// ---- for tests ----------------------------------------------------------------------

void dxr_layer_surface_size(void *p, int *w, int *h) {
	@autoreleasepool {
		*w = *h = 0;
		NSWindow *win = (__bridge NSWindow *)p;
		id c = win.contentView.layer.contents;
		if (c && CFGetTypeID((__bridge CFTypeRef)c) == IOSurfaceGetTypeID()) {
			IOSurfaceRef s = (__bridge IOSurfaceRef)c;
			*w = (int)IOSurfaceGetWidth(s);
			*h = (int)IOSurfaceGetHeight(s);
		}
	}
}

void dxr_test_deminiaturize(void *p) {
	@autoreleasepool {
		[(__bridge NSWindow *)p deminiaturize:nil];
	}
}

// dxr_test_link_released makes a window and releases it without closing it (NewWindow's
// error path), and reports whether its display link was let go (true before macOS 14).
bool dxr_test_link_released(void) {
	__weak id link = nil;
	@autoreleasepool {
		void *w = dxWindowNew(0, 100, 80, "dxr link");
		if (!w) return false;
		DXRLinkTarget *t = dxrTargetOf(w);
		if (t) link = t.link;
		t = nil;
		dxWindowRelease(w);
	}
	return link == nil;
}
