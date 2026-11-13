package daemon

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/store"
	"downex/internal/testserver"
)

// flakyStore fails GetDownload while failGet is set.
type flakyStore struct {
	*store.Memory
	failGet atomic.Bool
}

func (f *flakyStore) GetDownload(ctx context.Context, id model.DownloadID) (*model.Download, error) {
	if f.failGet.Load() {
		return nil, errors.New("injected read failure")
	}
	return f.Memory.GetDownload(ctx, id)
}

// When the record cannot be read back after a run, nothing is written over what the
// engine committed, and the download resumes once the store works again.
func TestUnreadableRecordAfterRunIsNotOverwritten(t *testing.T) {
	const size, seed = 4 << 20, 51
	ts := serve(t, testserver.Options{Size: size, Seed: seed, ETag: `"f"`})
	st := &flakyStore{Memory: store.NewMemory()}
	h := newHarness(t, harnessOptions{st: st})

	d := h.add(&downexv1.AddRequest{Url: ts.URL + "/flaky.bin", SpeedLimit: 512 << 10})
	h.waitReceived(d.GetId(), 64<<10)
	st.failGet.Store(true)
	p, err := h.m.Pause(ctxT(t), d.GetId())
	st.failGet.Store(false)
	if err != nil {
		t.Fatal(err)
	}
	if p.GetState() != stPaused {
		t.Fatalf("paused = %v", p.GetState())
	}
	rec := storedState(t, st, d.GetId())
	segs, err := st.LoadSegments(context.Background(), rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	var durable int64
	for _, s := range segs {
		durable += s.Durable
	}
	if rec.State != model.StatePaused || rec.Mode != model.ModeRanged || rec.Filename == "" || rec.Validators.ETag != `"f"` || durable == 0 {
		t.Fatalf("engine's record was overwritten: %+v, %d durable bytes", rec, durable)
	}
	unlimited := int64(0)
	h.resume(&downexv1.ResumeRequest{Id: d.GetId(), SpeedLimit: &unlimited})
	checkFile(t, h.waitCompleted(d.GetId()), seed, size)
}
