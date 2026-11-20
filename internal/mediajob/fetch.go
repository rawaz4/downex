package mediajob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"downex/internal/media"
	"downex/internal/model"
	"downex/internal/transport"
)

// localError marks a failure of the local side (a file or buffer write), which a retry
// of the request cannot fix.
type localError struct{ err error }

func (e *localError) Error() string { return e.err.Error() }
func (e *localError) Unwrap() error { return e.err }

// fetchSpec is one resource to fetch: the whole resource, or an inclusive byte range.
type fetchSpec struct {
	url     string
	headers http.Header
	rng     *[2]int64
	// maxReq caps the length of each request for a byte range (the host's
	// max_request_bytes; 0 = no cap): a longer range is fetched with several requests.
	maxReq int64
	// total is the size of the whole resource as declared (0 = unknown): a 206 that names
	// another total, or a 200 of another length, is a range mismatch.
	total int64
	// whole (rng nil) asks for the resource without a Range header and accepts a
	// content-coded answer to that one request, which the caller decodes (a manifest,
	// fetchManifest). Any other coded answer is refused with ErrContentCoded: its bytes
	// and Content-Range are those of the coded data.
	whole bool
}

// respMeta is what the first response of a fetch says about the resource.
type respMeta struct {
	finalURL    string
	contentType string
	coding      []string // transport.Response.ContentCoding
}

// copier moves body bytes to their destination in read slices, waiting on the limiter
// for every slice. lim may be changed between copies (the limiter of a fragment's host).
type copier struct {
	lim     throttle // nil = unlimited
	buf     []byte
	onBytes func(n int) // counts received payload bytes; may be nil
	clock   Clock       // times the stall watchdog; nil = RealClock()
}

// Read slice sizes, as in internal/engine: smaller slices keep a capped rate smooth.
const (
	sliceUncapped = 256 << 10
	sliceCapped   = 32 << 10
)

// mediaStallTimeout is how long a response may deliver no byte while the copier waits
// for one before it is closed and the copy fails with errStalled (the engine's stall
// timeout, docs/ENGINE-SPEC.md §5).
const mediaStallTimeout = 20 * time.Second

// errStalled reports a response that delivered no byte for mediaStallTimeout. It is
// transient: the request is retried within the job's retry bounds.
var errStalled = errors.New("mediajob: no data received")

func newCopier(lim throttle, onBytes func(int)) *copier {
	return &copier{lim: lim, buf: make([]byte, sliceUncapped), onBytes: onBytes}
}

// copy reads n bytes from src (to EOF when n < 0) into dst. Received bytes are counted
// when count is set. A body that ends before n bytes yields ErrTruncated. When src can
// be closed (a response body), a watchdog closes it once a Read has waited
// mediaStallTimeout without a byte, and copy fails with errStalled; the time spent
// waiting on the limiter or writing to dst never counts.
func (c *copier) copy(ctx context.Context, dst io.Writer, src io.Reader, n int64, count bool) (int64, error) {
	var w *stallWatch
	if cl, ok := src.(io.Closer); ok {
		w = c.watch(cl)
		defer w.stop()
	}
	var done int64
	for n < 0 || done < n {
		p := c.buf
		if c.lim != nil && len(p) > sliceCapped {
			p = p[:sliceCapped]
		}
		if n >= 0 && int64(len(p)) > n-done {
			p = p[:n-done]
		}
		if w != nil {
			w.reading()
		}
		k, err := src.Read(p)
		if w != nil && k > 0 {
			w.received()
		}
		if k > 0 {
			if c.lim != nil {
				if werr := c.lim.WaitN(ctx, k); werr != nil {
					return done, werr
				}
			}
			if _, werr := dst.Write(p[:k]); werr != nil {
				return done, &localError{werr}
			}
			done += int64(k)
			if count && c.onBytes != nil {
				c.onBytes(k)
			}
		}
		if err != nil && w != nil && w.tripped() {
			// The watchdog closed src: whatever the Read returned (an error, or io.EOF
			// from a body of unknown length), the response is incomplete.
			return done, fmt.Errorf("%w: %d bytes received, then none for %v", errStalled, done, mediaStallTimeout)
		}
		switch {
		case err == io.EOF:
			if n >= 0 && done < n {
				return done, fmt.Errorf("%w: got %d of %d bytes", ErrTruncated, done, n)
			}
			return done, nil
		case err != nil:
			return done, err
		}
	}
	return done, nil
}

