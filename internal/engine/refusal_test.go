package engine

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"downex/internal/model"
	"downex/internal/testserver"
	"downex/internal/transport"
)

// limitFactory serves a ranged resource of size bytes from a server that answers at
// most limit requests at once, as a server with a per-client connection limit does. It
// refuses the others with 503 and Retry-After: 1, or (closes) by closing the connection
// before answering. A body it serves delivers 1 KiB and then blocks until its request is
// cancelled, so connections stay open while a test drives the fake clock. Its tests run
// in fakeTest bubbles.
type limitFactory struct {
	size   int64
	closes bool
	// lag is how long a closed body still counts as served, in time of clk (which is then
	// required): the server's view of the connection lags behind the client's, and the
	// test's order of events (lag, then Retry-After, both on the fake clock) must not
	// depend on how fast the machine runs.
	lag time.Duration
	// With clk set, the first body (the probe's) delivers up to probeTo bytes once the
	// clock reached probeAt.
	clk     *FakeClock
	probeAt time.Time
	probeTo int64
	// With clk and pace set, every body delivers its whole range: pace bytes per tick
	// of the fake clock after its first KiB, then io.EOF. status (default 503) answers
	// the refused requests.
	pace   int64
	status int

	mu        sync.Mutex
	moved     chan struct{} // closed by the next clock move (nil: nobody waits)
	limit     int
	active    int         // bodies being served
	leaving   []time.Time // clock times at which closed bodies stop counting as served
	released  int         // bodies closed and no longer counted
	fetches   int
	refused   int
	delivered int // bodies that delivered their first KiB
	peak      int // most bodies served at once
}

func (f *limitFactory) setLimit(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.limit = n
}

func (f *limitFactory) releasedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forgetLocked()
	return f.released
}

func (f *limitFactory) counts() (fetches, refused, active, delivered int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forgetLocked()
	return f.fetches, f.refused, f.active, f.delivered
}

// forgetLocked stops counting the closed bodies whose lag has passed.
func (f *limitFactory) forgetLocked() {
	if len(f.leaving) == 0 {
		return
	}
	now := f.clk.Now()
	kept := f.leaving[:0]
	for _, at := range f.leaving {
		if now.Before(at) {
			kept = append(kept, at)
			continue
		}
		f.active--
		f.released++
	}
	f.leaving = kept
}

func (f *limitFactory) NewSlot() transport.Fetcher { return limitFetcher{f} }

type limitFetcher struct{ f *limitFactory }

func (limitFetcher) Close() {}

func (l limitFetcher) Fetch(ctx context.Context, rr transport.RangeRequest) (*transport.Response, error) {
	f := l.f
	f.mu.Lock()
	f.forgetLocked()
	f.fetches++
	if f.active >= f.limit {
		f.refused++
		f.mu.Unlock()
		if f.closes {
			return nil, fmt.Errorf("read response: %w", io.EOF) // what net/http returns
		}
		status := f.status
		if status == 0 {
			status = http.StatusServiceUnavailable
		}
		return &transport.Response{StatusCode: status, Body: http.NoBody, RetryAfter: time.Second}, nil
	}
	f.active++
	f.peak = max(f.peak, f.active)
	probe := f.fetches == 1
	f.mu.Unlock()
	end := rr.End
	if end < 0 {
		end = f.size - 1
	}
	return &transport.Response{
		StatusCode: 206, Body: &limitBody{f: f, ctx: ctx, off: rr.Start, end: end, probe: probe, start: rr.Start, from: f.now()}, ContentLength: end - rr.Start + 1,
		ContentRange: &transport.ContentRange{Start: rr.Start, End: end, Total: f.size},
		Validators:   model.Validators{ETag: `"l"`, StrongETag: true}, FinalURL: rr.URL,
	}, nil
}

type limitBody struct {
	f        *limitFactory
	ctx      context.Context
	off, end int64
	sent     bool
	probe    bool
	start    int64     // first byte of the range (pace)
	from     time.Time // clock time of the request (pace)
	once     sync.Once
}

