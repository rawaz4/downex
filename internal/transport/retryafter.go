package transport

import (
	"net/http"
	"time"
)

// MaxRetryAfter caps a server-requested delay so a hostile or broken server cannot
// park a download indefinitely.
const MaxRetryAfter = time.Hour

// ParseRetryAfter parses a Retry-After field value (RFC 9110 §10.2.3): either
// delay-seconds or an HTTP-date, which is measured from now. It returns 0 for empty,
// malformed or past values and caps the result at MaxRetryAfter.
func ParseRetryAfter(v string, now time.Time) time.Duration {
	v = trimOWS(v)
	if v == "" {
		return 0
	}
	if isDigits(v) {
		// More digits than MaxRetryAfter has seconds cannot be below the cap
		// (and could overflow time.Duration).
		if len(v) > 6 {
			return MaxRetryAfter
		}
		secs, _ := parseDigits(v)
		return min(time.Duration(secs)*time.Second, MaxRetryAfter)
	}
	t, err := http.ParseTime(v)
	if err != nil {
		return 0
	}
	d := t.Sub(now)
	if d <= 0 {
		return 0
	}
	return min(d, MaxRetryAfter)
}

// retryAfterFromHeader measures an HTTP-date Retry-After against the response's own
// Date header when it has a valid one, which cancels out clock skew between client
// and server; otherwise against the local time now.
func retryAfterFromHeader(h http.Header, now time.Time) time.Duration {
	v := h.Get("Retry-After")
	if v == "" {
		return 0
	}
	if date, err := http.ParseTime(h.Get("Date")); err == nil {
		now = date
	}
	return ParseRetryAfter(v, now)
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
