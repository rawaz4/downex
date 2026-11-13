package media

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// maxAttrs bounds the attributes of one HLS attribute list, which keeps the duplicate
// check cheap.
const maxAttrs = 256

// attr is one AttributeName=AttributeValue pair (RFC 8216 §4.2). value excludes the
// quotes of a quoted-string.
type attr struct {
	name   string
	value  string
	quoted bool
}

type attrList []attr

// get returns the value of name, quoted or not.
func (l attrList) get(name string) (string, bool) {
	for _, a := range l {
		if a.name == name {
			return a.value, true
		}
	}
	return "", false
}

// parseAttrList splits an attribute list (RFC 8216 §4.2): comma-separated
// AttributeName=AttributeValue pairs, names of [A-Z0-9-], values either quoted
// strings (which may contain commas) or unquoted tokens without quotes, commas or
// whitespace. Duplicate names are refused, as the RFC recommends. Spaces around pairs
// are tolerated.
func parseAttrList(s string) (attrList, error) {
	var out attrList
	i := 0
	for {
		i = skipBlanks(s, i)
		if i == len(s) {
			return out, nil
		}
		start := i
		for i < len(s) && isAttrNameByte(s[i]) {
			i++
		}
		if i == start {
			return nil, errf(ErrMalformed, "attribute name expected at column %d", i+1)
		}
		name := s[start:i]
		if i == len(s) || s[i] != '=' {
			return nil, errf(ErrMalformed, "attribute %s has no value", name)
		}
		i++
		var a attr
		a.name = name
		if i < len(s) && s[i] == '"' {
			end := strings.IndexByte(s[i+1:], '"')
			if end < 0 {
				return nil, errf(ErrMalformed, "attribute %s has an unterminated quoted string", name)
			}
			a.value, a.quoted = s[i+1:i+1+end], true
			i += end + 2
		} else {
			start := i
			for i < len(s) && s[i] != ',' {
				if s[i] == '"' {
					return nil, errf(ErrMalformed, "attribute %s has a stray quote", name)
				}
				i++
			}
			a.value = strings.TrimRight(s[start:i], " \t")
			if a.value == "" || strings.ContainsAny(a.value, " \t") {
				return nil, errf(ErrMalformed, "attribute %s has an invalid value", name)
			}
		}
		if _, dup := out.get(name); dup {
			return nil, errf(ErrMalformed, "attribute %s appears twice", name)
		}
		if len(out) == maxAttrs {
			return nil, errf(ErrTooLarge, "attribute list has more than %d attributes", maxAttrs)
		}
		out = append(out, a)
		i = skipBlanks(s, i)
		if i == len(s) {
			return out, nil
		}
		if s[i] != ',' {
			return nil, errf(ErrMalformed, "comma expected after attribute %s", name)
		}
		i++
	}
}

func skipBlanks(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return i
}

