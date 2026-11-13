package engine

import (
	"net/http"
	"net/url"
	"strings"

	"downex/internal/model"
)

// pinnedRequest aims req directly at finalURL, the URL the probe's redirects ended at
// and whose validators were recorded, so that the segments of one session all come
// from that origin instead of re-following a redirector that may pick another mirror
// (with other entity tags) for every request. Credentials are dropped when finalURL is
// another origin, as following the redirect itself would have done.
//
// ok is false and req is returned unchanged when there is nothing to pin (no
// redirect), when finalURL would downgrade https, or when the request is not a plain
// GET: a redirect may have rewritten its method and body.
func pinnedRequest(req model.Request, finalURL string) (pinned model.Request, ok bool) {
	if finalURL == "" || finalURL == req.URL || req.Method != http.MethodGet || len(req.Body) > 0 {
		return req, false
	}
	from, err1 := url.Parse(req.URL)
	to, err2 := url.Parse(finalURL)
	if err1 != nil || err2 != nil || to.Host == "" {
		return req, false
	}
	switch {
	case strings.EqualFold(to.Scheme, "https"):
	case strings.EqualFold(to.Scheme, "http") && !strings.EqualFold(from.Scheme, "https"):
	default:
		return req, false
	}
	pinned = req
	pinned.URL = finalURL
	if originOf(from) != originOf(to) {
		h := req.Headers.Clone()
		for _, k := range [...]string{"Authorization", "Cookie", "Cookie2"} {
			h.Del(k)
		}
		pinned.Headers = h
	}
	return pinned, true
}

// originOf returns the RFC 6454 origin "scheme://host:port" with the default port.
func originOf(u *url.URL) string {
	scheme := strings.ToLower(u.Scheme)
	port := u.Port()
	if port == "" {
		switch scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		}
	}
	return scheme + "://" + strings.ToLower(u.Hostname()) + ":" + port
}

// pinRefused reports statuses with which a pinned target says it no longer serves the
// request (a signed URL expired, a mirror dropped the file); the original URL is then
// used again, which also re-validates the representation.
func pinRefused(status int) bool {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusGone:
		return true
	}
	return false
}

// pinTo sends this session's segment requests to finalURL (see pinnedRequest) and
// keeps the original request as the fallback for a target that refuses them.
func (j *job) pinTo(finalURL string) {
	if pinned, ok := pinnedRequest(j.req, finalURL); ok {
		orig := j.req
		j.req, j.fallback = pinned, &orig
	}
}
