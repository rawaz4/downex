package state

import (
	"sync"

	downexv1 "downex/internal/rpc/downex/v1"
)

// PoliteOptions are what a PoliteStore works with.
type PoliteOptions struct {
	Events Events
}

// PoliteStore is Polite mode's status as downexd streams it (docs/POLITE-SPEC.md §7): Watch
// of every download carries a polite event in its first batch and after every change. Before
// the first one (an older downexd sends none) the status is unknown and the window goes by
// the setting alone.
type PoliteStore struct {
	Changes Signal

	events Events

	mu      sync.Mutex
	unsub   func()
	started bool
	status  *downexv1.PoliteStatus
}

// NewPolite makes the store; Start subscribes it.
func NewPolite(o PoliteOptions) *PoliteStore { return &PoliteStore{events: o.Events} }

// Start subscribes to the polite events.
func (p *PoliteStore) Start() {
	p.mu.Lock()
	if p.started {
		p.mu.Unlock()
		return
	}
	p.started = true
	p.mu.Unlock()
	if p.events != nil {
		unsub := p.events.Subscribe(p.Apply)
		p.mu.Lock()
		p.unsub = unsub
		p.mu.Unlock()
	}
}

// Dispose unsubscribes.
func (p *PoliteStore) Dispose() {
	p.mu.Lock()
	unsub := p.unsub
	p.unsub = nil
	p.mu.Unlock()
	if unsub != nil {
		unsub()
	}
}

// Apply takes one batch of events: the latest polite status wins.
func (p *PoliteStore) Apply(batch Batch) {
	changed := false
	p.mu.Lock()
	for _, ev := range batch {
		if k, ok := ev.GetKind().(*downexv1.Event_Polite); ok && k.Polite != nil {
			p.status = k.Polite
			changed = true
		}
	}
	p.mu.Unlock()
	if changed {
		p.Changes.Notify()
	}
}

// Status is the latest status, and whether downexd reported one.
func (p *PoliteStore) Status() (*downexv1.PoliteStatus, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status, p.status != nil
}