func (b *limitBody) Read(p []byte) (int, error) {
	if !b.sent {
		b.sent = true
		n := min(len(p), 1024, int(b.end-b.off+1))
		testserver.Fill(fakeSeed, p[:n], b.off)
		b.off += int64(n)
		b.f.mu.Lock()
		b.f.delivered++
		b.f.mu.Unlock()
		return n, nil
	}
	if b.f.pace > 0 {
		return b.paced(p)
	}
	for b.probe && b.f.clk != nil && b.off < b.f.probeTo {
		moved := b.f.nextMove() // before the clock: a move after reading it wakes the wait
		if b.f.clk.Now().Before(b.f.probeAt) {
			if err := b.wait(moved); err != nil {
				return 0, err
			}
			continue
		}
		n := int(min(int64(len(p)), b.f.probeTo-b.off))
		testserver.Fill(fakeSeed, p[:n], b.off)
		b.off += int64(n)
		return n, nil
	}
	<-b.ctx.Done()
	b.release()
	return 0, b.ctx.Err()
}

func (f *limitFactory) now() time.Time {
	if f.clk == nil {
		return time.Time{}
	}
	return f.clk.Now()
}

func (f *limitFactory) peakServed() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.peak
}

// paced delivers the body's range at f.pace bytes per tick of the fake clock since the
// request, then io.EOF; the response then stops counting as served after f.lag.
func (b *limitBody) paced(p []byte) (int, error) {
	for {
		if b.off > b.end {
			b.release()
			return 0, io.EOF
		}
		moved := b.f.nextMove()
		ticks := int64(b.f.clk.Now().Sub(b.from) / tickInterval)
		if due := b.start + 1024 + ticks*b.f.pace; b.off < due {
			n := int(min(int64(len(p)), due-b.off, b.end-b.off+1))
			testserver.Fill(fakeSeed, p[:n], b.off)
			b.off += int64(n)
			return n, nil
		}
		if err := b.wait(moved); err != nil {
			return 0, err
		}
	}
}

// wait waits until moved is closed (see limitFactory.advance) or the request is
// cancelled.
func (b *limitBody) wait(moved <-chan struct{}) error {
	select {
	case <-moved:
		return nil
	case <-b.ctx.Done():
		b.release()
		return b.ctx.Err()
	}
}

// nextMove returns a channel that the next clock move by advance closes.
func (f *limitFactory) nextMove() <-chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.moved == nil {
		f.moved = make(chan struct{})
	}
	return f.moved
}

// advance moves clk (required) by ticks of the coordinator. After each it waits until
// the run has handled the tick, and only then lets the bodies see the clock moved: they
// send what became due, and it waits until the run has handled that too. So the
// coordinator measures the bytes of a tick interval on the next tick, as the earlier
// real-time polling of the bodies mostly let it, and which of the goroutines woken by
// one clock move the scheduler runs first moves no split or takeover.
func (f *limitFactory) advance(ticks int) {
	for range ticks {
		f.clk.Advance(tickInterval)
		synctest.Wait()
		f.mu.Lock()
		if f.moved != nil {
			close(f.moved)
			f.moved = nil
		}
		f.mu.Unlock()
		synctest.Wait()
	}
}

func (b *limitBody) Close() error {
	b.release()
	return nil
}

func (b *limitBody) release() {
	b.once.Do(func() {
		b.f.mu.Lock()
		defer b.f.mu.Unlock()
		if b.f.lag <= 0 {
			b.f.active--
			b.f.released++
			return
		}
		b.f.leaving = append(b.f.leaving, b.f.clk.Now().Add(b.f.lag))
	})
}

// TestRefusedBurstConverges checks §3 and §10 against a server that serves 4 connections
// and refuses the rest, with 503 or by closing the connection unanswered. The first
// ramp-up step opens all 32 connections at once; the refused ones lower the per-host limit
// to the 4 the server serves and stop at once, without retrying, and their ranges go back
// to the pool. When the host penalty expires the download grows back by doubling, each
// step waiting until every connection receives, instead of opening 28 again at once.
func TestRefusedBurstConverges(t *testing.T) {
	t.Parallel()
	for _, closes := range []bool{false, true} {
		name := "503"
		if closes {
			name = "closed unanswered"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fakeTest(t, func(t *testing.T, dir string) { testRefusedBurstConverges(t, dir, closes) })
		})
	}
}

