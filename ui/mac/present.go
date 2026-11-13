//go:build darwin && cgo

package mac

// [render] A window's frames without copies. Before, every frame was turned into a CGImage
// of the bitmap (copy on write: the next frame's drawing copied the whole bitmap while the
// layer still held the image) and Core Animation uploaded it again: about 40 MB for a main
// window at 2×. Now the target draws straight into IOSurface memory and the layer shows the
// surface itself, so nothing is copied but the areas the last frames changed.
//
// A surface the compositor shows is not drawn into: each frame draws into another one,
// first bringing it up to date by copying from the shown surface the areas changed since it
// was last drawn (rndChain.hist, rectangle by rectangle: two small areas far apart are not
// copied as their bounds). Two surfaces take turns. The compositor lets go of the surface
// it showed before only some milliseconds after it has shown the next one, so a frame can
// find the spare still held. A frame that starts right after the last one ended (the last
// ran late: both are in one refresh) then waits for the next refresh (errRndBusy; the
// window keeps its dirty area and asks for the next tick); a frame on time takes a third
// surface at once, which goes again once frames have not needed it for rndThirdKeep.
// Waiting on time as well (or polling IOSurfaceIsInUse for a few milliseconds) was
// measured to cost frames: a 60 Hz animation of the main window under -live fell from
// about 26 to 24 shown frames a second waiting, and to 16-34 polling, so an animation
// keeps three surfaces, as WebKit's back buffers do.
//
// A second after the last frame the spare is marked volatile (the system may take its
// memory back; a surface that comes back without its contents is brought up to date by
// copying all of the shown one); after rndFreeAfter it is freed, because a volatile
// surface still counts in the process's footprint until the system purges it (10-25 s
// measured), so an idle window keeps one surface (main window idle at 1200×760, 2×:
// 52-55 MB before, 38-39 MB at 10 s).

/*
#cgo LDFLAGS: -framework IOSurface
#include "present_darwin.h"
*/
import "C"

import (
	"fmt"
	"math"
	"time"

	"downex/ui/kit"
)

// rndMaxSurfaces is how many surfaces a window keeps at most.
const rndMaxSurfaces = 3

// rndHistory is how many frames' dirty areas a chain remembers; a surface older than that
// is brought up to date by copying all of the shown one.
const rndHistory = 8

// rndThirdKeep is how long a third surface is kept after a frame last needed it.
const rndThirdKeep = 500 * time.Millisecond

// rndSameRefresh: a frame that starts sooner than this after the last one ended is in the
// same refresh (the last one ran late).
const rndSameRefresh = 8 * time.Millisecond

// errRndBusy: the compositor still holds the surface the frame would draw into; the frame
// is made at the next refresh (the window keeps its dirty area).
var errRndBusy = fmt.Errorf("mac: the window's surfaces are held by the compositor")

// errRndLock: a surface cannot be locked; the frame is lost like a device's (the window
// paints everything again), never shown with stale pixels.
var errRndLock = fmt.Errorf("mac: the window's surface cannot be locked: %w", kit.ErrDeviceLost)

type rndPixRect struct{ x0, y0, x1, y1 int }

func (r rndPixRect) empty() bool { return r.x1 <= r.x0 || r.y1 <= r.y0 }

func (r rndPixRect) area() int {
	if r.empty() {
		return 0
	}
	return (r.x1 - r.x0) * (r.y1 - r.y0)
}

func (r rndPixRect) union(o rndPixRect) rndPixRect {
	if r.empty() {
		return o
	}
	if o.empty() {
		return r
	}
	return rndPixRect{min(r.x0, o.x0), min(r.y0, o.y0), max(r.x1, o.x1), max(r.y1, o.y1)}
}

func (r rndPixRect) contains(o rndPixRect) bool {
	return o.empty() || (!r.empty() && r.x0 <= o.x0 && r.y0 <= o.y0 && r.x1 >= o.x1 && r.y1 >= o.y1)
}

type rndSurface struct {
	s        C.IOSurfaceRef
	ctx      C.CGContextRef
	frame    uint64 // the frame it holds; 0: its pixels are no frame's
	volatile bool
}

