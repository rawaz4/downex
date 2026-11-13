package transport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"

	"golang.org/x/net/http/httpproxy"
)

// Default proxy ports by scheme.
const (
	defaultSOCKSPort = "1080" // the registered SOCKS port (RFC 1928 §3)
	defaultHTTPPort  = "80"
	defaultHTTPSPort = "443"
)

// ProxyKind is how a connection reaches an origin.
type ProxyKind uint8

// Proxy kinds.
const (
	ProxyDirect  ProxyKind = iota // no proxy
	ProxyHTTP                     // HTTP proxy: absolute-URI requests for http://, CONNECT tunnels for https://
	ProxyHTTPS                    // HTTP proxy reached over TLS (verified like any origin)
	ProxySOCKS5                   // SOCKS5 (RFC 1928, RFC 1929 auth); host names are resolved by the proxy
	ProxySOCKS4                   // SOCKS4: the target is resolved locally to an IPv4 address
	ProxySOCKS4A                  // SOCKS4a: host names are resolved by the proxy
)

// Scheme returns the URL scheme of k ("http", "https", "socks5", "socks4", "socks4a"),
// or "direct".
func (k ProxyKind) Scheme() string {
	switch k {
	case ProxyHTTP:
		return "http"
	case ProxyHTTPS:
		return "https"
	case ProxySOCKS5:
		return "socks5"
	case ProxySOCKS4:
		return "socks4"
	case ProxySOCKS4A:
		return "socks4a"
	}
	return "direct"
}

func (k ProxyKind) isHTTP() bool { return k == ProxyHTTP || k == ProxyHTTPS }

func (k ProxyKind) isSOCKS() bool { return k == ProxySOCKS5 || k == ProxySOCKS4 || k == ProxySOCKS4A }

// Proxy is one route to an origin.
type Proxy struct {
	Kind ProxyKind
	Addr string // "host:port"; empty for ProxyDirect
	// Username and Password are static credentials, normally from a proxy URL given in
	// HTTPOptions.Proxy: they are sent preemptively (Basic) to HTTP proxies and offered
	// to SOCKS proxies. Without them HTTPOptions.Credentials is asked once the proxy
	// demands authentication.
	Username, Password string
}

// Direct is the route without a proxy.
var Direct = Proxy{Kind: ProxyDirect}

// String names the proxy without credentials: "direct" or "<scheme>://host:port".
func (p Proxy) String() string {
	if p.Kind == ProxyDirect {
		return "direct"
	}
	return p.Kind.Scheme() + "://" + p.Addr
}

// GoString keeps credentials out of %#v.
func (p Proxy) GoString() string { return "transport.Proxy(" + p.String() + ")" }

func (p Proxy) hasStaticLogin() bool { return p.Username != "" || p.Password != "" }

// host returns the proxy's host name or address without the port.
func (p Proxy) host() string {
	h, _, err := net.SplitHostPort(p.Addr)
	if err != nil {
		return p.Addr
	}
	return h
}

// ProxyResolver chooses the proxies of each request. Implementations must be safe for
// concurrent use and should return quickly (cache what is slow to compute).
type ProxyResolver interface {
	// ResolveProxy returns the routes to try for target, in order: when connecting to a
	// proxy fails, the next one is tried. An empty list means direct. target never
	// carries user information.
	ResolveProxy(ctx context.Context, target *url.URL) ([]Proxy, error)
}

// ParseProxyURL parses "scheme://[user:pass@]host[:port]" with scheme http, https,
// socks5 (socks5h is an alias), socks4 or socks4a, or a bare "host[:port]" (http).
// Missing ports default to 80 (http), 443 (https) and 1080 (SOCKS). Errors never
// contain the password.
func ParseProxyURL(raw string) (Proxy, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Proxy{}, errors.New("transport: empty proxy URL")
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		// url.Parse errors quote the input, which may contain a password.
		return Proxy{}, errors.New("transport: invalid proxy URL")
	}
	if u.Hostname() == "" {
		return Proxy{}, fmt.Errorf("transport: proxy URL %s has no host", u.Redacted())
	}
	var kind ProxyKind
	var port string
	switch strings.ToLower(u.Scheme) {
	case "http":
		kind, port = ProxyHTTP, defaultHTTPPort
	case "https":
		kind, port = ProxyHTTPS, defaultHTTPSPort
	case "socks5", "socks5h", "socks":
		kind, port = ProxySOCKS5, defaultSOCKSPort
	case "socks4":
		kind, port = ProxySOCKS4, defaultSOCKSPort
	case "socks4a":
		kind, port = ProxySOCKS4A, defaultSOCKSPort
	default:
		return Proxy{}, fmt.Errorf("transport: unsupported proxy scheme %q in %s", u.Scheme, u.Redacted())
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return Proxy{}, fmt.Errorf("transport: invalid port in proxy URL %s", u.Redacted())
		}
		port = p
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return Proxy{}, fmt.Errorf("transport: proxy URL %s has a path", u.Redacted())
	}
	p := Proxy{Kind: kind, Addr: net.JoinHostPort(u.Hostname(), port)}
	if u.User != nil {
		p.Username = u.User.Username()
		p.Password, _ = u.User.Password()
	}
	return p, nil
}

// StaticResolver always returns the same routes (nil or empty: direct).
type StaticResolver []Proxy

// ResolveProxy implements ProxyResolver.
func (s StaticResolver) ResolveProxy(context.Context, *url.URL) ([]Proxy, error) {
	return append([]Proxy(nil), s...), nil
}

