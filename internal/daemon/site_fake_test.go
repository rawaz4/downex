package daemon

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"downex/internal/engine"
	"downex/internal/media"
	"downex/internal/model"
	"downex/internal/rpc/downex/v1/downexv1connect"
	"downex/internal/sitemedia"
	"downex/internal/store"
	"downex/internal/transport"
)

const testPage = "https://www.video.test/watch?v=abcdefghijk"

// sampleSiteTracks are the tracks of fakeSite's default probe, best first: three MP4
// video qualities, one per quality class under its standard name (an H.264 1080p, an
// AV1 720p, a muxed 360p), and two MP4 audio tracks.
func sampleSiteTracks() []media.Track {
	return []media.Track{
		{ID: "137", Kind: media.KindVideo, Bandwidth: 4_000_000, Width: 1920, Height: 1080, FrameRate: 30, Codecs: "avc1.640028",
			Name: "1080p (Full HD)", Key: "s1-v137", Size: 412_000_000, Ext: "mp4"},
		{ID: "399", Kind: media.KindVideo, Bandwidth: 2_500_000, Width: 1280, Height: 720, FrameRate: 30, Codecs: "av01.0.08M.08",
			Name: "720p (HD)", Key: "s1-v399", Size: 300_000_000, SizeApprox: true, Ext: "mp4"},
		{ID: "22", Kind: media.KindMuxed, Bandwidth: 1_500_000, Width: 640, Height: 360, FrameRate: 30,
			Codecs: "avc1.64001F, mp4a.40.2", Name: "360p", Key: "s1-m22", Size: 90_000_000, Ext: "mp4"},
		{ID: "140", Kind: media.KindAudio, Bandwidth: 130_000, Codecs: "mp4a.40.2", Language: "en", Name: "original",
			Key: "s1-a140", Size: 4_000_000, Ext: "m4a"},
		{ID: "140-drc", Kind: media.KindAudio, Bandwidth: 130_000, Codecs: "mp4a.40.2", Language: "en", Name: "DRC",
			Key: "s1-a140d", Size: 4_100_000, SizeApprox: true, Ext: "m4a"},
	}
}

// fakeSite is a SiteMedia that serves a configured probe and downloads like fakeMedia
// (its fragments, gate, failure and checkpoints), reporting BytesTotal with every
// progress event. Probe resolves the requested tracks like the real adapter; with
// probeWait it waits for a token (or the end of ctx, then ErrSiteResolving). Run waits
// for startGate (when set) before the job starts. Every call is recorded.
type fakeSite struct {
	job *fakeMedia // the download part

	mu         sync.Mutex
	probe      SiteProbe
	probeErr   error
	probeWait  chan struct{}
	startGate  chan struct{}
	bytesTotal int64
	runErr     error // returned by Run at once (after startGate)
	probes     []SiteSelection
	runs       []fakeSiteRun
}

type fakeSiteRun struct {
	opts   MediaRunOptions
	sel    SiteSelection
	resume *model.MediaCheckpoint
}

func newFakeSite(fragments int) *fakeSite {
	return &fakeSite{
		job: newFakeMedia(fragments),
		probe: SiteProbe{
			MediaProbe: MediaProbe{Format: "http", Tracks: sampleSiteTracks(), DefaultVideo: "137", DefaultAudio: "140",
				Duration: 212 * time.Second, Container: "mp4"},
			Site: "YouTube", Title: "A video title",
		},
		bytesTotal: 416_000_000,
	}
}

func (f *fakeSite) set(fn func(f *fakeSite)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeSite) calls() (probes []SiteSelection, runs []fakeSiteRun) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.probes), slices.Clone(f.runs)
}

func (f *fakeSite) Probe(ctx context.Context, tf transport.Factory, sel SiteSelection) (*SiteProbe, error) {
	f.mu.Lock()
	f.probes = append(f.probes, sel)
	wait, probeErr, p := f.probeWait, f.probeErr, f.probe
	f.mu.Unlock()
	if tf == nil {
		return nil, fmt.Errorf("fake site probe: no transport factory")
	}
	if wait != nil {
		select {
		case <-wait:
		case <-ctx.Done():
			return nil, fmt.Errorf("%w: %w", sitemedia.ErrSiteResolving, ctx.Err())
		}
	}
	if probeErr != nil {
		return nil, probeErr
	}
	p.Tracks = slices.Clone(p.Tracks)
	if sel.VideoTrack != "" {
		p.DefaultVideo = sel.VideoTrack
	}
	if sel.AudioTrack != "" {
		p.DefaultAudio = sel.AudioTrack
	}
	return &p, nil
}

