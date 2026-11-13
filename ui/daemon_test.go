package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	"downex/internal/client"
	"downex/internal/ipc"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/rpc/downex/v1/downexv1connect"
)

// downloadServer and daemonServer are downexd's services as an httptest server serves them.
type downloadServer struct {
	downexv1connect.UnimplementedDownloadServiceHandler
}

func (downloadServer) List(context.Context, *connect.Request[downexv1.ListRequest]) (*connect.Response[downexv1.ListResponse], error) {
	return connect.NewResponse(&downexv1.ListResponse{Downloads: []*downexv1.Download{{Id: 1, Size: -1}}}), nil
}

func (downloadServer) Pause(_ context.Context, req *connect.Request[downexv1.PauseRequest]) (*connect.Response[downexv1.PauseResponse], error) {
	return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("download %d not found", req.Msg.GetId()))
}

func (downloadServer) Confirm(_ context.Context, req *connect.Request[downexv1.ConfirmRequest]) (*connect.Response[downexv1.ConfirmResponse], error) {
	if !req.Msg.GetStart() {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("download %d is not awaiting confirmation", req.Msg.GetId()))
	}
	return connect.NewResponse(&downexv1.ConfirmResponse{Download: &downexv1.Download{
		Id: req.Msg.GetId(), Dir: req.Msg.GetDir(), Size: -1, State: downexv1.DownloadState_DOWNLOAD_STATE_QUEUED,
	}}), nil
}

func dashMedia(manifest string) *downexv1.MediaInfo {
	return &downexv1.MediaInfo{
		ManifestUrl: manifest, Format: "dash", Container: "mp4", DurationMs: 60_000, FragmentsTotal: 30,
		Tracks: []*downexv1.MediaTrack{
			{Id: "video-720", Kind: "video", Bandwidth: 2_500_000, Width: 1280, Height: 720, Codecs: "avc1.64001f"},
			{Id: "video-1080", Kind: "video", Bandwidth: 5_000_000, Width: 1920, Height: 1080, Codecs: "avc1.640028"},
			{Id: "audio-en", Kind: "audio", Bandwidth: 128_000, Codecs: "mp4a.40.2", Language: "en"},
		},
		SelectedVideo: "video-1080", SelectedAudio: "audio-en",
	}
}

var errLiveStream = connect.NewError(connect.CodeInvalidArgument, errors.New("live: the presentation is live"))

func (downloadServer) AddMedia(_ context.Context, req *connect.Request[downexv1.AddMediaRequest]) (*connect.Response[downexv1.AddMediaResponse], error) {
	if strings.Contains(req.Msg.GetManifestUrl(), "live") {
		return nil, errLiveStream
	}
	media := dashMedia(req.Msg.GetManifestUrl())
	if v := req.Msg.GetVideoTrack(); v != "" {
		media.SelectedVideo = v
	}
	return connect.NewResponse(&downexv1.AddMediaResponse{Download: &downexv1.Download{
		Id: 21, Url: req.Msg.GetManifestUrl(), Size: -1, State: downexv1.DownloadState_DOWNLOAD_STATE_QUEUED,
		Kind: downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA, Media: media,
	}}), nil
}

func (downloadServer) ProbeMedia(_ context.Context, req *connect.Request[downexv1.ProbeMediaRequest]) (*connect.Response[downexv1.ProbeMediaResponse], error) {
	if strings.Contains(req.Msg.GetManifestUrl(), "live") {
		return nil, errLiveStream
	}
	return connect.NewResponse(&downexv1.ProbeMediaResponse{Media: dashMedia(req.Msg.GetManifestUrl())}), nil
}

func (downloadServer) SetDownloadOptions(_ context.Context, req *connect.Request[downexv1.SetDownloadOptionsRequest]) (*connect.Response[downexv1.SetDownloadOptionsResponse], error) {
	return connect.NewResponse(&downexv1.SetDownloadOptionsResponse{Download: &downexv1.Download{
		Id: req.Msg.GetId(), Size: -1, SpeedLimit: req.Msg.GetSpeedLimit(), MaxConnections: req.Msg.GetMaxConnections(),
		State: downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING,
	}}), nil
}

func (downloadServer) PauseAll(context.Context, *connect.Request[downexv1.PauseAllRequest]) (*connect.Response[downexv1.PauseAllResponse], error) {
	return connect.NewResponse(&downexv1.PauseAllResponse{Paused: 3}), nil
}

