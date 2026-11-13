package limiter

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// level is one node of the limiter hierarchy, for example the global, a queue or a
// download limiter. Its waits consume from itself first and then from every ancestor.
//
// An unlimited level owns no bucket: WaitN only performs an atomic load for it and
// never sleeps or allocates.
type level struct {
	parent *level

	// limit mirrors the configured rate (0 = unlimited) for lock-free Limit calls.
	limit atomic.Int64
	// bucket is the active token bucket, or nil when the level is unlimited. It is
	// only replaced while mu is held but may be loaded without mu.
	bucket atomic.Pointer[bucket]
	// mu serialises reservations against bucket replacement so refunds stay exact.
	// It is never held while sleeping or while another level's mu is acquired.
	mu sync.Mutex
}

var _ Limiter = (*level)(nil)

// New returns a root limiter that allows bytesPerSecond bytes per second.
// Zero or a negative value means unlimited.
func New(bytesPerSecond int64) Limiter {
	l := &level{}
	l.SetLimit(bytesPerSecond)
	return l
}

// Child returns a limiter allowing bytesPerSecond (0 or negative = unlimited) whose
// waits also consume from l and l's ancestors. The parent keeps no reference to the
// child, so an abandoned child is simply garbage collected.
func (l *level) Child(bytesPerSecond int64) Limiter {
	c := &level{parent: l}
	c.SetLimit(bytesPerSecond)
	return c
}

// Limit returns the current limit in bytes per second (0 = unlimited).
func (l *level) Limit() int64 {
	return l.limit.Load()
}

// SetLimit changes the limit (0 or negative = unlimited) without blocking.
//
// Sleeping waiters on this level are woken and re-planned: when the level becomes
// unlimited they proceed at once; when the rate changes they keep the tokens they have
// already earned and reserve again at the new rate. A level that becomes limited starts
// with an empty bucket.
func (l *level) SetLimit(bytesPerSecond int64) {
	bytesPerSecond = max(bytesPerSecond, 0)
	l.mu.Lock()
	defer l.mu.Unlock()
	if bytesPerSecond == l.limit.Load() {
		return
	}
	l.replaceLocked(time.Now(), bytesPerSecond, 0)
}

// replaceLocked installs a bucket for bytesPerSecond (none when 0) and wakes the
// waiters of the old one. When the level stays limited, the new bucket starts with the
// old bucket's earned tokens plus refund plus the refunds of every voided reservation.
// l.mu must be held.
func (l *level) replaceLocked(now time.Time, bytesPerSecond int64, refund float64) {
	old := l.bucket.Load()
	var next *bucket
	if bytesPerSecond > 0 {
		carry := refund
		if old != nil {
			carry += old.retire(now)
		}
		next = newBucket(bytesPerSecond, carry, now)
	}
	l.limit.Store(bytesPerSecond)
	l.bucket.Store(next)
	if old != nil {
		close(old.changed)
	}
}

// WaitN blocks until n bytes may be consumed by this limiter and all its ancestors,
// or ctx is done, in which case it returns ctx.Err().
//
// n is split into chunks no larger than the smallest burst of any limited level, and
// each chunk is taken from this level first and then from each ancestor in turn, so
// the levels' waits overlap with their refills. Bytes already taken by lower levels
// when ctx is cancelled at a higher level are not returned to the lower levels.
func (l *level) WaitN(ctx context.Context, n int) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for n > 0 {
		chunk, limited := l.chunk(n)
		if !limited {
			return nil
		}
		for lv := l; lv != nil; lv = lv.parent {
			if err := lv.acquire(ctx, chunk); err != nil {
				return err
			}
		}
		n -= chunk
		if n > 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
	}
	return nil
}

// chunk returns how many of the n remaining bytes the next pass over the hierarchy
// takes: at most the smallest burst of any limited level, so each level can grant it
// with one reservation. limited is false when every level is unlimited.
func (l *level) chunk(n int) (size int, limited bool) {
	size = n
	for lv := l; lv != nil; lv = lv.parent {
		if b := lv.bucket.Load(); b != nil {
			limited = true
			size = min(size, b.burst)
		}
	}
	return size, limited
}
