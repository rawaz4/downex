package daemon

import (
	"testing"

	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/store"
	"downex/internal/testserver"
)

// The re-read of a running download (refreshLive) never adopts a record that its run has
// already ended. The engine stores a completed download and then emits its last progress
// event (for a small file, its first) just before Run returns; a re-read that found the
// completed record before finish took over made finish see the download as completed
// before this run, so the completion was never reported, marked, scanned or counted for
// its queue (whose post-queue action then never started).
func TestLiveRefreshKeepsARunsEnd(t *testing.T) {
	ts := serve(t, testserver.Options{Size: 1 << 20, Seed: 78, StallAfter: 1024})
	st := store.NewMemory()
	h := newHarness(t, harnessOptions{st: st})
	id := h.add(&downexv1.AddRequest{Url: ts.URL + "/r"}).GetId()
	h.waitState(id, downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING)

	h.m.mu.Lock()
	e := h.m.entries[model.DownloadID(id)]
	r := e.run
	h.m.mu.Unlock()
	if r == nil {
		t.Fatal("the download does not run")
	}
	// The Get above shows "downloading" from the first progress event alone; the re-read
	// that event started may still be on its way (and would then read the record below).
	waitUntil(t, "the live re-read", func() bool {
		h.m.mu.Lock()
		defer h.m.mu.Unlock()
		return e.rec.State == model.StateDownloading && e.rec.Filename != ""
	})
	stored, err := st.GetDownload(ctxT(t), e.id)
	if err != nil {
		t.Fatal(err)
	}
	done := *stored // what the engine stores when the run completes
	done.State = model.StateCompleted
	if err := st.UpdateDownload(ctxT(t), &done); err != nil {
		t.Fatal(err)
	}
	h.m.wg.Add(1)
	h.m.refreshLive(e, r)
	h.m.mu.Lock()
	got := e.rec.State
	h.m.mu.Unlock()
	if err := st.UpdateDownload(ctxT(t), stored); err != nil { // the run goes on
		t.Fatal(err)
	}
	if got != model.StateDownloading {
		t.Fatalf("cached state after the re-read = %v, want %v until the run ends", got, model.StateDownloading)
	}
}
