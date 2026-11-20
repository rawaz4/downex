// Package testserver is an httptest-based HTTP server for engine and transport tests.
// It serves deterministic content (see Byte and Fill) of a configurable size with
// single byte-range support (RFC 9110 §14) and injectable faults: ranges disabled,
// "Accept-Ranges: none", changeable validators, throttling, stalls, 429/503 with
// Retry-After, a per-client connection limit, forced 416, Content-Disposition names,
// signed links that expire (Token) and HTTP/2 over TLS. Hold withholds body bytes past
// a budget until Release, so a test can act on a download midway without racing its
// completion.
package testserver

import (
	"cmp"
	"crypto/sha256"
	"mime"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Options configures a Server. The zero value serves an empty resource with ranges.
type Options struct {
	Size int64  // content length in bytes
	Seed uint64 // content generator seed; change it to simulate a modified file
	// Prefix replaces the generated content from offset 0 (ranges honoured), so tests
	// can serve a file that starts like a program, a web page or an archive followed by
	// deterministic filler. Bytes beyond Size are not served.
	Prefix []byte

	NoRanges         bool // ignore Range headers and always answer 200 with the full body
	AcceptRangesNone bool // send "Accept-Ranges: none" and ignore Range headers
	Always416        bool // answer every range request with 416
	// UnknownTotal answers ranges with an unknown total: "Content-Range: bytes a-b/*".
	UnknownTotal bool

	ETag         string    // ETag header, e.g. `"v1"` or `W/"v1"` (weak); empty = none
	LastModified time.Time // Last-Modified header; zero = none
	Filename     string    // Content-Disposition attachment filename; empty = none
	ContentType  string    // Content-Type; empty = application/octet-stream

	Throttle   int64 // bytes per second per response; 0 = unlimited
	StallAfter int64 // stall a response after this many body bytes; 0 = never
	StallCount int   // how many responses stall (0 = every eligible response)

	FailFirst  int    // answer FailFirst requests (after FailSkip) with FailStatus
	FailSkip   int    // requests served normally before injected failures start
	FailStatus int    // status for injected failures (default 503)
	RetryAfter string // Retry-After header sent with injected failures

	// Limit serves at most Limit requests at once (0 = no limit), as a server with a
	// per-client connection limit does. It refuses the others with FailStatus (default
	// 503, with RetryAfter), or with LimitAbort by closing the connection unanswered.
	Limit      int
	LimitAbort bool

	HTTP2 bool // serve over TLS with HTTP/2 enabled

	// Token makes the server act like one serving signed links (docs/ENGINE-SPEC.md §7.2):
	// when set, a request whose query lacks token=<Token> gets the expiry answer, which is
	// ExpiredStatus (default 403); 401 without WWW-Authenticate with
	// ExpiredNoChallenge401; or with ExpiredRedirect a 302 to /expired, which serves a
	// 200 text/html page. Change it with SetToken to expire the links handed out so far.
	Token                 string
	ExpiredStatus         int
	ExpiredRedirect       bool
	ExpiredNoChallenge401 bool
}

// ExpiredPage is the body of the /expired page (Options.ExpiredRedirect).
const ExpiredPage = "<!doctype html><html><head><title>Link expired</title></head><body>This link has expired.</body></html>"

// URLWithToken returns the server's URL for path with the token query of the current
// Options.Token ("" without one).
func (s *Server) URLWithToken(path string) string {
	u := s.URL + path
	if tok := s.Options().Token; tok != "" {
		u += "?token=" + tok
	}
	return u
}

// Request records one request the server received.
type Request struct {
	Method  string
	Proto   string
	Range   string
	IfRange string
	Status  int // 0: the connection was closed unanswered (LimitAbort)
}

// Server serves deterministic content. It is safe for concurrent use.
type Server struct {
	*httptest.Server

	mu        sync.Mutex
	opts      Options
	reqs      []Request
	stalled   int
	stalls    []Stall
	active    int // requests being served
	maxActive int // peak of active

	holding  bool          // Hold is in effect
	holdLeft int64         // body bytes Hold still lets through
	released chan struct{} // closed by Release

	sent atomic.Int64 // body bytes written to clients

	quit      chan struct{}
	closeOnce sync.Once
}

// New starts a server configured by opts.
func New(opts Options) *Server {
	s := &Server{opts: opts, quit: make(chan struct{})}
	hs := httptest.NewUnstartedServer(http.HandlerFunc(s.serve))
	if opts.HTTP2 {
		hs.EnableHTTP2 = true
		hs.StartTLS()
	} else {
		hs.Start()
	}
	s.Server = hs
	return s
}

// Stall records one response that stalled (Options.StallAfter).
type Stall struct {
	Start time.Time // the write of the response's last byte before the stall began
	End   time.Time // the client went away or the server closed; zero while stalled
}

// Stalls returns the responses that stalled so far, in the order they stalled. A test
// times how promptly a client gave up on one from these, not from the length of a
// whole run.
func (s *Server) Stalls() []Stall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Stall(nil), s.stalls...)
}

