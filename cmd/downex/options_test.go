package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
)

func TestSetCommand(t *testing.T) {
	f, h := newFakeDaemon(t)
	ctx := context.Background()
	f.do(func() {
		f.downloads[3] = &downexv1.Download{Id: 3, Url: "https://example.test/a.iso", Size: 100,
			State: downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING, MaxConnections: 8}
		f.downloads[4] = &downexv1.Download{Id: 4, Url: "https://example.test/b.iso", Size: 100,
			State: downexv1.DownloadState_DOWNLOAD_STATE_PAUSED, MaxConnections: 8, SpeedLimit: 1000}
		f.downloads[5] = &downexv1.Download{Id: 5, State: downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED}
	})

	r := h.run(ctx, "set", "3", "--limit", "500K", "-c", "4")
	if r.code != exitOK || !strings.Contains(r.stdout, "Download 3: speed limit 500.0 KB/s, up to 4 connections.") ||
		!strings.Contains(r.stdout, "The running download uses them now.") {
		t.Fatalf("set running: %v", r)
	}
	reqs := f.optionRequests()
	if len(reqs) != 1 || reqs[0].GetId() != 3 || reqs[0].SpeedLimit == nil || reqs[0].GetSpeedLimit() != 500_000 ||
		reqs[0].MaxConnections == nil || reqs[0].GetMaxConnections() != 4 {
		t.Fatalf("request = %v", reqs)
	}

	// Only the given flag is sent; a stopped download takes it on its next start.
	r = h.run(ctx, "set", "-c", "2", "4")
	if r.code != exitOK || !strings.Contains(r.stdout, "speed limit 1.0 KB/s, up to 2 connections") ||
		!strings.Contains(r.stdout, "They apply when it starts next (it is paused).") {
		t.Fatalf("set paused: %v", r)
	}
	if reqs = f.optionRequests(); len(reqs) != 2 || reqs[1].SpeedLimit != nil || reqs[1].GetMaxConnections() != 2 {
		t.Fatalf("second request = %v", reqs[1])
	}
	if r = h.run(ctx, "set", "4", "--limit", "0"); r.code != exitOK || !strings.Contains(r.stdout, "speed limit unlimited") {
		t.Fatalf("unlimited: %v", r)
	}
	if reqs = f.optionRequests(); reqs[2].SpeedLimit == nil || reqs[2].GetSpeedLimit() != 0 || reqs[2].MaxConnections != nil {
		t.Fatalf("third request = %v", reqs[2])
	}
	// --connections is the long form of -c.
	if r = h.run(ctx, "set", "4", "--connections", "3"); r.code != exitOK || !strings.Contains(r.stdout, "up to 3 connections") {
		t.Fatalf("--connections: %v", r)
	}
	if reqs = f.optionRequests(); reqs[3].SpeedLimit != nil || reqs[3].MaxConnections == nil || reqs[3].GetMaxConnections() != 3 {
		t.Fatalf("fourth request = %v", reqs[3])
	}

	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"set", "3"}, "give --limit, -c or both"},
		{[]string{"set", "--limit", "1M"}, "expects exactly one download id"},
		{[]string{"set", "3", "-c", "40"}, "-c must be between 1 and 32"},
		{[]string{"set", "3", "--limit", "fast"}, "--limit: invalid rate"},
		{[]string{"set", "99", "--limit", "1M"}, "no download with id 99"},
		{[]string{"set", "5", "--limit", "1M"}, "already completed"},
	} {
		if r := h.run(ctx, c.args...); r.code != exitError || !strings.Contains(r.stderr, c.want) {
			t.Errorf("%q: %v, want %q", c.args, r, c.want)
		}
	}
	if n := len(f.optionRequests()); n != 6 { // the two refused by the daemon reached it
		t.Errorf("%d requests sent", n)
	}
	if r := h.run(ctx, "help", "set"); r.code != exitOK || !strings.Contains(r.stdout, "Usage: downex set <id>") {
		t.Errorf("help set: %v", r)
	}
	if r := h.run(ctx, "help"); !strings.Contains(r.stdout, "downex set <id> [--limit rate] [-c connections]") {
		t.Errorf("usage lacks set:\n%s", r.stdout)
	}
}

func TestSettingsProgressWindowFlags(t *testing.T) {
	f, h := newFakeDaemon(t)
	ctx := context.Background()
	f.do(func() { f.settings.ShowProgressWindow, f.settings.ShowCompleteDialog = true, true })

	r := h.run(ctx, "settings")
	for _, want := range []string{"Progress window:            on", "Completion view:            on"} {
		if r.code != exitOK || !strings.Contains(r.stdout, want) {
			t.Errorf("settings: want %q in\n%s", want, r.stdout)
		}
	}
	r = h.run(ctx, "settings", "--progress-window", "off", "--complete-dialog=off", "--json")
	if r.code != exitOK {
		t.Fatalf("settings: %v", r)
	}
	u := f.rec().updates
	if len(u) != 1 || u[0].GetShowProgressWindow() || u[0].GetShowCompleteDialog() || !u[0].GetShowTrayIcon() ||
		len(u[0].GetCategories()) != 2 {
		t.Fatalf("UpdateSettings %v", u)
	}
	var v settingsView
	if err := json.Unmarshal([]byte(r.stdout), &v); err != nil || v.ShowProgressWindow || v.ShowCompleteDialog {
		t.Errorf("settings --json: %+v %v\n%s", v, err, r.stdout)
	}
	if r = h.run(ctx, "settings", "--complete-dialog", "on"); r.code != exitOK ||
		!strings.Contains(r.stdout, "Progress window:            off") || !strings.Contains(r.stdout, "Completion view:            on") {
		t.Errorf("settings --complete-dialog on: %v", r)
	}
	if r := h.run(ctx, "settings", "--progress-window", "sometimes"); r.code != exitError || !strings.Contains(r.stderr, "want on or off") {
		t.Errorf("bad on/off value: %v", r)
	}
}

