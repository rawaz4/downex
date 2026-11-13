package main

import (
	"context"
	"encoding/json"
	"errors"
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

// fakeDaemon is an in-memory Daemon that records its requests.
type fakeDaemon struct {
	mu        sync.Mutex
	downloads map[int64]*downexv1.Download
	settings  *downexv1.Settings
	status    *downexv1.StatusResponse
	err       error // returned by every unary call when set
	requests  []proto.Message
	gets      int
	// M6 (fakes_m6_test.go)
	queues     []*downexv1.Queue
	nextQueue  int64
	updateInfo *downexv1.UpdateInfo
	// Site media: the tools' state (nil = a daemon without them, unimplemented), and what
	// a probe or add of a web page answers instead of the sample video (siteErr).
	tools   *downexv1.ToolsStatus
	siteErr error

	// watch serves Watch; nil refuses with unavailable.
	watch       func(ctx context.Context) (WatchStream, error)
	watchCalls  int
	watchIDs    [][]int64     // the ids of each Watch call
	watchCalled chan struct{} // receives a value per Watch call when not nil
}

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
	if req.GetDir() != "" {
		d.Dir = req.GetDir()
	}
	if req.GetFilename() != "" {
		d.Filename = req.GetFilename()
	}
	if m := d.GetMedia(); m != nil {
		if req.GetVideoTrack() != "" {
			m.SelectedVideo = req.GetVideoTrack()
		}
		if req.GetAudioTrack() != "" {
			m.SelectedAudio = req.GetAudioTrack()
		}
	}
	d.State = downexv1.DownloadState_DOWNLOAD_STATE_PAUSED
	if req.GetStart() {
		d.State = downexv1.DownloadState_DOWNLOAD_STATE_QUEUED
	}
	return &downexv1.ConfirmResponse{Download: d}, nil
}

// errEncrypted is how the fake refuses a manifest whose URL mentions "drm", as downexd
// refuses encrypted streams (docs/MEDIA-SPEC.md §6.3).
var errEncrypted = connect.NewError(connect.CodeInvalidArgument, errors.New("encrypted: the stream is encrypted or DRM-protected"))

// sampleMedia is the media info of an HLS presentation with three variants and two
// audio renditions.
func sampleMedia(manifest string) *downexv1.MediaInfo {
	return &downexv1.MediaInfo{
		ManifestUrl: manifest, Format: "hls",
		Tracks: []*downexv1.MediaTrack{
			{Id: "v0", Kind: "video", Bandwidth: 800_000, Width: 640, Height: 360, FrameRate: 25, Codecs: "avc1.4d401e"},
			{Id: "v1", Kind: "video", Bandwidth: 5_000_000, Width: 1920, Height: 1080, FrameRate: 29.97, Codecs: "avc1.640028"},
			{Id: "v2", Kind: "muxed", Bandwidth: 1_200_000, Width: 960, Height: 540, Codecs: "avc1.4d401f,mp4a.40.2"},
			{Id: "r0", Kind: "audio", Bandwidth: 128_000, Codecs: "mp4a.40.2", Language: "en", Name: "English"},
			{Id: "r1", Kind: "audio", Codecs: "mp4a.40.2", Language: "de", Name: "Deutsch"},
		},
		SelectedVideo: "v1", SelectedAudio: "r0", DurationMs: 5_400_250, FragmentsTotal: 1800, Container: "mp4",
		Title: "Keynote",
	}
}

