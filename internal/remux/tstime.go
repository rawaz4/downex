package remux

import (
	"math"
	"math/bits"
)

// Timing rules of transport stream conversion (docs/VIDEO-DETECT-SPEC.md §4.5).
// Everything here is a pure function of its inputs, so that both passes of the
// conversion compute the same times.

const (
	// tsClock is the 90 kHz clock of PTS and DTS.
	tsClock = 90000
	// tsWrap is the period of 33-bit timestamps.
	tsWrap = 1 << 33
	// maxTSValue bounds unwrapped values: beyond it the unwrapper starts afresh, so
	// that no later arithmetic overflows.
	maxTSValue = 1 << 60
	// clampTicks is the largest step back of a video decode time (1 ms) that is
	// clamped to the previous value + 1 rather than stitched.
	clampTicks = tsClock / 1000
	// defaultFrameTicks is the duration of a video sample without a neighbour.
	defaultFrameTicks = 3000
	// aacFrame is the duration of an AAC frame in samples.
	aacFrame = 1024
	// aacTolerance is how far a frame may be from its expected time and still
	// follow the previous frame directly.
	aacTolerance = 512
	// maxCompositionTicks bounds |PTS − DTS| of a video sample (10 s); a larger
	// offset is taken as a missing PTS.
	maxCompositionTicks = maxGapSeconds * tsClock
)

// unwrapper unwraps the 33-bit timestamps of one input against the last value it
// returned: each value becomes the representative of its residue modulo 2^33
// nearest to that one, within ±2^32.
type unwrapper struct {
	last int64
	ok   bool
}

func (u *unwrapper) unwrap(v int64) int64 {
	v &= tsWrap - 1
	if !u.ok || u.last > maxTSValue || u.last < -maxTSValue {
		u.last, u.ok = v, true
		return v
	}
	d := (v - u.last) & (tsWrap - 1)
	if d >= tsWrap/2 {
		d -= tsWrap
	}
	u.last += d
	return u.last
}

// unit unwraps the PTS and DTS of a PES unit (-1 = absent). A missing DTS equals
// the PTS. Without a PTS, has is false and both are the last value unwrapped (0
// before any).
func (u *unwrapper) unit(pts, dts int64) (p, d int64, has bool) {
	if pts < 0 {
		return u.last, u.last, false
	}
	if dts < 0 {
		p = u.unwrap(pts)
		return p, p, true
	}
	d = u.unwrap(dts)
	p = u.unwrap(pts)
	return p, d, true
}

// mulDivRound returns v × mul / div rounded to the nearest integer (halves away
// from zero), saturating at the int64 range. mul and div are positive.
func mulDivRound(v int64, mul, div uint64) int64 {
	neg := v < 0
	a := uint64(v)
	if neg {
		a = uint64(-v) // for MinInt64 this is 2^63, which is right
	}
	hi, lo := bits.Mul64(a, mul)
	var c uint64
	lo, c = bits.Add64(lo, div/2, 0)
	hi += c
	if hi >= div {
		if neg {
			return math.MinInt64
		}
		return math.MaxInt64
	}
	q, _ := bits.Div64(hi, lo, div)
	if neg {
		if q > 1<<63 {
			return math.MinInt64
		}
		return -int64(q)
	}
	if q > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(q)
}

// toClock converts v from timescale ts to the 90 kHz clock, rounding to nearest.
func toClock(v int64, ts uint32) int64 {
	if ts == tsClock {
		return v
	}
	return mulDivRound(v, tsClock, uint64(ts))
}

// fromClock converts v from the 90 kHz clock to timescale ts, rounding to nearest.
func fromClock(v int64, ts uint32) int64 {
	if ts == tsClock {
		return v
	}
	return mulDivRound(v, uint64(ts), tsClock)
}

// addOK returns a + b and whether it did not overflow.
func addOK(a, b int64) (int64, bool) {
	s := a + b
	return s, (s > a) == (b > 0)
}

// videoTimer places the samples of one video track within a discontinuity domain:
// decode times increase strictly, small steps back are clamped, and steps below 0
// or above 10 s are stitched to the track's own end.
type videoTimer struct {
	started bool  // a sample in this domain
	prevDTS int64 // decode time of the previous sample of the domain
	lastDur int64 // the last duration of the track (across domains); 0 = none
	jump    int64 // added to the source timestamps after a stitch
}

