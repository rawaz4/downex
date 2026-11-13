package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	downexv1 "downex/internal/rpc/downex/v1"
)

// sampleTools is the fake daemon's GetTools answer: Downex's own yt-dlp, Deno from PATH.
func sampleTools() *downexv1.ToolsStatus {
	return &downexv1.ToolsStatus{Enabled: true, Ready: true, Tools: []*downexv1.ToolInfo{
		{Name: "yt-dlp", State: downexv1.ToolState_TOOL_STATE_READY, Version: "2026.10.10.233141",
			Path: "/data/tools/yt-dlp/2026.10.10.233141/yt-dlp", Managed: true, LatestVersion: "2026.10.12.020101",
			CheckedAt: timestamppb.New(time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)), BytesTotal: -1, Verified: "openpgp+sha256"},
		{Name: "deno", State: downexv1.ToolState_TOOL_STATE_READY, Version: "2.5.4", Path: "/usr/bin/deno", BytesTotal: -1,
			Error: "could not download Deno (network)", ErrorCode: "network"},
	}}
}

func TestToolsStatus(t *testing.T) {
	_, h := newFakeDaemon(t)
	ctx := context.Background()
	for _, args := range [][]string{{"tools"}, {"tools", "status"}} {
		r := h.run(ctx, args...)
		if r.code != exitOK {
			t.Fatalf("%q: %v", args, r)
		}
		for _, want := range []string{
			"Video sites: on, ready", "yt-dlp:", "State:", "ready", "2026.10.10.233141 (latest: 2026.10.12.020101)",
			"Downex copy: /data/tools/yt-dlp/2026.10.10.233141/yt-dlp", "OpenPGP signature and SHA-256",
			"deno:", "found on PATH: /usr/bin/deno", "not checked (your own copy)", "Last check:  never",
			"could not download Deno (network) (network)",
		} {
			if !strings.Contains(r.stdout, want) {
				t.Errorf("%q lacks %q:\n%s", args, want, r.stdout)
			}
		}
	}
	for _, args := range [][]string{{"tools", "--json"}, {"tools", "status", "--json"}} {
		r := h.run(ctx, args...)
		var v toolsView
		if err := json.Unmarshal([]byte(r.stdout), &v); err != nil || !v.Enabled || !v.Ready || len(v.Tools) != 2 ||
			v.Tools[0].State != "ready" || !v.Tools[0].Managed || v.Tools[0].CheckedAt == nil || v.Tools[1].Managed ||
			v.Tools[1].ErrorCode != "network" || v.Tools[0].Verified != "openpgp+sha256" {
			t.Errorf("%q: %+v %v\n%s", args, v, err, r.stdout)
		}
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"tools", "frob"}, `unknown subcommand "frob"`},
		{[]string{"tools", "status", "x"}, "status takes no arguments"},
		{[]string{"tools", "install", "x"}, "install takes no arguments"},
	} {
		if r := h.run(ctx, c.args...); r.code != exitError || !strings.Contains(r.stderr, c.want) {
			t.Errorf("%q: %v, want %q", c.args, r, c.want)
		}
	}
}

// TestToolsInstallFollows: "tools install" follows the progress of the install (one line
// on a pipe) and prints the status; "tools update" asks for newer releases.
func TestToolsInstallFollows(t *testing.T) {
	f, h := newFakeDaemon(t)
	release := make(chan struct{})
	f.do(func() {
		f.installWait = release
		f.tools = &downexv1.ToolsStatus{Enabled: true, Tools: []*downexv1.ToolInfo{
			{Name: "yt-dlp", State: downexv1.ToolState_TOOL_STATE_INSTALLING, BytesDone: 34, BytesTotal: 100},
			{Name: "deno", State: downexv1.ToolState_TOOL_STATE_MISSING, BytesTotal: -1},
		}}
	})
	run := h.start(context.Background(), "tools", "install")
	waitFor(t, "the progress line", func() bool { return strings.Contains(run.stderr.String(), "Downloading yt-dlp and Deno") })
	f.do(func() { f.tools = sampleTools() })
	close(release)
	if code := run.wait(t); code != exitOK {
		t.Fatalf("tools install: %d\n%s\n%s", code, run.stdout, run.stderr)
	}
	if !strings.Contains(run.stdout.String(), "Video sites: on, ready") ||
		strings.Count(run.stderr.String(), "Downloading yt-dlp and Deno") != 1 {
		t.Errorf("output:\n%s\n%s", run.stdout, run.stderr)
	}
	if r := h.run(context.Background(), "tools", "update"); r.code != exitOK {
		t.Fatalf("tools update: %v", r)
	}
	f.do(func() {
		if len(f.toolInstalls) != 2 || f.toolInstalls[0].GetUpdate() || !f.toolInstalls[1].GetUpdate() {
			t.Errorf("installs = %v", f.toolInstalls)
		}
	})
	st := &downexv1.ToolsStatus{Tools: []*downexv1.ToolInfo{
		{State: downexv1.ToolState_TOOL_STATE_INSTALLING, BytesDone: 30, BytesTotal: 100},
		{State: downexv1.ToolState_TOOL_STATE_INSTALLING, BytesDone: 10, BytesTotal: 100},
		{State: downexv1.ToolState_TOOL_STATE_READY, BytesTotal: -1},
	}}
	if p := installProgress(st); p != "20%" {
		t.Errorf("installProgress = %q", p)
	}
	st.Tools[1].BytesTotal = -1
	if p := installProgress(st); p != "" {
		t.Errorf("installProgress with an unknown size = %q", p)
	}
}