// stallWatch is the watchdog of one copy: it polls every quarter of mediaStallTimeout
// and closes src once the copier has waited in Read that long without a byte.
type stallWatch struct {
	clock Clock
	base  time.Time // clock time at the start; since counts from it
	src   io.Closer
	// since is when the current wait for bytes began, as clock time since base plus 1
	// ns; 0 while the copier is not waiting in Read (limiter waits, writes).
	since   atomic.Int64
	stalled atomic.Bool
	quit    chan struct{}
	exited  chan struct{}
}

// watch starts the watchdog of a copy from src; the caller stops it before it returns.
func (c *copier) watch(src io.Closer) *stallWatch {
	clk := c.clock
	if clk == nil {
		clk = RealClock()
	}
	w := &stallWatch{clock: clk, base: clk.Now(), src: src, quit: make(chan struct{}), exited: make(chan struct{})}
	go w.run()
	return w
}

func (w *stallWatch) run() {
	defer close(w.exited)
	for {
		t := w.clock.NewTimer(mediaStallTimeout / 4)
		select {
		case <-w.quit:
			t.Stop()
			return
		case <-t.C():
		}
		if since := w.since.Load(); since != 0 && w.elapsed()-(since-1) >= int64(mediaStallTimeout) {
			w.stalled.Store(true) // before Close: the failing Read must see it
			_ = w.src.Close()
			return
		}
	}
}

func (w *stallWatch) elapsed() int64 { return int64(w.clock.Now().Sub(w.base)) }

// reading marks a Read about to start: the wait for bytes begins unless it already has
// (a Read that returned no byte does not restart it).
func (w *stallWatch) reading() {
	if w.since.Load() == 0 {
		w.since.Store(w.elapsed() + 1)
	}
}

// received marks a Read that returned bytes: the copier is no longer waiting.
func (w *stallWatch) received() { w.since.Store(0) }

// tripped reports whether the watchdog closed src.
func (w *stallWatch) tripped() bool { return w.stalled.Load() }

// stop ends the watchdog and waits until its goroutine has exited.
func (w *stallWatch) stop() {
	close(w.quit)
	<-w.exited
}

// fetchInto fetches spec and writes exactly its bytes to w: it continues with a new
// request when a server answers an open or bounded range with a shorter one (or when
// spec.maxReq split a bounded range), and skips the leading bytes when a server ignores
// the range and sends the whole resource. It returns the bytes written to w; those of
// a content-coded answer only for spec.whole (respMeta.coding names the coding).
func fetchInto(ctx context.Context, f transport.Fetcher, spec fetchSpec, w io.Writer, cp *copier, onRequest func(delta int)) (respMeta, int64, error) {
	var meta respMeta
	start, end := int64(0), int64(-1)
	if spec.rng != nil {
		start, end = spec.rng[0], spec.rng[1]
	}
	pos := start
	for first := true; ; first = false {
		reqEnd := end
		if spec.rng != nil {
			reqEnd = cappedEnd(pos, end, spec.maxReq)
		}
		if onRequest != nil {
			onRequest(1)
		}
		resp, err := f.Fetch(ctx, transport.RangeRequest{
			Request: model.Request{URL: spec.url, Headers: spec.headers},
			Start:   pos,
			End:     reqEnd,
			Whole:   spec.whole && first,
		})
		if err != nil {
			if onRequest != nil {
				onRequest(-1)
			}
			return meta, pos - start, redactURL(err)
		}
		if first {
			meta = respMeta{finalURL: resp.FinalURL, contentType: resp.ContentType, coding: resp.ContentCoding}
		}
		// A coded answer is usable only whole: its bytes cannot be joined with another
		// answer's, nor written where the plain bytes belong.
		if coding := resp.ContentCoding; (len(coding) > 0 && !(spec.whole && first)) || (!first && len(meta.coding) > 0) {
			_ = resp.Body.Close()
			if onRequest != nil {
				onRequest(-1)
			}
			if len(coding) == 0 {
				coding = meta.coding
			}
			return meta, pos - start, fmt.Errorf("%w: %s", ErrContentCoded, strings.Join(coding, ", "))
		}
		n, done, err := readResponse(ctx, resp, pos, reqEnd, spec.total, w, cp)
		_ = resp.Body.Close()
		if onRequest != nil {
			onRequest(-1)
		}
		pos += n
		if err != nil || (done && (end < 0 || pos > end)) {
			return meta, pos - start, err
		}
	}
}

