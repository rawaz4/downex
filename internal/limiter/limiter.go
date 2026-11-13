// Package limiter implements hierarchical bandwidth limits (global → queue → download).
// See docs/ENGINE-SPEC.md §8.
package limiter

import "context"

// Limiter throttles bytes. A zero limit means unlimited.
type Limiter interface {
	// WaitN blocks until n bytes may be consumed by this limiter and all its parents,
	// or ctx is done. n may exceed the burst; implementations must split it.
	WaitN(ctx context.Context, n int) error
	// SetLimit changes the limit in bytes per second (0 = unlimited) without blocking.
	SetLimit(bytesPerSecond int64)
	// Limit returns the current limit in bytes per second (0 = unlimited).
	Limit() int64
	// Child returns a new limiter whose waits also consume from this limiter.
	Child(bytesPerSecond int64) Limiter
}
