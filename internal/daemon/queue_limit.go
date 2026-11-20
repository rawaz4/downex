package daemon

import (
	"context"
	"sync/atomic"

	"downex/internal/limiter"
)

// The speed limits form a chain (docs/M6-SPEC.md §1, docs/POLITE-SPEC.md §5): global →
// polite → queue → download. The global limiter is a limiter root; Polite mode's level
// (polite.go) is a chainLink below it whose own limit is Polite mode's cap (0 while it
// does not slow anything), and every queue and every run has a chainLink below its
// parent. A queue's parent is the polite level. A run's parent is the limiter of the
// queue its download is in now: moveLocked re-points it when the download changes queues,
// also when DeleteQueue moves it to the main queue, so that the old queue's limit stops
// applying at once (a limiter level cannot change its parent). Every run's link counts
// the bytes it lets through into Polite mode's byte meter.

// chainChunk is the most a chainLink takes from one level before it moves on to the
// next: at most the smallest bucket depth of a limited level (64 KiB), so that each level
// grants a chunk with one reservation and the levels refill while the next one waits, as
// within one limiter hierarchy. The engine's capped read slices are smaller still.
const chainChunk = 64 << 10

// chainLink is one level of the chain: its own limit (a limiter root) below a parent
// that can change. Its waits consume from its own level first and then from the parent
// and the parent's ancestors. Limit reports the tightest limit along the chain, which is
// the cap the engine sizes its reads and its connection ramp-up by (a limiter level
// reports only its own). Safe for concurrent use.
type chainLink struct {
	own    limiter.Limiter
	parent atomic.Pointer[parentLimiter]
	meter  atomic.Pointer[atomic.Int64] // counts the bytes of WaitN (runs only; nil: none)
}

// parentLimiter boxes the parent of a chainLink for atomic replacement.
type parentLimiter struct{ limiter.Limiter }

var _ limiter.Limiter = (*chainLink)(nil)

// newChainLink returns a level allowing bytesPerSecond (0 or negative = unlimited) below
// parent.
func newChainLink(bytesPerSecond int64, parent limiter.Limiter) *chainLink {
	c := &chainLink{own: limiter.New(bytesPerSecond)}
	c.parent.Store(&parentLimiter{parent})
	return c
}

// setParent makes later waits consume from p instead of the previous parent; a wait in
// progress finishes on the previous one.
func (c *chainLink) setParent(p limiter.Limiter) { c.parent.Store(&parentLimiter{p}) }

// setMeter makes WaitN add every byte it is asked for to m (docs/POLITE-SPEC.md rule 16):
// the receive rate Polite mode measures. Only a run's link has one, so that no byte is
// counted twice along the chain.
func (c *chainLink) setMeter(m *atomic.Int64) { c.meter.Store(m) }

// WaitN blocks until n bytes may pass this level and every level above it, or ctx is
// done (then it returns ctx.Err()). Once the whole chain is unlimited the rest passes at
// once.
func (c *chainLink) WaitN(ctx context.Context, n int) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if m := c.meter.Load(); m != nil && n > 0 {
		m.Add(int64(n)) // before the unlimited fast path: every byte counts
	}
	for n > 0 && c.Limit() > 0 {
		chunk := min(n, chainChunk)
		if err := c.own.WaitN(ctx, chunk); err != nil {
			return err
		}
		if err := c.parent.Load().WaitN(ctx, chunk); err != nil {
			return err
		}
		n -= chunk
	}
	return nil
}

// SetLimit changes this level's own limit (0 or negative = unlimited) without blocking.
func (c *chainLink) SetLimit(bytesPerSecond int64) { c.own.SetLimit(bytesPerSecond) }

// Limit returns the tightest non-zero limit of this level and every level above it
// (0: the whole chain is unlimited).
func (c *chainLink) Limit() int64 { return tightest(c.own.Limit(), c.parent.Load().Limit()) }

// Child returns a level allowing bytesPerSecond below this one.
func (c *chainLink) Child(bytesPerSecond int64) limiter.Limiter {
	return newChainLink(bytesPerSecond, c)
}

// tightest returns the lower of two limits, where 0 means unlimited.
func tightest(a, b int64) int64 {
	switch {
	case a <= 0:
		return max(b, 0)
	case b <= 0:
		return a
	}
	return min(a, b)
}
