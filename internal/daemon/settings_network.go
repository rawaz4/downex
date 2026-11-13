package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"downex/internal/limiter"
	"downex/internal/pac"
	"downex/internal/secrets"
	"downex/internal/sysproxy"
	"downex/internal/transport"

	downexv1 "downex/internal/rpc/downex/v1"
)

// Secret kinds (DaemonService.SetSecret). These are the opaque name prefixes under
// which passwords are stored in the OS keyring.
const (
	SecretKindProxy     = "proxy"
	SecretKindSiteLogin = "site_login"

	// proxySecretName is the keyring name of the single proxy password.
	proxySecretName = SecretKindProxy
	// maxHostRules and maxSiteLogins bound the lists a client may set.
	maxHostRules  = 256
	maxSiteLogins = 256
	// pacMaxBytes bounds a fetched PAC script.
	pacMaxBytes = 1 << 20
	// pacFetchTimeout bounds fetching a PAC script.
	pacFetchTimeout = 15 * time.Second
	// MinRequestBytes and MaxRequestBytes bound a host rule's max_request_bytes when it is
	// set (docs/SITE-MEDIA-SPEC.md §3.8).
	MinRequestBytes = 1 << 20
	MaxRequestBytes = 1 << 30
)

// ProxyMode selects how connections are proxied (docs/M6-SPEC.md §2). The zero value is
// ProxyModeSystem, matching the proto default.
type ProxyMode uint8

// Proxy modes.
const (
	ProxyModeSystem ProxyMode = iota
	ProxyModeNone
	ProxyModeManual
	ProxyModePAC
)

// ProxySettings is the proxy configuration (passwords are never here; see SetSecret).
type ProxySettings struct {
	Mode       ProxyMode `json:"mode"`
	HTTPProxy  string    `json:"http_proxy,omitempty"`
	HTTPSProxy string    `json:"https_proxy,omitempty"`
	SOCKSProxy string    `json:"socks_proxy,omitempty"`
	Bypass     string    `json:"bypass,omitempty"`
	PACURL     string    `json:"pac_url,omitempty"`
	Username   string    `json:"username,omitempty"`
}

// HostRule caps and throttles connections to hosts matching Pattern (§2). MaxRequestBytes
// caps the length of each ranged request to them (0 = no cap; docs/SITE-MEDIA-SPEC.md
// §2.4).
type HostRule struct {
	Pattern         string `json:"pattern"`
	MaxConnections  int    `json:"max_connections,omitempty"`
	SpeedLimit      int64  `json:"speed_limit,omitempty"`
	MaxRequestBytes int64  `json:"max_request_bytes,omitempty"`
}

// builtinHostRules apply to hosts that no user host rule matches (docs/SITE-MEDIA-SPEC.md
// §2.4). They are neither stored nor shown as user rules.
var builtinHostRules = []HostRule{
	// YouTube's video servers slow every request down after its first second or so, but
	// not the number of connections: 1 MiB requests over the download's own connections.
	// Measured on a ~84 Mbit/s line (2026-10-29, 48 MiB 1080p track): 4 connections of
	// 10 MiB 3.0 MiB/s, 32 connections of 1 MiB 7.6-7.8 MiB/s.
	{Pattern: "*.googlevideo.com", MaxRequestBytes: 1 << 20},
}

// SiteLogin is a stored user name for a URL pattern; the password lives in the keyring.
type SiteLogin struct {
	ID         string `json:"id"`
	URLPattern string `json:"url_pattern"`
	Username   string `json:"username,omitempty"`
}

// NetworkSettings holds the M6 network settings (docs/M6-SPEC.md §2). It is owned by the
// network area: the integrator persists it through settings.go's settingsFile, converts
// it in convert.go with networkFromProto/networkToProto, normalises it from
// Settings.normalize/repair with Normalize, and drives a NetworkRuntime from it.
type NetworkSettings struct {
	Proxy      ProxySettings `json:"proxy"`
	HostRules  []HostRule    `json:"host_rules,omitempty"`
	SiteLogins []SiteLogin   `json:"site_logins,omitempty"`
}

// DefaultNetworkSettings returns the settings of a new installation: SYSTEM proxy, no
// host rules, no site logins.
func DefaultNetworkSettings() NetworkSettings {
	return NetworkSettings{Proxy: ProxySettings{Mode: ProxyModeSystem}}
}

// Clone returns a deep copy of n.
func (n NetworkSettings) Clone() NetworkSettings {
	n.HostRules = append([]HostRule(nil), n.HostRules...)
	n.SiteLogins = append([]SiteLogin(nil), n.SiteLogins...)
	return n
}

