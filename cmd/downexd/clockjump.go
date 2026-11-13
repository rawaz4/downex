package main

import "time"

// Outside Windows nothing tells downexd that the system resumed from sleep or that its
// clock was set: sessionend_other.go watches for it with watchClockJumps instead. A
// sleep or a clock change shows as a difference between the wall clock and the
// monotonic clock, which neither runs during sleep (macOS, Linux) nor follows clock
// changes. The check is cheap and does not decide anything itself: it only makes the
// queue scheduler (docs/M6-SPEC.md §1) and a system/PAC proxy configuration look again.
const (
	clockCheckInterval = 30 * time.Second
	clockJumpTolerance = 2 * time.Second
)

// clockJumped reports whether wall clock time moved differently from monotonic time
// between two readings by more than clockJumpTolerance.
func clockJumped(mono, wall time.Duration) bool {
	d := wall - mono
	return d > clockJumpTolerance || d < -clockJumpTolerance
}

// watchClockJumps calls onJump after each check (every interval) that finds the wall
// clock jumped against the monotonic clock, until stop is closed.
func watchClockJumps(stop <-chan struct{}, interval time.Duration, onJump func()) {
	t := time.NewTicker(interval)
	defer t.Stop()
	last := time.Now()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		now := time.Now()
		// Sub uses the monotonic readings; with Round(0) only the wall clock readings.
		if clockJumped(now.Sub(last), now.Round(0).Sub(last.Round(0))) {
			onJump()
		}
		last = now
	}
}
