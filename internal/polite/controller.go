package polite

import (
	"errors"
	"net/netip"
	"time"
)

// Controller is Polite mode's decision logic (docs/POLITE-SPEC.md §2-§4): the anchors,
// the byte meter and the state machine. It is pure: it never reads a clock and starts no
// goroutine; every method takes the current time. It is not safe for concurrent use (the
// Monitor serialises it).
type Controller struct {
	target  time.Duration
	enabled bool
	anchors anchors
	meter   meter
	peak    peak

	state State
	since time.Time
	r     int64 // the cap; 0 = none
	floor int64 // floorEff: Floor, raised by the starvation guard

	lastTick    time.Time // zero: no gap check (just started, or coming back from idle)
	settleUntil time.Time // decisions wait for samples sent after it (rule 8)
	dropBefore  time.Time // samples sent before it are ignored (a tick gap, rule 20)
	est         estimate  // the latest estimate
	shown       estimate  // the latest known estimate (status)

	overTicks      int       // consecutive ticks with d over the target
	unknownSince   time.Time // d unknown (outside a settle) while the line is not quiet
	untrustedSince time.Time // no trusted live anchor while the line is not quiet
	freeSince      time.Time // the cap has not bound since
	floorSince     time.Time // the cap has been at the floor since

	// Calibration (rule 10, MEASURING).
	calStart    time.Time
	calPrev     State
	calPrevR    int64
	calBases    map[netip.Addr]time.Duration
	calSeen     map[netip.Addr]bool // the anchors selected at the end of the last calibration
	lastCal     time.Time
	calSpacing  time.Duration
	calOnEnable bool // switched on while downloads run: calibrate once X2 is known

	// Cut history while SLOWED: the d of the latest cut of the current series (0 = none)
	// and the rate that last overloaded the line (0 = none).
	cutD  time.Duration
	rHigh int64

	// Release backoff (rule 10).
	releaseHold time.Duration
	lastRelease time.Time
}

// Decision is the outcome of a tick: the cap to apply (0 = none) and the status.
type Decision struct {
	Rate   int64
	Status Status
}

// Probe is one probe to send: ICMP echo to Addr's address, else a TCP connect to Addr.
type Probe struct {
	Addr netip.AddrPort
	ICMP bool
}

// NewController returns a switched-off controller with the given target (0 or less =
// DefaultTarget).
func NewController(target time.Duration) *Controller {
	c := &Controller{anchors: newAnchors(), floor: Floor, releaseHold: ReleaseHoldMin, calSpacing: CalibrateSpacing}
	c.SetTarget(target)
	c.state = Off
	return c
}

// AllowLoopback makes loopback addresses eligible anchors (end-to-end tests only).
func (c *Controller) AllowLoopback(on bool) { c.anchors.loopback = on }

// SetTarget changes the target extra delay (0 or less = DefaultTarget).
func (c *Controller) SetTarget(t time.Duration) {
	if t <= 0 {
		t = DefaultTarget
	}
	c.target = t
}

// Target returns the target extra delay.
func (c *Controller) Target() time.Duration { return c.target }

// State returns the current state.
func (c *Controller) State() State { return c.state }

// Enable switches the controller on or off at now. rx is the byte counter and busy
// whether downloads run: switched on while they do, the controller calibrates once it
// has measured their rate (rule 10, MEASURING). Switched off, the cap is removed at once
// and nothing is probed; the anchors' bases are kept.
func (c *Controller) Enable(now time.Time, on bool, rx int64, busy bool) {
	if on == c.enabled {
		return
	}
	c.enabled = on
	c.resetCounters()
	c.leaveSlowed()
	c.settleUntil = time.Time{}
	c.lastTick = time.Time{}
	if !on {
		c.calOnEnable = false
		c.setState(Off, now)
		return
	}
	c.meter.reset(now, rx, !busy)
	c.calOnEnable = busy
	if busy {
		c.lastTick = now
		c.setState(Watching, now)
	} else {
		c.setState(Idle, now)
	}
}