func (downloadServer) ResumeAll(context.Context, *connect.Request[downexv1.ResumeAllRequest]) (*connect.Response[downexv1.ResumeAllResponse], error) {
	return connect.NewResponse(&downexv1.ResumeAllResponse{Resumed: 4}), nil
}

func (downloadServer) Watch(ctx context.Context, req *connect.Request[downexv1.WatchRequest], s *connect.ServerStream[downexv1.WatchResponse]) error {
	ids := req.Msg.GetIds()
	if len(ids) == 0 {
		ids = []int64{1, 2}
	}
	for _, id := range ids {
		if id > 2 {
			return connect.NewError(connect.CodeNotFound, fmt.Errorf("download %d not found", id))
		}
	}
	if err := s.Send(snapshotOf(ids...)); err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}

type daemonServer struct {
	downexv1connect.UnimplementedDaemonServiceHandler
}

func (daemonServer) Status(context.Context, *connect.Request[downexv1.StatusRequest]) (*connect.Response[downexv1.StatusResponse], error) {
	return connect.NewResponse(&downexv1.StatusResponse{Version: "test", Pid: 7}), nil
}

// fakeSystem is a client.Env whose daemon "runs" once StartDetached was called: until
// then every connection fails with ipc.ErrNotRunning, as on a real endpoint.
type fakeSystem struct {
	mu       sync.Mutex
	up       bool
	starts   int
	elevated bool
	noDaemon error
	crashes  bool // a started daemon exits at once (it never listens)

	srv *httptest.Server
	hc  *http.Client
}

func (s *fakeSystem) running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.up
}

func (s *fakeSystem) startCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.starts
}

// stop ends the daemon as "downex daemon stop" does: its connections close, and nothing
// listens on the endpoint any more.
func (s *fakeSystem) stop() {
	s.mu.Lock()
	s.up = false
	s.mu.Unlock()
	s.srv.CloseClientConnections()
	s.hc.CloseIdleConnections()
}

// startByUser starts the daemon without Downex, as "downex daemon start" does.
func (s *fakeSystem) startByUser() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.up = true
}

func (s *fakeSystem) env(t *testing.T) client.Env {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(downexv1connect.NewDownloadServiceHandler(downloadServer{}))
	mux.Handle(downexv1connect.NewDaemonServiceHandler(daemonServer{}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	s.srv = srv
	dial := func(ctx context.Context) (net.Conn, error) {
		if !s.running() {
			return nil, fmt.Errorf("fake endpoint: %w", ipc.ErrNotRunning)
		}
		var d net.Dialer
		return d.DialContext(ctx, "tcp", srv.Listener.Addr().String())
	}
	hc := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return dial(ctx) },
	}}
	t.Cleanup(hc.CloseIdleConnections)
	s.hc = hc
	return client.Env{
		Endpoint:   func() (string, error) { return "fake-endpoint", nil },
		Dial:       func(ctx context.Context, _ string) (net.Conn, error) { return dial(ctx) },
		HTTPClient: func(string) connect.HTTPClient { return hc },
		BaseURL:    srv.URL,
		DaemonPath: func() (string, error) {
			if s.noDaemon != nil {
				return "", s.noDaemon
			}
			return `C:\fake\downexd.exe`, nil
		},
		StartDetached: func(string, ...string) error {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.starts++
			s.up = !s.crashes
			return nil
		},
		Elevated:     func() (bool, error) { return s.elevated, nil },
		LogDir:       func() (string, error) { return `C:\fake\logs`, nil },
		StartTimeout: 2 * time.Second,
		PollInterval: time.Millisecond,
	}
}

