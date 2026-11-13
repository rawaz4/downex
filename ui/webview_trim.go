package main

import "time"

// WebView2 keeps squeezing a window whose memory target stays low, and a window that still
// draws (a download in progress behind another app) pages straight back in: measured
// ~1,200 page faults/s and +3 points of CPU against ~370 at normal. A short pulse of low
// frees what the window does not use, once, and the window then runs at normal; while it
// stays out of use the pulse repeats, so what it grows back is freed again.
const (
	trimPoll    = time.Second
	trimAfter   = 10 * time.Second // an Alt+Tab away and back does not trim
	trimHold    = 3 * time.Second  // how long a pulse keeps the target low
	retrimEvery = time.Minute
)

// trimPolicy decides WebView2's memory target from samples of whether the window is in
// use: a pulse of low once it has been out of use for trimAfter and every retrimEvery
// after, normal at once when it is in use again. The zero value starts at normal.
type trimPolicy struct {
	low      bool
	outSince time.Time // when the window went out of use; zero while in use
	lowSince time.Time // when the current pulse began
}

// step takes the sample at now and returns the target and whether it changed.
func (p *trimPolicy) step(now time.Time, inUse bool) (low, change bool) {
	if inUse {
		p.outSince, p.lowSince = time.Time{}, time.Time{}
		if p.low {
			p.low = false
			return false, true
		}
		return false, false
	}
	if p.outSince.IsZero() {
		p.outSince = now
	}
	if p.low {
		if now.Sub(p.lowSince) >= trimHold {
			p.low = false
			return false, true
		}
		return true, false
	}
	if now.Sub(p.outSince) >= trimAfter && (p.lowSince.IsZero() || now.Sub(p.lowSince) >= retrimEvery) {
		p.low, p.lowSince = true, now
		return true, true
	}
	return false, false
}
