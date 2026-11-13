package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"downex/internal/ipc"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/rpc/downex/v1/downexv1connect"
	"downex/internal/testserver"
	"downex/internal/tray"
)

// TestMain lets tests run the real main in a child process (see daemonCommand). No
// daemon of the tests touches the desktop of the user running them: no real
// notification-area icon or notification (tests that exercise the tray swap in a
// fake), no sign-in entry and no window started.
func TestMain(m *testing.M) {
	newTrayIcon = func(tray.Options) (trayIcon, error) { return nil, tray.ErrNotSupported }
	setLaunchAtLogin = func(bool, string, ...string) error { return nil }
	startApp = func(exe string, _ ...string) error { return errors.New("tests start no " + exe) }
	if args := os.Getenv("DOWNEXD_TEST_MAIN"); args != "" {
		os.Args = append([]string{"downexd"}, strings.Split(args, "\n")...)
		main()
		return
	}
	os.Exit(m.Run())
}

// testEndpoint returns a private endpoint for one test.
func testEndpoint(t *testing.T) string {
	t.Helper()
	switch runtime.GOOS {
	case "windows":
		b := make([]byte, 8)
		_, _ = rand.Read(b)
		return `\\.\pipe\downex-test-` + hex.EncodeToString(b)
	case "linux", "darwin", "freebsd":
		// Not below t.TempDir(): on macOS $TMPDIR and the test's name would make the
		// socket path longer than sun_path allows (104 bytes with the NUL).
		dir, err := os.MkdirTemp("", "dx") // mode 0700, as ipc requires
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		return filepath.Join(dir, "rpc.sock")
	}
	t.Skip("no local endpoint on " + runtime.GOOS)
	return ""
}

// syncBuffer is a bytes.Buffer safe for the daemon's goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type daemonRun struct {
	cancel context.CancelFunc
	done   chan int
	stdout *syncBuffer
	stderr *syncBuffer
}

func startDaemon(t *testing.T, args []string) *daemonRun {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &daemonRun{cancel: cancel, done: make(chan int, 1), stdout: &syncBuffer{}, stderr: &syncBuffer{}}
	go func() { r.done <- run(ctx, args, r.stdout, r.stderr) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-r.done:
		case <-time.After(30 * time.Second):
			t.Error("daemon did not stop")
		}
	})
	return r
}

func (r *daemonRun) wait(t *testing.T) int {
	t.Helper()
	select {
	case code := <-r.done:
		r.done <- code // keep it for the cleanup
		return code
	case <-time.After(30 * time.Second):
		t.Fatalf("daemon did not exit; stderr: %s", r.stderr.String())
		return -1
	}
}

type clients struct {
	dl downexv1connect.DownloadServiceClient
	dm downexv1connect.DaemonServiceClient
}

func dial(t *testing.T, endpoint string) clients {
	hc := ipc.NewHTTPClient(endpoint)
	t.Cleanup(hc.CloseIdleConnections)
	return clients{
		dl: downexv1connect.NewDownloadServiceClient(hc, ipc.BaseURL),
		dm: downexv1connect.NewDaemonServiceClient(hc, ipc.BaseURL),
	}
}

