package transport

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

// proxyConnectTimeout bounds one CONNECT exchange with a proxy (net/http's own bound).
const proxyConnectTimeout = time.Minute

// tunnelError reports a proxy that refused a CONNECT tunnel with a status other than
// 407 (which is ErrAuthRequired). It is not a reason to try another route.
type tunnelError struct {
	proxy  Proxy
	status string
}

func (e *tunnelError) Error() string {
	return "transport: proxy " + e.proxy.String() + " refused the tunnel: " + e.status
}

// dialTunnel opens a CONNECT tunnel to target through the HTTP or HTTPS proxy p. A
// static or stored password login is sent as Basic before the proxy asks; a 407 is
// answered like a forwarding proxy's (Basic, Digest, NTLM/Negotiate, the
// connection-based schemes on the same connection), and a final 407 fails with
// ErrAuthRequired.
func (r *router) dialTunnel(ctx context.Context, p Proxy, target string) (net.Conn, error) {
	flow := &authFlow{
		field: "Proxy-Authenticate",
		req:   authRequest{method: http.MethodConnect, uri: target, host: p.host()},
		login: r.proxyLogin(p),
	}
	established := false
	defer func() { flow.done(established) }()
	preemptive := r.preemptive(p)

	var (
		conn net.Conn
		lr   *io.LimitedReader
		br   *bufio.Reader
	)
	for round := 0; ; round++ {
		if conn == nil {
			c, err := r.dialProxy(ctx, p)
			if err != nil {
				return nil, err
			}
			conn, lr = c, &io.LimitedReader{R: c}
			br = bufio.NewReader(lr)
		}
		auth, ok := flow.pending()
		if !ok && round == 0 {
			auth = preemptive
		}
		lr.N = r.maxHeader + drainOnAuthRetry
		resp, err := r.connect(ctx, conn, br, target, auth)
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("transport: CONNECT through proxy %s: %w", p, err)
		}
		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			_ = conn.SetDeadline(time.Time{})
			established = true
			return withBuffered(conn, br), nil
		case resp.StatusCode == http.StatusProxyAuthRequired:
			reusable := drainConnectResponse(resp)
			if round >= maxAuthRounds {
				conn.Close()
				return nil, fmt.Errorf("transport: proxy %s: %w", p, ErrAuthRequired)
			}
			challenges := resp.Header.Values("Proxy-Authenticate")
			if _, ok := flow.respond(challenges); !ok {
				conn.Close()
				return nil, fmt.Errorf("transport: proxy %s: %w", p, ErrAuthRequired)
			}
			if !reusable {
				conn.Close()
				conn = nil
				if flow.connBased() && midHandshake(challenges, flow.scheme) {
					// The proxy closed the connection its challenge belongs to.
					return nil, fmt.Errorf("transport: proxy %s closed the connection during authentication: %w", p, ErrAuthRequired)
				}
			}
		default:
			_ = resp.Body.Close()
			conn.Close()
			return nil, &tunnelError{proxy: p, status: resp.Status}
		}
	}
}

// connect writes one CONNECT request for target on conn and reads the response. The
// exchange is bounded by proxyConnectTimeout and ends with ctx.
func (r *router) connect(ctx context.Context, conn net.Conn, br *bufio.Reader, target, auth string) (*http.Response, error) {
	req := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Opaque: target},
		Host:   target,
		Header: http.Header{},
	}
	if r.userAgent != "" {
		req.Header.Set("User-Agent", r.userAgent)
	}
	if auth != "" {
		req.Header.Set("Proxy-Authorization", auth)
	}
	deadline := time.Now().Add(proxyConnectTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	defer stop()
	if err := req.Write(conn); err != nil {
		return nil, err
	}
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	return resp, nil
}

// drainConnectResponse reads and closes the body of a 407 to CONNECT and reports whether
// the connection can carry the next attempt.
func drainConnectResponse(resp *http.Response) bool {
	n, err := io.Copy(io.Discard, io.LimitReader(resp.Body, drainOnAuthRetry+1))
	_ = resp.Body.Close()
	return err == nil && n <= drainOnAuthRetry && !resp.Close
}

// midHandshake reports whether the challenge of scheme in challenges carries a token: a
// connection-based handshake is then past its first leg and bound to its connection.
func midHandshake(challenges []string, scheme string) bool {
	ch, ok := findChallenge(parseChallenges(challenges), scheme)
	return ok && ch.token != ""
}

// withBuffered returns conn, reading first what br already buffered from it (bytes a
// proxy sent after its 2xx before the client spoke).
func withBuffered(conn net.Conn, br *bufio.Reader) net.Conn {
	n := br.Buffered()
	if n == 0 {
		return conn
	}
	b, _ := br.Peek(n)
	return &bufferedConn{Conn: conn, r: io.MultiReader(bytes.NewReader(bytes.Clone(b)), conn)}
}

// bufferedConn is a net.Conn whose reads start with bytes read ahead from it.
type bufferedConn struct {
	net.Conn
	r io.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }
