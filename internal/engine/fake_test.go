package engine

import (
	"bytes"
	"cmp"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"downex/internal/limiter"
	"downex/internal/model"
	"downex/internal/testserver"
	"downex/internal/transport"
)

const fakeSeed = 77

// trickleFactory serves a ranged resource whose bodies deliver a first chunk and then
// block until the request is cancelled, so connections stay open while a test drives
// the fake clock. With gate set, bodies after the probe withhold their first byte
// until the gate is closed; with silent, every body withholds it for good. With whole,
// it answers 200 with the whole resource: no ranges (single mode).
type trickleFactory struct {
	size      int64
	gate      chan struct{}
	silent    bool
	whole     bool
	slots     atomic.Int64
	fetches   atomic.Int64
	reading   atomic.Int64 // bodies whose Read was called
	delivered atomic.Int64 // bodies that delivered their first chunk
	ended     atomic.Int64 // bodies that saw their request cancelled

	mu   sync.Mutex
	reqs []transport.RangeRequest // in fetch order
}

func (f *trickleFactory) requests() []transport.RangeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.reqs)
}

func (f *trickleFactory) NewSlot() transport.Fetcher {
	f.slots.Add(1)
	return trickleFetcher{f}
}

type trickleFetcher struct{ f *trickleFactory }

func (trickleFetcher) Close() {}

func (t trickleFetcher) Fetch(ctx context.Context, rr transport.RangeRequest) (*transport.Response, error) {
	t.f.mu.Lock()
	t.f.reqs = append(t.f.reqs, rr)
	t.f.mu.Unlock()
	n := t.f.fetches.Add(1)
	end := rr.End
	if end < 0 {
		end = t.f.size - 1
	}
	b := &trickleBody{ctx: ctx, off: rr.Start, end: end, first: 1024, f: t.f}
	if t.f.silent {
		b.first = 0
	}
	if n > 1 {
		b.gate = t.f.gate
	}
	if t.f.whole {
		return &transport.Response{StatusCode: 200, Body: b, ContentLength: t.f.size, FinalURL: rr.URL}, nil
	}
	return &transport.Response{
		StatusCode: 206, Body: b, ContentLength: end - rr.Start + 1,
		ContentRange: &transport.ContentRange{Start: rr.Start, End: end, Total: t.f.size},
		Validators:   model.Validators{ETag: `"t"`, StrongETag: true}, FinalURL: rr.URL,
	}, nil
}

type trickleBody struct {
	ctx      context.Context
	off, end int64
	first    int
	sent     int
	read     bool
	gate     chan struct{}
	f        *trickleFactory
}

func (b *trickleBody) Read(p []byte) (int, error) {
	if !b.read {
		b.read = true
		b.f.reading.Add(1)
	}
	if b.gate != nil {
		select {
		case <-b.gate:
			b.gate = nil
		case <-b.ctx.Done():
			b.f.ended.Add(1)
			return 0, b.ctx.Err()
		}
	}
	if b.sent < b.first && b.off <= b.end {
		n := min(len(p), b.first-b.sent, int(b.end-b.off+1))
		testserver.Fill(fakeSeed, p[:n], b.off)
		if b.sent == 0 {
			b.f.delivered.Add(1)
		}
		b.off += int64(n)
		b.sent += n
		return n, nil
	}
	<-b.ctx.Done()
	b.f.ended.Add(1)
	return 0, b.ctx.Err()
}

func (b *trickleBody) Close() error { return nil }

// fakeTest runs a fake-clock test in a testing/synctest bubble. The engine's timing
// rules run on the FakeClock the test moves; the bubble only tells the test when every
// goroutine of the run is durably blocked (synctest.Wait), which a goroutine in a write,
// fsync, close or rename system call is not. So the clock never moves while the engine
// still works on the disk or has a tick to handle, however loaded the machine is.
//
// dir is made outside the bubble: t.TempDir's cleanup retries a file another process
// (an anti-virus scan) briefly holds, and needs real time for that, which a bubble's
// fake time.Sleep does not give it.
func fakeTest(t *testing.T, f func(t *testing.T, dir string)) {
	t.Helper()
	dir := t.TempDir()
	synctest.Test(t, func(t *testing.T) { f(t, dir) })
}

