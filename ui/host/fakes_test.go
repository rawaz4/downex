package host

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	downexv1 "downex/internal/rpc/downex/v1"
)

// fakeDaemon is an in-memory Daemon that records its requests (today's ui/fakes_test.go).
type fakeDaemon struct {
	mu        sync.Mutex
	downloads map[int64]*downexv1.Download
	settings  *downexv1.Settings
	status    *downexv1.StatusResponse
	err       error // returned by every unary call when set
	requests  []proto.Message
	gets      int
	// M6
	queues     []*downexv1.Queue
	nextQueue  int64
	updateInfo *downexv1.UpdateInfo
	// Site media: the tools' state (nil = a daemon without them, unimplemented).
	tools *downexv1.ToolsStatus

	// watch serves Watch; nil refuses with unavailable.
	watch       func(ctx context.Context) (WatchStream, error)
	watchCalls  int
	watchIDs    [][]int64     // the ids of each Watch call
	watchCalled chan struct{} // receives a value per Watch call when not nil
}

var _ Daemon = (*fakeDaemon)(nil)

func newFakeDaemon(ds ...*downexv1.Download) *fakeDaemon {
	f := &fakeDaemon{downloads: map[int64]*downexv1.Download{}}
	for _, d := range ds {
		f.downloads[d.GetId()] = d
	}
	return f
}

func (f *fakeDaemon) record(m proto.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m != nil {
		f.requests = append(f.requests, proto.Clone(m))
	}
	return f.err
}

func (f *fakeDaemon) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakeDaemon) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func (f *fakeDaemon) lastRequest(t *testing.T) proto.Message {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		t.Fatal("no request reached the daemon")
	}
	return f.requests[len(f.requests)-1]
}

func (f *fakeDaemon) lookup(id int64) (*downexv1.Download, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.downloads[id]
	if d == nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such download"))
	}
	return proto.CloneOf(d), nil
}

func (f *fakeDaemon) List(context.Context) (*downexv1.ListResponse, error) {
	if err := f.record(nil); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	resp := &downexv1.ListResponse{}
	for id := int64(1); id <= 100; id++ {
		if d := f.downloads[id]; d != nil {
			resp.Downloads = append(resp.Downloads, d)
		}
	}
	return resp, nil
}

func (f *fakeDaemon) Get(_ context.Context, id int64) (*downexv1.Download, error) {
	f.mu.Lock()
	f.gets++
	f.mu.Unlock()
	if err := f.record(&downexv1.GetRequest{Id: id}); err != nil {
		return nil, err
	}
	return f.lookup(id)
}

func (f *fakeDaemon) Add(_ context.Context, req *downexv1.AddRequest) (*downexv1.AddResponse, error) {
	if err := f.record(req); err != nil {
		return nil, err
	}
	return &downexv1.AddResponse{Download: &downexv1.Download{
		Id: 99, Url: req.GetUrl(), Dir: req.GetDir(), Filename: req.GetFilename(), Size: -1,
		State: downexv1.DownloadState_DOWNLOAD_STATE_QUEUED, MaxConnections: req.GetMaxConnections(),
		SpeedLimit: req.GetSpeedLimit(), QueuePosition: 1,
	}}, nil
}

func (f *fakeDaemon) Pause(_ context.Context, req *downexv1.PauseRequest) (*downexv1.PauseResponse, error) {
	if err := f.record(req); err != nil {
		return nil, err
	}
	d, err := f.lookup(req.GetId())
	if err != nil {
		return nil, err
	}
	d.State = downexv1.DownloadState_DOWNLOAD_STATE_PAUSED
	return &downexv1.PauseResponse{Download: d}, nil
}

func (f *fakeDaemon) Resume(_ context.Context, req *downexv1.ResumeRequest) (*downexv1.ResumeResponse, error) {
	if err := f.record(req); err != nil {
		return nil, err
	}
	d, err := f.lookup(req.GetId())
	if err != nil {
		return nil, err
	}
	d.State = downexv1.DownloadState_DOWNLOAD_STATE_QUEUED
	return &downexv1.ResumeResponse{Download: d}, nil
}

