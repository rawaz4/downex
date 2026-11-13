package transport

import (
	"context"
	"net"
)

// IP-family pinning (docs/SITE-MEDIA-SPEC.md §2.4). A site may bind a signed URL to the
// address family of the client that resolved it (a googlevideo URL's "ip" parameter):
// its requests must then leave over that family, or the server refuses them.

// networkKey is the context key under which a WithNetwork slot hands its address family
// to the dial of its requests.
type networkKey struct{}

// WithNetwork returns a Factory whose slots dial direct connections only over network,
// "tcp4" or "tcp6"; "" (or any other value) returns f itself. Connections through a
// proxy are not affected: the proxy dials the target, so the family of the connection
// to the proxy says nothing about the address the origin sees.
//
// The family travels in the context of every Fetch of the slot, which the dial of an
// HTTPFactory slot reads. When factories are nested, the outermost WithNetwork wins.
func WithNetwork(f Factory, network string) Factory {
	switch network {
	case "tcp4", "tcp6":
	default:
		return f
	}
	if f == nil {
		return nil
	}
	return networkFactory{f: f, network: network}
}

type networkFactory struct {
	f       Factory
	network string
}

func (n networkFactory) NewSlot() Fetcher {
	return networkFetcher{Fetcher: n.f.NewSlot(), network: n.network}
}

// networkFetcher marks the context of every Fetch with its address family.
type networkFetcher struct {
	Fetcher
	network string
}

func (n networkFetcher) Fetch(ctx context.Context, req RangeRequest) (*Response, error) {
	if ctx != nil {
		if _, set := ctx.Value(networkKey{}).(string); !set {
			ctx = context.WithValue(ctx, networkKey{}, n.network)
		}
	}
	return n.Fetcher.Fetch(ctx, req)
}

// dialNetwork is the network a dial for ctx uses in place of network: the family a
// WithNetwork slot pinned, when network is the family-neutral "tcp".
func dialNetwork(ctx context.Context, network string) string {
	if network != "tcp" {
		return network
	}
	if pinned, ok := ctx.Value(networkKey{}).(string); ok {
		return pinned
	}
	return network
}

// pinnedDial wraps a slot's dial so that a direct connection uses the family pinned in
// the request's context. The router passes network on only to direct dials; it dials
// proxies itself (always "tcp"), so proxied connections keep any family.
func pinnedDial(dial func(ctx context.Context, network, addr string) (net.Conn, error)) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		return dial(ctx, dialNetwork(ctx, network), addr)
	}
}
