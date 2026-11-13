package pac

import (
	"errors"
	"net"
	"sync"
	"testing"
)

// TestDNSResolveIPLiteral: dnsResolve returns an IPv4 literal as it is (as browsers do),
// so the common isInNet(dnsResolve(host), ...) idiom sends intranet addresses DIRECT;
// an empty or unresolvable argument makes isInNet false without asking DNS for "".
func TestDNSResolveIPLiteral(t *testing.T) {
	script := `function FindProxyForURL(url, host) {
		var ip = dnsResolve(host);
		if (isInNet(ip, "10.0.0.0", "255.0.0.0")) return "DIRECT";
		return "PROXY proxy.corp:8080";
	}`
	var mu sync.Mutex
	var lookups []string
	e := newEval(t, script, Options{LookupIP: func(host string) ([]net.IP, error) {
		mu.Lock()
		lookups = append(lookups, host)
		mu.Unlock()
		if host == "intranet.corp" {
			return []net.IP{net.ParseIP("10.9.9.9")}, nil
		}
		return nil, errors.New("no such host")
	}})
	for _, c := range []struct {
		url  string
		want Kind
	}{
		{"http://intranet.corp/", Direct},
		{"http://10.1.2.3/", Direct},
		{"http://[::ffff:10.1.2.4]/", Direct},
		{"http://192.0.2.1/", HTTP},
		{"http://unknown.corp/", HTTP},
	} {
		res, err := e.FindProxy(mustURL(t, c.url))
		if err != nil {
			t.Fatalf("%s: %v", c.url, err)
		}
		if len(res) != 1 || res[0].Kind != c.want {
			t.Errorf("%s -> %+v, want kind %v", c.url, res, c.want)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for _, h := range lookups {
		if h == "" || net.ParseIP(h) != nil {
			t.Errorf("DNS was asked for %q", h)
		}
	}
	if len(lookups) != 2 {
		t.Errorf("lookups = %q, want one per host name (intranet.corp, unknown.corp)", lookups)
	}
}