// Normalize validates n as a full replacement and fills defaults. It assigns a stable
// id to each site login that lacks one (new entries), using nextID for generation. The
// returned error describes the first invalid field.
func (n NetworkSettings) Normalize(nextID func() string) (NetworkSettings, error) {
	p, err := n.Proxy.normalize()
	if err != nil {
		return n, err
	}
	n.Proxy = p
	if len(n.HostRules) > maxHostRules {
		return n, fmt.Errorf("too many host rules (max %d)", maxHostRules)
	}
	rules := make([]HostRule, 0, len(n.HostRules))
	seen := map[string]bool{}
	for _, r := range n.HostRules {
		r.Pattern = strings.ToLower(strings.TrimSpace(r.Pattern))
		if r.Pattern == "" {
			return n, errors.New("host rule pattern must not be empty")
		}
		if !validHostPattern(r.Pattern) {
			return n, fmt.Errorf("invalid host rule pattern %q", r.Pattern)
		}
		if seen[r.Pattern] {
			return n, fmt.Errorf("duplicate host rule pattern %q", r.Pattern)
		}
		seen[r.Pattern] = true
		if r.MaxConnections < 0 || r.MaxConnections > MaxConnectionsLimit {
			return n, fmt.Errorf("host rule %q: max_connections must be between 0 and %d", r.Pattern, MaxConnectionsLimit)
		}
		if r.SpeedLimit < 0 {
			return n, fmt.Errorf("host rule %q: speed_limit must not be negative", r.Pattern)
		}
		if r.MaxRequestBytes != 0 && (r.MaxRequestBytes < MinRequestBytes || r.MaxRequestBytes > MaxRequestBytes) {
			return n, fmt.Errorf("host rule %q: max_request_bytes must be 0 or between %d and %d", r.Pattern,
				MinRequestBytes, MaxRequestBytes)
		}
		rules = append(rules, r)
	}
	n.HostRules = rules

	if len(n.SiteLogins) > maxSiteLogins {
		return n, fmt.Errorf("too many site logins (max %d)", maxSiteLogins)
	}
	logins := make([]SiteLogin, 0, len(n.SiteLogins))
	ids := map[string]bool{}
	for _, l := range n.SiteLogins {
		l.URLPattern = strings.TrimSpace(l.URLPattern)
		if l.URLPattern == "" {
			return n, errors.New("site login url_pattern must not be empty")
		}
		if !validSitePattern(l.URLPattern) {
			return n, fmt.Errorf("invalid site login url_pattern %q", l.URLPattern)
		}
		l.ID = strings.TrimSpace(l.ID)
		if l.ID == "" {
			l.ID = nextID()
		}
		if err := validateSecretRef(SecretKindSiteLogin, l.ID); err != nil {
			return n, fmt.Errorf("site login %q: %w", l.URLPattern, err)
		}
		if ids[l.ID] {
			return n, fmt.Errorf("duplicate site login id %q", l.ID)
		}
		ids[l.ID] = true
		logins = append(logins, l)
	}
	n.SiteLogins = logins
	return n, nil
}

// normalize validates the proxy settings for the active mode. A proxy or PAC URL with
// user information is refused in every mode: passwords never appear in Settings (the
// user name is Username, the password goes to the keyring with SetSecret).
func (p ProxySettings) normalize() (ProxySettings, error) {
	if p.Mode > ProxyModePAC {
		return p, fmt.Errorf("unknown proxy mode %d", p.Mode)
	}
	p.HTTPProxy = strings.TrimSpace(p.HTTPProxy)
	p.HTTPSProxy = strings.TrimSpace(p.HTTPSProxy)
	p.SOCKSProxy = strings.TrimSpace(p.SOCKSProxy)
	p.PACURL = strings.TrimSpace(p.PACURL)
	for _, f := range p.urlFields() {
		if _, found := withoutUserinfo(*f.value); found {
			// Never the value: it holds the password.
			return p, fmt.Errorf("%s must not contain a user name or password; set the proxy user name and store its password separately", f.name)
		}
	}
	switch p.Mode {
	case ProxyModeManual:
		if _, err := transport.NewManualResolver(transport.ManualProxies{
			HTTP: p.HTTPProxy, HTTPS: p.HTTPSProxy, SOCKS: p.SOCKSProxy, Bypass: p.Bypass,
		}); err != nil {
			return p, err
		}
	case ProxyModePAC:
		if err := validatePACURL(p.PACURL); err != nil {
			return p, err
		}
	}
	return p, nil
}

// proxyURLField is a URL-valued field of ProxySettings.
type proxyURLField struct {
	name  string // the field's settings name
	value *string
}

