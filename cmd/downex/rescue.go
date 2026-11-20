package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"connectrpc.com/connect"

	"downex/internal/engine"
	"downex/internal/rescuetext"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/validate"
)

// rescueTimeout bounds a rescue: downexd checks the partial file with a few range
// requests (docs/ENGINE-SPEC.md §7.4) before it moves it.
const rescueTimeout = 2 * time.Minute

// rescue finishes a download a browser left unfinished (docs/DAEMON-SPEC.md §7).
func (a *app) rescue(ctx context.Context, args []string) error {
	fs := a.flagSet("rescue", "rescue <partial-file> <url> [flags]",
		"Finishes a download a browser left unfinished: <partial-file> is its .crdownload or .part\n"+
			"file, <url> the download's address. downexd checks that the server still has the same\n"+
			"file, then continues from the bytes already saved, in the partial file's folder.")
	t := a.daemonFlags(fs)
	var (
		headers  headerFlag
		conns    int
		limitArg string
		name     string
		queueArg string
		paused   bool
		wait     bool
	)
	fs.Var(&headers, "H", "request header \"Name: value\" (repeatable), e.g. Cookie or Referer")
	fs.StringVar(&name, "name", "", "save as `file` instead of the partial file's name without .crdownload or .part")
	fs.IntVar(&conns, "c", 0, fmt.Sprintf("maximum `connections` (1-%d; 0 = the daemon's default)", engine.HardMaxConnections))
	fs.StringVar(&limitArg, "limit", "0", "speed limit as a `rate` in bytes per second (0 = unlimited)")
	fs.StringVar(&queueArg, "queue", "", queueFlagUsage)
	fs.BoolVar(&paused, "paused", false, "keep the rescued download paused instead of queueing it")
	fs.BoolVar(&wait, "wait", false, "follow the download's progress until it completes")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return silentError{err}
	}
	if len(pos) != 2 {
		return usageError{"rescue", "expects the partial file and the download's address"}
	}
	if err := validate.URL(pos[1]); err != nil {
		return usageError{"rescue", "the address: " + err.Error()}
	}
	if err := checkConnections("rescue", conns); err != nil {
		return err
	}
	limit, err := parseRate(limitArg)
	if err != nil {
		return usageError{"rescue", "--limit: " + err.Error()}
	}
	if paused && wait {
		return usageError{"rescue", "--paused and --wait cannot be combined"}
	}
	partial, err := filepath.Abs(pos[0])
	if err != nil {
		return fmt.Errorf("partial file: %w", err)
	}
	req := &downexv1.RescueRequest{
		Url: pos[1], Headers: rpcHeaders(headers.h), PartialPath: partial, Filename: name,
		MaxConnections: int32(conns), SpeedLimit: limit, StartPaused: paused, Source: downexv1.AddSource_ADD_SOURCE_CLI,
	}
	c, err := a.connect(ctx, t)
	if err != nil {
		return err
	}
	var queue *downexv1.Queue
	if visited(fs)["queue"] {
		if queue, err = a.resolveQueueFlag(ctx, c, queueArg); err != nil {
			return err
		}
		req.QueueId = queue.GetId()
	}
	cctx, cancel := context.WithTimeout(ctx, rescueTimeout)
	defer cancel()
	resp, err := c.Downloads.Rescue(cctx, connect.NewRequest(req))
	if err != nil {
		return a.rescueRefused(err)
	}
	d := resp.Msg.GetDownload()
	fmt.Fprintf(a.stdout, "Rescued %s from %s. Download %d continues as %s.\n",
		keptText(rpcSize(d), resp.Msg.GetRescuedBytes()), filepath.Base(partial), d.GetId(), d.GetFilename())
	a.hintStoppedQueue(t, queue, d)
	if !wait {
		return nil
	}
	return a.follow(ctx, c, t, d)
}

// rescueRefused prints a refused rescue as "downex rescue: <message>": the texts every
// client shares for the rescue codes (rescuetext), else downexd's message without its
// code.
func (a *app) rescueRefused(err error) error {
	var ce *connect.Error
	if !errors.As(err, &ce) || interruptedErr(err) {
		return rpcError("rescue", err)
	}
	code := ce.Meta().Get("Downex-Error-Code")
	msg := rescuetext.Text(code, runtime.GOOS)
	if msg == "" {
		msg = strings.TrimPrefix(ce.Message(), code+": ")
		if code == "" {
			return rpcError("rescue", err)
		}
	}
	a.con.printf("downex rescue: %s\n", msg)
	return silentError{err}
}

// interruptedErr reports a call that Ctrl+C canceled.
func interruptedErr(err error) bool { return connect.CodeOf(err) == connect.CodeCanceled }
