package engine

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"downex/internal/model"
	"downex/internal/transport"
)

// probeResult is the outcome of the first request of a fresh download (§2).
type probeResult struct {
	mode      model.Mode
	size      int64 // -1 when unknown
	streamEnd int64 // last byte the probe response carries, -1 if unknown
	empty     bool  // the resource has zero bytes; there is no body to stream
	// chained is set for single mode with an unknown total ("bytes 0-N/*"): each
	// response may hold only part of the file, so the connection keeps requesting
	// "bytes=pos-" until the server answers 416.
	chained bool
}

// probe sends GET with "Range: bytes=0-" (§2.1), or "bytes=0-(cap-1)" under a host's
// request cap (RequestCaps), retrying transient failures and honouring Retry-After. It
// returns 200, 206 and 416 responses to the caller. An answer that says the link
// expired (§7.2 E1, judged with htmlFile: the download may be a web page itself) is
// ErrLinkExpired, never retried.
func (e *Engine) probe(ctx context.Context, f transport.Fetcher, req model.Request, htmlFile bool) (*transport.Response, error) {
	end := cappedEnd(0, -1, requestCap(e.requestCaps(), "", req.URL))
	var lastErr error
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			if attempt > e.opts.MaxRetries {
				return nil, fmt.Errorf("probe: giving up after %d attempts: %w", attempt, lastErr)
			}
			if err := sleep(ctx, e.clock, backoffDelay(&e.opts, attempt, retryAfterOf(lastErr))); err != nil {
				return nil, err
			}
		}
		resp, err := f.Fetch(ctx, transport.RangeRequest{Request: req, Start: 0, End: end})
		if err == nil {
			if resp.Body == nil {
				resp.Body = http.NoBody
			}
			if expired, why := isExpiry(resp, expiryCheck{sentURL: req.URL, probe: true, htmlFile: htmlFile}); expired {
				closeBody(resp)
				return nil, fmt.Errorf("probe: %s: %w", why, ErrLinkExpired)
			}
			switch resp.StatusCode {
			case http.StatusOK, http.StatusPartialContent, http.StatusRequestedRangeNotSatisfiable:
				return resp, nil
			case http.StatusUnauthorized, http.StatusProxyAuthRequired:
				closeBody(resp)
				return nil, fmt.Errorf("probe: HTTP %d: %w", resp.StatusCode, ErrAuthRequired)
			}
			err = &StatusError{Code: resp.StatusCode, RetryAfter: resp.RetryAfter}
			closeBody(resp)
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !isTransient(err) {
			return nil, fmt.Errorf("probe: %w", err)
		}
		if isMisdirected(err) {
			f.Close() // RFC 9110 §15.5.20: retry over a different connection
		}
		lastErr = err
		e.log.Warn("probe failed, will retry", "attempt", attempt+1, "err", err)
	}
}

// classifyProbe applies the mode selection of §2.2–2.3. It never accepts a response
// as the whole file unless the response proves it:
//   - 206 starting at 0 with a known total ⇒ ranged. "Accept-Ranges: none" selects
//     single mode only when this response carries the whole file; a partial answer can
//     only be completed with more ranges (and a later 200 then fails, never truncates);
//   - 206 with an unknown total ⇒ single, continued with "bytes=pos-" until 416;
//   - 206 without a usable Content-Range ⇒ ErrRangeIgnored: the body's place in the
//     file is unknown and its length is not the file's size (RFC 9110 §15.3.7);
//   - 200 ⇒ single; 416 with total 0 ⇒ an empty resource.
func classifyProbe(resp *transport.Response) (probeResult, error) {
	switch resp.StatusCode {
	case http.StatusPartialContent:
		cr := resp.ContentRange
		if cr == nil {
			return probeResult{}, fmt.Errorf("probe answered 206 without a usable Content-Range: %w", ErrRangeIgnored)
		}
		if cr.Start != 0 || cr.End < cr.Start || (cr.Total >= 0 && cr.End >= cr.Total) {
			return probeResult{}, fmt.Errorf("probe answered with bytes %d-%d/%d: %w", cr.Start, cr.End, cr.Total, ErrRangeIgnored)
		}
		switch {
		case cr.Total < 0:
			return probeResult{mode: model.ModeSingle, size: -1, streamEnd: cr.End, chained: true}, nil
		case resp.AcceptRangesNone && cr.End == cr.Total-1:
			return singleResult(cr.Total), nil
		}
		return probeResult{mode: model.ModeRanged, size: cr.Total, streamEnd: cr.End}, nil
	case http.StatusOK:
		return singleResult(resp.ContentLength), nil
	case http.StatusRequestedRangeNotSatisfiable:
		if cr := resp.ContentRange; cr != nil && cr.Total == 0 {
			return probeResult{mode: model.ModeRanged, size: 0, streamEnd: -1, empty: true}, nil
		}
	}
	return probeResult{}, fmt.Errorf("probe: %w", &StatusError{Code: resp.StatusCode, RetryAfter: resp.RetryAfter})
}

