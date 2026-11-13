package engine

import (
	"context"
	"math"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"downex/internal/limiter"
	"downex/internal/model"
)

// slowSetupStore takes setup of fake time to commit a download as downloading, the
// time a real part file and record take while the probe's body keeps arriving.
type slowSetupStore struct {
	*memStore
	fac   *pacedFactory
	setup time.Duration
	once  sync.Once
}

func (s *slowSetupStore) SaveCheckpoint(ctx context.Context, d *model.Download, segs []model.Segment) error {
	if d.State == model.StateDownloading {
		s.once.Do(func() { s.fac.advance(s.setup) })
	}
	return s.memStore.SaveCheckpoint(ctx, d, segs)
}

// The shown speed (docs/ENGINE-SPEC.md §9): Progress.SpeedBps and each connection's
// WorkerView.SpeedBps are smoothed over about 3 s, follow a sustained change within a
// few seconds, show 0 within a second when bytes stop (later for a connection whose
// bytes come seconds apart) and once the download stops, and a real rate as soon as
// bytes flow. These tests drive one connection on the fake clock.

// ratePlan is a pacedFactory.sent function: rates[i] bytes per second during the i-th
// period after the fetch; with repeat the list repeats, else its last rate continues.
func ratePlan(period time.Duration, repeat bool, rates ...float64) func(time.Duration) int64 {
	return func(el time.Duration) int64 {
		var total float64
		for i := 0; el > 0; i++ {
			r := rates[min(i, len(rates)-1)]
			if repeat {
				r = rates[i%len(rates)]
			}
			d := min(el, period)
			total += r * d.Seconds()
			el -= d
		}
		return int64(total)
	}
}

// shownSample is one progress event at fake time t after the run started.
type shownSample struct {
	t time.Duration
	p Progress
}

// runShown downloads size bytes over one connection of a pacedFactory delivering sent
// and returns its progress events.
func runShown(t *testing.T, size int64, sent func(time.Duration) int64, lim limiter.Limiter) []shownSample {
	t.Helper()
	var out []shownSample
	fakeTest(t, func(t *testing.T, dir string) {
		clk := NewFakeClock(time.Unix(70_000, 0))
		fac := &pacedFactory{clk: clk, size: size, seed: 97, sent: sent}
		st := newMemStore()
		// No stall handling: a plan's silence (5 s in TestShownSpeedZeroOnStall) keeps its connection.
		eng := New(st, fac, Options{Clock: clk, StallTimeout: time.Hour, MidStallTimeout: time.Hour, FlushInterval: time.Hour})
		d, err := eng.Add(context.Background(), model.Request{URL: "http://fake.test/shown.bin"}, dir, "", 1, 0)
		if err != nil {
			t.Fatal(err)
		}
		t0 := clk.Now()
		var mu sync.Mutex
		fac.drive(t, eng, d.ID, lim, func(p Progress) {
			mu.Lock()
			defer mu.Unlock()
			out = append(out, shownSample{t: clk.Now().Sub(t0), p: p})
		}, 2000, nil)
		checkPacedFile(t, st, d.ID, fac)
	})
	return out
}

// running drops the events of the finished download (after its last byte).
func running(ss []shownSample) []shownSample {
	var out []shownSample
	for _, s := range ss {
		if s.p.Received < s.p.Size {
			out = append(out, s)
		}
	}
	return out
}

// workerSpeed is the speed of the download's one connection in p (−1 when not listed).
func workerSpeed(p Progress) float64 {
	for _, w := range p.Workers {
		if w.State == model.WorkerReceiving || w.State == model.WorkerRequesting || w.State == model.WorkerIdle {
			return w.SpeedBps
		}
	}
	return -1
}

func cv(xs []float64) (mean, coeff float64) {
	for _, x := range xs {
		mean += x
	}
	mean /= float64(len(xs))
	var v float64
	for _, x := range xs {
		v += (x - mean) * (x - mean)
	}
	return mean, math.Sqrt(v/float64(len(xs))) / mean
}

