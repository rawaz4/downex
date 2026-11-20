package polite

import (
	"context"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// fakeProber answers each address after its scripted RTT (0: no reply, after the
// timeout) and records what it was asked.
type fakeProber struct {
	mu        sync.Mutex
	rtt       map[netip.Addr]time.Duration
	tcpRTT    time.Duration
	icmp      bool // ICMP is answered (else every ICMP probe times out)
	inflight  map[netip.Addr]int
	maxPer    int
	maxTotal  int
	total     int
	icmpSent  int
	tcpSent   int
	sentTimes []time.Time
	blockAll  bool // probes block until their context ends
	cancelled int
}

func newFakeProber() *fakeProber {
	return &fakeProber{rtt: map[netip.Addr]time.Duration{}, inflight: map[netip.Addr]int{}, icmp: true, tcpRTT: 30 * time.Millisecond}
}

func (p *fakeProber) setRTT(a string, d time.Duration) {
	p.mu.Lock()
	p.rtt[netip.MustParseAddr(a)] = d
	p.mu.Unlock()
}

func (p *fakeProber) begin(a netip.Addr, icmp bool) (time.Duration, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.inflight[a]++
	p.maxPer = max(p.maxPer, p.inflight[a])
	n := 0
	for _, v := range p.inflight {
		n += v
	}
	p.maxTotal = max(p.maxTotal, n)
	p.total++
	p.sentTimes = append(p.sentTimes, time.Now())
	if icmp {
		p.icmpSent++
		return p.rtt[a], p.icmp
	}
	p.tcpSent++
	return p.tcpRTT, true
}

func (p *fakeProber) end(a netip.Addr) {
	p.mu.Lock()
	p.inflight[a]--
	p.mu.Unlock()
}

func (p *fakeProber) answer(ctx context.Context, rtt time.Duration, ok bool) (time.Duration, error) {
	p.mu.Lock()
	block := p.blockAll
	p.mu.Unlock()
	if block {
		<-ctx.Done()
		p.mu.Lock()
		p.cancelled++
		p.mu.Unlock()
		return 0, ctx.Err()
	}
	wait := rtt
	if !ok || rtt == 0 {
		wait = ProbeTimeout
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	if !ok || rtt == 0 {
		return 0, ErrNoReply
	}
	return rtt, nil
}

func (p *fakeProber) ProbeICMP(ctx context.Context, a netip.Addr) (time.Duration, error) {
	rtt, ok := p.begin(a, true)
	defer p.end(a)
	return p.answer(ctx, rtt, ok)
}

func (p *fakeProber) ProbeTCP(ctx context.Context, a netip.AddrPort) (time.Duration, error) {
	rtt, ok := p.begin(a.Addr(), false)
	defer p.end(a.Addr())
	return p.answer(ctx, rtt, ok)
}

func (p *fakeProber) Close() error { return nil }

// rig is a Monitor with fakes: Downex receives rate bytes/s while busy.
type rig struct {
	m        *Monitor
	p        *fakeProber
	busy     atomic.Bool
	rate     atomic.Int64
	rx       atomic.Int64
	last     atomic.Int64 // UnixNano of the last rx update
	peers    atomic.Pointer[[]Peer]
	mu       sync.Mutex
	applied  []int64
	statuses []Status
}

func newRig(t *testing.T, enabled bool, anchors ...string) *rig {
	r := &rig{p: newFakeProber()}
	var peers []Peer
	for i, a := range anchors {
		peers = append(peers, Peer{Addr: netip.MustParseAddrPort(a), Conns: 4 - i, First: time.Now()})
		r.p.setRTT(netip.MustParseAddrPort(a).Addr().String(), 20*time.Millisecond)
	}
	r.peers.Store(&peers)
	r.last.Store(time.Now().UnixNano())
	r.m = NewMonitor(Config{
		Prober: r.p,
		Peers:  func() []Peer { return *r.peers.Load() },
		Received: func() int64 {
			now := time.Now().UnixNano()
			prev := r.last.Swap(now)
			if r.busy.Load() {
				r.rx.Add(r.rate.Load() * (now - prev) / int64(time.Second))
			}
			return r.rx.Load()
		},
		Busy: func() bool { return r.busy.Load() },
		Apply: func(rate int64) {
			_ = r.m.Status() // re-entrant: Apply is never called under the monitor's lock
			r.mu.Lock()
			r.applied = append(r.applied, rate)
			r.mu.Unlock()
		},
		Publish: func(st Status) {
			_ = r.m.Status()
			r.mu.Lock()
			r.statuses = append(r.statuses, st)
			r.mu.Unlock()
		},
		Enabled: enabled,
	})
	r.m.Start()
	t.Cleanup(r.m.Stop)
	return r
}

func (r *rig) lastStatus() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.statuses[len(r.statuses)-1]
}

func (r *rig) lastApplied() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.applied) == 0 {
		return -1
	}
	return r.applied[len(r.applied)-1]
}

