package shell

import (
	"time"

	"downex/ui/kit"
	"downex/ui/kit/tokens"
)

// cssEase is CSS's ease timing function, cubic-bezier(0.25, 0.1, 0.25, 1), which the title
// bar's transitions use (transition: background-color var(--dur-2) ease).
var cssEase = kit.CubicBezier(0.25, 0.1, 0.25, 1)

// fade is a colour that follows its target as a CSS colour transition does: from where it
// is now to the new target over the duration, in premultiplied sRGB; at once under reduced
// motion (the kit then finishes every animation as it starts).
type fade struct {
	cur, from, to kit.Color
	dur           time.Duration
	anim          *fadeAnim
	set           bool
	onChange      func()
}

type fadeAnim struct {
	f     *fade
	start time.Time
	dead  bool
}

// Step is kit.Animation's.
func (a *fadeAnim) Step(now time.Time) bool {
	if a.dead {
		return false
	}
	if a.start.IsZero() {
		a.start = now
	}
	t := float32(1)
	if a.f.dur > 0 {
		t = float32(now.Sub(a.start)) / float32(a.f.dur)
	}
	if t >= 1 {
		a.f.show(a.f.to)
		return false
	}
	a.f.show(kit.Mix(a.f.to, cssEase(max(t, 0)), a.f.from))
	return true
}

// Finish is kit.Animation's.
func (a *fadeAnim) Finish() {
	if !a.dead {
		a.f.show(a.f.to)
	}
}

func (f *fade) show(c kit.Color) {
	if c == f.cur {
		return
	}
	f.cur = c
	if f.onChange != nil {
		f.onChange()
	}
}

// moveTo moves the colour to target in w's motion (at once before the first paint, or without
// a window).
func (f *fade) moveTo(w *kit.Window, target kit.Color) {
	if f.set && target == f.to {
		return
	}
	if f.anim != nil {
		f.anim.dead = true
		f.anim = nil
	}
	f.to = target
	if !f.set || w == nil || f.dur <= 0 {
		f.set = true
		f.show(target)
		return
	}
	f.from = f.cur
	f.anim = &fadeAnim{f: f}
	w.Animate(f.anim)
}

// jump shows target at once (a theme change repaints everything in the new colours).
func (f *fade) jump(target kit.Color) {
	if f.anim != nil {
		f.anim.dead = true
		f.anim = nil
	}
	f.to, f.set = target, true
	f.show(target)
}

// snap shows target at once without asking for a repaint: for a Paint that found the theme
// changed and draws the new colour itself.
func (f *fade) snap(target kit.Color) {
	if f.anim != nil {
		f.anim.dead = true
		f.anim = nil
	}
	f.to, f.cur, f.set = target, target, true
}

// themeMark is the theme a widget's faded colours were last set in. A theme change reaches
// the window in several ways (the choice, the system's light or dark apps, a contrast theme);
// a widget that keeps its colours checks the mark when it paints, so that it shows the new
// theme's colours at once however the change came.
type themeMark struct{ t *tokens.Theme }

// changed reports whether t differs from the theme marked, and marks t.
func (m *themeMark) changed(t *tokens.Theme) bool {
	if m.t == t {
		return false
	}
	m.t = t
	return true
}