// sampleSiteMedia is what downexd offers for a video page (docs/SITE-MEDIA-SPEC.md §3.11):
// one MP4 quality per class best first under its standard name, with the site's sizes, a
// muxed one, and the MP4 audio.
func sampleSiteMedia(page string) *downexv1.MediaInfo {
	return &downexv1.MediaInfo{
		Format: "http", Container: "mp4", Origin: downexv1.MediaOrigin_MEDIA_ORIGIN_SITE, Site: "YouTube", PageUrl: page,
		Title: "A talk", DurationMs: 600_000,
		Tracks: []*downexv1.MediaTrack{
			{Id: "137", Kind: "video", Bandwidth: 4_400_000, Width: 1920, Height: 1080, FrameRate: 30, Codecs: "avc1.640028",
				Name: "1080p (Full HD)", Size: 330_000_000, Ext: "mp4"},
			{Id: "399", Kind: "video", Bandwidth: 2_100_000, Width: 1280, Height: 720, FrameRate: 30, Codecs: "av01.0.08M.08",
				Name: "720p (HD)", Size: 157_500_000, SizeApprox: true, Ext: "mp4"},
			{Id: "18", Kind: "muxed", Bandwidth: 600_000, Width: 640, Height: 360, FrameRate: 30, Codecs: "avc1.42001E, mp4a.40.2",
				Name: "360p", Ext: "mp4"},
			{Id: "140", Kind: "audio", Bandwidth: 129_000, Codecs: "mp4a.40.2", Language: "en", Name: "medium",
				Size: 9_700_000, Ext: "m4a"},
		},
		SelectedVideo: "137", SelectedAudio: "140",
	}
}

// isSite reports a probe or add of a web page.
func isSite(origin downexv1.MediaOrigin) bool {
	return origin == downexv1.MediaOrigin_MEDIA_ORIGIN_SITE
}

func (f *fakeDaemon) AddMedia(_ context.Context, req *downexv1.AddMediaRequest) (*downexv1.AddMediaResponse, error) {
	if err := f.record(req); err != nil {
		return nil, err
	}
	if isSite(req.GetOrigin()) {
		f.mu.Lock()
		siteErr := f.siteErr
		f.mu.Unlock()
		if siteErr != nil {
			return nil, siteErr
		}
		media := sampleSiteMedia(req.GetPageUrl())
		if req.GetVideoTrack() != "" {
			media.SelectedVideo = req.GetVideoTrack()
		}
		media.SelectedAudio = req.GetAudioTrack()
		return &downexv1.AddMediaResponse{Download: &downexv1.Download{
			Id: 97, Url: req.GetPageUrl(), Dir: req.GetDir(), Filename: "A talk.mp4", Size: -1, ExpectedSize: 339_700_000,
			State: downexv1.DownloadState_DOWNLOAD_STATE_QUEUED, QueuePosition: 1, Source: req.GetSource(),
			Category: "Video", Kind: downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA, Media: media,
		}}, nil
	}
	if strings.Contains(req.GetManifestUrl(), "drm") {
		return nil, errEncrypted
	}
	media := sampleMedia(req.GetManifestUrl())
	if req.GetVideoTrack() != "" {
		media.SelectedVideo = req.GetVideoTrack()
	}
	if req.GetAudioTrack() != "" {
		media.SelectedAudio = req.GetAudioTrack()
	}
	return &downexv1.AddMediaResponse{Download: &downexv1.Download{
		Id: 98, Url: req.GetManifestUrl(), Dir: req.GetDir(), Filename: "Keynote.mp4", Size: -1,
		State: downexv1.DownloadState_DOWNLOAD_STATE_QUEUED, QueuePosition: 1, Source: req.GetSource(),
		Category: "Video", Kind: downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA, Media: media,
	}}, nil
}

func (f *fakeDaemon) ProbeMedia(_ context.Context, req *downexv1.ProbeMediaRequest) (*downexv1.ProbeMediaResponse, error) {
	if err := f.record(req); err != nil {
		return nil, err
	}
	if isSite(req.GetOrigin()) {
		f.mu.Lock()
		siteErr := f.siteErr
		f.mu.Unlock()
		if siteErr != nil {
			return nil, siteErr
		}
		return &downexv1.ProbeMediaResponse{Media: sampleSiteMedia(req.GetPageUrl())}, nil
	}
	if strings.Contains(req.GetManifestUrl(), "drm") {
		return nil, errEncrypted
	}
	return &downexv1.ProbeMediaResponse{Media: sampleMedia(req.GetManifestUrl())}, nil
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

// InstallTools makes every tool READY at once, as a finished install reports them.
func (f *fakeDaemon) InstallTools(_ context.Context, req *downexv1.InstallToolsRequest) (*downexv1.InstallToolsResponse, error) {
	if err := f.record(req); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.tools == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("downex.v1.DaemonService.InstallTools is not implemented"))
	}
	if !f.tools.GetEnabled() {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("site_media_off: video downloads from web pages are turned off (Settings → Video sites)"))
	}
	for _, t := range f.tools.GetTools() {
		t.State, t.BytesDone, t.BytesTotal, t.Error, t.ErrorCode = downexv1.ToolState_TOOL_STATE_READY, 0, 0, "", ""
	}
	f.tools.Ready = true
	return &downexv1.InstallToolsResponse{Status: proto.CloneOf(f.tools)}, nil
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
	if err := f.record(nil); err != nil {
		return nil, err
	}
	return &downexv1.GetSettingsResponse{Settings: f.settings}, nil
}

