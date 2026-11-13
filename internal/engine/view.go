package engine

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http/httptrace"
	"os"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"downex/internal/model"
	"downex/internal/transport"
)

// Detail texts of a connecting worker (docs/DESKTOP-SPEC.md §7).
const (
	detailLookup  = "looking up host"
	detailOpening = "opening connection"
	detailSecure  = "securing connection"
)

// liveView is what one worker connection reports about itself in progress events
// (Progress.Workers). The worker goroutine and the transport's trace callbacks write
// it; the coordinator reads it when it emits.
type liveView struct {
	state atomic.Int32 // model.WorkerState, also readable without the lock (hot path)

	mu      sync.Mutex
	gen     uint64 // the request being traced; callbacks of earlier requests are ignored
	tracing bool   // the request of generation gen has not returned yet
	detail  string
	retryAt time.Time
}

func (v *liveView) setLocked(s model.WorkerState, detail string, retryAt time.Time) {
	v.detail, v.retryAt = detail, retryAt
	v.state.Store(int32(s))
}

// set reports state s and ends the tracing of the current request.
func (v *liveView) set(s model.WorkerState, detail string) {
	v.mu.Lock()
	v.tracing = false
	v.setLocked(s, detail, time.Time{})
	v.mu.Unlock()
}

// retry reports a wait for reason until at.
func (v *liveView) retry(reason string, at time.Time) {
	v.mu.Lock()
	v.tracing = false
	v.setLocked(model.WorkerRetrying, reason, at)
	v.mu.Unlock()
}

// receiving reports body bytes; it only takes the lock when the state changes.
func (v *liveView) receiving() {
	if model.WorkerState(v.state.Load()) != model.WorkerReceiving {
		v.set(model.WorkerReceiving, "")
	}
}

// trace reports Connecting for a new request and returns its context, whose trace
// callbacks advance the state to Requesting once the request is written. Call end with
// the returned generation when the request returned.
func (v *liveView) trace(ctx context.Context) (context.Context, uint64) {
	v.mu.Lock()
	v.gen++
	gen := v.gen
	v.tracing = true
	v.setLocked(model.WorkerConnecting, "", time.Time{})
	v.mu.Unlock()
	return httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		DNSStart:          func(httptrace.DNSStartInfo) { v.step(gen, model.WorkerConnecting, detailLookup) },
		ConnectStart:      func(string, string) { v.step(gen, model.WorkerConnecting, detailOpening) },
		TLSHandshakeStart: func() { v.step(gen, model.WorkerConnecting, detailSecure) },
		WroteRequest:      func(httptrace.WroteRequestInfo) { v.step(gen, model.WorkerRequesting, "") },
	}), gen
}

// step applies a trace callback of request gen while that request is being sent.
// Dials the transport finishes in the background for an earlier request, and
// callbacks after the response arrived, change nothing.
func (v *liveView) step(gen uint64, s model.WorkerState, detail string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if !v.tracing || gen != v.gen {
		return
	}
	switch model.WorkerState(v.state.Load()) {
	case model.WorkerConnecting, model.WorkerRequesting:
		v.setLocked(s, detail, time.Time{})
	}
}

// end stops tracing request gen.
func (v *liveView) end(gen uint64) {
	v.mu.Lock()
	if gen == v.gen {
		v.tracing = false
	}
	v.mu.Unlock()
}

func (v *liveView) snapshot() (model.WorkerState, string, time.Time) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return model.WorkerState(v.state.Load()), v.detail, v.retryAt
}

// retryDetail is the Detail of a connection that waits until at before its next
// attempt: the reason and the wait left, rounded up ("HTTP 503, retrying in 4 s").
func retryDetail(reason string, at, now time.Time) string {
	left := at.Sub(now)
	wait := strconv.FormatInt(max(int64((left+time.Second-1)/time.Second), 1), 10) + " s"
	if left > 99*time.Second {
		wait = strconv.FormatInt(int64((left+time.Minute-1)/time.Minute), 10) + " min"
	}
	if reason == "" {
		return "retrying in " + wait
	}
	return reason + ", retrying in " + wait
}

// Windows socket errors (WSAECONNABORTED, WSAECONNRESET, WSAECONNREFUSED), which
// have no portable syscall constant.
const (
	wsaConnAborted = syscall.Errno(10053)
	wsaConnReset   = syscall.Errno(10054)
	wsaConnRefused = syscall.Errno(10061)
)

// shortReason describes err for a connection's Detail. It never returns the error's
// own text, which may carry URLs.
func shortReason(err error) string {
	var se *StatusError
	var dnsErr *net.DNSError
	var ne net.Error
	switch {
	case err == nil:
		return ""
	case errors.As(err, &se):
		return "HTTP " + strconv.Itoa(se.Code)
	case errors.Is(err, ErrStalled):
		return "no data received"
	case errors.Is(err, ErrValidatorChanged):
		return "file changed on the server"
	case errors.Is(err, ErrRangeIgnored):
		return "server ignored the range"
	case errors.Is(err, ErrDiskFull):
		return "disk full"
	case errors.Is(err, transport.ErrTLS):
		return "TLS verification failed"
	case errors.Is(err, transport.ErrTooLarge):
		return "response too large"
	case errors.Is(err, transport.ErrRedirects):
		return "too many redirects"
	case errors.Is(err, transport.ErrInsecureRedirect):
		return "insecure redirect refused"
	case errors.Is(err, transport.ErrInvalidRequest), errors.Is(err, ErrInvalidRequest):
		return "invalid request"
	case errors.Is(err, io.ErrUnexpectedEOF):
		return "connection closed early"
	case errors.As(err, &dnsErr):
		return "host not found"
	case isSocketErr(err, syscall.ECONNREFUSED, wsaConnRefused):
		return "connection refused"
	case isSocketErr(err, syscall.ECONNRESET, wsaConnReset), isSocketErr(err, syscall.ECONNABORTED, wsaConnAborted):
		return "connection reset"
	case errors.Is(err, os.ErrDeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return "timed out"
	}
	return "network error"
}

func isSocketErr(err error, posix, windows syscall.Errno) bool {
	if runtime.GOOS == "windows" {
		var errno syscall.Errno
		return errors.As(err, &errno) && errno == windows
	}
	return errors.Is(err, posix)
}
