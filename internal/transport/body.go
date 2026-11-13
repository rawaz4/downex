package transport

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"
)

// Draining limits (docs/ENGINE-SPEC.md §5): an HTTP/1.1 body abandoned with at most
// maxDrainBytes left is read to the end so the slot's connection can be reused;
// anything larger, or a drain slower than drainTimeout, closes the connection instead.
// HTTP/2 bodies are never drained: closing one resets its stream, and with more than
// maxDrainBytes left it also retires the slot's connection. The server may already
// have sent that much of the reset stream (up to the 4 MiB stream window, plus its
// socket buffers), and the slot's next response would arrive only behind it: at a busy
// connection's share of the line, seconds later than over a new connection.
const (
	maxDrainBytes = 64 << 10
	drainTimeout  = 2 * time.Second
)

// responseBody wraps a response body, tracks how much of it is left and releases the
// request context on Close.
type responseBody struct {
	rc        io.ReadCloser
	cancel    context.CancelFunc
	drainable bool   // HTTP/1.x with a known Content-Length
	http2     bool   // closing resets the stream; the connection stays open
	retire    func() // closes the slot's idle connections; nil = none

	// mu is held for the duration of each Read. Close drains only when it can take mu
	// without waiting; a Close racing a blocked Read just aborts the transfer.
	mu        sync.Mutex
	remaining int64 // bytes left per Content-Length, -1 when unknown
	eof       bool

	closeOnce sync.Once
	closeErr  error
}

// newResponseBody wraps hr's body; retire, if not nil, closes the idle connections of
// the slot that received it.
func newResponseBody(hr *http.Response, cancel context.CancelFunc, retire func()) *responseBody {
	remaining := hr.ContentLength
	if remaining < 0 {
		remaining = -1
	}
	return &responseBody{
		rc:        hr.Body,
		cancel:    cancel,
		drainable: hr.ProtoMajor == 1 && remaining >= 0,
		http2:     hr.ProtoMajor == 2,
		retire:    retire,
		remaining: remaining,
	}
}

// Read reads from the underlying body.
func (b *responseBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n, err := b.rc.Read(p)
	if b.remaining > 0 {
		b.remaining = max(b.remaining-int64(n), 0)
	}
	if err == io.EOF {
		b.eof = true
	}
	return n, err
}

// Close releases the response. It is safe to call more than once and concurrently with Read.
func (b *responseBody) Close() error {
	b.closeOnce.Do(func() {
		retire := b.http2 // a Close racing a Read abandons the transfer midway
		if b.mu.TryLock() {
			small := b.eof || (b.remaining >= 0 && b.remaining <= maxDrainBytes)
			if b.drainable && !b.eof && small {
				// Reading one byte past the expected end observes EOF, which is what
				// returns the connection to the slot's pool.
				timer := time.AfterFunc(drainTimeout, b.cancel)
				_, _ = io.Copy(io.Discard, io.LimitReader(b.rc, b.remaining+1))
				timer.Stop()
			}
			retire = b.http2 && !small
			b.mu.Unlock()
		}
		// HTTP/2: Close returns once the stream is reset and forgotten, so the
		// connection is idle when it is retired.
		b.closeErr = b.rc.Close()
		b.cancel()
		if retire && b.retire != nil {
			b.retire()
		}
	})
	return b.closeErr
}
