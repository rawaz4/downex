package mediajob

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"downex/internal/testserver"
	"downex/internal/transport"
)

// The copier's stall watchdog (docs/MEDIA-SPEC.md §7): a response that delivers no byte
// for mediaStallTimeout while the copier waits in Read is closed, and the copy fails
// with errStalled, which the retries treat as transient. Limiter waits and writes never
// count as idle.

// stallClock is a fake Clock for the watchdog tests: time moves only by advance, which
// fires the timers that are due.
type stallClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*stallTimer
}

type stallTimer struct {
	clk     *stallClock
	c       chan time.Time
	at      time.Time
	stopped bool // guarded by clk.mu
}

func (t *stallTimer) C() <-chan time.Time { return t.c }

func (t *stallTimer) Stop() bool {
	t.clk.mu.Lock()
	defer t.clk.mu.Unlock()
	was := !t.stopped
	t.stopped = true
	return was
}

func newStallClock() *stallClock {
	return &stallClock{now: time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)}
}

func (c *stallClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *stallClock) NewTimer(d time.Duration) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &stallTimer{clk: c, c: make(chan time.Time, 1), at: c.now.Add(d)}
	if d <= 0 {
		t.c <- c.now
		t.stopped = true
		return t
	}
	c.timers = append(c.timers, t)
	return t
}

func (c *stallClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	keep := c.timers[:0]
	for _, t := range c.timers {
		switch {
		case t.stopped:
		case !t.at.After(c.now):
			t.stopped = true
			t.c <- c.now
		default:
			keep = append(keep, t)
		}
	}
	c.timers = keep
}

// pending counts the timers that have neither fired nor been stopped.
func (c *stallClock) pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, t := range c.timers {
		if !t.stopped {
			n++
		}
	}
	return n
}

// stallWaitFor polls cond for up to 5 s.
func stallWaitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(time.Millisecond)
	}
}

// stallReader serves data, then blocks in Read until it is closed.
type stallReader struct {
	data    []byte
	off     int
	blocked chan struct{} // closed when Read first blocks
	closed  chan struct{}
	bOnce   sync.Once
	cOnce   sync.Once
}

func newStallReader(n int) *stallReader {
	return &stallReader{data: bytes.Repeat([]byte{0x47}, n), blocked: make(chan struct{}), closed: make(chan struct{})}
}

func (r *stallReader) Read(p []byte) (int, error) {
	if r.off < len(r.data) {
		n := copy(p, r.data[r.off:])
		r.off += n
		return n, nil
	}
	r.bOnce.Do(func() { close(r.blocked) })
	<-r.closed
	return 0, errors.New("read on closed body")
}

func (r *stallReader) Close() error {
	r.cOnce.Do(func() { close(r.closed) })
	return nil
}

func (r *stallReader) isClosed() bool {
	select {
	case <-r.closed:
		return true
	default:
		return false
	}
}

// stallWatchers counts the goroutines running a stall watchdog.
func stallWatchers() int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return strings.Count(string(buf[:n]), "mediajob.(*stallWatch).run")
		}
		buf = make([]byte, 2*len(buf))
	}
}

type stallResult struct {
	n   int64
	err error
}

// stallCopy runs cp.copy in the background.
func stallCopy(ctx context.Context, cp *copier, dst io.Writer, src io.Reader, n int64) <-chan stallResult {
	out := make(chan stallResult, 1)
	go func() {
		k, err := cp.copy(ctx, dst, src, n, true)
		out <- stallResult{k, err}
	}()
	return out
}

func stallNotYet(t *testing.T, res <-chan stallResult, what string) {
	t.Helper()
	select {
	case r := <-res:
		t.Fatalf("%s: copy returned %d, %v", what, r.n, r.err)
	case <-time.After(20 * time.Millisecond):
	}
}

func stallAwait(t *testing.T, res <-chan stallResult) stallResult {
	t.Helper()
	select {
	case r := <-res:
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("copy did not return")
		return stallResult{}
	}
}

func TestStallCopierTripsAfterTimeout(t *testing.T) {
	clk := newStallClock()
	var counted int64
	cp := newCopier(nil, func(n int) { counted += int64(n) })
	cp.clock = clk
	src := newStallReader(64 << 10)
	var dst bytes.Buffer
	res := stallCopy(testContext(t), cp, &dst, src, 1<<20)
	<-src.blocked
	stallWaitFor(t, func() bool { return clk.pending() == 1 })
	// Three polls of 5 s: 15 s without data is not a stall.
	for i := 1; i <= 3; i++ {
		clk.advance(mediaStallTimeout / 4)
		stallWaitFor(t, func() bool { return clk.pending() == 1 })
		stallNotYet(t, res, fmt.Sprintf("after %v", time.Duration(i)*mediaStallTimeout/4))
	}
	if src.isClosed() {
		t.Fatal("the body was closed before the stall timeout")
	}
	clk.advance(mediaStallTimeout / 4)
	r := stallAwait(t, res)
	if !errors.Is(r.err, errStalled) || r.n != 64<<10 || counted != 64<<10 || dst.Len() != 64<<10 {
		t.Fatalf("copy = %d, %v (counted %d, wrote %d), want 64 KiB and errStalled", r.n, r.err, counted, dst.Len())
	}
	if !strings.Contains(r.err.Error(), "65536") {
		t.Errorf("error %q does not name the bytes received", r.err)
	}
	if !src.isClosed() {
		t.Error("the stalled body was not closed")
	}
	if n := stallWatchers(); n != 0 || clk.pending() != 0 {
		t.Errorf("after the copy: %d watchdogs, %d timers", n, clk.pending())
	}
}

