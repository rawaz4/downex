package sysproxy

import (
	"net/url"
	"slices"
	"testing"

	"downex/internal/transport"
)

// routesOf resolves target with r and fails the test on an error.
func routesOf(t *testing.T, r transport.ProxyResolver, target string) []transport.Proxy {
	t.Helper()
	routes, err := r.ResolveProxy(t.Context(), mustURLSP(t, target))
	if err != nil {
		t.Fatalf("ResolveProxy(%s): %v", target, err)
	}
	return routes
}

// wantRoute checks that target resolves to exactly want (nil: direct).
func wantRoute(t *testing.T, r transport.ProxyResolver, target string, want *transport.Proxy) {
	t.Helper()
	got := routesOf(t, r, target)
	switch {
	case want == nil && len(got) != 0:
		t.Errorf("%s -> %v, want direct", target, got)
	case want != nil && (len(got) != 1 || got[0] != *want):
		t.Errorf("%s -> %#v, want [%#v]", target, got, *want)
	}
}

// TestScutilDefaultExceptions: macOS's default exceptions ("*.local, 169.254/16") use a
// short CIDR form; the resolver still builds, and the link-local range goes direct.
func TestScutilDefaultExceptions(t *testing.T) {
	out := []byte(`<dictionary> {
  ExceptionsList : <array> {
    0 : *.local
    1 : 169.254/16
    2 : 10/8
    3 : 192.168.7/24
  }
  ExcludeSimpleHostnames : 1
  FTPPassive : 1
  HTTPEnable : 1
  HTTPPort : 8080
  HTTPProxy : proxy.example.com
  HTTPSEnable : 1
  HTTPSPort : 8080
  HTTPSProxy : proxy.example.com
}`)
	s := ParseScutil(out)
	wantBypass := []string{"<local>", "*.local", "169.254.0.0/16", "10.0.0.0/8", "192.168.7.0/24"}
	if !slices.Equal(s.Bypass, wantBypass) {
		t.Errorf("Bypass = %v, want %v", s.Bypass, wantBypass)
	}
	r, err := NewStaticResolver(s)
	if err != nil || r == nil {
		t.Fatalf("NewStaticResolver = %v, %v; want a resolver", r, err)
	}
	proxy := &transport.Proxy{Kind: transport.ProxyHTTP, Addr: "proxy.example.com:8080"}
	wantRoute(t, r, "http://example.com/a", proxy)
	wantRoute(t, r, "https://example.com/a", proxy)
	for _, direct := range []string{"http://169.254.10.20/", "http://10.1.2.3/", "http://192.168.7.9/",
		"http://printer.local/", "http://intranet/"} {
		wantRoute(t, r, direct, nil)
	}
	wantRoute(t, r, "http://192.168.8.9/", proxy)
}

// TestStaticResolverSkipsUnusableBypass: an operating-system bypass entry that Downex
// cannot read is left out (and reported) instead of disabling the whole system proxy.
func TestStaticResolverSkipsUnusableBypass(t *testing.T) {
	s := Settings{HTTPProxy: "proxy.test:3128", Bypass: []string{"*.ok.test", "bad entry/x", "[::1", "a@b"}}
	r, err := NewStaticResolver(s)
	if err != nil || r == nil {
		t.Fatalf("NewStaticResolver = %v, %v; want a resolver", r, err)
	}
	wantRoute(t, r, "http://x.ok.test/", nil)
	wantRoute(t, r, "http://example.com/", &transport.Proxy{Kind: transport.ProxyHTTP, Addr: "proxy.test:3128"})
	if got, want := s.UnusableBypass(), []string{"bad entry/x", "[::1", "a@b"}; !slices.Equal(got, want) {
		t.Errorf("UnusableBypass = %q, want %q", got, want)
	}
}

