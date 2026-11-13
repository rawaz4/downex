package engine

import (
	"context"
	"log/slog"
	"math"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"downex/internal/limiter"
	"downex/internal/model"
)

// TestSlowOwnerETA pins the conditions of §5 under which a receiving owner counts as
// slow but alive: receiving for at least 1 s, its measured speed below the given
// quarter of the average, and more than 1 s left at that speed.
func TestSlowOwnerETA(t *testing.T) {
	const below = 20_000.0
	for _, tc := range []struct {
		name    string
		rem     int64
		age     time.Duration
		primed  bool
		ownerBs float64
		below   float64
		want    time.Duration
	}{
		{"slow but alive", 100_000, time.Second, true, 10_000, below, 10 * time.Second},
		{"receiving for less than 1 s", 100_000, time.Second - time.Millisecond, true, 10_000, below, 0},
		{"no first byte yet", 100_000, 0, true, 10_000, below, 0},
		{"speed not measured yet", 100_000, time.Hour, false, 10_000, below, 0},
		{"no measured speed", 100_000, time.Hour, true, 0, below, 0},
		{"at a quarter of the average", 100_000, time.Hour, true, below, below, 0},
		{"just below a quarter of the average", 100_000, time.Hour, true, below - 1, below, 5_000_250 * time.Microsecond},
		{"stealing disabled", 100_000, time.Hour, true, 10_000, 0, 0},
		{"exactly 1 s left", 10_000, time.Hour, true, 10_000, below, 0},
		{"just over 1 s left", 10_001, time.Hour, true, 10_000, below, 1_000_100 * time.Microsecond},
	} {
		got := slowOwnerETA(tc.rem, tc.age, tc.primed, tc.ownerBs, tc.below)
		if d := got - tc.want; d < -time.Microsecond || d > time.Microsecond {
			t.Errorf("%s: slowOwnerETA = %v, want %v", tc.name, got, tc.want)
		}
	}
	if got := slowOwnerETA(math.MaxInt64, time.Hour, true, 1e-9, below); got <= 0 {
		t.Errorf("a huge ETA overflowed to %v", got)
	}
}

// TestStealBelow pins the speed under which an owner is slow: a quarter of the average
// live connection, and no stealing at all under an active speed cap or while the
// download's speed is unknown.
func TestStealBelow(t *testing.T) {
	for _, tc := range []struct {
		name string
		cap  int64
		rate float64
		live int
		want float64
	}{
		{"quarter of the average", 0, 800_000, 4, 50_000},
		{"one live connection", 0, 800_000, 1, 200_000},
		{"speed cap active", 1 << 20, 800_000, 4, 0},
		{"speed unknown", 0, 0, 4, 0},
		{"no live connection", 0, 800_000, 0, 0},
	} {
		if got := stealBelow(tc.cap, tc.rate, tc.live); got != tc.want {
			t.Errorf("%s: stealBelow(%d, %v, %d) = %v, want %v", tc.name, tc.cap, tc.rate, tc.live, got, tc.want)
		}
	}
}

// TestCanSteal pins which free workers may steal (§5): only one whose own measured
// speed is at least the speed under which an owner is slow, so the stealer is always
// faster than its victim.
func TestCanSteal(t *testing.T) {
	const below = 20_000.0
	for _, tc := range []struct {
		name   string
		primed bool
		bps    float64
		below  float64
		want   bool
	}{
		{"fast stealer", true, 160_000, below, true},
		{"at the slow bound", true, below, below, true},
		{"just below the slow bound", true, below - 1, below, false},
		{"slow stealer", true, 2_000, below, false},
		{"nothing received yet", true, 0, below, false},
		{"speed not measured yet", false, 160_000, below, false},
		{"stealing disabled", true, 160_000, 0, false},
	} {
		if got := canSteal(tc.primed, tc.bps, tc.below); got != tc.want {
			t.Errorf("%s: canSteal(%v, %v, %v) = %v, want %v", tc.name, tc.primed, tc.bps, tc.below, got, tc.want)
		}
	}
}

// stealLog records the end-game steals the coordinator logs ("split" with steal=true).
type stealLog struct {
	n      atomic.Int64
	mu     sync.Mutex
	starts []int64 // first offset of each stolen range
}

