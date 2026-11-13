package limiter

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"
)

func TestBurstFor(t *testing.T) {
	tests := []struct {
		bps  int64
		want int
	}{
		{1, minBurst},
		{16 * kib, minBurst},
		{256 * kib, minBurst},
		{640 * kib, minBurst},
		{mib, mib / 10},
		{5 * mib / 2, 256 * kib},
		{100 * mib, 10 * mib},
		{math.MaxInt64, maxBurst},
	}
	for _, tt := range tests {
		if got := burstFor(tt.bps); got != tt.want {
			t.Errorf("burstFor(%d) = %d, want %d", tt.bps, got, tt.want)
		}
	}
}

func TestLimitAccessors(t *testing.T) {
	root := New(5 * kib)
	if got := root.Limit(); got != 5*kib {
		t.Fatalf("New(5 KiB).Limit() = %d", got)
	}
	child := root.Child(7 * kib)
	if got := child.Limit(); got != 7*kib {
		t.Fatalf("Child(7 KiB).Limit() = %d", got)
	}
	if got := root.Limit(); got != 5*kib {
		t.Fatalf("parent limit changed by Child: %d", got)
	}
	child.SetLimit(-3)
	if got := child.Limit(); got != 0 {
		t.Fatalf("SetLimit(-3) then Limit() = %d, want 0", got)
	}
	if b := bucketOf(t, child); b != nil {
		t.Fatal("negative limit kept a bucket, want unlimited")
	}
	if got := New(-1).Limit(); got != 0 {
		t.Fatalf("New(-1).Limit() = %d, want 0", got)
	}
	if got := root.Child(-9).Limit(); got != 0 {
		t.Fatalf("Child(-9).Limit() = %d, want 0", got)
	}
}

func TestSetLimitSameValueKeepsBucket(t *testing.T) {
	l := New(mib)
	before := bucketOf(t, l)
	l.SetLimit(mib)
	if after := bucketOf(t, l); after != before {
		t.Fatal("SetLimit with the current value replaced the bucket")
	}
}

func TestUnlimitedFastPath(t *testing.T) {
	root := New(0)
	queue := root.Child(0)
	download := queue.Child(0)
	for _, l := range []Limiter{root, queue, download} {
		if bucketOf(t, l) != nil {
			t.Fatal("unlimited level allocated a token bucket")
		}
	}
	ctx := context.Background()

	allocs := testing.AllocsPerRun(1000, func() {
		if err := download.WaitN(ctx, 256*kib); err != nil {
			t.Fatal(err)
		}
	})
	if allocs != 0 {
		t.Fatalf("unlimited WaitN allocates %.1f times per call, want 0", allocs)
	}

	start := time.Now()
	for range 100_000 {
		if err := download.WaitN(ctx, 256*kib); err != nil {
			t.Fatal(err)
		}
	}
	if err := download.WaitN(ctx, math.MaxInt); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("100k unlimited WaitN calls took %v", elapsed)
	}
}

func TestWaitNNonPositive(t *testing.T) {
	l := New(1) // one byte per second: any real wait would be obvious
	ctx := context.Background()
	for _, n := range []int{0, -1, math.MinInt} {
		start := time.Now()
		if err := l.WaitN(ctx, n); err != nil {
			t.Fatalf("WaitN(%d) = %v", n, err)
		}
		if d := time.Since(start); d > 50*time.Millisecond {
			t.Fatalf("WaitN(%d) blocked for %v", n, d)
		}
	}
}

func TestWaitNNilContext(t *testing.T) {
	var ctx context.Context // nil on purpose: bad input must not panic
	if err := New(0).WaitN(ctx, 10); err != nil {
		t.Fatalf("unlimited WaitN(nil ctx) = %v", err)
	}
	if err := New(10*mib).WaitN(ctx, 10); err != nil {
		t.Fatalf("limited WaitN(nil ctx) = %v", err)
	}
}

func TestNewBucketStartsEmpty(t *testing.T) {
	l := New(mib)
	b := bucketOf(t, l)
	if b == nil {
		t.Fatal("limited level has no bucket")
	}
	if tokens := b.tokens.Tokens(); tokens > 1*kib {
		t.Fatalf("new bucket holds %.0f tokens, want ~0", tokens)
	}
	// After a long idle period the bucket holds at most one burst.
	if tokens := b.tokens.TokensAt(time.Now().Add(time.Hour)); tokens != float64(b.burst) {
		t.Fatalf("idle bucket holds %.0f tokens, want burst %d", tokens, b.burst)
	}
}

func TestAlreadyCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for name, l := range map[string]Limiter{
		"unlimited": New(0),
		"limited":   New(kib),
		"child":     New(kib).Child(0),
	} {
		if err := l.WaitN(ctx, 64*kib); !errors.Is(err, context.Canceled) {
			t.Errorf("%s: WaitN(cancelled ctx) = %v, want context.Canceled", name, err)
		}
	}
}