func TestToolsErrors(t *testing.T) {
	f, h := newFakeDaemon(t)
	ctx := context.Background()
	f.do(func() {
		f.toolsErr = siteRefusal("tools_unavailable", connect.CodeUnimplemented,
			"yt-dlp and Deno are not set up: this Downex daemon does not download tools")
		f.installErr = siteRefusal("site_media_off", connect.CodeFailedPrecondition,
			"video downloads from web pages are turned off (Settings → Video sites)")
	})
	// The reason follows the sentence once (§5.5), without the sentinel's own words.
	r := h.run(ctx, "tools")
	if r.code != exitError ||
		!strings.Contains(r.stderr, "tools: Downex could not set up yt-dlp/Deno: this Downex daemon does not download tools") ||
		strings.Contains(r.stderr, "not set up: ") {
		t.Errorf("tools: %v", r)
	}
	r = h.run(ctx, "tools", "install")
	if r.code != exitError || !strings.Contains(r.stderr, "downex settings --site-media on") {
		t.Errorf("tools install: %v", r)
	}
}

// TestSettingsSiteMedia: the settings command sets and shows the site-media settings and
// the host rules' request cap.
func TestSettingsSiteMedia(t *testing.T) {
	f, h := newFakeDaemon(t)
	ctx := context.Background()
	f.do(func() {
		f.settings.SiteMedia, f.settings.ToolsAutoUpdate = true, true
		f.settings.HostRules = []*downexv1.HostRule{
			{Pattern: "*.cdn.test", MaxConnections: 2, MaxRequestBytes: 10 << 20}, {Pattern: "plain.test", SpeedLimit: 1 << 20},
		}
	})
	r := h.run(ctx, "settings")
	for _, want := range []string{"Video sites (yt-dlp):", "Update yt-dlp and Deno:     on (nightly channel)", "MAX REQUEST",
		"10.5 MB", "no cap"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("settings lacks %q:\n%s", want, r.stdout)
		}
	}
	r = h.run(ctx, "settings", "--site-media", "off", "--tools-auto-update", "off", "--tools-channel", "stable", "--json")
	if r.code != exitOK {
		t.Fatalf("settings: %v", r)
	}
	upd := f.rec().updates
	if len(upd) != 1 || upd[0].GetSiteMedia() || upd[0].GetToolsAutoUpdate() || upd[0].GetToolsChannel() != "stable" ||
		upd[0].GetHostRules()[0].GetMaxRequestBytes() != 10<<20 {
		t.Errorf("update = %v", upd)
	}
	var v settingsView
	if err := json.Unmarshal([]byte(r.stdout), &v); err != nil || v.SiteMedia || v.ToolsAutoUpdate || v.ToolsChannel != "stable" ||
		v.HostRules[0].MaxRequestBytes != 10<<20 {
		t.Errorf("--json: %+v %v", v, err)
	}
	if r := h.run(ctx, "settings", "--tools-channel", "beta"); r.code != exitError || !strings.Contains(r.stderr, "stable or nightly") {
		t.Errorf("--tools-channel beta: %v", r)
	}
	if n := len(f.rec().updates); n != 1 {
		t.Errorf("%d updates after a refused flag", n)
	}
}
