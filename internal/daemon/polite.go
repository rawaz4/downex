package daemon

import (
	"sync/atomic"

	"google.golang.org/protobuf/types/known/timestamppb"

	"downex/internal/limiter"
	"downex/internal/polite"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/transport"
)

// PeerSource lists the remote addresses downloads hold live connections to
// (transport.PeerTable, docs/POLITE-SPEC.md rule 1) and reports new ones.
type PeerSource interface {
	Snapshot() []transport.Peer
	OnNew(func())
}

var _ PeerSource = (*transport.PeerTable)(nil)

// politeState is Polite mode in the daemon (docs/POLITE-SPEC.md §5): the polite level of
// the limiter chain, the byte meter every run's link adds to, the monitor that sets the
// level's limit, and the latest status.
type politeState struct {
	link   *chainLink      // own limit = Polite mode's cap (0 = none), parent = the global limiter
	rx     atomic.Int64    // bytes passed by every run's link (rule 16)
	mon    *polite.Monitor // nil: no prober or no peers (UNAVAILABLE while on)
	status atomic.Pointer[polite.Status]
}

// newPolite creates Polite mode's level below the global limiter (before the queues,
// which hang below it) and its monitor; startPolite starts the monitor.
func (m *Manager) newPolite() {
	m.polite.link = newChainLink(0, m.global)
	s := m.settings.Polite
	st := polite.OffStatus(m.clock.Now())
	if m.cfg.PoliteProber == nil || m.cfg.Peers == nil {
		if s.Enabled {
			st = polite.UnavailableStatus(m.clock.Now())
		}
		m.polite.status.Store(&st)
		return
	}
	m.polite.status.Store(&st)
	m.polite.mon = polite.NewMonitor(polite.Config{
		Clock:         m.clock,
		Prober:        m.cfg.PoliteProber,
		Peers:         m.politePeers,
		Received:      m.polite.rx.Load,
		Busy:          m.politeBusy,
		Apply:         m.polite.link.SetLimit,
		Publish:       m.politePublished,
		Enabled:       s.Enabled,
		Target:        s.target(),
		AllowLoopback: m.cfg.PoliteLoopback,
		Logger:        m.log.With("component", "polite"),
	})
}

// startPolite starts the monitor (the end of New).
func (m *Manager) startPolite() {
	if mon := m.polite.mon; mon != nil {
		m.cfg.Peers.OnNew(mon.Wake)
		mon.Start()
	}
}

// stopPolite stops the monitor (first thing in Shutdown): no probe and no cap from here
// on.
func (m *Manager) stopPolite() {
	if mon := m.polite.mon; mon != nil {
		m.cfg.Peers.OnNew(nil)
		mon.Stop()
		m.polite.link.SetLimit(0)
	}
}

// configurePolite applies the Polite mode settings after UpdateSettings saved them
// (outside m.mu: the monitor reads politeBusy).
func (m *Manager) configurePolite(prev, next PoliteSettings) {
	if mon := m.polite.mon; mon != nil {
		mon.Configure(next.Enabled, next.target())
		return
	}
	if prev.Enabled == next.Enabled {
		return
	}
	st := polite.OffStatus(m.clock.Now())
	if next.Enabled {
		st = polite.UnavailableStatus(m.clock.Now())
	}
	m.politePublished(st)
}

// politeWake tells the monitor that a download started. It never blocks, so m.mu may be
// held.
func (m *Manager) politeWake() {
	if mon := m.polite.mon; mon != nil {
		mon.Wake()
	}
}

// politeBusy reports whether a download runs (the monitor calls it outside its lock).
func (m *Manager) politeBusy() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running > 0
}

// politePeers converts the transport's peers for the monitor, marking those through
// which a host with an engine connection penalty is reached (rule 4: no TCP probes to
// them). Host names stay here: the monitor sees addresses only.
func (m *Manager) politePeers() []polite.Peer {
	ps := m.cfg.Peers.Snapshot()
	out := make([]polite.Peer, len(ps))
	for i, p := range ps {
		out[i] = polite.Peer{Addr: p.Addr, Conns: p.Conns, First: p.First}
		for _, h := range p.Hosts {
			if m.eng.HostPenalized(h) {
				out[i].Penalized = true
				break
			}
		}
	}
	return out
}

// politePublished records a new status and streams it to the Watch subscribers of every
// download (rule 11).
func (m *Manager) politePublished(st polite.Status) {
	m.polite.status.Store(&st)
	m.hub.publishPolite()
}

// politeStatus returns the current status: the monitor's own, whose line delay is fresh
// at every tick (rule 11 throttles only what is published, so Status and the published
// events show the latest delay), else the latest published one. The monitor never calls
// back into the Manager under the lock Status takes, so m.mu may be held.
func (m *Manager) politeStatus() polite.Status {
	if mon := m.polite.mon; mon != nil {
		return mon.Status()
	}
	return *m.polite.status.Load()
}

// queueParent is the limiter every queue's level hangs below: Polite mode's level.
func (m *Manager) queueParent() limiter.Limiter { return m.polite.link }

// politeProto returns the latest status in its API form.
func (m *Manager) politeProto() *downexv1.PoliteStatus {
	st := m.politeStatus()
	p := &downexv1.PoliteStatus{
		State:     protoPoliteState(st.State),
		RateLimit: st.RateLimit,
		DelayMs:   st.DelayMs,
		BaseMs:    st.BaseMs,
	}
	if !st.Since.IsZero() {
		p.Since = timestamppb.New(st.Since)
	}
	return p
}

// politeEvent is Watch's polite event.
func (m *Manager) politeEvent() *downexv1.Event {
	return &downexv1.Event{Kind: &downexv1.Event_Polite{Polite: m.politeProto()}}
}

func protoPoliteState(s polite.State) downexv1.PoliteState {
	switch s {
	case polite.Off:
		return downexv1.PoliteState_POLITE_STATE_OFF
	case polite.Idle:
		return downexv1.PoliteState_POLITE_STATE_IDLE
	case polite.Watching:
		return downexv1.PoliteState_POLITE_STATE_WATCHING
	case polite.Slowed:
		return downexv1.PoliteState_POLITE_STATE_SLOWED
	case polite.Measuring:
		return downexv1.PoliteState_POLITE_STATE_MEASURING
	case polite.NoSignal:
		return downexv1.PoliteState_POLITE_STATE_NO_SIGNAL
	case polite.Unavailable:
		return downexv1.PoliteState_POLITE_STATE_UNAVAILABLE
	}
	return downexv1.PoliteState_POLITE_STATE_UNSPECIFIED
}
