package transport

import (
	"errors"
	"net/netip"
	"strings"
	"testing"
)

// A PublicOnly slot refuses a host name that resolves to this computer ("localhost" here,
// as a public name pointing at 127.0.0.1 would), at the dial: the server never sees the
// request. Other slots are unchanged.
func TestPublicOnlyRefusesLocalAddresses(t *testing.T) {
	ctx := testCtx(t)
	srv, _ := startFamilyServer(t, "127.0.0.1:0", "local")
	port := srv.URL[strings.LastIndex(srv.URL, ":")+1:]
	base := directFactory(t)
	byName := "http://localhost:" + port + "/a"
	if got, err := fetchText(ctx, base, byName); err != nil || got != "local" {
		t.Fatalf("plain slot = %q, %v", got, err)
	}
	before := srv.hits.Load()
	pub := PublicOnly(base)
	for _, u := range []string{byName, srv.URL + "/b"} {
		if _, err := fetchText(ctx, pub, u); !errors.Is(err, ErrLocalAddress) {
			t.Fatalf("%s: got %v, want ErrLocalAddress", u, err)
		}
	}
	if srv.hits.Load() != before {
		t.Fatalf("the local server saw %d requests from a public-only slot", srv.hits.Load()-before)
	}
}

func TestLocalAddress(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "::1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.1.1", "fe80::1", "fc00::1", "0.0.0.0", "::", "224.0.0.1", "::ffff:127.0.0.1", "::ffff:192.168.0.1",
		// Shared (CGNAT, tailnets), benchmarking, IETF, reserved and broadcast ranges.
		"100.64.0.1", "100.100.100.100", "100.127.255.255", "198.18.0.1", "198.19.255.1", "192.0.0.8", "0.1.2.3", "240.0.0.1", "255.255.255.255",
		// IPv6 site-local, Teredo and documentation ranges.
		"fec0::1", "feff::1", "2001::1", "2001:db8::1",
		// IPv4 addresses embedded in IPv6: NAT64, local-use NAT64, 6to4 and IPv4-compatible.
		"64:ff9b::7f00:1", "64:ff9b::c0a8:101", "64:ff9b:1::a00:1", "2002:c0a8:101::1", "2002:7f00:1::", "::127.0.0.1", "::192.168.1.1"} {
		if !LocalAddress(netip.MustParseAddr(s)) {
			t.Errorf("%s is not local", s)
		}
	}
	for _, s := range []string{"8.8.8.8", "2606:4700::1111", "::ffff:8.8.8.8", "100.63.255.255", "100.128.0.1", "198.20.0.1", "64:ff9b::808:808", "2002:808:808::1", "2001:4860:4860::8888"} {
		if LocalAddress(netip.MustParseAddr(s)) {
			t.Errorf("%s is local", s)
		}
	}
}
