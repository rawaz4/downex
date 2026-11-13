package client

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
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"downex/internal/ipc"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/rpc/downex/v1/downexv1connect"
)

func TestFindDaemon(t *testing.T) {
	noEnv := func(string) string { return "" }
	if p, err := FindDaemon(func(k string) string {
		if k == EnvDaemon {
			return "/custom/downexd"
		}
		return ""
	}, nil); err != nil || p != "/custom/downexd" {
		t.Errorf("with %s: %q %v", EnvDaemon, p, err)
	}

	dir := t.TempDir()
	exe := filepath.Join(dir, "downex-cli")
	self := func() (string, error) { return exe, nil }
	if _, err := FindDaemon(noEnv, self); err == nil || !strings.Contains(err.Error(), EnvDaemon) ||
		!strings.Contains(err.Error(), "next to downex-cli") {
		t.Errorf("missing daemon: %v, want a hint about %s", err, EnvDaemon)
	}
	want := filepath.Join(dir, DaemonName())
	if err := os.WriteFile(want, nil, 0o700); err != nil {
		t.Fatal(err)
	}
	if p, err := FindDaemon(noEnv, self); err != nil || p != want {
		t.Errorf("next to the executable: %q %v, want %q", p, err, want)
	}
	if _, err := FindDaemon(noEnv, func() (string, error) { return "", errors.New("no exe") }); err == nil {
		t.Error("executable error ignored")
	}
}

func TestDaemonName(t *testing.T) {
	want := "downexd"
	if runtime.GOOS == "windows" {
		want = "downexd.exe"
	}
	if DaemonName() != want {
		t.Errorf("DaemonName() = %q, want %q", DaemonName(), want)
	}
}

// statusServer answers Status on an httptest server.
type statusServer struct {
	downexv1connect.UnimplementedDaemonServiceHandler
}

func (statusServer) Status(context.Context, *connect.Request[downexv1.StatusRequest]) (*connect.Response[downexv1.StatusResponse], error) {
	return connect.NewResponse(&downexv1.StatusResponse{Pid: 99, Version: "t"}), nil
}

// fakeEnv is an Env whose endpoint is up once a start was requested (after `after`
// further dials), served by an httptest server.
type fakeEnv struct {
	mu      sync.Mutex
	up      bool
	starts  [][]string
	elev    bool
	pending int
}

func (f *fakeEnv) env(t *testing.T) Env {
	mux := http.NewServeMux()
	mux.Handle(downexv1connect.NewDaemonServiceHandler(statusServer{}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return Env{
		Endpoint: func() (string, error) { return "ep", nil },
		Dial: func(context.Context, string) (net.Conn, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if !f.up {
				if len(f.starts) > 0 {
					f.pending++
					f.up = f.pending > 2
				}
				return nil, fmt.Errorf("fake: %w", ipc.ErrNotRunning)
			}
			c, peer := net.Pipe()
			_ = peer.Close()
			return c, nil
		},
		HTTPClient: func(string) connect.HTTPClient { return srv.Client() },
		BaseURL:    srv.URL,
		DaemonPath: func() (string, error) { return "/x/downexd", nil },
		StartDetached: func(exe string, args ...string) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.starts = append(f.starts, append([]string{exe}, args...))
			return nil
		},
		Elevated:     func() (bool, error) { return f.elev, nil },
		LogDir:       func() (string, error) { return "/x/logs", nil },
		StartTimeout: 2 * time.Second,
		PollInterval: time.Millisecond,
	}
}

func TestConnectStartsDaemon(t *testing.T) {
	f := &fakeEnv{}
	env := f.env(t)
	ctx := context.Background()
	c, st, err := env.Connect(ctx, "ep", "--db", "/d")
	if err != nil || st.GetPid() != 99 || c.Endpoint != "ep" {
		t.Fatalf("Connect = %v %v %v", c, st, err)
	}
	if len(f.starts) != 1 || strings.Join(f.starts[0], " ") != "/x/downexd --db /d" {
		t.Fatalf("starts = %q", f.starts)
	}
	// Running now: no second start.
	if _, st, err := env.Connect(ctx, "ep"); err != nil || st != nil || len(f.starts) != 1 {
		t.Fatalf("second Connect: %v %v, starts %q", st, err, f.starts)
	}
}

func TestStartRefusals(t *testing.T) {
	f := &fakeEnv{elev: true}
	env := f.env(t)
	if _, _, err := env.Connect(context.Background(), "ep"); !errors.Is(err, ErrElevatedStart) || len(f.starts) != 0 {
		t.Fatalf("elevated: %v, starts %q", err, f.starts)
	}

	f = &fakeEnv{}
	env = f.env(t)
	env.StartTimeout = 20 * time.Millisecond
	env.Dial = func(context.Context, string) (net.Conn, error) { return nil, ipc.ErrNotRunning }
	_, _, err := env.Connect(context.Background(), "ep")
	if !errors.Is(err, ErrNotReady) || !strings.Contains(err.Error(), "/x/logs") {
		t.Fatalf("never ready: %v", err)
	}

	env.Dial = func(context.Context, string) (net.Conn, error) { return nil, ipc.ErrForeignEndpoint }
	if up, err := env.Running(context.Background(), "ep"); up || !errors.Is(err, ipc.ErrForeignEndpoint) {
		t.Fatalf("foreign endpoint: %v %v", up, err)
	}
}