// rndChain is a window target's surfaces. It belongs to the main thread.
type rndChain struct {
	w, h   int
	opaque bool
	bufs   [rndMaxSurfaces]rndSurface
	front  int // the surface shown, -1 before the first frame
	cur    int // the surface the frame in progress draws into, -1 outside a frame
	frame  uint64
	hist   [rndHistory]struct {
		frame uint64
		r     rndPixRect
	}
	waited  bool      // the last frame asked for waited a refresh (errRndBusy)
	lastEnd time.Time // when the last frame ended
	thirdAt time.Time // when a frame last needed a third surface

	// Tests: the clock, a compositor that holds surfaces, failing allocations and locks.
	now       func() time.Time
	held      func(i int) bool
	failAlloc bool
	failLock  func(i int, readonly bool) bool
	// copied counts the pixels copied forward, allocs the surfaces made, waits the frames
	// that waited a refresh (tests and measurements).
	copied, allocs, waits int
}

func newRndChain(w, h int, opaque bool) (*rndChain, error) {
	ch := &rndChain{w: w, h: h, opaque: opaque, front: -1, cur: -1, now: time.Now}
	if err := ch.alloc(0); err != nil {
		return nil, err
	}
	return ch, nil
}

// alloc makes surface i. The first surface starts cleared; a spare is not cleared, since
// all of it is copied from the shown surface before it is drawn into (its frame is 0).
func (ch *rndChain) alloc(i int) error {
	if ch.failAlloc {
		return errInvalid("IOSurface %d×%d cannot be made (test)", ch.w, ch.h)
	}
	s := C.dxr_surface_new(C.int(ch.w), C.int(ch.h), cbool(ch.opaque), cbool(ch.surfaces() == 0))
	if s == 0 {
		return errInvalid("IOSurface %d×%d cannot be made", ch.w, ch.h)
	}
	ctx := C.dxr_surface_context(s, C.int(ch.w), C.int(ch.h))
	if ctx == 0 {
		C.dxr_release(s)
		return errInvalid("bitmap context over an IOSurface %d×%d", ch.w, ch.h)
	}
	ch.bufs[i] = rndSurface{s: s, ctx: ctx}
	ch.allocs++
	return nil
}

func (ch *rndChain) free(i int) {
	b := &ch.bufs[i]
	if b.ctx != 0 {
		C.CGContextRelease(b.ctx)
	}
	if b.s != 0 {
		C.dxr_release(b.s)
	}
	*b = rndSurface{}
}

func (ch *rndChain) lock(i int, readonly bool) bool {
	if ch.failLock != nil && ch.failLock(i, readonly) {
		return false
	}
	return C.dxr_lock(ch.bufs[i].s, cbool(readonly)) != 0
}

func (ch *rndChain) inUse(i int) bool {
	if ch.held != nil {
		return ch.held(i)
	}
	return C.dxr_in_use(ch.bufs[i].s) != 0
}

// close releases every surface (the layer keeps its own reference to the one it shows).
func (ch *rndChain) close() {
	if ch.cur >= 0 {
		C.dxr_unlock(ch.bufs[ch.cur].s, 0)
		ch.cur = -1
	}
	for i := range ch.bufs {
		ch.free(i)
	}
	ch.front = -1
}

// resize makes the chain w × h pixels: new surfaces, nothing shown yet from them (the
// window paints all of the new size next). The new surface is made first: when it cannot
// be, the chain stays as it was, usable at its old size.
func (ch *rndChain) resize(w, h int) error {
	n := &rndChain{w: w, h: h, opaque: ch.opaque, front: -1, cur: -1, now: ch.now,
		held: ch.held, failAlloc: ch.failAlloc, failLock: ch.failLock}
	if err := n.alloc(0); err != nil {
		return err
	}
	ch.close()
	n.copied, n.allocs, n.waits = ch.copied, ch.allocs+1, ch.waits
	*ch = *n
	return nil
}

// context is the context of the surface last drawn (or the first one): what the target's
// ctx is between frames; 0 when the chain has no surface.
func (ch *rndChain) context() C.CGContextRef {
	if ch.front >= 0 {
		return ch.bufs[ch.front].ctx
	}
	return ch.bufs[0].ctx
}

