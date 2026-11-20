package e2e

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/testserver"
)

// expireMidway makes download id fail with link_expired, keeping its bytes: it is paused
// after some data, the server's token changes, and it is resumed (docs/ENGINE-SPEC.md §7.2).
func (h *harness) expireMidway(srv *testserver.Server, id int64) *downexv1.Download {
	h.t.Helper()
	h.waitReceived(id, 1<<20)
	h.cli("pause", strconv.FormatInt(id, 10)).ok(h.t)
	srv.SetToken("new")
	srv.Release()
	h.cli("resume", strconv.FormatInt(id, 10)).ok(h.t)
	d := h.waitFailed(id)
	if d.GetErrorCode() != "link_expired" || d.GetReceived() <= 0 {
		h.t.Fatalf("download %d: %v", id, d)
	}
	return d
}

// gone reports that downexd no longer knows download id.
func (h *harness) gone(id int64) bool {
	ctx, cancel := context.WithTimeout(context.Background(), rpcCallTimeout)
	defer cancel()
	_, err := h.dl.Get(ctx, connect.NewRequest(&downexv1.GetRequest{Id: id}))
	return connect.CodeOf(err) == connect.CodeNotFound
}

// TestRenewExpiredLink (docs/DESKTOP-SPEC.md §10): real downexd, downex-nmhost and downex
// against a server of signed links. A download whose link expires keeps its bytes; the
// browser's next hand-off of a fresh link to the same file continues it (the hand-off is
// removed) and the file is byte-exact; `downex renew` refuses a link to another file and
// continues with a good one; `ls` and `info` say "link expired".
func TestRenewExpiredLink(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	p := h.startDaemon()
	h.waitReady(p)
	h.setDefaultDir(h.out)
	const size, seed = 6 << 20, 401

	srv := newServer(t, testserver.Options{Size: size, Seed: seed, ETag: `"r1"`, Token: "old", Filename: "data.bin"})
	srv.Hold(2 << 20)
	d := h.addTo(srv.URL+"/data.bin?token=old", 0, 0)
	h.expireMidway(srv, d.GetId())
	id := strconv.FormatInt(d.GetId(), 10)
	if info := h.cli("info", id).ok(t); !strings.Contains(info.stdout, "failed (link expired)") {
		t.Errorf("info:\n%s", info.stdout)
	}

	nm := h.startNMHost(h.env)
	nm.send(map[string]any{"id": "1", "type": "hello", "protocol": 1, "browser": "chrome", "extVersion": "0.1.0"})
	if w := nm.recv(); w["type"] != "welcome" || !strings.Contains(strings.Join(anyStrings(w["features"]), ","), "renew") {
		t.Fatalf("welcome %v", w)
	}
	nm.send(map[string]any{"id": "2", "type": "add", "url": srv.URL + "/data.bin?token=new", "method": "GET",
		"headers": [][]string{{"User-Agent", "E2E/1"}}, "pageUrl": "https://pages.example/data", "size": size})
	a := nm.recv()
	newID, _ := a["downloadId"].(float64)
	if a["type"] != "added" || a["renewCheck"] != true || newID <= 0 {
		t.Fatalf("add answered %v", a)
	}
	deadline := time.Now().Add(completeTimeout)
	for !h.gone(int64(newID)) {
		if time.Now().After(deadline) {
			t.Fatalf("the hand-off %v was not removed: %v", newID, h.get(int64(newID)))
		}
		time.Sleep(pollInterval)
	}
	done := h.waitCompleted(d.GetId())
	if done.GetPageUrl() != "https://pages.example/data" {
		t.Errorf("page %q", done.GetPageUrl())
	}
	checkFile(t, filepath.Join(h.out, done.GetFilename()), seed, size)

	// downex renew: another file is refused, the right one continues.
	srv2 := newServer(t, testserver.Options{Size: size, Seed: seed + 1, Token: "old", Filename: "second.bin"})
	srv2.Hold(2 << 20)
	d2 := h.addTo(srv2.URL+"/second.bin?token=old", 0, 0)
	h.expireMidway(srv2, d2.GetId())
	if ls := h.cli("ls").ok(t); !strings.Contains(ls.stdout, "link expired") {
		t.Errorf("ls:\n%s", ls.stdout)
	}
	other := newServer(t, testserver.Options{Size: size, Seed: seed + 2})
	id2 := strconv.FormatInt(d2.GetId(), 10)
	r := h.cli("renew", id2, other.URL+"/second.bin")
	if r.code != 1 || !strings.Contains(r.stderr, "the new link serves different data") {
		t.Fatalf("renew with another file: %v", r)
	}
	if got := h.get(d2.GetId()); got.GetErrorCode() != "link_expired" {
		t.Fatalf("the refused renewal changed the download: %v", got)
	}
	r = h.cli("renew", id2, srv2.URL+"/second.bin?token=new").ok(t)
	if !strings.Contains(r.stdout, "continues with the new link") {
		t.Errorf("renew: %v", r)
	}
	checkFile(t, filepath.Join(h.out, h.waitCompleted(d2.GetId()).GetFilename()), seed+1, size)
	h.stop(p)
}

func anyStrings(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, x := range list {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
