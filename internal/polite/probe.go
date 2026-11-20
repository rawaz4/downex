package polite

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"runtime"
	"syscall"
	"time"
)

// Prober sends the probes of docs/POLITE-SPEC.md rule 4. Both methods return the round
// trip measured with Go's monotonic clock, and are bounded by ProbeTimeout (an answer
// after it is ErrNoReply). Implementations must be safe for concurrent use.
type Prober interface {
	// ProbeICMP sends one ICMP echo request with a 32-byte zero payload to addr.
	ProbeICMP(ctx context.Context, addr netip.Addr) (time.Duration, error)
	// ProbeTCP opens a TCP connection to addr and resets it at once (SetLinger(0) and
	// Close), so no request is ever sent. A refused connection is a sample too: it is
	// returned without an error.
	ProbeTCP(ctx context.Context, addr netip.AddrPort) (time.Duration, error)
	// Close releases the prober's resources.
	Close() error
}

// Probe failures. None of them gives a sample.
var (
	// ErrNoReply: no answer within ProbeTimeout.
	ErrNoReply = errors.New("polite: no reply")
	// ErrUnreachable: the network or host answered that the address cannot be reached.
	ErrUnreachable = errors.New("polite: unreachable")
	// ErrUnsupported: this system cannot send ICMP without privileges; TCP probes only.
	ErrUnsupported = errors.New("polite: ICMP is not available")
)

// echoPayload is the ICMP echo payload (rule 4).
const echoPayload = 32

// NewSystemProber returns the system's prober: ICMP through IcmpSendEcho2 on Windows and
// unprivileged datagram ICMP sockets on macOS and Linux (ErrUnsupported elsewhere, and
// where the system refuses them), TCP connects everywhere. It needs no privileges.
func NewSystemProber() Prober { return systemProber{} }

type systemProber struct{}

func (systemProber) ProbeICMP(ctx context.Context, addr netip.Addr) (time.Duration, error) {
	return probeICMP(ctx, addr.Unmap())
}

func (systemProber) ProbeTCP(ctx context.Context, addr netip.AddrPort) (time.Duration, error) {
	return probeTCP(ctx, addr)
}

func (systemProber) Close() error { return nil }

// probeTCP times a TCP connect to addr (rule 4): never through a proxy (addr already is
// the proxy when downloads use one), reset at once with SetLinger(0) so that no request
// is sent and no TIME_WAIT is left. A refused connection (RST) is a sample.
func probeTCP(ctx context.Context, addr netip.AddrPort) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()
	d := net.Dialer{Timeout: ProbeTimeout, KeepAlive: -1}
	elapsed := stopwatch()
	conn, err := d.DialContext(ctx, "tcp", addr.String())
	rtt := elapsed()
	if err != nil {
		switch {
		case isConnRefused(err):
			return rtt, nil
		case errors.Is(err, context.DeadlineExceeded), isTimeout(err):
			if ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return 0, ctx.Err()
			}
			return 0, ErrNoReply
		case isUnreachable(err):
			return 0, ErrUnreachable
		}
		return 0, err
	}
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetLinger(0)
	}
	_ = conn.Close()
	return rtt, nil
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// Windows socket error numbers (syscall's ECONNREFUSED and friends are POSIX values).
const (
	wsaENetUnreach  = syscall.Errno(10051)
	wsaConnRefused  = syscall.Errno(10061)
	wsaEHostUnreach = syscall.Errno(10065)
)

func isConnRefused(err error) bool { return isSocketErr(err, syscall.ECONNREFUSED, wsaConnRefused) }

func isUnreachable(err error) bool {
	return isSocketErr(err, syscall.ENETUNREACH, wsaENetUnreach) || isSocketErr(err, syscall.EHOSTUNREACH, wsaEHostUnreach)
}

func isSocketErr(err error, posix, windows syscall.Errno) bool {
	if runtime.GOOS == "windows" {
		var errno syscall.Errno
		return errors.As(err, &errno) && errno == windows
	}
	return errors.Is(err, posix)
}