// urlFields returns the fields of p that hold proxy or PAC URLs.
func (p *ProxySettings) urlFields() []proxyURLField {
	return []proxyURLField{
		{"http_proxy", &p.HTTPProxy},
		{"https_proxy", &p.HTTPSProxy},
		{"socks_proxy", &p.SOCKSProxy},
		{"pac_url", &p.PACURL},
	}
}

// repairUserinfo removes the user information that an older version may have saved in
// the URL fields of settings read from disk, keeping the rest of each value, and names
// every field it changed (never the value).
func (p ProxySettings) repairUserinfo() (ProxySettings, []string) {
	var problems []string
	for _, f := range p.urlFields() {
		if v, found := withoutUserinfo(*f.value); found {
			*f.value = v
			problems = append(problems, f.name+" contained a user name or password; removed it")
		}
	}
	return p, problems
}

// withoutUserinfo returns raw (a proxy "[scheme://][userinfo@]host[:port][/...]" or PAC
// URL as written) without its user information, and whether it had any. Like url.Parse,
// it takes the last '@' of the authority as the end of the user information.
func withoutUserinfo(raw string) (string, bool) {
	start := 0
	if i := strings.Index(raw, "://"); i >= 0 {
		start = i + 3
	}
	end := len(raw)
	if i := strings.IndexAny(raw[start:], "/?#"); i >= 0 {
		end = start + i
	}
	at := strings.LastIndexByte(raw[start:end], '@')
	if at < 0 {
		return raw, false
	}
	return raw[:start] + raw[start+at+1:], true
}

// validatePACURL accepts only http, https and file URLs for a PAC script.
func validatePACURL(raw string) error {
	if raw == "" {
		return errors.New("pac_url must be set for PAC proxy mode")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("invalid pac_url")
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "file":
		return nil
	}
	return fmt.Errorf("pac_url must be an http, https or file URL")
}

// validHostPattern allows a host, "*.suffix", "*" and simple ? / * globs; no scheme,
// path, port or whitespace.
func validHostPattern(p string) bool {
	if p == "" || strings.ContainsAny(p, " \t/:@") || strings.Contains(p, "://") {
		return false
	}
	for i := 0; i < len(p); i++ {
		c := p[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.IndexByte(".-_*?", c) >= 0) {
			return false
		}
	}
	return true
}

// validSitePattern allows either a URL-shaped glob ("https://host/path*") or a host
// glob ("*.example.com").
func validSitePattern(p string) bool {
	if strings.Contains(p, "://") {
		i := strings.Index(p, "://")
		scheme := strings.ToLower(p[:i])
		if scheme != "http" && scheme != "https" && scheme != "*" {
			return false
		}
		return len(p) > i+3 && !strings.ContainsAny(p, " \t")
	}
	return validHostPattern(strings.ToLower(p))
}

// ===================== proto conversion (integrator wires from convert.go) =============

// proxyModeFromProto maps an API proxy mode (unknown values default to SYSTEM).
func proxyModeFromProto(m downexv1.ProxyMode) ProxyMode {
	switch m {
	case downexv1.ProxyMode_PROXY_MODE_NONE:
		return ProxyModeNone
	case downexv1.ProxyMode_PROXY_MODE_MANUAL:
		return ProxyModeManual
	case downexv1.ProxyMode_PROXY_MODE_PAC:
		return ProxyModePAC
	default:
		return ProxyModeSystem
	}
}

func proxyModeToProto(m ProxyMode) downexv1.ProxyMode {
	switch m {
	case ProxyModeNone:
		return downexv1.ProxyMode_PROXY_MODE_NONE
	case ProxyModeManual:
		return downexv1.ProxyMode_PROXY_MODE_MANUAL
	case ProxyModePAC:
		return downexv1.ProxyMode_PROXY_MODE_PAC
	default:
		return downexv1.ProxyMode_PROXY_MODE_SYSTEM
	}
}

// networkFromProto converts the network fields of API settings (a full replacement);
// validation happens in Normalize. The integrator calls it from settingsFromProto.
func networkFromProto(p *downexv1.Settings) NetworkSettings {
	var n NetworkSettings
	if px := p.GetProxy(); px != nil {
		n.Proxy = ProxySettings{
			Mode:       proxyModeFromProto(px.GetMode()),
			HTTPProxy:  px.GetHttpProxy(),
			HTTPSProxy: px.GetHttpsProxy(),
			SOCKSProxy: px.GetSocksProxy(),
			Bypass:     px.GetBypass(),
			PACURL:     px.GetPacUrl(),
			Username:   px.GetUsername(),
		}
	}
	for _, r := range p.GetHostRules() {
		n.HostRules = append(n.HostRules, HostRule{
			Pattern:         r.GetPattern(),
			MaxConnections:  int(r.GetMaxConnections()),
			SpeedLimit:      r.GetSpeedLimit(),
			MaxRequestBytes: r.GetMaxRequestBytes(),
		})
	}
	for _, l := range p.GetSiteLogins() {
		n.SiteLogins = append(n.SiteLogins, SiteLogin{
			ID:         l.GetId(),
			URLPattern: l.GetUrlPattern(),
			Username:   l.GetUsername(),
		})
	}
	return n
}

