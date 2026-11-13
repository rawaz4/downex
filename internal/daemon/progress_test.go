package daemon

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"downex/internal/engine"
	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/store"
	"downex/internal/testserver"
)

// Progress window support (docs/DESKTOP-SPEC.md §7): settings, connection_info,
// SetDownloadOptions and the window launch for browser hand-offs.

func TestProgressWindowSettings(t *testing.T) {
	if s := DefaultSettings(); !s.ShowProgressWindow || !s.ShowCompleteDialog {
		t.Fatalf("defaults = %+v", s)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, SettingsFileName)
	// A file of an older daemon lacks the fields: both are on.
	old := `{"max_concurrent_downloads": 2, "default_dir": ` + jsonString(dir) + `, "show_tray_icon": false}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := loadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if !s.ShowProgressWindow || !s.ShowCompleteDialog || s.ShowTrayIcon {
		t.Fatalf("old file = %+v", s)
	}
	explicit := `{"show_progress_window": false, "show_complete_dialog": false}`
	if err := os.WriteFile(path, []byte(explicit), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, err = loadSettings(path); err != nil || s.ShowProgressWindow || s.ShowCompleteDialog {
		t.Fatalf("explicit file = %+v, %v", s, err)
	}
	// Round trip: false is written and read back as false.
	s = DefaultSettings()
	s.ShowCompleteDialog = false
	if err := saveSettings(path, s); err != nil {
		t.Fatal(err)
	}
	if got, err := loadSettings(path); err != nil || !got.ShowProgressWindow || got.ShowCompleteDialog {
		t.Fatalf("round trip = %+v, %v", got, err)
	}

	// Through the API: defaults on, changes persisted and reported.
	h := newHarness(t, harnessOptions{settingsPath: filepath.Join(t.TempDir(), SettingsFileName)})
	ctx := ctxT(t)
	got, err := h.dm.GetSettings(ctx, connect.NewRequest(&downexv1.GetSettingsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	ps := got.Msg.GetSettings()
	if !ps.GetShowProgressWindow() || !ps.GetShowCompleteDialog() {
		t.Fatalf("API defaults = %v", ps)
	}
	notices, cancel := h.m.Subscribe()
	defer cancel()
	ps.ShowProgressWindow = false
	upd, err := h.dm.UpdateSettings(ctx, connect.NewRequest(&downexv1.UpdateSettingsRequest{Settings: ps}))
	if err != nil {
		t.Fatal(err)
	}
	if us := upd.Msg.GetSettings(); us.GetShowProgressWindow() || !us.GetShowCompleteDialog() {
		t.Fatalf("updated = %v", us)
	}
	if n := nextNotice(t, notices, NoticeSettings); n.Settings.ShowProgressWindow || !n.Settings.ShowCompleteDialog {
		t.Fatalf("settings notice = %+v", n.Settings)
	}
	stored, err := loadSettings(h.settingsPath)
	if err != nil || stored.ShowProgressWindow || !stored.ShowCompleteDialog {
		t.Fatalf("stored = %+v, %v", stored, err)
	}
}

func TestProtoConnections(t *testing.T) {
	if got := protoConnections(nil, false); got != nil {
		t.Fatalf("no workers = %v", got)
	}
	long := strings.Repeat("é", maxConnectionDetail) // 2 bytes each
	views := []model.WorkerView{
		{Slot: 3, State: model.WorkerRetrying, Start: -1, End: 99, Detail: "HTTP 503,\nretrying in 4 s",
			RetryAt: time.Now().Add(4 * time.Second), Fragment: 7},
		{Slot: 1, State: model.WorkerReceiving, Start: 0, End: 1023, Received: 512, SpeedBps: 2048, Fragment: -1},
		{Slot: 0, State: model.WorkerIdle}, // no slot: dropped
		{Slot: 2, State: model.WorkerError, Start: 1024, End: 2047, Received: -5, SpeedBps: math.NaN(), Detail: long},
		{Slot: 4, State: model.WorkerState(99), Start: 5, End: -1, SpeedBps: -1},
	}
	got := protoConnections(views, false)
	want := []*downexv1.ConnectionInfo{
		{Index: 1, State: downexv1.ConnectionState_CONNECTION_STATE_RECEIVING, Start: 0, End: 1023, Received: 512, SpeedBps: 2048, Fragment: -1},
		{Index: 2, State: downexv1.ConnectionState_CONNECTION_STATE_ERROR, Start: 1024, End: 2047, Fragment: -1,
			Detail: strings.Repeat("é", maxConnectionDetail/2)},
		{Index: 3, State: downexv1.ConnectionState_CONNECTION_STATE_RETRYING, Start: -1, End: -1,
			Detail: "HTTP 503, retrying in 4 s", Fragment: -1},
		{Index: 4, State: downexv1.ConnectionState_CONNECTION_STATE_UNSPECIFIED, Start: 5, End: -1, Fragment: -1},
	}
	if !slices.EqualFunc(got, want, func(a, b *downexv1.ConnectionInfo) bool { return proto.Equal(a, b) }) {
		t.Fatalf("connections =\n%v\nwant\n%v", got, want)
	}
	for _, c := range got {
		if !utf8.ValidString(c.GetDetail()) || len(c.GetDetail()) > maxConnectionDetail {
			t.Fatalf("detail %q", c.GetDetail())
		}
	}
	// Media downloads keep the fragment index.
	media := protoConnections(views, true)
	if media[2].GetFragment() != 7 || media[0].GetFragment() != -1 {
		t.Fatalf("media fragments = %v", media)
	}
	states := map[model.WorkerState]downexv1.ConnectionState{
		model.WorkerConnecting: downexv1.ConnectionState_CONNECTION_STATE_CONNECTING,
		model.WorkerRequesting: downexv1.ConnectionState_CONNECTION_STATE_REQUESTING,
		model.WorkerReceiving:  downexv1.ConnectionState_CONNECTION_STATE_RECEIVING,
		model.WorkerRetrying:   downexv1.ConnectionState_CONNECTION_STATE_RETRYING,
		model.WorkerIdle:       downexv1.ConnectionState_CONNECTION_STATE_IDLE,
		model.WorkerDone:       downexv1.ConnectionState_CONNECTION_STATE_DONE,
		model.WorkerError:      downexv1.ConnectionState_CONNECTION_STATE_ERROR,
		model.WorkerUnknown:    downexv1.ConnectionState_CONNECTION_STATE_UNSPECIFIED,
	}
	for in, want := range states {
		if got := protoConnectionState(in); got != want {
			t.Errorf("state %d = %v, want %v", in, got, want)
		}
	}
}

// TestConnectionInfoOnlyWhileRunning renders file downloads: connection_info comes from
// the live progress of a running download only.
func TestConnectionInfoOnlyWhileRunning(t *testing.T) {
	workers := []model.WorkerView{
		{Slot: 2, State: model.WorkerConnecting, Start: 100, End: 199, Fragment: -1},
		{Slot: 1, State: model.WorkerReceiving, Start: 0, End: 99, Received: 10, SpeedBps: 5, Fragment: -1},
	}
	p := &engine.Progress{ID: 1, Mode: model.ModeRanged, Size: 200, Received: 10, Connections: 2, Workers: workers}
	v := downloadView{rec: model.Download{ID: 1, Size: 200, State: model.StateDownloading}, running: true, progress: p}
	pd := v.toProto()
	if len(pd.GetConnectionInfo()) != 2 || pd.GetConnectionInfo()[0].GetIndex() != 1 ||
		pd.GetConnectionInfo()[1].GetState() != downexv1.ConnectionState_CONNECTION_STATE_CONNECTING {
		t.Fatalf("running = %v", pd.GetConnectionInfo())
	}
	v.running = false
	if pd := v.toProto(); len(pd.GetConnectionInfo()) != 0 {
		t.Fatalf("stopped = %v", pd.GetConnectionInfo())
	}
}

// TestMediaConnectionInfoInWatch follows a running media download: its workers reach
// Get and Watch updates as connection_info with their fragment numbers, and vanish
// once it stopped; nothing of them is stored.
func TestMediaConnectionInfoInWatch(t *testing.T) {
	fm := newFakeMedia(4)
	fm.gate = make(chan struct{})
	fm.workers = []model.WorkerView{
		{Slot: 1, State: model.WorkerReceiving, Start: 0, End: 4095, Received: 100, SpeedBps: 900, Fragment: 1},
		{Slot: 2, State: model.WorkerRequesting, Start: -1, End: -1, Fragment: 2},
	}
	h := newHarness(t, harnessOptions{media: fm})
	d := h.addMedia(&downexv1.AddMediaRequest{ManifestUrl: testManifest})
	ctx := ctxT(t)
	stream, err := h.dl.Watch(ctx, connect.NewRequest(&downexv1.WatchRequest{Ids: []int64{d.GetId()}}))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if !stream.Receive() {
		t.Fatalf("snapshot: %v", stream.Err())
	}
	fm.feed(t, 1)

	var live *downexv1.Download
	for live == nil && stream.Receive() {
		for _, ev := range stream.Msg().GetEvents() {
			if u := ev.GetUpdated(); u != nil && len(u.GetConnectionInfo()) > 0 {
				live = u
			}
		}
	}
	if live == nil {
		t.Fatalf("no update with connection_info: %v", stream.Err())
	}
	ci := live.GetConnectionInfo()
	if len(ci) != 2 || ci[0].GetFragment() != 1 || ci[0].GetReceived() != 100 || ci[1].GetFragment() != 2 ||
		ci[1].GetState() != downexv1.ConnectionState_CONNECTION_STATE_REQUESTING {
		t.Fatalf("connection_info = %v", ci)
	}
	if g := h.get(d.GetId()); len(g.GetConnectionInfo()) != 2 {
		t.Fatalf("get = %v", g.GetConnectionInfo())
	}
	if p := h.pause(d.GetId()); p.GetState() != stPaused || len(p.GetConnectionInfo()) != 0 {
		t.Fatalf("paused = %v %v", p.GetState(), p.GetConnectionInfo())
	}
	for _, l := range h.list() {
		if len(l.GetConnectionInfo()) != 0 {
			t.Fatalf("listed = %v", l.GetConnectionInfo())
		}
	}
}

// connCalls records setConns calls.
type connCalls struct {
	mu    sync.Mutex
	calls [][2]int64
	err   error
}

func (c *connCalls) set(id model.DownloadID, n int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, [2]int64{int64(id), int64(n)})
	return c.err
}

func (c *connCalls) recorded() [][2]int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.calls)
}

func (h *harness) setOptions(req *downexv1.SetDownloadOptionsRequest) (*downexv1.Download, error) {
	res, err := h.dl.SetDownloadOptions(ctxT(h.t), connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return res.Msg.GetDownload(), nil
}

// runLimit returns the speed limit of download id's running limiter.
func (h *harness) runLimit(id int64) (int64, bool) {
	h.m.mu.Lock()
	defer h.m.mu.Unlock()
	e := h.m.entries[model.DownloadID(id)]
	if e == nil || e.run == nil {
		return 0, false
	}
	return e.run.lim.Limit(), true
}

func TestSetDownloadOptionsStopped(t *testing.T) {
	const size, seed = 64 << 10, 81
	ts := serve(t, testserver.Options{Size: size, Seed: seed})
	calls := &connCalls{}
	h := newHarness(t, harnessOptions{setConns: calls.set})

	d := h.add(&downexv1.AddRequest{Url: ts.URL + "/a.bin", StartPaused: true})
	if d.GetMaxConnections() != DefaultMaxConnections {
		t.Fatalf("added with %d connections", d.GetMaxConnections())
	}
	speed, conns := int64(300<<10), int32(3)
	got, err := h.setOptions(&downexv1.SetDownloadOptionsRequest{Id: d.GetId(), SpeedLimit: &speed, MaxConnections: &conns})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetSpeedLimit() != speed || got.GetMaxConnections() != conns || got.GetState() != stPaused {
		t.Fatalf("set = %v", got)
	}
	if rec := storedState(t, h.st, d.GetId()); rec.SpeedLimit != speed || rec.MaxConnections != int(conns) {
		t.Fatalf("stored = %+v", rec)
	}
	// Only one field: the other is kept. 0 connections stores the default.
	zero := int32(0)
	if got, err = h.setOptions(&downexv1.SetDownloadOptionsRequest{Id: d.GetId(), MaxConnections: &zero}); err != nil ||
		got.GetSpeedLimit() != speed || got.GetMaxConnections() != DefaultMaxConnections {
		t.Fatalf("set default connections = %v, %v", got, err)
	}
	// Nothing set: unchanged.
	if got, err = h.setOptions(&downexv1.SetDownloadOptionsRequest{Id: d.GetId()}); err != nil || got.GetSpeedLimit() != speed {
		t.Fatalf("empty request = %v, %v", got, err)
	}
	if c := calls.recorded(); len(c) != 0 {
		t.Fatalf("engine asked for a stopped download: %v", c)
	}

	// Invalid values and unknown ids.
	neg, many, below := int64(-1), int32(MaxConnectionsLimit+1), int32(-1)
	for _, req := range []*downexv1.SetDownloadOptionsRequest{
		{Id: d.GetId(), SpeedLimit: &neg},
		{Id: d.GetId(), MaxConnections: &many},
		{Id: d.GetId(), MaxConnections: &below},
	} {
		if _, err := h.setOptions(req); connectCode(err) != connect.CodeInvalidArgument {
			t.Errorf("%v: %v", req, err)
		}
	}
	if _, err := h.setOptions(&downexv1.SetDownloadOptionsRequest{Id: 999, SpeedLimit: &speed}); connectCode(err) != connect.CodeNotFound {
		t.Errorf("unknown id: %v", err)
	}

	// The stored limits apply to the next run; a completed download cannot change.
	unlimited := int64(0)
	if _, err := h.setOptions(&downexv1.SetDownloadOptionsRequest{Id: d.GetId(), SpeedLimit: &unlimited}); err != nil {
		t.Fatal(err)
	}
	h.resume(&downexv1.ResumeRequest{Id: d.GetId()})
	done := h.waitCompleted(d.GetId())
	checkFile(t, done, seed, size)
	if _, err := h.setOptions(&downexv1.SetDownloadOptionsRequest{Id: d.GetId(), SpeedLimit: &speed}); connectCode(err) != connect.CodeFailedPrecondition {
		t.Fatalf("completed: %v", err)
	}
}

func TestSetDownloadOptionsRunning(t *testing.T) {
	const size, seed = 4 << 20, 82
	ts := serve(t, testserver.Options{Size: size, Seed: seed})
	calls := &connCalls{err: context.Canceled} // errors of the engine are only logged
	h := newHarness(t, harnessOptions{setConns: calls.set})

	d := h.add(&downexv1.AddRequest{Url: ts.URL + "/big.bin", SpeedLimit: 64 << 10, MaxConnections: 4})
	h.waitReceived(d.GetId(), 1)
	ctx := ctxT(t)
	stream, err := h.dl.Watch(ctx, connect.NewRequest(&downexv1.WatchRequest{Ids: []int64{d.GetId()}}))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if !stream.Receive() {
		t.Fatalf("snapshot: %v", stream.Err())
	}

	speed, conns := int64(96<<10), int32(2)
	got, err := h.setOptions(&downexv1.SetDownloadOptionsRequest{Id: d.GetId(), SpeedLimit: &speed, MaxConnections: &conns})
	if err != nil {
		t.Fatal(err)
	}
	if !isRunning(got) || got.GetSpeedLimit() != speed || got.GetMaxConnections() != conns {
		t.Fatalf("set while running = %v", got)
	}
	// The limiter changed at once; the engine was asked for the connection limit.
	if lim, ok := h.runLimit(d.GetId()); !ok || lim != speed {
		t.Fatalf("run limiter = %d (running %v)", lim, ok)
	}
	if c := calls.recorded(); len(c) == 0 || c[0] != [2]int64{d.GetId(), 2} {
		t.Fatalf("engine calls = %v", c)
	}
	// Watch updates and Get show the new values while the run's own record is older.
	seen := false
	for !seen && stream.Receive() {
		for _, ev := range stream.Msg().GetEvents() {
			if u := ev.GetUpdated(); u != nil && u.GetSpeedLimit() == speed && u.GetMaxConnections() == conns {
				seen = true
			}
		}
	}
	if !seen {
		t.Fatalf("no update with the new options: %v", stream.Err())
	}
	if g := h.get(d.GetId()); g.GetSpeedLimit() != speed || g.GetMaxConnections() != conns {
		t.Fatalf("get = %v", g)
	}
	// Stored once the run stops.
	h.pause(d.GetId())
	if rec := storedState(t, h.st, d.GetId()); rec.SpeedLimit != speed || rec.MaxConnections != int(conns) {
		t.Fatalf("stored after pause = %d B/s, %d connections", rec.SpeedLimit, rec.MaxConnections)
	}
	// The next run starts with them.
	h.resume(&downexv1.ResumeRequest{Id: d.GetId()})
	waitUntil(t, "the next run", func() bool { _, ok := h.runLimit(d.GetId()); return ok })
	if lim, _ := h.runLimit(d.GetId()); lim != speed {
		t.Fatalf("next run limiter = %d", lim)
	}
}

func TestSetDownloadOptionsRunningMedia(t *testing.T) {
	fm := newFakeMedia(4)
	fm.gate = make(chan struct{})
	calls := &connCalls{}
	h := newHarness(t, harnessOptions{media: fm, setConns: calls.set})
	d := h.addMedia(&downexv1.AddMediaRequest{ManifestUrl: testManifest})
	waitUntil(t, "the media run", func() bool { _, runs := fm.calls(); return len(runs) == 1 })

	speed, conns := int64(50<<10), int32(5)
	got, err := h.setOptions(&downexv1.SetDownloadOptionsRequest{Id: d.GetId(), SpeedLimit: &speed, MaxConnections: &conns})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetSpeedLimit() != speed || got.GetMaxConnections() != conns {
		t.Fatalf("set = %v", got)
	}
	_, runs := fm.calls()
	if lim := runs[0].opts.Limiter.Limit(); lim != speed {
		t.Fatalf("job limiter = %d", lim)
	}
	if c := calls.recorded(); len(c) != 0 {
		t.Fatalf("engine asked for a media download: %v", c)
	}
	fm.feed(t, 1) // a checkpoint written by the run keeps the new values pending
	h.pause(d.GetId())
	if rec := storedState(t, h.st, d.GetId()); rec.SpeedLimit != speed || rec.MaxConnections != int(conns) {
		t.Fatalf("stored = %d B/s, %d connections", rec.SpeedLimit, rec.MaxConnections)
	}
	h.resume(&downexv1.ResumeRequest{Id: d.GetId()})
	waitUntil(t, "the second media run", func() bool { _, runs := fm.calls(); return len(runs) == 2 })
	if _, runs := fm.calls(); runs[1].opts.MaxConnections != int(conns) || runs[1].opts.Limiter.Limit() != speed {
		t.Fatalf("second run with %d connections, %d B/s", runs[1].opts.MaxConnections, runs[1].opts.Limiter.Limit())
	}
}

// TestPendingConnectionsReapplied checks that a connection limit set before the engine
// had started the run is handed to it again after its first progress event.
func TestPendingConnectionsReapplied(t *testing.T) {
	calls := &connCalls{}
	h := newHarness(t, harnessOptions{setConns: calls.set})
	n := 6
	r := &run{}
	e := &entry{id: 77, run: r, pendMax: &n, rec: model.Download{ID: 77, Kind: model.KindFile}}
	h.m.applyPendingConns(e, r)
	if c := calls.recorded(); !slices.Equal(c, [][2]int64{{77, 6}}) {
		t.Fatalf("calls = %v", c)
	}
	// Not for media, another run, a finalizing run or without a pending limit.
	media := &entry{id: 78, run: r, pendMax: &n, rec: model.Download{ID: 78, Kind: model.KindMedia}}
	h.m.applyPendingConns(media, r)
	h.m.applyPendingConns(e, &run{})
	h.m.applyPendingConns(&entry{id: 79, run: r, rec: model.Download{ID: 79}}, r)
	r.finalizing = true
	h.m.applyPendingConns(e, r)
	if c := calls.recorded(); len(c) != 1 {
		t.Fatalf("calls = %v", c)
	}
}

// TestBrowserHandOffOpensProgressWindow checks when the daemon opens a progress window:
// for a browser hand-off that starts at once while confirmations are off and
// show_progress_window is on, and never for other sources, paused or queued adds,
// downloads awaiting confirmation, or with the setting off.
func TestBrowserHandOffOpensProgressWindow(t *testing.T) {
	ts := serve(t, testserver.Options{Size: 4 << 20, Seed: 83})
	windows := &launcher{}
	fm := newFakeMedia(2)
	fm.gate = make(chan struct{})
	h := newHarness(t, harnessOptions{openProgress: windows.open, media: fm, settings: func(s *downexv1.Settings) {
		s.ConfirmBrowserDownloads = false
		s.MaxConcurrentDownloads = 3
	}})
	browser := downexv1.AddSource_ADD_SOURCE_BROWSER
	slow := int64(32 << 10)

	// Not opened: CLI and window adds (the window opens its own), paused hand-offs.
	h.add(&downexv1.AddRequest{Url: ts.URL + "/cli.bin", Source: downexv1.AddSource_ADD_SOURCE_CLI, SpeedLimit: slow})
	h.add(&downexv1.AddRequest{Url: ts.URL + "/ui.bin", Source: downexv1.AddSource_ADD_SOURCE_UI, SpeedLimit: slow})
	h.add(&downexv1.AddRequest{Url: ts.URL + "/later.bin", Source: browser, StartPaused: true})

	// Opened: a browser hand-off that starts at once.
	d := h.add(&downexv1.AddRequest{Url: ts.URL + "/now.bin", Source: browser, SpeedLimit: slow})
	waitUntil(t, "the progress window", func() bool { return len(windows.launched()) > 0 })
	if got := windows.launched(); !slices.Equal(got, []int64{d.GetId()}) {
		t.Fatalf("windows opened for %v, want [%d]", got, d.GetId())
	}

	// Queued behind the three running downloads: it starts later, without a window.
	q := h.add(&downexv1.AddRequest{Url: ts.URL + "/queued.bin", Source: browser})
	if q.GetState() != stQueued {
		t.Fatalf("fourth download = %v", q.GetState())
	}

	// With the setting off nothing opens; a media hand-off opens one once it is on.
	h.pause(q.GetId())
	for _, id := range []int64{1, 2} {
		h.pause(id)
	}
	s := protoSettings(h.m.Settings())
	s.ShowProgressWindow = false
	if _, err := h.m.UpdateSettings(ctxT(t), s); err != nil {
		t.Fatal(err)
	}
	h.add(&downexv1.AddRequest{Url: ts.URL + "/off.bin", Source: browser, SpeedLimit: slow})
	s.ShowProgressWindow = true
	if _, err := h.m.UpdateSettings(ctxT(t), s); err != nil {
		t.Fatal(err)
	}
	m := h.addMedia(&downexv1.AddMediaRequest{ManifestUrl: testManifest, Source: browser})
	waitUntil(t, "the media progress window", func() bool { return len(windows.launched()) > 1 })
	if got := windows.launched(); !slices.Equal(got, []int64{d.GetId(), m.GetId()}) {
		t.Fatalf("windows opened for %v, want [%d %d]", got, d.GetId(), m.GetId())
	}
}

// TestAwaitingHandOffOpensNoProgressWindow: with confirmations on, the confirmation
// window is asked instead.
func TestAwaitingHandOffOpensNoProgressWindow(t *testing.T) {
	ts := serve(t, testserver.Options{Size: 1 << 10, Seed: 84})
	windows, confirms := &launcher{}, &launcher{}
	h := newHarness(t, harnessOptions{openProgress: windows.open, openConfirmation: confirms.open})
	d := h.add(&downexv1.AddRequest{Url: ts.URL + "/v/clip.mp4", Source: downexv1.AddSource_ADD_SOURCE_BROWSER})
	if d.GetState() != stAwaiting {
		t.Fatalf("state = %v", d.GetState())
	}
	waitUntil(t, "the confirmation window", func() bool { return len(confirms.launched()) > 0 })
	// Confirming with start does not open one either: the confirmation window opens it.
	if _, err := h.confirmReq(&downexv1.ConfirmRequest{Id: d.GetId(), Start: true}); err != nil {
		t.Fatal(err)
	}
	h.waitCompleted(d.GetId())
	if got := windows.launched(); len(got) != 0 {
		t.Fatalf("progress windows opened for %v", got)
	}
}

// TestFileConnectionInfoLive runs a file download through the real engine: its workers
// reach Get as connection_info, and a lower connection limit set while it runs is
// applied by the engine and stored when it stops.
func TestFileConnectionInfoLive(t *testing.T) {
	ts := serve(t, testserver.Options{Size: 16 << 20, Seed: 85})
	h := newHarness(t, harnessOptions{})
	d := h.add(&downexv1.AddRequest{Url: ts.URL + "/c.bin", SpeedLimit: 512 << 10, MaxConnections: 4})
	live := h.waitFor(d.GetId(), "connection_info", func(d *downexv1.Download) bool {
		return isRunning(d) && len(d.GetConnectionInfo()) > 0
	})
	for i, c := range live.GetConnectionInfo() {
		if c.GetIndex() < 1 || c.GetFragment() != -1 || (i > 0 && c.GetIndex() <= live.GetConnectionInfo()[i-1].GetIndex()) {
			t.Fatalf("connection_info = %v", live.GetConnectionInfo())
		}
	}
	one := int32(1)
	if _, err := h.setOptions(&downexv1.SetDownloadOptionsRequest{Id: d.GetId(), MaxConnections: &one}); err != nil {
		t.Fatal(err)
	}
	h.waitFor(d.GetId(), "one connection", func(d *downexv1.Download) bool {
		return isRunning(d) && d.GetConnections() <= 1 && d.GetMaxConnections() == 1
	})
	h.pause(d.GetId())
	if rec := storedState(t, h.st, d.GetId()); rec.MaxConnections != 1 {
		t.Fatalf("stored %d connections", rec.MaxConnections)
	}
}

// optionsGate holds the first UpdateDownload that match accepts until release is
// closed, then fails it when fail is set.
type optionsGate struct {
	*store.Memory
	mu      sync.Mutex
	match   func(*model.Download) bool
	fail    bool
	entered chan struct{}
	release chan struct{}
}

func (g *optionsGate) arm(match func(*model.Download) bool, fail bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.match, g.fail = match, fail
	g.entered, g.release = make(chan struct{}), make(chan struct{})
}

func (g *optionsGate) UpdateDownload(ctx context.Context, d *model.Download) error {
	g.mu.Lock()
	hit := g.match != nil && g.match(d)
	if hit {
		g.match = nil
	}
	fail, entered, release := g.fail, g.entered, g.release
	g.mu.Unlock()
	if hit {
		close(entered)
		<-release
		if fail {
			return errors.New("injected write failure")
		}
	}
	return g.Memory.UpdateDownload(ctx, d)
}

// TestSetDownloadOptionsQueuedRace checks that a queued download whose options are
// being stored cannot start with the old ones in the meantime (the download ahead of
// it stops just then): the new limit reaches its run and its stored record, and after a
// failed write it is queued again with the old ones.
func TestSetDownloadOptionsQueuedRace(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "stored"
		if fail {
			name = "write fails"
		}
		t.Run(name, func(t *testing.T) {
			const size = 4 << 20
			ts := serve(t, testserver.Options{Size: size, Seed: 84})
			st := &optionsGate{Memory: store.NewMemory()}
			h := newHarness(t, harnessOptions{st: st, settings: func(s *downexv1.Settings) { s.MaxConcurrentDownloads = 1 }})

			a := h.add(&downexv1.AddRequest{Url: ts.URL + "/a.bin", SpeedLimit: 64 << 10})
			h.waitReceived(a.GetId(), 1)
			old := int64(32 << 10)
			b := h.add(&downexv1.AddRequest{Url: ts.URL + "/b.bin", SpeedLimit: old})
			if g := h.get(b.GetId()); g.GetState() != stQueued || g.GetQueuePosition() != 1 {
				t.Fatalf("b = %v", g)
			}

			speed := int64(77 << 10)
			st.arm(func(d *model.Download) bool { return int64(d.ID) == b.GetId() && d.SpeedLimit == speed }, fail)
			type result struct {
				d   *downexv1.Download
				err error
			}
			res := make(chan result, 1)
			go func() {
				d, err := h.setOptions(&downexv1.SetDownloadOptionsRequest{Id: b.GetId(), SpeedLimit: &speed})
				res <- result{d, err}
			}()
			select {
			case <-st.entered:
			case <-time.After(10 * time.Second):
				t.Fatal("SetDownloadOptions did not write")
			}
			h.pause(a.GetId()) // frees the only slot while the options are being written
			close(st.release)
			r := <-res

			want := speed
			if fail {
				if connectCode(r.err) != connect.CodeInternal {
					t.Fatalf("failed write: %v, %v", r.d, r.err)
				}
				want = old
			} else if r.err != nil || r.d.GetSpeedLimit() != speed {
				t.Fatalf("set = %v, %v", r.d, r.err)
			}
			waitUntil(t, "b's run", func() bool { _, ok := h.runLimit(b.GetId()); return ok })
			if lim, _ := h.runLimit(b.GetId()); lim != want {
				t.Fatalf("b's run limiter = %d, want %d", lim, want)
			}
			if g := h.get(b.GetId()); g.GetSpeedLimit() != want {
				t.Fatalf("get = %d B/s, want %d", g.GetSpeedLimit(), want)
			}
			h.pause(b.GetId())
			if rec := storedState(t, h.st, b.GetId()); rec.SpeedLimit != want {
				t.Fatalf("stored = %d B/s, want %d", rec.SpeedLimit, want)
			}
		})
	}
}

// TestDefaultConnectionsFollowSettings: changing default_max_connections gives every
// unfinished download that used the old default the new one; a download given its own
// limit, and a completed one, keep theirs.
func TestDefaultConnectionsFollowSettings(t *testing.T) {
	const size, seed = 64 << 10, 82
	ts := serve(t, testserver.Options{Size: size, Seed: seed})
	h := newHarness(t, harnessOptions{})
	ctx := context.Background()

	done := h.add(&downexv1.AddRequest{Url: ts.URL + "/done.bin"})
	h.waitCompleted(done.GetId())
	paused := h.add(&downexv1.AddRequest{Url: ts.URL + "/a.bin", StartPaused: true})
	own := h.add(&downexv1.AddRequest{Url: ts.URL + "/b.bin", StartPaused: true, MaxConnections: 5})
	if paused.GetMaxConnections() != DefaultMaxConnections || own.GetMaxConnections() != 5 {
		t.Fatalf("added with %d and %d connections", paused.GetMaxConnections(), own.GetMaxConnections())
	}

	got, err := h.dm.GetSettings(ctx, connect.NewRequest(&downexv1.GetSettingsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	s := got.Msg.GetSettings()
	s.DefaultMaxConnections = 12
	if _, err := h.dm.UpdateSettings(ctx, connect.NewRequest(&downexv1.UpdateSettingsRequest{Settings: s})); err != nil {
		t.Fatal(err)
	}
	if n := h.get(paused.GetId()).GetMaxConnections(); n != 12 {
		t.Errorf("default download: %d connections, want 12", n)
	}
	if rec := storedState(t, h.st, paused.GetId()); rec.MaxConnections != 12 {
		t.Errorf("default download stored %d connections", rec.MaxConnections)
	}
	if n := h.get(own.GetId()).GetMaxConnections(); n != 5 {
		t.Errorf("own limit: %d connections, want 5", n)
	}
	if n := h.get(done.GetId()).GetMaxConnections(); n != DefaultMaxConnections {
		t.Errorf("completed: %d connections, want %d", n, DefaultMaxConnections)
	}
}
