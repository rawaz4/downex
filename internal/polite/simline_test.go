package polite

import (
	"math/rand/v2"
	"net/netip"
	"slices"
	"testing"
	"time"
)

// sim is a fluid model of one bottleneck (docs/POLITE-SPEC.md §9): the owner's line of
// about 38 Mbit/s with a 500 ms buffer, Downex's downloads whose input follows the cap
// with a first-order lag (buffered receive windows), other traffic, and probes whose RTT
// is the base plus the queue's delay, with jitter and loss. Downex and a competing bulk
// download are TCP-like: uncapped, each offers a little more than the line carries
// (ACK-clocked flows whose windows keep growing), which fills the buffer. It drives a
// Controller in virtual time, 10 ms per step.
type sim struct {
	t   *testing.T
	c   *Controller
	rng *rand.Rand
	now time.Time

	capacity float64       // bytes/s
	base     time.Duration // propagation RTT
	qmax     float64       // buffer, bytes
	tau      float64       // Downex's lag, seconds
	demand   float64       // what Downex offers uncapped, bytes/s
	call     float64       // a call's constant rate, bytes/s (0 = none)
	bulk     float64       // a competing bulk download's offered rate (0 = none)
	loss     float64       // probe loss
	lostAll  bool          // every probe lost
	jitter   time.Duration // ± per probe
	spikes   float64       // share of probes delayed by spike on top (ICMP deprioritised)
	spike    time.Duration

	q     float64 // queue, bytes
	dxIn  float64 // Downex's input into the queue
	rx    float64 // Downex's received bytes
	peers []Peer
	dec   Decision
	steps int

	// Recorded per step after the warm-up.
	delays []time.Duration // queue delays
	floorT time.Duration   // time spent capped at the floor
}

const simStep = 10 * time.Millisecond

func newSim(t *testing.T, seed uint64) *sim {
	s := &sim{
		t: t, c: NewController(0), rng: rand.New(rand.NewPCG(seed, 1)), now: t0,
		capacity: 4.75e6, base: 20 * time.Millisecond, tau: 2, demand: 1.05 * 4.75e6,
		loss: 0.02, jitter: 3 * time.Millisecond,
		peers: []Peer{
			{Addr: netip.MustParseAddrPort("203.0.113.10:443"), Conns: 8, First: t0},
			{Addr: netip.MustParseAddrPort("198.51.100.20:443"), Conns: 4, First: t0},
		},
	}
	s.qmax = 0.5 * s.capacity
	s.c.Enable(t0, true, 0, false)
	return s
}

// run advances the model by d; record keeps the queue delays and floor time.
func (s *sim) run(d time.Duration, record bool) {
	dt := simStep.Seconds()
	for end := s.now.Add(d); s.now.Before(end); {
		s.now = s.now.Add(simStep)
		s.steps++
		target := s.demand
		if s.dec.Rate > 0 {
			target = min(target, float64(s.dec.Rate))
		}
		s.dxIn += (target - s.dxIn) * dt / s.tau
		in := s.dxIn + s.call + s.bulk
		out := in
		if s.q > 0 || in > s.capacity {
			out = s.capacity
		}
		s.q = min(max(s.q+(in-s.capacity)*dt, 0), s.qmax)
		if in > 0 {
			s.rx += out * dt * s.dxIn / in
		}
		if record {
			s.delays = append(s.delays, s.queueDelay())
			if s.dec.Rate == s.c.floor && s.dec.Rate > 0 {
				s.floorT += simStep
			}
		}
		if s.steps%int(ProbeSpacing/simStep) == 0 {
			s.c.SetPeers(s.now, s.peers)
			if p, ok := s.c.NextProbe(s.now, int64(s.rx)); ok {
				if s.lostAll || s.rng.Float64() < s.loss {
					s.c.ProbeDone(p, 0, ErrNoReply)
				} else {
					j := time.Duration((s.rng.Float64()*2 - 1) * float64(s.jitter))
					if s.rng.Float64() < s.spikes {
						j += s.spike
					}
					s.c.ProbeDone(p, s.base+s.queueDelay()+j, nil)
				}
			}
		}
		if s.steps%int(Tick/simStep) == 0 {
			s.dec = s.c.Step(s.now, int64(s.rx), true)
		}
	}
}

func (s *sim) queueDelay() time.Duration {
	return time.Duration(s.q / s.capacity * float64(time.Second))
}

// throughput runs d and returns Downex's mean rate over it.
func (s *sim) throughput(d time.Duration) float64 {
	rx := s.rx
	s.run(d, true)
	return (s.rx - rx) / d.Seconds()
}

func p95(ds []time.Duration) time.Duration {
	s := slices.Clone(ds)
	slices.Sort(s)
	return s[len(s)*95/100]
}

