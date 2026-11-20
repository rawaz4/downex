package polite

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"runtime"
	"testing"
	"time"
)

// A TCP probe connects and resets at once: the listener sees a connection that sends no
// byte, and the probe returns its connect time.
func TestTCPProbeSendsNothing(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan int, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			got <- -1
			return
		}
		defer c.Close()
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, _ := io.Copy(io.Discard, c) // EOF or reset: the probe closed it
		got <- int(n)
	}()
	addr := netip.MustParseAddrPort(ln.Addr().String())
	rtt, err := NewSystemProber().ProbeTCP(context.Background(), addr)
	if err != nil {
		t.Fatalf("ProbeTCP: %v", err)
	}
	if rtt <= 0 || rtt > time.Second {
		t.Fatalf("rtt = %v", rtt)
	}
	select {
	case n := <-got:
		if n != 0 {
			t.Fatalf("the listener read %d bytes, want 0", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the listener saw no connection")
	}
}

// A refused connection is a sample (the reset is the server's answer), except where the
// system retries a refused connect past the probe timeout (Windows retries SYNs on a
// reset), which is then no reply.
func TestTCPProbeRefused(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := netip.MustParseAddrPort(ln.Addr().String())
	ln.Close() // nothing listens there now
	rtt, err := NewSystemProber().ProbeTCP(context.Background(), addr)
	switch {
	case err == nil:
		if rtt <= 0 || rtt > ProbeTimeout {
			t.Fatalf("refused sample rtt = %v", rtt)
		}
	case runtime.GOOS == "windows" && errors.Is(err, ErrNoReply):
	default:
		t.Fatalf("ProbeTCP to a closed port: %v", err)
	}
}

// A cancelled context ends a probe.
func TestTCPProbeCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewSystemProber().ProbeTCP(ctx, netip.MustParseAddrPort("192.0.2.1:443")); err == nil {
		t.Fatal("a cancelled probe succeeded")
	}
}
