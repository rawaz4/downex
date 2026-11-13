//go:build darwin && cgo

package mac

// [popups] The URL rules of a drop (ui/win's drop_data.go, the same reading).

import (
	"net/url"
	"os"
	"strings"
	"unicode/utf16"
)

func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// validURL is s when it parses as an absolute URL, else "".
func validURL(s string) string {
	if s == "" {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" || (u.Host == "" && u.Opaque == "" && u.Path == "") {
		return ""
	}
	return s
}

// maxShortcut bounds the Internet shortcut file read.
const maxShortcut = 64 << 10

// shortcutURL is the URL of an Internet shortcut (a .url file: its [InternetShortcut]
// section's URL=), or "".
func shortcutURL(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	b := make([]byte, maxShortcut)
	n, _ := f.Read(b)
	text := string(b[:n])
	if len(text) >= 2 && text[0] == 0xff && text[1] == 0xfe { // UTF-16 LE
		u := make([]uint16, 0, n/2)
		for i := 2; i+1 < n; i += 2 {
			u = append(u, uint16(b[i])|uint16(b[i+1])<<8)
		}
		text = string(utf16.Decode(u))
	}
	text = strings.TrimPrefix(text, string(rune(0xfeff)))
	in := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			in = strings.EqualFold(line, "[InternetShortcut]")
			continue
		}
		if !in {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok && strings.EqualFold(strings.TrimSpace(k), "URL") {
			return validURL(strings.TrimSpace(v))
		}
	}
	return ""
}
