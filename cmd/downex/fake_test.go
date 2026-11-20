package main

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"downex/internal/client"
	"downex/internal/ipc"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/rpc/downex/v1/downexv1connect"
)

// These helpers run the CLI against fake daemon services on an httptest server: the
// Connect clients are pointed at it through the injectable system, and a fake dialer
// and spawner stand in for the local endpoint and process start.

// fakeDaemon implements DownloadService and DaemonService. Handlers record their
// requests; the fields configure the answers.
type fakeDaemon struct {
	downexv1connect.UnimplementedDownloadServiceHandler
	downexv1connect.UnimplementedDaemonServiceHandler
	downexv1connect.UnimplementedQueueServiceHandler
	fakeM6 // queues, secrets, updates, post actions (fake_m6_test.go)

	mu         sync.Mutex
	downloads  map[int64]*downexv1.Download
	settings   *downexv1.Settings
	status     *downexv1.StatusResponse
	addErr     error
	adds       []*downexv1.AddRequest
	pauses     []int64
	resumes    []*downexv1.ResumeRequest
	removes    []*downexv1.RemoveRequest
	confirms   []*downexv1.ConfirmRequest
	options    []*downexv1.SetDownloadOptionsRequest
	updates    []*downexv1.Settings
	watches    []*downexv1.WatchRequest
	shutdowns  int
	onShutdown func()
	// Media: AddMedia answers download 7 (kind media, the probe's tracks), ProbeMedia
	// answers probe; mediaErr fails both.
	probe       *downexv1.MediaInfo
	mediaErr    error
	mediaAdds   []*downexv1.AddMediaRequest
	mediaProbes []*downexv1.ProbeMediaRequest
	// mediaRetry fails the next AddMedia/ProbeMedia calls, one error each (site media:
	// tools_installing, site_resolving), before they answer.
	mediaRetry []error
	// Site-media tools (docs/SITE-MEDIA-SPEC.md §3.3): GetTools answers tools (toolsErr
	// fails it); InstallTools waits for installWait (when set) or the caller's end, then
	// answers tools or installErr.
	tools        *downexv1.ToolsStatus
	toolsErr     error
	installErr   error
	installWait  chan struct{}
	toolInstalls []*downexv1.InstallToolsRequest
	// RenewLink answers renewed (renewErr fails it); renews records the requests.
	renews   []*downexv1.RenewLinkRequest
	renewed  *downexv1.RenewLinkResponse
	renewErr error
	// Rescue answers rescued (rescueErr fails it); rescues records the requests.
	rescues   []*downexv1.RescueRequest
	rescued   *downexv1.RescueResponse
	rescueErr error
	// watch serves Watch when set; send delivers one batch of events.
	watch func(ctx context.Context, req *downexv1.WatchRequest, send func(...*downexv1.Event) error) error
}