// newDomain starts a discontinuity domain; the track's last duration is kept.
func (t *videoTimer) newDomain() { t.started, t.jump = false, 0 }

// dur is the duration of a sample without a successor.
func (t *videoTimer) dur() int64 {
	if t.lastDur > 0 {
		return t.lastDur
	}
	return defaultFrameTicks
}

// place returns the decode time and composition offset of the next sample, whose
// unwrapped timestamps are pts and dts (has: they are known), and the duration
// of the previous sample of the domain (-1 when this is its first sample).
func (t *videoTimer) place(pts, dts int64, has bool) (d int64, cto int32, prevDur int64) {
	d, p := dts+t.jump, pts+t.jump
	if !has && t.started {
		d = t.prevDTS + t.dur()
		p = d
	}
	prevDur = -1
	if t.started {
		step := d - t.prevDTS
		switch {
		case step >= 1 && step <= maxGapSeconds*tsClock:
		case step <= 0 && step >= -clampTicks:
			d = t.prevDTS + 1
		default: // an unlabelled jump: continue at the track's end
			adj := t.prevDTS + t.dur() - d
			t.jump += adj
			d += adj
			p += adj
		}
		prevDur = d - t.prevDTS
		t.lastDur = prevDur
	}
	t.started, t.prevDTS = true, d
	c := p - d
	if c > maxCompositionTicks || c < -maxCompositionTicks {
		c = 0
	}
	return d, int32(c), prevDur
}

// audioTimer places the AAC frames of one audio track within a discontinuity
// domain at the track's sample rate.
type audioTimer struct {
	started bool
	next    int64 // expected time of the next frame
	jump    int64 // added to frame times after a stitch
}

func (t *audioTimer) newDomain() { t.started, t.jump = false, 0 }

// place returns the time of a frame whose time from its PES is f (rate ticks),
// whether the frame is dropped (an overlap), and how much the previous frame is
// lengthened (a gap). Without a known time (has false) a frame follows the
// previous one; the first frame of a domain must have one.
func (t *audioTimer) place(f int64, has bool, rate int64) (at int64, drop bool, gap int64) {
	if !has && t.started {
		f = t.next
	} else {
		f += t.jump
	}
	if !t.started {
		t.started, t.next = true, f+aacFrame
		return f, false, 0
	}
	diff := f - t.next
	limit := maxGapSeconds * rate
	switch {
	case diff >= -aacTolerance && diff <= aacTolerance:
		at = t.next
	case diff > aacTolerance && diff <= limit:
		at, gap = f, diff
	case diff < -aacTolerance && -diff <= limit:
		return 0, true, 0
	default: // a jump of more than 10 s: continue at the track's end
		t.jump += t.next - f
		at = t.next
	}
	t.next = at + aacFrame
	return at, false, gap
}

// audioJumpTicks is how far (90 kHz) an audio unit may start before the previous
// unit of its stream and still be an overlap, which the audio timer drops, rather
// than an unlabelled jump of the input (1 s).
const audioJumpTicks = tsClock

// inputJumps finds the unlabelled timestamp jumps of one input (§4.5) from the
// timestamps of its kept streams alone, and moves every stream of the input by the
// same amount from the first unit that shows a jump, so that the input's video and
// audio keep their relative offset across it. A jump is a decode time (video) more
// than 1 ms, or a presentation time (audio) more than 1 s, before the previous unit
// of the same stream, or one more than 10 s after it; that stream then continues one
// step (its last step forward) after its previous unit. Each discontinuity domain
// has its own offset. Both passes see the same timestamps (pass 2 follows the other
// stream's in timestamps-only mode), so they move the units alike; the per-track
// timers still stitch what this does not see.
type inputJumps struct {
	doms []jumpState // by domain
}

type jumpState struct {
	off  int64 // added to every timestamp of the domain from here on
	seen [2]bool
	prev [2]int64 // per role: the last time, with off
	step [2]int64 // per role: the last step forward (0 = none yet)
}

