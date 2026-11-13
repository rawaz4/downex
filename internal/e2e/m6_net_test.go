package e2e

import (
	"bufio"
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/testserver"
)

// Network end-to-end tests (docs/M6-SPEC.md §2): downloads through a manual HTTP proxy
// (CONNECT tunnels to an https origin) and a SOCKS5 proxy, and from a server that wants
// Digest authentication with a site login whose password is stored with SetSecret.

// connectProxy is an HTTP proxy that only tunnels (CONNECT) and counts its tunnels.
type connectProxy struct {
	ln      net.Listener
	tunnels atomic.Int64
	wg      sync.WaitGroup
}

func newConnectProxy(t *testing.T) *connectProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &connectProxy{ln: ln}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			p.wg.Add(1)
			go func() { defer p.wg.Done(); p.serve(c) }()
		}
	}()
	t.Cleanup(func() { _ = ln.Close(); p.wg.Wait() })
	return p
}

func (p *connectProxy) addr() string { return p.ln.Addr().String() }

func (p *connectProxy) serve(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(2 * time.Minute))
	br := bufio.NewReader(c)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	if req.Method != http.MethodConnect {
		_, _ = io.WriteString(c, "HTTP/1.1 405 Method Not Allowed\r\nContent-Length: 0\r\n\r\n")
		return
	}
	up, err := net.DialTimeout("tcp", req.Host, 10*time.Second)
	if err != nil {
		_, _ = io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
		return
	}
	defer up.Close()
	p.tunnels.Add(1)
	if _, err := io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	pipe(c, br, up)
}

// pipe copies between the client c (whose buffered reader is r) and up until either ends.
func pipe(c net.Conn, r io.Reader, up net.Conn) {
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(up, r); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, up); done <- struct{}{} }()
	<-done
}

// socks5Proxy is a SOCKS5 proxy (RFC 1928, no authentication, CONNECT only) that counts
// its connections.
type socks5Proxy struct {
	ln    net.Listener
	conns atomic.Int64
	wg    sync.WaitGroup
}

func newSOCKS5Proxy(t *testing.T) *socks5Proxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &socks5Proxy{ln: ln}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			p.wg.Add(1)
			go func() { defer p.wg.Done(); p.serve(c) }()
		}
	}()
	t.Cleanup(func() { _ = ln.Close(); p.wg.Wait() })
	return p
}

func (p *socks5Proxy) serve(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(2 * time.Minute))
	br := bufio.NewReader(c)
	head := make([]byte, 2)
	if _, err := io.ReadFull(br, head); err != nil || head[0] != 5 {
		return
	}
	methods := make([]byte, head[1])
	if _, err := io.ReadFull(br, methods); err != nil || !bytes.Contains(methods, []byte{0}) {
		_, _ = c.Write([]byte{5, 0xff})
		return
	}
	if _, err := c.Write([]byte{5, 0}); err != nil {
		return
	}
	req := make([]byte, 4)
	if _, err := io.ReadFull(br, req); err != nil || req[0] != 5 || req[1] != 1 {
		return
	}
	var host string
	switch req[3] {
	case 1:
		b := make([]byte, 4)
		if _, err := io.ReadFull(br, b); err != nil {
			return
		}
		host = net.IP(b).String()
	case 3:
		n, err := br.ReadByte()
		if err != nil {
			return
		}
		b := make([]byte, n)
		if _, err := io.ReadFull(br, b); err != nil {
			return
		}
		host = string(b)
	case 4:
		b := make([]byte, 16)
		if _, err := io.ReadFull(br, b); err != nil {
			return
		}
		host = net.IP(b).String()
	default:
		return
	}
	pb := make([]byte, 2)
	if _, err := io.ReadFull(br, pb); err != nil {
		return
	}
	up, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(pb)))), 10*time.Second)
	if err != nil {
		_, _ = c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer up.Close()
	p.conns.Add(1)
	if _, err := c.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
		return
	}
	pipe(c, br, up)
}

