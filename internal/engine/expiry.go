package engine

import (
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"downex/internal/model"
	"downex/internal/transport"
)

// expiryCheck says what was asked when a response is judged by isExpiry
// (docs/ENGINE-SPEC.md §7.2, rule E1).
type expiryCheck struct {
	sentURL string // the URL the request was sent to (before redirects)
	// rangedRequest: the request asked for a range of a ranged download (a segment,
	// resumed or not), so a 200 web page instead of the range is an expiry (E1 d).
	rangedRequest bool
	// probe: the first request of a fresh download, whose redirect to a web page is an
	// expiry unless the download is an HTML file (E1 e).
	probe    bool
	htmlFile bool // the download is (or may be) an HTML file: E1 e never applies
}

// isExpiry reports whether resp says that the link stopped working (E1), and why in a
// few words for the error message (never the URL). It is checked after the pinned
// target's fallback (pin.go) and before the other status checks.
//   - (a) 403 or 410;
//   - (b) 401 without any WWW-Authenticate challenge (with one it is auth_required);
//   - (c) a redirected request whose final answer is 404 (a plain 404 is a removed file);
//   - (d) 200 with an HTML type answering a range request of a ranged download;
//   - (e) a redirected probe whose 2xx answer is HTML, for a download that is no HTML file.
//
// A 206 is never an expiry: its Content-Range is checked as usual.
func isExpiry(resp *transport.Response, c expiryCheck) (bool, string) {
	switch resp.StatusCode {
	case http.StatusForbidden, http.StatusGone:
		return true, statusText(resp.StatusCode)
	case http.StatusUnauthorized:
		if len(resp.Header.Values("WWW-Authenticate")) == 0 {
			return true, statusText(resp.StatusCode)
		}
		return false, ""
	case http.StatusNotFound:
		if redirected(c.sentURL, resp.FinalURL) {
			return true, "redirected to " + statusText(resp.StatusCode)
		}
		return false, ""
	}
	if !isHTMLType(resp.ContentType) || resp.StatusCode == http.StatusPartialContent {
		return false, ""
	}
	switch {
	case resp.StatusCode == http.StatusOK && c.rangedRequest:
		return true, "a web page (" + servedType(resp.ContentType) + ") instead of the range"
	case c.probe && !c.htmlFile && resp.StatusCode >= 200 && resp.StatusCode < 300 && redirected(c.sentURL, resp.FinalURL):
		return true, "redirected to a web page (" + servedType(resp.ContentType) + ")"
	}
	return false, ""
}

// expiryError is the error of an expiry of the request for bytes [pos, end] (E2):
// it names the range and the answer, never the URL.
func expiryError(pos, end int64, why string) error {
	if end < 0 {
		return fmt.Errorf("bytes %d-: %s: %w", pos, why, ErrLinkExpired)
	}
	return fmt.Errorf("bytes %d-%d: %s: %w", pos, end, why, ErrLinkExpired)
}

func statusText(code int) string { return fmt.Sprintf("HTTP %d %s", code, http.StatusText(code)) }

// redirected reports whether a request sent to sent ended at final, another URL, after
// following redirects. Both are compared in their parsed form.
func redirected(sent, final string) bool {
	if final == "" || sent == "" {
		return false
	}
	a, err1 := url.Parse(sent)
	b, err2 := url.Parse(final)
	if err1 != nil || err2 != nil {
		return sent != final
	}
	return a.String() != b.String()
}

// isHTMLType reports the media types of web pages.
func isHTMLType(mediaType string) bool {
	mediaType = servedType(mediaType)
	return mediaType == "text/html" || mediaType == "application/xhtml+xml"
}

// htmlExts are the extensions of files that are web pages themselves.
var htmlExts = map[string]bool{"htm": true, "html": true, "xhtml": true, "shtml": true, "mht": true, "mhtml": true}

// isHTMLFile reports whether d may itself be a web page, so a probe redirected to one is
// no expiry (E1 e): it has no name yet, its name has an HTML extension, or its MIME
// hint is an HTML type.
func isHTMLFile(d *model.Download) bool {
	if d.Filename == "" || isHTMLType(d.MIME) {
		return true
	}
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(d.Filename), "."))
	return htmlExts[ext]
}