func (l *stealLog) Enabled(context.Context, slog.Level) bool { return true }
func (l *stealLog) WithAttrs([]slog.Attr) slog.Handler       { return l }
func (l *stealLog) WithGroup(string) slog.Handler            { return l }
func (l *stealLog) Handle(_ context.Context, r slog.Record) error {
	if r.Message != "split" {
		return nil
	}
	start, steal := int64(-1), false
	r.Attrs(func(a slog.Attr) bool {
		switch {
		case a.Key == "start" && a.Value.Kind() == slog.KindInt64:
			start = a.Value.Int64()
		case a.Key == "steal" && a.Value.Kind() == slog.KindBool:
			steal = a.Value.Bool()
		}
		return true
	})
	if steal {
		l.n.Add(1)
		l.mu.Lock()
		l.starts = append(l.starts, start)
		l.mu.Unlock()
	}
	return nil
}

// stolen returns the first offsets of the ranges stolen so far.
func (l *stealLog) stolen() []int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.starts)
}

// pacedResume is a paused download resumed on a pacedFactory under the fake clock.
type pacedResume struct {
	fac      *pacedFactory
	st       *memStore
	id       model.DownloadID
	t0       time.Time
	steals   *stealLog
	maxConns atomic.Int64 // the most connections a progress event reported
}

// took returns the fake time from the resumed run's start to the last body read that
// delivered bytes, on any connection (an owner whose range was split or stolen too): the
// transfer, not what closing and renaming the file then took.
func (r *pacedResume) took() time.Duration { return r.fac.lastByteAt().Sub(r.t0) }

