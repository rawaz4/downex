package sysproxy

import "strings"

// The WinINET proxy strings (Discover and the per-URL WinHTTP answer on Windows) are
// parsed here, untagged, so that the rules are tested on every OS.

// applyWindowsProxy fills s from a WinINET proxy string: either "host:port" for all
// schemes or "scheme=host:port;..." entries. WinINET speaks SOCKS4 to a "socks="
// proxy, so that one becomes a socks4:// URL; like the HTTP(S) entries it serves only
// the schemes without a proxy of their own (see proxiesFor).
func applyWindowsProxy(s *Settings, proxy string) {
	proxy = strings.TrimSpace(proxy)
	if proxy == "" {
		return
	}
	if !strings.Contains(proxy, "=") {
		s.HTTPProxy, s.HTTPSProxy = proxy, proxy
		return
	}
	for _, part := range strings.FieldsFunc(proxy, func(r rune) bool { return r == ';' || r == ' ' }) {
		scheme, hp, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		hp = strings.TrimSpace(hp)
		switch strings.ToLower(strings.TrimSpace(scheme)) {
		case "http":
			s.HTTPProxy = hp
		case "https":
			s.HTTPSProxy = hp
		case "socks":
			if hp != "" && !strings.Contains(hp, "://") {
				hp = "socks4://" + hp
			}
			s.SOCKSProxy = hp
		case "ftp":
			s.FTPProxy = hp
		}
	}
}

// parseWindowsBypass splits a WinINET bypass list (';'- or whitespace-separated).
func parseWindowsBypass(bypass string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(bypass, func(r rune) bool { return r == ';' || r == ' ' || r == '\t' || r == '\n' }) {
		if p == "" {
			continue
		}
		if strings.EqualFold(p, "<-loopback>") {
			out = append(out, "<-loopback>")
			continue
		}
		out = append(out, p)
	}
	return out
}