// SetPeers updates the anchors from the live peers (rule 3).
func (c *Controller) SetPeers(now time.Time, peers []Peer) { c.anchors.update(now, peers) }

// probing reports whether probes may be sent: on, and downloads run.
func (c *Controller) probing() bool {
	return c.enabled && c.state != Off && c.state != Idle && c.state != Unavailable
}

// NextProbe returns the probe to send at now, if any (rule 4); rx is the byte counter
// now. The caller sends it and reports the result with ProbeDone.
func (c *Controller) NextProbe(now time.Time, rx int64) (Probe, bool) {
	if !c.probing() {
		return Probe{}, false
	}
	a, icmp, ok := c.anchors.next(now)
	if !ok {
		return Probe{}, false
	}
	if !icmp {
		a.lastTCP = now
	}
	a.out = &outstanding{sent: now, icmp: icmp, quiet: c.quietAt(now, rx)}
	return Probe{Addr: netip.AddrPortFrom(a.addr, a.port), ICMP: icmp}, true
}

// unmeasured reports whether d is unknown where it is needed (rule 4): in NO_SIGNAL, or
// at the latest tick on a loaded line outside a settle (unknownSince runs).
func (c *Controller) unmeasured() bool {
	return c.state == NoSignal || !c.unknownSince.IsZero()
}

// quietAt reports whether the line counts as quiet at now (trust rule 7a): Downex
// receives under Quiet, or the trusted anchors measured at most half the target at the
// latest tick (the cap holds the line quiet).
func (c *Controller) quietAt(now time.Time, rx int64) bool {
	if c.est.known && c.est.d <= c.target/2 && (c.state == Watching || c.state == Slowed) {
		return true
	}
	return c.meter.quietAt(now, rx)
}

// ProbeDone records the result of probe p: its round trip, or err (ErrNoReply,
// ErrUnreachable, ErrUnsupported or any other failure, which give no sample). A refused
// TCP connection is a sample: the prober reports it without an error.
func (c *Controller) ProbeDone(p Probe, rtt time.Duration, err error) {
	a := c.anchors.m[p.Addr.Addr()]
	if a == nil || a.out == nil || a.out.icmp != p.ICMP {
		return
	}
	o := a.out
	a.out = nil
	if o.icmp {
		switch {
		case err == nil:
			a.icmpFails, a.answered = 0, true
		case errors.Is(err, ErrUnsupported):
			c.anchors.icmpOff = true // this system sends no ICMP: every anchor uses TCP
			return
		default:
			a.icmpFails++
			if a.icmpFails >= ICMPFailuresToTCP {
				switch {
				case !a.answered:
					a.tcp, a.tcpSince = true, o.sent
				case !a.lossy && c.unmeasured():
					// It answered before, so it keeps its echoes (a congested line drops
					// them too), but a server that rate-limits ICMP answers too rarely to
					// give a d: TCP probes go between them (rule 4).
					a.lossy, a.lossySince = true, o.sent
				}
			}
			return
		}
	} else if err != nil {
		return
	}
	if o.sent.Before(c.dropBefore) || rtt <= 0 {
		return
	}
	// Rule 7: an anchor is trusted when its very first sample was sent while the line was
	// quiet (a), or when it answers at least CalibrateTrust into a calibration (b).
	first := !a.sampled
	a.add(sample{sent: o.sent, rtt: rtt, tcp: !o.icmp})
	a.lastReply = o.sent
	if first && o.quiet {
		a.trusted = true
	}
	if c.state == Measuring && !o.sent.Before(c.calStart.Add(CalibrateTrust)) {
		a.trusted = true
		// The calibration may end once the RTT has stopped falling: this sample set no
		// new low by BaseMoved or more.
		a.calFalling = a.calCount > 0 && rtt <= a.calMin-BaseMoved
		if a.calCount == 0 || rtt < a.calMin {
			a.calMin = rtt
		}
		a.calCount++
	}
}

