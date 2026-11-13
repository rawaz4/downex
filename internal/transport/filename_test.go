package transport

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizeFilename(t *testing.T) {
	long := strings.Repeat("a", 300)
	longUTF8 := strings.Repeat("ä", 200) // 400 bytes
	tests := []struct {
		name, in, want string
	}{
		{"plain", "report.pdf", "report.pdf"},
		{"unicode kept", "Grüße 中文 😀.txt", "Grüße 中文 😀.txt"},
		{"forbidden characters", `a<b>c:d"e/f\g|h?i*j.txt`, "a_b_c_d_e_f_g_h_i_j.txt"},
		{"control characters dropped", "a\x00b\x1fc\x7fd\xc2\x85e.txt", "abcde.txt"},
		{"bidi and bom dropped", "x\xe2\x80\xaey\xe2\x81\xa6z\xef\xbb\xbf.txt", "xyz.txt"},
		{"invalid utf-8 replaced", "a\xffb.txt", "a_b.txt"},
		{"trailing dots and spaces", "name. . .", "name"},
		{"leading spaces", "   name.txt", "name.txt"},
		{"leading dot kept", ".config", ".config"},
		{"empty", "", "download"},
		{"dot", ".", "download"},
		{"dot dot", "..", "download"},
		{"only spaces", "   ", "download"},
		{"only controls", "\x01\x02", "download"},
		{"reserved CON", "CON", "_CON"},
		{"reserved lower-case with extension", "con.txt", "_con.txt"},
		{"reserved with double extension", "Nul.tar.gz", "_Nul.tar.gz"},
		{"reserved with space before dot", "AUX .txt", "_AUX .txt"},
		{"reserved COM1", "com1.log", "_com1.log"},
		{"reserved LPT9", "LPT9", "_LPT9"},
		{"reserved superscript", "COM\xc2\xb9.txt", "_COM\xc2\xb9.txt"},
		{"reserved CONIN$", "conin$", "_conin$"},
		{"reserved after trimming", "PRN...", "_PRN"},
		{"not reserved COM10", "COM10.txt", "COM10.txt"},
		{"not reserved CONSOLE", "console.txt", "console.txt"},
		{"not reserved prefix", "aux-data.bin", "aux-data.bin"},
		{"truncate keeps extension", long + ".iso", strings.Repeat("a", 236) + ".iso"},
		{"truncate keeps double extension", long + ".tar.gz", strings.Repeat("a", 233) + ".tar.gz"},
		{"truncate without extension", long, strings.Repeat("a", 240)},
		{"truncate overlong extension", "a." + long, "a." + strings.Repeat("a", 238)},
		{"truncate on rune boundary", longUTF8 + ".txt", strings.Repeat("ä", 118) + ".txt"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := SanitizeFilename(tc.in)
			if got != tc.want {
				t.Errorf("SanitizeFilename(%q) = %q, want %q", tc.in, got, tc.want)
			}
			checkSanitized(t, tc.in, got)
		})
	}
}

func TestFilenameFromURL(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"https://example.com/files/setup.exe", "setup.exe"},
		{"https://example.com/files/setup.exe?token=abc&x=1#frag", "setup.exe"},
		{"https://example.com/dl?file=foo.zip", "dl"},
		{"https://example.com/a%20b%2Bc.txt", "a b+c.txt"},
		{"https://example.com/na%C3%AFve.txt", "naïve.txt"},
		{"https://example.com/caf%E9.txt", "café.txt"},
		{"https://example.com/%2541.txt", "%41.txt"},
		{"https://example.com/dir%2Fname.txt", "dir_name.txt"},
		{"https://example.com/files/", "files"},
		{"https://example.com/", "example.com"},
		{"https://example.com", "example.com"},
		{"https://user:pw@example.com:8443/", "example.com"},
		{"https://example.com/CON", "_CON"},
		{"https://example.com/..", "example.com"},
		{"https://example.com/a/%2E%2E", "example.com"},
		{"http://exa mple.com/bad%zz name.txt?q=1", "bad%zz name.txt"},
		{"example.com/file.zip", "file.zip"},
		{"", "download"},
		{"mailto:someone@example.com", "download"},
		{"https://example.com/a:b*c?.txt", "a_b_c"},
	}
	for _, tc := range tests {
		got := FilenameFromURL(tc.in)
		if got != tc.want {
			t.Errorf("FilenameFromURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
		checkSanitized(t, tc.in, got)
	}
}

// checkSanitized asserts the SanitizeFilename output invariants.
func checkSanitized(t *testing.T, in, out string) {
	t.Helper()
	switch {
	case out == "" || out == "." || out == "..":
		t.Errorf("sanitize(%q) = %q: empty or dot name", in, out)
	case len(out) > MaxFilenameBytes:
		t.Errorf("sanitize(%q) = %d bytes > %d", in, len(out), MaxFilenameBytes)
	case !utf8.ValidString(out):
		t.Errorf("sanitize(%q) = %q: invalid UTF-8", in, out)
	case strings.ContainsAny(out, `<>:"/\|?*`):
		t.Errorf("sanitize(%q) = %q: forbidden character", in, out)
	case isReservedName(out):
		t.Errorf("sanitize(%q) = %q: reserved device name", in, out)
	case strings.HasSuffix(out, ".") || strings.HasSuffix(out, " "):
		t.Errorf("sanitize(%q) = %q: trailing dot or space", in, out)
	}
	for _, r := range out {
		if r < 0x20 || r == 0x7f || droppedRune(r) {
			t.Errorf("sanitize(%q) = %q: contains %U", in, out, r)
			break
		}
	}
}
