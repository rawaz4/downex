package daemon

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/testserver"
)

// A Watch subscriber whose sends block (a client that stopped reading) must not delay
// downloads; its changes coalesce to one pending entry per download meanwhile.
func TestSlowWatchSubscriberDoesNotStallDownloads(t *testing.T) {
	const size = 4 << 20
	ts := serve(t, testserver.Options{Size: size, Seed: 31})
	h := newHarness(t, harnessOptions{})

	unblock := make(chan struct{})
	var (
		mu     sync.Mutex
		sends  int
		latest = map[int64]downexv1.DownloadState{}
	)
	watchDone := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		watchDone <- h.m.Watch(ctx, nil, func(r *downexv1.WatchResponse) error {
			mu.Lock()
			sends++
			n := sends
			for _, ev := range r.GetEvents() {
				if u := ev.GetUpdated(); u != nil {
					latest[u.GetId()] = u.GetState()
				}
			}
			mu.Unlock()
			if n == 2 { // the first batch after the snapshot is stuck until the end
				<-unblock
			}
			return nil
		})
	}()

	// Also an RPC subscriber that never reads its stream.
	stuck, err := h.dl.Watch(ctxT(t), connect.NewRequest(&downexv1.WatchRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	defer stuck.Close()

	var ids []int64
	start := time.Now()
	for i := range 4 {
		// About a second each, so most progress happens while the subscriber is stuck.
		ids = append(ids, h.add(&downexv1.AddRequest{Url: ts.URL + "/w" + string(rune('0'+i)), SpeedLimit: 4 << 20}).GetId())
	}
	for _, id := range ids {
		checkFile(t, h.waitCompleted(id), 31, size)
	}
	elapsed := time.Since(start)
	t.Logf("downloads completed in %v while a subscriber was stuck", elapsed)
	if elapsed > 10*time.Second {
		t.Errorf("downloads took %v", elapsed)
	}
	mu.Lock()
	if sends != 2 {
		t.Errorf("%d sends while the subscriber was stuck, want 2", sends)
	}
	mu.Unlock()

	// The blocked subscriber holds at most one pending change per download.
	h.m.hub.mu.Lock()
	for s := range h.m.hub.subs {
		if n := len(s.pending); n > len(ids) {
			t.Errorf("subscriber has %d pending changes for %d downloads", n, len(ids))
		}
	}
	h.m.hub.mu.Unlock()

	close(unblock)
	// It then receives the coalesced latest state of every download.
	waitUntil(t, "the slow subscriber to catch up", func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, id := range ids {
			if latest[id] != stCompleted {
				return false
			}
		}
		return sends >= 3
	})
	cancel()
	if err := <-watchDone; err != context.Canceled {
		t.Fatalf("watch returned %v", err)
	}
}

// Batches of one stream are at least 250 ms apart and carry the latest state.
func TestWatchBatchRate(t *testing.T) {
	ts := serve(t, testserver.Options{Size: 8 << 20, Seed: 32})
	h := newHarness(t, harnessOptions{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var (
		mu    sync.Mutex
		times []time.Time
		last  *downexv1.Download
	)
	watchDone := make(chan error, 1)
	go func() {
		watchDone <- h.m.Watch(ctx, nil, func(r *downexv1.WatchResponse) error {
			mu.Lock()
			defer mu.Unlock()
			times = append(times, time.Now())
			for _, ev := range r.GetEvents() {
				if u := ev.GetUpdated(); u != nil {
					last = u
				}
			}
			return nil
		})
	}()
	waitUntil(t, "snapshot", func() bool { mu.Lock(); defer mu.Unlock(); return len(times) == 1 })
	d := h.add(&downexv1.AddRequest{Url: ts.URL + "/rate", SpeedLimit: 4 << 20})
	h.waitCompleted(d.GetId())
	waitUntil(t, "the completed event", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return last != nil && last.GetState() == stCompleted
	})
	cancel()
	<-watchDone
	mu.Lock()
	defer mu.Unlock()
	if len(times) < 4 {
		t.Fatalf("only %d batches for a ~2 s download", len(times))
	}
	for i := 1; i < len(times); i++ {
		if gap := times[i].Sub(times[i-1]); gap < minBatchInterval-5*time.Millisecond {
			t.Errorf("batches %d and %d were sent %v apart", i-1, i, gap)
		}
	}
}

// pendingLen reports how many downloads have pending changes for s.
func (h *hub) pendingLen(s *subscriber) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(s.pending)
}