// Step runs one tick at now with the byte counter rx and whether downloads run, and
// returns the cap and status (rules 9-11, 20).
func (c *Controller) Step(now time.Time, rx int64, busy bool) Decision {
	switch {
	case !c.enabled:
		return c.decision()
	case !busy:
		if c.state != Idle {
			c.resetCounters()
			c.leaveSlowed()
			c.calOnEnable = false
			c.setState(Idle, now)
		}
		c.lastTick = time.Time{}
		return c.decision()
	case c.state == Idle:
		// Downloads start: nothing was received while idle.
		c.meter.reset(now, rx, true)
		c.lastTick = now
		c.setState(Watching, now)
		return c.decision()
	}
	if !c.lastTick.IsZero() && now.Sub(c.lastTick) > GapReset {
		c.gap(now, rx)
	}
	c.lastTick = now
	c.meter.add(now, rx)
	c.est = c.anchors.estimate(now, maxTime(c.settleUntil, c.dropBefore))
	if c.est.known {
		c.shown = c.est
	}
	x2, x2ok := c.meter.rate(spanX2)
	x10, x10ok := c.meter.rate(spanX10)
	loaded := x2ok && x2 >= Quiet
	switch c.state {
	case Watching:
		c.stepWatching(now, x2, x2ok, x10, x10ok, loaded)
	case Slowed:
		c.stepSlowed(now, x10, x10ok)
	case Measuring:
		c.stepMeasuring(now)
	case NoSignal:
		c.stepNoSignal(now, loaded)
	}
	return c.decision()
}

// gap handles a tick gap over GapReset (sleep or resume, rule 20): the samples are
// dropped and the bases kept, the rates measured afresh, decisions wait EngageSettle, and
// a calibration in progress ends.
func (c *Controller) gap(now time.Time, rx int64) {
	c.anchors.dropSamples()
	c.dropBefore = now
	c.settleUntil = now.Add(EngageSettle)
	c.meter.reset(now, rx, false)
	c.resetCounters()
	if c.state == Measuring {
		c.endCalibration(now)
	}
}

func (c *Controller) stepWatching(now time.Time, x2 int64, x2ok bool, x10 int64, x10ok, loaded bool) {
	if x10ok {
		c.peak.record(now, x10)
	}
	if !c.lastRelease.IsZero() && now.Sub(c.lastRelease) >= ReleaseReset {
		c.releaseHold = ReleaseHoldMin
	}
	if c.calOnEnable && x2ok {
		c.calOnEnable = false
		if loaded && c.canCalibrate(now) {
			c.startCalibration(now)
			return
		}
	}
	if c.est.known && c.est.d > c.target {
		c.overTicks++
	} else {
		c.overTicks = 0
	}
	if c.overTicks >= EngageTicks && x2ok && x2 >= 2*Floor {
		c.engage(now, x2)
		return
	}
	if c.calibrateIfUntrusted(now, loaded) {
		return
	}
	if c.est.known || !loaded || now.Before(c.settleUntil) {
		c.unknownSince = time.Time{}
		return
	}
	if c.unknownSince.IsZero() {
		c.unknownSince = now
	}
	if now.Sub(c.unknownSince) >= NoSignalAfter {
		c.resetCounters()
		c.setState(NoSignal, now)
	}
}

// calibrateIfUntrusted starts a calibration after NoTrustedAfter without a trusted live
// anchor while the line is loaded, as far as the spacing allows (rule 10).
func (c *Controller) calibrateIfUntrusted(now time.Time, loaded bool) bool {
	if !loaded || c.anchors.trustedLive() {
		c.untrustedSince = time.Time{}
		return false
	}
	if c.untrustedSince.IsZero() {
		c.untrustedSince = now
	}
	if now.Sub(c.untrustedSince) < NoTrustedAfter || !c.canCalibrate(now) {
		return false
	}
	c.startCalibration(now)
	return true
}

