// Package sysproxy discovers the operating system's proxy configuration for the
// SYSTEM proxy mode (docs/M6-SPEC.md §2). Discover returns a Settings snapshot; the
// daemon turns it into a transport.ProxyResolver (fetching and evaluating a PAC URL
// with internal/pac where one is configured). The pure parsers (ParseScutil,
// EnvSettings, the WinINET proxy strings) are exported or untagged so they can be
// checked against fixtures on any OS.
package sysproxy

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"downex/internal/transport"
)

// Settings is a snapshot of the system proxy configuration.
type Settings struct {
	// AutoDetect requests WPAD discovery (Windows). The daemon may ignore it where
	// unsupported.
	AutoDetect bool
	// PACURL is an auto-config script URL (http/https/file); "" when none.
	PACURL string
	// HTTPProxy and HTTPSProxy are "host:port" (an HTTP proxy) or, from the environment,
	// a proxy URL that keeps its scheme and credentials ("https://u:p@host:port"); ""
	// when unset.
	HTTPProxy  string
	HTTPSProxy string
	// SOCKSProxy is "host:port" (SOCKS5, the macOS system proxy) or a socks5://,
	// socks4:// or socks4a:// URL (WinINET's "socks=" is SOCKS4, ALL_PROXY keeps its
	// scheme). It serves only the schemes without a proxy of their own.
	SOCKSProxy string
	// FTPProxy is kept for completeness but unused by Downex.
	FTPProxy string
	// Bypass lists host patterns reached directly, in transport bypass syntax. Entries
	// that do not parse are skipped by NewStaticResolver (see UnusableBypass).
	Bypass []string
}

// Empty reports whether s names no proxy at all (a direct configuration).
func (s Settings) Empty() bool {
	return !s.AutoDetect && s.PACURL == "" && s.HTTPProxy == "" && s.HTTPSProxy == "" && s.SOCKSProxy == ""
}

// String describes s without the passwords proxy URLs from the environment may carry.
func (s Settings) String() string {
	return fmt.Sprintf("sysproxy.Settings{AutoDetect:%v PACURL:%q HTTP:%q HTTPS:%q SOCKS:%q Bypass:%q}",
		s.AutoDetect, s.PACURL, redactProxy(s.HTTPProxy), redactProxy(s.HTTPSProxy), redactProxy(s.SOCKSProxy), s.Bypass)
}

// GoString keeps passwords out of %#v.
func (s Settings) GoString() string { return s.String() }

// redactProxy hides the password of a proxy URL.
func redactProxy(v string) string {
	if !strings.Contains(v, "@") {
		return v
	}
	if u, err := url.Parse(v); err == nil && u.User != nil {
		return u.Redacted()
	}
	return "(proxy URL)"
}

// BypassString joins the bypass patterns with ';' (the transport bypass syntax).
func (s Settings) BypassString() string { return strings.Join(s.Bypass, ";") }

// UnusableBypass returns the bypass entries that are not valid transport bypass
// patterns; NewStaticResolver leaves them out rather than ignoring the whole system
// configuration.
func (s Settings) UnusableBypass() []string {
	_, bad := usableBypass(s.Bypass)
	return bad
}

// usableBypass joins the entries that parse as bypass patterns (at most the transport's
// limit) and returns the others.
func usableBypass(entries []string) (string, []string) {
	const maxEntries = 512 // transport's bypass list limit
	var ok, bad []string
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if _, err := transport.ParseBypass(e); err != nil || len(ok) >= maxEntries {
			bad = append(bad, e)
			continue
		}
		ok = append(ok, e)
	}
	return strings.Join(ok, ";"), bad
}

// EnvSettings reads proxy settings from the environment (the Linux SYSTEM behaviour):
// HTTP_PROXY, HTTPS_PROXY, ALL_PROXY and NO_PROXY (and their lower-case forms), as
// curl, wget and Go read them. Values may be a bare host:port (an HTTP proxy) or a
// proxy URL, kept whole with its scheme and credentials; a socks URL in ALL_PROXY fills
// SOCKSProxy. A NO_PROXY domain also covers its subdomains.
func EnvSettings(getenv func(string) string) Settings {
	get := func(names ...string) string {
		for _, n := range names {
			if v := strings.TrimSpace(getenv(n)); v != "" {
				return v
			}
		}
		return ""
	}
	var s Settings
	s.HTTPProxy = envProxyValue(get("HTTP_PROXY", "http_proxy"), false)
	s.HTTPSProxy = envProxyValue(get("HTTPS_PROXY", "https_proxy"), false)
	if all := get("ALL_PROXY", "all_proxy"); all != "" {
		if sock := envProxyValue(all, true); sock != "" {
			s.SOCKSProxy = sock
		} else {
			h := envProxyValue(all, false)
			if s.HTTPProxy == "" {
				s.HTTPProxy = h
			}
			if s.HTTPSProxy == "" {
				s.HTTPSProxy = h
			}
		}
	}
	if no := get("NO_PROXY", "no_proxy"); no != "" {
		for _, p := range strings.FieldsFunc(no, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
			s.Bypass = append(s.Bypass, noProxyPatterns(p)...)
		}
	}
	return s
}

