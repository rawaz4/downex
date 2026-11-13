package engine

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"downex/internal/model"
	"downex/internal/transport"
)

// TestMidStallTimeout pins the stall timeout of a connection whose response already
// delivered body bytes (§5): MidStallTimeout, at least 4·RTT once a request latency is
// known and twice the longest wait for that response's bytes, never more than
// StallTimeout.
func TestMidStallTimeout(t *testing.T) {
	const s = time.Second
	for _, tc := range []struct {
		name                 string
		stall, mid, rtt, gap time.Duration
		want                 time.Duration
	}{
		{"defaults, RTT unknown", 20 * s, 5 * s, 0, 0, 5 * s},
		{"short RTT, short gaps", 20 * s, 5 * s, 300 * time.Millisecond, 100 * time.Millisecond, 5 * s},
		{"4·RTT above it", 20 * s, 5 * s, 2 * s, 0, 8 * s},
		{"2·gap above it", 20 * s, 5 * s, 300 * time.Millisecond, 6 * s, 12 * s},
		{"4·RTT above 2·gap", 20 * s, 5 * s, 4 * s, 7 * s, 16 * s},
		{"RTT: never above the stall timeout", 20 * s, 5 * s, 6 * s, 0, 20 * s},
		{"gap: never above the stall timeout", 20 * s, 5 * s, 0, 15 * s, 20 * s},
		{"a shorter stall timeout", 300 * time.Millisecond, 5 * s, 0, 0, 300 * time.Millisecond},
	} {
		o := Options{StallTimeout: tc.stall, MidStallTimeout: tc.mid}
		if got := midStallTimeout(&o, tc.rtt, tc.gap); got != tc.want {
			t.Errorf("%s: midStallTimeout(stall %v, mid %v, RTT %v, gap %v) = %v, want %v",
				tc.name, tc.stall, tc.mid, tc.rtt, tc.gap, got, tc.want)
		}
	}
}

// TestSlowSteadyStream (§5): a slow but steady connection whose bytes come in 16 KiB
// pieces 6 s apart (TLS records at about 2.7 KB/s: a read sees nothing between whole
// records) is not taken for stalled after 5 s. Its mid-stream timeout is twice the
// longest wait for its bytes, the first one's counted from the headers, so one
// request serves the file.
func TestSlowSteadyStream(t *testing.T) {
	t.Parallel()
	fakeTest(t, func(t *testing.T, dir string) {
		const rec = 16 << 10
		clk := NewFakeClock(time.Unix(90_000, 0))
		fac := &pacedFactory{clk: clk, size: 4 * rec, seed: 111,
			sent: func(el time.Duration) int64 { return rec * int64(el/(6*time.Second)) }}
		st := newMemStore()
		eng := New(st, fac, Options{Clock: clk, FlushInterval: time.Hour})
		d, err := eng.Add(context.Background(), model.Request{URL: "http://fake.test/slow.bin"}, dir, "", 1, 0)
		if err != nil {
			t.Fatal(err)
		}
		fac.drive(t, eng, d.ID, nil, nil, 600, nil)
		checkPacedFile(t, st, d.ID, fac)
		if reqs := fac.requests(); len(reqs) != 1 {
			var got []string
			for _, q := range reqs {
				got = append(got, fmt.Sprintf("%d- at %v", q.start, q.at.Sub(time.Unix(90_000, 0))))
			}
			t.Fatalf("%d requests (%s), want 1: the slow but steady connection was taken for stalled",
				len(reqs), strings.Join(got, ", "))
		}
	})
}