// pick chooses the surface the next frame draws into: the newest one the compositor does
// not hold (the least to copy); the second surface when only the shown one exists; else
// the frame waits a refresh once (-1, busy); then a third surface; else the oldest.
func (ch *rndChain) pick() (int, bool) {
	best, free, held := -1, -1, -1
	for i := range ch.bufs {
		if i == ch.front {
			continue
		}
		b := &ch.bufs[i]
		if b.s == 0 {
			if free < 0 {
				free = i
			}
			continue
		}
		if ch.inUse(i) {
			if held < 0 || b.frame < ch.bufs[held].frame {
				held = i
			}
			continue
		}
		if best < 0 || b.frame > ch.bufs[best].frame {
			best = i
		}
	}
	if best >= 0 {
		ch.waited = false
		return best, false
	}
	if held >= 0 { // every other surface is held: the compositor has not shown the last frame yet
		// A frame right after the last one (in the same refresh: the last ran late) waits
		// for the next refresh, by when the compositor has let go; a frame on time takes a
		// third surface, as waiting would cost it its refresh.
		if !ch.waited && ch.now().Sub(ch.lastEnd) < rndSameRefresh {
			ch.waited = true
			ch.waits++
			return -1, true
		}
		ch.thirdAt = ch.now()
	}
	ch.waited = false
	if free >= 0 && ch.alloc(free) == nil {
		return free, false
	}
	if held >= 0 { // nothing more can be made: the oldest, which the compositor no longer shows
		return held, false
	}
	return ch.front, false // only the shown one exists and nothing else can be made: draw into it
}

// begin readies a surface for a frame that redraws dirty (device pixels) and returns its
// context: errRndBusy when the frame should wait a refresh, errRndLock when a surface
// cannot be locked (nothing changed; the caller repaints everything).
func (ch *rndChain) begin(dirty rndPixRect) (C.CGContextRef, error) {
	i, busy := ch.pick()
	if busy {
		return 0, errRndBusy
	}
	if i < 0 || ch.bufs[i].s == 0 {
		return 0, errRndLock
	}
	b := &ch.bufs[i]
	if b.volatile {
		b.volatile = false
		if C.dxr_set_volatile(b.s, 0) != 0 {
			b.frame = 0 // its memory was taken back
		}
	}
	if !ch.lock(i, false) {
		return 0, errRndLock
	}
	if C.dxr_context_matches(b.ctx, b.s) == 0 { // the memory moved: a context over it again
		C.CGContextRelease(b.ctx)
		b.ctx = C.dxr_surface_context(b.s, C.int(ch.w), C.int(ch.h))
		if b.ctx == 0 {
			C.dxr_unlock(b.s, 0)
			return 0, errRndLock
		}
	}
	if ch.front >= 0 && i != ch.front {
		need, n := ch.since(b.frame)
		copying := false
		for _, r := range need[:n] {
			if !dirty.contains(r) {
				copying = true
			}
		}
		if copying {
			// Without the shown surface's pixels the frame would show older ones outside
			// dirty: the frame is not made.
			if !ch.lock(ch.front, true) {
				C.dxr_unlock(b.s, 0)
				return 0, errRndLock
			}
			f := &ch.bufs[ch.front]
			// A rectangle less another is up to four bands, copied as such.
			for _, r := range need[:n] {
				parts, m := rndSubtract(r, dirty)
				for _, p := range parts[:m] {
					C.dxr_copy(b.s, f.s, C.int(p.x0), C.int(p.y0), C.int(p.x1), C.int(p.y1))
					ch.copied += p.area()
				}
			}
			C.dxr_unlock(f.s, 1)
		}
	}
	ch.cur = i
	return b.ctx, nil
}

// since is the area that changed after frame f, as up to rndHistory rectangles (out[:n])
// that do not contain one another: all of the surface when f is no frame or older than the
// history, and the rectangles' bounds when those cost no more to copy than they do.
func (ch *rndChain) since(f uint64) (out [rndHistory]rndPixRect, n int) {
	all := rndPixRect{0, 0, ch.w, ch.h}
	if f != 0 && f == ch.frame {
		return out, 0
	}
	if f == 0 || ch.frame-f >= rndHistory {
		out[0] = all
		return out, 1
	}
	var bounds rndPixRect
	sum := 0
next:
	for g := f + 1; g <= ch.frame; g++ {
		h := ch.hist[g%rndHistory]
		if h.frame != g {
			out[0] = all
			return out, 1
		}
		r := h.r
		if r.empty() {
			continue
		}
		bounds = bounds.union(r)
		for k := 0; k < n; k++ {
			if out[k].contains(r) {
				continue next
			}
			if r.contains(out[k]) {
				sum -= out[k].area()
				out[k] = out[n-1]
				n--
				k--
			}
		}
		out[n] = r
		n++
		sum += r.area()
	}
	if n > 1 && bounds.area() <= sum {
		out[0] = bounds
		n = 1
	}
	return out, n
}