func (f *fakeDaemon) RenewLink(_ context.Context, req *connect.Request[downexv1.RenewLinkRequest]) (*connect.Response[downexv1.RenewLinkResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renews = append(f.renews, req.Msg)
	if f.renewErr != nil {
		return nil, f.renewErr
	}
	return connect.NewResponse(f.renewed), nil
}

func (f *fakeDaemon) Rescue(_ context.Context, req *connect.Request[downexv1.RescueRequest]) (*connect.Response[downexv1.RescueResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rescues = append(f.rescues, req.Msg)
	if f.rescueErr != nil {
		return nil, f.rescueErr
	}
	return connect.NewResponse(f.rescued), nil
}

func (f *fakeDaemon) Add(_ context.Context, req *connect.Request[downexv1.AddRequest]) (*connect.Response[downexv1.AddResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.adds = append(f.adds, req.Msg)
	if f.addErr != nil {
		return nil, f.addErr
	}
	d := &downexv1.Download{Id: 7, Url: req.Msg.GetUrl(), Dir: req.Msg.GetDir(), Size: -1,
		State: downexv1.DownloadState_DOWNLOAD_STATE_QUEUED, QueuePosition: 1}
	if req.Msg.GetStartPaused() {
		d.State, d.QueuePosition = downexv1.DownloadState_DOWNLOAD_STATE_PAUSED, 0
	}
	f.downloads[d.Id] = d
	return connect.NewResponse(&downexv1.AddResponse{Download: clone(d)}), nil
}

func (f *fakeDaemon) AddMedia(_ context.Context, req *connect.Request[downexv1.AddMediaRequest]) (*connect.Response[downexv1.AddMediaResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mediaAdds = append(f.mediaAdds, req.Msg)
	if err := f.nextMediaErr(); err != nil {
		return nil, err
	}
	mi := proto.Clone(f.probe).(*downexv1.MediaInfo)
	if v := req.Msg.GetVideoTrack(); v != "" {
		mi.SelectedVideo = v
	}
	if a := req.Msg.GetAudioTrack(); a != "" {
		mi.SelectedAudio = a
	}
	name := req.Msg.GetFilename()
	if name == "" {
		name = "stream"
	}
	d := &downexv1.Download{Id: 7, Url: req.Msg.GetManifestUrl(), Dir: req.Msg.GetDir(), Filename: name + ".mp4", Size: -1,
		State: downexv1.DownloadState_DOWNLOAD_STATE_QUEUED, QueuePosition: 1, Kind: downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA,
		Media: mi}
	if req.Msg.GetStartPaused() {
		d.State, d.QueuePosition = downexv1.DownloadState_DOWNLOAD_STATE_PAUSED, 0
	}
	f.downloads[d.Id] = d
	return connect.NewResponse(&downexv1.AddMediaResponse{Download: clone(d)}), nil
}

func (f *fakeDaemon) ProbeMedia(_ context.Context, req *connect.Request[downexv1.ProbeMediaRequest]) (*connect.Response[downexv1.ProbeMediaResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mediaProbes = append(f.mediaProbes, req.Msg)
	if err := f.nextMediaErr(); err != nil {
		return nil, err
	}
	return connect.NewResponse(&downexv1.ProbeMediaResponse{Media: proto.Clone(f.probe).(*downexv1.MediaInfo)}), nil
}

// nextMediaErr is the failure of a media call: the next mediaRetry error, else mediaErr.
// f.mu must be held.
func (f *fakeDaemon) nextMediaErr() error {
	if len(f.mediaRetry) > 0 {
		err := f.mediaRetry[0]
		f.mediaRetry = f.mediaRetry[1:]
		return err
	}
	return f.mediaErr
}

func (f *fakeDaemon) GetTools(context.Context, *connect.Request[downexv1.GetToolsRequest]) (*connect.Response[downexv1.GetToolsResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.toolsErr != nil {
		return nil, f.toolsErr
	}
	return connect.NewResponse(&downexv1.GetToolsResponse{Status: proto.Clone(f.tools).(*downexv1.ToolsStatus)}), nil
}

func (f *fakeDaemon) InstallTools(ctx context.Context, req *connect.Request[downexv1.InstallToolsRequest]) (*connect.Response[downexv1.InstallToolsResponse], error) {
	f.mu.Lock()
	f.toolInstalls = append(f.toolInstalls, req.Msg)
	wait := f.installWait
	f.mu.Unlock()
	if wait != nil {
		select {
		case <-wait:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.installErr != nil {
		return nil, f.installErr
	}
	return connect.NewResponse(&downexv1.InstallToolsResponse{Status: proto.Clone(f.tools).(*downexv1.ToolsStatus)}), nil
}

func (f *fakeDaemon) List(context.Context, *connect.Request[downexv1.ListRequest]) (*connect.Response[downexv1.ListResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	resp := &downexv1.ListResponse{}
	for _, d := range f.downloads {
		resp.Downloads = append(resp.Downloads, clone(d))
	}
	slices.SortFunc(resp.Downloads, func(x, y *downexv1.Download) int { return cmp.Compare(x.Id, y.Id) })
	return connect.NewResponse(resp), nil
}

// lookup returns download id, or a NotFound error. f.mu must be held.
func (f *fakeDaemon) lookup(id int64) (*downexv1.Download, error) {
	d, ok := f.downloads[id]
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("download %d not found", id))
	}
	return d, nil
}

func (f *fakeDaemon) Get(_ context.Context, req *connect.Request[downexv1.GetRequest]) (*connect.Response[downexv1.GetResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, err := f.lookup(req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&downexv1.GetResponse{Download: clone(d)}), nil
}

func (f *fakeDaemon) Pause(_ context.Context, req *connect.Request[downexv1.PauseRequest]) (*connect.Response[downexv1.PauseResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pauses = append(f.pauses, req.Msg.GetId())
	d, err := f.lookup(req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	d.State, d.SpeedBps, d.Connections = downexv1.DownloadState_DOWNLOAD_STATE_PAUSED, 0, 0
	return connect.NewResponse(&downexv1.PauseResponse{Download: clone(d)}), nil
}

func (f *fakeDaemon) Resume(_ context.Context, req *connect.Request[downexv1.ResumeRequest]) (*connect.Response[downexv1.ResumeResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resumes = append(f.resumes, req.Msg)
	d, err := f.lookup(req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	if req.Msg.GetExistingFile() != downexv1.ExistingFile_EXISTING_FILE_UNSPECIFIED && !req.Msg.GetRestart() {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("existing_file is only for a restart"))
	}
	completed := d.State == downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED
	if completed && req.Msg.GetRestart() && req.Msg.GetExistingFile() != downexv1.ExistingFile_EXISTING_FILE_REPLACE {
		// As downexd: keep both saves the new copy under a free name.
		ext := filepath.Ext(d.Filename)
		d.Filename = strings.TrimSuffix(d.Filename, ext) + " (1)" + ext
	}
	if !completed || req.Msg.GetRestart() {
		d.State, d.QueuePosition = downexv1.DownloadState_DOWNLOAD_STATE_QUEUED, 2
	}
	return connect.NewResponse(&downexv1.ResumeResponse{Download: clone(d)}), nil
}

func (f *fakeDaemon) Remove(_ context.Context, req *connect.Request[downexv1.RemoveRequest]) (*connect.Response[downexv1.RemoveResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removes = append(f.removes, req.Msg)
	if _, err := f.lookup(req.Msg.GetId()); err != nil {
		return nil, err
	}
	delete(f.downloads, req.Msg.GetId())
	return connect.NewResponse(&downexv1.RemoveResponse{}), nil
}

func (f *fakeDaemon) Confirm(_ context.Context, req *connect.Request[downexv1.ConfirmRequest]) (*connect.Response[downexv1.ConfirmResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.confirms = append(f.confirms, req.Msg)
	d, err := f.lookup(req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	if d.State != downexv1.DownloadState_DOWNLOAD_STATE_AWAITING_CONFIRMATION {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("download %d is not awaiting confirmation (it is paused)", d.Id))
	}
	if req.Msg.GetDir() != "" {
		d.Dir = req.Msg.GetDir()
	}
	if req.Msg.GetFilename() != "" {
		d.Filename = req.Msg.GetFilename()
	}
	d.State, d.QueuePosition = downexv1.DownloadState_DOWNLOAD_STATE_PAUSED, 0
	if req.Msg.GetStart() {
		d.State, d.QueuePosition = downexv1.DownloadState_DOWNLOAD_STATE_QUEUED, 1
	}
	return connect.NewResponse(&downexv1.ConfirmResponse{Download: clone(d)}), nil
}

func (f *fakeDaemon) Watch(ctx context.Context, req *connect.Request[downexv1.WatchRequest], stream *connect.ServerStream[downexv1.WatchResponse]) error {
	f.mu.Lock()
	f.watches = append(f.watches, req.Msg)
	watch := f.watch
	f.mu.Unlock()
	if watch == nil {
		return connect.NewError(connect.CodeUnimplemented, errors.New("no watch in this test"))
	}
	return watch(ctx, req.Msg, func(evs ...*downexv1.Event) error {
		return stream.Send(&downexv1.WatchResponse{Events: evs})
	})
}

func (f *fakeDaemon) Status(context.Context, *connect.Request[downexv1.StatusRequest]) (*connect.Response[downexv1.StatusResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return connect.NewResponse(proto.Clone(f.status).(*downexv1.StatusResponse)), nil
}

func (f *fakeDaemon) GetSettings(context.Context, *connect.Request[downexv1.GetSettingsRequest]) (*connect.Response[downexv1.GetSettingsResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return connect.NewResponse(&downexv1.GetSettingsResponse{Settings: f.settingsOut()}), nil
}

func (f *fakeDaemon) UpdateSettings(_ context.Context, req *connect.Request[downexv1.UpdateSettingsRequest]) (*connect.Response[downexv1.UpdateSettingsResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = append(f.updates, proto.Clone(req.Msg.GetSettings()).(*downexv1.Settings))
	f.settings = proto.Clone(req.Msg.GetSettings()).(*downexv1.Settings)
	f.assignLoginIDs()
	return connect.NewResponse(&downexv1.UpdateSettingsResponse{Settings: f.settingsOut()}), nil
}

func (f *fakeDaemon) Shutdown(context.Context, *connect.Request[downexv1.ShutdownRequest]) (*connect.Response[downexv1.ShutdownResponse], error) {
	f.mu.Lock()
	f.shutdowns++
	fn := f.onShutdown
	f.mu.Unlock()
	if fn != nil {
		fn()
	}
	return connect.NewResponse(&downexv1.ShutdownResponse{}), nil
}

func clone(d *downexv1.Download) *downexv1.Download { return proto.Clone(d).(*downexv1.Download) }

// do runs fn with the fake's lock held: tests change the fake's answers through it,
// because the handlers run on server goroutines.
func (f *fakeDaemon) do(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

// record is a copy of the requests a fakeDaemon received.
type record struct {
	adds      []*downexv1.AddRequest
	pauses    []int64
	resumes   []*downexv1.ResumeRequest
	removes   []*downexv1.RemoveRequest
	confirms  []*downexv1.ConfirmRequest
	updates   []*downexv1.Settings
	watches   []*downexv1.WatchRequest
	shutdowns int
}

func (f *fakeDaemon) rec() record {
	f.mu.Lock()
	defer f.mu.Unlock()
	return record{
		adds: slices.Clone(f.adds), pauses: slices.Clone(f.pauses), resumes: slices.Clone(f.resumes),
		removes: slices.Clone(f.removes), confirms: slices.Clone(f.confirms), updates: slices.Clone(f.updates),
		watches:   slices.Clone(f.watches),
		shutdowns: f.shutdowns,
	}
}

// fakeHost stands in for the local endpoint (dialer) and process start (spawner).
type fakeHost struct {
	srv *httptest.Server

	mu         sync.Mutex
	up         bool     // a daemon listens on the endpoint
	dialErr    error    // when set, every dial fails with it
	readyAfter int      // after a spawn, dials that still find nothing before the daemon is up; <0 = never
	pending    int      // dials since the spawn
	spawned    bool     // the daemon was started
	spawnErr   error    // startDetached fails with it
	daemonErr  error    // daemonPath fails with it
	dials      []string // endpoints dialled
	spawns     [][]string
	termSize   func() (int, int) // when set, stdout is a terminal of this size
	elevated   bool              // the CLI runs with administrator rights
	password   string            // what the password prompt returns
	pwErr      error             // the password prompt fails with it
	prompts    []string          // password prompts shown

	startTimeout, stopTimeout time.Duration
	retryFor                  time.Duration // app.retryFor (0 = the CLI's)
}

func newFakeDaemon(t *testing.T) (*fakeDaemon, *fakeHost) {
	t.Helper()
	f := &fakeDaemon{
		downloads: make(map[int64]*downexv1.Download),
		settings: &downexv1.Settings{MaxConcurrentDownloads: 3, GlobalSpeedLimit: 0, DefaultDir: "/home/u/Downloads",
			DefaultMaxConnections: 8,
			Categories: []*downexv1.Category{{Name: "Video", Extensions: []string{"mp4", "mkv"}},
				{Name: "Books", Extensions: []string{"epub"}, Dir: "/srv/books"}},
			UseCategoryFolders: true, ConfirmBrowserDownloads: true, NotifyOnComplete: true, NotifyOnFailure: true,
			ShowTrayIcon: true},
		status: &downexv1.StatusResponse{Version: "0.2.0-test", Pid: 4242, Endpoint: "fake-endpoint"},
		probe:  sampleProbe(),
		tools:  sampleTools(),
	}
	mux := http.NewServeMux()
	mux.Handle(downexv1connect.NewDownloadServiceHandler(f))
	mux.Handle(downexv1connect.NewDaemonServiceHandler(f))
	mux.Handle(downexv1connect.NewQueueServiceHandler(f))
	f.initM6()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return f, &fakeHost{srv: srv, up: true, startTimeout: 2 * time.Second, stopTimeout: 2 * time.Second}
}

func (h *fakeHost) setUp(up bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.up = up
}

func (h *fakeHost) dial(_ context.Context, endpoint string) (net.Conn, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.dials = append(h.dials, endpoint)
	if h.dialErr != nil {
		return nil, h.dialErr
	}
	if !h.up && h.spawned && h.readyAfter >= 0 {
		if h.pending >= h.readyAfter {
			h.up = true
		}
		h.pending++
	}
	if !h.up {
		return nil, fmt.Errorf("fake dial %s: %w", endpoint, ipc.ErrNotRunning)
	}
	c, peer := net.Pipe()
	_ = peer.Close()
	return c, nil
}

func (h *fakeHost) startDetached(exe string, args ...string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.spawns = append(h.spawns, append([]string{exe}, args...))
	if h.spawnErr != nil {
		return h.spawnErr
	}
	h.spawned, h.pending = true, 0
	return nil
}

func (h *fakeHost) recorded() (dials []string, spawns [][]string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.dials), slices.Clone(h.spawns)
}

const fakeDaemonPath = "/opt/downex/downexd"

func (h *fakeHost) system() system {
	return system{
		Env: client.Env{
			Endpoint:   func() (string, error) { return "fake-endpoint", nil },
			Dial:       h.dial,
			HTTPClient: func(string) connect.HTTPClient { return h.srv.Client() },
			BaseURL:    h.srv.URL,
			DaemonPath: func() (string, error) {
				if h.daemonErr != nil {
					return "", h.daemonErr
				}
				return fakeDaemonPath, nil
			},
			StartDetached: h.startDetached,
			Elevated:      func() (bool, error) { return h.elevated, nil },
			LogDir:        func() (string, error) { return "/fake/logs", nil },
			StartTimeout:  h.startTimeout,
			PollInterval:  5 * time.Millisecond,
		},
		terminal: func(io.Writer) (func() (int, int), func(), bool) {
			if h.termSize == nil {
				return nil, nil, false
			}
			return h.termSize, func() {}, true
		},
		stopTimeout: h.stopTimeout,
		password:    h.readPassword,
	}
}

func (h *fakeHost) readPassword(_ context.Context, _ io.Writer, prompt string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.prompts = append(h.prompts, prompt)
	return h.password, h.pwErr
}

func (h *fakeHost) promptsShown() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.prompts)
}

// run runs one command line against the fake daemon.
func (h *fakeHost) run(ctx context.Context, args ...string) cliResult {
	var stdout, stderr syncBuffer
	code := h.newApp(&stdout, &stderr).run(ctx, args)
	return cliResult{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

// newApp is newApp on the fake host, with the waits of site media shortened: tries every
// 5 ms (for retryFor when set) and progress read every 5 ms.
func (h *fakeHost) newApp(stdout, stderr io.Writer) *app {
	a := newApp(stdout, stderr, h.system())
	a.retryEvery, a.toolsPoll = 5*time.Millisecond, 5*time.Millisecond
	if h.retryFor > 0 {
		a.retryFor = h.retryFor
	}
	return a
}

// offlineSystem is a system where no daemon runs and none can be started, so that
// commands use the database directly or fail without touching a real daemon.
func offlineSystem() system {
	noDaemon := errors.New("starting downexd is disabled in this test")
	return system{
		Env: client.Env{
			Endpoint: func() (string, error) { return "offline-test-endpoint", nil },
			Dial: func(_ context.Context, endpoint string) (net.Conn, error) {
				return nil, fmt.Errorf("test dial %s: %w", endpoint, ipc.ErrNotRunning)
			},
			HTTPClient:    func(string) connect.HTTPClient { return http.DefaultClient },
			BaseURL:       "http://downexd.invalid",
			DaemonPath:    func() (string, error) { return "", noDaemon },
			StartDetached: func(string, ...string) error { return noDaemon },
			Elevated:      func() (bool, error) { return false, nil },
			LogDir:        func() (string, error) { return "", noDaemon },
			StartTimeout:  100 * time.Millisecond,
			PollInterval:  10 * time.Millisecond,
		},
		terminal:    func(io.Writer) (func() (int, int), func(), bool) { return nil, nil, false },
		stopTimeout: 100 * time.Millisecond,
		password: func(context.Context, io.Writer, string) (string, error) {
			return "", errors.New("no password prompt in this test")
		},
	}
}

func runWith(ctx context.Context, sys system, args ...string) cliResult {
	var stdout, stderr syncBuffer
	code := newApp(&stdout, &stderr, sys).run(ctx, args)
	return cliResult{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

// bgRun is a CLI invocation in progress whose output can be read while it runs.
type bgRun struct {
	stdout, stderr *syncBuffer
	done           chan int
}

func (h *fakeHost) start(ctx context.Context, args ...string) *bgRun {
	r := &bgRun{stdout: new(syncBuffer), stderr: new(syncBuffer), done: make(chan int, 1)}
	a := h.newApp(r.stdout, r.stderr)
	go func() { r.done <- a.run(ctx, args) }()
	return r
}

// wait returns the exit status, failing the test if the command does not finish.
func (r *bgRun) wait(t *testing.T) int {
	t.Helper()
	select {
	case code := <-r.done:
		return code
	case <-time.After(10 * time.Second):
		t.Fatalf("command did not finish; stdout:\n%s\nstderr:\n%s", r.stdout, r.stderr)
		return -1
	}
}

// waitFor polls until cond holds, failing the test after a few seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// syncBuffer is a bytes.Buffer safe for concurrent use that also records when each
// Write happened.
type syncBuffer struct {
	mu     sync.Mutex
	b      bytes.Buffer
	writes []time.Time
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes = append(s.writes, time.Now())
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func (s *syncBuffer) writeTimes() []time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.writes)
}

// Event constructors.
func snapshot(d *downexv1.Download) *downexv1.Event {
	return &downexv1.Event{Kind: &downexv1.Event_Snapshot{Snapshot: d}}
}

func updated(d *downexv1.Download) *downexv1.Event {
	return &downexv1.Event{Kind: &downexv1.Event_Updated{Updated: d}}
}

func removed(id int64) *downexv1.Event {
	return &downexv1.Event{Kind: &downexv1.Event_Removed{Removed: id}}
}

func (f *fakeDaemon) SetDownloadOptions(_ context.Context, req *connect.Request[downexv1.SetDownloadOptionsRequest]) (*connect.Response[downexv1.SetDownloadOptionsResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.options = append(f.options, req.Msg)
	d, err := f.lookup(req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	if d.State == downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("download %d is already completed", d.Id))
	}
	if req.Msg.SpeedLimit != nil {
		d.SpeedLimit = req.Msg.GetSpeedLimit()
	}
	if req.Msg.MaxConnections != nil {
		d.MaxConnections = req.Msg.GetMaxConnections()
		if d.MaxConnections == 0 {
			d.MaxConnections = f.settings.GetDefaultMaxConnections()
		}
	}
	return connect.NewResponse(&downexv1.SetDownloadOptionsResponse{Download: clone(d)}), nil
}

// optionRequests returns the SetDownloadOptions requests received.
func (f *fakeDaemon) optionRequests() []*downexv1.SetDownloadOptionsRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.options)
}