// networkToProto writes n into the network fields of p. hasSecret reports whether a
// password is stored for a kind/id (read-only has_password flags), so clients see which
// logins have a password without the password itself; a nil hasSecret reports none.
func networkToProto(p *downexv1.Settings, n NetworkSettings, hasSecret func(kind, id string) bool) {
	if hasSecret == nil {
		hasSecret = func(string, string) bool { return false }
	}
	p.Proxy = &downexv1.ProxySettings{
		Mode:        proxyModeToProto(n.Proxy.Mode),
		HttpProxy:   n.Proxy.HTTPProxy,
		HttpsProxy:  n.Proxy.HTTPSProxy,
		SocksProxy:  n.Proxy.SOCKSProxy,
		Bypass:      n.Proxy.Bypass,
		PacUrl:      n.Proxy.PACURL,
		Username:    n.Proxy.Username,
		HasPassword: hasSecret(SecretKindProxy, ""),
	}
	p.HostRules = make([]*downexv1.HostRule, len(n.HostRules))
	for i, r := range n.HostRules {
		p.HostRules[i] = &downexv1.HostRule{
			Pattern:         r.Pattern,
			MaxConnections:  int32(r.MaxConnections),
			SpeedLimit:      r.SpeedLimit,
			MaxRequestBytes: r.MaxRequestBytes,
		}
	}
	p.SiteLogins = make([]*downexv1.SiteLogin, len(n.SiteLogins))
	for i, l := range n.SiteLogins {
		p.SiteLogins[i] = &downexv1.SiteLogin{
			Id:          l.ID,
			UrlPattern:  l.URLPattern,
			Username:    l.Username,
			HasPassword: hasSecret(SecretKindSiteLogin, l.ID),
		}
	}
}

// ===================== runtime (resolver, credentials, host rules) ====================

// NetworkRuntime turns the live NetworkSettings into the transport.ProxyResolver,
// transport.Credentials and engine.HostRules the engine's factory and coordinator use.
// The integrator creates one with NewNetworkRuntime, passes its Resolver and
// Credentials to transport.HTTPOptions and itself as engine.Options.HostRules, and calls
// Update on every settings change. Its methods are safe for concurrent use.
type NetworkRuntime struct {
	keyring secrets.Keyring
	log     logger
	fetch   func(ctx context.Context, rawURL string) (string, error) // PAC fetcher (tests override)

	issued atomic.Uint64 // numbers of the updates begun (beginUpdate)

	mu           sync.RWMutex
	applied      uint64 // the number of the update in effect
	cfg          NetworkSettings
	resolver     transport.ProxyResolver
	hostLimiters map[string]limiter.Limiter // keyed by rule pattern
	autoCloser   func()                     // releases the resolver in effect (Windows WPAD, a PAC helper process)

	// chosen holds the proxies the resolver in effect returned (proxyKey), so that
	// ProxyLogin answers only for a proxy this runtime routed through, never for an
	// origin server or a proxy named by anything else. Update clears it.
	chosenMu  sync.Mutex
	chosenGen uint64 // the update the set belongs to
	chosen    map[string]struct{}
}

// maxChosenProxies bounds NetworkRuntime.chosen (a PAC script may name many proxies);
// the set starts over when it is full.
const maxChosenProxies = 256

// proxyKey identifies a proxy by kind and address.
func proxyKey(p transport.Proxy) string {
	return p.Kind.Scheme() + "://" + strings.ToLower(p.Addr)
}

// logger is the minimal logging surface the runtime needs (satisfied by *slog.Logger).
type logger interface {
	Warn(msg string, args ...any)
}

type nopLogger struct{}

func (nopLogger) Warn(string, ...any) {}

// NewNetworkRuntime returns a runtime backed by keyring. log may be nil.
func NewNetworkRuntime(keyring secrets.Keyring, log logger) *NetworkRuntime {
	if log == nil {
		log = nopLogger{}
	}
	rt := &NetworkRuntime{
		keyring:      keyring,
		log:          log,
		resolver:     transport.StaticResolver(nil),
		hostLimiters: map[string]limiter.Limiter{},
	}
	rt.fetch = fetchPACScript
	return rt
}