// engage caps downloads at 0.75 of their rate (rule 10, WATCHING → SLOWED).
func (c *Controller) engage(now time.Time, x2 int64) {
	if !c.lastRelease.IsZero() && now.Sub(c.lastRelease) <= ReengageWithin {
		c.releaseHold = min(2*c.releaseHold, ReleaseHoldMax)
	}
	c.resetCounters()
	c.r = max(c.floor, int64(0.75*float64(x2)))
	c.cutD, c.rHigh = 0, x2
	c.settleUntil = now.Add(EngageSettle)
	c.setState(Slowed, now)
}

func (c *Controller) stepSlowed(now time.Time, x10 int64, x10ok bool) {
	t := c.target
	switch {
	case c.est.known && c.est.d > t:
		c.unknownSince = time.Time{}
		if c.cutD > 0 && c.est.d < c.cutD {
			// The queue has been draining since the last cut: the receive buffers lag the
			// cap by seconds, so wait for it instead of cutting again.
			c.cutD = c.est.d
			break
		}
		// Decrease: multiplicative, at most halving, from what Downex receives (a cap
		// above it would cut nothing). The window starts at settleUntil, so d here rests
		// on at least 2 samples sent after the last change.
		from := c.r
		if x2, ok := c.meter.rate(spanX2); ok && x2 < from {
			from = x2
		}
		if c.cutD == 0 {
			c.rHigh = from // the rate that overloaded the line: the next rise slows near it
		}
		f := max(0.5, 1-float64(c.est.d-t)/(CutGain*float64(t)))
		c.r = max(c.floor, int64(float64(from)*f))
		c.cutD = c.est.d
		c.settleUntil = now.Add(DecreaseSettle)
	case c.est.known:
		c.unknownSince = time.Time{}
		c.cutD = 0
		if !c.binding() {
			break // Downex does not use the cap: raising it would change nothing yet
		}
		// Increase: additive, in proportion to the room under the target; slowly near
		// the rate that last overloaded the line, fast again once past it; never above
		// Rmax and never lowering R.
		gain := max(Floor/4, c.r/IncreaseDiv)
		if c.rHigh > 0 {
			switch {
			case float64(c.r) > NearHigh*float64(c.rHigh):
				c.rHigh = 0 // past it without a new overload: the line has room again
			case float64(c.r) >= NearLow*float64(c.rHigh):
				gain = max(Floor/16, c.r/ProbeDiv)
			}
		}
		step := float64(gain) * float64(t-c.est.d) / float64(t)
		rmax := max(c.r, 4*Floor)
		if x10ok {
			rmax = max(int64(1.5*float64(x10)), 4*Floor)
		}
		c.r = max(c.r, min(rmax, c.r+int64(step)))
	case now.Before(c.settleUntil):
		// Settling: no samples to judge by yet; R is held.
	case c.calibrateIfUntrusted(now, true):
		return
	default:
		if c.unknownSince.IsZero() {
			c.unknownSince = now
		}
		if now.Sub(c.unknownSince) >= NoSignalAfter {
			c.leaveSlowed()
			c.resetCounters()
			c.setState(NoSignal, now) // fail-open
			return
		}
	}
	// Release once the cap has not bound for releaseHold and the line is quiet. The cap is
	// free when Downex receives well under it (X10 < 0.8·R), or when its rate is steady
	// under it (X0.5 within SteadyBand of X10, short of Binding·R): a download that wants
	// a little less than the cap (its server's speed; the engine's ramp-up stops under a
	// cap) is not raised against, so R would otherwise stay just above its rate and the
	// mode SLOWED for good.
	if x10ok && (float64(x10) < 0.8*float64(c.r) || c.steadyUnder(x10)) {
		if c.freeSince.IsZero() {
			c.freeSince = now
		}
	} else {
		c.freeSince = time.Time{}
	}
	if !c.freeSince.IsZero() && now.Sub(c.freeSince) >= c.releaseHold && c.est.known && c.est.d <= t/2 {
		c.leaveSlowed()
		c.lastRelease = now
		c.resetCounters()
		c.setState(Watching, now)
		return
	}
	// Starvation guard: 15 min near the floor (under StarveBelow; the base history needs a
	// low sample every 10 min, which lifts R for a few ticks, so "at" the floor never lasts)
	// raises the floor to a tenth of the line's capacity; it returns to Floor once R is
	// well above it.
	if c.r < StarveBelow {
		if c.floorSince.IsZero() {
			c.floorSince = now
		}
		if now.Sub(c.floorSince) >= StarveAfter {
			c.floor = max(Floor, c.peak.max(now)/10)
			c.r = max(c.r, c.floor)
		}
	} else {
		c.floorSince = time.Time{}
	}
	if c.floor > Floor && c.r >= 4*c.floor {
		c.floor = Floor
	}
}

