package transport

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/idna"
)

// badRouteTTL is how long a route that could not be reached is tried only after the
// other routes of its list (the same rule as browsers' proxy retry delay).
const badRouteTTL = 5 * time.Minute

// maxBadRoutes bounds the remembered bad routes of one slot.
const maxBadRoutes = 256

// router routes the requests of one slot. hopTransport resolves the routes of every
// request (the first of a Fetch and each redirect hop) and tries them in order: when a
// route cannot be reached (a proxy that refuses connections, a failed direct dial) the
// next one is used and the failed one is tried last for badRouteTTL. The chosen route
// travels in the request context (hopRoute), which net/http keeps for the dial:
//
//   - a plain-http request through an HTTP or HTTPS proxy is forwarded by it: proxyFunc
//     names the proxy so that net/http writes the absolute-URI form, and dialContext
//     connects to the proxy (TLS for an HTTPS proxy, HTTP/1.1 only);
//   - everything else is dialled by dialContext: directly, through SOCKS, or through a
//     CONNECT tunnel of an HTTP(S) proxy that dialTunnel authenticates itself (Basic,
//     Digest, NTLM/Negotiate, multi-leg on the same connection).
//
// Proxy credentials are never put in a proxy URL: forwarded requests carry them as a
// header that hopTransport sets for the proxy that forwards them only.
type router struct {
	resolver    ProxyResolver
	dialer      *net.Dialer
	dialTimeout time.Duration
	tlsTimeout  time.Duration
	creds       Credentials
	proxyTLS    *tls.Config // TLS with HTTPS proxies: verified, HTTP/1.1 only
	userAgent   string
	maxHeader   int64
	now         func() time.Time

	mu  sync.Mutex
	bad map[badRoute]time.Time // routes that could not be reached, until when
}

// badRoute names a route of one target.
type badRoute struct {
	target string // routeTarget
	route  string // Proxy.String
}

func newRouter(resolver ProxyResolver, dialer *net.Dialer, opts HTTPOptions, proxyTLS *tls.Config) *router {
	return &router{
		resolver:    resolver,
		dialer:      dialer,
		dialTimeout: opts.DialTimeout,
		tlsTimeout:  opts.TLSHandshakeTimeout,
		creds:       opts.Credentials,
		proxyTLS:    proxyTLS,
		userAgent:   opts.UserAgent,
		maxHeader:   int64(opts.MaxHeaderBytes),
		now:         time.Now,
		bad:         map[badRoute]time.Time{},
	}
}

// hopRoute is the route chosen for one attempt of a request: how the connection
// reaches the target, and whether an HTTP(S) proxy forwards the request itself (a
// plain-http target) instead of tunnelling it to the origin with CONNECT.
type hopRoute struct {
	route     Proxy
	forwarded bool
	target    string // routeTarget of the request
}

// forwardedBy returns the proxy that forwards the request, if any. Only that proxy can
// answer it with 407; behind a CONNECT tunnel, a SOCKS proxy or a direct connection the
// response comes from the origin.
func (h hopRoute) forwardedBy() (Proxy, bool) {
	if !h.forwarded {
		return Proxy{}, false
	}
	return h.route, true
}

// hopRouteKey is the request-context key under which hopTransport hands the route it
// chose to proxyFunc and dialContext.
type hopRouteKey struct{}

// resolve returns the routes of req and its routing key. The resolver sees the URL
// without user information and with the host in the IDNA form that net/http dials, so
// that the route is decided for the name the connection is made to.
func (r *router) resolve(req *http.Request) (string, []Proxy, error) {
	u := *req.URL
	u.User = nil
	u.Fragment, u.RawFragment = "", ""
	if host := u.Hostname(); !isASCII(host) {
		if ascii, err := idna.Lookup.ToASCII(host); err == nil {
			if port := u.Port(); port != "" {
				u.Host = net.JoinHostPort(ascii, port)
			} else {
				u.Host = ascii
			}
		}
	}
	routes, err := r.resolver.ResolveProxy(req.Context(), &u)
	return routeTarget(&u), routes, err
}

