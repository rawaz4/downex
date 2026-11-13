package transport

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"downex/internal/model"
)

// socks4Server is a minimal SOCKS4/4a server (CONNECT only) for tests. It records the
// target (as an IP:port, or "host:port" for a SOCKS4a name) and the USERID.
type socks4Server struct {
	ln net.Listener

	mu      sync.Mutex
	targets []string
	userIDs []string
	wg      sync.WaitGroup
}

func startSOCKS4(t *testing.T) *socks4Server {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &socks4Server{ln: ln}
	s.wg.Add(1)
	go s.acceptLoop()
	t.Cleanup(func() {
		ln.Close()
		s.wg.Wait()
	})
	return s
}

func (s *socks4Server) addr() string { return s.ln.Addr().String() }

func (s *socks4Server) seenTargets() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.targets...)
}

func (s *socks4Server) seenUserIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.userIDs...)
}

func (s *socks4Server) acceptLoop() {
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

func readCString(br *bufio.Reader) (string, error) {
	var b []byte
	for {
		c, err := br.ReadByte()
		if err != nil {
			return "", err
		}
		if c == 0 {
			return string(b), nil
		}
		b = append(b, c)
	}
}

func (s *socks4Server) serve(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	br := bufio.NewReader(c)
	head := make([]byte, 8)
	if _, err := io.ReadFull(br, head); err != nil {
		return
	}
	if head[0] != 4 || head[1] != 1 {
		return
	}
	port := binary.BigEndian.Uint16(head[2:4])
	ip := net.IP(head[4:8])
	userID, err := readCString(br)
	if err != nil {
		return
	}
	var target string
	// SOCKS4a: 0.0.0.x (x != 0) is followed by the host name.
	if ip[0] == 0 && ip[1] == 0 && ip[2] == 0 && ip[3] != 0 {
		host, err := readCString(br)
		if err != nil {
			return
		}
		target = net.JoinHostPort(host, strconv.Itoa(int(port)))
	} else {
		target = net.JoinHostPort(ip.String(), strconv.Itoa(int(port)))
	}
	s.mu.Lock()
	s.targets = append(s.targets, target)
	s.userIDs = append(s.userIDs, userID)
	s.mu.Unlock()

	// Grant reply: VN=0, CD=0x5a, then port and ip (ignored by clients).
	if _, err := c.Write([]byte{0, 0x5a, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}
	// Dial the real target and splice.
	dialTarget := target
	if host, _, err := net.SplitHostPort(target); err == nil {
		if net.ParseIP(host) == nil {
			// SOCKS4a name: resolve locally for the test splice.
			dialTarget = target
		}
	}
	up, err := net.DialTimeout("tcp", dialTarget, 5*time.Second)
	if err != nil {
		return
	}
	defer up.Close()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(up, br); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, up); done <- struct{}{} }()
	<-done
}

func TestSOCKS4Proxy(t *testing.T) {
	cs := newContentServer(20_000)
	ts := startServer(t, kindHTTP1, cs)
	socks := startSOCKS4(t)

	opts := DefaultHTTPOptions()
	opts.Proxy = "socks4://user42@" + socks.addr()
	slot := newTestSlot(t, opts, ts)
	resp, err := slot.Fetch(testCtx(t), RangeRequest{Request: model.Request{URL: ts.URL}, Start: 10, End: 509})
	if err != nil {
		t.Fatal(err)
	}
	if got := readBody(t, resp); !bytes.Equal(got, cs.content[10:510]) {
		t.Fatal("body mismatch through SOCKS4")
	}
	targets := socks.seenTargets()
	if len(targets) != 1 || targets[0] != ts.Listener.Addr().String() {
		t.Fatalf("SOCKS4 targets = %v, want [%s] (IPv4 resolved locally)", targets, ts.Listener.Addr())
	}
	if ids := socks.seenUserIDs(); len(ids) != 1 || ids[0] != "user42" {
		t.Fatalf("SOCKS4 userIDs = %v, want [user42]", ids)
	}
}

func TestSOCKS4aProxyResolvesByProxy(t *testing.T) {
	cs := newContentServer(5_000)
	ts := startServer(t, kindHTTP1, cs)
	socks := startSOCKS4(t)
	_, port, _ := net.SplitHostPort(ts.Listener.Addr().String())

	opts := DefaultHTTPOptions()
	opts.Proxy = "socks4a://" + socks.addr()
	slot := newTestSlot(t, opts)
	resp, err := slot.Fetch(testCtx(t), RangeRequest{Request: model.Request{URL: "http://localhost:" + port + "/"}, End: -1})
	if err != nil {
		t.Fatal(err)
	}
	readBody(t, resp)
	if targets := socks.seenTargets(); len(targets) != 1 || targets[0] != "localhost:"+port {
		t.Fatalf("SOCKS4a targets = %v, want the unresolved name localhost:%s", targets, port)
	}
}
