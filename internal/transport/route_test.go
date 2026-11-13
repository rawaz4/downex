package transport

import (
	"bufio"
	"bytes"
	"context"
	"crypto/md5"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Azure/go-ntlmssp"

	"downex/internal/model"
)

// netProxy is a raw HTTP/1.1 proxy for the route tests. Every connection serves
// requests in a loop; check (when set) decides whether a request is authorised, else it
// is answered 407 with the challenge check returns. Authorised CONNECT requests are
// tunnelled to their target; forwarded requests are answered "hello" as an origin
// would. With a TLS config the proxy is reached over TLS and records the negotiated
// application protocol.
type netProxy struct {
	ln    net.Listener
	check func(r *http.Request, conn int) (challenge string, ok bool)

	mu    sync.Mutex
	seen  []netProxyRequest
	alpn  []string
	conns int
	wg    sync.WaitGroup
}

// netProxyRequest is one request a netProxy received.
type netProxyRequest struct {
	conn   int    // 1-based connection number
	method string // CONNECT or the forwarded method
	target string // the request-target: host:port or an absolute URI
	auth   string // Proxy-Authorization
}

func startNetProxy(t *testing.T, cfg *tls.Config, check func(*http.Request, int) (string, bool)) *netProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if cfg != nil {
		ln = tls.NewListener(ln, cfg)
	}
	p := &netProxy{ln: ln, check: check}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			p.mu.Lock()
			p.conns++
			n := p.conns
			p.mu.Unlock()
			p.wg.Add(1)
			go func() {
				defer p.wg.Done()
				p.serve(c, n)
			}()
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		p.wg.Wait()
	})
	return p
}

func (p *netProxy) addr() string { return p.ln.Addr().String() }

func (p *netProxy) requests() []netProxyRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]netProxyRequest(nil), p.seen...)
}

func (p *netProxy) protocols() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.alpn...)
}

func (p *netProxy) serve(c net.Conn, n int) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(20 * time.Second))
	if tc, ok := c.(*tls.Conn); ok {
		if err := tc.Handshake(); err != nil {
			return
		}
		p.mu.Lock()
		p.alpn = append(p.alpn, tc.ConnectionState().NegotiatedProtocol)
		p.mu.Unlock()
	}
	br := bufio.NewReader(c)
	for {
		req, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		_, _ = io.Copy(io.Discard, req.Body)
		p.mu.Lock()
		p.seen = append(p.seen, netProxyRequest{conn: n, method: req.Method, target: req.RequestURI, auth: req.Header.Get("Proxy-Authorization")})
		p.mu.Unlock()
		if p.check != nil {
			if challenge, ok := p.check(req, n); !ok {
				msg := "HTTP/1.1 407 Proxy Authentication Required\r\nContent-Length: 0\r\n"
				if challenge != "" {
					msg += "Proxy-Authenticate: " + challenge + "\r\n"
				}
				if _, err := io.WriteString(c, msg+"\r\n"); err != nil {
					return
				}
				continue
			}
		}
		if req.Method != http.MethodConnect {
			if _, err := io.WriteString(c, "HTTP/1.1 206 Partial Content\r\nContent-Range: bytes 0-4/5\r\nContent-Length: 5\r\n\r\nhello"); err != nil {
				return
			}
			continue
		}
		up, err := net.DialTimeout("tcp", req.Host, 5*time.Second)
		if err != nil {
			_, _ = io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
			return
		}
		defer up.Close()
		if _, err := io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n"); err != nil {
			return
		}
		_ = c.SetDeadline(time.Time{})
		done := make(chan struct{}, 2)
		go func() { _, _ = io.Copy(up, br); done <- struct{}{} }()
		go func() { _, _ = io.Copy(c, up); done <- struct{}{} }()
		<-done
		c.Close()
		up.Close()
		<-done
		return
	}
}

// netProxyTLS returns a TLS server configuration with httptest's certificate offering
// HTTP/2 and HTTP/1.1 (as h2-capable proxies do), and a pool trusting it.
func netProxyTLS(t *testing.T) (*tls.Config, *x509.CertPool) {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.NotFoundHandler())
	srv.StartTLS()
	cfg := srv.TLS.Clone()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	srv.Close()
	cfg.NextProtos = []string{"h2", "http/1.1"}
	return cfg, pool
}