func startFakeRun(t *testing.T, dir string, fac transport.Factory, clk *FakeClock) (cancel func() error) {
	t.Helper()
	eng := New(newMemStore(), fac, Options{Clock: clk, StallTimeout: time.Hour, FlushInterval: time.Hour})
	d, err := eng.Add(context.Background(), model.Request{URL: "http://fake.test/big.bin"}, dir, "", 8, 0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- eng.Run(ctx, d.ID, nil, nil) }()
	t.Cleanup(stop) // after a failed check; Run's goroutine must end before the bubble does
	return func() error {
		stop()
		return <-done // a Run that never returns is a deadlock the bubble reports with its stacks
	}
}

// advance moves the fake clock in 100 ms ticks and waits after each until the run has
// handled it: every goroutine of the bubble is durably blocked again.
func advance(clk *FakeClock, ticks int) {
	for range ticks {
		clk.Advance(tickInterval)
		synctest.Wait()
	}
}

// TestRampUpFakeClock checks that ramp-up opens the whole budget at once (§3): on the
// first tick after the probe received bytes, the download goes from 1 to its 8
// connections, and no further.
func TestRampUpFakeClock(t *testing.T) {
	t.Parallel()
	fakeTest(t, func(t *testing.T, dir string) {
		clk := NewFakeClock(time.Unix(10_000, 0))
		fac := &trickleFactory{size: 1 << 30}
		stop := startFakeRun(t, dir, fac, clk)
		synctest.Wait()
		if n := fac.delivered.Load(); n != 1 {
			t.Fatalf("%d bodies delivered before the first tick, want the probe's", n)
		}
		advance(clk, 1) // the first tick: the probe's speed is measured
		if slots, delivered := fac.slots.Load(), fac.delivered.Load(); slots != 8 || delivered != 8 {
			t.Fatalf("after the first tick: %d connections, %d delivered; want the budget of 8", slots, delivered)
		}
		advance(clk, 10)
		if got := fac.slots.Load(); got != 8 {
			t.Fatalf("%d connections after ramp-up, want the budget of 8", got)
		}
		if err := stop(); err == nil {
			t.Fatal("Run returned nil after cancel")
		}
	})
}

// TestRampCappedDoubles checks that a capped download keeps doubling (§3): a speed cap
// needs only the connections that reach it, which the 85% rule finds step by step.
func TestRampCappedDoubles(t *testing.T) {
	t.Parallel()
	fakeTest(t, func(t *testing.T, dir string) {
		clk := NewFakeClock(time.Unix(10_000, 0))
		fac := &trickleFactory{size: 1 << 30}
		eng := New(newMemStore(), fac, Options{Clock: clk, StallTimeout: time.Hour, FlushInterval: time.Hour})
		d, err := eng.Add(context.Background(), model.Request{URL: "http://fake.test/big.bin"}, dir, "", 8, 0)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- eng.Run(ctx, d.ID, capOnly{1 << 40}, nil) }()
		defer func() { cancel(); <-done }()
		synctest.Wait()
		if n := fac.delivered.Load(); n != 1 {
			t.Fatalf("%d bodies delivered before the first tick, want the probe's", n)
		}
		for _, want := range []int64{2, 4} {
			for ticks := 0; fac.slots.Load() < want || fac.delivered.Load() < want; ticks++ {
				if ticks == 50 {
					t.Fatalf("%d connections after 50 more ticks, want %d", fac.slots.Load(), want)
				}
				advance(clk, 1)
			}
			// advance waited until the step had started all its connections.
			if got := fac.slots.Load(); got != want {
				t.Fatalf("%d connections, want %d", got, want)
			}
		}
	})
}

