package transport

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// The fuzz targets below parse untrusted server input (docs/REQUIREMENTS.md N5).
// Run one with: go test ./internal/transport -run '^$' -fuzz FuzzContentDisposition -fuzztime 20s

func FuzzContentDisposition(f *testing.F) {
	for _, s := range []string{
		`attachment; filename="foo.html"`,
		`attachment; filename*=UTF-8''%e2%82%ac%20rates`,
		`attachment; filename="EURO rates"; filename*=utf-8''%e2%82%ac%20rates`,
		`inline; filename="\"quoted\\".txt"`,
		`attachment; filename="unterminated`,
		`filename=no type; x="y"`,
		`attachment; filename*=iso-8859-1'en'%A3%20rates`,
		`attachment; filename="=?UTF-8?B?Zm9vLmh0bWw=?="`,
		`attachment; filename="%E4%B8%AD.txt"`,
		"attachment; filename=\"\xff\xfe.bin\"",
		`attachment; filename="../../CON.txt"`,
		`;;;=;"=\`,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, v string) {
		typ, name := ParseContentDisposition(v)
		if !utf8.ValidString(name) {
			t.Fatalf("ParseContentDisposition(%q) filename %q is not valid UTF-8", v, name)
		}
		if typ != strings.ToLower(typ) {
			t.Fatalf("ParseContentDisposition(%q) type %q is not lower case", v, typ)
		}
		if got := filenameFromDisposition(v); got != "" {
			checkSanitized(t, v, got)
		}
	})
}

func FuzzContentRange(f *testing.F) {
	for _, s := range []string{
		"bytes 0-499/1234", "bytes 0-499/*", "bytes */1234", "BYTES 1-1/2", "bytes=0-9/10",
		"bytes 9-0/10", "bytes 0-10/10", "bytes -1-2/3", "bytes 0-9223372036854775807/*", "",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, v string) {
		cr, err := ParseContentRange(v)
		if err != nil {
			return
		}
		if cr.Start < 0 {
			// Unsatisfied range "*/total".
			if cr.Start != -1 || cr.End != -1 || cr.Total < 0 {
				t.Fatalf("ParseContentRange(%q) = %+v: bad unsatisfied range", v, cr)
			}
		} else {
			if cr.End < cr.Start {
				t.Fatalf("ParseContentRange(%q) = %+v: end before start", v, cr)
			}
			if cr.Total != -1 && cr.End >= cr.Total {
				t.Fatalf("ParseContentRange(%q) = %+v: end beyond total", v, cr)
			}
			if cr.Total < -1 {
				t.Fatalf("ParseContentRange(%q) = %+v: negative total", v, cr)
			}
		}
		// Re-formatting the parsed value must parse back to the same range.
		again, err := ParseContentRange(formatContentRange(cr))
		if err != nil || again != cr {
			t.Fatalf("round trip of %+v: %+v, %v", cr, again, err)
		}
	})
}

func formatContentRange(cr ContentRange) string {
	total := "*"
	if cr.Total >= 0 {
		total = fmt.Sprint(cr.Total)
	}
	if cr.Start < 0 {
		return "bytes */" + total
	}
	return fmt.Sprintf("bytes %d-%d/%s", cr.Start, cr.End, total)
}

func FuzzSanitizeFilename(f *testing.F) {
	for _, s := range []string{
		"", ".", "..", "...", " ", "CON", "con.txt", "LPT1.tar.gz", "COM\xc2\xb9", "a<b>c",
		"report.pdf. . ", "\x00\x01", "\xff\xfe", "x\xe2\x80\xaey.exe", strings.Repeat("ä", 200) + ".txt",
		strings.Repeat("a", 300), "a." + strings.Repeat("b", 300), "_CON",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		out := SanitizeFilename(in)
		checkSanitized(t, in, out)
		if again := SanitizeFilename(out); again != out {
			t.Fatalf("SanitizeFilename is not idempotent: %q -> %q -> %q", in, out, again)
		}
	})
}

func FuzzFilenameFromURL(f *testing.F) {
	for _, s := range []string{
		"https://example.com/a/b.zip?x=1#y", "http://h/%E9%2F..", "example.com", "http://[::1]:80/x",
		"http://exa mple.com/bad%zz", "mailto:x@y", "https://h/%2e%2e/", "",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		checkSanitized(t, in, FilenameFromURL(in))
	})
}

func FuzzParseRetryAfter(f *testing.F) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, s := range []string{"120", "0", "99999999999999999999", "Fri, 02 Jan 2026 00:00:00 GMT", "-5", "1e3", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, v string) {
		if d := ParseRetryAfter(v, now); d < 0 || d > MaxRetryAfter {
			t.Fatalf("ParseRetryAfter(%q) = %v outside [0, %v]", v, d, MaxRetryAfter)
		}
	})
}