func (c *Controller) stepNoSignal(now time.Time, loaded bool) {
	if c.est.known {
		c.resetCounters()
		c.setState(Watching, now)
		return
	}
	c.calibrateIfUntrusted(now, loaded)
}

// canCalibrate reports whether a calibration may start at now: some selected anchor
// answers probes, and it is spaced from the last one by CalibrateSpacing, or by
// CalibrateSpacingIdle after one that moved no base unless an untrusted selected anchor
// was not part of it (a new server gets its calibration).
func (c *Controller) canCalibrate(now time.Time) bool {
	if !c.anchors.answeredSince(now.Add(-AnsweredWithin)) {
		return false
	}
	if c.lastCal.IsZero() {
		return true
	}
	el := now.Sub(c.lastCal)
	if el < CalibrateSpacing {
		return false
	}
	if el >= c.calSpacing {
		return true
	}
	for _, a := range c.anchors.selected {
		if !a.trusted && !c.calSeen[a.addr] {
			return true
		}
	}
	return false
}

// startCalibration holds downloads at the floor to learn the line's base (MEASURING).
func (c *Controller) startCalibration(now time.Time) {
	c.calPrev, c.calPrevR = c.state, c.r
	c.calStart, c.lastCal = now, now
	c.calBases = c.anchors.bases(now)
	for _, a := range c.anchors.m {
		a.calCount, a.calFalling = 0, false
	}
	c.resetCounters()
	c.r = Floor
	c.setState(Measuring, now)
}

func (c *Controller) stepMeasuring(now time.Time) {
	el := now.Sub(c.calStart)
	if el >= CalibrateMax || (el >= CalibrateMin && c.calibrated()) {
		c.endCalibration(now)
	}
}

// calibrated reports whether every selected anchor has CalibrateNeeds samples sent at
// least CalibrateTrust into the calibration and its RTT stopped falling (the queue has
// drained).
func (c *Controller) calibrated() bool {
	if len(c.anchors.selected) == 0 {
		return false
	}
	for _, a := range c.anchors.selected {
		if a.calCount < CalibrateNeeds || a.calFalling {
			return false
		}
	}
	return true
}

// endCalibration resumes the state before the calibration with its cap, and spaces the
// next calibration by whether this one moved any base (rule 10).
func (c *Controller) endCalibration(now time.Time) {
	moved := false
	for ip, b := range c.anchors.bases(now) {
		if old, ok := c.calBases[ip]; !ok || old-b >= BaseMoved || b-old >= BaseMoved {
			moved = true
		}
	}
	c.calSpacing = CalibrateSpacing
	if !moved {
		c.calSpacing = CalibrateSpacingIdle
	}
	c.calBases = nil
	c.calSeen = map[netip.Addr]bool{}
	for _, a := range c.anchors.selected {
		c.calSeen[a.addr] = true
	}
	c.resetCounters()
	prev := c.calPrev
	if prev == Measuring || prev == 0 {
		prev = Watching
	}
	c.r = 0
	if prev == Slowed {
		c.r = max(c.floor, c.calPrevR)
		c.settleUntil = now.Add(EngageSettle)
	}
	c.setState(prev, now)
}

