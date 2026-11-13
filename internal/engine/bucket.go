package engine

import (
	"context"
	"sync"
	"time"
)

// throttle is the part of limiter.Limiter the engine uses.
type throttle interface {
	WaitN(ctx context.Context, n int) error
	Limit() int64 // bytes per second, 0 = unlimited
}

// bucket is a minimal token bucket on the engine clock. Run uses it only when the
// caller passes no limiter but the download carries its own SpeedLimit.
type bucket struct {
	clock Clock
	rate  float64 // bytes per second
	burst float64

	mu     sync.Mutex
	tokens float64
	last   time.Time
}

func newBucket(c Clock, bytesPerSecond int64) *bucket {
	rate := float64(bytesPerSecond)
	burst := max(rate/4, sliceCapped)
	return &bucket{clock: c, rate: rate, burst: burst, tokens: burst, last: c.Now()}
}

func (b *bucket) Limit() int64 { return int64(b.rate) }

// WaitN blocks until n bytes may pass, in chunks no larger than the burst.
func (b *bucket) WaitN(ctx context.Context, n int) error {
	for n > 0 {
		chunk := min(n, int(b.burst))
		if err := sleep(ctx, b.clock, b.reserve(float64(chunk))); err != nil {
			return err
		}
		n -= chunk
	}
	return ctx.Err()
}

// reserve takes n tokens, possibly going into debt, and returns how long to wait.
func (b *bucket) reserve(n float64) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.clock.Now()
	if el := now.Sub(b.last); el > 0 {
		b.tokens = min(b.burst, b.tokens+el.Seconds()*b.rate)
		b.last = now
	}
	b.tokens -= n
	if b.tokens >= 0 {
		return 0
	}
	return time.Duration(-b.tokens / b.rate * float64(time.Second))
}
