package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Download limits (docs/SITE-MEDIA-SPEC.md §4.2).
const (
	maxYtDlpAsset   = 128 << 20
	maxDenoZip      = 256 << 20
	maxDenoUnpacked = 512 << 20
	maxSumsFile     = 64 << 10
	maxSigFile      = 16 << 10
	// minFreeSpace is the free space an install needs on the tools volume.
	minFreeSpace = 256 << 20
	// headerTimeout bounds the wait for a response's headers.
	headerTimeout = 30 * time.Second
	// stallWindow and stallMinRate: a download fails when less than stallMinRate bytes
	// per second arrive over stallWindow.
	stallWindow  = 60 * time.Second
	stallMinRate = 1 << 10
	// installTimeout bounds a whole install.
	installTimeout = 15 * time.Minute
	// maxRedirects bounds the redirects of one request.
	maxRedirects = 10
)

// userAgent identifies Downex's tool downloads.
const userAgent = "Downex"

var (
	errHeaderTimeout    = errors.New("the server did not answer")
	errStalled          = errors.New("the download stalled")
	errInsecureRedirect = errors.New("redirected to an address that is not https")
)

// fetcher downloads release files over https only, with the time and size limits of
// §4.2. Its errors are Errors without a tool and never contain URLs.
type fetcher struct {
	client        *http.Client
	headerTimeout time.Duration
	stallWindow   time.Duration
	stallMinBytes int64 // per stallWindow
	// retryDelay is the first wait before a broken part asks again (0: defaultRetryDelay).
	retryDelay time.Duration
}

// secureClient returns a copy of c that follows redirects only to https URLs, at most
// maxRedirects of them; c's own CheckRedirect still runs.
func secureClient(c *http.Client) *http.Client {
	cp := *c
	next := c.CheckRedirect
	cp.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" {
			return errInsecureRedirect
		}
		if len(via) >= maxRedirects {
			return errors.New("too many redirects")
		}
		if next != nil {
			return next(req, via)
		}
		return nil
	}
	return &cp
}

// do sends a GET of raw (https only) with c, failing when no response headers arrive
// within the header timeout. The response body fails with errStalled when it delivers
// less than the minimum rate over a stall window; closing it ends the request.
func (f *fetcher) do(ctx context.Context, c *http.Client, raw string) (*http.Response, error) {
	return f.doWith(ctx, c, raw, nil)
}

// doRange is do of bytes [from, to) of raw with c. It sends no If-Range: the answer must
// name exactly that range of a file of the first response's size (getRange), and the
// joined file is checked against the release's SHA-256 afterwards.
func (f *fetcher) doRange(ctx context.Context, c *http.Client, raw string, from, to int64) (*http.Response, error) {
	return f.doWith(ctx, c, raw, func(req *http.Request) {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(from, 10)+"-"+strconv.FormatInt(to-1, 10))
	})
}

// partClient returns the client of one part: f's client with a transport of its own when
// it is an *http.Transport, so that parts never share one HTTP/2 connection (one
// congestion window, and one break that stops every part), and the function that closes
// that transport's connections when the part is done.
func (f *fetcher) partClient() (*http.Client, func()) {
	rt := f.client.Transport
	if rt == nil {
		rt = http.DefaultTransport
	}
	t, ok := rt.(*http.Transport)
	if !ok {
		return f.client, func() {}
	}
	own := t.Clone()
	c := *f.client
	c.Transport = own
	return &c, own.CloseIdleConnections
}

// doWith is do with setup (may be nil) applied to the request before it is sent.
func (f *fetcher) doWith(ctx context.Context, c *http.Client, raw string, setup func(*http.Request)) (*http.Response, error) {
	if u, err := url.Parse(raw); err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, coded(CodeNetwork, errors.New("not an https address"))
	}
	rctx, cancel := context.WithCancelCause(ctx)
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, raw, nil)
	if err != nil {
		cancel(nil)
		return nil, coded(CodeNetwork, errors.New("not an https address"))
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Cache-Control", "no-cache")
	if setup != nil {
		setup(req)
	}
	timer := time.AfterFunc(f.headerTimeout, func() { cancel(errHeaderTimeout) })
	resp, err := c.Do(req)
	timer.Stop()
	if err != nil {
		err = requestError(rctx, err)
		cancel(nil)
		return nil, err
	}
	done := make(chan struct{})
	body := &watchedBody{ReadCloser: resp.Body, ctx: rctx}
	body.stop = sync.OnceFunc(func() {
		close(done)
		cancel(nil)
	})
	go func() {
		t := time.NewTicker(f.stallWindow)
		defer t.Stop()
		last := int64(0)
		for {
			select {
			case <-done:
				return
			case <-t.C:
				n := body.n.Load()
				if n-last < f.stallMinBytes {
					cancel(errStalled)
					return
				}
				last = n
			}
		}
	}()
	resp.Body = body
	return resp, nil
}

