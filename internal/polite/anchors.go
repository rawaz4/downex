package polite

import (
	"cmp"
	"net/netip"
	"slices"
	"time"
)

// Peer is one remote address Downex holds live connections to (docs/POLITE-SPEC.md
// rule 1): the origin of a direct connection, else the proxy's address.
type Peer struct {
	Addr  netip.AddrPort
	Conns int       // live connections now
	First time.Time // when the first of them was made
	// Penalized: a host reached through this address has an active connection penalty
	// (the engine lowered its connection cap after 429/503 or refusals, ENGINE-SPEC §10).
	// Such an anchor gets no TCP probes, which the server would count as connections.
	Penalized bool
}

// fakeIPv4 are the ranges fake-IP TUN proxies hand out (rule 2): probing them measures
// nothing real.
var fakeIPv4 = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"), // shared address space (carrier-grade NAT, TUN apps)
	netip.MustParsePrefix("198.18.0.0/15"), // benchmarking range, used by fake-IP proxies
}

// Eligible reports whether a may be probed (rule 2): a global unicast address, never
// loopback, private (RFC 1918, ULA), link-local, multicast, unspecified or a fake-IP
// range. An IPv4-mapped IPv6 address is judged as its IPv4 address. So Polite mode never
// probes the local network.
func Eligible(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsValid() || !a.IsGlobalUnicast() || a.IsPrivate() || a.Zone() != "" {
		return false
	}
	for _, p := range fakeIPv4 {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

// sample is one probe's round trip, timed by when it was sent (rule 5).
type sample struct {
	sent time.Time
	rtt  time.Duration
	tcp  bool // a TCP connect probe's: its decision window is TCPFilterWindow (rule 8)
}

// window is how long s counts towards d (rule 8).
func (s sample) window() time.Duration {
	if s.tcp {
		return TCPFilterWindow
	}
	return FilterWindow
}

// baseBucket is the minimum RTT of one minute of samples.
type baseBucket struct {
	idx int64 // minute index of the samples' send times, 0 = unused
	min time.Duration
}

// maxSamples bounds the samples kept per anchor: the filter window holds at most six
// (4 probes/s over 1.5 s; TCP probes, one per 2 s, at most three over 5 s), and older
// ones are dropped as new ones come.
const maxSamples = 32

// outstanding is a probe in flight.
type outstanding struct {
	sent  time.Time
	icmp  bool
	quiet bool // the line was quiet when it was sent (trust rule 7a)
}

// anchor is one probed server (by IP address; its TCP probes go to the port with the
// most connections).
type anchor struct {
	addr      netip.Addr
	port      uint16
	conns     int
	first     time.Time
	lastLive  time.Time
	live      bool
	selected  bool
	penalized bool

	// Probing (rule 4).
	tcp       bool // TCP connect probes instead of ICMP
	tcpSince  time.Time
	icmpFails int  // consecutive ICMP probes without an answer
	answered  bool // ICMP was ever answered
	// lossy: it answered ICMP, then lost ICMPFailuresToTCP in a row while d was unknown
	// where needed; TCP probes go between its echoes until lossySince + ICMPRetryAfter.
	lossy      bool
	lossySince time.Time
	lastTCP    time.Time
	out        *outstanding
	lastReply  time.Time // when the latest answered probe was sent

	// Base and delay (rules 6-8).
	buckets [BaseBuckets]baseBucket
	samples []sample // oldest first
	sampled bool     // any sample yet: the first decides trust rule 7a
	trusted bool
	// The current calibration's samples sent CalibrateTrust or more after it began: how
	// many, their least RTT, and whether the latest set a new low by BaseMoved or more.
	calCount   int
	calMin     time.Duration
	calFalling bool
}

func minuteOf(t time.Time) int64 { return t.UnixNano()/int64(BaseBucket) + 1 }

// add records a sample.
func (a *anchor) add(s sample) {
	idx := minuteOf(s.sent)
	b := &a.buckets[idx%BaseBuckets]
	if b.idx != idx {
		*b = baseBucket{idx: idx, min: s.rtt}
	} else {
		b.min = min(b.min, s.rtt)
	}
	if len(a.samples) == maxSamples {
		copy(a.samples, a.samples[1:])
		a.samples = a.samples[:maxSamples-1]
	}
	// Results can come back out of order; keep the samples sorted by send time.
	i, _ := slices.BinarySearchFunc(a.samples, s.sent, func(x sample, t time.Time) int { return x.sent.Compare(t) })
	a.samples = slices.Insert(a.samples, i, s)
	a.sampled = true
}

// base returns the minimum RTT over the last BaseBuckets minutes including the current
// one (rule 6).
func (a *anchor) base(now time.Time) (time.Duration, bool) {
	cur := minuteOf(now)
	var out time.Duration
	ok := false
	for _, b := range a.buckets {
		if b.idx == 0 || b.idx <= cur-BaseBuckets || b.idx > cur {
			continue
		}
		if !ok || b.min < out {
			out, ok = b.min, true
		}
	}
	return out, ok
}

// minIn returns the least RTT of the samples in the decision window at now and how many
// there are: sent at or after floor, at most now, and within their own window of now
// (rule 8).
func (a *anchor) minIn(now, floor time.Time) (time.Duration, int) {
	var out time.Duration
	n := 0
	for _, s := range a.samples {
		if s.sent.Before(floor) || s.sent.After(now) || s.sent.Before(now.Add(-s.window())) {
			continue
		}
		if n == 0 || s.rtt < out {
			out = s.rtt
		}
		n++
	}
	return out, n
}

// anchors is the anchor table (rules 2-3): every server probed recently, the selected
// ones in selection order.
type anchors struct {
	m        map[netip.Addr]*anchor
	selected []*anchor
	rr       int  // round robin position in selected
	icmpOff  bool // this system cannot send ICMP: TCP probes only
	loopback bool // loopback addresses are eligible (end-to-end tests only)
}

func newAnchors() anchors { return anchors{m: map[netip.Addr]*anchor{}} }

func (t *anchors) eligible(a netip.Addr) bool {
	return Eligible(a) || (t.loopback && a.Unmap().IsLoopback())
}

// update merges the live peers (by IP address), forgets anchors gone for AnchorKeep and
// selects at most MaxAnchors: most live connections first, then the earliest seen.
func (t *anchors) update(now time.Time, peers []Peer) {
	for _, a := range t.m {
		a.live, a.conns, a.penalized = false, 0, false
	}
	type portUse struct {
		port  uint16
		conns int
	}
	best := map[netip.Addr]portUse{}
	for _, p := range peers {
		ip := p.Addr.Addr().Unmap()
		if !t.eligible(ip) || p.Conns <= 0 {
			continue
		}
		a := t.m[ip]
		if a == nil {
			a = &anchor{addr: ip, first: p.First}
			t.m[ip] = a
		}
		if !a.live || p.First.Before(a.first) {
			a.first = p.First
		}
		a.live, a.lastLive = true, now
		a.conns += p.Conns
		a.penalized = a.penalized || p.Penalized
		if u := best[ip]; p.Conns > u.conns {
			best[ip] = portUse{p.Addr.Port(), p.Conns}
		}
	}
	for ip, u := range best {
		t.m[ip].port = u.port
	}
	var live []*anchor
	for ip, a := range t.m {
		a.selected = false
		if a.live {
			live = append(live, a)
		} else if now.Sub(a.lastLive) > AnchorKeep {
			delete(t.m, ip)
		}
	}
	slices.SortFunc(live, func(x, y *anchor) int {
		if c := cmp.Compare(y.conns, x.conns); c != 0 {
			return c
		}
		if c := x.first.Compare(y.first); c != 0 {
			return c
		}
		return x.addr.Compare(y.addr)
	})
	if len(live) > MaxAnchors {
		live = live[:MaxAnchors]
	}
	for _, a := range live {
		a.selected = true
	}
	t.selected = live
	if t.rr >= len(live) {
		t.rr = 0
	}
}

// inFlight counts the probes in flight.
func (t *anchors) inFlight() int {
	n := 0
	for _, a := range t.m {
		if a.out != nil {
			n++
		}
	}
	return n
}

// next picks the anchor to probe now, round robin over the selected ones (rule 4): never
// one with a probe in flight, a TCP probe at most every TCPProbeSpacing per anchor and
// never to a penalized one, and at most MaxAnchors probes in flight in total; a lossy
// anchor gets a TCP probe in place of an echo whenever one is due. It returns whether the
// probe is ICMP.
func (t *anchors) next(now time.Time) (*anchor, bool, bool) {
	n := len(t.selected)
	if n == 0 || t.inFlight() >= MaxAnchors {
		return nil, false, false
	}
	for i := range n {
		a := t.selected[(t.rr+i)%n]
		if a.out != nil {
			continue
		}
		if a.tcp && !t.icmpOff && now.Sub(a.tcpSince) >= ICMPRetryAfter {
			a.tcp, a.icmpFails = false, 0 // ICMP is tried again
		}
		if a.lossy && now.Sub(a.lossySince) >= ICMPRetryAfter {
			a.lossy, a.icmpFails = false, 0
		}
		tcpOK := !a.penalized && a.port != 0 && (a.lastTCP.IsZero() || now.Sub(a.lastTCP) >= TCPProbeSpacing)
		icmp := !a.tcp && !t.icmpOff && !(a.lossy && tcpOK) // a lossy anchor's TCP probe when one is due
		if !icmp && !tcpOK {
			continue
		}
		t.rr = (t.rr + i + 1) % n
		return a, icmp, true
	}
	return nil, false, false
}

// dropSamples forgets every stored sample and keeps the bases (a sleep, rule 20).
func (t *anchors) dropSamples() {
	for _, a := range t.m {
		a.samples = a.samples[:0]
	}
}

// trustedLive reports whether a selected anchor is trusted.
func (t *anchors) trustedLive() bool {
	return slices.ContainsFunc(t.selected, func(a *anchor) bool { return a.trusted })
}

// answeredSince reports whether a selected anchor answered a probe sent at or after t.
func (t *anchors) answeredSince(at time.Time) bool {
	return slices.ContainsFunc(t.selected, func(a *anchor) bool { return !a.lastReply.Before(at) && !a.lastReply.IsZero() })
}

// estimate is the line delay d of rule 8.
type estimate struct {
	known bool
	d     time.Duration // the lower median of the anchors' extra delays
	base  time.Duration // the base of the anchor that gave d
}

// estimate computes d at now over the samples sent at or after floor (the latest settle
// or tick gap) and within their window (FilterWindow, or TCPFilterWindow for a TCP
// probe's): for each trusted anchor with samples in it, its least RTT minus its base (at
// least 0); d is the lower median of these and is known only with at least 2 samples in
// total.
func (t *anchors) estimate(now, floor time.Time) estimate {
	type extra struct{ d, base time.Duration }
	var vals []extra
	total := 0
	for _, a := range t.m {
		if !a.trusted {
			continue
		}
		base, ok := a.base(now)
		if !ok {
			continue
		}
		least, n := a.minIn(now, floor)
		if n == 0 {
			continue
		}
		vals = append(vals, extra{max(0, least-base), base})
		total += n
	}
	if total < 2 || len(vals) == 0 {
		return estimate{}
	}
	slices.SortFunc(vals, func(x, y extra) int {
		if c := cmp.Compare(x.d, y.d); c != 0 {
			return c
		}
		return cmp.Compare(x.base, y.base)
	})
	m := vals[(len(vals)-1)/2]
	return estimate{known: true, d: m.d, base: m.base}
}

// bases returns the bases of the selected anchors (for a calibration's "moved" test).
func (t *anchors) bases(now time.Time) map[netip.Addr]time.Duration {
	out := map[netip.Addr]time.Duration{}
	for _, a := range t.selected {
		if b, ok := a.base(now); ok {
			out[a.addr] = b
		}
	}
	return out
}
