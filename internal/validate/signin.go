package validate

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	"golang.org/x/net/publicsuffix"
)

// MaxSiteCookieBytes bounds the Cookie header of the user's sign-in for a web page
// (docs/SITE-MEDIA-SPEC.md §6.3); the extension sends none larger.
const MaxSiteCookieBytes = 16 << 10

// maxDomainBytes is the longest DNS name.
const maxDomainBytes = 253

// ErrCookieDomain refuses a cookie domain that does not hold the page (§6.3).
var ErrCookieDomain = errors.New("cookie_domain: the page is not on that domain")

// CookieDomainName checks the syntax of the domain of a sign-in (§6.3): "" (none) or a
// lowercase ASCII DNS name of at most 253 bytes, dot-separated labels of 1-63 letters,
// digits and inner hyphens, without a trailing dot, that is not an IP address. Its
// errors never repeat the value.
func CookieDomainName(domain string) error {
	if domain == "" {
		return nil
	}
	if !dnsName(domain) || net.ParseIP(domain) != nil {
		return errors.New("cookie_domain is not a domain name")
	}
	return nil
}

// SiteCookie checks the sign-in of a web page request (docs/SITE-MEDIA-SPEC.md §6.3): a
// Cookie header of at most MaxSiteCookieBytes, and a cookie domain ("" = the page's
// exact host) that passes CookieDomainName, is not a public suffix (the public-suffix
// list, private section included) and is the page's host or a parent of it. Its errors
// never repeat the values.
func SiteCookie(pageURL, header, domain string) error {
	if len(header) > MaxSiteCookieBytes {
		return fmt.Errorf("the Cookie header is larger than %d bytes", MaxSiteCookieBytes)
	}
	if domain == "" {
		return nil
	}
	if err := CookieDomainName(domain); err != nil {
		return err
	}
	if suffix, _ := publicsuffix.PublicSuffix(domain); suffix == domain {
		return errors.New("cookie_domain is a public suffix")
	}
	u, err := url.Parse(pageURL)
	if err != nil {
		return ErrCookieDomain
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host != domain && !strings.HasSuffix(host, "."+domain) {
		return ErrCookieDomain
	}
	return nil
}

// dnsName reports a lowercase ASCII DNS name of at most maxDomainBytes.
func dnsName(s string) bool {
	if s == "" || len(s) > maxDomainBytes {
		return false
	}
	for label := range strings.SplitSeq(s, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}
