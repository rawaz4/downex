package host

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	downexv1 "downex/internal/rpc/downex/v1"
)

func snapshotOf(ids ...int64) *downexv1.WatchResponse {
	r := &downexv1.WatchResponse{}
	for _, id := range ids {
		r.Events = append(r.Events, &downexv1.Event{Kind: &downexv1.Event_Snapshot{Snapshot: &downexv1.Download{
			Id: id, Size: -1, State: downexv1.DownloadState_DOWNLOAD_STATE_QUEUED,
		}}})
	}
	return r
}

func updated(id, received int64) *downexv1.WatchResponse {
	return &downexv1.WatchResponse{Events: []*downexv1.Event{{Kind: &downexv1.Event_Updated{Updated: &downexv1.Download{
		Id: id, Size: 100, Received: received, State: downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING, SpeedBps: 2.5,
	}}}}}
}

func removed(id int64) *downexv1.WatchResponse {
	return &downexv1.WatchResponse{Events: []*downexv1.Event{{Kind: &downexv1.Event_Removed{Removed: id}}}}
}

// scriptedWatch serves Watch from a list of sessions: each returns a stream (or an error);
// after the last, Watch blocks until ctx ends.
type scriptedWatch struct {
	mu       sync.Mutex
	sessions []func(ctx context.Context) (WatchStream, error)
	streams  []*fakeStream
}

func (s *scriptedWatch) watch(ctx context.Context) (WatchStream, error) {
	s.mu.Lock()
	var next func(ctx context.Context) (WatchStream, error)
	if len(s.sessions) > 0 {
		next, s.sessions = s.sessions[0], s.sessions[1:]
	}
	s.mu.Unlock()
	if next == nil {
		next = stream(nil) // the daemon stays up: wait for the end
	}
	ws, err := next(ctx)
	if fs, ok := ws.(*fakeStream); ok {
		s.mu.Lock()
		s.streams = append(s.streams, fs)
		s.mu.Unlock()
	}
	return ws, err
}

func (s *scriptedWatch) stream(i int) *fakeStream {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.streams[i]
}

func stream(end error, batches ...*downexv1.WatchResponse) func(ctx context.Context) (WatchStream, error) {
	return func(ctx context.Context) (WatchStream, error) { return newFakeStream(ctx, end, batches...), nil }
}

func refuse(err error) func(ctx context.Context) (WatchStream, error) {
	return func(context.Context) (WatchStream, error) { return nil, err }
}

// nextBatch waits for the next batch the bridge emits.
func nextBatch(t *testing.T, ch <-chan *downexv1.WatchResponse) *downexv1.WatchResponse {
	t.Helper()
	select {
	case m := <-ch:
		return m
	case <-time.After(10 * time.Second):
		t.Fatal("no batch was emitted")
		return nil
	}
}

// eventKinds names a batch's events: "snapshot:<id>", "updated:<id>", "removed:<id>", and
// the M6 and site media kinds.
func eventKinds(t *testing.T, m *downexv1.WatchResponse) []string {
	t.Helper()
	var out []string
	for _, e := range m.GetEvents() {
		switch k := e.GetKind().(type) {
		case *downexv1.Event_Snapshot:
			out = append(out, "snapshot:"+strconv.FormatInt(k.Snapshot.GetId(), 10))
		case *downexv1.Event_Updated:
			out = append(out, "updated:"+strconv.FormatInt(k.Updated.GetId(), 10))
		case *downexv1.Event_Removed:
			out = append(out, "removed:"+strconv.FormatInt(k.Removed, 10))
		case *downexv1.Event_QueueUpdated:
			out = append(out, "queue_updated:"+strconv.FormatInt(k.QueueUpdated.GetId(), 10))
		case *downexv1.Event_QueueRemoved:
			out = append(out, "queue_removed:"+strconv.FormatInt(k.QueueRemoved, 10))
		case *downexv1.Event_PostAction:
			out = append(out, "post_action:"+strconv.FormatInt(k.PostAction.GetQueueId(), 10))
		case *downexv1.Event_Tools:
			out = append(out, "tools")
		default:
			t.Errorf("unexpected event %v", e)
		}
	}
	return out
}

