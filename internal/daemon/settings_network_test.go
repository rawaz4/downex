package daemon

import (
	"context"
	"net/url"
	"runtime"
	"strings"
	"testing"

	"downex/internal/secrets"
	"downex/internal/transport"

	downexv1 "downex/internal/rpc/downex/v1"
)

func mustURLN(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
}

func seqIDs() func() string {
	n := 0
	return func() string { n++; return "id" + string(rune('0'+n)) }
}

func TestNetworkNormalizeAssignsIDs(t *testing.T) {
	n := NetworkSettings{
		Proxy: ProxySettings{Mode: ProxyModeManual, HTTPProxy: "proxy:8080", Bypass: "*.local"},
		SiteLogins: []SiteLogin{
			{URLPattern: "https://a.example/*", Username: "u1"},
			{ID: "keep", URLPattern: "*.b.example", Username: "u2"},
		},
		HostRules: []HostRule{{Pattern: "*.cdn.example", MaxConnections: 4, SpeedLimit: 1000}},
	}
	got, err := n.Normalize(seqIDs())
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if got.SiteLogins[0].ID == "" {
		t.Error("missing id was not assigned")
	}
	if got.SiteLogins[1].ID != "keep" {
		t.Errorf("existing id changed to %q", got.SiteLogins[1].ID)
	}
}

func TestNetworkNormalizeRejectsBad(t *testing.T) {
	cases := []NetworkSettings{
		{Proxy: ProxySettings{Mode: ProxyModePAC, PACURL: "ftp://bad/proxy.pac"}},
		{Proxy: ProxySettings{Mode: ProxyModeManual, SOCKSProxy: "http://not-socks:1"}},
		{HostRules: []HostRule{{Pattern: "", MaxConnections: 1}}},
		{HostRules: []HostRule{{Pattern: "x", MaxConnections: 999}}},
		{SiteLogins: []SiteLogin{{URLPattern: ""}}},
	}
	for i, c := range cases {
		if _, err := c.Normalize(seqIDs()); err == nil {
			t.Errorf("case %d: Normalize accepted invalid settings", i)
		}
	}
}

func TestNetworkProtoRoundTrip(t *testing.T) {
	n := NetworkSettings{
		Proxy: ProxySettings{Mode: ProxyModeManual, HTTPProxy: "p:8080", HTTPSProxy: "p:8443",
			SOCKSProxy: "socks5://s:1080", Bypass: "*.local", Username: "pu"},
		HostRules:  []HostRule{{Pattern: "*.cdn.example", MaxConnections: 4, SpeedLimit: 2048}},
		SiteLogins: []SiteLogin{{ID: "x1", URLPattern: "https://a.example/*", Username: "u1"}},
	}
	var p downexv1.Settings
	networkToProto(&p, n, func(kind, id string) bool {
		return kind == SecretKindSiteLogin && id == "x1"
	})
	if p.GetProxy().GetMode() != downexv1.ProxyMode_PROXY_MODE_MANUAL {
		t.Errorf("mode = %v", p.GetProxy().GetMode())
	}
	if !p.GetSiteLogins()[0].GetHasPassword() {
		t.Error("has_password should be true for x1")
	}
	back := networkFromProto(&p)
	if back.Proxy.HTTPProxy != "p:8080" || back.Proxy.SOCKSProxy != "socks5://s:1080" {
		t.Errorf("proxy round trip wrong: %+v", back.Proxy)
	}
	if len(back.HostRules) != 1 || back.HostRules[0].MaxConnections != 4 {
		t.Errorf("host rules round trip wrong: %+v", back.HostRules)
	}
	if len(back.SiteLogins) != 1 || back.SiteLogins[0].ID != "x1" {
		t.Errorf("site logins round trip wrong: %+v", back.SiteLogins)
	}
}