// Hold lets n more body bytes through, over all responses together, and then holds
// every response (after flushing what it sent) until Release, the client going away
// or the server closing. A download can then not complete before the test acts on it,
// however fast the machine serves it.
func (s *Server) Hold(n int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.holding {
		s.released = make(chan struct{})
	}
	s.holding, s.holdLeft = true, max(n, 0)
}

// Release ends a Hold: held responses go on.
func (s *Server) Release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.holding {
		s.holding = false
		close(s.released)
	}
}

// grant returns how many of want body bytes a response may send now: all of them
// without a Hold, else what the Hold has left (taken from it). With nothing granted it
// returns the channel Release closes.
func (s *Server) grant(want int64) (int64, <-chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.holding {
		return want, nil
	}
	n := min(want, s.holdLeft)
	s.holdLeft -= n
	if n == 0 {
		return 0, s.released
	}
	return n, nil
}

// Close releases stalled handlers and shuts the server down.
func (s *Server) Close() {
	s.closeOnce.Do(func() { close(s.quit) })
	s.Server.Close()
}

// Update changes the options for subsequent requests.
func (s *Server) Update(fn func(*Options)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.opts)
}

// SetETag changes the ETag of the served representation.
func (s *Server) SetETag(etag string) { s.Update(func(o *Options) { o.ETag = etag }) }

// SetLastModified changes the Last-Modified time of the served representation.
func (s *Server) SetLastModified(t time.Time) { s.Update(func(o *Options) { o.LastModified = t }) }

// SetToken changes the token that links need (Options.Token): links with the old one
// expire.
func (s *Server) SetToken(token string) { s.Update(func(o *Options) { o.Token = token }) }

// Options returns the current options.
func (s *Server) Options() Options {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.opts
}

// Requests returns the requests received so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.reqs...)
}

// Sum256 returns the SHA-256 of the content currently served.
func (s *Server) Sum256() [32]byte {
	o := s.Options()
	if len(o.Prefix) == 0 {
		return Sum256(o.Seed, o.Size)
	}
	return sha256.Sum256(Content(o))
}

// ContentAt writes the served bytes [off, off+len(p)) of a server configured by o into
// p: the generated content with o.Prefix in place of its first bytes.
func ContentAt(o Options, p []byte, off int64) {
	Fill(o.Seed, p, off)
	if off < int64(len(o.Prefix)) {
		copy(p, o.Prefix[off:])
	}
}

// Content returns every byte a server configured by o serves (o.Size bytes).
func Content(o Options) []byte {
	p := make([]byte, o.Size)
	ContentAt(o, p, 0)
	return p
}

// BytesSent returns the number of response body bytes written to clients so far,
// including bytes a client discarded after closing or resetting the response. A
// chunk being written counts already, so a client that has read N bytes always
// sees at least N.
func (s *Server) BytesSent() int64 { return s.sent.Load() }

// MaxConcurrent returns the largest number of requests that were being served at
// the same time.
func (s *Server) MaxConcurrent() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.maxActive
}

