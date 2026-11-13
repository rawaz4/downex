package limiter

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// blockedWaiters starts count goroutines that each call WaitN(n) on the limiter returned
// by pick(i) and returns a channel closed when all of them have returned without error.
func blockedWaiters(t *testing.T, count, n int, pick func(i int) Limiter) <-chan struct{} {
	t.Helper()
	done := make(chan struct{})
	var wg sync.WaitGroup
	for i := range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := pick(i).WaitN(context.Background(), n); err != nil {
				t.Errorf("WaitN = %v", err)
			}
		}()
	}
	go func() {
		wg.Wait()
		close(done)
	}()
	return done
}

func TestSetLimitReleasesWaiters(t *testing.T) {
	t.Parallel()
	const settle = 50 * time.Millisecond
	cases := map[string]struct {
		build func() (target Limiter, waiters []Limiter)
		limit int64
	}{
		"limited to unlimited": {
			build: func() (Limiter, []Limiter) { l := New(kib); return l, []Limiter{l} },
			limit: 0,
		},
		"slow to fast": {
			build: func() (Limiter, []Limiter) { l := New(kib); return l, []Limiter{l} },
			limit: 100 * mib,
		},
		"parent to unlimited": {
			build: func() (Limiter, []Limiter) {
				p := New(kib)
				return p, []Limiter{p.Child(0), p.Child(mib), p.Child(0).Child(10 * mib)}
			},
			limit: 0,
		},
		"parent slow to fast": {
			build: func() (Limiter, []Limiter) {
				p := New(kib)
				return p, []Limiter{p.Child(0), p.Child(100 * mib), p.Child(0)}
			},
			limit: 100 * mib,
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			target, waiters := c.build()
			// Four waiters per limiter; at 1 KiB/s each would otherwise wait over a minute.
			done := blockedWaiters(t, 4*len(waiters), 64*kib, func(i int) Limiter { return waiters[i%len(waiters)] })
			time.Sleep(settle)
			select {
			case <-done:
				t.Fatal("waiters finished before the limit was raised")
			default:
			}
			start := time.Now()
			target.SetLimit(c.limit)
			waitDone(t, done, 500*time.Millisecond, "waiters after SetLimit")
			t.Logf("released after %v", time.Since(start))
			if got := target.Limit(); got != c.limit {
				t.Fatalf("Limit() = %d, want %d", got, c.limit)
			}
			if c.limit == 0 && bucketOf(t, target) != nil {
				t.Fatal("unlimited level kept its bucket")
			}
		})
	}
}

func TestSetLimitUnlimitedToLimited(t *testing.T) {
	t.Parallel()
	l := New(0)
	if err := l.WaitN(context.Background(), 64*mib); err != nil {
		t.Fatal(err)
	}
	l.SetLimit(512 * kib)
	assertRate(t, measureRate(t, flow{l, 512 * kib, 32 * kib}), 512*kib, 0.10)
}

func TestSetLimitFastToSlow(t *testing.T) {
	t.Parallel()
	parent := New(4 * mib)
	l := parent.Child(0)
	if err := consume(context.Background(), l, 2*mib, 64*kib); err != nil {
		t.Fatal(err)
	}
	parent.SetLimit(256 * kib)
	// Tokens earned before the change are honoured; measure the steady state after it.
	if err := l.WaitN(context.Background(), 64*kib); err != nil {
		t.Fatal(err)
	}
	assertRate(t, measureRate(t, flow{l, 256 * kib, 16 * kib}), 256*kib, 0.10)
}

// TestSetLimitRapidChanges checks that a stream of limit changes (a slider being
// dragged) neither starves waiters nor lets them exceed the highest limit.
func TestSetLimitRapidChanges(t *testing.T) {
	t.Parallel()
	const (
		lo, hi   = 512 * kib, 768 * kib
		duration = 1500 * time.Millisecond
	)
	l := New(lo)
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()

	var granted atomic.Int64
	var wg sync.WaitGroup
	for _, slice := range []int{32 * kib, 48 * kib, 64 * kib} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				if l.WaitN(ctx, slice) == nil {
					granted.Add(int64(slice))
				}
			}
		}()
	}
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	changes := 0
	for ; ctx.Err() == nil; changes++ {
		<-tick.C
		if changes%2 == 0 {
			l.SetLimit(hi)
		} else {
			l.SetLimit(lo)
		}
	}
	wg.Wait()

	got := float64(granted.Load()) / duration.Seconds()
	t.Logf("%d limit changes, rate %.0f B/s", changes, got)
	if got < 0.85*lo || got > 1.1*hi {
		t.Fatalf("rate %.0f B/s during rapid changes, want within [%.0f, %.0f]", got, 0.85*lo, 1.1*hi)
	}
}
