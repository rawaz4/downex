package daemon

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"downex/internal/engine"
	"downex/internal/model"
	"downex/internal/polite"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/rpc/downex/v1/downexv1connect"
	"downex/internal/store"
	"downex/internal/testserver"
	"downex/internal/transport"
)

const waitTimeout = 20 * time.Second

// harness runs a Manager behind both Connect services on an h2c httptest server,
// with a real engine and HTTP transport.
type harness struct {
	t            *testing.T
	st           store.Store
	m            *Manager
	srv          *httptest.Server
	dl           downexv1connect.DownloadServiceClient
	dm           downexv1connect.DaemonServiceClient
	dq           downexv1connect.QueueServiceClient
	dir          string // the default download directory
	settingsPath string
	shutdowns    chan struct{} // one value per onShutdown call
}

type harnessOptions struct {
	st           store.Store // reuse a store (recovery tests); nil = a new memory store
	dir          string      // default download directory; "" = new temp dir
	settingsPath string      // "" = in-memory settings
	lockDir      string
	// M4.5 hooks (Config.LaunchAtLogin, Config.OpenConfirmation); nil = none.
	launchAtLogin    func(bool) error
	openConfirmation func(int64) error
	// settings, when non-nil, replace the defaults right after start (UpdateSettings).
	settings func(s *downexv1.Settings)
	// nobodyAsks leaves the daemon without a way to ask the user to confirm a browser
	// hand-off. Otherwise, without openConfirmation, the harness subscribes to the
	// notices as a subscriber that shows them (the tray does), so that browser
	// hand-offs await confirmation.
	nobodyAsks bool
	// media runs media downloads (Config.Media); nil = none.
	media MediaJobs
	// openProgress is Config.OpenProgress; nil = none.
	openProgress func(int64) error
	// showBrowserAdd is Config.ShowBrowserAdd; nil = none.
	showBrowserAdd func(int64, AddWindow) error
	// setConns replaces the engine's SetMaxConnections; nil keeps the engine's.
	setConns func(model.DownloadID, int) error
	// now is Config.Now (the clock that times runs); nil = time.Now.
	now func() time.Time
	// M6 queues: Config.Clock, Location, Power, Quit, Wake, KeepAwake (nil = defaults /
	// none).
	clock    engine.Clock
	location *time.Location
	power    Power
	quit     func()
	wake     WakeTimer
	awake    KeepAwake
	// M6 network and post-processing: Config.Network, Config.PostProcess (nil = none /
	// the default).
	network *NetworkRuntime
	post    *PostProcessor
	// log is Config.Logger for a test that reads the daemon's log; nil = testLogger.
	log *slog.Logger
	// politeProber is Config.PoliteProber, with the harness's transport peers and
	// loopback anchors (the test servers listen there); nil = no Polite mode.
	politeProber polite.Prober
}

func newHarness(t *testing.T, o harnessOptions) *harness {
	t.Helper()
	logger := o.log
	if logger == nil {
		logger = testLogger(t)
	}
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
		Store: o.st, Transport: tf, SettingsPath: o.settingsPath, LockDir: o.lockDir,
		DownloadsDir: func() (string, error) { return o.dir, nil },
		Logger:       logger, Version: "test-version", Endpoint: "test-endpoint",
		LaunchAtLogin: o.launchAtLogin, OpenConfirmation: o.openConfirmation, Media: o.media,
		OpenProgress: o.openProgress, ShowBrowserAdd: o.showBrowserAdd, Now: o.now,
		Clock: o.clock, Location: o.location, Power: o.power, Quit: o.quit, Wake: o.wake, KeepAwake: o.awake,
		Network: o.network, PostProcess: o.post,
		PoliteProber: o.politeProber, Peers: politePeers(tf, o.politeProber), PoliteLoopback: o.politeProber != nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	if o.setConns != nil {
		m.setConns = o.setConns // before any download runs
	}
	if o.openConfirmation == nil && !o.nobodyAsks {
		// A subscriber that shows the notices, as the tray does: it never reads them
		// (the hub drops what does not fit).
		ch, cancel := m.Subscribe()
		m.ShowNotices(ch, true)
		t.Cleanup(cancel)
	}
	h := &harness{t: t, st: o.st, m: m, dir: o.dir, settingsPath: o.settingsPath, shutdowns: make(chan struct{}, 4)}
	srv := httptest.NewUnstartedServer(NewHandler(m, func() { h.shutdowns <- struct{}{} }))
	// The daemon's endpoint speaks HTTP/2 cleartext (and HTTP/1.1 for debugging).
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

// politePeers is the factory's peer table when the harness has a prober.
func politePeers(tf *transport.HTTPFactory, p polite.Prober) PeerSource {
	if p == nil {
		return nil
	}
	return tf.Peers()
}

// testLogger logs through t until the test ends; later records are dropped.
func testLogger(t *testing.T) *slog.Logger {
	w := &testWriter{t: t}
	t.Cleanup(w.stop) // registered first, so it runs after every other cleanup
	level := slog.LevelWarn
	if os.Getenv("DOWNEX_TEST_DEBUG") != "" {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}))
}