// watchedBody counts what a response body delivers and maps the request's cancellation
// cause into its errors.
type watchedBody struct {
	io.ReadCloser
	ctx  context.Context
	n    atomic.Int64
	stop func()
}

func (b *watchedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.n.Add(int64(n))
	if err != nil && err != io.EOF {
		err = requestError(b.ctx, err)
	}
	return n, err
}

func (b *watchedBody) Close() error {
	err := b.ReadCloser.Close()
	b.stop()
	return err
}

// requestError classifies a failed request or body read: an Error with CodeTLS or
// CodeNetwork, without the URL net/http puts into its errors.
func requestError(ctx context.Context, err error) error {
	if c := context.Cause(ctx); errors.Is(c, errHeaderTimeout) || errors.Is(c, errStalled) {
		return coded(CodeNetwork, c)
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	if isTLSError(err) {
		return coded(CodeTLS, err)
	}
	return coded(CodeNetwork, err)
}

// isTLSError reports a failed TLS handshake or certificate check.
func isTLSError(err error) bool {
	var (
		verr *tls.CertificateVerificationError
		uerr x509.UnknownAuthorityError
		herr x509.HostnameError
		cerr x509.CertificateInvalidError
		rerr tls.RecordHeaderError
		aerr tls.AlertError
	)
	return errors.As(err, &verr) || errors.As(err, &uerr) || errors.As(err, &herr) ||
		errors.As(err, &cerr) || errors.As(err, &rerr) || errors.As(err, &aerr)
}

// statusError is the Error of an HTTP status other than the expected one; a 404 also
// wraps ErrNotFound.
func statusError(code int) *Error {
	err := fmt.Errorf("HTTP status %d", code)
	if code == http.StatusNotFound {
		err = fmt.Errorf("%w (HTTP status %d)", ErrNotFound, code)
	}
	return &Error{Code: CodeHTTPStatus, Detail: "HTTP " + strconv.Itoa(code), Err: err}
}

// tooLarge is the Error of a download above its limit.
func tooLarge(limit int64) *Error {
	return &Error{Code: CodeInternal, Detail: "the download is larger than its limit",
		Err: fmt.Errorf("%w (%d bytes)", ErrTooLarge, limit)}
}

// errWebPage refuses an HTML page served in place of a release file (a GitHub error page).
var errWebPage = &Error{Code: CodeHTTPStatus, Detail: "a web page instead of the file", Err: errors.New("the server sent a web page")}

// isWebPage reports data that starts, after white space and a byte order mark, with
// "<!DOCTYPE html" or "<html" (any case).
func isWebPage(data []byte) bool {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	data = bytes.TrimLeft(data, " \t\r\n")
	if len(data) > 16 {
		data = data[:16]
	}
	lower := bytes.ToLower(data)
	return bytes.HasPrefix(lower, []byte("<!doctype html")) || bytes.HasPrefix(lower, []byte("<html"))
}

// get starts a GET of raw that must answer 200 with at most limit bytes.
func (f *fetcher) get(ctx context.Context, raw string, limit int64) (*http.Response, error) {
	resp, err := f.do(ctx, f.client, raw)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, statusError(resp.StatusCode)
	}
	if resp.ContentLength > limit {
		resp.Body.Close()
		return nil, tooLarge(limit)
	}
	return resp, nil
}

// bytes fetches raw into memory: at most limit bytes, never a web page.
func (f *fetcher) bytes(ctx context.Context, raw string, limit int64) ([]byte, error) {
	resp, err := f.get(ctx, raw, limit)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, tooLarge(limit)
	}
	if isWebPage(data) {
		return nil, errWebPage
	}
	return data, nil
}

