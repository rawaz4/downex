package daemon

import (
	"sync"

	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
)

// eventKind is what changed about a download or queue since a subscriber's last batch.
type eventKind uint8

const (
	kindUpdated eventKind = iota + 1 // state or progress changed
	kindRemoved                      // the download or queue was removed
)

// hub fans change notifications out to Watch subscribers. Publishing never blocks:
// each subscriber keeps at most one pending entry per download and per queue (the
// latest kind wins, and removed is final) and the latest post-queue action event per
// queue, so its buffer is bounded by the number of downloads and queues however slowly
// it is drained, and the actual messages are built only when the subscriber sends its
// next batch. Queue, post-queue action, tools, update and polite events go to subscribers
// of every download only (a Watch without ids); a tools or polite event carries the status
// when the batch is built, so one pending flag is enough, and an update event the latest
// check's result.
type hub struct {
	mu     sync.Mutex
	subs   map[*subscriber]struct{}
	closed bool
	done   chan struct{} // closed by close
}

// subscriber is one Watch stream.
type subscriber struct {
	ids     map[model.DownloadID]bool // nil = every download (and the queues)
	pending map[model.DownloadID]eventKind
	queues  map[model.QueueID]eventKind
	posts   map[model.QueueID]*downexv1.PostActionPending
	tools   bool                 // the tools status changed (docs/SITE-MEDIA-SPEC.md §3.1)
	update  *downexv1.UpdateInfo // an automatic update check's result (docs/M6-SPEC.md §4)
	polite  bool                 // Polite mode's status changed (docs/POLITE-SPEC.md §7)
	quit    bool                 // the user quit Downex: the windows close
	signal  chan struct{}        // capacity 1: something became pending
}

// pendingBatch is what a subscriber has to send.
type pendingBatch struct {
	downloads map[model.DownloadID]eventKind
	queues    map[model.QueueID]eventKind
	posts     map[model.QueueID]*downexv1.PostActionPending
	tools     bool
	update    *downexv1.UpdateInfo // the latest automatic update check's result, if one ran since
	polite    bool
	quit      bool
}

func (b pendingBatch) empty() bool {
	return len(b.downloads) == 0 && len(b.queues) == 0 && len(b.posts) == 0 && !b.tools && b.update == nil && !b.polite && !b.quit
}

func newHub() *hub {
	return &hub{subs: make(map[*subscriber]struct{}), done: make(chan struct{})}
}

// subscribe registers a subscriber for ids (nil = all). It returns false once the hub
// is closed.
func (h *hub) subscribe(ids map[model.DownloadID]bool) (*subscriber, bool) {
	s := &subscriber{
		ids: ids, pending: make(map[model.DownloadID]eventKind), queues: make(map[model.QueueID]eventKind),
		posts: make(map[model.QueueID]*downexv1.PostActionPending), signal: make(chan struct{}, 1),
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, false
	}
	h.subs[s] = struct{}{}
	return s, true
}

func (h *hub) unsubscribe(s *subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subs, s)
}

func (s *subscriber) wake() {
	select {
	case s.signal <- struct{}{}:
	default:
	}
}

// publish records that download id changed for every interested subscriber and wakes
// them. It never blocks on a subscriber.
func (h *hub) publish(id model.DownloadID, kind eventKind) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		if s.ids != nil && !s.ids[id] {
			continue
		}
		if s.pending[id] == kindRemoved {
			continue
		}
		s.pending[id] = kind
		s.wake()
	}
}

// publishQueue records that queue id changed (or was removed) for the subscribers of
// every download.
func (h *hub) publishQueue(id model.QueueID, kind eventKind) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		if s.ids != nil || s.queues[id] == kindRemoved {
			continue
		}
		s.queues[id] = kind
		s.wake()
	}
}

// publishPost records a post-queue action event (a countdown, or its cancellation) for
// the subscribers of every download.
func (h *hub) publishPost(p *downexv1.PostActionPending) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		if s.ids != nil {
			continue
		}
		s.posts[model.QueueID(p.GetQueueId())] = p
		s.wake()
	}
}

// publishTools records that the status of the tools changed for the subscribers of every
// download.
func (h *hub) publishTools() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		if s.ids != nil {
			continue
		}
		s.tools = true
		s.wake()
	}
}

// publishUpdate records the result of an automatic update check for the subscribers of
// every download (docs/M6-SPEC.md §4); a later one replaces it.
func (h *hub) publishUpdate(info *downexv1.UpdateInfo) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		if s.ids != nil {
			continue
		}
		s.update = info
		s.wake()
	}
}

// publishQuit tells every subscriber, with ids or without, that the user quit Downex: the
// windows close. A stream that ends at Shutdown still sends it (finalBatch).
func (h *hub) publishQuit() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		s.quit = true
		s.wake()
	}
}

// publishPolite records that Polite mode's status changed for the subscribers of every
// download (docs/POLITE-SPEC.md §7).
func (h *hub) publishPolite() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		if s.ids != nil {
			continue
		}
		s.polite = true
		s.wake()
	}
}

// take returns and clears the subscriber's pending changes.
func (h *hub) take(s *subscriber) pendingBatch {
	h.mu.Lock()
	defer h.mu.Unlock()
	b := pendingBatch{downloads: s.pending, queues: s.queues, posts: s.posts, tools: s.tools, update: s.update, polite: s.polite, quit: s.quit}
	s.tools, s.update, s.polite, s.quit = false, nil, false, false
	if len(s.pending) > 0 {
		s.pending = make(map[model.DownloadID]eventKind, len(b.downloads))
	} else {
		b.downloads = nil
	}
	if len(s.queues) > 0 {
		s.queues = make(map[model.QueueID]eventKind)
	} else {
		b.queues = nil
	}
	if len(s.posts) > 0 {
		s.posts = make(map[model.QueueID]*downexv1.PostActionPending)
	} else {
		b.posts = nil
	}
	return b
}

// close ends every subscription: done is closed and later subscribe calls fail.
func (h *hub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.closed {
		h.closed = true
		close(h.done)
	}
}