func (f *fakeDaemon) Remove(_ context.Context, req *downexv1.RemoveRequest) (*downexv1.RemoveResponse, error) {
	if err := f.record(req); err != nil {
		return nil, err
	}
	if _, err := f.lookup(req.GetId()); err != nil {
		return nil, err
	}
	return &downexv1.RemoveResponse{}, nil
}

func (f *fakeDaemon) Confirm(_ context.Context, req *downexv1.ConfirmRequest) (*downexv1.ConfirmResponse, error) {
	if err := f.record(req); err != nil {
		return nil, err
	}
	d, err := f.lookup(req.GetId())
	if err != nil {
		return nil, err
	}
	if d.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_AWAITING_CONFIRMATION {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("download is not awaiting confirmation"))
	}
	d.State = downexv1.DownloadState_DOWNLOAD_STATE_PAUSED
	if req.GetStart() {
		d.State = downexv1.DownloadState_DOWNLOAD_STATE_QUEUED
	}
	return &downexv1.ConfirmResponse{Download: d}, nil
}

func (f *fakeDaemon) AddMedia(_ context.Context, req *downexv1.AddMediaRequest) (*downexv1.AddMediaResponse, error) {
	if err := f.record(req); err != nil {
		return nil, err
	}
	return &downexv1.AddMediaResponse{Download: &downexv1.Download{
		Id: 98, Url: req.GetManifestUrl(), Dir: req.GetDir(), Filename: "Keynote.mp4", Size: -1,
		State: downexv1.DownloadState_DOWNLOAD_STATE_QUEUED, Kind: downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA,
	}}, nil
}

func (f *fakeDaemon) ProbeMedia(_ context.Context, req *downexv1.ProbeMediaRequest) (*downexv1.ProbeMediaResponse, error) {
	if err := f.record(req); err != nil {
		return nil, err
	}
	return &downexv1.ProbeMediaResponse{Media: &downexv1.MediaInfo{ManifestUrl: req.GetManifestUrl(), Format: "hls"}}, nil
}

func (f *fakeDaemon) GetTools(context.Context) (*downexv1.GetToolsResponse, error) {
	if err := f.record(&downexv1.GetToolsRequest{}); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.tools == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("downex.v1.DaemonService.GetTools is not implemented"))
	}
	return &downexv1.GetToolsResponse{Status: proto.CloneOf(f.tools)}, nil
}

func (f *fakeDaemon) InstallTools(_ context.Context, req *downexv1.InstallToolsRequest) (*downexv1.InstallToolsResponse, error) {
	if err := f.record(req); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.tools == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("downex.v1.DaemonService.InstallTools is not implemented"))
	}
	return &downexv1.InstallToolsResponse{Status: proto.CloneOf(f.tools)}, nil
}

func (f *fakeDaemon) Rescue(_ context.Context, req *downexv1.RescueRequest) (*downexv1.RescueResponse, error) {
	if err := f.record(req); err != nil {
		return nil, err
	}
	return &downexv1.RescueResponse{Download: &downexv1.Download{
		Id: 98, Url: req.GetUrl(), Size: 100, Received: 40, RescuedBytes: 40,
		State: downexv1.DownloadState_DOWNLOAD_STATE_QUEUED,
	}, RescuedBytes: 40}, nil
}

func (f *fakeDaemon) RenewLink(_ context.Context, req *downexv1.RenewLinkRequest) (*downexv1.RenewLinkResponse, error) {
	if err := f.record(req); err != nil {
		return nil, err
	}
	d, err := f.lookup(req.GetId())
	if err != nil {
		return nil, err
	}
	return &downexv1.RenewLinkResponse{Download: d}, nil
}