// download fetches raw into a new file "<random>.part" in dir: at most limit bytes,
// never a web page, flushed to stable storage. progress (may be nil) receives the bytes
// written so far and the announced total (-1 = unknown). It returns the file's path,
// SHA-256 and size; on failure no file is left.
//
// A file of known size from a server that serves byte ranges comes over up to
// maxFetchParts connections at once (parts of at least minFetchPart), and a part whose
// connection breaks continues from its last byte, up to fetchRetries times in a row
// without progress. Release CDNs can be slow per connection and drop long transfers: a
// single stream that starts over made a first install take minutes and then fail.
// What the parts are joined into is checked afterwards by the caller (SHA-256 against
// the release's sums, OpenPGP for yt-dlp).
func (f *fetcher) download(ctx context.Context, raw string, limit int64, dir string, progress func(done, total int64)) (path string, sum [sha256.Size]byte, size int64, err error) {
	resp, err := f.get(ctx, raw, limit)
	if err != nil {
		return "", sum, 0, err
	}
	first := resp.Body
	defer func() {
		if first != nil {
			first.Close()
		}
	}()
	total := resp.ContentLength
	ranges := total > 0 && strings.EqualFold(strings.TrimSpace(resp.Header.Get("Accept-Ranges")), "bytes")
	if progress == nil {
		progress = func(int64, int64) {}
	}
	var (
		pmu  sync.Mutex
		done atomic.Int64
	)
	report := func(n int64) {
		pmu.Lock()
		defer pmu.Unlock()
		progress(done.Add(n), total)
	}
	report(0)

	file, err := os.CreateTemp(dir, "*.part")
	if err != nil {
		return "", sum, 0, writeError(err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = file.Close()
			_ = os.Remove(file.Name())
		}
	}()

	if total < 0 {
		// Unknown size: one stream, bounded by limit, nothing to resume from.
		body := first
		first = nil
		size, err = f.copyPart(ctx, body, file, 0, limit+1, report)
		body.Close()
		if err != nil {
			return "", sum, 0, err
		}
		if size > limit {
			return "", sum, 0, tooLarge(limit)
		}
	} else {
		if err := file.Truncate(total); err != nil {
			return "", sum, 0, writeError(err)
		}
		parts := int64(1)
		if ranges {
			parts = max(1, min(int64(maxFetchParts), total/minFetchPart))
		}
		step := (total + parts - 1) / parts
		pctx, cancel := context.WithCancelCause(ctx)
		defer cancel(nil)
		var (
			wg   sync.WaitGroup
			emu  sync.Mutex
			ferr error
		)
		fail := func(err error) {
			emu.Lock()
			if ferr == nil {
				ferr = err
				cancel(err)
			}
			emu.Unlock()
		}
		body := first
		first = nil
		// Part 0 reads the first response, which the caller's context opened: a failed
		// part must end that read too, not leave it to the stall watchdog.
		stopFirst := context.AfterFunc(pctx, func() { body.Close() })
		for i := int64(0); i < parts; i++ {
			from, to := i*step, min((i+1)*step, total)
			var b io.ReadCloser
			if i == 0 {
				b = body // the first response carries part 0
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := f.fetchPart(pctx, raw, b, file, from, to, total, ranges, report); err != nil {
					fail(err)
				}
			}()
		}
		wg.Wait()
		stopFirst()
		if ferr != nil {
			if cerr := ctx.Err(); cerr != nil {
				return "", sum, 0, coded(CodeNetwork, cerr)
			}
			return "", sum, 0, ferr
		}
		size = total
	}
	if err := file.Sync(); err != nil {
		return "", sum, 0, writeError(err)
	}
	h := sha256.New()
	head := &headWriter{buf: make([]byte, 0, 16)}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", sum, 0, writeError(err)
	}
	n, err := io.Copy(h, io.TeeReader(io.LimitReader(file, size), head))
	if err != nil || n != size {
		return "", sum, 0, &Error{Code: CodeInternal, Detail: "cannot read the download back", Err: fmt.Errorf("read %d of %d bytes: %v", n, size, err)}
	}
	if isWebPage(head.buf) {
		return "", sum, 0, errWebPage
	}
	if err := file.Close(); err != nil {
		return "", sum, 0, writeError(err)
	}
	ok = true
	copy(sum[:], h.Sum(nil))
	return file.Name(), sum, size, nil
}

// Parallel and resumed downloads (download).
const (
	// maxFetchParts is the most connections one download uses.
	maxFetchParts = 8
	// minFetchPart is the smallest part worth a connection of its own.
	minFetchPart = 1 << 20
	// fetchRetries is how often a part asks again, in a row, without receiving a byte.
	fetchRetries = 5
	// defaultRetryDelay is the first wait before a part asks again; it doubles per retry
	// in a row, up to maxRetryDelay.
	defaultRetryDelay = time.Second
	maxRetryDelay     = 15 * time.Second
)

