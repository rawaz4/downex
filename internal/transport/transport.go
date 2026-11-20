// Package transport performs single HTTP(S) range requests for the engine.
// Each worker slot owns its own Fetcher so connections are never coalesced
// (HTTP/2 would otherwise multiplex every slot onto one TCP connection).
package transport

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"downex/internal/model"
)

// RangeRequest asks for bytes [Start, End] of a resource. End = -1 requests an open range
// ("bytes=Start-"). Start = 0 with End = -1 is the probe request.
type RangeRequest struct {
	model.Request
	Start   int64
	End     int64
	IfRange string // optional If-Range validator (strong ETag or HTTP-date)
	// Whole asks for the complete representation with no Range header (Start 0, End -1,
	// no IfRange), for a document that is read whole, such as a playlist: a cache may
	// answer a range request from another copy than a plain request (one measured
	// answered with a gzip-coded copy despite Accept-Encoding: identity).
	Whole bool
}

// ContentRange is a parsed Content-Range header. Total is -1 when the server sent "*".
type ContentRange struct {
	Start, End, Total int64
}

// Response is the result of one range request. The caller must close Body.
type Response struct {
	StatusCode       int
	Header           http.Header
	Body             io.ReadCloser
	ContentLength    int64         // -1 when unknown
	ContentRange     *ContentRange // nil when absent or unparsable
	AcceptRangesNone bool          // "Accept-Ranges: none"
	Validators       model.Validators
	FinalURL         string // URL after redirects
	Filename         string // sanitised name from Content-Disposition, "" if none
	// FilenameControls: the Content-Disposition name held hidden text-direction
	// characters that sanitising removed (HiddenControls; docs/DESKTOP-SPEC.md §9.5 N1).
	FilenameControls bool
	ContentType      string        // media type, lower case, without parameters
	RetryAfter       time.Duration // parsed Retry-After for 429/503, 0 if none
	// ContentCoding lists the response's content codings (RFC 9110 §8.4) in the order
	// they were applied, lower case, without "identity"; nil when the body is the
	// representation itself. Requests ask for identity, but a server may still code
	// the body, and then its bytes (and Content-Range) are those of the coded data.
	ContentCoding []string
}

// Fetcher performs requests over a connection owned by one worker slot.
type Fetcher interface {
	Fetch(ctx context.Context, req RangeRequest) (*Response, error)
	// Close releases the slot's idle connections.
	Close()
}

// Factory creates one Fetcher per worker slot.
type Factory interface {
	NewSlot() Fetcher
}

// Errors returned by transports. Wrap them with fmt.Errorf("...: %w", err).
var (
	ErrTLS       = errors.New("tls verification failed")
	ErrTooLarge  = errors.New("header or body exceeds limit")
	ErrRedirects = errors.New("too many redirects")
	// ErrGone reports a transport that cannot serve a request and never will: the browser
	// that fetched for Downex stopped, or what it received is not the media
	// (docs/SITE-MEDIA-SPEC.md §2.8). Retrying will not help.
	ErrGone = errors.New("the transport can no longer serve this request")
)