func (f *fakeDaemon) SetDownloadOptions(_ context.Context, req *downexv1.SetDownloadOptionsRequest) (*downexv1.SetDownloadOptionsResponse, error) {
	if err := f.record(req); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.downloads[req.GetId()]
	if d == nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such download"))
	}
	if req.SpeedLimit != nil {
		d.SpeedLimit = req.GetSpeedLimit()
	}
	if req.MaxConnections != nil {
		d.MaxConnections = req.GetMaxConnections()
	}
	return &downexv1.SetDownloadOptionsResponse{Download: proto.CloneOf(d)}, nil
}

func (f *fakeDaemon) PauseAll(context.Context) (*downexv1.PauseAllResponse, error) {
	if err := f.record(&downexv1.PauseAllRequest{}); err != nil {
		return nil, err
	}
	return &downexv1.PauseAllResponse{Paused: 2}, nil
}

func (f *fakeDaemon) ResumeAll(context.Context) (*downexv1.ResumeAllResponse, error) {
	if err := f.record(&downexv1.ResumeAllRequest{}); err != nil {
		return nil, err
	}
	return &downexv1.ResumeAllResponse{}, nil
}

func (f *fakeDaemon) GetSettings(context.Context) (*downexv1.GetSettingsResponse, error) {
	if err := f.record(&downexv1.GetSettingsRequest{}); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return &downexv1.GetSettingsResponse{Settings: proto.CloneOf(f.settings)}, nil
}

func (f *fakeDaemon) UpdateSettings(_ context.Context, req *downexv1.UpdateSettingsRequest) (*downexv1.UpdateSettingsResponse, error) {
	if err := f.record(req); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settings = proto.CloneOf(req.GetSettings())
	return &downexv1.UpdateSettingsResponse{Settings: proto.CloneOf(f.settings)}, nil
}

func (f *fakeDaemon) Status(context.Context) (*downexv1.StatusResponse, error) {
	if err := f.record(&downexv1.StatusRequest{}); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status, nil
}

func (f *fakeDaemon) Watch(ctx context.Context, ids []int64) (WatchStream, error) {
	f.mu.Lock()
	f.watchCalls++
	f.watchIDs = append(f.watchIDs, slices.Clone(ids))
	watch, called := f.watch, f.watchCalled
	f.mu.Unlock()
	if called != nil {
		called <- struct{}{}
	}
	if watch == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("downexd is not running"))
	}
	return watch(ctx)
}

func (f *fakeDaemon) watches() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.watchCalls
}

// ---- M6 (today's ui/fakes_m6_test.go) -------------------------------------------------

func (f *fakeDaemon) queueList() []*downexv1.Queue {
	if len(f.queues) == 0 {
		f.queues = []*downexv1.Queue{{Id: 1, Name: "Main queue", Running: true, IsMain: true}}
		f.nextQueue = 2
	}
	return f.queues
}

func (f *fakeDaemon) queue(id int64) (*downexv1.Queue, error) {
	for _, q := range f.queueList() {
		if q.GetId() == id {
			return q, nil
		}
	}
	return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("queue %d not found", id))
}

func (f *fakeDaemon) ListQueues(context.Context) (*downexv1.ListQueuesResponse, error) {
	if err := f.record(&downexv1.ListQueuesRequest{}); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	resp := &downexv1.ListQueuesResponse{}
	for _, q := range f.queueList() {
		resp.Queues = append(resp.Queues, proto.CloneOf(q))
	}
	return resp, nil
}

func (f *fakeDaemon) CreateQueue(_ context.Context, req *downexv1.CreateQueueRequest) (*downexv1.CreateQueueResponse, error) {
	if err := f.record(req); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queueList()
	q := &downexv1.Queue{Id: f.nextQueue, Name: req.GetQueue().GetName()}
	f.nextQueue++
	f.queues = append(f.queues, q)
	return &downexv1.CreateQueueResponse{Queue: proto.CloneOf(q)}, nil
}

func (f *fakeDaemon) UpdateQueue(_ context.Context, req *downexv1.UpdateQueueRequest) (*downexv1.UpdateQueueResponse, error) {
	if err := f.record(req); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	q, err := f.queue(req.GetQueue().GetId())
	if err != nil {
		return nil, err
	}
	q.Name = req.GetQueue().GetName()
	return &downexv1.UpdateQueueResponse{Queue: proto.CloneOf(q)}, nil
}