// TestEnvSettingsKeepProxyURLs: the environment's proxy URLs keep their scheme and
// credentials, and NO_PROXY domains also cover their subdomains (as curl, wget and Go
// read them).
func TestEnvSettingsKeepProxyURLs(t *testing.T) {
	env := map[string]string{
		"HTTP_PROXY":  "http://alice:p%40ss@envproxy:3128",
		"HTTPS_PROXY": "https://tlsproxy:443",
		"NO_PROXY":    "example.com, .corp.test,10.0.0.0/8,192.0.2.7,files.test:8080",
	}
	r, err := NewStaticResolver(EnvSettings(func(k string) string { return env[k] }))
	if err != nil {
		t.Fatal(err)
	}
	wantRoute(t, r, "http://a.test/", &transport.Proxy{Kind: transport.ProxyHTTP, Addr: "envproxy:3128", Username: "alice", Password: "p@ss"})
	wantRoute(t, r, "https://a.test/", &transport.Proxy{Kind: transport.ProxyHTTPS, Addr: "tlsproxy:443"})
	for _, direct := range []string{"http://example.com/", "http://dl.example.com/", "https://a.b.example.com/",
		"http://x.corp.test/", "http://10.9.8.7/", "http://192.0.2.7/", "http://files.test:8080/", "http://dl.files.test:8080/"} {
		wantRoute(t, r, direct, nil)
	}
	for _, proxied := range []string{"http://notexample.com/", "http://files.test/", "http://192.0.2.8/"} {
		if got := routesOf(t, r, proxied); len(got) != 1 {
			t.Errorf("%s -> %v, want the proxy", proxied, got)
		}
	}
}

// TestSystemSOCKSIsAFallback: in the system configuration a SOCKS proxy serves only the
// schemes that have no proxy of their own (WinINET's "socks=", macOS, ALL_PROXY), and
// WinINET's "socks=" speaks SOCKS4.
func TestSystemSOCKSIsAFallback(t *testing.T) {
	http := &transport.Proxy{Kind: transport.ProxyHTTP, Addr: "proxy:8080"}

	var win Settings
	applyWindowsProxy(&win, "http=proxy:8080;https=proxy:8080;ftp=proxy:8080;socks=socks:1080")
	if win.SOCKSProxy != "socks4://socks:1080" {
		t.Errorf("WinINET socks= -> %q, want socks4://socks:1080", win.SOCKSProxy)
	}
	r, err := NewStaticResolver(win)
	if err != nil {
		t.Fatal(err)
	}
	wantRoute(t, r, "http://a.test/", http)
	wantRoute(t, r, "https://a.test/", http)

	var socksOnly Settings
	applyWindowsProxy(&socksOnly, "http=proxy:8080;socks=socks:1080")
	r, err = NewStaticResolver(socksOnly)
	if err != nil {
		t.Fatal(err)
	}
	wantRoute(t, r, "http://a.test/", http)
	wantRoute(t, r, "https://a.test/", &transport.Proxy{Kind: transport.ProxySOCKS4, Addr: "socks:1080"})

	// macOS: a bare SOCKS host is SOCKS5, used where no HTTP(S) proxy is set.
	r, err = NewStaticResolver(Settings{HTTPProxy: "proxy:8080", SOCKSProxy: "socks:1080"})
	if err != nil {
		t.Fatal(err)
	}
	wantRoute(t, r, "http://a.test/", http)
	wantRoute(t, r, "https://a.test/", &transport.Proxy{Kind: transport.ProxySOCKS5, Addr: "socks:1080"})

	// Linux: HTTP_PROXY wins over ALL_PROXY for http; NO_PROXY applies to both.
	env := map[string]string{
		"HTTP_PROXY": "http://proxy:8080",
		"ALL_PROXY":  "socks5://socks:1080",
		"NO_PROXY":   "internal.test",
	}
	r, err = NewStaticResolver(EnvSettings(func(k string) string { return env[k] }))
	if err != nil {
		t.Fatal(err)
	}
	wantRoute(t, r, "http://a.test/", http)
	wantRoute(t, r, "https://a.test/", &transport.Proxy{Kind: transport.ProxySOCKS5, Addr: "socks:1080"})
	wantRoute(t, r, "http://dl.internal.test/", nil)
	wantRoute(t, r, "https://internal.test/", nil)

	// The per-URL WinHTTP answer in WinINET form follows the same rule.
	https, _ := url.Parse("https://a.test/")
	if got := proxiesFor(socksOnly, https); len(got) != 1 || got[0].Kind != transport.ProxySOCKS4 || got[0].Addr != "socks:1080" {
		t.Errorf("proxiesFor(https) = %v, want the SOCKS4 fallback", got)
	}
}
