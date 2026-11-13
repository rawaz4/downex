package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ErrInvalidRequest reports a RangeRequest that cannot be sent as given: a bad URL or
// scheme, range, method, header, or If-Range validator. Retrying it will not help.
var ErrInvalidRequest = errors.New("invalid request")

// drainOnAuthRetry bounds how much of a 401/407 body is read so the connection can be
// reused for the next authentication leg (NTLM authenticates the connection).
const drainOnAuthRetry = 64 << 10

// httpFetcher is one worker slot: an http.Client over a transport that no other slot uses.
type httpFetcher struct {
	transport   *http.Transport
	client      *http.Client
	userAgent   string
	credentials Credentials
	maxRedirect int
	now         func() time.Time
}

// Fetch sends one range request and returns the response with its body still unread.
// Non-2xx statuses are not errors; they are reported in Response.StatusCode so the
// caller can act on 416, 429 or 503. Errors wrap ErrTLS, ErrTooLarge, ErrRedirects,
// ErrInvalidRequest or the context error where they apply.
//
// A 401 or 407 is answered from the factory's Credentials (Basic, Digest, NTLM/
// Negotiate) when a login matches; the authenticated response is returned. Server
// credentials are only ever sent to the origin that issued the challenge (the retry
// goes straight to the URL that challenged, so the hops that led there never see
// them), and never over plain HTTP to a different origin than the request started
// from. Proxy credentials are only sent to the HTTP proxy that answered 407 to a request
// it forwarded; a 407 from an origin (direct, or inside a CONNECT tunnel) is returned
// as is.
//
// With HTTP/1.1 a slot holds one connection per host, so a second Fetch to the same
// host blocks until the previous response body is closed.
func (f *httpFetcher) Fetch(ctx context.Context, rr RangeRequest) (*Response, error) {
	if ctx == nil {
		return nil, fmt.Errorf("transport: %w: nil context", ErrInvalidRequest)
	}
	// The request context lives until the body is closed; the body uses it to bound
	// draining (see responseBody.Close). It carries the Fetch's state to hopTransport.
	state := &fetchState{}
	reqCtx, cancel := context.WithCancel(context.WithValue(ctx, fetchStateKey{}, state))
	origin, err := url.Parse(rr.URL)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("transport: %w: %w", ErrInvalidRequest, err)
	}

	var server *authFlow
	rounds := 0
	for {
		req, err := newHTTPRequest(reqCtx, rr, f.userAgent)
		if err != nil {
			cancel()
			return nil, err
		}
		if v, ok := server.pending(); ok {
			req.Header.Set("Authorization", v)
		}
		hr, err := f.client.Do(req)
		if err != nil {
			cancel()
			return nil, classifyError(err)
		}
		if f.credentials != nil && rounds < maxAuthRounds {
			retry, aerr := f.authenticate(hr, origin, state, &server)
			if aerr != nil {
				_ = hr.Body.Close()
				cancel()
				return nil, aerr
			}
			if retry {
				rounds++
				rr = retarget(rr, origin, req, hr)
				drainForReuse(hr)
				continue
			}
		}
		if server != nil {
			server.done(hr.StatusCode != http.StatusUnauthorized)
		}
		state.done(hr.StatusCode != http.StatusProxyAuthRequired)
		return newResponse(hr, cancel, f.now(), f.transport.CloseIdleConnections), nil
	}
}

