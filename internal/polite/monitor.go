package polite

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"downex/internal/engine"
)

// Config configures a Monitor. Prober, Peers, Received and Busy are required.
type Config struct {
	// Clock times the ticks and stamps the samples; nil selects engine.RealClock.
	Clock engine.Clock
	// Prober sends the probes.
	Prober Prober
	// Peers returns the live peers (transport.PeerTable through the daemon).
	Peers func() []Peer
	// Received returns the byte counter of every download (rule 16).
	Received func() int64
	// Busy reports whether a download runs.
	Busy func() bool
	// Apply sets the polite level of the limiter chain (0 = no cap). It is called only
	// when the cap changes enough (rule 11), never under the monitor's lock, and never
	// concurrently with itself or Publish.
	Apply func(rate int64)
	// Publish reports a new status (rule 11), under the same rules as Apply.
	Publish func(Status)
	// Enabled and Target are the initial settings (Configure changes them).
	Enabled bool
	Target  time.Duration
	// AllowLoopback makes loopback addresses eligible anchors (end-to-end tests only).
	AllowLoopback bool
	// Logger receives the state changes (default: discarded). Addresses are never logged.
	Logger *slog.Logger
}

// Monitor runs a Controller: a tick every Tick and a probe every ProbeSpacing while
// Polite mode is on and downloads run, nothing at all otherwise (no ticker, no probe;
// Wake starts it again when a download starts). Its methods are safe for concurrent use.
type Monitor struct {
	cfg   Config
	clock engine.Clock
	log   *slog.Logger

	mu      sync.Mutex // guards ctrl and gen
	ctrl    *Controller
	gen     uint64 // counts Configure calls that switched on or off
	emitMu  sync.Mutex
	em      emitter // guarded by emitMu
	kick    chan struct{}
	results chan probeResult
	// lastProbe is when the latest probe was sent (the run goroutine's own).
	lastProbe time.Time

	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	stopOnce sync.Once
}

type probeResult struct {
	p   Probe
	rtt time.Duration
	err error
}

