package transport

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"time"
)

// tlsSessionCacheSize bounds the TLS session tickets shared by all slots of a factory,
// so the 2nd..Nth connection to a host can resume instead of doing a full handshake.
const tlsSessionCacheSize = 128

// HTTPFactory creates Fetchers backed by net/http. Every slot gets its own
// *http.Transport (its own connection pool, at most one connection per host), so
// HTTP/2 never multiplexes several worker slots onto one TCP connection.
// An HTTPFactory is safe for concurrent use.
type HTTPFactory struct {
	opts     HTTPOptions
	resolver ProxyResolver
	base     *http.Transport // template cloned by NewSlot; never used for requests
	proxyTLS *tls.Config     // TLS with HTTPS proxies (shared by the slots' routers)
	now      func() time.Time
	peers    *PeerTable // every slot's live connections (docs/POLITE-SPEC.md rule 1)
}

var _ Factory = (*HTTPFactory)(nil)

// NewHTTPFactory validates opts, applies defaults and returns a factory.
// TLS always verifies certificates against the system roots with TLS 1.2 or newer.
func NewHTTPFactory(opts HTTPOptions) (*HTTPFactory, error) {
	return newHTTPFactory(opts, nil)
}

// NewHTTPFactoryWithRoots is NewHTTPFactory trusting roots instead of the system's root
// certificates (nil: the system's). Only tests use it (the end-to-end tests' build of
// downexd trusts an httptest certificate); verification itself is never disabled.
func NewHTTPFactoryWithRoots(opts HTTPOptions, roots *x509.CertPool) (*HTTPFactory, error) {
	return newHTTPFactory(opts, roots)
}

// newHTTPFactory is NewHTTPFactory with an optional root pool replacing the system
// roots. Only tests pass one (to trust an httptest certificate); verification itself
// is never disabled.
func newHTTPFactory(opts HTTPOptions, roots *x509.CertPool) (*HTTPFactory, error) {
	opts, err := opts.normalize()
	if err != nil {
		return nil, err
	}
	resolver := opts.Resolver
	if resolver == nil {
		resolver, err = newProxyResolver(opts.Proxy)
		if err != nil {
			return nil, err
		}
	}
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(opts.EnableHTTP2)

	base := &http.Transport{
		TLSClientConfig: &tls.Config{
			MinVersion:         tls.VersionTLS12,
			RootCAs:            roots, // nil selects the system roots
			ClientSessionCache: tls.NewLRUClientSessionCache(tlsSessionCacheSize),
		},
		TLSHandshakeTimeout:    opts.TLSHandshakeTimeout,
		DisableCompression:     true, // ranges must be byte-exact
		MaxConnsPerHost:        1,
		MaxIdleConnsPerHost:    1,
		IdleConnTimeout:        opts.IdleConnTimeout,
		ResponseHeaderTimeout:  opts.ResponseHeaderTimeout,
		MaxResponseHeaderBytes: int64(opts.MaxHeaderBytes),
		ForceAttemptHTTP2:      opts.EnableHTTP2,
		Protocols:              protocols,
	}
	if opts.HTTP2HealthCheck > 0 {
		// Only the health check: every other HTTP/2 setting (flow-control windows,
		// frame sizes) keeps net/http's default. Clone gives each slot its own copy.
		base.HTTP2 = &http.HTTP2Config{SendPingTimeout: opts.HTTP2HealthCheck, PingTimeout: opts.HTTP2HealthCheck}
	}
	// HTTPS proxies are verified like origins but always spoken to in HTTP/1.1: the
	// CONNECT and forwarded requests net/http writes to them are HTTP/1.1.
	proxyTLS := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		RootCAs:            roots,
		NextProtos:         []string{"http/1.1"},
		ClientSessionCache: tls.NewLRUClientSessionCache(tlsSessionCacheSize),
	}
	return &HTTPFactory{opts: opts, resolver: resolver, base: base, proxyTLS: proxyTLS, now: time.Now, peers: NewPeerTable()}, nil
}

// Peers returns the table of the remote addresses the factory's slots hold live
// connections to (Polite mode's anchors).
func (f *HTTPFactory) Peers() *PeerTable { return f.peers }

// Options returns the effective options after defaults were applied.
func (f *HTTPFactory) Options() HTTPOptions { return f.opts }

// NewSlot returns a Fetcher with a fresh transport and connection pool.
func (f *HTTPFactory) NewSlot() Fetcher {
	t := f.base.Clone()
	dialer := &net.Dialer{Timeout: f.opts.DialTimeout, KeepAlive: tcpKeepAlive}
	rt := newRouter(f.resolver, dialer, f.opts, f.proxyTLS, f.peers)
	t.Proxy = rt.proxyFunc
	t.DialContext = pinnedDial(rt.dialContext) // direct dials keep a WithNetwork family
	return &httpFetcher{
		transport: t,
		client: &http.Client{
			Transport:     hopTransport{base: t, router: rt},
			CheckRedirect: redirectPolicy(f.opts.MaxRedirects),
		},
		userAgent:   f.opts.UserAgent,
		credentials: f.opts.Credentials,
		maxRedirect: f.opts.MaxRedirects,
		now:         f.now,
	}
}
