package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"downex/internal/client"
	"downex/internal/daemon"
	"downex/internal/ipc"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/rpc/downex/v1/downexv1connect"
	"downex/internal/store"
	"downex/internal/testserver"
	"downex/internal/transport"
)

// daemonServer is a daemon.Manager behind both Connect services on an h2c httptest
// server, as downexd serves them, with a real engine and HTTP transport.
type daemonServer struct {
	srv *httptest.Server
	hc  *http.Client
	dir string // default download directory
	dl  downexv1connect.DownloadServiceClient
}

func newDaemonServer(t *testing.T) *daemonServer {
	t.Helper()
	return newMediaDaemonServer(t, nil)
}

// newMediaDaemonServer is newDaemonServer whose daemon runs media downloads with mj.
func newMediaDaemonServer(t *testing.T, mj daemon.MediaJobs) *daemonServer {
	t.Helper()
	opts := transport.DefaultHTTPOptions()
	opts.Proxy = "direct"
	tf, err := transport.NewHTTPFactory(opts)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	m, err := daemon.New(context.Background(), daemon.Config{
		Store: store.NewMemory(), Transport: tf,
		DownloadsDir: func() (string, error) { return dir, nil },
		Logger:       testLogger(t), Version: "test-version", Endpoint: "test-endpoint",
		// A confirmation window that would show hand-offs, so that they are held.
		OpenConfirmation: func(int64) error { return nil },
		Media:            mj,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(daemon.NewHandler(m, nil))
	var sp http.Protocols
	sp.SetHTTP1(true)
	sp.SetUnencryptedHTTP2(true)
	srv.Config.Protocols = &sp
	srv.Start()
	var cp http.Protocols
	cp.SetUnencryptedHTTP2(true)
	hc := &http.Client{Transport: &http.Transport{Protocols: &cp}}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
		defer cancel()
		if err := m.Shutdown(ctx); err != nil {
			t.Errorf("shutdown: %v", err)
		}
		hc.CloseIdleConnections()
		srv.Close()
	})
	return &daemonServer{srv: srv, hc: hc, dir: dir, dl: downexv1connect.NewDownloadServiceClient(hc, srv.URL)}
}

// env returns a client.Env that reaches d; the endpoint is "up" once up() is true.
func (d *daemonServer) env(up func() bool, start func(exe string, args ...string) error) client.Env {
	return client.Env{
		Endpoint: func() (string, error) { return "test-endpoint", nil },
		Dial: func(context.Context, string) (net.Conn, error) {
			if !up() {
				return nil, fmt.Errorf("test: %w", ipc.ErrNotRunning)
			}
			c, peer := net.Pipe()
			_ = peer.Close()
			return c, nil
		},
		HTTPClient:    func(string) connect.HTTPClient { return d.hc },
		BaseURL:       d.srv.URL,
		DaemonPath:    func() (string, error) { return "/test/downexd", nil },
		StartDetached: start,
		Elevated:      func() (bool, error) { return false, nil },
		LogDir:        func() (string, error) { return "/test/logs", nil },
		StartTimeout:  3 * time.Second,
		PollInterval:  5 * time.Millisecond,
	}
}