func (c *Controller) resetCounters() {
	c.overTicks = 0
	c.unknownSince, c.untrustedSince, c.freeSince, c.floorSince = time.Time{}, time.Time{}, time.Time{}, time.Time{}
}

func (c *Controller) setState(s State, now time.Time) {
	if s == c.state {
		return
	}
	c.state, c.since = s, now
	if s == Off || s == Idle || s == NoSignal {
		c.shown = estimate{}
	}
}

func (c *Controller) decision() Decision {
	st := c.Status()
	return Decision{Rate: st.RateLimit, Status: st}
}

// Status returns the current status.
func (c *Controller) Status() Status {
	st := Status{State: c.state, DelayMs: -1, BaseMs: -1, Since: c.since}
	if c.state == Slowed || c.state == Measuring {
		st.RateLimit = c.r
	}
	if c.shown.known && c.enabled && c.state != Idle {
		st.DelayMs, st.BaseMs = millis(c.shown.d), millis(c.shown.base)
	}
	return st
}

func maxTime(ts ...time.Time) time.Time {
	var out time.Time
	for _, t := range ts {
		if t.After(out) {
			out = t
		}
	}
	return out
}

// binding reports whether Downex uses the cap: it received at least Binding of R over the
// last tick.
func (c *Controller) binding() bool {
	x, ok := c.meter.rate(spanX05)
	return ok && float64(x) >= Binding*float64(c.r)
}

// steadyUnder reports whether Downex receives steadily under the cap: its rate over the
// last tick is within SteadyBand of its 10 s rate x10 (no lag still catching up with a
// raised cap) and the cap does not bind.
func (c *Controller) steadyUnder(x10 int64) bool {
	x, ok := c.meter.rate(spanX05)
	if !ok || x10 <= 0 || float64(x) >= Binding*float64(c.r) {
		return false
	}
	d := float64(x - x10)
	if d < 0 {
		d = -d
	}
	return d <= SteadyBand*float64(x10)
}

// leaveSlowed removes the cap and forgets the cut history.
func (c *Controller) leaveSlowed() {
	c.r, c.floor = 0, Floor
	c.cutD, c.rHigh = 0, 0
}

// emitter throttles what reaches the limiter and the subscribers (rule 11).
type emitter struct {
	applied   int64
	didApply  bool
	pub       Status
	pubAt     time.Time
	published bool
}

// apply reports whether rate r must be passed on: the first time, a change between none
// and a cap, or a change of at least ApplyChange.
func (e *emitter) apply(r int64) bool {
	if e.didApply && (r == 0) == (e.applied == 0) && !changed(e.applied, r, ApplyChange) {
		return false
	}
	e.applied, e.didApply = r, true
	return true
}

// publish reports whether st must be published at now: every state change, and while
// SLOWED a cap moved by PublishChange or more at least PublishGap after the last one.
func (e *emitter) publish(now time.Time, st Status) bool {
	switch {
	case !e.published, st.State != e.pub.State, !st.Since.Equal(e.pub.Since):
	case st.State == Slowed && changed(e.pub.RateLimit, st.RateLimit, PublishChange) && now.Sub(e.pubAt) >= PublishGap:
	default:
		return false
	}
	e.pub, e.pubAt, e.published = st, now, true
	return true
}

// changed reports whether b differs from a by at least the fraction f of a.
func changed(a, b int64, f float64) bool {
	if a == b {
		return false
	}
	if a == 0 {
		return true
	}
	d := float64(b - a)
	if d < 0 {
		d = -d
	}
	return d >= f*float64(a)
}
