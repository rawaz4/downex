package engine

import (
	"math"
	"time"
)

// Connection budget and ramp-up constants (docs/ENGINE-SPEC.md §3).
const (
	bytesPerConnection = 10_240 // budget = remaining / bytesPerConnection
	rampCapFraction    = 0.85   // no ramp-up once peak speed ≥ 85% of an active cap
)

// connBudget is clamp(remaining / 10,240, 1, perHostMax).
func connBudget(remaining int64, perHostMax int) int {
	perHostMax = max(perHostMax, 1)
	b := remaining / bytesPerConnection
	if b < 1 {
		return 1
	}
	if b > int64(perHostMax) {
		return perHostMax
	}
	return int(b)
}

// ramp tracks the active connection target. It starts at 1; a step raises it to the
// whole budget at once, only while every live worker has received bytes, and a step after
// the first comes at most once per interval (docs/ENGINE-SPEC.md §3). Under a speed cap
// a step only doubles the target, so that the 85% rule can stop it at the connections
// the cap needs; so does every step after the server pushed back (lower).
type ramp struct {
	target   int
	last     time.Time // time of the latest step; zero before the first
	interval time.Duration
	cautious bool // the server pushed back (lower): steps double
}

func newRamp(interval time.Duration) ramp {
	return ramp{target: 1, interval: interval}
}

// step raises the target (to budget, or to twice the target while capped or cautious)
// when allowed and reports whether it did.
func (r *ramp) step(now time.Time, budget int, allReceiving, enabled, capped bool) bool {
	if !enabled || !allReceiving || r.target >= budget {
		return false
	}
	if !r.last.IsZero() && now.Sub(r.last) < r.interval {
		return false
	}
	next := budget
	if capped || r.cautious {
		next = min(2*r.target, budget)
	}
	r.target, r.last = next, now
	return true
}

// lower clamps the target to limit after the server pushed back (429/503/421 or a
// refused connection, §10). When the limit rises again (the host penalty expired) the
// target grows back through step's gates, doubling, rather than all at once.
func (r *ramp) lower(limit int) {
	r.target = max(1, min(r.target, limit))
	r.cautious = true
}

// rampMinETA is the worth-it guard of ramp-up: no connection is added while the download
// would finish within max(rampMinETA, 4·RTT) at its current speed (docs/ENGINE-SPEC.md §3).
const rampMinETA = 500 * time.Millisecond

// rampWorthwhile reports whether another ramp-up step can still pay off: always while the
// speed is unknown (rateBps ≤ 0), else only while remaining/rateBps exceeds
// max(rampMinETA, 4·rtt). A new connection needs at least a round trip before its first
// byte, so a download about to finish gains nothing from it.
func rampWorthwhile(remaining int64, rateBps float64, rtt time.Duration) bool {
	if rateBps <= 0 {
		return true
	}
	return float64(remaining)/rateBps > max(rampMinETA, 4*rtt).Seconds()
}

// rampEnabled reports whether ramp-up may run: never in single mode, and not while a
// speed cap is active and the peak speed already reached 85% of it.
func rampEnabled(single bool, capBps int64, peakBps float64) bool {
	if single {
		return false
	}
	return capBps <= 0 || peakBps < rampCapFraction*float64(capBps)
}

// speedTau is the EWMA time constant of the speed that ramp-up, splitting and end-game
// stealing decide on.
const speedTau = time.Second

// speedMeter is an exponentially weighted moving average of throughput, sampled
// every tickInterval (docs/ENGINE-SPEC.md §9).
type speedMeter struct {
	last      time.Time
	lastBytes int64
	rate      float64 // bytes per second
	peak      float64
	primed    bool
}

func (m *speedMeter) reset(now time.Time, total int64) {
	*m = speedMeter{last: now, lastBytes: total}
}

// sample folds the bytes received since the previous sample into the average. The
// average starts with the first sample that saw bytes: until then a sample only moves
// the start on, so the time before the first bytes (a connect, a server's time to first
// byte) does not count as a slow start and the worth-it guard sees the real speed.
func (m *speedMeter) sample(now time.Time, total int64) {
	dt := now.Sub(m.last)
	if dt <= 0 {
		return
	}
	if !m.primed && total == m.lastBytes {
		m.last = now
		return
	}
	inst := float64(total-m.lastBytes) / dt.Seconds()
	if m.primed {
		alpha := 1 - math.Exp(-dt.Seconds()/speedTau.Seconds())
		m.rate += alpha * (inst - m.rate)
	} else {
		m.rate, m.primed = inst, true
	}
	m.last, m.lastBytes = now, total
	m.peak = max(m.peak, m.rate)
}

// The shown speed (docs/ENGINE-SPEC.md §9): what progress events report for a download
// and for each of its connections, so that every client shows (and derives its ETA
// from) the same steady value.
const (
	shownTau     = 5 * time.Second        // smoothing once bytes flow for 4·shownTau
	shownTauMin  = 200 * time.Millisecond // smoothing right after the first bytes
	shownWarmup  = 4                      // in between: time since the first bytes ÷ 4
	shownSilence = time.Second            // no bytes this long (uncapped) shows 0 ...
	shownGaps    = 2.5                    // ... or this many recent gaps between bytes ...
	shownMaxWait = 10 * time.Second       // ... at most
	shownMaxGap  = 4 * time.Second        // bytes after a longer silence show their own rate
	shownGapTau  = 2 * time.Second        // how long the recent gap between bytes is remembered

	// A real change is followed quickly: when a 0.5 s average (the detector) has differed
	// from the shown speed by more than shownBand for shownFollowAfter, the shown speed
	// moves with shownTauFollow until the two are within half the band again. The line's
	// own second-to-second wobble leaves the band only briefly and is smoothed over
	// shownTau.
	shownTauFast     = 500 * time.Millisecond
	shownBand        = 0.25
	shownFollowAfter = time.Second
	shownTauFollow   = time.Second
)

