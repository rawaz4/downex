package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/testserver"
)

// The per-download progress window (docs/DESKTOP-SPEC.md §7) with real executables: the
// live connection_info of a running download in Watch and "downex info --json", and
// "downex set" changing a running download's speed and connection limits. Starting the
// window for a browser hand-off is TestBrowserHandOffWithoutConfirmation (desktop_test.go).

// connJSON is one live connection in "downex info --json".
type connJSON struct {
	Index    int     `json:"index"`
	State    string  `json:"state"`
	Start    int64   `json:"start"`
	End      int64   `json:"end"`
	Received int64   `json:"received"`
	SpeedBps float64 `json:"speedBps"`
	Detail   string  `json:"detail"`
	Fragment *int    `json:"fragment"`
}

// optionsJSON is the part of "downex info --json" these tests read: the stored limits,
// and the live connections while downexd runs the download.
type optionsJSON struct {
	State          string     `json:"state"`
	MaxConnections int        `json:"maxConnections"`
	SpeedLimit     int64      `json:"speedLimit"`
	Connections    int        `json:"connections"`
	ConnectionInfo []connJSON `json:"connectionInfo"`
}

func (h *harness) optionsInfo(id int64) optionsJSON {
	h.t.Helper()
	r := h.cli("info", idArg(id), "--json").ok(h.t)
	var v optionsJSON
	if err := json.Unmarshal([]byte(r.stdout), &v); err != nil {
		h.t.Fatalf("info --json: %v: %v", err, r)
	}
	return v
}

// activeConnections counts the connections that are open or about to be (connecting,
// requesting, receiving, retrying).
func activeConnections(cs []*downexv1.ConnectionInfo) int {
	n := 0
	for _, c := range cs {
		switch c.GetState() {
		case downexv1.ConnectionState_CONNECTION_STATE_CONNECTING, downexv1.ConnectionState_CONNECTION_STATE_REQUESTING,
			downexv1.ConnectionState_CONNECTION_STATE_RECEIVING, downexv1.ConnectionState_CONNECTION_STATE_RETRYING:
			n++
		}
	}
	return n
}

func receivingConnections(cs []*downexv1.ConnectionInfo) int {
	n := 0
	for _, c := range cs {
		if c.GetState() == downexv1.ConnectionState_CONNECTION_STATE_RECEIVING {
			n++
		}
	}
	return n
}

// checkConnectionInfo verifies the shape of a file download's connection_info: 1-based
// unique slots in ascending order, no fragment, and ranges inside the file.
func checkConnectionInfo(t *testing.T, cs []*downexv1.ConnectionInfo, size int64) {
	t.Helper()
	prev := int32(0)
	for _, c := range cs {
		if c.GetIndex() <= prev {
			t.Errorf("connection slots not ascending and 1-based: %v", cs)
		}
		prev = c.GetIndex()
		if c.GetFragment() != -1 {
			t.Errorf("file download connection with fragment %d: %v", c.GetFragment(), c)
		}
		if c.GetState() != downexv1.ConnectionState_CONNECTION_STATE_RECEIVING {
			continue
		}
		if c.GetStart() < 0 || c.GetStart() >= size || (c.GetEnd() != -1 && (c.GetEnd() < c.GetStart() || c.GetEnd() >= size)) ||
			c.GetReceived() < 0 || c.GetSpeedBps() < 0 || strings.Contains(c.GetDetail(), "http") {
			t.Errorf("receiving connection %v (file of %d bytes)", c, size)
		}
	}
}