// Update rebuilds the resolver and host-rule limiters from n. It reuses host limiters
// whose pattern persists (so running downloads keep sharing them) and applies a changed
// speed limit in place. A resolver that cannot be built (e.g. an unreachable PAC URL)
// leaves connections direct and returns the error.
func (rt *NetworkRuntime) Update(ctx context.Context, n NetworkSettings) error {
	return rt.update(ctx, n, rt.beginUpdate())
}

// beginUpdate numbers an update. Of concurrent updates the one numbered last wins: an
// earlier one that finishes later (a slow PAC fetch) is dropped.
func (rt *NetworkRuntime) beginUpdate() uint64 { return rt.issued.Add(1) }

// update is Update numbered seq (beginUpdate).
func (rt *NetworkRuntime) update(ctx context.Context, n NetworkSettings, seq uint64) error {
	resolver, closer, rerr := rt.buildResolver(ctx, n.Proxy)

	// The resolver that is not kept is released after rt.mu is unlocked, in the
	// background: closing a WinHTTP session waits for a WPAD lookup in progress, which
	// can take long, and the runtime's readers (every connection, every coordinator's
	// host rules) must never wait for it.
	var release func()
	defer func() {
		if release != nil {
			go release()
		}
	}()
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if seq < rt.applied { // a newer update was applied meanwhile
		release = closer
		return rerr
	}
	rt.applied = seq
	rt.chosenMu.Lock()
	rt.chosenGen, rt.chosen = seq, nil
	rt.chosenMu.Unlock()
	release, rt.autoCloser = rt.autoCloser, closer
	rt.cfg = n.Clone()
	if resolver == nil {
		resolver = transport.StaticResolver(nil)
	}
	rt.resolver = resolver
	next := make(map[string]limiter.Limiter, len(n.HostRules))
	for _, r := range n.HostRules {
		if r.SpeedLimit <= 0 {
			continue
		}
		if lim, ok := rt.hostLimiters[r.Pattern]; ok {
			lim.SetLimit(r.SpeedLimit)
			next[r.Pattern] = lim
		} else {
			next[r.Pattern] = limiter.New(r.SpeedLimit)
		}
	}
	rt.hostLimiters = next
	return rerr
}

// Close releases any per-URL system resolver. It waits for the release, without
// holding the runtime's lock meanwhile.
func (rt *NetworkRuntime) Close() {
	rt.mu.Lock()
	release := rt.autoCloser
	rt.autoCloser = nil
	rt.mu.Unlock()
	if release != nil {
		release()
	}
}

// buildResolver constructs the resolver for a proxy configuration. closer, when
// non-nil, must be called when the resolver is replaced.
func (rt *NetworkRuntime) buildResolver(ctx context.Context, p ProxySettings) (_ transport.ProxyResolver, closer func(), err error) {
	switch p.Mode {
	case ProxyModeNone:
		return transport.StaticResolver(nil), nil, nil
	case ProxyModeManual:
		r, err := transport.NewManualResolver(transport.ManualProxies{
			HTTP: p.HTTPProxy, HTTPS: p.HTTPSProxy, SOCKS: p.SOCKSProxy, Bypass: p.Bypass,
		})
		return r, nil, err
	case ProxyModePAC:
		return rt.buildPACResolver(ctx, p.PACURL)
	default: // SYSTEM
		return rt.buildSystemResolver(ctx)
	}
}

// buildPACResolver fetches and compiles the PAC script and returns a resolver over it
// and the function that stops its evaluator. The script, which may come from anyone on
// the network (WPAD, a pac_url over plain http), runs in an isolated helper process
// (pac.NewIsolated): one that exhausts memory or blocks in native code ends that
// process, never the daemon.
func (rt *NetworkRuntime) buildPACResolver(ctx context.Context, pacURL string) (transport.ProxyResolver, func(), error) {
	script, err := rt.fetch(ctx, pacURL)
	if err != nil {
		return transport.StaticResolver(nil), nil, fmt.Errorf("fetch PAC: %w", err)
	}
	ev, err := pac.NewIsolated(script, pac.Options{})
	if err != nil {
		return transport.StaticResolver(nil), nil, fmt.Errorf("compile PAC: %w", err)
	}
	return pacResolver{ev: ev, log: rt.log}, ev.Close, nil
}