// routeTarget is the key of a target's routes: scheme, host and port.
func routeTarget(u *url.URL) string {
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(hostPort(u))
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// pick returns the first route of routes that did not fail during this request
// (failed), preferring routes not remembered as bad. An empty list is direct.
func (r *router) pick(target string, routes, failed []Proxy) (Proxy, bool) {
	if len(routes) == 0 {
		return Direct, len(failed) == 0
	}
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	fallback := -1
	for i, p := range routes {
		if slices.ContainsFunc(failed, func(f Proxy) bool { return sameProxy(f, p) }) {
			continue
		}
		if until, bad := r.bad[badRoute{target, p.String()}]; bad && now.Before(until) {
			if fallback < 0 {
				fallback = i
			}
			continue
		}
		return p, true
	}
	if fallback >= 0 {
		return routes[fallback], true
	}
	return Proxy{}, false
}

// markBad remembers that route p of target could not be reached.
func (r *router) markBad(target string, p Proxy) {
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.bad) >= maxBadRoutes {
		for k, until := range r.bad {
			if !now.Before(until) {
				delete(r.bad, k)
			}
		}
		if len(r.bad) >= maxBadRoutes {
			clear(r.bad)
		}
	}
	r.bad[badRoute{target, p.String()}] = now.Add(badRouteTTL)
}

// clearBad forgets a bad mark of route p once it connected again.
func (r *router) clearBad(target string, p Proxy) {
	r.mu.Lock()
	delete(r.bad, badRoute{target, p.String()})
	r.mu.Unlock()
}

// proxyFunc is the http.Transport.Proxy callback: for a request forwarded by an HTTP or
// HTTPS proxy it names that proxy (so net/http writes the absolute-URI form; the URL
// carries no credentials, and dialContext makes the connection, with TLS for an HTTPS
// proxy, whose URL is marked so that it never shares pooled connections with a plain
// proxy at the same address). Everything else is dialled by dialContext.
func (r *router) proxyFunc(req *http.Request) (*url.URL, error) {
	hop, ok := req.Context().Value(hopRouteKey{}).(hopRoute)
	if !ok {
		return nil, errNoRoute
	}
	if !hop.forwarded {
		return nil, nil
	}
	u := &url.URL{Scheme: "http", Host: hop.route.Addr}
	if hop.route.Kind == ProxyHTTPS {
		u.Fragment = "tls"
	}
	return u, nil
}

// errNoRoute is returned for a request that did not go through hopTransport: it is
// never sent rather than sent on an unchosen route.
var errNoRoute = errors.New("transport: no route was chosen for the request")

// hopTransport is a slot's http.Client transport. It resolves and chooses the route of
// every request it sends (the first of a Fetch and each redirect hop), falls back to the
// next route when one cannot be reached, and attaches proxy credentials only to a
// request forwarded through a proxy: the Fetch's pending ones (fetchState) for the proxy
// that asked for them, else a static Basic login of that proxy. No other hop, and so no
// origin, ever carries Proxy-Authorization.
type hopTransport struct {
	base   *http.Transport
	router *router
}

// RoundTrip implements http.RoundTripper.
func (h hopTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	target, routes, err := h.router.resolve(req)
	if err != nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, err
	}
	state := fetchStateOf(req.Context())
	var failed []Proxy
	for {
		route, ok := h.router.pick(target, routes, failed)
		if !ok {
			return nil, err // every route failed (err is the last failure)
		}
		hop := hopRoute{route: route, forwarded: route.Kind.isHTTP() && strings.EqualFold(req.URL.Scheme, "http"), target: target}
		out := req.Clone(context.WithValue(req.Context(), hopRouteKey{}, hop))
		out.Header.Del("Proxy-Authorization")
		p, forwarded := hop.forwardedBy()
		if v, ok := state.proxyAuthorization(p, forwarded); ok {
			out.Header.Set("Proxy-Authorization", v)
		} else if forwarded {
			if v := h.router.preemptive(p); v != "" {
				out.Header.Set("Proxy-Authorization", v)
			}
		}
		state.sent(p, forwarded)
		var resp *http.Response
		resp, err = h.base.RoundTrip(out)
		var re *routeError
		if err == nil || !errors.As(err, &re) || req.Context().Err() != nil {
			return resp, err
		}
		// Nothing was sent: the connection for this route could not be made.
		h.router.markBad(target, route)
		failed = append(failed, route)
		if req.Body != nil && req.Body != http.NoBody {
			if req.GetBody == nil {
				return nil, err
			}
			body, berr := req.GetBody()
			if berr != nil {
				return nil, err
			}
			req = req.Clone(req.Context())
			req.Body = body
		}
	}
}

