package state_test

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/state"
	"downex/ui/state/statetest"
)

func progressStore(t *testing.T, f *statetest.Daemon, id int64) (*state.ProgressStore, *statetest.Clock) {
	t.Helper()
	c := statetest.NewClock(1_800_000_000_000)
	p := state.NewProgress(state.ProgressOptions{Daemon: f, Events: &f.Events, ID: id, Clock: c, Go: statetest.Sync})
	t.Cleanup(p.Dispose)
	return p, c
}

func running(id int64, edit func(d *downexv1.Download)) *downexv1.Download {
	return dl(id, func(d *downexv1.Download) {
		d.State = state.StateDownloading
		if edit != nil {
			edit(d)
		}
	})
}

func TestProgressReadsTheDownloadAndTheSettings(t *testing.T) {
	f := statetest.NewDaemon(running(4, nil))
	p, _ := progressStore(t, f, 4)
	p.Start(0)
	if !p.Ready() || p.Download().GetId() != 4 || p.Settings() == nil || p.Missing() || p.Removed() || !p.Connected() {
		t.Fatal(p.Download(), p.Settings())
	}
	if s, ok := p.FirstState(); !ok || s != state.StateDownloading {
		t.Fatal(s, ok)
	}
	// The window tells downexd once that this download gets the complete dialog.
	if f.Count("setDownloadOptions") != 1 || !p.Download().GetCompletionDialog() {
		t.Fatal(f.Count("setDownloadOptions"), p.Download())
	}
	f.Emit(statetest.Updated(running(4, func(d *downexv1.Download) { d.UpdatedAt = statetest.Ms(9e12) })))
	if f.Count("setDownloadOptions") != 1 {
		t.Fatal("marked twice")
	}
}

// A completed download needs no mark; one marked in an earlier run is told again, once:
// downexd gives the complete dialog only to a run that a window shows (DESKTOP-SPEC §8).
func TestProgressMarksNothingForACompletedDownload(t *testing.T) {
	f := statetest.NewDaemon(dl(1, func(d *downexv1.Download) { d.State = state.StateCompleted }),
		running(2, func(d *downexv1.Download) { d.CompletionDialog = true }))
	p, _ := progressStore(t, f, 1)
	p.Start(0)
	if f.Count("setDownloadOptions") != 0 {
		t.Fatal(f.Count("setDownloadOptions"))
	}
	q, _ := progressStore(t, f, 2)
	q.Start(0)
	f.Emit(statetest.Updated(running(2, func(d *downexv1.Download) { d.CompletionDialog, d.UpdatedAt = true, statetest.Ms(9e12) })))
	if f.Count("setDownloadOptions") != 1 {
		t.Fatal(f.Count("setDownloadOptions"))
	}
	if req, ok := f.Args("setDownloadOptions")[1].(*downexv1.SetDownloadOptionsRequest); !ok || req.GetId() != 2 || !req.GetCompletionDialog() {
		t.Fatal(f.Args("setDownloadOptions"))
	}
}

// A run that the open window sees start (resumed from the main window, the CLI or the queue)
// is marked too, once: downexd gives the complete dialog to the runs a window shows.
func TestProgressMarksEachRunItSeesStart(t *testing.T) {
	f := statetest.NewDaemon(running(5, nil))
	p, _ := progressStore(t, f, 5)
	p.Start(0)
	if f.Count("setDownloadOptions") != 1 {
		t.Fatal(f.Count("setDownloadOptions"))
	}
	at := int64(9e12)
	step := func(st downexv1.DownloadState) {
		at += 1000
		f.Emit(statetest.Updated(running(5, func(d *downexv1.Download) { d.State, d.CompletionDialog, d.UpdatedAt = st, true, statetest.Ms(at) })))
	}
	step(state.StatePaused)
	if f.Count("setDownloadOptions") != 1 {
		t.Fatal("marked when it stopped")
	}
	step(state.StateQueued)
	step(state.StateDownloading)
	if f.Count("setDownloadOptions") != 2 {
		t.Fatal(f.Count("setDownloadOptions"))
	}
}

