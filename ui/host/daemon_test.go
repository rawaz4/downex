package host

import (
	"context"
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
	"google.golang.org/protobuf/proto"

	"downex/internal/client"
	"downex/internal/ipc"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/rpc/downex/v1/downexv1connect"
)

// downloadServer and daemonServer are downexd's services as an httptest server serves them
// (today's ui/daemon_test.go).
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

// queueServer is QueueService as an httptest server serves it.
type queueServer struct {
	downexv1connect.UnimplementedQueueServiceHandler
}

func (queueServer) ListQueues(context.Context, *connect.Request[downexv1.ListQueuesRequest]) (*connect.Response[downexv1.ListQueuesResponse], error) {
	return connect.NewResponse(&downexv1.ListQueuesResponse{Queues: []*downexv1.Queue{{Id: 1, Name: "Main queue", IsMain: true, Running: true}}}), nil
}

func (queueServer) StartQueue(context.Context, *connect.Request[downexv1.StartQueueRequest]) (*connect.Response[downexv1.StartQueueResponse], error) {
	return nil, connect.NewError(connect.CodeNotFound, errors.New("queue not found"))
}

// fakeSystem is a client.Env whose daemon "runs" once StartDetached was called: until then
// every connection fails with ipc.ErrNotRunning, as on a real endpoint. It never starts a
// process.
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
	mux.Handle(downexv1connect.NewQueueServiceHandler(queueServer{}))
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
	d := NewClientDaemon(sys.env(t))
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			l, err := d.List(callCtx(t))
			if err == nil && (len(l.GetDownloads()) != 1 || l.GetDownloads()[0].GetSize() != -1) {
				err = fmt.Errorf("List = %v", l)
			}
			errs <- err
		})
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
	if _, err := d.Pause(callCtx(t), &downexv1.PauseRequest{Id: 5}); errText(err) != "not_found: download 5 not found" {
		t.Errorf("Pause = %v", err)
	}
	if st, err := d.Status(callCtx(t)); err != nil || st.GetPid() != 7 {
		t.Errorf("Status = %v, %v", st, err)
	}
	if n := sys.startCount(); n != 1 {
		t.Errorf("downexd started %d times, want once", n)
	}
}