// shownMeter is the shown speed: an EWMA whose time constant grows from 200 ms at the
// first bytes to 5 s, so it follows a ramp-up as it happens and then holds steady through
// the line's own noise (2026-10-29: at 2 s the owner saw it wander 6–9 MiB/s on a line
// averaging 8; a reference download manager shows a steadier, 10% inflated number). A
// change that lasts (a 0.5 s average off by more than 25% for a second: a limit, a dip
// of the line, a recovery) is then followed within about a second. Bytes are folded in
// over the whole gap since the previous bytes, and the
// value holds in between, so a slow connection whose bytes come in records seconds
// apart shows its rate, not a saw-tooth. It shows 0 once no bytes came for its silence:
// max(the caller's floor, 2.5 recent gaps), at most 10 s. The first bytes, and the bytes
// after a stall (a silence over 4 s), show their real rate at once; bytes after a shorter
// silence show their rate over it.
type shownMeter struct {
	last      time.Time
	lastBytes int64
	rate      float64       // bytes per second
	since     time.Time     // first sample of the current run of bytes; zero while showing 0
	lastByte  time.Time     // latest sample that saw bytes; zero before the first
	gap       time.Duration // recent gap between samples with bytes: a max decaying over shownGapTau
	fast      float64       // the detector: a 0.5 s average of the same bytes
	off       time.Duration // how long the detector has been outside the band (held inside it)
}

func (m *shownMeter) reset(now time.Time, total int64) {
	*m = shownMeter{last: now, lastBytes: total}
}

// sample folds the bytes received since the previous sample in; floor is the shortest
// silence (a gap without bytes) after which the meter shows 0.
func (m *shownMeter) sample(now time.Time, total int64, floor time.Duration) {
	dt := now.Sub(m.last)
	if dt <= 0 {
		return
	}
	got := total - m.lastBytes
	m.last, m.lastBytes = now, total
	if got <= 0 {
		if !m.since.IsZero() && now.Sub(m.lastByte) >= m.silence(floor) {
			m.rate, m.since = 0, time.Time{}
		}
		return
	}
	gap := now.Sub(m.lastByte)
	switch {
	case m.lastByte.IsZero(): // the first bytes
		m.start(float64(got)/dt.Seconds(), now)
	case !m.since.IsZero(): // the run of bytes goes on
		m.noteGap(gap)
		inst := float64(got) / gap.Seconds()
		warm := max(shownTauMin, now.Sub(m.since)/shownWarmup)
		m.fast += ewmaStep(gap, min(shownTauFast, warm)) * (inst - m.fast)
		switch dev := math.Abs(m.fast-m.rate) / math.Max(m.rate, 1); {
		case dev > shownBand:
			m.off += gap
		case dev <= shownBand/2:
			m.off = 0
		}
		tau := min(shownTau, warm)
		if m.off >= shownFollowAfter {
			tau = min(tau, shownTauFollow)
		}
		m.rate += ewmaStep(gap, tau) * (inst - m.rate)
	case gap <= shownMaxGap: // after showing 0 for a short silence: a slow flow
		m.noteGap(gap)
		m.start(float64(got)/gap.Seconds(), now)
	default: // after a stall
		m.start(float64(got)/dt.Seconds(), now)
	}
	m.lastByte = now
}

// start begins a run of bytes at rate (bytes per second).
func (m *shownMeter) start(rate float64, now time.Time) {
	m.rate, m.fast, m.off, m.since = rate, rate, 0, now
}

// ewmaStep is the weight an EWMA with time constant tau gives a sample covering dt.
func ewmaStep(dt, tau time.Duration) float64 {
	return 1 - math.Exp(-dt.Seconds()/tau.Seconds())
}

// noteGap keeps the longest recent gap between bytes, forgetting it over shownGapTau.
func (m *shownMeter) noteGap(gap time.Duration) {
	decayed := time.Duration(float64(m.gap) * math.Exp(-gap.Seconds()/shownGapTau.Seconds()))
	m.gap = max(gap, decayed)
}

// silence is how long the meter waits without bytes before it shows 0.
func (m *shownMeter) silence(floor time.Duration) time.Duration {
	return max(floor, min(shownMaxWait, time.Duration(shownGaps*float64(m.gap))))
}

// shownSilenceFor is the shortest silence after which a shown speed shows 0 (the floor
// of shownMeter.silence): 1 s, or under a speed cap of capBps the gap its pacing explains
// for a meter fed by share connections (two capped read slices each).
func shownSilenceFor(capBps int64, share int) time.Duration {
	if capBps <= 0 {
		return shownSilence
	}
	gap := 2 * float64(sliceCapped) * float64(max(share, 1)) / float64(capBps)
	return max(shownSilence, time.Duration(min(gap, maxETA)*float64(time.Second)))
}
