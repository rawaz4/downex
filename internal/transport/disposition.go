package transport

import (
	"mime"
	"strings"
	"unicode/utf8"
)

// ParseContentDisposition parses a Content-Disposition field value (RFC 6266).
//
// It returns the disposition type in lower case ("" when the value starts with a
// parameter) and the filename the sender suggested, decoded to UTF-8 but neither
// stripped of directories nor sanitised ("" when absent or undecodable). A decodable
// filename* parameter (RFC 8187 ext-value, UTF-8 or ISO-8859-1) is preferred over
// filename. Parameter names are case-insensitive and the first occurrence wins.
//
// Common malformed inputs are tolerated: a missing disposition type or ";", missing,
// single or unterminated quotes, whitespace around "=", stray semicolons, raw UTF-8 or
// ISO-8859-1 bytes, RFC 2047 encoded-words, and percent-encoded UTF-8 in filename.
// RFC 2231 continuations (filename*0=...) are not supported, as RFC 6266 §4.3 specifies.
func ParseContentDisposition(v string) (dispType, filename string) {
	dispType, params := splitDisposition(v)
	var plain, ext string
	var havePlain, haveExt bool
	for _, p := range params {
		switch p.name {
		case "filename*":
			if !haveExt {
				if s, ok := decodeExtValue(p.value); ok && s != "" {
					ext, haveExt = s, true
				}
			}
		case "filename":
			if !havePlain {
				plain, havePlain = p.value, true
			}
		}
	}
	switch {
	case haveExt:
		return dispType, ext
	case havePlain:
		return dispType, decodePlainFilename(plain)
	}
	return dispType, ""
}

// filenameFromDisposition returns the sanitised final path component of the
// Content-Disposition filename, or "" when there is none worth using.
func filenameFromDisposition(v string) string {
	if v == "" {
		return ""
	}
	_, name := ParseContentDisposition(v)
	// RFC 6266 §4.3: recipients must not let the name select a directory.
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	s, ok := sanitizeFilename(name)
	if !ok {
		return ""
	}
	return s
}

// dispParam is one name=value parameter; names are lower-cased, quoted values unescaped.
type dispParam struct {
	name, value string
}

// splitDisposition tokenises a Content-Disposition value into its type and parameters.
func splitDisposition(v string) (string, []dispParam) {
	n := len(v)
	i := skipOWS(v, 0)

	// The disposition type runs to the first ";". Reaching "=" first means the type is
	// missing and the value starts with a parameter.
	start := i
	for i < n && v[i] != ';' && v[i] != '=' {
		i++
	}
	dispType := ""
	if i < n && v[i] == '=' {
		i = start
	} else {
		dispType = strings.ToLower(trimOWS(v[start:i]))
	}

	var params []dispParam
	for i < n {
		for i < n && (v[i] == ';' || v[i] == ' ' || v[i] == '\t') {
			i++
		}
		if i >= n {
			break
		}
		start := i
		for i < n && v[i] != '=' && v[i] != ';' {
			i++
		}
		name := paramName(v[start:i])
		if i >= n || v[i] == ';' {
			continue // parameter without a value
		}
		i = skipOWS(v, i+1) // past "="
		var value string
		if i < n && v[i] == '"' {
			value, i = readQuoted(v, i+1)
			for i < n && v[i] != ';' { // ignore junk after the closing quote
				i++
			}
		} else {
			start := i
			for i < n && v[i] != ';' {
				i++
			}
			value = trimOWS(v[start:i])
		}
		if name != "" {
			params = append(params, dispParam{name: name, value: value})
		}
	}
	return dispType, params
}

// paramName lower-cases a parameter name. When a ";" is missing ("attachment filename=x")
// the last whitespace-separated word is the name.
func paramName(s string) string {
	f := strings.Fields(s)
	if len(f) == 0 {
		return ""
	}
	return strings.ToLower(f[len(f)-1])
}