// The window's own Resume and Retry tell downexd that the run they start shows in it.
func TestProgressResumeShowsTheRunInTheWindow(t *testing.T) {
	f := statetest.NewDaemon(dl(3, func(d *downexv1.Download) { d.State = state.StateFailed }))
	p, _ := progressStore(t, f, 3)
	p.Start(0)
	if err := p.Resume(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	req, ok := f.Args("resume")[1].(*downexv1.ResumeRequest)
	if !ok || req.GetId() != 3 || !req.GetProgressWindow() {
		t.Fatal(f.Args("resume"))
	}
}

func TestProgressSendsTheMarkAgainOnlyAfterDownexdWasUnreachable(t *testing.T) {
	f := statetest.NewDaemon(running(1, nil), running(2, nil))
	refusal := statetest.Err(connect.CodeUnavailable, "connection refused")
	f.Before = func(name string, _ ...any) error {
		if name == "setDownloadOptions" {
			return refusal
		}
		return nil
	}
	p, _ := progressStore(t, f, 1)
	p.Start(0)
	f.Emit(statetest.Updated(running(1, func(d *downexv1.Download) { d.UpdatedAt = statetest.Ms(9e12) })))
	if f.Count("setDownloadOptions") != 2 {
		t.Fatal(f.Count("setDownloadOptions"))
	}
	// Any other refusal (completed meanwhile, being removed) is final.
	refusal = statetest.Err(connect.CodeFailedPrecondition, "download 2 is already completed")
	q, _ := progressStore(t, f, 2)
	q.Start(0)
	f.Emit(statetest.Updated(running(2, func(d *downexv1.Download) { d.UpdatedAt = statetest.Ms(9e12) })))
	if f.Count("setDownloadOptions") != 3 {
		t.Fatal(f.Count("setDownloadOptions"))
	}
}

func TestProgressTellsAMissingFromARemovedDownload(t *testing.T) {
	f := statetest.NewDaemon(running(1, nil))
	p, _ := progressStore(t, f, 9)
	p.Start(0)
	if !p.Missing() || p.Removed() || p.Download() != nil || p.LoadError() != "" {
		t.Fatal(p.Missing(), p.Removed())
	}
	q, _ := progressStore(t, f, 1)
	q.Start(0)
	f.Emit(statetest.Removed(1))
	if q.Missing() || !q.Removed() {
		t.Fatal(q.Missing(), q.Removed())
	}
	// The bridge reports a download it cannot watch as removed.
	r, _ := progressStore(t, statetest.NewDaemon(), 5)
	r.Apply(state.Batch{statetest.Removed(5)})
	if !r.Missing() {
		t.Fatal("not missing")
	}
}

func TestProgressReportsAReadThatFailedOtherwise(t *testing.T) {
	f := statetest.NewDaemon(running(1, nil))
	f.SetDown(true)
	p, c := progressStore(t, f, 1)
	p.Start(time.Second)
	if p.LoadError() == "" || p.Download() != nil || !p.Ready() || p.Settings() != nil {
		t.Fatal(p.LoadError())
	}
	// The next check finds downexd and reads again.
	c.Advance(time.Second)
	if p.Connected() {
		t.Fatal("connected while down")
	}
	f.SetDown(false)
	c.Advance(time.Second)
	if !p.Connected() || p.Download() == nil || p.LoadError() != "" || p.Settings() == nil {
		t.Fatal(p.Connected(), p.Download(), p.LoadError())
	}
}

func TestProgressKeepsTheConnectionsOfEventsOverReads(t *testing.T) {
	conns := []*downexv1.ConnectionInfo{statetest.Connection(1, nil), statetest.Connection(2, nil)}
	f := statetest.NewDaemon(running(1, func(d *downexv1.Download) { d.CompletionDialog = true }))
	p, _ := progressStore(t, f, 1)
	p.Start(0)
	stored := f.Item(1).GetUpdatedAt()
	f.Emit(statetest.Updated(running(1, func(d *downexv1.Download) {
		d.CompletionDialog, d.ConnectionInfo, d.UpdatedAt = true, conns, stored
	})))
	if len(p.Download().GetConnectionInfo()) != 2 {
		t.Fatal(p.Download())
	}
	// A read of a running download (without connections) keeps the ones last reported.
	p.Refresh()
	if p.Download().GetState() != state.StateDownloading || len(p.Download().GetConnectionInfo()) != 2 {
		t.Fatal(p.Download())
	}
	// A pause answers without connections and is not running: they go.
	_ = p.Pause(ctx)
	if p.Download().GetState() != state.StatePaused || len(p.Download().GetConnectionInfo()) != 0 {
		t.Fatal(p.Download())
	}
	// Older reports are ignored.
	f.Emit(statetest.Updated(running(1, func(d *downexv1.Download) { d.UpdatedAt = statetest.Ms(1) })))
	if p.Download().GetState() != state.StatePaused {
		t.Fatal(p.Download())
	}
}

func TestProgressChangesOptionsAndTheCompleteDialogSetting(t *testing.T) {
	f := statetest.NewDaemon(running(1, func(d *downexv1.Download) { d.CompletionDialog = true }))
	unknown := proto.CloneOf(f.Settings())
	unknown.ProtoReflect().SetUnknown([]byte{0xf8, 0x07, 0x01}) // field 127, varint 1: a field this build does not know
	f.SetSettings(unknown)
	p, _ := progressStore(t, f, 1)
	p.Start(0)
	d, err := p.SetOptions(ctx, proto.Int64(4096), nil)
	if err != nil || d.GetSpeedLimit() != 4096 || p.Download().GetSpeedLimit() != 4096 {
		t.Fatal(d, err)
	}
	if err := p.SetShowCompleteDialog(ctx, false); err != nil {
		t.Fatal(err)
	}
	saved := f.Settings()
	if saved.GetShowCompleteDialog() || p.Settings().GetShowCompleteDialog() || len(saved.ProtoReflect().GetUnknown()) != 3 ||
		saved.GetDefaultDir() != "/home/you/Downloads" || !saved.GetShowProgressWindow() {
		t.Fatal(saved)
	}
}