// CloseIdleConnections closes the idle connections of the underlying transport.
func (h hopTransport) CloseIdleConnections() { h.base.CloseIdleConnections() }

// routeError reports a route over which no connection could be made (the proxy or the
// origin did not answer, or the proxy failed before the tunnel was up). The request was
// not sent, so the next route may be tried.
type routeError struct {
	route Proxy
	err   error
}

func (e *routeError) Error() string { return e.err.Error() }
func (e *routeError) Unwrap() error { return e.err }

// unreachable wraps err as a routeError unless it is a failure that another route must
// not paper over: an authentication demand, a certificate that does not verify, or a
// proxy that refused the tunnel.
func unreachable(p Proxy, err error) error {
	var refused *tunnelError
	switch {
	case errors.Is(err, ErrAuthRequired), isTLSVerificationError(err), errors.As(err, &refused):
		return err
	}
	return &routeError{route: p, err: err}
}

// dialContext is the http.Transport.DialContext callback. It connects as the request's
// hopRoute says: to the forwarding proxy (addr is then that proxy's address), directly
// or through SOCKS or a CONNECT tunnel to addr (the target, in the form net/http dials).
func (r *router) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	hop, ok := ctx.Value(hopRouteKey{}).(hopRoute)
	if !ok {
		return nil, fmt.Errorf("%w (dial %s)", errNoRoute, addr)
	}
	p := hop.route
	var conn net.Conn
	var err error
	switch {
	case hop.forwarded:
		conn, err = r.dialProxy(ctx, p)
	case p.Kind == ProxyDirect:
		conn, err = r.dialer.DialContext(ctx, network, addr)
	case p.Kind.isSOCKS():
		conn, err = dialSOCKS(ctx, r.dialer, p, addr, r.dialTimeout, r.socksLogin(p))
	case p.Kind.isHTTP():
		conn, err = r.dialTunnel(ctx, p, addr)
	default:
		err = fmt.Errorf("transport: unsupported route %s", p)
	}
	if err != nil {
		return nil, unreachable(p, err)
	}
	r.clearBad(hop.target, p)
	return conn, nil
}

// dialProxy connects to the HTTP or HTTPS proxy p. An HTTPS proxy is verified like any
// server and spoken to in HTTP/1.1 only (net/http writes HTTP/1.1 to it, whatever the
// proxy could negotiate).
func (r *router) dialProxy(ctx context.Context, p Proxy) (net.Conn, error) {
	conn, err := r.dialer.DialContext(ctx, "tcp", p.Addr)
	if err != nil {
		return nil, fmt.Errorf("transport: connect to proxy %s: %w", p, err)
	}
	if p.Kind != ProxyHTTPS {
		return conn, nil
	}
	cfg := r.proxyTLS.Clone()
	cfg.ServerName = p.host()
	tc := tls.Client(conn, cfg)
	hctx, cancel := context.WithTimeout(ctx, r.tlsTimeout)
	defer cancel()
	if err := tc.HandshakeContext(hctx); err != nil {
		conn.Close()
		return nil, fmt.Errorf("transport: TLS with proxy %s: %w", p, err)
	}
	return tc, nil
}

func (r *router) socksLogin(p Proxy) Login {
	if p.hasStaticLogin() {
		return Login{Username: p.Username, Password: p.Password}
	}
	if r.creds != nil {
		if l, ok := r.creds.ProxyLogin(p); ok {
			return l
		}
	}
	return Login{}
}

// proxyLogin returns the login lookup of an HTTP proxy's authentication flow: its static
// credentials, else the Credentials' login for that proxy.
func (r *router) proxyLogin(p Proxy) func() (Login, bool) {
	return func() (Login, bool) {
		if p.hasStaticLogin() {
			return Login{Username: p.Username, Password: p.Password}, true
		}
		if r.creds == nil {
			return Login{}, false
		}
		return r.creds.ProxyLogin(p)
	}
}

// preemptive returns the Basic Proxy-Authorization value sent to proxy p before it asks:
// its static credentials, or a stored login with a password; "" otherwise (the proxy's
// challenge is then answered with the scheme it asks for).
func (r *router) preemptive(p Proxy) string {
	if p.hasStaticLogin() {
		return basicValue(Login{Username: p.Username, Password: p.Password})
	}
	if r.creds == nil {
		return ""
	}
	if l, ok := r.creds.ProxyLogin(p); ok && l.Password != "" {
		return basicValue(l)
	}
	return ""
}
