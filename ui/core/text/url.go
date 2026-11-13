package text

import (
	"errors"
	"net/netip"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/idna"
)

// URL is the part of a parsed address the window reads, as JavaScript's URL (the WHATWG URL
// Standard) gives it: the scheme, the host and port, and the path.
type URL struct {
	scheme   string
	hostname string // serialized host: a lower-case ASCII domain, an IPv4 address, "[v6]"
	port     string // "" when none or the scheme's default
	pathname string
}

// Scheme is the scheme in lower case, without its ":" ("https").
func (u URL) Scheme() string { return u.scheme }

// Hostname is URL.hostname: the host without its port ("cdn.example.net", "[::1]").
func (u URL) Hostname() string { return u.hostname }

// Host is URL.host: the host with its port when it has one other than the scheme's default.
func (u URL) Host() string {
	if u.port == "" {
		return u.hostname
	}
	return u.hostname + ":" + u.port
}

// Port is URL.port: "" when there is none or it is the scheme's default.
func (u URL) Port() string { return u.port }

// Pathname is URL.pathname: the path, its dot segments resolved and the characters a path
// cannot hold percent-encoded ("/files/my%20notes.pdf").
func (u URL) Pathname() string { return u.pathname }

// ErrInvalidURL is a string JavaScript's URL constructor refuses (it throws a TypeError).
var ErrInvalidURL = errors.New("text: invalid URL")

var defaultPorts = map[string]string{"ftp": "21", "file": "", "http": "80", "https": "443", "ws": "80", "wss": "443"}

func isSpecial(scheme string) bool { _, ok := defaultPorts[scheme]; return ok }

// ParseURL parses an absolute URL as the WHATWG URL parser does (without a base): leading and
// trailing C0 controls and spaces are dropped, tabs and newlines removed; a special scheme
// (http, https, ws, wss, ftp, file) needs a host, which is percent-decoded, mapped to ASCII
// with UTS #46 (IDNA) and checked, or read as an IPv4 or IPv6 address; the port must be a
// number up to 65535. The query and fragment are not kept.
func ParseURL(input string) (URL, error) {
	s := strings.TrimFunc(input, func(r rune) bool { return r <= 0x20 })
	if strings.ContainsAny(s, "\t\n\r") {
		s = strings.NewReplacer("\t", "", "\n", "", "\r", "").Replace(s)
	}
	scheme, rest, ok := splitScheme(s)
	if !ok {
		return URL{}, ErrInvalidURL
	}
	u := URL{scheme: scheme}
	special := isSpecial(scheme)
	switch {
	case scheme == "file":
		return parseFile(u, rest)
	case special:
		rest = strings.TrimLeft(rest, `/\`)
		authority, after := cut(rest, `/\?#`)
		if err := u.authority(authority, true); err != nil {
			return URL{}, err
		}
		u.pathname = path(after, true)
	case strings.HasPrefix(rest, "//"):
		authority, after := cut(rest[2:], "/?#")
		if err := u.authority(authority, false); err != nil {
			return URL{}, err
		}
		if after != "" && after[0] == '/' {
			u.pathname = path(after, false)
		}
	default:
		opaque, _ := cut(rest, "?#")
		if strings.HasPrefix(opaque, "/") {
			u.pathname = path(opaque, false)
		} else {
			u.pathname = percentEncode(opaque, c0Control)
		}
	}
	return u, nil
}

// splitScheme reads "scheme:" (an ASCII letter, then letters, digits, "+", "-", ".").
func splitScheme(s string) (scheme, rest string, ok bool) {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
		case i > 0 && (c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.'):
		case i > 0 && c == ':':
			return strings.ToLower(s[:i]), s[i+1:], true
		default:
			return "", "", false
		}
	}
	return "", "", false
}

// cut splits s at the first byte of any of stops.
func cut(s, stops string) (before, after string) {
	if i := strings.IndexAny(s, stops); i >= 0 {
		return s[:i], s[i:]
	}
	return s, ""
}

// authority reads [userinfo@]host[:port].
func (u *URL) authority(a string, special bool) error {
	if at := strings.LastIndexByte(a, '@'); at >= 0 {
		a = a[at+1:]
		if a == "" {
			return ErrInvalidURL
		}
	}
	hostText, port := a, ""
	inBrackets := false
	for i := 0; i < len(a); i++ {
		switch a[i] {
		case '[':
			inBrackets = true
		case ']':
			inBrackets = false
		case ':':
			if !inBrackets {
				hostText, port = a[:i], a[i+1:]
				i = len(a)
			}
		}
	}
	if hostText == "" {
		if special || port != "" || strings.Contains(a, ":") {
			return ErrInvalidURL
		}
		return nil
	}
	host, err := parseHost(hostText, special)
	if err != nil {
		return err
	}
	u.hostname = host
	if port != "" {
		for i := 0; i < len(port); i++ {
			if port[i] < '0' || port[i] > '9' {
				return ErrInvalidURL
			}
		}
		n, err := strconv.ParseUint(strings.TrimLeft(port, "0")+"0", 10, 64)
		if err != nil || n/10 > 65535 {
			return ErrInvalidURL
		}
		p := strconv.FormatUint(n/10, 10)
		if def, ok := defaultPorts[u.scheme]; !ok || def != p {
			u.port = p
		}
	}
	return nil
}

// parseFile reads the rest of a "file:" URL: "//host/path" or a path.
func parseFile(u URL, rest string) (URL, error) {
	if len(rest) >= 2 && isSlashByte(rest[0]) && isSlashByte(rest[1]) {
		hostText, after := cut(rest[2:], `/\?#`)
		if isDriveLetter(hostText) {
			after = "/" + rest[2:]
			hostText = ""
		}
		if hostText != "" {
			host, err := parseHost(hostText, true)
			if err != nil {
				return URL{}, err
			}
			if host != "localhost" {
				u.hostname = host
			}
		}
		rest = after
	}
	p, _ := cut(rest, "?#")
	u.pathname = path(p, true)
	return u, nil
}

func isSlashByte(c byte) bool { return c == '/' || c == '\\' }

func isDriveLetter(s string) bool {
	return len(s) == 2 && (s[0] >= 'a' && s[0] <= 'z' || s[0] >= 'A' && s[0] <= 'Z') && (s[1] == ':' || s[1] == '|')
}

// path is the path state: segments between "/" (and "\" for special schemes), each
// percent-encoded, "." and ".." (also as "%2e") resolved. It stops at "?" and "#".
func path(s string, special bool) string {
	s, _ = cut(s, "?#")
	seps := "/"
	if special {
		seps = `/\`
	}
	if s != "" && strings.IndexByte(seps, s[0]) >= 0 {
		s = s[1:]
	}
	var out []string
	for {
		seg, rest := cut(s, seps)
		last := rest == ""
		switch {
		case isDoubleDot(seg):
			if len(out) > 0 {
				out = out[:len(out)-1]
			}
			if last {
				out = append(out, "")
			}
		case isSingleDot(seg):
			if last {
				out = append(out, "")
			}
		default:
			out = append(out, percentEncode(seg, pathSet))
		}
		if last {
			break
		}
		s = rest[1:]
	}
	return "/" + strings.Join(out, "/")
}

func isSingleDot(s string) bool { return s == "." || strings.EqualFold(s, "%2e") }

func isDoubleDot(s string) bool {
	switch strings.ToLower(s) {
	case "..", ".%2e", "%2e.", "%2e%2e":
		return true
	}
	return false
}

type encodeSet uint8

const (
	c0Control encodeSet = iota
	pathSet
)

func (set encodeSet) has(c byte) bool {
	if c < 0x20 || c > 0x7E {
		return true
	}
	if set == c0Control {
		return false
	}
	switch c {
	case ' ', '"', '#', '<', '>', '?', '`', '{', '}':
		return true
	}
	return false
}

// percentEncode writes the bytes of s in the set as "%XX" (s is UTF-8: a character outside
// ASCII is its bytes' escapes).
func percentEncode(s string, set encodeSet) string {
	n := 0
	for i := 0; i < len(s); i++ {
		if set.has(s[i]) {
			n++
		}
	}
	if n == 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 2*n)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if set.has(c) {
			b.WriteByte('%')
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&0xF])
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// domainToASCII is the URL Standard's UTS #46 processing: non-transitional mapping, the bidi
// and joiner rules, no hyphen, STD3 or length checks.
var domainToASCII = idna.New(
	idna.MapForLookup(),
	idna.BidiRule(),
	idna.Transitional(false),
	idna.StrictDomainName(false),
	idna.CheckHyphens(false),
	idna.CheckJoiners(true),
	idna.VerifyDNSLength(false),
)

// parseHost is the host parser: "[v6]", an opaque host (not special), else a domain mapped to
// ASCII, or an IPv4 address when it ends in a number.
func parseHost(s string, special bool) (string, error) {
	if strings.HasPrefix(s, "[") {
		if !strings.HasSuffix(s, "]") || len(s) < 3 {
			return "", ErrInvalidURL
		}
		return ipv6(s[1 : len(s)-1])
	}
	if !special {
		for i := 0; i < len(s); i++ {
			if isForbiddenHost(s[i]) && s[i] != '%' {
				return "", ErrInvalidURL
			}
		}
		return percentEncode(s, c0Control), nil
	}
	decoded := percentDecode(s)
	if !utf8.ValidString(decoded) {
		decoded = strings.ToValidUTF8(decoded, "\ufffd")
	}
	decoded = strings.TrimPrefix(decoded, "\ufeff")
	ascii := ""
	if isASCIIOnly(decoded) && !hasACELabel(decoded) {
		ascii = strings.ToLower(decoded)
	} else {
		a, err := domainToASCII.ToASCII(decoded)
		if err != nil {
			return "", ErrInvalidURL
		}
		ascii = a
	}
	if ascii == "" {
		return "", ErrInvalidURL
	}
	for i := 0; i < len(ascii); i++ {
		if isForbiddenDomain(ascii[i]) {
			return "", ErrInvalidURL
		}
	}
	if endsInNumber(ascii) {
		return ipv4(ascii)
	}
	return ascii, nil
}