// NewMonitor returns a monitor for cfg. It calls nothing and starts nothing until Start.
func NewMonitor(cfg Config) *Monitor {
	if cfg.Clock == nil {
		cfg.Clock = engine.RealClock()
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	m := &Monitor{
		cfg: cfg, clock: cfg.Clock, log: cfg.Logger, ctrl: NewController(cfg.Target),
		kick: make(chan struct{}, 1), results: make(chan probeResult, MaxAnchors),
	}
	m.ctrl.AllowLoopback(cfg.AllowLoopback)
	m.ctx, m.cancel = context.WithCancel(context.Background())
	return m
}

// Start publishes the first status, applies Config.Enabled and starts the monitor's
// goroutine. Call it once.
func (m *Monitor) Start() {
	m.emitStatus(m.Status(), m.clock.Now()) // Off
	m.wg.Add(1)
	go m.run()
	if m.cfg.Enabled {
		m.Configure(true, m.cfg.Target)
	}
}

// Configure switches Polite mode on or off and sets its target (0 = DefaultTarget).
// Switching off removes the cap and publishes Off before it returns (rule 15).
func (m *Monitor) Configure(enabled bool, target time.Duration) {
	var rx int64
	var busy bool
	if enabled {
		rx, busy = m.cfg.Received(), m.cfg.Busy()
	}
	now := m.clock.Now()
	m.mu.Lock()
	m.ctrl.SetTarget(target)
	switched := enabled != m.ctrl.enabled
	if switched {
		m.ctrl.Enable(now, enabled, rx, busy)
		m.gen++
	}
	gen := m.gen
	d := m.ctrl.decision()
	m.mu.Unlock()
	if switched {
		m.emit(gen, now, d)
		m.Wake()
	}
}

// Wake makes a monitor that is on look again: the daemon calls it when a download starts
// (an idle monitor starts ticking) and when a connection to a new address is made (a
// running monitor probes it at once).
func (m *Monitor) Wake() {
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

// Status returns the current status.
func (m *Monitor) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ctrl.Status()
}

// Stop ends the monitor and waits for its goroutines, a probe in flight included (at
// most ProbeTimeout). It does not close the Prober.
func (m *Monitor) Stop() {
	m.stopOnce.Do(func() {
		m.cancel()
		m.wg.Wait()
	})
}

func (m *Monitor) run() {
	defer m.wg.Done()
	var tick, probe engine.Ticker
	stop := func() {
		if tick != nil {
			tick.Stop()
			probe.Stop()
			tick, probe = nil, nil
		}
	}
	defer stop()
	for {
		var tickC, probeC <-chan time.Time
		if tick != nil {
			tickC, probeC = tick.C(), probe.C()
		}
		select {
		case <-m.ctx.Done():
			return
		case <-m.kick:
			switch {
			case tick != nil:
				// A new peer while running: probe it now, before its download loads the
				// line, so that its first sample can be trusted (rule 7a).
				m.refreshPeers()
				m.sendProbe()
			case m.step():
				tick, probe = m.clock.NewTicker(Tick), m.clock.NewTicker(ProbeSpacing)
				m.sendProbe()
			}
		case <-tickC:
			if !m.step() {
				stop()
			}
		case <-probeC:
			m.sendProbe()
		case r := <-m.results:
			m.mu.Lock()
			m.ctrl.ProbeDone(r.p, r.rtt, r.err)
			m.mu.Unlock()
		}
	}
}

// step runs one tick and reports whether the monitor must keep ticking (on, and
// downloads run).
func (m *Monitor) step() bool {
	m.mu.Lock()
	on := m.ctrl.enabled
	m.mu.Unlock()
	if !on {
		return false
	}
	busy := m.cfg.Busy()
	rx := m.cfg.Received()
	var peers []Peer
	if busy {
		peers = m.cfg.Peers()
	}
	now := m.clock.Now()
	m.mu.Lock()
	if !m.ctrl.enabled { // switched off meanwhile
		m.mu.Unlock()
		return false
	}
	if busy {
		m.ctrl.SetPeers(now, peers)
	}
	d := m.ctrl.Step(now, rx, busy)
	active := m.ctrl.probing()
	gen := m.gen
	m.mu.Unlock()
	m.emit(gen, now, d)
	return active
}

// refreshPeers gives the controller the live peers between two ticks.
func (m *Monitor) refreshPeers() {
	if !m.cfg.Busy() {
		return
	}
	peers := m.cfg.Peers()
	now := m.clock.Now()
	m.mu.Lock()
	if m.ctrl.probing() {
		m.ctrl.SetPeers(now, peers)
	}
	m.mu.Unlock()
}

// sendProbe starts the next probe, if any, on its own goroutine (at most MaxAnchors in
// flight, one per anchor: the Controller decides), at most one per ProbeSpacing.
func (m *Monitor) sendProbe() {
	now := m.clock.Now()
	if !m.lastProbe.IsZero() && now.Sub(m.lastProbe) < ProbeSpacing {
		return
	}
	rx := m.cfg.Received()
	m.mu.Lock()
	p, ok := m.ctrl.NextProbe(now, rx)
	m.mu.Unlock()
	if !ok {
		return
	}
	m.lastProbe = now
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ctx, cancel := context.WithTimeout(m.ctx, ProbeTimeout)
		var r probeResult
		r.p = p
		if p.ICMP {
			r.rtt, r.err = m.cfg.Prober.ProbeICMP(ctx, p.Addr.Addr())
		} else {
			r.rtt, r.err = m.cfg.Prober.ProbeTCP(ctx, p.Addr)
		}
		cancel()
		select {
		case m.results <- r:
		case <-m.ctx.Done():
		}
	}()
}

// emit passes decision d of configuration generation gen on to Apply and Publish as rule
// 11 says, unless a Configure switched the mode since (it emitted its own).
func (m *Monitor) emit(gen uint64, now time.Time, d Decision) {
	m.emitMu.Lock()
	defer m.emitMu.Unlock()
	m.mu.Lock()
	stale := gen != m.gen
	m.mu.Unlock()
	if stale {
		return
	}
	if m.em.apply(d.Rate) && m.cfg.Apply != nil {
		m.cfg.Apply(d.Rate)
	}
	m.publishLocked(now, d.Status)
}

// emitStatus publishes st (the first status).
func (m *Monitor) emitStatus(st Status, now time.Time) {
	m.emitMu.Lock()
	defer m.emitMu.Unlock()
	m.publishLocked(now, st)
}

func (m *Monitor) publishLocked(now time.Time, st Status) {
	prev, had := m.em.pub, m.em.published
	if !m.em.publish(now, st) {
		return
	}
	if !had || prev.State != st.State {
		m.log.Info("polite mode", "state", st.State.String(), "rate_limit", st.RateLimit, "delay_ms", st.DelayMs)
	}
	if m.cfg.Publish != nil {
		m.cfg.Publish(st)
	}
}