type testWriter struct {
	mu   sync.Mutex
	t    *testing.T
	done bool
}

func (w *testWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.done {
		w.t.Log(strings.TrimRight(string(p), "\n"))
	}
	return len(p), nil
}

func (w *testWriter) stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.done = true
}

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	t.Cleanup(cancel)
	return ctx
}

func (h *harness) add(req *downexv1.AddRequest) *downexv1.Download {
	h.t.Helper()
	res, err := h.dl.Add(ctxT(h.t), connect.NewRequest(req))
	if err != nil {
		h.t.Fatalf("add: %v", err)
	}
	return res.Msg.GetDownload()
}

func (h *harness) get(id int64) *downexv1.Download {
	h.t.Helper()
	res, err := h.dl.Get(ctxT(h.t), connect.NewRequest(&downexv1.GetRequest{Id: id}))
	if err != nil {
		h.t.Fatalf("get %d: %v", id, err)
	}
	return res.Msg.GetDownload()
}

func (h *harness) pause(id int64) *downexv1.Download {
	h.t.Helper()
	res, err := h.dl.Pause(ctxT(h.t), connect.NewRequest(&downexv1.PauseRequest{Id: id}))
	if err != nil {
		h.t.Fatalf("pause %d: %v", id, err)
	}
	return res.Msg.GetDownload()
}

func (h *harness) resume(req *downexv1.ResumeRequest) *downexv1.Download {
	h.t.Helper()
	res, err := h.dl.Resume(ctxT(h.t), connect.NewRequest(req))
	if err != nil {
		h.t.Fatalf("resume %d: %v", req.GetId(), err)
	}
	return res.Msg.GetDownload()
}

func (h *harness) list() []*downexv1.Download {
	h.t.Helper()
	res, err := h.dl.List(ctxT(h.t), connect.NewRequest(&downexv1.ListRequest{}))
	if err != nil {
		h.t.Fatalf("list: %v", err)
	}
	return res.Msg.GetDownloads()
}

func (h *harness) status() *downexv1.StatusResponse {
	h.t.Helper()
	res, err := h.dm.Status(ctxT(h.t), connect.NewRequest(&downexv1.StatusRequest{}))
	if err != nil {
		h.t.Fatalf("status: %v", err)
	}
	return res.Msg
}

// waitFor polls download id until cond holds.
func (h *harness) waitFor(id int64, what string, cond func(*downexv1.Download) bool) *downexv1.Download {
	h.t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for {
		d := h.get(id)
		if cond(d) {
			return d
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("download %d: timed out waiting for %s; last state %v received %d error %q (%s)",
				id, what, d.GetState(), d.GetReceived(), d.GetError(), d.GetErrorCode())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitUntil polls cond.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (h *harness) waitState(id int64, want downexv1.DownloadState) *downexv1.Download {
	h.t.Helper()
	return h.waitFor(id, want.String(), func(d *downexv1.Download) bool { return d.GetState() == want })
}

func (h *harness) waitCompleted(id int64) *downexv1.Download {
	h.t.Helper()
	return h.waitFor(id, "completion", func(d *downexv1.Download) bool {
		if d.GetState() == downexv1.DownloadState_DOWNLOAD_STATE_FAILED {
			h.t.Fatalf("download %d failed: %s (%s)", id, d.GetError(), d.GetErrorCode())
		}
		return d.GetState() == downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED
	})
}

// waitReceived waits until download id is downloading with at least n bytes.
func (h *harness) waitReceived(id, n int64) *downexv1.Download {
	h.t.Helper()
	return h.waitFor(id, fmt.Sprintf("%d bytes", n), func(d *downexv1.Download) bool {
		return d.GetState() == downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING && d.GetReceived() >= n
	})
}

// checkFile verifies that the completed download d holds the server's content.
func checkFile(t *testing.T, d *downexv1.Download, seed uint64, size int64) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(d.GetDir(), d.GetFilename()))
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(data)) != size {
		t.Fatalf("%s has %d bytes, want %d", d.GetFilename(), len(data), size)
	}
	if sha256.Sum256(data) != testserver.Sum256(seed, size) {
		t.Fatalf("%s does not match the served content", d.GetFilename())
	}
}

func isRunning(d *downexv1.Download) bool {
	s := d.GetState()
	return s == downexv1.DownloadState_DOWNLOAD_STATE_PROBING || s == downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING
}

func connectCode(err error) connect.Code {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce.Code()
	}
	return connect.CodeUnknown
}

// storedState reads a record straight from the store.
func storedState(t *testing.T, st store.Store, id int64) model.Download {
	t.Helper()
	d, err := st.GetDownload(context.Background(), model.DownloadID(id))
	if err != nil {
		t.Fatal(err)
	}
	return *d
}

// serve starts a test server and closes it at the end of the test (after the
// harness's cleanup when created first).
func serve(t *testing.T, o testserver.Options) *testserver.Server {
	ts := testserver.New(o)
	t.Cleanup(ts.Close)
	return ts
}