// TestStallGapPerResponse (§5): the longest wait for bytes counts per response. The
// connection's first response (requests are capped at 2 bytes) delivers a byte every
// 4 s, an 8 s mid-stream timeout; its next one delivers a byte at once and then nothing,
// and is cancelled 5 s after that byte, not 8 s.
func TestStallGapPerResponse(t *testing.T) {
	t.Parallel()
	fakeTest(t, func(t *testing.T, dir string) {
		clk := NewFakeClock(time.Unix(110_000, 0))
		fac := &silentFactory{after: 1, pacedFactory: &pacedFactory{clk: clk, size: 16, seed: 117,
			rate: func(_ int, start int64) float64 {
				if start == 0 {
					return 0.25 // the probe's response: a byte every 4 s
				}
				return 1e6
			}}}
		st := newMemStore()
		eng := New(st, fac, Options{Clock: clk, FlushInterval: time.Hour,
			HostRules: &capRules{caps: map[string]int64{"fake.test": 2}}})
		d, err := eng.Add(context.Background(), model.Request{URL: "http://fake.test/gap.bin"}, dir, "", 1, 0)
		if err != nil {
			t.Fatal(err)
		}
		fac.drive(t, eng, d.ID, nil, nil, 400, nil)
		checkPacedFile(t, st, d.ID, fac.pacedFactory)
		fac.mu.Lock()
		start, lastByte, cancelled := fac.start, fac.lastByte, fac.cancelled
		fac.mu.Unlock()
		if start != 2 || lastByte.IsZero() || cancelled.IsZero() {
			t.Fatalf("the silent request from %d: last byte at %v, cancelled at %v; want the connection's second request", start, lastByte, cancelled)
		}
		if gap := cancelled.Sub(lastByte); gap < 5*time.Second || gap > 6*time.Second {
			t.Errorf("the second response cancelled %v after its last byte, want 5 s: the first response's 4 s waits still counted", gap)
		}
	})
}

// wallShifted returns t, which carries a monotonic reading, with its wall clock reading
// moved by d (whole seconds) and its monotonic reading kept: time.Now after the system
// clock was set forward by d. No time.Time method does that (Add moves both readings),
// so it edits the packed wall field, whose bits 30–62 hold the seconds of a time with a
// monotonic reading. TestWallClockJump checks the result.
func wallShifted(t time.Time, d time.Duration) time.Time {
	type packed struct {
		wall uint64
		ext  int64
		loc  *time.Location
	}
	(*packed)(unsafe.Pointer(&t)).wall += uint64(d/time.Second) << 30
	return t
}

// jumpClock is a FakeClock whose times carry a monotonic reading, as time.Now's do, and
// whose wall clock reading is jump ahead from the fake time at: a system clock set
// forward (a time sync) while the monotonic clock goes on. Timers and tickers are the
// FakeClock's.
type jumpClock struct {
	*FakeClock
	start, at time.Time // fake times: the clock's start, the jump
	base      time.Time // time.Now() when created
	jump      time.Duration
}

func (c *jumpClock) Now() time.Time {
	n := c.FakeClock.Now()
	t := c.base.Add(n.Sub(c.start))
	if !n.Before(c.at) {
		t = wallShifted(t, c.jump)
	}
	return t
}

// TestWallClockJump (§5): the system clock set forward by more than MidStallTimeout
// while two connections receive stalls neither of them: idle times follow the
// monotonic clock, not the wall clock.
func TestWallClockJump(t *testing.T) {
	t.Parallel()
	now := time.Now()
	if j := wallShifted(now, time.Minute); j.Sub(now) != 0 || j.Round(0).Sub(now.Round(0)) != time.Minute {
		t.Fatalf("wallShifted moved the monotonic reading by %v and the wall clock by %v, want 0 and 1m: time.Time's layout changed",
			j.Sub(now), j.Round(0).Sub(now.Round(0)))
	}
	// now is taken outside the bubble: it carries the monotonic reading checked above.
	fakeTest(t, func(t *testing.T, dir string) {
		fake := NewFakeClock(time.Unix(100_000, 0))
		clk := &jumpClock{FakeClock: fake, start: fake.Now(), at: fake.Now().Add(3 * time.Second), base: now,
			jump: 10 * time.Second}
		// 1 MB/s per connection: both still receive at 3 s.
		fac := &pacedFactory{clk: fake, size: 16_000_000, seed: 113, rate: func(int, int64) float64 { return 1e6 }}
		st := newMemStore()
		eng := New(st, fac, Options{Clock: clk, FlushInterval: time.Hour})
		d, err := eng.Add(context.Background(), model.Request{URL: "http://fake.test/jump.bin"}, dir, "", 2, 0)
		if err != nil {
			t.Fatal(err)
		}
		fac.drive(t, eng, d.ID, nil, nil, 400, nil)
		checkPacedFile(t, st, d.ID, fac)
		for _, q := range fac.requests() {
			if el := q.at.Sub(clk.at); el >= 0 && el < 2*time.Second {
				t.Errorf("request %d-%d %v after the clock jump: a receiving connection was taken for stalled", q.start, q.end, el)
			}
		}
	})
}

