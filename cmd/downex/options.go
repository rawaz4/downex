package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"connectrpc.com/connect"

	"downex/internal/engine"
	downexv1 "downex/internal/rpc/downex/v1"
)

// set changes a download's speed and connection limits through downexd
// (SetDownloadOptions, docs/DESKTOP-SPEC.md §7).
func (a *app) set(ctx context.Context, args []string) error {
	fs := a.flagSet("set", "set <id> [--limit rate] [-c connections]",
		"Change a download's speed limit or maximum connections in downexd. A running download\n"+
			"uses a new speed limit at once and a new connection limit as soon as it can; otherwise\n"+
			"they apply when it starts next. downexd is started if it is not running.")
	t := a.daemonFlags(fs)
	var (
		conns    int
		limitArg string
	)
	fs.IntVar(&conns, "c", 0, fmt.Sprintf("maximum `connections` (1-%d; 0 = the daemon's default)", engine.HardMaxConnections))
	fs.IntVar(&conns, "connections", 0, "the same as -c")
	fs.StringVar(&limitArg, "limit", "", "speed limit as a `rate` in bytes per second, e.g. 500K = 500 KB/s or 1.5M = 1.5 MB/s (0 = unlimited)")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return silentError{err}
	}
	mid, err := parseID("set", pos)
	if err != nil {
		return err
	}
	given := visited(fs)
	connsGiven := given["c"] || given["connections"]
	if !given["limit"] && !connsGiven {
		return usageError{"set", "give --limit, -c or both"}
	}
	req := &downexv1.SetDownloadOptionsRequest{Id: int64(mid)}
	if connsGiven {
		if err := checkConnections("set", conns); err != nil {
			return err
		}
		n := int32(conns)
		req.MaxConnections = &n
	}
	if given["limit"] {
		limit, err := parseRate(limitArg)
		if err != nil {
			return usageError{"set", "--limit: " + err.Error()}
		}
		req.SpeedLimit = &limit
	}

	c, err := a.connect(ctx, t)
	if err != nil {
		return err
	}
	cctx, cancel := rpcContext(ctx)
	defer cancel()
	resp, err := c.Downloads.SetDownloadOptions(cctx, connect.NewRequest(req))
	if err != nil {
		return downloadRPCError("set", int64(mid), err)
	}
	d := resp.Msg.GetDownload()
	conn := "the default number of connections"
	if n := d.GetMaxConnections(); n > 0 {
		conn = fmt.Sprintf("up to %d connections", n)
	}
	fmt.Fprintf(a.stdout, "Download %d: speed limit %s, %s.\n", mid, formatRate(d.GetSpeedLimit()), conn)
	switch d.GetState() {
	case downexv1.DownloadState_DOWNLOAD_STATE_PROBING, downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING:
		fmt.Fprintln(a.stdout, "The running download uses them now.")
	default:
		fmt.Fprintf(a.stdout, "They apply when it starts next (it is %s).\n", describeState(d))
	}
	return nil
}

// connectionView is one live worker connection of a running download in "info"
// (Download.connection_info).
type connectionView struct {
	Index    int     `json:"index"`
	State    string  `json:"state"`
	Start    int64   `json:"start"` // current range, inclusive; -1 when none
	End      int64   `json:"end"`
	Received int64   `json:"received"` // bytes of the current range
	SpeedBps float64 `json:"speedBps"`
	Detail   string  `json:"detail,omitempty"`
	Fragment *int    `json:"fragment,omitempty"` // media downloads: 0-based index of the fragment being fetched
}

func newConnectionViews(cs []*downexv1.ConnectionInfo) []connectionView {
	if len(cs) == 0 {
		return nil
	}
	out := make([]connectionView, 0, len(cs))
	for _, c := range cs {
		v := connectionView{
			Index: int(c.GetIndex()), State: connectionStateName(c.GetState()),
			Start: c.GetStart(), End: c.GetEnd(), Received: c.GetReceived(), SpeedBps: c.GetSpeedBps(),
			Detail: c.GetDetail(),
		}
		if f := c.GetFragment(); f >= 0 {
			n := int(f)
			v.Fragment = &n
		}
		out = append(out, v)
	}
	return out
}

// connectionStateName names a connection state for "info".
func connectionStateName(s downexv1.ConnectionState) string {
	switch s {
	case downexv1.ConnectionState_CONNECTION_STATE_CONNECTING:
		return "connecting"
	case downexv1.ConnectionState_CONNECTION_STATE_REQUESTING:
		return "requesting"
	case downexv1.ConnectionState_CONNECTION_STATE_RECEIVING:
		return "receiving"
	case downexv1.ConnectionState_CONNECTION_STATE_RETRYING:
		return "retrying"
	case downexv1.ConnectionState_CONNECTION_STATE_IDLE:
		return "idle"
	case downexv1.ConnectionState_CONNECTION_STATE_DONE:
		return "done"
	case downexv1.ConnectionState_CONNECTION_STATE_ERROR:
		return "error"
	case downexv1.ConnectionState_CONNECTION_STATE_UNSPECIFIED:
		return "unknown"
	}
	return fmt.Sprintf("state(%d)", int32(s))
}

// writeConnections prints the live connections of a running download.
func writeConnections(w io.Writer, cs []connectionView) error {
	fmt.Fprintf(w, "\nConnections (%d live):\n", len(cs))
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  N\tSTATE\tRANGE\tRECEIVED\tSPEED\tINFO")
	for _, c := range cs {
		rng := "-"
		if c.Start >= 0 {
			end := "end"
			if c.End >= 0 {
				end = fmt.Sprint(c.End)
			}
			rng = fmt.Sprintf("%d-%s", c.Start, end)
		}
		var info []string
		if c.Fragment != nil {
			// Counted from 1, as the progress window shows it; the JSON keeps the index.
			info = append(info, fmt.Sprintf("fragment %d", *c.Fragment+1))
		}
		if c.Detail != "" {
			info = append(info, printable(c.Detail))
		}
		fmt.Fprintf(tw, "  %d\t%s\t%s\t%s\t%s\t%s\n", c.Index, c.State, rng, formatBytes(c.Received),
			formatBytes(int64(c.SpeedBps))+"/s", strings.Join(info, ", "))
	}
	return tw.Flush()
}