// TestRampBurstRequestsFinalRanges checks the split plan of a ramp-up that opens several
// connections at once (§3, §4): the probe keeps an even share instead of the early first
// split's 10·(received + 11,680) bytes, and every new connection asks for exactly the
// segment it ends up with, so no response has to be cut short when its segment ends.
func TestRampBurstRequestsFinalRanges(t *testing.T) {
	t.Parallel()
	fakeTest(t, testRampBurstRequestsFinalRanges)
}

func testRampBurstRequestsFinalRanges(t *testing.T, dir string) {
	const size = 1 << 30
	clk := NewFakeClock(time.Unix(10_000, 0))
	fac := &trickleFactory{size: size}
	stop := startFakeRun(t, dir, fac, clk)
	synctest.Wait()
	if n := fac.delivered.Load(); n != 1 {
		t.Fatalf("%d bodies delivered before the first tick, want the probe's", n)
	}
	advance(clk, 1)
	reqs := fac.requests()
	_ = stop()
	if len(reqs) < 8 {
		t.Fatalf("%d requests after the first tick, want the ramp-up's 8", len(reqs))
	}
	if reqs[0].Start != 0 || reqs[0].End != -1 {
		t.Fatalf("probe asked for %d-%d", reqs[0].Start, reqs[0].End)
	}
	rest := slices.Clone(reqs[1:8])
	slices.SortFunc(rest, func(a, b transport.RangeRequest) int { return cmp.Compare(a.Start, b.Start) })
	// The probe's share: even shares of 8 are 128 MiB; the early first split would leave
	// it 127,040 bytes.
	if rest[0].Start < size/16 {
		t.Errorf("the probe keeps bytes 0-%d, want about 1/8 of %d", rest[0].Start-1, int64(size))
	}
	for i, r := range rest {
		next := int64(size)
		if i+1 < len(rest) {
			next = rest[i+1].Start
		}
		if r.End != next-1 {
			t.Errorf("request %d asked for %d-%d, but its segment ends at %d", i+2, r.Start, r.End, next-1)
		}
	}
}

// TestRampWaitsForEveryWorker checks that the connection target only grows while every
// live worker has received bytes (§3): a raised limit is not used while one connection
// still waits for its first byte, and then the whole new budget is opened at once.
func TestRampWaitsForEveryWorker(t *testing.T) {
	t.Parallel()
	fakeTest(t, testRampWaitsForEveryWorker)
}

func testRampWaitsForEveryWorker(t *testing.T, dir string) {
	clk := NewFakeClock(time.Unix(10_000, 0))
	fac := &trickleFactory{size: 1 << 30, gate: make(chan struct{})}
	eng := New(newMemStore(), fac, Options{Clock: clk, StallTimeout: time.Hour, FlushInterval: time.Hour})
	d, err := eng.Add(context.Background(), model.Request{URL: "http://fake.test/big.bin"}, dir, "", 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- eng.Run(ctx, d.ID, nil, nil) }()
	defer func() { cancel(); <-done }()
	synctest.Wait()
	if n := fac.delivered.Load(); n != 1 {
		t.Fatalf("%d bodies delivered before the first tick, want the probe's", n)
	}
	advance(clk, 1)
	if got := fac.slots.Load(); got != 2 {
		t.Fatalf("%d connections after the first tick, want 2", got)
	}
	if err := eng.SetMaxConnections(d.ID, 8); err != nil {
		t.Fatal(err)
	}
	advance(clk, 25)
	if got := fac.slots.Load(); got != 2 {
		t.Fatalf("%d connections while the second has received nothing", got)
	}
	close(fac.gate)
	for ticks := 0; fac.slots.Load() <= 2; ticks++ {
		if ticks == 100 {
			t.Fatal("no ramp-up 100 ticks after the gate opened")
		}
		advance(clk, 1)
	}
	// advance waited until the coordinator had started the whole step.
	if got := fac.slots.Load(); got != 8 {
		t.Fatalf("%d connections after the gate opened, want the new budget of 8 at once", got)
	}
}