// tickReader returns one byte for every tick, then io.EOF once ticks is closed.
type tickReader struct {
	ticks   chan struct{}
	waiting chan struct{} // receives once per Read that waits for a tick
}

func (r *tickReader) Read(p []byte) (int, error) {
	r.waiting <- struct{}{}
	if _, ok := <-r.ticks; !ok {
		return 0, io.EOF
	}
	p[0] = 'x'
	return 1, nil
}

func (r *tickReader) Close() error { return nil }

// A slow body that keeps delivering (one byte every 5 s) never trips the watchdog.
func TestStallCopierSlowReaderNeverTrips(t *testing.T) {
	clk := newStallClock()
	cp := newCopier(nil, nil)
	cp.clock = clk
	src := &tickReader{ticks: make(chan struct{}), waiting: make(chan struct{}, 1)}
	var dst bytes.Buffer
	res := stallCopy(testContext(t), cp, &dst, src, -1)
	const bytesWanted = 12 // 60 s of fake time
	for range bytesWanted {
		<-src.waiting
		stallWaitFor(t, func() bool { return clk.pending() == 1 })
		clk.advance(5 * time.Second)
		stallWaitFor(t, func() bool { return clk.pending() == 1 })
		src.ticks <- struct{}{}
	}
	<-src.waiting
	close(src.ticks)
	r := stallAwait(t, res)
	if r.err != nil || r.n != bytesWanted || dst.Len() != bytesWanted {
		t.Fatalf("copy = %d, %v, want %d bytes", r.n, r.err, bytesWanted)
	}
	if n := stallWatchers(); n != 0 || clk.pending() != 0 {
		t.Errorf("after the copy: %d watchdogs, %d timers", n, clk.pending())
	}
}

// stallLimiter makes every WaitN take d on clk.
type stallLimiter struct {
	clk   Clock
	d     time.Duration
	waits chan struct{}
}

func (l *stallLimiter) WaitN(ctx context.Context, n int) error {
	l.waits <- struct{}{}
	return sleep(ctx, l.clk, l.d)
}

// A limiter wait of 30 s is not idle time.
func TestStallCopierLimiterWaitIsNotIdle(t *testing.T) {
	clk := newStallClock()
	lim := &stallLimiter{clk: clk, d: 30 * time.Second, waits: make(chan struct{}, 4)}
	cp := newCopier(lim, nil)
	cp.clock = clk
	src := io.NopCloser(bytes.NewReader(bytes.Repeat([]byte{1}, sliceCapped)))
	var dst bytes.Buffer
	res := stallCopy(testContext(t), cp, &dst, src, sliceCapped)
	<-lim.waits
	// The watchdog's poll and the limiter's 30 s wait.
	stallWaitFor(t, func() bool { return clk.pending() == 2 })
	for range 5 {
		clk.advance(mediaStallTimeout / 4)
		stallWaitFor(t, func() bool { return clk.pending() == 2 })
		stallNotYet(t, res, "during the limiter wait")
	}
	clk.advance(mediaStallTimeout / 4) // 30 s: the wait ends
	r := stallAwait(t, res)
	if r.err != nil || r.n != sliceCapped {
		t.Fatalf("copy = %d, %v, want %d bytes", r.n, r.err, sliceCapped)
	}
	if n := stallWatchers(); n != 0 || clk.pending() != 0 {
		t.Errorf("after the copy: %d watchdogs, %d timers", n, clk.pending())
	}
}

