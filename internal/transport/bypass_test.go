package transport

import (
	"net/url"
	"testing"
)

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
}

func TestBypassMatch(t *testing.T) {
	list, err := ParseBypass("<local>; *.example.com; 10.0.0.0/8; intranet:8080; https://secure.test; 192.168.1.5; fe80::/10")
	if err != nil {
		t.Fatalf("ParseBypass: %v", err)
	}
	cases := []struct {
		url  string
		want bool
	}{
		{"http://localhost/a", true},       // loopback is always bypassed
		{"http://127.0.0.1/a", true},       // loopback IP
		{"http://intranet/a", true},        // <local>: no dot
		{"http://intranet2/a", true},       // <local>: any dotless host
		{"http://host.with.dots/a", false}, // dotted, no rule
		{"http://www.example.com/a", true}, // *.example.com
		{"http://example.com/a", true},     // *.example.com also matches the bare domain
		{"http://evilexample.com/a", false},
		{"http://10.1.2.3/a", true},      // in 10.0.0.0/8
		{"http://11.1.2.3/a", false},     // outside
		{"http://192.168.1.5/a", true},   // exact IP
		{"http://intranet:8080/a", true}, // host:port rule, matching port (also <local>)
		{"https://secure.test/a", true},  // scheme-qualified
		{"http://secure.test/a", false},  // wrong scheme; dotted so <local> does not apply
		{"http://[fe80::1]/a", true},     // IPv6 CIDR
	}
	for _, c := range cases {
		if got := list.Match(mustURL(t, c.url)); got != c.want {
			t.Errorf("Match(%q) = %v, want %v", c.url, got, c.want)
		}
	}
}

func TestBypassNilMatchesLoopbackOnly(t *testing.T) {
	var b *BypassList
	if !b.Match(mustURL(t, "http://localhost/")) {
		t.Error("nil bypass should match localhost")
	}
	if b.Match(mustURL(t, "http://example.com/")) {
		t.Error("nil bypass should not match a public host")
	}
}

func TestBypassNoLoopback(t *testing.T) {
	b, err := ParseBypass("<-loopback>")
	if err != nil {
		t.Fatal(err)
	}
	if b.Match(mustURL(t, "http://127.0.0.1/")) {
		t.Error("<-loopback> should let loopback be proxied")
	}
}

func TestBypassWildcardPort(t *testing.T) {
	b, err := ParseBypass("cache.local:3128")
	if err != nil {
		t.Fatal(err)
	}
	if !b.Match(mustURL(t, "http://cache.local:3128/")) {
		t.Error("should match the exact port")
	}
	if b.Match(mustURL(t, "http://cache.local:9999/")) {
		t.Error("should not match another port")
	}
	if b.Match(mustURL(t, "http://cache.local/")) {
		t.Error("default port 80 should not match the :3128 rule")
	}
}

func TestParseBypassRejectsGarbage(t *testing.T) {
	for _, bad := range []string{"10.0.0.0/99", "host:99999"} {
		if _, err := ParseBypass(bad); err == nil {
			t.Errorf("ParseBypass(%q) accepted invalid input", bad)
		}
	}
}

func TestManualResolverSelection(t *testing.T) {
	r, err := NewManualResolver(ManualProxies{
		HTTP:   "http.proxy:8080",
		HTTPS:  "https.proxy:8443",
		Bypass: "*.internal; <local>",
	})
	if err != nil {
		t.Fatalf("NewManualResolver: %v", err)
	}
	cases := []struct {
		url      string
		wantKind ProxyKind
		wantAddr string
	}{
		{"http://example.com/a", ProxyHTTP, "http.proxy:8080"},
		{"https://example.com/a", ProxyHTTP, "https.proxy:8443"},
		{"http://db.internal/a", ProxyDirect, ""},
		{"http://localhost/a", ProxyDirect, ""},
	}
	for _, c := range cases {
		routes, err := r.ResolveProxy(t.Context(), mustURL(t, c.url))
		if err != nil {
			t.Fatalf("ResolveProxy(%q): %v", c.url, err)
		}
		if c.wantKind == ProxyDirect {
			if len(routes) != 0 {
				t.Errorf("%s: want direct, got %v", c.url, routes)
			}
			continue
		}
		if len(routes) != 1 || routes[0].Kind != c.wantKind || routes[0].Addr != c.wantAddr {
			t.Errorf("%s: got %v, want %s %s", c.url, routes, c.wantKind.Scheme(), c.wantAddr)
		}
	}
}

