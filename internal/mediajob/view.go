package mediajob

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

	"downex/internal/media"
	"downex/internal/model"
	"downex/internal/transport"
)

// Detail texts of a connecting worker (docs/DESKTOP-SPEC.md §7), as in internal/engine.
const (
	detailLookup  = "looking up host"
	detailOpening = "opening connection"
	detailSecure  = "securing connection"
)

// connView is what one fetch worker reports about itself in Progress.Workers. The
// worker goroutine and the transport's trace callbacks write it; the goroutine that
// called Run reads it when it emits and owns speed.
type connView struct {
	slot     int          // 1-based, fixed for the fetch phase
	state    atomic.Int32 // model.WorkerState, also readable without the lock (hot path)
	received atomic.Int64 // payload bytes of the current fragment attempt
	total    atomic.Int64 // every payload byte of this worker, for its speed average
	speed    speedMeter   // owned by the goroutine that called Run

	mu       sync.Mutex
	gen      uint64 // the attempt being traced; callbacks of earlier ones are ignored
	tracing  bool
	detail   string
	retryAt  time.Time
	frag     int // the fragment's index in the job; -1 for none
	rngStart int64
	rngEnd   int64
}

func newConnView(slot int) *connView {
	v := &connView{slot: slot, frag: -1, rngStart: -1, rngEnd: -1}
	v.state.Store(int32(model.WorkerIdle))
	return v
}

func (v *connView) setLocked(s model.WorkerState, detail string, retryAt time.Time) {
	v.detail, v.retryAt = detail, retryAt
	v.state.Store(int32(s))
}

// set reports state s and ends the tracing of the current attempt.
func (v *connView) set(s model.WorkerState, detail string) {
	v.mu.Lock()
	v.tracing = false
	v.setLocked(s, detail, time.Time{})
	v.mu.Unlock()
}

// assign reports the fragment the worker starts on: index frag of the job, bytes
// [start, end] of its resource (-1, -1 for the whole resource).
func (v *connView) assign(frag int, start, end int64) {
	v.received.Store(0)
	v.mu.Lock()
	v.tracing = false
	v.frag, v.rngStart, v.rngEnd = frag, start, end
	v.setLocked(model.WorkerConnecting, "", time.Time{})
	v.mu.Unlock()
}

func (v *connView) retry(reason string, at time.Time) {
	v.mu.Lock()
	v.tracing = false
	v.setLocked(model.WorkerRetrying, reason, at)
	v.mu.Unlock()
}

// receiving reports body bytes; it only takes the lock when the state changes.
func (v *connView) receiving() {
	if model.WorkerState(v.state.Load()) != model.WorkerReceiving {
		v.set(model.WorkerReceiving, "")
	}
}

// trace starts a fetch attempt: Connecting, with trace callbacks on the returned
// context that advance the state to Requesting once the request is written. Call end
// with the returned generation when the attempt's requests returned.
func (v *connView) trace(ctx context.Context) (context.Context, uint64) {
	v.received.Store(0)
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

// step applies a trace callback of attempt gen while its request is being sent; late
// callbacks (a background dial, a request after the body started) change nothing.
func (v *connView) step(gen uint64, s model.WorkerState, detail string) {
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

func (v *connView) end(gen uint64) {
	v.mu.Lock()
	if gen == v.gen {
		v.tracing = false
	}
	v.mu.Unlock()
}

// view is the worker's row in a progress event at time now. Only the goroutine that
// called Run calls it (it reads speed).
func (v *connView) view(now time.Time) model.WorkerView {
	v.mu.Lock()
	defer v.mu.Unlock()
	w := model.WorkerView{
		Slot: v.slot, State: model.WorkerState(v.state.Load()), Start: v.rngStart, End: v.rngEnd,
		Received: v.received.Load(), SpeedBps: v.speed.rate, Detail: v.detail, RetryAt: v.retryAt,
		Fragment: v.frag,
	}
	switch w.State {
	case model.WorkerDone, model.WorkerError, model.WorkerIdle:
		w.SpeedBps = 0
	case model.WorkerRetrying:
		w.Detail = retryDetail(w.Detail, w.RetryAt, now)
	}
	return w
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
	var le *localError
	var dnsErr *net.DNSError
	var ne net.Error
	switch {
	case err == nil:
		return ""
	case errors.As(err, &se):
		return "HTTP " + strconv.Itoa(se.Code)
	case errors.As(err, &le):
		return "cannot write the fragment"
	case errors.Is(err, errStalled):
		return "no data received"
	case errors.Is(err, ErrRangeMismatch):
		return "server sent another range"
	case errors.Is(err, ErrTruncated), errors.Is(err, io.ErrUnexpectedEOF):
		return "connection closed early"
	case errors.Is(err, media.ErrEncrypted):
		return "encrypted"
	case errors.Is(err, transport.ErrTLS):
		return "TLS verification failed"
	case errors.Is(err, transport.ErrTooLarge):
		return "response too large"
	case errors.Is(err, transport.ErrRedirects):
		return "too many redirects"
	case errors.Is(err, transport.ErrInsecureRedirect):
		return "insecure redirect refused"
	case errors.Is(err, transport.ErrInvalidRequest):
		return "invalid request"
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