// buildSystemResolver discovers the OS proxy configuration. A PAC/auto-config URL is
// evaluated with internal/pac; WPAD auto-detect uses a per-URL system resolver where
// supported; otherwise the static proxies apply.
func (rt *NetworkRuntime) buildSystemResolver(ctx context.Context) (transport.ProxyResolver, func(), error) {
	s, err := sysproxy.Discover(ctx)
	if err != nil {
		return transport.StaticResolver(nil), nil, fmt.Errorf("discover system proxy: %w", err)
	}
	if s.PACURL != "" {
		return rt.buildPACResolver(ctx, s.PACURL)
	}
	if s.AutoDetect && s.HTTPProxy == "" && s.HTTPSProxy == "" && s.SOCKSProxy == "" {
		if ar, aerr := sysproxy.NewAutoResolver(""); aerr == nil {
			return ar, ar.Close, nil
		}
	}
	r, err := sysproxy.NewStaticResolver(s)
	if bad := s.UnusableBypass(); err == nil && len(bad) > 0 {
		rt.log.Warn("ignoring system proxy exceptions Downex cannot read", "entries", bad)
	}
	return r, nil, err
}

// ResolveProxy implements transport.ProxyResolver. The proxies it returns are
// remembered for ProxyLogin.
func (rt *NetworkRuntime) ResolveProxy(ctx context.Context, target *url.URL) ([]transport.Proxy, error) {
	rt.mu.RLock()
	r, gen := rt.resolver, rt.applied
	rt.mu.RUnlock()
	if r == nil {
		return nil, nil
	}
	routes, err := r.ResolveProxy(ctx, target)
	if err == nil {
		rt.remember(gen, routes)
	}
	return routes, err
}

// remember records the proxies among routes, resolved with update gen in effect.
func (rt *NetworkRuntime) remember(gen uint64, routes []transport.Proxy) {
	rt.chosenMu.Lock()
	defer rt.chosenMu.Unlock()
	if gen != rt.chosenGen { // an update replaced the resolver meanwhile
		return
	}
	for _, p := range routes {
		if p.Kind == transport.ProxyDirect {
			continue
		}
		k := proxyKey(p)
		if _, ok := rt.chosen[k]; ok {
			continue
		}
		if rt.chosen == nil || len(rt.chosen) >= maxChosenProxies {
			rt.chosen = make(map[string]struct{})
		}
		rt.chosen[k] = struct{}{}
	}
}

// wasChosen reports whether the resolver in effect returned p.
func (rt *NetworkRuntime) wasChosen(p transport.Proxy) bool {
	if p.Kind == transport.ProxyDirect || p.Addr == "" {
		return false
	}
	rt.chosenMu.Lock()
	defer rt.chosenMu.Unlock()
	_, ok := rt.chosen[proxyKey(p)]
	return ok
}

// ProxyURL is a net/http proxy function (http.Transport.Proxy) that follows the runtime,
// for requests made outside the engine (update checks): the first route of the resolver
// that net/http can use, an HTTP or HTTPS proxy (with the stored login of a manual proxy,
// sent only to that proxy) or a SOCKS5 proxy. Direct (and SOCKS4, which net/http cannot
// speak) yields nil.
func (rt *NetworkRuntime) ProxyURL(req *http.Request) (*url.URL, error) {
	routes, err := rt.ResolveProxy(req.Context(), req.URL)
	if err != nil {
		return nil, err
	}
	for _, p := range routes {
		var scheme string
		switch p.Kind {
		case transport.ProxyDirect:
			return nil, nil
		case transport.ProxyHTTP:
			scheme = "http"
		case transport.ProxyHTTPS:
			scheme = "https"
		case transport.ProxySOCKS5:
			scheme = "socks5"
		default:
			continue
		}
		u := &url.URL{Scheme: scheme, Host: p.Addr}
		user, pass := p.Username, p.Password
		if user == "" && pass == "" {
			if l, ok := rt.ProxyLogin(p); ok && !l.CurrentUser {
				user, pass = l.Username, l.Password
			}
		}
		if user != "" || pass != "" {
			u.User = url.UserPassword(user, pass)
		}
		return u, nil
	}
	return nil, nil
}

// ServerLogin implements transport.Credentials: the first site login whose pattern
// matches u, with its password from the keyring. On Windows a matching login without a
// stored password answers NTLM/Negotiate as the current user.
func (rt *NetworkRuntime) ServerLogin(u *url.URL) (transport.Login, bool) {
	rt.mu.RLock()
	logins := rt.cfg.SiteLogins
	rt.mu.RUnlock()
	for _, l := range logins {
		if !siteMatch(l.URLPattern, u) {
			continue
		}
		return rt.loginFor(SecretKindSiteLogin, l.ID, l.Username), true
	}
	return transport.Login{}, false
}

