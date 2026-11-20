// Package pageurl is the one rule for "Open page" (docs/DESKTOP-SPEC.md §10): which
// address of a download's page the window may hand to the system's browser. It is pure;
// the Windows and macOS backends open what it returns.
package pageurl

import (
	"errors"
	"net/url"
	"strings"
	"unicode"
)

// MaxBytes bounds a page address (the store's limit).
const MaxBytes = 32 << 10

// ErrNotPage is the answer for anything but an http or https address of a page.
var ErrNotPage = errors.New("not an http or https page address")

// Check returns raw re-serialised from its parsed form when it is an http or https
// address with a host, without a user name or password, of at most MaxBytes and without
// control or space characters; anything else is ErrNotPage. The scheme check keeps
// file:, shell: and settings addresses from ever reaching the shell.
func Check(raw string) (string, error) {
	if raw == "" || len(raw) > MaxBytes || strings.IndexFunc(raw, func(r rune) bool {
		return unicode.IsControl(r) || unicode.IsSpace(r)
	}) >= 0 {
		return "", ErrNotPage
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Host == "" || u.Hostname() == "" || u.Opaque != "" {
		return "", ErrNotPage
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return "", ErrNotPage
	}
	u.Scheme = strings.ToLower(u.Scheme)
	return u.String(), nil
}