func (f *fakeSite) Run(ctx context.Context, opts MediaRunOptions, sel SiteSelection, resume *model.MediaCheckpoint,
	onProgress func(MediaProgress), onCheckpoint func(model.MediaCheckpoint) error) (string, error) {
	f.mu.Lock()
	f.runs = append(f.runs, fakeSiteRun{opts: opts, sel: sel, resume: resume.Clone()})
	gate, total, runErr := f.startGate, f.bytesTotal, f.runErr
	f.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if runErr != nil {
		return "", runErr
	}
	return f.job.Run(ctx, opts, MediaSelection{VideoTrack: sel.VideoTrack, AudioTrack: sel.AudioTrack}, resume,
		func(p MediaProgress) {
			p.BytesTotal = total
			onProgress(p)
		}, onCheckpoint)
}

// fakeTools is a Tools whose Ensure answers at once (ensureErr, nil when ready) or, while
// installing, waits for ready (or the end of ctx, then ErrToolsInstalling with 34%).
type fakeTools struct {
	mu         sync.Mutex
	snap       ToolsSnapshot
	installing bool
	ready      chan struct{} // closed when an install that Ensure waits for ends
	ensureErr  error
	installErr error         // Install fails with it
	installFor chan struct{} // non-nil: Install waits for it (or the end of ctx)
	configs    []toolsConfig
	installs   []bool
	ensures    int
	checks     int
	checkErr   error
	subs       map[int]func()
	nextSub    int
}

type toolsConfig struct {
	enabled, autoUpdate bool
	channel             string
}

func newFakeTools() *fakeTools {
	return &fakeTools{
		snap: ToolsSnapshot{Enabled: true, Ready: true, Tools: []ToolSnapshot{
			{Name: "yt-dlp", State: ToolStateReady, Version: "2026.10.10.233141", Path: `C:\tools\yt-dlp\2026.10.10.233141\yt-dlp.exe`,
				Managed: true, LatestVersion: "2026.10.10.233141", CheckedAt: time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC),
				BytesTotal: -1, Verified: "openpgp+sha256"},
			{Name: "deno", State: ToolStateReady, Version: "2.5.4", Path: `C:\tools\deno\2.5.4\deno.exe`, Managed: true,
				BytesTotal: -1, Verified: "sha256+authenticode"},
		}},
		ready: make(chan struct{}),
		subs:  make(map[int]func()),
	}
}

func (f *fakeTools) Configure(enabled, autoUpdate bool, channel string) {
	f.mu.Lock()
	f.configs = append(f.configs, toolsConfig{enabled, autoUpdate, channel})
	f.snap.Enabled = enabled
	f.mu.Unlock()
	f.changed()
}

func (f *fakeTools) Status() ToolsSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.snap
	s.Tools = slices.Clone(s.Tools)
	return s
}

func (f *fakeTools) Ensure(ctx context.Context) error {
	f.mu.Lock()
	f.ensures++
	err, installing, ready := f.ensureErr, f.installing, f.ready
	f.mu.Unlock()
	if err != nil {
		return err
	}
	if !installing {
		return nil
	}
	select {
	case <-ready:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("%w (34%%)", sitemedia.ErrToolsInstalling)
	}
}

func (f *fakeTools) Install(ctx context.Context, update bool) (ToolsSnapshot, error) {
	f.mu.Lock()
	f.installs = append(f.installs, update)
	err, wait := f.installErr, f.installFor
	f.mu.Unlock()
	if wait != nil {
		select {
		case <-wait:
		case <-ctx.Done():
			return f.Status(), fmt.Errorf("%w (34%%)", sitemedia.ErrToolsInstalling)
		}
	}
	if err != nil {
		return ToolsSnapshot{}, err
	}
	return f.Status(), nil
}

func (f *fakeTools) CheckUpdate(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks++
	return f.checkErr
}

func (f *fakeTools) Subscribe(fn func()) func() {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.nextSub
	f.nextSub++
	f.subs[id] = fn
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		delete(f.subs, id)
	}
}