func TestManualResolverSOCKSOverrides(t *testing.T) {
	r, err := NewManualResolver(ManualProxies{
		HTTP:  "http.proxy:8080",
		SOCKS: "socks5://socks.proxy:1080",
	})
	if err != nil {
		t.Fatalf("NewManualResolver: %v", err)
	}
	routes, err := r.ResolveProxy(t.Context(), mustURL(t, "http://example.com/"))
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 || routes[0].Kind != ProxySOCKS5 {
		t.Fatalf("SOCKS should override HTTP: got %v", routes)
	}
}

func TestManualResolverRejectsBadSOCKS(t *testing.T) {
	if _, err := NewManualResolver(ManualProxies{SOCKS: "http://not-socks:1"}); err == nil {
		t.Error("a non-socks URL in SOCKS should be rejected")
	}
	if _, err := NewManualResolver(ManualProxies{HTTP: "socks5://x:1"}); err == nil {
		t.Error("a socks URL in HTTP should be rejected")
	}
}

func TestParseProxyURL(t *testing.T) {
	cases := []struct {
		raw      string
		wantKind ProxyKind
		wantAddr string
		wantUser string
	}{
		{"http://proxy:3128", ProxyHTTP, "proxy:3128", ""},
		{"proxy:3128", ProxyHTTP, "proxy:3128", ""},
		{"https://u:p@proxy", ProxyHTTPS, "proxy:443", "u"},
		{"socks5://proxy", ProxySOCKS5, "proxy:1080", ""},
		{"socks5h://proxy:9050", ProxySOCKS5, "proxy:9050", ""},
		{"socks4://proxy", ProxySOCKS4, "proxy:1080", ""},
		{"socks4a://proxy:1081", ProxySOCKS4A, "proxy:1081", ""},
	}
	for _, c := range cases {
		p, err := ParseProxyURL(c.raw)
		if err != nil {
			t.Errorf("ParseProxyURL(%q): %v", c.raw, err)
			continue
		}
		if p.Kind != c.wantKind || p.Addr != c.wantAddr || p.Username != c.wantUser {
			t.Errorf("ParseProxyURL(%q) = %+v, want kind %v addr %s user %s", c.raw, p, c.wantKind, c.wantAddr, c.wantUser)
		}
	}
}

func TestParseProxyURLRedactsPassword(t *testing.T) {
	_, err := ParseProxyURL("gopher://user:topsecret@proxy")
	if err == nil {
		t.Fatal("expected an error")
	}
	if contains(err.Error(), "topsecret") {
		t.Errorf("error leaks the password: %v", err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestEnvironmentResolver(t *testing.T) {
	env := map[string]string{
		"HTTP_PROXY":  "http://envproxy:8080",
		"HTTPS_PROXY": "http://envproxy:8443",
		"NO_PROXY":    "*.internal,localhost",
	}
	r := newEnvResolver(func(k string) string { return env[k] })
	routes, err := r.ResolveProxy(t.Context(), mustURL(t, "http://example.com/"))
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 || routes[0].Addr != "envproxy:8080" {
		t.Fatalf("http route = %v", routes)
	}
	routes, _ = r.ResolveProxy(t.Context(), mustURL(t, "http://x.internal/"))
	if len(routes) != 0 {
		t.Errorf("NO_PROXY host should be direct, got %v", routes)
	}
}
