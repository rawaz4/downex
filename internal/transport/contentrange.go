package transport

import (
	"errors"
	"strconv"
	"strings"
)

// errContentRange reports a Content-Range value that is not a valid "bytes" range.
var errContentRange = errors.New("transport: invalid Content-Range")

// ParseContentRange parses a Content-Range field value in the "bytes" unit
// (RFC 9110 §14.4):
//
//	"bytes 0-499/1234"  → {Start: 0, End: 499, Total: 1234}
//	"bytes 0-499/*"     → {Start: 0, End: 499, Total: -1}
//	"bytes */1234"      → {Start: -1, End: -1, Total: 1234} (unsatisfied range, sent with 416)
//
// It requires first ≤ last and, when the total is known, last < total. The unit is
// matched case-insensitively and whitespace around the parts is tolerated, as is the
// common malformed separator "bytes=".
func ParseContentRange(v string) (ContentRange, error) {
	s := trimOWS(v)
	const unit = "bytes"
	if len(s) <= len(unit) || !strings.EqualFold(s[:len(unit)], unit) {
		return ContentRange{}, errContentRange
	}
	rest := s[len(unit):]
	switch rest[0] {
	case ' ', '\t':
		rest = trimOWS(rest)
	case '=':
		rest = trimOWS(rest[1:])
	default:
		return ContentRange{}, errContentRange
	}

	rangePart, totalPart, ok := strings.Cut(rest, "/")
	if !ok {
		return ContentRange{}, errContentRange
	}
	rangePart, totalPart = trimOWS(rangePart), trimOWS(totalPart)

	if rangePart == "*" {
		total, ok := parseDigits(totalPart)
		if !ok {
			return ContentRange{}, errContentRange
		}
		return ContentRange{Start: -1, End: -1, Total: total}, nil
	}

	firstPart, lastPart, ok := strings.Cut(rangePart, "-")
	if !ok {
		return ContentRange{}, errContentRange
	}
	first, ok1 := parseDigits(trimOWS(firstPart))
	last, ok2 := parseDigits(trimOWS(lastPart))
	if !ok1 || !ok2 || first > last {
		return ContentRange{}, errContentRange
	}
	total := int64(-1)
	if totalPart != "*" {
		t, ok := parseDigits(totalPart)
		if !ok || last >= t {
			return ContentRange{}, errContentRange
		}
		total = t
	}
	return ContentRange{Start: first, End: last, Total: total}, nil
}

// parseDigits parses 1*DIGIT into a non-negative int64, rejecting signs and overflow.
func parseDigits(s string) (int64, bool) {
	if !isDigits(s) {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil
}

// trimOWS trims optional whitespace (SP / HTAB, RFC 9110 §5.6.3).
func trimOWS(s string) string {
	return strings.Trim(s, " \t")
}