func TestClientDaemonStartsDownexdOnce(t *testing.T) {
	sys := &fakeSystem{}
	a, _ := newTestApp(t, newClientDaemon(sys.env(t)), nil)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			raw, err := a.List()
			if err == nil && !strings.Contains(string(raw), `"size":"-1"`) {
				err = fmt.Errorf("List = %s", raw)
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	if n := sys.startCount(); n != 1 {
		t.Errorf("downexd started %d times, want once", n)
	}
	// Running now: no further start, and daemon errors arrive as Connect text.
	if _, err := a.Pause(json.RawMessage(`{"id":5}`)); err == nil || err.Error() != "not_found: download 5 not found" {
		t.Errorf("Pause = %v", err)
	}
	if raw, err := a.Status(); err != nil || !strings.Contains(string(raw), `"pid":7`) {
		t.Errorf("Status = %s, %v", raw, err)
	}
	if n := sys.startCount(); n != 1 {
		t.Errorf("downexd started %d times, want once", n)
	}
}

func TestClientDaemonConfirmPauseAllResumeAll(t *testing.T) {
	sys := &fakeSystem{}
	sys.startByUser()
	a, _ := newTestApp(t, newClientDaemon(sys.env(t)), nil)
	raw, err := a.Confirm(json.RawMessage(`{"id":"11","dir":"C:\\Video","start":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"id":"11"`) || !strings.Contains(string(raw), `"state":"DOWNLOAD_STATE_QUEUED"`) {
		t.Errorf("Confirm = %s", raw)
	}
	if _, err := a.Confirm(json.RawMessage(`{"id":"12"}`)); err == nil ||
		err.Error() != "failed_precondition: download 12 is not awaiting confirmation" {
		t.Errorf("Confirm = %v", err)
	}
	if raw, err := a.PauseAll(); err != nil || string(raw) != `{"paused":3}` {
		t.Errorf("PauseAll = %s, %v", raw, err)
	}
	if raw, err := a.ResumeAll(); err != nil || string(raw) != `{"resumed":4}` {
		t.Errorf("ResumeAll = %s, %v", raw, err)
	}
	if n := sys.startCount(); n != 0 {
		t.Errorf("downexd started %d times while running", n)
	}
}

func TestClientDaemonWatchStartsDownexd(t *testing.T) {
	sys := &fakeSystem{}
	d := newClientDaemon(sys.env(t))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s, err := d.Watch(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !s.Receive() {
		t.Fatalf("no snapshot: %v", s.Err())
	}
	if n := len(s.Msg().GetEvents()); n != 2 || sys.startCount() != 1 {
		t.Errorf("snapshot of %d downloads after %d starts", n, sys.startCount())
	}
	cancel()
	if s.Receive() {
		t.Error("the stream outlived its context")
	}
}

// countingDaemon counts the Watch calls (the events bridge's subscriptions) of the
// Daemon it wraps.
type countingDaemon struct {
	Daemon
	watches atomic.Int32
}

func (c *countingDaemon) Watch(ctx context.Context, ids []int64) (WatchStream, error) {
	c.watches.Add(1)
	return c.Daemon.Watch(ctx, ids)
}

// eventually reports whether cond holds within 10 s.
func eventually(cond func() bool) bool {
	for deadline := time.Now().Add(10 * time.Second); !cond(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			return false
		}
	}
	return true
}

const notRunningText = `unavailable: downexd is not running, and Downex starts it only when its window opens; start it with "downex daemon start"`

// The window starts downexd at its first contact only. A daemon that the user (or an
// installer) stops later stays stopped: neither the frontend's status checks nor the
// events bridge's re-subscriptions start it again.
func TestClientDaemonLeavesAStoppedDaemonStopped(t *testing.T) {
	sys := &fakeSystem{}
	d := &countingDaemon{Daemon: newClientDaemon(sys.env(t))}
	h := &fakeHost{emitted: make(chan json.RawMessage, 64)}
	a, _ := newTestApp(t, d, h)
	if got := eventKinds(t, nextEvent(t, h.emitted)); !slices.Equal(got, []string{"snapshot:1", "snapshot:2"}) {
		t.Fatalf("first batch %v", got)
	}
	if n := sys.startCount(); n != 1 {
		t.Fatalf("downexd started %d times when the window opened, want once", n)
	}

	sys.stop() // "downex daemon stop"
	// The bridge subscribes again and again (every 1 to 4 ms here).
	base := d.watches.Load()
	if !eventually(func() bool { return d.watches.Load() >= base+5 || sys.startCount() > 1 }) {
		t.Fatalf("the bridge subscribed %d times after the stop, want 5", d.watches.Load()-base)
	}
	if n := sys.startCount(); n != 1 {
		t.Fatalf("downexd started %d times, want once: the events bridge started the stopped daemon again", n)
	}
	for range 3 {
		if _, err := a.Status(); err == nil || err.Error() != notRunningText {
			t.Errorf("Status after a stop = %v, want %q", err, notRunningText)
		}
	}
	if _, err := a.List(); err == nil || err.Error() != notRunningText {
		t.Errorf("List after a stop = %v, want %q", err, notRunningText)
	}
	if n := sys.startCount(); n != 1 {
		t.Errorf("downexd started %d times, want once: a daemon that was stopped was started again", n)
	}

	// Started again by the user, it is used at once.
	sys.startByUser()
	if raw, err := a.Status(); err != nil || !strings.Contains(string(raw), `"pid":7`) {
		t.Errorf("Status after the user started downexd = %s, %v", raw, err)
	}
	if got := eventKinds(t, nextEvent(t, h.emitted)); !slices.Equal(got, []string{"snapshot:1", "snapshot:2"}) {
		t.Errorf("batch after the user started downexd = %v", got)
	}
	if n := sys.startCount(); n != 1 {
		t.Errorf("downexd started %d times, want once", n)
	}
}

// A daemon that exits at once (a corrupt database, bad settings) is started once when
// the window opens, not again on every status check and re-subscription; the errors
// after that say why it is not running.
func TestClientDaemonStartsACrashingDaemonOnce(t *testing.T) {
	sys := &fakeSystem{crashes: true}
	env := sys.env(t)
	env.StartTimeout = 50 * time.Millisecond
	d := &countingDaemon{Daemon: newClientDaemon(env)}
	a, _ := newTestApp(t, d, &fakeHost{})

	failed := `started C:\fake\downexd.exe, but downexd did not answer within 50ms; see its log in C:\fake\logs`
	// The first contact is this call or the bridge's first Watch; this call waits for it.
	if _, err := a.Status(); err == nil || err.Error() != "unavailable: "+failed {
		t.Errorf("first Status = %v, want %q", err, "unavailable: "+failed)
	}
	if !eventually(func() bool { return d.watches.Load() >= 5 }) {
		t.Fatalf("the bridge subscribed %d times, want 5", d.watches.Load())
	}
	want := notRunningText + " (starting it when the window opened failed: " + failed + ")"
	for range 3 {
		if _, err := a.Status(); err == nil || err.Error() != want {
			t.Errorf("Status = %v, want %q", err, want)
		}
	}
	if _, err := a.GetSettings(); err == nil || err.Error() != want {
		t.Errorf("GetSettings = %v, want %q", err, want)
	}
	if n := sys.startCount(); n != 1 {
		t.Errorf("downexd started %d times, want once", n)
	}
}

// The production transport (HTTP/2 over the pipe or socket) must keep ipc.ErrNotRunning
// in a Connect error's chain: unary calls start downexd and retry only on it.
func TestNotRunningSurvivesTheRealTransport(t *testing.T) {
	ep := fmt.Sprintf(`\\.\pipe\downex-ui-test-%d-%d`, os.Getpid(), time.Now().UnixNano())
	if runtime.GOOS != "windows" {
		// Not below t.TempDir(): on macOS that path is too long for a socket (sun_path).
		dir, err := os.MkdirTemp("", "dx") // mode 0700, as ipc requires
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		ep = filepath.Join(dir, "rpc.sock")
	}
	c := client.Default().Client(ep)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	_, err := c.Daemon.Status(ctx, connect.NewRequest(&downexv1.StatusRequest{}))
	if !errors.Is(err, ipc.ErrNotRunning) || connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("Status on an endpoint where nothing listens: %v (code %v), want unavailable wrapping ipc.ErrNotRunning", err, connect.CodeOf(err))
	}
}

func TestClientDaemonStartRefusals(t *testing.T) {
	sys := &fakeSystem{elevated: true}
	a, _ := newTestApp(t, newClientDaemon(sys.env(t)), nil)
	_, err := a.List()
	if err == nil || !strings.HasPrefix(err.Error(), "failed_precondition: downexd is not running, and Downex never starts it while it runs as administrator") {
		t.Errorf("elevated: %v", err)
	}
	// Later calls do not try again, and still say why.
	_, err = a.Status()
	if err == nil || !strings.HasPrefix(err.Error(), notRunningText+" (starting it when the window opened failed: downexd is not running, and Downex never starts it while it runs as administrator") {
		t.Errorf("elevated, later: %v", err)
	}
	if sys.startCount() != 0 {
		t.Error("downexd was started from an elevated process")
	}

	sys = &fakeSystem{noDaemon: errors.New("downexd is not installed next to Downex")}
	a, _ = newTestApp(t, newClientDaemon(sys.env(t)), nil)
	_, err = a.Status()
	if err == nil || err.Error() != "unavailable: cannot start downexd: downexd is not installed next to Downex" {
		t.Errorf("no daemon: %v", err)
	}

	env := (&fakeSystem{}).env(t)
	env.Endpoint = func() (string, error) { return "", errors.New("no profile directory") }
	a, _ = newTestApp(t, newClientDaemon(env), nil)
	if _, err := a.GetSettings(); err == nil || err.Error() != "unavailable: locate the downexd endpoint: no profile directory" {
		t.Errorf("no endpoint: %v", err)
	}
}
