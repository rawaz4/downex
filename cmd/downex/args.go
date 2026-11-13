package main

import (
	"flag"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"

	"downex/internal/engine"
	"downex/internal/model"
)

// parseArgs parses the flags of fs wherever they appear among args, so that both
// "get URL -o dir" and "get -o dir URL" work, and returns the positional arguments.
// Everything after a "--" argument is positional.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return pos, nil
		}
		if n := len(args) - len(rest); n > 0 && args[n-1] == "--" {
			return append(pos, rest...), nil
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

// visited returns the names of the flags set on the command line.
func visited(fs *flag.FlagSet) map[string]bool {
	set := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	return set
}

// headerFlag collects repeated -H "Name: value" request headers.
type headerFlag struct{ h http.Header }

func (f *headerFlag) String() string { return "" }

// Set adds one header. Names must be HTTP tokens and values must not contain
// control characters (RFC 9110 §5).
func (f *headerFlag) Set(v string) error {
	name, value, ok := strings.Cut(v, ":")
	name, value = strings.TrimSpace(name), strings.TrimSpace(value)
	if !ok || !validToken(name) {
		return fmt.Errorf("want \"Name: value\", got %q", v)
	}
	if !validFieldValue(value) {
		return fmt.Errorf("value of header %s contains control characters", name)
	}
	if f.h == nil {
		f.h = http.Header{}
	}
	f.h.Add(name, value)
	return nil
}

// parseRate parses a speed limit in bytes per second: a non-negative number with an
// optional decimal suffix K, M or G (1 K = 1,000 B/s, 1 M = 1,000,000 B/s, 1 G =
// 1,000,000,000 B/s), e.g. "500K", "1.5M" or "0". "B" and "/s" are accepted after the
// suffix ("1MB/s" = 1,000,000). "KiB", "MiB" and "GiB" are binary (1 KiB = 1,024 B), so
// old habits and scripts keep their meaning ("512KiB" = 524,288). 0 means unlimited.
func parseRate(s string) (int64, error) {
	num := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), "/s")
	binary := false
	if after, ok := strings.CutSuffix(num, "ib"); ok {
		num, binary = after, true
	} else {
		num = strings.TrimSuffix(num, "b")
	}
	base, mult := 1000.0, 1.0
	if binary {
		base = 1024
	}
	if n := len(num); n > 0 {
		switch num[n-1] {
		case 'k':
			mult = base
		case 'm':
			mult = base * base
		case 'g':
			mult = base * base * base
		}
		if mult != 1 {
			num = num[:n-1]
		}
	}
	v, err := strconv.ParseFloat(num, 64)
	if binary && mult == 1 { // "iB" without K, M or G ("5iB") is not a unit
		err = strconv.ErrSyntax
	}
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0, fmt.Errorf("invalid rate %q (want e.g. 500K, 1.5M or 0)", s)
	}
	b := math.Round(v * mult)
	switch {
	case b >= math.MaxInt64:
		return 0, fmt.Errorf("rate %q is too large", s)
	case v > 0 && b < 1:
		return 0, fmt.Errorf("rate %q is below 1 byte per second", s)
	}
	return int64(b), nil
}

// checkConnections validates a -c value: 0 selects the engine default.
func checkConnections(cmd string, n int) error {
	if n < 0 || n > engine.HardMaxConnections {
		return usageError{cmd, fmt.Sprintf("-c must be between 1 and %d (or 0 for the default %d), got %d",
			engine.HardMaxConnections, engine.DefaultMaxConnections, n)}
	}
	return nil
}

// parseID parses the single download-id argument of cmd.
func parseID(cmd string, pos []string) (model.DownloadID, error) {
	if len(pos) != 1 {
		return 0, usageError{cmd, "expects exactly one download id"}
	}
	n, err := strconv.ParseInt(pos[0], 10, 64)
	if err != nil || n <= 0 {
		return 0, usageError{cmd, fmt.Sprintf("invalid download id %q", pos[0])}
	}
	return model.DownloadID(n), nil
}

// validToken reports whether s is an RFC 9110 §5.6.2 token (a valid field name).
func validToken(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		c := s[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}

// validFieldValue reports whether s may be sent as a field value (RFC 9110 §5.5):
// no control characters other than horizontal tab.
func validFieldValue(s string) bool {
	for i := range len(s) {
		if c := s[i]; (c < ' ' && c != '\t') || c == 0x7f {
			return false
		}
	}
	return true
}
