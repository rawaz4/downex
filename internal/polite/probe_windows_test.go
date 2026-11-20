//go:build windows

package polite

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"
)

// IcmpSendEcho2/Icmp6SendEcho2 answer from the loopback addresses (the structure layouts
// and status offsets are right), quickly.
func TestICMPLoopbackWindows(t *testing.T) {
	for _, a := range []string{"127.0.0.1", "::1"} {
		t.Run(a, func(t *testing.T) {
			p := NewSystemProber()
			defer p.Close()
			rtt, err := p.ProbeICMP(context.Background(), netip.MustParseAddr(a))
			if err != nil {
				t.Fatalf("ProbeICMP(%s): %v", a, err)
			}
			if rtt <= 0 || rtt > 50*time.Millisecond {
				t.Fatalf("rtt = %v, want (0, 50ms]", rtt)
			}
		})
	}
}

// An address nobody answers (TEST-NET-1, RFC 5737) gives no sample within the timeout:
// no reply, or unreachable when this machine has no route.
func TestICMPBlackholeWindows(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the probe timeout")
	}
	start := time.Now()
	_, err := NewSystemProber().ProbeICMP(context.Background(), netip.MustParseAddr("192.0.2.1"))
	if !errors.Is(err, ErrNoReply) && !errors.Is(err, ErrUnreachable) {
		t.Fatalf("err = %v, want ErrNoReply or ErrUnreachable", err)
	}
	if el := time.Since(start); el > 1200*time.Millisecond {
		t.Fatalf("took %v, want at most 1.2 s", el)
	}
}
