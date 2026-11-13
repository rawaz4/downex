package limiter

import (
	"context"
	"sync"
	"testing"
	"time"
)

const (
	kib = 1 << 10
	mib = 1 << 20
)

// flow is one goroutine that consumes bytes from lim in slices of size slice.
type flow struct {
	lim   Limiter
	bytes int
	slice int
}

// consume calls WaitN in slices until total bytes were granted.
func consume(ctx context.Context, lim Limiter, total, slice int) error {
	for total > 0 {
		n := min(slice, total)
		if err := lim.WaitN(ctx, n); err != nil {
			return err
		}
		total -= n
	}
	return nil
}

// measureRate runs the flows concurrently and returns the aggregate observed rate in
// bytes per second, measured from start until the last byte was granted.
func measureRate(t *testing.T, flows ...flow) float64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var (
		wg    sync.WaitGroup
		total int
		errs  = make(chan error, len(flows))
	)
	start := time.Now()
	for _, f := range flows {
		total += f.bytes
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- consume(ctx, f.lim, f.bytes, f.slice)
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("WaitN: %v", err)
		}
	}
	return float64(total) / elapsed.Seconds()
}

// assertRate fails unless got is within ±tolerance (a fraction) of want.
func assertRate(t *testing.T, got, want, tolerance float64) {
	t.Helper()
	if lo, hi := want*(1-tolerance), want*(1+tolerance); got < lo || got > hi {
		t.Fatalf("rate = %.0f B/s, want %.0f B/s ±%.0f%% [%.0f, %.0f]", got, want, tolerance*100, lo, hi)
	}
	t.Logf("rate = %.0f B/s (want %.0f B/s, %+.1f%%)", got, want, (got/want-1)*100)
}

// waitDone fails the test if done is not closed within d.
func waitDone(t *testing.T, done <-chan struct{}, d time.Duration, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not finish within %v", what, d)
	}
}

// bucketOf returns the active bucket of a Limiter created by this package.
func bucketOf(t *testing.T, l Limiter) *bucket {
	t.Helper()
	lv, ok := l.(*level)
	if !ok {
		t.Fatalf("limiter is %T, want *level", l)
	}
	return lv.bucket.Load()
}
