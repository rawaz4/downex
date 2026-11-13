//go:build windows

package sysproxy

import (
	"context"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"downex/internal/transport"
)

// Results of the per-URL resolution are kept per scheme and host for autoCacheTTL (the
// same rule as PAC results, docs/M6-SPEC.md §2), for at most maxAutoCache hosts.
const (
	autoCacheTTL = 5 * time.Minute
	maxAutoCache = 1024
)

// NewAutoResolver returns a resolver that asks WinHTTP for the proxy of each URL via
// WPAD auto-detect and, when configURL is non-empty, that auto-config script. It is
// used for SYSTEM mode when the IE configuration requests auto-detection. The caller
// closes it with Close to release the WinHTTP session.
func NewAutoResolver(configURL string) (*AutoResolver, error) {
	s, err := openSession(configURL)
	if err != nil {
		return nil, err
	}
	return &AutoResolver{session: s, lookup: s.proxyForURL, cache: map[string]autoEntry{}, now: time.Now}, nil
}

// AutoResolver resolves proxies through WinHTTP per URL. Any failure of WinHTTP means
// direct, as in browsers: "automatically detect settings" is on by default, and on most
// networks there is no WPAD server (ERROR_WINHTTP_AUTODETECTION_FAILED), or its script
// cannot be fetched or run.
type AutoResolver struct {
	session *winHTTPSession
	lookup  func(target string) (string, error) // session.proxyForURL; tests: a fake

	mu       sync.Mutex
	cache    map[string]autoEntry
	inflight map[string]*autoLookup // lookups under way, by cache key
	now      func() time.Time
}

type autoEntry struct {
	routes []transport.Proxy
	until  time.Time
}

// autoLookup is one WinHTTP lookup under way; done is closed once routes is set.
type autoLookup struct {
	done   chan struct{}
	routes []transport.Proxy
}

// autoLookupTimeout bounds how long a request waits for WinHTTP (WPAD discovery can
// take long while the network comes up): it then goes direct, and the answer, once it
// arrives, is cached for the next ones.
const autoLookupTimeout = 30 * time.Second

// ResolveProxy implements transport.ProxyResolver. WinHTTP failures mean direct; it
// fails only when ctx ends before the answer (the lookup goes on and is cached). One
// lookup per scheme and host runs at a time; other callers wait for it.
func (r *AutoResolver) ResolveProxy(ctx context.Context, target *url.URL) ([]transport.Proxy, error) {
	key := strings.ToLower(target.Scheme) + "://" + strings.ToLower(target.Host)
	r.mu.Lock()
	if e, ok := r.cache[key]; ok && r.now().Before(e.until) {
		r.mu.Unlock()
		return slices.Clone(e.routes), nil
	}
	l, ok := r.inflight[key]
	if !ok {
		l = &autoLookup{done: make(chan struct{})}
		if r.inflight == nil {
			r.inflight = map[string]*autoLookup{}
		}
		r.inflight[key] = l
		u := *target
		go r.run(key, u.String(), &u, l)
	}
	r.mu.Unlock()

	timer := time.NewTimer(autoLookupTimeout)
	defer timer.Stop()
	select {
	case <-l.done:
		return slices.Clone(l.routes), nil
	case <-timer.C:
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// run performs the lookup l for key and caches its answer.
func (r *AutoResolver) run(key, raw string, target *url.URL, l *autoLookup) {
	var routes []transport.Proxy
	if proxy, err := r.lookup(raw); err == nil {
		routes = autoRoutes(proxy, target)
	}
	r.mu.Lock()
	if len(r.cache) >= maxAutoCache {
		clear(r.cache)
	}
	r.cache[key] = autoEntry{routes: routes, until: r.now().Add(autoCacheTTL)}
	delete(r.inflight, key)
	l.routes = routes
	r.mu.Unlock()
	close(l.done)
}

// autoRoutes maps the proxy list WinHTTP resolved for target to routes: "host:port"
// entries separated by ';' or white space, tried in that order (a script's "PROXY a;
// PROXY b"), or WinINET's "scheme=host:port" form. An empty list is direct; entries that
// do not parse are left out.
func autoRoutes(proxy string, target *url.URL) []transport.Proxy {
	proxy = strings.TrimSpace(proxy)
	if proxy == "" {
		return nil
	}
	if strings.Contains(proxy, "=") {
		var s Settings
		applyWindowsProxy(&s, proxy)
		return proxiesFor(s, target)
	}
	var out []transport.Proxy
	for _, e := range strings.FieldsFunc(proxy, func(r rune) bool { return r == ';' || r == ' ' || r == '\t' }) {
		if p, err := transport.ParseProxyURL(e); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// Close releases the WinHTTP session.
func (r *AutoResolver) Close() { r.session.close() }