// TestInfoShowsLiveConnections: info reads the database and adds the connections of a
// download that the running daemon runs; without a daemon (or for a stopped download)
// it shows the stored state only.
func TestInfoShowsLiveConnections(t *testing.T) {
	db := filepath.Join(t.TempDir(), "downex.db")
	st, _, err := openStore(db)
	if err != nil {
		t.Fatal(err)
	}
	d := &model.Download{Request: model.Request{URL: "https://example.test/big.iso"}, Dir: t.TempDir(), Filename: "big.iso",
		Size: 4096, Mode: model.ModeRanged, State: model.StateDownloading, MaxConnections: 4}
	if err := st.CreateDownload(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	f, h := newFakeDaemon(t)
	ctx := context.Background()
	f.do(func() {
		f.downloads[1] = &downexv1.Download{Id: 1, Size: 4096, State: downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING,
			SpeedBps: 2048, Connections: 2, ConnectionInfo: []*downexv1.ConnectionInfo{
				{Index: 1, State: downexv1.ConnectionState_CONNECTION_STATE_RECEIVING, Start: 0, End: 2047, Received: 1024,
					SpeedBps: 1536, Fragment: -1},
				{Index: 2, State: downexv1.ConnectionState_CONNECTION_STATE_RETRYING, Start: 2048, End: 4095, Fragment: -1,
					Detail: "HTTP 503, retrying in 4 s"},
			}}
	})

	r := h.run(ctx, "info", "1", "--db", db)
	for _, want := range []string{"Live:", "2 connections, 2.0 KB/s", "Connections (2 live):", "receiving", "0-2047",
		"1.0 KB", "retrying", "2048-4095", "HTTP 503, retrying in 4 s", "Segments (0):"} {
		if r.code != exitOK || !strings.Contains(r.stdout, want) {
			t.Errorf("info: want %q in\n%s", want, r.stdout)
		}
	}
	r = h.run(ctx, "info", "1", "--json", "--db", db)
	var v infoView
	if err := json.Unmarshal([]byte(r.stdout), &v); err != nil || v.Connections != 2 || len(v.ConnectionInfo) != 2 ||
		v.ConnectionInfo[1].State != "retrying" || v.ConnectionInfo[0].Fragment != nil || v.ConnectionInfo[0].Received != 1024 {
		t.Fatalf("info --json: %+v %v\n%s", v, err, r.stdout)
	}

	// A stopped download in the daemon, a download it does not know, and no daemon.
	f.do(func() {
		f.downloads[1].State, f.downloads[1].ConnectionInfo = downexv1.DownloadState_DOWNLOAD_STATE_PAUSED, nil
	})
	if r = h.run(ctx, "info", "1", "--db", db); r.code != exitOK || strings.Contains(r.stdout, "Connections (") ||
		strings.Contains(r.stdout, "Live:") {
		t.Errorf("paused in the daemon: %v", r)
	}
	f.do(func() { delete(f.downloads, 1) })
	if r = h.run(ctx, "info", "1", "--db", db); r.code != exitOK || strings.Contains(r.stdout, "Connections (") || r.stderr != "" {
		t.Errorf("unknown to the daemon: %v", r)
	}
	h.setUp(false)
	if r = h.run(ctx, "info", "1", "--db", db); r.code != exitOK || !strings.Contains(r.stdout, "big.iso") ||
		strings.Contains(r.stdout, "Connections (") {
		t.Errorf("no daemon: %v", r)
	}
	if _, spawns := h.recorded(); len(spawns) != 0 {
		t.Errorf("info started downexd: %v", spawns)
	}
}

// TestInfoMediaConnections shows the fragment of each connection of a stream.
func TestInfoMediaConnections(t *testing.T) {
	cs := newConnectionViews([]*downexv1.ConnectionInfo{
		{Index: 1, State: downexv1.ConnectionState_CONNECTION_STATE_RECEIVING, Start: -1, End: -1, Fragment: 12},
		{Index: 2, State: downexv1.ConnectionState(42), Start: 10, End: -1, Fragment: -1},
	})
	if cs[0].Fragment == nil || *cs[0].Fragment != 12 || cs[1].Fragment != nil || cs[1].State != "state(42)" {
		t.Fatalf("views = %+v", cs)
	}
	var b strings.Builder
	if err := writeConnections(&b, cs); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"fragment 13", "10-end", "state(42)"} { // index 12 is the 13th fragment
		if !strings.Contains(b.String(), want) {
			t.Errorf("want %q in\n%s", want, b.String())
		}
	}
}
