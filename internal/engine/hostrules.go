package engine

import (
	"context"
	"math"

	"downex/internal/limiter"
)

// HostRules supplies per-host limits the daemon derives from Settings.host_rules
// (docs/M6-SPEC.md §2). A nil HostRules (the default) applies no host rules. The engine
// calls it on every running download, so implementations must be fast and safe for
// concurrent use; the most specific pattern wins is the implementation's concern.
type HostRules interface {
	// HostMaxConnections caps the connections of one download to host, in addition to
	// the download's own limit and any 429/503 penalty. 0 means no cap.
	HostMaxConnections(host string) int
	// HostLimiter returns a speed limiter shared by every download to host (so their
	// combined rate is bounded), or nil for no host speed limit. The same host must
	// return the same limiter instance across calls.
	HostLimiter(host string) limiter.Limiter
}

// RequestCaps is an optional extension of HostRules, found by type assertion: a cap on
// the length of every ranged request to a host (docs/SITE-MEDIA-SPEC.md §2.4; the
// daemon's built-in rule caps *.googlevideo.com, which throttles longer requests).
// Under a cap the probe asks for "bytes=0-(cap-1)" and every later request ends at most
// cap bytes after its start; a connection continues with a new request where the last
// one ended. Implementations must be fast and safe for concurrent use.
type RequestCaps interface {
	// HostMaxRequestBytes returns the most bytes one ranged request to host may ask
	// for; 0 means no cap.
	HostMaxRequestBytes(host string) int64
}

// combinedThrottle waits on two throttles so both limits hold. Either may be nil.
type combinedThrottle struct {
	a, b throttle
}

// combineThrottle returns a throttle enforcing both a and b. A nil argument is dropped;
// when both are nil the result is nil (unlimited).
func combineThrottle(a, b throttle) throttle {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	}
	return combinedThrottle{a: a, b: b}
}

// WaitN waits on both throttles. The outer one is charged first; if it is cancelled the
// second is not consulted.
func (c combinedThrottle) WaitN(ctx context.Context, n int) error {
	if err := c.a.WaitN(ctx, n); err != nil {
		return err
	}
	return c.b.WaitN(ctx, n)
}

// Limit reports the tighter of the two non-zero limits (0 = unlimited), so the worker
// reads in small slices whenever either throttle caps the rate.
func (c combinedThrottle) Limit() int64 {
	la, lb := c.a.Limit(), c.b.Limit()
	switch {
	case la == 0:
		return lb
	case lb == 0:
		return la
	}
	return min(la, lb)
}

// hostThrottle returns the host-shared limiter for host from the engine's HostRules, or
// nil when there is none.
func (e *Engine) hostThrottle(host string) throttle {
	if e.opts.HostRules == nil {
		return nil
	}
	if lim := e.opts.HostRules.HostLimiter(host); lim != nil {
		return lim
	}
	return nil
}

// hostRuleMax returns the per-host connection cap from HostRules, or 0 when none.
func (e *Engine) hostRuleMax(host string) int {
	if e.opts.HostRules == nil {
		return 0
	}
	return e.opts.HostRules.HostMaxConnections(host)
}

// requestCaps returns the engine's RequestCaps, or nil when its HostRules has none.
func (e *Engine) requestCaps() RequestCaps {
	rc, _ := e.opts.HostRules.(RequestCaps)
	return rc
}

// requestCap returns the longest range one request to rawURL may ask for: the smaller
// non-zero cap of the URL's host and of host (the download's own host, whose rules
// apply to the whole download; "" for none). 0 means no cap.
func requestCap(rc RequestCaps, host, rawURL string) int64 {
	if rc == nil {
		return 0
	}
	limit := max(rc.HostMaxRequestBytes(hostKey(rawURL)), 0)
	if host != "" {
		if c := rc.HostMaxRequestBytes(host); c > 0 && (limit == 0 || c < limit) {
			limit = c
		}
	}
	return limit
}

// cappedEnd is the last byte a request starting at pos may ask for under limit (0 = no
// cap): end itself, or pos+limit-1 when that comes first (end -1 = to the end of the
// resource).
func cappedEnd(pos, end, limit int64) int64 {
	if limit <= 0 || pos > math.MaxInt64-limit {
		return end
	}
	if last := pos + limit - 1; end < 0 || last < end {
		return last
	}
	return end
}
