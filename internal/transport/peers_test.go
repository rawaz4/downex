package transport

import (
	"bytes"
	"net"
	"net/http/httptest"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"downex/internal/model"
)

// newPeerSlot returns a slot of a factory trusting the given servers, and the factory's
// table.
func newPeerSlot(t *testing.T, opts HTTPOptions, servers ...*httptest.Server) (*HTTPFactory, Fetcher) {
	t.Helper()
	f, err := newHTTPFactory(opts, trustPool(servers...))
	if err != nil {
		t.Fatal(err)
	}
	s := f.NewSlot()
	t.Cleanup(s.Close)
	return f, s
}

func fetchAll(t *testing.T, s Fetcher, url string, want []byte) {
	t.Helper()
	resp, err := s.Fetch(testCtx(t), RangeRequest{Request: model.Request{URL: url}, End: -1})
	if err != nil {
		t.Fatal(err)
	}
	if got := readBody(t, resp); !bytes.Equal(got, want) {
		t.Fatal("body mismatch")
	}
}

// waitPeers polls the table until cond holds (a connection is released when net/http
// closes it, which may lag the call that asked for it).
func waitPeers(t *testing.T, tab *PeerTable, what string, cond func([]Peer) bool) []Peer {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		ps := tab.Snapshot()
		if cond(ps) {
			return ps
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: table %+v", what, ps)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A direct connection is recorded under the origin's address with its host name, and
// released when the connection closes.
func TestPeersDirect(t *testing.T) {
	cs := newContentServer(10_000)
	ts := startServer(t, kindHTTP1, cs)
	f, slot := newPeerSlot(t, DefaultHTTPOptions())
	var news atomic.Int32
	f.Peers().OnNew(func() { news.Add(1) })
	fetchAll(t, slot, ts.URL, cs.content)
	want := netip.MustParseAddrPort(ts.Listener.Addr().String())
	ps := f.Peers().Snapshot()
	if len(ps) != 1 || ps[0].Addr != want || ps[0].Conns != 1 || len(ps[0].Hosts) != 1 || ps[0].Hosts[0] != "127.0.0.1" {
		t.Fatalf("peers %+v, want %v with 1 connection to 127.0.0.1", ps, want)
	}
	if ps[0].First.IsZero() {
		t.Fatal("no first-seen time")
	}
	// The pooled connection is reused: still one, and no second "new" call.
	fetchAll(t, slot, ts.URL, cs.content)
	if ps := f.Peers().Snapshot(); len(ps) != 1 || ps[0].Conns != 1 || news.Load() != 1 {
		t.Fatalf("after a reuse: %+v, %d new", ps, news.Load())
	}
	slot.Close()
	waitPeers(t, f.Peers(), "after Close", func(ps []Peer) bool { return len(ps) == 0 })
}

// Through a SOCKS proxy or a forwarding HTTP proxy the peer is the proxy's address.
func TestPeersThroughProxies(t *testing.T) {
	cs := newContentServer(5_000)
	ts := startServer(t, kindHTTP1, cs)

	socks := startSOCKS5(t, "", "")
	opts := DefaultHTTPOptions()
	opts.Proxy = "socks5://" + socks.addr()
	f, slot := newPeerSlot(t, opts)
	fetchAll(t, slot, ts.URL, cs.content)
	ps := f.Peers().Snapshot()
	if len(ps) != 1 || ps[0].Addr != netip.MustParseAddrPort(socks.addr()) || ps[0].Hosts[0] != "127.0.0.1" {
		t.Fatalf("through SOCKS5: %+v, want the proxy %s", ps, socks.addr())
	}

	// A forwarded plain-http request (the test proxy answers it itself) and a CONNECT
	// tunnel to an https origin.
	proxy := startNetProxy(t, nil, nil)
	tlsOrigin := startServer(t, kindHTTP1TLS, cs)
	opts = DefaultHTTPOptions()
	opts.Proxy = "http://" + proxy.addr()
	f, slot = newPeerSlot(t, opts, tlsOrigin)
	fetchAll(t, slot, ts.URL, []byte("hello"))
	fetchAll(t, slot, tlsOrigin.URL, cs.content)
	ps = f.Peers().Snapshot()
	if len(ps) != 1 || ps[0].Addr != netip.MustParseAddrPort(proxy.addr()) || ps[0].Conns != 2 {
		t.Fatalf("through an HTTP proxy: %+v, want 2 connections to the proxy %s", ps, proxy.addr())
	}
	if reqs := proxy.requests(); len(reqs) != 2 || reqs[1].method != "CONNECT" {
		t.Fatalf("proxy saw %+v", reqs)
	}
}

// Many slots dialling at once: every connection counted, all released (run with -race).
func TestPeersConcurrent(t *testing.T) {
	cs := newContentServer(50_000)
	ts := startServer(t, kindHTTP1, cs)
	f, err := newHTTPFactory(DefaultHTTPOptions(), nil)
	if err != nil {
		t.Fatal(err)
	}
	const n = 16
	slots := make([]Fetcher, n)
	var wg sync.WaitGroup
	for i := range slots {
		slots[i] = f.NewSlot()
		wg.Add(1)
		go func() {
			defer wg.Done()
			fetchAll(t, slots[i], ts.URL, cs.content)
		}()
	}
	wg.Wait()
	ps := f.Peers().Snapshot()
	if len(ps) != 1 || ps[0].Conns != n {
		t.Fatalf("peers %+v, want one address with %d connections", ps, n)
	}
	for _, s := range slots {
		s.Close()
	}
	waitPeers(t, f.Peers(), "after closing every slot", func(ps []Peer) bool { return len(ps) == 0 })
}

func TestTrackedConnReleasesOnce(t *testing.T) {
	tab := NewPeerTable()
	c1, c2 := netPipe(t)
	got := tab.track(fakeAddrConn{c1, "203.0.113.5:443"}, "https://example.com:443")
	got2 := tab.track(fakeAddrConn{c2, "203.0.113.5:443"}, "https://cdn.example.com:443")
	ps := tab.Snapshot()
	if len(ps) != 1 || ps[0].Conns != 2 || len(ps[0].Hosts) != 2 {
		t.Fatalf("%+v", ps)
	}
	got.Close()
	got.Close() // a second Close releases nothing more
	if ps := tab.Snapshot(); len(ps) != 1 || ps[0].Conns != 1 || ps[0].Hosts[0] != "cdn.example.com" {
		t.Fatalf("after one close: %+v", ps)
	}
	got2.Close()
	if ps := tab.Snapshot(); len(ps) != 0 {
		t.Fatalf("after both: %+v", ps)
	}
	var nilTab *PeerTable
	if c := nilTab.track(c1, "x"); c != c1 {
		t.Fatal("a nil table wrapped the connection")
	}
}

func netPipe(t *testing.T) (net.Conn, net.Conn) {
	a, b := net.Pipe()
	t.Cleanup(func() { a.Close(); b.Close() })
	return a, b
}

// fakeAddrConn reports a fixed remote address.
type fakeAddrConn struct {
	net.Conn
	remote string
}

func (c fakeAddrConn) RemoteAddr() net.Addr {
	return net.TCPAddrFromAddrPort(netip.MustParseAddrPort(c.remote))
}