// TestStallFakeClock checks the 20 s default stall timeout with the fake clock where the
// 5 s mid-transfer timeout does not apply (§5): a response that never delivers a body
// byte, the probe's or a later request's after an earlier one delivered bytes (its range
// is reassigned to a new connection), and single mode after body bytes (it cannot resume
// a response: the download starts over).
func TestStallFakeClock(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		fac   func() *trickleFactory // made inside the bubble, which owns its gate
		rules HostRules
		ready func(f *trickleFactory) bool // the stalling request is out and read
		fresh func(f *trickleFactory) bool // a new connection took the stalled range
	}{
		{"no first byte", func() *trickleFactory { return &trickleFactory{size: 1 << 30, silent: true} }, nil,
			func(f *trickleFactory) bool { return f.reading.Load() == 1 },
			func(f *trickleFactory) bool { return f.slots.Load() == 2 && f.fetches.Load() == 2 }},
		// Requests are capped at 1 KiB: the probe's completes, the connection's next one
		// never delivers a byte.
		{"no first byte of a later request",
			func() *trickleFactory { return &trickleFactory{size: 1 << 30, gate: make(chan struct{})} },
			&capRules{caps: map[string]int64{"fake.test": 1024}},
			func(f *trickleFactory) bool { return f.reading.Load() == 2 },
			func(f *trickleFactory) bool { return f.slots.Load() == 2 && f.fetches.Load() == 3 }},
		{"single mode", func() *trickleFactory { return &trickleFactory{size: 1 << 30, whole: true} }, nil,
			func(f *trickleFactory) bool { return f.delivered.Load() == 1 }, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fakeTest(t, func(t *testing.T, dir string) {
				t0 := time.Unix(10_000, 0)
				clk := NewFakeClock(t0)
				fac := tc.fac()
				eng := New(newMemStore(), fac, Options{Clock: clk, FlushInterval: time.Hour, RampInterval: time.Hour, HostRules: tc.rules})
				d, err := eng.Add(context.Background(), model.Request{URL: "http://fake.test/big.bin"}, dir, "", 1, 0)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				done := make(chan error, 1)
				go func() { done <- eng.Run(ctx, d.ID, nil, nil) }()
				defer func() { cancel(); <-done }()
				synctest.Wait()
				if !tc.ready(fac) {
					t.Fatalf("the stalling request is not out before the first tick (%d fetches, %d reading)",
						fac.fetches.Load(), fac.reading.Load())
				}
				fetches := fac.fetches.Load()
				advance(clk, 199) // 19.9 s idle: not yet stalled
				if got := fac.ended.Load(); got != 0 || fac.fetches.Load() != fetches {
					t.Fatalf("after 19.9 s: %d requests cancelled, %d requests (was %d); want none before the stall timeout",
						got, fac.fetches.Load(), fetches)
				}
				// The worker's last activity is at or shortly after the start, so the stall
				// is detected on one of the next ticks. The clock stops once the stalled
				// connection is cancelled.
				for fac.ended.Load() == 0 && clk.Now().Sub(t0) <= 21*time.Second {
					advance(clk, 1)
				}
				if elapsed := clk.Now().Sub(t0); fac.ended.Load() == 0 || elapsed > 21*time.Second {
					t.Fatalf("stall detected after %v of fake time (%d requests cancelled)", elapsed, fac.ended.Load())
				}
				if tc.fresh != nil && !tc.fresh(fac) {
					t.Fatalf("the stalled range was not reassigned once the stall was handled: %d connections, %d fetches",
						fac.slots.Load(), fac.fetches.Load())
				}
			})
		})
	}
}

// staticFactory answers every request with 200 and an unknown length.
type staticFactory struct{ body []byte }

func (s staticFactory) NewSlot() transport.Fetcher { return s }
func (staticFactory) Close()                       {}
func (s staticFactory) Fetch(context.Context, transport.RangeRequest) (*transport.Response, error) {
	return &transport.Response{StatusCode: 200, ContentLength: -1, Body: io.NopCloser(&onceReader{b: s.body}),
		Filename: "stream.out"}, nil
}

type onceReader struct {
	mu sync.Mutex
	b  []byte
}

func (r *onceReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.b) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.b[:min(len(r.b), 1000)])
	r.b = r.b[n:]
	return n, nil
}

