package tools

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// rangeServer serves body at every path with byte ranges (http.ServeContent) and counts
// the requests: all of them, and those that asked for a range. cut, when set, ends a
// response early: it gets the request number (1-based) and the range start and returns
// how many bytes to send before the connection is broken (-1 = all).
type rangeServer struct {
	srv    *httptest.Server
	body   []byte
	all    atomic.Int64
	ranged atomic.Int64
	mu     sync.Mutex
	starts []int64
	cut    func(n int64, start int64) int64
}

func newRangeServer(t *testing.T, body []byte, ranges bool) *rangeServer {
	t.Helper()
	rs := &rangeServer{body: body}
	rs.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := rs.all.Add(1)
		start := int64(0)
		if h := r.Header.Get("Range"); h != "" && ranges {
			rs.ranged.Add(1)
			if _, err := fmt.Sscanf(h, "bytes=%d-", &start); err != nil {
				http.Error(w, "bad range", http.StatusBadRequest)
				return
			}
		}
		rs.mu.Lock()
		rs.starts = append(rs.starts, start)
		rs.mu.Unlock()
		if !ranges {
			r.Header.Del("Range")
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			rs.write(w, n, 0, body)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		if rs.cut == nil {
			http.ServeContent(w, r, "f.bin", time.Time{}, bytes.NewReader(body))
			return
		}
		cw := &cutWriter{ResponseWriter: w, left: rs.cut(n, start)}
		http.ServeContent(cw, r, "f.bin", time.Time{}, bytes.NewReader(body))
	}))
	rs.srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	t.Cleanup(rs.srv.Close)
	return rs
}

func (rs *rangeServer) write(w http.ResponseWriter, n, start int64, data []byte) {
	if rs.cut != nil {
		if left := rs.cut(n, start); left >= 0 && left < int64(len(data)) {
			_, _ = w.Write(data[:left])
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		}
	}
	_, _ = w.Write(data)
}

// cutWriter breaks the connection after left bytes of body (left < 0: never).
type cutWriter struct {
	http.ResponseWriter
	left int64
}

func (c *cutWriter) Write(p []byte) (int, error) {
	if c.left < 0 {
		return c.ResponseWriter.Write(p)
	}
	if int64(len(p)) > c.left {
		_, _ = c.ResponseWriter.Write(p[:c.left])
		c.ResponseWriter.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}
	c.left -= int64(len(p))
	return c.ResponseWriter.Write(p)
}

func testBody(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7 + i/251)
	}
	return b
}

func fastFetcher(srv *httptest.Server) *fetcher {
	f := newTestFetcher(srv)
	f.retryDelay = time.Millisecond
	return f
}

// A large file that the server serves in ranges comes over several connections at once.
func TestFetchDownloadParallel(t *testing.T) {
	body := testBody(9<<20 + 12345)
	rs := newRangeServer(t, body, true)
	f := fastFetcher(rs.srv)
	dir := t.TempDir()
	var last, total atomic.Int64
	p, sum, n, err := f.download(context.Background(), rs.srv.URL+"/f.bin", 64<<20, dir, func(d, tt int64) { last.Store(d); total.Store(tt) })
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if n != int64(len(body)) || sum != [32]byte(sumOf(body)) || last.Load() != n || total.Load() != n {
		t.Fatalf("download: %d bytes, progress %d/%d", n, last.Load(), total.Load())
	}
	if got := readAll(t, p); !bytes.Equal(got, body) {
		t.Fatal("file differs from the served body")
	}
	if rs.ranged.Load() < 2 {
		t.Errorf("%d ranged requests, want a download over several connections", rs.ranged.Load())
	}
	if rs.all.Load() > int64(maxFetchParts)+1 {
		t.Errorf("%d requests, want at most %d parts plus the first", rs.all.Load(), maxFetchParts)
	}
}

