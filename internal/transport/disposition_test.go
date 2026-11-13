package transport

import (
	"encoding/base64"
	"testing"
)

func TestFilenameFromDisposition(t *testing.T) {
	rfc2047 := "=?UTF-8?B?" + base64.StdEncoding.EncodeToString([]byte("foo-ä.html")) + "?="
	tests := []struct {
		name, header, want string
	}{
		// RFC 6266 §5 examples.
		{"rfc6266 token", `Attachment; filename=example.html`, "example.html"},
		{"rfc6266 quoted with space before value", `INLINE; FILENAME= "an example.html"`, "an example.html"},
		{"rfc6266 ext-value", `attachment; filename*= UTF-8''%e2%82%ac%20rates`, "€ rates"},
		{"rfc6266 ext-value preferred", `attachment; filename="EURO rates"; filename*=utf-8''%e2%82%ac%20rates`, "€ rates"},

		// Plain filename forms.
		{"inline type", `inline; filename="foo.html"`, "foo.html"},
		{"quoted pair escapes", `attachment; filename="\"quoting\" tested.html"`, "_quoting_ tested.html"},
		{"backslash escapes letters", `attachment; filename="f\oo.html"`, "foo.html"},
		{"semicolon inside quotes", `attachment; filename="Here's a semicolon;.html"`, "Here's a semicolon;.html"},
		{"other param with escapes first", `attachment; foo="\"\\";filename="foo.html"`, "foo.html"},
		{"uppercase param name", `attachment; FILENAME="foo.html"`, "foo.html"},
		{"spaces around equals", `attachment; filename = "foo.html"`, "foo.html"},
		{"unquoted with spaces", `attachment; filename=my report v2.pdf`, "my report v2.pdf"},
		{"unquoted with comma", `attachment; filename=foo,bar.html`, "foo,bar.html"},
		{"trailing semicolon and space", `attachment; filename=foo.html ;`, "foo.html"},
		{"unterminated quote", `attachment; filename="foo.html`, "foo.html"},
		{"unterminated quote before next param", `attachment; filename="foo.html; size=12`, "foo.html"},
		{"junk after closing quote", `attachment; filename="foo.html"bar; x=y`, "foo.html"},
		{"single quotes", `attachment; filename='foo.html'`, "foo.html"},
		{"missing type", `filename="foo.html"`, "foo.html"},
		{"missing semicolon after type", `attachment filename=foo.html`, "foo.html"},
		{"stray semicolons", `attachment;;; filename=foo.html`, "foo.html"},
		{"unknown type", `x-custom; filename=foo.html`, "foo.html"},
		{"first duplicate wins", `attachment; filename="a.txt"; filename="b.txt"`, "a.txt"},
		{"no filename", `attachment`, ""},
		{"empty filename", `attachment; filename=""`, ""},
		{"empty header", ``, ""},
		{"only dots", `attachment; filename="..."`, ""},

		// Character sets and encodings.
		{"raw utf-8", "attachment; filename=\"foo-\xc3\xa4.html\"", "foo-ä.html"},
		{"raw iso-8859-1", "attachment; filename=\"foo-\xe4.html\"", "foo-ä.html"},
		{"ext utf-8 lower-case", `attachment; filename*=utf-8''foo-%c3%a4.html`, "foo-ä.html"},
		{"ext iso-8859-1", `attachment; filename*=iso-8859-1''foo-%E4.html`, "foo-ä.html"},
		{"ext with language", `attachment; filename*=UTF-8'en-US'na%C3%AFve.txt`, "naïve.txt"},
		{"ext decoded once", `attachment; filename*=UTF-8''A-%2541.html`, "A-%41.html"},
		{"ext preferred when first", `attachment; filename*=UTF-8''%e2%82%ac.txt; filename="EUR.txt"`, "€.txt"},
		{"ext quoted leniently", `attachment; filename*="UTF-8''foo-%c3%a4.html"`, "foo-ä.html"},
		{"ext invalid utf-8 falls back", `attachment; filename*=UTF-8''foo-%E4.html; filename="fallback.html"`, "fallback.html"},
		{"ext bad escape falls back", `attachment; filename*=UTF-8''foo%zz.html; filename=bar.html`, "bar.html"},
		{"ext missing charset ignored", `attachment; filename*=''foo-%c3%a4.html`, ""},
		{"ext unknown charset falls back", `attachment; filename*=x-klingon''foo.html; filename=ok.html`, "ok.html"},
		{"rfc2231 continuations ignored", `attachment; filename*0="foo."; filename*1="html"`, ""},
		{"rfc2047 encoded word", `attachment; filename="` + rfc2047 + `"`, "foo-ä.html"},
		{"percent-encoded utf-8 in plain filename", `attachment; filename="%E4%B8%AD%E6%96%87.txt"`, "中文.txt"},
		{"ascii percent escapes stay literal", `attachment; filename="foo-%41.html"`, "foo-%41.html"},

		// Safety: directories, reserved names, controls, spoofing.
		{"strip unix directories", `attachment; filename="../../etc/passwd"`, "passwd"},
		{"strip windows directories", `attachment; filename="C:\\Users\\x\\evil.exe"`, "evil.exe"},
		{"encoded slash in ext-value", `attachment; filename*=UTF-8''..%2F..%2Fsecret.txt`, "secret.txt"},
		{"reserved device name", `attachment; filename="CON.txt"`, "_CON.txt"},
		{"control characters", "attachment; filename=\"a\x01b\x7fc.txt\"", "abc.txt"},
		{"bidi override removed", "attachment; filename*=UTF-8''invoice%E2%80%AEtxt.exe", "invoicetxt.exe"},
		{"forbidden characters", `attachment; filename="a<b>c:d|e?f*.txt"`, "a_b_c_d_e_f_.txt"},
		{"trailing dots and spaces", `attachment; filename="report.pdf. . "`, "report.pdf"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := filenameFromDisposition(tc.header); got != tc.want {
				t.Errorf("filenameFromDisposition(%q) = %q, want %q", tc.header, got, tc.want)
			}
		})
	}
}

func TestParseContentDisposition(t *testing.T) {
	tests := []struct {
		header, wantType, wantName string
	}{
		{`attachment; filename="a/b.txt"`, "attachment", "a/b.txt"}, // directories are the caller's to strip
		{`INLINE`, "inline", ""},
		{`Attachment ; filename=x`, "attachment", "x"},
		{`filename=x`, "", "x"},
		{`form-data; name="file"; filename="up.bin"`, "form-data", "up.bin"},
		{`attachment; name="filename"`, "attachment", ""},
	}
	for _, tc := range tests {
		typ, name := ParseContentDisposition(tc.header)
		if typ != tc.wantType || name != tc.wantName {
			t.Errorf("ParseContentDisposition(%q) = (%q, %q), want (%q, %q)", tc.header, typ, name, tc.wantType, tc.wantName)
		}
	}
}