// (a) A quiet line: Downex alone keeps the queue near the target and still takes most of
// the line.
func TestSimQuietLine(t *testing.T) {
	for _, tau := range []float64{1, 2, 3} {
		s := newSim(t, 1)
		s.tau = tau
		s.run(20*time.Second, false)
		got := s.throughput(3 * time.Minute)
		if p := p95(s.delays); p > s.c.target+10*time.Millisecond {
			t.Errorf("τ %.0f s: 95th percentile queue delay %v, want ≤ %v", tau, p, s.c.target+10*time.Millisecond)
		}
		if got < 0.85*s.capacity {
			t.Errorf("τ %.0f s: Downex %.2f MB/s, want ≥ 85%% of %.2f", tau, got/1e6, s.capacity/1e6)
		}
	}
}

// (b) A call shares the line: its delay stays near the target.
func TestSimCall(t *testing.T) {
	s := newSim(t, 2)
	s.call = 0.3e6
	s.run(20*time.Second, false)
	got := s.throughput(3 * time.Minute)
	if p := p95(s.delays); p > s.c.target+15*time.Millisecond {
		t.Fatalf("the call's 95th percentile delay %v, want ≤ %v", p, s.c.target+15*time.Millisecond)
	}
	if left := s.capacity - s.call; got < 0.85*left {
		t.Fatalf("Downex %.2f MB/s, want ≥ 85%% of the %.2f left", got/1e6, left/1e6)
	}
}

// (c, d) A saturating competitor takes the line: Downex goes to the floor and never
// below; when it stops, Downex recovers or is released within 30 s.
func TestSimCompetitor(t *testing.T) {
	s := newSim(t, 3)
	s.run(20*time.Second, false)
	s.bulk = 1.05 * s.capacity
	for range 120 {
		s.run(time.Second, false)
		if s.c.State() == Slowed && s.dec.Rate < Floor {
			t.Fatalf("R %d below the floor", s.dec.Rate)
		}
	}
	if s.c.State() != Slowed || s.dec.Rate != Floor {
		t.Fatalf("with a saturating competitor: %v at %d, want SLOWED at the floor", s.c.State(), s.dec.Rate)
	}
	s.bulk = 0
	for i := 0; ; i++ {
		s.run(time.Second, false)
		if s.c.State() == Watching || s.dec.Rate >= int64(0.75*s.capacity) {
			break
		}
		if i >= 30 {
			t.Fatalf("30 s after the competitor stopped: %v at %d", s.c.State(), s.dec.Rate)
		}
	}
}

// (e) A path change of +40 ms reads as a queue until the base history forgets the old
// path: recovered within 11 minutes.
func TestSimPathChange(t *testing.T) {
	s := newSim(t, 4)
	s.run(time.Minute, false)
	s.base += 40 * time.Millisecond
	s.run(11*time.Minute-time.Minute, false)
	if got := s.throughput(time.Minute); got < 0.8*s.capacity {
		t.Fatalf("11 min after a path change Downex gets %.2f MB/s (%v at %d)", got/1e6, s.c.State(), s.dec.Rate)
	}
}

// (f) Every probe lost: NO_SIGNAL once d has been unknown for 10 s, and full speed.
func TestSimProbesLost(t *testing.T) {
	s := newSim(t, 5)
	s.run(30*time.Second, false)
	s.lostAll = true
	for i := 0; s.c.State() != NoSignal; i++ {
		if i >= int((FilterWindow+NoSignalAfter+Tick)/Tick) {
			t.Fatalf("not NO_SIGNAL %v after the probes stopped: %v", FilterWindow+NoSignalAfter, s.c.State())
		}
		s.run(Tick, false)
	}
	if s.dec.Rate != 0 {
		t.Fatalf("NO_SIGNAL with a cap of %d", s.dec.Rate)
	}
	s.run(time.Minute, false)
	if s.c.State() != NoSignal || s.dec.Rate != 0 {
		t.Fatalf("a minute later: %v at %d", s.c.State(), s.dec.Rate)
	}
}

// (g) The connections move to a server first probed under load (its base too high): it
// does not stall Downex, a calibration learns its base, and Polite mode works again.
func TestSimLatecomer(t *testing.T) {
	s := newSim(t, 6)
	s.peers = s.peers[:1]
	s.run(30*time.Second, false)
	s.peers = []Peer{{Addr: netip.MustParseAddrPort("192.0.2.200:443"), Conns: 8, First: s.now}}
	s.run(time.Minute, true)
	a := s.c.anchors.m[netip.MustParseAddr("192.0.2.200")]
	if a == nil || !a.trusted {
		t.Fatal("the latecomer is not trusted a minute later")
	}
	if b, _ := a.base(s.now); b > s.base+10*time.Millisecond {
		t.Fatalf("the latecomer's base %v, want near %v", b, s.base)
	}
	if s.floorT > 2*CalibrateMax {
		t.Fatalf("%v at the floor in a minute", s.floorT)
	}
	s.delays = nil
	s.run(time.Minute, true)
	if p := p95(s.delays); p > s.c.target+10*time.Millisecond {
		t.Fatalf("95th percentile queue delay %v after the switch", p)
	}
}
