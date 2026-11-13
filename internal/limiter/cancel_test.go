package limiter

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// TestWaitNCancellation checks that a blocked WaitN returns ctx.Err() promptly, both
// when it blocks on its own level and when it blocks on an ancestor.
func TestWaitNCancellation(t *testing.T) {
	t.Parallel()
	const (
		cancelAfter = 50 * time.Millisecond
		slack       = 250 * time.Millisecond
	)
	cases := map[string]func() Limiter{
		"own level":           func() Limiter { return New(kib) },
		"parent level":        func() Limiter { return New(kib).Child(10 * mib) },
		"grandparent level":   func() Limiter { return New(kib).Child(0).Child(0) },
		"split above burst":   func() Limiter { return New(64 * kib) },
		"child of fast queue": func() Limiter { return New(100 * mib).Child(kib) },
	}
	for name, build := range cases {
		t.Run(name+"/cancel", func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			time.AfterFunc(cancelAfter, cancel)
			start := time.Now()
			err := build().WaitN(ctx, 10*mib)
			elapsed := time.Since(start)
			if !errors.Is(err, context.Canceled) || err != ctx.Err() {
				t.Fatalf("WaitN = %v, want ctx.Err() = %v", err, ctx.Err())
			}
			if elapsed > cancelAfter+slack {
				t.Fatalf("WaitN returned %v after start, cancel was at %v", elapsed, cancelAfter)
			}
		})
		t.Run(name+"/deadline", func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), cancelAfter)
			defer cancel()
			start := time.Now()
			err := build().WaitN(ctx, 10*mib)
			elapsed := time.Since(start)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("WaitN = %v, want context.DeadlineExceeded", err)
			}
			if elapsed > cancelAfter+slack {
				t.Fatalf("WaitN returned %v after start, deadline was %v", elapsed, cancelAfter)
			}
		})
	}
}

// TestCancelRefundsQueuedBytes checks that cancelled waiters do not leave phantom debt:
// a waiter queued behind them moves up as soon as they give up.
func TestCancelRefundsQueuedBytes(t *testing.T) {
	t.Parallel()
	const rate = 64 * kib // burst 64 KiB: each waiter below needs one second
	l := New(rate)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	start := time.Now()
	var wg sync.WaitGroup
	for range 7 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := l.WaitN(ctx, 64*kib); !errors.Is(err, context.Canceled) {
				t.Errorf("cancelled waiter: WaitN = %v, want context.Canceled", err)
			}
		}()
	}
	time.Sleep(30 * time.Millisecond) // let the seven reserve first: the keeper is last in line

	kept := make(chan time.Duration, 1)
	go func() {
		if err := l.WaitN(context.Background(), 64*kib); err != nil {
			t.Errorf("kept waiter: WaitN = %v", err)
		}
		kept <- time.Since(start)
	}()
	time.Sleep(70 * time.Millisecond)
	cancel()
	wg.Wait()

	// Without refunds the keeper would wait about 8 s; with them about 1 s from start.
	select {
	case elapsed := <-kept:
		if elapsed > 1500*time.Millisecond {
			t.Fatalf("kept waiter finished after %v, want about 1s", elapsed)
		}
		t.Logf("kept waiter finished after %v", elapsed)
	case <-time.After(3 * time.Second):
		t.Fatal("kept waiter still blocked behind cancelled reservations")
	}
}
