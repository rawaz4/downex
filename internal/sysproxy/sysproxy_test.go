package sysproxy

import (
	"net/url"
	"slices"
	"testing"
)

func mustURLSP(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
}

func TestParseScutilManual(t *testing.T) {
	out := []byte(`<dictionary> {
  HTTPEnable : 1
  HTTPProxy : proxy.example.com
  HTTPPort : 8080
  HTTPSEnable : 1
  HTTPSProxy : proxy.example.com
  HTTPSPort : 8443
  SOCKSEnable : 0
  ProxyAutoConfigEnable : 0
  ProxyAutoDiscoveryEnable : 0
  ExcludeSimpleHostnames : 1
  ExceptionsList : <array> {
    0 : *.local
    1 : 169.254/16
  }
}`)
	s := ParseScutil(out)
	if s.HTTPProxy != "proxy.example.com:8080" {
		t.Errorf("HTTPProxy = %q", s.HTTPProxy)
	}
	if s.HTTPSProxy != "proxy.example.com:8443" {
		t.Errorf("HTTPSProxy = %q", s.HTTPSProxy)
	}
	if s.SOCKSProxy != "" {
		t.Errorf("SOCKSProxy = %q, want empty (disabled)", s.SOCKSProxy)
	}
	wantBypass := []string{"<local>", "*.local", "169.254.0.0/16"}
	if !slices.Equal(s.Bypass, wantBypass) {
		t.Errorf("Bypass = %v, want %v", s.Bypass, wantBypass)
	}
}

func TestParseScutilPAC(t *testing.T) {
	out := []byte(`<dictionary> {
  ProxyAutoConfigEnable : 1
  ProxyAutoConfigURLString : http://wpad.example/proxy.pac
  ProxyAutoDiscoveryEnable : 1
}`)
	s := ParseScutil(out)
	if !s.AutoDetect {
		t.Error("AutoDetect should be true")
	}
	if s.PACURL != "http://wpad.example/proxy.pac" {
		t.Errorf("PACURL = %q", s.PACURL)
	}
}

func TestParseScutilDisabledPACIgnoresURL(t *testing.T) {
	out := []byte(`<dictionary> {
  ProxyAutoConfigEnable : 0
  ProxyAutoConfigURLString : http://wpad.example/proxy.pac
}`)
	s := ParseScutil(out)
	if s.PACURL != "" {
		t.Errorf("disabled PAC should yield no PACURL, got %q", s.PACURL)
	}
}

func TestParseScutilSOCKS(t *testing.T) {
	out := []byte(`<dictionary> {
  SOCKSEnable : 1
  SOCKSProxy : socks.example
  SOCKSPort : 1080
}`)
	s := ParseScutil(out)
	if s.SOCKSProxy != "socks.example:1080" {
		t.Errorf("SOCKSProxy = %q", s.SOCKSProxy)
	}
}

func TestEnvSettings(t *testing.T) {
	env := map[string]string{
		"HTTP_PROXY":  "http://envproxy:8080",
		"HTTPS_PROXY": "envproxy:8443",
		"ALL_PROXY":   "socks5://socks:1080",
		"NO_PROXY":    "localhost,*.internal",
	}
	s := EnvSettings(func(k string) string { return env[k] })
	if s.HTTPProxy != "http://envproxy:8080" {
		t.Errorf("HTTPProxy = %q", s.HTTPProxy)
	}
	if s.HTTPSProxy != "envproxy:8443" {
		t.Errorf("HTTPSProxy = %q", s.HTTPSProxy)
	}
	if s.SOCKSProxy != "socks5://socks:1080" {
		t.Errorf("SOCKSProxy = %q", s.SOCKSProxy)
	}
	wantBypass := []string{"localhost", "*.localhost", "*.internal"}
	if !slices.Equal(s.Bypass, wantBypass) {
		t.Errorf("Bypass = %v, want %v", s.Bypass, wantBypass)
	}
}

func TestNewStaticResolverSelection(t *testing.T) {
	s := Settings{HTTPProxy: "p:8080", HTTPSProxy: "p:8443", Bypass: []string{"*.internal"}}
	r, err := NewStaticResolver(s)
	if err != nil {
		t.Fatal(err)
	}
	routes, err := r.ResolveProxy(t.Context(), mustURLSP(t, "http://example.com/"))
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 || routes[0].Addr != "p:8080" {
		t.Fatalf("http route = %v", routes)
	}
	routes, _ = r.ResolveProxy(t.Context(), mustURLSP(t, "http://db.internal/"))
	if len(routes) != 0 {
		t.Errorf("bypassed host should be direct, got %v", routes)
	}
}

func TestEmptySettingsDirect(t *testing.T) {
	if !(Settings{}).Empty() {
		t.Error("zero Settings should be Empty")
	}
}
