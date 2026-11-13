package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	"downex/internal/client"
	"downex/internal/ipc"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/rpc/downex/v1/downexv1connect"
)

const (
	// envEndpoint overrides the default daemon endpoint (see ipc.Endpoint).
	envEndpoint = ipc.EnvEndpoint

	// rpcTimeout bounds a unary daemon call, so a wedged daemon cannot hang the CLI.
	rpcTimeout = 30 * time.Second
)

// system is what the CLI uses beyond its output streams: the daemon's endpoint,
// transport and autostart (client.Env), and the terminal. defaultSystem returns the
// real services; tests substitute fakes.
type system struct {
	client.Env
	// terminal reports whether w is a terminal that can redraw a table in place;
	// size returns its current width and height, restore undoes any mode change.
	terminal func(w io.Writer) (size func() (width, height int), restore func(), ok bool)
	// stopTimeout bounds the wait for a daemon to exit after Shutdown.
	stopTimeout time.Duration
	// password reads a secret (a site login's or the proxy's password): typed on the
	// terminal without echo after prompt is written to w, or else the first line of
	// standard input (scripts).
	password func(ctx context.Context, w io.Writer, prompt string) (string, error)
}

func defaultSystem() system {
	return system{
		Env: client.Default(), terminal: liveTerminal, stopTimeout: 15 * time.Second,
		password: func(ctx context.Context, w io.Writer, prompt string) (string, error) {
			return readPassword(ctx, os.Stdin, w, prompt)
		},
	}
}

// queueClient returns a QueueService client of c's daemon (M6). client.Client holds the
// download and daemon services; the queue service is reached over the same endpoint.
func (a *app) queueClient(c *client.Client) downexv1connect.QueueServiceClient {
	return downexv1connect.NewQueueServiceClient(a.sys.HTTPClient(c.Endpoint), a.sys.BaseURL)
}

// endpointOf returns the endpoint t selects: --endpoint, else the per-user default.
func (a *app) endpointOf(t *daemonTarget) (string, error) {
	if t.endpoint != "" {
		return t.endpoint, nil
	}
	ep, err := a.sys.Endpoint()
	if err != nil {
		return "", fmt.Errorf("locate the downexd endpoint: %w", err)
	}
	return ep, nil
}

// connect returns a client of the daemon t selects, starting downexd first when
// nothing listens on its endpoint.
func (a *app) connect(ctx context.Context, t *daemonTarget) (*client.Client, error) {
	ep, err := a.endpointOf(t)
	if err != nil {
		return nil, err
	}
	up, err := a.sys.Running(ctx, ep)
	if err != nil {
		return nil, err
	}
	c := a.sys.Client(ep)
	if !up {
		st, err := a.startDaemon(ctx, t, c)
		if err != nil {
			return nil, err
		}
		a.con.printf("Started downexd (pid %d).\n", st.GetPid())
	}
	return c, nil
}

// connectIfRunning returns a client of the daemon t selects, or nil when no daemon
// is running there.
func (a *app) connectIfRunning(ctx context.Context, t *daemonTarget) (*client.Client, error) {
	ep, err := a.endpointOf(t)
	if err != nil {
		return nil, err
	}
	up, err := a.sys.Running(ctx, ep)
	if err != nil || !up {
		return nil, err
	}
	return a.sys.Client(ep), nil
}

// startDaemon starts downexd detached, with the --db and --endpoint flags of t so
// that it opens the same database and listens where c dials, then waits up to
// StartTimeout for it to answer Status.
func (a *app) startDaemon(ctx context.Context, t *daemonTarget, c *client.Client) (*downexv1.StatusResponse, error) {
	args, err := daemonArgs(t)
	if err != nil {
		return nil, fmt.Errorf("cannot start downexd: %w", err)
	}
	return a.sys.Start(ctx, c, args...)
}

// daemonArgs returns the command line of an autostarted downexd: the --db (made
// absolute) and --endpoint flags this command was given.
func daemonArgs(t *daemonTarget) ([]string, error) {
	var args []string
	if t.db != "" {
		abs, err := filepath.Abs(t.db)
		if err != nil {
			return nil, fmt.Errorf("database path: %w", err)
		}
		args = append(args, "--db", abs)
	}
	if t.endpoint != "" {
		args = append(args, "--endpoint", t.endpoint)
	}
	return args, nil
}

// rpcContext bounds one unary daemon call.
func rpcContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, rpcTimeout)
}

// daemonError is a failed daemon call described for people. It wraps the RPC error.
type daemonError struct {
	msg string
	err error
}

func (e *daemonError) Error() string { return e.msg }
func (e *daemonError) Unwrap() error { return e.err }

// rpcError describes a failed call op ("add", "list", ...) to the daemon. Canceled
// calls are returned unchanged, so that Ctrl+C maps to the interrupted exit status.
func rpcError(op string, err error) error {
	switch connect.CodeOf(err) {
	case connect.CodeCanceled:
		return err
	case connect.CodeUnavailable:
		return &daemonError{msg: fmt.Sprintf("%s: downexd is not reachable (%v)", op, err), err: err}
	case connect.CodeDeadlineExceeded:
		return &daemonError{msg: op + ": downexd did not answer in time", err: err}
	}
	var ce *connect.Error
	if errors.As(err, &ce) && ce.Message() != "" {
		return &daemonError{msg: op + ": " + ce.Message(), err: err}
	}
	return fmt.Errorf("%s: %w", op, err)
}

// downloadRPCError is rpcError for a call about download id.
func downloadRPCError(op string, id int64, err error) error {
	if connect.CodeOf(err) == connect.CodeNotFound {
		return &daemonError{msg: fmt.Sprintf("no download with id %d (see \"downex ls\")", id), err: err}
	}
	return rpcError(op, err)
}

// commandLine formats a downex command line for people to copy, repeating the
// --endpoint and --db flags of t when they were set.
func commandLine(t *daemonTarget, args ...string) string {
	parts := append([]string{"downex"}, args...)
	if t.endpoint != "" {
		parts = append(parts, "--endpoint", shellQuote(t.endpoint))
	}
	if t.db != "" {
		db := t.db
		if abs, err := filepath.Abs(db); err == nil {
			db = abs
		}
		parts = append(parts, "--db", shellQuote(db))
	}
	return strings.Join(parts, " ")
}

func idArg(id int64) string { return strconv.FormatInt(id, 10) }