// resumePaced stores a paused download in dir whose segments have the given lengths (in
// file order), resumes it with conns connections over a pacedFactory serving at rate
// (nil lim: no speed cap) and drives it to completion, calling each after every tick. It
// checks the output and that no more than conns connections and requests were live. It
// runs in a fakeTest bubble.
func resumePaced(t *testing.T, dir string, r *pacedResume, layout []int64, conns int, rate func(slot int, start int64) float64,
	lim limiter.Limiter, each func(step int)) {
	t.Helper()
	clk := NewFakeClock(time.Unix(40_000, 0))
	r.st, r.t0, r.steals = newMemStore(), clk.Now(), &stealLog{}
	segs := make([]model.Segment, 0, len(layout))
	var size int64
	for _, n := range layout {
		segs = append(segs, model.Segment{Start: size, End: size + n - 1})
		size += n
	}
	r.fac = &pacedFactory{clk: clk, size: size, seed: 83, rate: rate}
	eng := New(r.st, r.fac, Options{Clock: clk, StallTimeout: time.Hour, FlushInterval: time.Hour,
		Logger: slog.New(r.steals)})
	ctx := context.Background()
	d, err := eng.Add(ctx, model.Request{URL: "http://fake.test/resume.bin"}, dir, "resume.bin", conns, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(partPath(d.Dir, d.Filename), size); err != nil {
		t.Fatal(err)
	}
	d.Mode, d.Size, d.State, d.Validators = model.ModeRanged, size, model.StatePaused, pacedValidators
	if err := r.st.SaveCheckpoint(ctx, d, segs); err != nil {
		t.Fatal(err)
	}
	r.id = d.ID
	onProgress := func(p Progress) {
		for cur := r.maxConns.Load(); int64(p.Connections) > cur; cur = r.maxConns.Load() {
			if r.maxConns.CompareAndSwap(cur, int64(p.Connections)) {
				break
			}
		}
	}
	r.fac.drive(t, eng, d.ID, lim, onProgress, 600, each)
	checkPacedFile(t, r.st, d.ID, r.fac)
	_, maxBusy := r.fac.stats()
	if m := r.maxConns.Load(); m > int64(conns) || maxBusy > conns {
		t.Fatalf("%d connections, %d requests in flight at once; the limit is %d", m, maxBusy, conns)
	}
}

// The end-game scenario: a resumed layout of one slow segment and three fast ones,
// four connections. The slow segment (230,000 bytes) stays below the unforced split
// floor at these speeds (2·116,800+3), so only end-game stealing can split it while
// the fast connections are still around.
const (
	egSlowLen = 230_000
	egFastBps = 160_000
	egConns   = 4
)

type endgameCase struct {
	slowFirst bool    // the slow segment comes first (its owner starts at once), else last (at 0.6 s)
	slowBps   float64 // speed of the slow segment's first request
	fastLen   int64   // bytes of each fast segment
	capped    bool    // run under an active (but not throttling) speed cap
}

type endgameRun struct {
	pacedResume
	slowStart int64
}

// inSlow returns the requests that start inside the slow segment (not at its start)
// and the earliest of their times.
func (r *endgameRun) inSlow() (n int, first time.Time) {
	for _, q := range r.fac.requests() {
		if q.start > r.slowStart && q.start < r.slowStart+egSlowLen {
			if n == 0 || q.at.Before(first) {
				first = q.at
			}
			n++
		}
	}
	return n, first
}

// runEndgame stores the layout in dir, resumes it under the fake clock and drives it to
// completion, calling each after every tick. It checks the output and the connection
// bound of every case. It runs in a fakeTest bubble.
func runEndgame(t *testing.T, dir string, ec endgameCase, each func(r *endgameRun, step int)) *endgameRun {
	t.Helper()
	r := &endgameRun{}
	layout := []int64{ec.fastLen, ec.fastLen, ec.fastLen}
	if ec.slowFirst {
		layout = append([]int64{egSlowLen}, layout...)
	} else {
		r.slowStart = 3 * ec.fastLen
		layout = append(layout, egSlowLen)
	}
	var slowTaken atomic.Bool
	rate := func(_ int, start int64) float64 {
		if start == r.slowStart && slowTaken.CompareAndSwap(false, true) {
			return ec.slowBps
		}
		return egFastBps
	}
	var lim limiter.Limiter
	if ec.capped {
		cl := &countingLimiter{}
		cl.SetLimit(1 << 40) // a cap is active, far above what ramp-up's 85% rule looks at
		lim = cl
	}
	resumePaced(t, dir, &r.pacedResume, layout, egConns, rate, lim, func(step int) {
		if each != nil {
			each(r, step)
		}
	})
	n, _ := r.inSlow()
	t.Logf("last byte after %.1f s of fake time; %d steals, %d requests inside the slow segment",
		r.took().Seconds(), r.steals.n.Load(), n)
	return r
}

// TestEndgameStealsFromSlowOwner: once the fast connections are done, a free worker
// splits the segment of an owner that still receives, but at a trickle, and the file
// is complete and exact. No connection is added for it.
func TestEndgameStealsFromSlowOwner(t *testing.T) {
	t.Parallel()
	fakeTest(t, func(t *testing.T, dir string) {
		r := runEndgame(t, dir, endgameCase{slowFirst: true, slowBps: 10_240, fastLen: 200_000}, nil)
		if r.steals.n.Load() == 0 {
			t.Fatal("no end-game steal")
		}
		n, first := r.inSlow()
		if n == 0 {
			t.Fatal("no request inside the slow segment")
		}
		// The owner's first byte arrives in the first tick, so it has been receiving for
		// 1 s from 1.1 s on. The fast segments end at about 1.55 s; without stealing, only
		// the decay of the download's speed (after 3 s) would let an unforced split in.
		if at := first.Sub(r.t0); at < 1100*time.Millisecond || at > 2500*time.Millisecond {
			t.Fatalf("first request inside the slow segment at %v, want between 1.1 s and 2.5 s", at)
		}
		if slots, _ := r.fac.stats(); slots != egConns {
			t.Fatalf("%d connections opened, want %d: stealing never adds one", slots, egConns)
		}
	})
}

// TestEndgameNoStealFromYoungOwner: the fast segments end while the slow owner has
// been receiving for less than 1 s, so nothing is stolen from it then.
func TestEndgameNoStealFromYoungOwner(t *testing.T) {
	t.Parallel()
	fakeTest(t, func(t *testing.T, dir string) {
		runEndgame(t, dir, endgameCase{slowFirst: false, slowBps: 10_240, fastLen: 100_000}, func(r *endgameRun, step int) {
			// The slow owner starts at 0.6 s and has its first byte at 0.7 s; the fast
			// segments end by about 1.25 s. Check up to 1.5 s, before it is 1 s old.
			if step > 15 {
				return
			}
			if n := r.steals.n.Load(); n > 0 {
				t.Fatalf("step %d: %d steals from an owner receiving for less than 1 s", step, n)
			}
			if n, _ := r.inSlow(); n > 0 {
				t.Fatalf("step %d: %d requests inside the slow segment", step, n)
			}
		})
	})
}

// TestEndgameNoStealUnderSpeedCap: under an active speed cap a slow connection is the
// limiter's doing, so nothing is stolen.
func TestEndgameNoStealUnderSpeedCap(t *testing.T) {
	t.Parallel()
	fakeTest(t, func(t *testing.T, dir string) {
		r := runEndgame(t, dir, endgameCase{slowFirst: true, slowBps: 10_240, fastLen: 200_000, capped: true},
			func(r *endgameRun, step int) {
				if step <= 25 { // before the speed's decay lets an unforced split in
					if n, _ := r.inSlow(); n > 0 {
						t.Fatalf("step %d: %d requests inside the slow segment under a speed cap", step, n)
					}
				}
			})
		if n := r.steals.n.Load(); n > 0 {
			t.Fatalf("%d steals under an active speed cap", n)
		}
	})
}

// TestEndgameNoStealFromFairOwner: an owner at a quarter of the average connection's
// speed or faster is not slow, even with more than 1 s left.
func TestEndgameNoStealFromFairOwner(t *testing.T) {
	t.Parallel()
	fakeTest(t, func(t *testing.T, dir string) {
		r := runEndgame(t, dir, endgameCase{slowFirst: true, slowBps: 80_000, fastLen: 200_000}, nil)
		if n := r.steals.n.Load(); n > 0 {
			t.Fatalf("%d steals from an owner at a quarter of the average or faster", n)
		}
		if n, _ := r.inSlow(); n > 0 {
			t.Fatalf("%d requests inside the owner's segment", n)
		}
	})
}

// TestEndgameNoStealBySlowWorker: a free worker that is slow itself does not steal (§5).
// Four connections resume [200,000 | 200,000 | short | 200,000]. Connection 1, the
// owner of the first segment, runs at 12 kB/s; connection 3, the owner of the short
// segment, is slower still; the others run at 160 kB/s. When connection 3 finishes its
// segment, the first owner counts as slow, but connection 3 must not take half of its
// rest: it would hold that half longer than the owner, and once the fast connections'
// steals shrank it below 46,720 bytes nothing could take it back.
func TestEndgameNoStealBySlowWorker(t *testing.T) {
	t.Parallel()
	const ownerBps = 12_000
	for _, tc := range []struct {
		name     string
		slowBps  float64 // connection 3
		shortLen int64   // its segment
	}{
		{"2 kB/s", 2_000, 1_700},
		{"6 kB/s", 6_000, 6_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			bps := func(slot int) float64 {
				switch slot {
				case 1:
					return ownerBps
				case 3:
					return tc.slowBps
				}
				return egFastBps
			}
			fakeTest(t, func(t *testing.T, dir string) {
				var r pacedResume
				resumePaced(t, dir, &r, []int64{200_000, 200_000, tc.shortLen, 200_000}, egConns,
					func(slot int, _ int64) float64 { return bps(slot) }, nil, nil)
				stolen := r.steals.stolen()
				for _, q := range r.fac.requests() {
					if slices.Contains(stolen, q.start) && bps(q.slot) <= ownerBps {
						t.Errorf("connection %d at %.0f B/s stole [%d-%d] at %v from an owner at %d B/s",
							q.slot, bps(q.slot), q.start, q.end, q.at.Sub(r.t0), ownerBps)
					}
				}
				// Without any steal by connection 3 the last byte arrives after 4.0 s of fake
				// time (both cases). With canSteal's stealer check removed, connection 3 at
				// 6 kB/s steals [106,599-199,999] at 1.1 s and, though the fast connections
				// then split that half again, the last byte arrives after 5.9 s. took counts
				// every delivering read: before, it counted only reads that ended a body's
				// range, so the bytes of an owner whose range was split or stolen were missed
				// and both runs measured about 2.9 s, which no bound could tell apart.
				took := r.took()
				t.Logf("last byte after %.1f s of fake time; %d steals", took.Seconds(), len(stolen))
				if took > 5*time.Second {
					t.Errorf("last byte after %.1f s of fake time, want at most 5 s", took.Seconds())
				}
			})
		})
	}
}
