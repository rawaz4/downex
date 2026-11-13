package pac

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// Kind is a proxy type a PAC script can name.
type Kind uint8

// Proxy kinds returned by FindProxyForURL.
const (
	Direct Kind = iota
	HTTP        // "PROXY" or "HTTP"
	HTTPS       // "HTTPS"
	SOCKS       // "SOCKS" (SOCKS4) and "SOCKS4"
	SOCKS4A
	SOCKS5
)

// String names the kind as its PAC token.
func (k Kind) String() string {
	switch k {
	case HTTP:
		return "PROXY"
	case HTTPS:
		return "HTTPS"
	case SOCKS:
		return "SOCKS"
	case SOCKS4A:
		return "SOCKS4A"
	case SOCKS5:
		return "SOCKS5"
	}
	return "DIRECT"
}

// Result is one route named by a PAC script, in the order the script listed them.
type Result struct {
	Kind Kind
	Host string // "" for Direct
	Port string // "" for Direct
}

// parseResults parses a FindProxyForURL return value such as
// "PROXY proxy.example:8080; SOCKS5 socks.example:1080; DIRECT". Unknown tokens are
// skipped; an empty or all-unknown value is treated as DIRECT. A value with no valid
// entries at all returns an error only when it was non-empty and entirely malformed.
func parseResults(value string) ([]Result, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return []Result{{Kind: Direct}}, nil
	}
	var out []Result
	for part := range strings.SplitSeq(value, ";") {
		if len(out) == maxResults {
			break
		}
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		fields := strings.Fields(part)
		token := strings.ToUpper(fields[0])
		if token == "DIRECT" {
			out = append(out, Result{Kind: Direct})
			continue
		}
		if len(fields) < 2 {
			continue
		}
		var kind Kind
		switch token {
		case "PROXY", "HTTP":
			kind = HTTP
		case "HTTPS":
			kind = HTTPS
		case "SOCKS", "SOCKS4":
			kind = SOCKS
		case "SOCKS4A":
			kind = SOCKS4A
		case "SOCKS5":
			kind = SOCKS5
		default:
			continue
		}
		host, port, err := splitHostPort(fields[1], kind)
		if err != nil || !validResultHost(host) {
			continue
		}
		out = append(out, Result{Kind: kind, Host: host, Port: port})
	}
	if len(out) == 0 {
		// The value comes from the script: only its start goes into the error.
		return nil, fmt.Errorf("pac: no usable proxy in %q", strings.ToValidUTF8(value[:min(len(value), 64)], ""))
	}
	return out, nil
}

// splitHostPort parses "host:port" (or "host" with a scheme default port) from a PAC
// entry. PAC proxy entries may use a space instead of a colon in some legacy forms;
// only the colon form is accepted here.
func splitHostPort(hp string, kind Kind) (host, port string, err error) {
	hp = strings.TrimSpace(hp)
	if h, p, e := net.SplitHostPort(hp); e == nil {
		if !validPacPort(p) {
			return "", "", fmt.Errorf("pac: invalid port %q", p)
		}
		return h, p, nil
	}
	if strings.Contains(hp, ":") && !strings.HasPrefix(hp, "[") {
		return "", "", fmt.Errorf("pac: invalid host:port %q", hp)
	}
	switch kind {
	case HTTP:
		return hp, "80", nil
	case HTTPS:
		return hp, "443", nil
	default:
		return hp, "1080", nil
	}
}

func validPacPort(p string) bool {
	n, err := strconv.Atoi(p)
	return err == nil && n >= 1 && n <= 65535
}
