package transport

import (
	"bytes"
	"context"
	"crypto/x509"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// Server flavours exercised by the fetch tests.
const (
	kindHTTP1    = "http1"
	kindHTTP1TLS = "http1-tls"
	kindHTTP2TLS = "http2-tls"
)

var allKinds = []string{kindHTTP1, kindHTTP1TLS, kindHTTP2TLS}

// startServer starts an httptest server of the given kind and closes it with the test.
func startServer(t *testing.T, kind string, h http.Handler) *httptest.Server {
	t.Helper()
	ts := httptest.NewUnstartedServer(h)
	// Expected failures (rejected certificates, aborted streams) are logged by the
	// server; keep test output readable.
	ts.Config.ErrorLog = log.New(io.Discard, "", 0)
	switch kind {
	case kindHTTP1:
		ts.Start()
	case kindHTTP1TLS:
		ts.EnableHTTP2 = false
		ts.StartTLS()
	case kindHTTP2TLS:
		ts.EnableHTTP2 = true
		ts.StartTLS()
	default:
		t.Fatalf("unknown server kind %q", kind)
	}
	t.Cleanup(ts.Close)
	return ts
}

// trustPool returns a root pool trusting the certificates of the TLS servers given.
func trustPool(servers ...*httptest.Server) *x509.CertPool {
	pool := x509.NewCertPool()
	for _, s := range servers {
		if s.TLS != nil {
			pool.AddCert(s.Certificate())
		}
	}
	return pool
}

// newTestSlot returns a slot that trusts the given test servers.
func newTestSlot(t *testing.T, opts HTTPOptions, servers ...*httptest.Server) Fetcher {
	t.Helper()
	f, err := newHTTPFactory(opts, trustPool(servers...))
	if err != nil {
		t.Fatalf("newHTTPFactory: %v", err)
	}
	s := f.NewSlot()
	t.Cleanup(s.Close)
	return s
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// testContent returns n deterministic, non-repeating-looking bytes.
func testContent(n int) []byte {
	b := make([]byte, n)
	x := uint32(2463534242)
	for i := range b {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		b[i] = byte(x)
	}
	return b
}

// readBody reads and closes a response body.
func readBody(t *testing.T, r *Response) []byte {
	t.Helper()
	defer r.Body.Close()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return b
}

// seenRequest is what a test handler observed about one request.
type seenRequest struct {
	Method     string
	Proto      int
	Host       string
	RemoteAddr string
	Header     http.Header
	Body       []byte
	URL        string
}

// recorder collects seenRequests from handlers running on server goroutines.
type recorder struct {
	mu   sync.Mutex
	reqs []seenRequest
}

func (rec *recorder) record(r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.reqs = append(rec.reqs, seenRequest{
		Method:     r.Method,
		Proto:      r.ProtoMajor,
		Host:       r.Host,
		RemoteAddr: r.RemoteAddr,
		Header:     r.Header.Clone(),
		Body:       body,
		URL:        r.URL.String(),
	})
}

func (rec *recorder) all() []seenRequest {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return append([]seenRequest(nil), rec.reqs...)
}

func (rec *recorder) last(t *testing.T) seenRequest {
	t.Helper()
	all := rec.all()
	if len(all) == 0 {
		t.Fatal("server saw no request")
	}
	return all[len(all)-1]
}

// contentServer serves a fixed representation with byte ranges, If-Range, a strong
// ETag and Last-Modified (http.ServeContent implements RFC 9110 range semantics).
type contentServer struct {
	content []byte
	etag    string
	modTime time.Time
	ctype   string
	rec     recorder
}

func newContentServer(n int) *contentServer {
	return &contentServer{
		content: testContent(n),
		etag:    `"v1-abc"`,
		modTime: time.Date(2025, 3, 4, 5, 6, 7, 0, time.UTC),
		ctype:   "Application/Zip; charset=binary",
	}
}

func (cs *contentServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cs.rec.record(r)
	w.Header().Set("ETag", cs.etag)
	w.Header().Set("Content-Type", cs.ctype)
	http.ServeContent(w, r, "", cs.modTime, bytes.NewReader(cs.content))
}
