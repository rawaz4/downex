package transport

import (
	"fmt"
	"time"
)

// Defaults applied by NewHTTPFactory to zero-valued HTTPOptions fields.
const (
	DefaultUserAgent             = "Downex"
	DefaultDialTimeout           = 30 * time.Second
	DefaultTLSHandshakeTimeout   = 15 * time.Second
	DefaultResponseHeaderTimeout = 60 * time.Second
	DefaultIdleConnTimeout       = 90 * time.Second
	DefaultMaxRedirects          = 10
	DefaultMaxHeaderBytes        = 64 << 10
	DefaultHTTP2HealthCheck      = 15 * time.Second
)

// tcpKeepAlive is the keep-alive probe interval for dialled connections.
const tcpKeepAlive = 30 * time.Second

// HTTPOptions configures an HTTPFactory. Zero durations and limits select the
// Default* constants. Start from DefaultHTTPOptions: EnableHTTP2 defaults to true
// there, which a bool zero value cannot express.
type HTTPOptions struct {
	// UserAgent is sent when the request carries no User-Agent header of its own
	// (browser hand-offs usually do). Empty selects DefaultUserAgent.
	UserAgent string

	// Proxy selects how connections are made:
	//
	//	""                              proxy from HTTP_PROXY / HTTPS_PROXY / NO_PROXY
	//	"direct"                        no proxy
	//	"http://user:pass@host:port"    HTTP proxy (CONNECT tunnel for https targets);
	//	                                "https://" proxies and a bare "host:port" are accepted too
	//	"socks5://user:pass@host:port"  SOCKS5 proxy (RFC 1928, RFC 1929 auth); target host
	//	                                names are resolved by the proxy. "socks5h://" is an alias.
	Proxy string

	DialTimeout           time.Duration // TCP connect, including a SOCKS5 handshake
	TLSHandshakeTimeout   time.Duration // TLS handshake with the origin (or an https proxy)
	ResponseHeaderTimeout time.Duration // wait for response headers once the request is written
	IdleConnTimeout       time.Duration // how long an idle pooled connection is kept

	// MaxRedirects is the number of redirects followed per request. 0 selects
	// DefaultMaxRedirects; a negative value refuses every redirect with ErrRedirects.
	MaxRedirects int

	// MaxHeaderBytes limits the size of response headers; larger responses fail
	// with ErrTooLarge. 0 selects DefaultMaxHeaderBytes.
	MaxHeaderBytes int

	// EnableHTTP2 negotiates HTTP/2 through ALPN on TLS connections. Each worker slot
	// still gets its own connection, so streams of different slots are never multiplexed.
	EnableHTTP2 bool

	// HTTP2HealthCheck checks HTTP/2 connections: after this long without a frame from
	// the server a connection sends a PING, and it is closed when no answer arrives
	// within the same time, so the reads of a black-holed connection fail (and the
	// slot's next request dials anew) instead of hanging. 0 selects
	// DefaultHTTP2HealthCheck; a negative value disables the check. HTTP/1.1 is not
	// affected.
	HTTP2HealthCheck time.Duration

	// Resolver chooses the proxies for each request and overrides Proxy when set. A nil
	// Resolver selects the route named by Proxy. Build one with NewManualResolver,
	// EnvironmentResolver, StaticResolver or a PAC evaluator.
	Resolver ProxyResolver

	// Credentials supplies logins for server (401) and proxy (407) authentication
	// challenges. nil disables authentication beyond what a request already carries.
	Credentials Credentials
}

// DefaultHTTPOptions returns the recommended options: environment proxy, HTTP/2 enabled
// and the Default* timeouts and limits.
func DefaultHTTPOptions() HTTPOptions {
	return HTTPOptions{
		UserAgent:             DefaultUserAgent,
		DialTimeout:           DefaultDialTimeout,
		TLSHandshakeTimeout:   DefaultTLSHandshakeTimeout,
		ResponseHeaderTimeout: DefaultResponseHeaderTimeout,
		IdleConnTimeout:       DefaultIdleConnTimeout,
		MaxRedirects:          DefaultMaxRedirects,
		MaxHeaderBytes:        DefaultMaxHeaderBytes,
		EnableHTTP2:           true,
		HTTP2HealthCheck:      DefaultHTTP2HealthCheck,
	}
}

// normalize validates o and fills zero fields with their defaults.
func (o HTTPOptions) normalize() (HTTPOptions, error) {
	durations := []struct {
		name string
		val  *time.Duration
		def  time.Duration
	}{
		{"DialTimeout", &o.DialTimeout, DefaultDialTimeout},
		{"TLSHandshakeTimeout", &o.TLSHandshakeTimeout, DefaultTLSHandshakeTimeout},
		{"ResponseHeaderTimeout", &o.ResponseHeaderTimeout, DefaultResponseHeaderTimeout},
		{"IdleConnTimeout", &o.IdleConnTimeout, DefaultIdleConnTimeout},
	}
	for _, d := range durations {
		switch {
		case *d.val < 0:
			return o, fmt.Errorf("transport: invalid options: negative %s %v", d.name, *d.val)
		case *d.val == 0:
			*d.val = d.def
		}
	}
	switch {
	case o.MaxHeaderBytes < 0:
		return o, fmt.Errorf("transport: invalid options: negative MaxHeaderBytes %d", o.MaxHeaderBytes)
	case o.MaxHeaderBytes == 0:
		o.MaxHeaderBytes = DefaultMaxHeaderBytes
	}
	if o.MaxRedirects == 0 {
		o.MaxRedirects = DefaultMaxRedirects
	}
	if o.HTTP2HealthCheck == 0 {
		o.HTTP2HealthCheck = DefaultHTTP2HealthCheck // a negative value stays: disabled
	}
	if o.UserAgent == "" {
		o.UserAgent = DefaultUserAgent
	}
	if !validHeaderValue(o.UserAgent) {
		return o, fmt.Errorf("transport: invalid options: UserAgent contains control characters")
	}
	return o, nil
}