func (r *rig) probes() int {
	r.p.mu.Lock()
	defer r.p.mu.Unlock()
	return r.p.total
}

// At most 4 probes a second in total and one in flight per anchor, however slowly the
// anchors answer.
func TestMonitorProbeRate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t, true, "203.0.113.1:443", "203.0.113.2:443", "203.0.113.3:443", "203.0.113.4:443")
		for _, a := range []string{"203.0.113.1", "203.0.113.2", "203.0.113.3"} {
			r.p.setRTT(a, 900*time.Millisecond) // slow answers overlap the next turns
		}
		r.busy.Store(true)
		r.m.Wake()
		time.Sleep(20 * time.Second)
		synctest.Wait()
		r.p.mu.Lock()
		defer r.p.mu.Unlock()
		if r.p.maxPer > 1 {
			t.Errorf("%d probes in flight to one anchor", r.p.maxPer)
		}
		if r.p.maxTotal > MaxAnchors {
			t.Errorf("%d probes in flight", r.p.maxTotal)
		}
		times := r.p.sentTimes
		for i := range times {
			n := 0
			for j := i; j < len(times) && times[j].Sub(times[i]) < time.Second; j++ {
				n++
			}
			if n > 4 {
				t.Fatalf("%d probes within a second from %v", n, times[i])
			}
		}
		if len(times) < 20 {
			t.Fatalf("only %d probes in 20 s", len(times))
		}
		for a, n := range r.p.inflight {
			if a == netip.MustParseAddr("203.0.113.4") && n != 0 {
				t.Fatal("the fourth anchor was probed")
			}
		}
	})
}

// Nothing is probed while off or while nothing downloads; a download start wakes it.
func TestMonitorIdleAndOff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t, false, "203.0.113.1:443")
		r.busy.Store(true)
		time.Sleep(5 * time.Second)
		synctest.Wait()
		if n := r.probes(); n != 0 {
			t.Fatalf("%d probes while off", n)
		}
		if st := r.m.Status(); st.State != Off {
			t.Fatalf("status %v while off", st.State)
		}
		r.busy.Store(false)
		r.m.Configure(true, 0)
		time.Sleep(5 * time.Second)
		synctest.Wait()
		if n := r.probes(); n != 0 || r.m.Status().State != Idle {
			t.Fatalf("%d probes, state %v while idle", n, r.m.Status().State)
		}
		r.busy.Store(true)
		r.m.Wake()
		time.Sleep(3 * time.Second)
		synctest.Wait()
		if n := r.probes(); n == 0 || r.m.Status().State != Watching {
			t.Fatalf("%d probes, state %v after a download started", n, r.m.Status().State)
		}
		// Downloads end: idle at the next tick, and the probes stop.
		r.busy.Store(false)
		time.Sleep(2 * Tick)
		synctest.Wait()
		n := r.probes()
		time.Sleep(10 * time.Second)
		synctest.Wait()
		if got := r.probes(); got != n || r.m.Status().State != Idle {
			t.Fatalf("%d probes while idle, state %v", got-n, r.m.Status().State)
		}
	})
}

