package mediajob

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"downex/internal/transport"
)

// Warm slots (docs/MEDIA-SPEC.md §7): Run keeps its transport slots for the whole job.
// The manifest's slot becomes fetch slot 1, and fetch worker k of every phase uses slot
// k, so a job opens no more connections than MaxConnections.

// poolServer starts a test server over TLS (HTTP/2 when h2) that counts the TCP
// connections it accepts.
func poolServer(t *testing.T, h2 bool) (*server, *atomic.Int32) {
	t.Helper()
	s := newUnstartedServer()
	conns := new(atomic.Int32)
	s.ts.Config.ConnState = func(_ net.Conn, st http.ConnState) {
		if st == http.StateNew {
			conns.Add(1)
		}
	}
	s.ts.Config.ErrorLog = log.New(io.Discard, "", 0) // cancelled streams
	s.ts.EnableHTTP2 = h2
	s.ts.StartTLS()
	t.Cleanup(s.ts.Close)
	return s, conns
}

// poolFactory is the daemon's transport (DefaultHTTPOptions) trusting s.
func poolFactory(t *testing.T, s *server) transport.Factory {
	t.Helper()
	roots := x509.NewCertPool()
	roots.AddCert(s.ts.Certificate())
	o := transport.DefaultHTTPOptions()
	o.Proxy = "direct"
	f, err := transport.NewHTTPFactoryWithRoots(o, roots)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// poolTSJob serves a TS video variant with a separate TS audio rendition of n segments
// each under dir; it returns the selection and the output fakeAssembler makes of it.
func poolTSJob(s *server, dir string, n int) (Selection, []byte) {
	s.add(dir+"master.m3u8", []byte(masterTSAudio), hlsType)
	v := addNamedPlaylist(s, dir+"video/", ".ts", n, 31)
	a := addNamedPlaylist(s, dir+"audio/", ".ts", n, 32)
	return Selection{ManifestURL: s.url(dir + "master.m3u8")}, append(v, a...)
}

func poolKinds(t *testing.T, fn func(t *testing.T, h2 bool)) {
	for _, h2 := range []bool{false, true} {
		t.Run(map[bool]string{false: "h1", true: "h2"}[h2], func(t *testing.T) { fn(t, h2) })
	}
}

// A two-track TS job (manifest, early TS check, then the fragments) opens at most
// MaxConnections connections: the manifest's connection and those of the early phase
// fetch the media fragments too.
func TestPoolConnectionsAcrossPhases(t *testing.T) {
	poolKinds(t, func(t *testing.T, h2 bool) {
		s, conns := poolServer(t, h2)
		sel, want := poolTSJob(s, "/pc/", 20)
		asm := &fakeAssembler{}
		opts := testOptions(t, asm)
		opts.Factory = poolFactory(t, s)
		opts.MaxConnections = 8
		c, err := Run(testContext(t), opts, sel, nil, nil, nil)
		if err != nil || c != "mp4" {
			t.Fatalf("Run = %q, %v", c, err)
		}
		if got := readFile(t, opts.OutPath); !bytes.Equal(got, want) {
			t.Fatalf("output: %d bytes, want %d", len(got), len(want))
		}
		if n := conns.Load(); n > int32(opts.MaxConnections) {
			t.Errorf("%d TCP connections for %d slots", n, opts.MaxConnections)
		} else {
			t.Logf("%d TCP connections", n)
		}
		for i := range 20 {
			for _, tr := range []string{"video", "audio"} {
				if hits := s.hitCount(fmt.Sprintf("/pc/%s/seg%d.ts", tr, i)); hits != 1 {
					t.Fatalf("%s segment %d requested %d times", tr, i, hits)
				}
			}
		}
	})
}

// One connection is enough for a job of MaxConnections 1: the manifest's slot fetches
// every fragment.
func TestPoolSingleConnection(t *testing.T) {
	poolKinds(t, func(t *testing.T, h2 bool) {
		s, conns := poolServer(t, h2)
		sel, want := poolTSJob(s, "/p1/", 6)
		opts := testOptions(t, &fakeAssembler{})
		opts.Factory = poolFactory(t, s)
		opts.MaxConnections = 1
		if _, err := Run(testContext(t), opts, sel, nil, nil, nil); err != nil {
			t.Fatal(err)
		}
		if got := readFile(t, opts.OutPath); !bytes.Equal(got, want) {
			t.Fatalf("output: %d bytes, want %d", len(got), len(want))
		}
		if n := conns.Load(); n != 1 {
			t.Errorf("%d TCP connections, want 1", n)
		}
	})
}

// Pausing and resuming over warm slots keeps the rules: the paused job assembles
// nothing, its checkpoint lists only durable fragments, the resumed job fetches only the
// others and its output is byte for byte the uninterrupted one.
func TestPoolPauseResume(t *testing.T) {
	poolKinds(t, func(t *testing.T, h2 bool) {
		const n = 20
		s, conns := poolServer(t, h2)
		sel, want := poolTSJob(s, "/pp/", n)
		ctx, cancel := context.WithCancel(testContext(t))
		defer cancel()
		var once sync.Once
		s.setHook(func(r *http.Request) {
			if r.URL.Path == "/pp/video/seg12.ts" {
				once.Do(func() {
					cancel() // pause while segment 12 is in flight
					select {
					case <-r.Context().Done():
					case <-time.After(10 * time.Second):
					}
				})
			}
		})
		asm := &fakeAssembler{}
		opts := testOptions(t, asm)
		opts.Factory = poolFactory(t, s)
		opts.MaxConnections = 8
		var rec recorder
		if _, err := Run(ctx, opts, sel, nil, rec.onProgress, rec.onCheckpoint); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		cp := rec.last()
		if cp == nil || len(cp.Tracks) != 2 || asm.callCount() != 0 {
			t.Fatalf("checkpoint %+v, %d assemblies", cp, asm.callCount())
		}
		if bitSet(cp.Tracks[0].Done, 12) {
			t.Fatal("the segment in flight is recorded durable")
		}
		s.setHook(nil)
		hits := func(tr string, i int) int { return s.hitCount(fmt.Sprintf("/pp/%s/seg%d.ts", tr, i)) }
		before := make(map[string]int)
		for i := range n {
			for _, tr := range []string{"video", "audio"} {
				before[fmt.Sprint(tr, i)] = hits(tr, i)
			}
		}
		conns.Store(0)
		if _, err := Run(testContext(t), opts, sel, cp, nil, nil); err != nil {
			t.Fatal(err)
		}
		if got := readFile(t, opts.OutPath); !bytes.Equal(got, want) {
			t.Fatalf("output: %d bytes, want %d", len(got), len(want))
		}
		for k, tr := range []string{"video", "audio"} {
			for i := range n {
				extra := hits(tr, i) - before[fmt.Sprint(tr, i)]
				switch durable := bitSet(cp.Tracks[k].Done, i); {
				case durable && extra != 0:
					t.Errorf("durable %s segment %d fetched again", tr, i)
				case !durable && extra != 1:
					t.Errorf("%s segment %d fetched %d times on resume, want 1", tr, i, extra)
				}
			}
		}
		if c := conns.Load(); c > int32(opts.MaxConnections) {
			t.Errorf("the resumed job opened %d TCP connections for %d slots", c, opts.MaxConnections)
		}
	})
}

// countedHost is a loopback origin serving fixed files that counts, server side, the
// TCP connections open to it. Its fields are guarded by the mutex all hosts of a test
// share.
type countedHost struct {
	name  string
	ts    *httptest.Server
	mu    *sync.Mutex
	open  map[net.Conn]bool
	files map[string][]byte
	hook  func(path string) // called without mu held, before the file is served
}

func newCountedHost(t *testing.T, name string, mu *sync.Mutex) *countedHost {
	t.Helper()
	h := &countedHost{name: name, mu: mu, open: make(map[net.Conn]bool), files: make(map[string][]byte)}
	h.ts = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		data, ok := h.files[r.URL.Path]
		hook := h.hook
		h.mu.Unlock()
		if hook != nil {
			hook(r.URL.Path)
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
	}))
	h.ts.Config.ConnState = func(c net.Conn, st http.ConnState) {
		h.mu.Lock()
		defer h.mu.Unlock()
		switch st {
		case http.StateNew:
			h.open[c] = true
		case http.StateClosed, http.StateHijacked:
			delete(h.open, c)
		}
	}
	h.ts.Start()
	t.Cleanup(h.ts.Close)
	return h
}