// waitCompleted polls the daemon until download id is completed.
func (d *daemonServer) waitCompleted(t *testing.T, id int64) *downexv1.Download {
	t.Helper()
	deadline := time.Now().Add(testTimeout)
	for {
		res, err := d.dl.Get(context.Background(), connect.NewRequest(&downexv1.GetRequest{Id: id}))
		if err != nil {
			t.Fatalf("get %d: %v", id, err)
		}
		dl := res.Msg.GetDownload()
		switch dl.GetState() {
		case downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED:
			return dl
		case downexv1.DownloadState_DOWNLOAD_STATE_FAILED:
			t.Fatalf("download failed: %v", dl)
		}
		if time.Now().After(deadline) {
			t.Fatalf("download did not complete: %v", dl)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func fileSum(t *testing.T, path string) [32]byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}

// TestAddRoundTrip hands a download to a real daemon through the relay and waits for
// the right bytes on disk.
func TestAddRoundTrip(t *testing.T) {
	const size = 3 << 20
	ts := testserver.New(testserver.Options{Size: size, Seed: 7, ETag: `"e1"`})
	t.Cleanup(ts.Close)
	d := newDaemonServer(t)
	be := &daemonBackend{env: d.env(func() bool { return true }, func(string, ...string) error {
		t.Error("the relay started a daemon although one runs")
		return nil
	})}
	s := startSession(t, be)

	s.send(map[string]any{"id": "h", "type": "hello", "protocol": 1, "browser": "chrome", "extVersion": "0.1"})
	if m := s.recv(); m["type"] != "welcome" || m["daemonVersion"] != "test-version" {
		t.Fatalf("hello: %v", m)
	}
	s.send(map[string]any{"id": "a", "type": "add", "url": ts.URL + "/files/data.bin", "method": "GET",
		"headers": [][]string{{"Cookie", "s=1"}, {"User-Agent", "Test/1"}}, "filename": "renamed:data.bin",
		"size": size, "mime": "Application/Octet-Stream; x=y"})
	m := s.recv()
	id, ok := m["downloadId"].(float64)
	// A browser hand-off awaits the user's confirmation (the default), and says so.
	if m["type"] != "added" || !ok || id <= 0 || m["awaiting"] != true {
		t.Fatalf("add: %v", m)
	}
	// It is held with its hints.
	got, err := d.dl.Get(context.Background(), connect.NewRequest(&downexv1.GetRequest{Id: int64(id)}))
	if err != nil {
		t.Fatal(err)
	}
	if g := got.Msg.GetDownload(); g.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_AWAITING_CONFIRMATION ||
		g.GetSource() != downexv1.AddSource_ADD_SOURCE_BROWSER || g.GetExpectedSize() != size ||
		g.GetMime() != "application/octet-stream" || g.GetFilename() != "renamed_data.bin" {
		t.Fatalf("handed-off download = %v", g)
	}
	if _, err := d.dl.Confirm(context.Background(), connect.NewRequest(&downexv1.ConfirmRequest{Id: int64(id), Start: true})); err != nil {
		t.Fatal(err)
	}
	dl := d.waitCompleted(t, int64(id))
	if dl.GetFilename() != "renamed_data.bin" || dl.GetDir() != d.dir {
		t.Fatalf("saved as %q in %q", dl.GetFilename(), dl.GetDir())
	}
	if fileSum(t, filepath.Join(d.dir, dl.GetFilename())) != ts.Sum256() {
		t.Fatal("downloaded file differs from the served content")
	}
	s.send(map[string]any{"id": "s", "type": "status"})
	if m := s.recv(); m["running"] != true || m["active"] != 0.0 {
		t.Fatalf("status: %v", m)
	}

	// A paused POST without a body.
	s.send(map[string]any{"id": "p", "type": "add", "url": ts.URL + "/p", "method": "POST", "startPaused": true})
	if m := s.recv(); m["type"] != "added" {
		t.Fatalf("paused add: %v", m)
	}
	if err := s.closeInput(); err != nil {
		t.Fatal(err)
	}
}

// lateDaemon is a daemon that comes up delay after it is started.
type lateDaemon struct {
	t      *testing.T
	delay  time.Duration
	mu     sync.Mutex
	up     bool
	starts int
}

func (l *lateDaemon) isUp() bool { l.mu.Lock(); defer l.mu.Unlock(); return l.up }

func (l *lateDaemon) start(exe string, args ...string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.starts++
	if exe != "/test/downexd" || len(args) != 0 {
		l.t.Errorf("started %s %q", exe, args)
	}
	time.AfterFunc(l.delay, func() {
		l.mu.Lock()
		l.up = true
		l.mu.Unlock()
	})
	return nil
}

func (l *lateDaemon) startCount() int { l.mu.Lock(); defer l.mu.Unlock(); return l.starts }

// TestAutostartOnce checks that concurrent adds with no daemon running start downexd
// once and all succeed when it answers.
func TestAutostartOnce(t *testing.T) {
	d := newDaemonServer(t)
	ld := &lateDaemon{t: t, delay: 30 * time.Millisecond}
	be := &daemonBackend{env: d.env(ld.isUp, ld.start)}
	s := startSession(t, be)

	// A status does not start the daemon.
	s.send(map[string]any{"id": "s", "type": "status"})
	if m := s.recv(); m["running"] != false {
		t.Fatalf("status: %v", m)
	}
	if n := ld.startCount(); n != 0 {
		t.Fatalf("status started downexd %d times", n)
	}
	const n = 5
	for i := range n {
		s.send(map[string]any{"id": fmt.Sprint(i), "type": "add", "url": fmt.Sprintf("http://127.0.0.1:9/f%d", i), "startPaused": true})
	}
	for range n {
		if m := s.recv(); m["type"] != "added" {
			t.Fatalf("add: %v", m)
		}
	}
	if n := ld.startCount(); n != 1 {
		t.Fatalf("downexd was started %d times", n)
	}
}

// TestHelloStartsTheDaemon checks that a hello starts downexd in the background, so
// that the first capture does not pay for a cold start, and still answers at once.
func TestHelloStartsTheDaemon(t *testing.T) {
	d := newDaemonServer(t)
	ld := &lateDaemon{t: t, delay: 2 * time.Second} // slower than the hello's wait
	be := &daemonBackend{env: d.env(ld.isUp, ld.start)}
	s := startSession(t, be)
	start := time.Now()
	s.send(map[string]any{"id": "h", "type": "hello", "protocol": 1, "browser": "edge"})
	if m := s.recv(); m["type"] != "welcome" || m["daemonVersion"] != "" {
		t.Fatalf("hello: %v", m)
	}
	if el := time.Since(start); el > 1800*time.Millisecond {
		t.Fatalf("welcome after %v, want about %v", el, helloTimeout)
	}
	if n := ld.startCount(); n != 1 {
		t.Fatalf("hello started downexd %d times, want once", n)
	}
	// Once it answers, hello reports its version, and an add does not start another.
	deadline := time.Now().Add(testTimeout)
	for !ld.isUp() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	s.send(map[string]any{"id": "h2", "type": "hello", "protocol": 1, "browser": "edge"})
	if m := s.recv(); m["daemonVersion"] != "test-version" {
		t.Fatalf("hello: %v", m)
	}
	s.send(map[string]any{"id": "a", "type": "add", "url": "http://127.0.0.1:9/f", "startPaused": true})
	if m := s.recv(); m["type"] != "added" {
		t.Fatalf("add: %v", m)
	}
	if err := s.closeInput(); err != nil {
		t.Fatal(err)
	}
	if n := ld.startCount(); n != 1 {
		t.Fatalf("downexd was started %d times", n)
	}
}

// TestLateAddIsDropped checks that an add whose deadline passes while downexd starts
// is never handed to the daemon: the extension has given up and the browser keeps the
// download, so a late add would download the file twice.
func TestLateAddIsDropped(t *testing.T) {
	d := newDaemonServer(t)
	ld := &lateDaemon{t: t, delay: 1500 * time.Millisecond}
	be := &daemonBackend{env: d.env(ld.isUp, ld.start)}
	s := startSession(t, be)
	start := time.Now()
	s.send(map[string]any{"id": "a", "type": "add", "url": "http://127.0.0.1:9/late.zip", "startPaused": true, "deadlineMs": 500})
	m := s.recv()
	if errorCode(m) != codeDaemonUnavailable {
		t.Fatalf("add past its deadline: %v", m)
	}
	if el := time.Since(start); el > 1200*time.Millisecond {
		t.Fatalf("answered after %v, want within the 500 ms deadline", el)
	}
	deadline := time.Now().Add(testTimeout)
	for !ld.isUp() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond) // a late Add would arrive now
	list, err := d.dl.List(context.Background(), connect.NewRequest(&downexv1.ListRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(list.Msg.GetDownloads()); n != 0 {
		t.Fatalf("the daemon has %d downloads after the deadline passed, want 0", n)
	}
	// The daemon started for the dropped add serves the next one.
	s.send(map[string]any{"id": "b", "type": "add", "url": "http://127.0.0.1:9/next.zip", "startPaused": true, "deadlineMs": 3000})
	if m := s.recv(); m["type"] != "added" {
		t.Fatalf("add once downexd runs: %v", m)
	}
	if n := ld.startCount(); n != 1 {
		t.Fatalf("downexd was started %d times", n)
	}
}

// TestAutostartFailure reports a daemon that cannot be started as daemon_unavailable.
func TestAutostartFailure(t *testing.T) {
	d := newDaemonServer(t)
	env := d.env(func() bool { return false }, func(string, ...string) error { return nil })
	env.StartTimeout = 50 * time.Millisecond
	s := startSession(t, &daemonBackend{env: env})
	s.send(map[string]any{"id": "a", "type": "add", "url": "https://example.test/f"})
	m := s.recv()
	if errorCode(m) != codeDaemonUnavailable {
		t.Fatalf("add without a daemon: %v", m)
	}
	t.Logf("message: %v", m["error"])
}

// TestPrefetchNeverStartsTheDaemon: a look-ahead (probeVideo with prefetch, BROWSER-SPEC
// §4) goes only to a downexd that runs: with none running it is daemon_unavailable and
// starts nothing (the user may have quit Downex), while a user's probe starts it.
func TestPrefetchNeverStartsTheDaemon(t *testing.T) {
	d := newDaemonServer(t)
	ld := &lateDaemon{t: t, delay: 30 * time.Millisecond}
	s := startSession(t, &daemonBackend{env: d.env(ld.isUp, ld.start)})
	const page = "https://www.youtube.com/watch?v=aaaaaaaaaaa"
	s.send(map[string]any{"id": "ahead", "type": "probeVideo", "pageUrl": page, "prefetch": true, "deadlineMs": 28000})
	if m := s.recv(); errorCode(m) != codeDaemonUnavailable {
		t.Fatalf("look-ahead without a daemon: %v", m)
	}
	if n := ld.startCount(); n != 0 {
		t.Fatalf("a look-ahead started downexd %d times", n)
	}
	s.send(map[string]any{"id": "user", "type": "probeVideo", "pageUrl": page, "deadlineMs": 28000})
	if m := s.recv(); errorCode(m) == codeDaemonUnavailable {
		t.Fatalf("user's probe: %v", m)
	}
	if n := ld.startCount(); n != 1 {
		t.Fatalf("the user's probe started downexd %d times, want 1", n)
	}
}
