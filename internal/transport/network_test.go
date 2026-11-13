package transport

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"downex/internal/model"
)

// familyServer answers every request with the address family it was reached over and
// counts its requests.
type familyServer struct {
	*httptest.Server
	hits atomic.Int32
}

// startFamilyServer listens on addr ("127.0.0.1:0", "[::1]:0"); ok is false when the
// address family is not available on this machine.
func startFamilyServer(t *testing.T, addr, family string) (*familyServer, bool) {
	t.Helper()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, false
	}
	fs := &familyServer{}
	fs.Server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fs.hits.Add(1)
		_, _ = io.WriteString(w, family)
	}))
	fs.Listener.Close()
	fs.Listener = ln
	fs.Start()
	t.Cleanup(fs.Close)
	return fs, true
}

func directFactory(t *testing.T) Factory {
	t.Helper()
	o := DefaultHTTPOptions()
	o.Proxy = "direct"
	f, err := NewHTTPFactory(o)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func fetchText(ctx context.Context, f Factory, url string) (string, error) {
	slot := f.NewSlot()
	defer slot.Close()
	resp, err := slot.Fetch(ctx, RangeRequest{Request: model.Request{URL: url}, Start: 0, End: -1})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

func TestWithNetworkDialsOnlyThatFamily(t *testing.T) {
	ctx := testCtx(t)
	v4, _ := startFamilyServer(t, "127.0.0.1:0", "v4")
	v6, haveV6 := startFamilyServer(t, "[::1]:0", "v6")
	if !haveV6 {
		t.Log("no IPv6 loopback: the IPv6 server is left out")
	}
	base := directFactory(t)

	// tcp4: the IPv4 server answers; an IPv6 address is never dialled.
	f4 := WithNetwork(base, "tcp4")
	if got, err := fetchText(ctx, f4, v4.URL+"/a"); err != nil || got != "v4" {
		t.Fatalf("tcp4 to 127.0.0.1 = %q, %v; want v4", got, err)
	}
	v6URL := "http://[::1]:1/b"
	if haveV6 {
		v6URL = v6.URL + "/b"
	}
	if _, err := fetchText(ctx, f4, v6URL); err == nil {
		t.Fatal("tcp4 reached an IPv6 address")
	}
	if haveV6 && v6.hits.Load() != 0 {
		t.Fatalf("the IPv6 server saw %d requests from a tcp4 slot", v6.hits.Load())
	}

	// tcp6: the IPv4 server is never dialled.
	before := v4.hits.Load()
	f6 := WithNetwork(base, "tcp6")
	if _, err := fetchText(ctx, f6, v4.URL+"/c"); err == nil {
		t.Fatal("tcp6 reached an IPv4 address")
	}
	if v4.hits.Load() != before {
		t.Fatal("the IPv4 server saw a request from a tcp6 slot")
	}
	if haveV6 {
		if got, err := fetchText(ctx, f6, v6.URL+"/d"); err != nil || got != "v6" {
			t.Fatalf("tcp6 to ::1 = %q, %v; want v6", got, err)
		}
	}

	// Without a family, both are reachable from the same factory.
	if got, err := fetchText(ctx, base, v4.URL+"/e"); err != nil || got != "v4" {
		t.Fatalf("unpinned to 127.0.0.1 = %q, %v", got, err)
	}
	if haveV6 {
		if got, err := fetchText(ctx, base, v6.URL+"/f"); err != nil || got != "v6" {
			t.Fatalf("unpinned to ::1 = %q, %v", got, err)
		}
	}
}

func TestWithNetworkLeavesProxiedConnections(t *testing.T) {
	// A forwarding proxy on 127.0.0.1: a tcp6 pin must not apply to the dial of the
	// proxy, which the target's family does not concern.
	var target atomic.Value
	proxy, _ := startFamilyServer(t, "127.0.0.1:0", "unused")
	proxy.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target.Store(r.URL.String())
		_, _ = io.WriteString(w, "via proxy")
	})
	o := DefaultHTTPOptions()
	o.Proxy = proxy.URL
	f, err := NewHTTPFactory(o)
	if err != nil {
		t.Fatal(err)
	}
	got, err := fetchText(testCtx(t), WithNetwork(f, "tcp6"), "http://media.example.invalid/v")
	if err != nil || got != "via proxy" {
		t.Fatalf("pinned fetch through a proxy = %q, %v", got, err)
	}
	if u, _ := target.Load().(string); !strings.HasPrefix(u, "http://media.example.invalid/") {
		t.Fatalf("proxy saw request-target %q", u)
	}
}

func TestWithNetworkValues(t *testing.T) {
	base := directFactory(t)
	for _, n := range []string{"", "tcp", "udp4", "TCP4"} {
		if f := WithNetwork(base, n); f != base {
			t.Errorf("WithNetwork(f, %q) wrapped the factory", n)
		}
	}
	if WithNetwork(nil, "tcp4") != nil {
		t.Error("WithNetwork(nil) is not nil")
	}

	ctx := context.Background()
	if got := dialNetwork(ctx, "tcp"); got != "tcp" {
		t.Errorf("unpinned dial network = %q", got)
	}
	pinned := context.WithValue(ctx, networkKey{}, "tcp6")
	if got := dialNetwork(pinned, "tcp"); got != "tcp6" {
		t.Errorf("pinned dial network = %q, want tcp6", got)
	}
	if got := dialNetwork(pinned, "udp"); got != "udp" {
		t.Errorf("a non-tcp dial became %q", got)
	}
}

// ctxFetcher records the family the context of each Fetch carries.
type ctxFetcher struct{ seen *[]string }

func (c ctxFetcher) Fetch(ctx context.Context, _ RangeRequest) (*Response, error) {
	v, _ := ctx.Value(networkKey{}).(string)
	*c.seen = append(*c.seen, v)
	return nil, io.EOF
}

func (ctxFetcher) Close() {}

type ctxFactory struct{ seen *[]string }

func (c ctxFactory) NewSlot() Fetcher { return ctxFetcher(c) }

func TestWithNetworkOutermostWins(t *testing.T) {
	var seen []string
	inner := WithNetwork(ctxFactory{&seen}, "tcp6")
	outer := WithNetwork(inner, "tcp4")
	_, _ = inner.NewSlot().Fetch(context.Background(), RangeRequest{})
	_, _ = outer.NewSlot().Fetch(context.Background(), RangeRequest{})
	if len(seen) != 2 || seen[0] != "tcp6" || seen[1] != "tcp4" {
		t.Fatalf("families seen = %v, want [tcp6 tcp4]", seen)
	}
}
