// Package validate checks download requests from untrusted clients against the limits
// of docs/DAEMON-SPEC.md §4. The daemon applies it to every AddRequest; the
// native-messaging relay applies it before forwarding browser data, so that both
// refuse the same input with the same messages. Messages never repeat header values,
// bodies or URLs, which may carry credentials.
package validate

import (
	"errors"
	"fmt"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"unicode/utf8"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/transport"
)

// Limits on client input (docs/DAEMON-SPEC.md §4).
const (
	MaxURLBytes    = 32 << 10 // longest accepted URL
	MaxBodyBytes   = 1 << 20  // largest accepted POST body
	MaxHeaderBytes = 64 << 10 // total of all header names and values
	MaxHeaderCount = 100      // most header entries
	// MaxFilenameBytes bounds a requested file name before sanitising; the sanitised
	// name is at most transport.MaxFilenameBytes.
	MaxFilenameBytes = 4 << 10
	// MaxMIMEBytes bounds a MIME type hint (docs/DESKTOP-SPEC.md §2), parameters included.
	MaxMIMEBytes = 255
	// MaxTitleRunes bounds a media title hint (docs/BROWSER-SPEC.md §2 addMedia).
	MaxTitleRunes = 512
	// MaxTrackIDBytes bounds a media track id (an HLS `v<i>`/`r<i>` or a DASH
	// Representation@id).
	MaxTrackIDBytes = 1 << 10
)

// Title checks a media title hint (a page or media title, docs/MEDIA-SPEC.md §6): valid
// UTF-8 of at most MaxTitleRunes characters. Control characters (line breaks, tabs)
// become spaces and surrounding space is trimmed; the result is only a hint for the
// file name, which is sanitised separately.
func Title(raw string) (string, error) {
	switch {
	case !utf8.ValidString(raw):
		return "", errors.New("title is not valid UTF-8")
	case utf8.RuneCountInString(raw) > MaxTitleRunes:
		return "", fmt.Errorf("title is longer than %d characters", MaxTitleRunes)
	}
	t := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return ' '
		}
		return r
	}, raw)
	return strings.Join(strings.Fields(t), " "), nil
}

// TrackID checks a requested media track id: "" (the default pick) or at most
// MaxTrackIDBytes of printable UTF-8.
func TrackID(id string) error {
	switch {
	case len(id) > MaxTrackIDBytes:
		return fmt.Errorf("track id is longer than %d bytes", MaxTrackIDBytes)
	case !utf8.ValidString(id) || strings.ContainsFunc(id, func(r rune) bool { return r < 0x20 || r == 0x7f }):
		return errors.New("track id must be printable text")
	}
	return nil
}

// MIME normalises a MIME type hint used for category selection: parameters (";
// charset=...") are dropped, surrounding space is trimmed and the type is lower-cased.
// Hints longer than MaxMIMEBytes or containing control characters are refused. A hint
// that is not a "type/subtype" pair of RFC 6838 names (non-ASCII text included) is
// only a bad hint, not a bad request: it is dropped ("" and no error).
func MIME(raw string) (string, error) {
	switch {
	case len(raw) > MaxMIMEBytes:
		return "", fmt.Errorf("mime is longer than %d bytes", MaxMIMEBytes)
	case strings.ContainsFunc(raw, func(r rune) bool { return r < 0x20 || r == 0x7f }):
		return "", errors.New("mime must not contain control characters")
	}
	t, _, _ := strings.Cut(raw, ";")
	t = strings.ToLower(strings.TrimSpace(t))
	typ, sub, ok := strings.Cut(t, "/")
	if !ok || !mimeName(typ) || !mimeName(sub) {
		return "", nil
	}
	return t, nil
}

// mimeName reports an RFC 6838 restricted-name: 1-127 characters, starting with a
// letter or digit, of letters, digits and !#$&-^_.+ (lower-case input).
func mimeName(s string) bool {
	if s == "" || len(s) > 127 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case i > 0 && strings.IndexByte("!#$&-^_.+", c) >= 0:
		default:
			return false
		}
	}
	return true
}

// ExpectedSize checks a requester's size hint in bytes: -1 and 0 mean unknown and are
// returned as 0; other negative values are refused.
func ExpectedSize(n int64) (int64, error) {
	if n < -1 {
		return 0, errors.New("expected_size must be -1 (unknown) or a number of bytes")
	}
	return max(n, 0), nil
}

