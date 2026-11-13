package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/testserver"
)

// The download complete dialog (docs/DESKTOP-SPEC.md §8): completion_dialog, started_at,
// active_ms and the window the daemon opens when a marked download ends.

// fakeClock is a Config.Now that moves only when told to.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// waitLaunches waits until windows were opened for want (in order) and checks that no
// other launch follows shortly after.
func waitLaunches(t *testing.T, windows *launcher, want ...int64) {
	t.Helper()
	waitUntil(t, "the download complete dialog", func() bool { return len(windows.launched()) >= len(want) })
	time.Sleep(150 * time.Millisecond) // a duplicate launch would be on its way by now
	if got := windows.launched(); !slices.Equal(got, want) {
		t.Fatalf("windows opened for %v, want %v", got, want)
	}
}

// TestCompletionDialogOnComplete marks a running download (the UI opened its progress
// window): the mark shows at once, is stored when the run stops, and the completion
// opens the window exactly once.
func TestCompletionDialogOnComplete(t *testing.T) {
	fm := newFakeMedia(2)
	fm.gate = make(chan struct{})
	windows := &launcher{}
	h := newHarness(t, harnessOptions{media: fm, openProgress: windows.open})
	d := h.addMedia(&downexv1.AddMediaRequest{ManifestUrl: testManifest, Source: downexv1.AddSource_ADD_SOURCE_UI})
	if d.GetCompletionDialog() {
		t.Fatalf("added marked: %v", d)
	}
	waitUntil(t, "the media run", func() bool { _, runs := fm.calls(); return len(runs) == 1 })

	got, err := h.setOptions(&downexv1.SetDownloadOptionsRequest{Id: d.GetId(), CompletionDialog: ptr(true)})
	if err != nil {
		t.Fatal(err)
	}
	if !got.GetCompletionDialog() || !isRunning(got) || !h.get(d.GetId()).GetCompletionDialog() {
		t.Fatalf("marked while running = %v", got)
	}
	if rec := storedState(t, h.st, d.GetId()); rec.CompletionDialog {
		t.Fatal("stored while the run owns the record")
	}
	fm.feed(t, 2)
	done := h.waitCompleted(d.GetId())
	waitLaunches(t, windows, d.GetId())
	if !done.GetCompletionDialog() || !storedState(t, h.st, d.GetId()).CompletionDialog {
		t.Fatalf("completed = %v, stored %+v", done, storedState(t, h.st, d.GetId()))
	}

	// A completed download keeps its mark changeable, but not its limits.
	if got, err := h.setOptions(&downexv1.SetDownloadOptionsRequest{Id: d.GetId(), CompletionDialog: ptr(false)}); err != nil ||
		got.GetCompletionDialog() || storedState(t, h.st, d.GetId()).CompletionDialog {
		t.Fatalf("unmark completed = %v, %v", got, err)
	}
	if _, err := h.setOptions(&downexv1.SetDownloadOptionsRequest{Id: d.GetId(), CompletionDialog: ptr(true),
		SpeedLimit: ptr(int64(1))}); connectCode(err) != connect.CodeFailedPrecondition {
		t.Fatalf("limit for a completed download: %v", err)
	}
	if got := windows.launched(); len(got) != 1 {
		t.Fatalf("windows opened for %v", got)
	}
}

