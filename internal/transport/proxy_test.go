package transport

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"downex/internal/model"
)

// socks5Server is a minimal RFC 1928 SOCKS5 server (CONNECT only) with optional
// RFC 1929 username/password authentication, for tests.
type socks5Server struct {
	ln         net.Listener
	user, pass string

	mu      sync.Mutex
	targets []string // CONNECT targets as received ("host:port")
	authOK  int
	wg      sync.WaitGroup
}

func startSOCKS5(t *testing.T, user, pass string) *socks5Server {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &socks5Server{ln: ln, user: user, pass: pass}
	s.wg.Add(1)
	go s.acceptLoop()
	t.Cleanup(func() {
		ln.Close()
		s.wg.Wait()
	})
	return s
}

func (s *socks5Server) addr() string { return s.ln.Addr().String() }

func (s *socks5Server) seenTargets() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.targets...)
}

func (s *socks5Server) acceptLoop() {
	defer s.wg.Done()
	var conns sync.WaitGroup
	defer conns.Wait()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		conns.Add(1)
		go func() {
			defer conns.Done()
			s.serve(c)
		}()
	}
}

func (s *socks5Server) serve(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	br := bufio.NewReader(c)
	read := func(n int) ([]byte, error) {
		b := make([]byte, n)
		_, err := io.ReadFull(br, b)
		return b, err
	}

	// Greeting: VER NMETHODS METHODS...
	hdr, err := read(2)
	if err != nil || hdr[0] != 5 {
		return
	}
	methods, err := read(int(hdr[1]))
	if err != nil {
		return
	}
	want := byte(0x00) // no authentication
	if s.user != "" {
		want = 0x02 // username/password
	}
	if bytes.IndexByte(methods, want) < 0 {
		_, _ = c.Write([]byte{5, 0xff})
		return
	}
	if _, err := c.Write([]byte{5, want}); err != nil {
		return
	}
	if want == 0x02 {
		// RFC 1929: VER ULEN UNAME PLEN PASSWD
		v, err := read(2)
		if err != nil || v[0] != 1 {
			return
		}
		user, _ := read(int(v[1]))
		plen, _ := read(1)
		pass, err := read(int(plen[0]))
		if err != nil {
			return
		}
		if string(user) != s.user || string(pass) != s.pass {
			_, _ = c.Write([]byte{1, 1})
			return
		}
		s.mu.Lock()
		s.authOK++
		s.mu.Unlock()
		if _, err := c.Write([]byte{1, 0}); err != nil {
			return
		}
	}

	// Request: VER CMD RSV ATYP DST.ADDR DST.PORT
	req, err := read(4)
	if err != nil || req[0] != 5 {
		return
	}
	reply := func(code byte) { _, _ = c.Write([]byte{5, code, 0, 1, 0, 0, 0, 0, 0, 0}) }
	if req[1] != 1 {
		reply(0x07) // command not supported
		return
	}
	var host string
	switch req[3] {
	case 1:
		b, err := read(4)
		if err != nil {
			return
		}
		host = net.IP(b).String()
	case 3:
		l, err := read(1)
		if err != nil {
			return
		}
		b, err := read(int(l[0]))
		if err != nil {
			return
		}
		host = string(b)
	case 4:
		b, err := read(16)
		if err != nil {
			return
		}
		host = net.IP(b).String()
	default:
		reply(0x08)
		return
	}
	pb, err := read(2)
	if err != nil {
		return
	}
	target := net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(pb))))
	s.mu.Lock()
	s.targets = append(s.targets, target)
	s.mu.Unlock()

	up, err := net.DialTimeout("tcp", target, 5*time.Second)
	if err != nil {
		reply(0x05) // connection refused
		return
	}
	defer up.Close()
	reply(0x00)
	_ = c.SetDeadline(time.Time{})

	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(up, br); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, up); done <- struct{}{} }()
	<-done
	c.Close()
	up.Close()
	<-done
}