// The watchdog runs only for bodies it can close, and is gone after every return:
// success, a read error, a cancelled context, a failed write.
func TestStallWatchdogExits(t *testing.T) {
	clk := newStallClock()
	cp := newCopier(nil, nil)
	cp.clock = clk
	ctx := testContext(t)

	if _, err := cp.copy(ctx, io.Discard, bytes.NewReader(make([]byte, 100)), 100, true); err != nil || clk.pending() != 0 {
		t.Fatalf("plain reader: %v, %d timers", err, clk.pending())
	}
	if _, err := cp.copy(ctx, io.Discard, io.NopCloser(bytes.NewReader(make([]byte, 100))), 100, true); err != nil {
		t.Fatal(err)
	}
	if _, err := cp.copy(ctx, io.Discard, io.NopCloser(bytes.NewReader(make([]byte, 10))), 100, true); !errors.Is(err, ErrTruncated) {
		t.Fatalf("short body: %v", err)
	}
	if _, err := cp.copy(ctx, failWriter{}, io.NopCloser(bytes.NewReader(make([]byte, 10))), 10, true); err == nil {
		t.Fatal("failed write: no error")
	}
	cctx, cancel := context.WithCancel(ctx)
	src := newStallReader(0)
	res := stallCopy(cctx, cp, io.Discard, src, 10)
	<-src.blocked
	cancel()
	_ = src.Close() // what the transport does when the request's context ends
	if r := stallAwait(t, res); r.err == nil || errors.Is(r.err, errStalled) {
		t.Fatalf("cancelled copy = %v, want the read error", r.err)
	}
	if n := stallWatchers(); n != 0 || clk.pending() != 0 {
		t.Errorf("after the copies: %d watchdogs, %d timers", n, clk.pending())
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("disk on fire") }

func TestStallIsTransient(t *testing.T) {
	wrapped := fmt.Errorf("mediajob: track %q fragment %d: %w", "v0", 3, fmt.Errorf("%w: 10 bytes", errStalled))
	if !transient(errStalled) || !transient(wrapped) {
		t.Fatal("a stalled response is not retried")
	}
	if got := shortReason(wrapped); got != "no data received" {
		t.Errorf("shortReason = %q", got)
	}
}

// stallScaledClock runs factor times faster than real time.
type stallScaledClock struct {
	start  time.Time
	base   time.Time
	factor time.Duration
}

func newStallScaledClock(factor time.Duration) *stallScaledClock {
	return &stallScaledClock{start: time.Now(), base: time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC), factor: factor}
}

func (c *stallScaledClock) Now() time.Time { return c.base.Add(time.Since(c.start) * c.factor) }

func (c *stallScaledClock) NewTimer(d time.Duration) Timer {
	return realTimer{time.NewTimer(d / c.factor)}
}

// A fragment whose response stalls midway is retried once the watchdog closes it, and
// the job completes byte for byte (HLS over HTTP/1.1 and HTTP/2).
func TestStallRunRetriesFragment(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		t.Run(map[bool]string{false: "h1", true: "h2"}[h2], func(t *testing.T) {
			const fragSize = 1 << 20
			ts := testserver.New(testserver.Options{Size: fragSize, Seed: 9, StallAfter: 512 << 10, StallCount: 1, HTTP2: h2})
			t.Cleanup(ts.Close)
			s := newServer(t)
			names := []string{"seg0.ts", "seg1.ts", "", "seg3.ts"}
			var want []byte
			for i, name := range names {
				if name == "" {
					names[i] = ts.URL + "/frag.ts"
					want = append(want, testserver.Bytes(9, 0, fragSize)...)
					continue
				}
				data := tsSegment(uint64(50+i), 40)
				s.add("/st/"+name, data, "video/mp2t")
				want = append(want, data...)
			}
			s.add("/st/index.m3u8", []byte(mediaPlaylist("", names)), hlsType)

			asm := &fakeAssembler{}
			opts := testOptions(t, asm)
			opts.Clock = newStallScaledClock(10) // the 20 s stall takes 2 s
			if h2 {
				roots := x509.NewCertPool()
				roots.AddCert(ts.Certificate())
				o := transport.DefaultHTTPOptions()
				o.Proxy = "direct"
				f, err := transport.NewHTTPFactoryWithRoots(o, roots)
				if err != nil {
					t.Fatal(err)
				}
				opts.Factory = f
			}
			var rec recorder
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			c, err := Run(ctx, opts, Selection{ManifestURL: s.url("/st/index.m3u8")}, nil, rec.onProgress, rec.onCheckpoint)
			if err != nil || c != "mp4" {
				t.Fatalf("Run = %q, %v", c, err)
			}
			if got := readFile(t, opts.OutPath); !bytes.Equal(got, want) {
				t.Fatalf("output: %d bytes, want %d", len(got), len(want))
			}
			reqs := ts.Requests()
			if len(reqs) != 2 {
				t.Fatalf("the stalled fragment was requested %d times, want 2: %+v", len(reqs), reqs)
			}
			if h2 && reqs[0].Proto != "HTTP/2.0" {
				t.Errorf("served over %s", reqs[0].Proto)
			}
			total := int64(len(want))
			for _, p := range rec.progress {
				if p.BytesDone > total {
					t.Fatalf("progress %+v counts more than the %d bytes", p, total)
				}
			}
			if last := rec.progress[len(rec.progress)-1]; last.BytesDone != total || last.FragmentsDone != 4 {
				t.Errorf("final progress %+v", last)
			}
			if n := stallWatchers(); n != 0 {
				t.Errorf("%d watchdogs left", n)
			}
		})
	}
}
