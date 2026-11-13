package limiter

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestConcurrentWaitNWithChanges hammers a global → queue → download tree from many
// goroutines while limits flip between unlimited and limited values and some waits are
// cancelled. Afterwards every level is made unlimited; waiters using a context that is
// never cancelled must all return, which detects lost wake-ups and deadlocks. Run it
// with -race to check the synchronisation.
//
// It is deliberately not parallel: its busy workers must not steal CPU from the timing
// tests, which run afterwards.
func TestConcurrentWaitNWithChanges(t *testing.T) {
	const (
		workers  = 48
		duration = 600 * time.Millisecond
	)
	global := New(4 * mib)
	levels := []Limiter{global}
	var downloads []Limiter
	for q := range 3 {
		queue := global.Child(int64(q) * mib) // unlimited, 1 MiB/s, 2 MiB/s
		levels = append(levels, queue)
		for d := range 4 {
			download := queue.Child(int64(d) * 512 * kib)
			levels = append(levels, download)
			downloads = append(downloads, download)
		}
	}
	limits := []int64{0, 0, 64 * kib, 512 * kib, mib, 8 * mib}

	var stop atomic.Bool
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rng := rand.New(rand.NewPCG(uint64(w), 1))
			lim := downloads[w%len(downloads)]
			for !stop.Load() {
				// Real workers do I/O between waits; without a pause, workers on unlimited
				// chains would spin and starve the goroutine that flips the limits.
				time.Sleep(time.Duration(rng.IntN(200)) * time.Microsecond)
				n := 1 + rng.IntN(96*kib)
				if w%3 == 0 {
					// Short-lived contexts exercise the cancel/refund path concurrently.
					ctx, cancel := context.WithTimeout(context.Background(), time.Duration(rng.IntN(20))*time.Millisecond)
					err := lim.WaitN(ctx, n)
					cancel()
					if err != nil && !errors.Is(err, context.DeadlineExceeded) {
						t.Errorf("WaitN = %v", err)
						return
					}
					continue
				}
				if err := lim.WaitN(context.Background(), n); err != nil {
					t.Errorf("WaitN = %v", err)
					return
				}
			}
		}()
	}

	// Flip limits on random levels; Limit must always report a value that was set.
	rng := rand.New(rand.NewPCG(42, 42))
	for deadline := time.Now().Add(duration); time.Now().Before(deadline); {
		lv := levels[rng.IntN(len(levels))]
		lv.SetLimit(limits[rng.IntN(len(limits))])
		if got := lv.Limit(); got < 0 {
			t.Fatalf("Limit() = %d", got)
		}
		time.Sleep(time.Millisecond)
	}

	stop.Store(true)
	for _, lv := range levels {
		lv.SetLimit(0)
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	waitDone(t, done, 5*time.Second, "workers after all levels became unlimited")
}