// TestProbeSpeedCountsFromHeaders: the probe's bytes that arrived while the download was
// set up (200 ms here) are read on the first tick; its speed counts from the headers, so
// it is the real 10 MB/s, not three times that (over the 100 ms since the coordinator
// started), and the worth-it guard opens connections for the 0.9 s that are left (at
// 30 MB/s it would see 0.3 s and never ramp up).
func TestProbeSpeedCountsFromHeaders(t *testing.T) {
	t.Parallel()
	run := func(t *testing.T, conns int) (fac *pacedFactory, events []Progress) {
		fakeTest(t, func(t *testing.T, dir string) {
			clk := NewFakeClock(time.Unix(72_000, 0))
			fac = &pacedFactory{clk: clk, size: 12_000_000, seed: 99, rate: func(int, int64) float64 { return 10e6 }}
			st := &slowSetupStore{memStore: newMemStore(), fac: fac, setup: 200 * time.Millisecond}
			eng := New(st, fac, Options{Clock: clk, StallTimeout: time.Hour, FlushInterval: time.Hour})
			d, err := eng.Add(context.Background(), model.Request{URL: "http://fake.test/setup.bin"}, dir, "", conns, 0)
			if err != nil {
				t.Fatal(err)
			}
			var log progressLog
			fac.drive(t, eng, d.ID, nil, log.on, 600, nil)
			checkPacedFile(t, st.memStore, d.ID, fac)
			events, _ = log.snapshot()
		})
		return fac, events
	}
	t.Run("speed", func(t *testing.T) {
		t.Parallel()
		_, events := run(t, 1)
		if len(events) < 2 || events[0].Received == 0 || events[0].Received == events[0].Size {
			t.Fatalf("events %+v", events)
		}
		if got := events[0].SpeedBps; math.Abs(got-10e6) > 0.5e6 {
			t.Errorf("first shown speed %.0f B/s, want 10 MB/s", got)
		}
	})
	t.Run("ramp-up", func(t *testing.T) {
		t.Parallel()
		fac, _ := run(t, 8)
		if slots, _ := fac.stats(); slots < 2 {
			t.Errorf("%d connection(s) for a download with 0.9 s left at the first tick, want ramp-up", slots)
		}
	})
}

// TestShownSpeedSmoothsLineNoise: a line that alternates between 1.3 and 0.7 MB/s every
// second (±30% around 1 MB/s, as connections sharing a real line do) is shown steady,
// for the download and for its connection. A 1 s average shows it with a CV near 0.09.
func TestShownSpeedSmoothsLineNoise(t *testing.T) {
	t.Parallel()
	ss := runShown(t, 24_000_000, ratePlan(time.Second, true, 1_300_000, 700_000), nil)
	var total, conn []float64
	for _, s := range running(ss) {
		if s.t >= 6*time.Second && s.t <= 22*time.Second {
			total = append(total, s.p.SpeedBps)
			conn = append(conn, workerSpeed(s.p))
		}
	}
	if len(total) < 40 {
		t.Fatalf("%d progress events in the steady window", len(total))
	}
	mean, c := cv(total)
	if math.Abs(mean-1e6) > 0.05e6 || c > 0.05 {
		t.Errorf("shown speed: mean %.0f B/s, CV %.3f; want 1 MB/s ±5%%, CV ≤ 0.05", mean, c)
	}
	mean, c = cv(conn)
	if math.Abs(mean-1e6) > 0.05e6 || c > 0.05 {
		t.Errorf("shown connection speed: mean %.0f B/s, CV %.3f; want 1 MB/s ±5%%, CV ≤ 0.05", mean, c)
	}
}

// TestShownSpeedHoldsThroughWobble: a line that wobbles ±20% around 1 MB/s for 2 s at a
// time (as the owner's did, 6 to 9 MiB/s around 8) is shown within ±6%, for the download
// and for its connection, once the smoothing has warmed up. A 2 s average swings ±9%.
func TestShownSpeedHoldsThroughWobble(t *testing.T) {
	t.Parallel()
	ss := runShown(t, 44_000_000, ratePlan(2*time.Second, true, 1_200_000, 800_000), nil)
	checked := 0
	for _, s := range running(ss) {
		if s.t < 22*time.Second || s.t > 40*time.Second {
			continue
		}
		checked++
		if got := s.p.SpeedBps; math.Abs(got-1e6) > 0.06e6 {
			t.Errorf("at %v: shown %.0f B/s, want 1 MB/s ±6%%", s.t, got)
		}
		if got := workerSpeed(s.p); math.Abs(got-1e6) > 0.06e6 {
			t.Errorf("at %v: connection shown %.0f B/s, want 1 MB/s ±6%%", s.t, got)
		}
	}
	if checked < 40 {
		t.Fatalf("only %d events checked", checked)
	}
}

