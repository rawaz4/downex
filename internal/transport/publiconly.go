package transport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"syscall"
)

// ErrLocalAddress refuses a direct connection to this computer or a local network (a
// PublicOnly slot's request whose host name resolved to such an address, or redirected to one).
var ErrLocalAddress = errors.New("transport: the address is on this computer or a local network")

// publicKey is the context key under which a PublicOnly slot marks its requests.
type publicKey struct{}

// PublicOnly returns a Factory whose slots connect directly only to public addresses: a host
// name that resolves to a local address (LocalAddress) is refused when it is dialed (redirects included), whatever its name says
// (docs/SITE-MEDIA-SPEC.md §2.9 "Limits"). Connections through a proxy are not affected: the
// proxy resolves the target.
func PublicOnly(f Factory) Factory {
	if f == nil {
		return nil
	}
	return publicFactory{f: f}
}

type publicFactory struct{ f Factory }

func (p publicFactory) NewSlot() Fetcher { return publicFetcher{Fetcher: p.f.NewSlot()} }

type publicFetcher struct{ Fetcher }

func (p publicFetcher) Fetch(ctx context.Context, req RangeRequest) (*Response, error) {
	if ctx != nil {
		ctx = context.WithValue(ctx, publicKey{}, true)
	}
	return p.Fetcher.Fetch(ctx, req)
}

// publicOnly reports a dial for a PublicOnly slot's request.
func publicOnly(ctx context.Context) bool {
	v, _ := ctx.Value(publicKey{}).(bool)
	return v
}

// LocalAddress reports an address of this computer or a local network: loopback, private,
// link-local, unspecified, multicast, shared (carrier-grade NAT and tailnets), benchmarking,
// IETF-reserved, future-use, broadcast, IPv6 site-local, Teredo or documentation; or an IPv6
// address embedding an IPv4 address that is one (IPv4-mapped, IPv4-compatible, NAT64 and
// 6to4), since a gateway may reach it (docs/SITE-MEDIA-SPEC.md §2.9 "Limits").
func LocalAddress(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() || ip.IsInterfaceLocalMulticast() {
		return true
	}
	for _, p := range localPrefixes {
		if p.Contains(ip) {
			return true
		}
	}
	if v4, ok := embeddedIPv4(ip); ok {
		return LocalAddress(v4)
	}
	return false
}

// localPrefixes are the special-purpose ranges LocalAddress refuses beyond the net/netip
// predicates.
var localPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("fec0::/10"),
	netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("2001:db8::/32"),
}

// embeddedPrefixes are the IPv6 ranges that carry an IPv4 address, with the byte offset of it.
var embeddedPrefixes = []struct {
	p   netip.Prefix
	off int
}{
	{netip.MustParsePrefix("64:ff9b::/96"), 12},   // NAT64 (RFC 6052)
	{netip.MustParsePrefix("64:ff9b:1::/48"), 12}, // local-use NAT64 (RFC 8215)
	{netip.MustParsePrefix("2002::/16"), 2},       // 6to4 (RFC 3056)
	{netip.MustParsePrefix("::/96"), 12},          // IPv4-compatible (deprecated)
}

// embeddedIPv4 is the IPv4 address an IPv6 address carries, if it is in such a range.
func embeddedIPv4(ip netip.Addr) (netip.Addr, bool) {
	if !ip.Is6() {
		return netip.Addr{}, false
	}
	b := ip.As16()
	for _, e := range embeddedPrefixes {
		if e.p.Contains(ip) {
			return netip.AddrFrom4([4]byte(b[e.off : e.off+4])), true
		}
	}
	return netip.Addr{}, false
}

// refuseLocal is a net.Dialer Control that refuses a connection to a local address (it
// runs after the name was resolved, for each address tried).
func refuseLocal(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("%w (%s)", ErrLocalAddress, address)
	}
	if LocalAddress(ap.Addr()) {
		return fmt.Errorf("%w (%s)", ErrLocalAddress, ap.Addr())
	}
	return nil
}

// publicDialer is d refusing local addresses.
func publicDialer(d *net.Dialer) *net.Dialer {
	c := *d
	c.Control = refuseLocal
	return &c
}