func TestRuntimeManualResolver(t *testing.T) {
	rt := NewNetworkRuntime(nil, nil)
	t.Cleanup(rt.Close)
	err := rt.Update(context.Background(), NetworkSettings{
		Proxy: ProxySettings{Mode: ProxyModeManual, HTTPProxy: "proxy:8080", Bypass: "*.local"},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	routes, err := rt.ResolveProxy(context.Background(), mustURLN(t, "http://example.com/"))
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 || routes[0].Addr != "proxy:8080" {
		t.Fatalf("route = %v", routes)
	}
	routes, _ = rt.ResolveProxy(context.Background(), mustURLN(t, "http://db.local/"))
	if len(routes) != 0 {
		t.Errorf("bypassed host should be direct, got %v", routes)
	}
}

func TestRuntimeNoneIsDirect(t *testing.T) {
	rt := NewNetworkRuntime(nil, nil)
	if err := rt.Update(context.Background(), NetworkSettings{Proxy: ProxySettings{Mode: ProxyModeNone}}); err != nil {
		t.Fatal(err)
	}
	routes, _ := rt.ResolveProxy(context.Background(), mustURLN(t, "http://example.com/"))
	if len(routes) != 0 {
		t.Errorf("NONE should be direct, got %v", routes)
	}
}

func TestRuntimePACResolver(t *testing.T) {
	rt := NewNetworkRuntime(nil, nil)
	rt.fetch = func(context.Context, string) (string, error) {
		return `function FindProxyForURL(u,h){ if (shExpMatch(h,"*.direct.test")) return "DIRECT"; return "PROXY pac.proxy:3128"; }`, nil
	}
	if err := rt.Update(context.Background(), NetworkSettings{Proxy: ProxySettings{Mode: ProxyModePAC, PACURL: "http://wpad/proxy.pac"}}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	routes, err := rt.ResolveProxy(context.Background(), mustURLN(t, "http://example.com/"))
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 || routes[0].Kind != transport.ProxyHTTP || routes[0].Addr != "pac.proxy:3128" {
		t.Fatalf("PAC route = %v", routes)
	}
	routes, _ = rt.ResolveProxy(context.Background(), mustURLN(t, "http://x.direct.test/"))
	if len(routes) != 1 || routes[0].Kind != transport.ProxyDirect {
		t.Fatalf("PAC DIRECT route = %v", routes)
	}
}

func TestRuntimeHostRules(t *testing.T) {
	rt := NewNetworkRuntime(nil, nil)
	err := rt.Update(context.Background(), NetworkSettings{
		HostRules: []HostRule{
			{Pattern: "*.example.com", MaxConnections: 2, SpeedLimit: 1000},
			{Pattern: "dl.example.com", MaxConnections: 8, SpeedLimit: 5000}, // more specific
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := rt.HostMaxConnections("dl.example.com"); got != 8 {
		t.Errorf("most specific rule should win: got %d, want 8", got)
	}
	if got := rt.HostMaxConnections("other.example.com"); got != 2 {
		t.Errorf("wildcard rule: got %d, want 2", got)
	}
	if got := rt.HostMaxConnections("unrelated.net"); got != 0 {
		t.Errorf("no rule should mean 0, got %d", got)
	}
	lim := rt.HostLimiter("dl.example.com")
	if lim == nil || lim.Limit() != 5000 {
		t.Fatalf("host limiter = %v", lim)
	}
	// The same host returns the same shared limiter instance.
	if rt.HostLimiter("dl.example.com") != lim {
		t.Error("host limiter is not shared across calls")
	}
}

func TestRuntimeHostLimiterReusedAcrossUpdates(t *testing.T) {
	rt := NewNetworkRuntime(nil, nil)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(rt.Update(ctx, NetworkSettings{HostRules: []HostRule{{Pattern: "h.example", SpeedLimit: 1000}}}))
	lim1 := rt.HostLimiter("h.example")
	must(rt.Update(ctx, NetworkSettings{HostRules: []HostRule{{Pattern: "h.example", SpeedLimit: 2000}}}))
	lim2 := rt.HostLimiter("h.example")
	if lim1 != lim2 {
		t.Error("limiter should be reused when the pattern persists")
	}
	if lim2.Limit() != 2000 {
		t.Errorf("limiter limit should update in place, got %d", lim2.Limit())
	}
}

func TestRuntimeCredentials(t *testing.T) {
	dir := t.TempDir()
	k, err := secrets.NewFileKeyring(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := k.Set(secretName(SecretKindSiteLogin, "x1"), []byte("pw-secret")); err != nil {
		t.Fatal(err)
	}
	rt := NewNetworkRuntime(k, nil)
	err = rt.Update(context.Background(), NetworkSettings{
		SiteLogins: []SiteLogin{{ID: "x1", URLPattern: "https://files.example/*", Username: "neo"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	login, ok := rt.ServerLogin(mustURLN(t, "https://files.example/a.bin"))
	if !ok || login.Username != "neo" || login.Password != "pw-secret" {
		t.Fatalf("ServerLogin = %+v ok=%v", login, ok)
	}
	if _, ok := rt.ServerLogin(mustURLN(t, "https://other.example/a.bin")); ok {
		t.Error("credentials must not match a different host")
	}
	if !rt.HasSecret(SecretKindSiteLogin, "x1") {
		t.Error("HasSecret should report the stored password")
	}
}

// TestProxyLoginOnlyForChosenProxies: ProxyLogin answers only for a proxy the resolver
// in effect returned. An origin server (or any other address) that asks for proxy
// credentials gets neither the stored proxy password nor a current-user handshake.
func TestProxyLoginOnlyForChosenProxies(t *testing.T) {
	ctx := context.Background()
	origin := transport.Proxy{Kind: transport.ProxyHTTP, Addr: "origin.test:443"}

	t.Run("default runtime", func(t *testing.T) {
		rt := NewNetworkRuntime(nil, nil) // SYSTEM until the first Update
		if l, ok := rt.ProxyLogin(origin); ok {
			t.Fatalf("ProxyLogin(%v) = %v (current user %v) before any proxy was chosen", origin, l, l.CurrentUser)
		}
	})

	t.Run("none", func(t *testing.T) {
		rt := NewNetworkRuntime(nil, nil)
		if err := rt.Update(ctx, NetworkSettings{Proxy: ProxySettings{Mode: ProxyModeNone}}); err != nil {
			t.Fatal(err)
		}
		if _, err := rt.ResolveProxy(ctx, mustURLN(t, "http://example.test/")); err != nil {
			t.Fatal(err)
		}
		if l, ok := rt.ProxyLogin(origin); ok {
			t.Fatalf("NONE: ProxyLogin = %v (current user %v)", l, l.CurrentUser)
		}
	})

	t.Run("manual", func(t *testing.T) {
		k, err := secrets.NewFileKeyring(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if err := k.Set(secretName(SecretKindProxy, ""), []byte("zzproxysecret")); err != nil {
			t.Fatal(err)
		}
		rt := NewNetworkRuntime(k, nil)
		if err := rt.Update(ctx, NetworkSettings{Proxy: ProxySettings{
			Mode: ProxyModeManual, HTTPProxy: "proxy.test:8080", Username: "zzuser"}}); err != nil {
			t.Fatal(err)
		}
		if l, ok := rt.ProxyLogin(origin); ok {
			t.Fatalf("MANUAL: ProxyLogin for an origin = %v (password given: %v)", l, l.Password != "")
		}
		routes, err := rt.ResolveProxy(ctx, mustURLN(t, "http://example.test/"))
		if err != nil || len(routes) != 1 {
			t.Fatalf("routes = %v, %v", routes, err)
		}
		if l, ok := rt.ProxyLogin(origin); ok {
			t.Fatalf("MANUAL: ProxyLogin for an origin after resolving = %v", l)
		}
		l, ok := rt.ProxyLogin(routes[0])
		if !ok || l.Username != "zzuser" || l.Password != "zzproxysecret" {
			t.Fatalf("MANUAL: ProxyLogin(%v) = %v ok=%v, want the stored login", routes[0], l, ok)
		}
		// After a change of proxy, the old one no longer gets the password.
		if err := rt.Update(ctx, NetworkSettings{Proxy: ProxySettings{
			Mode: ProxyModeManual, HTTPProxy: "other.test:8080", Username: "zzuser"}}); err != nil {
			t.Fatal(err)
		}
		if l, ok := rt.ProxyLogin(routes[0]); ok {
			t.Fatalf("MANUAL: ProxyLogin for the replaced proxy = %v", l)
		}
	})

	t.Run("pac", func(t *testing.T) {
		rt := NewNetworkRuntime(nil, nil)
		rt.fetch = func(context.Context, string) (string, error) {
			return `function FindProxyForURL(u,h){ return "PROXY pac.proxy.test:3128"; }`, nil
		}
		if err := rt.Update(ctx, NetworkSettings{Proxy: ProxySettings{Mode: ProxyModePAC, PACURL: "http://wpad.test/proxy.pac"}}); err != nil {
			t.Fatal(err)
		}
		routes, err := rt.ResolveProxy(ctx, mustURLN(t, "http://example.test/"))
		if err != nil || len(routes) != 1 {
			t.Fatalf("routes = %v, %v", routes, err)
		}
		if l, ok := rt.ProxyLogin(origin); ok {
			t.Fatalf("PAC: ProxyLogin for an origin = %v (current user %v)", l, l.CurrentUser)
		}
		l, ok := rt.ProxyLogin(routes[0])
		if wantOK := runtime.GOOS == "windows"; ok != wantOK || (ok && !l.CurrentUser) {
			t.Fatalf("PAC: ProxyLogin(%v) = %v ok=%v, want the current user only on Windows", routes[0], l, ok)
		}
	})
}

// TestProxySettingsRejectUserinfo: a proxy password typed into a proxy URL is refused
// (passwords never appear in Settings; they go to the keyring with SetSecret), in every
// mode, with an error that names the field but not the value.
func TestProxySettingsRejectUserinfo(t *testing.T) {
	const secret = "zzhunter2"
	for _, tc := range []struct {
		field string
		p     ProxySettings
	}{
		{"socks_proxy", ProxySettings{Mode: ProxyModeManual, SOCKSProxy: "socks5://bob:" + secret + "@proxy.test:1080"}},
		{"http_proxy", ProxySettings{Mode: ProxyModeManual, HTTPProxy: "bob:" + secret + "@proxy.test:8080"}},
		{"http_proxy", ProxySettings{Mode: ProxyModeManual, HTTPProxy: "http://bob:" + secret + "@proxy.test:8080"}},
		{"https_proxy", ProxySettings{Mode: ProxyModeManual, HTTPSProxy: "https://:" + secret + "@proxy.test"}},
		{"https_proxy", ProxySettings{Mode: ProxyModeSystem, HTTPSProxy: "bob:" + secret + "@proxy.test:8443"}},
		{"pac_url", ProxySettings{Mode: ProxyModePAC, PACURL: "http://bob:" + secret + "@wpad.test/proxy.pac"}},
	} {
		_, err := NetworkSettings{Proxy: tc.p}.Normalize(seqIDs())
		switch {
		case err == nil:
			t.Errorf("%+v: Normalize accepted a password in %s", tc.p.Mode, tc.field)
		case strings.Contains(err.Error(), secret):
			t.Errorf("%s: the error repeats the password: %v", tc.field, err)
		case !strings.Contains(err.Error(), tc.field):
			t.Errorf("%s: the error does not name the field: %v", tc.field, err)
		}
	}
	// A '@' outside the authority is not user information.
	if _, err := (NetworkSettings{Proxy: ProxySettings{Mode: ProxyModePAC, PACURL: "http://wpad.test/a@b.pac"}}).Normalize(seqIDs()); err != nil {
		t.Errorf("pac_url with '@' in the path: %v", err)
	}
}

// TestRepairStripsProxyUserinfo: a proxy password that an older version saved in
// settings.json is removed when the settings are read, keeping the rest of the proxy
// configuration; the problem names the field, never the password.
func TestRepairStripsProxyUserinfo(t *testing.T) {
	const secret = "zzhunter2"
	in := NetworkSettings{Proxy: ProxySettings{
		Mode:       ProxyModeManual,
		HTTPProxy:  "bob:" + secret + "@proxy.test:8080",
		SOCKSProxy: "socks5://bob:" + secret + "@proxy.test:1080",
		Bypass:     "*.local",
		Username:   "bob",
	}}
	got, problems := repairNetwork(in)
	want := ProxySettings{Mode: ProxyModeManual, HTTPProxy: "proxy.test:8080", SOCKSProxy: "socks5://proxy.test:1080",
		Bypass: "*.local", Username: "bob"}
	if got.Proxy != want {
		t.Errorf("repaired proxy = %+v, want %+v", got.Proxy, want)
	}
	if len(problems) != 2 {
		t.Errorf("problems = %q, want one per stripped field", problems)
	}
	for _, p := range problems {
		if strings.Contains(p, secret) {
			t.Errorf("problem repeats the password: %q", p)
		}
	}
}

func TestSecretName(t *testing.T) {
	if secretName(SecretKindProxy, "") != "proxy" {
		t.Error("proxy secret name")
	}
	if secretName(SecretKindSiteLogin, "abc") != "site_login:abc" {
		t.Error("site login secret name")
	}
}

// TestHostRuleMaxRequestBytes (docs/SITE-MEDIA-SPEC.md §3.8): max_request_bytes is 0 or
// 1 MiB..1 GiB, and it goes through the proto conversion.
func TestHostRuleMaxRequestBytes(t *testing.T) {
	for _, ok := range []int64{0, MinRequestBytes, 10 << 20, MaxRequestBytes} {
		n := NetworkSettings{HostRules: []HostRule{{Pattern: "*.cdn.example", MaxRequestBytes: ok}}}
		if got, err := n.Normalize(seqIDs()); err != nil || got.HostRules[0].MaxRequestBytes != ok {
			t.Errorf("max_request_bytes %d: %+v, %v", ok, got.HostRules, err)
		}
	}
	for _, bad := range []int64{-1, 1, MinRequestBytes - 1, MaxRequestBytes + 1} {
		n := NetworkSettings{HostRules: []HostRule{{Pattern: "*.cdn.example", MaxRequestBytes: bad}}}
		if _, err := n.Normalize(seqIDs()); err == nil || !strings.Contains(err.Error(), "max_request_bytes") {
			t.Errorf("max_request_bytes %d accepted: %v", bad, err)
		}
	}
	var p downexv1.Settings
	networkToProto(&p, NetworkSettings{HostRules: []HostRule{{Pattern: "a.example", MaxRequestBytes: 4 << 20}}}, nil)
	if p.GetHostRules()[0].GetMaxRequestBytes() != 4<<20 {
		t.Errorf("toProto = %v", p.GetHostRules())
	}
	if back := networkFromProto(&p); back.HostRules[0].MaxRequestBytes != 4<<20 {
		t.Errorf("fromProto = %+v", back.HostRules)
	}
}

// TestRuntimeRequestCaps: HostMaxRequestBytes takes the most specific matching user rule
// that sets a cap; the built-in googlevideo rule (1 MiB requests, no connection cap)
// applies only to hosts that no user rule matches.
func TestRuntimeRequestCaps(t *testing.T) {
	rt := NewNetworkRuntime(nil, nil)
	if got := rt.HostMaxRequestBytes("rr3---sn-abc.googlevideo.com:443"); got != 1<<20 {
		t.Errorf("built-in cap = %d", got)
	}
	if got := rt.HostMaxConnections("rr3---sn-abc.googlevideo.com"); got != 0 {
		t.Errorf("built-in connections = %d", got)
	}
	if got := rt.HostMaxRequestBytes("www.youtube.com"); got != 0 {
		t.Errorf("no rule = %d", got)
	}
	if got := rt.HostMaxConnections("googlevideo.example"); got != 0 {
		t.Errorf("another host = %d", got)
	}
	err := rt.Update(context.Background(), NetworkSettings{HostRules: []HostRule{
		{Pattern: "*.example.com", MaxRequestBytes: 2 << 20},
		{Pattern: "dl.example.com", MaxRequestBytes: 8 << 20},
		{Pattern: "plain.example.com", MaxConnections: 3},
		{Pattern: "rr1.googlevideo.com", MaxConnections: 8},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for host, want := range map[string]int64{
		"dl.example.com":      8 << 20,
		"x.example.com":       2 << 20,
		"plain.example.com":   2 << 20, // the wildcard sets the cap; the specific rule only connections
		"rr1.googlevideo.com": 0,       // a user rule matches: the built-in rule does not apply
		"rr2.googlevideo.com": 1 << 20,
		"[::1]:8080":          0,
	} {
		if got := rt.HostMaxRequestBytes(host); got != want {
			t.Errorf("HostMaxRequestBytes(%s) = %d, want %d", host, got, want)
		}
	}
	for host, want := range map[string]int{"rr1.googlevideo.com": 8, "rr2.googlevideo.com": 0, "plain.example.com": 3} {
		if got := rt.HostMaxConnections(host); got != want {
			t.Errorf("HostMaxConnections(%s) = %d, want %d", host, got, want)
		}
	}
	// The built-in rules are not user rules.
	if p := protoSettings(DefaultSettings()); len(p.GetHostRules()) != 0 {
		t.Errorf("default host rules = %v", p.GetHostRules())
	}
}