// authenticate inspects a 401 or 407 response and, when a login answers the challenge,
// prepares the next request's credentials and reports retry = true. It never returns an
// error for a plain missing login (the challenge response is returned to the caller,
// which maps it to auth_required); it errors only on a context failure.
//
// A flow stays bound to the origin (401) or proxy (407) it began with: a challenge from
// another one during the same exchange is not answered.
func (f *httpFetcher) authenticate(hr *http.Response, origin *url.URL, state *fetchState, server **authFlow) (bool, error) {
	switch hr.StatusCode {
	case http.StatusUnauthorized:
		challengeURL := requestURL(hr, origin)
		if !f.mayAuthenticate(origin, challengeURL) {
			return false, nil
		}
		switch {
		case *server == nil:
			*server = &authFlow{
				field:  "Www-Authenticate",
				req:    serverAuthRequest(hr, challengeURL),
				login:  f.serverLogin(challengeURL),
				origin: originString(challengeURL),
			}
		case (*server).origin != originString(challengeURL):
			return false, nil
		}
		_, ok := (*server).respond(hr.Header.Values("Www-Authenticate"))
		return ok, nil
	case http.StatusProxyAuthRequired:
		// Only the HTTP proxy that forwarded the request can have sent this 407.
		p, forwarded := state.lastForwardedBy()
		if !forwarded {
			return false, nil
		}
		flow, ok := state.proxyFlow(p, func() *authFlow {
			return &authFlow{
				field: "Proxy-Authenticate",
				req:   proxyAuthRequest(hr, origin),
				login: f.proxyLogin(p),
			}
		})
		if !ok {
			return false, nil
		}
		_, ok = flow.respond(hr.Header.Values("Proxy-Authenticate"))
		return ok, nil
	}
	return false, nil
}

// retarget aims the next attempt at the URL whose response asked for credentials
// (hr's request, when redirects led away from sent, the first request of this attempt),
// so that the credentials prepared for it travel on the first hop to that URL and never
// to the hops before it. A redirect that turned the request into a GET is kept, and
// credentials the caller gave for the first origin stay behind when the challenger is
// another origin (as a cross-origin redirect drops them).
func retarget(rr RangeRequest, origin *url.URL, sent *http.Request, hr *http.Response) RangeRequest {
	if hr.Request == nil || hr.Request.URL == nil || hr.Request.URL.String() == sent.URL.String() {
		return rr
	}
	u := hr.Request.URL
	out := rr
	out.URL = u.String()
	var drop []string
	if m := hr.Request.Method; m != "" && m != sent.Method {
		out.Method, out.Body = m, nil
		drop = append(drop, "Content-Type", "Content-Encoding", "Content-Language", "Content-Location")
	}
	if !sameOrigin(origin, u) {
		drop = append(drop, "Authorization", "Cookie", "Cookie2")
	}
	if len(drop) > 0 && len(rr.Headers) > 0 {
		out.Headers = rr.Headers.Clone()
		for _, k := range drop {
			for name := range out.Headers {
				if strings.EqualFold(name, k) {
					delete(out.Headers, name)
				}
			}
		}
	}
	return out
}

// mayAuthenticate enforces that server credentials are not sent over plain HTTP to a
// different origin than the request started from (a redirect to another host).
func (f *httpFetcher) mayAuthenticate(origin, challenge *url.URL) bool {
	if challenge == nil {
		return false
	}
	if sameOrigin(origin, challenge) {
		return true
	}
	return strings.EqualFold(challenge.Scheme, "https")
}

func (f *httpFetcher) serverLogin(u *url.URL) func() (Login, bool) {
	return func() (Login, bool) {
		if l, ok := loginFromUserinfo(u); ok {
			return l, true
		}
		return f.credentials.ServerLogin(u)
	}
}

func (f *httpFetcher) proxyLogin(p Proxy) func() (Login, bool) {
	return func() (Login, bool) {
		if p.hasStaticLogin() {
			return Login{Username: p.Username, Password: p.Password}, true
		}
		return f.credentials.ProxyLogin(p)
	}
}

// Close closes the slot's idle connections. Responses still being read are not
// affected, and the slot may be used again afterwards (it dials anew).
func (f *httpFetcher) Close() {
	f.transport.CloseIdleConnections()
}

// pending returns the credentials value this flow prepared for the next request.
func (a *authFlow) pending() (string, bool) {
	if a == nil || a.next == "" {
		return "", false
	}
	return a.next, true
}

// fetchStateKey is the request-context key of a Fetch's fetchState.
type fetchStateKey struct{}

// fetchState is what one Fetch shares with the requests it sends (hopTransport): the
// route of the latest request and the proxy authentication of the exchange, bound to
// the proxy that asked. Its methods accept a nil receiver (a request sent without a
// Fetch).
type fetchState struct {
	mu        sync.Mutex
	last      Proxy // the HTTP proxy that forwarded the latest request
	forwarded bool  // whether the latest request was forwarded by an HTTP proxy at all
	proxy     *authFlow
	proxyFor  Proxy // the proxy the flow authenticates to
}