func TestSingleModeUnknownSize(t *testing.T) {
	t.Parallel()
	body := testserver.Bytes(3, 0, 123_457)
	st := newMemStore()
	eng := New(st, staticFactory{body: body}, Options{})
	d, err := eng.Add(context.Background(), model.Request{URL: "http://fake.test/s"}, t.TempDir(), "", 8, 0)
	if err != nil {
		t.Fatal(err)
	}
	var last Progress
	if err := eng.Run(context.Background(), d.ID, nil, func(p Progress) { last = p }); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := st.get(t, d.ID)
	if got.State != model.StateCompleted || got.Size != int64(len(body)) || got.Mode != model.ModeSingle || got.Filename != "stream.out" {
		t.Fatalf("record %+v", got)
	}
	if last.Received != int64(len(body)) {
		t.Fatalf("final progress %+v", last)
	}
	segs := st.segs(d.ID)
	if len(segs) != 1 || segs[0].End != int64(len(body))-1 || !segs[0].Done() {
		t.Fatalf("segments %+v", segs)
	}
}

// pacedFactory serves a ranged resource of size bytes (testserver.Fill content) whose
// bodies deliver bytes at a fixed rate of fake-clock time: a body may have sent
// rate·(now − its fetch time) bytes, and Read waits for the clock to move on. step lets
// every body catch up before the coordinator's tick, so the speeds the engine measures
// are exact. It runs in a fakeTest bubble.
type pacedFactory struct {
	clk  *FakeClock
	size int64
	seed uint64
	// rate is the bytes per second of a request starting at start over connection slot
	// (1-based, in NewSlot order), so a connection can stay slow for every request.
	rate func(slot int, start int64) float64
	// sent, when set, replaces rate: the bytes a body may have sent by elapsed fake time
	// after its fetch (non-decreasing), for rates that change over time.
	sent func(elapsed time.Duration) int64

	mu       sync.Mutex
	moved    chan struct{} // closed by the next clock move (nil: nobody waits)
	slots    int
	reqs     []pacedReq
	busy     int // bodies with bytes left that are not closed
	maxBusy  int
	lastByte time.Time // fake time of the latest read that delivered bytes
}

type pacedReq struct {
	slot       int // the connection slot (1-based, in NewSlot order)
	start, end int64
	at         time.Time
}

var pacedValidators = model.Validators{ETag: `"paced"`, StrongETag: true}

func (f *pacedFactory) NewSlot() transport.Fetcher {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.slots++
	return pacedFetcher{f: f, slot: f.slots}
}

type pacedFetcher struct {
	f    *pacedFactory
	slot int
}

func (pacedFetcher) Close() {}

func (p pacedFetcher) Fetch(ctx context.Context, rr transport.RangeRequest) (*transport.Response, error) {
	f := p.f
	end := rr.End
	if end < 0 || end >= f.size {
		end = f.size - 1
	}
	now := f.clk.Now()
	b := &pacedBody{f: f, ctx: ctx, since: now, off: rr.Start, end: end}
	if f.sent == nil {
		b.rate = f.rate(p.slot, rr.Start)
	}
	f.mu.Lock()
	f.reqs = append(f.reqs, pacedReq{slot: p.slot, start: rr.Start, end: end, at: now})
	if b.off <= b.end {
		b.busy = true
		f.busy++
		f.maxBusy = max(f.maxBusy, f.busy)
	}
	f.mu.Unlock()
	return &transport.Response{
		StatusCode: 206, Body: b, ContentLength: end - rr.Start + 1,
		ContentRange: &transport.ContentRange{Start: rr.Start, End: end, Total: f.size},
		Validators:   pacedValidators, FinalURL: rr.URL,
	}, nil
}

type pacedBody struct {
	f     *pacedFactory
	ctx   context.Context
	rate  float64
	since time.Time

	off, end int64 // owned by the reader
	sent     int64

	busy bool // guarded by f.mu
}

