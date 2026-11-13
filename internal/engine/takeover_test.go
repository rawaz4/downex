package engine

import (
	"context"
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"downex/internal/model"
	"downex/internal/testserver"
	"downex/internal/transport"
)

// scriptFactory scripts three kinds of responses for TestTakeoverAdjacentSegment:
// the probe delivers 1 KiB, waits for hold to close, then streams everything in
// 4 KiB reads; the second request never delivers a byte; later requests stream normally.
type scriptFactory struct {
	size       int64
	hold       chan struct{}
	fetches    atomic.Int64
	probeBytes atomic.Int64
	secondDone atomic.Bool // the second request's body saw its context cancelled
}

func (f *scriptFactory) NewSlot() transport.Fetcher { return scriptFetcher{f} }

type scriptFetcher struct{ f *scriptFactory }

func (scriptFetcher) Close() {}

func (s scriptFetcher) Fetch(ctx context.Context, rr transport.RangeRequest) (*transport.Response, error) {
	n := s.f.fetches.Add(1)
	end := rr.End
	if end < 0 {
		end = s.f.size - 1
	}
	b := &scriptBody{ctx: ctx, f: s.f, n: n, off: rr.Start, end: end}
	return &transport.Response{
		StatusCode: 206, Body: b, ContentLength: end - rr.Start + 1,
		ContentRange: &transport.ContentRange{Start: rr.Start, End: end, Total: s.f.size},
		Validators:   model.Validators{ETag: `"s"`, StrongETag: true}, FinalURL: rr.URL,
	}, nil
}

type scriptBody struct {
	ctx      context.Context
	f        *scriptFactory
	n        int64
	off, end int64
}

func (b *scriptBody) Read(p []byte) (int, error) {
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	switch {
	case b.n == 2:
		<-b.ctx.Done()
		b.f.secondDone.Store(true)
		return 0, b.ctx.Err()
	case b.n == 1 && b.off == 1024:
		select {
		case <-b.f.hold:
		case <-b.ctx.Done():
			return 0, b.ctx.Err()
		}
	}
	if b.off > b.end {
		return 0, io.EOF
	}
	limit := int64(len(p))
	if b.n == 1 {
		limit = min(limit, 4096)
		if b.off < 1024 {
			limit = min(limit, 1024-b.off)
		}
	}
	k := min(limit, b.end-b.off+1)
	testserver.Fill(fakeSeed, p[:k], b.off)
	b.off += k
	if b.n == 1 {
		b.f.probeBytes.Add(k)
	}
	return int(k), nil
}

func (b *scriptBody) Close() error { return nil }

// TestTakeoverAdjacentSegment: the first worker's response covers the segment split
// off for the second worker; the second has received nothing when the first reaches
// the split point, so the first takes the segment over and the second is cancelled (§5).
func TestTakeoverAdjacentSegment(t *testing.T) {
	t.Parallel()
	fakeTest(t, testTakeoverAdjacentSegment)
}

func testTakeoverAdjacentSegment(t *testing.T, dir string) {
	const size = 1 << 20
	clk := NewFakeClock(time.Unix(50_000, 0))
	fac := &scriptFactory{size: size, hold: make(chan struct{})}
	st := newMemStore()
	eng := New(st, fac, Options{Clock: clk})
	d, err := eng.Add(context.Background(), model.Request{URL: "http://fake.test/a.bin"}, dir, "", 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- eng.Run(ctx, d.ID, nil, nil) }()
	defer func() { cancel(); <-done }()
	synctest.Wait()
	if n := fac.probeBytes.Load(); n != 1024 {
		t.Fatalf("the probe delivered %d bytes before the first tick, want 1024", n)
	}
	// Ramp-up to two connections: early first split at 10·(1,024 + 11,680) = 127,040.
	advanceUntil(t, func(n int) { advance(clk, n) }, 100, "the second request", func() bool { return fac.fetches.Load() >= 2 })
	// The takeover needs no clock: it happens when the probe reaches the split, and the
	// second request is cancelled then.
	close(fac.hold)
	synctest.Wait()
	if !fac.secondDone.Load() {
		t.Fatal("the second connection was not cancelled before the clock moved")
	}
	// Finishing may wait on the clock: the final rename retries a Windows sharing
	// violation (an anti-virus scan of the new file) with back-off, so step the clock
	// until Run returns, as the drain test does.
	start := clk.Now()
	for finished := false; !finished; {
		select {
		case err := <-done:
			done <- err // for the deferred wait
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			finished = true
		default:
			if clk.Now().Sub(start) > time.Minute {
				t.Fatal("download did not finish")
			}
			advance(clk, 1)
			realPause() // the final rename's back-off: see pacedFactory.drive
		}
	}
	if got := fac.probeBytes.Load(); got <= 127_040+4096 {
		t.Fatalf("first response delivered only %d bytes; no takeover happened", got)
	}
	r := st.get(t, d.ID)
	data, err := os.ReadFile(filepath.Join(r.Dir, r.Filename))
	if err != nil {
		t.Fatal(err)
	}
	if r.State != model.StateCompleted || sha256.Sum256(data) != testserver.Sum256(fakeSeed, size) {
		t.Fatalf("state %v, %d bytes, content mismatch", r.State, len(data))
	}
}

// TestRecoverAfterRenameCrash: the part file was already renamed but the "completed"
// commit was lost; Run finishes the record without downloading again.
func TestRecoverAfterRenameCrash(t *testing.T) {
	t.Parallel()
	h := newHarness(t, testserver.Options{Size: 100_000, Seed: 8, ETag: `"r"`}, fastOpts())
	d := h.add("/r.bin", "", 2)
	if err := h.run(d.ID, nil, nil); err != nil {
		t.Fatal(err)
	}
	r := h.verify(d.ID)
	r.State = model.StateDownloading
	if err := h.st.UpdateDownload(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	before := len(h.srv.Requests())
	if err := h.run(d.ID, nil, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	h.verify(d.ID)
	if n := len(h.srv.Requests()); n != before {
		t.Fatalf("%d new requests; expected none", n-before)
	}
}