// ResolverFunc adapts a function to ProxyResolver, for callers that compute routes
// themselves (a PAC evaluator, system-proxy discovery).
type ResolverFunc func(ctx context.Context, target *url.URL) ([]Proxy, error)

// ResolveProxy implements ProxyResolver.
func (f ResolverFunc) ResolveProxy(ctx context.Context, target *url.URL) ([]Proxy, error) {
	return f(ctx, target)
}

// newProxyResolver interprets HTTPOptions.Proxy (see there).
func newProxyResolver(raw string) (ProxyResolver, error) {
	raw = strings.TrimSpace(raw)
	switch strings.ToLower(raw) {
	case "":
		return EnvironmentResolver(), nil
	case "direct", "none":
		return StaticResolver(nil), nil
	}
	p, err := ParseProxyURL(raw)
	if err != nil {
		return nil, err
	}
	return StaticResolver{p}, nil
}

// envResolver selects proxies from HTTP_PROXY, HTTPS_PROXY, ALL_PROXY and NO_PROXY
// (or their lower-case forms), read once when it is created.
type envResolver struct {
	byScheme func(*url.URL) (*url.URL, error) // HTTP(S)_PROXY with NO_PROXY
	all      func(*url.URL) (*url.URL, error) // ALL_PROXY with NO_PROXY; nil without ALL_PROXY
}

// EnvironmentResolver returns the proxies named by the environment: HTTP_PROXY for
// http://, HTTPS_PROXY for https:// and ALL_PROXY for both when the former is unset,
// except for hosts listed in NO_PROXY (and loopback addresses). Values may be
// http, https, socks5, socks4 or socks4a URLs. The environment is read once.
func EnvironmentResolver() ProxyResolver { return newEnvResolver(os.Getenv) }

func newEnvResolver(getenv func(string) string) *envResolver {
	get := func(names ...string) string {
		for _, n := range names {
			if v := getenv(n); v != "" {
				return v
			}
		}
		return ""
	}
	cfg := httpproxy.Config{
		HTTPProxy:  get("HTTP_PROXY", "http_proxy"),
		HTTPSProxy: get("HTTPS_PROXY", "https_proxy"),
		NoProxy:    get("NO_PROXY", "no_proxy"),
	}
	r := &envResolver{byScheme: cfg.ProxyFunc()}
	if all := get("ALL_PROXY", "all_proxy"); all != "" {
		r.all = (&httpproxy.Config{HTTPProxy: all, HTTPSProxy: all, NoProxy: cfg.NoProxy}).ProxyFunc()
	}
	return r
}

// ResolveProxy implements ProxyResolver.
func (r *envResolver) ResolveProxy(_ context.Context, target *url.URL) ([]Proxy, error) {
	pu, err := r.byScheme(target)
	if err == nil && pu == nil && r.all != nil {
		pu, err = r.all(target)
	}
	if err != nil {
		// httpproxy quotes the variable's value, which may hold a password.
		return nil, errors.New("transport: invalid proxy URL in the environment")
	}
	if pu == nil {
		return nil, nil
	}
	p, err := ParseProxyURL(pu.String())
	if err != nil {
		return nil, fmt.Errorf("transport: proxy from the environment: %w", err)
	}
	return []Proxy{p}, nil
}

// ManualProxies is a fixed proxy configuration (docs/M6-SPEC.md §2, MANUAL).
type ManualProxies struct {
	HTTP   string // "host:port" of the HTTP proxy for http:// targets; "" = direct
	HTTPS  string // "host:port" of the HTTP proxy for https:// targets (CONNECT); "" = direct
	SOCKS  string // "socks5://host:port", "socks4://..." or "socks4a://..."; overrides HTTP and HTTPS
	Bypass string // see ParseBypass
}

// NewManualResolver validates m and returns its resolver: targets matched by the
// bypass list (and loopback targets) go direct, every other target through SOCKS when
// set, else through the HTTP or HTTPS proxy of its scheme.
func NewManualResolver(m ManualProxies) (ProxyResolver, error) {
	r := &manualResolver{}
	var err error
	if r.bypass, err = ParseBypass(m.Bypass); err != nil {
		return nil, err
	}
	parse := func(name, v string, socks bool) (*Proxy, error) {
		v = strings.TrimSpace(v)
		if v == "" {
			return nil, nil
		}
		if socks != strings.HasPrefix(strings.ToLower(v), "socks") {
			if socks {
				return nil, fmt.Errorf("transport: %s proxy must be a socks5://, socks4:// or socks4a:// URL", name)
			}
			if strings.Contains(v, "://") && !strings.HasPrefix(strings.ToLower(v), "http") {
				return nil, fmt.Errorf("transport: %s proxy must be host:port", name)
			}
		}
		p, err := ParseProxyURL(v)
		if err != nil {
			return nil, fmt.Errorf("transport: %s proxy: %w", name, err)
		}
		return &p, nil
	}
	if r.http, err = parse("http", m.HTTP, false); err != nil {
		return nil, err
	}
	if r.https, err = parse("https", m.HTTPS, false); err != nil {
		return nil, err
	}
	if r.socks, err = parse("socks", m.SOCKS, true); err != nil {
		return nil, err
	}
	return r, nil
}

type manualResolver struct {
	http, https, socks *Proxy
	bypass             *BypassList
}

func (r *manualResolver) ResolveProxy(_ context.Context, target *url.URL) ([]Proxy, error) {
	if r.bypass.Match(target) {
		return nil, nil
	}
	p := r.socks
	if p == nil {
		switch strings.ToLower(target.Scheme) {
		case "http":
			p = r.http
		case "https":
			p = r.https
		}
	}
	if p == nil {
		return nil, nil
	}
	return []Proxy{*p}, nil
}
