package e2e

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"connectrpc.com/connect"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/testserver"
)

// writePartial writes the first n bytes of o's content to path, as a browser leaves them.
func writePartial(t *testing.T, path string, o testserver.Options, n int64) {
	t.Helper()
	b := make([]byte, n)
	testserver.ContentAt(o, b, 0)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestRescueBrowserPartial (docs/BROWSER-SPEC.md §6, docs/ENGINE-SPEC.md §7.4): real downexd,
// downex-nmhost and downex. A browser's "<name>.crdownload" holding the first 30 % is
// rescued paused through the relay, started with the relay's resume, and completes byte-exact
// with the partial moved (never copied); the first start cannot be sent twice; a partial of
// another file is refused with rescue_changed and left as it was; `downex rescue` finishes a
// Firefox-style ".part" and `info` says what was rescued.
func TestRescueBrowserPartial(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	p := h.startDaemon()
	h.waitReady(p)
	h.setDefaultDir(h.out)
	const size, seed = 6<<20 + 11, 451
	o := testserver.Options{Size: size, Seed: seed, ETag: `"b1"`, Filename: "disk.iso"}
	srv := newServer(t, o)

	kept := int64(size * 3 / 10)
	partial := filepath.Join(h.out, "disk.iso.crdownload")
	writePartial(t, partial, o, kept)

	nm := h.startNMHost(h.env)
	nm.send(map[string]any{"id": "1", "type": "hello", "protocol": 1, "browser": "chrome", "extVersion": "0.1.0"})
	if w := nm.recv(); w["type"] != "welcome" || !strings.Contains(strings.Join(anyStrings(w["features"]), ","), "rescue") {
		t.Fatalf("welcome %v", w)
	}
	nm.send(map[string]any{"id": "2", "type": "rescue", "url": srv.URL + "/disk.iso?sig=e2e-secret",
		"headers": [][]string{{"User-Agent", "E2E/1"}}, "target": filepath.Join(h.out, "disk.iso"),
		"bytesReceived": kept, "totalBytes": size, "etag": `"b1"`, "startPaused": true, "deadlineMs": 30000})
	r := nm.recv()
	id, _ := r["downloadId"].(float64)
	if r["type"] != "rescued" || id <= 0 || r["keptBytes"] != float64(kept) {
		t.Fatalf("rescue answered %v", r)
	}
	if _, err := os.Lstat(partial); !os.IsNotExist(err) {
		t.Fatalf("the browser's partial is still there: %v", err)
	}
	if d := h.get(int64(id)); d.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_PAUSED || d.GetRescuedBytes() != kept ||
		d.GetReceived() != kept || d.GetSize() != size {
		t.Fatalf("rescued download %v", d)
	}
	nm.send(map[string]any{"id": "3", "type": "resume", "downloadId": id})
	if m := nm.recv(); m["type"] != "resumed" || m["downloadId"] != id {
		t.Fatalf("resume answered %v", m)
	}
	done := h.waitCompleted(int64(id))
	if done.GetFilename() != "disk.iso" || done.GetRescuedBytes() != kept {
		t.Fatalf("completed %v", done)
	}
	checkFile(t, filepath.Join(h.out, "disk.iso"), seed, size)
	noPartFiles(t, h.out)
	// The rescue's first start is spent: the relay cannot start downloads at will.
	nm.send(map[string]any{"id": "4", "type": "resume", "downloadId": id})
	if m := nm.recv(); m["ok"] != false {
		t.Fatalf("a second resume answered %v", m)
	}

	// A partial of another file is refused and left untouched.
	other := filepath.Join(h.out, "other.bin.crdownload")
	writePartial(t, other, testserver.Options{Size: size, Seed: seed + 1}, kept)
	nm.send(map[string]any{"id": "5", "type": "rescue", "url": srv.URL + "/disk.iso", "headers": [][]string{},
		"target": filepath.Join(h.out, "other.bin"), "bytesReceived": kept, "startPaused": true})
	m := nm.recv()
	e, _ := m["error"].(map[string]any)
	if m["ok"] != false || e["reason"] != "rescue_changed" || strings.Contains(e["message"].(string), "127.0.0.1") {
		t.Fatalf("rescue of another file answered %v", m)
	}
	if fi, err := os.Stat(other); err != nil || fi.Size() != kept {
		t.Fatalf("the refused partial: %v, %v", fi, err)
	}
	nm.closeAndWait()

	// The relay's log names the target's base name only, never the query.
	b, err := os.ReadFile(h.userLog("nmhost.log"))
	if err != nil {
		t.Fatalf("relay log: %v", err)
	}
	if log := string(b); !strings.Contains(log, `"type":"rescue"`) || strings.Contains(log, "e2e-secret") {
		t.Errorf("relay log:\n%s", log)
	}

	// downex rescue: a Firefox-style partial, started at once.
	part := filepath.Join(h.out, "cli.iso.part")
	writePartial(t, part, o, size*4/10)
	res := h.cli("rescue", part, srv.URL+"/disk.iso").ok(t)
	got := regexp.MustCompile(`^Rescued .+ from cli\.iso\.part\. Download (\d+) continues as cli\.iso\.\n$`).FindStringSubmatch(res.stdout)
	if got == nil {
		t.Fatalf("rescue printed %q", res.stdout)
	}
	cid, _ := strconv.ParseInt(got[1], 10, 64)
	h.waitCompleted(cid)
	checkFile(t, filepath.Join(h.out, "cli.iso"), seed, size)
	if info := h.cli("info", got[1]).ok(t); !strings.Contains(info.stdout, "Rescued:") {
		t.Errorf("info:\n%s", info.stdout)
	}
	// A refusal: the shared text, exit 1.
	gone := h.cli("rescue", filepath.Join(h.out, "nothing.part"), srv.URL+"/disk.iso")
	if gone.code != 1 || !strings.Contains(gone.stderr, "downex rescue: ") {
		t.Fatalf("rescue of a missing file: %v", gone)
	}
	ctx, cancel := context.WithTimeout(context.Background(), rpcCallTimeout)
	defer cancel()
	if _, err := h.dl.Resume(ctx, connect.NewRequest(&downexv1.ResumeRequest{Id: cid, RescueStart: true})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("rescue_start of a CLI rescue: %v", err)
	}
}
