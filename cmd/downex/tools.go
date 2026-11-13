package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"connectrpc.com/connect"

	"downex/internal/client"
	downexv1 "downex/internal/rpc/downex/v1"
)

const toolsSynopsis = `tools [status] [--json]
       downex tools install
       downex tools update`

const toolsAbout = "Show or set up the tools downexd resolves video pages with (\"downex media --site\"):\n" +
	"yt-dlp, and Deno, which yt-dlp runs for YouTube. downexd uses copies found on PATH, else\n" +
	"downloads its own from the publishers' GitHub releases on first use, verifies them and keeps\n" +
	"them updated (\"downex settings --tools-auto-update\", \"--tools-channel\").\n" +
	"  status   show each tool: state, version, Downex copy or PATH, how it was verified, the last\n" +
	"           check and the last error (the default)\n" +
	"  install  download the tools now, unless they are ready; follows the progress\n" +
	"  update   look for newer releases of Downex's copies and install them; follows the progress\n\n" +
	"yt-dlp (Unlicense; the Windows/macOS/Linux executables are GPLv3+) is downloaded from\n" +
	"github.com/yt-dlp; Deno (MIT) from github.com/denoland/deno. Whether a site's terms allow\n" +
	"downloading is your responsibility."

func (a *app) tools(ctx context.Context, args []string) error {
	if len(args) > 0 && strings.HasPrefix(args[0], "-") && !isHelpFlag(args[0]) {
		return a.toolsStatus(ctx, args) // "downex tools --json"
	}
	return a.runSub(ctx, "tools", toolsSynopsis, toolsAbout, "status, install or update", args, "status",
		map[string]func(context.Context, []string) error{
			"status":  a.toolsStatus,
			"install": func(ctx context.Context, args []string) error { return a.toolsInstall(ctx, args, false) },
			"update":  func(ctx context.Context, args []string) error { return a.toolsInstall(ctx, args, true) },
		})
}

// toolsView is the JSON form of the tools' status ("downex tools --json").
type toolsView struct {
	Enabled bool       `json:"enabled"` // site media is on
	Ready   bool       `json:"ready"`   // video pages can be resolved
	Tools   []toolView `json:"tools"`
}

type toolView struct {
	Name          string     `json:"name"`
	State         string     `json:"state"` // missing | installing | ready | failed | disabled | unknown
	Version       string     `json:"version,omitempty"`
	Path          string     `json:"path,omitempty"`
	Managed       bool       `json:"managed"` // Downex's own copy (false: found on PATH)
	LatestVersion string     `json:"latestVersion,omitempty"`
	CheckedAt     *time.Time `json:"checkedAt,omitempty"`
	BytesDone     int64      `json:"bytesDone,omitempty"`
	BytesTotal    int64      `json:"bytesTotal,omitempty"` // -1 = unknown
	Error         string     `json:"error,omitempty"`
	ErrorCode     string     `json:"errorCode,omitempty"`
	Verified      string     `json:"verified,omitempty"`
}

func newToolsView(s *downexv1.ToolsStatus) toolsView {
	v := toolsView{Enabled: s.GetEnabled(), Ready: s.GetReady(), Tools: make([]toolView, 0, len(s.GetTools()))}
	for _, t := range s.GetTools() {
		tv := toolView{
			Name: t.GetName(), State: toolStateName(t.GetState()), Version: t.GetVersion(), Path: t.GetPath(),
			Managed: t.GetManaged(), LatestVersion: t.GetLatestVersion(), BytesDone: t.GetBytesDone(),
			BytesTotal: t.GetBytesTotal(), Error: t.GetError(), ErrorCode: t.GetErrorCode(), Verified: t.GetVerified(),
		}
		if ts := t.GetCheckedAt(); ts != nil {
			c := ts.AsTime()
			tv.CheckedAt = &c
		}
		v.Tools = append(v.Tools, tv)
	}
	return v
}

