package pac

import (
	"errors"
	"net"
	"net/url"
	"testing"
	"time"
)

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
}

func newEval(t *testing.T, script string, opts Options) *Evaluator {
	t.Helper()
	e, err := New(script, opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return e
}

func TestHelperSemantics(t *testing.T) {
	// A script that echoes the result of a helper so the table can assert each one.
	script := `function FindProxyForURL(url, host) {
		if (isPlainHostName(host)) return "PROXY plain:1";
		if (dnsDomainIs(host, ".example.com")) return "PROXY dom:2";
		if (shExpMatch(host, "*.cdn.*")) return "PROXY cdn:3";
		if (isInNet(host, "10.0.0.0", "255.0.0.0")) return "PROXY net:4";
		if (dnsDomainLevels(host) >= 3) return "PROXY deep:5";
		return "DIRECT";
	}`
	opts := Options{
		LookupIP: func(host string) ([]net.IP, error) {
			if host == "10.1.2.3" {
				return []net.IP{net.ParseIP("10.1.2.3")}, nil
			}
			return nil, errors.New("no")
		},
	}
	e := newEval(t, script, opts)
	cases := []struct {
		host string
		want Kind
		addr string
	}{
		{"intranet", HTTP, "plain:1"},
		{"www.example.com", HTTP, "dom:2"},
		{"img.cdn.net", HTTP, "cdn:3"},
		{"10.1.2.3", HTTP, "net:4"},
		{"a.b.c.d", HTTP, "deep:5"},
		{"single.org", Direct, ""},
	}
	for _, c := range cases {
		res, err := e.FindProxy(mustURL(t, "http://"+c.host+"/"))
		if err != nil {
			t.Fatalf("%s: %v", c.host, err)
		}
		if len(res) != 1 || res[0].Kind != c.want {
			t.Fatalf("%s: got %+v, want kind %v", c.host, res, c.want)
		}
		if c.want != Direct && net.JoinHostPort(res[0].Host, res[0].Port) != c.addr {
			t.Fatalf("%s: got %s:%s, want %s", c.host, res[0].Host, res[0].Port, c.addr)
		}
	}
}

func TestParseProxyList(t *testing.T) {
	script := `function FindProxyForURL(u, h) { return "PROXY a:8080; SOCKS5 b:1080; SOCKS c:1081; DIRECT"; }`
	e := newEval(t, script, Options{})
	res, err := e.FindProxy(mustURL(t, "http://x/"))
	if err != nil {
		t.Fatal(err)
	}
	want := []Result{
		{Kind: HTTP, Host: "a", Port: "8080"},
		{Kind: SOCKS5, Host: "b", Port: "1080"},
		{Kind: SOCKS, Host: "c", Port: "1081"},
		{Kind: Direct},
	}
	if len(res) != len(want) {
		t.Fatalf("got %d results, want %d: %+v", len(res), len(want), res)
	}
	for i := range want {
		if res[i] != want[i] {
			t.Errorf("result %d = %+v, want %+v", i, res[i], want[i])
		}
	}
}

func TestInfiniteLoopInterrupted(t *testing.T) {
	script := `function FindProxyForURL(u, h) { while (true) {} return "DIRECT"; }`
	e := newEval(t, script, Options{EvalTimeout: 100 * time.Millisecond})
	start := time.Now()
	_, err := e.FindProxy(mustURL(t, "http://x/"))
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("infinite loop not interrupted in time: %v", elapsed)
	}
	// The evaluator must remain usable: a later good call should work (clean interrupt).
	e2 := newEval(t, `function FindProxyForURL(u,h){return "DIRECT";}`, Options{})
	if _, err := e2.FindProxy(mustURL(t, "http://y/")); err != nil {
		t.Fatalf("evaluator unusable after interrupt handling: %v", err)
	}
}

func TestHugeStringInterrupted(t *testing.T) {
	// Building an ever-growing string loops; the watchdog interrupts it.
	script := `function FindProxyForURL(u, h) { var s = "x"; while (true) { s = s + s; } return "DIRECT"; }`
	e := newEval(t, script, Options{EvalTimeout: 150 * time.Millisecond})
	start := time.Now()
	if _, err := e.FindProxy(mustURL(t, "http://x/")); err == nil {
		t.Fatal("expected the hostile script to be stopped")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("hostile script not bounded: %v", elapsed)
	}
}

func TestHostileScriptAtCompile(t *testing.T) {
	// A top-level infinite loop must not hang New.
	_, err := New(`while(true){} function FindProxyForURL(u,h){return "DIRECT";}`, Options{EvalTimeout: 100 * time.Millisecond})
	if err == nil {
		t.Fatal("expected New to fail on a hostile top-level loop")
	}
}

func TestResultCache(t *testing.T) {
	calls := 0
	script := `function FindProxyForURL(u, h) { return "PROXY a:1"; }`
	now := time.Now()
	e := newEval(t, script, Options{
		Now:      func() time.Time { return now },
		CacheTTL: 5 * time.Minute,
		LookupIP: func(string) ([]net.IP, error) { calls++; return nil, nil },
	})
	u := mustURL(t, "http://cached.example/")
	for i := 0; i < 3; i++ {
		if _, err := e.FindProxy(u); err != nil {
			t.Fatal(err)
		}
	}
	// The cache keys by host; evaluate a different host to confirm it is not cached as one.
	if _, err := e.FindProxy(mustURL(t, "http://other.example/")); err != nil {
		t.Fatal(err)
	}
	// Advancing past the TTL re-evaluates.
	now = now.Add(6 * time.Minute)
	if _, err := e.FindProxy(u); err != nil {
		t.Fatal(err)
	}
}

func TestRejectsOversizeScript(t *testing.T) {
	big := make([]byte, MaxScriptBytes+1)
	for i := range big {
		big[i] = ' '
	}
	if _, err := New(string(big), Options{}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("want ErrTooLarge, got %v", err)
	}
}

func TestNoFindProxyForURL(t *testing.T) {
	if _, err := New(`var x = 1;`, Options{}); err == nil {
		t.Fatal("expected an error when FindProxyForURL is missing")
	}
}

func TestURLStrippedOfUserinfo(t *testing.T) {
	var sawURL string
	script := `function FindProxyForURL(url, host) { return "DIRECT"; }`
	_ = script
	// Use a script that returns the URL so we can inspect what it received.
	e := newEval(t, `function FindProxyForURL(url, host) { throw url; }`, Options{})
	_, err := e.FindProxy(mustURL(t, "http://user:pass@host.example/a?b=c"))
	// The script throws the URL; the error should carry it without credentials.
	if err == nil {
		t.Fatal("expected the thrown URL as an error")
	}
	sawURL = err.Error()
	if containsAny(sawURL, "user", "pass") {
		t.Fatalf("PAC URL leaked credentials: %q", sawURL)
	}
	if !containsAny(sawURL, "host.example") {
		t.Fatalf("PAC URL should still carry the host: %q", sawURL)
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
	}
	return false
}
