package host

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"time"

	"connectrpc.com/connect"

	"downex/internal/client"
	downexv1 "downex/internal/rpc/downex/v1"
)

// The default reconnect delays of a Bridge.
const (
	BridgeMinBackoff = time.Second
	BridgeMaxBackoff = 10 * time.Second
)

// Bridge forwards DownloadService.Watch to the window: every WatchResponse reaches Emit
// as it arrives (downexd sends at most four batches per second).
//
// When the stream ends (the daemon stopped, restarted or crashed) the bridge subscribes
// again after a delay that starts at Min and doubles up to Max while subscribing keeps
// failing. Each new stream starts with a full snapshot of every download; the bridge
// adds a removed event for each download it had reported that is missing from it, so
// the window's list stays exact across outages.
//
// A bridge with IDs (a progress window's) watches those downloads only. When the daemon
// refuses that because one of them does not exist (not_found), the bridge reports them
// as removed and keeps trying, in case a restored daemon knows them again.
//
// A Bridge runs once (Run or Start); its fields are not changed while it runs.
type Bridge struct {
	Daemon Daemon
	IDs    []int64
	// Emit receives each batch on the bridge's goroutine; the batch is the receiver's.
	Emit func(*downexv1.WatchResponse)
	// Log may be nil.
	Log *slog.Logger
	// Min and Max bound the reconnect delay (BridgeMinBackoff and BridgeMaxBackoff when
	// zero; a Max below Min is Min).
	Min, Max time.Duration
	// Sleep waits between subscriptions (client.Sleep when nil).
	Sleep func(ctx context.Context, d time.Duration) bool

	// known holds the ids reported since the last snapshot (Run's goroutine only).
	known map[int64]struct{}
}

// Start runs the bridge on a goroutine of its own until ctx ends or stop is called. stop
// cancels it and waits for it for at most ShutdownWait; once stop has returned in time,
// Emit is not called again.
func (b *Bridge) Start(ctx context.Context) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.Run(ctx)
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(ShutdownWait):
				b.logger().Warn("the events bridge did not stop in time")
			}
		})
	}
}

func (b *Bridge) logger() *slog.Logger {
	if b.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return b.Log
}

// Run forwards events until ctx ends.
func (b *Bridge) Run(ctx context.Context) {
	sleep := b.Sleep
	if sleep == nil {
		sleep = client.Sleep
	}
	lo, hi := b.Min, b.Max
	if lo <= 0 {
		lo = BridgeMinBackoff
	}
	if hi <= 0 {
		hi = BridgeMaxBackoff
	}
	hi = max(hi, lo)
	log := b.logger()
	delay := lo
	for {
		snapshot, err := b.session(ctx, log)
		if ctx.Err() != nil {
			return
		}
		if snapshot {
			delay = lo
		}
		log.Info("download events interrupted; subscribing again", "error", err, "delay", delay)
		if !sleep(ctx, delay) {
			return
		}
		delay = min(2*delay, hi)
	}
}

// session subscribes once and forwards the stream until it ends. It reports whether the
// stream delivered its snapshot, and why it ended.
func (b *Bridge) session(ctx context.Context, log *slog.Logger) (snapshot bool, err error) {
	s, err := b.Daemon.Watch(ctx, b.IDs)
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
			log.Info("receiving download events", "downloads", len(b.known))
		} else {
			b.track(msg.GetEvents())
		}
		b.Emit(msg)
	}
	err = s.Err()
	if !snapshot {
		b.gone(err)
	}
	return snapshot, err
}

// gone reports the watched downloads as removed when the daemon refused to watch them
// because one of them does not exist (a Connect stream reports that from Receive).
func (b *Bridge) gone(err error) {
	if len(b.IDs) == 0 || connect.CodeOf(err) != connect.CodeNotFound {
		return
	}
	events := make([]*downexv1.Event, 0, len(b.IDs))
	for _, id := range b.IDs {
		events = append(events, &downexv1.Event{Kind: &downexv1.Event_Removed{Removed: id}})
	}
	b.known = nil
	b.Emit(&downexv1.WatchResponse{Events: events})
}

// reconcile takes a stream's first batch, the snapshot, as the full set of downloads.
// Downloads reported before that are missing from it were removed meanwhile: it returns
// the batch with a removed event for each of them.
func (b *Bridge) reconcile(msg *downexv1.WatchResponse) *downexv1.WatchResponse {
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
func (b *Bridge) track(events []*downexv1.Event) {
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
