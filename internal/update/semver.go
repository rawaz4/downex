package update

import (
	"cmp"
	"fmt"
	"strconv"
	"strings"
)

// Version is a semantic version (https://semver.org, 2.0.0): MAJOR.MINOR.PATCH with an
// optional pre-release ("-rc.1") and build metadata ("+abc", ignored when comparing).
type Version struct {
	Major, Minor, Patch uint64
	Pre                 []string // dot-separated pre-release identifiers; nil = a release
	Build               string   // build metadata without the "+"
}

// ParseVersion parses s, which may start with "v" ("v1.2.3"). Numbers have no leading
// zeros, identifiers are non-empty and of [0-9A-Za-z-].
func ParseVersion(s string) (Version, error) {
	var v Version
	rest := strings.TrimPrefix(s, "v")
	bad := func(why string) (Version, error) {
		return Version{}, fmt.Errorf("%w %q: %s", ErrVersion, truncate(s, 64), why)
	}
	if rest == "" || len(s) > 256 {
		return bad("not a semantic version")
	}
	if i := strings.IndexByte(rest, '+'); i >= 0 {
		v.Build = rest[i+1:]
		rest = rest[:i]
		for id := range strings.SplitSeq(v.Build, ".") {
			if !validIdent(id) {
				return bad("invalid build metadata")
			}
		}
	}
	if i := strings.IndexByte(rest, '-'); i >= 0 {
		pre := rest[i+1:]
		rest = rest[:i]
		v.Pre = strings.Split(pre, ".")
		for _, id := range v.Pre {
			if !validIdent(id) || (isNumeric(id) && len(id) > 1 && id[0] == '0') {
				return bad("invalid pre-release")
			}
		}
	}
	parts := strings.Split(rest, ".")
	if len(parts) != 3 {
		return bad("not MAJOR.MINOR.PATCH")
	}
	core := [3]*uint64{&v.Major, &v.Minor, &v.Patch}
	for i, p := range parts {
		if !isNumeric(p) || (len(p) > 1 && p[0] == '0') {
			return bad("not MAJOR.MINOR.PATCH")
		}
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil {
			return bad("number out of range")
		}
		*core[i] = n
	}
	return v, nil
}

// String formats v without a "v" prefix.
func (v Version) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if len(v.Pre) > 0 {
		s += "-" + strings.Join(v.Pre, ".")
	}
	if v.Build != "" {
		s += "+" + v.Build
	}
	return s
}

// Compare returns -1, 0 or +1 as v has lower, equal or higher precedence than o
// (semver §11: build metadata is ignored; a pre-release precedes its release).
func (v Version) Compare(o Version) int {
	if c := cmp.Compare(v.Major, o.Major); c != 0 {
		return c
	}
	if c := cmp.Compare(v.Minor, o.Minor); c != 0 {
		return c
	}
	if c := cmp.Compare(v.Patch, o.Patch); c != 0 {
		return c
	}
	switch {
	case len(v.Pre) == 0 && len(o.Pre) == 0:
		return 0
	case len(v.Pre) == 0:
		return 1
	case len(o.Pre) == 0:
		return -1
	}
	for i := range min(len(v.Pre), len(o.Pre)) {
		if c := compareIdent(v.Pre[i], o.Pre[i]); c != 0 {
			return c
		}
	}
	return cmp.Compare(len(v.Pre), len(o.Pre))
}

// compareIdent compares pre-release identifiers: numeric ones numerically (they have no
// leading zeros, so by length first), numeric before alphanumeric, else in ASCII order.
func compareIdent(a, b string) int {
	an, bn := isNumeric(a), isNumeric(b)
	switch {
	case an && bn:
		if c := cmp.Compare(len(a), len(b)); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	case an:
		return -1
	case bn:
		return 1
	}
	return strings.Compare(a, b)
}

func validIdent(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-') {
			return false
		}
	}
	return true
}

func isNumeric(s string) bool {
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

// truncate shortens s to at most n bytes of valid UTF-8 for messages.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "…"
}
