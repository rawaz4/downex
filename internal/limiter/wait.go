package limiter

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// errReservation reports that a token bucket refused a reservation within its burst.
// It indicates a broken invariant rather than an expected condition.
var errReservation = errors.New("limiter: token bucket refused a reservation")

// acquire blocks until this level alone grants n bytes or ctx is done. It splits n by
// the level's current burst and reserves again whenever the bucket is replaced while
// it sleeps. An unlimited level returns after one atomic load, without locking.
func (l *level) acquire(ctx context.Context, n int) error {
	for n > 0 {
		if l.bucket.Load() == nil {
			return nil
		}
		l.mu.Lock()
		b := l.bucket.Load()
		if b == nil {
			l.mu.Unlock()
			return nil
		}
		take := min(n, b.burst)
		now := time.Now()
		r := b.tokens.ReserveN(now, take)
		if !r.OK() {
			l.mu.Unlock()
			return fmt.Errorf("reserve %d of burst %d bytes: %w", take, b.burst, errReservation)
		}
		delay := r.DelayFrom(now)
		if delay <= 0 {
			l.mu.Unlock()
			n -= take
			continue
		}
		t := b.register(take, now.Add(delay))
		l.mu.Unlock()

		granted, err := l.sleep(ctx, b, t, delay)
		if err != nil {
			return err
		}
		if granted {
			n -= take
		}
	}
	return nil
}

// sleep waits until the reservation recorded as t in b is due, b is retired, or
// ctx is done. It reports whether the reserved bytes were granted; when they were not
// and err is nil, the caller must reserve again.
func (l *level) sleep(ctx context.Context, b *bucket, t *ticket, delay time.Duration) (granted bool, err error) {
	timer := time.NewTimer(delay)
	select {
	case <-timer.C:
	case <-b.changed:
	case <-ctx.Done():
	}
	timer.Stop()

	l.mu.Lock()
	defer l.mu.Unlock()
	if t.void {
		// The bytes were refunded into the replacement bucket.
		return false, ctx.Err()
	}
	b.unregister(t)
	if err := ctx.Err(); err != nil {
		now := time.Now()
		if l.bucket.Load() == b && t.due.After(now) {
			// Refund exactly: rebuild the bucket at the same rate so the waiters queued
			// behind this reservation move up instead of paying for unused bytes.
			l.replaceLocked(now, l.limit.Load(), float64(t.n))
		}
		return false, err
	}
	return true, nil
}