func TestSOCKS5Proxy(t *testing.T) {
	for _, kind := range []string{kindHTTP1, kindHTTP2TLS} {
		t.Run(kind, func(t *testing.T) {
			cs := newContentServer(50_000)
			ts := startServer(t, kind, cs)
			socks := startSOCKS5(t, "alice", "s3cret")

			opts := DefaultHTTPOptions()
			opts.Proxy = "socks5://alice:s3cret@" + socks.addr()
			slot := newTestSlot(t, opts, ts)
			resp, err := slot.Fetch(testCtx(t), RangeRequest{Request: model.Request{URL: ts.URL}, Start: 100, End: 4099})
			if err != nil {
				t.Fatal(err)
			}
			if got := readBody(t, resp); !bytes.Equal(got, cs.content[100:4100]) {
				t.Fatal("body mismatch through SOCKS5")
			}
			if targets := socks.seenTargets(); len(targets) != 1 || targets[0] != ts.Listener.Addr().String() {
				t.Fatalf("SOCKS5 CONNECT targets = %v, want [%s]", targets, ts.Listener.Addr())
			}
		})
	}

	t.Run("host names resolved by the proxy", func(t *testing.T) {
		cs := newContentServer(100)
		ts := startServer(t, kindHTTP1, cs)
		socks := startSOCKS5(t, "", "")
		_, port, _ := net.SplitHostPort(ts.Listener.Addr().String())
		opts := DefaultHTTPOptions()
		opts.Proxy = "socks5h://" + socks.addr()
		slot := newTestSlot(t, opts)
		resp, err := slot.Fetch(testCtx(t), RangeRequest{Request: model.Request{URL: "http://localhost:" + port + "/"}, End: -1})
		if err != nil {
			t.Fatal(err)
		}
		readBody(t, resp)
		if targets := socks.seenTargets(); len(targets) != 1 || targets[0] != "localhost:"+port {
			t.Fatalf("SOCKS5 CONNECT targets = %v, want the unresolved name", targets)
		}
	})

	t.Run("wrong password", func(t *testing.T) {
		var rec recorder
		ts := startServer(t, kindHTTP1, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { rec.record(r) }))
		socks := startSOCKS5(t, "alice", "s3cret")
		opts := DefaultHTTPOptions()
		opts.Proxy = "socks5://alice:wrong@" + socks.addr()
		slot := newTestSlot(t, opts, ts)
		if _, err := slot.Fetch(testCtx(t), RangeRequest{Request: model.Request{URL: ts.URL}, End: -1}); err == nil {
			t.Fatal("Fetch succeeded with a rejected SOCKS5 password")
		}
		if n := len(rec.all()); n != 0 {
			t.Fatalf("origin saw %d requests", n)
		}
	})
}

func TestHTTPProxy(t *testing.T) {
	const user, pass = "bob", "pw:with:colons"
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))

	cs := newContentServer(20_000)
	origin := startServer(t, kindHTTP2TLS, cs)

	var (
		mu       sync.Mutex
		seen     []string // "METHOD target auth"
		tunnelWG sync.WaitGroup
	)
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.RequestURI+" "+r.Header.Get("Proxy-Authorization"))
		mu.Unlock()
		if r.Header.Get("Proxy-Authorization") != wantAuth {
			w.WriteHeader(http.StatusProxyAuthRequired)
			return
		}
		if r.Method == http.MethodConnect {
			up, err := net.Dial("tcp", r.Host)
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
			tunnelWG.Add(1)
			go func() {
				defer tunnelWG.Done()
				done := make(chan struct{}, 2)
				go func() { _, _ = io.Copy(up, brw.Reader); done <- struct{}{} }()
				go func() { _, _ = io.Copy(conn, up); done <- struct{}{} }()
				<-done
				conn.Close()
				up.Close()
				<-done
			}()
			return
		}
		// Plain HTTP is forwarded by absolute URI; answer as the origin would.
		w.Header().Set("Content-Range", "bytes 0-4/5")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, "hello")
	}))
	t.Cleanup(func() {
		proxySrv.CloseClientConnections()
		proxySrv.Close()
		tunnelWG.Wait()
	})

	opts := DefaultHTTPOptions()
	opts.Proxy = "http://" + user + ":" + strings.ReplaceAll(pass, ":", "%3A") + "@" + proxySrv.Listener.Addr().String()
	slot := newTestSlot(t, opts, origin)

	t.Run("plain http by absolute URI", func(t *testing.T) {
		resp, err := slot.Fetch(testCtx(t), RangeRequest{Request: model.Request{URL: "http://origin.invalid/file.bin"}, End: -1})
		if err != nil {
			t.Fatal(err)
		}
		if got := readBody(t, resp); string(got) != "hello" || resp.StatusCode != http.StatusPartialContent {
			t.Fatalf("status %d body %q", resp.StatusCode, got)
		}
		mu.Lock()
		last := seen[len(seen)-1]
		mu.Unlock()
		if want := "GET http://origin.invalid/file.bin " + wantAuth; last != want {
			t.Fatalf("proxy saw %q, want %q", last, want)
		}
	})

	t.Run("https through CONNECT", func(t *testing.T) {
		resp, err := slot.Fetch(testCtx(t), RangeRequest{Request: model.Request{URL: origin.URL}, Start: 5, End: 1004})
		if err != nil {
			t.Fatal(err)
		}
		if got := readBody(t, resp); !bytes.Equal(got, cs.content[5:1005]) {
			t.Fatal("body mismatch through CONNECT tunnel")
		}
		mu.Lock()
		last := seen[len(seen)-1]
		mu.Unlock()
		if want := "CONNECT " + origin.Listener.Addr().String() + " " + wantAuth; last != want {
			t.Fatalf("proxy saw %q, want %q", last, want)
		}
		if p := cs.rec.last(t).Proto; p != 2 {
			t.Errorf("origin saw HTTP/%d through the tunnel, want HTTP/2", p)
		}
	})
}

