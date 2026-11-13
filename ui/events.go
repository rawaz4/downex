package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"time"

	"connectrpc.com/connect"

	"downex/internal/client"
	downexv1 "downex/internal/rpc/downex/v1"
)

// eventBridge forwards DownloadService.Watch to the frontend: every WatchResponse
// becomes one "downex:events" event with the protojson WatchResponse as its payload
// ({events: [{snapshot}|{updated}|{removed}]}), as often as the daemon sends them (at
// most four batches per second).
//
// When the stream ends (the daemon stopped, restarted or crashed) the bridge
// subscribes again after a delay that starts at min and doubles up to max while
// subscribing keeps failing. Each new stream starts with a full snapshot of every
// download; the bridge adds a removed event for each download it had reported that
// is missing from it, so the frontend's list stays exact across outages.
//
// A bridge with ids (a progress window's) watches those downloads only. When the daemon
// refuses that because one of them does not exist (not_found), the bridge reports them
// as removed and keeps trying, in case a restored daemon knows them again.
type eventBridge struct {
	daemon   Daemon
	ids      []int64
	emit     func(json.RawMessage)
	log      *slog.Logger
	min, max time.Duration
	// sleep waits between subscriptions (client.Sleep when nil).
	sleep func(ctx context.Context, d time.Duration) bool

	// known holds the ids reported since the last snapshot (run's goroutine only).
	known map[int64]struct{}
}

// run forwards events until ctx ends.
func (b *eventBridge) run(ctx context.Context) {
	sleep := b.sleep
	if sleep == nil {
		sleep = client.Sleep
	}
	delay := b.min
	for {
		snapshot, err := b.session(ctx)
		if ctx.Err() != nil {
			return
		}
		if snapshot {
			delay = b.min
		}
		b.log.Info("download events interrupted; subscribing again", "error", err, "delay", delay)
		if !sleep(ctx, delay) {
			return
		}
		delay = min(2*delay, b.max)
	}
}

// session subscribes once and forwards the stream until it ends. It reports whether
// the stream delivered its snapshot, and why it ended.
func (b *eventBridge) session(ctx context.Context) (snapshot bool, err error) {
	s, err := b.daemon.Watch(ctx, b.ids)
	if err != nil {
		b.gone(err)
		return false, err
	}
	defer func() { _ = s.Close() }()
	for s.Receive() {
		msg := s.Msg()
		if !snapshot {
			msg = b.reconcile(msg)
			snapshot = true
			b.log.Info("receiving download events", "downloads", len(b.known))
		} else {
			b.track(msg.GetEvents())
		}
		payload, err := marshalOptions.Marshal(msg)
		if err != nil {
			b.log.Error("cannot encode download events", "error", err)
			continue
		}
		b.emit(payload)
	}
	err = s.Err()
	if !snapshot {
		b.gone(err)
	}
	return snapshot, err
}

// gone reports the watched downloads as removed when the daemon refused to watch them
// because one of them does not exist (a Connect stream reports that from Receive).
func (b *eventBridge) gone(err error) {
	if len(b.ids) == 0 || connect.CodeOf(err) != connect.CodeNotFound {
		return
	}
	events := make([]*downexv1.Event, 0, len(b.ids))
	for _, id := range b.ids {
		events = append(events, &downexv1.Event{Kind: &downexv1.Event_Removed{Removed: id}})
	}
	b.known = nil
	payload, merr := marshalOptions.Marshal(&downexv1.WatchResponse{Events: events})
	if merr != nil {
		b.log.Error("cannot encode download events", "error", merr)
		return
	}
	b.emit(payload)
}

// reconcile takes a stream's first batch, the snapshot, as the full set of downloads.
// Downloads reported before that are missing from it were removed meanwhile: it
// returns the batch with a removed event for each of them.
func (b *eventBridge) reconcile(msg *downexv1.WatchResponse) *downexv1.WatchResponse {
	prev := b.known
	b.known = make(map[int64]struct{}, len(msg.GetEvents()))
	b.track(msg.GetEvents())
	var gone []int64
	for id := range prev {
		if _, ok := b.known[id]; !ok {
			gone = append(gone, id)
		}
	}
	if len(gone) == 0 {
		return msg
	}
	slices.Sort(gone)
	events := slices.Clone(msg.GetEvents())
	for _, id := range gone {
		events = append(events, &downexv1.Event{Kind: &downexv1.Event_Removed{Removed: id}})
	}
	return &downexv1.WatchResponse{Events: events}
}

// track records the downloads that events report or remove.
func (b *eventBridge) track(events []*downexv1.Event) {
	if b.known == nil {
		b.known = make(map[int64]struct{})
	}
	for _, e := range events {
		switch k := e.GetKind().(type) {
		case *downexv1.Event_Snapshot:
			b.known[k.Snapshot.GetId()] = struct{}{}
		case *downexv1.Event_Updated:
			b.known[k.Updated.GetId()] = struct{}{}
		case *downexv1.Event_Removed:
			delete(b.known, k.Removed)
		}
	}
}
