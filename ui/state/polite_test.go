package state_test

import (
	"testing"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/state"
	"downex/ui/state/statetest"
)

func politeEvent(s downexv1.PoliteState, rate int64) *downexv1.Event {
	return &downexv1.Event{Kind: &downexv1.Event_Polite{Polite: &downexv1.PoliteStatus{State: s, RateLimit: rate, DelayMs: -1, BaseMs: -1}}}
}

// TestPoliteStore: unknown before downexd reports Polite mode's status; then the latest
// polite event of a batch wins, and other events change nothing.
func TestPoliteStore(t *testing.T) {
	d := statetest.NewDaemon()
	p := state.NewPolite(state.PoliteOptions{Events: &d.Events})
	p.Start()
	defer p.Dispose()
	changes := 0
	defer p.Changes.Subscribe(func() { changes++ })()

	if _, ok := p.Status(); ok {
		t.Fatal("a status before any event")
	}
	d.Emit(politeEvent(downexv1.PoliteState_POLITE_STATE_WATCHING, 0))
	if st, ok := p.Status(); !ok || st.GetState() != downexv1.PoliteState_POLITE_STATE_WATCHING || changes != 1 {
		t.Fatalf("after WATCHING: %v %v, %d changes", st, ok, changes)
	}
	d.Emit(politeEvent(downexv1.PoliteState_POLITE_STATE_SLOWED, 3_000_000), politeEvent(downexv1.PoliteState_POLITE_STATE_SLOWED, 2_400_000))
	if st, _ := p.Status(); st.GetRateLimit() != 2_400_000 || changes != 2 {
		t.Fatalf("after a batch of two: %v, %d changes", st, changes)
	}
	d.Emit(&downexv1.Event{Kind: &downexv1.Event_Removed{Removed: 7}})
	if changes != 2 {
		t.Fatalf("an unrelated event notified: %d changes", changes)
	}
	p.Dispose()
	d.Emit(politeEvent(downexv1.PoliteState_POLITE_STATE_OFF, 0))
	if st, _ := p.Status(); st.GetState() != downexv1.PoliteState_POLITE_STATE_SLOWED {
		t.Fatal("a disposed store still follows the events")
	}
}