// envProxyValue returns an environment proxy value as a proxy URL (a bare host:port is
// an HTTP proxy, or with socksOnly a SOCKS5 one), or "" when it is not of the wanted
// kind (socks URLs only with socksOnly, never without).
func envProxyValue(v string, socksOnly bool) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if socksOnly != strings.HasPrefix(strings.ToLower(v), "socks") {
		return ""
	}
	if !strings.Contains(v, "://") {
		if socksOnly {
			return "socks5://" + v
		}
		return v
	}
	if u, err := url.Parse(v); err != nil || u.Host == "" {
		return ""
	}
	if scheme, rest, _ := strings.Cut(v, "://"); strings.EqualFold(scheme, "socks") {
		return "socks5://" + rest
	}
	return v
}

// noProxyPatterns translates one NO_PROXY entry to bypass patterns: a domain name
// ("example.com", optionally with a port) matches itself and its subdomains, ".example.com"
// its subdomains, and IP addresses, CIDR ranges and "*" are kept as they are.
func noProxyPatterns(e string) []string {
	e = strings.TrimSpace(e)
	if e == "" {
		return nil
	}
	if e == "*" || strings.ContainsAny(e, "*/[") || strings.HasPrefix(e, ".") {
		return []string{e}
	}
	host := e
	if h, port, ok := strings.Cut(e, ":"); ok && !strings.Contains(port, ":") {
		host = h
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return []string{e}
	}
	if strings.Count(e, ":") > 1 { // a bare IPv6 address that did not parse
		return []string{e}
	}
	return []string{e, "*." + e}
}

// ParseScutil parses the output of `scutil --proxy` on macOS into a Settings. The
// output is a dictionary of key : value lines; unknown keys are ignored. Enable flags
// gate their matching host/port pairs.
func ParseScutil(out []byte) Settings {
	vals := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		vals[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	is1 := func(k string) bool { return vals[k] == "1" }
	hostPort := func(hostKey, portKey string) string {
		host := vals[hostKey]
		if host == "" {
			return ""
		}
		if port := vals[portKey]; port != "" {
			return net.JoinHostPort(host, port)
		}
		return host
	}
	var s Settings
	if is1("ProxyAutoDiscoveryEnable") {
		s.AutoDetect = true
	}
	if is1("ProxyAutoConfigEnable") {
		s.PACURL = vals["ProxyAutoConfigURLString"]
	}
	if is1("HTTPEnable") {
		s.HTTPProxy = hostPort("HTTPProxy", "HTTPPort")
	}
	if is1("HTTPSEnable") {
		s.HTTPSProxy = hostPort("HTTPSProxy", "HTTPSPort")
	}
	if is1("SOCKSEnable") {
		s.SOCKSProxy = hostPort("SOCKSProxy", "SOCKSPort")
	}
	if is1("FTPEnable") {
		s.FTPProxy = hostPort("FTPProxy", "FTPPort")
	}
	s.Bypass = parseScutilExceptions(vals)
	return s
}

// parseScutilExceptions reads the ExceptionsList array, printed by scutil as indexed
// lines ("0 : *.local"), plus the ExcludeSimpleHostnames flag ("<local>"). macOS's
// short IPv4 ranges ("169.254/16", in its default list) are written out in full.
func parseScutilExceptions(vals map[string]string) []string {
	var out []string
	if vals["ExcludeSimpleHostnames"] == "1" {
		out = append(out, "<local>")
	}
	// scutil prints array entries as "<index> : value"; those became keys like "0".
	for i := 0; ; i++ {
		v, ok := vals[strconv.Itoa(i)]
		if !ok {
			break
		}
		if v != "" {
			out = append(out, expandShortCIDR(v))
		}
	}
	return out
}

// expandShortCIDR pads an IPv4 range with fewer than four octets ("169.254/16") to the
// dotted form transport bypass patterns use ("169.254.0.0/16"); other values are
// returned unchanged.
func expandShortCIDR(v string) string {
	addr, bits, ok := strings.Cut(v, "/")
	if !ok || addr == "" || strings.Count(addr, ".") >= 3 {
		return v
	}
	octets := strings.Split(addr, ".")
	for _, o := range octets {
		if n, err := strconv.Atoi(o); err != nil || n < 0 || n > 255 || o == "" {
			return v
		}
	}
	for len(octets) < 4 {
		octets = append(octets, "0")
	}
	return strings.Join(octets, ".") + "/" + bits
}