// end finishes the frame in progress: the surface becomes the one shown.
func (ch *rndChain) end(dirty rndPixRect) {
	if ch.cur < 0 {
		return
	}
	b := &ch.bufs[ch.cur]
	C.dxr_unlock(b.s, 0)
	ch.frame++
	b.frame = ch.frame
	ch.hist[ch.frame%rndHistory] = struct {
		frame uint64
		r     rndPixRect
	}{ch.frame, dirty}
	ch.front, ch.cur = ch.cur, -1
	ch.lastEnd = ch.now()
	// A third surface goes once no frame has needed it for rndThirdKeep: the one with the
	// oldest frame (the next frame takes the newest free one).
	if ch.surfaces() > 2 && ch.now().Sub(ch.thirdAt) >= rndThirdKeep {
		ch.freeOldestSpare()
	}
}

// freeOldestSpare frees the surface not shown with the oldest frame.
func (ch *rndChain) freeOldestSpare() {
	oldest := -1
	for i := range ch.bufs {
		if b := &ch.bufs[i]; b.s != 0 && i != ch.front && i != ch.cur && (oldest < 0 || b.frame < ch.bufs[oldest].frame) {
			oldest = i
		}
	}
	if oldest >= 0 {
		ch.free(oldest)
	}
}

// surfaces is how many surfaces the chain has.
func (ch *rndChain) surfaces() int {
	n := 0
	for i := range ch.bufs {
		if ch.bufs[i].s != 0 {
			n++
		}
	}
	return n
}

// shown is the surface to show, 0 before the first frame.
func (ch *rndChain) shown() C.IOSurfaceRef {
	if ch.front < 0 {
		return 0
	}
	return ch.bufs[ch.front].s
}

// idle keeps one spare surface, marked volatile: the system may take its memory back.
func (ch *rndChain) idle() {
	for ch.surfaces() > 2 {
		ch.freeOldestSpare()
	}
	for i := range ch.bufs {
		b := &ch.bufs[i]
		if i == ch.front || i == ch.cur || b.s == 0 || b.volatile {
			continue
		}
		C.dxr_set_volatile(b.s, 1)
		b.volatile = true
	}
}

// release frees every surface but the shown one: a window idle for longer keeps one.
func (ch *rndChain) release() {
	for i := range ch.bufs {
		if i != ch.front && i != ch.cur && ch.bufs[i].s != 0 && ch.front >= 0 {
			ch.free(i)
		}
	}
}

// readable is the surface whose pixels are the target's: the shown one, else the first;
// -1 when there is none.
func (ch *rndChain) readable() int {
	i := ch.front
	if i < 0 {
		i = 0
	}
	if ch.bufs[i].s == 0 {
		return -1
	}
	return i
}

// lockRead locks the shown surface for reading its pixels; unlockRead undoes it.
func (ch *rndChain) lockRead() bool {
	i := ch.readable()
	return i >= 0 && ch.lock(i, true)
}

func (ch *rndChain) unlockRead() {
	if i := ch.readable(); i >= 0 {
		C.dxr_unlock(ch.bufs[i].s, 1)
	}
}

// rndSubtract is a less b, as up to four rectangles: out[:n].
func rndSubtract(a, b rndPixRect) (out [4]rndPixRect, n int) {
	if a.empty() {
		return out, 0
	}
	i := rndPixRect{max(a.x0, b.x0), max(a.y0, b.y0), min(a.x1, b.x1), min(a.y1, b.y1)}
	if i.empty() {
		out[0] = a
		return out, 1
	}
	add := func(r rndPixRect) {
		if !r.empty() {
			out[n] = r
			n++
		}
	}
	add(rndPixRect{a.x0, a.y0, a.x1, i.y0})
	add(rndPixRect{a.x0, i.y1, a.x1, a.y1})
	add(rndPixRect{a.x0, i.y0, i.x0, i.y1})
	add(rndPixRect{i.x1, i.y0, a.x1, i.y1})
	return out, n
}

// rndPixels is r (DIPs, whole device pixels already) in device pixels.
func rndPixels(r kit.Rect, ppd float32) rndPixRect {
	if r.Empty() {
		return rndPixRect{}
	}
	p := func(v float32) int { return int(math.Round(float64(v * ppd))) }
	return rndPixRect{p(r.Min.X), p(r.Min.Y), p(r.Max.X), p(r.Max.Y)}
}