func singleResult(size int64) probeResult {
	r := probeResult{mode: model.ModeSingle, size: max(size, -1), streamEnd: -1}
	if size >= 0 {
		r.streamEnd = size - 1
		r.empty = size == 0
	}
	return r
}

// checkRangeResponse validates the answer to a request starting at start (§6, §7).
// A 416, a changed total or validators that differ from the recorded ones mean the
// remote file changed. So does a 200 when changedOn200 is set (If-Range was sent, or
// the session resumes stored progress); otherwise a 200 means ranges are ignored.
func checkRangeResponse(resp *transport.Response, start, size int64, changedOn200 bool, v model.Validators) error {
	switch resp.StatusCode {
	case http.StatusPartialContent:
		cr := resp.ContentRange
		if cr == nil || cr.Start != start || cr.End < cr.Start || (size >= 0 && cr.End >= size) {
			if cr != nil && cr.Total >= 0 && size >= 0 && cr.Total != size {
				return fmt.Errorf("size changed from %d to %d: %w", size, cr.Total, ErrValidatorChanged)
			}
			return fmt.Errorf("unexpected Content-Range: %w", ErrRangeIgnored)
		}
		if cr.Total >= 0 && size >= 0 && cr.Total != size {
			return fmt.Errorf("size changed from %d to %d: %w", size, cr.Total, ErrValidatorChanged)
		}
		return compareValidators(v, resp.Validators)
	case http.StatusOK:
		if changedOn200 {
			return fmt.Errorf("server sent the whole body instead of a range: %w", ErrValidatorChanged)
		}
		return fmt.Errorf("server sent the whole body: %w", ErrRangeIgnored)
	case http.StatusRequestedRangeNotSatisfiable:
		return fmt.Errorf("range no longer satisfiable: %w", ErrValidatorChanged)
	}
	return &StatusError{Code: resp.StatusCode, RetryAfter: resp.RetryAfter}
}

// compareValidators reports a change of representation visible in a 206's validators,
// so that bytes of two versions are never combined even when no If-Range could be
// sent. Entity tags use the weak comparison of RFC 9110 §8.8.3.2 (different opaque
// tags are different representations, weak or not); Last-Modified dates must be equal
// when both are present. A validator the response omits is not a change.
func compareValidators(want, got model.Validators) error {
	if want.ETag != "" && got.ETag != "" && opaqueTag(want.ETag) != opaqueTag(got.ETag) {
		return fmt.Errorf("ETag changed from %s to %s: %w", want.ETag, got.ETag, ErrValidatorChanged)
	}
	if want.LastModified != "" && got.LastModified != "" && !sameHTTPDate(want.LastModified, got.LastModified) {
		return fmt.Errorf("Last-Modified changed from %s to %s: %w", want.LastModified, got.LastModified, ErrValidatorChanged)
	}
	return nil
}

func opaqueTag(etag string) string { return strings.TrimPrefix(etag, "W/") }

func sameHTTPDate(a, b string) bool {
	ta, errA := http.ParseTime(a)
	tb, errB := http.ParseTime(b)
	if errA != nil || errB != nil {
		return a == b
	}
	return ta.Equal(tb)
}

// ifRangeValidator returns the If-Range value for v (RFC 9110 §13.1.5): the strong
// entity tag; nothing when the only entity tag is weak (a date must not be sent when
// an entity tag exists); else Last-Modified. Without If-Range, changes are still
// caught by compareValidators on every 206.
func ifRangeValidator(v model.Validators) string {
	switch {
	case v.ETag != "" && v.StrongETag:
		return v.ETag
	case v.ETag != "":
		return ""
	}
	return v.LastModified
}
