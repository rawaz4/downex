//go:build darwin && cgo

package mac

// [render] The window's half of presentation (present.go): show the surface a frame drew;
// a second after the last frame mark the spare volatile, a few seconds later free it.
//
// Hidden windows. WebKit stops requestAnimationFrame for a page whose window is not
// visible (fully covered, on another Space, minimised: the page is hidden), paints nothing
// until it shows again, and throttles the page's timers to about once a second. The native
// window does the same: while AppKit reports it not visible (NSWindow.occlusionState), a
// tick runs Root.Frame at most once a second (animations end, layout, focus fix-up: what a
// hidden page's timers and handlers would still do; a progress window's fit still happens)
// from a timer instead of the display link, and paints nothing; the area to paint is kept
// and painted as soon as AppKit reports the window visible again (goRndOcclusion). A
// minimised window runs no Frame at all, as before. (The display link itself was measured
// to keep firing for a window on another Space, so the throttling is the window's own.)

/*
#include "present_darwin.h"
*/
import "C"

import (
	"time"
)

// rndIdleAfter is how long after its last frame a window marks its spare surface volatile;
// rndFreeAfter how long until it frees it.
const (
	rndIdleAfter = time.Second
	rndFreeAfter = 4 * time.Second
)

// rndHiddenEvery is how often a hidden window runs Root.Frame at most.
const rndHiddenEvery = time.Second

// rndWindow is a window's presentation state.
type rndWindow struct {
	idle     *time.Timer
	idleStep int       // 0: the next idle marks the spare volatile, 1: it frees it
	last     time.Time // the last frame shown

	hiddenTimer *time.Timer
	hiddenArmed bool
	hiddenLast  time.Time // the last Frame while hidden

	// occluded overrides the window's occlusion (tests): 0 asks AppKit, 1 hidden, 2 shown.
	occluded int
	// presented counts the frames shown, hiddenFrames the Frames run while the window was
	// hidden (tests).
	presented, hiddenFrames int
}

// rndPresent shows the surface the last frame drew; false when the target has no surfaces
// (the CGImage path then presents).
func (w *Window) rndPresent() bool {
	t := w.target
	if t == nil || t.chain == nil {
		return false
	}
	s := t.chain.shown()
	if s == 0 {
		return true
	}
	C.dxr_present(w.ns, s, C.double(w.ppd), cbool(t.opaque))
	w.rnd.presented++
	w.rnd.last = time.Now()
	w.rnd.idleStep = 0
	w.rndScheduleIdle(rndIdleAfter)
	return true
}

// rndHidden reports whether the shown window is not visible (see the file comment).
func (w *Window) rndHidden() bool {
	if !w.shown || w.closed {
		return false
	}
	switch w.rnd.occluded {
	case 1:
		return true
	case 2:
		return false
	}
	return !bool(C.dxrWindowVisible(w.ns))
}

// rndHiddenFrameDue reports whether a hidden window's tick runs Root.Frame now; when not,
// the tick comes back once it is due.
func (w *Window) rndHiddenFrameDue() bool {
	if time.Since(w.rnd.hiddenLast) >= rndHiddenEvery {
		w.rnd.hiddenLast = time.Now()
		return true
	}
	w.tickAsked = true
	w.rndHiddenTick()
	return false
}

// rndHiddenTick asks for a tick when the hidden window's next Frame is due.
func (w *Window) rndHiddenTick() {
	if w.rnd.hiddenArmed || w.closed {
		return
	}
	w.rnd.hiddenArmed = true
	wait := max(rndHiddenEvery-time.Since(w.rnd.hiddenLast), 0)
	if w.rnd.hiddenTimer == nil {
		w.rnd.hiddenTimer = time.AfterFunc(wait, func() { w.app.Post(w.rndHiddenFire) })
		return
	}
	w.rnd.hiddenTimer.Reset(wait)
}

func (w *Window) rndHiddenFire() {
	w.rnd.hiddenArmed = false
	if !w.closed {
		w.tick()
	}
}

// rndShown paints what a hidden window kept, once it shows again.
func (w *Window) rndShown() {
	if w.closed || w.rndHidden() {
		return
	}
	if !w.dirty.Empty() || w.frameWanted {
		w.tickAsked = false // a hidden window's tick may be due only in a second
		w.askTick()
	}
}

// rndLayerSurfaceSize is the size in pixels of the surface the window's layer shows (tests).
func (w *Window) rndLayerSurfaceSize() (int, int) {
	var cw, ch C.int
	C.dxr_layer_surface_size(w.ns, &cw, &ch)
	return int(cw), int(ch)
}

// rndLinkRunning reports whether the window's display link runs (tests; false for a
// CVDisplayLink, before macOS 14).
func (w *Window) rndLinkRunning() bool { return bool(C.dxrLinkRunning(w.ns)) }

// rndVisible reports what AppKit says of the window's visibility (tests).
func (w *Window) rndVisible() bool { return bool(C.dxrWindowVisible(w.ns)) }

// rndTestLinkReleased makes a window and releases it without closing it, and reports
// whether its display link was let go (tests).
func rndTestLinkReleased() bool { return bool(C.dxr_test_link_released()) }

// rndDeminiaturize brings the minimised window back (tests).
func (w *Window) rndDeminiaturize() { C.dxr_test_deminiaturize(w.ns) }

// rndScheduleIdle (re)starts the idle timer; it allocates only the first time.
func (w *Window) rndScheduleIdle(after time.Duration) {
	if w.rnd.idle != nil {
		w.rnd.idle.Reset(after)
		return
	}
	w.rnd.idle = time.AfterFunc(after, func() { w.app.Post(w.rndIdle) })
}

// rndIdle runs on the main thread when the idle timer fires. A frame shown since the
// timer was set (an idle already queued when an animation started) puts it off again.
func (w *Window) rndIdle() {
	if w.closed || w.target == nil || w.target.chain == nil {
		return
	}
	wait := rndIdleAfter
	if w.rnd.idleStep == 1 {
		wait = rndFreeAfter
	}
	if since := time.Since(w.rnd.last); since < wait || w.inFrame {
		w.rnd.idle.Reset(max(wait-since, 10*time.Millisecond))
		return
	}
	if w.rnd.idleStep == 0 {
		w.target.chain.idle() // a third surface goes, the spare turns volatile
		w.target.freeScratch()
		w.rnd.idleStep = 1
		w.rnd.idle.Reset(rndFreeAfter - rndIdleAfter)
	} else {
		w.target.chain.release()
	}
	// Core Animation keeps the contents a layer showed before until its next commit: a
	// freed surface stayed mapped (and in the footprint) until then. Showing the same
	// surface again commits.
	if s := w.target.chain.shown(); s != 0 {
		C.dxr_present(w.ns, s, C.double(w.ppd), cbool(w.target.opaque))
	}
	C.dxr_relief()
}

// rndClosed stops the timers of a closing window.
func (w *Window) rndClosed() {
	if w.rnd.idle != nil {
		w.rnd.idle.Stop()
	}
	if w.rnd.hiddenTimer != nil {
		w.rnd.hiddenTimer.Stop()
	}
}

//export goRndOcclusion
func goRndOcclusion(h C.uintptr_t) {
	if w := windowOf(h); w != nil {
		w.rndShown()
	}
}