// A small file stays one request.
func TestFetchDownloadSmallIsOneRequest(t *testing.T) {
	body := testBody(300 << 10)
	rs := newRangeServer(t, body, true)
	f := fastFetcher(rs.srv)
	p, sum, _, err := f.download(context.Background(), rs.srv.URL+"/f.bin", 64<<20, t.TempDir(), nil)
	if err != nil || sum != [32]byte(sumOf(body)) {
		t.Fatalf("download: %v", err)
	}
	os.Remove(p)
	if rs.all.Load() != 1 {
		t.Errorf("%d requests for a small file, want 1", rs.all.Load())
	}
}

// Broken connections are resumed where they stopped: every response is cut after about
// a third of its part, yet the file arrives whole and nothing is fetched twice in full.
func TestFetchDownloadResumesBrokenConnections(t *testing.T) {
	for _, size := range []int{9<<20 + 777, 400 << 10} { // several parts, and one
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			body := testBody(size)
			rs := newRangeServer(t, body, true)
			var broken atomic.Int64
			rs.cut = func(n, start int64) int64 {
				if broken.Load() >= 12 {
					return -1
				}
				broken.Add(1)
				return 128 << 10
			}
			f := fastFetcher(rs.srv)
			dir := t.TempDir()
			p, sum, n, err := f.download(context.Background(), rs.srv.URL+"/f.bin", 64<<20, dir, nil)
			if err != nil {
				t.Fatalf("download after %d broken connections: %v", broken.Load(), err)
			}
			if n != int64(size) || sum != [32]byte(sumOf(body)) || !bytes.Equal(readAll(t, p), body) {
				t.Fatalf("resumed file differs (%d bytes)", n)
			}
			// Every retry continued past where its broken response stopped.
			rs.mu.Lock()
			defer rs.mu.Unlock()
			seen := map[int64]bool{}
			for _, s := range rs.starts {
				seen[s] = true
			}
			resumed := 0
			for _, s := range rs.starts {
				if seen[s-128<<10] {
					resumed++
				}
			}
			if resumed == 0 {
				t.Errorf("no request resumed mid-part: starts %v", rs.starts)
			}
		})
	}
}

// A part that keeps breaking gives up after its retries: a network error, no file left.
func TestFetchDownloadGivesUp(t *testing.T) {
	body := testBody(2 << 20)
	rs := newRangeServer(t, body, true)
	rs.cut = func(n, start int64) int64 { return 0 }
	f := fastFetcher(rs.srv)
	dir := t.TempDir()
	_, _, _, err := f.download(context.Background(), rs.srv.URL+"/f.bin", 64<<20, dir, nil)
	if ErrorCode(err) != CodeNetwork {
		t.Fatalf("download of a server that always breaks: %v (code %q)", err, ErrorCode(err))
	}
	if got := rs.all.Load(); got > int64(1+fetchRetries)*int64(maxFetchParts) {
		t.Errorf("%d requests, more than the retries allow", got)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("partial files left: %v", entries)
	}
}

// A server without ranges: one stream, and a break is a failure (nothing to resume from).
func TestFetchDownloadWithoutRanges(t *testing.T) {
	body := testBody(9 << 20)
	rs := newRangeServer(t, body, false)
	f := fastFetcher(rs.srv)
	dir := t.TempDir()
	p, sum, _, err := f.download(context.Background(), rs.srv.URL+"/f.bin", 64<<20, dir, nil)
	if err != nil || sum != [32]byte(sumOf(body)) {
		t.Fatalf("download without ranges: %v", err)
	}
	os.Remove(p)
	if rs.ranged.Load() != 0 || rs.all.Load() != 1 {
		t.Errorf("%d requests (%d ranged), want 1", rs.all.Load(), rs.ranged.Load())
	}
	rs.cut = func(n, start int64) int64 { return 1 << 20 }
	if _, _, _, err := f.download(context.Background(), rs.srv.URL+"/f.bin", 64<<20, dir, nil); ErrorCode(err) != CodeNetwork {
		t.Errorf("broken download without ranges: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("partial files left: %v", entries)
	}
}

// A ranged answer that does not match what was asked (another start, another size) is
// refused, never stitched into the file.
func TestFetchDownloadRefusesMismatchedRanges(t *testing.T) {
	body := testBody(6 << 20)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "" {
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			_, _ = w.Write(body)
			return
		}
		// Always the first MiB of a larger file, whatever was asked.
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", 1<<20-1, len(body)+1))
		w.Header().Set("Content-Length", strconv.Itoa(1<<20))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(body[:1<<20])
	}))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	defer srv.Close()
	f := fastFetcher(srv)
	dir := t.TempDir()
	if _, _, _, err := f.download(context.Background(), srv.URL+"/f.bin", 64<<20, dir, nil); err == nil {
		t.Fatal("mismatched ranges were accepted")
	} else if strings.Contains(err.Error(), "127.0.0.1") {
		t.Errorf("error names the address: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("partial files left: %v", entries)
	}
}

