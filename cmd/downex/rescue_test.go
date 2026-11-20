package main

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"downex/internal/model"
	"downex/internal/rescuetext"
	downexv1 "downex/internal/rpc/downex/v1"
)

// rescueError is a Rescue refusal as downexd sends it.
func rescueError(c connect.Code, code, msg string) error {
	ce := connect.NewError(c, errors.New(code+": "+msg))
	ce.Meta().Set("Downex-Error-Code", code)
	return ce
}

// TestRescue is "downex rescue" against downexd (docs/DAEMON-SPEC.md §7).
func TestRescue(t *testing.T) {
	d := &downexv1.Download{Id: 57, Filename: "disk.iso", Size: 1_200_000_000, Received: 412_000_000,
		State: downexv1.DownloadState_DOWNLOAD_STATE_QUEUED}
	for _, tc := range []struct {
		name   string
		args   []string
		resp   *downexv1.RescueResponse
		err    error
		code   int
		stdout string
		stderr string
	}{
		{name: "rescued", args: []string{"rescue", "disk.iso.crdownload", "https://cdn.example/disk.iso", "-H", "Cookie: a=1",
			"--name", "disk.iso", "-c", "4", "--limit", "1M", "--paused"},
			resp: &downexv1.RescueResponse{Download: d, RescuedBytes: 412_000_000}, code: exitOK,
			stdout: "Rescued 412.0 MB of 1.2 GB from disk.iso.crdownload. Download 57 continues as disk.iso.\n"},
		{name: "changed", args: []string{"rescue", "disk.iso.crdownload", "https://cdn.example/disk.iso"}, code: exitError,
			err:    rescueError(connect.CodeFailedPrecondition, "rescue_changed", "the server's bytes 1-2 differ"),
			stderr: "downex rescue: " + rescuetext.Text("rescue_changed", runtime.GOOS) + "\n"},
		{name: "other code", args: []string{"rescue", "disk.iso.crdownload", "https://cdn.example/disk.iso"}, code: exitError,
			err:    rescueError(connect.CodeFailedPrecondition, "disk_full", "size part file: no space"),
			stderr: "downex rescue: size part file: no space\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, h := newFakeDaemon(t)
			f.do(func() { f.rescued, f.rescueErr = tc.resp, tc.err })
			r := h.run(context.Background(), tc.args...)
			if r.code != tc.code || r.stdout != tc.stdout || (tc.stderr != "" && r.stderr != tc.stderr) {
				t.Fatalf("rescue: %v", r)
			}
			var req *downexv1.RescueRequest
			f.do(func() { req = f.rescues[0] })
			abs, _ := filepath.Abs(tc.args[1])
			if req.GetPartialPath() != abs || req.GetUrl() != tc.args[2] || req.GetSource() != downexv1.AddSource_ADD_SOURCE_CLI {
				t.Fatalf("request %v", req)
			}
			if tc.name == "rescued" && (req.GetFilename() != "disk.iso" || req.GetMaxConnections() != 4 ||
				req.GetSpeedLimit() != 1_000_000 || !req.GetStartPaused() || len(req.GetHeaders()) != 1) {
				t.Fatalf("flags not forwarded: %v", req)
			}
		})
	}
	_, h := newFakeDaemon(t)
	for _, args := range [][]string{{"rescue", "a.part"}, {"rescue", "a.part", "ftp://a.example/f"},
		{"rescue", "a.part", "https://a.example/", "--paused", "--wait"}, {"rescue", "a.part", "https://a.example/", "-c", "99"}} {
		if r := h.run(context.Background(), args...); r.code != exitError || !strings.Contains(r.stderr, "downex rescue:") {
			t.Errorf("%v: %v", args, r)
		}
	}
}

// TestInfoShowsRescued: info prints the bytes a download kept from a partial file.
func TestInfoShowsRescued(t *testing.T) {
	db := filepath.Join(t.TempDir(), "downex.db")
	st, _, err := openStore(db)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []*model.Download{
		{Request: model.Request{URL: "https://example.test/r.iso"}, Dir: t.TempDir(), Filename: "r.iso", Size: 1_000_000_000,
			Mode: model.ModeRanged, State: model.StatePaused, RescuedBytes: 412_000_000},
		{Request: model.Request{URL: "https://example.test/n.iso"}, Dir: t.TempDir(), Size: -1, State: model.StatePaused},
	} {
		if err := st.CreateDownload(context.Background(), d); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	_, h := newFakeDaemon(t)
	h.setUp(false)
	r := h.run(context.Background(), "info", "1", "--db", db)
	if l := lineOf(r.stdout, "Rescued:"); r.code != exitOK || !strings.HasSuffix(l, "412.0 MB from a partial file") {
		t.Fatalf("info 1: %q in %v", l, r)
	}
	if r = h.run(context.Background(), "info", "2", "--db", db); lineOf(r.stdout, "Rescued:") != "" {
		t.Fatalf("info 2: %v", r)
	}
}
