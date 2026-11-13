package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

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

// scriptedWatch serves Watch from a list of sessions: each returns a stream (or an
// error); after the last, Watch blocks until ctx ends.
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

func stream(end error, batches ...*downexv1.WatchResponse) func(ctx context.Context) (WatchStream, error) {
	return func(ctx context.Context) (WatchStream, error) { return newFakeStream(ctx, end, batches...), nil }
}

func refuse(err error) func(ctx context.Context) (WatchStream, error) {
	return func(context.Context) (WatchStream, error) { return nil, err }
}

// nextEvent waits for the next payload the bridge emits and decodes it.
func nextEvent(t *testing.T, ch <-chan json.RawMessage) map[string]any {
	t.Helper()
	select {
	case p := <-ch:
		return decodeJSON(t, p)
	case <-time.After(10 * time.Second):
		t.Fatal("no event was emitted")
		return nil
	}
}

func eventKinds(t *testing.T, payload map[string]any) []string {
	t.Helper()
	var out []string
	events, _ := payload["events"].([]any)
	for _, e := range events {
		for k, v := range e.(map[string]any) {
			switch k {
			case "snapshot", "updated":
				out = append(out, k+":"+v.(map[string]any)["id"].(string))
			case "removed":
				out = append(out, "removed:"+v.(string))
			default:
				t.Errorf("unexpected event field %q", k)
			}
		}
	}
	return out
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
	h := &fakeHost{emitted: make(chan json.RawMessage, 16)}
	newTestApp(t, f, h)

	first := nextEvent(t, h.emitted)
	if got, want := eventKinds(t, first), []string{"snapshot:1", "snapshot:2", "snapshot:3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("first batch %v, want %v", got, want)
	}
	// The payload is the protojson WatchResponse.
	snap := first["events"].([]any)[0].(map[string]any)["snapshot"].(map[string]any)
	if !reflect.DeepEqual(snap, map[string]any{"id": "1", "size": "-1", "state": "DOWNLOAD_STATE_QUEUED"}) {
		t.Errorf("snapshot event = %v", snap)
	}
	up := nextEvent(t, h.emitted)
	if got := up["events"].([]any)[0].(map[string]any)["updated"].(map[string]any); got["received"] != "40" || got["speedBps"] != 2.5 {
		t.Errorf("updated event = %v", got)
	}
	if got := eventKinds(t, nextEvent(t, h.emitted)); !reflect.DeepEqual(got, []string{"removed:3"}) {
		t.Errorf("removed batch = %v", got)
	}
	if got := eventKinds(t, nextEvent(t, h.emitted)); !reflect.DeepEqual(got, []string{"updated:4"}) {
		t.Errorf("added-later batch = %v", got)
	}
	// After the outage: the new snapshot, plus removals of what it no longer has.
	if got, want := eventKinds(t, nextEvent(t, h.emitted)), []string{"snapshot:1", "snapshot:5", "removed:2", "removed:4"}; !reflect.DeepEqual(got, want) {
		t.Errorf("snapshot after reconnecting = %v, want %v", got, want)
	}
	if n := f.watches(); n < 3 {
		t.Errorf("Watch called %d times, want 3", n)
	}
	sw.mu.Lock()
	defer sw.mu.Unlock()
	if !sw.streams[0].isClosed() {
		t.Error("the ended stream was not closed")
	}
}

func TestBridgeBackoff(t *testing.T) {
	unavailable := connect.NewError(connect.CodeUnavailable, errors.New("down"))
	f := newFakeDaemon()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var sessions []func(context.Context) (WatchStream, error)
	for range 6 {
		sessions = append(sessions, refuse(unavailable))
	}
	sessions = append(sessions, stream(unavailable, snapshotOf(1))) // back, then gone again
	sessions = append(sessions, refuse(unavailable), refuse(unavailable))
	sw := &scriptedWatch{sessions: sessions}
	f.watch = sw.watch

	var delays []time.Duration
	b := &eventBridge{
		daemon: f, emit: func(json.RawMessage) {}, log: slog.New(slog.DiscardHandler),
		min: time.Second, max: 10 * time.Second,
		sleep: func(_ context.Context, d time.Duration) bool {
			delays = append(delays, d)
			if len(delays) == 9 {
				cancel()
				return false
			}
			return true
		},
	}
	b.run(ctx)
	s := time.Second
	want := []time.Duration{1 * s, 2 * s, 4 * s, 8 * s, 10 * s, 10 * s, 1 * s, 2 * s, 4 * s}
	if !reflect.DeepEqual(delays, want) {
		t.Errorf("delays %v, want %v", delays, want)
	}
}

func TestBridgeStopsOnShutdown(t *testing.T) {
	sw := &scriptedWatch{sessions: []func(context.Context) (WatchStream, error){stream(nil, snapshotOf(1))}}
	f := newFakeDaemon()
	f.watch = sw.watch
	h := &fakeHost{emitted: make(chan json.RawMessage, 4)}
	a, _ := newTestApp(t, f, h)
	nextEvent(t, h.emitted)

	done := make(chan struct{})
	go func() {
		a.shutdown()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(shutdownWait):
		t.Fatal("shutdown did not stop the bridge")
	}
	calls, emitted := f.watches(), h.emittedCount()
	sw.mu.Lock()
	closed := sw.streams[0].isClosed()
	sw.mu.Unlock()
	if !closed {
		t.Error("the stream was not closed on shutdown")
	}
	time.Sleep(20 * time.Millisecond) // several backoff periods of the test App
	if f.watches() != calls || h.emittedCount() != emitted {
		t.Errorf("the bridge kept running after shutdown: Watch %d→%d, events %d→%d", calls, f.watches(), emitted, h.emittedCount())
	}
	// A late startup does not revive it.
	a.startup(h)
	time.Sleep(10 * time.Millisecond)
	if f.watches() != calls {
		t.Error("startup after shutdown subscribed again")
	}
}

func TestBridgeFirstSnapshotHasNoRemovals(t *testing.T) {
	b := &eventBridge{}
	got := b.reconcile(snapshotOf(4, 2))
	if len(got.GetEvents()) != 2 {
		t.Fatalf("first snapshot changed: %v", got)
	}
	b.track(updated(9, 1).GetEvents())
	b.track(removed(4).GetEvents())
	// A later snapshot without 2 and 9 reports them removed, in id order.
	got = b.reconcile(&downexv1.WatchResponse{})
	if kinds := eventKinds(t, decodeJSON(t, mustMarshal(t, got))); !reflect.DeepEqual(kinds, []string{"removed:2", "removed:9"}) {
		t.Errorf("empty snapshot after 2 and 9 = %v", kinds)
	}
	if len(b.known) != 0 {
		t.Errorf("known after an empty snapshot = %v", b.known)
	}
}

func mustMarshal(t *testing.T, m *downexv1.WatchResponse) json.RawMessage {
	t.Helper()
	raw, err := marshalOptions.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