// TestCompletionDialogOnFailure marks a stopped download (stored at once): each of its
// failures opens the window once.
func TestCompletionDialogOnFailure(t *testing.T) {
	missing := serve(t, testserver.Options{Size: 1, FailFirst: 1000, FailStatus: 404})
	windows := &launcher{}
	h := newHarness(t, harnessOptions{openProgress: windows.open})
	d := h.add(&downexv1.AddRequest{Url: missing.URL + "/gone.bin", StartPaused: true})
	got, err := h.setOptions(&downexv1.SetDownloadOptionsRequest{Id: d.GetId(), CompletionDialog: ptr(true)})
	if err != nil {
		t.Fatal(err)
	}
	if !got.GetCompletionDialog() || got.GetState() != stPaused || !storedState(t, h.st, d.GetId()).CompletionDialog {
		t.Fatalf("marked while paused = %v", got)
	}
	h.resume(&downexv1.ResumeRequest{Id: d.GetId()})
	h.waitState(d.GetId(), stFailed)
	waitLaunches(t, windows, d.GetId())

	// Retry fails again: one more window, for the new failure.
	h.resume(&downexv1.ResumeRequest{Id: d.GetId()})
	h.waitFor(d.GetId(), "the second failure", func(x *downexv1.Download) bool {
		return x.GetState() == stFailed && len(windows.launched()) == 2
	})
	waitLaunches(t, windows, d.GetId(), d.GetId())
}

