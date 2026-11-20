package polite

import (
	"net/netip"
	"testing"
	"time"
)

func TestEligible(t *testing.T) {
	for _, tc := range []struct {
		addr string
		want bool
	}{
		{"8.8.8.8", true},
		{"203.0.113.7", true},
		{"2606:4700::1111", true},
		{"::ffff:8.8.8.8", true}, // judged as its IPv4 address
		{"127.0.0.1", false},
		{"::1", false},
		{"10.1.2.3", false},
		{"172.16.0.1", false},
		{"192.168.1.1", false},
		{"::ffff:192.168.1.1", false}, // a mapped private address is still private
		{"fd00::1", false},            // ULA
		{"169.254.1.1", false},
		{"fe80::1", false},
		{"fe80::1%eth0", false},
		{"224.0.0.1", false},
		{"ff02::1", false},
		{"0.0.0.0", false},
		{"::", false},
		{"100.64.0.1", false}, // shared address space / TUN
		{"100.127.255.254", false},
		{"198.18.0.1", false}, // fake-IP proxies
		{"198.19.255.254", false},
		{"255.255.255.255", false},
	} {
		if got := Eligible(netip.MustParseAddr(tc.addr)); got != tc.want {
			t.Errorf("Eligible(%s) = %v, want %v", tc.addr, got, tc.want)
		}
	}
	if Eligible(netip.Addr{}) {
		t.Error("the zero address is eligible")
	}
}

func TestSelection(t *testing.T) {
	tab := newAnchors()
	p := func(a string, conns int, first time.Duration) Peer {
		return Peer{Addr: netip.MustParseAddrPort(a), Conns: conns, First: t0.Add(first)}
	}
	tab.update(t0, []Peer{
		p("203.0.113.1:443", 2, 0),
		p("203.0.113.2:443", 8, time.Second),
		p("203.0.113.3:443", 2, -time.Second), // same connections as .1, seen earlier
		p("203.0.113.4:443", 1, -time.Minute),
		p("192.168.1.10:443", 30, 0), // the LAN: never
		p("203.0.113.2:80", 1, 0),    // same address, another port: merged
	})
	var got []string
	for _, a := range tab.selected {
		got = append(got, a.addr.String())
	}
	want := []string{"203.0.113.2", "203.0.113.3", "203.0.113.1"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("selected %v, want %v", got, want)
	}
	a := tab.m[netip.MustParseAddr("203.0.113.2")]
	if a.conns != 9 || a.port != 443 {
		t.Fatalf("merged anchor: %d connections, port %d", a.conns, a.port)
	}
	if _, ok := tab.m[netip.MustParseAddr("192.168.1.10")]; ok {
		t.Fatal("a LAN address became an anchor")
	}
	// An anchor that drops out keeps its history for 10 minutes.
	tab.update(t0.Add(time.Minute), nil)
	if len(tab.selected) != 0 || len(tab.m) != 4 {
		t.Fatalf("after the connections closed: %d selected, %d kept", len(tab.selected), len(tab.m))
	}
	tab.update(t0.Add(AnchorKeep), nil)
	if len(tab.m) != 4 {
		t.Fatalf("forgotten at exactly %v", AnchorKeep)
	}
	tab.update(t0.Add(AnchorKeep+time.Second), nil)
	if len(tab.m) != 0 {
		t.Fatalf("%d anchors kept after %v", len(tab.m), AnchorKeep)
	}
	// Loopback only where allowed (end-to-end tests).
	tab.update(t0, []Peer{p("127.0.0.1:8080", 1, 0)})
	if len(tab.selected) != 0 {
		t.Fatal("loopback selected")
	}
	tab.loopback = true
	tab.update(t0, []Peer{p("127.0.0.1:8080", 1, 0)})
	if len(tab.selected) != 1 {
		t.Fatal("loopback not selected where allowed")
	}
}

// The base is the minimum over 10 one-minute buckets including the current one: a lower
// RTT older than 10 minutes is forgotten (a path change resolves within 10 minutes).
func TestBaseRollover(t *testing.T) {
	a := &anchor{}
	a.add(sample{sent: t0, rtt: 20 * time.Millisecond})
	for m := 1; m <= 9; m++ {
		a.add(sample{sent: t0.Add(time.Duration(m) * time.Minute), rtt: 60 * time.Millisecond})
	}
	if b, _ := a.base(t0.Add(9 * time.Minute)); b != 20*time.Millisecond {
		t.Fatalf("base %v at 9 min", b)
	}
	a.add(sample{sent: t0.Add(10 * time.Minute), rtt: 60 * time.Millisecond})
	if b, _ := a.base(t0.Add(10 * time.Minute)); b != 60*time.Millisecond {
		t.Fatalf("base %v at 10 min, want the old minimum forgotten", b)
	}
	if _, ok := a.base(t0.Add(30 * time.Minute)); ok {
		t.Fatal("a base without any sample in 10 minutes")
	}
	// Out-of-order results keep the samples sorted.
	b := &anchor{}
	b.add(sample{sent: t0.Add(time.Second), rtt: 1})
	b.add(sample{sent: t0, rtt: 2})
	if !b.samples[0].sent.Equal(t0) {
		t.Fatal("samples not sorted by send time")
	}
}