// A throttled download with several connections reports one live entry per worker
// connection: in a Watch for that download (as its progress window watches) and in
// "downex info --json"; a paused or completed download reports none.
func TestLiveConnectionInfo(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	const size, seed = 8 << 20, 501
	srv := newServer(t, testserver.Options{Size: size, Seed: seed, ETag: `"ci"`, Throttle: 192 << 10})
	d := h.startDaemon()
	h.waitReady(d)

	id := h.add(srv.URL+"/conns.bin", "-o", h.out, "-c", "4")
	ctx, cancel := context.WithTimeout(context.Background(), progressTimeout)
	defer cancel()
	stream, err := h.dl.Watch(ctx, connect.NewRequest(&downexv1.WatchRequest{Ids: []int64{id}}))
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	var seen *downexv1.Download
	for seen == nil && stream.Receive() {
		for _, ev := range stream.Msg().GetEvents() {
			dl := ev.GetUpdated()
			if dl == nil {
				dl = ev.GetSnapshot()
			}
			if dl == nil {
				t.Fatalf("Watch for download %d sent %v", id, ev)
			}
			if dl.GetId() != id {
				t.Fatalf("Watch for download %d sent download %d", id, dl.GetId())
			}
			if receivingConnections(dl.GetConnectionInfo()) >= 2 {
				seen = dl
				break
			}
		}
	}
	if seen == nil {
		t.Fatalf("Watch ended (%v) before two connections were receiving: %v", stream.Err(), h.get(id))
	}
	_ = stream.Close()
	cancel()
	checkConnectionInfo(t, seen.GetConnectionInfo(), size)
	if n := len(seen.GetConnectionInfo()); n > 4 {
		t.Errorf("%d connections listed for a limit of 4: %v", n, seen.GetConnectionInfo())
	}

	// "downex info" asks the running downexd for the same.
	deadline := time.Now().Add(progressTimeout)
	for {
		v := h.optionsInfo(id)
		receiving := 0
		for _, c := range v.ConnectionInfo {
			if c.State == "receiving" {
				receiving++
			}
			if c.Fragment != nil || c.Index < 1 {
				t.Fatalf("info --json connection %+v", c)
			}
		}
		if receiving >= 2 && v.Connections >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("info --json never showed two receiving connections: %+v", v)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if r := h.cli("info", idArg(id)).ok(t); !strings.Contains(r.stdout, "live):") || !strings.Contains(r.stdout, "receiving") {
		t.Errorf("info lacks the live connections:\n%s", r.stdout)
	}

	// Paused: nothing is live, in the daemon's view or in info.
	h.cli("pause", idArg(id)).ok(t)
	if cs := h.get(id).GetConnectionInfo(); len(cs) != 0 {
		t.Errorf("paused download with connection_info %v", cs)
	}
	if v := h.optionsInfo(id); v.State != "paused" || len(v.ConnectionInfo) != 0 {
		t.Errorf("info --json of the paused download: %+v", v)
	}

	srv.Update(func(o *testserver.Options) { o.Throttle = 0 })
	h.cli("resume", idArg(id)).ok(t)
	if dl := h.waitCompleted(id); len(dl.GetConnectionInfo()) != 0 {
		t.Errorf("completed download with connection_info %v", dl.GetConnectionInfo())
	}
	checkFile(t, filepath.Join(h.out, "conns.bin"), seed, size)
	noPartFiles(t, h.out)
	h.stop(d)
}

// rate measures download id's receive rate over window from the daemon's view.
func (h *harness) rate(id int64, window time.Duration) float64 {
	h.t.Helper()
	a, t0 := h.get(id), time.Now()
	time.Sleep(window)
	b, el := h.get(id), time.Since(t0)
	if !running(a.GetState()) || !running(b.GetState()) {
		h.t.Fatalf("download %d stopped while its rate was measured: %v", id, b)
	}
	return float64(b.GetReceived()-a.GetReceived()) / el.Seconds()
}

// "downex set --limit" slows a running download to the new limit at once; the limit is
// saved with the download, survives a restart and applies when it runs again.
func TestSetSpeedLimitLive(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	const size, seed, limit = 8 << 20, 502, 64_000 // "64K" is decimal
	srv := newServer(t, testserver.Options{Size: size, Seed: seed, ETag: `"sl"`, Throttle: 256 << 10})
	d := h.startDaemon()
	h.waitReady(d)

	id := h.add(srv.URL+"/limited.bin", "-o", h.out, "-c", "2")
	h.waitReceived(id, 128<<10)
	before := h.rate(id, 1500*time.Millisecond)

	r := h.cli("set", idArg(id), "--limit", "64K").ok(t)
	if !strings.Contains(r.stdout, fmt.Sprintf("Download %d: speed limit 64.0 KB/s", id)) ||
		!strings.Contains(r.stdout, "The running download uses them now.") {
		t.Errorf("set --limit: %v", r)
	}
	if got := h.get(id).GetSpeedLimit(); got != limit {
		t.Fatalf("the daemon reports speed limit %d after set, want %d", got, limit)
	}
	time.Sleep(2 * time.Second) // the bucket's burst and the slices in flight drain
	after := h.rate(id, 6*time.Second)
	t.Logf("rate before the limit %.0f B/s, with %d B/s: %.0f B/s", before, limit, after)
	if before < 1.5*limit {
		t.Errorf("the download ran at %.0f B/s before the limit; too slow to show that %d B/s slows it", before, limit)
	}
	if after < 0.7*limit || after > 1.3*limit {
		t.Errorf("rate with the limit %.0f B/s, want %d B/s ±30%%", after, limit)
	}

	// Saved when the run stops; kept across a restart; used by the next run.
	h.cli("pause", idArg(id)).ok(t)
	if v := h.optionsInfo(id); v.State != "paused" || v.SpeedLimit != limit {
		t.Fatalf("stored after pause: %+v, want speed limit %d", v, limit)
	}
	h.stop(d)
	d = h.startDaemon()
	h.waitReady(d)
	if got := h.get(id).GetSpeedLimit(); got != limit {
		t.Fatalf("speed limit after a restart %d, want %d", got, limit)
	}
	h.cli("resume", idArg(id)).ok(t)
	h.waitReceived(id, h.get(id).GetReceived()+1)
	time.Sleep(1500 * time.Millisecond)
	if again := h.rate(id, 4*time.Second); again > 1.3*limit {
		t.Errorf("the resumed download runs at %.0f B/s, above its stored limit %d B/s", again, limit)
	}

	// Lifting it lets the download finish at full speed.
	srv.Update(func(o *testserver.Options) { o.Throttle = 0 })
	r = h.cli("set", idArg(id), "--limit", "0").ok(t)
	if !strings.Contains(r.stdout, "speed limit unlimited") {
		t.Errorf("set --limit 0: %v", r)
	}
	h.waitCompleted(id)
	checkFile(t, filepath.Join(h.out, "limited.bin"), seed, size)
	if v := h.optionsInfo(id); v.SpeedLimit != 0 {
		t.Errorf("stored speed limit %d after completion, want 0", v.SpeedLimit)
	}
	h.stop(d)
}

// "downex set --connections" lowers a running download's connections at once without
// losing data; ramp-up does not grow it again, and the limit is saved.
func TestSetConnectionsLive(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	const size, seed = 16 << 20, 503
	srv := newServer(t, testserver.Options{Size: size, Seed: seed, ETag: `"sc"`, Throttle: 128 << 10})
	d := h.startDaemon()
	h.waitReady(d)

	id := h.add(srv.URL+"/fewer.bin", "-o", h.out, "-c", "6")
	deadline := time.Now().Add(progressTimeout)
	for {
		dl := h.get(id)
		if dl.GetConnections() >= 4 && activeConnections(dl.GetConnectionInfo()) >= 4 {
			break
		}
		if !running(dl.GetState()) && dl.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_QUEUED {
			t.Fatalf("download stopped: %v", dl)
		}
		if time.Now().After(deadline) {
			t.Fatalf("the download never used 4 connections: %v", dl)
		}
		time.Sleep(50 * time.Millisecond)
	}

	r := h.cli("set", idArg(id), "--connections", "2").ok(t)
	if !strings.Contains(r.stdout, "up to 2 connections") || !strings.Contains(r.stdout, "The running download uses them now.") {
		t.Errorf("set --connections: %v", r)
	}
	if got := h.get(id).GetMaxConnections(); got != 2 {
		t.Fatalf("the daemon reports max_connections %d after set, want 2", got)
	}
	deadline = time.Now().Add(10 * time.Second)
	for {
		dl := h.get(id)
		if dl.GetConnections() <= 2 && activeConnections(dl.GetConnectionInfo()) <= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("still %d connections (%d active) 10 s after the limit was lowered to 2: %v",
				dl.GetConnections(), activeConnections(dl.GetConnectionInfo()), dl.GetConnectionInfo())
		}
		time.Sleep(50 * time.Millisecond)
	}
	// It stays there: ramp-up (at most every 250 ms) does not add connections again.
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		dl := h.get(id)
		if !running(dl.GetState()) {
			t.Fatalf("download stopped: %v", dl)
		}
		if dl.GetConnections() > 2 || activeConnections(dl.GetConnectionInfo()) > 2 {
			t.Fatalf("%d connections (%d active) after the limit was lowered to 2: %v",
				dl.GetConnections(), activeConnections(dl.GetConnectionInfo()), dl.GetConnectionInfo())
		}
	}

	srv.Update(func(o *testserver.Options) { o.Throttle = 0 })
	h.waitCompleted(id)
	checkFile(t, filepath.Join(h.out, "fewer.bin"), seed, size)
	noPartFiles(t, h.out)
	if v := h.optionsInfo(id); v.MaxConnections != 2 {
		t.Errorf("stored max connections %d, want 2", v.MaxConnections)
	}
	h.stop(d)
}
