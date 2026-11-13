package mediajob

import (
	"context"
	"time"
)

// Clock is the job's source of time: backoff waits, the progress and checkpoint
// intervals and the speed average read it, so tests can substitute a fake.
type Clock interface {
	Now() time.Time
	NewTimer(d time.Duration) Timer
}

// Timer is a single-shot timer created by a Clock.
type Timer interface {
	C() <-chan time.Time
	// Stop prevents the timer from firing and reports whether it was active.
	Stop() bool
}

// RealClock returns a Clock backed by package time.
func RealClock() Clock { return realClock{} }

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) NewTimer(d time.Duration) Timer { return realTimer{time.NewTimer(d)} }

type realTimer struct{ t *time.Timer }

func (r realTimer) C() <-chan time.Time { return r.t.C }
func (r realTimer) Stop() bool          { return r.t.Stop() }

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
