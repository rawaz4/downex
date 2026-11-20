package polite

import "time"

// meter derives Downex's receive rates from the daemon's byte counter (rule 16), one
// point per tick: X0.5, X2 and X10 are the rates over the last 1, 4 and 20 ticks (rule 9).
// It is pure: the controller feeds it the tick times.
type meter struct {
	pts []point // oldest first, at most maxPoints
	// quietBefore: nothing was received before the first point (the meter started when
	// downloads started), so a span reaching further back counts those bytes over the
	// whole span. Without it a rate is unknown until the meter holds the span.
	quietBefore bool
}

type point struct {
	t  time.Time
	rx int64
}

// maxPoints keeps 10 s of ticks plus slack for late ticks.
const maxPoints = 24

// The rate spans of rule 9.
const (
	spanX05 = Tick
	spanX2  = 4 * Tick
	spanX10 = 20 * Tick
)

// reset starts the meter afresh at (now, rx).
func (m *meter) reset(now time.Time, rx int64, quietBefore bool) {
	m.pts = append(m.pts[:0], point{now, rx})
	m.quietBefore = quietBefore
}

// add records the counter at a tick.
func (m *meter) add(now time.Time, rx int64) {
	if n := len(m.pts); n > 0 && !now.After(m.pts[n-1].t) {
		m.pts[n-1].rx = rx // same instant: keep the latest count
		return
	}
	if len(m.pts) == maxPoints {
		copy(m.pts, m.pts[1:])
		m.pts = m.pts[:maxPoints-1]
	}
	m.pts = append(m.pts, point{now, rx})
}

// rate returns the receive rate over the span ending at the latest point, and whether it
// is known. The point closest to span ago is used; a tick may come a little late.
func (m *meter) rate(span time.Duration) (int64, bool) {
	n := len(m.pts)
	if n == 0 {
		return 0, false
	}
	last := m.pts[n-1]
	want := last.t.Add(-span)
	for i := n - 2; i >= 0; i-- {
		p := m.pts[i]
		if p.t.After(want.Add(Tick / 2)) {
			continue
		}
		// p is the newest point at most half a tick later than wanted: the span is
		// covered (an older one would only be further off).
		el := last.t.Sub(p.t)
		if el <= 0 {
			return 0, false
		}
		return perSecond(last.rx-p.rx, el), true
	}
	if !m.quietBefore {
		return 0, false
	}
	// Nothing came in before the first point: the bytes since it, over the whole span.
	return perSecond(last.rx-m.pts[0].rx, span), true
}

// quietAt reports whether the line counts as quiet at now with the counter at rx (trust
// rule 7a): Downex received under Quiet over the preceding 2 s, and also since the last
// tick (a download that has just started fills the line long before its 2 s average
// shows it).
func (m *meter) quietAt(now time.Time, rx int64) bool {
	n := len(m.pts)
	if n == 0 {
		return false
	}
	last := m.pts[n-1]
	if el := now.Sub(last.t); el >= ProbeSpacing/2 {
		if perSecond(rx-last.rx, el) >= Quiet {
			return false
		}
	} else if x, ok := m.rate(spanX05); !ok || x >= Quiet {
		return false
	}
	probe := meter{pts: append(append([]point(nil), m.pts...), point{now, rx}), quietBefore: m.quietBefore}
	if !now.After(last.t) {
		probe.pts = probe.pts[:n]
		probe.pts[n-1].rx = rx
	}
	x2, ok := probe.rate(spanX2)
	return ok && x2 < Quiet
}

func perSecond(bytes int64, d time.Duration) int64 {
	if bytes <= 0 || d <= 0 {
		return 0
	}
	return int64(float64(bytes) / d.Seconds())
}

// peak keeps the highest rate recorded within CapacityWindow: hourly maxima, so that an
// old peak expires within an hour of the window (the line's capacity C, rule 9).
type peak struct {
	hours [peakBuckets]struct {
		hour int64 // index of the hour, 0 = unused
		v    int64
	}
}

const peakBuckets = int(CapacityWindow / time.Hour)

func hourOf(t time.Time) int64 { return t.UnixNano()/int64(time.Hour) + 1 }

func (p *peak) record(now time.Time, v int64) {
	h := hourOf(now)
	b := &p.hours[h%int64(peakBuckets)]
	if b.hour != h {
		b.hour, b.v = h, 0
	}
	b.v = max(b.v, v)
}

// max returns the highest rate recorded in the window ending at now (0 if none).
func (p *peak) max(now time.Time) int64 {
	h := hourOf(now)
	var out int64
	for _, b := range p.hours {
		if b.hour != 0 && b.hour > h-int64(peakBuckets) && b.hour <= h {
			out = max(out, b.v)
		}
	}
	return out
}
