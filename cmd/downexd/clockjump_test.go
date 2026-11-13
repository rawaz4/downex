package main

import (
	"testing"
	"time"
)

func TestClockJumped(t *testing.T) {
	for _, c := range []struct {
		mono, wall time.Duration
		want       bool
	}{
		{30 * time.Second, 30 * time.Second, false},
		{30 * time.Second, 31 * time.Second, false}, // drift and NTP slewing
		{30 * time.Second, 29 * time.Second, false},
		{30 * time.Second, 2 * time.Hour, true}, // slept, or the clock was set forward
		{30 * time.Second, -time.Hour, true},    // the clock was set back
		{30 * time.Second, 30*time.Second + clockJumpTolerance, false},
		{30 * time.Second, 30*time.Second + clockJumpTolerance + 1, true},
	} {
		if got := clockJumped(c.mono, c.wall); got != c.want {
			t.Errorf("clockJumped(%v, %v) = %v, want %v", c.mono, c.wall, got, c.want)
		}
	}
}

// TestWatchClockJumpsStops: without a jump the watcher calls nothing, and it returns when
// stop is closed.
func TestWatchClockJumpsStops(t *testing.T) {
	stop := make(chan struct{})
	done := make(chan struct{})
	jumps := 0
	go func() {
		defer close(done)
		watchClockJumps(stop, time.Millisecond, func() { jumps++ })
	}()
	time.Sleep(20 * time.Millisecond)
	close(stop)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("watchClockJumps did not return after stop")
	}
	if jumps != 0 {
		t.Errorf("%d jumps reported without a clock change", jumps)
	}
}