func (b *pacedBody) Read(p []byte) (int, error) {
	for {
		if b.off > b.end {
			b.finish()
			return 0, io.EOF
		}
		moved := b.f.nextMove() // before the clock: a move after reading it wakes the wait below
		elapsed := b.f.clk.Now().Sub(b.since)
		allowed := int64(b.rate * elapsed.Seconds())
		if b.f.sent != nil {
			allowed = b.f.sent(elapsed)
		}
		if n := min(int64(len(p)), allowed-b.sent, b.end-b.off+1); n > 0 {
			testserver.Fill(b.f.seed, p[:n], b.off)
			b.off += n
			b.sent += n
			// Every read that delivers bytes moves lastByte, not only the one that ends the
			// body's range: an owner whose segment was split or stolen stops reading its body
			// before that body's end, so its last bytes would otherwise not count.
			b.f.mu.Lock()
			if now := b.f.clk.Now(); now.After(b.f.lastByte) {
				b.f.lastByte = now
			}
			b.f.mu.Unlock()
			if b.off > b.end {
				b.finish()
			}
			return int(n), nil
		}
		select {
		case <-b.ctx.Done():
			return 0, b.ctx.Err()
		case <-moved:
		}
	}
}

func (b *pacedBody) Close() error {
	b.finish()
	return nil
}

// finish marks the body as having no bytes left to send: all sent, or closed.
func (b *pacedBody) finish() {
	b.f.mu.Lock()
	defer b.f.mu.Unlock()
	if b.busy {
		b.busy = false
		b.f.busy--
	}
}

// nextMove returns a channel that the next clock move by advance closes.
func (f *pacedFactory) nextMove() <-chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.moved == nil {
		f.moved = make(chan struct{})
	}
	return f.moved
}

// step moves the clock by one 100 ms tick of the coordinator: 90 ms, then every body
// catches up, then the last 10 ms, which fires the tick; the bodies see that move only
// once the tick is handled. After each move it waits until every goroutine of the run is
// durably blocked: the bodies have sent what is due, the workers have read and written
// it, and the coordinator has handled its tick (and started what it decided to start).
// A goroutine in a system call (a write, an fsync, a rename) is not durably blocked, so
// the clock never runs ahead of the disk; and every tick sees the bytes due 10 ms
// before it, whichever goroutine the scheduler runs first.
func (f *pacedFactory) step() {
	f.advance(90 * time.Millisecond)
	synctest.Wait()
	f.clk.Advance(10 * time.Millisecond)
	synctest.Wait()
	f.wake()
	synctest.Wait()
}

// advance moves the clock by d and lets the bodies see it.
func (f *pacedFactory) advance(d time.Duration) {
	f.clk.Advance(d)
	f.wake()
}

// wake lets the bodies waiting for a clock move see the clock.
func (f *pacedFactory) wake() {
	f.mu.Lock()
	if f.moved != nil {
		close(f.moved)
		f.moved = nil
	}
	f.mu.Unlock()
}

// lastByteAt returns the fake time of the latest body read that delivered bytes, from
// any request (whole, split or stolen): when a finished download's transfer ended,
// however long its conclusion then took.
func (f *pacedFactory) lastByteAt() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastByte
}

func (f *pacedFactory) requests() []pacedReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.reqs)
}

func (f *pacedFactory) stats() (slots, maxBusy int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.slots, f.maxBusy
}

// drive runs id on eng and steps the clock until Run returns, calling each (if set)
// after every step. It fails the test after maxSteps ticks or when Run fails, and
// returns the number of ticks the download took. maxSteps only guards against a hang.
// Once every body has sent its range, a step finds Run still running only while it
// waits on this clock, as the final rename does when it retries a Windows sharing
// violation (an anti-virus scan of the new file) with back-off. Fake time cannot end
// that scan sooner, so those steps also let a little real time pass.
func (f *pacedFactory) drive(t *testing.T, eng *Engine, id model.DownloadID, lim limiter.Limiter,
	onProgress func(Progress), maxSteps int, each func(step int)) int {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- eng.Run(ctx, id, lim, onProgress) }()
	t.Cleanup(func() { // after a failed check; Run's goroutine must end before the bubble does
		cancel()
		<-done // a Run that never returns is a deadlock the bubble reports with its stacks
	})
	synctest.Wait()
	if len(f.requests()) == 0 {
		t.Fatal("no request before the first tick")
	}
	return f.steps(t, done, maxSteps, each)
}

