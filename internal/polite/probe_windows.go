//go:build windows

package polite

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The ICMP helper API of iphlpapi.dll: echo requests without privileges or raw sockets.
var (
	iphlpapi            = windows.NewLazySystemDLL("iphlpapi.dll")
	procIcmpCreateFile  = iphlpapi.NewProc("IcmpCreateFile")
	procIcmp6CreateFile = iphlpapi.NewProc("Icmp6CreateFile")
	procIcmpCloseHandle = iphlpapi.NewProc("IcmpCloseHandle")
	procIcmpSendEcho2   = iphlpapi.NewProc("IcmpSendEcho2")
	procIcmp6SendEcho2  = iphlpapi.NewProc("Icmp6SendEcho2")
)

// IP_STATUS values of the ICMP helper API.
const (
	ipSuccess           = 0
	ipDestNetUnreach    = 11002 // also IP_DEST_NO_ROUTE (IPv6)
	ipDestHostUnreach   = 11003 // also IP_DEST_ADDR_UNREACHABLE
	ipDestProtUnreach   = 11004 // also IP_DEST_PROHIBITED
	ipDestPortUnreach   = 11005
	ipReqTimedOut       = 11010
	ipTTLExpiredTransit = 11013 // also IP_HOP_LIMIT_EXCEEDED
	ipDestUnreachable   = 11040 // IPv6
)

// Win32 errors the echo calls set when no reply was copied.
const (
	errorNetworkUnreachable = syscall.Errno(1231)
	errorHostUnreachable    = syscall.Errno(1232)
)

// ipOptionInformation is IP_OPTION_INFORMATION in the process's own pointer size.
type ipOptionInformation struct {
	TTL, TOS, Flags, OptionsSize uint8
	OptionsData                  uintptr
}

// icmpEchoReply is ICMP_ECHO_REPLY in the process's own pointer size (the 64-bit layout
// in 64-bit processes, not ICMP_ECHO_REPLY32).
type icmpEchoReply struct {
	Address       uint32
	Status        uint32
	RoundTripTime uint32 // whole milliseconds only: the RTT is timed around the call instead
	DataSize      uint16
	Reserved      uint16
	Data          uintptr
	Options       ipOptionInformation
}

// icmpv6StatusOffset is where ICMPV6_ECHO_REPLY's Status lies: after the packed 26-byte
// IPV6_ADDRESS_EX, aligned to 4.
const icmpv6StatusOffset = 28

// replyBufferSize holds one reply with its 32-byte payload, 8 bytes of ICMP error data
// and the IO_STATUS_BLOCK IcmpSendEcho2 also writes there, with room to spare.
const replyBufferSize = 256

// probeICMP sends one echo request through IcmpSendEcho2 (IPv4) or Icmp6SendEcho2 (IPv6)
// synchronously (no event, no APC) with a 1000 ms timeout. The call cannot be
// interrupted, so ctx is checked only before it; the timeout bounds it. Each probe opens
// and closes its own ICMP handle.
func probeICMP(ctx context.Context, addr netip.Addr) (time.Duration, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := iphlpapi.Load(); err != nil {
		return 0, ErrUnsupported
	}
	v6 := addr.Is6()
	create := procIcmpCreateFile
	if v6 {
		create = procIcmp6CreateFile
	}
	h, _, cerr := create.Call()
	if windows.Handle(h) == windows.InvalidHandle || h == 0 {
		return 0, fmt.Errorf("polite: create ICMP handle: %w", cerr)
	}
	defer procIcmpCloseHandle.Call(h)

	payload := make([]byte, echoPayload)
	var buf [replyBufferSize / 8]uint64 // 8-byte aligned
	timeout := uintptr(ProbeTimeout / time.Millisecond)
	var n uintptr
	var callErr error
	elapsed := stopwatch()
	if v6 {
		src := windows.RawSockaddrInet6{Family: windows.AF_INET6}
		dst := windows.RawSockaddrInet6{Family: windows.AF_INET6, Addr: addr.As16()}
		n, _, callErr = procIcmp6SendEcho2.Call(h, 0, 0, 0,
			uintptr(unsafe.Pointer(&src)), uintptr(unsafe.Pointer(&dst)),
			uintptr(unsafe.Pointer(&payload[0])), uintptr(len(payload)), 0,
			uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)*8), timeout)
	} else {
		a4 := addr.As4()
		n, _, callErr = procIcmpSendEcho2.Call(h, 0, 0, 0,
			uintptr(binary.LittleEndian.Uint32(a4[:])), // IPAddr: the address bytes in memory order
			uintptr(unsafe.Pointer(&payload[0])), uintptr(len(payload)), 0,
			uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)*8), timeout)
	}
	rtt := elapsed()
	if n == 0 {
		var errno syscall.Errno
		if errors.As(callErr, &errno) {
			switch errno {
			case ipReqTimedOut:
				return 0, ErrNoReply
			case errorNetworkUnreachable, errorHostUnreachable, ipDestNetUnreach, ipDestHostUnreach,
				ipDestProtUnreach, ipDestPortUnreach, ipTTLExpiredTransit, ipDestUnreachable:
				return 0, ErrUnreachable
			}
		}
		if rtt >= ProbeTimeout {
			return 0, ErrNoReply
		}
		return 0, fmt.Errorf("polite: ICMP echo: %w", callErr)
	}
	var status uint32
	if v6 {
		status = *(*uint32)(unsafe.Add(unsafe.Pointer(&buf[0]), icmpv6StatusOffset))
	} else {
		status = (*icmpEchoReply)(unsafe.Pointer(&buf[0])).Status
	}
	switch status {
	case ipSuccess:
		return rtt, nil
	case ipReqTimedOut:
		return 0, ErrNoReply
	case ipDestNetUnreach, ipDestHostUnreach, ipDestProtUnreach, ipDestPortUnreach, ipTTLExpiredTransit, ipDestUnreachable:
		return 0, ErrUnreachable
	}
	return 0, fmt.Errorf("polite: ICMP echo status %d", status)
}
