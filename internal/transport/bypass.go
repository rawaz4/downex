package transport

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
)

// maxBypassRules bounds a bypass list.
const maxBypassRules = 512

// BypassList is a parsed list of hosts that are reached without a proxy. The nil list
// bypasses loopback targets only.
type BypassList struct {
	rules      []bypassRule
	local      bool // "<local>": host names without a dot
	noLoopback bool // "<-loopback>": loopback targets are proxied too
}

type bypassRule struct {
	scheme string       // "" = any
	host   string       // lower-case glob with * and ?; "" when prefix is set
	port   string       // "" = any
	prefix netip.Prefix // valid for CIDR rules
}

// ParseBypass parses a bypass list: patterns separated by ';', ',' or white space.
//
//	<local>                host names without a dot ("intranet", not "a.b" or an IP)
//	<-loopback>            also proxy localhost and loopback addresses (bypassed by default)
//	example.com            exactly this host
//	*.example.com          its subdomains (".example.com" is the same)
//	*example.com, 10.*     wildcards: * any run of characters, ? one character
//	10.0.0.0/8, fe80::/10  IP literals in a CIDR range
//	host:8080, [::1]:80    only this port
//	https://x.example.com  only this scheme
//	*                      every host
func ParseBypass(list string) (*BypassList, error) {
	b := &BypassList{}
	for _, tok := range strings.FieldsFunc(list, func(r rune) bool {
		return r == ';' || r == ',' || r == ' ' || r == '\t' || r == '\r' || r == '\n'
	}) {
		if len(b.rules) >= maxBypassRules {
			return nil, fmt.Errorf("transport: bypass list has more than %d entries", maxBypassRules)
		}
		switch strings.ToLower(tok) {
		case "<local>":
			b.local = true
			continue
		case "<-loopback>":
			b.noLoopback = true
			continue
		}
		r, err := parseBypassRule(tok)
		if err != nil {
			return nil, err
		}
		b.rules = append(b.rules, r)
	}
	return b, nil
}

func parseBypassRule(tok string) (bypassRule, error) {
	var r bypassRule
	orig := tok
	tok = strings.ToLower(tok)
	if i := strings.Index(tok, "://"); i >= 0 {
		r.scheme, tok = tok[:i], tok[i+3:]
		if r.scheme == "" || strings.ContainsAny(r.scheme, "*?/[]:") {
			return r, fmt.Errorf("transport: invalid bypass entry %q", orig)
		}
	}
	tok = strings.TrimSuffix(tok, "/")
	if strings.Contains(tok, "/") {
		p, err := netip.ParsePrefix(strings.Trim(tok, "[]"))
		if err != nil {
			return r, fmt.Errorf("transport: invalid bypass range %q", orig)
		}
		r.prefix = p.Masked()
		return r, nil
	}
	host := tok
	switch {
	case strings.HasPrefix(tok, "["):
		end := strings.IndexByte(tok, ']')
		if end < 0 {
			return r, fmt.Errorf("transport: invalid bypass entry %q", orig)
		}
		host = tok[1:end]
		if rest := tok[end+1:]; rest != "" {
			if !strings.HasPrefix(rest, ":") || !validPort(rest[1:]) {
				return r, fmt.Errorf("transport: invalid port in bypass entry %q", orig)
			}
			r.port = rest[1:]
		}
	case strings.Count(tok, ":") == 1:
		h, p, _ := strings.Cut(tok, ":")
		if !validPort(p) {
			return r, fmt.Errorf("transport: invalid port in bypass entry %q", orig)
		}
		host, r.port = h, p
	}
	if strings.HasPrefix(host, ".") {
		host = "*" + host
	}
	if host == "" {
		return r, fmt.Errorf("transport: invalid bypass entry %q", orig)
	}
	for i := 0; i < len(host); i++ {
		c := host[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.IndexByte(".-_*?:", c) >= 0) {
			return r, fmt.Errorf("transport: invalid character in bypass entry %q", orig)
		}
	}
	r.host = host
	return r, nil
}

func validPort(p string) bool {
	if p == "" || len(p) > 5 {
		return false
	}
	n := 0
	for i := 0; i < len(p); i++ {
		if p[i] < '0' || p[i] > '9' {
			return false
		}
		n = n*10 + int(p[i]-'0')
	}
	return n >= 1 && n <= 65535
}

// Match reports whether target is reached without a proxy.
func (b *BypassList) Match(target *url.URL) bool {
	host := strings.ToLower(strings.TrimSuffix(target.Hostname(), "."))
	if host == "" {
		return false
	}
	addr, ipErr := netip.ParseAddr(host)
	isIP := ipErr == nil
	if (b == nil || !b.noLoopback) && isLoopback(host, addr, isIP) {
		return true
	}
	if b == nil {
		return false
	}
	if b.local && !isIP && !strings.Contains(host, ".") {
		return true
	}
	scheme := strings.ToLower(target.Scheme)
	port := target.Port()
	if port == "" {
		switch scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		}
	}
	for _, r := range b.rules {
		if r.scheme != "" && r.scheme != scheme {
			continue
		}
		if r.port != "" && r.port != port {
			continue
		}
		if r.prefix.IsValid() {
			if isIP && r.prefix.Contains(addr.Unmap()) {
				return true
			}
			continue
		}
		// "*.example.com" matches its subdomains and the bare domain itself.
		if suffix, ok := strings.CutPrefix(r.host, "*."); ok {
			if host == suffix || strings.HasSuffix(host, "."+suffix) {
				return true
			}
			continue
		}
		if globMatch(r.host, host) {
			return true
		}
	}
	return false
}

func isLoopback(host string, addr netip.Addr, isIP bool) bool {
	if isIP {
		return addr.Unmap().IsLoopback()
	}
	return host == "localhost" || strings.HasSuffix(host, ".localhost")
}

// globMatch matches s against pattern with '*' (any run, also empty) and '?' (one byte).
func globMatch(pattern, s string) bool {
	px, sx := 0, 0
	nextPx, nextSx := -1, -1
	for px < len(pattern) || sx < len(s) {
		if px < len(pattern) {
			switch c := pattern[px]; c {
			case '*':
				nextPx, nextSx = px, sx+1
				px++
				continue
			case '?':
				if sx < len(s) {
					px++
					sx++
					continue
				}
			default:
				if sx < len(s) && s[sx] == c {
					px++
					sx++
					continue
				}
			}
		}
		if nextSx > 0 && nextSx <= len(s) {
			px, sx = nextPx, nextSx
			continue
		}
		return false
	}
	return true
}

// hostPort returns u's host and port with the scheme's default port made explicit.
func hostPort(u *url.URL) string {
	port := u.Port()
	if port == "" {
		switch strings.ToLower(u.Scheme) {
		case "https":
			port = defaultHTTPSPort
		default:
			port = defaultHTTPPort
		}
	}
	return net.JoinHostPort(u.Hostname(), port)
}