// NewTransport returns a fresh transport trusting the server's certificate (and
// speaking HTTP/2 when enabled), so each client slot gets its own connections.
func (s *Server) NewTransport() *http.Transport {
	return s.Client().Transport.(*http.Transport).Clone()
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	o := s.opts
	n := len(s.reqs)
	fail := n >= o.FailSkip && n < o.FailSkip+o.FailFirst
	rec := Request{Method: r.Method, Proto: r.Proto, Range: r.Header.Get("Range"), IfRange: r.Header.Get("If-Range")}
	refused := o.Limit > 0 && s.active >= o.Limit // over the limit: not counted as served
	if !refused {
		s.active++
		s.maxActive = max(s.maxActive, s.active)
	}
	s.mu.Unlock()
	if refused {
		if o.LimitAbort {
			s.record(rec, 0)
			panic(http.ErrAbortHandler)
		}
		fail = true
	} else {
		defer func() {
			s.mu.Lock()
			s.active--
			s.mu.Unlock()
		}()
	}

	if r.URL.Path == "/expired" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		s.record(rec, http.StatusOK)
		_, _ = w.Write([]byte(ExpiredPage))
		return
	}
	if o.Token != "" && r.URL.Query().Get("token") != o.Token {
		switch {
		case o.ExpiredRedirect:
			s.record(rec, http.StatusFound)
			http.Redirect(w, r, "/expired", http.StatusFound)
		case o.ExpiredNoChallenge401:
			s.record(rec, http.StatusUnauthorized)
			w.WriteHeader(http.StatusUnauthorized)
		default:
			status := cmp.Or(o.ExpiredStatus, http.StatusForbidden)
			s.record(rec, status)
			w.WriteHeader(status)
		}
		return
	}
	if fail {
		status := o.FailStatus
		if status == 0 {
			status = http.StatusServiceUnavailable
		}
		if o.RetryAfter != "" {
			w.Header().Set("Retry-After", o.RetryAfter)
		}
		s.record(rec, status)
		w.WriteHeader(status)
		return
	}

	h := w.Header()
	if o.ETag != "" {
		h.Set("ETag", o.ETag)
	}
	if !o.LastModified.IsZero() {
		h.Set("Last-Modified", o.LastModified.UTC().Format(http.TimeFormat))
	}
	ct := o.ContentType
	if ct == "" {
		ct = "application/octet-stream"
	}
	h.Set("Content-Type", ct)
	if o.Filename != "" {
		h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": o.Filename}))
	}
	switch {
	case o.AcceptRangesNone:
		h.Set("Accept-Ranges", "none")
	case !o.NoRanges:
		h.Set("Accept-Ranges", "bytes")
	}

	start, end, status := int64(0), o.Size-1, http.StatusOK
	if rng := r.Header.Get("Range"); rng != "" && !o.NoRanges && !o.AcceptRangesNone && ifRangeMatches(r.Header.Get("If-Range"), o) {
		a, b, kind := parseRange(rng, o.Size)
		switch {
		case o.Always416 || kind == rangeUnsatisfiable:
			h.Set("Content-Range", "bytes */"+strconv.FormatInt(o.Size, 10))
			s.record(rec, http.StatusRequestedRangeNotSatisfiable)
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		case kind == rangeOK:
			start, end, status = a, b, http.StatusPartialContent
			total := strconv.FormatInt(o.Size, 10)
			if o.UnknownTotal {
				total = "*"
			}
			h.Set("Content-Range", "bytes "+strconv.FormatInt(a, 10)+"-"+strconv.FormatInt(b, 10)+"/"+total)
		}
	}
	length := end - start + 1
	h.Set("Content-Length", strconv.FormatInt(length, 10))

	stallAt := int64(-1)
	s.mu.Lock()
	if o.StallAfter > 0 && length > o.StallAfter && r.Method != http.MethodHead &&
		(o.StallCount <= 0 || s.stalled < o.StallCount) {
		s.stalled++
		stallAt = o.StallAfter
	}
	s.mu.Unlock()

	s.record(rec, status)
	w.WriteHeader(status)
	if r.Method == http.MethodHead {
		return
	}
	s.writeBody(w, r, o, start, end, stallAt)
}

func (s *Server) record(rec Request, status int) {
	rec.Status = status
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reqs = append(s.reqs, rec)
}

