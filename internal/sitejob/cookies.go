package sitejob

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// cookieFileName is the cookie file in a run's work folder.
const cookieFileName = "cookies.txt"

// cookieLines returns the Netscape cookie file for yt-dlp (docs/SITE-MEDIA-SPEC.md
// §4.7, §6.3) that holds the cookies of a Cookie header for pageURL: after the header
// line, one line per "name=value" pair whose name is a token and whose value is
// printable ASCII without ';', path "/", secure when the page is https, a session
// cookie. With a domain (checked by validate.SiteCookie) each is for ".domain" and its
// subdomains; without one, for the page's own host only. Other pairs are left out;
// nil when none remains.
func cookieLines(pageURL, header, domain string) []string {
	u, err := url.Parse(pageURL)
	if err != nil || u.Hostname() == "" {
		return nil
	}
	host, subdomains := strings.ToLower(u.Hostname()), "FALSE"
	if domain != "" {
		host, subdomains = "."+domain, "TRUE"
	}
	secure := "FALSE"
	if u.Scheme == "https" {
		secure = "TRUE"
	}
	var lines []string
	for pair := range strings.SplitSeq(header, ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(pair), "=")
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		if !ok || !token(name) || !cookieValue(value) {
			continue
		}
		lines = append(lines, strings.Join([]string{host, subdomains, "/", secure, "0", name, value}, "\t"))
	}
	if lines == nil {
		return nil
	}
	return append([]string{"# Netscape HTTP Cookie File"}, lines...)
}

// writeCookieFile writes the cookie file of a Cookie header into the run folder run,
// private to the user (createPrivate), and returns its path and how many cookies it
// holds; "" when the header holds no usable cookie. yt-dlp rewrites the file when it
// exits; the run folder is removed with it.
func writeCookieFile(run, pageURL, header, domain string) (string, int, error) {
	lines := cookieLines(pageURL, header, domain)
	if lines == nil {
		return "", 0, nil
	}
	path := filepath.Join(run, cookieFileName)
	f, err := createPrivate(path)
	if err != nil {
		return "", 0, fmt.Errorf("create the cookie file: %w", err)
	}
	_, err = f.WriteString(strings.Join(lines, "\n") + "\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(path)
		return "", 0, fmt.Errorf("write the cookie file: %w", err)
	}
	return path, len(lines) - 1, nil
}

// token reports a non-empty RFC 9110 token (a cookie name).
func token(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}

// cookieValue reports a cookie value of printable ASCII without ';' (the separator)
// or white space, which a cookie file line cannot carry.
func cookieValue(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c <= ' ' || c > '~' || c == ';' {
			return false
		}
	}
	return true
}
