package transport

import (
	"bytes"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"downex/internal/model"
)

// funcCreds is a Credentials provider built from functions (a nil function: no login).
type funcCreds struct {
	server func(u *url.URL) (Login, bool)
	proxy  func(p Proxy) (Login, bool)
}

func (c funcCreds) ServerLogin(u *url.URL) (Login, bool) {
	if c.server == nil {
		return Login{}, false
	}
	return c.server(u)
}

func (c funcCreds) ProxyLogin(p Proxy) (Login, bool) {
	if c.proxy == nil {
		return Login{}, false
	}
	return c.proxy(p)
}

// testProxy is an HTTP proxy for tests: CONNECT requests are tunnelled to the address
// tunnel returns (refused when it returns ""), and forwarded (absolute-URI) requests are
// answered by forward. Every request is recorded.
type testProxy struct {
	srv *httptest.Server
	rec recorder
	wg  sync.WaitGroup
}

func startTestProxy(t *testing.T, tunnel func(r *http.Request) string, forward http.HandlerFunc) *testProxy {
	t.Helper()
	p := &testProxy{}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.rec.record(r)
		if r.Method != http.MethodConnect {
			forward(w, r)
			return
		}
		addr := tunnel(r)
		if addr == "" {
			w.WriteHeader(http.StatusProxyAuthRequired)
			return
		}
		up, err := net.Dial("tcp", addr)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		conn, brw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			up.Close()
			return
		}
		if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection established\r\n\r\n"); err != nil {
			conn.Close()
			up.Close()
			return
		}
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			done := make(chan struct{}, 2)
			go func() { _, _ = io.Copy(up, brw.Reader); done <- struct{}{} }()
			go func() { _, _ = io.Copy(conn, up); done <- struct{}{} }()
			<-done
			conn.Close()
			up.Close()
			<-done
		}()
	}))
	t.Cleanup(func() {
		p.srv.CloseClientConnections()
		p.srv.Close()
		p.wg.Wait()
	})
	return p
}

func (p *testProxy) addr() string { return p.srv.Listener.Addr().String() }

