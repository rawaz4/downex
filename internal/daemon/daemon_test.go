package daemon

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"downex/internal/avscan"
	"downex/internal/engine"
	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/testserver"
)

const (
	stQueued      = downexv1.DownloadState_DOWNLOAD_STATE_QUEUED
	stDownloading = downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING
	stPaused      = downexv1.DownloadState_DOWNLOAD_STATE_PAUSED
	stCompleted   = downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED
	stFailed      = downexv1.DownloadState_DOWNLOAD_STATE_FAILED
)

func TestAddCompletesAndWatchStreamsIt(t *testing.T) {
	const size, seed = 3<<20 + 123, 7
	ts := serve(t, testserver.Options{Size: size, Seed: seed, ETag: `"v1"`, Filename: "report.bin"})
	h := newHarness(t, harnessOptions{})

	ctx := ctxT(t)
	stream, err := h.dl.Watch(ctx, connect.NewRequest(&downexv1.WatchRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if !stream.Receive() {
		t.Fatalf("no snapshot batch: %v", stream.Err())
	}
	// Without downloads the snapshot holds only the main queue (docs/M6-SPEC.md §1) and
	// Polite mode's status (docs/POLITE-SPEC.md §7).
	if evs := stream.Msg().GetEvents(); len(evs) != 2 || evs[0].GetQueueUpdated().GetId() != 1 ||
		evs[1].GetPolite().GetState() != downexv1.PoliteState_POLITE_STATE_OFF {
		t.Fatalf("snapshot of an empty daemon = %v", evs)
	}

	d := h.add(&downexv1.AddRequest{Url: ts.URL + "/file", SpeedLimit: 4 << 20})
	if d.GetId() == 0 || d.GetDir() != h.dir || d.GetMaxConnections() != DefaultMaxConnections {
		t.Fatalf("added download = %v", d)
	}
	var (
		states  []downexv1.DownloadState
		batches []time.Time
		final   *downexv1.Download
	)
	for final == nil && stream.Receive() {
		batches = append(batches, time.Now())
		for _, ev := range stream.Msg().GetEvents() {
			if ev.GetQueueUpdated() != nil { // the main queue lists the new download
				continue
			}
			u := ev.GetUpdated()
			if u == nil || u.GetId() != d.GetId() {
				t.Fatalf("unexpected event %v", ev)
			}
			states = append(states, u.GetState())
			if u.GetState() == stFailed {
				t.Fatalf("download failed: %s", u.GetError())
			}
			if u.GetState() == stCompleted {
				final = u
			}
		}
	}
	if final == nil {
		t.Fatalf("stream ended before completion: %v", stream.Err())
	}
	if final.GetFilename() != "report.bin" || final.GetSize() != size || final.GetReceived() != size ||
		final.GetMode() != downexv1.Mode_MODE_RANGED || final.GetCompletedAt() == nil || final.GetSegments() != nil {
		t.Fatalf("completed event = %v", final)
	}
	checkFile(t, final, seed, size)
	for i := 1; i < len(batches); i++ {
		// Client-side receive times; the server spaces batches by 250 ms (checked
		// exactly in TestWatchBatchRate), so a much smaller gap means no rate limit.
		if gap := batches[i].Sub(batches[i-1]); gap < 150*time.Millisecond {
			t.Errorf("batches %d and %d arrived %v apart", i-1, i, gap)
		}
	}

	// A new watcher gets the completed download in its snapshot.
	s2, err := h.dl.Watch(ctx, connect.NewRequest(&downexv1.WatchRequest{Ids: []int64{d.GetId()}}))
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if !s2.Receive() {
		t.Fatal(s2.Err())
	}
	evs := s2.Msg().GetEvents()
	if len(evs) != 1 || evs[0].GetSnapshot().GetState() != stCompleted || evs[0].GetSnapshot().GetReceived() != size {
		t.Fatalf("snapshot = %v", evs)
	}
	st := h.status()
	if st.GetActive() != 0 || st.GetQueued() != 0 || st.GetVersion() != "test-version" ||
		st.GetEndpoint() != "test-endpoint" || st.GetPid() != int32(os.Getpid()) || st.GetStartedAt() == nil {
		t.Fatalf("status = %v", st)
	}
	t.Logf("states seen: %v in %d batches", states, len(batches))
}

func TestPauseResumeKeepsProgress(t *testing.T) {
	const size, seed = 8 << 20, 11
	ts := serve(t, testserver.Options{Size: size, Seed: seed, ETag: `"e1"`})
	h := newHarness(t, harnessOptions{})

	d := h.add(&downexv1.AddRequest{Url: ts.URL + "/big.bin", SpeedLimit: 1 << 20})
	h.waitReceived(d.GetId(), 512<<10)
	p := h.pause(d.GetId())
	if p.GetState() != stPaused || p.GetReceived() <= 0 || p.GetReceived() >= size || p.GetSpeedBps() != 0 {
		t.Fatalf("paused download = %v", p)
	}
	saved := p.GetReceived()
	if _, err := os.Stat(engine.PartPath(p.GetDir(), p.GetFilename())); err != nil {
		t.Fatalf("part file: %v", err)
	}
	if got := storedState(t, h.st, d.GetId()); got.State != model.StatePaused {
		t.Fatalf("stored state = %v", got.State)
	}
	// Pausing again is a no-op.
	if again := h.pause(d.GetId()); again.GetState() != stPaused || again.GetReceived() != saved {
		t.Fatalf("second pause = %v", again)
	}
	time.Sleep(300 * time.Millisecond)
	if again := h.get(d.GetId()); again.GetReceived() != saved {
		t.Fatalf("paused download moved from %d to %d bytes", saved, again.GetReceived())
	}

	before := len(ts.Requests())
	unlimited := int64(0)
	r := h.resume(&downexv1.ResumeRequest{Id: d.GetId(), SpeedLimit: &unlimited})
	if r.GetSpeedLimit() != 0 {
		t.Fatalf("resumed download keeps speed limit %d", r.GetSpeedLimit())
	}
	done := h.waitCompleted(d.GetId())
	checkFile(t, done, seed, size)
	for _, req := range ts.Requests()[before:] {
		if strings.HasPrefix(req.Range, "bytes=0-") {
			t.Errorf("resumed download requested %q: progress was discarded", req.Range)
		}
		if req.IfRange != `"e1"` {
			t.Errorf("resumed request without If-Range: %+v", req)
		}
	}
}

func TestConcurrencyLimitIsFIFO(t *testing.T) {
	ts := serve(t, testserver.Options{Size: 4 << 20, Seed: 3})
	h := newHarness(t, harnessOptions{})

	var ids []int64
	for i := range 5 {
		d := h.add(&downexv1.AddRequest{Url: ts.URL + "/f" + string(rune('a'+i)), SpeedLimit: 64 << 10})
		ids = append(ids, d.GetId())
	}
	waitUntil(t, "3 active and 2 queued", func() bool {
		s := h.status()
		return s.GetActive() == 3 && s.GetQueued() == 2
	})
	check := func(running []int64, queued []int64) {
		t.Helper()
		byID := map[int64]*downexv1.Download{}
		for _, d := range h.list() {
			byID[d.GetId()] = d
		}
		for _, id := range running {
			if d := byID[id]; !isRunning(d) || d.GetQueuePosition() != 0 {
				t.Fatalf("download %d = %v, want running", id, d.GetState())
			}
		}
		for i, id := range queued {
			if d := byID[id]; d.GetState() != stQueued || d.GetQueuePosition() != int32(i+1) {
				t.Fatalf("download %d = %v at position %d, want queued at %d", id, d.GetState(), d.GetQueuePosition(), i+1)
			}
		}
	}
	check(ids[:3], ids[3:])

	// Pausing a running download starts the oldest queued one.
	h.pause(ids[1])
	waitUntil(t, "download 4 to start", func() bool { return isRunning(h.get(ids[3])) })
	check([]int64{ids[0], ids[2], ids[3]}, []int64{ids[4]})
	if s := h.status(); s.GetActive() != 3 || s.GetQueued() != 1 {
		t.Fatalf("status = %v", s)
	}
	// downexd's idle check (platform.ReleaseMemoryAndTrimWhileIdle): 4 runs started, 3 running.
	if n, runs := h.m.Active(); n != 3 || runs != 4 {
		t.Fatalf("Active() = %d running, %d started, want 3 and 4", n, runs)
	}

	// A resumed download is queued by id, ahead of newer ones.
	if r := h.resume(&downexv1.ResumeRequest{Id: ids[1]}); r.GetState() != stQueued || r.GetQueuePosition() != 1 {
		t.Fatalf("resumed = %v position %d", r.GetState(), r.GetQueuePosition())
	}
	check([]int64{ids[0], ids[2], ids[3]}, []int64{ids[1], ids[4]})
	if peak := ts.MaxConcurrent(); peak > 3*DefaultMaxConnections {
		t.Fatalf("server saw %d concurrent requests", peak)
	}
}

func TestRemoveRunningDeletesPartFile(t *testing.T) {
	ts := serve(t, testserver.Options{Size: 4 << 20, Seed: 5})
	h := newHarness(t, harnessOptions{})

	d := h.add(&downexv1.AddRequest{Url: ts.URL + "/x", Filename: "keep me.bin", SpeedLimit: 128 << 10})
	if d.GetFilename() != "keep me.bin" {
		t.Fatalf("filename = %q", d.GetFilename())
	}
	part := engine.PartPath(d.GetDir(), d.GetFilename())
	h.waitReceived(d.GetId(), 1)
	if _, err := os.Stat(part); err != nil {
		t.Fatal(err)
	}
	ctx := ctxT(t)
	stream, err := h.dl.Watch(ctx, connect.NewRequest(&downexv1.WatchRequest{Ids: []int64{d.GetId()}}))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if !stream.Receive() || len(stream.Msg().GetEvents()) != 1 {
		t.Fatalf("snapshot: %v", stream.Err())
	}

	if _, err := h.dl.Remove(ctx, connect.NewRequest(&downexv1.RemoveRequest{Id: d.GetId(), DeleteFile: true})); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(part); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("part file after remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(d.GetDir(), d.GetFilename())); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("final file after remove: %v", err)
	}
	if _, err := h.dl.Get(ctx, connect.NewRequest(&downexv1.GetRequest{Id: d.GetId()})); connectCode(err) != connect.CodeNotFound {
		t.Fatalf("get after remove: %v", err)
	}
	if _, err := h.st.GetDownload(ctx, model.DownloadID(d.GetId())); err == nil {
		t.Fatal("record still stored")
	}
	removed := false
	for !removed && stream.Receive() {
		for _, ev := range stream.Msg().GetEvents() {
			if ev.GetRemoved() == d.GetId() {
				removed = true
			}
		}
	}
	if !removed {
		t.Fatalf("no removed event: %v", stream.Err())
	}
	if _, err := h.dl.Remove(ctx, connect.NewRequest(&downexv1.RemoveRequest{Id: d.GetId()})); connectCode(err) != connect.CodeNotFound {
		t.Fatalf("second remove: %v", err)
	}
	if s := h.status(); s.GetActive() != 0 {
		t.Fatalf("status after remove = %v", s)
	}
}

func TestRemoveCompletedKeepsOrDeletesFile(t *testing.T) {
	ts := serve(t, testserver.Options{Size: 100 << 10, Seed: 9})
	h := newHarness(t, harnessOptions{})
	ctx := ctxT(t)

	keep := h.waitCompleted(h.add(&downexv1.AddRequest{Url: ts.URL + "/keep.dat"}).GetId())
	del := h.waitCompleted(h.add(&downexv1.AddRequest{Url: ts.URL + "/delete.dat"}).GetId())
	if _, err := h.dl.Remove(ctx, connect.NewRequest(&downexv1.RemoveRequest{Id: keep.GetId()})); err != nil {
		t.Fatal(err)
	}
	if _, err := h.dl.Remove(ctx, connect.NewRequest(&downexv1.RemoveRequest{Id: del.GetId(), DeleteFile: true})); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(keep.GetDir(), keep.GetFilename())); err != nil {
		t.Fatalf("kept file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(del.GetDir(), del.GetFilename())); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("deleted file: %v", err)
	}
	if n := len(h.list()); n != 0 {
		t.Fatalf("%d downloads left", n)
	}
}

func TestRestartAfterRemoteChange(t *testing.T) {
	const size = 2 << 20
	ts := serve(t, testserver.Options{Size: size, Seed: 1, ETag: `"v1"`})
	h := newHarness(t, harnessOptions{})

	d := h.add(&downexv1.AddRequest{Url: ts.URL + "/changing.iso", SpeedLimit: 256 << 10})
	h.waitReceived(d.GetId(), 128<<10)
	h.pause(d.GetId())
	ts.Update(func(o *testserver.Options) { o.ETag, o.Seed = `"v2"`, 2 })

	unlimited := int64(0)
	h.resume(&downexv1.ResumeRequest{Id: d.GetId(), SpeedLimit: &unlimited})
	failed := h.waitState(d.GetId(), stFailed)
	if failed.GetErrorCode() != CodeValidatorChanged || failed.GetError() == "" {
		t.Fatalf("failed download = %v", failed)
	}

	r := h.resume(&downexv1.ResumeRequest{Id: d.GetId(), Restart: true})
	if r.GetError() != "" || r.GetErrorCode() != "" {
		t.Fatalf("restarted download still reports %q", r.GetError())
	}
	done := h.waitCompleted(d.GetId())
	checkFile(t, done, 2, size)
	// A completed download is resumed only to download it again (restart, again_test.go).
	if _, err := h.dl.Resume(ctxT(t), connect.NewRequest(&downexv1.ResumeRequest{Id: d.GetId()})); connectCode(err) != connect.CodeFailedPrecondition {
		t.Fatalf("resume of a completed download: %v", err)
	}
}

func TestRestartRunningDownload(t *testing.T) {
	const size, seed = 2 << 20, 4
	ts := serve(t, testserver.Options{Size: size, Seed: seed, ETag: `"r"`})
	h := newHarness(t, harnessOptions{})

	d := h.add(&downexv1.AddRequest{Url: ts.URL + "/r.bin", SpeedLimit: 256 << 10})
	h.waitReceived(d.GetId(), 64<<10)
	before := len(ts.Requests())
	unlimited := int64(0)
	h.resume(&downexv1.ResumeRequest{Id: d.GetId(), Restart: true, SpeedLimit: &unlimited})
	done := h.waitCompleted(d.GetId())
	checkFile(t, done, seed, size)
	if done.GetSpeedLimit() != 0 {
		t.Fatalf("speed limit = %d", done.GetSpeedLimit())
	}
	fresh := false
	for _, req := range ts.Requests()[before:] {
		fresh = fresh || (req.Range == "bytes=0-" && req.IfRange == "")
	}
	if !fresh {
		t.Fatal("restart did not probe the file again from byte 0")
	}
}

func TestShutdownPausesAllAndRecoveryRequeues(t *testing.T) {
	const size, seed = 4 << 20, 21
	ts := serve(t, testserver.Options{Size: size, Seed: seed, ETag: `"s"`})
	h1 := newHarness(t, harnessOptions{})
	ctx := ctxT(t)

	var ids []int64
	for i := range 4 {
		ids = append(ids, h1.add(&downexv1.AddRequest{Url: ts.URL + "/s" + string(rune('0'+i)), SpeedLimit: 128 << 10}).GetId())
	}
	for _, id := range ids[:3] {
		h1.waitReceived(id, 1)
	}
	stream, err := h1.dl.Watch(ctx, connect.NewRequest(&downexv1.WatchRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if !stream.Receive() {
		t.Fatal(stream.Err())
	}

	if _, err := h1.dm.Shutdown(ctx, connect.NewRequest(&downexv1.ShutdownRequest{})); err != nil {
		t.Fatal(err)
	}
	select {
	case <-h1.shutdowns:
	default:
		t.Fatal("onShutdown was not called")
	}
	// The Watch stream ends with Unavailable after reporting the stopped downloads.
	for stream.Receive() {
	}
	if connectCode(stream.Err()) != connect.CodeUnavailable {
		t.Fatalf("watch ended with %v", stream.Err())
	}
	if s := h1.status(); s.GetActive() != 0 {
		t.Fatalf("status after shutdown = %v", s)
	}
	saved := map[int64]int64{}
	for _, id := range ids {
		rec := storedState(t, h1.st, id)
		if rec.State != model.StateQueued {
			t.Fatalf("download %d stored as %v after shutdown, want queued", id, rec.State)
		}
		segs, err := h1.st.LoadSegments(ctx, model.DownloadID(id))
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range segs {
			saved[id] += s.Durable
		}
	}
	for _, id := range ids[:3] {
		if saved[id] == 0 {
			t.Fatalf("download %d has no committed progress after shutdown", id)
		}
	}
	if _, err := h1.dl.Add(ctx, connect.NewRequest(&downexv1.AddRequest{Url: ts.URL + "/late"})); connectCode(err) != connect.CodeUnavailable {
		t.Fatalf("add after shutdown: %v", err)
	}
	ws, err := h1.dl.Watch(ctx, connect.NewRequest(&downexv1.WatchRequest{}))
	if err == nil {
		for ws.Receive() {
		}
		err = ws.Err()
		ws.Close()
	}
	if connectCode(err) != connect.CodeUnavailable {
		t.Fatalf("watch after shutdown: %v", err)
	}

	// A crash leaves a download "downloading"; the next start requeues it as well.
	rec := storedState(t, h1.st, ids[3])
	rec.State = model.StateDownloading
	if err := h1.st.UpdateDownload(ctx, &rec); err != nil {
		t.Fatal(err)
	}

	before := len(ts.Requests())
	h2 := newHarness(t, harnessOptions{st: h1.st})
	waitUntil(t, "recovered downloads to run", func() bool {
		s := h2.status()
		return s.GetActive() == 3 && s.GetQueued() == 1
	})
	unlimited := int64(0)
	for _, id := range ids {
		h2.resume(&downexv1.ResumeRequest{Id: id, SpeedLimit: &unlimited})
	}
	for _, id := range ids {
		checkFile(t, h2.waitCompleted(id), seed, size)
	}
	// Only the download that never ran probes from byte 0; the others resume.
	fresh := 0
	for _, req := range ts.Requests()[before:] {
		if req.Range == "bytes=0-" && req.IfRange == "" {
			fresh++
		}
	}
	if fresh != 1 {
		t.Errorf("%d downloads started from the beginning after recovery, want 1", fresh)
	}
}

func TestSettingsPersistAndApply(t *testing.T) {
	ts := serve(t, testserver.Options{Size: 4 << 20, Seed: 8})
	path := filepath.Join(t.TempDir(), SettingsFileName)
	h := newHarness(t, harnessOptions{settingsPath: path})
	ctx := ctxT(t)

	got, err := h.dm.GetSettings(ctx, connect.NewRequest(&downexv1.GetSettingsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if s := got.Msg.GetSettings(); s.GetMaxConcurrentDownloads() != 3 || s.GetGlobalSpeedLimit() != 0 ||
		s.GetDefaultDir() != h.dir || s.GetDefaultMaxConnections() != DefaultMaxConnections {
		t.Fatalf("default settings = %v", s)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("settings file written before any update: %v", err)
	}

	other := filepath.Join(t.TempDir(), "elsewhere")
	want := &downexv1.Settings{MaxConcurrentDownloads: 1, GlobalSpeedLimit: 1 << 20, DefaultDir: other, DefaultMaxConnections: 4}
	upd, err := h.dm.UpdateSettings(ctx, connect.NewRequest(&downexv1.UpdateSettingsRequest{Settings: want}))
	if err != nil {
		t.Fatal(err)
	}
	if s := upd.Msg.GetSettings(); s.GetMaxConcurrentDownloads() != 1 || s.GetDefaultDir() != other || s.GetDefaultMaxConnections() != 4 {
		t.Fatalf("updated settings = %v", s)
	}
	if lim := h.m.global.Limit(); lim != 1<<20 {
		t.Fatalf("global limit = %d", lim)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), `"max_concurrent_downloads": 1`) {
		t.Fatalf("settings file = %q (%v)", data, err)
	}

	// New downloads use the default directory (created below its existing parent),
	// the default connection limit, and only one runs at a time.
	a := h.add(&downexv1.AddRequest{Url: ts.URL + "/a", SpeedLimit: 64 << 10})
	b := h.add(&downexv1.AddRequest{Url: ts.URL + "/b", SpeedLimit: 64 << 10})
	if a.GetDir() != other || a.GetMaxConnections() != 4 {
		t.Fatalf("added = %v", a)
	}
	if fi, err := os.Stat(other); err != nil || !fi.IsDir() {
		t.Fatalf("default dir not created: %v", err)
	}
	h.waitReceived(a.GetId(), 1)
	time.Sleep(200 * time.Millisecond)
	if d := h.get(b.GetId()); d.GetState() != stQueued || d.GetQueuePosition() != 1 {
		t.Fatalf("second download = %v, want queued", d.GetState())
	}

	// Another daemon reads the persisted settings.
	h2 := newHarness(t, harnessOptions{settingsPath: path})
	// The update named no categories and left the booleans false: settings are replaced
	// as a whole.
	if s := h2.m.Settings(); !s.Equal(Settings{MaxConcurrentDownloads: 1, GlobalSpeedLimit: 1 << 20, DefaultDir: other,
		DefaultMaxConnections: 4, Desktop: DesktopSettings{AVScanArgs: avscan.DefaultArgs}}) {
		t.Fatalf("reloaded settings = %+v", s)
	}
	// Zero values select the defaults.
	upd, err = h2.dm.UpdateSettings(ctx, connect.NewRequest(&downexv1.UpdateSettingsRequest{Settings: &downexv1.Settings{}}))
	if err != nil {
		t.Fatal(err)
	}
	if s := upd.Msg.GetSettings(); s.GetMaxConcurrentDownloads() != 3 || s.GetDefaultMaxConnections() != DefaultMaxConnections || s.GetDefaultDir() != h2.dir {
		t.Fatalf("zero settings = %v", s)
	}
}

func TestLowerMaxConcurrentRequeuesNewest(t *testing.T) {
	ts := serve(t, testserver.Options{Size: 4 << 20, Seed: 13})
	h := newHarness(t, harnessOptions{})
	ctx := ctxT(t)

	var ids []int64
	for i := range 3 {
		ids = append(ids, h.add(&downexv1.AddRequest{Url: ts.URL + "/m" + string(rune('0'+i)), SpeedLimit: 128 << 10}).GetId())
	}
	for _, id := range ids {
		h.waitReceived(id, 1)
	}
	set := func(n int32) {
		t.Helper()
		_, err := h.dm.UpdateSettings(ctx, connect.NewRequest(&downexv1.UpdateSettingsRequest{
			Settings: &downexv1.Settings{MaxConcurrentDownloads: n, DefaultDir: h.dir}}))
		if err != nil {
			t.Fatal(err)
		}
	}
	set(1)
	q2 := h.waitState(ids[1], stQueued)
	q3 := h.waitState(ids[2], stQueued)
	if !isRunning(h.get(ids[0])) {
		t.Fatalf("oldest download stopped: %v", h.get(ids[0]).GetState())
	}
	if q2.GetQueuePosition() != 1 || q3.GetQueuePosition() != 2 || q2.GetReceived() == 0 || q3.GetReceived() == 0 {
		t.Fatalf("requeued = %v / %v", q2, q3)
	}
	time.Sleep(200 * time.Millisecond)
	if s := h.status(); s.GetActive() != 1 || s.GetQueued() != 2 {
		t.Fatalf("status = %v", s)
	}
	set(3)
	waitUntil(t, "all three to run again", func() bool { return h.status().GetActive() == 3 })
}

func TestPauseAndRemoveWhileStarting(t *testing.T) {
	ts := serve(t, testserver.Options{Size: 1 << 20, Seed: 17})
	h := newHarness(t, harnessOptions{})
	ctx := ctxT(t)

	for i := range 15 {
		d := h.add(&downexv1.AddRequest{Url: ts.URL + "/race", SpeedLimit: 64 << 10})
		if i%3 == 1 {
			time.Sleep(time.Duration(i) * time.Millisecond)
		}
		p := h.pause(d.GetId())
		if p.GetState() != stPaused && p.GetState() != stCompleted {
			t.Fatalf("pause right after add: state %v", p.GetState())
		}
		if rec := storedState(t, h.st, d.GetId()); rec.State != model.StatePaused && rec.State != model.StateCompleted {
			t.Fatalf("stored state after pause = %v", rec.State)
		}
		h.resume(&downexv1.ResumeRequest{Id: d.GetId()})
		if i%2 == 0 {
			time.Sleep(time.Duration(i) * time.Millisecond)
		}
		if _, err := h.dl.Remove(ctx, connect.NewRequest(&downexv1.RemoveRequest{Id: d.GetId(), DeleteFile: true})); err != nil {
			t.Fatalf("remove: %v", err)
		}
	}
	waitUntil(t, "no running downloads", func() bool { s := h.status(); return s.GetActive() == 0 && s.GetQueued() == 0 })
	if n := len(h.list()); n != 0 {
		t.Fatalf("%d downloads left", n)
	}
	entries, err := os.ReadDir(h.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("file left behind: %s", e.Name())
	}
}

func TestOperationErrors(t *testing.T) {
	ts := serve(t, testserver.Options{Size: 64 << 10, Seed: 2})
	h := newHarness(t, harnessOptions{})
	ctx := ctxT(t)

	errOf := func(_ any, err error) error { return err }
	for name, err := range map[string]error{
		"get":    errOf(h.dl.Get(ctx, connect.NewRequest(&downexv1.GetRequest{Id: 99}))),
		"pause":  errOf(h.dl.Pause(ctx, connect.NewRequest(&downexv1.PauseRequest{Id: 99}))),
		"resume": errOf(h.dl.Resume(ctx, connect.NewRequest(&downexv1.ResumeRequest{Id: 99}))),
		"remove": errOf(h.dl.Remove(ctx, connect.NewRequest(&downexv1.RemoveRequest{Id: 99}))),
		"zero":   errOf(h.dl.Get(ctx, connect.NewRequest(&downexv1.GetRequest{}))),
	} {
		if connectCode(err) != connect.CodeNotFound {
			t.Errorf("%s of a missing download: %v", name, err)
		}
	}
	s, err := h.dl.Watch(ctx, connect.NewRequest(&downexv1.WatchRequest{Ids: []int64{42}}))
	if err == nil {
		s.Receive()
		err = s.Err()
		s.Close()
	}
	if connectCode(err) != connect.CodeNotFound {
		t.Errorf("watch of a missing download: %v", err)
	}

	done := h.waitCompleted(h.add(&downexv1.AddRequest{Url: ts.URL + "/small"}).GetId())
	if _, err := h.dl.Pause(ctx, connect.NewRequest(&downexv1.PauseRequest{Id: done.GetId()})); connectCode(err) != connect.CodeFailedPrecondition {
		t.Errorf("pause of a completed download: %v", err)
	}
	if _, err := h.dl.Resume(ctx, connect.NewRequest(&downexv1.ResumeRequest{Id: done.GetId()})); connectCode(err) != connect.CodeFailedPrecondition {
		t.Errorf("resume of a completed download: %v", err)
	}

	paused := h.add(&downexv1.AddRequest{Url: ts.URL + "/later", StartPaused: true})
	if paused.GetState() != stPaused {
		t.Fatalf("start_paused download = %v", paused.GetState())
	}
	time.Sleep(100 * time.Millisecond)
	if d := h.get(paused.GetId()); d.GetState() != stPaused || h.status().GetActive() != 0 {
		t.Fatalf("start_paused download started: %v", d.GetState())
	}
	if rec := storedState(t, h.st, paused.GetId()); rec.State != model.StatePaused {
		t.Fatalf("stored = %v", rec.State)
	}
	conns := int32(0)
	r := h.resume(&downexv1.ResumeRequest{Id: paused.GetId(), MaxConnections: &conns})
	if r.GetMaxConnections() != DefaultMaxConnections {
		t.Fatalf("max_connections 0 stored as %d", r.GetMaxConnections())
	}
	h.waitCompleted(paused.GetId())
}

func TestFailedDownloadReportsCodeAndResumes(t *testing.T) {
	ts := serve(t, testserver.Options{Size: 64 << 10, Seed: 6, FailFirst: 1000, FailStatus: 404})
	h := newHarness(t, harnessOptions{})

	d := h.add(&downexv1.AddRequest{Url: ts.URL + "/missing"})
	failed := h.waitState(d.GetId(), stFailed)
	if failed.GetErrorCode() != CodeHTTPStatus || !strings.Contains(failed.GetError(), "404") {
		t.Fatalf("failed = %v", failed)
	}
	if _, err := h.dl.Pause(ctxT(t), connect.NewRequest(&downexv1.PauseRequest{Id: d.GetId()})); connectCode(err) != connect.CodeFailedPrecondition {
		t.Fatalf("pause of a failed download: %v", err)
	}
	ts.Update(func(o *testserver.Options) { o.FailFirst = 0 })
	r := h.resume(&downexv1.ResumeRequest{Id: d.GetId()})
	if r.GetErrorCode() != "" || r.GetError() != "" {
		t.Fatalf("resumed = %v", r)
	}
	checkFile(t, h.waitCompleted(d.GetId()), 6, 64<<10)

	// A daemon started later derives the code from the stored message.
	rec := storedState(t, h.st, d.GetId())
	rec.State, rec.Error = model.StateFailed, (&engine.StatusError{Code: 503}).Error()
	if err := h.st.UpdateDownload(context.Background(), &rec); err != nil {
		t.Fatal(err)
	}
	h2 := newHarness(t, harnessOptions{st: h.st})
	if got := h2.get(d.GetId()); got.GetErrorCode() != CodeHTTPStatus {
		t.Fatalf("recovered failure code = %q", got.GetErrorCode())
	}
}
