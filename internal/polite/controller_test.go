package polite

import (
	"math"
	"net/netip"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

// line drives a Controller in virtual time: Downex receives rate bytes/s (capped by the
// controller's cap when capped is set), every probe is answered at once with rtt(addr)
// (0 = no reply), and the controller ticks every Tick.
type line struct {
	t      *testing.T
	c      *Controller
	now    time.Time
	rx     int64
	rate   int64
	capped bool
	busy   bool
	peers  []Peer
	rtt    func(a netip.Addr) time.Duration
	tcp    func(a netip.AddrPort) time.Duration // nil: like rtt
	last   Decision
	probes []Probe
	steps  int
}

func newLine(t *testing.T, anchors ...string) *line {
	l := &line{t: t, c: NewController(0), now: t0, busy: true}
	for i, a := range anchors {
		l.peers = append(l.peers, Peer{Addr: netip.MustParseAddrPort(a), Conns: 4 - i, First: t0})
	}
	l.rtt = func(netip.Addr) time.Duration { return 20 * time.Millisecond }
	l.c.Enable(l.now, true, 0, false)
	return l
}

// run advances virtual time by d in ProbeSpacing steps.
func (l *line) run(d time.Duration) {
	for end := l.now.Add(d); l.now.Before(end); {
		l.now = l.now.Add(ProbeSpacing)
		l.steps++
		rate := l.rate
		if l.capped && l.last.Rate > 0 {
			rate = min(rate, l.last.Rate)
		}
		l.rx += rate * int64(ProbeSpacing) / int64(time.Second)
		l.c.SetPeers(l.now, l.peers)
		if p, ok := l.c.NextProbe(l.now, l.rx); ok {
			l.probes = append(l.probes, p)
			var rtt time.Duration
			if p.ICMP || l.tcp == nil {
				rtt = l.rtt(p.Addr.Addr())
			} else {
				rtt = l.tcp(p.Addr)
			}
			if rtt > 0 {
				l.c.ProbeDone(p, rtt, nil)
			} else {
				l.c.ProbeDone(p, 0, ErrNoReply)
			}
		}
		if l.steps%2 == 0 {
			l.last = l.c.Step(l.now, l.rx, l.busy)
		}
	}
}

// until runs until cond holds, at most max; it fails the test otherwise.
func (l *line) until(max time.Duration, what string, cond func() bool) time.Duration {
	l.t.Helper()
	start := l.now
	for l.now.Sub(start) < max {
		l.run(Tick)
		if cond() {
			return l.now.Sub(start)
		}
	}
	l.t.Fatalf("%s: not within %v (state %v, R %d, d %+v)", what, max, l.c.state, l.c.r, l.c.est)
	return 0
}

func (l *line) state() State { return l.c.State() }

func (l *line) extra(d time.Duration) {
	l.rtt = func(netip.Addr) time.Duration { return 20*time.Millisecond + d }
}

// slowed brings l to SLOWED: a trusted anchor learned on a quiet line, then a loaded
// line with extra delay.
func slowed(t *testing.T, rate int64, extra time.Duration) *line {
	l := newLine(t, "203.0.113.10:443")
	l.run(2 * time.Second) // quiet: the anchor is trusted
	if !l.c.anchors.m[netip.MustParseAddr("203.0.113.10")].trusted {
		t.Fatal("the anchor of a quiet line is not trusted")
	}
	l.rate = rate
	l.extra(extra)
	l.until(10*time.Second, "engage", func() bool { return l.state() == Slowed })
	return l
}

func TestEngageNeedsTwoTicksAndLoad(t *testing.T) {
	l := newLine(t, "203.0.113.10:443")
	l.run(2 * time.Second)
	l.rate = 1 << 20
	l.extra(50 * time.Millisecond)
	// The 3 s minimum filter holds d down until the window holds only high samples; then
	// two ticks over the target engage.
	var overAt time.Time
	for l.state() == Watching && l.now.Sub(t0) < 20*time.Second {
		l.run(Tick)
		if l.c.est.known && l.c.est.d > l.c.target && overAt.IsZero() {
			overAt = l.now
			if l.state() != Watching {
				t.Fatal("engaged on the first tick over the target")
			}
		}
	}
	if l.state() != Slowed || l.now.Sub(overAt) != Tick {
		t.Fatalf("state %v %v after the first tick over", l.state(), l.now.Sub(overAt))
	}
	x2, _ := l.c.meter.rate(spanX2)
	if want := int64(0.75 * float64(x2)); l.c.r != want {
		t.Fatalf("R = %d, want 0.75·X2 = %d", l.c.r, want)
	}

	// A line delayed by other traffic while Downex receives little is not Downex's to fix.
	l = newLine(t, "203.0.113.10:443")
	l.run(2 * time.Second)
	l.rate = 2*Floor - 1024
	l.extra(80 * time.Millisecond)
	l.run(20 * time.Second)
	if l.state() != Watching {
		t.Fatalf("engaged below 2·Floor: %v", l.state())
	}
}

func TestDecreaseFormula(t *testing.T) {
	for _, tc := range []struct {
		over   float64 // d in targets
		factor float64
	}{{1.2, 0.95}, {2, 0.75}, {3, 0.5}, {5, 0.5}} {
		l := slowed(t, 4<<20, 60*time.Millisecond)
		r0 := l.c.r
		engaged := l.now
		l.extra(time.Duration(tc.over * float64(30*time.Millisecond)))
		// Nothing is decided during the engage settle.
		for l.now.Before(engaged.Add(EngageSettle)) {
			l.run(Tick)
			if l.c.r != r0 {
				t.Fatalf("over %.1f: R changed during the settle: %d → %d", tc.over, r0, l.c.r)
			}
		}
		l.until(2*time.Second, "first decrease", func() bool { return l.c.r != r0 })
		want := int64(float64(r0) * tc.factor)
		if diff := math.Abs(float64(l.c.r - want)); diff > 2 {
			t.Fatalf("over %.1f: R = %d, want %d (factor %.2f)", tc.over, l.c.r, want, tc.factor)
		}
	}
}

// After a decrease, the next one waits for 2 samples sent after DecreaseSettle: the 3 s
// filter does not judge the same old samples twice.
func TestNoSecondDecreaseWithoutFreshSamples(t *testing.T) {
	l := slowed(t, 4<<20, 100*time.Millisecond)
	r0 := l.c.r
	l.until(5*time.Second, "first decrease", func() bool { return l.c.r != r0 })
	first, at := l.c.r, l.now
	// Probes stop answering: no fresh samples, no decrease, R held.
	l.rtt = func(netip.Addr) time.Duration { return 0 }
	l.run(3 * time.Second)
	if l.c.r != first {
		t.Fatalf("decreased without fresh samples: %d → %d", first, l.c.r)
	}
	l.extra(100 * time.Millisecond)
	l.until(2*time.Second, "second decrease", func() bool { return l.c.r != first })
	if l.now.Sub(at) < DecreaseSettle+Tick {
		t.Fatalf("second decrease %v after the first", l.now.Sub(at))
	}
}

func TestIncreaseFormulaAndRmax(t *testing.T) {
	l := slowed(t, 256<<20, 60*time.Millisecond)
	l.capped = true
	l.extra(0)
	l.c.releaseHold = time.Hour // keep it SLOWED for these checks
	// step sets R (and the cap Downex received during the last tick) to r and runs one
	// tick: Downex used the whole cap, so it binds.
	step := func(r int64) int64 {
		l.c.r, l.last.Rate = r, r
		l.run(Tick)
		return l.c.r
	}
	l.run(EngageSettle + 10*time.Second)
	for range 20 { // X10 measures a capped rate of 8 MiB/s: Rmax is 12 MiB/s
		step(8 << 20)
	}
	r := int64(8 << 20)
	// d = 0, far from any overload: one full step of max(Floor/4, R/16).
	l.c.rHigh = 0
	if got, want := step(r), r+max(Floor/4, r/IncreaseDiv); got != want {
		t.Fatalf("R = %d after one tick at d = 0, want %d", got, want)
	}
	// d = T/2: half a step.
	l.extra(15 * time.Millisecond)
	l.run(FilterWindow + Tick) // the window forgets the lower samples
	l.c.rHigh = 0
	if got, want := step(r), r+max(Floor/4, r/IncreaseDiv)/2; abs(got-want) > 2 {
		t.Fatalf("R = %d after one tick at d = T/2, want %d", got, want)
	}
	// Near the rate that last overloaded the line: slow probing.
	l.extra(0)
	l.run(FilterWindow + Tick)
	l.c.rHigh = r
	if got, want := step(r), r+max(Floor/16, r/ProbeDiv); got != want {
		t.Fatalf("R = %d after one tick near the last overload, want %d", got, want)
	}
	// Past it: fast again, and the overload is forgotten.
	l.c.rHigh = r * 9 / 10
	if got, want := step(r), r+max(Floor/4, r/IncreaseDiv); got != want || l.c.rHigh != 0 {
		t.Fatalf("past the last overload: R %d, want %d, rHigh %d", got, want, l.c.rHigh)
	}
	// Rmax = max(1.5·X10, 4·Floor) bounds the increase.
	x10, _ := l.c.meter.rate(spanX10)
	from := int64(1.5*float64(x10)) - 1000
	got := step(from)
	x10, _ = l.c.meter.rate(spanX10) // as the tick measured it
	if rmax := int64(1.5 * float64(x10)); got != rmax || got >= from+from/IncreaseDiv {
		t.Fatalf("R = %d from %d, want it held at Rmax %d", got, from, rmax)
	}
	// A cap Downex does not use is not raised, and an increase never lowers R.
	l.rate = 100 << 10
	l.run(2 * time.Second)
	r4 := l.c.r
	l.run(20 * time.Second)
	if l.c.r != r4 {
		t.Fatalf("an unused cap changed: %d → %d", r4, l.c.r)
	}
}

// A cut starts from what Downex receives when the cap is above it, and a queue that is
// already draining since the last cut is waited for, not cut again.
func TestCutFromReceivedAndDrainHold(t *testing.T) {
	l := slowed(t, 4<<20, 90*time.Millisecond)
	l.run(EngageSettle)
	l.c.r = 16 << 20 // far above the 4 MiB/s Downex receives
	l.c.cutD = 0
	l.run(Tick)
	x2, _ := l.c.meter.rate(spanX2)
	if want := int64(float64(x2) * 0.5); abs(l.c.r-want) > 2 {
		t.Fatalf("R = %d, want the cut from X2 %d to %d", l.c.r, x2, want)
	}
	if l.c.rHigh != x2 {
		t.Fatalf("rHigh %d, want X2 %d", l.c.rHigh, x2)
	}
	// The delay falls but stays over the target: hold.
	r := l.c.r
	from := l.now
	l.rtt = func(netip.Addr) time.Duration {
		return 20*time.Millisecond + 90*time.Millisecond - l.now.Sub(from)/50 // 20 ms less per second
	}
	l.run(2 * time.Second)
	if l.c.r != r {
		t.Fatalf("cut again while draining: %d → %d", r, l.c.r)
	}
	// It rises again: cut.
	l.extra(120 * time.Millisecond)
	l.until(3*time.Second, "a cut on a rising delay", func() bool { return l.c.r < r })
	if l.c.rHigh != x2 {
		t.Fatal("rHigh moved within one series of cuts")
	}
}

func abs(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

func TestReleaseAndBackoff(t *testing.T) {
	l := slowed(t, 4<<20, 60*time.Millisecond)
	l.capped = true
	l.extra(0)
	// The other traffic stops and Downex needs less than the cap: released after the
	// hold, back to full speed.
	l.rate = 64 << 10
	took := l.until(30*time.Second, "release", func() bool { return l.state() == Watching })
	if took < ReleaseHoldMin || l.last.Rate != 0 {
		t.Fatalf("released after %v with rate %d", took, l.last.Rate)
	}
	released := l.now
	// Re-engaging within 30 s doubles the hold, up to 5 min.
	l.rate = 4 << 20
	l.extra(60 * time.Millisecond)
	l.until(10*time.Second, "re-engage", func() bool { return l.state() == Slowed })
	if l.now.Sub(released) > ReengageWithin || l.c.releaseHold != 2*ReleaseHoldMin {
		t.Fatalf("hold %v after a re-engage %v after the release", l.c.releaseHold, l.now.Sub(released))
	}
	for range 10 {
		l.c.lastRelease = l.now
		l.c.engage(l.now, 4<<20)
	}
	if l.c.releaseHold != ReleaseHoldMax {
		t.Fatalf("hold %v, want the 5 min ceiling", l.c.releaseHold)
	}
	// 5 min without an engage resets it.
	l.c.setState(Watching, l.now)
	l.c.r = 0
	l.rate = 64 << 10
	l.extra(0)
	l.run(ReleaseReset + Tick)
	if l.c.releaseHold != ReleaseHoldMin {
		t.Fatalf("hold %v after 5 min calm, want %v", l.c.releaseHold, ReleaseHoldMin)
	}
}

// R never goes below the floor nor to 0 while SLOWED, however laggy the line.
func TestFloor(t *testing.T) {
	l := slowed(t, 4<<20, 500*time.Millisecond)
	l.capped = true
	for range 120 {
		l.run(Tick)
		if l.state() == Slowed && (l.c.r < Floor || l.last.Rate < Floor) {
			t.Fatalf("R = %d (applied %d) below the floor", l.c.r, l.last.Rate)
		}
	}
	if l.c.r != Floor {
		t.Fatalf("R = %d after a minute of 500 ms extra delay, want the floor", l.c.r)
	}
}

func TestStarvationGuard(t *testing.T) {
	l := newLine(t, "203.0.113.10:443")
	l.run(2 * time.Second)
	// The line's capacity: 30 MB/s seen while WATCHING.
	l.rate = 30 << 20
	l.run(15 * time.Second)
	if c := l.c.peak.max(l.now); c < 29<<20 {
		t.Fatalf("capacity %d", c)
	}
	// Another program keeps the line laggy, with a short lull every 4 minutes (which keeps
	// the base: without any, a lasting queue reads as a path change after 10 minutes).
	busySince := l.now
	l.rtt = func(netip.Addr) time.Duration {
		if l.now.Sub(busySince)%(4*time.Minute) < time.Second {
			return 20 * time.Millisecond
		}
		return 520 * time.Millisecond
	}
	l.capped = true
	l.until(10*time.Second, "engage", func() bool { return l.state() == Slowed })
	l.until(30*time.Second, "the floor", func() bool { return l.c.r == Floor })
	low := l.now
	l.until(StarveAfter+time.Minute, "a raised floor", func() bool { return l.c.floor != Floor })
	if l.state() != Slowed || l.now.Sub(low) < StarveAfter-time.Minute {
		t.Fatalf("%v; floor raised %v after reaching it", l.state(), l.now.Sub(low))
	}
	if want := l.c.peak.max(l.now) / 10; l.c.floor != want || l.c.r < want {
		t.Fatalf("floor %d, R %d after 15 min near the floor, want C/10 = %d", l.c.floor, l.c.r, want)
	}
	for range 100 {
		l.run(Tick)
		if l.state() == Slowed && l.c.r < l.c.floor {
			t.Fatalf("R %d under the raised floor %d", l.c.r, l.c.floor)
		}
	}
	// It returns to Floor once the state leaves SLOWED.
	l.extra(0)
	l.rtt = func(netip.Addr) time.Duration { return 20 * time.Millisecond }
	l.rate = 32 << 10
	l.until(5*time.Minute, "release", func() bool { return l.state() == Watching })
	if l.c.floor != Floor {
		t.Fatalf("floor %d after the release", l.c.floor)
	}
}

func TestNoSignalFailOpen(t *testing.T) {
	l := slowed(t, 4<<20, 60*time.Millisecond)
	engaged := l.now
	l.rtt = func(netip.Addr) time.Duration { return 0 } // every probe lost
	took := l.until(20*time.Second, "NO_SIGNAL", func() bool { return l.state() == NoSignal })
	if l.last.Rate != 0 || l.c.r != 0 {
		t.Fatalf("NO_SIGNAL with R = %d (applied %d)", l.c.r, l.last.Rate)
	}
	// 10 s of unknown d after the 3 s engage settle.
	if el := l.now.Sub(engaged); el < NoSignalAfter || took > NoSignalAfter+EngageSettle+FilterWindow {
		t.Fatalf("NO_SIGNAL %v after the engage", el)
	}
	// Answers again: WATCHING.
	l.extra(0)
	l.until(5*time.Second, "WATCHING", func() bool { return l.state() == Watching })
}

func TestCalibration(t *testing.T) {
	// Downloads already loaded when the anchor appears: its first sample is not trusted.
	l := newLine(t)
	l.rate = 4 << 20
	l.run(3 * time.Second)
	l.peers = []Peer{{Addr: netip.MustParseAddrPort("203.0.113.10:443"), Conns: 8, First: l.now}}
	l.extra(80 * time.Millisecond)
	l.capped = true
	// No trusted live anchor for 5 s already (there was none): the calibration starts
	// once the new anchor answers.
	took := l.until(10*time.Second, "MEASURING", func() bool { return l.state() == Measuring })
	if took > NoTrustedAfter {
		t.Fatalf("calibrated %v after the anchor came", took)
	}
	if l.last.Rate != Floor {
		t.Fatalf("calibration rate %d, want the floor", l.last.Rate)
	}
	// At the floor the queue drains: the real base shows.
	l.extra(0)
	start := l.now
	l.until(CalibrateMax+Tick, "end of calibration", func() bool { return l.state() != Measuring })
	if el := l.now.Sub(start); el < CalibrateMin || el > CalibrateMax+Tick {
		t.Fatalf("calibration lasted %v", el)
	}
	a := l.c.anchors.m[netip.MustParseAddr("203.0.113.10")]
	if !a.trusted {
		t.Fatal("the anchor is not trusted after a calibration")
	}
	if b, _ := a.base(l.now); b != 20*time.Millisecond {
		t.Fatalf("base %v", b)
	}
	if l.state() != Watching || l.last.Rate != 0 {
		t.Fatalf("after the calibration: %v at %d", l.state(), l.last.Rate)
	}
	// The base moved by 80 ms: the next calibration may come after a minute.
	if l.c.calSpacing != CalibrateSpacing {
		t.Fatalf("spacing %v", l.c.calSpacing)
	}
	if l.c.canCalibrate(l.now.Add(CalibrateSpacing - time.Second)) {
		t.Fatal("a calibration within a minute of the last")
	}
	// A calibration that moves no base spaces the next by 10 minutes.
	l.c.startCalibration(l.now.Add(CalibrateSpacing))
	l.now = l.now.Add(CalibrateSpacing)
	l.until(CalibrateMax+Tick, "end of the second calibration", func() bool { return l.state() != Measuring })
	if l.c.calSpacing != CalibrateSpacingIdle {
		t.Fatalf("spacing %v after a calibration that moved nothing", l.c.calSpacing)
	}
}

// Switched on while downloads run: a calibration as soon as their rate is known.
func TestCalibrationOnEnable(t *testing.T) {
	l := &line{t: t, c: NewController(0), now: t0, busy: true, rate: 4 << 20}
	l.peers = []Peer{{Addr: netip.MustParseAddrPort("203.0.113.10:443"), Conns: 8, First: t0}}
	l.extra(60 * time.Millisecond)
	l.c.Enable(l.now, true, 0, true)
	if l.state() != Watching {
		t.Fatalf("state %v at enable while downloading", l.state())
	}
	took := l.until(5*time.Second, "MEASURING", func() bool { return l.state() == Measuring })
	if took > spanX2+Tick {
		t.Fatalf("calibrated %v after enable", took)
	}
}

func TestTrustRules(t *testing.T) {
	// 7a: the first sample sent on a quiet line.
	l := newLine(t, "203.0.113.10:443")
	l.run(time.Second)
	if !l.c.anchors.m[netip.MustParseAddr("203.0.113.10")].trusted {
		t.Fatal("7a: not trusted")
	}
	// Not on a loaded line, and later quiet samples do not change that (only the first
	// sample counts, or a calibration).
	l = newLine(t)
	l.rate = 1 << 20
	l.run(3 * time.Second)
	l.peers = []Peer{{Addr: netip.MustParseAddrPort("203.0.113.10:443"), Conns: 1, First: l.now}}
	l.run(Tick)
	a := l.c.anchors.m[netip.MustParseAddr("203.0.113.10")]
	if a.trusted {
		t.Fatal("7a: trusted though its first sample was sent under load")
	}
	l.rate = 0
	l.c.lastCal = l.now // no calibration now
	l.run(5 * time.Second)
	if a.trusted {
		t.Fatal("trusted by a later quiet sample")
	}
	// 7b: a sample sent at least 1 s into a calibration.
	l.c.lastCal = time.Time{}
	l.c.startCalibration(l.now)
	l.run(CalibrateTrust - ProbeSpacing)
	if a.trusted {
		t.Fatal("7b: trusted by a sample sent in the first second of a calibration")
	}
	l.run(2 * ProbeSpacing)
	if !a.trusted {
		t.Fatal("7b: not trusted")
	}
}

func TestLowerMedian(t *testing.T) {
	tab := newAnchors()
	add := func(ip string, base, cur time.Duration) {
		a := &anchor{addr: netip.MustParseAddr(ip), trusted: true}
		a.add(sample{sent: t0.Add(-time.Minute), rtt: base})
		a.add(sample{sent: t0.Add(-time.Second), rtt: cur})
		tab.m[a.addr] = a
	}
	add("203.0.113.1", 20*time.Millisecond, 60*time.Millisecond)  // +40
	add("203.0.113.2", 30*time.Millisecond, 40*time.Millisecond)  // +10
	add("203.0.113.3", 10*time.Millisecond, 100*time.Millisecond) // +90
	e := tab.estimate(t0, time.Time{})
	if !e.known || e.d != 40*time.Millisecond || e.base != 20*time.Millisecond {
		t.Fatalf("3 anchors: %+v, want the middle +40 ms", e)
	}
	delete(tab.m, netip.MustParseAddr("203.0.113.3"))
	if e = tab.estimate(t0, time.Time{}); e.d != 10*time.Millisecond {
		t.Fatalf("2 anchors: %+v, want the lower +10 ms", e)
	}
	// One sample in the window in total: unknown.
	delete(tab.m, netip.MustParseAddr("203.0.113.2"))
	if e = tab.estimate(t0, time.Time{}); e.known {
		t.Fatalf("1 sample: %+v", e)
	}
	// Untrusted anchors do not count.
	a := &anchor{addr: netip.MustParseAddr("203.0.113.9")}
	a.add(sample{sent: t0.Add(-2 * time.Second), rtt: time.Second})
	a.add(sample{sent: t0.Add(-time.Second), rtt: time.Second})
	tab.m[a.addr] = a
	if e = tab.estimate(t0, time.Time{}); e.known {
		t.Fatalf("an untrusted anchor counted: %+v", e)
	}
}

func TestTickGap(t *testing.T) {
	l := slowed(t, 4<<20, 60*time.Millisecond)
	r := l.c.r
	// Asleep for a minute.
	l.now = l.now.Add(time.Minute)
	a := l.c.anchors.m[netip.MustParseAddr("203.0.113.10")]
	base, _ := a.base(l.now.Add(-time.Minute))
	l.run(Tick)
	if l.state() != Slowed || l.c.r != r {
		t.Fatalf("after the gap: %v at %d, want SLOWED at %d", l.state(), l.c.r, r)
	}
	if len(a.samples) > 2 {
		t.Fatalf("%d samples kept across the gap", len(a.samples))
	}
	if b, ok := a.base(l.now); !ok || b != base {
		t.Fatalf("base %v %v after the gap, want %v kept", b, ok, base)
	}
	if !l.c.settleUntil.Equal(l.now.Add(-Tick).Add(EngageSettle)) && !l.c.settleUntil.After(l.now) {
		t.Fatalf("no settle after the gap: %v", l.c.settleUntil.Sub(l.now))
	}
}

func TestIdleAndOff(t *testing.T) {
	l := slowed(t, 4<<20, 60*time.Millisecond)
	l.busy = false
	l.run(Tick)
	if l.state() != Idle || l.last.Rate != 0 {
		t.Fatalf("not busy: %v at %d", l.state(), l.last.Rate)
	}
	if _, ok := l.c.NextProbe(l.now, l.rx); ok {
		t.Fatal("a probe while idle")
	}
	l.busy = true
	l.run(Tick)
	if l.state() != Watching {
		t.Fatalf("downloads again: %v", l.state())
	}
	l.c.Enable(l.now, false, l.rx, true)
	if l.state() != Off || l.c.Status().RateLimit != 0 {
		t.Fatalf("off: %+v", l.c.Status())
	}
	if _, ok := l.c.NextProbe(l.now, l.rx); ok {
		t.Fatal("a probe while off")
	}
}

func TestEmitter(t *testing.T) {
	var e emitter
	for i, tc := range []struct {
		r    int64
		want bool
	}{{0, true}, {0, false}, {1000, true}, {1010, false}, {1019, false}, {1020, true}, {1000, false}, {999, true}, {0, true}} {
		if got := e.apply(tc.r); got != tc.want {
			t.Fatalf("step %d: apply(%d) = %v, want %v", i, tc.r, got, tc.want)
		}
	}
	var p emitter
	st := Status{State: Watching, Since: t0}
	if !p.publish(t0, st) || p.publish(t0, st) {
		t.Fatal("first publish, then nothing")
	}
	st = Status{State: Slowed, RateLimit: 1000, Since: t0.Add(time.Second)}
	if !p.publish(t0.Add(time.Second), st) {
		t.Fatal("state change not published")
	}
	st.RateLimit = 1200
	if p.publish(t0.Add(1500*time.Millisecond), st) {
		t.Fatal("published within a second")
	}
	if !p.publish(t0.Add(2*time.Second), st) {
		t.Fatal("a 20 % move after a second not published")
	}
	st.RateLimit = 1250
	if p.publish(t0.Add(5*time.Second), st) {
		t.Fatal("a 4 % move published")
	}
}

func TestICMPFallbackToTCP(t *testing.T) {
	l := newLine(t, "203.0.113.10:8443")
	l.rtt = func(netip.Addr) time.Duration { return 0 } // ICMP filtered
	l.tcp = func(netip.AddrPort) time.Duration { return 25 * time.Millisecond }
	l.run(2 * time.Second)
	a := l.c.anchors.m[netip.MustParseAddr("203.0.113.10")]
	if !a.tcp {
		t.Fatalf("still ICMP after %d probes", len(l.probes))
	}
	icmp := 0
	for _, p := range l.probes {
		if p.ICMP {
			icmp++
		}
	}
	if icmp != ICMPFailuresToTCP {
		t.Fatalf("%d ICMP probes before TCP, want %d", icmp, ICMPFailuresToTCP)
	}
	// TCP probes go to the peer's port, at most one per TCPProbeSpacing.
	l.probes = nil
	l.run(time.Minute)
	var tcp []Probe
	for _, p := range l.probes {
		if !p.ICMP {
			tcp = append(tcp, p)
		}
	}
	if len(tcp) > int(time.Minute/TCPProbeSpacing)+1 || len(tcp) < int(time.Minute/TCPProbeSpacing)-1 {
		t.Fatalf("%d TCP probes in a minute", len(tcp))
	}
	if tcp[0].Addr != netip.MustParseAddrPort("203.0.113.10:8443") {
		t.Fatalf("TCP probe to %v", tcp[0].Addr)
	}
	if len(a.samples) == 0 {
		t.Fatal("TCP probes gave no samples")
	}
	// ICMP is tried again after 5 minutes.
	l.probes = nil
	l.run(ICMPRetryAfter)
	sawICMP := false
	for _, p := range l.probes {
		sawICMP = sawICMP || p.ICMP
	}
	if !sawICMP {
		t.Fatal("ICMP not tried again after 5 min")
	}
	// A penalized anchor gets no TCP probes.
	l.peers[0].Penalized = true
	l.run(10 * time.Second)
	l.probes = nil
	l.run(30 * time.Second)
	for _, p := range l.probes {
		if !p.ICMP {
			t.Fatalf("TCP probe %v to a penalized anchor", p.Addr)
		}
	}
	// An anchor that has answered ICMP before stays on ICMP through losses.
	l2 := newLine(t, "203.0.113.11:443")
	l2.run(time.Second)
	l2.rtt = func(netip.Addr) time.Duration { return 0 }
	l2.run(5 * time.Second)
	if l2.c.anchors.m[netip.MustParseAddr("203.0.113.11")].tcp {
		t.Fatal("an anchor that answered ICMP moved to TCP")
	}
}

// A server that does not answer ICMP (edgedl.me.gvt1.com, the owner's NO_SIGNAL) is
// measured with TCP connect probes, at most one every TCPProbeSpacing: d must still be
// known from them. With every sample in a FilterWindow shorter than that spacing, one
// such anchor never had the 2 samples d needs, so Polite mode went WATCHING → MEASURING →
// WATCHING → NO_SIGNAL and ran at full speed for the rest of the download.
func TestTCPOnlyAnchorMeasures(t *testing.T) {
	ip := netip.MustParseAddr("34.104.35.123")
	tcpLine := func() *line {
		l := newLine(t, "34.104.35.123:443")
		l.rtt = func(netip.Addr) time.Duration { return 0 } // ICMP filtered
		l.tcp = func(netip.AddrPort) time.Duration { return 80 * time.Millisecond }
		l.rate, l.capped = 2<<20, true // a download starts at once and loads the line
		return l
	}
	// The line is not laggy: WATCHING with d known, never NO_SIGNAL.
	l := tcpLine()
	known, ticks := 0, 0
	var states []State
	for range 80 {
		l.run(Tick)
		if n := len(states); n == 0 || states[n-1] != l.state() {
			states = append(states, l.state())
		}
		if l.now.Sub(t0) >= 15*time.Second { // after the 3 ICMP probes and the calibration
			ticks++
			if l.c.est.known {
				known++
			}
		}
	}
	if !l.c.anchors.m[ip].tcp {
		t.Fatal("the anchor is not on TCP probes")
	}
	for _, s := range states {
		if s == NoSignal || s == Slowed {
			t.Fatalf("states %v on a quiet line measured over TCP", states)
		}
	}
	if l.state() != Watching || known < ticks*3/4 {
		t.Fatalf("%v with d known on %d of %d ticks (states %v)", l.state(), known, ticks, states)
	}
	if st := l.c.Status(); st.DelayMs != 0 || st.BaseMs != 80 {
		t.Fatalf("status %+v, want line delay +0 ms over a base of 80 ms", st)
	}

	// The line gets laggy: SLOWED, and d stays known while the cap works.
	l = tcpLine()
	l.run(15 * time.Second)
	l.tcp = func(netip.AddrPort) time.Duration { return 140 * time.Millisecond }
	l.until(15*time.Second, "SLOWED over TCP probes", func() bool { return l.state() == Slowed })
	for range 60 {
		l.run(Tick)
		if l.state() != Slowed {
			t.Fatalf("%v while the line stays laggy (R %d, d %+v)", l.state(), l.c.r, l.c.est)
		}
	}
	// Each decrease waits for 2 TCP samples sent after it (about 4 s, against 1 s over
	// ICMP), so the cuts come slower but still come.
	if l.c.r > l.rate/4 {
		t.Fatalf("R %d after 30 s of +60 ms, want under a quarter of %d", l.c.r, l.rate)
	}
}

// TCP samples count over TCPFilterWindow, ICMP ones over FilterWindow (rule 8).
func TestTCPSampleWindow(t *testing.T) {
	ip := netip.MustParseAddr("34.104.35.123")
	// One TCP sample in the window (the older one only gives the base) is not enough: d
	// needs two.
	tab := newAnchors()
	a := &anchor{addr: ip, trusted: true}
	a.add(sample{sent: t0.Add(-time.Minute), rtt: 80 * time.Millisecond, tcp: true})
	a.add(sample{sent: t0.Add(-time.Second), rtt: 90 * time.Millisecond, tcp: true})
	tab.m[ip] = a
	if e := tab.estimate(t0, time.Time{}); e.known {
		t.Fatalf("1 TCP sample in the window: %+v", e)
	}
	// Two TCP samples a probe spacing apart are, as long as both are within TCPFilterWindow
	// and after the floor (a settle or a tick gap).
	a.add(sample{sent: t0.Add(-time.Second - TCPProbeSpacing), rtt: 100 * time.Millisecond, tcp: true})
	if e := tab.estimate(t0, time.Time{}); !e.known || e.d != 10*time.Millisecond {
		t.Fatalf("2 TCP samples in the window: %+v, want +10 ms", e)
	}
	if e := tab.estimate(t0, t0.Add(-2*time.Second)); e.known {
		t.Fatalf("a TCP sample before the floor counted: %+v", e)
	}
	// ICMP samples keep the FilterWindow.
	a.samples = a.samples[:0]
	a.add(sample{sent: t0.Add(-time.Second), rtt: 90 * time.Millisecond})
	a.add(sample{sent: t0.Add(-time.Second - TCPProbeSpacing), rtt: 100 * time.Millisecond})
	if e := tab.estimate(t0, time.Time{}); e.known {
		t.Fatalf("an ICMP sample older than FilterWindow counted: %+v", e)
	}
}

// A server that rate-limits ICMP (17.253.73.207, Apple's devstreaming-cdn.apple.com,
// answered about 1 echo in 5 on the owner's line, idle or loaded) answered once, so rule 4
// kept it on ICMP; its rare samples never put 2 in the FilterWindow and an HLS download
// from it went WATCHING → NO_SIGNAL 12 s in, at full speed from then on. Once such an
// anchor loses ICMPFailuresToTCP echoes in a row while d is unknown on a loaded line, TCP
// probes go between its echoes.
func TestLossyICMPAnchorMeasures(t *testing.T) {
	ip := netip.MustParseAddr("17.253.73.207")
	l := newLine(t, "17.253.73.207:443")
	echoes := 0
	l.rtt = func(netip.Addr) time.Duration { // 1 echo in 7 answered
		echoes++
		if echoes%7 == 1 {
			return 100 * time.Millisecond
		}
		return 0
	}
	l.tcp = func(netip.AddrPort) time.Duration { return 100 * time.Millisecond }
	l.rate, l.capped = 6<<20, true // an HLS download loads the line from the start
	known, ticks := 0, 0
	var states []State
	for range 120 {
		l.run(Tick)
		if n := len(states); n == 0 || states[n-1] != l.state() {
			states = append(states, l.state())
		}
		if l.now.Sub(t0) >= 15*time.Second {
			ticks++
			if l.c.est.known {
				known++
			}
		}
	}
	icmp, tcp := 0, 0
	for _, p := range l.probes {
		if p.ICMP {
			icmp++
		} else {
			tcp++
		}
	}
	if a := l.c.anchors.m[ip]; a.tcp || !a.lossy || tcp == 0 {
		t.Fatalf("anchor tcp %v lossy %v, %d TCP probes (states %v)", a.tcp, a.lossy, tcp, states)
	}
	// Its echoes keep their pace: TCP probes take at most one slot every TCPProbeSpacing.
	if max := int(l.now.Sub(t0)/TCPProbeSpacing) + 1; tcp > max || icmp < 3*tcp {
		t.Fatalf("%d echoes and %d TCP probes in %v", icmp, tcp, l.now.Sub(t0))
	}
	for _, s := range states {
		if s == NoSignal {
			t.Fatalf("states %v on a line measurable over TCP", states)
		}
	}
	if l.state() != Watching || known < ticks*3/4 {
		t.Fatalf("%v with d known on %d of %d ticks (states %v)", l.state(), known, ticks, states)
	}

	// While another anchor keeps d known, a lossy one gets no TCP probes: losses alone (a
	// congested line drops echoes too) never add them.
	l = newLine(t, "203.0.113.10:443", "17.253.73.207:443")
	echoes = 0
	l.rtt = func(a netip.Addr) time.Duration {
		if a == ip {
			echoes++
			if echoes%7 != 1 {
				return 0
			}
		}
		return 20 * time.Millisecond
	}
	l.run(2 * time.Second) // quiet: both anchors are trusted
	l.rate, l.capped = 6<<20, true
	for range 60 {
		l.run(Tick)
		if !l.c.est.known && l.now.Sub(t0) > 5*time.Second {
			t.Fatalf("d unknown with an anchor answering every probe (state %v)", l.state())
		}
	}
	for _, p := range l.probes {
		if !p.ICMP {
			t.Fatalf("TCP probe %v while d was known", p.Addr)
		}
	}
}

func TestICMPUnsupported(t *testing.T) {
	c := NewController(0)
	c.Enable(t0, true, 0, true)
	c.SetPeers(t0, []Peer{{Addr: netip.MustParseAddrPort("203.0.113.10:443"), Conns: 1, First: t0}})
	p, ok := c.NextProbe(t0, 0)
	if !ok || !p.ICMP {
		t.Fatalf("first probe %+v %v", p, ok)
	}
	c.ProbeDone(p, 0, ErrUnsupported)
	p, ok = c.NextProbe(t0.Add(ProbeSpacing), 0)
	if !ok || p.ICMP {
		t.Fatalf("after ErrUnsupported: %+v %v, want a TCP probe", p, ok)
	}
}

// A download that wants a little less than the cap (its server's speed; the engine's
// ramp-up stops under a cap) is released once the line is quiet: the cap is not raised
// while it does not bind, so without the steady test R stayed just above the download's
// rate, the cap never counted as free (X10 < 0.8·R) and the mode stayed SLOWED for good.
func TestReleaseWhenDemandBelowCap(t *testing.T) {
	for _, demand := range []int64{160 << 10, 512 << 10, 2 << 20, 8 << 20} {
		l := slowed(t, demand, 35*time.Millisecond) // a mild overload: R stays near demand
		l.capped = true
		l.extra(0)
		l.until(2*time.Minute, "release of a cap the download does not use", func() bool { return l.state() == Watching })
		if l.last.Rate != 0 {
			t.Fatalf("demand %d: released with rate %d", demand, l.last.Rate)
		}
	}
	// A download catching up with a raised cap is not steady: no release while it binds.
	l := slowed(t, 4<<20, 60*time.Millisecond)
	l.capped = true
	l.c.releaseHold = ReleaseHoldMin
	l.c.r, l.last.Rate = 1<<20, 1<<20
	l.run(20 * time.Second)
	l.extra(0)
	l.c.r, l.last.Rate = 2<<20, 2<<20 // a raised cap the download fills at once
	l.run(Tick)
	if l.c.steadyUnder(2 << 20) {
		t.Fatal("a download that fills its cap counts as steady under it")
	}
}
