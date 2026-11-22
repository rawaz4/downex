package kit

import (
	"math"
	"time"
)

// Animation is something that changes over time. Window.Animate starts it; the window
// then steps it once per frame, at the frame's time, for as long as it runs, and asks the
// host for frames only while at least one animation runs: with nothing moving the window
// makes no frames at all.
type Animation interface {
	// Step advances the animation to now and reports whether it still runs.
	Step(now time.Time) bool
	// Finish jumps to the end: under reduced motion the window finishes an animation
	// as it starts, and when it is stopped.
	Finish()
}

// Easing maps the share of an animation's time gone (0 to 1) to the share of its change.
type Easing func(t float32) float32

// The easing curves of app.css (§2.7) and lib/motion.svelte.ts.
var (
	// EaseOut is --ease-out, cubic-bezier(0.2, 0.8, 0.3, 1): entrances.
	EaseOut = CubicBezier(0.2, 0.8, 0.3, 1)
	// EaseIn is --ease-in, cubic-bezier(0.4, 0, 1, 1): exits.
	EaseIn = CubicBezier(0.4, 0, 1, 1)
	// CubicOut is 1 - (1 - t)³, the value tweens of lib/motion.svelte.ts.
	CubicOut Easing = func(t float32) float32 { u := 1 - t; return 1 - u*u*u }
	// Linear is CSS's linear (the spinner, the hatch).
	Linear Easing = func(t float32) float32 { return t }
)

// The durations of app.css (§2.7).
const (
	DurPress  = 90 * time.Millisecond  // --dur-1
	DurHover  = 120 * time.Millisecond // --dur-2
	DurMenu   = 160 * time.Millisecond // --dur-3: menus, popovers, thumbs, knobs
	DurDialog = 240 * time.Millisecond // --dur-4: dialog in, toast in, the details pane
	DurRow    = 320 * time.Millisecond // --dur-5: row reorder
	DurExit   = 140 * time.Millisecond // --dur-exit
	DurTick   = 360 * time.Millisecond // a value tween (ticking)
)

// CubicBezier is CSS's cubic-bezier(x1, y1, x2, y2) timing function.
func CubicBezier(x1, y1, x2, y2 float32) Easing {
	// The curve runs from (0, 0) to (1, 1) with control points (x1, y1) and (x2, y2); for
	// a time share x, find the curve parameter whose x it is, then return that y.
	cx := 3 * float64(x1)
	bx := 3*float64(x2-x1) - cx
	ax := 1 - cx - bx
	cy := 3 * float64(y1)
	by := 3*float64(y2-y1) - cy
	ay := 1 - cy - by
	sx := func(t float64) float64 { return ((ax*t+bx)*t + cx) * t }
	dx := func(t float64) float64 { return (3*ax*t+2*bx)*t + cx }
	sy := func(t float64) float64 { return ((ay*t+by)*t + cy) * t }
	return func(x float32) float32 {
		if x <= 0 {
			return 0
		}
		if x >= 1 {
			return 1
		}
		target := float64(x)
		t := target
		for range 8 { // Newton's method, which converges at once for these curves
			e := sx(t) - target
			if math.Abs(e) < 1e-7 {
				return float32(sy(t))
			}
			d := dx(t)
			if math.Abs(d) < 1e-6 {
				break
			}
			t -= e / d
		}
		lo, hi := 0.0, 1.0 // bisection where the slope is flat
		t = target
		for range 40 {
			v := sx(t)
			if math.Abs(v-target) < 1e-7 {
				break
			}
			if v < target {
				lo = t
			} else {
				hi = t
			}
			t = (lo + hi) / 2
		}
		return float32(sy(t))
	}
}

// Tween moves a number from From to To over Duration, eased, calling Set with the value at
// every frame and To at the end.
type Tween struct {
	From, To float32
	Duration time.Duration
	Ease     Easing // CubicOut when nil
	Set      func(v float32)

	start time.Time
}

// Step is Animation's.
func (t *Tween) Step(now time.Time) bool {
	if t.start.IsZero() {
		t.start = now
	}
	share := float32(1)
	if t.Duration > 0 {
		share = float32(now.Sub(t.start)) / float32(t.Duration)
	}
	if share >= 1 {
		t.Finish()
		return false
	}
	ease := t.Ease
	if ease == nil {
		ease = CubicOut
	}
	t.set(t.From + (t.To-t.From)*ease(max(share, 0)))
	return true
}

// Finish is Animation's: the value jumps to To.
func (t *Tween) Finish() { t.set(t.To) }

func (t *Tween) set(v float32) {
	if t.Set != nil {
		t.Set(v)
	}
}

// Eased is a number that eases to each new target, as lib/motion.svelte.ts's ticking does
// for speeds, percentages and times left: it ticks rather than jumps. A target that
// changes again within twice the duration of its last change (a running download's
// progress, reported every 250 ms) is shown at once, and so is every change while motion
// is off and every change to or from a number that is not finite, so that no frame loop
// runs while a download streams.
type Eased struct {
	// Duration is the tween's length; DurTick when 0.
	Duration time.Duration
	// OnChange hears that Value changed (to invalidate what shows it).
	OnChange func()
	// Glide moves to a streaming target (one that comes within twice Duration of the last)
	// at an even pace over the time since that last one, instead of showing it at once: a
	// download's progress then advances smoothly between its reports rather than in steps.
	Glide bool

	value   float32
	changed time.Time // when the target last changed
	tween   *Tween
	w       *Window
}

// Value is the number to show now.
func (e *Eased) Value() float32 { return e.value }

// Reset shows v at once, with no motion and no memory of earlier changes.
func (e *Eased) Reset(v float32) {
	e.stop()
	e.changed = time.Time{}
	e.value = v
}

