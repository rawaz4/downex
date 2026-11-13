package transport

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// ErrInsecureRedirect reports a redirect chain that would leave https for plain http.
// Following it would expose the request (its Referer and other headers) and let an
// on-path attacker replace the content, so it is refused (docs/ENGINE-SPEC.md §10:
// never downgrade TLS). Retrying will not help.
var ErrInsecureRedirect = errors.New("redirect from https to http refused")

// redirectPolicy returns an http.Client CheckRedirect function that
//   - follows at most maxRedirects redirects (none when negative), else ErrRedirects;
//   - follows only http and https locations, and never an http location once the
//     chain used https (ErrInsecureRedirect: TLS is never downgraded);
//   - drops credentials once any hop is cross-origin to the initial request, and keeps
//     them dropped for the rest of the chain;
//   - sends only the caller's own Referer, never a synthesised one naming a redirect
//     hop (those often carry signed query strings).
//
// net/http itself copies the initial headers (including Range, If-Range and
// Accept-Encoding) onto each hop and replays a re-readable body on 307/308.
func redirectPolicy(maxRedirects int) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if maxRedirects < 0 {
			return fmt.Errorf("transport: %w: redirects are disabled", ErrRedirects)
		}
		if len(via) > maxRedirects {
			return fmt.Errorf("transport: %w: stopped after %d", ErrRedirects, maxRedirects)
		}
		s := strings.ToLower(req.URL.Scheme)
		if s != "http" && s != "https" {
			return fmt.Errorf("transport: redirect to unsupported scheme %q", s)
		}
		if len(via) == 0 {
			return nil
		}
		if s == "http" && usedTLS(via) {
			return fmt.Errorf("transport: %w (to host %s)", ErrInsecureRedirect, req.URL.Host)
		}
		initial := via[0]
		if crossedOrigin(initial.URL, via[1:], req.URL) {
			// Credentials belong to the initial origin (scheme, host and port).
			for _, k := range [...]string{"Authorization", "Cookie", "Cookie2", "Www-Authenticate"} {
				req.Header.Del(k)
			}
		}
		if initial.Header.Get("Referer") == "" {
			req.Header.Del("Referer")
		}
		return nil
	}
}

// usedTLS reports whether the initial request or any earlier hop was https.
func usedTLS(via []*http.Request) bool {
	for _, r := range via {
		if strings.EqualFold(r.URL.Scheme, "https") {
			return true
		}
	}
	return false
}

// crossedOrigin reports whether next or any intermediate hop differs in origin from initial.
func crossedOrigin(initial *url.URL, hops []*http.Request, next *url.URL) bool {
	o := origin(initial)
	if origin(next) != o {
		return true
	}
	for _, h := range hops {
		if origin(h.URL) != o {
			return true
		}
	}
	return false
}

// origin returns the RFC 6454 origin "scheme://host:port" with the default port made explicit.
func origin(u *url.URL) string {
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