// toolStateName names a tool state.
func toolStateName(s downexv1.ToolState) string {
	switch s {
	case downexv1.ToolState_TOOL_STATE_MISSING:
		return "missing"
	case downexv1.ToolState_TOOL_STATE_INSTALLING:
		return "installing"
	case downexv1.ToolState_TOOL_STATE_READY:
		return "ready"
	case downexv1.ToolState_TOOL_STATE_FAILED:
		return "failed"
	case downexv1.ToolState_TOOL_STATE_DISABLED:
		return "disabled"
	}
	return "unknown"
}

// verifiedText describes how a copy was verified.
func verifiedText(v string, managed bool) string {
	switch v {
	case "openpgp+sha256":
		return "OpenPGP signature and SHA-256"
	case "sha256+authenticode":
		return "SHA-256 and Authenticode signature"
	case "sha256":
		return "SHA-256"
	case "":
		if managed {
			return "-"
		}
		return "not checked (your own copy)"
	}
	return printable(v)
}

// writeTools prints the tools' status for people.
func writeTools(w io.Writer, v toolsView) error {
	switch {
	case !v.Enabled:
		fmt.Fprintln(w, "Video sites: off (downex settings --site-media on)")
	case v.Ready:
		fmt.Fprintln(w, "Video sites: on, ready")
	default:
		fmt.Fprintln(w, "Video sites: on, not ready (downex tools install)")
	}
	for _, t := range v.Tools {
		fmt.Fprintf(w, "\n%s:\n", printable(t.Name))
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		state := t.State
		if t.State == "installing" {
			if p := toolPercent(t); p != "" {
				state += " " + p
			}
		}
		fmt.Fprintf(tw, "  State:\t%s\n", state)
		version := dash(printable(t.Version))
		if t.LatestVersion != "" && t.LatestVersion != t.Version {
			version += " (latest: " + printable(t.LatestVersion) + ")"
		}
		fmt.Fprintf(tw, "  Version:\t%s\n", version)
		if t.Path != "" {
			copyOf := "Downex copy"
			if !t.Managed {
				copyOf = "found on PATH"
			}
			fmt.Fprintf(tw, "  Copy:\t%s: %s\n", copyOf, printable(t.Path))
			fmt.Fprintf(tw, "  Verified:\t%s\n", verifiedText(t.Verified, t.Managed))
		}
		checked := "never"
		if t.CheckedAt != nil {
			checked = formatTime(*t.CheckedAt)
		}
		fmt.Fprintf(tw, "  Last check:\t%s\n", checked)
		if t.Error != "" {
			e := printable(t.Error)
			if t.ErrorCode != "" {
				e += " (" + printable(t.ErrorCode) + ")"
			}
			fmt.Fprintf(tw, "  Error:\t%s\n", e)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	return nil
}

// toolPercent is the progress of an installing tool ("34%"), or "" when its size is unknown.
func toolPercent(t toolView) string {
	if t.BytesTotal <= 0 {
		return ""
	}
	return fmt.Sprintf("%.0f%%", percent(t.BytesDone, t.BytesTotal))
}

// installProgress is the progress of every installing tool together ("34%"), or "".
func installProgress(s *downexv1.ToolsStatus) string {
	var done, total int64
	for _, t := range s.GetTools() {
		if t.GetState() != downexv1.ToolState_TOOL_STATE_INSTALLING {
			continue
		}
		if t.GetBytesTotal() <= 0 {
			return ""
		}
		done, total = done+t.GetBytesDone(), total+t.GetBytesTotal()
	}
	if total <= 0 {
		return ""
	}
	return fmt.Sprintf("%.0f%%", percent(done, total))
}

// installing reports whether a tool is being installed.
func installing(s *downexv1.ToolsStatus) bool {
	for _, t := range s.GetTools() {
		if t.GetState() == downexv1.ToolState_TOOL_STATE_INSTALLING {
			return true
		}
	}
	return false
}

func (a *app) toolsStatus(ctx context.Context, args []string) error {
	fs := a.flagSet("tools status", "tools [status] [--json]",
		"Show the tools downexd resolves video pages with: yt-dlp and Deno.")
	t := a.daemonFlags(fs)
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return silentError{err}
	}
	if len(pos) != 0 {
		return usageError{"tools", "status takes no arguments"}
	}
	c, err := a.connect(ctx, t)
	if err != nil {
		return err
	}
	cctx, cancel := rpcContext(ctx)
	defer cancel()
	resp, err := c.Daemon.GetTools(cctx, connect.NewRequest(&downexv1.GetToolsRequest{}))
	if err != nil {
		return toolsRPCError(err)
	}
	v := newToolsView(resp.Msg.GetStatus())
	if *asJSON {
		return writeJSON(a.stdout, v)
	}
	return writeTools(a.stdout, v)
}

// toolsInstall is "tools install" (InstallTools{}) and "tools update" (InstallTools{update:
// true}): it follows the progress for up to a.toolsFor ("Downloading yt-dlp and Deno: 34%"),
// then prints the status. Ctrl+C only stops following; the install goes on.
func (a *app) toolsInstall(ctx context.Context, args []string, update bool) error {
	sub, about := "install", "Download yt-dlp and Deno now, unless they are ready, and follow the progress."
	if update {
		sub, about = "update", "Look for newer releases of Downex's copies of yt-dlp and Deno, install them and follow the progress."
	}
	fs := a.flagSet("tools "+sub, "tools "+sub, about)
	t := a.daemonFlags(fs)
	pos, err := parseArgs(fs, args)
	if err != nil {
		return silentError{err}
	}
	if len(pos) != 0 {
		return usageError{"tools", sub + " takes no arguments"}
	}
	c, err := a.connect(ctx, t)
	if err != nil {
		return err
	}
	ictx, cancel := context.WithTimeout(ctx, a.toolsFor)
	defer cancel()
	type result struct {
		st  *downexv1.ToolsStatus
		err error
	}
	done := make(chan result, 1)
	go func() {
		resp, err := c.Daemon.InstallTools(ictx, connect.NewRequest(&downexv1.InstallToolsRequest{Update: update}))
		if err != nil {
			done <- result{err: err}
			return
		}
		done <- result{st: resp.Msg.GetStatus()}
	}()
	shown := false
	var res result
follow:
	for {
		select {
		case res = <-done:
			break follow
		case <-ctx.Done():
			a.con.printf("")
			return ctx.Err()
		default:
		}
		if !client.Sleep(ctx, a.toolsPoll) {
			continue // ctx's end is reported above
		}
		select {
		case res = <-done:
			break follow
		default:
		}
		pctx, pcancel := rpcContext(ctx)
		resp, err := c.Daemon.GetTools(pctx, connect.NewRequest(&downexv1.GetToolsRequest{}))
		pcancel()
		if err != nil || !installing(resp.Msg.GetStatus()) {
			continue
		}
		line := "Downloading yt-dlp and Deno…"
		if p := installProgress(resp.Msg.GetStatus()); p != "" {
			line = "Downloading yt-dlp and Deno: " + p
		}
		if a.con.live {
			a.con.status(line)
		} else if !shown {
			shown = true
			a.con.printf("Downloading yt-dlp and Deno…\n")
		}
	}
	a.con.printf("") // erases the progress line
	if res.err != nil {
		if errors.Is(res.err, context.DeadlineExceeded) || connect.CodeOf(res.err) == connect.CodeDeadlineExceeded {
			return fmt.Errorf("tools: the install did not end within %s; it goes on in downexd (see \"downex tools\")", a.toolsFor)
		}
		return toolsRPCError(res.err)
	}
	if err := writeTools(a.stdout, newToolsView(res.st)); err != nil {
		return err
	}
	if installing(res.st) {
		a.con.printf("The install goes on in downexd; see \"downex tools\".\n")
	}
	return nil
}

// toolsRPCError describes a failed tools call, explaining the site-media codes.
func toolsRPCError(err error) error {
	var ce *connect.Error
	if errors.As(err, &ce) {
		switch code := ce.Meta().Get(errorCodeMeta); code {
		case "site_media_off":
			return &daemonError{msg: "tools: video downloads from web pages are turned off (downex settings --site-media on)", err: err}
		case "tools_unavailable":
			msg := "tools: Downex could not set up yt-dlp/Deno"
			if reason := siteReason(ce.Message(), code); reason != "" {
				msg += ": " + reason
			}
			return &daemonError{msg: msg, err: err}
		}
	}
	return rpcError("tools", err)
}