// newRouteSlot returns a slot trusting pool (nil: no extra roots).
func newRouteSlot(t *testing.T, opts HTTPOptions, pool *x509.CertPool) Fetcher {
	t.Helper()
	f, err := newHTTPFactory(opts, pool)
	if err != nil {
		t.Fatalf("newHTTPFactory: %v", err)
	}
	s := f.NewSlot()
	t.Cleanup(s.Close)
	return s
}

// closedAddr returns a loopback address nothing listens on.
func closedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// routeCreds answers every proxy login with login and records which proxies it was
// asked for.
type routeCreds struct {
	login Login
	ok    bool

	mu    sync.Mutex
	asked []string
}

func (c *routeCreds) ServerLogin(*url.URL) (Login, bool) { return Login{}, false }
func (c *routeCreds) ProxyLogin(p Proxy) (Login, bool) {
	c.mu.Lock()
	c.asked = append(c.asked, p.String())
	c.mu.Unlock()
	return c.login, c.ok
}

// TestOrigin407ThroughSOCKSIsNotAnswered: an origin reached through a SOCKS proxy that
// answers 407 gets no proxy credentials (Basic or NTLM); its response is returned.
func TestOrigin407ThroughSOCKSIsNotAnswered(t *testing.T) {
	var rec recorder
	origin := startServer(t, kindHTTP1, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.Header().Add("Proxy-Authenticate", "NTLM")
		w.Header().Add("Proxy-Authenticate", `Basic realm="evil"`)
		w.WriteHeader(http.StatusProxyAuthRequired)
	}))
	socks := startSOCKS5(t, "", "")
	opts := DefaultHTTPOptions()
	opts.Resolver = StaticResolver{{Kind: ProxySOCKS5, Addr: socks.addr()}}
	opts.Credentials = &routeCreds{login: Login{Username: "zzcorp", Password: "zzproxy-secret"}, ok: true}
	slot := newRouteSlot(t, opts, nil)
	resp, err := slot.Fetch(testCtx(t), RangeRequest{Request: model.Request{URL: origin.URL + "/x"}, End: -1})
	if err != nil {
		t.Fatal(err)
	}
	readBody(t, resp)
	if resp.StatusCode != http.StatusProxyAuthRequired {
		t.Fatalf("status %d, want the origin's 407", resp.StatusCode)
	}
	all := rec.all()
	if len(all) != 1 {
		t.Errorf("origin saw %d requests, want 1", len(all))
	}
	for _, r := range all {
		if v := r.Header.Get("Proxy-Authorization"); v != "" {
			t.Errorf("origin behind SOCKS received Proxy-Authorization %q", v)
		}
	}
	if got := socks.seenTargets(); len(got) != 1 {
		t.Errorf("SOCKS targets %v, want the origin once", got)
	}
}

// digestProxyCheck verifies RFC 7616 Digest (MD5, qop=auth) Proxy-Authorization for
// user/pass, with the request's method and request-target as the digest URI.
func digestProxyCheck(user, pass string) func(*http.Request, int) (string, bool) {
	const realm, nonce = "proxyrealm", "pn0nce"
	challenge := fmt.Sprintf(`Digest realm=%q, qop="auth", nonce=%q, algorithm=MD5`, realm, nonce)
	h := func(parts ...string) string {
		sum := md5.Sum([]byte(strings.Join(parts, ":")))
		return hex.EncodeToString(sum[:])
	}
	return func(r *http.Request, _ int) (string, bool) {
		auth := r.Header.Get("Proxy-Authorization")
		if !strings.HasPrefix(auth, "Digest ") {
			return challenge, false
		}
		chs := parseChallenges([]string{auth})
		if len(chs) == 0 {
			return challenge, false
		}
		p := chs[0].params
		if p["uri"] != r.RequestURI || p["username"] != user {
			return challenge, false
		}
		want := h(h(user, realm, pass), nonce, p["nc"], p["cnonce"], p["qop"], h(r.Method, p["uri"]))
		return challenge, p["response"] == want
	}
}