func fetchStateOf(ctx context.Context) *fetchState {
	s, _ := ctx.Value(fetchStateKey{}).(*fetchState)
	return s
}

// sent records the route of a request about to be sent: forwarded through p, or not
// forwarded by any HTTP proxy.
func (s *fetchState) sent(p Proxy, forwarded bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.last, s.forwarded = p, forwarded
	s.mu.Unlock()
}

// lastForwardedBy returns the HTTP proxy that forwarded the latest request, if any.
func (s *fetchState) lastForwardedBy() (Proxy, bool) {
	if s == nil {
		return Proxy{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last, s.forwarded
}

// proxyFlow returns the exchange's proxy authentication flow for p, creating it with
// create the first time. It reports false when the flow belongs to another proxy.
func (s *fetchState) proxyFlow(p Proxy, create func() *authFlow) (*authFlow, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.proxy == nil {
		s.proxy, s.proxyFor = create(), p
	}
	if !sameProxy(s.proxyFor, p) {
		return nil, false
	}
	return s.proxy, true
}

// proxyAuthorization returns the Proxy-Authorization value for a request forwarded
// through p: the one prepared for p, nothing for any other proxy or route.
func (s *fetchState) proxyAuthorization(p Proxy, forwarded bool) (string, bool) {
	if s == nil || !forwarded {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.proxy == nil || !sameProxy(s.proxyFor, p) {
		return "", false
	}
	return s.proxy.pending()
}

// done ends the proxy flow once the exchange got a response that is not a challenge.
func (s *fetchState) done(success bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.proxy != nil {
		s.proxy.done(success)
	}
}

// sameProxy reports whether a and b name the same proxy (kind and address).
func sameProxy(a, b Proxy) bool {
	return a.Kind == b.Kind && strings.EqualFold(a.Addr, b.Addr)
}

// requestURL is the URL that produced hr (after redirects), falling back to origin.
func requestURL(hr *http.Response, origin *url.URL) *url.URL {
	if hr.Request != nil && hr.Request.URL != nil {
		return hr.Request.URL
	}
	return origin
}

func sameOrigin(a, b *url.URL) bool {
	return a != nil && b != nil && originString(a) == originString(b)
}

func originString(u *url.URL) string {
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

// loginFromUserinfo returns the credentials embedded in u's user info, if any.
func loginFromUserinfo(u *url.URL) (Login, bool) {
	if u == nil || u.User == nil {
		return Login{}, false
	}
	pass, _ := u.User.Password()
	user := u.User.Username()
	if user == "" && pass == "" {
		return Login{}, false
	}
	return Login{Username: user, Password: pass}, true
}

func serverAuthRequest(hr *http.Response, u *url.URL) authRequest {
	method := http.MethodGet
	if hr.Request != nil && hr.Request.Method != "" {
		method = hr.Request.Method
	}
	return authRequest{method: method, uri: requestTargetURI(u), host: u.Hostname()}
}

func proxyAuthRequest(hr *http.Response, origin *url.URL) authRequest {
	// A 407 to a forward request carries the absolute-URI target; the request-target
	// for Digest is that URI. CONNECT tunnels are authenticated by dialTunnel.
	u := origin
	method := http.MethodGet
	if hr.Request != nil && hr.Request.URL != nil {
		u = hr.Request.URL
		if hr.Request.Method != "" {
			method = hr.Request.Method
		}
	}
	return authRequest{method: method, uri: u.String(), host: u.Hostname()}
}

// requestTargetURI is the origin-form request target (path and query) used in the
// Digest "uri" directive.
func requestTargetURI(u *url.URL) string {
	p := u.EscapedPath()
	if p == "" {
		p = "/"
	}
	if u.RawQuery != "" {
		p += "?" + u.RawQuery
	}
	return p
}

// drainForReuse reads and closes an intermediate auth response so its connection can be
// reused for the next leg (NTLM authenticates the connection).
func drainForReuse(hr *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(hr.Body, drainOnAuthRetry))
	_ = hr.Body.Close()
}