func basicCredentials(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

// TestServerAuthRetryGoesToTheChallenger: when a redirect leads to another origin that
// challenges, the authenticated retry is sent to that origin directly. The origin that
// redirected never sees the challenger's credentials (they would cross plain HTTP), and
// the challenger never sees the caller's credentials of the first origin.
func TestServerAuthRetryGoesToTheChallenger(t *testing.T) {
	const user, pass = "zzuser", "zzpass"
	content := testContent(3000)
	var recB recorder
	b := startServer(t, kindHTTP1TLS, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recB.record(r)
		if u, p, ok := r.BasicAuth(); !ok || u != user || p != pass {
			w.Header().Set("WWW-Authenticate", `Basic realm="b"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(content))
	}))
	var recA recorder
	a := startServer(t, kindHTTP1, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recA.record(r)
		http.Redirect(w, r, b.URL+"/file?sig=1", http.StatusFound)
	}))
	bHost := mustURL(t, b.URL).Host
	opts := DefaultHTTPOptions()
	opts.Proxy = "direct"
	opts.Credentials = funcCreds{server: func(u *url.URL) (Login, bool) {
		if u.Host == bHost {
			return Login{Username: user, Password: pass}, true
		}
		return Login{}, false
	}}
	slot := newTestSlot(t, opts, b)
	resp, err := slot.Fetch(testCtx(t), RangeRequest{
		Request: model.Request{URL: a.URL + "/x", Headers: http.Header{"Cookie": {"a=1"}}},
		Start:   10, End: 509,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := readBody(t, resp)
	for i, r := range recA.all() {
		if v := r.Header.Get("Authorization"); v != "" {
			t.Errorf("request %d to the redirecting origin carried Authorization %q", i, v)
		}
	}
	for i, r := range recB.all() {
		if v := r.Header.Get("Cookie"); v != "" {
			t.Errorf("request %d to the challenger carried the first origin's Cookie %q", i, v)
		}
	}
	if resp.StatusCode != http.StatusPartialContent || !bytes.Equal(got, content[10:510]) {
		t.Fatalf("status %d, %d bytes: the challenger was not authenticated", resp.StatusCode, len(got))
	}
	if last := recB.last(t); last.Header.Get("Authorization") != basicCredentials(user, pass) {
		t.Errorf("challenger saw Authorization %q", last.Header.Get("Authorization"))
	}
	if want := b.URL + "/file?sig=1"; resp.FinalURL != want {
		t.Errorf("FinalURL = %q, want %q", resp.FinalURL, want)
	}
}

// TestOrigin407IsNotAnswered: a 407 that did not come from an HTTP proxy forwarding the
// request (a direct origin, or an origin inside a CONNECT tunnel) is never answered with
// proxy credentials; it is returned as is.
func TestOrigin407IsNotAnswered(t *testing.T) {
	const proxyUser, proxyPass = "zzproxy", "zzsecret"
	originHandler := func(rec *recorder) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec.record(r)
			w.Header().Add("Proxy-Authenticate", `Basic realm="evil"`)
			w.Header().Add("Proxy-Authenticate", "NTLM")
			w.WriteHeader(http.StatusProxyAuthRequired)
		})
	}
	var logins atomic.Int32
	creds := funcCreds{proxy: func(Proxy) (Login, bool) {
		logins.Add(1)
		return Login{Username: proxyUser, Password: proxyPass}, true
	}}
	check := func(t *testing.T, slot Fetcher, rawURL string, rec *recorder) {
		t.Helper()
		resp, err := slot.Fetch(testCtx(t), RangeRequest{Request: model.Request{URL: rawURL}, End: -1})
		if err != nil {
			t.Fatal(err)
		}
		readBody(t, resp)
		if resp.StatusCode != http.StatusProxyAuthRequired {
			t.Errorf("status = %d, want the origin's 407 returned", resp.StatusCode)
		}
		all := rec.all()
		if len(all) != 1 {
			t.Errorf("origin saw %d requests, want 1 (no authentication retry)", len(all))
		}
		for i, r := range all {
			if v := r.Header.Get("Proxy-Authorization"); v != "" {
				t.Errorf("request %d: the origin received Proxy-Authorization %q", i, v)
			}
		}
	}
	for _, kind := range []string{kindHTTP1, kindHTTP1TLS} {
		t.Run("direct "+kind, func(t *testing.T) {
			logins.Store(0)
			var rec recorder
			origin := startServer(t, kind, originHandler(&rec))
			opts := DefaultHTTPOptions()
			opts.Proxy = "direct"
			opts.Credentials = creds
			check(t, newTestSlot(t, opts, origin), origin.URL+"/f.bin", &rec)
			if n := logins.Load(); n != 0 {
				t.Errorf("ProxyLogin was asked %d times for a direct connection", n)
			}
		})
	}
	t.Run("origin inside a CONNECT tunnel", func(t *testing.T) {
		var rec recorder
		origin := startServer(t, kindHTTP1TLS, originHandler(&rec))
		proxy := startTestProxy(t, func(*http.Request) string { return origin.Listener.Addr().String() },
			func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) })
		opts := DefaultHTTPOptions()
		opts.Resolver = StaticResolver{{Kind: ProxyHTTP, Addr: proxy.addr()}}
		opts.Credentials = creds
		check(t, newTestSlot(t, opts, origin), "https://example.com/f.bin", &rec)
	})
}

// TestForwardingProxy407IsAnswered: a 407 from the HTTP proxy that forwarded a plain-http
// request is answered with the login of that proxy (ProxyLogin is asked for its
// address), and the proxy credentials never follow a redirect to an origin, not even
// one on the same host reached through a CONNECT tunnel.
func TestForwardingProxy407IsAnswered(t *testing.T) {
	const user, pass = "zzproxy", "zzsecret"
	want := basicCredentials(user, pass)
	content := testContent(4000)
	var originRec recorder
	origin := startServer(t, kindHTTP1TLS, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originRec.record(r)
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(content))
	}))
	var challenged atomic.Bool
	proxy := startTestProxy(t,
		func(r *http.Request) string {
			if r.Header.Get("Proxy-Authorization") != want {
				return ""
			}
			return origin.Listener.Addr().String()
		},
		func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Proxy-Authorization") != want {
				challenged.Store(true)
				w.Header().Set("Proxy-Authenticate", `Basic realm="proxy"`)
				w.WriteHeader(http.StatusProxyAuthRequired)
				return
			}
			http.Redirect(w, r, "https://example.com/file.bin", http.StatusFound)
		})
	var mu sync.Mutex
	var asked []string
	opts := DefaultHTTPOptions()
	opts.Resolver = StaticResolver{{Kind: ProxyHTTP, Addr: proxy.addr()}}
	opts.Credentials = funcCreds{proxy: func(p Proxy) (Login, bool) {
		mu.Lock()
		asked = append(asked, p.String())
		mu.Unlock()
		// The login is only known once the proxy asked for it (no preemptive Basic).
		if !challenged.Load() {
			return Login{}, false
		}
		return Login{Username: user, Password: pass}, true
	}}
	slot := newTestSlot(t, opts, origin)
	resp, err := slot.Fetch(testCtx(t), RangeRequest{Request: model.Request{URL: "http://example.com/start"}, Start: 100, End: 1099})
	if err != nil {
		t.Fatal(err)
	}
	got := readBody(t, resp)
	if resp.StatusCode != http.StatusPartialContent || !bytes.Equal(got, content[100:1100]) {
		t.Fatalf("status %d, %d bytes after proxy authentication", resp.StatusCode, len(got))
	}
	forwarded := 0
	for _, r := range proxy.rec.all() {
		if r.Method != http.MethodConnect {
			forwarded++
		}
	}
	if forwarded != 2 {
		t.Errorf("proxy saw %d forwarded requests, want 2 (challenge, then authenticated)", forwarded)
	}
	mu.Lock()
	for _, a := range asked {
		if a != "http://"+proxy.addr() {
			t.Errorf("ProxyLogin asked for %s, want only the forwarding proxy %s", a, proxy.addr())
		}
	}
	mu.Unlock()
	for i, r := range originRec.all() {
		if v := r.Header.Get("Proxy-Authorization"); v != "" {
			t.Errorf("request %d: the origin received the proxy's credentials %q", i, v)
		}
	}
}
