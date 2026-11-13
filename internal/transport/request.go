package transport

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
)

// newHTTPRequest builds the wire request for rr: caller headers minus the ones the
// transport owns, then User-Agent, Range, If-Range and Accept-Encoding: identity.
func newHTTPRequest(ctx context.Context, rr RangeRequest, userAgent string) (*http.Request, error) {
	rangeValue, err := rangeHeaderValue(rr.Start, rr.End)
	if err != nil {
		return nil, err
	}
	if rr.IfRange != "" && !validIfRange(rr.IfRange) {
		return nil, fmt.Errorf("transport: %w: If-Range needs a strong entity tag or an HTTP-date", ErrInvalidRequest)
	}
	method := rr.Method
	if method == "" {
		method = http.MethodGet
	}
	if !isToken(method) {
		return nil, fmt.Errorf("transport: %w: method %q", ErrInvalidRequest, method)
	}
	var body io.Reader
	if len(rr.Body) > 0 {
		// A *bytes.Reader gives the request a GetBody, so 307/308 redirects replay it.
		body = bytes.NewReader(rr.Body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rr.URL, body)
	if err != nil {
		return nil, fmt.Errorf("transport: %w: %w", ErrInvalidRequest, err)
	}
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return nil, fmt.Errorf("transport: %w: unsupported URL scheme %q", ErrInvalidRequest, req.URL.Scheme)
	}
	if req.URL.Host == "" {
		return nil, fmt.Errorf("transport: %w: URL has no host", ErrInvalidRequest)
	}
	if err := copyRequestHeaders(req.Header, rr.Headers); err != nil {
		return nil, err
	}
	if _, ok := req.Header["User-Agent"]; !ok {
		req.Header.Set("User-Agent", userAgent)
	}
	req.Header.Set("Range", rangeValue)
	req.Header.Set("Accept-Encoding", "identity")
	if rr.IfRange != "" {
		req.Header.Set("If-Range", rr.IfRange)
	}
	return req, nil
}

// rangeHeaderValue formats a single byte range (RFC 9110 §14.1.2): "bytes=S-E", or
// "bytes=S-" when end is -1.
func rangeHeaderValue(start, end int64) (string, error) {
	switch {
	case start < 0:
		return "", fmt.Errorf("transport: %w: negative range start %d", ErrInvalidRequest, start)
	case end == -1:
		return "bytes=" + strconv.FormatInt(start, 10) + "-", nil
	case end < start:
		return "", fmt.Errorf("transport: %w: range end %d before start %d", ErrInvalidRequest, end, start)
	default:
		return "bytes=" + strconv.FormatInt(start, 10) + "-" + strconv.FormatInt(end, 10), nil
	}
}

// validIfRange reports whether v may be sent as If-Range: RFC 9110 §13.1.5 forbids weak
// entity tags, so only a strong tag or an HTTP-date qualifies.
func validIfRange(v string) bool {
	if isStrongETag(v) {
		return true
	}
	_, err := http.ParseTime(v)
	return err == nil
}

// copyRequestHeaders copies the caller's headers (Cookie, Referer, User-Agent,
// Authorization, ...) except hop-by-hop headers, headers the transport sets itself,
// and cache conditionals that would turn a range request into a bodiless 304.
func copyRequestHeaders(dst, src http.Header) error {
	if len(src) == 0 {
		return nil
	}
	connTokens := connectionTokens(src)
	for k, vv := range src {
		if !isToken(k) {
			return fmt.Errorf("transport: %w: header name %q", ErrInvalidRequest, k)
		}
		ck := textproto.CanonicalMIMEHeaderKey(k)
		if transportOwnedHeader(ck) || connTokens[ck] {
			continue
		}
		for _, v := range vv {
			if !validHeaderValue(v) {
				return fmt.Errorf("transport: %w: value of header %s", ErrInvalidRequest, ck)
			}
			dst.Add(ck, v)
		}
	}
	return nil
}

// transportOwnedHeader reports headers a caller must not set on a range request.
func transportOwnedHeader(canonicalKey string) bool {
	switch canonicalKey {
	case "Host", "Content-Length", "Transfer-Encoding", "Te", "Trailer", "Upgrade",
		"Connection", "Keep-Alive", "Proxy-Connection", "Proxy-Authorization", "Proxy-Authenticate",
		"Expect", "Range", "If-Range", "Accept-Encoding",
		"If-Match", "If-None-Match", "If-Modified-Since", "If-Unmodified-Since":
		return true
	}
	return false
}

// connectionTokens returns the header names listed in Connection, which are hop-by-hop
// for this connection only (RFC 9110 §7.6.1).
func connectionTokens(h http.Header) map[string]bool {
	var out map[string]bool
	for k, vv := range h {
		if textproto.CanonicalMIMEHeaderKey(k) != "Connection" {
			continue
		}
		for _, v := range vv {
			for _, tok := range strings.Split(v, ",") {
				tok = strings.TrimSpace(tok)
				if tok == "" {
					continue
				}
				if out == nil {
					out = make(map[string]bool)
				}
				out[textproto.CanonicalMIMEHeaderKey(tok)] = true
			}
		}
	}
	return out
}

// isToken reports whether s is a non-empty RFC 9110 token (header names, methods).
func isToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isTokenChar(s[i]) {
			return false
		}
	}
	return true
}

func isTokenChar(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	return strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0
}

// validHeaderValue reports whether v is a field value without control characters other
// than horizontal tab (RFC 9110 §5.5), which also rules out header injection via CR/LF.
func validHeaderValue(v string) bool {
	for i := 0; i < len(v); i++ {
		c := v[i]
		if (c < 0x20 && c != '\t') || c == 0x7f {
			return false
		}
	}
	return true
}