func (f *fakeDaemon) DeleteQueue(_ context.Context, req *downexv1.DeleteQueueRequest) (*downexv1.DeleteQueueResponse, error) {
	if err := f.record(req); err != nil {
		return nil, err
	}
	return &downexv1.DeleteQueueResponse{}, nil
}

func (f *fakeDaemon) StartQueue(_ context.Context, req *downexv1.StartQueueRequest) (*downexv1.StartQueueResponse, error) {
	if err := f.record(req); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	q, err := f.queue(req.GetId())
	if err != nil {
		return nil, err
	}
	q.Running = true
	return &downexv1.StartQueueResponse{Queue: proto.CloneOf(q)}, nil
}

func (f *fakeDaemon) StopQueue(_ context.Context, req *downexv1.StopQueueRequest) (*downexv1.StopQueueResponse, error) {
	if err := f.record(req); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	q, err := f.queue(req.GetId())
	if err != nil {
		return nil, err
	}
	q.Running = false
	return &downexv1.StopQueueResponse{Queue: proto.CloneOf(q)}, nil
}

func (f *fakeDaemon) MoveDownloads(_ context.Context, req *downexv1.MoveDownloadsRequest) (*downexv1.MoveDownloadsResponse, error) {
	if err := f.record(req); err != nil {
		return nil, err
	}
	return &downexv1.MoveDownloadsResponse{}, nil
}

func (f *fakeDaemon) SetSecret(_ context.Context, req *downexv1.SetSecretRequest) (*downexv1.SetSecretResponse, error) {
	if err := f.record(req); err != nil {
		return nil, err
	}
	return &downexv1.SetSecretResponse{}, nil
}

func (f *fakeDaemon) ClearSecret(_ context.Context, req *downexv1.ClearSecretRequest) (*downexv1.ClearSecretResponse, error) {
	if err := f.record(req); err != nil {
		return nil, err
	}
	return &downexv1.ClearSecretResponse{}, nil
}

func (f *fakeDaemon) CheckForUpdate(context.Context) (*downexv1.CheckForUpdateResponse, error) {
	if err := f.record(&downexv1.CheckForUpdateRequest{}); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.updateInfo == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("updates are off: no update feed is set"))
	}
	return &downexv1.CheckForUpdateResponse{Info: proto.CloneOf(f.updateInfo)}, nil
}

func (f *fakeDaemon) InstallUpdate(context.Context) (*downexv1.InstallUpdateResponse, error) {
	if err := f.record(&downexv1.InstallUpdateRequest{}); err != nil {
		return nil, err
	}
	return &downexv1.InstallUpdateResponse{}, nil
}

func (f *fakeDaemon) CancelPostAction(_ context.Context, req *downexv1.CancelPostActionRequest) (*downexv1.CancelPostActionResponse, error) {
	if err := f.record(req); err != nil {
		return nil, err
	}
	return &downexv1.CancelPostActionResponse{}, nil
}

// ---- streams --------------------------------------------------------------------------

// fakeStream is a WatchStream that delivers batches until they run out, then ends with end
// (or, when end is nil, waits for ctx).
type fakeStream struct {
	ctx     context.Context
	batches chan *downexv1.WatchResponse
	end     error
	msg     *downexv1.WatchResponse
	err     error

	mu     sync.Mutex
	closed bool
}

func newFakeStream(ctx context.Context, end error, batches ...*downexv1.WatchResponse) *fakeStream {
	ch := make(chan *downexv1.WatchResponse, len(batches))
	for _, b := range batches {
		ch <- b
	}
	if end != nil {
		close(ch)
	}
	return &fakeStream{ctx: ctx, batches: ch, end: end}
}

func (s *fakeStream) Receive() bool {
	select {
	case m, ok := <-s.batches:
		if !ok {
			s.err = s.end
			return false
		}
		s.msg = m
		return true
	case <-s.ctx.Done():
		s.err = connect.NewError(connect.CodeCanceled, s.ctx.Err())
		return false
	}
}