// A laggy line slows downloads; switching off removes the cap and publishes Off before
// Configure returns.
func TestMonitorSlowAndOff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t, true, "203.0.113.1:443", "203.0.113.2:443")
		r.busy.Store(true)
		r.m.Wake()
		time.Sleep(2 * time.Second) // quiet: the anchors are trusted
		r.rate.Store(4 << 20)
		r.p.setRTT("203.0.113.1", 100*time.Millisecond)
		r.p.setRTT("203.0.113.2", 100*time.Millisecond)
		time.Sleep(6 * time.Second)
		synctest.Wait()
		if st := r.lastStatus(); st.State != Slowed || st.RateLimit <= 0 {
			t.Fatalf("published %+v, want SLOWED", st)
		}
		if a := r.lastApplied(); a <= 0 {
			t.Fatalf("applied %d, want a cap", a)
		}
		r.m.Configure(false, 0)
		if st, a := r.lastStatus(), r.lastApplied(); st.State != Off || a != 0 {
			t.Fatalf("after Configure(false): published %v, applied %d", st.State, a)
		}
		n := r.probes()
		time.Sleep(5 * time.Second)
		synctest.Wait()
		if r.probes() != n {
			t.Fatal("probes after switching off")
		}
		if st := r.lastStatus(); st.State != Off {
			t.Fatalf("a stale tick published %v after Off", st.State)
		}
		// The target applies at once.
		r.m.Configure(true, 150*time.Millisecond)
		if got := r.m.ctrl.Target(); got != 150*time.Millisecond {
			t.Fatalf("target %v", got)
		}
	})
}

// Every publish follows rule 11: each state change once, not every tick.
func TestMonitorPublishesStateChanges(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t, true, "203.0.113.1:443")
		r.busy.Store(true)
		r.m.Wake()
		time.Sleep(30 * time.Second)
		synctest.Wait()
		r.mu.Lock()
		defer r.mu.Unlock()
		var states []State
		for _, st := range r.statuses {
			states = append(states, st.State)
		}
		want := []State{Off, Idle, Watching}
		if len(states) != len(want) {
			t.Fatalf("published %v, want %v", states, want)
		}
		for i := range want {
			if states[i] != want[i] {
				t.Fatalf("published %v, want %v", states, want)
			}
		}
	})
}

// Stop cancels the probes in flight and leaves no goroutine behind (the bubble would
// report it).
func TestMonitorStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t, true, "203.0.113.1:443", "203.0.113.2:443")
		r.p.blockAll = true
		r.busy.Store(true)
		r.m.Wake()
		time.Sleep(time.Second)
		synctest.Wait()
		r.m.Stop()
		r.p.mu.Lock()
		defer r.p.mu.Unlock()
		if r.p.total == 0 || r.p.cancelled == 0 {
			t.Fatalf("%d probes, %d cancelled", r.p.total, r.p.cancelled)
		}
	})
}

// ICMP that never answers moves an anchor to TCP probes, at most one per 2 s.
func TestMonitorTCPFallback(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t, true, "203.0.113.1:443")
		r.p.icmp = false
		r.busy.Store(true)
		r.m.Wake()
		time.Sleep(time.Minute)
		synctest.Wait()
		r.p.mu.Lock()
		defer r.p.mu.Unlock()
		if r.p.icmpSent != ICMPFailuresToTCP {
			t.Fatalf("%d ICMP probes, want %d", r.p.icmpSent, ICMPFailuresToTCP)
		}
		if max := int(time.Minute/TCPProbeSpacing) + 1; r.p.tcpSent > max || r.p.tcpSent < max-4 {
			t.Fatalf("%d TCP probes in a minute, want about %d", r.p.tcpSent, max)
		}
	})
}