// TestManualProxies: downloads go through a manual HTTP proxy (CONNECT to an https
// origin, HTTP/2 inside the tunnel) and through a SOCKS5 proxy. Loopback targets are
// proxied only because the bypass list says <-loopback>.
func TestManualProxies(t *testing.T) {
	t.Parallel()
	const size = 512 << 10
	ts := newServer(t, testserver.Options{Size: size, Seed: 71, ETag: `"x"`, HTTP2: true})
	h := newHarness(t)
	ca := filepath.Join(h.root, "origin-ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	p := h.startHooksDaemon(map[string]string{envHookRootCA: ca})

	httpProxy := newConnectProxy(t)
	h.settings(func(s *downexv1.Settings) {
		s.Proxy = &downexv1.ProxySettings{Mode: downexv1.ProxyMode_PROXY_MODE_MANUAL,
			HttpsProxy: httpProxy.addr(), Bypass: "<-loopback>"}
	})
	d := h.addTo(ts.URL+"/through-http-proxy.bin", 0, 4)
	h.waitCompleted(d.GetId())
	checkFile(t, filepath.Join(h.out, h.get(d.GetId()).GetFilename()), 71, size)
	if httpProxy.tunnels.Load() == 0 {
		t.Fatal("the download did not go through the HTTP proxy")
	}

	socks := newSOCKS5Proxy(t)
	h.settings(func(s *downexv1.Settings) {
		s.Proxy = &downexv1.ProxySettings{Mode: downexv1.ProxyMode_PROXY_MODE_MANUAL,
			SocksProxy: "socks5://" + socks.addr(), Bypass: "<-loopback>"}
	})
	before := httpProxy.tunnels.Load()
	d = h.addTo(ts.URL+"/through-socks.bin", 0, 4)
	h.waitCompleted(d.GetId())
	checkFile(t, filepath.Join(h.out, h.get(d.GetId()).GetFilename()), 71, size)
	if socks.conns.Load() == 0 {
		t.Fatal("the download did not go through the SOCKS5 proxy")
	}
	if httpProxy.tunnels.Load() != before {
		t.Error("the HTTP proxy was used after the settings named only SOCKS")
	}

	// Without <-loopback> a loopback origin is reached directly.
	h.settings(func(s *downexv1.Settings) {
		s.Proxy = &downexv1.ProxySettings{Mode: downexv1.ProxyMode_PROXY_MODE_MANUAL, SocksProxy: "socks5://" + socks.addr()}
	})
	n := socks.conns.Load()
	d = h.addTo(ts.URL+"/direct.bin", 0, 2)
	h.waitCompleted(d.GetId())
	if socks.conns.Load() != n {
		t.Error("a loopback origin went through the proxy without <-loopback>")
	}
	h.stop(p)
}

func (p *socks5Proxy) addr() string { return p.ln.Addr().String() }

// digestServer serves content after RFC 7616 Digest authentication (MD5, qop=auth) of
// user/password; anything else gets 401 with a challenge.
type digestServer struct {
	*httptest.Server
	user, password, realm, nonce string
	content                      []byte
	authorized, refused          atomic.Int64
}

func newDigestServer(t *testing.T, user, password string, content []byte) *digestServer {
	t.Helper()
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	s := &digestServer{user: user, password: password, realm: "downex-e2e", nonce: hex.EncodeToString(b), content: content}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

func md5hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func (s *digestServer) serve(w http.ResponseWriter, r *http.Request) {
	if !s.valid(r) {
		s.refused.Add(1)
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Digest realm="%s", qop="auth", algorithm=MD5, nonce="%s", opaque="e2e"`, s.realm, s.nonce))
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	s.authorized.Add(1)
	w.Header().Set("ETag", `"digest"`)
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(s.content))
}

// valid checks the request's Digest response.
func (s *digestServer) valid(r *http.Request) bool {
	v, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Digest ")
	if !ok {
		return false
	}
	p := map[string]string{}
	for _, part := range splitParams(v) {
		k, val, _ := strings.Cut(part, "=")
		p[strings.ToLower(strings.TrimSpace(k))] = strings.Trim(strings.TrimSpace(val), `"`)
	}
	if p["username"] != s.user || p["realm"] != s.realm || p["nonce"] != s.nonce || p["qop"] != "auth" || p["uri"] != r.URL.RequestURI() {
		return false
	}
	ha1 := md5hex(s.user + ":" + s.realm + ":" + s.password)
	ha2 := md5hex(r.Method + ":" + p["uri"])
	return p["response"] == md5hex(ha1+":"+s.nonce+":"+p["nc"]+":"+p["cnonce"]+":auth:"+ha2)
}

// splitParams splits a Digest parameter list at the commas outside quotes.
func splitParams(v string) []string {
	var out []string
	quoted, start := false, 0
	for i := 0; i < len(v); i++ {
		switch v[i] {
		case '"':
			quoted = !quoted
		case ',':
			if !quoted {
				out = append(out, v[start:i])
				start = i + 1
			}
		}
	}
	return append(out, v[start:])
}

// TestSiteLoginDigest: a download from a server that wants Digest authentication
// completes with a site login whose password was stored with SetSecret, and fails with
// auth_required with the wrong password.
func TestSiteLoginDigest(t *testing.T) {
	t.Parallel()
	content := make([]byte, 300<<10)
	_, _ = rand.Read(content)
	srv := newDigestServer(t, "alice", "correct horse", content)
	h := newHarness(t)
	p := h.startDaemon()
	h.waitReady(p)

	s := h.settings(func(s *downexv1.Settings) {
		s.SiteLogins = []*downexv1.SiteLogin{{UrlPattern: srv.URL + "/*", Username: "alice"}}
	})
	if len(s.GetSiteLogins()) != 1 || s.GetSiteLogins()[0].GetId() == "" || s.GetSiteLogins()[0].GetHasPassword() {
		t.Fatalf("site logins = %v", s.GetSiteLogins())
	}
	id := s.GetSiteLogins()[0].GetId()
	setPassword := func(pw string) {
		t.Helper()
		call(t, "set secret", func(ctx context.Context) (*connect.Response[downexv1.SetSecretResponse], error) {
			return h.dm.SetSecret(ctx, connect.NewRequest(&downexv1.SetSecretRequest{Kind: "site_login", Id: id, Password: pw}))
		})
	}

	// Without a password the server refuses the download.
	d := h.addTo(srv.URL+"/no-password.bin", 0, 2)
	if f := h.waitFailed(d.GetId()); f.GetErrorCode() != "auth_required" {
		t.Fatalf("without a password: %v, want auth_required", f)
	}

	setPassword("correct horse")
	if got := h.settings(func(*downexv1.Settings) {}).GetSiteLogins(); len(got) != 1 || !got[0].GetHasPassword() {
		t.Fatalf("after SetSecret, site logins = %v", got)
	}
	d = h.addTo(srv.URL+"/protected.bin", 0, 4)
	h.waitCompleted(d.GetId())
	got, err := os.ReadFile(filepath.Join(h.out, h.get(d.GetId()).GetFilename()))
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("protected download: %d bytes, %v; want the served content", len(got), err)
	}
	if srv.authorized.Load() == 0 {
		t.Fatal("no request was authorized")
	}

	setPassword("wrong")
	d = h.addTo(srv.URL+"/wrong.bin", 0, 2)
	if f := h.waitFailed(d.GetId()); f.GetErrorCode() != "auth_required" || strings.Contains(f.GetError(), "wrong") {
		t.Fatalf("with the wrong password: %v, want auth_required without the password", f)
	}
	if log := h.daemonLog(); strings.Contains(log, "correct horse") {
		t.Error("the daemon log contains a password")
	}
	h.stop(p)
}
