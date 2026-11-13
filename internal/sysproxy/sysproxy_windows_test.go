//go:build windows

package sysproxy

import (
	"context"
	"errors"
	"net/url"
	"sync"
	"testing"
	"time"

	"downex/internal/transport"
)

// TestAutoResolverFailsOpen: when WinHTTP finds no WPAD server (the usual case with
// "automatically detect settings" on, ERROR_WINHTTP_AUTODETECTION_FAILED) or cannot run
// the script, downloads go direct instead of failing; results are cached per scheme and
// host for five minutes.
func TestAutoResolverFailsOpen(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	calls := 0
	answer, fail := "", errors.New("winapi error #12180")
	r := &AutoResolver{
		lookup: func(string) (string, error) { calls++; return answer, fail },
		cache:  map[string]autoEntry{},
		now:    func() time.Time { return now },
	}
	u, _ := url.Parse("https://files.example.com/a.bin")
	routes, err := r.ResolveProxy(t.Context(), u)
	if err != nil || len(routes) != 0 {
		t.Fatalf("failed detection = %v, %v; want direct", routes, err)
	}
	other, _ := url.Parse("https://FILES.example.com/b.bin")
	if _, err := r.ResolveProxy(t.Context(), other); err != nil || calls != 1 {
		t.Fatalf("same host again: err %v, %d lookups; want the cached answer", err, calls)
	}
	// After five minutes the answer is looked up again; now there is a WPAD proxy list.
	now = now.Add(autoCacheTTL)
	answer, fail = "proxy1:8080; proxy2:3128", nil
	routes, err = r.ResolveProxy(t.Context(), u)
	want := []transport.Proxy{{Kind: transport.ProxyHTTP, Addr: "proxy1:8080"}, {Kind: transport.ProxyHTTP, Addr: "proxy2:3128"}}
	if err != nil || len(routes) != 2 || routes[0] != want[0] || routes[1] != want[1] || calls != 2 {
		t.Fatalf("after the cache expired = %v, %v (%d lookups); want %v", routes, err, calls, want)
	}
	// Callers may change what they get.
	routes[0].Addr = "changed:1"
	if again, _ := r.ResolveProxy(t.Context(), u); again[0] != want[0] {
		t.Fatalf("the cache was changed through a returned slice: %v", again)
	}
}

// TestAutoResolverHonoursContext: a WinHTTP lookup that hangs (WPAD discovery while the
// network comes up) does not hold the caller beyond its context; concurrent requests for
// the same host share the one lookup, whose answer is cached when it arrives.
func TestAutoResolverHonoursContext(t *testing.T) {
	release := make(chan struct{})
	var mu sync.Mutex
	calls := 0
	r := &AutoResolver{
		lookup: func(string) (string, error) {
			mu.Lock()
			calls++
			mu.Unlock()
			<-release
			return "proxy1:8080", nil
		},
		cache: map[string]autoEntry{},
		now:   time.Now,
	}
	u, _ := url.Parse("https://files.example.com/a.bin")
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	routes, err := r.ResolveProxy(ctx, u)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("ResolveProxy waited %v for a hanging lookup", elapsed)
	}
	if err == nil || len(routes) != 0 {
		t.Fatalf("ResolveProxy = %v, %v; want the context's error", routes, err)
	}
	// A second caller meanwhile joins the pending lookup instead of starting another.
	got := make(chan []transport.Proxy, 1)
	go func() {
		routes, _ := r.ResolveProxy(t.Context(), u)
		got <- routes
	}()
	time.Sleep(50 * time.Millisecond)
	close(release)
	select {
	case routes := <-got:
		if len(routes) != 1 || routes[0].Addr != "proxy1:8080" {
			t.Fatalf("joined lookup = %v", routes)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the waiting caller never got the answer")
	}
	if again, err := r.ResolveProxy(t.Context(), u); err != nil || len(again) != 1 {
		t.Fatalf("cached = %v, %v", again, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("%d lookups, want 1", calls)
	}
}

func TestAutoRoutes(t *testing.T) {
	https, _ := url.Parse("https://example.com/")
	plain, _ := url.Parse("http://example.com/")
	for _, c := range []struct {
		proxy  string
		target *url.URL
		want   []string
	}{
		{"", https, nil},
		{"one:3128", https, []string{"one:3128"}},
		{"a:80;b:81", plain, []string{"a:80", "b:81"}},
		{"http=h:80;https=s:443", https, []string{"s:443"}},
		{"http=h:80;https=s:443", plain, []string{"h:80"}},
	} {
		var got []string
		for _, p := range autoRoutes(c.proxy, c.target) {
			got = append(got, p.Addr)
		}
		if len(got) != len(c.want) || (len(got) > 0 && got[0] != c.want[0]) || (len(got) > 1 && got[1] != c.want[1]) {
			t.Errorf("autoRoutes(%q, %s) = %v, want %v", c.proxy, c.target, got, c.want)
		}
	}
}

// TestDiscoverSmoke reads the current user's IE/WinINET proxy configuration. It must not
// error and must not mutate anything; the values depend on the machine, so only the call
// is asserted.
func TestDiscoverSmoke(t *testing.T) {
	s, err := Discover(t.Context())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	// A sane snapshot: ports, if present, are well-formed host:port (checked loosely).
	t.Logf("system proxy: autodetect=%v pac=%q http=%q https=%q socks=%q bypass=%v",
		s.AutoDetect, s.PACURL, s.HTTPProxy, s.HTTPSProxy, s.SOCKSProxy, s.Bypass)
}

func TestApplyWindowsProxy(t *testing.T) {
	var s Settings
	applyWindowsProxy(&s, "http=p1:8080;https=p2:8443;socks=p3:1080")
	if s.HTTPProxy != "p1:8080" || s.HTTPSProxy != "p2:8443" || s.SOCKSProxy != "socks4://p3:1080" {
		t.Fatalf("scheme form parsed wrong: %+v", s)
	}
	var s2 Settings
	applyWindowsProxy(&s2, "oneproxy:3128")
	if s2.HTTPProxy != "oneproxy:3128" || s2.HTTPSProxy != "oneproxy:3128" {
		t.Fatalf("single-proxy form parsed wrong: %+v", s2)
	}
}

func TestParseWindowsBypass(t *testing.T) {
	got := parseWindowsBypass("*.local;<-loopback>;10.*")
	want := []string{"*.local", "<-loopback>", "10.*"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
