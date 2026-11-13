package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	"downex/internal/engine"
	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/store"
	"downex/internal/testserver"
)

// gatedStore holds SaveCheckpoint calls while its gate is closed, so that a run that
// was told to stop cannot finish committing (a slow final flush).
type gatedStore struct {
	*store.Memory
	mu   sync.Mutex
	gate chan struct{} // non-nil: SaveCheckpoint waits until it is closed
}

func (g *gatedStore) hold() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.gate = make(chan struct{})
}

func (g *gatedStore) release() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.gate != nil {
		close(g.gate)
		g.gate = nil
	}
}

func (g *gatedStore) SaveCheckpoint(ctx context.Context, d *model.Download, segs []model.Segment) error {
	g.mu.Lock()
	gate := g.gate
	g.mu.Unlock()
	if gate != nil {
		<-gate
	}
	return g.Memory.SaveCheckpoint(ctx, d, segs)
}

// pauseUntilCanceled starts Pause for id with a context that is canceled once the run
// has the pause intent but before it stopped (the store holds its final commit), as
// with Ctrl+C on "downex pause" or a client deadline. It returns Pause's error.
func pauseUntilCanceled(t *testing.T, h *harness, gs *gatedStore, id int64, op func(context.Context) error) error {
	t.Helper()
	gs.hold()
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- op(ctx) }()
	waitUntil(t, "the stop request", func() bool {
		h.m.mu.Lock()
		defer h.m.mu.Unlock()
		e := h.m.entries[model.DownloadID(id)]
		return e != nil && e.run != nil && e.run.intent >= intentPause
	})
	cancel()
	select {
	case err := <-errc:
		return err
	case <-time.After(waitTimeout):
		t.Fatal("the stop request did not return after its context was canceled")
		return nil
	}
}

// TestResumeOverridesPendingPause (L1): a Resume that arrives while a Pause (whose call
// already ended) is still stopping the run must win: the download is queued again and
// completes, instead of Resume reporting success and the download ending paused.
func TestResumeOverridesPendingPause(t *testing.T) {
	const size, seed = 8 << 20, 61
	ts := serve(t, testserver.Options{Size: size, Seed: seed, ETag: `"p"`})
	gs := &gatedStore{Memory: store.NewMemory()}
	h := newHarness(t, harnessOptions{st: gs})

	d := h.add(&downexv1.AddRequest{Url: ts.URL + "/pending.bin", SpeedLimit: 256 << 10})
	h.waitReceived(d.GetId(), 32<<10)
	err := pauseUntilCanceled(t, h, gs, d.GetId(), func(ctx context.Context) error {
		_, err := h.m.Pause(ctx, d.GetId())
		return err
	})
	if !errors.Is(err, context.Canceled) {
		gs.release()
		t.Fatalf("pause = %v, want context.Canceled", err)
	}
	unlimited := int64(0)
	r, err := h.m.Resume(ctxT(t), &downexv1.ResumeRequest{Id: d.GetId(), SpeedLimit: &unlimited})
	gs.release()
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	t.Logf("resume answered %v", r.GetState())
	checkFile(t, h.waitCompleted(d.GetId()), seed, size)
}

// TestResumeDuringPendingRemove (L1): a removal in progress is final; Resume says so
// instead of reporting success.
func TestResumeDuringPendingRemove(t *testing.T) {
	ts := serve(t, testserver.Options{Size: 8 << 20, Seed: 62})
	gs := &gatedStore{Memory: store.NewMemory()}
	h := newHarness(t, harnessOptions{st: gs})

	d := h.add(&downexv1.AddRequest{Url: ts.URL + "/removed.bin", SpeedLimit: 256 << 10})
	h.waitReceived(d.GetId(), 32<<10)
	err := pauseUntilCanceled(t, h, gs, d.GetId(), func(ctx context.Context) error {
		return h.m.Remove(ctx, d.GetId(), false)
	})
	if !errors.Is(err, context.Canceled) {
		gs.release()
		t.Fatalf("remove = %v, want context.Canceled", err)
	}
	_, err = h.m.Resume(ctxT(t), &downexv1.ResumeRequest{Id: d.GetId()})
	gs.release()
	if connectCode(err) != connect.CodeFailedPrecondition {
		t.Fatalf("resume during a pending removal = %v, want FailedPrecondition", err)
	}
	h.waitState(d.GetId(), stPaused)
}