// startBridge runs a session's bridge on d and returns its batches.
func startBridge(t *testing.T, d Daemon, args ...string) (*Session, *recorder) {
	t.Helper()
	s, _ := newTestSession(t, d, args...)
	r := &recorder{emitted: make(chan *downexv1.WatchResponse, 64)}
	s.Start(r.emit)
	return s, r
}

func TestBridgeForwardsAndResubscribes(t *testing.T) {
	down := connect.NewError(connect.CodeUnavailable, errors.New("downexd went away"))
	sw := &scriptedWatch{sessions: []func(context.Context) (WatchStream, error){
		stream(down, snapshotOf(1, 2, 3), updated(2, 40), removed(3), updated(4, 1)),
		refuse(connect.NewError(connect.CodeUnavailable, errors.New("downexd is not running"))),
		stream(nil, snapshotOf(1, 5)), // 2 and 4 were removed while it was down
	}}
	f := newFakeDaemon()
	f.watch = sw.watch
	_, r := startBridge(t, f)

	first := nextBatch(t, r.emitted)
	if got, want := eventKinds(t, first), []string{"snapshot:1", "snapshot:2", "snapshot:3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("first batch %v, want %v", got, want)
	}
	// The batch is the daemon's message, as it arrived.
	want := &downexv1.Download{Id: 1, Size: -1, State: downexv1.DownloadState_DOWNLOAD_STATE_QUEUED}
	if got := first.GetEvents()[0].GetSnapshot(); !proto.Equal(got, want) {
		t.Errorf("snapshot event = %v, want %v", got, want)
	}
	up := nextBatch(t, r.emitted)
	if got := up.GetEvents()[0].GetUpdated(); got.GetReceived() != 40 || got.GetSpeedBps() != 2.5 {
		t.Errorf("updated event = %v", got)
	}
	if got := eventKinds(t, nextBatch(t, r.emitted)); !reflect.DeepEqual(got, []string{"removed:3"}) {
		t.Errorf("removed batch = %v", got)
	}
	if got := eventKinds(t, nextBatch(t, r.emitted)); !reflect.DeepEqual(got, []string{"updated:4"}) {
		t.Errorf("added-later batch = %v", got)
	}
	// After the outage: the new snapshot, plus removals of what it no longer has.
	if got, want := eventKinds(t, nextBatch(t, r.emitted)), []string{"snapshot:1", "snapshot:5", "removed:2", "removed:4"}; !reflect.DeepEqual(got, want) {
		t.Errorf("snapshot after reconnecting = %v, want %v", got, want)
	}
	if n := f.watches(); n < 3 {
		t.Errorf("Watch called %d times, want 3", n)
	}
	if !sw.stream(0).isClosed() {
		t.Error("the ended stream was not closed")
	}
}

func TestBridgeBackoff(t *testing.T) {
	unavailable := connect.NewError(connect.CodeUnavailable, errors.New("down"))
	s := time.Second
	for _, tc := range []struct {
		name     string
		min, max time.Duration
		want     []time.Duration
	}{
		{"1 s to 10 s", s, 10 * s, []time.Duration{1 * s, 2 * s, 4 * s, 8 * s, 10 * s, 10 * s, 1 * s, 2 * s, 4 * s}},
		{"defaults", 0, 0, []time.Duration{1 * s, 2 * s, 4 * s, 8 * s, 10 * s, 10 * s, 1 * s, 2 * s, 4 * s}},
		{"a max below the min is the min", 3 * s, s, []time.Duration{3 * s, 3 * s, 3 * s, 3 * s, 3 * s, 3 * s, 3 * s, 3 * s, 3 * s}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeDaemon()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var sessions []func(context.Context) (WatchStream, error)
			for range 6 {
				sessions = append(sessions, refuse(unavailable))
			}
			sessions = append(sessions, stream(unavailable, snapshotOf(1))) // back, then gone again
			sessions = append(sessions, refuse(unavailable), refuse(unavailable))
			f.watch = (&scriptedWatch{sessions: sessions}).watch

			var delays []time.Duration
			b := &Bridge{
				Daemon: f, Emit: func(*downexv1.WatchResponse) {}, Min: tc.min, Max: tc.max,
				Sleep: func(_ context.Context, d time.Duration) bool {
					delays = append(delays, d)
					if len(delays) == 9 {
						cancel()
						return false
					}
					return true
				},
			}
			b.Run(ctx)
			if !reflect.DeepEqual(delays, tc.want) {
				t.Errorf("delays %v, want %v", delays, tc.want)
			}
		})
	}
}

