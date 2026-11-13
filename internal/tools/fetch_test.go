package tools

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// newTestFetcher returns a fetcher for srv with the production header and stall
// timeouts: a download that should succeed must not depend on a loaded machine's TLS
// handshake or server writes keeping within a short real-time window.
func newTestFetcher(srv *httptest.Server) *fetcher {
	return &fetcher{client: secureClient(srv.Client()), headerTimeout: headerTimeout, stallWindow: stallWindow, stallMinBytes: 1024}
}

// impatientFetcher returns a fetcher for srv with 300 ms header and stall timeouts, for
// the requests a server never answers or never finishes: no machine is too slow for
// those to time out.
func impatientFetcher(srv *httptest.Server) *fetcher {
	return &fetcher{client: secureClient(srv.Client()), headerTimeout: 300 * time.Millisecond, stallWindow: 300 * time.Millisecond, stallMinBytes: 1024}
}

func TestFetchLimits(t *testing.T) {
	stall := make(chan struct{})
	defer close(stall)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			_, _ = w.Write([]byte("hello world"))
		case "/big":
			w.Header().Set("Content-Length", "100")
			_, _ = w.Write(make([]byte, 100))
		case "/chunked-big":
			w.(http.Flusher).Flush()
			_, _ = w.Write(make([]byte, 100))
		case "/html":
			_, _ = w.Write([]byte("\r\n <!doctype HTML><title>Too many requests</title>"))
		case "/slow-headers":
			select { // no headers until the client gives up
			case <-stall:
			case <-r.Context().Done():
			}
		case "/stall":
			w.Header().Set("Content-Length", "100000")
			_, _ = w.Write(make([]byte, 10))
			w.(http.Flusher).Flush()
			select {
			case <-stall:
			case <-r.Context().Done():
			}
		case "/to-http":
			http.Redirect(w, r, "http://"+r.Host+"/ok", http.StatusFound)
		case "/to-ok":
			http.Redirect(w, r, "/ok", http.StatusFound)
		case "/503":
			http.Error(w, "busy", http.StatusServiceUnavailable)
		default:
			http.NotFound(w, r)
		}
	}))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	defer srv.Close()
	f, impatient := newTestFetcher(srv), impatientFetcher(srv)
	ctx := context.Background()
	if b, err := f.bytes(ctx, srv.URL+"/ok", 64); err != nil || string(b) != "hello world" {
		t.Errorf("ok: %q, %v", b, err)
	}
	if b, err := f.bytes(ctx, srv.URL+"/to-ok", 64); err != nil || string(b) != "hello world" {
		t.Errorf("https redirect: %q, %v", b, err)
	}
	cases := map[string]struct {
		path  string
		code  string
		cause error
	}{
		"announced too large": {"/big", CodeInternal, ErrTooLarge},
		"too large":           {"/chunked-big", CodeInternal, ErrTooLarge},
		"web page":            {"/html", CodeHTTPStatus, nil},
		"header timeout":      {"/slow-headers", CodeNetwork, errHeaderTimeout},
		"stall":               {"/stall", CodeNetwork, errStalled},
		"redirect to http":    {"/to-http", CodeNetwork, errInsecureRedirect},
		"404":                 {"/missing", CodeHTTPStatus, ErrNotFound},
		"503":                 {"/503", CodeHTTPStatus, nil},
	}
	for name, c := range cases {
		limit, fc := int64(64), f
		switch c.path {
		case "/stall":
			limit, fc = 1<<20, impatient
		case "/slow-headers":
			fc = impatient
		}
		_, err := fc.bytes(ctx, srv.URL+c.path, limit)
		if ErrorCode(err) != c.code || c.cause != nil && !errors.Is(err, c.cause) {
			t.Errorf("%s: %v (code %q), want %s/%v", name, err, ErrorCode(err), c.code, c.cause)
		}
		if err != nil && strings.Contains(err.Error(), "127.0.0.1") {
			t.Errorf("%s: error names the address: %v", name, err)
		}
	}
	if _, err := f.bytes(ctx, "http://"+srv.Listener.Addr().String()+"/ok", 64); ErrorCode(err) != CodeNetwork {
		t.Errorf("plain http: %v", err)
	}
	// TLS failures: a client that does not trust the server.
	plain := &fetcher{client: secureClient(&http.Client{}), headerTimeout: 5 * time.Second, stallWindow: 5 * time.Second}
	if _, err := plain.bytes(ctx, srv.URL+"/ok", 64); ErrorCode(err) != CodeTLS {
		t.Errorf("untrusted certificate: %v (code %q)", err, ErrorCode(err))
	}
}

func TestFetchDownload(t *testing.T) {
	body := strings.Repeat("0123456789", 10000)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		if r.URL.Path == "/html" {
			_, _ = w.Write([]byte("<html>" + body[6:]))
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	defer srv.Close()
	f := newTestFetcher(srv)
	dir := t.TempDir()
	var last, total int64
	p, sum, n, err := f.download(context.Background(), srv.URL+"/file", 1<<20, dir, func(d, tt int64) { last, total = d, tt })
	if err != nil || n != int64(len(body)) || sum != sha256Of(body) || last != n || total != n {
		t.Fatalf("download: %s %d %v (progress %d/%d)", p, n, err, last, total)
	}
	if got := readAll(t, p); string(got) != body || !strings.HasSuffix(p, ".part") {
		t.Errorf("file %s", p)
	}
	os.Remove(p)
	for name, c := range map[string]struct {
		path  string
		limit int64
	}{"too large": {"/file", 1000}, "web page": {"/html", 1 << 20}} {
		if _, _, _, err := f.download(context.Background(), srv.URL+c.path, c.limit, dir, nil); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("partial files left: %v", entries)
	}
}

func sha256Of(s string) [32]byte {
	var out [32]byte
	copy(out[:], sumOf([]byte(s)))
	return out
}

func TestIsWebPage(t *testing.T) {
	for in, want := range map[string]bool{
		"<!DOCTYPE html>":       true,
		"\xef\xbb\xbf  <HTML>":  true,
		"\n\t<!doctype html ":   true,
		"MZ\x90\x00":            false,
		"<?xml version=\"1.0\"": false,
		"":                      false,
		"<htm":                  false,
	} {
		if got := isWebPage([]byte(in)); got != want {
			t.Errorf("isWebPage(%q) = %v", in, got)
		}
	}
}
