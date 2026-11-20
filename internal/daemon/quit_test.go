package daemon

import (
	"context"
	"testing"
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
)

// TestAnnounceQuit: the user's Quit reaches every Watch stream, of every download and of some,
// as a quit event, also when Shutdown follows at once (the stream's last batch).
func TestAnnounceQuit(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	d := h.add(&downexv1.AddRequest{Url: "http://127.0.0.1:9/a", StartPaused: true})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	quits := make(chan bool, 2)
	started := make(chan struct{}, 2)
	watch := func(ids []int64) {
		first := true
		_ = h.m.Watch(ctx, ids, func(r *downexv1.WatchResponse) error {
			if first {
				first = false
				started <- struct{}{}
			}
			for _, ev := range r.GetEvents() {
				if ev.GetQuit() {
					quits <- true
				}
			}
			return nil
		})
	}
	go watch(nil)
	go watch([]int64{d.GetId()})
	<-started
	<-started
	h.m.AnnounceQuit()
	_ = h.m.Shutdown(ctx)
	for i := range 2 {
		select {
		case <-quits:
		case <-ctx.Done():
			t.Fatalf("%d of 2 streams got the quit event", i)
		}
	}
}