func (s *fakeStream) Msg() *downexv1.WatchResponse { return s.msg }
func (s *fakeStream) Err() error                   { return s.err }

func (s *fakeStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func (s *fakeStream) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// ---- the window's side ----------------------------------------------------------------

// recorder stands in for the window: it receives the bridge's batches, answers the yes/no
// dialog and counts how often it was brought to the front (today's fakeHost).
type recorder struct {
	mu         sync.Mutex
	batches    []*downexv1.WatchResponse
	emitted    chan *downexv1.WatchResponse // receives each batch when not nil
	confirms   []string                     // title + "\n" + message of each question
	answer     bool
	confirmErr error
	focused    int
	shown      [][]int64 // the confirmation requests that reached the window
}

func (r *recorder) emit(m *downexv1.WatchResponse) {
	r.mu.Lock()
	r.batches = append(r.batches, m)
	ch := r.emitted
	r.mu.Unlock()
	if ch != nil {
		ch <- m
	}
}

func (r *recorder) confirm(title, message string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.confirms = append(r.confirms, title+"\n"+message)
	return r.answer, r.confirmErr
}

func (r *recorder) setAnswer(answer bool, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.answer, r.confirmErr = answer, err
}

func (r *recorder) questions() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.confirms)
}

func (r *recorder) focus() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.focused++
}

func (r *recorder) focusCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.focused
}

func (r *recorder) show(ids []int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.shown = append(r.shown, slices.Clone(ids))
}

// shownIDs returns every confirmation request that reached the window, in order.
func (r *recorder) shownIDs() []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []int64
	for _, ids := range r.shown {
		out = append(out, ids...)
	}
	return out
}

func (r *recorder) emittedCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.batches)
}

// fakeShell records the files it was asked to open or show.
type fakeShell struct {
	mu         sync.Mutex
	opened     []string
	openedWith []string // paths handed to OpenWith
	shown      []string // "reveal:<path>" or "folder:<dir>"
	err        error
	withErr    error // returned by OpenWith (the user closed the dialog, no dialog, …)
}

func (s *fakeShell) OpenWith(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.openedWith = append(s.openedWith, path)
	if s.withErr != nil {
		return s.withErr
	}
	return s.err
}

func (s *fakeShell) Open(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opened = append(s.opened, path)
	return s.err
}

func (s *fakeShell) Reveal(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shown = append(s.shown, "reveal:"+path)
	return s.err
}

func (s *fakeShell) ShowFolder(dir string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shown = append(s.shown, "folder:"+dir)
	return s.err
}

// newTestSession returns a session on d with a fake shell, fast backoff, a temporary theme
// file (never the user's own) and no way to start a window; it is closed when the test
// ends. args is the command line.
func newTestSession(t *testing.T, d Daemon, args ...string) (*Session, *fakeShell) {
	t.Helper()
	sh := &fakeShell{}
	s := NewSession(SessionOptions{
		Args: args, Version: "1.2.3-test", Daemon: d, Shell: sh,
		ThemePath:  filepath.Join(t.TempDir(), "Downex", ThemeFileName),
		MinBackoff: time.Millisecond, MaxBackoff: 4 * time.Millisecond,
		OpenWindow:       func(int64) error { return errors.New("no windows in tests") },
		OpenRaisedWindow: func(int64) error { return errors.New("no windows in tests") },
		Env:              func(string) string { return "" },
	})
	t.Cleanup(s.Close)
	return s, sh
}

// callCtx is a call's context in tests.
func callCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// errText is err's "<code>: <message>" as the window shows it ("" for nil).
func errText(err error) string {
	if ce := ErrorOf(err); ce != nil {
		return ce.Error()
	}
	return ""
}

// hasPrefix reports whether err's window text starts with prefix.
func hasPrefix(err error, prefix string) bool {
	return err != nil && strings.HasPrefix(errText(err), prefix)
}
