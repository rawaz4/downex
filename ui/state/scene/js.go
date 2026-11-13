package scene

import (
	"math"
	"net/url"
	"strings"
	"unicode/utf16"

	"connectrpc.com/connect"
	"errors"

	"downex/ui/core/format"
	"downex/ui/core/text"
	"downex/ui/core/validate"
)

// The JavaScript number rules the simulation is written in: every byte count, time and
// speed is a float64 handled with Math.floor and Math.round, so a seed gives the same
// scene as today's demo.

func floor(x float64) float64 { return math.Floor(x) }
func round(x float64) float64 { return format.Round(x) }

// prng is mulberry32, the demo's small deterministic generator.
func prng(seed uint32) func() float64 {
	a := seed
	return func() float64 {
		a += 0x6d2b79f5
		t := a
		t = (t ^ (t >> 15)) * (t | 1)
		t ^= t + (t^(t>>7))*(t|61)
		return float64(t^(t>>14)) / 4294967296
	}
}

// hashOf is FNV-1a over UTF-16 code units: a stable number per address.
func hashOf(s string) uint32 {
	h := uint32(0x811c9dc5)
	for _, c := range utf16.Encode([]rune(s)) {
		h ^= uint32(c)
		h *= 0x01000193
	}
	return h
}

// encodeURIComponent is JavaScript's: everything but A–Z a–z 0–9 - _ . ! ~ * ' ( ) is
// percent-encoded as UTF-8.
func encodeURIComponent(s string) string {
	const keep = "-_.!~*'()"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.IndexByte(keep, c) >= 0 {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte("0123456789ABCDEF"[c>>4])
		b.WriteByte("0123456789ABCDEF"[c&15])
	}
	return b.String()
}

// jsURL is what new URL(s) gives the simulation: host name, path and query.
type jsURL struct {
	hostname, pathname, username string
	query                        url.Values
}

func parseJSURL(s string) (jsURL, bool) {
	u, err := text.ParseURL(s)
	if err != nil {
		return jsURL{}, false
	}
	out := jsURL{hostname: u.Hostname(), pathname: u.Pathname()}
	if p, err := url.Parse(s); err == nil {
		out.query = p.Query()
		out.username = p.User.Username()
	}
	return out, true
}

func (u jsURL) param(name string) (string, bool) {
	if u.query == nil {
		return "", false
	}
	vs, ok := u.query[name]
	if !ok || len(vs) == 0 {
		return "", false
	}
	return vs[0], true
}

// apiErr is the simulated daemon's refusal (an ApiError of today's mock).
func apiErr(code connect.Code, msg string) error { return connect.NewError(code, errors.New(msg)) }

const (
	invalidArgument    = connect.CodeInvalidArgument
	failedPrecondition = connect.CodeFailedPrecondition
	notFoundCode       = connect.CodeNotFound
)

func checkURL(s string) (string, bool) { return validate.CheckURL(s) }