// probeArmedStore fails GetDownload from the moment the engine commits the probe's
// outcome until disarmed, so the daemon never learns the probed record.
type probeArmedStore struct {
	*store.Memory
	armed, failing atomic.Bool
}

func (s *probeArmedStore) SaveCheckpoint(ctx context.Context, d *model.Download, segs []model.Segment) error {
	if d.State == model.StateDownloading && d.Filename != "" && s.armed.CompareAndSwap(true, false) {
		s.failing.Store(true)
	}
	return s.Memory.SaveCheckpoint(ctx, d, segs)
}

func (s *probeArmedStore) GetDownload(ctx context.Context, id model.DownloadID) (*model.Download, error) {
	if s.failing.Load() {
		return nil, errors.New("injected read failure (database busy)")
	}
	return s.Memory.GetDownload(ctx, id)
}

// staleAfterPause adds a download, makes the store unreadable from the probe on,
// pauses it (so the daemon keeps its unprobed copy) and makes the store readable again.
func staleAfterPause(t *testing.T, name string) (*harness, *probeArmedStore, *downexv1.Download) {
	t.Helper()
	const size, seed = 4 << 20, 63
	ts := serve(t, testserver.Options{Size: size, Seed: seed, ETag: `"st"`})
	st := &probeArmedStore{Memory: store.NewMemory()}
	st.armed.Store(true)
	h := newHarness(t, harnessOptions{st: st})
	d := h.add(&downexv1.AddRequest{Url: ts.URL + "/" + name, SpeedLimit: 256 << 10})
	waitUntil(t, "the probe", func() bool { return st.failing.Load() })
	h.waitReceived(d.GetId(), 64<<10) // progress events tried (and failed) to re-read it
	if _, err := h.m.Pause(ctxT(t), d.GetId()); err != nil {
		t.Fatal(err)
	}
	st.failing.Store(false)
	rec := storedState(t, st, d.GetId())
	if rec.Mode != model.ModeRanged || rec.Filename != name || rec.Validators.ETag != `"st"` {
		t.Fatalf("the engine's probed record is not stored: %+v", rec)
	}
	return h, st, d
}

// TestResumeAfterUnreadableRunRecord (L2): when the record could not be read after a
// run, the next Resume reads it again instead of writing the daemon's older copy over
// the probe's outcome: the download resumes into its reserved name and leaves no
// orphaned part file.
func TestResumeAfterUnreadableRunRecord(t *testing.T) {
	h, _, d := staleAfterPause(t, "flaky.bin")
	unlimited := int64(0)
	h.resume(&downexv1.ResumeRequest{Id: d.GetId(), SpeedLimit: &unlimited})
	done := h.waitCompleted(d.GetId())
	if done.GetFilename() != "flaky.bin" {
		t.Errorf("completed as %q, want the reserved name flaky.bin", done.GetFilename())
	}
	checkFile(t, done, 63, 4<<20)
	if _, err := os.Stat(engine.PartPath(h.dir, "flaky.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("part file left behind: %v", err)
	}
	if entries, _ := os.ReadDir(h.dir); len(entries) != 1 {
		t.Errorf("download directory holds %v, want only flaky.bin", entries)
	}
}

// TestRemoveAfterUnreadableRunRecord (L2): Remove with delete_file finds the part file
// of a download whose record was unreadable after its run.
func TestRemoveAfterUnreadableRunRecord(t *testing.T) {
	h, st, d := staleAfterPause(t, "gone.bin")
	part := engine.PartPath(h.dir, "gone.bin")
	if _, err := os.Stat(part); err != nil {
		t.Fatalf("part file: %v", err)
	}
	// While the store is unreadable again, the operation fails rather than acting on
	// the outdated copy.
	st.failing.Store(true)
	if err := h.m.Remove(ctxT(t), d.GetId(), true); connectCode(err) != connect.CodeUnavailable {
		st.failing.Store(false)
		t.Fatalf("remove with an unreadable store = %v, want Unavailable", err)
	}
	st.failing.Store(false)
	if err := h.m.Remove(ctxT(t), d.GetId(), true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(part); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("part file %s survived Remove(delete_file): %v", filepath.Base(part), err)
	}
}
