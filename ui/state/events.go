package state

import (
	"sync"

	downexv1 "downex/internal/rpc/downex/v1"
)

// Batch is one Watch batch (downexd sends at most four a second). A store never modifies
// a batch or the messages in it.
type Batch = []*downexv1.Event

// Events is where the stores subscribe to the Watch batches (today's Api.onEvents).
type Events interface {
	// Subscribe calls f with every batch, on the goroutine that publishes it, until
	// cancel is called.
	Subscribe(f func(Batch)) (cancel func())
}

// Hub fans the batches of one event bridge out to the stores: hand Emit to
// host.Session.Start (or host.Bridge.Emit). The zero Hub is ready.
type Hub struct {
	mu   sync.Mutex
	next int
	subs map[int]func(Batch)
	// order keeps the subscribers in the order they subscribed.
	order []int
}

// Emit publishes the batch of a WatchResponse.
func (h *Hub) Emit(resp *downexv1.WatchResponse) { h.Publish(resp.GetEvents()) }

// Publish calls every subscriber with batch, in the order they subscribed.
func (h *Hub) Publish(batch Batch) {
	h.mu.Lock()
	subs := make([]func(Batch), 0, len(h.order))
	for _, id := range h.order {
		subs = append(subs, h.subs[id])
	}
	h.mu.Unlock()
	for _, f := range subs {
		f(batch)
	}
}

// Subscribe implements Events.
func (h *Hub) Subscribe(f func(Batch)) (cancel func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subs == nil {
		h.subs = map[int]func(Batch){}
	}
	id := h.next
	h.next++
	h.subs[id] = f
	h.order = append(h.order, id)
	var once sync.Once
	return func() {
		once.Do(func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			delete(h.subs, id)
			for i, x := range h.order {
				if x == id {
					h.order = append(h.order[:i:i], h.order[i+1:]...)
					break
				}
			}
		})
	}
}
