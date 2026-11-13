package engine

import (
	"context"
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"downex/internal/model"
	"downex/internal/testserver"
	"downex/internal/transport"
)

// tailStallFactory scripts the responses of TestReassignedWorkerDoesNotDrainStalledTail:
//   - the probe delivers 1 KiB, waits until the second response delivered its first
//     KiB, then streams 4 KiB reads up to stallAt and blocks there until cancelled;
//   - the second response waits for release, delivers 1 KiB and blocks until cancelled;
//   - later responses stream normally.
type tailStallFactory struct {
	size     int64
	stallAt  int64
	release  chan struct{} // closed by the test: the second response may deliver
	secondGo chan struct{} // closed once the second response delivered its first KiB
	once     sync.Once
	fetches  atomic.Int64
}

func (f *tailStallFactory) NewSlot() transport.Fetcher { return tailStallFetcher{f} }

type tailStallFetcher struct{ f *tailStallFactory }

func (tailStallFetcher) Close() {}

func (s tailStallFetcher) Fetch(ctx context.Context, rr transport.RangeRequest) (*transport.Response, error) {
	n := s.f.fetches.Add(1)
	end := rr.End
	if end < 0 {
		end = s.f.size - 1
	}
	b := &tailStallBody{ctx: ctx, f: s.f, n: n, start: rr.Start, off: rr.Start, end: end}
	return &transport.Response{
		StatusCode: 206, Body: b, ContentLength: end - rr.Start + 1,
		ContentRange: &transport.ContentRange{Start: rr.Start, End: end, Total: s.f.size},
		Validators:   model.Validators{ETag: `"d"`, StrongETag: true}, FinalURL: rr.URL,
	}, nil
}

type tailStallBody struct {
	ctx             context.Context
	f               *tailStallFactory
	n               int64
	start, off, end int64
}

func (b *tailStallBody) wait(ch <-chan struct{}) error {
	select {
	case <-ch:
		return nil
	case <-b.ctx.Done():
		return b.ctx.Err()
	}
}

func (b *tailStallBody) Read(p []byte) (int, error) {
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	if b.off > b.end {
		return 0, io.EOF
	}
	limit := int64(len(p))
	switch b.n {
	case 1:
		if b.off == 1024 {
			if err := b.wait(b.f.secondGo); err != nil {
				return 0, err
			}
		}
		if b.off >= b.f.stallAt {
			<-b.ctx.Done()
			return 0, b.ctx.Err()
		}
		limit = min(limit, 4096, b.f.stallAt-b.off)
		if b.off < 1024 {
			limit = min(limit, 1024-b.off)
		}
	case 2:
		if err := b.wait(b.f.release); err != nil {
			return 0, err
		}
		if sent := b.off - b.start; sent >= 1024 {
			b.f.once.Do(func() { close(b.f.secondGo) })
			<-b.ctx.Done()
			return 0, b.ctx.Err()
		}
		limit = min(limit, 1024-(b.off-b.start))
	}
	k := min(limit, b.end-b.off+1)
	testserver.Fill(fakeSeed, p[:k], b.off)
	b.off += k
	return int(k), nil
}

func (b *tailStallBody) Close() error { return nil }

// TestReassignedWorkerDoesNotDrainStalledTail: a worker whose response still has a
// short tail beyond its finished segment is given a new segment. Closing that old
// response must not read the tail itself: the server stalls there, and a worker
// blocked in that read would own its new segment forever (§5).
func TestReassignedWorkerDoesNotDrainStalledTail(t *testing.T) {
	t.Parallel()
	fakeTest(t, testReassignedWorkerDoesNotDrainStalledTail)
}

func testReassignedWorkerDoesNotDrainStalledTail(t *testing.T, dir string) {
	const size = 100_000
	clk := NewFakeClock(time.Unix(60_000, 0))
	fac := &tailStallFactory{size: size, stallAt: 60_000, release: make(chan struct{}), secondGo: make(chan struct{})}
	st := newMemStore()
	eng := New(st, fac, Options{Clock: clk, StallTimeout: 3 * time.Second, FlushInterval: time.Hour})
	d, err := eng.Add(context.Background(), model.Request{URL: "http://fake.test/drain.bin"}, dir, "", 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- eng.Run(ctx, d.ID, nil, nil) }()
	defer func() { cancel(); <-done }()

	synctest.Wait()
	if fac.fetches.Load() < 1 {
		t.Fatal("the probe did not start before the first tick")
	}
	// Ramp-up to two connections; the second gets the upper half [50511, 99999].
	advanceUntil(t, func(n int) { advance(clk, n) }, 100, "the second request", func() bool { return fac.fetches.Load() >= 2 })
	// From here on no fake time passes until the probe worker was reassigned: its
	// takeover fails (the second response delivered a KiB), it splits the second
	// segment and must open a third request for its new part.
	close(fac.release)
	synctest.Wait()
	if fac.fetches.Load() < 3 {
		t.Fatal("the reassigned worker never requested its new segment; it is stuck closing its old response")
	}
	// The second response stays stalled; the stall timeout reassigns its range.
	for finished := false; !finished; {
		select {
		case err := <-done:
			done <- err // for the deferred wait
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			finished = true
		default:
			if clk.Now().Sub(time.Unix(60_000, 0)) > time.Minute {
				t.Fatal("download did not finish")
			}
			advance(clk, 1)
			realPause() // the final rename's back-off: see pacedFactory.drive
		}
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

// blockingBody blocks every Read until it is closed.
type blockingBody struct {
	closed chan struct{}
	reads  atomic.Int64
}

func (b *blockingBody) Read([]byte) (int, error) {
	b.reads.Add(1)
	<-b.closed
	return 0, io.ErrClosedPipe
}

func (b *blockingBody) Close() error {
	select {
	case <-b.closed:
	default:
		close(b.closed)
	}
	return nil
}

// TestStreamCloseDoesNotRead: closing a stream with a short unread tail must not read
// from the body (the transport bounds any drain; a stalled server must not block).
func TestStreamCloseDoesNotRead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		body := &blockingBody{closed: make(chan struct{})}
		st := newStream(body, 0, 1000)
		closed := make(chan struct{})
		go func() {
			st.close()
			close(closed)
		}()
		synctest.Wait() // close has returned, or blocks for good in a Read
		select {
		case <-closed:
		default:
			_ = body.Close()
			<-closed
			t.Fatal("stream.close blocked reading the unread tail")
		}
		if n := body.reads.Load(); n != 0 {
			t.Fatalf("stream.close read the body %d times", n)
		}
	})
}