// changed calls the subscribers, as a status change does.
func (f *fakeTools) changed() {
	f.mu.Lock()
	fns := make([]func(), 0, len(f.subs))
	for _, fn := range f.subs {
		fns = append(fns, fn)
	}
	f.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

// setInstalling makes Ensure wait (installing: the status shows yt-dlp being installed)
// or answer at once (finished: the install that Ensure waits for ends).
func (f *fakeTools) setInstalling(installing bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.installing && !installing {
		close(f.ready)
		f.ready = make(chan struct{})
	}
	f.installing = installing
	f.snap.Ready = !installing
	f.snap.Tools = slices.Clone(f.snap.Tools)
	for i := range f.snap.Tools {
		if f.snap.Tools[i].Name == "yt-dlp" {
			f.snap.Tools[i].State = ToolStateReady
			if installing {
				f.snap.Tools[i].State = ToolStateInstalling
			}
		}
	}
}

// setMissing makes the status show yt-dlp missing (not ready, nothing installing), or
// ready again.
func (f *fakeTools) setMissing(missing bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snap.Ready = !missing
	f.snap.Tools = slices.Clone(f.snap.Tools)
	for i := range f.snap.Tools {
		if f.snap.Tools[i].Name == "yt-dlp" {
			f.snap.Tools[i].State = ToolStateReady
			if missing {
				f.snap.Tools[i].State = ToolStateMissing
			}
		}
	}
}

// setDiscovering makes the status show copies not looked for yet since the start (not
// ready, Discovering), or ready again.
func (f *fakeTools) setDiscovering(discovering bool) {
	f.mu.Lock()
	f.snap.Discovering = discovering
	f.mu.Unlock()
	f.setMissing(discovering)
}

func (f *fakeTools) ensureCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ensures
}

// setEnsureErr makes Ensure fail with err (nil: answer normally).
func (f *fakeTools) setEnsureErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureErr = err
}

func (f *fakeTools) recorded() (configs []toolsConfig, installs []bool, checks int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.configs), slices.Clone(f.installs), f.checks
}

// siteHarnessOptions are what newSiteHarness adds to harnessOptions.
type siteHarnessOptions struct {
	site      SiteMedia
	tools     Tools
	hostRules engine.HostRules // Config.Engine.HostRules
}

// newSiteHarness is newHarness with Config.Site, Config.Tools and the engine's host
// rules (o.st, o.dir, o.settingsPath, o.media, o.network, o.nobodyAsks and o.settings
// apply as there).
func newSiteHarness(t *testing.T, o harnessOptions, so siteHarnessOptions) *harness {
	t.Helper()
	if o.st == nil {
		o.st = store.NewMemory()
	}
	if o.dir == "" {
		o.dir = t.TempDir()
	}
	opts := transport.DefaultHTTPOptions()
	opts.Proxy = "direct"
	tf, err := transport.NewHTTPFactory(opts)
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(context.Background(), Config{
		Store: o.st, Transport: tf, SettingsPath: o.settingsPath,
		DownloadsDir: func() (string, error) { return o.dir, nil },
		Logger:       testLogger(t), Version: "test-version", Endpoint: "test-endpoint",
		Media: o.media, Network: o.network, Site: so.site, Tools: so.tools,
		Engine: engine.Options{HostRules: so.hostRules},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !o.nobodyAsks {
		ch, cancel := m.Subscribe()
		m.ShowNotices(ch, true)
		t.Cleanup(cancel)
	}
	h := &harness{t: t, st: o.st, m: m, dir: o.dir, settingsPath: o.settingsPath, shutdowns: make(chan struct{}, 4)}
	srv := httptest.NewUnstartedServer(NewHandler(m, func() { h.shutdowns <- struct{}{} }))
	var sp http.Protocols
	sp.SetHTTP1(true)
	sp.SetUnencryptedHTTP2(true)
	srv.Config.Protocols = &sp
	srv.Start()
	h.srv = srv
	var cp http.Protocols
	cp.SetUnencryptedHTTP2(true)
	client := &http.Client{Transport: &http.Transport{Protocols: &cp}}
	h.dl = downexv1connect.NewDownloadServiceClient(client, srv.URL)
	h.dm = downexv1connect.NewDaemonServiceClient(client, srv.URL)
	h.dq = downexv1connect.NewQueueServiceClient(client, srv.URL)
	if o.settings != nil {
		s := protoSettings(m.Settings())
		o.settings(s)
		if _, err := m.UpdateSettings(context.Background(), s); err != nil {
			t.Fatalf("settings: %v", err)
		}
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
		defer cancel()
		if err := m.Shutdown(ctx); err != nil {
			t.Errorf("shutdown: %v", err)
		}
		client.CloseIdleConnections()
		srv.Close()
	})
	return h
}
