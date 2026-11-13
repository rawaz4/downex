// Package client connects the daemon's clients (the downex CLI, the native-messaging
// relay) to downexd: Connect clients over the per-user local endpoint (internal/ipc),
// and the autostart of docs/DAEMON-SPEC.md §7: when nothing listens on the endpoint,
// downexd (the one of the running executable's installation, see Layout, or
// $DOWNEX_DAEMON) is started detached and awaited until it answers Status, for at most
// StartTimeout (3 s by default).
package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"connectrpc.com/connect"

	"downex/internal/ipc"
	"downex/internal/platform"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/rpc/downex/v1/downexv1connect"
)

const (
	// EnvDaemon names the downexd executable that clients autostart.
	EnvDaemon = "DOWNEX_DAEMON"

	// ProbeTimeout bounds one connection attempt to the endpoint: a Windows pipe whose
	// instances are all busy makes Dial wait.
	ProbeTimeout = 2 * time.Second
	// DefaultStartTimeout bounds the wait for an autostarted daemon (DAEMON-SPEC §7).
	DefaultStartTimeout = 3 * time.Second
)

var (
	// ErrElevatedStart refuses to start downexd from an elevated process: the daemon
	// would inherit administrator rights, and every program of the user could use them
	// through it (docs/DAEMON-SPEC.md §1: no elevation).
	ErrElevatedStart = errors.New("downexd is not running, and it is never started from an elevated " +
		"(administrator) prompt; run \"downex daemon start\" from a normal prompt first")
	// ErrNotReady reports a daemon that did not answer within the start timeout.
	ErrNotReady = errors.New("downexd did not answer")
)

// Env is what a client needs beyond its own logic: the daemon's endpoint and transport
// and the means to start it. Default returns the real services; tests substitute fakes.
type Env struct {
	// Endpoint returns the per-user default endpoint (ipc.Endpoint).
	Endpoint func() (string, error)
	// Dial connects to an endpoint; the error wraps ipc.ErrNotRunning when nothing
	// listens there (ipc.Dial).
	Dial func(ctx context.Context, endpoint string) (net.Conn, error)
	// HTTPClient returns the client for Connect calls to an endpoint (ipc.NewHTTPClient).
	HTTPClient func(endpoint string) connect.HTTPClient
	// BaseURL is the base URL of the daemon's services (ipc.BaseURL).
	BaseURL string
	// DaemonPath locates the downexd executable (FindDaemon).
	DaemonPath func() (string, error)
	// StartDetached starts a background process (platform.StartDetached).
	StartDetached func(exe string, args ...string) error
	// Elevated reports whether this process runs with administrator (elevated) rights
	// (platform.Elevated); downexd is never started from such a process.
	Elevated func() (bool, error)
	// LogDir returns the daemon's log directory, named when it fails to start.
	LogDir func() (string, error)
	// StartTimeout bounds the wait for an autostarted daemon to answer Status.
	StartTimeout time.Duration
	// PollInterval is the delay between readiness checks.
	PollInterval time.Duration
}

// Default returns the Env of the real system.
func Default() Env {
	return Env{
		Endpoint:      ipc.Endpoint,
		Dial:          ipc.Dial,
		HTTPClient:    func(endpoint string) connect.HTTPClient { return ipc.NewHTTPClient(endpoint) },
		BaseURL:       ipc.BaseURL,
		DaemonPath:    func() (string, error) { return FindDaemon(os.Getenv, os.Executable) },
		StartDetached: platform.StartDetached,
		Elevated:      platform.Elevated,
		LogDir:        platform.LogDir,
		StartTimeout:  DefaultStartTimeout,
		PollInterval:  50 * time.Millisecond,
	}
}

// DaemonName is the file name of the daemon executable.
func DaemonName() string { return daemonName(runtime.GOOS) }

// FindDaemon returns the downexd executable: $DOWNEX_DAEMON when set, else the one of
// the running executable's installation (FindLayout: the file next to it, symbolic
// links resolved; inside the macOS app bundle the login item Downex Agent.app when it
// is installed, which shows the menu-bar icon, else the downexd in Contents/Helpers).
func FindDaemon(getenv func(string) string, executable func() (string, error)) (string, error) {
	if p := getenv(EnvDaemon); p != "" {
		return p, nil
	}
	l, err := FindLayout(executable)
	if err != nil {
		return "", fmt.Errorf("locate the running executable: %w (set %s to the path of downexd)", err, EnvDaemon)
	}
	p := l.autostart(func(p string) bool {
		fi, err := os.Stat(p)
		return err == nil && fi.Mode().IsRegular()
	})
	if _, err := os.Stat(p); err != nil {
		where := "next to " + strings.TrimSuffix(filepath.Base(l.Exe), ".exe")
		if l.Bundle != "" {
			where = "in " + l.Dir
		}
		return "", fmt.Errorf("downexd is not installed %s: %w (install it there or set %s to its path)", where, err, EnvDaemon)
	}
	return p, nil
}