// TestConnectProxyAuthentication: a proxy that answers CONNECT with 407 is
// authenticated like a forwarding proxy (Digest, NTLM on the same connection), and a
// final 407 fails with ErrAuthRequired instead of a network error.
func TestConnectProxyAuthentication(t *testing.T) {
	cs := newContentServer(30_000)
	origin := startServer(t, kindHTTP2TLS, cs)
	fetch := func(t *testing.T, proxy *netProxy, creds Credentials) (*Response, error) {
		t.Helper()
		opts := DefaultHTTPOptions()
		opts.Resolver = StaticResolver{{Kind: ProxyHTTP, Addr: proxy.addr()}}
		opts.Credentials = creds
		return newTestSlot(t, opts, origin).Fetch(testCtx(t), RangeRequest{Request: model.Request{URL: origin.URL + "/f"}, Start: 7, End: 4006})
	}

	t.Run("digest", func(t *testing.T) {
		proxy := startNetProxy(t, nil, digestProxyCheck("zzuser", "zzpw"))
		resp, err := fetch(t, proxy, &routeCreds{login: Login{Username: "zzuser", Password: "zzpw"}, ok: true})
		if err != nil {
			t.Fatalf("Fetch through a Digest CONNECT proxy: %v", err)
		}
		if got := readBody(t, resp); !bytes.Equal(got, cs.content[7:4007]) {
			t.Fatal("body mismatch through the authenticated tunnel")
		}
		reqs := proxy.requests()
		if last := reqs[len(reqs)-1]; last.method != http.MethodConnect || !strings.HasPrefix(last.auth, "Digest ") {
			t.Fatalf("proxy saw %+v last, want an authenticated CONNECT", last)
		}
	})

	t.Run("ntlm on one connection", func(t *testing.T) {
		chal, _ := base64.StdEncoding.DecodeString(cannedChallenge())
		if _, err := ntlmssp.ProcessChallenge(chal, "WORKGROUP\\user", "pw", true); err != nil {
			t.Skipf("canned NTLM challenge rejected by go-ntlmssp (%v)", err)
		}
		var mu sync.Mutex
		legs := map[int][]int{} // NTLM message type -> connections
		proxy := startNetProxy(t, nil, func(r *http.Request, conn int) (string, bool) {
			auth := r.Header.Get("Proxy-Authorization")
			raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(auth, "NTLM "))
			if !strings.HasPrefix(auth, "NTLM ") || err != nil || len(raw) < 9 {
				return "NTLM", false
			}
			mu.Lock()
			legs[int(raw[8])] = append(legs[int(raw[8])], conn)
			mu.Unlock()
			if raw[8] == 1 {
				return "NTLM " + cannedChallenge(), false
			}
			return "", raw[8] == 3
		})
		resp, err := fetch(t, proxy, &routeCreds{login: Login{Username: "WORKGROUP\\user", Password: "pw"}, ok: true})
		if err != nil {
			t.Fatalf("Fetch through an NTLM CONNECT proxy: %v", err)
		}
		readBody(t, resp)
		mu.Lock()
		defer mu.Unlock()
		if len(legs[1]) != 1 || len(legs[3]) != 1 || legs[1][0] != legs[3][0] {
			t.Fatalf("NTLM messages by connection %v, want type 1 and type 3 on one connection", legs)
		}
	})

	for _, c := range []struct {
		name  string
		creds Credentials
	}{
		{"no login", &routeCreds{}},
		{"rejected login", &routeCreds{login: Login{Username: "zzuser", Password: "wrong"}, ok: true}},
	} {
		t.Run(c.name, func(t *testing.T) {
			proxy := startNetProxy(t, nil, digestProxyCheck("zzuser", "zzpw"))
			_, err := fetch(t, proxy, c.creds)
			if !errors.Is(err, ErrAuthRequired) {
				t.Fatalf("err = %v, want ErrAuthRequired", err)
			}
			for _, r := range proxy.requests() {
				if r.method != http.MethodConnect {
					t.Errorf("proxy saw %s %s, want only CONNECT attempts", r.method, r.target)
				}
			}
		})
	}
}

// TestRouteFailover: when a route cannot be reached the next one of the list is tried,
// whatever their kinds ("PROXY p1; PROXY p2; DIRECT", "DIRECT; PROXY p").
func TestRouteFailover(t *testing.T) {
	cs := newContentServer(10_000)
	plain := startServer(t, kindHTTP1, cs)
	secure := startServer(t, kindHTTP2TLS, cs)
	live := startNetProxy(t, nil, nil)
	for _, c := range []struct {
		name   string
		routes StaticResolver
		url    string
		want   []byte
	}{
		{"dead http proxy then direct", StaticResolver{{Kind: ProxyHTTP, Addr: closedAddr(t)}, Direct}, plain.URL + "/a", cs.content[:100]},
		{"dead socks then direct", StaticResolver{{Kind: ProxySOCKS5, Addr: closedAddr(t)}, Direct}, plain.URL + "/b", cs.content[:100]},
		{"dead proxy then a live CONNECT proxy", StaticResolver{{Kind: ProxyHTTP, Addr: closedAddr(t)}, {Kind: ProxyHTTP, Addr: live.addr()}}, secure.URL + "/c", cs.content[:100]},
		{"direct fails then a forwarding proxy", StaticResolver{Direct, {Kind: ProxyHTTP, Addr: live.addr()}}, "http://origin.invalid/d", []byte("hello")},
	} {
		t.Run(c.name, func(t *testing.T) {
			opts := DefaultHTTPOptions()
			opts.Resolver = c.routes
			slot := newTestSlot(t, opts, secure)
			for i := range 2 { // the second time the dead route is remembered
				resp, err := slot.Fetch(testCtx(t), RangeRequest{Request: model.Request{URL: c.url}, Start: 0, End: 99})
				if err != nil {
					t.Fatalf("attempt %d: %v", i, err)
				}
				if got := readBody(t, resp); !bytes.Equal(got, c.want) {
					t.Fatalf("attempt %d: body %q", i, got)
				}
			}
		})
	}
}