func TestBridgeStopsOnClose(t *testing.T) {
	sw := &scriptedWatch{sessions: []func(context.Context) (WatchStream, error){stream(nil, snapshotOf(1))}}
	f := newFakeDaemon()
	f.watch = sw.watch
	s, r := startBridge(t, f)
	nextBatch(t, r.emitted)

	done := make(chan struct{})
	go func() {
		s.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(ShutdownWait):
		t.Fatal("Close did not stop the bridge")
	}
	calls, emitted := f.watches(), r.emittedCount()
	if !sw.stream(0).isClosed() {
		t.Error("the stream was not closed on Close")
	}
	time.Sleep(20 * time.Millisecond) // several backoff periods of the test session
	if f.watches() != calls || r.emittedCount() != emitted {
		t.Errorf("the bridge kept running after Close: Watch %d→%d, events %d→%d", calls, f.watches(), emitted, r.emittedCount())
	}
	// A late Start does not revive it, nor does a second Close block.
	s.Start(r.emit)
	s.Close()
	time.Sleep(10 * time.Millisecond)
	if f.watches() != calls {
		t.Error("Start after Close subscribed again")
	}
}

func TestBridgeStartsOnce(t *testing.T) {
	f := newFakeDaemon()
	f.watchCalled = make(chan struct{}, 8)
	f.watch = (&scriptedWatch{sessions: []func(context.Context) (WatchStream, error){stream(nil, snapshotOf(1))}}).watch
	s, r := startBridge(t, f)
	s.Start(r.emit)
	nextBatch(t, r.emitted)
	<-f.watchCalled
	time.Sleep(10 * time.Millisecond)
	if n := f.watches(); n != 1 {
		t.Errorf("Watch called %d times after two Starts, want 1", n)
	}
}

func TestBridgeFirstSnapshotHasNoRemovals(t *testing.T) {
	b := &Bridge{}
	got := b.reconcile(snapshotOf(4, 2))
	if len(got.GetEvents()) != 2 {
		t.Fatalf("first snapshot changed: %v", got)
	}
	b.track(updated(9, 1).GetEvents())
	b.track(removed(4).GetEvents())
	// A later snapshot without 2 and 9 reports them removed, in id order.
	got = b.reconcile(&downexv1.WatchResponse{})
	if kinds := eventKinds(t, got); !reflect.DeepEqual(kinds, []string{"removed:2", "removed:9"}) {
		t.Errorf("empty snapshot after 2 and 9 = %v", kinds)
	}
	if len(b.known) != 0 {
		t.Errorf("known after an empty snapshot = %v", b.known)
	}
}

// A progress window watches its own download only, and shows no confirmation dialogs.
func TestProgressWindowWatchesItsDownload(t *testing.T) {
	sw := &scriptedWatch{sessions: []func(context.Context) (WatchStream, error){
		stream(nil, snapshotOf(42), updated(42, 10)),
	}}
	f := newFakeDaemon()
	f.watch = sw.watch
	s, r := startBridge(t, f, "--progress", "42", "--confirm", "5")
	s.Confirmations().Ready(r.show)

	if got := eventKinds(t, nextBatch(t, r.emitted)); !slices.Equal(got, []string{"snapshot:42"}) {
		t.Errorf("first batch = %v", got)
	}
	if got := eventKinds(t, nextBatch(t, r.emitted)); !slices.Equal(got, []string{"updated:42"}) {
		t.Errorf("second batch = %v", got)
	}
	f.mu.Lock()
	ids := f.watchIDs
	f.mu.Unlock()
	if len(ids) == 0 || !slices.Equal(ids[0], []int64{42}) {
		t.Errorf("Watch ids = %v, want [[42]]", ids)
	}

	// A second start for the same download brings the window forward and asks nothing.
	s.SecondInstance([]string{"--progress", "42", "--confirm", "6"}, r.focus)
	if n := r.focusCount(); n != 1 {
		t.Errorf("focus called %d times, want 1", n)
	}
	if got := r.shownIDs(); len(got) != 0 {
		t.Errorf("a progress window asked for confirmations: %v", got)
	}
}

