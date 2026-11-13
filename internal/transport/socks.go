package transport

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

// SOCKS protocol constants (RFC 1928, RFC 1929; SOCKS4/4a per the de-facto spec).
const (
	socks5Version = 0x05
	socks4Version = 0x04

	socks5AuthNone     = 0x00
	socks5AuthUserPass = 0x02
	socks5AuthNoneOK   = 0x00
	socks5NoAcceptable = 0xff
	socks5AuthVersion  = 0x01

	socksCmdConnect = 0x01

	socks5AddrIPv4   = 0x01
	socks5AddrDomain = 0x03
	socks5AddrIPv6   = 0x04

	socks4Granted = 0x5a
)

// dialSOCKS connects through the SOCKS proxy p to target ("host:port"). For SOCKS5 and
// SOCKS4a the host name is sent to the proxy to resolve; for SOCKS4 it is resolved
// locally to an IPv4 address. The whole exchange is bounded by timeout. login (when
// non-zero) overrides p's own credentials for SOCKS5 username/password auth.
func dialSOCKS(ctx context.Context, d *net.Dialer, p Proxy, target string, timeout time.Duration, login Login) (net.Conn, error) {
	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		return nil, fmt.Errorf("transport: socks target %q: %w", target, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("transport: socks target port %q", portStr)
	}
	dctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := d.DialContext(dctx, "tcp", p.Addr)
	if err != nil {
		return nil, fmt.Errorf("transport: dial socks proxy %s: %w", p.String(), err)
	}
	if dl, ok := dctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	user, pass := login.Username, login.Password
	if !login.hasPassword() {
		user, pass = p.Username, p.Password
	}
	ok := false
	defer func() {
		if !ok {
			conn.Close()
		}
	}()
	switch p.Kind {
	case ProxySOCKS5:
		if err := socks5Handshake(conn, host, port, user, pass); err != nil {
			return nil, err
		}
	case ProxySOCKS4, ProxySOCKS4A:
		if err := socks4Handshake(conn, p.Kind == ProxySOCKS4A, host, port, user); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("transport: %s is not a SOCKS proxy", p.String())
	}
	_ = conn.SetDeadline(time.Time{})
	ok = true
	return conn, nil
}

func socks5Handshake(conn net.Conn, host string, port int, user, pass string) error {
	methods := []byte{socks5AuthNone}
	if user != "" || pass != "" {
		methods = []byte{socks5AuthUserPass, socks5AuthNone}
	}
	greet := append([]byte{socks5Version, byte(len(methods))}, methods...)
	if _, err := conn.Write(greet); err != nil {
		return fmt.Errorf("transport: socks5 greeting: %w", err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		return fmt.Errorf("transport: socks5 method reply: %w", err)
	}
	if reply[0] != socks5Version {
		return errors.New("transport: socks5 proxy spoke the wrong version")
	}
	switch reply[1] {
	case socks5AuthNoneOK:
	case socks5AuthUserPass:
		if err := socks5UserPass(conn, user, pass); err != nil {
			return err
		}
	case socks5NoAcceptable:
		return fmt.Errorf("transport: %w: socks5 proxy rejected the offered authentication", ErrAuthRequired)
	default:
		return fmt.Errorf("transport: socks5 proxy asked for unsupported authentication %#x", reply[1])
	}
	req := []byte{socks5Version, socksCmdConnect, 0x00}
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			req = append(req, socks5AddrIPv4)
			req = append(req, v4...)
		} else {
			req = append(req, socks5AddrIPv6)
			req = append(req, ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			return errors.New("transport: socks5 host name too long")
		}
		req = append(req, socks5AddrDomain, byte(len(host)))
		req = append(req, host...)
	}
	req = binary.BigEndian.AppendUint16(req, uint16(port))
	if _, err := conn.Write(req); err != nil {
		return fmt.Errorf("transport: socks5 connect: %w", err)
	}
	return socks5ReadReply(conn)
}

func socks5UserPass(conn net.Conn, user, pass string) error {
	if len(user) > 255 || len(pass) > 255 {
		return errors.New("transport: socks5 username or password too long")
	}
	msg := []byte{socks5AuthVersion, byte(len(user))}
	msg = append(msg, user...)
	msg = append(msg, byte(len(pass)))
	msg = append(msg, pass...)
	if _, err := conn.Write(msg); err != nil {
		return fmt.Errorf("transport: socks5 auth: %w", err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		return fmt.Errorf("transport: socks5 auth reply: %w", err)
	}
	if reply[1] != 0 {
		return fmt.Errorf("transport: %w: socks5 username/password rejected", ErrAuthRequired)
	}
	return nil
}

func socks5ReadReply(conn net.Conn) error {
	head := make([]byte, 4)
	if _, err := io.ReadFull(conn, head); err != nil {
		return fmt.Errorf("transport: socks5 connect reply: %w", err)
	}
	if head[0] != socks5Version {
		return errors.New("transport: socks5 proxy spoke the wrong version")
	}
	if head[1] != 0x00 {
		return fmt.Errorf("transport: socks5 connect failed (code %#x)", head[1])
	}
	var n int
	switch head[3] {
	case socks5AddrIPv4:
		n = 4
	case socks5AddrIPv6:
		n = 16
	case socks5AddrDomain:
		l := make([]byte, 1)
		if _, err := io.ReadFull(conn, l); err != nil {
			return fmt.Errorf("transport: socks5 reply: %w", err)
		}
		n = int(l[0])
	default:
		return errors.New("transport: socks5 reply has an unknown address type")
	}
	if _, err := io.ReadFull(conn, make([]byte, n+2)); err != nil {
		return fmt.Errorf("transport: socks5 reply: %w", err)
	}
	return nil
}

func socks4Handshake(conn net.Conn, remoteDNS bool, host string, port int, user string) error {
	var ip net.IP
	var sendHost bool
	if v4 := net.ParseIP(host).To4(); v4 != nil {
		ip = v4
	} else if remoteDNS {
		// SOCKS4a: 0.0.0.x marks a host name that follows the request.
		ip = net.IPv4(0, 0, 0, 1).To4()
		sendHost = true
	} else {
		addrs, err := net.DefaultResolver.LookupIP(context.Background(), "ip4", host)
		if err != nil || len(addrs) == 0 {
			return fmt.Errorf("transport: socks4 cannot resolve %q", host)
		}
		ip = addrs[0].To4()
		if ip == nil {
			return fmt.Errorf("transport: socks4 needs an IPv4 address for %q", host)
		}
	}
	req := []byte{socks4Version, socksCmdConnect}
	req = binary.BigEndian.AppendUint16(req, uint16(port))
	req = append(req, ip...)
	req = append(req, []byte(user)...)
	req = append(req, 0x00)
	if sendHost {
		if len(host) > 255 {
			return errors.New("transport: socks4a host name too long")
		}
		req = append(req, []byte(host)...)
		req = append(req, 0x00)
	}
	if _, err := conn.Write(req); err != nil {
		return fmt.Errorf("transport: socks4 connect: %w", err)
	}
	reply := make([]byte, 8)
	if _, err := io.ReadFull(conn, reply); err != nil {
		return fmt.Errorf("transport: socks4 reply: %w", err)
	}
	if reply[1] != socks4Granted {
		return fmt.Errorf("transport: socks4 connect failed (code %#x)", reply[1])
	}
	return nil
}