func isASCIIOnly(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func hasACELabel(s string) bool {
	for _, label := range strings.Split(s, ".") {
		if len(label) >= 4 && strings.EqualFold(label[:4], "xn--") {
			return true
		}
	}
	return false
}

func percentDecode(s string) string {
	if strings.IndexByte(s, '%') < 0 {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			if v, ok := hexByte(s, i); ok {
				b.WriteByte(v)
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isForbiddenHost(c byte) bool {
	switch c {
	case 0, '\t', '\n', '\r', ' ', '#', '/', ':', '<', '>', '?', '@', '[', '\\', ']', '^', '|':
		return true
	}
	return false
}

func isForbiddenDomain(c byte) bool {
	return isForbiddenHost(c) || c <= 0x1F || c == '%' || c == 0x7F
}

// endsInNumber: the last label (before a trailing dot) is digits, or reads as an IPv4 number.
func endsInNumber(s string) bool {
	parts := strings.Split(s, ".")
	if parts[len(parts)-1] == "" {
		if len(parts) == 1 {
			return false
		}
		parts = parts[:len(parts)-1]
	}
	last := parts[len(parts)-1]
	if last != "" && strings.Trim(last, "0123456789") == "" {
		return true
	}
	_, ok := ipv4Number(last)
	return ok
}

// ipv4Number reads one part of an IPv4 address: "0x" hex, a leading 0 octal, else decimal.
func ipv4Number(s string) (uint64, bool) {
	if s == "" {
		return 0, false
	}
	base := 10
	if len(s) >= 2 && s[0] == '0' && (s[1] == 'x' || s[1] == 'X') {
		s, base = s[2:], 16
	} else if len(s) >= 2 && s[0] == '0' {
		s, base = s[1:], 8
	}
	if s == "" {
		return 0, true
	}
	var n uint64
	for i := 0; i < len(s); i++ {
		v, ok := unhex(s[i])
		if !ok || int(v) >= base {
			return 0, false
		}
		if n > 1<<40 {
			n = 1 << 41 // too large for any part; keep it from overflowing
			continue
		}
		n = n*uint64(base) + uint64(v)
	}
	return n, true
}

func ipv4(s string) (string, error) {
	parts := strings.Split(s, ".")
	if parts[len(parts)-1] == "" && len(parts) > 1 {
		parts = parts[:len(parts)-1]
	}
	if len(parts) > 4 {
		return "", ErrInvalidURL
	}
	nums := make([]uint64, len(parts))
	for i, p := range parts {
		n, ok := ipv4Number(p)
		if !ok {
			return "", ErrInvalidURL
		}
		nums[i] = n
	}
	for _, n := range nums[:len(nums)-1] {
		if n > 255 {
			return "", ErrInvalidURL
		}
	}
	last := nums[len(nums)-1]
	if last >= 1<<(8*(5-len(nums))) {
		return "", ErrInvalidURL
	}
	addr := last
	for i, n := range nums[:len(nums)-1] {
		addr += n << (8 * (3 - i))
	}
	return strconv.Itoa(int(addr>>24)) + "." + strconv.Itoa(int(addr>>16&0xFF)) + "." +
		strconv.Itoa(int(addr>>8&0xFF)) + "." + strconv.Itoa(int(addr&0xFF)), nil
}

// ipv6 parses the text between the brackets and writes it as the URL Standard serializes it:
// lower-case hex pieces, the first longest run of two or more zero pieces as "::".
func ipv6(s string) (string, error) {
	if strings.ContainsAny(s, "%") {
		return "", ErrInvalidURL
	}
	a, err := netip.ParseAddr(s)
	if err != nil || !a.Is6() {
		return "", ErrInvalidURL
	}
	b := a.As16()
	var pieces [8]uint16
	for i := range pieces {
		pieces[i] = uint16(b[2*i])<<8 | uint16(b[2*i+1])
	}
	start, length := -1, 1
	for i := 0; i < 8; {
		if pieces[i] != 0 {
			i++
			continue
		}
		j := i
		for j < 8 && pieces[j] == 0 {
			j++
		}
		if j-i > length {
			start, length = i, j-i
		}
		i = j
	}
	var out strings.Builder
	out.WriteByte('[')
	for i := 0; i < 8; i++ {
		if i == start {
			if i == 0 {
				out.WriteString("::")
			} else {
				out.WriteByte(':')
			}
			i += length - 1
			continue
		}
		out.WriteString(strconv.FormatUint(uint64(pieces[i]), 16))
		if i != 7 {
			out.WriteByte(':')
		}
	}
	out.WriteByte(']')
	return out.String(), nil
}
