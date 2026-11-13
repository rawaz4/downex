package sysproxy

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"downex/internal/transport"
)

// systemProxies are the static proxies of a Settings snapshot, parsed.
type systemProxies struct {
	http, https, socks *transport.Proxy
}

// parseSystemProxies parses the static proxies of s. lenient skips entries that do not
// parse (a per-URL WinHTTP answer) instead of failing.
func parseSystemProxies(s Settings, lenient bool) (systemProxies, error) {
	var out systemProxies
	for _, f := range []struct {
		name  string
		value string
		socks bool
		dst   **transport.Proxy
	}{
		{"http", s.HTTPProxy, false, &out.http},
		{"https", s.HTTPSProxy, false, &out.https},
		{"socks", s.SOCKSProxy, true, &out.socks},
	} {
		p, err := parseSystemProxy(f.value, f.socks)
		if err != nil {
			if lenient {
				continue
			}
			return systemProxies{}, fmt.Errorf("sysproxy: %s proxy: %w", f.name, err)
		}
		*f.dst = p
	}
	return out, nil
}

// parseSystemProxy parses one proxy value: "host:port" or a proxy URL. A bare SOCKS
// value is SOCKS5.
func parseSystemProxy(v string, socks bool) (*transport.Proxy, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, nil
	}
	if socks && !strings.Contains(v, "://") {
		v = "socks5://" + v
	}
	p, err := transport.ParseProxyURL(v)
	if err != nil {
		return nil, err
	}
	isSOCKS := p.Kind == transport.ProxySOCKS5 || p.Kind == transport.ProxySOCKS4 || p.Kind == transport.ProxySOCKS4A
	switch {
	case socks && !isSOCKS:
		return nil, fmt.Errorf("%s is not a SOCKS proxy", p)
	case !socks && isSOCKS:
		return nil, fmt.Errorf("%s is not an HTTP proxy", p)
	}
	return &p, nil
}

// forTarget returns the route for target the way the operating systems apply their
// settings: the proxy of the target's scheme, else the SOCKS proxy (WinINET's "socks=",
// macOS, ALL_PROXY), else direct.
func (sp systemProxies) forTarget(target *url.URL) []transport.Proxy {
	var p *transport.Proxy
	switch strings.ToLower(target.Scheme) {
	case "http":
		p = sp.http
	case "https":
		p = sp.https
	}
	if p == nil {
		p = sp.socks
	}
	if p == nil {
		return nil
	}
	return []transport.Proxy{*p}
}

func (sp systemProxies) empty() bool { return sp.http == nil && sp.https == nil && sp.socks == nil }

// proxiesFor maps a Settings snapshot to the routes for one target (see forTarget),
// leaving out proxies that do not parse. An empty result is direct.
func proxiesFor(s Settings, target *url.URL) []transport.Proxy {
	sp, _ := parseSystemProxies(s, true)
	return sp.forTarget(target)
}

// staticResolver is the resolver of a Settings snapshot's static proxies.
type staticResolver struct {
	proxies systemProxies
	bypass  *transport.BypassList
}

func (r *staticResolver) ResolveProxy(_ context.Context, target *url.URL) ([]transport.Proxy, error) {
	if r.bypass.Match(target) {
		return nil, nil
	}
	return r.proxies.forTarget(target), nil
}

// NewStaticResolver builds a resolver from a Settings snapshot (static HTTP/HTTPS/SOCKS
// proxies plus the bypass list): targets matched by the bypass list (and loopback
// targets) go direct, others through the proxy of their scheme, else the SOCKS proxy.
// Bypass entries that do not parse are left out (Settings.UnusableBypass names them).
// It ignores AutoDetect and PACURL, which the caller handles with internal/pac or
// NewAutoResolver.
func NewStaticResolver(s Settings) (transport.ProxyResolver, error) {
	sp, err := parseSystemProxies(s, false)
	if err != nil {
		return nil, err
	}
	if sp.empty() {
		// No proxy configured: every target is direct (the bypass list is moot).
		return transport.StaticResolver(nil), nil
	}
	list, _ := usableBypass(s.Bypass)
	bypass, err := transport.ParseBypass(list)
	if err != nil {
		return nil, err
	}
	return &staticResolver{proxies: sp, bypass: bypass}, nil
}

// ResolverFor builds the resolver a Settings snapshot describes, except that PAC and
// WPAD auto-detect are left to the caller (they need fetching or a WinHTTP session).
// It is a convenience wrapper over NewStaticResolver.
func ResolverFor(_ context.Context, s Settings) (transport.ProxyResolver, error) {
	return NewStaticResolver(s)
}