// TestShownSpeedFollowsChange: a sustained change of the rate (1 → 0.5 → 1 MB/s, 8 s
// each) is shown within 25% 3 s after it and within 10% 5 s after it, so the smoothing
// does not hide a real dip or recovery for long (a 2 s average: 22% and 8%).
func TestShownSpeedFollowsChange(t *testing.T) {
	t.Parallel()
	ss := runShown(t, 20_000_000, ratePlan(8*time.Second, false, 1e6, 0.5e6, 1e6), nil)
	checked := 0
	for _, s := range running(ss) {
		var want, tol float64
		switch {
		case s.t >= 11*time.Second && s.t < 13*time.Second:
			want, tol = 0.5e6, 0.25
		case s.t >= 13*time.Second && s.t <= 16*time.Second:
			want, tol = 0.5e6, 0.10
		case s.t >= 19*time.Second && s.t < 21*time.Second:
			want, tol = 1e6, 0.25
		case s.t >= 21*time.Second:
			want, tol = 1e6, 0.10
		default:
			continue
		}
		checked++
		if got := s.p.SpeedBps; math.Abs(got-want) > tol*want {
			t.Errorf("at %v: shown %.0f B/s, want %.0f ±%.0f%%", s.t, got, want, tol*100)
		}
		if got := workerSpeed(s.p); math.Abs(got-want) > tol*want {
			t.Errorf("at %v: connection shown %.0f B/s, want %.0f ±%.0f%%", s.t, got, want, tol*100)
		}
	}
	if checked < 20 {
		t.Fatalf("only %d events checked", checked)
	}
}

// TestShownSpeedFollowsRampUp: while a download ramps up (here 0.25 → 1 MB/s within
// 0.75 s) the shown speed follows within half a second, so the smoothing does not make
// the start look slow (a 1 s average shows 0.88 MB/s 0.5 s after the ramp ended).
func TestShownSpeedFollowsRampUp(t *testing.T) {
	t.Parallel()
	ss := runShown(t, 6_000_000, ratePlan(250*time.Millisecond, false, 0.25e6, 0.5e6, 0.75e6, 1e6), nil)
	checked := 0
	for _, s := range running(ss) {
		if s.t < 1250*time.Millisecond || s.t > 4*time.Second {
			continue
		}
		checked++
		if got := s.p.SpeedBps; got < 0.92e6 || got > 1.02e6 {
			t.Errorf("at %v: shown %.0f B/s, want 1 MB/s (−8%%, +2%%)", s.t, got)
		}
	}
	if checked < 8 {
		t.Fatalf("only %d events checked", checked)
	}
}

// TestShownSpeedZeroOnStall: when bytes stop the speed shows 0 within a second (not a
// decaying average), and when they flow again it shows their real rate at once. Both
// are timed from the events that first show the bytes received before and after the
// stall, so a worker that counts its bytes a tick late (a loaded machine) moves neither:
// 0 from one progress event after the stall's first second, a rate from the event after
// the bytes resumed, and their full 1 MB/s within a second.
func TestShownSpeedZeroOnStall(t *testing.T) {
	t.Parallel() // fakeTest's bubble: a loaded machine no longer lets a worker count late
	ss := running(runShown(t, 10_000_000, ratePlan(5*time.Second, false, 1e6, 0, 1e6), nil))
	const plateau = 5_000_000 // received when the bytes stop
	stalledAt, resumedAt := -1, -1
	for i, s := range ss {
		if stalledAt < 0 && s.p.Received >= plateau {
			stalledAt = i
		}
		if resumedAt < 0 && s.p.Received > plateau {
			resumedAt = i
		}
	}
	if stalledAt < 0 || ss[stalledAt].p.Received != plateau || resumedAt < 0 {
		t.Fatalf("no events show the %d bytes received before the stall and the bytes after it", plateau)
	}
	both := func(p Progress) (float64, float64) { return p.SpeedBps, workerSpeed(p) }
	// 0 at most 1 s after the tick that counted the last byte, shown by the next event.
	zeroBy := ss[stalledAt].t + shownSilence + tickInterval + progressInterval
	stalled := 0
	for _, s := range ss[stalledAt:resumedAt] {
		if s.t < zeroBy {
			continue
		}
		stalled++
		if total, conn := both(s.p); total != 0 || conn > 0 {
			t.Errorf("at %v, %v after the event that showed the last byte: shown %.0f B/s, connection %.0f B/s; want 0",
				s.t, s.t-ss[stalledAt].t, total, conn)
		}
	}
	if stalled < 8 {
		t.Fatalf("%d events while stalled (from %v)", stalled, zeroBy)
	}
	full := false
	for i, s := range ss[resumedAt:] {
		total, conn := both(s.p)
		if i > 0 && (total <= 0 || conn <= 0) {
			t.Errorf("at %v, after the bytes resumed: shown %.0f B/s, connection %.0f B/s", s.t, total, conn)
		}
		if s.t <= ss[resumedAt].t+time.Second && total >= 0.8e6 && conn >= 0.8e6 {
			full = true
		}
	}
	if !full {
		t.Errorf("no event within 1 s after the bytes resumed at 1 MB/s shows at least 0.8 MB/s")
	}
}