// steps steps the clock until the Run that reports to done returns (and puts its error
// back for a cleanup), as drive does.
func (f *pacedFactory) steps(t *testing.T, done chan error, maxSteps int, each func(step int)) int {
	t.Helper()
	for step := 1; step <= maxSteps; step++ {
		f.step()
		if each != nil {
			each(step)
		}
		select {
		case err := <-done:
			done <- err // for the cleanup
			if err != nil {
				t.Fatalf("Run after %d ticks: %v", step, err)
			}
			return step
		default:
		}
		f.mu.Lock()
		idle := f.busy == 0
		f.mu.Unlock()
		if idle {
			realPause()
		}
	}
	f.mu.Lock()
	n, busy := len(f.reqs), f.busy
	f.mu.Unlock()
	t.Fatalf("download not finished after %d ticks (%d requests, %d with bytes left); goroutines:\n%s",
		maxSteps, n, busy, goroutines())
	return 0
}

// goroutines returns the stacks of every goroutine, for a failure that would otherwise
// only say that something hung.
func goroutines() []byte {
	buf := make([]byte, 1<<20)
	return buf[:runtime.Stack(buf, true)]
}

// checkPacedFile checks that download id completed with the factory's content.
func checkPacedFile(t *testing.T, st *memStore, id model.DownloadID, f *pacedFactory) {
	t.Helper()
	d := st.get(t, id)
	if d.State != model.StateCompleted {
		t.Fatalf("state %v (%s), want completed", d.State, d.Error)
	}
	got, err := os.ReadFile(filepath.Join(d.Dir, d.Filename))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, testserver.Bytes(f.seed, 0, f.size)) {
		t.Fatalf("%s: %d bytes that differ from the served content", d.Filename, len(got))
	}
}

// TestRampSkipsShortDownloads checks the worth-it guard of §3: no connection is added
// while the download would finish within 500 ms at its current speed.
func TestRampSkipsShortDownloads(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		size    int64
		oneSlot bool
	}{
		// 10 MB/s on one connection: 0.3 s left after the first tick, then less.
		{"short", 4_000_000, true},
		// 3.9 s left after the first tick: doubling pays off.
		{"long", 40_000_000, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fakeTest(t, func(t *testing.T, dir string) {
				clk := NewFakeClock(time.Unix(30_000, 0))
				fac := &pacedFactory{clk: clk, size: tc.size, seed: 61, rate: func(int, int64) float64 { return 10_000_000 }}
				st := newMemStore()
				// A doubling is allowed on every tick, so only the guard can hold it back.
				eng := New(st, fac, Options{Clock: clk, StallTimeout: time.Hour, FlushInterval: time.Hour, RampInterval: tickInterval})
				d, err := eng.Add(context.Background(), model.Request{URL: "http://fake.test/" + tc.name + ".bin"}, dir, "", 8, 0)
				if err != nil {
					t.Fatal(err)
				}
				fac.drive(t, eng, d.ID, nil, nil, 600, nil)
				checkPacedFile(t, st, d.ID, fac)
				slots, _ := fac.stats()
				if tc.oneSlot && slots != 1 {
					t.Fatalf("%d connections for a download that ends within 500 ms, want 1", slots)
				}
				if !tc.oneSlot && slots < 2 {
					t.Fatalf("%d connection(s) for a 4 s download, want ramp-up", slots)
				}
			})
		})
	}
}

// latencyFactory is a pacedFactory whose fetches take delay of fake time before the
// response (a connect, TLS and a server's time to first byte). fetches counts the
// fetches that started.
type latencyFactory struct {
	*pacedFactory
	delay   time.Duration
	fetches *atomic.Int64
}