func TestNewHTTPFactoryOptions(t *testing.T) {
	f, err := NewHTTPFactory(HTTPOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got := f.Options()
	want := DefaultHTTPOptions()
	want.EnableHTTP2 = false // a zero bool stays false; DefaultHTTPOptions turns it on
	if got != want {
		t.Errorf("zero options normalised to %+v, want %+v", got, want)
	}
	if !DefaultHTTPOptions().EnableHTTP2 {
		t.Error("DefaultHTTPOptions has HTTP/2 disabled")
	}

	valid := []string{"", "direct", "DIRECT", "none", "http://proxy.local:3128", "proxy.local:3128",
		"https://u:p@proxy.local", "socks5://proxy.local", "socks5://u:p@proxy.local:1081", "socks5h://proxy.local:9050"}
	for _, p := range valid {
		opts := DefaultHTTPOptions()
		opts.Proxy = p
		if _, err := NewHTTPFactory(opts); err != nil {
			t.Errorf("Proxy %q: %v", p, err)
		}
	}

	invalid := []struct {
		name string
		mod  func(*HTTPOptions)
	}{
		{"unsupported proxy scheme", func(o *HTTPOptions) { o.Proxy = "gopher://user:hunter2@proxy.local" }},
		{"proxy without host", func(o *HTTPOptions) { o.Proxy = "socks5://user:hunter2@:1080" }},
		{"unparsable proxy", func(o *HTTPOptions) { o.Proxy = "http://user:hunter2@proxy local:%%" }},
		{"negative dial timeout", func(o *HTTPOptions) { o.DialTimeout = -time.Second }},
		{"negative idle timeout", func(o *HTTPOptions) { o.IdleConnTimeout = -1 }},
		{"negative header limit", func(o *HTTPOptions) { o.MaxHeaderBytes = -1 }},
		{"user agent with CRLF", func(o *HTTPOptions) { o.UserAgent = "x\r\nX-Evil: 1" }},
	}
	for _, tc := range invalid {
		opts := DefaultHTTPOptions()
		tc.mod(&opts)
		_, err := NewHTTPFactory(opts)
		if err == nil {
			t.Errorf("%s: NewHTTPFactory succeeded", tc.name)
			continue
		}
		if strings.Contains(err.Error(), "hunter2") {
			t.Errorf("%s: error leaks the proxy password: %v", tc.name, err)
		}
	}
}

func TestSOCKS5DialTimeout(t *testing.T) {
	// A proxy that accepts TCP but never answers the SOCKS greeting.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var held []net.Conn
	var mu sync.Mutex
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, c)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		mu.Lock()
		for _, c := range held {
			c.Close()
		}
		mu.Unlock()
	})
	opts := DefaultHTTPOptions()
	opts.Proxy = "socks5://" + ln.Addr().String()
	opts.DialTimeout = 200 * time.Millisecond
	slot := newTestSlot(t, opts)
	start := time.Now()
	_, err = slot.Fetch(testCtx(t), RangeRequest{Request: model.Request{URL: "http://origin.invalid/"}, End: -1})
	if err == nil {
		t.Fatal("Fetch through a silent SOCKS5 proxy succeeded")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("SOCKS5 handshake not bounded by DialTimeout: took %v", elapsed)
	}
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Logf("error is not a net timeout (acceptable): %v", err)
	}
}
