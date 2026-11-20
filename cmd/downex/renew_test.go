package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/testserver"
)

// renewError is a RenewLink refusal as downexd sends it: the code as message prefix and
// as Downex-Error-Code.
func renewError(code, msg string) error {
	ce := connect.NewError(connect.CodeFailedPrecondition, errors.New(code+": "+msg))
	ce.Meta().Set("Downex-Error-Code", code)
	return ce
}

// TestRenew is "downex renew" against downexd (docs/DESKTOP-SPEC.md §10, CLI).
func TestRenew(t *testing.T) {
	kept := &downexv1.Download{Id: 12, State: downexv1.DownloadState_DOWNLOAD_STATE_QUEUED, Size: 9_900_000, Received: 4_200_000}
	for _, tc := range []struct {
		name     string
		args     []string
		resp     *downexv1.RenewLinkResponse
		err      error
		code     int
		stdout   string
		stderr   string
		nameOK   bool
		restart  bool
		start    bool
		hasHeads bool
	}{
		{name: "continues", args: []string{"renew", "12", "https://cdn.example/a.zip?t=2"},
			resp: &downexv1.RenewLinkResponse{Download: kept}, code: exitOK, start: true,
			stdout: "Download 12 continues with the new link (4.2 MB of 9.9 MB kept).\n"},
		{name: "restarted", args: []string{"renew", "12", "https://cdn.example/a.zip", "--restart-if-needed", "--name-ok", "-H", "Cookie: c=1"},
			resp: &downexv1.RenewLinkResponse{Download: kept, Restarted: true}, code: exitOK, nameOK: true, restart: true, start: true,
			hasHeads: true, stdout: "Download 12 downloads again from the start with the new link.\n"},
		{name: "later", args: []string{"renew", "12", "https://cdn.example/a.zip", "--later"},
			resp: &downexv1.RenewLinkResponse{Download: kept}, code: exitOK,
			stdout: "Download 12 continues with the new link (4.2 MB of 9.9 MB kept).\nIt stays paused. Resume it with: downex resume 12\n"},
		{name: "other name", args: []string{"renew", "12", "https://cdn.example/b.zip"}, code: exitError, start: true,
			err:    renewError("renew_name", `the new link names the file "b.zip"; download 12 is "a.zip"`),
			stderr: "downex: error: the new link names the file \"b.zip\"; download 12 is \"a.zip\". Run again with --name-ok to continue anyway.\n"},
		{name: "unproven", args: []string{"renew", "12", "https://cdn.example/a.zip"}, code: exitError, start: true,
			err: renewError("renew_unproven", "too little data"),
			stderr: "downex: error: too little of download 12 can be compared with the new link. Run again with " +
				"--restart-if-needed to download it from the start.\n"},
		{name: "mismatch", args: []string{"renew", "12", "https://cdn.example/a.zip"}, code: exitError, start: true,
			err:    renewError("renew_mismatch", "bytes 0-65535 differ: engine: the new link serves different data"),
			stderr: "downex: error: bytes 0-65535 differ: engine: the new link serves different data\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, h := newFakeDaemon(t)
			f.do(func() { f.renewed, f.renewErr = tc.resp, tc.err })
			r := h.run(context.Background(), tc.args...)
			if r.code != tc.code || r.stdout != tc.stdout || (tc.stderr != "" && r.stderr != tc.stderr) {
				t.Fatalf("renew: %v", r)
			}
			var req *downexv1.RenewLinkRequest
			f.do(func() { req = f.renews[0] })
			if req.GetId() != 12 || req.GetUrl() != tc.args[2] || req.GetAllowNameChange() != tc.nameOK ||
				req.GetRestartIfUnproven() != tc.restart || req.GetStart() != tc.start || (len(req.GetHeaders()) > 0) != tc.hasHeads {
				t.Fatalf("request = %v", req)
			}
		})
	}
	_, h := newFakeDaemon(t)
	for _, args := range [][]string{{"renew", "12"}, {"renew", "x", "https://a.example/"}, {"renew", "12", "ftp://a.example/f"},
		{"renew", "12", "https://a.example/", "--later", "--wait"}} {
		if r := h.run(context.Background(), args...); r.code != exitError || !strings.Contains(r.stderr, "downex renew:") {
			t.Errorf("%v: %v", args, r)
		}
	}
}

// TestSettingsRenewLinks: --renew-links sets the setting and settings shows it.
func TestSettingsRenewLinks(t *testing.T) {
	f, h := newFakeDaemon(t)
	r := h.run(context.Background(), "settings", "--renew-links", "ask")
	if r.code != exitOK || !strings.Contains(r.stdout, "Expired links:") || !strings.Contains(r.stdout, "ask before continuing") {
		t.Fatalf("settings: %v", r)
	}
	if u := f.rec().updates; len(u) != 1 || u[0].GetRenewLinks() != downexv1.RenewLinks_RENEW_LINKS_ASK {
		t.Fatalf("updates = %v", u)
	}
	if r := h.run(context.Background(), "settings", "--renew-links", "sometimes"); r.code != exitError ||
		!strings.Contains(r.stderr, "--renew-links must be auto, ask or off") {
		t.Fatalf("bad value: %v", r)
	}
	r = h.run(context.Background(), "settings", "--json")
	if !strings.Contains(r.stdout, `"renewLinks": "ask"`) {
		t.Fatalf("settings --json: %s", r.stdout)
	}
}

// TestLinkExpiredState: ls says "link expired", from downexd's code or a stored message.
func TestLinkExpiredState(t *testing.T) {
	v := downloadView{State: "failed", ErrorCode: "link_expired"}
	if v.stateText() != "link expired" {
		t.Fatalf("stateText = %q", v.stateText())
	}
	local := downloadView{State: "failed", Error: "bytes 0-9: HTTP 410 Gone: engine: link expired"}
	if local.stateText() != "link expired" {
		t.Fatalf("local stateText = %q", local.stateText())
	}
	if other := (downloadView{State: "failed", ErrorCode: "http_status", Error: "x engine: link expired"}); other.stateText() != "failed" {
		t.Fatalf("other code = %q", other.stateText())
	}
}

// TestRenewLocal: a "get" whose link already expired fails with the renew hint; "renew
// --local" with a new link downloads it (nothing was kept: from the start).
func TestRenewLocal(t *testing.T) {
	t.Parallel()
	const size = 300 << 10
	srv := newServer(t, testserver.Options{Size: size, Seed: 77, Token: "new"})
	e := newEnv(t)
	ctx := context.Background()
	r := e.get(ctx, srv.URL+"/f.bin?token=old")
	if r.code != exitError || !strings.Contains(r.stderr, "The link stopped working (it expired, was revoked or leads to a web page).") ||
		!strings.Contains(r.stderr, "downex renew 1 <new-url> --local") {
		t.Fatalf("get of an expired link: %v", r)
	}
	if d, _ := e.load(t, 1); d.State != model.StateFailed {
		t.Fatalf("state %v", d.State)
	}
	if r := e.cli(ctx, "ls"); !strings.Contains(r.stdout, "link expired") {
		t.Fatalf("ls: %v", r)
	}
	r = e.cli(ctx, "renew", "1", srv.URL+"/f.bin?token=new", "--local", "--proxy", "direct")
	if r.code != exitOK || !strings.Contains(r.stdout, "Download 1 downloads again from the start with the new link.") {
		t.Fatalf("renew --local: %v", r)
	}
	if got := fileSum(t, filepath.Join(e.dir, "f.bin")); got != srv.Sum256() {
		t.Fatal("the renewed download's file differs from the served one")
	}
}
