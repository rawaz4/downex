package daemon

import (
	"context"
	"slices"
	"time"

	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
)

// minBatchInterval spaces the batches of one Watch stream: at most four per second
// (docs/DAEMON-SPEC.md §5).
const minBatchInterval = 250 * time.Millisecond

// Watch streams the downloads with the given ids (empty = all) to send. The first
// batch holds one snapshot event per watched download (it is sent even when empty,
// so it marks the end of the snapshot), or a removed event for a watched id whose
// download was removed meanwhile. Later batches hold updated events (a download
// added after the snapshot first appears as updated) and removed events, coalesced to
// the latest state of each download, at most four batches per second.
//
// A Watch of every download (no ids) also streams the queues (docs/M6-SPEC.md §1): its
// first batch ends with a queue_updated event per queue and a post_action event per
// countdown in progress; later batches carry queue_updated, queue_removed and
// post_action events (the latest per queue) after the download events. With
// Config.Tools it streams the tools too (docs/SITE-MEDIA-SPEC.md §3.1): the first batch
// ends with a tools event, and a later batch ends with one after the status changed. Once an
// automatic update check has run it streams its result (docs/M6-SPEC.md §4): the first batch
// ends with an update event, and a later batch ends with one after each automatic check.
// It streams Polite mode's status (docs/POLITE-SPEC.md §7): the first batch ends with a
// polite event (after tools and update), and a later batch ends with one after the status
// changed.
//
// Watch returns when ctx ends, send fails, or the manager shuts down, which ends the
// stream with CodeUnavailable after a last batch. A slow send only delays this
// stream: changes keep coalescing meanwhile and downloads never wait for it.
func (m *Manager) Watch(ctx context.Context, ids []int64, send func(*downexv1.WatchResponse) error) error {
	var filter map[model.DownloadID]bool
	if len(ids) > 0 {
		filter = make(map[model.DownloadID]bool, len(ids))
		for _, id := range ids {
			if _, err := m.lookup(ctx, model.DownloadID(id)); err != nil {
				return err
			}
			filter[model.DownloadID(id)] = true
		}
	}
	// Subscribe before taking the snapshot so that no change in between is lost; one
	// that is already in the snapshot is merely sent again.
	sub, ok := m.hub.subscribe(filter)
	if !ok {
		return errShuttingDown()
	}
	defer m.hub.unsubscribe(sub)

	snap, err := m.snapshot(ctx, filter)
	if err != nil {
		return err
	}
	if err := send(&downexv1.WatchResponse{Events: snap}); err != nil {
		return err
	}
	last := time.Now()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-m.hub.done:
			return m.finalBatch(sub, send)
		case <-sub.signal:
		}
		if wait := minBatchInterval - time.Since(last); wait > 0 {
			t := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				t.Stop()
				return ctx.Err()
			case <-m.hub.done:
				t.Stop()
				return m.finalBatch(sub, send)
			case <-t.C:
			}
		}
		events := m.batchEvents(m.hub.take(sub))
		if len(events) == 0 {
			continue
		}
		if err := send(&downexv1.WatchResponse{Events: events}); err != nil {
			return err
		}
		last = time.Now()
	}
}

// finalBatch sends the changes still pending when the manager shut down (the paused
// states of the stopped downloads) and ends the stream.
func (m *Manager) finalBatch(sub *subscriber, send func(*downexv1.WatchResponse) error) error {
	if events := m.batchEvents(m.hub.take(sub)); len(events) > 0 {
		_ = send(&downexv1.WatchResponse{Events: events})
	}
	return errShuttingDown()
}

// snapshot returns a snapshot event per watched download, ordered by id; a watched id
// whose download is gone gets a removed event instead.
func (m *Manager) snapshot(ctx context.Context, filter map[model.DownloadID]bool) ([]*downexv1.Event, error) {
	if filter == nil {
		all, err := m.List(ctx)
		if err != nil {
			return nil, err
		}
		tools := m.toolsEvent() // read outside m.mu: Config.Tools has locks of its own
		upd := m.updateEvent()
		pol := m.politeEvent()
		out := make([]*downexv1.Event, len(all))
		for i, d := range all {
			out[i] = &downexv1.Event{Kind: &downexv1.Event_Snapshot{Snapshot: d}}
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		for _, q := range m.sortedQueuesLocked() {
			out = append(out, &downexv1.Event{Kind: &downexv1.Event_QueueUpdated{QueueUpdated: m.protoQueueLocked(q)}})
		}
		for _, p := range m.pendingPostsLocked() {
			out = append(out, &downexv1.Event{Kind: &downexv1.Event_PostAction{PostAction: p}})
		}
		if tools != nil {
			out = append(out, tools)
		}
		if upd != nil {
			out = append(out, upd)
		}
		return append(out, pol), nil
	}
	ids := make([]model.DownloadID, 0, len(filter))
	for id := range filter {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*downexv1.Event, 0, len(ids))
	for _, id := range ids {
		if e := m.entries[id]; e != nil {
			out = append(out, &downexv1.Event{Kind: &downexv1.Event_Snapshot{Snapshot: m.protoLocked(e)}})
		} else {
			// Removed after Watch looked it up but before it subscribed: the removed
			// event went to earlier subscribers only, so report it here.
			out = append(out, &downexv1.Event{Kind: &downexv1.Event_Removed{Removed: int64(id)}})
		}
	}
	return out, nil
}

// batchEvents renders pending changes as events: downloads, then queues, then
// post-queue actions, each ordered by id, then the tools status, then the update check,
// then Polite mode's status. An update of a download or queue that no longer exists is
// dropped: its removal is (or was) reported.
func (m *Manager) batchEvents(b pendingBatch) []*downexv1.Event {
	if b.empty() {
		return nil
	}
	var tools *downexv1.Event
	if b.tools {
		tools = m.toolsEvent() // outside m.mu
	}
	ids := make([]model.DownloadID, 0, len(b.downloads))
	for id := range b.downloads {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	qids := make([]model.QueueID, 0, len(b.queues))
	for id := range b.queues {
		qids = append(qids, id)
	}
	slices.Sort(qids)
	pids := make([]model.QueueID, 0, len(b.posts))
	for id := range b.posts {
		pids = append(pids, id)
	}
	slices.Sort(pids)
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*downexv1.Event, 0, len(ids)+len(qids)+len(pids))
	for _, id := range ids {
		switch b.downloads[id] {
		case kindRemoved:
			out = append(out, &downexv1.Event{Kind: &downexv1.Event_Removed{Removed: int64(id)}})
		case kindUpdated:
			if e := m.entries[id]; e != nil {
				out = append(out, &downexv1.Event{Kind: &downexv1.Event_Updated{Updated: m.protoLocked(e)}})
			}
		}
	}
	for _, id := range qids {
		switch b.queues[id] {
		case kindRemoved:
			out = append(out, &downexv1.Event{Kind: &downexv1.Event_QueueRemoved{QueueRemoved: int64(id)}})
		case kindUpdated:
			if q := m.queues[id]; q != nil {
				out = append(out, &downexv1.Event{Kind: &downexv1.Event_QueueUpdated{QueueUpdated: m.protoQueueLocked(q)}})
			}
		}
	}
	for _, id := range pids {
		out = append(out, &downexv1.Event{Kind: &downexv1.Event_PostAction{PostAction: b.posts[id]}})
	}
	if tools != nil {
		out = append(out, tools)
	}
	if b.update != nil {
		out = append(out, &downexv1.Event{Kind: &downexv1.Event_Update{Update: b.update}})
	}
	if b.polite {
		out = append(out, m.politeEvent())
	}
	return out
}