// adjust returns the unwrapped timestamps of a unit of the stream of role ro in
// domain moved by the domain's offset (has: the unit has a PTS; without one only the
// offset is added).
func (j *inputJumps) adjust(ro role, pts, dts int64, has bool, domain int) (int64, int64) {
	for len(j.doms) <= domain {
		j.doms = append(j.doms, jumpState{})
	}
	s := &j.doms[domain]
	if has {
		t, back := dts+s.off, int64(clampTicks)
		if ro == roleAudio {
			t, back = pts+s.off, audioJumpTicks
		}
		if s.seen[ro] {
			switch diff := t - s.prev[ro]; {
			case diff < -back || diff > maxGapSeconds*tsClock:
				step := s.step[ro]
				if step == 0 {
					step = defaultFrameTicks
				}
				adj := s.prev[ro] + step - t
				s.off += adj
				t += adj
			case diff > 0:
				s.step[ro] = diff
			}
		}
		s.prev[ro], s.seen[ro] = t, true
	}
	return pts + s.off, dts + s.off
}

// trackDomain is what the first pass knows of one track in one discontinuity
// domain, on the track's source timeline (track timescale), and where the
// domain is placed on the output timeline.
type trackDomain struct {
	n       int    // samples
	first   uint64 // index of the domain's first sample in the track
	srcDTS  int64  // decode time of the first sample
	lastDTS int64  // decode time of the last sample
	lastDur uint32 // duration of the last sample (the one before repeated)
	minPTS  int64  // earliest presentation time
	presEnd int64  // latest presentation end (PTS + duration)
	minCTO  int32  // smallest composition offset

	// Placement (placeDomains).
	outDTS       int64  // output decode time of the first sample
	shift        int64  // moved by this much more than the domain's offset (continued at the track's end)
	delta        int64  // added to the decode times only (composition offsets − delta)
	finalLastDur uint32 // duration of the last sample on the output timeline
	presStart    int64  // output presentation time of minPTS
	presStop     int64  // output presentation time of presEnd
}

// timedTrack is one track for placeDomains.
type timedTrack struct {
	input     int
	timescale uint32
	domains   []trackDomain // by ordinal; n == 0 = no samples there
}

// placeDomains places the discontinuity domains of all tracks on one output
// timeline (docs/VIDEO-DETECT-SPEC.md §4.5). Domains are matched by ordinal. With
// P_d the earliest presentation in domain d over all tracks (90 kHz) and E the
// latest presentation end so far of the inputs that have samples in d, domain d is
// offset by E − P_d (the first domain by −P_0): the output starts at the earliest
// presentation, domains follow each other without gaps, and the tracks keep their
// relative offsets within each domain. An input with more domains than another
// continues at its own end.
//
// Decode times of a track increase strictly across domains: a domain that would
// start at or before the previous sample is delayed in decode time only (its
// composition offsets shrink by as much), up to 10 s; beyond that, or when the gap
// to the previous sample does not fit 32 bits, the domain continues at the track's
// end. It returns false when the times leave the int64 range.
func placeDomains(tracks []*timedTrack) bool {
	type state struct {
		has  bool
		last int64 // output decode time of the last sample
		dom  int   // its domain
	}
	var c checked
	st := make([]state, len(tracks))
	ends := map[int]int64{} // per input: latest presentation end, 90 kHz
	var globalEnd int64
	started := false
	maxD := 0
	for _, tr := range tracks {
		maxD = max(maxD, len(tr.domains))
	}
	for d := range maxD {
		adj := reconcileInputs(tracks, d, &c)
		p, found := int64(0), false
		for _, tr := range tracks {
			if d < len(tr.domains) && tr.domains[d].n > 0 {
				v := c.add(c.toClock(tr.domains[d].minPTS, tr.timescale), adj[tr.input])
				if !found || v < p {
					p, found = v, true
				}
			}
		}
		if !found {
			continue
		}
		var off int64
		if !started {
			off, started = c.sub(0, p), true
		} else {
			e, ok := int64(0), false
			for _, tr := range tracks {
				if d < len(tr.domains) && tr.domains[d].n > 0 {
					if v, has := ends[tr.input]; has && (!ok || v > e) {
						e, ok = v, true
					}
				}
			}
			if !ok {
				e = globalEnd
			}
			off = c.sub(e, p)
		}
		for i, tr := range tracks {
			if d >= len(tr.domains) || tr.domains[d].n == 0 {
				continue
			}
			dom := &tr.domains[d]
			offTS := c.fromClock(c.add(off, adj[tr.input]), tr.timescale)
			first := c.add(dom.srcDTS, offTS)
			shift, delta := int64(0), int64(0)
			if s := &st[i]; s.has {
				prev := &tr.domains[s.dom]
				if first <= s.last {
					delta = c.sub(c.add(s.last, 1), first)
				}
				gap := c.sub(c.add(first, delta), s.last)
				if delta > fromClock(maxGapSeconds*tsClock, tr.timescale) || gap > math.MaxUint32 {
					shift = c.sub(c.add(s.last, int64(prev.lastDur)), first)
					delta, gap = 0, int64(prev.lastDur)
				}
				prev.finalLastDur = uint32(gap)
			}
			dom.outDTS = c.add(c.add(first, shift), delta)
			dom.shift, dom.delta = shift, delta
			dom.finalLastDur = dom.lastDur
			move := c.add(offTS, shift)
			dom.presStart = c.add(dom.minPTS, move)
			dom.presStop = c.add(dom.presEnd, move)
			st[i] = state{has: true, last: c.add(dom.outDTS, c.sub(dom.lastDTS, dom.srcDTS)), dom: d}
			e := c.toClock(dom.presStop, tr.timescale)
			if v, has := ends[tr.input]; !has || e > v {
				ends[tr.input] = e
			}
			globalEnd = max(globalEnd, e)
		}
		if !c.ok() {
			return false
		}
	}
	return c.ok()
}