// readQuoted reads a quoted-string body starting just after the opening quote and
// returns the unescaped text and the index after the closing quote. Without a closing
// quote the value ends at the next ";" (or the end of the field).
func readQuoted(v string, i int) (string, int) {
	var b strings.Builder
	for j := i; j < len(v); j++ {
		switch c := v[j]; c {
		case '"':
			return b.String(), j + 1
		case '\\':
			if j+1 < len(v) {
				j++
				b.WriteByte(v[j])
			}
		default:
			b.WriteByte(c)
		}
	}
	// Unterminated: re-read up to the next ";" so following parameters survive.
	end := strings.IndexByte(v[i:], ';')
	if end < 0 {
		end = len(v) - i
	}
	return unescapeQuotedPairs(v[i : i+end]), i + end
}

func unescapeQuotedPairs(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for j := 0; j < len(s); j++ {
		if s[j] == '\\' && j+1 < len(s) {
			j++
		}
		b.WriteByte(s[j])
	}
	return b.String()
}

// decodeExtValue decodes an RFC 8187 ext-value: charset "'" [language] "'" value-chars.
func decodeExtValue(v string) (string, bool) {
	charset, rest, ok := strings.Cut(trimOWS(v), "'")
	if !ok || charset == "" {
		return "", false
	}
	_, encoded, ok := strings.Cut(rest, "'") // skip the language tag
	if !ok {
		return "", false
	}
	raw, ok := percentDecode(encoded)
	if !ok {
		return "", false
	}
	switch strings.ToLower(charset) {
	case "utf-8", "utf8":
		if !utf8.Valid(raw) {
			return "", false
		}
		return string(raw), true
	case "iso-8859-1", "iso8859-1", "latin1", "us-ascii":
		return latin1ToUTF8(raw), true
	}
	return "", false
}

// decodePlainFilename turns a filename parameter value into UTF-8: RFC 2047
// encoded-words are decoded, bytes that are not UTF-8 are read as ISO-8859-1, and
// percent-encoded UTF-8 is decoded when that is unambiguous (see decodePercentUTF8).
func decodePlainFilename(s string) string {
	if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' {
		s = s[1 : len(s)-1]
	}
	if strings.Contains(s, "=?") && strings.Contains(s, "?=") {
		var dec mime.WordDecoder
		if d, err := dec.DecodeHeader(s); err == nil && utf8.ValidString(d) {
			return d
		}
	}
	if !utf8.ValidString(s) {
		return latin1ToUTF8([]byte(s))
	}
	return decodePercentUTF8(s)
}

// decodePercentUTF8 decodes %XX escapes in s only when every escape is well formed,
// at least one encodes a non-ASCII byte and the result is valid UTF-8. Names like
// "foo-%41.html" therefore stay literal while "%E4%B8%AD.txt" becomes "中.txt".
func decodePercentUTF8(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	raw, ok := percentDecode(s)
	if !ok || !utf8.Valid(raw) {
		return s
	}
	for _, c := range raw {
		if c >= 0x80 {
			return string(raw)
		}
	}
	return s
}

// percentDecode decodes %XX escapes; any other byte is copied. A "%" not followed by
// two hex digits fails.
func percentDecode(s string) ([]byte, bool) {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '%' {
			out = append(out, c)
			continue
		}
		if i+2 >= len(s) {
			return nil, false
		}
		hi, ok1 := unhex(s[i+1])
		lo, ok2 := unhex(s[i+2])
		if !ok1 || !ok2 {
			return nil, false
		}
		out = append(out, hi<<4|lo)
		i += 2
	}
	return out, true
}

func unhex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// latin1ToUTF8 maps each ISO-8859-1 byte to the code point of the same value.
func latin1ToUTF8(b []byte) string {
	var sb strings.Builder
	sb.Grow(len(b))
	for _, c := range b {
		sb.WriteRune(rune(c))
	}
	return sb.String()
}

func skipOWS(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return i
}
