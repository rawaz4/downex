package daemon

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"

	"downex/internal/dlock"
	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/testserver"
)

// A download held by another process (the standalone CLI) is neither started nor
// changed by the daemon until that process lets go of it.
func TestLockedDownloadIsDeferred(t *testing.T) {
	ts := serve(t, testserver.Options{Size: 256 << 10, Seed: 41})
	lockDir := t.TempDir()
	h := newHarness(t, harnessOptions{lockDir: lockDir})
	ctx := ctxT(t)

	// The next download gets id 1; another process holds its lock.
	other, err := dlock.Acquire(lockDir, 1)
	if err != nil {
		t.Fatal(err)
	}
	d := h.add(&downexv1.AddRequest{Url: ts.URL + "/locked"})
	if d.GetId() != 1 {
		t.Fatalf("id = %d", d.GetId())
	}
	time.Sleep(300 * time.Millisecond)
	if got := h.get(1); got.GetState() != stQueued {
		t.Fatalf("locked download is %v, want queued", got.GetState())
	}
	if _, err := h.dl.Remove(ctx, connect.NewRequest(&downexv1.RemoveRequest{Id: 1, DeleteFile: true})); connectCode(err) != connect.CodeFailedPrecondition {
		t.Fatalf("remove of a locked download: %v", err)
	}
	if len(ts.Requests()) != 0 {
		t.Fatal("a locked download was fetched")
	}
	other.Release()
	h.m.mu.Lock()
	h.m.entries[1].deferUntil = time.Now() // skip the retry delay
	h.m.scheduleLocked()
	h.m.mu.Unlock()
	checkFile(t, h.waitCompleted(1), 41, 256<<10)
}

// A stale copy of a removed download (read by a List racing the removal) is never
// indexed again.
func TestRemovedDownloadIsNotReadopted(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	d := h.add(&downexv1.AddRequest{Url: "http://127.0.0.1:9/x", StartPaused: true})
	stale := storedState(t, h.st, d.GetId())
	if _, err := h.dl.Remove(ctxT(t), connect.NewRequest(&downexv1.RemoveRequest{Id: d.GetId()})); err != nil {
		t.Fatal(err)
	}
	h.m.mu.Lock()
	e := h.m.adoptLocked(&stale, 0)
	n := len(h.m.entries)
	h.m.mu.Unlock()
	if e != nil || n != 0 {
		t.Fatalf("removed download re-indexed: %v, %d entries", e, n)
	}
}

// Downloads created by another process after the daemon started are listed and can be
// adopted once that process has finished with them.
func TestForeignDownloadIsAdopted(t *testing.T) {
	ts := serve(t, testserver.Options{Size: 128 << 10, Seed: 42})
	lockDir := t.TempDir()
	h := newHarness(t, harnessOptions{lockDir: lockDir})
	ctx := ctxT(t)

	// Another process creates and runs a download.
	rec := &model.Download{
		Request: model.Request{URL: ts.URL + "/foreign"}, Dir: h.dir, Size: -1,
		State: model.StateDownloading, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := h.st.CreateDownload(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	other, err := dlock.Acquire(lockDir, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	all := h.list()
	if len(all) != 1 || all[0].GetId() != int64(rec.ID) || all[0].GetState() != stDownloading {
		t.Fatalf("list = %v", all)
	}
	if _, err := h.dl.Pause(ctx, connect.NewRequest(&downexv1.PauseRequest{Id: int64(rec.ID)})); connectCode(err) != connect.CodeFailedPrecondition {
		t.Fatalf("pause of a download another process runs: %v", err)
	}
	if s := h.status(); s.GetActive() != 0 || s.GetQueued() != 0 {
		t.Fatalf("foreign download scheduled: %v", s)
	}
	// That process dies: its stale "downloading" state is adopted as paused.
	other.Release()
	if p := h.pause(int64(rec.ID)); p.GetState() != stPaused {
		t.Fatalf("adopted = %v", p.GetState())
	}
	if got := storedState(t, h.st, int64(rec.ID)); got.State != model.StatePaused {
		t.Fatalf("stored = %v", got.State)
	}
	h.resume(&downexv1.ResumeRequest{Id: int64(rec.ID)})
	checkFile(t, h.waitCompleted(int64(rec.ID)), 42, 128<<10)
}