func testRefusedBurstConverges(t *testing.T, dir string, closes bool) {
	const ttl = 30 * time.Second
	clk := NewFakeClock(time.Unix(40_000, 0))
	fac := &limitFactory{size: 1 << 30, closes: closes, limit: 4, clk: clk}
	// No stall handling: the served bodies stay silent after their first KiB.
	eng := New(newMemStore(), fac, Options{Clock: clk, StallTimeout: time.Hour, MidStallTimeout: time.Hour,
		FlushInterval: time.Hour, HostPenaltyTTL: ttl})
	d, err := eng.Add(context.Background(), model.Request{URL: "http://fake.test/limit.bin"}, dir, "", 32, 0)
	if err != nil {
		t.Fatal(err)
	}
	var log progressLog
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- eng.Run(ctx, d.ID, nil, log.on) }()
	defer func() { cancel(); <-done }()
	host := hostKey(d.Request.URL)

	synctest.Wait()
	if _, _, _, n := fac.counts(); n != 1 {
		t.Fatalf("%d bodies delivered before the first tick, want the probe's", n)
	}
	// The first tick that sees the probe's bytes: 31 more connections.
	advanceUntil(t, fac.advance, 10, "the burst", func() bool { fetches, _, _, _ := fac.counts(); return fetches >= 32 })
	if _, _, _, delivered := fac.counts(); delivered != 4 || eng.hostLimit(host) != 4 {
		t.Fatalf("after the burst: %d delivered, per-host limit %d; want 4 and 4", delivered, eng.hostLimit(host))
	}
	fac.advance(30) // 3 s: three Retry-After periods, several backoffs
	fetches, refused, active, _ := fac.counts()
	if fetches != 32 || refused != 28 || active != 4 {
		t.Fatalf("after the burst: %d fetches, %d refused, %d served; want 32, 28, 4 (refused connections retried)",
			fetches, refused, active)
	}
	if lim := eng.hostLimit(host); lim != 4 {
		t.Fatalf("per-host limit %d, want the 4 the server serves", lim)
	}
	if p, ok := log.last(); !ok || p.Connections != 4 {
		t.Fatalf("%d connections shown, want 4", p.Connections)
	}

	// The server lifts its limit and the penalty expires.
	fac.setLimit(1000)
	clk.Advance(ttl)
	synctest.Wait()
	for _, want := range []int{8, 16, 32} {
		for ticks := 0; ; ticks++ {
			// advance waited until a step had started all its connections and
			// they had delivered their first bytes.
			_, _, active, delivered := fac.counts()
			if delivered < active {
				t.Fatalf("%d of %d connections without their first bytes after a tick", active-delivered, active)
			}
			if active >= want {
				break
			}
			if ticks == 50 {
				t.Fatalf("not %d connections after 50 ticks", want)
			}
			fac.advance(1) // steps come at most every 250 ms: this sees each one
		}
		if _, _, active, _ := fac.counts(); active != want {
			t.Fatalf("%d connections after the penalty expired, want %d (doubling)", active, want)
		}
	}
}

// TestReplacementRefusalKeepsLimit checks §10 for a request that replaces a connection
// the download just closed: the 4 connections of a server that serves 4 stall and are
// cancelled, and the server still counts them when their replacements ask. The 503s
// those get are retried after Retry-After and do not lower the per-host limit, which
// would otherwise drop by one with every replacement.
func TestReplacementRefusalKeepsLimit(t *testing.T) {
	t.Parallel()
	fakeTest(t, testReplacementRefusalKeepsLimit)
}

// advanceUntil calls advance(1) until cond holds, failing the test after limit ticks.
func advanceUntil(t *testing.T, advance func(ticks int), limit int, what string, cond func() bool) {
	t.Helper()
	for ticks := 0; !cond(); ticks++ {
		if ticks == limit {
			t.Fatalf("not %s after %d ticks", what, limit)
		}
		advance(1)
	}
}

