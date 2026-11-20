//go:build darwin || linux

package polite

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"
)

// An unprivileged datagram ICMP echo to the loopback address answers; a system that
// refuses such sockets (Linux outside ping_group_range) reports ErrUnsupported.
func TestICMPLoopbackUnix(t *testing.T) {
	rtt, err := NewSystemProber().ProbeICMP(context.Background(), netip.MustParseAddr("127.0.0.1"))
	if errors.Is(err, ErrUnsupported) {
		t.Skip("datagram ICMP sockets are not allowed here:", err)
	}
	if err != nil {
		t.Fatalf("ProbeICMP: %v", err)
	}
	if rtt <= 0 || rtt > 50*time.Millisecond {
		t.Fatalf("rtt = %v", rtt)
	}
}

func TestStripIPv4Header(t *testing.T) {
	icmp := []byte{0, 0, 0xff, 0xff, 0, 1, 0, 1}
	hdr := append([]byte{0x45}, make([]byte, 19)...)
	if got := stripIPv4Header(append(hdr, icmp...), protoICMP); string(got) != string(icmp) {
		t.Fatalf("with header: %x", got)
	}
	if got := stripIPv4Header(icmp, protoICMP); string(got) != string(icmp) {
		t.Fatalf("without header: %x", got)
	}
	if got := stripIPv4Header(append(hdr, icmp...), protoICMPv6); len(got) != 28 {
		t.Fatalf("ICMPv6 must be left alone: %x", got)
	}
}