func TestHubCoalescesWithoutBlocking(t *testing.T) {
	h := newHub()
	all, _ := h.subscribe(nil)
	one, _ := h.subscribe(map[model.DownloadID]bool{7: true})

	start := time.Now()
	for i := range 200_000 {
		h.publish(model.DownloadID(i%1000+1), kindUpdated)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("publishing to undrained subscribers took %v", d)
	}
	if n := h.pendingLen(all); n != 1000 {
		t.Fatalf("pending = %d, want one per download", n)
	}
	if n := h.pendingLen(one); n != 1 {
		t.Fatalf("filtered subscriber pending = %d", n)
	}
	h.publish(7, kindRemoved)
	h.publish(7, kindUpdated) // a late update never hides the removal
	if p := h.take(one).downloads; len(p) != 1 || p[7] != kindRemoved {
		t.Fatalf("take = %v", p)
	}
	if p := h.take(one); !p.empty() || p.downloads != nil {
		t.Fatalf("second take = %v", p)
	}
	// Queue and post-queue action events reach only the subscribers of every download.
	h.publishQueue(2, kindUpdated)
	h.publishQueue(2, kindRemoved)
	h.publishQueue(2, kindUpdated) // a removed queue stays removed
	h.publishPost(&downexv1.PostActionPending{QueueId: 2})
	h.publishPost(&downexv1.PostActionPending{QueueId: 2, Cancelled: true}) // the latest wins
	if p := h.take(one); !p.empty() {
		t.Fatalf("filtered subscriber got queue events: %v", p)
	}
	if p := h.take(all); p.queues[2] != kindRemoved || len(p.posts) != 1 || !p.posts[2].GetCancelled() {
		t.Fatalf("queue events = %v / %v", p.queues, p.posts)
	}
	select {
	case <-all.signal:
	default:
		t.Fatal("subscriber was not signalled")
	}
	h.unsubscribe(all)
	h.publish(1, kindUpdated)
	h.close()
	if _, ok := h.subscribe(nil); ok {
		t.Fatal("subscribe after close succeeded")
	}
	select {
	case <-h.done:
	default:
		t.Fatal("done not closed")
	}
	h.close() // idempotent
}

// TestWatchSnapshotReportsRemovedIDs (L9): Watch checks the requested ids before it
// subscribes; a download removed in between got its removed event before the stream
// subscribed. The snapshot (taken after subscribing) must report it, or a follower
// ("downex resume --wait") would wait forever.
func TestWatchSnapshotReportsRemovedIDs(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	keep := h.add(&downexv1.AddRequest{Url: "http://127.0.0.1:9/keep", StartPaused: true})
	gone := h.add(&downexv1.AddRequest{Url: "http://127.0.0.1:9/gone", StartPaused: true})
	if err := h.m.Remove(ctxT(t), gone.GetId(), false); err != nil {
		t.Fatal(err)
	}
	filter := map[model.DownloadID]bool{model.DownloadID(keep.GetId()): true, model.DownloadID(gone.GetId()): true}
	evs, err := h.m.snapshot(ctxT(t), filter)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || evs[0].GetSnapshot().GetId() != keep.GetId() || evs[1].GetRemoved() != gone.GetId() {
		t.Fatalf("snapshot = %v, want a snapshot of %d and the removal of %d", evs, keep.GetId(), gone.GetId())
	}
}

// TestWatchToolsEvents (docs/SITE-MEDIA-SPEC.md §3.1): a Watch of every download ends
// its first batch with the tools status and sends it again after changes, coalesced to
// at most four batches per second, each carrying the latest status; a Watch of some
// downloads gets no tools events.
func TestWatchToolsEvents(t *testing.T) {
	tools := newFakeTools()
	h := newSiteHarness(t, harnessOptions{}, siteHarnessOptions{site: newFakeSite(1), tools: tools})
	d := h.add(&downexv1.AddRequest{Url: "http://127.0.0.1:9/paused", StartPaused: true})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var (
		mu       sync.Mutex
		times    []time.Time
		versions []string // of yt-dlp, per tools event
		firstEnd *downexv1.Event
		some     int // tools events of the Watch of one download
	)
	watchDone := make(chan error, 2)
	go func() {
		watchDone <- h.m.Watch(ctx, nil, func(r *downexv1.WatchResponse) error {
			mu.Lock()
			defer mu.Unlock()
			evs := r.GetEvents()
			if len(times) == 0 && len(evs) > 0 {
				firstEnd = evs[len(evs)-1]
			}
			times = append(times, time.Now())
			for _, ev := range evs {
				if ts := ev.GetTools(); ts != nil {
					versions = append(versions, ts.GetTools()[0].GetVersion())
				}
			}
			return nil
		})
	}()
	go func() {
		watchDone <- h.m.Watch(ctx, []int64{d.GetId()}, func(r *downexv1.WatchResponse) error {
			mu.Lock()
			defer mu.Unlock()
			for _, ev := range r.GetEvents() {
				if ev.GetTools() != nil {
					some++
				}
			}
			return nil
		})
	}()
	waitUntil(t, "the first batch", func() bool { mu.Lock(); defer mu.Unlock(); return len(times) == 1 })
	mu.Lock()
	if ts := firstEnd.GetTools(); ts == nil || !ts.GetEnabled() || len(ts.GetTools()) != 2 || versions[0] != "2026.10.10.233141" {
		t.Errorf("the first batch ends with %v", firstEnd)
	}
	mu.Unlock()

	// A burst of 40 changes over about 400 ms.
	start := time.Now()
	for i := range 40 {
		tools.mu.Lock()
		tools.snap.Tools[0].Version = fmt.Sprintf("v%02d", i)
		tools.mu.Unlock()
		tools.changed()
		time.Sleep(10 * time.Millisecond)
	}
	waitUntil(t, "the latest status", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return versions[len(versions)-1] == "v39"
	})
	elapsed := time.Since(start)
	cancel()
	<-watchDone
	<-watchDone
	mu.Lock()
	defer mu.Unlock()
	if limit := int(elapsed/minBatchInterval) + 2; len(times) > limit {
		t.Errorf("%d batches in %v for 40 changes, want at most %d", len(times), elapsed, limit)
	}
	for i := 1; i < len(times); i++ {
		if gap := times[i].Sub(times[i-1]); gap < minBatchInterval-5*time.Millisecond {
			t.Errorf("batches %d and %d were sent %v apart", i-1, i, gap)
		}
	}
	if some != 0 {
		t.Errorf("the Watch of one download got %d tools events", some)
	}
}