func TestClientDaemonConfirmPauseAllResumeAll(t *testing.T) {
	sys := &fakeSystem{}
	sys.startByUser()
	d := NewClientDaemon(sys.env(t))
	resp, err := d.Confirm(callCtx(t), &downexv1.ConfirmRequest{Id: 11, Dir: `C:\Video`, Start: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.GetDownload(); got.GetId() != 11 || got.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_QUEUED {
		t.Errorf("Confirm = %v", got)
	}
	if _, err := d.Confirm(callCtx(t), &downexv1.ConfirmRequest{Id: 12}); errText(err) != "failed_precondition: download 12 is not awaiting confirmation" {
		t.Errorf("Confirm = %v", err)
	}
	if r, err := d.PauseAll(callCtx(t)); err != nil || r.GetPaused() != 3 {
		t.Errorf("PauseAll = %v, %v", r, err)
	}
	if r, err := d.ResumeAll(callCtx(t)); err != nil || r.GetResumed() != 4 {
		t.Errorf("ResumeAll = %v, %v", r, err)
	}
	if n := sys.startCount(); n != 0 {
		t.Errorf("downexd started %d times while running", n)
	}
}

func TestClientDaemonWatchStartsDownexd(t *testing.T) {
	sys := &fakeSystem{}
	d := NewClientDaemon(sys.env(t))
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

// countingDaemon counts the Watch calls (the events bridge's subscriptions) of the Daemon
// it wraps.
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
// installer) stops later stays stopped: neither the window's status checks nor the events
// bridge's re-subscriptions start it again.
func TestClientDaemonLeavesAStoppedDaemonStopped(t *testing.T) {
	sys := &fakeSystem{}
	d := &countingDaemon{Daemon: NewClientDaemon(sys.env(t))}
	_, r := startBridge(t, d)
	if got := eventKinds(t, nextBatch(t, r.emitted)); !slices.Equal(got, []string{"snapshot:1", "snapshot:2"}) {
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
		if _, err := d.Status(callCtx(t)); errText(err) != notRunningText {
			t.Errorf("Status after a stop = %v, want %q", err, notRunningText)
		}
	}
	if _, err := d.List(callCtx(t)); errText(err) != notRunningText {
		t.Errorf("List after a stop = %v, want %q", err, notRunningText)
	}
	if n := sys.startCount(); n != 1 {
		t.Errorf("downexd started %d times, want once: a daemon that was stopped was started again", n)
	}

	// Started again by the user, it is used at once.
	sys.startByUser()
	if st, err := d.Status(callCtx(t)); err != nil || st.GetPid() != 7 {
		t.Errorf("Status after the user started downexd = %v, %v", st, err)
	}
	if got := eventKinds(t, nextBatch(t, r.emitted)); !slices.Equal(got, []string{"snapshot:1", "snapshot:2"}) {
		t.Errorf("batch after the user started downexd = %v", got)
	}
	if n := sys.startCount(); n != 1 {
		t.Errorf("downexd started %d times, want once", n)
	}
}

// A daemon that exits at once (a corrupt database, bad settings) is started once when the
// window opens, not again on every status check and re-subscription; the errors after that
// say why it is not running.
func TestClientDaemonStartsACrashingDaemonOnce(t *testing.T) {
	sys := &fakeSystem{crashes: true}
	env := sys.env(t)
	env.StartTimeout = 50 * time.Millisecond
	d := &countingDaemon{Daemon: NewClientDaemon(env)}
	startBridge(t, d)

	failed := `started C:\fake\downexd.exe, but downexd did not answer within 50ms; see its log in C:\fake\logs`
	// The first contact is this call or the bridge's first Watch; this call waits for it.
	if _, err := d.Status(callCtx(t)); errText(err) != "unavailable: "+failed {
		t.Errorf("first Status = %v, want %q", err, "unavailable: "+failed)
	}
	if !eventually(func() bool { return d.watches.Load() >= 5 }) {
		t.Fatalf("the bridge subscribed %d times, want 5", d.watches.Load())
	}
	want := notRunningText + " (starting it when the window opened failed: " + failed + ")"
	for range 3 {
		if _, err := d.Status(callCtx(t)); errText(err) != want {
			t.Errorf("Status = %v, want %q", err, want)
		}
	}
	if _, err := d.GetSettings(callCtx(t)); errText(err) != want {
		t.Errorf("GetSettings = %v, want %q", err, want)
	}
	if n := sys.startCount(); n != 1 {
		t.Errorf("downexd started %d times, want once", n)
	}
}

// The production transport (HTTP/2 over the pipe or socket) must keep ipc.ErrNotRunning in
// a Connect error's chain: unary calls start downexd and retry only on it.
func TestNotRunningSurvivesTheRealTransport(t *testing.T) {
	ep := fmt.Sprintf(`\\.\pipe\downex-host-test-%d-%d`, os.Getpid(), time.Now().UnixNano())
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
	d := NewClientDaemon(sys.env(t))
	_, err := d.List(callCtx(t))
	if !hasPrefix(err, "failed_precondition: downexd is not running, and Downex never starts it while it runs as administrator") {
		t.Errorf("elevated: %v", err)
	}
	// Later calls do not try again, and still say why.
	_, err = d.Status(callCtx(t))
	if !hasPrefix(err, notRunningText+" (starting it when the window opened failed: downexd is not running, and Downex never starts it while it runs as administrator") {
		t.Errorf("elevated, later: %v", err)
	}
	if sys.startCount() != 0 {
		t.Error("downexd was started from an elevated process")
	}

	sys = &fakeSystem{noDaemon: errors.New("downexd is not installed next to Downex")}
	d = NewClientDaemon(sys.env(t))
	if _, err = d.Status(callCtx(t)); errText(err) != "unavailable: cannot start downexd: downexd is not installed next to Downex" {
		t.Errorf("no daemon: %v", err)
	}

	env := (&fakeSystem{}).env(t)
	env.Endpoint = func() (string, error) { return "", errors.New("no profile directory") }
	d = NewClientDaemon(env)
	if _, err := d.GetSettings(callCtx(t)); errText(err) != "unavailable: locate the downexd endpoint: no profile directory" {
		t.Errorf("no endpoint: %v", err)
	}
	// Watch makes the same first contact and reports the same refusal.
	if _, err := d.Watch(callCtx(t), nil); errText(err) != "unavailable: locate the downexd endpoint: no profile directory" {
		t.Errorf("Watch without an endpoint: %v", err)
	}
}

// $DOWNEX_ENDPOINT names the endpoint (ipc.Endpoint, which client.Default uses): a window
// started for a test daemon talks to that daemon only.
func TestClientDaemonHonoursDownexEndpoint(t *testing.T) {
	ep := fmt.Sprintf(`\\.\pipe\downex-host-test-endpoint-%d-%d`, os.Getpid(), time.Now().UnixNano())
	if runtime.GOOS != "windows" {
		dir, err := os.MkdirTemp("", "dx") // mode 0700, as ipc requires
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		ep = filepath.Join(dir, "rpc.sock")
	}
	t.Setenv(ipc.EnvEndpoint, ep)
	env := client.Default()
	var started []string
	env.StartDetached = func(exe string, args ...string) error {
		started = append(started, exe)
		return errors.New("tests start no daemon")
	}
	env.Elevated = func() (bool, error) { return false, nil }
	env.DaemonPath = func() (string, error) { return "downexd-stand-in", nil }
	var dialed []string
	dial := env.Dial
	env.Dial = func(ctx context.Context, endpoint string) (net.Conn, error) {
		dialed = append(dialed, endpoint)
		return dial(ctx, endpoint)
	}
	d := NewClientDaemon(env)
	if _, err := d.Status(callCtx(t)); errText(err) != "unavailable: cannot start downexd (downexd-stand-in): tests start no daemon" {
		t.Errorf("Status = %v", err)
	}
	if len(dialed) == 0 || slices.IndexFunc(dialed, func(e string) bool { return e != ep }) >= 0 {
		t.Errorf("dialed %q, want only %q", dialed, ep)
	}
	if len(started) != 1 {
		t.Errorf("start attempts = %v, want one (the first contact)", started)
	}
}

func TestClientDaemonWatchesSomeDownloads(t *testing.T) {
	sys := &fakeSystem{}
	d := NewClientDaemon(sys.env(t))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s, err := d.Watch(ctx, []int64{2})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !s.Receive() {
		t.Fatalf("no snapshot: %v", s.Err())
	}
	if ev := s.Msg().GetEvents(); len(ev) != 1 || ev[0].GetSnapshot().GetId() != 2 {
		t.Errorf("snapshot = %v, want download 2 only", ev)
	}

	// An unknown id ends the stream with not_found (from Receive, as Connect reports it).
	s2, err := d.Watch(ctx, []int64{9})
	if err == nil {
		defer s2.Close()
		if s2.Receive() {
			t.Fatal("a stream for an unknown download delivered a batch")
		}
		err = s2.Err()
	}
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("Watch of an unknown download: %v, want not_found", err)
	}

	resp, err := d.SetDownloadOptions(ctx, &downexv1.SetDownloadOptionsRequest{Id: 2, SpeedLimit: proto.Int64(4096)})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetDownload().GetSpeedLimit() != 4096 {
		t.Errorf("SetDownloadOptions = %v", resp)
	}
}

func TestClientDaemonMedia(t *testing.T) {
	sys := &fakeSystem{}
	sys.startByUser()
	d := NewClientDaemon(sys.env(t))
	p, err := d.ProbeMedia(callCtx(t), &downexv1.ProbeMediaRequest{ManifestUrl: "https://media.example.org/a.mpd"})
	if err != nil {
		t.Fatal(err)
	}
	if m := p.GetMedia(); m.GetFormat() != "dash" || m.GetManifestUrl() != "https://media.example.org/a.mpd" {
		t.Errorf("ProbeMedia = %v", m)
	}
	a, err := d.AddMedia(callCtx(t), &downexv1.AddMediaRequest{ManifestUrl: "https://media.example.org/a.mpd", VideoTrack: "video-720"})
	if err != nil {
		t.Fatal(err)
	}
	if got := a.GetDownload(); got.GetKind() != downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA || got.GetMedia().GetSelectedVideo() != "video-720" {
		t.Errorf("AddMedia = %v", got)
	}
	// The daemon's refusal survives the real transport as Connect text.
	if _, err := d.AddMedia(callCtx(t), &downexv1.AddMediaRequest{ManifestUrl: "https://media.example.org/live.mpd"}); errText(err) != "invalid_argument: live: the presentation is live" {
		t.Errorf("AddMedia of a live stream = %v", err)
	}
	if n := sys.startCount(); n != 0 {
		t.Errorf("downexd started %d times while running", n)
	}
}

func TestClientDaemonTools(t *testing.T) {
	sys := &fakeSystem{}
	sys.startByUser()
	d := NewClientDaemon(sys.env(t))
	// The test daemon has no tools RPCs, like a downexd older than site media.
	if _, err := d.GetTools(callCtx(t)); !hasPrefix(err, "unimplemented: ") {
		t.Errorf("GetTools = %v", err)
	}
	if _, err := d.InstallTools(callCtx(t), &downexv1.InstallToolsRequest{Update: true}); !hasPrefix(err, "unimplemented: ") {
		t.Errorf("InstallTools = %v", err)
	}
	if n := sys.startCount(); n != 0 {
		t.Errorf("downexd started %d times while running", n)
	}
}

// The real client reaches QueueService on the daemon's endpoint and the M6 DaemonService
// methods on its daemon client.
func TestClientDaemonQueueService(t *testing.T) {
	sys := &fakeSystem{}
	sys.startByUser()
	d := NewClientDaemon(sys.env(t))
	l, err := d.ListQueues(callCtx(t))
	if err != nil {
		t.Fatal(err)
	}
	want := &downexv1.ListQueuesResponse{Queues: []*downexv1.Queue{{Id: 1, Name: "Main queue", IsMain: true, Running: true}}}
	if !proto.Equal(l, want) {
		t.Errorf("ListQueues = %v, want %v", l, want)
	}
	if _, err := d.StartQueue(callCtx(t), &downexv1.StartQueueRequest{Id: 9}); errText(err) != "not_found: queue not found" {
		t.Errorf("StartQueue = %v", err)
	}
	// An older downexd without the M6 methods answers unimplemented.
	for name, call := range map[string]func() error{
		"CheckForUpdate": func() error { _, err := d.CheckForUpdate(callCtx(t)); return err },
		"InstallUpdate":  func() error { _, err := d.InstallUpdate(callCtx(t)); return err },
		"SetSecret":      func() error { _, err := d.SetSecret(callCtx(t), &downexv1.SetSecretRequest{Kind: "proxy"}); return err },
		"ClearSecret": func() error {
			_, err := d.ClearSecret(callCtx(t), &downexv1.ClearSecretRequest{Kind: "proxy"})
			return err
		},
		"CancelPostAction": func() error {
			_, err := d.CancelPostAction(callCtx(t), &downexv1.CancelPostActionRequest{})
			return err
		},
		"CreateQueue":   func() error { _, err := d.CreateQueue(callCtx(t), &downexv1.CreateQueueRequest{}); return err },
		"UpdateQueue":   func() error { _, err := d.UpdateQueue(callCtx(t), &downexv1.UpdateQueueRequest{}); return err },
		"DeleteQueue":   func() error { _, err := d.DeleteQueue(callCtx(t), &downexv1.DeleteQueueRequest{Id: 2}); return err },
		"StopQueue":     func() error { _, err := d.StopQueue(callCtx(t), &downexv1.StopQueueRequest{Id: 1}); return err },
		"MoveDownloads": func() error { _, err := d.MoveDownloads(callCtx(t), &downexv1.MoveDownloadsRequest{}); return err },
	} {
		if err := call(); !hasPrefix(err, "unimplemented: ") {
			t.Errorf("%s on a daemon without it = %v", name, err)
		}
	}
	if n := sys.startCount(); n != 0 {
		t.Errorf("downexd started %d times while running", n)
	}
}