// readResponse copies the part of resp that the request for [pos, end] (end -1: to
// the end of the resource) asked for; total is the resource's declared size (0 =
// unknown, fetchSpec.total). done reports that nothing is left to request.
func readResponse(ctx context.Context, resp *transport.Response, pos, end, total int64, w io.Writer, cp *copier) (n int64, done bool, err error) {
	switch resp.StatusCode {
	case http.StatusOK:
		// The whole resource, from byte 0: skip what precedes pos.
		want := int64(-1)
		switch {
		case total > 0 && resp.ContentLength >= 0 && resp.ContentLength != total:
			return 0, true, fmt.Errorf("%w: the resource has %d bytes, not the %d declared", ErrRangeMismatch, resp.ContentLength, total)
		case end >= 0:
			want = end - pos + 1
			if resp.ContentLength >= 0 && resp.ContentLength <= end {
				return 0, true, fmt.Errorf("%w: the resource has %d bytes, the range ends at byte %d", ErrRangeMismatch, resp.ContentLength, end)
			}
		case resp.ContentLength >= 0:
			if resp.ContentLength <= pos {
				return 0, true, fmt.Errorf("%w: the resource shrank to %d bytes", ErrRangeMismatch, resp.ContentLength)
			}
			want = resp.ContentLength - pos
		}
		if pos > 0 {
			if _, err := cp.copy(ctx, io.Discard, resp.Body, pos, false); err != nil {
				return 0, true, err
			}
		}
		n, err := cp.copy(ctx, w, resp.Body, want, true)
		if err == nil && total > 0 && resp.ContentLength < 0 {
			// A body of unknown length must end with the declared size.
			var extra [1]byte
			if _, rerr := io.ReadFull(resp.Body, extra[:]); rerr == nil {
				err = fmt.Errorf("%w: the resource is longer than the %d bytes declared", ErrRangeMismatch, total)
			}
		}
		return n, true, err
	case http.StatusPartialContent:
		cr := resp.ContentRange
		switch {
		case cr == nil:
			return 0, true, fmt.Errorf("%w: 206 without a usable Content-Range", ErrRangeMismatch)
		case cr.Start != pos, cr.End < cr.Start, end >= 0 && cr.End > end:
			return 0, true, fmt.Errorf("%w: asked from byte %d, got %d-%d", ErrRangeMismatch, pos, cr.Start, cr.End)
		case total > 0 && cr.Total >= 0 && cr.Total != total:
			return 0, true, fmt.Errorf("%w: the resource has %d bytes, not the %d declared", ErrRangeMismatch, cr.Total, total)
		}
		length := cr.End - cr.Start + 1
		if resp.ContentLength >= 0 && resp.ContentLength != length {
			return 0, true, fmt.Errorf("%w: Content-Length %d disagrees with Content-Range", ErrRangeMismatch, resp.ContentLength)
		}
		n, err := cp.copy(ctx, w, resp.Body, length, true)
		if err != nil {
			return n, true, err
		}
		if end >= 0 {
			return n, cr.End == end, nil
		}
		return n, cr.Total < 0 || cr.End+1 >= cr.Total, nil
	case http.StatusRequestedRangeNotSatisfiable:
		if end < 0 && pos == 0 {
			return 0, true, fmt.Errorf("%w: the resource is empty", ErrTruncated)
		}
	}
	return 0, true, &StatusError{Code: resp.StatusCode, RetryAfter: resp.RetryAfter}
}

// redacted is an error whose message has its request URL removed (URLs may carry
// credentials or signed tokens); errors.Is and errors.As still see the original.
type redacted struct {
	msg string
	err error
}

func (e *redacted) Error() string { return e.msg }
func (e *redacted) Unwrap() error { return e.err }

// redactURL removes the URL that net/http puts into the message of a *url.Error.
func redactURL(err error) error {
	var ue *url.Error
	if err == nil || !errors.As(err, &ue) || ue.URL == "" {
		return err
	}
	return &redacted{msg: strings.ReplaceAll(err.Error(), ue.URL, "(URL)"), err: err}
}

