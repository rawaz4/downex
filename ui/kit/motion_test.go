package kit_test

import (
	"math"
	"testing"
	"time"

	"downex/ui/kit"
)

// TestMotionClockStops checks that a tween asks for frames while it runs and for none
// once it is done: an idle window makes 0 frames.
func TestMotionClockStops(t *testing.T) {
	bar := &kit.Element{Style: kit.Style{Width: kit.Px(100), Height: kit.Px(6)}}
	w, loop := open(t, box(kit.Style{}, bar), kit.Size{W: 200, H: 100}, env100)
	loop.Settle(10)
	start := loop.Frames
	var values []float32
	w.Animate(&kit.Tween{From: 0, To: 1, Duration: kit.DurDialog, Ease: kit.EaseOut, Set: func(v float32) {
		values = append(values, v)
		bar.Invalidate()
	}})
	loop.Run(kit.DurDialog + 100*time.Millisecond)
	frames := loop.Frames - start
	// 240 ms at 60 Hz is 15 refreshes, and one more that sees the end.
	if frames < 15 || frames > 17 {
		t.Errorf("a 240 ms tween took %d frames, want 15 or 16", frames)
	}
	if w.Animating() != 0 {
		t.Errorf("%d animations still run", w.Animating())
	}
	if len(values) == 0 || values[len(values)-1] != 1 {
		t.Fatalf("the tween did not end at its target: %v", values)
	}
	for i := 1; i < len(values); i++ {
		if values[i] < values[i-1] {
			t.Errorf("ease-out went back: %v", values)
			break
		}
	}
	before := loop.Frames
	requests := loop.Host.FrameRequests
	loop.Run(3 * time.Second)
	if loop.Frames != before || loop.Host.FrameRequests != requests {
		t.Errorf("after the tween: %d frames, %d requests in 3 s, want 0", loop.Frames-before, loop.Host.FrameRequests-requests)
	}
}

// TestEasedTicking checks lib/motion.svelte.ts's ticking: a lone change eases over 360 ms,
// and a value that changes again within 720 ms of its last change (a download reporting
// every 250 ms) is shown at once, with no frame loop.
func TestEasedTicking(t *testing.T) {
	w, loop := open(t, box(kit.Style{}), kit.Size{W: 100, H: 100}, env100)
	loop.Settle(10)
	var e kit.Eased
	e.Reset(0)
	e.SetTarget(w, 50) // a lone change: it eases
	if w.Animating() != 1 || e.Value() != 0 {
		t.Fatalf("a lone change: %d animations, value %v", w.Animating(), e.Value())
	}
	loop.Run(100 * time.Millisecond)
	if v := e.Value(); !(v > 0 && v < 50) {
		t.Errorf("100 ms in, the value is %v, want between 0 and 50", v)
	}
	// Streaming: every 250 ms a new value, shown at once.
	animated := 0
	for i := range 20 {
		loop.Run(250 * time.Millisecond)
		e.SetTarget(w, float32(60+i))
		if w.Animating() != 0 {
			animated++
		}
		if e.Value() != float32(60+i) {
			t.Errorf("streamed value %v, want %v shown at once", e.Value(), 60+i)
		}
	}
	if animated != 0 {
		t.Errorf("%d of 20 streamed changes started an animation", animated)
	}
	// Quiet for longer than 2 × 360 ms: the next change eases again.
	loop.Run(time.Second)
	e.SetTarget(w, 10)
	if w.Animating() != 1 {
		t.Error("a change after a quiet second does not ease")
	}
	loop.Run(time.Second)
	if e.Value() != 10 || w.Animating() != 0 {
		t.Errorf("after the tween: value %v, %d animations", e.Value(), w.Animating())
	}
	// Values that are not finite are shown at once.
	e.SetTarget(w, float32(math.Inf(1)))
	loop.Run(time.Second)
	e.SetTarget(w, 5)
	if w.Animating() != 0 || e.Value() != 5 {
		t.Error("a change from infinity eased")
	}
}

// TestReducedMotion checks that animations finish at once and ask for no frame when the
// system's animation effects are off.
func TestReducedMotion(t *testing.T) {
	env := env100
	env.ReducedMotion = true
	w, loop := open(t, box(kit.Style{}), kit.Size{W: 100, H: 100}, env)
	loop.Settle(10)
	requests := loop.Host.FrameRequests
	got := float32(-1)
	w.Animate(&kit.Tween{From: 0, To: 1, Duration: time.Second, Set: func(v float32) { got = v }})
	if got != 1 || w.Animating() != 0 || loop.Host.FrameRequests != requests {
		t.Errorf("under reduced motion: value %v, %d running, %d frames asked", got, w.Animating(), loop.Host.FrameRequests-requests)
	}
	var e kit.Eased
	e.Reset(0)
	e.SetTarget(w, 9)
	if e.Value() != 9 || w.Animating() != 0 {
		t.Error("a ticking value eased under reduced motion")
	}
	// Turning reduced motion on finishes what runs.
	w2, loop2 := open(t, box(kit.Style{}), kit.Size{W: 100, H: 100}, env100)
	got = -1
	w2.Animate(&kit.Tween{From: 0, To: 1, Duration: time.Second, Set: func(v float32) { got = v }})
	loop2.Send(kit.EnvironmentEvent{Env: env})
	if got != 1 || w2.Animating() != 0 {
		t.Errorf("reduced motion turned on: value %v, %d running", got, w2.Animating())
	}
}

// TestCubicBezier checks the easing curves: CSS's ease at 0.5 is 0.8024034 (the value
// browsers compute), the ends are 0 and 1, and the curves never go back.
func TestCubicBezier(t *testing.T) {
	ease := kit.CubicBezier(0.25, 0.1, 0.25, 1)
	if got := ease(0.5); math.Abs(float64(got)-0.8024034) > 1e-4 {
		t.Errorf("ease(0.5) = %v, want 0.8024034", got)
	}
	lin := kit.CubicBezier(0, 0, 1, 1)
	for _, x := range []float32{0, 0.1, 0.33, 0.5, 0.9, 1} {
		if got := lin(x); math.Abs(float64(got-x)) > 1e-5 {
			t.Errorf("cubic-bezier(0,0,1,1)(%v) = %v", x, got)
		}
	}
	for _, c := range []struct {
		name string
		f    kit.Easing
	}{{"ease-out", kit.EaseOut}, {"ease-in", kit.EaseIn}, {"cubic-out", kit.CubicOut}} {
		if c.f(0) != 0 || math.Abs(float64(c.f(1)-1)) > 1e-6 {
			t.Errorf("%s: ends %v, %v", c.name, c.f(0), c.f(1))
		}
		prev := float32(0)
		for i := 1; i <= 100; i++ {
			v := c.f(float32(i) / 100)
			if v < prev-1e-6 {
				t.Errorf("%s goes back at %d%%", c.name, i)
				break
			}
			prev = v
		}
	}
	// --ease-out leads: at 20% of the time it is already past half the way.
	if v := kit.EaseOut(0.2); v < 0.5 {
		t.Errorf("ease-out(0.2) = %v", v)
	}
}
