// [render] How a window's frames reach the screen without copies (present.go): the target
// draws into IOSurface memory with CoreGraphics, and the content view's layer shows the
// IOSurface itself. A window keeps two surfaces (three while the compositor still holds
// one), copying the previous frames' changed areas forward, so the pixels outside a frame's
// dirty rectangle are the last frame's, as kit.RenderTarget requires.
#ifndef DOWNEX_UI_MAC_PRESENT_H
#define DOWNEX_UI_MAC_PRESENT_H

#include <stdbool.h>
#include <stdint.h>
#include <CoreGraphics/CoreGraphics.h>
#include <IOSurface/IOSurfaceRef.h>

// dxr_surface_new makes a w × h BGRA surface (premultiplied, sRGB); with clear, cleared to
// opaque black when opaque, else to transparent (a spare that is copied over in full is
// not: its pages are touched only by that copy); NULL when it cannot be made.
IOSurfaceRef dxr_surface_new(int w, int h, int opaque, int clear);
// dxr_surface_context is a bitmap context over s's memory (sRGB, BGRA premultiplied), +1.
CGContextRef dxr_surface_context(IOSurfaceRef s, int w, int h);
// dxr_context_matches reports whether c still draws into s's memory.
int dxr_context_matches(CGContextRef c, IOSurfaceRef s);
int dxr_lock(IOSurfaceRef s, int readonly);
void dxr_unlock(IOSurfaceRef s, int readonly);
int dxr_in_use(IOSurfaceRef s);
// dxr_set_volatile marks s volatile (its memory may be taken back while it is) or not; it
// returns 1 when the surface comes back from volatile with its contents thrown away.
int dxr_set_volatile(IOSurfaceRef s, int vol);
// dxr_copy copies the pixels [x0, x1) × [y0, y1) (rows from the top) of src into dst; both
// are locked by the caller (src at least for reading).
void dxr_copy(IOSurfaceRef dst, IOSurfaceRef src, int x0, int y0, int x1, int y1);
void dxr_release(IOSurfaceRef s);
// dxr_relief gives back to the system the memory malloc keeps from freed blocks (Core
// Graphics' transparency layers are as large as the window while a dialog fades).
void dxr_relief(void);

// Frame pacing on macOS 14 and later: the content view's own display link (NSView
// displayLink), which follows the window to whichever screen shows it, at that screen's
// refresh, stops while the window is not on screen, and costs no memory, where a
// CVDisplayLink over every active display mapped 16 MB of display memory into the process.
// It runs only while a tick is wanted. dxrLinkAttach returns false before macOS 14 (the
// window then keeps its CVDisplayLink).
bool dxrLinkAttach(void *win, void *view, uintptr_t handle);
// dxrLinkRequest asks for one goWindowTick at the next refresh; false when the window has
// no view display link. A request the link has not served within a second (a link that
// stopped for a covered window before the window heard of it) is served by a fallback
// timer; a hidden window's own ticks come from present_window.go, not the link.
bool dxrLinkRequest(void *win);
// dxrWindowVisible reports whether any part of the window is visible (its occlusion state).
bool dxrWindowVisible(void *win);
// dxrLinkStop ends the display link of a closing window.
void dxrLinkStop(void *win);
// dxrLinkRunning reports whether the window's display link is running (tests).
bool dxrLinkRunning(void *win);

// For tests: the size of the IOSurface the window's layer shows (0 × 0 for none), and
// bringing a minimised window back.
void dxr_layer_surface_size(void *win, int *w, int *h);
void dxr_test_deminiaturize(void *win);
bool dxr_test_link_released(void);
// dxr_present shows s (y-down pixels of the whole content area at scale) as the content of
// the window win (an NSWindow), with no animation, in the current Core Animation
// transaction (so a live resize shows the new frame with the new size); NULL clears it.
void dxr_present(void *win, IOSurfaceRef s, double scale, int opaque);

#endif
