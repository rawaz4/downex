package transport

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"downex/internal/model"
)

// HTTP/2 health check (HTTPOptions.HTTP2HealthCheck): a connection that receives no
// frame for the check's time sends a PING and is closed when no answer arrives within
// the same time, so a black-holed connection fails its reads instead of hanging, and
// the slot's next request dials anew.

// healthRelay is a TCP relay to target whose current connections can be frozen: a
// frozen connection stays open but forwards nothing more in either direction (a
// black-holed path). Connections accepted later forward normally.
type healthRelay struct {
	ln     net.Listener
	target string

	mu    sync.Mutex
	conns []*healthConn
}

type healthConn struct {
	client, upstream net.Conn
	frozen           chan struct{}
	once             sync.Once
}

func (c *healthConn) freeze() { c.once.Do(func() { close(c.frozen) }) }

func (c *healthConn) close() {
	_ = c.client.Close()
	_ = c.upstream.Close()
}

func newHealthRelay(t *testing.T, target string) *healthRelay {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := &healthRelay{ln: ln, target: target}
	go r.accept()
	t.Cleanup(func() {
		_ = ln.Close()
		r.mu.Lock()
		defer r.mu.Unlock()
		for _, c := range r.conns {
			c.close()
		}
	})
	return r
}

func (r *healthRelay) addr() string { return r.ln.Addr().String() }

func (r *healthRelay) accept() {
	for {
		client, err := r.ln.Accept()
		if err != nil {
			return
		}
		upstream, err := net.Dial("tcp", r.target)
		if err != nil {
			_ = client.Close()
			continue
		}
		c := &healthConn{client: client, upstream: upstream, frozen: make(chan struct{})}
		r.mu.Lock()
		r.conns = append(r.conns, c)
		r.mu.Unlock()
		go c.pipe(upstream, client)
		go c.pipe(client, upstream)
	}
}

// pipe copies src to dst until either fails; once frozen it reads on but delivers
// nothing.
func (c *healthConn) pipe(dst, src net.Conn) {
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			select {
			case <-c.frozen:
			default:
				if _, werr := dst.Write(buf[:n]); werr != nil {
					return
				}
			}
		}
		if err != nil {
			return
		}
	}
}

// freeze black-holes every connection accepted so far.
func (r *healthRelay) freeze() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.conns {
		c.freeze()
	}
}

func (r *healthRelay) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.conns)
}

// healthFactory is a factory trusting ts with the given health check.
func healthFactory(t *testing.T, check time.Duration, ts ...*httptest.Server) *HTTPFactory {
	t.Helper()
	o := DefaultHTTPOptions()
	o.Proxy = "direct"
	o.HTTP2HealthCheck = check
	f, err := newHTTPFactory(o, trustPool(ts...))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// readAllWithin reads body to its end in the background and returns what the read
// returned, or ok = false when it is still blocked after d (the body is then closed).
func readAllWithin(body io.ReadCloser, d time.Duration) (n int, err error, elapsed time.Duration, ok bool) {
	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	start := time.Now()
	go func() {
		b, err := io.ReadAll(body)
		done <- result{len(b), err}
	}()
	select {
	case r := <-done:
		return r.n, r.err, time.Since(start), true
	case <-time.After(d):
		_ = body.Close()
		<-done
		return 0, nil, time.Since(start), false
	}
}

// healthHandler serves "/small" at once; any other path sends 32 KiB, then waits for
// the client to go away (a live server whose bytes the relay stops delivering).
func healthHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/small" {
		_, _ = io.WriteString(w, "small")
		return
	}
	_, _ = w.Write(make([]byte, 32<<10))
	w.(http.Flusher).Flush()
	<-r.Context().Done()
}

func TestHTTP2HealthCheckClosesBlackHoledConnection(t *testing.T) {
	ts := startServer(t, kindHTTP2TLS, http.HandlerFunc(healthHandler))
	for _, tc := range []struct {
		name  string
		check time.Duration
		fails bool
	}{
		{"enabled", 200 * time.Millisecond, true},
		{"disabled", -1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			relay := newHealthRelay(t, ts.Listener.Addr().String())
			f := healthFactory(t, tc.check, ts)
			slot := f.NewSlot()
			defer slot.Close()
			ctx := testCtx(t)
			resp, err := slot.Fetch(ctx, RangeRequest{Request: model.Request{URL: "https://" + relay.addr() + "/big"}, End: -1})
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if _, err := io.ReadFull(resp.Body, make([]byte, 32<<10)); err != nil {
				t.Fatalf("first bytes: %v", err)
			}
			relay.freeze()
			_, err, elapsed, returned := readAllWithin(resp.Body, 3*time.Second)
			switch {
			case tc.fails && !returned:
				t.Fatal("the read of a black-holed HTTP/2 connection still blocks after 3 s")
			case tc.fails && err == nil:
				t.Fatal("the read of a black-holed HTTP/2 connection succeeded")
			case tc.fails && elapsed > 2*time.Second:
				t.Errorf("the read failed after %v, want about 0.4 s (PING after 200 ms, no answer in 200 ms)", elapsed)
			case !tc.fails && returned:
				t.Fatalf("without a health check the read returned after %v: %v", elapsed, err)
			}
			if !tc.fails {
				return
			}
			// The slot's next request dials a new connection (the relay forwards it).
			before := relay.count()
			resp2, err := slot.Fetch(ctx, RangeRequest{Request: model.Request{URL: "https://" + relay.addr() + "/small"}, End: -1})
			if err != nil {
				t.Fatalf("next request on the slot: %v", err)
			}
			if got := string(readBody(t, resp2)); got != "small" {
				t.Fatalf("next request read %q", got)
			}
			if relay.count() != before+1 {
				t.Errorf("the next request used %d new connections, want 1", relay.count()-before)
			}
			// So does a request on a new slot.
			other := f.NewSlot()
			defer other.Close()
			resp3, err := other.Fetch(ctx, RangeRequest{Request: model.Request{URL: "https://" + relay.addr() + "/small"}, End: -1})
			if err != nil {
				t.Fatalf("request on a new slot: %v", err)
			}
			if got := string(readBody(t, resp3)); got != "small" {
				t.Fatalf("new slot read %q", got)
			}
		})
	}
}