// ProxyLogin implements transport.Credentials for proxy authentication. It answers only
// for a proxy that the resolver in effect returned (never for an origin server, nor for
// a proxy of an earlier configuration): MANUAL with the configured user name and the
// stored password; SYSTEM and PAC on Windows as the current user (NTLM/Negotiate via
// SSPI), since Downex stores no credentials for those proxies; NONE never.
func (rt *NetworkRuntime) ProxyLogin(p transport.Proxy) (transport.Login, bool) {
	rt.mu.RLock()
	user := rt.cfg.Proxy.Username
	mode := rt.cfg.Proxy.Mode
	rt.mu.RUnlock()
	if mode == ProxyModeNone || !rt.wasChosen(p) {
		return transport.Login{}, false
	}
	if mode != ProxyModeManual {
		if runtime.GOOS == "windows" {
			return transport.Login{CurrentUser: true}, true
		}
		return transport.Login{}, false
	}
	return rt.loginFor(SecretKindProxy, "", user), true
}

// loginFor builds a Login for a stored secret, reading the password from the keyring.
func (rt *NetworkRuntime) loginFor(kind, id, user string) transport.Login {
	l := transport.Login{Username: user}
	if rt.keyring != nil {
		if pw, err := rt.keyring.Get(secretName(kind, id)); err == nil {
			l.Password = string(pw)
		}
	}
	if l.Password == "" && runtime.GOOS == "windows" {
		l.CurrentUser = true
	}
	return l
}

// ruleHost returns the host name of a host or "host:port" (the engine passes a URL's
// host, which carries an explicit port): host rule patterns never name a port.
func ruleHost(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
}

// HostMaxConnections implements engine.HostRules: the cap of the most specific matching
// rule that sets one; a built-in rule only for a host that no user rule matches.
func (rt *NetworkRuntime) HostMaxConnections(host string) int {
	host = ruleHost(host)
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	return int(hostRuleValue(rt.cfg.HostRules, host, func(r HostRule) int64 { return int64(r.MaxConnections) }))
}

// HostMaxRequestBytes implements engine.RequestCaps (docs/SITE-MEDIA-SPEC.md §2.4): the
// longest ranged request to host, 0 for no cap. The most specific matching rule that
// sets one wins; a built-in rule applies only to a host that no user rule matches.
func (rt *NetworkRuntime) HostMaxRequestBytes(host string) int64 {
	host = ruleHost(host)
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	return hostRuleValue(rt.cfg.HostRules, host, func(r HostRule) int64 { return r.MaxRequestBytes })
}

// hostRuleValue returns field of the most specific rule of rules matching host that sets
// it (a positive value), or 0. Without any matching user rule the built-in rules answer.
func hostRuleValue(rules []HostRule, host string, field func(HostRule) int64) int64 {
	best, score, matched := int64(0), -1, false
	for _, r := range rules {
		s := hostPatternScore(r.Pattern, host)
		if s < 0 {
			continue
		}
		matched = true
		if v := field(r); v > 0 && s > score {
			best, score = v, s
		}
	}
	if matched {
		return best
	}
	for _, r := range builtinHostRules {
		if v := field(r); v > 0 && hostPatternScore(r.Pattern, host) > score {
			best, score = v, hostPatternScore(r.Pattern, host)
		}
	}
	return best
}

// HostLimiter implements engine.HostRules: the shared limiter of the most specific
// matching rule with a speed limit, or nil.
func (rt *NetworkRuntime) HostLimiter(host string) limiter.Limiter {
	host = ruleHost(host)
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	var best string
	score := -1
	for _, r := range rt.cfg.HostRules {
		if r.SpeedLimit <= 0 {
			continue
		}
		if s := hostPatternScore(r.Pattern, host); s > score {
			best, score = r.Pattern, s
		}
	}
	if score < 0 {
		return nil
	}
	return rt.hostLimiters[best]
}

// HasSecret reports whether a password is stored for kind/id (for networkToProto).
func (rt *NetworkRuntime) HasSecret(kind, id string) bool {
	if rt.keyring == nil {
		return false
	}
	_, err := rt.keyring.Get(secretName(kind, id))
	return err == nil
}

// secretName is the keyring name for a secret kind and id.
func secretName(kind, id string) string {
	if kind == SecretKindProxy || id == "" {
		return proxySecretName
	}
	return kind + ":" + id
}

// pacResolver adapts an isolated PAC evaluator to transport.ProxyResolver.
type pacResolver struct {
	ev  *pac.Isolated
	log logger
}

func (p pacResolver) ResolveProxy(_ context.Context, target *url.URL) ([]transport.Proxy, error) {
	results, err := p.ev.FindProxy(target)
	if err != nil {
		p.log.Warn("PAC evaluation failed; connecting directly", "host", target.Hostname(), "err", err)
		return nil, nil
	}
	return pacResultsToProxies(results), nil
}