// fetchPart writes bytes [from, to) of the file into file at their offsets. body, when not
// nil, is a response that starts at from (the first response, for part 0); else the part
// is requested. A broken connection asks again from the next byte when the server serves
// ranges. Every byte written is reported.
func (f *fetcher) fetchPart(ctx context.Context, raw string, body io.ReadCloser, file *os.File, from, to, total int64, ranges bool, report func(int64)) error {
	var client *http.Client // created at the part's first request
	defer func() {
		if body != nil {
			body.Close()
		}
	}()
	first := f.retryDelay
	if first <= 0 {
		first = defaultRetryDelay
	}
	pos, misses, delay := from, 0, first
	for {
		var err error
		if body == nil {
			if client == nil {
				var done func()
				client, done = f.partClient()
				defer done()
			}
			body, err = f.getRange(ctx, client, raw, pos, to, total)
		}
		if err == nil {
			var n int64
			n, err = f.copyPart(ctx, body, file, pos, to-pos, report)
			body.Close()
			body = nil
			pos += n
			if err == nil && pos < to {
				err = coded(CodeNetwork, io.ErrUnexpectedEOF)
			}
			if n > 0 { // progress: the retries in a row start again
				misses, delay = 0, first
			}
		}
		if err == nil {
			return nil
		}
		if ctx.Err() != nil || !ranges || !retryable(err) {
			return err
		}
		if misses++; misses > fetchRetries {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(delay):
		}
		delay = min(2*delay, maxRetryDelay)
	}
}

// getRange requests bytes [from, to) of a file of total bytes and checks that the answer
// is exactly that range.
func (f *fetcher) getRange(ctx context.Context, c *http.Client, raw string, from, to, total int64) (io.ReadCloser, error) {
	resp, err := f.doRange(ctx, c, raw, from, to)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusPartialContent {
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return nil, errOtherRange
		}
		return nil, statusError(resp.StatusCode)
	}
	s, e, t, ok := parseContentRange(resp.Header.Get("Content-Range"))
	if !ok || s != from || e != to-1 || t != total || resp.ContentLength >= 0 && resp.ContentLength != to-from {
		resp.Body.Close()
		return nil, errOtherRange
	}
	return resp.Body, nil
}

// errOtherRange refuses a ranged answer that is not the part asked for (another start,
// end or file size, or the whole file): it is never joined into the download.
var errOtherRange = &Error{Code: CodeHTTPStatus, Detail: "the server sent another part of the file", Err: errors.New("the server answered with another range")}

// retryable reports an error a part may ask again after: a broken or stalled connection,
// or a server that is busy for now.
func retryable(err error) bool {
	var e *Error
	if !errors.As(err, &e) {
		return false
	}
	switch e.Code {
	case CodeNetwork:
		return true
	case CodeHTTPStatus:
		return e != errOtherRange && (strings.HasPrefix(e.Detail, "HTTP 5") || e.Detail == "HTTP 429")
	}
	return false
}

// copyPart copies at most n bytes of body into file from offset at, reporting them, and
// returns how many it wrote.
func (f *fetcher) copyPart(ctx context.Context, body io.Reader, file *os.File, at, n int64, report func(int64)) (int64, error) {
	buf := make([]byte, 64<<10)
	var written int64
	for written < n {
		if err := ctx.Err(); err != nil {
			return written, coded(CodeNetwork, err)
		}
		want := min(int64(len(buf)), n-written)
		k, rerr := body.Read(buf[:want])
		if k > 0 {
			if _, werr := file.WriteAt(buf[:k], at+written); werr != nil {
				return written, writeError(werr)
			}
			written += int64(k)
			report(int64(k))
		}
		if rerr == io.EOF {
			return written, nil
		}
		if rerr != nil {
			var e *Error
			if !errors.As(rerr, &e) {
				rerr = coded(CodeNetwork, rerr)
			}
			return written, rerr
		}
	}
	return written, nil
}

// parseContentRange reads "bytes <start>-<end>/<total>".
func parseContentRange(v string) (start, end, total int64, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(v), "bytes ")
	if !found {
		return 0, 0, 0, false
	}
	span, size, found := strings.Cut(rest, "/")
	if !found {
		return 0, 0, 0, false
	}
	a, b, found := strings.Cut(span, "-")
	if !found {
		return 0, 0, 0, false
	}
	var err1, err2, err3 error
	start, err1 = strconv.ParseInt(a, 10, 64)
	end, err2 = strconv.ParseInt(b, 10, 64)
	total, err3 = strconv.ParseInt(size, 10, 64)
	if err1 != nil || err2 != nil || err3 != nil || start < 0 || end < start || total <= end {
		return 0, 0, 0, false
	}
	return start, end, total, true
}

// headWriter keeps the first cap(buf) bytes written to it.
type headWriter struct{ buf []byte }

func (w *headWriter) Write(p []byte) (int, error) {
	if room := cap(w.buf) - len(w.buf); room > 0 {
		w.buf = append(w.buf, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

// writeError classifies a failed write: CodeDiskFull when the volume is full.
func writeError(err error) *Error {
	if isDiskFull(err) {
		return coded(CodeDiskFull, err)
	}
	return &Error{Code: CodeInternal, Detail: "cannot write the download", Err: err}
}