func (f latencyFactory) NewSlot() transport.Fetcher {
	return latencyFetcher{Fetcher: f.pacedFactory.NewSlot(), f: f}
}

type latencyFetcher struct {
	transport.Fetcher
	f latencyFactory
}

func (l latencyFetcher) Fetch(ctx context.Context, rr transport.RangeRequest) (*transport.Response, error) {
	l.f.fetches.Add(1)
	clk := l.f.clk
	for t0 := clk.Now(); ; {
		moved := l.f.nextMove()
		if clk.Now().Sub(t0) >= l.f.delay {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-moved:
		}
	}
	return l.Fetcher.Fetch(ctx, rr)
}

// TestRampGuardAfterSilentStart checks the worth-it guard of §3 when the first ticks of
// a run see no bytes: they do not count towards the speed it decides on, so a download
// with about 0.3 s of work left at 10 MB/s still uses one connection, as it does when
// its bytes come at once (TestRampSkipsShortDownloads).
func TestRampGuardAfterSilentStart(t *testing.T) {
	t.Parallel()
	t.Run("resumed, 300 ms to its first byte", func(t *testing.T) {
		t.Parallel()
		fakeTest(t, testRampGuardResumed)
	})
	t.Run("probe body 300 ms after its headers", func(t *testing.T) {
		t.Parallel()
		fakeTest(t, func(t *testing.T, dir string) {
			clk := NewFakeClock(time.Unix(32_000, 0))
			lag := 300 * time.Millisecond
			fac := &pacedFactory{clk: clk, size: 4_000_000, seed: 63, sent: func(el time.Duration) int64 {
				return int64(10e6 * max(el-lag, 0).Seconds())
			}}
			st := newMemStore()
			eng := New(st, fac, Options{Clock: clk, StallTimeout: time.Hour, FlushInterval: time.Hour})
			d, err := eng.Add(context.Background(), model.Request{URL: "http://fake.test/lag.bin"}, dir, "", 32, 0)
			if err != nil {
				t.Fatal(err)
			}
			fac.drive(t, eng, d.ID, nil, nil, 600, nil)
			checkPacedFile(t, st, d.ID, fac)
			if slots, _ := fac.stats(); slots != 1 {
				t.Errorf("%d connections for 0.4 s of work, want 1", slots)
			}
		})
	})
}

func testRampGuardResumed(t *testing.T, dir string) {
	const size, done = 40_000_000, 37_000_000
	clk := NewFakeClock(time.Unix(31_000, 0))
	fac := &pacedFactory{clk: clk, size: size, seed: 62, rate: func(int, int64) float64 { return 10e6 }}
	lf := latencyFactory{pacedFactory: fac, delay: 300 * time.Millisecond, fetches: new(atomic.Int64)}
	st := newMemStore()
	eng := New(st, lf, Options{Clock: clk, StallTimeout: time.Hour, FlushInterval: time.Hour})
	d, err := eng.Add(context.Background(), model.Request{URL: "http://fake.test/resumed.bin"}, dir, "resumed.bin", 32, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(partPath(dir, "resumed.bin"), testserver.Bytes(fac.seed, 0, done), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(partPath(dir, "resumed.bin"), size); err != nil {
		t.Fatal(err)
	}
	d.Mode, d.Size, d.Validators, d.State = model.ModeRanged, size, pacedValidators, model.StatePaused
	if err := st.SaveCheckpoint(context.Background(), d, []model.Segment{{Start: 0, End: size - 1, Durable: done}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- eng.Run(ctx, d.ID, nil, nil) }()
	t.Cleanup(func() { cancel(); <-finished })
	// The coordinator's ticker is armed before its first worker fetches.
	synctest.Wait()
	if lf.fetches.Load() == 0 {
		t.Fatal("the resumed connection did not start before the first tick")
	}
	fac.steps(t, finished, 100, nil)
	checkPacedFile(t, st, d.ID, fac)
	if slots, _ := fac.stats(); slots != 1 {
		t.Errorf("%d connections for a resumed download with 0.3 s of work left, want 1", slots)
	}
}