// The warm slots never hold more TCP connections than MaxConnections: a slot that
// fetched the playlist (or an initialization segment) from another host than the media
// segments' drops that idle connection before it fetches them. Each segment request
// waits up to 1 s for the client's closes to reach the servers before it counts the
// open connections of all hosts.
func TestPoolDropsConnectionsToOtherHosts(t *testing.T) {
	for _, tc := range []struct {
		name     string
		initHost bool // fMP4 with its EXT-X-MAP on a third host, else TS
		maxConns int
		segs     int
	}{
		{"ts, 1 connection", false, 1, 4},
		{"ts, 4 connections", false, 4, 12},
		{"fmp4 with an init host, 1 connection", true, 1, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			m := newCountedHost(t, "playlist", &mu)
			c := newCountedHost(t, "segments", &mu)
			hosts := []*countedHost{m, c}
			mu.Lock() // the handlers read what follows under mu
			var initURL string
			if tc.initHost {
				i := newCountedHost(t, "init", &mu)
				hosts = append(hosts, i)
				i.files["/init.mp4"] = initSegment("v", false)
				initURL = i.ts.URL + "/init.mp4"
			}
			count := func() (n int, per string) { // under mu
				for _, h := range hosts {
					n += len(h.open)
					per += fmt.Sprintf(" %s=%d", h.name, len(h.open))
				}
				return n, per
			}
			worst, worstAt := 0, ""
			c.hook = func(path string) {
				for deadline := time.Now().Add(time.Second); ; time.Sleep(10 * time.Millisecond) {
					mu.Lock()
					n, per := count()
					if n <= tc.maxConns || time.Now().After(deadline) {
						if n > worst {
							worst, worstAt = n, path+":"+per
						}
						mu.Unlock()
						return
					}
					mu.Unlock()
				}
			}
			var names []string
			for k := range tc.segs {
				p := fmt.Sprintf("/seg%d", k)
				if tc.initHost {
					c.files[p] = mediaSegment(uint64(500+k), 300)
				} else {
					c.files[p] = tsSegment(uint64(70+k), 40)
				}
				names = append(names, c.ts.URL+p)
			}
			m.files["/index.m3u8"] = []byte(mediaPlaylist(initURL, names))
			mu.Unlock()
			opts := testOptions(t, &fakeAssembler{})
			opts.MaxConnections = tc.maxConns
			if _, err := Run(testContext(t), opts, Selection{ManifestURL: m.ts.URL + "/index.m3u8"}, nil, nil, nil); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if worst > tc.maxConns {
				t.Errorf("%d TCP connections open while fetching segments, MaxConnections %d (at %s)", worst, tc.maxConns, worstAt)
			}
		})
	}
}

