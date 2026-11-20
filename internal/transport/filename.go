package transport

import (
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxFilenameBytes is the longest name SanitizeFilename returns, in UTF-8 bytes. It
// leaves room below the common 255-byte limit for the ".dxpart" suffix and " (n)".
const MaxFilenameBytes = 240

// fallbackFilename is used when nothing usable remains of a name.
const fallbackFilename = "download"

// Extension preservation limits for truncation: the final extension may be up to
// maxExtBytes long, and one short alphanumeric inner extension (".tar" in ".tar.gz")
// up to maxInnerExtBytes is kept with it.
const (
	maxExtBytes      = 16
	maxInnerExtBytes = 5
)

// FilenameFromURL returns a sanitised file name for rawURL: the last non-empty path
// segment, percent-decoded once (query and fragment ignored), else the host name,
// else "download". Decoded bytes that are not UTF-8 are read as ISO-8859-1.
func FilenameFromURL(rawURL string) string {
	seg, host := urlNameParts(rawURL)
	if name, ok := sanitizeFilename(seg); ok {
		return name
	}
	if name, ok := sanitizeFilename(host); ok {
		return name
	}
	return fallbackFilename
}

// urlNameParts returns the decoded last non-empty path segment and the host of rawURL.
// Unparsable URLs are split by hand so a stray "%" or space does not lose the name.
func urlNameParts(rawURL string) (segment, host string) {
	raw := strings.TrimSpace(rawURL)
	if u, err := url.Parse(raw); err == nil {
		return lastPathSegment(u.EscapedPath()), u.Hostname()
	}
	s, _, _ := strings.Cut(raw, "#")
	s, _, _ = strings.Cut(s, "?")
	if _, after, ok := strings.Cut(s, "://"); ok {
		var p string
		host, p, _ = strings.Cut(after, "/")
		if at := strings.LastIndexByte(host, '@'); at >= 0 {
			host = host[at+1:]
		}
		if h, _, ok := strings.Cut(host, ":"); ok && !strings.HasPrefix(host, "[") {
			host = h
		}
		s = p
	}
	return lastPathSegment(s), host
}

// RawFilenameFromURL returns the last non-empty path segment of rawURL, percent-decoded
// once, before sanitising: what the file check inspects for the hidden text-direction
// characters that SanitizeFilename removes (docs/DESKTOP-SPEC.md §9.5 N1).
func RawFilenameFromURL(rawURL string) string {
	seg, _ := urlNameParts(rawURL)
	return seg
}

// HiddenControls reports whether name holds bidirectional embeddings, overrides or
// isolates (U+202A–U+202E, U+2066–U+2069), which can make an extension look like
// another ("txt.exe" shown as "exe.txt"). The marks U+200E, U+200F and U+061C do not
// count: legitimate right-to-left names use them (docs/DESKTOP-SPEC.md §9.5 N1).
func HiddenControls(name string) bool {
	for _, r := range name {
		if r >= 0x202A && r <= 0x202E || r >= 0x2066 && r <= 0x2069 {
			return true
		}
	}
	return false
}

// lastPathSegment returns the last non-empty segment of an escaped path, unescaped once.
func lastPathSegment(escaped string) string {
	p := strings.TrimRight(escaped, "/")
	seg := p[strings.LastIndexByte(p, '/')+1:]
	raw, ok := percentDecode(seg)
	if !ok {
		return seg
	}
	if !utf8.Valid(raw) {
		return latin1ToUTF8(raw)
	}
	return string(raw)
}

// SanitizeFilename makes name safe as a single file name on Windows and Linux.
// It drops control and bidirectional-override characters, replaces <>:"/\|?* and
// invalid UTF-8 with "_", trims surrounding whitespace and trailing dots, prefixes
// Windows reserved device names (CON, PRN, AUX, NUL, COM0-9, LPT0-9, ...) with "_",
// and truncates to MaxFilenameBytes on a rune boundary while keeping the extension.
// The result is valid UTF-8 and never "", "." or ".."; when nothing usable remains it
// is "download".
func SanitizeFilename(name string) string {
	if s, ok := sanitizeFilename(name); ok {
		return s
	}
	return fallbackFilename
}

// mediaExtensions are the extensions MediaFileName replaces with the container's.
var mediaExtensions = map[string]bool{
	"m3u8": true, "m3u": true, "mpd": true, "mp4": true, "m4v": true, "m4a": true, "m4s": true, "ts": true,
	"mkv": true, "webm": true, "mov": true, "aac": true, "mp3": true,
}

// MediaExtension returns name's extension, lower-case, when it is one of a media file or
// manifest (".m3u8", ".mp4", ".webm"...), else "".
func MediaExtension(name string) string {
	i := strings.LastIndexByte(name, '.')
	if i <= 0 || i == len(name)-1 {
		return ""
	}
	if ext := strings.ToLower(name[i+1:]); mediaExtensions[ext] {
		return ext
	}
	return ""
}

// MediaFileName names a media download's output file from base (a requested name or a
// title): sanitised, without leading dots (no hidden files), with a media extension
// (MediaExtension) replaced by the container's (none for ""), and shortened so the
// extension and a " (n)" variant fit within MaxFilenameBytes; "video" names a base of which
// nothing usable remains. downexd names media and site downloads with it, and the window
// proposes the same name for another video of a page (docs/SITE-MEDIA-SPEC.md §3.12).
func MediaFileName(base, container string) string {
	base = strings.TrimLeft(SanitizeFilename(base), ".")
	if ext := MediaExtension(base); ext != "" && len(base) > len(ext)+1 {
		base = strings.TrimRight(base[:len(base)-len(ext)-1], ". ")
	}
	ext := ""
	if container != "" {
		ext = "." + container
	}
	if limit := MaxFilenameBytes - len(ext) - 12; len(base) > limit {
		base = truncateUTF8(base, limit)
	}
	if base = strings.TrimRight(base, ". "); base == "" {
		base = "video"
	}
	return SanitizeFilename(base + ext)
}

// sanitizeFilename is SanitizeFilename that reports false instead of falling back.
func sanitizeFilename(name string) (string, bool) {
	s := trimName(cleanRunes(name))
	s = trimName(truncateName(s, MaxFilenameBytes))
	if s == "" {
		return "", false
	}
	if isReservedName(s) {
		s = trimName(truncateName("_"+s, MaxFilenameBytes))
	}
	return s, true
}

// cleanRunes drops or replaces characters that are unsafe in a file name.
func cleanRunes(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for i := 0; i < len(name); {
		r, size := utf8.DecodeRuneInString(name[i:])
		i += size
		switch {
		case r == utf8.RuneError && size == 1: // invalid UTF-8 byte
			b.WriteByte('_')
		case strings.ContainsRune(`<>:"/\|?*`, r):
			b.WriteByte('_')
		case droppedRune(r):
			// removed
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// droppedRune reports characters removed from names: C0/C1 controls and DEL, line and
// paragraph separators, the BOM, noncharacters, and bidirectional formatting
// characters that can disguise an extension ("txt.exe" shown as "exe.txt").
func droppedRune(r rune) bool {
	switch {
	case unicode.IsControl(r):
		return true
	case r == 0x2028, r == 0x2029, r == 0xFEFF: // line/paragraph separator, BOM
		return true
	case r == 0x061C, r == 0x200E, r == 0x200F: // ALM, LRM, RLM
		return true
	case r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069: // embeddings, overrides, isolates
		return true
	case r >= 0xFDD0 && r <= 0xFDEF, r&0xFFFE == 0xFFFE: // noncharacters
		return true
	}
	return false
}

// trimName trims leading whitespace and trailing whitespace and dots (Windows silently
// strips the latter, which would make the name on disk differ from the one chosen).
func trimName(s string) string {
	s = strings.TrimLeftFunc(s, unicode.IsSpace)
	return strings.TrimRightFunc(s, func(r rune) bool { return r == '.' || unicode.IsSpace(r) })
}

// truncateName shortens a valid UTF-8 name to at most maxBytes, keeping its extension.
func truncateName(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	ext := nameExtension(s)
	stem := s[:len(s)-len(ext)]
	return truncateUTF8(stem, maxBytes-len(ext)) + ext
}

// nameExtension returns the extension kept on truncation: the final ".ext" (at most
// maxExtBytes) plus one short alphanumeric inner extension such as ".tar".
func nameExtension(s string) string {
	i := strings.LastIndexByte(s, '.')
	if i <= 0 || len(s)-i > maxExtBytes {
		return ""
	}
	ext := s[i:]
	stem := s[:i]
	if j := strings.LastIndexByte(stem, '.'); j > 0 {
		inner := stem[j+1:]
		if len(inner) > 0 && len(inner) <= maxInnerExtBytes && isASCIIAlnum(inner) {
			ext = s[j:]
		}
	}
	return ext
}

// truncateUTF8 cuts s to at most n bytes without splitting a rune.
func truncateUTF8(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// isReservedName reports Windows device names, which are reserved with or without an
// extension and regardless of case or trailing spaces before the first dot.
func isReservedName(s string) bool {
	stem, _, _ := strings.Cut(s, ".")
	stem = strings.TrimRight(stem, " ")
	for _, dev := range [...]string{"CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$", "CLOCK$"} {
		if strings.EqualFold(stem, dev) {
			return true
		}
	}
	if len(stem) < 4 {
		return false
	}
	prefix, suffix := stem[:3], stem[3:]
	if !strings.EqualFold(prefix, "COM") && !strings.EqualFold(prefix, "LPT") {
		return false
	}
	switch suffix {
	case "0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "¹", "²", "³":
		return true
	}
	return false
}

func isASCIIAlnum(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}
