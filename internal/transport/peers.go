package transport

import (
	"cmp"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// PeerTable records the remote address of every live connection a factory's slots dial
// (docs/POLITE-SPEC.md rule 1): the origin of a direct connection, else the address of
// the proxy (SOCKS, CONNECT tunnel or forwarding proxy). Polite mode probes these
// addresses. It holds no URLs: only, per address, the host names of the targets reached
// through it, so that the daemon can tell whether one of them has an engine connection
// penalty (ENGINE-SPEC §10); they are never logged nor leave the process. Probes are not
// made through it and never count as its connections. Safe for concurrent use.
type PeerTable struct {
	mu    sync.Mutex
	m     map[netip.AddrPort]*peerEntry
	now   func() time.Time
	onNew atomic.Pointer[func()]
}

type peerEntry struct {
	conns int
	first time.Time
	hosts map[string]int // live connections per target host name
}

// Peer is one remote address with live connections.
type Peer struct {
	Addr  netip.AddrPort
	Conns int
	First time.Time // when the oldest of the live connections was made
	Hosts []string  // the target host names reached through it (lower case, no port), sorted
}

// NewPeerTable returns an empty table.
func NewPeerTable() *PeerTable {
	return &PeerTable{m: map[netip.AddrPort]*peerEntry{}, now: time.Now}
}

// OnNew sets f (nil: none) to be called, outside any lock, whenever a connection is made
// to an address without other live connections.
func (t *PeerTable) OnNew(f func()) {
	if f == nil {
		t.onNew.Store(nil)
		return
	}
	t.onNew.Store(&f)
}

// Snapshot returns the addresses with live connections, ordered by address.
func (t *PeerTable) Snapshot() []Peer {
	t.mu.Lock()
	out := make([]Peer, 0, len(t.m))
	for a, e := range t.m {
		p := Peer{Addr: a, Conns: e.conns, First: e.first, Hosts: make([]string, 0, len(e.hosts))}
		for h := range e.hosts {
			p.Hosts = append(p.Hosts, h)
		}
		slices.Sort(p.Hosts)
		out = append(out, p)
	}
	t.mu.Unlock()
	slices.SortFunc(out, func(x, y Peer) int { return cmp.Compare(x.Addr.String(), y.Addr.String()) })
	return out
}

// track records conn, made for target (a routeTarget, "scheme://host:port"), until it is
// closed. A conn whose remote address is not an IP address and port is not recorded.
func (t *PeerTable) track(conn net.Conn, target string) net.Conn {
	if t == nil || conn == nil {
		return conn
	}
	addr, ok := remoteAddrPort(conn)
	if !ok {
		return conn
	}
	host := targetHost(target)
	t.mu.Lock()
	e := t.m[addr]
	isNew := e == nil
	if isNew {
		e = &peerEntry{first: t.now(), hosts: map[string]int{}}
		t.m[addr] = e
	}
	e.conns++
	if host != "" {
		e.hosts[host]++
	}
	t.mu.Unlock()
	if f := t.onNew.Load(); isNew && f != nil {
		(*f)()
	}
	return &trackedConn{Conn: conn, release: func() { t.release(addr, host) }}
}

func (t *PeerTable) release(addr netip.AddrPort, host string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.m[addr]
	if e == nil {
		return
	}
	if host != "" {
		if e.hosts[host]--; e.hosts[host] <= 0 {
			delete(e.hosts, host)
		}
	}
	if e.conns--; e.conns <= 0 {
		delete(t.m, addr)
	}
}

func remoteAddrPort(conn net.Conn) (netip.AddrPort, bool) {
	ra := conn.RemoteAddr()
	if ra == nil {
		return netip.AddrPort{}, false
	}
	if tcp, ok := ra.(*net.TCPAddr); ok {
		ap := tcp.AddrPort()
		return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port()), ap.IsValid()
	}
	ap, err := netip.ParseAddrPort(ra.String())
	if err != nil {
		return netip.AddrPort{}, false
	}
	return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port()), true
}

// targetHost returns the lower-case host name of a routeTarget, without its port.
func targetHost(target string) string {
	if i := strings.Index(target, "://"); i >= 0 {
		target = target[i+3:]
	}
	if h, _, err := net.SplitHostPort(target); err == nil {
		target = h
	}
	return strings.ToLower(target)
}

// trackedConn releases its PeerTable entry once, when it is closed.
type trackedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *trackedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}
