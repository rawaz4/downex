package engine

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"downex/internal/filecheck"
	"downex/internal/model"
	"downex/internal/testserver"
)

// Progress.Head (docs/ENGINE-SPEC.md §9): the file's first bytes, once per run.

type headLog struct {
	mu     sync.Mutex
	heads  [][]byte
	events int
	first  int // the event that carried the first head (1-based; 0 = none)
}

func (l *headLog) progress(p Progress) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events++
	if p.Head != nil {
		l.heads = append(l.heads, bytes.Clone(p.Head))
		if l.first == 0 {
			l.first = l.events
		}
	}
}

func (l *headLog) only(t *testing.T) []byte {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.heads) != 1 {
		t.Fatalf("%d heads in %d events, want exactly 1", len(l.heads), l.events)
	}
	return l.heads[0]
}

func pePrefix() []byte {
	b := make([]byte, 1024)
	copy(b, "MZ")
	b[0x3C] = 0x80
	copy(b[0x80:], "PE\x00\x00")
	return b
}

func TestHeadBytesMatchFileCheck(t *testing.T) {
	if HeadBytes != filecheck.HeadBytes {
		t.Fatalf("engine head %d, file check head %d", HeadBytes, filecheck.HeadBytes)
	}
}

func TestHeadOncePerRanged32ConnectionRun(t *testing.T) {
	t.Parallel()
	so := testserver.Options{Size: 8 << 20, Seed: 5, ETag: `"h1"`, Prefix: pePrefix(), ContentType: "video/mp4; codecs=avc1", Filename: "movie.mp4"}
	h := newHarness(t, so, fastOpts())
	d := h.add("/movie", "", 32)
	var log headLog
	if err := h.run(d.ID, nil, log.progress); err != nil {
		t.Fatalf("Run: %v", err)
	}
	h.verifyPrefixed(d.ID)
	head := log.only(t)
	if want := testserver.Content(h.srv.Options())[:HeadBytes]; !bytes.Equal(head, want) {
		t.Fatal("head differs from the served bytes")
	}
	got := h.st.get(t, d.ID)
	if got.ServedType != "video/mp4" {
		t.Fatalf("ServedType = %q", got.ServedType)
	}
	if fc := filecheck.CheckHead(got.Filename, got.NameControls, got.ServedType, head); fc == nil || fc.Level != model.FileCheckDanger {
		t.Fatalf("CheckHead = %+v", fc)
	}
}

func TestHeadOfSmallKnownSize(t *testing.T) {
	t.Parallel()
	so := testserver.Options{Size: 10_000, Seed: 6, Prefix: []byte("<!DOCTYPE html><html>")}
	h := newHarness(t, so, fastOpts())
	d := h.add("/a.zip", "", 8)
	var log headLog
	if err := h.run(d.ID, nil, log.progress); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if head := log.only(t); !bytes.Equal(head, testserver.Content(so)) {
		t.Fatalf("head of %d bytes differs from the file", len(head))
	}
}

// TestHeadOfUnknownSize: a single-mode download without Content-Length gets its head
// at completion when it is shorter than HeadBytes.
func TestHeadOfUnknownSize(t *testing.T) {
	t.Parallel()
	body := []byte(strings.Repeat("not the video you wanted\n", 100))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush() // chunked: no Content-Length
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	st := newMemStore()
	eng := New(st, &httpFactory{newTransport: func() *http.Transport { return srv.Client().Transport.(*http.Transport).Clone() }}, fastOpts())
	d, err := eng.Add(context.Background(), model.Request{URL: srv.URL + "/video.mp4"}, t.TempDir(), "", 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	var log headLog
	ctx, cancel := runContext(t)
	defer cancel()
	if err := eng.Run(ctx, d.ID, nil, log.progress); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := st.get(t, d.ID); got.Mode != model.ModeSingle || got.Size != int64(len(body)) || got.ServedType != "text/plain" {
		t.Fatalf("record = %+v", got)
	}
	if head := log.only(t); !bytes.Equal(head, body) {
		t.Fatalf("head of %d bytes, want the %d-byte body", len(head), len(body))
	}
}

// TestHeadOnResumeFromDisk: a resumed run whose durable bytes cover the head reads it
// from the part file for its first event; a run paused before the head has none.
func TestHeadOnResumeFromDisk(t *testing.T) {
	t.Parallel()
	so := testserver.Options{Size: 4 << 20, Seed: 8, ETag: `"r1"`, Throttle: 1 << 20, Prefix: pePrefix()}
	h := newHarness(t, so, resumeOpts())
	d := h.add("/r.bin", "", 1)

	h.srv.Hold(16 << 10) // the first run stops before the head is complete
	ctx, cancel := context.WithCancel(context.Background())
	var first headLog
	done := make(chan error, 1)
	go func() {
		done <- h.eng.Run(ctx, d.ID, nil, func(p Progress) {
			first.progress(p)
			if p.Received >= 8<<10 {
				cancel()
			}
		})
	}()
	if err := awaitRun(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("first run = %v", err)
	}
	h.srv.Release()
	if len(first.heads) != 0 {
		t.Fatalf("a run paused before %d bytes had %d heads", HeadBytes, len(first.heads))
	}

	// Second run: paused once well past the head, so its durable bytes cover it.
	if err := pauseMidway(t, h, d.ID, 512<<10); !errors.Is(err, context.Canceled) {
		t.Fatalf("second run = %v", err)
	}
	var segDurable int64
	for _, s := range h.st.segs(d.ID) {
		if s.Start == 0 {
			segDurable = s.Durable
		}
	}
	if segDurable < HeadBytes {
		t.Fatalf("durable prefix %d after the second run", segDurable)
	}

	var third headLog
	if err := h.run(d.ID, nil, third.progress); err != nil {
		t.Fatalf("third run: %v", err)
	}
	head := third.only(t)
	if third.first != 1 {
		t.Fatalf("head in event %d, want the first", third.first)
	}
	if !bytes.Equal(head, testserver.Content(so)[:HeadBytes]) {
		t.Fatal("resumed head differs from the served bytes")
	}
	h.verifyPrefixed(d.ID)
}

func TestNameControlsFromDisposition(t *testing.T) {
	t.Parallel()
	so := testserver.Options{Size: 1000, Seed: 9, Filename: "invoice‮fdp.exe"}
	h := newHarness(t, so, fastOpts())
	d := h.add("/x", "", 1)
	if err := h.run(d.ID, nil, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := h.st.get(t, d.ID)
	if !got.NameControls || strings.ContainsRune(got.Filename, '‮') || got.Filename != "invoicefdp.exe" {
		t.Fatalf("record name %q controls %v", got.Filename, got.NameControls)
	}
	plain := newHarness(t, testserver.Options{Size: 1000, Seed: 9, Filename: "report.pdf"}, fastOpts())
	d2 := plain.add("/x", "", 1)
	if err := plain.run(d2.ID, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := plain.st.get(t, d2.ID); got.NameControls {
		t.Fatal("a plain name has no controls")
	}
}

// verifyPrefixed checks the completed file against the served bytes (prefix included).
func (h *harness) verifyPrefixed(id model.DownloadID) {
	h.t.Helper()
	d := h.st.get(h.t, id)
	if d.State != model.StateCompleted {
		h.t.Fatalf("state = %v (%s)", d.State, d.Error)
	}
	got, err := os.ReadFile(filepath.Join(d.Dir, d.Filename))
	if err != nil {
		h.t.Fatal(err)
	}
	if !bytes.Equal(got, testserver.Content(h.srv.Options())) {
		h.t.Fatal("file differs from the served bytes")
	}
}