// Client holds the Connect clients of one daemon endpoint.
type Client struct {
	Downloads downexv1connect.DownloadServiceClient
	Daemon    downexv1connect.DaemonServiceClient
	Queues    downexv1connect.QueueServiceClient // docs/M6-SPEC.md §1
	Endpoint  string
}

// Client returns clients for endpoint using the default Connect protocol.
func (e Env) Client(endpoint string) *Client {
	hc := e.HTTPClient(endpoint)
	return &Client{
		Downloads: downexv1connect.NewDownloadServiceClient(hc, e.BaseURL),
		Daemon:    downexv1connect.NewDaemonServiceClient(hc, e.BaseURL),
		Queues:    downexv1connect.NewQueueServiceClient(hc, e.BaseURL),
		Endpoint:  endpoint,
	}
}

// Running reports whether a daemon accepts connections on endpoint. It is false only
// when the dialer reports ipc.ErrNotRunning; any other failure (such as
// ipc.ErrForeignEndpoint) is an error.
func (e Env) Running(ctx context.Context, endpoint string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()
	conn, err := e.Dial(ctx, endpoint)
	switch {
	case errors.Is(err, ipc.ErrNotRunning):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("connect to downexd at %s: %w", endpoint, err)
	}
	_ = conn.Close()
	return true, nil
}

// Connect returns a client of the daemon on endpoint, starting downexd with args
// first when nothing listens there. started is the new daemon's status, or nil when
// one was already running.
func (e Env) Connect(ctx context.Context, endpoint string, args ...string) (c *Client, started *downexv1.StatusResponse, err error) {
	up, err := e.Running(ctx, endpoint)
	if err != nil {
		return nil, nil, err
	}
	c = e.Client(endpoint)
	if !up {
		if started, err = e.Start(ctx, c, args...); err != nil {
			return nil, nil, err
		}
	}
	return c, started, nil
}

// Start starts downexd detached with args (which should make it listen where c dials),
// then waits up to StartTimeout for it to answer Status.
func (e Env) Start(ctx context.Context, c *Client, args ...string) (*downexv1.StatusResponse, error) {
	if up, err := e.Elevated(); err != nil || up {
		if err == nil {
			err = ErrElevatedStart
		}
		return nil, fmt.Errorf("cannot start downexd: %w", err)
	}
	exe, err := e.DaemonPath()
	if err != nil {
		return nil, fmt.Errorf("cannot start downexd: %w", err)
	}
	if err := e.StartDetached(exe, args...); err != nil {
		return nil, fmt.Errorf("cannot start downexd (%s): %w", exe, err)
	}
	st, err := e.Await(ctx, c)
	if err != nil {
		if ctx.Err() != nil {
			return nil, err
		}
		hint := "its log"
		if dir, lerr := e.LogDir(); lerr == nil {
			hint = "its log in " + dir
		}
		return nil, fmt.Errorf("started %s, but %w; see %s", exe, err, hint)
	}
	return st, nil
}

// Await polls c's endpoint until the daemon answers Status, for at most StartTimeout.
// The error wraps ErrNotReady when it did not answer in time.
func (e Env) Await(ctx context.Context, c *Client) (*downexv1.StatusResponse, error) {
	wctx, cancel := context.WithTimeout(ctx, e.StartTimeout)
	defer cancel()
	var last error // the most recent failure other than "not running yet"
	for {
		up, err := e.Running(wctx, c.Endpoint)
		if err == nil && up {
			resp, serr := c.Daemon.Status(wctx, connect.NewRequest(&downexv1.StatusRequest{}))
			if serr == nil {
				return resp.Msg, nil
			}
			err = serr
		}
		if err != nil && wctx.Err() == nil {
			last = err
		}
		if !Sleep(wctx, e.PollInterval) {
			break
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if last != nil {
		return nil, fmt.Errorf("%w within %s (last error: %v)", ErrNotReady, e.StartTimeout, last)
	}
	return nil, fmt.Errorf("%w within %s", ErrNotReady, e.StartTimeout)
}

// Sleep waits for d and reports whether it elapsed before ctx was done.
func Sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
