package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"runtime"
	"syscall"
	"time"

	"downex/internal/transport"
)

// Sentinel errors (docs/ENGINE-SPEC.md §10). Errors returned by Run wrap them with %w.
var (
	// ErrRangeIgnored reports a server that answered a range request with the wrong range
	// or with the whole body although no validator was sent.
	ErrRangeIgnored = errors.New("engine: server ignored the requested byte range")
	// ErrValidatorChanged reports that the remote file changed (If-Range failed, the size
	// changed, or a resumed range became unsatisfiable). Call Engine.Restart, then Run.
	ErrValidatorChanged = errors.New("engine: remote file changed")
	// ErrDiskFull reports that the part file could not be written or extended.
	ErrDiskFull = errors.New("engine: disk full")
	// ErrStalled reports a connection that made no progress within Options.StallTimeout
	// in single-connection mode, where its range cannot be reassigned.
	ErrStalled = errors.New("engine: connection stalled")
	// ErrAlreadyRunning is returned when Run or Restart is called for a download that is
	// already being run by this Engine.
	ErrAlreadyRunning = errors.New("engine: download is already running")
	// ErrNotRunning is returned by live changes (SetMaxConnections) for a download that
	// this Engine is not running.
	ErrNotRunning = errors.New("engine: download is not running")
	// ErrCorruptCheckpoint reports persisted segments or a part file that cannot be
	// resumed safely. Call Engine.Restart, then Run.
	ErrCorruptCheckpoint = errors.New("engine: stored progress is inconsistent")
	// ErrInvalidRequest reports an Add call with an unusable request.
	ErrInvalidRequest = errors.New("engine: invalid request")
	// ErrTLS is transport.ErrTLS, re-exported for callers of this package.
	ErrTLS = transport.ErrTLS
	// ErrAuthRequired reports a server (401) or proxy (407) that demands credentials
	// which are missing or were rejected (transport.ErrAuthRequired, re-exported). The
	// daemon maps it to the auth_required error code so the UI can offer a site login.
	ErrAuthRequired = transport.ErrAuthRequired
)

// StatusError reports an HTTP status the engine could not use.
type StatusError struct {
	Code       int
	RetryAfter time.Duration // from Retry-After on 429/503, 0 if absent
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("unexpected HTTP status %d %s", e.Code, http.StatusText(e.Code))
}

// isTransient reports whether a failed request may succeed when retried.
// Unknown errors (network failures, truncated bodies) are transient; retries are bounded.
func isTransient(err error) bool {
	if err == nil || isContextError(err) {
		return false
	}
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
	for _, permanent := range []error{
		ErrRangeIgnored, ErrValidatorChanged, ErrDiskFull, ErrCorruptCheckpoint, ErrInvalidRequest,
		transport.ErrTLS, transport.ErrTooLarge, transport.ErrRedirects, transport.ErrInvalidRequest,
		transport.ErrInsecureRedirect, transport.ErrAuthRequired,
	} {
		if errors.Is(err, permanent) {
			return false
		}
	}
	return true
}

// isThrottle reports a status that asks the client to back off and lowers the per-host cap.
func isThrottle(err error) bool {
	var se *StatusError
	if !errors.As(err, &se) {
		return false
	}
	return se.Code == http.StatusTooManyRequests || se.Code == http.StatusServiceUnavailable ||
		se.Code == http.StatusMisdirectedRequest
}

// noResponseError marks a request that failed before any response arrived.
type noResponseError struct{ err error }

func (e *noResponseError) Error() string { return e.err.Error() }
func (e *noResponseError) Unwrap() error { return e.err }

// isRefused reports a request whose connection the server refused, reset or closed
// before it answered: what a server at its connection limit does instead of 503 (§10).
// Timeouts and name or route failures are not refusals.
func isRefused(err error) bool {
	var nr *noResponseError
	if !errors.As(err, &nr) {
		return false
	}
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		isSocketErr(err, syscall.ECONNREFUSED, wsaConnRefused) ||
		isSocketErr(err, syscall.ECONNRESET, wsaConnReset)
}

// isMisdirected reports 421 Misdirected Request, which is retried over a new connection.
func isMisdirected(err error) bool {
	var se *StatusError
	return errors.As(err, &se) && se.Code == http.StatusMisdirectedRequest
}

func retryAfterOf(err error) time.Duration {
	var se *StatusError
	if errors.As(err, &se) {
		return se.RetryAfter
	}
	return 0
}

func isContextError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// backoffDelay returns the wait before attempt number failures+1: Retry-After when the
// server sent one, else exponential backoff from RetryBaseDelay with ±20% jitter.
func backoffDelay(o *Options, failures int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		return retryAfter
	}
	d := o.RetryBaseDelay
	for i := 1; i < failures && d < o.RetryMaxDelay; i++ {
		d *= 2
	}
	d = min(d, o.RetryMaxDelay)
	return time.Duration(float64(d) * (0.8 + 0.4*rand.Float64()))
}

// Windows error numbers that have no portable syscall constant.
const (
	winErrorAccessDenied     = syscall.Errno(5)
	winErrorSharingViolation = syscall.Errno(32)
	winErrorLockViolation    = syscall.Errno(33)
	winErrorHandleDiskFull   = syscall.Errno(39)
	winErrorDiskFull         = syscall.Errno(112)
)

// isDiskFull maps ENOSPC and ERROR_DISK_FULL / ERROR_HANDLE_DISK_FULL.
func isDiskFull(err error) bool {
	if errors.Is(err, syscall.ENOSPC) {
		return true
	}
	var errno syscall.Errno
	if runtime.GOOS == "windows" && errors.As(err, &errno) {
		return errno == winErrorDiskFull || errno == winErrorHandleDiskFull
	}
	return false
}

// diskError wraps a file error, tagging it with ErrDiskFull when the volume is full.
func diskError(op string, err error) error {
	if isDiskFull(err) {
		return fmt.Errorf("%s: %w: %w", op, ErrDiskFull, err)
	}
	return fmt.Errorf("%s: %w", op, err)
}

// isSharingViolation reports transient Windows errors raised while another process
// (an indexer or virus scanner) briefly holds the file open.
func isSharingViolation(err error) bool {
	var errno syscall.Errno
	if runtime.GOOS != "windows" || !errors.As(err, &errno) {
		return false
	}
	return errno == winErrorSharingViolation || errno == winErrorLockViolation ||
		errno == winErrorAccessDenied
}