// reconcileInputs returns, per input with samples in domain d, the multiple of 2^33
// (90 kHz) that brings its earliest presentation nearest to that of the input with the
// lowest index there. Each input unwraps its own 33-bit timestamps from its own first
// value, so separate renditions whose starts straddle the wrap (or a discontinuity
// that jumps by about 2^32) would otherwise land a period apart; within one input the
// tracks share one unwrapper, and the reference input is never moved.
func reconcileInputs(tracks []*timedTrack, d int, c *checked) map[int]int64 {
	ref, refInput, found := int64(0), 0, false
	for _, tr := range tracks {
		if d < len(tr.domains) && tr.domains[d].n > 0 && (!found || tr.input < refInput) {
			ref, refInput, found = c.toClock(tr.domains[d].minPTS, tr.timescale), tr.input, true
		}
	}
	adj := map[int]int64{}
	if !found {
		return adj
	}
	adj[refInput] = 0
	for _, tr := range tracks {
		if d >= len(tr.domains) || tr.domains[d].n == 0 {
			continue
		}
		if _, done := adj[tr.input]; done {
			continue
		}
		diff := c.sub(ref, c.toClock(tr.domains[d].minPTS, tr.timescale))
		// The nearest multiple: floor((diff + 2^32) / 2^33) periods.
		k := c.add(diff, tsWrap/2) >> 33
		adj[tr.input] = c.mul(k, tsWrap)
	}
	return adj
}

// checked does int64 arithmetic and remembers an overflow.
type checked struct{ overflow bool }

func (c *checked) ok() bool { return !c.overflow }

func (c *checked) add(a, b int64) int64 {
	s, ok := addOK(a, b)
	if !ok {
		c.overflow = true
	}
	return s
}

// mul returns a × b for b > 0.
func (c *checked) mul(a, b int64) int64 {
	hi, lo := bits.Mul64(uint64(max(a, -a)), uint64(b))
	if a == math.MinInt64 || hi != 0 || lo > math.MaxInt64 {
		c.overflow = true
		return 0
	}
	if a < 0 {
		return -int64(lo)
	}
	return int64(lo)
}

func (c *checked) sub(a, b int64) int64 {
	if b == math.MinInt64 {
		c.overflow = true
		return 0
	}
	return c.add(a, -b)
}

// toClock and fromClock convert like the functions of the same name; a result
// that saturates is an overflow.
func (c *checked) toClock(v int64, ts uint32) int64 { return c.sat(toClock(v, ts)) }

func (c *checked) fromClock(v int64, ts uint32) int64 { return c.sat(fromClock(v, ts)) }

func (c *checked) sat(v int64) int64 {
	if v == math.MaxInt64 || v == math.MinInt64 {
		c.overflow = true
	}
	return v
}