// Cancelling the install ends every connection and leaves no file.
func TestFetchDownloadCancel(t *testing.T) {
	body := testBody(9 << 20)
	rs := newRangeServer(t, body, true)
	release := make(chan struct{})
	rs.cut = func(n, start int64) int64 { <-release; return -1 }
	f := fastFetcher(rs.srv)
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, _, _, err := f.download(ctx, rs.srv.URL+"/f.bin", 64<<20, dir, nil)
		errc <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-errc:
		if err == nil {
			t.Error("a cancelled download succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("download did not end after cancel")
	}
	close(release)
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("partial files left: %v", entries)
	}
}

// Over HTTP/2 the parts still use connections of their own: one shared connection would
// carry every part's bytes through one congestion window and break them all at once.
func TestFetchDownloadPartsOwnConnections(t *testing.T) {
	body := testBody(9 << 20)
	var conns atomic.Int64
	var h2 atomic.Int64
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 {
			h2.Add(1)
		}
		http.ServeContent(w, r, "f.bin", time.Time{}, bytes.NewReader(body))
	}))
	srv.EnableHTTP2 = true
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	defer srv.Close()
	f := fastFetcher(srv)
	p, sum, _, err := f.download(context.Background(), srv.URL+"/f.bin", 64<<20, t.TempDir(), nil)
	if err != nil || sum != [32]byte(sumOf(body)) {
		t.Fatalf("download over HTTP/2: %v", err)
	}
	os.Remove(p)
	if h2.Load() == 0 {
		t.Fatal("the test server did not speak HTTP/2")
	}
	if got := conns.Load(); got < maxFetchParts {
		t.Errorf("%d connections for %d parts over HTTP/2, want one per part", got, maxFetchParts)
	}
}

// When a part fails for good, the download ends at once, also while part 0 waits on a
// quiet first response (it is not left to the stall watchdog).
func TestFetchDownloadFailsFastWhilePartZeroWaits(t *testing.T) {
	quiet := make(chan struct{})
	defer close(quiet)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "" {
			http.Error(w, "gone", http.StatusForbidden)
			return
		}
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Length", strconv.Itoa(8<<20))
		_, _ = w.Write(make([]byte, 1024))
		w.(http.Flusher).Flush()
		select {
		case <-quiet:
		case <-r.Context().Done():
		}
	}))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	defer srv.Close()
	f := &fetcher{client: secureClient(srv.Client()), headerTimeout: 5 * time.Second, stallWindow: 10 * time.Second,
		stallMinBytes: 1, retryDelay: time.Millisecond}
	dir := t.TempDir()
	start := time.Now()
	_, _, _, err := f.download(context.Background(), srv.URL+"/f.bin", 64<<20, dir, nil)
	if ErrorCode(err) != CodeHTTPStatus {
		t.Fatalf("download: %v (code %q), want the parts' HTTP 403", err, ErrorCode(err))
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("the failure took %v to come back", d)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("partial files left: %v", entries)
	}
}
