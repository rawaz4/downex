package validate

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The daemon checks the sign-in on its own (§6.3): at most 16 KiB of header, and a
// domain that is a DNS name holding the page, never an IP address or a public suffix.
func TestSiteCookie(t *testing.T) {
	ok := []struct{ page, domain string }{
		{"https://www.instagram.example/reel/abc/", ""},
		{"https://www.instagram.example/reel/abc/", "instagram.example"},
		{"https://www.instagram.example/reel/abc/", "www.instagram.example"},
		{"https://i.video.example.co.uk/v/1", "video.example.co.uk"},
		{"https://WWW.Video.Example:8443/v/1", "video.example"},
		{"https://player.vimeo.example./video/1", "vimeo.example"},
	}
	for _, c := range ok {
		if err := SiteCookie(c.page, "a=1", c.domain); err != nil {
			t.Errorf("SiteCookie(%q, %q) = %v", c.page, c.domain, err)
		}
	}
	bad := []struct{ page, domain string }{
		{"https://www.instagram.example/reel/abc/", "facebook.example"},  // another site
		{"https://www.instagram.example/reel/abc/", "stagram.example"},   // a suffix, not a parent
		{"https://fb.watch.example/abc/", "facebook.example"},            // a share link elsewhere
		{"https://www.instagram.example/reel/abc/", "example"},           // a public suffix (unknown TLDs count)
		{"https://www.video.example.co.uk/v/1", "co.uk"},                 // a public suffix
		{"https://user.github.io/v/1", "github.io"},                      // a private-section public suffix
		{"https://www.instagram.example/reel/abc/", "Instagram.example"}, // not lowercase
		{"https://www.instagram.example/reel/abc/", ".instagram.example"},
		{"https://www.instagram.example/reel/abc/", "instagram.example."},
		{"https://www.instagram.example/reel/abc/", "insta gram.example"},
		{"https://www.instagram.example/reel/abc/", "-x.instagram.example"},
		{"http://127.0.0.1:8080/v", "127.0.0.1"},
		{"http://[::1]:8080/v", "::1"},
		{"https://www.instagram.example/reel/abc/", strings.Repeat("a.", 127) + "example"},
		{"://bad", "example.com"},
	}
	for _, c := range bad {
		if err := SiteCookie(c.page, "a=1", c.domain); err == nil {
			t.Errorf("SiteCookie(%q, %q) accepted", c.page, c.domain)
		} else if c.domain != "" && strings.Contains(err.Error(), c.domain) {
			t.Errorf("the error repeats the domain: %v", err)
		}
	}
	if err := SiteCookie("https://www.instagram.example/", strings.Repeat("a", MaxSiteCookieBytes), ""); err != nil {
		t.Errorf("16 KiB of cookie refused: %v", err)
	}
	if err := SiteCookie("https://www.instagram.example/", strings.Repeat("a", MaxSiteCookieBytes+1), ""); err == nil {
		t.Error("more than 16 KiB of cookie accepted")
	}
}

// TestCuratedSignInDomains: every sign-in domain of the extension's site table
// (ext/src/lib/site-video.ts SIGN_IN_DOMAINS) is one the daemon accepts for a page on it,
// none a public suffix, so the extension never sends a domain downexd refuses.
func TestCuratedSignInDomains(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "ext", "src", "lib", "site-video.ts"))
	if err != nil {
		t.Skipf("the extension's sources are not here: %v", err)
	}
	block := regexp.MustCompile(`(?s)export const SIGN_IN_DOMAINS[^=]*= \{(.*?)\n\};`).FindSubmatch(src)
	if block == nil {
		t.Fatal("SIGN_IN_DOMAINS not found in site-video.ts")
	}
	domains := regexp.MustCompile(`'([^']+)'`).FindAllSubmatch(block[1], -1)
	if len(domains) < 28 {
		t.Fatalf("only %d domains read", len(domains))
	}
	for _, m := range domains {
		d := string(m[1])
		for _, page := range []string{"https://" + d + "/v/1", "https://www." + d + "/v/1"} {
			if err := SiteCookie(page, "a=1", d); err != nil {
				t.Errorf("SiteCookie(%q, %q) = %v", page, d, err)
			}
		}
	}
}