// The main window watches every download.
func TestMainWindowWatchesEverything(t *testing.T) {
	sw := &scriptedWatch{sessions: []func(context.Context) (WatchStream, error){stream(nil, snapshotOf(1))}}
	f := newFakeDaemon()
	f.watch = sw.watch
	_, r := startBridge(t, f)
	nextBatch(t, r.emitted)
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.watchIDs) == 0 || f.watchIDs[0] != nil {
		t.Errorf("Watch ids = %v, want all downloads", f.watchIDs)
	}
}

// A watched download that does not exist (removed meanwhile, or never there) is reported
// as removed, whether Watch refuses at once or its stream says so.
func TestBridgeReportsAMissingDownloadAsRemoved(t *testing.T) {
	notFound := connect.NewError(connect.CodeNotFound, errors.New("download 42 not found"))
	f := newFakeDaemon()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f.watch = (&scriptedWatch{sessions: []func(context.Context) (WatchStream, error){
		refuse(notFound),
		stream(notFound),
		refuse(connect.NewError(connect.CodeUnavailable, errors.New("down"))),
		stream(nil, snapshotOf(42)),
	}}).watch
	var batches [][]string
	b := &Bridge{
		Daemon: f, IDs: []int64{42},
		Emit: func(m *downexv1.WatchResponse) {
			batches = append(batches, eventKinds(t, m))
			if len(batches) == 3 {
				cancel()
			}
		},
		Min: time.Millisecond, Max: time.Millisecond,
		Sleep: func(context.Context, time.Duration) bool { return ctx.Err() == nil },
	}
	b.Run(ctx)
	want := [][]string{{"removed:42"}, {"removed:42"}, {"snapshot:42"}}
	if !reflect.DeepEqual(batches, want) {
		t.Errorf("batches = %v, want two removals, then the snapshot", batches)
	}

	// Without ids (the main window), not_found is just another failed subscription.
	ctx2, cancel2 := context.WithCancel(t.Context())
	defer cancel2()
	f.watch = (&scriptedWatch{sessions: []func(context.Context) (WatchStream, error){refuse(notFound)}}).watch
	var emitted int
	b = &Bridge{
		Daemon: f, Emit: func(*downexv1.WatchResponse) { emitted++ },
		Min: time.Millisecond, Max: time.Millisecond,
		Sleep: func(context.Context, time.Duration) bool { cancel2(); return false },
	}
	b.Run(ctx2)
	if emitted != 0 {
		t.Errorf("the main window's bridge emitted %d batches", emitted)
	}
}

// Queue and post-action events are forwarded as they come.
func TestBridgeForwardsQueueEvents(t *testing.T) {
	queueEvents := &downexv1.WatchResponse{Events: []*downexv1.Event{
		{Kind: &downexv1.Event_QueueUpdated{QueueUpdated: &downexv1.Queue{Id: 2, Name: "Night", Running: true, DownloadIds: []int64{5, 4}}}},
		{Kind: &downexv1.Event_QueueRemoved{QueueRemoved: 3}},
		{Kind: &downexv1.Event_PostAction{PostAction: &downexv1.PostActionPending{
			QueueId: 2, Action: downexv1.PostAction_POST_ACTION_SLEEP,
			Deadline: timestamppb.New(time.Date(2026, 10, 21, 23, 1, 0, 0, time.UTC)),
		}}},
		{Kind: &downexv1.Event_Tools{Tools: &downexv1.ToolsStatus{Enabled: true}}},
	}}
	f := newFakeDaemon()
	f.watch = (&scriptedWatch{sessions: []func(context.Context) (WatchStream, error){stream(nil, snapshotOf(4, 5), queueEvents)}}).watch
	_, r := startBridge(t, f)
	if got := eventKinds(t, nextBatch(t, r.emitted)); !slices.Equal(got, []string{"snapshot:4", "snapshot:5"}) {
		t.Fatalf("snapshot = %v", got)
	}
	got := nextBatch(t, r.emitted)
	if !proto.Equal(got, queueEvents) {
		t.Errorf("queue events =\n%v\nwant\n%v", got, queueEvents)
	}
	// Queue events do not count as downloads: a later snapshot reports no removals for them.
	b := &Bridge{}
	b.reconcile(snapshotOf(4))
	b.track(queueEvents.GetEvents())
	if kinds := eventKinds(t, b.reconcile(snapshotOf(4))); !slices.Equal(kinds, []string{"snapshot:4"}) {
		t.Errorf("snapshot after queue events = %v", kinds)
	}
}