// URL accepts absolute http and https URLs with a host, at most MaxURLBytes long.
func URL(raw string) error {
	switch {
	case raw == "":
		return errors.New("url is required")
	case len(raw) > MaxURLBytes:
		return fmt.Errorf("url is longer than %d bytes", MaxURLBytes)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("url is malformed")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("url scheme must be http or https")
	}
	if u.Host == "" || u.Hostname() == "" {
		return errors.New("url has no host")
	}
	return nil
}

// Method returns the canonical method: GET (also for "") or POST, in any case.
func Method(m string) (string, error) {
	switch {
	case m == "", strings.EqualFold(m, http.MethodGet):
		return http.MethodGet, nil
	case strings.EqualFold(m, http.MethodPost):
		return http.MethodPost, nil
	}
	return "", errors.New("method must be GET or POST")
}

// Body accepts a request body of at most MaxBodyBytes, and a non-empty one only with
// POST. method is canonical (see Method).
func Body(n int, method string) error {
	switch {
	case n > MaxBodyBytes:
		return fmt.Errorf("body is larger than %d bytes", MaxBodyBytes)
	case n > 0 && method != http.MethodPost:
		return errors.New("a body is only allowed with POST")
	}
	return nil
}

// Headers validates and converts client headers: at most MaxHeaderCount entries of at
// most MaxHeaderBytes in total. Names must be RFC 9110 tokens, values must not contain
// CR, LF or other control characters (horizontal tab excepted), and hop-by-hop,
// framing and range headers are refused because the engine and transport own them.
// Messages name the header but never its value. No headers give a nil Header.
func Headers(hs []*downexv1.Header) (http.Header, error) {
	if len(hs) > MaxHeaderCount {
		return nil, fmt.Errorf("at most %d headers are allowed", MaxHeaderCount)
	}
	if len(hs) == 0 {
		return nil, nil
	}
	h := make(http.Header, len(hs))
	total := 0
	for i, x := range hs {
		name, value := x.GetName(), x.GetValue()
		if !isToken(name) {
			return nil, fmt.Errorf("header %d has an invalid name %q", i+1, truncate(name, 64))
		}
		key := textproto.CanonicalMIMEHeaderKey(name)
		if !validHeaderValue(value) {
			return nil, fmt.Errorf("header %s contains CR, LF or other control characters", key)
		}
		if forbiddenHeader(key) {
			return nil, fmt.Errorf("header %s may not be set by clients", key)
		}
		if total += len(name) + len(value); total > MaxHeaderBytes {
			return nil, fmt.Errorf("headers are larger than %d bytes", MaxHeaderBytes)
		}
		h.Add(key, value)
	}
	return h, nil
}

// forbiddenHeader reports hop-by-hop, framing and range headers (canonical form).
// Accept-Encoding and cache conditionals, which browsers send routinely, are allowed:
// the engine replaces or removes them.
func forbiddenHeader(key string) bool {
	switch key {
	case "Host", "Content-Length", "Transfer-Encoding", "Connection", "Keep-Alive", "Proxy-Connection",
		"Te", "Trailer", "Upgrade", "Expect", "Http2-Settings", "Range", "If-Range",
		"Proxy-Authorization", "Proxy-Authenticate":
		return true
	}
	return false
}

// Filename refuses names that address another directory and sanitises the rest with
// transport.SanitizeFilename. "" means the engine chooses the name.
func Filename(name string) (string, error) {
	switch {
	case name == "":
		return "", nil
	case len(name) > MaxFilenameBytes:
		return "", fmt.Errorf("filename is longer than %d bytes", MaxFilenameBytes)
	case strings.ContainsAny(name, `/\`):
		return "", errors.New("filename must not contain a path separator")
	case name == "." || name == "..":
		return "", errors.New("filename must name a file")
	}
	return transport.SanitizeFilename(name), nil
}

// isToken reports whether s is a non-empty RFC 9110 token.
func isToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}

// validHeaderValue reports a field value without control characters other than
// horizontal tab (RFC 9110 §5.5), which rules out header injection through CR/LF.
func validHeaderValue(v string) bool {
	for i := 0; i < len(v); i++ {
		if c := v[i]; (c < 0x20 && c != '\t') || c == 0x7f {
			return false
		}
	}
	return true
}

// truncate shortens s to at most n bytes for messages.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