// waitReady polls Status until the daemon answers.
func waitReady(t *testing.T, c clients, r *daemonRun) *downexv1.StatusResponse {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		res, err := c.dm.Status(context.Background(), connect.NewRequest(&downexv1.StatusRequest{}))
		if err == nil {
			return res.Msg
		}
		select {
		case code := <-r.done:
			t.Fatalf("daemon exited with %d: %s", code, r.stderr.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon not ready: %v; stderr: %s", err, r.stderr.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestDaemonEndToEnd(t *testing.T) {
	const size, seed = 2<<20 + 7, 99
	ts := testserver.New(testserver.Options{Size: size, Seed: seed, ETag: `"e2e"`})
	t.Cleanup(ts.Close)
	endpoint := testEndpoint(t)
	dir := t.TempDir()
	logDir := filepath.Join(dir, "logs")
	args := []string{"--db", filepath.Join(dir, "state", "downex.db"), "--endpoint", endpoint,
		"--log-dir", logDir, "--log-level", "debug"}

	d1 := startDaemon(t, args)
	c := dial(t, endpoint)
	st := waitReady(t, c, d1)
	if st.GetPid() != int32(os.Getpid()) || st.GetVersion() != version || st.GetEndpoint() != endpoint {
		t.Fatalf("status = %v", st)
	}

	// A second instance finds the first and exits 0.
	var out, errOut bytes.Buffer
	if code := run(t.Context(), args, &out, &errOut); code != exitOK || !strings.Contains(out.String(), "already running") {
		t.Fatalf("second instance: exit %d, stdout %q, stderr %q", code, out.String(), errOut.String())
	}

	ctx := t.Context()
	res, err := c.dl.Add(ctx, connect.NewRequest(&downexv1.AddRequest{
		Url: ts.URL + "/e2e.bin?token=QUERYSECRET", Dir: filepath.Join(dir, "out"),
		Headers: []*downexv1.Header{{Name: "Cookie", Value: "session=TOPSECRET"}, {Name: "Authorization", Value: "Bearer HIDDEN"}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	id := res.Msg.GetDownload().GetId()
	var done *downexv1.Download
	deadline := time.Now().Add(20 * time.Second)
	for done == nil {
		g, err := c.dl.Get(ctx, connect.NewRequest(&downexv1.GetRequest{Id: id}))
		if err != nil {
			t.Fatal(err)
		}
		switch d := g.Msg.GetDownload(); d.GetState() {
		case downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED:
			done = d
		case downexv1.DownloadState_DOWNLOAD_STATE_FAILED:
			t.Fatalf("download failed: %s", d.GetError())
		}
		if time.Now().After(deadline) {
			t.Fatal("download did not complete")
		}
		time.Sleep(50 * time.Millisecond)
	}
	data, err := os.ReadFile(filepath.Join(done.GetDir(), done.GetFilename()))
	if err != nil || sha256.Sum256(data) != testserver.Sum256(seed, size) {
		t.Fatalf("downloaded file is wrong: %v", err)
	}

	if _, err := c.dm.Shutdown(ctx, connect.NewRequest(&downexv1.ShutdownRequest{})); err != nil {
		t.Fatal(err)
	}
	if code := d1.wait(t); code != exitOK {
		t.Fatalf("daemon exited with %d: %s", code, d1.stderr.String())
	}

	logs, err := os.ReadFile(filepath.Join(logDir, logFileName))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"msg":"downexd started"`, `"msg":"download completed"`, `"msg":"stopping on request"`, `"msg":"downexd stopped"`} {
		if !bytes.Contains(logs, []byte(want)) {
			t.Errorf("log lacks %s", want)
		}
	}
	for _, secret := range []string{"TOPSECRET", "HIDDEN", "QUERYSECRET"} {
		if bytes.Contains(logs, []byte(secret)) {
			t.Errorf("log contains %s:\n%s", secret, logs)
		}
	}

	// The next start finds the download in the database; cancelling the context
	// (what SIGINT/SIGTERM do) stops it cleanly.
	d2 := startDaemon(t, args)
	c2 := dial(t, endpoint)
	waitReady(t, c2, d2)
	list, err := c2.dl.List(ctx, connect.NewRequest(&downexv1.ListRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if ds := list.Msg.GetDownloads(); len(ds) != 1 || ds[0].GetId() != id || ds[0].GetState() != downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED {
		t.Fatalf("list after restart = %v", ds)
	}
	d2.cancel()
	if code := d2.wait(t); code != exitOK {
		t.Fatalf("daemon exited with %d after cancel: %s", code, d2.stderr.String())
	}
}
