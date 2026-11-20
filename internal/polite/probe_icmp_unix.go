//go:build darwin || linux

package polite

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// The IANA protocol numbers icmp.ParseMessage takes.
const (
	protoICMP   = 1
	protoICMPv6 = 58
)

// echoSeq numbers the echo requests of this process.
var echoSeq atomic.Uint32

// probeICMP sends one echo request over an unprivileged datagram ICMP socket
// (socket(AF_INET, SOCK_DGRAM, IPPROTO_ICMP) and its ICMPv6 counterpart, through
// golang.org/x/net/icmp): no privileges, no raw socket. Each probe has its own socket, so
// the kernel delivers it only replies to its own identifier on Linux (which rewrites the
// identifier); the reply is matched by peer and sequence (the identifier is not checked,
// so that a kernel that rewrites it never loses a reply). A
// system that refuses such sockets (EACCES or EPERM: Linux outside ping_group_range)
// yields ErrUnsupported.
func probeICMP(ctx context.Context, addr netip.Addr) (time.Duration, error) {
	network, laddr, proto := "udp4", "0.0.0.0", protoICMP
	var typ icmp.Type = ipv4.ICMPTypeEcho
	if addr.Is6() {
		network, laddr, proto = "udp6", "::", protoICMPv6
		typ = ipv6.ICMPTypeEchoRequest
	}
	conn, err := icmp.ListenPacket(network, laddr)
	if err != nil {
		if errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) ||
			errors.Is(err, syscall.EPROTONOSUPPORT) || errors.Is(err, syscall.EAFNOSUPPORT) {
			return 0, ErrUnsupported
		}
		return 0, err
	}
	defer conn.Close()
	deadline := time.Now().Add(ProbeTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()

	id := os.Getpid() & 0xffff
	seq := int(echoSeq.Add(1) & 0xffff)
	msg := icmp.Message{Type: typ, Body: &icmp.Echo{ID: id, Seq: seq, Data: make([]byte, echoPayload)}}
	wire, err := msg.Marshal(nil)
	if err != nil {
		return 0, err
	}
	dst := &net.UDPAddr{IP: addr.AsSlice()}
	elapsed := stopwatch()
	if _, err := conn.WriteTo(wire, dst); err != nil {
		if isUnreachable(err) {
			return 0, ErrUnreachable
		}
		return 0, err
	}
	buf := make([]byte, 1500)
	for {
		n, peer, err := conn.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return 0, ctx.Err()
			}
			if isTimeout(err) {
				return 0, ErrNoReply
			}
			if isUnreachable(err) || isConnRefused(err) {
				return 0, ErrUnreachable
			}
			return 0, err
		}
		rtt := elapsed()
		if !samePeer(peer, addr) {
			continue
		}
		data := stripIPv4Header(buf[:n], proto)
		reply, err := icmp.ParseMessage(proto, data)
		if err != nil {
			continue
		}
		switch reply.Type {
		case ipv4.ICMPTypeEchoReply, ipv6.ICMPTypeEchoReply:
			e, ok := reply.Body.(*icmp.Echo)
			// Linux rewrites the identifier to the socket's; macOS keeps ours.
			if ok && e.Seq == seq {
				return rtt, nil
			}
		case ipv4.ICMPTypeDestinationUnreachable, ipv6.ICMPTypeDestinationUnreachable:
			return 0, ErrUnreachable
		}
	}
}

// stripIPv4Header removes the IPv4 header macOS may deliver in front of an ICMP message
// on a datagram socket.
func stripIPv4Header(b []byte, proto int) []byte {
	if proto != protoICMP || len(b) < 20 || b[0]>>4 != 4 {
		return b
	}
	hl := int(b[0]&0x0f) * 4
	if hl < 20 || hl > len(b) {
		return b
	}
	return b[hl:]
}

func samePeer(peer net.Addr, want netip.Addr) bool {
	var ip net.IP
	switch p := peer.(type) {
	case *net.UDPAddr:
		ip = p.IP
	case *net.IPAddr:
		ip = p.IP
	default:
		return false
	}
	got, ok := netip.AddrFromSlice(ip)
	return ok && got.Unmap() == want.Unmap()
}