// A live server is never closed by the check: not while it sends a frame more often
// than the check's time, and not when it pauses longer but answers the PING.
func TestHTTP2HealthCheckKeepsLiveConnection(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind string
		gap  time.Duration
	}{
		{"h2 frames every 150ms", kindHTTP2TLS, 150 * time.Millisecond},
		{"h2 pauses answered by PING", kindHTTP2TLS, 500 * time.Millisecond},
		{"h1 unaffected", kindHTTP1TLS, 500 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const chunks = 6
			var proto int
			var mu sync.Mutex
			ts := startServer(t, tc.kind, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				proto = r.ProtoMajor
				mu.Unlock()
				for i := range chunks {
					if i > 0 {
						select {
						case <-time.After(tc.gap):
						case <-r.Context().Done():
							return
						}
					}
					_, _ = w.Write([]byte(strings.Repeat("x", 1024)))
					w.(http.Flusher).Flush()
				}
			}))
			f := healthFactory(t, 200*time.Millisecond, ts)
			slot := f.NewSlot()
			defer slot.Close()
			resp, err := slot.Fetch(testCtx(t), RangeRequest{Request: model.Request{URL: ts.URL + "/live"}, End: -1})
			if err != nil {
				t.Fatal(err)
			}
			n, err, _, returned := readAllWithin(resp.Body, 20*time.Second)
			if !returned || err != nil || n != chunks*1024 {
				t.Fatalf("read %d bytes, %v (returned %v), want %d", n, err, returned, chunks*1024)
			}
			mu.Lock()
			defer mu.Unlock()
			want := 2
			if tc.kind == kindHTTP1TLS {
				want = 1
			}
			if proto != want {
				t.Errorf("served over HTTP/%d, want HTTP/%d", proto, want)
			}
		})
	}
}

func TestHTTP2HealthCheckOptions(t *testing.T) {
	if got := DefaultHTTPOptions().HTTP2HealthCheck; got != 15*time.Second || DefaultHTTP2HealthCheck != 15*time.Second {
		t.Fatalf("DefaultHTTPOptions().HTTP2HealthCheck = %v, DefaultHTTP2HealthCheck = %v, want 15s", got, DefaultHTTP2HealthCheck)
	}
	for _, tc := range []struct {
		name  string
		check time.Duration
		want  time.Duration // 0: no health check
	}{
		{"default", DefaultHTTP2HealthCheck, 15 * time.Second},
		{"zero selects the default", 0, 15 * time.Second},
		{"custom", 3 * time.Second, 3 * time.Second},
		{"negative disables", -1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := DefaultHTTPOptions()
			o.HTTP2HealthCheck = tc.check
			f, err := NewHTTPFactory(o)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want > 0 && f.Options().HTTP2HealthCheck != tc.want {
				t.Errorf("Options().HTTP2HealthCheck = %v, want %v", f.Options().HTTP2HealthCheck, tc.want)
			}
			a, b := f.NewSlot().(*httpFetcher), f.NewSlot().(*httpFetcher)
			for _, tr := range []*http.Transport{f.base, a.transport, b.transport} {
				switch {
				case tc.want == 0 && tr.HTTP2 != nil && (tr.HTTP2.SendPingTimeout != 0 || tr.HTTP2.PingTimeout != 0):
					// net/http itself fills in an empty HTTP2Config when it sets HTTP/2 up.
					t.Fatalf("HTTP2 = %+v, want no health check", *tr.HTTP2)
				case tc.want == 0:
				case tr.HTTP2 == nil:
					t.Fatal("no HTTP2 configuration")
				case tr.HTTP2.SendPingTimeout != tc.want || tr.HTTP2.PingTimeout != tc.want:
					t.Fatalf("HTTP2 = %+v, want PINGs after %v answered within %v", *tr.HTTP2, tc.want, tc.want)
				case tr.HTTP2.MaxReceiveBufferPerStream != 0 || tr.HTTP2.MaxReceiveBufferPerConnection != 0:
					t.Fatalf("HTTP2 = %+v changes the flow-control windows", *tr.HTTP2)
				}
			}
			if tc.want > 0 && (a.transport.HTTP2 == b.transport.HTTP2 || a.transport.HTTP2 == f.base.HTTP2) {
				t.Error("slots share one HTTP2 configuration")
			}
		})
	}
}
