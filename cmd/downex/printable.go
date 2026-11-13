package main

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// printable returns s with every character that a terminal could interpret as a
// command replaced by a visible escape: C0 controls (including ESC, BEL, CR, LF and
// tab), DEL, C1 controls (8-bit CSI and OSC), bidirectional overrides and invalid UTF-8
// bytes. Text from the daemon, the database or a server (URLs, file names, directories,
// error messages, which can embed a certificate's names) passes through it before it is
// printed for people, so that it cannot rewrite the screen, the window title or the
// clipboard. JSON output is not affected: encoding/json escapes such characters itself.
func printable(s string) string {
	i := strings.IndexFunc(s, needsEscape)
	if i < 0 && utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for len(s) > 0 {
		r, n := utf8.DecodeRuneInString(s)
		switch {
		case r == utf8.RuneError && n == 1:
			fmt.Fprintf(&b, `\x%02x`, s[0])
		case needsEscape(r) && r < 0x100:
			fmt.Fprintf(&b, `\x%02x`, r)
		case needsEscape(r):
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteString(s[:n])
		}
		s = s[n:]
	}
	return b.String()
}

// needsEscape reports the characters printable escapes.
func needsEscape(r rune) bool {
	switch {
	case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
		return true
	case r == 0x200e, r == 0x200f, r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
		return true // directional marks, embeddings, overrides and isolates
	}
	return false
}

// printableArgs applies printable to the string and error arguments of a format call.
// args is not modified; a copy is returned when something had to be escaped.
func printableArgs(args []any) []any {
	out := args
	copied := false
	for i, a := range args {
		var s string
		switch v := a.(type) {
		case string:
			s = v
		case error:
			if v == nil {
				continue
			}
			s = v.Error()
		default:
			continue
		}
		if p := printable(s); p != s {
			if !copied {
				out, copied = append([]any(nil), args...), true
			}
			out[i] = p
		}
	}
	return out
}