func testReplacementRefusalKeepsLimit(t *testing.T, dir string) {
	clk := NewFakeClock(time.Unix(41_000, 0))
	fac := &limitFactory{size: 1 << 30, limit: 4, lag: 50 * time.Millisecond, clk: clk}
	eng := New(newMemStore(), fac, Options{Clock: clk, StallTimeout: 2 * time.Second, FlushInterval: time.Hour})
	d, err := eng.Add(context.Background(), model.Request{URL: "http://fake.test/replace.bin"}, dir, "", 4, 0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- eng.Run(ctx, d.ID, nil, nil) }()
	defer func() { cancel(); <-done }()
	synctest.Wait()
	if _, _, _, n := fac.counts(); n != 1 {
		t.Fatalf("%d bodies delivered before the first tick, want the probe's", n)
	}
	// The first tick that sees the probe's bytes opens the rest.
	advanceUntil(t, fac.advance, 10, "4 connections", func() bool { _, _, active, n := fac.counts(); return active == 4 && n == 4 })
	// Every connection stalls after its first KiB: after 2 s they are cancelled and
	// replaced while the server still counts them, and the replacements are refused.
	advanceUntil(t, fac.advance, 50, "4 refused replacements", func() bool { _, refused, _, _ := fac.counts(); return refused >= 4 })
	// The server forgets the stalled connections (50 ms) before the replacements retry
	// (Retry-After: 1 s).
	advanceUntil(t, fac.advance, 9, "the stalled connections released", func() bool { return fac.releasedCount() >= 4 })
	advanceUntil(t, fac.advance, 50, "4 replacements served", func() bool { _, _, active, n := fac.counts(); return active == 4 && n == 8 })
	if lim := eng.hostLimit(hostKey(d.Request.URL)); lim != 0 {
		t.Fatalf("per-host limit %d after refused replacements, want none", lim)
	}
}

// TestAbandonedResponseRefusalKeepsLimit is TestReplacementRefusalKeepsLimit for a
// worker that closes its own response with bytes left: the probe's response runs past
// its segment while the next one's owner receives, so the probe gets other work and
// closes it. The server still counts that response when the probe's next request comes,
// so it answers 503; the request is retried and the per-host limit stays.
func TestAbandonedResponseRefusalKeepsLimit(t *testing.T) {
	t.Parallel()
	fakeTest(t, testAbandonedResponseRefusalKeepsLimit)
}

func testAbandonedResponseRefusalKeepsLimit(t *testing.T, dir string) {
	const size = 4 << 20
	clk := NewFakeClock(time.Unix(42_000, 0))
	fac := &limitFactory{size: size, limit: 2, lag: 50 * time.Millisecond,
		clk: clk, probeAt: time.Unix(42_000, 0).Add(250 * time.Millisecond), probeTo: 3 << 20}
	eng := New(newMemStore(), fac, Options{Clock: clk, StallTimeout: time.Hour, FlushInterval: time.Hour})
	d, err := eng.Add(context.Background(), model.Request{URL: "http://fake.test/abandon.bin"}, dir, "", 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- eng.Run(ctx, d.ID, nil, nil) }()
	defer func() { cancel(); <-done }()
	synctest.Wait()
	if _, _, _, n := fac.counts(); n != 1 {
		t.Fatalf("%d bodies delivered before the first tick, want the probe's", n)
	}
	fac.advance(1) // the second connection takes the upper half
	if _, _, active, n := fac.counts(); active != 2 || n != 2 {
		t.Fatalf("after the first tick: %d served, %d delivered; want 2 connections", active, n)
	}
	// The probe's bytes past its segment arrive at 250 ms.
	advanceUntil(t, fac.advance, 50, "the probe's next request", func() bool { fetches, _, _, _ := fac.counts(); return fetches >= 3 })
	// The server forgets the probe's first response (50 ms) before the probe retries
	// (Retry-After: 1 s).
	advanceUntil(t, fac.advance, 9, "the abandoned response released", func() bool { return fac.releasedCount() >= 1 })
	advanceUntil(t, fac.advance, 50, "the retried request served", func() bool { _, _, active, n := fac.counts(); return active == 2 && n >= 3 })
	_, refused, _, _ := fac.counts()
	if lim := eng.hostLimit(hostKey(d.Request.URL)); refused == 0 || lim != 0 {
		t.Fatalf("%d refused, per-host limit %d; want the replacement refused and no limit", refused, lim)
	}
}