// pacResultsToProxies maps PAC results to transport routes.
func pacResultsToProxies(results []pac.Result) []transport.Proxy {
	out := make([]transport.Proxy, 0, len(results))
	for _, r := range results {
		switch r.Kind {
		case pac.Direct:
			out = append(out, transport.Direct)
		case pac.HTTP:
			out = append(out, transport.Proxy{Kind: transport.ProxyHTTP, Addr: net.JoinHostPort(r.Host, r.Port)})
		case pac.HTTPS:
			out = append(out, transport.Proxy{Kind: transport.ProxyHTTPS, Addr: net.JoinHostPort(r.Host, r.Port)})
		case pac.SOCKS, pac.SOCKS4A:
			kind := transport.ProxySOCKS4
			if r.Kind == pac.SOCKS4A {
				kind = transport.ProxySOCKS4A
			}
			out = append(out, transport.Proxy{Kind: kind, Addr: net.JoinHostPort(r.Host, r.Port)})
		case pac.SOCKS5:
			out = append(out, transport.Proxy{Kind: transport.ProxySOCKS5, Addr: net.JoinHostPort(r.Host, r.Port)})
		}
	}
	return out
}

// fetchPACScript fetches a PAC script from an http, https or file URL (≤ 1 MiB). It uses
// a direct HTTP client (a PAC server is not itself proxied).
func fetchPACScript(ctx context.Context, rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", errors.New("invalid pac_url")
	}
	switch strings.ToLower(u.Scheme) {
	case "file":
		b, err := os.ReadFile(filePathFromURL(u))
		if err != nil {
			return "", err
		}
		if len(b) > pacMaxBytes {
			return "", pac.ErrTooLarge
		}
		return string(b), nil
	case "http", "https":
	default:
		return "", fmt.Errorf("unsupported pac_url scheme %q", u.Scheme)
	}
	cctx, cancel := context.WithTimeout(ctx, pacFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{Transport: &http.Transport{Proxy: nil}}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("pac_url returned HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, pacMaxBytes+1))
	if err != nil {
		return "", err
	}
	if len(b) > pacMaxBytes {
		return "", pac.ErrTooLarge
	}
	return string(b), nil
}

// filePathFromURL turns a file:// URL into a local path (handling the Windows
// file:///C:/… form).
func filePathFromURL(u *url.URL) string {
	p := u.Path
	if runtime.GOOS == "windows" {
		p = strings.TrimPrefix(p, "/")
		p = strings.ReplaceAll(p, "/", "\\")
	}
	return p
}

// ===================== pattern matching =====================

// hostPatternScore returns a specificity score for pattern matching host, or -1 for no
// match. A longer matched literal (fewer wildcards) scores higher, so the most specific
// rule wins.
func hostPatternScore(pattern, host string) int {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	pattern = strings.ToLower(pattern)
	if pattern == host {
		return 1000 + len(host)
	}
	if strings.HasPrefix(pattern, "*.") {
		suffix := pattern[1:] // ".example.com"
		if strings.HasSuffix(host, suffix) || host == pattern[2:] {
			return 500 + len(suffix)
		}
		return -1
	}
	if strings.ContainsAny(pattern, "*?") {
		if netGlob(pattern, host) {
			return len(strings.ReplaceAll(strings.ReplaceAll(pattern, "*", ""), "?", ""))
		}
		return -1
	}
	return -1
}

// siteMatch reports whether a site-login pattern matches u. A URL-shaped pattern
// ("https://host/path*") matches scheme, host and path; a host pattern matches the host.
func siteMatch(pattern string, u *url.URL) bool {
	if u == nil {
		return false
	}
	if strings.Contains(pattern, "://") {
		i := strings.Index(pattern, "://")
		scheme := strings.ToLower(pattern[:i])
		if scheme != "*" && scheme != strings.ToLower(u.Scheme) {
			return false
		}
		target := strings.ToLower(u.Host) + u.EscapedPath()
		return netGlob(strings.ToLower(pattern[i+3:]), target)
	}
	return hostPatternScore(strings.ToLower(pattern), u.Hostname()) >= 0
}

// netGlob matches s against a glob with '*' (any run) and '?' (one byte).
func netGlob(pattern, s string) bool {
	px, sx := 0, 0
	nextPx, nextSx := -1, -1
	for px < len(pattern) || sx < len(s) {
		if px < len(pattern) {
			switch c := pattern[px]; c {
			case '*':
				nextPx, nextSx = px, sx+1
				px++
				continue
			case '?':
				if sx < len(s) {
					px++
					sx++
					continue
				}
			default:
				if sx < len(s) && s[sx] == c {
					px++
					sx++
					continue
				}
			}
		}
		if nextSx > 0 && nextSx <= len(s) {
			px, sx = nextPx, nextSx
			continue
		}
		return false
	}
	return true
}