// SetTarget eases to target in w (at once when w is nil).
func (e *Eased) SetTarget(w *Window, target float32) {
	from := e.value
	e.stop()
	dur := e.Duration
	if dur <= 0 {
		dur = DurTick
	}
	var now time.Time
	if w != nil {
		now = w.Now()
	}
	since := now.Sub(e.changed)
	streaming := !e.changed.IsZero() && !now.IsZero() && since < 2*dur
	if target != from {
		e.changed = now
	}
	if streaming && e.Glide && w != nil && w.MotionOK() && finite(target) && finite(from) && from != target {
		e.w = w
		e.tween = &Tween{From: from, To: target, Duration: GlideDuration(since), Ease: Linear, Set: e.show}
		w.Animate(e.tween)
		return
	}
	if streaming || w == nil || !w.MotionOK() || !finite(target) || !finite(from) || from == target {
		e.show(target)
		return
	}
	e.w = w
	e.tween = &Tween{From: from, To: target, Duration: dur, Ease: CubicOut, Set: e.show}
	w.Animate(e.tween)
}

func (e *Eased) show(v float32) {
	if v == e.value {
		return
	}
	e.value = v
	if e.OnChange != nil {
		e.OnChange()
	}
}

func (e *Eased) stop() {
	if e.tween != nil && e.w != nil {
		e.w.stopAnimation(e.tween)
	}
	e.tween = nil
}

// The bounds of a glide's length (Eased.Glide, SegmentBar): reports closer together than
// minGlide still glide briefly, and a pause longer than maxGlide does not slow the motion down.
const (
	minGlide = 120 * time.Millisecond
	maxGlide = 1200 * time.Millisecond
)

// GlideDuration is the length of a glide toward a value reported since after the last one:
// since, kept between the bounds above.
func GlideDuration(since time.Duration) time.Duration { return min(max(since, minGlide), maxGlide) }

func finite(v float32) bool { return !math.IsNaN(float64(v)) && !math.IsInf(float64(v), 0) }

// Scheduler runs f once after d, on a goroutine of its own; the window hands the call to
// the UI thread with Host.Post. Tests give the window one driven by a fake clock.
type Scheduler interface {
	AfterFunc(d time.Duration, f func()) (stop func() bool)
}

type realScheduler struct{}

func (realScheduler) AfterFunc(d time.Duration, f func()) func() bool {
	return time.AfterFunc(d, f).Stop
}

// Timer is a function the window runs once on the UI thread after a delay (see
// Window.After).
type Timer struct {
	w    *Window
	f    func()
	stop func() bool
	done bool
}

// Stop keeps the timer from running; it is safe to call more than once.
func (t *Timer) Stop() {
	if t == nil || t.done {
		return
	}
	t.done = true
	if t.stop != nil {
		t.stop()
	}
	delete(t.w.timers, t)
}

// Active reports whether the timer has neither run nor been stopped.
func (t *Timer) Active() bool { return t != nil && !t.done }

// After runs f on the UI thread after d, unless the timer is stopped first or the window
// closes. A timer costs no frames while it waits.
func (w *Window) After(d time.Duration, f func()) *Timer {
	t := &Timer{w: w, f: f}
	h := w.host
	if h == nil || w.closed {
		t.done = true
		return t
	}
	w.timers[t] = struct{}{}
	w.stats.Timers++
	t.stop = w.sched.AfterFunc(d, func() {
		_ = h.Post(func() {
			w.enter()
			defer w.leave()
			if t.done || w.closed {
				return
			}
			t.done = true
			delete(w.timers, t)
			t.f()
		})
	})
	return t
}

// Animate starts a (it runs from the next frame on); under reduced motion it finishes it
// at once instead.
func (w *Window) Animate(a Animation) {
	if a == nil {
		return
	}
	if !w.MotionOK() || w.closed {
		a.Finish()
		return
	}
	w.anims = append(w.anims, a)
	w.stats.Animations++
	w.requestFrame()
}

// stopAnimation stops a, which stays where it is, and reports whether it ran.
func (w *Window) stopAnimation(a Animation) bool {
	ran := false
	for i, x := range w.stepping {
		if x == a {
			w.stepping[i] = nil
			ran = true
		}
	}
	for i, x := range w.anims {
		if x == a {
			w.anims = append(w.anims[:i], w.anims[i+1:]...)
			return true
		}
	}
	return ran
}

// StopAnimation stops a and finishes it (its value jumps to its end).
func (w *Window) StopAnimation(a Animation) {
	if w.stopAnimation(a) {
		a.Finish()
	}
}

// Animating reports how many animations run.
func (w *Window) Animating() int { return len(w.anims) }

// MotionOK reports whether animations may run: the system's animation effects are on (no
// reduced motion) and the window was not made without motion.
func (w *Window) MotionOK() bool { return !w.env.ReducedMotion && !w.opts.NoMotion }

// Now is the time on the window's clock (time.Now, or the one its options give).
func (w *Window) Now() time.Time {
	if w.opts.Now != nil {
		return w.opts.Now()
	}
	return time.Now()
}

// stepAnimations advances every animation to now, keeping those that still run.
func (w *Window) stepAnimations(now time.Time) {
	if len(w.anims) == 0 {
		return
	}
	run := w.anims
	w.anims = nil
	w.stepping = run // an animation stopped while stepping is cleared here
	for i, a := range run {
		if a != nil && !a.Step(now) {
			run[i] = nil
		}
	}
	w.stepping = nil
	keep := run[:0]
	for _, a := range run {
		if a != nil {
			keep = append(keep, a)
		}
	}
	// Animations started while stepping were appended to w.anims.
	w.anims = append(keep, w.anims...)
}