// TestConnectionLimitServer checks §10 over real connections: a server that serves 4
// requests at once and refuses the others, with 503 or by closing their connections
// unanswered. The download completes, the per-host limit drops to the 4 it serves, and
// the burst's refused connections are not retried one by one for the rest of the
// download. A request that replaces a connection the download just closed (a takeover
// cancels its owner's request) may find the server still counting the old one: it is
// refused and retried, and does not lower the limit. Each slot keeps one connection, as
// transport.HTTPFactory's do, so a slot's next request waits for its previous one.
func TestConnectionLimitServer(t *testing.T) {
	t.Parallel()
	for _, abort := range []bool{false, true} {
		name := "503"
		if abort {
			name = "closed unanswered"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			// Retries back off from 200 ms (default 500 ms): the server needs a moment
			// to notice a closed connection, more on a loaded machine.
			opts := fastOpts()
			opts.RetryBaseDelay, opts.RetryMaxDelay = 200*time.Millisecond, time.Second
			h := newHarness(t, testserver.Options{Size: 16 << 20, Seed: 73, ETag: `"r"`, Throttle: 4 << 20,
				Limit: 4, LimitAbort: abort, RetryAfter: "1"}, opts)
			h.fac.newTransport = func() *http.Transport {
				tr := h.srv.NewTransport()
				tr.MaxConnsPerHost, tr.MaxIdleConnsPerHost = 1, 1
				return tr
			}
			d := h.add("/r.bin", "", 32)
			if err := h.run(d.ID, nil, nil); err != nil {
				t.Fatalf("Run: %v", err)
			}
			h.verify(d.ID)
			refused := 0
			for _, r := range h.srv.Requests() {
				if r.Status == 0 || r.Status == 503 {
					refused++
				}
			}
			if lim := h.eng.hostLimit(hostKey(h.srv.URL)); lim != 4 {
				t.Errorf("per-host limit %d after the refusals, want the 4 the server serves", lim)
			}
			if refused > 31 {
				t.Errorf("%d refused requests, want the 28 of the first burst and a few replacements", refused)
			}
			if n := h.srv.MaxConcurrent(); n > 4 {
				t.Errorf("served %d at once", n)
			}
		})
	}
}

// TestFinishedResponseRefusalKeepsLimit checks §10 for a server that serves 3 requests at
// once and answers 429 beyond, and counts a finished response a moment longer (a
// load balancer, a request finalised after its last byte, the client reading the last
// byte first). 5 connections are allowed: the ramp-up step's two refusals lower the
// per-host limit to the 3 the server serves. A connection that finished its range asks
// for the next one at once and is refused while the server still counts its previous
// response: that request adds no connection, so it is retried after Retry-After and the
// limit stays 3. Taking it as pushback lowered the limit 3 → 2 → 1 (measured
// 2026-11-10 against a local server with these rules: 3.1 MiB/s where 3 connections give
// 4.7–5.9).
func TestFinishedResponseRefusalKeepsLimit(t *testing.T) {
	t.Parallel()
	fakeTest(t, testFinishedResponseRefusalKeepsLimit)
}

func testFinishedResponseRefusalKeepsLimit(t *testing.T, dir string) {
	const size = 3 << 20
	clk := NewFakeClock(time.Unix(43_000, 0))
	fac := &limitFactory{size: size, limit: 3, lag: 50 * time.Millisecond, clk: clk, pace: 64 << 10,
		status: http.StatusTooManyRequests}
	eng := New(newMemStore(), fac, Options{Clock: clk, StallTimeout: time.Hour, FlushInterval: time.Hour})
	d, err := eng.Add(context.Background(), model.Request{URL: "http://fake.test/finished.bin"}, dir, "", 5, 0)
	if err != nil {
		t.Fatal(err)
	}
	var log progressLog
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- eng.Run(ctx, d.ID, nil, log.on) }()
	defer cancel()
	host := hostKey(d.Request.URL)
	synctest.Wait()
	if _, _, _, n := fac.counts(); n != 1 {
		t.Fatalf("%d bodies delivered before the first tick, want the probe's", n)
	}
	lowest := 0
	for ticks := 0; ; ticks++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
		default:
			if ticks == 600 {
				t.Fatal("not complete after 600 ticks")
			}
			fac.advance(1)
			if lim := eng.hostLimit(host); lim > 0 && (lowest == 0 || lim < lowest) {
				lowest = lim
			}
			continue
		}
		break
	}
	fetches, refused, _, _ := fac.counts()
	if lowest != 3 || eng.hostLimit(host) != 3 {
		t.Errorf("per-host limit fell to %d (now %d), want the 3 the server serves", lowest, eng.hostLimit(host))
	}
	if peak := fac.peakServed(); peak != 3 {
		t.Errorf("%d served at once, want 3", peak)
	}
	// The burst's 2, then at most one refusal for each later request.
	if served := fetches - refused; refused > 2+served {
		t.Errorf("%d of %d requests refused", refused, fetches)
	}
	t.Logf("%d requests, %d refused", fetches, refused)
}
