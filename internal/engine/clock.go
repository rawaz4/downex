package engine

import (
	"context"
	"time"
)

// Clock is the engine's source of time. Every timing rule of docs/ENGINE-SPEC.md
// (ramp-up, speed EWMA, stall detection, flush and checkpoint intervals, backoff)
// reads time through it, so tests can substitute a FakeClock.
type Clock interface {
	Now() time.Time
	NewTimer(d time.Duration) Timer
	NewTicker(d time.Duration) Ticker
}

// Timer is a single-shot timer created by a Clock.
type Timer interface {
	C() <-chan time.Time
	// Stop prevents the timer from firing and reports whether it was active.
	Stop() bool
	// Reset re-arms the timer to fire after d and reports whether it was active.
	Reset(d time.Duration) bool
}

// Ticker delivers ticks at a fixed period. A slow receiver misses ticks, as with time.Ticker.
type Ticker interface {
	C() <-chan time.Time
	Stop()
}

// RealClock returns a Clock backed by package time.
func RealClock() Clock { return realClock{} }

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) NewTimer(d time.Duration) Timer { return realTimer{time.NewTimer(d)} }

func (realClock) NewTicker(d time.Duration) Ticker {
	if d <= 0 {
		d = time.Nanosecond
	}
	return realTicker{time.NewTicker(d)}
}

type realTimer struct{ t *time.Timer }

func (r realTimer) C() <-chan time.Time        { return r.t.C }
func (r realTimer) Stop() bool                 { return r.t.Stop() }
func (r realTimer) Reset(d time.Duration) bool { return r.t.Reset(d) }

type realTicker struct{ t *time.Ticker }

func (r realTicker) C() <-chan time.Time { return r.t.C }
func (r realTicker) Stop()               { r.t.Stop() }

// sleep waits for d on c, returning early with ctx's error when ctx is done.
func sleep(ctx context.Context, c Clock, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := c.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C():
		return nil
	}
}