func isAttrNameByte(c byte) bool {
	return 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '-'
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

// parseDecimalInt parses a decimal-integer: digits only, 0 to 2^64-1.
func parseDecimalInt(s string) (uint64, error) {
	if !isDigits(s) {
		return 0, errors.New("not a decimal integer")
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, errors.New("decimal integer out of range")
	}
	return v, nil
}

// parseInt63 parses a decimal-integer that must fit an int64.
func parseInt63(s string) (int64, error) {
	v, err := parseDecimalInt(s)
	if err != nil {
		return 0, err
	}
	if v > math.MaxInt64 {
		return 0, errors.New("decimal integer out of range")
	}
	return int64(v), nil
}

// isDecimalFloat reports a non-negative decimal positional number: digits with an
// optional fraction ("10", "10.5", "10.", ".5"); no sign or exponent.
func isDecimalFloat(s string) bool {
	intPart, frac, hasDot := strings.Cut(s, ".")
	if intPart == "" && frac == "" {
		return false
	}
	return (intPart == "" || isDigits(intPart)) && (!hasDot || frac == "" || isDigits(frac))
}

// parseDecimalFloat parses a decimal-floating-point.
func parseDecimalFloat(s string) (float64, error) {
	if !isDecimalFloat(s) {
		return 0, errors.New("not a decimal number")
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsInf(v, 0) {
		return 0, errors.New("decimal number out of range")
	}
	return v, nil
}

// maxSeconds is the largest whole number of seconds a time.Duration holds.
const maxSeconds = math.MaxInt64 / int64(time.Second)

// parseSeconds converts a non-negative decimal number of seconds to a Duration
// exactly, truncating below one nanosecond.
func parseSeconds(s string) (time.Duration, error) {
	if !isDecimalFloat(s) {
		return 0, errors.New("not a decimal number of seconds")
	}
	intPart, frac, _ := strings.Cut(s, ".")
	var secs int64
	for i := 0; i < len(intPart); i++ {
		secs = secs*10 + int64(intPart[i]-'0')
		if secs > maxSeconds {
			return 0, errors.New("duration out of range")
		}
	}
	var nanos int64
	for i := 0; i < 9; i++ {
		nanos *= 10
		if i < len(frac) {
			nanos += int64(frac[i] - '0')
		}
	}
	total := secs*int64(time.Second) + nanos
	if total < 0 {
		return 0, errors.New("duration out of range")
	}
	return time.Duration(total), nil
}

// parseResolution parses a decimal-resolution "<width>x<height>".
func parseResolution(s string) (w, h int, err error) {
	ws, hs, ok := strings.Cut(s, "x")
	if !ok {
		return 0, 0, errors.New("not a resolution")
	}
	wv, err1 := parseDecimalInt(ws)
	hv, err2 := parseDecimalInt(hs)
	if err1 != nil || err2 != nil || wv > math.MaxInt32 || hv > math.MaxInt32 {
		return 0, 0, errors.New("not a resolution")
	}
	return int(wv), int(hv), nil
}

// isHexSequence reports a hexadecimal-sequence: 0x or 0X followed by hex digits.
func isHexSequence(s string) bool {
	if len(s) < 3 || s[0] != '0' || (s[1] != 'x' && s[1] != 'X') {
		return false
	}
	for i := 2; i < len(s); i++ {
		c := s[i]
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F') {
			return false
		}
	}
	return true
}

// parseHLSByteRange parses "<n>[@<o>]" (RFC 8216 §4.3.2.2).
func parseHLSByteRange(s string) (n, o uint64, hasOffset bool, err error) {
	ns, os, hasOffset := strings.Cut(s, "@")
	if n, err = parseDecimalInt(ns); err != nil {
		return 0, 0, false, fmt.Errorf("byte range length: %w", err)
	}
	if hasOffset {
		if o, err = parseDecimalInt(os); err != nil {
			return 0, 0, false, fmt.Errorf("byte range offset: %w", err)
		}
	}
	return n, o, hasOffset, nil
}

// inclusiveRange converts a length and offset to an inclusive [first, last] range.
func inclusiveRange(n, o uint64) (*[2]int64, error) {
	if n == 0 {
		return nil, errors.New("empty byte range")
	}
	if o > math.MaxInt64 || n-1 > math.MaxInt64-o {
		return nil, errors.New("byte range out of range")
	}
	return &[2]int64{int64(o), int64(o + n - 1)}, nil
}

// parseDASHRange parses an inclusive "first-last" byte range (ISO/IEC 23009-1, after
// RFC 7233 byte-range-spec, both positions required).
func parseDASHRange(s string) (*[2]int64, error) {
	fs, ls, ok := strings.Cut(strings.TrimSpace(s), "-")
	if !ok {
		return nil, errors.New("byte range has no '-'")
	}
	first, err1 := parseDecimalInt(fs)
	last, err2 := parseDecimalInt(ls)
	if err1 != nil || err2 != nil || first > math.MaxInt64 || last > math.MaxInt64 {
		return nil, errors.New("byte range is not two byte positions")
	}
	if last < first {
		return nil, errors.New("byte range ends before it starts")
	}
	return &[2]int64{int64(first), int64(last)}, nil
}
