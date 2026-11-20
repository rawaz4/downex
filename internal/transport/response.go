package transport

import (
	"context"
	"mime"
	"net/http"
	"strings"
	"time"

	"downex/internal/model"
)

// newResponse converts a net/http response. now is the local time the headers arrived;
// retire (nil = none) closes the idle connections of the slot that received it.
func newResponse(hr *http.Response, cancel context.CancelFunc, now time.Time, retire func()) *Response {
	h := hr.Header
	r := &Response{
		StatusCode:       hr.StatusCode,
		Header:           h,
		Body:             newResponseBody(hr, cancel, retire),
		ContentLength:    hr.ContentLength,
		ContentRange:     responseContentRange(hr.StatusCode, h.Get("Content-Range")),
		AcceptRangesNone: acceptRangesNone(h),
		Validators:       parseValidators(h),
		Filename:         filenameFromDisposition(h.Get("Content-Disposition")),
		ContentType:      mediaType(h.Get("Content-Type")),
		ContentCoding:    contentCoding(h),
	}
	if v := h.Get("Content-Disposition"); v != "" {
		_, raw := ParseContentDisposition(v)
		r.FilenameControls = r.Filename != "" && HiddenControls(raw)
	}
	if r.ContentLength < 0 {
		r.ContentLength = -1
	}
	if hr.Request != nil && hr.Request.URL != nil {
		r.FinalURL = hr.Request.URL.String()
	}
	if hr.StatusCode == http.StatusTooManyRequests || hr.StatusCode == http.StatusServiceUnavailable {
		r.RetryAfter = retryAfterFromHeader(h, now)
	}
	return r
}

// responseContentRange returns the Content-Range that is meaningful for status
// (RFC 9110 §14.4): a satisfied range for 206, an unsatisfied "*/total" for 416.
// Any other combination, or an unparsable value, yields nil.
func responseContentRange(status int, v string) *ContentRange {
	if v == "" {
		return nil
	}
	cr, err := ParseContentRange(v)
	if err != nil {
		return nil
	}
	switch {
	case status == http.StatusPartialContent && cr.Start >= 0:
		return &cr
	case status == http.StatusRequestedRangeNotSatisfiable && cr.Start < 0:
		return &cr
	}
	return nil
}

// acceptRangesNone reports "Accept-Ranges: none" (RFC 9110 §14.3). A list that also
// names "bytes" is not "none".
func acceptRangesNone(h http.Header) bool {
	none := false
	for _, line := range h.Values("Accept-Ranges") {
		for _, tok := range strings.Split(line, ",") {
			switch strings.ToLower(strings.TrimSpace(tok)) {
			case "none":
				none = true
			case "bytes":
				return false
			}
		}
	}
	return none
}

// parseValidators extracts the entity tag and Last-Modified date (RFC 9110 §8.8).
// An ETag counts as strong only when it is a well-formed quoted tag without the "W/"
// prefix; Last-Modified is kept verbatim (If-Range compares it exactly) when it parses
// as an HTTP-date.
func parseValidators(h http.Header) model.Validators {
	var v model.Validators
	if etag := strings.Trim(h.Get("ETag"), " \t"); etag != "" {
		v.ETag = etag
		v.StrongETag = isStrongETag(etag)
	}
	if lm := strings.Trim(h.Get("Last-Modified"), " \t"); lm != "" {
		if _, err := http.ParseTime(lm); err == nil {
			v.LastModified = lm
		}
	}
	return v
}

// isStrongETag reports whether s is a strong entity-tag: DQUOTE *etagc DQUOTE.
func isStrongETag(s string) bool {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return false
	}
	for i := 1; i < len(s)-1; i++ {
		if c := s[i]; c == '"' || c < 0x21 || c == 0x7f {
			return false
		}
	}
	return true
}

// mediaType returns the lower-cased media type of a Content-Type value without
// parameters, e.g. "application/zip"; "" when absent.
// contentCoding parses Content-Encoding (RFC 9110 §8.4): the codings of every field line,
// in order, lower case. "identity" codes nothing and is left out; a value that is not a
// token is kept as it is, so that a caller refuses it rather than reading coded bytes.
func contentCoding(h http.Header) []string {
	var out []string
	for _, line := range h.Values("Content-Encoding") {
		for _, c := range strings.Split(line, ",") {
			c = strings.ToLower(strings.TrimSpace(c))
			if c != "" && c != "identity" {
				out = append(out, c)
			}
		}
	}
	return out
}

func mediaType(v string) string {
	if v == "" {
		return ""
	}
	if mt, _, err := mime.ParseMediaType(v); err == nil {
		return mt
	}
	mt, _, _ := strings.Cut(v, ";")
	return strings.ToLower(strings.TrimSpace(mt))
}