// fakeSlot answers every request with an empty 200 from final (from the request's URL
// when final is "") and counts its Close calls.
type fakeSlot struct {
	final  string
	closes int
}

func (f *fakeSlot) Fetch(_ context.Context, req transport.RangeRequest) (*transport.Response, error) {
	u := req.URL
	if f.final != "" {
		u = f.final
	}
	return &transport.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(nil)), FinalURL: u}, nil
}

func (f *fakeSlot) Close() { f.closes++ }

type fakeSlots struct{ made []*fakeSlot }

func (fs *fakeSlots) NewSlot() transport.Fetcher {
	f := &fakeSlot{}
	fs.made = append(fs.made, f)
	return f
}

// TestWarmPool pins which slots keep their idle connections into a phase (MEDIA-SPEC
// §7): those the phase uses whose hosts, final hosts after redirects included, are all
// hosts of the phase. A slot the phase does not use, or that reached another host,
// drops them; slots the phase lacks are added.
func TestWarmPool(t *testing.T) {
	fs := &fakeSlots{}
	r := &runner{o: Options{Factory: fs}}
	for range 4 {
		r.pool = append(r.pool, newWarmSlot(fs.NewSlot()))
	}
	fs.made[2].final = "http://edge.test/seg1"
	for k, u := range []string{
		"http://cdn.test/seg0",        // a host of the phase: kept
		"http://playlist.test/a.m3u8", // another host: dropped
		"http://cdn.test/seg1",        // redirected to another host: dropped
		"http://cdn.test/seg2",        // not used by the phase: dropped
	} {
		resp, err := r.pool[k].Fetch(context.Background(), transport.RangeRequest{URL: u})
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}
	phase := map[string]bool{"cdn.test": true}
	r.warmPool(3, phase)
	for k, want := range []int{0, 1, 1, 1} {
		if got := fs.made[k].closes; got != want {
			t.Errorf("slot %d closed %d times, want %d", k, got, want)
		}
	}
	// A dropped slot has no hosts left; a bigger phase adds slots.
	r.warmPool(6, phase)
	for k, want := range []int{0, 1, 1, 1} {
		if got := fs.made[k].closes; got != want {
			t.Errorf("after the second phase: slot %d closed %d times, want %d", k, got, want)
		}
	}
	if len(r.pool) != 6 || len(fs.made) != 6 {
		t.Errorf("%d slots in the pool, %d made; want 6", len(r.pool), len(fs.made))
	}
	r.closePool()
	for k, f := range fs.made {
		if f.closes == 0 {
			t.Errorf("slot %d not closed when the pool closed", k)
		}
	}
}