// TestCompletionDialogNotOpened: no window for unmarked downloads, for marked ones that
// are paused or removed, or while show_complete_dialog is off.
func TestCompletionDialogNotOpened(t *testing.T) {
	ts := serve(t, testserver.Options{Size: 16 << 10, Seed: 91})
	fm := newFakeMedia(3)
	fm.gate = make(chan struct{})
	windows := &launcher{}
	h := newHarness(t, harnessOptions{media: fm, openProgress: windows.open, settings: func(s *downexv1.Settings) {
		s.MaxConcurrentDownloads = 4
	}})
	ctx := ctxT(t)

	// Unmarked: completes without a window.
	plain := h.add(&downexv1.AddRequest{Url: ts.URL + "/plain.bin"})
	h.waitCompleted(plain.GetId())

	// Marked, then paused; marked, then removed while running.
	paused := h.addMedia(&downexv1.AddMediaRequest{ManifestUrl: testManifest})
	removed := h.addMedia(&downexv1.AddMediaRequest{ManifestUrl: testManifest})
	waitUntil(t, "both media runs", func() bool { _, runs := fm.calls(); return len(runs) == 2 })
	for _, id := range []int64{paused.GetId(), removed.GetId()} {
		if _, err := h.setOptions(&downexv1.SetDownloadOptionsRequest{Id: id, CompletionDialog: ptr(true)}); err != nil {
			t.Fatal(err)
		}
	}
	if p := h.pause(paused.GetId()); p.GetState() != stPaused || !p.GetCompletionDialog() {
		t.Fatalf("paused = %v", p)
	}
	if _, err := h.dl.Remove(ctx, connect.NewRequest(&downexv1.RemoveRequest{Id: removed.GetId()})); err != nil {
		t.Fatal(err)
	}

	// Marked while show_complete_dialog is off: completes without a window.
	s := protoSettings(h.m.Settings())
	s.ShowCompleteDialog = false
	if _, err := h.m.UpdateSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
	off := h.add(&downexv1.AddRequest{Url: ts.URL + "/off.bin", StartPaused: true})
	if _, err := h.setOptions(&downexv1.SetDownloadOptionsRequest{Id: off.GetId(), CompletionDialog: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	h.resume(&downexv1.ResumeRequest{Id: off.GetId()})
	h.waitCompleted(off.GetId())

	// Positive control: with the setting on again, a marked download opens one; it is
	// the only window of the test.
	s.ShowCompleteDialog = true
	if _, err := h.m.UpdateSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
	on := h.add(&downexv1.AddRequest{Url: ts.URL + "/on.bin", StartPaused: true})
	if _, err := h.setOptions(&downexv1.SetDownloadOptionsRequest{Id: on.GetId(), CompletionDialog: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	h.resume(&downexv1.ResumeRequest{Id: on.GetId()})
	h.waitCompleted(on.GetId())
	waitLaunches(t, windows, on.GetId())
}

// TestCompletionDialogLaunchFailureIsLogged: a window that cannot be opened changes
// nothing about the download.
func TestCompletionDialogLaunchFailureIsLogged(t *testing.T) {
	ts := serve(t, testserver.Options{Size: 4 << 10, Seed: 92})
	windows := &launcher{err: errors.New("no window")}
	h := newHarness(t, harnessOptions{openProgress: windows.open})
	d := h.add(&downexv1.AddRequest{Url: ts.URL + "/a.bin", StartPaused: true})
	if _, err := h.setOptions(&downexv1.SetDownloadOptionsRequest{Id: d.GetId(), CompletionDialog: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	h.resume(&downexv1.ResumeRequest{Id: d.GetId()})
	checkFile(t, h.waitCompleted(d.GetId()), 92, 4<<10)
	waitLaunches(t, windows, d.GetId())
}

// TestActiveTimeExcludesPauses times a file download through the real engine with a
// fake clock: started_at is the first run's start, and active_ms adds up the runs only
// (the engine's own writes keep the earlier total).
func TestActiveTimeExcludesPauses(t *testing.T) {
	const size, seed = 256 << 10, 93
	ts := serve(t, testserver.Options{Size: size, Seed: seed})
	clk := newFakeClock()
	h := newHarness(t, harnessOptions{now: clk.Now})
	t0 := clk.Now()

	d := h.add(&downexv1.AddRequest{Url: ts.URL + "/timed.bin", SpeedLimit: 16 << 10})
	if d.GetActiveMs() != 0 || (d.GetStartedAt() != nil && !d.GetStartedAt().AsTime().Equal(t0)) {
		t.Fatalf("added = %v", d)
	}
	h.waitReceived(d.GetId(), 1)
	clk.advance(4 * time.Second)
	if g := h.get(d.GetId()); g.GetActiveMs() != 4000 || !g.GetStartedAt().AsTime().Equal(t0) {
		t.Fatalf("running: active %d ms, started %v", g.GetActiveMs(), g.GetStartedAt().AsTime())
	}
	p := h.pause(d.GetId())
	if p.GetActiveMs() != 4000 || !p.GetStartedAt().AsTime().Equal(t0) {
		t.Fatalf("paused: active %d ms, started %v", p.GetActiveMs(), p.GetStartedAt())
	}
	if rec := storedState(t, h.st, d.GetId()); rec.ActiveMS != 4000 || !rec.StartedAt.Equal(t0) {
		t.Fatalf("stored after pause: %d ms, started %v", rec.ActiveMS, rec.StartedAt)
	}

	clk.advance(time.Hour) // paused: not counted
	if g := h.get(d.GetId()); g.GetActiveMs() != 4000 {
		t.Fatalf("paused an hour: active %d ms", g.GetActiveMs())
	}
	h.resume(&downexv1.ResumeRequest{Id: d.GetId()})
	h.waitFor(d.GetId(), "the second run", isRunning)
	clk.advance(1500 * time.Millisecond)
	if _, err := h.setOptions(&downexv1.SetDownloadOptionsRequest{Id: d.GetId(), SpeedLimit: ptr(int64(0))}); err != nil {
		t.Fatal(err)
	}
	done := h.waitCompleted(d.GetId())
	checkFile(t, done, seed, size)
	if done.GetActiveMs() != 5500 || !done.GetStartedAt().AsTime().Equal(t0) {
		t.Fatalf("completed: active %d ms, started %v", done.GetActiveMs(), done.GetStartedAt().AsTime())
	}
	if rec := storedState(t, h.st, d.GetId()); rec.ActiveMS != 5500 || !rec.StartedAt.Equal(t0) {
		t.Fatalf("stored after completion: %d ms, started %v", rec.ActiveMS, rec.StartedAt)
	}
}

// TestCompletedLogHasAverageSpeed: the "download completed" record carries the running time
// of all runs (active_ms) and the average speed over it (avg_bps), so whole downloads can be
// compared from the log.
func TestCompletedLogHasAverageSpeed(t *testing.T) {
	const size, seed = 256 << 10, 94
	ts := serve(t, testserver.Options{Size: size, Seed: seed})
	clk := newFakeClock()
	var logs logBuffer
	h := newHarness(t, harnessOptions{now: clk.Now, log: slog.New(slog.NewJSONHandler(&logs, nil))})

	d := h.add(&downexv1.AddRequest{Url: ts.URL + "/avg.bin", SpeedLimit: 16 << 10})
	h.waitReceived(d.GetId(), 1)
	clk.advance(4 * time.Second)
	if _, err := h.setOptions(&downexv1.SetDownloadOptionsRequest{Id: d.GetId(), SpeedLimit: ptr(int64(0))}); err != nil {
		t.Fatal(err)
	}
	checkFile(t, h.waitCompleted(d.GetId()), seed, size)
	var recs []map[string]any
	waitUntil(t, "the completion record", func() bool { recs = logs.records("download completed"); return len(recs) > 0 })
	// 256 KiB in 4 s of running: 64 KiB/s
	if rec := recs[0]; rec["download"] != float64(d.GetId()) || rec["bytes"] != float64(size) ||
		rec["active_ms"] != float64(4000) || rec["avg_bps"] != float64(size/4) {
		t.Fatalf("completion record = %v", rec)
	}
}

// TestDownloadAgainStartsTheTimeOver: downloading a completed download again discards its
// running time with its bytes, so the new copy's live and final active_ms, started_at and
// completion record are its own (docs/DESKTOP-SPEC.md §8).
func TestDownloadAgainStartsTheTimeOver(t *testing.T) {
	const size, seed = 256 << 10, 95
	ts := serve(t, testserver.Options{Size: size, Seed: seed})
	clk := newFakeClock()
	var logs logBuffer
	h := newHarness(t, harnessOptions{now: clk.Now, log: slog.New(slog.NewJSONHandler(&logs, nil))})

	d := h.add(&downexv1.AddRequest{Url: ts.URL + "/again.bin", SpeedLimit: 16 << 10})
	h.waitReceived(d.GetId(), 1)
	clk.advance(4 * time.Second)
	if _, err := h.setOptions(&downexv1.SetDownloadOptionsRequest{Id: d.GetId(), SpeedLimit: ptr(int64(0))}); err != nil {
		t.Fatal(err)
	}
	if first := h.waitCompleted(d.GetId()); first.GetActiveMs() != 4000 {
		t.Fatalf("first copy: active %d ms", first.GetActiveMs())
	}

	t1 := clk.Now()
	again := h.resume(&downexv1.ResumeRequest{Id: d.GetId(), Restart: true, SpeedLimit: ptr(int64(16 << 10))})
	if again.GetActiveMs() != 0 || (again.GetStartedAt() != nil && !again.GetStartedAt().AsTime().Equal(t1)) {
		t.Fatalf("downloaded again: active %d ms, started %v", again.GetActiveMs(), again.GetStartedAt().AsTime())
	}
	if r := h.waitReceived(d.GetId(), 1); r.GetActiveMs() != 0 {
		t.Fatalf("new copy running: active %d ms", r.GetActiveMs())
	}
	clk.advance(time.Second)
	if _, err := h.setOptions(&downexv1.SetDownloadOptionsRequest{Id: d.GetId(), SpeedLimit: ptr(int64(0))}); err != nil {
		t.Fatal(err)
	}
	second := h.waitCompleted(d.GetId())
	checkFile(t, second, seed, size)
	if second.GetActiveMs() != 1000 || !second.GetStartedAt().AsTime().Equal(t1) {
		t.Fatalf("new copy completed: active %d ms, started %v", second.GetActiveMs(), second.GetStartedAt().AsTime())
	}
	var recs []map[string]any
	waitUntil(t, "two completion records", func() bool { recs = logs.records("download completed"); return len(recs) >= 2 })
	// 256 KiB in the new copy's 1 s
	if rec := recs[1]; rec["active_ms"] != float64(1000) || rec["avg_bps"] != float64(size) {
		t.Fatalf("second completion record = %v", rec)
	}
}

// TestRestartWhileRunningStartsTheTimeOver: a Restart of a running download discards the
// time of the run it stops along with that run's bytes.
func TestRestartWhileRunningStartsTheTimeOver(t *testing.T) {
	const size, seed = 256 << 10, 96
	ts := serve(t, testserver.Options{Size: size, Seed: seed})
	clk := newFakeClock()
	h := newHarness(t, harnessOptions{now: clk.Now})

	d := h.add(&downexv1.AddRequest{Url: ts.URL + "/restart.bin", SpeedLimit: 16 << 10})
	h.waitReceived(d.GetId(), 1)
	clk.advance(2 * time.Second)
	t1 := clk.Now()
	r := h.resume(&downexv1.ResumeRequest{Id: d.GetId(), Restart: true})
	if r.GetActiveMs() != 0 || (r.GetStartedAt() != nil && !r.GetStartedAt().AsTime().Equal(t1)) {
		t.Fatalf("restarted: active %d ms, started %v", r.GetActiveMs(), r.GetStartedAt().AsTime())
	}
	h.waitReceived(d.GetId(), 1)
	clk.advance(time.Second)
	if _, err := h.setOptions(&downexv1.SetDownloadOptionsRequest{Id: d.GetId(), SpeedLimit: ptr(int64(0))}); err != nil {
		t.Fatal(err)
	}
	done := h.waitCompleted(d.GetId())
	checkFile(t, done, seed, size)
	if done.GetActiveMs() != 1000 || !done.GetStartedAt().AsTime().Equal(t1) {
		t.Fatalf("completed: active %d ms, started %v", done.GetActiveMs(), done.GetStartedAt().AsTime())
	}
	if rec := storedState(t, h.st, d.GetId()); rec.ActiveMS != 1000 || !rec.StartedAt.Equal(t1) {
		t.Fatalf("stored: %d ms, started %v", rec.ActiveMS, rec.StartedAt)
	}
}

// TestDownloadAgainMediaStartsTheTimeOver: a completed media download downloaded again
// (restartMedia) starts its running time over too.
func TestDownloadAgainMediaStartsTheTimeOver(t *testing.T) {
	fm := newFakeMedia(2)
	fm.gate = make(chan struct{})
	clk := newFakeClock()
	h := newHarness(t, harnessOptions{media: fm, now: clk.Now})
	d := h.addMedia(&downexv1.AddMediaRequest{ManifestUrl: testManifest})
	waitUntil(t, "the media run", func() bool { _, runs := fm.calls(); return len(runs) == 1 })
	clk.advance(2 * time.Second)
	fm.feed(t, 2)
	if first := h.waitCompleted(d.GetId()); first.GetActiveMs() != 2000 {
		t.Fatalf("first copy: active %d ms", first.GetActiveMs())
	}

	t1 := clk.Now()
	r := h.resume(&downexv1.ResumeRequest{Id: d.GetId(), Restart: true})
	if r.GetActiveMs() != 0 || (r.GetStartedAt() != nil && !r.GetStartedAt().AsTime().Equal(t1)) {
		t.Fatalf("downloaded again: active %d ms, started %v", r.GetActiveMs(), r.GetStartedAt().AsTime())
	}
	waitUntil(t, "the second media run", func() bool { _, runs := fm.calls(); return len(runs) == 2 })
	clk.advance(time.Second)
	fm.feed(t, 2)
	done := h.waitCompleted(d.GetId())
	if done.GetActiveMs() != 1000 || !done.GetStartedAt().AsTime().Equal(t1) {
		t.Fatalf("new copy completed: active %d ms, started %v", done.GetActiveMs(), done.GetStartedAt().AsTime())
	}
	if rec := storedState(t, h.st, d.GetId()); rec.ActiveMS != 1000 || !rec.StartedAt.Equal(t1) {
		t.Fatalf("stored: %d ms, started %v", rec.ActiveMS, rec.StartedAt)
	}
}

// logBuffer collects the JSON log of a test's daemon, which logs from several goroutines.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// records are the records with message msg so far, oldest first.
func (b *logBuffer) records(msg string) []map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	var recs []map[string]any
	for line := range strings.Lines(b.buf.String()) {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) == nil && rec["msg"] == msg {
			recs = append(recs, rec)
		}
	}
	return recs
}

// TestActiveTimeLiveInWatch: Watch updates of a running (media) download carry the
// running total, which includes the earlier runs.
func TestActiveTimeLiveInWatch(t *testing.T) {
	fm := newFakeMedia(3)
	fm.gate = make(chan struct{})
	clk := newFakeClock()
	h := newHarness(t, harnessOptions{media: fm, now: clk.Now})
	t0 := clk.Now()
	d := h.addMedia(&downexv1.AddMediaRequest{ManifestUrl: testManifest})
	waitUntil(t, "the media run", func() bool { _, runs := fm.calls(); return len(runs) == 1 })
	clk.advance(2 * time.Second)
	fm.feed(t, 1)
	h.pause(d.GetId())
	clk.advance(10 * time.Minute)
	h.resume(&downexv1.ResumeRequest{Id: d.GetId()})
	waitUntil(t, "the second media run", func() bool { _, runs := fm.calls(); return len(runs) == 2 })
	clk.advance(3 * time.Second)

	ctx := ctxT(t)
	stream, err := h.dl.Watch(ctx, connect.NewRequest(&downexv1.WatchRequest{Ids: []int64{d.GetId()}}))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if !stream.Receive() {
		t.Fatalf("snapshot: %v", stream.Err())
	}
	if snap := stream.Msg().GetEvents()[0].GetSnapshot(); snap.GetActiveMs() != 5000 || !snap.GetStartedAt().AsTime().Equal(t0) {
		t.Fatalf("snapshot: active %d ms, started %v", snap.GetActiveMs(), snap.GetStartedAt().AsTime())
	}
	clk.advance(time.Second)
	fm.feed(t, 1)
	var last *downexv1.Download
	for last == nil && stream.Receive() {
		for _, ev := range stream.Msg().GetEvents() {
			if u := ev.GetUpdated(); u != nil && u.GetMedia().GetFragmentsDone() == 2 {
				last = u
			}
		}
	}
	if last == nil || last.GetActiveMs() != 6000 || !isRunning(last) {
		t.Fatalf("update = %v (%v)", last, stream.Err())
	}
	fm.feed(t, 1)
	done := h.waitCompleted(d.GetId())
	if done.GetActiveMs() != 6000 || storedState(t, h.st, d.GetId()).ActiveMS != 6000 {
		t.Fatalf("completed: active %d ms, stored %d", done.GetActiveMs(), storedState(t, h.st, d.GetId()).ActiveMS)
	}
}

// TestAccountRun covers the arithmetic: the first start is kept, a clock that went
// backwards adds nothing.
func TestAccountRun(t *testing.T) {
	t0 := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	rec := model.Download{}
	accountRun(&rec, &run{started: t0, stopped: t0.Add(1999 * time.Microsecond)})
	if rec.ActiveMS != 1 || !rec.StartedAt.Equal(t0) {
		t.Fatalf("first run: %+v", rec)
	}
	accountRun(&rec, &run{started: t0.Add(time.Hour), stopped: t0.Add(time.Hour + 2*time.Second)})
	accountRun(&rec, &run{started: t0.Add(2 * time.Hour), stopped: t0.Add(time.Hour)})
	if rec.ActiveMS != 2001 || !rec.StartedAt.Equal(t0) {
		t.Fatalf("later runs: %+v", rec)
	}
}