// TestIDNTargetUsesItsRoute: a host name outside ASCII is resolved and routed in the
// IDNA form net/http dials, so its SOCKS route is used (no local DNS lookup).
func TestIDNTargetUsesItsRoute(t *testing.T) {
	socks := startSOCKS5(t, "", "")
	var mu sync.Mutex
	var resolved []string
	opts := DefaultHTTPOptions()
	opts.Resolver = ResolverFunc(func(_ context.Context, u *url.URL) ([]Proxy, error) {
		mu.Lock()
		resolved = append(resolved, u.Host)
		mu.Unlock()
		return []Proxy{{Kind: ProxySOCKS5, Addr: socks.addr()}}, nil
	})
	slot := newRouteSlot(t, opts, nil)
	if _, err := slot.Fetch(testCtx(t), RangeRequest{Request: model.Request{URL: "http://bücher.invalid/f"}, End: -1}); err == nil {
		t.Fatal("Fetch of an unreachable name succeeded")
	}
	if got := socks.seenTargets(); len(got) != 1 || got[0] != "xn--bcher-kva.invalid:80" {
		t.Fatalf("SOCKS targets = %v, want [xn--bcher-kva.invalid:80] (the route was bypassed)", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(resolved) == 0 || resolved[0] != "xn--bcher-kva.invalid" {
		t.Errorf("resolver saw hosts %q, want the IDNA form", resolved)
	}
}

// TestHTTPSProxyUsesHTTP1: an HTTPS proxy that offers HTTP/2 is spoken to in HTTP/1.1
// (CONNECT and forwarded requests); HTTP/2 is still negotiated with the origin inside
// the tunnel.
func TestHTTPSProxyUsesHTTP1(t *testing.T) {
	cfg, pool := netProxyTLS(t)
	cs := newContentServer(20_000)
	origin := startServer(t, kindHTTP2TLS, cs)
	pool.AddCert(origin.Certificate())
	proxy := startNetProxy(t, cfg, nil)
	opts := DefaultHTTPOptions()
	opts.Resolver = StaticResolver{{Kind: ProxyHTTPS, Addr: proxy.addr()}}
	slot := newRouteSlot(t, opts, pool)

	resp, err := slot.Fetch(testCtx(t), RangeRequest{Request: model.Request{URL: origin.URL + "/t"}, Start: 1, End: 1000})
	if err != nil {
		t.Fatalf("https through an HTTPS proxy: %v", err)
	}
	if got := readBody(t, resp); !bytes.Equal(got, cs.content[1:1001]) {
		t.Fatal("body mismatch through the HTTPS proxy's tunnel")
	}
	if p := cs.rec.last(t).Proto; p != 2 {
		t.Errorf("origin saw HTTP/%d through the tunnel, want HTTP/2", p)
	}
	resp, err = slot.Fetch(testCtx(t), RangeRequest{Request: model.Request{URL: "http://origin.invalid/f"}, End: -1})
	if err != nil {
		t.Fatalf("http forwarded by an HTTPS proxy: %v", err)
	}
	if got := readBody(t, resp); string(got) != "hello" {
		t.Fatalf("forwarded body %q", got)
	}
	for _, p := range proxy.protocols() {
		if p == "h2" {
			t.Fatalf("negotiated %q with the proxy, want HTTP/1.1 only", proxy.protocols())
		}
	}
	reqs := proxy.requests()
	if len(reqs) != 2 || reqs[0].method != http.MethodConnect || reqs[1].target != "http://origin.invalid/f" {
		t.Fatalf("proxy saw %+v", reqs)
	}
}