// TestShownSpeedSlowRecords: a slow but steady connection (a 16 KiB TLS record every
// 1.5 s, about 10.9 KB/s) is shown at its rate once the gap between its records is
// known, and never as 0 between them; neither for the download nor for its connection.
func TestShownSpeedSlowRecords(t *testing.T) {
	t.Parallel() // fakeTest's bubble: a loaded machine no longer lets a worker count late
	const rec = 16 << 10
	const want = float64(rec) / 1.5
	sent := func(el time.Duration) int64 { return rec * (int64(el/(1500*time.Millisecond)) + 1) }
	checked := 0
	for _, s := range running(runShown(t, 12*rec, sent, nil)) {
		if s.t < 1700*time.Millisecond { // the second record shows the gap
			continue
		}
		checked++
		if got := s.p.SpeedBps; math.Abs(got-want) > 0.1*want {
			t.Errorf("at %v: shown %.0f B/s, want %.0f ±10%%", s.t, got, want)
		}
		if got := workerSpeed(s.p); math.Abs(got-want) > 0.1*want {
			t.Errorf("at %v: connection shown %.0f B/s, want %.0f ±10%%", s.t, got, want)
		}
	}
	if checked < 40 {
		t.Fatalf("only %d events checked", checked)
	}
}

// capOnly reports a speed cap without delaying anything: the pacing is the test's.
type capOnly struct{ bps int64 }

func (capOnly) WaitN(context.Context, int) error { return nil }
func (c capOnly) Limit() int64                   { return c.bps }
func (capOnly) SetLimit(int64)                   {}
func (c capOnly) Child(int64) limiter.Limiter    { return c }

// TestShownSpeedCapGaps: under a 16 KiB/s cap the connection receives a 32 KiB slice
// every 2 s; those gaps are the cap's doing and never show as 0.
func TestShownSpeedCapGaps(t *testing.T) {
	t.Parallel()
	const slice = 32 << 10
	sent := func(el time.Duration) int64 { return slice * (int64(el/(2*time.Second)) + 1) }
	ss := runShown(t, 11*slice, sent, capOnly{16 << 10})
	checked := 0
	for _, s := range running(ss) {
		if s.t < 300*time.Millisecond {
			continue
		}
		checked++
		if s.p.SpeedBps <= 0 {
			t.Errorf("at %v: shown 0 between the capped slices", s.t)
		}
	}
	if checked < 40 {
		t.Fatalf("only %d events checked", checked)
	}
}

// TestShownSpeedZeroWhenPaused: the event a paused download ends with shows no speed.
func TestShownSpeedZeroWhenPaused(t *testing.T) {
	t.Parallel()
	fakeTest(t, testShownSpeedZeroWhenPaused)
}

func testShownSpeedZeroWhenPaused(t *testing.T, dir string) {
	clk := NewFakeClock(time.Unix(71_000, 0))
	fac := &pacedFactory{clk: clk, size: 1 << 30, seed: 98, rate: func(int, int64) float64 { return 1e6 }}
	eng := New(newMemStore(), fac, Options{Clock: clk, StallTimeout: time.Hour, FlushInterval: time.Hour})
	d, err := eng.Add(context.Background(), model.Request{URL: "http://fake.test/pause.bin"}, dir, "", 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	var log progressLog
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- eng.Run(ctx, d.ID, nil, log.on) }()
	t.Cleanup(cancel) // after a failed check; Run's goroutine must end before the bubble does
	synctest.Wait()
	if len(fac.requests()) == 0 {
		t.Fatal("no request before the first tick")
	}
	for range 30 {
		fac.step()
	}
	if p, ok := log.last(); !ok || p.SpeedBps < 0.9e6 {
		t.Fatalf("speed %.0f while running (event %v)", p.SpeedBps, ok)
	}
	cancel()
	<-done // a Run that never returns is a deadlock the bubble reports with its stacks
	p, _ := log.last()
	if p.SpeedBps != 0 {
		t.Errorf("paused download shows %.0f B/s", p.SpeedBps)
	}
	for _, w := range p.Workers {
		if w.SpeedBps != 0 {
			t.Errorf("paused download's connection %d shows %.0f B/s", w.Slot, w.SpeedBps)
		}
	}
}