// writeBody streams content [start, end], pacing it to o.Throttle and blocking after
// stallAt bytes (when stallAt ≥ 0) until the client goes away or the server closes.
func (s *Server) writeBody(w http.ResponseWriter, r *http.Request, o Options, start, end, stallAt int64) {
	flusher, _ := w.(http.Flusher)
	flushEach := o.Throttle > 0 || stallAt >= 0
	buf := make([]byte, 32<<10)
	t0 := time.Now()
	var sent int64
	var wrote time.Time // when the last write began: no client read its bytes before
	for off := start; off <= end; {
		if stallAt >= 0 && sent >= stallAt {
			if flusher != nil {
				flusher.Flush()
			}
			s.mu.Lock()
			i := len(s.stalls)
			s.stalls = append(s.stalls, Stall{Start: wrote})
			s.mu.Unlock()
			select {
			case <-r.Context().Done():
			case <-s.quit:
			}
			s.mu.Lock()
			s.stalls[i].End = time.Now()
			s.mu.Unlock()
			return
		}
		chunk := min(int64(len(buf)), end-off+1)
		if o.Throttle > 0 {
			chunk = min(chunk, max(o.Throttle/50, 1))
		}
		if stallAt >= 0 {
			chunk = min(chunk, stallAt-sent)
		}
		granted, released := s.grant(chunk)
		if granted == 0 {
			if flusher != nil {
				flusher.Flush()
			}
			select {
			case <-released:
				if o.Throttle > 0 { // pace on from here, not in a burst for the time held
					t0 = time.Now().Add(-time.Duration(float64(sent) / float64(o.Throttle) * float64(time.Second)))
				}
				continue
			case <-r.Context().Done():
			case <-s.quit:
			}
			return
		}
		chunk = granted
		ContentAt(o, buf[:chunk], off)
		wrote = time.Now()
		// Counted before the write: a client can read the whole chunk before Write
		// returns, and must then already see it in BytesSent.
		s.sent.Add(chunk)
		n, err := w.Write(buf[:chunk])
		if int64(n) < chunk {
			s.sent.Add(int64(n) - chunk)
		}
		if err != nil {
			return
		}
		off += chunk
		sent += chunk
		if flushEach && flusher != nil {
			flusher.Flush()
		}
		if o.Throttle > 0 {
			due := time.Duration(float64(sent) / float64(o.Throttle) * float64(time.Second))
			if wait := due - time.Since(t0); wait > 0 {
				t := time.NewTimer(wait)
				select {
				case <-t.C:
				case <-r.Context().Done():
					t.Stop()
					return
				case <-s.quit:
					t.Stop()
					return
				}
			}
		}
	}
}

// ifRangeMatches evaluates If-Range (RFC 9110 §13.1.5): an entity tag matches only by
// strong comparison, a date only when it equals Last-Modified exactly.
func ifRangeMatches(v string, o Options) bool {
	if v == "" {
		return true
	}
	if strings.HasPrefix(v, `"`) || strings.HasPrefix(v, "W/") {
		return o.ETag != "" && !strings.HasPrefix(o.ETag, "W/") && v == o.ETag
	}
	t, err := http.ParseTime(v)
	return err == nil && !o.LastModified.IsZero() && t.Equal(o.LastModified.UTC().Truncate(time.Second))
}

type rangeKind int

const (
	rangeIgnored       rangeKind = iota // absent, malformed or multi-range: serve 200
	rangeOK                             // a satisfiable single range
	rangeUnsatisfiable                  // serve 416
)

// parseRange parses a single "bytes=" range against a representation of size bytes.
func parseRange(v string, size int64) (start, end int64, kind rangeKind) {
	spec, ok := strings.CutPrefix(v, "bytes=")
	if !ok || strings.Contains(spec, ",") {
		return 0, 0, rangeIgnored
	}
	first, last, ok := strings.Cut(strings.TrimSpace(spec), "-")
	if !ok {
		return 0, 0, rangeIgnored
	}
	if first == "" { // suffix range "-n"
		n, err := strconv.ParseInt(last, 10, 64)
		if err != nil || n < 0 {
			return 0, 0, rangeIgnored
		}
		if n == 0 || size == 0 {
			return 0, 0, rangeUnsatisfiable
		}
		return max(size-n, 0), size - 1, rangeOK
	}
	a, err := strconv.ParseInt(first, 10, 64)
	if err != nil || a < 0 {
		return 0, 0, rangeIgnored
	}
	b := size - 1
	if last != "" {
		if b, err = strconv.ParseInt(last, 10, 64); err != nil || b < a {
			return 0, 0, rangeIgnored
		}
	}
	if a >= size {
		return 0, 0, rangeUnsatisfiable
	}
	return a, min(b, size-1), rangeOK
}