// silentFactory is a pacedFactory whose second request (the first of the second
// connection) delivers after bytes and then nothing until it is cancelled: a connection
// that stops receiving mid-transfer while the other keeps receiving.
type silentFactory struct {
	*pacedFactory
	after   int64
	fetches atomic.Int64

	mu        sync.Mutex
	start     int64     // the silent request's first byte
	lastByte  time.Time // fake time of its last byte
	cancelled time.Time // fake time its body saw the request cancelled
}

func (f *silentFactory) NewSlot() transport.Fetcher {
	return silentFetcher{Fetcher: f.pacedFactory.NewSlot(), f: f}
}

func (f *silentFactory) note(at *time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	*at = f.clk.Now()
}

type silentFetcher struct {
	transport.Fetcher
	f *silentFactory
}

func (s silentFetcher) Fetch(ctx context.Context, rr transport.RangeRequest) (*transport.Response, error) {
	resp, err := s.Fetcher.Fetch(ctx, rr)
	if err != nil || s.f.fetches.Add(1) != 2 {
		return resp, err
	}
	s.f.mu.Lock()
	s.f.start = rr.Start
	s.f.mu.Unlock()
	resp.Body = &silentBody{inner: resp.Body.(*pacedBody), f: s.f}
	return resp, nil
}

type silentBody struct {
	inner *pacedBody
	f     *silentFactory
	got   int64
}

func (b *silentBody) Read(p []byte) (int, error) {
	if b.got < b.f.after {
		n, err := b.inner.Read(p[:min(int64(len(p)), b.f.after-b.got)])
		b.got += int64(n)
		if b.got == b.f.after {
			b.f.note(&b.f.lastByte)
			b.inner.finish() // nothing more is due: no longer a body with bytes left
		}
		return n, err
	}
	<-b.inner.ctx.Done()
	b.f.note(&b.f.cancelled)
	return 0, b.inner.ctx.Err()
}

func (b *silentBody) Close() error { return b.inner.Close() }

// TestMidTransferStall (§5): a connection that received bytes and then nothing is
// cancelled after MidStallTimeout (5 s), not the 20 s stall timeout, while the other
// connection is still busy (so no free worker could split its range), and the rest of
// its range goes to a new connection; the file is complete and exact.
func TestMidTransferStall(t *testing.T) {
	t.Parallel()
	fakeTest(t, func(t *testing.T, dir string) {
		clk := NewFakeClock(time.Unix(80_000, 0))
		// 1 MB/s per connection: the probe's share keeps it busy until about 8.5 s.
		fac := &silentFactory{after: 64 << 10, pacedFactory: &pacedFactory{clk: clk, size: 16_000_000, seed: 101,
			rate: func(int, int64) float64 { return 1e6 }}}
		st := newMemStore()
		eng := New(st, fac, Options{Clock: clk, FlushInterval: time.Hour})
		d, err := eng.Add(context.Background(), model.Request{URL: "http://fake.test/mid.bin"}, dir, "", 2, 0)
		if err != nil {
			t.Fatal(err)
		}
		fac.drive(t, eng, d.ID, nil, nil, 400, nil)
		checkPacedFile(t, st, d.ID, fac.pacedFactory)

		fac.mu.Lock()
		pos, lastByte, cancelled := fac.start+fac.after, fac.lastByte, fac.cancelled
		fac.mu.Unlock()
		if lastByte.IsZero() || cancelled.IsZero() {
			t.Fatalf("the silent request's last byte at %v, cancelled at %v", lastByte, cancelled)
		}
		if gap := cancelled.Sub(lastByte); gap < 5*time.Second || gap > 6*time.Second {
			t.Errorf("silent connection cancelled %v after its last byte, want 5 s (MidStallTimeout), not the 20 s stall timeout", gap)
		}
		var again []pacedReq
		for _, q := range fac.requests() {
			if q.start == pos {
				again = append(again, q)
			}
		}
		if len(again) != 1 {
			t.Fatalf("%d requests from the silent connection's position %d, want 1: its rest reassigned", len(again), pos)
		}
		if gap := again[0].at.Sub(lastByte); gap < 5*time.Second || gap > 6*time.Second {
			t.Errorf("its rest requested again %v after its last byte, want 5 s", gap)
		}
	})
}
