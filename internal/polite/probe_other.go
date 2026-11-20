//go:build !windows && !darwin && !linux

package polite

import (
	"context"
	"net/netip"
	"time"
)

// probeICMP: this system has no unprivileged ICMP that Downex uses; TCP probes only.
func probeICMP(context.Context, netip.Addr) (time.Duration, error) { return 0, ErrUnsupported }