func (f *fakeDaemon) UpdateSettings(_ context.Context, req *downexv1.UpdateSettingsRequest) (*downexv1.UpdateSettingsResponse, error) {
	if err := f.record(req); err != nil {
		return nil, err
	}
	return &downexv1.UpdateSettingsResponse{Settings: req.GetSettings()}, nil
}

func (f *fakeDaemon) Status(context.Context) (*downexv1.StatusResponse, error) {
	if err := f.record(nil); err != nil {
		return nil, err
	}
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

// fakeStream is a WatchStream that delivers batches until they run out, then ends
// with end (or, when end is nil, waits for ctx).
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

// fakeHost records what App asks of the window.
type fakeHost struct {
	mu         sync.Mutex
	events     []json.RawMessage
	confirmIDs []json.RawMessage    // payloads of confirmName
	menus      []json.RawMessage    // payloads of menuName
	emitted    chan json.RawMessage // receives each payload when not nil
	confirms   []string             // title + "\n" + message of each question
	answer     bool
	confirmErr error
	picks      []string // starting folders of PickFolder
	picked     string
	filePicks  []string // title + "\n" + starting folder of each PickFile
	pickedFile string
	focused    int
	themes     []themeChoice // SetTheme calls
}

func (h *fakeHost) Emit(name string, payload json.RawMessage) {
	switch name {
	case confirmName:
		h.mu.Lock()
		h.confirmIDs = append(h.confirmIDs, payload)
		h.mu.Unlock()
		return
	case menuName:
		h.mu.Lock()
		h.menus = append(h.menus, payload)
		h.mu.Unlock()
		return
	}
	if name != eventsName {
		panic("unexpected event " + name)
	}
	h.mu.Lock()
	h.events = append(h.events, payload)
	ch := h.emitted
	h.mu.Unlock()
	if ch != nil {
		ch <- payload
	}
}

func (h *fakeHost) Confirm(title, message string) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.confirms = append(h.confirms, title+"\n"+message)
	return h.answer, h.confirmErr
}

func (h *fakeHost) PickFolder(_, dir string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.picks = append(h.picks, dir)
	return h.picked, nil
}

func (h *fakeHost) PickFile(title, dir string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.filePicks = append(h.filePicks, title+"\n"+dir)
	return h.pickedFile, nil
}

func (h *fakeHost) Focus() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.focused++
}

func (h *fakeHost) SetTheme(c themeChoice) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.themes = append(h.themes, c)
}

// themesSet returns the themes SetTheme gave the window, in order.
func (h *fakeHost) themesSet() []themeChoice {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.themes)
}

// confirmations returns the payloads of the confirmation requests sent to the page.
func (h *fakeHost) confirmations() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, len(h.confirmIDs))
	for i, p := range h.confirmIDs {
		out[i] = string(p)
	}
	return out
}

func (h *fakeHost) emittedCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.events)
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

// newTestApp returns an App on d with a fake shell and, when host is not nil, started
// with host (its events bridge then runs until the test ends).
func newTestApp(t *testing.T, d Daemon, host Host) (*App, *fakeShell) {
	t.Helper()
	sh := &fakeShell{}
	a := newApp(d, sh, nil)
	a.minBackoff, a.maxBackoff = time.Millisecond, 4*time.Millisecond
	// Never the user's own theme file.
	a.themePath = filepath.Join(t.TempDir(), "Downex", themeFileName)
	if host != nil {
		a.startup(host)
	}
	t.Cleanup(a.shutdown)
	return a, sh
}

// decodeJSON parses a binding's result into a generic value.
func decodeJSON(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("result is not a JSON object: %v: %s", err, raw)
	}
	return v
}
