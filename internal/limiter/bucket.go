package limiter

import (
	"time"

	"golang.org/x/time/rate"
)

// Bucket depth (burst) policy, ENGINE-SPEC §8.
//
// The depth is about 100 ms of traffic (limit/10), clamped to [minBurst, maxBurst]:
//   - 100 ms absorbs timer and scheduler jitter while keeping the flow smooth: an idle
//     level can never release more than a tenth of a second of extra traffic at once.
//   - minBurst (64 KiB) is the largest read slice the engine uses while capped, so a capped
//     slice is granted by a single reservation per level.
//   - From 2.5 MiB/s upwards the depth is at least 256 KiB, so uncapped-size slices pass a
//     limited ancestor without being split. Below that, WaitN splits larger slices into
//     depth-sized chunks; the total wait is the same, only the bookkeeping is finer.
//   - Unlimited levels have no bucket at all, so 256 KiB uncapped slices pass them for free.
//   - maxBurst bounds the depth for very high limits so it stays representable as a
//     32-bit int and the post-idle surge stays finite.
const (
	burstDivisor = 10
	minBurst     = 64 << 10
	maxBurst     = 64 << 20
)

// burstFor returns the bucket depth in bytes for a positive limit in bytes per second.
func burstFor(bytesPerSecond int64) int {
	b := bytesPerSecond / burstDivisor
	switch {
	case b < minBurst:
		return minBurst
	case b > maxBurst:
		return maxBurst
	default:
		return int(b)
	}
}

// bucket is the token bucket of one limited level. A level never mutates its bucket's
// rate in place: SetLimit (and a cancelled wait) retire the bucket and install a new one,
// carrying the earned tokens over, so every sleeping waiter can be re-planned exactly once.
//
// All fields except tokens (which has its own lock) are guarded by the owning level's mu.
type bucket struct {
	tokens  *rate.Limiter
	burst   int
	changed chan struct{}        // closed when the bucket is retired
	waiting map[*ticket]struct{} // reservations whose holders are still asleep
}

// ticket records a reservation whose holder is sleeping until due.
type ticket struct {
	n   int
	due time.Time
	// void reports that retire refunded the reservation into the replacement bucket;
	// the holder must reserve again instead of proceeding.
	void bool
}

// newBucket returns a bucket for a positive limit that starts with carry tokens,
// clamped to [0, burst]. A level that becomes limited starts empty (carry 0), so the
// average rate measured from the moment a limit is applied never exceeds it.
func newBucket(bytesPerSecond int64, carry float64, now time.Time) *bucket {
	burst := burstFor(bytesPerSecond)
	tokens := rate.NewLimiter(rate.Limit(bytesPerSecond), burst) // starts full
	carry = max(0, min(carry, float64(burst)))
	if drain := burst - int(carry); drain > 0 {
		tokens.AllowN(now, drain) // drain <= burst, so this always succeeds
	}
	return &bucket{tokens: tokens, burst: burst, changed: make(chan struct{})}
}

// register records a sleeping reservation of n bytes that becomes usable at due.
func (b *bucket) register(n int, due time.Time) *ticket {
	if b.waiting == nil {
		b.waiting = make(map[*ticket]struct{})
	}
	t := &ticket{n: n, due: due}
	b.waiting[t] = struct{}{}
	return t
}

// unregister forgets a ticket whose holder woke up.
func (b *bucket) unregister(t *ticket) {
	delete(b.waiting, t)
}

// retire prepares the bucket for replacement by another limited bucket at time now.
// Reservations that are not yet due are voided and their bytes refunded; reservations
// that are already due keep their grant. It returns the tokens the replacement should
// start with: the tokens earned so far plus the refunds. The caller closes changed.
func (b *bucket) retire(now time.Time) float64 {
	carry := b.tokens.TokensAt(now)
	for t := range b.waiting {
		if t.due.After(now) {
			t.void = true
			carry += float64(t.n)
			delete(b.waiting, t)
		}
	}
	return carry
}