// retrier repeats an attempt while it fails transiently, with exponential backoff.
type retrier struct {
	clock          Clock
	max            int
	base, maxDelay time.Duration
}

// do runs attempt until it succeeds, fails permanently, fails r.max times in a row or
// ctx is done (then it returns ctx.Err()).
func (r retrier) do(ctx context.Context, attempt func() error) error {
	return r.doNotify(ctx, attempt, nil)
}

// doNotify is do with onWait (may be nil) told the failure and the wait before every
// retry.
func (r retrier) doNotify(ctx context.Context, attempt func() error, onWait func(err error, wait time.Duration)) error {
	for failures := 0; ; {
		err := attempt()
		if err == nil {
			return nil
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if !transient(err) {
			return err
		}
		failures++
		if failures >= r.max {
			return fmt.Errorf("giving up after %d attempts: %w", failures, err)
		}
		wait := r.delay(failures, retryAfterOf(err))
		if onWait != nil {
			onWait(err, wait)
		}
		if serr := sleep(ctx, r.clock, wait); serr != nil {
			return serr
		}
	}
}

// delay is the wait before attempt failures+1: Retry-After when the server sent one,
// else exponential backoff from r.base with ±20% jitter, capped at r.maxDelay.
func (r retrier) delay(failures int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		return retryAfter
	}
	d := r.base
	for i := 1; i < failures && d < r.maxDelay; i++ {
		d *= 2
	}
	d = min(d, r.maxDelay)
	return time.Duration(float64(d) * (0.8 + 0.4*rand.Float64()))
}

func retryAfterOf(err error) time.Duration {
	var se *StatusError
	if errors.As(err, &se) {
		return se.RetryAfter
	}
	return 0
}

// transient reports whether a failed request may succeed when retried. Unknown errors
// (network failures, truncated bodies) are transient; retries are bounded.
func transient(err error) bool {
	var se *StatusError
	if errors.As(err, &se) {
		switch se.Code {
		case http.StatusRequestTimeout, http.StatusMisdirectedRequest, http.StatusTooEarly,
			http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway,
			http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return true
		}
		return false
	}
	var le *localError
	if errors.As(err, &le) {
		return false
	}
	for _, permanent := range []error{
		ErrRangeMismatch, ErrInvalidSelection, ErrNotManifest, ErrInvalidOptions,
		transport.ErrTLS, transport.ErrTooLarge, transport.ErrRedirects, transport.ErrInvalidRequest,
		transport.ErrInsecureRedirect, transport.ErrGone, transport.ErrLocalAddress,
		media.ErrEncrypted, media.ErrLive, media.ErrTooLarge, media.ErrMalformed, media.ErrUnsupported,
		media.ErrNotFound,
	} {
		if errors.Is(err, permanent) {
			return false
		}
	}
	return true
}

// headerPolicy sends the caller's headers with every request, but credentials only to
// the origin they were captured for (the manifest URL's).
type headerPolicy struct {
	origin string
	full   http.Header
	bare   http.Header // full without credentials
}

func newHeaderPolicy(manifestURL string, h http.Header) headerPolicy {
	p := headerPolicy{full: h.Clone()}
	if u, err := url.Parse(manifestURL); err == nil {
		p.origin = origin(u)
	}
	p.bare = h.Clone()
	for k := range p.bare {
		switch http.CanonicalHeaderKey(k) {
		case "Authorization", "Cookie", "Cookie2":
			delete(p.bare, k)
		}
	}
	return p
}

func (p headerPolicy) forURL(raw string) http.Header {
	if p.origin != "" {
		if u, err := url.Parse(raw); err == nil && origin(u) == p.origin {
			return p.full
		}
	}
	return p.bare
}

// origin returns the RFC 6454 origin "scheme://host:port" with the default port made
// explicit.
func origin(u *url.URL) string {
	scheme := strings.ToLower(u.Scheme)
	port := u.Port()
	if port == "" {
		switch scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		}
	}
	return scheme + "://" + strings.ToLower(u.Hostname()) + ":" + port
}

// limitedBuffer collects a manifest, refusing more than max bytes.
type limitedBuffer struct {
	max int
	b   []byte
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if len(p) > l.max-len(l.b) {
		return 0, fmt.Errorf("%w: manifest is larger than %d bytes", media.ErrTooLarge, l.max)
	}
	l.b = append(l.b, p...)
	return len(p), nil
}
